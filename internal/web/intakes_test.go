package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// intakesGateMarker es el ancla del bloque gateado por `cart_basic` en el dashboard, e
// intakesListMarker la del listado. Si el gate cierra, estas cadenas no aparecen en el HTML: eso es
// lo que distingue un gate server-side de un `display:none`.
const (
	intakesGateMarker = `id="section-intakes"`
	intakesListMarker = `id="section-intakes-list"`
)

// intakesAPI levanta una API pública fake que sirve SIEMPRE las features dadas (plan "commerce") y
// delega el resto de rutas en `handle`, para que cada test declare solo lo que le importa. Una ruta
// que el test no atiende responde 500, igual que routedAPI.
func intakesAPI(features []string, handle http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("commerce", features...))
			return
		}
		if handle != nil {
			handle(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
}

const intakeListBody = `{"intakes":[
 {"id":"in-1","contact_id":"ct-op4","session_id":"s-1","status":"pending_approval","total":42.5,
  "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z"},
 {"id":"in-2","contact_id":"ct-zz9","session_id":"s-2","status":"closed","total":7,
  "created_at":"2026-08-04T09:00:00Z","updated_at":"2026-08-04T09:30:00Z"}],
 "page":1,"page_size":50,"total":120}`

// TestIntakesListRendersTable (T1.5): el listado pinta una fila por solicitud con su estado ya
// traducido, y el paginador cuenta sobre el TOTAL que devuelve la API.
func TestIntakesListRendersTable(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes" {
			_, _ = io.WriteString(w, intakeListBody)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el listado debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{intakesListMarker, "in-1", "in-2", "ct-op4", "s-2", "42.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("el listado debía contener %q", want)
		}
	}
	// El estado se pinta con su nombre de negocio, no con la clave cruda.
	if !strings.Contains(out, "por aprobar") {
		t.Error("pending_approval debía pintarse como «por aprobar»")
	}
	// La solicitud legada en `closed` no debe verse como un estado desconocido.
	if !strings.Contains(out, "confirmado") {
		t.Error("el `closed` legado debía traducirse a «confirmado»")
	}
	// Paginador: 120 resultados de 50 en 50 son 3 páginas, y la 1 tiene siguiente pero no anterior.
	if !strings.Contains(out, "página 1 de 3") {
		t.Error("el paginador debía contar 3 páginas para 120 resultados de 50")
	}
	if !strings.Contains(out, `href="/intakes?page=2"`) {
		t.Error("debía ofrecerse el enlace a la página siguiente")
	}
	if strings.Contains(out, ">Anterior<") {
		t.Error("la primera página no debe ofrecer «Anterior»")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (ADR-0035: server-side, cero framework)")
	}
	if !strings.Contains(out, "PROVISIONAL — migra a KMP (Plan 045/047, ADR-0035)") {
		t.Error("la marca PROVISIONAL debía estar en el listado")
	}
}

// TestIntakesListSendsFilters (T1.5): los filtros del formulario viajan a la API tal cual y vuelven
// al formulario, para que el operador vea con qué criterios está mirando la bandeja.
func TestIntakesListSendsFilters(t *testing.T) {
	var seen url.Values
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes" {
			seen = r.URL.Query()
			_, _ = io.WriteString(w, `{"intakes":[],"page":2,"page_size":50,"total":0}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)),
		"/intakes?from=2026-08-01&to=2026-08-06&status=confirmed&session=s-7&page=2", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el listado filtrado debía renderizar 200, got %d", rec.Code)
	}

	for key, want := range map[string]string{
		"from": "2026-08-01", "to": "2026-08-06", "status": "confirmed", "session": "s-7", "page": "2",
	} {
		if seen.Get(key) != want {
			t.Errorf("la API debía recibir %s=%q, got %q", key, want, seen.Get(key))
		}
	}
	if seen.Get("page_size") != "50" {
		t.Errorf("el tamaño de página debía viajar explícito (50), got %q", seen.Get("page_size"))
	}
	out := rec.Body.String()
	if !strings.Contains(out, `value="2026-08-01"`) || !strings.Contains(out, `value="s-7"`) {
		t.Error("los filtros aplicados debían volver al formulario")
	}
	if !strings.Contains(out, "No hay solicitudes que casen con estos filtros") {
		t.Error("una página vacía debía decirlo en vez de pintar una tabla sin filas")
	}
}

// TestIntakesListRejectedFilter: un 400 de la API (p. ej. `status` que no existe) llega al operador
// con el motivo, no con un error genérico.
func TestIntakesListRejectedFilter(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"status desconocido"}`)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes?status=inventado", validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un filtro rechazado debía dar 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "status desconocido") {
		t.Error("el motivo del rechazo debía llegar al operador")
	}
}

