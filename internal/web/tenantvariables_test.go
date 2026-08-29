package web

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// varsServer es una API fake CON ESTADO: guarda el conjunto que le manda el PUT y lo devuelve en el
// GET siguiente. Hace falta que tenga estado para poder probar el ROUNDTRIP —lo que se guarda es lo
// que vuelve— en vez de solo comprobar que se envió algo.
type varsServer struct {
	mu       sync.Mutex
	vars     map[string]string
	lastBody string
	puts     int
	srv      *httptest.Server
	// override, si está puesto, contesta el PUT en lugar de guardar (para probar rechazos).
	override http.HandlerFunc
}

func newVarsServer(initial map[string]string) *varsServer {
	vs := &varsServer{vars: initial}
	if vs.vars == nil {
		vs.vars = map[string]string{}
	}
	vs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/tenant-variables" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		vs.mu.Lock()
		defer vs.mu.Unlock()

		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			vs.lastBody = string(body)
			vs.puts++
			if vs.override != nil {
				vs.override(w, r)
				return
			}
			var req struct {
				Variables map[string]string `json:"variables"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"cuerpo ilegible"}`)
				return
			}
			// Reemplazo TOTAL, igual que la plataforma: lo que no viene, deja de existir.
			vs.vars = req.Variables
		}
		out, _ := json.Marshal(map[string]any{
			"variables": vs.vars, "updated_at": "2026-08-06T10:00:00Z",
		})
		_, _ = w.Write(out)
	}))
	return vs
}

func (vs *varsServer) close() { vs.srv.Close() }

func (vs *varsServer) sent() map[string]string {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	var req struct {
		Variables map[string]string `json:"variables"`
	}
	_ = json.Unmarshal([]byte(vs.lastBody), &req)
	return req.Variables
}

// varsForm arma el formulario de la pantalla: los pares viajan como dos arrays paralelos.
func varsForm(rows ...[2]string) url.Values {
	form := url.Values{}
	for _, row := range rows {
		form.Add("key", row[0])
		form.Add("value", row[1])
	}
	return form
}

// TestTenantVariablesListsSortedWithBlankRows: el conjunto se pinta ordenado (un mapa de Go no tiene
// orden y la tabla bailaría), y al final hay filas vacías para dar de alta sin JS.
func TestTenantVariablesListsSortedWithBlankRows(t *testing.T) {
	api := newVarsServer(map[string]string{"zona": "Providencia", "moneda": "CLP", "nombre": "Aurora"})
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	moneda, nombre, zona := strings.Index(out, `value="CLP"`), strings.Index(out, `value="Aurora"`), strings.Index(out, `value="Providencia"`)
	if moneda < 0 || nombre < 0 || zona < 0 {
		t.Fatal("debían pintarse las tres variables del tenant")
	}
	if moneda >= nombre || nombre >= zona {
		t.Error("las filas debían salir ordenadas por clave (moneda, nombre, zona)")
	}
	if !strings.Contains(out, "3 variables en este tenant") {
		t.Error("la pantalla debía decir cuántas variables hay")
	}
	// 3 reales + 3 vacías de alta.
	if got := strings.Count(out, `name="key"`); got != 6 {
		t.Errorf("debían pintarse 6 filas (3 llenas + 3 de alta), got %d", got)
	}
	if !strings.Contains(out, "2026-08-06T10:00:00Z") {
		t.Error("debía mostrarse la marca del último cambio")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (server-side, CSP sin unsafe-inline)")
	}
}

// TestTenantVariablesIsPermanent (requisito explícito de T2.1): esta pantalla NO lleva la marca de
// provisionalidad de las de negocio — es capa técnica y se queda en el BFF (ADR-0035).
func TestTenantVariablesIsPermanent(t *testing.T) {
	api := newVarsServer(map[string]string{"nombre": "Aurora"})
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", validSessionCookie(t)).Body.String()
	for _, forbidden := range []string{"PROVISIONAL", "migra a la consola de administración", "ADR-0047"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("la pantalla de variables no debe llevar la marca provisional (%q)", forbidden)
		}
	}
	// Y su enlace sale para cualquier sesión: no hay gate de feature en este frente.
	if !strings.Contains(out, `href="/variables"`) {
		t.Error("la barra debía ofrecer el enlace a Variables sin depender de ninguna feature")
	}
}

