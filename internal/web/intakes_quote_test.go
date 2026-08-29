package web

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// Anclas del bloque de la sugerencia (Plan 047 · T2.4). Si el gate duro cierra, estas cadenas no
// están en el HTML: es lo que distingue un gate server-side de un `display:none`.
const (
	intakeQuoteMarker       = `id="section-intake-quote"`
	intakeQuoteOriginMarker = `id="section-intake-quote-origin"`
)

// quoteAPI levanta la API fake del generador: sirve el detalle de Ambar en cualquier GET, atiende el
// POST de la sugerencia con lo que diga el test, y ANOTA TODAS las escrituras que ve.
//
// Anotarlas es la mitad del criterio de esta tarea: lo que hay que poder demostrar no es solo que la
// sugerencia devuelve un texto, sino que NO TOCA NADA MÁS — ni aprueba, ni pregunta, ni mueve el
// estado, ni manda un mensaje. Con el fake devolviendo 500 a lo no mapeado eso se vería como un
// fallo; con la lista se ve como lo que es, una llamada que nunca debió salir.
func quoteAPI(t *testing.T, features []string, status int, body string, seen *[]string) *httptest.Server {
	t.Helper()
	return intakesAPI(features, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			*seen = append(*seen, r.Method+" "+r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/intakes/"):
			_, _ = io.WriteString(w, ambarDetail(false))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/intakes/in-ambar/quote-suggestion":
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

// assertNothingWasWritten es el aserto que sostiene «la sugerencia no aprueba ni envía nada»: la
// ÚNICA escritura que la plataforma puede haber visto es la de la propia sugerencia.
func assertNothingWasWritten(t *testing.T, seen []string) {
	t.Helper()
	for _, call := range seen {
		if call != "POST /api/v1/intakes/in-ambar/quote-suggestion" {
			t.Errorf("la sugerencia no puede escribir nada más, y llamó a %q", call)
		}
	}
	// Y se nombran las rutas prohibidas de una en una, porque un `!= …` pasaría igual el día que
	// alguien cambiara el id del fixture: esto tiene que fallar por lo que se llamó, no por cómo se
	// escribió la ruta.
	for _, forbidden := range []string{"/approve", "/request-info", "/status", "/api/v1/messages"} {
		for _, call := range seen {
			if strings.Contains(call, forbidden) {
				t.Errorf("la sugerencia llamó a %q, que es exactamente lo que no puede hacer", call)
			}
		}
	}
}

// TestQuoteSuggestionPreloadsTheApproveFieldAndSendsNothing es el criterio central de T2.4: alguien
// abre una solicitud, pide la sugerencia, y VE EL TEXTO CON SU ORIGEN en el campo de aprobar — sin
// que se haya aprobado ni enviado nada, y con la solicitud donde estaba.
func TestQuoteSuggestionPreloadsTheApproveFieldAndSendsNothing(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"Hola Ambar 💛 Torta de chocolate 45.00 — Total 65.50","source":"llm"}`, &seen)
	defer api.Close()

	// 🔄 GIRADO POR T3.5, NO REBAJADO: esta ruta pasó a POST-Redirect-GET, así que la pantalla que
	// este test siempre ha examinado ya no llega en la respuesta del POST sino en el GET siguiente.
	// Todo lo que afirmaba sigue afirmándose, sobre la página que ahora lo enseña.
	_, get := postQuoteAndFollow(t, NewRouter(authTestCfg(api.URL)), validSessionCookie(t))
	out := get.Body.String()

	// El texto está EN EL CAMPO editable de aprobar, no en un cartel de solo lectura: la dueña lo
	// ajusta antes de mandarlo.
	textarea := strings.Index(out, `name="rendered_text"`)
	suggested := strings.Index(out, "Hola Ambar 💛 Torta de chocolate 45.00 — Total 65.50")
	if textarea < 0 || suggested < textarea {
		t.Errorf("el texto sugerido debía quedar dentro del textarea de aprobar "+
			"(textarea=%d texto=%d)", textarea, suggested)
	}
	// Y su ORIGEN al lado: sin esto, «lo escribió el modelo» y «se lo compuso la plataforma» serían
	// la misma pantalla.
	if !strings.Contains(out, intakeQuoteOriginMarker) || !strings.Contains(out, "Origen: LLM") {
		t.Error("la pantalla debía decir que este texto lo redactó el modelo")
	}
	if !strings.Contains(out, "NO SE HA ENVIADO NADA") {
		t.Error("el aviso debía dejar claro que la sugerencia no envía")
	}
	// La solicitud no se movió de sitio.
	if !strings.Contains(out, "estado · por aprobar") {
		t.Error("la sugerencia no cambia el estado de la solicitud")
	}
	assertNothingWasWritten(t, seen)
}

// TestQuoteSuggestionDeterministicIsNotAnErrorOnScreen: con el modelo caído la plataforma responde
// 200 con el texto sobrio y su motivo. La pantalla NO puede pintar eso como un fallo — el texto se
// puede enviar igual—, y lo que cambia es la línea del origen.
func TestQuoteSuggestionDeterministicIsNotAnErrorOnScreen(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"Tu pedido:\n- 1 × Torta — 45.00\nTotal: 65.50","source":"deterministic",
		  "fallback_reason":"proveedor_no_disponible"}`, &seen)
	defer api.Close()

	// 🔄 GIRADO POR T3.5: el respaldo sobrio también viaja por el redirect. Que un 200 con
	// `deterministic` NO se pinte como error es lo mismo que este test siempre midió — y ahora,
	// además, comprueba que el MOTIVO sobrevive al viaje: sin él la pantalla diría que lo redactó el
	// modelo, que es justo la confusión que T2.4 vino a impedir.
	_, get := postQuoteAndFollow(t, NewRouter(authTestCfg(api.URL)), validSessionCookie(t))
	out := get.Body.String()

	if !strings.Contains(out, "Total: 65.50") {
		t.Error("el texto determinista debía quedar igualmente en el campo")
	}
	if !strings.Contains(out, "NO lo redactó el modelo") {
		t.Error("la pantalla debía decir que este texto no lo escribió el modelo")
	}
	if !strings.Contains(out, "el modelo no estaba disponible") {
		t.Error("el motivo del respaldo debía decirse en español, no con la clave cruda")
	}
	if strings.Contains(out, "proveedor_no_disponible") {
		t.Error("la clave cruda del motivo no se le enseña a la dueña")
	}
	// Un 200 con respaldo se pinta como ÉXITO y no como error: el aviso rojo diría que no hay
	// propuesta, y sí la hay.
	if !strings.Contains(out, "snackbar--success") {
		t.Error("el respaldo sobrio es una propuesta utilizable, no un fallo")
	}
	assertNothingWasWritten(t, seen)
}

// TestQuoteSuggestionSurfacesTheLinesWithoutPrice es el desenlace MÁS PROBABLE en campo —un borrador
// recién interpretado no tiene precios (el muro de PC-20)— y por eso se pinta como caso normal y
// accionable: qué líneas, por posición, y qué hacer. Es el MISMO cuerpo que devuelve `approve`, así
// que se dice con las mismas palabras.
func TestQuoteSuggestionSurfacesTheLinesWithoutPrice(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusBadRequest,
		`{"error":"lines_without_price","lines":[{"index":2,"label":"Torta vainilla"}]}`, &seen)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/quote-suggestion",
		url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"queda 1 línea sin precio", "línea 3", "Torta vainilla",
		"Ponles precio en el borrador", "No se ha enviado nada",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("el aviso debía decir qué arreglar: falta %q", want)
		}
	}
	// La página se sigue pudiendo operar: esto no es una pantalla de error.
	if !strings.Contains(out, intakeActionsMarker) || !strings.Contains(out, intakeQuoteMarker) {
		t.Error("tras el rechazo la pantalla sigue ofreciendo las acciones y el botón")
	}
	assertNothingWasWritten(t, seen)
}

