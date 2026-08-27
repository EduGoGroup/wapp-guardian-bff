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

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// discardPath es la ruta de la API pública que descarta por lotes (cloud `2527c17`). Va como
// constante para que un test que la escriba mal no pase por casualidad.
const discardPath = "/api/v1/intakes/discard"

// overLimitBatch es uno más que el tope del contrato: el primer lote que la plataforma rechaza con
// un 400. Se escribe a partir de la constante del apiclient para que suba con ella.
const overLimitBatch = apiclient.MaxIntakeDiscardBatch + 1

// discardListBody son tres solicitudes de la bandeja: una abierta, una confirmada y una vencida.
// La mezcla es deliberada — el lote mixto es el caso normal de esta pantalla.
const discardListBody = `{"intakes":[
 {"id":"in-open","contact_id":"ct-1","session_id":"s-1","status":"open","total":12.5,
  "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z"},
 {"id":"in-conf","contact_id":"ct-2","session_id":"s-1","status":"confirmed","total":40,
  "created_at":"2026-08-04T10:00:00Z","updated_at":"2026-08-04T10:00:00Z"},
 {"id":"in-old","contact_id":"ct-3","session_id":"s-2","status":"expired","total":7,
  "created_at":"2026-07-04T10:00:00Z","updated_at":"2026-07-04T10:00:00Z"}],
 "page":1,"page_size":50,"total":3}`

