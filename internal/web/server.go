// Package web es el servidor Gin de la consola BFF de wApp (SSR endurecido).
//
// El BFF sirve la UI Material Design 3 mismo-origen, custodia el JWT en una cookie HttpOnly y relaya
// server-to-server contra la API pública REST (:8103 /api/v1). NO habla gRPC con el Gateway/Edge ni
// toca material criptográfico (zero-knowledge). A diferencia de edugo-messaging-web NO hay relay SSE del
// QR (el emparejamiento es local en el Edge), así que el http.Server SÍ fija WriteTimeout.
package web

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
	"github.com/EduGoGroup/wapp-shared/ui"
)

//go:embed templates
var templatesFS embed.FS

// appCSS es el design system Material Design 3 propio de wApp (teal/verde). Se sirve mismo-origen en
// /static/css/app.css con Content-Type text/css, sin CDNs (encaja con la CSP endurecida).
//
//go:embed static/css/app.css
var appCSS []byte

// NewRouter construye el *gin.Engine completo (plantillas + rutas + middlewares) sin dependencias
// externas opcionales. Se expone para que los tests lo monten con httptest sin levantar un puerto real.
func NewRouter(cfg *config.Config) *gin.Engine {
	router, _ := newRouterWithLimiter(cfg)
	return router
}

// newRouterWithLimiter es como NewRouter pero además devuelve el rate-limiter para poder cerrar su
// goroutine de barrido (lo usa Run para la vida del proceso y los tests para no filtrar goroutines).
func newRouterWithLimiter(cfg *config.Config) (*gin.Engine, *keyedRateLimiter) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	// Proxies de confianza: por defecto (lista vacía) NO se confía en ninguno, de modo que ClientIP()
	// ignora X-Forwarded-For y usa la IP de la conexión. Esto blinda el rate-limit por IP de /login
	// (única defensa anti fuerza-bruta) contra la suplantación del header. Solo se confía en la lista
	// explícita cuando el BFF queda detrás de un proxy de confianza (WAPP_GUARDIAN_TRUSTED_PROXIES).
	if err := router.SetTrustedProxies(parseTrustedProxies(cfg.TrustedProxies)); err != nil {
		// Config inválida en el arranque: fail-closed (como el panic al compilar plantillas). Mejor no
		// arrancar que hacerlo con una allowlist de proxies malformada y un ClientIP() no fiable.
		slog.Error("lista de proxies de confianza inválida", "valor", cfg.TrustedProxies, "error", err)
		panic(err)
	}
	router.Use(gin.Recovery())
	router.Use(slogMiddleware())
	// Cabeceras de seguridad + nonce CSP por petición (antes de los handlers que renderizan).
	router.Use(SecurityHeadersMiddleware(cfg))
	// CORS fail-closed (allowlist, nunca "*"); same-origin por defecto.
	router.Use(CORSMiddleware(cfg))

	var rateLimiter *keyedRateLimiter // nil cuando el rate-limit está apagado.
	if cfg.RateLimitEnabled {
		var rlMiddleware gin.HandlerFunc
		rlMiddleware, rateLimiter = RateLimitMiddleware(cfg)
		// Rate-limit global (antes de auth): clava por user_id si hay sesión, si no por IP.
		router.Use(rlMiddleware)
	}

	// Motor de plantillas con el helper `yield`: base.html es el layout maestro y ejecuta el fragmento
	// de página (pages/*.html) que le indica ContentTemplate. El conjunto es LOCAL a este router (no un
	// global mutable): el helper `yield` cierra sobre `tmpl`, que se asigna tras el parse, así cada router
	// tiene su propio motor y los tests pueden montar routers en paralelo sin compartir estado.
	var tmpl *template.Template
	root := template.New("").Funcs(template.FuncMap{
		// hasPrefix resalta el enlace activo de la navegación (app-bar): la sección se decide por el
		// prefijo del path (p. ej. "/flows/menu" activa "Flujos").
		"hasPrefix": strings.HasPrefix,
		// statusLabel traduce la clave del ciclo de vida de una solicitud al nombre de negocio. Es
		// presentación pura: lo que se puede hacer con ese estado lo dice la plataforma, no esta tabla.
		"statusLabel": intakeStatusLabel,
		"yield": func(name string, data interface{}) (template.HTML, error) {
			if name == "" {
				return "", nil
			}
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
				slog.Error("error al renderizar plantilla yield", "nombre", name, "error", err)
				return "", err
			}
			return template.HTML(buf.String()), nil // #nosec G203 -- fragmento de plantilla propia.
		},
	})
	tmpl, err := root.ParseFS(templatesFS,
		"templates/layouts/*.html",
		"templates/pages/*.html",
	)
	if err != nil {
		slog.Error("no se pudieron compilar las plantillas HTML", "error", err)
		panic(err)
	}
	router.SetHTMLTemplate(tmpl)

	h := NewHandler(cfg)

	// CSS propio (Material Design 3) servido mismo-origen, sin CDNs. Cache moderada (1h): el contenido
	// cambia solo con un deploy, así que un revalidate frecuente basta.
	router.GET("/static/css/app.css", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/css; charset=utf-8", appCSS)
	})

	// Estilos compartidos de UI Tokens de wApp ecosistema
	router.GET("/static/css/wapp-tokens.css", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		data, err := ui.GetCSS("wapp-tokens.css")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/css; charset=utf-8", data)
	})
	router.GET("/static/css/wapp-components.css", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		data, err := ui.GetCSS("wapp-components.css")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/css; charset=utf-8", data)
	})
	router.GET("/static/css/theme-bff.css", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		data, err := ui.GetCSS("theme-bff.css")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/css; charset=utf-8", data)
	})

	// Liveness/readiness probe (REQ-B5). No requiere sesión.
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "time": time.Now().UTC().Format(time.RFC3339)})
	})

	// Techo del cuerpo para la ÚNICA pantalla que acepta archivos (el import de catálogo). Va antes del
	// CSRF a propósito: el CSRF lee el formulario para comparar el token y con eso se traga el cuerpo
	// entero, así que un tope montado después llegaría tarde.
	router.Use(BodyLimitMiddleware(maxCatalogImportBody, catalogImportRoute))

	// Defensa CSRF double-submit (H2): a partir de aquí toda ruta que renderiza formularios o muta estado
	// lleva el token. Se registra DESPUÉS de /static y /healthz (que no renderizan formularios ni mutan) para
	// no ensuciar sus respuestas cacheables con una cookie de token.
	router.Use(CSRFMiddleware(cfg))

	// --- Rutas públicas (sin sesión) ---
	// Login server-to-server contra la API pública. GET pinta el form; POST autentica y custodia el JWT.
	router.GET("/login", h.ShowLogin)
	router.POST("/login", h.DoLogin)
	// Logout: borra la cookie de sesión (best-effort en la API) y vuelve al login. SOLO POST (muta estado):
	// un GET no debe cerrar sesión (evita cierres por prefetch/enlaces cruzados) y va con token CSRF.
	router.POST("/logout", h.DoLogout)

	// --- Rutas protegidas (AuthMiddleware: cookie válida o redirect a /login) ---
	protected := router.Group("/")
	protected.Use(h.AuthMiddleware())
	// Deadline por petición: acota TODA la cadena withAuthRetry hacia la API pública (H4) para que un
	// upstream lento no cuelgue el handler más allá del presupuesto (bajo el WriteTimeout del servidor).
	protected.Use(RequestDeadlineMiddleware(cfg))
	// Dashboard: listado de sesiones del tenant + formulario de envío (T3). POST /send procesa el envío y
	// re-renderiza el dashboard con el resultado.
	protected.GET("/", h.ShowDashboard)
	protected.POST("/send", h.DoSend)
	// Rol de sesión bot|passive (Plan 020 · T1): select por fila de la tabla del dashboard. POST clásico
	// SSR con CSRF; re-renderiza el dashboard con el rol ya cambiado.
	protected.POST("/sessions/:id/role", h.DoSetSessionRole)

	// Editor de menú/encuestas (T4): flujos (inmutables versionados) + triggers (crear/borrar). "Editar"
	// un flujo = publicar versión N+1 (POST /flows); "editar" un trigger = borrar + crear.
	protected.GET("/flows", h.ShowFlows)
	protected.GET("/flows/:id", h.ShowFlowDetail)
	protected.POST("/flows", h.DoPublishFlow)
	protected.GET("/triggers", h.ShowTriggers)
	protected.POST("/triggers", h.DoCreateTrigger)
	protected.POST("/triggers/:id/delete", h.DoDeleteTrigger)

	// Variables de empresa (Plan 041 · T2.1): pares clave→valor que wApp no interpreta. Pantalla
	// PERMANENTE (capa técnica, no migra a KMP) y SIN gate de feature. El POST guarda el conjunto
	// entero, que es la única forma que da la API de quitar una variable.
	protected.GET("/variables", h.ShowTenantVariables)
	protected.POST("/variables", h.DoSaveTenantVariables)

	// Bandeja de solicitudes (Plan 041 · T1.5), gateada por la feature `cart_basic` en la plantilla y
	// por RequireFeature en la plataforma. PANTALLA PROVISIONAL: migra a KMP (planes 045/047, ADR-0035).
	protected.GET("/intakes", h.ShowIntakes)
	protected.GET("/intakes/:id", h.ShowIntakeDetail)
	protected.POST("/intakes/:id/status", h.DoSetIntakeStatus)

	// Import de catálogo (Plan 041 · T3.5), gateado por la feature `catalog_import` en la plantilla y
	// por RequireFeature en la plataforma. El POST atiende los dos pasos —comprobar y aplicar— y cuál
	// se pide lo dice el botón: el que escribe solo existe después de haber enseñado el diff.
	// PANTALLA PROVISIONAL: migra a KMP (planes 045/047, ADR-0035).
	protected.GET(catalogImportRoute, h.ShowCatalogImport)
	protected.POST(catalogImportRoute, h.DoCatalogImport)
	protected.GET(catalogImportRoute+"/template", h.DownloadCatalogTemplate)

	return router, rateLimiter
}

