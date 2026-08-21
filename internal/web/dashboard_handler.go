package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

type sendView struct {
	Success   bool
	Message   string
	CommandID string
}

// DashboardHandler maneja las peticiones del dashboard de sesiones y envíos.
type DashboardHandler struct {
	cfg  *config.Config
	api  DashboardAPI
	auth *AuthHandler
}

// NewDashboardHandler construye un DashboardHandler dependiente de DashboardAPI y AuthHandler.
func NewDashboardHandler(cfg *config.Config, api DashboardAPI, auth *AuthHandler) *DashboardHandler {
	return &DashboardHandler{
		cfg:  cfg,
		api:  api,
		auth: auth,
	}
}

// ShowDashboard pinta el dashboard tras el AuthMiddleware.
func (h *DashboardHandler) ShowDashboard(c *gin.Context) {
	h.renderDashboard(c, http.StatusOK, nil, gin.H{})
}

// DoSend procesa el formulario de envío.
func (h *DashboardHandler) DoSend(c *gin.Context) {
	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	to := strings.TrimSpace(c.PostForm("to"))
	text := strings.TrimSpace(c.PostForm("text"))

	form := gin.H{"FormSessionID": sessionID, "FormTo": to, "FormText": text}

	if sessionID == "" || to == "" || text == "" {
		h.renderDashboard(c, http.StatusBadRequest,
			&sendView{Success: false, Message: "Elige una sesión e introduce el número de destino y el texto."},
			form)
		return
	}

	var result *apiclient.SendResult
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var serr error
		result, serr = h.api.SendMessage(c.Request.Context(), accessToken, sessionID, to, text)
		return serr
	})

	view := sendResultView(result, err)
	status := http.StatusOK
	if !view.Success {
		status = http.StatusBadRequest
	}
	h.renderDashboard(c, status, view, form)
}

func sendResultView(result *apiclient.SendResult, err error) *sendView {
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			return &sendView{Success: false, Message: sessionExpiredMessage}
		}
		switch apiclient.StatusCodeOf(err) {
		case http.StatusBadRequest:
			return &sendView{Success: false, Message: "Datos inválidos: revisa la sesión, el número de destino y el texto."}
		case http.StatusNotFound:
			return &sendView{Success: false, Message: "Esa sesión no es tuya o no existe. Elige una del listado."}
		case http.StatusBadGateway:
			return &sendView{Success: false, Message: "El teléfono está desconectado ahora mismo. Inténtalo cuando vuelva a estar en línea."}
		case http.StatusGatewayTimeout:
			return &sendView{Success: false, Message: "El envío tardó demasiado. Inténtalo de nuevo."}
		default:
			slog.Warn("envío de mensaje falló", "error", err)
			return &sendView{Success: false, Message: "No se pudo enviar el mensaje. Inténtalo más tarde."}
		}
	}
	if result == nil {
		return &sendView{Success: false, Message: "No se pudo enviar el mensaje. Inténtalo más tarde."}
	}
	if !result.OK {
		return &sendView{Success: false, Message: "El Edge recibió el mensaje pero no pudo entregarlo. Inténtalo de nuevo."}
	}
	return &sendView{Success: true, Message: "Mensaje aceptado por el Edge.", CommandID: result.AckedCommandID}
}

// validProfiles son los PERFILES de negocio admitidos (ADR-0027 §Decisión 1). Los identificadores
// viajan en inglés por el cable; el castellano —«activa»/«pasiva»— es solo lo que ve el dueño y sale
// de profileLabel.
//
// 🔴 No tiene NADA que ver con `devices.role` del Edge (`primary`/`standby`, ADR-0018).
var validProfiles = map[string]bool{"active": true, "passive": true}

// profileLabel traduce el identificador del cable al vocabulario del dueño. Es la ÚNICA puerta por
// la que el castellano entra en los mensajes: si aquí llega algo fuera de validProfiles se devuelve
// tal cual, pero eso no puede pasar —el handler valida antes de llamar.
func profileLabel(profile string) string {
	switch profile {
	case "active":
		return "activa"
	case "passive":
		return "pasiva"
	}
	return profile
}

// DoSetSessionProfile procesa el formulario de cambio de PERFIL de una sesión (ADR-0027).
func (h *DashboardHandler) DoSetSessionProfile(c *gin.Context) {
	sessionID := strings.TrimSpace(c.Param("id"))
	profile := strings.TrimSpace(c.PostForm("profile"))

	if sessionID == "" || !validProfiles[profile] {
		h.renderDashboard(c, http.StatusBadRequest,
			&sendView{Success: false, Message: "Elige un perfil válido (activa o pasiva)."}, gin.H{})
		return
	}

	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		return h.api.SetSessionProfile(c.Request.Context(), accessToken, sessionID, profile)
	})

	view := setSessionProfileResultView(profile, err)
	status := http.StatusOK
	if !view.Success {
		status = http.StatusBadRequest
	}
	h.renderDashboard(c, status, view, gin.H{})
}

func setSessionProfileResultView(profile string, err error) *sendView {
	if err == nil {
		return &sendView{Success: true, Message: "Perfil de la sesión cambiado a " + profileLabel(profile) + "."}
	}
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return &sendView{Success: false, Message: sessionExpiredMessage}
	}
	switch apiclient.StatusCodeOf(err) {
	case http.StatusBadRequest:
		return &sendView{Success: false, Message: "La plataforma rechazó el perfil de la sesión. Elige activa o pasiva."}
	case http.StatusNotFound:
		return &sendView{Success: false, Message: "Esa sesión no es tuya o no existe. Elige una del listado."}
	default:
		slog.Warn("cambio de perfil de sesión falló", "error", err)
		return &sendView{Success: false, Message: "No se pudo cambiar el perfil de la sesión. Inténtalo más tarde."}
	}
}

func (h *DashboardHandler) renderDashboard(c *gin.Context, status int, send *sendView, extra gin.H) {
	var sessions []apiclient.Session
	sessionsErr := h.auth.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		sessions, lerr = h.api.ListSessions(c.Request.Context(), accessToken)
		return lerr
	})
	if sessionsErr != nil {
		if errors.Is(sessionsErr, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		slog.Warn("no se pudieron listar las sesiones (modo degradado)", "error", sessionsErr)
	}

	// Features efectivas del tenant: alimentan los chips y deciden qué secciones se emiten. No puede
	// tumbar la página —devuelve la vista cero y el gate cierra— así que va después del listado, que
	// es la llamada que sí manda sobre la sesión.
	entitlements := resolveEntitlements(c, h.auth, h.api)

	data := gin.H{
		"Title":                 "Consola",
		"Sessions":              sessions,
		"SessionsError":         sessionsErr != nil,
		"Send":                  send,
		entitlementsDataKey:     entitlements,
		intakesNavDataKey:       entitlements.Has(intakesFeature),
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
		integrationsNavDataKey:  entitlements.Has(integrationsFeature),
	}
	for k, v := range extra {
		data[k] = v
	}
	render(h.cfg, c, status, "dashboard.html", data)
}
