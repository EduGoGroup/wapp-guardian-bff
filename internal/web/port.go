package web

import (
	"context"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// Authenticator define el contrato para la autenticación contra la API pública.
type Authenticator interface {
	Login(ctx context.Context, email, password string) (*apiclient.AuthResult, error)
	Refresh(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
	Logout(ctx context.Context, accessToken, refreshToken string) error
	Signup(ctx context.Context, email, password, firstName, lastName, origin string) error
}

// EntitlementsReader define el contrato para leer el plan y las features efectivas del tenant del
// token. Va segregado del resto porque no es una operación de negocio: es lo que decide QUÉ se pinta,
// y lo consulta cualquier página que tenga secciones condicionadas por feature.
type EntitlementsReader interface {
	GetEntitlements(ctx context.Context, accessToken string) (*apiclient.Entitlements, error)
}

// HomeAPI es lo que la PORTADA consume, y hoy es una sola cosa: las features efectivas del tenant.
//
// 📌 Antes se llamaba `DashboardAPI` y componía `SessionManager` + `EntitlementsReader`, porque la
// portada era el dashboard de sesiones. El Plan 047 · T2.1 se llevó las sesiones a la consola del
// cliente, y con ellas la mitad de sesiones de este puerto. Lo que queda es un alias de una sola
// interfaz, y se conserva —en vez de usar `EntitlementsReader` a pelo— porque nombra al CONSUMIDOR:
// cuando la portada vuelva a pedir algo propio, se añade aquí y no en el contrato compartido.
type HomeAPI interface {
	EntitlementsReader
}

// 🔴 AQUÍ ESTUVIERON `IntakeManager` (10 métodos) e `IntakesAPI`, el contrato de la bandeja de
// SOLICITUDES. Se fueron con la pantalla (Plan 047 · T7.7): la bandeja se administra ahora en la
// consola del cliente. Lo que aquí se anticipó —«cuando esa pantalla muera, esta interfaz se va con
// ella sin arrastrar a nadie»— se cumplió: el puerto segregado hizo que la retirada no tocara ni un
// handler ajeno.

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

// IntegrationsManager define el contrato de la CONFIGURACIÓN DEL PUENTE CRM (Plan 042 · T5.2): leer
// la del tenant, guardarla entera y quitarla.
//
// El secreto de firma entra por `SaveIntegration` y NO SALE POR NINGÚN MÉTODO, que es el contrato
// dicho en tipos: `apiclient.Integration` no tiene campo donde ponerlo. Por eso ninguna vista de esta
// consola puede pintarlo aunque quiera —lo que se pinta es `SecretSet` y la huella corta—, y el
// campo vacío del formulario no borra nada: significa «deja el que está» (D-042.7).
//
// Va segregado del resto por lo mismo que la bandeja: es un frente de pago (feature `crm_bridge`,
// que la plataforma exige en los TRES verbos, también el GET). A diferencia de la bandeja, su
// pantalla es PERMANENTE: es capa técnica y no migra (ADR-0035 §3, doc 14 D-03/D-14): se queda en el BFF.
type IntegrationsManager interface {
	GetIntegration(ctx context.Context, accessToken string) (*apiclient.Integration, error)
	SaveIntegration(ctx context.Context, accessToken string, s apiclient.IntegrationSettings) (*apiclient.Integration, error)
	DeleteIntegration(ctx context.Context, accessToken string) error
	// GetOutboxCounters es la otra mitad de la pantalla: la configuración dice a dónde se entrega,
	// esto dice si está llegando. Va en el MISMO contrato porque comparte guardias (scope
	// `integrations.read` + feature `crm_bridge`) y porque es la pregunta que sigue a configurar el
	// puente — separarlo en un puerto propio sugeriría que una pantalla puede tener una sin la otra.
	//
	// Devuelve CONTADORES. No hay método aquí para mirar dentro de una entrega, y no es un hueco por
	// llenar: el contenido de las entregas no se publica por esta puerta.
	GetOutboxCounters(ctx context.Context, accessToken string) (*apiclient.OutboxCounters, error)
}

// IntegrationsAPI es lo que la pantalla de integraciones consume: el CRUD del puente más las features
// efectivas, que son las que deciden si la sección se emite siquiera.
type IntegrationsAPI interface {
	IntegrationsManager
	EntitlementsReader
}

// TenantLLMManager define el contrato de la CONFIGURACIÓN LLM DEL TENANT (Plan 047 · T3.4, sobre la
// API que el Plan 044 dejó construida): leer la del tenant, guardarla entera y quitarla.
//
// 🔴 LA CREDENCIAL ENTRA POR `SaveTenantLLM` Y NO SALE POR NINGÚN MÉTODO, y eso es el contrato dicho
// en tipos: `apiclient.TenantLLM` no tiene campo donde ponerla. Por eso ninguna vista de esta consola
// puede pintarla aunque quiera —lo que se pinta es `KeySet`, un booleano— y por eso el criterio «la
// clave nunca se re-pinta en el HTML» se cumple por construcción y no por disciplina.
//
// 🔴 Y AQUÍ NO HAY «DEJA LA QUE ESTÁ», al revés que en IntegrationsManager: la plataforma exige la
// clave en CADA PUT de la vía `api` porque el PUT es un reemplazo completo. La pantalla no disimula la
// consecuencia (hay que volver a teclearla); avisarlo es más honesto que inventar aquí una semántica
// que el cloud no tiene.
//
// Va segregado del resto por lo mismo que la bandeja: es un frente de pago (feature `api_llm`, que la
// plataforma exige en los TRES verbos, también el GET). A diferencia de la bandeja, su pantalla es
// PERMANENTE: es capa técnica y no migra (ADR-0035 §3, D-047.5/D-047.9): se queda en el BFF.
type TenantLLMManager interface {
	GetTenantLLM(ctx context.Context, accessToken string) (*apiclient.TenantLLM, error)
	SaveTenantLLM(ctx context.Context, accessToken string, s apiclient.TenantLLMSettings) (*apiclient.TenantLLM, error)
	DeleteTenantLLM(ctx context.Context, accessToken string) error
}

// TenantLLMAPI es lo que la pantalla de configuración LLM consume: el CRUD más las features efectivas,
// que son las que deciden si la sección se emite siquiera.
type TenantLLMAPI interface {
	TenantLLMManager
	EntitlementsReader
}

// APIPort es el puerto compuesto por compatibilidad con el cliente unificado de la API pública.
type APIPort interface {
	Authenticator
	EntitlementsReader
	TenantVariablesManager
	IntegrationsManager
	TenantLLMManager
}

// Verificación en compilación de que los clientes concretos satisfacen las interfaces segregadas.
//
// Los dos clientes completos —el legacy y el delegado— cumplen el MISMO puerto, y de ahí sale que
// encender la delegación no obligue a tocar ni un handler: lo que cambia es quién autentica, no cómo
// se le pide.
var (
	_ Authenticator      = (*apiclient.AuthClient)(nil)
	_ Authenticator      = (*apiclient.DelegatedAuthenticator)(nil)
	_ EntitlementsReader = (*apiclient.EntitlementsClient)(nil)
	_ HomeAPI            = (*apiclient.EntitlementsClient)(nil)

	_ TenantVariablesManager = (*apiclient.TenantVariablesClient)(nil)
	_ IntegrationsManager    = (*apiclient.IntegrationsClient)(nil)
	_ TenantLLMManager       = (*apiclient.TenantLLMClient)(nil)
	_ APIPort                = (*apiclient.Client)(nil)
	_ APIPort                = (*apiclient.DelegatedClient)(nil)
)
