package config

import (
	"os"
	"testing"
	"time"
)

// sinLaVariable deja el entorno sin la env var del plazo de la sugerencia mientras dura el test, y
// la repone al salir. Hace falta porque este test comprueba DEFAULTS: con la variable puesta en la
// máquina que lo corre, mediría la configuración del que lo corre y no el default del binario.
func sinLaVariable(t *testing.T, clave string) {
	t.Helper()
	anterior, había := os.LookupEnv(clave)
	if err := os.Unsetenv(clave); err != nil {
		t.Fatalf("no se pudo limpiar %s: %v", clave, err)
	}
	t.Cleanup(func() {
		if había {
			_ = os.Setenv(clave, anterior)
			return
		}
		_ = os.Unsetenv(clave)
	})
}

// TestLosPlazosDeLaSugerenciaSuperanALosGenerales es el criterio de esta tarea escrito en números de
// PRODUCCIÓN: los tres plazos de la ruta de la sugerencia tienen que ir por encima de sus
// equivalentes generales, o la ruta se sigue muriendo antes de que el cloud conteste.
//
// Los generales están medidos y son los que son: 15s el cliente HTTP (apiclient), 20s el
// UpstreamTimeout y 30s el WriteTimeout. La llamada tarda 24,8-35,5s contra UAT y el cloud se da
// 48s de techo, así que un plazo de la sugerencia por debajo de esos generales sería no haber
// arreglado nada.
func TestLosPlazosDeLaSugerenciaSuperanALosGenerales(t *testing.T) {
	sinLaVariable(t, "WAPP_GUARDIAN_QUOTE_SUGGESTION_TIMEOUT_SECS")
	sinLaVariable(t, "WAPP_GUARDIAN_UPSTREAM_TIMEOUT_SECS")
	sinLaVariable(t, "WAPP_GUARDIAN_WRITE_TIMEOUT_SECS")

	cfg := Load()

	const techoDelCloud = 48 * time.Second // pipeline.PlazoPorLlamadaSuelo, medido el 2026-08-28.

	if cfg.QuoteSuggestionTimeout < techoDelCloud {
		t.Errorf("el plazo del cliente de la sugerencia (%s) no cubre el techo que el cloud se da "+
			"para redactar (%s): la llamada se corta antes de que llegue la respuesta",
			cfg.QuoteSuggestionTimeout, techoDelCloud)
	}
	if cfg.QuoteSuggestionRequestDeadline() <= cfg.UpstreamTimeout {
		t.Errorf("el deadline de la sugerencia (%s) no supera al general (%s): la ruta se sigue "+
			"cortando con el plazo del grupo", cfg.QuoteSuggestionRequestDeadline(), cfg.UpstreamTimeout)
	}
	if cfg.QuoteSuggestionWriteDeadline() <= cfg.WriteTimeout {
		t.Errorf("el write deadline de la sugerencia (%s) no supera al WriteTimeout del servidor "+
			"(%s): la conexión se corta igual, y ése es el corte que no deja pintar nada",
			cfg.QuoteSuggestionWriteDeadline(), cfg.WriteTimeout)
	}

	// Y el orden entre los tres, que es lo que hace que el corte —cuando llegue— sea el del cliente
	// HTTP (traducible a un aviso) y no el del servidor (una conexión cerrada sin nada que enseñar).
	if cfg.QuoteSuggestionTimeout >= cfg.QuoteSuggestionRequestDeadline() {
		t.Errorf("el cliente (%s) debe cortar ANTES que el deadline de petición (%s), o el corte "+
			"llega sin cuerpo que leer y el aviso en pantalla sale peor",
			cfg.QuoteSuggestionTimeout, cfg.QuoteSuggestionRequestDeadline())
	}
	if cfg.QuoteSuggestionRequestDeadline() >= cfg.QuoteSuggestionWriteDeadline() {
		t.Errorf("el deadline de petición (%s) debe vencer ANTES que el write deadline (%s), o corta "+
			"el servidor y no queda conexión donde pintar el aviso",
			cfg.QuoteSuggestionRequestDeadline(), cfg.QuoteSuggestionWriteDeadline())
	}
}

// TestNoSeTocaronLosPlazosGenerales: la decisión de esta tarea fue plazo POR RUTA, y esto lo fija en
// un test. Si algún día alguien "arregla" la sugerencia subiendo los globales, aquí se entera: el
// WriteTimeout es la defensa anti-slowloris del BFF (REQ-B4) y el UpstreamTimeout tiene que quedar
// por debajo de él para que el modo degradado alcance a pintarse.
func TestNoSeTocaronLosPlazosGenerales(t *testing.T) {
	sinLaVariable(t, "WAPP_GUARDIAN_UPSTREAM_TIMEOUT_SECS")
	sinLaVariable(t, "WAPP_GUARDIAN_WRITE_TIMEOUT_SECS")

	cfg := Load()

	if cfg.UpstreamTimeout != 20*time.Second {
		t.Errorf("el UpstreamTimeout general debía seguir en 20s y está en %s", cfg.UpstreamTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Errorf("el WriteTimeout general debía seguir en 30s y está en %s", cfg.WriteTimeout)
	}
	if cfg.UpstreamTimeout >= cfg.WriteTimeout {
		t.Errorf("el UpstreamTimeout (%s) tiene que quedar por debajo del WriteTimeout (%s) para que "+
			"el modo degradado alcance a pintarse", cfg.UpstreamTimeout, cfg.WriteTimeout)
	}
}

// TestElPlazoDeLaSugerenciaSeApagaConCero cubre la puerta: 0 == la ruta vuelve a los plazos
// generales. Los dos derivados tienen que apagarse CON él, o quedaría una ruta con write deadline
// propio y sin plazo de cliente que lo respalde.
func TestElPlazoDeLaSugerenciaSeApagaConCero(t *testing.T) {
	cfg := &Config{QuoteSuggestionTimeout: 0}
	if d := cfg.QuoteSuggestionRequestDeadline(); d != 0 {
		t.Errorf("con el plazo apagado el deadline de petición debía ser 0 y salió %s", d)
	}
	if d := cfg.QuoteSuggestionWriteDeadline(); d != 0 {
		t.Errorf("con el plazo apagado el write deadline debía ser 0 y salió %s", d)
	}
}
