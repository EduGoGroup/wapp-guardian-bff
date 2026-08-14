package web

import (
	"errors"
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
		var rej *apiclient.RejectionError
		if errors.As(err, &rej) && rej.Message != "" {
			h.renderSignup(c, http.StatusBadRequest, rej.Message, "")
			return
		}
		slog.Warn("signup falló", "error", err)
		h.renderSignup(c, http.StatusInternalServerError, "No se pudo procesar la solicitud. Inténtalo más tarde.", "")
		return
	}

	h.renderSignup(c, http.StatusOK, "", constantSignupSuccess)
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
