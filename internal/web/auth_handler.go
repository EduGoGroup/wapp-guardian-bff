package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// AuthHandler maneja la autenticación y los middlewares de sesión del BFF.
type AuthHandler struct {
	cfg     *config.Config
	api     Authenticator
	refresh *refreshGroup
}

// NewAuthHandler construye un AuthHandler dependiente de Authenticator.
func NewAuthHandler(cfg *config.Config, api Authenticator, refresh *refreshGroup) *AuthHandler {
	if refresh == nil {
		refresh = newRefreshGroup()
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

// DoLogout cierra la sesión.
func (h *AuthHandler) DoLogout(c *gin.Context) {
	if raw, err := c.Cookie(sessionCookieName); err == nil && raw != "" {
		if sess, derr := decodeSession(raw); derr == nil && sess.AccessToken != "" {
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
		raw, err := c.Cookie(sessionCookieName)
		if err != nil || raw == "" {
			h.redirectToLogin(c)
			return
		}
		sess, err := decodeSession(raw)
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
		case refreshDue(claims) && refreshToken != "":
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
				if !sessionValid(claims) {
					clearSessionCookie(h.cfg, c)
					h.redirectToLogin(c)
					return
				}
				slog.Warn("refresh proactivo falló; se continúa con el access aún vigente", "error", rerr)
			}
		case !sessionValid(claims):
			clearSessionCookie(h.cfg, c)
			h.redirectToLogin(c)
			return
		}

		c.Set(ctxAccessToken, accessToken)
		c.Set(ctxRefreshToken, refreshToken)
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxTenantID, claims.TenantID)
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
	rt, _ := c.Get(ctxRefreshToken)
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
	res, err := h.refresh.do(refreshToken, func() (*apiclient.AuthResult, error) {
		return h.api.Refresh(c.Request.Context(), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	if err := h.startSession(c, res); err != nil {
		return nil, err
	}
	c.Set(ctxAccessToken, res.AccessToken)
	c.Set(ctxRefreshToken, res.RefreshToken)
	slog.Info("sesión refrescada", "user_id", res.Context.UserID)
	return res, nil
}

// withAuthRetry ejecuta una llamada de negocio con el access token y reintenta ante 401.
func (h *AuthHandler) withAuthRetry(c *gin.Context, fn func(accessToken string) error) error {
	token, _ := c.Get(ctxAccessToken)
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
	}
	if message != "" {
		data["Error"] = message
	}
	render(h.cfg, c, status, "login.html", data)
}

// startSession custodia el par de tokens en la cookie HttpOnly.
func (h *AuthHandler) startSession(c *gin.Context, res *apiclient.AuthResult) error {
	value, err := encodeSession(sessionData{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    res.ExpiresAt,
	})
	if err != nil {
		return err
	}
	setSessionCookie(h.cfg, c, value, sessionMaxAge(res.ExpiresAt))
	return nil
}

// hasValidSession dice si la petición trae una cookie con un access token no expirado.
func (h *AuthHandler) hasValidSession(c *gin.Context) bool {
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
