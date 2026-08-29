package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// integrationsFeature es la capacidad que abre el puente CRM. Es la MISMA clave con la que la
// plataforma gatea las tres rutas (`RequireFeature("crm_bridge")`, también el GET): aquí decide lo
// que se PINTA, allí lo que se PUEDE, y esconder un formulario nunca sustituye a ese corte.
const integrationsFeature = "crm_bridge"

// integrationsRoute es la ruta de la pantalla en el BFF (no la de la API pública).
const integrationsRoute = "/integrations"

// integrationsNavDataKey es la clave con la que una página declara que el enlace a integraciones debe
// salir en la barra superior. Misma mecánica que las otras dos: si la página no resolvió las
// features, la clave no existe, se lee como falsa y el enlace no llega al HTML.
const integrationsNavDataKey = "IntegrationsNav"

// Adaptadores que esta pantalla ofrece para los EVENTOS. Es el vocabulario cerrado de la plataforma
// menos «http», que solo existe para el catálogo y está diferido: ofrecerlo aquí sería enseñar una
// opción cuyo único desenlace posible es un rechazo.
const (
	integrationAdapterLocal   = "local"
	integrationAdapterWebhook = "webhook"
)

// integrationNotice es el aviso de una operación sobre la integración.
type integrationNotice struct {
	Success bool
	Message string
}

// integrationView es lo que pinta la plantilla.
//
// NO TIENE CAMPO PARA EL SECRETO, y es la misma razón por la que no lo tiene `apiclient.Integration`:
// el valor no debe existir en esta capa. La pantalla enseña si HAY uno (SecretSet) y su huella corta
// (SecretFingerprint), que es lo que permite compararlo con el que el puente tiene configurado sin
// que el secreto llegue nunca al HTML. Ni siquiera se conserva lo que el operador acaba de teclear
// para re-pintarlo tras un rechazo: el campo vuelve vacío, que además es la forma que tiene el
// contrato de decir «deja el que está».
type integrationView struct {
	// Configured distingue «este tenant tiene una integración puesta» de «está en el default
	// local/local porque nunca configuró nada». Es lo que decide si se ofrece quitarla.
	Configured bool
	// EventsAdapter es por dónde salen los eventos del tenant: local (los guarda wApp) o webhook.
	EventsAdapter string
	// CatalogAdapter NO se edita en esta pantalla; se muestra para que el operador sepa de dónde sale
	// su catálogo. El guardado lo preserva (ver DoSaveIntegration).
	CatalogAdapter string
	EndpointURL    string
	Enabled        bool
	// SecretSet dice si hay secreto de firma guardado; SecretFingerprint es su huella corta.
	SecretSet         bool
	SecretFingerprint string
	UpdatedAt         string
	// Loaded distingue «este tenant no tiene integración» de «no se pudo leer».
	Loaded bool
}

// IsWebhook responde si los eventos salen por el puente. La plantilla lo usa para marcar la opción
// elegida sin meter lógica en el HTML.
func (v integrationView) IsWebhook() bool { return v.EventsAdapter == integrationAdapterWebhook }

// outboxDataKey es la clave con la que el estado de la cola entra en los datos de plantilla.
const outboxDataKey = "Outbox"

// outboxView es el estado de la cola de entregas tal como lo pinta la plantilla.
//
// Los nombres de los campos siguen siendo los de la plataforma; la traducción a lenguaje de negocio
// («en cola», «no entregadas») está en el HTML, que es donde está el lector. Repartirla entre las dos
// capas obligaría a buscar en dos sitios qué significa cada número.
//
// AQUÍ NO HAY CONTENIDO DE ENTREGAS: son cuatro números y una antigüedad, que es literalmente todo lo
// que la API publica. Lo que no se puede pedir tampoco se puede pintar por descuido.
type outboxView struct {
	// Loaded distingue «la cola está vacía» de «no se pudo consultar», que en contadores es la
	// distinción que más importa: un cero se lee como «todo al día».
	Loaded bool
	// Notice es el aviso legible cuando no se pudo consultar (vacío si se pudo).
	Notice     string
	Pending    int64
	Delivering int64
	Delivered  int64
	Dead       int64
	// OldestPendingAge es cuánto lleva esperando la entrega en cola más antigua, ya en palabras
	// («6 horas»). Vacío si no hay nada en cola.
	OldestPendingAge string
}

