# wapp-guardian-bff

**Estado:** implementado (Plan 021, T1–T4) — consola operativa mínima, primer consumidor real
de la API pública `/api/v1` de wApp.

## Qué es

Consola web BFF (Back-For-Frontend) de **operación** de wApp: permite listar las sesiones
(teléfonos) vinculadas del tenant, enviar un mensaje de WhatsApp por una de ellas, y gestionar
menús/encuestas (`flows` versionados) y sus disparadores (`triggers`). Es la **implementación de
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
  cada llamada) — ver `internal/web/auth.go`.
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
| `WAPP_PUBLIC_API_BASE` | `http://localhost:8103` | URL base de la API pública `/api/v1` — único interlocutor del BFF |
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
│   ├── client.go             — auth (login/refresh/logout) + sessions + messages
│   ├── flows.go               — flows (listar/ver/publicar)
│   └── triggers.go            — triggers (listar/crear/borrar)
└── web/
    ├── server.go             — NewRouter (Gin), rutas, http.Server endurecido
    ├── security.go           — CSP+nonce, headers, CORS fail-closed
    ├── ratelimit.go          — keyedRateLimiter en memoria
    ├── handlers.go           — login/logout, cookie de sesión, render()
    ├── auth.go               — AuthMiddleware, parse-unverified+exp, refresh
    ├── dashboard.go          — listado de sesiones + envío de mensaje
    ├── editor.go             — flows (publicar versión) + triggers (crear/borrar)
    ├── static/css/app.css    — design system MD3 embebido (//go:embed)
    └── templates/            — layout base.html + páginas (login, dashboard, flows, triggers)
docs/contrato-api-publica.md — contrato consumido de /api/v1 (referencia para otros clientes)
```

## Documentación relacionada

- [`docs/contrato-api-publica.md`](docs/contrato-api-publica.md) — el contrato de `/api/v1` tal
  como este BFF lo consume (plantilla para Android/iOS).
- `../../docs/piezas/04-consola-bff-guardian.md` — spec de la Pieza 04 en el monorepo de docs.
- `../../docs/plans/021-cliente-web-referencia/` — plan que materializó este BFF (requirements,
  design, tasks).
- `CLAUDE.md` (este repo) — orientación para agentes LLM.
