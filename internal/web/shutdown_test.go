package web

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// freeAddr reserva un puerto efímero y lo libera, devolviendo host:port para que el servidor lo tome.
// Hay una ventana de carrera teórica entre cerrar y reusar, aceptable para un test local.
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

// TestServeWithContextGracefulShutdown verifica el path de T2: el servidor sirve, y al cancelarse el
// contexto (equivalente a SIGTERM) apaga de forma graceful y serveWithContext retorna nil sin colgarse.
func TestServeWithContextGracefulShutdown(t *testing.T) {
	cfg := hardenedCfg()
	cfg.HTTPAddr = freeAddr(t)
	cfg.ShutdownTimeout = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- serveWithContext(ctx, cfg, nil) }()

	// Espera a que el servidor esté aceptando conexiones (poll a /healthz).
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

	// "SIGTERM": cancelar el contexto debe drenar y retornar nil dentro del plazo.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("el apagado graceful debía retornar nil, got %v", err)
		}
	case <-time.After(cfg.ShutdownTimeout + time.Second):
		t.Fatal("serveWithContext no retornó tras cancelar el contexto (apagado colgado)")
	}
}
