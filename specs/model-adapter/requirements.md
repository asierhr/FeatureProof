# Adapter de modelo real — Requirements

## Objetivo y problema

FeatureProof necesita obtener decisiones estructuradas de un modelo real sin
acoplar el harness a un proveedor, SDK, transporte o mecanismo de
autenticación. La frontera debe convertir contexto interno acotado en una
petición externa, aceptar únicamente una decisión válida y devolver metadatos
de consumo seguros para que el núcleo controle sus presupuestos.

La primera integración real utilizará OpenAI Responses API con un access token
obtenido mediante Sign in with ChatGPT para aplicaciones open source elegibles.
El usuario seleccionará explícitamente proveedor, modelo y perfil de
autenticación; no existirá un modelo por defecto.

## Alcance

- Definir un runtime y contratos internos independientes del proveedor.
- Ampliar el puerto consumidor del harness para recibir decisión, uso y
  telemetría segura mediante modelos propios.
- Validar al arrancar proveedor, modelo, perfil, timeout y presupuestos.
- Implementar perfiles OpenAI Sign in with ChatGPT: registro dinámico,
  autorización PKCE, validación de identidad/scopes, refresh y cierre de sesión
  detrás de fronteras de composición sustituibles.
- Implementar un adapter OpenAI para `POST https://api.openai.com/v1/responses`
  con bearer OAuth, `store:false`, `stream:true` y consumo SSE hasta el evento
  terminal.
- Serializar la petición interna y validar la decisión JSON v1 antes de
  entregarla al harness.
- Clasificar fallos, propagar cancelación, aplicar timeout y emitir telemetría
  segura.
- Controlar por sesión llamadas al modelo, tokens confirmados, duración total y
  replanificaciones reservadas.
- Probar el comportamiento ordinario con transporte/OAuth falsos y ofrecer una
  prueba real opt-in separada de `go test`.

## Fuera de alcance

- Elegir automáticamente proveedor, modelo o perfil.
- Incluir nombres de modelos por defecto o un catálogo estático de modelos.
- Usar API keys, client secrets, ChatGPT `backend-api` o un endpoint alternativo.
- Incluir tarifas, consultar pricing o aplicar presupuestos monetarios.
- Ejecutar herramientas, recibir handles del registry o convertir tool calls
  del proveedor en acciones.
- Generar el plan completo de la feature 10, ejecutar planes o calcular
  veredictos.
- Implementar replanificación; esta feature solo entrega la reserva neutral que
  una iteración posterior deberá usar.
- Añadir retries automáticos dentro del adapter.
- Integrar todavía login, selección de perfil o selección de modelo en la CLI
  pública de `featureproof validate`.
- Soportar aplicaciones pagadas/remotamente alojadas fuera de la elegibilidad
  documentada para el flujo open source.

## Actores y términos

- **Caller/composición:** capa que valida la selección, resuelve el perfil y
  construye una sesión de modelo sin entregar secretos al harness.
- **Runtime neutral:** componente por sesión que aplica budgets y delega una
  inferencia a un adapter seleccionado.
- **Provider adapter:** traductor privado entre modelos propios y el contrato
  externo del proveedor.
- **Auth profile:** identidad opaca elegida por el usuario; no contiene tokens
  en contratos del core.
- **Credential boundary:** componente de composición que autoriza, protege,
  refresca y entrega credenciales solo al transporte OpenAI.
- **Model session:** propietario aislado de configuración, consumo y deadline
  de un run.
- **Confirmed usage:** tokens reportados por una respuesta terminal válida.
- **Safe telemetry:** proveedor, modelo, duración, contadores de tokens y
  categoría técnica; nunca prompts, respuestas o credenciales.
- **Structured decision:** `harness.ModelDecision` v1 validada por el contrato
  interno y no autoritativa sobre herramientas o veredictos.

## Requirements

### R1 — Selección explícita y frontera neutral

WHEN a caller creates a model session,
THE model runtime SHALL require an explicitly supported provider, a non-empty
model identifier, a non-empty authentication-profile identifier, positive
timeouts and non-negative explicit budgets, SHALL reject unknown or duplicate
configuration before authentication or network I/O, and SHALL NOT select a
default model or expose provider DTOs through the harness port.

#### Acceptance criteria

- GIVEN valid provider, model, profile, timeout and budgets
  WHEN a session is created
  THEN its public request/result/error contracts contain only FeatureProof
  types.
- GIVEN an empty model or profile, unknown provider, non-positive timeout, or
  negative budget
  WHEN configuration is validated
  THEN it returns `invalid_config` before resolving credentials or invoking the
  transport.
