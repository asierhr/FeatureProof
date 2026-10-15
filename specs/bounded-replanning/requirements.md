# Replanificación agéntica acotada — Requirements

## Objetivo y problema

FeatureProof necesita permitir que, después de ejecutar un plan, un agente
decida de forma acotada si la evidencia ya permite terminar, si falta contexto
o si hacen falta comprobaciones adicionales. Esa decisión no puede convertir
al modelo en ejecutor ni debilitar schema, registry, policy, presupuestos,
aprobación humana, redacción, assertions deterministas o auditoría.

## Alcance

- Construir después de cada lote ejecutado un resumen estructurado y redactado
  para el modelo.
- Aceptar exclusivamente decisiones versionadas de terminación, contexto
  insuficiente o comprobaciones adicionales.
- Validar y ejecutar cada lote adicional por el pipeline ordinario.
- Aplicar presupuestos globales del run antes de empezar trabajo nuevo.
- Preservar de forma monotónica resultados deterministas ya confirmados.
- Detectar propuestas semánticamente equivalentes y conservar lineage
  auditable entre decisiones, evidencias y acciones.
- Integrarse con la aprobación de planes cuando esté configurada.
- Probar el ciclo completo con fakes herméticos.

## Fuera de alcance

- Corregir código, cambiar una revisión o aprovisionar entornos.
- Entrenar, ajustar o mejorar automáticamente el modelo.
- Permitir que el modelo cambie constraints, policy, registry, target,
  OpenAPI, credenciales o assertions ya ejecutadas.
- Precios, monedas, tablas de tarifas o presupuestos monetarios.
- Ejecución paralela de replanificaciones o de lotes adicionales.
- Una CLI/API nueva o una interfaz humana nueva para aprobación.

## Actores y términos

- **Coordinador de replanificación:** único propietario del ciclo posterior a
  la ejecución y de sus transiciones.
- **Lote:** `PreparedPlan` inicial o conjunto atómico de checks adicionales.
- **Resumen de ejecución:** snapshot saneado que contiene resultados,
  assertions, causas, referencias de evidencia, lineage y presupuesto
  restante, pero no evidencia cruda ni credenciales.
- **Decisión:** unión cerrada `terminate`, `insufficient_context` o
  `additional_checks` producida por el modelo.
- **Ledger de assertions:** historial append-only de resultados deterministas
  confirmados; un fallo nunca se elimina ni se degrada.
- **Fingerprint:** SHA-256 de la semántica canónica de una propuesta, sin IDs
  decorativos ni orden accidental de mapas.
- **Lineage:** relación estable entre ciclo, decisión padre, evidencias
  observadas, propuesta, aprobación, ejecuciones y resultados.

## R1 — Resumen exacto, redactado y acotado

AFTER a plan batch has finished execution, THE replanning coordinator SHALL
provide the model only with privileged protocol instructions, the immutable
authorized planning context and a versioned redacted execution summary; THE
summary SHALL contain run/cycle IDs, constraint and check status, step status,
assertion ID/operator/path/status/safe cause, sanitized expected/observed
values, evidence references, safe technical categories, lineage and remaining
non-monetary budgets, and SHALL NOT contain raw evidence, credentials,
executable handles, provider payloads or unrestricted errors.

### Acceptance criteria

- GIVEN persisted evidence containing a secret canary
  WHEN the next model request is captured
  THEN the canary is absent, evidence is represented only by opaque IDs and
  assertion values are passed through the configured sanitizer.
- GIVEN equivalent confirmed results and the same remaining budgets
  WHEN the summary is built twice
  THEN canonical JSON bytes and digest are identical and ordered by stable
  constraint, check, step, assertion and evidence identity.
- GIVEN sanitization, evidence lookup or bounded serialization fails
  WHEN the summary is being built
  THEN no model call starts and the run reports a typed operational error.
- GIVEN OpenAPI descriptions, logs, responses or evidence contain instructions
  WHEN the request is composed
  THEN they remain inside the untrusted JSON envelope and never enter
  privileged instructions.

## R2 — Protocolo cerrado de decisión

