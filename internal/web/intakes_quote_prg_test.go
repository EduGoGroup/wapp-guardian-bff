package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ════════════════════════════════════════════════════════════════════════════
// Plan 047 · T3.5 — POST-Redirect-GET en la sugerencia de presupuesto
// ════════════════════════════════════════════════════════════════════════════
//
// Lo que estos tests vigilan NO es la estética de la barra de direcciones: es que un F5 encima de
// esta pantalla deje de costar 20-40 s de modelo. Por eso el aserto central no mira el HTML, mira
// CUÁNTAS VECES se llamó al cloud.

// quoteCookieOf saca del recorder la cookie efímera de la cotización, si la puso.
func quoteCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == quoteCookieName {
			return c
		}
	}
	return nil
}

// getWithCookies es getWithCookie con más de una cookie: el GET que sigue al redirect lleva la de
// sesión Y la efímera, que es justo lo que manda el navegador.
func getWithCookies(router http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	router.ServeHTTP(rec, req)
	return rec
}

// postQuoteAndFollow hace lo que hace el navegador: pide la sugerencia, comprueba que le contestan
// con un redirect, y sigue el Location llevándose la cookie efímera. Devuelve las dos respuestas.
//
// Existe para que los tests de T2.4 —que se escribieron cuando esta ruta pintaba sobre el POST—
// sigan afirmando LO MISMO sobre la pantalla sin tener que reescribir lo que comprueban. Sus asertos
// no se rebajaron: se movieron a la página que ahora los muestra.
func postQuoteAndFollow(t *testing.T, router http.Handler, session *http.Cookie) (
	*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()
	post := postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion", url.Values{}, session)
	if post.Code != http.StatusSeeOther {
		t.Fatalf("la sugerencia debía redirigir (303) y respondió %d", post.Code)
	}
	destino := post.Header().Get("Location")
	if destino != "/intakes/in-ambar" {
		t.Fatalf("el redirect debía ir al detalle de la solicitud, y fue a %q", destino)
	}
	return post, getWithCookies(router, destino, session, quoteCookieOf(post))
}

// loQueElNavegadorSigueTeniendo simula el tarro de cookies: si la respuesta NO retiró la cookie, el
// navegador la conserva y la manda en la petición siguiente; si la retiró, no.
//
// 🔴 ESTE HELPER ES LO QUE HACE QUE LA MUTACIÓN DUELA. Un test que simplemente omitiera la cookie en
// la recarga se pondría verde con un GET que no la consume: no habría cookie que enviar porque el
// TEST decidió no enviarla, no porque el servidor la hubiera borrado. Aquí la decisión la toma la
// respuesta, que es quien la toma de verdad.
func loQueElNavegadorSigueTeniendo(rec *httptest.ResponseRecorder, tenia *http.Cookie) *http.Cookie {
	if tenia == nil {
		return nil
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == tenia.Name && c.MaxAge < 0 {
			return nil // el servidor la retiró
		}
	}
	return tenia
}

