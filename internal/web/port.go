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

// IntakeManager define el contrato de la bandeja de SOLICITUDES (Plan 041 · T1.5, T4.8 y T4.10):
// listar con filtros, abrir el detalle, mover el estado del ciclo de vida, corregir las líneas a
// mano y descartar por lotes lo que ya no va a ninguna parte.
//
// Va segregado del resto porque es un frente propio y de pago: sus CINCO rutas exigen la feature
// `cart_basic` en la plataforma, y la pantalla que lo consume es PROVISIONAL (migra a
// `wapp-client-console` con el Plan 047, ADR-0047; antes decía «a KMP», y dejó de decirlo porque el
// Plan 045 está al 0 % y esa app está diferida: el marcador no era ejecutable). Cuando esa pantalla
// muera, esta interfaz se va con ella sin arrastrar a nadie.
//
// `ReplaceIntakeItems` recibe el conjunto COMPLETO de líneas de cliente, no una operación por
// línea, porque así es el contrato de la plataforma (PUT, no POST): añadir, quitar y corregir son
// la misma llamada con una lista distinta. Devuelve el detalle ya actualizado —con la revisión
// `corrected` dentro— para que la pantalla repinte sin un segundo GET.
//
// `DiscardIntakes` es la única operación de esta interfaz que trabaja sobre VARIAS solicitudes de
// una vez, y su resultado se lee por ítem: un lote mixto —unos descartados y otros no— es el caso
// normal, así que devolver `nil` de error no autoriza a decir «listo».
//
// Las TRES acciones del 044 —`CorrectIntakeItems`, `ApproveIntake` y `RequestIntakeInfo`— son de
// otra naturaleza que el resto y por eso el contrato las nombra aparte: las dos últimas LE HABLAN AL
// CLIENTE por WhatsApp, y las tres dejan revisión. El desplegable de estado (`SetIntakeStatus`) no
// hace ninguna de las dos cosas, y confundirlos en la pantalla sería ofrecer «responderle al
// cliente» donde solo se mueve una etiqueta.
//
// `CorrectIntakeItems` es el MISMO PUT que `ReplaceIntakeItems` con el campo `as_correction`: no hay
// ninguna ruta `/correct` y no debe inventarse una.
//
// 🔴 `ReanalyzeIntake` es la ÚNICA que no devuelve un detalle, y no es una asimetría por descuido: la
// plataforma abre un trabajo que corre por detrás y contesta con el número que la revisión TENDRÁ.
// Cuando responde, esa revisión NO EXISTE todavía, así que no hay detalle nuevo que devolver — y una
// firma que lo devolviera obligaría a inventarlo.
//
// 🔴 `SuggestIntakeQuote` tampoco devuelve detalle, y por un motivo DISTINTO y más importante: NO
// CAMBIA NADA. Redacta un texto y lo devuelve; no aprueba, no transiciona y no le manda nada al
// cliente. Esa estrechez de la firma es lo que sostiene INV-1 —la máquina propone por un camino, la
// dueña aprueba por otro—, y devolver aquí un `*IntakeDetail` insinuaría que algo se guardó.
type IntakeManager interface {
	ListIntakes(ctx context.Context, accessToken string, f apiclient.IntakeFilter) (*apiclient.IntakePage, error)
	GetIntake(ctx context.Context, accessToken, id string) (*apiclient.IntakeDetail, error)
	SetIntakeStatus(ctx context.Context, accessToken, id, status string) (*apiclient.Intake, error)
	ReplaceIntakeItems(ctx context.Context, accessToken, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error)
	CorrectIntakeItems(ctx context.Context, accessToken, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error)
	ApproveIntake(ctx context.Context, accessToken, id, renderedText string) (*apiclient.IntakeDetail, error)
	RequestIntakeInfo(ctx context.Context, accessToken, id, question string) (*apiclient.IntakeDetail, error)
	ReanalyzeIntake(ctx context.Context, accessToken, id, text string) (*apiclient.IntakeReanalysis, error)
	SuggestIntakeQuote(ctx context.Context, accessToken, id string) (*apiclient.IntakeQuoteSuggestion, error)
	DiscardIntakes(ctx context.Context, accessToken string, intakeIDs []string) (*apiclient.IntakeDiscardResult, error)
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
// `catalog_import`) y la pantalla que lo consume es PROVISIONAL (migra a `wapp-client-console` con el
// Plan 047, ADR-0047; antes decía «a KMP», y dejó de decirlo porque el Plan 045 está al 0 % y esa app
// está diferida: el marcador no era ejecutable). Cuando esa pantalla muera, esta interfaz se va con ella.
// Las dos puertas del import —JSON y planilla— van en el MISMO puerto y no en dos, porque para la
// pantalla son un solo acto con dos entradas: el paso 2 sale siempre por la de JSON, incluso cuando
// el paso 1 entró por la planilla (el `document` normalizado que devuelve el tabular es lo que lo
// hace posible).
type CatalogImporter interface {
	ImportCatalog(ctx context.Context, accessToken string, document []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error)
	ImportCatalogTabular(ctx context.Context, accessToken, filename string, content []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error)
	GetCatalogTemplate(ctx context.Context, accessToken, format string) (*apiclient.CatalogTemplate, error)
	GetCatalogPrompt(ctx context.Context, accessToken string) (*apiclient.CatalogPrompt, error)
	// ListTenantContentRefs alimenta el selector de ref del paso 1. Sin él la pantalla mandaba la ref
	// vacía y la plataforma caía a su default, enseñando un diff contra un catálogo distinto del que
	// el operador creía reemplazar (defecto A3 del cierre del Plan 041).
	ListTenantContentRefs(ctx context.Context, accessToken string) ([]apiclient.TenantContentRef, error)
}

// CatalogImportAPI es lo que la pantalla de import consume: el import más las features efectivas,
// que son las que deciden si la sección se emite siquiera.
type CatalogImportAPI interface {
	CatalogImporter
	EntitlementsReader
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
	IntakeManager
	TenantVariablesManager
	CatalogImporter
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
	_ IntakeManager      = (*apiclient.IntakesClient)(nil)

	_ TenantVariablesManager = (*apiclient.TenantVariablesClient)(nil)
	_ CatalogImporter        = (*apiclient.CatalogImportClient)(nil)
	_ IntegrationsManager    = (*apiclient.IntegrationsClient)(nil)
	_ TenantLLMManager       = (*apiclient.TenantLLMClient)(nil)
	_ APIPort                = (*apiclient.Client)(nil)
	_ APIPort                = (*apiclient.DelegatedClient)(nil)
)