// TestTenantVariablesRoundtripVerbatim (criterio de aceptación): lo guardado vuelve TAL CUAL —
// acentos, espacios interiores y de los bordes, y un JSON serializado dentro de una cadena.
func TestTenantVariablesRoundtripVerbatim(t *testing.T) {
	api := newVarsServer(nil)
	defer api.close()

	casos := map[string]string{
		"dirección":   "Av. Providencia 1234, Santiago — 2.º piso",
		"horario":     "  09:00 a 18:00  ",
		"config_json": `{"envio":{"gratis_desde":50000,"nota":"sin cargo"}}`,
	}
	form := varsForm(
		[2]string{"dirección", casos["dirección"]},
		[2]string{"horario", casos["horario"]},
		[2]string{"config_json", casos["config_json"]},
	)

	router := NewRouter(authTestCfg(api.srv.URL))
	rec := postFormWithCookie(router, "/variables", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("guardar debía dar 200, got %d — body=%s", rec.Code, rec.Body.String())
	}

	// 1) Lo que viajó a la API es exactamente lo tecleado: ni recortes de espacios ni reescrituras.
	sent := api.sent()
	for k, want := range casos {
		if sent[k] != want {
			t.Errorf("a la API debía viajar %q verbatim; got %q", want, sent[k])
		}
	}
	if len(sent) != 3 {
		t.Errorf("debían viajar 3 variables, got %d (%v)", len(sent), sent)
	}

	// 2) Lo que vuelve a la pantalla es lo mismo, ya escapado como HTML pero sin alterar el dato.
	out := rec.Body.String()
	for k, want := range casos {
		if !strings.Contains(out, `value="`+template.HTMLEscapeString(want)+`"`) {
			t.Errorf("la pantalla debía devolver %q verbatim (clave %s)", want, k)
		}
	}

	// 3) Y una recarga posterior —GET limpio— sigue devolviendo lo mismo.
	reload := getWithCookie(router, "/variables", validSessionCookie(t)).Body.String()
	for _, want := range casos {
		if !strings.Contains(reload, `value="`+template.HTMLEscapeString(want)+`"`) {
			t.Errorf("tras recargar, %q debía seguir intacto", want)
		}
	}
}

// Bytes REALES del intercambio con la plataforma (wapp-cloud-platform `0063b74`), capturados en las
// dos direcciones: `realPutBody` es el cuerpo que ESTA pantalla generó, y `realPutResponse` lo que el
// handler real respondió al recibirlo (su GET posterior devolvió lo mismo). No están escritos a mano.
const (
	realPutBody = `{"variables":{"config_json":"{\"envio\":{\"gratis_desde\":50000,\"nota\":\"sin cargo\"}}",` +
		`"dirección":"Av. Providencia 1234, Santiago — 2.º piso","horario":"  09:00 a 18:00  "}}`
	realPutResponse = `{"variables":{"config_json":"{\"envio\":{\"gratis_desde\":50000,\"nota\":\"sin cargo\"}}",` +
		`"dirección":"Av. Providencia 1234, Santiago — 2.º piso","horario":"  09:00 a 18:00  "},` +
		`"updated_at":"2026-08-06T16:42:52Z"}`
)

// TestTenantVariablesAgainstRealPlatformPayload es la prueba de contrato entre los dos repos.
//
// El cuerpo que genera esta pantalla se mandó al handler REAL de la plataforma, que lo aceptó con 200
// y devolvió los valores intactos; aquí se fijan las dos mitades de ese intercambio. Si alguien
// cambia la serialización del PUT (un `omitempty` que convierta el conjunto vacío en `null`, un
// recorte de espacios) o la lectura de la respuesta, salta aquí y no en producción.
func TestTenantVariablesAgainstRealPlatformPayload(t *testing.T) {
	t.Run("el cuerpo que enviamos es el que la plataforma aceptó", func(t *testing.T) {
		api := newVarsServer(nil)
		defer api.close()

		form := varsForm(
			[2]string{"dirección", "Av. Providencia 1234, Santiago — 2.º piso"},
			[2]string{"horario", "  09:00 a 18:00  "},
			[2]string{"config_json", `{"envio":{"gratis_desde":50000,"nota":"sin cargo"}}`},
		)
		if rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", form, validSessionCookie(t)); rec.Code != http.StatusOK {
			t.Fatalf("guardar debía dar 200, got %d", rec.Code)
		}
		if api.lastBody != realPutBody {
			t.Errorf("el cuerpo del PUT cambió respecto al que la plataforma aceptó.\n got: %s\nquiero: %s",
				api.lastBody, realPutBody)
		}
	})

	t.Run("la respuesta real se pinta verbatim", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, realPutResponse)
		}))
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/variables", validSessionCookie(t)).Body.String()
		for _, want := range []string{
			"Av. Providencia 1234, Santiago — 2.º piso",
			"  09:00 a 18:00  ",
			`{"envio":{"gratis_desde":50000,"nota":"sin cargo"}}`,
		} {
			if !strings.Contains(out, `value="`+template.HTMLEscapeString(want)+`"`) {
				t.Errorf("la respuesta real debía pintarse verbatim: %q", want)
			}
		}
		if !strings.Contains(out, "2026-08-06T16:42:52Z") {
			t.Error("debía mostrarse el updated_at que manda la plataforma")
		}
	})
}

