package apiclient

import "testing"

// TestEffectiveProfileNuncaInventaActive fija el contrato de EffectiveProfile por su consecuencia,
// no por su implementación: el valor que devuelve decide qué perfil ve el dueño preseleccionado en
// la consola, y "active" es el que hace que la sesión conteste sola.
//
// Por eso el caso que manda es el ÚLTIMO: sin `profile` y sin `role` traducible NO se cae a "active".
// Caerse ahí sería enseñarle al dueño que una sesión de la que no sabemos nada «conversa sola», y un
// clic en «Aplicar» la activaría de verdad. Ante la duda, DESCONOCIDO ("") — y dashboard.html lo
// pinta como «— sin dato —», porque un <select> sin `selected` enseña la primera opción, no ninguna.
func TestEffectiveProfileNuncaInventaActive(t *testing.T) {
	casos := []struct {
		nombre  string
		sesion  Session
		esperar string
	}{
		// El camino normal, una vez T1.2 está desplegada.
		{"solo profile active", Session{Profile: "active"}, "active"},
		{"solo profile passive", Session{Profile: "passive"}, "passive"},

		// El respaldo del ciclo de deprecación: plataforma vieja, BFF nuevo.
		{"solo role bot", Session{Role: "bot"}, "active"},
		{"solo role passive", Session{Role: "passive"}, "passive"},

		// Los dos ejes vienen y se CONTRADICEN. Gana `profile` en las dos direcciones: es el mismo
		// orden que la plataforma (fleet.go: la lectura de negocio ya solo mira Profile; `role` es un
		// alias deprecado que puede quedarse rancio). No es «gana el más seguro», es «gana la fuente».
		{"contradictorios: profile passive gana al role bot", Session{Profile: "passive", Role: "bot"}, "passive"},
		{"contradictorios: profile active gana al role passive", Session{Profile: "active", Role: "passive"}, "active"},

		// Basura en cualquiera de los dos ejes NO se propaga a la vista.
		{"profile desconocido con role utilizable", Session{Profile: "bot", Role: "passive"}, "passive"},
		{"profile desconocido y role inservible", Session{Profile: "activa", Role: "primary"}, ""},

		// 🔴 El caso que protege al dueño: no hay dato ⇒ no hay perfil, y NO es "active".
		{"los dos vacíos", Session{}, ""},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.sesion.EffectiveProfile(); got != c.esperar {
				t.Errorf("EffectiveProfile() = %q, esperaba %q", got, c.esperar)
			}
		})
	}
}
