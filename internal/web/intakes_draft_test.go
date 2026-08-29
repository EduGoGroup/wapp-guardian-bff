package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// Anclas de los bloques del Plan 044 en el detalle. Si el gate cierra o el bloque no se emite, estas
// cadenas no están en el HTML: es lo que distingue un gate server-side de un `display:none`.
const (
	intakeDraftMarker   = `id="section-intake-draft"`
	intakeActionsMarker = `id="section-intake-actions"`
)

// ambarPayload es el borrador del caso Ambar (design §7.5) tal como lo congela la revisión
// `interpreted`: dos líneas del catálogo con precio, la torta que el catálogo NO supo casar sin
// precio, otra línea del catálogo y el envío por confirmar.
//
// ⚠️ Ningún precio del fixture formatea a «0.00» (45, 12, 8.5 y el total 65.50), y eso es
// deliberado: hay un test que comprueba que ese literal NO aparece en la página, y una línea a 0
// legítima lo haría salir verde sin probar nada.
const ambarPayload = `{
  "version": 1,
  "message_ts": "2026-07-13T09:55:00Z",
  "analysis": {"provider": "", "model": "qwen3:4b", "source": "event_thread", "reanalyzed_from": null},
  "delivery_date": "2026-07-22",
  "source_text": "quiero una torta de chocolate de 10 o 12 porciones y otra de vainilla",
  "media_refs": [{"ref": "wapp/media/opaca-1", "kind": "ptt", "label": "🎙️ audio del cliente — escúchalo"}],
  "lines": [
    {"kind": "matched", "sku": "TORTA-CHOC-12", "label": "Torta chocolate húmedo + crema choc.",
     "qty": 1, "unit_price": 45, "customization": "sin lactosa",
     "range": {"min": 10, "max": 12, "unit": "porciones"},
     "evidence": "de 10 o 12 porciones", "match": {"strategy": "fuzzy_osa", "confidence": 0.91}},
    {"kind": "matched", "sku": "DECO-INF", "label": "Decoración infantil", "qty": 1, "unit_price": 12},
    {"kind": "unmatched", "label": "Torta vainilla, lluvia de colores", "qty": 1, "unit_price": null,
     "evidence": "25-30 porciones"},
    {"kind": "matched", "sku": "TEQ-30", "label": "Tequeños congelados paquete x30", "qty": 1,
     "unit_price": 8.5, "unit_kind": "package", "package_size": 30},
    {"kind": "shipping", "label": "Envío", "qty": 1, "unit_price": null, "note": "por confirmar zona"}
  ],
  "suggested_questions": ["¿De cuántas porciones quieres la torta de vainilla?"]
}`

// ambarDetail arma el detalle de la solicitud de Ambar. `overdue` se pasa aparte porque hay un test
// que lo necesita en cada valor.
func ambarDetail(overdue bool) string {
	mark := "false"
	if overdue {
		mark = "true"
	}
	return `{"id":"in-ambar","contact_id":"ct-am","session_id":"s-1","status":"pending_approval",
	  "total":65.5,"customer_note":"dejarlo en portería","overdue":` + mark + `,
	  "items":[
	    {"sku":"TORTA-CHOC-12","label":"Torta chocolate húmedo","customization":"sin lactosa","qty":1,"unit_price":45},
	    {"sku":"DECO-INF","label":"Decoración infantil","customization":"","qty":1,"unit_price":12},
	    {"sku":"TEQ-30","label":"Tequeños congelados paquete x30","customization":"","qty":1,"unit_price":8.5}],
	  "revisions":[{"revision_no":1,"kind":"interpreted","created_by":"system",
	    "created_at":"2026-07-13T09:55:00Z","payload":` + ambarPayload + `}],
	  "allowed_transitions":["confirmed","needs_info"],
	  "created_at":"2026-07-13T09:55:00Z","updated_at":"2026-07-13T09:56:00Z"}`
}

// detailAPI levanta la API fake sirviendo `cart_basic` y el detalle dado en GET /api/v1/intakes/{id}.
func detailAPI(body string) *httptest.Server {
	return intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/intakes/") {
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
}

