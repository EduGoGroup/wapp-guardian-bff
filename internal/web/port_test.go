package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// fakeAPIPort es un doble en memoria de APIPort para tests que no necesitan HTTP real. Cada método delega
// en un campo función si está puesto; si no, devuelve el cero correspondiente. Verifica en compilación que
// cumple el puerto.
type fakeAPIPort struct {
	refresh         func(ctx context.Context, refreshToken string) (*apiclient.AuthResult, error)
	getEntitlements func(ctx context.Context, accessToken string) (*apiclient.Entitlements, error)
	listIntakes     func(ctx context.Context, accessToken string, f apiclient.IntakeFilter) (*apiclient.IntakePage, error)
	getIntake       func(ctx context.Context, accessToken, id string) (*apiclient.IntakeDetail, error)
	setIntakeStatus func(ctx context.Context, accessToken, id, status string) (*apiclient.Intake, error)
	replaceItems    func(ctx context.Context, accessToken, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error)
	correctItems    func(ctx context.Context, accessToken, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error)
	approveIntake   func(ctx context.Context, accessToken, id, renderedText string) (*apiclient.IntakeDetail, error)
	requestInfo     func(ctx context.Context, accessToken, id, question string) (*apiclient.IntakeDetail, error)
	reanalyze       func(ctx context.Context, accessToken, id, text string) (*apiclient.IntakeReanalysis, error)
	suggestQuote    func(ctx context.Context, accessToken, id string) (*apiclient.IntakeQuoteSuggestion, error)
	discardIntakes  func(ctx context.Context, accessToken string, intakeIDs []string) (*apiclient.IntakeDiscardResult, error)

	getTenantVariables     func(ctx context.Context, accessToken string) (*apiclient.TenantVariables, error)
	replaceTenantVariables func(ctx context.Context, accessToken string, vars map[string]string) (*apiclient.TenantVariables, error)

	importCatalog         func(ctx context.Context, accessToken string, document []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error)
	importCatalogTabular  func(ctx context.Context, accessToken, filename string, content []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error)
	getCatalogTemplate    func(ctx context.Context, accessToken, format string) (*apiclient.CatalogTemplate, error)
	getCatalogPrompt      func(ctx context.Context, accessToken string) (*apiclient.CatalogPrompt, error)
	listTenantContentRefs func(ctx context.Context, accessToken string) ([]apiclient.TenantContentRef, error)

	getIntegration     func(ctx context.Context, accessToken string) (*apiclient.Integration, error)
	saveIntegration    func(ctx context.Context, accessToken string, s apiclient.IntegrationSettings) (*apiclient.Integration, error)
	deleteIntegration  func(ctx context.Context, accessToken string) error
	getOutboxCounters  func(ctx context.Context, accessToken string) (*apiclient.OutboxCounters, error)
	outboxCounterCalls int

	getTenantLLM    func(ctx context.Context, accessToken string) (*apiclient.TenantLLM, error)
	saveTenantLLM   func(ctx context.Context, accessToken string, s apiclient.TenantLLMSettings) (*apiclient.TenantLLM, error)
	deleteTenantLLM func(ctx context.Context, accessToken string) error
}

var _ APIPort = (*fakeAPIPort)(nil)