// TestQuoteSuggestionWithoutLinesSaysWhatToDo cubre el OTRO 400 de la puerta. Se redacta con las
// palabras de la pantalla y no con las del upstream, que nombra una ruta de la API.
func TestQuoteSuggestionWithoutLinesSaysWhatToDo(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusBadRequest,
		`{"error":"la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador `+
			`con PUT /api/v1/intakes/{id}/items"}`, &seen)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/quote-suggestion",
		url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no tiene líneas que cotizar") {
		t.Error("el aviso debía decir qué falta")
	}
	if !strings.Contains(out, "Guarda primero las líneas del borrador de arriba") {
		t.Error("el aviso debía decir qué hacer, en términos de esta pantalla")
	}
	// El mensaje del upstream NO se vuelca: nombra una ruta de la API que a la dueña no le dice nada.
	if strings.Contains(out, "PUT /api/v1/intakes") {
		t.Error("el detalle técnico del upstream no se le enseña a la dueña")
	}
	assertNothingWasWritten(t, seen)
}

// TestQuoteSuggestionRespectsBothGates es el criterio de los gates, y son DOS distintos:
//
//   - `cart_basic` es el gate DURO: sin él la pantalla entera desaparece y el HTML no contiene el
//     botón por ninguna parte;
//   - `llm_intake` es el gate BLANDO: el botón se emite igual, DESHABILITADO y con su motivo a la
//     vista, y el POST a mano se corta en el handler sin gastar un viaje al cloud.
func TestQuoteSuggestionRespectsBothGates(t *testing.T) {
	t.Run("sin cart_basic el HTML no contiene el botón", func(t *testing.T) {
		var seen []string
		api := quoteAPI(t, []string{}, http.StatusOK, `{}`, &seen)
		defer api.Close()

		rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t))
		out := rec.Body.String()
		for _, absent := range []string{intakeQuoteMarker, "Sugerir la respuesta", "quote-suggestion"} {
			if strings.Contains(out, absent) {
				t.Errorf("sin `cart_basic` la pantalla entera desaparece, y el HTML trae %q", absent)
			}
		}
	})

	t.Run("sin llm_intake sale deshabilitado con su motivo", func(t *testing.T) {
		var seen []string
		api := quoteAPI(t, []string{"cart_basic"}, http.StatusOK, `{}`, &seen)
		defer api.Close()

		router := NewRouter(authTestCfg(api.URL))
		rec := getWithCookie(router, "/intakes/in-ambar", validSessionCookie(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("la solicitud se lee igual sin `llm_intake`: debía dar 200, got %d", rec.Code)
		}
		out := rec.Body.String()

		if !strings.Contains(out, intakeQuoteMarker) {
			t.Error("el botón no se esconde: se deshabilita con su motivo a la vista")
		}
		if !strings.Contains(out, `class="btn btn--outlined" disabled>Sugerir la respuesta<`) {
			t.Error("sin `llm_intake` el botón debía quedar deshabilitado")
		}
		for _, want := range []string{"llm_intake", "no incluye el análisis con IA", "ampliando el plan"} {
			if !strings.Contains(out, want) {
				t.Errorf("la razón del botón deshabilitado debía contener %q", want)
			}
		}
		// El resto de la pantalla sigue funcionando: lo que falta es la redacción automática, no la
		// bandeja.
		if !strings.Contains(out, intakeActionsMarker) || !strings.Contains(out, `name="rendered_text"`) {
			t.Error("sin `llm_intake` la solicitud se responde igual, escribiendo a mano")
		}

		// Y un POST a mano —que no tiene `disabled`— se corta AQUÍ, sin viaje al cloud.
		rec = postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion",
			url.Values{}, validSessionCookie(t))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("sin `llm_intake` el POST debía cortar con 403, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "no incluye el análisis con IA") {
			t.Error("el 403 propio debía decir qué capacidad falta")
		}
		for _, call := range seen {
			if strings.Contains(call, "quote-suggestion") {
				t.Errorf("sin la capacidad no se gasta un viaje al cloud, y se llamó a %q", call)
			}
		}
	})
}

