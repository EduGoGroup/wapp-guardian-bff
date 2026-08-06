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

// catalogImportPath es la ruta del import de catálogo en la API pública (Plan 041 · T3.3).
const catalogImportPath = "/api/v1/catalog/import"

// Modos del import. El de la plataforma cuando el parámetro falta es `validate`, y esa red de
// seguridad se respeta pero no se usa: el cliente manda SIEMPRE el modo explícito, porque una
// petición que escribe el catálogo del tenant no debe depender de un default para no escribirlo.
const (
	catalogModeValidate = "validate"
	catalogModeApply    = "apply"
)

// maxCatalogImportErrorBody acota lo que se lee de un rechazo. Es mucho más alto que
// maxRejectionBody (500 B) a propósito: el 400 del import no trae un motivo suelto sino la lista
// COMPLETA de defectos del documento —seis, veinte o los que haya, cada uno con su prosa—, y
// recortarla dejaría al operador arreglando solo los primeros.
const maxCatalogImportErrorBody = 64 << 10

// maxCatalogTemplateBytes acota la plantilla descargable. Es un documento de ejemplo con cuatro
// artículos, así que 4 MiB sobra; el tope existe para que un upstream que responda cualquier cosa
// no se lleve la memoria del BFF por delante.
const maxCatalogTemplateBytes = 4 << 20

// ErrUnsupportedTemplateFormat rechaza un formato de plantilla que no está en la lista. Se
// distingue del resto de fallos porque no es un problema de la plataforma sino de la petición: la
// pantalla lo traduce a «ese formato no existe», no a «la plataforma no responde».
var ErrUnsupportedTemplateFormat = errors.New("apiclient: formato de plantilla no soportado")

// CatalogImportResult es la respuesta de las dos modalidades del import. La plataforma responde el
// MISMO objeto en validate y en apply —Applied es la única diferencia semántica—, así que la
// pantalla pinta el diff con un solo camino de código.
type CatalogImportResult struct {
	Mode string `json:"mode"`
	// Ref es la ref de contenido donde vive (o viviría) el catálogo. Viene de la respuesta y NO se
	// fija aquí: el default es de la plataforma y duplicarlo sería tener dos.
	Ref string `json:"ref"`
	// Applied dice si el catálogo se escribió de verdad. En validate es SIEMPRE false.
	Applied bool `json:"applied"`
	// Items es cuántos artículos trae el documento subido: la cifra con la que el operador reconoce
	// que subió el archivo que quería antes de leer el diff.
	Items int         `json:"items"`
	Diff  CatalogDiff `json:"diff"`
	// ArchivedVersion es el número con el que quedó guardado el catálogo ANTERIOR. Cero cuando no se
	// archivó nada: en validate (no se escribe) y en el primer import de una ref.
	ArchivedVersion int `json:"archived_version"`
}

// CatalogDiff responde a la única pregunta que el dueño se hace antes de aplicar un import: qué le
// va a pasar a su catálogo. Se calcula por sku en la plataforma; aquí solo se transporta.
type CatalogDiff struct {
	PriceChanges []CatalogPriceChange `json:"price_changes"`
	Added        []CatalogItemRef     `json:"added"`
	// Removed son los artículos que dejan de venderse en cuanto se aplique.
	Removed []CatalogItemRef `json:"removed"`
	// ChangedDetails son los sku a los que les cambió algo que no es el precio (variantes, tags,
	// componentes, etiqueta…). La v1 del diff dice QUÉ cambió, no qué campo.
	ChangedDetails []string `json:"changed_details"`
	Unchanged      int      `json:"unchanged"`
	// CurrentWarnings es lo que el catálogo VIGENTE ya tenía mal y el motor ignora en silencio. NO es
	// decorativo: esos artículos no están en el lado viejo de la comparación, así que no aparecen en
	// Removed aunque desaparezcan de verdad. Sin enseñarlos, un artículo con sku reservado se esfuma
	// sin que nada lo diga.
	CurrentWarnings []string `json:"current_warnings"`
}

// CatalogPriceChange es un artículo que cambia de precio. La etiqueta es la NUEVA: la pantalla
// enseña el catálogo que viene, no el que se va.
type CatalogPriceChange struct {
	SKU      string  `json:"sku"`
	Label    string  `json:"label"`
	OldPrice float64 `json:"old_price"`
	NewPrice float64 `json:"new_price"`
}

