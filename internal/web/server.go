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
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// templates es el puntero global de plantillas para el helper dinámico `yield` (el layout maestro
// ejecuta el fragmento de página por nombre en tiempo de render).
var templates *template.Template

//go:embed templates
var templatesFS embed.FS

// appCSS es el design system Material Design 3 propio de wApp (teal/verde). Se sirve mismo-origen en
// /static/css/app.css con Content-Type text/css, sin CDNs (encaja con la CSP endurecida).
//
//go:embed static/css/app.css
var appCSS []byte

// NewRouter construye el *gin.Engine completo (plantillas + rutas + middlewares). Se expone para que los
// tests lo monten con httptest sin levantar un puerto real. Run() lo usa y además escucha.
func NewRouter(cfg *config.Config) *gin.Engine {
	router, _ := newRouterWithLimiter(cfg)
	return router
}

// newRouterWithLimiter es como NewRouter pero además devuelve el rate-limiter para poder cerrar su
// goroutine de barrido (lo usa Run para la vida del proceso y los tests para no filtrar goroutines).
func newRouterWithLimiter(cfg *config.Config) (*gin.Engine, *keyedRateLimiter) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
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
	// de página (pages/*.html) que le indica ContentTemplate.
	var err error
	templates = template.New("").Funcs(template.FuncMap{
		"yield": func(name string, data interface{}) (template.HTML, error) {
			if name == "" {
				return "", nil
			}
			var buf bytes.Buffer
			if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
				slog.Error("error al renderizar plantilla yield", "nombre", name, "error", err)
				return "", err
			}
			return template.HTML(buf.String()), nil // #nosec G203 -- fragmento de plantilla propia.
		},
	})
	templates, err = templates.ParseFS(templatesFS,
		"templates/layouts/*.html",
		"templates/pages/*.html",
	)
	if err != nil {
		slog.Error("no se pudieron compilar las plantillas HTML", "error", err)
		panic(err)
	}
	router.SetHTMLTemplate(templates)

	h := NewHandler(cfg)

	// CSS propio (Material Design 3) servido mismo-origen, sin CDNs. Cache moderada (1h): el contenido
	// cambia solo con un deploy, así que un revalidate frecuente basta.
	router.GET("/static/css/app.css", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/css; charset=utf-8", appCSS)
	})

	// Liveness/readiness probe (REQ-B5). No requiere sesión.
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "time": time.Now().UTC().Format(time.RFC3339)})
	})

	// --- Rutas públicas (sin sesión) ---
	// Login server-to-server contra la API pública. GET pinta el form; POST autentica y custodia el JWT.
	router.GET("/login", h.ShowLogin)
	router.POST("/login", h.DoLogin)
	// Logout: borra la cookie de sesión (best-effort en la API) y vuelve al login. GET y POST, idempotente.
	router.GET("/logout", h.DoLogout)
	router.POST("/logout", h.DoLogout)

	// --- Rutas protegidas (AuthMiddleware: cookie válida o redirect a /login) ---
	protected := router.Group("/")
	protected.Use(h.AuthMiddleware())
	// Dashboard: listado de sesiones del tenant + formulario de envío (T3). POST /send procesa el envío y
	// re-renderiza el dashboard con el resultado. T4 añadirá los editores de flows/triggers.
	protected.GET("/", h.ShowDashboard)
	protected.POST("/send", h.DoSend)

	return router, rateLimiter
}

// Run arranca el servidor web sobre un http.Server endurecido (anti-slowloris) y bloquea hasta que
// termine.
func Run(cfg *config.Config) error {
	router, rateLimiter := newRouterWithLimiter(cfg)
	if rateLimiter != nil {
		defer rateLimiter.close()
	}

	srv := newHTTPServer(cfg, router)
	slog.Info("consola BFF escuchando",
		"addr", cfg.HTTPAddr, "public_api", cfg.PublicAPIBaseURL, "ambiente", cfg.Environment)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// newHTTPServer construye el http.Server endurecido. A diferencia de edugo-messaging-web SÍ fija
// WriteTimeout: el BFF no tiene endpoints long-lived (sin SSE del QR), así que un deadline de escritura
// es una capa más contra clientes lentos.
func newHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
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
