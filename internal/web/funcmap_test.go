package web

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// EL ANDAMIAJE QUE NO PERTENECE A NINGUNA PANTALLA (Plan 047 · T9.4)
// ---------------------------------------------------------------------------
//
// Las tres olas de la mudanza se llevaron pantallas enteras —rutas, handlers, clientes y plantillas—,
// pero lo TRANSVERSAL se quedó a medias por definición: un helper de plantilla, un gate de features o
// una entrada de navegación no son de una pantalla, así que ninguna retirada podía llevárselos sin
// mirar a las demás. Este fichero es la barrida final, y su forma es la que exige el criterio: no una
// lista de lo que se quitó, sino una REGLA que se sigue cumpliendo mañana.
//
// 🔴 Un símbolo sin consumidor NO FALLA, y ese es todo el problema. Este plan ya cerró una casilla por
// exactamente esto: una clave de FuncMap que ninguna plantilla llama compila, pasa `vet`, no aparece
// en ningún gate y se queda ahí esperando —hasta el día en que alguien escriba `{{ cuenta … }}` con la
// firma cambiada y el error salga en TIEMPO DE EJECUCIÓN, en la página de un cliente, que es el único
// sitio donde las plantillas de Go fallan.

// accionesDePlantilla devuelve el texto de cada acción `{{ … }}` de una plantilla, SIN los comentarios.
//
// Descartar `{{/* … */}}` no es cosmético: el comentario del gate en home.html ilustra su regla con
// `{{ if $.Entitlements.Has "crm_bridge" }}` escrito dentro. Un colector que lo contara vería viva una
// clave que quizá solo sigue nombrada en la prosa —que es como envejecen los comentarios de este
// paquete, y ya ha pasado tres veces con ejemplos que citaban rutas retiradas—.
func accionesDePlantilla(contenido string) []string {
	re := regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(contenido, -1) {
		cuerpo := strings.TrimSpace(strings.Trim(m[1], "-"))
		if strings.HasPrefix(cuerpo, "/*") {
			continue
		}
		out = append(out, cuerpo)
	}
	return out
}

// plantillasVivas devuelve nombre→contenido de todo lo que el binario sirve de verdad (el embed), sin
// distinguir layout de página: un helper consumido SOLO por base.html está igual de vivo.
func plantillasVivas(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"templates/layouts", "templates/pages"} {
		entradas, err := fs.ReadDir(templatesFS, dir)
		if err != nil {
			t.Fatalf("no se pudo leer %s del embed: %v", dir, err)
		}
		for _, e := range entradas {
			ruta := path.Join(dir, e.Name())
			b, err := fs.ReadFile(templatesFS, ruta)
			if err != nil {
				t.Fatalf("no se pudo leer %s: %v", ruta, err)
			}
			out[ruta] = string(b)
		}
	}
	if len(out) == 0 {
		t.Fatal("el embed no trajo ninguna plantilla: el test no está midiendo nada")
	}
	return out
}

// TestNingunHelperDelFuncMapSeQuedaSinConsumidor: toda clave registrada en `funcsDePlantilla` la
// invoca alguna plantilla superviviente.
//
// La lista de claves se deriva del PROPIO FuncMap y no se escribe a mano, por el mismo motivo que el
// inventario de rutas se deriva de `router.Routes()`: una lista a mano nace con fecha de caducidad y
// el helper que alguien añada mañana entra solo.
//
// 📌 Al cerrar el plan quedan DOS —`hasPrefix` y `yield`—, las dos consumidas por base.html, y ninguna
// por una plantilla de negocio: por eso las tres retiradas no las tocaron. `statusLabel` y `cuenta` se
// fueron en el T7.7 con la bandeja, que era su única consumidora; `cuenta` sigue viva como función Go
// en integrations_handler.go (los plazos del CRM), que es harina de otro costal —ahí sí la compila
// alguien—.
func TestNingunHelperDelFuncMapSeQuedaSinConsumidor(t *testing.T) {
	// El `yield` real cierra sobre la plantilla compilada del router; aquí solo interesan las CLAVES,
	// así que un nil basta.
	helpers := funcsDePlantilla(nil)
	if len(helpers) == 0 {
		t.Fatal("el FuncMap está vacío: este test dejó de tener sujeto. Si de verdad no queda ningún " +
			"helper, quita también el parámetro y el motivo por el que funcsDePlantilla existe")
	}

	plantillas := plantillasVivas(t)

	for nombre := range helpers {
		usoDe := regexp.MustCompile(`\b` + regexp.QuoteMeta(nombre) + `\b`)
		var consumidores []string
		for ruta, contenido := range plantillas {
			for _, accion := range accionesDePlantilla(contenido) {
				if usoDe.MatchString(accion) {
					consumidores = append(consumidores, ruta)
					break
				}
			}
		}
		if len(consumidores) == 0 {
			t.Errorf("el helper %q está registrado en el FuncMap y NINGUNA plantilla superviviente lo "+
				"invoca. Una clave sin consumidor no rompe nada hoy: compila, pasa vet y espera a que "+
				"alguien la llame mañana con otra firma, y entonces revienta en tiempo de ejecución. "+
				"O se consume o se retira", nombre)
		}
	}
}

