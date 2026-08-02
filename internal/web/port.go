package web

import (
	"context"
	"encoding/json"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// Authenticator define el contrato para la autenticación contra la API pública.
type Authenticator interface {
	Login(ctx context.Context, email, password string) (*apiclient.AuthResult, error)
	Refresh(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
	Logout(ctx context.Context, accessToken, refreshToken string) error
}

// SessionManager define el contrato para administrar sesiones del tenant y envío de mensajes.
type SessionManager interface {
	ListSessions(ctx context.Context, accessToken string) ([]apiclient.Session, error)
	SetSessionRole(ctx context.Context, accessToken, sessionID, role string) error
	SendMessage(ctx context.Context, accessToken, sessionID, to, text string) (*apiclient.SendResult, error)
}

// EditorManager define el contrato para la edición de flujos y la gestión de reglas de disparo.
type EditorManager interface {
	ListFlows(ctx context.Context, accessToken string) ([]apiclient.FlowSummary, error)
	GetFlow(ctx context.Context, accessToken, id string) (json.RawMessage, error)
	PublishFlow(ctx context.Context, accessToken string, flowJSON []byte) (*apiclient.PublishFlowResult, error)
	ListTriggers(ctx context.Context, accessToken string) ([]apiclient.Trigger, error)
	CreateTrigger(ctx context.Context, accessToken string, tr apiclient.CreateTriggerRequest) (*apiclient.Trigger, error)
	DeleteTrigger(ctx context.Context, accessToken, id string) error
}

// APIPort es el puerto compuesto por compatibilidad con el cliente unificado de la API pública.
type APIPort interface {
	Authenticator
	SessionManager
	EditorManager
}

// Verificación en compilación de que los clientes concretos satisfacen las interfaces segregadas.
var (
	_ Authenticator  = (*apiclient.AuthClient)(nil)
	_ SessionManager = (*apiclient.DashboardClient)(nil)
	_ EditorManager  = (*apiclient.EditorClient)(nil)
	_ APIPort        = (*apiclient.Client)(nil)
)
