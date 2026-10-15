# Aprobación humana del plan — Requirements

## Objetivo

Introducir una puerta humana entre un plan completamente validado y su
ejecución. La persona debe poder revisar una representación segura del trabajo
propuesto y tomar una única decisión auditable. Ninguna decisión antigua o
referida a otro plan, target, restricciones o contexto contractual puede
autorizar herramientas.

## Alcance

- Incorporar `awaiting_approval` al ciclo de vida del run cuando la aprobación
  esté habilitada.
- Crear una solicitud de aprobación a partir de un `agentplanning.PreparedPlan`
  y snapshots explícitos de target, restricciones y contexto contractual.
- Presentar una vista estable y saneada del plan.
- Admitir aprobación, rechazo terminal, rechazo con vuelta a planificación,
  solicitud de regeneración y edición de restricciones.
- Calcular huellas canónicas del plan y del contexto relevante.
- Registrar decisiones e invalidaciones como eventos append-only.
- Entregar una autorización opaca como única entrada válida a ejecución.
- Resolver decisiones concurrentes con una revisión esperada.

## Fuera de alcance

- Autenticar personas, administrar usuarios, roles, sesiones o permisos. La
  composición debe aportar una identidad ya autenticada y su fuente.
- Diseñar una interfaz web, GitHub Check, API pública o protocolo remoto.
- Editar archivos YAML o interpretar texto libre como restricciones. Esta
  feature devuelve una solicitud tipada al caller, que debe volver a usar los
  loaders y validadores existentes.
- Generar o replanificar por sí misma. La generación sigue perteneciendo a la
  feature 10 y la replanificación posterior a evidencias a la feature 13.
- Ejecutar herramientas, obtener evidencias, evaluar assertions o calcular el
  veredicto.
- Aprobar parcialmente pasos o checks de un plan.
- Firmas criptográficas, aprobación múltiple o políticas de quorum.

## Actores y términos

- **Caller/composición:** mantiene el flujo del run y aporta snapshots ya
  validados, identidad autenticada y dependencias.
- **Aprobador:** persona identificada por un `subject` estable no vacío y una
  `source` estable no vacía, proporcionados por una frontera confiable.
- **Solicitud de aprobación:** snapshot inmutable de plan, contexto, vista y
  revisión que espera una decisión.
- **Vista de aprobación:** proyección saneada para humanos; no es la fuente de
  ejecución ni participa en los hashes.
- **Contexto contractual:** target exacto, restricciones validadas, contexto
  OpenAPI reducido, catálogo de capacidades y configuración de runtime/policy
  que condicionaron el plan.
- **Plan hash:** SHA-256 de la representación canónica y semántica de todo el
  plan preparado.
- **Context hash:** SHA-256 de la representación canónica del contexto
  contractual.
- **Revisión:** entero monotónico de la solicitud usado para compare-and-swap.
- **Autorización opaca:** valor inmutable, no construible por callers, que
  enlaza run, revisión y hashes aprobados y permite componer el executor.

## Requirements

### R1 — Puerta obligatoria antes de ejecutar

WHERE plan approval is enabled,
WHEN a fully prepared plan and its contractual context are staged,
THE approval workflow SHALL snapshot them, calculate their fingerprints,
transition the run from `planning` to `awaiting_approval`, and SHALL NOT expose
an execution authorization or invoke any tool, executor or evidence store.

#### Acceptance criteria

- GIVEN a plan that has not completed feature 10 validation or lacks any
  required context snapshot
  WHEN staging is requested
  THEN a typed input error is returned, no approval request is created and the
  run does not enter `awaiting_approval`.
- GIVEN a valid prepared plan with approval enabled
  WHEN staging succeeds
  THEN one `plan_approval_requested` event is confirmed with run, plan,
  revision and hashes, while fake executor/tool/evidence collaborators have
  zero calls.
- GIVEN approval is disabled
  WHEN composition proceeds
  THEN this workflow is not invoked; this feature does not create an implicit
  approval or alter the pre-existing execution policy.

