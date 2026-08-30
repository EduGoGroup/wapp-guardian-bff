package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"
)

// sessionsMovedMarker es el ancla del bloque que dice dónde se administran ahora las sesiones. Si la
// portada dejara de emitirlo, esta cadena no aparecería en el HTML.
const sessionsMovedMarker = `id="section-sessions-moved"`

// editorMovedMarker es el mismo ancla para flujos y disparadores (Plan 047 · T6.5/T6.6).
const editorMovedMarker = `id="section-editor-moved"`

// homeAPI levanta una API pública fake que sirve las features dadas y NADA MÁS: cualquier otra ruta
// responde 500 y deja constancia de que se llamó.
//
// Que las rutas del dashboard NO estén mapeadas es el punto: la portada que las llamara se encontraría
// un 500, y el test que cuenta las llamadas lo diría.
func homeAPI(t *testing.T, features ...string) (*httptest.Server, func(path string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("commerce", features...))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
}

// TestPortadaNoPintaLaTablaDeSesionesNiElFormularioDeEnvio es la mitad NEGATIVA de la retirada (Plan
// 047 · T2.1): la portada ya no lista teléfonos, ya no ofrece el desplegable de perfil y ya no manda
// mensajes.
//
// Va por marcadores del HTML y no por «no aparece la palabra sesión», que sería incumplible: la
// portada SIGUE hablando de sesiones —dice dónde se administran ahora—. Lo que no puede tener es la
// maquinaria: el <form> a /send, el <form> a /sessions/{id}/profile, el <select> de perfil y la tabla.
func TestPortadaNoPintaLaTablaDeSesionesNiElFormularioDeEnvio(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic", "catalog_import", "crm_bridge", "llm_intent")

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la portada debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, prohibido := range []string{
		`action="/send"`,          // el formulario de envío
		`/profile"`,               // el formulario de perfil de cada fila
		`name="session_id"`,       // el selector de sesión de salida
		"Sesiones vinculadas",     // el título de la tabla
		"Enviar un mensaje",       // el título del formulario
		"<table",                  // la tabla de teléfonos
		`<option value="active"`,  // el desplegable de perfil
		`<option value="passive"`, //
		"CPU disjunta",            // la columna de salud del clasificador
	} {
		if strings.Contains(out, prohibido) {
			t.Errorf("la portada retirada no debía contener %q", prohibido)
		}
	}
}

// TestPortadaNoLlamaALasRutasDeSesiones cierra la otra mitad de la retirada, la que el HTML no puede
// enseñar: la portada tampoco PREGUNTA por las sesiones.
//
// Sin esto, una portada que siguiera llamando a `GET /api/v1/sessions` y tirara el resultado pasaría
// los asertos del HTML sin que nadie lo notase, gastando un viaje a la plataforma en cada visita.
func TestPortadaNoLlamaALasRutasDeSesiones(t *testing.T) {
	api, hits := homeAPI(t, "cart_basic")

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la portada debía renderizar 200, got %d", rec.Code)
	}
	if got := hits("/api/v1/sessions"); got != 0 {
		t.Errorf("la portada no debía preguntar por las sesiones, got %d llamadas", got)
	}
	if got := hits("/api/v1/entitlements"); got != 1 {
		t.Errorf("la portada debía leer las capacidades del tenant una vez, got %d", got)
	}
}

// TestPortadaAvisaDondeSeAdministranLasSesiones es la mitad POSITIVA: retirar una pantalla sin decir a
// dónde se fue deja al operador buscándola. El aviso es el sustituto de la tabla, no un adorno.
func TestPortadaAvisaDondeSeAdministranLasSesiones(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic")

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la portada debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, sessionsMovedMarker) {
		t.Error("la portada debía emitir el bloque que dice dónde se administran ahora las sesiones")
	}
	if !strings.Contains(out, "se\n                administran ahora en la consola del cliente") &&
		!strings.Contains(out, "administran ahora en la consola del cliente") {
		t.Error("el aviso debía nombrar la consola del cliente como el sitio donde se administran")
	}
}

