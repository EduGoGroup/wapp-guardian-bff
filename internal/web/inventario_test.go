package web

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// EL INVENTARIO FINAL DEL BFF (Plan 047 · T9.1, REQ-10)
// ---------------------------------------------------------------------------
//
// Las olas 6, 7 y 8 mudaron a la consola del cliente las diecinueve rutas de negocio de esta consola
// —el editor de flujos y disparadores, la bandeja de solicitudes y el import de catálogo— y con ellas
// sus seis plantillas. Lo que queda ya no es un tránsito hacia otra cosa: es el BFF definitivo, y lo
// que hace este fichero es fijarlo por escrito de forma EJECUTABLE.
//
// 🔴 EL ASERTO ES LA LISTA COMPLETA, NO UNA LISTA NEGRA, y eso no es una preferencia de estilo: es
// una lección medida en este mismo plan. Un test que comprobara «ninguna ruta contiene /intakes»
// pasaría tan campante con `/solicitudes` registrada — el nombre en castellano de la MISMA pantalla,
// que es justo el nombre que tiene en su casa nueva—. Una lista negra solo sabe decir que no volvió
// lo que ya se fue; lo que hay que impedir es que entre lo que no se ha declarado. Por eso el aserto
// es de IGUALDAD de conjuntos: sobra ⇒ rojo, falta ⇒ rojo.
//
// Y por eso cada registro va CLASIFICADO en una de las cuatro familias de REQ-10 en vez de aparecer
// en una lista plana: una ruta nueva no se cuela poniendo un renglón más, hay que decir qué es. Si es
// de negocio, no hay familia donde ponerla y el test se queda rojo mientras exista.

// familiaDeRuta son las cuatro clases en las que REQ-10 reparte lo que el BFF conserva. NO hay una
// familia «negocio»: esa es exactamente la ausencia que el inventario hace cumplir.
type familiaDeRuta string

const (
	// familiaAduana: el plano de autenticación. Es lo que el BFF conserva PARA SIEMPRE (ADR-0035 §3,
	// que el ADR-0047 deja intacto): entrar, darse de alta, salir y esperar a que te asignen empresa.
	familiaAduana familiaDeRuta = "aduana"
	// familiaPortada: la raíz. Es el destino de las tres redirecciones del plano de autenticación, así
	// que no puede irse ni aunque no pintara nada (ver TestLasTresRedireccionesAterrizanEnLaPortada).
	familiaPortada familiaDeRuta = "portada"
	// familiaTecnica: configuración del tenant que no es operación del negocio. Capa técnica: no migra.
	familiaTecnica familiaDeRuta = "técnica"
	// familiaInfraestructura: lo que no es una pantalla —las hojas de estilo y la sonda de salud—.
	familiaInfraestructura familiaDeRuta = "infraestructura"
)

// inventarioDeRutas es REQ-10 escrito de forma que una máquina pueda compararlo con la realidad.
//
// La clave es "MÉTODO /patrón" —el patrón tal como lo guarda gin, no la URL concreta— porque el
// método es parte de la identidad de un registro: este router nace con HandleMethodNotAllowed en
// false y responde 404 a un verbo no registrado igual que a una ruta inexistente, así que un
// inventario que solo mirara paths daría por retirado un `POST /x` que sigue vivo bajo un `GET /x`
// declarado.
var inventarioDeRutas = map[string]familiaDeRuta{
	// --- Aduana (6): el plano de autenticación, lo único que esta consola conserva para siempre. ---
	"GET /login":   familiaAduana,
	"POST /login":  familiaAduana,
	"GET /signup":  familiaAduana,
	"POST /signup": familiaAduana,
	"POST /logout": familiaAduana,
	"GET /pending": familiaAduana,

	// --- Portada (1). ---
	"GET /": familiaPortada,

	// --- Técnicas (8): configuración del tenant, capa técnica que no migra (ADR-0035 §3). ---
	"GET /variables":            familiaTecnica,
	"POST /variables":           familiaTecnica,
	"GET /integrations":         familiaTecnica,
	"POST /integrations":        familiaTecnica,
	"POST /integrations/delete": familiaTecnica,
	"GET /tenant-llm":           familiaTecnica,
	"POST /tenant-llm":          familiaTecnica,
	"POST /tenant-llm/delete":   familiaTecnica,

	// --- Infraestructura (5): las cuatro hojas de estilo mismo-origen (sin CDNs, encaja con la CSP
	// endurecida) y la sonda de salud. Ninguna renderiza una pantalla ni exige sesión. ---
	"GET /static/css/app.css":             familiaInfraestructura,
	"GET /static/css/wapp-tokens.css":     familiaInfraestructura,
	"GET /static/css/wapp-components.css": familiaInfraestructura,
	"GET /static/css/theme-bff.css":       familiaInfraestructura,
	"GET /healthz":                        familiaInfraestructura,
}

