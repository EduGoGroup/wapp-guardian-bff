package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReanalyzeIntakeNoMandaVia es el candado de D-044.51 dicho EN BYTES: el cuerpo que sale de este
// cliente no lleva `via`.
//
// El contrato admite el campo, pero solo para AFIRMAR la vía ya configurada del tenant —una distinta
// es un 400 `invalid_via`—, así que mandarla es, en el mejor caso, una copia que se desincroniza el
// día que el tenant la cambie. Omitirla es equivalente y no puede mentir. Se mira el cuerpo REAL y no
// la estructura de Go: un campo con `omitempty` mal puesto se vería aquí y no allí.
func TestReanalyzeIntakeNoMandaVia(t *testing.T) {
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w,
			`{"intake_id":"in-1","revision_no":3,"job_id":"job-9","via":"local","status":"processing"}`)
	}))
	defer srv.Close()

	client := NewIntakesClient(NewTransport(srv.URL))
	out, err := client.ReanalyzeIntake(context.Background(), "tok", "in-1", "")
	if err != nil {
		t.Fatalf("ReanalyzeIntake: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatalf("el cuerpo debía ser JSON: %v (%s)", err, seen)
	}
	if _, ok := body["via"]; ok {
		t.Errorf("el cuerpo NO puede llevar `via`, got %s", seen)
	}
	if _, ok := body["text"]; ok {
		t.Errorf("sin material extra la clave `text` no se manda, got %s", seen)
	}

	// El 200 se decodifica entero, incluidos los dos campos que se leen mal con facilidad.
	if out.RevisionNo != 3 || out.JobID != "job-9" || out.Via != "local" {
		t.Errorf("la respuesta debía llegar entera, got %+v", out)
	}
	if out.Status != "processing" {
		t.Errorf("`status` vale siempre «processing» y no es el estado del job, got %q", out.Status)
	}

	// Con material extra sí viaja `text`, y la vía sigue sin viajar.
	if _, err := client.ReanalyzeIntake(context.Background(), "tok", "in-1", "son 30 tequeños crudos"); err != nil {
		t.Fatalf("ReanalyzeIntake con texto: %v", err)
	}
	body = nil
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatalf("el cuerpo debía ser JSON: %v (%s)", err, seen)
	}
	if body["text"] != "son 30 tequeños crudos" {
		t.Errorf("el material extra debía viajar tal cual, got %s", seen)
	}
	if _, ok := body["via"]; ok {
		t.Errorf("tampoco con material extra puede viajar `via`, got %s", seen)
	}
}

