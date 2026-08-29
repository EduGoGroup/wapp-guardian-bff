package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// tenantLLMPath es la ruta del CRUD de la configuración LLM del tenant en la API pública.
const tenantLLMPath = "/api/v1/tenant-llm"

// TenantLLM es la CONFIGURACIÓN LLM del tenant del token (Plan 044 · T0.3, T1.5-2) tal como la
// devuelve la API pública, y es la MISMA forma en el GET y en la respuesta del PUT: quien la pinta no
// tiene por qué saber cuál se acaba de llamar.
//
// 🔴 NO HAY CAMPO PARA LA CLAVE, y esa ausencia es el contrato, no un olvido: la credencial es
// write-only y la plataforma no la devuelve jamás. Lo que vuelve es si HAY una (KeySet) y nada más.
// Al no existir el campo, el valor tampoco puede llegar por accidente a una vista, a una plantilla ni
// a un log: lo que no se decodifica no se filtra. Es lo que hace que «la clave nunca se re-pinta en el
// HTML» se cumpla POR CONSTRUCCIÓN.
//
// 🔴 Y TAMPOCO HAY HUELLA, al revés que Integration. La huella del secreto HMAC del puente existe
// porque el tenant tiene con qué compararla (el mismo secreto está configurado en SU sistema). Una API
// key de un proveedor no tiene contraparte que comparar, y publicar su huella regalaría un oráculo de
// confirmación offline sobre un valor de formato conocido y público. No la pidas: la API no la tiene.
//
// Via SIEMPRE viene con valor (nunca vacío): un tenant que no configuró nada está en la vía por
// defecto —`local`—, que es el default del producto y no «ninguna». Provider, Model, ConsentedAt,
// CreatedAt y UpdatedAt sí pueden faltar; la vía local no llama a ningún tercero y no tiene nada que
// poner en los tres primeros.
//
// Configured distingue «este tenant tiene configuración LLM puesta» de «nunca configuró nada», que es
// lo que decide si la pantalla ofrece «quitar».
type TenantLLM struct {
	Configured  bool   `json:"configured"`
	Via         string `json:"via"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	KeySet      bool   `json:"key_set"`
	ConsentedAt string `json:"consented_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// TenantLLMSettings es el cuerpo del PUT: la foto COMPLETA de la configuración.
//
// No lleva tenant_id y no puede llevarlo: la plataforma opera SIEMPRE sobre el tenant del token
// (INV-04) y un tenant_id en el cuerpo lo descarta sin ruido. Mandarlo desde aquí solo serviría para
// hacer creer que se puede elegir.
//
// 🔴 APIKey ES OBLIGATORIA EN CADA PUT DE LA VÍA `api`, y aquí este contrato se separa a propósito del
// de IntegrationSettings (donde el secreto vacío conserva el que ya está guardado). La plataforma NO
// tiene una semántica de «deja la que está»: el PUT es un reemplazo completo y una vía `api` sin clave
// es un 400. La consecuencia —que hay que volver a teclear la credencial en cada guardado— NO se
// disimula: la pantalla lo dice antes de que el operador escriba nada. Cambiar eso sería cambiar el
// contrato del cloud, no este cliente.
//
// El `omitempty` de los tres campos de la vía `api` no es una semántica de «conserva»: es que en la
// vía `local` esos campos NO EXISTEN. Un PUT de vía local manda `{"via":"local","consented":false}` y
// nada más, así que la credencial no viaja ni siquiera vacía por un cable que no la necesita.
type TenantLLMSettings struct {
	Via       string `json:"via"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	Consented bool   `json:"consented"`
}

// TenantLLMClient maneja la configuración LLM del tenant contra la API pública.
//
// Los tres verbos exigen scope propio (llm.read / llm.write) Y la feature `api_llm`: sin ella son 403
// los tres, también el GET. Los scopes no se reusan de content.* porque esta fila guarda una
// credencial de pago de un tercero — quien la escribe puede facturarle al tenant.
//
// ⚠️ Los grants no son simétricos entre roles: `tenant_admin` tiene los dos scopes, `viewer` solo el
// de lectura y `operator` NINGUNO. Un operador ve 403 en los tres verbos, y la pantalla tiene que
// tratarlo como un aviso legible, no como una página rota.
type TenantLLMClient struct {
	t *Transport
}

// NewTenantLLMClient construye un TenantLLMClient sobre un Transport.
func NewTenantLLMClient(t *Transport) *TenantLLMClient {
	return &TenantLLMClient{t: t}
}

// GetTenantLLM lee la configuración LLM del tenant vía GET /api/v1/tenant-llm.
//
// Un tenant sin fila responde 200 con `configured:false` y `via:"local"`: «no tengo vía API» es una
// respuesta —y es la que la pantalla necesita para dibujar el formulario vacío—, no un fallo.
//
// LA CLAVE NO VIENE EN ESTA RESPUESTA y no hay forma de pedirla: el DTO de la plataforma solo publica
// `key_set`.
func (c *TenantLLMClient) GetTenantLLM(ctx context.Context, accessToken string) (*TenantLLM, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, tenantLLMPath, nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: configuración LLM: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("configuración LLM", resp.StatusCode)
	}
	return decodeTenantLLM(resp, "configuración LLM")
}

// SaveTenantLLM guarda la configuración vía PUT /api/v1/tenant-llm y devuelve la que quedó, ya releída
// por la plataforma (con los timestamps y el `key_set` que de verdad se guardó).
//
// Los rechazos con motivo conservan el cuerpo de la API porque es la única forma de que el operador
// sepa qué corregir. El 400 cubre la vía desconocida, el consentimiento que falta, el proveedor fuera
// del vocabulario, el modelo vacío o largo y la clave ausente / corta / larga; el 422 queda reservado
// a lo que la plataforma declare no cableado. El resto —403, 413, 5xx— sale como *APIError: el código
// ya lo dice todo y el cuerpo del upstream no debe acabar en pantalla.
//
// 🔴 EL ERROR NUNCA LLEVA LA CLAVE. No la mete este cliente (no la escribe en ningún mensaje) y no la
// devuelve la plataforma: sus mensajes evitan a propósito hasta la longitud real de la credencial,
// para no convertir el endpoint en un medidor. No la «mejores» aquí.
func (c *TenantLLMClient) SaveTenantLLM(ctx context.Context, accessToken string, s TenantLLMSettings) (*TenantLLM, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPut, tenantLLMPath, s, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: guardar configuración LLM: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, reasonedStatusError("guardar configuración LLM", resp,
			http.StatusBadRequest, http.StatusUnprocessableEntity)
	}
	return decodeTenantLLM(resp, "guardar configuración LLM")
}

// DeleteTenantLLM borra la fila del tenant vía DELETE /api/v1/tenant-llm: se va la credencial Y el
// consentimiento, que viven en la misma fila y por eso se van juntos —un consentimiento que
// sobreviviera a la retirada de la clave sería un permiso vivo sin nada que lo ejerza.
//
// Es IDEMPOTENTE en la plataforma (204 también sin fila), así que aquí no hay un «no había nada que
// borrar» que distinguir: el resultado pedido es un estado, no un objeto.
func (c *TenantLLMClient) DeleteTenantLLM(ctx context.Context, accessToken string) error {
	req, err := c.t.newAuthedRequest(ctx, http.MethodDelete, tenantLLMPath, nil, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: borrar configuración LLM: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("borrar configuración LLM", resp.StatusCode)
	}
	return nil
}

// decodeTenantLLM lee la respuesta común de GET y PUT.
func decodeTenantLLM(resp *http.Response, op string) (*TenantLLM, error) {
	var out TenantLLM
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	return &out, nil
}
