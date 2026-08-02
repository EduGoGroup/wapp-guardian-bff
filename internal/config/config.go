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
	// server-to-server con el Bearer JWT del usuario. Único interlocutor del BFF mientras la
	// delegación esté apagada; con IdentityBaseURL puesta, las credenciales viajan a identity y aquí
	// se queda el canje del token y todo el tráfico de negocio.
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

	// ShutdownTimeout acota el apagado graceful: al recibir SIGINT/SIGTERM el servidor deja de aceptar
	// conexiones nuevas y espera hasta este plazo a que las peticiones en vuelo terminen antes de forzar
	// el cierre. Default 10s.
	ShutdownTimeout time.Duration

	// UpstreamTimeout acota, por petición entrante, TODA la cadena de llamadas a la API pública (incluida
	// la secuencia withAuthRetry: intento → refresh → reintento). Sin este tope la cadena podía encadenar
	// hasta 3 llamadas de 15s (45s) bajo un WriteTimeout de 30s y romper el render a mitad. DEBE quedar por
	// debajo de WriteTimeout para que el modo degradado alcance a pintarse. 0 o negativo lo desactiva.
	// Default 20s.
	UpstreamTimeout time.Duration

	// EnableAlphaTestAccounts habilita el renderizado del selector de "Usuario de prueba (Alpha)" en la UI de login.
	// Se activa mediante WAPP_ALPHA_TEST_ACCOUNTS=true o WAPP_ENABLE_ALPHA_LOGIN=true. Default: false (fail-closed).
	EnableAlphaTestAccounts bool

	// IdentityJWKSURL es el endpoint JWKS de identity-api (identity-core) del que salen las claves
	// públicas para verificar Identity Tokens. Es la PUERTA del modo dual (identity Plan 003, T1.2):
	// vacío == modo dual apagado y el BFF arranca sin identity, exactamente como hoy.
	//
	// Con valor, el verificador se construye en el arranque y es fail-closed: hace su primer fetch ahí
	// mismo y, si identity-api no responde, el BFF NO arranca. Es deliberado —un verificador nacido con
	// cero claves rechazaría todo token como si fuera credencial inválida— y es la razón de que la
	// puerta exista: la Ola 1 no puede imponerle a wApp una dependencia de arranque contra identity.
	//
	// Local: http://localhost:8200/.well-known/jwks.json (http solo se admite contra loopback).
	IdentityJWKSURL string

	// IdentityBaseURL es la URL base de identity-api (:8200), el único emisor de Identity Tokens del
	// grupo. Es la PUERTA de la delegación (identity Plan 003, T3.2): vacía == delegación APAGADA y el
	// BFF autentica contra la API pública de wApp exactamente como hasta hoy.
	//
	// Con valor, el login y el refresh viajan a identity con el system "wapp.bff" y el Identity Token
	// resultante se canjea al instante por un Context Token de wApp (POST /api/v1/auth/exchange de la
	// plataforma). El Identity Token NO se persiste: vive solo el instante server-side del canje, así
	// que la cookie de sesión custodia SIEMPRE el Context Token —de donde sale el tenant— y el refresh
	// de identity. El logout revoca en identity solo esa sesión: la del Edge sobrevive (T3.4).
	//
	// El canje exige que la plataforma tenga su propio verificador de Identity Tokens construido; si no
	// lo tiene, responde 503 y el login no prospera. Son dos puertas independientes a propósito.
	//
	// Local: http://localhost:8200
	IdentityBaseURL string
}

// Load resuelve la configuración desde variables de entorno (prefijo WAPP_) con defaults de desarrollo
// local. Las claves quedan bajo WAPP_GUARDIAN_* salvo WAPP_PUBLIC_API_BASE y WAPP_IDENTITY_JWKS_URL,
// compartidas con el resto del ecosistema wApp.
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

		ShutdownTimeout: time.Duration(l.GetInt("GUARDIAN_SHUTDOWN_TIMEOUT_SECS", 10)) * time.Second,
		UpstreamTimeout: time.Duration(l.GetInt("GUARDIAN_UPSTREAM_TIMEOUT_SECS", 20)) * time.Second,

		EnableAlphaTestAccounts: l.GetBool("ALPHA_TEST_ACCOUNTS", l.GetBool("ENABLE_ALPHA_LOGIN", false)),

		IdentityJWKSURL: l.GetString("IDENTITY_JWKS_URL", ""), // vacío == modo dual apagado.
		IdentityBaseURL: l.GetString("IDENTITY_URL", ""),      // vacío == delegación apagada (flujo legacy).
	}
}
