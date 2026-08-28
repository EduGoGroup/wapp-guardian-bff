package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Origen de la sugerencia, tal como lo publica `source` en el 200. Vocabulario CERRADO del cloud
// (`quotetext.Origen*`), y va como constante por lo mismo que los motivos: lo compara la pantalla
// para decidir qué frase pinta, y una cadena suelta ahí no la revisa nadie.
const (
	// QuoteSourceLLM — el texto lo redactó el modelo con el estilo del tenant.
	QuoteSourceLLM = "llm"
	// QuoteSourceDeterministic — lo compuso el respaldo sobrio del cloud. SIEMPRE viene con motivo.
	QuoteSourceDeterministic = "deterministic"
)

// Motivos por los que la sugerencia salió del respaldo sobrio en vez del modelo (`fallback_reason`).
//
// 🔴 SON TRECE, NO SEIS, y el número importa porque quien los pinta tiene que traducirlos todos:
// cuatro los emite el generador (`quotetext/quotetext.go:116-129`) y NUEVE el verificador de precios
// (`quotetext/precios.go:145-171`), que viajan por ESTE MISMO campo —el propio cloud lo dice: «para
// quien lee el log son la misma pregunta»—. Un catálogo a medias no rompe nada visible: el motivo
// que falte se pinta con su clave cruda, que es exactamente la fuga que el test de la pantalla
// existe para cazar.
//
// Van declarados en el BFF y no importados porque este repo NO depende del cloud (son dos módulos
// separados): lo que hay es una copia con la fuente escrita al lado, y el test que las enumera es
// lo que la mantiene honesta.
const (
	// — Los CUATRO del generador —

	// QuoteFallbackNoExamples — el tenant no tiene ni historial aprobado ni semilla de estilo, así
	// que no hay voz que imitar. En este caso NO se llama al modelo: no es que se descarte su
	// respuesta, es que no ocurre la llamada.
	QuoteFallbackNoExamples = "sin_ejemplos"
	// QuoteFallbackProviderDown — no se pudo obtener el proveedor de la vía del tenant.
	QuoteFallbackProviderDown = "proveedor_no_disponible"
	// QuoteFallbackLLMFailed — el proveedor respondió con error (transporte, plazo o calidad).
	QuoteFallbackLLMFailed = "llm_fallo"
	// QuoteFallbackBadOutput — respondió, pero lo que devolvió no es el artefacto esperado.
	QuoteFallbackBadOutput = "salida_no_es_artefacto"

	// — Los NUEVE del verificador de precios (INV-2: el texto tiene que cuadrar con las líneas) —

	// QuoteFallbackDraftWithoutAmounts — el borrador no tiene ni un importe positivo. Es el único
	// que se emite por DOS caminos: también antes de llamar al modelo, cuando todas las líneas
	// están por confirmar.
	QuoteFallbackDraftWithoutAmounts = "borrador_sin_importes"
	// QuoteFallbackUnreadableText — la salida no es texto utilizable (no UTF-8, con caracteres de
	// control, vacía o pasada de largo).
	QuoteFallbackUnreadableText = "texto_ilegible"
	// QuoteFallbackUnreadableNumber — hay en el texto un número que no se puede leer como número.
	QuoteFallbackUnreadableNumber = "numero_ilegible"
	// QuoteFallbackTextWithoutAmounts — el texto no trae NI UN importe: no dice precios, así que no
	// es una cotización.
	QuoteFallbackTextWithoutAmounts = "texto_sin_importes"
	// QuoteFallbackMissingUnitPrice — falta en el texto el precio unitario de alguna línea.
	QuoteFallbackMissingUnitPrice = "falta_precio_de_linea"
	// QuoteFallbackMissingTotal — falta en el texto el total.
	QuoteFallbackMissingTotal = "falta_total"
	// QuoteFallbackForeignAmount — el texto trae un importe que no sale de ninguna línea.
	QuoteFallbackForeignAmount = "importe_ajeno"
	// QuoteFallbackForeignNumber — el texto trae un número sin marca de moneda, por encima de todo
	// lo que cuesta el pedido, que tampoco sale del borrador.
	QuoteFallbackForeignNumber = "numero_ajeno"
	// QuoteFallbackAmountsOutOfPlace — los importes salen del borrador pero no están donde les
	// toca: sobran, faltan o van en otro orden.
	QuoteFallbackAmountsOutOfPlace = "importes_fuera_de_sitio"
)

