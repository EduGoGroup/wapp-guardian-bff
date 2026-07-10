package web

import (
	"context"
	"encoding/json"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// APIPort es el contrato que el BFF necesita de la API pública (Pieza 03). El Handler depende de esta
// interfaz —no del cliente concreto *apiclient.Client— para invertir la dependencia (DIP): la
// implementación real es apiclient.Client (transporte HTTP + Bearer server-side), y los tests pueden
// inyectar un doble en memoria sin levantar un servidor HTTP donde eso simplifique.
//
// Las firmas y los tipos del wire son los de apiclient (fuente de verdad del contrato REST); esta interfaz
// solo enumera el subconjunto que el BFF consume.
type APIPort interface {
	// Autenticación (cookie de sesión server-side).
	Login(ctx context.Context, email, password string) (*apiclient.AuthResult, error)
	Refresh(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
	Logout(ctx context.Context, accessToken, refreshToken string) error

	// Dashboard: sesiones del tenant + envío de mensaje.
	ListSessions(ctx context.Context, accessToken string) ([]apiclient.Session, error)
	SendMessage(ctx context.Context, accessToken, sessionID, to, text string) (*apiclient.SendResult, error)

	// Editor: flujos (listar/ver/publicar) y triggers (listar/crear/borrar).
	ListFlows(ctx context.Context, accessToken string) ([]apiclient.FlowSummary, error)
	GetFlow(ctx context.Context, accessToken, id string) (json.RawMessage, error)
	PublishFlow(ctx context.Context, accessToken string, flowJSON []byte) (*apiclient.PublishFlowResult, error)
	ListTriggers(ctx context.Context, accessToken string) ([]apiclient.Trigger, error)
	CreateTrigger(ctx context.Context, accessToken string, tr apiclient.CreateTriggerRequest) (*apiclient.Trigger, error)
	DeleteTrigger(ctx context.Context, accessToken, id string) error
}

// Verificación en compilación de que el cliente concreto satisface el puerto.
var _ APIPort = (*apiclient.Client)(nil)
