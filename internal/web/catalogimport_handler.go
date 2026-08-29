package web

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// catalogImportFeature es la capacidad que abre el import de catálogo. Es la MISMA clave con la que
// la plataforma gatea la ruta (`RequireFeature("catalog_import")`): aquí decide lo que se PINTA,
// allí lo que se PUEDE, y esconder un formulario nunca sustituye a ese corte.
const catalogImportFeature = "catalog_import"

// catalogImportRoute es la ruta de la pantalla en el BFF (no la de la API pública).
const catalogImportRoute = "/catalog-import"

// maxCatalogImportBody es el techo del cuerpo que esta pantalla acepta, archivo incluido.
//
// NO es el límite del documento —ese lo pone la plataforma y responde 413—; es el tope con el que el
// BFF se protege de que una sesión legítima le mande un archivo de gigabytes que acabaría entero en
// memoria y en disco antes de que nadie lo mire. Va holgado sobre el techo del documento (1 MiB por
// defecto en la plataforma) para no rechazar aquí lo que allí sería válido.
const maxCatalogImportBody = 4 << 20

// catalogImportNavDataKey es la clave con la que una página declara que el enlace al import debe
// salir en la barra superior. Misma mecánica que el de la bandeja: si la página no resolvió las
// features, la clave no existe, se lee como falsa y el enlace no llega al HTML.
const catalogImportNavDataKey = "CatalogImportNav"

// defaultCatalogTemplateFormat es el formato de plantilla que sirve el enlace cuando no se pide otro.
const defaultCatalogTemplateFormat = "json"

// fallbackCatalogRef es la ref que el selector ofrece cuando el tenant NO tiene todavía ninguna:
// hay que poder estrenar catálogo, y un desplegable vacío no dejaría importar nunca.
//
// Es un ESPEJO declarado del `defaultCatalogRef` de la plataforma
// (`cloud/…/internal/publicapi/catalogimport.go`), no una segunda fuente de verdad: solo se usa
// cuando la lista viene vacía, y si algún día divergen el síntoma es que el primer import de un
// tenant nuevo crea la ref con otro nombre. La alternativa —mandar la ref vacía y dejar que la
// plataforma elija— es literalmente el defecto A3 que este selector cierra.
const fallbackCatalogRef = "catalogo"

// catalogImportNotice es el aviso de una operación sobre el import tal como lo lee la plantilla.
type catalogImportNotice struct {
	Success bool
	Message string
}

// catalogImportErrorRow es UN defecto del documento tal como se pinta.
//
// La ubicación se redacta en el sistema del que la va a leer: la fila de la planilla tal cual viene,
// y los índices del JSON pasados a base 1. No es un capricho: la prosa del motivo ya cuenta como
// cuenta una persona («el artículo 3 de la categoría 2»), así que dejar el índice crudo pondría dos
// números distintos para el mismo sitio en la misma fila. El Reason NO se toca: viaja verbatim desde
// la plataforma, donde se escribió para el dueño del negocio.
type catalogImportErrorRow struct {
	Location string
	// Field es el campo del contrato por el camino JSON y el nombre de la columna por el tabular. Se
	// pinta bajo un encabezado que admite las dos cosas en vez de inventarse dos tablas.
	Field  string
	Reason string
}

// catalogImportView es lo que pinta la plantilla.
type catalogImportView struct {
	// Document es lo que el operador pegó o subió. Se conserva para re-pintarlo cuando hay errores
	// (su trabajo no se tira) y para viajar oculto en la confirmación del apply.
	Document string
	// Errors son los defectos del documento. No vacío ⇒ no hay diff que enseñar.
	Errors []catalogImportErrorRow
	// Result es el diff, sea el de la comprobación o el de lo ya aplicado.
	Result *apiclient.CatalogImportResult
	// Prompt es el texto copiable para el asistente, pedido a la plataforma.
	Prompt catalogPromptView
	// Refs son las refs entre las que el operador elige en el paso 1: contra cuál se compara el
	// documento y sobre cuál se escribirá. NUNCA está vacía cuando la pantalla se pinta —ver
	// catalogImportRefOptions—, porque una ref vacía es exactamente el defecto A3.
	Refs []catalogRefOption
	// Ref es la que se eligió en el envío que se está re-pintando. Existe para que un documento
	// rechazado vuelva con SU ref todavía elegida: si se perdiera, el reintento se compararía
	// contra otra sin que nadie lo dijera.
	Ref string
}

