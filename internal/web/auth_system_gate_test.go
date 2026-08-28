package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLogin_ElLogDISTINGUE401De403 — la RESPUESTA funde credenciales y System Gate a propósito
// (REQ-C3: no filtrar si un correo existe), así que el LOG es el ÚNICO sitio donde queda la
// diferencia. Quien diagnostica un «no puedo entrar» decide con esa línea si buscar la contraseña o
// la fila de `iam.user_systems`.
//
// 🔴 Nació de un fallo real: el 2026-08-28 costó una tarde porque el BFF fundía el 403 del System
// Gate en un 401 «Credenciales inválidas», log incluido. Quien tenía la contraseña bien y no tenía
// acreditada la aplicación leía «revisa tus datos», reintentaba y acababa bloqueado por el lockout.
// Las dos consolas ya separaban estas ramas; el BFF era el único que no.
//
// 🔑 Con el hueco de acreditación cerrado (Plan 047 · Ola B) esto importa MÁS, no menos: ahora que el
// alta acredita, el 403 que quede será de las cuentas viejas, y es justo cuando hay que distinguirlo.
//
// Deliberadamente SIN t.Parallel(): slog.SetDefault es global y el buffer solo debe recoger lo de
// este login.
func TestLogin_ElLogDISTINGUE401De403(t *testing.T) {
	casos := []struct {
		nombre     string
		estado     int
		esperado   string
		noEsperado string
	}{
		{"credenciales", http.StatusUnauthorized, "credenciales inválidas", "System Gate"},
		{"system_gate", http.StatusForbidden, "System Gate", "credenciales inválidas"},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			api := fakeAPI(c.estado, `{"error":{"code":"forbidden","message":"nope"}}`)
			defer api.Close()

			var log bytes.Buffer
			anterior := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))
			defer slog.SetDefault(anterior)

			router := NewRouter(authTestCfg(api.URL))
			rec := postForm(router, "/login", url.Values{
				"email": {"quien-no-entra@empresa.test"}, "password": {"la-que-si-es"},
			})

			// La respuesta NO distingue: los dos son 401 con el mismo texto ciego.
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("la respuesta tiene que ser 401 en los dos casos y fue %d", rec.Code)
			}

			escrito := log.String()
			if !strings.Contains(escrito, c.esperado) {
				t.Fatalf("con la API devolviendo %d, el log tiene que decir %q y dice: %s", c.estado, c.esperado, escrito)
			}
			if strings.Contains(escrito, c.noEsperado) {
				t.Fatalf("con la API devolviendo %d, el log NO puede decir %q: %s", c.estado, c.noEsperado, escrito)
			}
		})
	}
}

// TestLogin_LaRespuestaNODistingue401De403 — el hermano negativo del de arriba, y no es redundante:
// fija que separar el LOG no se coló en la PANTALLA. Si alguien «mejora» el mensaje diciéndole al
// visitante que su cuenta existe pero no tiene acceso, filtra el padrón y este test muere.
func TestLogin_LaRespuestaNODistingue401De403(t *testing.T) {
	cuerpos := make([]string, 0, 2)
	for _, estado := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		api := fakeAPI(estado, `{"error":{"code":"x","message":"nope"}}`)
		router := NewRouter(authTestCfg(api.URL))
		rec := postForm(router, "/login", url.Values{
			"email": {"quien-no-entra@empresa.test"}, "password": {"la-que-si-es"},
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("con la API devolviendo %d la pantalla tiene que dar 401 y dio %d", estado, rec.Code)
		}
		cuerpos = append(cuerpos, rec.Body.String())
		api.Close()
	}
	// Se compara el MENSAJE, no el cuerpo entero: cada render trae su propio token CSRF y su nonce
	// CSP, así que una igualdad byte a byte compararía ruido y fallaría siempre.
	const mensajeCiego = "Credenciales inválidas. Revisa tus datos e inténtalo de nuevo."
	for i, cuerpo := range cuerpos {
		if !strings.Contains(cuerpo, mensajeCiego) {
			t.Fatalf("el caso %d tiene que mostrar el mensaje ciego %q y no lo hace: %s", i, mensajeCiego, cuerpo)
		}
		if strings.Contains(cuerpo, "System Gate") || strings.Contains(cuerpo, "user_systems") {
			t.Fatalf("la pantalla no puede nombrar el System Gate: %s", cuerpo)
		}
	}
}
