package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// testSecret es el secreto de firma de estas pruebas. Es una cadena inconfundible a propósito: todo
// lo que estos tests buscan es que NO aparezca —ni en el HTML ni en el log—, y un valor corriente
// («abc») daría falsos verdes al colarse dentro de cualquier otra palabra.
const testSecret = "secreto-de-firma-INCONFUNDIBLE-0123456789"

// storedIntegration es la fila que el fake guarda. El secreto va aparte, como en la plataforma: no
// forma parte de lo que se devuelve.
type storedIntegration struct {
	CatalogAdapter    string `json:"catalog_adapter"`
	EventsAdapter     string `json:"events_adapter"`
	EndpointURL       string `json:"endpoint_url"`
	Enabled           bool   `json:"enabled"`
	SecretSet         bool   `json:"secret_set"`
	SecretFingerprint string `json:"secret_fingerprint"`
	Configured        bool   `json:"configured"`
	UpdatedAt         string `json:"updated_at"`
}

// integrationsServer es una API fake CON ESTADO que imita el contrato de /api/v1/integrations: guarda
// lo que le manda el PUT y lo devuelve en el GET siguiente.
//
// LO IMPORTANTE ES QUE CONOCE EL SECRETO Y NO LO DEVUELVE, igual que la plataforma: lo guarda en un
// campo propio y en el JSON solo salen `secret_set` y la huella. Así, si el BFF filtrara el valor,
// solo podría venir de lo que el operador tecleó —que es exactamente el descuido que estos tests
// vigilan.
type integrationsServer struct {
	mu       sync.Mutex
	row      *storedIntegration // nil = tenant sin fila (default local/local)
	secret   string             // el secreto GUARDADO; jamás sale en una respuesta
	gets     int
	puts     int
	deletes  int
	lastPut  string // cuerpo crudo del último PUT
	features []string
	// override, si está puesto, contesta el PUT en lugar de guardar (para probar rechazos).
	override http.HandlerFunc
	srv      *httptest.Server

	// La cola de entregas (GET /api/v1/integrations/outbox). `outbox` es lo que se devuelve;
	// `outboxStatus`, si no es cero, corta con ese código ANTES de devolver nada (para probar que un
	// contador ilegible no tumba la pantalla). `outboxGets` cuenta las consultas.
	outbox       storedOutbox
	outboxStatus int
	outboxGets   int
}

// storedOutbox es la respuesta del endpoint de la cola. Son CONTADORES: no hay campo para el
// contenido de las entregas, igual que en la plataforma.
type storedOutbox struct {
	Pending         int64  `json:"pending"`
	Delivering      int64  `json:"delivering"`
	Delivered       int64  `json:"delivered"`
	Dead            int64  `json:"dead"`
	OldestPendingAt string `json:"oldest_pending_at,omitempty"`
}

func newIntegrationsServer(row *storedIntegration, secret string, features ...string) *integrationsServer {
	is := &integrationsServer{row: row, secret: secret, features: features}
	is.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("advisor_ai", is.features...))
			return
		}
		if r.URL.Path == "/api/v1/integrations/outbox" {
			is.mu.Lock()
			defer is.mu.Unlock()
			is.outboxGets++
			if is.outboxStatus != 0 {
				w.WriteHeader(is.outboxStatus)
				_, _ = io.WriteString(w, `{"error":"no se pudo leer el estado de la cola de entregas"}`)
				return
			}
			out, _ := json.Marshal(is.outbox)
			_, _ = w.Write(out)
			return
		}
		if r.URL.Path != "/api/v1/integrations" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
			return
		}

		is.mu.Lock()
		defer is.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			is.gets++
		case http.MethodDelete:
			is.deletes++
			is.row = nil
			is.secret = ""
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			is.lastPut = string(body)
			is.puts++
			if is.override != nil {
				is.override(w, r)
				return
			}
			var req struct {
				CatalogAdapter string `json:"catalog_adapter"`
				EventsAdapter  string `json:"events_adapter"`
				EndpointURL    string `json:"endpoint_url"`
				Secret         string `json:"secret"`
				Enabled        bool   `json:"enabled"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"cuerpo ilegible"}`)
				return
			}
			// Write-only con silencio conservador, igual que la plataforma: el secreto vacío deja el
			// que ya estaba.
			if req.Secret != "" {
				is.secret = req.Secret
			}
			is.row = &storedIntegration{
				Configured:        true,
				CatalogAdapter:    req.CatalogAdapter,
				EventsAdapter:     req.EventsAdapter,
				EndpointURL:       req.EndpointURL,
				Enabled:           req.Enabled,
				SecretSet:         is.secret != "",
				SecretFingerprint: "a1b2c3d4",
				UpdatedAt:         "2026-08-08T10:00:00Z",
			}
		}

		if is.row == nil {
			_, _ = io.WriteString(w,
				`{"configured":false,"catalog_adapter":"local","events_adapter":"local","enabled":false,"secret_set":false}`)
			return
		}
		out, _ := json.Marshal(is.row)
		_, _ = w.Write(out)
	}))
	return is
}

