package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// failingReader agota la entropía: es lo que `wapp-shared/web` recibe en SecurityOptions.Rand.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("sin entropía") }

// TestCSPFailsClosedWithoutNonce verifica que si no se puede generar el nonce (entropía agotada), ESTE
// router responde 500 y NO sirve una página sin CSP (REQ-B2: fallar cerrado).
//
// El algoritmo del nonce y su fail-closed viven ahora en `wapp-shared/web` y allí tienen su test. Éste
// sigue aportando otra cosa: que el BFF le PASA su fuente de entropía al middleware y que respeta el
// 500 que devuelve. Un cableado que se olvidara del campo Rand dejaría el módulo verde y esta consola
// sirviendo con la fuente por defecto — es decir, con este criterio sin comprobar.
func TestCSPFailsClosedWithoutNonce(t *testing.T) {
	orig := entropy
	entropy = failingReader{}
	defer func() { entropy = orig }()

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
