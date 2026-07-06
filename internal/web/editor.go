package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// editorNotice es el aviso (snackbar) que las páginas del editor pintan tras una
// acción: Success gobierna el estilo (éxito/error) y Message el texto legible. Es el
// equivalente de sendView del dashboard para flows/triggers.
type editorNotice struct {
	Success bool
	Message string
}

// newFlowStarter es la plantilla mínima que se ofrece al crear un flujo desde cero
// (/flows/new): un menú con dos opciones y un cierre. Da al operador una estructura
// válida que editar en vez de un textarea en blanco. No se envía sola: solo se
// publica cuando el operador pulsa "Publicar" (REQ-E2).
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

// ---------------------------------------------------------------------------
// Flows (REQ-E1/E2)
// ---------------------------------------------------------------------------

// ShowFlows lista los flujos del tenant (REQ-E1). Modo degradado si el listado
// falla: avisa y deja el resto de la consola operable.
func (h *Handler) ShowFlows(c *gin.Context) {
	h.renderFlows(c, http.StatusOK, nil)
}

// renderFlows carga los flujos (con refresh + reintento ante 401) y pinta flows.html
// con un posible aviso. Centraliza el modo degradado.
func (h *Handler) renderFlows(c *gin.Context, status int, notice *editorNotice) {
	var flows []apiclient.FlowSummary
	err := h.withAuthRetry(c, func(accessToken string) error {
		var lerr error
		flows, lerr = h.api.ListFlows(c.Request.Context(), accessToken)
		return lerr
	})
	if err != nil {
		slog.Warn("no se pudieron listar los flujos (modo degradado)", "error", err)
	}
	h.render(c, status, "flows.html", gin.H{
		"Title":      "Flujos",
		"Flows":      flows,
		"FlowsError": err != nil,
		"Notice":     notice,
	})
}

