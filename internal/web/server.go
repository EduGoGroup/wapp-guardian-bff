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
// funcsDePlantilla es el ÚNICO sitio donde se declaran los helpers de plantilla. Existe porque no lo
// era: `intakes_quote_test.go` compilaba las plantillas con su propia copia del FuncMap, y al añadir
// `cuenta` esa copia se quedó atrás y reventó el gate con «function "cuenta" not defined». El arreglo
// no fue añadir la clave en los dos sitios —eso deja la copia viva y volvería a divergir—, sino que
// no haya dos sitios.
//
// `yield` viaja como parámetro y no como clave fija porque es lo ÚNICO que legítimamente difiere: el
// router necesita cerrar sobre la plantilla ya compilada y los tests lo stubean.
func funcsDePlantilla(yield func(string, any) (template.HTML, error)) template.FuncMap {
	return template.FuncMap{
		// hasPrefix resalta el enlace activo de la navegación (app-bar): la sección se decide por el
		// prefijo del path (p. ej. "/catalog-import/template" activa "Catálogo"). El ejemplo era
		// "/flows/menu" hasta el Plan 047 · T6.6 y "/intakes/in-1" hasta el T7.7: un comentario que
		// ilustra con una ruta retirada envejece a mentira, y ya lo hizo dos veces.
		"hasPrefix": strings.HasPrefix,
		// `statusLabel` y `cuenta` estuvieron aquí hasta el Plan 047 · T7.7. Las dos las usaba SOLO la
		// bandeja de solicitudes: `statusLabel` no tiene ya ni función ni consumidor, y `cuenta` sigue
		// viva en Go (integrations_handler.go la usa para los plazos) pero ninguna plantilla la llama.
		// Una clave de FuncMap sin consumidor no falla: se queda esperando, y el día que alguien
		// escriba `{{ cuenta … }}` con la firma cambiada el error sale en tiempo de ejecución.
		"yield": yield,
	}
}

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
	root := template.New("").Funcs(funcsDePlantilla(func(name string, data any) (template.HTML, error) {
		if name == "" {
			return "", nil
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			slog.Error("error al renderizar plantilla yield", "nombre", name, "error", err)
			return "", err
		}
		return template.HTML(buf.String()), nil // #nosec G203 -- fragmento de plantilla propia.
	}))
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
	//
	// 🔴 VUELVE A SER UNO PARA EL GRUPO ENTERO (Plan 047 · T7.7). Entre el Plan 047 · T2.4 y el T7.7
	// aquí vivía `requestDeadlineByRoute`, un despachador con UNA excepción cableada: la ruta de la
	// sugerencia de cotización se llevaba 58s en vez de 20s porque esperaba a que un modelo redactara.
	// Esa ruta se mudó a la consola del cliente, y con ella el único caso que justificaba el
	// despachador. Dejarlo con una rama que ya nunca se toma no era «código muerto» sin más: era código
	// muerto EN EL CAMINO CRÍTICO de todas las peticiones del BFF, comparando en cada una un FullPath()
	// contra una ruta que ya no existe. Que ninguna constante de este paquete nombre una ruta fantasma
	// lo vigila ahora `TestNingunaConstanteDeRutaNombraUnaRutaFantasma` (rutas_declaradas_test.go).
	protected.Use(webgin.RequestDeadline(cfg.UpstreamTimeout))
	// PORTADA: el índice de lo que ESTA consola conserva (plan y capacidades del tenant + accesos a las
	// pantallas vivas).
	// PANTALLA PERMANENTE: es el destino de las redirecciones del plano de autenticación, así que es
	// capa técnica y no migra: se queda en el BFF (ADR-0035 §3, que el ADR-0047 deja intacto).
	//
	// 📌 Este marcador nació SIN ponerse, para no desajustar el recuento con el que se verificó la
	// tarea (se esperaban 2 permanentes, y con la portada son 3). Fue el criterio equivocado y se
	// corrigió en el acto: el censo cuenta lo que hay, no lo que se esperaba. Un grep que cuadra porque
	// alguien se calló una pantalla mide su propia expectativa, no el código.
	//
	// 🔴 AQUÍ ESTUVO EL DASHBOARD DE SESIONES, y su retirada (Plan 047 · T2.1) se llevó DOS rutas que
	// ya no existen: `POST /send` (enviar un mensaje por una sesión) y `POST /sessions/:id/profile`
	// (cambiar el perfil active|passive, ADR-0027). Las tres pantallas se administran ahora en la
	// consola del cliente (`wapp-client-console`), y se retiraron de aquí en el mismo ciclo (REQ-08):
	// dos copias de la misma pantalla divergen, y la que sigue viva contesta antes que la documentación.
	//
	// 🔴 LA RUTA `GET /` NO SE FUE CON ELLAS. No es un resto por limpiar: es el destino de TRES
	// redirecciones —DoLogin tras autenticar, ShowLogin con sesión ya válida y el AuthMiddleware al
	// confirmarse el tenant viniendo de /pending—. Borrarla convertiría un login correcto en un 404.
	protected.GET("/", h.ShowHome)

	// 🔴 AQUÍ ESTUVO EL EDITOR DE FLUJOS Y DISPARADORES, y su retirada (Plan 047 · T6.6) se llevó SEIS
	// rutas: `GET /flows`, `GET /flows/:id`, `POST /flows` (publicar la versión N+1) y las tres de
	// disparadores, `GET /triggers`, `POST /triggers`, `POST /triggers/:id/delete`. Las dos pantallas
	// se administran ahora en la consola del cliente (`wapp-client-console`, `/flujos` y
	// `/disparadores`) y se retiraron de aquí EN EL MISMO CICLO (REQ-08): dos copias de la misma
	// pantalla divergen, y la que sigue viva contesta antes que la documentación.
	//
	// La retirada la vigila `TestRutasDelEditorYaNoExisten` (home_test.go) contra `router.Routes()`,
	// no contra el status: este router nace con HandleMethodNotAllowed en false y responde 404 a un
	// verbo no registrado igual que a una ruta inexistente, así que un test de status daría por
	// retirada una ruta que sigue viva con otro método.

	// Variables de empresa (Plan 041 · T2.1): pares clave→valor que wApp no interpreta. Va SIN gate de
	// feature. El POST guarda el conjunto entero, que es la única forma que da la API de quitar una
	// variable.
	// PANTALLA PERMANENTE: es capa técnica y no migra (ADR-0035 §3): se queda en el BFF.
	protected.GET("/variables", h.ShowTenantVariables)
	protected.POST("/variables", h.DoSaveTenantVariables)

	// 🔴 AQUÍ ESTUVO LA BANDEJA DE SOLICITUDES, y su retirada (Plan 047 · T7.7) se llevó DIEZ rutas:
	// `GET /intakes`, `GET /intakes/:id`, `POST /intakes/discard`, `POST /intakes/:id/status`,
	// `POST /intakes/:id/items`, `POST /intakes/:id/correct`, `POST /intakes/:id/approve`,
	// `POST /intakes/:id/request-info`, `POST /intakes/:id/reanalyze` y
	// `POST /intakes/:id/quote-suggestion`. La bandeja se administra ahora en la consola del cliente
	// (`wapp-client-console`, `/solicitudes`) y se retiró de aquí EN EL MISMO CICLO (REQ-08): dos
	// copias de la misma pantalla divergen, y la que sigue viva contesta antes que la documentación.
	//
	// Con ellas se fueron DOS cosas que no eran rutas y que sostenían solo a ésta: el despachador de
	// plazos por ruta (arriba, `protected.Use`) y el cliente HTTP de inferencia del apiclient
	// (`InferenceHTTPClient`, 55s), que existía para la ÚNICA llamada del BFF que esperaba a que un
	// modelo redactara.
	//
	// La retirada la vigila `TestRutasDeLaBandejaYaNoExisten` (home_test.go) contra `router.Routes()`,
	// no contra el status: este router nace con HandleMethodNotAllowed en false y responde 404 a un
	// verbo no registrado igual que a una ruta inexistente, así que un test de status daría por
	// retirada una ruta que sigue viva con otro método.

	// Import de catálogo (Plan 041 · T3.5), gateado por la feature `catalog_import` en la plantilla y
	// por RequireFeature en la plataforma. El POST atiende los dos pasos —comprobar y aplicar— y cuál
	// se pide lo dice el botón: el que escribe solo existe después de haber enseñado el diff.
	// PANTALLA PROVISIONAL: migra a `wapp-client-console` (Plan 047, ADR-0047). El destino cambió —ya
	// no es la app KMP— porque el Plan 045 está al 0 % y esa app está declarada diferida: un marcador
	// que apuntaba ahí no era ejecutable.
	protected.GET(catalogImportRoute, h.ShowCatalogImport)
	protected.POST(catalogImportRoute, h.DoCatalogImport)
	protected.GET(catalogImportRoute+"/template", h.DownloadCatalogTemplate)

	// Integraciones (Plan 042 · T5.2): la configuración del puente CRM del tenant —por dónde salen los
	// pedidos, a qué endpoint y con qué secreto de firma—, gateada por la feature `crm_bridge` en la
	// plantilla y por RequireFeature en la plataforma, que la exige en los TRES verbos (también el GET).
	// PANTALLA PERMANENTE: es capa técnica y no migra (ADR-0035 §3, doc 14 D-03/D-14): se queda en el BFF.
	//
	// El borrado cuelga de una ruta propia y va por POST porque un formulario HTML solo sabe GET y
	// POST; fingir el DELETE con un campo oculto añadiría una convención que esta consola no tiene en
	// ninguna otra pantalla. La traducción al verbo real la hace el apiclient, que es quien habla el
	// contrato. Y va aparte del guardado a propósito: borra la fila entera con su secreto, que es la
	// única forma de retirarlo.
	protected.GET(integrationsRoute, h.ShowIntegrations)
	protected.POST(integrationsRoute, h.DoSaveIntegration)
	protected.POST(integrationsRoute+"/delete", h.DoDeleteIntegration)

	// Proveedor de IA (Plan 047 · T3.4, sobre la API que el Plan 044 dejó construida): quién interpreta
	// los mensajes del tenant —el equipo de su local o un proveedor externo—, con qué modelo y con qué
	// credencial. Gateada por la feature `api_llm` en la plantilla y por RequireFeature en la
	// plataforma, que la exige en los TRES verbos (también el GET).
	// PANTALLA PERMANENTE: es capa técnica y no migra (ADR-0035 §3, D-047.5/D-047.9): se queda en el BFF.
	//
	// El borrado cuelga de una ruta propia y va por POST por lo mismo que el de integraciones: un
	// formulario HTML solo sabe GET y POST, y la traducción al verbo real la hace el apiclient. Va
	// aparte del guardado a propósito: borra la credencial Y el consentimiento, que es la única forma
	// de retirar ninguno de los dos.
	protected.GET(tenantLLMRoute, h.ShowTenantLLM)
	protected.POST(tenantLLMRoute, h.DoSaveTenantLLM)
	protected.POST(tenantLLMRoute+"/delete", h.DoDeleteTenantLLM)

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
