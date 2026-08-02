package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

type editorNotice struct {
	Success bool
	Message string
}

const sessionExpiredMessage = "Tu sesión expiró. Vuelve a iniciar sesión e inténtalo de nuevo."

type upstreamErrorSpec struct {
	rejectionPrefix string
	notFoundMessage string
	logMessage      string
	fallbackMessage string
}

func mapEditorError(err error, spec upstreamErrorSpec) (int, *editorNotice) {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return http.StatusUnauthorized, &editorNotice{Success: false, Message: sessionExpiredMessage}
	}
	if spec.notFoundMessage != "" && apiclient.StatusCodeOf(err) == http.StatusNotFound {
		return http.StatusNotFound, &editorNotice{Success: false, Message: spec.notFoundMessage}
	}
	if spec.rejectionPrefix != "" {
		if msg, ok := apiclient.RejectionMessageOf(err); ok {
			return http.StatusBadRequest, &editorNotice{Success: false, Message: spec.rejectionPrefix + msg}
		}
	}
	slog.Warn(spec.logMessage, "error", err)
	return http.StatusBadGateway, &editorNotice{Success: false, Message: spec.fallbackMessage}
}

var (
	publishFlowErrorSpec = upstreamErrorSpec{
		rejectionPrefix: "La plataforma rechazó la definición: ",
		logMessage:      "no se pudo publicar el flujo",
		fallbackMessage: "No se pudo publicar el flujo. Inténtalo más tarde.",
	}
	createTriggerErrorSpec = upstreamErrorSpec{
		rejectionPrefix: "La plataforma rechazó la regla: ",
		logMessage:      "no se pudo crear el trigger",
		fallbackMessage: "No se pudo crear la regla de disparo. Inténtalo más tarde.",
	}
	deleteTriggerErrorSpec = upstreamErrorSpec{
		notFoundMessage: "Esa regla ya no existe o no es tuya.",
		logMessage:      "no se pudo borrar el trigger",
		fallbackMessage: "No se pudo borrar la regla de disparo. Inténtalo más tarde.",
	}
)

const newFlowStarter = `{
  "flow_id": "mi-flujo",
  "version": 1,
  "initial": "inicio",
  "nodes": {
    "inicio": {
      "type": "menu",
      "prompt": "Hola, ¿qué necesitas?",
      "options": { "1": "info", "2": "fin" }
    },
    "info": { "type": "message", "text": "Aquí va la información.", "next": "fin" },
    "fin": { "type": "message", "text": "¡Hasta luego!", "next": null }
  }
}`

// EditorHandler maneja la edición de flujos y la gestión de reglas de disparo.
type EditorHandler struct {
	cfg  *config.Config
	api  EditorManager
	auth *AuthHandler
}

// NewEditorHandler construye un EditorHandler dependiente de EditorManager y AuthHandler.
func NewEditorHandler(cfg *config.Config, api EditorManager, auth *AuthHandler) *EditorHandler {
	return &EditorHandler{
		cfg:  cfg,
		api:  api,
		auth: auth,
	}
}

// ShowFlows lista los flujos del tenant.
func (h *EditorHandler) ShowFlows(c *gin.Context) {
	h.renderFlows(c, http.StatusOK, nil)
}

func (h *EditorHandler) renderFlows(c *gin.Context, status int, notice *editorNotice) {
	var flows []apiclient.FlowSummary
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		flows, lerr = h.api.ListFlows(c.Request.Context(), accessToken)
		return lerr
	})
	if err != nil {
		slog.Warn("no se pudieron listar los flujos (modo degradado)", "error", err)
	}
	render(h.cfg, c, status, "flows.html", gin.H{
		"Title":      "Flujos",
		"Flows":      flows,
		"FlowsError": err != nil,
		"Notice":     notice,
	})
}

// ShowFlowDetail pinta el editor de un flujo.
func (h *EditorHandler) ShowFlowDetail(c *gin.Context) {
	id := c.Param("id")
	if id == "new" {
		h.renderFlowDetail(c, http.StatusOK, "", true, newFlowStarter, nil)
		return
	}

	var raw json.RawMessage
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		raw, gerr = h.api.GetFlow(c.Request.Context(), accessToken, id)
		return gerr
	})
	if err != nil {
		if apiclient.StatusCodeOf(err) == http.StatusNotFound {
			h.renderFlows(c, http.StatusNotFound,
				&editorNotice{Success: false, Message: "Ese flujo no es tuyo o no existe."})
			return
		}
		slog.Warn("no se pudo cargar el flujo", "flow_id", id, "error", err)
		h.renderFlows(c, http.StatusBadGateway,
			&editorNotice{Success: false, Message: "No se pudo cargar el flujo ahora mismo. Inténtalo de nuevo."})
		return
	}
	h.renderFlowDetail(c, http.StatusOK, id, false, prettyJSON(raw), nil)
}

func (h *EditorHandler) renderFlowDetail(c *gin.Context, status int, flowID string, isNew bool, definition string, notice *editorNotice) {
	render(h.cfg, c, status, "flow_detail.html", gin.H{
		"Title":      "Editar flujo",
		"FlowID":     flowID,
		"IsNew":      isNew,
		"Definition": definition,
		"Notice":     notice,
	})
}