// SelectedRef es la ref que el selector debe traer marcada. Manda la del resultado cuando lo hay
// —es la que la plataforma usó de verdad, no la que se pidió— y si no, la del envío.
func (v catalogImportView) SelectedRef() string {
	if v.Result != nil && v.Result.Ref != "" {
		return v.Result.Ref
	}
	return v.Ref
}

// catalogRefOption es UNA opción del selector de ref del paso 1.
type catalogRefOption struct {
	Value string
	// Selected marca la que viene elegida de fábrica. Es siempre exactamente una.
	Selected bool
	// Existing distingue una ref que YA tiene contenido (se va a reemplazar) de la que se ofrece
	// para estrenar. La pantalla lo dice con palabras porque no es lo mismo pisar que crear.
	Existing bool
}

// catalogPromptView es el prompt-plantilla tal como lo pinta la plantilla.
//
// NO hay copia local de reserva, y es a propósito: el texto está versionado junto al contrato en la
// plataforma y un respaldo pegado aquí sería exactamente la segunda fuente que el contrato evita —se
// quedaría vieja sin que nadie lo notara y le dictaría al asistente un formato que ya se rechaza—.
// Si no se puede pedir, la pantalla lo dice; el resto (pegar, comprobar, aplicar) sigue funcionando.
type catalogPromptView struct {
	Text    string
	Format  string
	Version int
	Loaded  bool
}

// Applied dice si lo que se está pintando ya se escribió en el catálogo. Con Result nulo es falso:
// no hay nada aplicado sin respuesta de la plataforma.
func (v catalogImportView) Applied() bool { return v.Result != nil && v.Result.Applied }

// AwaitingConfirmation dice si hay un diff comprobado esperando el «Aplicar». Es lo que separa los
// dos pasos de la pantalla: comprobar enseña, confirmar escribe.
func (v catalogImportView) AwaitingConfirmation() bool { return v.Result != nil && !v.Result.Applied }

// NoChanges dice si aplicar el documento dejaría el catálogo tal como está. Los avisos del catálogo
// vigente NO cuentan como cambio: describen lo que ya pasaba antes de este import (mismo criterio
// que la plataforma).
func (v catalogImportView) NoChanges() bool {
	if v.Result == nil {
		return false
	}
	d := v.Result.Diff
	return len(d.PriceChanges) == 0 && len(d.Added) == 0 && len(d.Removed) == 0 && len(d.ChangedDetails) == 0
}

// CatalogImportHandler sirve la pantalla de import de catálogo: pegar o subir el documento,
// comprobarlo y —en un segundo paso explícito— aplicarlo.
//
// PANTALLA PROVISIONAL (ADR-0035): muere cuando la operación pase a la app KMP (planes 045/047).
// Está construida para borrarse de una pieza —handler, plantilla y puerto propios—, no para crecer.
type CatalogImportHandler struct {
	cfg  *config.Config
	api  CatalogImportAPI
	auth *AuthHandler
}

// NewCatalogImportHandler construye el handler sobre el puerto del import.
func NewCatalogImportHandler(cfg *config.Config, api CatalogImportAPI, auth *AuthHandler) *CatalogImportHandler {
	return &CatalogImportHandler{cfg: cfg, api: api, auth: auth}
}

// ShowCatalogImport pinta el formulario vacío con el prompt y las plantillas.
func (h *CatalogImportHandler) ShowCatalogImport(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	h.render(c, http.StatusOK, ent, catalogImportView{}, nil)
}

