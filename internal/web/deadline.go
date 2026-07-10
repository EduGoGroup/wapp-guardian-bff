package web

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// RequestDeadlineMiddleware deriva del contexto de la petición entrante un deadline acotado
// (cfg.UpstreamTimeout) y lo instala en c.Request. Como todos los handlers relayan a la API pública con
// c.Request.Context(), ese deadline acota de una sola vez TODA la cadena hacia el upstream —incluida la
// secuencia withAuthRetry (intento → refresh → reintento)— de modo que un upstream lento no cuelgue el
// handler más allá del presupuesto: al vencer, las llamadas devuelven context.DeadlineExceeded y el
// handler cae a su modo degradado (o mapea el error) dentro del WriteTimeout del servidor.
//
// El deadline hereda del contexto de la petición, así que sigue cancelándose también si el cliente se
// desconecta o el servidor se apaga. Con UpstreamTimeout <= 0 el middleware es transparente (sin tope).
func RequestDeadlineMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.UpstreamTimeout <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.UpstreamTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
