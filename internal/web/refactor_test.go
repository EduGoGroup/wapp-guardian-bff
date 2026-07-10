package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// TestMapEditorError verifica que el mapper unificado (H6/T6) preserva status y mensaje por caso y spec:
// 401→sesión expirada; rechazo→400 con el prefijo de la entidad; 404→aviso "no existe"; genérico→502.
func TestMapEditorError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		spec       upstreamErrorSpec
		wantStatus int
		wantMsg    string
	}{
		{
			"unauthorized (publish)", apiclient.ErrUnauthorized, publishFlowErrorSpec,
			http.StatusUnauthorized, sessionExpiredMessage,
		},
		{
			"rejection (publish)", &apiclient.RejectionError{Op: "publish", StatusCode: 400, Message: "nodo inválido"},
			publishFlowErrorSpec, http.StatusBadRequest, "La plataforma rechazó la definición: nodo inválido",
		},
		{
			"rejection (create trigger)", &apiclient.RejectionError{Op: "trigger", StatusCode: 400, Message: "keyword requerido"},
			createTriggerErrorSpec, http.StatusBadRequest, "La plataforma rechazó la regla: keyword requerido",
		},
		{
			"not found (delete trigger)", &apiclient.APIError{Op: "delete", StatusCode: http.StatusNotFound},
			deleteTriggerErrorSpec, http.StatusNotFound, "Esa regla ya no existe o no es tuya.",
		},
		{
			"genérico (publish)", &apiclient.APIError{Op: "publish", StatusCode: http.StatusBadGateway},
			publishFlowErrorSpec, http.StatusBadGateway, "No se pudo publicar el flujo. Inténtalo más tarde.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, notice := mapEditorError(tc.err, tc.spec)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if notice == nil || notice.Success {
				t.Fatalf("notice debía ser un aviso de error, got %+v", notice)
			}
			if notice.Message != tc.wantMsg {
				t.Errorf("mensaje = %q, want %q", notice.Message, tc.wantMsg)
			}
		})
	}
}

// TestIsAuthenticatedFromContext verifica H7/T6: la barra autenticada se decide por el contexto validado
// (AuthMiddleware), no por la mera presencia de cookie. Una cookie de sesión caducada en una página pública
// NO pinta la navegación autenticada; una sesión válida en una página protegida SÍ.
func TestIsAuthenticatedFromContext(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	// Cookie caducada sobre /login (público, sin AuthMiddleware): no debe pintar "Cerrar sesión".
	expired := makeToken(t, time.Now().Add(-time.Hour))
	value, err := encodeSession(sessionData{AccessToken: expired, RefreshToken: "r"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	rec := getWithCookie(router, "/login", &http.Cookie{Name: sessionCookieName, Value: value})
	if rec.Code != http.StatusOK {
		t.Fatalf("/login debía renderizar 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Cerrar sesión") {
		t.Error("una cookie caducada NO debía pintar la barra autenticada (H7)")
	}

	// Sesión válida sobre / (protegida): sí pinta la navegación autenticada.
	recAuth := getWithCookie(router, "/", validSessionCookie(t))
	if recAuth.Code != http.StatusOK {
		t.Fatalf("/ con sesión válida debía renderizar 200, got %d", recAuth.Code)
	}
	if !strings.Contains(recAuth.Body.String(), "Cerrar sesión") {
		t.Error("una sesión válida SÍ debía pintar la barra autenticada")
	}
}
