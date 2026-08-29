package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// La ruta HERMANA con la que se contrasta en todo este fichero. Se eligió `/reanalyze` y no una
// pantalla cualquiera porque es la más parecida de todo el BFF a la sugerencia —misma bandeja, mismo
// gate `llm_intake`, mismo POST sin cuerpo útil, y también acaba llamando a un modelo en el cloud—:
// si el plazo largo se escapara a alguna ruta por parecido, se escaparía primero a ésta.
const rutaHermana = "/intakes/in-ambar/reanalyze"

// plazoDeCadaRuta observa, DESDE EL UPSTREAM, cuánto tiempo aguantó viva cada llamada del BFF antes
// de que la abortaran.
//
// Es la medición directa del plazo efectivo, y por eso no se afirma sobre la pantalla: el cliente
// HTTP del BFF cierra la conexión al vencer su plazo (el suyo propio o el del contexto de la
// petición), y el servidor lo ve en r.Context(). Lo que este fake devuelve es, para cada ruta, el
// plazo real que el BFF le concedió.
type plazoDeCadaRuta struct {
	mu      sync.Mutex
	aguantó map[string]time.Duration
}

func (p *plazoDeCadaRuta) anota(ruta string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ya := p.aguantó[ruta]; !ya {
		p.aguantó[ruta] = d
	}
}

