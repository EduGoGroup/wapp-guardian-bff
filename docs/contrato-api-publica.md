# Contrato consumido de la API pública `/api/v1`

> Documenta cómo **este BFF** (`wapp-guardian-bff`) consume la API pública REST de
> `cloud/wapp-cloud-platform` (`:8103`, Plan 018). Es la implementación de referencia: describe el
> contrato **tal como el código lo usa hoy** (`internal/apiclient/{auth.go,dashboard.go,editor.go,
> entitlements.go}`), para que un futuro cliente Android/iOS lo replique sin tener que leer el BFF
> entero. Fuente de
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

Request (`internal/apiclient/auth.go:25`, tipo `loginRequest`):
```json
{ "email": "operador@tenant.example", "password": "..." }
```

Response 2xx (`AuthResult`, `auth.go:17`) — **todo en el body**, no hay `Set-Cookie` del cloud:
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
  si el correo existe (`internal/web/auth_handler.go:44`, `DoLogin`, REQ-C3).
- Otro no-2xx → fallo de transporte/upstream, mismo tratamiento (mensaje genérico).

**Custodia server-side (este BFF):** el par de tokens se serializa a JSON y se guarda en **una**
cookie HttpOnly `wapp_guardian_session` (`internal/web/session.go:21`, `sessionData{AccessToken,
RefreshToken, ExpiresAt}`, valor en base64-URL sin padding; el nombre y el `setSessionCookie` viven
en `internal/web/render.go:15` y `:33`). El `maxAge` de la cookie sigue al `expires_at` del access
token (`sessionMaxAge`, `internal/web/render.go:59`). El navegador solo ve una cookie
opaca; el JWT nunca llega al DOM/JS (INV-4).

### Validación del token (decisión de diseño, no criptográfica)

Este BFF **no** verifica la firma del JWT: usa `jwt.NewParser().ParseUnverified` y solo comprueba
que `exp` exista y sea futuro (`parseAccessClaims`, `internal/web/session.go:53`). La API pública es el gate
criptográfico real — revalida el Bearer en cada llamada server-to-server. Un cliente que sí tenga
el secreto de firma (o que confíe menos en el transporte) puede optar por validación completa; no
es una obligación del contrato, es una decisión de este BFF.

### `POST /api/v1/auth/refresh`

Request (`refreshRequest`, `auth.go:30`): `{ "refresh_token": "…" }`. Response: mismo shape
`AuthResult` que login (nuevo `access_token`+`refresh_token`).

**Patrón refresh + reintento** (`internal/web/auth_handler.go:190`, función `withAuthRetry`,
reusada por dashboard/flows/triggers/entitlements): toda llamada de negocio se ejecuta primero con el
`access_token` vigente; si la API responde `401` (`apiclient.ErrUnauthorized`), el BFF llama
`Refresh` una vez, re-emite la cookie (`refreshSession`, `internal/web/auth_handler.go:159`) y
**reintenta la llamada original una sola vez**. Si el refresh también falla, el error original se
propaga tal cual (el llamador degrada o redirige a `/login`). No hay reintento en cadena.

### `POST /api/v1/auth/logout`

Request (`logoutRequest`, `auth.go:34`): `{ "refresh_token": "…" }` + `Authorization: Bearer
<access_token>`. Es **best-effort**: el BFF borra su cookie local **siempre**, llame o no la API
con éxito (`DoLogout`, `internal/web/auth_handler.go:80`). Un fallo del logout remoto no bloquea el logout
local.

## 3. Endpoints de negocio usados

Todas las llamadas de esta sección llevan `Authorization: Bearer <access_token>`
(`newAuthedRequest`, `transport.go:78`) y usan el patrón refresh+reintento de §2 cuando el
llamador es un handler del dashboard/editor.

