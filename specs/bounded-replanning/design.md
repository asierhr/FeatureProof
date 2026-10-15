# Replanificación agéntica acotada — Design

## Resumen de la solución

Se añadirá `internal/replanning`, un paquete de aplicación que coordina el
ciclo posterior a cada lote ejecutado. Recibe snapshots de planificación,
resultados deterministas y referencias de evidencia confirmadas; produce un
resumen redactado, reserva un ciclo en la sesión de modelo, obtiene una decisión
estructurada y, si hay checks adicionales, reutiliza generación, aprobación y
ejecución sin crear una ruta privilegiada.

```text
PreparedPlan aprobado/permitido
            |
            v
 PlanExecutor -> evidence + assertion results
            |
            v
 append-only assertion ledger
            |
            v
 redacted summary + original authorized contract
            |
       reserve replan/model budgets
            |
            v
 terminate | insufficient_context | additional_checks
      |              |                    |
 deterministic   inconclusive       strict decode
 verdict          (unless failed)          |
                                  semantic fingerprint
                                           |
                               generation validation/policy
                                           |
                           approval revision when required
                                           |
                         runtime policy/budget/tool/evidence
                                           |
                                      next cycle
```

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md`, `feature_list.json` id 13 | Alcance, acceptance, feature única y proceso SDD. |
| `.codex/agents/spec.md`, `docs/specs.md` | Artefactos, EARS, aprobación humana y protocolo de finalización. |
| `docs/architecture.md` | El agente propone; el sistema valida/autoriza/ejecuta; verdicts solo desde evidencia; journal y límites. |
| `docs/conventions.md`, `docs/verification.md` | Contexto, concurrencia, errores, fakes, race, coverage, trazabilidad y comentarios sistémicos. |
| `specs/agent-harness`, `internal/harness` | Aggregate `Run`, budgets de steps/tools/retries, journal, estados y finalización. |
| `specs/multi-step-validation-plan`, `internal/planning` | `ValidatedPlan`, resolución de referencias, policy runtime, executor y `PlanResult`. |
| `specs/assertion-engine`, `internal/assertion` | Evaluación ternaria determinista y árboles auditables ligados a evidencia. |
| `specs/evidence-redaction`, `internal/evidence` | Sanitizer, store sellado, truncación y proyección restringida. |
| `specs/model-adapter`, `internal/model` | `Session`, inferencia estructurada y budgets atómicos de calls/tokens/duration/replans sin dinero. |
| `specs/agent-plan-generation`, `internal/agentplanning` | Protocolo seguro, validación, fingerprints, `PreparedPlan`, contexto descriptor-only y cero ejecución. |
| `specs/plan-approval`, `internal/planapproval` | Revisión, vista saneada, CAS, grant ligado y `ExecutionComposition`. |

No se consultaron contratos externos. La feature compone únicamente contratos
internos ya aprobados.

## Reglas arquitectónicas aplicadas

- El modelo solo emite una propuesta; el coordinador valida y el executor
  conserva registry, policy, budgets, redacción y assertions.
- El resumen dinámico nunca contiene cuerpos de evidencia. El contexto
  autorizado original se vuelve a suministrar como datos no confiables para
  que el modelo pueda referenciar únicamente operaciones/capacidades retenidas.
- El ledger y journal, no el texto del modelo, determinan verdad y progreso.
- Los presupuestos se reservan antes de trabajo y no dependen de precios.
- Cada lote se trata de forma all-or-nothing antes de aprobación/ejecución.
- El primer diseño es secuencial por run, cancelable y sin goroutines.
- No hay excepción a arquitectura o convenciones.

## Componentes y responsabilidades

### `Coordinator`

Caso de uso principal. Valida la sesión y límites, ejecuta o incorpora el lote
inicial ya autorizado, confirma sus resultados, construye cada resumen,
reserva el siguiente ciclo, procesa una decisión y dirige un lote adicional a
las fronteras existentes. Solo avanza mediante CAS confirmado y nunca mantiene
un lock al invocar colaboradores.

### `SummaryBuilder`

Lee `PlanResult` y registros de evidencia confirmados, verifica pertenencia al
run, aplica el sanitizer a valores expected/observed y produce un
`ExecutionSummaryV1` inmutable. Ordena identidades de forma estable, limita
bytes/nodos/items y calcula el digest canónico usado por lineage.

El envelope exacto visible al modelo contiene:

```text
version
run_id, cycle, parent_decision_id
constraints[]: id, aggregate_status
checks[]: batch_id, check_id, constraint_id, scenario, status
  steps[]: step_id, status, safe_cause, evidence_ids,
    assertions[]: declaration_id, node_path, operator, path, status,
                  sanitized_expected, sanitized_observed,
                  safe_cause, evidence_ids