// discardAPI levanta la API fake con `cart_basic`: sirve el listado y delega el POST del descarte en
// `discard`. Un `discard` nil hace fallar el test si alguien llama a la puerta que ESCRIBE — que es
// como se comprueba que mirar no escribe.
func discardAPI(t *testing.T, list string, discard http.HandlerFunc) *httptest.Server {
	t.Helper()
	return intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes":
			_, _ = io.WriteString(w, list)
		case r.Method == http.MethodPost && r.URL.Path == discardPath:
			if discard == nil {
				t.Errorf("no debía llamarse a %s: esta prueba no llega a descartar", discardPath)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			discard(w, r)
		default:
			t.Errorf("ruta no esperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

// El texto de la confirmación, FIJADO POR D-044.47 §3 (Plan 044 · T4.8). Va como constante y no
// suelto en cada test por lo que ese punto de la decisión dice con todas las letras: es una decisión
// de producto y no un ajuste de copy, así que si alguien la cambia tiene que venir aquí, verla
// escrita entera y decidir a sabiendas — no ir tocando `Contains` sueltos hasta que la suite calle.
//
// La redacción dice las DOS cosas que los textos en liza se contradecían: se marcan como
// `abandoned` —no se borra nada— y aun así no hay vuelta atrás. El singular es concordancia de la
// plantilla; la decisión redacta el caso de N.
const (
	avisoDescartePlural   = "Vas a descartar 2 solicitudes. Quedarán marcadas como <strong>abandonadas</strong> —no se borra nada— pero <strong>esto no se puede deshacer</strong>."
	avisoDescarteSingular = "Vas a descartar 1 solicitud. Quedará marcada como <strong>abandonada</strong> —no se borra nada— pero <strong>esto no se puede deshacer</strong>."
	// avisoDescarteRetirado es el literal que este texto SUSTITUYE (D-041.22). Se comprueba que ya
	// no está: sugería borrado —«no hay papelera»— y convivir con el nuevo dejaría la tarjeta
	// diciendo dos cosas distintas sobre lo mismo.
	avisoDescarteRetirado = "no hay papelera"
)

// discardForm arma el formulario con los ids marcados y, si `action` no está vacío, el botón pulsado.
func discardForm(action string, ids ...string) url.Values {
	form := url.Values{}
	for _, id := range ids {
		form.Add(intakeDiscardFieldID, id)
	}
	if action != "" {
		form.Set(intakeDiscardFieldAction, action)
	}
	return form
}

// TestIntakesListOffersDiscardSelection (T4.8): la bandeja ofrece marcar solicitudes y un botón que
// lleva a REVISAR. Lo que no ofrece es descartar de un clic desde el listado.
func TestIntakesListOffersDiscardSelection(t *testing.T) {
	api := discardAPI(t, discardListBody, nil)
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes?status=open", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el listado debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, id := range []string{"in-open", "in-conf", "in-old"} {
		if !strings.Contains(out, `<input type="checkbox" name="intake_id" value="`+id+`"`) {
			t.Errorf("la fila %q debía ofrecer su casilla de descarte", id)
		}
	}
	// El formulario del listado arrastra los filtros vigentes: descartar no puede devolver al
	// operador a una bandeja distinta de la que estaba mirando.
	if !strings.Contains(out, `action="/intakes/discard?page=1&amp;status=open"`) {
		t.Error("el formulario debía apuntar a /intakes/discard conservando los filtros")
	}
	if !strings.Contains(out, `name="action" value="review"`) {
		t.Error("el botón del listado debía ser el de REVISAR, no el que descarta")
	}
	if strings.Contains(out, `value="discard"`) {
		t.Error("el listado NO puede ofrecer el botón que escribe: eso vive tras la confirmación")
	}
	// Confirmación server-side: ni JS ni diálogos del navegador (ADR-0035).
	for _, forbidden := range []string{"<script", "confirm(", "alert(", "onclick"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("la pantalla no debe emitir %q: la confirmación es server-side", forbidden)
		}
	}
}

// TestDiscardReviewShowsTheBatchWithoutWriting (el corazón de D-041.22): el paso de revisar enseña
// QUÉ se va a descartar —con su estado y su total— y NO llama a la API. El botón que escribe solo
// aparece aquí, después de esa lista.
func TestDiscardReviewShowsTheBatchWithoutWriting(t *testing.T) {
	api := discardAPI(t, discardListBody, nil) // nil ⇒ el test falla si se llama a la puerta que escribe
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard?status=open",
		discardForm("review", "in-open", "in-old"), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisar el lote debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, `id="section-intakes-discard-confirm"`) {
		t.Fatal("debía pintarse la tarjeta de confirmación")
	}
	if !strings.Contains(out, avisoDescartePlural) {
		t.Errorf("la confirmación debía llevar el texto de D-044.47 §3, palabra por palabra:\n%s", avisoDescartePlural)
	}
	if !strings.Contains(out, "al cliente NO se le avisa") {
		t.Error("la confirmación debía decir que el cliente no recibe ningún aviso")
	}
	// El dueño ve lo que va a descartar, no una cuenta: id, contacto, estado en español y total.
	for _, want := range []string{"in-open", "in-old", "ct-1", "ct-3", "abierto", "vencido (histórico)", "12.50", "7.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("la confirmación debía enseñar %q de lo seleccionado", want)
		}
	}
	// Lo NO seleccionado no viaja en el lote (sale en el listado de abajo, pero no como oculto).
	if strings.Contains(out, `<input type="hidden" name="intake_id" value="in-conf">`) {
		t.Error("una solicitud que no se marcó no puede colarse en el lote a confirmar")
	}
	for _, id := range []string{"in-open", "in-old"} {
		if !strings.Contains(out, `<input type="hidden" name="intake_id" value="`+id+`">`) {
			t.Errorf("el lote debía re-enviarse entero: falta el oculto de %q", id)
		}
	}
	if !strings.Contains(out, `name="action" value="discard"`) {
		t.Error("la confirmación debía ofrecer el botón que descarta")
	}
	if !strings.Contains(out, `name="csrf_token"`) {
		t.Error("el formulario que escribe debía llevar su token CSRF, como el resto de la consola")
	}
	if !strings.Contains(out, `href="/intakes?page=1&amp;status=open"`) {
		t.Error("debía ofrecerse volver a la bandeja sin descartar")
	}
}

