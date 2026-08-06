package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// intakeEditMarker es el ancla del formulario de corrección, e intakeEditLockedMarker la del aviso
// que sale en su lugar cuando desde ese estado no se corrige. Si el gate cierra, estas cadenas no
// aparecen en el HTML: eso es lo que distingue un gate server-side de un `display:none`.
const (
	intakeEditMarker       = `id="section-intake-items-edit"`
	intakeEditLockedMarker = `id="section-intake-items-locked"`
)

// Solicitud POR APROBAR con la escena del queso extra (D-041.26): el cliente pidió una hamburguesa
// y anotó «con queso extra» por escrito; el catálogo no tenía el extra, así que el pedido cerró en
// 8.00 con la personalización anotada y sin cobrar.
const detailPendingApproval = `{"id":"in-9","contact_id":"ct-op4","session_id":"s-1",
 "status":"pending_approval","total":8.00,"created_at":"2026-08-05T10:00:00Z",
 "updated_at":"2026-08-05T11:00:00Z",
 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"con queso extra","qty":1,"unit_price":8.00}],
 "revisions":[],"allowed_transitions":["confirmed","cancelled"]}`

// La misma solicitud ya corregida: el queso extra cobrado a 1.00 y el total en 9.00. Es lo que
// devuelve el PUT, y con ello repinta la pantalla sin un segundo GET.
const detailCorrected = `{"id":"in-9","contact_id":"ct-op4","session_id":"s-1",
 "status":"pending_approval","total":9.00,"created_at":"2026-08-05T10:00:00Z",
 "updated_at":"2026-08-05T12:00:00Z",
 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"con queso extra","qty":1,"unit_price":8.00},
          {"sku":"QUESO-EX","label":"Queso extra","customization":"","qty":1,"unit_price":1.00}],
 "revisions":[{"revision_no":1,"kind":"corrected","payload":{},"created_by":"owner",
               "created_at":"2026-08-05T12:00:00Z"}],
 "allowed_transitions":["confirmed","cancelled"]}`

// Solicitud por aprobar CON la línea de envío que pone la plataforma (T4.3): la `_shipping` es una
// fila más de `intake_items` y viaja en el detalle como cualquier otra.
const detailWithShipping = `{"id":"in-9","contact_id":"ct-op4","session_id":"s-1",
 "status":"pending_approval","total":11.50,"created_at":"2026-08-05T10:00:00Z",
 "updated_at":"2026-08-05T11:00:00Z",
 "items":[{"sku":"HAM","label":"Hamburguesa","customization":"","qty":1,"unit_price":8.00},
          {"sku":"_shipping","label":"Envío","customization":"","qty":1,"unit_price":3.50}],
 "revisions":[],"allowed_transitions":["confirmed","cancelled"]}`

// editAPI es la API pública fake de la edición: sirve el detalle en el GET y captura el PUT de
// líneas. Guarda el método y la ruta además del cuerpo porque el contrato de la plataforma es un
// PUT a `/items`, y un cliente que acertara el cuerpo contra la ruta equivocada no valdría de nada.
type editAPI struct {
	server   *httptest.Server
	detail   string
	putCode  int
	putBody  string
	seenBody []byte
	seenPath string
	seenVerb string
	puts     int
}

// newEditAPI monta la API fake con las features dadas.
func newEditAPI(features []string, detail string, putCode int, putBody string) *editAPI {
	api := &editAPI{detail: detail, putCode: putCode, putBody: putBody}
	api.server = intakesAPI(features, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, api.detail)
			return
		}
		api.puts++
		api.seenVerb, api.seenPath = r.Method, r.URL.Path
		api.seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(api.putCode)
		_, _ = io.WriteString(w, api.putBody)
	})
	return api
}

func (a *editAPI) close() { a.server.Close() }