// TestIntakeDraftRendersAmbarWithoutFakingAPrice (T4.2, §7.5) es el render del caso Ambar: el
// borrador sale de la revisión interpretada, la línea que el catálogo NO supo casar pide «¿precio?»
// EDITABLE, y NO se imprime ningún `0.00` — un precio que todavía no existe no es cero, y decir que
// lo es le regala la torta al cliente.
func TestIntakeDraftRendersAmbarWithoutFakingAPrice(t *testing.T) {
	api := detailAPI(ambarDetail(false))
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el detalle debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{
		intakeDraftMarker,
		"Interpretación · revisión 1",
		// Ojo al escapado: `html/template` convierte el `+` de la etiqueta en `&#43;` dentro del
		// atributo, así que se busca por el trozo que no lo lleva.
		"crema choc.",
		// La línea sin match SÍ se pinta, y es la que `items` ni siquiera trae: sin este bloque no
		// se vería en ninguna pantalla.
		"Torta vainilla, lluvia de colores",
		"10-12 porciones",
		"package de 30",
		"Lo pidió así: «25-30 porciones»",
		"Entrega: 2026-07-22",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("el borrador debía contener %q", want)
		}
	}
	if !strings.Contains(out, "Torta vainilla") {
		t.Fatal("sin la línea sin match no hay nada más que comprobar")
	}
	// El «¿precio?» es EDITABLE: es el campo del formulario, vacío y con su rótulo. Sin JS, el
	// editable ES el input y el «después» es el re-render del servidor.
	if !strings.Contains(out, `value="" placeholder="¿precio?"`) {
		t.Error("la línea sin precio debía ofrecer el campo VACÍO con «¿precio?»")
	}
	if !strings.Contains(out, "¿precio?") {
		t.Error("«¿precio?» debía verse como texto, no solo como placeholder")
	}
	// El assert que da sentido a todo lo anterior: ningún «0.00» en la página. Ver el aviso del
	// fixture — ninguno de sus precios formatea así.
	if strings.Contains(out, "0.00") {
		t.Error("una línea sin precio NO puede salir como 0.00 (§7.5)")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (ADR-0035: server-side, cero framework)")
	}
	// NO-REGRESIÓN: la marca PROVISIONAL sigue estando (T4.2 la conserva, no la vuelve a poner).
	if !strings.Contains(out, "PROVISIONAL — migra a la consola de administración (Plan 047, ADR-0047)") {
		t.Error("la marca PROVISIONAL debía seguir en el detalle")
	}
}

// TestIntakeDraftPartialTotalCountsOnlyTheUnmatchedOnes fija la regla del conteo, que es donde el
// §7.5 se lee mal con facilidad: hay DOS líneas sin precio —la que no casó y el envío— y el total
// parcial dice UNA pendiente. La del envío no cuenta porque no depende del dueño sino de dónde vive
// el cliente, y la que espera presentación tampoco: su precio ya está en el catálogo.
func TestIntakeDraftPartialTotalCountsOnlyTheUnmatchedOnes(t *testing.T) {
	api := detailAPI(ambarDetail(false))
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()

	// El número lo manda la plataforma (INV-13): 45 + 12 + 8,50, o sea lo que SÍ tiene precio.
	if !strings.Contains(out, "Total parcial: 65.50 (1 línea pendiente de precio)") {
		t.Error("el total parcial debía decir su cifra Y cuántas líneas faltan")
	}
	if !strings.Contains(out, "por confirmar zona") {
		t.Error("el envío debía pintarse con su motivo")
	}
	if !strings.Contains(out, "tampoco cuenta como línea pendiente") {
		t.Error("hay que decir POR QUÉ el envío no entra en el conteo")
	}
}

// TestIntakeDraftCountsVariantsApart: una línea del catálogo con varias presentaciones viaja con
// `unit_price: null` porque falta ELEGIR, no porque falte precio. Contarla como pendiente de precio
// haría que la pantalla pidiera al dueño algo que ya está en su catálogo.
func TestIntakeDraftCountsVariantsApart(t *testing.T) {
	payload := `{"lines":[
	  {"kind":"matched","sku":"TORTA-CHOC","label":"Torta de chocolate","qty":1,"unit_price":null,
	   "variant_options":[{"sku":"TORTA-CHOC#V1","label":"12 porciones","price":45},
	                      {"sku":"TORTA-CHOC#V2","label":"25 porciones","price":78}]},
	  {"kind":"unmatched","label":"tequeños","qty":2,"unit_price":null}],
	 "suggested_questions":[]}`
	detail := `{"id":"in-v","status":"pending_approval","total":0,"items":[],
	  "revisions":[{"revision_no":1,"kind":"interpreted","payload":` + payload + `}],
	  "allowed_transitions":["confirmed"]}`
	api := detailAPI(detail)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-v", validSessionCookie(t)).Body.String()

	if !strings.Contains(out, "(1 línea pendiente de precio)") {
		t.Error("solo la línea sin match cuenta como pendiente de precio, no las tres sin precio")
	}
	if !strings.Contains(out, "1 línea espera a que elijas presentación") {
		t.Error("la línea con presentaciones debía contarse APARTE y decirlo")
	}
	if !strings.Contains(out, "falta elegir presentación") {
		t.Error("la celda de esa línea debía decir por qué no trae precio")
	}
	for _, want := range []string{"12 porciones", "25 porciones", "TORTA-CHOC#V2", "78.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("las presentaciones del catálogo debían enseñarse con su sku y su precio: falta %q", want)
		}
	}
}

