package web

import (
	"time"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	"github.com/golang-jwt/jwt/v5"
)

// unverifiedParser parsea claims SIN verificar la firma.
var unverifiedParser = jwt.NewParser()

// parseAccessClaims extrae los claims del access token sin verificar firma.
//
// Se queda EN EL BFF y no sube a `wapp-shared/web`: ese módulo es de nivel 0 y no importa ninguna
// librería de JWT a propósito. Sus dos decisiones sobre la sesión —¿sigue valiendo?, ¿toca
// refrescar?— reciben el `exp` ya extraído, y quien lo extrae es esto.
func parseAccessClaims(accessToken string) (*sharedjwt.Claims, error) {
	var claims sharedjwt.Claims
	if _, _, err := unverifiedParser.ParseUnverified(accessToken, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// accessExp es el puente entre los claims del JWT y las dos funciones del módulo: devuelve el `exp`
// como *time.Time, o nil si el token no lo traía (que sharedweb.SessionValid trata como inválido y
// sharedweb.RefreshDue como «refresca ya»).
func accessExp(claims *sharedjwt.Claims) *time.Time {
	if claims == nil || claims.ExpiresAt == nil {
		return nil
	}
	return &claims.ExpiresAt.Time
}

// guardianWorkday es el Max-Age de la cookie de sesión: un techo fijo, no el `exp` real del access
// token. `startSession` la reemite completa en cada refresco, así que mientras el usuario siga
// volviendo dentro de esta ventana la sesión no expira nunca de verdad — el mismo patrón que
// `consoleWorkday` en `wapp-client-console` y `wapp-platform-console`.
//
// Antes de esto, el Max-Age era `sharedweb.SessionMaxAge(res.ExpiresAt)`: el vencimiento corto del
// access token. Si el usuario tardaba más que eso en volver, el NAVEGADOR borraba la cookie —y el
// refresh token que viajaba dentro de ella— antes de que `AuthMiddleware` tuviera ocasión de
// refrescar nada. Por eso el BFF perdía la sesión mientras las dos consolas no.
const guardianWorkday = 24 * time.Hour

const guardianSessionCookieMaxAge = int(guardianWorkday / time.Second)
