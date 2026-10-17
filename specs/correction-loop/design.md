# Bucle verificable de corrección — Design

## Resumen de la solución

Se añadirá un paquete de aplicación `internal/correction` que coordina una
cadena lineal de runs sin poseer Git, entornos, modelos, herramientas ni
paquetes. El coordinador verifica el paquete padre, reserva un hijo y su
presupuesto, exige un binding nuevo y una autorización de continuación cuando
corresponda, y delega la validación en un ejecutor que conserva todas las
puertas actuales. Un plan de ejecución ordena primero los fallos aplicables y
después la regresión requerida. Al terminar, un comparator puro lee dos
`PackageIndex` verificados y publica una comparación segura.

```text
correction_submit(parent handle, authorized alias, key)
       │
       ├─ identity/scope + verified parent package + chain CAS/budgets
       │
       ├─ new revision preflight ──same repo/environment──> new child binding
       │                               │
       ├─ derive parent failures ── applicability ── regression snapshot
       │                               │
       ├─ optional continuation approval (one use)
       │                               │
       └─ child executor
              ├─ failed_first through ordinary validation pipeline
              ├─ required_regression through ordinary validation pipeline
              └─ atomic evidence package
                                      │
                      verified parent + verified child
                                      │
                      deterministic comparison artifact
```

La composición productiva seguirá usando un ejecutor fail-closed mientras no
exista el provider confiable de previews de la feature 19. La feature entrega
el caso de uso, persistencia, adapters y pruebas positivas con fakes; no crea
una atestación ficticia.

## Fuentes de contexto consultadas

