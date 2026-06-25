# CLAUDE.md — wapp-guardian-bff

> Orientado al agente LLM. Lee también:
> - `../../docs/piezas/04-consola-bff-guardian.md` (pieza completa con ADRs)
> - `../../CLAUDE.md` (raíz del monorepo wApp)

---

## Qué es esta pieza

**Consola/BFF** — el terminal web de operación de negocio del cliente wApp.
Es un front endurecido (Go / Gin) sin lógica de dominio: valida sesión, aplica
hardening y proxea hacia las APIs cloud de negocio (Pieza 03).

## Responsabilidad

| Área | Qué hace | A quién llama |
|---|---|---|
| Subida de contenido | Recibe PDF/texto/teléfono destino; los entrega a la nube | API de negocio + almacenamiento de objetos |
| Campañas | Crear/programar campañas; seguir progreso | API de negocio |
| Plantillas y contactos | CRUD de plantillas, catálogos, contactos, segmentos | API de negocio |
| Estados y métricas | Enviado/entregado/leído, estado online/offline de Edges | API de negocio |

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
- Auth server-to-server contra el IAM (`edugo-api-identity`).
- CSS compilado embebido (sin CDNs externas).

## Qué se elimina o cambia respecto a edugo-messaging-web

- Relay SSE del QR y endpoint `/sessions/:id/stream` — **eliminados**.
- Excepción de `WriteTimeout` — **innecesaria** sin streams de larga vida.
- Se añaden vistas: subida de contenido, campañas, plantillas, contactos, dashboards.

## Estructura del proyecto

```
cmd/guardian-bff/main.go   — punto de entrada (placeholder)
internal/handlers/         — handlers HTTP por implementar
internal/services/         — servicios de aplicación por implementar
web/                       — assets/plantillas por implementar
go.mod                     — módulo: github.com/wApp/wapp-guardian-bff, go 1.23
```

## Puntos abiertos relevantes

- Multi-teléfono: la consola debe operar N sesiones por Edge (ADR-0008).
- Recuperación ante pérdida de DEK implica re-emparejar (sin backdoor).
