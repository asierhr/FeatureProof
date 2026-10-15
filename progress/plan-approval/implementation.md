# Implementación — plan-approval

## Resultado

Se implementó `internal/planapproval` como frontera de aplicación entre un
`agentplanning.PreparedPlan` ya validado y `planning`. El workflow registra
`awaiting_approval`, publica una vista completa saneada, liga la decisión a
hashes canónicos del plan/contexto, resuelve decisiones mediante CAS y entrega
un grant opaco consumible una sola vez. Rechazo, regeneración, edición y
snapshots obsoletos vuelven a estados cerrados sin autorización.

El journal local confirma eventos JSONL con `fsync`; las solicitudes de
recuperación contienen exclusivamente vista saneada y fingerprints, además de
un hash de integridad de esa vista. Los handles compuestos sobre la misma raíz
comparten particiones por run dentro del monolito para conservar CAS. El plan
ejecutable nunca se serializa: tras recuperación, la composición debe aportar
de nuevo el `PreparedPlan` y el contexto actuales, que se vuelven a hashear.

También se añadió `harness.StatusAwaitingApproval` con sus únicas salidas
permitidas, y `evidence.SanitizedValue.Value` para que el adapter de aprobación
reutilice la política de redacción existente mediante una copia aislada.

## Tasks

- T1: estados, acciones, eventos, errores, proyección estricta y límites.
- T2: snapshots aislados y fingerprints SHA-256 canónicos versionados.
- T3: staging fail-closed de `PreparedPlan` con CAS.
- T4: vista completa, estable, clonada y saneada mediante `evidence`.
- T5: decisiones, invalidaciones, idempotencia y stores memoria/local.
- T6: autorización opaca y consumo persistido una sola vez.
- T7: puerta hacia `planning` y ruta explícita con aprobación deshabilitada.
- T8: suite hermética, concurrencia, recuperación, corrupción y fallos.
- T9: verificación canónica y trazabilidad.

## Trazabilidad

| Requirement | Tests reales |
|---|---|
| R1 | `TestStageEntersAwaitingWithoutExecution`, `TestStageRejectsUnpreparedOrIncompleteSnapshot`, `TestApprovalDisabledPreservesDirectExecution`, `TestApprovalTransitionsPermitOnlyTheApprovedLifecycleEdges` |
| R2 | `TestApprovalViewIsCompleteStableAndSecretFree`, `TestSanitizerFailurePublishesNoRequest`, `TestEvidenceSanitizerReusesConfiguredRedaction`, `TestViewProjectionFailureBranches` |
| R3 | `TestDecisionRequiresClosedActionTrustedIdentityAndExpectedRevision`, `TestDecisionConflictsDoNotAlterConfirmedProjection` |
| R4 | `TestApproveRecordsIdentityHashesAndReturnsBoundGrant`, `TestApprovalAppendFailureReturnsNoGrant`, `TestGrantBindingAndMismatchKeepRuntimeClosed` |
| R5 | `TestRejectTerminalCancelsAndRejectReplanReturnsToPlanning` |
| R6 | `TestEditAndRegenerationInvalidateAndRequireNewRevision`, subtest `edit_constraints` |
| R7 | `TestEditAndRegenerationInvalidateAndRequireNewRevision`, subtest `regenerate`; `TestIdempotentInvalidationRetriesReturnTypedWork` |
| R8 | `TestEveryRelevantContextChangeInvalidatesApproval`, `TestChangedPreparedPlanInvalidatesApproval`, `TestCanonicalHashesIgnoreMapOrder` |
| R9 | `TestExecutionRequiresMatchingOneUseAuthorization`, `TestGateRuntimeFailuresRemainAfterAuthorization`, `TestExecutionCancellationStopsRemainingApprovedPlans` |
| R10 | `TestConcurrentDecisionsHaveExactlyOneWinner`, `TestIndependentRunsProgressConcurrently`, `TestLocalStoreHandlesSharePerRunCAS`, `TestMemoryStoreConflictsAndCancellation` |
| R11 | `TestCorruptJournalFailsClosed`, `TestCancellationPreservesConfirmedPrefix`, `TestLocalStoreRecoversStrictEvents`, `TestProjectAdditionalIntegrityBranches`, `TestLocalStoreSnapshotAndJournalWriteFailuresRemainUnconfirmed` |
| R12 | Todos los tests anteriores; suite completa, race y coverage indicados debajo. |

## Verificación ejecutada

Go usado: `go1.25.1 windows/amd64`.

- `gofmt -d` sobre todos los `.go` modificados: pass.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con CGO y
  `C:\msys64\ucrt64\bin\gcc.exe`.