// TestPortadaNoInventaElEnlaceALaConsolaDelCliente fija la decisión de no cablear infraestructura: sin
// una URL configurada, el aviso NO ofrece enlace.
//
// Un `localhost:8107` escrito en la plantilla sería un enlace roto para todo el que no esté sentado en
// la máquina del despliegue, y un enlace roto en una consola de operación cuesta más que no tenerlo:
// quien lo pulsa concluye que la pantalla nueva no existe.
func TestPortadaNoInventaElEnlaceALaConsolaDelCliente(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic")

	cfg := authTestCfg(api.URL)
	if cfg.ClientConsoleURL != "" {
		t.Fatalf("el default de ClientConsoleURL debía ser vacío, got %q", cfg.ClientConsoleURL)
	}
	out := getWithCookie(NewRouter(cfg), "/", validSessionCookie(t)).Body.String()

	if strings.Contains(out, "8107") || strings.Contains(out, "localhost:") || strings.Contains(out, "127.0.0.1") {
		t.Error("sin URL configurada la portada no puede inventarse la dirección de la consola del cliente")
	}
	if strings.Contains(out, "Abrir la consola del cliente") {
		t.Error("sin URL configurada no debe ofrecerse un enlace que no lleva a ninguna parte")
	}

	// Con la URL puesta por entorno, el enlace sí sale: el vacío es un default, no una prohibición.
	cfg2 := authTestCfg(api.URL)
	cfg2.ClientConsoleURL = "https://consola.ejemplo/"
	out2 := getWithCookie(NewRouter(cfg2), "/", validSessionCookie(t)).Body.String()
	if !strings.Contains(out2, `href="https://consola.ejemplo/"`) {
		t.Error("con WAPP_GUARDIAN_CLIENT_CONSOLE_URL puesta, la portada debía ofrecer el enlace")
	}
}

// TestPortadaConservaLosAccesosDeLoQueSigueVivoAqui: la retirada se lleva las sesiones y NADA MÁS. La
// portada es el índice de esta consola, así que tiene que seguir ofreciendo lo que no se mudó, con los
// gates que cada cosa ya tenía.
func TestPortadaConservaLosAccesosDeLoQueSigueVivoAqui(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic", "catalog_import", "crm_bridge", "llm_intent")

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t)).Body.String()

	for _, want := range []string{
		"Plan y capacidades", // la tarjeta de plan
		`chip chip--info">plan · commerce`,
		`href="/intakes"`,        // gateado por cart_basic
		`href="/catalog-import"`, // gateado por catalog_import
		`href="/variables"`,      // sin gate
		`href="/integrations"`,   // gateado por crm_bridge
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la portada debía conservar el acceso %q", want)
		}
	}
}

// TestPortadaRespetaLosGatesDeLoQueConserva es el contraste del test anterior: los accesos gateados no
// se emiten sin su feature. Sin este, «conserva los accesos» pasaría con una portada que los pinta
// SIEMPRE, que es justo el fallo que el gate server-side existe para evitar.
func TestPortadaRespetaLosGatesDeLoQueConserva(t *testing.T) {
	api, _ := homeAPI(t, "menu") // ni cart_basic, ni catalog_import, ni crm_bridge, ni llm_intent

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t)).Body.String()

	for _, prohibido := range []string{
		`href="/intakes"`, "Abrir la bandeja",
		`href="/catalog-import"`, "Abrir el import de catálogo",
		`href="/integrations"`, "Abrir las integraciones",
		`id="section-llm-intent"`,
	} {
		if strings.Contains(out, prohibido) {
			t.Errorf("sin su feature, la portada no debía emitir %q", prohibido)
		}
	}
	// Lo que no depende de features sigue estando: el gate no se lleva por delante lo que no gatea.
	for _, want := range []string{`href="/variables"`, sessionsMovedMarker, editorMovedMarker} {
		if !strings.Contains(out, want) {
			t.Errorf("el gate cerrado no debía llevarse por delante %q", want)
		}
	}
}