- GIVEN two sessions with different provider selections
  WHEN they are used independently
  THEN each delegates only to its selected adapter and does not share mutable
  consumption.

### R2 — Perfil OpenAI OAuth aislado

WHEN the selected provider is OpenAI with a Sign in with ChatGPT profile,
THE composition boundary SHALL use the official dynamic-client OAuth flow with
a stable opaque host identifier, fresh state/nonce/PKCE per attempt, loopback
callback, required identity and ChatGPT-plan scopes, ID-token validation and
rotating refresh, SHALL keep registrations separate by issued client and
verified identity, and SHALL make tokens available only to the authenticated
OpenAI transport.

#### Acceptance criteria

- GIVEN a first sign-in
  WHEN authorization starts
  THEN it uses `dynamic_agent_client`, the persisted host ID, the app name,
  fresh state/nonce/PKCE, the documented scopes/resource and a `127.0.0.1`
  loopback callback; the issued client ID returned for registration is used for
  token exchange and persistence.
- GIVEN a callback with mismatched state, missing issued client ID for a new
  registration, invalid ID-token signature/issuer/audience/expiry/nonce,
  changed account identity or missing `chatgpt.tokens.use.direct`
  WHEN the profile is activated
  THEN no inference credential is exposed.
- GIVEN an expired access token and usable refresh token
  WHEN the profile is resolved
  THEN refresh is serialized per session and the access token, expiry, scopes
  and rotating refresh token are replaced together only after success.
- GIVEN two saved profiles
  WHEN one is selected
  THEN its client ID and tokens are never combined with the other profile.
- GIVEN sign-out
  WHEN revocation is attempted
  THEN inference stops; local tokens are cleared after confirmed revocation or
  after reporting that remote revocation could not be confirmed.

### R3 — Petición OpenAI permitida

WHEN the OpenAI adapter sends an inference request,
THE adapter SHALL call only `POST https://api.openai.com/v1/responses` with the
selected model, bearer OAuth credential, complete bounded input history,
`store:false` and `stream:true`, SHALL omit fields prohibited by the current
Sign in with ChatGPT preview contract, and SHALL NOT send tools, credentials or
an executable registry to the model.

#### Acceptance criteria

- GIVEN a valid internal request
  WHEN the fake transport captures the outbound request
  THEN method, URL, bearer placement, model, input, `store` and `stream` match
  this requirement and no secret occurs in the body.
- GIVEN an instruction and untrusted context
  WHEN the request is encoded
  THEN privileged instructions remain in the provider-supported instruction or
  developer position and untrusted context remains data in the input array; no
  explicit system-message input is emitted.
- GIVEN any internal request
  WHEN it is encoded
  THEN it omits the unsupported preview fields, provider-hosted tools and all
  executable FeatureProof dependencies.

### R4 — Respuesta estructurada validada

WHEN OpenAI returns a stream,
THE adapter SHALL enforce a configured byte limit, accept success only after
`response.completed`, assemble the model's output text into exactly one JSON
decision, decode numbers without lossy coercion, reject trailing content, and
apply the versioned FeatureProof decision validator before returning a result.

#### Acceptance criteria

- GIVEN output-text deltas containing one valid decision followed by
  `response.completed` with usage
  WHEN the stream is consumed
  THEN the adapter returns the equivalent internal decision and confirmed
  usage.
- GIVEN malformed JSON, unsupported decision version/kind, missing required
  fields, trailing JSON/text, output over the limit, a stream ending without
  `response.completed`, `response.failed`, `response.incomplete`, or missing
  usage
  WHEN decoding finishes
  THEN no decision is returned and the failure is typed.
- GIVEN a model claim about a verdict
  WHEN the decision is validated
  THEN existing deterministic harness rules remain authoritative.

### R5 — Taxonomía estable de errores

IF configuration, authentication, transport or provider processing fails,
THEN THE model boundary SHALL return an error distinguishable with
`errors.Is`/`errors.As` as one of `invalid_config`, `invalid_response`,
`timeout`, `cancelled`, `authentication`, `rate_limit`, `provider_transient`,
`provider_rejected`, `budget_exhausted` or `internal`, SHALL preserve the safe
underlying cause, HTTP status, provider code and request ID when available, and
SHALL NOT expose response bodies, OAuth values, prompts or output text in its
public message.

#### Acceptance criteria

- GIVEN context cancellation or the model-call deadline
  WHEN the request stops
  THEN categories are respectively `cancelled` and `timeout` and preserve the
  context cause.
- GIVEN direct admission `401`, Responses authentication errors, `429`,
  documented temporary `503` errors, other `5xx`, unsupported/rejected inputs,
  or malformed successful payloads
  WHEN classified
  THEN they map deterministically to authentication, rate limit, transient,
  rejected or invalid-response categories without text matching.
