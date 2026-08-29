package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// testAPIKey es la credencial de estas pruebas. Es una cadena inconfundible a propósito: todo lo que
// estos tests buscan es que NO aparezca —ni en el HTML ni en el log—, y un valor corriente («abc»)
// daría falsos verdes al colarse dentro de cualquier otra palabra.
//
// Tiene además longitud admisible para la plataforma (16–512), para que un rechazo por tamaño no
// enmascare lo que se está midiendo.
const testAPIKey = "credencial-de-proveedor-INCONFUNDIBLE-0123456789"

// storedTenantLLM es la fila que el fake guarda. La credencial va aparte, como en la plataforma: no
// forma parte de lo que se devuelve.
//
// 🔴 No tiene campo de huella y no es un olvido: el DTO de la plataforma tampoco lo tiene. Una API key
// no tiene contraparte que comparar, y publicar su huella regalaría un oráculo de confirmación offline.
type storedTenantLLM struct {
	Configured  bool   `json:"configured"`
	Via         string `json:"via"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	KeySet      bool   `json:"key_set"`
	ConsentedAt string `json:"consented_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// tenantLLMServer es una API fake CON ESTADO que imita el contrato de /api/v1/tenant-llm: guarda lo
// que le manda el PUT y lo devuelve en el GET siguiente.
//
// LO IMPORTANTE ES QUE CONOCE LA CREDENCIAL Y NO LA DEVUELVE, igual que la plataforma: la guarda en un
// campo propio y en el JSON solo sale `key_set`. Así, si el BFF filtrara el valor, solo podría venir de
// lo que el operador tecleó —que es exactamente el descuido que estos tests vigilan.
type tenantLLMServer struct {
	mu      sync.Mutex
	row     *storedTenantLLM // nil = tenant sin fila (default: vía local)
	apiKey  string           // la credencial GUARDADA; jamás sale en una respuesta
	gets    int
	puts    int
	deletes int
	lastPut string // cuerpo crudo del último PUT

	features []string
	// getStatus, si no es cero, corta el GET con ese código (para el caso del rol sin scope de lectura).
	getStatus int
	// override, si está puesto, contesta el PUT en lugar de guardar (para probar rechazos).
	override http.HandlerFunc
	srv      *httptest.Server
}

func newTenantLLMServer(row *storedTenantLLM, apiKey string, features ...string) *tenantLLMServer {
	ts := &tenantLLMServer{row: row, apiKey: apiKey, features: features}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("advisor_ai", ts.features...))
			return
		}
		if r.URL.Path != "/api/v1/tenant-llm" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
			return
		}

		ts.mu.Lock()
		defer ts.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			ts.gets++
			if ts.getStatus != 0 {
				w.WriteHeader(ts.getStatus)
				_, _ = io.WriteString(w, `{"error":"no se pudo leer la configuración LLM"}`)
				return
			}
		case http.MethodDelete:
			ts.deletes++
			ts.row = nil
			ts.apiKey = ""
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			ts.lastPut = string(body)
			ts.puts++
			if ts.override != nil {
				ts.override(w, r)
				return
			}
			var req struct {
				Via       string `json:"via"`
				Provider  string `json:"provider"`
				Model     string `json:"model"`
				APIKey    string `json:"api_key"`
				Consented bool   `json:"consented"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"cuerpo ilegible"}`)
				return
			}
			// Reemplazo COMPLETO, igual que la plataforma: la vía local retira la credencial guardada
			// en vez de conservarla «por si vuelve» (no hay semántica de «deja la que está»).
			if req.Via != "api" {
				ts.apiKey = ""
				ts.row = &storedTenantLLM{Configured: true, Via: req.Via, UpdatedAt: "2026-08-29T10:00:00Z"}
				break
			}
			ts.apiKey = req.APIKey
			ts.row = &storedTenantLLM{
				Configured:  true,
				Via:         "api",
				Provider:    req.Provider,
				Model:       req.Model,
				KeySet:      ts.apiKey != "",
				ConsentedAt: "2026-08-29T10:00:00Z",
				UpdatedAt:   "2026-08-29T10:00:00Z",
			}
		}

		if ts.row == nil {
			// El tenant sin fila responde 200 con la vía local, que NO es «ninguna vía»: es el default
			// del producto.
			_, _ = io.WriteString(w, `{"configured":false,"via":"local","key_set":false}`)
			return
		}
		out, _ := json.Marshal(ts.row)
		_, _ = w.Write(out)
	}))
	return ts
}