| Método y ruta | Request | Response 2xx | Códigos de error relevantes | Cliente (`apiclient`) |
|---|---|---|---|---|
| `GET /api/v1/sessions` | — | `[]Session{session_id, edge_id, state, profile, self_pn?, last_connected_at?, last_seen_at?}` | `401` | `ListSessions` (`dashboard.go`) |
| `POST /api/v1/sessions/{id}/profile` | `{profile}` con `profile ∈ {active, passive}` (`setSessionProfileRequest`) | `{session_id, profile}` (200; este BFF descarta el body y re-lista) | `400` perfil inválido · `401` · `404` sesión ajena/inexistente (opaco) · `500` | `SetSessionProfile` (`dashboard.go`) |
| ~~`POST /api/v1/sessions/{id}/role`~~ | 🔴 **RETIRADA** de la plataforma (migración `0064`), junto con el campo `role` de la respuesta de `GET /api/v1/sessions`. Su ciclo de deprecación se cerró sin esperar: al comprobarlo, **no había ningún consumidor** de esa ruta fuera de la propia plataforma. | — | — | — |
| `POST /api/v1/messages` | `{session_id, to, text}` (`sendMessageRequest`) | `SendResult{acked_command_id, ok, error?}` (200 **incluso si `ok:false`**) | `400` datos inválidos · `401` · `404` sesión ajena · `502` Edge offline · `504` timeout · `500` | `SendMessage` (`dashboard.go:88`) |
| `GET /api/v1/entitlements` | — | `Entitlements{plan, features[], cache_ttl_seconds}` | `401` · `403` token sin el scope `entitlements.read` | `GetEntitlements` (`entitlements.go:26`) |
| `GET /api/v1/flows` | — | `[]FlowSummary{flow_id, version, created_at?}` | `401` | `ListFlows` (`editor.go:102`) |
| `GET /api/v1/flows/{id}` | — | `model.Flow` crudo (`{flow_id, version, initial, nodes}`), devuelto sin re-serializar | `401` · `404` (ajeno/inexistente, opaco) | `GetFlow` (`editor.go:123`) |
| `POST /api/v1/flows` | `{definition: <model.Flow>}` (**anidado**, `publishFlowRequest`) | `PublishFlowResult{flow_id, version}` (201) | `401` · `4xx` rechazo de validación (mensaje mostrable) · `5xx` | `PublishFlow` (`editor.go:144`) |
| `GET /api/v1/triggers` | — | `[]Trigger{trigger_id, kind, keyword?, event_kind?, match_type, flow_id?, priority, enabled, message?, session_id?, shadowed_by_event_list?}` | `401` | `ListTriggers` (`editor.go:188`) |
| `POST /api/v1/triggers` | `CreateTriggerRequest{kind, keyword?, event_kind?, match_type?, flow_id?, priority, message?, session_id?}` | `Trigger` creado (201) | `401` · `4xx` rechazo de validación (mensaje mostrable) · `5xx` | `CreateTrigger` (`editor.go:209`) |
| `DELETE /api/v1/triggers/{id}` | — | sin body (204) | `401` · `404` (ajeno/inexistente, opaco) | `DeleteTrigger` (`editor.go:230`) |

Notas de contrato:
- **Entitlements: lista de habilitadas, no mapa** (`Entitlements`, `entitlements.go:17`). `features`
  trae **solo** las capacidades efectivas —las del plan con los overrides del tenant ya aplicados—,
  en el orden estable que fija el servidor. La decisión del cliente es por **pertenencia**: lo que no
  está en la lista, no está contratado. El tenant no viaja ni en la petición ni en la respuesta (sale
  del token, INV-8). El `403` por falta de scope es un caso esperado, no un fallo de plataforma: se
  distingue con `StatusCodeOf`. **Este BFF resuelve por petición, deliberadamente**: pide el endpoint
  en cada render del dashboard, así que no necesita `cache_ttl_seconds` —el gate va siempre fresco—.
  El TTL está en el contrato para los clientes que **sí** cachean (una app móvil, p. ej.): respétalo
  si guardas la respuesta. Y ojo con el alcance: en este BFF las features deciden lo que se **pinta**
  (§6), nunca lo que se **puede** —eso lo resuelve el servidor en cada llamada—.
- **Flows son inmutables versionados**: la clave persistida es `(tenant_id, flow_id, version)`. No
  hay `PUT`/`DELETE`; "editar" = publicar con `POST /api/v1/flows` una definición nueva, que el
  servidor versiona como `version+1`.
