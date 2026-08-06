package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Anclas del gate: si `catalog_import` no está contratada, estas cadenas NO aparecen en el HTML. Eso
// es lo que distingue un gate server-side de un `display:none`.
const (
	catalogFormMarker    = `id="section-catalog-import"`
	catalogHelpMarker    = `id="section-catalog-help"`
	catalogDiffMarker    = `id="section-catalog-diff"`
	catalogConfirmMarker = `id="section-catalog-confirm"`
	catalogErrorsMarker  = `id="section-catalog-errors"`
)

// catalogImportAPI levanta una API pública fake que sirve SIEMPRE las features dadas (plan
// "commerce") y delega el resto de rutas en `handle`. Recuerda la última petición al import para
// poder afirmar QUÉ se pidió, no solo qué se pintó.
type catalogImportAPI struct {
	mu          sync.Mutex
	lastQuery   url.Values
	lastBody    string
	calls       int
	promptCalls int
	srv         *httptest.Server
	// prompt, si está puesto, contesta el prompt-plantilla en lugar del canned (para probar fallos).
	prompt http.HandlerFunc
}

// promptBody es el prompt tal como lo sirve la plataforma: el texto viaja con el format y la VERSIÓN
// del contrato al que corresponde. El BFF no tiene copia de este texto (a propósito), así que todo
// lo que la pantalla enseñe de aquí tuvo que venir por HTTP.
const promptBody = `{"format":"wapp.catalog_import","version":1,` +
	`"prompt":"Te doy mi lista de productos con precios y una plantilla JSON. ` +
	`Genera SOLO un JSON válido (format: wapp.catalog_import, version: 1). ` +
	`Reglas: no inventes productos ni precios que no estén en mi lista. Mi lista: …"}`

func newCatalogImportAPI(features []string, handle http.HandlerFunc) *catalogImportAPI {
	api := &catalogImportAPI{}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("commerce", features...))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/catalog/import") {
			body, _ := io.ReadAll(r.Body)
			api.mu.Lock()
			// El prompt NO se apunta como última petición: lo pide cualquier render del formulario y
			// pisaría la petición que el test quiere afirmar. Su contador va aparte, igual que el del
			// import: la plantilla y el prompt cuelgan de la misma capacidad pero no tocan el catálogo,
			// y mezclarlos escondería si se llamó a lo que escribe.
			if r.URL.Path == "/api/v1/catalog/import/prompt" {
				api.promptCalls++
			} else {
				api.lastQuery = r.URL.Query()
				api.lastBody = string(body)
				if r.URL.Path == "/api/v1/catalog/import" {
					api.calls++
				}
			}
			api.mu.Unlock()
		}
		// El prompt se sirve SIEMPRE por defecto (lo pide cualquier render del formulario), antes de
		// delegar: así un test que fuerza un fallo del import no fuerza también el del prompt.
		if r.URL.Path == "/api/v1/catalog/import/prompt" {
			api.mu.Lock()
			custom := api.prompt
			api.mu.Unlock()
			if custom != nil {
				custom(w, r)
				return
			}
			_, _ = io.WriteString(w, promptBody)
			return
		}
		if handle != nil {
			handle(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
	return api
}

func (a *catalogImportAPI) close() { a.srv.Close() }

func (a *catalogImportAPI) seen() (url.Values, string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastQuery, a.lastBody, a.calls
}

func (a *catalogImportAPI) prompts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.promptCalls
}

