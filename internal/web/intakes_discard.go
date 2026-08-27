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

// Nombres de los campos del formulario de descarte. Van como constantes porque los lee el handler y
// los escribe la plantilla, y un desajuste entre los dos no lo detecta el compilador.
const (
	intakeDiscardFieldID     = "intake_id"
	intakeDiscardFieldAction = "action"
	// intakeDiscardFieldVisibleID lleva, en un oculto por fila, los ids de la PÁGINA que se está
	// mirando (Plan 044 · T4.8). Es la materia prima de «seleccionar todo lo visible», y viaja por
	// el formulario en vez de recalcularse en el servidor a propósito: lo que el dueño va a
	// descartar es lo que TENÍA DELANTE, no lo que la bandeja devolvería si se volviera a leer un
	// segundo después. Mismo criterio que el re-envío del lote en el paso de confirmar.
	intakeDiscardFieldVisibleID = "visible_intake_id"
	// intakeDiscardFieldSelectVisible es la casilla MAESTRA de la cabecera de la tabla. Marcada
	// significa «toda esta página», nunca «todo lo que cumpla el filtro»: son 20 solicitudes o son
	// 4.000, y esto no tiene vuelta atrás (D-041.22).
	intakeDiscardFieldSelectVisible = "select_visible"
	// intakeDiscardConfirm es el valor del botón que ESCRIBE. Cualquier otro valor —incluido el
	// ausente— es el paso de mirar: el descarte no puede caerse del lado de escribir por un campo
	// que llegó vacío.
	intakeDiscardConfirm = "discard"
)

// intakeDiscardReasons traduce las razones del contrato (`intakes.DiscardSkip*`) a la voz del dueño
// del negocio. Es un diccionario de PRESENTACIÓN, como intakeStatusLabels: aquí no se decide nada,
// solo se dice en español lo que decidió la plataforma.
//
// `live_event` es la que más se aleja de su clave, y a propósito: en el contrato significa «hay una
// conversación viva detrás de esa solicitud» —hoy, un carrito abierto de ese contacto en esa sesión—
// y a quien lee la pantalla eso le llega como «el cliente está a media compra». Decirle
// «live_event», o «evento vivo», sería contarle la implementación en vez del hecho.
//
// Una razón que no esté en el diccionario se pinta TAL CUAL (ver intakeDiscardReason): es preferible
// que el dueño lea una clave rara a que la pantalla se calle que esa solicitud no se descartó.
var intakeDiscardReasons = map[string]string{
	"not_found": "No está en tu bandeja: o no existe o ya no es de este negocio.",
	"already_discarded": "Ya estaba descartada. No se ha vuelto a tocar y no se ha duplicado nada " +
		"de lo que quedó registrado.",
	"not_open": "Ya no está abierta, y desde donde está no se descarta. Si estaba confirmada, lo " +
		"que corresponde es cancelarla desde su ficha.",
	"live_event": "El cliente sigue en plena conversación con este pedido: descartarlo ahora se lo " +
		"cortaría a medias. Atiéndelo o espera a que termine, y descártalo después.",
}

// intakeDiscardReason redacta el motivo por el que una solicitud del lote no se descartó.
func intakeDiscardReason(reason string) string {
	if text, ok := intakeDiscardReasons[reason]; ok {
		return text
	}
	return reason
}

// intakeDiscardRow es UNA solicitud del lote tal como se enseña ANTES de descartarla. Los valores
// llegan ya redactados —el estado con su nombre de negocio, el total con sus dos decimales— porque
// la plantilla no calcula nada.
type intakeDiscardRow struct {
	ID        string
	ContactID string
	SessionID string
	Status    string
	Total     string
	// Listed dice si la solicitud se pudo describir con la bandeja que se está mirando. Con `false`
	// solo se conoce el id —la fila cambió de página o de estado entre la selección y el envío—, y
	// la pantalla lo dice en vez de pintar una fila con celdas vacías que parecerían datos.
	Listed bool
}

// intakeDiscardSkipRow es una solicitud que NO se descartó, con el motivo ya en español.
type intakeDiscardSkipRow struct {
	ID     string
	Reason string
}

