package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// routedAPI levanta una API pública fake que responde según método+ruta. Cada entrada del mapa es
// "MÉTODO /ruta" → (status, body). Una ruta no mapeada responde 500 (fuerza al test a declarar lo que usa).
func routedAPI(routes map[string]struct {
	status int
	body   string
}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if resp, ok := routes[r.Method+" "+r.URL.Path]; ok {
			w.WriteHeader(resp.status)
			_, _ = io.WriteString(w, resp.body)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
}

// validSessionCookie arma la cookie de sesión con un access token vigente (para pasar el AuthMiddleware).
func validSessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	access := makeToken(t, time.Now().Add(time.Hour))
	value, err := encodeSession(sessionData{AccessToken: access, RefreshToken: "r-ok"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: value}
}

func getWithCookie(router http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}

func postFormWithCookie(router http.Handler, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	csrf := mintCSRF(router)
	form.Set(csrfFieldName, csrf.Value)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}

// TestDashboardRendersSessionTable: GET / con sesiones del fixture → la tabla las pinta (REQ-D1).
func TestDashboardRendersSessionTable(t *testing.T) {
	body := `[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","profile":"active","self_pn":"593999000111"},` +
		`{"session_id":"s-2","edge_id":"edge-beta","state":"offline","profile":"passive"}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"593999000111", "edge-alpha", "online", "edge-beta", "offline", "Perfil"} {
		if !strings.Contains(out, want) {
			t.Errorf("la tabla debía contener %q", want)
		}
	}
	// Sin número (s-2) cae al session_id en la primera columna.
	if !strings.Contains(out, "s-2") {
		t.Error("la sesión sin self_pn debía mostrar su session_id")
	}
}

// TestDashboardDegradesWhenListFails: si ListSessions falla → aviso degradado + input manual de session_id
// (REQ-D4).
func TestDashboardDegradesWhenListFails(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusInternalServerError, `{"error":"boom"}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("el dashboard degradado debía seguir sirviendo 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "No se pudieron cargar las sesiones") {
		t.Error("el modo degradado debía avisar del fallo del listado")
	}
	// En degradado el envío usa un input de texto para el session_id (no un <select>).
	if !strings.Contains(out, `id="session_id"`) || strings.Contains(out, "<select") {
		t.Error("en degradado el session_id debía introducirse a mano (input, no select)")
	}
	// No se filtra el detalle crudo del upstream.
	if strings.Contains(out, "boom") {
		t.Error("no debe filtrarse el detalle del upstream")
	}
}

// TestSendShowsAck: POST /send con la API devolviendo 200 ok → muestra el acked_command_id (REQ-D3).
func TestSendShowsAck(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/messages": {http.StatusOK, `{"acked_command_id":"cmd-abc123","ok":true}`},
		"GET /api/v1/sessions":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"session_id": {"s-1"}, "to": {"+593999000111"}, "text": {"hola"}}
	rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("envío OK debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Error("envío OK debía mostrar un snackbar de éxito")
	}
	if !strings.Contains(out, "cmd-abc123") {
		t.Error("envío OK debía mostrar el acked_command_id")
	}
}

// TestSendMapsBusinessErrors: 404/502/504 se traducen a mensajes legibles, sin trazas (REQ-D3).
func TestSendMapsBusinessErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"ajena", http.StatusNotFound, "no es tuya o no existe"},
		{"offline", http.StatusBadGateway, "está desconectado"},
		{"timeout", http.StatusGatewayTimeout, "tardó demasiado"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := routedAPI(map[string]struct {
				status int
				body   string
			}{
				"POST /api/v1/messages": {tc.status, `{"error":"detalle interno que no debe verse"}`},
				"GET /api/v1/sessions":  {http.StatusOK, `[]`},
			})
			defer api.Close()

			router := NewRouter(authTestCfg(api.URL))
			form := url.Values{"session_id": {"s-1"}, "to": {"+1"}, "text": {"hola"}}
			rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

			out := rec.Body.String()
			if !strings.Contains(out, "snackbar--error") {
				t.Errorf("%s debía mostrar un snackbar de error", tc.name)
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("%s debía mostrar %q; body=%s", tc.name, tc.wantSubstr, out)
			}
			if strings.Contains(out, "detalle interno que no debe verse") {
				t.Errorf("%s no debía filtrar el detalle del upstream", tc.name)
			}
		})
	}
}

