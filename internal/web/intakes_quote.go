package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"
	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// intakeQuoteView es el botón «Sugerir la respuesta» y, cuando ya se pidió, DE DÓNDE salió el texto
// que quedó en el campo de aprobar.
//
// El botón NUNCA se esconde, igual que Regenerar y por lo mismo: sin `llm_intake` sale DESHABILITADO
// con la razón delante. Esconderlo dejaría a la dueña sin saber que existe una redacción automática
// ni por qué no la tiene, que es peor que un botón apagado con su motivo. El gate DURO de esta
// pantalla sigue siendo `cart_basic`, que se la lleva entera desde la plantilla.
type intakeQuoteView struct {
	Enabled bool
	// Reason es por qué NO se puede pulsar (vacío cuando Enabled).
	Reason string
	// Paywall es si el motivo es del PLAN, que decide si el aviso lleva a contratar o a otro sitio.
	Paywall bool
	// Suggested es si esta página viene de una sugerencia recién pedida. Solo entonces se pinta el
	// origen: fuera de ese caso el campo de aprobar lleva la propuesta de siempre —la que arma la
	// consola con las líneas— y decir «lo redactó el modelo» sería mentir sobre un texto que el
	// modelo no ha visto.
	Suggested bool
	// OriginText es QUIÉN redactó lo que hay en el campo, ya redactado para la dueña.
	OriginText string
	// MaxWait es CUÁNTO espera esta página antes de rendirse, en palabras y ya redondeado.
	//
	// 🔴 SALE DEL PLAZO CONFIGURADO, no de un número escrito en la plantilla, y esa es toda la
	// razón de que este campo exista. El aviso decía «unos segundos» porque se redactó cuando la
	// espera moría a los 15s; en cuanto la ruta pasó a esperar de verdad, la frase se quedó
	// mintiéndole a la dueña —lo medido son 24,8-35,5s— sin que nada fallara. Colgado del plazo,
	// mover el plazo mueve el texto.
	MaxWait string
}

// quoteViewOf decide si el botón se puede pulsar, y si no, POR QUÉ.
//
// Solo hay UN motivo que se pueda anticipar aquí —la capacidad—, y no es un hueco: los otros dos
// desenlaces malos (líneas sin precio y solicitud sin líneas) dependen del estado del borrador en la
// plataforma, y adivinarlos desde el espejo local sería apagar el botón por una foto vieja. Llegan
// como RECHAZO, y mapQuoteSuggestionError los dice con lo que hay que hacer.
func quoteViewOf(ent entitlementsView, suggestion *apiclient.IntakeQuoteSuggestion,
	espera time.Duration) intakeQuoteView {
	view := intakeQuoteView{MaxWait: quoteWaitText(espera)}
	if !ent.Has(intakeLLMFeature) {
		view.Paywall = true
		view.Reason = quoteFeatureNotice(intakeLLMFeature)
	} else {
		view.Enabled = true
	}
	if suggestion != nil {
		view.Suggested = true
		view.OriginText = quoteOriginText(suggestion)
	}
	return view
}

// quoteWaitText pone en palabras lo que la página espera antes de rendirse.
//
// Redondea a propósito: el aviso lo lee una persona que está decidiendo si quedarse mirando la
// pantalla, y «57 segundos» no le sirve mejor que «un minuto» — pero «unos segundos» sí le sirve
// PEOR, porque describe una espera que no es la que va a tener. El corte del minuto es donde la
// unidad deja de ayudar: por debajo se cuenta en segundos, por encima se dice en minutos.
func quoteWaitText(espera time.Duration) string {
	if espera <= 0 {
		// Sin plazo conocido no se inventa una magnitud: se dice lo único cierto, que espera a que
		// llegue. Es el caso de un Config armado a mano (los tests), no el de producción.
		return "que la plataforma conteste"
	}
	if espera < time.Minute {
		return strconv.Itoa(int(espera.Round(time.Second)/time.Second)) + " segundos"
	}
	minutos := int(espera.Round(time.Minute) / time.Minute)
	if minutos <= 1 {
		return "un minuto"
	}
	return strconv.Itoa(minutos) + " minutos"
}

