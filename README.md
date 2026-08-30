# wapp-guardian-bff

**Estado:** implementado (Plan 021, T1–T4; + Plan 040 · Ola 2: plan y capacidades del tenant) —
consola operativa mínima, primer consumidor real de la API pública `/api/v1` de wApp.

## Qué es

Consola web BFF (Back-For-Frontend) de **operación** de wApp: permite atender la bandeja de
solicitudes que llegan por WhatsApp, importar el catálogo, configurar el puente CRM y el proveedor de
IA, editar las variables de empresa, y ver el **plan y las capacidades contratadas** del tenant —que
además deciden qué secciones se pintan—.

> 🔴 **Las SESIONES ya no se administran aquí.** Listar los teléfonos vinculados, cambiarles el perfil
> `active|passive` y enviar un mensaje se mudaron a la **consola del cliente**
> (`guardian/wapp-client-console`) con el **Plan 047 · T2.1**, y se retiraron de este BFF en el mismo
> ciclo: dos copias de la misma pantalla divergen, y la que sigue viva contesta antes que la
> documentación. La portada lo dice, y ofrece el enlace solo si `WAPP_GUARDIAN_CLIENT_CONSOLE_URL`
> está puesta (vacía por defecto: en UAT no hay URL pública que publicar). Es la **implementación de
referencia** de un cliente de `/api/v1` (Pieza 04) — su contrato consumido, documentado en
[`docs/contrato-api-publica.md`](docs/contrato-api-publica.md), sirve de plantilla para futuros
clientes Android/iOS.

Su rol es **operación de negocio**, no infraestructura del Edge:
- **NO** custodia la DEK ni participa del emparejamiento (eso es local, en el Edge — Pieza 01).
- **NO** habla gRPC con el Gateway CloudLink ni con el Edge; **todo** pasa por la API pública REST.
- **NO** es fuente de verdad de ningún dato: relaya server-to-server contra la Pieza 03
  (`cloud/wapp-cloud-platform`).

**Fuera de alcance de esta v1** (diferido, ver `docs/plans/021-cliente-web-referencia/requirements.md`
REQ-E5): editor visual de nodos, catálogos (`tenant-content`), subida de media/`upload-url`.

## Arquitectura

```
Navegador ──HTTPS──►  wapp-guardian-bff  ──HTTPS Bearer──►  API pública REST  :8103  /api/v1
(cookie HttpOnly       (BFF SSR, :8104,     (server-to-server,
 el token NO viaja       Go + Gin +          JWT server-side)
 al navegador)           html/template)
```

- **SSR clásico**: Gin + `html/template` embebido (`//go:embed`), sin framework JS. Un layout
  maestro (`templates/layouts/base.html`) ejecuta el fragmento de página (`templates/pages/*.html`)
  vía el helper `yield`.
- **JWT server-side**: el login llama `POST /api/v1/auth/login` y el par `access_token`/
  `refresh_token` se guarda en **una** cookie HttpOnly (`wapp_guardian_session`, JSON en
  base64-URL); el navegador **nunca** ve el token (INV-4). El BFF valida el token con
  **parse-unverified + `exp`** (no verifica firma: la API pública es el gate criptográfico real en
  cada llamada) — ver `internal/web/session.go`.
- **Refresh + reintento**: toda llamada de negocio pasa por `withAuthRetry` — ante un 401 refresca
  la sesión una vez (`POST /api/v1/auth/refresh`) y reintenta; si falla, el usuario vuelve a ver el
  login.
- **Hardening** (copia-adaptación de `edugo-messaging-web`, ADR-0004): CSP con nonce por request y
  headers de seguridad (`internal/web/security.go`), CORS fail-closed (allowlist, nunca `*`),
  rate-limit en memoria por usuario/IP (`internal/web/ratelimit.go`, token-bucket vía
  `golang.org/x/time/rate`), cookies HttpOnly, `http.Server` endurecido anti-slowloris
  (`ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout` **y** `WriteTimeout` — a diferencia de EduGo, aquí
  sí se puede fijar porque no hay SSE de larga vida: el QR es local en el Edge).
- **UI Material Design 3** propia (paleta teal/verde), CSS embebido y servido mismo-origen
  (`internal/web/static/css/app.css`), sin CDNs — encaja con la CSP.
- **Gate por capacidad, server-side**: la portada lee `GET /api/v1/entitlements` y envuelve las
  secciones que dependen de una feature en `{{ if $.Entitlements.Has "<feature>" }}`. Sin la feature
  el bloque **no se emite en el HTML** (no se esconde con CSS ni JS), y si el endpoint falla la vista
  es **fail-closed**: se degrada con un aviso y ningún bloque gateado sale
  (`internal/web/entitlements.go`).

## Cómo se ejecuta

```bash
go run ./cmd/guardian-bff
```

