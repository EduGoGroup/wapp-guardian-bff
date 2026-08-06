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

// ShowIntakes pinta la bandeja del tenant con los filtros de la query.
func (h *IntakesHandler) ShowIntakes(c *gin.Context) {
	entitlements := resolveEntitlements(c, h.auth, h.api)
	filter := intakeFilterFromQuery(c)

	data := gin.H{
		"Title":                 "Solicitudes",
		"Filter":                filterView(filter),
		"StatusOptions":         intakeStatusOptions,
		entitlementsDataKey:     entitlements,
		intakesNavDataKey:       entitlements.Has(intakesFeature),
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
	}

	// Sin la capacidad no se llama a la API: la plataforma cortaría con 403 igualmente, y gastar el
	// viaje solo serviría para llenar el log de rechazos previsibles. El gate real de lo que se emite
	// está en la plantilla.
	if !entitlements.Has(intakesFeature) {
		render(h.cfg, c, http.StatusOK, "intakes.html", data)
		return
	}

	var page *apiclient.IntakePage
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		page, lerr = h.api.ListIntakes(c.Request.Context(), accessToken, filter)
		return lerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		listStatus, listNotice := mapIntakeListError(err)
		data["Notice"] = listNotice
		data["IntakesError"] = true
		render(h.cfg, c, listStatus, "intakes.html", data)
		return
	}

	data["Intakes"] = page.Intakes
	data["Pager"] = pagerView(filter, page)
	render(h.cfg, c, http.StatusOK, "intakes.html", data)
}

// ShowIntakeDetail pinta una solicitud con sus líneas y el cambio de estado.
func (h *IntakesHandler) ShowIntakeDetail(c *gin.Context) {
	h.renderIntakeDetail(c, http.StatusOK, strings.TrimSpace(c.Param("id")), nil, nil)
}

// renderIntakeDetail recarga el detalle desde la API y lo pinta. `allowedFromRejection` son los
// destinos que devolvió un 422 previo: se usan SOLO si el detalle no los trae.
func (h *IntakesHandler) renderIntakeDetail(c *gin.Context, status int, id string, notice *intakeNotice, allowedFromRejection []string) {
	entitlements := resolveEntitlements(c, h.auth, h.api)

	data := gin.H{
		"Title":                 "Solicitud",
		"IntakeID":              id,
		"Notice":                notice,
		entitlementsDataKey:     entitlements,
		intakesNavDataKey:       entitlements.Has(intakesFeature),
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
	}

	if !entitlements.Has(intakesFeature) {
		render(h.cfg, c, status, "intake-detail.html", data)
		return
	}
	if id == "" {
		data["Notice"] = &intakeNotice{Message: "Falta el identificador de la solicitud."}
		render(h.cfg, c, http.StatusBadRequest, "intake-detail.html", data)
		return
	}

	var detail *apiclient.IntakeDetail
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		detail, gerr = h.api.GetIntake(c.Request.Context(), accessToken, id)
		return gerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		detailStatus, detailNotice := mapIntakeDetailError(err)
		data["Notice"] = detailNotice
		render(h.cfg, c, detailStatus, "intake-detail.html", data)
		return
	}

	data["View"] = transitionsOf(detail, allowedFromRejection)
	render(h.cfg, c, status, "intake-detail.html", data)
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
		h.renderIntakeDetail(c, http.StatusBadRequest, id,
			&intakeNotice{Message: "Elige el estado al que quieres mover la solicitud."}, nil)
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
		h.renderIntakeDetail(c, httpStatus, id, notice, allowed)
		return
	}

	h.renderIntakeDetail(c, http.StatusOK, id, &intakeNotice{
		Success: true,
		Message: "Solicitud movida a «" + intakeStatusLabel(status) + "».",
	}, nil)
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
	return "/intakes?" + q.Encode()
}
