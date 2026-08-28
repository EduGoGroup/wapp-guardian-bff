package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-shared/iam"
	sharedweb "github.com/EduGoGroup/wapp-shared/web"
	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// AuthHandler maneja la autenticación y los middlewares de sesión del BFF.
type AuthHandler struct {
	cfg     *config.Config
	api     Authenticator
	refresh *sharedweb.RefreshGroup[*apiclient.AuthResult]
}

// NewAuthHandler construye un AuthHandler dependiente de Authenticator.
func NewAuthHandler(cfg *config.Config, api Authenticator, refresh *sharedweb.RefreshGroup[*apiclient.AuthResult]) *AuthHandler {
	if refresh == nil {
		refresh = sharedweb.NewRefreshGroup[*apiclient.AuthResult]()
	}
	return &AuthHandler{
		cfg:     cfg,
		api:     api,
		refresh: refresh,
	}
}

// ShowLogin pinta la página de login. Si ya hay sesión válida, salta directo a la home.
func (h *AuthHandler) ShowLogin(c *gin.Context) {
	if h.hasValidSession(c) {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	h.renderLoginError(c, http.StatusOK, "")
}

// DoLogin procesa el form de login server-to-server.
func (h *AuthHandler) DoLogin(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	if email == "" || password == "" {
		h.renderLoginError(c, http.StatusBadRequest, "Introduce tu correo y contraseña.")
		return
	}

	res, err := h.api.Login(c.Request.Context(), email, password)
	if err != nil {
		// El canje apagado en la plataforma NO es un fallo de credenciales, y decirlo importa: quien
		// escribió bien su contraseña reintentaría hasta agotar sus intentos y acabaría bloqueado por
		// una avería de configuración que no es suya. Se distingue arriba porque abajo ya no se puede:
		// la respuesta de credenciales es deliberadamente ciega para no filtrar detalle (REQ-C3).
		if errors.Is(err, iam.ErrDualModeOff) {
			slog.Error("login imposible: la plataforma no tiene el canje cableado", "error", err)
			h.renderLoginError(c, http.StatusServiceUnavailable,
				"El servicio de identidad no está disponible en este momento. Inténtalo de nuevo en unos minutos.")
			return
		}
		// La RESPUESTA sigue siendo ciega a propósito (REQ-C3: no filtrar si un correo existe), pero el
		// LOG no tiene por qué serlo, y fundirlo ahí deja ciego a quien diagnostica: un 403 del System
		// Gate —contraseña correcta, sin la fila en iam.user_systems— es indistinguible de una
		// contraseña mal escrita, y la persona reintenta hasta que el lockout la bloquea. Costó una
		// tarde el 2026-08-28. Las dos consolas ya separaban estas ramas; el BFF no.
		//
		// 🔑 Se pregunta por los errores del apiclient del BFF, NO por los sentinelas de `iam`: esta
		// ruta no los propaga (el 401 llega como apiclient.ErrUnauthorized y el 403 como *APIError).
		// `StatusCodeOf` es el idioma que ya usan entitlements.go y catalogimport_handler.go.
		switch {
		case apiclient.StatusCodeOf(err) == http.StatusForbidden:
			slog.Warn("login rechazado por el System Gate: falta la fila en iam.user_systems para "+apiclient.SystemBFF,
				"error", err)
		case errors.Is(err, apiclient.ErrUnauthorized):
			slog.Warn("login rechazado por identity: credenciales inválidas", "error", err)
		default:
			slog.Warn("login rechazado", "error", err)
		}
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

// DoLogout cierra la sesión.
func (h *AuthHandler) DoLogout(c *gin.Context) {
	if raw := webgin.SessionCookieValue(c, sessionCookieOptions(h.cfg)); raw != "" {
		if sess, derr := sharedweb.DecodeSession(raw); derr == nil && sess.AccessToken != "" {
			if lerr := h.api.Logout(c.Request.Context(), sess.AccessToken, sess.RefreshToken); lerr != nil {
				slog.Warn("logout en la API falló (se ignora, se cierra localmente)", "error", lerr)
			}
		}
	}
	clearSessionCookie(h.cfg, c)
	c.Redirect(http.StatusSeeOther, "/login")
}

// AuthMiddleware protege las rutas operativas.
func (h *AuthHandler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := webgin.SessionCookieValue(c, sessionCookieOptions(h.cfg))
		if raw == "" {
			h.redirectToLogin(c)
			return
		}
		sess, err := sharedweb.DecodeSession(raw)
		if err != nil || sess.AccessToken == "" {
			clearSessionCookie(h.cfg, c)
			h.redirectToLogin(c)
			return
		}
		claims, err := parseAccessClaims(sess.AccessToken)
		if err != nil {
			clearSessionCookie(h.cfg, c)
			h.redirectToLogin(c)
			return
		}

		accessToken := sess.AccessToken
		refreshToken := sess.RefreshToken

		switch {
		case sharedweb.RefreshDue(accessExp(claims), sharedweb.DefaultRefreshMargin) && refreshToken != "":
			res, rerr := h.refreshViaFlight(c, refreshToken)
			switch {
			case rerr == nil:
				accessToken = res.AccessToken
				refreshToken = res.RefreshToken
				if nc, cerr := parseAccessClaims(res.AccessToken); cerr == nil {
					claims = nc
				}
			case errors.Is(rerr, apiclient.ErrUnauthorized):
				clearSessionCookie(h.cfg, c)
				h.redirectToLogin(c)
				return
			default:
				if !sharedweb.SessionValid(accessExp(claims)) {
					clearSessionCookie(h.cfg, c)
					h.redirectToLogin(c)
					return
				}
				slog.Warn("refresh proactivo falló; se continúa con el access aún vigente", "error", rerr)
			}
		case !sharedweb.SessionValid(accessExp(claims)):
			clearSessionCookie(h.cfg, c)
			h.redirectToLogin(c)
			return
		}

		c.Set(webgin.ContextAccessToken, accessToken)
		c.Set(webgin.ContextRefreshToken, refreshToken)
		c.Set(webgin.ContextUserID, claims.UserID)
		c.Set(webgin.ContextTenantID, claims.TenantID)

		// Usuario en estado "en espera" (sin tenant asignado, Plan 056 · T3.5 / D-056.12)
		if claims.TenantID == "" {
			if c.Request.URL.Path != "/pending" && c.Request.URL.Path != "/logout" {
				c.Redirect(http.StatusSeeOther, "/pending")
				c.Abort()
				return
			}
		} else if c.Request.URL.Path == "/pending" {
			c.Redirect(http.StatusSeeOther, "/")
			c.Abort()
			return
		}

		c.Next()
	}
}

// redirectToLogin corta la cadena y manda al login.
func (h *AuthHandler) redirectToLogin(c *gin.Context) {
	c.Redirect(http.StatusSeeOther, "/login")
	c.Abort()
}

// refreshSession renueva la sesión con el refresh token del contexto.
func (h *AuthHandler) refreshSession(c *gin.Context) (string, error) {
	rt, _ := c.Get(webgin.ContextRefreshToken)
	refreshToken, _ := rt.(string)
	if refreshToken == "" {
		return "", apiclient.ErrUnauthorized
	}
	res, err := h.refreshViaFlight(c, refreshToken)
	if err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

// refreshViaFlight ejecuta el refresh serializado por sesión.
func (h *AuthHandler) refreshViaFlight(c *gin.Context, refreshToken string) (*apiclient.AuthResult, error) {
	res, err := h.refresh.Do(refreshToken, func() (*apiclient.AuthResult, error) {
		return h.api.Refresh(c.Request.Context(), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	if err := h.startSession(c, res); err != nil {
		return nil, err
	}
	c.Set(webgin.ContextAccessToken, res.AccessToken)
	c.Set(webgin.ContextRefreshToken, res.RefreshToken)
	slog.Info("sesión refrescada", "user_id", res.Context.UserID)
	return res, nil
}

// withAuthRetry ejecuta una llamada de negocio con el access token y reintenta ante 401.
func (h *AuthHandler) withAuthRetry(c *gin.Context, fn func(accessToken string) error) error {
	token, _ := c.Get(webgin.ContextAccessToken)
	accessToken, _ := token.(string)

	err := fn(accessToken)
	if !errors.Is(err, apiclient.ErrUnauthorized) {
		return err
	}

	newToken, rerr := h.refreshSession(c)
	if rerr != nil {
		return err
	}
	return fn(newToken)
}

// renderLoginError repinta la página de login con un mensaje y el status dado.
func (h *AuthHandler) renderLoginError(c *gin.Context, status int, message string) {
	data := gin.H{
		"Title":                   "Entrar",
		"Subtitle":                "Consola Cloud",
		"EnableAlphaTestAccounts": h.cfg.EnableAlphaTestAccounts,
		"AlphaTestPassword":       h.cfg.AlphaTestPassword,
	}
	if message != "" {
		data["Error"] = message
	}
	render(h.cfg, c, status, "login.html", data)
}

// startSession custodia el par de tokens en la cookie HttpOnly.
func (h *AuthHandler) startSession(c *gin.Context, res *apiclient.AuthResult) error {
	value, err := sharedweb.EncodeSession(sharedweb.SessionData{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    res.ExpiresAt,
	})
	if err != nil {
		return err
	}
	setSessionCookie(h.cfg, c, value, sharedweb.SessionMaxAge(res.ExpiresAt))
	return nil
}

// hasValidSession dice si la petición trae una cookie con un access token no expirado.
func (h *AuthHandler) hasValidSession(c *gin.Context) bool {
	raw := webgin.SessionCookieValue(c, sessionCookieOptions(h.cfg))
	if raw == "" {
		return false
	}
	sess, err := sharedweb.DecodeSession(raw)
	if err != nil || sess.AccessToken == "" {
		return false
	}
	claims, err := parseAccessClaims(sess.AccessToken)
	return err == nil && sharedweb.SessionValid(accessExp(claims))
}