// postMultipartWithCookie envía el formulario de la pantalla como multipart, que es como lo manda el
// navegador cuando hay un `<input type=file>`. Con fileName vacío se manda la parte de archivo VACÍA:
// es lo que llega de verdad cuando el operador no elige ninguno.
func postMultipartWithCookie(router http.Handler, path string, fields map[string]string,
	fileName, fileContent string, cookie *http.Cookie) *httptest.ResponseRecorder {
	csrf := mintCSRF(router)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField(csrfFieldName, csrf.Value)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", fileName)
	_, _ = fw.Write([]byte(fileContent))
	_ = mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(csrf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}

const validCatalogDoc = `{"format":"wapp.catalog_import","version":1,"categories":[]}`

// textareaContent devuelve el contenido del textarea con ese id. Hace falta mirar DENTRO: el JSON se
// pinta escapado (html/template convierte las comillas), así que buscar el documento crudo en la
// página entera daría un falso negativo, y buscar un trozo suelto daría un falso positivo con el
// prompt, que también habla del formato.
func textareaContent(html, id string) string {
	at := strings.Index(html, `id="`+id+`"`)
	if at < 0 {
		return ""
	}
	open := strings.Index(html[at:], ">")
	if open < 0 {
		return ""
	}
	rest := html[at+open+1:]
	end := strings.Index(rest, "</textarea>")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// diffBody arma la respuesta 200 del import con un diff completo.
const diffBody = `{"mode":"validate","ref":"catalogo","applied":false,"items":12,"diff":{
 "price_changes":[{"sku":"emp-carne","label":"Empanada de carne","old_price":2.5,"new_price":3}],
 "added":[{"sku":"emp-queso","label":"Empanada de queso"}],
 "removed":[{"sku":"emp-pollo","label":"Empanada de pollo"}],
 "changed_details":["cafe-500"],"unchanged":9}}`

// TestCatalogImportShowsFormPromptAndTemplates (T3.5): la pantalla ofrece pegar o subir, enseña el
// prompt copiable y los enlaces de plantilla, y lleva la marca PROVISIONAL.
func TestCatalogImportShowsFormPromptAndTemplates(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{
		catalogFormMarker, catalogHelpMarker,
		`name="document"`, `type="file"`, `enctype="multipart/form-data"`,
		"Comprobar documento",
		// El prompt copiable, con la regla que lo hace útil (sin ella el asistente inventa productos).
		"wapp.catalog_import", "no inventes productos ni precios",
		// Los tres formatos de plantilla, cableados al endpoint de la plataforma vía el BFF.
		`href="/catalog-import/template?format=json"`,
		`href="/catalog-import/template?format=csv"`,
		`href="/catalog-import/template?format=xlsx"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la pantalla debía contener %q", want)
		}
	}
	if !strings.Contains(out, "PROVISIONAL — migra a KMP (Plan 045/047, ADR-0035)") {
		t.Error("la marca PROVISIONAL debía estar en la pantalla de import")
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (ADR-0035: server-side, cero framework)")
	}
	// Nada de diff ni de confirmación antes de mandar nada.
	if strings.Contains(out, catalogDiffMarker) || strings.Contains(out, `value="apply"`) {
		t.Error("sin documento comprobado no debe haber ni diff ni botón de aplicar")
	}
}

// TestCatalogImportPromptComesFromThePlatform: el prompt se PIDE, no se copia. Está versionado junto
// al contrato en la plataforma (T3.2), así que la pantalla enseña lo que llegó por HTTP —con la
// versión al lado— y, si no llega, lo dice en vez de servir una copia vieja.
func TestCatalogImportPromptComesFromThePlatform(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	out := getWithCookie(router, "/catalog-import", cookie).Body.String()
	if got := textareaContent(out, "prompt"); !strings.Contains(got, "no inventes productos ni precios") {
		t.Errorf("el prompt debía ser el que sirvió la plataforma, got %q", got)
	}
	if !strings.Contains(out, "contrato wapp.catalog_import versión 1") {
		t.Error("la versión del contrato debía verse: es lo que permite notar un prompt que se quedó atrás")
	}
	if api.prompts() != 1 {
		t.Errorf("debía pedirse el prompt una vez, got %d", api.prompts())
	}

	// Sin prompt, la pantalla sigue sirviendo: es ayuda, no la operación.
	api.mu.Lock()
	api.prompt = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	api.mu.Unlock()

	rec := getWithCookie(router, "/catalog-import", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("un prompt que no carga no puede tumbar la pantalla, got %d", rec.Code)
	}
	degraded := rec.Body.String()
	if !strings.Contains(degraded, "No se pudo cargar el texto para el asistente") {
		t.Error("debía decirse que el texto no cargó")
	}
	if strings.Contains(degraded, "no inventes productos ni precios") {
		t.Error("no puede haber copia local del prompt: sería una segunda fuente que envejece sola")
	}
	if !strings.Contains(degraded, catalogFormMarker) {
		t.Error("el formulario debía seguir emitiéndose sin el prompt")
	}
}

// TestCatalogImportListsAllValidationErrors (criterio de aceptación): un JSON con errores se lista
// LEGIBLE —todos los defectos, con el motivo tal cual lo redactó la plataforma— y no ofrece aplicar.
func TestCatalogImportListsAllValidationErrors(t *testing.T) {
	const errorsBody = `{"error":"validation_failed","errors":[
	 {"field":"version","reason":"el documento dice version 2 y esta plataforma entiende la 1: pide el archivo en la version 1."},
	 {"category_index":1,"field":"label","reason":"la categoria 2 no tiene nombre: escribe como la ve el cliente."},
	 {"category_index":1,"item_index":3,"field":"price","reason":"el articulo 4 de la categoria 2 no tiene precio: escribe el numero sin simbolo de moneda."}]}`

	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, errorsBody)
	})
	defer api.close()

	// El documento lleva una marca propia para poder afirmar que vuelve ÉL, y no confundirlo con el
	// prompt de la misma página (que también nombra el formato).
	const docWithMarker = `{"format":"wapp.catalog_import","version":2,"marca":"vuelve-esto","categories":[]}`

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		url.Values{"document": {docWithMarker}}, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un documento inválido debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, catalogErrorsMarker) {
		t.Fatal("debía emitirse la tabla de defectos")
	}
	if !strings.Contains(out, "El documento tiene 3 problemas") {
		t.Error("el aviso debía decir cuántos defectos hay antes de que el operador empiece")
	}
	// Los TRES motivos, verbatim: son el texto que la plataforma escribió para el dueño del negocio.
	for _, reason := range []string{
		"esta plataforma entiende la 1",
		"no tiene nombre: escribe como la ve el cliente",
		"escribe el numero sin simbolo de moneda",
	} {
		if !strings.Contains(out, reason) {
			t.Errorf("faltó el motivo %q: la lista debe traerlos TODOS y sin reescribir", reason)
		}
	}
	// La ubicación se cuenta desde 1, como ya cuenta la prosa del motivo (que habla del artículo 4 de
	// la categoría 2). Un índice crudo pondría dos números distintos para el mismo sitio.
	for _, where := range []string{"Todo el documento", "Categoría 2", "Categoría 2 · artículo 4"} {
		if !strings.Contains(out, where) {
			t.Errorf("faltó la ubicación %q", where)
		}
	}
	if !strings.Contains(textareaContent(out, "document"), "vuelve-esto") {
		t.Error("el documento debía volver al cuadro de texto: el trabajo del operador no se tira")
	}
	if strings.Contains(out, `value="apply"`) || strings.Contains(out, catalogDiffMarker) {
		t.Error("con errores no puede ofrecerse aplicar ni pintarse un diff")
	}
}

// TestCatalogImportShowsDiffAndOffersApply (criterio de aceptación): un JSON válido se COMPRUEBA —sin
// escribir nada— y el diff se ve antes de que exista el botón de aplicar.
func TestCatalogImportShowsDiffAndOffersApply(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, diffBody)
	})
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		url.Values{"document": {validCatalogDoc}}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un documento válido debía responder 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	// El modo por defecto de la plataforma es `validate`, pero el BFF no se apoya en él: manda el modo
	// explícito, porque una petición que puede escribir el catálogo no debe depender de un default.
	query, body, calls := api.seen()
	if calls != 1 {
		t.Fatalf("debía llamarse una sola vez al import, got %d", calls)
	}
	if query.Get("mode") != "validate" {
		t.Errorf("el primer paso debía pedir mode=validate, got %q", query.Get("mode"))
	}
	if body != validCatalogDoc {
		t.Errorf("el documento debía viajar CRUDO y sin tocar, got %q", body)
	}
	if query.Has("ref") {
		t.Error("la ref no debe mandarse: el default es de la plataforma y fijarlo aquí sería tener dos")
	}

	for _, want := range []string{
		catalogDiffMarker, catalogConfirmMarker,
		"12 artículos en el documento", "catálogo «catalogo»",
		"1 cambian de precio", "1 nuevos", "1 desaparecen", "9 quedan igual",
		"emp-carne", "2.50", "3.00", // el precio de antes y el de ahora
		"emp-queso", "emp-pollo", "cafe-500",
		"Dejan de venderse",
		`value="apply"`, "Aplicar este catálogo",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("el diff debía contener %q", want)
		}
	}
	if !strings.Contains(out, "Todavía NO se ha cambiado nada") {
		t.Error("comprobar tiene que decir en voz alta que no ha escrito nada")
	}
	if api.prompts() != 0 {
		t.Error("con un diff esperando confirmación no se pinta la ayuda, así que no debe pedirse el prompt")
	}
	// El documento confirmado viaja OCULTO y congelado: se aplica lo que se acaba de enseñar, no lo
	// que hubiera en un cuadro editable. Y por eso el paso 1 desaparece.
	if !strings.Contains(out, `<input type="hidden" name="document"`) {
		t.Error("la confirmación debía llevar el documento oculto")
	}
	if strings.Contains(out, catalogFormMarker) {
		t.Error("con un diff esperando confirmación no debe seguir emitiéndose el formulario de pegar")
	}
}

// TestCatalogImportAppliesOnlyWhenConfirmed (criterio de aceptación): el segundo paso escribe, y solo
// entonces se pide mode=apply.
func TestCatalogImportAppliesOnlyWhenConfirmed(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "apply" {
			t.Errorf("este test solo espera el apply, llegó mode=%q", r.URL.Query().Get("mode"))
		}
		_, _ = io.WriteString(w, `{"mode":"apply","ref":"catalogo","applied":true,"items":12,
		 "archived_version":4,"diff":{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":12}}`)
	})
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		url.Values{"document": {validCatalogDoc}, "action": {"apply"}}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("el apply debía responder 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	query, body, _ := api.seen()
	if query.Get("mode") != "apply" {
		t.Errorf("el segundo paso debía pedir mode=apply, got %q", query.Get("mode"))
	}
	if body != validCatalogDoc {
		t.Errorf("debía aplicarse EXACTAMENTE el documento confirmado, got %q", body)
	}
	for _, want := range []string{
		"Catálogo aplicado", "12 artículos en «catalogo»", "versión 4",
		// El diff vacío se dice, no se deja en blanco. Y en pasado: ya se aplicó.
		"No cambió ni un artículo",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la confirmación debía contener %q", want)
		}
	}
	if strings.Contains(out, catalogConfirmMarker) || strings.Contains(out, `value="apply"`) {
		t.Error("aplicado el catálogo, no puede quedar un botón que lo vuelva a aplicar sin comprobar")
	}
	if !strings.Contains(out, catalogFormMarker) {
		t.Error("tras aplicar, la pantalla debía volver a ofrecer el formulario")
	}
}

// TestCatalogImportShowsCurrentWarnings: los avisos del catálogo VIGENTE se ven en pantalla. Son
// artículos que el motor ya descarta en silencio, así que no salen en «desaparecen» aunque se
// pierdan: esconderlos deshace la única salvaguarda contra una pérdida invisible.
func TestCatalogImportShowsCurrentWarnings(t *testing.T) {
	const warned = `{"mode":"validate","ref":"catalogo","applied":false,"items":3,"diff":{
	 "price_changes":[],"added":[{"sku":"pan","label":"Pan"}],"removed":[],"changed_details":[],"unchanged":0,
	 "current_warnings":["catálogo vigente: se ignoró el artículo con sku _envio porque el guion bajo está reservado",
	                     "catálogo vigente: la ref tiene contenido, pero no se pudo interpretar como catálogo"]}}`

	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, warned)
	})
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		url.Values{"document": {validCatalogDoc}}, validSessionCookie(t))
	out := rec.Body.String()

	for _, want := range []string{
		"Ojo con el catálogo que tienes ahora",
		"el guion bajo está reservado",
		"no se pudo interpretar como catálogo",
		"no lo verás en «desaparecen»",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("los avisos del catálogo vigente debían verse: falta %q", want)
		}
	}
	// Y van ANTES del resto del diff, que es donde se leen antes de decidir.
	if warn, chips := strings.Index(out, "Ojo con el catálogo"), strings.Index(out, "nuevos</span>"); warn < 0 || chips < 0 || warn > chips {
		t.Error("los avisos debían ir delante del resumen del diff")
	}
}