// tamanoPorFamilia es el reparto de REQ-10. Está aparte del mapa a propósito: sin él, reclasificar
// una ruta de una familia a otra —meter una pantalla de negocio entre las «técnicas», que es la forma
// que tendría el error de buena fe— dejaría el conjunto idéntico y el test verde. Con él hay que
// tocar también el número, que es donde se ve.
var tamanoPorFamilia = map[familiaDeRuta]int{
	familiaAduana:          6,
	familiaPortada:         1,
	familiaTecnica:         8,
	familiaInfraestructura: 5,
}

// TestElInventarioDeRutasEsExactamenteElDeREQ10 compara lo que el router REGISTRA con lo que REQ-10
// DECLARA, en los dos sentidos.
func TestElInventarioDeRutasEsExactamenteElDeREQ10(t *testing.T) {
	registradas := map[string]bool{}
	for _, r := range NewRouter(authTestCfg("http://api.invalid")).Routes() {
		registradas[r.Method+" "+r.Path] = true
	}
	if len(registradas) == 0 {
		t.Fatal("router.Routes() vacío: el test no está midiendo nada")
	}

	// SOBRA: registrada y sin declarar. Es el caso que este test existe para cazar —una ruta nueva
	// que entra sin que nadie diga qué es—, y da igual si es de negocio o no: sin clasificar, rojo.
	var sobrantes []string
	for reg := range registradas {
		if _, ok := inventarioDeRutas[reg]; !ok {
			sobrantes = append(sobrantes, reg)
		}
	}
	sort.Strings(sobrantes)
	for _, s := range sobrantes {
		t.Errorf("el router registra %q y el inventario de REQ-10 no lo declara. Clasifícala en una "+
			"de las cuatro familias (aduana, portada, técnica, infraestructura) o retírala: si es una "+
			"pantalla de NEGOCIO no tiene familia aquí, su casa es wapp-client-console", s)
	}

	// FALTA: declarada y sin registrar. Un inventario que nombra rutas que ya no existen envejece a
	// mentira, y es la misma forma que caza TestNingunaConstanteDeRutaNombraUnaRutaFantasma.
	var faltantes []string
	for dec := range inventarioDeRutas {
		if !registradas[dec] {
			faltantes = append(faltantes, dec)
		}
	}
	sort.Strings(faltantes)
	for _, f := range faltantes {
		t.Errorf("el inventario de REQ-10 declara %q y el router NO la registra: o se retiró sin "+
			"actualizar el inventario, o se rompió al montarla", f)
	}

	if len(registradas) != len(inventarioDeRutas) {
		t.Errorf("el BFF sirve %d registros y REQ-10 declara %d", len(registradas), len(inventarioDeRutas))
	}
}

