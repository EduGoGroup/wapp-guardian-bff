# Constitución de `wapp-guardian-bff`

Las reglas de esta pieza. Si algo que vas a escribir contradice este documento, no lo escribas:
o el cambio está mal, o hay que cambiar antes esta constitución con su motivo.

---

## 1. Lo que hay que saber del ecosistema para no equivocarse aquí

Este repo se clona solo. Estos invariantes son del ecosistema wApp entero y **no están enlazados en
ningún sitio desde aquí a propósito**: se repiten porque son los que alguien puede violar
trabajando en esta pieza.

**INV-1 · zero-knowledge.** La nube nunca accede a credenciales ni llaves privadas del cliente. Lo
que protege son **llaves**, no el contenido de negocio: los pedidos, los contactos y los mensajes
**sí** suben a la nube, a propósito. El BFF es nube: puede pintar negocio, **nunca** tocar llaves.

**INV-2 · doble llave.** Son dos llaves distintas y viven en sitios distintos:
- La **DEK** descifra el almacén de `whatsmeow` en el equipo del cliente. **La custodia el cliente y
  jamás cruza ningún contrato.** Ni aparece en este repo ni puede aparecer.
- El **Lease** autoriza a operar y **lo emite y lo revoca el servidor**: es el kill-switch anti-clon.
  El BFF no lo emite, no lo valida y no lo ve.

**INV-3 · sin Redis ni broker en el Edge.** La concurrencia se resuelve con Go. Aquí la
consecuencia directa: el único estado del proceso es en memoria y por proceso —el
`sharedweb.KeyedRateLimiter` (`internal/web/server.go:99-107`), el `RefreshGroup` de single-flight
(`internal/web/handlers.go:67`) y el árbol de plantillas—; **no metas una cola, ni un caché
compartido, ni una sesión en Redis**.

**INV-4 · copia-adaptación, nunca dependencia.** Se copió código de otro producto (EduGo) y se
adaptó al espacio de nombres de wApp. **Está prohibido importar un repo `edugo-*`.** De aquí vienen
el patrón de BFF endurecido (CSP con nonce, rate-limit, cookies HttpOnly) y el `.golangci.yml`,
que declara su origen en su primera línea. Se comprueba con
`grep -rn 'edugo-' --include='*.go' . | grep -v EduGoGroup/wapp-` → tiene que dar 0.

**INV-5 · el código compartido interno vive en `wapp-shared`**, un monorepo multi-módulo con
releases por módulo (tags `<modulo>/vX.Y.Z`). Este BFF consume **seis** de sus módulos (§3). Cuando
algo se repita entre las tres consolas, **sube a `wapp-shared`**; no lo copies aquí.

**⚠️ La excepción real, y hay que decirla.** `go.mod:6` declara
`github.com/EduGoGroup/identity-shared/auth v0.3.1`, que es del microecosistema **identity**, no de
wApp. No es `edugo-*`, pero es la única dependencia de código externa al ecosistema wApp, y el
`CLAUDE.md` viejo del repo la omitía al enumerar las dependencias. Si añades una segunda, escríbelo.

---

## 2. Los invariantes propios de esta pieza

