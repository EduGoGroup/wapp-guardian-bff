package web

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// intakeEditBlankRows son las filas vacías que el formulario ofrece al final para añadir líneas
// nuevas. Sin JS no se pueden crear filas en el navegador, así que vienen servidas.
//
// Son DOS y no las tres de la pantalla de variables a propósito: una línea tiene cinco campos, no
// dos, y tres filas en blanco convierten el formulario en un muro que hay que saltar para llegar al
// botón de guardar. Con dos se cubre la escena real —añadir el extra que el catálogo no tenía— y
// tras guardar aparecen otras dos.
const intakeEditBlankRows = 2

// intakeEditableStatus es el estado desde el que la plataforma admite la edición manual
// (`intakes.EditableStatus`, hoy `pending_approval`).
//
// Es un ESPEJO, y tenerlo aquí incumple la regla que sostiene transitionsOf —el BFF no replica el
// ciclo de vida— por una razón concreta: el detalle publica `allowed_transitions` pero NO publica
// desde dónde se editan las líneas, así que hoy no hay ninguna otra forma de saberlo sin provocar
// un 422. Se asume el espejo en vez de emitir el formulario a ciegas porque la doctrina de esta
// pantalla ya está escrita y probada: un estado que no admite la acción no ofrece el botón
// (TestIntakeDetailAbandonedOffersNoAction, T4.6).
//
// Lo que lo hace soportable es que el espejo NO se usa para deducir el ciclo de vida: para saber
// si una solicitud puede LLEGAR a ser editable se mira `allowed_transitions`, que sí es de la
// plataforma. El riesgo que queda —que allá se añada otro estado editable y aquí no se ofrezca el
// formulario— está anotado como follow-up: lo cierra publicar `editable_in` en el detalle, igual
// que en su día se publicó `allowed_transitions`.
const intakeEditableStatus = "pending_approval"

// intakeSystemSKUPrefix es el prefijo de los skus que pone LA PLATAFORMA (hoy la línea de envío,
// `_shipping`). Las líneas que empiezan por él NO se ofrecen en el formulario y NO viajan en el
// cuerpo del PUT: son filas del sistema y esa puerta las rechaza.
//
// Es otro espejo (`intakes.ReservedSKUPrefix`), y aquí es de PRESENTACIÓN: decide qué filas se
// pintan como editables. La autoridad sigue siendo la plataforma, que rechaza el prefijo en la
// entrada con un defecto de línea y además tiene un índice único parcial detrás; si este espejo se
// quedara corto, lo peor que pasa es que el formulario ofrezca una fila que la API devuelve con su
// motivo, no que se cuele una línea del sistema duplicada.
const intakeSystemSKUPrefix = "_"

// Nombres de los campos del formulario de edición. Van como constantes porque los lee el handler y
// los escribe la plantilla, y un desajuste entre los dos no lo detecta el compilador.
const (
	intakeEditFieldSKU           = "item_sku"
	intakeEditFieldLabel         = "item_label"
	intakeEditFieldCustomization = "item_customization"
	intakeEditFieldQty           = "item_qty"
	intakeEditFieldPrice         = "item_price"
	intakeEditFieldRemove        = "remove"
)

// intakeEditFieldLabels traduce el campo que nombra un defecto —venga de aquí o de la plataforma—
// al rótulo que el formulario tiene delante. Un campo desconocido se devuelve TAL CUAL: es
// preferible que el operador lea `unit_price` a que la pantalla se calle un defecto que existe.
var intakeEditFieldLabels = map[string]string{
	"sku":           "SKU",
	"label":         "artículo",
	"customization": "personalización",
	"qty":           "cantidad",
	"unit_price":    "precio",
}

// intakeEditRow es UNA fila del formulario, con los valores como TEXTO.
//
// Son cadenas y no números a propósito: es lo que el operador tecleó, y hay que poder repintárselo
// tal cual cuando algo se rechaza. Convertir a float64 antes de tiempo obligaría a re-imprimir el
// número, y «8,50» volvería a la pantalla como «8.5» — la corrección de otro, encima de la suya.
type intakeEditRow struct {
	// Number es el número de línea que se ve en el formulario, 1-based. Se calcula en Go y no en
	// la plantilla para que sea el MISMO que el de los defectos (intakeEditDefect.Line): si cada
	// uno contara por su cuenta, un rechazo señalaría una fila y el operador miraría otra.
	//
	// Ojo: el valor con el que viaja «Quitar» NO es éste sino el índice 0-based, que es lo que el
	// handler entiende. Son dos números para dos públicos distintos.
	Number        int
	SKU           string
	Label         string
	Customization string
	Qty           string
	UnitPrice     string
}

