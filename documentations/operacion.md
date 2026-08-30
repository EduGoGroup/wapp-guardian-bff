# Operación de `wapp-guardian-bff`

Cómo se arranca, se prueba, se publica y se depura. Todo verificado ejecutándolo el 2026-08-30 sobre
el commit `26e84c9`.

---

## 1. 🔴 El aviso que aplica a todo el ecosistema y hay que repetir aquí

**Un PR no valida nada.** `.github/workflows/ci.yml` es `on: workflow_dispatch` (línea 19-20) y
nada más. No se dispara con `push` ni con `pull_request`. El único workflow que corre solo es
`sync-main-to-dev.yml`, que alinea `dev` con `main` tras cada push a `main` y **no valida ni compila
nada** — lo dice su propia cabecera. **El gate real es local: `make ci-local`.**

**Un `rc=0` no significa que se haya probado algo.** `go test` devuelve 0 igual con un `--- SKIP`
que con un `--- PASS`, así que **hay que contar los SKIP a mano**:

```bash
GOWORK=off go test ./... -v 2>&1 | grep -c -- '--- SKIP'
GOWORK=off go test ./... -v 2>&1 | grep -c -- '--- PASS'
```

Medido hoy en este repo: **0 SKIP y 192 PASS**. Y aquí eso es un dato sólido, no una casualidad:
**este repo no tiene tests de integración**. No hay `WAPP_TEST_DB_DSN`, ni `testcontainers`, ni
`-tags integration`, ni un solo `t.Skip` condicionado por variable de entorno. En otros repos del
ecosistema los tests de integración **se saltan solos** cuando falta la variable de base de datos, y
ahí el conteo de SKIP es obligatorio; aquí sirve para detectar el día que alguien introduzca el
primero.

Y una tercera trampa del mismo género: **lee el `rc` sin pipe**. `make ci-local | tee log` devuelve
el código de `tee`, no el del gate.

---

## 2. Arranque en local

Requisitos: **Go 1.26.5** (la misma versión que fija el `Makefile`) y nada más — no hace falta
Docker, ni Postgres, ni red, salvo que quieras hablar con una plataforma de verdad.

```bash
cp .env.example .env      # ajusta lo que necesites; NO metas secretos en .env.example
export $(grep -v '^#' .env | xargs)   # o cárgalo con tu herramienta habitual
GOWORK=off go run ./cmd/guardian-bff
```

Escucha en **`:8104`** (`WAPP_GUARDIAN_HTTP_ADDR`, default `:8104`) y espera una plataforma en
`http://localhost:8103` (`WAPP_PUBLIC_API_BASE`). Sin ella arranca igual: lo que falla son las
llamadas, y las pantallas caen en **modo degradado** con el gate cerrado (fail-closed), salvo el
login, que no puede autenticar.

Comprobación de vida:

```bash
curl -s localhost:8104/healthz     # {"status":"healthy","time":"..."}
curl -s -o /dev/null -w '%{http_code}\n' localhost:8104/login   # 200
```

**Nota sobre `GOWORK=off`.** El `Makefile` lo pone en todos sus targets (`GO := GOWORK=off go`,
`Makefile:18`) para que el repo se compile contra los módulos **publicados** de `wapp-shared` y no
contra el árbol de al lado a través de un `go.work`. Un puerto de `wapp-shared` se verifica contra
el **tag publicado**, nunca contra el checkout vecino: si compilas con el `go.work` puesto puedes
tener verde algo que en un clon limpio no compila.

**Encender el selector Alpha en local** (autocompleta correos de prueba en el login):
`WAPP_ALPHA_TEST_ACCOUNTS=true` y `WAPP_ALPHA_TEST_PASSWORD=<la que sea>`. Es un atajo
**deliberadamente inseguro** —los correos de prueba están fijos en `templates/pages/login.html` y la
contraseña viaja en el HTML— y por eso nace apagado. **Nunca en un ambiente con datos reales.**

**Encender la delegación en identity:** pon `WAPP_IDENTITY_URL`. Con ella el login viaja a identity
con el system `wapp.bff` y el token se canjea en la plataforma; si la plataforma no tiene su propio
verificador, el canje responde 503 y verás el mensaje de «servicio de identidad no disponible», que
**no** es un fallo de credenciales.

---

## 3. Cómo se prueba: los `make` reales y qué valida cada uno