// TestIntakeDetailOffersTheThreeActionsApartFromTheStatusSelect es el criterio literal de T4.2: las
// tres acciones existen, apuntan a `correct`/`approve`/`request-info`, y el desplegable de estado
// del 041 SIGUE FUNCIONANDO en su propia tarjeta. Son puertas distintas —éstas le escriben al
// cliente, aquél solo mueve la etiqueta— y la pantalla tiene que decirlo.
func TestIntakeDetailOffersTheThreeActionsApartFromTheStatusSelect(t *testing.T) {
	api := detailAPI(ambarDetail(false))
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()

	for _, want := range []string{
		intakeActionsMarker,
		`action="/intakes/in-ambar/approve"`,
		`action="/intakes/in-ambar/request-info"`,
		`action="/intakes/in-ambar/correct"`,
		">Aprobar y responder<",
		">Pedir más información<",
		">Corregir<",
		// El botón que manda el borrador vive fuera de su <form> y lo alcanza sin JS.
		`form="intake-draft-form"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("las tres acciones debían estar: falta %q", want)
		}
	}
	// El selector genérico del 041 sigue exactamente donde estaba.
	for _, want := range []string{
		`action="/intakes/in-ambar/status"`, `name="status"`, "Cambiar el estado", ">Aplicar<",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("el cambio de estado del 041 no se toca: falta %q", want)
		}
	}
	// Y se dice en voz alta que no son lo mismo, que es lo que impide confundirlos.
	if !strings.Contains(out, "no le escribe a nadie") {
		t.Error("la pantalla debía distinguir las acciones del desplegable de estado")
	}
	// La propuesta de respuesta se ofrece EDITABLE: el autor del texto es el dueño (D-044.19).
	if !strings.Contains(out, `name="rendered_text"`) || !strings.Contains(out, `name="question"`) {
		t.Error("las dos acciones que le hablan al cliente debían ofrecer su texto editable")
	}
	if !strings.Contains(out, "¿De cuántas porciones quieres la torta de vainilla?") {
		t.Error("la pregunta preparada debía proponerse en el formulario")
	}
	if !strings.Contains(out, "ninguna sale sola") {
		t.Error("hay que decir que la pregunta no se envía sin que una persona la mande (INV-1)")
	}
}

// TestIntakeDetailAudioIsNamedNeverLinked (D-044.52 §1): el audio se NOMBRA con su rótulo y NO se
// enlaza. La referencia del adjunto es opaca —nunca una URL— y no hay ruta de descarga en el API:
// un `<a href>` sobre ella llevaría a ninguna parte. La referencia tampoco sale a la página.
func TestIntakeDetailAudioIsNamedNeverLinked(t *testing.T) {
	api := detailAPI(ambarDetail(false))
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()

	if !strings.Contains(out, "🎙️ audio del cliente — escúchalo") {
		t.Error("el rótulo del audio debía pintarse")
	}
	if strings.Contains(out, "wapp/media/opaca-1") {
		t.Error("la referencia OPACA del adjunto no sale a la página: no es una URL y no lleva a ningún sitio")
	}
	if !strings.Contains(out, "no lo descarga") {
		t.Error("hay que decir que esta consola no descarga el audio, para que nadie lo busque")
	}
}

// TestIntakeDraftWithoutMediaSaysNothingAboutAudio: sin adjuntos NO se emite el rótulo. Hoy el
// pipeline no ancla media nunca, así que este es el caso corriente y un rótulo fijo mentiría en
// todas las solicitudes.
func TestIntakeDraftWithoutMediaSaysNothingAboutAudio(t *testing.T) {
	detail := `{"id":"in-s","status":"pending_approval","total":10,"items":[],
	  "revisions":[{"revision_no":1,"kind":"interpreted","payload":
	    {"lines":[{"kind":"matched","sku":"A","label":"Algo","qty":1,"unit_price":10}],
	     "suggested_questions":[]}}],
	  "allowed_transitions":["confirmed"]}`
	api := detailAPI(detail)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-s", validSessionCookie(t)).Body.String()
	if strings.Contains(out, "audio del cliente") {
		t.Error("sin adjuntos no se emite NADA sobre el audio")
	}
	if !strings.Contains(out, "Total parcial: 10.00 (ninguna línea pendiente de precio)") {
		t.Error("con todo a precio el total sigue diciendo el conteo, y el conteo es cero")
	}
}

// TestIntakeMarksOverdueWithoutCallingItExpired: `overdue` es un AVISO sobre una solicitud viva, y
// `expired` es un ESTADO terminal legado. Pintar el primero con el vocabulario del segundo le diría
// al dueño que la solicitud ya no sirve, que es lo contrario de lo que pasa.
func TestIntakeMarksOverdueWithoutCallingItExpired(t *testing.T) {
	t.Run("en el detalle", func(t *testing.T) {
		api := detailAPI(ambarDetail(true))
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()
		if !strings.Contains(out, "⏳ sin responder hace más de 24 h") {
			t.Error("la marca debía pintarse")
		}
		if !strings.Contains(out, "no cambia su estado") {
			t.Error("hay que decir que la marca no cambia nada: ni el estado ni lo que se puede hacer")
		}
		// El estado sigue siendo el suyo y se pinta con su nombre.
		if !strings.Contains(out, "estado · por aprobar") {
			t.Error("la solicitud sigue en «por aprobar»: `overdue` no la mueve")
		}
		if strings.Contains(out, "vencido") {
			t.Error("`overdue` NO es `expired`: no puede pintarse con la palabra del estado terminal")
		}
	})

	t.Run("en el listado", func(t *testing.T) {
		body := `{"intakes":[{"id":"in-1","contact_id":"ct-1","session_id":"s-1",
		  "status":"pending_approval","total":10,"overdue":true,
		  "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z"}],
		  "page":1,"page_size":50,"total":1}`
		api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes" {
				_, _ = io.WriteString(w, body)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		})
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes", validSessionCookie(t)).Body.String()
		// El desplegable de filtro lista TODOS los estados, «vencido (histórico)» incluido, así que
		// la comprobación se hace sobre las FILAS y no sobre la página entera.
		rows := out[strings.Index(out, "<tbody>"):]
		if !strings.Contains(rows, "⏳ sin responder hace más de 24 h") {
			t.Error("la marca debía pintarse en la fila")
		}
		if !strings.Contains(rows, "por aprobar") {
			t.Error("la fila sigue enseñando su estado real")
		}
		if strings.Contains(rows, "vencido") {
			t.Error("`overdue` NO es `expired`: la fila no puede llamarlo vencido")
		}
	})
}

// TestIntakesListAsksForTheOldestFirst (D-044.47 §2): la bandeja del dueño pide `sort=oldest`. Lo
// que lleva más tiempo esperando es lo que hay que atender, y con el default de la API —lo más
// reciente arriba— eso queda al final de la última página, que es donde nadie mira.
func TestIntakesListAsksForTheOldestFirst(t *testing.T) {
	var seen url.Values
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes" {
			seen = r.URL.Query()
			_, _ = io.WriteString(w, `{"intakes":[],"page":1,"page_size":50,"total":0}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes", validSessionCookie(t))
	if seen.Get("sort") != "oldest" {
		t.Errorf("la API debía recibir sort=oldest, got %q", seen.Get("sort"))
	}
}

// draftForm arma el envío del formulario del borrador con las cinco columnas emparejadas.
func draftForm(skus, labels, customs, qtys, prices []string) url.Values {
	form := url.Values{}
	for i := range skus {
		form.Add("item_sku", skus[i])
		form.Add("item_label", labels[i])
		form.Add("item_customization", customs[i])
		form.Add("item_qty", qtys[i])
		form.Add("item_price", prices[i])
	}
	return form
}

// TestDraftCorrectionTravelsAsACorrectionAndTheOldFormDoesNot es la cero-regresión del 041 vista
// desde la pantalla: el formulario del borrador manda `as_correction`, y el formulario de líneas de
// siempre manda EXACTAMENTE lo de siempre. Una puerta de la API, dos conductas, dos formularios.
func TestDraftCorrectionTravelsAsACorrectionAndTheOldFormDoesNot(t *testing.T) {
	var seen string
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/intakes/in-ambar/items" {
			raw, _ := io.ReadAll(r.Body)
			seen = string(raw)
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := draftForm(
		[]string{"TORTA-CHOC-12", "TORTA-VAIN-30"},
		[]string{"Torta chocolate", "Torta vainilla"},
		[]string{"sin lactosa", ""},
		[]string{"1", "1"},
		[]string{"45", "60"})

	rec := postFormWithCookie(router, "/intakes/in-ambar/correct", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la corrección debía responder 200, got %d", rec.Code)
	}
	if !strings.Contains(seen, `"as_correction":true`) {
		t.Errorf("la corrección del borrador debía viajar con la marca, got %s", seen)
	}
	// El precio que el dueño acaba de poner a la línea que no casó llega a la API.
	if !strings.Contains(seen, `"label":"Torta vainilla"`) || !strings.Contains(seen, `"unit_price":60`) {
		t.Errorf("la línea sin match debía llegar YA con su precio, got %s", seen)
	}

	seen = ""
	rec = postFormWithCookie(router, "/intakes/in-ambar/items", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la edición del 041 debía responder 200, got %d", rec.Code)
	}
	if strings.Contains(seen, "as_correction") {
		t.Errorf("el formulario del 041 NO puede empezar a mandar la clave nueva, got %s", seen)
	}
}

// TestDraftCorrectionRepaintsWhatWasTypedInItsOwnForm: un rechazo repinta lo tecleado EN EL
// FORMULARIO DEL BORRADOR, no en el del 041. Son dos formularios sobre la misma página, y volcar lo
// de uno en el otro pondría los precios del dueño en filas que no son las suyas.
func TestDraftCorrectionRepaintsWhatWasTypedInItsOwnForm(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_items","errors":[{"index":1,"field":"sku","message":"sku desconocido"}]}`)
	})
	defer api.Close()

	// El formulario manda las CUATRO filas editables del borrador (el envío no viaja): mandar
	// menos sería un envío viejo, y para eso está el otro test.
	form := draftForm(
		[]string{"TORTA-CHOC-12", "NO-EXISTE", "", "TEQ-30"},
		[]string{"Torta chocolate", "Decoración infantil", "Torta vainilla", "Tequeños"},
		[]string{"sin lactosa", "", "", ""},
		[]string{"1", "1", "1", "1"},
		[]string{"45", "12", "60", "8,50"})

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/correct", form, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el rechazo debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	// Lo tecleado sobrevive dentro del bloque del borrador, con el defecto en su línea.
	draft := out[strings.Index(out, intakeDraftMarker):]
	if !strings.Contains(draft, `value="NO-EXISTE"`) || !strings.Contains(draft, `value="60"`) {
		t.Error("lo tecleado en el borrador debía repintarse en el borrador")
	}
	if !strings.Contains(draft, "Línea 2 · SKU: sku desconocido") {
		t.Error("el defecto de la plataforma debía salir en su línea del borrador")
	}
	// Y lo tecleado NO se cuela en el formulario del 041, que edita otra cosa.
	old := out[strings.Index(out, "section-intake-items-edit"):]
	if strings.Contains(old, `value="NO-EXISTE"`) {
		t.Error("lo tecleado en el borrador no puede aparecer en el formulario de líneas del 041")
	}
}