// sentItems decodifica las líneas que el BFF mandó a la plataforma. Se comprueban los VALORES ya
// decodificados y no la cadena JSON: lo que importa es que el precio que llega sea el número que la
// persona quiso, no cómo se serializó.
func (a *editAPI) sentItems(t *testing.T) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(a.seenBody, &body); err != nil {
		t.Fatalf("el cuerpo enviado no era JSON legible (%q): %v", a.seenBody, err)
	}
	return body.Items
}

// editForm arma el formulario con una fila por línea. Las cinco listas van emparejadas, igual que
// las emite la plantilla.
func editForm(rows ...[5]string) url.Values {
	form := url.Values{}
	for _, r := range rows {
		form.Add(intakeEditFieldSKU, r[0])
		form.Add(intakeEditFieldLabel, r[1])
		form.Add(intakeEditFieldCustomization, r[2])
		form.Add(intakeEditFieldQty, r[3])
		form.Add(intakeEditFieldPrice, r[4])
	}
	return form
}

// TestParseIntakePriceReadsWhatAPersonTypes es el test que muerde la parte peligrosa de esta
// pantalla. El precio lo teclea alguien con prisa, y un precio mal LEÍDO no avisa: si «8,50» se
// leyera como 0 se regala el artículo, y si «1.234» se leyera como 1,234 se cobra mil veces menos.
// Las dos cosas se descubren cuando el pedido ya salió.
//
// De ahí la regla que se comprueba aquí: o el número se lee sin ambigüedad, o se RECHAZA con un
// motivo. Ninguna entrada rara puede acabar en un número que nadie escribió — y muy en particular,
// ninguna puede acabar en 0.
func TestParseIntakePriceReadsWhatAPersonTypes(t *testing.T) {
	good := map[string]float64{
		"8,50":      8.50, // coma decimal: la escritura de media Europa y toda Latinoamérica
		"8.50":      8.50, // punto decimal
		"8":         8,
		"0":         0, // el artículo de regalo es legítimo, y es la ÚNICA vía a un 0
		"  8,50  ":  8.50,
		"+8,50":     8.50,
		"8,5":       8.5,
		"1.234,56":  1234.56, // miles con punto, decimal con coma
		"1,234.56":  1234.56, // y al revés: las dos escrituras dan el mismo número
		"1.234.567": 1234567, // el mismo signo repetido solo puede ser de miles
		"-1":        -1,      // se LEE; que un precio negativo no valga lo dice la plataforma
	}
	for raw, want := range good {
		got, msg := parseIntakePrice(raw)
		if msg != "" {
			t.Errorf("%q debía leerse como %v y se rechazó: %s", raw, want, msg)
			continue
		}
		if got != want {
			t.Errorf("%q debía leerse como %v, got %v", raw, want, got)
		}
	}

	// Lo que NO se puede leer con certeza se rechaza. Cada uno con su motivo, porque el operador
	// tiene que poder corregirlo sin adivinar qué le molestó a la pantalla.
	bad := []string{
		"",                 // el campo vacío NO es 0: es un precio que falta
		"   ",              //
		"ocho",             // texto
		"8 50",             // espacio en medio
		"$8.50",            // símbolo de moneda
		"8.50€",            //
		"8,",               // acaba en separador
		",",                //
		"-",                // el signo suelto no es un número
		"1.234",            // ¿mil doscientos treinta y cuatro, o uno con 234 milésimas?
		"0,001",            // el mismo caso, y aquí leerlo mal multiplica por mil
		"8.999",            //
		"8,9999",           // más decimales de los que la pantalla imprime
		"1.23.456",         // miles mal agrupados
		"1.2345,67",        //
		"NaN",              // ParseFloat lo aceptaría; el filtro de caracteres no
		"Inf",              //
		"1e3",              // notación científica: nadie teclea un precio así
		"8,50,25",          // dos decimales no existen
		"--8",              //
		"9999999999999999", // no se rechaza por grande, pero se comprueba que se lee entero
	}
	for _, raw := range bad {
		got, msg := parseIntakePrice(raw)
		if raw == "9999999999999999" {
			if msg != "" || got != 9999999999999999 {
				t.Errorf("%q debía leerse entero, got %v (%s)", raw, got, msg)
			}
			continue
		}
		if msg == "" {
			t.Errorf("%q debía rechazarse y se leyó como %v", raw, got)
		}
		// Y lo que más importa: un rechazo nunca deja pasar un valor. Cero se llega SOLO
		// escribiendo cero.
		if msg != "" && got != 0 {
			t.Errorf("%q se rechazó pero devolvió %v: un rechazo no puede traer valor", raw, got)
		}
	}
}

