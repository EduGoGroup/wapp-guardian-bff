# CLAUDE.md — wapp-guardian-bff

> Orientado al agente LLM. Lee también:
> - `../../docs/piezas/04-consola-bff-guardian.md` (pieza completa con ADRs)
> - `../../CLAUDE.md` (raíz del monorepo wApp)

---

## Qué es esta pieza

**Consola/BFF** — el terminal web de operación de negocio del cliente wApp, y el **primer consumidor real** de
la **API pública** (`/api/v1`, `:8103`) como **implementación de referencia** (Plan 021). Es un front endurecido
(Go / Gin, SSR con `html/template`) **sin lógica de dominio**: valida sesión, aplica hardening y habla **solo
REST** con la Plataforma Cloud (Pieza 03) —y, si la delegación está encendida, también con identity-api
para las credenciales (ver más abajo)—. Custodia el JWT **server-side** en cookie HttpOnly (el navegador nunca
ve el token). **No** empareja teléfonos ni custodia DEK.

## Responsabilidad (lo IMPLEMENTADO — Plan 021 MVP + Plan 040 · Ola 2)

| Área | Qué hace | Endpoint `/api/v1` consumido |
|---|---|---|
| Login/sesión | Login server-side, JWT en cookie HttpOnly, refresh+reintento, logout | `auth/{login,refresh,logout}` (plataforma) o identity + `auth/exchange` si la delegación está encendida |
| ~~Sesiones~~ | 🔴 **RETIRADO (Plan 047 · T2.1)** — se administran en `guardian/wapp-client-console` | — |
| ~~Enviar mensaje~~ | 🔴 **RETIRADO (Plan 047 · T2.1)** — se administra en `guardian/wapp-client-console` | — |
| Portada | Índice de lo que ESTA consola conserva: plan/capacidades + accesos, y el aviso de dónde se administran ahora las sesiones | `GET entitlements` |
| ~~Editar menú/encuestas~~ | 🔴 **RETIRADO (Plan 047 · T6.6)** — flujos y disparadores se administran en `guardian/wapp-client-console` (`/flujos` y `/disparadores`) | — |
| ~~Bandeja de solicitudes~~ | 🔴 **RETIRADO (Plan 047 · T7.7)** — el listado, el detalle y sus ocho acciones se administran en `guardian/wapp-client-console` (`/solicitudes`). Con las diez rutas se fueron el despachador de plazos por ruta y el cliente HTTP de inferencia del `apiclient` | — |
| Plan y capacidades | Pinta el plan del tenant y un chip por feature efectiva, y **gatea qué secciones se emiten** (ver abajo) | `GET entitlements` |

### 🔴 Lo que se RETIRÓ de aquí (Plan 047 · T2.1)

`GET /` era el **dashboard de sesiones**; hoy es la **portada**. Se fueron tres cosas y su código
entero: la tabla de teléfonos vinculados, el `<select>` de perfil (`POST /sessions/:id/profile`) y el
formulario de envío (`POST /send`), con `apiclient/dashboard.go`, `web/dashboard_handler.go` y sus
tests. Viven ahora en **`guardian/wapp-client-console`**.

Tres cosas que conviene saber antes de tocar esto:

1. **`GET /` NO se fue con ellas.** Es el destino de TRES redirecciones del plano de autenticación
   (`DoLogin` tras autenticar, `ShowLogin` con sesión ya válida, `AuthMiddleware` al confirmarse el
   tenant viniendo de `/pending`). Borrarla convierte un login correcto en un 404, y hay un test que
   recorre la cadena entera para impedirlo (`TestLoginSigueAterrizandoEnLaPortada`).
2. **La portada expulsa al login ante un 401 persistente**, y esa línea de `ShowHome` es deliberada:
   quien expulsaba antes era el listado de sesiones (la llamada de negocio de la página). Al retirarse,
   la única llamada que queda son las capacidades, y `resolveEntitlements` traga el 401 a propósito
   («es accesoria»). Por eso existe `resolveEntitlementsWithError`: la portada lee el error y decide.
3. **`DashboardClient` se renombró a `EntitlementsClient`.** Su único método siempre fue
   `GetEntitlements`; el nombre venía de quién lo usaba, no de qué hacía.

> **Diferido (NO implementado):** subida de contenido/PDF, campañas, plantillas/contactos/segmentos, editor
> visual de nodos, `tenant-content`/`media`. Ver `../../docs/plans/021-cliente-web-referencia/` (REQ-E5).

