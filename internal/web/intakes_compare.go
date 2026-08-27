package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// intakeLLMFeature es la capacidad que abre el RE-ANÁLISIS. NO es la misma que abre la bandeja:
// las páginas y las siete rutas de lectura van tras `cart_basic` (D-044.47 §1) y `/reanalyze` exige
// además `llm_intake` DENTRO del servicio, no en el middleware, y eso es deliberado.
//
// 🔴 De ahí sale el primer estado de borde de esta pantalla, que además es el plan REAL de UAT: un
// tenant con `cart_basic` y sin `llm_intake` abre la solicitud, la lee entera… y el botón Regenerar
// le devolvería un 403. Por eso el botón se pinta DESHABILITADO con la razón a la vista.
const intakeLLMFeature = "llm_intake"

// intakeVIAUnknownText es lo que dice el encabezado de una revisión cuando `analysis.provider` viene
// vacío (D-044.52 (b)).
//
// 🔴 Y viene vacío en el caso COMÚN: la plataforma solo rellena `provider` en el re-análisis del
// dueño (`intake/stages/draft.go:625-654`; en cualquier otro camino deja un `Warn` y el campo en
// blanco), así que la revisión 1 —la que nace del pipeline— no tiene vía que enseñar. Escribir aquí
// «LLM local» sería inventarse un hecho que nadie registró, y esta consola no lo hace ni cuando la
// suposición parece segura.
const intakeVIAUnknownText = "vía no registrada"

// intakeReanalyzeMaxRunes es el tope del material EXTRA que el dueño puede pegar (contrato §8.1).
// Se cuenta en RUNAS y no en bytes porque el contrato habla de runas y porque un texto con acentos
// —el caso normal en español— cabría de sobra en runas y se pasaría en bytes.
const intakeReanalyzeMaxRunes = 280

// intakeTenantLLMSettings nombra dónde se cambia la vía. Va como TEXTO y no como enlace, y es la
// misma razón por la que los adjuntos se nombran y no se enlazan (D-044.52 §1): esta consola NO
// tiene pantalla de ajustes de LLM —no existe ninguna ruta `/tenant-llm` en este BFF—, así que un
// `<a href>` llevaría a un 404. Cuando esa pantalla exista, aquí es donde se pone el enlace.
const intakeTenantLLMSettings = "PUT /api/v1/tenant-llm"

// intakeSourceView es la columna ORIGINAL DEL CLIENTE del §7.6: el literal, o por qué no está.
//
// 🔴 El literal LLEGA YA DESCIFRADO del cloud —lo descifra la lectura del detalle en el borde
// (`intakes/postgres.go:584`), y este BFF no tiene KEK ni la quiere—, así que aquí no se descifra
// nada. Lo que sí es responsabilidad de esta capa es que ese texto no se quede escrito en ningún
// sitio del camino: la respuesta va `no-store` y ninguna rama lo mete en el log.
type intakeSourceView struct {
	// Text es el literal del cliente. Vacío cuando no hay.
	Text string
	// Present es si hay literal que enseñar.
	Present bool
	// Reason es `purged` o `never_stored` cuando no lo hay (vacío si lo hay). Sale de DOS claves de
	// la revisión y no de un `422` de `/reanalyze`, que es una escritura auditada: preguntar por la
	// razón no puede costar una escritura.
	Reason string
	// PrunedAt es cuándo se podó (solo en `purged`).
	PrunedAt string
}

// ReasonText redacta por qué no hay original. Las dos razones se dicen DISTINTO a propósito: una es
// una pérdida —existió y venció— y la otra nunca fue una promesa.
func (s intakeSourceView) ReasonText() string {
	switch s.Reason {
	case apiclient.SourcePurged:
		text := "El texto original de esta conversación ya venció por la política de retención y se " +
			"podó, así que no hay de qué regenerar."
		if s.PrunedAt != "" {
			text += " Se podó el " + s.PrunedAt + "."
		}
		return text
	case apiclient.SourceNeverStored:
		return "No hay original guardado de esta conversación: cuando ocurrió, el plan de este tenant " +
			"no guardaba el texto del cliente. No se borró — nunca se guardó."
	}
	return ""
}

