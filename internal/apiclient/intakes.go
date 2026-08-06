package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type Intake struct {
	ID        string  `json:"id"`
	ContactID string  `json:"contact_id"`
	SessionID string  `json:"session_id"`
	Status    string  `json:"status"`
	Total     float64 `json:"total"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
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
//   - `nil` (la plataforma NO manda el campo): no se sabe. Hoy es el caso real —el
//     detalle todavía no lo publica y solo viaja dentro del 422—, así que la
//     pantalla lo declara en vez de fingir un estado terminal.
//   - lista vacía (la plataforma manda `[]`): estado TERMINAL, no admite cambios.
//
// El campo NO trae `revisions`: esa tabla nace en la Ola 4 y la plataforma la omite
// a propósito para no afirmar «no hay revisiones» cuando la verdad es «todavía no
// se registran».
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
