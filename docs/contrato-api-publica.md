# Contrato consumido de la API pública `/api/v1`

> Documenta cómo **este BFF** (`wapp-guardian-bff`) consume la API pública REST de
> `cloud/wapp-cloud-platform` (`:8103`, Plan 018). Es la implementación de referencia: describe el
> contrato **tal como el código lo usa hoy** (`internal/apiclient/{client.go,flows.go,triggers.go}`),
> para que un futuro cliente Android/iOS lo replique sin tener que leer el BFF entero. Fuente de
> verdad del contrato real es siempre la propia API pública; este documento describe **el
> subconjunto y la forma en que este BFF lo consume**.

## 1. Invariantes para cualquier cliente

- **Todo por `/api/v1`.** El cliente habla **solo** REST contra `:8103`. Nunca contra la base de
  datos, el Gateway CloudLink (gRPC del Edge, `:8101`/`:8102`) ni el Edge directamente.
- **Zero-knowledge.** El cliente no maneja DEK, llaves privadas ni credenciales de WhatsApp; solo
  el JWT de operación devuelto por `/auth/login`. No participa del emparejamiento (QR local en el
  Edge — ver §5).
- **Tenant del token.** El `tenant_id` se deriva siempre del JWT (`Bearer`); **nunca** viaja en el
  body de una petición. Los DTO de request de este BFF (`sendMessageRequest`, `publishFlowRequest`,
  `CreateTriggerRequest`, …) no tienen campo tenant — es deliberado.
- **JWT server-side.** El `access_token`/`refresh_token` se custodian server-side (aquí, en una
  cookie HttpOnly); el navegador nunca los ve. Un cliente móvil nativo es su propio "server-side"
  (keystore/keychain del SO) — el patrón de custodia cambia, el contrato de red no.

## 2. Flujo de login, custodia y refresh

### `POST /api/v1/auth/login`

Request (`internal/apiclient/client.go:84-87`, tipo `loginRequest`):
```json
{ "email": "operador@tenant.example", "password": "..." }
```

Response 2xx (`AuthResult`, `client.go:48-54`) — **todo en el body**, no hay `Set-Cookie` del cloud:
```json
{
  "access_token": "…",
  "refresh_token": "…",
  "token_type": "Bearer",
  "expires_at": "2026-07-06T12:00:00Z",
  "context": { "tenant_id": "…", "user_id": "…", "roles": ["…"] }
}
```

- `401` → credenciales inválidas. Este BFF lo repinta como login fallido genérico, **sin** indicar
  si el correo existe (`internal/web/handlers.go:104-111`, REQ-C3).
- Otro no-2xx → fallo de transporte/upstream, mismo tratamiento (mensaje genérico).

**Custodia server-side (este BFF):** el par de tokens se serializa a JSON y se guarda en **una**
cookie HttpOnly `wapp_guardian_session` (`internal/web/auth.go:26-44`, `sessionData{AccessToken,
RefreshToken, ExpiresAt}`, valor en base64-URL sin padding). El `maxAge` de la cookie sigue al
`expires_at` del access token (`internal/web/handlers.go:174-186`). El navegador solo ve una cookie
opaca; el JWT nunca llega al DOM/JS (INV-4).

### Validación del token (decisión de diseño, no criptográfica)

Este BFF **no** verifica la firma del JWT: usa `jwt.NewParser().ParseUnverified` y solo comprueba
que `exp` exista y sea futuro (`internal/web/auth.go:59-82`). La API pública es el gate
criptográfico real — revalida el Bearer en cada llamada server-to-server. Un cliente que sí tenga
el secreto de firma (o que confíe menos en el transporte) puede optar por validación completa; no
es una obligación del contrato, es una decisión de este BFF.

### `POST /api/v1/auth/refresh`

Request (`refreshRequest`, `client.go:89-91`): `{ "refresh_token": "…" }`. Response: mismo shape
`AuthResult` que login (nuevo `access_token`+`refresh_token`).