// quoteFeatureNotice redacta el paywall de la sugerencia. Va aparte del de Regenerar porque lleva a
// contratar LO MISMO por una razón distinta, y decirlo con las palabras de la otra acción dejaría a
// la dueña buscando un botón que no es el que tiene delante.
func quoteFeatureNotice(feature string) string {
	if feature == intakeLLMFeature {
		return "El plan de este tenant no incluye el análisis con IA (`" + intakeLLMFeature +
			"`), así que la plataforma no puede redactar la respuesta. La solicitud se responde igual: " +
			"el campo de abajo trae la propuesta que arma esta consola con las líneas, y se edita a mano."
	}
	if strings.TrimSpace(feature) == "" {
		return "El plan de este tenant no incluye esta capacidad, y la plataforma no dijo cuál."
	}
	// Una capacidad que esta consola no conoce se nombra TAL CUAL: misma doctrina que
	// intakeStatusLabel — antes una clave cruda que una traducción inventada.
	return "El plan de este tenant no incluye `" + feature + "`."
}

// quoteOriginText redacta DE DÓNDE salió el texto que quedó en el campo.
//
// 🔴 El origen no es un adorno: es lo único que distingue «la voz de la dueña funciona» de «la voz
// de la dueña está apagada y le están sirviendo el texto sobrio desde hace semanas». Sin él, las dos
// pantallas serían idénticas.
func quoteOriginText(s *apiclient.IntakeQuoteSuggestion) string {
	switch s.Source {
	case apiclient.QuoteSourceLLM:
		return "Origen: LLM. Lo redactó el modelo imitando el estilo de tus cotizaciones aprobadas. " +
			"Léelo entero antes de enviarlo: quien responde sigues siendo tú."
	case apiclient.QuoteSourceDeterministic:
		return "Origen: texto determinista (NO lo redactó el modelo). Lo compuso la plataforma con el " +
			"formato sobrio, y se puede enviar igual. " + quoteFallbackText(s.FallbackReason)
	}
	if strings.TrimSpace(s.Source) == "" {
		return "Origen: la plataforma no dijo quién redactó este texto."
	}
	// Un origen que esta consola no conoce se pinta TAL CUAL, misma doctrina que intakeViaText:
	// antes una clave cruda que una procedencia inventada sobre un texto que se le va a mandar a un
	// cliente.
	return "Origen: `" + s.Source + "` (esta consola no conoce ese origen)."
}

