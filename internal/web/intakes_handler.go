package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// intakesFeature es la capacidad que abre la bandeja de solicitudes. Es la MISMA clave con la que la
// plataforma gatea las tres rutas (`RequireFeature("cart_basic")`): aquí decide lo que se PINTA, allí
// lo que se PUEDE, y esconder un bloque nunca sustituye a ese corte.
const intakesFeature = "cart_basic"

// defaultIntakesPageSize es el tamaño de página del listado. Coincide con el default de la API (50);
// se manda explícito para que la URL diga siempre en qué página está el operador.
const defaultIntakesPageSize = 50

// maxIntakesPageSize es el techo que acepta la API. Se aplica también aquí para no gastar un viaje en
// una petición que la plataforma va a rechazar.
const maxIntakesPageSize = 200

// intakeNotice es el aviso de una operación sobre la bandeja (éxito o fallo) tal como lo lee la
// plantilla.
type intakeNotice struct {
	Success bool
	Message string
}

// intakesFilterView son los filtros tal como vuelven al formulario: se re-pintan siempre para que el
// operador vea con qué criterios está mirando la bandeja.
type intakesFilterView struct {
	From    string
	To      string
	Status  string
	Session string
}

// intakesPageView es el paginador ya resuelto. Las cuentas se hacen en Go —no en la plantilla— y las
// URLs de anterior/siguiente arrastran los filtros vigentes: cambiar de página no puede cambiar en
// silencio lo que se está mirando.
type intakesPageView struct {
	Page      int
	PageSize  int
	Total     int
	PageCount int
	RangeFrom int
	RangeTo   int
	HasPrev   bool
	HasNext   bool
	PrevURL   string
	NextURL   string
}

// intakeDetailView es el detalle con los destinos que el `<select>` puede ofrecer.
//
// La lista NO se calcula aquí: el ciclo de vida (D-041.10) vive en la plataforma y replicarlo en el
// BFF garantizaría que las dos copias divergieran en cuanto la Ola 4 lo amplíe. Por eso la pantalla
// solo ofrece lo que la plataforma dice, y cuando la plataforma no lo dice, lo declara en vez de
// adivinar (ver transitionsOf).
type intakeDetailView struct {
	Detail *apiclient.IntakeDetail
	// Transitions son los destinos ofrecidos en el desplegable (vacío ⇒ no se ofrece ninguno).
	Transitions []string
	// Known distingue «este estado es terminal» de «la plataforma no informa de los destinos».
	Known bool
	// FromRejection avisa de que los destinos salieron del rechazo 422 de un intento previo, no del
	// detalle: es información válida —misma fuente de verdad, el dominio— pero llega tarde y conviene
	// que se note.
	FromRejection bool
	// Edit es el formulario de corrección manual de las líneas (Plan 041 · T4.10).
	Edit *intakeEditForm
}

// intakeDetailRender son las variables con las que se pinta el detalle. Va como struct y no como
// una lista de parámetros porque ya son siete y la mitad son opcionales: una llamada con cuatro
// `nil` seguidos no dice cuál es cuál, y añadir la octava obligaría a tocar todos los sitios.
type intakeDetailRender struct {
	// status es el código con el que se responde (la página se pinta igual).
	status int
	// id es la solicitud a pintar.
	id string
	// notice es el aviso de la operación que trajo hasta aquí.
	notice *intakeNotice
	// allowedFromRejection son los destinos que devolvió un 422 del cambio de estado.
	allowedFromRejection []string
	// entitlements ya resueltas por quien llama (nil ⇒ se resuelven aquí). Existe para que un POST
	// que ya preguntó por la feature —para no gastar el viaje a una ruta que va a dar 403— no la
	// vuelva a preguntar al repintar.
	entitlements *entitlementsView
	// detail ya cargado (nil ⇒ se relee de la API). Lo usa el guardado de líneas, que recibe el
	// detalle ya actualizado en la respuesta del PUT: releerlo abriría un hueco por el que cabe la
	// edición de otro operador y pintaría un estado que nadie pidió.
	detail *apiclient.IntakeDetail
	// rows es lo que el operador tecleó en el formulario de líneas (nil ⇒ el formulario se arma con
	// lo que dice la plataforma). Al repintar tras un rechazo su trabajo no se tira.
	rows []intakeEditRow
	// defects son los problemas de esa tentativa, ya redactados.
	defects []intakeEditDefect
	// editableIn son los estados desde los que la plataforma dice que sí se edita (salen del 422).
	editableIn []string
}

