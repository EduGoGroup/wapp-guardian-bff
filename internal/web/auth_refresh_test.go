package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// stubAPI es un doble del APIPort que SOLO implementa Refresh (lo único que el AuthMiddleware invoca). Embebe
// la interfaz para satisfacer el resto de métodos sin escribirlos: si el test tocara uno no implementado,
// haría panic (señal de que el flujo no debía llamarlo). Cuenta las llamadas a Refresh para verificar el
// single-flight.
type stubAPI struct {
	APIPort
	refreshCount int64
	refreshFn    func(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
}

func (s *stubAPI) Refresh(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error) {
	atomic.AddInt64(&s.refreshCount, 1)
	return s.refreshFn(ctx, refreshToken)
}

// authResult arma el par de tokens que devuelve /auth/refresh.
func authResult(access, refresh string, exp time.Time) *apiclient.AuthResult {
	return &apiclient.AuthResult{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresAt:    exp.Format(time.RFC3339),
		Context:      apiclient.IdentityContext{TenantID: "t-1", UserID: "u-1", Roles: []string{"admin"}},
	}
}

// middlewareProbeRouter monta un router mínimo: SOLO el AuthMiddleware + una ruta que reporta el access token
// sembrado en el contexto. Aísla el middleware de los handlers de negocio (que llamarían a otros métodos del
// APIPort). Sin rate-limit ni CSRF: solo interesa la lógica de sesión/refresh.
func middlewareProbeRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	prot := r.Group("/")
	prot.Use(h.AuthMiddleware())
	prot.GET("/probe", func(c *gin.Context) {
		tok, _ := c.Get(ctxAccessToken)
		s, _ := tok.(string)
		c.String(http.StatusOK, s)
	})
	return r
}

// cookieFor serializa una sesión (access + refresh) al valor de la cookie HttpOnly.
func cookieFor(t *testing.T, access, refresh string) *http.Cookie {
	t.Helper()
	value, err := encodeSession(sessionData{AccessToken: access, RefreshToken: refresh})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: value}
}

// TestAuthMiddlewareExpiredAccessRefreshesAndContinues (caso a): access expirado + refresh OK → el
// middleware renueva, re-emite la cookie con el par NUEVO y DEJA PASAR la petición (no redirige a /login).
func TestAuthMiddlewareExpiredAccessRefreshesAndContinues(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	newAccess := makeToken(t, exp)
	api := &stubAPI{refreshFn: func(_ context.Context, rt string) (*apiclient.AuthResult, error) {
		if rt != "r-old" {
			t.Errorf("Refresh debía recibir el refresh token de la cookie, got %q", rt)
		}
		return authResult(newAccess, "r-new", exp), nil
	}}
	h := NewHandlerWithAPI(authTestCfg("http://unused"), api)
	router := middlewareProbeRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookieFor(t, makeToken(t, time.Now().Add(-time.Hour)), "r-old"))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("access expirado con refresh OK debía continuar (200), got %d", rec.Code)
	}
	if body := rec.Body.String(); body != newAccess {
		t.Errorf("el contexto debía llevar el access token NUEVO tras el refresh, got %q", body)
	}
	if atomic.LoadInt64(&api.refreshCount) != 1 {
		t.Errorf("debía llamarse a Refresh exactamente 1 vez, got %d", api.refreshCount)
	}
	// La cookie se re-emite con el par nuevo (HttpOnly) y sin exponer el JWT en claro.
	raw := sessionSetCookie(rec)
	if raw == "" || !strings.Contains(raw, "HttpOnly") {
		t.Errorf("debía re-emitir la cookie de sesión HttpOnly, got %q", raw)
	}
	if strings.Contains(raw, newAccess) {
		t.Error("el access token no debía viajar en claro en la cookie")
	}
}

