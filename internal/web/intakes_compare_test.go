package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Anclas del bloque de comparación (Plan 044 · T4.7, design §7.6). Si el gate cierra o el bloque no
// se emite, estas cadenas no están en el HTML: es lo que distingue un gate server-side de un
// `display:none`.
const (
	intakeCompareMarker    = `id="section-intake-compare"`
	intakeOriginalMarker   = `id="section-intake-original"`
	intakeUnderstoodMarker = `id="section-intake-understood"`
	intakeRevisionsMarker  = `id="section-intake-revisions"`
	intakeRegenerateMarker = `id="section-intake-regenerate"`
)

// burgerLiteral es el texto del cliente del caso de las hamburguesas. Es la cadena que la mitad de
// estos tests busca en el HTML y la otra mitad exige que NO esté en el log.
const burgerLiteral = "quiero 1 hamburguesa con queso y cebolla"

// burgerSourceLine es el `source_text` ya serializado. Va como fragmento de JSON —y no como un campo
// que se pone a cadena vacía— porque la tarea entera es distinguir la CLAVE AUSENTE: la plataforma lo
// emite con `omitempty`, así que «sin literal» es que la clave no está, no que valga "".
const burgerSourceLine = `"source_text": "` + burgerLiteral + `",`

// burgerPayload arma el payload de una revisión `interpreted` del caso de las hamburguesas: el
// cliente pide UNA y se interpretan TRES (2 con queso + 1 sin cebolla), que es la discrepancia que el
// §7.6 existe para hacer visible.
//
// `provider` vacío es el caso COMÚN y no una rareza del fixture: la plataforma solo lo rellena en el
// re-análisis del dueño.
func burgerPayload(provider, sourceLine string) string {
	return `{
	  "version": 1,
	  "message_ts": "2026-08-27T10:00:00Z",
	  "analysis": {"provider": "` + provider + `", "model": "qwen3:4b", "source": "event_thread", "reanalyzed_from": null},
	  "delivery_date": "2026-08-28",
	  ` + sourceLine + `
	  "lines": [
	    {"kind": "matched", "sku": "BURG-QUESO", "label": "Hamburguesa con queso", "qty": 2, "unit_price": 5},
	    {"kind": "matched", "sku": "BURG-SIN-CEB", "label": "Hamburguesa sin cebolla", "qty": 1, "unit_price": 5},
	    {"kind": "shipping", "label": "Envío", "qty": 1, "unit_price": null, "note": "por confirmar zona"}
	  ],
	  "suggested_questions": []
	}`
}

// burgerRevision arma UNA revisión interpretada. `prunedLine` es el `literal_pruned_at` ya
// serializado —o vacío—, y va FUERA del payload porque ahí es donde vive: es hermano de `created_at`.
func burgerRevision(no int, provider, sourceLine, prunedLine string) string {
	return `{"revision_no":` + strconv.Itoa(no) + `,"kind":"interpreted","created_by":"system",` + prunedLine +
		`"created_at":"2026-08-27T10:00:00Z","payload":` + burgerPayload(provider, sourceLine) + `}`
}

// burgerDetail arma el detalle con las revisiones dadas, EN EL ORDEN EN QUE SE LE PASEN. Que el orden
// sea del test y no del fixture es deliberado: hay un test que las manda del revés para comprobar que
// la pantalla ordena por `revision_no` y no por posición.
func burgerDetail(revisions ...string) string {
	return `{"id":"in-burger","contact_id":"ct-b","session_id":"s-9","status":"pending_approval",
	  "total":15,"overdue":false,
	  "items":[{"sku":"BURG-QUESO","label":"Hamburguesa con queso","customization":"","qty":2,"unit_price":5},
	           {"sku":"BURG-SIN-CEB","label":"Hamburguesa sin cebolla","customization":"","qty":1,"unit_price":5}],
	  "revisions":[` + strings.Join(revisions, ",") + `],
	  "allowed_transitions":["confirmed","needs_info"],
	  "created_at":"2026-08-27T10:00:00Z","updated_at":"2026-08-27T10:01:00Z"}`
}

