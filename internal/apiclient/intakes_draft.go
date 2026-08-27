package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Clases de revisión que publica el detalle (`intake_revisions.kind`). Van como constantes porque
// la pantalla elige POR CLASE cuál pinta, y una errata en un literal repartido por tres ficheros no
// la detecta el compilador.
//
// El histórico se lee, no se juzga: una clase que este cliente no conozca llega igual dentro de
// `IntakeDetail.Revisions` y se pinta con su nombre crudo, que es la misma doctrina de
// `intakeStatusLabel` — antes una clave sin traducir que un histórico con huecos.
const (
	RevisionKindCart        = "cart"
	RevisionKindInterpreted = "interpreted"
	RevisionKindCorrected   = "corrected"
	RevisionKindApproved    = "approved"
	RevisionKindCRM         = "crm"
	RevisionKindDiscarded   = "discarded"
	RevisionKindRevalidated = "revalidated"
)

// IntakeRevision es UNA entrada del histórico de una solicitud, tal como la publica
// `GET /api/v1/intakes/{id}` (Plan 044 · T4.1).
//
// `Payload` viaja como JSON CRUDO y no como un tipo cerrado a propósito: cada clase de revisión
// guarda una forma distinta —la `interpreted` es el borrador rico del LLM, la `corrected` es la
// lista plana de líneas que mandó el dueño— y decodificarlas todas aquí obligaría a este cliente a
// conocer siete contratos para pintar uno. Quien necesite el de la interpretación llama a
// DecodeInterpretation; el resto se conserva sin abrir.
//
// `CreatedBy` es un ROL (`system`, `owner`, `crm`), NUNCA una persona: la plataforma no publica
// quién tecleó, y esta consola no puede inventarlo (INV-04/ADR-0010, cero PII).
type IntakeRevision struct {
	RevisionNo   int             `json:"revision_no"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
	RenderedText string          `json:"rendered_text"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    string          `json:"created_at"`
}

// Clases de adjunto de una `IntakeMediaRef` (`anclaje.Kind*` de la plataforma). Los tres primeros
// son AUDIO: `ptt` es la nota de voz de WhatsApp y `voice` el alias que usan algunos puentes.
const (
	MediaKindImage    = "image"
	MediaKindAudio    = "audio"
	MediaKindPTT      = "ptt"
	MediaKindVoice    = "voice"
	MediaKindVideo    = "video"
	MediaKindDocument = "document"
)