// TestQuoteOriginIsOnlyPaintedWhenItWasAsked: abrir la solicitud NO pinta ningún origen. El campo de
// aprobar trae entonces la propuesta que arma esta consola con las líneas, y decir «lo redactó el
// modelo» sobre ella sería atribuirle al modelo un texto que no ha visto.
func TestQuoteOriginIsOnlyPaintedWhenItWasAsked(t *testing.T) {
	api := detailAPI(ambarDetail(false))
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar", validSessionCookie(t))
	out := rec.Body.String()
	if !strings.Contains(out, intakeQuoteMarker) {
		t.Fatal("el botón de sugerir debía estar en la página")
	}
	if strings.Contains(out, intakeQuoteOriginMarker) || strings.Contains(out, "Origen:") {
		t.Error("sin haber pedido la sugerencia no se atribuye a nadie el texto del campo")
	}
}

// TestQuoteFallbackReasonsAreAllTranslated enumera el vocabulario CERRADO de `fallback_reason` y
// exige que ninguno caiga en el genérico.
//
// 🔴 SON TRECE, no seis: cuatro los emite el generador (`quotetext/quotetext.go:116-129`) y NUEVE el
// verificador de precios (`quotetext/precios.go:145-171`), y los nueve viajan por este mismo campo.
// El día que el cloud añada el catorceavo, este test es lo que lo caza — porque un motivo sin
// traducir NO ROMPE NADA VISIBLE: se cuela como clave cruda en una pantalla que lee una persona que
// no programa.
func TestQuoteFallbackReasonsAreAllTranslated(t *testing.T) {
	reasons := []string{
		apiclient.QuoteFallbackNoExamples,
		apiclient.QuoteFallbackProviderDown,
		apiclient.QuoteFallbackLLMFailed,
		apiclient.QuoteFallbackBadOutput,
		apiclient.QuoteFallbackDraftWithoutAmounts,
		apiclient.QuoteFallbackUnreadableText,
		apiclient.QuoteFallbackUnreadableNumber,
		apiclient.QuoteFallbackTextWithoutAmounts,
		apiclient.QuoteFallbackMissingUnitPrice,
		apiclient.QuoteFallbackMissingTotal,
		apiclient.QuoteFallbackForeignAmount,
		apiclient.QuoteFallbackForeignNumber,
		apiclient.QuoteFallbackAmountsOutOfPlace,
	}
	if len(reasons) != 13 {
		t.Fatalf("el vocabulario de `fallback_reason` son 13 motivos, y aquí hay %d", len(reasons))
	}

	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		text := quoteFallbackText(reason)
		if strings.Contains(text, "sin traducir en esta consola") {
			t.Errorf("el motivo %q cae en el genérico: hay que traducirlo", reason)
		}
		if strings.Contains(text, reason) {
			t.Errorf("el motivo %q se le enseña a la dueña con su clave cruda", reason)
		}
		if !strings.HasPrefix(text, "Motivo: ") {
			t.Errorf("el motivo %q debía redactarse como un motivo, got %q", reason, text)
		}
		// Dos motivos distintos con el MISMO texto son un motivo que se perdió por el camino. Las
		// dos excepciones son deliberadas y van declaradas: la salida ilegible del modelo y el texto
		// ilegible son la misma historia para quien mira la pantalla.
		if seen[text] && reason != apiclient.QuoteFallbackUnreadableText {
			t.Errorf("el motivo %q repite la frase de otro: se perdió la distinción", reason)
		}
		seen[text] = true
	}

	// Y el desconocido se dice FEO a propósito: significa que falta traducir uno nuevo.
	if got := quoteFallbackText("motivo_del_futuro"); !strings.Contains(got, "motivo_del_futuro") {
		t.Errorf("un motivo desconocido se nombra tal cual, got %q", got)
	}
	if got := quoteFallbackText(""); strings.Contains(got, "``") {
		t.Errorf("un motivo vacío no se pinta como una clave vacía, got %q", got)
	}
}