// CatalogItemRef identifica un artículo en las listas de altas y bajas. La etiqueta va junto al sku
// porque un sku suelto no le dice nada al dueño del negocio.
type CatalogItemRef struct {
	SKU   string `json:"sku"`
	Label string `json:"label"`
}

// CatalogImportFieldError localiza UN defecto del documento. Los índices son posiciones de lista en
// base 0 —así los publica la plataforma— y Reason es la explicación en español, escrita para el
// dueño del negocio. Ese texto viaja VERBATIM hasta la pantalla: reescribirlo aquí sería mantener
// dos criterios de redacción que acabarían discrepando.
type CatalogImportFieldError struct {
	CategoryIndex *int   `json:"category_index,omitempty"`
	ItemIndex     *int   `json:"item_index,omitempty"`
	Field         string `json:"field"`
	Reason        string `json:"reason"`
}

// CatalogImportInvalidError es el rechazo por documento inválido: TODOS los defectos en una sola
// respuesta (el validador de la plataforma acumula, no corta en el primero).
//
// Va como tipo propio y no como *RejectionError porque la pantalla hace con él algo distinto: no
// enseña un motivo, enseña una lista con la que el operador corrige el archivo.
type CatalogImportInvalidError struct {
	Errors []CatalogImportFieldError
}

func (e *CatalogImportInvalidError) Error() string {
	return fmt.Sprintf("apiclient: el documento de catálogo tiene %d problemas", len(e.Errors))
}

// CatalogImportInvalidOf extrae el rechazo por documento inválido de un error (nil, false si no lo es).
func CatalogImportInvalidOf(err error) (*CatalogImportInvalidError, bool) {
	var invalid *CatalogImportInvalidError
	if errors.As(err, &invalid) {
		return invalid, true
	}
	return nil, false
}

// CatalogPrompt es el prompt-plantilla que el dueño del negocio pega en SU asistente junto con la
// plantilla y su lista de productos.
//
// SE PIDE, NO SE COPIA. El texto está versionado junto al contrato en la plataforma
// (internal/catalogimport/prompt.go, con un test que lo ata a la versión del contrato) y por eso lo
// sirve un endpoint: una copia pegada en una plantilla HTML de este repo sería una segunda fuente
// que envejece sola y acabaría dictándole al asistente un formato que el validador ya no acepta.
//
// Version es la del contrato al que corresponde el texto, y viaja para que la pantalla pueda
// enseñarla: es lo que permite notar que el prompt se quedó en una versión anterior.
type CatalogPrompt struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Prompt  string `json:"prompt"`
}

// CatalogTemplate es la plantilla descargable ya lista para servírsela al navegador.
//
// El tipo y el nombre del archivo salen del formato PEDIDO, no de las cabeceras del upstream: un
// Content-Disposition ajeno no debe poder decidir cómo guarda el navegador un archivo servido desde
// el origen del BFF.
type CatalogTemplate struct {
	Content     []byte
	ContentType string
	Filename    string
}

// catalogTemplateFormats es la lista blanca de formatos de plantilla con lo que el BFF pone de su
// parte al servirlos.
var catalogTemplateFormats = map[string]struct {
	contentType string
	filename    string
}{
	"json": {"application/json; charset=utf-8", "catalogo-plantilla.json"},
	"csv":  {"text/csv; charset=utf-8", "catalogo-plantilla.csv"},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "catalogo-plantilla.xlsx"},
}

// CatalogImportClient maneja el import de catálogo contra la API pública. Sus rutas exigen el scope
// content.write y la feature `catalog_import`: sin ella la plataforma corta con 403, que es la
// autoridad real — el gate de la plantilla solo decide qué se pinta.
type CatalogImportClient struct {
	t *Transport
}

// NewCatalogImportClient construye un CatalogImportClient sobre un Transport.
func NewCatalogImportClient(t *Transport) *CatalogImportClient {
	return &CatalogImportClient{t: t}
}

