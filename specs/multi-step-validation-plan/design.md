# Planes de validación de varios pasos — Design

## Resumen de la solución

Se incorporará un modelo interno de plan y un ejecutor secuencial determinista.
El plan se valida completamente contra las restricciones y el registry antes
de cualquier efecto. El validador construye un orden topológico estable usando
la posición declarada para deshacer empates. El ejecutor recorre ese orden,
resuelve referencias desde evidencia confirmada, conserva las fronteras de
control del harness y asigna un estado terminal a cada paso.

```text
ValidationPlan + Specification + Registry
                 |
                 v
       validación estructural y de grafo
                 |
                 v
       orden topológico determinista
                 |
                 v
   por cada paso: dependencias satisfactorias?
          | sí                         | no
          v                            v
 resolver refs desde evidencia       blocked + causas
 -> validar input -> policy -> budget
 -> tool -> sanitize -> evidence
 -> StepAssertionEvaluator
          |
          v
 PlanResult + eventos reconstruibles
```

El evaluador de assertions es un puerto consumidor. Esta feature usa un fake
determinista en pruebas y no define operadores; `assertion-engine` podrá
implementar el puerto después sin cambiar el planificador.

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md` | Contexto del producto, una feature por sesión, SDD y responsabilidades de agentes. |
| `feature_list.json`, feature `id=3` | Pasos, referencias tipadas, validación de grafo, orden determinista, propagación, journal y tests herméticos. |
| `.codex/agents/spec.md` | Salidas permitidas, contenido obligatorio y condición de finalización del spec author. |
| `docs/specs.md` | EARS, puerta humana, tasks TDD y trazabilidad `R → task → test`. |
| `docs/architecture.md` | `ValidationPlan`, `PlanStep`, plan validator, deterministic executor, ejecución secuencial, evidencia, estados e invariantes ARCH-001 a ARCH-012. |
| `docs/conventions.md` | Interfaces del consumidor, contexto, errores tipados, concurrencia, eventos y tests deterministas. |
| `specs/agent-harness/` e `internal/harness/` | Contratos implementados de `Run`, `ToolCall`, `ToolSpec`, `Registry`, `Policy`, presupuestos, retries, journal y orden de controles. |
| `specs/validation-specification/` e `internal/specification/` | `Specification`, constraints, IDs, límites, errores por field path y aislamiento respecto de YAML. |
| `internal/evidence`, `internal/tools`, `internal/policy` | Evidencia inmutable por intento, registry ordenado y policy deny-by-default existentes. |

No se consultaron repositorios, APIs ni documentación externa. No se incorpora
un contrato de terceros.

## Contratos existentes observados

- `specification.Specification` contiene constraints validadas y límites, pero
  no contiene planes ni depende del harness.
- `harness.Registry` resuelve `ToolSpec` por nombre y cada spec contiene el
  validador de input, efectos, idempotencia y ejecutor.
- `harness.Policy` se evalúa antes de cada llamada; una denegación nunca puede
  ser anulada por el plan.
- `harness.Run` posee límites y reservas de pasos, llamadas y retries. El nuevo
  flujo reutilizará esas reglas o extraerá comportamiento mínimo sin cambiar su
  semántica observable.
- `evidence.Store` confirma records inmutables y verifica integridad al leer.
  La evidencia actual identifica run, tool call, herramienta e intento. Para
  reconstruir el origen del plan se añadirá `StepID` sin eliminar campos.
- `harness.Journal` es append-only por run y asigna secuencia monotónica. Los
  eventos de plan ampliarán el contrato con campos tipados, no con texto que
  luego deba parsearse.
- El harness actual maneja una restricción y una propuesta por iteración. No se
  hará que el modelo ejecute el plan paso a paso: una vez validado, el nuevo
  plan executor es determinista.

## Contratos internos propuestos

### Modelo del plan

El paquete `planning` será propietario de:

- `ValidationPlan`: ID, constraint ID y slice ordenado de `PlanStep`; la
  restricción se resuelve una vez y es común a todos sus pasos.
- `PlanStep`: ID, tool, input, assertions y dependencies.
- `InputValue`: árbol compuesto por literales JSON compatibles y
  `OutputReference` explícitas; una referencia no se representa mediante una
  string con interpolación.
- `OutputReference`: source step ID, `OutputPath` y `ValueType`.
- `PathSegment`: unión válida de `Field` o `Index`, exactamente uno.
- `AssertionDeclaration`: ID y definición estructurada opaca no vacía.
- `ValidatedPlan`: snapshot que conserva pasos, declaration order, cierre de
  dependencias y orden topológico; solo lo produce el validador.
- `StepStatus`: `passed`, `failed`, `inconclusive`, `blocked`.
- `StepResult`: estado, evidence IDs y causas tipadas.
- `PlanResult`: resultado agregado y resultados en declaration order.

La representación externa del plan queda fuera de alcance. No se añaden tags
YAML al dominio.

### Errores de validación y resolución

Los errores distinguibles incluyen categoría y field path:

- `required` y `invalid_value` para entidades incompletas;
- `duplicate_id` para pasos, assertions o dependencies repetidos;
- `unknown_reference` para constraint, tool, dependency o source desconocido;
- `invalid_reference` para source no ancestro, path inválido o tipo no
  soportado;
- `dependency_cycle` para ciclos;
- `resolution_failed` para path ausente, evidencia ajena/no recuperable o tipo
  observado incompatible durante ejecución.

La validación recorre plan y pasos en declaration order y campos en orden
contractual. El detector de ciclos visita vecinos en declaration order y
reporta el primer nodo del primer ciclo encontrado de forma estable.

### Puerto de assertions mínimo

`StepAssertionEvaluator` pertenecerá al paquete consumidor del ejecutor y
aceptará contexto, paso, output/evidence IDs confirmados. Devolverá únicamente
un estado `passed`, `failed` o `inconclusive` y metadatos opacos necesarios para
eventos. No acepta `blocked`, no muta el plan y no decide el resultado global.

La implementación productiva completa se reserva para feature 4. En esta
feature un fake permite demostrar propagación y agregación sin anticipar
operadores, rutas ni grupos.

## Componentes y responsabilidades

### Plan validator

1. Valida ID, constraint ID y existencia de pasos.
2. Resuelve la constraint del plan e indexa tools y step IDs sin normalizarlos.
3. Recorre cada paso y valida campos, assertions, dependencies e input.
4. Comprueba que cada output reference apunte a un step existente y que su
   path/type tenga forma válida.
5. Detecta ciclos.
6. Calcula cierre transitivo y rechaza referencias cuyo source no sea ancestro.
7. Calcula un orden topológico estable con una cola ordenada por declaration
   order.
8. Devuelve `ValidatedPlan` o el primer error; nunca un plan parcial.

El validador comprueba el árbol de input, los literales y la forma/tipo declarado
de las referencias. `ToolSpec.Validate` se invoca sobre el input completamente
resuelto justo antes de policy. Los validadores existentes no reciben
placeholders y no necesitan conocer tipos de `planning`.

### Reference resolver

Recibe un paso listo y resultados de sus ancestros. Recupera cada evidence ID
mediante el store, exige mismo `RunID` y `StepID`, selecciona el path segmento a
segmento y verifica el tipo. Construye una copia profunda del input con valores
resueltos. No modifica el plan ni acepta observaciones no persistidas.

Si hay varios intentos de una herramienta, el step result identifica
explícitamente la evidencia terminal que aporta el output. Evidencias de
intentos transitorios anteriores siguen auditables pero no alimentan
referencias.

### Deterministic plan executor

Es el único propietario de la mutación de estados de pasos y del resultado.
Recorre el orden topológico una vez:

1. Reúne dependencias directas no `passed`.
2. Si existen, marca `blocked`, registra todas en declaration order y continúa.
3. Resuelve de nuevo la herramienta por nombre para no conservar handles en el
   plan validado.
4. Resuelve referencias desde evidencia.
5. Valida el input resuelto con `ToolSpec.Validate`.
6. Evalúa policy y reserva presupuesto.
7. Ejecuta la herramienta con cancelación y retries ya autorizados.
8. Sanea y persiste cada intento; enlaza la evidencia terminal.
9. Invoca el puerto de assertions sobre evidencia confirmada.
10. Confirma el estado terminal del paso y continúa con el siguiente.

Una denegación, presupuesto agotado, fallo permanente de herramienta, retry
agotado o resolución imposible produce `inconclusive` para el paso. Una
assertion incumplida produce `failed`. Un fallo interno del journal/evidence
store detiene la ejecución como error de FeatureProof y no se convierte en
resultado funcional.

### Aggregator

Agrega sin modelo: `failed` si existe cualquier step `failed`; en ausencia de
fallos, `inconclusive` si existe `inconclusive` o `blocked`; `passed` solo si
todos están `passed`. Devuelve resultados en declaration order para que la
vista sea estable aunque la ejecución siga orden topológico.

## Paquetes afectados

```text
internal/
  planning/
    model.go                 # entidades, estados y errores del plan
    validate.go              # validación, ciclos y topological order
    resolve.go               # referencias desde evidencia confirmada
    executor.go              # ejecución secuencial y agregación
    *_test.go                # tests unitarios y de integración herméticos
  harness/
    protocol.go              # puerto evaluador/contratos mínimos compartidos
    journal.go               # eventos y campos tipados de plan/step
    state.go                 # reservas existentes reutilizadas
  evidence/
    store.go                 # StepID en el origen de evidencia
