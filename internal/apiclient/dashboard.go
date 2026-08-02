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