// ShowFlowDetail pinta el editor de un flujo: su definición JSON en un <textarea>
// (REQ-E1). id == "new" abre el editor con una plantilla de arranque para publicar
// un flujo nuevo (mismo endpoint POST, REQ-E2). 404 de la API → aviso "no existe".
func (h *Handler) ShowFlowDetail(c *gin.Context) {
	id := c.Param("id")
	if id == "new" {
		h.renderFlowDetail(c, http.StatusOK, "", true, newFlowStarter, nil)
		return
	}

	var raw json.RawMessage
	err := h.withAuthRetry(c, func(accessToken string) error {
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

// renderFlowDetail pinta flow_detail.html con la definición en el textarea. isNew
// distingue "publicar versión N+1" de "publicar flujo nuevo" en la copia de la UI.
func (h *Handler) renderFlowDetail(c *gin.Context, status int, flowID string, isNew bool, definition string, notice *editorNotice) {
	h.render(c, status, "flow_detail.html", gin.H{
		"Title":      "Editar flujo",
		"FlowID":     flowID,
		"IsNew":      isNew,
		"Definition": definition,
		"Notice":     notice,
	})
}

// DoPublishFlow publica la definición del textarea como versión NUEVA (REQ-E2). El
// JSON se valida parseable ANTES de llamar a la API (REQ-E4): si no lo es, se
// re-pinta el editor con el error SIN enviar nada. Si la plataforma lo rechaza
// (validación de nodos server-side), se muestra su mensaje. Éxito → aviso con el
// flow_id y la versión asignada.
func (h *Handler) DoPublishFlow(c *gin.Context) {
	flowID := strings.TrimSpace(c.PostForm("flow_id"))
	isNew := c.PostForm("is_new") == "1"
	definition := c.PostForm("definition")

	// (REQ-E4) validación mínima cliente: el JSON debe ser parseable. No se envía si no.
	if !json.Valid([]byte(strings.TrimSpace(definition))) {
		h.renderFlowDetail(c, http.StatusBadRequest, flowID, isNew, definition,
			&editorNotice{Success: false, Message: "El JSON no es válido. Revisa la definición antes de publicar."})
		return
	}

	var result *apiclient.PublishFlowResult
	err := h.withAuthRetry(c, func(accessToken string) error {
		var perr error
		result, perr = h.api.PublishFlow(c.Request.Context(), accessToken, []byte(definition))
		return perr
	})
	if err != nil {
		h.renderFlowDetail(c, publishErrorStatus(err), flowID, isNew, definition, publishErrorNotice(err))
		return
	}

	h.renderFlowDetail(c, http.StatusOK, result.FlowID, false, definition, &editorNotice{
		Success: true,
		Message: "Publicada la versión " + strconv.Itoa(result.Version) + " del flujo " + result.FlowID + ".",
	})
}

// publishErrorStatus mapea el error de PublishFlow a un status HTTP para el
// re-render (400 en validación, 401/502 en sesión/upstream).
func publishErrorStatus(err error) int {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return http.StatusUnauthorized
	}
	if _, ok := apiclient.RejectionMessageOf(err); ok {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// publishErrorNotice traduce el error de PublishFlow a un aviso legible. El rechazo
// de la plataforma (validación del contenido propio) SÍ se muestra (REQ-E4); un
// fallo genérico del upstream no filtra trazas.
func publishErrorNotice(err error) *editorNotice {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return &editorNotice{Success: false, Message: "Tu sesión expiró. Vuelve a iniciar sesión e inténtalo de nuevo."}
	}
	if msg, ok := apiclient.RejectionMessageOf(err); ok {
		return &editorNotice{Success: false, Message: "La plataforma rechazó la definición: " + msg}
	}
	slog.Warn("no se pudo publicar el flujo", "error", err)
	return &editorNotice{Success: false, Message: "No se pudo publicar el flujo. Inténtalo más tarde."}
}

// prettyJSON re-indenta el JSON para mostrarlo legible en el textarea. Si no
// parsea (no debería venir de la API), devuelve el original tal cual.
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// ---------------------------------------------------------------------------
// Triggers (REQ-E3)
// ---------------------------------------------------------------------------

// ShowTriggers lista las reglas de disparo del tenant + el formulario de alta
// (REQ-E3). Modo degradado si el listado falla.
func (h *Handler) ShowTriggers(c *gin.Context) {
	h.renderTriggers(c, http.StatusOK, nil, gin.H{})
}

// renderTriggers carga las reglas (con refresh + reintento ante 401) y pinta
// triggers.html con un posible aviso y los valores del form repoblados (extra).
func (h *Handler) renderTriggers(c *gin.Context, status int, notice *editorNotice, extra gin.H) {
	var triggers []apiclient.Trigger
	err := h.withAuthRetry(c, func(accessToken string) error {
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
	h.render(c, status, "triggers.html", data)
}

// DoCreateTrigger crea una regla de disparo desde el formulario (REQ-E3). Valida
// mínimamente según kind ANTES de llamar a la API (REQ-E4): keyword→keyword+flow_id,
// fallback→flow_id, escape→keyword. Si la plataforma rechaza (validación
// server-side), muestra su mensaje. Éxito → re-lista con aviso.
func (h *Handler) DoCreateTrigger(c *gin.Context) {
	kind := strings.TrimSpace(c.PostForm("kind"))
	keyword := strings.TrimSpace(c.PostForm("keyword"))
	matchType := strings.TrimSpace(c.PostForm("match_type"))
	flowID := strings.TrimSpace(c.PostForm("flow_id"))
	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	message := strings.TrimSpace(c.PostForm("message"))
	priorityStr := strings.TrimSpace(c.PostForm("priority"))

	// Valores repoblados al re-render si hay error (mejor UX).
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
	err := h.withAuthRetry(c, func(accessToken string) error {
		_, cerr := h.api.CreateTrigger(c.Request.Context(), accessToken, req)
		return cerr
	})
	if err != nil {
		h.renderTriggers(c, createTriggerErrorStatus(err), createTriggerErrorNotice(err), form)
		return
	}
	// Éxito: re-lista (la nueva regla aparece) con aviso, sin repoblar el form.
	h.renderTriggers(c, http.StatusCreated,
		&editorNotice{Success: true, Message: "Regla de disparo creada."}, gin.H{})
}

// validateTriggerForm replica la validación mínima de la API por kind (REQ-E4).
// Devuelve "" si es coherente o un mensaje legible si falta algo requerido.
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

// createTriggerErrorStatus mapea el error de CreateTrigger a un status para el
// re-render.
func createTriggerErrorStatus(err error) int {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return http.StatusUnauthorized
	}
	if _, ok := apiclient.RejectionMessageOf(err); ok {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// createTriggerErrorNotice traduce el error de CreateTrigger a un aviso legible. El
// rechazo de la plataforma SÍ se muestra (REQ-E4).
func createTriggerErrorNotice(err error) *editorNotice {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return &editorNotice{Success: false, Message: "Tu sesión expiró. Vuelve a iniciar sesión e inténtalo de nuevo."}
	}
	if msg, ok := apiclient.RejectionMessageOf(err); ok {
		return &editorNotice{Success: false, Message: "La plataforma rechazó la regla: " + msg}
	}
	slog.Warn("no se pudo crear el trigger", "error", err)
	return &editorNotice{Success: false, Message: "No se pudo crear la regla de disparo. Inténtalo más tarde."}
}

// DoDeleteTrigger borra una regla (REQ-E3, "editar" = borrar + crear) y re-lista con
// un aviso. Un 404 (regla ajena o ya borrada) se trata como aviso, no como fallo
// duro: la lista simplemente ya no la tendrá.
func (h *Handler) DoDeleteTrigger(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		h.renderTriggers(c, http.StatusBadRequest,
			&editorNotice{Success: false, Message: "Falta el identificador de la regla a borrar."}, gin.H{})
		return
	}
	err := h.withAuthRetry(c, func(accessToken string) error {
		return h.api.DeleteTrigger(c.Request.Context(), accessToken, id)
	})
	if err != nil {
		h.renderTriggers(c, deleteTriggerErrorStatus(err), deleteTriggerErrorNotice(err), gin.H{})
		return
	}
	h.renderTriggers(c, http.StatusOK,
		&editorNotice{Success: true, Message: "Regla de disparo borrada."}, gin.H{})
}

// deleteTriggerErrorStatus mapea el error de DeleteTrigger a un status para el
// re-render.
func deleteTriggerErrorStatus(err error) int {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return http.StatusUnauthorized
	}
	if apiclient.StatusCodeOf(err) == http.StatusNotFound {
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// deleteTriggerErrorNotice traduce el error de DeleteTrigger a un aviso legible.
func deleteTriggerErrorNotice(err error) *editorNotice {
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return &editorNotice{Success: false, Message: "Tu sesión expiró. Vuelve a iniciar sesión e inténtalo de nuevo."}
	}
	if apiclient.StatusCodeOf(err) == http.StatusNotFound {
		return &editorNotice{Success: false, Message: "Esa regla ya no existe o no es tuya."}
	}
	slog.Warn("no se pudo borrar el trigger", "error", err)
	return &editorNotice{Success: false, Message: "No se pudo borrar la regla de disparo. Inténtalo más tarde."}
}