// TestIntakesGateOmitsBlocksWithoutFeature (criterio de aceptación): sin `cart_basic`, ni el
// dashboard ni la propia pantalla emiten el bloque de solicitudes. Se verifica sobre el HTML
// RENDERIZADO, que es donde el gate importa.
func TestIntakesGateOmitsBlocksWithoutFeature(t *testing.T) {
	api := intakesAPI([]string{"menu", "llm_intent"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		// La bandeja NO debe consultarse siquiera cuando la feature no está.
		t.Errorf("sin cart_basic no debía llamarse a %s", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	cookie := validSessionCookie(t)

	dash := getWithCookie(router, "/", cookie).Body.String()
	if strings.Contains(dash, intakesGateMarker) {
		t.Error("sin cart_basic, el dashboard NO debía emitir el bloque de solicitudes")
	}
	if strings.Contains(dash, "Abrir la bandeja") || strings.Contains(dash, `href="/intakes"`) {
		t.Error("sin la feature no debe quedar rastro de la bandeja en el HTML del dashboard")
	}
	// El gate no se lleva por delante lo que no depende de la feature.
	if !strings.Contains(dash, "Enviar un mensaje") {
		t.Error("las secciones base del dashboard debían seguir emitiéndose")
	}

	list := getWithCookie(router, "/intakes", cookie).Body.String()
	if strings.Contains(list, intakesListMarker) || strings.Contains(list, "<table") {
		t.Error("sin cart_basic, la pantalla de solicitudes no debía emitir el listado")
	}
	if !strings.Contains(list, "no incluye la bandeja de solicitudes") {
		t.Error("la pantalla debía explicar por qué no hay nada, en vez de quedarse muda")
	}
}

// TestIntakesGateEmitsBlocksWithFeature: con `cart_basic`, el bloque del dashboard y el enlace de la
// barra sí llegan al HTML.
func TestIntakesGateEmitsBlocksWithFeature(t *testing.T) {
	api := intakesAPI([]string{"cart_basic", "menu"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, intakesGateMarker) {
		t.Error("con cart_basic, el dashboard debía emitir el bloque de solicitudes")
	}
	if !strings.Contains(out, `<a href="/intakes" class="btn btn--text`) {
		t.Error("con la feature, la barra superior debía ofrecer el enlace a la bandeja")
	}
}

// TestIntakesNavHiddenWhenEntitlementsUnknown (fail-closed): si las features no se pudieron resolver,
// ninguna página emite el enlace a la bandeja. Vale también para las páginas que ni siquiera las
// consultan (flujos), donde la clave no existe en los datos de plantilla.
func TestIntakesNavHiddenWhenEntitlementsUnknown(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/entitlements":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/flows":
			_, _ = io.WriteString(w, `[]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	cookie := validSessionCookie(t)
	for _, path := range []string{"/", "/flows"} {
		if strings.Contains(getWithCookie(router, path, cookie).Body.String(), `href="/intakes"`) {
			t.Errorf("%s no debía emitir el enlace a la bandeja sin features resueltas", path)
		}
	}
}

const intakeDetailBody = `{"id":"in-1","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
 "total":42.5,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
 "items":[{"sku":"TORTA-CHOCO","label":"Torta de chocolate","qty":2,"unit_price":18.25},
          {"sku":"VELA","label":"Velas","qty":1,"unit_price":6}],
 "allowed_transitions":["cancelled","deposit_requested","pending_approval","settled"]}`

// TestIntakeDetailRendersItemsAndAllowedTransitions (T1.5, el corazón de la tarea): el detalle pinta
// las líneas y el desplegable ofrece EXACTAMENTE los destinos que publica la plataforma — ni uno más.
func TestIntakeDetailRendersItemsAndAllowedTransitions(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes/in-1" {
			_, _ = io.WriteString(w, intakeDetailBody)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el detalle debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{"TORTA-CHOCO", "Torta de chocolate", "18.25", "VELA", "ct-op4"} {
		if !strings.Contains(out, want) {
			t.Errorf("el detalle debía contener %q", want)
		}
	}
	for _, want := range []string{"cancelled", "deposit_requested", "pending_approval", "settled"} {
		if !strings.Contains(out, `<option value="`+want+`">`) {
			t.Errorf("el desplegable debía ofrecer el destino %q", want)
		}
	}
	// Lo que la plataforma NO autoriza no puede aparecer: `open` y `needs_info` no están en allowed.
	for _, forbidden := range []string{"open", "needs_info", "abandoned", "expired"} {
		if strings.Contains(out, `<option value="`+forbidden+`">`) {
			t.Errorf("el desplegable NO debía ofrecer %q: la plataforma no lo autoriza", forbidden)
		}
	}
	if strings.Count(out, "<option") != 4 {
		t.Errorf("el desplegable debía tener exactamente 4 opciones, got %d", strings.Count(out, "<option"))
	}
	if !strings.Contains(out, "PROVISIONAL — migra a KMP (Plan 045/047, ADR-0035)") {
		t.Error("la marca PROVISIONAL debía estar en el detalle")
	}
}

// TestIntakeDetailTerminalHasNoSelect: una lista VACÍA de destinos significa estado final, y así se
// dice — sin desplegable.
func TestIntakeDetailTerminalHasNoSelect(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"in-9","contact_id":"c","session_id":"s","status":"settled",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z",
		 "items":[],"allowed_transitions":[]}`)
	})
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-9", validSessionCookie(t)).Body.String()
	if strings.Contains(out, "<select") {
		t.Error("un estado final no debe ofrecer desplegable de transición")
	}
	if !strings.Contains(out, "estado final") {
		t.Error("el detalle debía explicar que la solicitud ya no admite cambios")
	}
}

// TestIntakeDetailWithoutAllowedTransitionsDeclaresGap: si la plataforma NO publica el campo, la
// pantalla lo DICE en vez de inventarse los destinos o fingir un estado final. Es la diferencia entre
// "no admite cambios" y "no lo sé", y confundirlas le costaría al operador un pedido.
func TestIntakeDetailWithoutAllowedTransitionsDeclaresGap(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z","items":[]}`)
	})
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1", validSessionCookie(t)).Body.String()
	if strings.Contains(out, "<select") {
		t.Error("sin destinos publicados no debe pintarse un desplegable")
	}
	if strings.Contains(out, "estado final") {
		t.Error("«no se sabe» no puede presentarse como «estado final»")
	}
	if !strings.Contains(out, "allowed_transitions") {
		t.Error("la pantalla debía nombrar lo que le falta a la API")
	}
}