// Blank responde si la fila está entera en blanco: una fila de alta que nadie rellenó. Se descarta
// sin ruido; una fila a medias, en cambio, es un error que hay que contar.
//
// Va exportada porque también la pregunta la plantilla, que solo ofrece «Quitar» en las filas que
// tienen algo que quitar.
func (r intakeEditRow) Blank() bool {
	return strings.TrimSpace(r.SKU) == "" && strings.TrimSpace(r.Label) == "" &&
		strings.TrimSpace(r.Customization) == "" && strings.TrimSpace(r.Qty) == "" &&
		strings.TrimSpace(r.UnitPrice) == ""
}

// intakeEditDefect es un problema de UNA fila ya redactado para la pantalla: con el número de fila
// que se ve en el formulario (1-based), el rótulo del campo y el motivo.
type intakeEditDefect struct {
	Line    int
	Field   string
	Message string
}

// intakeEditForm es el formulario de corrección tal como lo pinta la plantilla.
type intakeEditForm struct {
	// Rows son las filas editables (las de cliente) más las de alta en blanco.
	Rows []intakeEditRow
	// Defects son los problemas de la última tentativa (vacío = no hubo).
	Defects []intakeEditDefect
	// SystemLines es cuántas líneas puso la plataforma (hoy el envío). No se editan aquí y no
	// viajan en el cuerpo, así que la pantalla lo dice en vez de dejar creer que se perdieron.
	SystemLines int
	// Editable es si el estado actual admite la corrección: SOLO entonces se emite el formulario.
	// Un estado que no la admite no puede ofrecer un botón que la plataforma va a rechazar — es la
	// misma regla con la que un terminal no ofrece desplegable de transición (T4.6).
	Editable bool
	// Reachable es si la solicitud puede LLEGAR a un estado editable, y sale de los destinos que
	// publica la PLATAFORMA, no del espejo. Es lo que separa las dos negativas: a un `confirmed`
	// se le dice cómo llegar a corregir sus líneas, y a un terminal no se le promete un camino que
	// no existe.
	Reachable bool
	// EditableIn son los nombres de negocio de los estados desde los que sí se edita.
	EditableIn []string
	// FromRejection avisa de que EditableIn salió del 422 de un intento previo —misma fuente de
	// verdad que el ciclo de vida, solo que llega tarde— y no del espejo local.
	FromRejection bool
}

// EditableInText redacta los estados desde los que sí se corrige, para el aviso de la pantalla.
// Devuelve el texto ya montado —«por aprobar», o «X» o «Y» si algún día son varios— en vez de
// dejar que la plantilla indexe la lista: una lista vacía indexada revienta el render entero, y
// este aviso no vale una página en blanco.
func (f *intakeEditForm) EditableInText() string {
	if len(f.EditableIn) == 0 {
		return "el estado que la plataforma admita"
	}
	return "«" + strings.Join(f.EditableIn, "» o «") + "»"
}

// editFormFromDetail arma el formulario a partir de lo que dice la plataforma: una fila por línea
// de CLIENTE, más las de alta. Las líneas del sistema se cuentan pero no se ofrecen.
//
// `transitions` son los destinos ya resueltos por transitionsOf: con ellos se decide si a una
// solicitud que hoy no se puede corregir se le enseña el camino o no se le enseña nada.
func editFormFromDetail(detail *apiclient.IntakeDetail, transitions []string) *intakeEditForm {
	form := &intakeEditForm{Editable: detail.Status == intakeEditableStatus}
	if !form.Editable {
		form.Reachable = slices.Contains(transitions, intakeEditableStatus)
	}
	for _, item := range detail.Items {
		if strings.HasPrefix(item.SKU, intakeSystemSKUPrefix) {
			form.SystemLines++
			continue
		}
		form.Rows = append(form.Rows, intakeEditRow{
			SKU:           item.SKU,
			Label:         item.Label,
			Customization: item.Customization,
			Qty:           strconv.Itoa(item.Qty),
			// El precio se re-imprime con dos decimales, que es como lo enseña la ficha de
			// arriba: si el formulario dijera «8.5» donde la tabla dice «8.50», el operador
			// tendría dos cifras distintas para el mismo precio delante de los ojos.
			UnitPrice: strconv.FormatFloat(item.UnitPrice, 'f', 2, 64),
		})
	}
	form.Rows = numberedRows(append(form.Rows, make([]intakeEditRow, intakeEditBlankRows)...))
	form.EditableIn = labelsOf([]string{intakeEditableStatus})
	return form
}

