package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrDualModeOff señala el 503 del canje: la plataforma no tiene construido su verificador de
// Identity Tokens, así que el modo dual está apagado de SU lado y no puede canjear nada.
//
// Se distingue del resto de fallos porque no es una avería: es una configuración incompleta del
// despliegue —el BFF delega pero la plataforma todavía no verifica— y se arregla poniéndole a la
// plataforma su WAPP_IDENTITY_JWKS_URL, no reintentando.
var ErrDualModeOff = errors.New("apiclient: canje no disponible (modo dual apagado en la plataforma)")

// ExchangeResult es la respuesta de `POST /api/v1/auth/exchange`: SOLO el Context Token.
//
// El canje no emite refresh y no es un descuido: el refresh es de identity y vive donde vive la
// sesión. Aquí se renueva presentando otra vez un Identity Token fresco.
type ExchangeResult struct {
	// ContextToken es el JWT de wApp con los grants del negocio (tenant, roles). Es el que la cookie
	// custodia y el único que el BFF vuelve a presentar.
	ContextToken string `json:"context_token"`
	// ExpiresAt es el vencimiento del Context Token en RFC3339. La plataforma lo acota con el del
	// Identity Token que se canjeó: la visa nunca dura más que el pasaporte.
	ExpiresAt string `json:"expires_at"`
}

type exchangeRequest struct {
	IdentityToken string `json:"identity_token"`
}

// ExchangeClient canjea Identity Tokens de identity-core por Context Tokens de wApp.
//
// Comparte el Transport de la plataforma porque el canje ES un endpoint de la plataforma: vive junto
// al resto de `/api/v1/auth`, y quien valida la firma del Identity Token es ella, no el BFF.
type ExchangeClient struct {
	t *Transport
}

// NewExchangeClient construye el cliente del canje sobre el Transport de la plataforma.
func NewExchangeClient(t *Transport) *ExchangeClient {
	return &ExchangeClient{t: t}
}

// Exchange presenta un Identity Token y recibe el Context Token del tenant del usuario.
//
// Los dos errores con nombre del contrato:
//   - **503** — modo dual apagado en la plataforma (ErrDualModeOff).
//   - **401** — el `sub` del Identity Token no corresponde a ningún usuario migrado de wApp
//     (ErrUnauthorized). Es un error explícito y deliberado: no se crea un usuario al vuelo.
func (c *ExchangeClient) Exchange(ctx context.Context, identityToken string) (*ExchangeResult, error) {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/exchange",
		exchangeRequest{IdentityToken: identityToken})
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: exchange: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, ErrDualModeOff
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("exchange", resp.StatusCode)
	}
	var out ExchangeResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: exchange: decodificar respuesta: %w", err)
	}
	if out.ContextToken == "" {
		return nil, errors.New("apiclient: exchange: respuesta sin context_token")
	}
	return &out, nil
}