// TestCatalogImportGateOmitsSectionWithoutFeature (criterio de aceptación): sin `catalog_import` la
// sección NO se emite en el HTML —ni formulario, ni prompt, ni enlaces— y no se llama a la API.
func TestCatalogImportGateOmitsSectionWithoutFeature(t *testing.T) {
	api := newCatalogImportAPI([]string{"cart_basic", "menu"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		// El import NO debe consultarse siquiera cuando la feature no está.
		t.Errorf("sin catalog_import no debía llamarse a %s", r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	defer api.close()

	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	out := getWithCookie(router, "/catalog-import", cookie).Body.String()
	for _, forbidden := range []string{
		catalogFormMarker, catalogHelpMarker,
		`name="document"`, "<textarea", `type="file"`,
		"wapp.catalog_import", "/catalog-import/template",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("sin catalog_import el HTML no debía contener %q", forbidden)
		}
	}
	if !strings.Contains(out, "no incluye la importación de catálogo") {
		t.Error("la pantalla debía explicar por qué no hay nada, en vez de quedarse muda")
	}
	// Tampoco el enlace de la barra: fail-closed también en la navegación.
	dash := getWithCookie(router, "/", cookie).Body.String()
	if strings.Contains(dash, `href="/catalog-import"`) {
		t.Error("sin la feature no debe quedar rastro del import en la navegación")
	}
	if _, _, calls := api.seen(); calls != 0 {
		t.Errorf("no debía gastarse ni un viaje al import, got %d", calls)
	}
}

// TestCatalogImportEmitsNavLinkWithFeature: con la feature, la barra superior ofrece el enlace.
func TestCatalogImportEmitsNavLinkWithFeature(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, `<a href="/catalog-import" class="btn btn--text`) {
		t.Error("con catalog_import, la barra superior debía ofrecer el enlace al import")
	}
}

// TestCatalogImportReadsUploadedFile: el archivo subido es el documento que viaja, y una parte de
// archivo VACÍA —la que manda el navegador cuando no se elige ninguno— no pisa lo que se pegó.
func TestCatalogImportReadsUploadedFile(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, diffBody)
	})
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	const fromFile = `{"format":"wapp.catalog_import","version":1,"categories":[{"label":"Del archivo"}]}`
	if rec := postMultipartWithCookie(router, "/catalog-import", nil, "catalogo.json", fromFile, cookie); rec.Code != http.StatusOK {
		t.Fatalf("la subida debía responder 200, got %d", rec.Code)
	}
	if _, body, _ := api.seen(); body != fromFile {
		t.Errorf("debía enviarse el contenido del archivo, got %q", body)
	}

	// Sin archivo elegido, manda lo pegado.
	if rec := postMultipartWithCookie(router, "/catalog-import",
		map[string]string{"document": validCatalogDoc}, "", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("el pegado debía responder 200, got %d", rec.Code)
	}
	if _, body, _ := api.seen(); body != validCatalogDoc {
		t.Errorf("una parte de archivo vacía no debe pisar lo pegado, got %q", body)
	}
}