// numberedRows numera las filas para la pantalla (1-based). Es el ÚNICO sitio donde se decide ese
// número, y por eso coincide con el que llevan los defectos.
func numberedRows(rows []intakeEditRow) []intakeEditRow {
	for i := range rows {
		rows[i].Number = i + 1
	}
	return rows
}

// editFormFromRows arma el formulario con lo que el operador tecleó, respetando su orden. Se usa al
// repintar tras un rechazo: su trabajo no se tira, y puede corregir sobre lo que escribió.
func editFormFromRows(rows []intakeEditRow, detail *apiclient.IntakeDetail, transitions []string, defects []intakeEditDefect) *intakeEditForm {
	form := editFormFromDetail(detail, transitions)
	form.Rows = numberedRows(append(append([]intakeEditRow{}, rows...), make([]intakeEditRow, intakeEditBlankRows)...))
	form.Defects = defects
	return form
}

// editRowsFromForm lee las filas del formulario. Devuelve false si los cinco arrays no vienen
// emparejados, que es señal de un envío manipulado o truncado: antes que adivinar qué precio va con
// qué artículo —y arriesgarse a guardar una mezcla— se rechaza y se pide recargar.
func editRowsFromForm(c *gin.Context) ([]intakeEditRow, bool) {
	skus := c.PostFormArray(intakeEditFieldSKU)
	labels := c.PostFormArray(intakeEditFieldLabel)
	customs := c.PostFormArray(intakeEditFieldCustomization)
	qtys := c.PostFormArray(intakeEditFieldQty)
	prices := c.PostFormArray(intakeEditFieldPrice)

	n := len(skus)
	if len(labels) != n || len(customs) != n || len(qtys) != n || len(prices) != n {
		return nil, false
	}
	rows := make([]intakeEditRow, 0, n)
	for i := range skus {
		rows = append(rows, intakeEditRow{
			SKU: skus[i], Label: labels[i], Customization: customs[i],
			Qty: qtys[i], UnitPrice: prices[i],
		})
	}
	return rows, true
}

// itemsFromRows convierte lo tecleado en el cuerpo que va a la API.
//
// Devuelve además `origin`: para cada posición del cuerpo, la fila del formulario de la que salió.
// Hace falta porque las filas en blanco NO se mandan, así que el índice 0-based con el que la
// plataforma señala un defecto no es el número de fila que el operador tiene delante. Sin esa
// traducción, un rechazo de la línea 4 apuntaría a la 2 y mandaría a corregir la línea equivocada.
//
// Lo que se valida aquí es la LECTURA del número —que «8,50» sean ocho con cincuenta y que «ocho»
// no sea nada—, nunca lo que el número significa: que el precio no sea negativo o que la cantidad
// sea 1 o más lo dice el dominio, y duplicarlo daría dos criterios para la misma regla. La frontera
// es exactamente ésa: aquí se lee, allá se juzga.
func itemsFromRows(rows []intakeEditRow, removeIdx int) (items []apiclient.IntakeItem, origin []int, defects []intakeEditDefect) {
	items = make([]apiclient.IntakeItem, 0, len(rows))
	origin = make([]int, 0, len(rows))

	for i, row := range rows {
		if i == removeIdx || row.Blank() {
			continue
		}
		line := i + 1

		qty, qtyErr := parseIntakeQty(row.Qty)
		if qtyErr != "" {
			defects = append(defects, intakeEditDefect{Line: line, Field: "cantidad", Message: qtyErr})
		}
		price, priceErr := parseIntakePrice(row.UnitPrice)
		if priceErr != "" {
			defects = append(defects, intakeEditDefect{Line: line, Field: "precio", Message: priceErr})
		}
		if qtyErr != "" || priceErr != "" {
			continue
		}

		items = append(items, apiclient.IntakeItem{
			SKU:           strings.TrimSpace(row.SKU),
			Label:         strings.TrimSpace(row.Label),
			Customization: strings.TrimSpace(row.Customization),
			Qty:           qty,
			UnitPrice:     price,
		})
		origin = append(origin, line)
	}
	return items, origin, defects
}