| # | Invariante | Cómo se comprueba | Test que lo vigila |
|---|---|---|---|
| B1 | **El BFF sirve exactamente 20 rutas y NINGUNA es de negocio.** | Deriva del router y compara conjuntos (§4) | `TestElInventarioDeRutasEsExactamenteElDeREQ10` (`internal/web/inventario_test.go:100`) y `TestElRepartoPorFamiliaEsElDeREQ10` (`:145`) |
| B2 | **Ninguna ruta protegida se sale de las tres pantallas técnicas.** `/variables`, `/integrations`, `/tenant-llm` y lo que cuelgue de ellas. | Aserto de propósito sobre cada ruta clasificada como técnica | `TestNingunaRutaProtegidaEscapaDelPlanoTecnico` (`inventario_test.go:180`) |
| B3 | **El navegador nunca ve el token.** El par access+refresh va cifrado dentro de la cookie HttpOnly `wapp_guardian_session`. | La respuesta del login trae `Set-Cookie` con `HttpOnly` y el HTML no contiene el token | `TestLoginOKSetsHttpOnlyCookie` (`auth_test.go:108`) |
| B4 | **El BFF NO verifica la firma del JWT.** Solo parsea sin verificar para leer el `exp` (`internal/web/session.go:20`, `ParseUnverified`). La autoridad es la API. | Solo hay un parser y es `jwt.NewParser().ParseUnverified` | — (invariante de lectura; no hay test de ausencia) |
| B5 | **Ningún secreto vuelve al HTML.** El secreto de firma del CRM y la credencial del LLM **entran** por su `Save*` y **no tienen campo de salida en el DTO** (`internal/web/port.go:56`, `:88`). | Se pinta un booleano (`SecretSet`, `KeySet`), nunca el valor | `TestIntegrationsSecretNeverReachesHTMLOrLog` (`integrations_test.go:289`), `TestTenantLLMKeyNeverReachesHTMLOrLog` (`tenantllm_test.go:227`) |
| B6 | **El gate por feature es server-side y fail-closed.** Sin la feature el bloque **no se emite en el HTML**; ante un fallo o un 403 la vista cero deja `Has` en `false` para todo (`internal/web/entitlements.go:75-83`). | Nunca se esconde con CSS ni JS —la CSP no admite `'unsafe-inline'`— | `TestPortadaGateOmitsSectionWithoutFeature` (`entitlements_test.go:87`), `TestPortadaDegradesWhenEntitlementsFail` (`:119`), `TestLasFeaturesDeLasPantallasTecnicasCortanTambienEnGo` (`funcmap_test.go:201`) |
| B7 | **Todo POST protegido sin sesión REDIRIGE (303 a `/login`), no contesta 4xx.** Las tres exenciones (`/login`, `/signup`, `/logout`) se declaran con su motivo. | Recorre `router.Routes()`, con prueba de autoría frente al 403 del CSRF y guarda anti-cero | `TestTodoPOSTProtegidoSinSesionRedirigeAlLogin` (`aduana_test.go:87`) |
| B8 | **`UpstreamTimeout` va por debajo de `WriteTimeout`**, para que el modo degradado alcance a pintarse (20 s vs 30 s por defecto). | Compara los dos plazos de `config.Config` | `TestNoSeTocaronLosPlazosGenerales` (`internal/config/config_test.go`) |
| B9 | **El BFF prefiere no nacer a nacer mal (fail-closed en el arranque).** Cuatro puntos de pánico o abort: allowlist de proxies inválida (`server.go:83-88`), plantillas que no compilan (`server.go:129-132`), delegación mal configurada (`handlers.go:57-62`) y JWKS de identity que no responde (`bootstrap/server.go:33-35`). | Se prueba montando el router con configuración inválida | `TestNewRouterPanicsOnInvalidTrustedProxies` (`proxy_test.go:51`) |
| B10 | **La cookie custodia SIEMPRE el Context Token, nunca el Identity Token.** Con la delegación encendida el Identity Token muere dentro de `wapp-shared/iam`; si acabara en la cookie, el `tenant_id` desaparecería sin aviso. | Se inspecciona la cookie tras un login delegado | `TestDelegacionLoginCanjeaYCustodiaSoloElContextToken` (`delegation_test.go:247`) |
| B11 | **El BFF no tiene persistencia ninguna.** Sin driver SQL en `go.mod`, sin `database/sql`, sin migraciones, sin esquema. Está escrito en `internal/config/config.go:3-5`. | `grep -rn 'database/sql' --include='*.go' .` → 0 | — (invariante por ausencia, sin candado) |
| B12 | **Ninguna plantilla enlaza a una ruta que este BFF no sirve.** | Extrae los `href`/`action` literales del embed y los contrasta con `router.Routes()`, con guarda anti-cero | `TestNingunaPlantillaEnlazaARutaQueElBFFNoSirve` (`inventario_test.go:385`) |

---

## 3. Tecnología y versiones reales

**Go 1.26.5** — declarado en `go.mod:3` y **fijado también en el Makefile** (`GO_VERSION := 1.26.5`,
`Makefile:15`). El lint va pinado a `golangci-lint v2.12.2` (`LINT_VERSION`, `Makefile:16`); v2.4.0 iba con go1.25 y
**no puede cargar** un `go.mod` que apunta a go 1.26 (sale `exit 3, can't load config`).

Dependencias directas (`go.mod:5-14`) y para qué sirve cada una, verificado en el código:

| Módulo | Versión | Para qué |
|---|---|---|
| `github.com/gin-gonic/gin` | v1.10.0 | el router y el motor HTTP |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | **solo** `ParseUnverified` para leer el `exp` (`internal/web/session.go:11,20`) |
| `github.com/EduGoGroup/wapp-shared/web` | v0.2.0 | CSP+nonce, CSRF, rate-limit, deadline, cookies, renderer, sesión, proxies |
| `github.com/EduGoGroup/wapp-shared/ui` | v0.4.1 | las tres hojas CSS compartidas (`ui.GetCSS`) |
| `github.com/EduGoGroup/wapp-shared/iam` | v0.1.0 | los dos saltos de la delegación: identity + canje |
| `github.com/EduGoGroup/wapp-shared/auth` | v0.5.0 | el tipo `sharedjwt.Claims` |
| `github.com/EduGoGroup/wapp-shared/config` | v0.3.0 | lector de entorno con prefijo `WAPP_` |
| `github.com/EduGoGroup/wapp-shared/logger` | v0.2.0 | logger por defecto |
| `github.com/EduGoGroup/identity-shared/auth` | v0.3.1 | verificador JWKS de Identity Tokens (la excepción del §1) |

**Frontend: plantillas Go, cero framework.** `html/template` embebido (`//go:embed templates`,
`internal/web/server.go:27-28`), un layout maestro `base.html` que ejecuta el fragmento por el helper
`yield` (`server.go:114-124`), CSS mismo-origen embebido (`//go:embed static/css/app.css`) más tres
hojas de `wapp-shared/ui`. **Hay un único `<script>` en todo el repo**, con nonce CSP, y va detrás
de un flag: el autocompletado del selector Alpha en `templates/pages/login.html`. Sin htmx, sin
React, sin CDNs — y no los metas: la CSP es estricta y no admite `'unsafe-inline'`.

**Base de datos: ninguna** (B11). **gRPC: ninguno** — no hay `google.golang.org/grpc` en `go.mod`
(el `protobuf` indirect lo arrastra gin), ni ficheros `.proto`. **CLI: ninguno** — el único `main`
no llama a `flag.Parse()` ni lee `os.Args`.

---

## 4. 🔒 La norma de esta casa: el candado se DERIVA del router y lleva guarda anti-cero

Esto es lo que este repo tiene y los demás no, y **es una norma, no una curiosidad**. Todo
inventario que este repo declare —rutas, plantillas, helpers de plantilla, constantes de ruta— se
vigila con un test que cumple las **cuatro** reglas siguientes. Cuando añadas un inventario nuevo,
cópialas.

**Regla 1 · El sujeto se DERIVA del código, no se escribe a mano.** El test recorre
`router.Routes()`, o el directorio del `embed`, o el AST del paquete. Una lista escrita a mano nace
con fecha de caducidad y no ve lo que se añada mañana.

**Regla 2 · El aserto es de IGUALDAD de conjuntos, jamás una lista negra.** Está razonado en
`internal/web/inventario_test.go:21-26`, y es una lección medida en este mismo repo: un test que
comprobara «ninguna ruta contiene `/intakes`» pasaría tan campante con `/solicitudes` registrada —el
nombre en castellano de la MISMA pantalla—. Una lista negra solo sabe decir que no volvió lo que ya
se fue; lo que hay que impedir es **que entre lo que nadie ha declarado**. Sobra ⇒ rojo. Falta ⇒
rojo.

**Regla 3 · Cada elemento va CLASIFICADO, y los tamaños van APARTE de la lista.** Las cuatro
familias son `aduana`, `portada`, `técnica` e `infraestructura` (`inventario_test.go:36-47`), y
**no hay familia «negocio»: esa ausencia es el invariante**. El reparto vive en un mapa separado
(`inventario_test.go:91-96`):

```go
var tamanoPorFamilia = map[familiaDeRuta]int{
	familiaAduana:          6,
	familiaPortada:         1,
	familiaTecnica:         8,
	familiaInfraestructura: 5,
}
```

**6 + 1 + 8 + 5 = 20.** Los números van fuera del mapa a propósito: sin ellos, colar una pantalla de
negocio bajo la etiqueta «técnica» —que es la forma que tendría el error de buena fe— dejaría el
conjunto idéntico y el test verde. Con ellos hay que tocar también el número, que es donde se ve. Y
la suma se comprueba contra `len(inventarioDeRutas)`, porque una suma escrita a mano se desincroniza
de la lista que resume.

**Regla 4 · GUARDA ANTI-CERO, siempre.** Un aserto universal («todas cumplen X») lo satisface el
conjunto vacío, y ese es el modo de fallo real: el día que el sujeto desaparezca, el test se queda
**verde midiendo cero**. Por eso, `inventario_test.go:105-107`:

```go
if len(registradas) == 0 {
	t.Fatal("router.Routes() vacío: el test no está midiendo nada")
}
```