- GIVEN a canary in an OAuth value, provider body, prompt and output
  WHEN any error is formatted, wrapped or observed
  THEN the canary is absent.

### R6 — Timeout y cancelación

WHEN a session requests a model decision,
THE runtime SHALL propagate the received context, derive a per-call deadline no
later than both the configured call timeout and remaining session duration,
observe cancellation while authenticating, sending and consuming the stream,
and SHALL NOT return partial decisions or begin another operation after
cancellation or total-duration exhaustion.

#### Acceptance criteria

- GIVEN cancellation while resolving a profile, awaiting headers or reading an
  SSE event
  WHEN the context closes
  THEN work terminates, the underlying operation sees cancellation and no
  partial result is returned.
- GIVEN less session duration remaining than the call timeout
  WHEN a call begins
  THEN its deadline uses the shorter duration.
- GIVEN an already cancelled or duration-exhausted session
  WHEN `Decide` is invoked
  THEN no credential or transport call occurs.

### R7 — Telemetría segura y uso confirmado

WHEN a model attempt completes or fails,
THE model boundary SHALL emit exactly one safe record containing provider,
model, duration, outcome/error category and confirmed input/output/total-token
usage when supplied by the completed response, SHALL correlate it with run and
attempt identifiers, and SHALL exclude auth profile identity, tokens,
authorization URLs, prompts, response text, provider bodies and arbitrary
external diagnostic text.

#### Acceptance criteria

- GIVEN completed, rejected, timeout and cancelled attempts
  WHEN observed
  THEN each emits one terminal telemetry record with stable fields and no raw
  content.
- GIVEN a terminal response with token usage
  WHEN the result is returned
  THEN the same normalized counts reach the runtime budget and observer.
- GIVEN canaries in every excluded source
  WHEN telemetry, journal-compatible metadata and formatted errors are scanned
  THEN none occurs.
- GIVEN observer failure
  WHEN terminal telemetry is emitted
  THEN it cannot turn an unsuccessful model call into success or expose the
  structured decision; the failure is surfaced according to the fail-closed
  internal-error policy.

### R8 — Presupuestos no monetarios

WHILE a model session has exhausted its model-call, confirmed-token,
total-duration or replan budget,
THE runtime SHALL NOT start the corresponding new work, SHALL reserve calls and
replans before beginning them, SHALL account confirmed input plus output tokens
after every completed response, and SHALL report the exhausted dimension
without using prices or monetary estimates.

#### Acceptance criteria

- GIVEN no remaining model calls
  WHEN another decision is requested
  THEN it returns `budget_exhausted` and performs no auth or transport work.
- GIVEN confirmed total usage reaches or exceeds the token budget
  WHEN another decision is requested
  THEN it is rejected before auth/network. The response that crossed the limit
  remains accounted and is not retroactively discarded solely for exceeding
  the budget.
- GIVEN a completed response without usable token counts
  WHEN token accounting is mandatory
  THEN it is `invalid_response`, returns no decision and prevents further work
  from relying on unknown consumption.
- GIVEN no remaining duration or replans
  WHEN new work is requested
  THEN it is rejected without partial reservation.
- GIVEN concurrent reservations on one session
  WHEN the last unit is contested
  THEN at most one caller succeeds and consumption never exceeds a count budget
  through a race.
- GIVEN the earlier acceptance wording about estimated cost
  WHEN this approved contract is implemented
  THEN no pricing table, currency or cost field exists; calls, tokens, duration
  and replans are the explicit replacement agreed for this feature.

### R9 — Retries e idempotencia

IF an inference, OAuth exchange or refresh attempt fails,
THEN THE provider adapters SHALL NOT retry invisibly; any retry policy SHALL be
owned by the caller, consume a newly reserved call where applicable, use a new
request attempt, respect cancellation and budgets, and keep rotating-token
refresh serialized so concurrent callers do not reuse an invalidated token.

#### Acceptance criteria

- GIVEN a `429`, temporary `503`, connection reset or interrupted stream
  WHEN one adapter invocation ends
  THEN exactly one outbound inference request was made.
- GIVEN a caller-authorized inference retry
  WHEN another attempt starts
  THEN it consumes another call reservation and emits another attempt record.
- GIVEN concurrent refresh demand for one profile
  WHEN refresh succeeds
  THEN one exchange updates the token set atomically and waiters receive the
  latest usable result.

### R10 — Aislamiento y ausencia de ejecución directa

THE model adapter SHALL receive only bounded request data, provider/model
selection, an authenticated transport and observation ports, SHALL NOT receive
the tool registry, policy evaluator, evidence store or mutable run, and SHALL
never execute a tool or mutate a verdict, plan, evidence or run state directly.

