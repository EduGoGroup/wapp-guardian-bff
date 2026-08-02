package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

const sessionCookieName = "wapp_guardian_session"
const defaultSessionMaxAge = 3600

// render layout maestro base.html.
func render(cfg *config.Config, c *gin.Context, status int, contentTemplate string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CurrentPath"] = c.Request.URL.Path
	_, authenticated := c.Get(ctxAccessToken)
	data["IsAuthenticated"] = authenticated
	data["ContentTemplate"] = contentTemplate
	data["Nonce"] = nonceFromCtx(c)
	data["CSRFToken"] = csrfTokenFromCtx(c)
	c.HTML(status, "base.html", data)
}

// setSessionCookie escribe la cookie de sesión HttpOnly.
func setSessionCookie(cfg *config.Config, c *gin.Context, value string, maxAgeSeconds int) {
	c.SetSameSite(sameSiteMode(cfg.CookieSameSite, cfg.CookieSecure))
	c.SetCookie(sessionCookieName, value, maxAgeSeconds, "/", "", cfg.CookieSecure, true)
}

// clearSessionCookie borra la cookie de sesión.
func clearSessionCookie(cfg *config.Config, c *gin.Context) {
	setSessionCookie(cfg, c, "", -1)
}

// sameSiteMode traduce el string de config al modo de http.
func sameSiteMode(mode string, secure bool) http.SameSite {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		if secure {
			return http.SameSiteNoneMode
		}
		return http.SameSiteLaxMode
	default:
		return http.SameSiteLaxMode
	}
}

// sessionMaxAge calcula la vida de la cookie (segundos) desde el expires_at RFC3339.
func sessionMaxAge(expiresAt string) int {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return defaultSessionMaxAge
	}
	secs := int(time.Until(t).Seconds())
	if secs <= 0 {
		return defaultSessionMaxAge
	}
	return secs
}

// userIDFromCtx devuelve el user_id de la sesión.
func userIDFromCtx(c *gin.Context) string {
	if v, ok := c.Get("user_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// prettyJSON re-indenta el JSON para mostrarlo legible en el textarea.
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