// TestReanalyzeIntakeSeparaLosSeisRechazos: el código HTTP NO basta para saber qué pasó —dos rechazos
// comparten el 403 y tres el 422—, así que cada uno tiene que llegar al llamante como lo que es.
//
// Mezclarlos tiene consecuencia directa en la pantalla: el 403 lleva al paywall y el 422 de
// credencial a los ajustes, y confundirlos manda al dueño a comprar algo que ya tiene.
func TestReanalyzeIntakeSeparaLosSeisRechazos(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{
			name: "403 de la capacidad base", status: http.StatusForbidden,
			body: `{"error":"feature_not_enabled","feature":"llm_intake"}`,
			check: func(t *testing.T, err error) {
				missing, ok := FeatureNotEnabledOf(err)
				if !ok || missing.Feature != "llm_intake" {
					t.Errorf("debía llegar la capacidad que falta, got %v", err)
				}
			},
		},
		{
			name: "403 del add-on de la vía", status: http.StatusForbidden,
			body: `{"error":"feature_not_enabled","feature":"api_llm"}`,
			check: func(t *testing.T, err error) {
				missing, ok := FeatureNotEnabledOf(err)
				if !ok || missing.Feature != "api_llm" {
					t.Errorf("las dos capacidades tienen que distinguirse, got %v", err)
				}
			},
		},
		{
			name: "422 de la credencial", status: http.StatusUnprocessableEntity,
			body: `{"error":"llm_credentials_missing","via":"api"}`,
			check: func(t *testing.T, err error) {
				creds, ok := LLMCredentialsMissingOf(err)
				if !ok || creds.Via != "api" {
					t.Errorf("la falta de credencial NO es un 403 de plan, got %v", err)
				}
				if _, isFeature := FeatureNotEnabledOf(err); isFeature {
					t.Error("la credencial que falta no puede leerse como una capacidad que falta")
				}
			},
		},
		{
			name: "422 de la fuente podada", status: http.StatusUnprocessableEntity,
			body: `{"error":"source_unavailable","reason":"purged"}`,
			check: func(t *testing.T, err error) {
				source, ok := SourceUnavailableOf(err)
				if !ok || !source.Purged() {
					t.Errorf("la poda debía llegar como tal, got %v", err)
				}
			},
		},
		{
			name: "422 de la fuente que nunca se guardó", status: http.StatusUnprocessableEntity,
			body: `{"error":"source_unavailable","reason":"never_stored"}`,
			check: func(t *testing.T, err error) {
				source, ok := SourceUnavailableOf(err)
				if !ok || source.Reason != SourceNeverStored {
					t.Errorf("«nunca se guardó» debía llegar como tal, got %v", err)
				}
				if source.Purged() {
					t.Error("«nunca se guardó» NO es una poda: son dos historias distintas")
				}
			},
		},
		{
			name: "422 de la concurrencia", status: http.StatusUnprocessableEntity,
			body: `{"error":"reanalysis_in_progress","job_id":"job-42"}`,
			check: func(t *testing.T, err error) {
				running, ok := ReanalysisInProgressOf(err)
				if !ok || running.JobID != "job-42" {
					t.Errorf("el job en curso debía llegar con su id, got %v", err)
				}
			},
		},
		{
			name: "400 de la vía", status: http.StatusBadRequest,
			body: `{"error":"invalid_via","via":"gemini"}`,
			check: func(t *testing.T, err error) {
				invalid, ok := InvalidViaOf(err)
				if !ok || invalid.Via != "gemini" {
					t.Errorf("el rechazo de vía debía llegar nombrado, got %v", err)
				}
			},
		},
		{
			name: "404 de la solicitud ajena", status: http.StatusNotFound, body: `{}`,
			check: func(t *testing.T, err error) {
				if StatusCodeOf(err) != http.StatusNotFound {
					t.Errorf("el 404 debía conservar su código, got %v", err)
				}
				if _, ok := FeatureNotEnabledOf(err); ok {
					t.Error("un 404 no es una capacidad que falta")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := NewIntakesClient(NewTransport(srv.URL)).
				ReanalyzeIntake(context.Background(), "tok", "in-1", "")
			if err == nil {
				t.Fatal("un rechazo debía devolver error")
			}
			tc.check(t, err)
		})
	}
}

