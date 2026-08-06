package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Intake es la cabecera de una solicitud tal como la publica la API pública
// (Plan 041 · T1.1). Los campos son EXACTAMENTE los de `intakeDTO` de la
// plataforma; nada se enriquece aquí.
//
// ContactID viaja OPACO: es un identificador sin número ni JID (INV-04/ADR-0010).
// El BFF lo pinta tal cual y no intenta resolverlo a un nombre o un teléfono —no
// puede, y no debe.
//
// CustomerNote es la indicación del cliente para el PEDIDO ENTERO (D-041.19): el
// «dejarlo en portería, no tocar el timbre». No confundir con
// IntakeItem.Customization, que es la indicación de UNA línea («sin cebolla»): son
// campos distintos, de niveles distintos, y la pantalla los pinta en sitios
// distintos a propósito. Va aquí —en la cabecera— y no en IntakeDetail porque la
// plataforma lo publica en `publicapi.intakeDTO`, que es también el DTO del
// listado y el que devuelve el cambio de estado.
//
// Como `customization`, la plataforma lo publica SIEMPRE, también vacío (sin
// `omitempty`), así que la clave ausente —un servidor anterior a T4.1c— y la nota
// vacía colapsan aquí en `""`. La pantalla trata las dos igual: no pinta nada.
type Intake struct {
	ID           string  `json:"id"`
	ContactID    string  `json:"contact_id"`
	SessionID    string  `json:"session_id"`
	Status       string  `json:"status"`
	Total        float64 `json:"total"`
	CustomerNote string  `json:"customer_note"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// IntakeItem es una línea de la solicitud (código de catálogo del tenant, nunca PII).
//
// Customization es la personalización NO facturable de la línea (D-041.17): el «sin
// cebolla». Es una instrucción de PRODUCCIÓN y por eso viaja hasta aquí — quien recibió
// el pedido y quien lo prepara son personas distintas, y una personalización que no
// llega a la pantalla se pierde. NUNCA entra en ninguna cuenta: el total lo manda la
// plataforma y esta capa no lo recalcula (INV-13).
//
// La plataforma la publica SIEMPRE, también vacía (sin `omitempty`, ver
// `publicapi.intakeItemDTO` en cloud), así que aquí las dos ausencias posibles —clave
// que no llega y personalización vacía— colapsan en `""`. Es aceptable porque la
// pantalla trata las dos igual: no pinta nada. No la conviertas en puntero para
// distinguirlas sin un consumidor que necesite esa diferencia.
type IntakeItem struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// IntakePage es la respuesta de GET /api/v1/intakes: la página más el TOTAL de
// coincidencias del filtro, que es lo que hace falta para pintar el paginador.
type IntakePage struct {
	Intakes  []Intake `json:"intakes"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int      `json:"total"`
}

// IntakeDetail es la respuesta de GET /api/v1/intakes/{id}: cabecera + líneas.
//
// AllowedTransitions son los destinos válidos desde el estado actual, y es lo que
// alimenta el `<select>` de la pantalla: el BFF NO replica la máquina de estados
// —que vive en `internal/intakes/status.go` de la plataforma— porque un mapa
// duplicado se desincroniza a la primera transición nueva.
//
// El campo es un puntero deliberado, porque hay dos «sin destinos» que no
// significan lo mismo y la UI tiene que decirlos distinto:
//   - `nil` (la plataforma NO manda el campo): no se sabe, así que la pantalla lo
//     declara en vez de fingir un estado terminal. Ya no es el caso corriente —el
//     detalle publica el campo SIEMPRE desde cloud `a804943`—, pero un servidor
//     anterior a esa versión sigue cayendo aquí y hay que distinguirlo.
//   - lista vacía (la plataforma manda `[]`): estado TERMINAL, no admite cambios. Es
//     lo que devuelve `abandoned` (T4.6) y el resto de estados sin salida.
//
// El struct NO declara `revisions`, y eso ya no es porque la plataforma lo omita
// —lo publica desde cloud `eb48245`, T4.1— sino porque esta consola no lo pinta: el
// decodificador ignora lo que no conoce. Decláralo cuando haya una pantalla que lo
// use, no antes.
type IntakeDetail struct {
	Intake
	Items              []IntakeItem `json:"items"`
	AllowedTransitions *[]string    `json:"allowed_transitions"`
}