// TestDiscardConfirmationNoDescartaConUnEnter es REQ-32f(a), y nace de un defecto REAL (A1 del
// cierre del Plan 041): «Descartar definitivamente» era el primer —y único— elemento enfocable del
// formulario, y «Cancelar» iba después, así que quien llegaba por teclado y pulsaba Enter descartaba
// de forma irreversible. Los `hidden` no son enfocables, de modo que el tabulador caía directo en el
// botón que escribe.
//
// Las tres cosas que verifica son las tres mitades del defecto: que el conteo exacto está en el
// texto, que la salida segura va ANTES en el DOM (orden del tabulador) y que tiene el foco inicial.
func TestDiscardConfirmationNoDescartaConUnEnter(t *testing.T) {
	api := discardAPI(t, discardListBody, nil) // nil ⇒ el test falla si se llama a la puerta que escribe
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard?status=open",
		discardForm("review", "in-open", "in-old"), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisar el lote debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	// (a) El conteo exacto, no un «las solicitudes de abajo».
	if !strings.Contains(out, "Vas a descartar 2 solicitudes") {
		t.Error("el modal debía decir CUÁNTAS solicitudes se van a descartar (REQ-32f(a))")
	}

	// (b) Cancelar va antes que el botón que escribe: es el primer parada del tabulador.
	cancel := strings.Index(out, `<a href="/intakes?page=1&amp;status=open" class="btn btn--text" autofocus>`)
	discard := strings.Index(out, `name="action" value="discard"`)
	if cancel < 0 {
		t.Fatal("no se encontró el enlace de cancelar con autofocus")
	}
	if discard < 0 {
		t.Fatal("no se encontró el botón que descarta")
	}
	if cancel > discard {
		t.Error("la salida segura debe ir ANTES que la acción destructiva: si el tabulador llega " +
			"primero al botón que escribe, un Enter descarta sin vuelta atrás")
	}

	// (c) El foco inicial está en la salida segura, no en la destructiva.
	if !strings.Contains(out, `class="btn btn--text" autofocus>Cancelar`) {
		t.Error("«Cancelar» debía llevar autofocus: el foco inicial no puede estar en lo que borra")
	}
	if strings.Contains(out, `value="discard" autofocus`) || strings.Contains(out, `autofocus class="btn btn--filled"`) {
		t.Error("el botón que descarta NUNCA puede llevar el foco inicial")
	}
}

// TestDiscardConfirmationCuentaUnaSolaEnSingular acompaña al anterior: el conteo exacto se lee
// también cuando el lote es de una, y ahí «2 solicitudes» en plural sonaría a error de la consola.
func TestDiscardConfirmationCuentaUnaSolaEnSingular(t *testing.T) {
	api := discardAPI(t, discardListBody, nil)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard?status=open",
		discardForm("review", "in-open"), validSessionCookie(t))
	out := rec.Body.String()

	if !strings.Contains(out, avisoDescarteSingular) {
		t.Errorf("con una sola solicitud el modal debía concordar entero, no solo el conteo:\n%s", avisoDescarteSingular)
	}
	if strings.Contains(out, "Vas a descartar 1 solicitudes") {
		t.Error("el plural mal concordado hace dudar de si la consola sabe lo que va a borrar")
	}
	// La concordancia va hasta el final de la frase: «1 solicitud … Quedarán marcadas como
	// abandonadas» sería el mismo defecto una oración más allá, donde se deja de mirar.
	if strings.Contains(out, "Quedarán marcadas como <strong>abandonadas</strong>") {
		t.Error("con una sola solicitud el resto de la frase también tiene que ir en singular")
	}
}

// TestDiscardConfirmationYaNoDiceQueSeBorra es la otra mitad de D-044.47 §3: el literal que este
// texto SUSTITUYE tiene que haberse ido.
//
// No basta con que el nuevo esté. El viejo —«Esto NO se puede deshacer y no hay papelera»— decía
// algo FALSO: no se borra nada, las solicitudes quedan en `abandoned`, con sus líneas y su
// historial. Dejarlo convivir con el nuevo pondría en la misma tarjeta las dos versiones de lo que
// pasa, y la que asusta es la que se lee.
func TestDiscardConfirmationYaNoDiceQueSeBorra(t *testing.T) {
	api := discardAPI(t, discardListBody, nil)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("review", "in-open", "in-old"), validSessionCookie(t))
	out := rec.Body.String()

	if strings.Contains(out, avisoDescarteRetirado) {
		t.Errorf("el literal retirado (%q) sigue en la pantalla: sugiere borrado y contradice al nuevo",
			avisoDescarteRetirado)
	}
}

