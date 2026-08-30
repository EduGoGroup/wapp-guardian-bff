package web

import (
	"bytes"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
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
	lastPath    string
	lastQuery   url.Values
	lastBody    string
	lastFile    string
	lastName    string
	calls       int
	promptCalls int
	srv         *httptest.Server
	// prompt, si está puesto, contesta el prompt-plantilla en lugar del canned (para probar fallos).
	prompt http.HandlerFunc
	// contentRefs, si está puesto, contesta el listado de refs en lugar del canned (para probar que
	// la pantalla sigue sirviendo cuando no se pueden listar).
	contentRefs http.HandlerFunc
}

// contentRefsBody son las refs de contenido que el tenant de los tests ya tiene. Son DOS y con el
// mismo prefijo a propósito: el defecto A3 se manifestó justo entre «catalogo» y «catalogo-1», que
// es el par que un operador confunde sin darse cuenta.
const contentRefsBody = `[{"ref":"catalogo","updated_at":"2026-08-01T10:00:00Z"},` +
	`{"ref":"catalogo-1","updated_at":"2026-08-05T10:00:00Z"}]`

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
				api.lastPath = r.URL.Path
				api.lastQuery = r.URL.Query()
				api.lastBody = string(body)
				// Las DOS puertas del import cuentan como llamada al import: las dos pueden escribir el
				// catálogo. La plantilla no.
				if r.URL.Path == "/api/v1/catalog/import" {
					api.calls++
				}
				if r.URL.Path == "/api/v1/catalog/import/tabular" {
					api.calls++
					api.lastFile, api.lastName = filePartOf(body, r.Header.Get("Content-Type"))
				}
			}
			api.mu.Unlock()
		}
		// Las refs de contenido se sirven SIEMPRE por defecto y por el mismo motivo que el prompt: las
		// pide cualquier render del paso 1 para armar el selector de ref, y sin este caso caerían en el
		// handler del test, que solo espera peticiones del import.
		if r.URL.Path == "/api/v1/tenant-content" {
			api.mu.Lock()
			custom := api.contentRefs
			api.mu.Unlock()
			if custom != nil {
				custom(w, r)
				return
			}
			_, _ = io.WriteString(w, contentRefsBody)
			return
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

// path devuelve por qué puerta entró la última petición del import.
func (a *catalogImportAPI) path() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastPath
}

// upload devuelve el contenido y el nombre del archivo que llegó en el multipart.
func (a *catalogImportAPI) upload() (content, filename string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFile, a.lastName
}

// filePartOf saca del cuerpo multipart la parte «file»: su contenido y su nombre. Se hace con el
// parser de verdad —no buscando cadenas— para que el test compruebe que lo enviado es un multipart
// bien formado y no un montón de bytes que casualmente contienen el archivo.
func filePartOf(body []byte, contentType string) (content, filename string) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	form, err := mr.ReadForm(1 << 20)
	if err != nil {
		return "", ""
	}
	files := form.File["file"]
	if len(files) == 0 {
		return "", ""
	}
	f, err := files[0].Open()
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", ""
	}
	return string(raw), files[0].Filename
}

