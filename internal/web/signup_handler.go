package web

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/EduGoGroup/wapp-guardian-bff/internal/apiclient"
	"github.com/gin-gonic/gin"
)

const constantSignupSuccess = "Listo. Entra con tu correo y tu clave. Si ya tenías cuenta en el ecosistema, usa la de siempre."

// ShowSignup sirve la pantalla de registro público para el BFF.
func (h *Handler) ShowSignup(c *gin.Context) {
	if h.hasValidSession(c) {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	h.renderSignup(c, http.StatusOK, "", "")
}

// DoSignup procesa el formulario de registro de usuario en el BFF.
func (h *Handler) DoSignup(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")
	firstName := strings.TrimSpace(c.PostForm("first_name"))
	lastName := strings.TrimSpace(c.PostForm("last_name"))

	if email == "" || password == "" || firstName == "" || lastName == "" {
		h.renderSignup(c, http.StatusBadRequest, "Todos los campos son requeridos.", "")
		return
	}

	if len(password) < 12 {
		h.renderSignup(c, http.StatusBadRequest, "La contraseña debe tener al menos 12 caracteres.", "")
		return
	}

	err := h.api.Signup(c.Request.Context(), email, password, firstName, lastName, "bff")
	if err != nil {
		status, msg := signupErrorResponse(err)
		h.renderSignup(c, status, msg, "")
		return
	}

	h.renderSignup(c, http.StatusOK, "", constantSignupSuccess)
}

// signupErrorResponse traduce el error de POST /api/v1/signup a (status HTTP, mensaje honesto) para
// el formulario (A-11 · Plan 056 T5): antes de este cambio, TODO error del signup —correo ya
// registrado, alta no disponible, rate limit— caía al genérico "No se pudo procesar la solicitud" con
// un 500, porque el cliente HTTP solo sabía leer un cuerpo JSON y la plataforma responde texto plano
// (ver apiclient.decodeSignupErrorBody). Espejo del equivalente del Edge
// (wapp-edge-agent/cmd/wapp-ctl/auth.go: friendlySignupMessage): cada status no-2xx de la plataforma
// tiene su propio mensaje honesto y accionable; el resto cae a uno genérico.
//
// El 409 NO añade ninguna pista nueva sobre si el correo existe en el ecosistema más allá de lo que
// la plataforma ya expone con su propio 409 —ni tiempos distintos ni texto que distinga "existe" de
// "existe pero está bloqueado"—; el enlace "Inicia sesión" ya está siempre visible bajo el formulario
// (signup.html), así que no hace falta un CTA nuevo para ese caso.
func signupErrorResponse(err error) (status int, msg string) {
	rej, ok := apiclient.RejectionOf(err)
	if !ok {
		slog.Warn("signup falló", "error", err)
		return http.StatusInternalServerError, "No se pudo procesar la solicitud. Inténtalo más tarde."
	}
	switch rej.StatusCode {
	case http.StatusConflict:
		return http.StatusConflict, "Ya existe una cuenta con ese correo: entra con tu clave de siempre."
	case http.StatusServiceUnavailable:
		return http.StatusServiceUnavailable, "El alta no está disponible ahora mismo. Inténtalo más tarde."
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "Demasiados intentos, espera un momento."
	case http.StatusBadRequest:
		if rej.Message != "" {
			return http.StatusBadRequest, rej.Message
		}
		return http.StatusBadRequest, "Revisa los datos del formulario."
	default:
		slog.Warn("signup falló", "status", rej.StatusCode, "error", err)
		return http.StatusInternalServerError, "No se pudo procesar la solicitud. Inténtalo más tarde."
	}
}

func (h *Handler) renderSignup(c *gin.Context, status int, errMsg, successMsg string) {
	c.HTML(status, "base.html", gin.H{
		"Title":           "Crear cuenta",
		"Subtitle":        "Solicitud de acceso",
		"ContentTemplate": "signup.html",
		"Error":           errMsg,
		"Success":         successMsg,
		"CSRFToken":       c.GetString("csrf_token"),
		"Nonce":           c.GetString("csp_nonce"),
		"IsAuthenticated": false,
		"HideNav":         true,
	})
}

// ShowPending muestra la pantalla de acceso en revisión cuando el usuario no tiene tenant asignado.
func (h *Handler) ShowPending(c *gin.Context) {
	c.HTML(http.StatusOK, "base.html", gin.H{
		"Title":           "Acceso en revisión",
		"Subtitle":        "wApp",
		"ContentTemplate": "pending.html",
		"CSRFToken":       c.GetString("csrf_token"),
		"Nonce":           c.GetString("csp_nonce"),
		"IsAuthenticated": true,
		"HideNav":         true,
	})
}