// TestSendValidatesEmptyFields: campos vacíos → error legible sin llamar a la API.
func TestSendValidatesEmptyFields(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"session_id": {""}, "to": {""}, "text": {""}}
	rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

	if !strings.Contains(rec.Body.String(), "Elige una sesión") {
		t.Error("campos vacíos debían pedir completar el formulario")
	}
}

// TestDashboardPintaElPerfilEnCastellano es el gate (b) de T1.3 (Plan 046 · ADR-0027).
//
// Lo que decide NO es que aparezca la palabra «pasiva» en algún sitio de la página —el párrafo de
// apoyo la nombra, así que una comprobación así seguiría verde con el desplegable intacto en inglés—.
// Lo que decide es que el TEXTO DEL <option> esté en castellano y el identificador en inglés siga
// viajando en el `value`: `>pasiva<` presente y `>passive<` ausente. Esa pareja de aserciones es la
// que distingue «se tradujo la vista» de «se renombró el dato», y solo la primera es lo que pide el
// ADR-0027 (el vocabulario del dueño cambia; el identificador del cable, no).
func TestDashboardPintaElPerfilEnCastellano(t *testing.T) {
	body := `[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","profile":"passive"}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	// (b) El criterio literal de la tarea.
	if !strings.Contains(out, ">pasiva<") {
		t.Error("el <option> del perfil debía decir «pasiva» al dueño")
	}
	if strings.Contains(out, ">passive<") {
		t.Error("el <option> NO puede seguir enseñando el identificador «passive» como texto")
	}
	if !strings.Contains(out, ">activa<") {
		t.Error("el <option> del perfil debía ofrecer «activa» al dueño")
	}

	// La otra mitad: el identificador sí viaja por el cable. Si esto se cayera, la traducción se
	// habría comido el dato en vez de la etiqueta.
	if !strings.Contains(out, `value="passive"`) || !strings.Contains(out, `value="active"`) {
		t.Error("los identificadores active/passive deben seguir viajando en el value del <option>")
	}
	if !strings.Contains(out, `action="/sessions/s-1/profile"`) {
		t.Error("el formulario debía apuntar a la ruta nueva /profile")
	}
	if !strings.Contains(out, "<th>Perfil</th>") {
		t.Error("la columna debía titularse «Perfil», no «Rol»")
	}
	// El perfil que trae la sesión sale preseleccionado.
	if !strings.Contains(out, `<option value="passive" selected>pasiva</option>`) {
		t.Error("el desplegable debía preseleccionar el perfil que la plataforma reportó")
	}
}

// TestDashboardNoPrometeLaPrivacidadQueAunNoEntrega. El filtrado de entrantes en el Edge —la promesa
// de fondo del perfil pasiva, ADR-0027 §Decisión 2— lo entrega la OLA 2 y hoy NO EXISTE: lo único que
// «pasiva» apaga es el motor reactivo en la nube. Una consola que dijera «sus mensajes no salen del
// equipo» estaría publicando una garantía de privacidad falsa, que es peor que no decir nada.
//
// Este test es el que impide que alguien «mejore» el texto quitándole el matiz. Se cae solo el día que
// la Ola 2 esté en campo, que es justo el día en que hay que volver a este párrafo.
func TestDashboardNoPrometeLaPrivacidadQueAunNoEntrega(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","profile":"passive"}]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	out := rec.Body.String()

	if !strings.Contains(out, "todavía no está disponible") {
		t.Error("mientras el filtrado del Edge no exista, la consola tiene que decirlo")
	}
	if !strings.Contains(out, "sigue llegando a la nube") {
		t.Error("el aviso tiene que decir QUÉ pasa hoy con los entrantes de una pasiva, no solo que falta algo")
	}
}

// TestSetProfileSuccess: POST /sessions/{id}/profile con la API devolviendo 200 → snackbar de éxito y la
// tabla re-listada ya pinta el perfil nuevo (el fixture del GET responde el estado post-cambio).
func TestSetProfileSuccess(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/sessions/s-1/profile": {http.StatusOK, `{"session_id":"s-1","profile":"passive"}`},
		"GET /api/v1/sessions": {http.StatusOK,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","profile":"passive"}]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"profile": {"passive"}}
	rec := postFormWithCookie(router, "/sessions/s-1/profile", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("cambio de perfil OK debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Error("cambio de perfil OK debía mostrar un snackbar de éxito")
	}
	// El snackbar habla el idioma del dueño, no el del cable: «pasiva», nunca «passive».
	if !strings.Contains(out, "Perfil de la sesión cambiado a pasiva") {
		t.Error("el snackbar debía nombrar el perfil nuevo en castellano")
	}
	// El re-render re-lista: el <select> de la fila trae pasiva seleccionada.
	if !strings.Contains(out, `<option value="passive" selected>pasiva</option>`) {
		t.Error("la tabla re-listada debía preseleccionar el perfil nuevo")
	}
}

// TestSetProfileRejectsInvalidProfile: un perfil fuera de {active, passive} se rechaza client-side (400)
// SIN llamar a la API (la ruta del POST no está mapeada en el fake: si se llamara, el 500 rompería el
// mensaje esperado).
//
// El caso elegido es "bot" a propósito: es el identificador VIEJO, y el que va a llegar si alguien
// reenvía un formulario cacheado o un marcador antiguo. Tiene que rebotar como cualquier otro valor
// inválido, no colarse por nostalgia.
func TestSetProfileRejectsInvalidProfile(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	for _, invalido := range []string{"admin", "bot"} {
		form := url.Values{"profile": {invalido}}
		rec := postFormWithCookie(router, "/sessions/s-1/profile", form, validSessionCookie(t))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("el perfil %q debía responder 400, got %d", invalido, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Elige un perfil válido") {
			t.Errorf("el perfil %q debía pedir activa o pasiva", invalido)
		}
	}
}

// TestSetProfileMapsUpstreamErrors: 400/404/500 del upstream se traducen a mensajes legibles, sin filtrar
// el detalle crudo (mismo criterio REQ-D3 que /send).
func TestSetProfileMapsUpstreamErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"rechazo", http.StatusBadRequest, "La plataforma rechazó el perfil"},
		{"ajena", http.StatusNotFound, "no es tuya o no existe"},
		{"upstream", http.StatusInternalServerError, "No se pudo cambiar el perfil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := routedAPI(map[string]struct {
				status int
				body   string
			}{
				"POST /api/v1/sessions/s-1/profile": {tc.status, `{"error":"detalle interno que no debe verse"}`},
				"GET /api/v1/sessions":              {http.StatusOK, `[]`},
			})
			defer api.Close()

			router := NewRouter(authTestCfg(api.URL))
			form := url.Values{"profile": {"active"}}
			rec := postFormWithCookie(router, "/sessions/s-1/profile", form, validSessionCookie(t))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s debía responder 400, got %d", tc.name, rec.Code)
			}
			out := rec.Body.String()
			if !strings.Contains(out, "snackbar--error") {
				t.Errorf("%s debía mostrar un snackbar de error", tc.name)
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("%s debía mostrar %q", tc.name, tc.wantSubstr)
			}
			if strings.Contains(out, "detalle interno que no debe verse") {
				t.Errorf("%s no debía filtrar el detalle del upstream", tc.name)
			}
		})
	}
}

// TestSetProfileWithoutCookieRedirects: POST /sessions/{id}/profile sin cookie → redirect a /login (ruta
// protegida por el AuthMiddleware, como el resto del dashboard).
func TestSetProfileWithoutCookieRedirects(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	rec := postFormWithCookie(router, "/sessions/s-1/profile", url.Values{"profile": {"active"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("POST /sessions/{id}/profile sin cookie debía redirigir a /login, got %d %q",
			rec.Code, rec.Header().Get("Location"))
	}
}

// TestDashboardCaeAlRoleViejoMientrasDuraLaDeprecacion cubre EffectiveProfile por su motivo real: el BFF
// y la plataforma NO se despliegan a la vez (el trade-off que T1.2 dejó escrito). Un BFF ya migrado
// contra una plataforma que todavía solo emite `role` tiene que seguir preseleccionando bien.
//
// El caso que decide es `bot` → `activa`: es el único donde el nombre cambia. Con `passive` el test
// pasaría aunque el respaldo no existiera, porque el identificador coincide en los dos vocabularios.
func TestDashboardCaeAlRoleViejoMientrasDuraLaDeprecacion(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","role":"bot"}]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<option value="active" selected>activa</option>`) {
		t.Error("una plataforma que aún emite el role viejo debía pintarse como perfil «activa»")
	}
}