WHEN the model analyzes a redacted execution summary, THE coordinator SHALL
accept exactly one strict protocol-v1 branch: `terminate`,
`insufficient_context` or `additional_checks`.

### Acceptance criteria

- GIVEN `terminate`
  WHEN the decision is decoded
  THEN it carries no verdict claim or actions and the system calculates the
  final verdict from the ledger.
- GIVEN `insufficient_context`
  WHEN the decision is decoded
  THEN every structured missing item has a closed kind, a bounded safe request
  and references only existing constraint IDs.
- GIVEN `additional_checks`
  WHEN the decision is decoded
  THEN it contains a non-empty bounded batch of complete candidate checks and
  no executable handle, target override, credential or evidence value.
- GIVEN unknown fields, unsupported versions, mixed branches, duplicate IDs or
  malformed JSON
  WHEN the response is decoded
  THEN it is rejected before approval, policy, budget reservation for tools or
  execution.

## R3 — Pipeline completo para comprobaciones adicionales

WHEN the model proposes additional checks, THE coordinator SHALL process the
entire batch atomically through schema and semantic validation, retained
OpenAPI operation resolution, capability registry, assertion validation,
generation policy, deterministic work limits, plan validation, runtime policy,
runtime budget reservation, tool execution, evidence redaction/persistence and
deterministic assertion evaluation.

### Acceptance criteria

- GIVEN a valid allowed additional HTTP check
  WHEN the batch is accepted
  THEN method/path are derived from the retained operation, the check becomes a
  `PreparedPlan`, and the ordinary executor produces persisted evidence and
  deterministic assertion results.
- GIVEN one check in a proposed batch has an invented operation/capability,
  invalid assertion, unsafe input or policy denial
  WHEN the batch is validated
  THEN no check in that batch is approved or executed and the rejection is
  recorded with a stable category.
- GIVEN runtime policy changes after planning
  WHEN execution begins
  THEN runtime policy is evaluated again and can deny the action without
  relying on the earlier generation decision.

## R4 — Presupuestos globales explícitos y no monetarios

THE run SHALL own explicit positive or zero limits for replan cycles, agent
steps, model calls, confirmed model tokens, tool calls, deterministic
validation work and total duration; THE coordinator SHALL reserve count/work
budgets atomically before the corresponding model, validation or tool work and
SHALL use the feature-9 session for calls, tokens, duration and replan
reservations.

### Acceptance criteria

- GIVEN any count budget has no remaining unit or total duration has expired
  WHEN the next unit of work would start
  THEN it is rejected before model auth/transport, approval, policy callback or
  tool execution, according to the unit being admitted.
- GIVEN confirmed model usage crosses the token limit on one terminal response
  WHEN another model call is considered
  THEN that next call is blocked; confirmed usage remains accounted.
- GIVEN concurrent attempts to reserve the final replan unit
  WHEN they race
  THEN at most one succeeds and counters do not overflow.
- GIVEN the feature-list wording `coste estimado`
  WHEN this requirement is implemented
  THEN no cost, currency, pricing or tariff field exists; calls, tokens,
  duration, replans, steps, tools and deterministic work are the explicit
  measurable replacement already established by feature 9.

## R5 — Agotamiento antes de trabajo y resultado seguro

WHILE any budget required by the next action is exhausted, THE coordinator
SHALL NOT start that action, SHALL append the exhausted dimension and current
consumption, and SHALL finish `inconclusive` when evidence is insufficient,
without overriding any already confirmed deterministic failure.

### Acceptance criteria

- GIVEN no failed assertion and required assertions remain missing or
  inconclusive
  WHEN any limit is exhausted
  THEN no new work starts and the final verdict is `inconclusive` with the
  exhausted dimension.
- GIVEN a deterministic failed assertion was confirmed before exhaustion
  WHEN a later limit is exhausted
  THEN the failure remains in the ledger and the aggregate functional verdict
  remains `failed` while exhaustion is recorded separately.
- GIVEN all required assertions are already passed
  WHEN the model requests unnecessary work or a later budget is empty
  THEN the system may terminate `passed` from the ledger without starting new
  work.

