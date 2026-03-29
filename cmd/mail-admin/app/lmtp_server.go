package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
)

const (
	lmtpMaxRcpts    = 100             // 한 트랜잭션에서 허용하는 최대 수신자 수 (RFC 5321 권고)
	lmtpIdleTimeout = 5 * time.Minute // 명령어 입력 대기 및 DATA 수신 최대 유휴 시간
)

// runLMTPServer 는 MTA(예: Postfix)로부터 메일을 수신해 아카이브 DB에 저장하는
// LMTP 리스너를 시작합니다. ctx가 취소될 때까지 블로킹됩니다.
func runLMTPServer(ctx context.Context, cfg config, archiveStore *archive.SQLStore) error {
	ln, err := net.Listen("tcp", cfg.LMTPAddr)
	if err != nil {
		return fmt.Errorf("lmtp listen %s: %w", cfg.LMTPAddr, err)
	}
	log.Printf("lmtp server listening on %s", cfg.LMTPAddr)

	go func() {
		<-ctx.Done()
		ln.Close() // ctx 취소 시 Accept 블로킹 해제
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("lmtp accept: %v", err)
				continue
			}
		}
		go (&lmtpSession{
			conn:    conn,
			archive: archiveStore,
			cfg:     cfg,
		}).serve(ctx) // 클라이언트마다 고루틴을 생성해 동시 처리
	}
}

// lmtpSession 은 단일 LMTP 클라이언트 연결의 상태를 보관합니다.
// SMTP 명령어 파싱 → 본문 수신 → 아카이브 저장까지 한 세션을 담당합니다.
type lmtpSession struct {
	conn    net.Conn          // 클라이언트 TCP 연결
	archive *archive.SQLStore // 메시지를 저장할 아카이브 DB
	cfg     config
	from    string   // MAIL FROM 주소 (현재 트랜잭션)
	rcpts   []string // RCPT TO 주소 목록 (현재 트랜잭션)
}

func (s *lmtpSession) serve(ctx context.Context) {
	defer s.conn.Close()
	s.conn.SetDeadline(time.Now().Add(lmtpIdleTimeout)) //nolint:errcheck
	maxMessageBytes := s.cfg.LMTPMaxMessageBytes
	if maxMessageBytes <= 0 {
		maxMessageBytes = 50 * 1024 * 1024 // 설정 누락 시 50 MB 기본값
	}

	rw := bufio.NewReadWriter(bufio.NewReaderSize(s.conn, 4096), bufio.NewWriter(s.conn))
	// LMTP 세션 시작 배너 전송
	s.reply(rw, "220 %s LMTP ready", s.domain())
	rw.Flush() //nolint:errcheck

	for {
		s.conn.SetDeadline(time.Now().Add(lmtpIdleTimeout)) //nolint:errcheck
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(strings.TrimSpace(verb))

		switch verb {
		case "LHLO":
			// LHLO는 SMTP의 EHLO에 해당 — LMTP 확장 기능 목록을 선언
			s.reply(rw, "250-%s", s.domain())
			s.reply(rw, "250-SIZE %d", maxMessageBytes)
			s.reply(rw, "250 8BITMIME")

		case "MAIL":
			// 새 메일 트랜잭션 시작: 발신자 주소 저장 및 수신자 목록 초기화
			s.from = extractLMTPAngle(arg)
			s.rcpts = nil
			s.reply(rw, "250 Ok")

		case "RCPT":
			if len(s.rcpts) >= lmtpMaxRcpts {
				s.reply(rw, "452 Too many recipients")
			} else {
				// 수신자 주소를 목록에 추가
				s.rcpts = append(s.rcpts, extractLMTPAngle(arg))
				s.reply(rw, "250 Ok")
			}

		case "DATA":
			if s.from == "" {
				s.reply(rw, "503 Need MAIL command first")
			} else if len(s.rcpts) == 0 {
				s.reply(rw, "503 Need RCPT command first")
			} else {
				s.reply(rw, "354 End data with <CR><LF>.<CR><LF>")
				rw.Flush()                                          //nolint:errcheck
				s.conn.SetDeadline(time.Now().Add(lmtpIdleTimeout)) //nolint:errcheck

				// dot-stuffed 본문을 maxMessageBytes 한도 내에서 수신
				raw, readErr := readLMTPDotData(rw.Reader, maxMessageBytes)

				// 수신자 및 발신자를 스냅샷한 후 엔벨로프를 초기화 (RSET 효과)
				from := s.from
				rcpts := s.rcpts
				s.from = ""
				s.rcpts = nil

				if readErr != nil {
					log.Printf("lmtp read data: %v", readErr)
					for range rcpts {
						s.reply(rw, "552 Message too large or read error")
					}
				} else {
					// MIME 파싱으로 제목과 본문을 추출
					subject, textBody := parseLMTPMIME(raw)
					receivedAt := time.Now().UTC()
					// LMTP는 수신자별로 독립 응답을 반환 (SMTP와의 차이점)
					for _, rcpt := range rcpts {
						if delivErr := s.deliverOne(ctx, from, raw, subject, textBody, rcpt, receivedAt); delivErr != nil {
							log.Printf("lmtp deliver to %s: %v", rcpt, delivErr)
							s.reply(rw, "451 Temporary delivery failure")
						} else {
							s.reply(rw, "250 Ok: delivered to %s", rcpt)
						}
					}
				}
			}

		case "RSET":
			// 현재 트랜잭션 초기화 (발신자·수신자 목록 리셋)
			s.from = ""
			s.rcpts = nil
			s.reply(rw, "250 Ok")

		case "NOOP":
			s.reply(rw, "250 Ok")

		case "QUIT":
			s.reply(rw, "221 Bye")
			rw.Flush() //nolint:errcheck
			return

		default:
			s.reply(rw, "500 Command unrecognized")
		}

		rw.Flush() //nolint:errcheck
	}
}