// TestParseIntakeQtyRejectsWhatIsNotAWholeNumber: «2,5 hamburguesas» no es una cantidad de un
// pedido, y redondearla decidiría por el operador cuál de las dos quiso decir.
func TestParseIntakeQtyRejectsWhatIsNotAWholeNumber(t *testing.T) {
	if got, msg := parseIntakeQty(" 2 "); msg != "" || got != 2 {
		t.Errorf("« 2 » debía leerse como 2, got %v (%s)", got, msg)
	}
	// El 0 y el negativo se LEEN: que la cantidad tenga que ser 1 o más lo dice el dominio de la
	// plataforma, y duplicar esa regla aquí daría dos criterios para lo mismo.
	if got, msg := parseIntakeQty("0"); msg != "" || got != 0 {
		t.Errorf("«0» debía leerse como 0, got %v (%s)", got, msg)
	}
	for _, raw := range []string{"", "dos", "2,5", "2.0", "2 unidades"} {
		if got, msg := parseIntakeQty(raw); msg == "" {
			t.Errorf("%q debía rechazarse como cantidad y se leyó como %v", raw, got)
		}
	}
}

// TestIntakeEditAddsTheCheeseAndCharges es la escena de D-041.26 de punta a punta: el pedido cerró
// en 8.00 con «con queso extra» anotado y sin cobrar; el dueño añade la línea a mano y el pedido
// pasa a 9.00. Se comprueba lo que sale por el cable —verbo, ruta y cuerpo— y lo que vuelve a la
// pantalla.
func TestIntakeEditAddsTheCheeseAndCharges(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()

	form := editForm(
		[5]string{"HAM", "Hamburguesa", "con queso extra", "1", "8,00"},
		[5]string{"QUESO-EX", "Queso extra", "", "1", "1,00"},
		[5]string{"", "", "", "", ""}, // la fila de alta que quedó sin rellenar no viaja
	)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la corrección debía responder 200, got %d", rec.Code)
	}

	// El contrato de la plataforma es un PUT a /items con el conjunto COMPLETO de líneas.
	if api.seenVerb != http.MethodPut || api.seenPath != "/api/v1/intakes/in-9/items" {
		t.Errorf("debía llamarse PUT /api/v1/intakes/in-9/items, got %s %s", api.seenVerb, api.seenPath)
	}
	items := api.sentItems(t)
	if len(items) != 2 {
		t.Fatalf("debían viajar 2 líneas (la vacía no cuenta), got %d: %s", len(items), api.seenBody)
	}
	if items[1]["sku"] != "QUESO-EX" || items[1]["label"] != "Queso extra" {
		t.Errorf("la segunda línea debía ser el queso extra, got %v", items[1])
	}
	// El precio tecleado con coma llega como número, y como EL número: ni 0 ni 100.
	if items[1]["unit_price"] != 1.0 {
		t.Errorf("«1,00» debía viajar como 1, got %v", items[1]["unit_price"])
	}
	if items[0]["unit_price"] != 8.0 || items[0]["qty"] != 1.0 {
		t.Errorf("la línea que no se tocó debía viajar igual, got %v", items[0])
	}
	// La personalización que escribió el cliente sobrevive a la corrección del dueño: es la razón
	// por la que se está cobrando el extra.
	if items[0]["customization"] != "con queso extra" {
		t.Errorf("la personalización debía viajar intacta, got %v", items[0]["customization"])
	}

	// Y la pantalla repinta con lo que devolvió el PUT: el total nuevo, sin un segundo GET.
	out := rec.Body.String()
	if !strings.Contains(out, "total · 9.00") {
		t.Error("la ficha debía pintar el total que devolvió la plataforma (9.00)")
	}
	if !strings.Contains(out, "Queso extra") {
		t.Error("la línea añadida debía aparecer en la tabla")
	}
	if !strings.Contains(out, "un total de 9.00") {
		t.Error("la confirmación debía decir en cuánto queda el pedido")
	}
}

