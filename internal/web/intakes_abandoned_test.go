package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// T4.6 · `abandoned` EN LA CONSOLA. La plataforma publicó el estado (cloud `79e20de`): la máquina de
// estados lo conoce, `?status=abandoned` lo filtra y el detalle de una solicitud abandonada devuelve
// `allowed_transitions: []`. Este lado tenía que verse y usarse — y la sorpresa, al mirarlo, fue la
// misma que del otro lado: los tres caminos que hacían falta YA funcionaban, y no por casualidad.
//
//   - El texto de negocio sale del ÚNICO diccionario de presentación (`intakeStatusOptions`,
//     `intakes_status.go:37`), que ya traía la clave desde T1.5. No hay un segundo diccionario que
//     pudiera discrepar.
//   - El filtro se pinta recorriendo ESE diccionario (`intakes.html:41`), así que la opción existe por
//     construcción, y `intakeFilterFromQuery` no valida estados —la autoridad es la API—, de modo que
//     `abandoned` viaja sin que nadie lo autorice aquí.
//   - La marca visual es la MISMA que la del resto de estados: el `chip chip--neutral` de la fila y el
//     `chip chip--info` de la cabecera del detalle. La pantalla no colorea por estado, y darle a
//     `abandoned` un color propio habría sido inventar un mecanismo que ningún otro estado tiene.
//
// Lo que faltaba, entonces, era la PRUEBA: que la bandeja lo encuentra, que la pantalla de una
// solicitud abandonada no ofrece acciones imposibles, y que el operador que la abandona aterriza en
// esa pantalla. Cada test de aquí se verificó mutando el código de producción para ver que muerde.

// Cuerpos con la forma que la plataforma publica HOY (cloud `79e20de`): `publicapi.intakeDetailResponse`
// (`internal/publicapi/intakes.go:100-105`) con `customer_note` y `customization` siempre presentes,
// `revisions` desde T4.1 y `allowed_transitions` que para un terminal es `[]` y nunca `null` —lo
// garantiza `intakes.AllowedTransitions`, que devuelve `make([]string, 0, …)` (`status.go:166`) sobre
// una clave AUSENTE del mapa `transitions`, que es lo que hace terminal a `abandoned`—.
//
// `revisions` va incluido a propósito aunque el BFF no lo consuma: es un campo que la plataforma manda
// hoy y que este lado tiene que ignorar sin romperse. Si el decodificador se volviera estricto, estos
// tests lo verían.
const (
	detailAbandonedBody = `{"id":"in-a7","contact_id":"ct-op4","session_id":"s-1","status":"abandoned",
	 "total":21.00,"customer_note":"dejarlo en portería","created_at":"2026-08-05T10:00:00Z",
	 "updated_at":"2026-08-07T12:00:00Z",
	 "items":[{"sku":"TORTA","label":"Torta","customization":"sin gluten","qty":1,"unit_price":18.00},
	          {"sku":"VELA","label":"Velas","customization":"","qty":1,"unit_price":3.00}],
	 "revisions":[{"revision_no":1,"kind":"cart","payload":{"version":1},"created_by":"system",
	               "created_at":"2026-08-05T10:00:00Z"}],
	 "allowed_transitions":[]}`

	// Los destinos de una solicitud ABIERTA son literalmente los que devuelve
	// `intakes.AllowedTransitions("open")`: los cuatro de `transitions[StatusOpen]` en orden
	// alfabético, con `abandoned` el primero (`internal/intakes/status.go:61,166`).
	detailOpenBody = `{"id":"in-a7","contact_id":"ct-op4","session_id":"s-1","status":"open",
	 "total":21.00,"customer_note":"","created_at":"2026-08-05T10:00:00Z",
	 "updated_at":"2026-08-05T10:00:00Z",
	 "items":[{"sku":"TORTA","label":"Torta","customization":"","qty":1,"unit_price":18.00}],
	 "revisions":[],
	 "allowed_transitions":["abandoned","cancelled","confirmed","pending_approval"]}`

	// Bandeja con una solicitud abandonada y una confirmada: hacen falta las dos para comprobar que la
	// marca visual del estado nuevo es la MISMA que la de los demás, y no una propia.
	listAbandonedBody = `{"intakes":[
	 {"id":"in-a7","contact_id":"ct-op4","session_id":"s-1","status":"abandoned","total":21,
	  "customer_note":"","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z"},
	 {"id":"in-a2","contact_id":"ct-zz9","session_id":"s-1","status":"confirmed","total":7,
	  "customer_note":"","created_at":"2026-08-04T09:00:00Z","updated_at":"2026-08-04T09:30:00Z"}],
	 "page":1,"page_size":50,"total":2}`
)