// TestAuthMiddlewareRefresh401ClearsAndRedirects (caso b): access expirado + refresh 401 (revocado/expirado)
// → limpia la cookie y redirige a /login.
func TestAuthMiddlewareRefresh401ClearsAndRedirects(t *testing.T) {
	api := &stubAPI{refreshFn: func(_ context.Context, _ string) (*apiclient.AuthResult, error) {
		return nil, fmt.Errorf("refresh: %w", apiclient.ErrUnauthorized)
	}}
	h := NewHandlerWithAPI(authTestCfg("http://unused"), api)
	router := middlewareProbeRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookieFor(t, makeToken(t, time.Now().Add(-time.Hour)), "r-old"))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("refresh 401 debía redirigir 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("debía redirigir a /login, got %q", loc)
	}
	raw := sessionSetCookie(rec)
	if raw == "" || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("la cookie debía limpiarse (Max-Age=0), got %q", raw)
	}
}

// TestAuthMiddlewareTransientRefreshErrorDegrades: access AÚN vigente (dentro del margen proactivo) + refresh
// falla por causa transitoria (red/5xx, NO 401) → degrada con gracia: continúa con el access actual, sin
// forzar logout.
func TestAuthMiddlewareTransientRefreshErrorDegrades(t *testing.T) {
	// Access válido pero dentro del margen proactivo (vence en 1 min < refreshMargin de 2 min).
	nearExp := time.Now().Add(time.Minute)
	access := makeToken(t, nearExp)
	api := &stubAPI{refreshFn: func(_ context.Context, _ string) (*apiclient.AuthResult, error) {
		return nil, fmt.Errorf("apiclient: refresh: dial tcp: connection refused")
	}}
	h := NewHandlerWithAPI(authTestCfg("http://unused"), api)
	router := middlewareProbeRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookieFor(t, access, "r-old"))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("fallo transitorio con access vigente debía continuar (200), got %d", rec.Code)
	}
	if body := rec.Body.String(); body != access {
		t.Errorf("debía continuar con el access token actual, got %q", body)
	}
	if atomic.LoadInt64(&api.refreshCount) != 1 {
		t.Errorf("debía intentar Refresh 1 vez, got %d", api.refreshCount)
	}
}

// TestAuthMiddlewareSingleFlight (caso c): N peticiones concurrentes de la MISMA sesión con el access
// expirado disparan UN SOLO Refresh (single-flight); todas reusan el par nuevo y continúan.
func TestAuthMiddlewareSingleFlight(t *testing.T) {
	const n = 20
	exp := time.Now().Add(time.Hour)
	newAccess := makeToken(t, exp)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	api := &stubAPI{refreshFn: func(_ context.Context, _ string) (*apiclient.AuthResult, error) {
		// Señala la PRIMERA entrada (no bloqueante) y espera el release: así el test ensancha la ventana
		// para que las demás peticiones lleguen al single-flight y se encolen en vez de refrescar.
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return authResult(newAccess, "r-new", exp), nil
	}}
	h := NewHandlerWithAPI(authTestCfg("http://unused"), api)
	router := middlewareProbeRouter(h)

	expiredAccess := makeToken(t, time.Now().Add(-time.Hour))
	codes := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.AddCookie(cookieFor(t, expiredAccess, "r-old"))
			router.ServeHTTP(rec, req)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}

	<-entered                         // el primer Refresh ya está en curso...
	time.Sleep(50 * time.Millisecond) // ...deja que las otras 19 se encolen en el single-flight.
	close(release)                    // libera la única ejecución; todas reusan su resultado.
	wg.Wait()

	if got := atomic.LoadInt64(&api.refreshCount); got != 1 {
		t.Fatalf("single-flight: debía haber UN solo Refresh para N peticiones concurrentes, got %d", got)
	}
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusOK {
			t.Errorf("petición %d debía continuar (200), got %d", i, codes[i])
		}
		if bodies[i] != newAccess {
			t.Errorf("petición %d debía reusar el access token nuevo, got %q", i, bodies[i])
		}
	}
}
