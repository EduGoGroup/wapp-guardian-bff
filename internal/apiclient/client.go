// Package apiclient es el cliente HTTP server-to-server del BFF contra la API pública REST de la
// plataforma wApp (:8103 /api/v1). Custodia el JWT server-side: el BFF añade `Authorization: Bearer <jwt>`
// en cada request de negocio; el navegador NUNCA ve el token (INV-4). NO habla gRPC con el Gateway/Edge ni
// toca material criptográfico (zero-knowledge). A diferencia de edugo-messaging-web NO hay StreamRelay del
// QR (el emparejamiento es local en el Edge), así que este cliente es puramente request/response.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout acota cada llamada a la API (anti-cuelgue). El BFF no tiene endpoints long-lived.
const defaultTimeout = 15 * time.Second

// Client relaya contra la API pública. BaseURL es la raíz sin barra final (p. ej. http://localhost:8103);
// las rutas se concatenan como BaseURL + "/api/v1/...". HTTPClient lleva un timeout acotado.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New construye el cliente con un http.Client de timeout por defecto (15 s).
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: defaultTimeout},
	}
}

// IdentityContext es el contexto de identidad que la API devuelve en el login (tenant/usuario/roles). El
// tenant sale del token (INV-8); el BFF no lo pide por separado.
type IdentityContext struct {
	TenantID string   `json:"tenant_id"`
	UserID   string   `json:"user_id"`
	Roles    []string `json:"roles"`
}

// AuthResult es el wire format de /api/v1/auth/{login,refresh}: el par de tokens + contexto, TODO en el
// body. El BFF lo custodia en la cookie HttpOnly; nunca lo expone al navegador.
type AuthResult struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	TokenType    string          `json:"token_type"`
	ExpiresAt    string          `json:"expires_at"`
	Context      IdentityContext `json:"context"`
}

// ErrUnauthorized señala un 401 de la API (credenciales inválidas o token expirado). Los handlers lo
// distinguen con errors.Is para (REQ-C3) repintar el login y (REQ-C6) intentar el refresh + reintento.
var ErrUnauthorized = errors.New("apiclient: no autorizado")

// APIError es un fallo de transporte con el status HTTP del upstream. No arrastra el cuerpo crudo de la API
// (los handlers deciden qué mensaje mostrar, sin filtrar detalle al usuario).
type APIError struct {
	Op         string
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("apiclient: %s devolvió status %d", e.Op, e.StatusCode)
}

// statusError traduce un status no-2xx a un error tipado. 401 se envuelve en ErrUnauthorized para que
// errors.Is lo detecte aguas arriba; el resto va como *APIError con el código.
func statusError(op string, status int) error {
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%s: %w", op, ErrUnauthorized)
	}
	return &APIError{Op: op, StatusCode: status}
}

// ---------------------------------------------------------------------------
// DTOs de request (wire format estable de /api/v1/auth)
// ---------------------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// ---------------------------------------------------------------------------
// Endpoints de autenticación
// ---------------------------------------------------------------------------

// Login autentica email+password contra POST /api/v1/auth/login (REQ-C1). Devuelve el par de tokens +
// contexto. 401 → ErrUnauthorized (credenciales); otros no-2xx → *APIError.
func (c *Client) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	return c.doAuth(req, "login")
}

// Refresh rota el refresh token y emite un access nuevo vía POST /api/v1/auth/refresh (REQ-C6). 401 →
// ErrUnauthorized (refresh inválido/expirado → forzar logout).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/refresh",
		refreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, err
	}
	return c.doAuth(req, "refresh")
}

