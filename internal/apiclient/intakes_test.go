package apiclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIntakeFilterQuery: lo vacío no viaja (un `status=` vacío diría lo mismo que no mandarlo y
// ensucia la traza), y la paginación solo se manda cuando se pide.
func TestIntakeFilterQuery(t *testing.T) {
	if got := (IntakeFilter{}).query(); got != "" {
		t.Errorf("un filtro vacío no debía producir query, got %q", got)
	}
	got := IntakeFilter{From: "2026-08-01", Status: "confirmed", Page: 3, PageSize: 25}.query()
	want := "?from=2026-08-01&page=3&page_size=25&status=confirmed"
	if got != want {
		t.Errorf("query = %q, quiero %q", got, want)
	}
}

// TestGetIntakeDistinguishesUnknownFromTerminal es la razón de que AllowedTransitions sea un puntero:
// «la plataforma no lo dice» y «este estado es final» son cosas distintas, y una UI que las confunda
// le asegura al operador que no puede tocar un pedido que sí puede tocar.
func TestGetIntakeDistinguishesUnknownFromTerminal(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantNil  bool
		wantLen  int
		scenario string
	}{
		"sin el campo": {
			body:     `{"id":"in-1","status":"confirmed","items":[]}`,
			wantNil:  true,
			scenario: "la API todavía no publica los destinos",
		},
		"campo nulo": {
			body:     `{"id":"in-1","status":"confirmed","items":[],"allowed_transitions":null}`,
			wantNil:  true,
			scenario: "un null explícito tampoco es una lista vacía",
		},
		"lista vacía": {
			body:     `{"id":"in-1","status":"settled","items":[],"allowed_transitions":[]}`,
			wantNil:  false,
			scenario: "estado terminal",
		},
		"con destinos": {
			body:     `{"id":"in-1","status":"open","items":[],"allowed_transitions":["cancelled","confirmed"]}`,
			wantNil:  false,
			wantLen:  2,
			scenario: "estado con salidas",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			detail, err := NewIntakesClient(NewTransport(srv.URL)).GetIntake(context.Background(), "tok", "in-1")
			if err != nil {
				t.Fatalf("GetIntake: %v", err)
			}
			if tc.wantNil {
				if detail.AllowedTransitions != nil {
					t.Fatalf("%s: el campo ausente debía quedar nil, got %v", tc.scenario, *detail.AllowedTransitions)
				}
				return
			}
			if detail.AllowedTransitions == nil {
				t.Fatalf("%s: el campo presente no podía quedar nil", tc.scenario)
			}
			if len(*detail.AllowedTransitions) != tc.wantLen {
				t.Errorf("%s: destinos = %v, quiero %d", tc.scenario, *detail.AllowedTransitions, tc.wantLen)
			}
		})
	}
}

// TestSetIntakeStatusParsesInvalidTransition: el 422 se convierte en un error TIPADO con los destinos
// válidos; perderlos dejaría al llamante probando estados a ciegas.
func TestSetIntakeStatusParsesInvalidTransition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"invalid_transition","status":"confirmed","requested":"open",
		 "allowed":["cancelled","settled"]}`)
	}))
	defer srv.Close()

	_, err := NewIntakesClient(NewTransport(srv.URL)).SetIntakeStatus(context.Background(), "tok", "in-1", "open")
	invalid, ok := InvalidTransitionOf(err)
	if !ok {
		t.Fatalf("el 422 debía dar un *InvalidTransitionError, got %v", err)
	}
	if invalid.Status != "confirmed" || invalid.Requested != "open" {
		t.Errorf("el rechazo debía decir de dónde a dónde, got %+v", invalid)
	}
	if len(invalid.Allowed) != 2 || invalid.Allowed[0] != "cancelled" {
		t.Errorf("los destinos válidos debían conservarse, got %v", invalid.Allowed)
	}
}

// TestSetIntakeStatusMapsConflictAndNotFound: el resto de códigos siguen siendo *APIError, que es lo
// que StatusCodeOf sabe leer (409 = otro operador se adelantó; 404 = no es del tenant).
func TestSetIntakeStatusMapsConflictAndNotFound(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusNotFound, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":"nope"}`)
		}))
		_, err := NewIntakesClient(NewTransport(srv.URL)).SetIntakeStatus(context.Background(), "tok", "in-1", "settled")
		if StatusCodeOf(err) != code {
			t.Errorf("StatusCodeOf debía devolver %d, got %d (%v)", code, StatusCodeOf(err), err)
		}
		srv.Close()
	}
}

