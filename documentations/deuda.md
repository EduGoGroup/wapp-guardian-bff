# Deuda viva de `wapp-guardian-bff`

Lo que está mal o a medias hoy, con `fichero:línea`, qué consecuencia tiene y cómo se cerraría.
Verificado el 2026-08-30 sobre el commit `26e84c9`.

**Marcadores clásicos de deuda en el código: CERO.** Un `grep` de `TODO|FIXME|HACK|XXX|WIP` sobre el
código de producción devuelve **solo falsos positivos**: la palabra española «TODO» en mayúsculas
dentro de prosa (`internal/web/tenantllm_handler.go:75`, `internal/web/signup_handler.go:52,56`,
`internal/web/port.go:56`, `internal/apiclient/auth.go:124`). **La deuda de este repo no está
etiquetada: está en la forma y en la documentación.** Por eso existe este fichero.

---

## 1. Código muerto verificado

### D-BFF-01 · Rama inalcanzable que documenta una protección que ya no existe

`internal/web/auth_handler.go:172`

```go
if c.Request.URL.Path != "/pending" && c.Request.URL.Path != "/logout" {
```

**El `AuthMiddleware` solo se monta sobre el grupo `protected`** (`internal/web/server.go:209-210`),
y **`POST /logout` se registra en el router pelado, en `internal/web/server.go:206`, antes del
grupo**. La comparación con `/logout` **nunca se evalúa a favor**: es rama muerta.

**Consecuencia:** no rompe nada hoy —el logout funciona por su propio handler— pero quien lea esa
línea concluirá que `/logout` está protegido por la aduana, y ese es exactamente el tipo de creencia
que produce el siguiente bug. El propio repo ya lo sabe por otro lado y lo escribe en la lista de
exenciones del test: `internal/web/aduana_test.go:52` («ruta pública del plano de autenticación: su
303 lo escribe `DoLogout`, no el `AuthMiddleware`»). **Nadie borró la rama.**

**Cómo se cierra:** quitar la segunda condición y dejar el comentario que diga por qué `/logout` no
puede llegar aquí. Un test que monte solo el grupo protegido y compruebe que `/logout` no está en él
ya existe de facto en el inventario de rutas.

### D-BFF-02 · Binario obsoleto y ejecutable en la raíz del repo

`guardian-bff` — 18.794.818 bytes, fecha **2026-08-07**, **23 días anterior a `HEAD`**. Está
gitignorado (`.gitignore:10`) y no trackeado, pero está ahí y se puede ejecutar.

**Consecuencia:** quien lo ejecute está corriendo **el BFF anterior a la mudanza**, con las 19 rutas
de negocio vivas, y sacará conclusiones falsas sobre qué sirve esta consola.

**Cómo se cierra:** borrarlo. `bin/` ya existe y está vacío; si hace falta un binario local, que
salga ahí.

---

## 2. Duplicación estructural

### D-BFF-03 · Dos pantallas isomorfas copiadas línea a línea

`internal/web/integrations_handler.go` (514 líneas) e `internal/web/tenantllm_handler.go` (451) son
**la misma máquina con otros nombres**. Mismo esqueleto en el mismo orden —`Show*` → `DoSave*` →
`DoDelete*` → `load` → `render` → `viewFrom*` → `saved*Message` → `map*ReadError` / `map*SaveError`
/ `map*DeleteError`—: compara `integrations_handler.go:131,164,224,268,288,434,451,463,479,504` con
`tenantllm_handler.go:126,159,206,250,287,350,365,394,414,440`. Los dos `DoDelete` son idénticos
salvo identificadores y textos (`integrations_handler.go:224-263` vs.
`tenantllm_handler.go:206-247`): mismo `resolveEntitlements`, mismo gate, mismo `withAuthRetry`,
mismo trato del 401, mismo re-`load` para repintar. Se replica un piso más abajo:
`internal/apiclient/integrations.go:194` (`decodeIntegration`) vs.
`internal/apiclient/tenantllm.go:163` (`decodeTenantLLM`), byte por byte salvo el tipo.

**Consecuencia:** **un arreglo en una no llega a la otra.** Y son las dos pantallas que manejan los
dos secretos del tenant.

**Cómo se cierra:** extraer el esqueleto a un helper genérico dentro de `internal/web` —no a
`wapp-shared`, porque es forma de este BFF— parametrizado por el puerto, la feature y los textos. La
señal de que se hizo bien es que las dos pantallas queden en `Show/DoSave/DoDelete` más un mapa de
mensajes. **Ojo al hacerlo:** las dos difieren en una cosa real y no se puede uniformar —el CRM
admite «deja el secreto que está» y el LLM **exige** la credencial en cada `PUT`, porque la
plataforma trata ese `PUT` como reemplazo completo (`internal/web/port.go:88-96`).

