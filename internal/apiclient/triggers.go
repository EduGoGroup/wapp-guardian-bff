package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// Triggers (reglas de disparo) — crear/listar/borrar (REQ-E3)
// ---------------------------------------------------------------------------
//
// La API pública NO tiene edición in-place de triggers: "editar" = borrar + crear
// (design §6). Este cliente refleja el CRUD parcial real: listar, crear, borrar.

// Trigger es una regla de disparo tal como la devuelve la API (triggerDTO de
// internal/flujos/admin/triggers.go). kind ∈ {keyword, fallback, escape};
// match_type ∈ {exact, contains}. Los campos opcionales se omiten según kind
// (fallback no tiene keyword; escape no tiene flow_id).
type Trigger struct {
	TriggerID string `json:"trigger_id"`
	Kind      string `json:"kind"`
	Keyword   string `json:"keyword,omitempty"`
	MatchType string `json:"match_type"`
	FlowID    string `json:"flow_id,omitempty"`
	Priority  int    `json:"priority"`
	Enabled   bool   `json:"enabled"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// CreateTriggerRequest es el cuerpo de POST /api/v1/triggers (espeja el
// triggerRequest de la API). El tenant NO viaja aquí: sale del token (INV-8). No se
// expone `enabled`: el BFF crea reglas activas por defecto (la API lo trata como
// true cuando se omite). Los campos vacíos se omiten para que la API aplique sus
// defaults (p.ej. match_type→exact).
type CreateTriggerRequest struct {
	Kind      string `json:"kind"`
	Keyword   string `json:"keyword,omitempty"`
	MatchType string `json:"match_type,omitempty"`
	FlowID    string `json:"flow_id,omitempty"`
	Priority  int    `json:"priority"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// ListTriggers lista las reglas de disparo del tenant del token vía
// GET /api/v1/triggers (REQ-E3). 401 → ErrUnauthorized; otros no-2xx → *APIError.
func (c *Client) ListTriggers(ctx context.Context, accessToken string) ([]Trigger, error) {
	req, err := c.newAuthedRequest(ctx, http.MethodGet, "/api/v1/triggers", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// CreateTrigger crea una regla de disparo vía POST /api/v1/triggers (REQ-E3). En
// 201 devuelve el *Trigger creado. Un 400 de validación de la plataforma (p.ej.
// "keyword es requerido") vuelve como *RejectionError con el mensaje de la API
// (REQ-E4); 401 → ErrUnauthorized; 5xx → *APIError genérico.
func (c *Client) CreateTrigger(ctx context.Context, accessToken string, tr CreateTriggerRequest) (*Trigger, error) {
	req, err := c.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/triggers", tr, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// DeleteTrigger borra la regla {id} del tenant del token vía
// DELETE /api/v1/triggers/{id} (REQ-E3). 204 → nil. 404 (id inexistente o de otro
// tenant, opaco) → *APIError; 401 → ErrUnauthorized.
func (c *Client) DeleteTrigger(ctx context.Context, accessToken, id string) error {
	req, err := c.newAuthedRequest(ctx, http.MethodDelete, "/api/v1/triggers/"+id, nil, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: delete trigger: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return statusError("delete trigger", resp.StatusCode)
	}
	return nil
}
