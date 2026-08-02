package web

import (
	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// Handler agrupa los controladores web especializados del BFF.
type Handler struct {
	cfg     *config.Config
	api     APIPort
	refresh *refreshGroup
	*AuthHandler
	*DashboardHandler
	*EditorHandler
}

// NewHandler construye el Handler con el cliente real de la API pública.
func NewHandler(cfg *config.Config) *Handler {
	return NewHandlerWithAPI(cfg, apiclient.New(cfg.PublicAPIBaseURL))
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