| Fuente | Contrato retenido |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md` | Una feature SDD, tres artefactos, aprobación humana antes de código y trazabilidad `R→T→test`. |
| `docs/architecture.md` | El agente propone; sistema, policy y budgets autorizan; assertions deterministas deciden; journal y evidencia permiten auditoría. |
| `docs/conventions.md`, `docs/verification.md` | Interfaces en consumidor, contexto, CAS, reloj/fakes, race, 85% de coverage y comentarios sistémicos. |
| Feature 12, `internal/planapproval` | Autorizaciones opacas de un uso, snapshots y hashes, revisión CAS e invalidación ante cambios. |
| Feature 13, `internal/replanning` | Presupuestos no monetarios, ledger monotónico, fingerprints, pipeline completo y lineage append-only. |
| Feature 14, `internal/revisionbinding` | Commit completo, repositorio/entorno atestados, binding inmutable, nuevo run por revisión y fallo cerrado sin provider. |
| Feature 15, `internal/localvalidation` | `ReadPackage` como única entrada confiable; package v1 separa resultados, failures, facts, evidence y reproduction, con publicación atómica. |
| Feature 16, `internal/externalagent` | Principal/scopes confiables, aliases, handles opacos, idempotencia durable, supervisor y paridad CLI/MCP. |

No se incorpora contrato externo nuevo. MCP conserva el protocolo stdio
`2026-07-28`; cambia de forma explícita la versión del contrato de aplicación
al añadir operaciones y DTOs.

## Reglas arquitectónicas aplicadas

- El modelo no elige qué fallo padre se revalida, no clasifica la comparación
  y no ejecuta la corrección.
- Solo un paquete que pasó `localvalidation.ReadPackage` puede alimentar
  selección o comparación.
- Cada hijo tiene revision binding y evidencias propios. Solo se deduplica
  trabajo dentro del hijo, nunca entre revisiones.
- Repository, environment, aliases, política de regresión, límites y requisito
  de aprobación proceden de configuración confiable y no de MCP/CLI.
- La clasificación de cambio es determinista y separada del veredicto actual.
- Estado durable antes de trabajo, CAS por cadena, callbacks fuera del lock,
  cancelación propagada y recuperación sin ejecución automática.
- Toda entidad y función nueva o modificada, incluidas helpers de test, tendrá
  un comentario que explique su responsabilidad dentro del sistema.

## Componentes y responsabilidades

### `CorrectionService`

Caso de uso de entrada. Resuelve principal y scopes, reautoriza el padre y el
alias, valida inputs, usa `PackageReader`, reserva idempotencia y entrega al
`Coordinator` una selección confiable. `Status`, `Result` y `Cancel` vuelven a
autorizar el correction handle. No acepta paths ni objetos internos del run.

### `Coordinator`

Único propietario de la transición de un intento. Reserva presupuesto,
ejecuta preflight, deriva el conjunto motivador, pide aplicabilidad y política
de regresión, consume autorización cuando se exige, llama al child executor,
verifica el paquete nuevo, compara, publica y finaliza una vez. No crea
goroutines ocultas; el supervisor externo posee el worker y hace join.

### `CorrectionLink`

Snapshot versionado e inmutable con `correction_id`, handle interno, principal,
`parent_run_id`, `parent_binding_sha256`, `root_run_id`, `attempt`, alias y hash
de selección, policy digest, limits digest, child run/binding cuando estén
confirmados y timestamps controlados. Su digest enlaza el journal de
corrección con ambos paquetes, sin modificarlos.

### `FailureSelector` y `ApplicabilityChecker`

`FailureSelector` recorre `PackageIndex.Failures`, exige referencias internas
válidas ya comprobadas por el reader, construye `ScenarioIdentity` y elimina
duplicados en orden canónico. `ApplicabilityChecker` recibe el snapshot
validado de specification/contract/catalog del hijo y devuelve exclusivamente
`applicable` o `not_applicable` con una causa cerrada (`constraint_removed`,
`operation_removed`, `capability_removed`, `assertion_changed`,
`schema_incompatible`). No interpreta texto del modelo.

### `RegressionPolicy`

Frontera confiable que resuelve por alias un `RegressionSnapshot` versionado:
escenarios requeridos, reglas de deduplicación, requirement de aprobación y
límites máximos. El snapshot se valida, canoniza y hashea antes de reservar el
hijo. El agente no puede editarlo. Ausencia o cambio tras la reserva causa
fallo cerrado/stale.

### `ContinuationGate`

Frontera pequeña de aprobación humana para la continuación de la cadena.
Stage recibe parent/child selection, commit resuelto, conjunto motivador,
policy y budget snapshot saneados. Consume una autorización opaca de un uso
ligada a esos hashes y al ordinal. Puede adaptarse al almacén/patrones de
`planapproval`, pero usa tipos propios para no presentar una corrección como si
fuera un plan ya generado. La interfaz concreta de usuario queda fuera; en
producción una política que exige aprobación sin gate configurado falla
cerrada. Cada plan producido dentro del hijo sigue además su aprobación normal.

### `ChildExecutor`

Interfaz consumida por `correction`. Recibe un `ChildRequest` inmutable con
binding, escenarios por fase, policy snapshot, budgets y autorización. Debe
atravesar planning, schema/semantic validation, registry, policy, runtime
budgets, plan approval, deterministic executor, evidence/assertions y
publisher existentes. Devuelve IDs internos, package locator no público,
outcome operativo/funcional y uso confirmado de modelo. La implementación
productiva inicial es fail-closed; fakes prueban la coordinación.

### `Comparator`

Función pura y acotada. Recibe parent/child `PackageIndex`, la selección de
aplicabilidad y el link confirmado. Une por `ScenarioIdentity`, aplica la tabla
cerrada de clasificación, conserva evidence IDs con `origin=parent|child` y
ordena por IDs. Rechaza ambigüedad, foreign binding o estado child no apto.
Nunca toma el veredicto del modelo ni calcula el veredicto del hijo.

### `CorrectionStore`, journal y projector

Store append-only con compare-and-append por cadena, índice de idempotencia por
principal/operación y snapshot derivado. El projector estricto comprueba
versión, secuencia, transiciones, un único tail, ordinal, hashes y parent/child.
Una implementación filesystem reutiliza las garantías de lock multiproceso,
append confirmado, snapshot atómico y recuperación de feature 16 sin compartir
sus tipos privados.

### Adapters CLI y MCP

Amplían la interfaz externa mediante el mismo `CorrectionService`. CLI ofrece
`correction submit|status|result|cancel --json`; MCP anuncia y llama
`featureproof.correction_submit`, `featureproof.correction_status`,
`featureproof.correction_result` y `featureproof.correction_cancel`. Ambos
serializan exactamente el mismo envelope de aplicación.

## Paquetes afectados

```text
internal/
  correction/                 # nuevo caso de uso, modelos, journal y comparator
    model.go
    canonical.go
    selector.go
    comparator.go
    coordinator.go
    store.go
    local_store.go
    service.go
    *_test.go
  externalagent/              # adapters/descubrimiento para el contrato v2
  revisionbinding/            # solo adapters mínimos si falta proyección segura
  localvalidation/            # reader reutilizado; no se debilita package v1
