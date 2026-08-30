package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// UN MAPA QUE NOMBRA RUTAS FANTASMA NO LO COMPILA NADIE
// ---------------------------------------------------------------------------
//
// 🔴 ESTE TEST NACE DE UNA MUTACIÓN QUE NINGÚN TEST DEL BFF MATABA (Plan 047 · T7.7).
//
// Entre el T2.4 y el T7.7, el grupo protegido no llevaba un deadline sino un DESPACHADOR de
// deadlines: `requestDeadlineByRoute`, que comparaba `c.FullPath()` con la constante
// `quoteSuggestionRoute = "/intakes/:id/quote-suggestion"` para darle a esa ruta —y solo a ella— 58s
// en vez de 20s. Al retirar la bandeja, la ruta desapareció del router. Dejar el despachador vivo
// habría sido código muerto EN EL CAMINO CRÍTICO de todas las peticiones del BFF: una comparación por
// petición contra una ruta que ya no existe, cuya rama larga no se toma nunca.
//
// Y no lo detectaba nadie. Un invariante que vive en una CADENA no lo compila nadie: la constante
// seguía siendo una constante válida, el despachador seguía compilando, `go vet` seguía en cero y la
// suite entera seguía verde. La ruta fantasma solo se ve mirando a la vez las DOS cosas que nunca se
// miran juntas: lo que el código DECLARA y lo que el router REGISTRA.
//
// El test recorre el AST del paquete en vez de una lista escrita a mano por el mismo motivo que la
// aduana recorre `router.Routes()`: una lista a mano nace con fecha de caducidad, y la constante que
// alguien añada mañana entra sola. Hoy tiene DOS sujetos —`integrationsRoute` y `tenantLLMRoute`—,
// los dos raíces de rutas que cuelgan de ellas por concatenación (`+"/delete"`), que es el caso que la
// rama de prefijo de abajo existe para admitir.
//
// 📌 Eran TRES hasta el Plan 047 · T8.5. El tercero era `catalogImportRoute`, y era el ejemplo vivo de
// lo que este test caza: `webgin.BodyLimit` lo trataba aparte en server.go, exactamente la misma forma
// que tenía el despachador de plazos. Al mudarse el import, ese `BodyLimit` habría quedado nombrando
// una ruta inexistente —compilando, con `vet` en cero y la suite verde—; se retiró entero con la
// constante, y este test es lo que habría gritado si no.

// TestNingunaConstanteDeRutaNombraUnaRutaFantasma: toda constante de este paquete cuyo valor tenga
// forma de patrón de ruta (empieza por "/") está registrada en el router, con algún verbo.
//
// El aserto es de PRESENCIA, así que el peligro es el contrario del habitual: no que sobre material,
// sino que falte. Por eso el mínimo de sujetos es explícito.
func TestNingunaConstanteDeRutaNombraUnaRutaFantasma(t *testing.T) {
	declaradas := constantesConFormaDeRuta(t)

	// Sin sujetos, un aserto de «todas están registradas» lo cumple el conjunto vacío. El día que la
	// última pantalla con ruta constante se mude, este test tiene que decirlo en voz alta y no
	// quedarse verde midiendo cero.
	if len(declaradas) == 0 {
		t.Fatal("ninguna constante de internal/web tiene forma de ruta: este test dejó de tener " +
			"sujeto. Si de verdad no queda ninguna, bórralo; si es que cambió la forma de declararlas, " +
			"arregla el recolector")
	}

	registradas := map[string]bool{}
	for _, r := range NewRouter(authTestCfg("http://api.invalid")).Routes() {
		registradas[r.Path] = true
	}
	if len(registradas) == 0 {
		t.Fatal("router.Routes() vacío: el test no está midiendo nada")
	}

	for nombre, patron := range declaradas {
		if registradas[patron] {
			continue
		}
		// Una constante puede ser la RAÍZ de rutas que cuelgan de ella (`integrationsRoute+"/delete"`),
		// y en ese caso la raíz sí está registrada por su cuenta. Si no lo está ni ella ni nada que
		// empiece por ella, es una ruta fantasma.
		fantasma := true
		for p := range registradas {
			if strings.HasPrefix(p, patron+"/") {
				fantasma = false
				break
			}
		}
		if fantasma {
			t.Errorf("la constante %s vale %q y NINGUNA ruta del router responde a ese patrón: el "+
				"código sigue nombrando una ruta que ya no existe. Un despachador, un BodyLimit o un "+
				"gate cableado contra ella no se dispara nunca, y nada más lo dice", nombre, patron)
		}
	}
}