// TestElRepartoPorFamiliaEsElDeREQ10 fija los cuatro números —6 · 1 · 8 · 5 = 20— y, sobre todo, que
// no hay ni un registro de negocio.
func TestElRepartoPorFamiliaEsElDeREQ10(t *testing.T) {
	real := map[familiaDeRuta]int{}
	for _, familia := range inventarioDeRutas {
		real[familia]++
	}

	for familia, esperado := range tamanoPorFamilia {
		if real[familia] != esperado {
			t.Errorf("la familia %q tiene %d rutas y REQ-10 declara %d", familia, real[familia], esperado)
		}
	}
	for familia := range real {
		if _, declarada := tamanoPorFamilia[familia]; !declarada {
			t.Errorf("la familia %q no existe en REQ-10: el BFF conserva aduana, portada, técnicas e "+
				"infraestructura, y nada más", familia)
		}
	}

	total := 0
	for _, n := range tamanoPorFamilia {
		total += n
	}
	if total != len(inventarioDeRutas) {
		t.Errorf("los tamaños por familia suman %d y el inventario tiene %d entradas: una suma escrita "+
			"a mano que no se deriva de la lista se desincroniza de ella", total, len(inventarioDeRutas))
	}
}

// TestNingunaRutaProtegidaEscapaDelPlanoTecnico es el aserto de PROPÓSITO, el que el conteo no puede
// dar: toda ruta que exige sesión y pinta algo es aduana, portada o capa técnica.
//
// Existe porque el inventario de arriba lo cumpliría igual alguien que declarase una pantalla de
// negocio como «técnica»: el conjunto cuadraría y el reparto también si además ajusta el número. Aquí
// el sujeto es distinto —la RUTA, no la lista—: las técnicas son tres pantallas concretas y nombradas,
// y cualquier path protegido que no sea una de ellas ni la portada es, por definición, algo nuevo.
func TestNingunaRutaProtegidaEscapaDelPlanoTecnico(t *testing.T) {
	// Las tres pantallas técnicas que se quedan, por su RAÍZ: de ellas cuelgan sus rutas de escritura
	// por concatenación (`+"/delete"`), igual que las trata rutas_declaradas_test.go.
	raicesTecnicas := []string{"/variables", "/integrations", "/tenant-llm"}

	comprobados := 0
	for reg, familia := range inventarioDeRutas {
		if familia != familiaTecnica {
			continue
		}
		comprobados++
		path := reg[len(methodOf(reg))+1:]
		if !cuelgaDeAlguna(path, raicesTecnicas) {
			t.Errorf("%q está clasificada como técnica y no cuelga de ninguna de las tres pantallas "+
				"técnicas del BFF (%v). Una pantalla nueva bajo esa etiqueta es negocio disfrazado: su "+
				"casa es wapp-client-console", reg, raicesTecnicas)
		}
	}
	if comprobados == 0 {
		t.Fatal("no queda ninguna ruta técnica: este test dejó de tener sujeto")
	}
}

// methodOf devuelve el verbo de una clave "MÉTODO /patrón".
func methodOf(registro string) string {
	for i := 0; i < len(registro); i++ {
		if registro[i] == ' ' {
			return registro[:i]
		}
	}
	return registro
}