// TestIntakeEditNeverTouchesTheShippingLine: la línea `_shipping` la pone la plataforma y esta
// puerta no la toca. No se ofrece en el formulario y NO viaja en el cuerpo — si viajara, la
// plataforma rechazaría la edición entera por su prefijo reservado, así que el dueño no podría
// corregir NINGUNA solicitud que llevara envío.
func TestIntakeEditNeverTouchesTheShippingLine(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailWithShipping, http.StatusOK, detailWithShipping)
	defer api.close()
	router := NewRouter(authTestCfg(api.server.URL))

	// Primero la pantalla: el envío se VE en la ficha, pero no se ofrece como fila editable.
	out := getWithCookie(router, "/intakes/in-9", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, "<td>_shipping</td>") {
		t.Error("la ficha debía seguir enseñando la línea de envío")
	}
	if strings.Contains(out, `value="_shipping"`) {
		t.Error("la línea de envío no puede ofrecerse como campo editable del formulario")
	}
	if !strings.Contains(out, "La línea de envío la pone wApp") {
		t.Error("la pantalla debía decir que el envío no se edita aquí y no se pierde")
	}

	// Y el guardado: solo viaja la línea de cliente.
	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	if rec := postFormWithCookie(router, "/intakes/in-9/items", form, validSessionCookie(t)); rec.Code != http.StatusOK {
		t.Fatalf("la corrección debía responder 200, got %d", rec.Code)
	}
	for _, item := range api.sentItems(t) {
		if sku, _ := item["sku"].(string); strings.HasPrefix(sku, "_") {
			t.Errorf("una línea del sistema no puede viajar en el cuerpo, got %q", sku)
		}
	}
	if n := len(api.sentItems(t)); n != 1 {
		t.Errorf("debía viajar solo la línea de cliente, got %d", n)
	}
}

// TestIntakeEditRefusesAnUnreadablePriceWithoutCalling es el otro lado del mismo peligro: cuando el
// precio no se puede leer NO se llama a la plataforma. Un cuerpo con el precio puesto a 0 «porque no
// se entendía» sería una corrección que el dueño no pidió, aplicada sobre el pedido de un cliente.
func TestIntakeEditRefusesAnUnreadablePriceWithoutCalling(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()

	form := editForm(
		[5]string{"HAM", "Hamburguesa", "con queso extra", "1", "8,00"},
		[5]string{"QUESO-EX", "Queso extra", "", "uno", "ocho cincuenta"},
	)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("una línea ilegible debía dar 400, got %d", rec.Code)
	}
	if api.puts != 0 {
		t.Fatalf("no debía llamarse a la plataforma con una línea que no se sabe leer, hubo %d PUT", api.puts)
	}

	out := rec.Body.String()
	// Los dos defectos de la MISMA línea se cuentan juntos: quien llena un formulario no puede
	// descubrir sus errores de uno en uno a base de reintentos.
	if !strings.Contains(out, "«ocho cincuenta» no es un precio") {
		t.Error("la pantalla debía decir qué le pasa al precio")
	}
	if !strings.Contains(out, "«uno» no es una cantidad") {
		t.Error("la pantalla debía decir qué le pasa a la cantidad")
	}
	if !strings.Contains(out, "Línea 2") {
		t.Error("el defecto debía señalar la línea del formulario en la que está")
	}
	// Y lo tecleado no se tira: el operador corrige encima de su trabajo, no lo reescribe.
	if !strings.Contains(out, `value="ocho cincuenta"`) {
		t.Error("el formulario debía repintar lo que el operador escribió")
	}
	if !strings.Contains(out, "No se guardó nada") {
		t.Error("la pantalla debía dejar claro que no se guardó nada")
	}
}