// intakeDiscardView es el descarte por lotes tal como lo pinta la bandeja: o el lote que espera
// confirmación, o el desglose del que ya se ejecutó. Nunca los dos a la vez.
type intakeDiscardView struct {
	// Confirming dice que hay un lote esperando el «Descartar definitivamente». Mientras sea true
	// NO se ha llamado a la API: mirar no escribe.
	Confirming bool
	// Selected son las solicitudes del lote, en el orden en que se marcaron.
	Selected []intakeDiscardRow
	// Action es la URL del formulario que confirma (arrastra los filtros vigentes) y CancelURL la
	// vuelta a la bandeja tal como se estaba mirando.
	Action    string
	CancelURL string
	// Done dice que el lote se ejecutó. No significa que se descartara nada: para eso están las dos
	// listas de abajo, que es justo lo que un «listo» global escondería.
	Done bool
	// Discarded son los ids que la plataforma descartó, y Skipped los que no, con su motivo.
	Discarded []string
	Skipped   []intakeDiscardSkipRow

	// ids es el lote en crudo mientras espera confirmación. No se exporta porque la plantilla pinta
	// Selected: lo rellena describeWith cuando la bandeja ya está leída.
	ids []string
}

// Total es cuántas solicitudes ejecutó el lote. Lo pregunta la plantilla para encabezar el desglose
// sin sumar en el HTML.
func (v *intakeDiscardView) Total() int { return len(v.Discarded) + len(v.Skipped) }

// describeWith completa las filas del lote pendiente con lo que dice la bandeja recién leída.
//
// Lo que NO hace es pedir cada solicitud a la API para describirla: son hasta 200 y el operador está
// mirando esa misma página. Un id que no esté en ella se conserva en el lote —es su selección, y
// quien decide si existe es la plataforma— pero se marca como no descrito.
func (v *intakeDiscardView) describeWith(listed []apiclient.Intake) {
	if v == nil || len(v.ids) == 0 {
		return
	}
	byID := make(map[string]apiclient.Intake, len(listed))
	for _, in := range listed {
		byID[in.ID] = in
	}

	rows := make([]intakeDiscardRow, 0, len(v.ids))
	for _, id := range v.ids {
		row := intakeDiscardRow{ID: id}
		if in, ok := byID[id]; ok {
			row.Listed = true
			row.ContactID = in.ContactID
			row.SessionID = in.SessionID
			row.Status = intakeStatusLabel(in.Status)
			row.Total = strconv.FormatFloat(in.Total, 'f', 2, 64)
		}
		rows = append(rows, row)
	}
	v.Selected = rows
}