// Waiting son las que todavía no han llegado a su destino: las que esperan turno más las que están
// saliendo en este instante. Es un método y no un campo porque la plantilla no sabe sumar.
func (v outboxView) Waiting() int64 { return v.Pending + v.Delivering }

// AllDelivered responde si no queda nada por entregar. Es la condición del mensaje tranquilizador, y
// exige Loaded a propósito: sin datos no se afirma que todo va bien.
func (v outboxView) AllDelivered() bool { return v.Loaded && v.Waiting() == 0 }

// HasDead responde si hay entregas que se dieron por perdidas. Es lo único de este panel que pide una
// acción del operador, y por eso se pregunta aparte.
func (v outboxView) HasDead() bool { return v.Dead > 0 }

// IntegrationsHandler sirve la pantalla del puente CRM: por dónde salen los eventos del tenant, a qué
// endpoint se entregan y con qué secreto se firman.
//
// Es una pantalla PERMANENTE (ADR-0035 §3, doc 14 D-03/D-14): configurar la integración es capa
// técnica del producto, no una operación de negocio, así que no migra —se queda en el BFF— y no lleva
// marca de provisionalidad.
type IntegrationsHandler struct {
	cfg  *config.Config
	api  IntegrationsAPI
	auth *AuthHandler
}

// NewIntegrationsHandler construye el handler sobre el puerto de integraciones.
func NewIntegrationsHandler(cfg *config.Config, api IntegrationsAPI, auth *AuthHandler) *IntegrationsHandler {
	return &IntegrationsHandler{cfg: cfg, api: api, auth: auth}
}

// ShowIntegrations pinta la configuración del puente del tenant.
func (h *IntegrationsHandler) ShowIntegrations(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	// Sin la capacidad no se llama a la API: la plataforma cortaría con 403 en el GET igualmente
	// (también el GET va gateado), y gastar el viaje solo serviría para llenar el log de rechazos
	// previsibles. El gate real de lo que se emite está en la plantilla.
	if !ent.Has(integrationsFeature) {
		h.render(c, http.StatusOK, ent, integrationView{}, nil)
		return
	}

	current, err := h.load(c)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapIntegrationReadError(err)
		h.render(c, status, ent, integrationView{}, notice)
		return
	}
	h.render(c, http.StatusOK, ent, viewFromIntegration(current), nil)
}

// DoSaveIntegration guarda la configuración COMPLETA que trae el formulario.
//
// Antes del PUT se RELEE la configuración actual, y no es un viaje de cortesía: el PUT es un upsert
// completo, así que lo que no viaje toma el default de la tabla. Esta pantalla no edita el adaptador
// de CATÁLOGO —el formulario es de eventos—, de modo que sin releerlo un tenant con el catálogo en
// «webhook» lo vería caer a «local» por haber tocado el endpoint de eventos. Preservarlo desde un
// campo oculto habría sido más barato, pero un oculto es del cliente: bastaría manipularlo para
// cambiar en silencio de dónde sale el catálogo. Si la relectura falla NO se guarda nada: pisar a
// ciegas es peor que no guardar, y el operador puede reintentar.
func (h *IntegrationsHandler) DoSaveIntegration(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(integrationsFeature) {
		// Un POST sin la capacidad es un envío forzado (el formulario no se emite siquiera): se dice
		// que no y no se llama a la API, que respondería 403 de todas formas.
		h.render(c, http.StatusForbidden, ent, integrationView{}, &integrationNotice{
			Message: "El plan de este tenant no incluye el puente con un CRM.",
		})
		return
	}

	current, err := h.load(c)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapIntegrationReadError(err)
		notice.Message += " No se ha guardado nada."
		h.render(c, status, ent, integrationView{}, notice)
		return
	}

	settings, typed, msg := settingsFromForm(c, current)
	if msg != "" {
		// Se re-pinta lo que el operador tecleó (menos el secreto), no lo que hay en el servidor: su
		// trabajo no se tira por un rechazo y puede corregir sobre lo que escribió.
		h.render(c, http.StatusBadRequest, ent, typed, &integrationNotice{Message: msg})
		return
	}

	var saved *apiclient.Integration
	err = h.auth.withAuthRetry(c, func(accessToken string) error {
		var serr error
		saved, serr = h.api.SaveIntegration(c.Request.Context(), accessToken, settings)
		return serr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapIntegrationSaveError(err)
		h.render(c, status, ent, typed, notice)
		return
	}

	h.render(c, http.StatusOK, ent, viewFromIntegration(saved), &integrationNotice{
		Success: true,
		Message: savedIntegrationMessage(saved),
	})
}

