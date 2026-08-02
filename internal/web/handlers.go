package web

import (
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// Handler agrupa los controladores web especializados del BFF.
type Handler struct {
	cfg     *config.Config
	api     APIPort
	refresh *refreshGroup

	// IdentityVerifier es el verificador de Identity Tokens de identity-core (nil si el modo dual está
	// apagado). Ver Deps.IdentityVerifier: lo cablea el arranque y lo estrena la Ola 3.
	IdentityVerifier *identityjwt.MultiVerifier

	*AuthHandler
	*DashboardHandler
	*EditorHandler
}

// NewHandler construye el Handler con el cliente real de la API y sin dependencias externas
// opcionales (modo dual apagado).
func NewHandler(cfg *config.Config) *Handler {
	return NewHandlerWithAPI(cfg, newAPIClient(cfg))
}

// NewHandlerWithDeps construye el Handler de producción: cliente real de la API más las dependencias
// que trae el arranque.
func NewHandlerWithDeps(cfg *config.Config, deps Deps) *Handler {
	h := NewHandlerWithAPI(cfg, newAPIClient(cfg))
	h.IdentityVerifier = deps.IdentityVerifier
	return h
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
	slog.Info("delegación de identidad activada: el login del BFF viaja a identity",
		"identity", identityURL, "system", apiclient.SystemBFF, "canje", cfg.PublicAPIBaseURL)
	return apiclient.NewDelegated(cfg.PublicAPIBaseURL, identityURL)
}

// NewHandlerWithAPI construye el Handler inyectando un APIPort.
func NewHandlerWithAPI(cfg *config.Config, api APIPort) *Handler {
	refresh := newRefreshGroup()
	ah := NewAuthHandler(cfg, api, refresh)
	dh := NewDashboardHandler(cfg, api, ah)
	eh := NewEditorHandler(cfg, api, ah)
	return &Handler{
		cfg:              cfg,
		api:              api,
		refresh:          refresh,
		AuthHandler:      ah,
		DashboardHandler: dh,
		EditorHandler:    eh,
	}
}

func (h *Handler) withAuthRetry(c *gin.Context, fn func(accessToken string) error) error {
	return h.AuthHandler.withAuthRetry(c, fn)
}
