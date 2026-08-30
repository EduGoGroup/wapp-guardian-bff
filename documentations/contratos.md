# Contratos de `wapp-guardian-bff`

Todo lo que otros consumen de esta pieza y todo lo que ella consume de otros. Verificado sobre el
commit `26e84c9`.

**De dónde sale cada lista y con qué regla se contó** está dicho en cada sección. La regla general:
nada de esto se leyó de un `.md`; o sale del registro de rutas, o de un `grep` sobre el código de
producción, o de ejecutar el propio test que lo deriva.

---

## 1. Las 20 rutas HTTP que sirve

**Fuente:** el registro está en `internal/web/server.go`; la lista declarada, en
`internal/web/inventario_test.go:56-85`. **Regla de conteo:** una entrada por par
`MÉTODO + patrón de gin`, no por path — este router nace con `HandleMethodNotAllowed` en `false`,
así que responde 404 a un verbo no registrado igual que a una ruta inexistente y un inventario que
solo mirara paths daría por retirado un `POST /x` que sigue vivo bajo un `GET /x`. La lista no está
escrita a mano: `TestElInventarioDeRutasEsExactamenteElDeREQ10` la deriva de `router.Routes()` y
exige **igualdad de conjuntos**.

Reparto: **aduana 6 · portada 1 · técnicas 8 · infraestructura 5 = 20**.

### Infraestructura (5) — sin sesión y sin CSRF (registradas ANTES del middleware CSRF)

| Método y ruta | Qué devuelve | Dónde |
|---|---|---|
| `GET /static/css/app.css` | el CSS propio embebido, `Cache-Control: public, max-age=3600` | `internal/web/server.go:139-142` |
| `GET /static/css/wapp-tokens.css` | hoja de `wapp-shared/ui` | `internal/web/server.go:145-153` |
| `GET /static/css/wapp-components.css` | hoja de `wapp-shared/ui` | `internal/web/server.go:154-162` |
| `GET /static/css/theme-bff.css` | hoja de `wapp-shared/ui` | `internal/web/server.go:163-171` |
| `GET /healthz` | `{"status":"healthy","time":"<RFC3339>"}`, **HTTP 200 siempre** | `internal/web/server.go:174-176` |

⚠️ `/healthz` es **liveness pura**: no comprueba la plataforma, ni identity, ni nada externo. Un 200
aquí no dice que el BFF pueda servir una pantalla.

### Aduana (6) — el plano de autenticación

| Método y ruta | Handler | Notas |
|---|---|---|
| `GET /login` | `ShowLogin` | con sesión válida redirige 303 a `/` (`auth_handler.go:39-45`) |
| `POST /login` | `DoLogin` | 303 a `/` si autentica; repinta 400/401/503 si no |
| `GET /signup` | `ShowSignup` | alta pública |
| `POST /signup` | `DoSignup` | exige los cuatro campos y **≥ 12 caracteres** de contraseña (`signup_handler.go:24,35`) |
| `POST /logout` | `DoLogout` | **ruta PÚBLICA, registrada fuera del grupo protegido** (`server.go:206`). Solo POST: un GET no debe cerrar sesión |
| `GET /pending` | `ShowPending` | dentro del grupo protegido; es donde queda quien no tiene empresa asignada |

### Portada (1)

| Método y ruta | Handler | Notas |
|---|---|---|
| `GET /` | `ShowHome` | plan y capacidades + la tarjeta «La operación del negocio». **No se puede borrar**: es el destino de tres redirecciones |

### Técnicas (8) — protegidas por `AuthMiddleware` + `RequestDeadline(UpstreamTimeout)`

| Método y ruta | Handler | Feature que la gatea |
|---|---|---|
| `GET /variables` | `ShowTenantVariables` | ninguna |
| `POST /variables` | `DoSaveTenantVariables` | ninguna |
| `GET /integrations` | `ShowIntegrations` | `crm_bridge` |
| `POST /integrations` | `DoSaveIntegration` | `crm_bridge` |
| `POST /integrations/delete` | `DoDeleteIntegration` | `crm_bridge` |
| `GET /tenant-llm` | `ShowTenantLLM` | `api_llm` |
| `POST /tenant-llm` | `DoSaveTenantLLM` | `api_llm` |
| `POST /tenant-llm/delete` | `DoDeleteTenantLLM` | `api_llm` |

