package apiclient

import (
	"context"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/golang-jwt/jwt/v5"
)

// DelegatedAuthenticator autentica al operador delegando en identity y canjeando el resultado por un
// Context Token de wApp, en un solo movimiento server-side.
//
// Las dos credenciales tienen papeles distintos y la delegación los respeta: el Identity Token dice
// QUIÉN ERES y lo emite identity; el Context Token dice QUÉ PUEDES HACER EN WAPP y lo emite la
// plataforma. El BFF necesita el segundo para operar, así que el primero **muere aquí**: no vuelve
// al llamador, no entra en la cookie y no se registra. Lo que no se guarda no se filtra.
//
// De ahí sale la regla que sostiene el resto del BFF: la cookie custodia SIEMPRE el Context Token,
// así que el tenant se sigue leyendo de sus claims como hasta hoy. Un Identity Token no tiene claims
// de negocio —no puede tenerlos— y si llegara a la cookie el tenant desaparecería sin más aviso.
type DelegatedAuthenticator struct {
	identity *IdentityClient
	exchange *ExchangeClient
}

// NewDelegatedAuthenticator compone el emisor de identidad con el canje de la plataforma.
func NewDelegatedAuthenticator(identity *IdentityClient, exchange *ExchangeClient) *DelegatedAuthenticator {
	return &DelegatedAuthenticator{identity: identity, exchange: exchange}
}

// Login autentica en identity y canjea al instante: dos saltos server-to-server, una sola sesión.
func (a *DelegatedAuthenticator) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	tokens, err := a.identity.Login(ctx, email, password)
	if err != nil {
		return nil, err
	}
	return a.session(ctx, tokens)
}

// Refresh rota el refresh en identity y vuelve a canjear. Es la cascada de REQ-A3 vista desde
// abajo: el AuthMiddleware la dispara proactivamente a menos de dos minutos del vencimiento del
// Context Token, y withAuthRetry la dispara ante un 401 de la plataforma. Las dos entran por aquí,
// y en las dos el navegador no ve ningún token: solo recibe la cookie renovada.
func (a *DelegatedAuthenticator) Refresh(ctx context.Context, refreshToken string) (*AuthResult, error) {
	tokens, err := a.identity.Refresh(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return a.session(ctx, tokens)
}

// Logout revoca en identity SOLO la sesión de esta aplicación: la del Edge es otra sesión en identity
// y sobrevive. Es el modelo Google —cerrar la consola no te echa del teléfono— y la revocación global
// es una operación aparte que este flujo no invoca nunca.
//
// El primer parámetro es el Context Token que el BFF custodia y aquí se ignora deliberadamente:
// identity no lo emitió y no sabría qué hacer con él. La sesión se identifica por el refresh.
func (a *DelegatedAuthenticator) Logout(ctx context.Context, _, refreshToken string) error {
	return a.identity.Logout(ctx, refreshToken)
}

// session canjea el Identity Token y arma la sesión que el BFF custodia: Context Token como access,
// refresh de identity, y el vencimiento que la plataforma acotó con el del Identity Token.
func (a *DelegatedAuthenticator) session(ctx context.Context, tokens *IdentityTokens) (*AuthResult, error) {
	exchanged, err := a.exchange.Exchange(ctx, tokens.IdentityToken)
	if err != nil {
		return nil, err
	}
	return &AuthResult{
		AccessToken:  exchanged.ContextToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    exchanged.ExpiresAt,
		Context:      contextOf(exchanged.ContextToken),
	}, nil
}

// contextOf lee tenant, usuario y roles del CONTEXT Token, que es de donde salen siempre.
//
// Sin verificar la firma, igual que el resto del BFF: el token acaba de llegar por un canal
// server-to-server y quien lo valida de verdad es la plataforma en cada llamada. Aquí solo alimenta
// la traza, así que un token ilegible devuelve un contexto vacío en vez de tumbar el login.
func contextOf(contextToken string) IdentityContext {
	var claims sharedjwt.Claims
	if _, _, err := jwt.NewParser().ParseUnverified(contextToken, &claims); err != nil {
		return IdentityContext{}
	}
	return IdentityContext{TenantID: claims.TenantID, UserID: claims.UserID, Roles: claims.Roles}
}

// DelegatedClient es el cliente del BFF con la delegación encendida: la autenticación viaja a
// identity y todo el tráfico de negocio sigue yendo a la plataforma, por el mismo Transport que usa
// el canje. Es el segundo destino del BFF, no un reemplazo del primero.
type DelegatedClient struct {
	*Transport
	*DelegatedAuthenticator
	*DashboardClient
	*EditorClient
	*IntakesClient
	*TenantVariablesClient
}

// NewDelegated construye el cliente de la delegación: identity para las credenciales, plataforma
// para el canje y para el negocio.
func NewDelegated(platformBaseURL, identityBaseURL string) *DelegatedClient {
	t := NewTransport(platformBaseURL)
	return &DelegatedClient{
		Transport:              t,
		DelegatedAuthenticator: NewDelegatedAuthenticator(NewIdentityClient(identityBaseURL, SystemBFF), NewExchangeClient(t)),
		DashboardClient:        NewDashboardClient(t),
		EditorClient:           NewEditorClient(t),
		IntakesClient:          NewIntakesClient(t),
		TenantVariablesClient:  NewTenantVariablesClient(t),
	}
}
