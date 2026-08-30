package web

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// sessionsMovedDataKey es la clave con la que la portada le pasa a la plantilla la URL de la consola
// del cliente —la aplicación a la que se mudó la administración de sesiones (Plan 047 · T2.1)—.
//
// 🔴 VACÍA es el caso NORMAL, no un fallo de configuración. En UAT las dos aplicaciones son loopback
// en puertos distintos y no hay URL pública que publicar; cablear un `localhost:8107` en la plantilla
// serviría a cada operador un enlace roto salvo que esté sentado en la máquina. Con la clave vacía la
// portada dice DÓNDE se administran las sesiones sin ofrecer un enlace que no lleva a ninguna parte, y
// el día que exista una URL basta con ponerla en el entorno.
const sessionsMovedDataKey = "ClientConsoleURL"

// HomeHandler pinta la PORTADA del BFF: el índice de lo que esta consola conserva.
//
// 📌 Hasta el Plan 047 · T2.1 esto era el `DashboardHandler` y `GET /` era el dashboard de sesiones:
// listaba los teléfonos del tenant, cambiaba su perfil y enviaba mensajes. Esas tres cosas se mudaron
// a la consola del cliente (`wapp-client-console`) y aquí se retiraron en el mismo ciclo (REQ-08), con
// sus dos rutas de escritura —`POST /send` y `POST /sessions/:id/profile`—, su cliente y sus tests.
//
// 🔴 LA RUTA `GET /` NO SE FUE CON ELLAS, y no es un resto por limpiar: es el destino de tres
// redirecciones del plano de autenticación —el login correcto (DoLogin), la visita al login con sesión
// ya válida (ShowLogin) y la salida de /pending al confirmarse el tenant (AuthMiddleware)—. Borrarla
// habría convertido un login correcto en un 404. Lo que se retiró es la FUNCIÓN de sesiones, no la
// raíz: lo que queda es la portada, que enseña el plan del tenant y los accesos a lo que sigue vivo
// aquí.
type HomeHandler struct {
	cfg  *config.Config
	api  HomeAPI
	auth *AuthHandler
}

// NewHomeHandler construye un HomeHandler dependiente de HomeAPI y AuthHandler.
func NewHomeHandler(cfg *config.Config, api HomeAPI, auth *AuthHandler) *HomeHandler {
	return &HomeHandler{
		cfg:  cfg,
		api:  api,
		auth: auth,
	}
}

// ShowHome pinta la portada tras el AuthMiddleware.
//
// 🔴 El 401 persistente echa al login, y esa línea es lo que queda de la retirada del dashboard: antes
// quien expulsaba era el listado de sesiones (la llamada de negocio de la página), y al irse habría
// dejado a un token repudiado por la plataforma pintando una portada degradada en vez de mandar a
// iniciar sesión. `withAuthRetry` ya intentó refrescar y reintentar por dentro: si el error sigue
// siendo 401 aquí, la sesión está muerta de verdad.
//
// Cualquier otro fallo —red, 5xx, un 403 sin el scope— NO expulsa: la vista degradada cierra el gate,
// avisa, y la portada sigue sirviendo. Es la misma asimetría de siempre («tu sesión ya no vale» vs.
// «no se pudo preguntar»), solo que ahora vive aquí.
func (h *HomeHandler) ShowHome(c *gin.Context) {
	entitlements, err := resolveEntitlementsWithError(c, h.auth, h.api)
	if errors.Is(err, apiclient.ErrUnauthorized) {
		clearSessionCookie(h.cfg, c)
		h.auth.redirectToLogin(c)
		return
	}

	render(h.cfg, c, http.StatusOK, "home.html", gin.H{
		"Title":                 "Inicio",
		entitlementsDataKey:     entitlements,
		catalogImportNavDataKey: entitlements.Has(catalogImportFeature),
		integrationsNavDataKey:  entitlements.Has(integrationsFeature),
		tenantLLMNavDataKey:     entitlements.Has(tenantLLMFeature),
		sessionsMovedDataKey:    h.cfg.ClientConsoleURL,
	})
}
