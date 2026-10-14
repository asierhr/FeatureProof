# Generación agéntica de planes de validación — Requirements

## Objetivo

Permitir que FeatureProof transforme restricciones validadas, un contexto
OpenAPI reducido y un catálogo explícito de capacidades permitidas en planes de
validación estructurados, verificables y aptos para una revisión posterior. El
modelo propone; el núcleo comprueba procedencia, estructura, semántica, registry
y policy antes de aceptar el resultado.

## Alcance

- Definir un protocolo interno versionado para solicitar y devolver generación
  de planes sin reutilizar el protocolo incremental del harness.
- Generar uno o más checks estructurados que se materializan como
  `planning.ValidationPlan` existentes.
- Relacionar cada check con una sola restricción y declarar la evidencia que
  permitirá evaluarlo.
- Representar escenarios positivos, negativos y de error, junto con su
  aplicabilidad declarada.
- Comprobar determinísticamente restricciones, operaciones OpenAPI,
  capacidades, inputs, assertions, dependencias, evidencia, duplicados y
  policy.
- Devolver contexto insuficiente como resultado de dominio estructurado.
- Propagar cancelación y aplicar límites explícitos sin ejecutar herramientas.
- Adaptar el runtime de modelo y OpenAI Responses API al nuevo protocolo sin
  romper `harness.ModelDecision` v1.
- Probar el flujo completo con modelos, catálogos y policies falsos, sin red.

## Fuera de alcance

- La suite versionada de evaluaciones, métricas, baselines y umbrales de la
  feature 11.
- La presentación, edición, aprobación o rechazo humano del plan de la feature
  12.
- Replanificar después de observaciones o fallos de ejecución, responsabilidad
  de la feature 13.
- Ejecutar planes, herramientas o requests HTTP; persistir evidencias; calcular
  assertions o veredictos.
- Inferir credenciales, targets, URLs absolutas, comandos o capacidades ausentes
  del contexto permitido.
- Cambiar la specification YAML v1 o cargar documentos OpenAPI.
- Añadir un modelo, proveedor, perfil de autenticación o configuración por
  defecto.

## Actores y términos

- **Caller/composición:** reúne inputs validados y dependencias de solo lectura.
- **Generador:** aplicación que coordina una inferencia y valida su resultado.
- **Modelo de planificación:** puerto consumidor que devuelve bytes JSON
  estructurados y metadata segura, sin herramientas ejecutables.
- **Protocolo de planificación v1:** unión cerrada entre `plan` y
  `insufficient_context`.
- **Check:** comprobación perteneciente a una sola restricción y un solo tipo de
  escenario.
- **Escenario:** `positive`, `negative` o `error`.
- **Evidence requirement:** referencia estructurada a los pasos y assertions
  cuyos resultados serán necesarios para evaluar un check.
- **Contexto OpenAPI reducido:** envelope canónico, acotado y marcado como dato
  no confiable por `internal/openapi`.
- **Catálogo permitido:** snapshot inmutable de descriptores de capacidades;
  contiene contratos de input y metadata, nunca implementaciones.
- **Plan preparado:** resultado inmutable que ya superó schema, semántica,
  procedencia, registry y policy de generación. No implica aprobación humana ni
  autoriza ejecución.

## Requirements

### R1 — Solicitud acotada y protocolo versionado

WHEN a caller requests plan generation,
THE generator SHALL require non-empty run and plan identifiers, at least one
validated constraint, one reduced OpenAPI context marked as untrusted, one
explicit allowed-capability catalog and positive explicit size/count limits,
SHALL snapshot those inputs before inference, and SHALL accept only planning
protocol version 1 with exactly one result kind: `plan` or
`insufficient_context`.

#### Acceptance criteria

- GIVEN duplicate or empty constraint IDs, a mismatched OpenAPI media type, a
  context not marked untrusted, a missing catalog, duplicate capability names or
  non-positive limits
  WHEN generation is requested
  THEN a typed input error is returned before a model call.
- GIVEN mutable caller-owned slices or maps
  WHEN they are changed after generation starts
  THEN the request and validation use the isolated snapshot.
- GIVEN an unknown protocol version/kind, both result branches, neither branch,
  duplicate JSON keys, unknown fields or trailing data
  WHEN the model response is decoded
  THEN it is rejected as `invalid_model_response`.

### R2 — Cobertura exacta y evidencia requerida