// TestIntakeDetailNotFound: una solicitud de otro tenant (404 opaco de la plataforma) se traduce sin
// revelar si el id existe.
func TestIntakeDetailNotFound(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"solicitud no encontrada"}`)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/ajena", validSessionCookie(t))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("una solicitud ajena debía dar 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no es tuya o no existe") {
		t.Error("el 404 debía traducirse a un aviso legible")
	}
}

// TestSetIntakeStatusSuccess (criterio de aceptación): el dueño cambia el estado y la pantalla
// vuelve a leer la solicitud, de modo que lo que ve ya es el estado NUEVO.
func TestSetIntakeStatusSuccess(t *testing.T) {
	status := "confirmed"
	var posted string
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/intakes/in-1/status":
			body, _ := io.ReadAll(r.Body)
			posted = string(body)
			status = "deposit_requested"
			_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"deposit_requested",
			 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T12:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes/in-1":
			_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"`+status+`",
			 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T12:00:00Z",
			 "items":[],"allowed_transitions":["cancelled","deposit_paid"]}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer api.Close()

	form := url.Values{"status": {"deposit_requested"}}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1/status", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("una transición válida debía dar 200, got %d", rec.Code)
	}
	if !strings.Contains(posted, `"status":"deposit_requested"`) {
		t.Errorf("el cuerpo enviado a la API debía llevar el estado pedido, got %q", posted)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Solicitud movida a «seña solicitada»") {
		t.Error("debía confirmarse el cambio con el nombre de negocio del estado")
	}
	// La ficha re-leída ya muestra el estado nuevo y su desplegable actualizado.
	if !strings.Contains(out, "estado · seña solicitada") {
		t.Error("el detalle debía reflejar el estado nuevo tras el cambio")
	}
	if !strings.Contains(out, `<option value="deposit_paid">`) {
		t.Error("los destinos ofrecidos debían recalcularse con la ficha nueva")
	}
}

// TestSetIntakeStatusInvalidTransition: el 422 explica dónde está la solicitud y adónde puede ir, y
// esos destinos —que son los ÚNICOS que la plataforma publica hoy— alimentan el desplegable.
func TestSetIntakeStatusInvalidTransition(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error":"invalid_transition","status":"confirmed",
			 "requested":"open","allowed":["cancelled","deposit_requested","pending_approval","settled"]}`)
		default:
			// La ficha NO publica los destinos: el desplegable tiene que salir del rechazo.
			_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
			 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z","items":[]}`)
		}
	})
	defer api.Close()

	form := url.Values{"status": {"open"}}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1/status", form, validSessionCookie(t))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("una transición inválida debía dar 422, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Desde «confirmado» no se puede pasar a «abierto»") {
		t.Error("el aviso debía decir de dónde a dónde se intentó mover la solicitud")
	}
	if !strings.Contains(out, "Destinos posibles:") || !strings.Contains(out, "seña solicitada") {
		t.Error("el aviso debía enumerar los destinos válidos que devolvió la plataforma")
	}
	if !strings.Contains(out, `<option value="deposit_requested">`) {
		t.Error("los destinos del 422 debían alimentar el desplegable")
	}
	if strings.Contains(out, `<option value="open">`) {
		t.Error("el destino rechazado no puede volver a ofrecerse")
	}
}