// IntakesHandler sirve la bandeja de solicitudes: listado con filtros, detalle y cambio de estado.
//
// PANTALLA PROVISIONAL (ADR-0035): esta consola web muere cuando la operación pase a la app KMP
// (planes 045/047). Está construida para ser borrada de una pieza —handler, plantillas y puerto
// propios—, no para crecer.
type IntakesHandler struct {
	cfg  *config.Config
	api  IntakesAPI
	auth *AuthHandler
}

// NewIntakesHandler construye un IntakesHandler dependiente de IntakesAPI y AuthHandler.
func NewIntakesHandler(cfg *config.Config, api IntakesAPI, auth *AuthHandler) *IntakesHandler {
	return &IntakesHandler{cfg: cfg, api: api, auth: auth}
}

// intakesListRender son las variables con las que se pinta la bandeja. Va como struct por lo mismo
// que intakeDetailRender: la mitad son opcionales y una llamada con tres `nil` seguidos no dice
// cuál es cuál.
type intakesListRender struct {
	// status es el código con el que se responde (la bandeja se pinta igual).
	status int
	// filter son los filtros vigentes, que mandan tanto en la consulta como en lo que se re-pinta.
	filter apiclient.IntakeFilter
	// notice es el aviso de la operación que trajo hasta aquí. Manda sobre el de la relectura.
	notice *intakeNotice
	// entitlements ya resueltas por quien llama (nil ⇒ se resuelven aquí). Existe para que un POST
	// que ya preguntó por la feature —para no gastar el viaje a una ruta que va a dar 403— no la
	// vuelva a preguntar al repintar.
	entitlements *entitlementsView
	// discard es el descarte por lotes: el que espera confirmación o el que ya se ejecutó.
	discard *intakeDiscardView
}

// ShowIntakes pinta la bandeja del tenant con los filtros de la query.
func (h *IntakesHandler) ShowIntakes(c *gin.Context) {
	h.renderIntakesList(c, intakesListRender{
		status: http.StatusOK, filter: intakeFilterFromQuery(c),
	})
}

// renderIntakesList pinta la bandeja: la lista con sus filtros y, si la operación que trajo hasta
// aquí lo pide, el descarte por lotes (lo que espera confirmación o lo que ya pasó).
func (h *IntakesHandler) renderIntakesList(c *gin.Context, r intakesListRender) {
	var entitlements entitlementsView
	if r.entitlements != nil {
		entitlements = *r.entitlements
	} else {
		entitlements = resolveEntitlements(c, h.auth, h.api)
	}

	data := gin.H{
		"Title":                 "Solicitudes",
		"Filter":                filterView(r.filter),
		"StatusOptions":         intakeStatusOptions,
		"Notice":                r.notice,
		"Discard":               r.discard,
		"DiscardURL":            discardURL(r.filter),
		entitlementsDataKey:     entitlements,
		intakesNavDataKey:       entitlements.Has(intakesFeature),
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
		integrationsNavDataKey:  entitlements.Has(integrationsFeature),
	}

	// Sin la capacidad no se llama a la API: la plataforma cortaría con 403 igualmente, y gastar el
	// viaje solo serviría para llenar el log de rechazos previsibles. El gate real de lo que se emite
	// está en la plantilla.
	if !entitlements.Has(intakesFeature) {
		render(h.cfg, c, r.status, "intakes.html", data)
		return
	}

	var page *apiclient.IntakePage
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		page, lerr = h.api.ListIntakes(c.Request.Context(), accessToken, r.filter)
		return lerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		listStatus, listNotice := mapIntakeListError(err)
		// El aviso de la operación que falló manda sobre el de la relectura (mismo criterio que el
		// detalle): el operador necesita saber por qué no se hizo lo suyo, no que además no se pudo
		// repintar.
		if r.notice == nil {
			data["Notice"] = listNotice
		}
		data["IntakesError"] = true
		// Sin la bandeja delante NO se ofrece el botón que descarta. El desglose de un lote YA
		// ejecutado sí se conserva —es lo que pasó, y ocultarlo dejaría al dueño sin saber qué se
		// descartó—, pero confirmar a ciegas es exactamente lo que esta pantalla no puede hacer:
		// enseñar qué se va a matar es la condición de una acción sin vuelta atrás (D-041.22).
		if r.discard != nil && r.discard.Confirming {
			data["Discard"] = nil
		}
		render(h.cfg, c, listStatus, "intakes.html", data)
		return
	}

	if r.discard != nil {
		r.discard.describeWith(page.Intakes)
	}
	data["Intakes"] = page.Intakes
	data["Pager"] = pagerView(r.filter, page)
	render(h.cfg, c, r.status, "intakes.html", data)
}