// DoCatalogImport atiende los DOS pasos de la pantalla: comprobar (el normal) y aplicar (el que
// escribe). Cuál de los dos se pide lo dice el botón pulsado, y el que escribe SOLO existe en el
// formulario de confirmación, o sea después de haber enseñado el diff.
//
// El documento se re-envía entero en el apply en vez de guardarse entre pasos, y eso es lo que
// permite que el BFF no tenga estado: la plataforma re-valida y re-diffea lo que le llega, así que
// lo que se aplica es exactamente lo que se confirmó, aunque el operador tenga dos pestañas
// abiertas.
//
// LA PLANILLA ENTRA POR SU PUERTA Y SALE POR LA DEL JSON. Un CSV o un XLSX se comprueban en
// `/tabular`, que devuelve el documento ya traducido; ese documento —texto— es el que viaja al paso
// 2 por el import JSON de siempre. Así el camino que ESCRIBE es uno solo, y la garantía de aplicar
// exactamente lo enseñado sobrevive a un archivo binario que no cabría en un campo oculto.
func (h *CatalogImportHandler) DoCatalogImport(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(catalogImportFeature) {
		// Sin la capacidad no se llama a la API: la plataforma cortaría con 403 igualmente y el viaje
		// solo serviría para llenar el log de rechazos previsibles.
		h.render(c, http.StatusForbidden, ent, catalogImportView{}, nil)
		return
	}

	sub, msg := catalogSubmissionFrom(c)
	if msg != "" {
		h.render(c, http.StatusBadRequest, ent, catalogImportView{Document: sub.Document, Ref: sub.Ref},
			&catalogImportNotice{Message: msg})
		return
	}
	apply := c.PostForm("action") == "apply"

	var result *apiclient.CatalogImportResult
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var ierr error
		if sub.tabular() {
			result, ierr = h.api.ImportCatalogTabular(c.Request.Context(), accessToken,
				sub.Filename, sub.File, apply, sub.Ref)
			return ierr
		}
		result, ierr = h.api.ImportCatalog(c.Request.Context(), accessToken,
			[]byte(sub.Document), apply, sub.Ref)
		return ierr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		if invalid, ok := apiclient.CatalogImportInvalidOf(err); ok {
			// El documento vuelve a la pantalla con TODOS sus defectos: es una lista para corregir el
			// archivo, no un motivo de rechazo, y por eso no se resume ni se recorta.
			//
			// De una planilla rechazada NO vuelve documento —la plataforma no lo manda a medias— y el
			// cuadro de texto se queda vacío a propósito: el archivo se corrige en la hoja de cálculo,
			// que es donde están las filas que señalan los errores.
			h.render(c, http.StatusBadRequest, ent,
				catalogImportView{Document: sub.Document, Ref: sub.Ref, Errors: catalogErrorRows(invalid.Errors)},
				&catalogImportNotice{Message: catalogInvalidMessage(len(invalid.Errors))})
			return
		}
		status, notice := mapCatalogImportError(err, apply)
		h.render(c, status, ent, catalogImportView{Document: sub.Document, Ref: sub.Ref}, notice)
		return
	}

	h.render(c, http.StatusOK, ent,
		catalogImportView{Document: documentToConfirm(sub, result), Result: result},
		&catalogImportNotice{Success: true, Message: catalogResultMessage(result)})
}

// documentToConfirm decide qué texto viaja al paso 2. De la planilla sale el documento NORMALIZADO
// que devolvió la plataforma —el archivo binario no puede volver—; del JSON, lo que el operador ya
// tenía. Se reenvía tal cual llegó, sin re-serializar: el BFF no interpreta el contrato del catálogo.
func documentToConfirm(sub catalogSubmission, result *apiclient.CatalogImportResult) string {
	if len(result.Document) > 0 {
		return string(result.Document)
	}
	return sub.Document
}

// DownloadCatalogTemplate sirve la plantilla de ejemplo al navegador.
//
// Va por el BFF y no por un enlace directo a la plataforma porque el token vive server-side en una
// cookie HttpOnly: el navegador no lo tiene y un enlace a :8103 se llevaría un 401. El fallo NO se
// descarga: se vuelve a la pantalla con el aviso, que es donde el operador puede hacer algo.
func (h *CatalogImportHandler) DownloadCatalogTemplate(c *gin.Context) {
	ent := resolveEntitlements(c, h.auth, h.api)
	if !ent.Has(catalogImportFeature) {
		h.render(c, http.StatusForbidden, ent, catalogImportView{}, nil)
		return
	}

	format := strings.TrimSpace(c.Query("format"))
	if format == "" {
		format = defaultCatalogTemplateFormat
	}

	var tpl *apiclient.CatalogTemplate
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		tpl, gerr = h.api.GetCatalogTemplate(c.Request.Context(), accessToken, format)
		return gerr
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			clearSessionCookie(h.cfg, c)
			h.auth.redirectToLogin(c)
			return
		}
		status, notice := mapCatalogTemplateError(err)
		h.render(c, status, ent, catalogImportView{}, notice)
		return
	}

	// El nombre sale de la lista blanca del cliente, nunca de la respuesta del upstream: así ninguna
	// cabecera ajena decide cómo guarda el navegador un archivo servido desde este origen.
	c.Header("Content-Disposition", `attachment; filename="`+tpl.Filename+`"`)
	c.Data(http.StatusOK, tpl.ContentType, tpl.Content)
}