cmd/featureproof/              # routing de comandos correction y MCP existente
```

El implementer puede reagrupar archivos dentro de `internal/correction` si las
responsabilidades y dirección de dependencias se conservan. No se fuerza una
modificación del formato package v1: el `CorrectionLink` y comparison artifact
pertenecen al journal de corrección y referencian paquetes inmutables por run y
binding digest.

## Contratos de datos

### Identidad de escenario

`ScenarioIdentity/v1` contiene strings limitados de `constraint_id`,
`scenario`, `batch_id`, `check_id`, `step_id`, `declaration_id` y una huella de
la semántica relevante. `unspecified` se conserva como valor explícito cuando
el paquete v1 no dispone de una categoría, pero step/declaration deben hacer la
identidad no ambigua. El orden canónico sigue esos campos en ese orden.

### Plan de fases

`CorrectionPlan/v1` contiene:

```text
failed_first[]: scenario identity + source failure reference
not_applicable[]: scenario identity + closed reason
required_regression[]: scenario identity + policy reference
```

Antes de la segunda fase, el coordinator construye un set de fingerprints
confirmados del hijo. Una entrada de regresión con el mismo fingerprint queda
`covered_by_failed_first`; cualquier diferencia semántica permanece pendiente.

### Contrato de aplicación v2

Las operaciones v1 conservan sus DTOs. Se agregan:

| Operación | Entrada | Salida |
|---|---|---|
| `correction_submit` | `{parent_run_handle, selection_alias, idempotency_key}` | `{correction_handle, parent_run_handle, child_run_handle, phase, updated_at}` |
| `correction_status` | `{correction_handle}` | mismo lifecycle, estado terminal opcional y categoría operativa segura |
| `correction_result` | `{correction_handle}` | link seguro, outcome del hijo y arrays `corrected`, `persistent`, `new`, `not_applicable`, `inconclusive` |
| `correction_cancel` | `{correction_handle, idempotency_key}` | lifecycle terminal o vigente |

`phase` usa `reserved|preflight|awaiting_approval|failed_first|regression|finalizing|terminal`.
El status terminal usa el vocabulario existente. `correction_result` antes de
terminal devuelve `in_progress`; un terminal operativo devuelve status y
categoría, sin comparación fabricada. Errores públicos mantienen
`invalid_request|unauthorized|not_found|conflict|in_progress|internal_error`;
`limit_exhausted` y `revision_not_deployed` son `terminal_reason` allowlisted,
no texto externo. Los esquemas son cerrados y acotados.

El MCP continúa en `2026-07-28`. `server/discover` anuncia application contract
v2 y `tools/list` filtra las cuatro herramientas nuevas por scopes
`corrections:create`, `corrections:read`, `corrections:cancel`. Los scopes v1
siguen aplicándose al parent/child run; poseer un handle no autoriza acceso.

## Estado y transiciones

```text
reserved
   ├─ preflight ──> awaiting_approval ──> failed_first ──> regression
   │                    │ reject/cancel                        │
   │                    └──────────────────────────────> terminal
   ├─ invalid/limit/infrastructure ────────────────────> terminal
   └─ cancel ─────────────────────────────────────────> terminal

