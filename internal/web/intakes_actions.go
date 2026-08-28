package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// Nombres de los campos de los formularios de acción. Van como constantes por lo mismo que los del
// formulario de líneas: los lee el handler y los escribe la plantilla, y un desajuste entre los dos
// no lo detecta el compilador.
const (
	intakeActionFieldRenderedText = "rendered_text"
	intakeActionFieldQuestion     = "question"
)

// intakeActionsView son las TRES acciones del dueño (Plan 044 · T4.2/T4.3/T4.4) tal como las pinta
// la plantilla: aprobar y responder, corregir, y pedir más información.
//
// Es una vista PROPIA y no una fila más del bloque de estado a propósito, y el criterio de la tarea
// lo exige: estas tres LE HABLAN AL CLIENTE por WhatsApp y dejan revisión; el desplegable del 041
// solo mueve la etiqueta del ciclo de vida y no le escribe a nadie. Confundirlos sería ofrecer
// «responderle al cliente» donde no se responde.
type intakeActionsView struct {
	// RenderedText es la cotización PROPUESTA, que el dueño edita antes de mandarla. Es una
	// propuesta y nada más: lo que la revisión guarda es byte a byte lo que se envió, y el autor de
	// ese texto es el dueño (D-044.19).
	RenderedText string
	// Question es la pregunta propuesta, tomada de las que preparó el LLM. Nunca sale sola (INV-1):
	// esto es lo que aparece en el formulario, no lo que se manda.
	Question string
	// Questions son todas las preparadas, para que el dueño pueda copiar otra.
	Questions []string
	// QuestionsKnown es falso cuando la plataforma NO publicó la clave, o sea cuando el plan del
	// tenant no incluye `llm_intake`. No es lo mismo que «no había nada que preguntar», y la
	// pantalla lo dice distinto.
	QuestionsKnown bool
	// PendingPrice es cuántas líneas siguen sin precio. Con una sola, la aprobación va a salir
	// rechazada, y decirlo ANTES ahorra el viaje y explica por qué.
	PendingPrice int
	// HasDraft es si hay un borrador que corregir. Sin él NO se ofrece el botón «Corregir»: manda
	// el formulario del borrador, y un botón que apunta a un formulario que no está en la página no
	// hace nada — que es peor que no ofrecerlo, porque parece que sí.
	HasDraft bool
	// Quote es la CUARTA acción (Plan 047 · T2.4), y es de otra naturaleza que las tres de arriba:
	// no le habla al cliente, no escribe en la solicitud y no la mueve de estado. Solo redacta una
	// propuesta y la deja en el campo de aquí al lado. Vive en esta vista —y no en una tarjeta
	// propia— porque su resultado ES este formulario: separarla dejaría un botón lejos del campo
	// que precarga.
	Quote intakeQuoteView
}

// actionsViewOf arma las tres acciones. Devuelve nil cuando el estado no las admite: un botón que
// la plataforma va a rechazar no se ofrece (misma regla que el desplegable de un estado terminal y
// que el formulario de líneas del 041).
func actionsViewOf(detail *apiclient.IntakeDetail, draft *intakeDraftView, ent entitlementsView,
	r intakeDetailRender) *intakeActionsView {
	if detail.Status != intakeEditableStatus {
		return nil
	}
	view := &intakeActionsView{
		RenderedText: proposedQuoteText(detail, draft),
		HasDraft:     draft != nil,
		Quote:        quoteViewOf(ent, r.quote),
	}
	if draft != nil {
		view.Questions = draft.Questions
		view.QuestionsKnown = draft.QuestionsKnown
		view.PendingPrice = draft.PendingPrice
		if len(draft.Questions) > 0 {
			view.Question = draft.Questions[0]
		}
	}
	// Lo que el dueño tecleó manda sobre la propuesta: al repintar tras un rechazo su trabajo no se
	// tira, que es la misma regla del formulario de líneas.
	if r.approveText != "" {
		view.RenderedText = r.approveText
	}
	if r.question != "" {
		view.Question = r.question
	}
	return view
}

// proposedQuoteText redacta la cotización que se le propone al dueño.
//
// Si la revisión ya trae un texto compuesto, ése; si no, se arma con las líneas del borrador. Y se
// arma SIN hacer una sola cuenta: los precios se copian tal cual y el total es el que manda la
// plataforma (INV-13). Multiplicar aquí crearía una segunda autoridad sobre el dinero, y el día que
// las dos discreparan el cliente tendría delante la de esta pantalla.
func proposedQuoteText(detail *apiclient.IntakeDetail, draft *intakeDraftView) string {
	if draft == nil {
		return ""
	}
	if rev := detail.LastRevisionOf(apiclient.RevisionKindInterpreted); rev != nil {
		if text := strings.TrimSpace(rev.RenderedText); text != "" {
			return text
		}
	}

	var b strings.Builder
	b.WriteString("Tu pedido:\n")
	for _, line := range draft.Lines {
		b.WriteString("- " + line.Qty + " × " + line.Label)
		if line.Size != "" {
			b.WriteString(" (" + line.Size + ")")
		}
		if line.Customization != "" {
			b.WriteString(" · " + line.Customization)
		}
		if line.HasPrice {
			b.WriteString(" — " + line.UnitPrice + " c/u")
		} else {
			b.WriteString(" — pendiente de precio")
		}
		b.WriteString("\n")
	}
	for _, line := range draft.Shipping {
		b.WriteString("- " + line.Label)
		if line.HasPrice {
			b.WriteString(" — " + line.UnitPrice)
		} else if line.Note != "" {
			b.WriteString(" — " + line.Note)
		}
		b.WriteString("\n")
	}
	if draft.DeliveryDate != "" {
		b.WriteString("Entrega: " + draft.DeliveryDate + "\n")
	}
	if detail.CustomerNote != "" {
		b.WriteString("Indicación: " + detail.CustomerNote + "\n")
	}
	b.WriteString("Total: " + strconv.FormatFloat(detail.Total, 'f', 2, 64))
	return b.String()
}

