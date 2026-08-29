package web

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// tenantVariablesBlankRows son las filas vacías que la pantalla ofrece al final para dar de alta
// variables nuevas. Sin JS no se pueden añadir filas en el navegador, así que vienen servidas: tres
// bastan para el caso normal y, tras guardar, aparecen otras tres.
const tenantVariablesBlankRows = 3

// tenantVariableRow es una fila del formulario. Clave y valor viajan VERBATIM en los dos sentidos:
// no se recortan espacios, no se normaliza mayúsculas y no se toca el contenido. wApp no interpreta
// estas variables (D-041.1), y «no interpretar» empieza por no reescribirlas al pasar.
type tenantVariableRow struct {
	Key   string
	Value string
}

// tenantVariablesView es lo que pinta la plantilla.
type tenantVariablesView struct {
	// Rows son las filas a pintar: las variables del tenant más las vacías de alta.
	Rows []tenantVariableRow
	// Count es cuántas variables tiene el tenant de verdad (sin contar las filas vacías).
	Count int
	// UpdatedAt es la marca del cambio más reciente (vacía si el tenant no tiene variables).
	UpdatedAt string
	// Loaded distingue «este tenant no tiene variables» de «no se pudieron leer».
	Loaded bool
}

// tenantVarsNotice es el aviso de una operación sobre las variables.
type tenantVarsNotice struct {
	Success bool
	Message string
}

// TenantVariablesHandler sirve la pantalla de variables de empresa: pares clave→valor que wApp
// guarda y devuelve tal cual, sin interpretarlos.
//
// Es una pantalla PERMANENTE (ADR-0035 §3): las variables son capa técnica del producto, no una
// operación de negocio, así que no migra —se queda en el BFF— y no lleva marca de provisionalidad.
type TenantVariablesHandler struct {
	cfg  *config.Config
	api  TenantVariablesManager
	auth *AuthHandler
}

// NewTenantVariablesHandler construye el handler sobre el puerto de variables.
func NewTenantVariablesHandler(cfg *config.Config, api TenantVariablesManager, auth *AuthHandler) *TenantVariablesHandler {
	return &TenantVariablesHandler{cfg: cfg, api: api, auth: auth}
}

// ShowTenantVariables pinta el conjunto del tenant.
func (h *TenantVariablesHandler) ShowTenantVariables(c *gin.Context) {
	var vars *apiclient.TenantVariables
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		vars, gerr = h.api.GetTenantVariables(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapTenantVariablesReadError(err)
		h.render(c, status, tenantVariablesView{}, notice)
		return
	}
	h.render(c, http.StatusOK, viewFromServer(vars), nil)
}

// DoSaveTenantVariables guarda el conjunto COMPLETO que trae el formulario.
//
// Las dos acciones de la pantalla —guardar y quitar— acaban en la misma llamada, y no por atajo:
// contra esta API borrar ES guardar el conjunto sin esa fila. Un «quitar» que hiciera otra cosa
// estaría fingiendo una operación por clave que el contrato no tiene.
func (h *TenantVariablesHandler) DoSaveTenantVariables(c *gin.Context) {
	rows, ok := rowsFromForm(c)
	if !ok {
		h.render(c, http.StatusBadRequest, tenantVariablesView{Rows: blankRows(), Loaded: true},
			&tenantVarsNotice{Message: "El formulario llegó incompleto. Recarga la página e inténtalo de nuevo."})
		return
	}

	// `remove` trae el ÍNDICE de la fila, no su clave: si el operador editó la clave antes de pulsar
	// «Quitar», la fila que quiere quitar sigue siendo esa, no la que dice el texto que acaba de
	// escribir.
	removeIdx := -1
	if raw := c.PostForm("remove"); raw != "" {
		idx, err := strconv.Atoi(raw)
		if err != nil || idx < 0 || idx >= len(rows) {
			h.render(c, http.StatusBadRequest, viewFromRows(rows), &tenantVarsNotice{
				Message: "No se pudo identificar la fila a quitar. Recarga la página e inténtalo de nuevo.",
			})
			return
		}
		removeIdx = idx
	}

	vars, msg := variablesFromRows(rows, removeIdx)
	if msg != "" {
		h.render(c, http.StatusBadRequest, viewFromRows(rows), &tenantVarsNotice{Message: msg})
		return
	}

	var saved *apiclient.TenantVariables
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var serr error
		saved, serr = h.api.ReplaceTenantVariables(c.Request.Context(), accessToken, vars)
		return serr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapTenantVariablesSaveError(err)
		// Se re-pinta lo que el operador tecleó, no lo que hay en el servidor: su trabajo no se tira
		// por un rechazo, y así puede corregir sobre lo que escribió.
		h.render(c, status, viewFromRows(rows), notice)
		return
	}

	h.render(c, http.StatusOK, viewFromServer(saved), &tenantVarsNotice{
		Success: true,
		Message: savedMessage(removeIdx >= 0, len(saved.Variables)),
	})
}

// savedMessage redacta la confirmación diciendo cuántas variables quedaron: es la forma más directa
// de que el operador note un borrado accidental, que es el riesgo propio de un guardado que reemplaza
// el conjunto entero.
func savedMessage(removed bool, count int) string {
	action := "Variables guardadas"
	if removed {
		action = "Variable quitada"
	}
	switch count {
	case 0:
		return action + ". Este tenant se queda sin ninguna variable."
	case 1:
		return action + ". Este tenant tiene ahora 1 variable."
	default:
		return action + ". Este tenant tiene ahora " + strconv.Itoa(count) + " variables."
	}
}

