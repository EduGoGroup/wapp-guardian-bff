package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// tenantLLMFeature es la capacidad que abre la configuración LLM del tenant. Es la MISMA clave con la
// que la plataforma gatea las tres rutas (`RequireFeature("api_llm")`, también el GET): aquí decide lo
// que se PINTA, allí lo que se PUEDE, y esconder un formulario nunca sustituye a ese corte.
const tenantLLMFeature = "api_llm"

// tenantLLMRoute es la ruta de la pantalla en el BFF (no la de la API pública).
const tenantLLMRoute = "/tenant-llm"

// tenantLLMNavDataKey es la clave con la que una página declara que el enlace a la configuración LLM
// debe salir en la barra superior. Misma mecánica que las otras tres: si la página no resolvió las
// features, la clave no existe, se lee como falsa y el enlace no llega al HTML.
const tenantLLMNavDataKey = "TenantLLMNav"

// Las DOS VÍAS que ofrece la pantalla. Es el vocabulario cerrado de la plataforma, entero: `local`
// —quien interpreta es el equipo del propio local, sin salir a Internet— y `api` —quien interpreta es
// un proveedor externo, con credencial de pago y consentimiento explícito—.
const (
	tenantLLMViaLocal = "local"
	tenantLLMViaAPI   = "api"
)

// tenantLLMProviders es el vocabulario de proveedores de la vía `api`, en el orden en que se ofrecen.
// Es EXACTAMENTE el que la plataforma admite: ofrecer aquí uno de más sería enseñar una opción cuyo
// único desenlace posible es un rechazo.
var tenantLLMProviders = []tenantLLMProvider{
	{Value: "anthropic", Label: "Anthropic (Claude)"},
	{Value: "gemini", Label: "Google (Gemini)"},
}

// tenantLLMProvider es una opción del desplegable de proveedor.
type tenantLLMProvider struct {
	Value string
	Label string
}

// tenantLLMNotice es el aviso de una operación sobre la configuración LLM.
type tenantLLMNotice struct {
	Success bool
	Message string
}

// tenantLLMView es lo que pinta la plantilla.
//
// 🔴 NO TIENE CAMPO PARA LA CLAVE, y es la misma razón por la que no lo tiene `apiclient.TenantLLM`:
// el valor no debe existir en esta capa. La pantalla enseña si HAY una (KeySet) y nada más — ni
// enmascarada, ni con huella, porque la API no devuelve ninguna de las dos cosas y esta pantalla no
// inventa lo que no le dan. Ni siquiera se conserva lo que el operador acaba de teclear para
// re-pintarlo tras un rechazo: el campo vuelve vacío.
//
// Al no existir el campo, «la clave nunca se re-pinta en el HTML» no es una disciplina que haya que
// recordar en cada plantilla: es que no hay de dónde sacarla.
type tenantLLMView struct {
	// Configured distingue «este tenant tiene configuración LLM puesta» de «nunca configuró nada y
	// está en la vía local por defecto». Es lo que decide si se ofrece quitarla.
	Configured bool
	// Via es quién interpreta los mensajes: `local` (el equipo del local) o `api` (un tercero).
	Via string
	// Provider y Model solo aplican a la vía `api`. En la vía local vienen vacíos y no se inventan.
	Provider string
	Model    string
	// KeySet dice si hay credencial guardada. Es un booleano y es TODO lo que la API publica de ella.
	KeySet bool
	// ConsentedAt y UpdatedAt son marcas UTC tal como las manda la plataforma (vacías si no aplican).
	ConsentedAt string
	UpdatedAt   string
	// Loaded distingue «este tenant no tiene configuración» de «no se pudo leer».
	Loaded bool
}

// IsAPI responde si la interpretación sale hacia un tercero. La plantilla lo usa para marcar la opción
// elegida sin meter lógica en el HTML.
func (v tenantLLMView) IsAPI() bool { return v.Via == tenantLLMViaAPI }

// IsProvider responde si ese es el proveedor elegido, para marcar la opción del desplegable.
func (v tenantLLMView) IsProvider(p string) bool { return v.Provider == p }

