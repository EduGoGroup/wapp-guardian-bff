package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// La aduana del BFF: todo POST protegido REDIRIGE, no responde 4xx
// ---------------------------------------------------------------------------

// Este fichero existe para RESCATAR un aserto que estaba a punto de morir con su fichero. El único
// sitio del paquete que probaba «un POST protegido SIN cookie de sesión responde 303 hacia /login»
// era `TestEditorRoutesProtected` en `editor_test.go`, y ese fichero se va entero cuando el Plan 047
// retire del BFF las pantallas de flujos y disparadores. El equivalente para GET sí estaba cubierto
// en varios sitios (auth, portada, variables); el de POST, en ninguno más.
//
// 🔴 Y es un invariante de ADUANA, que es justo lo que el BFF conserva para siempre: la diferencia
// entre 303→/login y un 401/403 no es cosmética. Un POST de formulario que responde 4xx deja al
// usuario ante una página muerta con lo que acababa de teclear perdido; el 303 lo lleva a la puerta.
// Se escribe AHORA, antes de borrar, porque en una retirada anterior de este mismo plan un aserto sin
// otro dueño (el `<th>Perfil</th>`) estuvo a punto de irse por el desagüe sin que nadie lo notara.
//
// Se prueba la REGLA sobre la tabla de rutas y no un puñado de rutas por su nombre: los tres frentes
// que vienen (flujos/disparadores, la bandeja de solicitudes, el import de catálogo) se llevan rutas
// concretas, así que cualquier lista escrita a mano nace con fecha de caducidad. Recorriendo
// `router.Routes()` el test cubre lo que HAYA el día que se ejecute, y cubre gratis lo que se añada.

// rutasPOSTExentasDeLaAduana son los POST que legítimamente NO redirigen a /login sin sesión, con el
// motivo de cada uno. Es una lista EXPLÍCITA a propósito: un POST nuevo que no redirija pone el test
// en rojo y obliga a justificarlo aquí: es imposible eximirse en silencio.
var rutasPOSTExentasDeLaAduana = map[string]string{
	// Es la puerta misma: no cuelga del AuthMiddleware y sin sesión es su caso NORMAL. Mandarlo a
	// /login sería mandarlo a sí mismo.
	"/login": "es el propio login: público por definición, y sin sesión repinta el formulario",
	// Alta de cuenta pública (Plan 056 · T3.5): quien la usa aún no tiene credenciales que ofrecer.
	"/signup": "alta pública: exigir sesión para pedir una cuenta sería un candado sobre la llave",
	// Cierra sesión y redirige al login, pero lo hace como ruta PÚBLICA (fuera del grupo protegido) y
	// por su propio handler: no acredita nada sobre la aduana, así que no cuenta como cobertura.
	"/logout": "ruta pública del plano de autenticación: su 303 lo escribe DoLogout, no el AuthMiddleware",
}

// rutaConcretaDeParametros convierte el patrón que guarda gin ("/intakes/:id/status") en una URL
// pedible. El valor del parámetro da igual: la aduana corta ANTES del handler, así que nunca se lee.
func rutaConcretaDeParametros(patron string) string {
	partes := strings.Split(patron, "/")
	for i, p := range partes {
		if strings.HasPrefix(p, ":") || strings.HasPrefix(p, "*") {
			partes[i] = "x"
		}
	}
	return strings.Join(partes, "/")
}

// postSinCSRFNiSesion pide un POST PELADO: sin cookie de sesión y sin token CSRF. Existe para
// distinguir QUIÉN emite el 303 del test de abajo. El helper compartido `postFormWithCookie`
// (`helpers_test.go:62`) siembra el token CSRF por su cuenta, así que una respuesta obtenida con él
// atraviesa DOS guardas —CSRF primero, aduana de sesión después— y por sí sola no dice cuál contestó.
// Un 303 a /login emitido por la guarda CSRF dejaría el test verde acreditando algo distinto de lo
// que dice acreditar.
func postSinCSRFNiSesion(router http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(rec, req)
	return rec
}

// TestTodoPOSTProtegidoSinSesionRedirigeAlLogin es el aserto rescatado, convertido en regla: TODOS los
// POST del router que no son de aduana responden 303 con Location /login cuando no hay cookie de
// sesión. Ni 401, ni 403, ni 200 repintando.
//
// El contador `cubiertos` no es decorativo: sin él, el día que la última ruta de negocio se mude el
// bucle recorrería cero rutas y el test seguiría VERDE sin comprobar nada —un criterio que un cero
// satisface—. Con él, un test que deja de tener material lo dice en voz alta.
func TestTodoPOSTProtegidoSinSesionRedirigeAlLogin(t *testing.T) {
	// api.invalid: si algún POST llegara a su handler en vez de morir en la aduana, la llamada a la
	// API pública fallaría de forma ruidosa en vez de pasar por buena.
	router := NewRouter(authTestCfg("http://api.invalid"))

	vistasExentas := map[string]bool{}
	cubiertos := 0
	for _, r := range router.Routes() {
		if r.Method != http.MethodPost {
			continue
		}
		if _, exenta := rutasPOSTExentasDeLaAduana[r.Path]; exenta {
			vistasExentas[r.Path] = true
			continue
		}
		cubiertos++

		ruta := rutaConcretaDeParametros(r.Path)

		// PRIMERO, la prueba de autoría: la misma petición sin token CSRF muere en la guarda anterior
		// y NO redirige (hoy contesta 403). Sin esta comprobación, el 303 de abajo podría venir de
		// esa guarda y no de la aduana de sesión, y el test mediría otra cosa creyendo medir ésta.
		if pelado := postSinCSRFNiSesion(router, ruta); pelado.Code == http.StatusSeeOther &&
			pelado.Header().Get("Location") == "/login" {
			t.Fatalf("POST %s sin token CSRF ya responde 303 a /login: la guarda CSRF y la aduana de "+
				"sesión contestan lo mismo, así que el aserto de abajo dejó de poder distinguirlas. "+
				"Este test ya no acredita que el 303 lo emita el AuthMiddleware", ruta)
		}

		rec := postFormWithCookie(router, ruta, url.Values{}, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("POST %s (patrón %s) sin cookie de sesión debía redirigir 303 a /login; got %d %q. "+
				"Un POST protegido que contesta 4xx deja al usuario ante una página muerta: la aduana "+
				"REDIRIGE. Si esta ruta es legítimamente pública, decláralo en rutasPOSTExentasDeLaAduana "+
				"con su motivo.",
				ruta, r.Path, rec.Code, rec.Header().Get("Location"))
		}
	}

	if cubiertos == 0 {
		t.Fatal("el router no registra NINGÚN POST protegido: este test dejó de medir el invariante de " +
			"aduana en vez de cumplirlo. Ánclalo a una ruta que se quede en el BFF antes de darlo por bueno")
	}

	// Una exención que ya no corresponde a ninguna ruta registrada es basura que envejece: la ruta se
	// fue y el permiso se quedó, listo para eximir de rebote a la siguiente que se llame igual.
	for path, motivo := range rutasPOSTExentasDeLaAduana {
		if !vistasExentas[path] {
			t.Errorf("la exención de POST %s (%q) ya no corresponde a ninguna ruta registrada: sobra", path, motivo)
		}
	}
}