// DoApproveIntake aprueba la solicitud y le responde al cliente con el texto que dejó escrito el
// dueño (Plan 044 · T4.3).
//
// 🔴 El éxito de esta puerta significa «se aplicó y quedó registrado», NUNCA «el cliente lo
// recibió»: el envío cuelga de una sesión de WhatsApp que puede estar caída. El aviso de la
// pantalla dice exactamente eso y no debe prometer la entrega.
func (h *IntakesHandler) DoApproveIntake(c *gin.Context) {
	id, entitlements, ok := h.actionPreflight(c)
	if !ok {
		return
	}

	text := strings.TrimSpace(c.PostForm(intakeActionFieldRenderedText))
	if text == "" {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id, entitlements: &entitlements,
			notice: &intakeNotice{Message: "Escribe la respuesta que quieres enviarle al cliente: " +
				"esta consola no manda una cotización que no hayas leído."},
		})
		return
	}

	var detail *apiclient.IntakeDetail
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var aerr error
		detail, aerr = h.api.ApproveIntake(c.Request.Context(), accessToken, id, text)
		return aerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapApproveError(err)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: status, id: id, entitlements: &entitlements, notice: notice, approveText: text,
		})
		return
	}

	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id, entitlements: &entitlements, detail: detail,
		notice: &intakeNotice{
			Success: true,
			Message: "Aprobada: la plataforma registró la respuesta y la envió por la sesión de esta " +
				"solicitud. Que quede registrada no garantiza que el cliente ya la tenga delante.",
		},
	})
}

// DoRequestIntakeInfo manda al cliente la pregunta del dueño y deja la solicitud esperando su
// respuesta (Plan 044 · T4.4).
//
// La pregunta va SIEMPRE editada por una persona: las que prepara el LLM son una propuesta del
// formulario y jamás salen solas (INV-1).
func (h *IntakesHandler) DoRequestIntakeInfo(c *gin.Context) {
	id, entitlements, ok := h.actionPreflight(c)
	if !ok {
		return
	}

	question := strings.TrimSpace(c.PostForm(intakeActionFieldQuestion))
	if question == "" {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id, entitlements: &entitlements,
			notice: &intakeNotice{Message: "Escribe la pregunta que quieres hacerle al cliente: " +
				"las que propone el sistema no se envían solas."},
		})
		return
	}

	var detail *apiclient.IntakeDetail
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var rerr error
		detail, rerr = h.api.RequestIntakeInfo(c.Request.Context(), accessToken, id, question)
		return rerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapRequestInfoError(err)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: status, id: id, entitlements: &entitlements, notice: notice, question: question,
		})
		return
	}

	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id, entitlements: &entitlements, detail: detail,
		notice: &intakeNotice{
			Success: true,
			Message: "Pregunta registrada y enviada por la sesión de esta solicitud. Que quede " +
				"registrada no garantiza que el cliente ya la tenga delante; cuando conteste, su " +
				"respuesta vuelve a esta misma solicitud.",
		},
	})
}

// actionPreflight resuelve lo que las tres acciones comprueban antes de gastar un viaje: la
// capacidad del plan y el identificador. Devuelve ok=false cuando ya respondió.
//
// Sin la capacidad no se llama a la API —la plataforma cortaría con 403 igualmente y el viaje solo
// llenaría el log de rechazos previsibles—, y el gate real de lo que se emite está en la plantilla.
func (h *IntakesHandler) actionPreflight(c *gin.Context) (string, entitlementsView, bool) {
	id := strings.TrimSpace(c.Param("id"))
	entitlements := resolveEntitlements(c, h.auth, h.api)

	if !entitlements.Has(intakesFeature) {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusForbidden, id: id, entitlements: &entitlements,
		})
		return "", entitlements, false
	}
	if id == "" {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, entitlements: &entitlements,
			notice: &intakeNotice{Message: "Falta el identificador de la solicitud."},
		})
		return "", entitlements, false
	}
	return id, entitlements, true
}