// Logout invalida la sesión en la API (REQ-C5, best-effort). Envía el refresh_token en el body y el
// access_token como Bearer. Un error aquí NO debe impedir el logout local: los handlers borran la cookie
// pase lo que pase.
func (c *Client) Logout(ctx context.Context, accessToken, refreshToken string) error {
	req, err := c.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/auth/logout",
		logoutRequest{RefreshToken: refreshToken}, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: logout: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("logout", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Endpoints de negocio (T3: dashboard de sesiones + envío)
// ---------------------------------------------------------------------------

// Session es una fila del listado GET /api/v1/sessions. Espeja el sessionDTO de la API pública
// (internal/publicapi/sessions.go): SOLO metadatos de operación, jamás credenciales ni PII más allá del
// número propio (SelfPn). Los campos opcionales se omiten si la API aún no los conoce. State ∈
// online|offline|loggedout; Role ∈ bot|passive.
type Session struct {
	SessionID       string `json:"session_id"`
	EdgeID          string `json:"edge_id"`
	State           string `json:"state"`
	Role            string `json:"role"`
	SelfPn          string `json:"self_pn,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastSeenAt      string `json:"last_seen_at,omitempty"`
}

// ListSessions lista las sesiones/teléfonos del tenant del token vía GET /api/v1/sessions (REQ-D1). El
// aislamiento por tenant lo garantiza la API (sale del Bearer, INV-8): el BFF no filtra. 401 →
// ErrUnauthorized (el llamador puede refrescar y reintentar); otros no-2xx → *APIError.
func (c *Client) ListSessions(ctx context.Context, accessToken string) ([]Session, error) {
	req, err := c.newAuthedRequest(ctx, http.MethodGet, "/api/v1/sessions", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// sendMessageRequest es el cuerpo JSON de POST /api/v1/messages (wire format estable de la API pública,
// internal/publicapi/messages.go): los tres campos son requeridos. El tenant NO viaja aquí: sale del token.
type sendMessageRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
	Text      string `json:"text"`
}

// SendResult refleja la respuesta 200 de POST /api/v1/messages (el Ack del Edge). OK=false significa que el
// Edge recibió el comando pero su ejecución falló (Error trae el detalle del Edge). El BFF decide qué
// mostrar sin filtrar trazas internas (REQ-D3).
type SendResult struct {
	AckedCommandID string `json:"acked_command_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// SendMessage envía un texto por una sesión del Edge vía POST /api/v1/messages (REQ-D2). En 200 devuelve el
// *SendResult con el Ack (incluso ok=false). Los códigos de negocio (400/404/502/504/500) vuelven como
// *APIError con su StatusCode para que el handler los mapee a un mensaje legible (REQ-D3); 401 →
// ErrUnauthorized (refresh + reintento, REQ-C6).
func (c *Client) SendMessage(ctx context.Context, accessToken, sessionID, to, text string) (*SendResult, error) {
	req, err := c.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/messages",
		sendMessageRequest{SessionID: sessionID, To: to, Text: text}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
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

// StatusCodeOf extrae el status HTTP del upstream de un error de *APIError (0 si no lo es). Los handlers
// lo usan para mapear 400/404/502/504/500 a mensajes legibles sin acoplarse al tipo concreto.
func StatusCodeOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// ---------------------------------------------------------------------------
// Helpers de transporte (los reusan T3/T4 para las llamadas de negocio)
// ---------------------------------------------------------------------------

// newRequest arma una petición JSON SIN autenticación (login/refresh establecen la sesión). El cuerpo ya
// serializado se pasa como bytes; nil deja un body vacío.
func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return nil, fmt.Errorf("apiclient: construir petición %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// newAuthedRequest es newRequest + `Authorization: Bearer <accessToken>`. Es el punto único donde el JWT se
// añade a una petición: T3/T4 lo reusan para cada llamada de negocio (el token viaja server-side, jamás al
// navegador).
func (c *Client) newAuthedRequest(ctx context.Context, method, path string, body []byte, accessToken string) (*http.Request, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

// newJSONRequest serializa payload a JSON y delega en newRequest.
func (c *Client) newJSONRequest(ctx context.Context, method, path string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("apiclient: serializar %s: %w", path, err)
	}
	return c.newRequest(ctx, method, path, body)
}

// newAuthedJSONRequest serializa payload a JSON y delega en newAuthedRequest.
func (c *Client) newAuthedJSONRequest(ctx context.Context, method, path string, payload any, accessToken string) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("apiclient: serializar %s: %w", path, err)
	}
	return c.newAuthedRequest(ctx, method, path, body, accessToken)
}

// doAuth ejecuta una petición de autenticación y decodifica el AuthResult del body.
func (c *Client) doAuth(req *http.Request, op string) (*AuthResult, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s: %w", op, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(op, resp.StatusCode)
	}
	var out AuthResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("apiclient: %s: respuesta sin access_token", op)
	}
	return &out, nil
}

// drainClose vacía y cierra el body para poder reutilizar la conexión keep-alive.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