// TestDraftIgnoresAFormThatDoesNotMatchTheDraft: si el formulario llega con otro número de filas
// —un envío viejo o manipulado— NO se mezcla nada. Emparejar precios con artículos ajenos es peor
// que perder lo tecleado, y este es el mismo criterio con el que `editRowsFromForm` rechaza cinco
// columnas descuadradas.
func TestDraftIgnoresAFormThatDoesNotMatchTheDraft(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_items","errors":[{"index":0,"field":"sku","message":"sku desconocido"}]}`)
	})
	defer api.Close()

	// Dos filas contra un borrador de cuatro: no cuadra.
	form := draftForm(
		[]string{"OTRA-COSA"}, []string{"Otra cosa"}, []string{""}, []string{"9"}, []string{"99"})

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/correct", form, validSessionCookie(t))
	out := rec.Body.String()
	draft := out[strings.Index(out, intakeDraftMarker):]
	if strings.Contains(draft, `value="99"`) || strings.Contains(draft, `value="OTRA-COSA"`) {
		t.Error("con las filas descuadradas no se mezcla lo tecleado con las líneas del borrador")
	}
	// Y el borrador se repinta con lo que dice la plataforma, no con un hueco.
	if !strings.Contains(draft, `value="45.00"`) {
		t.Error("sin poder emparejar, el borrador vuelve a lo que dice la plataforma")
	}
}

