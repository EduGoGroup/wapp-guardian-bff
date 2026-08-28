package web

import (
	"bytes"
	"encoding/json"

	"github.com/gin-gonic/gin"

	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// pageRenderer pinta sobre el layout maestro base.html, sembrando en cada página el nonce, el token
// CSRF y el estado de sesión. Es del módulo: que esas tres claves las ponga el renderizador y no
// cada handler es justo el punto —repetirlas a mano es lo que un día se olvida en una pantalla nueva.
var pageRenderer = webgin.NewRenderer("base.html")

// render pinta contentTemplate dentro del layout maestro.
func render(cfg *config.Config, c *gin.Context, status int, contentTemplate string, data gin.H) {
	pageRenderer.HTML(c, status, contentTemplate, data)
}

// setSessionCookie escribe la cookie de sesión HttpOnly con la política de ESTE BFF.
func setSessionCookie(cfg *config.Config, c *gin.Context, value string, maxAgeSeconds int) {
	webgin.SetSessionCookie(c, sessionCookieOptions(cfg), value, maxAgeSeconds)
}

// clearSessionCookie borra la cookie de sesión.
func clearSessionCookie(cfg *config.Config, c *gin.Context) {
	webgin.ClearSessionCookie(c, sessionCookieOptions(cfg))
}

// prettyJSON re-indenta el JSON para mostrarlo legible en el textarea.
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
