package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Motivos tipados que publica POST /api/v1/intakes/{id}/reanalyze (contrato §8.1 del Plan 044).
//
// Van como constantes porque los CINCO se distinguen por la clave `error` del cuerpo y no por el
// código: dos comparten el 403 y tres comparten el 422, y tratarlos igual sería llevar al dueño al
// paywall cuando lo que le falta es una credencial.
const (
	errFeatureNotEnabled     = "feature_not_enabled"
	errLLMCredentialsMissing = "llm_credentials_missing"
	errSourceUnavailable     = "source_unavailable"
	errReanalysisInProgress  = "reanalysis_in_progress"
	errInvalidVia            = "invalid_via"
)

// Razones por las que no hay literal del que re-analizar. Van EXPORTADAS porque la pantalla las
// necesita en dos sitios distintos y tienen que decir lo mismo en los dos: al leer el detalle
// —donde se deducen de `source_text` + `literal_pruned_at`— y al leer el 422 de `/reanalyze`. Dos
// literales sueltos acabarían discrepando.
const (
	SourcePurged      = "purged"
	SourceNeverStored = "never_stored"
)

// IntakeReanalysis es el 200 de POST /api/v1/intakes/{id}/reanalyze.
//
// 🔴 Los dos campos que más fácil se leen mal, y por eso se documentan aquí y no en la pantalla:
//
//   - `Status` vale SIEMPRE «processing» y NO es el estado del job (que nace `pending`). Es la
//     palabra con la que el endpoint dice «te acepté el encargo», nada más.
//   - `RevisionNo` es el número que la revisión TENDRÁ. Cuando esta respuesta llega, la revisión
//     todavía NO EXISTE: por eso esta ruta —a diferencia de aprobar o corregir— no devuelve el
//     detalle. Prometer en la UI que ya está lista sería prometer algo que no ha pasado.
type IntakeReanalysis struct {
	IntakeID   string `json:"intake_id"`
	RevisionNo int    `json:"revision_no"`
	JobID      string `json:"job_id"`
	Via        string `json:"via"`
	Status     string `json:"status"`
}

// reanalyzeIntakeRequest es el cuerpo de POST /api/v1/intakes/{id}/reanalyze.
//
// 🔴 NO TIENE CAMPO `via`, Y ESO ES LA DECISIÓN, no un olvido (D-044.51). El contrato acepta un
// `via` opcional pero solo para AFIRMAR la vía ya configurada del tenant: mandar una distinta es un
// 400 `invalid_via`, porque cambiar de vía es un acto de configuración (`PUT /api/v1/tenant-llm`,
// con su consentimiento) y no un parámetro de una llamada suelta que mandaría el texto de un
// cliente a un tercero de pago. Omitirlo es EQUIVALENTE a afirmar la configurada y, además, no
// puede desincronizarse el día que el tenant la cambie.
//
// `Text` es material EXTRA del dueño (una transcripción pegada): SUMA al literal del hilo, no lo
// sustituye. Vacío ⇒ no se manda, que es el caso corriente («regenera otra vez, según el origen»).
type reanalyzeIntakeRequest struct {
	Text string `json:"text,omitempty"`
}

// FeatureNotEnabledError es el 403 del contrato: al plan del tenant le falta una capacidad. Trae
// CUÁL, porque las dos que puede faltar llevan a sitios distintos —`llm_intake` es la bandeja con
// IA y `api_llm` el add-on de la vía externa— y un aviso genérico dejaría al dueño sin saber qué
// contratar.
type FeatureNotEnabledError struct {
	Feature string
}

func (e *FeatureNotEnabledError) Error() string {
	return fmt.Sprintf("apiclient: el plan del tenant no incluye %q", e.Feature)
}

// FeatureNotEnabledOf extrae el rechazo por capacidad (nil, false si no lo es).
func FeatureNotEnabledOf(err error) (*FeatureNotEnabledError, bool) {
	var missing *FeatureNotEnabledError
	if errors.As(err, &missing) {
		return missing, true
	}
	return nil, false
}

// LLMCredentialsMissingError es el 422 `llm_credentials_missing`: la feature SÍ está, la credencial
// no.
//
// 🔴 Es un caso DISTINTO del 403 y el contrato los separa a propósito: el 403 es «tu plan no lo
// incluye» y lleva al paywall; este es «configura tus credenciales» y lleva a los ajustes. Tratarlos
// igual mandaría a comprar algo que el tenant ya tiene.
type LLMCredentialsMissingError struct {
	Via string
}

func (e *LLMCredentialsMissingError) Error() string {
	return fmt.Sprintf("apiclient: la vía %q no tiene credencial configurada", e.Via)
}

// LLMCredentialsMissingOf extrae el rechazo por credencial (nil, false si no lo es).
func LLMCredentialsMissingOf(err error) (*LLMCredentialsMissingError, bool) {
	var missing *LLMCredentialsMissingError
	if errors.As(err, &missing) {
		return missing, true
	}
	return nil, false
}

// SourceUnavailableError es el 422 `source_unavailable`: no hay literal del que re-analizar. `Reason`
// distingue las dos historias —`purged` (existió y venció por retención) y `never_stored` (el plan
// del tenant no guardaba el texto cuando ocurrió)—, y son dos mensajes distintos porque una es una
// pérdida y la otra nunca fue una promesa.
type SourceUnavailableError struct {
	Reason string
}

