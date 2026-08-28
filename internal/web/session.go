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