// DoDeleteIntegration quita la integración del tenant, que vuelve al default local/local: los eventos
// se quedan en wApp y el secreto de firma se borra con la fila.
//
// Es la ÚNICA forma de retirar el secreto —el guardado nunca lo borra, porque el campo vacío
// significa «deja el que está»—, y por eso la pantalla la ofrece aparte y lo dice con esas palabras.
func (h *IntegrationsHandler) DoDeleteIntegration(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(integrationsFeature) {
		h.render(c, http.StatusForbidden, ent, integrationView{}, &integrationNotice{
			Message: "El plan de este tenant no incluye el puente con un CRM.",
		})
		return
	}

	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		return h.api.DeleteIntegration(c.Request.Context(), accessToken)
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapIntegrationDeleteError(err)
		// Se re-pinta con lo que haya en el servidor: si el borrado no se hizo, la pantalla debe
		// seguir enseñando la integración que sigue viva.
		current, rerr := h.load(c)
		if rerr != nil {
			h.render(c, status, ent, integrationView{}, notice)
			return
		}
		h.render(c, status, ent, viewFromIntegration(current), notice)
		return
	}

	// El estado resultante se sabe sin preguntar (la plataforma responde 204 y deja al tenant en
	// local/local), así que no se gasta un GET que además podría fallar y enturbiar un borrado que
	// sí se hizo.
	h.render(c, http.StatusOK, ent, integrationView{
		EventsAdapter:  integrationAdapterLocal,
		CatalogAdapter: integrationAdapterLocal,
		Loaded:         true,
	}, &integrationNotice{
		Success: true,
		Message: "Integración quitada. Los eventos se quedan en wApp y el secreto de firma se borró.",
	})
}