La misma guarda está en `inventario_test.go:198-200` («no queda ninguna ruta técnica: este test dejó
de tener sujeto»), `inventario_test.go:254-256`, `inventario_test.go:415-417`,
`rutas_declaradas_test.go:50-55` y `aduana_test.go:126-128`. **Un aserto de ausencia sin guarda
anti-cero no vale nada**, y el fallo gemelo —escribir el aserto con un sujeto que no existe, con lo
que se acaba midiendo que nadie llama a rutas inexistentes— sale verde a la primera con el candado
roto.

Los cinco candados vivos, para copiarlos:

| Test | `fichero:línea` | De dónde deriva su sujeto |
|---|---|---|
| `TestElInventarioDeRutasEsExactamenteElDeREQ10` | `internal/web/inventario_test.go:100` | `router.Routes()` |
| `TestElInventarioDePlantillasEsExactamenteElDeREQ10` | `internal/web/inventario_test.go:249` | `fs.ReadDir` sobre el `embed` |
| `TestNingunaPlantillaEnlazaARutaQueElBFFNoSirve` | `internal/web/inventario_test.go:385` | los `href`/`action` literales del `embed` × `router.Routes()` |
| `TestNingunaConstanteDeRutaNombraUnaRutaFantasma` | `internal/web/rutas_declaradas_test.go:49` | **el AST del paquete**, resolviendo concatenaciones a punto fijo |
| `TestNingunHelperDelFuncMapSeQuedaSinConsumidor` | `internal/web/funcmap_test.go:84` | las claves del `FuncMap` × las plantillas del `embed` |

El del AST nació de una mutación que ningún otro test mataba: un invariante que vive dentro de una
**cadena de texto** no lo compila nadie, y una constante que nombraba una ruta ya retirada seguía
compilando, con `vet` en cero y la suite verde.

---

## 5. Convenciones de código

- **Español en los identificadores nuevos que describen reglas del dominio** (`duraciónLegible`,
  `cuelgaDeAlguna`, `familiaDeRuta`) y **español en TODOS los nombres de test y en los mensajes de
  error**. El mensaje de un test dice qué hacer, no solo qué falló: mira `inventario_test.go:119-121`
  («…si es una pantalla de NEGOCIO no tiene familia aquí, su casa es `wapp-client-console`»).
  ⚠️ `duraciónLegible` (`internal/web/integrations_handler.go:365`) lleva **acento en el nombre de la
  función**: compila y `gofmt` calla, pero es un caso único en el repo; **no lo repitas**.
- **Los comentarios documentan la DECISIÓN, y cuando algo se retira se deja la nota en su sitio
  exacto**: qué rutas se fueron, a dónde, con qué tarea y qué test lo vigila (`server.go:178-197`,
  `:235-244`, `:246-256`, `:265-281`, `:283-296`). Es la mejor documentación viva del repo; si
  retiras algo, sigue el patrón.
- **Puertos segregados por consumidor** en `internal/web/port.go`: `Authenticator`,
  `EntitlementsReader`, `HomeAPI`, `TenantVariablesManager`, `IntegrationsManager`,
  `TenantLLMManager`, y `APIPort` como composición. Gracias a eso, retirar la bandeja no tocó ni un
  handler ajeno. Una pantalla nueva trae **su** interfaz.
- **Toda pantalla renderiza por `render()`** (`internal/web/render.go:16-19`), que delega en
  `webgin.NewRenderer("base.html")`. El renderizador siembra el nonce, el token CSRF, el estado de
  sesión y el `CurrentPath`. **No llames a `c.HTML` directamente** (dos pantallas lo hacen y es
  deuda; ver `deuda.md`).
- **Los nombres de cookie son de esta consola, no del módulo**: `wapp_guardian_session` y
  `wapp_csrf` (`internal/web/policy.go:19-20`). Se pasan como parámetro justamente para que dos
  consolas del mismo navegador no se pisen la cookie.
- **Los errores del upstream se traducen a mensajes legibles y NO se filtra el detalle**
  (`entitlementsNotice`, los `map*Error` de cada handler). La respuesta del login es ciega a
  propósito; **el log no lo es**: distingue el 401 de credenciales del 403 del System Gate
  (`auth_handler.go:77-85`), porque fundirlos deja ciego a quien diagnostica.

---

## 6. Trampas conocidas — lo que un agente hace mal aquí si nadie se lo dice

**T1 · Creer que el BFF administra el negocio.** Es lo que dicen el `README.md` de la raíz del repo,
su `docs/contrato-api-publica.md` y varias fichas del manual del ecosistema. **Todos están
caducados.** Aquí no hay sesiones, ni bandeja, ni flujos, ni import de catálogo desde el `26e84c9`.

