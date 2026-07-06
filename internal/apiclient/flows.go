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

// ---------------------------------------------------------------------------
// Flows (menú/encuesta) — INMUTABLES versionados (REQ-E1/E2)
// ---------------------------------------------------------------------------
//
// La API pública NO tiene edición in-place ni PUT/DELETE de flujos: la unidad
// persistida es (tenant_id, flow_id, version) y "editar" = publicar una versión
// nueva (design §6). Este cliente refleja ese contrato tal cual: listar, ver y
// publicar. El aislamiento por tenant lo garantiza la API (sale del Bearer,
// INV-8); el BFF no filtra.

// FlowSummary es una fila del listado GET /api/v1/flows. Espeja el flowSummaryDTO
// de la API pública (internal/publicapi/flows.go): solo metadatos de la versión
// vigente, sin la definición completa.
type FlowSummary struct {
	FlowID    string `json:"flow_id"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ListFlows lista los flujos del tenant del token (última versión de cada uno) vía
// GET /api/v1/flows (REQ-E1). 401 → ErrUnauthorized (refresh + reintento); otros
// no-2xx → *APIError.
func (c *Client) ListFlows(ctx context.Context, accessToken string) ([]FlowSummary, error) {
	req, err := c.newAuthedRequest(ctx, http.MethodGet, "/api/v1/flows", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// GetFlow devuelve la definición vigente (última versión) del flujo {id} como JSON
// crudo (el objeto model.Flow {flow_id, version, initial, nodes}) para pintarlo en
// el <textarea> del editor (REQ-E1). Se devuelve el body sin re-serializar para no
// perder ni reordenar campos. 404 → *APIError (flujo inexistente o de otro tenant,
// opaco); 401 → ErrUnauthorized.
func (c *Client) GetFlow(ctx context.Context, accessToken, id string) (json.RawMessage, error) {
	req, err := c.newAuthedRequest(ctx, http.MethodGet, "/api/v1/flows/"+id, nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// publishFlowRequest es el cuerpo JSON de POST /api/v1/flows: el objeto-flujo crudo
// va ANIDADO en {definition} (definitionRequest de la API, internal/flujos/admin/
// handlers.go), NO en la raíz. El tenant NO viaja aquí: sale del token (INV-8).
type publishFlowRequest struct {
	Definition json.RawMessage `json:"definition"`
}

// PublishFlowResult refleja la respuesta 201 de POST /api/v1/flows: el flow_id
// publicado y la versión que el repositorio asignó (versionado, design §6).
type PublishFlowResult struct {
	FlowID  string `json:"flow_id"`
	Version int    `json:"version"`
}

// PublishFlow publica una definición como versión NUEVA vía POST /api/v1/flows
// (REQ-E2). flowJSON es el objeto model.Flow crudo que el operador editó (el
// handler ya validó que sea JSON parseable, REQ-E4); aquí se envuelve en
// {definition}. En 201 devuelve el *PublishFlowResult. Un 4xx de validación de la
// plataforma (p.ej. nodos inválidos) vuelve como *RejectionError con el mensaje de la
// API (contenido propio del operador, seguro de mostrar, REQ-E4); 401 →
// ErrUnauthorized; 5xx → *APIError genérico.
func (c *Client) PublishFlow(ctx context.Context, accessToken string, flowJSON []byte) (*PublishFlowResult, error) {
	req, err := c.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/flows",
		publishFlowRequest{Definition: json.RawMessage(flowJSON)}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// ---------------------------------------------------------------------------
// RejectionError: 4xx de un endpoint de escritura cuyo cuerpo SÍ es seguro mostrar
// ---------------------------------------------------------------------------

// RejectionError es un rechazo 4xx (≠401) de un endpoint de escritura del editor
// (publicar flow / crear trigger) cuyo mensaje de la API describe el problema del
// CONTENIDO PROPIO del operador (p.ej. "definición de flujo inválida: …", "keyword
// es requerido"). A diferencia de *APIError —que oculta el cuerpo del upstream para
// no filtrar trazas internas— aquí el mensaje es del propio tenant y ayudarle a
// corregir es el objetivo (REQ-E4). Se acota en longitud para no volcar cuerpos
// enormes. html/template lo auto-escapa al pintarlo, así que es inerte.
type RejectionError struct {
	Op         string
	StatusCode int
	Message    string
}

func (e *RejectionError) Error() string {
	return fmt.Sprintf("apiclient: %s rechazado (%d): %s", e.Op, e.StatusCode, e.Message)
}

// maxRejectionBody acota el mensaje de rechazo que se muestra al operador.
const maxRejectionBody = 500

// writeStatusError traduce una respuesta no-esperada de un endpoint de escritura:
// 401 → ErrUnauthorized (refresh + reintento). 4xx (400/404/409/…) → *RejectionError
// con el cuerpo de la API (mensaje del contenido propio del operador). 5xx u otros
// → *APIError genérico (sin filtrar cuerpo). Consume el body para poder reutilizar
// la conexión.
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

// RejectionMessageOf extrae el mensaje mostrable de un *RejectionError (msg, true) o
// devuelve ("", false) si el error no lo es. Los handlers lo usan para pintar el
// rechazo de la plataforma sin acoplarse al tipo concreto.
func RejectionMessageOf(err error) (string, bool) {
	var rej *RejectionError
	if errors.As(err, &rej) {
		return rej.Message, true
	}
	return "", false
}