// TestDiscardMixedBatchTellsWhatHappenedToEach (criterio (a) del plan, adaptado por la enmienda):
// un lote mixto se cuenta SOLICITUD POR SOLICITUD. Ni un «listo» global ni jerga del contrato.
func TestDiscardMixedBatchTellsWhatHappenedToEach(t *testing.T) {
	api := discardAPI(t, discardListBody, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"discarded":["in-old"],"skipped":[
		 {"intake_id":"in-open","reason":"live_event"},
		 {"intake_id":"in-conf","reason":"not_open"},
		 {"intake_id":"in-ghost","reason":"not_found"},
		 {"intake_id":"in-done","reason":"already_discarded"}]}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-old", "in-open", "in-conf", "in-ghost", "in-done"), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un lote mixto debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, `id="section-intakes-discard-result"`) {
		t.Fatal("debía pintarse el desglose del lote")
	}
	// El aviso de cabecera dice las dos cifras y NO se pinta en verde: un lote a medias no es un
	// «listo», y el verde es lo único que mucha gente lee.
	if !strings.Contains(out, "Se descartó 1 de 5 solicitudes") {
		t.Error("el aviso debía decir cuántas cayeron de cuántas")
	}
	if !strings.Contains(out, "Las otras 4 siguen en tu bandeja") {
		t.Error("el aviso debía decir cuántas siguen ahí")
	}
	if strings.Contains(out, "snackbar--success") {
		t.Error("un lote mixto no puede anunciarse como un éxito limpio")
	}
	// Cada una de las cuatro que no cayeron aparece con su motivo en la voz del dueño.
	for _, id := range []string{"in-open", "in-conf", "in-ghost", "in-done"} {
		if !strings.Contains(out, id) {
			t.Errorf("la solicitud %q debía aparecer en el desglose", id)
		}
	}
	for _, reason := range []string{
		"El cliente sigue en plena conversación con este pedido",
		"Ya no está abierta, y desde donde está no se descarta",
		"No está en tu bandeja: o no existe o ya no es de este negocio",
		"Ya estaba descartada",
	} {
		if !strings.Contains(out, reason) {
			t.Errorf("el desglose debía explicar %q", reason)
		}
	}
	// Y la que sí cayó se nombra: «se descartó algo» sin decir qué no es un resultado.
	if !strings.Contains(out, "Descartadas (1 de 5)") {
		t.Error("el desglose debía encabezar lo descartado con su cuenta")
	}
	// Jerga del contrato fuera de la pantalla: `live_event` no le dice nada a un dueño de negocio.
	for _, jargon := range []string{"live_event", "not_open", "not_found", "already_discarded"} {
		if strings.Contains(out, jargon) {
			t.Errorf("la clave %q del contrato no puede llegar a la pantalla sin traducir", jargon)
		}
	}
}

// TestDiscardCleanBatchIsAnnouncedAsSuccess: cuando NO se salta ninguna, y solo entonces, el aviso
// es de éxito — y sigue diciendo que no hay vuelta atrás.
func TestDiscardCleanBatchIsAnnouncedAsSuccess(t *testing.T) {
	api := discardAPI(t, discardListBody, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"discarded":["in-old","in-open"],"skipped":[]}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-old", "in-open"), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el lote limpio debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, "snackbar--success") {
		t.Error("un lote sin saltos sí es un éxito y debía anunciarse como tal")
	}
	if !strings.Contains(out, "Descartadas 2 solicitudes") {
		t.Error("el aviso debía decir cuántas se descartaron")
	}
	if !strings.Contains(out, "no se puede deshacer") {
		t.Error("el aviso debía recordar que el descarte no tiene vuelta atrás")
	}
	if !strings.Contains(out, "Descartadas (2 de 2)") {
		t.Error("el desglose debía nombrarlas una por una, también en el caso limpio")
	}
}