// TestListIntakesKeepsRejectionReason: el 400 es el único fallo del listado con un motivo accionable
// (el filtro venía mal), así que su mensaje se conserva; el 403 sigue siendo un *APIError legible por
// código.
func TestListIntakesKeepsRejectionReason(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"status desconocido"}`)
	}))
	defer bad.Close()

	_, err := NewIntakesClient(NewTransport(bad.URL)).ListIntakes(context.Background(), "tok", IntakeFilter{})
	msg, ok := RejectionMessageOf(err)
	if !ok || msg != "status desconocido" {
		t.Errorf("el motivo del 400 debía conservarse, got %q (%v)", msg, err)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"feature_not_enabled","feature":"cart_basic"}`)
	}))
	defer forbidden.Close()

	_, err = NewIntakesClient(NewTransport(forbidden.URL)).ListIntakes(context.Background(), "tok", IntakeFilter{})
	if StatusCodeOf(err) != http.StatusForbidden {
		t.Errorf("el 403 debía seguir siendo legible por código, got %v", err)
	}
}

// TestReplaceIntakeItemsSendsTheWholeSetAsPut fija el contrato que se verificó contra el código de
// la plataforma (cloud 284a90f): PUT a /items con el conjunto COMPLETO de líneas dentro de `items`.
// Es lo que hace que mandar dos veces el mismo cuerpo deje la solicitud igual.
func TestReplaceIntakeItemsSendsTheWholeSetAsPut(t *testing.T) {
	var (
		gotMethod, gotPath string
		gotBody            []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"id":"in-1","status":"pending_approval","total":9,
		 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"","qty":1,"unit_price":8},
		          {"sku":"QUESO-EX","label":"Queso extra","customization":"","qty":1,"unit_price":1}],
		 "allowed_transitions":["confirmed"]}`)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
		context.Background(), "tok", "in-1", []IntakeItem{
			{SKU: "HAM", Label: "Hamburguesa", Qty: 1, UnitPrice: 8},
			{SKU: "QUESO-EX", Label: "Queso extra", Qty: 1, UnitPrice: 1},
		})
	if err != nil {
		t.Fatalf("ReplaceIntakeItems devolvió error: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/intakes/in-1/items" {
		t.Errorf("debía llamarse PUT /api/v1/intakes/in-1/items, got %s %s", gotMethod, gotPath)
	}
	want := `{"items":[{"sku":"HAM","label":"Hamburguesa","customization":"","qty":1,"unit_price":8},` +
		`{"sku":"QUESO-EX","label":"Queso extra","customization":"","qty":1,"unit_price":1}]}`
	if string(gotBody) != want {
		t.Errorf("cuerpo = %s\nquiero  = %s", gotBody, want)
	}
	// La respuesta es el detalle YA actualizado: con eso repinta la consola sin un segundo GET.
	if detail.Total != 9 || len(detail.Items) != 2 {
		t.Errorf("debía volver el detalle corregido (total 9, 2 líneas), got %v con %d líneas",
			detail.Total, len(detail.Items))
	}
}

// TestReplaceIntakeItemsSendsEmptyListNotNull: quitar todas las líneas es una edición legítima. La
// plataforma distingue a propósito `null` («no mandaste la clave» ⇒ 400) de `[]` («déjala sin
// líneas» ⇒ se aplica), así que un slice nil no puede salir de aquí como `null`.
func TestReplaceIntakeItemsSendsEmptyListNotNull(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"id":"in-1","status":"pending_approval","items":[]}`)
	}))
	defer srv.Close()

	if _, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
		context.Background(), "tok", "in-1", nil); err != nil {
		t.Fatalf("vaciar las líneas devolvió error: %v", err)
	}
	if string(gotBody) != `{"items":[]}` {
		t.Errorf("un conjunto vacío debía viajar como {\"items\":[]}, got %s", gotBody)
	}
}

