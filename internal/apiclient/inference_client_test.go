package apiclient

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// errCableEquivocado es el sentinela del cliente envenenado: si una llamada sale por el cliente que
// no le toca, el error dice exactamente eso y no un fallo de red cualquiera.
var errCableEquivocado = errors.New("esta llamada salió por el cliente HTTP equivocado")

type cableCortado struct{}

func (cableCortado) RoundTrip(*http.Request) (*http.Response, error) { return nil, errCableEquivocado }

// envenena deja inservible uno de los dos clientes del Transport, para que la llamada solo pueda
// prosperar si usa el otro.
func envenena(c *http.Client) { c.Transport = cableCortado{} }

// TestLaSugerenciaViajaPorElClienteDeInferencia: con el cliente GENERAL inservible, la sugerencia
// tiene que funcionar igual. Es la prueba directa de que no usa el de 15s.
func TestLaSugerenciaViajaPorElClienteDeInferencia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rendered_text":"Total 65.50","source":"llm"}`)
	}))
	defer srv.Close()

	tr := NewTransport(srv.URL)
	envenena(tr.HTTPClient)

	out, err := NewIntakesClient(tr).SuggestIntakeQuote(context.Background(), "tok", "in-1")
	if err != nil {
		if errors.Is(err, errCableEquivocado) {
			t.Fatal("la sugerencia salió por el cliente HTTP general (15s), que es el que la mataba")
		}
		t.Fatalf("SuggestIntakeQuote: %v", err)
	}
	if out.RenderedText != "Total 65.50" {
		t.Errorf("texto sugerido inesperado: %q", out.RenderedText)
	}
}

// TestLasDemasLlamadasNoViajanPorElClienteDeInferencia es la otra mitad, y la que de verdad protege
// al BFF: el plazo largo no puede escaparse a ninguna otra llamada del apiclient. Con el cliente de
// inferencia inservible, todo lo demás sigue funcionando.
func TestLasDemasLlamadasNoViajanPorElClienteDeInferencia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/intakes":
			_, _ = io.WriteString(w, `{"intakes":[],"page":1,"page_size":50,"total":0}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/entitlements":
			_, _ = io.WriteString(w, `{"plan":"commerce","features":[],"cache_ttl_seconds":60}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	tr := NewTransport(srv.URL)
	envenena(tr.InferenceHTTPClient)

	if _, err := NewIntakesClient(tr).ListIntakes(context.Background(), "tok", IntakeFilter{}); err != nil {
		if errors.Is(err, errCableEquivocado) {
			t.Error("el listado de solicitudes se llevó el cliente de inferencia (55s), que no le toca")
		} else {
			t.Errorf("ListIntakes: %v", err)
		}
	}
	if _, err := NewEntitlementsClient(tr).GetEntitlements(context.Background(), "tok"); err != nil {
		if errors.Is(err, errCableEquivocado) {
			t.Error("las capacidades se llevaron el cliente de inferencia (55s), que no les toca")
		} else {
			t.Errorf("GetEntitlements: %v", err)
		}
	}
}

// TestElClienteDeInferenciaTieneUnSoloUso vigila la REGLA sobre el código, no sobre la conducta de
// una llamada concreta: el selector `.InferenceHTTPClient` solo puede aparecer en transport.go —que
// es quien declara y configura el campo— y dentro de SuggestIntakeQuote.
//
// Va sobre el AST y no como N tests de conducta porque el riesgo no es que la sugerencia deje de
// usarlo (eso lo caza el test de arriba), sino que MAÑANA otra llamada lo copie: un test por método
// existente no dice nada del método que todavía no está escrito, y este sí.
func TestElClienteDeInferenciaTieneUnSoloUso(t *testing.T) {
	const campo = "InferenceHTTPClient"
	const dueño = "SuggestIntakeQuote"

	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("no se pudo listar el paquete: %v", err)
	}

	fset := token.NewFileSet()
	usos := map[string]int{}
	revisados := 0
	for _, e := range entradas {
		nombre := e.Name()
		// transport.go queda fuera porque es el DUEÑO del campo: lo declara y lo configura. Los
		// _test.go, porque este mismo fichero lo nombra a propósito.
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") ||
			strings.HasSuffix(nombre, "_test.go") || nombre == "transport.go" {
			continue
		}
		fichero, err := parser.ParseFile(fset, nombre, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", nombre, err)
		}
		revisados++
		for _, decl := range fichero.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == campo {
					usos[fn.Name.Name]++
				}
				return true
			})
		}
	}
	// Sin este recuento, un filtro mal escrito dejaría el barrido en CERO ficheros y el test saldría
	// verde sin haber mirado nada.
	if revisados < 5 {
		t.Fatalf("el barrido solo miró %d ficheros del paquete: no está revisando nada", revisados)
	}

	if usos[dueño] == 0 {
		t.Errorf("%s ya no usa %s: la sugerencia volvió al cliente de 15s, que es el que la mata",
			dueño, campo)
	}
	delete(usos, dueño)
	for fn, n := range usos {
		t.Errorf("%s usa %s %d vez/veces: el plazo de 55s es de la sugerencia y de nadie más "+
			"(si de verdad hace falta otra llamada larga, se decide y se documenta, no se copia)",
			fn, campo, n)
	}
}

// TestWithInferenceTimeoutIgnoraElCero: un cero es «no configurado», y dejarlo pasar a un http.Client
// significaría SIN PLAZO —un cuelgue indefinido—, que es lo contrario de lo que se pretende.
func TestWithInferenceTimeoutIgnoraElCero(t *testing.T) {
	if got := NewTransport("http://x", WithInferenceTimeout(0)).InferenceHTTPClient.Timeout; got != DefaultInferenceTimeout {
		t.Errorf("con 0 el plazo debía quedarse en el default (%s), y quedó en %s",
			DefaultInferenceTimeout, got)
	}
	if got := NewTransport("http://x", WithInferenceTimeout(-1)).InferenceHTTPClient.Timeout; got != DefaultInferenceTimeout {
		t.Errorf("con un plazo negativo debía quedarse en el default (%s), y quedó en %s",
			DefaultInferenceTimeout, got)
	}
	if got := NewTransport("http://x", WithInferenceTimeout(7*time.Second)).InferenceHTTPClient.Timeout; got != 7*time.Second {
		t.Errorf("el plazo configurado debía mandar: se esperaba 7s y salió %s", got)
	}
	// Y el general no se toca: son dos plazos, no uno.
	if got := NewTransport("http://x", WithInferenceTimeout(7*time.Second)).HTTPClient.Timeout; got != defaultTimeout {
		t.Errorf("el cliente general debía conservar su plazo (%s) y salió %s", defaultTimeout, got)
	}
}