// TestDiscardNothingDiscardedIsNotSilent: un lote en el que no cayó ninguna se dice, y se dice por
// qué. Es el caso que un «listo» global convertiría en una mentira completa.
func TestDiscardNothingDiscardedIsNotSilent(t *testing.T) {
	api := discardAPI(t, discardListBody, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"discarded":[],"skipped":[{"intake_id":"in-conf","reason":"not_open"}]}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-conf"), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if strings.Contains(out, "snackbar--success") {
		t.Error("no descartar nada no puede pintarse de verde")
	}
	if !strings.Contains(out, "No se descartó la solicitud que mandaste") {
		t.Error("el aviso debía decir que no cayó ninguna")
	}
	// El plural tiene que salir bien también en el otro extremo: nada de «de las 1 solicitud».
	if strings.Contains(out, "las 1 solicitud") {
		t.Error("el aviso no puede leerse como una plantilla mal rellenada")
	}
	if !strings.Contains(out, "Ya no está abierta") {
		t.Error("debía explicarse el motivo, no solo el hecho")
	}
}

// TestDiscardSendsExactBatch: lo que viaja a la plataforma es EXACTAMENTE lo marcado, sin repetidos
// y en el orden de la bandeja, con el token de sesión en la cabecera.
func TestDiscardSendsExactBatch(t *testing.T) {
	var body struct {
		IntakeIDs []string `json:"intake_ids"`
	}
	var authorization string
	api := discardAPI(t, discardListBody, func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("el cuerpo debía ser JSON legible: %v", err)
		}
		_, _ = io.WriteString(w, `{"discarded":["in-old","in-open"],"skipped":[]}`)
	})
	defer api.Close()

	// El mismo id dos veces: si viajara repetido, la plataforma contestaría `already_discarded`
	// por obra del primero y la respuesta tendría el mismo id en las dos listas.
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-old", "in-open", "in-old", "  "), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("debía renderizar 200, got %d", rec.Code)
	}

	if got := strings.Join(body.IntakeIDs, ","); got != "in-old,in-open" {
		t.Errorf("el lote enviado = %q, quiero \"in-old,in-open\" (sin repetidos, sin vacíos, en orden)", got)
	}
	if !strings.HasPrefix(authorization, "Bearer ") {
		t.Errorf("la llamada debía ir autenticada server-side, got %q", authorization)
	}
}

// TestDiscardOverLimitIsExplainedWithoutCallingAPI (criterio (f) del plan, visto desde la pantalla):
// con 201 ids la plataforma responde 400. Aquí se corta antes y se explica en español.
func TestDiscardOverLimitIsExplainedWithoutCallingAPI(t *testing.T) {
	api := discardAPI(t, discardListBody, nil) // nil ⇒ falla si se gasta el viaje
	defer api.Close()

	ids := make([]string, 0, overLimitBatch)
	for i := 0; i < overLimitBatch; i++ {
		ids = append(ids, "in-"+strconv.Itoa(i))
	}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", ids...), validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un lote de %d debía rechazarse con 400, got %d", len(ids), rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Marcaste 201 solicitudes y de una vez se pueden descartar como mucho 200") {
		t.Error("el rechazo debía decir cuántas se marcaron y cuál es el tope")
	}
	if !strings.Contains(out, "No se ha descartado ninguna") {
		t.Error("el rechazo debía dejar claro que no se tocó nada")
	}
	if strings.Contains(out, `id="section-intakes-discard-confirm"`) {
		t.Error("un lote rechazado por tamaño no puede quedar esperando confirmación")
	}
}

// TestDiscardEmptySelectionIsExplained: dar al botón sin marcar nada no puede parecer un descarte
// que no hizo nada, ni gastar un viaje que va a volver con un 400.
func TestDiscardEmptySelectionIsExplained(t *testing.T) {
	api := discardAPI(t, discardListBody, nil)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("review"), validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un lote vacío debía dar 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "Marca al menos una solicitud antes de descartar") {
		t.Error("debía decirse que no había nada marcado")
	}
	if strings.Contains(out, `id="section-intakes-discard-confirm"`) {
		t.Error("sin selección no hay nada que confirmar")
	}
}

