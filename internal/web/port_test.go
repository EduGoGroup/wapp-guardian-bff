package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// fakeAPIPort es un doble en memoria de APIPort para tests que no necesitan HTTP real. Cada método delega
// en un campo función si está puesto; si no, devuelve el cero correspondiente. Verifica en compilación que
// cumple el puerto.
type fakeAPIPort struct {
	refresh     func(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
	sendMessage func(ctx context.Context, accessToken, sessionID, to, text string) (*apiclient.SendResult, error)
}

var _ APIPort = (*fakeAPIPort)(nil)

func (f *fakeAPIPort) Login(context.Context, string, string) (*apiclient.AuthResult, error) {
	return nil, nil
}
func (f *fakeAPIPort) Refresh(ctx context.Context, rt string) (*apiclient.AuthResult, error) {
	if f.refresh != nil {
		return f.refresh(ctx, rt)
	}
	return nil, nil
}
func (f *fakeAPIPort) Logout(context.Context, string, string) error { return nil }
func (f *fakeAPIPort) ListSessions(context.Context, string) ([]apiclient.Session, error) {
	return nil, nil
}
func (f *fakeAPIPort) SetSessionRole(context.Context, string, string, string) error { return nil }
func (f *fakeAPIPort) SendMessage(ctx context.Context, at, sid, to, text string) (*apiclient.SendResult, error) {
	if f.sendMessage != nil {
		return f.sendMessage(ctx, at, sid, to, text)
	}
	return nil, nil
}
func (f *fakeAPIPort) ListFlows(context.Context, string) ([]apiclient.FlowSummary, error) {
	return nil, nil
}
func (f *fakeAPIPort) GetFlow(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeAPIPort) PublishFlow(context.Context, string, []byte) (*apiclient.PublishFlowResult, error) {
	return nil, nil
}
func (f *fakeAPIPort) ListTriggers(context.Context, string) ([]apiclient.Trigger, error) {
	return nil, nil
}
func (f *fakeAPIPort) CreateTrigger(context.Context, string, apiclient.CreateTriggerRequest) (*apiclient.Trigger, error) {
	return nil, nil
}
func (f *fakeAPIPort) DeleteTrigger(context.Context, string, string) error { return nil }

// TestWithAuthRetryRefreshesOn401 ejercita el seam del puerto SIN HTTP: la primera llamada de negocio
// devuelve 401, withAuthRetry refresca (vía el doble) y reintenta con el token nuevo, que ya pasa.
func TestWithAuthRetryRefreshesOn401(t *testing.T) {
	newAccess := makeToken(t, time.Now().Add(time.Hour))
	fake := &fakeAPIPort{
		refresh: func(_ context.Context, _ string) (*apiclient.AuthResult, error) {
			return &apiclient.AuthResult{AccessToken: newAccess, RefreshToken: "r-new"}, nil
		},
	}
	h := NewHandlerWithAPI(authTestCfg("http://api.invalid"), fake)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(ctxAccessToken, "token-viejo")
	c.Set(ctxRefreshToken, "r-old")

	calls := 0
	var seen []string
	err := h.withAuthRetry(c, func(token string) error {
		calls++
		seen = append(seen, token)
		if token == "token-viejo" {
			return apiclient.ErrUnauthorized
		}
		return nil
	})

	if err != nil {
		t.Fatalf("tras refrescar, el reintento debía tener éxito; got %v", err)
	}
	if calls != 2 {
		t.Fatalf("la función debía llamarse 2 veces (intento + reintento), got %d", calls)
	}
	if seen[1] != newAccess {
		t.Errorf("el reintento debía usar el token refrescado, got %q", seen[1])
	}
}

// TestSendMessageViaFakePort comprueba que el Handler opera contra el puerto inyectado (no el cliente
// concreto): el doble devuelve un Ack y sendResultView lo refleja, sin transporte HTTP.
func TestSendMessageViaFakePort(t *testing.T) {
	fake := &fakeAPIPort{
		sendMessage: func(_ context.Context, _, _, _, _ string) (*apiclient.SendResult, error) {
			return &apiclient.SendResult{AckedCommandID: "cmd-fake-1", OK: true}, nil
		},
	}
	h := NewHandlerWithAPI(authTestCfg("http://api.invalid"), fake)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(ctxAccessToken, "tok")

	var result *apiclient.SendResult
	err := h.withAuthRetry(c, func(token string) error {
		var serr error
		result, serr = h.api.SendMessage(c.Request.Context(), token, "s-1", "+1", "hola")
		return serr
	})
	view := sendResultView(result, err)
	if !view.Success || view.CommandID != "cmd-fake-1" {
		t.Fatalf("el envío por el puerto fake debía reflejar el Ack, got %+v", view)
	}
}