// TestCatalogImportRejectsUnsendableDocument: lo que el BFF comprueba es si PUEDE enviar el
// documento, no qué tiene de malo. Un cuadro vacío o una planilla no gastan un viaje.
func TestCatalogImportRejectsUnsendableDocument(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	empty := postFormWithCookie(router, "/catalog-import", url.Values{"document": {"   "}}, cookie)
	if empty.Code != http.StatusBadRequest {
		t.Errorf("un documento vacío debía responder 400, got %d", empty.Code)
	}
	if !strings.Contains(empty.Body.String(), "Pega el documento del catálogo o elige un archivo") {
		t.Error("debía pedirse el documento en vez de mandar una petición vacía")
	}

	sheet := postFormWithCookie(router, "/catalog-import",
		url.Values{"document": {"sku;nombre;precio\nemp-carne;Empanada;2.5"}}, cookie)
	if sheet.Code != http.StatusBadRequest {
		t.Errorf("una planilla pegada debía responder 400, got %d", sheet.Code)
	}
	if !strings.Contains(sheet.Body.String(), "tiene que empezar por «{»") {
		t.Error("debía decirse por qué no se puede enviar eso")
	}
	if _, _, calls := api.seen(); calls != 0 {
		t.Errorf("nada de esto debía llegar a la plataforma, got %d llamadas", calls)
	}
}