// ShowIntakeDetail pinta una solicitud con sus líneas, el cambio de estado y la corrección manual.
func (h *IntakesHandler) ShowIntakeDetail(c *gin.Context) {
	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: strings.TrimSpace(c.Param("id")),
	})
}

// renderIntakeDetail pinta el detalle: el que le den ya cargado o, si no, el que relee de la API.
// `allowedFromRejection` son los destinos que devolvió un 422 previo: se usan SOLO si el detalle no
// los trae.
func (h *IntakesHandler) renderIntakeDetail(c *gin.Context, r intakeDetailRender) {
	var entitlements entitlementsView
	if r.entitlements != nil {
		entitlements = *r.entitlements
	} else {
		entitlements = resolveEntitlements(c, h.auth, h.api)
	}

	data := gin.H{
		"Title":                 "Solicitud",
		"IntakeID":              r.id,
		"Notice":                r.notice,
		entitlementsDataKey:     entitlements,
		intakesNavDataKey:       entitlements.Has(intakesFeature),
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
		integrationsNavDataKey:  entitlements.Has(integrationsFeature),
	}

	if !entitlements.Has(intakesFeature) {
		render(h.cfg, c, r.status, "intake-detail.html", data)
		return
	}
	if r.id == "" {
		data["Notice"] = &intakeNotice{Message: "Falta el identificador de la solicitud."}
		render(h.cfg, c, http.StatusBadRequest, "intake-detail.html", data)
		return
	}

	detail := r.detail
	if detail == nil {
		err := h.auth.withAuthRetry(c, func(accessToken string) error {
			var gerr error
			detail, gerr = h.api.GetIntake(c.Request.Context(), accessToken, r.id)
			return gerr
		})
		if err != nil {
			if errors.Is(err, apiclient.ErrUnauthorized) {
				clearSessionCookie(h.cfg, c)
				h.auth.redirectToLogin(c)
				return
			}
			detailStatus, detailNotice := mapIntakeDetailError(err)
			// El aviso de la operación que falló manda sobre el de la relectura: el operador
			// necesita saber por qué no se guardó lo suyo, no que además no se pudo repintar.
			if r.notice == nil {
				data["Notice"] = detailNotice
			}
			render(h.cfg, c, detailStatus, "intake-detail.html", data)
			return
		}
	}

	view := transitionsOf(detail, r.allowedFromRejection)
	view.Edit = editFormOf(detail, view.Transitions, r)
	data["View"] = view
	render(h.cfg, c, r.status, "intake-detail.html", data)
}

// editFormOf arma el formulario de corrección: con lo que el operador tecleó cuando venimos de un
// rechazo, y con lo que dice la plataforma en cualquier otro caso.
func editFormOf(detail *apiclient.IntakeDetail, transitions []string, r intakeDetailRender) *intakeEditForm {
	form := editFormFromDetail(detail, transitions)
	if r.rows != nil {
		form = editFormFromRows(r.rows, detail, transitions, r.defects)
	}
	// Los estados editables que trae un 422 mandan sobre el espejo local por lo mismo que los
	// destinos del cambio de estado: misma fuente de verdad —el dominio de la plataforma—, solo
	// que llega tarde. Y que llegó tarde se dice, no se disimula.
	if len(r.editableIn) > 0 {
		form.EditableIn = labelsOf(r.editableIn)
		form.FromRejection = true
	}
	return form
}

// transitionsOf decide qué destinos ofrece el desplegable, por orden de fiabilidad:
//
//  1. los que publica el detalle (`allowed_transitions`) — la fuente buena: sale de la máquina de
//     estados de la plataforma en el momento de leer la solicitud;
//  2. si el detalle NO trae el campo, los que devolvió el 422 de un intento previo — misma autoridad,
//     solo que llega tarde;
//  3. si no hay ninguno de los dos, NINGUNO, y la pantalla lo dice.
//
// Lo que nunca ocurre es el paso 4 que sería cómodo: deducirlos de una tabla propia. El BFF no conoce
// el ciclo de vida y no debe fingir que sí.
func transitionsOf(detail *apiclient.IntakeDetail, allowedFromRejection []string) intakeDetailView {
	view := intakeDetailView{Detail: detail}
	switch {
	case detail.AllowedTransitions != nil:
		view.Transitions = *detail.AllowedTransitions
		view.Known = true
	case len(allowedFromRejection) > 0:
		view.Transitions = allowedFromRejection
		view.Known = true
		view.FromRejection = true
	}
	return view
}