// TestRevisionDistingueLaClaveAusenteDeLaVacia es la regla de D-044.52 §3 en su capa: `LiteralPrunedAt`
// pregunta por PRESENCIA DE CLAVE.
//
// Con un `string` pelado, la clave ausente y un `""` explícito colapsarían en el mismo cero y las dos
// razones —«nunca lo hubo» y «se podó»— dirían lo mismo. Por eso viaja como puntero, y por eso este
// test mira las TRES formas en que la clave puede llegar.
func TestRevisionDistingueLaClaveAusenteDeLaVacia(t *testing.T) {
	body := `{"id":"in-1","status":"pending_approval","items":[],"revisions":[
	  {"revision_no":1,"kind":"interpreted","created_by":"system",
	   "created_at":"2026-07-13T09:55:00Z","payload":` + ambarInterpretedPayload + `},
	  {"revision_no":2,"kind":"interpreted","created_by":"owner",
	   "literal_pruned_at":"2026-08-20T03:00:00Z",
	   "created_at":"2026-08-20T03:00:00Z","payload":` + ambarInterpretedPayload + `}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).GetIntake(context.Background(), "tok", "in-1")
	if err != nil {
		t.Fatalf("GetIntake: %v", err)
	}
	if len(detail.Revisions) != 2 {
		t.Fatalf("debían llegar 2 revisiones, got %d", len(detail.Revisions))
	}

	// Clave AUSENTE: nunca lo hubo. El puntero es nil, no una cadena vacía.
	sinPoda := detail.Revisions[0]
	if sinPoda.LiteralPrunedAt != nil {
		t.Errorf("sin la clave, el campo debía quedar nil, got %q", *sinPoda.LiteralPrunedAt)
	}
	if sinPoda.LiteralPruned() {
		t.Error("sin la clave NO hay poda que declarar")
	}
	if sinPoda.PrunedAt() != "" {
		t.Error("sin poda no hay instante que enseñar")
	}

	// Clave PRESENTE: se podó, y trae cuándo.
	conPoda := detail.Revisions[1]
	if !conPoda.LiteralPruned() {
		t.Fatal("con la clave presente la revisión declara la poda")
	}
	if conPoda.PrunedAt() != "2026-08-20T03:00:00Z" {
		t.Errorf("el instante de la poda debía llegar tal cual, got %q", conPoda.PrunedAt())
	}

	// Y el campo NO se inventa cuando la plataforma no lo manda: un `omitempty` en la ida evitaría
	// que este cliente publicara una poda que nadie declaró.
	raw, err := json.Marshal(sinPoda)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reencoded map[string]any
	if err := json.Unmarshal(raw, &reencoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := reencoded["literal_pruned_at"]; ok {
		t.Errorf("sin poda, la clave no se re-emite, got %s", raw)
	}
}

// TestRevisionsOfOrdenaPorNumeroYNoPorPosicion: el orden con el que la API devuelve el histórico no es
// contrato, así que la navegación del detalle no puede colgarse de él.
//
// El fixture las manda 3, 1, 2 y con una clase intercalada: por posición, la navegación empezaría en
// la 3 y la comparación arrancaría en la 2 — o sea, mirando algo que no es lo último.
func TestRevisionsOfOrdenaPorNumeroYNoPorPosicion(t *testing.T) {
	body := `{"id":"in-1","status":"pending_approval","items":[],"revisions":[
	  {"revision_no":3,"kind":"interpreted","created_by":"owner","created_at":"c","payload":` + ambarInterpretedPayload + `},
	  {"revision_no":1,"kind":"interpreted","created_by":"system","created_at":"a","payload":` + ambarInterpretedPayload + `},
	  {"revision_no":2,"kind":"corrected","created_by":"owner","created_at":"b","payload":{}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).GetIntake(context.Background(), "tok", "in-1")
	if err != nil {
		t.Fatalf("GetIntake: %v", err)
	}

	interpreted := detail.RevisionsOf(RevisionKindInterpreted)
	if len(interpreted) != 2 {
		t.Fatalf("debían salir 2 interpretaciones, got %d", len(interpreted))
	}
	if interpreted[0].RevisionNo != 1 || interpreted[1].RevisionNo != 3 {
		t.Errorf("debían salir de la más antigua a la más nueva, got %d y %d",
			interpreted[0].RevisionNo, interpreted[1].RevisionNo)
	}
	// La `corrected` no entra: no tiene interpretación que comparar.
	if got := detail.RevisionsOf(RevisionKindCorrected); len(got) != 1 || got[0].RevisionNo != 2 {
		t.Errorf("la clase pedida es la que sale, got %+v", got)
	}
	// Y una clase sin revisiones devuelve vacío, no la lista entera.
	if got := detail.RevisionsOf(RevisionKindApproved); len(got) != 0 {
		t.Errorf("una clase sin revisiones debía salir vacía, got %d", len(got))
	}
}
