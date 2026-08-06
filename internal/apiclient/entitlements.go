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

// GetEntitlements lee el plan y las features efectivas del tenant del token vía
// GET /api/v1/entitlements. Exige el scope entitlements.read: un token válido sin él devuelve 403,
// que el llamador distingue con StatusCodeOf.
func (c *DashboardClient) GetEntitlements(ctx context.Context, accessToken string) (*Entitlements, error) {
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