// TestIntakeEditAmbiguousPriceIsRefused separa el caso que parece inofensivo: «1.234» no se rechaza
// por raro sino porque tiene dos lecturas que se diferencian en tres ceros, y ninguna pantalla
// puede elegir por el dueño cuál de las dos cobra.
func TestIntakeEditAmbiguousPriceIsRefused(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "1.234"})
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest || api.puts != 0 {
		t.Fatalf("un precio ambiguo debía dar 400 sin llamar a la plataforma, got %d con %d PUT", rec.Code, api.puts)
	}
	out := rec.Body.String()
	// El motivo enseña las DOS lecturas y cómo desempatarlas: sin eso, el operador vuelve a
	// escribir lo mismo.
	for _, want := range []string{"no se sabe si «1.234»", "1234", "1,234"} {
		if !strings.Contains(out, want) {
			t.Errorf("el motivo debía contener %q", want)
		}
	}
}

// TestIntakeEditRemovesTheLineByIndex: quitar una línea es guardar el conjunto sin ella, y la fila
// se identifica por su ÍNDICE y no por su sku — si el operador corrigió el sku antes de pulsar
// «Quitar», la fila que quiere quitar sigue siendo esa.
func TestIntakeEditRemovesTheLineByIndex(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailPendingApproval)
	defer api.close()

	form := editForm(
		[5]string{"HAM", "Hamburguesa", "", "1", "8,00"},
		[5]string{"VELA-TEXTO-YA-CAMBIADO", "Velas", "", "2", "1,50"},
	)
	form.Set(intakeEditFieldRemove, "1")
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("quitar una línea debía responder 200, got %d", rec.Code)
	}
	items := api.sentItems(t)
	if len(items) != 1 || items[0]["sku"] != "HAM" {
		t.Errorf("debía quedar solo la primera línea, got %v", items)
	}
}