// TestReplaceIntakeItemsParsesInvalidItems: el 400 por líneas mal formadas trae TODOS los defectos
// con su posición y su campo, y así tienen que llegar. Aplanarlo a «la plataforma dijo que no»
// dejaría al dueño arreglando sus líneas a ciegas.
func TestReplaceIntakeItemsParsesInvalidItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_items","errors":[
		 {"index":0,"field":"sku","message":"el sku es obligatorio"},
		 {"index":1,"field":"unit_price","message":"el precio no puede ser negativo"}]}`)
	}))
	defer srv.Close()

	_, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
		context.Background(), "tok", "in-1", []IntakeItem{{SKU: "X"}})
	invalid, ok := InvalidItemsOf(err)
	if !ok {
		t.Fatalf("el 400 con lista de defectos debía salir como *InvalidItemsError, got %#v", err)
	}
	if len(invalid.Defects) != 2 {
		t.Fatalf("debían llegar los 2 defectos, got %d", len(invalid.Defects))
	}
	if invalid.Defects[1].Index != 1 || invalid.Defects[1].Field != "unit_price" {
		t.Errorf("el segundo defecto debía conservar índice y campo, got %+v", invalid.Defects[1])
	}
	if invalid.Defects[0].Message != "el sku es obligatorio" {
		t.Errorf("el motivo debía llegar verbatim, got %q", invalid.Defects[0].Message)
	}
}

// TestReplaceIntakeItemsParsesNotEditable: el 422 dice dónde está la solicitud y desde dónde SÍ se
// edita. Ese `editable_in` es lo que permite a la consola no replicar el ciclo de vida.
func TestReplaceIntakeItemsParsesNotEditable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"not_editable","status":"confirmed","editable_in":["pending_approval"]}`)
	}))
	defer srv.Close()

	_, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
		context.Background(), "tok", "in-1", []IntakeItem{{SKU: "X"}})
	notEditable, ok := NotEditableOf(err)
	if !ok {
		t.Fatalf("el 422 debía salir como *NotEditableError, got %#v", err)
	}
	if notEditable.Status != "confirmed" || len(notEditable.EditableIn) != 1 ||
		notEditable.EditableIn[0] != "pending_approval" {
		t.Errorf("el rechazo debía conservar estado y estados editables, got %+v", notEditable)
	}
}

// TestReplaceIntakeItemsMapsTheRestByStatus: 404 (no es tuya), 409 (otro se adelantó), 403 (sin la
// feature o sin el scope) y el 400 con motivo suelto salen cada uno por su vía, porque la pantalla
// dice cosas distintas con cada uno.
func TestReplaceIntakeItemsMapsTheRestByStatus(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusConflict, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":"lo que sea"}`)
		}))
		_, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
			context.Background(), "tok", "in-1", []IntakeItem{{SKU: "X"}})
		if got := StatusCodeOf(err); got != code {
			t.Errorf("el %d debía llegar como *APIError con su status, got %d (%v)", code, got, err)
		}
		srv.Close()
	}

	// El 400 sin lista de defectos (cuerpo ilegible, tope de líneas) conserva el motivo: es lo
	// único con lo que el operador sabe qué corregir.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"la edición trae 300 líneas y el máximo es 200"}`)
	}))
	defer srv.Close()
	_, err := NewIntakesClient(NewTransport(srv.URL)).ReplaceIntakeItems(
		context.Background(), "tok", "in-1", []IntakeItem{{SKU: "X"}})
	msg, ok := RejectionMessageOf(err)
	if !ok || msg != "la edición trae 300 líneas y el máximo es 200" {
		t.Errorf("el 400 con motivo debía conservarlo, got %q (%v)", msg, err)
	}
}
