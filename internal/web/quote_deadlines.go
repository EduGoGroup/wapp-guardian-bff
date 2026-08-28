package web

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	webgin "github.com/EduGoGroup/wapp-shared/web/gin"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// quoteSuggestionRoute es el patrón de ruta —tal como lo devuelve gin.Context.FullPath()— de la
// sugerencia de cotización. Va como constante porque lo comparan DOS piezas de plazos (el
// despachador de deadlines y su test) además del registro de la ruta: escrito a mano en tres sitios,
// el día que la ruta cambie de forma el despachador dejaría de reconocerla EN SILENCIO y la pantalla
// volvería a morir a los 20s sin que nada fallara.
const quoteSuggestionRoute = "/intakes/:id/quote-suggestion"

// ════════════════════════════════════════════════════════════════════════════
// LOS PLAZOS DE LA SUGERENCIA — POR RUTA, NUNCA GLOBALES
// ════════════════════════════════════════════════════════════════════════════
//
// El BFF tiene TRES plazos encadenados y los tres cortaban la sugerencia de cotización antes de
// que el cloud pudiera contestarla (medido contra UAT el 2026-08-28: 24,8 / 28,4 / 29,7 / 35,5 s
// por llamada, y hasta ~48-50 s de techo porque el cloud se da 48 s para redactar):
//
//	                       general      sugerencia   quién lo pone
//	  cliente HTTP            15 s          55 s     apiclient.Transport (InferenceHTTPClient)
//	  deadline de petición    20 s          58 s     requestDeadlineByRoute (aquí)
//	  write deadline          30 s          60 s     quoteSuggestionWriteDeadline (aquí)
//
// 🔴 NINGUNO DE LOS TRES GENERALES SE TOCÓ, y no es conservadurismo: el WriteTimeout de 30 s es la
// defensa anti-slowloris del BFF (REQ-B4 — aquí no hay streams de larga vida, al revés que en
// edugo-messaging-web), y el UpstreamTimeout DEBE quedar por debajo de él para que el modo
// degradado alcance a pintarse (config.go). Subirlos le daría a las 20 y pico de rutas de esta
// consola un plazo que necesita UNA.
//
// El orden importa y es lo que sostiene el diseño: cliente < deadline de petición < write deadline.
// Así, cuando la espera se pasa de larga, el que corta primero es el cliente HTTP —que devuelve un
// error que el handler traduce a un aviso en pantalla— y no el servidor, que cierra la conexión y
// deja al navegador sin nada. Los tres números salen de UN solo valor configurable
// (config.QuoteSuggestionTimeout) más dos márgenes, para que el orden no dependa de que alguien
// recuerde mover tres constantes a la vez.

// requestDeadlineByRoute instala el deadline por petición del grupo protegido, con UNA excepción: la
// ruta de la sugerencia, que se lleva el suyo.
//
// Es un despachador y no dos grupos de gin hermanos a propósito. Un segundo grupo con su propio
// RequestDeadline habría duplicado la cadena entera de middlewares del grupo protegido, y el día que
// alguien añadiera uno a `protected` la ruta de la sugerencia se quedaría sin él sin que nada lo
// dijera. Aquí solo se bifurca el plazo, que es lo único que cambia.
//
// 🔴 NO SIRVE ponerle a la ruta un middleware propio DESPUÉS del del grupo: un context.WithTimeout
// más largo colgado de un padre que vence antes no alarga nada —gana siempre el padre—, así que el
// plazo largo tiene que sustituir al corto, no añadirse.
func requestDeadlineByRoute(cfg *config.Config) gin.HandlerFunc {
	short := webgin.RequestDeadline(cfg.UpstreamTimeout)
	// Con el plazo de la sugerencia apagado (0), la ruta NO se queda sin deadline: cae al del grupo,
	// como cualquier otra. Un cero significa «sin plazo propio», nunca «sin plazo».
	long := short
	if d := cfg.QuoteSuggestionRequestDeadline(); d > 0 {
		long = webgin.RequestDeadline(d)
	}
	return func(c *gin.Context) {
		// Los dos llaman a c.Next() por dentro, así que la cadena sigue desde el que se elija y aquí
		// no se vuelve a llamar.
		if c.FullPath() == quoteSuggestionRoute {
			long(c)
			return
		}
		short(c)
	}
}

// quoteSuggestionWriteDeadline releva al WriteTimeout del http.Server durante esta petición y solo
// durante ésta, instalando el deadline de escritura sobre la conexión con http.NewResponseController.
//
// 🔴 ES EL PLAZO QUE FALLA SIN DEJAR RASTRO. Los otros dos, al vencer, devuelven un error que el
// handler convierte en un aviso legible; el WriteTimeout cierra la conexión a mitad y el navegador
// se queda sin página que pintar, sin aviso y sin explicación. Por eso este middleware es la pieza
// que no se puede quitar aunque los otros dos plazos estén bien puestos.
//
// El error de SetWriteDeadline no aborta la petición: fuera de un servidor HTTP real —un
// httptest.ResponseRecorder, por ejemplo— no hay conexión donde poner un deadline, y ahí la
// ausencia es lo normal y no un fallo. En producción sí lo sería, y por eso se distinguen: lo
// esperable va a Debug y lo demás a Warn.
func quoteSuggestionWriteDeadline(cfg *config.Config) gin.HandlerFunc {
	d := cfg.QuoteSuggestionWriteDeadline()
	return func(c *gin.Context) {
		if d <= 0 {
			c.Next()
			return
		}
		if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(d)); err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				slog.Debug("sin conexión donde instalar el write deadline de la sugerencia",
					"ruta", c.FullPath(), "error", err)
			} else {
				slog.Warn("no se pudo ampliar el write deadline de la sugerencia; "+
					"el WriteTimeout del servidor puede cortar la respuesta a mitad",
					"ruta", c.FullPath(), "plazo", d, "error", err)
			}
		}
		c.Next()
	}
}
