package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"
)

// Este fichero es lo que queda de `dashboard_test.go` tras la retirada del dashboard de sesiones
// (Plan 047 · T2.1): sus veinte funciones eran dieciséis tests de sesiones —que se fueron con su
// código a la consola del cliente— y estos CUATRO helpers, que no eran del dashboard aunque vivieran
// en su fichero. Los usa medio paquete (entitlements, editor, deadline, intakes, catálogo,
// integraciones, delegación…), así que borrar el fichero entero habría sido una retirada que rompe a
// terceros. Se conserva el fichero por el historial de git y se renombra por lo que de verdad tiene
// dentro.

// routedAPI levanta una API pública fake que responde según método+ruta. Cada entrada del mapa es
// "MÉTODO /ruta" → (status, body). Una ruta no mapeada responde 500 (fuerza al test a declarar lo que usa).
func routedAPI(routes map[string]struct {
	status int
	body   string
}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if resp, ok := routes[r.Method+" "+r.URL.Path]; ok {
			w.WriteHeader(resp.status)
			_, _ = io.WriteString(w, resp.body)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ruta no mapeada"}`)
	}))
}

// validSessionCookie arma la cookie de sesión con un access token vigente (para pasar el AuthMiddleware).
func validSessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	access := makeToken(t, time.Now().Add(time.Hour))
	value, err := sharedweb.EncodeSession(sharedweb.SessionData{AccessToken: access, RefreshToken: "r-ok"})
	if err != nil {
		t.Fatalf("sharedweb.EncodeSession: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: value}
}

func getWithCookie(router http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}

func postFormWithCookie(router http.Handler, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	csrf := mintCSRF(router)
	form.Set(sharedweb.CSRFFieldName, csrf.Value)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}