// Providers expone el vocabulario de proveedores a la plantilla, para que el desplegable no sea una
// lista escrita a mano en el HTML que se desincronice de la que valida el servidor.
func (v tenantLLMView) Providers() []tenantLLMProvider { return tenantLLMProviders }

// TenantLLMHandler sirve la pantalla de configuración LLM del tenant: quién interpreta los mensajes de
// sus clientes —el equipo del propio local o un proveedor externo—, con qué proveedor y modelo, y con
// qué credencial.
//
// Es una pantalla PERMANENTE (ADR-0035, D-047.5 / D-047.9): configurar el proveedor de inferencia es
// capa técnica del producto, no una operación de negocio, así que no migra a la app KMP y no lleva
// marca de provisionalidad.
type TenantLLMHandler struct {
	cfg  *config.Config
	api  TenantLLMAPI
	auth *AuthHandler
}

// NewTenantLLMHandler construye el handler sobre el puerto de configuración LLM.
func NewTenantLLMHandler(cfg *config.Config, api TenantLLMAPI, auth *AuthHandler) *TenantLLMHandler {
	return &TenantLLMHandler{cfg: cfg, api: api, auth: auth}
}

// ShowTenantLLM pinta la configuración LLM del tenant.
func (h *TenantLLMHandler) ShowTenantLLM(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	// Sin la capacidad no se llama a la API: la plataforma cortaría con 403 en el GET igualmente
	// (también el GET va gateado), y gastar el viaje solo serviría para llenar el log de rechazos
	// previsibles. El gate real de lo que se emite está en la plantilla.
	if !ent.Has(tenantLLMFeature) {
		h.render(c, http.StatusOK, ent, tenantLLMView{}, nil)
		return
	}

	current, err := h.load(c)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapTenantLLMReadError(err)
		h.render(c, status, ent, tenantLLMView{}, notice)
		return
	}
	h.render(c, http.StatusOK, ent, viewFromTenantLLM(current), nil)
}

// DoSaveTenantLLM guarda la configuración COMPLETA que trae el formulario.
//
// 📌 AQUÍ NO HAY RELECTURA PREVIA, y esa es la diferencia real con la pantalla de integraciones: allí
// el PUT es un upsert completo del que la pantalla solo edita la mitad (el adaptador de catálogo se
// preserva releyéndolo), y aquí el formulario edita TODOS los campos que la plataforma guarda. Un
// viaje que no aporta nada al cuerpo solo añadiría una forma de que el guardado fallara.
//
// La relectura sí existe, pero DESPUÉS y solo si algo se rechaza: sirve para que el re-pintado diga el
// estado que hay hoy en el servidor y no uno inventado (ver repaint).
func (h *TenantLLMHandler) DoSaveTenantLLM(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(tenantLLMFeature) {
		// Un POST sin la capacidad es un envío forzado (el formulario no se emite siquiera): se dice
		// que no y no se llama a la API, que respondería 403 de todas formas.
		h.render(c, http.StatusForbidden, ent, tenantLLMView{}, &tenantLLMNotice{
			Message: "El plan de este tenant no incluye configurar un proveedor de IA propio.",
		})
		return
	}

	settings, typed, msg := tenantLLMFromForm(c)
	if msg != "" {
		// Se re-pinta lo que el operador tecleó (menos la clave), no lo que hay en el servidor: su
		// trabajo no se tira por un rechazo y puede corregir sobre lo que escribió.
		h.render(c, http.StatusBadRequest, ent, h.repaint(c, typed), &tenantLLMNotice{Message: msg})
		return
	}

	var saved *apiclient.TenantLLM
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var serr error
		saved, serr = h.api.SaveTenantLLM(c.Request.Context(), accessToken, settings)
		return serr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapTenantLLMSaveError(err)
		h.render(c, status, ent, h.repaint(c, typed), notice)
		return
	}

	h.render(c, http.StatusOK, ent, viewFromTenantLLM(saved), &tenantLLMNotice{
		Success: true,
		Message: savedTenantLLMMessage(saved),
	})
}