| Target | Qué corre exactamente | Qué acredita |
|---|---|---|
| `make fmt-check` | `gofmt -l .` y falla si la salida no está vacía | que ningún fichero está sin formatear |
| `make vet` | `GOWORK=off go vet ./...` | los fallos que el compilador no ve (formatos, shadowing de métodos…) |
| `make lint` | `golangci-lint run --timeout=5m` con **v2.12.2** | `errcheck` (con `check-type-assertions: true`), `govet`, `ineffassign`, `staticcheck`, `unused`, `errorlint`, `errname`, `nilerr`, más `gofmt` y `goimports` como formateadores (`.golangci.yml:8-27`) |
| `make test` | `GOWORK=off go test -race ./...` | las 147 funciones `Test`, **con detector de carreras**. Aquí viven los candados del inventario |
| `make build` | `GOWORK=off go build ./...` | que compila |
| **`make ci-local`** | `fmt-check vet lint test build`, en ese orden (`Makefile:39`) | **el gate. Es lo que hay que pasar antes de mergear y de pushear** |
| `make ci-docker` | lo anterior dentro de `golang:1.26.5-bookworm`, instalando el lint pinado | reproduce el toolchain fijado; **exige Docker** |

⚠️ **`ci-local` y `ci-docker` no dicen siempre lo mismo.** El segundo corre con el toolchain fijado y
sin tu caché; hay casos históricos en el ecosistema de `ci-local` verde y `ci-docker` rojo en el
mismo commit. Si vas a publicar, pasa los dos.

**El lint está pinado a v2.12.2 y no es capricho:** es la primera versión compilada con go1.26; la
v2.4.0 iba con go1.25 y **no podía cargar** un `go.mod` que apunta a `go 1.26` (salía
`exit 3, can't load config`).

### Cobertura, medida

```
internal/config      100,0 %
internal/web          81,1 %
internal/bootstrap     75,7 %
internal/apiclient      0,0 %   🔴
cmd/guardian-bff        0,0 %   (sin ficheros de test)
```

**`internal/apiclient` está al 0,0 % de sentencias**, y no es un redondeo: su único test
(`TestTenantLLMNoTieneDondeGuardarLaCredencial`, `internal/apiclient/tenantllm_test.go:21`) es un
aserto de tipos por reflexión que **no ejecuta ni una línea de producción**. Las 1.011 líneas del
cliente HTTP se prueban solo indirectamente, desde `internal/web`, con servidores `httptest` falsos.
Está anotado en [`deuda.md`](deuda.md).

### Correr un candado suelto

```bash
GOWORK=off go test ./internal/web/ -run 'TestElInventarioDeRutasEsExactamenteElDeREQ10' -v
GOWORK=off go test ./internal/web/ -run 'TestNingunaConstanteDeRutaNombraUnaRutaFantasma' -v
```

⚠️ Un verde con `-run` suelto puede depender del orden alfabético de los paquetes o de estado que
otro test dejó montado. Antes de dar algo por bueno, **corre la suite entera**.

---

## 4. Cómo se publica una versión

Este repo **no tiene `release.yml`**: la versión se corta a mano. El flujo del ecosistema:

1. El trabajo aterriza en **`dev`**. A **`main`** se pasa al final del plan, no ola a ola.
2. Antes de pushear, `make ci-local` (y `make ci-docker` si vas a publicar). Verde de verdad, con
   los SKIP contados.
3. Merge `dev` → `main` y push. El workflow `sync-main-to-dev.yml` devuelve `dev` a la altura de
   `main` solo: no hay que hacerlo a mano.
4. El tag, si se corta, es `vX.Y.Z` sobre `main`.

**Estado hoy:** `HEAD`, `main`, `dev` y `origin/main` están en el **mismo commit `26e84c9`**, y el
único tag es **`v0.1.0`** (`afa8bd4`, del 2026-08-28), que es **anterior a la mudanza**: no hay tag
que describa el BFF de 20 rutas. Existe además una rama local de respaldo
`backup/pre-047-main-20260828`, que **no está en `origin`**.

**Despliegue en UAT** (para saber contra qué te comparas, no como receta): corre como unidad systemd
`wapp-bff`, con el binario en `/usr/local/bin/wapp-guardian-bff` y su `EnvironmentFile` propio. La
unidad lleva `PartOf=wapp-cloud.service`, que propaga stop y restart pero **no** start. El binario
vivo lleva `vcs.revision = 26e84c9…` y `vcs.modified=false`.

⚠️ **Instalar el binario y reiniciar el servicio son dos pasos.** Revisar el fichero de
`/usr/local/bin` no dice qué está corriendo. Lo que corre se pregunta así:

```bash
readlink /proc/$(systemctl show -p MainPID --value wapp-bff)/exe
go version -m /proc/$(systemctl show -p MainPID --value wapp-bff)/exe | grep vcs.revision
```