// TestIntakeEditWithoutFeatureOffersNothing: el gate `cart_basic` es el mismo con el que la
// plataforma corta las cuatro rutas de la bandeja. Sin la feature no se emite el formulario en el
// HTML —no se esconde con CSS— y el POST ni siquiera gasta el viaje: la plataforma respondería 403.
func TestIntakeEditWithoutFeatureOffersNothing(t *testing.T) {
	api := newEditAPI([]string{"catalog_import"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()
	router := NewRouter(authTestCfg(api.server.URL))

	out := getWithCookie(router, "/intakes/in-9", validSessionCookie(t)).Body.String()
	for _, forbidden := range []string{intakeEditMarker, intakeEditLockedMarker, intakeEditFieldSKU, "/items"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("sin `cart_basic` no debía emitirse %q en el HTML", forbidden)
		}
	}

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	rec := postFormWithCookie(router, "/intakes/in-9/items", form, validSessionCookie(t))
	if rec.Code != http.StatusForbidden {
		t.Errorf("sin la feature el guardado debía responder 403, got %d", rec.Code)
	}
	if api.puts != 0 {
		t.Errorf("sin la feature no debía llamarse a la plataforma, hubo %d PUT", api.puts)
	}
}

// TestIntakeEditOnlyWhereItCanWork: el formulario se emite SOLO desde el estado que admite la
// corrección. Desde un `confirmed` —que sí puede llegar ahí— se enseña el camino y ni un botón;
// eso lo decide `allowed_transitions` de la plataforma, no una tabla de esta pantalla.
func TestIntakeEditOnlyWhereItCanWork(t *testing.T) {
	// (a) Por aprobar: el formulario está.
	editable := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, "")
	defer editable.close()
	out := getWithCookie(NewRouter(authTestCfg(editable.server.URL)), "/intakes/in-9", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, intakeEditMarker) {
		t.Error("desde «por aprobar» debía ofrecerse el formulario de corrección")
	}
	if !strings.Contains(out, `action="/intakes/in-9/items"`) {
		t.Error("el formulario debía apuntar a la ruta de líneas")
	}

	// (b) Confirmado, con `pending_approval` entre sus destinos: se enseña el camino, sin botón.
	confirmed := newEditAPI([]string{"cart_basic"}, strings.Replace(
		strings.Replace(detailPendingApproval, `"status":"pending_approval"`, `"status":"confirmed"`, 1),
		`"allowed_transitions":["confirmed","cancelled"]`, `"allowed_transitions":["pending_approval","cancelled"]`, 1),
		http.StatusOK, "")
	defer confirmed.close()
	out = getWithCookie(NewRouter(authTestCfg(confirmed.server.URL)), "/intakes/in-9", validSessionCookie(t)).Body.String()
	if strings.Contains(out, intakeEditMarker) {
		t.Error("desde «confirmado» no puede ofrecerse un formulario que la plataforma va a rechazar")
	}
	if !strings.Contains(out, intakeEditLockedMarker) {
		t.Error("a una solicitud que PUEDE llegar a corregirse hay que enseñarle el camino")
	}
	if !strings.Contains(out, "por aprobar") {
		t.Error("el aviso debía nombrar el estado desde el que sí se corrige")
	}
	if strings.Contains(out, `action="/intakes/in-9/items"`) {
		t.Error("el aviso no puede traer un formulario escondido")
	}

	// (c) Terminal: ni formulario ni promesa. Sin destinos no hay camino que enseñar, y prometerlo
	// sería exactamente «ofrecer lo imposible» (T4.6).
	terminal := newEditAPI([]string{"cart_basic"}, strings.Replace(
		strings.Replace(detailPendingApproval, `"status":"pending_approval"`, `"status":"abandoned"`, 1),
		`"allowed_transitions":["confirmed","cancelled"]`, `"allowed_transitions":[]`, 1),
		http.StatusOK, "")
	defer terminal.close()
	out = getWithCookie(NewRouter(authTestCfg(terminal.server.URL)), "/intakes/in-9", validSessionCookie(t)).Body.String()
	for _, forbidden := range []string{intakeEditMarker, intakeEditLockedMarker} {
		if strings.Contains(out, forbidden) {
			t.Errorf("un estado terminal no debía emitir %q", forbidden)
		}
	}
}

// TestIntakeEditShowsPlatformDefectsOnTheRightLine: los defectos que manda la plataforma vienen
// indexados sobre la lista ENVIADA, no sobre el formulario. Como las filas en blanco no se mandan,
// traducirlos es obligatorio: sin la traducción, el rechazo de la línea 3 señalaría la 2 y mandaría
// a corregir la línea equivocada.
func TestIntakeEditShowsPlatformDefectsOnTheRightLine(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusBadRequest,
		`{"error":"invalid_items","errors":[
		   {"index":1,"field":"unit_price","message":"el precio no puede ser negativo (0 sí: es un artículo de regalo)"}]}`)
	defer api.close()

	form := editForm(
		[5]string{"HAM", "Hamburguesa", "", "1", "8,00"},
		[5]string{"", "", "", "", ""}, // fila en blanco EN MEDIO: no viaja, y desplaza los índices
		[5]string{"QUESO-EX", "Queso extra", "", "1", "-1,00"},
	)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("el rechazo de la plataforma debía llegar como 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	// El índice 1 del cuerpo es la fila 3 del formulario.
	if !strings.Contains(out, "Línea 3 · precio:") {
		t.Error("el defecto debía señalar la fila 3 del formulario, que es la que produjo el índice 1")
	}
	if strings.Contains(out, "Línea 2 · precio:") {
		t.Error("el defecto no puede señalar la fila en blanco que ni siquiera viajó")
	}
	// El motivo de la plataforma viaja VERBATIM: está escrito para el dueño del negocio y
	// reescribirlo aquí sería mantener dos redacciones que acabarían discrepando.
	if !strings.Contains(out, "el precio no puede ser negativo") {
		t.Error("el motivo de la plataforma debía llegar tal cual a la pantalla")
	}
	if !strings.Contains(out, `value="-1,00"`) {
		t.Error("lo tecleado debía repintarse para poder corregirlo")
	}
}