// IntakeMediaRef es un adjunto del cliente referenciado desde el borrador.
//
// `Ref` es OPACO —la key del objeto, o el id del mensaje de WhatsApp— y NUNCA una URL ni una
// credencial. Hoy la API no publica ninguna ruta por la que descargarlo, así que esta consola lo
// NOMBRA y no lo enlaza: un `<a href>` construido sobre esta referencia apuntaría a nada.
type IntakeMediaRef struct {
	Ref   string `json:"ref"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// IsAudio responde si el adjunto se escucha. Es lo que decide el rótulo del borrador, y por eso
// cubre los tres alias: tratar `ptt` como «otro adjunto» dejaría la nota de voz —que es la forma
// más habitual en que un cliente pide— sin mención en la pantalla.
func (m IntakeMediaRef) IsAudio() bool {
	return m.Kind == MediaKindAudio || m.Kind == MediaKindPTT || m.Kind == MediaKindVoice
}

// IntakeLineRange es el rango que pidió el cliente SIN colapsar («10 o 12 porciones»).
type IntakeLineRange struct {
	Min  int    `json:"min"`
	Max  int    `json:"max"`
	Unit string `json:"unit"`
}

// IntakeVariantOption es una presentación entre las que elige EL DUEÑO (D-041.4). Cuando hay más de
// una, la línea viaja con `unit_price: null`: elegir por el cliente sería inventar el precio.
type IntakeVariantOption struct {
	SKU   string  `json:"sku"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// IntakeLineMatch es de dónde salió el match de una línea. Nil en `unmatched` y en `shipping`.
type IntakeLineMatch struct {
	Strategy   string  `json:"strategy"`
	Confidence float64 `json:"confidence"`
}

// Clases de línea del borrador (`stages.Kind*`).
const (
	LineKindMatched   = "matched"
	LineKindUnmatched = "unmatched"
	LineKindShipping  = "shipping"
)

// IntakeDraftLine es un renglón del borrador tal como lo congeló la revisión `interpreted`.
//
// 🔴 NO ES `IntakeItem`, y confundirlas es el error que esta pantalla existe para no cometer.
// `IntakeItem` son las líneas RESUELTAS —las que la plataforma factura— y su `unit_price` es un
// `float64` que no puede decir «todavía no hay precio»; la línea `unmatched`, que es justo la que
// el dueño tiene que atender, NI SIQUIERA ESTÁ en `items`. Esta sí la trae, y con el precio en
// puntero.
//
// UnitPrice es *float64 y NO lleva `omitempty` por eso mismo: `null` significa «lo pone el dueño» y
// `0` significa «va de regalo». Un `float64` pelado colapsaría las dos en `0.00`, que es
// exactamente lo que el render del §7.5 prohíbe.
type IntakeDraftLine struct {
	Kind           string                `json:"kind"`
	SKU            string                `json:"sku,omitempty"`
	Label          string                `json:"label"`
	Qty            int                   `json:"qty"`
	UnitPrice      *float64              `json:"unit_price"`
	Customization  string                `json:"customization,omitempty"`
	Range          *IntakeLineRange      `json:"range,omitempty"`
	UnitKind       string                `json:"unit_kind,omitempty"`
	PackageSize    int                   `json:"package_size,omitempty"`
	VariantOptions []IntakeVariantOption `json:"variant_options,omitempty"`
	Match          *IntakeLineMatch      `json:"match,omitempty"`
	Note           string                `json:"note,omitempty"`
	Evidence       string                `json:"evidence,omitempty"`
	MediaRefs      []IntakeMediaRef      `json:"media_refs,omitempty"`
}

// HasPrice responde si la línea trae precio. Es la pregunta que decide si se imprime un número o se
// pide uno, y va aquí —y no en la plantilla comparando con nil— para que exista UN solo criterio.
func (l IntakeDraftLine) HasPrice() bool { return l.UnitPrice != nil }

// Price es el precio unitario, y 0 cuando no hay. Solo se llama detrás de HasPrice: quien lo use
// sin preguntar imprimirá el 0 que el §7.5 prohíbe.
func (l IntakeDraftLine) Price() float64 {
	if l.UnitPrice == nil {
		return 0
	}
	return *l.UnitPrice
}

// IntakeAnalysis es el rastro de QUIÉN interpretó (D-044.15).
//
// ⚠️ `Provider` sale CADENA VACÍA en la interpretación normal: hoy solo lo rellenan las revisiones
// nacidas de `/reanalyze`. Quien lo pinte tiene que tratar el vacío como «no consta», no como un
// proveedor llamado «».
//
// `ReanalyzedFrom` es puntero por el mismo motivo que `AllowedTransitions`: `null` dice «esta es la
// primera lectura», que no es lo mismo que no traer el campo.
type IntakeAnalysis struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Source         string `json:"source"`
	ReanalyzedFrom *int   `json:"reanalyzed_from"`
}

// IntakeInterpretation es el payload de una revisión `interpreted`: el borrador que el §7.5 pinta.
//
// `SuggestedQuestions` es PUNTERO y ahí hay una distinción que la pantalla necesita: la lista vacía
// (`[]`) significa «no había nada que preguntar», y la clave AUSENTE significa que el tenant no
// tiene la feature `llm_intake` —la plataforma borra la clave, no la deja en `[]`, justamente para
// que las dos no se confundan—. Colapsarlas aquí en un slice nil le diría al dueño que el LLM no
// tenía preguntas cuando lo que pasa es que no ha pagado por ellas.
type IntakeInterpretation struct {
	Version            int               `json:"version"`
	SourceText         string            `json:"source_text"`
	MessageTS          string            `json:"message_ts"`
	Analysis           IntakeAnalysis    `json:"analysis"`
	DeliveryDate       string            `json:"delivery_date"`
	MediaRefs          []IntakeMediaRef  `json:"media_refs"`
	Lines              []IntakeDraftLine `json:"lines"`
	SuggestedQuestions *[]string         `json:"suggested_questions"`
}

// Questions son las preguntas preparadas (vacío si no hay o si la feature no está).
func (p *IntakeInterpretation) Questions() []string {
	if p == nil || p.SuggestedQuestions == nil {
		return nil
	}
	return *p.SuggestedQuestions
}

// QuestionsKnown responde si la plataforma llegó a publicar la clave. Falso ⇒ el tenant no tiene
// `llm_intake`, y la pantalla lo dice en vez de fingir que no había preguntas.
func (p *IntakeInterpretation) QuestionsKnown() bool {
	return p != nil && p.SuggestedQuestions != nil
}

