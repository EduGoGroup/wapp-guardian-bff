package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ambarInterpretedPayload es el borrador del caso Ambar (design §7.5) tal como lo congela la
// revisión `interpreted`: una línea del catálogo CON precio, la que el catálogo no supo casar SIN
// precio, una de regalo a CERO y el envío.
//
// El `0` y el `null` conviven en el mismo fixture a propósito: son los dos valores que esta capa
// tiene que poder distinguir, y un fixture con solo uno de los dos no probaría nada.
const ambarInterpretedPayload = `{
  "version": 1,
  "message_ts": "2026-07-13T09:55:00Z",
  "analysis": {"provider": "", "model": "qwen3:4b", "source": "event_thread", "reanalyzed_from": null},
  "delivery_date": "2026-07-22",
  "source_text": "quiero una torta de chocolate de 10 o 12 porciones",
  "media_refs": [{"ref": "wapp/media/abc", "kind": "ptt", "label": "🎙️ audio del cliente — escúchalo"}],
  "lines": [
    {"kind": "matched", "sku": "TORTA-CHOC-12", "label": "Torta chocolate húmedo", "qty": 1,
     "unit_price": 45, "customization": "sin lactosa",
     "range": {"min": 10, "max": 12, "unit": "porciones"},
     "evidence": "de 10 o 12 porciones", "match": {"strategy": "fuzzy_osa", "confidence": 0.91}},
    {"kind": "matched", "sku": "DECO-INF", "label": "Decoración infantil", "qty": 1, "unit_price": 0},
    {"kind": "unmatched", "label": "tequeños", "qty": 2, "unit_price": null,
     "evidence": "2 kilos de tequeños"},
    {"kind": "shipping", "label": "Envío", "qty": 1, "unit_price": null, "note": "por confirmar zona"}
  ],
  "suggested_questions": ["¿De cuántas porciones la quieres?"]
}`

// TestInterpretationTellsNullApartFromZero es EL test de esta capa: `null` («todavía no hay
// precio») y `0` («va de regalo») son cosas distintas y tienen que llegar distintas.
//
// Un `float64` pelado las colapsa en 0 y la pantalla imprime «0.00» en las dos, que es decirle al
// dueño que la torta que el catálogo no supo casar es gratis.
func TestInterpretationTellsNullApartFromZero(t *testing.T) {
	payload, err := DecodeInterpretation(json.RawMessage(ambarInterpretedPayload))
	if err != nil {
		t.Fatalf("DecodeInterpretation: %v", err)
	}
	if len(payload.Lines) != 4 {
		t.Fatalf("el borrador debía traer 4 líneas, got %d", len(payload.Lines))
	}

	regalo := payload.Lines[1]
	if !regalo.HasPrice() {
		t.Error("la línea de regalo trae `unit_price: 0`: SÍ tiene precio, y es cero")
	}
	if regalo.Price() != 0 {
		t.Errorf("el precio de la línea de regalo debía ser 0, got %v", regalo.Price())
	}

	sinPrecio := payload.Lines[2]
	if sinPrecio.HasPrice() {
		t.Error("la línea `unmatched` trae `unit_price: null`: NO tiene precio, y no es un cero")
	}
	if sinPrecio.UnitPrice != nil {
		t.Error("`null` tenía que quedar como puntero nil, que es lo único que distingue las dos")
	}
	if sinPrecio.SKU != "" {
		t.Errorf("la línea `unmatched` no tiene sku, got %q", sinPrecio.SKU)
	}
}

