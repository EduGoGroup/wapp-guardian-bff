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

// TestHomeRenders verifica que una página SSR pública renderiza el layout con el CSS enlazado mismo-origen.
// T2 protegió "/" con el AuthMiddleware, así que se usa /login (público) para comprobar el render del
// layout maestro (base.html + marca wApp + CSS).
func TestHomeRenders(t *testing.T) {
	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("la página de login debía responder 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/static/css/app.css"`) {
		t.Error("la home debía enlazar el CSS mismo-origen /static/css/app.css")
	}
	if !strings.Contains(body, "wApp") {
		t.Error("la home debía renderizar la marca wApp")
	}
}

// TestSharedCSSServedSameOrigin verifica que los nuevos tokens y componentes compartidos se sirven mismo-origen.
func TestSharedCSSServedSameOrigin(t *testing.T) {
	router := NewRouter(hardenedCfg())

	cssFiles := []string{
		"/static/css/wapp-tokens.css",
		"/static/css/wapp-components.css",
		"/static/css/theme-bff.css",
	}

	for _, path := range cssFiles {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s debía responder 200, got %d", path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
				t.Errorf("%s debía tener Content-Type text/css, got %q", path, ct)
			}
		})
	}
}

// TestKillSwitchAlphaUserSelect verifica el comportamiento fail-closed del Kill-Switch de cuentas Alpha.
func TestKillSwitchAlphaUserSelect(t *testing.T) {
	t.Run("KillSwitch_OFF_Default", func(t *testing.T) {
		cfg := hardenedCfg()
		cfg.EnableAlphaTestAccounts = false
		router := NewRouter(cfg)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /login debía responder 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "alpha-user-container") || strings.Contains(body, "alpha-user-select") {
			t.Errorf("FAIL-CLOSED VIOLATION: el selector Alpha se renderizó estando la flag deshabilitada")
		}
	})

	t.Run("KillSwitch_ON", func(t *testing.T) {
		cfg := hardenedCfg()
		cfg.EnableAlphaTestAccounts = true
		router := NewRouter(cfg)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /login debía responder 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "alpha-user-container") || !strings.Contains(body, "alpha-user-select") {
			t.Errorf("El selector Alpha debía renderizarse cuando EnableAlphaTestAccounts es true")
		}
	})
}
