package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/web"
)

// Run arranca el servidor web sobre un http.Server endurecido y bloquea hasta recibir SIGINT/SIGTERM.
func Run(cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ServeWithContext(ctx, cfg, stop)
}

// ServeWithContext levanta el servidor y bloquea hasta la cancelación del contexto.
func ServeWithContext(ctx context.Context, cfg *config.Config, onSignal func()) error {
	// Composition root: aquí se construye lo que vive fuera del proceso antes de montar el router. El
	// verificador de Identity Tokens es fail-closed, así que un fallo suyo aborta el arranque.
	//
	// El *identityjwt.MultiVerifier que devuelve se descarta a propósito: el BFF no verifica Identity
	// Tokens (los canjea por un Context Token contra wapp-cloud-platform, que sí verifica), así que
	// aquí no hay nada que cablear. Lo que sí importa, y por lo que se llama igual, es el efecto
	// colateral: si WAPP_IDENTITY_JWKS_URL está puesta, valida en el arranque que el emisor responde.
	if _, err := newIdentityVerifier(cfg); err != nil {
		return err
	}

	router, cleanup := web.NewRouterWithLimiter(cfg)
	if cleanup != nil {
		defer cleanup()
	}

	srv := newHTTPServer(cfg, router)

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("consola BFF escuchando",
			"addr", cfg.HTTPAddr, "public_api", cfg.PublicAPIBaseURL, "ambiente", cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		if onSignal != nil {
			onSignal()
		}
		slog.Info("señal de apagado recibida, drenando peticiones en vuelo",
			"timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("apagado graceful excedió el plazo; forzando cierre", "error", err)
		return err
	}
	slog.Info("consola BFF apagada limpiamente")
	return nil
}

func newHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}