remaining: replans, agent_steps, model_calls, model_tokens,
           tool_calls, validation_work, duration
seen_proposal_fingerprints[]
```

Además incluye el snapshot original de constraints, `ReducedContext` y
descriptores de capabilities sin callbacks. No incluye target mutable,
credenciales, handles, bodies de evidencia, logs, responses crudas, errores
externos ni texto anterior del modelo. Todo el envelope es `untrusted_data`;
solo las reglas del protocolo son instrucciones privilegiadas.

### `DecisionCodec`

Decodifica JSON v1 estricto y una unión exclusiva:

- `terminate {}`;
- `insufficient_context {items[]}`;
- `additional_checks {batch_id, checks[]}`.

La rama adicional reutiliza el modelo de `CandidateCheck`; IDs y referencias
se confinan al contexto actual. El codec clona y limita antes de devolver.

### `ProposalPreparer`

Adapta el batch a las validaciones de `agentplanning`: seguridad profunda,
operation/capability resolution, assertion validation, generation policy,
plan validator, cobertura y materialización. No contiene tools ejecutables. Un
fallo rechaza el batch completo.

### `FingerprintSet`

Canonicaliza la semántica con el algoritmo aprobado de feature 10: colorea el
DAG, reemplaza IDs/referencias por identidades canónicas, ordena maps y pasos
independientes y conserva cambios semánticos. El digest del batch completo se
reserva mediante CAS en el journal; estados `proposed`, `approved` y `executed`
son equivalentes para impedir repetición.

### `AssertionLedger`

Proyecta resultados append-only por identidad compuesta de run, batch, check,
step, declaration y node path. Confirma evidencia y estado una vez. Replays
idénticos son idempotentes; un estado distinto para la misma identidad es
corrupción. La agregación da precedencia `failed > inconclusive > passed` y
nunca permite que otra declaración sobrescriba un fallo.

### `BudgetController`

Combina contadores propios (`agent_steps`, `tool_calls`, `validation_work`) con
`model.Session` (`calls`, `tokens`, `duration`, `replans`). Todas las reservas
de count/work son CAS con resta previa y aritmética comprobada. Se llama
`ReserveReplan` antes de cada inferencia posterior al lote; `InferStructured`
reserva la llamada. El executor sigue reservando tool calls en el aggregate.

No existe dimensión monetaria: el texto anterior de coste estimado está
resuelto por calls/tokens/duration/replans y las unidades deterministas del
harness.

### `ApprovalBridge` y `BatchExecutor`

Puertos consumidores pequeños. En modo `required`, el bridge crea una nueva
revisión con `ApprovalWorkflow.Stage`; solo un grant vigente y consumido por
`ExecutionComposition` permite el lote. En modo `disabled`, usa la rama fijada
en composición. El lote adicional mantiene el mismo snapshot contractual y
una identidad de plan derivada del run/ciclo/fingerprint. Una edición o
regeneración vuelve a preparación; nunca muta el batch aprobado.

### `ReplanJournal` y `Projector`

El store append-only ofrece `Events` y `CompareAndAppend` por run/revisión con
idempotency key. El projector estricto valida secuencia, schema y transiciones.
Eventos v1:

- `replan_cycle_reserved`;
- `replan_summary_confirmed`;
- `replan_decision_received`;
- `replan_proposal_rejected|repeated|prepared`;
- `replan_approval_requested|resolved` (referencias, sin duplicar el journal de
  feature 12);
- `replan_batch_execution_started|completed`;
- `replan_assertions_confirmed`;
- `replan_terminated|cancelled|failed`.

Cada evento conserva IDs/digests/counters/categorías permitidos, no payloads.

## Paquetes afectados

```text
internal/
  replanning/
    model.go          # decisiones, summary, budgets, lineage y errores
    protocol.go       # JSON estricto v1
    summary.go        # proyección saneada y digest
    fingerprint.go    # identidad semántica del batch
    ledger.go         # assertions monotónicas y verdict
    journal.go        # eventos, store, CAS y projector
    coordinator.go    # ciclo y admisión de trabajo
    compose.go        # adapters a generation/approval/execution/model
    *_test.go
  agentplanning/
    ...               # factor mínimo para preparar un batch ya decodificado
  planapproval/
    ...               # staging repetible de revisiones del mismo run
  harness/
    ...               # counters/eventos/transiciones que deban compartirse
```

Se preferirá reutilizar funciones actuales antes que duplicar validadores. Los
cambios fuera de `internal/replanning` serán adaptaciones mínimas y conservarán
compatibilidad. Toda entidad, función o helper de test nuevo o modificado
tendrá un comentario sobre su responsabilidad sistémica.

## Estado y transiciones

```text
executing batch
      |
      v