#### Acceptance criteria

- GIVEN a model decision that names an unknown tool
  WHEN returned by the adapter
  THEN the adapter performs no action; the consumer's structural, registry,
  policy and budget checks remain mandatory.
- GIVEN an adapter unit test
  WHEN dependencies are enumerated
  THEN no executable tool, registry, policy, evidence or run handle is present.
- GIVEN a failed or invalid model response
  WHEN the call ends
  THEN no evidence or verdict has been written by the model package.

### R11 — Suite ordinaria hermética

WHEN the repository runs its ordinary verification,
THE model and OAuth packages SHALL use controlled fake transports, browser,
clock, token validator and credential store, SHALL perform no Internet access,
require no account or secret, and SHALL cover configuration, OAuth, request
translation, streaming, taxonomy, cancellation, budgets, safety and races
deterministically.

#### Acceptance criteria

- GIVEN `go test ./...` and `go test -race ./...`
  WHEN executed without credentials and with Internet unavailable
  THEN all tests pass without skips caused by missing external state.
- GIVEN fake pre-stream and mid-stream failures
  WHEN tests execute
  THEN all R1–R10 branches have concrete traceable assertions.
- GIVEN cancellation or concurrent tests
  WHEN coordinated
  THEN they use controllable barriers/contexts rather than `time.Sleep`.

### R12 — Prueba real opt-in

WHERE a developer explicitly invokes the model smoke executable with a selected
OpenAI model and an existing eligible Sign in with ChatGPT profile,
THE executable SHALL perform one bounded Responses request, validate one
structured decision, print only safe provider/model/duration/usage/outcome
metadata, and return a non-zero exit code for missing opt-in, profile, model,
permission, invalid response or provider failure; it SHALL never run as part of
`go test` or normal build verification.

#### Acceptance criteria

- GIVEN ordinary tests and builds
  WHEN run
  THEN the real endpoint is never contacted.
- GIVEN the smoke command without explicit opt-in, model or profile
  WHEN invoked
  THEN it fails before credential resolution/network.
- GIVEN explicit opt-in and a valid eligible profile
  WHEN the provider completes
  THEN exactly one request is made and stdout/stderr contain no prompt,
  response, token or authorization URL.
- GIVEN the preview capability is unavailable for the account/workspace/app
  WHEN the smoke command runs
  THEN it reports a typed safe failure and does not fall back to another billing
  or authentication path.

## Errores y casos límite

- Un model ID se trata como identificador opaco no vacío; FeatureProof no lo
  corrige, infiere ni compara con una lista embebida.
- La selección del perfil es opaca para core y no aparece en telemetría.
- Un token OAuth jamás forma parte de configuración serializable, request
  interna, event, evidence, report o error público.
- Los límites de bytes se aplican a request interno serializado, eventos SSE y
  output agregado. Superarlos falla de forma cerrada.
- Eventos SSE desconocidos no implican éxito. Solo `response.completed` permite
  finalizar correctamente; terminales fallidos/incompletos se clasifican.
- Una respuesta puede consumir más tokens que los restantes porque el flujo
  preview prohíbe `max_output_tokens`. Ese uso se contabiliza y bloquea la
  siguiente llamada; no se afirma un corte estricto de la respuesta en curso.
- Un request ID o provider code solo se conserva como metadata acotada y
  validada; nunca se copia el body externo.
- Fallos de credential storage, ID-token validation o refresh impiden inferencia.
- La indisponibilidad temporal no borra credenciales; una revocación o refresh
  terminal confirmado sí invalida el token set según el flujo OAuth.
- La feature no sustituye las validaciones estructurales posteriores del
  harness ni convierte una respuesta del modelo en evidencia.

## Decisiones cerradas

- El runtime es independiente del proveedor.
- Proveedor, modelo y perfil se seleccionan explícitamente; no hay modelo por
  defecto.
- El primer adapter real es OpenAI Responses API mediante Sign in with ChatGPT
  OAuth para aplicaciones open source elegibles.
- El núcleo limita llamadas, tokens, duración y replanificaciones. Estos límites
  sustituyen expresamente el criterio anterior de coste estimado; no existe
  pricing en esta entrega.
- Los secretos se resuelven y protegen en composición y no cruzan al harness ni
  a observabilidad.
- El smoke test real es opt-in y está fuera de `go test`.

## Decisiones abiertas

Ninguna para esta entrega. La disponibilidad efectiva del flujo preview depende
de elegibilidad, cuenta, workspace y política del proveedor; se trata como
estado externo observable, no como comportamiento garantizado por FeatureProof.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-14T19:28:41+02:00
```