func (f *fakeAPIPort) Login(context.Context, string, string) (*apiclient.AuthResult, error) {
	return nil, nil
}
func (f *fakeAPIPort) Refresh(ctx context.Context, rt string) (*apiclient.AuthResult, error) {
	if f.refresh != nil {
		return f.refresh(ctx, rt)
	}
	return nil, nil
}
func (f *fakeAPIPort) Logout(context.Context, string, string) error { return nil }
func (f *fakeAPIPort) Signup(context.Context, string, string, string, string, string) error {
	return nil
}
func (f *fakeAPIPort) GetEntitlements(ctx context.Context, at string) (*apiclient.Entitlements, error) {
	if f.getEntitlements != nil {
		return f.getEntitlements(ctx, at)
	}
	return nil, nil
}
func (f *fakeAPIPort) ListIntakes(ctx context.Context, at string, filter apiclient.IntakeFilter) (*apiclient.IntakePage, error) {
	if f.listIntakes != nil {
		return f.listIntakes(ctx, at, filter)
	}
	return &apiclient.IntakePage{Intakes: []apiclient.Intake{}, Page: 1, PageSize: 50}, nil
}
func (f *fakeAPIPort) GetIntake(ctx context.Context, at, id string) (*apiclient.IntakeDetail, error) {
	if f.getIntake != nil {
		return f.getIntake(ctx, at, id)
	}
	return &apiclient.IntakeDetail{}, nil
}
func (f *fakeAPIPort) SetIntakeStatus(ctx context.Context, at, id, status string) (*apiclient.Intake, error) {
	if f.setIntakeStatus != nil {
		return f.setIntakeStatus(ctx, at, id, status)
	}
	return &apiclient.Intake{}, nil
}
func (f *fakeAPIPort) ReplaceIntakeItems(ctx context.Context, at, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error) {
	if f.replaceItems != nil {
		return f.replaceItems(ctx, at, id, items)
	}
	return &apiclient.IntakeDetail{}, nil
}
func (f *fakeAPIPort) CorrectIntakeItems(ctx context.Context, at, id string, items []apiclient.IntakeItem) (*apiclient.IntakeDetail, error) {
	if f.correctItems != nil {
		return f.correctItems(ctx, at, id, items)
	}
	return &apiclient.IntakeDetail{}, nil
}
func (f *fakeAPIPort) ApproveIntake(ctx context.Context, at, id, renderedText string) (*apiclient.IntakeDetail, error) {
	if f.approveIntake != nil {
		return f.approveIntake(ctx, at, id, renderedText)
	}
	return &apiclient.IntakeDetail{}, nil
}
func (f *fakeAPIPort) RequestIntakeInfo(ctx context.Context, at, id, question string) (*apiclient.IntakeDetail, error) {
	if f.requestInfo != nil {
		return f.requestInfo(ctx, at, id, question)
	}
	return &apiclient.IntakeDetail{}, nil
}
func (f *fakeAPIPort) ReanalyzeIntake(ctx context.Context, at, id, text string) (*apiclient.IntakeReanalysis, error) {
	if f.reanalyze != nil {
		return f.reanalyze(ctx, at, id, text)
	}
	return &apiclient.IntakeReanalysis{}, nil
}
func (f *fakeAPIPort) SuggestIntakeQuote(ctx context.Context, at, id string) (*apiclient.IntakeQuoteSuggestion, error) {
	if f.suggestQuote != nil {
		return f.suggestQuote(ctx, at, id)
	}
	return &apiclient.IntakeQuoteSuggestion{}, nil
}
func (f *fakeAPIPort) DiscardIntakes(ctx context.Context, at string, ids []string) (*apiclient.IntakeDiscardResult, error) {
	if f.discardIntakes != nil {
		return f.discardIntakes(ctx, at, ids)
	}
	// Las dos listas van vacías y NO nil, como las emite la plataforma: un doble que devolviera
	// `null` dejaría pasar código que confunde «no se descartó nada» con «no hubo respuesta».
	return &apiclient.IntakeDiscardResult{Discarded: []string{}, Skipped: []apiclient.IntakeDiscardSkip{}}, nil
}
func (f *fakeAPIPort) GetTenantVariables(ctx context.Context, at string) (*apiclient.TenantVariables, error) {
	if f.getTenantVariables != nil {
		return f.getTenantVariables(ctx, at)
	}
	return &apiclient.TenantVariables{Variables: map[string]string{}}, nil
}
func (f *fakeAPIPort) ReplaceTenantVariables(ctx context.Context, at string, vars map[string]string) (*apiclient.TenantVariables, error) {
	if f.replaceTenantVariables != nil {
		return f.replaceTenantVariables(ctx, at, vars)
	}
	return &apiclient.TenantVariables{Variables: vars}, nil
}
func (f *fakeAPIPort) ImportCatalog(ctx context.Context, at string, document []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error) {
	if f.importCatalog != nil {
		return f.importCatalog(ctx, at, document, apply, ref)
	}
	return &apiclient.CatalogImportResult{}, nil
}
func (f *fakeAPIPort) ImportCatalogTabular(ctx context.Context, at, filename string, content []byte, apply bool, ref string) (*apiclient.CatalogImportResult, error) {
	if f.importCatalogTabular != nil {
		return f.importCatalogTabular(ctx, at, filename, content, apply, ref)
	}
	return &apiclient.CatalogImportResult{}, nil
}
func (f *fakeAPIPort) GetCatalogTemplate(ctx context.Context, at, format string) (*apiclient.CatalogTemplate, error) {
	if f.getCatalogTemplate != nil {
		return f.getCatalogTemplate(ctx, at, format)
	}
	return &apiclient.CatalogTemplate{}, nil
}
func (f *fakeAPIPort) GetCatalogPrompt(ctx context.Context, at string) (*apiclient.CatalogPrompt, error) {
	if f.getCatalogPrompt != nil {
		return f.getCatalogPrompt(ctx, at)
	}
	return &apiclient.CatalogPrompt{}, nil
}
func (f *fakeAPIPort) ListTenantContentRefs(ctx context.Context, at string) ([]apiclient.TenantContentRef, error) {
	if f.listTenantContentRefs != nil {
		return f.listTenantContentRefs(ctx, at)
	}
	return nil, nil
}
func (f *fakeAPIPort) GetIntegration(ctx context.Context, at string) (*apiclient.Integration, error) {
	if f.getIntegration != nil {
		return f.getIntegration(ctx, at)
	}
	// El default es el de la plataforma para un tenant sin fila: local/local apagado.
	return &apiclient.Integration{CatalogAdapter: "local", EventsAdapter: "local"}, nil
}
func (f *fakeAPIPort) SaveIntegration(ctx context.Context, at string, s apiclient.IntegrationSettings) (*apiclient.Integration, error) {
	if f.saveIntegration != nil {
		return f.saveIntegration(ctx, at, s)
	}
	// El doble devuelve la foto guardada SIN el secreto, igual que la API: no hay campo donde ponerlo.
	return &apiclient.Integration{
		Configured:     true,
		CatalogAdapter: s.CatalogAdapter,
		EventsAdapter:  s.EventsAdapter,
		EndpointURL:    s.EndpointURL,
		Enabled:        s.Enabled,
		SecretSet:      s.Secret != "",
	}, nil
}
func (f *fakeAPIPort) DeleteIntegration(ctx context.Context, at string) error {
	if f.deleteIntegration != nil {
		return f.deleteIntegration(ctx, at)
	}
	return nil
}
func (f *fakeAPIPort) GetOutboxCounters(ctx context.Context, at string) (*apiclient.OutboxCounters, error) {
	f.outboxCounterCalls++
	if f.getOutboxCounters != nil {
		return f.getOutboxCounters(ctx, at)
	}
	// El default es una cola vacía: 200 con todo a cero, que es lo que responde la plataforma para un
	// tenant que nunca encoló nada. NO es un error, y por eso el doble tampoco lo trata como tal.
	return &apiclient.OutboxCounters{}, nil
}