// TestDashboardNoPreseleccionaActivaCuandoNoSabeElPerfil es el test que atrapa un fallo que NO es de
// Go sino de HTML, y que por eso no lo ve ni el compilador ni ningún gate salvo este render.
//
// Cuando la plataforma no dice ni `profile` ni un `role` traducible, EffectiveProfile devuelve "".
// La tentación es pensar que entonces el <select> «sale sin selección»: NO. Un <select> sin ninguna
// opción `selected` enseña la PRIMERA, que aquí sería «activa». El dueño leería «esta sesión
// conversa sola» sobre una sesión de la que no sabemos nada, y un clic en «Aplicar» la activaría.
// De ahí el <option> «sin dato» selected+disabled: ante la duda no se activa nada.
func TestDashboardNoPreseleccionaActivaCuandoNoSabeElPerfil(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		// Ni profile ni role: el perfil es DESCONOCIDO.
		"GET /api/v1/sessions": {http.StatusOK,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online"}]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if strings.Contains(out, `<option value="active" selected>`) {
		t.Error("un perfil desconocido NO puede preseleccionar «activa»: activaría una sesión que nadie activó")
	}
	if strings.Contains(out, `<option value="passive" selected>`) {
		t.Error("un perfil desconocido tampoco puede afirmar «pasiva»: la plataforma no lo dijo")
	}
	if !strings.Contains(out, `<option value="" selected disabled>— sin dato —</option>`) {
		t.Error("sin dato, el desplegable tiene que decirlo en vez de dejar que el navegador elija la primera opción")
	}
}