Notas de contrato que no se ven en la tabla:
- **Los tres POST repintan sobre el propio POST** (200/400/403). **No hay PRG**: un F5 tras guardar
  reenvía el formulario, y cuando algo falla se conserva lo tecleado.
- **El borrado va por POST a una ruta propia**, no por `DELETE` con campo oculto: un formulario HTML
  solo sabe GET y POST, y la traducción al verbo real la hace el `apiclient`.
- **En `/variables`, borrar ES guardar**: la API solo ofrece reemplazo del conjunto entero, así que
  quitar una fila es un `PUT` sin ella (`tenantvariables_handler.go:85-88`).
- **La feature se exige también en el GET**, y no solo aquí: la plataforma la exige en los **tres**
  verbos con su `RequireFeature`.

### Rutas que ya NO existen aquí

Diecinueve rutas de negocio se retiraron con la mudanza del `26e84c9`. Están documentadas *in situ*
en `internal/web/server.go` y cada bloque tiene un test que vigila que no resuciten **contra
`router.Routes()`, no contra el status**:

| Se fue | Rutas | Casa nueva en `wapp-client-console` | Test |
|---|---|---|---|
| Dashboard de sesiones | `POST /send`, `POST /sessions/:id/profile` | `/sesiones/enviar`, `/sesiones/:id/perfil` | `TestRutasDelDashboardYaNoExisten` (`home_test.go:396`) |
| Editor de flujos y disparadores | `GET/POST /flows`, `GET /flows/:id`, `GET/POST /triggers`, `POST /triggers/:id/delete` | `/flujos`, `/disparadores` | `TestRutasDelEditorYaNoExisten` (`home_test.go:555`) |
| Bandeja de solicitudes | `GET /intakes`, `GET /intakes/:id` y ocho POST (incluida `/intakes/:id/quote-suggestion`) | `/solicitudes` | `TestRutasDeLaBandejaYaNoExisten` (`home_test.go:634`) |
| Import de catálogo | `GET/POST /catalog-import`, `GET /catalog-import/template` | `/importar-catalogo` | `TestRutasDelImportDeCatalogoYaNoExisten` (`home_test.go:721`) |

---

## 2. Plantillas servidas

**Fuente:** `fs.ReadDir` sobre el `embed`, que es lo que el binario sirve de verdad — no una lista
compilada. **7 páginas + 1 layout**, vigilado por `TestElInventarioDePlantillasEsExactamenteElDeREQ10`
(`inventario_test.go:249`) y `TestElLayoutSigueSiendoUnoSolo` (`:301`).

| Fichero | Líneas | Ruta que lo pinta |
|---|---|---|
| `templates/layouts/base.html` | 88 | el layout de todas |
| `templates/pages/login.html` | 90 | `GET /login` |
| `templates/pages/signup.html` | 74 | `GET /signup` |
| `templates/pages/pending.html` | 26 | `GET /pending` |
| `templates/pages/home.html` | 113 | `GET /` |
| `templates/pages/tenant-variables.html` | 80 | `GET /variables` |
| `templates/pages/integrations.html` | 179 | `GET /integrations` |
| `templates/pages/tenant-llm.html` | 145 | `GET /tenant-llm` |

`TestElInventarioCubreTodaPantallaServida` (`inventario_test.go:322`) ata las dos listas en los dos
sentidos: ninguna ruta GET sin plantilla declarada, ninguna plantilla sin quien la pida.

---

## 3. Lo que el BFF CONSUME de la plataforma (`:8103`)

**Fuente:** `grep -rn '/api/v1' internal/apiclient/*.go` sobre código de producción. **Regla de
conteo:** una entrada por par método + path del upstream.