func (s *lmtpSession) domain() string {
	if s.cfg.LMTPDomain != "" {
		return s.cfg.LMTPDomain
	}
	return "localhost"
}

func (s *lmtpSession) reply(rw *bufio.ReadWriter, format string, args ...any) {
	fmt.Fprintf(rw, format+"\r\n", args...) //nolint:errcheck
}

// deliverOne 는 단일 수신자에 대해 메시지 한 부를 inbound 메일박스에 저장합니다.
func (s *lmtpSession) deliverOne(ctx context.Context, from string, raw []byte, subject, textBody, rcpt string, receivedAt time.Time) error {
	mailboxID, err := s.findOrCreateMailbox(ctx, rcpt, s.cfg.ArchiveInboundMailbox)
	if err != nil {
		return err
	}
	_, err = s.archive.CreateMessage(ctx, archive.CreateMessageInput{
		MailboxID:  mailboxID,
		Direction:  "inbound",
		FromAddr:   from,
		ToAddr:     rcpt,
		Subject:    subject,
		RawMIME:    string(raw),
		TextBody:   textBody,
		SizeBytes:  int64(len(raw)),
		ReceivedAt: receivedAt,
	})
	return err
}

// findOrCreateMailbox 는 userEmail 의 name 메일박스 ID를 반환합니다.
// 존재하지 않으면 생성하며, 동시 생성 경쟁 조건은 두 번째 조회로 처리합니다.
func (s *lmtpSession) findOrCreateMailbox(ctx context.Context, userEmail, name string) (string, error) {
	boxes, err := s.archive.ListMailboxes(ctx, userEmail)
	if err != nil {
		return "", fmt.Errorf("list mailboxes for %s: %w", userEmail, err)
	}
	for _, b := range boxes {
		if strings.EqualFold(b.Name, name) {
			return b.ID, nil
		}
	}
	box, err := s.archive.CreateMailbox(ctx, userEmail, name)
	if err != nil {
		// 경쟁 조건: 다른 고루틴이 먼저 생성했을 수 있으므로 목록을 한 번 더 조회
		boxes2, err2 := s.archive.ListMailboxes(ctx, userEmail)
		if err2 != nil {
			return "", fmt.Errorf("create mailbox for %s: %w", userEmail, err)
		}
		for _, b := range boxes2 {
			if strings.EqualFold(b.Name, name) {
				return b.ID, nil
			}
		}
		return "", fmt.Errorf("create mailbox for %s: %w", userEmail, err)
	}
	return box.ID, nil
}

// extractLMTPAngle 은 "FROM:<addr>" 또는 "TO:<addr>" 구문에서 이메일 주소를 추출합니다.
func extractLMTPAngle(s string) string {
	i := strings.IndexByte(s, '<')
	j := strings.LastIndexByte(s, '>')
	if i >= 0 && j > i {
		return strings.TrimSpace(s[i+1 : j])
	}
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		return strings.TrimSpace(s[idx+1:])
	}
	return strings.TrimSpace(s)
}

// readLMTPDotData 는 SMTP/LMTP dot-stuffed DATA를 "." 종료 마커까지 읽습니다.
// maxBytes를 초과하면 오류를 반환하여 메모리 과부하를 방지합니다.
func readLMTPDotData(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		// dot-stuffing 해제: ".." 으로 시작하는 줄은 첫 "." 제거
		if len(line) >= 2 && line[0] == '.' && line[1] == '.' {
			line = line[1:]
		}
		// 단독 "." 줄이 DATA 종료 마커
		if bytes.Equal(bytes.TrimRight(line, "\r\n"), []byte(".")) {
			break
		}
		buf = append(buf, line...)
		if len(buf) > maxBytes {
			return nil, fmt.Errorf("message exceeds %d bytes", maxBytes)
		}
	}
	return buf, nil
}

// parseLMTPMIME 는 raw RFC 5322 메시지에서 Subject 헤더와
// 첫 번째 text/plain 파트의 본문을 추출합니다.
func parseLMTPMIME(raw []byte) (subject, textBody string) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}

	dec := new(mime.WordDecoder)
	subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))

	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	mediaType, params, _ := mime.ParseMediaType(ct)

	switch {
	case strings.HasPrefix(mediaType, "text/plain"):
		b, _ := io.ReadAll(msg.Body)
		textBody = string(b)

	case strings.HasPrefix(mediaType, "multipart/"):
		if boundary := params["boundary"]; boundary != "" {
			mr := multipart.NewReader(msg.Body, boundary)
			for {
				part, partErr := mr.NextPart()
				if partErr != nil {
					break
				}
				partMedia, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
				if strings.HasPrefix(partMedia, "text/plain") {
					b, _ := io.ReadAll(part)
					textBody = string(b)
					break
				}
			}
		}
	}

	return subject, textBody
}
