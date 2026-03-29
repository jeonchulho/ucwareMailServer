package app

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	// 유휴 연결 타임아웃. 클라이언트가 오랫동안 명령을 보내지 않으면 세션을 종료한다.
	pop3IdleTimeout = 10 * time.Minute
	// 인증 실패 허용 횟수. 초과 시 연결을 종료해 무차별 대입 시도를 완화한다.
	pop3MaxAuthFails = 3
)

// runPOP3Server 는 POP3 TCP 리스너를 시작해 메일 클라이언트가 아카이브 DB 메시지를
// 조회/다운로드할 수 있게 한다. ctx 취소 시 리스너를 닫고 정상 종료한다.
func runPOP3Server(ctx context.Context, cfg config, userStore *store.SQLiteStore, archiveStore *archive.SQLStore) error {
	ln, err := net.Listen("tcp", cfg.POP3Addr)
	if err != nil {
		return fmt.Errorf("pop3 listen %s: %w", cfg.POP3Addr, err)
	}
	log.Printf("pop3 server listening on %s", cfg.POP3Addr)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("pop3 accept: %v", err)
				continue
			}
		}
		go (&pop3Session{
			conn:      conn,
			userStore: userStore,
			archive:   archiveStore,
			cfg:       cfg,
		}).serve(ctx)
	}
}

// pop3Msg 는 현재 POP3 세션에서 메일 1건의 최소 메타데이터를 담는다.
// deleted=true 는 DB 즉시 삭제가 아니라 QUIT 시점 커밋 대상임을 의미한다.
type pop3Msg struct {
	id      string
	size    int64
	deleted bool
}

// pop3Session 은 단일 POP3 클라이언트 연결의 상태를 관리한다.
// POP3 상태 머신(AUTHORIZATION -> TRANSACTION)을 이 구조체가 유지한다.
type pop3Session struct {
	conn      net.Conn
	userStore *store.SQLiteStore
	archive   *archive.SQLStore
	cfg       config

	// 인증 성공 후 설정되는 세션 사용자 정보
	userEmail string
	// 현재 메일드롭 스냅샷(세션 시작 시 로드)
	messages []pop3Msg
}

const (
	// 인증 전 상태(USER/PASS/QUIT 허용)
	stateAuth = iota
	// 인증 후 트랜잭션 상태(STAT/LIST/RETR/DELE 등 허용)
	stateTxn
)