WHEN a `plan` result is validated,
THE generator SHALL require every check to reference exactly one existing
constraint, SHALL require every requested constraint to be covered by at least
one check, SHALL reject references to constraints outside the request, and
SHALL require every check to identify at least one existing source step and the
existing assertion IDs whose terminal evidence is necessary for evaluation.

#### Acceptance criteria

- GIVEN constraints `c1` and `c2`
  WHEN a plan omits either, repeats a check ID, or references `c3`
  THEN no prepared plan is returned and the exact field/category is reported.
- GIVEN an evidence requirement with an unknown step, an unknown assertion, no
  source, or the same source/assertion pair twice
  WHEN validated
  THEN the candidate is rejected before policy.
- GIVEN a valid candidate
  WHEN materialized
  THEN each check becomes one `planning.ValidationPlan` whose
  `ConstraintID` equals that check's sole constraint and whose evidence
  requirement remains available to later review/reporting.

### R3 — Escenarios y deduplicación determinista

WHEN the model assesses a requested constraint,
THE planning protocol SHALL account for `positive`, `negative` and `error` in a
scenario matrix, SHALL provide one or more checks for each scenario marked
`planned`, SHALL provide a bounded non-empty reason for each scenario marked
`not_applicable`, and THE generator SHALL remove equivalent checks by a
canonical fingerprint while preserving the first declaration and stable
constraint/scenario/declaration order.

#### Acceptance criteria

- GIVEN a constraint with all three scenarios marked `planned`
  WHEN validated
  THEN at least one surviving check of every type exists.
- GIVEN a scenario marked `not_applicable`
  WHEN its reason is empty or a check of that scenario is also present
  THEN the result is rejected as semantically inconsistent.
- GIVEN equivalent checks with different IDs or map insertion order
  WHEN normalized
  THEN one check remains, the first declaration wins and repeated runs produce
  byte-equivalent canonical plan snapshots.
- GIVEN deduplication removes the only check for a planned scenario
  WHEN coverage is checked
  THEN the result is rejected rather than silently weakening coverage.

### R4 — Procedencia cerrada de operaciones y capacidades

WHEN a proposed check is normalized,
THE generator SHALL accept only an exact capability name from the allowed
catalog and, for `http.request`, an exact method/path operation present for that
constraint in the retained OpenAPI context, SHALL derive the HTTP method and
concrete path from that trusted operation template plus typed declared path
parameter bindings, and SHALL reject free-form absolute URLs,
commands, credential material, endpoint substitutions and capability-bearing
fields that have no typed reference to an allowed context item.

#### Acceptance criteria

- GIVEN an allowed `GET /pets` operation and `http.request` capability
  WHEN a check references both exact identifiers
  THEN materialization derives `method=GET` and `path=/pets` without accepting
  a model-authored URL.
- GIVEN an allowed `GET /pets/{petId}` operation with a declared required path
  parameter
  WHEN a check supplies one schema-valid typed binding
  THEN materialization expands and escapes only that segment; missing, extra or
  schema-invalid bindings are rejected.
- GIVEN an unknown method/path, an operation assigned to a different
  constraint, an invented tool or a case-confusable tool name
  WHEN validated
  THEN no plan is returned.
- GIVEN an absolute URL, userinfo, authorization/cookie field, credential-like
  field, shell/command field or executable payload in the candidate protocol
  WHEN decoded or materialized
  THEN it is rejected before registry or policy and its value is absent from
  diagnostics.
- GIVEN an operation omitted by the OpenAPI context size budget
  WHEN proposed
  THEN it is unavailable even if it existed in the original full contract.

### R5 — Cadena obligatoria de validación

WHEN the model returns a `plan`,
THE generator SHALL complete, in order, strict protocol/schema validation,
candidate semantic/provenance validation, materialization, existing planning
graph/assertion validation, descriptor-only registry validation and generation
policy evaluation for every surviving check before returning a `PreparedPlan`,
and SHALL return no executable or partially prepared plan if any stage fails or
is denied.

#### Acceptance criteria

- GIVEN malformed structure
  WHEN generation completes
  THEN semantic, registry and policy collaborators have zero calls.
- GIVEN an unknown dependency, cycle, invalid typed reference, invalid
  assertion, input outside the capability contract or unknown capability
  WHEN validated
  THEN it is rejected with a stable category/path and policy is not evaluated.
- GIVEN policy denial for one check
  WHEN the candidate is otherwise valid
  THEN the entire generation result is rejected, all denials already observed
  remain auditable and no prepared subset is returned.
