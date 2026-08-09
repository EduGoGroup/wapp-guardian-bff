package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// integrationsPath es la ruta del CRUD de la configuración del puente en la API pública.
const integrationsPath = "/api/v1/integrations"

// integrationsOutboxPath es el estado de la COLA de entregas del tenant. Cuelga de la misma ruta
// porque es la otra mitad de la misma pregunta: la configuración dice a dónde se entrega, esto dice
// si está llegando.
const integrationsOutboxPath = integrationsPath + "/outbox"

// Integration es la CONFIGURACIÓN del puente CRM del tenant del token (Plan 042 · T5.1) tal como la
// devuelve la API pública, y es la MISMA forma en el GET y en la respuesta del PUT: quien la pinta no
// tiene por qué saber cuál se acaba de llamar.
//
// NO HAY CAMPO PARA EL SECRETO, y esa ausencia es el contrato, no un olvido: el secreto de firma es
// write-only y la plataforma no lo devuelve jamás (D-042.7). Lo que vuelve es si HAY uno (SecretSet)
// y su huella corta (SecretFingerprint), que es lo justo para comparar contra el que el puente tiene
// configurado. Al no existir el campo, el valor tampoco puede llegar por accidente a una vista, a
// una plantilla ni a un log: lo que no se decodifica no se filtra.
//
// Configured distingue las dos maneras de estar en local/local —no tener fila (el default de la
// plataforma) y tenerla puesta a mano—, que es lo que decide si la pantalla ofrece «quitar».
type Integration struct {
	Configured        bool   `json:"configured"`
	CatalogAdapter    string `json:"catalog_adapter"`
	EventsAdapter     string `json:"events_adapter"`
	EndpointURL       string `json:"endpoint_url"`
	Enabled           bool   `json:"enabled"`
	SecretSet         bool   `json:"secret_set"`
	SecretFingerprint string `json:"secret_fingerprint"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// IntegrationSettings es el cuerpo del PUT: la foto COMPLETA de la configuración.
//
// No lleva tenant_id y no puede llevarlo: la plataforma opera SIEMPRE sobre el tenant del token
// (INV-8) y un tenant_id en el cuerpo lo descarta sin ruido. Mandarlo desde aquí solo serviría para
// hacer creer que se puede elegir.
//
// Secret es WRITE-ONLY y va con `omitempty` a propósito: el campo vacío NO viaja, y su ausencia
// significa «deja el secreto que ya está guardado». Es exactamente lo que manda un formulario cuyo
// campo de secreto se dejó en blanco —el caso normal al reconfigurar el endpoint—, y es lo único que
// hace posible cambiar la URL sin reenviar un secreto que el GET no devuelve. Para dejar de firmar se
// borra la integración entera (DELETE).
type IntegrationSettings struct {
	CatalogAdapter string `json:"catalog_adapter"`
	EventsAdapter  string `json:"events_adapter"`
	EndpointURL    string `json:"endpoint_url"`
	Secret         string `json:"secret,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// IntegrationsClient maneja la configuración del puente CRM contra la API pública.
//
// Los tres verbos exigen scope propio (integrations.read / integrations.write) Y la feature
// `crm_bridge`: sin ella son 403 los tres, también el GET. Los scopes no se reusan de content.*
// porque esta fila guarda el secreto de firma y la URL a la que se entregan todos los pedidos del
// tenant — quien la escribe puede repuntar el destino a un host propio.
type IntegrationsClient struct {
	t *Transport
}

// NewIntegrationsClient construye un IntegrationsClient sobre un Transport.
func NewIntegrationsClient(t *Transport) *IntegrationsClient {
	return &IntegrationsClient{t: t}
}

// GetIntegration lee la configuración del puente del tenant vía GET /api/v1/integrations.
//
// Un tenant sin fila responde 200 con el default local/local y `configured:false`: «no tengo puente»
// es una respuesta —y es la que la pantalla necesita para dibujar el formulario vacío—, no un fallo.
func (c *IntegrationsClient) GetIntegration(ctx context.Context, accessToken string) (*Integration, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, integrationsPath, nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: integración: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("integración", resp.StatusCode)
	}
	return decodeIntegration(resp, "integración")
}