| Método y ruta upstream | Método Go | Dónde |
|---|---|---|
| `POST /api/v1/auth/login` | `AuthClient.Login` | `internal/apiclient/auth.go:51` |
| `POST /api/v1/auth/refresh` | `AuthClient.Refresh` | `internal/apiclient/auth.go:61` |
| `POST /api/v1/auth/logout` | `AuthClient.Logout` | `internal/apiclient/auth.go:71` |
| `POST /api/v1/signup` | `AuthClient.Signup` | `internal/apiclient/auth.go:97` |
| `GET /api/v1/entitlements` | `EntitlementsClient.GetEntitlements` | `internal/apiclient/entitlements.go:43` |
| `GET /api/v1/tenant-variables` | `GetTenantVariables` | `internal/apiclient/tenantvariables.go:45` |
| `PUT /api/v1/tenant-variables` | `ReplaceTenantVariables` | `internal/apiclient/tenantvariables.go:77` |
| `GET` · `PUT` · `DELETE /api/v1/integrations` | `Get/Save/DeleteIntegration` | `internal/apiclient/integrations.go:11` (constante), `:76`, `:96`, `:122` |
| `GET /api/v1/integrations/outbox` | `GetOutboxCounters` | `internal/apiclient/integrations.go:166` |
| `GET` · `PUT` · `DELETE /api/v1/tenant-llm` | `Get/Save/DeleteTenantLLM` | `internal/apiclient/tenantllm.go:11` (constante), `:88`, `:111`, `:140` |

Plazo del cliente HTTP: **15 s** por llamada (`internal/apiclient/transport.go:17`,
`defaultTimeout`), bajo el `RequestDeadline` de 20 s que acota la cadena entera.

**Con la delegación encendida** (`WAPP_IDENTITY_URL` con valor) se añaden cuatro llamadas más, que
**no vienen de este repo** sino de `wapp-shared/iam@v0.1.0`:

| Llamada | Dónde vive |
|---|---|
| `POST <identity>/api/v1/auth/login` | `iam@v0.1.0/identity.go:48` |
| `POST <identity>/api/v1/auth/refresh` | `iam@v0.1.0/identity.go:59` |
| `POST <identity>/api/v1/auth/logout` | `iam@v0.1.0/identity.go:75` |
| `POST <plataforma>/api/v1/auth/exchange` (el canje) | `iam@v0.1.0/exchange.go:40` |

El identificador de esta aplicación ante identity es **`wapp.bff`**
(`internal/apiclient/delegated.go:21`, constante `SystemBFF`). Es un valor de contrato, no
configuración: identity conoce esta aplicación con el mismo nombre en todos sus ambientes.
⚠️ `wapp-client-console` usa **el mismo** `system`: es una decisión, no un descuido.

**Con `WAPP_IDENTITY_JWKS_URL` puesta**, además, un `GET` al JWKS **en el arranque** y fail-closed:
si no responde, el BFF no arranca (`internal/bootstrap/identity.go:26-33`).

---

## 4. gRPC, CLI y base de datos: NINGUNO

Los tres verificados **por ausencia**:
- **gRPC** — no hay `google.golang.org/grpc` en `go.mod` (solo `protobuf` indirect, arrastrado por
  gin), ni ficheros `.proto`, ni importación de `wapp-cloudlink`. Está escrito en
  `internal/web/server.go:4-5`.
- **CLI** — un solo `main.go` de 45 líneas, sin `flag.Parse()` y sin `os.Args`. **`-version` no
  existe**: el binario lo ignora y arranca.
- **Base de datos** — sin driver SQL, sin `database/sql`, sin migraciones y sin versión de esquema.
  **El BFF no toca ninguna tabla.** Toda la persistencia vive en la plataforma y se alcanza por
  REST. Está escrito en `internal/config/config.go:3-5`.

---

## 5. Variables de entorno

**Fuente:** `internal/config/config.go:142-181`, el único `Load()`. **Nombre efectivo:** el loader
compone el prefijo con `sharedconfig.WithEnvPrefix("WAPP_")` (`config.go:143`), así que el literal
`GUARDIAN_HTTP_ADDR` del código **se pone en el entorno como `WAPP_GUARDIAN_HTTP_ADDR`**. Abajo van
ya con su nombre efectivo. Son **23 literales** de entorno, uno de ellos alias legado del selector Alpha.