func (ts *tenantLLMServer) close() { ts.srv.Close() }

func (ts *tenantLLMServer) counts() (gets, puts, deletes int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.gets, ts.puts, ts.deletes
}

func (ts *tenantLLMServer) sentPut() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.lastPut
}

// failGet hace que el GET conteste con ese código (el caso del rol `operator`, sin `llm.read`).
func (ts *tenantLLMServer) failGet(status int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.getStatus = status
}

// configuredLLMRow es una configuración ya puesta: vía api, con proveedor, modelo y credencial.
func configuredLLMRow() *storedTenantLLM {
	return &storedTenantLLM{
		Configured:  true,
		Via:         "api",
		Provider:    "anthropic",
		Model:       "claude-sonnet-4-5",
		KeySet:      true,
		ConsentedAt: "2026-08-20T09:00:00Z",
		UpdatedAt:   "2026-08-29T10:00:00Z",
	}
}

// tenantLLMForm arma el formulario de la pantalla.
func tenantLLMForm(via, provider, model, apiKey string, consented bool) url.Values {
	form := url.Values{}
	form.Set("via", via)
	form.Set("provider", provider)
	form.Set("model", model)
	form.Set("api_key", apiKey)
	if consented {
		form.Set("consented", "on")
	}
	return form
}

// TestTenantLLMShowsStateWithoutKey: la pantalla pinta el estado —vía, proveedor, modelo, si hay
// credencial— y el campo de la credencial sale VACÍO. El valor guardado, que el fake sí conoce, no está.
func TestTenantLLMShowsStateWithoutKey(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if strings.Contains(out, testAPIKey) {
		t.Fatal("la credencial del proveedor no puede aparecer en el HTML")
	}
	if !strings.Contains(out, `value="claude-sonnet-4-5"`) {
		t.Error("debía pintarse el modelo configurado")
	}
	if !strings.Contains(out, "credencial guardada") {
		t.Error("debía decirse que hay credencial guardada (lo único que la API publica de ella)")
	}
	if !strings.Contains(out, `id="api_key"`) || !strings.Contains(out, `type="password"`) {
		t.Error("debía ofrecerse el campo de escritura de la credencial")
	}
	// El campo de la credencial va sin valor: es de escritura pura.
	if !strings.Contains(out, `name="api_key"`) || !strings.Contains(out, `value="" autocomplete="new-password"`) {
		t.Error("el campo de la credencial debía salir vacío")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (server-side, CSP sin unsafe-inline)")
	}
}