// mapApproveError traduce el fallo de la aprobación a un aviso legible sin filtrar el detalle del
// upstream (mismo criterio que el resto de mappers del BFF).
func mapApproveError(err error) (int, *intakeNotice) {
	if missing, ok := apiclient.LinesWithoutPriceOf(err); ok {
		return http.StatusBadRequest, &intakeNotice{
			Message: "No se envió nada: " + linesWithoutPriceText(missing.Lines) +
				" Ponles precio en el borrador y guarda la corrección antes de aprobar.",
		}
	}
	if notApprovable, ok := apiclient.NotApprovableOf(err); ok {
		msg := "Esta solicitud está en «" + intakeStatusLabel(notApprovable.Status) + "» y así no se aprueba. "
		if len(notApprovable.ApprovableIn) == 0 {
			msg += "La plataforma no dice desde qué estado se aprueba."
		} else {
			msg += "Se aprueba desde «" + strings.Join(labelsOf(notApprovable.ApprovableIn), "» o «") + "»."
		}
		return http.StatusUnprocessableEntity, &intakeNotice{Message: msg}
	}
	if _, ok := apiclient.InvalidTransitionOf(err); ok {
		return http.StatusUnprocessableEntity, &intakeNotice{
			Message: "Esta solicitud ya no está donde estaba cuando abriste la página, así que no se " +
				"envió nada. Aquí tienes el estado actual; revísalo y vuelve a intentarlo si sigue " +
				"haciendo falta.",
		}
	}
	if rej, ok := apiclient.RejectionOf(err); ok && rej.Message != "" {
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la aprobación: " + rej.Message,
		}
	}
	return mapIntakeActionStatusError(err, "aprobar")
}

// mapRequestInfoError traduce el fallo de «pedir más información». El 422 de esta puerta llega SIN
// estados permitidos —la plataforma no publica ese cuerpo aquí—, así que el aviso no promete un
// camino: dice lo que pasó y enseña el estado actual.
func mapRequestInfoError(err error) (int, *intakeNotice) {
	if _, ok := apiclient.InvalidTransitionOf(err); ok {
		return http.StatusUnprocessableEntity, &intakeNotice{
			Message: "Esta solicitud ya no admite pedir más información desde el estado en el que " +
				"está, así que no se envió nada. Aquí tienes el estado actual.",
		}
	}
	if rej, ok := apiclient.RejectionOf(err); ok && rej.Message != "" {
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la pregunta: " + rej.Message,
		}
	}
	return mapIntakeActionStatusError(err, "pedir más información sobre")
}

// mapIntakeActionStatusError cubre los rechazos que las dos acciones comparten y que solo dice el
// código. `what` es el verbo con el que se redacta el 403, para que el aviso hable de lo que el
// operador acaba de intentar.
func mapIntakeActionStatusError(err error, what string) (int, *intakeNotice) {
	switch apiclient.StatusCodeOf(err) {
	case http.StatusConflict:
		return http.StatusConflict, &intakeNotice{
			Message: "Otro operador cambió esta solicitud mientras la mirabas, así que no se envió nada. " +
				"Aquí tienes el estado actual; revísalo y vuelve a intentarlo si sigue haciendo falta.",
		}
	case http.StatusNotFound:
		return http.StatusNotFound, &intakeNotice{Message: "Esa solicitud no es tuya o no existe."}
	case http.StatusForbidden:
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede " + what + " las solicitudes de este tenant, o el plan ya no " +
				"incluye la bandeja.",
		}
	case http.StatusBadRequest:
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma lo rechazó y no dijo el motivo. Revisa el texto e inténtalo de nuevo.",
		}
	}
	slog.Warn("no se pudo ejecutar la acción sobre la solicitud", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudo completar la acción ahora mismo. Vuelve a intentarlo; nada se ha enviado.",
	}
}

// linesWithoutPriceText redacta las líneas que se quedaron sin precio.
//
// Se nombran por POSICIÓN y etiqueta, y no por sku, porque la que suele faltar es precisamente la
// que no tiene sku (`unmatched`). Aquí se suma 1 para que el número coincida con el que el dueño
// tiene delante en el formulario.
//
// 🔑 Que `index` sea 0-based NO es una suposición por analogía con `ItemDefect.Index`: está
// VERIFICADO en la plataforma el 2026-08-27 —`cloud/wapp-cloud-platform/internal/intakes/
// approve.go:279` construye `PendingPriceLine{Index: i, …}` con la `i` del bucle sobre las líneas, y
// `approve_test.go:437` espera `Index: 0` para la primera—. Se deja escrito con fichero y línea
// porque es una asunción sobre un contrato AJENO: el día que ese `+1` parezca un fallo de dedo, aquí
// está de dónde sale.
func linesWithoutPriceText(lines []apiclient.IntakeLineRef) string {
	if len(lines) == 0 {
		return "la plataforma dice que quedan líneas sin precio, pero no dice cuáles."
	}
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		part := "línea " + strconv.Itoa(line.Index+1)
		if label := strings.TrimSpace(line.Label); label != "" {
			part += " («" + label + "»)"
		}
		parts = append(parts, part)
	}
	if len(lines) == 1 {
		return "queda 1 línea sin precio: " + parts[0] + "."
	}
	return "quedan " + strconv.Itoa(len(lines)) + " líneas sin precio: " + strings.Join(parts, ", ") + "."
}