// serve 는 POP3 세션 메인 루프로, 명령 파싱/상태 전이/응답 작성을 처리한다.
// 소켓은 유휴 타임아웃을 주기적으로 갱신해 장시간 유휴 연결을 정리한다.
func (s *pop3Session) serve(ctx context.Context) {
	defer s.conn.Close()
	s.conn.SetDeadline(time.Now().Add(pop3IdleTimeout)) //nolint:errcheck

	rw := bufio.NewReadWriter(bufio.NewReaderSize(s.conn, 4096), bufio.NewWriter(s.conn))
	s.ok(rw, "POP3 server ready")
	rw.Flush() //nolint:errcheck

	state := stateAuth
	pendingUser := ""
	authFails := 0

	for {
		s.conn.SetDeadline(time.Now().Add(pop3IdleTimeout)) //nolint:errcheck
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(strings.TrimSpace(verb))
		arg = strings.TrimSpace(arg)

		switch state {

		// ── AUTHORIZATION 상태 ───────────────────────────────────────────────
		case stateAuth:
			switch verb {
			case "USER":
				if arg == "" {
					s.err(rw, "missing username")
				} else {
					pendingUser = strings.ToLower(arg)
					s.ok(rw, "")
				}

			case "PASS":
				if pendingUser == "" {
					s.err(rw, "USER required first")
				} else {
					if authErr := s.authenticate(ctx, pendingUser, arg); authErr != nil {
						authFails++
						pendingUser = ""
						log.Printf("pop3 auth failure for %s (attempt %d)", pendingUser, authFails)
						if authFails >= pop3MaxAuthFails {
							s.err(rw, "too many authentication failures")
							rw.Flush() //nolint:errcheck
							return
						}
						s.err(rw, "invalid credentials")
					} else {
						s.userEmail = pendingUser
						pendingUser = ""
						if loadErr := s.loadMailbox(ctx); loadErr != nil {
							log.Printf("pop3 load mailbox for %s: %v", s.userEmail, loadErr)
							s.err(rw, "maildrop access failed")
							rw.Flush() //nolint:errcheck
							return
						}
						state = stateTxn
						s.ok(rw, "maildrop ready, %d message(s)", len(s.messages))
					}
				}

			case "QUIT":
				s.ok(rw, "bye")
				rw.Flush() //nolint:errcheck
				return

			default:
				s.err(rw, "unknown command")
			}

		// ── TRANSACTION 상태 ──────────────────────────────────────────────────
		case stateTxn:
			switch verb {
			case "STAT":
				n, total := s.stat()
				s.ok(rw, "%d %d", n, total)

			case "LIST":
				if arg == "" {
					n, total := s.stat()
					s.ok(rw, "%d messages (%d octets)", n, total)
					for i, m := range s.messages {
						if !m.deleted {
							fmt.Fprintf(rw, "%d %d\r\n", i+1, m.size) //nolint:errcheck
						}
					}
					fmt.Fprintf(rw, ".\r\n") //nolint:errcheck
				} else {
					idx, ok := s.parseIndex(arg)
					if !ok {
						s.err(rw, "no such message")
					} else {
						s.ok(rw, "%d %d", idx+1, s.messages[idx].size)
					}
				}

			case "UIDL":
				if arg == "" {
					s.ok(rw, "")
					for i, m := range s.messages {
						if !m.deleted {
							fmt.Fprintf(rw, "%d %s\r\n", i+1, m.id) //nolint:errcheck
						}
					}
					fmt.Fprintf(rw, ".\r\n") //nolint:errcheck
				} else {
					idx, ok := s.parseIndex(arg)
					if !ok {
						s.err(rw, "no such message")
					} else {
						s.ok(rw, "%d %s", idx+1, s.messages[idx].id)
					}
				}

			case "RETR":
				idx, ok := s.parseIndex(arg)
				if !ok {
					s.err(rw, "no such message")
				} else {
					msg, fetchErr := s.archive.GetMessage(ctx, s.messages[idx].id)
					if fetchErr != nil {
						log.Printf("pop3 RETR %s: %v", s.messages[idx].id, fetchErr)
						s.err(rw, "fetch error")
					} else {
						s.ok(rw, "%d octets", msg.SizeBytes)
						writePOP3DotData(rw, []byte(msg.RawMIME))
					}
				}

			case "TOP":
				// TOP msg n: 헤더 + 빈 줄 + 본문 n줄만 반환
				parts := strings.SplitN(arg, " ", 2)
				if len(parts) != 2 {
					s.err(rw, "syntax: TOP msg n")
				} else {
					idx, okIdx := s.parseIndex(parts[0])
					nLines, errN := strconv.Atoi(strings.TrimSpace(parts[1]))
					if !okIdx || errN != nil || nLines < 0 {
						s.err(rw, "invalid arguments")
					} else {
						msg, fetchErr := s.archive.GetMessage(ctx, s.messages[idx].id)
						if fetchErr != nil {
							log.Printf("pop3 TOP %s: %v", s.messages[idx].id, fetchErr)
							s.err(rw, "fetch error")
						} else {
							s.ok(rw, "")
							writePOP3DotData(rw, topLines([]byte(msg.RawMIME), nLines))
						}
					}
				}

			case "DELE":
				idx, ok := s.parseIndex(arg)
				if !ok {
					s.err(rw, "no such message")
				} else {
					s.messages[idx].deleted = true
					s.ok(rw, "message %d deleted", idx+1)
				}

			case "RSET":
				for i := range s.messages {
					s.messages[i].deleted = false
				}
				n, total := s.stat()
				s.ok(rw, "maildrop has %d messages (%d octets)", n, total)

			case "NOOP":
				s.ok(rw, "")

			case "QUIT":
				// UPDATE 상태: DELE 표시된 메시지를 실제 DB에서 삭제 커밋
				s.commitDeletions(ctx)
				s.ok(rw, "bye")
				rw.Flush() //nolint:errcheck
				return

			default:
				s.err(rw, "unknown command")
			}
		}

		rw.Flush() //nolint:errcheck
	}
}

// authenticate 는 입력한 평문 비밀번호를 저장된 bcrypt 해시와 비교해 인증한다.
func (s *pop3Session) authenticate(ctx context.Context, email, password string) error {
	users, err := s.userStore.ListUsersByEmail(ctx, email)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return fmt.Errorf("user not found")
	}
	return bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte(password))
}

