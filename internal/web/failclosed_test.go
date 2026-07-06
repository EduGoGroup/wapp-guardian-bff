package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCSPFailsClosedWithoutNonce verifica que si no se puede generar el nonce (entropía agotada), el
// middleware de seguridad responde 500 y NO sirve una página sin CSP (REQ-B2: fallar cerrado).
func TestCSPFailsClosedWithoutNonce(t *testing.T) {
	orig := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("sin entropía") }
	defer func() { randRead = orig }()

	router := NewRouter(hardenedCfg())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("sin nonce el BFF debe fallar cerrado con 500, got %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("no debe emitirse CSP cuando falla la generación del nonce, got %q", csp)
	}
}