| Variable | Valor por defecto | Qué hace |
|---|---|---|
| `WAPP_GUARDIAN_ENV` | `local` | ambiente lógico. **Cualquier valor distinto de `local` endurece los defaults** (Secure + HSTS) y pasa el log a JSON |
| `WAPP_GUARDIAN_HTTP_ADDR` | `:8104` | dirección de escucha. Sin host, bindea a todas las interfaces |
| `WAPP_PUBLIC_API_BASE` | `http://localhost:8103` | URL base de la API pública de la plataforma |
| `WAPP_GUARDIAN_COOKIE_SECURE` | `env != local` | marca `Secure` la cookie de sesión |
| `WAPP_GUARDIAN_COOKIE_SAMESITE` | `lax` | `lax` · `strict` · `none` (`none` obliga `Secure`) |
| `WAPP_GUARDIAN_ALLOWED_ORIGINS` | `""` | allowlist CORS en CSV. Vacío = same-origin estricto. **Nunca `*`** |
| `WAPP_GUARDIAN_TRUSTED_PROXIES` | `""` | 🔴 vacío = no se confía en ningún proxy y `ClientIP()` ignora `X-Forwarded-For`. **No está en `.env.example`** |
| `WAPP_GUARDIAN_HSTS_ENABLED` | `env != local` | emite `Strict-Transport-Security` |
| `WAPP_GUARDIAN_RATE_ENABLED` | `true` | enciende el rate-limit por IP o `user_id` |
| `WAPP_GUARDIAN_RATE_RPS` | `5` | tasa sostenida por clave |
| `WAPP_GUARDIAN_RATE_BURST` | `10` | ráfaga máxima por clave |
| `WAPP_GUARDIAN_READ_HEADER_TIMEOUT_SECS` | `5` | anti-slowloris |
| `WAPP_GUARDIAN_READ_TIMEOUT_SECS` | `15` | — |
| `WAPP_GUARDIAN_WRITE_TIMEOUT_SECS` | `30` | — |
| `WAPP_GUARDIAN_IDLE_TIMEOUT_SECS` | `60` | — |
| `WAPP_GUARDIAN_SHUTDOWN_TIMEOUT_SECS` | `10` | plazo del apagado graceful. **No está en `.env.example`** |
| `WAPP_GUARDIAN_UPSTREAM_TIMEOUT_SECS` | `20` | acota **toda** la cadena hacia la API (intento → refresco → reintento). **Debe quedar por debajo de `WRITE_TIMEOUT`**; 0 o negativo lo desactiva |
| `WAPP_ALPHA_TEST_ACCOUNTS` | `false` | selector de cuentas de prueba en el login. **Alias legado: `WAPP_ENABLE_ALPHA_LOGIN`** |
| `WAPP_ALPHA_TEST_PASSWORD` | `""` | contraseña que autocompleta el selector. Vacía ⇒ el operador la teclea. **No está en `.env.example`** |
| `WAPP_GUARDIAN_CLIENT_CONSOLE_URL` | `""` | dirección de `wapp-client-console` para el enlace de la portada. Vacío ⇒ **no se pinta enlace** |
| `WAPP_IDENTITY_JWKS_URL` | `""` | puerta del modo dual. Con valor, verificador fail-closed en el arranque |
| `WAPP_IDENTITY_URL` | `""` | puerta de la delegación. Con valor, login y refresco van a identity |

⚠️ **`WAPP_LOG_LEVEL` aparece en el `.env` de UAT y este repo NO la lee**: no está en
`internal/config/config.go`. El nivel del logger se fija a `slog.LevelInfo` en el código
(`cmd/guardian-bff/main.go:27,29` — las dos ramas del `if jsonLogs`).

---

## 6. Ficheros y cookies

**Ficheros que escribe: NINGUNO.** Verificado por ausencia: no hay `os.Create`, `os.WriteFile` ni
`os.OpenFile` en el árbol de producción. La única salida es **stdout**, vía `slog`.

**Ficheros que lee: NINGUNO en ejecución.** Las plantillas y el CSS van **embebidos en el binario**
(`//go:embed`), así que no se leen del disco y no se pueden cambiar sin recompilar. El `.env` lo lee
systemd, no el proceso.

**Cookies que pone** — son tres, y ninguna es de un solo uso:

| Cookie | Quién la nombra | Contenido | Vida |
|---|---|---|---|
| `wapp_guardian_session` | `internal/web/policy.go:19` | el par access+refresh codificado. **HttpOnly siempre**; `Secure` y `SameSite` por configuración | la del `exp` del token (`SessionMaxAge`) |
| `wapp_csrf` | `internal/web/policy.go:20` | token double-submit | 12 h (el TTL del módulo compartido) |
| la del nonce CSP | la pone `wapp-shared/web` | — | por petición |

⚠️ `wapp_csrf` es **el valor por defecto del módulo compartido**
(`DefaultCSRFCookieName`): este BFF es el fork cuyo nombre se quedó como default, y las otras dos
consolas lo pasan explícito para no heredarlo y pisarse la cookie en el mismo navegador.
