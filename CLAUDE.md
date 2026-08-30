# CLAUDE.md — `wapp-guardian-bff`

Portal. **La verdad vive en [`documentations/`](documentations/README.md)**; aquí solo se apunta.
Si algo de este fichero contradice a `documentations/`, manda `documentations/`.

## Qué es esta pieza

Consola web SSR en Go (Gin) del **cliente**, en **`:8104`**. Hoy es **la aduana y la configuración
técnica**: entrar, darse de alta, esperar a que te asignen empresa, y configurar tres cosas
—variables de empresa, puente CRM y proveedor de IA—. Custodia el JWT server-side en una cookie
HttpOnly y relaya server-to-server contra la API pública REST (`/api/v1`, `:8103`).

🔴 **Ya NO administra el negocio.** El commit `26e84c9` (2026-08-30) se llevó **17.072 líneas**: las
sesiones, el editor de flujos y disparadores, la bandeja de solicitudes y el import de catálogo
viven ahora en **`wapp-client-console`** (`:8107`). **No busques `/sesiones` ni `/solicitudes`
aquí.** Quedan **20 rutas, ninguna de negocio**, y **7 páginas más un layout**.

⚠️ El `README.md` y el `docs/contrato-api-publica.md` de este repo **están caducados** y describen la
consola anterior a la mudanza. No heredes lo que dicen.

## Las cinco reglas innegociables

1. **Zero-knowledge y doble llave.** La nube nunca accede a credenciales ni llaves privadas. La
   **DEK** (que descifra el almacén de `whatsmeow`) la custodia el cliente y jamás cruza ningún
   contrato; el **Lease** lo emite y revoca el servidor y es el kill-switch anti-clon. Este BFF **no
   toca material criptográfico**, no habla gRPC y **no tiene base de datos**. Lo que sí sube a la
   nube, a propósito, es el contenido de negocio.
2. **20 rutas y ninguna de negocio.** Toda ruta va clasificada en una de cuatro familias —aduana 6 ·
   portada 1 · técnicas 8 · infraestructura 5—. Si lo que vas a añadir es negocio, **no tiene
   familia aquí**: su casa es `wapp-client-console`.
3. **El candado se DERIVA del router (o del `embed`, o del AST), el aserto es de IGUALDAD de
   conjuntos, y lleva GUARDA ANTI-CERO.** Una lista negra solo sabe decir que no volvió lo que ya se
   fue; y un aserto universal sin guarda anti-cero sale verde midiendo cero. Es la norma de esta
   casa: `internal/web/inventario_test.go:90-107`.
4. **Copia-adaptación, nunca dependencia: cero import `edugo-*`.** El código compartido interno vive
   en **`wapp-shared`**, monorepo multi-módulo con releases por módulo (tags `<modulo>/vX.Y.Z`);
   este repo consume seis de sus módulos. Lo que se repita entre consolas **sube a `wapp-shared`**,
   no se copia. Sin Redis ni broker: la concurrencia se resuelve con Go, y el único estado es en
   memoria y por proceso.
5. **Un PR no valida nada.** `.github/workflows/ci.yml` es `workflow_dispatch`. El gate real es
   `make ci-local` (fmt · vet · lint · test `-race` · build), con Go **1.26.5** y golangci-lint
   **v2.12.2** fijados. Y **cuenta los SKIP**: un `rc=0` los cuenta igual que un PASS.

## Índice de `documentations/`

| Documento | Para qué |
|---|---|
| [`documentations/README.md`](documentations/README.md) | portal: qué es, qué se fue y a dónde, estado verificado |
| [`documentations/constitucion.md`](documentations/constitucion.md) | 🔴 **empieza aquí**: invariantes con su comprobación, tecnología real, convenciones y **las trampas conocidas** |
| [`documentations/arquitectura.md`](documentations/arquitectura.md) | capas, mapa de paquetes, arranque, orden de middlewares, diagramas |
| [`documentations/contratos.md`](documentations/contratos.md) | las 20 rutas, lo que consume de `/api/v1` y de identity, variables de entorno con su default, cookies |
| [`documentations/operacion.md`](documentations/operacion.md) | arranque local, `make` reales, publicación y depuración |
| [`documentations/deuda.md`](documentations/deuda.md) | deuda viva con `fichero:línea`, incluido el código muerto |

## Antes de tocar código

- Lee `internal/web/server.go` entero: es el registro de rutas **y** el sitio donde cada retirada
  dejó escrito qué se fue, a dónde y qué test lo vigila.
- Corre `make ci-local` antes de mergear y de pushear. Nada de este repo se valida en GitHub.