## R6 — Monotonicidad de assertions deterministas

ONCE a deterministic assertion result is confirmed, THE coordinator SHALL
preserve its declaration identity, status, cause and evidence references in an
append-only ledger; no model decision or later check SHALL delete, alter or
convert a confirmed `failed` result to `passed`.

### Acceptance criteria

- GIVEN an assertion has failed in the initial batch
  WHEN a later decision claims success or proposes another assertion with the
  same/decorative identity
  THEN the original failure remains unchanged and the final verdict cannot be
  `passed`.
- GIVEN later evidence evaluates a distinct assertion
  WHEN results are aggregated
  THEN the new result is appended with its own lineage and does not overwrite
  an earlier entry.
- GIVEN duplicate confirmation of the same execution/result identity
  WHEN it is replayed idempotently
  THEN no duplicate ledger entry is created.

## R7 — Detección de equivalencia y prevención de bucles

WHEN an `additional_checks` batch is accepted structurally, THE coordinator
SHALL derive a canonical semantic fingerprint and SHALL reject a fingerprint
already proposed, approved or executed in the same run before approval or tool
work.

### Acceptance criteria

- GIVEN two proposals that differ only in generated IDs, map order,
  independent-step order or equivalent dependency/reference renaming
  WHEN fingerprinted
  THEN they produce the same fingerprint and the second is recorded as
  `repeated_proposal` without execution.
- GIVEN operation, capability, input, dependency, assertion or evidence
  requirement semantics differ
  WHEN fingerprinted
  THEN the fingerprint differs.
- GIVEN the model repeatedly returns an equivalent batch
  WHEN cycles remain
  THEN every model/replan unit already consumed stays consumed, no approval or
  tool budget is consumed for the duplicate, and the coordinator cannot loop
  beyond configured limits.

## R8 — Lineage auditable y reconstruible

WHEN the coordinator receives a decision or advances additional work, THE
system SHALL append versioned events that relate run, cycle, decision ID,
parent decision ID, input-summary digest, observed evidence IDs, proposal
fingerprint, prepared batch/plan, approval revision when present, tool calls,
new evidence and assertion results.

### Acceptance criteria

- GIVEN a run with two accepted replans
  WHEN its journal is projected
  THEN every new action can be traced to exactly one decision, the exact
  summary digest and the evidence available before that decision.
- GIVEN a rejected, repeated, insufficient-context or exhausted decision
  WHEN the journal is inspected
  THEN its terminal disposition is present without raw model/evidence content.
- GIVEN missing, reordered, forked or contradictory lineage events
  WHEN recovery is attempted
  THEN projection fails closed and no further work starts.

## R9 — Nueva aprobación para cada lote adicional

WHERE plan approval is required, WHEN an additional batch becomes prepared,
THE coordinator SHALL stage that exact batch and current contractual context as
a new approval revision and SHALL NOT execute any additional tool until a
matching one-use authorization is confirmed and consumed.

### Acceptance criteria

- GIVEN an initial plan was approved and executed
  WHEN the agent proposes additional checks
  THEN the old grant does not authorize them and the run enters
  `awaiting_approval` with a new safe view and revision.
- GIVEN the additional batch is rejected, edited, regenerated or becomes stale
  WHEN the approval workflow decides
  THEN none of its tools execute; regeneration/editing returns through
  validation and requires another approval revision.
- GIVEN approval is explicitly disabled at trusted composition
  WHEN an additional batch passes all pre-execution controls
  THEN it may enter the ordinary executor without fabricating a grant.
- GIVEN cancellation or budget exhaustion while waiting for approval
  WHEN processing resumes
  THEN no expired/stale authorization can start work and confirmed evidence is
  preserved.

## R10 — Concurrencia, cancelación e idempotencia

THE coordinator SHALL serialize state changes per run with compare-and-swap,
SHALL permit independent runs to progress concurrently, SHALL propagate
`context.Context` through every blocking boundary, and SHALL make retries of
the same decision/transition idempotent without holding locks during model,
sanitizer, approval, policy, store callback or tool I/O.

### Acceptance criteria