// TestIntakeEditNotEditableTellsWhereItIsEditable: el 422 llega cuando otro operador movió la
// solicitud entre que se pintó el formulario y se pulsó «Guardar». La pantalla no adivina el ciclo
// de vida: dice lo que dijo la plataforma, incluidos los estados desde los que sí se edita.
func TestIntakeEditNotEditableTellsWhereItIsEditable(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusUnprocessableEntity,
		`{"error":"not_editable","status":"confirmed","editable_in":["pending_approval"]}`)
	defer api.close()

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("un estado que no admite corrección debía dar 422, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "está en «confirmado»") {
		t.Error("el aviso debía decir dónde está la solicitud, con su nombre de negocio")
	}
	if !strings.Contains(out, "«por aprobar»") {
		t.Error("el aviso debía decir desde dónde SÍ se corrige, traducido")
	}
	if strings.Contains(out, "pending_approval") {
		t.Error("la clave cruda del wire no debe llegar a la pantalla")
	}
}

// TestIntakeEditConflictSaysNothingWasSaved: el 409 es otro operador adelantándose. Lo que el
// operador necesita saber es que su corrección NO entró — un aviso ambiguo le haría creer que sí y
// no volvería a intentarlo.
func TestIntakeEditConflictSaysNothingWasSaved(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusConflict, `{"error":"conflicto"}`)
	defer api.close()

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusConflict {
		t.Fatalf("el 409 de la plataforma debía llegar como 409, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no se guardó nada") {
		t.Error("el aviso debía dejar claro que la corrección no entró")
	}
}

// TestIntakeEditEmptiesEveryLine: quitar TODAS las líneas es una edición legítima, y tiene que
// viajar como lista vacía. Si viajara como `null`, la plataforma lo leería como «no mandaste la
// clave» y contestaría un 400 que el operador no podría entender.
func TestIntakeEditEmptiesEveryLine(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailPendingApproval)
	defer api.close()

	form := editForm([5]string{"", "", "", "", ""})
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("vaciar las líneas debía responder 200, got %d", rec.Code)
	}
	if !strings.Contains(string(api.seenBody), `"items":[]`) {
		t.Errorf("la lista vacía debía viajar como [], got %s", api.seenBody)
	}
}

// TestIntakeEditRequiresCSRF: el formulario que escribe pasa por el mismo camino que los demás
// formularios del BFF. Sin token no llega al handler, y por tanto tampoco a la plataforma.
func TestIntakeEditRequiresCSRF(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/intakes/in-9/items", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(validSessionCookie(t))
	NewRouter(authTestCfg(api.server.URL)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("un POST sin token CSRF debía dar 403, got %d", rec.Code)
	}
	if api.puts != 0 {
		t.Errorf("sin CSRF no debía llegarse a la plataforma, hubo %d PUT", api.puts)
	}
}

// TestIntakeEditRejectsMismatchedForm: los cinco arrays del formulario tienen que venir
// emparejados. Si no lo están —envío truncado o manipulado— no se adivina qué precio va con qué
// artículo: se rechaza y se pide recargar, porque guardar una mezcla sería peor que no guardar.
func TestIntakeEditRejectsMismatchedForm(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, detailCorrected)
	defer api.close()

	form := editForm([5]string{"HAM", "Hamburguesa", "", "1", "8,00"})
	form.Add(intakeEditFieldPrice, "3,00") // un precio de más, sin su fila
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un formulario descuadrado debía dar 400, got %d", rec.Code)
	}
	if api.puts != 0 {
		t.Errorf("no debía llamarse a la plataforma con un formulario descuadrado, hubo %d PUT", api.puts)
	}
	if !strings.Contains(rec.Body.String(), "llegó incompleto") {
		t.Error("la pantalla debía pedir recargar en vez de guardar una mezcla")
	}
}