// quoteFallbackText traduce POR QUÉ no lo redactó el modelo.
//
// 🔴 SON TRECE MOTIVOS, no seis: cuatro los emite el generador y NUEVE el verificador de precios del
// cloud, y los nueve viajan por este mismo campo. La lista está enumerada entera en el test
// hermano, que falla si alguno cae en el genérico — porque un motivo sin traducir no rompe nada
// visible: se cuela como clave cruda en una pantalla que lee una persona que no programa.
//
// Los trece se agrupan en TRES historias, y esa agrupación es el trabajo de esta función: la dueña
// no necesita saber cuál de las cinco comprobaciones de importes falló, necesita saber si esto se
// arregla contratando algo, esperando, o mirando los precios del borrador.
func quoteFallbackText(reason string) string {
	switch reason {
	// (1) NO SE LLAMÓ AL MODELO, y no es un fallo de nadie.
	case apiclient.QuoteFallbackNoExamples:
		return "Motivo: todavía no hay cotizaciones aprobadas de las que aprender tu estilo, así que " +
			"no se llamó al modelo. En cuanto apruebes unas cuantas, empezará a sonar como tú."
	case apiclient.QuoteFallbackDraftWithoutAmounts:
		return "Motivo: el borrador no tiene ni un importe cerrado, así que no había precios que " +
			"escribir. Pon precios a las líneas de arriba y vuelve a pedirla."

	// (2) EL MODELO NO CONTESTÓ, o contestó algo inservible. Se reintenta pulsando otra vez.
	case apiclient.QuoteFallbackProviderDown:
		return "Motivo: el modelo no estaba disponible en este momento. Puedes volver a pedirla en un rato."
	case apiclient.QuoteFallbackLLMFailed:
		return "Motivo: el modelo falló al redactar (se cayó, tardó demasiado o devolvió algo que no " +
			"servía). Puedes volver a pedirla."
	case apiclient.QuoteFallbackBadOutput, apiclient.QuoteFallbackUnreadableText:
		return "Motivo: el modelo contestó algo que no se puede usar como texto. Puedes volver a pedirla."

	// (3) EL MODELO CONTESTÓ Y SUS NÚMEROS NO CUADRABAN CON EL PEDIDO. Este grupo es el importante:
	// el texto se descartó para que a nadie se le mande un precio que la plataforma no respalda.
	case apiclient.QuoteFallbackUnreadableNumber:
		return "Motivo: el texto del modelo traía un número que no se puede leer como precio, y no se " +
			"manda una cotización con un importe dudoso."
	case apiclient.QuoteFallbackTextWithoutAmounts:
		return "Motivo: el texto del modelo no decía ni un precio, así que no era una cotización."
	case apiclient.QuoteFallbackMissingUnitPrice:
		return "Motivo: al texto del modelo le faltaba el precio de alguna línea."
	case apiclient.QuoteFallbackMissingTotal:
		return "Motivo: al texto del modelo le faltaba el total."
	case apiclient.QuoteFallbackForeignAmount:
		return "Motivo: el texto del modelo traía un importe que no sale de ninguna línea de este " +
			"pedido — un precio inventado, y eso no se le manda a un cliente."
	case apiclient.QuoteFallbackForeignNumber:
		return "Motivo: el texto del modelo traía un número grande que no sale de este pedido y que se " +
			"podría leer como un precio."
	case apiclient.QuoteFallbackAmountsOutOfPlace:
		return "Motivo: los precios del texto del modelo eran los del pedido, pero mal colocados " +
			"(cambiados de línea, repetidos o de más), así que la cuenta no cuadraba."
	}
	if strings.TrimSpace(reason) == "" {
		return "La plataforma no dijo por qué."
	}
	// Un motivo que esta consola no conoce se nombra TAL CUAL. Es feo A PROPÓSITO: significa que el
	// cloud publicó uno nuevo y que aquí falta traducirlo, y una frase amable lo escondería.
	return "Motivo (sin traducir en esta consola): `" + reason + "`."
}

// quoteSuggestedNotice es el acuse de la sugerencia, en UN solo sitio porque lo pintan DOS caminos
// —el redirect y el repinte de reserva— y dos copias se separan en cuanto alguien toque una.
const quoteSuggestedNotice = "Propuesta lista en el campo de abajo. NO SE HA ENVIADO NADA y la " +
	"solicitud sigue donde estaba: léela, cámbiala si hace falta y aprueba tú."

// intakeDetailPath es la pantalla de UNA solicitud: el destino del redirect y el Path de la cookie
// efímera. Se compone en un solo sitio porque los dos tienen que coincidir EXACTAMENTE —el navegador
// identifica una cookie por la terna (dominio, ruta, nombre)—, y dos literales iguales escritos
// aparte se desalinean el día que alguien mueva la ruta.
func intakeDetailPath(id string) string { return "/intakes/" + id }

// intakeQuoteFlash es lo que viaja del POST al GET dentro de la cookie efímera. Las claves son de una
// letra a propósito: el presupuesto de una cookie es de bytes, y el nombre del campo se paga tantas
// veces como el valor.
type intakeQuoteFlash struct {
	// ID es la solicitud a la que pertenece este texto. Ver takeQuoteFlash: es la SEGUNDA cerradura.
	ID string `json:"i"`
	// Text es la cotización redactada.
	Text string `json:"t"`
	// Source y Fallback son de dónde salió y por qué, para que el GET pinte la MISMA línea de origen
	// que pintaba el POST. Sin ellos el redirect perdería justo lo que distingue «la voz de la dueña
	// funciona» de «lleva semanas apagada».
	Source   string `json:"s"`
	Fallback string `json:"f,omitempty"`
}

