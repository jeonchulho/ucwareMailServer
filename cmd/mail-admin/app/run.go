package app

import (
	"context"
	"log"
	"os/signal"
	"syscall"
)

func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := initConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	if err := initPaths(cfg); err != nil {
		log.Fatalf("path setup error: %v", err)
	}

	st, archiveStore, err := initAppStores(cfg)
	if err != nil {
		log.Fatalf("store init error: %v", err)
	}
	defer st.Close()
	if archiveStore != nil {
		defer archiveStore.Close()
		log.Printf("archive db enabled: driver=%s", cfg.ArchiveDBDriver)
	}

	s := &server{store: st, archive: archiveStore, cfg: cfg}
	configureOAuth2(s)

	authService, handlerService, err := buildServices(s)
	if err != nil {
		log.Fatalf("service init error: %v", err)
	}
	mux := buildMux(authService, handlerService)

	if cfg.LMTPEnabled {
		if archiveStore == nil {
			log.Printf("lmtp server is enabled but archive db is disabled — skipping lmtp")
		} else {
			go func() {
				if err := runLMTPServer(ctx, cfg, archiveStore); err != nil {
					log.Printf("lmtp server error: %v", err)
				}
			}()
		}
	}

	if cfg.POP3Enabled {
		if archiveStore == nil {
			log.Printf("pop3 server is enabled but archive db is disabled — skipping pop3")
		} else {
			go func() {
				if err := runPOP3Server(ctx, cfg, st, archiveStore); err != nil {
					log.Printf("pop3 server error: %v", err)
				}
			}()
		}
	}

	if err := runHTTPServer(ctx, cfg, newHTTPServer(cfg, mux)); err != nil {
		log.Fatalf("http server error: %v", err)
	}
}
