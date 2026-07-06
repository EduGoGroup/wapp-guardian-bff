package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

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
	value, err := encodeSession(sessionData{AccessToken: access, RefreshToken: "r-ok"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
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
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	router.ServeHTTP(rec, req)
	return rec
}

// TestDashboardRendersSessionTable: GET / con sesiones del fixture → la tabla las pinta (REQ-D1).
func TestDashboardRendersSessionTable(t *testing.T) {
	body := `[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","role":"bot","self_pn":"593999000111"},` +
		`{"session_id":"s-2","edge_id":"edge-beta","state":"offline","role":"passive"}]`
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, body},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"593999000111", "edge-alpha", "online", "bot", "edge-beta", "offline", "passive"} {
		if !strings.Contains(out, want) {
			t.Errorf("la tabla debía contener %q", want)
		}
	}
	// Sin número (s-2) cae al session_id en la primera columna.
	if !strings.Contains(out, "s-2") {
		t.Error("la sesión sin self_pn debía mostrar su session_id")
	}
}

// TestDashboardDegradesWhenListFails: si ListSessions falla → aviso degradado + input manual de session_id
// (REQ-D4).
func TestDashboardDegradesWhenListFails(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusInternalServerError, `{"error":"boom"}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("el dashboard degradado debía seguir sirviendo 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "No se pudieron cargar las sesiones") {
		t.Error("el modo degradado debía avisar del fallo del listado")
	}
	// En degradado el envío usa un input de texto para el session_id (no un <select>).
	if !strings.Contains(out, `id="session_id"`) || strings.Contains(out, "<select") {
		t.Error("en degradado el session_id debía introducirse a mano (input, no select)")
	}
	// No se filtra el detalle crudo del upstream.
	if strings.Contains(out, "boom") {
		t.Error("no debe filtrarse el detalle del upstream")
	}
}

// TestSendShowsAck: POST /send con la API devolviendo 200 ok → muestra el acked_command_id (REQ-D3).
func TestSendShowsAck(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"POST /api/v1/messages": {http.StatusOK, `{"acked_command_id":"cmd-abc123","ok":true}`},
		"GET /api/v1/sessions":  {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"session_id": {"s-1"}, "to": {"+593999000111"}, "text": {"hola"}}
	rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("envío OK debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "snackbar--success") {
		t.Error("envío OK debía mostrar un snackbar de éxito")
	}
	if !strings.Contains(out, "cmd-abc123") {
		t.Error("envío OK debía mostrar el acked_command_id")
	}
}

// TestSendMapsBusinessErrors: 404/502/504 se traducen a mensajes legibles, sin trazas (REQ-D3).
func TestSendMapsBusinessErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"ajena", http.StatusNotFound, "no es tuya o no existe"},
		{"offline", http.StatusBadGateway, "está desconectado"},
		{"timeout", http.StatusGatewayTimeout, "tardó demasiado"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := routedAPI(map[string]struct {
				status int
				body   string
			}{
				"POST /api/v1/messages": {tc.status, `{"error":"detalle interno que no debe verse"}`},
				"GET /api/v1/sessions":  {http.StatusOK, `[]`},
			})
			defer api.Close()

			router := NewRouter(authTestCfg(api.URL))
			form := url.Values{"session_id": {"s-1"}, "to": {"+1"}, "text": {"hola"}}
			rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

			out := rec.Body.String()
			if !strings.Contains(out, "snackbar--error") {
				t.Errorf("%s debía mostrar un snackbar de error", tc.name)
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("%s debía mostrar %q; body=%s", tc.name, tc.wantSubstr, out)
			}
			if strings.Contains(out, "detalle interno que no debe verse") {
				t.Errorf("%s no debía filtrar el detalle del upstream", tc.name)
			}
		})
	}
}

// TestSendValidatesEmptyFields: campos vacíos → error legible sin llamar a la API.
func TestSendValidatesEmptyFields(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK, `[]`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	form := url.Values{"session_id": {""}, "to": {""}, "text": {""}}
	rec := postFormWithCookie(router, "/send", form, validSessionCookie(t))

	if !strings.Contains(rec.Body.String(), "Elige una sesión") {
		t.Error("campos vacíos debían pedir completar el formulario")
	}
}

// TestDashboardWithoutCookieRedirects: GET / y POST /send sin cookie → redirect a /login (AuthMiddleware de
// T2).
func TestDashboardWithoutCookieRedirects(t *testing.T) {
	router := NewRouter(authTestCfg("http://api.invalid"))

	rec := getWithCookie(router, "/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET / sin cookie debía redirigir a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = postFormWithCookie(router, "/send", url.Values{"session_id": {"s"}, "to": {"+1"}, "text": {"h"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("POST /send sin cookie debía redirigir a /login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