// DoDeleteTenantLLM quita la configuración LLM del tenant, que vuelve a la vía local por defecto.
//
// Se van JUNTAS la credencial y el consentimiento, porque viven en la misma fila y porque es lo
// correcto: un consentimiento que sobreviviera a la retirada de la clave sería un permiso vivo sin
// nada que lo ejerza. La pantalla lo dice con esas palabras.
func (h *TenantLLMHandler) DoDeleteTenantLLM(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(tenantLLMFeature) {
		h.render(c, http.StatusForbidden, ent, tenantLLMView{}, &tenantLLMNotice{
			Message: "El plan de este tenant no incluye configurar un proveedor de IA propio.",
		})
		return
	}

	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		return h.api.DeleteTenantLLM(c.Request.Context(), accessToken)
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapTenantLLMDeleteError(err)
		// Se re-pinta con lo que haya en el servidor: si el borrado no se hizo, la pantalla debe
		// seguir enseñando la configuración que sigue viva.
		current, rerr := h.load(c)
		if rerr != nil {
			h.render(c, status, ent, tenantLLMView{}, notice)
			return
		}
		h.render(c, status, ent, viewFromTenantLLM(current), notice)
		return
	}

	// El estado resultante se sabe sin preguntar (la plataforma responde 204 y deja al tenant sin
	// fila, o sea en la vía local), así que no se gasta un GET que además podría fallar y enturbiar un
	// borrado que sí se hizo.
	h.render(c, http.StatusOK, ent, tenantLLMView{
		Via:    tenantLLMViaLocal,
		Loaded: true,
	}, &tenantLLMNotice{
		Success: true,
		Message: "Configuración quitada. La interpretación vuelve al equipo de tu local, y la credencial " +
			"y el consentimiento se borraron juntos.",
	})
}

