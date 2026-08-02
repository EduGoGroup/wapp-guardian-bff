package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// SystemBFF es la clave de ESTA aplicación en el catálogo de identity (`iam.systems`).
//
// El System Gate de identity autoriza aplicaciones, no ecosistemas: la clave es namespaced
// `<ecosistema>.<app>` y "wapp" a secas no es un valor válido. Es un identificador de contrato, no
// configuración de infraestructura —identity conoce esta aplicación con el mismo nombre en todos sus
// ambientes—, así que vive en el binario y lo que sale a env vars es la URL del emisor.
const SystemBFF = "wapp.bff"

// IdentityTokens es la respuesta de `POST /api/v1/auth/{login,refresh}` de identity-api.
//
// El Identity Token responde QUIÉN ERES y no lleva claims de negocio: el tenant no está aquí y no
// puede estarlo (INV-1 de identity). Por eso este par no llega nunca a la cookie tal cual — antes se
// canjea por un Context Token de wApp.
type IdentityTokens struct {
	// SessionID es el UUID de la sesión abierta en identity (la de "wapp.bff").
	SessionID string `json:"session_id"`
	// System es la aplicación de la sesión, tal como la devuelve el emisor.
	System string `json:"system"`
	// IdentityToken es el JWT ES256 firmado por identity-core.
	IdentityToken string `json:"identity_token"`
	// RefreshToken es el refresh opaco: se entrega una vez y rota en cada uso.
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn son los segundos de vida que le quedan al Identity Token.
	ExpiresIn int64 `json:"expires_in"`
}

type identityLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	System   string `json:"system"`
}

type identityRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type identityLogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// IdentityClient habla con identity-api (:8200), el único emisor de Identity Tokens del grupo.
//
// Tiene su propio Transport porque identity es un destino distinto de la plataforma: son dos
// servicios, dos URLs base y dos contratos. Todo lo que sale de aquí son credenciales, así que
// ninguna respuesta se registra: los tokens no aparecen en los logs del BFF.
type IdentityClient struct {
	t      *Transport
	system string
}

// NewIdentityClient construye el cliente de identity para una aplicación concreta del catálogo.
func NewIdentityClient(baseURL, system string) *IdentityClient {
	return &IdentityClient{t: NewTransport(baseURL), system: system}
}

// Login autentica email+password contra `POST /api/v1/auth/login` de identity.
//
// El `system` viaja en el cuerpo y es lo que el System Gate evalúa: un usuario sin acceso a esta
// aplicación recibe 403 con la contraseña CORRECTA, que es un caso distinto del 401 de credenciales
// inválidas y por eso no se colapsan aquí.
func (c *IdentityClient) Login(ctx context.Context, email, password string) (*IdentityTokens, error) {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/login",
		identityLoginRequest{Email: email, Password: password, System: c.system})
	if err != nil {
		return nil, err
	}
	return c.doTokens(req, "identity login")
}

// Refresh rota el refresh opaco contra `POST /api/v1/auth/refresh` y emite un Identity Token nuevo.
//
// El cuerpo NO lleva `system`: la aplicación sale de la fila de la sesión en identity, nunca del
// cliente. Aceptarlo permitiría canjear el refresh de una aplicación por el token de otra.
func (c *IdentityClient) Refresh(ctx context.Context, refreshToken string) (*IdentityTokens, error) {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/refresh",
		identityRefreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, err
	}
	return c.doTokens(req, "identity refresh")
}

// Logout cierra en identity la sesión del refresh presentado vía `POST /api/v1/auth/logout` (204).
//
// Cierra UNA sesión, la de esta aplicación: las de las demás —la del Edge, sin ir más lejos—
// sobreviven. Es el modelo Google, y es la casilla T3.4 de la Ola 3.
//
// No lleva Bearer a propósito: identity resuelve el usuario a partir del refresh, server-side, y
// responde 204 tanto si había sesión que revocar como si no (un 404 sería un oráculo de validez de
// refresh tokens). El Context Token que el BFF custodia no le sirve a identity: lo emitió wApp.
func (c *IdentityClient) Logout(ctx context.Context, refreshToken string) error {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/logout",
		identityLogoutRequest{RefreshToken: refreshToken})
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: identity logout: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("identity logout", resp.StatusCode)
	}
	return nil
}

// doTokens ejecuta la petición y exige que la respuesta traiga el par completo: un login que no
// devuelve refresh dejaría una sesión sin forma de renovarse y se descubriría quince minutos después.
func (c *IdentityClient) doTokens(req *http.Request, op string) (*IdentityTokens, error) {
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s: %w", op, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(op, resp.StatusCode)
	}
	var out IdentityTokens
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	if out.IdentityToken == "" {
		return nil, fmt.Errorf("apiclient: %s: respuesta sin identity_token", op)
	}
	if out.RefreshToken == "" {
		return nil, fmt.Errorf("apiclient: %s: respuesta sin refresh_token", op)
	}
	return &out, nil
}