// TestApproveNeedsATextAndNeverSendsSilently: sin texto no se gasta el viaje y no se envía nada. El
// autor de lo que sale es el dueño, y aprobar con el cuerpo vacío sería mandar en su nombre algo que
// no ha leído.
func TestApproveNeedsATextAndNeverSendsSilently(t *testing.T) {
	called := false
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/approve",
		url.Values{"rendered_text": {"   "}}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("debía responder 400, got %d", rec.Code)
	}
	if called {
		t.Error("no se llama a la plataforma con un texto vacío")
	}
	if !strings.Contains(rec.Body.String(), "esta consola no manda una cotización que no hayas leído") {
		t.Error("el aviso debía decir por qué no se envió")
	}
}

// TestApproveSendsTheOwnersTextAndPromisesNoDelivery: el texto viaja tal cual, y el aviso de éxito
// dice «quedó registrado», nunca «el cliente lo recibió»: el envío cuelga de una sesión de WhatsApp
// que esta puerta no sabe si está viva.
func TestApproveSendsTheOwnersTextAndPromisesNoDelivery(t *testing.T) {
	var seen string
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/intakes/in-ambar/approve" {
			raw, _ := io.ReadAll(r.Body)
			seen = string(raw)
			_, _ = io.WriteString(w, `{"id":"in-ambar","status":"confirmed","total":65.5,"items":[],
			  "allowed_transitions":["deposit_requested"]}`)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/approve",
		url.Values{"rendered_text": {"Tu pedido: 1 torta — 45.00"}}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("debía responder 200, got %d", rec.Code)
	}
	if !strings.Contains(seen, `"rendered_text":"Tu pedido: 1 torta — 45.00"`) {
		t.Errorf("el texto del dueño debía viajar tal cual, got %s", seen)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no garantiza que el cliente ya la tenga delante") {
		t.Error("el aviso NO puede prometer la entrega: solo el registro y el intento de envío")
	}
	if !strings.Contains(out, "estado · confirmado") {
		t.Error("debía repintarse con el detalle que devolvió la aprobación")
	}
}