// load lee la configuración del tenant con la cascada de refresco de sesión.
func (h *TenantLLMHandler) load(c *gin.Context) (*apiclient.TenantLLM, error) {
	var current *apiclient.TenantLLM
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		current, gerr = h.api.GetTenantLLM(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil {
		return nil, err
	}
	return current, nil
}

// repaint completa la vista de un RECHAZO con el estado que hay hoy en el servidor, releído en
// best-effort. Lo que el operador tecleó manda; lo que se rellena es solo lo que el formulario no
// puede saber (si hay fila, si hay credencial guardada, desde cuándo).
//
// Si la relectura falla se devuelve la vista tecleada tal cual, que es lo honesto: sin datos no se
// afirma que haya credencial guardada ni se ofrece quitar una configuración que no se ha podido ver.
// Y NO tumba el re-pintado: la pantalla que arregla el problema no puede caerse por una consulta
// accesoria.
//
// 🔴 Ninguna rama de aquí toca la credencial: no hay campo en `tenantLLMView` donde ponerla, ni en
// `apiclient.TenantLLM` de donde sacarla.
func (h *TenantLLMHandler) repaint(c *gin.Context, typed tenantLLMView) tenantLLMView {
	current, err := h.load(c)
	if err != nil || current == nil {
		return typed
	}
	typed.Configured = current.Configured
	typed.KeySet = current.KeySet
	typed.ConsentedAt = current.ConsentedAt
	typed.UpdatedAt = current.UpdatedAt
	return typed
}

// render pinta la pantalla. Es el único embudo por el que pasa todo lo que se emite.
func (h *TenantLLMHandler) render(c *gin.Context, status int, ent entitlementsView, view tenantLLMView, notice *tenantLLMNotice) {
	render(h.cfg, c, status, "tenant-llm.html", gin.H{
		"Title":                 "Proveedor de IA",
		"View":                  view,
		"Notice":                notice,
		entitlementsDataKey:     ent,
		intakesNavDataKey:       ent.Has(intakesFeature),
		catalogImportNavDataKey: ent.Has(catalogImportFeature),
		integrationsNavDataKey:  ent.Has(integrationsFeature),
		tenantLLMNavDataKey:     ent.Has(tenantLLMFeature),
	})
}

// tenantLLMFromForm arma la foto que se va a mandar y la vista con la que se re-pinta si hay rechazo.
// Devuelve el mensaje de error (vacío = admisible) en vez de escribir la respuesta.
//
// Lo que se comprueba aquí es SOLO lo que la pantalla ofrece: que la vía sea una de las dos del
// desplegable. El resto de reglas —consentimiento obligatorio, vocabulario del proveedor, modelo no
// vacío, longitud de la clave— las decide la plataforma y sus motivos se le enseñan al operador:
// duplicarlas aquí crearía una segunda fuente que se quedaría vieja en silencio. En particular NO se
// mide la clave: el cloud evita a propósito decir su longitud real para no ser un medidor, y añadir el
// medidor aquí sería deshacer esa decisión desde el cliente.
//
// 🔴 LA ASIMETRÍA DE LA VÍA ES DELIBERADA Y VIENE DE LA API: con `via = local` NO viaja proveedor, ni
// modelo, ni credencial, ni consentimiento — la vía local no manda texto a ningún tercero, así que no
// hay a qué consentir ni a quién llamar. Rellenar esos campos «por si acaso» guardaría una
// contradicción en la base (una fila local con proveedor) y obligaría al operador a teclear una
// credencial para elegir no usar ninguna.
func tenantLLMFromForm(c *gin.Context) (apiclient.TenantLLMSettings, tenantLLMView, string) {
	via := strings.TrimSpace(c.PostForm("via"))
	provider := strings.TrimSpace(c.PostForm("provider"))
	model := strings.TrimSpace(c.PostForm("model"))
	// La clave NO se recorta ni se normaliza: es material de credencial y cualquier reescritura
	// silenciosa guardaría algo distinto de lo que el operador pegó. Que un pegado con espacios falle
	// al llamar al proveedor es información; que funcione a veces, no.
	apiKey := c.PostForm("api_key")
	consented := c.PostForm("consented") != ""

	// La vista de re-pintado se arma SIN la clave tecleada (a propósito): el campo vuelve vacío.
	typed := tenantLLMView{
		Via:      via,
		Provider: provider,
		Model:    model,
		Loaded:   true,
	}

	if via != tenantLLMViaLocal && via != tenantLLMViaAPI {
		return apiclient.TenantLLMSettings{}, typed,
			"Elige quién interpreta los mensajes: el equipo de tu local o un proveedor externo."
	}

	if via == tenantLLMViaLocal {
		return apiclient.TenantLLMSettings{Via: tenantLLMViaLocal}, typed, ""
	}
	return apiclient.TenantLLMSettings{
		Via:       tenantLLMViaAPI,
		Provider:  provider,
		Model:     model,
		APIKey:    apiKey,
		Consented: consented,
	}, typed, ""
}

// viewFromTenantLLM arma la vista con lo que devolvió la API.
func viewFromTenantLLM(in *apiclient.TenantLLM) tenantLLMView {
	return tenantLLMView{
		Configured:  in.Configured,
		Via:         in.Via,
		Provider:    in.Provider,
		Model:       in.Model,
		KeySet:      in.KeySet,
		ConsentedAt: in.ConsentedAt,
		UpdatedAt:   in.UpdatedAt,
		Loaded:      true,
	}
}

// savedTenantLLMMessage redacta la confirmación diciendo el estado en el que quedó la configuración,
// que es lo que el operador necesita comprobar de un vistazo.
func savedTenantLLMMessage(in *apiclient.TenantLLM) string {
	if in.Via != tenantLLMViaAPI {
		return "Configuración guardada. Interpreta el equipo de tu local: el texto de las conversaciones " +
			"no sale hacia ningún tercero, y la credencial que hubiera guardada se retiró."
	}
	return "Configuración guardada. A partir de ahora interpreta el proveedor externo con la credencial " +
		"que acabas de escribir."
}

// tenantLLMRejectionMessages traduce los CÓDIGOS de rechazo de la plataforma a algo que una persona
// pueda leer.
//
// Existe porque los 400 de este endpoint no tienen todos la misma forma: unos traen una frase legible
// en `error` («model es obligatorio y no puede pasar de 128 caracteres») y otros traen un CÓDIGO
// («consent_required», «invalid_provider», «invalid_via») con la explicación en un campo `detail` que
// el cliente no decodifica. Enseñar el código tal cual sería enseñarle a la dueña de un local la
// palabra «consent_required» y esperar que sepa qué hacer.
//
// Lo que NO hace este mapa es mejorar los mensajes que ya son frases: esos se enseñan tal cual, porque
// son la única forma de saber qué corregir y porque la plataforma los redactó evitando a propósito
// filtrar la longitud real de la credencial.
var tenantLLMRejectionMessages = map[string]string{
	"invalid_via": "Elige quién interpreta los mensajes: el equipo de tu local o un proveedor externo.",
	"consent_required": "Falta tu consentimiento. Marca la casilla que autoriza a que el texto de las " +
		"conversaciones de tus clientes salga hacia el proveedor externo.",
	"invalid_provider": "Ese proveedor no está admitido. Elige uno de los del desplegable.",
}

// mapTenantLLMReadError traduce el fallo de lectura sin filtrar el detalle del upstream.
func mapTenantLLMReadError(err error) (int, *tenantLLMNotice) {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &tenantLLMNotice{
			Message: "Tu usuario no puede consultar el proveedor de IA de este tenant. " +
				"Pídeselo a quien administre la cuenta.",
		}
	}
	slog.Warn("no se pudo leer la configuración LLM del tenant", "error", err)
	return http.StatusBadGateway, &tenantLLMNotice{
		Message: "No se pudo cargar el proveedor de IA ahora mismo. Inténtalo de nuevo.",
	}
}

