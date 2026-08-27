package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// intakeActionFieldReanalyzeText es el campo del material EXTRA del formulario de Regenerar. Va como
// constante por lo mismo que los otros dos: lo lee el handler y lo escribe la plantilla, y un
// desajuste entre los dos no lo detecta el compilador.
const intakeActionFieldReanalyzeText = "reanalyze_text"

// intakeAPILLMFeature es el add-on de la vía externa. NO se comprueba antes de llamar —para saber si
// la vía efectiva es `api` habría que leer la configuración del tenant, y esa lectura exige
// justamente este add-on—, así que llega siempre como RECHAZO del re-análisis, nunca como gate
// previo. Se nombra aquí para poder redactar su paywall.
const intakeAPILLMFeature = "api_llm"

// DoReanalyzeIntake pide re-interpretar la solicitud desde el literal original del cliente
// (Plan 044 · T4.7 sobre el endpoint de T4.6).
//
// 🔴 Esta puerta NO devuelve la interpretación nueva, y la pantalla no puede fingir que sí: la
// plataforma abre un trabajo que corre por detrás, responde con el número que la revisión TENDRÁ y
// un `status` que vale siempre «processing» —que no es el estado del job, que nace `pending`—. Al
// volver, el detalle se relee y sigue enseñando la interpretación anterior, porque la nueva todavía
// no existe. El aviso lo dice con esas palabras.
//
// 🔴 Y NO se manda `via`: cambiar de vía es un acto de configuración del tenant, no un botón de la
// bandeja (D-044.51). Ver reanalyzeIntakeRequest en el apiclient.
func (h *IntakesHandler) DoReanalyzeIntake(c *gin.Context) {
	id, entitlements, ok := h.actionPreflight(c)
	if !ok {
		return
	}

	// El gate de `llm_intake` vive DENTRO del servicio de la plataforma y no en su middleware, así
	// que la bandeja abre y esta ruta no. Se comprueba antes de gastar el viaje —mismo criterio que
	// `actionPreflight` con `cart_basic`—, y el motivo se dice: es el plan REAL de UAT.
	if !entitlements.Has(intakeLLMFeature) {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusForbidden, id: id, entitlements: &entitlements,
			notice: &intakeNotice{Message: reanalyzeFeatureNotice(intakeLLMFeature)},
		})
		return
	}

	text := strings.TrimSpace(c.PostForm(intakeActionFieldReanalyzeText))
	if utf8.RuneCountInString(text) > intakeReanalyzeMaxRunes {
		h.renderIntakeDetail(c, intakeDetailRender{
			status: http.StatusBadRequest, id: id, entitlements: &entitlements, reanalyzeText: text,
			notice: &intakeNotice{Message: "No se pidió nada: el material extra son como mucho " +
				strconv.Itoa(intakeReanalyzeMaxRunes) + " caracteres y ahí van " +
				strconv.Itoa(utf8.RuneCountInString(text)) + ". Recórtalo y vuelve a intentarlo."},
		})
		return
	}

	var out *apiclient.IntakeReanalysis
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var rerr error
		out, rerr = h.api.ReanalyzeIntake(c.Request.Context(), accessToken, id, text)
		return rerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapReanalyzeError(err)
		h.renderIntakeDetail(c, intakeDetailRender{
			status: status, id: id, entitlements: &entitlements, notice: notice, reanalyzeText: text,
		})
		return
	}

	h.renderIntakeDetail(c, intakeDetailRender{
		status: http.StatusOK, id: id, entitlements: &entitlements,
		notice: &intakeNotice{Success: true, Message: reanalyzeAcceptedNotice(out)},
	})
}

// reanalyzeAcceptedNotice redacta el 200, y su trabajo es NO prometer lo que no ha pasado.
//
// La plataforma dice «processing» siempre y anuncia un `revision_no` que todavía no existe: si esta
// pantalla escribiera «listo», el dueño recargaría, vería la interpretación vieja y creería que la
// regeneración falló. Lo que se dice es que se encargó, con qué número aparecerá y que hay que
// volver.
func reanalyzeAcceptedNotice(out *apiclient.IntakeReanalysis) string {
	msg := "Regeneración encargada. TODAVÍA NO ESTÁ LISTA: la plataforma la procesa por detrás y lo " +
		"que ves debajo sigue siendo la interpretación anterior."
	if out.RevisionNo > 0 {
		msg += " Cuando termine aparecerá como la revisión " + strconv.Itoa(out.RevisionNo) + "."
	}
	if out.JobID != "" {
		msg += " Trabajo " + out.JobID + "."
	}
	return msg + " Vuelve a abrir esta solicitud en un momento para verla."
}