func (is *integrationsServer) close() { is.srv.Close() }

func (is *integrationsServer) counts() (gets, puts, deletes int) {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.gets, is.puts, is.deletes
}

// setOutbox fija lo que responde el endpoint de la cola.
func (is *integrationsServer) setOutbox(o storedOutbox) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.outbox = o
}

// failOutbox hace que el endpoint de la cola conteste con ese código.
func (is *integrationsServer) failOutbox(status int) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.outboxStatus = status
}

func (is *integrationsServer) outboxCount() int {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.outboxGets
}

func (is *integrationsServer) sentPut() string {
	is.mu.Lock()
	defer is.mu.Unlock()
	return is.lastPut
}

// configuredRow es una integración ya puesta: puente encendido, con secreto y con el CATÁLOGO en
// webhook —el campo que esta pantalla no edita y que el guardado tiene que preservar.
func configuredRow() *storedIntegration {
	return &storedIntegration{
		Configured:        true,
		CatalogAdapter:    "webhook",
		EventsAdapter:     "webhook",
		EndpointURL:       "https://crm.aurora.cl/wapp/eventos",
		Enabled:           true,
		SecretSet:         true,
		SecretFingerprint: "a1b2c3d4",
		UpdatedAt:         "2026-08-08T10:00:00Z",
	}
}

// integrationForm arma el formulario de la pantalla.
func integrationForm(events, endpoint, secret string, enabled bool) url.Values {
	form := url.Values{}
	form.Set("events_adapter", events)
	form.Set("endpoint_url", endpoint)
	form.Set("secret", secret)
	if enabled {
		form.Set("enabled", "on")
	}
	return form
}

// syncBuffer es un destino de log seguro para -race: el handler de slog puede escribir desde la
// goroutine del test y desde las del servidor fake.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs desvía el logger por defecto a un buffer durante el test. Se captura a nivel DEBUG para
// no dejar fuera ninguna línea: un secreto filtrado en un log de diagnóstico es un secreto filtrado.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestIntegrationsShowsStateWithoutSecret: la pantalla pinta el estado del puente —endpoint, chips,
// huella— y el campo del secreto sale VACÍO. El valor guardado, que el fake sí conoce, no está.
func TestIntegrationsShowsStateWithoutSecret(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if strings.Contains(out, testSecret) {
		t.Fatal("el secreto de firma no puede aparecer en el HTML")
	}
	if !strings.Contains(out, `value="https://crm.aurora.cl/wapp/eventos"`) {
		t.Error("debía pintarse el endpoint configurado")
	}
	if !strings.Contains(out, "huella a1b2c3d4") {
		t.Error("debía pintarse la huella del secreto (lo único que identifica al guardado)")
	}
	if !strings.Contains(out, `id="secret"`) || !strings.Contains(out, `type="password"`) {
		t.Error("debía ofrecerse el campo de escritura del secreto")
	}
	// El campo del secreto va sin valor: es de escritura pura.
	if !strings.Contains(out, `name="secret"`) || !strings.Contains(out, `value="" autocomplete="new-password"`) {
		t.Error("el campo del secreto debía salir vacío")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (server-side, CSP sin unsafe-inline)")
	}
}

