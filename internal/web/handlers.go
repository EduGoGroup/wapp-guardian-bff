package web

import (
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// Handler agrupa los controladores web especializados del BFF.
type Handler struct {
	cfg     *config.Config
	api     APIPort
	refresh *sharedweb.RefreshGroup[*apiclient.AuthResult]

	*AuthHandler
	*DashboardHandler
	*EditorHandler
	*IntakesHandler
	*TenantVariablesHandler
	*CatalogImportHandler
	*IntegrationsHandler
}

// NewHandler construye el Handler de producción: cliente real de la API, eligiendo entre login directo
// contra wApp o delegado a identity según la PUERTA de newAPIClient.
func NewHandler(cfg *config.Config) *Handler {
	return NewHandlerWithAPI(cfg, newAPIClient(cfg))
}

// newAPIClient elige el cliente según la PUERTA de la delegación (WAPP_IDENTITY_URL).
//
// Sin ella —el default— el BFF autentica contra la API pública de wApp y no hay una sola llamada
// distinta de las de ayer. Con ella, las credenciales viajan a identity y el Identity Token se canjea
// al instante por el Context Token que la cookie custodia; el negocio sigue yendo a la plataforma por
// el mismo Transport.
//
// Que la transición se gobierne por env es lo que permite encender, apagar y comparar los dos flujos
// en el mismo binario, sin desplegar nada distinto.
func newAPIClient(cfg *config.Config) APIPort {
	identityURL := strings.TrimSpace(cfg.IdentityBaseURL)
	if identityURL == "" {
		return apiclient.New(cfg.PublicAPIBaseURL)
	}
	client, err := apiclient.NewDelegated(cfg.PublicAPIBaseURL, identityURL)
	if err != nil {
		// Fail-closed en el arranque, como la allowlist de proxies y las plantillas: unas URLs con
		// las que no se puede hablar con el plano de identidad convertirían cada login en un fallo
		// que parece del usuario. El módulo las valida aquí en vez de dentro de la primera llamada.
		slog.Error("delegación de identidad mal configurada", "identity", identityURL, "error", err)
		panic(err)
	}
	slog.Info("delegación de identidad activada: el login del BFF viaja a identity",
		"identity", identityURL, "system", apiclient.SystemBFF, "canje", cfg.PublicAPIBaseURL)
	return client
}

// NewHandlerWithAPI construye el Handler inyectando un APIPort.
func NewHandlerWithAPI(cfg *config.Config, api APIPort) *Handler {
	refresh := sharedweb.NewRefreshGroup[*apiclient.AuthResult]()
	ah := NewAuthHandler(cfg, api, refresh)
	dh := NewDashboardHandler(cfg, api, ah)
	eh := NewEditorHandler(cfg, api, ah)
	ih := NewIntakesHandler(cfg, api, ah)
	vh := NewTenantVariablesHandler(cfg, api, ah)
	ch := NewCatalogImportHandler(cfg, api, ah)
	gh := NewIntegrationsHandler(cfg, api, ah)
	return &Handler{
		cfg:                    cfg,
		api:                    api,
		refresh:                refresh,
		AuthHandler:            ah,
		DashboardHandler:       dh,
		EditorHandler:          eh,
		IntakesHandler:         ih,
		TenantVariablesHandler: vh,
		CatalogImportHandler:   ch,
		IntegrationsHandler:    gh,
	}
}

func (h *Handler) withAuthRetry(c *gin.Context, fn func(accessToken string) error) error {
	return h.AuthHandler.withAuthRetry(c, fn)
}
