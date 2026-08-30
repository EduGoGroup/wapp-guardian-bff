package config

import (
	"os"
	"testing"
	"time"
)

// sinLaVariable deja el entorno sin una env var mientras dura el test y la repone al salir. Hace
// falta porque el test de abajo comprueba DEFAULTS: con la variable puesta en la máquina que lo
// corre, mediría la configuración del que lo corre y no el default del binario.
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

// TestNoSeTocaronLosPlazosGenerales fija los dos plazos generales del BFF y, sobre todo, su ORDEN.
//
// 🔴 ES UN ASERTO RESCATADO (Plan 047 · T7.7). Nació junto a los plazos de la sugerencia de
// cotización, para que nadie "arreglara" esa ruta subiendo los globales; esa ruta se mudó a la consola
// del cliente y sus tres plazos propios se fueron con ella, pero el invariante que este test vigila no
// era suyo: el WriteTimeout es la defensa anti-slowloris del BFF (REQ-B4, aquí no hay streams de larga
// vida) y el UpstreamTimeout tiene que quedar POR DEBAJO de él para que el modo degradado alcance a
// pintarse. Sin ese orden, un upstream lento cierra la conexión a mitad y el navegador se queda sin
// página que pintar, sin aviso y sin explicación. Se queda escrito como la regla a secas.
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