// TestDiscardRequiresCSRF: el formulario que escribe pasa por el MISMO camino que los demás
// formularios de esta consola. Sin token no se llega ni a la API.
func TestDiscardRequiresCSRF(t *testing.T) {
	api := discardAPI(t, discardListBody, nil)
	defer api.Close()

	form := discardForm("discard", "in-old")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/intakes/discard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(validSessionCookie(t))
	NewRouter(authTestCfg(api.URL)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("un POST sin token CSRF debía dar 403, got %d", rec.Code)
	}
}

// TestDiscardWithoutFeatureIsCutBeforeTheAPI: sin `cart_basic` no se pinta la casilla ni se llama a
// la plataforma. El gate real sigue siendo el `RequireFeature` de allá; esto es lo que se emite.
func TestDiscardWithoutFeatureIsCutBeforeTheAPI(t *testing.T) {
	api := intakesAPI([]string{"menu"}, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("sin cart_basic no debía llamarse a %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	cookie := validSessionCookie(t)

	if out := getWithCookie(router, "/intakes", cookie).Body.String(); strings.Contains(out, `name="intake_id"`) {
		t.Error("sin la feature no debe quedar rastro de la selección de descarte en el HTML")
	}

	rec := postFormWithCookie(router, "/intakes/discard", discardForm("discard", "in-old"), cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sin la feature el descarte debía dar 403, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `id="section-intakes-discard-result"`) {
		t.Error("sin la feature no puede pintarse ningún resultado de descarte")
	}
}

// TestDiscardPlatformRejectionIsExplained: un 400 de la plataforma llega con su motivo, y diciendo
// que no se tocó nada.
func TestDiscardPlatformRejectionIsExplained(t *testing.T) {
	api := discardAPI(t, discardListBody, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"intake_ids es obligatorio: manda entre 1 y 200 ids"}`)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-old"), validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el rechazo debía propagarse como 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "intake_ids es obligatorio: manda entre 1 y 200 ids") {
		t.Error("el motivo del rechazo debía llegar al operador")
	}
	if !strings.Contains(out, "no se tocó ninguna solicitud") {
		t.Error("el aviso debía decir que nada cambió")
	}
}

// TestDiscardConfirmationNotOfferedWhenListFails: si la bandeja no se puede leer, no se ofrece
// confirmar. Enseñar qué se va a matar es la condición de esta acción, no un adorno.
func TestDiscardConfirmationNotOfferedWhenListFails(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Errorf("no debía llamarse a %s: no se llegó a confirmar", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("review", "in-old"), validSessionCookie(t))
	out := rec.Body.String()

	if strings.Contains(out, `id="section-intakes-discard-confirm"`) {
		t.Error("sin la bandeja delante no se puede ofrecer el botón que descarta")
	}
	if !strings.Contains(out, "No se pudieron cargar las solicitudes") {
		t.Error("debía decirse por qué no se puede seguir")
	}
}

// TestDiscardResultSurvivesAFailedRepaint: si la bandeja no se puede releer DESPUÉS de descartar, el
// desglose se pinta igual. Lo que pasó ya pasó, y ocultarlo dejaría al dueño sin saber qué se mató.
func TestDiscardResultSurvivesAFailedRepaint(t *testing.T) {
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == discardPath {
			_, _ = io.WriteString(w, `{"discarded":["in-old"],"skipped":[{"intake_id":"in-conf","reason":"not_open"}]}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard",
		discardForm("discard", "in-old", "in-conf"), validSessionCookie(t))
	out := rec.Body.String()

	if !strings.Contains(out, `id="section-intakes-discard-result"`) {
		t.Fatal("el desglose del lote ya ejecutado debía pintarse aunque la bandeja no se pueda releer")
	}
	if !strings.Contains(out, "in-old") || !strings.Contains(out, "Ya no está abierta") {
		t.Error("el desglose debía conservar qué cayó y qué no")
	}
}

// TestIntakeDiscardReasonKeepsUnknownKeys: una razón que esta consola no conoce se pinta TAL CUAL.
// Un motivo raro sigue siendo un motivo; callarlo dejaría creer que esa solicitud sí se descartó.
func TestIntakeDiscardReasonKeepsUnknownKeys(t *testing.T) {
	if got := intakeDiscardReason("razon_del_futuro"); got != "razon_del_futuro" {
		t.Errorf("una razón desconocida debía pasar tal cual, got %q", got)
	}
	for _, known := range []string{"not_found", "already_discarded", "not_open", "live_event"} {
		if got := intakeDiscardReason(known); got == known {
			t.Errorf("la razón %q debía traducirse a la voz del dueño", known)
		}
	}
}

// ============ «seleccionar todo lo visible» (Plan 044 · T4.8, D-041.18) ============

// Las dos páginas del fixture de la maestra. Los ids llevan el número de página en el nombre para
// que un fallo diga a simple vista de dónde salió la fila que no debía estar.
const (
	páginaUnoA = "in-p1-a"
	páginaUnoB = "in-p1-b"
	páginaDosA = "in-p2-a"
	páginaDosB = "in-p2-b"
)

// bandejaPaginada sirve DOS páginas distintas de la misma bandeja, elegidas por el `page` de la
// query. Es el fixture que hace verificable el «lo visible»: con una sola página, un filtro que
// seleccionara «todo lo que cumple el filtro» daría exactamente el mismo resultado que el correcto y
// el test pasaría sin mirar nada.
func bandejaPaginada(t *testing.T) *httptest.Server {
	t.Helper()
	página := func(a, b string, n int) string {
		return `{"intakes":[
		 {"id":"` + a + `","contact_id":"ct-1","session_id":"s-1","status":"open","total":10,
		  "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-05T10:00:00Z"},
		 {"id":"` + b + `","contact_id":"ct-2","session_id":"s-1","status":"open","total":20,
		  "created_at":"2026-08-04T10:00:00Z","updated_at":"2026-08-04T10:00:00Z"}],
		 "page":` + strconv.Itoa(n) + `,"page_size":2,"total":4}`
	}
	return intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes":
			if r.URL.Query().Get("page") == "2" {
				_, _ = io.WriteString(w, página(páginaDosA, páginaDosB, 2))
				return
			}
			_, _ = io.WriteString(w, página(páginaUnoA, páginaUnoB, 1))
		default:
			t.Errorf("ruta no esperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

// visibleOculto es el oculto que la plantilla emite por fila VISIBLE: la materia prima de la
// maestra, y el único conjunto que este formulario tiene al alcance.
func visibleOculto(id string) string {
	return `name="visible_intake_id" value="` + id + `"`
}

// TestIntakesListOfreceLaCasillaMaestra (T4.8): la cabecera de la tabla ofrece «marcar toda la
// página», y lo que viaja con ella son los ids de las filas PINTADAS — ni uno más.
func TestIntakesListOfreceLaCasillaMaestra(t *testing.T) {
	api := bandejaPaginada(t)
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes?page=2", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el listado debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, `<input type="checkbox" name="select_visible" value="1"`) {
		t.Fatal("la cabecera de la tabla debía ofrecer la casilla maestra")
	}
	for _, id := range []string{páginaDosA, páginaDosB} {
		if !strings.Contains(out, visibleOculto(id)) {
			t.Errorf("falta el oculto de %q: sin él esa fila no entra en «todo lo visible»", id)
		}
	}
	// 🔴 EL CRITERIO: lo de la OTRA página no está en el formulario. No «no se selecciona»: no
	// está — el conjunto ancho no existe aquí, así que la maestra no puede alcanzarlo ni por un
	// descuido del handler.
	for _, id := range []string{páginaUnoA, páginaUnoB} {
		if strings.Contains(out, id) {
			t.Errorf("%q es de la página 1 y aparece en el formulario de la página 2", id)
		}
	}
	// Sigue sin haber JS: la maestra la resuelve el servidor (ADR-0035).
	for _, forbidden := range []string{"<script", "onclick", "onchange"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("la maestra no puede traer %q: esta consola no emite JS", forbidden)
		}
	}
}

// TestDiscardMaestraSeleccionaSoloLaPáginaVISIBLE es el criterio de T4.8 escrito como conducta:
// marcar la maestra en la página 2 lleva a confirmar las DOS de la página 2, y ninguna de la 1.
//
// 💥 MUTACIÓN EJECUTADA, y COMPILA: hacer que la rama de la maestra, en vez de leer los ocultos del
// formulario, releyera la bandeja con `PageSize: apiclient.MaxIntakeDiscardBatch, Page: 1` — o sea
// «todo lo que cumple el filtro», que es la implementación que este criterio prohíbe. Con ella el
// lote pasa a ser el de la página 1 y este test se pone rojo por las dos mitades: falta lo de la
// página 2 y sobra lo de la 1.
func TestDiscardMaestraSeleccionaSoloLaPáginaVISIBLE(t *testing.T) {
	api := bandejaPaginada(t)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard?page=2",
		formConMaestra(páginaDosA, páginaDosB), validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisar el lote debía renderizar 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()

	if !strings.Contains(out, `id="section-intakes-discard-confirm"`) {
		t.Fatal("la maestra debía llevar a la pantalla de confirmación")
	}
	if !strings.Contains(out, avisoDescartePlural) {
		t.Error("la confirmación de la maestra es la MISMA que la de las casillas sueltas: 2 solicitudes")
	}
	for _, id := range []string{páginaDosA, páginaDosB} {
		if !strings.Contains(out, loteOculto(id)) {
			t.Errorf("falta %q en el lote a confirmar: la maestra tiene que marcar TODA la página", id)
		}
	}
	for _, id := range []string{páginaUnoA, páginaUnoB} {
		if strings.Contains(out, loteOculto(id)) {
			t.Errorf("%q es de OTRA página y se coló en el lote: la maestra selecciona lo visible, no lo que cumple el filtro", id)
		}
	}
}

// TestDiscardSinLaMaestraLosOcultosNoSeleccionanNada es la otra mitad, y sin ella la primera no
// significa nada: los ocultos `visible_intake_id` viajan SIEMPRE (un `hidden` no se desmarca), así
// que un handler que los leyera sin mirar la maestra convertiría cada «revisar» en «revisar toda la
// página» — incluido el de quien marcó UNA sola fila.
func TestDiscardSinLaMaestraLosOcultosNoSeleccionanNada(t *testing.T) {
	api := bandejaPaginada(t)
	defer api.Close()

	form := url.Values{}
	form.Set(intakeDiscardFieldAction, "review")
	form.Add(intakeDiscardFieldID, páginaDosA) // el operador marcó UNA
	form.Add(intakeDiscardFieldVisibleID, páginaDosA)
	form.Add(intakeDiscardFieldVisibleID, páginaDosB) // …pero la página trae las dos

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/discard?page=2",
		form, validSessionCookie(t))
	out := rec.Body.String()

	if !strings.Contains(out, loteOculto(páginaDosA)) {
		t.Errorf("falta la que el operador SÍ marcó (%q)", páginaDosA)
	}
	if strings.Contains(out, loteOculto(páginaDosB)) {
		t.Errorf("%q no estaba marcada y entró en el lote: sin la maestra los ocultos no seleccionan", páginaDosB)
	}
	if !strings.Contains(out, avisoDescarteSingular) {
		t.Error("con una sola marcada la confirmación tenía que contar 1, no la página entera")
	}
}

// formConMaestra arma el envío del navegador desde una página con la maestra marcada: la casilla,
// los ocultos de las filas visibles y el botón de revisar. Las casillas sueltas NO van — una maestra
// marcada es justo el caso en el que el operador no marcó ninguna a mano.
func formConMaestra(visibles ...string) url.Values {
	form := url.Values{}
	form.Set(intakeDiscardFieldSelectVisible, "1")
	for _, id := range visibles {
		form.Add(intakeDiscardFieldVisibleID, id)
	}
	form.Set(intakeDiscardFieldAction, "review")
	return form
}

// loteOculto es el oculto con el que la tarjeta de confirmación re-envía CADA solicitud del lote. Es
// el sitio exacto donde se lee qué se va a descartar: el listado de abajo pinta los mismos ids, pero
// en otro campo.
func loteOculto(id string) string {
	return `<input type="hidden" name="intake_id" value="` + id + `">`
}
