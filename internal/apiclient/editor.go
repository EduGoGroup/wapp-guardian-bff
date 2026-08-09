package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// FlowSummary es una fila del listado GET /api/v1/flows.
type FlowSummary struct {
	FlowID    string `json:"flow_id"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at,omitempty"`
}

type publishFlowRequest struct {
	Definition json.RawMessage `json:"definition"`
}

// PublishFlowResult refleja la respuesta 201 de POST /api/v1/flows.
type PublishFlowResult struct {
	FlowID  string `json:"flow_id"`
	Version int    `json:"version"`
}

// Trigger es una regla de disparo tal como la devuelve la API.
type Trigger struct {
	TriggerID string `json:"trigger_id"`
	Kind      string `json:"kind"`
	Keyword   string `json:"keyword,omitempty"`
	// EventKind solo viaja en triggers kind=event_start (Plan 043 · T2.1): qué tipo de evento
	// arranca/conmuta (menu|cart|survey|media). Vacío en el resto de los kinds.
	EventKind string `json:"event_kind,omitempty"`
	MatchType string `json:"match_type"`
	FlowID    string `json:"flow_id,omitempty"`
	Priority  int    `json:"priority"`
	Enabled   bool   `json:"enabled"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// ShadowedByEventList es una marca DERIVADA (D-043.20/REQ-27b, MD-043.11): la plataforma la
	// calcula para triggers kind=fallback cuando, a partir de esta ola, una conversación SIN
	// evento activo la lista de eventos del despachador se ofrece primero y el fallback del
	// tenant ya no suena ahí. El BFF solo la pinta — no la calcula ni la persiste.
	ShadowedByEventList bool `json:"shadowed_by_event_list,omitempty"`
}

// CreateTriggerRequest es el cuerpo de POST /api/v1/triggers.
type CreateTriggerRequest struct {
	Kind    string `json:"kind"`
	Keyword string `json:"keyword,omitempty"`
	// EventKind solo aplica —y solo se envía— cuando Kind es event_start (defensa en
	// profundidad: la plataforma también lo exige y lo rechaza sin él).
	EventKind string `json:"event_kind,omitempty"`
	MatchType string `json:"match_type,omitempty"`
	FlowID    string `json:"flow_id,omitempty"`
	Priority  int    `json:"priority"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// RejectionError es un rechazo 4xx (≠401) de un endpoint de escritura del editor.
type RejectionError struct {
	Op         string
	StatusCode int
	Message    string
}

func (e *RejectionError) Error() string {
	return fmt.Sprintf("apiclient: %s rechazado (%d): %s", e.Op, e.StatusCode, e.Message)
}

const maxRejectionBody = 500

func writeStatusError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: %w", op, ErrUnauthorized)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRejectionBody+1))
		msg := strings.TrimSpace(string(raw))
		if len(msg) > maxRejectionBody {
			msg = msg[:maxRejectionBody]
		}
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: msg}
	}
	return &APIError{Op: op, StatusCode: resp.StatusCode}
}

// RejectionMessageOf extrae el mensaje mostrable de un *RejectionError.
func RejectionMessageOf(err error) (string, bool) {
	var rej *RejectionError
	if errors.As(err, &rej) {
		return rej.Message, true
	}
	return "", false
}

// RejectionOf extrae el rechazo entero (status + mensaje). Hace falta cuando el llamante distingue
// entre varios códigos con motivo —un 400 de forma y un 413 por tamaño piden consejos distintos— y
// no le basta con el texto.
func RejectionOf(err error) (*RejectionError, bool) {
	var rej *RejectionError
	if errors.As(err, &rej) {
		return rej, true
	}
	return nil, false
}

// EditorClient maneja las operaciones de flujos y triggers contra la API.
type EditorClient struct {
	t *Transport
}

// NewEditorClient construye un EditorClient acoplado a un Transport.
func NewEditorClient(t *Transport) *EditorClient {
	return &EditorClient{t: t}
}

// ListFlows lista los flujos del tenant del token vía GET /api/v1/flows.
func (c *EditorClient) ListFlows(ctx context.Context, accessToken string) ([]FlowSummary, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/flows", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: flows: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("flows", resp.StatusCode)
	}
	var out []FlowSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: flows: decodificar respuesta: %w", err)
	}
	return out, nil
}

// GetFlow devuelve la definición vigente del flujo {id} como JSON crudo.
func (c *EditorClient) GetFlow(ctx context.Context, accessToken, id string) (json.RawMessage, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/flows/"+id, nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: flow: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("flow", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("apiclient: flow: leer respuesta: %w", err)
	}
	return json.RawMessage(raw), nil
}

// PublishFlow publica una definición como versión NUEVA vía POST /api/v1/flows.
func (c *EditorClient) PublishFlow(ctx context.Context, accessToken string, flowJSON []byte) (*PublishFlowResult, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/flows",
		publishFlowRequest{Definition: json.RawMessage(flowJSON)}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: publish flow: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, writeStatusError("publish flow", resp)
	}
	var out PublishFlowResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: publish flow: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// ListTriggers lista las reglas de disparo del tenant del token vía GET /api/v1/triggers.
func (c *EditorClient) ListTriggers(ctx context.Context, accessToken string) ([]Trigger, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/triggers", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: triggers: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("triggers", resp.StatusCode)
	}
	var out []Trigger
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: triggers: decodificar respuesta: %w", err)
	}
	return out, nil
}

// CreateTrigger crea una regla de disparo vía POST /api/v1/triggers.
func (c *EditorClient) CreateTrigger(ctx context.Context, accessToken string, tr CreateTriggerRequest) (*Trigger, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/triggers", tr, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: create trigger: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, writeStatusError("create trigger", resp)
	}
	var out Trigger
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: create trigger: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// DeleteTrigger borra la regla {id} del tenant del token vía DELETE /api/v1/triggers/{id}.
func (c *EditorClient) DeleteTrigger(ctx context.Context, accessToken, id string) error {
	req, err := c.t.newAuthedRequest(ctx, http.MethodDelete, "/api/v1/triggers/"+id, nil, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: delete trigger: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return statusError("delete trigger", resp.StatusCode)
	}
	return nil
}
