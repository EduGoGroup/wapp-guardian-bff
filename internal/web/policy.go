package web

import (
	"crypto/rand"
	"io"

	sharedweb "github.com/EduGoGroup/wapp-shared/web"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// Los nombres de las cookies DE ESTE BFF.
//
// Dejaron de ser constantes del middleware cuando éste subió a `wapp-shared/web`: allí son
// PARÁMETROS (CSRFOptions.CookieName, SessionCookieOptions.Name) justamente para que no los fije el
// módulo. Eran distintos en cada consola, y una constante compartida habría hecho que esta consola y
// la de operadores se pisaran la cookie en el mismo navegador.
const (
	sessionCookieName = "wapp_guardian_session"
	csrfCookieName    = "wapp_csrf"
)

// entropy es la fuente de aleatoriedad del nonce CSP y del token CSRF. Es una variable —y no
// crypto/rand a secas dentro del módulo— para que los tests puedan agotarla y comprobar que ESTE
// router falla cerrado: el algoritmo lo prueba el módulo, pero que el cableado del BFF le pase la
// fuente y respete su 500 solo se puede comprobar aquí.
//
// El módulo la lee de las opciones, así que las dos piezas comparten una sola fuente y por tanto un
// solo punto de fallo y un solo camino de fail-closed.
var entropy io.Reader = rand.Reader

// securityOptions es la política de cabeceras del BFF: HSTS solo tras TLS (enviarlo sobre http:// en
// local no aporta y ensucia el navegador del desarrollador).
func securityOptions(cfg *config.Config) sharedweb.SecurityOptions {
	return sharedweb.SecurityOptions{HSTS: cfg.HSTSEnabled, Rand: entropy}
}

// corsOptions arma la allowlist CORS desde el CSV de configuración. Los métodos, cabeceras y max-age
// se dejan en los del módulo a propósito: son la reconciliación de las dos consolas y no hay ninguna
// razón del BFF para separarse de ellos.
func corsOptions(cfg *config.Config) sharedweb.CORSOptions {
	return sharedweb.CORSOptions{AllowedOrigins: sharedweb.ParseOrigins(cfg.AllowedOrigins)}
}

// csrfOptions fija la cookie CSRF de ESTA consola. El TTL se deja en el del módulo (12 h), que es el
// que el BFF tenía clavado.
func csrfOptions(cfg *config.Config) sharedweb.CSRFOptions {
	return sharedweb.CSRFOptions{CookieName: csrfCookieName, Secure: cfg.CookieSecure, Rand: entropy}
}

// sessionCookieOptions es la política de la cookie de sesión del BFF: su nombre propio, Secure según
// despliegue y el SameSite de la configuración.
func sessionCookieOptions(cfg *config.Config) sharedweb.SessionCookieOptions {
	return sharedweb.SessionCookieOptions{
		Name:     sessionCookieName,
		Secure:   cfg.CookieSecure,
		SameSite: cfg.CookieSameSite,
	}
}
