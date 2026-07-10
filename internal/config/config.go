// Package config centraliza la configuración de la consola web BFF (wapp-guardian-bff).
//
// El BFF es un terminal server-side sin estado propio: sirve la UI, custodia el JWT en una cookie
// HttpOnly y relaya server-to-server contra la API pública de la plataforma (:8103 /api/v1). NO toca
// la BD, el Gateway CloudLink (gRPC del Edge) ni material criptográfico (zero-knowledge, INV-2).
//
// Todos los valores salen de variables de entorno (prefijo WAPP_) con defaults de desarrollo local;
// sin secretos hardcodeados (REQ-B5). La lectura se apoya en github.com/EduGoGroup/wapp-shared/config
// (REQ-B6: compartido de wApp, NUNCA edugo-*).
package config

import (
	"time"

	sharedconfig "github.com/EduGoGroup/wapp-shared/config"
)

// Config agrupa la configuración efectiva del servidor web BFF.
type Config struct {
	// Environment es el ambiente lógico ("local", "staging", "production"). Gobierna los defaults de
	// hardening sensibles a producción (Secure cookie, HSTS) sin activar cada flag a mano. Vacío o
	// "local" == postura permisiva de desarrollo (sin TLS).
	Environment string

	// HTTPAddr es la dirección de escucha del servidor (host:port). Default ":8104" (banda 81xx de wApp).
	HTTPAddr string

	// PublicAPIBaseURL es la URL base de la API pública REST (:8103 /api/v1) que el BFF relaya
	// server-to-server con el Bearer JWT del usuario. Único interlocutor del BFF (INV-1).
	PublicAPIBaseURL string

	// --- Hardening público ---

	// CookieSecure marca la cookie de sesión como Secure (solo se envía sobre TLS). Debe ser true en
	// producción (el BFF va detrás de HTTPS); en local va false porque no hay TLS. Default: true salvo
	// Environment="local".
	CookieSecure bool
	// CookieSameSite controla el atributo SameSite de la cookie: "lax" (default), "strict" o "none".
	// "none" obliga Secure=true. Mantiene resistencia CSRF sin romper la navegación same-site.
	CookieSameSite string

	// AllowedOrigins es la allowlist de orígenes CORS (CSV de orígenes completos). Vacío == same-origin
	// estricto (sin cabeceras CORS). NUNCA se acepta "*": el BFF es de mismo origen y el CORS es defensa
	// en profundidad (fail-closed).
	AllowedOrigins string

	// TrustedProxies es la lista (CSV de IPs o CIDRs) de proxies de confianza cuyo X-Forwarded-For se
	// honra para resolver ClientIP(). Vacío == NO se confía en ningún proxy: ClientIP() ignora las
	// cabeceras de reenvío y usa la IP de la conexión, blindando el rate-limit por IP de /login contra
	// suplantación del header. La topología del Plan 026 es TCP directo sin L7, así que el default vacío
	// es el correcto; se configura solo si el BFF queda detrás de un proxy de confianza.
	TrustedProxies string

	// HSTSEnabled emite Strict-Transport-Security. Solo tiene sentido tras TLS; default sigue a
	// CookieSecure (true salvo local) para no enviar HSTS sobre http:// en desarrollo.
	HSTSEnabled bool

	// RateLimitEnabled enciende el rate-limit por IP/usuario. Default true (defensa pública, REQ-B3).
	RateLimitEnabled bool
	// RateLimitRPS es la tasa sostenida (requests/segundo) por clave (IP o user_id). Default 5.
	RateLimitRPS float64
	// RateLimitBurst es la ráfaga máxima por clave (capacidad del bucket). Default 10.
	RateLimitBurst float64

	// --- Timeouts del http.Server (anti-slowloris, REQ-B4) ---
	// A diferencia de edugo-messaging-web NO hay SSE long-lived (el QR es local en el Edge, no pasa por
	// aquí), así que el BFF SÍ fija WriteTimeout además de los de lectura/idle.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Load resuelve la configuración desde variables de entorno (prefijo WAPP_) con defaults de desarrollo
// local. Las claves quedan bajo WAPP_GUARDIAN_* salvo WAPP_PUBLIC_API_BASE (compartida con el resto del
// ecosistema wApp).
func Load() Config {
	l := sharedconfig.New(sharedconfig.WithEnvPrefix("WAPP_"))

	env := l.GetString("GUARDIAN_ENV", "local")
	// "Producción" a efectos de hardening = cualquier ambiente que no sea "local": en ese caso los
	// defaults de seguridad se endurecen (Secure cookie + HSTS), salvo override explícito por env.
	secureDefault := env != "local"

	return Config{
		Environment:      env,
		HTTPAddr:         l.GetString("GUARDIAN_HTTP_ADDR", ":8104"),
		PublicAPIBaseURL: l.GetString("PUBLIC_API_BASE", "http://localhost:8103"),

		CookieSecure:   l.GetBool("GUARDIAN_COOKIE_SECURE", secureDefault),
		CookieSameSite: l.GetString("GUARDIAN_COOKIE_SAMESITE", "lax"),
		AllowedOrigins: l.GetString("GUARDIAN_ALLOWED_ORIGINS", ""), // vacío == same-origin; NUNCA "*".
		TrustedProxies: l.GetString("GUARDIAN_TRUSTED_PROXIES", ""), // vacío == no se confía en ningún proxy.
		HSTSEnabled:    l.GetBool("GUARDIAN_HSTS_ENABLED", secureDefault),

		RateLimitEnabled: l.GetBool("GUARDIAN_RATE_ENABLED", true),
		RateLimitRPS:     float64(l.GetInt("GUARDIAN_RATE_RPS", 5)),
		RateLimitBurst:   float64(l.GetInt("GUARDIAN_RATE_BURST", 10)),

		ReadHeaderTimeout: time.Duration(l.GetInt("GUARDIAN_READ_HEADER_TIMEOUT_SECS", 5)) * time.Second,
		ReadTimeout:       time.Duration(l.GetInt("GUARDIAN_READ_TIMEOUT_SECS", 15)) * time.Second,
		WriteTimeout:      time.Duration(l.GetInt("GUARDIAN_WRITE_TIMEOUT_SECS", 30)) * time.Second,
		IdleTimeout:       time.Duration(l.GetInt("GUARDIAN_IDLE_TIMEOUT_SECS", 60)) * time.Second,
	}
}