### R2 — Vista completa, estable y saneada

WHEN an approval request is read,
THE workflow SHALL return a deterministic view containing every covered
constraint, check/scenario, ordered step and dependency, identity, capability,
operation, assertion, declared effect/idempotency and requested evidence,
SHALL identify redacted or omitted values structurally, and SHALL NOT expose
credentials, authorization material or unsanitized plan/context values.

#### Acceptance criteria

- GIVEN a multi-check plan
  WHEN its view is obtained twice
  THEN all required elements appear in declaration order and both serialized
  views are byte-equivalent.
- GIVEN authorization headers, cookies, credential-like fields, sensitive JSON
  paths or configured redaction values in plan inputs/context
  WHEN the view, diagnostics and events are scanned
  THEN no original value appears and the view records a redacted marker at the
  applicable structural location.
- GIVEN a returned view is mutated by the caller
  WHEN the request is read again or decided
  THEN the stored snapshot and fingerprints are unchanged.

### R3 — Decisiones humanas cerradas e identidad confiable

WHEN a person decides an awaiting request,
THE workflow SHALL accept exactly one action from `approve`,
`reject_terminal`, `reject_replan`, `regenerate` or `edit_constraints`, SHALL
require non-empty authenticated subject/source and an exact run ID, plan ID and
expected revision, and SHALL reject unknown, ambiguous or untrusted identities
without changing state.

#### Acceptance criteria

- GIVEN a missing identity, identity source, run, plan, revision or action
  WHEN a decision is submitted
  THEN a typed field/category error is returned and no event is appended.
- GIVEN a caller supplies a display label different from the authenticated
  subject
  WHEN a decision is recorded
  THEN audit uses the stable subject/source; the label cannot replace them.
- GIVEN a text value that is not a member of the closed action set
  WHEN decoded
  THEN it cannot be coerced into approval or rejection.

### R4 — Aprobación auditable ligada a huellas

WHEN `approve` is applied to the current awaiting request,
THE workflow SHALL rederive and compare the current plan/context fingerprints,
atomically append one `plan_approved` event containing approver subject/source,
controlled timestamp, run ID, plan ID, revision, plan hash and context hash,
transition from `awaiting_approval` to `executing`, and return exactly one
opaque authorization bound to those values.

#### Acceptance criteria

- GIVEN current snapshots equal the staged snapshots
  WHEN an authenticated actor approves the expected revision
  THEN the event contains both SHA-256 hashes and controlled instant, state is
  `executing` and the returned authorization identifies the approved snapshot.
- GIVEN event persistence fails
  WHEN approval is attempted
  THEN no authorization is returned and the confirmed projection remains
  `awaiting_approval`.
- GIVEN an authorization value
  WHEN its plan snapshot is obtained for the executor
  THEN caller mutation cannot change the approved plan or either fingerprint.

### R5 — Rechazo terminal o vuelta a planificación

WHEN `reject_terminal` or `reject_replan` is applied to the current request,
THE workflow SHALL atomically append one `plan_rejected` event with identity,
timestamp, revision, hashes and disposition, SHALL return no execution
authorization, and SHALL transition respectively to `cancelled` or `planning`.

#### Acceptance criteria

- GIVEN either rejection disposition
  WHEN it succeeds
  THEN the rejected plan never reaches an executor and the event distinguishes
  `terminal` from `replan` without relying on prose.
- GIVEN terminal rejection
  WHEN another stage or decision is attempted for that run
  THEN terminal-state rules reject it.
- GIVEN rejection with replan
  WHEN a new plan is later produced
  THEN it must be staged as a new revision and pass the full approval flow.

### R6 — Edición invalida y exige validación completa

WHEN `edit_constraints` is applied to the current request,
THE workflow SHALL append one `plan_invalidated` event with reason
`constraints_edited`, transition to `planning`, return a typed edit request
without an authorization, and SHALL require any changed constraints to pass
the specification loader and a complete feature 10 generation/validation
before another approval request can be staged.