// TestRutasDelDashboardYaNoExisten es el gate de la retirada por el lado del router.
//
// 🔴 EL CÓDIGO DE ESTADO NO BASTA, Y ESTÁ MEDIDO. El criterio natural sería «404 (no existe) y no 405
// (existe con otro verbo)», pero este router NUNCA emite 405: gin nace con HandleMethodNotAllowed en
// false y responde 404 a un verbo no registrado exactamente igual que a una ruta inexistente. Se
// comprobó mutando —registrando `GET /send` y pidiendo `POST /send`— y el 404 seguía llegando: un test
// que solo mirase el status daría por retirada una ruta que sigue viva con otro método.
//
// Por eso hay DOS asertos y no uno: el status responde «el navegador se encuentra un 404», y la tabla
// de rutas de gin responde «no queda nada registrado bajo ese path», que es lo que de verdad significa
// retirar. El segundo es el que mata la mutación.
func TestRutasDelDashboardYaNoExisten(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic")
	router := NewRouter(authTestCfg(api.URL))

	casos := []struct {
		nombre string
		metodo string
		ruta   string
	}{
		{"envío de mensaje", http.MethodPost, "/send"},
		{"perfil de sesión", http.MethodPost, "/sessions/s-1/profile"},
		{"listado de sesiones", http.MethodGet, "/sessions"},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.metodo == http.MethodPost {
				rec = postFormWithCookie(router, tc.ruta, url.Values{}, validSessionCookie(t))
			} else {
				rec = getWithCookie(router, tc.ruta, validSessionCookie(t))
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s debía responder 404 (retirada), got %d", tc.metodo, tc.ruta, rec.Code)
			}
		})
	}

	// Y el aserto que el status no puede dar: ninguna de las rutas retiradas queda registrada, con
	// NINGÚN verbo. Se compara por el patrón declarado (`/sessions/:id/profile`), no por la URL
	// concreta, porque eso es lo que guarda la tabla.
	retiradas := map[string]bool{
		"/send":                 true,
		"/sessions":             true,
		"/sessions/:id/profile": true,
		"/sessions/:id":         true,
	}
	for _, r := range NewRouter(authTestCfg(api.URL)).Routes() {
		if retiradas[r.Path] {
			t.Errorf("la ruta %s %s sigue registrada: la retirada quedó a medias", r.Method, r.Path)
		}
	}
}

// TestLoginSigueAterrizandoEnLaPortada es el fallo que ESTA tarea podía causar y que ningún test cubría:
// `GET /` es el destino de tres redirecciones del plano de autenticación, así que borrarla habría
// convertido un login correcto en un 404.
//
// El test recorre la cadena entera —POST /login → 303 a / → GET / → 200— en vez de comprobar solo el
// Location: un aserto sobre la redirección seguiría verde con la raíz muerta, porque el 303 lo escribe
// DoLogin sin preguntarle al router si el destino existe.
func TestLoginSigueAterrizandoEnLaPortada(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	access := makeToken(t, exp)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = io.WriteString(w, loginBody(access, "r-ok", exp))
		case "/api/v1/entitlements":
			_, _ = io.WriteString(w, entitlementsBody("commerce", "cart_basic"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))

	rec := postFormWithCookie(router, "/login",
		url.Values{"email": {"duena@ejemplo.com"}, "password": {"la-que-sea"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("un login correcto debía redirigir 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("el login debía aterrizar en la portada, got %q", loc)
	}

	// Y el destino tiene que existir: se sigue la redirección con la cookie que el login emitió.
	raw := sessionSetCookie(rec)
	if raw == "" {
		t.Fatal("el login debía emitir la cookie de sesión")
	}
	sess, err := sharedweb.DecodeSession(cookieValueFromSetCookie(t, raw))
	if err != nil || sess.AccessToken == "" {
		t.Fatalf("la cookie emitida debía custodiar el token, got %+v (err %v)", sess, err)
	}
	destino := getWithCookie(router, "/",
		&http.Cookie{Name: sessionCookieName, Value: cookieValueFromSetCookie(t, raw)})
	if destino.Code != http.StatusOK {
		t.Fatalf("el destino del login (GET /) debía responder 200, got %d", destino.Code)
	}
}

// cookieValueFromSetCookie extrae el valor de la cookie de sesión de una cabecera Set-Cookie.
func cookieValueFromSetCookie(t *testing.T, raw string) string {
	t.Helper()
	header := http.Header{}
	header.Add("Set-Cookie", raw)
	for _, c := range (&http.Response{Header: header}).Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatalf("no se encontró la cookie %q en %q", sessionCookieName, raw)
	return ""
}

// TestPortadaEchaAlLoginAnte401Persistente conserva la garantía que la retirada se habría llevado por
// delante sin que nadie lo notara.
//
// Hasta ahora, quien echaba al login ante un 401 persistente era la llamada de negocio de la página: el
// listado de sesiones. Al retirarse, la portada se quedó con una sola llamada —las capacidades—, y ésa
// tragaba el 401 a propósito («es accesoria»). Sin esta línea, un token vigente en el reloj pero
// repudiado por la plataforma habría pintado una portada degradada, con el operador navegando una
// consola muerta hasta que el token venciera.
func TestPortadaEchaAlLoginAnte401Persistente(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// También el refresh: el 401 tiene que ser PERSISTENTE para que la expulsión sea la correcta.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"token_revoked"}`)
	}))
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("un 401 persistente debía echar al login (303), got %d %q",
			rec.Code, rec.Header().Get("Location"))
	}
	if raw := sessionSetCookie(rec); raw == "" || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("al echar al login debía limpiarse la cookie de sesión, got %q", raw)
	}
}

