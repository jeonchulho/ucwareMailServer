package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
)

type ExportConfig struct {
	DovecotUsersFile       string
	PostfixMailboxMapsFile string
	PostfixDomainsFile     string
	MailRoot               string
	MailUID                int
	MailGID                int
}

func Export(users []store.User, cfg ExportConfig) error {
	if err := os.MkdirAll(filepath.Dir(cfg.DovecotUsersFile), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.PostfixMailboxMapsFile), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.PostfixDomainsFile), 0o755); err != nil {
		return err
	}

	dovecotLines := make([]string, 0, len(users))
	mailboxLines := make([]string, 0, len(users))
	domainsSet := make(map[string]struct{})

	for _, u := range users {
		parts := strings.Split(u.Email, "@")
		if len(parts) != 2 {
			return fmt.Errorf("invalid email in store: %s", u.Email)
		}
		localPart, domain := parts[0], parts[1]
		relativeMaildir := filepath.ToSlash(filepath.Join(domain, localPart)) + "/"
		homeDir := filepath.ToSlash(filepath.Join(cfg.MailRoot, domain, localPart))

		dovecotLines = append(
			dovecotLines,
			fmt.Sprintf("%s:{BLF-CRYPT}%s:%d:%d::%s::", u.Email, u.PasswordHash, cfg.MailUID, cfg.MailGID, homeDir),
		)
		mailboxLines = append(mailboxLines, fmt.Sprintf("%s %s", u.Email, relativeMaildir))
		domainsSet[domain] = struct{}{}
	}

	sort.Strings(dovecotLines)
	sort.Strings(mailboxLines)

	domains := make([]string, 0, len(domainsSet))
	for domain := range domainsSet {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	if err := writeAtomically(cfg.DovecotUsersFile, strings.Join(dovecotLines, "\n")+"\n", 0o640); err != nil {
		return err
	}
	if err := writeAtomically(cfg.PostfixMailboxMapsFile, strings.Join(mailboxLines, "\n")+"\n", 0o640); err != nil {
		return err
	}
	if err := writeAtomically(cfg.PostfixDomainsFile, strings.Join(domains, "\n")+"\n", 0o640); err != nil {
		return err
	}

	return nil
}

func writeAtomically(path, content string, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpName, path)
}