```

La ubicación exacta del puerto evaluador puede permanecer en `planning` si ese
es su único consumidor. No se modifica `cmd/`, el adapter YAML v1 ni se crea un
paquete `common`.

## Estados y transiciones

Los estados mutables internos pueden incluir `pending` y `running`, pero solo
los terminales forman parte del resultado:

```text
pending -> running -> passed
                   -> failed
                   -> inconclusive
pending -----------> blocked
```

- `pending -> blocked` solo ocurre después de conocer todos los estados de sus
  dependencias directas.
- `running` comienza después de confirmar el evento de inicio del paso y antes
  de policy/tool.
- Cada step recibe exactamente un estado terminal.
- Cancelación o error interno detiene el plan y devuelve un resultado de
  ejecución separado; los steps aún `pending` no se falsifican como
  funcionalmente `blocked`.

## Flujos de error

- **Plan inválido:** error tipado; cero policy calls, tool calls y evidencias.
- **Referencia no resoluble:** step `inconclusive`, evidencia previa conservada
  y dependientes `blocked`.
- **Policy denegada/límite agotado:** step `inconclusive`; herramienta no
  ejecutada.
- **Tool transient:** retry solo si efecto/idempotencia y presupuesto lo
  permiten; cada intento ejecutado persiste evidencia.
- **Tool permanent/retry agotado:** step `inconclusive` con evidencias de
  intentos y causa.
- **Assertion failed:** step `failed`; no se reintenta buscando un resultado
  favorable.
- **Store/journal/evaluator operativo:** error de FeatureProof; se detiene el
  plan y se conserva lo confirmado.
- **Cancelación/deadline:** se propaga la causa, no empieza otro paso y se
  intenta confirmar un evento terminal usando el patrón existente sin ocultar
  un fallo de journal.

## Propiedad del estado y orden determinista

Una instancia de `PlanExecutor` posee los maps de estado, evidence IDs,
presupuesto consumido y resultados durante una ejecución. Las herramientas,
policy, store y evaluador devuelven valores; no mutan el plan ni el run.

El plan original y `ValidatedPlan` son snapshots inmutables para el executor.
Los maps se usan para lookup, nunca para elegir el siguiente step. Tanto el
orden topológico como listas de causas, eventos y resultados se derivan de
declaration order. Así no dependen de la iteración aleatoria de maps de Go.

## Concurrencia y cancelación

La primera versión no crea goroutines y ejecuta como máximo una herramienta o
evaluación a la vez. Toda frontera bloqueable recibe el mismo
`context.Context`. El executor comprueba cancelación antes de seleccionar un
nuevo step y la propaga a policy, tool, waiter, evidence, journal y evaluator.
No almacena contextos dentro de structs de larga vida.

La ejecución secuencial es deliberada: satisface el orden reproducible de la
arquitectura y evita fijar todavía sincronización, reservas concurrentes o
orden de confirmación de evidencias.

## Retries e idempotencia

Se preservan las reglas del harness actual. Solo un error clasificado como
transitorio es candidato; una herramienta con efectos requiere idempotencia o
clave explícita. Retry y tool-call budget se reservan atómicamente, el backoff
es cancelable mediante `Waiter` y cada intento genera eventos/evidencia. La
resolución, validación estructural, policy denied, assertion failed y step
blocked no se reintentan.

El executor no reanuda automáticamente un plan tras reiniciar el proceso. Esa
decisión necesitaría un contrato de idempotencia y recuperación adicional.

## Journal y observabilidad

Se incorporan eventos tipados equivalentes a:

- `plan_validated`, `plan_execution_started`, `plan_completed`;
- `plan_step_started`, `plan_step_input_resolved`;
- eventos existentes de policy, presupuesto, ejecución, retry y evidencia con
  `StepID`;
- `plan_step_completed` con status y evidence IDs;
- `plan_step_blocked` con lista estructurada de `{step_id, status}`.

Cada evento incluye `RunID`, `PlanID`, `StepID`, `ConstraintID` y
`ToolCallID` cuando apliquen. La secuencia del journal define el orden real. No
se registra el input/output sin sanear ni se codifican causas en strings que un
consumidor deba parsear.

Para reconstruir se leen eventos por `Sequence`: `step_started` determina
selección, eventos de herramienta prueban ejecución, `step_completed` aporta
estado/evidencia y `step_blocked` prueba omisión y causa. Un plan válido termina
con un único `plan_completed` que contiene su status agregado.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro de FeatureProof y
su relación con el flujo o frontera correspondiente. El comentario expresará
intención, invariantes o motivo arquitectónico, sin limitarse a repetir nombre,
parámetros o sintaxis. Esta convención procede de
`specs/validation-specification/design.md` y se aplica también a DTOs, fakes y
auxiliares de grafo/resolución.

## Estrategia de pruebas

Los tests usan constraints internas, registry/policy/tool/evaluator/journal y
evidence store falsos o temporales. No usan red ni modelo. No coordinan mediante
`time.Sleep`; cancelación y waits son controlables.

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | modelos/validator | `TestValidatePlanRequiresUniqueStepsAndKnownConstraint`; `TestPlanIDsRemainCaseSensitive` |
| R2 | validator/registry | `TestValidatePlanRequiresStructuredStepFields`; `TestInvalidPlanNeverReachesPolicyOrTools` |
| R3 | reference validator/resolver | `TestResolveTypedReferencesFromPersistedAncestorEvidence`; `TestRejectInvalidOrNonAncestorReferences`; `TestResolutionRejectsForeignEvidenceMissingPathAndWrongType` |
| R4 | graph validator | `TestRejectUnknownDuplicateAndSelfDependencies`; `TestRejectCyclesDeterministicallyBeforeExecution` |
| R5 | topological order/executor | `TestExecutePlanUsesStableTopologicalDeclarationOrder`; `TestEveryStepPassesControlsBeforeToolExecution` |
| R6 | evaluator/aggregator | `TestStepAndPlanResultsAreDerivedDeterministically`; `TestPlanCannotPassWithoutPassedEvidence` |
| R7 | executor | `TestFailedAndInconclusiveDependenciesBlockDescendants`; `TestIndependentBranchesContinueAndBlockingCausesAreStable` |
| R8 | journal | `TestJournalReconstructsExecutedAndBlockedStepsInOrder`; `TestBlockedStepHasNoToolStartEvent` |
| R9 | executor/retry ports | `TestPlanCancellationPreservesConfirmedWorkAndStopsSelection`; `TestPlanRetriesOnlySafeTransientOperationsWithinBudget`; `TestPlanSuiteUsesOnlyControlledLocalDependencies` |

Además se prueban plan vacío, slices nil, IDs duplicados, assertion duplicada,
referencias anidadas en arrays/maps, índice fuera de rango, evidencia de otro
run/step, múltiples intentos, budget agotado, policy denied, error interno de
store/journal/evaluator y repetición del mismo plan para detectar orden no
determinista. La suite completa se ejecuta con race detector.

## Alternativas y decisiones

- **Ejecutar el grafo en paralelo:** descartado en esta iteración; complicaría
  presupuestos y orden observable sin ser necesario para los acceptance
  criteria.
- **Usar strings `${steps.a.output.x}`:** descartado; mezcla literales y
  referencias, dificulta tipos y admite sustituciones ambiguas.
- **Inferir dependencias desde referencias:** descartado; ocultaría aristas. La
  referencia debe apuntar al cierre de una dependencia declarada.
- **Resolver desde resultados en memoria:** descartado; ARCH-003 exige evidencia
  confirmada para confiar en outputs.
- **Extender YAML v1:** descartado porque rompería la compatibilidad ya fijada;
  una versión externa futura requiere su propia spec.
- **Implementar assertions mínimas otra vez:** descartado. El executor usa un
  puerto para que feature 4 sea propietaria de la semántica.
- **Modificar el harness actual para que el modelo elija cada siguiente paso:**
  descartado; debilita el orden determinista y mezcla planificación con
  ejecución.
- **Bloquear todo el plan tras el primer resultado no favorable:** descartado;
  ramas independientes aportan evidencia útil y los criterios solo bloquean
  dependientes.

## Conformidad arquitectónica

El diseño preserva ARCH-001/002/011 mediante registry, schema y policy; ARCH-003
y ARCH-004 mediante evidencia y evaluador externo al modelo; ARCH-005 mediante
presupuestos; ARCH-006 mediante contextos; ARCH-007 mediante eventos append-only;
ARCH-009 mediante saneado previo; ARCH-010 mediante fakes; y ARCH-012 porque
solo el executor muta estado. Respeta CONV-001 a CONV-009 y no solicita
excepciones.

## Mapa R → componente/prueba

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T6 | El plan exige steps únicos vinculados a una constraint real común. |
| R2 | T1, T2, T6 | Cada step contiene herramienta, input, assertions y dependencies válidos antes de efectos. |
| R3 | T1, T3, T4, T6 | Solo se resuelven refs tipadas de ancestros mediante evidencia confirmada. |
| R4 | T2, T6 | Duplicados, desconocidos y ciclos rechazan todo el plan de forma estable. |
| R5 | T2, T4, T6 | La ejecución secuencial respeta topología, declaration order y controles. |
| R6 | T1, T4, T6 | Steps y plan obtienen estados deterministas sin claims de modelo. |
| R7 | T4, T6 | Dependientes no se ejecutan y conservan todas las causas directas. |
| R8 | T5, T6 | El journal reconstruye selección, ejecución, evidencia, omisión y resultado. |
| R9 | T4, T5, T6 | Cancelación/retries respetan contratos y los tests son herméticos. |