// SaveIntegration guarda la configuración vía PUT /api/v1/integrations y devuelve la que quedó, ya
// releída por la plataforma (con los timestamps y la huella del secreto vigente, que puede ser el de
// antes si este PUT no traía uno).
//
// Los rechazos con motivo conservan el mensaje de la API porque es la única forma de que el operador
// sepa qué corregir: el 400 dice qué tiene de malo la configuración (URL no absoluta, secreto corto,
// puente encendido sin endpoint o sin secreto) y el 422 que el adaptador de catálogo «http» está
// diferido. El resto —403, 5xx— sale como *APIError: el código ya lo dice todo y el cuerpo del
// upstream no debe acabar en pantalla.
func (c *IntegrationsClient) SaveIntegration(ctx context.Context, accessToken string, s IntegrationSettings) (*Integration, error) {
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPut, integrationsPath, s, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: guardar integración: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, reasonedStatusError("guardar integración", resp,
			http.StatusBadRequest, http.StatusUnprocessableEntity)
	}
	return decodeIntegration(resp, "guardar integración")
}

// DeleteIntegration borra la fila del tenant vía DELETE /api/v1/integrations: vuelve al default
// local/local, sin CRM y con la experiencia completa de wApp.
//
// Es IDEMPOTENTE en la plataforma (204 también sin fila), así que aquí no hay un «no había nada que
// borrar» que distinguir: el resultado pedido es un estado, no un objeto. Y borra el secreto cifrado
// con la fila — es la única forma de retirarlo, porque el PUT nunca lo borra.
func (c *IntegrationsClient) DeleteIntegration(ctx context.Context, accessToken string) error {
	req, err := c.t.newAuthedRequest(ctx, http.MethodDelete, integrationsPath, nil, accessToken)
	if err != nil {
		return err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("apiclient: borrar integración: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("borrar integración", resp.StatusCode)
	}
	return nil
}

// OutboxCounters es el estado de la cola de entregas del tenant tal como lo devuelve la API pública:
// cuántas hay en cada estado del ciclo de vida y desde cuándo espera la más antigua que sigue en cola.
//
// LOS NOMBRES SON LOS DE LA PLATAFORMA (pending/delivering/delivered/dead) y aquí se conservan tal
// cual: traducir en el cliente crearía una segunda taxonomía a mitad de camino. Quien traduce a
// lenguaje de negocio es la pantalla, que es donde está el lector humano.
//
// NO HAY CAMPO PARA EL CONTENIDO DE LAS ENTREGAS, y la ausencia es el contrato: el endpoint devuelve
// contadores y nada más. El payload de las entregadas se vacía al entregar y el de las perdidas es lo
// único que queda de un intento fallido; ninguno de los dos sale por esta puerta, así que tampoco
// puede llegar por accidente a una plantilla ni a un log.
//
// OldestPendingAt viene VACÍA cuando no hay nada en cola: la plataforma omite el campo en vez de
// mandar una fecha cero, así que «no hay la más antigua» se lee como lo que es.
type OutboxCounters struct {
	Pending         int64  `json:"pending"`
	Delivering      int64  `json:"delivering"`
	Delivered       int64  `json:"delivered"`
	Dead            int64  `json:"dead"`
	OldestPendingAt string `json:"oldest_pending_at"`
}

// GetOutboxCounters lee el estado de la cola vía GET /api/v1/integrations/outbox.
//
// Mismas guardias que el GET de la configuración (scope `integrations.read` + feature `crm_bridge`),
// así que un tenant sin puente recibe 403 y no un cero: la pantalla tiene que poder distinguir «no
// tienes puente» de «no hay nada pendiente», que se parecen mucho y significan cosas distintas.
//
// Una cola vacía responde 200 con todo a cero, que es la respuesta correcta y no un fallo.
func (c *IntegrationsClient) GetOutboxCounters(ctx context.Context, accessToken string) (*OutboxCounters, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, integrationsOutboxPath, nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: cola de entregas: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("cola de entregas", resp.StatusCode)
	}
	var out OutboxCounters
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: cola de entregas: decodificar respuesta: %w", err)
	}
	return &out, nil
}

// decodeIntegration lee la respuesta común de GET y PUT.
func decodeIntegration(resp *http.Response, op string) (*Integration, error) {
	var out Integration
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	return &out, nil
}