// constantesConFormaDeRuta recorre el AST de los .go NO-test de este directorio y devuelve
// nombre→valor de cada constante de paquete cuyo valor de cadena empieza por "/".
//
// Se leen los ficheros del disco y no una lista compilada porque una constante no exportada y sin uso
// no deja rastro en tiempo de ejecución: el compilador de Go la borra sin quejarse (a diferencia de
// una variable, que sí sería un error de compilación). Justamente ésa es la que hay que cazar.
//
// 🔴 RESUELVE LA CONCATENACIÓN, y no es un adorno. Un candado que COMPONE el literal que protege no
// se encuentra buscando el literal, y es un fallo ya cometido en este ecosistema. La consola del
// cliente —que es donde acaba de aterrizar esta pantalla— declara su ruta de la sugerencia así:
//
//	const rutaSugerenciaCompleta = rutaSolicitudes + rutaSolicitudDetalle + sufijoSugerir
//
// Un colector que solo mirara literales daría por buena esa constante sin haberla mirado, que es
// exactamente el silencio que este test existe para romper. Se resuelve en pasadas hasta punto fijo
// para no depender del orden de declaración ni del orden alfabético de los ficheros.
func constantesConFormaDeRuta(t *testing.T) map[string]string {
	t.Helper()

	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo leer el paquete: %v", err)
	}

	fset := token.NewFileSet()
	var specs []*ast.ValueSpec
	vistos := 0
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || filepath.Ext(nombre) != ".go" || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		vistos++
		fichero, err := parser.ParseFile(fset, nombre, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", nombre, err)
		}
		for _, decl := range fichero.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					specs = append(specs, vs)
				}
			}
		}
	}
	if vistos == 0 {
		t.Fatal("no se parseó ni un fichero .go: el recolector no está mirando donde cree")
	}

	// Punto fijo: cada pasada resuelve las que ya pueden resolverse con lo conocido hasta entonces.
	conocidas := map[string]string{}
	for {
		antes := len(conocidas)
		for _, vs := range specs {
			for i, valor := range vs.Values {
				if i >= len(vs.Names) {
					break
				}
				nombre := vs.Names[i].Name
				if _, ya := conocidas[nombre]; ya {
					continue
				}
				if v, ok := evalCadenaConstante(valor, conocidas); ok {
					conocidas[nombre] = v
				}
			}
		}
		if len(conocidas) == antes {
			break
		}
	}

	out := map[string]string{}
	for nombre, v := range conocidas {
		if strings.HasPrefix(v, "/") {
			out[nombre] = v
		}
	}
	return out
}

// evalCadenaConstante resuelve un literal de cadena, una referencia a otra constante ya conocida, o
// la suma de las dos cosas. Cualquier otra expresión se declara irresoluble y se deja para otra
// pasada (o para nunca, si de verdad no es una cadena constante).
func evalCadenaConstante(e ast.Expr, conocidas map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		s, ok := conocidas[v.Name]
		return s, ok
	case *ast.ParenExpr:
		return evalCadenaConstante(v.X, conocidas)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		izq, ok := evalCadenaConstante(v.X, conocidas)
		if !ok {
			return "", false
		}
		der, ok := evalCadenaConstante(v.Y, conocidas)
		if !ok {
			return "", false
		}
		return izq + der, true
	}
	return "", false
}
