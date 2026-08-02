package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest guarda lo que el cliente puso en el cable: es lo que se afirma en los tests del
// contrato (ruta, cuerpo y cabeceras), porque el contrato ES lo que viaja, no lo que se pretendía.
type capturedRequest struct {
	path   string
	body   string
	bearer string
}

// stubServer levanta un emisor fake que responde status+body fijos y captura la última petición.
func stubServer(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.path = r.URL.Path
		captured.body = string(raw)
		captured.bearer = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

// TestIdentityLoginViajaConElSystemDeLaAplicacion: el login lleva la clave namespaced del catálogo de
// identity. Es lo que el System Gate evalúa; sin ella no hay autorización de aplicación que valga.
func TestIdentityLoginViajaConElSystemDeLaAplicacion(t *testing.T) {
	srv, captured := stubServer(t, http.StatusOK,
		`{"status":"ok","session_id":"s-1","system":"wapp.bff","identity_token":"idt-abc",`+
			`"refresh_token":"rt-abc","expires_in":900,"user":{"id":"u-1","email":"a@b.com"}}`)

	tokens, err := NewIdentityClient(srv.URL, SystemBFF).Login(context.Background(), "a@b.com", "secret")
	if err != nil {
		t.Fatalf("el login contra identity no debía fallar: %v", err)
	}

	if captured.path != "/api/v1/auth/login" {
		t.Errorf("ruta = %q, want /api/v1/auth/login", captured.path)
	}
	if !strings.Contains(captured.body, `"system":"wapp.bff"`) {
		t.Errorf("el cuerpo debía declarar el system de la aplicación, got %q", captured.body)
	}
	if tokens.IdentityToken != "idt-abc" || tokens.RefreshToken != "rt-abc" {
		t.Errorf("el par de tokens no se leyó de la respuesta, got %+v", tokens)
	}
}

// TestIdentityLoginGate403NoEsCredencialInvalida: el System Gate niega con la contraseña CORRECTA. Es
// un caso distinto del 401 y no se colapsa con él: quien lo diagnostique tiene que poder distinguir
// "no eres tú" de "eres tú pero esta aplicación no es tuya".
func TestIdentityLoginGate403NoEsCredencialInvalida(t *testing.T) {
	srv, _ := stubServer(t, http.StatusForbidden, `{"error":"system_access_denied"}`)

	_, err := NewIdentityClient(srv.URL, SystemBFF).Login(context.Background(), "a@b.com", "secret")
	if err == nil {
		t.Fatal("un 403 del gate debía devolver error")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Error("el 403 del gate NO debe confundirse con credenciales inválidas")
	}
	if got := StatusCodeOf(err); got != http.StatusForbidden {
		t.Errorf("el error debía preservar el 403 del emisor, got %d", got)
	}
}

// TestIdentityLoginExigeElParCompleto: una respuesta sin refresh dejaría una sesión sin forma de
// renovarse y el fallo aparecería quince minutos después, lejos de su causa. Se rechaza al llegar.
func TestIdentityLoginExigeElParCompleto(t *testing.T) {
	srv, _ := stubServer(t, http.StatusOK, `{"status":"ok","identity_token":"idt-abc","expires_in":900}`)

	if _, err := NewIdentityClient(srv.URL, SystemBFF).Login(context.Background(), "a@b.com", "x"); err == nil {
		t.Fatal("una respuesta sin refresh_token debía rechazarse")
	}
}

// TestIdentityRefreshNoDeclaraSystem: la aplicación sale de la fila de la sesión en identity, jamás
// del cliente. Si el refresh pudiera declarar system, se canjearía el refresh de una aplicación por
// el token de otra y el System Gate quedaría sorteado.
func TestIdentityRefreshNoDeclaraSystem(t *testing.T) {
	srv, captured := stubServer(t, http.StatusOK,
		`{"status":"ok","session_id":"s-2","system":"wapp.bff","identity_token":"idt-2",`+
			`"refresh_token":"rt-2","expires_in":900}`)

	if _, err := NewIdentityClient(srv.URL, SystemBFF).Refresh(context.Background(), "rt-1"); err != nil {
		t.Fatalf("el refresh contra identity no debía fallar: %v", err)
	}

	if captured.path != "/api/v1/auth/refresh" {
		t.Errorf("ruta = %q, want /api/v1/auth/refresh", captured.path)
	}
	if strings.Contains(captured.body, "system") {
		t.Errorf("el refresh NO debe declarar system, got %q", captured.body)
	}
	if !strings.Contains(captured.body, `"refresh_token":"rt-1"`) {
		t.Errorf("el refresh debía presentar el token vigente, got %q", captured.body)
	}
}

// TestIdentityLogoutPresentaSoloElRefresh: el logout de identity resuelve al usuario server-side a
// partir del refresh. No lleva Bearer —el Context Token que el BFF custodia lo emitió wApp, no
// identity— y cierra solo esa sesión.
func TestIdentityLogoutPresentaSoloElRefresh(t *testing.T) {
	srv, captured := stubServer(t, http.StatusNoContent, "")

	if err := NewIdentityClient(srv.URL, SystemBFF).Logout(context.Background(), "rt-bff"); err != nil {
		t.Fatalf("el logout contra identity no debía fallar: %v", err)
	}

	if captured.path != "/api/v1/auth/logout" {
		t.Errorf("ruta = %q, want /api/v1/auth/logout", captured.path)
	}
	if captured.bearer != "" {
		t.Errorf("el logout de identity no debe llevar Authorization, got %q", captured.bearer)
	}
	if !strings.Contains(captured.body, `"refresh_token":"rt-bff"`) {
		t.Errorf("el logout debía presentar el refresh de la sesión, got %q", captured.body)
	}
}
