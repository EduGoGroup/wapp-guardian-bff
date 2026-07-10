package web

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-shared/auth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// Claves del gin.Context donde el AuthMiddleware siembra los tokens/identidad de la sesión validada para
// que handlers y apiclient los usen como Bearer server-side.
const (
	ctxAccessToken  = "access_token"
	ctxRefreshToken = "refresh_token"
	ctxUserID       = "user_id" // lo lee userIDFromCtx (rate-limit por usuario).
	ctxTenantID     = "tenant_id"
)

// sessionData es lo MÍNIMO que el BFF custodia server-side para operar y refrescar: el access token (JWT
// que viaja como Bearer a la API), el refresh token (opaco, REQ-C6) y el instante de expiración (RFC3339,
// informativo). Se guarda en UNA sola cookie HttpOnly (`wapp_guardian_session`): set/clear atómico y un
// refresh que reemplaza ambos tokens de una vez, sin desincronizar dos cookies.
type sessionData struct {
	AccessToken  string `json:"a"`
	RefreshToken string `json:"r"`
	ExpiresAt    string `json:"e,omitempty"`
}

// encodeSession serializa la sesión a un valor de cookie seguro: base64-URL sin padding, así el JSON (con
// comas, comillas y dos puntos) no lo sanea ni descarta http.SetCookie.
//
// Nota de diseño (H9): el valor NO lleva MAC/firma propia —es base64(JSON), no un token sellado por el
// BFF—. Es aceptable porque la integridad NO recae en la cookie: el contenido útil es el access token (un
// JWT) y la API pública REVALIDA ese Bearer en CADA llamada server-side (parse-unverified + exp en el BFF
// es solo un filtro barato, no el gate). Un valor manipulado produce un JWT que la API rechaza (401) → se
// fuerza logout; el BFF es zero-secret (no comparte WAPP_JWT_SECRET), así que no podría firmar la cookie
// aunque quisiera. Si en el futuro se guardara estado sensible propio en la cookie, habría que sellarla.
func encodeSession(s sessionData) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeSession revierte encodeSession.
func decodeSession(value string) (sessionData, error) {
	var s sessionData
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	return s, nil
}

// unverifiedParser parsea claims SIN verificar la firma. Decisión de diseño (design §4): el BFF NO es el
// gate criptográfico —la API pública revalida el Bearer en CADA llamada server-side— y NO comparte
// WAPP_JWT_SECRET (zero-secret). Reusa auth.Claims de wapp-shared (embebe jwt.RegisteredClaims → exp) para
// no redefinir el shape del token.
var unverifiedParser = jwt.NewParser()

// parseAccessClaims extrae los claims del access token sin verificar firma.
func parseAccessClaims(accessToken string) (*auth.Claims, error) {
	var claims auth.Claims
	if _, _, err := unverifiedParser.ParseUnverified(accessToken, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// sessionValid es la ÚNICA validación que hace el BFF: exp presente y en el futuro. Sin secreto no
// verifica firma (la API pública es el gate real).
func sessionValid(claims *auth.Claims) bool {
	exp := claims.ExpiresAt
	if exp == nil {
		return false
	}
	return exp.After(time.Now())
}

// AuthMiddleware protege las rutas operativas (REQ-C4): lee la cookie de sesión, valida el access token
// (parse-unverified + exp) y siembra en el contexto el access/refresh token y la identidad para que los
// handlers y el apiclient los usen como Bearer. Cookie ausente/ilegible o token inválido/expirado → limpia
// la cookie y redirige a /login.
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(sessionCookieName)
		if err != nil || raw == "" {
			h.redirectToLogin(c)
			return
		}
		sess, err := decodeSession(raw)
		if err != nil || sess.AccessToken == "" {
			h.clearSessionCookie(c)
			h.redirectToLogin(c)
			return
		}
		claims, err := parseAccessClaims(sess.AccessToken)
		if err != nil || !sessionValid(claims) {
			h.clearSessionCookie(c)
			h.redirectToLogin(c)
			return
		}
		c.Set(ctxAccessToken, sess.AccessToken)
		c.Set(ctxRefreshToken, sess.RefreshToken)
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxTenantID, claims.TenantID)
		c.Next()
	}
}

// redirectToLogin corta la cadena y manda al login (303: fuerza GET tras el redirect).
func (h *Handler) redirectToLogin(c *gin.Context) {
	c.Redirect(http.StatusSeeOther, "/login")
	c.Abort()
}

// refreshSession intenta renovar la sesión con el refresh token del contexto y re-emite la cookie (REQ-C6).
// Devuelve el nuevo access token. Es el ANDAMIAJE que consumirán T3/T4: cuando una llamada de negocio
// devuelva apiclient.ErrUnauthorized, reintentan UNA vez tras refrescar; si el refresh falla, fuerzan
// logout. En T2 aún no hay llamadas de negocio que lo disparen en caliente (queda cubierto por su test).
func (h *Handler) refreshSession(c *gin.Context) (string, error) {
	rt, _ := c.Get(ctxRefreshToken)
	refreshToken, _ := rt.(string)
	if refreshToken == "" {
		return "", apiclient.ErrUnauthorized
	}
	res, err := h.api.Refresh(c.Request.Context(), refreshToken)
	if err != nil {
		return "", err
	}
	if err := h.startSession(c, res); err != nil {
		return "", err
	}
	c.Set(ctxAccessToken, res.AccessToken)
	c.Set(ctxRefreshToken, res.RefreshToken)
	slog.Info("sesión refrescada", "user_id", res.Context.UserID)
	return res.AccessToken, nil
}
