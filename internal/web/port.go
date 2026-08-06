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

// TenantVariablesManager define el contrato de las VARIABLES DE EMPRESA (Plan 041 · T2.1): pares
// clave→valor que wApp no interpreta (D-041.1).
//
// Solo hay leer y REEMPLAZAR, y no falta un borrado: el borrado ES el reemplazo sin esa clave. La
// interfaz refleja el contrato tal cual en vez de ofrecer un `Delete` de conveniencia que tendría que
// inventarse por dentro la foto completa —y que dejaría creer al llamante que existe una operación
// por clave que la API no tiene.
type TenantVariablesManager interface {
	GetTenantVariables(ctx context.Context, accessToken string) (*apiclient.TenantVariables, error)
	ReplaceTenantVariables(ctx context.Context, accessToken string, vars map[string]string) (*apiclient.TenantVariables, error)
}

// CatalogImporter define el contrato del IMPORT DE CATÁLOGO (Plan 041 · T3.5): comprobar un
// documento y aplicarlo, más la plantilla de ejemplo que se descarga.
//
// `ImportCatalog` cubre las dos modalidades con un booleano en vez de con dos métodos porque la
// plataforma responde el MISMO objeto en las dos: dos métodos harían creer que hay dos respuestas
// que interpretar, y solo hay una con un `Applied` distinto.
//
// Va segregado del resto por lo mismo que la bandeja: es un frente de pago (feature
// `catalog_import`) y la pantalla que lo consume es PROVISIONAL (migra a KMP con los planes
// 045/047, ADR-0035). Cuando esa pantalla muera, esta interfaz se va con ella.
type CatalogImporter interface {
	ImportCatalog(ctx context.Context, accessToken string, document []byte, apply bool) (*apiclient.CatalogImportResult, error)
	GetCatalogTemplate(ctx context.Context, accessToken, format string) (*apiclient.CatalogTemplate, error)
	GetCatalogPrompt(ctx context.Context, accessToken string) (*apiclient.CatalogPrompt, error)
}

// CatalogImportAPI es lo que la pantalla de import consume: el import más las features efectivas,
// que son las que deciden si la sección se emite siquiera.
type CatalogImportAPI interface {
	CatalogImporter
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
	TenantVariablesManager
	CatalogImporter
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

	_ TenantVariablesManager = (*apiclient.TenantVariablesClient)(nil)
	_ CatalogImporter        = (*apiclient.CatalogImportClient)(nil)
	_ APIPort                = (*apiclient.Client)(nil)
	_ APIPort                = (*apiclient.DelegatedClient)(nil)
)