// TestInterpretationDecodesTheRestOfTheContract: el resto de campos del payload llegan enteros. Va
// junto porque son un solo contrato y un campo que se cae en silencio no lo nota nadie.
func TestInterpretationDecodesTheRestOfTheContract(t *testing.T) {
	payload, err := DecodeInterpretation(json.RawMessage(ambarInterpretedPayload))
	if err != nil {
		t.Fatalf("DecodeInterpretation: %v", err)
	}
	if payload.DeliveryDate != "2026-07-22" || payload.SourceText == "" {
		t.Errorf("fecha de entrega y texto original debían llegar, got %q / %q",
			payload.DeliveryDate, payload.SourceText)
	}
	// `provider` sale CADENA VACÍA en la interpretación normal: solo lo rellena el re-análisis.
	if payload.Analysis.Provider != "" || payload.Analysis.Model != "qwen3:4b" {
		t.Errorf("analysis debía llegar con provider vacío y modelo, got %+v", payload.Analysis)
	}
	if payload.Analysis.ReanalyzedFrom != nil {
		t.Error("`reanalyzed_from: null` es «esta es la primera lectura» y debía quedar nil")
	}
	line := payload.Lines[0]
	if line.Range == nil || line.Range.Min != 10 || line.Range.Max != 12 || line.Range.Unit != "porciones" {
		t.Errorf("el rango pedido debía llegar sin colapsar, got %+v", line.Range)
	}
	if line.Match == nil || line.Match.Strategy != "fuzzy_osa" {
		t.Errorf("la procedencia del match debía llegar, got %+v", line.Match)
	}
	if payload.Lines[2].Match != nil {
		t.Error("una línea `unmatched` no tiene match y debía quedar nil")
	}
	if len(payload.MediaRefs) != 1 || !payload.MediaRefs[0].IsAudio() {
		t.Errorf("el audio de la cabecera debía llegar y reconocerse como audio, got %+v", payload.MediaRefs)
	}
}

// TestSuggestedQuestionsAbsentIsNotEmpty: la lista vacía significa «no había nada que preguntar» y
// la clave AUSENTE significa que el tenant no tiene `llm_intake`. La plataforma las emite distintas
// a propósito, y colapsarlas aquí le diría al dueño que el LLM no preguntó nada cuando lo que pasa
// es que no lo ha contratado.
func TestSuggestedQuestionsAbsentIsNotEmpty(t *testing.T) {
	empty, err := DecodeInterpretation(json.RawMessage(`{"lines":[],"suggested_questions":[]}`))
	if err != nil {
		t.Fatalf("DecodeInterpretation: %v", err)
	}
	if !empty.QuestionsKnown() {
		t.Error("`[]` es una respuesta: la clave estaba y hay que saberlo")
	}
	if len(empty.Questions()) != 0 {
		t.Error("`[]` no trae preguntas")
	}

	absent, err := DecodeInterpretation(json.RawMessage(`{"lines":[]}`))
	if err != nil {
		t.Fatalf("DecodeInterpretation: %v", err)
	}
	if absent.QuestionsKnown() {
		t.Error("sin la clave, la pantalla NO puede decir que no había preguntas")
	}
}