// reanalyzeFeatureNotice redacta el paywall de una capacidad que falta. Las dos que pueden faltar
// llevan a contratar cosas distintas, así que se nombran.
func reanalyzeFeatureNotice(feature string) string {
	switch feature {
	case intakeLLMFeature:
		return "No se pidió nada: el plan de este tenant no incluye el análisis con IA (`" +
			intakeLLMFeature + "`). La bandeja se lee igual; lo que hace falta contratar es volver a " +
			"interpretar la solicitud."
	case intakeAPILLMFeature:
		return "No se pidió nada: la vía configurada de este tenant es la API externa y el plan no " +
			"incluye ese add-on (`" + intakeAPILLMFeature + "`). O se contrata el add-on, o se cambia " +
			"la vía a la local desde los ajustes (`" + intakeTenantLLMSettings + "`)."
	}
	if strings.TrimSpace(feature) == "" {
		return "No se pidió nada: el plan de este tenant no incluye esta capacidad, y la plataforma no " +
			"dijo cuál."
	}
	// Una capacidad que esta consola no conoce se nombra TAL CUAL: misma doctrina que
	// intakeStatusLabel — antes una clave cruda que una traducción inventada.
	return "No se pidió nada: el plan de este tenant no incluye `" + feature + "`."
}

// mapReanalyzeError traduce el fallo del re-análisis a un aviso legible sin filtrar el detalle del
// upstream (mismo criterio que el resto de mappers del BFF).
//
// 🔴 El 403 de capacidad y el 422 de credencial NO se dicen igual, y el contrato los separa a
// propósito: el 403 es «tu plan no lo incluye» y lleva a contratar; el 422 es «te falta la
// credencial» y lleva a los ajustes. Mezclarlos mandaría al dueño a comprar algo que ya tiene.
func mapReanalyzeError(err error) (int, *intakeNotice) {
	if missing, ok := apiclient.FeatureNotEnabledOf(err); ok {
		return http.StatusForbidden, &intakeNotice{Message: reanalyzeFeatureNotice(missing.Feature)}
	}
	if creds, ok := apiclient.LLMCredentialsMissingOf(err); ok {
		msg := "No se pidió nada: el plan SÍ incluye la vía externa, pero este tenant no tiene " +
			"credencial configurada"
		if creds.Via != "" {
			msg += " para la vía «" + creds.Via + "»"
		}
		return http.StatusUnprocessableEntity, &intakeNotice{
			Message: msg + ". No hay nada que contratar: se configura en los ajustes de LLM del tenant (`" +
				intakeTenantLLMSettings + "`).",
		}
	}
	if source, ok := apiclient.SourceUnavailableOf(err); ok {
		return http.StatusUnprocessableEntity, &intakeNotice{
			Message: "No se pudo regenerar. " + intakeSourceView{Reason: source.Reason}.ReasonText(),
		}
	}
	if running, ok := apiclient.ReanalysisInProgressOf(err); ok {
		msg := "Ya hay una regeneración en curso para esta solicitud, así que no se encargó otra."
		if running.JobID != "" {
			msg += " Trabajo " + running.JobID + "."
		}
		return http.StatusUnprocessableEntity, &intakeNotice{
			Message: msg + " Espera a que termine y recarga la página.",
		}
	}
	if invalid, ok := apiclient.InvalidViaOf(err); ok {
		// Esta consola NO manda vía, así que este rechazo no debería poder ocurrir. Si ocurre, se
		// dice con todas las letras en vez de esconderlo en el aviso genérico: significa que alguien
		// reintrodujo el campo, y eso es exactamente lo que D-044.51 prohíbe.
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la vía «" + invalid.Via + "». Esta pantalla no propone " +
				"ninguna: la vía la fija la configuración del tenant (`" + intakeTenantLLMSettings + "`).",
		}
	}
	if rej, ok := apiclient.RejectionOf(err); ok && rej.Message != "" {
		return http.StatusBadRequest, &intakeNotice{
			Message: "La plataforma rechazó la regeneración: " + rej.Message,
		}
	}
	return mapIntakeActionStatusError(err, "regenerar")
}
