package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// hardenedCfg arma una config con hardening explícito (HSTS on, rate-limit off) para los tests de
// cabeceras. PublicAPIBase apunta a un destino inválido: estos tests solo miran cabeceras, no negocio.
func hardenedCfg() *config.Config {
	return &config.Config{
		Environment:      "production",
		HTTPAddr:         ":0",
		PublicAPIBaseURL: "http://api.invalid",
		CookieSecure:     true,
		CookieSameSite:   "lax",
		HSTSEnabled:      true,
		RateLimitEnabled: false, // los tests de cabeceras no deben competir con el límite.
	}
}

func TestSecurityHeadersPresent(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	h := rec.Header()
	cases := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	}
	for header, want := range cases {
		if got := h.Get(header); got != want {
			t.Errorf("cabecera %s: esperaba %q, got %q", header, want, got)
		}
	}

	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("falta la cabecera Content-Security-Policy")
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("la CSP debe arrancar de default-src 'self', got %q", csp)
	}
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Errorf("la CSP NO debe usar 'unsafe-inline' (se usa nonce), got %q", csp)
	}
	if !strings.Contains(csp, "'nonce-") {
		t.Errorf("la CSP debe llevar un nonce para los inline, got %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("la CSP debe negar el embebido (frame-ancestors 'none'), got %q", csp)
	}
	// Endurecido: el BFF no carga CDNs de terceros; style-src/font-src deben ser mismo-origen.
	if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
		t.Errorf("la CSP no debe enumerar orígenes externos (CSS/fuentes mismo-origen), got %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'nonce-") {
		t.Errorf("style-src debe ser 'self' + nonce (sin CDNs), got %q", csp)
	}
}

// TestHSTSOmittedWhenDisabled verifica que en local (sin TLS) NO se emite HSTS.
func TestHSTSOmittedWhenDisabled(t *testing.T) {
	cfg := hardenedCfg()
	cfg.HSTSEnabled = false
	router := NewRouter(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("no debe emitirse HSTS cuando HSTSEnabled=false, got %q", got)
	}
}

// TestNonceInjectedIntoTemplate verifica que el nonce de la CSP (cabecera) es el MISMO que el del
// <style> inline de la página. Si no coincidieran, el navegador bloquearía el bloque y la UI se rompería.
func TestNonceInjectedIntoTemplate(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	nonce := extractCSPNonce(t, rec.Header().Get("Content-Security-Policy"))
	if nonce == "" {
		t.Fatal("el nonce de la CSP está vacío")
	}
	if !strings.Contains(rec.Body.String(), `nonce="`+nonce+`"`) {
		t.Errorf("el nonce de la CSP (%q) no aparece en los <style>/<script> de la página", nonce)
	}
}

// TestNonceIsPerRequest verifica que el nonce cambia entre peticiones (no es estático/predecible).
func TestNonceIsPerRequest(t *testing.T) {
	router := NewRouter(hardenedCfg())

	extract := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(rec, req)
		return extractCSPNonce(t, rec.Header().Get("Content-Security-Policy"))
	}

	if first, second := extract(), extract(); first == second {
		t.Error("el nonce CSP debe ser único por petición, se repitió entre dos peticiones")
	}
}

func TestCORSAllowsListedOrigin(t *testing.T) {
	cfg := hardenedCfg()
	cfg.AllowedOrigins = "https://consola.wapp.test"
	router := NewRouter(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://consola.wapp.test")
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://consola.wapp.test" {
		t.Errorf("se esperaba eco del origen permitido, got %q", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	cfg := hardenedCfg()
	cfg.AllowedOrigins = "https://consola.wapp.test"
	router := NewRouter(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("un origen no permitido no debe recibir Access-Control-Allow-Origin, got %q", got)
	}
}

// TestCORSWildcardConfigIsIgnored verifica que aunque se configure "*", NUNCA se emite wildcard.
func TestCORSWildcardConfigIsIgnored(t *testing.T) {
	cfg := hardenedCfg()
	cfg.AllowedOrigins = "*"
	router := NewRouter(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://anything.example.com")
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf(`con AllowedOrigins="*" no debe permitirse ningún origen, got %q`, got)
	}
}

// extractCSPNonce saca el valor del `'nonce-...'` del directive de la CSP.
func extractCSPNonce(t *testing.T, csp string) string {
	t.Helper()
	idx := strings.Index(csp, "'nonce-")
	if idx == -1 {
		t.Fatalf("no se encontró nonce en la CSP: %q", csp)
	}
	rest := csp[idx+len("'nonce-"):]
	end := strings.Index(rest, "'")
	if end == -1 {
		t.Fatalf("nonce mal formado en la CSP: %q", csp)
	}
	return rest[:end]
}
