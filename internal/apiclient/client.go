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
}

// New construye el cliente unificado con un http.Client de timeout por defecto (15s).
func New(baseURL string) *Client {
	t := NewTransport(baseURL)
	return &Client{
		Transport:             t,
		AuthClient:            NewAuthClient(t),
		EntitlementsClient:    NewEntitlementsClient(t),
		EditorClient:          NewEditorClient(t),
		IntakesClient:         NewIntakesClient(t),
		TenantVariablesClient: NewTenantVariablesClient(t),
		CatalogImportClient:   NewCatalogImportClient(t),
		IntegrationsClient:    NewIntegrationsClient(t),
	}
}