func (f *fakeAPIPort) GetTenantLLM(ctx context.Context, at string) (*apiclient.TenantLLM, error) {
	if f.getTenantLLM != nil {
		return f.getTenantLLM(ctx, at)
	}
	// El default es el de la plataforma para un tenant sin fila: la vía local, que NO es «ninguna vía»
	// sino el default del producto, y sin credencial.
	return &apiclient.TenantLLM{Via: "local"}, nil
}
func (f *fakeAPIPort) SaveTenantLLM(ctx context.Context, at string, s apiclient.TenantLLMSettings) (*apiclient.TenantLLM, error) {
	if f.saveTenantLLM != nil {
		return f.saveTenantLLM(ctx, at, s)
	}
	// El doble devuelve la foto guardada SIN la credencial, igual que la API: no hay campo donde
	// ponerla, solo el booleano que dice que la hay.
	return &apiclient.TenantLLM{
		Configured: true,
		Via:        s.Via,
		Provider:   s.Provider,
		Model:      s.Model,
		KeySet:     s.APIKey != "",
	}, nil
}
func (f *fakeAPIPort) DeleteTenantLLM(ctx context.Context, at string) error {
	if f.deleteTenantLLM != nil {
		return f.deleteTenantLLM(ctx, at)
	}
	return nil
}

// TestWithAuthRetryRefreshesOn401 ejercita el seam del puerto SIN HTTP: la primera llamada de negocio
// devuelve 401, withAuthRetry refresca (vía el doble) y reintenta con el token nuevo, que ya pasa.
func TestWithAuthRetryRefreshesOn401(t *testing.T) {
	newAccess := makeToken(t, time.Now().Add(time.Hour))
	fake := &fakeAPIPort{
		refresh: func(_ context.Context, _ string) (*apiclient.AuthResult, error) {
			return &apiclient.AuthResult{AccessToken: newAccess, RefreshToken: "r-new"}, nil
		},
	}
	h := NewHandlerWithAPI(authTestCfg("http://api.invalid"), fake)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(webgin.ContextAccessToken, "token-viejo")
	c.Set(webgin.ContextRefreshToken, "r-old")

	calls := 0
	var seen []string
	err := h.withAuthRetry(c, func(token string) error {
		calls++
		seen = append(seen, token)
		if token == "token-viejo" {
			return apiclient.ErrUnauthorized
		}
		return nil
	})

	if err != nil {
		t.Fatalf("tras refrescar, el reintento debía tener éxito; got %v", err)
	}
	if calls != 2 {
		t.Fatalf("la función debía llamarse 2 veces (intento + reintento), got %d", calls)
	}
	if seen[1] != newAccess {
		t.Errorf("el reintento debía usar el token refrescado, got %q", seen[1])
	}
}