// de espera a que el upstream haya anotado la medida de esa ruta y la devuelve; 0 si no llegó a
// anotarla en el tope.
//
// La espera NO es decorado: al vencer el plazo, el cliente del BFF cierra la conexión y el test
// recupera el control ANTES de que la goroutine del servidor haya despertado de su select y
// apuntado el número. Sin ella, la medida se leía en cero la mitad de las veces y el test acusaba
// de «no se midió» a un corte que sí había ocurrido.
func (p *plazoDeCadaRuta) de(ruta string) time.Duration {
	límite := time.Now().Add(2 * time.Second)
	for {
		p.mu.Lock()
		d, ok := p.aguantó[ruta]
		p.mu.Unlock()
		if ok || time.Now().After(límite) {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// upstreamQueNuncaContesta sirve las capacidades al instante (para que la pantalla llegue a la
// llamada de negocio) y luego se queda callado en los POST hasta que lo aborten, anotando cuánto
// tardaron en abortarlo. El techo evita que un plazo mal puesto cuelgue el test para siempre: si se
// alcanza, la medida sale igual al techo y el aserto del corte falla, que es lo que debe pasar.
func upstreamQueNuncaContesta(techo time.Duration) (*httptest.Server, *plazoDeCadaRuta) {
	obs := &plazoDeCadaRuta{aguantó: map[string]time.Duration{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("commerce", "cart_basic", "llm_intake"))
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		// 🔴 DRENAR EL CUERPO ES OBLIGATORIO, y costó un test en falso: mientras al net/http del
		// servidor le queda cuerpo por leer NO hace la lectura en segundo plano con la que detecta
		// que el cliente cerró, así que r.Context() no se cancela nunca y la llamada parece eterna.
		// Se notaba justo en la ruta hermana (`/reanalyze` manda JSON) y no en la sugerencia, que
		// viaja sin cuerpo — es decir, el fake medía bien la ruta que no hacía falta vigilar.
		_, _ = io.Copy(io.Discard, r.Body)

		inicio := time.Now()
		select {
		case <-r.Context().Done():
		case <-time.After(techo):
		}
		obs.anota(r.URL.Path, time.Since(inicio))
	}))
	return srv, obs
}

// TestElPlazoLargoEsSoloDeLaRutaDeLaSugerencia es la mitad que de verdad importa de esta tarea: que
// la sugerencia se lleva el plazo largo y que NINGUNA otra ruta se lo lleva con ella.
//
// Se mide el plazo EFECTIVO que el BFF le concede a cada llamada, no lo que se pinta: un aserto
// sobre la pantalla podría salir verde por cualquier otra razón (un gate, un 500 del fake, una
// plantilla), y lo que hay que demostrar aquí es un número de segundos.
//
// Los plazos van escalados —milisegundos en vez de decenas de segundos— porque lo que se prueba es
// la REGLA (qué ruta se lleva cuál), no los valores de producción, que se comprueban en
// config_test.go. Lo que se conserva del montaje real es el orden: corto para el grupo, largo para
// la sugerencia.
func TestElPlazoLargoEsSoloDeLaRutaDeLaSugerencia(t *testing.T) {
	api, obs := upstreamQueNuncaContesta(5 * time.Second)
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 150 * time.Millisecond         // el plazo corto: el de TODAS las demás rutas.
	cfg.QuoteSuggestionTimeout = 1200 * time.Millisecond // el largo, solo de la sugerencia.
	router := NewRouter(cfg)

	postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion", url.Values{}, validSessionCookie(t))
	postFormWithCookie(router, rutaHermana, url.Values{}, validSessionCookie(t))

	sugerencia := obs.de("/api/v1/intakes/in-ambar/quote-suggestion")
	hermana := obs.de("/api/v1/intakes/in-ambar/reanalyze")

	if sugerencia < 800*time.Millisecond {
		t.Errorf("la sugerencia debía disponer del plazo LARGO (1,2s): la abortaron a los %s", sugerencia)
	}
	// Y el techo: si nadie la aborta nunca, la medida sale igual al techo del fake y eso tampoco es
	// «plazo largo», es «sin plazo».
	if sugerencia >= 5*time.Second {
		t.Errorf("la sugerencia debía tener un plazo, no ser eterna: aguantó %s (el techo del fake)", sugerencia)
	}
	if hermana > 600*time.Millisecond {
		t.Errorf("NINGUNA otra ruta puede llevarse el plazo largo, y a `/reanalyze` le duró %s "+
			"(debía cortarse con el plazo corto del grupo, 150ms)", hermana)
	}
	if hermana == 0 {
		t.Error("el upstream no llegó a ver la llamada de `/reanalyze`: el contraste no se midió")
	}
}

// TestSinPlazoPropioLaSugerenciaVuelveAlDelGrupo cubre el apagado (QuoteSuggestionTimeout = 0), que
// es como corren todos los demás tests de este paquete: un cero significa «sin plazo propio», nunca
// «sin plazo». Sin este caso, un bug que dejara la ruta SIN deadline pasaría por «apagado».
func TestSinPlazoPropioLaSugerenciaVuelveAlDelGrupo(t *testing.T) {
	api, obs := upstreamQueNuncaContesta(3 * time.Second)
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 150 * time.Millisecond
	cfg.QuoteSuggestionTimeout = 0 // apagado
	router := NewRouter(cfg)

	postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion", url.Values{}, validSessionCookie(t))

	sugerencia := obs.de("/api/v1/intakes/in-ambar/quote-suggestion")
	if sugerencia == 0 {
		t.Fatal("el upstream no llegó a ver la llamada: no se midió nada")
	}
	if sugerencia > 600*time.Millisecond {
		t.Errorf("con el plazo propio apagado la sugerencia debía caer al del grupo (150ms), "+
			"y aguantó %s: quedó sin deadline", sugerencia)
	}
}

// upstreamLento contesta bien, pero tarda: es el cloud redactando. Sirve para el test del write
// deadline, donde lo que hay que provocar es que el servidor corte la conexión a mitad de la espera.
func upstreamLento(t *testing.T, tardanza time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/entitlements" {
			_, _ = io.WriteString(w, entitlementsBody("commerce", "cart_basic", "llm_intake"))
			return
		}
		time.Sleep(tardanza)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, ambarDetail(false))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/quote-suggestion") {
			_, _ = io.WriteString(w,
				`{"rendered_text":"Hola Ambar 💛 Torta de chocolate 45.00 — Total 65.50","source":"llm"}`)
			return
		}
		_, _ = io.WriteString(w, ambarDetail(false))
	}))
}