// TestTenantVariablesRemoveOmitsRow: quitar es no enviar esa fila. El PUT lleva el resto del conjunto
// completo, que es la única forma de borrar que da la API.
func TestTenantVariablesRemoveOmitsRow(t *testing.T) {
	api := newVarsServer(map[string]string{"a": "1", "b": "2", "c": "3"})
	defer api.close()

	form := varsForm([2]string{"a", "1"}, [2]string{"b", "2"}, [2]string{"c", "3"})
	form.Set("remove", "1") // la fila del medio: `b`

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("quitar debía dar 200, got %d", rec.Code)
	}
	sent := api.sent()
	if _, sigue := sent["b"]; sigue {
		t.Error("la fila quitada no debía viajar en el conjunto")
	}
	if sent["a"] != "1" || sent["c"] != "3" || len(sent) != 2 {
		t.Errorf("el resto del conjunto debía viajar entero, got %v", sent)
	}
	if !strings.Contains(rec.Body.String(), "Variable quitada") {
		t.Error("debía confirmarse el quitado")
	}
	if !strings.Contains(rec.Body.String(), "ahora 2 variables") {
		t.Error("la confirmación debía decir cuántas quedan (así se nota un borrado accidental)")
	}
}

// TestTenantVariablesRemoveByIndexNotByKey: el índice manda. Si el operador editó la clave de una
// fila y pulsa «Quitar» en ESA fila, se quita la fila que señaló, no la que dice el texto editado.
func TestTenantVariablesRemoveByIndexNotByKey(t *testing.T) {
	api := newVarsServer(map[string]string{"a": "1", "b": "2"})
	defer api.close()

	// La fila 0 se editó de `a` a `a_renombrada` y se pulsa Quitar en ella.
	form := varsForm([2]string{"a_renombrada", "1"}, [2]string{"b", "2"})
	form.Set("remove", "0")

	postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", form, validSessionCookie(t))
	sent := api.sent()
	if len(sent) != 1 || sent["b"] != "2" {
		t.Errorf("debía quedar solo `b`, got %v", sent)
	}
}

// TestTenantVariablesEmptySetIsExplicit: dejar el tenant sin variables es una intención legítima y
// viaja como `{}`. Un `null` sería otra cosa —la API lo rechaza con 400— y no puede colarse.
func TestTenantVariablesEmptySetIsExplicit(t *testing.T) {
	api := newVarsServer(map[string]string{"solo": "una"})
	defer api.close()

	form := varsForm([2]string{"solo", "una"})
	form.Set("remove", "0")

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("vaciar el conjunto debía dar 200, got %d", rec.Code)
	}
	if !strings.Contains(api.lastBody, `"variables":{}`) {
		t.Errorf("el conjunto vacío debía viajar como {}, got %s", api.lastBody)
	}
	if !strings.Contains(rec.Body.String(), "se queda sin ninguna variable") {
		t.Error("la pantalla debía decir que el tenant se quedó sin variables")
	}
}

// TestTenantVariablesFormValidation: los dos únicos errores de FORMA que se detectan aquí (valor sin
// clave, clave repetida) no llegan a la API y no tiran lo que el operador escribió.
func TestTenantVariablesFormValidation(t *testing.T) {
	casos := map[string]struct {
		form url.Values
		want string
	}{
		"valor sin clave": {
			form: varsForm([2]string{"", "huérfano"}),
			want: "Hay un valor sin clave",
		},
		"clave repetida": {
			form: varsForm([2]string{"moneda", "CLP"}, [2]string{"moneda", "USD"}),
			want: "está repetida",
		},
	}
	for name, tc := range casos {
		t.Run(name, func(t *testing.T) {
			api := newVarsServer(nil)
			defer api.close()

			rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", tc.form, validSessionCookie(t))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("debía rechazarse con 400, got %d", rec.Code)
			}
			if api.puts != 0 {
				t.Error("un error de forma no debía llegar a la API")
			}
			out := rec.Body.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("el aviso debía explicar el problema (%q)", tc.want)
			}
			// Lo tecleado se re-pinta: el rechazo no puede costarle al operador su trabajo.
			for _, v := range tc.form["value"] {
				if v != "" && !strings.Contains(out, `value="`+template.HTMLEscapeString(v)+`"`) {
					t.Errorf("debía conservarse lo tecleado (%q)", v)
				}
			}
		})
	}
}