regression ──> finalizing ──> terminal
```

- `reserved` confirma idempotencia, ordinal y consumo de intento antes de
  trabajo externo; un replay no consume otra unidad.
- El preflight confirma el child run/binding y conjunto/policy hashes.
- Si aprobación no se exige, la transición omite `awaiting_approval` mediante
  un evento explícito `continuation_approval_not_required`.
- `failed_first` debe terminar antes de `regression`; sus fingerprints
  confirmados determinan deduplicación dentro del hijo.
- Solo `finalizing` puede verificar ambos paquetes, escribir la comparación y
  confirmar terminal.
- Los estados terminales no se reabren. Infraestructura resuelta requiere una
  nueva operación explícita bajo el tail funcional permitido y sus límites.

## Eventos y propiedad del estado

Eventos v1 previstos: `correction_reserved`, `preflight_confirmed`,
`scenario_applicability_confirmed`, `continuation_approval_requested`,
`continuation_approval_resolved`, `phase_started`, `scenario_confirmed`,
`regression_covered`, `child_terminal`, `comparison_published`,
`cancellation_requested`, `correction_terminal`, `owner_interrupted`.

Los eventos contienen IDs, digests, ordinales, contadores y categorías
cerradas. No contienen package paths, URLs, values, prompts, respuestas,
credenciales ni razones externas. El store posee el journal; el worker posee
la ejecución mientras mantiene un lease renovable. Package store y child
executor poseen sus recursos. No se copian structs con mutex ni se mantienen
locks durante callbacks.

## Flujo principal

1. Resolver principal y scopes; validar handles, alias e idempotency key.
2. Reautorizar/read parent record, cargar su paquete mediante `ReadPackage`,
   exigir terminal `failed` y comprobar que es tail elegible.
3. Resolver policy/limits confiables, canonizar request y reservar una vez
   correction handle, ordinal, attempt budget y worker ownership.
4. Resolver/atestar revisión nueva; exigir mismo repository/environment
   autorizado, commit distinto y vigente; sellar child binding.
5. Derivar todos los failed scenarios, comprobar aplicabilidad y formar el plan
   de fases con regresión completa.
6. Si policy lo exige, stage/esperar/consumir autorización exacta; si cambia un
   hash, marcar stale y no ejecutar.
7. Ejecutar `failed_first` a través del pipeline ordinario. Confirmar outcomes,
   evidencias y fingerprints actuales en journal.
8. Construir pendientes de regresión, acreditar solo duplicates idénticos con
   evidencia del hijo y ejecutar el resto por el mismo pipeline.
9. Publicar el paquete hijo atómicamente y leerlo de nuevo mediante
   `ReadPackage`. Sumar uso confirmado de modelo a la cadena.
10. Comparar ambos índices, publicar comparison artifact no-replace, confirmar
    terminal y proyectar el DTO seguro.

## Flujos de error

- **Parent no elegible/ajeno:** `not_found` público; no reserva ni revela
  status real.
- **Idempotencia conflictiva/tail movido:** `conflict`; no crea rama.
- **Same commit/ref inválido/dirty/repository incompatible:** terminal operativo
  seguro; cero model/tool calls.
- **Revision no desplegada/atestación inválida:** `revision_not_deployed` o
  categoría de binding, sin comparación funcional.
- **Policy o limits ausentes/cambiados:** error/stale antes de ejecución.
- **Aprobación rechazada:** terminal `cancelled`; aprobación stale vuelve a
  espera o termina según policy, nunca cruza al executor.
- **Denegación de plan/policy o evidencia insuficiente:** child
  `inconclusive`; comparison solo se publica si el package actual es íntegro y
  permite clasificación completa.
- **Infraestructura durante ejecución/publicación:** `error` o
  `inconclusive` tipado; eventos confirmados permanecen, comparison final
  ausente y no se crean fallos funcionales.
- **Paquete corrupto/foreign:** integrity failure; no se devuelve vista parcial.

## Límites, idempotencia y concurrencia

`ChainBudget` guarda máximos y consumo de attempts, elapsed duration,
model_calls y model_tokens. Los límites per-run/model session existentes se
aplican además; el efectivo es el más restrictivo. Attempt se consume al
confirmar `correction_reserved` y no se reintegra por rechazo, cancelación o
infraestructura. Calls/tokens se suman solo desde usage confirmado, y un
overflow es integridad fallida. El deadline absoluto confirmado sobrevive al
reinicio.

La idempotencia cubre el request canónico y cada transición del coordinator.
Misma key/request reconstruye el handle y resultado; misma key/diferente hash
es conflicto. Chain CAS serializa la creación del hijo y los cambios de fase.
Runs/cadenas diferentes usan particiones y avanzan concurrentemente. El
coordinador v1 ejecuta escenarios secuencialmente para demostrar failed-first;
una paralelización futura requerirá conservar barrera de fase y orden de
journal.

## Cancelación y recuperación

`context.Context` cruza package reader, store, revision/attestation, approval,
executor y publisher. El supervisor posee `cancel` y `join`; cierre de CLI/MCP
cancela sus workers. Cancelar confirma la solicitud, señala al child, espera
salida con plazo y finaliza una vez. Solo la finalización durable usa un
contexto independiente y acotado después de una cancelación. Una lease perdida
se recupera como `owner_interrupted`, sin rerun ni hijo nuevo automático.

## Observabilidad y seguridad

Logs estructurados pueden contener correction handle, handles autorizados,
ordinal, fase, status, terminal reason, digests y contadores. No son fuente de
estado. Nunca incluyen aliases resueltos a paths/URLs, commit recibido libre,
attestation payload, policy content, evidence values, prompts, provider
payloads, idempotency key ni secrets. Comparisons exponen únicamente IDs
allowlisted, estados cerrados y evidence handles reautorizables.

## Estrategia de pruebas

| Requirement | Componentes | Tests previstos |
|---|---|---|
| R1 | service/store | `TestCorrectionSubmitRequiresOwnedFailedParent`; `TestCorrectionSubmitIsDurablyIdempotent` |
| R2 | link/projector/CAS | `TestCorrectionChainCreatesImmutableLinearChildren`; `TestConcurrentTailAdmissionHasOneWinner` |
| R3 | preflight/binding | `TestCorrectionRequiresDistinctCompatibleDeployedRevision`; `TestRevisionNotDeployedStopsBeforeFunctionalWork` |
| R4 | selector/applicability | `TestFailureSelectionIsCompleteCanonicalAndUnmodifiable`; `TestObsoleteScenarioIsNotApplicable` |
| R5 | coordinator/executor | `TestFailedScenariosRunBeforeRequiredRegression`; `TestRegressionRechecksPriorPassAndDeduplicatesOnlyChildEvidence` |
| R6 | comparator/reader | `TestComparisonClassifiesCorrectedPersistentNewAndInconclusive`; `TestComparisonRejectsCorruptOrForeignPackage` |
| R7 | child aggregation | `TestChildVerdictUsesOnlyCurrentEvidence`; `TestNewRegressionKeepsCorrectedChildFailed` |
| R8 | budget/gate | `TestChainLimitsStopBeforeNewWork`; `TestContinuationApprovalIsBoundOneUseAndStaleSafe` |
| R9 | operational mapping | `TestInfrastructureFailureNeverBecomesFunctionalFailure`; `TestUndeployedRevisionHasNoComparison` |
| R10 | supervisor/store | `TestCorrectionCancelJoinsAndPreservesParent`; `TestInterruptedOwnerDoesNotRerun`; `TestIndependentChainsProgressConcurrently` |
| R11 | CLI/MCP/projection | `TestCorrectionCLIAndMCPContractParity`; `TestCorrectionScopesAndSecretProjection` |
| R12 | suite/comments | `TestCorrectionFullFlowHermetic`; canonical verification, race, coverage and source review |

Los tests usan clocks e IDs controlados, filesystem temporal y fakes seguros
para concurrencia. No usan `time.Sleep`, red, Git remoto, modelo real ni preview
real. La suite contractual ejecuta las mismas fixtures por CLI y MCP. Se
ejecutan todos los gates de `docs/verification.md` y tests race repetidos sobre
admisión/cancelación.

## Alternativas y decisiones

- **Mutar el run padre:** descartado; rompe revision binding, paquete e
  historial auditable.
- **Aceptar commit/URL/target desde MCP:** descartado; sortea aliases y amplía
  autoridad del agente.
- **Revalidar solo fallos:** descartado; no detecta regresiones nuevas.
- **Ejecutar regresión antes o en paralelo:** descartado en v1; impide la
  garantía observable failed-first.
- **Volver a ejecutar en regresión un escenario idéntico ya probado en el
  hijo:** descartado; evidencia actual con misma huella ya lo cubre y evita un
  efecto duplicado. No se reutiliza evidencia del padre.
- **Comparar texto o posición:** descartado; los IDs estructurales y fingerprints
  son estables y verificables.
- **Permitir ramas desde un padre:** descartado en v1; una cadena lineal ofrece
  límites, ordinales y aprobación no ambiguos.
- **Integrar ya feature 19:** descartado; la composición real permanece
  fail-closed hasta disponer del adapter aprobado.

## Mapa inicial de trazabilidad

| Requirement | Tasks | Evidencia de diseño |
|---|---|---|
| R1 | T1, T2, T7 | Identidad/scopes, parent reader e idempotencia durable. |
| R2 | T1, T2, T6 | CorrectionLink, projector y CAS lineal. |
| R3 | T3 | Revision preflight y binding nuevo fail-closed. |
| R4 | T3 | Selector completo y checker determinista. |
| R5 | T4 | Barrera failed-first y regresión completa actual. |
| R6 | T5 | Comparator sobre dos paquetes verificados. |
| R7 | T4, T5 | Veredicto child separado de comparación. |
| R8 | T2, T3, T4 | ChainBudget y ContinuationGate de un uso. |
| R9 | T3, T4, T5 | Outcome operativo separado del funcional. |
| R10 | T2, T4, T6 | CAS, supervisor, cancelación y recovery. |
| R11 | T7 | Contrato v2 compartido, scopes y proyección segura. |
| R12 | T1–T8 | Fakes, gates, race, coverage y comentarios. |