#### Acceptance criteria

- GIVEN an edit decision with the expected revision
  WHEN it succeeds
  THEN the former plan cannot be authorized and the response identifies the
  run/revision requiring externally validated constraints.
- GIVEN a caller presents modified constraints with the old prepared plan
  WHEN staging or authorization is attempted
  THEN the context hash mismatch rejects it as stale.
- GIVEN edited constraints are invalid
  WHEN the caller uses the existing loader
  THEN no replacement approval request can be created from them.

### R7 — Regeneración invalida sin reutilizar aprobación

WHEN `regenerate` is applied to the current request,
THE workflow SHALL append one `plan_invalidated` event with reason
`regeneration_requested`, transition to `planning`, return a typed generation
request reference and no authorization, and SHALL require the new prepared
plan to be staged under a later revision even if its content hashes equal the
previous plan.

#### Acceptance criteria

- GIVEN regeneration is requested
  WHEN the caller invokes feature 10 and receives another prepared plan
  THEN it cannot inherit the former decision and must emit a new approval
  request with a greater revision.
- GIVEN regeneration produces byte-equivalent plan/context content
  WHEN restaged
  THEN the previous approval remains consumed/invalid and a new human decision
  is required.
- GIVEN generation fails or returns insufficient context
  WHEN processing the action result
  THEN no awaiting or executing state is fabricated by this workflow.

### R8 — Invalidación ante cambios relevantes

IF the prepared plan, target identity or base URL, validated constraints,
retained OpenAPI context, allowed capability descriptors, runtime contract or
generation contract differs from the staged snapshot,
THEN THE workflow SHALL classify the approval as stale, append at most one
`plan_invalidated` event with a non-secret structural reason, transition to
`planning`, and SHALL NOT return an authorization.

#### Acceptance criteria

- GIVEN independent changes to plan, target, constraints, OpenAPI,
  capabilities or runtime/generation contract
  WHEN approval is attempted
  THEN each change causes a hash mismatch, no execution transition and a
  stable invalidation category.
- GIVEN only map insertion order changes in semantically identical snapshots
  WHEN hashes are recalculated
  THEN the canonical hashes remain equal.
- GIVEN untrusted payload values in changed context
  WHEN stale diagnostics/events are inspected
  THEN only the structural category and hashes are present, not the value.

### R9 — Autorización opaca como única entrada a ejecución

WHERE approval is enabled,
WHEN deterministic execution is composed,
THE execution boundary SHALL require a valid opaque authorization produced by
R4 for the exact run, plan, revision and fingerprints, SHALL consume only its
immutable approved plan/context snapshots, and SHALL refuse missing, forged,
consumed, stale or mismatched authorization before policy, budget or tool work.

#### Acceptance criteria

- GIVEN no decision, rejection, edit, regeneration or stale approval
  WHEN execution is requested
  THEN it is denied before `PlanExecutor.Execute` and before every tool call.
- GIVEN one valid authorization
  WHEN execution starts for its exact run and plan
  THEN it can cross the gate once and ordinary runtime registry/policy/budget
  controls still apply.
- GIVEN the authorization is reused or paired with another run/plan/context
  WHEN execution is requested
  THEN it is rejected and no effect occurs.

### R10 — Una sola decisión bajo concurrencia

WHEN two or more decisions target the same awaiting revision,
THE workflow SHALL use atomic compare-and-append so exactly one decision can
change state, SHALL return a typed `revision_conflict` or terminal-state result
to every loser, and SHALL never emit two authorizations or contradictory
decision events.

#### Acceptance criteria

- GIVEN concurrent approve/approve, approve/reject and reject/regenerate pairs
  for one revision
  WHEN released together
  THEN exactly one event wins, revisions remain monotonic and at most one
  authorization exists.