### D-BFF-04 · Dos pantallas se saltan el renderizador y escriben las claves a mano

`internal/web/signup_handler.go:98-110` (`renderSignup`) y `:113-121` (`ShowPending`) llaman a
`c.HTML(status, "base.html", …)` **directamente**, en vez de pasar por `render()`
(`internal/web/render.go:16-19`). Y leen el nonce y el CSRF **por literal**:
`c.GetString("csrf_token")` y `c.GetString("csp_nonce")` (`signup_handler.go:105-106,118-119`).

**Consecuencia:** funciona hoy **solo porque esos literales coinciden** con las constantes del módulo
compartido. Es exactamente el fallo que el comentario de `internal/web/render.go:11-13` dice que se
quiso eliminar: «que esas tres claves las ponga el renderizador y no cada handler es justo el punto
—repetirlas a mano es lo que un día se olvida en una pantalla nueva». **Dos pantallas ya se
olvidaron.** Efecto lateral verificable: esas dos **no siembran `CurrentPath`**, que sí pone el
renderizador; pasa desapercibido solo porque ambas mandan `HideNav: true`.

**Cómo se cierra:** pasarlas por `render()`. Y añadir el candado que falta: un test que derive del
`embed` las páginas servidas y compruebe que **ningún handler llama a `c.HTML` directamente** —de la
familia de los del §4 de [`constitucion.md`](constitucion.md), con guarda anti-cero.

---

## 3. Cobertura y diagnóstico

### D-BFF-05 · El paquete que habla con el mundo está al 0,0 % de cobertura

`internal/apiclient` — 1.011 líneas: el transport, los tipos de error, cuatro clientes de dominio y
el adaptador de la delegación. Su único test,
`TestTenantLLMNoTieneDondeGuardarLaCredencial` (`internal/apiclient/tenantllm_test.go:21`), es un
aserto de tipos **por reflexión**: no ejecuta ni una sentencia de producción.

**Consecuencia:** toda la traducción de estados HTTP a errores con nombre —`ErrUnauthorized`,
`APIError`, `RejectionError`, `StatusCodeOf`, `reasonedStatusError`— se prueba **solo
indirectamente** desde `internal/web` con servidores `httptest`. Un cambio en `transport.go` que
rompa la clasificación de un 403 saldría verde hasta que alguien lo viera en pantalla.

**Cómo se cierra:** tests de tabla sobre `internal/apiclient/transport.go` con `httptest`: por cada
código de estado, qué error sale y qué dice `StatusCodeOf`. Es trabajo mecánico y sube el paquete a
dos dígitos altos.

### D-BFF-06 · `withAuthRetry` devuelve el error VIEJO cuando el refresco falla

`internal/web/auth_handler.go:225-239`, en concreto `:234-236`: si `refreshSession` falla, se
devuelve `err` (el 401 original) y **se descarta `rerr`**.

**Consecuencia:** el llamante ve «401» y nunca sabe si el refresco murió por red o por un refresh
token revocado. Es coherente con lo que necesita `ShowHome`, pero **pierde diagnóstico y no hay
comentario que lo justifique** —a diferencia del resto de errores tragados del repo, que sí lo
tienen (`apiclient/transport.go:120`, `:193-196`, `auth_handler.go:105`).

**Cómo se cierra:** envolver (`errors.Join` o un `%w` doble) para que el 401 siga siendo detectable
con `errors.Is` **y** el motivo del refresco fallido llegue al log.

---

## 4. Configuración y despliegue

### D-BFF-07 · 🔴 `WAPP_GUARDIAN_CLIENT_CONSOLE_URL` está VACÍA en UAT

`internal/config/config.go:176` — `ClientConsoleURL: l.GetString("GUARDIAN_CLIENT_CONSOLE_URL", "")`,
y en el `.env` de UAT la variable **no está puesta**, así que toma el default vacío.

**Consecuencia, y es la peor de esta lista:** la portada del BFF **no ofrece enlace a la consola
donde ahora vive el negocio**. Con la clave vacía, `templates/pages/home.html:63-65` pinta «pídele a
quien administre el despliegue la dirección de la consola del cliente: esta consola no la publica».
Es decir: se acaba de mudar todo el negocio a `wapp-client-console` y **la única aplicación que
podría decirle a la dueña dónde está no se lo dice**. Que el enlace apunte de verdad es trabajo del
**despliegue**, no de la plantilla — y el despliegue no lo ha hecho.

