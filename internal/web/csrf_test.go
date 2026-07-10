package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestCSRFRejectsPostWithoutToken: un POST mutante sin token CSRF (ni cookie ni campo) → 403, antes incluso
// de tocar la lógica del handler.
func TestCSRFRejectsPostWithoutToken(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(url.Values{"email": {"a@b.com"}, "password": {"secret"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST sin token CSRF debía dar 403, got %d", rec.Code)
	}
}

// TestCSRFRejectsTokenMismatch: cookie CSRF válida pero el campo del formulario trae otro valor → 403.
func TestCSRFRejectsTokenMismatch(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	csrf := mintCSRF(router)

	rec := httptest.NewRecorder()
	form := url.Values{"email": {"a@b.com"}, "password": {"secret"}, csrfFieldName: {"otro-valor-distinto"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("token del form que no coincide con la cookie debía dar 403, got %d", rec.Code)
	}
}

// TestCSRFAcceptsMatchingToken: el flujo real (GET siembra cookie+token; POST los presenta) pasa la
// validación CSRF y llega al handler (login OK → 303).
func TestCSRFAcceptsMatchingToken(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	access := makeToken(t, exp)
	api := fakeAPI(http.StatusOK, loginBody(access, "r-1", exp))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	csrf := mintCSRF(router)

	rec := httptest.NewRecorder()
	form := url.Values{"email": {"a@b.com"}, "password": {"secret"}, csrfFieldName: {csrf.Value}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login con token CSRF válido debía redirigir 303, got %d", rec.Code)
	}
}

// TestCSRFCookieIsHttpOnlyAndLax: la cookie del token es HttpOnly (el JS nunca la lee) y SameSite=Lax
// (fail-safe; no se envía en un POST cross-site).
func TestCSRFCookieIsHttpOnlyAndLax(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	router.ServeHTTP(rec, req)

	var raw string
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == csrfCookieName {
			raw = sc.Raw
		}
	}
	if raw == "" {
		t.Fatal("un GET debía sembrar la cookie CSRF")
	}
	if !strings.Contains(raw, "HttpOnly") {
		t.Errorf("la cookie CSRF debía ser HttpOnly, got %q", raw)
	}
	if !strings.Contains(raw, "SameSite=Lax") {
		t.Errorf("la cookie CSRF debía ser SameSite=Lax, got %q", raw)
	}
}

// TestLogoutGETNotAllowed: /logout ya no responde a GET (muta estado); solo POST. Un GET → 404/405.
func TestLogoutGETNotAllowed(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := getWithCookie(router, "/logout", validSessionCookie(t))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout debía dar 404/405, got %d", rec.Code)
	}
}

// TestLogoutPOSTClearsSession: POST /logout con token CSRF y sesión → borra la cookie de sesión (Max-Age=0)
// y redirige a /login, aunque la API upstream falle (best-effort).
func TestLogoutPOSTClearsSession(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := postFormWithCookie(router, "/logout", url.Values{}, validSessionCookie(t))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout debía redirigir 303 a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	raw := sessionSetCookie(rec)
	if raw == "" || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("POST /logout debía limpiar la cookie de sesión (Max-Age=0), got %q", raw)
	}
}

// TestGenerateCSRFTokenFailsClosed: si se agota la entropía, la generación del token falla (fail-closed) en
// vez de devolver un token vacío/predecible.
func TestGenerateCSRFTokenFailsClosed(t *testing.T) {
	orig := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("sin entropía") }
	defer func() { randRead = orig }()

	if _, err := generateCSRFToken(); err == nil {
		t.Fatal("generateCSRFToken debía fallar cuando no hay entropía")
	}
}