func (h *CatalogImportHandler) render(c *gin.Context, status int, ent entitlementsView, view catalogImportView, notice *catalogImportNotice) {
	// El prompt se pide solo cuando se va a pintar: sin la capacidad la plataforma cortaría con 403, y
	// con un diff esperando confirmación esa tarjeta ni siquiera se emite. Gastar el viaje en esos dos
	// casos solo serviría para llenar el log.
	if ent.Has(catalogImportFeature) && !view.AwaitingConfirmation() {
		view.Prompt = h.resolvePrompt(c)
		// El selector solo se arma cuando se pinta el paso 1. Con un diff esperando confirmación la
		// ref ya está decidida y viaja en su oculto: volver a listarla ahí sería un viaje de más y,
		// peor, una segunda oportunidad de cambiarla entre el diff y el apply.
		view.Refs = h.resolveRefs(c, view.SelectedRef())
	}
	render(h.cfg, c, status, "catalog-import.html", gin.H{
		"Title":                  "Catálogo",
		"View":                   view,
		"Notice":                 notice,
		entitlementsDataKey:      ent,
		intakesNavDataKey:        ent.Has(intakesFeature),
		catalogImportNavDataKey:  ent.Has(catalogImportFeature),
		integrationsNavDataKey:   ent.Has(integrationsFeature),
		tenantLLMNavDataKey:      ent.Has(tenantLLMFeature),
		"CatalogTemplateFormats": catalogTemplateLinks,
	})
}

