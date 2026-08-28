package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-shared/iam"
)

// IdentityContext y AuthResult son los tipos del módulo `wapp-shared/iam`, no una copia con el mismo
// shape: son ALIAS, así que en el ecosistema hay UNA sola definición de cada uno (INV-08) y el resto
// del BFF —el puerto, los handlers y sus tests— sigue nombrándolos por este paquete sin cambiar.
//
// Que sirvan también para el login DIRECTO contra la plataforma (el que no delega en identity, y que
// este paquete conserva) no es una casualidad: el wire format es el mismo en las dos vías, porque la
// vía delegada existe justo para devolver lo que la directa ya devolvía.
type (
	IdentityContext = iam.IdentityContext
	AuthResult      = iam.AuthResult
)

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

// AuthClient maneja las operaciones HTTP de autenticación contra la API.
type AuthClient struct {
	t *Transport
}

// NewAuthClient construye un AuthClient acoplado a un Transport.
func NewAuthClient(t *Transport) *AuthClient {
	return &AuthClient{t: t}
}

// Login autentica email+password contra POST /api/v1/auth/login.
func (c *AuthClient) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	return c.t.doAuth(req, "login")
}

// Refresh rota el refresh token y emite un access nuevo vía POST /api/v1/auth/refresh.
func (c *AuthClient) Refresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/auth/refresh",
		refreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, err
	}
	return c.t.doAuth(req, "refresh")
}

// Logout invalida la sesión en la API vía POST /api/v1/auth/logout.
func (c *AuthClient) Logout(ctx context.Context, accessToken, refreshToken string) error {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost, "/api/v1/auth/logout",
		logoutRequest{RefreshToken: refreshToken}, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: logout: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("logout", resp.StatusCode)
	}
	return nil
}

type signupPayload struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Origin    string `json:"origin"`
}

// Signup registra una solicitud de usuario en la plataforma pública vía POST /api/v1/signup.
func (c *AuthClient) Signup(ctx context.Context, email, password, firstName, lastName, origin string) error {
	req, err := c.t.newJSONRequest(ctx, http.MethodPost, "/api/v1/signup",
		signupPayload{
			Email:     email,
			Password:  password,
			FirstName: firstName,
			LastName:  lastName,
			Origin:    origin,
		})
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: signup: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return signupStatusError(resp)
	}
	return nil
}

// signupStatusError traduce un no-2xx de POST /api/v1/signup a un *RejectionError con el MOTIVO
// legible del cuerpo (A-11). A diferencia de reasonedStatusError (pensado para el envelope JSON
// `{"error":"…"}` que usa el resto de la plataforma, y solo para los status que el llamante marca
// como "con motivo"), este endpoint responde sus errores en TEXTO PLANO vía http.Error (ver
// wapp-cloud-platform/internal/platformadmin/signup.go) para CUALQUIER status —400, 409, 429, 502,
// 503—, y su contrato está en evolución en paralelo (C-03). Por eso se envuelve TODO no-2xx (no solo
// 4xx, a diferencia de writeStatusError) y el cuerpo se decodifica tolerante en decodeSignupErrorBody:
// el llamante (DoSignup) necesita tanto el status real como el motivo para elegir el mensaje amigable
// (409 → ya existe, 503 → no disponible, 429 → rate limit), y sin esto un 503 llegaba como *APIError
// sin mensaje y caía al genérico de "error interno" (A-11).
func signupStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError("signup", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRejectionBody+1))
	return &RejectionError{Op: "signup", StatusCode: resp.StatusCode, Message: decodeSignupErrorBody(raw)}
}

// decodeSignupErrorBody extrae un mensaje legible del cuerpo de error de POST /api/v1/signup. Se
// prueba, en orden: (1) el envelope estándar {"error":{"message":…}} por si el endpoint lo adopta,
// (2) un objeto simple {"message":"…"}, (3) el cuerpo crudo como texto (recortado a maxRejectionBody)
// — nunca se asume un shape concreto. Espejo exacto de decodePlatformError en el Edge
// (edge/wapp-edge-agent/cmd/wapp-ctl/auth.go), para que las dos superficies del signup (BFF y Edge)
// decodifiquen el mismo contrato igual.
func decodeSignupErrorBody(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	var flat struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &flat); err == nil && flat.Message != "" {
		return flat.Message
	}
	msg := strings.TrimSpace(string(raw))
	if len(msg) > maxRejectionBody {
		msg = msg[:maxRejectionBody]
	}
	return msg
}