// TestIntakesFilterFindsAbandoned: la bandeja puede pedir lo abandonado. El filtro ofrece la opción,
// el criterio viaja a la API TAL CUAL —el BFF no valida estados: quien manda es la plataforma, que
// desde `79e20de` responde 200 a `?status=abandoned`— y vuelve marcado en el formulario, para que el
// operador vea con qué criterio está mirando.
func TestIntakesFilterFindsAbandoned(t *testing.T) {
	var seen url.Values
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes" {
			seen = r.URL.Query()
			_, _ = io.WriteString(w, listAbandonedBody)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer api.Close()

	rec := getWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes?status=abandoned", validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("la bandeja filtrada por abandoned debía renderizar 200, got %d", rec.Code)
	}
	if seen.Get("status") != "abandoned" {
		t.Errorf("el filtro debía viajar a la API como status=abandoned, got %q", seen.Get("status"))
	}

	out := rec.Body.String()
	// La opción existe en el desplegable de filtro y vuelve marcada.
	if !strings.Contains(out, `<option value="abandoned" selected>abandonado</option>`) {
		t.Error("el filtro debía ofrecer «abandonado» y dejarlo seleccionado tras aplicarlo")
	}
	// La fila se pinta con el nombre de negocio, no con la clave del wire.
	if !strings.Contains(out, `<span class="chip chip--neutral">abandonado</span>`) {
		t.Error("la solicitud abandonada debía pintarse como «abandonado»")
	}
	if strings.Contains(out, ">abandoned<") {
		t.Error("la clave cruda del wire no debe llegar a la pantalla")
	}
	// Y la marca visual es la de siempre: las dos filas —abandonada y confirmada— llevan el MISMO chip.
	// Un estado con color propio se vería aquí como un tercer chip.
	if n := strings.Count(out, `<span class="chip chip--neutral">`); n != 2 {
		t.Errorf("las dos filas debían llevar el mismo chip que el resto de estados, got %d", n)
	}
}

// TestIntakeDetailAbandonedOffersNoAction es el corazón de esta parte: `abandoned` es TERMINAL y la
// pantalla no puede ofrecer lo que la plataforma no autoriza. Con `allowed_transitions: []` no se
// emite ni desplegable ni botón —no hay un desplegable vacío ni un «Aplicar» que no lleve a ninguna
// parte—, y se dice por qué. Se comprueba además que abandonar no borró nada: las líneas, la
// personalización y la indicación del pedido siguen en la ficha.
func TestIntakeDetailAbandonedOffersNoAction(t *testing.T) {
	out := renderRealDetail(t, detailAbandonedBody)

	if !strings.Contains(out, "estado · abandonado") {
		t.Error("la cabecera debía pintar el estado con su nombre de negocio")
	}
	if strings.Contains(out, "<select") || strings.Contains(out, "<option") {
		t.Error("un estado terminal no puede ofrecer un desplegable de transición, ni siquiera vacío")
	}
	// El único formulario y el único botón de la página son los de cerrar sesión, que vienen del
	// layout: en el cuerpo de la solicitud abandonada no queda ninguna acción que pulsar.
	if n := strings.Count(out, "<form"); n != 1 {
		t.Errorf("solo debía quedar el formulario de logout, got %d formularios", n)
	}
	if n := strings.Count(out, "<button"); n != 1 {
		t.Errorf("solo debía quedar el botón de logout, got %d botones", n)
	}
	if !strings.Contains(out, `action="/logout"`) {
		t.Error("el formulario que queda debía ser el de logout")
	}
	if strings.Contains(out, `action="/intakes/`) {
		t.Error("no debía emitirse el formulario de cambio de estado")
	}
	// «No hay acciones» y «no lo sé» son cosas distintas y se dicen distinto (ver transitionsOf).
	if !strings.Contains(out, "estado final") {
		t.Error("la pantalla debía explicar que la solicitud ya no admite cambios")
	}
	if strings.Contains(out, "allowed_transitions") {
		t.Error("un terminal real no puede presentarse como «la plataforma no informa»")
	}
	// Abandonar cambia el estado y nada más: lo que el cliente pidió sigue ahí para consultarlo.
	for _, want := range []string{"<td>Torta<span class=\"cell-note\">Personalización: sin gluten</span></td>",
		"<td>Velas</td>", "dejarlo en portería", "total · 21.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("la ficha abandonada debía conservar %q", want)
		}
	}
}