// parseIntakeQty lee la cantidad. Devuelve el motivo (vacío = bien leída).
//
// Solo un entero: «2,5 hamburguesas» no es una cantidad de un pedido, y aceptarla redondeando
// decidiría por el operador cuál de las dos quiso decir.
func parseIntakeQty(raw string) (int, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "falta la cantidad: escribe cuántas unidades son (por ejemplo 2)"
	}
	qty, err := strconv.Atoi(s)
	if err != nil {
		return 0, "«" + s + "» no es una cantidad: escribe un número entero (por ejemplo 2)"
	}
	return qty, ""
}

// parseIntakePrice lee el precio tal como lo teclea una persona con prisa, y RECHAZA todo lo que no
// pueda leer con certeza. Devuelve el motivo (vacío = bien leído).
//
// La regla de fondo: un precio mal leído no avisa. Si «8,50» se leyera como 0 se regala el
// artículo, y si «1.234» se leyera como 1,234 se cobra mil veces de menos; las dos cosas se
// descubren cuando ya se entregó. Por eso aquí no hay ninguna rama que, ante la duda, se quede con
// un valor: o el número se lee sin ambigüedad, o se rechaza con un motivo que dice cómo escribirlo.
//
// Qué se acepta:
//   - coma o punto decimal, indistintamente («8,50» y «8.50» son lo mismo);
//   - separador de miles cuando la escritura lo deja claro, que es cuando aparecen los dos signos
//     («1.234,56» y «1,234.56» son los dos 1234.56) o cuando el mismo se repite («1.234.567»);
//   - como mucho DOS decimales, que es con los que la pantalla imprime.
//
// Y el caso que se rechaza aunque parezca inofensivo: un único separador con exactamente tres
// dígitos detrás («1.234»). Ahí no hay forma de saber si son mil doscientos treinta y cuatro o uno
// con doscientos treinta y cuatro milésimas, y las dos lecturas se diferencian en tres ceros.
func parseIntakePrice(raw string) (float64, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "falta el precio: escribe cuánto cuesta la unidad; si va de regalo, escribe 0"
	}

	// El signo se separa antes del filtro de caracteres para poder decir «no puede ser negativo»
	// en vez de «no es un número», que es un motivo que no ayuda a corregir. Que sea negativo lo
	// juzga la plataforma; aquí solo se lee el número que hay detrás del signo.
	sign := 1.0
	body := s
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		sign, body = -1, strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = strings.TrimSpace(rest)
	}

	digits, dots, commas := 0, 0, 0
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
		case r == ',':
			commas++
		default:
			return 0, priceUnreadable(s)
		}
	}
	if digits == 0 {
		return 0, priceUnreadable(s)
	}

	intPart, decPart := body, ""
	if last := strings.LastIndexAny(body, ".,"); last >= 0 {
		intPart, decPart = body[:last], body[last+1:]

		switch {
		case decPart == "":
			return 0, "«" + s + "» acaba en un separador y le falta la parte decimal: escribe 1,50 o 1"
		case dots > 0 && commas > 0:
			// Los dos signos presentes: el último es el decimal y el otro el de miles. Las dos
			// escrituras habituales —«1.234,56» y «1,234.56»— caen aquí y dan lo mismo.
		case dots+commas > 1:
			// El mismo signo repetido solo puede ser separador de miles («1.234.567»): un número
			// no tiene dos comas decimales. La parte decimal, entonces, no existe.
			intPart, decPart = body, ""
		case len(decPart) == 3:
			return 0, "no se sabe si «" + s + "» son " + strings.NewReplacer(".", "", ",", "").Replace(body) +
				" o " + intPart + "," + decPart + ": escríbelo sin separador de miles (" +
				strings.NewReplacer(".", "", ",", "").Replace(body) + ") o con decimales (" + intPart + "," + decPart + "0)"
		}
		if len(decPart) > 2 {
			return 0, "el precio admite como mucho dos decimales, y «" + s + "» trae " +
				strconv.Itoa(len(decPart))
		}
	}

	// Lo que queda a la izquierda del decimal son grupos de miles, y tienen que estar bien
	// formados: «1.23.456» no es un número escrito de otra manera, es un número mal escrito, y
	// limpiarle los puntos para quedarse con 123456 sería inventarse lo que quiso decir.
	groups := strings.FieldsFunc(intPart, func(r rune) bool { return r == '.' || r == ',' })
	if strings.ContainsAny(intPart, ".,") {
		for i, g := range groups {
			if (i == 0 && (len(g) < 1 || len(g) > 3)) || (i > 0 && len(g) != 3) {
				return 0, "«" + s + "» tiene los miles mal agrupados: escríbelo sin separador (" +
					strings.Join(groups, "") + ")"
			}
		}
	}

	clean := strings.Join(groups, "")
	if decPart != "" {
		clean += "." + decPart
	}
	value, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, priceUnreadable(s)
	}
	return sign * value, ""
}