// DoDiscardIntakes atiende los DOS pasos del descarte por lotes desde la bandeja: revisar (el
// normal) y descartar (el que escribe). Cuál se pide lo dice el botón pulsado, y el que escribe SOLO
// existe en el formulario de confirmación, o sea después de haber enseñado qué se va a descartar.
//
// Esa separación no es cortesía: el descarte es IRREVERSIBLE y no hay papelera (D-041.22), así que
// el dueño tiene que ver qué va a matar antes de matarlo. La confirmación es SERVER-SIDE —una
// pantalla más, con su token CSRF y su lista— y no un `confirm()` del navegador: esta consola no
// emite JS (ADR-0035) y un diálogo del navegador no puede enseñar los estados ni los totales de lo
// seleccionado, que es precisamente lo que hace informada a la decisión.
//
// El lote se re-envía entero en el paso 2 en vez de guardarse entre pasos, igual que el documento
// del import de catálogo (T3.5): así el BFF no tiene estado y lo que se descarta es exactamente lo
// que se confirmó, aunque el operador tenga dos pestañas abiertas.
func (h *IntakesHandler) DoDiscardIntakes(c *gin.Context) {
	entitlements := resolveEntitlements(c, h.auth, h.api)
	filter := intakeFilterFromQuery(c)

	// Sin la capacidad no se llama a la API, igual que en el listado y el detalle: la plataforma
	// cortaría con 403 y el viaje solo serviría para llenar el log de rechazos previsibles.
	if !entitlements.Has(intakesFeature) {
		h.renderIntakesList(c, intakesListRender{
			status: http.StatusForbidden, filter: filter, entitlements: &entitlements,
		})
		return
	}

	ids := selectedIntakeIDs(c)
	switch {
	case len(ids) == 0:
		h.renderIntakesList(c, intakesListRender{
			status: http.StatusBadRequest, filter: filter, entitlements: &entitlements,
			notice: &intakeNotice{Message: "Marca al menos una solicitud antes de descartar. " +
				"No se ha tocado nada."},
		})
		return
	case len(ids) > apiclient.MaxIntakeDiscardBatch:
		// La plataforma responde 400 a un lote así y no descarta ninguna. Aquí se corta antes y se
		// explica en español, que es lo que ese 400 no trae: el operador necesita saber que su
		// selección sigue viva y que la salida es partirla en dos tandas.
		h.renderIntakesList(c, intakesListRender{
			status: http.StatusBadRequest, filter: filter, entitlements: &entitlements,
			notice: &intakeNotice{Message: discardTooManyMessage(len(ids))},
		})
		return
	}

	if strings.TrimSpace(c.PostForm(intakeDiscardFieldAction)) != intakeDiscardConfirm {
		h.renderIntakesList(c, intakesListRender{
			status: http.StatusOK, filter: filter, entitlements: &entitlements,
			discard: &intakeDiscardView{
				Confirming: true, ids: ids,
				Action: discardURL(filter), CancelURL: intakesURL(filter, filter.Page),
			},
		})
		return
	}

	var result *apiclient.IntakeDiscardResult
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var derr error
		result, derr = h.api.DiscardIntakes(c.Request.Context(), accessToken, ids)
		return derr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapDiscardError(err)
		h.renderIntakesList(c, intakesListRender{
			status: status, filter: filter, entitlements: &entitlements, notice: notice,
		})
		return
	}

	h.renderIntakesList(c, intakesListRender{
		status: http.StatusOK, filter: filter, entitlements: &entitlements,
		discard: discardResultView(result), notice: discardResultNotice(result),
	})
}

// selectedIntakeIDs lee las solicitudes marcadas. Colapsa los repetidos conservando el ORDEN de
// llegada, que es el de la bandeja que el operador tiene delante.
//
// El colapso ocurre ANTES de medir el lote, y esa es la diferencia con la plataforma —que mide el
// cuerpo tal como llega—: lo que se mide aquí es lo que se va a mandar, así que las dos cuentas dan
// lo mismo y no hay forma de que el BFF acepte un lote que el otro lado rechace por tamaño.
//
// 🔴 CON LA MAESTRA MARCADA la selección son los ids de la PÁGINA (T4.8), y de ahí sale el límite
// de esta pantalla: la lista llega del formulario, o sea de las filas que se PINTARON, y el
// servidor no tiene forma de ampliarla ni aunque quisiera. «Todo lo visible» no puede convertirse
// en «todo lo que cumple el filtro» por un descuido, porque el conjunto ancho no está aquí.
//
// La maestra GANA sobre las casillas sueltas en vez de sumarse a ellas, y es lo mismo: los ocultos
// de la página son un superconjunto de lo que se pueda haber marcado a mano en ella. Desmarcar una
// fila y dejar la maestra puesta selecciona la página entera — que es lo que la maestra dice, y lo
// que la pantalla de confirmación enseña antes de escribir nada.
func selectedIntakeIDs(c *gin.Context) []string {
	campo := intakeDiscardFieldID
	if strings.TrimSpace(c.PostForm(intakeDiscardFieldSelectVisible)) != "" {
		campo = intakeDiscardFieldVisibleID
	}
	raw := c.PostFormArray(campo)
	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// discardResultView proyecta la respuesta de la plataforma a lo que pinta la pantalla, traduciendo
// cada razón. Las dos listas se conservan enteras: el desglose por ítem ES el resultado de esta
// operación, y resumirlo a «hecho» escondería que hay solicitudes que siguen ahí.
func discardResultView(res *apiclient.IntakeDiscardResult) *intakeDiscardView {
	skipped := make([]intakeDiscardSkipRow, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, intakeDiscardSkipRow{ID: s.IntakeID, Reason: intakeDiscardReason(s.Reason)})
	}
	return &intakeDiscardView{Done: true, Discarded: res.Discarded, Skipped: skipped}
}