- GIVEN all stages succeed
  WHEN the result is returned
  THEN it is an immutable `PreparedPlan`; obtaining it cannot bypass runtime
  registry/policy checks that a future executor will still apply.

### R6 — Contexto insuficiente como resultado de dominio

WHEN the supplied constraints, retained OpenAPI operations or allowed
capabilities do not contain enough information to construct a valid check,
THE model MAY return `insufficient_context`, and THE generator SHALL validate a
non-empty bounded list of missing items, each with a stable kind
`question` or `evidence`, at least one existing affected constraint ID and a
non-empty safe request, SHALL reject any accompanying plan, and SHALL expose the
result without treating it as provider failure.

#### Acceptance criteria

- GIVEN a valid insufficient-context response
  WHEN generation completes
  THEN the caller receives typed missing questions/evidence in stable order and
  no plan, registry call or policy call.
- GIVEN an unknown constraint, duplicate missing item, unsupported kind, empty
  request, secret-bearing value, URL, command or item/count limit excess
  WHEN validated
  THEN it becomes `invalid_model_response` and no raw value enters errors or
  telemetry.
- GIVEN a model invents the missing fact inside a plan
  WHEN provenance validation runs
  THEN the plan is rejected; it is not converted automatically into context
  insufficiency.

### R7 — Generación sin efectos

WHILE a plan is being generated or validated,
THE generator and model boundary SHALL NOT receive executable tool handles,
mutable run state, an evidence store, target credentials or an execution
policy capable of side effects, SHALL NOT execute tools or network calls to the
application under validation, and SHALL NOT mutate run, application, evidence,
journal, verdict or persisted plan state.

#### Acceptance criteria

- GIVEN valid, invalid, denied and insufficient-context responses
  WHEN generation runs
  THEN fake application/tool/evidence/journal collaborators observe zero
  mutations and only the model transport may perform the configured inference.
- GIVEN the generator's dependency graph
  WHEN inspected in tests
  THEN it contains descriptor snapshots and a read-only policy port, not a
  `harness.Tool`, executable registry, evidence store or mutable `harness.Run`.
- GIVEN a prepared plan
  WHEN generation returns
  THEN no execution has begun and no evidence or verdict exists as a result of
  this feature.

### R8 — Cancelación, límites y ausencia de replanificación

WHEN generation starts,
THE generator SHALL propagate the received context through inference and every
potentially long validation pass, SHALL enforce explicit limits for input
bytes, output bytes, constraints, capabilities, checks, steps, assertions,
missing items and total validation work, SHALL use the model-session call/token/
duration budgets from feature 9, and SHALL perform at most one inference per
generation request with no hidden retry or replan.

#### Acceptance criteria

- GIVEN an already cancelled context or an exceeded preflight limit
  WHEN generation is called
  THEN no model, registry or policy work starts.
- GIVEN cancellation during inference, normalization, validation or policy
  WHEN observed
  THEN work stops, no partial result is returned and the cancellation cause is
  preserved.
- GIVEN a provider-transient error or invalid candidate
  WHEN generation ends
  THEN the generator does not retry, repair through another model call or
  reserve a replan; a caller retry is a new bounded request.
- GIVEN a limit is reached during deterministic traversal
  WHEN checked
  THEN validation fails closed in stable order without integer overflow or
  unbounded allocation.

### R9 — Datos no confiables y separación de instrucciones

WHEN the planning request is sent to a model,
THE model adapter SHALL place the versioned planning contract and safety rules
in the privileged instruction channel, SHALL label constraint descriptions,
OpenAPI bytes, schemas, catalog descriptions and prior external text as
untrusted data, and SHALL NOT interpret instructions embedded in those inputs
as permission to change the output schema, reveal secrets, invent context or
execute an action.

#### Acceptance criteria

- GIVEN prompt-injection canaries in a constraint, OpenAPI field, schema or
  capability description
  WHEN the provider request is captured
  THEN those canaries occur only inside the untrusted data payload and the
  planning instructions remain separately controlled.
- GIVEN injected output requesting a tool call, URL, credential or alternate
  schema
  WHEN decoded
  THEN deterministic validation rejects it.
- GIVEN redaction-sensitive input
  WHEN a planning request is formed
  THEN it has already crossed the approved model projection/redaction boundary
  and the generator cannot access excluded credential values.

### R10 — Errores y observabilidad segura