// resolveRefs pide las refs de contenido del tenant y arma el selector del paso 1.
//
// Como resolvePrompt, no falla nunca hacia arriba: si la lista no se puede leer, la pantalla sigue
// sirviendo con la ref de arranque. Lo que NO hace —y es la diferencia con el prompt— es degradar a
// «sin opciones»: el selector tiene que ofrecer siempre algo que mandar, porque una ref vacía es el
// defecto que esto viene a cerrar.
func (h *CatalogImportHandler) resolveRefs(c *gin.Context, selected string) []catalogRefOption {
	var refs []apiclient.TenantContentRef
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		refs, gerr = h.api.ListTenantContentRefs(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil {
		slog.Warn("no se pudieron listar las refs de contenido (se ofrece solo la de arranque)", "error", err)
		refs = nil
	}
	return catalogImportRefOptions(refs, selected)
}

// catalogImportRefOptions convierte las refs del tenant en las opciones del desplegable.
//
// Tres reglas, y las tres salen del defecto A3:
//
//   - La lista NUNCA sale vacía. Sin refs (tenant nuevo, o el listado no se pudo leer) se ofrece
//     fallbackCatalogRef para estrenar.
//   - Hay SIEMPRE exactamente una opción marcada. Si `selected` viene con valor —el paso 2, que
//     tiene que aplicar sobre la misma ref contra la que se calculó el diff— manda esa, y si no
//     existe en la lista se añade igual: perder la ref elegida entre los dos pasos es justo el
//     fallo silencioso del que nace todo esto.
//   - El orden es el que devuelve la plataforma, sin reordenar por «parece un catálogo». Filtrar
//     por prefijo inventaría una convención que el contrato no tiene.
func catalogImportRefOptions(refs []apiclient.TenantContentRef, selected string) []catalogRefOption {
	opts := make([]catalogRefOption, 0, len(refs)+1)
	found := false
	for _, r := range refs {
		if r.Ref == "" {
			continue
		}
		opts = append(opts, catalogRefOption{Value: r.Ref, Existing: true})
		if r.Ref == selected {
			found = true
		}
	}
	if selected != "" && !found {
		opts = append(opts, catalogRefOption{Value: selected})
	}
	if len(opts) == 0 {
		opts = append(opts, catalogRefOption{Value: fallbackCatalogRef})
	}

	// La marca va en una segunda pasada para que haya exactamente una: con `selected` vacío manda la
	// primera, que es el caso del paso 1 recién abierto.
	want := selected
	if want == "" {
		want = opts[0].Value
	}
	for i := range opts {
		opts[i].Selected = opts[i].Value == want
	}
	return opts
}

// resolvePrompt pide el prompt-plantilla y devuelve SIEMPRE una vista usable: el fallo se traduce en
// la vista cero, nunca en un error que tumbe la página. Es información de ayuda, no la operación: el
// import tiene que poder hacerse aunque el texto para el asistente no se pueda cargar (mismo
// criterio que resolveEntitlements con las features).
func (h *CatalogImportHandler) resolvePrompt(c *gin.Context) catalogPromptView {
	var prompt *apiclient.CatalogPrompt
	err := h.auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		prompt, gerr = h.api.GetCatalogPrompt(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil || prompt == nil {
		slog.Warn("no se pudo leer el prompt del import (la pantalla sigue sirviendo)", "error", err)
		return catalogPromptView{}
	}
	return catalogPromptView{
		Text:    prompt.Prompt,
		Format:  prompt.Format,
		Version: prompt.Version,
		Loaded:  true,
	}
}

// catalogTemplateLink es un formato ofrecido en los enlaces de descarga.
type catalogTemplateLink struct {
	Format string
	Label  string
	Hint   string
}

// catalogTemplateLinks son las plantillas que la pantalla ofrece. Las tres las sirve el MISMO
// endpoint de la plataforma cambiando `format`, y las tres vuelven a entrar por esta pantalla: la
// planilla es la que se llena sin saber qué es un JSON.
var catalogTemplateLinks = []catalogTemplateLink{
	{Format: "json", Label: "Plantilla JSON", Hint: "el formato que se pega aquí"},
	{Format: "csv", Label: "Planilla CSV", Hint: "para llenar en una hoja de cálculo"},
	{Format: "xlsx", Label: "Planilla Excel", Hint: "para llenar en Excel"},
}

// catalogSubmission es lo que el operador mandó, ya resuelto: o un documento JSON (pegado, subido o
// el que viaja oculto en la confirmación) o una planilla que hay que subir tal cual.
type catalogSubmission struct {
	// Document es el JSON del catálogo. Vacío cuando lo que se manda es una planilla.
	Document string
	// File son los bytes del archivo subido cuando NO es un JSON, y Filename su nombre original.
	File     []byte
	Filename string
	// Ref es la ref contra la que se comprobó el paso 1, arrastrada al paso 2. Vacía = manda el
	// default de la plataforma.
	Ref string
}

// tabular dice si esto va por la puerta de la planilla.
func (s catalogSubmission) tabular() bool { return len(s.File) > 0 }

// catalogSubmissionFrom resuelve qué mandó el operador y por qué puerta tiene que salir. Devuelve el
// mensaje de error (vacío = todo bien) en vez de escribir la respuesta.
//
// LA PUERTA SE ELIGE POR EL CONTENIDO, NO POR LA EXTENSIÓN, igual que hace la plataforma para elegir
// el parser: si el archivo empieza por «{» es el documento JSON del contrato, y si no, es una
// planilla y se sube tal cual. Fiarse del `.csv` del nombre sería fiarse de algo que el operador
// cambia sin cambiar el archivo —y renombrar un XLSX a .json no debe romper nada.
//
// Lo ÚNICO que se comprueba es que se pueda enviar: que haya algo, y que lo PEGADO sea JSON (una
// planilla no se pega, se sube). Qué tiene de malo el documento lo dice el validador de la
// plataforma, que acumula todos los defectos y los redacta para un humano; repetir aquí ese criterio
// sería garantizar que los dos discrepen.
func catalogSubmissionFrom(c *gin.Context) (catalogSubmission, string) {
	sub := catalogSubmission{Ref: strings.TrimSpace(c.PostForm("ref"))}

	raw, filename, msg := uploadedCatalogFile(c)
	if msg != "" {
		return sub, msg
	}
	if len(raw) > 0 {
		if looksLikeJSONDocument(raw) {
			sub.Document = string(bytes.TrimSpace(raw))
			return sub, ""
		}
		// Los bytes van SIN TOCAR: ni se recorta el espacio de los bordes ni se toca el BOM, porque un
		// XLSX es binario y un CSV lo lee la plataforma, que ya sabe de BOM y de separadores.
		sub.File, sub.Filename = raw, filename
		return sub, ""
	}

	sub.Document = strings.TrimSpace(c.PostForm("document"))
	switch {
	case sub.Document == "":
		return sub, "Pega el documento del catálogo o elige un archivo antes de comprobarlo."
	case !strings.HasPrefix(sub.Document, "{"):
		return sub, "Lo que pegaste no es un documento JSON de catálogo: tiene que empezar por «{». " +
			"Si lo tuyo es una planilla, no la pegues aquí: súbela con el botón de archivo."
	}
	return sub, ""
}

// looksLikeJSONDocument decide si el archivo subido es el documento del contrato. Se mira el primer
// carácter con contenido, saltando el BOM que escriben algunos editores de Windows: sin eso, un JSON
// impecable guardado desde el Bloc de notas se iría por la puerta de las planillas y se rechazaría
// con un motivo que no le diría nada a nadie.
func looksLikeJSONDocument(raw []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))), []byte("{"))
}

