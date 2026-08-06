package web

import (
	"context"
	"encoding/json"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
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

// EntitlementsReader define el contrato para leer el plan y las features efectivas del tenant del
// token. Va segregado del resto porque no es una operación de negocio: es lo que decide QUÉ se pinta,
// y lo consulta cualquier página que tenga secciones condicionadas por feature.
type EntitlementsReader interface {
	GetEntitlements(ctx context.Context, accessToken string) (*apiclient.Entitlements, error)
}

// DashboardAPI es lo que el dashboard consume: sesiones y envío, más las features efectivas con las
// que decide qué secciones emite.
type DashboardAPI interface {
	SessionManager
	EntitlementsReader
}

// IntakeManager define el contrato de la bandeja de SOLICITUDES (Plan 041 · T1.5): listar con
// filtros, abrir el detalle y mover el estado del ciclo de vida.
//
// Va segregado del resto porque es un frente propio y de pago: sus tres rutas exigen la feature
// `cart_basic` en la plataforma, y la pantalla que lo consume es PROVISIONAL (migra a KMP con los
// planes 045/047, ADR-0035). Cuando esa pantalla muera, esta interfaz se va con ella sin arrastrar
// a nadie.
type IntakeManager interface {
	ListIntakes(ctx context.Context, accessToken string, f apiclient.IntakeFilter) (*apiclient.IntakePage, error)
	GetIntake(ctx context.Context, accessToken, id string) (*apiclient.IntakeDetail, error)
	SetIntakeStatus(ctx context.Context, accessToken, id, status string) (*apiclient.Intake, error)
}

// IntakesAPI es lo que la pantalla de solicitudes consume: la bandeja más las features efectivas,
// que son las que deciden si la sección se emite siquiera.
type IntakesAPI interface {
	IntakeManager
	EntitlementsReader
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
	EntitlementsReader
	EditorManager
	IntakeManager
}

// Verificación en compilación de que los clientes concretos satisfacen las interfaces segregadas.
//
// Los dos clientes completos —el legacy y el delegado— cumplen el MISMO puerto, y de ahí sale que
// encender la delegación no obligue a tocar ni un handler: lo que cambia es quién autentica, no cómo
// se le pide.
var (
	_ Authenticator      = (*apiclient.AuthClient)(nil)
	_ Authenticator      = (*apiclient.DelegatedAuthenticator)(nil)
	_ SessionManager     = (*apiclient.DashboardClient)(nil)
	_ EntitlementsReader = (*apiclient.DashboardClient)(nil)
	_ DashboardAPI       = (*apiclient.DashboardClient)(nil)
	_ EditorManager      = (*apiclient.EditorClient)(nil)
	_ IntakeManager      = (*apiclient.IntakesClient)(nil)
	_ APIPort            = (*apiclient.Client)(nil)
	_ APIPort            = (*apiclient.DelegatedClient)(nil)
)
