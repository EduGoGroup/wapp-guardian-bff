package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Entitlements es la respuesta de GET /api/v1/entitlements: el plan del tenant y sus features
// EFECTIVAS (las del plan con los overrides del tenant ya aplicados), en el orden estable que fija
// el servidor.
//
// La lista trae SOLO las habilitadas —no un mapa clave→bool—, así que la UI decide por pertenencia:
// lo que no está en la lista, no está contratado. El tenant no viaja en la petición ni en la
// respuesta: sale del token (INV-8).
type Entitlements struct {
	Plan            string   `json:"plan"`
	Features        []string `json:"features"`
	CacheTTLSeconds int      `json:"cache_ttl_seconds"`
}

// EntitlementsClient lee el plan y las capacidades efectivas del tenant contra la API pública.
//
// 📌 Hasta el Plan 047 · T2.1 se llamaba `DashboardClient` y era el cliente del dashboard de
// sesiones —listar, enviar, cambiar el perfil—, con las capacidades como un método más. Ese
// dashboard se retiró del BFF (migró a la consola del cliente) y con él se fueron sus tres
// llamadas; lo que quedó fue esta, que nunca fue de sesiones. El tipo se renombró en vez de
// conservarse: un `DashboardClient` sin dashboard es un nombre que miente a quien lo lea después.
type EntitlementsClient struct {
	t *Transport
}

// NewEntitlementsClient construye un EntitlementsClient acoplado a un Transport.
func NewEntitlementsClient(t *Transport) *EntitlementsClient {
	return &EntitlementsClient{t: t}
}

// GetEntitlements lee el plan y las features efectivas del tenant del token vía
// GET /api/v1/entitlements. Exige el scope entitlements.read: un token válido sin él devuelve 403,
// que el llamador distingue con StatusCodeOf.
func (c *EntitlementsClient) GetEntitlements(ctx context.Context, accessToken string) (*Entitlements, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/entitlements", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: entitlements: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("entitlements", resp.StatusCode)
	}
	var out Entitlements
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("apiclient: entitlements: decodificar respuesta: %w", err)
	}
	return &out, nil
}