// DecodeInterpretation abre el payload de una revisión `interpreted`.
//
// Va como función aparte y no como un campo tipado de IntakeRevision porque solo tiene sentido para
// UNA de las siete clases: decodificar el payload de una `corrected` con esta forma daría un
// borrador con las líneas vacías y nadie se enteraría.
//
// NO usa `DisallowUnknownFields`, como ningún decodificador de este cliente: es lo que permite que
// esta consola siga funcionando cuando la plataforma añada un campo, en vez de caerse entera por
// algo que no iba a pintar.
func DecodeInterpretation(raw json.RawMessage) (*IntakeInterpretation, error) {
	if len(raw) == 0 {
		return nil, errors.New("apiclient: la revisión no trae payload")
	}
	var out IntakeInterpretation
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("apiclient: decodificar el borrador de la revisión: %w", err)
	}
	return &out, nil
}

// LastRevisionOf devuelve la ÚLTIMA revisión de una clase (nil si no hay ninguna).
//
// «Última» es la de mayor `revision_no` y no la última del slice: el orden con el que la API
// devuelve el histórico no es contrato, y ordenar por posición dejaría al dueño mirando una
// interpretación vieja el día que ese orden cambie.
func (d *IntakeDetail) LastRevisionOf(kind string) *IntakeRevision {
	var found *IntakeRevision
	for i := range d.Revisions {
		rev := &d.Revisions[i]
		if rev.Kind != kind {
			continue
		}
		if found == nil || rev.RevisionNo > found.RevisionNo {
			found = rev
		}
	}
	return found
}

// RevisionsAfter cuenta las revisiones POSTERIORES a un número. Es lo que permite avisar de que el
// borrador que se está pintando ya no es lo último que pasó con la solicitud: la revisión
// `interpreted` se congela cuando el LLM interpreta y NO se reescribe cuando el dueño corrige.
func (d *IntakeDetail) RevisionsAfter(revisionNo int) int {
	n := 0
	for _, rev := range d.Revisions {
		if rev.RevisionNo > revisionNo {
			n++
		}
	}
	return n
}

// Motivos tipados de las acciones de la Ola 4, tal como los nombra la plataforma.
const (
	errLinesWithoutPrice = "lines_without_price"
	errNotApprovable     = "not_approvable"
)

// IntakeLineRef localiza UNA línea sin precio dentro del borrador: su POSICIÓN y su etiqueta.
//
// Va con posición y NO con sku a propósito, y no es un descuido del contrato: la línea que suele
// faltar de precio es la `unmatched`, que por definición NO TIENE sku. Señalarla por sku sería
// señalarla por un campo vacío.
type IntakeLineRef struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

// LinesWithoutPriceError es el 400 de POST /api/v1/intakes/{id}/approve: quedan líneas sin precio y
// la cotización no puede salir. Trae TODAS de una vez, que es lo que el dueño necesita para
// arreglarlas en una pasada.
type LinesWithoutPriceError struct {
	Lines []IntakeLineRef
}

func (e *LinesWithoutPriceError) Error() string {
	return fmt.Sprintf("apiclient: la solicitud tiene %d líneas sin precio", len(e.Lines))
}

// LinesWithoutPriceOf extrae el rechazo por líneas sin precio (nil, false si no lo es).
func LinesWithoutPriceOf(err error) (*LinesWithoutPriceError, bool) {
	var missing *LinesWithoutPriceError
	if errors.As(err, &missing) {
		return missing, true
	}
	return nil, false
}

// NotApprovableError es el 422 de la aprobación: la solicitud no está en un estado que la admita.
// Trae dónde está AHORA y desde qué estados SÍ se aprueba, por lo mismo que NotEditableError: el
// ciclo de vida lo manda la plataforma y esta consola no lo replica.
type NotApprovableError struct {
	Status       string   `json:"status"`
	ApprovableIn []string `json:"approvable_in"`
}

func (e *NotApprovableError) Error() string {
	return fmt.Sprintf("apiclient: una solicitud en %q no se puede aprobar", e.Status)
}

// NotApprovableOf extrae el rechazo por estado no aprobable (nil, false si no lo es).
func NotApprovableOf(err error) (*NotApprovableError, bool) {
	var notApprovable *NotApprovableError
	if errors.As(err, &notApprovable) {
		return notApprovable, true
	}
	return nil, false
}

// approveIntakeRequest es el cuerpo de POST /api/v1/intakes/{id}/approve.
//
// El texto es el ÚNICO campo, y es obligatorio (D-044.19): el dueño es el autor de lo que sale, y
// lo que la revisión guarda es byte a byte lo que se envió. No van aquí ni las líneas ni el total
// —esos ya están escritos— y mandarlos abriría la puerta a aprobar una cotización distinta de la
// que está en la solicitud.
type approveIntakeRequest struct {
	RenderedText string `json:"rendered_text"`
}