// TestCatalogImportTemplateDownload: la plantilla se descarga a través del BFF —el token vive
// server-side, así que un enlace directo a la plataforma se llevaría un 401— y con el nombre que
// pone el BFF, no el que diga el upstream.
func TestCatalogImportTemplateDownload(t *testing.T) {
	const template = `{"format":"wapp.catalog_import","version":1,"categories":[{"label":"Ejemplo"}]}`
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/catalog/import/template" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Un upstream que intenta decidir cómo se guarda el archivo: se ignora a propósito.
		w.Header().Set("Content-Disposition", `attachment; filename="ajeno.txt"`)
		_, _ = io.WriteString(w, template)
	})
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import/template?format=json", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la descarga debía responder 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != template {
		t.Errorf("debía servirse la plantilla tal cual, got %q", body)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="catalogo-plantilla.json"` {
		t.Errorf("el nombre lo pone el BFF, no el upstream; got %q", cd)
	}
	if query, _, _ := api.seen(); query.Get("format") != "json" {
		t.Errorf("el formato debía viajar a la plataforma, got %q", query.Get("format"))
	}
}

// TestCatalogImportTemplateNotYetServed: mientras la plataforma no sirva la plantilla, el fallo no se
// descarga —se vuelve a la pantalla, que es donde el operador puede hacer algo— y el prompt sigue ahí.
func TestCatalogImportTemplateNotYetServed(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`)
	})
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import/template", validSessionCookie(t))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("debía responder 404, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no está sirviendo la plantilla") {
		t.Error("debía explicarse qué pasa, no un error genérico")
	}
	if !strings.Contains(out, "avísale a quien administre la plataforma") {
		t.Error("el aviso debía decir qué hacer con eso")
	}
	if !strings.Contains(out, catalogFormMarker) {
		t.Error("la pantalla debía seguir sirviendo: un fallo de descarga no tumba el import")
	}
	// Sin `format` en la URL se pide la JSON, que es la que se pega aquí.
	if query, _, _ := api.seen(); query.Get("format") != "json" {
		t.Errorf("el formato por defecto debía ser json, got %q", query.Get("format"))
	}
}

