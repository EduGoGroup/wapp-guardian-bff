package bootstrap

import (
	"fmt"
	"log/slog"
	"strings"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/config"
)

// identityIssuer es el `iss` que llevan los Identity Tokens del microecosistema identity. Es un valor
// del contrato del emisor, no una URL configurable: identity-core firma con ese issuer en todos sus
// ambientes, y lo que cambia entre local y nube son las claves (el JWKS), no el nombre de quien firma.
const identityIssuer = "identity-core"

// newIdentityVerifier construye el verificador de Identity Tokens de identity-core, o devuelve nil si
// el modo dual está apagado.
//
// La puerta es WAPP_IDENTITY_JWKS_URL (cfg.IdentityJWKSURL): sin ella el BFF arranca sin identity, como
// hasta hoy. Con ella, el constructor de identity-shared hace el primer fetch del JWKS AQUÍ, en el
// arranque, y falla si no puede completarlo — un identity-api apagado deja al BFF sin arrancar. Eso es
// lo pretendido: preferimos no nacer a nacer con un verificador de cero claves, que confundiría una
// caída del emisor con credenciales inválidas del usuario.
func newIdentityVerifier(cfg *config.Config) (*identityjwt.MultiVerifier, error) {
	jwksURL := strings.TrimSpace(cfg.IdentityJWKSURL)
	if jwksURL == "" {
		return nil, nil
	}

	verifier, err := identityjwt.NewMultiVerifierFromJWKS(identityIssuer, identityjwt.JWKSOptions{URL: jwksURL})
	if err != nil {
		return nil, fmt.Errorf("no se pudo construir el verificador de Identity Tokens contra %s: %w", jwksURL, err)
	}

	slog.Info("modo dual de identidad activado: verificador de Identity Tokens listo",
		"emisor", identityIssuer, "jwks", jwksURL)
	return verifier, nil
}