// cuelgaDeAlguna responde si el path es una de las raíces o cuelga de ella.
func cuelgaDeAlguna(path string, raices []string) bool {
	for _, raiz := range raices {
		if path == raiz || (len(path) > len(raiz) && path[:len(raiz)+1] == raiz+"/") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// El inventario de PLANTILLAS
// ---------------------------------------------------------------------------

// inventarioDePaginas es REQ-10 por el lado de las vistas: siete fragmentos de página, uno por
// pantalla viva. Se declara con la misma familia que su ruta para que las dos listas se lean juntas.
//
// El listado con el que se compara sale del DIRECTORIO (del embed, que es lo que el binario sirve de
// verdad) y no de una lista compilada: un .html que alguien añada entra solo en el embed —el patrón
// es de carpeta— y se sirve sin que nadie lo declare. Justo eso es lo que hay que ver.
var inventarioDePaginas = map[string]familiaDeRuta{
	"login.html":            familiaAduana,
	"signup.html":           familiaAduana,
	"pending.html":          familiaAduana,
	"home.html":             familiaPortada,
	"tenant-variables.html": familiaTecnica,
	"integrations.html":     familiaTecnica,
	"tenant-llm.html":       familiaTecnica,
}

// TestElInventarioDePlantillasEsExactamenteElDeREQ10: templates/pages/ tiene SIETE ficheros —3 de
// aduana, 1 de portada, 3 técnicas—, ni uno más.
//
// Una plantilla huérfana no da la cara sola: compila con el resto, no la referencia ningún handler y
// se queda ahí. La que sobra tras una mudanza es peor que muerta —es el molde de una pantalla que
// vive en otra casa, listo para que alguien la resucite aquí y tener dos copias divergiendo—.
func TestElInventarioDePlantillasEsExactamenteElDeREQ10(t *testing.T) {
	entradas, err := fs.ReadDir(templatesFS, "templates/pages")
	if err != nil {
		t.Fatalf("no se pudo leer templates/pages del embed: %v", err)
	}
	if len(entradas) == 0 {
		t.Fatal("templates/pages está vacío en el embed: el test no está midiendo nada")
	}

	enDisco := map[string]bool{}
	for _, e := range entradas {
		enDisco[e.Name()] = true
		if _, declarada := inventarioDePaginas[e.Name()]; !declarada {
			t.Errorf("templates/pages/%s existe y REQ-10 no lo declara. Clasifícalo en una de las "+
				"cuatro familias o bórralo: la plantilla de una pantalla mudada es el molde para "+
				"resucitarla aquí, y dos copias de la misma pantalla divergen", e.Name())
		}
	}
	for nombre := range inventarioDePaginas {
		if !enDisco[nombre] {
			t.Errorf("REQ-10 declara templates/pages/%s y no está en el embed: el inventario nombra "+
				"una plantilla que ya no existe", nombre)
		}
	}

	if len(entradas) != len(inventarioDePaginas) {
		t.Errorf("templates/pages tiene %d ficheros y REQ-10 declara %d", len(entradas), len(inventarioDePaginas))
	}

	// El reparto por familia, por el mismo motivo que en las rutas: sin él, reetiquetar una plantilla
	// deja el conjunto idéntico.
	real := map[familiaDeRuta]int{}
	for _, familia := range inventarioDePaginas {
		real[familia]++
	}
	for familia, esperado := range map[familiaDeRuta]int{familiaAduana: 3, familiaPortada: 1, familiaTecnica: 3} {
		if real[familia] != esperado {
			t.Errorf("templates/pages tiene %d plantillas de la familia %q y REQ-10 declara %d",
				real[familia], familia, esperado)
		}
	}
	if n := real[familiaInfraestructura]; n != 0 {
		t.Errorf("hay %d plantillas clasificadas como infraestructura: las hojas de estilo y la sonda "+
			"de salud no renderizan pantallas", n)
	}
}

// TestElLayoutSigueSiendoUnoSolo: templates/layouts/ tiene base.html y nada más.
//
// No es una obviedad: el layout es lo único que TODAS las páginas comparten, y un segundo layout
// «para las pantallas nuevas» es la forma en que una consola acaba con dos cabeceras que divergen —el
// mismo fallo que la mudanza vino a resolver, un piso más abajo—.
func TestElLayoutSigueSiendoUnoSolo(t *testing.T) {
	entradas, err := fs.ReadDir(templatesFS, "templates/layouts")
	if err != nil {
		t.Fatalf("no se pudo leer templates/layouts del embed: %v", err)
	}
	if len(entradas) != 1 || entradas[0].Name() != "base.html" {
		var nombres []string
		for _, e := range entradas {
			nombres = append(nombres, e.Name())
		}
		sort.Strings(nombres)
		t.Errorf("templates/layouts debía tener solo base.html, got %v", nombres)
	}
}

// TestElInventarioCubreTodaPantallaServida cierra el círculo entre las dos listas: toda ruta GET que
// renderiza una pantalla tiene su plantilla declarada, y toda plantilla declarada tiene quien la pida.
//
// Los dos inventarios de arriba son exactos cada uno por su lado y aun así podrían describir dos
// mundos distintos: siete plantillas correctas y un router que solo sirve tres. Este es el aserto que
// los ata.
func TestElInventarioCubreTodaPantallaServida(t *testing.T) {
	// Ruta GET → plantilla que renderiza. Las de infraestructura no pintan pantalla y quedan fuera.
	pantallas := map[string]string{
		"GET /login":     "login.html",
		"GET /signup":    "signup.html",
		"GET /pending":   "pending.html",
		"GET /":          "home.html",
		"GET /variables": "tenant-variables.html",
	}
	// Las dos gateadas se declaran aparte solo por legibilidad; entran en el mismo bucle.
	pantallas["GET /integrations"] = "integrations.html"
	pantallas["GET /tenant-llm"] = "tenant-llm.html"

	registradas := map[string]bool{}
	for _, r := range NewRouter(authTestCfg("http://api.invalid")).Routes() {
		registradas[r.Method+" "+r.Path] = true
	}

	usadas := map[string]bool{}
	for ruta, plantilla := range pantallas {
		if !registradas[ruta] {
			t.Errorf("%q pinta %s y no está registrada en el router", ruta, plantilla)
		}
		if _, ok := inventarioDePaginas[plantilla]; !ok {
			t.Errorf("%q pinta %s y esa plantilla no está en el inventario de REQ-10", ruta, plantilla)
		}
		usadas[plantilla] = true
	}
	for plantilla := range inventarioDePaginas {
		if !usadas[plantilla] {
			t.Errorf("templates/pages/%s no la pide ninguna ruta GET: es una plantilla huérfana. Una "+
				"vista sin quien la renderice no falla, no sale en ningún gate y sobrevive a la "+
				"pantalla que la usaba", plantilla)
		}
	}

	// Toda ruta GET del inventario que NO sea infraestructura tiene que estar aquí arriba: si mañana
	// alguien añade una pantalla y no la mapea, este bucle lo dice.
	for reg, familia := range inventarioDeRutas {
		if familia == familiaInfraestructura || methodOf(reg) != http.MethodGet {
			continue
		}
		if _, mapeada := pantallas[reg]; !mapeada {
			t.Errorf("%q es un GET que no es infraestructura y no declara qué plantilla pinta", reg)
		}
	}
}

// TestNingunaPlantillaEnlazaARutaQueElBFFNoSirve: todo `href`/`action` LITERAL y absoluto de las
// plantillas está registrado en el router.
//
// 🔴 ES EL CANDADO POSITIVO QUE FALTABA, y su ausencia costó trabajo en cada una de las tres olas.
// Hasta ahora esto se vigilaba con listas negras dentro de los tests de cada retirada —«la portada ya
// no contiene href="/flows"», «…ya no contiene href="/intakes"»—, una lista por mudanza, escrita a
// mano y ciega a la siguiente: un enlace a `/solicitudes` las pasaría todas. Aquí el sujeto se deriva
// de las PLANTILLAS y el veredicto lo da `router.Routes()`, así que cubre lo que haya el día que se
// ejecute y cubre gratis lo que se añada.
//
// Se miran los literales y no el HTML renderizado a propósito: renderizar exige montar cada pantalla
// con su API fake y su sesión, y las que no se llegaran a montar quedarían sin mirar —que es justo
// donde se esconde un enlace muerto—. Un `href="{{ .ClientConsoleURL }}"` no es literal y queda fuera
// por su propia naturaleza: apunta a OTRA aplicación, y comprobarlo contra este router no tendría
// sentido.
func TestNingunaPlantillaEnlazaARutaQueElBFFNoSirve(t *testing.T) {
	registradas := map[string]bool{}
	for _, r := range NewRouter(authTestCfg("http://api.invalid")).Routes() {
		registradas[r.Path] = true
	}

	enlace := regexp.MustCompile(`(?:href|action)="(/[^"{}]*)"`)
	comprobados := 0
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
			for _, m := range enlace.FindAllStringSubmatch(string(b), -1) {
				destino := m[1]
				comprobados++
				if !registradas[destino] {
					t.Errorf("%s enlaza a %q y el BFF no registra esa ruta: el enlace manda al usuario "+
						"a un 404 de esta misma consola. Si la pantalla se mudó, el enlace se va con "+
						"ella EN EL MISMO CICLO (REQ-08)", ruta, destino)
				}
			}
		}
	}
	if comprobados == 0 {
		t.Fatal("ninguna plantilla tiene un href o un action absoluto: este test dejó de tener sujeto")
	}
}
