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