- `go test -race -count=20 ./internal/planapproval ./internal/harness ./internal/evidence`: pass.
- `go test -count=1 -coverprofile=coverage.out ./...` y
  `go tool cover -func=coverage.out`: pass.
- `go mod verify`: pass.
- `git diff --check`: pass; solo avisos de conversión LF/CRLF del checkout.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`: pass.
- `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...`: pass.

Coverage reproducible:

- global: **91,4 %** (baseline aprobado previo: 91,1 %);
- `internal/planapproval`: **94,3 %**;
- `internal/evidence`: **91,0 %**;
- `internal/harness`: **88,0 %**.

## Seguridad y alcance

- No se ejecutó red, OAuth, credenciales, un target real ni herramientas de
  producción.
- No se añadió una CLI interactiva.
- Los eventos contienen IDs internos, enums, timestamps y hashes; no contienen
  plan/contexto crudos ni valores saneados eliminados.
- La vista recuperada se autentica con su hash antes de presentarse o decidirse.
- Registry, policy, budgets, sanitizer y cancelación permanecen detrás del
  gate en el executor existente.

## Estado solicitado

T1-T9 están completas. La feature permanece `in_progress`; el implementer no
modificó `feature_list.json` ni creó `review.md`. Se solicita revisión
independiente.

## Correcciones posteriores a la primera revisión

- REV-1: `ExecutionComposition` fija `ApprovalRequired` o `ApprovalDisabled` al componer el `harness.Run`, el `ExecutionGate` y el `planning.PlanExecutor`; `Execute` ya no acepta un booleano del proveedor del plan. `Stage` y `Decide` reciben y transicionan el aggregate real. `TestExecutionCompositionGatesRealPlanExecutor` demuestra cero policy, presupuesto y tool antes del grant, un único `plan_execution_start` después del consumo, y el flujo disabled explícito con el executor real.
- REV-2: feature 10 sella en `PreparedPlan` una provenance privada de constraints, OpenAPI reducido y capabilities sin callbacks. `Stage` vuelve a derivarla desde el contrato y falla sin evento ante un plan anterior. `TestStageRejectsOldPlanAfterConstraintEdit` cubre rechazo y el caso positivo después de regenerar; `TestPreparedPlanProvenanceBindsGenerationInputs` cubre constraints, OpenAPI, capabilities y construcción externa vacía.
- REV-3: `LocalStore.ensureLoaded` decodifica y proyecta en un buffer temporal y publica el slice solo después de validar el stream completo. `TestLocalStoreRecoveryPublishesOnlyCompleteStreams` y `TestLocalStoreCancelledRecoveryLeavesNoPartialPrefix` prueban fallo tras un prefijo y retry sin duplicados.
- REV-4: el append local serializa el prefijo candidato completo en un temporal, comprueba short write, sincroniza y cierra antes de publicar, sincroniza metadata y mantiene backup recuperable durante el reemplazo. `TestLocalStoreAtomicPublicationSurvivesPartialFileFailures` inyecta create, short write, file sync, close, stat, ambos rename y directory sync; `TestLocalStoreRecoversInterruptedBackupPublication` cubre recuperación tras interrupción.
- REV-5: `Project` valida el schema cerrado v1 por tipo, incluidos event/run/plan/decision IDs, timestamp, hashes, `view_hash`, actor subject/source y campos requeridos/prohibidos. `TestProjectRejectsEveryMissingCommonAuditField` y `TestProjectEnforcesClosedPerTypeSchemas` eliminan o introducen cada campo relevante por tabla.

## Trazabilidad actualizada

| Requirement | Tests adicionales de regresión |
|---|---|
| R1 | `TestExecutionCompositionGatesRealPlanExecutor` |
| R3 | `TestProjectRejectsEveryMissingCommonAuditField`, `TestProjectEnforcesClosedPerTypeSchemas` |
| R4 | `TestExecutionCompositionGatesRealPlanExecutor`, schema cerrado de decisiones |
| R6 | `TestStageRejectsOldPlanAfterConstraintEdit`, `TestPreparedPlanProvenanceBindsGenerationInputs` |
| R9 | `TestExecutionCompositionGatesRealPlanExecutor`, `TestExecutionCompositionRejectsInvalidFixedConfiguration` |
| R10 | `TestLocalStoreAtomicPublicationSurvivesPartialFileFailures` |
| R11 | `TestLocalStoreRecoveryPublishesOnlyCompleteStreams`, `TestLocalStoreCancelledRecoveryLeavesNoPartialPrefix`, `TestLocalStoreRecoversInterruptedBackupPublication`, tablas de schema |
| R12 | Todos los tests anteriores y verificación canónica fresca posterior a las correcciones. |

## Verificación fresca posterior a REV-1–REV-5

- `gofmt -l .`: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con CGO y GCC UCRT64.
- `go test -race -count=20 ./internal/planapproval ./internal/agentplanning ./internal/harness ./internal/evidence`: pass.
- Coverage reproducible: 91,2 % global, 92,8 % `internal/planapproval`, 90,8 % `internal/agentplanning`, 91,0 % `internal/evidence`, 88,0 % `internal/harness`. El baseline global previo a la feature (91,1 %) se conserva.
- `go mod verify`: pass.
- `git diff --check`: pass; únicamente avisos esperados de conversión LF/CRLF.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`: pass.
- `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...`: pass.