// TestGetIntakeDecodesOverdueAndRevisions: los dos campos nuevos del detalle llegan, y `overdue`
// llega como lo que es —un booleano al margen del estado— sin tocar el `status`.
func TestGetIntakeDecodesOverdueAndRevisions(t *testing.T) {
	body := `{"id":"in-1","status":"pending_approval","overdue":true,"total":57,
	  "items":[],"allowed_transitions":["confirmed"],
	  "revisions":[{"revision_no":1,"kind":"interpreted","created_by":"system",
	    "created_at":"2026-07-13T09:55:00Z","payload":` + ambarInterpretedPayload + `}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).GetIntake(context.Background(), "tok", "in-1")
	if err != nil {
		t.Fatalf("GetIntake: %v", err)
	}
	if !detail.Overdue {
		t.Error("`overdue` debía llegar")
	}
	if detail.Status != "pending_approval" {
		t.Errorf("`overdue` NO cambia el estado: sigue siendo pending_approval, got %q", detail.Status)
	}
	if len(detail.Revisions) != 1 || detail.Revisions[0].Kind != RevisionKindInterpreted {
		t.Fatalf("el histórico debía llegar con su revisión, got %+v", detail.Revisions)
	}
	if detail.LastRevisionOf(RevisionKindInterpreted) == nil {
		t.Fatal("la última revisión interpretada debía encontrarse")
	}
	if detail.LastRevisionOf(RevisionKindApproved) != nil {
		t.Error("no hay revisión `approved` y no debía inventarse ninguna")
	}
}

// TestLastRevisionOfPicksTheHighestNumber: «la última» es la de mayor `revision_no`, NUNCA la
// última del slice. El orden con el que la API devuelve el histórico no es contrato, y ordenar por
// posición dejaría al dueño mirando una interpretación vieja el día que ese orden cambie.
func TestLastRevisionOfPicksTheHighestNumber(t *testing.T) {
	detail := &IntakeDetail{Revisions: []IntakeRevision{
		{RevisionNo: 3, Kind: RevisionKindInterpreted},
		{RevisionNo: 4, Kind: RevisionKindCorrected},
		{RevisionNo: 1, Kind: RevisionKindInterpreted},
	}}
	rev := detail.LastRevisionOf(RevisionKindInterpreted)
	if rev == nil || rev.RevisionNo != 3 {
		t.Fatalf("debía elegir la interpretada número 3, got %+v", rev)
	}
	if got := detail.RevisionsAfter(3); got != 1 {
		t.Errorf("después de la 3 hay 1 revisión, got %d", got)
	}
}

// TestIntakeFilterSerializesSort: el orden viaja como un parámetro más, y vacío no viaja (que es lo
// que deja decidir a la API su propio default).
func TestIntakeFilterSerializesSort(t *testing.T) {
	if got := (IntakeFilter{Sort: IntakeSortOldest}).query(); got != "?sort=oldest" {
		t.Errorf("query = %q, quiero ?sort=oldest", got)
	}
	if got := (IntakeFilter{Status: "open"}).query(); strings.Contains(got, "sort") {
		t.Errorf("sin orden no se manda la clave, got %q", got)
	}
}

// TestCorrectIntakeItemsAddsTheFlagAndReplaceDoesNot es la CERO-REGRESIÓN del 041 dicha en bytes:
// el guardado de siempre manda exactamente el cuerpo de siempre —sin la clave `as_correction`— y la
// corrección del 044 es ese mismo cuerpo con la clave puesta. Una puerta, dos conductas.
func TestCorrectIntakeItemsAddsTheFlagAndReplaceDoesNot(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = string(raw)
		_, _ = io.WriteString(w, `{"id":"in-1","status":"pending_approval","items":[]}`)
	}))
	defer srv.Close()

	client := NewIntakesClient(NewTransport(srv.URL))
	items := []IntakeItem{{SKU: "TEQ-30", Label: "Tequeños", Qty: 2, UnitPrice: 6}}

	if _, err := client.ReplaceIntakeItems(context.Background(), "tok", "in-1", items); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}
	if strings.Contains(seen, "as_correction") {
		t.Errorf("el guardado del 041 NO puede llevar la clave nueva, got %s", seen)
	}
	want := `{"items":[{"sku":"TEQ-30","label":"Tequeños","customization":"","qty":2,"unit_price":6}]}`
	if seen != want {
		t.Errorf("el cuerpo del 041 cambió:\n got %s\nwant %s", seen, want)
	}

	if _, err := client.CorrectIntakeItems(context.Background(), "tok", "in-1", items); err != nil {
		t.Fatalf("CorrectIntakeItems: %v", err)
	}
	if !strings.Contains(seen, `"as_correction":true`) {
		t.Errorf("la corrección del 044 debía llevar la clave, got %s", seen)
	}
}

// TestApproveIntakeSendsOnlyTheText: el cuerpo de la aprobación es UN campo, y ni las líneas ni el
// total viajan — ya están escritos, y mandarlos abriría la puerta a aprobar otra cosa.
func TestApproveIntakeSendsOnlyTheText(t *testing.T) {
	var seen, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen, path = string(raw), r.URL.Path
		_, _ = io.WriteString(w, `{"id":"in-1","status":"confirmed","items":[]}`)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).
		ApproveIntake(context.Background(), "tok", "in-1", "Tu pedido: 1 torta — 45.00")
	if err != nil {
		t.Fatalf("ApproveIntake: %v", err)
	}
	if path != "/api/v1/intakes/in-1/approve" {
		t.Errorf("ruta = %q", path)
	}
	if seen != `{"rendered_text":"Tu pedido: 1 torta — 45.00"}` {
		t.Errorf("cuerpo = %s", seen)
	}
	if detail.Status != "confirmed" {
		t.Errorf("la respuesta es el detalle ya transicionado, got %q", detail.Status)
	}
}

// TestApproveIntakeParsesItsRejections: los tres rechazos propios de la aprobación llegan tipados,
// que es lo que permite a la pantalla decir qué arreglar en vez de enseñar un código.
func TestApproveIntakeParsesItsRejections(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		check  func(*testing.T, error)
	}{
		"faltan precios": {
			status: http.StatusBadRequest,
			body:   `{"error":"lines_without_price","lines":[{"index":2,"label":"tequeños"}]}`,
			check: func(t *testing.T, err error) {
				missing, ok := LinesWithoutPriceOf(err)
				if !ok {
					t.Fatalf("debía ser *LinesWithoutPriceError, got %T", err)
				}
				if len(missing.Lines) != 1 || missing.Lines[0].Label != "tequeños" || missing.Lines[0].Index != 2 {
					t.Errorf("la línea debía llegar con posición y etiqueta, got %+v", missing.Lines)
				}
			},
		},
		"estado que no aprueba": {
			status: http.StatusUnprocessableEntity,
			body:   `{"error":"not_approvable","status":"confirmed","approvable_in":["pending_approval"]}`,
			check: func(t *testing.T, err error) {
				notApprovable, ok := NotApprovableOf(err)
				if !ok {
					t.Fatalf("debía ser *NotApprovableError, got %T", err)
				}
				if notApprovable.Status != "confirmed" || len(notApprovable.ApprovableIn) != 1 {
					t.Errorf("debía traer dónde está y desde dónde se aprueba, got %+v", notApprovable)
				}
			},
		},
		"carrera con otro operador": {
			status: http.StatusUnprocessableEntity,
			body:   `{"error":"invalid_transition","status":"needs_info","requested":"confirmed","allowed":["pending_approval"]}`,
			check: func(t *testing.T, err error) {
				if _, ok := NotApprovableOf(err); ok {
					t.Fatal("una transición inválida NO es un «este estado no aprueba»: son dos avisos distintos")
				}
				if _, ok := InvalidTransitionOf(err); !ok {
					t.Fatalf("debía ser *InvalidTransitionError, got %T", err)
				}
			},
		},
		"texto vacío": {
			status: http.StatusBadRequest,
			body:   `{"error":"rendered_text es obligatorio"}`,
			check: func(t *testing.T, err error) {
				if _, ok := LinesWithoutPriceOf(err); ok {
					t.Fatal("un 400 sin la clave `lines_without_price` no es ese rechazo")
				}
				msg, ok := RejectionMessageOf(err)
				if !ok || msg == "" {
					t.Fatalf("el motivo del 400 debía conservarse, got %v", err)
				}
			},
		},
		"conflicto": {
			status: http.StatusConflict,
			body:   `{"error":"conflict"}`,
			check: func(t *testing.T, err error) {
				if StatusCodeOf(err) != http.StatusConflict {
					t.Fatalf("el 409 debía salir por código, got %v", err)
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := NewIntakesClient(NewTransport(srv.URL)).
				ApproveIntake(context.Background(), "tok", "in-1", "texto")
			if err == nil {
				t.Fatal("debía fallar")
			}
			tc.check(t, err)
		})
	}
}

// TestRequestIntakeInfoSendsTheQuestion: la pregunta viaja sola en su campo, y el 422 de esta puerta
// —que llega SIN estados permitidos— no se confunde con un estado terminal.
func TestRequestIntakeInfoSendsTheQuestion(t *testing.T) {
	var seen, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen, path = string(raw), r.URL.Path
		_, _ = io.WriteString(w, `{"id":"in-1","status":"needs_info","items":[]}`)
	}))
	defer srv.Close()

	detail, err := NewIntakesClient(NewTransport(srv.URL)).
		RequestIntakeInfo(context.Background(), "tok", "in-1", "¿de cuántas porciones?")
	if err != nil {
		t.Fatalf("RequestIntakeInfo: %v", err)
	}
	if path != "/api/v1/intakes/in-1/request-info" {
		t.Errorf("ruta = %q", path)
	}
	if seen != `{"question":"¿de cuántas porciones?"}` {
		t.Errorf("cuerpo = %s", seen)
	}
	if detail.Status != "needs_info" {
		t.Errorf("la respuesta es el detalle ya transicionado, got %q", detail.Status)
	}
}

// TestRequestIntakeInfoParsesBareInvalidTransition: el 422 de esta puerta no trae cuerpo propio, y
// aun así tiene que salir tipado para que la pantalla no lo confunda con un fallo de red.
func TestRequestIntakeInfoParsesBareInvalidTransition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"invalid_transition"}`)
	}))
	defer srv.Close()

	_, err := NewIntakesClient(NewTransport(srv.URL)).
		RequestIntakeInfo(context.Background(), "tok", "in-1", "¿cuántas?")
	if _, ok := InvalidTransitionOf(err); !ok {
		t.Fatalf("debía ser *InvalidTransitionError, got %T (%v)", err, err)
	}
}