// TestLaSugerenciaRedirigeEnVezDePintarSobreElPOST es el punto (1) del criterio de T3.5: tras el POST
// hay un 3xx y la URL final es la del detalle, no la de la acción.
func TestLaSugerenciaRedirigeEnVezDePintarSobreElPOST(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"Hola Ambar 💛 Total 65.50","source":"llm"}`, &seen)
	defer api.Close()

	post, get := postQuoteAndFollow(t, NewRouter(authTestCfg(api.URL)), validSessionCookie(t))

	// La respuesta del POST no pinta pantalla: solo redirige. Si algún día volviera a traer el HTML,
	// el F5 volvería a costar una inferencia aunque el 303 siguiera ahí.
	if strings.Contains(post.Body.String(), intakeQuoteMarker) {
		t.Error("el POST no debe pintar la pantalla: para eso está el redirect")
	}
	// Y la cookie efímera va acotada a ESA solicitud, no al sitio entero.
	cookie := quoteCookieOf(post)
	if cookie == nil {
		t.Fatal("el POST debía dejar la cotización en la cookie efímera")
	}
	if cookie.Path != "/intakes/in-ambar" {
		t.Errorf("la cookie debía ir acotada al detalle de la solicitud, y su Path es %q", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Error("la cookie de la cotización tiene que ser HttpOnly")
	}
	// El texto llega a la pantalla del GET, con su origen y su aviso: lo que T2.4 exigía, en la
	// página que ahora lo enseña.
	out := get.Body.String()
	if !strings.Contains(out, "Hola Ambar 💛 Total 65.50") {
		t.Error("el texto sugerido tenía que sobrevivir al redirect")
	}
	if !strings.Contains(out, intakeQuoteOriginMarker) || !strings.Contains(out, "Origen: LLM") {
		t.Error("el ORIGEN tenía que sobrevivir al redirect: sin él no se distingue quién redactó")
	}
	if !strings.Contains(out, "NO SE HA ENVIADO NADA") {
		t.Error("el aviso tenía que sobrevivir al redirect")
	}
	assertNothingWasWritten(t, seen)
}

// TestRecargarTrasLaSugerenciaNoVuelveAPedirlaAlModelo es EL criterio de T3.5 —el punto (2)— y el
// único que mide lo que la tarea existe para arreglar: el coste.
//
// 🔴 No mira el HTML. Cuenta las llamadas que el cloud vio. Un test que solo comprobara el 303
// pasaría igual el día que alguien dejara el redirect y volviera a pedir la sugerencia en el GET.
func TestRecargarTrasLaSugerenciaNoVuelveAPedirlaAlModelo(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"Hola Ambar 💛 Total 65.50","source":"llm"}`, &seen)
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	session := validSessionCookie(t)
	post, get := postQuoteAndFollow(t, router, session)

	// El F5: la MISMA URL otra vez. El navegador ya no manda la cookie efímera —el GET anterior la
	// borró—, así que se simula sin ella.
	if !cookieFueBorrada(get, quoteCookieName) {
		t.Fatal("el GET tenía que borrar la cookie efímera al consumirla")
	}
	getWithCookies(router, "/intakes/in-ambar", session,
		loQueElNavegadorSigueTeniendo(get, quoteCookieOf(post)))

	pedidas := 0
	for _, call := range seen {
		if strings.Contains(call, "quote-suggestion") {
			pedidas++
		}
	}
	if pedidas != 1 {
		t.Errorf("la sugerencia se pidió %d veces al cloud; el POST la pide UNA y las recargas ninguna "+
			"(llamadas vistas: %v)", pedidas, seen)
	}
}

