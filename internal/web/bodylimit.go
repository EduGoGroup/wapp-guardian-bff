package web

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimitMiddleware acota el cuerpo de las rutas indicadas (las que aceptan archivos subidos).
//
// EL ORDEN IMPORTA Y NO ES NEGOCIABLE: este middleware tiene que registrarse ANTES del de CSRF,
// porque el de CSRF lee el formulario para comparar el token y con eso consume el cuerpo entero
// —a memoria hasta `MaxMultipartMemory` y a disco lo que sobre—. Un tope montado después llegaría
// cuando el daño ya está hecho.
//
// Se aplica por lista de rutas y no globalmente a propósito: el editor de flujos publica
// definiciones que pueden ser grandes, y meterle un techo por la puerta de atrás sería cambiar el
// comportamiento de una pantalla ajena sin que nadie lo pidiera.
//
// La defensa es doble porque un solo control no basta: el `Content-Length` se mira primero para
// poder decir qué pasó (un navegador siempre lo manda en una subida), y el MaxBytesReader cubre el
// caso en que no venga (cuerpo troceado), aunque ahí el corte se note como un formulario ilegible.
func BodyLimitMiddleware(limit int64, paths ...string) gin.HandlerFunc {
	guarded := make(map[string]bool, len(paths))
	for _, p := range paths {
		guarded[p] = true
	}
	return func(c *gin.Context) {
		if !isUnsafeMethod(c.Request.Method) || !guarded[c.Request.URL.Path] {
			c.Next()
			return
		}
		if c.Request.ContentLength > limit {
			slog.Warn("petición rechazada por tamaño",
				"path", c.Request.URL.Path, "bytes", c.Request.ContentLength, "limite", limit)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "El archivo es demasiado grande para esta pantalla. Sube un documento más pequeño.",
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
