package apiclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/iam"
)

// SystemBFF es la clave de ESTA aplicación en el catálogo de identity (`iam.systems`).
//
// El System Gate de identity autoriza aplicaciones, no ecosistemas: la clave es namespaced
// `<ecosistema>.<app>` y "wapp" a secas no es un valor válido. Es un identificador de contrato, no
// configuración de infraestructura —identity conoce esta aplicación con el mismo nombre en todos sus
// ambientes—, así que vive en el binario y lo que sale a env vars es la URL del emisor.
//
// En el módulo `iam` el `system` dejó de ser una constante para ser un CAMPO del cliente: el mismo
// código sirve al BFF y a la consola de operadores sin una rama por su valor. La constante que
// decide cuál es el de esta aplicación se queda aquí, que es de quien es.
const SystemBFF = "wapp.bff"

// DelegatedAuthenticator autentica al operador delegando en identity y canjeando el resultado por un
// Context Token de wApp, en un solo movimiento server-side.
//
// El mecanismo entero —los dos saltos, el trato al Identity Token, los errores con nombre— vive en
// `wapp-shared/iam`. Aquí queda lo que es del BFF y no del plano de identidad: adaptar la firma de
// `Logout` al puerto Authenticator (que recibe también el access token, y que identity ignora), el
// `Signup`, que es de la plataforma y no de identity, y la traducción de errores de [bffError].
//
// La regla que sostiene el resto del BFF sigue en pie y la garantiza el módulo: la cookie custodia
// SIEMPRE el Context Token, así que el tenant se lee de sus claims. El Identity Token no vuelve del
// módulo, no entra en la cookie y no se registra.
type DelegatedAuthenticator struct {
	identity *iam.Client
	platform *Transport
}

// NewDelegatedAuthenticator compone el cliente del plano de identidad con el Transport de la
// plataforma, que es a donde va el signup (identity no lo atiende).
func NewDelegatedAuthenticator(identity *iam.Client, platform *Transport) *DelegatedAuthenticator {
	return &DelegatedAuthenticator{identity: identity, platform: platform}
}

// Login autentica en identity y canjea al instante: dos saltos server-to-server, una sola sesión.
func (a *DelegatedAuthenticator) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	res, err := a.identity.Login(ctx, email, password)
	if err != nil {
		return nil, bffError(err)
	}
	return res, nil
}

// Refresh rota el refresh en identity y vuelve a canjear. Es la cascada de REQ-A3 vista desde
// abajo: el AuthMiddleware la dispara proactivamente a menos de dos minutos del vencimiento del
// Context Token, y withAuthRetry la dispara ante un 401 de la plataforma. Las dos entran por aquí,
// y en las dos el navegador no ve ningún token: solo recibe la cookie renovada.
func (a *DelegatedAuthenticator) Refresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	res, err := a.identity.Refresh(ctx, refreshToken)
	if err != nil {
		return nil, bffError(err)
	}
	return res, nil
}

// Logout revoca en identity SOLO la sesión de esta aplicación: la del Edge es otra sesión en identity
// y sobrevive. Es el modelo Google —cerrar la consola no te echa del teléfono— y la revocación global
// es una operación aparte que este flujo no invoca nunca.
//
// El primer parámetro es el Context Token que el BFF custodia y aquí se ignora deliberadamente:
// identity no lo emitió y no sabría qué hacer con él. La sesión se identifica por el refresh. La
// firma la impone el puerto Authenticator, que la comparte con el login directo contra la
// plataforma, donde el access token SÍ viaja.
func (a *DelegatedAuthenticator) Logout(ctx context.Context, _, refreshToken string) error {
	return bffError(a.identity.Logout(ctx, refreshToken))
}

// Signup delega la solicitud de registro público en la plataforma pública (:8103).
//
// No pasa por el módulo `iam` y no es un olvido: el alta de una cuenta de wApp la resuelve la
// plataforma, no identity, así que no es una operación del plano de identidad.
func (a *DelegatedAuthenticator) Signup(ctx context.Context, email, password, firstName, lastName, origin string) error {
	return NewAuthClient(a.platform).Signup(ctx, email, password, firstName, lastName, origin)
}

// bffError añade al 401 del plano de identidad el sentinela que el resto del BFF interroga.
//
// Hace falta porque el puerto Authenticator tiene DOS implementaciones —ésta y el login directo
// contra la plataforma— y el AuthMiddleware pregunta lo mismo a las dos: «¿fue un 401?». El login
// directo responde con ErrUnauthorized de este paquete y el módulo con el suyo, que es otro valor;
// sin esta traducción un refresh caducado por identity dejaría de limpiar la cookie y el usuario se
// quedaría dando vueltas con una sesión muerta.
//
// Se envuelven los DOS con `%w: %w` en vez de sustituir uno por otro: así el mismo error sigue
// respondiendo a `errors.Is(err, iam.ErrUnauthorized)` y a `iam.StatusCodeOf(err) == 401`, que es lo
// que distingue un 409 del canje de un 401 de credencial.
//
// El resto de errores con nombre del módulo (ErrForbidden del System Gate, ErrDualModeOff del canje)
// viajan tal cual: no tienen gemelo en este paquete y quien los interroga lo hace por el del módulo.
func bffError(err error) error {
	if err == nil || !errors.Is(err, iam.ErrUnauthorized) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnauthorized, err)
}

// DelegatedClient es el cliente del BFF con la delegación encendida: la autenticación viaja a
// identity y todo el tráfico de negocio sigue yendo a la plataforma, por el mismo Transport que usa
// el canje. Es el segundo destino del BFF, no un reemplazo del primero.
type DelegatedClient struct {
	*Transport
	*DelegatedAuthenticator
	*EntitlementsClient
	*IntakesClient
	*TenantVariablesClient
	*CatalogImportClient
	*IntegrationsClient
	*TenantLLMClient
}

// NewDelegated construye el cliente de la delegación: identity para las credenciales, plataforma
// para el canje y para el negocio.
//
// Devuelve error porque `iam.NewClient` valida las opciones ANTES de la primera llamada: una URL sin
// esquema fallaba antes dentro del primer login, con un mensaje que parecía un problema del usuario.
// El plazo que se le pasa es el mismo que tenía el Transport de identity (defaultTimeout), para que
// la migración no mueva ningún reloj. Y es el de IDENTITY, que no espera a ningún modelo: las
// Option de aquí van al Transport de la PLATAFORMA, que es por donde sale la sugerencia.
func NewDelegated(platformBaseURL, identityBaseURL string, opts ...Option) (*DelegatedClient, error) {
	identity, err := iam.NewClient(iam.Options{
		System:          SystemBFF,
		IdentityBaseURL: identityBaseURL,
		PlatformBaseURL: platformBaseURL,
		Timeout:         defaultTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("apiclient: delegación de identidad: %w", err)
	}
	t := NewTransport(platformBaseURL, opts...)
	return &DelegatedClient{
		Transport:              t,
		DelegatedAuthenticator: NewDelegatedAuthenticator(identity, t),
		EntitlementsClient:     NewEntitlementsClient(t),
		IntakesClient:          NewIntakesClient(t),
		TenantVariablesClient:  NewTenantVariablesClient(t),
		CatalogImportClient:    NewCatalogImportClient(t),
		IntegrationsClient:     NewIntegrationsClient(t),
		TenantLLMClient:        NewTenantLLMClient(t),
	}, nil
}
