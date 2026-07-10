// Package web es el servidor Gin de la consola BFF de wApp (SSR endurecido).
//
// El BFF sirve la UI Material Design 3 mismo-origen, custodia el JWT en una cookie HttpOnly y relaya
// server-to-server contra la API pública REST (:8103 /api/v1). NO habla gRPC con el Gateway/Edge ni
// toca material criptográfico (zero-knowledge). A diferencia de edugo-messaging-web NO hay relay SSE del
// QR (el emparejamiento es local en el Edge), así que el http.Server SÍ fija WriteTimeout.
package web

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	// de página (pages/*.html) que le indica ContentTemplate.
	var err error
	templates = template.New("").Funcs(template.FuncMap{
		// hasPrefix resalta el enlace activo de la navegación (app-bar): la sección se decide por el
		// prefijo del path (p. ej. "/flows/menu" activa "Flujos").
		"hasPrefix": strings.HasPrefix,
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

	// Editor de menú/encuestas (T4): flujos (inmutables versionados) + triggers (crear/borrar). "Editar"
	// un flujo = publicar versión N+1 (POST /flows); "editar" un trigger = borrar + crear.
	protected.GET("/flows", h.ShowFlows)
	protected.GET("/flows/:id", h.ShowFlowDetail)
	protected.POST("/flows", h.DoPublishFlow)
	protected.GET("/triggers", h.ShowTriggers)
	protected.POST("/triggers", h.DoCreateTrigger)
	protected.POST("/triggers/:id/delete", h.DoDeleteTrigger)

	return router, rateLimiter
}

// Run arranca el servidor web sobre un http.Server endurecido (anti-slowloris) y bloquea hasta recibir
// SIGINT/SIGTERM, momento en que apaga de forma graceful. Solo traduce señales del SO a la cancelación
// del contexto; el ciclo de vida serve/shutdown vive en serveWithContext (testeable sin señales).
func Run(cfg *config.Config) error {
	// El contexto se cancela con la primera SIGINT/SIGTERM; stop restaura el manejo por defecto para que
	// una segunda señal (p. ej. si el drenado se atasca) mate el proceso de inmediato.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveWithContext(ctx, cfg, stop)
}

// serveWithContext levanta el servidor y bloquea hasta que ctx se cancela (señal del SO) o el propio
// servidor termina (típicamente, fallo al bindear el puerto). Al cancelarse ctx apaga de forma graceful:
// deja de aceptar conexiones y espera hasta ShutdownTimeout a que terminen las peticiones en vuelo antes
// de forzar el cierre (cada deploy dejaba de cortar peticiones a mitad). onSignal, si no es nil, se
// invoca al recibir la cancelación para restaurar el manejo por defecto de la señal.
func serveWithContext(ctx context.Context, cfg *config.Config, onSignal func()) error {
	router, rateLimiter := newRouterWithLimiter(cfg)
	if rateLimiter != nil {
		defer rateLimiter.close()
	}

	srv := newHTTPServer(cfg, router)

	// ListenAndServe corre en su propia goroutine; su error (fallo al bindear el puerto) viaja por el
	// canal para no perderlo mientras el hilo principal espera la señal.
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("consola BFF escuchando",
			"addr", cfg.HTTPAddr, "public_api", cfg.PublicAPIBaseURL, "ambiente", cfg.Environment)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// El servidor terminó por sí solo (típicamente, no pudo bindear el puerto).
		return err
	case <-ctx.Done():
		if onSignal != nil {
			onSignal() // segunda señal a partir de aquí = terminación inmediata por defecto.
		}
		slog.Info("señal de apagado recibida, drenando peticiones en vuelo",
			"timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("apagado graceful excedió el plazo; forzando cierre", "error", err)
		return err
	}
	slog.Info("consola BFF apagada limpiamente")
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
