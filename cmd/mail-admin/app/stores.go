package app

import (
	"fmt"
	"strings"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
)

func initStores(cfg config) (*store.SQLiteStore, *archive.SQLStore, error) {
	st, err := store.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("db init error: %w", err)
	}

	var archiveStore *archive.SQLStore
	if cfg.ArchiveDBEnabled {
		if strings.TrimSpace(cfg.ArchiveDBDriver) == "" || strings.TrimSpace(cfg.ArchiveDSN) == "" {
			_ = st.Close()
			return nil, nil, fmt.Errorf("ARCHIVE_DB_ENABLED=true requires ARCHIVE_DB_DRIVER and ARCHIVE_DSN")
		}

		archiveStore, err = archive.NewSQLStore(cfg.ArchiveDBDriver, cfg.ArchiveDSN)
		if err != nil {
			_ = st.Close()
			return nil, nil, fmt.Errorf("archive db init error: %w", err)
		}
	}

	return st, archiveStore, nil
}
