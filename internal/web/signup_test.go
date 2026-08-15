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

// signupTestServer levanta un backend fake de POST /api/v1/signup que responde el status y el cuerpo
// dados EN TEXTO PLANO — el contrato real de wapp-cloud-platform/internal/platformadmin/signup.go
// (http.Error para todos sus errores, no JSON) — para probar que el BFF decodifica ese cuerpo y
// muestra un mensaje honesto en vez de caer siempre al genérico (A-11).
func signupTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signup" && r.Method == http.MethodPost {
			http.Error(w, body, status)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func signupForm() url.Values {
	return url.Values{
		"first_name": {"Pedro"},
		"last_name":  {"Perez"},
		"email":      {"pedro@example.com"},
		"password":   {"Password123456!"},
	}
}

func TestDoSignup_EmailTaken409(t *testing.T) {
	t.Parallel()
	api := signupTestServer(t, http.StatusConflict, "ese correo ya tiene cuenta: entra con tu clave")
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/signup", signupForm())

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409. Body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ya existe una cuenta con ese correo: entra con tu clave de siempre.") {
		t.Fatalf("mensaje honesto de 409 no encontrado en: %s", body)
	}
	// El texto crudo de la plataforma NO debe filtrarse; solo el mensaje traducido.
	if strings.Contains(body, "ese correo ya tiene cuenta: entra con tu clave") {
		t.Fatalf("el cuerpo crudo de la plataforma se filtró al usuario: %s", body)
	}
}

func TestDoSignup_ServiceUnavailable503(t *testing.T) {
	t.Parallel()
	api := signupTestServer(t, http.StatusServiceUnavailable, "registro no disponible")
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/signup", signupForm())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503. Body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "El alta no está disponible ahora mismo. Inténtalo más tarde.") {
		t.Fatalf("mensaje honesto de 503 no encontrado en: %s", body)
	}
	// Nada que sugiera que fue culpa del usuario.
	if strings.Contains(body, "Revisa") || strings.Contains(body, "correo") {
		t.Fatalf("el mensaje de 503 no debe sugerir culpa del usuario: %s", body)
	}
}

func TestDoSignup_ValidationDetail400(t *testing.T) {
	t.Parallel()
	api := signupTestServer(t, http.StatusBadRequest, "la contraseña no cumple la política de seguridad (mínimo 12 caracteres)")
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/signup", signupForm())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "la contraseña no cumple la política de seguridad (mínimo 12 caracteres)") {
		t.Fatalf("detalle de validación del 400 no propagado: %s", body)
	}
}

func TestDoSignup_RateLimited429(t *testing.T) {
	t.Parallel()
	api := signupTestServer(t, http.StatusTooManyRequests, "demasiadas solicitudes desde esta IP")
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/signup", signupForm())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429. Body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Demasiados intentos, espera un momento.") {
		t.Fatalf("mensaje honesto de 429 no encontrado en: %s", body)
	}
}

func TestDoSignup_OtherStatusFallsBackToGeneric(t *testing.T) {
	t.Parallel()
	// 502 (p.ej. ReplaceUserSystems o identity caídos): no está en la lista explícita de A-11, así
	// que debe caer al mensaje genérico — nunca reflejar el texto crudo del upstream.
	api := signupTestServer(t, http.StatusBadGateway, "error al configurar aplicaciones")
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := postForm(router, "/signup", signupForm())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (genérico). Body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No se pudo procesar la solicitud. Inténtalo más tarde.") {
		t.Fatalf("mensaje genérico no encontrado en: %s", body)
	}
	if strings.Contains(body, "error al configurar aplicaciones") {
		t.Fatalf("el cuerpo crudo del upstream se filtró al usuario: %s", body)
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
