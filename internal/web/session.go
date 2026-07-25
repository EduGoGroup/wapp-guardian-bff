package web

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-shared/auth"
	"github.com/golang-jwt/jwt/v5"
)

// Claves del gin.Context donde el AuthMiddleware siembra los tokens/identidad.
const (
	ctxAccessToken  = "access_token"
	ctxRefreshToken = "refresh_token"
	ctxUserID       = "user_id"
	ctxTenantID     = "tenant_id"
)

// sessionData es lo MÍNIMO que el BFF custodia server-side para operar y refrescar.
type sessionData struct {
	AccessToken  string `json:"a"`
	RefreshToken string `json:"r"`
	ExpiresAt    string `json:"e,omitempty"`
}

// encodeSession serializa la sesión a un valor de cookie seguro en base64-URL.
func encodeSession(s sessionData) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeSession revierte encodeSession.
func decodeSession(value string) (sessionData, error) {
	var s sessionData
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	return s, nil
}

// unverifiedParser parsea claims SIN verificar la firma.
var unverifiedParser = jwt.NewParser()

// parseAccessClaims extrae los claims del access token sin verificar firma.
func parseAccessClaims(accessToken string) (*auth.Claims, error) {
	var claims auth.Claims
	if _, _, err := unverifiedParser.ParseUnverified(accessToken, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// sessionValid es la validación mínima del BFF: exp presente y en el futuro.
func sessionValid(claims *auth.Claims) bool {
	exp := claims.ExpiresAt
	if exp == nil {
		return false
	}
	return exp.After(time.Now())
}

// refreshMargin es el colchón del refresh PROACTIVO.
const refreshMargin = 2 * time.Minute

// refreshDue indica si conviene refrescar la sesión.
func refreshDue(claims *auth.Claims) bool {
	exp := claims.ExpiresAt
	if exp == nil {
		return true
	}
	return time.Until(exp.Time) < refreshMargin
}