// llmDetailAPI levanta la API fake con las features dadas, sirviendo el detalle en el GET y delegando
// el resto en `handle` (nil ⇒ 500, que es lo que hace que una ruta no prevista se note).
func llmDetailAPI(features []string, body string, handle http.HandlerFunc) *httptest.Server {
	return intakesAPI(features, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/intakes/") {
			_, _ = io.WriteString(w, body)
			return
		}
		if handle != nil {
			handle(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
}

// TestIntakeCompareRendersOriginalNextToInterpretation (T4.7, §7.6) es el criterio central: el texto
// del cliente y lo que se entendió, EN DOS COLUMNAS de la misma fila, con la discrepancia legible sin
// abrir nada más — pidió 1 hamburguesa y se interpretaron 3 unidades.
func TestIntakeCompareRendersOriginalNextToInterpretation(t *testing.T) {
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")), nil)
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el detalle debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{
		intakeCompareMarker, intakeOriginalMarker, intakeUnderstoodMarker,
		"ORIGINAL DEL CLIENTE", "LO QUE ENTENDIÓ",
		burgerLiteral,
		"2× Hamburguesa con queso",
		"1× Hamburguesa sin cebolla",
		// La discrepancia, dicha en voz alta: 1 pedida contra 3 interpretadas.
		"3 unidades interpretadas en 2 líneas",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la comparación debía contener %q", want)
		}
	}
	// AL LADO, NO DEBAJO: las dos columnas viven dentro de la misma fila y en ese orden. Sin este
	// assert el bloque pasaría igual apilando una encima de otra, que es justo lo que el §7.6 dice
	// que no sirve.
	row := strings.Index(out, `<div class="row compare">`)
	original := strings.Index(out, intakeOriginalMarker)
	understood := strings.Index(out, intakeUnderstoodMarker)
	if row < 0 || original < row || understood < original {
		t.Errorf("el original y lo interpretado debían ir en la misma fila y en ese orden "+
			"(fila=%d original=%d interpretado=%d)", row, original, understood)
	}
	// El envío NO cuenta como «lo que entendió»: lo pone wApp, no sale del texto del cliente, y
	// contarlo inflaría la discrepancia con una línea que el cliente nunca pidió.
	if strings.Contains(out, "1× Envío") {
		t.Error("la línea de envío no puede contarse entre lo que se entendió del cliente")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (ADR-0035: server-side, cero framework)")
	}
	// NO-REGRESIÓN: la marca PROVISIONAL sigue estando.
	if !strings.Contains(out, "PROVISIONAL — migra a KMP (Plan 045/047, ADR-0035)") {
		t.Error("la marca PROVISIONAL debía seguir en el detalle")
	}
}

// TestIntakeCompareDiceViaNoRegistradaYNuncaLlmLocal (D-044.52 (b)) es el candado del encabezado: la
// revisión 1 —el caso común, que nace del pipeline y no del re-análisis del dueño— NO tiene vía que
// enseñar, y la pantalla lo dice en vez de inventarse la local.
//
// El assert negativo va sobre la PÁGINA ENTERA y no solo sobre el encabezado a propósito: un rótulo
// suelto en cualquier otro párrafo diciendo «LLM local» se leería como si esta revisión lo afirmara.
func TestIntakeCompareDiceViaNoRegistradaYNuncaLlmLocal(t *testing.T) {
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")), nil)
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t))
	out := rec.Body.String()

	if !strings.Contains(out, "Interpretación · revisión 1 · vía no registrada") {
		t.Error("con `provider` vacío el encabezado debía decir «vía no registrada»")
	}
	if strings.Contains(out, "LLM local") {
		t.Error("una revisión sin `provider` NO puede pintarse como «LLM local»: nadie registró esa vía")
	}
	// Y el borrador del §7.5 —que habla del MISMO dato unos centímetros más abajo— lo dice con las
	// mismas palabras. Dos redacciones para el mismo hecho en la misma página harían dudar de si
	// hablan de lo mismo.
	if !strings.Contains(out, "Interpretado por vía no registrada") {
		t.Error("el borrador debía decir la vía con la misma redacción que la comparación")
	}
	// Y con vía registrada sí se dice, porque entonces consta.
	api2 := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "anthropic", burgerSourceLine, "")), nil)
	defer api2.Close()

	out2 := getWithCookie(NewRouter(authTestCfg(api2.URL)), "/intakes/in-burger", validSessionCookie(t)).Body.String()
	if !strings.Contains(out2, "vía anthropic") {
		t.Error("con `provider` relleno el encabezado debía enseñar la vía registrada")
	}
	if strings.Contains(out2, "vía no registrada") {
		t.Error("con `provider` relleno NO debía decirse que no consta")
	}
}