- **Triggers no tienen edición in-place**: no hay `PUT`. "Editar" = `DELETE` + `POST`.
- **Perfil de sesión** (`POST /api/v1/sessions/{id}/profile`, scope `sessions.write`, ADR-0027 ·
  Plan 046 · T1.2): `active` dispara triggers/auto-responde; `passive` solo transporta salida. El
  tenant sale del token (INV-8) y el `session_id` del path; el body solo lleva `{profile}`. Este BFF
  valida el perfil client-side antes de enviar (ver §4) y tras el 200 re-lista las sesiones (el
  perfil nuevo se ve en la tabla).
  - **Los identificadores viajan en inglés** (`active`/`passive`); «activa»/«pasiva» es SOLO el
    vocabulario del dueño en la vista (`profileLabel`, `dashboard_handler.go`). Que el `value` del
    `<option>` y su texto no coincidan es deliberado.
  - **Sustituye al rol `bot|passive`** del Plan 020. `Session.Role` sigue en el DTO como respaldo de
    LECTURA mientras dure la deprecación —`Session.EffectiveProfile` traduce `bot`→`active`— porque
    el BFF y la plataforma no se despliegan a la vez. No se escribe por la ruta vieja.
  - 🔴 **Deslinde**: este `profile` NO es el `devices.role` del Edge (`primary`/`standby`, failover
    multi-dispositivo, ADR-0018). Dominios sin relación; el Edge no se renombra.
- **`kind` de trigger** ∈ `{keyword, fallback, escape, event_start, event_stop}` (Plan 043 · T2.1
  añade los dos últimos; el kind `llm` existe en la plataforma desde el Plan 029 pero este BFF
  todavía no lo ofrece — deuda pendiente, no confundir con "no soportado por la API").
  **`match_type`** ∈ `{exact, contains}`. Campos requeridos según `kind` (este BFF los valida
  client-side antes de enviar, ver §4): `keyword` → `keyword`+`flow_id`; `fallback` → `flow_id`;
  `escape` → `keyword`; `event_start` → `keyword`+`event_kind` (con `event_kind ∈ {menu, cart,
  survey, media}`, los kinds de fábrica del despachador — D-043.3); `event_stop` → `keyword`.
  `event_kind` **solo** viaja en el request cuando `kind = event_start`.
- **`shadowed_by_event_list`** (solo en la respuesta de `GET /api/v1/triggers`, nunca en el POST):
  marca booleana **derivada** que la plataforma calcula sobre los triggers `kind = fallback` del
  tenant (D-043.20/REQ-27b). Desde el Plan 043 · Ola 2, una conversación nueva sin evento activo la
  responde la **lista de eventos** del despachador primero, así que un `fallback` marcado así ya no
  suena ahí. Este BFF solo la pinta como aviso en la tabla de triggers — no la calcula ni la
  persiste.

## 4. Manejo de errores y códigos

El cliente (`internal/apiclient`) tipifica los fallos en tres formas que los handlers distinguen
sin acoplarse al string del error:

1. **`ErrUnauthorized`** (sentinel, `transport.go:33`) — cualquier `401`. Se detecta con
   `errors.Is`. Dispara el patrón refresh+reintento (§2); si no hay recuperación, el usuario
   termina en `/login`.
