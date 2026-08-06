package apiclient

// Client relaya contra la API pública integrando los clientes de dominio.
type Client struct {
	*Transport
	*AuthClient
	*DashboardClient
	*EditorClient
	*IntakesClient
}

// New construye el cliente unificado con un http.Client de timeout por defecto (15s).
func New(baseURL string) *Client {
	t := NewTransport(baseURL)
	return &Client{
		Transport:       t,
		AuthClient:      NewAuthClient(t),
		DashboardClient: NewDashboardClient(t),
		EditorClient:    NewEditorClient(t),
		IntakesClient:   NewIntakesClient(t),
	}
}