// requestIntakeInfoRequest es el cuerpo de POST /api/v1/intakes/{id}/request-info: la pregunta YA
// EDITADA por el dueño. Las `suggested_questions` del borrador son una propuesta y jamás salen
// solas (INV-1); lo que viaja es lo que el dueño dejó escrito.
type requestIntakeInfoRequest struct {
	Question string `json:"question"`
}

// ApproveIntake aprueba la solicitud y le responde al cliente con `renderedText`, vía
// POST /api/v1/intakes/{id}/approve (Plan 044 · T4.3). Devuelve el detalle ya en `confirmed`.
//
// 🔴 El 200 significa «se aplicó y quedó registrado», NUNCA «el cliente lo recibió». El envío por
// WhatsApp cuelga de una sesión que puede estar caída, y prometerle al dueño la entrega es
// prometerle algo que esta puerta no sabe.
//
// Errores: *LinesWithoutPriceError (400, con la lista entera), *NotApprovableError (422, con los
// estados desde los que sí se aprueba), *InvalidTransitionError (422 por carrera con otro
// operador), *RejectionError para el resto de 400 con motivo —el texto vacío, sobre todo— y
// *APIError para 403/404/409/5xx.
func (c *IntakesClient) ApproveIntake(ctx context.Context, accessToken, id, renderedText string) (*IntakeDetail, error) {
	return c.intakeAction(ctx, accessToken, id, "approve", "intake approve",
		approveIntakeRequest{RenderedText: renderedText})
}

// RequestIntakeInfo manda al cliente la pregunta del dueño y deja la solicitud en `needs_info`, vía
// POST /api/v1/intakes/{id}/request-info (Plan 044 · T4.4). Devuelve el detalle ya transicionado.
//
// Mismo aviso que la aprobación: el 200 dice que se registró y se intentó enviar, no que el cliente
// lo tenga delante.
//
// Errores: *RejectionError para el 400 (pregunta vacía), *InvalidTransitionError para el 422 —que
// aquí llega SIN estados permitidos, porque esta puerta no publica ese cuerpo— y *APIError para el
// resto.
func (c *IntakesClient) RequestIntakeInfo(ctx context.Context, accessToken, id, question string) (*IntakeDetail, error) {
	return c.intakeAction(ctx, accessToken, id, "request-info", "intake request-info",
		requestIntakeInfoRequest{Question: question})
}

// intakeAction ejecuta una de las acciones POST del detalle. Las dos comparten forma —cuerpo JSON,
// respuesta con el detalle COMPLETO y la misma familia de rechazos— y separarlas en dos funciones
// gemelas solo garantizaría que una de las dos se quedara sin el siguiente error tipado.
func (c *IntakesClient) intakeAction(ctx context.Context, accessToken, id, action, op string, payload any) (*IntakeDetail, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost,
		"/api/v1/intakes/"+url.PathEscape(id)+"/"+action, payload, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s: %w", op, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, intakeActionError(op, resp)
	}
	var out IntakeDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	return &out, nil
}

// intakeActionError traduce un no-2xx de las acciones del detalle. Lee el cuerpo UNA vez y decide
// con él, porque dos rechazos distintos comparten status y solo los separa la CLAVE `error`: el 400
// puede ser «faltan precios» o «falta el texto», y el 422 puede ser «este estado no aprueba» o «otro
// operador la movió».
func intakeActionError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error        string          `json:"error"`
		Lines        []IntakeLineRef `json:"lines"`
		Status       string          `json:"status"`
		ApprovableIn []string        `json:"approvable_in"`
		Requested    string          `json:"requested"`
		Allowed      []string        `json:"allowed"`
	}
	// Un cuerpo ilegible deja el motivo en blanco: el status sigue siendo la información principal
	// y el llamante tiene su texto genérico (mismo criterio que replaceIntakeItemsError).
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxIntakeItemsErrorBody)).Decode(&body)

	switch resp.StatusCode {
	case http.StatusBadRequest:
		if body.Error == errLinesWithoutPrice {
			return &LinesWithoutPriceError{Lines: body.Lines}
		}
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
	case http.StatusUnprocessableEntity:
		if body.Error == errNotApprovable {
			return &NotApprovableError{Status: body.Status, ApprovableIn: body.ApprovableIn}
		}
		return &InvalidTransitionError{Status: body.Status, Requested: body.Requested, Allowed: body.Allowed}
	}
	return statusError(op, resp.StatusCode)
}