## La delegación de identidad (identity Plan 003 · Ola 3)

El BFF tiene **dos destinos posibles para la autenticación**, y cuál se usa lo decide una env var:

- **`WAPP_IDENTITY_URL` vacía (default)** — flujo legacy: credenciales contra la API pública de wApp.
- **Con valor** — las credenciales viajan a **identity-api (`:8200`)** con el system **`wapp.bff`**, y el
  **Identity Token se canjea al instante** por un Context Token de wApp (`POST /api/v1/auth/exchange`
  de la plataforma). El canje exige que la plataforma tenga su `WAPP_IDENTITY_JWKS_URL`, o responde 503.

**La regla que no se negocia: la cookie custodia SIEMPRE el Context Token.** El Identity Token dice
quién eres y no tiene claims de negocio (no puede tenerlas); el Context Token dice qué puedes hacer en
wApp y es de donde sale el `tenant_id`. Por eso el Identity Token no se persiste —muere dentro de
`wapp-shared/iam`, que es quien hace los dos saltos; `apiclient.DelegatedAuthenticator` solo lo adapta
al puerto— y `parseAccessClaims` (`internal/web/session.go`) sigue leyendo el tenant sin cambio
alguno. Si alguna vez el token de identity acabara en la cookie, el tenant desaparecería sin más
aviso: hay un test que lo vigila.

Los dos clientes —`apiclient.Client` y `apiclient.DelegatedClient`— cumplen el mismo puerto `APIPort`,
así que la cascada de refresco (proactiva a 2 min del vencimiento, pasiva ante 401) es la misma de
siempre y ningún handler sabe quién autentica. El logout delegado revoca en identity **solo la sesión
de esta aplicación**: la del Edge sobrevive (modelo Google).

## El gate por feature (Plan 040 · Ola 2) — el patrón que copiarán 041/042

Cada pantalla pide `GET /api/v1/entitlements` en su render (`resolveEntitlements`,
`internal/web/entitlements.go`) y mete la vista en los datos de plantilla bajo la clave
`Entitlements` (en la portada, `internal/web/home_handler.go`). Con eso, una sección que dependa de
una capacidad se envuelve así —y **así es como se añaden las secciones nuevas**:

```
{{ if $.Entitlements.Has "<feature>" }} … {{ end }}
```

Dos reglas que no se negocian al tocar esto:

1. **El gate es server-side, en la PLANTILLA.** Sin la feature, el bloque **no se emite en el HTML**.
   Nunca lo escondas con CSS (`display:none`) ni con JS: lo no contratado no debe estar ahí para que
   alguien lo destape con el inspector, y además la CSP no admite `'unsafe-inline'`. En la portada
   (`templates/pages/home.html`) están gateados los accesos a integraciones (`crm_bridge`), al
   proveedor de IA (`api_llm`) y el bloque del clasificador (`llm_intent`). El acceso al import de
   catálogo iba gateado por `catalog_import` hasta el Plan 047 · T8.5: la pantalla se mudó a
   `wapp-client-console` y en su sitio quedó un aviso de mudanza SIN gate, igual que los de sesiones,
   flujos y solicitudes.
2. **Fail-closed.** `resolveEntitlements` no devuelve error nunca: ante un fallo o un `403` devuelve
   la vista cero y `Has` responde `false` para todo (`internal/web/entitlements.go`). La pantalla
   sigue sirviendo con un aviso de modo degradado, y con todos los bloques gateados fuera. No añadas
   una rama que abra el gate cuando las features no se pudieron resolver.
   La ÚNICA excepción es el 401 en la portada, y no abre ningún gate: echa al login
   (`resolveEntitlementsWithError`, ver arriba).

Y el alcance, para no confundirse: este gate decide lo que se **pinta**. Lo que se **puede** lo
resuelve el middleware `RequireFeature` de la plataforma
(`cloud/wapp-cloud-platform/internal/entitlements/middleware.go`), que corta con `403` y
`{"error":"feature_not_enabled","feature":"<clave>"}` en cada llamada — la consola no es la autoridad
de autorización, y esconder un botón nunca sustituye a ese corte.

## Decisiones clave (Pieza 04 / ADRs)

1. **El QR NO pasa por esta web.** En EduGo la consola era el terminal del QR (SSE).
   En wApp el QR es local en el Edge (terminal o web loopback de onboarding; **no hay
   systray**). El endpoint `/sessions/:id/stream` y la excepción de `WriteTimeout` del
   BFF de EduGo se eliminan.
