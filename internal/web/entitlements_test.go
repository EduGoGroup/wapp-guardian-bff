package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// gatedBlockMarker es el ancla del bloque gateado por `llm_intent` en dashboard.html. Si el bloque no
// se emite, esta cadena no aparece en el HTML: eso es exactamente lo que verifica el gate server-side.
const gatedBlockMarker = `id="section-llm-intent"`

// entitlementsBody arma la respuesta del endpoint con las features dadas.
func entitlementsBody(plan string, features ...string) string {
	quoted := make([]string, 0, len(features))
	for _, f := range features {
		quoted = append(quoted, `"`+f+`"`)
	}
	return `{"plan":"` + plan + `","features":[` + strings.Join(quoted, ",") +
		`],"cache_ttl_seconds":60}`
}

// TestDashboardRendersFeatureChips (T2.4): el dashboard pinta un chip por feature efectiva y el plan.
func TestDashboardRendersFeatureChips(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions":     {http.StatusOK, `[]`},
		"GET /api/v1/entitlements": {http.StatusOK, entitlementsBody("advisor_ai", "cart_basic", "llm_intent", "menu")},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `<span class="chip chip--info">plan · advisor_ai</span>`) {
		t.Error("el dashboard debía nombrar el plan del tenant")
	}
	for _, want := range []string{"cart_basic", "llm_intent", "menu"} {
		if !strings.Contains(out, `<span class="chip chip--ok">`+want+`</span>`) {
			t.Errorf("debía pintarse el chip de la feature %q", want)
		}
	}
	if strings.Count(out, `class="chip chip--ok"`) != 3 {
		t.Errorf("debía pintarse un chip por feature efectiva (3), got %d",
			strings.Count(out, `class="chip chip--ok"`))
	}
	// Sin JS nuevo: la página sigue sin <script> (la CSP endurecida no lo permitiría sin nonce).
	if strings.Contains(out, "<script") {
		t.Error("los chips no deben introducir JS")
	}
}

// TestDashboardGateEmitsSectionWithFeature (T2.5, mitad ON): con `llm_intent` efectiva, el bloque
// gateado SÍ está en el HTML.
func TestDashboardGateEmitsSectionWithFeature(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions":     {http.StatusOK, `[]`},
		"GET /api/v1/entitlements": {http.StatusOK, entitlementsBody("advisor_ai", "llm_intent", "menu")},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if !strings.Contains(rec.Body.String(), gatedBlockMarker) {
		t.Error("con llm_intent efectiva, el bloque gateado debía emitirse")
	}
}

// TestDashboardGateOmitsSectionWithoutFeature (T2.5, mitad OFF): sin `llm_intent`, el bloque NO llega
// al HTML —no basta con ocultarlo—, mientras el resto del dashboard sigue intacto.
func TestDashboardGateOmitsSectionWithoutFeature(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions":     {http.StatusOK, `[]`},
		"GET /api/v1/entitlements": {http.StatusOK, entitlementsBody("basic", "cart_basic", "menu")},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard debía renderizar 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if strings.Contains(out, gatedBlockMarker) {
		t.Error("sin llm_intent, el bloque gateado NO debía emitirse")
	}
	if strings.Contains(out, "Clasificador de intenciones") {
		t.Error("sin la feature no debe quedar rastro del bloque en el HTML")
	}
	// El gate no se lleva por delante lo que no depende de features.
	if !strings.Contains(out, "Enviar un mensaje") {
		t.Error("las secciones base debían seguir emitiéndose")
	}
}