// DoPublishFlow publica la definición del textarea como versión NUEVA.
func (h *EditorHandler) DoPublishFlow(c *gin.Context) {
	flowID := strings.TrimSpace(c.PostForm("flow_id"))
	isNew := c.PostForm("is_new") == "1"
	definition := c.PostForm("definition")

	if !json.Valid([]byte(strings.TrimSpace(definition))) {
		h.renderFlowDetail(c, http.StatusBadRequest, flowID, isNew, definition,
			&editorNotice{Success: false, Message: "El JSON no es válido. Revisa la definición antes de publicar."})
		return
	}

	var result *apiclient.PublishFlowResult
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var perr error
		result, perr = h.api.PublishFlow(c.Request.Context(), accessToken, []byte(definition))
		return perr
	})
	if err != nil {
		status, notice := mapEditorError(err, publishFlowErrorSpec)
		h.renderFlowDetail(c, status, flowID, isNew, definition, notice)
		return
	}

	h.renderFlowDetail(c, http.StatusOK, result.FlowID, false, definition, &editorNotice{
		Success: true,
		Message: "Publicada la versión " + strconv.Itoa(result.Version) + " del flujo " + result.FlowID + ".",
	})
}

// ShowTriggers lista las reglas de disparo del tenant + el formulario de alta.
func (h *EditorHandler) ShowTriggers(c *gin.Context) {
	h.renderTriggers(c, http.StatusOK, nil, gin.H{})
}

func (h *EditorHandler) renderTriggers(c *gin.Context, status int, notice *editorNotice, extra gin.H) {
	var triggers []apiclient.Trigger
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		triggers, lerr = h.api.ListTriggers(c.Request.Context(), accessToken)
		return lerr
	})
	if err != nil {
		slog.Warn("no se pudieron listar los triggers (modo degradado)", "error", err)
	}
	data := gin.H{
		"Title":         "Triggers",
		"Triggers":      triggers,
		"TriggersError": err != nil,
		"Notice":        notice,
	}
	for k, v := range extra {
		data[k] = v
	}
	render(h.cfg, c, status, "triggers.html", data)
}

// DoCreateTrigger crea una regla de disparo desde el formulario.
func (h *EditorHandler) DoCreateTrigger(c *gin.Context) {
	kind := strings.TrimSpace(c.PostForm("kind"))
	keyword := strings.TrimSpace(c.PostForm("keyword"))
	matchType := strings.TrimSpace(c.PostForm("match_type"))
	flowID := strings.TrimSpace(c.PostForm("flow_id"))
	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	message := strings.TrimSpace(c.PostForm("message"))
	priorityStr := strings.TrimSpace(c.PostForm("priority"))

	form := gin.H{
		"FormKind": kind, "FormKeyword": keyword, "FormMatchType": matchType,
		"FormFlowID": flowID, "FormSessionID": sessionID, "FormMessage": message,
		"FormPriority": priorityStr,
	}

	priority := 0
	if priorityStr != "" {
		p, perr := strconv.Atoi(priorityStr)
		if perr != nil {
			h.renderTriggers(c, http.StatusBadRequest,
				&editorNotice{Success: false, Message: "La prioridad debe ser un número entero."}, form)
			return
		}
		priority = p
	}

	if msg := validateTriggerForm(kind, keyword, flowID); msg != "" {
		h.renderTriggers(c, http.StatusBadRequest, &editorNotice{Success: false, Message: msg}, form)
		return
	}

	req := apiclient.CreateTriggerRequest{
		Kind: kind, Keyword: keyword, MatchType: matchType, FlowID: flowID,
		Priority: priority, Message: message, SessionID: sessionID,
	}
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		_, cerr := h.api.CreateTrigger(c.Request.Context(), accessToken, req)
		return cerr
	})
	if err != nil {
		status, notice := mapEditorError(err, createTriggerErrorSpec)
		h.renderTriggers(c, status, notice, form)
		return
	}
	h.renderTriggers(c, http.StatusCreated,
		&editorNotice{Success: true, Message: "Regla de disparo creada."}, gin.H{})
}

func validateTriggerForm(kind, keyword, flowID string) string {
	switch kind {
	case "keyword":
		if keyword == "" || flowID == "" {
			return "Un trigger de tipo keyword necesita la palabra clave y el flow_id."
		}
	case "fallback":
		if flowID == "" {
			return "Un trigger de tipo fallback necesita el flow_id."
		}
	case "escape":
		if keyword == "" {
			return "Un trigger de tipo escape necesita la palabra clave."
		}
	default:
		return "Elige un tipo de trigger válido (keyword, fallback o escape)."
	}
	return ""
}

// DoDeleteTrigger borra una regla.
func (h *EditorHandler) DoDeleteTrigger(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		h.renderTriggers(c, http.StatusBadRequest,
			&editorNotice{Success: false, Message: "Falta el identificador de la regla a borrar."}, gin.H{})
		return
	}
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		return h.api.DeleteTrigger(c.Request.Context(), accessToken, id)
	})
	if err != nil {
		status, notice := mapEditorError(err, deleteTriggerErrorSpec)
		h.renderTriggers(c, status, notice, gin.H{})
		return
	}
	h.renderTriggers(c, http.StatusOK,
		&editorNotice{Success: true, Message: "Regla de disparo borrada."}, gin.H{})
}
