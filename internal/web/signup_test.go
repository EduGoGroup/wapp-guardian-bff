package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/golang-jwt/jwt/v5"
)

func TestShowSignup_RendersForm(t *testing.T) {
	t.Parallel()
	router := NewRouter(authTestCfg("http://api.invalid"))
	req := httptest.NewRequest(http.MethodGet, "/signup", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /signup status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Crear cuenta") {
		t.Fatal("esperado 'Crear cuenta' en la página de signup")
	}
	if !strings.Contains(body, `action="/signup"`) {
		t.Fatal("esperado form action /signup")
	}
}

func TestDoSignup_Success(t *testing.T) {
	t.Parallel()
	var (
		signupCalled bool
		receivedBody map[string]string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signup" && r.Method == http.MethodPost {
			signupCalled = true
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &receivedBody)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"message":"Listo. Entra con tu correo y tu clave."}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))

	form := url.Values{
		"first_name": {"Pedro"},
		"last_name":  {"Perez"},
		"email":      {"pedro@example.com"},
		"password":   {"Password123456!"},
	}
	rec := postForm(router, "/signup", form)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /signup status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}
	if !signupCalled {
		t.Fatal("la API /api/v1/signup no fue invocada")
	}
	if receivedBody["origin"] != "bff" {
		t.Fatalf("origin = %s, want bff", receivedBody["origin"])
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Listo. Entra con tu correo y tu clave.") {
		t.Fatalf("mensaje constante de éxito no encontrado en cuerpo: %s", body)
	}
}

func TestPendingState_RedirectsWhenNoTenant(t *testing.T) {
	t.Parallel()
	router := NewRouter(authTestCfg("http://api.invalid"))

	// Crear token válido sin tenant (estado en espera, Plan 056 · T3.5 / D-056.12)
	claims := sharedjwt.Claims{
		TenantID:         "", // Sin tenant
		UserID:           "u-123",
		Roles:            []string{},
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}
	tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("dummy"))
	if err != nil {
		t.Fatalf("firmar token: %v", err)
	}

	cookieVal, err := encodeSession(sessionData{AccessToken: tokenStr, RefreshToken: "rt-1"})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: cookieVal}

	// 1. Acceder a / debe redirigir a /pending
	reqHome := httptest.NewRequest(http.MethodGet, "/", nil)
	reqHome.AddCookie(cookie)
	recHome := httptest.NewRecorder()
	router.ServeHTTP(recHome, reqHome)

	if recHome.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want 303", recHome.Code)
	}
	if recHome.Header().Get("Location") != "/pending" {
		t.Fatalf("Location = %q, want /pending", recHome.Header().Get("Location"))
	}

	// 2. Acceder a /pending debe mostrar la pantalla de espera sin menú ni llamadas de negocio
	reqPending := httptest.NewRequest(http.MethodGet, "/pending", nil)
	reqPending.AddCookie(cookie)
	recPending := httptest.NewRecorder()
	router.ServeHTTP(recPending, reqPending)

	if recPending.Code != http.StatusOK {
		t.Fatalf("GET /pending status = %d, want 200", recPending.Code)
	}

	body := recPending.Body.String()
	if !strings.Contains(body, "Tu acceso está en revisión") {
		t.Fatal("esperado 'Tu acceso está en revisión' en /pending")
	}
	if strings.Contains(body, "Sesiones") || strings.Contains(body, "Flujos") || strings.Contains(body, "Solicitudes") {
		t.Fatal("la pantalla /pending no debe contener opciones de menú de negocio")
	}
}