// mapTenantLLMSaveError traduce el fallo del guardado. El 400 y el 422 traen motivo y se le enseña al
// operador —traducido si vino como código, tal cual si ya era una frase—: es lo único que le dice qué
// corregir.
//
// 🔴 EL MENSAJE NUNCA LLEVA LA CREDENCIAL, y no por prudencia al escribirlo: no hay de dónde sacarla.
// El apiclient no la mete en ningún error y la plataforma no la devuelve. Este log es el sitio donde
// un descuido de esos aparecería.
func mapTenantLLMSaveError(err error) (int, *tenantLLMNotice) {
	if rej, ok := apiclient.RejectionOf(err); ok {
		msg := "La plataforma rechazó la configuración. Revisa el proveedor, el modelo y la credencial."
		if legible, ok := tenantLLMRejectionMessages[rej.Message]; ok {
			msg = legible
		} else if rej.Message != "" {
			msg = "La plataforma rechazó la configuración: " + rej.Message
		}
		if rej.StatusCode == http.StatusUnprocessableEntity {
			return http.StatusUnprocessableEntity, &tenantLLMNotice{Message: msg}
		}
		return http.StatusBadRequest, &tenantLLMNotice{Message: msg}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &tenantLLMNotice{
			Message: "Tu usuario no puede modificar el proveedor de IA de este tenant. " +
				"Pídeselo a quien administre la cuenta.",
		}
	}
	slog.Warn("no se pudo guardar la configuración LLM del tenant", "error", err)
	return http.StatusBadGateway, &tenantLLMNotice{
		Message: "No se pudo guardar el proveedor de IA ahora mismo. Vuelve a intentarlo; nada se ha cambiado.",
	}
}

// mapTenantLLMDeleteError traduce el fallo del borrado.
func mapTenantLLMDeleteError(err error) (int, *tenantLLMNotice) {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &tenantLLMNotice{
			Message: "Tu usuario no puede quitar el proveedor de IA de este tenant. " +
				"Pídeselo a quien administre la cuenta.",
		}
	}
	slog.Warn("no se pudo quitar la configuración LLM del tenant", "error", err)
	return http.StatusBadGateway, &tenantLLMNotice{
		Message: "No se pudo quitar el proveedor de IA ahora mismo. Vuelve a intentarlo; sigue como estaba.",
	}
}
