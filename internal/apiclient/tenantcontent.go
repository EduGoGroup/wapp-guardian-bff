package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxTenantContentListBytes acota el listado de refs. Son nombres cortos con dos marcas de tiempo,
// así que 1 MiB es holgadísimo; el tope existe para que un upstream que responda cualquier cosa no
// se lleve la memoria del BFF por delante.
const maxTenantContentListBytes = 1 << 20

// TenantContentRef es UNA ref de contenido del tenant tal como la lista
// GET /api/v1/tenant-content: el nombre lógico y sus marcas de tiempo. NO trae el blob —para eso
// está GET /{ref}—, y la pantalla de import no lo necesita: solo quiere saber qué refs existen.
type TenantContentRef struct {
	Ref       string `json:"ref"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ListTenantContentRefs devuelve las refs de contenido del tenant del token (INV-8).
//
// Un tenant sin contenido responde 200 con la lista vacía: «no tengo ninguna» es una respuesta, no
// un fallo. Quien la consume no debe convertir la lista vacía en un error, pero SÍ tiene que seguir
// ofreciendo una ref explícita —ver catalogImportRefOptions—: el defecto A3 del Plan 041 nació justo
// de mandar la ref vacía y dejar que la plataforma eligiera por su cuenta.
// Cuelga del CatalogImportClient y no de un cliente propio a propósito: es el mismo scope
// (content.read) y el único consumidor es la pantalla de import, que ya tiene ese cliente cableado.
// Un cliente nuevo solo añadiría wiring en el bootstrap para una sola llamada GET.
func (c *CatalogImportClient) ListTenantContentRefs(ctx context.Context, accessToken string) ([]TenantContentRef, error) {
	req, err := c.t.newAuthedRequest(ctx, http.MethodGet, "/api/v1/tenant-content", nil, accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apiclient: tenant content: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("tenant content", resp.StatusCode)
	}

	var refs []TenantContentRef
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTenantContentListBytes)).Decode(&refs); err != nil {
		return nil, fmt.Errorf("apiclient: tenant content: respuesta ilegible: %w", err)
	}
	return refs, nil
}