No se utilizó red, OAuth, credenciales, targets reales ni herramientas de producción. T1–T9 permanecen `[x]`; `feature_list.json` y `progress/plan-approval/review.md` no fueron modificados por el implementer durante estas correcciones.

## Correcciones posteriores a la segunda revisión

- REV-6: el store posee ahora un `runLifecycle` único por run ID y liga de forma irreversible la identidad del `*harness.Run`; intentar componer otro aggregate con el mismo ID falla. `ApprovalWorkflow` recibe el aggregate una sola vez en composición, los comandos ya no aceptan punteros de run y Stage/Decide serializan lectura del prefijo, CAS y transición bajo el mismo propietario compartido incluso entre handles de `LocalStore`. `harness.Run.status` pasó a ser privado y `Status`/`Transition` usan RWMutex, eliminando lecturas/escrituras públicas con race. `ExecutionComposition` reutiliza el lifecycle del workflow y comprueba que el `PlanExecutor` esté ligado al mismo aggregate mediante `IsBoundTo`.
- REV-6 queda cubierto directamente por `TestSharedAggregateConcurrentDecisionsRemainJournalAligned` para approve/approve, approve/reject y reject/regenerate sobre exactamente el mismo aggregate, `TestLifecycleOwnerRejectsAggregateSubstitution` y `TestSharedAggregateFailureAndReplayStayJournalAligned`. Todos comparan `run.Status()` con `Project` tras el prefijo confirmado; la suite race completa y race ×20 pasan.
- REV-7: `validateEventSchema` acepta únicamente SHA-256 hex minúsculo de 64 caracteres en `plan_hash`, `context_hash` y `view_hash`, y exige `OccurredAt` normalizado con `time.UTC`. Los fixtures válidos usan hashes contractuales. `TestProjectRejectsNonCanonicalHashAndTimestampFormats` cubre longitud, caracteres, mayúsculas para cada hash y una zona horaria no UTC.

## Verificación fresca posterior a REV-6–REV-7

- `gofmt -l .`: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con CGO y GCC UCRT64.
- `go test -race -count=20 ./internal/planapproval ./internal/agentplanning ./internal/harness ./internal/evidence ./internal/planning`: pass.
- Coverage reproducible: 91,3 % global, 93,1 % `internal/planapproval`, 90,8 % `internal/agentplanning`, 88,3 % `internal/harness`, 89,3 % `internal/planning`; baseline global preservado.
- `go mod verify`: pass.
- `git diff --check`: pass; solo avisos LF/CRLF del checkout.
- Builds `linux/amd64` y `darwin/amd64` con `CGO_ENABLED=0`: pass.

Trazabilidad añadida: R1/R9/R10/R11 → tests de aggregate compartido, sustitución, fallo y replay; R4/R8/R11/R12 → tabla de hashes y timestamp canónicos. T1–T9 permanecen `[x]`. No se modificaron `feature_list.json` ni `progress/plan-approval/review.md`, y no se usaron red, credenciales, OAuth, targets reales ni tools de producción.

## Corrección posterior a la tercera revisión

- REV-8: `runLifecycle` conserva un propietario único por run y sustituye la
  sección crítica extensa por una reserva tokenizada. El mutex solo protege la
  comprobación de estado, la asignación/liberación de la reserva y el commit
  corto sobre `harness.Run`. Ninguna llamada a store, projector, hashing,
  sanitizer, policy, budgets, tools, waits, evidence o callbacks se ejecuta
  con ese mutex retenido.
- `Stage` y `Decide` calculan y sanean fuera del lock, reservan el lifecycle,
  vuelven a leer y comparar la proyección completa y reconstruyen el prefijo
  confirmado antes de hacer commit del estado. El CAS del journal sigue siendo
  la autoridad final y una operación rival recibe un conflicto tipado.
- `ExecutionComposition` consume el grant mediante el CAS persistente, confirma
  `executing`, libera la reserva y solo entonces delega los planes al
  `PlanExecutor` real. El modo disabled también confirma y libera antes del
  executor.