// postMultipartWithCookie envía el formulario de la pantalla como multipart, que es como lo manda el
// navegador cuando hay un `<input type=file>`. Con fileName vacío se manda la parte de archivo VACÍA:
// es lo que llega de verdad cuando el operador no elige ninguno.
func postMultipartWithCookie(router http.Handler, path string, fields map[string]string,
	fileName, fileContent string, cookie *http.Cookie) *httptest.ResponseRecorder {
	csrf := mintCSRF(router)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField(sharedweb.CSRFFieldName, csrf.Value)
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

// hiddenValue devuelve el valor de un `<input type="hidden">` DESESCAPADO, que es lo que el
// navegador reenviaría. Va así a propósito: el documento se pinta en un atributo con las comillas
// escapadas, y comprobar el viaje de ida y vuelta con el valor crudo no probaría nada del round-trip
// real.
func hiddenValue(page, name string) string {
	marker := `name="` + name + `" value="`
	at := strings.Index(page, marker)
	if at < 0 {
		return ""
	}
	rest := page[at+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return html.UnescapeString(rest[:end])
}

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
	if !strings.Contains(out, "PROVISIONAL — migra a la consola de administración (Plan 047, ADR-0047)") {
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
		// El import NO debe consultarse siquiera cuando la feature no está. La rama que atendía
		// `/api/v1/sessions` se fue con el dashboard (Plan 047 · T2.1): ahora esa ruta también cae
		// aquí, que es donde debe caer si alguien la resucita.
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
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
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

// planillaCSV es una planilla mínima: lo que importa del test es por dónde sale y con qué bytes, no
// que el contenido sea un catálogo válido (eso lo valida la plataforma, no el BFF).
const planillaCSV = "sku;categoria;nombre;precio\nemp-carne;Empanadas;Empanada de carne;2,5\n"

// diffConDocumento es la respuesta del validate TABULAR: el mismo objeto del import JSON con
// `document` —el sobre entero ya traducido— como único añadido.
const diffConDocumento = `{"mode":"validate","ref":"catalogo","applied":false,"items":1,
 "document":{"format":"wapp.catalog_import","version":1,"source":"planilla",
   "catalog":{"categories":[{"label":"Empanadas","items":[{"sku":"emp-carne","label":"Empanada de carne","price":2.5}]}]}},
 "diff":{"price_changes":[],"added":[{"sku":"emp-carne","label":"Empanada de carne"}],
   "removed":[],"changed_details":[],"unchanged":0}}`

// TestCatalogImportSendsSpreadsheetToTabularDoor (T3.4): una planilla sale por `/tabular`, en el
// campo `file` de un multipart, y con los bytes del archivo SIN TOCAR.
func TestCatalogImportSendsSpreadsheetToTabularDoor(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, diffConDocumento)
	})
	defer api.close()

	rec := postMultipartWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		nil, "catalogo.csv", planillaCSV, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la planilla debía comprobarse con 200, got %d", rec.Code)
	}

	if got := api.path(); got != "/api/v1/catalog/import/tabular" {
		t.Errorf("la planilla debía salir por la puerta tabular, got %q", got)
	}
	if query, _, _ := api.seen(); query.Get("mode") != "validate" {
		t.Errorf("el primer paso debía pedir mode=validate, got %q", query.Get("mode"))
	}
	content, filename := api.upload()
	if content != planillaCSV {
		t.Errorf("el archivo debía viajar sin tocar, got %q", content)
	}
	if filename != "catalogo.csv" {
		t.Errorf("el nombre original debía viajar en la parte, got %q", filename)
	}
	// Y en pantalla, el mismo diff de siempre: la planilla no estrena renderizador.
	if out := rec.Body.String(); !strings.Contains(out, catalogDiffMarker) || !strings.Contains(out, "emp-carne") {
		t.Error("la planilla debía pintar el diff con el mismo camino que el JSON")
	}
}