// load lee la configuración del tenant con la cascada de refresco de sesión.
func (h *IntegrationsHandler) load(c *gin.Context) (*apiclient.Integration, error) {
	var current *apiclient.Integration
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		current, gerr = h.api.GetIntegration(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

// render pinta la pantalla. El estado de la cola se resuelve AQUÍ, en el único embudo por el que pasa
// todo lo que se emite, y no en cada entrada: el panel es estado de la PÁGINA, no resultado de la
// operación que trajo al operador. Resolverlo en ShowIntegrations solamente dejaría el panel en
// blanco justo después de guardar o de quitar el puente, que es cuando más se mira.
//
// Solo se pregunta con la capacidad puesta: sin `crm_bridge` no se emite nada de esta pantalla, así
// que el viaje solo serviría para cobrar un 403 previsible.
func (h *IntegrationsHandler) render(c *gin.Context, status int, ent entitlementsView, view integrationView, notice *integrationNotice) {
	outbox := outboxView{}
	if ent.Has(integrationsFeature) {
		outbox = h.resolveOutbox(c)
	}
	render(h.cfg, c, status, "integrations.html", gin.H{
		"Title":                 "Integraciones",
		"View":                  view,
		"Notice":                notice,
		outboxDataKey:           outbox,
		entitlementsDataKey:     ent,
		intakesNavDataKey:       ent.Has(intakesFeature),
		catalogImportNavDataKey: ent.Has(catalogImportFeature),
		integrationsNavDataKey:  ent.Has(integrationsFeature),
		tenantLLMNavDataKey:     ent.Has(tenantLLMFeature),
	})
}

// resolveOutbox pide el estado de la cola y devuelve SIEMPRE una vista usable: el fallo se traduce en
// la vista cero más un aviso, nunca en un error que tumbe la página (mismo patrón que
// resolveEntitlements). Un contador que no se puede leer no puede dejar sin configurar el puente: la
// pantalla que arregla el problema no puede caerse por el panel que lo señala.
//
// El 401 no se trata aparte —withAuthRetry ya refresca y reintenta—: quien decide expulsar al
// operador es la llamada de negocio de la página, no esta consulta accesoria.
func (h *IntegrationsHandler) resolveOutbox(c *gin.Context) outboxView {
	var counters *apiclient.OutboxCounters
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		counters, gerr = h.api.GetOutboxCounters(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil || counters == nil {
		slog.Warn("no se pudo leer el estado de la cola de entregas", "error", err)
		return outboxView{Notice: outboxNotice(err)}
	}
	return outboxView{
		Loaded:           true,
		Pending:          counters.Pending,
		Delivering:       counters.Delivering,
		Delivered:        counters.Delivered,
		Dead:             counters.Dead,
		OldestPendingAge: colaAge(counters.OldestPendingAt),
	}
}

// outboxNotice traduce el fallo a un aviso legible, sin filtrar el detalle del upstream (mismo
// criterio que el resto de mappers de esta pantalla).
func outboxNotice(err error) string {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return "Tu usuario no puede consultar el estado de las entregas de este tenant."
	}
	return "No se pudo consultar el estado de las entregas ahora mismo. Vuelve a cargar la página en un rato."
}

// colaAge convierte la marca de la entrega en cola más antigua en cuánto lleva esperando, en palabras.
//
// Se resuelve en el servidor y no en el HTML porque es lo único que hace útil el dato: una fecha UTC
// obliga a restar de cabeza, y la pregunta que este panel contesta es «¿cuánto lleva atascado esto?».
// Cadena vacía cuando no hay nada en cola (la API omite el campo) o cuando la marca no se entiende:
// preferimos callar la antigüedad a inventarla, y los contadores siguen sirviendo sin ella.
func colaAge(raw string) string {
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		slog.Warn("la plataforma mandó una marca de cola que no se entiende", "valor", raw, "error", err)
		return ""
	}
	return duraciónLegible(time.Since(t))
}

// duraciónLegible dice una duración como la diría una persona. La escala se corta en días: más
// precisión no cambia ninguna decisión —una cola de tres días y una de tres días y cuatro horas piden
// exactamente lo mismo— y sí alarga la frase.
//
// Una duración negativa (reloj del servidor por detrás del de la base) cae en el primer caso y se
// dice «menos de un minuto»: es lo más cercano a la verdad y no enseña un número absurdo.
func duraciónLegible(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "menos de un minuto"
	case d < time.Hour:
		return cuenta(int(d.Minutes()), "minuto", "minutos")
	case d < 24*time.Hour:
		return cuenta(int(d.Hours()), "hora", "horas")
	default:
		return cuenta(int(d.Hours()/24), "día", "días")
	}
}