**Cómo se cierra:** poner la variable en el `.env` de UAT con la dirección real de la consola. Hoy
las dos aplicaciones son loopback en puertos distintos (`:8104` y `:8107`) y no hay URL pública, que
es justo el motivo de que el default sea vacío y no un `localhost:8107` cableado —serviría un enlace
roto a todo el que no esté sentado en la máquina—. **La deuda no es el default: es que no hay URL
pública que publicar.** Se cierra de verdad el día que haya una entrada única (proxy o dominio).

### D-BFF-08 · En UAT el BFF corre con `WAPP_GUARDIAN_ENV=local`

El `.env` de UAT lleva `WAPP_GUARDIAN_ENV=local`, y el log vivo lo confirma:
`msg="consola BFF escuchando" addr=:8104 … ambiente=local`. **Las dos consolas hermanas van con
`uat`.**

**Consecuencia, en tres frentes:**
1. Los defaults de endurecimiento **no se aplican**: `CookieSecure` y `HSTSEnabled` derivan de
   `env != "local"` (`internal/config/config.go:148`). En UAT están además puestos explícitamente a
   `false`, y `:8104` sirve el login **por HTTP en claro**, alcanzable desde fuera de la máquina.
2. El log sale en **texto** en vez de JSON (`cmd/guardian-bff/main.go:24-31`), así que el fichero de
   esta pieza no es homogéneo con los de las consolas.
3. Cualquier futura rama que mire `Environment` se comportará como en el portátil de alguien.

**Cómo se cierra:** poner `WAPP_GUARDIAN_ENV=uat` y **a la vez** resolver el TLS, porque con `uat`
la cookie pasa a `Secure` por default y sobre `http://` dejaría de viajar. Los dos pasos van juntos
o el login deja de funcionar.

### D-BFF-09 · Tres variables que el código lee y `.env.example` no documenta

`WAPP_GUARDIAN_TRUSTED_PROXIES`, `WAPP_GUARDIAN_SHUTDOWN_TIMEOUT_SECS` y
`WAPP_ALPHA_TEST_PASSWORD` — las tres se leen en `internal/config/config.go:158,170,174` y ninguna
aparece en `.env.example`.

**Consecuencia:** la que duele es la primera. Gobierna si `ClientIP()` honra `X-Forwarded-For` y con
ello si el rate-limit de `/login` —única defensa anti fuerza bruta de esta consola— se puede
suplantar por cabecera, o si, al revés, todo el tráfico detrás de un proxy comparte una sola clave
de rate-limit. Quien despliegue detrás de un proxy no tiene forma de enterarse de que existe.

**Cómo se cierra:** documentarlas en `.env.example` con su default y su consecuencia. Y, mejor: un
test que derive los literales de entorno de `config.go` y exija que **todos** estén nombrados en
`.env.example` —candado del §4 de [`constitucion.md`](constitucion.md), con guarda anti-cero—.

### D-BFF-10 · Credenciales de prueba escritas en la plantilla

`templates/pages/login.html:22-24` lleva dos correos de cuentas de prueba **fijos en el HTML**, con
`data-password="{{ .AlphaTestPassword }}"`. La contraseña **sí** sale del entorno
(`internal/config/config.go:174`, default vacío); **los correos no**.

**Consecuencia:** el bloque está gateado por `EnableAlphaTestAccounts`, `false` por defecto, y
`.env.example:39-43` lo marca como «atajo deliberadamente inseguro». Es aceptable, pero **es una
superficie que viaja dentro del binario de producción**.

**Cómo se cierra:** sacar también los correos a entorno (una lista CSV), o compilar el bloque solo
bajo build tag. Lo segundo lo saca del binario de verdad.

### D-BFF-11 · Sin techo de cuerpo en ninguna petición

El único `webgin.BodyLimit` del BFF se retiró entero con el import de catálogo, y su hueco está
documentado en `internal/web/server.go:178-190`.

**Consecuencia:** hoy no la tiene —ninguna pantalla acepta subidas— pero **la próxima que las acepte
nace sin tope** salvo que se lo traiga, y tiene que montarlo **antes** del CSRF: el CSRF lee el
formulario para comparar el token y se traga el cuerpo entero.

**Cómo se cierra:** no hace falta cerrarla hoy. Es un aviso para quien añada la siguiente pantalla
con `multipart`.

### D-BFF-12 · Un identificador de Go con acento

`internal/web/integrations_handler.go:365` — `func duraciónLegible(d time.Duration) string`.
Compila, `gofmt` no protesta y hay test (`integrations_test.go:687`). Es una rareza única en el
repo, donde el resto son `cuenta`, `colaAge`… **Consecuencia:** ninguna funcional; hace ruido al
grepear. **Cómo se cierra:** renombrar a `duracionLegible` en el mismo commit que toque ese fichero.

---

## 5. Documentación del propio repo que MIENTE