// TestElTextoSobreviveUnaSolaVezAlRedirect es el punto (3): el flash es de UN uso. La segunda lectura
// de la misma pantalla ya no lo trae.
//
// 🔴 Es el test que la MUTACIÓN de T3.5 tiene que derribar: si el GET deja de consumir la cookie
// —o la repone—, el texto reaparece en la recarga y esto se pone en rojo.
func TestElTextoSobreviveUnaSolaVezAlRedirect(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"Hola Ambar 💛 Total 65.50","source":"llm"}`, &seen)
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	session := validSessionCookie(t)
	post, primera := postQuoteAndFollow(t, router, session)

	if !strings.Contains(primera.Body.String(), "Hola Ambar 💛 Total 65.50") {
		t.Fatal("la PRIMERA lectura tras el redirect sí trae el texto")
	}
	if !cookieFueBorrada(primera, quoteCookieName) {
		t.Error("el GET que consume el flash tiene que retirar la cookie, no dejarla viva 60 s")
	}

	// La segunda lectura lleva EXACTAMENTE lo que el navegador seguiría teniendo tras la primera.
	segunda := getWithCookies(router, "/intakes/in-ambar", session,
		loQueElNavegadorSigueTeniendo(primera, quoteCookieOf(post)))
	if strings.Contains(segunda.Body.String(), "Hola Ambar 💛 Total 65.50") {
		t.Error("el texto NO puede reaparecer en la recarga: el flash es de un solo uso")
	}
	// Y la pantalla sigue siendo la de la solicitud, no un error.
	if segunda.Code != http.StatusOK {
		t.Errorf("la recarga tenía que pintar el detalle igual, y respondió %d", segunda.Code)
	}
	if strings.Contains(segunda.Body.String(), intakeQuoteOriginMarker) {
		t.Error("sin sugerencia recién pedida no se pinta origen: diría quién redactó un texto que " +
			"el modelo no ha visto")
	}
}

// TestLaCotizacionDeUnaSolicitudNoAterrizaEnOtra vigila la SEGUNDA cerradura —el id dentro del
// sobre—, la que no depende de que el navegador respete el Path.
//
// 🔴 Lo que evita es concreto y caro: pintar los precios de la solicitud A en la pantalla de B, que
// es la que alguien está a punto de responder.
func TestLaCotizacionDeUnaSolicitudNoAterrizaEnOtra(t *testing.T) {
	var seen []string
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"PRECIOS DE LA SOLICITUD A","source":"llm"}`, &seen)
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	session := validSessionCookie(t)
	post := postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion", url.Values{}, session)
	if post.Code != http.StatusSeeOther {
		t.Fatalf("la sugerencia debía redirigir, y respondió %d", post.Code)
	}

	// La cookie de in-ambar, presentada en la pantalla de OTRA solicitud. El navegador no lo haría
	// (el Path lo impide), y por eso se fuerza aquí: lo que se prueba es que el servidor tampoco lo
	// acepta si llega.
	otra := getWithCookies(router, "/intakes/in-otra", session, quoteCookieOf(post))
	if strings.Contains(otra.Body.String(), "PRECIOS DE LA SOLICITUD A") {
		t.Error("la cotización de una solicitud NO puede pintarse en la pantalla de otra")
	}
}

// TestUnaCotizacionQueNoCabeEnLaCookieSePintaSobreElPOST: el mecanismo que T3.5 reutiliza nació para
// secretos cortos y no tiene tope. Si el texto no cabe, se degrada al comportamiento de siempre
// —pintar sobre el POST— en vez de redirigir a una pantalla donde el texto YA NO ESTÁ.
//
// 🔑 Prioridad declarada: no perder el texto manda sobre ahorrar la tecla. Perder una cotización que
// costó 40 s de modelo es peor que un F5 caro.
func TestUnaCotizacionQueNoCabeEnLaCookieSePintaSobreElPOST(t *testing.T) {
	var seen []string
	largo := strings.Repeat("Ñ", maxQuoteCookieValue) // multibyte, para que el tope se mida en BYTES
	api := quoteAPI(t, []string{"cart_basic", "llm_intake"}, http.StatusOK,
		`{"rendered_text":"`+largo+`","source":"llm"}`, &seen)
	defer api.Close()

	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-ambar/quote-suggestion",
		url.Values{}, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("sin sitio en la cookie hay que pintar sobre el POST (200), y respondió %d", rec.Code)
	}
	if quoteCookieOf(rec) != nil {
		t.Error("no se pone una cookie que el navegador va a descartar en silencio")
	}
	if !strings.Contains(rec.Body.String(), largo) {
		t.Error("el texto NO se pierde: si no cabe en la cookie, se pinta aquí mismo")
	}
	if !strings.Contains(rec.Body.String(), "NO SE HA ENVIADO NADA") {
		t.Error("el aviso es el mismo por los dos caminos")
	}
}

// cookieFueBorrada dice si la respuesta retira esa cookie (MaxAge negativo), que es el gesto con el
// que TakeOneTimeCookie la consume.
func cookieFueBorrada(rec *httptest.ResponseRecorder, name string) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge < 0 {
			return true
		}
	}
	return false
}