// TestCatalogImportTemplate404DoesNotPromiseThePrompt: en la plataforma la plantilla, el prompt y el
// import se montan JUNTOS o no se monta ninguno, así que un 404 de la plantilla suele venir
// acompañado de un prompt que tampoco carga. El aviso NO puede ofrecer como alternativa algo que va a
// fallar igual: mandaría al operador a chocarse dos veces.
func TestCatalogImportTemplate404DoesNotPromiseThePrompt(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer api.close()
	// Las rutas del import no están montadas: el prompt cae con ellas.
	api.mu.Lock()
	api.prompt = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }
	api.mu.Unlock()

	out := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import/template", validSessionCookie(t)).Body.String()

	if strings.Contains(out, "puedes usar el texto de abajo") || strings.Contains(out, "Mientras tanto") {
		t.Error("el aviso no puede remitir al prompt: con las rutas sin montar tampoco carga")
	}
	if !strings.Contains(out, "No se pudo cargar el texto para el asistente") {
		t.Error("y la pantalla debía decir que el prompt tampoco está, en vez de fingir que sí")
	}
}

// TestCatalogImportTemplateRejectsUnknownFormat: el formato se valida contra la lista blanca ANTES
// de gastar el viaje, y de esa misma lista salen el tipo y el nombre del archivo, así que un formato
// inventado no puede colarse hasta la descarga.
func TestCatalogImportTemplateRejectsUnknownFormat(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import/template?format=pdf", validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un formato desconocido debía responder 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Ese formato de plantilla no existe") {
		t.Error("debía decirse que ese formato no existe")
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("un formato desconocido no puede acabar en una descarga")
	}
	if query, _, _ := api.seen(); query != nil {
		t.Error("no debía gastarse el viaje a la plataforma")
	}
}