// TestIntakeDetailOffersAbandonedOnlyWhenPlatformDoesIt fija la propiedad que sostiene toda esta
// pantalla: el `<select>` es un PASO A TRAVÉS de `allowed_transitions`, sin filtro propio en ninguna
// de las dos direcciones. La consola no añade `abandoned` como destino —lo publica la plataforma, que
// tiene `open → abandoned` en su mapa (`internal/intakes/status.go:61`)— y tampoco lo quita, porque
// quitarlo sería la misma falta al revés: una segunda máquina de estados escondida en el cliente.
//
// El descarte MANUAL, que es otra cosa, tiene su propia puerta (`POST /intakes/discard`, T4.8) y no
// pasa por aquí: `expired → abandoned` NO está en `transitions` sino en `discardable`, así que una
// solicitud vencida no ofrecerá este destino por más que sea descartable.
func TestIntakeDetailOffersAbandonedOnlyWhenPlatformDoesIt(t *testing.T) {
	out := renderRealDetail(t, detailOpenBody)

	if !strings.Contains(out, `<option value="abandoned">abandonado</option>`) {
		t.Error("si la plataforma autoriza el destino, la pantalla debía ofrecerlo traducido")
	}
	if n := strings.Count(out, "<option"); n != 4 {
		t.Errorf("debían ofrecerse los 4 destinos que publica la plataforma, ni uno más, got %d", n)
	}
}

// TestSetIntakeStatusToAbandonedLandsOnTerminalScreen recorre el camino entero del operador: abandona
// desde el desplegable y aterriza en la ficha re-leída. La confirmación habla en español y la pantalla
// que queda ya no ofrece nada, que es exactamente lo que debe pasar tras una acción irreversible.
func TestSetIntakeStatusToAbandonedLandsOnTerminalScreen(t *testing.T) {
	detail := detailOpenBody
	var posted string
	api := intakesAPI([]string{"cart_basic"}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/intakes/in-a7/status":
			body, _ := io.ReadAll(r.Body)
			posted = string(body)
			detail = detailAbandonedBody
			_, _ = io.WriteString(w, `{"id":"in-a7","contact_id":"ct-op4","session_id":"s-1",
			 "status":"abandoned","total":21,"customer_note":"dejarlo en portería",
			 "created_at":"2026-08-05T10:00:00Z","updated_at":"2026-08-07T12:00:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes/in-a7":
			_, _ = io.WriteString(w, detail)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer api.Close()

	form := url.Values{"status": {"abandoned"}}
	rec := postFormWithCookie(NewRouter(authTestCfg(api.URL)), "/intakes/in-a7/status", form, validSessionCookie(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("abandonar una solicitud abierta debía dar 200, got %d", rec.Code)
	}
	if !strings.Contains(posted, `"status":"abandoned"`) {
		t.Errorf("la API debía recibir el estado pedido, got %q", posted)
	}

	out := rec.Body.String()
	if !strings.Contains(out, "Solicitud movida a «abandonado».") {
		t.Error("la confirmación debía nombrar el estado en español")
	}
	if !strings.Contains(out, "estado · abandonado") {
		t.Error("la ficha re-leída debía mostrar el estado nuevo")
	}
	if strings.Contains(out, "<select") {
		t.Error("tras abandonar no puede quedar un desplegable: el estado es terminal")
	}
	if !strings.Contains(out, "estado final") {
		t.Error("la pantalla que queda debía decir que ya no admite cambios")
	}
}