func (e *SourceUnavailableError) Error() string {
	return fmt.Sprintf("apiclient: no hay literal del que re-analizar (%s)", e.Reason)
}

// Purged responde si el literal EXISTIÓ y se podó.
func (e *SourceUnavailableError) Purged() bool { return e.Reason == SourcePurged }

// SourceUnavailableOf extrae el rechazo por fuente (nil, false si no lo es).
func SourceUnavailableOf(err error) (*SourceUnavailableError, bool) {
	var missing *SourceUnavailableError
	if errors.As(err, &missing) {
		return missing, true
	}
	return nil, false
}

// ReanalysisInProgressError es el 422 `reanalysis_in_progress`: ya hay un trabajo no terminal para
// esta solicitud. Trae el job para poder nombrarlo, que es lo único con lo que el dueño distingue
// «no se hizo» de «se está haciendo».
type ReanalysisInProgressError struct {
	JobID string
}

func (e *ReanalysisInProgressError) Error() string {
	return fmt.Sprintf("apiclient: ya hay un re-análisis en curso (job %q)", e.JobID)
}

// ReanalysisInProgressOf extrae el rechazo por concurrencia (nil, false si no lo es).
func ReanalysisInProgressOf(err error) (*ReanalysisInProgressError, bool) {
	var running *ReanalysisInProgressError
	if errors.As(err, &running) {
		return running, true
	}
	return nil, false
}

// InvalidViaError es el 400 `invalid_via`.
//
// ⚠️ Este cliente NUNCA manda `via`, así que en teoría no puede provocarlo. Se traduce igual —y no
// se deja caer en el error genérico— porque si algún día llega significa que alguien reintrodujo el
// campo, y un aviso nombrado es lo que lo delata en vez de un «no se pudo, inténtalo de nuevo».
type InvalidViaError struct {
	Via string
}

func (e *InvalidViaError) Error() string {
	return fmt.Sprintf("apiclient: la plataforma rechazó la vía %q", e.Via)
}

// InvalidViaOf extrae el rechazo por vía (nil, false si no lo es).
func InvalidViaOf(err error) (*InvalidViaError, bool) {
	var invalid *InvalidViaError
	if errors.As(err, &invalid) {
		return invalid, true
	}
	return nil, false
}

// ReanalyzeIntake pide re-interpretar la solicitud desde el literal original del cliente, vía
// POST /api/v1/intakes/{id}/reanalyze (Plan 044 · T4.7 sobre el endpoint de T4.6).
//
// `text` es material EXTRA opcional del dueño (una transcripción pegada) y SUMA al origen. La vía NO
// viaja: sale de la configuración del tenant (ver reanalyzeIntakeRequest).
//
// 🔴 El 200 NO significa que la nueva interpretación esté lista: abre un trabajo que corre por
// detrás, y la revisión que anuncia todavía no existe. Quien pinte esta respuesta tiene que decirlo.
//
// Errores: *FeatureNotEnabledError (403, con la capacidad que falta), *LLMCredentialsMissingError,
// *SourceUnavailableError, *ReanalysisInProgressError e *InvalidViaError para los cuerpos nombrados
// del §8.1; *RejectionError para el resto de 400 con motivo; y *APIError para 404/409/5xx.
func (c *IntakesClient) ReanalyzeIntake(ctx context.Context, accessToken, id, text string) (*IntakeReanalysis, error) {
	const op = "intake reanalyze"
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPost,
		"/api/v1/intakes/"+url.PathEscape(id)+"/reanalyze",
		reanalyzeIntakeRequest{Text: text}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: %s: %w", op, err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, reanalyzeError(op, resp)
	}
	var out IntakeReanalysis
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	return &out, nil
}

// reanalyzeError traduce un no-2xx del re-análisis. Lee el cuerpo UNA vez y decide con él, porque el
// código HTTP NO basta: el 403 puede ser dos capacidades distintas y el 422 tres historias distintas,
// y solo las separa la clave `error`.
func reanalyzeError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error   string `json:"error"`
		Feature string `json:"feature"`
		Via     string `json:"via"`
		Reason  string `json:"reason"`
		JobID   string `json:"job_id"`
	}
	// Un cuerpo ilegible deja el motivo en blanco: el status sigue siendo la información principal y
	// el llamante tiene su texto genérico (mismo criterio que intakeActionError).
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxIntakeItemsErrorBody)).Decode(&body)

	switch resp.StatusCode {
	case http.StatusBadRequest:
		if body.Error == errInvalidVia {
			return &InvalidViaError{Via: body.Via}
		}
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
	case http.StatusForbidden:
		if body.Error == errFeatureNotEnabled {
			return &FeatureNotEnabledError{Feature: body.Feature}
		}
	case http.StatusUnprocessableEntity:
		switch body.Error {
		case errLLMCredentialsMissing:
			return &LLMCredentialsMissingError{Via: body.Via}
		case errSourceUnavailable:
			return &SourceUnavailableError{Reason: body.Reason}
		case errReanalysisInProgress:
			return &ReanalysisInProgressError{JobID: body.JobID}
		}
	}
	return statusError(op, resp.StatusCode)
}
