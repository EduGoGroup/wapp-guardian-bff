package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// upstreamStub es un doble de servidor HTTP que responde por ruta y registra lo que recibió: cuántas
// veces, con qué cuerpo y con qué Bearer. Sirve igual para identity-api y para la plataforma —el BFF
// los distingue por URL base—, y una ruta no declarada responde 404 para que el test tenga que decir
// qué esperaba que se llamara.
type upstreamStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	hits   map[string]int
	bodies map[string]string
	auths  map[string]string
}

func newUpstreamStub(t *testing.T) *upstreamStub {
	t.Helper()
	s := &upstreamStub{
		routes: map[string]http.HandlerFunc{},
		hits:   map[string]int{},
		bodies: map[string]string{},
		auths:  map[string]string{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.bodies[r.URL.Path] = string(raw)
		s.auths[r.URL.Path] = r.Header.Get("Authorization")
		handler, declared := s.routes[r.URL.Path]
		s.mu.Unlock()

		if !declared {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// on declara la respuesta de una ruta.
func (s *upstreamStub) on(path string, handler http.HandlerFunc) *upstreamStub {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = handler
	return s
}

// onJSON declara una ruta que responde siempre 200 con el mismo cuerpo.
func (s *upstreamStub) onJSON(path, body string) *upstreamStub {
	return s.on(path, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
}

func (s *upstreamStub) hitsOf(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *upstreamStub) bodyOf(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[path]
}

func (s *upstreamStub) bearerOf(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auths[path]
}

func (s *upstreamStub) url() string { return s.srv.URL }

// delegatedCfg arma la config con la PUERTA de la delegación abierta: identity para las credenciales,
// plataforma para el canje y el negocio.
func delegatedCfg(platformURL, identityURL string) *config.Config {
	cfg := authTestCfg(platformURL)
	cfg.IdentityBaseURL = identityURL
	return cfg
}

// makeIdentityToken firma un token con la forma de un Identity Token de identity-core: identifica al
// titular (`sub`) y a la aplicación, y NO lleva claims de negocio —no puede llevarlas—. El BFF no
// verifica firmas (parse-unverified), así que el algoritmo del doble es irrelevante; lo que importa
// es que si este token acabara en la cookie, el tenant desaparecería.
func makeIdentityToken(t *testing.T, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":       "identity-core",
		"sub":       "11111111-2222-3333-4444-555555555555",
		"system":    "wapp.bff",
		"token_use": "identity",
		"exp":       exp.Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("dummy-secret"))
	if err != nil {
		t.Fatalf("no se pudo firmar el Identity Token de prueba: %v", err)
	}
	return signed
}

// identityLoginBody arma la respuesta de identity-api para login y refresh.
func identityLoginBody(identityToken, refreshToken string) string {
	return fmt.Sprintf(
		`{"status":"ok","session_id":"sess-bff","system":"wapp.bff","identity_token":%q,`+
			`"refresh_token":%q,"expires_in":900,`+
			`"user":{"id":"11111111-2222-3333-4444-555555555555","email":"a@b.com"}}`,
		identityToken, refreshToken)
}

// exchangeBody arma la respuesta del canje tal como la emite la plataforma, con el sobre completo.
// El BFF ignora `context` a propósito: el tenant sale de las claims del Context Token, no del sobre.
func exchangeBody(contextToken string, exp time.Time) string {
	return fmt.Sprintf(
		`{"context_token":%q,"token_type":"Bearer","expires_at":%q,`+
			`"context":{"tenant_id":"t-1","user_id":"u-1","roles":["admin"]}}`,
		contextToken, exp.UTC().Format(time.RFC3339))
}

// sessionFromCookie decodifica la cookie de sesión y devuelve también su JSON en claro, que es lo que
// permite afirmar qué NO hay dentro: la comparación contra el valor crudo de la cookie no serviría,
// porque va en base64 y cualquier token estaría igual de "ausente".
func sessionFromCookie(t *testing.T, rec *httptest.ResponseRecorder) (sessionData, string) {
	t.Helper()
	var value string
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == sessionCookieName {
			value = sc.Value
		}
	}
	if value == "" {
		t.Fatal("no se emitió cookie de sesión")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("la cookie de sesión no es base64-URL: %v", err)
	}
	var sess sessionData
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("la cookie de sesión no es JSON: %v", err)
	}
	return sess, string(raw)
}

// sessionCookieMaxAge devuelve el Max-Age (segundos) de la cookie de sesión emitida.
func sessionCookieMaxAge(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == sessionCookieName {
			return sc.MaxAge
		}
	}
	t.Fatal("no se emitió cookie de sesión")
	return 0
}

// cookieWith arma la cookie de sesión con un par (access, refresh) dado.
func cookieWith(t *testing.T, access, refresh string) *http.Cookie {
	t.Helper()
	value, err := encodeSession(sessionData{AccessToken: access, RefreshToken: refresh})
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: value}
}

