# Adapter de modelo real — Design

## Resumen de la solución

Se ampliará `internal/model` con una `Session` neutral por run que implementa el
puerto `harness.Model`, posee presupuestos y delega exactamente un intento a un
`Provider` seleccionado por composición. El harness seguirá recibiendo solo
`ModelDecision`; la sesión retendrá y expondrá snapshots propios de consumo y
emitirá telemetría por un puerto seguro. Los DTO, SSE y errores de OpenAI
permanecerán en `internal/model/openai`.

El bearer OAuth no se entregará a `Session`, al harness ni al adapter de
traducción. Un componente OpenAI de autenticación resolverá el perfil elegido,
gestionará registro/sign-in/refresh/revocación y proporcionará un transporte
autenticado que añade el header en la frontera HTTP. El credential store y las
operaciones de browser/callback serán puertos sustituibles para pruebas.

```text
provider + model + auth profile + budgets
        -> composition validates selection
        -> OpenAI OAuth profile boundary
        -> provider-neutral model.Session
             -> reserve call / check tokens and deadline
             -> OpenAI adapter
                  -> authenticated HTTP transport
                  -> POST /v1/responses (store:false, stream:true)
                  -> bounded SSE through response.completed
                  -> exact JSON decision + internal validation
             -> account confirmed usage
             -> emit safe terminal telemetry
        -> harness receives ModelDecision only
```

## Fuentes de contexto consultadas

