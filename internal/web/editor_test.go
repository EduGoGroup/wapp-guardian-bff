package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recordingAPI es una API pública fake que registra cada request (método, ruta,
// cuerpo) y responde según el mapa de rutas "MÉTODO /ruta" → (status, body). Una
// ruta no mapeada responde 500 (fuerza al test a declarar lo que usa). A diferencia
// de routedAPI, permite inspeccionar QUÉ se llamó y con qué cuerpo (necesario para
// verificar el envoltorio {definition} y el "no se envía si el JSON es inválido").
type recordingAPI struct {
	*httptest.Server
	mu     sync.Mutex
	hits   []string // "MÉTODO /ruta", en orden
	bodies []string // cuerpo recibido, mismo índice que hits
	last   string   // último cuerpo recibido (cualquier ruta)
}

func newRecordingAPI(routes map[string]struct {
	status int
	body   string
}) *recordingAPI {
	rec := &recordingAPI{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.hits = append(rec.hits, r.Method+" "+r.URL.Path)
		rec.bodies = append(rec.bodies, string(body))
		rec.last = string(body)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if resp, ok := routes[r.Method+" "+r.URL.Path]; ok {
			w.WriteHeader(resp.status)
			_, _ = io.WriteString(w, resp.body)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
	return rec
}

func (r *recordingAPI) hitCount(methodPath string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, h := range r.hits {
		if h == methodPath {
			n++
		}
	}
	return n
}

func (r *recordingAPI) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// bodyFor devuelve el ÚLTIMO cuerpo recibido para esa ruta concreta ("MÉTODO /ruta"), a
// diferencia de lastBody() que es el último cuerpo de CUALQUIER ruta — necesario cuando,
// como en la creación de un trigger, el handler encadena otra llamada (el re-listado) que
// pisaría el cuerpo que el test quiere inspeccionar.
func (r *recordingAPI) bodyFor(methodPath string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.hits) - 1; i >= 0; i-- {
		if r.hits[i] == methodPath {
			return r.bodies[i]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Flows (REQ-E1/E2)
// ---------------------------------------------------------------------------

// TestFlowsListRenders: GET /flows con summaries → la tabla los pinta (REQ-E1).
func TestFlowsListRenders(t *testing.T) {
	body := `[{"flow_id":"menu-soporte","version":3,"created_at":"2026-07-06T10:00:00Z"},` +
		`{"flow_id":"encuesta-nps","version":1}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/flows": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/flows", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /flows debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"menu-soporte", "v3", "encuesta-nps", "Nuevo flujo"} {
		if !strings.Contains(out, want) {
			t.Errorf("la lista de flujos debía contener %q", want)
		}
	}
}

// TestFlowDetailRenders: GET /flows/{id} → la definición JSON en el textarea + el
// aviso de que publicar crea versión nueva (REQ-E1/E2).
func TestFlowDetailRenders(t *testing.T) {
	def := `{"flow_id":"menu-soporte","version":3,"initial":"inicio","nodes":{}}`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/flows/menu-soporte": {http.StatusOK, def},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/flows/menu-soporte", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /flows/menu-soporte debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "flow_id") || !strings.Contains(out, "menu-soporte") {
		t.Error("el detalle debía mostrar la definición en el textarea")
	}
	if !strings.Contains(out, "publica una versión nueva") {
		t.Error("el detalle debía advertir que publicar crea una versión nueva (REQ-E2)")
	}
}

// TestPublishFlowValidJSON: POST /flows con JSON válido → se envía envuelto en
// {definition}, la API responde 201 y se muestra la versión publicada (REQ-E2).
func TestPublishFlowValidJSON(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/flows": {http.StatusCreated, `{"flow_id":"menu-soporte","version":4}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	def := `{"flow_id":"menu-soporte","version":4,"initial":"a","nodes":{}}`
	form := url.Values{"flow_id": {"menu-soporte"}, "is_new": {"0"}, "definition": {def}}
	rec := postFormWithCookie(router, "/flows", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("publicar OK debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Error("publicar OK debía mostrar un snackbar de éxito")
	}
	if !strings.Contains(out, "Publicada la versión 4") {
		t.Errorf("publicar OK debía mostrar la versión publicada; body=%s", out)
	}
	// El cuerpo enviado a la API debe anidar la definición en {definition} (contrato real).
	last := api.lastBody()
	if !strings.Contains(last, `"definition"`) || !strings.Contains(last, `"flow_id":"menu-soporte"`) {
		t.Errorf("el cuerpo debía envolver la definición en {definition}; got %s", last)
	}
}

// TestPublishFlowInvalidJSONNotSent: POST /flows con JSON no parseable → NO se llama
// a la API y se muestra el error (REQ-E4).
func TestPublishFlowInvalidJSONNotSent(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/flows": {http.StatusCreated, `{"flow_id":"x","version":1}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"flow_id": {"x"}, "is_new": {"0"}, "definition": {`{ esto no es json`}}
	rec := postFormWithCookie(router, "/flows", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/flows") != 0 {
		t.Error("JSON inválido NO debía enviarse a la API (REQ-E4)")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") || !strings.Contains(out, "El JSON no es válido") {
		t.Errorf("JSON inválido debía mostrar el error; body=%s", out)
	}
	// El textarea conserva lo que el operador escribió para poder corregirlo.
	if !strings.Contains(out, "esto no es json") {
		t.Error("el textarea debía conservar la definición inválida para corregirla")
	}
}

// TestPublishFlowAPIRejection: la plataforma rechaza (400 con detalle) → se muestra
// su mensaje (REQ-E4, validación de nodos server-side).
func TestPublishFlowAPIRejection(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/flows": {http.StatusBadRequest, "definición de flujo inválida: nodo inicial ausente"},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	def := `{"flow_id":"x","version":1,"initial":"a","nodes":{}}`
	form := url.Values{"flow_id": {"x"}, "is_new": {"0"}, "definition": {def}}
	rec := postFormWithCookie(router, "/flows", form, validSessionCookie(t))

	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") {
		t.Error("un rechazo de la API debía mostrar un snackbar de error")
	}
	if !strings.Contains(out, "nodo inicial ausente") {
		t.Errorf("debía mostrarse el mensaje de la API (REQ-E4); body=%s", out)
	}
}

// ---------------------------------------------------------------------------
// Triggers (REQ-E3)
// ---------------------------------------------------------------------------

// TestTriggersListRenders: GET /triggers con reglas → la tabla las pinta (REQ-E3).
func TestTriggersListRenders(t *testing.T) {
	body := `[{"trigger_id":"tr-1","kind":"keyword","keyword":"hola","match_type":"exact","flow_id":"menu","priority":10,"enabled":true},` +
		`{"trigger_id":"tr-2","kind":"fallback","match_type":"exact","flow_id":"menu","priority":0,"enabled":true}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/triggers": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/triggers", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /triggers debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"hola", "keyword", "fallback", "menu", "Crear regla"} {
		if !strings.Contains(out, want) {
			t.Errorf("la lista de triggers debía contener %q", want)
		}
	}
}

// TestCreateTriggerOK: POST /triggers keyword válido → 201 → éxito + re-lista (REQ-E3).
func TestCreateTriggerOK(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{"trigger_id":"tr-9","kind":"keyword","keyword":"hola","match_type":"exact","flow_id":"menu","priority":0,"enabled":true}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"keyword"}, "keyword": {"hola"}, "match_type": {"exact"}, "flow_id": {"menu"}, "priority": {"0"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 1 {
		t.Error("un trigger válido debía enviarse a la API")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") || !strings.Contains(out, "creada") {
		t.Errorf("crear OK debía mostrar un snackbar de éxito; body=%s", out)
	}
}

// TestCreateTriggerMissingFields: keyword sin flow_id → error legible SIN llamar a la
// API (REQ-E4).
func TestCreateTriggerMissingFields(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"keyword"}, "keyword": {"hola"}, "flow_id": {""}, "priority": {"0"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 0 {
		t.Error("un trigger incompleto NO debía enviarse a la API (REQ-E4)")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") || !strings.Contains(out, "flow_id") {
		t.Errorf("un trigger incompleto debía mostrar el error; body=%s", out)
	}
}

// TestCreateTriggerAPIRejection: la API rechaza (400 con detalle) → se muestra su
// mensaje (REQ-E4).
func TestCreateTriggerAPIRejection(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusBadRequest, "match_type inválido (usar exact|contains)"},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"keyword"}, "keyword": {"hola"}, "match_type": {"raro"}, "flow_id": {"menu"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") {
		t.Error("un rechazo de la API debía mostrar un snackbar de error")
	}
	if !strings.Contains(out, "match_type inválido") {
		t.Errorf("debía mostrarse el mensaje de la API (REQ-E4); body=%s", out)
	}
}

// TestDeleteTriggerRemoves: POST /triggers/{id}/delete → DELETE en la API + re-lista
// con aviso (REQ-E3).
func TestDeleteTriggerRemoves(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"DELETE /api/v1/triggers/tr-1": {http.StatusNoContent, ``},
		"GET /api/v1/triggers":         {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postFormWithCookie(router, "/triggers/tr-1/delete", url.Values{}, validSessionCookie(t))

	if api.hitCount("DELETE /api/v1/triggers/tr-1") != 1 {
		t.Error("borrar debía llamar a DELETE en la API")
	}
	if api.hitCount("GET /api/v1/triggers") != 1 {
		t.Error("tras borrar debía re-listarse")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") || !strings.Contains(out, "borrada") {
		t.Errorf("borrar debía mostrar un snackbar de éxito; body=%s", out)
	}
}

// ---------------------------------------------------------------------------
// Triggers · kinds event_start/event_stop (Plan 043 · T2.1)
// ---------------------------------------------------------------------------

// TestTriggersListRendersEventKindAndShadowedNotice: GET /triggers con un event_start
// (event_kind) y un fallback marcado shadowed_by_event_list → la tabla pinta el tipo
// de evento y el aviso de sombreado (D-043.20/REQ-27b, MD-043.11).
func TestTriggersListRendersEventKindAndShadowedNotice(t *testing.T) {
	body := `[{"trigger_id":"tr-1","kind":"event_start","keyword":"carrito","event_kind":"cart","match_type":"exact","priority":0,"enabled":true},` +
		`{"trigger_id":"tr-2","kind":"fallback","match_type":"exact","flow_id":"menu","priority":0,"enabled":true,"shadowed_by_event_list":true}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/triggers": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/triggers", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /triggers debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"event_start", "carrito", "cart", "chip--error\">sombreado", "no llega a dispararse"} {
		if !strings.Contains(out, want) {
			t.Errorf("la lista de triggers debía contener %q; body=%s", want, out)
		}
	}
}

// TestCreateTriggerEventStartOK: POST /triggers event_start con keyword + event_kind
// válidos → se envía a la API con event_kind incluido (REQ-05).
func TestCreateTriggerEventStartOK(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{"trigger_id":"tr-9","kind":"event_start","keyword":"carrito","event_kind":"cart"}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"event_start"}, "keyword": {"carrito"}, "event_kind": {"cart"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 1 {
		t.Error("un event_start válido debía enviarse a la API")
	}
	sent := api.bodyFor("POST /api/v1/triggers")
	if !strings.Contains(sent, `"event_kind":"cart"`) {
		t.Errorf("el cuerpo debía incluir event_kind para un event_start; got %s", sent)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Errorf("crear OK debía mostrar un snackbar de éxito; body=%s", out)
	}
}

// TestCreateTriggerEventStartMissingEventKind: event_start sin event_kind → error
// claro SIN llegar a la API (defensa en profundidad; la plataforma también lo rechaza).
func TestCreateTriggerEventStartMissingEventKind(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"event_start"}, "keyword": {"carrito"}, "event_kind": {""}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 0 {
		t.Error("un event_start sin event_kind NO debía enviarse a la API")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") || !strings.Contains(out, "tipo de evento") {
		t.Errorf("debía mostrarse un error claro pidiendo el tipo de evento; body=%s", out)
	}
}

// TestCreateTriggerEventStartInvalidEventKind: event_kind fuera del catálogo de fábrica
// (menu|cart|survey|media) → rechazado localmente, sin llegar a la API.
func TestCreateTriggerEventStartInvalidEventKind(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"event_start"}, "keyword": {"carrito"}, "event_kind": {"factura"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 0 {
		t.Error("un event_kind fuera de catálogo NO debía enviarse a la API")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--error") {
		t.Errorf("debía mostrarse un error para el event_kind inválido; body=%s", out)
	}
}

// TestCreateTriggerEventStopOK: POST /triggers event_stop con keyword → se envía a la
// API (REQ-06), sin event_kind (kind que no lo usa).
func TestCreateTriggerEventStopOK(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{"trigger_id":"tr-9","kind":"event_stop","keyword":"salir"}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"event_stop"}, "keyword": {"salir"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 1 {
		t.Error("un event_stop válido debía enviarse a la API")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Errorf("crear OK debía mostrar un snackbar de éxito; body=%s", out)
	}
}

// TestCreateTriggerEventKindNotSentForOtherKinds: un event_kind "colgado" de un envío
// anterior en el formulario (p. ej. el operador cambió el <select> de kind sin limpiar
// el de event_kind) NO debe viajar a la API cuando el kind elegido no es event_start.
func TestCreateTriggerEventKindNotSentForOtherKinds(t *testing.T) {
	api := newRecordingAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/triggers": {http.StatusCreated, `{"trigger_id":"tr-9","kind":"keyword","keyword":"hola","flow_id":"menu"}`},
		"GET /api/v1/triggers":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"kind": {"keyword"}, "keyword": {"hola"}, "flow_id": {"menu"}, "event_kind": {"cart"}}
	rec := postFormWithCookie(router, "/triggers", form, validSessionCookie(t))

	if api.hitCount("POST /api/v1/triggers") != 1 {
		t.Fatalf("el trigger keyword válido debía enviarse a la API; body=%s", rec.Body.String())
	}
	sent := api.bodyFor("POST /api/v1/triggers")
	if strings.Contains(sent, "event_kind") {
		t.Errorf("event_kind NO debía viajar en un trigger que no es event_start; got %s", sent)
	}
}

// TestEditorRoutesProtected: las rutas del editor sin cookie → redirect a /login
// (AuthMiddleware de T2).
func TestEditorRoutesProtected(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	for _, path := range []string{"/flows", "/flows/menu", "/triggers"} {
		rec := getWithCookie(router, path, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET %s sin cookie debía redirigir a /login, got %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := postFormWithCookie(router, "/flows", url.Values{"definition": {"{}"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("POST /flows sin cookie debía redirigir a /login, got %d", rec.Code)
	}
	rec = postFormWithCookie(router, "/triggers", url.Values{"kind": {"keyword"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("POST /triggers sin cookie debía redirigir a /login, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// El distintivo VISIBLE de provisionalidad (ADR-0047 · punto 2)
// ---------------------------------------------------------------------------

// TestEditorScreensCarryProvisionalBadge cierra la asimetría que el ADR-0047 anotó y que nadie había
// escrito: de las seis pantallas provisionales del BFF, la bandeja y el import se lo decían AL
// USUARIO, y flujos, detalle de flujo y disparadores lo decían sólo en un comentario de `server.go`.
// «O lo dicen las tres o no lo dice ninguna.»
//
// 🔴 El test existe porque el distintivo es lo primero que se cae al reordenar una plantilla, y un
// comentario no lo sujeta: sin un aserto que lo exija, la asimetría se reabre en silencio y la
// pantalla vuelve a callarse su fecha de caducidad. Es hermano de los que ya cubren `intakes.html`,
// `intake-detail.html` y `catalog-import.html`.
//
// Comprueba el literal completo —destino y ADR incluidos— y no sólo la palabra «PROVISIONAL»: lo que
// el ADR-0047 corrige no es que faltara la marca, es que la marca nombrara un destino que nadie ha
// empezado.
func TestEditorScreensCarryProvisionalBadge(t *testing.T) {
	const badge = "PROVISIONAL — migra a la consola de administración (Plan 047, ADR-0047)"

	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/flows":              {http.StatusOK, `[{"flow_id":"menu-soporte","version":3}]`},
		"GET /api/v1/flows/menu-soporte": {http.StatusOK, `{"flow_id":"menu-soporte","version":3,"initial":"inicio","nodes":{}}`},
		"GET /api/v1/triggers":           {http.StatusOK, `[{"trigger_id":"tr-1","kind":"keyword","keyword":"hola","match_type":"exact","flow_id":"menu","priority":10,"enabled":true}]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	for _, path := range []string{"/flows", "/flows/menu-soporte", "/triggers"} {
		rec := getWithCookie(router, path, validSessionCookie(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s debía renderizar 200, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), badge) {
			t.Errorf("GET %s debía llevar el distintivo visible %q (ADR-0047 §2)", path, badge)
		}
	}
}