// --- La puerta cerrada: sin WAPP_IDENTITY_URL no cambia NADA ---

// TestSinIdentityURLElLoginSigueSiendoLegacy: sin la variable, el login va a la API pública de wApp y
// no se canjea nada. No hay siquiera un identity al que llamar en este test — si el BFF lo intentara,
// no tendría a dónde ir.
func TestSinIdentityURLElLoginSigueSiendoLegacy(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	access := makeToken(t, exp)
	platform := newUpstreamStub(t)
	platform.onJSON("/api/v1/auth/login", loginBody(access, "r-legacy", exp))

	router := NewRouter(authTestCfg(platform.url()))
	rec := postForm(router, "/login", url.Values{"email": {"a@b.com"}, "password": {"secret"}})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("el login legacy debía redirigir 303, got %d", rec.Code)
	}
	if got := platform.hitsOf("/api/v1/auth/login"); got != 1 {
		t.Errorf("el login debía ir a la API pública de wApp una vez, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/exchange"); got != 0 {
		t.Errorf("con la puerta cerrada no debe canjearse nada, got %d canjes", got)
	}

	sess, _ := sessionFromCookie(t, rec)
	if sess.AccessToken != access || sess.RefreshToken != "r-legacy" {
		t.Errorf("la cookie debía custodiar el par de la plataforma, got %+v", sess)
	}
}

