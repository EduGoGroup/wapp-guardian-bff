package apiclient

// Client relaya contra la API pública integrando los clientes de dominio.
type Client struct {
	*Transport
	*AuthClient
	*EntitlementsClient
	*EditorClient
	*IntakesClient
	*TenantVariablesClient
	*CatalogImportClient
	*IntegrationsClient
	*TenantLLMClient
}

// New construye el cliente unificado con un http.Client de timeout por defecto (15s) y el cliente
// de inferencia aparte, que solo usa la sugerencia de cotización (ver Transport.InferenceHTTPClient).
func New(baseURL string, opts ...Option) *Client {
	t := NewTransport(baseURL, opts...)
	return &Client{
		Transport:             t,
		AuthClient:            NewAuthClient(t),
		EntitlementsClient:    NewEntitlementsClient(t),
		EditorClient:          NewEditorClient(t),
		IntakesClient:         NewIntakesClient(t),
		TenantVariablesClient: NewTenantVariablesClient(t),
		CatalogImportClient:   NewCatalogImportClient(t),
		IntegrationsClient:    NewIntegrationsClient(t),
		TenantLLMClient:       NewTenantLLMClient(t),
	}
}
