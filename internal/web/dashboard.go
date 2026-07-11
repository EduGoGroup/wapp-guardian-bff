package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// sendView es el resultado del envío que la plantilla pinta como snackbar: Success gobierna el estilo
// (éxito/error) y Message el texto legible (REQ-D3, sin trazas internas). CommandID acompaña el acuse en el
// caso feliz.
type sendView struct {
	Success   bool
	Message   string
	CommandID string
}

// ShowDashboard pinta el dashboard tras el AuthMiddleware (ruta protegida): lista las sesiones del tenant
// (REQ-D1) con su rol editable (bot|passive, Plan 020 · T1) y ofrece el formulario de envío (REQ-D2). Si
// el listado falla, degrada (REQ-D4): avisa y deja introducir el session_id a mano en el form. No
// empareja ni desvincula: el emparejamiento vive en el Edge.
func (h *Handler) ShowDashboard(c *gin.Context) {
	h.renderDashboard(c, http.StatusOK, nil, gin.H{})
}

// DoSend procesa el formulario de envío (REQ-D2/D3): valida los tres campos, llama a POST /api/v1/messages
// (con refresh + reintento ante 401, REQ-C6) y re-renderiza el dashboard con el resultado en un snackbar.
// Nunca expone el detalle crudo del upstream al usuario.
func (h *Handler) DoSend(c *gin.Context) {
	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	to := strings.TrimSpace(c.PostForm("to"))
	text := strings.TrimSpace(c.PostForm("text"))

	// Valores del form que se repueblan al re-renderizar (mejor UX si hay error).
	form := gin.H{"FormSessionID": sessionID, "FormTo": to, "FormText": text}

	if sessionID == "" || to == "" || text == "" {
		h.renderDashboard(c, http.StatusBadRequest,
			&sendView{Success: false, Message: "Elige una sesión e introduce el número de destino y el texto."},
			form)
		return
	}

	var result *apiclient.SendResult
	err := h.withAuthRetry(c, func(accessToken string) error {
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

// sendResultView traduce el (resultado, error) de SendMessage a un mensaje legible (REQ-D3). Los códigos de
// negocio se mapean uno a uno; jamás se filtra la traza/detalle interno del upstream.
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
		// El Edge recibió el comando pero su ejecución falló: mensaje genérico (no se filtra result.Error).
		return &sendView{Success: false, Message: "El Edge recibió el mensaje pero no pudo entregarlo. Inténtalo de nuevo."}
	}
	return &sendView{Success: true, Message: "Mensaje aceptado por el Edge.", CommandID: result.AckedCommandID}
}

// validRoles son los roles de sesión que la consola permite fijar (Plan 020 · T1): bot dispara
// triggers/auto-responde; passive solo escucha/transporta. Espeja fleet.ValidRole de la plataforma.
var validRoles = map[string]bool{"bot": true, "passive": true}

// DoSetSessionRole procesa el formulario de cambio de rol de una sesión (select bot|passive por fila de
// la tabla): valida el rol client-side, llama a POST /api/v1/sessions/{id}/role (con refresh + reintento
// ante 401, REQ-C6) y re-renderiza el dashboard con el resultado en un snackbar. El re-render re-lista
// las sesiones, así que la tabla ya muestra el rol nuevo. Nunca expone el detalle crudo del upstream.
func (h *Handler) DoSetSessionRole(c *gin.Context) {
	sessionID := strings.TrimSpace(c.Param("id"))
	role := strings.TrimSpace(c.PostForm("role"))

	if sessionID == "" || !validRoles[role] {
		h.renderDashboard(c, http.StatusBadRequest,
			&sendView{Success: false, Message: "Elige un rol válido para la sesión (bot o passive)."},
			gin.H{})
		return
	}

	err := h.withAuthRetry(c, func(accessToken string) error {
		return h.api.SetSessionRole(c.Request.Context(), accessToken, sessionID, role)
	})

	view := roleResultView(role, err)
	status := http.StatusOK
	if !view.Success {
		status = http.StatusBadRequest
	}
	h.renderDashboard(c, status, view, gin.H{})
}

// roleResultView traduce el error de SetSessionRole a un mensaje legible (mismo criterio que
// sendResultView, REQ-D3): los códigos de negocio se mapean uno a uno; jamás se filtra la traza/detalle
// interno del upstream.
func roleResultView(role string, err error) *sendView {
	if err == nil {
		return &sendView{Success: true, Message: "Rol de la sesión cambiado a " + role + "."}
	}
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return &sendView{Success: false, Message: sessionExpiredMessage}
	}
	switch apiclient.StatusCodeOf(err) {
	case http.StatusBadRequest:
		return &sendView{Success: false, Message: "La plataforma rechazó el rol. Usa bot o passive."}
	case http.StatusNotFound:
		return &sendView{Success: false, Message: "Esa sesión no es tuya o no existe. Elige una del listado."}
	default:
		slog.Warn("cambio de rol de sesión falló", "error", err)
		return &sendView{Success: false, Message: "No se pudo cambiar el rol de la sesión. Inténtalo más tarde."}
	}
}

// renderDashboard carga las sesiones y pinta el dashboard con un posible resultado de envío. Centraliza el
// modo degradado (REQ-D4): si ListSessions falla (con refresh + reintento ante 401), marca SessionsError y
// deja el listado vacío; la plantilla ofrece entonces un input manual de session_id.
func (h *Handler) renderDashboard(c *gin.Context, status int, send *sendView, extra gin.H) {
	var sessions []apiclient.Session
	sessionsErr := h.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		sessions, lerr = h.api.ListSessions(c.Request.Context(), accessToken)
		return lerr
	})
	if sessionsErr != nil {
		slog.Warn("no se pudieron listar las sesiones (modo degradado)", "error", sessionsErr)
	}

	data := gin.H{
		"Title":         "Consola",
		"Sessions":      sessions,
		"SessionsError": sessionsErr != nil,
		"Send":          send,
	}
	for k, v := range extra {
		data[k] = v
	}
	h.render(c, status, "dashboard.html", data)
}

// withAuthRetry ejecuta una llamada de negocio con el access token de la sesión y, si la API responde 401
// (ErrUnauthorized), refresca la sesión UNA vez y reintenta (REQ-C6). Si no hay token o el refresh falla,
// devuelve el error tal cual para que el llamador degrade o mapee el mensaje.
func (h *Handler) withAuthRetry(c *gin.Context, fn func(accessToken string) error) error {
	token, _ := c.Get(ctxAccessToken)
	accessToken, _ := token.(string)

	err := fn(accessToken)
	if !errors.Is(err, apiclient.ErrUnauthorized) {
		return err
	}

	// 401: intenta refrescar la sesión UNA vez y reintenta (REQ-C6). Si el refresh falla, devuelve el
	// 401 original para que el llamador degrade o pinte "sesión expirada".
	newToken, rerr := h.refreshSession(c)
	if rerr != nil {
		return err
	}
	return fn(newToken)
}