// TestPortadaSinCookieRedirigeAlLogin es lo que queda de TestDashboardWithoutCookieRedirects: la mitad
// de `/` sobrevive a la retirada, la de `POST /send` se fue con su ruta (ahora vive en
// TestRutasDelDashboardYaNoExisten, que exige 404 y no un redirect).
func TestPortadaSinCookieRedirigeAlLogin(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	rec := getWithCookie(router, "/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET / sin cookie debía redirigir a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestRutasDelEditorYaNoExisten es el gate de la retirada del editor (Plan 047 · T6.6) por el lado del
// router: flujos y disparadores viven ahora en `wapp-client-console` y aquí no queda ni una de las seis.
//
// 🔴 EL CÓDIGO DE ESTADO NO BASTA, Y ESTÁ MEDIDO — es la misma lección que dejó T2.1 y está escrita en
// TestRutasDelDashboardYaNoExisten: este router nace con HandleMethodNotAllowed en false y responde
// 404 a un verbo no registrado exactamente igual que a una ruta inexistente. Un test que solo mirase
// el status daría por retirada una ruta que sigue viva con otro método. Por eso el aserto que mata la
// mutación —resucitar `GET /flows`— es el que recorre `router.Routes()`, no el que pide la URL.
//
// Se compara por el PATRÓN declarado (`/flows/:id`, `/triggers/:id/delete`), no por la URL concreta,
// porque eso es lo que guarda la tabla de gin.
func TestRutasDelEditorYaNoExisten(t *testing.T) {
	api, _ := homeAPI(t, "cart_basic")
	router := NewRouter(authTestCfg(api.URL))

	casos := []struct {
		nombre string
		metodo string
		ruta   string
	}{
		{"listado de flujos", http.MethodGet, "/flows"},
		{"detalle de flujo", http.MethodGet, "/flows/f-1"},
		{"publicar flujo", http.MethodPost, "/flows"},
		{"listado de disparadores", http.MethodGet, "/triggers"},
		{"crear disparador", http.MethodPost, "/triggers"},
		{"borrar disparador", http.MethodPost, "/triggers/t-1/delete"},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if tc.metodo == http.MethodPost {
				rec = postFormWithCookie(router, tc.ruta, url.Values{}, validSessionCookie(t))
			} else {
				rec = getWithCookie(router, tc.ruta, validSessionCookie(t))
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s debía responder 404 (retirada), got %d", tc.metodo, tc.ruta, rec.Code)
			}
		})
	}

	// EL ASERTO DEL CRITERIO: ninguno de los seis patrones queda registrado, con NINGÚN verbo.
	retiradas := map[string]bool{
		"/flows":               true,
		"/flows/:id":           true,
		"/triggers":            true,
		"/triggers/:id/delete": true,
	}
	// Falla si el bucle se queda sin material: un criterio de ausencia lo satisface una tabla vacía, y
	// este proyecto ya se comió un verde que medía cero. Si `Routes()` no devolviera nada, el aserto de
	// arriba pasaría sin haber mirado una sola ruta.
	rutas := NewRouter(authTestCfg(api.URL)).Routes()
	if len(rutas) == 0 {
		t.Fatal("router.Routes() vacío: el test no está midiendo nada")
	}
	for _, r := range rutas {
		if retiradas[r.Path] {
			t.Errorf("la ruta %s %s sigue registrada: la retirada quedó a medias", r.Method, r.Path)
		}
	}

	// Y el otro lado de la misma retirada (T6.5): ninguna página emite un enlace a una ruta que esta
	// casa ya no sirve. Se mira el HTML RENDERIZADO del layout, no la plantilla — un `{{ if }}` mal
	// puesto deja el literal en el fichero y fuera del HTML, y al revés.
	out := getWithCookie(router, "/", validSessionCookie(t)).Body.String()
	for _, prohibido := range []string{`href="/flows"`, `href="/triggers"`, `action="/flows"`, `action="/triggers"`} {
		if strings.Contains(out, prohibido) {
			t.Errorf("la portada seguía ofreciendo %q, que esta consola ya no sirve", prohibido)
		}
	}
	// Y el aviso que dice a dónde se fueron: retirar sin decirlo deja al operador buscando.
	if !strings.Contains(out, editorMovedMarker) {
		t.Error("la portada debía emitir el bloque que dice dónde se administran ahora flujos y disparadores")
	}
}