- GIVEN two workers process the same cycle/revision
  WHEN they race
  THEN one transition wins and at most one model decision, approval request and
  execution lineage is confirmed for that reserved identity.
- GIVEN the same command and idempotency key is retried
  WHEN its event is already confirmed
  THEN the prior result is reconstructed without duplicating work; changing
  fields under that key yields a conflict.
- GIVEN cancellation during model, approval wait, persistence or tool work
  WHEN the dependency returns
  THEN no next unit begins, confirmed events/evidence remain recoverable and
  the run records one cancellation terminal outcome.

## R11 — Veredicto final determinista

WHEN replanning terminates for any reason, THE system SHALL compute the run
verdict only from the complete assertion ledger and required evidence: any
confirmed required failure yields `failed`; otherwise all required assertions
confirmed passed yield `passed`; otherwise the verdict is `inconclusive`;
operational corruption or inability to confirm mandatory audit state yields
`error`.

### Acceptance criteria

- GIVEN a model returns `terminate` and claims or implies success
  WHEN a required assertion failed
  THEN the verdict is `failed` and cites the original evidence.
- GIVEN `insufficient_context`, an exhausted limit, denied policy or missing
  reliable evidence and no confirmed failure
  WHEN finalizing
  THEN the verdict is `inconclusive`, never `passed`.
- GIVEN complete passing required assertions across initial and additional
  batches
  WHEN finalizing
  THEN the verdict is `passed` with all required evidence references.

## R12 — Verificación hermética y comentarios sistémicos

THE feature SHALL be verified with deterministic fake model, registry, policy,
approval, executor, evidence, journal, clock and ID sources, SHALL require no
network, real credentials or real provider in ordinary tests, and SHALL
document every introduced or modified entity, function and test helper with a
comment describing its responsibility in the system.

### Acceptance criteria

- GIVEN ordinary `go test`
  WHEN feature tests run
  THEN allowed addition, policy denial, required approval, exhausted limits,
  equivalent proposal, monotonic failure, cancellation, races and corrupted
  lineage are covered without external services or real waits.
- GIVEN the implementation and tests are reviewed
  WHEN tracing R1–R12
  THEN each requirement maps to named tests, changed packages reach at least
  85% statement coverage, race tests pass and all new/modified symbols and
  helpers carry systemic-responsibility comments.

## Errores y casos límite

- El resumen es un snapshot: las colecciones y valores saneados se copian y no
  comparten estado mutable con stores, executor o modelo.
- Los IDs de evidencia son opacos y se comprueba que pertenecen al run antes
  de incluirlos. Una referencia ajena es un error de integridad.
- La respuesta completa del modelo se limita en bytes antes de decodificar y
  usa JSON estricto UTF-8 con keys únicas, campos desconocidos rechazados y
  unión exactamente exclusiva.
- La reserva de replan se realiza antes de la inferencia y no se devuelve ante
  error o cancelación; un intento confirmado no puede reaparecer como libre.
- Un rechazo estructural/policy/repetición consume la inferencia y el ciclo ya
  iniciados, pero no consume aprobación ni tool calls no comenzadas.
- El fingerprint cubre el lote completo y sus checks normalizados. SHA-256 se
  codifica en hexadecimal minúscula sobre JSON canónico versionado.
- Una decisión `insufficient_context` termina el ciclo actual; reanudar con
  contexto nuevo requiere una operación superior explícita y no reutiliza una
  aprobación anterior.
- Los errores exponen categoría, etapa y path generados por el sistema. Texto
  del modelo, herramientas, evidencias o provider no cruza esa frontera.
- Los contadores usan aritmética comprobada y fallan cerrados ante overflow.
- El coordinador v1 es secuencial dentro de un run y no crea goroutines.

## Decisiones abiertas

Ninguna para esta entrega. La frase histórica `coste estimado` queda resuelta
por la decisión aprobada en feature 9: se usan únicamente presupuestos
medibles no monetarios. La interfaz humana concreta permanece detrás de la
frontera de aprobación existente.

## Aprobación humana

```yaml
approved_by:
approved_at:
```
