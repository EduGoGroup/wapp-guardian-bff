package apiclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestExchangeDevuelveElContextToken: el canje presenta el Identity Token y recibe SOLO el Context
// Token con su vencimiento. No hay refresh en la respuesta y no debe haberlo: el refresh es de
// identity y vive donde vive la sesión.
func TestExchangeDevuelveElContextToken(t *testing.T) {
	srv, captured := stubServer(t, http.StatusOK,
		`{"context_token":"ctx-abc","expires_at":"2026-08-02T18:00:00Z"}`)

	res, err := NewExchangeClient(NewTransport(srv.URL)).Exchange(context.Background(), "idt-abc")
	if err != nil {
		t.Fatalf("el canje no debía fallar: %v", err)
	}

	if captured.path != "/api/v1/auth/exchange" {
		t.Errorf("ruta = %q, want /api/v1/auth/exchange", captured.path)
	}
	if !strings.Contains(captured.body, `"identity_token":"idt-abc"`) {
		t.Errorf("el canje debía presentar el Identity Token, got %q", captured.body)
	}
	if res.ContextToken != "ctx-abc" || res.ExpiresAt != "2026-08-02T18:00:00Z" {
		t.Errorf("no se leyó la respuesta del canje, got %+v", res)
	}
}

// TestExchange503EsModoDualApagado: la plataforma sin verificador de Identity Tokens no puede canjear
// nada. Tiene error propio porque no es una avería: es un despliegue a medias —el BFF delega y la
// plataforma todavía no verifica— y se arregla configurando, no reintentando.
func TestExchange503EsModoDualApagado(t *testing.T) {
	srv, _ := stubServer(t, http.StatusServiceUnavailable, `{"error":"dual_mode_off"}`)

	_, err := NewExchangeClient(NewTransport(srv.URL)).Exchange(context.Background(), "idt-abc")
	if !errors.Is(err, ErrDualModeOff) {
		t.Fatalf("un 503 del canje debía ser ErrDualModeOff, got %v", err)
	}
}

// TestExchange401EsUsuarioNoMigrado: el `sub` del Identity Token no corresponde a ningún usuario de
// wApp. Es un error explícito del contrato —no se crea un usuario al vuelo— y viaja como no
// autorizado, que es lo que hace que el BFF limpie la sesión en vez de degradar.
func TestExchange401EsUsuarioNoMigrado(t *testing.T) {
	srv, _ := stubServer(t, http.StatusUnauthorized, `{"error":"user_not_migrated"}`)

	_, err := NewExchangeClient(NewTransport(srv.URL)).Exchange(context.Background(), "idt-abc")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("un 401 del canje debía ser ErrUnauthorized, got %v", err)
	}
}

// TestExchangeSinContextTokenEsError: un 200 vacío dejaría al BFF custodiando una cookie sin token y
// el usuario descubriría el fallo en la siguiente llamada de negocio, no en el login.
func TestExchangeSinContextTokenEsError(t *testing.T) {
	srv, _ := stubServer(t, http.StatusOK, `{"expires_at":"2026-08-02T18:00:00Z"}`)

	if _, err := NewExchangeClient(NewTransport(srv.URL)).Exchange(context.Background(), "idt-abc"); err == nil {
		t.Fatal("un canje sin context_token debía rechazarse")
	}
}