// TestIntakeCompareDistingueLosTresCasosDelLiteral (D-044.52 §3) es la regla de las DOS CLAVES: la
// presencia de `source_text` y la presencia de `literal_pruned_at`, nunca su valor.
//
// Y el detalle las distingue SIN llamar a `/reanalyze`, que es una escritura auditada: preguntar por
// qué no hay original no puede costar una escritura.
func TestIntakeCompareDistingueLosTresCasosDelLiteral(t *testing.T) {
	const pruned = `"literal_pruned_at":"2026-08-20T03:00:00Z",`

	for _, tc := range []struct {
		name       string
		sourceLine string
		prunedLine string
		want       []string
		absent     []string
		regenerate bool
	}{
		{
			name: "con literal", sourceLine: burgerSourceLine, prunedLine: "",
			want: []string{burgerLiteral}, absent: []string{"nunca se guardó", "se podó"},
			regenerate: true,
		},
		{
			// Clave AUSENTE en las dos: nunca lo hubo. Es el único de los dos que se ve en campo.
			name: "sin literal y sin poda", sourceLine: "", prunedLine: "",
			want:   []string{"No hay original guardado", "nunca se guardó"},
			absent: []string{burgerLiteral, "se podó"},
		},
		{
			// `literal_pruned_at` PRESENTE: existió y venció. Hoy no lo produce nadie —el 046 no
			// construyó la poda—, así que esta rama existe por contrato y se prueba con dobles.
			name: "sin literal y con poda", sourceLine: "", prunedLine: pruned,
			want:   []string{"se podó", "2026-08-20T03:00:00Z", "retención"},
			absent: []string{burgerLiteral, "nunca se guardó"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 🔑 Contar las llamadas a `/reanalyze` es parte del criterio: la razón por la que no hay
			// original la daba antes el 422 de esa ruta, que es una ESCRITURA AUDITADA. Preguntar por
			// qué falta el literal no puede costar una escritura, y por eso D-044.52 §3 publicó
			// `literal_pruned_at` en la lectura.
			escrituras := 0
			api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
				burgerDetail(burgerRevision(1, "", tc.sourceLine, tc.prunedLine)),
				func(w http.ResponseWriter, r *http.Request) {
					escrituras++
					w.WriteHeader(http.StatusInternalServerError)
				})
			defer api.Close()

			rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t))
			if rec.Code != http.StatusOK {
				t.Fatalf("el detalle debía renderizar 200, got %d", rec.Code)
			}
			out := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("la columna del original debía contener %q", want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("la columna del original NO debía contener %q", absent)
				}
			}
			// El botón NUNCA se esconde: sin literal queda deshabilitado con la razón delante.
			if !strings.Contains(out, ">Regenerar<") {
				t.Fatal("el botón Regenerar no puede desaparecer: se deshabilita con su motivo")
			}
			disabled := strings.Contains(out, `class="btn btn--filled" disabled>Regenerar<`)
			if disabled == tc.regenerate {
				t.Errorf("Regenerar habilitado=%v y se esperaba habilitado=%v", !disabled, tc.regenerate)
			}
			if escrituras != 0 {
				t.Errorf("la razón se deduce de la LECTURA: no se puede gastar el 422 de `/reanalyze` "+
					"(hubo %d escrituras)", escrituras)
			}
		})
	}
}

