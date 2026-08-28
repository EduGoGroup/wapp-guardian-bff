package web

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
)

// entitlementsDataKey es la clave con la que la vista de features entra en los datos de plantilla.
// Cualquier página que quiera gatear un bloque la necesita puesta: sin ella la plantilla ni siquiera
// puede preguntar.
const entitlementsDataKey = "Entitlements"

// entitlementsView es el plan del tenant tal como lo consume la plantilla.
//
// El gate del BFF es SERVER-SIDE: el bloque de una sección sin feature no se emite en el HTML, no se
// esconde con CSS ni con JS. Eso encaja con la CSP endurecida (sin 'unsafe-inline') y, sobre todo,
// hace que lo no contratado no esté ahí para que nadie lo destape con el inspector.
type entitlementsView struct {
	// Plan es el identificador del plan del tenant ("basic", "advisor_ai", …). Vacío si no se resolvió.
	Plan string
	// Features son las efectivas, en el orden alfabético estable que fija el servidor (para los chips).
	Features []string
	// Resolved distingue "el tenant no tiene features" de "no se pudo preguntar".
	Resolved bool
	// Notice es el aviso legible del modo degradado (vacío cuando se resolvió).
	Notice string

	enabled map[string]bool
}

// Has responde si la feature está habilitada para el tenant.
//
// El gate es fail-closed por construcción: la vista cero —la que queda cuando el endpoint falla,
// responde 403 o todavía no existe en la plataforma— no tiene ninguna feature, así que ningún bloque
// condicionado se emite. Preferimos una consola que enseña de menos a una que promete lo que el
// tenant no ha contratado.
func (v entitlementsView) Has(feature string) bool { return v.enabled[feature] }

// resolveEntitlements pide las features efectivas del tenant con el token de la sesión y devuelve
// SIEMPRE una vista usable: el fallo se traduce en la vista cero (fail-closed) más un aviso, nunca en
// un error que tumbe la página. Una pantalla debe seguir sirviendo aunque el catálogo de planes no
// conteste.
//
// El 401 no se trata aquí: withAuthRetry ya refresca y reintenta, y si aun así persiste, quien decide
// expulsar al operador es la llamada de negocio de la página, no esta consulta accesoria.
//
// 🔴 LA PORTADA ES LA EXCEPCIÓN, y no por gusto. Hasta el Plan 047 · T2.1 su llamada de negocio era el
// listado de sesiones, y era ESA la que echaba al login ante un 401 persistente. Al retirarse el
// dashboard, la portada se quedó con una sola llamada —ésta— y con ella se habría ido esa expulsión:
// un token vigente en el reloj pero repudiado por la plataforma habría pintado una portada degradada
// en vez de mandar a iniciar sesión. Por eso existe resolveEntitlementsWithError: la portada lee el
// error y decide, y el resto de pantallas —que sí tienen su propia llamada de negocio— siguen usando
// esta, que lo traga.
func resolveEntitlements(c *gin.Context, auth *AuthHandler, api EntitlementsReader) entitlementsView {
	view, _ := resolveEntitlementsWithError(c, auth, api)
	return view
}

// resolveEntitlementsWithError es resolveEntitlements devolviendo además el fallo que la vista
// degradada esconde. La vista que devuelve es SIEMPRE usable, error o no: quien lo ignore se comporta
// exactamente como antes.
func resolveEntitlementsWithError(c *gin.Context, auth *AuthHandler, api EntitlementsReader) (entitlementsView, error) {
	var ent *apiclient.Entitlements
	err := auth.withAuthRetry(c, func(accessToken string) error {
		var gerr error
		ent, gerr = api.GetEntitlements(c.Request.Context(), accessToken)
		return gerr
	})
	if err != nil || ent == nil {
		slog.Warn("no se pudieron leer las features del tenant (modo degradado; el gate cierra)", "error", err)
		return entitlementsView{Notice: entitlementsNotice(err)}, err
	}

	enabled := make(map[string]bool, len(ent.Features))
	for _, f := range ent.Features {
		enabled[f] = true
	}
	return entitlementsView{
		Plan:     ent.Plan,
		Features: ent.Features,
		Resolved: true,
		enabled:  enabled,
	}, nil
}

// entitlementsNotice traduce el fallo a un aviso legible, sin filtrar el detalle del upstream (mismo
// criterio que el resto de mappers del BFF).
func entitlementsNotice(err error) string {
	if apiclient.StatusCodeOf(err) == http.StatusForbidden {
		return "Tu usuario no tiene permiso para consultar el plan del tenant. " +
			"Las secciones que dependen de una capacidad quedan ocultas."
	}
	return "No se pudo consultar el plan del tenant ahora mismo. " +
		"Las secciones que dependen de una capacidad quedan ocultas hasta que se pueda comprobar."
}
