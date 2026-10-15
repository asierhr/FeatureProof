# Aprobación humana del plan — Design

## Resumen de la solución

Se añadirá `internal/planapproval`, un paquete de aplicación que posee la
solicitud, la proyección para humanos, las huellas, las decisiones y la
autorización opaca. El paquete recibe únicamente un plan ya preparado por
feature 10 y snapshots contractuales. No puede generar ni ejecutar.

```text
PreparedPlan + contract snapshot + sanitizer
                 |
                 v
       canonical hashes + safe view
                 |
                 v
 planning --stage/CAS--> awaiting_approval
                 |
       human decision + expected revision
      +----------+-----------+-------------+
      |          |           |             |
   approve    reject      regenerate      edit
      |       /    \          |             |
 current     terminal replan  +------ planning
 hashes?       |       |       (old plan invalid)
      |     cancelled planning
      v
 executing + opaque one-use authorization
      |
      v
 PlanExecutor (runtime policy/budgets remain mandatory)
```

La operación que confirma `approve` compara de nuevo plan y contexto, anexa el
evento mediante CAS y devuelve el único valor que la composición de ejecución
acepta. De este modo no existe un intervalo en el que un booleano mutable o un
texto de aprobación autorice otro snapshot.

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md`, `feature_list.json` id 12 | Alcance, aceptación humana, una feature activa y proceso SDD. |
| `.codex/agents/spec_author.md`, `docs/specs.md` | Salidas del rol, EARS, puerta de aprobación y trazabilidad. |
| `docs/architecture.md` | Máquina con `awaiting_approval`, journal append-only, autorización antes del executor y separación modelo/sistema. |
| `docs/conventions.md`, `docs/verification.md` | Estado tipado, CAS/concurrencia, reloj y fakes, errores, race/coverage y comentarios. |
| `specs/agent-harness`, `internal/harness` | `Run`, estados, journal, eventos, cancelación y transición exclusiva del orquestador. |
| `specs/multi-step-validation-plan`, `internal/planning` | Plan validado inmutable, executor determinista y policy runtime. |
| `specs/evidence-redaction`, `internal/evidence` | Proyección/redacción previa a persistencia o presentación. |
| `specs/local-validation-cli`, `internal/localvalidation` | Composición local actual que preflight-valida y ejecuta planes aportados, sin planificación agéntica suspendible. |
| `specs/openapi-inspector`, `internal/openapi` | Contexto reducido canónico y datos contractuales no confiables. |
| `specs/model-adapter`, `internal/model` | Identidad de proveedor/modelo separada de secretos; no participa en decisión humana. |
| `specs/agent-plan-generation`, `internal/agentplanning` | `PreparedPlan`, checks/evidence, snapshots y garantía de cero ejecución. |

No se consultaron contratos externos. Autenticación e interfaz humana son
puertos de composición, no integraciones inventadas por este diseño.

## Reglas arquitectónicas aplicadas

- El agente propone; la persona autoriza un snapshot; el sistema vuelve a
  validar hashes y conserva policy/budgets antes de efectos.
- Una vista o texto humano no es estado de coordinación. Solo un evento
  confirmado y un grant opaco permiten ejecutar.
- El journal reconstruye la verdad; los logs son diagnóstico.
- Plan, contexto y vistas se copian profundamente. Los stores no reciben
  handles ejecutables ni secretos.
- La primera versión no crea goroutines; el store ofrece atomicidad por run.
- Todas las dependencias bloqueables reciben `context.Context`.
- No hay excepción a arquitectura o convenciones.

## Componentes y responsabilidades

### `ApprovalWorkflow`

Caso de uso consumidor. `Stage` valida estado/input, clona snapshots, calcula
hashes, construye la vista segura y confirma `plan_approval_requested` mediante
CAS. `View` reconstruye la solicitud. `Decide` valida identidad, acción,
revisión e idempotencia; para aprobación rederiva hashes actuales y aplica una
única transición. Nunca llama al executor.

### `ApprovalRequest`

Snapshot privado con run/plan, revisión, plan preparado, contexto contractual,
hashes y vista. Sus métodos devuelven clones. Solo `Stage` puede construirlo.

### `ContractSnapshot`

Modelo serializable propio con target, constraints, ReducedContext proyectado,
descriptores de capacidades sin callbacks, runtime/policy y contrato de
generación. Valida campos/versiones y excluye credenciales. Evita hashear una
función `Validate` o un DTO de proveedor.

### `ApprovalViewBuilder`

Deriva una vista de solo lectura desde el snapshot y usa un puerto mínimo
`Sanitizer`. Enumera todos los elementos revisables y representa campos
sensibles con markers tipados. Una proyección parcial o fallo de sanitizer no
se publica. Se reutilizará la política de `internal/evidence` mediante un
adapter pequeño en composición, sin hacer que el dominio de aprobación lea
configuración.

### `Fingerprinter`

Codifica envelopes versionados mediante una canonicalización acotada y
determinista. Mantiene separados `plan_hash` y `context_hash`. Recorre mapas
por key ordenada, conserva declaration order de slices y contabiliza trabajo y
tamaño para evitar entradas no acotadas.

### `ApprovalStore`

Puerto consumidor mínimo:

```text
Events(ctx, runID) -> []ApprovalEvent
CompareAndAppend(ctx, runID, expectedRevision, decisionID, event)
```

`CompareAndAppend` es atómico dentro de un run, asigna la siguiente revisión y
aplica idempotencia de `decisionID`. La implementación en memoria permite tests
concurrentes. La implementación local persistente mantiene un JSONL append-only
por run, lock exclusivo acotado y fsync antes de confirmar; no mantiene locks
durante sanitizer, hashing, callbacks ni I/O ajeno al propio append.

### `Projector`

Reconstruye estado y revisión desde el stream estricto. Exige secuencias
contiguas, una solicitud antes de decisiones, transiciones válidas y ausencia
de eventos después de terminal. Streams corruptos o contradictorios fallan
cerrados.

### `ExecutionAuthorization` y `ExecutionGate`

El grant tiene campos privados y conserva clones de plan/context. Solo la rama
approve de `Decide` lo crea después del append confirmado. El gate lo consume
una vez y entrega el `planning.ValidatedPlan` exacto a la composición. El
executor no acepta `ApprovalView`, hashes sueltos ni booleanos. Registry,
policy, presupuesto y validation runtime siguen ejecutándose normalmente.

### Identidad y reloj

`Actor{Subject, Source}` llega desde una frontera que ya autenticó a la persona;
el paquete solo exige identidad estable. La CLI local futura podrá usar una
identidad explícita de OS/composición, mientras que una API podrá usar el
subject autenticado. `Clock` fija el instante auditable en tests.

## Paquetes afectados

```text
internal/
  planapproval/
    model.go          # acciones, snapshots, vista, eventos, errores y grant
    canonical.go      # hashes canónicos acotados
    view.go           # proyección exhaustiva y saneada
    workflow.go       # stage/read/decide y transiciones
    store.go          # puerto, store memoria y persistencia local append-only
    gate.go           # consumo único del grant
    *_test.go         # unitarios, integración, concurrencia y límites
  harness/
    state.go          # awaiting_approval y transiciones aprobadas
    journal.go        # tipos de eventos de aprobación/proyección común
  planning/
    executor.go       # frontera de composición exige grant cuando aplique
