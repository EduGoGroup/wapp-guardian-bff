package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthzOK verifica que /healthz responde 200 (liveness probe, REQ-B5).
func TestHealthzOK(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz debía responder 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "healthy") {
		t.Errorf("/healthz debía reportar estado healthy, got %q", rec.Body.String())
	}
}

// TestCSSServedSameOrigin verifica que el design system se sirve mismo-origen desde el propio BFF con
// Content-Type CSS (sin CDNs: encaja con la CSP endurecida).
func TestCSSServedSameOrigin(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("el CSS debía servirse 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("el CSS debía tener Content-Type text/css, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "--md-primary") {
		t.Errorf("el CSS servido debía incluir los tokens MD3 (--md-primary), got %d bytes", len(body))
	}
	// No debe referenciar orígenes externos (mismo-origen puro).
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Errorf("el CSS no debe referenciar CDNs/orígenes externos")
	}
}

// TestHomeRenders verifica que la home renderiza el layout SSR con el CSS enlazado mismo-origen.
func TestHomeRenders(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("la home debía responder 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/static/css/app.css"`) {
		t.Error("la home debía enlazar el CSS mismo-origen /static/css/app.css")
	}
	if !strings.Contains(body, "wApp") {
		t.Error("la home debía renderizar la marca wApp")
	}
}
