package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSuggestIntakeQuoteVaSinCuerpoYPorPOST fija el contrato en BYTES: la llamada va por POST a la
// ruta de la solicitud y NO LLEVA CUERPO.
//
// Que no lleve cuerpo no es una simplificación: el cloud ni siquiera lo lee, y mandar parámetros
// —cuántos ejemplos, qué tono, qué vía— sería dejar que una llamada suelta se saltara la
// configuración del tenant, que es el mismo argumento por el que `/reanalyze` no acepta `provider`.
func TestSuggestIntakeQuoteVaSinCuerpoYPorPOST(t *testing.T) {
	var method, path, auth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"rendered_text":"Tu pedido: 1 torta — 45.00\nTotal: 45.00","source":"llm"}`)
	}))
	defer srv.Close()

	client := NewIntakesClient(NewTransport(srv.URL))
	out, err := client.SuggestIntakeQuote(context.Background(), "tok", "in-1")
	if err != nil {
		t.Fatalf("SuggestIntakeQuote: %v", err)
	}

	if method != http.MethodPost {
		t.Errorf("debía ir por POST —consume una inferencia y no es cacheable—, got %s", method)
	}
	if path != "/api/v1/intakes/in-1/quote-suggestion" {
		t.Errorf("ruta inesperada: %s", path)
	}
	if auth != "Bearer tok" {
		t.Errorf("debía viajar el token del operador, got %q", auth)
	}
	if len(body) != 0 {
		t.Errorf("esta puerta NO lleva cuerpo, got %q", body)
	}
	if out.RenderedText == "" || !out.FromLLM() {
		t.Errorf("el 200 debía llegar entero, got %+v", out)
	}
	if out.FallbackReason != "" {
		t.Errorf("con `llm` no hay motivo que dar, got %q", out.FallbackReason)
	}
}

// TestSuggestIntakeQuoteDecodificaElRespaldo: el 200 con el texto determinista trae SU MOTIVO, y los
// dos campos llegan. Un modelo caído no es un error de esta puerta y este cliente no lo convierte en
// uno.
func TestSuggestIntakeQuoteDecodificaElRespaldo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w,
			`{"rendered_text":"Tu pedido:\nTotal: 45.00","source":"deterministic","fallback_reason":"llm_fallo"}`)
	}))
	defer srv.Close()

	out, err := NewIntakesClient(NewTransport(srv.URL)).
		SuggestIntakeQuote(context.Background(), "tok", "in-1")
	if err != nil {
		t.Fatalf("un respaldo NO es un error: %v", err)
	}
	if out.FromLLM() {
		t.Error("`deterministic` no lo redactó el modelo")
	}
	if out.Source != QuoteSourceDeterministic || out.FallbackReason != QuoteFallbackLLMFailed {
		t.Errorf("el origen y el motivo debían llegar enteros, got %+v", out)
	}
}

// TestSuggestIntakeQuoteTipaSusRechazos: cada rechazo llega como el tipo que la pantalla necesita
// para redactar un consejo distinto. Un `*APIError` genérico para todos obligaría a la consola a
// decir «no se pudo» donde hay algo concreto que hacer.
func TestSuggestIntakeQuoteTipaSusRechazos(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{
			// El desenlace MÁS PROBABLE en campo, y el MISMO cuerpo que devuelve `approve`: es la
			// misma precondición sobre el mismo objeto, así que el tipo también es el mismo.
			name: "400 lines_without_price trae la lista entera", status: http.StatusBadRequest,
			body: `{"error":"lines_without_price","lines":[{"index":2,"label":"Torta vainilla"}]}`,
			check: func(t *testing.T, err error) {
				missing, ok := LinesWithoutPriceOf(err)
				if !ok {
					t.Fatalf("debía ser *LinesWithoutPriceError, got %T", err)
				}
				if len(missing.Lines) != 1 || missing.Lines[0].Index != 2 ||
					missing.Lines[0].Label != "Torta vainilla" {
					t.Errorf("la línea debía llegar por posición Y etiqueta, got %+v", missing.Lines)
				}
			},
		},
		{
			name: "400 sin clave conocida llega como rechazo con su mensaje", status: http.StatusBadRequest,
			body: `{"error":"la solicitud no tiene líneas que cotizar"}`,
			check: func(t *testing.T, err error) {
				rej, ok := RejectionOf(err)
				if !ok {
					t.Fatalf("debía ser *RejectionError, got %T", err)
				}
				if rej.StatusCode != http.StatusBadRequest || rej.Message == "" {
					t.Errorf("el rechazo debía conservar código y motivo, got %+v", rej)
				}
				if _, isLines := LinesWithoutPriceOf(err); isLines {
					t.Error("un 400 sin `lines_without_price` NO es el de las líneas sin precio")
				}
			},
		},
		{
			// Esta es la ÚNICA ruta de la bandeja con `llm_intake` en la cadena del cloud, así que es
			// la única que puede devolver este 403.
			name: "403 feature_not_enabled dice QUÉ capacidad falta", status: http.StatusForbidden,
			body: `{"error":"feature_not_enabled","feature":"llm_intake"}`,
			check: func(t *testing.T, err error) {
				missing, ok := FeatureNotEnabledOf(err)
				if !ok {
					t.Fatalf("debía ser *FeatureNotEnabledError, got %T", err)
				}
				if missing.Feature != "llm_intake" {
					t.Errorf("debía nombrar la capacidad, got %q", missing.Feature)
				}
			},
		},
		{
			// 404 y no 403 cuando la solicitud es de otro tenant: el cloud no confirma que ese id
			// exista (INV-8), y este cliente no puede decir más de lo que sabe.
			name: "404 se queda en el código", status: http.StatusNotFound, body: `{"error":"no encontrada"}`,
			check: func(t *testing.T, err error) {
				if got := StatusCodeOf(err); got != http.StatusNotFound {
					t.Errorf("debía conservar el 404, got %d (%v)", got, err)
				}
			},
		},
		{
			name: "500 se queda en el código", status: http.StatusInternalServerError, body: `{"error":"boom"}`,
			check: func(t *testing.T, err error) {
				if got := StatusCodeOf(err); got != http.StatusInternalServerError {
					t.Errorf("debía conservar el 500, got %d (%v)", got, err)
				}
			},
		},
		{
			name: "401 sale como ErrUnauthorized", status: http.StatusUnauthorized, body: `{}`,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrUnauthorized) {
					t.Errorf("debía envolver ErrUnauthorized, got %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			out, err := NewIntakesClient(NewTransport(srv.URL)).
				SuggestIntakeQuote(context.Background(), "tok", "in-1")
			if err == nil {
				t.Fatalf("debía fallar, got %+v", out)
			}
			tc.check(t, err)
		})
	}
}
