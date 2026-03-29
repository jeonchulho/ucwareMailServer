package main

import (
	"log"
	"os"

	"github.com/jeonchulho/ucwareMailServer/cmd/web-frontend/web"
)

func main() {
	cfg := web.Config{
		Addr:         envOr("WEB_ADDR", ":8090"),
		APIBaseURL:   envOr("API_BASE_URL", "http://localhost:8080"),
		SecureCookie: envOr("WEB_SECURE_COOKIE", "false") == "true",
	}
	if err := web.Run(cfg); err != nil {
		log.Fatalf("web-frontend error: %v", err)
	}
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
