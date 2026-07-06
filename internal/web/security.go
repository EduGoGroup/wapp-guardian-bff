package web

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// cspNonceKey es la clave en el gin.Context donde vive el nonce CSP de la petición actual. El
// renderizador (handlers.render) lo lee para inyectarlo en las plantillas (<style>/<script> inline).
const cspNonceKey = "csp_nonce"

// randRead es la fuente de aleatoriedad del nonce (crypto/rand por defecto). Es una variable para que
// los tests puedan forzar un fallo de entropía y verificar que la CSP falla cerrado (500) en vez de
// servir sin nonce.
var randRead = rand.Read

// SecurityHeadersMiddleware añade las cabeceras de seguridad de una web pública y siembra un nonce CSP
// por petición.
//
// CSP (defensa anti-XSS): `default-src 'self'`. A diferencia de edugo-messaging-web, el BFF NO carga
// terceros por CDN (ni Google Fonts ni Font Awesome): la estética Material Design 3 se sirve mismo-origen
// (CSS embebido con //go:embed) y la tipografía es system-font. Eso permite endurecer `style-src`/
// `font-src` a `'self'` (+ nonce para el CSS crítico inline) sin allowlist de CDNs.
//
// Los bloques inline mínimos (p. ej. el CSS crítico anti-flash del layout) NO usan `'unsafe-inline'`:
// cada bloque lleva el nonce de la petición, así un estilo/script inyectado (sin el nonce) no ejecuta.
//
// HSTS solo se emite cuando cfg.HSTSEnabled (tras TLS); enviarlo sobre http:// en local no aporta y
// puede ensuciar el navegador del desarrollador.
func SecurityHeadersMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		nonce, err := generateNonce()
		if err != nil {
			// Sin nonce no podemos servir una CSP segura con inline: fallar cerrado (REQ-B2).
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Set(cspNonceKey, nonce)

		h := c.Writer.Header()
		h.Set("Content-Security-Policy", buildCSP(nonce))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Limita las APIs potentes del navegador que este BFF no usa.
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		if cfg.HSTSEnabled {
			// 1 año + subdominios. preload se omite a propósito (decisión de despliegue, no de la app).
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

// buildCSP arma la Content-Security-Policy con el nonce de la petición. Todo es mismo-origen (`'self'`);
// el `'nonce-...'` autoriza solo los bloques inline propios; jamás `*` ni CDNs de terceros.
func buildCSP(nonce string) string {
	directives := []string{
		"default-src 'self'",
		fmt.Sprintf("script-src 'self' 'nonce-%s'", nonce),
		fmt.Sprintf("style-src 'self' 'nonce-%s'", nonce),
		"font-src 'self'",
		"img-src 'self' data:", // iconos/ilustraciones SVG inline pueden viajar como data:.
		"connect-src 'self'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'", // refuerza X-Frame-Options: DENY.
		"object-src 'none'",
	}
	return strings.Join(directives, "; ")
}

// generateNonce produce un nonce CSP aleatorio (128 bits) único por petición.
//
// Usa base64 URL-safe SIN padding (alfabeto A-Za-z0-9-_): a diferencia del base64 estándar, no contiene
// '+', '/' ni '=', caracteres que html/template ESCAPA dentro del atributo `nonce` (p. ej. '+' -> '&#43;').
// Ese escape haría que el nonce del atributo renderizado difiera del de la cabecera CSP. Con el alfabeto
// URL-safe, cabecera y atributo quedan byte-idénticos; el valor sigue siendo un base64 válido para CSP.
func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// nonceFromCtx recupera el nonce CSP sembrado por SecurityHeadersMiddleware (cadena vacía si no hay).
func nonceFromCtx(c *gin.Context) string {
	if v, ok := c.Get(cspNonceKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// CORSMiddleware aplica una política CORS fail-closed: solo refleja un Origin presente en la allowlist
// (cfg.AllowedOrigins, CSV). NUNCA emite `*` ni hace eco de un origen no listado. Si la allowlist está
// vacía, no se emite ninguna cabecera CORS (postura same-origin: el navegador bloquea el cross-origin
// por defecto). El BFF es de mismo origen; el CORS es defensa en profundidad.
func CORSMiddleware(cfg *config.Config) gin.HandlerFunc {
	allowed := parseOrigins(cfg.AllowedOrigins)

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin) // eco del origen exacto, jamás "*".
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			appendVary(c, "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			// Preflight: 204 sin cuerpo. Si el origen no estaba permitido, va sin cabeceras CORS.
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// parseOrigins convierte el CSV de orígenes a un set, descartando vacíos y, por seguridad, cualquier
// "*" que se haya colado por config (el BFF nunca debe abrir wildcard).
func parseOrigins(csv string) map[string]bool {
	set := make(map[string]bool)
	for _, raw := range strings.Split(csv, ",") {
		o := strings.TrimSpace(raw)
		if o == "" || o == "*" {
			continue
		}
		set[o] = true
	}
	return set
}

// appendVary agrega un valor a la cabecera Vary sin pisar lo existente (correcto cacheo de CORS).
func appendVary(c *gin.Context, value string) {
	h := c.Writer.Header()
	existing := h.Get("Vary")
	if existing == "" {
		h.Set("Vary", value)
	} else if !strings.Contains(existing, value) {
		h.Set("Vary", existing+", "+value)
	}
}