// intakeRevisionLink es UNA entrada de la navegación entre interpretaciones.
//
// 🔴 SIN JAVASCRIPT (ADR-0035): saltar de una revisión a otra es un enlace normal a la misma página
// con un parámetro de query, y el «después» lo pinta el servidor. No hay pestañas ni acordeones,
// porque no hay quien los mueva en el navegador.
type intakeRevisionLink struct {
	RevisionNo int
	URL        string
	// ViaText es la vía REGISTRADA en esa revisión, ya redactada («vía no registrada» si no consta).
	ViaText string
	// Current marca la que se está mirando: se pinta sin enlace, para que el enlace no prometa un
	// salto a donde ya se está.
	Current bool
}

// intakeRegenerateView es el botón Regenerar y, sobre todo, POR QUÉ no se puede pulsar.
//
// El botón nunca se esconde: los CUATRO estados de borde del §7.6 lo dejan DESHABILITADO con la
// razón delante. Esconderlo dejaría al dueño sin saber que existe una regeneración —ni por qué no
// la tiene—, que es peor que un botón apagado con su motivo.
type intakeRegenerateView struct {
	Enabled bool
	// Reason es por qué NO se puede (vacío cuando Enabled).
	Reason string
	// Paywall es si el motivo es del PLAN, que es lo que decide si el aviso lleva a contratar o a
	// otro sitio. El 403 de capacidad y el 422 de credencial no se dicen igual a propósito.
	Paywall bool
	// Text es el material extra que el dueño tecleó, para repintarlo tras un rechazo.
	Text string
	// MaxRunes es el tope del material extra, para poder decirlo en la pantalla.
	MaxRunes int
}

// intakeCompareView es el §7.6 entero: el original del cliente AL LADO de lo que se entendió, la
// navegación por las interpretaciones y el botón de regenerar.
//
// Se pinta sobre la revisión SELECCIONADA, que no tiene por qué ser la última. El borrador editable
// del §7.5, en cambio, sigue saliendo SIEMPRE de la última interpretada: navegar por el histórico
// es leer, no cambiar lo que se corrige, y confundir las dos cosas dejaría al dueño guardando
// precios sobre una lectura vieja.
type intakeCompareView struct {
	IntakeID   string
	RevisionNo int
	CreatedAt  string
	// RoleText es QUIÉN dejó la revisión, y es un ROL —no una persona—: la plataforma publica
	// `system`/`owner`/`crm` y esta consola no puede convertir eso en un nombre (cero PII).
	RoleText string
	// ViaText es la vía registrada en esta revisión, ya redactada.
	ViaText string
	// Model es el modelo que consta (vacío si no consta).
	Model string
	// ReanalyzedFrom es la revisión de la que salió este re-análisis (0 ⇒ es una primera lectura).
	ReanalyzedFrom int

	Source intakeSourceView
	// Lines es lo interpretado en la columna de al lado, en el mismo formato que el §7.5 para que
	// las dos digan lo mismo del mismo dato.
	Lines []intakeDraftLine
	// Units son las unidades TOTALES interpretadas y LineCount las líneas. Es lo que hace legible la
	// discrepancia del caso de las hamburguesas —1 pedida, 3 interpretadas— sin abrir nada más.
	Units     int
	LineCount int

	Revisions  []intakeRevisionLink
	Regenerate intakeRegenerateView

	// MissingRevision es la revisión que se pidió por query y no existe (0 si no pasó). Se dice en
	// vez de redirigir en silencio: un enlace viejo o tecleado a mano tiene que enterarse.
	MissingRevision int
}

// HeaderText redacta el encabezado de la interpretación que se está mirando.
func (v *intakeCompareView) HeaderText() string {
	text := "Interpretación · revisión " + strconv.Itoa(v.RevisionNo) + " · " + v.ViaText
	if v.Model != "" {
		text += " · modelo " + v.Model
	}
	if v.ReanalyzedFrom > 0 {
		text += " · re-análisis de la revisión " + strconv.Itoa(v.ReanalyzedFrom)
	}
	return text
}

// UnitsText redacta cuántas unidades se interpretaron. Es la mitad de la comparación: al lado del
// texto del cliente, «3 unidades interpretadas» es lo que hace saltar a la vista que se pidió una.
func (v *intakeCompareView) UnitsText() string {
	units := "1 unidad interpretada"
	if v.Units != 1 {
		units = strconv.Itoa(v.Units) + " unidades interpretadas"
	}
	if v.LineCount == 1 {
		return units + " en 1 línea"
	}
	return units + " en " + strconv.Itoa(v.LineCount) + " líneas"
}

