package apiclient

import (
	"context"
	"fmt"
	"net/http"
)

// IdentityContext es el contexto de identidad que la API devuelve en el login (tenant/usuario/roles).
type IdentityContext struct {
	TenantID string   `json:"tenant_id"`
	UserID   string   `json:"user_id"`
	Roles    []string `json:"roles"`
}

// AuthResult es el wire format de /api/v1/auth/{login,refresh}: par de tokens + contexto.
type AuthResult struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	TokenType    string          `json:"token_type"`
	ExpiresAt    string          `json:"expires_at"`
	Context      IdentityContext `json:"context"`
}

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
		return reasonedStatusError("signup", resp, http.StatusBadRequest, http.StatusTooManyRequests)
	}
	return nil
}
