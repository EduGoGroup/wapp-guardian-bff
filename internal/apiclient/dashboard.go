package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Session es una fila del listado GET /api/v1/sessions.
type Session struct {
	SessionID       string `json:"session_id"`
	EdgeID          string `json:"edge_id"`
	State           string `json:"state"`
	Role            string `json:"role"`
	SelfPn          string `json:"self_pn,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at,omitempty"`

	// Salud del clasificador de intenciones (Plan 051 · Ola 4 · T4.3). Son los dos únicos
	// campos de salud que esta consola lee: sirven para responder «¿está clasificando?» y
	// «¿se estorban el cajero y Ollama?» SIN ENTRAR EN LA MÁQUINA, que es el criterio de T4.3.
	//
	// 🔴 LOS DOS LLEGAN AUSENTES CUANDO EL EDGE NO LO SABE, y ausente NO es un valor por
	// defecto: la API los marca `omitempty` precisamente porque el Edge manda su cero a
	// propósito cuando el parte del worker-cajero lleva más de 90 s sin refrescarse (cajero
	// muerto, o Edge que no es Linux). Pintar "" como `disjunta` o como `closed` publicaría
	// una salud INVENTADA sobre un clasificador apagado — es el fallo exacto que la Ola 4
	// existe para cerrar. En la vista, vacío se pinta «desconocido» y nunca otra cosa.

	// IntentCircuit es el breaker del clasificador: "closed" | "open" | "half_open".
	IntentCircuit string `json:"intent_circuit,omitempty"`
	// WorkerTaskset es el veredicto del reparto de CPU entre el cajero y Ollama:
	// "disjunta" | "solapada" | "cajero_sin_confinar".
	WorkerTaskset string `json:"worker_taskset,omitempty"`
}

type setSessionRoleRequest struct {
	Role string `json:"role"`
}

type sendMessageRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
	Text      string `json:"text"`
}

// SendResult refleja la respuesta 200 de POST /api/v1/messages.
type SendResult struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// DashboardClient maneja las operaciones de sesiones y mensajes de dashboard contra la API.
type DashboardClient struct {
	t *Transport
}

// NewDashboardClient construye un DashboardClient acoplado a un Transport.
func NewDashboardClient(t *Transport) *DashboardClient {
	return &DashboardClient{t: t}
}

// ListSessions lista las sesiones/teléfonos del tenant del token vía GET /api/v1/sessions.
func (c *DashboardClient) ListSessions(ctx context.Context, accessToken string) ([]Session, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/sessions", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: sessions: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("sessions", resp.StatusCode)
	}
	var out []Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: sessions: decodificar respuesta: %w", err)
	}
	return out, nil
}

// SetSessionRole fija el rol de una sesión vía POST /api/v1/sessions/{id}/role.
func (c *DashboardClient) SetSessionRole(ctx context.Context, accessToken, sessionID, role string) error {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/sessions/"+sessionID+"/role",
		setSessionRoleRequest{Role: role}, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: session role: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return statusError("session role", resp.StatusCode)
	}
	return nil
}

// SendMessage envía un texto por una sesión del Edge vía POST /api/v1/messages.
func (c *DashboardClient) SendMessage(ctx context.Context, accessToken, sessionID, to, text string) (*SendResult, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/messages",
		sendMessageRequest{SessionID: sessionID, To: to, Text: text}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: messages: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("messages", resp.StatusCode)
	}
	var out SendResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: messages: decodificar respuesta: %w", err)
	}
	return &out, nil
}
