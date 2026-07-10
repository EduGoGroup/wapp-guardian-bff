package web

import (
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// Constantes del token CSRF (patrón double-submit): el mismo valor viaja en una cookie propia y, en cada
// formulario mutante, en un campo oculto. El servidor compara ambos en los métodos que mutan estado.
const (
	csrfCookieName = "wapp_csrf"    // cookie HttpOnly con el token (el navegador la reenvía same-site).
	csrfFieldName  = "csrf_token"   // nombre del <input hidden> que las plantillas incrustan.
	csrfHeaderName = "X-CSRF-Token" // alternativa por cabecera (defensa; el primario es el campo del form).
	ctxCSRFToken   = "csrf_token"   // clave del gin.Context donde se siembra el token para el render.
)

// csrfTokenMaxAge es la vida de la cookie CSRF (12 h). Cubre de sobra una sesión de trabajo sin rotar el
// token en cada GET (rotarlo invalidaría formularios abiertos en otras pestañas).
const csrfTokenMaxAge = 12 * 60 * 60

// CSRFMiddleware implementa la defensa CSRF double-submit sobre los métodos que mutan estado (REQ: H2):
//
//   - En cada petición asegura que exista un token en la cookie `wapp_csrf`; si falta, genera uno y lo
//     fija (fail-closed: si no hay entropía, responde 500 en vez de servir sin token). El token se siembra
//     en el contexto para que handlers.render lo incruste como campo oculto en los formularios.
//   - En métodos inseguros (POST/PUT/PATCH/DELETE) exige que el campo del formulario (o la cabecera
//     X-CSRF-Token) coincida con la cookie (comparación en tiempo constante). Cookie ausente, campo ausente
//     o discrepancia → 403: un origen atacante no puede leer la cookie (SOP) ni conoce el token, y con la
//     cookie marcada SameSite=Lax ni siquiera se envía en un POST cross-site.
//
// La cookie es HttpOnly (el JS nunca la lee; el token lo incrusta el servidor al renderizar) y SameSite=Lax
// SIEMPRE, con independencia de la config de SameSite de la cookie de sesión: el fail-safe CSRF no debe
// degradarse a None aunque la sesión se configure así.
func CSRFMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieToken, _ := c.Cookie(csrfCookieName)

		if isUnsafeMethod(c.Request.Method) {
			submitted := csrfTokenFromRequest(c)
			if cookieToken == "" || submitted == "" ||
				subtle.ConstantTimeCompare([]byte(cookieToken), []byte(submitted)) != 1 {
				slog.Warn("petición rechazada por CSRF",
					"method", c.Request.Method, "path", c.Request.URL.Path)
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "Petición no válida (token de seguridad ausente o incorrecto). Recarga la página e inténtalo de nuevo.",
				})
				return
			}
		}

		// Asegura un token para los formularios de esta respuesta (sin rotar el existente: estabilidad
		// entre pestañas). Si no hay cookie, se genera y se fija; sin entropía se falla cerrado.
		token := cookieToken
		if token == "" {
			t, err := generateCSRFToken()
			if err != nil {
				slog.Error("no se pudo generar el token CSRF", "error", err)
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			token = t
			setCSRFCookie(c, cfg, token)
		}
		c.Set(ctxCSRFToken, token)
		c.Next()
	}
}

// isUnsafeMethod dice si el método HTTP muta estado y por tanto exige validación CSRF. GET/HEAD/OPTIONS son
// seguros (idempotentes, sin efectos secundarios).
func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// csrfTokenFromRequest extrae el token presentado: primero el campo del formulario, luego la cabecera.
func csrfTokenFromRequest(c *gin.Context) string {
	if v := c.PostForm(csrfFieldName); v != "" {
		return v
	}
	return c.GetHeader(csrfHeaderName)
}

// setCSRFCookie fija la cookie del token: HttpOnly (el JS nunca la lee), SameSite=Lax SIEMPRE (fail-safe,
// no sigue a la config de la cookie de sesión) y Secure según la config (solo TLS fuera de local).
func setCSRFCookie(c *gin.Context, cfg *config.Config, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(csrfCookieName, token, csrfTokenMaxAge, "/", "", cfg.CookieSecure, true)
}

// generateCSRFToken produce un token aleatorio de 256 bits (base64 URL-safe sin padding). Reusa randRead
// para que los tests puedan forzar el fallo de entropía y verificar el fail-closed.
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// csrfTokenFromCtx recupera el token CSRF sembrado por CSRFMiddleware (cadena vacía si no hay).
func csrfTokenFromCtx(c *gin.Context) string {
	if v, ok := c.Get(ctxCSRFToken); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