// TestIntakeEditKeepsPricesLegible: la ficha imprime «8.00» y el formulario tiene que decir lo
// mismo. Dos cifras distintas para el mismo precio delante de los ojos es la clase de detalle que
// acaba en una corrección que nadie quiso hacer.
func TestIntakeEditKeepsPricesLegible(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailPendingApproval, http.StatusOK, "")
	defer api.close()

	out := getWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9", validSessionCookie(t)).Body.String()
	if !strings.Contains(out, `value="8.00"`) {
		t.Error("el precio del formulario debía imprimirse con los dos decimales de la ficha")
	}
	if !strings.Contains(out, `value="con queso extra"`) {
		t.Error("la personalización debía llegar al formulario para poder corregirla")
	}
	// Las filas de alta vienen vacías y sin botón de quitar: no hay nada que quitar en ellas.
	if n := strings.Count(out, `name="remove"`); n != 1 {
		t.Errorf("solo la línea con contenido debía ofrecer «Quitar», got %d", n)
	}
	if strings.Contains(out, "<script") {
		t.Error("la pantalla no debe introducir JS (ADR-0035: server-side, cero framework)")
	}
}

// TestIntakeEditForwardsAShippingSKUAndShowsTheRefusal cubre el hueco que deja el test anterior: la
// pantalla no OFRECE la línea de envío, pero un cliente viejo, una pestaña cacheada o alguien
// tecleando el sku a mano pueden mandarla igualmente. Qué pasa entonces es una decisión, y ésta es
// la que se tomó: el BFF NO la filtra en silencio.
//
// Filtrarla parecería más amable y sería peor. Quien escribió `_shipping` vería su línea
// desaparecer sin explicación y volvería a escribirla; y sobre todo, el BFF estaría decidiendo qué
// líneas son del sistema, que es justo lo que no le toca. La autoridad es la plataforma —rechaza el
// prefijo reservado en la validación y tiene detrás un índice único parcial que impide un segundo
// envío—, así que la línea viaja, vuelve rechazada con su motivo, y ese motivo se lee en pantalla.
//
// Lo que sí está garantizado en las dos capas: no se duplica el envío ni se convierte otra línea en
// él. La edición es todo-o-nada, así que un cuerpo con `_shipping` no escribe NADA.
func TestIntakeEditForwardsAShippingSKUAndShowsTheRefusal(t *testing.T) {
	api := newEditAPI([]string{"cart_basic"}, detailWithShipping, http.StatusBadRequest,
		`{"error":"invalid_items","errors":[{"index":1,"field":"sku",
		  "message":"el sku empieza por _, que está reservado para las líneas que pone wApp (el envío): esas no se editan por aquí"}]}`)
	defer api.close()

	form := editForm(
		[5]string{"HAM", "Hamburguesa", "", "1", "8,00"},
		[5]string{"_shipping", "Envío", "", "1", "9,99"}, // un segundo envío, o el mismo repisado
	)
	rec := postFormWithCookie(NewRouter(authTestCfg(api.server.URL)), "/intakes/in-9/items", form, validSessionCookie(t))

	// Viaja tal cual: el BFF no se inventa un filtro que le corresponde a la plataforma.
	items := api.sentItems(t)
	if len(items) != 2 || items[1]["sku"] != "_shipping" {
		t.Fatalf("la línea debía viajar sin filtrar para que la plataforma la juzgue, got %v", items)
	}
	// Y vuelve rechazada, con el motivo delante del operador.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("la plataforma rechaza el prefijo reservado: debía llegar como 400, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "está reservado para las líneas que pone wApp") {
		t.Error("el motivo de la plataforma debía leerse en pantalla")
	}
	if !strings.Contains(out, "Línea 2 · SKU:") {
		t.Error("el defecto debía señalar la línea del formulario y el campo, traducido")
	}
}
