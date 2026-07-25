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

// defaultTimeout acota cada llamada a la API (anti-cuelgue).
const defaultTimeout = 15 * time.Second

// Transport maneja la configuración base HTTP y las peticiones primitivas.
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