// TestTenantLLMKeyNeverReachesHTMLOrLog es el requisito EXPLÍCITO de T3.4: la credencial que el
// operador teclea no puede acabar en el HTML renderizado ni en un log, en NINGUNO de los tres
// desenlaces del guardado —guardado bien, rechazado por la plataforma, o caído el upstream—, que son
// justo los tres sitios donde un re-pintado descuidado o un log de diagnóstico la sacarían.
func TestTenantLLMKeyNeverReachesHTMLOrLog(t *testing.T) {
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
				_, _ = io.WriteString(w, `{"error":"model es obligatorio y no puede pasar de 128 caracteres"}`)
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "upstream caído",
			override: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"no se pudo guardar la configuración LLM"}`)
			},
			wantCode: http.StatusBadGateway,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			api := newTenantLLMServer(configuredLLMRow(), "", "api_llm")
			api.override = tc.override
			defer api.close()

			form := tenantLLMForm("api", "anthropic", "claude-sonnet-4-5", testAPIKey, true)
			rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))

			if rec.Code != tc.wantCode {
				t.Fatalf("status esperado %d, got %d; body=%s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), testAPIKey) {
				t.Error("la credencial tecleada NO puede volver en el HTML re-pintado")
			}
			if strings.Contains(logs.String(), testAPIKey) {
				t.Error("la credencial tecleada NO puede aparecer en el log")
			}
		})
	}
}

// TestTenantLLMViaLocalSavesWithoutProviderModelOrKey fija LA ASIMETRÍA DE LA API, que es fácil de
// romper sin querer: elegir la vía local no exige NADA —ni consentimiento, ni proveedor, ni modelo, ni
// credencial— y por eso ninguno de esos campos debe viajar en el PUT.
//
// El formulario los manda todos rellenos a propósito: el filtro está en el servidor, no en el HTML. Si
// alguno se colara, la plataforma guardaría una fila local con proveedor —una contradicción escrita en
// la base— o, peor, se mandaría una credencial por un cable que no la necesita.
func TestTenantLLMViaLocalSavesWithoutProviderModelOrKey(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()

	form := tenantLLMForm("local", "anthropic", "claude-sonnet-4-5", testAPIKey, true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la vía local debía guardarse sin exigir nada, got %d; body=%s", rec.Code, rec.Body.String())
	}

	sent := api.sentPut()
	if !strings.Contains(sent, `"via":"local"`) {
		t.Errorf("el PUT debía llevar la vía local, got %s", sent)
	}
	for _, prohibido := range []string{`"provider"`, `"model"`, `"api_key"`, testAPIKey} {
		if strings.Contains(sent, prohibido) {
			t.Errorf("la vía local no debe mandar %q; cuerpo del PUT: %s", prohibido, sent)
		}
	}
	if strings.Contains(sent, `"consented":true`) {
		t.Errorf("la vía local no consiente nada: no hay tercero a quien mandarle texto; cuerpo: %s", sent)
	}
	if !strings.Contains(rec.Body.String(), "no sale hacia ningún tercero") {
		t.Errorf("la confirmación debía decir qué significa la vía local; HTML:\n%s", rec.Body.String())
	}
}

// TestTenantLLMViaAPISendsTheWholePhoto es el reverso del anterior: con la vía `api` viajan los cuatro
// campos, porque el PUT es un reemplazo COMPLETO y la plataforma exige la credencial en cada uno.
func TestTenantLLMViaAPISendsTheWholePhoto(t *testing.T) {
	api := newTenantLLMServer(nil, "", "api_llm")
	defer api.close()

	form := tenantLLMForm("api", "gemini", "gemini-2.5-pro", testAPIKey, true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el guardado debía responder 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	sent := api.sentPut()
	for _, quiero := range []string{`"via":"api"`, `"provider":"gemini"`, `"model":"gemini-2.5-pro"`, `"consented":true`} {
		if !strings.Contains(sent, quiero) {
			t.Errorf("el PUT de la vía api debía llevar %s, got %s", quiero, sent)
		}
	}
	// La credencial sí viaja (es lo único que la pone), pero no vuelve.
	if !strings.Contains(sent, testAPIKey) {
		t.Errorf("la credencial debía viajar en el PUT, got %s", sent)
	}
	if strings.Contains(rec.Body.String(), testAPIKey) {
		t.Error("la credencial no puede volver en el HTML tras guardarla")
	}
	if !strings.Contains(rec.Body.String(), "credencial guardada") {
		t.Error("tras guardar, la pantalla debía decir que hay credencial")
	}
}

// TestTenantLLMShowsPlatformRejection: el motivo del rechazo llega al operador en palabras que pueda
// leer. La plataforma manda `consent_required` —un CÓDIGO, no una frase—, y enseñárselo tal cual sería
// enseñarle a la dueña de un local una palabra de máquina y esperar que sepa qué hacer.
//
// Se comprueba además que el código crudo NO se emite: el cuerpo del upstream no acaba en pantalla.
func TestTenantLLMShowsPlatformRejection(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	api.override = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"consent_required","detail":"hay que consentir explícitamente"}`)
	}
	defer api.close()

	// Sin marcar la casilla: es exactamente el caso que la plataforma rechaza.
	form := tenantLLMForm("api", "anthropic", "claude-sonnet-4-5", testAPIKey, false)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el 400 de la plataforma debía conservarse, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Falta tu consentimiento") {
		t.Errorf("el rechazo debía llegar al operador en palabras legibles; HTML:\n%s", out)
	}
	if strings.Contains(out, "consent_required") {
		t.Error("el código de máquina del upstream no debe emitirse en la pantalla")
	}
	if strings.Contains(out, testAPIKey) {
		t.Error("la credencial tecleada no puede volver en el HTML del rechazo")
	}
}