// priceUnreadable es el motivo único de «esto no es un precio». Va en una función para que las tres
// puertas que pueden llegar a él digan exactamente lo mismo.
func priceUnreadable(raw string) string {
	return "«" + raw + "» no es un precio: escribe solo números, con coma o punto para los " +
		"decimales (por ejemplo 1,50)"
}

// DoEditIntakeItems aplica la corrección manual de las líneas desde el formulario del detalle.
//
// Es la puerta con la que el dueño le pone precio a lo que el cliente pidió por escrito y el
// catálogo no tenía (D-041.26): el «queso extra» anotado en la personalización y cobrado a 0. Cada
// envío deja una revisión `corrected` en la plataforma; esta pantalla no la pinta todavía.
func (h *IntakesHandler) DoEditIntakeItems(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	entitlements := resolveEntitlements(c, h.auth, h.api)

	// Sin la capacidad no se llama a la API, igual que en el listado y el detalle: la plataforma
	// cortaría con 403 y el viaje solo serviría para llenar el log de rechazos previsibles. El
	// gate real de lo que se emite está en la plantilla, y el de lo que se PUEDE, allá.
	if !entitlements.Has(intakesFeature) {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusForbidden, id: id, entitlements: &entitlements,
		})
		return
	}
	if id == "" {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, entitlements: &entitlements,
			notice: &intakeNotice{Message: "Falta el identificador de la solicitud."},
		})
		return
	}

	rows, ok := editRowsFromForm(c)
	if !ok {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id, entitlements: &entitlements,
			notice: &intakeNotice{Message: "El formulario llegó incompleto. Recarga la página e inténtalo de nuevo."},
		})
		return
	}

	// `remove` trae el ÍNDICE de la fila, no su sku: si el operador corrigió el sku antes de pulsar
	// «Quitar», la fila que quiere quitar sigue siendo esa, no la que dice el texto recién escrito.
	removeIdx := -1
	if raw := strings.TrimSpace(c.PostForm(intakeEditFieldRemove)); raw != "" {
		idx, err := strconv.Atoi(raw)
		if err != nil || idx < 0 || idx >= len(rows) {
			h.renderIntakeDetail(c, intakeDetailRender{
				status: http.StatusBadRequest, id: id, entitlements: &entitlements, rows: rows,
				notice: &intakeNotice{Message: "No se pudo identificar la línea a quitar. Recarga la página e inténtalo de nuevo."},
			})
			return
		}
		removeIdx = idx
	}

	items, origin, defects := itemsFromRows(rows, removeIdx)
	if len(defects) > 0 {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id, entitlements: &entitlements,
			rows: rows, defects: defects,
			notice: &intakeNotice{Message: "No se guardó nada: revisa las líneas marcadas abajo."},
		})
		return
	}

	var detail *apiclient.IntakeDetail
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var rerr error
		detail, rerr = h.api.ReplaceIntakeItems(c.Request.Context(), accessToken, id, items)
		return rerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice, remote, editableIn := mapEditItemsError(err, origin)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: status, id: id, entitlements: &entitlements,
			rows: rows, defects: remote, editableIn: editableIn, notice: notice,
		})
		return
	}

	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id, entitlements: &entitlements, detail: detail,
		notice: &intakeNotice{Success: true, Message: editedMessage(detail)},
	})
}

