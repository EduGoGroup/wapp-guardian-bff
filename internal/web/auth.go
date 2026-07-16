package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
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

// sessionValid es la validación mínima del BFF: exp presente y en el futuro. Sin secreto no verifica firma
// (la API pública es el gate real).
func sessionValid(claims *auth.Claims) bool {
	exp := claims.ExpiresAt
	if exp == nil {
		return false
	}
	return exp.After(time.Now())
}

// refreshMargin es el colchón del refresh PROACTIVO: si al access le queda menos que esto, se renueva ANTES
// de dejar pasar la petición (para no cortarla a mitad si el token muere en los próximos segundos). El
// access dura ~15 min; el refresh ~30 días, así que renovar temprano es barato y evita 401 en caliente.
const refreshMargin = 2 * time.Minute

// refreshDue indica si conviene refrescar YA: el access ya expiró o le queda menos de refreshMargin. Sin exp
// (token raro) se trata como que necesita refresh.
func refreshDue(claims *auth.Claims) bool {
	exp := claims.ExpiresAt
	if exp == nil {
		return true
	}
	return time.Until(exp.Time) < refreshMargin
}

// AuthMiddleware protege las rutas operativas (REQ-C4): lee la cookie de sesión, valida el access token
// (parse-unverified + exp) y siembra en el contexto el access/refresh token y la identidad para que los
// handlers y el apiclient los usen como Bearer.
//
// Bug 0001 — refresh PROACTIVO: expirar el access (~15 min) NO debe cerrar la sesión mientras el refresh
// (~30 días) siga vivo. Si el access ya expiró o está por hacerlo (refreshDue), y hay refresh token, se
// renueva ANTES de dejar pasar la petición y se re-emite la cookie con el par NUEVO (la rotación del IAM
// revoca el anterior), sin redirigir. Solo se fuerza logout si el refresh es rechazado (401) o si el access
// ya no es utilizable y no hay forma de renovarlo. Un fallo transitorio del refresh (red/5xx) con el access
// AÚN vigente degrada con gracia: se continúa con el token actual.
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
		if err != nil {
			// Token ilegible: no se puede leer ni exp ni identidad, ni sabemos si el refresh casa. Al login.
			h.clearSessionCookie(c)
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
				// Renovado y cookie re-emitida con el par NUEVO. Sigue con la identidad nueva.
				accessToken = res.AccessToken
				refreshToken = res.RefreshToken
				if nc, cerr := parseAccessClaims(res.AccessToken); cerr == nil {
					claims = nc
				}
			case errors.Is(rerr, apiclient.ErrUnauthorized):
				// Refresh inválido/expirado/revocado: única causa para forzar logout.
				h.clearSessionCookie(c)
				h.redirectToLogin(c)
				return
			default:
				// Fallo transitorio (red/5xx): degrada con gracia si el access AÚN sirve; si ya expiró no
				// hay manera de operar → logout.
				if !sessionValid(claims) {
					h.clearSessionCookie(c)
					h.redirectToLogin(c)
					return
				}
				slog.Warn("refresh proactivo falló; se continúa con el access aún vigente", "error", rerr)
			}
		case !sessionValid(claims):
			// Access expirado y sin refresh token disponible: no se puede renovar → logout.
			h.clearSessionCookie(c)
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

// redirectToLogin corta la cadena y manda al login (303: fuerza GET tras el redirect).
func (h *Handler) redirectToLogin(c *gin.Context) {
	c.Redirect(http.StatusSeeOther, "/login")
	c.Abort()
}

// refreshSession renueva la sesión con el refresh token del contexto y re-emite la cookie (REQ-C6). Es el
// camino REACTIVO: cuando una llamada de negocio devuelve apiclient.ErrUnauthorized, withAuthRetry refresca
// UNA vez y reintenta; si el refresh falla, degrada/fuerza logout aguas arriba. Comparte el single-flight
// con el refresh proactivo del AuthMiddleware (misma sesión ⇒ una sola llamada).
func (h *Handler) refreshSession(c *gin.Context) (string, error) {
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

// refreshViaFlight ejecuta el refresh serializado por sesión (single-flight, clave = refresh token entrante)
// y, con el par nuevo, re-emite la cookie y siembra el contexto. Lo comparten el refresh proactivo
// (AuthMiddleware) y el reactivo (refreshSession): dos peticiones concurrentes de la misma sesión colapsan
// en UNA sola llamada a la API y reusan el resultado (evita que la segunda refresque con un token ya rotado
// —revocado por el IAM— y se autoexpulse). Cada petición re-emite su PROPIA cookie con el par compartido.
func (h *Handler) refreshViaFlight(c *gin.Context, refreshToken string) (*apiclient.AuthResult, error) {
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

// refreshGroup es un single-flight casero (sin dependencia nueva) que serializa los refresh por clave. Varios
// llamadores concurrentes con la MISMA clave (el refresh token de la sesión) comparten UNA sola ejecución y
// reciben el mismo resultado. Reentrante entre peticiones distintas: se limpia la entrada al terminar.
type refreshGroup struct {
	mu    sync.Mutex
	calls map[string]*refreshCall
}

// refreshCall es una ejecución en curso: los que esperan bloquean en done y luego leen res/err.
type refreshCall struct {
	done chan struct{}
	res  *apiclient.AuthResult
	err  error
}

func newRefreshGroup() *refreshGroup {
	return &refreshGroup{calls: make(map[string]*refreshCall)}
}

// do ejecuta fn una sola vez por key; los llamadores concurrentes con la misma key esperan y reciben el
// mismo (res, err). Al terminar borra la entrada para que un refresh posterior (token ya rotado) arranque
// limpio.
func (g *refreshGroup) do(key string, fn func() (*apiclient.AuthResult, error)) (*apiclient.AuthResult, error) {
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.res, call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.res, call.err = fn()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)

	return call.res, call.err
}