// TestSetIntakeStatusConflict: si otro operador se adelantó (409), no se re-pinta lo que el operador
// tenía delante —ya es viejo—: se recarga la ficha y se avisa.
func TestSetIntakeStatusConflict(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"la solicitud cambió de estado; recárgala y reintenta"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"cancelled",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T13:00:00Z",
		 "items":[],"allowed_transitions":[]}`)
	})
	defer api.Close()

	form := url.Values{"status": {"settled"}}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1/status", form, validSessionCookie(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("una carrera perdida debía dar 409, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Otro operador cambió esta solicitud") {
		t.Error("el 409 debía explicarse como una carrera, no como un error genérico")
	}
	if !strings.Contains(out, "estado · cancelado") {
		t.Error("tras el 409 debía mostrarse el estado REAL recargado")
	}
}

// TestSetIntakeStatusForbidden: perder la feature (o el scope) a mitad de sesión se cuenta como lo
// que es, sin filtrar el cuerpo del upstream.
func TestSetIntakeStatusForbidden(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"feature_not_enabled","feature":"cart_basic"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z",
		 "items":[],"allowed_transitions":["cancelled"]}`)
	})
	defer api.Close()

	form := url.Values{"status": {"cancelled"}}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1/status", form, validSessionCookie(t))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("un 403 debía propagarse como 403, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no puede cambiar el estado") {
		t.Error("el 403 debía traducirse a un aviso de permisos")
	}
	if strings.Contains(out, "feature_not_enabled") {
		t.Error("no debe filtrarse el cuerpo del upstream")
	}
}

// TestSetIntakeStatusWithoutStatus: el formulario sin estado no llega a la API.
func TestSetIntakeStatusWithoutStatus(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("no debía llamarse a la API sin estado elegido")
		}
		_, _ = io.WriteString(w, `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z",
		 "items":[],"allowed_transitions":["cancelled"]}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-1/status", url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un formulario sin estado debía dar 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Elige el estado") {
		t.Error("debía pedirse el estado al operador")
	}
}

// TestIntakesRequireSession: las tres rutas viven en el grupo protegido.
func TestIntakesRequireSession(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))
	for _, path := range []string{"/intakes", "/intakes/in-1"} {
		rec := getWithCookie(router, path, nil)
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
			t.Errorf("%s sin sesión debía redirigir al login, got %d", path, rec.Code)
		}
	}
}

// TestIntakeStatusLabelFallback: una clave que el diccionario de presentación no conoce se muestra
// TAL CUAL. Es la garantía de que un estado nuevo de la Ola 4 se vea aunque nadie actualice la tabla
// de nombres: se degrada a la clave, no desaparece ni se traduce mal.
func TestIntakeStatusLabelFallback(t *testing.T) {
	if got := intakeStatusLabel("deposit_refunded"); got != "deposit_refunded" {
		t.Errorf("una clave desconocida debía mostrarse tal cual, got %q", got)
	}
	if got := intakeStatusLabel("closed"); got != "confirmado" {
		t.Errorf("el `closed` legado debía leerse como «confirmado», got %q", got)
	}
}