// HasHistory responde si hay más de una interpretación que comparar. Con una sola no se pinta una
// navegación de un elemento, que solo diría «estás donde estás».
func (v *intakeCompareView) HasHistory() bool { return len(v.Revisions) > 1 }

// compareViewOf arma el §7.6 desde el detalle. Devuelve nil cuando no hay ninguna revisión
// `interpreted` (o su payload no se puede leer): la comparación no se pinta a medias, igual que el
// §7.5, y la página ya dice en ese caso que no hay interpretación.
//
// `asked` es el `?revision=N` de la query: sin él —o con uno que no existe— se mira la ÚLTIMA.
func compareViewOf(detail *apiclient.IntakeDetail, ent entitlementsView, r intakeDetailRender) *intakeCompareView {
	revisions := detail.RevisionsOf(apiclient.RevisionKindInterpreted)
	if len(revisions) == 0 {
		return nil
	}

	current := revisions[len(revisions)-1]
	missing := 0
	if r.revision > 0 && r.revision != current.RevisionNo {
		if found := revisionNumbered(revisions, r.revision); found != nil {
			current = found
		} else {
			missing = r.revision
		}
	}

	payload, err := apiclient.DecodeInterpretation(current.Payload)
	if err != nil {
		return nil
	}

	view := &intakeCompareView{
		IntakeID:        detail.ID,
		RevisionNo:      current.RevisionNo,
		CreatedAt:       current.CreatedAt,
		RoleText:        intakeRevisionRoleText(current.CreatedBy),
		ViaText:         intakeViaText(payload.Analysis.Provider),
		Model:           payload.Analysis.Model,
		Source:          sourceViewOf(current, payload),
		MissingRevision: missing,
	}
	if payload.Analysis.ReanalyzedFrom != nil {
		view.ReanalyzedFrom = *payload.Analysis.ReanalyzedFrom
	}
	for _, line := range payload.Lines {
		// El envío lo pone wApp y no es algo que el cliente pidiera: contarlo entre «lo que se
		// entendió» inflaría la discrepancia con una línea que no sale de su texto.
		if line.Kind == apiclient.LineKindShipping {
			continue
		}
		view.Lines = append(view.Lines, draftLineOf(line))
		view.Units += line.Qty
		view.LineCount++
	}
	view.Revisions = revisionLinksOf(detail.ID, revisions, current.RevisionNo)
	view.Regenerate = regenerateViewOf(ent, view.Source, r.reanalyzeText)
	return view
}

// revisionNumbered busca una interpretación por su NÚMERO (nil si no está). Va por `revision_no` y
// no por índice por lo mismo que LastRevisionOf: el orden del histórico no es contrato.
func revisionNumbered(revisions []*apiclient.IntakeRevision, no int) *apiclient.IntakeRevision {
	for _, rev := range revisions {
		if rev.RevisionNo == no {
			return rev
		}
	}
	return nil
}

// sourceViewOf decide cuál de los TRES casos del literal es, y es el corazón de D-044.52 §3.
//
// 🔴 La pregunta es de PRESENCIA DE CLAVE, no de valor:
//
//   - `source_text` con texto                      ⇒ hay literal;
//   - sin texto y SIN `literal_pruned_at`          ⇒ nunca lo hubo (`never_stored`);
//   - sin texto y CON `literal_pruned_at`          ⇒ se podó (`purged`), y trae cuándo.
//
// Un `source_text` vacío cuenta como ausente y no es una licencia: la plataforma lo emite con
// `omitempty`, y un literal vacío tampoco sería un literal. Lo que NO se puede colapsar es la otra
// clave, y por eso viaja como puntero.
func sourceViewOf(rev *apiclient.IntakeRevision, payload *apiclient.IntakeInterpretation) intakeSourceView {
	if text := strings.TrimSpace(payload.SourceText); text != "" {
		return intakeSourceView{Text: payload.SourceText, Present: true}
	}
	if rev.LiteralPruned() {
		return intakeSourceView{Reason: apiclient.SourcePurged, PrunedAt: rev.PrunedAt()}
	}
	return intakeSourceView{Reason: apiclient.SourceNeverStored}
}

