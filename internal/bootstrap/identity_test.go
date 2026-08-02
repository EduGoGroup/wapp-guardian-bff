package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ecJWKSBody arma un documento JWKS con una única clave EC P-256 recién generada, en la forma que
// exige RFC 7517/7518: coordenadas de 32 octetos en base64url sin padding.
//
// Las coordenadas se leen del punto sin comprimir que devuelve PublicKey.Bytes (SEC 1 §2.3.3:
// 0x04 || X || Y) porque los campos X e Y del ecdsa.PublicKey quedaron deprecados en Go 1.26.
func ecJWKSBody(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("no se pudo generar la clave P-256 de prueba: %v", err)
	}
	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("no se pudo serializar la clave pública de prueba: %v", err)
	}
	half := (len(point) - 1) / 2

	body, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC",
		"crv": "P-256",
		"kid": "test-2026-v1",
		"use": "sig",
		"alg": "ES256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1 : 1+half]),
		"y":   base64.RawURLEncoding.EncodeToString(point[1+half:]),
	}}})
	if err != nil {
		t.Fatalf("no se pudo serializar el JWKS de prueba: %v", err)
	}
	return body
}

// TestNewIdentityVerifierDisabledWithoutURL fija la puerta del modo dual: sin
// WAPP_IDENTITY_JWKS_URL no se construye verificador y el arranque no consulta a nadie.
func TestNewIdentityVerifierDisabledWithoutURL(t *testing.T) {
	for _, valor := range []string{"", "   "} {
		cfg := hardenedCfg()
		cfg.IdentityJWKSURL = valor

		verifier, err := newIdentityVerifier(&cfg)
		if err != nil {
			t.Fatalf("con la variable vacía (%q) el arranque no debía fallar, got %v", valor, err)
		}
		if verifier != nil {
			t.Fatalf("con la variable vacía (%q) no debía construirse verificador", valor)
		}
	}
}

// TestNewIdentityVerifierBuildsFromJWKS comprueba el camino feliz del modo dual: con la variable
// puesta y un emisor que responde, el verificador queda construido en el arranque.
func TestNewIdentityVerifierBuildsFromJWKS(t *testing.T) {
	body := ecJWKSBody(t)
	emisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer emisor.Close()

	cfg := hardenedCfg()
	cfg.IdentityJWKSURL = emisor.URL + "/.well-known/jwks.json"

	verifier, err := newIdentityVerifier(&cfg)
	if err != nil {
		t.Fatalf("el verificador debía construirse contra un JWKS válido, got %v", err)
	}
	if verifier == nil {
		t.Fatal("el verificador no debía ser nil con el modo dual activado")
	}
}

// TestNewIdentityVerifierFailsClosedWhenIssuerIsDown fija el fail-closed: con la variable puesta y el
// emisor caído, el verificador NO se construye. No es un modo degradado que haya que tolerar — es la
// razón de que la puerta exista.
func TestNewIdentityVerifierFailsClosedWhenIssuerIsDown(t *testing.T) {
	cfg := hardenedCfg()
	cfg.IdentityJWKSURL = "http://" + freeAddr(t) + "/.well-known/jwks.json"

	verifier, err := newIdentityVerifier(&cfg)
	if err == nil {
		t.Fatal("con el emisor caído el arranque debía fallar (fail-closed)")
	}
	if verifier != nil {
		t.Fatal("un arranque fallido no debe devolver verificador")
	}
}

// TestServeWithContextFailsWhenIdentityIsUnreachable comprueba que el fail-closed llega hasta el
// arranque completo: el servidor no alcanza a escuchar si el modo dual está encendido y no hay emisor.
func TestServeWithContextFailsWhenIdentityIsUnreachable(t *testing.T) {
	cfg := hardenedCfg()
	cfg.HTTPAddr = freeAddr(t)
	cfg.IdentityJWKSURL = "http://" + freeAddr(t) + "/.well-known/jwks.json"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ServeWithContext(ctx, &cfg, nil); err == nil {
		t.Fatal("el BFF no debía arrancar con el modo dual encendido y identity apagado")
	}
}
