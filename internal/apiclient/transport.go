package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// defaultTimeout acota cada llamada a la API (anti-cuelgue).
const defaultTimeout = 15 * time.Second

// Transport maneja la configuración base HTTP y las peticiones primitivas.
//
// 🔴 AQUÍ HUBO UN SEGUNDO CLIENTE HTTP, `InferenceHTTPClient` (55s), hasta el Plan 047 · T7.7. Existía
// porque `http.Client.Timeout` NO se puede sobrescribir por petición —es un campo del cliente, no del
// request, y ante un contexto más largo gana siempre el menor—, así que la ÚNICA llamada del BFF que
// esperaba a que un modelo redactara (`SuggestIntakeQuote`) necesitaba otro cliente para no darle su
// plazo a todas las demás. Esa llamada se mudó a la consola del cliente con la bandeja, y con ella se
// fueron el campo, su `Option` constructora y el test de AST que vigilaba que el uso siguiera siendo
// uno solo. El razonamiento sigue siendo válido el día que vuelva a haber una llamada así: no es que
// el segundo cliente fuera un error, es que se quedó sin llamante.
type Transport struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewTransport construye un Transport con el timeout predeterminado (15s).
func NewTransport(baseURL string) *Transport {
	return &Transport{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: defaultTimeout},
	}
}

// ErrUnauthorized señala un 401 de la API (credenciales inválidas o token expirado).
var ErrUnauthorized = errors.New("apiclient: no autorizado")

// APIError es un fallo de transporte con el status HTTP del upstream.
type APIError struct {
	Op         string
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("apiclient: %s devolvió status %d", e.Op, e.StatusCode)
}

// statusError traduce un status no-2xx a un error tipado. 401 se envuelve en ErrUnauthorized.
func statusError(op string, status int) error {
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%s: %w", op, ErrUnauthorized)
	}
	return &APIError{Op: op, StatusCode: status}
}

// RejectionError es un rechazo 4xx (≠401) de un endpoint de escritura que trae un MOTIVO mostrable.
//
// 🔴 Vive aquí y no en el fichero de una pantalla concreta. Nació con el editor de flujos
// (`apiclient/editor.go`), pero cuando ese fichero se retiró con las pantallas —Plan 047 · T6.6— se
// vio que el tipo lo construyen `reasonedStatusError` (justo debajo) y seis clientes de dominio, y lo
// consultan una docena de handlers: era infraestructura de transporte alojada de prestado. Un
// símbolo compartido no se guarda en el fichero de su primer usuario, porque el día que ese usuario
// se va parece que se puede borrar con él.
type RejectionError struct {
	Op         string
	StatusCode int
	Message    string
}

func (e *RejectionError) Error() string {
	return fmt.Sprintf("apiclient: %s rechazado (%d): %s", e.Op, e.StatusCode, e.Message)
}

// maxRejectionBody acota cuánto cuerpo del upstream se lee para componer el motivo: lo que acaba en
// pantalla no lo dimensiona el que responde.
const maxRejectionBody = 500

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

// reasonedStatusError traduce un no-2xx conservando el MOTIVO que manda la API (`{"error":"…"}`) solo
// para los códigos indicados, y dejando el resto como *APIError legible por StatusCodeOf.
//
// La distinción no es cosmética: hay rechazos cuyo cuerpo es la única forma de que el operador sepa
// qué corregir (un filtro mal escrito, una clave demasiado larga), y otros —403, 404, 5xx— donde el
// código ya lo dice todo y el cuerpo del upstream no debe acabar en pantalla.
func reasonedStatusError(op string, resp *http.Response, reasoned ...int) error {
	if !slices.Contains(reasoned, resp.StatusCode) {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	// Un cuerpo ilegible deja el mensaje vacío y el llamante usa su texto genérico: el código HTTP
	// sigue siendo la información principal.
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxRejectionBody)).Decode(&body)
	return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
}

// StatusCodeOf extrae el status HTTP del upstream de un error de *APIError (0 si no lo es).
func StatusCodeOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func (t *Transport) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.BaseURL+path, r)
	if err != nil {
		return nil, fmt.Errorf("apiclient: construir petición %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (t *Transport) newAuthedRequest(ctx context.Context, method, path string, body []byte, accessToken string) (*http.Request, error) {
	req, err := t.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

func (t *Transport) newJSONRequest(ctx context.Context, method, path string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("apiclient: serializar %s: %w", path, err)
	}
	return t.newRequest(ctx, method, path, body)
}

func (t *Transport) newAuthedJSONRequest(ctx context.Context, method, path string, payload any, accessToken string) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("apiclient: serializar %s: %w", path, err)
	}
	return t.newAuthedRequest(ctx, method, path, body, accessToken)
}

func (t *Transport) doAuth(req *http.Request, op string) (*AuthResult, error) {
	resp, err := t.HTTPClient.Do(req)
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

func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