- GIVEN a client retries the exact decision after an unknown transport outcome
  with the same decision ID
  WHEN the first event was confirmed
  THEN it receives the same recorded outcome without appending a duplicate;
  reuse of that ID with different content is rejected.
- GIVEN decisions for different runs
  WHEN applied concurrently
  THEN they do not block one another beyond their own store partitions and the
  race detector reports no shared-state race.

### R11 — Journal, fallos y cancelación fail-closed

WHILE staging, reading or deciding an approval request,
THE workflow SHALL propagate cancellation, SHALL treat the append-only journal
as the source for reconstructing revision/state/decisions, SHALL preserve every
confirmed prefix, and IF storage, hashing, sanitization, clock or reconstruction
fails THEN it SHALL return a typed operational error and no authorization.

#### Acceptance criteria

- GIVEN cancellation before or during a boundary operation
  WHEN observed
  THEN no later event or state transition is attempted and the cancellation
  cause remains available through `errors.Is`.
- GIVEN a corrupt, missing-sequence or contradictory event stream
  WHEN reconstructed
  THEN it fails closed and cannot authorize execution.
- GIVEN a failure after earlier events were confirmed
  WHEN retried/reconstructed
  THEN those events are unchanged and no log or mutable state is used as an
  alternative source of truth.

### R12 — Verificación hermética, trazable y documentada

WHEN ordinary repository verification runs,
THE feature SHALL use controlled clocks, identities, stores, sanitizers,
prepared plans and execution spies without network, credentials or real
targets, SHALL cover every transition, invalidation and concurrency branch,
and every new or modified entity and function, including internal and test
helpers, SHALL include a comment describing its responsibility in the system.

#### Acceptance criteria

- GIVEN `go test ./...` and `go test -race ./...` offline
  WHEN run
  THEN all approval tests pass without prompting or contacting external
  services.
- GIVEN R1–R11
  WHEN traceability is reviewed
  THEN each requirement maps to at least one named test and all new/modified
  behavior reaches at least 85% statement coverage.
- GIVEN exported, internal or test-only functions/entities introduced or
  changed by this feature
  WHEN source is reviewed
  THEN each carries a systemic-responsibility comment rather than a mechanical
  restatement of syntax.

## Errores y casos límite

- Una solicitud contiene el plan completo, no una selección parcial de pasos.
- Hashes usan JSON canónico UTF-8, campos versionados, orden de slices
  significativo, keys de maps ordenadas, números JSON sin pérdida y SHA-256 en
  hexadecimal minúscula. La vista saneada queda fuera del material hasheado.
- El context hash incluye target ID/base URL normalizada, restricciones
  validadas en orden, `ReducedContext` retenido, descriptores serializables de
  capacidades, runtime/policy y versión/identidad del contrato de generación.
  No incluye handles, funciones, credenciales ni identidad del aprobador.
- El plan hash incluye checks, scenarios, operaciones derivadas, pasos,
  dependencias, inputs estructurados, assertions y evidence requirements. No
  depende de direcciones de memoria ni iteration order de maps.
- El instante procede de un reloj inyectado y se serializa en UTC RFC3339Nano.
- `decision_id` es obligatorio, opaco y único por run. Su idempotencia no hace
  reutilizable una autorización ya consumida.
- La razón humana opcional se sanea, se limita y no controla transiciones. Los
  eventos de control usan enums y no requieren parsear esa razón.
- Una lectura de vista no cambia revisión ni estado.
- Un fallo al sanear la vista impide crear la solicitud; no se muestra una
  versión parcialmente saneada.
- `cancelled` por rechazo terminal es un estado del run; no significa que el
  contexto de Go haya sido cancelado.

## Decisiones abiertas

Ninguna para esta entrega. La interfaz humana concreta queda desacoplada por
una frontera de aplicación; el repositorio aún no ofrece un comando de
generación de planes que pueda suspenderse y reanudarse, por lo que no se
inventa una CLI persistente específica en esta feature.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-15T18:30:28+02:00
```