// TestApproveSurfacesTheLinesThatHaveNoPrice: el 400 de la plataforma se traduce a la lista de
// líneas que hay que arreglar, POR POSICIÓN y etiqueta —nunca por sku, que la línea que suele
// faltar no lo tiene—.
func TestApproveSurfacesTheLinesThatHaveNoPrice(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"lines_without_price","lines":[{"index":2,"label":"Torta vainilla"}]}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/approve",
		url.Values{"rendered_text": {"lo que sea"}}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"queda 1 línea sin precio", "línea 3", "Torta vainilla", "No se envió nada"} {
		if !strings.Contains(out, want) {
			t.Errorf("el aviso debía decir qué arreglar: falta %q", want)
		}
	}
}

// TestRequestInfoNeedsAQuestionAndSendsIt: la pregunta jamás sale sola (INV-1) y viaja editada por
// el dueño.
func TestRequestInfoNeedsAQuestionAndSendsIt(t *testing.T) {
	var seen string
	called := false
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/intakes/in-ambar/request-info" {
			called = true
			raw, _ := io.ReadAll(r.Body)
			seen = string(raw)
			_, _ = io.WriteString(w, `{"id":"in-ambar","status":"needs_info","total":65.5,"items":[],
			  "allowed_transitions":["pending_approval"]}`)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postFormWithCookie(router, "/intakes/in-ambar/request-info",
		url.Values{"question": {""}}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sin pregunta debía responder 400, got %d", rec.Code)
	}
	if called {
		t.Error("no se llama a la plataforma sin pregunta")
	}
	if !strings.Contains(rec.Body.String(), "no se envían solas") {
		t.Error("el aviso debía decir que las preguntas preparadas no salen solas")
	}

	rec = postFormWithCookie(router, "/intakes/in-ambar/request-info",
		url.Values{"question": {"¿de cuántas porciones la de vainilla?"}}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("debía responder 200, got %d", rec.Code)
	}
	if !strings.Contains(seen, `"question":"¿de cuántas porciones la de vainilla?"`) {
		t.Errorf("la pregunta editada debía viajar tal cual, got %s", seen)
	}
	if !strings.Contains(rec.Body.String(), "estado · falta info") {
		t.Error("debía repintarse con el detalle que devolvió la puerta")
	}
}

// TestIntakeWithoutInterpretationSaysSoInsteadOfPretending: una solicitud sin revisión interpretada
// —las del carrito numérico del 041— no tiene borrador, y la pantalla lo dice en vez de enseñar uno
// vacío o de ofrecer un «Corregir» que no manda nada.
func TestIntakeWithoutInterpretationSaysSoInsteadOfPretending(t *testing.T) {
	detail := `{"id":"in-041","status":"pending_approval","total":10,
	  "items":[{"sku":"A","label":"Algo","customization":"","qty":1,"unit_price":10}],
	  "revisions":[{"revision_no":1,"kind":"cart","payload":{}}],
	  "allowed_transitions":["confirmed"]}`
	api := detailAPI(detail)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-041", validSessionCookie(t)).Body.String()
	if strings.Contains(out, intakeDraftMarker) {
		t.Error("sin revisión interpretada no se emite el bloque del borrador")
	}
	if !strings.Contains(out, "no tiene ninguna revisión interpretada") {
		t.Error("hay que decir por qué no hay borrador")
	}
	if strings.Contains(out, `form="intake-draft-form"`) {
		t.Error("sin formulario del borrador no se ofrece el botón que lo manda: no haría nada")
	}
	// El formulario de líneas del 041 sigue ahí, que es lo que esta solicitud sí puede usar.
	if !strings.Contains(out, "section-intake-items-edit") {
		t.Error("el formulario de líneas del 041 sigue siendo el camino de esta solicitud")
	}
}