// uploadedCatalogFile lee el archivo elegido, si lo hay. Devuelve nil cuando no hay archivo
// utilizable, y eso incluye el caso que manda de verdad el navegador: un `<input type=file>` sin
// elegir nada SÍ viaja, como una parte vacía. Tratarla como «hay archivo» dejaría al operador que
// pegó el documento con un «pega el documento» en la cara.
func uploadedCatalogFile(c *gin.Context) (raw []byte, filename, msg string) {
	fh, err := c.FormFile("file")
	if err != nil || fh == nil || fh.Size == 0 {
		return nil, "", ""
	}
	f, err := fh.Open()
	if err != nil {
		slog.Warn("no se pudo abrir el archivo de catálogo subido", "error", err)
		return nil, "", "No se pudo leer el archivo que subiste. Inténtalo de nuevo."
	}
	defer func() { _ = f.Close() }()

	// El tamaño ya viene acotado por BodyLimitMiddleware, así que aquí se lee lo que haya.
	raw, err = io.ReadAll(f)
	if err != nil {
		slog.Warn("no se pudo leer el archivo de catálogo subido", "error", err)
		return nil, "", "No se pudo leer el archivo que subiste. Inténtalo de nuevo."
	}
	return raw, fh.Filename, ""
}

// catalogErrorRows traduce los defectos a filas de tabla. Solo se calcula la ubicación legible: el
// motivo se copia tal cual.
func catalogErrorRows(errs []apiclient.CatalogImportFieldError) []catalogImportErrorRow {
	rows := make([]catalogImportErrorRow, 0, len(errs))
	for _, e := range errs {
		rows = append(rows, catalogImportErrorRow{
			Location: catalogErrorLocation(e),
			Field:    e.Field,
			Reason:   e.Reason,
		})
	}
	return rows
}

// catalogErrorLocation redacta dónde está el defecto contando desde 1, como cuenta una persona y
// como ya cuenta la prosa del motivo. Sin índices el defecto es del documento entero (cabecera,
// límites), y decirlo así evita que se busque una categoría que no existe.
//
// LA FILA MANDA CUANDO VIENE. Quien subió una planilla tiene delante una hoja de cálculo con su
// margen numerado, no un árbol de categorías, y ese número ya llega en el sistema del operador
// (cabecera = 1): aquí no se le suma nada. Por el camino JSON no hay filas y mandan los índices, que
// sí van en base 0 y sí hay que traducir.
func catalogErrorLocation(e apiclient.CatalogImportFieldError) string {
	switch {
	case e.Row > 0:
		return "Fila " + strconv.Itoa(e.Row)
	case e.CategoryIndex == nil:
		return "Todo el documento"
	case e.ItemIndex == nil:
		return "Categoría " + strconv.Itoa(*e.CategoryIndex+1)
	default:
		return "Categoría " + strconv.Itoa(*e.CategoryIndex+1) + " · artículo " + strconv.Itoa(*e.ItemIndex+1)
	}
}

// catalogInvalidMessage encabeza la lista de defectos diciendo cuántos hay, porque el operador
// necesita saber si le quedan dos correcciones o veinte antes de empezar.
func catalogInvalidMessage(n int) string {
	if n == 1 {
		return "El documento tiene 1 problema y no se ha tocado nada del catálogo. Corrígelo y vuelve a comprobar."
	}
	return "El documento tiene " + strconv.Itoa(n) + " problemas y no se ha tocado nada del catálogo. " +
		"Están todos abajo: corrígelos y vuelve a comprobar."
}

