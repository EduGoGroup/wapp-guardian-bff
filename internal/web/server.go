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

	"github.com/EduGoGroup/wapp-shared/ui"
	sharedweb "github.com/EduGoGroup/wapp-shared/web"
	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
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

// newRouterWithLimiter es como NewRouter pero además devuelve el rate-limiter para que su dueño
// pueda soltar el mapa al apagar (lo usa Run para la vida del proceso).
func newRouterWithLimiter(cfg *config.Config) (*gin.Engine, *sharedweb.KeyedRateLimiter) {
	webgin.SetReleaseMode()

	router := gin.New()
	// Proxies de confianza: por defecto (lista vacía) NO se confía en ninguno, de modo que ClientIP()
	// ignora X-Forwarded-For y usa la IP de la conexión. Esto blinda el rate-limit por IP de /login
	// (única defensa anti fuerza-bruta) contra la suplantación del header. Solo se confía en la lista
	// explícita cuando el BFF queda detrás de un proxy de confianza (WAPP_GUARDIAN_TRUSTED_PROXIES).
	if err := webgin.SetTrustedProxies(router, cfg.TrustedProxies); err != nil {
		// Config inválida en el arranque: fail-closed (como el panic al compilar plantillas). Mejor no
		// arrancar que hacerlo con una allowlist de proxies malformada y un ClientIP() no fiable.
		slog.Error("lista de proxies de confianza inválida", "valor", cfg.TrustedProxies, "error", err)
		panic(err)
	}
	router.Use(gin.Recovery())
	router.Use(webgin.SlogLogger())
	// Cabeceras de seguridad + nonce CSP por petición (antes de los handlers que renderizan).
	router.Use(webgin.SecurityHeaders(securityOptions(cfg)))
	// CORS fail-closed (allowlist, nunca "*"); same-origin por defecto.
	router.Use(webgin.CORS(corsOptions(cfg)))

	// El limitador se construye AQUÍ y se conserva: antes se descartaba al montar el router y su
	// goroutine de barrido quedaba viva para siempre, una por router. El del módulo no arranca
	// ninguna —purga en perezoso dentro de Allow—, así que no hay nada que filtrar.
	var rateLimiter *sharedweb.KeyedRateLimiter // nil cuando el rate-limit está apagado.
	if cfg.RateLimitEnabled {
		rateLimiter = sharedweb.NewKeyedRateLimiter(sharedweb.RateLimiterOptions{
			RPS:   cfg.RateLimitRPS,
			Burst: int(cfg.RateLimitBurst),
		})
		// Rate-limit global (antes de auth): clava por user_id si hay sesión, si no por IP.
		router.Use(webgin.RateLimit(rateLimiter))
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
	router.Use(webgin.BodyLimit(maxCatalogImportBody, catalogImportRoute))

	// Defensa CSRF double-submit (H2): a partir de aquí toda ruta que renderiza formularios o muta estado
	// lleva el token. Se registra DESPUÉS de /static y /healthz (que no renderizan formularios ni mutan) para
	// no ensuciar sus respuestas cacheables con una cookie de token.
	router.Use(webgin.CSRF(csrfOptions(cfg)))

	// --- Rutas públicas (sin sesión) ---
	// Login server-to-server contra la API pública. GET pinta el form; POST autentica y custodia el JWT.
	router.GET("/login", h.ShowLogin)
	router.POST("/login", h.DoLogin)
	// Signup público (Plan 056 · T3.5): solicitud de alta de cuenta en wApp.
	router.GET("/signup", h.ShowSignup)
	router.POST("/signup", h.DoSignup)
	// Logout: borra la cookie de sesión (best-effort en la API) y vuelve al login. SOLO POST (muta estado):
	// un GET no debe cerrar sesión (evita cierres por prefetch/enlaces cruzados) y va con token CSRF.
	router.POST("/logout", h.DoLogout)

	// --- Rutas protegidas (AuthMiddleware: cookie válida o redirect a /login) ---
	protected := router.Group("/")
	protected.Use(h.AuthMiddleware())
	// Pantalla de estado "en espera" (sin tenant asignado, Plan 056 · T3.5)
	protected.GET("/pending", h.ShowPending)
	// Deadline por petición: acota TODA la cadena withAuthRetry hacia la API pública (H4) para que un
	// upstream lento no cuelgue el handler más allá del presupuesto (bajo el WriteTimeout del servidor).
	protected.Use(webgin.RequestDeadline(cfg.UpstreamTimeout))
	// Dashboard: listado de sesiones del tenant + formulario de envío (T3). POST /send procesa el envío y
	// re-renderiza el dashboard con el resultado.
	protected.GET("/", h.ShowDashboard)
	protected.POST("/send", h.DoSend)
	// PERFIL de sesión active|passive (ADR-0027, Plan 046 · T1.3; sustituye al rol bot|passive del Plan
	// 020): select por fila de la tabla del dashboard. POST clásico SSR con CSRF; re-renderiza el
	// dashboard con el perfil ya cambiado.
	//
	// 🔴 La ruta vieja `/sessions/:id/role` NO se conserva aquí: es una pantalla SSR y el único cliente
	// del formulario es esta misma consola, que se despliega con él.
	//
	// 📌 Este comentario decía además que la deprecación con dos rutas vivas «es de la API pública,
	// donde SÍ hay clientes que no se despliegan a la vez». Ese razonamiento era correcto y su premisa
	// FALSA: al comprobarla contra los seis repos no apareció ni un consumidor de `/role`. La ruta
	// pública se retiró con la 0064 por la misma razón que aquí.
	protected.POST("/sessions/:id/profile", h.DoSetSessionProfile)

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

	// Bandeja de solicitudes (Plan 041 · T1.5 y T4.10), gateada por la feature `cart_basic` en la
	// plantilla y por RequireFeature en la plataforma. PANTALLA PROVISIONAL: migra a KMP (planes
	// 045/047, ADR-0035).
	//
	// El POST de líneas es POST y no PUT aunque la ruta de la API lo sea: un formulario HTML solo
	// sabe emitir GET y POST, y fingir el verbo con un campo oculto añadiría una convención que
	// esta consola no tiene en ninguna otra pantalla. La traducción a PUT la hace el apiclient,
	// que es quien habla el contrato.
	//
	// El descarte por lotes (T4.8) cuelga de una ruta LITERAL bajo /intakes, como en la API pública:
	// la operación es sobre VARIAS solicitudes, así que ninguna de ellas es el recurso de la URL.
	// Atiende los dos pasos —revisar y descartar— y cuál se pide lo dice el botón, igual que el
	// import de catálogo: el que escribe solo existe después de haber enseñado qué se va a matar.
	protected.GET("/intakes", h.ShowIntakes)
	protected.GET("/intakes/:id", h.ShowIntakeDetail)
	protected.POST("/intakes/discard", h.DoDiscardIntakes)
	protected.POST("/intakes/:id/status", h.DoSetIntakeStatus)
	protected.POST("/intakes/:id/items", h.DoEditIntakeItems)

	// LAS TRES ACCIONES DEL DUEÑO (Plan 044 · T4.2, T4.3, T4.4). Van por rutas propias y no por un
	// campo más del cambio de estado porque no son lo mismo y la pantalla tiene que poder decirlo:
	// éstas LE HABLAN AL CLIENTE por WhatsApp y dejan revisión; el desplegable de `/status` solo
	// mueve la etiqueta del ciclo de vida.
	//
	// `/correct` es ruta DEL BFF, no de la API: allá corregir es el mismo `PUT …/items` con el campo
	// `as_correction` (D-044.48 §1), y quien lo traduce es el apiclient. Aquí existe porque son dos
	// formularios distintos —el del 041 edita `items`, éste edita el borrador— y cada formulario
	// necesita su acción para que un rechazo repinte en el sitio donde se tecleó.
	protected.POST("/intakes/:id/correct", h.DoCorrectIntakeItems)
	protected.POST("/intakes/:id/approve", h.DoApproveIntake)
	protected.POST("/intakes/:id/request-info", h.DoRequestIntakeInfo)

	// REGENERAR LA INTERPRETACIÓN (Plan 044 · T4.7). Va por ruta propia y NO como un botón más de las
	// tres de arriba porque no es de la misma familia: las otras le hablan al cliente por WhatsApp y
	// esta no le habla a nadie —vuelve a interpretar el texto que el cliente ya mandó—. Y sobre todo,
	// es la única que no devuelve nada que pintar: abre un trabajo y la revisión llega después.
	protected.POST("/intakes/:id/reanalyze", h.DoReanalyzeIntake)

	// SUGERIR LA RESPUESTA CON LA VOZ DE LA DUEÑA (Plan 047 · T2.4, sobre el endpoint del Plan 044 ·
	// T5.1). Va por ruta propia por lo mismo que `/reanalyze`: no es de la familia de las tres de
	// arriba —no le habla a nadie, no escribe en la solicitud y no la mueve de estado—, solo redacta
	// una propuesta y la deja en el campo de aprobar para que la dueña la lea y decida.
	//
	// Es POST aunque no escriba nada, y no es por el formulario: consume una inferencia. No es
	// cacheable, no es gratis, y un GET lo dispararía un prefetch del navegador.
	protected.POST("/intakes/:id/quote-suggestion", h.DoSuggestIntakeQuote)

	// Import de catálogo (Plan 041 · T3.5), gateado por la feature `catalog_import` en la plantilla y
	// por RequireFeature en la plataforma. El POST atiende los dos pasos —comprobar y aplicar— y cuál
	// se pide lo dice el botón: el que escribe solo existe después de haber enseñado el diff.
	// PANTALLA PROVISIONAL: migra a KMP (planes 045/047, ADR-0035).
	protected.GET(catalogImportRoute, h.ShowCatalogImport)
	protected.POST(catalogImportRoute, h.DoCatalogImport)
	protected.GET(catalogImportRoute+"/template", h.DownloadCatalogTemplate)

	// Integraciones (Plan 042 · T5.2): la configuración del puente CRM del tenant —por dónde salen los
	// pedidos, a qué endpoint y con qué secreto de firma—, gateada por la feature `crm_bridge` en la
	// plantilla y por RequireFeature en la plataforma, que la exige en los TRES verbos (también el GET).
	// PANTALLA PERMANENTE: es capa técnica (ADR-0035, doc 14 D-03/D-14) y no migra a KMP.
	//
	// El borrado cuelga de una ruta propia y va por POST porque un formulario HTML solo sabe GET y
	// POST; fingir el DELETE con un campo oculto añadiría una convención que esta consola no tiene en
	// ninguna otra pantalla. La traducción al verbo real la hace el apiclient, que es quien habla el
	// contrato. Y va aparte del guardado a propósito: borra la fila entera con su secreto, que es la
	// única forma de retirarlo.
	protected.GET(integrationsRoute, h.ShowIntegrations)
	protected.POST(integrationsRoute, h.DoSaveIntegration)
	protected.POST(integrationsRoute+"/delete", h.DoDeleteIntegration)

	return router, rateLimiter
}

// NewRouterWithLimiter construye el engine y la función de limpieza del rate-limiter.
//
// Cerrar el limitador libera su mapa de golpe; NO lo inhabilita —Allow sigue atendiendo y purgando
// después—, así que llamarlo no deja al router sirviendo sin defensa.
func NewRouterWithLimiter(cfg *config.Config) (*gin.Engine, func()) {
	router, limiter := newRouterWithLimiter(cfg)
	cleanup := func() {
		if limiter != nil {
			limiter.Close()
		}
	}
	return router, cleanup
}