// TestCatalogImportConfirmsSpreadsheetThroughJSONDoor: el paso 2 de una planilla sale por el import
// JSON con el documento NORMALIZADO que devolvió el validate. Es lo que permite confirmar un .xlsx
// —binario, incapaz de volver en un campo oculto— sin volver a pedir el archivo.
func TestCatalogImportConfirmsSpreadsheetThroughJSONDoor(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/catalog/import/tabular" {
			_, _ = io.WriteString(w, diffConDocumento)
			return
		}
		_, _ = io.WriteString(w, `{"mode":"apply","ref":"catalogo","applied":true,"items":1,"archived_version":2,
		 "diff":{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":1}}`)
	})
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	// Paso 1: la planilla. El documento traducido queda en el campo oculto, listo para confirmarse.
	step1 := postMultipartWithCookie(router, "/catalog-import", nil, "catalogo.xlsx", planillaCSV, cookie).Body.String()
	if !strings.Contains(step1, `<input type="hidden" name="document"`) {
		t.Fatal("el paso 1 debía dejar el documento traducido en el formulario de confirmación")
	}
	if !strings.Contains(step1, "wapp.catalog_import") {
		t.Error("el documento oculto debía ser el que devolvió la plataforma, no la planilla")
	}
	if !strings.Contains(step1, `name="ref" value="catalogo"`) {
		t.Error("la ref del paso 1 debía arrastrarse al formulario de confirmación")
	}

	// Paso 2: confirmar. Sale por la puerta JSON con ese documento y esa ref.
	hidden := hiddenValue(step1, "document")
	if hidden == "" {
		t.Fatal("no se pudo leer el documento oculto")
	}
	step2 := postFormWithCookie(router, "/catalog-import",
		url.Values{"document": {hidden}, "ref": {"catalogo"}, "action": {"apply"}}, cookie)
	if step2.Code != http.StatusOK {
		t.Fatalf("el apply debía responder 200, got %d", step2.Code)
	}

	if got := api.path(); got != "/api/v1/catalog/import" {
		t.Errorf("el paso 2 debía salir por la puerta JSON, got %q", got)
	}
	query, body, _ := api.seen()
	if query.Get("mode") != "apply" {
		t.Errorf("el paso 2 debía pedir mode=apply, got %q", query.Get("mode"))
	}
	if query.Get("ref") != "catalogo" {
		t.Errorf("la ref debía viajar explícita en el paso 2, got %q", query.Get("ref"))
	}
	if !strings.Contains(body, `"wapp.catalog_import"`) || !strings.Contains(body, `"emp-carne"`) {
		t.Errorf("debía aplicarse el documento traducido, got %q", body)
	}
	if !strings.Contains(step2.Body.String(), "Catálogo aplicado") {
		t.Error("la confirmación debía decir que se aplicó")
	}
}

// TestCatalogImportTabularErrorsLocateTheRow: los defectos de una planilla se ubican por FILA —el
// número que la hoja enseña en su margen— y no por índices de categoría. No se le suma nada: ya
// viene en el sistema del operador.
func TestCatalogImportTabularErrorsLocateTheRow(t *testing.T) {
	const errores = `{"error":"validation_failed","errors":[
	 {"row":4,"field":"precio","reason":"la fila 4 trae un precio que no es un número: escribe solo el número, sin símbolo de moneda."},
	 {"row":7,"field":"sku","reason":"el sku emp-carne ya lo usa la fila 4: cada artículo necesita el suyo."},
	 {"field":"archivo","reason":"el libro no tiene ninguna hoja llamada «catalogo»: renómbrala así o parte de la plantilla."}]}`

	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, errores)
	})
	defer api.close()

	rec := postMultipartWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		nil, "catalogo.xlsx", planillaCSV, validSessionCookie(t))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("una planilla inválida debía responder 400, got %d", rec.Code)
	}
	out := rec.Body.String()

	for _, want := range []string{
		"Fila 4", "Fila 7",
		"precio", "sku", "archivo",
		"escribe solo el número, sin símbolo de moneda",
		"ya lo usa la fila 4",
		"el libro no tiene ninguna hoja",
		"Campo o columna", // el encabezado admite las dos formas del contrato
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la lista de defectos debía contener %q", want)
		}
	}
	// El defecto sin fila es del archivo entero, y se dice así en vez de inventarse una fila.
	if !strings.Contains(out, "Todo el documento") {
		t.Error("un defecto sin fila debía ubicarse en el documento entero")
	}
	// Con la planilla rechazada NO hay documento que confirmar: la plataforma no lo manda a medias.
	if strings.Contains(out, `value="apply"`) {
		t.Error("una planilla rechazada no puede ofrecer aplicar")
	}
}