**T2 · Añadir una ruta sin clasificarla.** El inventario del §4 se pone rojo, y con razón: si es de
negocio, **no tiene familia aquí**; su casa es `wapp-client-console`.

**T3 · Retirar una ruta y dejar viva la pieza que la nombraba.** Cuando se fue la bandeja, con ella
tuvieron que irse el despachador de plazos por ruta y el cliente HTTP de inferencia de 55 s; cuando
se fue el import, el `webgin.BodyLimit` de 4 MiB. Dejarlos habría sido código muerto **en el camino
crítico de todas las peticiones**, comparando en cada una un `FullPath()` contra una ruta
inexistente. Y hay una segunda búsqueda que se olvida: **los tests que se quedan y usaban esa ruta
como testigo** siguen verdes midiendo un 404.

**T4 · No hay techo de cuerpo.** El único `BodyLimit` se fue entero con el import de catálogo
(`server.go:178-190`). Hoy ninguna pantalla acepta subidas; **la que vuelva a aceptarlas tiene que
traerse su propio tope**, y montarlo **antes** del CSRF: el CSRF lee el formulario para comparar el
token y con eso se traga el cuerpo entero, así que un tope puesto después llega tarde.

**T5 · El orden de los middlewares importa y no es negociable.** `SetTrustedProxies` → `Recovery` →
`SlogLogger` → `SecurityHeaders` → `CORS` → `RateLimit` → *[estáticos y `/healthz`]* → `CSRF` →
*[aduana pública]* → `AuthMiddleware` → `RequestDeadline` (`server.go:76-224`). Los estáticos y la
sonda van **antes** del CSRF para no ensuciar respuestas cacheables con una cookie de token.

**T6 · No hay PRG en este BFF.** Los tres POST de negocio técnico **repintan sobre el propio POST**
(200/400/403) y conservan lo tecleado cuando algo falla (`tenantvariables_handler.go:132`). Un F5
tras guardar **reenvía el formulario**. `wapp-client-console` hace lo contrario (PRG universal), así
que **mudar una pantalla de aquí a allí cambia lo que responden sus acciones**: el 303 borra el
400/403. Lo único que redirige aquí es el plano de autenticación.

**T7 · `WAPP_GUARDIAN_TRUSTED_PROXIES` vacío es la postura correcta, y tiene consecuencia.** Vacío
significa que `ClientIP()` **ignora** `X-Forwarded-For` y usa la IP de la conexión; eso blinda el
rate-limit de `/login` —única defensa anti fuerza bruta— contra la suplantación por cabecera. Si
pones el BFF detrás de un proxy sin declararlo, **todas las peticiones comparten clave de
rate-limit**. Y esa variable **no está en `.env.example`**.

**T8 · La ruta `GET /` no se puede borrar aunque parezca que no pinta nada.** Es el destino de
**tres** redirecciones del plano de autenticación: `DoLogin` tras autenticar, `ShowLogin` con sesión
válida, y el `AuthMiddleware` al confirmarse el tenant viniendo de `/pending`. Borrarla convierte un
login correcto en un 404. Lo vigila `TestLasTresRedireccionesAterrizanEnLaPortada`
(`home_test.go:222`).

**T9 · El 401 solo expulsa en la portada, y es deliberado.** `resolveEntitlements` se traga el 401
porque es una consulta accesoria; la portada usa `resolveEntitlementsWithError` y decide expulsar
(`home_handler.go:63-67`), porque al retirarse el dashboard se quedó sin llamada de negocio. **No
"unifiques" las dos.** Cualquier otro fallo —red, 5xx, 403— no expulsa: degrada y cierra el gate.

**T10 · Un `RequireFeature` de la plantilla no autoriza nada.** El gate del BFF decide lo que **se
pinta**; lo que **se puede** lo corta el middleware de la plataforma con un 403 y
`{"error":"feature_not_enabled"}`. Esconder un botón nunca sustituye a ese corte.

**T11 · El binario suelto de la raíz miente.** Hay un `guardian-bff` de 18,7 MB con fecha
**2026-08-07** en la raíz del repo, gitignorado (`.gitignore:10`) y no trackeado. Es **anterior a la
mudanza**: quien lo ejecute está corriendo el BFF viejo, con las rutas de negocio vivas.

**T12 · Un PR no valida nada.** `.github/workflows/ci.yml` es `on: workflow_dispatch`. El gate real
es `make ci-local`. Y un `rc=0` cuenta igual un `--- SKIP` que un `--- PASS`: **cuenta los SKIP**
(hoy son 0; ver `operacion.md`).