2. **`*APIError{Op, StatusCode}`** (`transport.go:36`) — cualquier otro no-2xx en un endpoint de
   **lectura**, o un `5xx`/error genérico en uno de **escritura**. **No** arrastra el cuerpo de la
   respuesta: el mensaje al usuario es genérico y fijo por código (mapeo en
   `internal/web/dashboard_handler.go:83` para `SendMessage`: `400`→"datos inválidos", `404`→"sesión
   ajena", `502`→"desconectado", `504`→"tardó demasiado", resto→genérico). El código se extrae con
   `apiclient.StatusCodeOf(err)` (`transport.go:54`).
3. **`*RejectionError{Op, StatusCode, Message}`** (`editor.go:55`) — **solo** en endpoints de
   **escritura** (`PublishFlow`, `CreateTrigger`) y **solo** para `4xx` distinto de `401`. Aquí el
   cuerpo de la API **sí** se muestra al usuario (acotado a 500 bytes,
   `maxRejectionBody`, `editor.go:65`): es un rechazo de **contenido propio del operador** (p. ej.
   "definición de flujo inválida", "keyword es requerido"), no una traza interna — mostrarlo ayuda a
   corregir (REQ-E4). Se extrae con `apiclient.RejectionMessageOf(err)` (`editor.go:83`).

Regla general que un cliente nuevo debería replicar: **nunca mostrar el cuerpo crudo de un error
que no sea un rechazo de validación sobre contenido propio.** Los `5xx` y los `401` no llevan
mensaje seguro de exponer.

### Ack asimétrico de `POST /api/v1/messages`

`SendMessage` devuelve **200** con `{ok: false, error: "..."}` cuando el Edge recibió el comando
pero **no pudo entregarlo** — es distinto de un error de transporte. Este BFF trata `ok:false`
como fallo de negocio pero **no expone `result.Error`** al usuario (mensaje fijo genérico,
`internal/web/dashboard_handler.go:94`): el detalle del Edge no se considera "contenido propio del
operador".

## 5. Diferencia con la mini-web local del Edge (`wapp-ctl`)

Este BFF es la consola **remota** de operación (Pieza 04), pero **no es la única superficie web**
del ecosistema wApp. El Edge Agent (Pieza 01) expone su propio **plano de control local**
(`wapp-ctl`, Plan 007): un servidor HTTP en **loopback** (`127.0.0.1:8105`) que corre **en la
máquina del cliente**, junto al daemon 24/7.

Son dos superficies deliberadamente **separadas, no fusionables**:

| | `wapp-guardian-bff` (este repo) | mini-web local del Edge (`wapp-ctl`) |
|---|---|---|
| Dónde corre | Nube (Pieza 04), remoto | Máquina del cliente, `127.0.0.1:8105` |
| A quién habla | API pública `/api/v1` (Pieza 03), server-to-server | Al propio Edge Agent, en proceso |
| Para qué | Operación de negocio: sesiones, mensajes, flows, triggers | Emparejamiento QR local, estado del daemon, zero-knowledge |
| Custodia de secretos | JWT de operación (cookie HttpOnly) | La DEK/llaves nunca salen de la máquina; el loopback es la frontera de confianza |
| Autenticación | Login contra el IAM de la plataforma | Ninguna remota — el loopback en sí es el control de acceso (solo procesos locales llegan) |

**Por qué no se fusionan:** fusionarlas rompería el principio zero-knowledge (ADR-0007) — el
emparejamiento y la DEK son asunto exclusivamente local del Edge; la nube (y por tanto este BFF)
nunca debe ver ni intermediar esas operaciones. Un cliente que necesite emparejar un teléfono habla
con `wapp-ctl` en la máquina del Edge, no con esta consola ni con `/api/v1`.

## 6. Cómo este BFF usa las features en la UI (Plan 040 · Ola 2)

Las features de §3 alimentan dos cosas en el dashboard, y conviene no confundirlas:

1. **Los chips informativos** — el plan y una etiqueta por feature efectiva
   (`templates/pages/dashboard.html:13-25`). Si el endpoint no se pudo consultar, en su lugar sale un
   aviso de modo degradado; la página **no** se cae por eso.
2. **El gate de secciones** — un bloque de UI que depende de una capacidad se emite solo si la
   feature está contratada:

   ```
   {{ if $.Entitlements.Has "<feature>" }} … {{ end }}
   ```

   El gate vive en la **plantilla**, no en CSS ni en JS: sin la feature el bloque **no llega al
   HTML**, así que no hay nada que destapar con el inspector (y encaja con la CSP sin
   `'unsafe-inline'`). Hoy lo usa la sección del clasificador de intenciones
   (`templates/pages/dashboard.html:124`, feature `llm_intent`).

**Fail-closed**: `resolveEntitlements` nunca devuelve error — ante un fallo, un `403` o un endpoint
que aún no exista, devuelve la vista cero (`internal/web/entitlements.go:60`), y `Has` sobre esa
vista responde `false` para todo (`internal/web/entitlements.go:41`). Una consola que enseña de menos es
preferible a una que promete lo que el tenant no ha contratado.

Un cliente móvil que replique el patrón debería replicar también la regla: **pintar por feature es
cortesía de UI, no control de acceso.** La autorización real la aplica el middleware `RequireFeature`
de la plataforma (`cloud/wapp-cloud-platform/internal/entitlements/middleware.go`) en cada llamada:
corta con `403` y cuerpo `{"error":"feature_not_enabled","feature":"<clave>"}`, y es fail-closed
(sin identidad o con el resolver caído también corta). Un cliente que se saltara el gate de UI
seguiría chocando con él.
