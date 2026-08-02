package web

import (
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

// NewHandler construye el Handler con el cliente real de la API pública y sin dependencias externas
// opcionales (modo dual apagado).
func NewHandler(cfg *config.Config) *Handler {
	return NewHandlerWithAPI(cfg, apiclient.New(cfg.PublicAPIBaseURL))
}

// NewHandlerWithDeps construye el Handler de producción: cliente real de la API pública más las
// dependencias que trae el arranque.
func NewHandlerWithDeps(cfg *config.Config, deps Deps) *Handler {
	h := NewHandlerWithAPI(cfg, apiclient.New(cfg.PublicAPIBaseURL))
	h.IdentityVerifier = deps.IdentityVerifier
	return h
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