// TestIntakeCompareBordeSinLlmIntake es el estado de borde (1) —el que le pasa al tenant REAL de UAT
// hoy—: las páginas van tras `cart_basic` pero `/reanalyze` exige `llm_intake` dentro del servicio.
// Sin ese gate el dueño vería un botón que devuelve 403.
func TestIntakeCompareBordeSinLlmIntake(t *testing.T) {
	api := llmDetailAPI([]string{"cart_basic"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")), nil)
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la solicitud se lee igual sin `llm_intake`: debía dar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	// La comparación SÍ se pinta: sin `llm_intake` la bandeja se lee, lo que falta es regenerar.
	if !strings.Contains(out, intakeCompareMarker) || !strings.Contains(out, burgerLiteral) {
		t.Error("sin `llm_intake` la comparación se sigue leyendo: solo cae el botón")
	}
	if !strings.Contains(out, intakeRegenerateMarker) {
		t.Error("el formulario de regenerar no se esconde: se deshabilita con su motivo a la vista")
	}
	if !strings.Contains(out, `class="btn btn--filled" disabled>Regenerar<`) {
		t.Error("sin `llm_intake` el botón Regenerar debía quedar deshabilitado")
	}
	for _, want := range []string{"llm_intake", "no incluye el análisis con IA", "ampliando el plan"} {
		if !strings.Contains(out, want) {
			t.Errorf("la razón del botón deshabilitado debía contener %q", want)
		}
	}
	if !strings.Contains(out, "PROVISIONAL — migra a KMP (Plan 045/047, ADR-0035)") {
		t.Error("la marca PROVISIONAL debía seguir en el detalle")
	}
	// Y no se gasta un viaje a una ruta que la plataforma va a rechazar: el POST corta aquí.
	form := url.Values{}
	rec = postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger/reanalyze",
		form, validSessionCookie(t))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin `llm_intake` el POST debía cortar con 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no incluye el análisis con IA") {
		t.Error("el 403 propio debía decir qué capacidad falta")
	}
}

// TestIntakeReanalyzeBordesDeVIAYCredencial son los estados de borde (2) y (3), y el punto del test es
// que NO SE DICEN IGUAL: el 403 es «tu plan no lo incluye» y lleva a contratar; el 422 es «te falta la
// credencial» y lleva a los ajustes. Mezclarlos mandaría al dueño a comprar algo que ya tiene.
//
// ⚠️ Los dos se verifican CON DOBLES y nadie los da por probados contra UAT: con `tenant_llm` vacía y
// sin API key, la vía `api` no es alcanzable en campo (D-044.51).
func TestIntakeReanalyzeBordesDeVIAYCredencial(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		want       []string
		absent     []string
	}{
		{
			name:   "sin el add-on api_llm",
			status: http.StatusForbidden, body: `{"error":"feature_not_enabled","feature":"api_llm"}`,
			wantStatus: http.StatusForbidden,
			want:       []string{"api_llm", "add-on"},
			absent:     []string{"No hay nada que contratar"},
		},
		{
			name:   "con el add-on y sin credencial",
			status: http.StatusUnprocessableEntity, body: `{"error":"llm_credentials_missing","via":"api"}`,
			wantStatus: http.StatusUnprocessableEntity,
			want:       []string{"credencial configurada", "No hay nada que contratar", "PUT /api/v1/tenant-llm"},
			absent:     []string{"add-on"},
		},
		{
			name:   "ya hay un re-análisis en curso",
			status: http.StatusUnprocessableEntity, body: `{"error":"reanalysis_in_progress","job_id":"job-42"}`,
			wantStatus: http.StatusUnprocessableEntity,
			want:       []string{"Ya hay una regeneración en curso", "job-42"},
		},
		{
			name:   "el literal ya no está",
			status: http.StatusUnprocessableEntity, body: `{"error":"source_unavailable","reason":"never_stored"}`,
			wantStatus: http.StatusUnprocessableEntity,
			want:       []string{"No hay original guardado", "nunca se guardó"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
				burgerDetail(burgerRevision(1, "", burgerSourceLine, "")),
				func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				})
			defer api.Close()

			rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger/reanalyze",
				url.Values{}, validSessionCookie(t))
			if rec.Code != tc.wantStatus {
				t.Fatalf("el rechazo debía responder %d, got %d", tc.wantStatus, rec.Code)
			}
			out := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("el aviso debía contener %q", want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("el aviso NO debía contener %q: los dos rechazos llevan a sitios distintos", absent)
				}
			}
		})
	}
}