// TestTenantLLMRechazoNoDevuelveElConsentimientoMarcado vigila el defecto que se descubrió PISANDO la
// pantalla en UAT el 2026-08-29, con todos los tests de este fichero en verde.
//
// La plantilla marcaba la casilla del consentimiento con `IsAPI`, es decir: con la VÍA elegida. Así que
// un rechazo POR FALTA DE CONSENTIMIENTO devolvía el formulario CON EL CONSENTIMIENTO YA MARCADO, y el
// siguiente clic en «Guardar» autorizaba que el texto de los clientes saliera hacia un tercero sin que
// nadie lo hubiera decidido. Elegir proveedor externo NO es consentir: son dos decisiones distintas, y
// esa casilla existe justo para separarlas.
//
// 🔴 El test tiene DOS mitades a propósito. La negativa sola sería vacua: pasaría también si la casilla
// no se marcara NUNCA, que rompería el re-pintado de quien sí consintió y falló por otra cosa.
func TestTenantLLMRechazoNoDevuelveElConsentimientoMarcado(t *testing.T) {
	casos := []struct {
		nombre    string
		consented bool
		upstream  string
		quiere    bool
	}{
		{
			nombre:    "sin consentir: la casilla vuelve VACÍA",
			consented: false,
			upstream:  `{"error":"consent_required","detail":"hay que consentir explícitamente"}`,
			quiere:    false,
		},
		{
			nombre:    "consintió y falló por otra cosa: la casilla se conserva marcada",
			consented: true,
			upstream:  `{"error":"model es obligatorio y no puede pasar de 128 caracteres"}`,
			quiere:    true,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
			api.override = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, c.upstream)
			}
			defer api.close()

			form := tenantLLMForm("api", "anthropic", "claude-sonnet-4-5", testAPIKey, c.consented)
			rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))

			tag := tagDelConsentimiento(t, rec.Body.String())
			if marcada := strings.Contains(tag, "checked"); marcada != c.quiere {
				t.Errorf("casilla marcada = %v, quiero %v; el input era:\n%s", marcada, c.quiere, tag)
			}
		})
	}
}

// tagDelConsentimiento devuelve el `<input>` del consentimiento entero, para poder afirmar sobre SU
// atributo `checked` y no sobre cualquier «checked» que aparezca en otra parte del HTML.
func tagDelConsentimiento(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, `id="consented"`)
	if i < 0 {
		t.Fatalf("la pantalla no trae el input del consentimiento; HTML:\n%s", html)
	}
	inicio := strings.LastIndex(html[:i], "<")
	fin := strings.Index(html[i:], ">")
	if inicio < 0 || fin < 0 {
		t.Fatal("no se pudo delimitar el input del consentimiento")
	}
	return html[inicio : i+fin+1]
}

// TestTenantLLMRejectionKeepsLegiblePlatformMessages es la otra mitad: los 400 que YA vienen redactados
// (los de modelo y credencial) se enseñan tal cual, porque son lo único que dice qué corregir.
//
// 🔴 Y ese mensaje evita a propósito decir la longitud real de la credencial: la plataforma no quiere
// ser un medidor. El BFF no lo «mejora» ni añade el suyo.
func TestTenantLLMRejectionKeepsLegiblePlatformMessages(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	api.override = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w,
			`{"error":"api_key es obligatoria en cada PUT y debe tener entre 16 y 512 caracteres"}`)
	}
	defer api.close()

	form := tenantLLMForm("api", "anthropic", "claude-sonnet-4-5", "corta", true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el 400 de la plataforma debía conservarse, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "debe tener entre 16 y 512 caracteres") {
		t.Errorf("el motivo redactado por la plataforma debía llegar tal cual; HTML:\n%s", rec.Body.String())
	}
}