// TestCatalogImportChoosesDoorByContent: la puerta la elige el CONTENIDO, no la extensión — igual
// que la plataforma elige el parser. Un JSON llamado .csv sigue siendo un JSON.
func TestCatalogImportChoosesDoorByContent(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, diffBody)
	})
	defer api.close()
	router := NewRouter(authTestCfg(api.srv.URL))
	cookie := validSessionCookie(t)

	// JSON con el nombre equivocado: puerta JSON.
	postMultipartWithCookie(router, "/catalog-import", nil, "catalogo.csv", validCatalogDoc, cookie)
	if got := api.path(); got != "/api/v1/catalog/import" {
		t.Errorf("un JSON debía salir por la puerta JSON aunque se llame .csv, got %q", got)
	}

	// JSON guardado con BOM por un editor de Windows: sigue siendo JSON, no una planilla.
	postMultipartWithCookie(router, "/catalog-import", nil, "catalogo.json", "\xef\xbb\xbf"+validCatalogDoc, cookie)
	if got := api.path(); got != "/api/v1/catalog/import" {
		t.Errorf("un JSON con BOM no es una planilla, got %q", got)
	}

	// Planilla con el nombre equivocado: puerta tabular.
	postMultipartWithCookie(router, "/catalog-import", nil, "catalogo.json", planillaCSV, cookie)
	if got := api.path(); got != "/api/v1/catalog/import/tabular" {
		t.Errorf("una planilla debía salir por la puerta tabular aunque se llame .json, got %q", got)
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

	// El techo es de esta ruta y solo de esta: las demás pantallas de escritura no ven cambiado su
	// comportamiento por la puerta de atrás.
	//
	// 🔴 LA RUTA TESTIGO TIENE QUE ESTAR VIVA. Aquí estaba `POST /flows`, y con la retirada del editor
	// (Plan 047 · T6.6) ese POST responde 404: el aserto habría seguido VERDE midiendo que un 404 no
	// es un 413, o sea, midiendo nada. Se cambió a `POST /variables`, que sigue registrada y sin gate
	// de feature, y se comprueba ADEMÁS que no da 404 — si algún día se retirara, este test lo dice en
	// vez de degradarse en silencio.
	otra := postFormWithCookie(router, "/variables", url.Values{"key_0": {"k"}, "value_0": {strings.Repeat("b", maxCatalogImportBody+1)}},
		validSessionCookie(t))
	if otra.Code == http.StatusNotFound {
		t.Fatal("la ruta testigo del techo dejó de existir: el aserto de abajo mediría un 404, no un techo")
	}
	if otra.Code == http.StatusRequestEntityTooLarge {
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

// ─────────────────────── El selector de ref (defecto A3 del Plan 041) ───────────────────────
//
// A3: el paso 1 no tenía campo `ref`. El handler la leía —`c.PostForm("ref")`— pero el formulario
// no la mandaba nunca, así que viajaba vacía y la plataforma caía a su default `catalogo`. El
// resultado no era un error sino una MENTIRA: el mismo documento enseñaba «5 nuevos, 0 desaparecen»
// por la consola y «5 nuevos, 3 desaparecen» por la API con `?ref=catalogo-1`. El operador
// confirmaba convencido contra un catálogo que no era el suyo.

// TestCatalogImportOfreceElegirLaRef es el defecto en su forma más directa: la pantalla tiene que
// emitir el selector con las refs que el tenant ya tiene.
func TestCatalogImportOfreceElegirLaRef(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la pantalla debía responder 200, got %d", rec.Code)
	}
	out := rec.Body.String()

	if !strings.Contains(out, `<select class="field__control" id="ref" name="ref">`) {
		t.Fatal("el paso 1 debía ofrecer el selector de ref: sin él se manda vacía y la plataforma elige")
	}
	for _, ref := range []string{"catalogo", "catalogo-1"} {
		if !strings.Contains(out, `<option value="`+ref+`"`) {
			t.Errorf("el selector debía ofrecer la ref %q que el tenant ya tiene", ref)
		}
	}
	if !strings.Contains(out, `<option value="catalogo" selected>`) {
		t.Error("exactamente una opción debía venir marcada, y sin refs previas manda la primera")
	}
	if strings.Count(out, " selected>") != 1 {
		t.Errorf("debía haber UNA sola opción marcada, hay %d", strings.Count(out, " selected>"))
	}
}

// TestCatalogImportMandaLaRefElegida es la otra mitad: que lo elegido LLEGUE a la plataforma. Es la
// comprobación que separa este arreglo de un adorno — el diff se calcula contra la ref que viaje.
func TestCatalogImportMandaLaRefElegida(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"validate","ref":"catalogo-1","applied":false,"items":5,
		 "diff":{"price_changes":[],"added":[],"removed":[],"changed_details":[],"unchanged":5}}`)
	})
	defer api.close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import",
		url.Values{"document": {validCatalogDoc}, "ref": {"catalogo-1"}}, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("comprobar debía responder 200, got %d", rec.Code)
	}

	query, _, _ := api.seen()
	if got := query.Get("ref"); got != "catalogo-1" {
		t.Fatalf("la ref elegida debía viajar a la plataforma, llegó %q "+
			"(con la ref vacía el diff se calcula contra otro catálogo: ese era el defecto A3)", got)
	}
}

// TestCatalogImportSinRefsSigueOfreciendoUna cubre el tenant que estrena catálogo y, de paso, el
// listado que no se puede leer: la pantalla NO puede degradar a un selector vacío, porque eso
// devolvería la ref vacía y con ella el defecto.
func TestCatalogImportSinRefsSigueOfreciendoUna(t *testing.T) {
	api := newCatalogImportAPI([]string{"catalog_import"}, nil)
	api.contentRefs = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
	defer api.close()

	rec := getWithCookie(NewRouter(authTestCfg(api.srv.URL)), "/catalog-import", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("un listado de refs caído no puede tumbar la pantalla, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `<option value="catalogo" selected>`) {
		t.Error("sin refs legibles debía ofrecerse la de arranque, marcada: un selector vacío manda la ref vacía")
	}
	if !strings.Contains(out, catalogFormMarker) {
		t.Error("el formulario debía seguir sirviéndose: listar refs es ayuda, no la operación")
	}
}

// TestCatalogImportRefOptions ejercita las tres reglas del armado sin pasar por HTTP.
func TestCatalogImportRefOptions(t *testing.T) {
	refs := []apiclient.TenantContentRef{{Ref: "catalogo"}, {Ref: ""}, {Ref: "menu-x"}}

	t.Run("descarta las vacías y conserva el orden de la plataforma", func(t *testing.T) {
		opts := catalogImportRefOptions(refs, "")
		if len(opts) != 2 {
			t.Fatalf("una ref vacía no es una opción: esperaba 2, got %d", len(opts))
		}
		if opts[0].Value != "catalogo" || opts[1].Value != "menu-x" {
			t.Errorf("el orden debía ser el de la plataforma, got %+v", opts)
		}
		if !opts[0].Selected || opts[1].Selected {
			t.Error("sin ref elegida manda la primera, y solo ella")
		}
	})

	t.Run("la ref elegida que ya no existe se conserva igual", func(t *testing.T) {
		opts := catalogImportRefOptions(refs, "catalogo-viejo")
		last := opts[len(opts)-1]
		if last.Value != "catalogo-viejo" || !last.Selected {
			t.Fatalf("perder la ref elegida entre los dos pasos es el fallo silencioso de A3, got %+v", opts)
		}
		if last.Existing {
			t.Error("una ref que no vino en el listado no puede pintarse como existente")
		}
	})

	t.Run("sin ninguna ref se ofrece la de arranque", func(t *testing.T) {
		opts := catalogImportRefOptions(nil, "")
		if len(opts) != 1 || opts[0].Value != fallbackCatalogRef || !opts[0].Selected {
			t.Fatalf("un tenant nuevo tiene que poder estrenar catálogo, got %+v", opts)
		}
	})
}