// TestIntegrationsSecretNeverReachesHTMLOrLog es el requisito EXPLÍCITO de T5.2: el secreto que el
// operador teclea no puede acabar en el HTML renderizado ni en un log, en NINGUNO de los tres
// desenlaces del guardado —guardado bien, rechazado por la plataforma, o caído el upstream—, que son
// justo los tres sitios donde un re-pintado descuidado o un log de diagnóstico lo sacarían.
func TestIntegrationsSecretNeverReachesHTMLOrLog(t *testing.T) {
	cases := []struct {
		name     string
		override http.HandlerFunc
		wantCode int
	}{
		{
			name:     "guardado con éxito",
			wantCode: http.StatusOK,
		},
		{
			name: "rechazado por la plataforma",
			override: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"endpoint_url debe ser una URL absoluta http(s)"}`)
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "upstream caído",
			override: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"no se pudo guardar la integración"}`)
			},
			wantCode: http.StatusBadGateway,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			api := newIntegrationsServer(configuredRow(), "", "crm_bridge")
			api.override = tc.override
			defer api.close()

			form := integrationForm("webhook", "https://crm.aurora.cl/wapp/eventos", testSecret, true)
			rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", form, validSessionCookie(t))

			if rec.Code != tc.wantCode {
				t.Fatalf("status esperado %d, got %d", tc.wantCode, rec.Code)
			}
			if strings.Contains(rec.Body.String(), testSecret) {
				t.Error("el secreto tecleado NO puede volver en el HTML re-pintado")
			}
			if strings.Contains(logs.String(), testSecret) {
				t.Error("el secreto tecleado NO puede aparecer en el log")
			}
		})
	}
}

// TestIntegrationsSavePreservesCatalogAdapter: el PUT es un upsert COMPLETO, así que la pantalla
// —que solo edita los eventos— tiene que mandar el adaptador de CATÁLOGO que ya estaba. Si no, tocar
// el endpoint de eventos tumbaría el catálogo a «local» sin decírselo a nadie.
//
// Y el campo del secreto en blanco NO viaja: su ausencia es lo que significa «deja el que está».
func TestIntegrationsSavePreservesCatalogAdapter(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()

	form := integrationForm("webhook", "https://crm.aurora.cl/otra-ruta", "", true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el guardado debía responder 200, got %d", rec.Code)
	}

	sent := api.sentPut()
	if !strings.Contains(sent, `"catalog_adapter":"webhook"`) {
		t.Errorf("el PUT debía preservar el adaptador de catálogo guardado, got %s", sent)
	}
	if strings.Contains(sent, `"secret"`) {
		t.Errorf("el campo del secreto en blanco NO debe viajar (significa «deja el que está»), got %s", sent)
	}
	if !strings.Contains(sent, `"endpoint_url":"https://crm.aurora.cl/otra-ruta"`) {
		t.Errorf("el PUT debía llevar el endpoint nuevo, got %s", sent)
	}
	// El secreto sigue guardado tras un PUT sin secreto: la pantalla lo dice con la huella.
	if !strings.Contains(rec.Body.String(), "huella a1b2c3d4") {
		t.Error("tras guardar sin secreto, la pantalla debía seguir diciendo que hay uno")
	}
}