// cuenta arma «1 hora» / «5 horas» sin que el singular se cuele en plural.
func cuenta(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// settingsFromForm arma la foto que se va a mandar y la vista con la que se re-pinta si hay rechazo.
// Devuelve el mensaje de error (vacío = admisible) en vez de escribir la respuesta.
//
// Lo que se comprueba aquí es SOLO lo que la pantalla ofrece: que el adaptador de eventos sea uno de
// los dos del desplegable. El resto de reglas —URL absoluta http(s), longitud del secreto, que un
// puente encendido tenga endpoint y secreto— las decide la plataforma y sus motivos se le enseñan al
// operador tal cual: duplicarlas aquí crearía una segunda fuente que se quedaría vieja en silencio.
func settingsFromForm(c *gin.Context, current *apiclient.Integration) (apiclient.IntegrationSettings, integrationView, string) {
	events := strings.TrimSpace(c.PostForm("events_adapter"))
	endpoint := strings.TrimSpace(c.PostForm("endpoint_url"))
	// El secreto NO se recorta ni se normaliza: es material de firma y cualquier reescritura silenciosa
	// haría que el HMAC del puente dejara de cuadrar sin que nadie supiera por qué.
	secret := c.PostForm("secret")
	enabled := c.PostForm("enabled") != ""

	// La vista de re-pintado se arma SIN el secreto tecleado (a propósito) y con el estado del que
	// está guardado, que viene de la relectura.
	typed := integrationView{
		Configured:        current.Configured,
		EventsAdapter:     events,
		CatalogAdapter:    current.CatalogAdapter,
		EndpointURL:       endpoint,
		Enabled:           enabled,
		SecretSet:         current.SecretSet,
		SecretFingerprint: current.SecretFingerprint,
		UpdatedAt:         current.UpdatedAt,
		Loaded:            true,
	}

	if events != integrationAdapterLocal && events != integrationAdapterWebhook {
		return apiclient.IntegrationSettings{}, typed,
			"Elige por dónde salen los eventos: dentro de wApp o por el puente."
	}

	catalog := current.CatalogAdapter
	if catalog == "" {
		catalog = integrationAdapterLocal
	}
	return apiclient.IntegrationSettings{
		CatalogAdapter: catalog,
		EventsAdapter:  events,
		EndpointURL:    endpoint,
		Secret:         secret,
		Enabled:        enabled,
	}, typed, ""
}

// viewFromIntegration arma la vista con lo que devolvió la API.
func viewFromIntegration(in *apiclient.Integration) integrationView {
	return integrationView{
		Configured:        in.Configured,
		EventsAdapter:     in.EventsAdapter,
		CatalogAdapter:    in.CatalogAdapter,
		EndpointURL:       in.EndpointURL,
		Enabled:           in.Enabled,
		SecretSet:         in.SecretSet,
		SecretFingerprint: in.SecretFingerprint,
		UpdatedAt:         in.UpdatedAt,
		Loaded:            true,
	}
}

// savedIntegrationMessage redacta la confirmación diciendo el estado en el que quedó el puente, que
// es lo que el operador necesita comprobar de un vistazo: encendido y firmando, o guardado pero sin
// entregar todavía.
func savedIntegrationMessage(in *apiclient.Integration) string {
	if in.EventsAdapter != integrationAdapterWebhook {
		return "Configuración guardada. Los eventos se quedan dentro de wApp: no se entrega nada a un puente."
	}
	if !in.Enabled {
		return "Configuración guardada, con el puente APAGADO: todavía no se entrega nada. " +
			"Enciéndelo cuando el receptor esté listo."
	}
	return "Configuración guardada. El puente está encendido: los pedidos se entregan firmados en el endpoint."
}

// mapIntegrationReadError traduce el fallo de lectura sin filtrar el detalle del upstream.
func mapIntegrationReadError(err error) (int, *integrationNotice) {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &integrationNotice{
			Message: "Tu usuario no puede consultar la integración de este tenant.",
		}
	}
	slog.Warn("no se pudo leer la integración del tenant", "error", err)
	return http.StatusBadGateway, &integrationNotice{
		Message: "No se pudo cargar la integración ahora mismo. Inténtalo de nuevo.",
	}
}

// mapIntegrationSaveError traduce el fallo del guardado. El 400 y el 422 traen motivo y se le enseña
// al operador tal cual: el primero dice qué tiene de malo la configuración (URL no absoluta, secreto
// corto, puente encendido sin endpoint o sin secreto) y el segundo que el adaptador de catálogo
// «http» está diferido.
func mapIntegrationSaveError(err error) (int, *integrationNotice) {
	if rej, ok := apiclient.RejectionOf(err); ok {
		msg := "La plataforma rechazó la configuración. Revisa el endpoint y el secreto."
		if rej.Message != "" {
			msg = "La plataforma rechazó la configuración: " + rej.Message
		}
		if rej.StatusCode == http.StatusUnprocessableEntity {
			return http.StatusUnprocessableEntity, &integrationNotice{Message: msg}
		}
		return http.StatusBadRequest, &integrationNotice{Message: msg}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &integrationNotice{
			Message: "Tu usuario no puede modificar la integración de este tenant.",
		}
	}
	// El error NO lleva el secreto: el apiclient nunca lo mete en el mensaje y la respuesta de la API
	// tampoco lo devuelve. Este log es el sitio donde un descuido de esos aparecería.
	slog.Warn("no se pudo guardar la integración del tenant", "error", err)
	return http.StatusBadGateway, &integrationNotice{
		Message: "No se pudo guardar la integración ahora mismo. Vuelve a intentarlo; nada se ha cambiado.",
	}
}

// mapIntegrationDeleteError traduce el fallo del borrado.
func mapIntegrationDeleteError(err error) (int, *integrationNotice) {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &integrationNotice{
			Message: "Tu usuario no puede quitar la integración de este tenant.",
		}
	}
	slog.Warn("no se pudo quitar la integración del tenant", "error", err)
	return http.StatusBadGateway, &integrationNotice{
		Message: "No se pudo quitar la integración ahora mismo. Vuelve a intentarlo; sigue como estaba.",
	}
}