// IntakeFilter son los filtros y la paginación de GET /api/v1/intakes. Los ceros
// significan «sin filtro»: la API aplica sus propios defaults (página 1, tamaño 50,
// máximo 200).
//
// From/To aceptan `YYYY-MM-DD` (día suelto en UTC) o RFC3339; un `to` con fecha
// suelta cubre el día ENTERO. Status admite las claves del ciclo de vida y el
// `closed` legado; una clave desconocida la rechaza la API con 400, y esa
// validación no se replica aquí.
type IntakeFilter struct {
	From     string
	To       string
	Status   string
	Session  string
	Page     int
	PageSize int
}

// query serializa el filtro a la query string de la API. Lo vacío no se manda: un
// `status=` vacío y no mandar `status` significan lo mismo, y omitirlo deja la URL
// legible en los logs.
func (f IntakeFilter) query() string {
	q := url.Values{}
	for key, value := range map[string]string{
		"from": f.From, "to": f.To, "status": f.Status, "session": f.Session,
	} {
		if value != "" {
			q.Set(key, value)
		}
	}
	if f.Page > 0 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if f.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(f.PageSize))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// maxIntakeItemsErrorBody acota lo que se lee de un rechazo de la edición manual. Es
// mucho más alto que maxRejectionBody (500 B) por lo mismo que el del import: el 400
// no trae un motivo suelto sino la lista COMPLETA de defectos —uno por línea y campo,
// cada uno con su prosa—, y recortarla dejaría al operador arreglando solo los
// primeros.
const maxIntakeItemsErrorBody = 64 << 10