// DoSetIntakeStatus aplica la transición pedida desde el formulario del detalle.
func (h *IntakesHandler) DoSetIntakeStatus(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	status := strings.TrimSpace(c.PostForm("status"))

	if id == "" || status == "" {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id,
			notice: &intakeNotice{Message: "Elige el estado al que quieres mover la solicitud."},
		})
		return
	}

	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		_, serr := h.api.SetIntakeStatus(c.Request.Context(), accessToken, id, status)
		return serr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		// El detalle se REcarga en todos los casos de fallo, y eso es deliberado: el 409 significa que
		// otro operador movió la solicitud, así que lo que el operador tiene delante ya es viejo y
		// re-pintarlo tal cual sería mentirle.
		httpStatus, notice, allowed := mapSetStatusError(err)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: httpStatus, id: id, notice: notice, allowedFromRejection: allowed,
		})
		return
	}

	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id,
		notice: &intakeNotice{
			Success: true,
			Message: "Solicitud movida a «" + intakeStatusLabel(status) + "».",
		},
	})
}

// mapIntakeListError traduce el fallo del listado a un aviso legible sin filtrar el detalle del
// upstream (mismo criterio que el resto de mappers del BFF).
func mapIntakeListError(err error) (int, *intakeNotice) {
	if msg, ok := apiclient.RejectionMessageOf(err); ok {
		notice := "La plataforma rechazó los filtros. Revisa las fechas (AAAA-MM-DD) y el estado."
		if msg != "" {
			notice = "La plataforma rechazó los filtros: " + msg
		}
		return http.StatusBadRequest, &intakeNotice{Message: notice}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede consultar las solicitudes de este tenant, o el plan ya no incluye la bandeja.",
		}
	}
	slog.Warn("no se pudieron listar las solicitudes", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudieron cargar las solicitudes ahora mismo. Inténtalo de nuevo.",
	}
}

// mapIntakeDetailError traduce el fallo al abrir una solicitud. El 404 cubre también la solicitud de
// OTRO tenant: la plataforma responde 404 a propósito para no confirmar que ese id existe, y el BFF
// no puede decir más de lo que sabe.
func mapIntakeDetailError(err error) (int, *intakeNotice) {
	switch apiclient.StatusCodeOf(err) {
	case http.StatusNotFound:
		return http.StatusNotFound, &intakeNotice{Message: "Esa solicitud no es tuya o no existe."}
	case http.StatusForbidden:
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede consultar las solicitudes de este tenant, o el plan ya no incluye la bandeja.",
		}
	}
	slog.Warn("no se pudo cargar la solicitud", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudo cargar la solicitud ahora mismo. Inténtalo de nuevo.",
	}
}

// mapSetStatusError traduce el fallo del cambio de estado y devuelve, cuando el rechazo los trae, los
// destinos válidos para que el desplegable pueda ofrecerlos en el re-render.
func mapSetStatusError(err error) (int, *intakeNotice, []string) {
	if invalid, ok := apiclient.InvalidTransitionOf(err); ok {
		msg := "Desde «" + intakeStatusLabel(invalid.Status) + "» no se puede pasar a «" +
			intakeStatusLabel(invalid.Requested) + "». "
		if len(invalid.Allowed) == 0 {
			msg += "Esta solicitud está en un estado final y ya no admite cambios."
		} else {
			msg += "Destinos posibles: " + strings.Join(labelsOf(invalid.Allowed), ", ") + "."
		}
		return http.StatusUnprocessableEntity, &intakeNotice{Message: msg}, invalid.Allowed
	}
	switch apiclient.StatusCodeOf(err) {
	case http.StatusConflict:
		return http.StatusConflict, &intakeNotice{
			Message: "Otro operador cambió esta solicitud mientras la mirabas. Aquí tienes el estado actual; " +
				"revísalo y vuelve a intentarlo si sigue haciendo falta.",
		}, nil
	case http.StatusNotFound:
		return http.StatusNotFound, &intakeNotice{Message: "Esa solicitud no es tuya o no existe."}, nil
	case http.StatusForbidden:
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede cambiar el estado de las solicitudes de este tenant.",
		}, nil
	case http.StatusBadRequest:
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma no reconoció ese estado. Elige uno del desplegable.",
		}, nil
	}
	slog.Warn("no se pudo cambiar el estado de la solicitud", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudo cambiar el estado ahora mismo. Inténtalo de nuevo.",
	}, nil
}