// TestDraftIsReadOnlyWhereItCannotBeSaved: en un estado que no admite corrección el borrador se
// SIGUE leyendo —leerlo no depende de poder tocarlo— pero no se ofrece ni el formulario ni las tres
// acciones: un botón que la plataforma va a rechazar no se ofrece.
func TestDraftIsReadOnlyWhereItCannotBeSaved(t *testing.T) {
	detail := strings.Replace(ambarDetail(false), `"status":"pending_approval"`, `"status":"confirmed"`, 1)
	api := detailAPI(detail)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, intakeDraftMarker) || !strings.Contains(out, "Torta vainilla, lluvia de colores") {
		t.Error("el borrador se sigue leyendo aunque no se pueda corregir")
	}
	if strings.Contains(out, intakeActionsMarker) {
		t.Error("desde un estado que no las admite NO se ofrecen las tres acciones")
	}
	if strings.Contains(out, `id="intake-draft-form"`) {
		t.Error("sin corrección posible no se emite el formulario del borrador")
	}
	// Y aun de solo lectura, la línea sin precio sigue sin inventarse un cero.
	if strings.Contains(out, "0.00") {
		t.Error("tampoco en solo lectura se imprime 0.00 por una línea sin precio")
	}
}

// TestDraftWarnsWhenTheInterpretationIsNoLongerTheLatest: la revisión `interpreted` se congela y NO
// se reescribe cuando el dueño corrige. Con correcciones encima, este bloque enseña la lectura
// original — y callarlo dejaría al dueño creyendo que son los precios vigentes.
func TestDraftWarnsWhenTheInterpretationIsNoLongerTheLatest(t *testing.T) {
	detail := strings.Replace(ambarDetail(false),
		`"created_at":"2026-07-13T09:55:00Z","payload":`,
		`"created_at":"2026-07-13T09:55:00Z","payload":`, 1)
	detail = strings.Replace(detail, `"allowed_transitions"`,
		`"__pad":0,"allowed_transitions"`, 1)
	// Se añade una revisión `corrected` POSTERIOR a la interpretada.
	detail = strings.Replace(detail, `}],
	  "__pad":0`, `},{"revision_no":2,"kind":"corrected","payload":{}}],
	  "__pad":0`, 1)

	api := detailAPI(detail)
	defer api.Close()

	out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "Después de esta interpretación hay 1 revisión más") {
		t.Error("con revisiones posteriores hay que avisar de que este bloque no es lo vigente")
	}
	// 🔴 El aserto de arriba decía «1 revisiones» y CONGELABA el defecto: el aviso se escribió sin
	// concordancia de plural y el test lo fijó tal cual. Este hermano impide que vuelva, y mira la
	// forma equivocada en vez de la correcta —que ya está arriba— porque es la que reaparece si
	// alguien devuelve `{{ .Newer }}` crudo a la plantilla.
	if strings.Contains(out, "1 revisiones") {
		t.Error("con n=1 el aviso tiene que decir «1 revisión», no «1 revisiones»")
	}
}

// detailWithMedia arma un detalle mínimo cuyo borrador lleva EXACTAMENTE los adjuntos dados.
//
// La referencia del fixture se llama `wapp/media/NO-DEBE-SALIR` a propósito: es opaca —ni una URL ni
// nada navegable— y ningún camino de la pantalla puede escupirla. Con ese nombre, un assert negativo
// que falle dice por sí solo qué se rompió.
func detailWithMedia(mediaJSON string) string {
	return `{"id":"in-m","status":"pending_approval","total":10,
	  "items":[{"sku":"A","label":"Algo","customization":"","qty":1,"unit_price":10}],
	  "revisions":[{"revision_no":1,"kind":"interpreted","payload":{
	    "lines":[{"kind":"matched","sku":"A","label":"Algo","qty":1,"unit_price":10}],
	    "media_refs":[` + mediaJSON + `],
	    "suggested_questions":[]}}],
	  "allowed_transitions":["confirmed"]}`
}