// editedMessage redacta la confirmación diciendo cuántas líneas quedaron y en cuánto queda el
// total. Las dos cifras las manda la plataforma —esta capa no suma nada (INV-13)— y están ahí por
// el riesgo propio de un guardado que reemplaza el conjunto entero: una línea que desapareció sin
// querer se nota en la cuenta antes que en la tabla.
func editedMessage(detail *apiclient.IntakeDetail) string {
	lines := len(detail.Items)
	quantity := strconv.Itoa(lines) + " líneas"
	if lines == 1 {
		quantity = "1 línea"
	}
	return "Líneas corregidas: la solicitud queda con " + quantity + " y un total de " +
		strconv.FormatFloat(detail.Total, 'f', 2, 64) + ". La plataforma dejó registrada la corrección."
}

// mapEditItemsError traduce el fallo del guardado. Devuelve, además del aviso, los defectos por
// línea que traiga el rechazo —ya traducidos a números de fila del formulario— y los estados desde
// los que la plataforma sí admite editar.
func mapEditItemsError(err error, origin []int) (int, *intakeNotice, []intakeEditDefect, []string) {
	if invalid, ok := apiclient.InvalidItemsOf(err); ok {
		return http.StatusBadRequest, &intakeNotice{
			Message: "No se guardó nada: la plataforma rechazó las líneas marcadas abajo.",
		}, remoteDefects(invalid.Defects, origin), nil
	}
	if notEditable, ok := apiclient.NotEditableOf(err); ok {
		msg := "Esta solicitud está en «" + intakeStatusLabel(notEditable.Status) +
			"» y así no se corrigen sus líneas. "
		if len(notEditable.EditableIn) == 0 {
			msg += "La plataforma no dice desde qué estado se editan."
		} else {
			msg += "Muévela primero a «" + strings.Join(labelsOf(notEditable.EditableIn), "» o «") +
				"» con el formulario de arriba."
		}
		return http.StatusUnprocessableEntity, &intakeNotice{Message: msg}, nil, notEditable.EditableIn
	}
	if rej, ok := apiclient.RejectionOf(err); ok && rej.Message != "" {
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la corrección: " + rej.Message,
		}, nil, nil
	}
	switch apiclient.StatusCodeOf(err) {
	case http.StatusConflict:
		return http.StatusConflict, &intakeNotice{
			Message: "Otro operador cambió esta solicitud mientras la corregías, así que no se guardó nada. " +
				"Aquí tienes el estado actual; revísalo y vuelve a intentarlo si sigue haciendo falta.",
		}, nil, nil
	case http.StatusNotFound:
		return http.StatusNotFound, &intakeNotice{Message: "Esa solicitud no es tuya o no existe."}, nil, nil
	case http.StatusForbidden:
		return http.StatusForbidden, &intakeNotice{
			Message: "Tu usuario no puede corregir las líneas de las solicitudes de este tenant, " +
				"o el plan ya no incluye la bandeja.",
		}, nil, nil
	case http.StatusBadRequest:
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la corrección y no dijo el motivo. Revisa las líneas e inténtalo de nuevo.",
		}, nil, nil
	}
	slog.Warn("no se pudieron corregir las líneas de la solicitud", "error", err)
	return http.StatusBadGateway, &intakeNotice{
		Message: "No se pudo guardar la corrección ahora mismo. Vuelve a intentarlo; nada se ha cambiado.",
	}, nil, nil
}

// remoteDefects traduce los defectos de la plataforma a filas del formulario. `origin` mapea cada
// posición del cuerpo enviado a su fila; un índice fuera de rango —un servidor que señale una línea
// que no se mandó— se pinta como fila 0 en vez de descartarse: un defecto que no se entiende sigue
// siendo un defecto, y callarlo dejaría al operador dándole a «Guardar» sin saber por qué no entra.
func remoteDefects(defects []apiclient.ItemDefect, origin []int) []intakeEditDefect {
	out := make([]intakeEditDefect, 0, len(defects))
	for _, d := range defects {
		line := 0
		if d.Index >= 0 && d.Index < len(origin) {
			line = origin[d.Index]
		}
		field := d.Field
		if label, ok := intakeEditFieldLabels[d.Field]; ok {
			field = label
		}
		out = append(out, intakeEditDefect{Line: line, Field: field, Message: d.Message})
	}
	return out
}