// intakeViaText redacta la vía de una revisión. El vacío se dice «vía no registrada» y JAMÁS «LLM
// local»: ver el comentario de intakeVIAUnknownText. Una vía desconocida se pinta TAL CUAL, misma
// doctrina que intakeStatusLabel — antes una clave cruda que una traducción inventada.
func intakeViaText(provider string) string {
	if strings.TrimSpace(provider) == "" {
		return intakeVIAUnknownText
	}
	return "vía " + provider
}

// intakeRevisionRoleText redacta QUIÉN dejó la revisión. Es un ROL y se dice como tal: la plataforma
// publica `system`/`owner`/`crm` y no publica personas, así que esta consola no puede pintar un
// nombre ni insinuarlo (cero PII). Un rol desconocido se pinta tal cual.
func intakeRevisionRoleText(createdBy string) string {
	switch createdBy {
	case apiclient.RevisionBySystem:
		return "la dejó el sistema (rol `system`)"
	case apiclient.RevisionByOwner:
		return "la dejó el dueño (rol `owner`)"
	case apiclient.RevisionByCRM:
		return "la dejó el CRM (rol `crm`)"
	}
	if strings.TrimSpace(createdBy) == "" {
		return "no consta qué rol la dejó"
	}
	return "la dejó el rol `" + createdBy + "`"
}

// revisionLinksOf arma la navegación. Cada entrada enseña SU vía —que es el punto de comparar— y la
// actual va sin URL.
func revisionLinksOf(intakeID string, revisions []*apiclient.IntakeRevision, currentNo int) []intakeRevisionLink {
	links := make([]intakeRevisionLink, 0, len(revisions))
	for _, rev := range revisions {
		link := intakeRevisionLink{
			RevisionNo: rev.RevisionNo,
			ViaText:    intakeVIAUnknownText,
			Current:    rev.RevisionNo == currentNo,
		}
		// La vía vive DENTRO del payload, así que cada entrada hay que abrirla. Un payload ilegible
		// no tumba la navegación: esa revisión se lista con la vía sin registrar, que es lo mismo
		// que dice cuando el campo viene vacío.
		if payload, err := apiclient.DecodeInterpretation(rev.Payload); err == nil {
			link.ViaText = intakeViaText(payload.Analysis.Provider)
		}
		if !link.Current {
			link.URL = intakeRevisionURL(intakeID, rev.RevisionNo)
		}
		links = append(links, link)
	}
	return links
}

// intakeRevisionURL arma el enlace a una revisión del detalle. Es el ÚNICO sitio donde se decide
// cómo viaja esa elección, por lo mismo que intakeFilteredURL en la bandeja.
//
// 🔴 El literal del cliente NUNCA entra en esta URL: lo que viaja es un número. El log de acceso de
// este BFF escribe el `path` de cada petición, así que meter texto de negocio en una URL sería
// escribirlo en el log sin que ninguna revisión de código lo viera venir.
func intakeRevisionURL(intakeID string, revisionNo int) string {
	return "/intakes/" + url.PathEscape(intakeID) + "?revision=" + strconv.Itoa(revisionNo)
}

// regenerateViewOf decide si el botón Regenerar se puede pulsar, y si no, POR QUÉ.
//
// El orden de los motivos es el del contrato §8.1 y no es cosmético: el gate de capacidad corta
// ANTES que la comprobación de la fuente, así que cuando faltan las dos cosas lo que el dueño ve es
// lo que la plataforma le diría — «tu plan no lo incluye», no «no hay original».
//
// Los otros dos bordes —el add-on `api_llm` y la credencial— NO se pueden anticipar aquí y no es un
// hueco: para saber que la vía efectiva es `api` habría que leer la configuración del tenant, y esa
// lectura exige justamente el add-on que falta. Llegan como RECHAZO del re-análisis, y el mapper los
// separa (403 ⇒ paywall, 422 ⇒ ajustes).
func regenerateViewOf(ent entitlementsView, source intakeSourceView, text string) intakeRegenerateView {
	view := intakeRegenerateView{Text: text, MaxRunes: intakeReanalyzeMaxRunes}
	switch {
	case !ent.Has(intakeLLMFeature):
		view.Paywall = true
		view.Reason = "El plan de este tenant no incluye el análisis con IA (`" + intakeLLMFeature +
			"`), así que la plataforma rechazaría la regeneración. La bandeja se lee igual: lo que " +
			"falta es volver a interpretar."
	case !source.Present:
		view.Reason = source.ReasonText()
	default:
		view.Enabled = true
	}
	return view
}