// postReal manda el POST por un servidor HTTP DE VERDAD (no un httptest.ResponseRecorder) y devuelve
// la respuesta o el error de transporte.
//
// Tiene que ser un servidor real: el write deadline se instala sobre la CONEXIÓN, y en un recorder
// no hay conexión donde instalarlo. Un test con recorder no puede distinguir «el write deadline
// funciona» de «el write deadline no existe», que es justo lo que hay que distinguir aquí.
func postReal(t *testing.T, ts *httptest.Server, router http.Handler, ruta string) (*http.Response, string, error) {
	t.Helper()
	csrf := mintCSRF(router)
	form := url.Values{}
	form.Set(sharedweb.CSRFFieldName, csrf.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+ruta, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("no se pudo construir la petición: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrf)
	req.AddCookie(validSessionCookie(t))

	// Cliente sin plazo propio y sin reutilizar conexiones: el único corte que puede aparecer aquí
	// es el del servidor, que es lo que se está midiendo.
	//
	// 🔴 Y NO SIGUE REDIRECTS (T3.5). Desde que la sugerencia hace POST-Redirect-GET, seguirlos
	// mediría DOS peticiones —la que espera al modelo y la que repinta— bajo un solo aserto, y la
	// segunda corre con los plazos GENERALES a propósito. Lo que este fichero mide es el plazo de la
	// PRIMERA. Para /reanalyze, que sigue pintando sobre el POST, esto no cambia nada.
	cli := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	cuerpo, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, "", err
	}
	return resp, string(cuerpo), nil
}

// TestLaRespuestaLentaDeLaSugerenciaLlegaEntera es el criterio del TERCER plazo, el que falla sin
// dejar rastro: sin un write deadline propio, el WriteTimeout del http.Server cierra la conexión a
// mitad de la espera y el navegador no recibe ni la página degradada.
//
// El montaje es el de producción en pequeño: un servidor real con WriteTimeout corto, un upstream
// que tarda MÁS que ese WriteTimeout, y los dos plazos de petición holgados para que el único que
// pueda cortar sea el del servidor. La sugerencia tiene que llegar entera; la ruta hermana, no.
func TestLaRespuestaLentaDeLaSugerenciaLlegaEntera(t *testing.T) {
	api := upstreamLento(t, 700*time.Millisecond)
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 5 * time.Second        // holgado: no es lo que se está midiendo.
	cfg.QuoteSuggestionTimeout = 5 * time.Second // → write deadline propio de 10s.
	router := NewRouter(cfg)

	ts := httptest.NewUnstartedServer(router)
	ts.Config.WriteTimeout = 300 * time.Millisecond // el corte del servidor, muy por debajo de la espera.
	ts.Start()
	defer ts.Close()

	resp, _, err := postReal(t, ts, router, "/intakes/in-ambar/quote-suggestion")
	if err != nil {
		t.Fatalf("la respuesta lenta de la sugerencia debía llegar y la conexión se cortó: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("la sugerencia debía responder 303, got %d", resp.StatusCode)
	}
	// 🔄 GIRADO POR T3.5, y el aserto NO se debilita: se muda al artefacto que ahora lleva el
	// resultado. Antes se comprobaba que el texto venía en el cuerpo; hoy el cuerpo del 303 está
	// vacío por diseño y el texto viaja en la cookie efímera. Que esa cookie llegue prueba
	// exactamente lo mismo de antes —la inferencia lenta terminó Y su resultado salió por la
	// conexión antes de que el WriteTimeout de 300 ms la cerrara—, que es el tercer plazo.
	llevaElTexto := false
	for _, c := range resp.Cookies() {
		if c.Name == quoteCookieName && c.Value != "" {
			llevaElTexto = true
		}
	}
	if !llevaElTexto {
		t.Error("la respuesta llegó, pero sin la cotización: el resultado de la espera larga no salió")
	}

	// Y el contraste: la ruta hermana, con la MISMA espera y el mismo servidor, sí la corta el
	// WriteTimeout. El aserto es sobre el ERROR DE TRANSPORTE y no sobre un status: un 500 llegaría
	// como respuesta válida, y eso sería otra cosa —querría decir que la conexión aguantó—.
	if _, _, err := postReal(t, ts, router, rutaHermana); err == nil {
		t.Error("el write deadline largo se le dio también a `/reanalyze`: su respuesta lenta debía " +
			"morir con el WriteTimeout del servidor (300ms) y llegó entera")
	}
}

// TestConWriteTimeoutHolgadoLaRutaHermanaSiResponde es el control que impide que el test de arriba
// sea tautológico: sin él, «/reanalyze falla» podría estar pasando por cualquier motivo (un gate, un
// 500 del fake, una plantilla rota) y el test seguiría verde sin haber medido ningún plazo.
//
// Mismo montaje, misma ruta, mismo upstream lento — y lo ÚNICO que cambia es el WriteTimeout del
// servidor. Si aquí responde, lo que la mataba allí era el plazo y nada más.
func TestConWriteTimeoutHolgadoLaRutaHermanaSiResponde(t *testing.T) {
	api := upstreamLento(t, 700*time.Millisecond)
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 5 * time.Second
	cfg.QuoteSuggestionTimeout = 5 * time.Second
	router := NewRouter(cfg)

	ts := httptest.NewUnstartedServer(router)
	ts.Config.WriteTimeout = 5 * time.Second // holgado, al revés que en el test de arriba.
	ts.Start()
	defer ts.Close()

	resp, _, err := postReal(t, ts, router, rutaHermana)
	if err != nil {
		t.Fatalf("con el WriteTimeout holgado `/reanalyze` debía responder: %v", err)
	}
	if resp.StatusCode >= 500 {
		t.Fatalf("`/reanalyze` respondió %d: el contraste del test hermano no mide un plazo, "+
			"mide un fallo del montaje", resp.StatusCode)
	}
}

// TestElDespachadorDeDeadlinesReconoceLaRuta vigila la costura más silenciosa del arreglo: el
// despachador compara c.FullPath() con una constante, así que el día que la ruta cambie de forma
// —otro prefijo, otro nombre de parámetro— dejaría de reconocerla SIN QUE NADA FALLE y la pantalla
// volvería a morir a los 20s. Aquí se comprueba que la constante es la ruta que el router registra
// de verdad.
func TestElDespachadorDeDeadlinesReconoceLaRuta(t *testing.T) {
	cfg := authTestCfg("http://127.0.0.1:1")
	var registrada bool
	for _, r := range NewRouter(cfg).Routes() {
		if r.Method == http.MethodPost && r.Path == quoteSuggestionRoute {
			registrada = true
		}
	}
	if !registrada {
		t.Fatalf("el router no registra %q: el despachador de plazos no la reconocerá nunca "+
			"y la sugerencia se quedará con el plazo corto", quoteSuggestionRoute)
	}
}

// TestLosPlazosDeLaSugerenciaVanEnOrden protege la única propiedad de la que depende que el corte,
// cuando llegue, se pueda pintar: cliente < deadline de petición < write deadline. Si el orden se
// invirtiera, cortaría el servidor —cerrando la conexión sin nada que enseñar— antes que el cliente
// HTTP, que sí devuelve un error traducible a un aviso.
func TestLosPlazosDeLaSugerenciaVanEnOrden(t *testing.T) {
	cfg := &config.Config{QuoteSuggestionTimeout: 55 * time.Second}

	cliente := cfg.QuoteSuggestionTimeout
	peticion := cfg.QuoteSuggestionRequestDeadline()
	escritura := cfg.QuoteSuggestionWriteDeadline()

	if cliente >= peticion {
		t.Errorf("el cliente (%s) debe cortar antes que el deadline de petición (%s)", cliente, peticion)
	}
	if peticion >= escritura {
		t.Errorf("el deadline de petición (%s) debe vencer antes que el write deadline (%s)",
			peticion, escritura)
	}
}

// registroCapturado retiene los mensajes de slog emitidos durante un test, para poder afirmar sobre
// lo que el middleware DIJO además de sobre lo que la pantalla hizo.
type registroCapturado struct {
	mu       sync.Mutex
	mensajes []string
}

func (r *registroCapturado) Enabled(context.Context, slog.Level) bool { return true }

func (r *registroCapturado) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mensajes = append(r.mensajes, rec.Message)
	return nil
}

func (r *registroCapturado) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *registroCapturado) WithGroup(string) slog.Handler      { return r }

