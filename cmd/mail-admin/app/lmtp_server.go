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
	lmtpMaxMessageBytes = 50 * 1024 * 1024 // 50 MB
	lmtpMaxRcpts        = 100
	lmtpIdleTimeout     = 5 * time.Minute
)

// runLMTPServer starts an LMTP listener that accepts mail deliveries from an MTA
// (e.g. Postfix) and writes each message into the archive DB.
// It blocks until ctx is cancelled.
func runLMTPServer(ctx context.Context, cfg config, archiveStore *archive.SQLStore) error {
	ln, err := net.Listen("tcp", cfg.LMTPAddr)
	if err != nil {
		return fmt.Errorf("lmtp listen %s: %w", cfg.LMTPAddr, err)
	}
	log.Printf("lmtp server listening on %s", cfg.LMTPAddr)

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
				log.Printf("lmtp accept: %v", err)
				continue
			}
		}
		go (&lmtpSession{
			conn:    conn,
			archive: archiveStore,
			cfg:     cfg,
		}).serve(ctx)
	}
}

type lmtpSession struct {
	conn    net.Conn
	archive *archive.SQLStore
	cfg     config
	from    string
	rcpts   []string
}

func (s *lmtpSession) serve(ctx context.Context) {
	defer s.conn.Close()
	s.conn.SetDeadline(time.Now().Add(lmtpIdleTimeout)) //nolint:errcheck

	rw := bufio.NewReadWriter(bufio.NewReaderSize(s.conn, 4096), bufio.NewWriter(s.conn))
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
			s.reply(rw, "250-%s", s.domain())
			s.reply(rw, "250-SIZE %d", lmtpMaxMessageBytes)
			s.reply(rw, "250 8BITMIME")

		case "MAIL":
			s.from = extractLMTPAngle(arg)
			s.rcpts = nil
			s.reply(rw, "250 Ok")

		case "RCPT":
			if len(s.rcpts) >= lmtpMaxRcpts {
				s.reply(rw, "452 Too many recipients")
			} else {
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

				raw, readErr := readLMTPDotData(rw.Reader, lmtpMaxMessageBytes)

				// Snapshot and reset envelope before sending per-recipient replies.
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
					subject, textBody := parseLMTPMIME(raw)
					receivedAt := time.Now().UTC()
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

// deliverOne saves a single copy of the message addressed to rcpt in their inbound mailbox.
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

// findOrCreateMailbox returns the ID of the named mailbox for userEmail,
// creating it if it does not exist. Handles concurrent creation races.
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
		// Race condition: another goroutine may have created it — retry once.
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

// extractLMTPAngle extracts the email address from "FROM:<addr>" or "TO:<addr>" syntax.
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

// readLMTPDotData reads SMTP/LMTP dot-stuffed DATA until the lone "." terminator.
func readLMTPDotData(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		// Dot-transparency: leading ".." → "."
		if len(line) >= 2 && line[0] == '.' && line[1] == '.' {
			line = line[1:]
		}
		// End-of-data marker: a line containing only "."
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

// parseLMTPMIME extracts the Subject header and the first text/plain body part
// from a raw RFC 5322 message.
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