2. **Sin lógica de WhatsApp ni material criptográfico.** La nube (Pieza 03) arma
   el payload completo (ADR-0005); el Edge es despachador.
3. **Media en la nube, no en el Edge.** PDF/archivos grandes viajan como URL
   prefirmada de corta vida al Edge (ADR-0005).
4. **Datos de negocio en la nube** (ADR-0009): la consola nunca es fuente de verdad.

## Qué se conserva de edugo-messaging-web (copia y adaptación, ADR-0004)

- CSP estricto con nonce por petición (sin `'unsafe-inline'`).
- Rate-limiter por IP/usuario.
- Cookies HttpOnly + SameSite para el JWT.
- Auth server-to-server contra la **API pública de wApp** (`/api/v1/auth/*` en Pieza 03); validación del token
  en el BFF por **parse-unverified + `exp`** (la API es el gate real, no se comparte el secreto JWT).
- CSS compilado embebido (sin CDNs externas), con **design system Material Design 3** propio (tokens en
  `internal/web/static/css/app.css`).
- **Deps:** `gin` + `github.com/EduGoGroup/wapp-shared/{logger,config,auth,ui,web,iam}` (repo de wApp en la
  org EduGoGroup; **no** es `edugo-shared`). **Cero import `edugo-*`** (ADR-0004). El CSP/CSRF/rate-limit
  ya no son código de este repo: viven en `wapp-shared/web`, que los reconcilió con los de la consola de
  operadores (Plan 047 · Ola 0.5).

## Qué se elimina o cambia respecto a edugo-messaging-web

- Relay SSE del QR y endpoint `/sessions/:id/stream` — **eliminados** (el QR es local del Edge).
- Multi-escuela / switch-context de EduGo — **eliminado** (el tenant sale del token, INV-8).
- `WriteTimeout` — **SÍ se fija** (30s): sin streams de larga vida, conviene endurecerlo (al revés que EduGo).

## Estructura del proyecto (real, tras Plan 040 · Ola 2)

```
cmd/guardian-bff/main.go   — punto de entrada (config + logger + web.Run en :8104)
internal/config/           — Config desde env (WAPP_GUARDIAN_*, WAPP_PUBLIC_API_BASE)
internal/apiclient/        — clientes HTTP (Bearer server-side): transport (request autenticada,
                             ErrUnauthorized/APIError), auth, catalogimport,
                             tenantvariables, integrations, tenantllm,
                             entitlements + delegated (el adaptador del
                             plano de identidad al puerto Authenticator; el cliente de identity y el
                             canje son github.com/EduGoGroup/wapp-shared/iam)
internal/web/              — server (Gin, cableado de middlewares), policy (nombres de cookie,
                             entropía y opciones del módulo), auth_handler (login/AuthMiddleware/
                             refresh), session (claims del JWT), home_handler (la PORTADA),
                             entitlements (vista de features + gate); templates/ +
                             static/css/app.css (//go:embed). El middleware transversal —CSP+nonce,
                             CSRF, rate-limit, deadline, body-limit, single-flight, render— es
                             github.com/EduGoGroup/wapp-shared/web (+ /gin), no vive aquí
docs/contrato-api-publica.md — el contrato consumido (referencia para clientes Android/iOS)
go.mod                     — módulo: github.com/EduGoGroup/wapp-guardian-bff (coincide con el
                             remoto; no se publica con tags, pero el path ya es resoluble)
```

## Puntos abiertos relevantes

- Multi-teléfono y perfil `active|passive` (ADR-0008 / ADR-0027): **ya no son de este repo** (Plan 047 · T2.1). Con ellos se fue el aviso de que `passive` todavía no entrega la privacidad que el ADR promete —el filtrado de entrantes en el Edge es de la Ola 2 del Plan 046 y sigue sin existir—, y su test guardián (`TestDashboardNoPrometeLaPrivacidadQueAunNoEntrega`). 🔴 Ese aviso y ese test tienen que existir en `wapp-client-console`: si no llegaron allí, la garantía se perdió al mudarse.
- La portada no ofrece enlace a la consola del cliente salvo que `WAPP_GUARDIAN_CLIENT_CONSOLE_URL` esté puesta. En UAT no lo está (loopback en puertos distintos, sin URL pública), así que el operador tiene que saber la dirección por otro medio.
- Recuperación ante pérdida de DEK implica re-emparejar (sin backdoor) — fuera del BFF (local del Edge).
- Alcance diferido (campañas, plantillas/contactos, editor visual, media) — futuros planes.