// TestSinIdentityURLElLogoutSigueSiendoLegacy: el cierre de sesión también se queda donde estaba.
func TestSinIdentityURLElLogoutSigueSiendoLegacy(t *testing.T) {
	platform := newUpstreamStub(t)
	platform.onJSON("/api/v1/auth/logout", `{}`)

	router := NewRouter(authTestCfg(platform.url()))
	access := makeToken(t, time.Now().Add(time.Hour))
	rec := postFormWithCookie(router, "/logout", url.Values{}, cookieWith(t, access, "r-legacy"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("el logout debía redirigir 303, got %d", rec.Code)
	}
	if got := platform.hitsOf("/api/v1/auth/logout"); got != 1 {
		t.Errorf("el logout legacy debía ir a la plataforma, got %d", got)
	}
}

// --- La puerta abierta: delegación (T3.2) ---

// TestDelegacionLoginCanjeaYCustodiaSoloElContextToken es el corazón de T3.2: login contra identity
// con el system de la aplicación, canje inmediato contra la plataforma, y una cookie que custodia el
// Context Token y el refresh de identity. El Identity Token NO se persiste: vive el instante
// server-side del canje y no aparece en la sesión ni en claro ni codificado.
func TestDelegacionLoginCanjeaYCustodiaSoloElContextToken(t *testing.T) {
	// El canje vence en 30 min y el Identity Token declara 15 (expires_in: 900). Que no coincidan es
	// deliberado: es lo que permite afirmar CUÁL de los dos vencimientos acabó en la cookie.
	exp := time.Now().Add(30 * time.Minute)
	identityToken := makeIdentityToken(t, exp)
	contextToken := makeToken(t, exp)

	identity := newUpstreamStub(t)
	identity.onJSON("/api/v1/auth/login", identityLoginBody(identityToken, "rt-identity"))
	platform := newUpstreamStub(t)
	platform.onJSON("/api/v1/auth/exchange", exchangeBody(contextToken, exp))

	router := NewRouter(delegatedCfg(platform.url(), identity.url()))
	rec := postForm(router, "/login", url.Values{"email": {"a@b.com"}, "password": {"secret"}})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("el login delegado debía redirigir 303, got %d", rec.Code)
	}
	if got := identity.hitsOf("/api/v1/auth/login"); got != 1 {
		t.Errorf("las credenciales debían viajar a identity una vez, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/login"); got != 0 {
		t.Errorf("con la delegación encendida la plataforma NO debe ver credenciales, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/exchange"); got != 1 {
		t.Errorf("el canje debía ejecutarse una vez, got %d", got)
	}

	if body := identity.bodyOf("/api/v1/auth/login"); !strings.Contains(body, `"system":"wapp.bff"`) {
		t.Errorf("identity debía recibir el system de la aplicación, got %q", body)
	}
	if body := platform.bodyOf("/api/v1/auth/exchange"); !strings.Contains(body, identityToken) {
		t.Error("el canje debía presentar el Identity Token recién emitido")
	}

	sess, rawJSON := sessionFromCookie(t, rec)
	if sess.AccessToken != contextToken {
		t.Error("la cookie debía custodiar el CONTEXT Token, no otro")
	}
	if sess.RefreshToken != "rt-identity" {
		t.Errorf("la cookie debía custodiar el refresh de identity, got %q", sess.RefreshToken)
	}
	if strings.Contains(rawJSON, identityToken) {
		t.Error("el Identity Token NO debe persistirse en la sesión: muere en el canje")
	}
	// El tercer campo del contrato de la cookie: el exp que se custodia es el del CONTEXTO, el que la
	// plataforma acotó al canjear, no el que declaró identity. Con 900 segundos en la respuesta de
	// identity y 30 min en la del canje, guardar el equivocado se ve.
	if want := exp.UTC().Format(time.RFC3339); sess.ExpiresAt != want {
		t.Errorf("la cookie debía custodiar el exp del contexto (%s), got %q", want, sess.ExpiresAt)
	}
	// Y la vida de la cookie lo sigue: ~30 min, ni el default de 1h ni los 15 min de identity.
	if maxAge := sessionCookieMaxAge(t, rec); maxAge < 1700 || maxAge > 1800 {
		t.Errorf("la cookie debía vivir lo que el Context Token (~1800s), got %d", maxAge)
	}
	// El tenant se lee del Context Token; si el de identity hubiera llegado a la cookie, no habría.
	claims, err := parseAccessClaims(sess.AccessToken)
	if err != nil || claims.TenantID == "" {
		t.Errorf("el token custodiado debía llevar el tenant del negocio, got %+v (err %v)", claims, err)
	}
}

// TestDelegacionRefrescaProactivamenteYReCanjea ejercita la mitad PROACTIVA de la cascada (REQ-A3): a
// menos de dos minutos del vencimiento del Context Token, el AuthMiddleware refresca contra identity y
// vuelve a canjear en el mismo movimiento server-side, y la petición continúa con el token nuevo.
func TestDelegacionRefrescaProactivamenteYReCanjea(t *testing.T) {
	nuevoExp := time.Now().Add(time.Hour)
	identityToken := makeIdentityToken(t, nuevoExp)
	contextToken2 := makeToken(t, nuevoExp)

	identity := newUpstreamStub(t)
	identity.onJSON("/api/v1/auth/refresh", identityLoginBody(identityToken, "rt-2"))
	platform := newUpstreamStub(t)
	platform.onJSON("/api/v1/auth/exchange", exchangeBody(contextToken2, nuevoExp))
	platform.onJSON("/api/v1/sessions", `[]`)

	router := NewRouter(delegatedCfg(platform.url(), identity.url()))
	// Context Token vigente pero dentro del margen proactivo de 2 minutos.
	porVencer := makeToken(t, time.Now().Add(time.Minute))
	rec := getWithCookie(router, "/", cookieWith(t, porVencer, "rt-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("la petición debía continuar tras el refresh, got %d", rec.Code)
	}
	if got := identity.hitsOf("/api/v1/auth/refresh"); got != 1 {
		t.Errorf("debía refrescarse contra identity una vez, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/exchange"); got != 1 {
		t.Errorf("debía re-canjearse una vez, got %d", got)
	}
	if body := identity.bodyOf("/api/v1/auth/refresh"); !strings.Contains(body, `"refresh_token":"rt-1"`) {
		t.Errorf("identity debía recibir el refresh vigente de la cookie, got %q", body)
	}
	if got := platform.bearerOf("/api/v1/sessions"); got != "Bearer "+contextToken2 {
		t.Error("el negocio debía llamarse con el Context Token RE-CANJEADO")
	}

	sess, rawJSON := sessionFromCookie(t, rec)
	if sess.AccessToken != contextToken2 || sess.RefreshToken != "rt-2" {
		t.Errorf("la cookie debía renovarse con el par nuevo, got %+v", sess)
	}
	if strings.Contains(rawJSON, identityToken) {
		t.Error("el Identity Token del refresh tampoco debe persistirse")
	}
}

// TestDelegacionRefrescaAnte401YReintenta ejercita la mitad PASIVA de la cascada (REQ-A3): el Context
// Token aún no vencía, pero la plataforma responde 401 —lo revocó, o el reloj no coincide—, así que el
// BFF refresca contra identity, re-canjea y REINTENTA la llamada de negocio. El navegador no ve nada
// de esto: solo recibe la página pintada.
func TestDelegacionRefrescaAnte401YReintenta(t *testing.T) {
	nuevoExp := time.Now().Add(time.Hour)
	identityToken := makeIdentityToken(t, nuevoExp)
	contextToken2 := makeToken(t, nuevoExp)

	identity := newUpstreamStub(t)
	identity.onJSON("/api/v1/auth/refresh", identityLoginBody(identityToken, "rt-2"))

	platform := newUpstreamStub(t)
	platform.onJSON("/api/v1/auth/exchange", exchangeBody(contextToken2, nuevoExp))
	var llamadas int
	platform.on("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		llamadas++
		if llamadas == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"token_revoked"}`)
			return
		}
		_, _ = io.WriteString(w,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","role":"bot"}]`)
	})

	router := NewRouter(delegatedCfg(platform.url(), identity.url()))
	// Context Token holgado: el refresh NO lo dispara el margen proactivo, lo dispara el 401.
	vigente := makeToken(t, time.Now().Add(time.Hour))
	rec := getWithCookie(router, "/", cookieWith(t, vigente, "rt-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("tras el 401 y el refresh la página debía pintarse, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "edge-alpha") {
		t.Error("el reintento debía traer las sesiones del tenant")
	}
	if got := platform.hitsOf("/api/v1/sessions"); got != 2 {
		t.Errorf("debía reintentarse la llamada de negocio (2 intentos), got %d", got)
	}
	if got := identity.hitsOf("/api/v1/auth/refresh"); got != 1 {
		t.Errorf("el 401 debía disparar UN refresh contra identity, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/exchange"); got != 1 {
		t.Errorf("el refresh debía re-canjear una vez, got %d", got)
	}
}

// TestDelegacion409DelCanjeDegradaSinEcharAlUsuario: durante el refresco proactivo, la plataforma
// responde 409 —el usuario tiene más de un tenant, el estado que el diseño difiere al Plan 005—.
//
// El BFF NO echa al usuario: el Context Token que tiene en la mano sigue vigente, así que continúa
// con él y lo registra. Es la diferencia entre un 409 y un 401, y es la razón de que el cliente no los
// colapse: ante «tu sesión ya no vale» limpiar la cookie es lo correcto; ante «wApp no sabe en qué
// tenant ponerte» limpiarla no arregla nada, corta la sesión de quien estaba trabajando y borra la
// pista de por qué. La sesión se acabará cuando el token venza de verdad, no antes.
func TestDelegacion409DelCanjeDegradaSinEcharAlUsuario(t *testing.T) {
	identity := newUpstreamStub(t)
	identity.onJSON("/api/v1/auth/refresh",
		identityLoginBody(makeIdentityToken(t, time.Now().Add(time.Hour)), "rt-2"))

	platform := newUpstreamStub(t)
	platform.on("/api/v1/auth/exchange", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"el usuario pertenece a más de un tenant"}`)
	})
	platform.onJSON("/api/v1/sessions",
		`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","role":"bot"}]`)

	router := NewRouter(delegatedCfg(platform.url(), identity.url()))
	// Dentro del margen proactivo (vence en 1 min), pero AÚN VIGENTE: ahí está el matiz.
	porVencer := makeToken(t, time.Now().Add(time.Minute))
	rec := getWithCookie(router, "/", cookieWith(t, porVencer, "rt-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("con el Context Token aún vigente la página debía pintarse, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc == "/login" {
		t.Error("un 409 del canje NO debe echar al usuario al login")
	}
	if raw := sessionSetCookie(rec); strings.Contains(raw, "Max-Age=0") {
		t.Error("un 409 del canje NO debe destruir la sesión: el token en curso sigue valiendo")
	}
	// Se intentó el refresco y se canjeó una vez; el fallo fue del canje, no de identity.
	if got := identity.hitsOf("/api/v1/auth/refresh"); got != 1 {
		t.Errorf("debía intentarse el refresco contra identity una vez, got %d", got)
	}
	if got := platform.hitsOf("/api/v1/auth/exchange"); got != 1 {
		t.Errorf("debía intentarse el canje una vez, got %d", got)
	}
	// Y el negocio siguió con el token viejo, que es lo que significa degradar aquí.
	if got := platform.bearerOf("/api/v1/sessions"); got != "Bearer "+porVencer {
		t.Error("el negocio debía continuar con el Context Token aún vigente")
	}
	if !strings.Contains(rec.Body.String(), "edge-alpha") {
		t.Error("la consola debía seguir operando en modo degradado")
	}
}

// --- Logout modelo Google (T3.4) ---

// TestLogoutDelegadoRevocaSoloLaSesionDelBFF: el logout de la consola cierra en identity SOLO la
// sesión de esta aplicación, presentando su refresh. No hay revocación global —la sesión del Edge es
// otra sesión en identity y sobrevive— y la plataforma no recibe ningún cierre: ella no emitió nada
// que revocar.
func TestLogoutDelegadoRevocaSoloLaSesionDelBFF(t *testing.T) {
	identity := newUpstreamStub(t)
	identity.on("/api/v1/auth/logout", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	platform := newUpstreamStub(t)

	router := NewRouter(delegatedCfg(platform.url(), identity.url()))
	contextToken := makeToken(t, time.Now().Add(time.Hour))
	rec := postFormWithCookie(router, "/logout", url.Values{}, cookieWith(t, contextToken, "rt-bff"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("el logout debía redirigir 303, got %d", rec.Code)
	}
	if raw := sessionSetCookie(rec); raw == "" || !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("la cookie de sesión debía limpiarse, got %q", raw)
	}

	if got := identity.hitsOf("/api/v1/auth/logout"); got != 1 {
		t.Errorf("el logout debía revocar en identity una vez, got %d", got)
	}
	if body := identity.bodyOf("/api/v1/auth/logout"); !strings.Contains(body, `"refresh_token":"rt-bff"`) {
		t.Errorf("debía revocarse la sesión del refresh custodiado, got %q", body)
	}
	if got := identity.hitsOf("/api/v1/auth/logout-all"); got != 0 {
		t.Error("el logout de la consola NO debe revocar todas las sesiones: la del Edge sobrevive")
	}
	if got := identity.bearerOf("/api/v1/auth/logout"); got != "" {
		t.Errorf("el logout de identity no debe presentar el Context Token de wApp, got %q", got)
	}
	if got := platform.hitsOf("/api/v1/auth/logout"); got != 0 {
		t.Errorf("la plataforma no emitió la sesión y no tiene nada que revocar, got %d", got)
	}
}