// maxQuoteCookieValue es cuánto se admite meter en la cookie de la cotización, en bytes del valor ya
// codificado.
//
// 🔴 EXISTE PORQUE EL MECANISMO QUE SE REUTILIZA AQUÍ NO SE ESCRIBIÓ PARA ESTO. La cookie de un solo
// uso de `wapp-shared/web` nació en la Ola A para un token de invitación —43 caracteres— y para un
// código de enrolamiento, y NO tiene guarda de tamaño. Una cotización no tiene largo acotado: el
// cloud no le pone tope (verificado el 2026-08-29: no hay ni un límite de longitud sobre
// `rendered_text` en `internal/intakes/`), y el navegador DESCARTA EN SILENCIO una cookie que pase de
// unos 4 KB — sin error, sin aviso, sin nada que falle. Sin este número, el desenlace de una
// cotización larga sería el peor posible: la dueña espera 40 s, la página redirige, y el texto NO
// ESTÁ. Peor que no haber hecho el PRG.
//
// El presupuesto: ~4096 B por cookie contando nombre y atributos, y el valor va en base64 (crece un
// tercio). 3000 B de valor codificado dejan sitio de sobra para lo demás y admiten un texto de unos
// 2 KB, que es varias veces la cotización más larga que se ha visto en campo.
const maxQuoteCookieValue = 3000

// setQuoteFlash guarda la cotización para el GET siguiente. Devuelve si CUPO: cuando no cupo, quien
// llama tiene que pintar sobre el POST, que es lo que se hacía siempre.
//
// 🔑 No perder el texto manda sobre hacer el PRG. Son dos cosas buenas y solo una es imprescindible:
// el PRG ahorra teclas, pero perder una cotización que costó 40 s de modelo es un daño de verdad.
func (h *IntakesHandler) setQuoteFlash(c *gin.Context, id string, out *apiclient.IntakeQuoteSuggestion) bool {
	valor, err := sharedweb.EncodeCookiePayload(intakeQuoteFlash{
		ID: id, Text: out.RenderedText, Source: out.Source, Fallback: out.FallbackReason,
	})
	if err != nil {
		// Sin el texto en el log: es contenido de negocio del cliente (§7.6, la misma razón del
		// `no-store` de la página).
		slog.Warn("no se pudo empaquetar la cotización para el redirect", "error", err, "intake_id", id)
		return false
	}
	if len(valor) > maxQuoteCookieValue {
		slog.Info("cotización demasiado larga para la cookie: se pinta sobre el POST",
			"intake_id", id, "bytes", len(valor), "tope", maxQuoteCookieValue)
		return false
	}
	webgin.SetOneTimeCookie(c, quoteCookieOptions(h.cfg, id), valor)
	return true
}

// takeQuoteFlash lee la cotización que dejó el POST y BORRA la cookie en el mismo gesto, que es lo
// que hace que el texto sobreviva UNA vez: el F5 siguiente ya no encuentra nada.
//
// 🔴 COMPRUEBA QUE EL TEXTO ES DE ESTA SOLICITUD, y no es una comprobación de adorno. Es la segunda
// de las dos cerraduras del comentario de quoteCookieOptions: si por lo que sea la cookie llegara a
// la pantalla de otra solicitud, aquí se descarta. Pintar la cotización de A en la pantalla de B
// pondría los precios de A delante de quien está a punto de responderle a B.
func (h *IntakesHandler) takeQuoteFlash(c *gin.Context, id string) *apiclient.IntakeQuoteSuggestion {
	raw := webgin.TakeOneTimeCookie(c, quoteCookieOptions(h.cfg, id))
	if raw == "" {
		return nil
	}
	var flash intakeQuoteFlash
	if err := sharedweb.DecodeCookiePayload(raw, &flash); err != nil {
		slog.Warn("la cookie de la cotización llegó ilegible", "error", err, "intake_id", id)
		return nil
	}
	if flash.ID != id || strings.TrimSpace(flash.Text) == "" {
		slog.Warn("la cookie de la cotización no es de esta solicitud, o venía vacía",
			"intake_id", id, "cookie_intake_id", flash.ID)
		return nil
	}
	return &apiclient.IntakeQuoteSuggestion{
		RenderedText: flash.Text, Source: flash.Source, FallbackReason: flash.Fallback,
	}
}

