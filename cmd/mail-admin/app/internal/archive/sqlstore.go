package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/sijms/go-ora/v2"
)

type Mailbox struct {
	ID        string
	UserEmail string
	Name      string
	CreatedAt time.Time
}

type Message struct {
	ID         string
	MailboxID  string
	Direction  string
	FromAddr   string
	ToAddr     string
	Subject    string
	RawMIME    string
	TextBody   string
	SizeBytes  int64
	ReceivedAt time.Time
	CreatedAt  time.Time
}

type CreateMessageInput struct {
	MailboxID  string
	Direction  string
	FromAddr   string
	ToAddr     string
	Subject    string
	RawMIME    string
	TextBody   string
	SizeBytes  int64
	ReceivedAt time.Time
}

type SQLStore struct {
	db     *sql.DB
	driver string
}

func NewSQLStore(driver, dsn string) (*SQLStore, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	sqlDriver, err := mapDriverName(driver)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	st := &SQLStore{db: db, driver: driver}
	if err := st.db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := st.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

func (s *SQLStore) Close() error {
	return s.db.Close()
}

func mapDriverName(driver string) (string, error) {
	switch driver {
	case "postgres", "postgresql":
		return "pgx", nil
	case "mysql":
		return "mysql", nil
	case "oracle":
		return "oracle", nil
	default:
		return "", fmt.Errorf("unsupported archive db driver: %s", driver)
	}
}

func rebind(driver, query string) string {
	switch driver {
	case "postgres", "postgresql":
		idx := 1
		var b strings.Builder
		for _, ch := range query {
			if ch == '?' {
				b.WriteString(fmt.Sprintf("$%d", idx))
				idx++
				continue
			}
			b.WriteRune(ch)
		}
		return b.String()
	case "oracle":
		idx := 1
		var b strings.Builder
		for _, ch := range query {
			if ch == '?' {
				b.WriteString(fmt.Sprintf(":%d", idx))
				idx++
				continue
			}
			b.WriteRune(ch)
		}
		return b.String()
	default:
		return query
	}
}

func (s *SQLStore) init() error {
	switch s.driver {
	case "postgres", "postgresql":
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS mailboxes (
				id TEXT PRIMARY KEY,
				user_email TEXT NOT NULL,
				name TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL
			)
		`); err != nil {
			return err
		}
		if _, err := s.db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS ux_mailboxes_user_name ON mailboxes(user_email, name)
		`); err != nil {
			return err
		}
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS messages (
				id TEXT PRIMARY KEY,
				mailbox_id TEXT NOT NULL,
				direction TEXT NOT NULL,
				from_addr TEXT NOT NULL,
				to_addr TEXT NOT NULL,
				subject TEXT NOT NULL,
				raw_mime TEXT NOT NULL,
				text_body TEXT NOT NULL,
				size_bytes BIGINT NOT NULL,
				received_at TIMESTAMPTZ NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				CONSTRAINT fk_messages_mailbox FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id)
			)
		`); err != nil {
			return err
		}
	case "mysql":
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS mailboxes (
				id VARCHAR(64) PRIMARY KEY,
				user_email VARCHAR(255) NOT NULL,
				name VARCHAR(64) NOT NULL,
				created_at DATETIME(6) NOT NULL,
				UNIQUE KEY ux_mailboxes_user_name (user_email, name)
			)
		`); err != nil {
			return err
		}
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS messages (
				id VARCHAR(64) PRIMARY KEY,
				mailbox_id VARCHAR(64) NOT NULL,
				direction VARCHAR(16) NOT NULL,
				from_addr VARCHAR(255) NOT NULL,
				to_addr TEXT NOT NULL,
				subject TEXT NOT NULL,
				raw_mime LONGTEXT NOT NULL,
				text_body LONGTEXT NOT NULL,
				size_bytes BIGINT NOT NULL,
				received_at DATETIME(6) NOT NULL,
				created_at DATETIME(6) NOT NULL,
				INDEX ix_messages_mailbox_received (mailbox_id, received_at),
				CONSTRAINT fk_messages_mailbox FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id)
			)
		`); err != nil {
			return err
		}
	case "oracle":
		if err := execOracleCreateIgnoreExists(s.db, `
			CREATE TABLE mailboxes (
				id VARCHAR2(64) PRIMARY KEY,
				user_email VARCHAR2(255) NOT NULL,
				name VARCHAR2(64) NOT NULL,
				created_at TIMESTAMP NOT NULL
			)
		`); err != nil {
			return err
		}
		if err := execOracleCreateIgnoreExists(s.db, `
			CREATE UNIQUE INDEX ux_mailboxes_user_name ON mailboxes(user_email, name)
		`); err != nil {
			return err
		}
		if err := execOracleCreateIgnoreExists(s.db, `
			CREATE TABLE messages (
				id VARCHAR2(64) PRIMARY KEY,
				mailbox_id VARCHAR2(64) NOT NULL,
				direction VARCHAR2(16) NOT NULL,
				from_addr VARCHAR2(255) NOT NULL,
				to_addr CLOB NOT NULL,
				subject CLOB NOT NULL,
				raw_mime CLOB NOT NULL,
				text_body CLOB NOT NULL,
				size_bytes NUMBER(19) NOT NULL,
				received_at TIMESTAMP NOT NULL,
				created_at TIMESTAMP NOT NULL,
				CONSTRAINT fk_messages_mailbox FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id)
			)
		`); err != nil {
			return err
		}
	}
	return nil
}

func execOracleCreateIgnoreExists(db *sql.DB, ddl string) error {
	block := `BEGIN
		EXECUTE IMMEDIATE :1;
	EXCEPTION
		WHEN OTHERS THEN
			IF SQLCODE != -955 THEN
				RAISE;
			END IF;
	END;`
	_, err := db.Exec(block, strings.TrimSpace(ddl))
	return err
}

func (s *SQLStore) CreateMailbox(ctx context.Context, userEmail, name string) (Mailbox, error) {
	now := time.Now().UTC()
	mb := Mailbox{
		ID:        uuid.NewString(),
		UserEmail: strings.ToLower(strings.TrimSpace(userEmail)),
		Name:      strings.TrimSpace(name),
		CreatedAt: now,
	}
	query := rebind(s.driver, `
		INSERT INTO mailboxes (id, user_email, name, created_at)
		VALUES (?, ?, ?, ?)
	`)
	if _, err := s.db.ExecContext(ctx, query, mb.ID, mb.UserEmail, mb.Name, mb.CreatedAt); err != nil {
		return Mailbox{}, err
	}
	return mb, nil
}

func (s *SQLStore) ListMailboxes(ctx context.Context, userEmail string) ([]Mailbox, error) {
	query := `
		SELECT id, user_email, name, created_at
		FROM mailboxes
	`
	args := []any{}
	if strings.TrimSpace(userEmail) != "" {
		query += ` WHERE user_email = ?`
		args = append(args, strings.ToLower(strings.TrimSpace(userEmail)))
	}
	query += ` ORDER BY user_email ASC, name ASC`
	query = rebind(s.driver, query)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Mailbox
	for rows.Next() {
		var m Mailbox
		if err := rows.Scan(&m.ID, &m.UserEmail, &m.Name, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLStore) GetMessage(ctx context.Context, id string) (Message, error) {
	query := rebind(s.driver, `
		SELECT id, mailbox_id, direction, from_addr, to_addr, subject,
		       raw_mime, text_body, size_bytes, received_at, created_at
		FROM messages
		WHERE id = ?
	`)
	var m Message
	err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(id)).Scan(
		&m.ID, &m.MailboxID, &m.Direction, &m.FromAddr, &m.ToAddr, &m.Subject,
		&m.RawMIME, &m.TextBody, &m.SizeBytes, &m.ReceivedAt, &m.CreatedAt,
	)
	if err != nil {
		return Message{}, err
	}
	return m, nil
}

func (s *SQLStore) DeleteMessage(ctx context.Context, id string) error {
	query := rebind(s.driver, `DELETE FROM messages WHERE id = ?`)
	_, err := s.db.ExecContext(ctx, query, strings.TrimSpace(id))
	return err
}

func (s *SQLStore) CreateMessage(ctx context.Context, in CreateMessageInput) (Message, error) {
	now := time.Now().UTC()
	m := Message{
		ID:         uuid.NewString(),
		MailboxID:  strings.TrimSpace(in.MailboxID),
		Direction:  strings.ToLower(strings.TrimSpace(in.Direction)),
		FromAddr:   strings.TrimSpace(in.FromAddr),
		ToAddr:     strings.TrimSpace(in.ToAddr),
		Subject:    strings.TrimSpace(in.Subject),
		RawMIME:    in.RawMIME,
		TextBody:   in.TextBody,
		SizeBytes:  in.SizeBytes,
		ReceivedAt: in.ReceivedAt.UTC(),
		CreatedAt:  now,
	}
	query := rebind(s.driver, `
		INSERT INTO messages (
			id, mailbox_id, direction, from_addr, to_addr, subject,
			raw_mime, text_body, size_bytes, received_at, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if _, err := s.db.ExecContext(ctx, query,
		m.ID,
		m.MailboxID,
		m.Direction,
		m.FromAddr,
		m.ToAddr,
		m.Subject,
		m.RawMIME,
		m.TextBody,
		m.SizeBytes,
		m.ReceivedAt,
		m.CreatedAt,
	); err != nil {
		return Message{}, err
	}
	return m, nil
}

func (s *SQLStore) ListMessages(ctx context.Context, mailboxID string, limit int) ([]Message, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := rebind(s.driver, `
		SELECT id, mailbox_id, direction, from_addr, to_addr, subject,
		       raw_mime, text_body, size_bytes, received_at, created_at
		FROM messages
		WHERE mailbox_id = ?
		ORDER BY received_at DESC
	`)

	var rows *sql.Rows
	var err error
	switch s.driver {
	case "oracle":
		query += ` FETCH FIRST ` + fmt.Sprintf("%d", limit) + ` ROWS ONLY`
		rows, err = s.db.QueryContext(ctx, query, strings.TrimSpace(mailboxID))
	default:
		query += ` LIMIT ` + fmt.Sprintf("%d", limit)
		rows, err = s.db.QueryContext(ctx, query, strings.TrimSpace(mailboxID))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID,
			&m.MailboxID,
			&m.Direction,
			&m.FromAddr,
			&m.ToAddr,
			&m.Subject,
			&m.RawMIME,
			&m.TextBody,
			&m.SizeBytes,
			&m.ReceivedAt,
			&m.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