func (r *registroCapturado) contiene(fragmento string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.mensajes {
		if strings.Contains(m, fragmento) {
			return true
		}
	}
	return false
}

// capturaElLog sustituye el logger por defecto mientras dura el test y lo repone al salir.
func capturaElLog(t *testing.T) *registroCapturado {
	t.Helper()
	captura := &registroCapturado{}
	anterior := slog.Default()
	slog.SetDefault(slog.New(captura))
	t.Cleanup(func() { slog.SetDefault(anterior) })
	return captura
}

// El fragmento del aviso que el middleware emite cuando NO pudo instalar el deadline (los dos
// caminos, el esperable y el que no lo es, empiezan igual).
const avisoSinWriteDeadline = "write deadline de la sugerencia"

// TestElWriteDeadlineLlegaDeVerdadALaConexionBajoGin responde a una pregunta que un test de conducta
// contesta solo por implicación: ¿http.NewResponseController atraviesa el envoltorio de Gin y llega
// a la conexión, o devuelve «feature not supported» y el arreglo no hace nada?
//
// La pregunta no es teórica: en el frente hermano del cloud el mismo mecanismo NO funcionaba porque
// un envoltorio intermedio no exponía Unwrap(), y ahí el middleware se quedaba en un no-op silencioso.
// Aquí se mide, no se supone — y se mide por lo que el middleware DICE: solo registra algo cuando
// SetWriteDeadline le devuelve error.
//
// Las dos mitades son necesarias. Sin la del recorder, «no se registró nada» podría significar que
// el capturador no funciona; sin la del servidor real, no habría medición ninguna, porque contra un
// httptest.ResponseRecorder los dos desenlaces —«llegó» y «no está soportado»— son indistinguibles.
func TestElWriteDeadlineLlegaDeVerdadALaConexionBajoGin(t *testing.T) {
	api := upstreamLento(t, 10*time.Millisecond)
	defer api.Close()

	cfg := authTestCfg(api.URL)
	cfg.UpstreamTimeout = 5 * time.Second
	cfg.QuoteSuggestionTimeout = 5 * time.Second
	router := NewRouter(cfg)

	// (1) CONTRA UN RECORDER: no hay conexión, así que el controller no puede instalar nada y el
	// middleware lo dice. Esto acredita que el capturador ve los avisos.
	sinConexion := capturaElLog(t)
	rec := postFormWithCookie(router, "/intakes/in-ambar/quote-suggestion", url.Values{}, validSessionCookie(t))
	if rec.Code != http.StatusSeeOther {
		// 303 desde T3.5: la ruta redirige. Lo que este test mide es lo que DICE el middleware del
		// write deadline, no el código de estado; el status se comprueba solo para saber que la
		// petición recorrió la ruta entera y no murió antes de llegar al middleware.
		t.Fatalf("la sugerencia debía responder 303 también con recorder, got %d", rec.Code)
	}
	if !sinConexion.contiene(avisoSinWriteDeadline) {
		t.Fatal("sobre un ResponseRecorder el middleware debía avisar de que no hay conexión donde " +
			"instalar el deadline; si no lo dice, este test no está capturando nada y su otra mitad " +
			"no demuestra nada")
	}

	// (2) CONTRA UN SERVIDOR REAL: si el controller llega a la conexión, SetWriteDeadline devuelve
	// nil y el middleware NO tiene nada que decir. Un aviso aquí sería exactamente el fallo del
	// frente hermano: el envoltorio de Gin cortando el camino al Unwrap().
	conConexion := capturaElLog(t)
	ts := httptest.NewUnstartedServer(router)
	ts.Config.WriteTimeout = 5 * time.Second
	ts.Start()
	defer ts.Close()

	if _, _, err := postReal(t, ts, router, "/intakes/in-ambar/quote-suggestion"); err != nil {
		t.Fatalf("la sugerencia debía responder sobre el servidor real: %v", err)
	}
	if conConexion.contiene(avisoSinWriteDeadline) {
		t.Error("sobre un servidor REAL el write deadline no llegó a la conexión: " +
			"http.NewResponseController no atravesó el envoltorio de Gin, y el middleware es un no-op")
	}
}