// TestTenantLLMDeleteRemovesRowAndSaysBothAreGone: quitar la configuración devuelve al tenant a la vía
// local y borra la credencial Y el consentimiento — viven en la misma fila y se van juntos.
func TestTenantLLMDeleteRemovesRowAndSaysBothAreGone(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm/delete", url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el borrado debía responder 200, got %d", rec.Code)
	}
	if _, _, deletes := api.counts(); deletes != 1 {
		t.Fatalf("debía llamarse una vez al DELETE, got %d", deletes)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "la credencial y el consentimiento se borraron juntos") {
		t.Errorf("la pantalla debía decir que se van las dos cosas; HTML:\n%s", out)
	}
	if strings.Contains(out, "credencial guardada") {
		t.Error("tras quitar la configuración no debe quedar rastro de la credencial anterior")
	}
	// Ya no hay configuración: la sección de quitar tampoco se ofrece.
	if strings.Contains(out, `id="section-tenant-llm-remove"`) {
		t.Error("sin configuración puesta no debe ofrecerse quitarla")
	}
}

// TestTenantLLMWithoutFeatureShowsPlanNotice (gate del Plan 040 · D-040.6): sin `api_llm` no se emite
// NADA del frente —ni formulario, ni proveedor, ni modelo, ni el estado de la credencial, ni el enlace
// de la barra—, y NO se llama a la API: la plataforma cortaría con 403 en los tres verbos, también el
// GET.
//
// 🔴 «No se ve» no basta: se comprueba que el HTML NO LO CONTIENE. Un bloque escondido con CSS lo
// destapa cualquiera con el inspector.
func TestTenantLLMWithoutFeatureShowsPlanNotice(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "cart_basic")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, "El plan de este tenant no incluye configurar un proveedor de IA propio.") {
		t.Error("sin la feature debía verse el estado «no disponible en tu plan»")
	}
	for _, forbidden := range []string{`name="api_key"`, `name="model"`, `name="provider"`, `name="via"`,
		"claude-sonnet-4-5", "credencial guardada", `id="section-tenant-llm"`} {
		if strings.Contains(out, forbidden) {
			t.Errorf("sin la feature, %q no debe emitirse en el HTML", forbidden)
		}
	}
	if strings.Contains(out, `href="/tenant-llm"`) {
		t.Error("sin la feature, el enlace de la barra no debe emitirse")
	}
	if gets, _, _ := api.counts(); gets != 0 {
		t.Errorf("sin la feature no debía llamarse a la API de configuración LLM, got %d GET", gets)
	}
}

// TestTenantLLMPostWithoutFeatureIsRefused: un POST forzado sin la capacidad se rechaza aquí y no llega
// a la API. El formulario ni siquiera se emite, así que llegar aquí ya es un envío a mano.
func TestTenantLLMPostWithoutFeatureIsRefused(t *testing.T) {
	api := newTenantLLMServer(nil, "", "cart_basic")
	defer api.close()

	form := tenantLLMForm("api", "anthropic", "claude-sonnet-4-5", testAPIKey, true)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", form, validSessionCookie(t))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin la feature el guardado debía responder 403, got %d", rec.Code)
	}
	if _, puts, _ := api.counts(); puts != 0 {
		t.Errorf("sin la feature no debía llamarse al PUT, got %d", puts)
	}
	if strings.Contains(rec.Body.String(), testAPIKey) {
		t.Error("la credencial tecleada no puede volver en el HTML del rechazo")
	}
}

// TestTenantLLMDeleteWithoutFeatureIsRefused: lo mismo por el otro verbo de escritura. Va aparte porque
// es OTRA guarda en OTRO handler, y un gate que solo cubriera el guardado dejaría la credencial
// borrable por un envío a mano.
func TestTenantLLMDeleteWithoutFeatureIsRefused(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "cart_basic")
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm/delete", url.Values{}, validSessionCookie(t))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin la feature el borrado debía responder 403, got %d", rec.Code)
	}
	if _, _, deletes := api.counts(); deletes != 0 {
		t.Errorf("sin la feature no debía llamarse al DELETE, got %d", deletes)
	}
}

// TestTenantLLMIsPermanent (INV-01 del Plan 047): esta pantalla NO lleva la marca de provisionalidad de
// las de negocio — es capa técnica y se queda en el BFF (ADR-0035, D-047.5/D-047.9). Y con la feature
// contratada su enlace SÍ sale en la barra.
func TestTenantLLMIsPermanent(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t)).Body.String()
	for _, forbidden := range []string{"PROVISIONAL", "migra a la consola de administración", "ADR-0047"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("la pantalla de configuración LLM no debe llevar la marca provisional (%q)", forbidden)
		}
	}
	if !strings.Contains(out, `href="/tenant-llm"`) {
		t.Error("con la feature contratada, la barra debía ofrecer el enlace al proveedor de IA")
	}
}

