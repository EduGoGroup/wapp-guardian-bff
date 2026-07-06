package web

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// sessionCookieName es el nombre de la cookie HttpOnly que custodia el JWT server-side (INV-4). El
// navegador nunca ve el token; solo lleva esta cookie opaca. La escritura/borrado la centralizan
// setSessionCookie/clearSessionCookie (las consume T2 en el login).
const sessionCookieName = "wapp_guardian_session"

// Handler agrupa las dependencias de los controladores web. En T1 solo lleva la config; T2 añadirá el
// cliente de la API pública (apiclient).
type Handler struct {
	cfg *config.Config
}

// NewHandler construye el Handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg}
}

// render pinta una página completa dentro del layout maestro base.html (navegación clásica de páginas,
// sin framework JS). Siembra el nonce CSP de la petición para que las plantillas lo pongan en cada
// <style>/<script> inline y cumplan la CSP estricta sin 'unsafe-inline'.
func (h *Handler) render(c *gin.Context, status int, contentTemplate string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CurrentPath"] = c.Request.URL.Path
	if _, err := c.Cookie(sessionCookieName); err == nil {
		data["IsAuthenticated"] = true
	} else {
		data["IsAuthenticated"] = false
	}
	data["ContentTemplate"] = contentTemplate
	data["Nonce"] = nonceFromCtx(c)
	c.HTML(status, "base.html", data)
}

// setSessionCookie escribe la cookie de sesión HttpOnly con los atributos de seguridad de la config:
// Secure (solo TLS) y SameSite (CSRF). HttpOnly SIEMPRE true (el JS jamás lee el token). El maxAge en
// segundos gobierna la vigencia; un maxAge negativo la borra. Centralizar aquí evita divergencias.
//
// En T1 aún no hay login que la invoque; queda lista para T2. Se marca con //nolint indirecto: la
// consumen setSessionCookie/clearSessionCookie entre sí y el login de T2.
func (h *Handler) setSessionCookie(c *gin.Context, value string, maxAgeSeconds int) {
	c.SetSameSite(sameSiteMode(h.cfg.CookieSameSite, h.cfg.CookieSecure))
	c.SetCookie(sessionCookieName, value, maxAgeSeconds, "/", "", h.cfg.CookieSecure, true)
}

// clearSessionCookie borra la cookie de sesión respetando los mismos atributos (necesario para que el
// navegador identifique y elimine la cookie correcta).
func (h *Handler) clearSessionCookie(c *gin.Context) {
	h.setSessionCookie(c, "", -1)
}

// sameSiteMode traduce el string de config al modo de http. "none" exige Secure=true (regla del
// navegador): si no hay Secure se degrada a Lax para no emitir una cookie inválida que el navegador
// descartaría. Default (vacío o desconocido): Lax.
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

// ShowHome pinta la página de inicio del BFF (placeholder de T1 para verificar el render SSR + la CSP
// con nonce). T2 la sustituirá por el login y el dashboard protegido.
func (h *Handler) ShowHome(c *gin.Context) {
	h.render(c, http.StatusOK, "index.html", gin.H{
		"Title": "Consola",
	})
}

// userIDFromCtx devuelve el user_id de la sesión para clavar el rate-limit por usuario. En T1 no hay
// autenticación, así que siempre devuelve "" (se limita por IP). T2 sembrará "user_id" en el contexto
// desde el AuthMiddleware y este helper lo leerá sin más cambios en el rate-limit.
func userIDFromCtx(c *gin.Context) string {
	if v, ok := c.Get("user_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