// TestMediaTextNeverLeaksTheOpaqueRef vigila la regla ESTRUCTURAL de `mediaText`: diga lo que diga,
// nunca devuelve la referencia del adjunto.
//
// 🔴 Va como test propio y recorriendo LAS SIETE ramas porque el assert de la página no bastaba: los
// audios reales traen `label`, así que la primera rama gana siempre y el `switch` de respaldo no lo
// ejecutaba NINGÚN test. Una `ref` que solo puede filtrarse por un camino que nadie recorre está
// protegida por un assert que no mira — que es exactamente el hueco que esta tanda ya pagó dos veces.
func TestMediaTextNeverLeaksTheOpaqueRef(t *testing.T) {
	const ref = "wapp/media/NO-DEBE-SALIR"
	cases := map[string]struct {
		kind  string
		label string
		want  string
	}{
		"la etiqueta que manda la plataforma gana": {
			kind: "ptt", label: "🎙️ audio del cliente — escúchalo",
			want: "🎙️ audio del cliente — escúchalo",
		},
		// Las cuatro de respaldo: solo se alcanzan con la etiqueta VACÍA.
		"audio sin etiqueta":       {kind: "audio", want: intakeAudioLabel},
		"nota de voz sin etiqueta": {kind: "ptt", want: intakeAudioLabel},
		"alias voice sin etiqueta": {kind: "voice", want: intakeAudioLabel},
		"imagen":                   {kind: "image", want: "🖼️ imagen del cliente"},
		"vídeo":                    {kind: "video", want: "🎬 vídeo del cliente"},
		"documento":                {kind: "document", want: "📄 documento del cliente"},
		// Una clase que este cliente no conozca se NOMBRA por su clave, misma doctrina que
		// intakeStatusLabel: antes una clave cruda que callar un adjunto que existe.
		"clase desconocida": {kind: "sticker", want: "adjunto del cliente (sticker)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := mediaText(apiclient.IntakeMediaRef{Ref: ref, Kind: tc.kind, Label: tc.label})
			if got != tc.want {
				t.Errorf("mediaText = %q, quiero %q", got, tc.want)
			}
			if strings.Contains(got, ref) {
				t.Errorf("la referencia OPACA no puede salir por ninguna rama, got %q", got)
			}
		})
	}
}

// TestIntakeDraftNeverCallsAnImageAnAudio cierra la otra mitad: `HasAudio` decide una frase que se le
// dice al dueño («el audio se escucha en la conversación de WhatsApp»), y con una FOTO esa frase es
// falsa. La regla estaba escrita en el comentario de `draftMediaOf` y no la miraba ningún test.
func TestIntakeDraftNeverCallsAnImageAnAudio(t *testing.T) {
	const ref = "wapp/media/NO-DEBE-SALIR"
	const audioNote = "El audio se escucha en la conversación de WhatsApp"

	t.Run("una imagen NO es un audio", func(t *testing.T) {
		api := detailAPI(detailWithMedia(`{"ref":"` + ref + `","kind":"image"}`))
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-m", validSessionCookie(t)).Body.String()
		if !strings.Contains(out, "🖼️ imagen del cliente") {
			t.Error("la imagen debía nombrarse con su propio rótulo de respaldo")
		}
		if strings.Contains(out, audioNote) {
			t.Error("con una imagen NO se le puede decir al dueño que hay un audio que escuchar")
		}
		if strings.Contains(out, intakeAudioLabel) {
			t.Error("el rótulo del audio no puede salir por un adjunto que no se escucha")
		}
		if strings.Contains(out, ref) {
			t.Error("la referencia OPACA no sale a la página")
		}
	})

	t.Run("una nota de voz sin etiqueta SÍ lo es", func(t *testing.T) {
		api := detailAPI(detailWithMedia(`{"ref":"` + ref + `","kind":"ptt"}`))
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-m", validSessionCookie(t)).Body.String()
		if !strings.Contains(out, intakeAudioLabel) {
			t.Error("un audio sin etiqueta debía caer en el rótulo de respaldo de la plataforma")
		}
		if !strings.Contains(out, audioNote) {
			t.Error("con un audio SÍ hay que decir dónde se escucha")
		}
		if strings.Contains(out, ref) {
			t.Error("la referencia OPACA no sale a la página ni siquiera por el camino del respaldo")
		}
	})

	t.Run("una imagen y un audio a la vez", func(t *testing.T) {
		api := detailAPI(detailWithMedia(
			`{"ref":"` + ref + `","kind":"image"},{"ref":"` + ref + `-2","kind":"voice"}`))
		defer api.Close()

		out := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-m", validSessionCookie(t)).Body.String()
		// Los dos se nombran, y la frase del audio sale UNA vez porque hay uno de verdad.
		if !strings.Contains(out, "🖼️ imagen del cliente") || !strings.Contains(out, intakeAudioLabel) {
			t.Error("los dos adjuntos debían nombrarse, cada uno como lo que es")
		}
		if !strings.Contains(out, audioNote) {
			t.Error("con un audio presente la frase debía salir")
		}
		if strings.Contains(out, ref) {
			t.Error("ninguna referencia OPACA sale a la página")
		}
	})
}