// TestDashboardDegradesWhenEntitlementsFail (T2.4 degradado + T2.5 fail-closed): si el endpoint falla,
// el dashboard sigue sirviendo 200 con las sesiones, avisa del plan no consultado y CIERRA el gate.
func TestDashboardDegradesWhenEntitlementsFail(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions": {http.StatusOK,
			`[{"session_id":"s-1","edge_id":"edge-alpha","state":"online","profile":"active"}]`},
		"GET /api/v1/entitlements": {http.StatusInternalServerError, `{"error":"detalle interno que no debe verse"}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("el dashboard degradado debía seguir sirviendo 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "No se pudo consultar el plan del tenant") {
		t.Error("el modo degradado debía avisar de que el plan no se pudo consultar")
	}
	if strings.Contains(out, gatedBlockMarker) {
		t.Error("fail-closed: sin features resueltas, ningún bloque gateado debe emitirse")
	}
	if !strings.Contains(out, "edge-alpha") {
		t.Error("el listado de sesiones no debía verse afectado por el fallo del plan")
	}
	if strings.Contains(out, "detalle interno que no debe verse") {
		t.Error("no debe filtrarse el detalle del upstream")
	}
}

// TestDashboardEntitlementsForbidden: un 403 (token válido sin el scope entitlements.read) es un caso
// esperado, no un error de plataforma: avisa con su propio mensaje y mantiene el gate cerrado.
func TestDashboardEntitlementsForbidden(t *testing.T) {
	api := routedAPI(map[string]struct {
		status int
		body   string
	}{
		"GET /api/v1/sessions":     {http.StatusOK, `[]`},
		"GET /api/v1/entitlements": {http.StatusForbidden, `{"error":"forbidden"}`},
	})
	defer api.Close()

	router := NewRouter(authTestCfg(api.URL))
	rec := getWithCookie(router, "/", validSessionCookie(t))

	if rec.Code != http.StatusOK {
		t.Fatalf("un 403 del plan no debía tumbar el dashboard, got %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "no tiene permiso para consultar el plan") {
		t.Error("el 403 debía traducirse a un aviso de permisos")
	}
	if strings.Contains(out, gatedBlockMarker) {
		t.Error("fail-closed: con 403 no se emite ningún bloque gateado")
	}
}

// TestResolveEntitlementsViaFakePort ejercita el seam del puerto SIN HTTP: el doble devuelve las
// features y la vista las indexa; el doble que falla devuelve la vista cero (Has siempre false).
func TestResolveEntitlementsViaFakePort(t *testing.T) {
	ok := &fakeAPIPort{
		getEntitlements: func(context.Context, string) (*apiclient.Entitlements, error) {
			return &apiclient.Entitlements{
				Plan: "pro", Features: []string{"cart_basic", "llm_intent"}, CacheTTLSeconds: 60,
			}, nil
		},
	}
	view := resolveViaPort(t, ok)
	if !view.Resolved || view.Plan != "pro" {
		t.Fatalf("la vista debía resolverse con el plan del doble, got %+v", view)
	}
	if !view.Has("llm_intent") || view.Has("intakes_export") {
		t.Error("Has debía responder por pertenencia a las features efectivas")
	}

	failing := &fakeAPIPort{
		getEntitlements: func(context.Context, string) (*apiclient.Entitlements, error) {
			return nil, &apiclient.APIError{Op: "entitlements", StatusCode: http.StatusBadGateway}
		},
	}
	degraded := resolveViaPort(t, failing)
	if degraded.Resolved || degraded.Notice == "" {
		t.Fatalf("un fallo debía dar vista degradada con aviso, got %+v", degraded)
	}
	// La vista cero es la que hace fail-closed el gate de la plantilla.
	if degraded.Has("llm_intent") || degraded.Has("cualquier_cosa") {
		t.Error("la vista no resuelta no debe habilitar ninguna feature")
	}
}

// resolveViaPort corre resolveEntitlements contra un doble del puerto, sin transporte HTTP.
func resolveViaPort(t *testing.T, api APIPort) entitlementsView {
	t.Helper()
	h := NewHandlerWithAPI(authTestCfg("http://api.invalid"), api)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(webgin.ContextAccessToken, "tok")
	return resolveEntitlements(c, h.AuthHandler, h.api)
}