// ItemDefect localiza UN defecto de UNA línea de la edición manual: en qué posición
// del cuerpo que se mandó, qué campo y qué le pasa. Es la forma EXACTA de
// `intakes.LineDefect` de la plataforma.
//
// Index es la posición 0-based en la lista ENVIADA, que no tiene por qué coincidir
// con la fila del formulario (las filas en blanco no se mandan). Traducir de una a
// otra es cosa de quien armó el cuerpo, y por eso este tipo no lo intenta.
type ItemDefect struct {
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// InvalidItemsError es el 400 de PUT /api/v1/intakes/{id}/items por líneas mal
// formadas: TODOS los defectos de una vez.
//
// Va como tipo propio y no como *RejectionError por lo mismo que
// CatalogImportInvalidError: la pantalla no enseña un motivo, enseña una lista con la
// que el dueño corrige sus líneas. Y cuando llega, la plataforma NO escribió nada: la
// edición es todo-o-nada.
type InvalidItemsError struct {
	Defects []ItemDefect
}

func (e *InvalidItemsError) Error() string {
	return fmt.Sprintf("apiclient: la edición tiene %d líneas inválidas", len(e.Defects))
}

// InvalidItemsOf extrae el rechazo por líneas inválidas de un error (nil, false si no
// lo es).
func InvalidItemsOf(err error) (*InvalidItemsError, bool) {
	var invalid *InvalidItemsError
	if errors.As(err, &invalid) {
		return invalid, true
	}
	return nil, false
}

// NotEditableError es el 422 de PUT /api/v1/intakes/{id}/items: la solicitud no está
// en un estado que admita edición manual. Trae dónde está AHORA y desde qué estados
// SÍ se edita.
//
// EditableIn es la razón de que esta pantalla no tenga que saberse el ciclo de vida:
// igual que `allowed` en el 422 del cambio de estado, es la plataforma la que dice
// qué se puede hacer, y el BFF solo lo traduce a nombres de negocio.
type NotEditableError struct {
	Status     string   `json:"status"`
	EditableIn []string `json:"editable_in"`
}

func (e *NotEditableError) Error() string {
	return fmt.Sprintf("apiclient: una solicitud en %q no admite edición manual", e.Status)
}

// NotEditableOf extrae el rechazo por estado no editable de un error (nil, false si no
// lo es).
func NotEditableOf(err error) (*NotEditableError, bool) {
	var notEditable *NotEditableError
	if errors.As(err, &notEditable) {
		return notEditable, true
	}
	return nil, false
}

// replaceIntakeItemsRequest es el cuerpo de PUT /api/v1/intakes/{id}/items: el
// conjunto COMPLETO de líneas de cliente que debe quedar.
type replaceIntakeItemsRequest struct {
	Items []IntakeItem `json:"items"`
}

// InvalidTransitionError es el 422 de POST /api/v1/intakes/{id}/status: la
// transición pedida no existe en el ciclo de vida. Trae dónde está la solicitud
// AHORA y adónde sí puede ir, que es lo único con lo que el operador puede corregir
// sin adivinar.
type InvalidTransitionError struct {
	Status    string   `json:"status"`
	Requested string   `json:"requested"`
	Allowed   []string `json:"allowed"`
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("apiclient: transición inválida de %q a %q", e.Status, e.Requested)
}

// InvalidTransitionOf extrae el rechazo de transición de un error (nil, false si no
// lo es).
func InvalidTransitionOf(err error) (*InvalidTransitionError, bool) {
	var invalid *InvalidTransitionError
	if errors.As(err, &invalid) {
		return invalid, true
	}
	return nil, false
}

// IntakesClient maneja la bandeja de solicitudes contra la API pública. Todas sus
// rutas exigen el Context Token y la feature `cart_basic`: sin ella la plataforma
// corta con 403 y `{"error":"feature_not_enabled"}`, que es la autoridad real — el
// gate de la plantilla solo decide qué se pinta.
type IntakesClient struct {
	t *Transport
}

// NewIntakesClient construye un IntakesClient acoplado a un Transport.
func NewIntakesClient(t *Transport) *IntakesClient {
	return &IntakesClient{t: t}
}

// ListIntakes lista las solicitudes del tenant del token vía GET /api/v1/intakes.
func (c *IntakesClient) ListIntakes(ctx context.Context, accessToken string, f IntakeFilter) (*IntakePage, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/intakes"+f.query(), nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: intakes: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// El 400 es el único fallo del listado con un motivo accionable: el filtro venía
		// mal (una fecha ilegible, un estado que no existe).
		return nil, reasonedStatusError("intakes", resp, http.StatusBadRequest)
	}
	var out IntakePage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: intakes: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// GetIntake devuelve la solicitud {id} con sus líneas vía GET /api/v1/intakes/{id}.
// Una solicitud de otro tenant responde 404 (opaco, INV-8), no 403.
func (c *IntakesClient) GetIntake(ctx context.Context, accessToken, id string) (*IntakeDetail, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/intakes/"+url.PathEscape(id), nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: intake: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("intake", resp.StatusCode)
	}
	var out IntakeDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: intake: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// SetIntakeStatus aplica una transición del ciclo de vida vía
// POST /api/v1/intakes/{id}/status y devuelve la solicitud ya transicionada.
//
// El 422 se traduce a *InvalidTransitionError CON los destinos permitidos en vez de
// a un error opaco: es la única respuesta de la API que hoy publica el ciclo de
// vida, y tirarla dejaría al operador probando estados a ciegas. El 409 (otro
// operador se adelantó) y el 404 salen como *APIError y los distingue StatusCodeOf.
func (c *IntakesClient) SetIntakeStatus(ctx context.Context, accessToken, id, status string) (*Intake, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost,
		"/api/v1/intakes/"+url.PathEscape(id)+"/status",
		setIntakeStatusRequest{Status: status}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: intake status: %w", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var invalid InvalidTransitionError
		if err := json.NewDecoder(resp.Body).Decode(&invalid); err != nil {
			return nil, fmt.Errorf("apiclient: intake status: decodificar 422: %w", err)
		}
		return nil, &invalid
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("intake status", resp.StatusCode)
	}

	var out Intake
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: intake status: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// setIntakeStatusRequest es el cuerpo de POST /api/v1/intakes/{id}/status.
type setIntakeStatusRequest struct {
	Status string `json:"status"`
}

// ReplaceIntakeItems SUSTITUYE las líneas de cliente de una solicitud por las que
// manda el dueño, vía PUT /api/v1/intakes/{id}/items, y devuelve el detalle YA
// actualizado (Plan 041 · T4.10).
//
// Es un REEMPLAZO del conjunto entero, no tres operaciones: añadir, quitar y
// corregir se expresan mandando la lista que debe quedar. Así lo publica la
// plataforma y así se consume; inventar aquí un `AddItem`/`RemoveItem` de
// conveniencia haría creer que existe una operación por línea que el contrato no
// tiene —y no puede tener, porque dos líneas comparten sku y solo se distinguen por
// la personalización—.
//
// La LÍNEA DE ENVÍO (`_shipping`) NO va en `items` y no se toca: es de la
// plataforma, sobrevive a la edición y sigue contando en el total. Un sku con el
// prefijo reservado en la entrada lo rechaza la plataforma con un defecto de línea,
// así que por esta puerta no se puede duplicar ni pisar.
//
// La respuesta es el detalle COMPLETO —el mismo cuerpo que el GET, con la revisión
// `corrected` recién escrita ya dentro—, y por eso el llamante repinta con lo que
// devuelve el PUT en vez de encadenar un segundo GET: entre los dos viajes cabe la
// edición de otro operador, y repintar con esa lectura enseñaría un estado que nadie
// pidió.
//
// Errores: *InvalidItemsError (líneas mal formadas, con la lista entera),
// *NotEditableError (422, con los estados desde los que sí se edita), *RejectionError
// para el resto de 400 con motivo (cuerpo ilegible, demasiadas líneas) y *APIError
// para 403/404/409/5xx, que StatusCodeOf distingue.
func (c *IntakesClient) ReplaceIntakeItems(ctx context.Context, accessToken, id string, items []IntakeItem) (*IntakeDetail, error) {
	// La lista vacía viaja como `[]`, NUNCA como `null`: quitar todas las líneas es una
	// edición legítima, y la plataforma distingue las dos a propósito —`null` es «no
	// mandaste la clave» y lo contesta con un 400—. Un `[]IntakeItem` nil serializaría
	// a `null` y convertiría ese vaciado en un error incomprensible.
	if items == nil {
		items = []IntakeItem{}
	}

	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPut,
		"/api/v1/intakes/"+url.PathEscape(id)+"/items",
		replaceIntakeItemsRequest{Items: items}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: intake items: %w", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, replaceIntakeItemsError("intake items", resp)
	}

	var out IntakeDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: intake items: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// replaceIntakeItemsError traduce un no-2xx de la edición manual. Lee el cuerpo UNA
// vez y decide con él, porque los dos rechazos del 400 comparten status y solo se
// distinguen por la forma: las líneas inválidas traen la lista de defectos, el resto
// (cuerpo ilegible, tope de líneas) un motivo suelto.
func replaceIntakeItemsError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error      string       `json:"error"`
		Errors     []ItemDefect `json:"errors"`
		Status     string       `json:"status"`
		EditableIn []string     `json:"editable_in"`
	}
	// Un cuerpo ilegible deja la lista vacía y el motivo en blanco: el status sigue
	// siendo la información principal y el llamante tiene su texto genérico.
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxIntakeItemsErrorBody)).Decode(&body)

	switch resp.StatusCode {
	case http.StatusUnprocessableEntity:
		return &NotEditableError{Status: body.Status, EditableIn: body.EditableIn}
	case http.StatusBadRequest:
		if len(body.Errors) > 0 {
			return &InvalidItemsError{Defects: body.Errors}
		}
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
	}
	return statusError(op, resp.StatusCode)
}