// TestTenantVariablesPlatformRejections: el 400 de la plataforma llega con su motivo (es lo único que
// dice qué corregir) y el 413 con un consejo propio.
func TestTenantVariablesPlatformRejections(t *testing.T) {
	t.Run("400 con motivo", func(t *testing.T) {
		api := newVarsServer(nil)
		api.override = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"hay una clave demasiado larga"}`)
		}
		defer api.close()

		rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables",
			varsForm([2]string{"k", "v"}), validSessionCookie(t))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("debía propagarse como 400, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "hay una clave demasiado larga") {
			t.Error("el motivo de la plataforma debía llegar al operador")
		}
	})

	t.Run("413 por tamaño", func(t *testing.T) {
		api := newVarsServer(nil)
		api.override = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = io.WriteString(w, `{"error":"el cuerpo excede el tamaño máximo"}`)
		}
		defer api.close()

		rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables",
			varsForm([2]string{"k", strings.Repeat("x", 100)}), validSessionCookie(t))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("debía propagarse como 413, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "demasiado grande") {
			t.Error("el 413 debía traducirse a un consejo accionable")
		}
	})

	t.Run("403 sin permiso", func(t *testing.T) {
		api := newVarsServer(nil)
		api.override = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"forbidden"}`)
		}
		defer api.close()

		rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables",
			varsForm([2]string{"k", "v"}), validSessionCookie(t))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("debía propagarse como 403, got %d", rec.Code)
		}
		out := rec.Body.String()
		if !strings.Contains(out, "no puede modificar las variables") {
			t.Error("el 403 debía traducirse a un aviso de permisos")
		}
		if strings.Contains(out, "forbidden") {
			t.Error("no debe filtrarse el cuerpo del upstream")
		}
	})
}

// TestTenantVariablesEmptyTenantIsNotAnError: un tenant sin variables es una respuesta normal (200 y
// mapa vacío), no un fallo: la pantalla invita a escribir la primera.
func TestTenantVariablesEmptyTenantIsNotAnError(t *testing.T) {
	api := newVarsServer(nil)
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un tenant sin variables debía dar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "todavía no tiene variables") {
		t.Error("debía invitarse a crear la primera variable")
	}
	if !strings.Contains(out, `name="key"`) {
		t.Error("el formulario de alta debía estar disponible igualmente")
	}
}

// TestTenantVariablesReadDegrades: si la lectura falla, la pantalla lo dice y NO ofrece un formulario
// vacío — guardar desde ahí borraría todo el conjunto del tenant.
func TestTenantVariablesReadDegrades(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"detalle interno que no debe verse"}`)
	}))
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/variables", validSessionCookie(t))
	out := rec.Body.String()
	if !strings.Contains(out, "No se pudieron cargar las variables") {
		t.Error("el fallo de lectura debía avisarse")
	}
	if strings.Contains(out, `name="key"`) {
		t.Error("sin conjunto cargado no puede ofrecerse el formulario: guardarlo vaciaría el tenant")
	}
	if strings.Contains(out, "detalle interno que no debe verse") {
		t.Error("no debe filtrarse el detalle del upstream")
	}
}

// TestTenantVariablesRequireSession: la pantalla vive en el grupo protegido.
func TestTenantVariablesRequireSession(t *testing.T) {
	rec := getWithCookie(NewRouter(authTestCfg("http://api.invalid")), "/variables", nil)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Errorf("sin sesión debía redirigir al login, got %d", rec.Code)
	}
}

// TestTenantVariablesNoInterpretation (D-041.1): la pantalla no juzga el contenido. Una clave que
// «parece» de otra cosa y un valor que no «parece» del tipo esperado se guardan igual.
func TestTenantVariablesNoInterpretation(t *testing.T) {
	api := newVarsServer(nil)
	defer api.close()

	form := varsForm(
		[2]string{"moneda", "no es una moneda"},
		[2]string{"UPPER_case-Mixto.123", "42"},
		[2]string{"clave con espacios", ""},
	)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/variables", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("wApp no interpreta claves ni valores: debía guardar, got %d", rec.Code)
	}
	sent := api.sent()
	if sent["moneda"] != "no es una moneda" {
		t.Error("no debe validarse que `moneda` sea una moneda")
	}
	if sent["UPPER_case-Mixto.123"] != "42" {
		t.Error("la clave no debe normalizarse")
	}
	if v, ok := sent["clave con espacios"]; !ok || v != "" {
		t.Error("un valor vacío con clave es legítimo y debe viajar")
	}
}