| Fuente | Evidencia aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Feature 9 única, criterios originales y puerta SDD. |
| `.codex/agents/spec.md`, `docs/specs.md` | Requirements EARS, bloqueo previo y trazabilidad. |
| `docs/architecture.md` | Puerto consumidor, decisiones tipadas, presupuestos, datos no confiables y ausencia de herramientas directas. |
| `docs/conventions.md`, `docs/verification.md` | Contextos, errores tipados, secretos, comentarios, tests herméticos y revisión. |
| `internal/harness/{protocol,state,harness}.go` | Contrato `Model`, request/decision v1, secuencia del harness y budgets existentes. |
| `internal/model/fake.go` | Fake programable que debe conservar tests offline. |
| Features 1–8 | Validación estructural, separación de adapters, redacción fail-closed, publicación y contexto OpenAPI no confiable. |
| [OpenAI OSS token-sharing overview](https://developers.openai.com/siwc/token-sharing-open-source) | Client dinámico, host ID estable y recorrido hasta inferencia terminal. |
| [OpenAI registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) | PKCE, loopback, scopes/resource, exchange, ID-token validation y almacenamiento protegido. |
| [OpenAI models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference) | Catálogo dependiente de cuenta, Responses endpoint, bearer OAuth, `store:false`, `stream:true` y terminales SSE. |
| [OpenAI preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations) | Campos/inputs/tools prohibidos y ausencia de estado persistido. |
| [OpenAI accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions) | Separación de perfiles, refresh rotatorio serializado, revocación y protección de credenciales. |
| [OpenAI errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery) | Status/codes de admisión, límites, indisponibilidad, auth y refresh terminal. |

La documentación de OpenAI describe un capability preview para apps open
source/locales elegibles. La spec no promete elegibilidad, modelos concretos,
límites de cuenta ni disponibilidad. El modelo es una selección opaca obtenida
por el usuario para su cuenta; no se embebe un catálogo.

## Contratos externos observados

### Sign in with ChatGPT

El primer registro usa `client_id=dynamic_agent_client`, nombre real de la app,
un `ext_agent_host_id` estable y opaco, y valores nuevos de state, nonce y PKCE
S256. La autorización usa el endpoint oficial, callback HTTP en `127.0.0.1`,
scopes `openid profile email offline_access resource.invoke
chatgpt.tokens.use.direct` y resource `https://api.openai.com/v1`. El callback
de primer registro entrega el issued client ID que se usa en el intercambio y
posteriores sign-ins.

El ID token se valida contra JWKS y se comprueban issuer, audience, expiry,
nonce e identidad. El scope directo se comprueba por separado. El refresh usa
el issued client ID y reemplaza atómicamente el token set rotatorio. Las
registrations no se combinan aunque compartan email.

### Responses API para ChatGPT plan usage

La inferencia usa exclusivamente `POST https://api.openai.com/v1/responses`,
Authorization bearer añadido por el transporte autenticado, el slug de modelo
seleccionado, input autocontenido, `store:false` y `stream:true`. No se usa
`previous_response_id`; la historia requerida viaja en cada input. Se omiten
todos los campos declarados incompatibles por el preview, incluidos
`max_output_tokens`, metadata, sampling, background, conversation y tools no
soportadas.

El adapter consume SSE hasta `response.completed`. `response.failed`,
`response.incomplete`, fin prematuro o interrupción no son éxito. Los errores
pre-stream pueden carecer del objeto API estándar. Los códigos/estado/request ID
se conservan como metadata segura acotada; el body no cruza la frontera.

## Componentes y responsabilidades

### `model.Config` y selección

Modelo propio con provider ID, model ID, auth profile ID, timeout por llamada,
bytes máximos de request/stream/output y `BudgetLimits`. No contiene token,
endpoint alternativo, precio ni moneda. El constructor valida todos los valores
antes de resolver el provider. El model ID solo se valida como string no vacío.

La composición mantiene un registry cerrado de provider factories. Solo
`openai` existe en esta entrega. La selección no usa reflection, nombres de
packages ni fallback.

### `model.Session`

Una sesión pertenece a un run y contiene provider configurado, reloj, observer,
deadline total, límites y consumo protegido. Implementa `harness.Model.Decide`:

1. comprueba cancelación/duración y consumo confirmado;
2. reserva atómicamente una model call;
3. deriva el deadline efectivo;
4. delega al provider;
5. exige uso confirmado en éxito, lo contabiliza aun si cruza el máximo;
6. emite un único terminal record;
7. devuelve únicamente la decisión interna ya validada.

`Snapshot` devuelve una copia del consumo. `ReserveReplan` reserva el contador
que usarán features posteriores; no genera ni ejecuta un plan. La sesión no
recibe mutable `Run`, registry, policy, evidence store ni herramientas.

### Contrato neutral `Provider`

Interfaz pequeña propiedad de `internal/model`. Recibe contexto, model ID y una
petición propia derivada de `harness.ModelRequest`; devuelve decisión, usage y
metadata segura. El provider no decide budgets. La traducción entre
`ModelRequest` y el envelope neutral etiqueta objetivos, observations y contexto
externo como datos. La lista de tool descriptors es descriptiva y no contiene
handles.

El `Fake` existente se conserva como implementación directa del puerto del
harness para sus tests. Se amplía o se acompaña de un provider fake para probar
`Session` sin cambiar el carácter offline del harness.

### `openai.Adapter`

Traduce el envelope neutral a DTOs privados. Genera instrucciones para devolver
exactamente el JSON v1 que representa `harness.ModelDecision`, pero no confía en
la obediencia del modelo: el output se decodifica con límites, `UseNumber`, EOF
obligatorio y `harness.ValidateDecision` con el constraint esperado.

No se envían tools de Responses. Los tool descriptors y OpenAPI/contexto se
incluyen solo como datos serializados y acotados. El adapter no interpreta una
tool call del proveedor ni ejecuta nada.

### `openai.StreamDecoder`

Lee status/headers y SSE mediante buffers explícitamente limitados. Solo agrega
deltas de output text dentro del límite. Reconoce terminales completado,
fallido e incompleto; exige exactamente un completado, usage consistente y fin
válido. Ignora eventos informativos conocidos que no aportan output, pero un
terminal desconocido o una secuencia contradictoria falla cerrada. DTOs y event
names no salen del paquete.

### Transporte autenticado

El adapter depende de un `AuthenticatedDoer` definido por el consumidor
OpenAI. La implementación se construye en composición con un perfil y añade el
bearer después de recibir el request, por lo que el adapter y `model.Session` no
pueden observarlo. Se deshabilitan redirects fuera del endpoint exacto y
retries automáticos. El transporte respeta contexto y límites de headers/body.

### `openai/oauth.ProfileService`

Orquesta registro, reautorización, refresh y revocación mediante puertos para
browser, listener loopback, HTTP, reloj, randomness, ID-token validator y
credential store. Un intento posee state/nonce/verifier y termina una vez. La
activación de perfil ocurre solo tras validar identidad, issued client y scopes.

El store recibe un record secreto únicamente dentro del paquete de auth. Su
adapter local usa ruta decidida por composición, escritura atómica y protección
owner-only equivalente del sistema operativo; no existe ruta global embebida.
Errores/logs nunca incluyen records, authorization URL con hints ni claims.

Un coordinador por profile serializa refresh. No se mantiene un lock durante
red: el coordinador marca un refresh en vuelo, libera el lock y notifica el
resultado a waiters mediante una primitiva cancelable.

### Errores

`model.Error` contiene `Category`, `Operation`, status/code/request ID seguros y
`Cause`. `Error()` usa texto controlado. La causa externa con payload se retiene
solo en un wrapper privado que no se formatea ni se expone a observers; las
causas estándar como `context.Canceled`/`DeadlineExceeded` sí permiten
`errors.Is`.

Mapeo inicial:

| Condición | Categoría |
|---|---|
| config/selección local inválida | `invalid_config` |
| JSON/decisión/SSE/usage terminal inválidos | `invalid_response` |
| contexto cancelado | `cancelled` |
| deadline efectivo | `timeout` |
| OAuth inválido, refresh terminal, HTTP 401 o código auth documentado | `authentication` |
| HTTP 429 o usage-limit code documentado | `rate_limit` |
| 503 temporal/codes de indisponibilidad y otros 5xx | `provider_transient` |
| 400/403 no-auth, capability/route/model/input rechazado | `provider_rejected` |
| budget local agotado | `budget_exhausted` |
| invariantes/observer/store internos | `internal` |

No se clasifica por textos de `detail` o message. Un status/code desconocido se
mantiene como metadata y se resuelve por clase de status o `provider_rejected`.

### Telemetría

`AttemptRecord` incluye run ID, attempt ordinal, provider, model, duración,
outcome/category y usage numérico confirmado. No incluye profile ID, account,
request/response body, prompt, output, email, authorization URL o token. El
observer es obligatorio y confirma el record; un fallo impide retornar una
decisión que no quedó auditada, conservando el fallo original como metadata
segura.

El journal actual no se modifica para almacenar contenido del modelo. Una
composición posterior puede adaptar `AttemptRecord` a un event seguro sin
cambiar este contrato.

### Smoke executable opt-in

Un ejecutable dedicado fuera de la suite ordinaria ofrece dos acciones
explícitas: provisionar/actualizar un perfil mediante OAuth interactivo y hacer
una inferencia smoke con provider/model/profile/budgets explícitos. Requiere un
flag inequívoco de opt-in. No se invoca desde `go test`, `go generate`, builds o
la CLI local normal. Su output es el mismo record seguro, nunca el JSON producido
ni secretos.

## Paquetes afectados

```text
internal/harness/
  protocol.go                 # conserva el puerto consumidor y validación v1
  *_test.go                   # regresión de compatibilidad, si es necesaria

internal/model/
  fake.go                     # fake offline existente
  config.go                   # selección y límites neutrales
  session.go                  # budgets, deadline y puerto harness.Model
  provider.go                 # request/result/usage/error/observer propios
  *_test.go

internal/model/openai/
  adapter.go                  # traducción Responses privada
  stream.go                   # SSE acotado y terminales
  transport.go                # frontera HTTP autenticada
  errors.go                   # clasificación provider -> model.Error
  oauth/
    service.go                # registro, sign-in, refresh y revocación
    model.go                  # perfiles/records privados y puertos
    local_store.go            # persistencia protegida elegida por composición
    *_test.go
  *_test.go

cmd/featureproof-model-smoke/
  main.go                     # utilidad real opt-in; no forma parte de validate
```

La distribución exacta en archivos puede cambiar si mantiene estas
responsabilidades. No se modifican planning, tools, evidence, OpenAPI,
specification ni el manifest de la CLI.

## Flujo principal

### Provisionar un perfil

1. Composición carga/genera una vez el host ID y elige el profile ID local.
2. `ProfileService` abre un intento con state/nonce/PKCE nuevos y listener
   `127.0.0.1` ya activo.
3. El browser completa registro/consent; el callback valida state y issued
   client ID.
4. Se intercambia el code con verifier/resource/redirect idénticos.
5. Se valida ID token, identidad y scope directo.
6. El store confirma atómicamente registration y token set protegido.
7. Solo entonces el perfil puede construir un transporte autenticado.

### Obtener una decisión

1. Composición valida provider/model/profile y construye una `Session`.
2. `Decide` valida contexto, duración y budgets; reserva call.
3. El perfil entrega un transporte con access token vigente, refrescando si
   corresponde.
4. OpenAI adapter serializa input completo y realiza una única petición.
5. Stream decoder agrega output acotado hasta `response.completed`.
6. Se exige usage y se valida JSON/decision v1.
7. Session contabiliza tokens, emite AttemptRecord y retorna ModelDecision.
8. El harness vuelve a aplicar sus validaciones, registry, policy y tool budget.

### Reintento explícito

1. Un caller recibe categoría transitoria/rate limit.
2. Decide externamente si su política permite retry.
3. Una nueva llamada vuelve a reservar budget y crea otro attempt ID.
4. Adapter hace una sola petición; no existe retry interno oculto.

## Flujos de error

- Config inválida: falla antes de profile/store/network.
- Profile ausente/inválido o scope denegado: `authentication`; no fallback.
- Error OAuth temporal: conserva credenciales previas utilizables; no mezcla
  token sets.
- Refresh terminal: invalida token set utilizable y exige nuevo sign-in.
- Budget agotado/cancelación/duración: no resuelve auth ni abre red.
- Admission/HTTP error: clasifica por status/code sin interpolar body.
- Stream fallido/incompleto/cortado/oversized: no retorna decisión parcial.
- JSON o decisión inválidos: `invalid_response`; harness no la observa.
- Usage ausente en éxito: `invalid_response` y sesión queda cerrada a nuevo
  trabajo porque el consumo ya no puede conocerse de forma segura.
- Observer fail: `internal`; la decisión no se publica al harness.

## Propiedad del estado

- Cada `Session` posee budgets, contadores, deadline y ordinal de attempts de un
  run. `Snapshot` copia valores.
- Cada OAuth attempt posee state/nonce/verifier/callback y no se reutiliza.
- Cada profile store record pertenece a una registration/identidad verificadas.
- Cada request/decoder posee buffers locales; no hay cache global de prompts,
  outputs o modelos.
- El caller posee las copias de decisiones y snapshots devueltos.

## Concurrencia y cancelación

El harness actual llama secuencialmente, pero reservas y refresh se prueban bajo
carrera. Mutex protege solo contadores o estado corto. No se mantiene durante
HTTP, browser, store ni callbacks. Cada waiter de refresh puede cancelar sin
cancelar el refresh que todavía necesitan otros waiters; el propietario de la
operación define cuándo termina el trabajo compartido.

No se guardan contextos en servicios duraderos. Todas las operaciones externas
reciben `context.Context` primero. No hay goroutines ocultas en constructores;
listener/refresh coordinado tienen owner, cierre y wait explícitos.

## Presupuestos y duración

`BudgetLimits` contiene `MaxCalls`, `MaxTokens`, `MaxDuration` y `MaxReplans`.
Los counts pueden ser cero para denegar esa dimensión; las duraciones deben ser
positivas. Una reserva es atómica. Los tokens se suman tras terminal completado
y pueden cruzar el máximo, porque el preview obliga a omitir
`max_output_tokens`; antes de la siguiente llamada se bloquea. Usage ausente
cierra la sesión para decisiones posteriores.

No existe `MaxCost`, pricing, moneda, estimación ni descarga de tarifas. Esto es
la sustitución deliberada del acceptance criterion antiguo autorizada por el
usuario y debe quedar visible en reportes de implementación/review.

## Retries e idempotencia

Inferencia y OAuth no se reintentan dentro de una invocación. Cada inferencia es
un intento único y consume call budget. Refresh se coalescea para evitar usar
dos veces un rotating token; no se duplica como retry. Revocación puede ser
reintentada solo por el caller con backoff cancelable mientras conserve el
refresh token, conforme al contrato oficial.

No se usa idempotency key externa no documentada. La protección consiste en no
ocultar retries y en separar attempt IDs.

## Observabilidad y secretos

La observabilidad usa allowlist de campos. El provider adapter no registra. La
capa superior decide dónde persistir el record seguro. Headers Authorization,
OAuth query params, ID/access/refresh tokens, profile/account IDs, email,
prompts, input, output, SSE bodies y error detail se consideran secretos o datos
no confiables y no se formatean.

Tests colocan canarios distintos en cada superficie y escanean error chains
publicables, observers y output del smoke fake. Los secrets solo viven el
tiempo necesario en records privados/transport y se copian lo mínimo posible.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá un
comentario breve que explique su responsabilidad dentro de FeatureProof y su
relación con el runtime, OAuth, traducción, presupuestos u observabilidad. El
comentario describirá intención, invariantes o motivo arquitectónico y no
repetirá la sintaxis. Los comentarios existentes se actualizarán cuando cambie
su responsabilidad.

## Estrategia de pruebas

Tests ordinarios usan `httptest` o un `RoundTripper` coordinado, browser/callback
fake, JWKS/token validator fake, store temporal/in-memory, clock controlado y
barriers. Ninguna prueba ordinaria conoce una credencial real o resuelve DNS
externo. El smoke es un executable separado y requiere opt-in explícito.

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | config/registry/session | `TestSessionRequiresExplicitProviderModelProfileTimeoutAndBudgets`; `TestSessionsSelectOnlyConfiguredProviderAndOwnState` |
| R2 | OAuth profile service | `TestOAuthRegistrationUsesHostPKCEStateNonceScopesAndIssuedClient`; `TestOAuthRejectsInvalidCallbackIdentityTokenAndScope`; `TestOAuthRefreshIsSerializedAndRotatesAtomically`; `TestProfilesNeverMixCredentialsAndSignOutRevokes` |
| R3 | OpenAI encoder/transport | `TestOpenAIRequestUsesOnlyResponsesOAuthStoreFalseStreamTrueAndSelectedModel`; `TestOpenAIRequestOmitsPreviewUnsupportedFieldsToolsAndSecrets` |
| R4 | stream/decision decoder | `TestOpenAIStreamReturnsOnlyCompletedExactValidatedDecisionWithUsage`; `TestOpenAIStreamRejectsMalformedTrailingOversizedFailedIncompleteAndPrematureResponses` |
| R5 | error mapper | `TestModelErrorsClassifyConfigContextHTTPProviderAndResponseWithoutTextMatching`; `TestModelErrorsNeverExposeCanaries` |
| R6 | session/auth/transport | `TestSessionUsesShortestDeadlineAndPropagatesCancellationAcrossAllBoundaries`; `TestCancelledOrExpiredSessionDoesNoExternalWork` |
| R7 | observer | `TestEveryAttemptEmitsOneSafeTerminalRecordWithConfirmedUsage`; `TestObserverFailureIsFailClosedAndAllExcludedCanariesRemainAbsent` |
| R8 | budgets | `TestSessionReservesCallsAndReplansAtomicallyAndBlocksExhaustedDimensions`; `TestTokenUsageMayCrossOnceThenBlocksNextCall`; `TestMissingUsageClosesSession`; `TestBudgetModelContainsNoCostOrPricing` |
| R9 | attempts/refresh | `TestAdapterNeverRetriesAndExplicitRetryConsumesAnotherCall`; `TestConcurrentRefreshUsesOneRotatingTokenExchange` |
| R10 | dependency boundary | `TestAdapterHasNoExecutableCapabilitiesAndNeverMutatesRunEvidenceOrVerdict`; harness regression tests |
| R11 | all packages | `TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies`; race tests for session and refresh |
| R12 | smoke composition | Fake-process tests for opt-in/arguments/safe output plus documented manual real invocation outside `go test` |

Además se cubren SSE dividido arbitrariamente, CRLF, evento desconocido,
multiple terminales, JSON numérico, límites exactos, request ID/code oversized,
refresh waiter cancelado, store fail-closed y provider factory duplicada.

## Alternativas y decisiones

- **SDK del proveedor en el harness:** descartado; viola la frontera propia y
  filtraría DTOs.
- **API key o variable de entorno:** descartado por la decisión de usar perfiles
  Sign in with ChatGPT OAuth. El smoke tampoco acepta bearer por flag/env.
- **Modelo por defecto:** descartado; disponibilidad depende de cuenta y el
  usuario debe seleccionar un slug válido.
- **Endpoint configurable/OpenAI-compatible:** descartado en v1; ampliaría
  confianza, auth y clasificación fuera de lo acordado.
- **Structured outputs dependientes de un campo provider:** no se necesita para
  la seguridad; se pide JSON y se valida localmente. El provider no controla el
  flujo hasta superar el validator interno.
- **Enviar function tools a Responses:** descartado; el adapter no ejecuta ni
  debe inducir una segunda vía de capacidades.
- **Pricing embebido o descargado:** descartado por decisión explícita y por
  variabilidad externa. Se usan límites no monetarios.
- **Cortar exactamente al llegar al token budget:** no es prometible en este
  flujo porque `max_output_tokens` debe omitirse. Se contabiliza el terminal y
  se impide la siguiente llamada.
- **Retry automático del cliente:** descartado; ocultaría consumo, intentos y
  errores, y rompería budgets auditables.
- **Incluir el adapter en `featureproof validate`:** descartado; feature 7 tiene
  flujo determinista aprobado y feature 10 será dueña de generación agéntica.

## Conformidad arquitectónica

El diseño preserva ARCH-001/ARCH-002 porque el modelo solo produce una decisión
no autoritativa; ARCH-004 porque no calcula el veredicto; ARCH-005 mediante
budgets previos; ARCH-006 con contextos/deadlines; ARCH-007 mediante records por
attempt; ARCH-008/ARCH-009 mediante separación de instrucciones, allowlists y
secret boundary; ARCH-010 mediante tests herméticos; ARCH-011 porque policy
sigue fuera; ARCH-012 porque el adapter no muta run/evidence.

Cumple CONV-002 a CONV-009: paquetes cohesionados, interfaces consumidoras,
cancelación, errores tipados, ciclos de goroutines explícitos, secretos fuera de
artefactos y tests deterministas/trazables. No se solicita excepción.

## Mapa inicial R → tasks

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T8 | Selección/config/session neutrales y sin defaults. |
| R2 | T3, T4, T8 | Perfil OAuth seguro con identidad, rotación y revocación. |
| R3 | T5, T8 | Request Responses exacto, acotado y sin tools/secrets. |
| R4 | T5, T6, T8 | Solo stream completado produce decisión v1 válida con usage. |
| R5 | T1, T4, T6, T8 | Toda frontera devuelve categorías seguras y distinguibles. |
| R6 | T2, T4, T5, T8 | Deadline mínimo y cancelación atraviesan auth/HTTP/SSE. |
| R7 | T2, T6, T8 | Un record seguro por intento y fail-closed del observer. |
| R8 | T2, T8 | Calls/tokens/duration/replans sin pricing se reservan/contabilizan. |
| R9 | T2, T4, T5, T8 | Sin retry oculto y refresh rotatorio serializado. |
| R10 | T1, T5, T7, T8 | Ningún handle ejecutable o mutación de dominio cruza el adapter. |
| R11 | T1–T8 | Suite completa local, determinista y race-safe. |
| R12 | T7, T8 | Smoke real opt-in separado con salida segura. |