**Patrón refresh + reintento** (`internal/web/dashboard.go:120-139`, función `withAuthRetry`,
reusada por dashboard/flows/triggers): toda llamada de negocio se ejecuta primero con el
`access_token` vigente; si la API responde `401` (`apiclient.ErrUnauthorized`), el BFF llama
`Refresh` una vez, re-emite la cookie (`refreshSession`, `internal/web/auth.go:125-142`) y
**reintenta la llamada original una sola vez**. Si el refresh también falla, el error original se
propaga tal cual (el llamador degrada o redirige a `/login`). No hay reintento en cadena.

### `POST /api/v1/auth/logout`

Request (`logoutRequest`, `client.go:93-95`): `{ "refresh_token": "…" }` + `Authorization: Bearer
<access_token>`. Es **best-effort**: el BFF borra su cookie local **siempre**, llame o no la API
con éxito (`internal/web/handlers.go:124-134`). Un fallo del logout remoto no bloquea el logout
local.

## 3. Endpoints de negocio usados

Todas las llamadas de esta sección llevan `Authorization: Bearer <access_token>`
(`newAuthedRequest`, `client.go:258-268`) y usan el patrón refresh+reintento de §2 cuando el
llamador es un handler del dashboard/editor.

| Método y ruta | Request | Response 2xx | Códigos de error relevantes | Cliente (`apiclient`) |
|---|---|---|---|---|
| `GET /api/v1/sessions` | — | `[]Session{session_id, edge_id, state, role, self_pn?, last_connected_at?, last_seen_at?}` | `401` | `ListSessions` (`client.go:164-182`) |
| `POST /api/v1/messages` | `{session_id, to, text}` (`sendMessageRequest`) | `SendResult{acked_command_id, ok, error?}` (200 **incluso si `ok:false`**) | `400` datos inválidos · `401` · `404` sesión ajena · `502` Edge offline · `504` timeout · `500` | `SendMessage` (`client.go:205-224`) |
| `GET /api/v1/flows` | — | `[]FlowSummary{flow_id, version, created_at?}` | `401` | `ListFlows` (`flows.go:35-53`) |
| `GET /api/v1/flows/{id}` | — | `model.Flow` crudo (`{flow_id, version, initial, nodes}`), devuelto sin re-serializar | `401` · `404` (ajeno/inexistente, opaco) | `GetFlow` (`flows.go:60-78`) |
| `POST /api/v1/flows` | `{definition: <model.Flow>}` (**anidado**, `publishFlowRequest`) | `PublishFlowResult{flow_id, version}` (201) | `401` · `4xx` rechazo de validación (mensaje mostrable) · `5xx` | `PublishFlow` (`flows.go:101-120`) |
| `GET /api/v1/triggers` | — | `[]Trigger{trigger_id, kind, keyword?, match_type, flow_id?, priority, enabled, message?, session_id?}` | `401` | `ListTriggers` (`triggers.go:50-68`) |
| `POST /api/v1/triggers` | `CreateTriggerRequest{kind, keyword?, match_type?, flow_id?, priority, message?, session_id?}` | `Trigger` creado (201) | `401` · `4xx` rechazo de validación (mensaje mostrable) · `5xx` | `CreateTrigger` (`triggers.go:74-92`) |
| `DELETE /api/v1/triggers/{id}` | — | sin body (204) | `401` · `404` (ajeno/inexistente, opaco) | `DeleteTrigger` (`triggers.go:97-111`) |

Notas de contrato:
- **Flows son inmutables versionados**: la clave persistida es `(tenant_id, flow_id, version)`. No
  hay `PUT`/`DELETE`; "editar" = publicar con `POST /api/v1/flows` una definición nueva, que el
  servidor versiona como `version+1`.
- **Triggers no tienen edición in-place**: no hay `PUT`. "Editar" = `DELETE` + `POST`.
- **`kind` de trigger** ∈ `{keyword, fallback, escape}`; **`match_type`** ∈ `{exact, contains}`.
  Campos requeridos según `kind` (este BFF los valida client-side antes de enviar, ver §4):
  `keyword` → `keyword`+`flow_id`; `fallback` → `flow_id`; `escape` → `keyword`.

