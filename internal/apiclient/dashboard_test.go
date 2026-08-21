package apiclient

import "testing"

// TestEffectiveProfileNuncaInventaActive fija el contrato de EffectiveProfile por su consecuencia,
// no por su implementación: el valor que devuelve decide qué perfil ve el dueño preseleccionado en
// la consola, y "active" es el que hace que la sesión conteste sola.
//
// Por eso el caso que manda es el ÚLTIMO: sin un `profile` utilizable NO se cae a "active".
// Caerse ahí sería enseñarle al dueño que una sesion de la que no sabemos nada «conversa sola», y un
// clic en «Aplicar» la activaría de verdad. Ante la duda, DESCONOCIDO ("") — y dashboard.html lo
// pinta como «— sin dato —», porque un <select> sin `selected` enseña la primera opción, no ninguna.
func TestEffectiveProfileNuncaInventaActive(t *testing.T) {
	casos := []struct {
		nombre  string
		sesion  Session
		esperar string
	}{
		// El camino normal.
		{"profile active", Session{Profile: "active"}, "active"},
		{"profile passive", Session{Profile: "passive"}, "passive"},

		// 🔧 Aquí había cuatro casos más sobre el campo `role` y sobre qué eje ganaba cuando los dos
		// venían y se contradecían. Se fueron con la 0064, que retiró ese campo: la plataforma ya no
		// lo emite y la pregunta «¿quién gana?» dejó de existir. Lo que NO se fue son los dos casos
		// de abajo, que son los que de verdad protegen al dueño.

		// Basura en el único eje que hay NO se propaga a la vista. `bot` está aquí a propósito: es
		// el valor que la plataforma emitía ANTES, y si alguien lo reintrodujera por error NO puede
		// colarse como «activa».
		{"profile desconocido (el vocabulario viejo)", Session{Profile: "bot"}, ""},
		{"profile desconocido (castellano)", Session{Profile: "activa"}, ""},

		// 🔴 El caso que protege al dueño: no hay dato ⇒ no hay perfil, y NO es "active".
		{"vacío", Session{}, ""},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.sesion.EffectiveProfile(); got != c.esperar {
				t.Errorf("EffectiveProfile() = %q, esperaba %q", got, c.esperar)
			}
		})
	}
}
