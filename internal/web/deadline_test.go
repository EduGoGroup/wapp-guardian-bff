package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRequestDeadlineBoundsSlowUpstream verifica T3/H4: si el upstream tarda más que UpstreamTimeout, el
// handler no se cuelga esperándolo —el deadline por petición corta la llamada y la portada cae a su modo
// degradado dentro del presupuesto, muy por debajo del timeout de 15s del cliente HTTP.
//
// 📌 El aviso que se busca era el del listado de sesiones hasta el Plan 047 · T2.1; con el dashboard
// retirado, la llamada que la portada hace —y que el deadline corta— es la de las capacidades, así que
// el degradado que se ve es el suyo. Lo que el test mide no cambió: que el corte llega y que la página
// se pinta igual.
func TestRequestDeadlineBoundsSlowUpstream(t *testing.T) {
	// Upstream deliberadamente lento: duerme mucho más que el deadline por petición.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer slow.Close()

	cfg := authTestCfg(slow.URL)
	cfg.UpstreamTimeout = 150 * time.Millisecond

	router := NewRouter(cfg)

	start := time.Now()
	rec := getWithCookie(router, "/", validSessionCookie(t))
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("el handler debía cortar por el deadline (~150ms), no esperar al upstream lento; tardó %s", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("la portada degradada debía seguir sirviendo 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No se pudo consultar el plan del tenant") {
		t.Error("al vencer el deadline la portada debía degradar (avisar de que el plan no se pudo consultar)")
	}
}

// TestRequestDeadlineDisabledWhenZero verifica que UpstreamTimeout <= 0 deja pasar la petición sin instalar
// deadline (el middleware es transparente): un upstream instantáneo responde normal.
func TestRequestDeadlineDisabledWhenZero(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/entitlements": {http.StatusOK, entitlementsBody("commerce", "cart_basic")},
	})
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 0 // desactivado

	router := NewRouter(cfg)
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("sin deadline la portada debía servir 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "plan · commerce") {
		t.Error("sin deadline la llamada normal debía completarse y pintarse")
	}
}