// ---------------------------------------------------------------------------
// El GATE DE ENTITLEMENTS: se queda, y aquí está el dato
// ---------------------------------------------------------------------------
//
// 🔴 LA DECISIÓN SE TOMÓ CON EL DATO DELANTE, que es lo que pedía el criterio: si tras la mudanza
// ninguna pantalla superviviente leyera una feature, el gate se retiraría —no se deja código que no
// gatea nada—. Se midió, y las lee:
//
//   - `crm_bridge`  → /integrations: la plantilla gatea lo que se PINTA y el handler corta los TRES
//     verbos (también el GET) antes de tocar la API. Es la MISMA clave que exige RequireFeature en la
//     plataforma.
//   - `api_llm`     → /tenant-llm: idéntico, los tres verbos.
//   - `llm_intent`  → la portada: gatea el bloque que dice que el clasificador está activo.
//
// Las tres son de pantallas PERMANENTES o de la portada, ninguna de negocio. Las que se fueron con la
// mudanza fueron otras —`cart_basic` con la bandeja y `catalog_import` con el import—, y su marcha es
// justo lo que hacía razonable dudar de si quedaba alguien: quedaban tres, y dos de ellas cortando en
// Go, no solo escondiendo HTML.

// featuresVivasDelBFF son las claves que alguna pantalla superviviente lee HOY. Es una lista explícita
// a propósito: una clave nueva pone el test en rojo y obliga a decir quién la lee, y una que se queda
// sin lector lo dice en voz alta en vez de envejecer como código que no gatea nada.
var featuresVivasDelBFF = map[string]string{
	integrationsFeature: "el puente CRM: /integrations la gatea en plantilla y corta los tres verbos en Go",
	tenantLLMFeature:    "el proveedor de IA: /tenant-llm la gatea en plantilla y corta los tres verbos en Go",
	"llm_intent":        "la portada: gatea el bloque que anuncia el clasificador de intenciones",
}

// TestElGateDeFeaturesSigueTeniendoSujeto comprueba que las claves que las plantillas consultan son
// EXACTAMENTE las declaradas vivas.
//
// El aserto que de verdad importa es el de conjunto vacío: el día que la última pantalla gateada se
// mude, este test se cae y pide que se retire el gate entero (entitlements.go, su cliente y su puerto)
// en vez de dejar un middleware que ya no decide nada.
func TestElGateDeFeaturesSigueTeniendoSujeto(t *testing.T) {
	if len(featuresVivasDelBFF) == 0 {
		t.Fatal("no queda ninguna feature con lector: el gate de entitlements del BFF ya no gatea " +
			"nada y hay que RETIRARLO (entitlements.go, EntitlementsClient y su puerto), no dejarlo " +
			"como código que no decide")
	}

	leidaEn := regexp.MustCompile(`Entitlements\.Has\s+"([a-z_]+)"`)
	enPlantillas := map[string][]string{}
	for ruta, contenido := range plantillasVivas(t) {
		for _, accion := range accionesDePlantilla(contenido) {
			for _, m := range leidaEn.FindAllStringSubmatch(accion, -1) {
				enPlantillas[m[1]] = append(enPlantillas[m[1]], ruta)
			}
		}
	}
	if len(enPlantillas) == 0 {
		t.Fatal("ninguna plantilla consulta .Entitlements.Has: o el gate perdió su último lector, o el " +
			"colector dejó de encontrar los usos")
	}

	for clave, dónde := range enPlantillas {
		if _, viva := featuresVivasDelBFF[clave]; !viva {
			sort.Strings(dónde)
			t.Errorf("la plantilla %v gatea por %q y esa clave no está declarada viva: di quién la lee "+
				"o quita el gate", dónde, clave)
		}
	}
	// `llm_intent` y las dos de las pantallas técnicas se leen en plantilla; que las tres declaradas
	// tengan lector se comprueba aquí. Una declarada sin uso es el caso «código que no gatea nada»
	// visto desde el otro lado.
	for clave, motivo := range featuresVivasDelBFF {
		if len(enPlantillas[clave]) == 0 {
			t.Errorf("%q se declara viva (%s) y NINGUNA plantilla la consulta", clave, motivo)
		}
	}
}

