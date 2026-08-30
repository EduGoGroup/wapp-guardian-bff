package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// intakesGateMarker es el ancla del bloque gateado por `cart_basic` en la portada, e
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
	if !strings.Contains(out, "PROVISIONAL — migra a la consola de administración (Plan 047, ADR-0047)") {
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

// TestIntakesGateOmitsBlocksWithoutFeature (criterio de aceptación): sin `cart_basic`, ni la portada
// ni la propia pantalla emiten el bloque de solicitudes. Se verifica sobre el HTML RENDERIZADO, que
// es donde el gate importa.
func TestIntakesGateOmitsBlocksWithoutFeature(t *testing.T) {
	api := intakesAPI([]string{"menu", "llm_intent"}, func(w http.ResponseWriter, r *http.Request) {
		// La bandeja NO debe consultarse siquiera cuando la feature no está. La rama que atendía
		// `/api/v1/sessions` se fue con el dashboard (Plan 047 · T2.1): ahora esa ruta también cae
		// aquí, que es donde debe caer si alguien la resucita.
		t.Errorf("sin cart_basic no debía llamarse a %s", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	cookie := validSessionCookie(t)

	dash := getWithCookie(router, "/", cookie).Body.String()
	if strings.Contains(dash, intakesGateMarker) {
		t.Error("sin cart_basic, la portada NO debía emitir el bloque de solicitudes")
	}
	if strings.Contains(dash, "Abrir la bandeja") || strings.Contains(dash, `href="/intakes"`) {
		t.Error("sin la feature no debe quedar rastro de la bandeja en el HTML de la portada")
	}
	// El gate no se lleva por delante lo que no depende de la feature. El testigo era «Enviar un
	// mensaje» hasta el Plan 047 · T2.1; con el envío retirado, lo es la tarjeta del plan.
	if !strings.Contains(dash, "Plan y capacidades") {
		t.Error("las secciones base de la portada debían seguir emitiéndose")
	}

	list := getWithCookie(router, "/intakes", cookie).Body.String()
	if strings.Contains(list, intakesListMarker) || strings.Contains(list, "<table") {
		t.Error("sin cart_basic, la pantalla de solicitudes no debía emitir el listado")
	}
	if !strings.Contains(list, "no incluye la bandeja de solicitudes") {
		t.Error("la pantalla debía explicar por qué no hay nada, en vez de quedarse muda")
	}
}

// TestIntakesGateEmitsBlocksWithFeature: con `cart_basic`, el bloque de la portada y el enlace de la
// barra sí llegan al HTML.
func TestIntakesGateEmitsBlocksWithFeature(t *testing.T) {
	api := intakesAPI([]string{"cart_basic", "menu"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, intakesGateMarker) {
		t.Error("con cart_basic, la portada debía emitir el bloque de solicitudes")
	}
	if !strings.Contains(out, `<a href="/intakes" class="btn btn--text`) {
		t.Error("con la feature, la barra superior debía ofrecer el enlace a la bandeja")
	}
}

// TestIntakesNavHiddenWhenEntitlementsUnknown (fail-closed): si las features no se pudieron resolver,
// ninguna página emite el enlace a la bandeja. Vale también para las páginas que ni siquiera las
// consultan (variables), donde la clave no existe en los datos de plantilla.
//
// La segunda página era `/flows` hasta que el editor se retiró (Plan 047 · T6.6): sobre una ruta
// muerta el aserto habría seguido verde comprobando que un 404 no trae el enlace.
func TestIntakesNavHiddenWhenEntitlementsUnknown(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/entitlements":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/tenant/variables":
			_, _ = io.WriteString(w, `{"variables":{}}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	cookie := validSessionCookie(t)
	for _, path := range []string{"/", "/variables"} {
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
	if !strings.Contains(out, "PROVISIONAL — migra a la consola de administración (Plan 047, ADR-0047)") {
		t.Error("la marca PROVISIONAL debía estar en el detalle")
	}
}

// Cuerpos REALES de GET /api/v1/intakes/{id}, capturados del handler de la plataforma
// (wapp-cloud-platform `a804943`, `publicapi.getIntakeHandler` servido por su propio mux con el
// Context Token firmado). No están escritos a mano: se volcaron tal cual para que este lado se
// verifique contra los bytes que la API produce, no contra lo que creíamos que producía.
const (
	realDetailConfirmed = `{"id":"55555555-5555-5555-5555-555555555555","contact_id":"9f1c0a7e-0000-4000-8000-000000000abc",` +
		`"session_id":"sess-b","status":"confirmed","total":18000,"created_at":"2026-08-05T12:00:00Z",` +
		`"updated_at":"2026-08-05T12:00:00Z","items":[],` +
		`"allowed_transitions":["cancelled","deposit_requested","pending_approval","settled"]}`
	realDetailTerminal = `{"id":"33333333-3333-3333-3333-333333333333","contact_id":"9f1c0a7e-0000-4000-8000-000000000abc",` +
		`"session_id":"sess-b","status":"cancelled","total":18000,"created_at":"2026-08-03T12:00:00Z",` +
		`"updated_at":"2026-08-03T12:00:00Z","items":[],"allowed_transitions":[]}`
	realDetailLegacy = `{"id":"11111111-1111-1111-1111-111111111111","contact_id":"9f1c0a7e-0000-4000-8000-000000000abc",` +
		`"session_id":"sess-a","status":"confirmed","total":18000,"created_at":"2026-08-01T12:00:00Z",` +
		`"updated_at":"2026-08-01T12:00:00Z","items":[{"sku":"torta-v1","label":"Torta 10-12 porciones","qty":1,` +
		`"unit_price":18000},{"sku":"_shipping","label":"Envío — Providencia","qty":1,"unit_price":3000}],` +
		`"allowed_transitions":["cancelled","deposit_requested","pending_approval","settled"]}`
)

// TestIntakeDetailAgainstRealPlatformPayload es la prueba de contrato entre los dos repos: la
// pantalla se puebla con la respuesta REAL de la plataforma, por la vía buena (el campo del detalle)
// y sin pasar por ningún rechazo 422.
func TestIntakeDetailAgainstRealPlatformPayload(t *testing.T) {
	t.Run("estado con salidas", func(t *testing.T) {
		out := renderRealDetail(t, realDetailConfirmed)
		for _, want := range []string{"cancelled", "deposit_requested", "pending_approval", "settled"} {
			if !strings.Contains(out, `<option value="`+want+`">`) {
				t.Errorf("el desplegable debía ofrecer %q", want)
			}
		}
		if strings.Count(out, "<option") != 4 {
			t.Errorf("debían ser exactamente 4 opciones, got %d", strings.Count(out, "<option"))
		}
		// La vía es la del detalle: sin esto, el mismo HTML podría estar saliendo del 422.
		if strings.Contains(out, "vienen del último intento rechazado") {
			t.Error("los destinos debían salir del detalle, no de un rechazo previo")
		}
		if strings.Contains(out, "allowed_transitions") {
			t.Error("con el campo publicado no debe quedar el aviso de que falta")
		}
	})

	t.Run("estado terminal", func(t *testing.T) {
		out := renderRealDetail(t, realDetailTerminal)
		if strings.Contains(out, "<select") {
			t.Error("un `[]` de la plataforma significa terminal: no se pinta desplegable")
		}
		if !strings.Contains(out, "estado final") {
			t.Error("el `[]` real debía leerse como «estado final», no como «no lo sé»")
		}
		if strings.Contains(out, "allowed_transitions") {
			t.Error("«no hay acciones» no puede presentarse como «falta el campo»")
		}
	})

	t.Run("solicitud legada con líneas", func(t *testing.T) {
		out := renderRealDetail(t, realDetailLegacy)
		if !strings.Contains(out, "estado · confirmado") {
			t.Error("el `closed` legado ya llega normalizado a confirmed y debía pintarse como «confirmado»")
		}
		if !strings.Contains(out, "Torta 10-12 porciones") || !strings.Contains(out, "Envío — Providencia") {
			t.Error("las líneas reales debían pintarse con sus tildes intactas")
		}
		if !strings.Contains(out, "18000.00") || !strings.Contains(out, "3000.00") {
			t.Error("los precios unitarios reales debían pintarse")
		}
	})
}

// renderRealDetail sirve un cuerpo REAL de la plataforma y devuelve el HTML de la pantalla.
func renderRealDetail(t *testing.T, body string) string {
	t.Helper()
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("esta prueba no debía provocar un %s: la vía es la del detalle", r.Method)
		}
		_, _ = io.WriteString(w, body)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-real", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el detalle real debía renderizar 200, got %d", rec.Code)
	}
	return rec.Body.String()
}

// Detalles con `customization` (Plan 041 · T4.1b). NO son capturas de un servidor vivo, al revés que
// los tres de arriba: están escritos contra el DTO que la plataforma publica hoy
// (`publicapi.intakeItemDTO`, cloud `511924b`), donde el campo va SIN `omitempty` y por tanto viaja
// también vacío. Por eso el primer cuerpo lleva las dos formas —línea con personalización y línea con
// `""`— y el segundo la omite del todo: un servidor anterior a T4.1b no la manda, y la pantalla tiene
// que comportarse igual que con la vacía.
const (
	detailWithCustomization = `{"id":"in-7","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":42.50,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"sin cebolla","qty":1,"unit_price":36.50},
	          {"sku":"VELA","label":"Velas","customization":"","qty":1,"unit_price":6}],
	 "allowed_transitions":["cancelled","settled"]}`
	detailWithoutCustomizationField = `{"id":"in-7","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":42.50,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","qty":1,"unit_price":36.50}],
	 "allowed_transitions":["cancelled","settled"]}`
	detailHostileCustomization = `{"id":"in-7","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":42.50,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"sin \"cebolla\" & <b>mucha</b> salsa<script>alert(1)</script>",
	           "qty":1,"unit_price":36.50}],
	 "allowed_transitions":["cancelled"]}`
)

// TestIntakeDetailRendersCustomization (T4.1b, el quinto camino de D-041.17): la personalización de la
// línea llega a la pantalla que mira quien prepara el pedido. Si no llega, el cliente recibe el
// producto mal hecho — es un fallo de negocio, no un detalle cosmético (REQ-31/31b).
func TestIntakeDetailRendersCustomization(t *testing.T) {
	out := renderRealDetail(t, detailWithCustomization)

	// La personalización va PEGADA a su artículo: se comprueba el HTML exacto, no solo que el texto
	// aparezca suelto en la página. Una personalización en la fila equivocada es peor que no tenerla.
	if !strings.Contains(out, `<td>Hamburguesa<span class="cell-note">Personalización: sin cebolla</span></td>`) {
		t.Error("la personalización debía pintarse dentro de la celda de SU artículo")
	}
	// La línea que no la trae no ensucia la pantalla: la celda es el artículo pelado, sin span vacío,
	// sin guion y sin fila de más.
	if !strings.Contains(out, `<td>Velas</td>`) {
		t.Error("una línea sin personalización debía dejar la celda del artículo limpia")
	}
	if n := strings.Count(out, "cell-note"); n != 1 {
		t.Errorf("solo la línea con personalización debía emitir la sub-línea, got %d", n)
	}
	if n := strings.Count(out, "Personalización"); n != 1 {
		t.Errorf("el rótulo solo debía aparecer en la línea que lo lleva, got %d", n)
	}
	// El dinero no se mueve por comentar (INV-13): la pantalla pinta el total y los precios que manda
	// la plataforma, y la personalización no entra en ninguna cuenta porque aquí no se hace ninguna.
	if !strings.Contains(out, "total · 42.50") || !strings.Contains(out, "36.50") {
		t.Error("los importes debían ser los de la API, intactos")
	}
}

// TestIntakeDetailWithoutCustomizationStaysClean: contra un servidor que no publique el campo, la
// pantalla se comporta EXACTAMENTE igual que con la personalización vacía — no inventa un hueco ni
// declara nada. Las dos ausencias colapsan a propósito (ver apiclient.IntakeItem).
func TestIntakeDetailWithoutCustomizationStaysClean(t *testing.T) {
	out := renderRealDetail(t, detailWithoutCustomizationField)

	if !strings.Contains(out, `<td>Hamburguesa</td>`) {
		t.Error("sin el campo, la celda del artículo debía quedarse tal cual")
	}
	for _, forbidden := range []string{"cell-note", "Personalización"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("sin personalización no debía emitirse %q", forbidden)
		}
	}
}

// TestIntakeDetailEscapesCustomization: `customization` es de las poquísimas cadenas de esta consola
// escritas por el CLIENTE FINAL (D-041.15 lo dice del export, y aquí vale igual). Llega a la pantalla
// como TEXTO, nunca como marcado.
func TestIntakeDetailEscapesCustomization(t *testing.T) {
	out := renderRealDetail(t, detailHostileCustomization)

	if strings.Contains(out, "<script>alert(1)</script>") || strings.Contains(out, "<b>mucha</b>") {
		t.Error("la personalización se pintó como marcado: es texto del cliente final, va escapada")
	}
	if !strings.Contains(out, "sin &#34;cebolla&#34; &amp; &lt;b&gt;mucha&lt;/b&gt; salsa&lt;script&gt;") {
		t.Error("la personalización hostil debía llegar escapada y COMPLETA, no recortada")
	}
}

// Detalles con `customer_note` (Plan 041 · T4.1c). La cabecera la publica la plataforma en
// `publicapi.intakeDTO` (cloud `f7273af`), otra vez SIN `omitempty`: por eso hay un cuerpo con la
// clave vacía y otro que la omite del todo —un servidor anterior a T4.1c—, y la pantalla tiene que
// comportarse igual con los dos. El primero lleva ADEMÁS personalización de línea: es el caso que
// importa, porque las dos indicaciones conviven en la misma solicitud y no pueden confundirse.
const (
	detailWithCustomerNote = `{"id":"in-8","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":42.50,"customer_note":"dejarlo en portería, no tocar el timbre",
	 "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"sin cebolla","qty":1,"unit_price":36.50},
	          {"sku":"VELA","label":"Velas","customization":"","qty":1,"unit_price":6}],
	 "allowed_transitions":["cancelled","settled"]}`
	detailEmptyCustomerNote = `{"id":"in-8","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":36.50,"customer_note":"","created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"","qty":1,"unit_price":36.50}],
	 "allowed_transitions":["cancelled"]}`
	detailWithoutCustomerNoteField = `{"id":"in-8","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":36.50,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","qty":1,"unit_price":36.50}],
	 "allowed_transitions":["cancelled"]}`
	detailHostileCustomerNote = `{"id":"in-8","contact_id":"ct-op4","session_id":"s-1","status":"confirmed",
	 "total":36.50,"customer_note":"llamar al \"portero\" & <b>NO</b> al timbre<script>alert(1)</script>",
	 "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T11:00:00Z",
	 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"","qty":1,"unit_price":36.50}],
	 "allowed_transitions":["cancelled"]}`
)

// TestIntakeDetailRendersCustomerNote (T4.1c, REQ-33f): la indicación del PEDIDO llega a la pantalla,
// y llega SIN confundirse con la de la línea. Las dos son texto del cliente y las dos son instrucciones
// de producción, pero tienen alcances distintos: «sin cebolla» toca un artículo y «dejarlo en
// portería» toca el envío entero. Pintarlas iguales —o la del pedido dentro de una celda— haría que
// quien prepara aplicase la de portería a la hamburguesa, o al revés.
func TestIntakeDetailRendersCustomerNote(t *testing.T) {
	out := renderRealDetail(t, detailWithCustomerNote)

	// HTML exacto: la nota del pedido es un párrafo propio con su rótulo, no una sub-línea de celda.
	if !strings.Contains(out, `<p class="order-note"><span class="order-note__label">Indicación del pedido:</span> dejarlo en portería, no tocar el timbre</p>`) {
		t.Error("la indicación del pedido debía pintarse en su propio párrafo de la cabecera")
	}
	// La de la línea sigue donde estaba, pegada a SU artículo (T4.1b): las dos conviven sin mezclarse.
	if !strings.Contains(out, `<td>Hamburguesa<span class="cell-note">Personalización: sin cebolla</span></td>`) {
		t.Error("la personalización de línea debía seguir dentro de la celda de su artículo")
	}
	// Y la del pedido está FUERA de la tabla: si cayera dentro de una celda, leería como si aplicase
	// solo a ese artículo. La cabecera se pinta antes que la tabla, así que el orden lo demuestra.
	noteAt, tableAt := strings.Index(out, "order-note"), strings.Index(out, "<table")
	if noteAt < 0 || tableAt < 0 || noteAt > tableAt {
		t.Errorf("la indicación del pedido debía ir en la cabecera, antes de la tabla (nota %d, tabla %d)", noteAt, tableAt)
	}
	// Cada rótulo aparece UNA vez y en su sitio: ni la nota del pedido se repite por línea, ni la
	// personalización se promueve a la cabecera.
	for label, want := range map[string]int{"Indicación del pedido": 1, "Personalización": 1, "order-note": 2, "cell-note": 1} {
		if n := strings.Count(out, label); n != want {
			t.Errorf("%q debía aparecer %d vez/veces, got %d", label, want, n)
		}
	}
	// El dinero no se mueve por indicar (INV-13): esta capa pinta los importes que manda la API.
	if !strings.Contains(out, "total · 42.50") || !strings.Contains(out, "36.50") {
		t.Error("los importes debían ser los de la API, intactos")
	}
}

// TestIntakeDetailWithoutCustomerNoteStaysClean: sin nota, la pantalla no se ensucia. Las dos ausencias
// posibles —clave vacía y clave que no viaja— se tratan igual, como en `customization`.
func TestIntakeDetailWithoutCustomerNoteStaysClean(t *testing.T) {
	for name, body := range map[string]string{
		"nota vacía":        detailEmptyCustomerNote,
		"campo que no vino": detailWithoutCustomerNoteField,
	} {
		t.Run(name, func(t *testing.T) {
			out := renderRealDetail(t, body)

			for _, forbidden := range []string{"order-note", "Indicación del pedido"} {
				if strings.Contains(out, forbidden) {
					t.Errorf("sin indicación del pedido no debía emitirse %q", forbidden)
				}
			}
			// Y la ficha sigue completa: no se pinta un hueco donde iría la nota.
			if !strings.Contains(out, "estado · confirmado") || !strings.Contains(out, "<td>Hamburguesa</td>") {
				t.Error("la ficha debía renderizarse igual de completa sin la nota")
			}
		})
	}
}

// TestIntakeDetailEscapesCustomerNote: `customer_note` la escribe un CLIENTE FINAL por WhatsApp —es,
// con `customization`, la única entrada no confiable de esta pantalla—. Llega como TEXTO, nunca como
// marcado, y llega ENTERA: recortarla en el primer `<` perdería la instrucción justo donde importa.
func TestIntakeDetailEscapesCustomerNote(t *testing.T) {
	out := renderRealDetail(t, detailHostileCustomerNote)

	if strings.Contains(out, "<script>alert(1)</script>") || strings.Contains(out, "<b>NO</b>") {
		t.Error("la indicación del pedido se pintó como marcado: es texto del cliente final, va escapada")
	}
	if !strings.Contains(out, `llamar al &#34;portero&#34; &amp; &lt;b&gt;NO&lt;/b&gt; al timbre&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Error("la indicación hostil debía llegar escapada y COMPLETA, no recortada")
	}
	// El atributo `class` del párrafo no puede quedar roto por las comillas de la nota: si la nota se
	// hubiera colado en el marcado, este ancla exacto no aparecería.
	if !strings.Contains(out, `<p class="order-note"><span class="order-note__label">Indicación del pedido:</span> llamar al `) {
		t.Error("el marcado del párrafo debía quedar intacto con una nota llena de comillas")
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
	// Los dos silencios de la API se tratan igual entre sí y DISTINTO del `[]` terminal: el campo que
	// no viene y el `null` explícito significan «no lo sé», nunca «no hay acciones posibles».
	for name, body := range map[string]string{
		"sin el campo": `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z","items":[]}`,
		"campo nulo": `{"id":"in-1","contact_id":"c","session_id":"s","status":"confirmed",
		 "total":1,"created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z","items":[],
		 "allowed_transitions":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
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
		})
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