// TestTenantLLMUnconfiguredTenantGetsEmptyForm: un tenant sin fila ve el formulario vacío en la vía
// local —no un error—, y sin la sección de quitar (no hay nada que quitar).
func TestTenantLLMUnconfiguredTenantGetsEmptyForm(t *testing.T) {
	api := newTenantLLMServer(nil, "", "api_llm")
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un tenant sin configuración debía ver la pantalla, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `name="model"`) || !strings.Contains(out, `name="api_key"`) {
		t.Error("debía ofrecerse el formulario vacío")
	}
	if !strings.Contains(out, "sin credencial") {
		t.Error("debía decirse que todavía no hay credencial")
	}
	if !strings.Contains(out, "interpreta · el equipo de tu local") {
		t.Error("un tenant sin fila está en la vía local por defecto, y debía decirse")
	}
	if strings.Contains(out, `id="section-tenant-llm-remove"`) {
		t.Error("sin configuración puesta no debe ofrecerse quitarla")
	}
}

// TestTenantLLMFormWarnsAboutRetypingTheKey es la decisión de producto de T3.4: como el servidor NO
// devuelve la credencial, cualquier guardado con vía `api` obliga a volver a escribirla entera. Eso se
// ACEPTA y se AVISA, y el aviso tiene que estar EN SU SITIO —junto al campo, en el cuerpo del texto—,
// no escondido en un tooltip ni en letra pequeña.
//
// El test mira dos cosas: que el aviso se emite, y que va en un `<p class="body">` (el estilo del
// cuerpo) y no en un `supporting` (la letra pequeña de esta consola).
func TestTenantLLMFormWarnsAboutRetypingTheKey(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t)).Body.String()

	if !strings.Contains(out, "tienes que volver a escribir la credencial completa") {
		t.Errorf("el formulario debía avisar del re-tecleo de la credencial; HTML:\n%s", out)
	}
	if !strings.Contains(out, `<p class="body" id="aviso-reteclear-credencial">`) {
		t.Error("el aviso del re-tecleo debe ir en el cuerpo del texto, no en la letra pequeña")
	}
	// Y va donde se teclea: entre el campo de la credencial y el final del formulario.
	iCampo := strings.Index(out, `id="api_key"`)
	iAviso := strings.Index(out, `id="aviso-reteclear-credencial"`)
	if iCampo < 0 || iAviso < 0 || iAviso < iCampo {
		t.Errorf("el aviso debía ir junto al campo de la credencial (campo=%d, aviso=%d)", iCampo, iAviso)
	}
}

// TestTenantLLMReadForbiddenIsLegible es el caso del rol `operator`, que NO tiene ninguno de los dos
// scopes (`llm.read`/`llm.write`) aunque su tenant sí tenga la feature: la plataforma le contesta 403.
//
// La pantalla tiene que decirlo con palabras y seguir sirviendo. Una página rota con un 403 crudo deja
// al operador sin saber si el problema es suyo, de su plan o del sistema.
func TestTenantLLMReadForbiddenIsLegible(t *testing.T) {
	api := newTenantLLMServer(configuredLLMRow(), testAPIKey, "api_llm")
	defer api.close()
	api.failGet(http.StatusForbidden)

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/tenant-llm", validSessionCookie(t))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("el 403 de la plataforma debía conservarse, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Tu usuario no puede consultar el proveedor de IA de este tenant") {
		t.Errorf("el 403 debía tener su propio mensaje legible; HTML:\n%s", out)
	}
	if strings.Contains(out, "no se pudo leer la configuración LLM") {
		t.Error("el cuerpo del upstream no debe acabar en la pantalla")
	}
	// Sin datos no se inventa un formulario que parezca decir el estado del tenant.
	if strings.Contains(out, "credencial guardada") {
		t.Error("sin poder leer, la pantalla no puede afirmar que hay credencial guardada")
	}
}