// TestLasFeaturesDeLasPantallasTecnicasCortanTambienEnGo es la otra mitad, y es la que hace que la
// respuesta a «¿el gate sigue haciendo falta?» sea sí sin discusión: en /integrations y /tenant-llm la
// feature no solo esconde HTML, decide si el handler llama a la API.
//
// Se prueba por conducta y no leyendo el código: sin la feature, un GET a la pantalla NO consulta su
// endpoint de negocio en la plataforma. Un gate que solo escondiera el formulario dejaría la llamada
// saliendo igual.
//
// 🔴 LLEVA LA MITAD POSITIVA POR UN FALLO MEDIDO AL ESCRIBIRLO. La primera versión solo tenía el
// aserto de ausencia y salió VERDE... contra los endpoints equivocados (`/api/v1/tenant/integrations`
// y `/api/v1/tenant/llm`, que no existen: son `/api/v1/integrations` y `/api/v1/tenant-llm`). Un
// «no se llamó» lo cumple de sobra una ruta que nadie iba a llamar nunca, y el test habría vivido
// para siempre acreditando el vacío. Con la feature PUESTA el endpoint tiene que recibir su visita:
// eso es lo que ata el nombre a la realidad.
func TestLasFeaturesDeLasPantallasTecnicasCortanTambienEnGo(t *testing.T) {
	casos := []struct {
		ruta     string
		feature  string
		endpoint string
	}{
		{"/integrations", integrationsFeature, "/api/v1/integrations"},
		{"/tenant-llm", tenantLLMFeature, "/api/v1/tenant-llm"},
	}

	for _, caso := range casos {
		t.Run(caso.ruta, func(t *testing.T) {
			// CON la feature: el endpoint recibe su visita. La API fake no lo mapea y contesta 500 —la
			// pantalla se degrada—, pero la llamada queda contada, que es lo que aquí se mide.
			api, hits := homeAPI(t, caso.feature)
			router := NewRouter(authTestCfg(api.URL))
			exigeRutaRegistrada(t, router, "GET", caso.ruta)

			if rec := getWithCookie(router, caso.ruta, validSessionCookie(t)); rec.Code == 0 {
				t.Fatalf("GET %s no respondió", caso.ruta)
			}
			if n := hits(caso.endpoint); n == 0 {
				t.Fatalf("con la feature %q, GET %s no llamó a %s: o el gate no abre, o este test está "+
					"vigilando un endpoint que nadie usa y su aserto de abajo mide el vacío",
					caso.feature, caso.ruta, caso.endpoint)
			}

			// SIN la feature: ni una llamada. El corte es en Go, antes de hablar con la plataforma.
			apiSinFeature, hitsSinFeature := homeAPI(t)
			routerSinFeature := NewRouter(authTestCfg(apiSinFeature.URL))

			if rec := getWithCookie(routerSinFeature, caso.ruta, validSessionCookie(t)); rec.Code == 0 {
				t.Fatalf("GET %s no respondió", caso.ruta)
			}
			if n := hitsSinFeature(caso.endpoint); n != 0 {
				t.Errorf("sin la feature %q, GET %s llamó %d veces a %s: el gate tiene que cortar ANTES "+
					"de hablar con la plataforma, no solo esconder el formulario",
					caso.feature, caso.ruta, n, caso.endpoint)
			}
		})
	}
}
