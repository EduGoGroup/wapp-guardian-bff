// wapp-guardian-bff: consola web BFF de operación de wApp.
//
// Servidor SSR endurecido (Gin + html/template embebido) que sirve la UI Material Design 3 mismo-origen,
// custodia el JWT en una cookie HttpOnly y relaya server-to-server contra la API pública REST
// (:8103 /api/v1). NO habla gRPC con el Gateway/Edge ni toca claves/DEK (zero-knowledge, INV-2).
package main

import (
	"log/slog"
	"os"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/wApp/wapp-guardian-bff/internal/config"
	"github.com/wApp/wapp-guardian-bff/internal/web"
)

func main() {
	cfg := config.Load()

	// Handler slog único (texto en local, JSON fuera): se fija como default del proceso para que los
	// middlewares del paquete web (que loguean vía slog) compartan destino y formato. El logger de
	// wApp (wapp-shared/logger, REQ-B6) envuelve ese mismo default.
	jsonLogs := cfg.Environment != "local"
	var handler slog.Handler
	if jsonLogs {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	slog.SetDefault(slog.New(handler))
	log := logger.Default()

	log.Info("consola BFF iniciada",
		"addr", cfg.HTTPAddr,
		"public_api", cfg.PublicAPIBaseURL,
		"ambiente", cfg.Environment,
	)

	if err := web.Run(&cfg); err != nil {
		log.Error("el servidor BFF terminó con error", "error", err)
		os.Exit(1)
	}
	log.Info("consola BFF finalizada")
}
