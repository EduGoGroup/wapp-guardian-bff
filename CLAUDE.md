# CLAUDE.md — wapp-guardian-bff

> Orientado al agente LLM. Lee también:
> - `../../docs/piezas/04-consola-bff-guardian.md` (pieza completa con ADRs)
> - `../../CLAUDE.md` (raíz del monorepo wApp)

---

## Qué es esta pieza

**Consola/BFF** — el terminal web de operación de negocio del cliente wApp, y el **primer consumidor real** de
la **API pública** (`/api/v1`, `:8103`) como **implementación de referencia** (Plan 021). Es un front endurecido
(Go / Gin, SSR con `html/template`) **sin lógica de dominio**: valida sesión, aplica hardening y habla **solo
REST** con la Plataforma Cloud (Pieza 03). Custodia el JWT **server-side** en cookie HttpOnly (el navegador nunca
ve el token). **No** empareja teléfonos ni custodia DEK.

## Responsabilidad (lo IMPLEMENTADO — Plan 021 MVP)

| Área | Qué hace | Endpoint `/api/v1` consumido |
|---|---|---|
| Login/sesión | Login server-side, JWT en cookie HttpOnly, refresh+reintento, logout | `auth/{login,refresh,logout}` |
| Sesiones | Lista los teléfonos/sesiones vinculados del tenant (self_pn/state/role) y cambia el **rol** `bot|passive` por sesión | `GET sessions`, `POST sessions/{id}/role` |
| Enviar mensaje | Elegir sesión + destino + texto y despachar | `POST messages` |
| Editar menú/encuestas | Listar/ver flows y **publicar versión nueva** (inmutables); triggers listar/crear/borrar | `flows`, `flows/{id}`, `triggers` |

> **Diferido (NO implementado):** subida de contenido/PDF, campañas, plantillas/contactos/segmentos, editor
> visual de nodos, `tenant-content`/`media`. Ver `../../docs/plans/021-cliente-web-referencia/` (REQ-E5).

## Decisiones clave (Pieza 04 / ADRs)

1. **El QR NO pasa por esta web.** En EduGo la consola era el terminal del QR (SSE).
   En wApp el QR es local en el Edge (systray). El endpoint `/sessions/:id/stream`
   y la excepción de `WriteTimeout` del BFF de EduGo se eliminan.
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
- **Deps:** `gin` + `github.com/EduGoGroup/wapp-shared/{logger,config}` (repo de wApp en la org EduGoGroup;
  **no** es `edugo-shared`) + `golang.org/x/time/rate`. **Cero import `edugo-*`** (ADR-0004).

## Qué se elimina o cambia respecto a edugo-messaging-web

- Relay SSE del QR y endpoint `/sessions/:id/stream` — **eliminados** (el QR es local del Edge).
- Multi-escuela / switch-context de EduGo — **eliminado** (el tenant sale del token, INV-8).
- `WriteTimeout` — **SÍ se fija** (30s): sin streams de larga vida, conviene endurecerlo (al revés que EduGo).

## Estructura del proyecto (real, tras Plan 021)

```
cmd/guardian-bff/main.go   — punto de entrada (config + logger + web.Run en :8104)
internal/config/           — Config desde env (WAPP_GUARDIAN_*, WAPP_PUBLIC_API_BASE)
internal/apiclient/        — cliente HTTP → /api/v1 (Bearer server-side): client, flows, triggers
internal/web/              — server (Gin, middlewares), auth (login/AuthMiddleware), dashboard, editor,
                             security (CSP+nonce), ratelimit; templates/ + static/css/app.css (//go:embed)
docs/contrato-api-publica.md — el contrato consumido (referencia para clientes Android/iOS)
go.mod                     — módulo: github.com/wApp/wapp-guardian-bff
```

## Puntos abiertos relevantes

- Multi-teléfono: la consola opera N sesiones por Edge (ADR-0008); hoy listado + cambio de rol (`bot|passive`); el resto de la operación de sesión (status/retiro) sigue fuera.
- Recuperación ante pérdida de DEK implica re-emparejar (sin backdoor) — fuera del BFF (local del Edge).
- Alcance diferido (campañas, plantillas/contactos, editor visual, media) — futuros planes.
