// refactor_test.go conservaba dos tests del refactor H6/H7. El primero, TestMapEditorError, se fue
// con el editor de flujos (Plan 047 · T6.6): probaba `mapEditorError` y sus tres specs, que ya no
// existen. El segundo se queda porque NO es del editor y NADIE MÁS lo cubre: es el único test del BFF
// que mira la barra autenticada («Cerrar sesión») y que exige que la decida el contexto validado.
package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"
)

// TestIsAuthenticatedFromContext verifica H7/T6: la barra autenticada se decide por el contexto validado
// (AuthMiddleware), no por la mera presencia de cookie. Una cookie de sesión caducada en una página pública
// NO pinta la navegación autenticada; una sesión válida en una página protegida SÍ.
func TestIsAuthenticatedFromContext(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	// Cookie caducada sobre /login (público, sin AuthMiddleware): no debe pintar "Cerrar sesión".
	expired := makeToken(t, time.Now().Add(-time.Hour))
	value, err := sharedweb.EncodeSession(sharedweb.SessionData{AccessToken: expired, RefreshToken: "r"})
	if err != nil {
		t.Fatalf("sharedweb.EncodeSession: %v", err)
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