Es la única forma fiable: **el binario no sabe decir su versión** (`-version` no es un flag y el
proceso arranca ignorándolo, chocando con el puerto ya ocupado).

---

## 5. Cómo se depura cuando falla

### Lo primero, en orden

1. **¿Está vivo?** `curl -s localhost:8104/healthz`. Un 200 aquí es **liveness pura**: no dice nada
   de la plataforma ni de identity.
2. **¿Qué binario corre?** El bloque de arriba. Si el `vcs.revision` no es el que crees, el resto
   del diagnóstico sobra.
3. **¿En qué ambiente se cree?** El arranque loguea `consola BFF escuchando addr=… ambiente=…`. Si
   dice `local`, las cookies **no** son `Secure` y **no** hay HSTS, y el log sale en texto en vez de
   JSON. En UAT hoy dice `local`; ver [`deuda.md`](deuda.md).

### Síntomas y su causa real

| Lo que ves | Lo que suele ser |
|---|---|
| **El proceso no arranca y no hay ni una petición** | uno de los cuatro fail-closed: allowlist de proxies malformada (`server.go:83-88`), plantillas que no compilan (`server.go:129-132`), `WAPP_IDENTITY_URL` mal escrita (`handlers.go:57-62`) o el JWKS de identity que no responde (`bootstrap/server.go:33-35`). **Es deliberado: el BFF prefiere no nacer.** |
| **`bind: address already in use`** | ya hay un BFF escuchando en `:8104`. Ojo: es también lo que pasa si ejecutas el binario «para ver su versión». |
| **Todo redirige a `/pending` y no hay salida** | el token no trae `tenant_id`. El usuario no tiene empresa asignada; se resuelve fuera del BFF. |
| **Login correcto que acaba en 404** | alguien tocó `GET /`. Es el destino de tres redirecciones; el test `TestLasTresRedireccionesAterrizanEnLaPortada` existe para impedirlo. |
| **La pantalla pinta el aviso de modo degradado y ningún bloque gateado** | `GET /api/v1/entitlements` falló o devolvió 403. El gate es fail-closed a propósito: prefiere enseñar de menos. Mira el log: `no se pudieron leer las features del tenant`. |
| **Un 401 en la portada te echa al login, pero en otra pantalla no** | es correcto y deliberado: solo la portada expulsa (`home_handler.go:63-67`), porque es la única sin llamada de negocio propia. |
| **«Credenciales inválidas» con la contraseña correcta** | mira el **log**, que sí distingue: `login rechazado por el System Gate` significa que falta la fila de `wapp.bff` para ese usuario en identity; `credenciales inválidas` es un 401 de verdad. La **respuesta** es ciega a propósito, el log no. Confundirlos costó una tarde el 2026-08-28. |
| **«El servicio de identidad no está disponible»** | no es la contraseña: es el canje apagado en la plataforma (503). Se distingue arriba porque abajo ya no se puede. |
| **429 en el login desde varias IP** | si el BFF está detrás de un proxy y `WAPP_GUARDIAN_TRUSTED_PROXIES` está vacío, **todos comparten la clave del rate-limit** (la IP del proxy). |
| **Un F5 tras guardar reenvía el formulario** | no es un bug: este BFF **no tiene PRG**, repinta sobre el POST. |
| **La portada no ofrece enlace a la consola del cliente** | `WAPP_GUARDIAN_CLIENT_CONSOLE_URL` vacía. Es el default, y hoy en UAT está así. |

### Dónde mirar el log

En local sale por **stdout en texto**. En un despliegue con `WAPP_GUARDIAN_ENV` distinto de `local`
sale en **JSON**, también por stdout, y quien lo recoge es la unidad systemd. ⚠️ En el VPS de UAT
**el log de los servicios de wApp no pasa por `journald`**: las **cinco unidades de aplicación**
(`wapp-cloud`, `wapp-bff`, `wapp-edge`, `wapp-client-console`, `wapp-platform-console`) redirigen con
`StandardOutput=append:/root/source/wApp/logs/<servicio>.log`, y el de esta pieza es `bff.log`.
`journalctl -u wapp-bff` puede salir vacío y eso no significa nada. Y como allí el ambiente es
`local`, ese fichero está en **texto (logfmt)**, no en JSON: una línea típica es
`level=INFO msg="petición web completada" status=200 method=GET path=/login latency=…`.

Antes de pedir una prueba de campo nueva, **mira si el log ya la tiene**: guarda semanas.
