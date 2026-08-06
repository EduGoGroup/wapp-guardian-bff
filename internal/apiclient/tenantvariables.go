package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// TenantVariables es el conjunto de variables de empresa del tenant del token
// (Plan 041 · T2.1): pares clave→valor que **wApp no interpreta** (D-041.1). Aquí
// no se tipa el valor, no se normaliza la clave y no hay lista blanca: son texto
// opaco que viaja verbatim en las dos direcciones.
//
// UpdatedAt es la marca del cambio más reciente del conjunto, y viene vacía cuando
// el tenant no tiene ninguna variable.
type TenantVariables struct {
	Variables map[string]string `json:"variables"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// tenantVariablesRequest es el cuerpo del PUT. El campo se serializa SIEMPRE —sin
// `omitempty`— porque un conjunto vacío es una intención legítima («déjame sin
// variables») y la API distingue eso de no mandar el campo, que rechaza con 400.
type tenantVariablesRequest struct {
	Variables map[string]string `json:"variables"`
}

// TenantVariablesClient maneja las variables de empresa contra la API pública.
// GET exige el scope content.read y PUT content.write; no hay gate de feature,
// porque las variables son capa técnica y no una capacidad comercial.
type TenantVariablesClient struct {
	t *Transport
}

// NewTenantVariablesClient construye un TenantVariablesClient sobre un Transport.
func NewTenantVariablesClient(t *Transport) *TenantVariablesClient {
	return &TenantVariablesClient{t: t}
}

// GetTenantVariables lee el conjunto del tenant vía GET /api/v1/tenant-variables.
// Un tenant sin variables responde 200 con el mapa vacío: «no tengo ninguna» es
// una respuesta, no un fallo, y el llamante no debe tratarla como error.
func (c *TenantVariablesClient) GetTenantVariables(ctx context.Context, accessToken string) (*TenantVariables, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/tenant-variables", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: tenant variables: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("tenant variables", resp.StatusCode)
	}
	return decodeTenantVariables(resp, "tenant variables")
}

// ReplaceTenantVariables sustituye el conjunto ENTERO del tenant vía
// PUT /api/v1/tenant-variables y devuelve el resultado tal como quedó guardado.
//
// Reemplaza, no fusiona: lo que no viaje en `vars` **se borra**. No es un descuido
// del contrato sino su única forma de borrar —no hay DELETE—, así que el llamante
// tiene que mandar siempre la foto completa. Un mapa vacío deja al tenant sin
// variables, y es una operación válida.
//
// Los rechazos con motivo (400 de forma, 413 por tamaño) conservan el mensaje de
// la API para poder decírselo al operador; el resto sale como *APIError.
func (c *TenantVariablesClient) ReplaceTenantVariables(ctx context.Context, accessToken string, vars map[string]string) (*TenantVariables, error) {
	if vars == nil {
		// Un mapa nil serializaría como `"variables":null`, que la API rechaza con
		// 400 por no traer el objeto. Quien quiere dejar el tenant sin variables
		// manda `{}`, y esa intención se respeta aquí en vez de gastar un viaje.
		vars = map[string]string{}
	}
	req, err := c.t.newAuthedJSONRequest(ctx, http.MethodPut, "/api/v1/tenant-variables",
		tenantVariablesRequest{Variables: vars}, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: guardar variables: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, reasonedStatusError("guardar variables", resp,
			http.StatusBadRequest, http.StatusRequestEntityTooLarge)
	}
	return decodeTenantVariables(resp, "guardar variables")
}

// decodeTenantVariables lee la respuesta y garantiza un mapa NO nulo: la API ya lo
// inicializa, pero el llamante no debería tener que ramificar por el nulo para
// pintar una tabla vacía.
func decodeTenantVariables(resp *http.Response, op string) (*TenantVariables, error) {
	var out TenantVariables
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: %s: decodificar respuesta: %w", op, err)
	}
	if out.Variables == nil {
		out.Variables = map[string]string{}
	}
	return &out, nil
}