reviewing_results --confirm ledger--> replanning
      ^                                  |
      |                      +-----------+-----------+
      |                      |           |           |
      |                  terminate  insufficient  additional
      |                      |           |           |
      |                   terminal    terminal    prepared
      |                                              |
      |                                  approval required?
      |                                    /          \
      |                         awaiting_approval    executing
      |                              |                    |
      +---------------- approve -----+------ execute -----+
```

- `failed` confirmado domina el verdict aunque el loop termine por límite.
- Un límite sin fallo y con evidencia incompleta termina `inconclusive`.
- `error` se reserva para fallo interno/integridad/auditoría no confirmable.
- Rechazo terminal de aprobación cancela; rechazo a replan/regeneración vuelve
  a preparación dentro de los presupuestos restantes.
- No se inicia otro ciclo después de un evento terminal confirmado.

## Flujo principal

1. Composición fija modo de aprobación, contract snapshot, sesión de modelo,
   límites, stores y adapters.
2. El lote inicial autorizado se ejecuta por `ExecutionComposition` y
   `PlanExecutor`.
3. El coordinador confirma resultados y los incorpora al ledger.
4. `SummaryBuilder` recupera referencias, sanea valores y confirma digest.
5. Se reservan atómicamente cycle/replan/agent-step; la sesión reserva model
   call al ejecutar `InferStructured`.
6. `DecisionCodec` valida la respuesta.
7. `terminate` o `insufficient_context` finalizan mediante el ledger.
8. `additional_checks` se valida, fingerprinta y reserva contra duplicados.
9. `ProposalPreparer` produce todo el `PreparedPlan` o rechaza todo el batch.
10. Si approval es required, se publica una nueva vista/revisión y se espera
    una autorización exacta; si no, continúa por la rama disabled compuesta.
11. Antes de cada tool, el executor reevalúa runtime policy y presupuesto,
    ejecuta, redacta/persiste evidencia y evalúa assertions.
12. Los resultados se confirman con lineage, se actualiza ledger y se repite
    desde el paso 4 mientras haya presupuesto y no exista decisión terminal.

## Flujos de error

- **Summary/sanitizer/store:** no hay inferencia; error operativo tipado.
- **Modelo/protocolo:** el intento y budgets quedan consumidos; no se ejecuta
  propuesta. El coordinador puede continuar solo si la policy de errores
  explícita y los límites lo permiten; v1 termina `error` para fallo de
  frontera y `inconclusive` para respuesta semánticamente insuficiente.
- **Batch inválido/policy denial:** rechazo auditable, cero aprobación/tools;
  se puede solicitar otra decisión si quedan límites.
- **Fingerprint repetido:** se registra, no se aprueba ni ejecuta; el ciclo ya
  reservado permanece consumido.
- **Approval denied/stale:** cero tool work; disposition define terminal o
  regreso a planning sin reutilizar grant.
- **Runtime denial/exhaustion:** el step resulta inconclusive; evidencia y
  fallos anteriores permanecen.
- **Journal/ledger corrupto:** fail closed, verdict operativo `error` y cero
  trabajo posterior.
- **Cancelación:** se propaga; un prefijo confirmado no se deshace y se intenta
  un único evento terminal con contexto detached y deadline acotado.

## Propiedad, concurrencia e idempotencia

El coordinator no guarda contexto ni estado mutable autoritativo. Journal,
ledger, approval store, evidence store y model session poseen sus respectivos
snapshots. Un `runLifecycle`/CAS por run serializa reserva, decisión, proposal y
terminal; runs distintos no comparten lock. Ningún lock cubre callbacks o I/O.

IDs de cycle/decision/batch y claves de idempotencia los aporta la composición
o un generador inyectado. Repetir el mismo comando reconstruye la proyección;
cambiar contenido bajo la misma clave es conflicto. Inferencias y tools no se
reintentan invisiblemente. Los retries seguros existentes conservan sus keys y
consumen budgets.

## Cancelación y límites

Todas las operaciones bloqueables reciben `context.Context`. Los recorridos de
summary, canonicalización, validación, proyección y ledger comprueban contexto
entre unidades. El deadline efectivo es el menor entre contexto, duración
total y timeout del colaborador. No se usa `time.Sleep` en tests.

Límites obligatorios: bytes de input/output/summary, constraints, checks,
steps/check, assertions/step, evidence refs, missing items, eventos, ledger
nodes, validation work, cycles/replans, agent steps, model calls/tokens,
tool calls y total duration. Cero deniega una unidad contable; tamaños y
duraciones estructurales necesarios deben ser positivos.

## Observabilidad y seguridad

La telemetría usa allowlist: run/cycle/decision/batch/plan IDs, fingerprints,
summary digest, estado, categoría, counters, duration y referencias opacas. No
registra prompts, respuesta del modelo, cuerpo de evidencia, valores de
assertion, razón externa, target, perfil OAuth o credentials. Los eventos de
feature 12 se referencian por revisión/digest sin copiar actor o vista.

## Estrategia de pruebas

| Requirement | Componente | Prueba prevista |
|---|---|---|
| R1 | summary/request | `TestSummaryIsExactStableRedactedAndBounded`; `TestSummaryFailureStartsNoInference` |
| R2 | protocol | `TestDecisionProtocolAcceptsExactlyThreeExclusiveBranches`; `TestMalformedDecisionStartsNoDownstreamWork` |
| R3 | preparer/executor | `TestAllowedAdditionalCheckTraversesCompletePipeline`; `TestInvalidOrDeniedBatchExecutesNothing` |
| R4 | budgets/session | `TestEveryBudgetIsReservedBeforeItsWork`; `TestConcurrentLastReplanHasOneWinner`; `TestBudgetModelHasNoMonetaryDimension` |
| R5 | coordinator/verdict | `TestExhaustionStartsNoWorkAndKeepsFailedPrecedence`; `TestExhaustionWithoutEvidenceIsInconclusive` |
| R6 | ledger | `TestFailedAssertionIsMonotonicAcrossReplans`; `TestLedgerReplayIsIdempotentAndConflictFailsClosed` |
| R7 | fingerprint | `TestEquivalentAdditionalBatchesAreRejectedBeforeApprovalAndTools`; `TestSemanticChangeProducesDifferentFingerprint` |
| R8 | journal/projector | `TestLineageReconstructsDecisionEvidenceAndActions`; `TestCorruptOrForkedLineageFailsClosed` |
| R9 | approval bridge | `TestAdditionalBatchRequiresFreshApproval`; `TestDeniedStaleAndDisabledApprovalPaths` |
| R10 | CAS/cancellation | `TestConcurrentCycleHasOneConfirmedWinner`; `TestCancellationStopsNextWorkAndPreservesPrefix` |
| R11 | ledger/verdict | `TestVerdictUsesOnlyCompleteAssertionLedger`; `TestModelClaimCannotOverrideDeterministicResult` |
| R12 | integration | `TestBoundedReplanningEndToEndWithFakes`; race/repetition/coverage y revisión de comentarios |

Las pruebas usan clocks, IDs y waiters controlados, stores temporales/fakes y
un executor falso o servidores locales solo donde ya sea contrato del tool.
Nunca usan proveedor, OAuth, red externa, target real ni credenciales.

## Alternativas y decisiones

- **Entregar evidencia cruda al modelo:** descartado; amplía exposición y hace
  al texto externo parte del control. Se usa resumen saneado y referencias.
- **Añadir checks directamente al executor:** descartado; omite generación,
  registry, schema, policy y aprobación.
- **Permitir al modelo emitir verdict:** descartado; el ledger determinista es
  la única fuente del verdict.
- **Reevaluar y reemplazar assertions anteriores:** descartado; destruiría
  auditabilidad y permitiría ocultar fallos.
- **Detectar duplicados por ID:** descartado; IDs decorativos permiten loops.
- **Reutilizar la aprobación inicial:** descartado; no liga la persona al
  contenido adicional.
- **Límite monetario:** descartado por la decisión aprobada en feature 9; no se
  introducen precios ni estimaciones.
- **Paralelizar checks/replans:** descartado en v1 para mantener orden,
  presupuestos y lineage deterministas.

## Mapa inicial de trazabilidad

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T2, T8 | Request exacta, estable, redactada y acotada. |
| R2 | T1, T3, T8 | Unión v1 estricta con tres ramas. |
| R3 | T4, T7, T8 | Checks adicionales atraviesan todo el pipeline. |
| R4 | T1, T5, T8 | Todas las dimensiones se reservan antes y no hay coste monetario. |
| R5 | T5, T7, T8 | Agotamiento no inicia trabajo y conserva precedencia de fallo. |
| R6 | T3, T6, T8 | Ledger append-only y fallos monotónicos. |
| R7 | T3, T4, T8 | Equivalencia semántica bloquea repeticiones. |
| R8 | T3, T7, T8 | Lineage completo reconstruible. |
| R9 | T4, T7, T8 | Cada batch adicional requiere revisión/grant nuevo. |
| R10 | T3, T5, T7, T8 | CAS, idempotencia y cancelación sin locks externos. |
| R11 | T6, T7, T8 | Verdict solo desde ledger/evidencia. |
| R12 | T1–T9 | Suite hermética, coverage, race, comentarios y trazabilidad. |
