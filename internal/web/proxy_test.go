package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// TestRateLimitIgnoresForwardedForByDefault es la prueba del blindaje H1/T1: con TrustedProxies vacío
// (default), ClientIP() ignora X-Forwarded-For, así que un atacante que rota el header desde la misma
// conexión sigue compartiendo la clave de rate-limit y termina en 429. Sin SetTrustedProxies(nil) Gin
// confiaría en todos los proxies y cada XFF distinto evadiría el límite.
func TestRateLimitIgnoresForwardedForByDefault(t *testing.T) {
	cfg := &config.Config{
		Environment:      "production",
		HTTPAddr:         ":0",
		PublicAPIBaseURL: "http://api.invalid",
		RateLimitEnabled: true,
		RateLimitRPS:     1,
		RateLimitBurst:   2,
		TrustedProxies:   "", // no se confía en ningún proxy.
	}
	router := NewRouter(cfg)

	do := func(xff string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "203.0.113.7:12345" // misma conexión física.
		req.Header.Set("X-Forwarded-For", xff)
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	// Ráfaga=2: las 2 primeras pasan aunque el XFF cambie en cada una.
	if c := do("1.1.1.1"); c != http.StatusOK {
		t.Fatalf("1ª petición debía pasar (200), got %d", c)
	}
	if c := do("2.2.2.2"); c != http.StatusOK {
		t.Fatalf("2ª petición debía pasar (200), got %d", c)
	}
	// La 3ª, con OTRO XFF, igual cae en 429: el header no cambia la clave.
	if c := do("3.3.3.3"); c != http.StatusTooManyRequests {
		t.Fatalf("rotar X-Forwarded-For no debe evadir el rate-limit; esperaba 429, got %d", c)
	}
}

// TestNewRouterPanicsOnInvalidTrustedProxies verifica el fail-closed: una lista de proxies malformada
// aborta el arranque en vez de dejar ClientIP() en un estado no fiable.
func TestNewRouterPanicsOnInvalidTrustedProxies(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("un CIDR inválido en TrustedProxies debía provocar panic en el arranque")
		}
	}()
	cfg := hardenedCfg()
	cfg.TrustedProxies = "no-es-una-ip"
	NewRouter(cfg)
}