// TestIntakeCompareNavegaEntreRevisionesSinJS: saltar de una interpretación a otra es un enlace con
// `?revision=N` y un re-render del servidor, no una pestaña de JavaScript (ADR-0035).
//
// El fixture manda las revisiones DEL REVÉS y con una `corrected` en medio: eso es lo que caza una
// navegación que ordenara por posición o que listara clases que no tienen nada que comparar.
func TestIntakeCompareNavegaEntreRevisionesSinJS(t *testing.T) {
	corrected := `{"revision_no":2,"kind":"corrected","created_by":"owner",
	  "created_at":"2026-08-27T10:05:00Z","payload":{"lines":[]}}`
	detail := burgerDetail(
		burgerRevision(3, "anthropic", burgerSourceLine, ""),
		corrected,
		burgerRevision(1, "", burgerSourceLine, ""),
	)
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"}, detail, nil)
	defer api.Close()
	router := NewRouter(authTestCfg(api.URL))

	// Sin query se mira la ÚLTIMA interpretación, que es la 3 aunque venga primera en el JSON.
	out := getWithCookie(router, "/intakes/in-burger", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "Interpretación · revisión 3 · vía anthropic") {
		t.Error("sin `?revision` debía mirarse la última interpretación")
	}
	if !strings.Contains(out, intakeRevisionsMarker) {
		t.Fatal("con dos interpretaciones debía ofrecerse la navegación")
	}
	if !strings.Contains(out, `href="/intakes/in-burger?revision=1"`) {
		t.Error("debía ofrecerse el enlace a la revisión 1")
	}
	if strings.Contains(out, `href="/intakes/in-burger?revision=3"`) {
		t.Error("la revisión que se está mirando no se enlaza a sí misma")
	}
	if strings.Contains(out, "revisión 2 ·") {
		t.Error("una revisión `corrected` no entra en la navegación: no tiene interpretación que comparar")
	}
	// ORDEN POR `revision_no` Y NO POR POSICIÓN: el fixture las manda 3, 2, 1.
	if strings.Index(out, "revisión 1 · vía no registrada") > strings.Index(out, "revisión 3 · vía anthropic — la que ves") {
		t.Error("la navegación debía ir de la más antigua a la más nueva, no en el orden del JSON")
	}
	if strings.Contains(out, "<script") {
		t.Error("la navegación entre revisiones no puede introducir JS (ADR-0035)")
	}

	// Con `?revision=1` se mira la vieja, y su encabezado dice lo que ESA revisión registró.
	out = getWithCookie(router, "/intakes/in-burger?revision=1", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "Interpretación · revisión 1 · vía no registrada") {
		t.Error("`?revision=1` debía enseñar la interpretación 1 con su propia vía")
	}
	if !strings.Contains(out, `href="/intakes/in-burger?revision=3"`) {
		t.Error("desde la 1 debía poder volverse a la 3")
	}
	// 🔑 Lo que NO cambia: el borrador editable del §7.5 sigue saliendo de la ÚLTIMA. Navegar por el
	// histórico es leer; si el formulario siguiera al histórico, el dueño guardaría precios sobre una
	// lectura vieja sin enterarse.
	if !strings.Contains(out, intakeDraftMarker) || !strings.Contains(out, "Interpretación · revisión 3") {
		t.Error("el borrador que se corrige debía seguir siendo el de la última revisión")
	}

	// Una revisión que no existe no rompe la página: se dice y se enseña la última.
	out = getWithCookie(router, "/intakes/in-burger?revision=99", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "no tiene ninguna revisión 99") {
		t.Error("una revisión inexistente debía decirse, no redirigirse en silencio")
	}
	if !strings.Contains(out, "Interpretación · revisión 3 · vía anthropic") {
		t.Error("con una revisión inexistente debía enseñarse la última")
	}
}

