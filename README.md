# wapp-guardian-bff

**Estado:** Greenfield — estructura inicial, sin lógica de negocio.

## Qué es

Consola web BFF (Back-For-Frontend) de operación de negocio para el cliente wApp.
Permite al operador subir contenido (PDF, texto, número de teléfono destino),
gestionar campañas, plantillas y contactos, y consultar estados y métricas de los
Edge Agents.

## Rol en wApp

- Pieza 04 del ecosistema wApp (ver `../../docs/piezas/04-consola-bff-guardian.md`).
- Se sitúa en el **lado de la nube**, entre el navegador del operador y las APIs cloud
  de negocio (Pieza 03). No ejecuta lógica de WhatsApp ni toca material criptográfico.
- El QR de emparejamiento es asunto del Edge (Pieza 01) y **no** pasa por esta consola
  (diferencia clave respecto a `edugo-messaging-web`).

## Tecnología

- Go 1.23 (ajustable; el `go.mod` usa este valor como placeholder).
- Router: Gin (a instalar al iniciar la implementación).
- Patrón: copia y adaptación de `edugo-messaging-web` (ADR-0004).

## Cómo correrá (placeholder)

```bash
# Pendiente de implementar
go run ./cmd/guardian-bff/
```

## Estructura

```
cmd/guardian-bff/main.go   — punto de entrada
internal/handlers/         — handlers HTTP (por implementar)
internal/services/         — servicios de aplicación (por implementar)
web/                       — assets/plantillas web (por implementar)
```