func (h *TenantVariablesHandler) render(c *gin.Context, status int, view tenantVariablesView, notice *tenantVarsNotice) {
	render(h.cfg, c, status, "tenant-variables.html", gin.H{
		"Title":  "Variables",
		"View":   view,
		"Notice": notice,
	})
}

// rowsFromForm lee los pares del formulario. Devuelve false si los dos arrays no vienen emparejados,
// que es señal de un envío manipulado o truncado: antes que adivinar qué valor va con qué clave —y
// arriesgarse a guardar una mezcla— se rechaza y se pide recargar.
func rowsFromForm(c *gin.Context) ([]tenantVariableRow, bool) {
	keys := c.PostFormArray("key")
	values := c.PostFormArray("value")
	if len(keys) != len(values) {
		return nil, false
	}
	rows := make([]tenantVariableRow, 0, len(keys))
	for i := range keys {
		rows = append(rows, tenantVariableRow{Key: keys[i], Value: values[i]})
	}
	return rows, true
}

// variablesFromRows arma el conjunto que se va a enviar. Devuelve el mensaje de error (vacío = todo
// bien) en vez de escribir la respuesta.
//
// Solo se comprueba la FORMA, nunca el contenido: que una fila con valor no se quede sin clave y que
// no haya dos veces la misma clave. Lo segundo importa más de lo que parece —un mapa se quedaría en
// silencio con la última y el operador vería desaparecer un valor sin que nadie se lo dijera—. Lo que
// NO se hace es mirar qué dicen la clave o el valor: ni listas de claves conocidas, ni tipado del
// valor, ni recorte de espacios (D-041.1).
func variablesFromRows(rows []tenantVariableRow, removeIdx int) (map[string]string, string) {
	vars := make(map[string]string, len(rows))
	for i, row := range rows {
		if i == removeIdx {
			continue
		}
		if row.Key == "" {
			if row.Value == "" {
				continue // fila de alta sin rellenar
			}
			return nil, "Hay un valor sin clave. Escribe la clave o vacía también el valor."
		}
		if _, dup := vars[row.Key]; dup {
			return nil, "La clave «" + row.Key + "» está repetida. Deja una sola fila con esa clave."
		}
		vars[row.Key] = row.Value
	}
	return vars, ""
}

// viewFromServer arma la vista con lo que devolvió la API, ordenada por clave para que el render sea
// estable (un mapa de Go no tiene orden y la tabla bailaría en cada recarga).
func viewFromServer(vars *apiclient.TenantVariables) tenantVariablesView {
	keys := make([]string, 0, len(vars.Variables))
	for k := range vars.Variables {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]tenantVariableRow, 0, len(keys)+tenantVariablesBlankRows)
	for _, k := range keys {
		rows = append(rows, tenantVariableRow{Key: k, Value: vars.Variables[k]})
	}
	return tenantVariablesView{
		Rows:      append(rows, blankRows()...),
		Count:     len(keys),
		UpdatedAt: vars.UpdatedAt,
		Loaded:    true,
	}
}

// viewFromRows arma la vista con lo que el operador tecleó, respetando su orden. Se usa al re-pintar
// tras un rechazo: reordenar por clave en ese momento le movería las filas bajo el cursor.
func viewFromRows(rows []tenantVariableRow) tenantVariablesView {
	filled := 0
	for _, row := range rows {
		if row.Key != "" || row.Value != "" {
			filled++
		}
	}
	return tenantVariablesView{
		Rows:   append(append([]tenantVariableRow{}, rows...), blankRows()...),
		Count:  filled,
		Loaded: true,
	}
}

func blankRows() []tenantVariableRow {
	return make([]tenantVariableRow, tenantVariablesBlankRows)
}

// mapTenantVariablesReadError traduce el fallo de lectura sin filtrar el detalle del upstream.
func mapTenantVariablesReadError(err error) (int, *tenantVarsNotice) {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &tenantVarsNotice{
			Message: "Tu usuario no puede consultar las variables de este tenant.",
		}
	}
	slog.Warn("no se pudieron leer las variables de empresa", "error", err)
	return http.StatusBadGateway, &tenantVarsNotice{
		Message: "No se pudieron cargar las variables ahora mismo. Inténtalo de nuevo.",
	}
}

// mapTenantVariablesSaveError traduce el fallo del guardado. El 400 y el 413 traen motivo: el
// primero dice qué tiene de malo la forma (una clave vacía, una demasiado larga, demasiadas
// variables) y el segundo que el conjunto no cabe.
func mapTenantVariablesSaveError(err error) (int, *tenantVarsNotice) {
	if rej, ok := apiclient.RejectionOf(err); ok {
		if rej.StatusCode == http.StatusRequestEntityTooLarge {
			return http.StatusRequestEntityTooLarge, &tenantVarsNotice{
				Message: "El conjunto de variables es demasiado grande para guardarlo. " +
					"Acorta los valores más largos o reparte el contenido.",
			}
		}
		msg := "La plataforma rechazó el conjunto. Revisa las claves."
		if rej.Message != "" {
			msg = "La plataforma rechazó el conjunto: " + rej.Message
		}
		return http.StatusBadRequest, &tenantVarsNotice{Message: msg}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &tenantVarsNotice{
			Message: "Tu usuario no puede modificar las variables de este tenant.",
		}
	}
	slog.Warn("no se pudieron guardar las variables de empresa", "error", err)
	return http.StatusBadGateway, &tenantVarsNotice{
		Message: "No se pudieron guardar las variables ahora mismo. Vuelve a intentarlo; " +
			"nada se ha cambiado.",
	}
}
