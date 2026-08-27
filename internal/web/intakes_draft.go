package web

import (
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// intakeAudioLabel es el rótulo con el que el borrador nombra un audio del cliente. Es el mismo
// literal que pone la plataforma (`anclaje.EtiquetaAudio`) y solo se usa como RESPALDO: si la
// referencia trae su etiqueta, se pinta la suya.
const intakeAudioLabel = "🎙️ audio del cliente — escúchalo"

// intakeOverdueHours son las horas tras las que la plataforma marca una solicitud como `overdue`.
// Es un ESPEJO de presentación —quien decide la marca es la plataforma, que la publica ya
// calculada— y vive aquí solo para poder redactar el aviso en horas en vez de decir «hace mucho».
const intakeOverdueHours = 24

// intakeDraftMedia es un adjunto del cliente ya redactado para la pantalla.
//
// Lleva TEXTO y no enlace, y eso es una decisión, no una tarea a medias (D-044.52 §1): la
// referencia del adjunto es OPACA —nunca una URL— y hoy la API no publica ninguna ruta por la que
// descargarlo. Un `<a href>` construido sobre ella llevaría a ninguna parte, y un enlace roto es
// peor que una mención: promete algo que no existe.
type intakeDraftMedia struct {
	Text  string
	Audio bool
}

// intakeDraftLine es UN renglón del borrador tal como lo pinta el §7.5: la parte que se lee y la
// parte que se teclea, juntas, porque en esta pantalla son la misma fila.
//
// Los valores editables van como TEXTO por lo mismo que en el formulario del 041: es lo que la
// persona tecleó y hay que poder repintárselo tal cual cuando algo se rechaza.
type intakeDraftLine struct {
	// Number es el número de fila 1-based ENTRE LAS EDITABLES, que es exactamente el índice con el
	// que viajan los campos del formulario y el que señalan los defectos. La línea de envío no
	// entra en la numeración porque tampoco entra en el formulario.
	Number int
	Kind   string
	// KindLabel es cómo se llama esa clase de línea en la pantalla.
	KindLabel string

	SKU           string
	Label         string
	Customization string
	Qty           string
	// UnitPrice es el precio ya formateado, y CADENA VACÍA cuando la línea no lo tiene. Aquí está
	// el corazón de la tarea: `null` y `0` llegan distintos desde el apiclient y tienen que seguir
	// distintos hasta el HTML. Un `printf "%.2f"` incondicional los colapsa en «0.00», que es lo
	// que el §7.5 prohíbe — dice que la torta sin match cuesta cero.
	UnitPrice string
	HasPrice  bool

	// PendingPrice es «esta línea es de las que el dueño tiene que poner a precio». SOLO lo son las
	// `unmatched` sin precio: son las que el catálogo no supo resolver.
	PendingPrice bool
	// PendingNote es el motivo cuando la línea NO tiene precio pero TAMPOCO cuenta como pendiente:
	// una `matched` con varias presentaciones espera a que el dueño ELIJA (el precio existe, en el
	// catálogo, uno por variante), y el envío espera a confirmar zona. Contarlas juntas haría que
	// la pantalla dijera «3 líneas pendientes de precio» donde el §7.5 dice 1.
	PendingNote string

	Evidence string
	Note     string
	// Size es el tamaño pedido sin colapsar («10-12 porciones») y Pack la unidad de venta que trajo
	// P4 («paquete de 30»). Viajan porque son lo que distingue dos líneas que se llaman igual.
	Size string
	Pack string

	Variants []apiclient.IntakeVariantOption
	Match    *apiclient.IntakeLineMatch
	Media    []intakeDraftMedia
}

// intakeDraftView es el borrador del §7.5 entero: lo que el dueño lee para decidir.
type intakeDraftView struct {
	// RevisionNo es la revisión `interpreted` de la que sale todo esto.
	RevisionNo int
	// Newer son las revisiones POSTERIORES a ella. La `interpreted` se congela cuando el LLM
	// interpreta y NO se reescribe cuando el dueño corrige, así que con correcciones encima este
	// bloque enseña la lectura original: se avisa en vez de dejar creer que es lo vigente.
	Newer int

	Lines []intakeDraftLine
	// Shipping son las líneas que pone la plataforma (el envío). Van FUERA de la tabla editable
	// porque no se editan aquí —la plataforma rechaza su prefijo reservado— y sacarlas de la tabla
	// es lo que permite que el número de fila y el índice del formulario sean el mismo número.
	Shipping []intakeDraftLine

	// PartialTotal es el total que manda LA PLATAFORMA, y es el total parcial del §7.5 sin que esta
	// capa sume nada (INV-13): `items` solo contiene las líneas resueltas —la `unmatched` ni
	// siquiera está—, así que ese número ya excluye exactamente lo que falta por poner a precio.
	// Recalcularlo aquí crearía una segunda autoridad sobre el dinero que divergiría de la primera.
	//
	// 🔑 Y no es una intuición, está MEDIDO contra el golden del cloud (2026-08-27): `total` vale
	// 21000, que es exactamente `qty × unit_price` del ÚNICO elemento de `items`, mientras el payload
	// de la revisión trae dos líneas más sin precio —la `unmatched` y la que espera variante— que
	// quedan fuera de la cuenta porque no están en `items`. El número de la plataforma YA es el total
	// parcial; lo único que pone esta pantalla es la palabra «parcial» y el conteo de lo que falta.
	PartialTotal float64
	// PendingPrice es cuántas líneas espera el dueño poner a precio (las `unmatched` sin precio).
	PendingPrice int
	// VariantPending y ShippingPending son las otras dos ausencias de precio, contadas APARTE
	// porque no son lo mismo y la pantalla no puede sumarlas al conteo de arriba.
	VariantPending  int
	ShippingPending bool

	DeliveryDate string
	SourceText   string
	Analysis     apiclient.IntakeAnalysis
	Media        []intakeDraftMedia

	// Questions son las preguntas preparadas y QuestionsKnown si la plataforma llegó a publicar la
	// clave: ausente ⇒ el plan del tenant no incluye `llm_intake`, que no es lo mismo que «el LLM
	// no tenía nada que preguntar».
	Questions      []string
	QuestionsKnown bool

	// Editable es si el estado admite corregir y responder. Sale del mismo espejo que el formulario
	// del 041 (`intakeEditableStatus`) y por la misma razón: la plataforma publica los destinos del
	// ciclo de vida pero no desde dónde se edita.
	Editable bool
	// Defects son los problemas de la última tentativa de corrección desde ESTE formulario.
	Defects []intakeEditDefect
}

// TotalText redacta el total parcial CON el conteo de lo que falta, que es lo que pide el §7.5: un
// número suelto no dice que sea parcial, y el dueño lo leería como el precio final del pedido.
func (v *intakeDraftView) TotalText() string {
	total := strconv.FormatFloat(v.PartialTotal, 'f', 2, 64)
	switch v.PendingPrice {
	case 0:
		return "Total parcial: " + total + " (ninguna línea pendiente de precio)"
	case 1:
		return "Total parcial: " + total + " (1 línea pendiente de precio)"
	default:
		return "Total parcial: " + total + " (" + strconv.Itoa(v.PendingPrice) + " líneas pendientes de precio)"
	}
}

// VariantPendingText redacta las líneas que esperan a que el dueño ELIJA presentación, y dice en
// voz alta por qué NO están en el conteo de arriba: su precio existe —hay uno por variante en el
// catálogo—, lo que falta es la elección. Vacío cuando no hay ninguna.
func (v *intakeDraftView) VariantPendingText() string {
	switch v.VariantPending {
	case 0:
		return ""
	case 1:
		return "1 línea espera a que elijas presentación: no cuenta como línea pendiente de precio, " +
			"porque el precio ya está en el catálogo —falta elegir cuál—."
	default:
		return strconv.Itoa(v.VariantPending) + " líneas esperan a que elijas presentación: no cuentan " +
			"como líneas pendientes de precio, porque el precio ya está en el catálogo —falta elegir cuál—."
	}
}

// AnalysisText redacta quién interpretó. `Provider` sale CADENA VACÍA en la interpretación normal
// —solo lo rellenan las revisiones nacidas de un re-análisis—, y eso se dice como «no consta» en vez
// de pintar un proveedor llamado «».
func (v *intakeDraftView) AnalysisText() string {
	via := v.Analysis.Provider
	if strings.TrimSpace(via) == "" {
		via = "una vía que no consta"
	}
	text := "Interpretado por " + via
	if v.Analysis.Model != "" {
		text += " · modelo " + v.Analysis.Model
	}
	if v.Analysis.ReanalyzedFrom != nil {
		text += " · re-análisis de la revisión " + strconv.Itoa(*v.Analysis.ReanalyzedFrom)
	}
	return text
}

// HasAudio responde si el cliente mandó algo que se escucha, mirando tanto la cabecera del borrador
// como las líneas. Es lo que decide si sale el rótulo del audio.
func (v *intakeDraftView) HasAudio() bool {
	for _, m := range v.Media {
		if m.Audio {
			return true
		}
	}
	for _, l := range v.Lines {
		for _, m := range l.Media {
			if m.Audio {
				return true
			}
		}
	}
	return false
}

// draftViewOf arma el borrador desde la ÚLTIMA revisión `interpreted` del detalle. Devuelve nil
// cuando no hay ninguna o cuando su payload no se puede leer: el §7.5 no se pinta a medias ni se
// sustituye por `items`, que es otra cosa (y que la pantalla sigue pintando arriba).
//
// `rows` es lo que el operador tecleó en este formulario (nil ⇒ se arma con lo que dice la
// plataforma): al repintar tras un rechazo su trabajo no se tira.
func draftViewOf(detail *apiclient.IntakeDetail, rows []intakeEditRow, defects []intakeEditDefect) *intakeDraftView {
	rev := detail.LastRevisionOf(apiclient.RevisionKindInterpreted)
	if rev == nil {
		return nil
	}
	payload, err := apiclient.DecodeInterpretation(rev.Payload)
	if err != nil {
		return nil
	}

	view := &intakeDraftView{
		RevisionNo:     rev.RevisionNo,
		Newer:          detail.RevisionsAfter(rev.RevisionNo),
		PartialTotal:   detail.Total,
		DeliveryDate:   payload.DeliveryDate,
		SourceText:     payload.SourceText,
		Analysis:       payload.Analysis,
		Media:          draftMediaOf(payload.MediaRefs),
		Questions:      payload.Questions(),
		QuestionsKnown: payload.QuestionsKnown(),
		Editable:       detail.Status == intakeEditableStatus,
		Defects:        defects,
	}

	for _, line := range payload.Lines {
		view.add(draftLineOf(line))
	}
	applyTypedRows(view.Lines, rows)
	return view
}

// add coloca la línea donde le toca y lleva las cuentas del §7.5 en el mismo sitio en que se decide
// la clase: si el reparto y el conteo vivieran separados, una clase nueva entraría en uno y no en
// el otro, y el conteo mentiría sin que nada fallara.
func (v *intakeDraftView) add(line intakeDraftLine) {
	if line.Kind == apiclient.LineKindShipping {
		v.Shipping = append(v.Shipping, line)
		if !line.HasPrice {
			v.ShippingPending = true
		}
		return
	}
	line.Number = len(v.Lines) + 1
	v.Lines = append(v.Lines, line)
	switch {
	case line.PendingPrice:
		v.PendingPrice++
	case !line.HasPrice && len(line.Variants) > 0:
		v.VariantPending++
	}
}

// draftLineOf traduce una línea del payload a lo que la pantalla necesita.
func draftLineOf(line apiclient.IntakeDraftLine) intakeDraftLine {
	out := intakeDraftLine{
		Kind:          line.Kind,
		KindLabel:     intakeLineKindLabel(line.Kind),
		SKU:           line.SKU,
		Label:         line.Label,
		Customization: line.Customization,
		Qty:           strconv.Itoa(line.Qty),
		HasPrice:      line.HasPrice(),
		Evidence:      line.Evidence,
		Note:          line.Note,
		Size:          rangeText(line.Range),
		Pack:          packText(line.UnitKind, line.PackageSize),
		Variants:      line.VariantOptions,
		Match:         line.Match,
		Media:         draftMediaOf(line.MediaRefs),
	}
	if out.HasPrice {
		// El precio se re-imprime con dos decimales, igual que la ficha de arriba: dos cifras
		// distintas para el mismo precio en la misma página harían dudar de cuál es.
		out.UnitPrice = strconv.FormatFloat(line.Price(), 'f', 2, 64)
		return out
	}
	// Sin precio NO se imprime ningún número: ni «0.00» ni «0». El hueco se queda vacío para que lo
	// rellene el dueño, y la pantalla dice por qué está vacío.
	switch {
	case line.Kind == apiclient.LineKindUnmatched:
		out.PendingPrice = true
	case len(line.VariantOptions) > 0:
		out.PendingNote = "falta elegir presentación"
	default:
		out.PendingNote = "sin precio"
	}
	return out
}

// intakeLineKindLabel traduce la clase de línea al nombre que ve el dueño. Una clase desconocida se
// devuelve TAL CUAL, misma doctrina que intakeStatusLabel: antes una clave cruda que una traducción
// inventada o una línea escondida.
func intakeLineKindLabel(kind string) string {
	switch kind {
	case apiclient.LineKindMatched:
		return "del catálogo"
	case apiclient.LineKindUnmatched:
		return "sin match"
	case apiclient.LineKindShipping:
		return "envío"
	}
	return kind
}

// rangeText redacta el tamaño pedido sin colapsarlo («10-12 porciones»): el rango es lo que el
// cliente dijo, y quedarse con un extremo decidiría por él.
func rangeText(r *apiclient.IntakeLineRange) string {
	if r == nil || (r.Min == 0 && r.Max == 0) {
		return ""
	}
	size := strconv.Itoa(r.Min)
	if r.Max != r.Min {
		size += "-" + strconv.Itoa(r.Max)
	}
	if r.Unit != "" {
		size += " " + r.Unit
	}
	return size
}

// packText redacta la unidad de venta que trajo P4 («paquete de 30»). Sin ella, «un paquete de 30»
// se pierde en cuanto la línea toma el nombre del catálogo.
func packText(unitKind string, packageSize int) string {
	if unitKind == "" && packageSize == 0 {
		return ""
	}
	if packageSize <= 0 {
		return unitKind
	}
	if unitKind == "" {
		return "de " + strconv.Itoa(packageSize)
	}
	return unitKind + " de " + strconv.Itoa(packageSize)
}

// draftMediaOf redacta los adjuntos. Un audio SIN etiqueta cae en el literal de la plataforma, y
// una clase que este cliente no conozca se nombra por su clave: callar un adjunto dejaría al dueño
// creyendo que el cliente solo escribió.
func draftMediaOf(refs []apiclient.IntakeMediaRef) []intakeDraftMedia {
	if len(refs) == 0 {
		return nil
	}
	out := make([]intakeDraftMedia, 0, len(refs))
	for _, ref := range refs {
		out = append(out, intakeDraftMedia{Text: mediaText(ref), Audio: ref.IsAudio()})
	}
	return out
}

func mediaText(ref apiclient.IntakeMediaRef) string {
	if label := strings.TrimSpace(ref.Label); label != "" {
		return label
	}
	switch ref.Kind {
	case apiclient.MediaKindAudio, apiclient.MediaKindPTT, apiclient.MediaKindVoice:
		return intakeAudioLabel
	case apiclient.MediaKindImage:
		return "🖼️ imagen del cliente"
	case apiclient.MediaKindVideo:
		return "🎬 vídeo del cliente"
	case apiclient.MediaKindDocument:
		return "📄 documento del cliente"
	}
	return "adjunto del cliente (" + ref.Kind + ")"
}

// applyTypedRows devuelve a las filas lo que el operador tecleó, conservando el resto de la línea
// —clase, evidencia, presentaciones— que el formulario no manda y que la pantalla necesita para
// seguir explicando por qué esa línea está ahí.
//
// Si las cuentas no cuadran no se mezcla nada: un formulario con otro número de filas es un envío
// viejo o manipulado, y emparejar precios con artículos ajenos es peor que perder lo tecleado.
func applyTypedRows(lines []intakeDraftLine, rows []intakeEditRow) {
	if len(rows) != len(lines) {
		return
	}
	for i := range lines {
		lines[i].SKU = rows[i].SKU
		lines[i].Label = rows[i].Label
		lines[i].Customization = rows[i].Customization
		lines[i].Qty = rows[i].Qty
		lines[i].UnitPrice = rows[i].UnitPrice
	}
}