// discardResultNotice encabeza el desglose diciendo cuántas cayeron de cuántas.
//
// Solo es un aviso de ÉXITO cuando no se saltó ninguna. Un lote mixto se anuncia como lo que es —un
// trabajo a medias— porque el verde de arriba es lo único que mucha gente lee: dárselo a un lote en
// el que tres solicitudes siguen en la bandeja sería enseñarle a no leer la tabla.
func discardResultNotice(res *apiclient.IntakeDiscardResult) *intakeNotice {
	done, skipped := len(res.Discarded), len(res.Skipped)
	switch {
	case done == 0 && skipped == 1:
		return &intakeNotice{Message: "No se descartó la solicitud que mandaste. Abajo está por qué."}
	case done == 0:
		return &intakeNotice{Message: "No se descartó ninguna de las " + intakeCount(skipped) +
			" que mandaste. Abajo está qué pasó con cada una."}
	case skipped == 0:
		return &intakeNotice{Success: true, Message: "Descartadas " + intakeCount(done) +
			". Salen de tu bandeja de pendientes y esto no se puede deshacer; lo que pidió el " +
			"cliente sigue guardado."}
	default:
		fell := "Se descartaron " + strconv.Itoa(done)
		if done == 1 {
			fell = "Se descartó 1"
		}
		rest := "Las otras " + strconv.Itoa(skipped) + " siguen"
		if skipped == 1 {
			rest = "La otra sigue"
		}
		return &intakeNotice{Message: fell + " de " + intakeCount(done+skipped) + ". " + rest +
			" en tu bandeja: abajo está por qué."}
	}
}

// discardTooManyMessage redacta el rechazo del lote demasiado grande.
func discardTooManyMessage(n int) string {
	return "Marcaste " + intakeCount(n) + " y de una vez se pueden descartar como mucho " +
		strconv.Itoa(apiclient.MaxIntakeDiscardBatch) + ". No se ha descartado ninguna: quita las " +
		"que sobren o hazlo en varias tandas."
}

// intakeCount escribe «1 solicitud» o «N solicitudes». Es una fea función de plural que existe para
// que ningún mensaje de esta pantalla acabe diciendo «1 solicitudes».
func intakeCount(n int) string {
	if n == 1 {
		return "1 solicitud"
	}
	return strconv.Itoa(n) + " solicitudes"
}

// mapDiscardError traduce el fallo del lote sin filtrar el detalle del upstream (mismo criterio que
// el resto de mappers del BFF).
//
// El texto del caso genérico es deliberadamente incómodo: cada solicitud del lote es su propia
// unidad de trabajo en la plataforma, así que un fallo a media faena deja escrito lo ya escrito.
// Prometer que «no se ha cambiado nada» sería mentir, y lo único honesto es mandar a mirar la
// bandeja — sabiendo que repetir el mismo lote es seguro.
func mapDiscardError(err error) (int, *intakeNotice) {
	if rej, ok := apiclient.RejectionOf(err); ok {
		msg := "La plataforma rechazó el descarte, así que no se tocó ninguna solicitud."
		if rej.Message != "" {
			msg = "La plataforma rechazó el descarte y no se tocó ninguna solicitud: " + rej.Message
		}
		return http.StatusBadRequest, &intakeNotice{Message: msg}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede descartar las solicitudes de este tenant, o el plan ya no " +
				"incluye la bandeja.",
		}
	}
	slog.Warn("no se pudo descartar el lote de solicitudes", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudo saber si el descarte llegó a hacerse. Mira la bandeja antes de repetirlo; " +
			"volver a mandar el mismo lote es seguro, porque lo que ya esté descartado se queda como está.",
	}
}