// TestIntegrationsSaveDoesNotWriteWhenCurrentUnreadable: si no se puede releer la configuración
// actual, NO se guarda nada. Guardar a ciegas pisaría el adaptador de catálogo con el default.
func TestIntegrationsSaveDoesNotWriteWhenCurrentUnreadable(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	// El GET cae; el PUT ni se intenta.
	api.mu.Lock()
	api.row = nil
	api.mu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("advisor_ai", "crm_bridge"))
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"caída"}`)
			return
		}
		t.Errorf("no debía llamarse al %s: sin la configuración actual no se guarda", r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	form := integrationForm("webhook", "https://crm.aurora.cl/wapp/eventos", testSecret, true)
	rec := postFormWithCookie(NewRouter(authTestCfg(srv.URL)), "/integrations", form, validSessionCookie(t))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("debía responder 502 al no poder leer la configuración actual, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No se ha guardado nada") {
		t.Error("la pantalla debía decir que no se guardó nada")
	}
}

// TestIntegrationsShowsPlatformRejection: el motivo del rechazo de la plataforma se le enseña al
// operador tal cual — es lo único que le dice qué corregir. El 422 (adaptador de catálogo diferido)
// conserva su código.
func TestIntegrationsShowsPlatformRejection(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	api.override = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"catalog.pull diferido: el adaptador de catálogo «http» todavía no está implementado; usa «local»"}`)
	}
	defer api.close()

	form := integrationForm("webhook", "https://crm.aurora.cl/wapp/eventos", "", true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", form, validSessionCookie(t))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("el 422 de la plataforma debía conservarse, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "catalog.pull diferido") {
		t.Error("el motivo del rechazo debía llegar al operador")
	}
}

// TestIntegrationsDeleteRemovesRowAndSaysSecretIsGone: quitar la integración devuelve al tenant a
// local/local y borra el secreto — es la única forma de retirarlo, y la pantalla lo dice.
func TestIntegrationsDeleteRemovesRowAndSaysSecretIsGone(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations/delete", url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el borrado debía responder 200, got %d", rec.Code)
	}
	if _, _, deletes := api.counts(); deletes != 1 {
		t.Fatalf("debía llamarse una vez al DELETE, got %d", deletes)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "el secreto de firma se borró") {
		t.Error("la pantalla debía decir que el secreto se borró con la fila")
	}
	if strings.Contains(out, "huella a1b2c3d4") {
		t.Error("tras quitar la integración no debe quedar rastro del secreto anterior")
	}
	// Ya no hay integración: la sección de quitar tampoco se ofrece.
	if strings.Contains(out, `id="section-integrations-remove"`) {
		t.Error("sin integración configurada no debe ofrecerse quitarla")
	}
}

// TestIntegrationsWithoutFeatureShowsPlanNotice (gate del Plan 040): sin `crm_bridge` no se emite NADA
// del frente —ni formulario, ni endpoint, ni huella, ni el enlace de la barra—, y NO se llama a la API:
// la plataforma cortaría con 403 en los tres verbos, también el GET.
func TestIntegrationsWithoutFeatureShowsPlanNotice(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "cart_basic")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, "El plan de este tenant no incluye el puente con un CRM.") {
		t.Error("sin la feature debía verse el estado «no disponible en tu plan»")
	}
	for _, forbidden := range []string{`name="secret"`, `name="endpoint_url"`, "huella a1b2c3d4",
		"crm.aurora.cl", `id="section-integrations-outbox"`} {
		if strings.Contains(out, forbidden) {
			t.Errorf("sin la feature, %q no debe emitirse en el HTML", forbidden)
		}
	}
	if strings.Contains(out, `href="/integrations"`) {
		t.Error("sin la feature, el enlace de la barra no debe emitirse")
	}
	if gets, _, _ := api.counts(); gets != 0 {
		t.Errorf("sin la feature no debía llamarse a la API de integraciones, got %d GET", gets)
	}
}

// TestIntegrationsPostWithoutFeatureIsRefused: un POST forzado sin la capacidad se rechaza aquí y no
// llega a la API. El formulario ni siquiera se emite, así que llegar aquí ya es un envío a mano.
func TestIntegrationsPostWithoutFeatureIsRefused(t *testing.T) {
	api := newIntegrationsServer(nil, "", "cart_basic")
	defer api.close()

	form := integrationForm("webhook", "https://crm.aurora.cl/wapp/eventos", testSecret, true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", form, validSessionCookie(t))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin la feature el guardado debía responder 403, got %d", rec.Code)
	}
	if _, puts, _ := api.counts(); puts != 0 {
		t.Errorf("sin la feature no debía llamarse al PUT, got %d", puts)
	}
	if strings.Contains(rec.Body.String(), testSecret) {
		t.Error("el secreto tecleado no puede volver en el HTML del rechazo")
	}
}