// TestSetProfileRechazaElPerfilVacio cierra la otra mitad del caso «sin dato»: el <option> placeholder
// viaja con value="" si alguien fuerza el envío, y el handler tiene que rebotarlo como cualquier otro
// perfil inválido. Si esto se cayera, el placeholder dejaría de ser seguro.
func TestSetProfileRechazaElPerfilVacio(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postFormWithCookie(router, "/sessions/s-1/profile", url.Values{"profile": {""}}, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un perfil vacío debía responder 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Elige un perfil válido") {
		t.Error("un perfil vacío debía pedir activa o pasiva")
	}
}

// TestDashboardWithoutCookieRedirects: GET / y POST /send sin cookie → redirect a /login (AuthMiddleware de
// T2).
func TestDashboardWithoutCookieRedirects(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	rec := getWithCookie(router, "/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET / sin cookie debía redirigir a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = postFormWithCookie(router, "/send", url.Values{"session_id": {"s"}, "to": {"+1"}, "text": {"h"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("POST /send sin cookie debía redirigir a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestDashboardPintaLaSaludDelClasificador cubre la columna «Clasificador» (Plan 051 · Ola 4 · T4.3):
// el operador tiene que poder responder «¿está clasificando?» y «¿se estorban el cajero y Ollama?»
// SIN ENTRAR EN LA MÁQUINA, que es el criterio literal de la tarea.
//
// Los tres casos van juntos a propósito, porque el que importa sólo se ve por contraste: la sesión
// SIN los campos tiene que pintar «desconocido», y la que los trae tiene que pintar su valor. Un test
// que sólo mirase la fila poblada seguiría verde con la plantilla pintando "closed" por defecto.
func TestDashboardPintaLaSaludDelClasificador(t *testing.T) {
	body := `[{"session_id":"s-ok","edge_id":"e1","state":"online","role":"bot",` +
		`"intent_circuit":"closed","worker_taskset":"disjunta"},` +
		`{"session_id":"s-roto","edge_id":"e2","state":"online","role":"bot",` +
		`"intent_circuit":"open","worker_taskset":"solapada"},` +
		`{"session_id":"s-mudo","edge_id":"e3","state":"online","role":"bot"}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{"closed", "CPU disjunta", "open", "CPU solapada"} {
		if !strings.Contains(out, want) {
			t.Errorf("la columna del clasificador debía contener %q", want)
		}
	}

	// 🔴 EL CASO QUE DECIDE. La sesión s-mudo no manda ninguno de los dos campos, y eso NO significa
	// «sano»: el Edge manda su cero a propósito cuando el parte del worker-cajero lleva más de 90 s sin
	// refrescarse, o sea cuando el cajero puede estar MUERTO. La consola tiene que decir «desconocido»;
	// pintar un valor por defecto ahí publicaría la salud de un clasificador apagado.
	if !strings.Contains(out, "desconocido") || !strings.Contains(out, "CPU desconocida") {
		t.Error("una sesión sin intent_circuit/worker_taskset debe pintarse «desconocido», no un valor sano")
	}
}

// TestDashboardAvisaQueElRepartoDeCPUEsDelArranque es el gate de T4.6 por su salida (b): el veredicto
// del `taskset` NO se recalcula —el Edge lo mide una vez, al arrancar el cajero, y lo republica igual
// cada 30 s—, así que un cambio de afinidad en caliente deja la consola enseñando un reparto que ya no
// existe. La regla de rancidez no puede cazarlo, porque el parte SÍ se refresca: es un valor obsoleto,
// no rancio.
//
// Habiendo elegido declarar el límite en vez de arreglarlo, la declaración tiene que estar donde
// alguien la lea EN EL MOMENTO en que le importa —el tooltip del chip— y tiene que estar en los TRES
// valores conocidos, no sólo en el bonito: quien mira «CPU solapada» para decidir si toca un taskset es
// justo quien más necesita saber que tendrá que reiniciar el cajero para ver el resultado.
//
// Cuenta ocurrencias a propósito: comprobar «aparece la frase» seguiría verde con dos de los tres chips
// desprovistos del aviso.
func TestDashboardAvisaQueElRepartoDeCPUEsDelArranque(t *testing.T) {
	const aviso = "al arrancar el cajero, y NO se recalcula"

	body := `[{"session_id":"s-a","edge_id":"e1","state":"online","role":"bot",` +
		`"intent_circuit":"closed","worker_taskset":"disjunta"},` +
		`{"session_id":"s-b","edge_id":"e2","state":"online","role":"bot",` +
		`"intent_circuit":"closed","worker_taskset":"solapada"},` +
		`{"session_id":"s-c","edge_id":"e3","state":"online","role":"bot",` +
		`"intent_circuit":"closed","worker_taskset":"cajero_sin_confinar"}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if got := strings.Count(out, aviso); got != 3 {
		t.Errorf("los TRES chips de reparto de CPU deben avisar de que el veredicto es del arranque; "+
			"encontrado %d veces, esperado 3", got)
	}

	// El breaker sí es continuo: si el aviso se colase en su tooltip diría una mentira distinta.
	i := strings.Index(out, ">closed<")
	if i < 0 {
		t.Fatal("la fila debía pintar el chip del breaker cerrado")
	}
	if inicio := strings.LastIndex(out[:i], "<span"); inicio >= 0 && strings.Contains(out[inicio:i], aviso) {
		t.Error("el aviso del arranque es del reparto de CPU, no del breaker: el breaker sí se refresca")
	}
}
