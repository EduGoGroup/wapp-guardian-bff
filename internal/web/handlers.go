package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// sessionCookieName es el nombre de la cookie HttpOnly que custodia el JWT server-side (INV-4). El
// navegador nunca ve el token; solo lleva esta cookie opaca. La escritura/borrado la centralizan
// setSessionCookie/clearSessionCookie (las consume T2 en el login).
const sessionCookieName = "wapp_guardian_session"

// Handler agrupa las dependencias de los controladores web: la config y el cliente de la API pública
// (relay server-to-server con el Bearer JWT custodiado en cookie).
type Handler struct {
	cfg *config.Config
	api *apiclient.Client
}

// NewHandler construye el Handler con un apiclient apuntando a la API pública de la config.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg, api: apiclient.New(cfg.PublicAPIBaseURL)}
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
	data["CSRFToken"] = csrfTokenFromCtx(c)
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

// ShowLogin pinta la página de login. Si ya hay sesión válida, salta directo a la home (evita re-login).
func (h *Handler) ShowLogin(c *gin.Context) {
	if h.hasValidSession(c) {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	h.render(c, http.StatusOK, "login.html", gin.H{"Title": "Entrar"})
}

// DoLogin procesa el form de login server-to-server (REQ-C1/C2/C3). Éxito → cookie HttpOnly con el par de
// tokens + redirect a la home. Fallo → repinta el login con 401 y un mensaje genérico (sin filtrar si el
// correo existe ni el detalle del upstream).
func (h *Handler) DoLogin(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	if email == "" || password == "" {
		h.renderLoginError(c, http.StatusBadRequest, "Introduce tu correo y contraseña.")
		return
	}

	res, err := h.api.Login(c.Request.Context(), email, password)
	if err != nil {
		// Credenciales inválidas o cualquier fallo del upstream: mensaje genérico (REQ-C3).
		slog.Warn("login rechazado", "error", err)
		h.renderLoginError(c, http.StatusUnauthorized,
			"Credenciales inválidas. Revisa tus datos e inténtalo de nuevo.")
		return
	}

	if err := h.startSession(c, res); err != nil {
		slog.Error("no se pudo custodiar la sesión tras el login", "error", err)
		h.renderLoginError(c, http.StatusInternalServerError,
			"No se pudo iniciar la sesión. Inténtalo de nuevo.")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// DoLogout cierra la sesión (REQ-C5): borra la cookie SIEMPRE y, best-effort, invalida los tokens en la
// API. Un fallo del upstream no impide el logout local. SOLO por POST (muta estado) y con token CSRF.
func (h *Handler) DoLogout(c *gin.Context) {
	if raw, err := c.Cookie(sessionCookieName); err == nil && raw != "" {
		if sess, derr := decodeSession(raw); derr == nil && sess.AccessToken != "" {
			if lerr := h.api.Logout(c.Request.Context(), sess.AccessToken, sess.RefreshToken); lerr != nil {
				slog.Warn("logout en la API falló (se ignora, se cierra localmente)", "error", lerr)
			}
		}
	}
	h.clearSessionCookie(c)
	c.Redirect(http.StatusSeeOther, "/login")
}

// renderLoginError repinta la página de login con un mensaje y el status dado (401 en credenciales,
// REQ-C3).
func (h *Handler) renderLoginError(c *gin.Context, status int, message string) {
	h.render(c, status, "login.html", gin.H{"Title": "Entrar", "Error": message})
}

// startSession custodia el par de tokens en la cookie HttpOnly (REQ-C2). El maxAge sigue a la expiración
// del access token; si no se puede leer, cae a un default prudente.
func (h *Handler) startSession(c *gin.Context, res *apiclient.AuthResult) error {
	value, err := encodeSession(sessionData{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    res.ExpiresAt,
	})
	if err != nil {
		return err
	}
	h.setSessionCookie(c, value, sessionMaxAge(res.ExpiresAt))
	return nil
}

// hasValidSession dice si la petición trae una cookie con un access token aún no expirado.
func (h *Handler) hasValidSession(c *gin.Context) bool {
	raw, err := c.Cookie(sessionCookieName)
	if err != nil || raw == "" {
		return false
	}
	sess, err := decodeSession(raw)
	if err != nil || sess.AccessToken == "" {
		return false
	}
	claims, err := parseAccessClaims(sess.AccessToken)
	return err == nil && sessionValid(claims)
}

// defaultSessionMaxAge es el fallback de vida de la cookie si expires_at no es parseable (1 h).
const defaultSessionMaxAge = 3600

// sessionMaxAge calcula la vida de la cookie (segundos) desde el expires_at RFC3339 de la API. Si no
// parsea o ya pasó, cae al default. La cookie caduca con el token: el navegador la descarta sola.
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