// intakeFilterFromQuery lee filtros y paginación de la query string. No valida los valores: quien
// manda sobre qué es una fecha o un estado válido es la API, y duplicar aquí esa validación
// significaría mantener dos criterios que acabarían discrepando. El único saneo es el de la
// paginación, que evita pedir una página imposible.
func intakeFilterFromQuery(c *gin.Context) apiclient.IntakeFilter {
	page, err := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(strings.TrimSpace(c.Query("page_size")))
	if err != nil || size < 1 {
		size = defaultIntakesPageSize
	}
	if size > maxIntakesPageSize {
		size = maxIntakesPageSize
	}
	return apiclient.IntakeFilter{
		From:     strings.TrimSpace(c.Query("from")),
		To:       strings.TrimSpace(c.Query("to")),
		Status:   strings.TrimSpace(c.Query("status")),
		Session:  strings.TrimSpace(c.Query("session")),
		Page:     page,
		PageSize: size,
	}
}

func filterView(f apiclient.IntakeFilter) intakesFilterView {
	return intakesFilterView{From: f.From, To: f.To, Status: f.Status, Session: f.Session}
}

// pagerView resuelve el paginador a partir de lo que respondió la API. El total y el tamaño los fija
// el servidor (puede acotar el page_size pedido), así que las cuentas salen de la respuesta, no de lo
// que se pidió.
func pagerView(f apiclient.IntakeFilter, page *apiclient.IntakePage) intakesPageView {
	size := page.PageSize
	if size <= 0 {
		size = defaultIntakesPageSize
	}
	current := page.Page
	if current <= 0 {
		current = 1
	}

	count := page.Total / size
	if page.Total%size != 0 {
		count++
	}

	view := intakesPageView{
		Page: current, PageSize: size, Total: page.Total, PageCount: count,
		HasPrev: current > 1,
		HasNext: current*size < page.Total,
	}
	if len(page.Intakes) > 0 {
		view.RangeFrom = (current-1)*size + 1
		view.RangeTo = view.RangeFrom + len(page.Intakes) - 1
	}
	if view.HasPrev {
		view.PrevURL = intakesURL(f, current-1)
	}
	if view.HasNext {
		view.NextURL = intakesURL(f, current+1)
	}
	return view
}

// intakesURL arma el enlace a una página conservando los filtros vigentes.
func intakesURL(f apiclient.IntakeFilter, page int) string {
	return intakeFilteredURL("/intakes", f, page)
}

// discardURL es la ruta a la que apuntan los dos formularios del descarte, con los filtros vigentes
// EN LA QUERY.
//
// Van en la URL y no en campos ocultos a propósito: así el POST se lee con el mismo
// intakeFilterFromQuery que el GET —una sola forma de saber qué bandeja se está mirando— y el
// re-pintado tras descartar cae exactamente en la página desde la que se descartó.
func discardURL(f apiclient.IntakeFilter) string {
	page := f.Page
	if page < 1 {
		page = 1
	}
	return intakeFilteredURL("/intakes/discard", f, page)
}

// intakeFilteredURL arma una URL de la bandeja conservando los filtros vigentes. Es el ÚNICO sitio
// donde se decide qué filtros sobreviven a una navegación: si el paginador y el descarte contaran
// cada uno por su cuenta, descartar podría devolver al operador a una bandeja distinta de la que
// estaba mirando.
func intakeFilteredURL(path string, f apiclient.IntakeFilter, page int) string {
	q := url.Values{}
	for key, value := range map[string]string{
		"from": f.From, "to": f.To, "status": f.Status, "session": f.Session,
	} {
		if value != "" {
			q.Set(key, value)
		}
	}
	q.Set("page", strconv.Itoa(page))
	if f.PageSize > 0 && f.PageSize != defaultIntakesPageSize {
		q.Set("page_size", strconv.Itoa(f.PageSize))
	}
	return path + "?" + q.Encode()
}