- `TestSanitizerRunsWithoutLifecycleLock`,
  `TestStoreRunsWithoutLifecycleLock` y
  `TestAppendCASRunsWithoutLifecycleLock` bloquean las fronteras externas,
  adquieren/liberan el lifecycle durante el bloqueo y reentran por `Decide` sin
  deadlock. `TestExecutionCallbacksRunOutsideLifecycleLock` bloquea policy,
  comprueba que el lifecycle está libre y repite idempotentemente `Decide`
  desde el callback antes de una única tool call.
- Las regresiones REV-1–REV-7 permanecen cubiertas por la suite previa,
  incluidas las matrices approve/approve, approve/reject,
  reject/regenerate, append failure/replay, publicación/recovery atómicos y
  schema canónico de hashes/timestamps.

## Verificación fresca posterior a REV-8

- `gofmt -l .`: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con CGO y GCC UCRT64 en `PATH`.
- `go test -race -count=20 ./internal/planapproval ./internal/agentplanning ./internal/harness ./internal/evidence ./internal/planning`: pass.
- Coverage reproducible: 91,1 % global y 91,3 %
  `internal/planapproval`; el mínimo del paquete y el baseline global se
  conservan.
- `go mod verify`: pass.
- `git diff --check`: pass; solo avisos LF/CRLF del checkout.
- Builds `linux/amd64` y `darwin/amd64` con `CGO_ENABLED=0`: pass.

Trazabilidad REV-8: R1/R9/R10/R11/R12 →
`TestSanitizerRunsWithoutLifecycleLock`,
`TestStoreRunsWithoutLifecycleLock`,
`TestAppendCASRunsWithoutLifecycleLock` y
`TestExecutionCallbacksRunOutsideLifecycleLock`, además de todas las
regresiones REV-1–REV-7. T1–T9 continúan `[x]`. No se modificaron
`feature_list.json` ni `progress/plan-approval/review.md`, y no se utilizaron
red, credenciales, OAuth, targets reales ni herramientas de producción.

## Corrección posterior a la cuarta revisión

- REV-9: `localBackend` mantiene mapas `loaded` y `loading` bajo un mutex de
  coordinación breve. Cada run elige un único `runLoadState` owner; los demás
  callers esperan su canal `done` con selección sobre `ctx.Done()`. Runs
  distintos del mismo root no comparten owner ni esperan entre sí.
- `LocalStore.ensureLoaded` libera el mutex antes de recovery, open/read,
  decode, cancelación y `Project`. Solo después de validar el stream completo
  clona el prefijo fuera del lock, lo publica bajo el lock de la partición y
  marca la carga como confirmada. Error o cancelación despiertan waiters,
  eliminan el estado in-flight y permiten un owner nuevo sin reutilizar datos
  temporales.
- La frontera `journalLoadFunc` cubre recovery y lectura y permite bloquear o
  fallar la carga sin red, credenciales ni filesystem externo al directorio
  temporal de cada test.
- `TestLocalLoadsForDifferentRunsProgressIndependently` demuestra que run A
  puede permanecer bloqueado mientras run B completa desde otro handle del
  mismo root. `TestSameRunCallersShareOneLocalLoad` confirma una sola invocación
  y el mismo prefijo para dos handles. `TestCancelledLocalLoadWaiterLeavesOwnerAndRetryStateUntouched`
  cubre espera cancelable. `TestLocalLoadOwnerFailuresPermitCleanRetry` cubre
  error y cancelación del owner seguidos por retry limpio con un único evento
  publicado.
- Todas las regresiones REV-1–REV-8 permanecen en pass, incluidas propiedad
  única del aggregate, schema canónico, recuperación/publicación transaccional,
  CAS, consumo único y fronteras externas sin lifecycle lock.

## Verificación fresca posterior a REV-9

- `gofmt -l .`: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con CGO y GCC UCRT64 en `PATH`.
- `go test -race -count=20 ./internal/planapproval ./internal/agentplanning ./internal/harness ./internal/evidence ./internal/planning`: pass.
- Coverage reproducible: 91,1 % global y 91,5 %
  `internal/planapproval`; el mínimo del paquete y el baseline global se
  conservan.
- `go mod verify`: pass.
- `git diff --check`: pass; solo avisos LF/CRLF del checkout.
- Builds `linux/amd64` y `darwin/amd64` con `CGO_ENABLED=0`: pass.

Trazabilidad REV-9: R10/R11/R12 →
`TestLocalLoadsForDifferentRunsProgressIndependently`,
`TestSameRunCallersShareOneLocalLoad`,
`TestCancelledLocalLoadWaiterLeavesOwnerAndRetryStateUntouched` y
`TestLocalLoadOwnerFailuresPermitCleanRetry`, además de las regresiones
REV-1–REV-8. T1–T9 continúan `[x]`. No se modificaron `feature_list.json` ni
`progress/plan-approval/review.md`.
