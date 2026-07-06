package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// TestRateLimitReturns429 verifica que al exceder la ráfaga el BFF responde 429 con Retry-After.
func TestRateLimitReturns429(t *testing.T) {
	cfg := &config.Config{
		Environment:      "production",
		HTTPAddr:         ":0",
		PublicAPIBaseURL: "http://api.invalid",
		RateLimitEnabled: true,
		RateLimitRPS:     1,
		RateLimitBurst:   2, // solo 2 peticiones inmediatas antes del 429.
	}
	router := NewRouter(cfg)

	do := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "203.0.113.7:12345" // misma IP -> misma clave de rate-limit.
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	// Las primeras 2 (burst) pasan.
	if c := do(); c != http.StatusOK {
		t.Fatalf("1ª petición debía pasar (200), got %d", c)
	}
	if c := do(); c != http.StatusOK {
		t.Fatalf("2ª petición debía pasar (200), got %d", c)
	}

	// La 3ª agota la ráfaga -> 429 con Retry-After.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:12345"
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("al exceder la ráfaga debía dar 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("el 429 debe incluir la cabecera Retry-After")
	}
}

// TestRateLimitPerKey verifica que el límite es por clave (IP): otra IP no queda afectada por la que
// agotó su ráfaga.
func TestRateLimitPerKey(t *testing.T) {
	cfg := &config.Config{
		Environment:      "production",
		HTTPAddr:         ":0",
		PublicAPIBaseURL: "http://api.invalid",
		RateLimitEnabled: true,
		RateLimitRPS:     1,
		RateLimitBurst:   1,
	}
	router := NewRouter(cfg)

	hit := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = ip + ":10000"
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := hit("198.51.100.1"); c != http.StatusOK {
		t.Fatalf("primera IP debía pasar, got %d", c)
	}
	if c := hit("198.51.100.1"); c != http.StatusTooManyRequests {
		t.Fatalf("primera IP debía agotar su ráfaga (429), got %d", c)
	}
	// Otra IP tiene su propio bucket: debe pasar.
	if c := hit("198.51.100.2"); c != http.StatusOK {
		t.Fatalf("segunda IP (otra clave) debía pasar, got %d", c)
	}
}