// DoSuggestIntakeQuote pide la cotización redactada con la voz de la dueña y la deja EN EL CAMPO de
// aprobar, editable y sin enviar nada (Plan 047 · T2.4 sobre el endpoint del Plan 044 · T5.1).
//
// 🔴 ESTO NO APRUEBA NI ENVÍA. La sugerencia es una propuesta que precarga el formulario; quien
// responde al cliente sigue siendo la dueña, pulsando «Aprobar y responder» después de leerla. Que
// sean dos actos y no uno es lo que sostiene INV-1, y por eso este handler no llama a ApproveIntake
// por ningún camino.
//
// 🔴 Y un modelo caído NO se pinta como error: la plataforma responde 200 con el texto determinista,
// y lo que cambia es la línea del ORIGEN, no el aviso.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LOS PLAZOS DE ESTA RUTA SON PROPIOS — Y ESO ES LO QUE LA HACE POSIBLE
// ════════════════════════════════════════════════════════════════════════════
//
// Ésta es la única llamada del BFF que espera a que un modelo redacte, y con los plazos generales
// no cabía. Medido el 2026-08-28, con los cuatro números delante:
//
//   - el cloud le da a ESTA llamada al modelo 48 s
//     (`bootstrap.go:1367` → `quotetext.ConPlazo(pipeline.PlazoPorLlamadaSuelo)`, y
//     `pipeline.go:207` dice 48 s), y en UAT tardó 24,8 / 28,4 / 29,7 / 35,5 s;
//   - el `http.Client` general de este BFF corta cada llamada a los 15 s;
//   - el deadline por petición del grupo `protected` son 20 s;
//   - y el `WriteTimeout` del servidor, 30 s — que además cortaba SIN dejar pintar pantalla.
//
// O sea: EL BFF CORTABA PRIMERO, siempre. Hoy los tres plazos de esta ruta —y solo de ésta— son
// 55 s / 58 s / 60 s: el cliente de inferencia del apiclient
// (`Transport.InferenceHTTPClient`), el deadline que instala `requestDeadlineByRoute` y el write
// deadline de `quoteSuggestionWriteDeadline`. Los tres viven en `internal/web/quote_deadlines.go`
// con su razonamiento; ninguno de los generales se movió.
//
// 🔴 HUECO DECLARADO, y es consecuencia del plazo largo, no un descuido: con 55 s de llamada bajo
// un deadline de petición de 58 s, la cadena `withAuthRetry` NO tiene sitio para su reintento. Si
// justo aquí cayera un 401, no hay presupuesto para refrescar y repetir una inferencia de 55 s —no
// cabría bajo ningún WriteTimeout razonable—, así que la dueña vería el aviso y pulsaría otra vez.
// Es raro por construcción: el AuthMiddleware refresca proactivamente a dos minutos del
// vencimiento, así que un token que entra vivo a esta ruta no expira dentro de ella.
// ════════════════════════════════════════════════════════════════════════════
func (h *IntakesHandler) DoSuggestIntakeQuote(c *gin.Context) {
	id, entitlements, ok := h.actionPreflight(c)
	if !ok {
		return
	}

	// DEFENSA EN PROFUNDIDAD. El botón ya sale deshabilitado sin la capacidad, pero un `disabled` es
	// del navegador y un POST a mano no lo tiene: sin `llm_intake` aquí no se llama al cloud. Es
	// además el mismo criterio que `/reanalyze` —no gastar un viaje a una ruta que la plataforma va
	// a rechazar—, solo que en esta ruta el gate SÍ vive en el middleware del cloud.
	if !entitlements.Has(intakeLLMFeature) {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusForbidden, id: id, entitlements: &entitlements,
			notice: &intakeNotice{Message: "No se pidió nada. " + quoteFeatureNotice(intakeLLMFeature)},
		})
		return
	}

	var out *apiclient.IntakeQuoteSuggestion
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var serr error
		out, serr = h.api.SuggestIntakeQuote(c.Request.Context(), accessToken, id)
		return serr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapQuoteSuggestionError(err)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: status, id: id, entitlements: &entitlements, notice: notice,
		})
		return
	}

	// ════════════════════════════════════════════════════════════════════════════
	// POST-REDIRECT-GET (Plan 047 · T3.5) — Y AQUÍ EL MOTIVO ES EL COSTE, NO EL RIESGO
	// ════════════════════════════════════════════════════════════════════════════
	//
	// Ésta es la ÚNICA acción de la bandeja que se lleva al PRG, y la razón es que es la única cara:
	// las otras siete repintan sobre el POST —la casa entera lo hace, 27 llamadas a
	// renderIntakeDetail— y un F5 encima de ellas cuesta una llamada barata. Encima de ésta cuesta
	// 20-40 s de modelo, medidos en campo (22,1 s en la corrida que cerró T2.4). No se convierte la
	// familia entera a PRG por el camino: eso sería otra tarea, y esta pantalla es provisional.
	//
	// 🔑 El mecanismo NO se inventa: es la cookie de un solo uso que la Ola A construyó y probó para
	// el token de invitación (`wapp-shared/web`, ya en el go.mod de este BFF). Lo único que hay de
	// nuevo es el tope de tamaño, que ese mecanismo no traía — ver maxQuoteCookieValue.
	//
	// 🔴 Y los desenlaces MALOS de esta ruta siguen pintando sobre el POST, a propósito: son baratos
	// de repetir (el cloud los rechaza sin llamar al modelo) y llevan un código de estado —400, 403—
	// que un 303 borraría. El PRG está aquí por el segundo que cuesta repetir la acción, no por la
	// estética de la barra de direcciones.
	if h.setQuoteFlash(c, id, out) {
		c.Redirect(http.StatusSeeOther, intakeDetailPath(id))
		return
	}

	// No cupo en la cookie: se pinta sobre el POST, como siempre. El texto no se pierde; lo que se
	// pierde es el PRG, y esa es la mitad prescindible.
	//
	// El texto entra por `approveText`, que es el MISMO carril por el que se repinta lo que la dueña
	// teclea tras un rechazo. No hay un segundo camino para «el texto de la máquina»: en cuanto está
	// en el campo, es un borrador suyo y se edita como tal.
	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id, entitlements: &entitlements,
		approveText: out.RenderedText, quote: out,
		notice: &intakeNotice{Success: true, Message: quoteSuggestedNotice},
	})
}