// TestIntakeReanalyzeNoImponeVia es el candado de D-044.51: el cuerpo que sale de esta consola NO
// lleva `via`.
//
// Mandar una vía distinta de la configurada es un 400 `invalid_via`, y mandar la misma es una copia
// que se desincroniza el día que el tenant la cambie. Omitirla es equivalente y no puede mentir. El
// assert mira el CUERPO REAL que viaja por el cable, no la vista.
func TestIntakeReanalyzeNoImponeVia(t *testing.T) {
	var seen []byte
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")),
		func(w http.ResponseWriter, r *http.Request) {
			seen, _ = io.ReadAll(r.Body)
			_, _ = io.WriteString(w,
				`{"intake_id":"in-burger","revision_no":4,"job_id":"job-7","via":"local","status":"processing"}`)
		})
	defer api.Close()
	router := NewRouter(authTestCfg(api.URL))

	rec := postFormWithCookie(router, "/intakes/in-burger/reanalyze", url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el re-análisis debía responder 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatalf("el cuerpo enviado debía ser JSON: %v (%s)", err, seen)
	}
	if _, ok := body["via"]; ok {
		t.Errorf("el cuerpo NO puede llevar `via`: la vía la fija la configuración del tenant (%s)", seen)
	}
	if _, ok := body["text"]; ok {
		t.Errorf("sin material extra la clave `text` no se manda, got %s", seen)
	}

	// 🔴 El 200 NO significa que esté lista: la revisión que anuncia todavía no existe.
	out := rec.Body.String()
	for _, want := range []string{"TODAVÍA NO ESTÁ LISTA", "revisión 4", "job-7"} {
		if !strings.Contains(out, want) {
			t.Errorf("el aviso del re-análisis debía contener %q", want)
		}
	}
	if strings.Contains(out, "Interpretación · revisión 4") {
		t.Error("la revisión anunciada NO existe todavía: la página no puede pintarla")
	}

	// Con material extra, viaja el texto — y sigue sin viajar la vía.
	rec = postFormWithCookie(router, "/intakes/in-burger/reanalyze",
		url.Values{intakeActionFieldReanalyzeText: {"lo dijo por teléfono: son 3 hamburguesas"}},
		validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el re-análisis con material extra debía responder 200, got %d", rec.Code)
	}
	body = nil
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatalf("el cuerpo enviado debía ser JSON: %v (%s)", err, seen)
	}
	if body["text"] != "lo dijo por teléfono: son 3 hamburguesas" {
		t.Errorf("el material extra debía viajar tal cual, got %s", seen)
	}
	if _, ok := body["via"]; ok {
		t.Errorf("tampoco con material extra puede viajar `via` (%s)", seen)
	}
}

// TestIntakeReanalyzeRechazaElMaterialExtraDemasiadoLargo: el tope del contrato son 280 RUNAS, y se
// cuentan en runas —no en bytes— porque un texto con acentos cabría de sobra en runas y se pasaría en
// bytes. Se corta aquí para no gastar un viaje que la plataforma va a rechazar.
func TestIntakeReanalyzeRechazaElMaterialExtraDemasiadoLargo(t *testing.T) {
	llamado := false
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")),
		func(w http.ResponseWriter, r *http.Request) {
			llamado = true
			w.WriteHeader(http.StatusInternalServerError)
		})
	defer api.Close()

	// 281 runas acentuadas: 281 caracteres y 562 bytes. Contadas en bytes, un texto de 200 runas ya
	// «sobraría»; contadas en runas, este es el primero que no cabe.
	largo := strings.Repeat("á", intakeReanalyzeMaxRunes+1)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger/reanalyze",
		url.Values{intakeActionFieldReanalyzeText: {largo}}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el material extra pasado de largo debía responder 400, got %d", rec.Code)
	}
	if llamado {
		t.Error("no se gasta un viaje a la plataforma con un cuerpo que va a rechazar")
	}
	out := rec.Body.String()
	if !strings.Contains(out, "281") || !strings.Contains(out, "280") {
		t.Error("el aviso debía decir cuánto cabe y cuánto se mandó")
	}
	// Y lo tecleado no se tira: se repinta para poder recortarlo.
	if !strings.Contains(out, strings.Repeat("á", 50)) {
		t.Error("el material extra rechazado debía volver al formulario")
	}
	// Justo en el tope SÍ pasa: el límite es «como mucho 280», no «menos de 280».
	llamado = false
	rec = postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger/reanalyze",
		url.Values{intakeActionFieldReanalyzeText: {strings.Repeat("á", intakeReanalyzeMaxRunes)}},
		validSessionCookie(t))
	if !llamado {
		t.Errorf("un material extra de %d runas debía llegar a la plataforma (got %d)",
			intakeReanalyzeMaxRunes, rec.Code)
	}
}

