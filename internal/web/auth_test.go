package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/auth"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// authTestCfg arma una config apuntando al servidor API fake, con rate-limit apagado y cookie no-Secure
// (para que la cookie se emita sobre el http:// de httptest).
func authTestCfg(apiURL string) *config.Config {
	return &config.Config{
		Environment:      "local",
		HTTPAddr:         ":0",
		PublicAPIBaseURL: apiURL,
		CookieSecure:     false,
		CookieSameSite:   "lax",
		RateLimitEnabled: false,
		UpstreamTimeout:  5 * time.Second, // holgado: los fakes responden al instante.
	}
}

// makeToken firma un JWT con exp dado. El BFF NO verifica la firma (parse-unverified), así que el secreto
// es irrelevante; solo importa que el token sea parseable y lleve el claim exp.
func makeToken(t *testing.T, exp time.Time) string {
	t.Helper()
	claims := auth.Claims{
		UserID:           "u-1",
		TenantID:         "t-1",
		Roles:            []string{"admin"},
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(exp)},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("no se pudo firmar el token de prueba: %v", err)
	}
	return signed
}

// loginBody arma el JSON que devuelve /api/v1/auth/login|refresh con el access token dado.
func loginBody(accessToken, refreshToken string, exp time.Time) string {
	return fmt.Sprintf(
		`{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_at":%q,`+
			`"context":{"tenant_id":"t-1","user_id":"u-1","roles":["admin"]}}`,
		accessToken, refreshToken, exp.Format(time.RFC3339),
	)
}

// fakeAPI levanta un servidor que responde a cualquier ruta con status/body fijos (API pública simulada).
func fakeAPI(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

// sessionSetCookie devuelve el primer Set-Cookie de la cookie de sesión (o "" si no hay).
func sessionSetCookie(rec *httptest.ResponseRecorder) string {
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == sessionCookieName {
			return sc.Raw
		}
	}
	return ""
}

// mintCSRF obtiene una cookie CSRF válida haciendo un GET público (el CSRFMiddleware la siembra). Su valor
// ES el token double-submit que los POST deben reflejar en el campo del formulario.
func mintCSRF(router http.Handler) *http.Cookie {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	router.ServeHTTP(rec, req)
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == csrfCookieName {
			return sc
		}
	}
	return &http.Cookie{Name: csrfCookieName, Value: ""}
}

func postForm(router http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	csrf := mintCSRF(router)
	form.Set(csrfFieldName, csrf.Value)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	router.ServeHTTP(rec, req)
	return rec
}

// TestLoginOKSetsHttpOnlyCookie: login correcto → 303 + cookie de sesión HttpOnly (REQ-C2).
func TestLoginOKSetsHttpOnlyCookie(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	access := makeToken(t, exp)
	api := fakeAPI(http.StatusOK, loginBody(access, "r-1", exp))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/login", url.Values{"email": {"a@b.com"}, "password": {"secret"}})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login OK debía redirigir 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("login OK debía redirigir a /, got %q", loc)
	}
	raw := sessionSetCookie(rec)
	if raw == "" {
		t.Fatal("login OK debía emitir la cookie de sesión")
	}
	if !strings.Contains(raw, "HttpOnly") {
		t.Errorf("la cookie de sesión debía ser HttpOnly, got %q", raw)
	}
	// El JWT NUNCA debe aparecer en claro en la cookie (va base64 dentro del JSON).
	if strings.Contains(raw, access) {
		t.Error("el access token no debía viajar en claro en la cookie")
	}
}

// TestLoginBadNoCookie: credenciales inválidas → 401 sin cookie, sin filtrar detalle (REQ-C3).
func TestLoginBadNoCookie(t *testing.T) {
	api := fakeAPI(http.StatusUnauthorized, `{"error":"invalid_credentials"}`)
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/login", url.Values{"email": {"a@b.com"}, "password": {"nope"}})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login malo debía responder 401, got %d", rec.Code)
	}
	if raw := sessionSetCookie(rec); raw != "" {
		t.Errorf("login malo NO debía emitir cookie de sesión, got %q", raw)
	}
	if strings.Contains(rec.Body.String(), "invalid_credentials") {
		t.Error("no debe filtrarse el detalle del upstream al usuario")
	}
}

// TestProtectedWithoutCookieRedirects: ruta protegida sin cookie → redirect /login (REQ-C4).
func TestProtectedWithoutCookieRedirects(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ruta protegida sin sesión debía redirigir 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("debía redirigir a /login, got %q", loc)
	}
}

// TestProtectedExpiredTokenClearsCookie: cookie con token expirado → cookie limpiada + redirect /login.
func TestProtectedExpiredTokenClearsCookie(t *testing.T) {
	expired := makeToken(t, time.Now().Add(-time.Hour))
	value, err := encodeSession(sessionData{AccessToken: expired, RefreshToken: "r-x"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}

	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("token expirado debía redirigir 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("debía redirigir a /login, got %q", loc)
	}
	// La cookie debe limpiarse (Max-Age=0 → el navegador la borra).
	raw := sessionSetCookie(rec)
	if raw == "" || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("la cookie debía limpiarse (Max-Age=0), got %q", raw)
	}
}

// TestValidSessionReachesHome: cookie válida → la home renderiza 200 (no redirige).
func TestValidSessionReachesHome(t *testing.T) {
	access := makeToken(t, time.Now().Add(time.Hour))
	value, err := encodeSession(sessionData{AccessToken: access, RefreshToken: "r-ok"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}

	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sesión válida debía renderizar la home 200, got %d", rec.Code)
	}
}

// TestRefreshSessionRenewsCookie ejercita el andamiaje de REQ-C6 (lo consumirán T3/T4): con un refresh
// token en contexto, refreshSession renueva y re-emite la cookie.
func TestRefreshSessionRenewsCookie(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	newAccess := makeToken(t, exp)
	api := fakeAPI(http.StatusOK, loginBody(newAccess, "r-new", exp))
	defer api.Close()

	h := NewHandler(authTestCfg(api.URL))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(ctxRefreshToken, "r-old")

	tok, err := h.refreshSession(c)
	if err != nil {
		t.Fatalf("refreshSession no debía fallar: %v", err)
	}
	if tok != newAccess {
		t.Error("refreshSession debía devolver el nuevo access token")
	}
	if raw := sessionSetCookie(rec); raw == "" || !strings.Contains(raw, "HttpOnly") {
		t.Errorf("refreshSession debía re-emitir la cookie HttpOnly, got %q", raw)
	}
}