// TestQuoteOriginNeverInventsAProvenance: un `source` que esta consola no conoce se pinta TAL CUAL
// —misma doctrina que `intakeViaText`—. Inventarle una procedencia a un texto que se le va a mandar
// a un cliente es exactamente lo que esta consola no hace.
func TestQuoteOriginNeverInventsAProvenance(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"llm", "Origen: LLM"},
		{"deterministic", "NO lo redactó el modelo"},
		{"vertex_ai_2027", "`vertex_ai_2027`"},
		{"", "no dijo quién redactó"},
	} {
		got := quoteOriginText(&apiclient.IntakeQuoteSuggestion{Source: tc.source})
		if !strings.Contains(got, tc.want) {
			t.Errorf("origen %q: debía contener %q, got %q", tc.source, tc.want, got)
		}
	}
}

// renderIntakeDetailTemplate ejecuta la plantilla del detalle DIRECTAMENTE, sin pasar por el
// handler.
//
// 🔑 Existe por un hallazgo de la verificación por mutación de T2.4: el aserto «sin `cart_basic` el
// HTML no trae el botón» sobrevivía a quitarle el gate a la plantilla, porque el corte lo estaba
// dando el OTRO cerrojo —`renderIntakeDetail` ni siquiera pone `View` sin esa capacidad—. Los dos
// candados son deliberados y van en serie; lo que no vale es un test que dice cubrir el segundo y
// solo ejercita el primero. Desde aquí la vista SÍ llega poblada, así que lo único que puede cerrar
// es el `{{ if .Entitlements.Has "cart_basic" }}` de la plantilla.
func renderIntakeDetailTemplate(t *testing.T, ent entitlementsView) string {
	t.Helper()
	// Los helpers salen de funcsDePlantilla, el MISMO sitio que usa el router: esta copia local del
	// FuncMap se quedó atrás al añadir `cuenta` y reventó el gate con «function "cuenta" not defined».
	// Sólo `yield` se stubea, que es lo único que legítimamente difiere aquí.
	tmpl, err := template.New("").
		Funcs(funcsDePlantilla(func(string, any) (template.HTML, error) { return "", nil })).
		ParseFS(templatesFS, "templates/layouts/*.html", "templates/pages/*.html")
	if err != nil {
		t.Fatalf("compilar plantillas: %v", err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "intake-detail.html", gin.H{
		"IntakeID":          "in-ambar",
		"CSRFToken":         "csrf",
		entitlementsDataKey: ent,
		"View": intakeDetailView{
			Detail:  &apiclient.IntakeDetail{Intake: apiclient.Intake{ID: "in-ambar", Status: intakeEditableStatus}},
			Actions: &intakeActionsView{Quote: intakeQuoteView{Enabled: true}},
		},
	})
	if err != nil {
		t.Fatalf("ejecutar intake-detail.html: %v", err)
	}
	return buf.String()
}

// TestQuoteButtonAlsoNeedsCartBasicInTheTemplate aísla el SEGUNDO cerrojo: con la vista poblada —lo
// que el handler nunca hace sin la capacidad—, la plantilla sigue sin emitir el botón. Sin este
// test, borrar el gate de la plantilla no rompería nada visible hasta que alguien reordenara el
// handler.
func TestQuoteButtonAlsoNeedsCartBasicInTheTemplate(t *testing.T) {
	// Primero, que el escenario NO sea vacuo: CON la capacidad el botón está.
	with := renderIntakeDetailTemplate(t, entitlementsView{
		Resolved: true, Features: []string{"cart_basic"},
		enabled: map[string]bool{"cart_basic": true},
	})
	if !strings.Contains(with, intakeQuoteMarker) || !strings.Contains(with, "quote-suggestion") {
		t.Fatal("con `cart_basic` y la vista poblada el botón TIENE que emitirse; si no, el aserto de " +
			"abajo no prueba nada")
	}

	// Y ahora, la misma vista sin la capacidad: la plantilla lo corta.
	without := renderIntakeDetailTemplate(t, entitlementsView{Resolved: true})
	for _, absent := range []string{intakeQuoteMarker, "Sugerir la respuesta", "quote-suggestion"} {
		if strings.Contains(without, absent) {
			t.Errorf("sin `cart_basic` la plantilla no puede emitir %q", absent)
		}
	}
}

// TestElAvisoDeEsperaDiceLaMagnitudDeVerdad fija la frase que le dice a la dueña cuánto va a esperar,
// y sobre todo la REGRESIÓN concreta que se acaba de corregir: decía «unos segundos» cuando lo medido
// contra UAT son 24,8-35,5 s y el tope de la página es de casi un minuto. Es el tipo de fallo que no
// rompe nada y solo se paga en la persona que se queda cuarenta segundos delante de una página en
// blanco que le prometió «unos segundos».
//
// El tope se comprueba CONTRA EL PLAZO CONFIGURADO y con dos valores distintos, que es lo que
// distingue «sale del plazo» de «hay un número escrito en la plantilla que hoy coincide».
func TestElAvisoDeEsperaDiceLaMagnitudDeVerdad(t *testing.T) {
	casos := []struct {
		nombre string
		plazo  time.Duration
		espera string
	}{
		{"el plazo de producción", 55 * time.Second, "55 segundos"},
		{"otro plazo cualquiera", 40 * time.Second, "40 segundos"},
		{"un plazo de más de un minuto", 90 * time.Second, "2 minutos"},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			var seen []string
			api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK, "{}", &seen)
			defer api.Close()

			cfg := authTestCfg(api.URL)
			cfg.QuoteSuggestionTimeout = tc.plazo
			out := getWithCookie(NewRouter(cfg), "/intakes/in-ambar", validSessionCookie(t)).Body.String()

			// PRIMERO el aserto positivo, porque sin él los dos de abajo son vacuos: si el bloque de la
			// sugerencia no se hubiera pintado, «no dice unos segundos» saldría verde sin haber mirado
			// ninguna frase.
			if !strings.Contains(out, intakeQuoteMarker) {
				t.Fatal("el bloque de la sugerencia no se pintó: los asertos del aviso no miran nada")
			}
			if !strings.Contains(out, "Esta página espera hasta "+tc.espera) {
				t.Errorf("el aviso debía decir el tope real de la espera (%q, que sale del plazo "+
					"configurado de %s) y no lo dice", tc.espera, tc.plazo)
			}
			// LA REGRESIÓN, nombrada: la frase que había antes describía una espera que no era la
			// que la dueña iba a tener.
			if strings.Contains(out, "unos segundos") {
				t.Error("el aviso volvió a decir «unos segundos»: lo medido son 24,8-35,5 s y el tope " +
					"de esta página es de casi un minuto, así que esa frase le miente a quien la lee")
			}
			// Y lo que la frase YA decía bien y no se puede perder al reescribirla.
			if !strings.Contains(out, "se queda cargando") || !strings.Contains(out, "no ha pasado nada") {
				t.Error("el aviso tiene que seguir diciendo que la página se queda cargando y que, " +
					"si no llega, no ha pasado nada y se puede volver a pulsar")
			}
		})
	}
}

// TestElAvisoDeEsperaNoInventaUnaMagnitudSinPlazo cubre el borde: con la Config armada a mano y sin
// plazo ninguno, el aviso no puede decir un número —no lo sabe—, y decir «0 segundos» sería peor que
// no decir nada.
func TestElAvisoDeEsperaNoInventaUnaMagnitudSinPlazo(t *testing.T) {
	if got := quoteWaitText(0); got != "que la plataforma conteste" {
		t.Errorf("sin plazo el aviso no debe inventar una magnitud; dijo %q", got)
	}
	if got := quoteWaitText(-3 * time.Second); got != "que la plataforma conteste" {
		t.Errorf("con un plazo negativo tampoco; dijo %q", got)
	}
}