// TestIntegrationsIsPermanent (requisito explícito de T5.2): esta pantalla NO lleva la marca de
// provisionalidad de las de negocio — es capa técnica y se queda en el BFF (ADR-0035, doc 14
// D-03/D-14). Y con la feature contratada su enlace SÍ sale en la barra.
func TestIntegrationsIsPermanent(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t)).Body.String()
	for _, forbidden := range []string{"PROVISIONAL", "migra a la consola de administración", "ADR-0047"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("la pantalla de integraciones no debe llevar la marca provisional (%q)", forbidden)
		}
	}
	if !strings.Contains(out, `href="/integrations"`) {
		t.Error("con la feature contratada, la barra debía ofrecer el enlace a Integraciones")
	}
}

// TestIntegrationsUnconfiguredTenantGetsEmptyForm: un tenant sin fila ve el formulario vacío en
// local/local —no un error—, y sin la sección de quitar (no hay nada que quitar).
func TestIntegrationsUnconfiguredTenantGetsEmptyForm(t *testing.T) {
	api := newIntegrationsServer(nil, "", "crm_bridge")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un tenant sin integración debía ver la pantalla, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `name="endpoint_url"`) {
		t.Error("debía ofrecerse el formulario vacío")
	}
	if !strings.Contains(out, "sin secreto") {
		t.Error("debía decirse que todavía no hay secreto")
	}
	if strings.Contains(out, `id="section-integrations-remove"`) {
		t.Error("sin integración configurada no debe ofrecerse quitarla")
	}
}

// ==================== EL PANEL DE ENTREGAS (outbox) ====================

// TestIntegrationsOutboxPanelShowsCounters: el panel pinta los cuatro números y la antigüedad de lo
// más viejo que espera, en palabras y no en una fecha UTC que haya que restar de cabeza.
func TestIntegrationsOutboxPanelShowsCounters(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	api.setOutbox(storedOutbox{
		Pending: 3, Delivering: 1, Delivered: 128, Dead: 2,
		OldestPendingAt: time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339),
	})

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, quiero := range []string{
		"en cola · 3", "saliendo ahora · 1", "entregadas · 128", "no entregadas · 2",
	} {
		if !strings.Contains(out, quiero) {
			t.Errorf("falta el contador %q en el panel", quiero)
		}
	}
	if !strings.Contains(out, "lleva esperando 6 horas") {
		t.Errorf("la antigüedad debía salir en palabras; HTML:\n%s", out)
	}
	if api.outboxCount() != 1 {
		t.Errorf("consultas a la cola=%d, quiero 1", api.outboxCount())
	}
	// El aviso de las perdidas aparece porque hay: es lo único del panel que pide una acción.
	if !strings.Contains(out, "se dieron por perdidas") {
		t.Error("con entregas muertas debía salir el aviso que dice qué hacer")
	}
}

// TestIntegrationsOutboxPanelEmptyQueue: la cola al día se dice, no se deja en blanco. Y sin nada
// esperando NO se pinta antigüedad — no existe «la más antigua de ninguna».
func TestIntegrationsOutboxPanelEmptyQueue(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	api.setOutbox(storedOutbox{Delivered: 42})

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t)).Body.String()

	if !strings.Contains(out, "No hay nada esperando") {
		t.Errorf("la cola vacía debía decirse; HTML:\n%s", out)
	}
	if strings.Contains(out, "lleva esperando") {
		t.Error("sin nada en cola no se pinta antigüedad")
	}
	if strings.Contains(out, "se dieron por perdidas") {
		t.Error("sin entregas muertas no se alarma al operador")
	}
}

