# `wapp-guardian-bff` — la consola de la aduana y la configuración técnica

Servidor web SSR en Go (Gin) que escucha en **`:8104`**. Es la puerta de entrada del **cliente** a
wApp —entrar, darse de alta, esperar a que le asignen empresa— y la casa de **tres pantallas de
configuración técnica** del tenant: variables de empresa, puente CRM y proveedor de IA. Custodia el
JWT server-side en una cookie HttpOnly y relaya server-to-server contra la API pública REST de la
plataforma (`/api/v1`, `:8103`); **no habla gRPC, no toca base de datos y no toca material
criptográfico**.

## 🔴 Lo primero que hay que saber: aquí ya no vive el negocio

El **2026-08-30**, en el commit **`26e84c9`**, este repo perdió **17.072 líneas** (`git show --stat
26e84c9` da `69 files changed, 1785 insertions(+), 17072 deletions(-)`; contra la rama de respaldo
`backup/pre-047-main-20260828` son **48 ficheros borrados**). Toda la UI de negocio se mudó a
**`wapp-client-console`** (`:8107`), y el BFF quedó en **20 rutas de las que ninguna es de negocio**.

| Se fue | Rutas | Casa nueva |
|---|---|---|
| Dashboard de sesiones (`POST /send`, `POST /sessions/:id/profile`) | 2 | `/sesiones` en `wapp-client-console` |
| Editor de flujos y disparadores (`/flows`, `/triggers`) | 6 | `/flujos` y `/disparadores` |
| Bandeja de solicitudes (`/intakes` y sus ocho acciones) | 10 | `/solicitudes` |
| Import de catálogo (`/catalog-import`) | 3 | `/importar-catalogo` |

**No busques `/sesiones` ni `/solicitudes` aquí: nunca existieron con ese nombre y las viejas ya no
existen.** Lo que queda son **7 páginas más un layout**: `home`, `login`, `signup`, `pending`,
`integrations`, `tenant-llm`, `tenant-variables` sobre `base.html`.

Y una advertencia sobre el propio repo: el `README.md` y el `docs/contrato-api-publica.md` de la
raíz **están caducados** y describen la consola anterior a la mudanza. La verdad de hoy es esta
carpeta.

## Índice

| Documento | Qué contiene |
|---|---|
| [`constitucion.md`](constitucion.md) | Los invariantes que no se pueden violar (los del ecosistema que aplican y los propios), tecnología y versiones reales del `go.mod`, convenciones y las trampas conocidas. **Incluye la norma del candado derivado del router.** |
| [`arquitectura.md`](arquitectura.md) | Capas, mapa de paquetes, punto de entrada, orden exacto de los middlewares y diagramas. |
| [`contratos.md`](contratos.md) | Las 20 rutas HTTP por familia, las llamadas a `/api/v1` y a identity, las variables de entorno con su valor por defecto, cookies y ficheros. |
| [`operacion.md`](operacion.md) | Arranque local, los `make` reales y qué valida cada uno, cómo se publica y cómo se depura. **`ci.yml` no valida un PR: el gate es local.** |
| [`deuda.md`](deuda.md) | Deuda viva con `fichero:línea`, código muerto verificado y los desajustes de UAT. |

## Estado verificado el 2026-08-30

- `HEAD`, `main`, `dev` y `origin/main` en el **mismo commit `26e84c9`**: la mudanza ya está
  publicada, no está pendiente de promoción.
- Un solo tag: `v0.1.0` (`afa8bd4`, del 2026-08-28) — **anterior** a la mudanza.
- **3.957 líneas** de Go de producción y **6.715** de tests (147 funciones `Test`, 192 casos, **0
  SKIP**). Cobertura: `internal/config` 100 %, `internal/web` 81,1 %, `internal/bootstrap` 75,7 %,
  **`internal/apiclient` 0,0 %**.
- En UAT corre en `:8104` como unidad `wapp-bff`, con el binario compilado del mismo `26e84c9`.
