package bootstrap

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

func hardenedCfg() config.Config {
	return config.Config{
		PublicAPIBaseURL: "http://api.invalid",
		Environment:      "production",
		CookieSecure:     true,
		CookieSameSite:   "lax",
		ShutdownTimeout:  2 * time.Second,
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo reservar un puerto libre: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestServeWithContextGracefulShutdown(t *testing.T) {
	cfg := hardenedCfg()
	cfg.HTTPAddr = freeAddr(t)
	cfg.ShutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- ServeWithContext(ctx, &cfg, nil) }()

	url := "http://" + cfg.HTTPAddr + "/healthz"
	deadline := time.Now().Add(2 * time.Second)
	up := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			up = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatal("el servidor no llegó a aceptar conexiones")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("el apagado graceful debía retornar nil, got %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + time.Second):
		t.Fatal("ServeWithContext no retornó tras cancelar el contexto (apagado colgado)")
	}
}