IF generation cannot return a prepared plan or valid context-insufficient
result,
THEN THE boundary SHALL return a typed category and structural field/stage,
SHALL distinguish invalid input, invalid model response, semantic rejection,
unknown context reference, registry rejection, policy denial, limit exceeded,
cancelled, timeout and model/provider failure, and SHALL emit at most one safe
terminal record with run/plan IDs, provider/model metadata already allowed by
feature 9, outcome, category, duration and aggregate counts, excluding prompts,
descriptions, OpenAPI bytes, inputs, assertions, questions, evidence requests,
credentials and external error text.

#### Acceptance criteria

- GIVEN each failure stage
  WHEN callers inspect errors with `errors.Is`/`errors.As`
  THEN the stable category and safe structural location are available without
  text matching.
- GIVEN canaries in every untrusted or secret-bearing surface
  WHEN errors, telemetry and formatted diagnostics are scanned
  THEN no canary appears.
- GIVEN success, context insufficiency, denial, cancellation and invalid output
  WHEN observed
  THEN exactly one generation terminal record is emitted after the final
  outcome, without duplicating feature 9's per-model-attempt record.

### R11 — Evolución compatible de la frontera de modelo

WHEN the real OpenAI adapter is used for plan generation,
THE model layer SHALL support a bounded structured-response request whose
consumer supplies the planning protocol instructions and strict decoder,
SHALL preserve the existing `model.Session.Decide` and
`harness.ModelDecision` v1 behavior unchanged for the harness, and SHALL share
the same authenticated Responses transport, streaming, usage accounting,
timeouts, error taxonomy and non-monetary budgets instead of duplicating a
provider client.

#### Acceptance criteria

- GIVEN all existing harness/model/OpenAI tests
  WHEN the new structured generation path is added
  THEN they continue to pass without changing the meaning of
  `harness.ModelDecision` v1.
- GIVEN one planning call through a fake OpenAI transport
  WHEN completed
  THEN exactly one Responses request is made, the planning decoder receives one
  bounded terminal JSON value and usage is charged to the same model session.
- GIVEN a planning response encoded as a harness decision or vice versa
  WHEN decoded by the selected consumer protocol
  THEN it is rejected as an invalid response rather than coerced.

### R12 — Verificación hermética y trazable

WHEN ordinary repository verification runs,
THE feature SHALL use deterministic fake models, structured transports,
descriptor catalogs, policies and clocks, SHALL require neither network nor
credentials, and SHALL include concrete tests for valid generation, scenario
coverage/deduplication, nonexistent endpoint, invented capability, forbidden
values, context insufficiency, invalid structured response, cancellation,
limits, policy denial, untrusted data and compatibility.

#### Acceptance criteria

- GIVEN `go test ./...` and `go test -race ./...` without Internet or model
  credentials
  WHEN run
  THEN every test passes and no real provider or deployed target is contacted.
- GIVEN each R1–R11 requirement
  WHEN implementation evidence is reviewed
  THEN at least one named test directly exercises its observable behavior.
- GIVEN concurrent calls using isolated generator requests or one shared
  model-budget session
  WHEN race tests execute
  THEN request state is isolated and feature 9 budget admission remains race
  safe without sleeps.

## Errores y casos límite

- Las restricciones llegan ya validadas; el generador no corrige IDs ni texto.
- Solo las operaciones retenidas realmente en `ReducedContext` son proponibles;
  una omisión por tamaño equivale a contexto ausente.
- El método HTTP se normaliza en la frontera OpenAPI y luego se compara de forma
  exacta; paths y nombres de tools no usan coincidencia aproximada.
- La deduplicación ignora IDs declarativos y orden de mapas, pero conserva
  constraint, escenario, operación/capacidad, inputs normalizados, grafo,
  assertions y evidence requirements en el fingerprint. Checks con distinta
  intención observable no se fusionan.
- `not_applicable` es una afirmación estructurada del modelo y no una prueba de
  calidad semántica; la futura suite de evals medirá su acierto. Esta feature sí
  exige consistencia y cobertura del protocolo.
- Un `PreparedPlan` significa validado y policy-checked en generación. No
  significa aprobado por una persona ni autorizado para saltar los controles
  de ejecución.
- La policy de generación es de solo lectura y no sustituye la policy aplicada
  inmediatamente antes de una herramienta futura.
- Un error del observer de generación falla de forma cerrada y no publica el
  plan al caller.
- No se permiten reparaciones automáticas del JSON, planes parciales ni
  aceptación de campos desconocidos.

## Decisiones abiertas

Ninguna para esta entrega.

## Aprobación humana

```yaml
approved_by:
approved_at:
```