// TestCatalogImportRejectsOversizedBody: el techo de cuerpo corta ANTES de que el CSRF se trague el
// archivo entero. Es la única pantalla del BFF que acepta subidas y la única con techo.
func TestCatalogImportRejectsOversizedBody(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))

	oversized := url.Values{"document": {strings.Repeat("a", maxCatalogImportBody+1)}}
	rec := postFormWithCookie(router, "/catalog-import", oversized, validSessionCookie(t))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("un cuerpo por encima del techo debía responder 413, got %d", rec.Code)
	}
	if _, _, calls := api.seen(); calls != 0 {
		t.Errorf("no debía llegar nada a la plataforma, got %d llamadas", calls)
	}

	// El techo es de esta ruta y solo de esta: el editor de flujos publica definiciones grandes y no
	// se le cambia el comportamiento por la puerta de atrás.
	flow := postFormWithCookie(router, "/flows", url.Values{"definition": {strings.Repeat("b", maxCatalogImportBody+1)}},
		validSessionCookie(t))
	if flow.Code == http.StatusRequestEntityTooLarge {
		t.Error("el techo no debía aplicarse a otras rutas")
	}
}

// TestCatalogImportUpstreamRejections: los rechazos con motivo llegan al operador, y el fallo del
// apply no promete que no haya pasado nada (podría haberse escrito y no haberse podido confirmar).
func TestCatalogImportUpstreamRejections(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		apply      bool
		wantStatus int
		wantText   string
	}{
		{"documento gigante", http.StatusRequestEntityTooLarge, `{"error":"el documento excede el tamaño máximo"}`,
			false, http.StatusRequestEntityTooLarge, "Divide el catálogo en varias importaciones"},
		{"modo desconocido", http.StatusBadRequest, `{"error":"mode debe ser validate o apply"}`,
			false, http.StatusBadRequest, "La plataforma rechazó la petición: mode debe ser validate o apply"},
		{"sin permiso", http.StatusForbidden, `{"error":"feature_not_enabled","feature":"catalog_import"}`,
			false, http.StatusForbidden, "o el plan ya no incluye la importación"},
		{"caída al aplicar", http.StatusInternalServerError, `{"error":"boom"}`,
			true, http.StatusBadGateway, "No se pudo confirmar si el catálogo llegó a aplicarse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			defer api.close()

			form := url.Values{"document": {validCatalogDoc}}
			if tc.apply {
				form.Set("action", "apply")
			}
			rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import", form, validSessionCookie(t))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tc.wantText) {
				t.Errorf("el aviso debía contener %q", tc.wantText)
			}
		})
	}
}