// TestIntakeCompareElLiteralNoSeCacheaNiSeLoguea es el criterio 4 de la tarea, y va escrito para NO
// ser un assert vacuo.
//
// 🔴 La lección que lo motiva: un assert de «esto no aparece» pasa solo cuando el dato ni siquiera
// podía llegar. Por eso los dos casos comprueban PRIMERO que la rama se recorrió —el literal está en
// el HTML, y el log tiene la línea que sí se escribe— y solo entonces afirman lo que falta.
func TestIntakeCompareElLiteralNoSeCacheaNiSeLoguea(t *testing.T) {
	t.Run("al leer la solicitud", func(t *testing.T) {
		logs := captureLogs(t)
		api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
			burgerDetail(burgerRevision(1, "", burgerSourceLine, "")), nil)
		defer api.Close()

		rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("el detalle debía renderizar 200, got %d", rec.Code)
		}
		// La rama SE RECORRIÓ: el literal llegó hasta el HTML. Sin esto, lo de abajo no probaría nada.
		if !strings.Contains(rec.Body.String(), burgerLiteral) {
			t.Fatal("el fixture no pintó el literal: el resto del test no probaría nada")
		}
		if cache := rec.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
			t.Errorf("la página que pinta el literal debía responder `no-store`, got %q", cache)
		}
		// El log de acceso SÍ escribió su línea —así que estamos mirando un log poblado— y el literal
		// no está en ella. El `path` es lo único que se escribe, y por eso la navegación entre
		// revisiones viaja como número y no como texto.
		out := logs.String()
		if !strings.Contains(out, "petición web completada") {
			t.Fatal("el log de acceso no registró la petición: el assert de abajo sería vacuo")
		}
		if strings.Contains(out, burgerLiteral) {
			t.Errorf("el literal del cliente NO puede aparecer en el log: %s", out)
		}
	})

	t.Run("cuando el re-análisis falla y se loguea el error", func(t *testing.T) {
		// Ésta es la rama por la que el texto podría escaparse de verdad: el mapper genérico escribe
		// `slog.Warn(..., "error", err)`, así que si el apiclient metiera el cuerpo dentro del error,
		// el material pegado por el dueño acabaría en el log sin que nadie lo viera venir.
		logs := captureLogs(t)
		const pegado = "transcripción del audio: son tres hamburguesas y una sin cebolla"
		api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
			burgerDetail(burgerRevision(1, "", burgerSourceLine, "")),
			func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"boom"}`)
			})
		defer api.Close()

		rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger/reanalyze",
			url.Values{intakeActionFieldReanalyzeText: {pegado}}, validSessionCookie(t))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("un 500 de la plataforma debía salir como 502, got %d", rec.Code)
		}
		// Lo tecleado volvió al formulario: había texto de verdad en juego.
		if !strings.Contains(rec.Body.String(), pegado) {
			t.Fatal("el material extra debía repintarse: sin él el assert del log sería vacuo")
		}
		out := logs.String()
		// La rama de log SE RECORRIÓ: el mapper escribió su advertencia.
		if !strings.Contains(out, "no se pudo ejecutar la acción sobre la solicitud") {
			t.Fatal("el fallo debía dejar su advertencia en el log: el assert de abajo sería vacuo")
		}
		if strings.Contains(out, pegado) {
			t.Errorf("el material que pegó el dueño NO puede acabar en el log: %s", out)
		}
		if strings.Contains(out, burgerLiteral) {
			t.Errorf("el literal del cliente NO puede acabar en el log: %s", out)
		}
	})
}

// TestIntakeCompareCreatedByEsUnRol: `created_by` es `system`/`owner`/`crm` y se pinta como ROL. La
// plataforma no publica quién tecleó, y esta consola no puede insinuar una persona (cero PII).
func TestIntakeCompareCreatedByEsUnRol(t *testing.T) {
	api := llmDetailAPI([]string{"cart_basic", "llm_intake"},
		burgerDetail(burgerRevision(1, "", burgerSourceLine, "")), nil)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-burger", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "la dejó el sistema (rol `system`)") {
		t.Error("`created_by: system` debía pintarse como un rol")
	}
	if !strings.Contains(out, "Es un rol, no una persona") {
		t.Error("la pantalla debía decir que es un rol y no quién tecleó")
	}
	// Un rol que esta consola no conozca se pinta TAL CUAL, misma doctrina que intakeStatusLabel.
	if got := intakeRevisionRoleText("integrador"); !strings.Contains(got, "integrador") {
		t.Errorf("un rol desconocido debía pintarse tal cual, got %q", got)
	}
}