## 4. Manejo de errores y códigos

El cliente (`internal/apiclient`) tipifica los fallos en tres formas que los handlers distinguen
sin acoplarse al string del error:

1. **`ErrUnauthorized`** (sentinel, `client.go:56-58`) — cualquier `401`. Se detecta con
   `errors.Is`. Dispara el patrón refresh+reintento (§2); si no hay recuperación, el usuario
   termina en `/login`.
2. **`*APIError{Op, StatusCode}`** (`client.go:60-78`) — cualquier otro no-2xx en un endpoint de
   **lectura**, o un `5xx`/error genérico en uno de **escritura**. **No** arrastra el cuerpo de la
   respuesta: el mensaje al usuario es genérico y fijo por código (mapeo en
   `internal/web/dashboard.go:65-92` para `SendMessage`: `400`→"datos inválidos", `404`→"sesión
   ajena", `502`→"desconectado", `504`→"tardó demasiado", resto→genérico). El código se extrae con
   `apiclient.StatusCodeOf(err)`.
3. **`*RejectionError{Op, StatusCode, Message}`** (`flows.go:126-164`) — **solo** en endpoints de
   **escritura** (`PublishFlow`, `CreateTrigger`) y **solo** para `4xx` distinto de `401`. Aquí el
   cuerpo de la API **sí** se muestra al usuario (acotado a 500 bytes,
   `maxRejectionBody`): es un rechazo de **contenido propio del operador** (p. ej. "definición de
   flujo inválida", "keyword es requerido"), no una traza interna — mostrarlo ayuda a corregir
   (REQ-E4). Se extrae con `apiclient.RejectionMessageOf(err)`.

Regla general que un cliente nuevo debería replicar: **nunca mostrar el cuerpo crudo de un error
que no sea un rechazo de validación sobre contenido propio.** Los `5xx` y los `401` no llevan
mensaje seguro de exponer.

### Ack asimétrico de `POST /api/v1/messages`

`SendMessage` devuelve **200** con `{ok: false, error: "..."}` cuando el Edge recibió el comando
pero **no pudo entregarlo** — es distinto de un error de transporte. Este BFF trata `ok:false`
como fallo de negocio pero **no expone `result.Error`** al usuario (mensaje fijo genérico,
`internal/web/dashboard.go:87-90`): el detalle del Edge no se considera "contenido propio del
operador".

## 5. Diferencia con la mini-web local del Edge (`wapp-ctl`)

Este BFF es la consola **remota** de operación (Pieza 04), pero **no es la única superficie web**
del ecosistema wApp. El Edge Agent (Pieza 01) expone su propio **plano de control local**
(`wapp-ctl`, Plan 007): un servidor HTTP en **loopback** (`127.0.0.1:8765`) que corre **en la
máquina del cliente**, junto al daemon 24/7.

Son dos superficies deliberadamente **separadas, no fusionables**:

| | `wapp-guardian-bff` (este repo) | mini-web local del Edge (`wapp-ctl`) |
|---|---|---|
| Dónde corre | Nube (Pieza 04), remoto | Máquina del cliente, `127.0.0.1:8765` |
| A quién habla | API pública `/api/v1` (Pieza 03), server-to-server | Al propio Edge Agent, en proceso |
| Para qué | Operación de negocio: sesiones, mensajes, flows, triggers | Emparejamiento QR local, estado del daemon, zero-knowledge |
| Custodia de secretos | JWT de operación (cookie HttpOnly) | La DEK/llaves nunca salen de la máquina; el loopback es la frontera de confianza |
| Autenticación | Login contra el IAM de la plataforma | Ninguna remota — el loopback en sí es el control de acceso (solo procesos locales llegan) |

**Por qué no se fusionan:** fusionarlas rompería el principio zero-knowledge (ADR-0007) — el
emparejamiento y la DEK son asunto exclusivamente local del Edge; la nube (y por tanto este BFF)
nunca debe ver ni intermediar esas operaciones. Un cliente que necesite emparejar un teléfono habla
con `wapp-ctl` en la máquina del Edge, no con esta consola ni con `/api/v1`.