Arranca en `:8104` y expone `GET /healthz` (sin sesión). Necesita una API pública (`cloud/
wapp-cloud-platform`, `:8103` por defecto) accesible en `WAPP_PUBLIC_API_BASE`.

### Variables de entorno

Prefijo `WAPP_`; ver `.env.example` para el listado completo con comentarios. Resumen:

| Variable | Default | Qué gobierna |
|---|---|---|
| `WAPP_GUARDIAN_ENV` | `local` | Ambiente lógico; distinto de `local` endurece `Secure` cookie + HSTS |
| `WAPP_GUARDIAN_HTTP_ADDR` | `:8104` | Dirección de escucha (banda 81xx de wApp) |
| `WAPP_PUBLIC_API_BASE` | `http://localhost:8103` | URL base de la API pública `/api/v1` — interlocutor del negocio y del canje |
| `WAPP_IDENTITY_JWKS_URL` | `` (vacío = modo dual apagado) | JWKS de identity-api para verificar Identity Tokens; con valor, el arranque es fail-closed |
| `WAPP_IDENTITY_URL` | `` (vacío = delegación apagada) | URL base de identity-api (`:8200`); con valor, login/refresh/logout viajan a identity y el token se canjea en la plataforma |
| `WAPP_GUARDIAN_COOKIE_SECURE` | `true` salvo `ENV=local` | Cookie de sesión solo sobre TLS |
| `WAPP_GUARDIAN_COOKIE_SAMESITE` | `lax` | `lax` \| `strict` \| `none` (`none` exige `Secure=true`) |
| `WAPP_GUARDIAN_ALLOWED_ORIGINS` | `` (vacío = same-origin) | Allowlist CSV de orígenes CORS; nunca `*` |
| `WAPP_GUARDIAN_HSTS_ENABLED` | sigue a `COOKIE_SECURE` | Emite `Strict-Transport-Security` |
| `WAPP_GUARDIAN_RATE_ENABLED` / `_RPS` / `_BURST` | `true` / `5` / `10` | Rate-limit en memoria por usuario/IP |
| `WAPP_GUARDIAN_READ_HEADER_TIMEOUT_SECS` / `_READ_TIMEOUT_SECS` / `_WRITE_TIMEOUT_SECS` / `_IDLE_TIMEOUT_SECS` | `5` / `15` / `30` / `60` | Timeouts del `http.Server` (anti-slowloris) |

Sin secretos hardcodeados: el BFF no comparte `WAPP_JWT_SECRET` con la plataforma (no valida
firma, ver arriba).

## Estructura

```
cmd/guardian-bff/main.go     — entrypoint: config + logger + web.Run(:8104)
internal/
├── config/config.go         — env (WAPP_*) → Config
├── apiclient/                — cliente HTTP server-to-server contra /api/v1
│   ├── transport.go          — request autenticada, ErrUnauthorized/APIError, StatusCodeOf
│   ├── auth.go               — login/refresh/logout (AuthResult y sus DTO)
│   ├── entitlements.go       — EntitlementsClient: GET /api/v1/entitlements (plan + features)
│   └── identity.go/exchange.go/delegated.go — identity-api y canje (delegación opcional)
└── web/
    ├── server.go             — NewRouter (Gin), rutas, http.Server endurecido
    ├── security.go           — CSP+nonce, headers, CORS fail-closed
    ├── csrf.go               — token CSRF de los formularios
    ├── ratelimit.go          — keyedRateLimiter en memoria
    ├── deadline.go           — deadline por petición sobre la cadena hacia la API
    ├── handlers.go           — composición de handlers (NewHandler, puerto de API)
    ├── render.go             — cookie de sesión (set/clear/maxAge) y render()
    ├── session.go            — sessionData + parse-unverified+exp de los claims
    ├── auth_handler.go       — login/logout, AuthMiddleware, refresh proactivo/pasivo, withAuthRetry
    ├── home_handler.go       — PORTADA: plan/capacidades + accesos a lo que esta consola conserva
    ├── entitlements.go       — vista de plan/features (Has) y gate fail-closed
    ├── static/css/app.css    — design system MD3 embebido (//go:embed)
    └── templates/            — layout base.html + páginas (login, home, variables, catálogo…)
docs/contrato-api-publica.md — contrato consumido de /api/v1 (referencia para otros clientes)
```

## Documentación relacionada

- [`docs/contrato-api-publica.md`](docs/contrato-api-publica.md) — el contrato de `/api/v1` tal
  como este BFF lo consume (plantilla para Android/iOS).
- `../../docs/piezas/04-consola-bff-guardian.md` — spec de la Pieza 04 en el monorepo de docs.
- `../../docs/plans/021-cliente-web-referencia/` — plan que materializó este BFF (requirements,
  design, tasks).
- `CLAUDE.md` (este repo) — orientación para agentes LLM.