// IntakeQuoteSuggestion es el 200 de POST /api/v1/intakes/{id}/quote-suggestion (Plan 044 · T5.1):
// la cotización REDACTADA que se le propone al dueño, y la verdad sobre quién la escribió.
//
// 🔴 ESTA PUERTA NO ESCRIBE NADA. No aprueba, no transiciona, no le manda nada al cliente: devuelve
// un texto para PRECARGAR el formulario de aprobación, y quien aprueba sigue siendo el dueño por su
// propio camino. Que la máquina redacte por un lado y la persona apruebe por otro es lo que sostiene
// INV-1, y una firma que devolviera el detalle actualizado insinuaría lo contrario.
//
// 🔴 UN MODELO CAÍDO NO ES UN ERROR DE ESTA PUERTA: contesta 200 con el texto del respaldo sobrio y
// su `FallbackReason`. Quien lo pinte no debe tratar `deterministic` como un fallo — es una
// cotización utilizable con otra procedencia.
type IntakeQuoteSuggestion struct {
	// RenderedText es la cotización sugerida. Se llama IGUAL que el campo del cuerpo de `approve`
	// a propósito: es el texto que la consola copia de una respuesta al siguiente formulario.
	RenderedText string `json:"rendered_text"`
	// Source es QuoteSourceLLM o QuoteSourceDeterministic.
	Source string `json:"source"`
	// FallbackReason dice POR QUÉ no fue el modelo. Vacío cuando sí lo fue.
	FallbackReason string `json:"fallback_reason"`
}

// FromLLM responde si la redactó el modelo.
func (s *IntakeQuoteSuggestion) FromLLM() bool { return s.Source == QuoteSourceLLM }

// SuggestIntakeQuote pide la cotización redactada con la voz de la dueña, vía
// POST /api/v1/intakes/{id}/quote-suggestion (Plan 044 · T5.1).
//
// NO LLEVA CUERPO, y eso es el contrato: todo lo que hace falta está en el token (el tenant) y en la
// ruta (la solicitud). El cloud ni siquiera lee el cuerpo.
//
// Errores: *LinesWithoutPriceError (400 con la lista entera — el MISMO cuerpo que `approve`, porque
// es la misma precondición sobre el mismo objeto), *FeatureNotEnabledError (403 con la capacidad que
// falta), *RejectionError para el otro 400 —la solicitud no tiene líneas que cotizar— y *APIError
// para 404/5xx.
func (c *IntakesClient) SuggestIntakeQuote(ctx context.Context, accessToken, id string) (*IntakeQuoteSuggestion, error) {
	const op = "intake quote-suggestion"
	req, err := c.t.newAuthedRequest(ctx, http.MethodPost,
		"/api/v1/intakes/"+url.PathEscape(id)+"/quote-suggestion", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s: %w", op, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, quoteSuggestionError(op, resp)
	}
	var out IntakeQuoteSuggestion
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	return &out, nil
}

// quoteSuggestionError traduce un no-2xx del generador. Lee el cuerpo UNA vez y decide con él, por lo
// mismo que reanalyzeError: el código HTTP no basta —los DOS cuerpos de 400 piden consejos distintos
// y solo los separa la clave `error`—.
//
// 🔴 NO HAY RAMA PARA 502/503, y no falta: con el modelo muerto esta puerta responde 200. Un 5xx
// aquí es un fallo del store del cloud, no del LLM, y cae en el genérico.
func quoteSuggestionError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error   string          `json:"error"`
		Lines   []IntakeLineRef `json:"lines"`
		Feature string          `json:"feature"`
	}
	// Un cuerpo ilegible deja el motivo en blanco: el status sigue siendo la información principal y
	// el llamante tiene su texto genérico (mismo criterio que intakeActionError).
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxIntakeItemsErrorBody)).Decode(&body)

	switch resp.StatusCode {
	case http.StatusBadRequest:
		if body.Error == errLinesWithoutPrice {
			return &LinesWithoutPriceError{Lines: body.Lines}
		}
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
	case http.StatusForbidden:
		// El 403 de esta ruta lo emite el middleware de entitlements del cloud, y es la ÚNICA de la
		// bandeja que lleva `llm_intake` en su cadena. Se traduce al mismo tipo que usa `/reanalyze`
		// para que la pantalla escriba UN paywall y no dos.
		if body.Error == errFeatureNotEnabled {
			return &FeatureNotEnabledError{Feature: body.Feature}
		}
	}
	return statusError(op, resp.StatusCode)
}