// TestIntegrationsOutboxFailureDoesNotBreakThePage es el criterio duro: un contador que no se puede
// leer NO puede tumbar la pantalla de configuración, que es justamente la que arregla el problema.
//
// Se comprueba además que el panel NO se disfraza de cero: un cero se lee como «todo al día», que es
// lo contrario de lo que pasa cuando la plataforma no contesta.
func TestIntegrationsOutboxFailureDoesNotBreakThePage(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	api.failOutbox(http.StatusInternalServerError)

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía seguir sirviendo 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	// El formulario —lo que arregla el puente— sigue estando entero.
	if !strings.Contains(out, `value="https://crm.aurora.cl/wapp/eventos"`) {
		t.Error("el formulario de configuración tenía que seguir sirviéndose")
	}
	if !strings.Contains(out, `name="secret"`) {
		t.Error("el campo del secreto tenía que seguir estando")
	}
	// Y el panel dice que no pudo, sin inventar números.
	if !strings.Contains(out, "No se pudo consultar el estado de las entregas") {
		t.Errorf("el panel debía decir que no pudo leerse; HTML:\n%s", out)
	}
	if strings.Contains(out, "en cola · 0") {
		t.Error("un contador ilegible se pintó como cero: eso se lee como «todo al día»")
	}
}

// TestIntegrationsOutbox403DoesNotBreakThePage: el otro fallo posible es de permisos. Mismo criterio
// —la página sigue— y mensaje propio, porque la acción que corresponde es distinta.
func TestIntegrationsOutbox403DoesNotBreakThePage(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	api.failOutbox(http.StatusForbidden)

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía seguir sirviendo 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no puede consultar el estado de las entregas") {
		t.Errorf("el 403 debía tener su propio mensaje; HTML:\n%s", rec.Body.String())
	}
}

// TestIntegrationsOutboxNotQueriedWithoutTheFeature: sin `crm_bridge` no se emite nada de esta
// pantalla, así que tampoco se gasta el viaje — la plataforma cortaría con 403 igualmente.
func TestIntegrationsOutboxNotQueriedWithoutTheFeature(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret) // sin features
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	if api.outboxCount() != 0 {
		t.Errorf("se consultó la cola sin la capacidad (%d veces)", api.outboxCount())
	}
	if strings.Contains(rec.Body.String(), "Entregas a tu sistema") {
		t.Error("sin la capacidad el panel no debe emitirse siquiera")
	}
}

// TestIntegrationsOutboxPanelAfterSave: el panel es estado de la PÁGINA, no de la operación. Tras
// guardar sigue estando —que es cuando más se mira— en vez de quedarse en blanco.
func TestIntegrationsOutboxPanelAfterSave(t *testing.T) {
	api := newIntegrationsServer(configuredRow(), testSecret, "crm_bridge")
	defer api.close()
	api.setOutbox(storedOutbox{Pending: 5, Delivered: 9})

	form := integrationForm("webhook", "https://crm.aurora.cl/wapp/eventos", "", true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/integrations", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el guardado debía renderizar 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "en cola · 5") {
		t.Errorf("el panel debía seguir pintándose tras guardar; HTML:\n%s", rec.Body.String())
	}
}

// TestDuraciónLegible fija la escala de la antigüedad, incluido el caso raro que un reloj desalineado
// produce: una duración negativa no puede pintar un número absurdo.
func TestDuraciónLegible(t *testing.T) {
	casos := []struct {
		d      time.Duration
		quiero string
	}{
		{-5 * time.Minute, "menos de un minuto"},
		{30 * time.Second, "menos de un minuto"},
		{time.Minute, "1 minuto"},
		{90 * time.Second, "1 minuto"},
		{45 * time.Minute, "45 minutos"},
		{time.Hour, "1 hora"},
		{6 * time.Hour, "6 horas"},
		{25 * time.Hour, "1 día"},
		{72 * time.Hour, "3 días"},
	}
	for _, c := range casos {
		if got := duraciónLegible(c.d); got != c.quiero {
			t.Errorf("duraciónLegible(%s)=%q, quiero %q", c.d, got, c.quiero)
		}
	}
}

// TestColaAgeAnteUnaMarcaRota: si la plataforma manda algo que no es una fecha, se calla la antigüedad
// en vez de inventarla. Los contadores siguen valiendo sin ella.
func TestColaAgeAnteUnaMarcaRota(t *testing.T) {
	if got := colaAge("no soy una fecha"); got != "" {
		t.Errorf("colaAge de una marca rota = %q, quiero vacío", got)
	}
	if got := colaAge(""); got != "" {
		t.Errorf("colaAge de vacío = %q, quiero vacío", got)
	}
}