// loadMailbox 는 사용자 인바운드 메일박스의 메시지 목록을 세션 메모리로 로드한다.
// 메일박스가 아직 없으면 오류가 아니라 빈 메일드롭으로 간주한다.
func (s *pop3Session) loadMailbox(ctx context.Context) error {
	boxes, err := s.archive.ListMailboxes(ctx, s.userEmail)
	if err != nil {
		return err
	}
	var mailboxID string
	inboundName := s.cfg.ArchiveInboundMailbox
	for _, b := range boxes {
		if strings.EqualFold(b.Name, inboundName) {
			mailboxID = b.ID
			break
		}
	}
	if mailboxID == "" {
		// 인박스 미생성 상태는 유효하므로 빈 목록으로 처리
		s.messages = nil
		return nil
	}
	msgs, err := s.archive.ListMessages(ctx, mailboxID, 1000)
	if err != nil {
		return err
	}
	s.messages = make([]pop3Msg, len(msgs))
	for i, m := range msgs {
		s.messages[i] = pop3Msg{id: m.ID, size: m.SizeBytes}
	}
	return nil
}

// commitDeletions 는 DELE로 삭제 표시된 메시지를 DB에서 실제 삭제한다.
// POP3 규약상 QUIT 시점(UPDATE 상태)에 반영한다.
func (s *pop3Session) commitDeletions(ctx context.Context) {
	for _, m := range s.messages {
		if m.deleted {
			if err := s.archive.DeleteMessage(ctx, m.id); err != nil {
				log.Printf("pop3 delete message %s: %v", m.id, err)
			}
		}
	}
}

// stat 은 삭제되지 않은 메시지 개수와 총 바이트 크기를 계산한다.
func (s *pop3Session) stat() (count int, total int64) {
	for _, m := range s.messages {
		if !m.deleted {
			count++
			total += m.size
		}
	}
	return
}

// parseIndex 는 POP3 1-based 메시지 번호를 내부 0-based 인덱스로 변환한다.
// 범위 밖 번호이거나 이미 DELE 처리된 메시지는 false를 반환한다.
func (s *pop3Session) parseIndex(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > len(s.messages) {
		return 0, false
	}
	idx := n - 1
	if s.messages[idx].deleted {
		return 0, false
	}
	return idx, true
}

// ok 는 POP3 성공 응답(+OK)을 작성한다.
// format이 비어 있으면 상태라인만 보낸다.
func (s *pop3Session) ok(rw *bufio.ReadWriter, format string, args ...any) {
	if format == "" {
		fmt.Fprintf(rw, "+OK\r\n") //nolint:errcheck
		return
	}
	fmt.Fprintf(rw, "+OK "+format+"\r\n", args...) //nolint:errcheck
}

// err 는 POP3 오류 응답(-ERR)을 작성한다.
func (s *pop3Session) err(rw *bufio.ReadWriter, format string, args ...any) {
	fmt.Fprintf(rw, "-ERR "+format+"\r\n", args...) //nolint:errcheck
}

// writePOP3DotData 는 POP3 멀티라인 응답 규약에 맞춰 본문을 기록한다.
// 줄 시작이 '.'인 경우 dot-stuffing(점 하나 추가)을 적용하고, 마지막에 "." 종료줄을 붙인다.
func writePOP3DotData(rw *bufio.ReadWriter, data []byte) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, ".") {
			fmt.Fprintf(rw, ".%s\r\n", line) //nolint:errcheck
		} else {
			fmt.Fprintf(rw, "%s\r\n", line) //nolint:errcheck
		}
	}
	fmt.Fprintf(rw, ".\r\n") //nolint:errcheck
}

// topLines 는 TOP 명령용으로 "헤더 + 빈 줄 + 본문 최대 n줄"을 반환한다.
// 헤더/본문 구분선이 없으면 원본을 그대로 반환한다.
func topLines(raw []byte, n int) []byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// 헤더/본문 구분(첫 빈 줄) 위치를 찾는다.
	sepIdx := strings.Index(text, "\n\n")
	if sepIdx < 0 {
		// 본문 구분이 없으면 원문 그대로 반환
		return raw
	}
	header := text[:sepIdx+2] // includes the blank line
	body := text[sepIdx+2:]

	if n == 0 {
		return []byte(header)
	}

	bodyLines := strings.SplitN(body, "\n", n+1)
	if len(bodyLines) > n {
		bodyLines = bodyLines[:n]
	}
	return []byte(header + strings.Join(bodyLines, "\n"))
}