// ImportCatalog envía el documento a POST /api/v1/catalog/import y devuelve lo que pasaría (o lo que
// pasó) con el catálogo del tenant.
//
// El documento viaja como JSON CRUDO y SIN TOCAR: es portátil por contrato (no lleva tenant ni ref)
// y el BFF no lo reserializa, ni lo reindenta, ni le quita campos. Lo que el operador ve en pantalla
// es exactamente lo que la plataforma valida.
//
// `apply` decide la modalidad. La ref NO se manda: el default es de la plataforma y fijarlo aquí
// significaría tener dos verdades sobre dónde vive el catálogo.
//
// Un documento inválido sale como *CatalogImportInvalidError con la lista entera de defectos; el
// resto de rechazos con motivo mostrable (413 por tamaño, 400 por modo desconocido) como
// *RejectionError, y lo demás como *APIError.
func (c *CatalogImportClient) ImportCatalog(ctx context.Context, accessToken string, document []byte, apply bool) (*CatalogImportResult, error) {
	mode := catalogModeValidate
	if apply {
		mode = catalogModeApply
	}
	req, err := c.t.newAuthedRequest(ctx, http.MethodPost,
		catalogImportPath+"?mode="+mode, document, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: import de catálogo: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, catalogImportError("import de catálogo", resp)
	}

	var out CatalogImportResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: import de catálogo: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// GetCatalogTemplate descarga la plantilla de ejemplo de GET /api/v1/catalog/import/template.
//
// El formato se valida contra la lista blanca ANTES de gastar el viaje, y de ahí salen también el
// Content-Type y el nombre con el que el navegador guardará el archivo.
func (c *CatalogImportClient) GetCatalogTemplate(ctx context.Context, accessToken, format string) (*CatalogTemplate, error) {
	meta, ok := catalogTemplateFormats[format]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTemplateFormat, format)
	}
	q := url.Values{"format": {format}}
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet,
		catalogImportPath+"/template?"+q.Encode(), nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: plantilla de catálogo: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("plantilla de catálogo", resp.StatusCode)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogTemplateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("apiclient: plantilla de catálogo: leer respuesta: %w", err)
	}
	if len(content) > maxCatalogTemplateBytes {
		// Servir una plantilla truncada sería peor que no servirla: pasaría por buena y no validaría.
		return nil, fmt.Errorf("apiclient: plantilla de catálogo: la respuesta excede %d bytes", maxCatalogTemplateBytes)
	}
	return &CatalogTemplate{Content: content, ContentType: meta.contentType, Filename: meta.filename}, nil
}

// GetCatalogPrompt pide el prompt-plantilla a GET /api/v1/catalog/import/prompt.
//
// Cuelga de la misma capacidad que el import (`catalog_import`), así que un 403 aquí significa lo
// mismo que allí y la pantalla ya no lo pide cuando el gate está cerrado.
func (c *CatalogImportClient) GetCatalogPrompt(ctx context.Context, accessToken string) (*CatalogPrompt, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, catalogImportPath+"/prompt", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: prompt de catálogo: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("prompt de catálogo", resp.StatusCode)
	}

	var out CatalogPrompt
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: prompt de catálogo: decodificar respuesta: %w", err)
	}
	if out.Prompt == "" {
		// Un prompt vacío pintado como texto copiable sería peor que decir que no se pudo cargar: el
		// operador copiaría la nada y le echaría la culpa a su asistente.
		return nil, fmt.Errorf("apiclient: prompt de catálogo: respuesta sin texto")
	}
	return &out, nil
}

// catalogImportError traduce un no-2xx del import. Lee el cuerpo UNA vez y decide con él, porque los
// dos rechazos que importan comparten status (400) y solo se distinguen por la forma: el documento
// inválido trae la lista de defectos, el modo desconocido un motivo suelto.
func catalogImportError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return statusError(op, resp.StatusCode)
	}
	var body struct {
		Error  string                    `json:"error"`
		Errors []CatalogImportFieldError `json:"errors"`
	}
	// Un cuerpo ilegible deja la lista vacía y el motivo en blanco: el status sigue siendo la
	// información principal y el llamante tiene su texto genérico.
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxCatalogImportErrorBody)).Decode(&body)

	if len(body.Errors) > 0 {
		return &CatalogImportInvalidError{Errors: body.Errors}
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return &RejectionError{Op: op, StatusCode: resp.StatusCode, Message: body.Error}
	}
	return statusError(op, resp.StatusCode)
}
