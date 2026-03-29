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
	pop3IdleTimeout  = 10 * time.Minute
	pop3MaxAuthFails = 3
)

// runPOP3Server starts a POP3 listener so mail clients can retrieve messages
// from the archive DB. It blocks until ctx is cancelled.
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

// pop3Msg holds envelope info for one message in the current session.
type pop3Msg struct {
	id      string
	size    int64
	deleted bool
}

// pop3Session handles a single POP3 client connection.
type pop3Session struct {
	conn      net.Conn
	userStore *store.SQLiteStore
	archive   *archive.SQLStore
	cfg       config

	// set after successful authentication
	userEmail string
	messages  []pop3Msg
}

const (
	stateAuth = iota
	stateTxn
)

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

		// ── AUTHORIZATION state ──────────────────────────────────────────────
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

		// ── TRANSACTION state ─────────────────────────────────────────────────
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
				// TOP msg n — headers + blank line + n body lines
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
				// UPDATE state: commit deletions
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

// authenticate checks the given plaintext password against the stored bcrypt hash.
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

// loadMailbox fetches all messages in the user's inbound mailbox into memory.
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
		// No inbox yet — empty maildrop is valid.
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

// commitDeletions deletes all messages marked for deletion from the DB.
func (s *pop3Session) commitDeletions(ctx context.Context) {
	for _, m := range s.messages {
		if m.deleted {
			if err := s.archive.DeleteMessage(ctx, m.id); err != nil {
				log.Printf("pop3 delete message %s: %v", m.id, err)
			}
		}
	}
}

// stat returns the count and total byte size of non-deleted messages.
func (s *pop3Session) stat() (count int, total int64) {
	for _, m := range s.messages {
		if !m.deleted {
			count++
			total += m.size
		}
	}
	return
}

// parseIndex parses a 1-based message number and returns the 0-based index.
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

func (s *pop3Session) ok(rw *bufio.ReadWriter, format string, args ...any) {
	if format == "" {
		fmt.Fprintf(rw, "+OK\r\n") //nolint:errcheck
		return
	}
	fmt.Fprintf(rw, "+OK "+format+"\r\n", args...) //nolint:errcheck
}

func (s *pop3Session) err(rw *bufio.ReadWriter, format string, args ...any) {
	fmt.Fprintf(rw, "-ERR "+format+"\r\n", args...) //nolint:errcheck
}

// writePOP3DotData writes data followed by the dot terminator, applying dot-stuffing.
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

// topLines returns the header section + a blank line + at most n body lines.
func topLines(raw []byte, n int) []byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// Find the header/body separator (first blank line).
	sepIdx := strings.Index(text, "\n\n")
	if sepIdx < 0 {
		// No body — return full text.
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