// catalogResultMessage redacta la confirmación de cada paso. El de comprobar insiste en que no se ha
// escrito nada —es lo que hace que mirar sea seguro— y el de aplicar dice dónde quedó guardado el
// catálogo anterior, que es por dónde se empieza a volver atrás.
func catalogResultMessage(r *apiclient.CatalogImportResult) string {
	if !r.Applied {
		return "Documento comprobado: " + strconv.Itoa(r.Items) + " artículos. Todavía NO se ha cambiado nada; " +
			"mira abajo qué pasaría y confirma si es lo que quieres."
	}
	msg := "Catálogo aplicado: " + strconv.Itoa(r.Items) + " artículos en «" + r.Ref + "»."
	if r.ArchivedVersion > 0 {
		msg += " El catálogo anterior quedó guardado como versión " + strconv.Itoa(r.ArchivedVersion) + "."
	}
	return msg
}

// mapCatalogImportError traduce el fallo sin filtrar el detalle del upstream (mismo criterio que el
// resto de mappers del BFF). El texto genérico depende del paso: al comprobar no ha pasado nada, y
// al aplicar lo honesto es decir que no se sabe si pasó.
func mapCatalogImportError(err error, apply bool) (int, *catalogImportNotice) {
	if rej, ok := apiclient.RejectionOf(err); ok {
		if rej.StatusCode == http.StatusRequestEntityTooLarge {
			return http.StatusRequestEntityTooLarge, &catalogImportNotice{
				Message: "El documento es demasiado grande para la plataforma. " +
					"Divide el catálogo en varias importaciones (por categorías, por ejemplo).",
			}
		}
		msg := "La plataforma rechazó la petición."
		if rej.Message != "" {
			msg = "La plataforma rechazó la petición: " + rej.Message
		}
		return http.StatusBadRequest, &catalogImportNotice{Message: msg}
	}
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return http.StatusForbidden, &catalogImportNotice{
			Message: "Tu usuario no puede cambiar el catálogo de este tenant, o el plan ya no incluye la importación.",
		}
	}
	slog.Warn("falló el import de catálogo", "aplicar", apply, "error", err)
	if apply {
		return http.StatusBadGateway, &catalogImportNotice{
			Message: "No se pudo confirmar si el catálogo llegó a aplicarse. Vuelve a comprobar el mismo documento " +
				"antes de repetir: si el resumen no trae cambios, ya estaba aplicado.",
		}
	}
	return http.StatusBadGateway, &catalogImportNotice{
		Message: "No se pudo comprobar el documento ahora mismo. Inténtalo de nuevo; no se ha cambiado nada.",
	}
}

// mapCatalogTemplateError traduce el fallo de la descarga. El 404 tiene un aviso propio porque
// significa algo concreto: en la plataforma, la plantilla, el prompt y el propio import se montan
// JUNTOS o no se monta ninguno —para no mandar a nadie a llenar la plantilla de un import que no
// puede aplicar—, así que un 404 aquí no es «falta esta descarga» sino «puede que no haya import».
// Por eso el aviso NO remite al prompt como alternativa: con las rutas sin montar, ese texto tampoco
// carga, y prometerlo sería mandar al operador a chocarse dos veces.
func mapCatalogTemplateError(err error) (int, *catalogImportNotice) {
	if errors.Is(err, apiclient.ErrUnsupportedTemplateFormat) {
		return http.StatusBadRequest, &catalogImportNotice{
			Message: "Ese formato de plantilla no existe. Usa uno de los enlaces de esta página.",
		}
	}
	switch apiclient.StatusCodeOf(err) {
	case http.StatusNotFound:
		return http.StatusNotFound, &catalogImportNotice{
			Message: "Esta plataforma no está sirviendo la plantilla. Suele significar que la importación de " +
				"catálogo no está montada aquí todavía: si comprobar un documento también falla, avísale a " +
				"quien administre la plataforma.",
		}
	case http.StatusForbidden:
		return http.StatusForbidden, &catalogImportNotice{
			Message: "Tu usuario no puede descargar la plantilla de este tenant, o el plan ya no incluye la importación.",
		}
	}
	slog.Warn("no se pudo descargar la plantilla de catálogo", "error", err)
	return http.StatusBadGateway, &catalogImportNotice{
		Message: "No se pudo descargar la plantilla ahora mismo. Inténtalo de nuevo.",
	}
}