Esta sección es deuda como cualquier otra: son ficheros que un agente leerá primero y le harán
trabajar sobre una consola que ya no existe.

| Fichero | Qué afirma | Realidad |
|---|---|---|
| `README.md:8-11` | «permite **atender la bandeja de solicitudes** …, **importar el catálogo**…» | falso desde `26e84c9`: esas 13 rutas se retiraron |
| `README.md:4-5` | «Estado: implementado (Plan 021, T1–T4; + Plan 040 · Ola 2)» | dos planes atrás |
| `README.md:99-113` | dibuja `internal/web/security.go`, `csrf.go`, `ratelimit.go`, `deadline.go`, `internal/apiclient/identity.go` y `exchange.go` | **los seis NO EXISTEN**: subieron a `wapp-shared/web` y `wapp-shared/iam` |
| `README.md:99` | «main.go → `web.Run`» | llama a **`bootstrap.Run`** (`cmd/guardian-bff/main.go:39`); el paquete `internal/bootstrap` no aparece en ninguno de los dos árboles documentados |
| `CLAUDE.md` (el anterior a esta documentación) | listaba `internal/apiclient/catalogimport.go` y omitía `internal/bootstrap` y la dependencia `identity-shared` | corregido: el `CLAUDE.md` de hoy solo apunta a `documentations/` |
| `docs/contrato-api-publica.md` §3 | lista nueve endpoints «de negocio usados»; **ocho ya no son de este BFF** | de los seis que el BFF **sí** llama para configuración (`tenant-variables`, `integrations`, `integrations/outbox`, `tenant-llm`, `signup`, `auth/exchange`) **aparecen CERO**. Como referencia para un cliente móvil, hoy describe `wapp-client-console` |

**Cómo se cierra:** reescribir `README.md` como portal corto que apunte a `documentations/`, y
**mover o retirar** `docs/contrato-api-publica.md` —su contenido describe la otra consola, así que o
se traslada allí o se reescribe con las diez llamadas reales de [`contratos.md`](contratos.md)—.

Lo que **sí está al día** y conviene no romper: los comentarios de `internal/web/server.go`, que son
la mejor documentación viva del repo, y `.env.example`, que **sí** documenta
`WAPP_GUARDIAN_CLIENT_CONSOLE_URL` con su motivo.

---

## 6. Deuda que NO es de este repo pero nació aquí

**El aviso del perfil `passive`.** Hasta el `26e84c9` este BFF pintaba un aviso de que el perfil
`passive` **todavía no entrega la privacidad que promete** —el filtrado de entrantes en el Edge no
existe— y tenía un test guardián que lo vigilaba
(`TestDashboardNoPrometeLaPrivacidadQueAunNoEntrega`). Con la mudanza, la pantalla de sesiones se
fue a `wapp-client-console`. **Si el aviso y su test no llegaron allí, la garantía se perdió al
mudarse.** **NO VERIFICADO** desde este repo — no se puede: el otro repo no está al lado en un clon
suelto. Quien tenga los dos, que lo compruebe; es la deuda más peligrosa que dejó la mudanza.

---

## 7. Lo que NO es deuda, aunque lo parezca

Para que nadie lo «arregle»:

- **Cuatro panics deliberados en el arranque** (`internal/web/server.go:87`, `:131`,
  `internal/web/handlers.go:58`, más el abort de `internal/bootstrap/server.go:33-35`). Es
  fail-closed: el BFF prefiere no nacer a nacer con una allowlist de proxies malformada o un
  verificador de cero claves.
- **Tres errores tragados con motivo escrito**: `internal/apiclient/transport.go:120` (cuerpo
  ilegible ⇒ mensaje vacío), `:193-196` (`drainClose`) y `internal/web/auth_handler.go:105` (logout
  upstream fallido ⇒ se cierra localmente igual).
- **Que `resolveEntitlements` no devuelva error nunca.** Es el fail-closed del gate; la portada usa
  la variante `WithError` **a propósito**, porque es la única pantalla sin llamada de negocio propia.
- **Que no haya PRG.** Es el comportamiento de este BFF; el que lo tiene es `wapp-client-console`.
- **El bug histórico 0001, «la sesión expira sin intentar refresh», está RESUELTO** (Plan 033) y
  **hoy sigue cerrado**: `internal/web/auth_handler.go:137-158` intenta el refresco cuando quedan
  menos de dos minutos de `exp` —lo que incluye un token ya vencido— y solo cae al logout si **no
  hay refresh token** o si el refresco devuelve 401. Lo vigila
  `TestAuthMiddlewareExpiredAccessRefreshesAndContinues` (`internal/web/auth_refresh_test.go:76`),
  con sus tres hermanos para el 401, el fallo transitorio y el single-flight.