// NewRouterWithLimiter construye el engine y la función de limpieza para el rate-limiter.
func NewRouterWithLimiter(cfg *config.Config) (*gin.Engine, func()) {
	router, limiter := newRouterWithLimiter(cfg)
	cleanup := func() {
		if limiter != nil {
			limiter.close()
		}
	}
	return router, cleanup
}

// parseTrustedProxies convierte el CSV de proxies de confianza (IPs o CIDRs) a una lista, descartando
// vacíos. Devuelve nil cuando no hay ninguno: SetTrustedProxies(nil) hace que Gin no confíe en ningún
// proxy y resuelva ClientIP() desde la IP de la conexión (ignorando X-Forwarded-For).
func parseTrustedProxies(csv string) []string {
	var proxies []string
	for _, raw := range strings.Split(csv, ",") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		proxies = append(proxies, p)
	}
	return proxies
}

// slogMiddleware envía cada petición HTTP a slog (diagnóstico).
func slogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		latency := time.Since(start)
		status := c.Writer.Status()
		if status >= 400 {
			slog.Warn("petición web con error",
				"status", status, "method", c.Request.Method, "path", path,
				"latency", latency, "ip", c.ClientIP())
		} else {
			slog.Info("petición web completada",
				"status", status, "method", c.Request.Method, "path", path, "latency", latency)
		}
	}
}
