package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/infra/httpx"
)

func newHTTPServer(cfg config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.LogMiddleware(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func runHTTPServer(ctx context.Context, cfg config, srv *http.Server) error {
	log.Printf("mail-admin listening on %s", cfg.Addr)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown error: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}