```

La integración exacta puede mantener eventos de aprobación en su propio
journal si ampliar `harness.Event` obligara a acoplar datos privados. Debe
existir una única proyección append-only por run y un único CAS autoritativo.
No se modifica `cmd/featureproof`: el comando actual consume manifests humanos
y no dispone aún de una sesión de generación agéntica que pueda presentar y
reanudar. `ApprovalWorkflow` es la frontera explícita para un adapter posterior.

Toda entidad y función nueva o modificada, exportada, interna o helper de test,
recibirá un comentario que explique su responsabilidad sistémica.

## Estados y transiciones

```text
planning --stage--> awaiting_approval

awaiting_approval --approve + current hashes + CAS--> executing
awaiting_approval --reject_terminal---------------> cancelled
awaiting_approval --reject_replan-----------------> planning
awaiting_approval --regenerate--------------------> planning
awaiting_approval --edit_constraints--------------> planning
awaiting_approval --stale snapshot----------------> planning
```

- Solo `stage` entra en `awaiting_approval` y siempre incrementa revisión.
- Solo `approve` exitoso produce `executing` y grant.
- Toda salida de `awaiting_approval` consume esa revisión, incluso si un nuevo
  snapshot resulta idéntico.
- `cancelled` es terminal. Volver a `planning` requiere generar/validar y hacer
  un `stage` posterior.
- `View` no es una transición.
- El projector, y no flags derivados, determina el estado.

## Contratos y datos

### Evento de aprobación v1

Campos comunes: `version`, `sequence`, `revision`, `event_id`, `decision_id`
cuando aplique, `type`, `run_id`, `plan_id`, `occurred_at`. Los eventos de
request/decision contienen `plan_hash` y `context_hash`.

Tipos cerrados:

- `plan_approval_requested`;
- `plan_approved`;
- `plan_rejected` con disposition `terminal|replan`;
- `plan_invalidated` con reason
  `constraints_edited|regeneration_requested|plan_changed|target_changed|constraints_changed|openapi_changed|capabilities_changed|runtime_changed|generation_contract_changed`;
- `approval_authorization_consumed` si el store y gate se separan en dos
  operaciones persistentes.

No se persisten plan/context crudos dentro del evento. El store conserva el
snapshot saneado/privado asociado a la solicitud o una referencia íntegra; el
journal contiene hechos de control y hashes.

### Errores

`Error` expone `Category`, `Operation` y `Field` seguros. Categorías:
`invalid_input`, `invalid_state`, `revision_conflict`, `decision_conflict`,
`stale_approval`, `unauthorized_identity`, `limit_exceeded`, `cancelled`,
`integrity_failure`, `storage_failure`, `sanitization_failure` e `internal`.
Valores no confiables y errores externos no se interpolan ni se conservan como
unwrap salvo `context.Canceled`/`DeadlineExceeded` y causas locales seguras.

## Flujo principal

1. La composición recibe `PreparedPlan` y contexto ya validado.
2. `Stage` comprueba `planning`, clona y valida límites.
3. Calcula plan/context hash sobre snapshots semánticos.
4. Construye toda la vista mediante sanitizer.
5. Anexa request con expected revision y proyecta `awaiting_approval`.
6. La interfaz humana llama `View` y presenta exclusivamente esa proyección.
7. Envía `DecisionCommand` con actor, action, decision ID y revisión vista.
8. `Decide` reconstruye estado. Para approve recalcula hashes desde snapshots
   actuales aportados por composición.
9. El store confirma evento y transición mediante CAS.
10. Solo approve devuelve un grant opaco; el gate lo consume una vez y entrega
    el snapshot exacto al executor.

## Flujos de error

- **Plan/context inválido:** no se crea request ni vista parcial.
- **Sanitization/hash failure:** no se anexa request.
- **Snapshot stale al aprobar:** invalidación CAS, vuelta a planning y cero
  grant. Si otro actor ganó primero, se devuelve conflicto sin segundo evento.
- **Revisión obsoleta/doble decisión:** el store devuelve conflicto y el
  caller relee la proyección.
- **Append/fdatasync fallido:** el evento no se considera confirmado y no sale
  grant.
- **Stream corrupto:** reconstruction error; ninguna reparación silenciosa.
- **Cancelación:** detiene antes del siguiente límite bloqueable. No se usa
  `context.Background`; una finalización confirmada no se deshace.
- **Grant consumido/mismatched:** gate rechaza antes de executor.

## Propiedad, concurrencia e idempotencia

El workflow no conserva estado mutable de requests. Cada operación reconstruye
una proyección y entrega valores copiados. `ApprovalStore` posee orden,
revisión, unicidad de decision ID y exclusión por run. Dos runs usan locks
distintos; una misma revisión tiene un solo ganador.

El caller genera `decision_id`. Repetir exactamente actor/action/run/plan/
revision con el mismo ID devuelve el resultado confirmado; cambiar cualquier
campo bajo ese ID produce `decision_conflict`. La autorización registrada solo
puede consumirse una vez, también mediante CAS. No hay retries internos de una
decisión no idempotente; el caller puede repetir el mismo comando.

## Cancelación y límites

Hashing, canonicalización, vista y reconstrucción comprueban contexto durante
traversals y aplican límites explícitos a bytes, constraints, checks, steps,
assertions, evidence requirements, profundidad y eventos. No se crean
goroutines. I/O observa contexto antes y después de lock/adquisición/escritura;
los tests usan coordinación por canales, nunca `time.Sleep`.

## Observabilidad y seguridad

Eventos contienen IDs internos, enum, revisión, timestamp y hashes. La razón
humana opcional solo se persiste tras saneado y límite; no es necesaria para
reconstruir. Logs pueden incluir run/plan/revision/category, nunca plan,
contexto, identidad de display, razón libre, credentials o errores externos.
La vista es el único artefacto humano y ya está saneada.

## Estrategia de pruebas

| Requirement | Componente | Prueba prevista |
|---|---|---|
| R1 | workflow/state | `TestStageEntersAwaitingWithoutExecution`; `TestStageRejectsUnpreparedOrIncompleteSnapshot` |
| R2 | view/sanitizer | `TestApprovalViewIsCompleteStableAndSecretFree`; `TestViewMutationCannotChangeRequest` |
| R3 | decision validator | `TestDecisionRequiresClosedActionTrustedIdentityAndExpectedRevision` |
| R4 | approve/CAS/grant | `TestApproveRecordsIdentityHashesAndReturnsBoundGrant`; `TestApprovalAppendFailureReturnsNoGrant` |
| R5 | reject/projector | `TestRejectTerminalCancelsAndRejectReplanReturnsToPlanning` |
| R6 | edit | `TestEditInvalidatesPlanAndRequiresValidatedRegeneration` |
| R7 | regenerate | `TestRegenerationRequiresNewRevisionEvenForEquivalentPlan` |
| R8 | fingerprints | `TestEveryRelevantContextChangeInvalidatesApproval`; `TestCanonicalHashesIgnoreMapOrder` |
| R9 | gate | `TestExecutionRequiresMatchingOneUseAuthorization` |
| R10 | store/concurrency | `TestConcurrentDecisionsHaveExactlyOneWinner`; `TestDecisionRetryIsIdempotent`; `TestIndependentRunsProgressConcurrently` |
| R11 | projector/store | `TestCorruptJournalFailsClosed`; `TestCancellationPreservesConfirmedPrefix` |
| R12 | suite/comments | `TestPlanApprovalBoundariesHaveNoNetworkOrExecutableDependency`; canonical verification suite |

Además se ejecutarán tests de integración entre approval gate y un
`PlanExecutor` spy, state transition tests del harness, file-store recovery,
failure injection antes/después de append y race tests repetidos sobre CAS.

## Alternativas y decisiones

- **Booleano `approved`:** descartado porque no liga la decisión al contenido,
  no resuelve carreras y no aporta auditoría.
- **Hashear la vista saneada:** descartado; cambios de presentación alterarían
  aprobaciones y una redacción podría ocultar cambios semánticos. Se hashean
  snapshots completos sin persistir secretos en eventos.
- **Permitir editar el plan en la vista:** descartado; todo cambio de intención
  debe volver a generación y validación deterministas.
- **Aprobación por paso:** descartada; debilita las invariantes all-or-nothing
  de `PreparedPlan` y deja combinaciones sin validar.
- **Inferir identidad desde texto/entorno en core:** descartado; una frontera
  autenticada la aporta explícitamente.
- **Bloqueo global del store:** descartado; serializaría runs independientes.
- **Añadir ahora una CLI interactiva:** descartado porque la CLI productiva
  actual ejecuta manifests aportados y feature 10 aún no ofrece un comando de
  generación reanudable. El workflow queda listo para CLI/API sin fijar una UX
  ficticia.
- **Autorizar con hashes aportados por el cliente:** descartado; el sistema
  recalcula sobre snapshots actuales y usa CAS.

## Mapa inicial de trazabilidad

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T3, T5, T8 | Staging entra en espera sin efectos ni grant. |
| R2 | T3, T4, T8 | Vista exhaustiva, estable, aislada y saneada. |
| R3 | T1, T5, T8 | Solo acciones e identidades válidas deciden. |
| R4 | T2, T5, T6, T8 | Approve registra hashes/actor y crea un grant ligado. |
| R5 | T1, T5, T8 | Rechazos producen únicamente cancelled o planning. |
| R6 | T5, T8 | Edición invalida y fuerza el pipeline de validación. |
| R7 | T5, T8 | Regeneración consume revisión y exige nueva decisión. |
| R8 | T2, T5, T8 | Todos los cambios relevantes vuelven stale la aprobación. |
| R9 | T6, T7, T8 | El executor solo recibe el snapshot de un grant válido. |
| R10 | T1, T5, T8 | CAS e idempotencia impiden doble decisión/autorización. |
| R11 | T1, T5, T8 | Journal reconstruible, cancelable y fail-closed. |
| R12 | T1–T9 | Suite hermética, comentarios, coverage y trazabilidad. |
