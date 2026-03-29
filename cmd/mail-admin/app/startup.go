package app

import (
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/archive"
	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
)

func initConfig() (config, error) {
	return loadConfig()
}

func initPaths(cfg config) error {
	return ensurePaths(cfg)
}

func initAppStores(cfg config) (*store.SQLiteStore, *archive.SQLStore, error) {
	return initStores(cfg)
}