// mapQuoteSuggestionError traduce el fallo del generador a un aviso legible sin filtrar el detalle
// del upstream (mismo criterio que el resto de mappers del BFF).
//
// 🔴 El desenlace MÁS PROBABLE en campo es el primero —un borrador recién interpretado no tiene
// precios—, y por eso NO se dice como un error genérico: se dice qué líneas arreglar y dónde,
// exactamente igual que lo dice `approve`, porque es el mismo muro sobre el mismo objeto.
func mapQuoteSuggestionError(err error) (int, *intakeNotice) {
	if missing, ok := apiclient.LinesWithoutPriceOf(err); ok {
		return http.StatusBadRequest, &intakeNotice{
			Message: "No hay nada que sugerir todavía: " + linesWithoutPriceText(missing.Lines) +
				" Ponles precio en el borrador de arriba, guarda la corrección y vuelve a pedir la " +
				"propuesta. No se ha enviado nada.",
		}
	}
	if feature, ok := apiclient.FeatureNotEnabledOf(err); ok {
		return http.StatusForbidden, &intakeNotice{
			Message: "No se sugirió nada. " + quoteFeatureNotice(feature.Feature),
		}
	}
	if rej, ok := apiclient.RejectionOf(err); ok && rej.StatusCode == http.StatusBadRequest {
		// 🔑 Esta puerta tiene EXACTAMENTE DOS cuerpos de 400 —verificado en
		// `cloud/wapp-cloud-platform/internal/publicapi/quotesuggestion.go:113-133`: el de líneas sin
		// precio, que se trata arriba, y éste—. Se redacta con las palabras de la pantalla y NO se
		// vuelca el mensaje del upstream, que nombra `PUT /api/v1/intakes/{id}/items` y para la dueña
		// no significa nada. El día que el cloud añada un tercer 400 con motivo, este aviso dirá algo
		// falso: la contramedida es el test que enumera los dos, que empezará a describir mal el
		// caso nuevo en cuanto alguien lo mire.
		return http.StatusBadRequest, &intakeNotice{
			Message: "No hay nada que sugerir: esta solicitud no tiene líneas que cotizar. Guarda " +
				"primero las líneas del borrador de arriba y vuelve a pedir la propuesta.",
		}
	}
	return mapIntakeActionStatusError(err, "sugerir la respuesta de")
}
