# Implementación de environment-cleanup-controller

## Estado

`ready_for_review_6`

La cuarta remediación está completa. REV-1–REV-2 de review 4, REV-1–REV-3 de
review 3 y REV-1–REV-6 de review 2 se reauditaron sobre el
backend multiproceso de receipts/head, la fuente real de capacity receipts, el
scan bounded de workflows, Claim+evento atómico y el digest lifecycle integral.
T1–T16 quedan cerradas con gates frescos y cobertura exacta del diff superior
al 85 % sin redondear.

### Remediación REV-2–REV-6

| Hallazgo | Corrección | Evidencia |
|---|---|---|
| REV-2 | `LocalStore` y `CapacityLedger` clonan estado comprometido, escriben un temporal único fuera del mutex y reservan/publican mediante versión CAS. Un writer bloqueado antes de write no impide publicar otro item/proyecto. | `TestOptimisticPersistenceAllowsIndependentProgress` |
| REV-3 | Ninguna mutación toca estado vivo hasta que rename termina. Fallos before-write/before-publish dejan item, events y cursor byte-idénticos, tanto vivos como tras reopen. | `TestStoreMutationFailureRollsBackLiveAndReopenState`, `TestLedgerPublicationFaultsRollbackInMemoryAndDisk` |
| REV-4 | Eventos cleanup/capacity sellan identidad, owner, attempt, category, timestamp y estado/fencing aplicable; mismo ID solo converge con payload idéntico. Open rechaza duplicado o tamper. | `TestAuditEventPayloadConflictsAreRejected`, `TestCleanupAuditIsExactlyOnceContentFreeAndReopenable`, `TestCapacityAuditTamperFailsReopen` |
| REV-5 | El proof esperado deriva de cleanup + environment digest + resource/UID digest + receipt ID. El receipt conserva proof y autoridad pre-release para recovery posterior. | `TestReleaseConfirmationIsExactAcrossReopen`, `TestMemoryCapacityAdmissionRecoveryMatrix`, `TestCapacityReleaseRecoversAfterConfirmedCrash` |
| REV-6 | `KubernetesInventory` publica un store privado de nombre/UID ligado a cleanup ID y binding digest antes de exponer el candidate. | `TestKubernetesPrivateAuthoritySurvivesRestartMidPagination` |

Comandos del bloque:

- `go test ./internal/environment/cleanup ./internal/environment ./internal/environment/kubernetes ./cmd/featureproof` — pass.
- `go vet ./internal/environment/cleanup ./internal/environment` — pass.
- `go test -count=10 ./internal/environment/cleanup -run 'TestOptimisticPersistenceAllowsIndependentProgress|TestStoreMutationFailureRollsBackLiveAndReopenState|TestAuditEventPayloadConflictsAreRejected|TestCapacityAuditTamperFailsReopen|TestReleaseConfirmationIsExactAcrossReopen|TestKubernetesPrivateAuthoritySurvivesRestartMidPagination'` — pass.
- UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=20 ./internal/environment/cleanup` — pass.
- UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=5 ./internal/environment` — pass.

T2, T9, T10 y T14 quedan cerradas nuevamente. T3, T4, T7, T15 y T16
permanecen abiertas hasta corregir REV-1 y repetir los gates finales completos.

### Remediación REV-1

- `composeWorkflowEnvironmentWithRuntime` exige `managed_capacity` y
  `environment_cleanup` sellados explícitos; no crea admisión efímera ni usa
  números del request/modelo.
- `workflowEnvironmentRuntimes` garantiza una autoridad por root/proyecto en el
  proceso. `workflowDurableCapacity` comparte el `CapacityLedger` reabrible con
  lifecycle y controller y difiere el release físico hasta confirmation.
- `environment.CleanupBridge` deriva binding integral solo desde el lifecycle
  store privado y conecta resolver de intents, índice lifecycle/capacity,
  status/release exactos y ausencia autoritativa.
- `validationworkflow.LocalStore.CleanupPending` expone terminales pendientes a
  la fuente real sin mezclarlos con el scan de recovery no terminal.
- Para Kubernetes, la composición registra `KubernetesInventory` con client,
  owner, page budgets e identity store explícitos. El entrypoint llama `Start`
  y posee el handle `Cancel`/`Join`; ningún constructor arranca goroutines.

Evidencia principal: `TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent`
ejecuta dos adquisiciones concurrentes con cuota uno, identifica un único
ganador, demuestra denial persistente tras reopen, persiste un terminal real
con desired outcome/package, lo descubre mediante `CleanupIntentSource`,
confirma cleanup, libera quota y admite al perdedor. Compara el workflow record
completo antes/después y prueba start/cancel/join. También verifica que
dependencias selladas ausentes fallan antes de provider I/O.

T3, T4, T7 y T15 quedan cerradas nuevamente. Solo T16 permanece abierta para
coverage exacta y gates finales completos.

Verificación fresca del bloque:

- `go test -count=1 ./internal/environment/cleanup ./cmd/featureproof ./internal/environment ./internal/validationworkflow` — pass.
- `go test -count=10 ./cmd/featureproof -run '^TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent$'` — pass.
- `go vet ./...` — pass.
- `go test -run '^$' ./...` — pass.
- UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=10 ./cmd/featureproof -run '^TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent$'` — pass.
- `gofmt -w internal/environment/cleanup/store.go` y `git diff --check` — pass; solo avisos CRLF informativos.

T1–T15 están completadas con evidencia reproducible. T16 sigue reabierta hasta
repetir coverage y gates finales sobre la remediación. `feature_list.json`
permanece intacto; la transición pertenece al leader tras review independiente.

## Bloque T2–T4 completado

- Store y ledger locales reabren receipts sellados, detectan corrupción,
  aplican CAS/leases, límites y overflow, y liberan exactamente una vez.
- `LifecycleService.Acquire` reserva antes del primer provider I/O y confirma
  environment/resource identity; incertidumbre conserva la reserva.
- `LifecycleService.Release` libera capacidad solo después de persistir
  `StateReleased`; redelivery recupera la liberación pendiente.
- La composición CLI aporta demand/policy explícitas para managed; existing-url
  no reserva capacidad.
- Tests: `TestManagedCapacitySurroundsProviderLifecycle`,
  `TestManagedCapacityDenialPreventsProviderIO`,
  `TestMemoryCapacityAdmissionSerializesConcurrentLimit` y
  `TestCapacityLedgerReopensImmutableReceipts`.

## Bloque T5–T10 completado

- `validationworkflow.CleanupIntentSource` lee el store real en modo read-only,
  proyecta passed/failed/inconclusive/insufficient-context/error/cancelled y
  conserva intactos desired outcome, package y evidencia. `RunAbsent` solo
  acepta el `ErrNotFound` autoritativo; indisponibilidad no equivale a ausencia.
- Discovery une intents, índices lifecycle/capacity e inventarios por identidad
  de cleanup. Cada fuente usa cursor CAS sellado y reabrible; tokens vacíos,
  repetidos, corrupción y drift de snapshot fallan cerrados.
- El límite TTL es inclusivo y `LifecycleIndexSource` no entrega records con
  lease lifecycle viva. Los timestamps fuera del rango durable se rechazan.
- Huérfanos requieren metadata integral, provider/version registrados, expiry
  más grace y ausencia autoritativa tanto de lifecycle como de workflow. Los
  objetos owned incompletos producen capacidad `unknown` y bloquean admisión.
- Adquisiciones inciertas usan un binding separado que solo puede ejecutar
  Reconcile; el binding conocido se persiste y cede ownership antes de que un
  ciclo posterior pueda ejecutar Release.
- Cleanup conocido y orphan confirman ausencia antes de liberar capacidad. Un
  crash tras `confirmed` recupera la liberación sin repetir la llamada física.

### Evidencia T5–T10

| Task | Tests principales |
|---|---|
| T5 | `TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation`, `TestCleanupIntentSourceRecoversCrashWindow`, `TestCleanupIntentSourceDistinguishesAuthoritativeAbsence` |
| T6 | `TestLifecycleCleanupRequiresCompleteSealedBinding`, `TestKubernetesErrorsRemainContentFreeAndCancelable`, `TestControllerChoosesOnlyRegisteredOrphanInventory` |
| T7 | `TestDiscoveryMergesLifecycleAndCapacityWithoutDuplicateIntent`, `TestIntentDiscoveryResumesDurableCursorAfterReopen`, `TestDiscoveryCursorCorruptionFailsClosed`, `TestInventoryDiscoveryRejectsSnapshotDrift` |
| T8 | `TestControllerExactTTLRetryAndPermanentCategories`, `TestLifecycleDiscoveryRespectsLiveLeaseAtExpiry`, `TestCleanupModelRejectsInvalidPoliciesItemsAndOverflow` |
| T9 | `TestInventoryDiscoveryAdoptsOnlyAfterGraceAndBothAbsences`, `TestInventoryDiscoveryBeforeGraceOrAmbiguousAuthorityDoesNotAdopt`, `TestMalformedOwnedInventoryBlocksProjectAdmission` |
| T10 | `TestUncertainAcquisitionPersistsKnownBindingBeforeRelease`, `TestLifecycleAdapterRejectsInvalidUncertainAuthorityBeforePort`, `TestCapacityReleaseRecoversAfterConfirmedCrash`, `TestCleanupUsesStableIdentityAndConfirmsAbsenceIdempotently` |

## Bloque T1 y T11–T15 completado

- T1 cierra source/state/binding vocabularies, strict decode sin unknown/trailing
  data, timestamps durables, overflow y deep-copy de binding incierto.
- T11 persiste attempts, categoría, `next_attempt_at` y cumulative wait antes de
  ceder ownership; deadlines se clasifican por tipo y una autorización sellada
  exacta es la única vía de requeue desde blocked/quarantine.
- T12 revalida eligibility dentro de `Claim`, por lo que un stale scan no puede
  saltarse lease/retry. Tests concurrentes prueban un solo owner físico, fencing,
  handoff inclusivo y ausencia de lock local durante provider I/O.
- T13 conserva `Run` caller-owned sin goroutines ocultas. Cancelar dentro de
  provider devuelve coordinadamente y deja lease/intent durable para handoff.
- T14 limita Kubernetes a List/Get/Delete Namespace, owner scope y UID
  precondition; distingue NotFound, Forbidden, conflict, rate-limit y timeout,
  no fuerza Terminating y converge tras response loss por reread exacto.
- T15 añade `Status` operacional local y observer best-effort separado del
  outcome. La composición de `cmd/featureproof` abre store/ledger locales,
  exige clock/waiter/lifecycle/config explícitos y no inicia goroutines.
  Discovery demuestra preservación byte-exact de outcome/package/report.

### Evidencia T1/T11–T15

| Task | Tests principales |
|---|---|
| T1 | `TestCleanupItemStrictDecodeAndDeepCopy`, `TestCleanupModelRejectsInvalidPoliciesItemsAndOverflow`, `TestBindingAndAdapterConstructorsRejectIncompleteAuthority` |
| T11 | `TestRetryBudgetPersistsAcrossClaimsAndRequiresAuthorizedRequeue`, `TestCleanupRetriesAreTypedBoundedAndDurableAcrossRestart`, `TestPermanentFailureQuarantinesAndDoesNotRetry` |
| T12 | `TestConcurrentRunOnceUsesSingleLiveProviderOwner`, `TestConcurrentControllersHandoffWithoutDuplicateLogicalCleanup`, `TestLocalStoreLeaseCASRenewEventsAndCursor` |
| T13 | `TestRunOnceCancellationDuringProviderLeavesLeaseForHandoff`, `TestControllerRunLoopIsCallerOwnedAndCancelable`, `TestRunPerformsOneCycleThenReturnsWaiterError` |
| T14 | `TestKubernetesDeleteResponseLossConvergesThroughExactReread`, `TestKubernetesInventoryScopesOwnerAndDeletesOnlyExactUID`, `TestKubernetesErrorsRemainContentFreeAndCancelable` |
| T15 | `TestStatusProjectionAndObserverFailureRemainOperational`, `TestComposeWorkflowCleanupControllerIsLocalExplicitAndInert`, `TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation` |

## Implementado

- Modelo canónico de cleanup, taxonomía, `LifecycleCleanupBinding` integral y
  binding incierto no destructivo.
- Store local reabrible con intents previos a efectos, eventos exactly-once,
  claims, leases, fencing y CAS dentro del proceso local.
- Ledger atómico por proyecto con límites, overflow checks, confirmación de
  identidad y liberación exactly-once tras confirmation.
- Controller `Run`/`RunOnce` cancelable, paginado y acotado, retries tipados,
  backoff, quarantine y handoff de leases.
- Adapters read-only de workflow y lifecycle con rechazo cross-authority.
- Inventario Kubernetes owner-scoped y delete exacto con precondición UID, sin
  force/finalizer patch ni acceso a Secrets.
- Tests herméticos de identidad, restart, TTL, cuota, retry, concurrencia,
  cancelación, auditoría, lifecycle binding y fake Kubernetes.

## Requirements y tests

| Requirement | Tests concretos | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestCleanupItemStrictDecodeAndDeepCopy`, `TestManagedCapacitySurroundsProviderLifecycle`, `TestKubernetesInventoryScopesOwnerAndDeletesOnlyExactUID` | `internal/environment/cleanup/edge_test.go`, `internal/environment/environment_test.go` | pass |
| R2 | `TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation`, `TestCleanupIntentSourceRecoversCrashWindow`, `TestEveryTerminalOutcomePersistsCleanupIntentBeforeProviderIO` | `internal/validationworkflow/cleanup_intents_test.go`, `internal/environment/cleanup/cleanup_test.go` | pass |
| R3 | `TestDiscoveryMergesLifecycleAndCapacityWithoutDuplicateIntent`, `TestIntentDiscoveryResumesDurableCursorAfterReopen`, `TestRunPerformsOneCycleThenReturnsWaiterError` | `internal/environment/cleanup/edge_test.go` | pass |
| R4 | `TestControllerExactTTLRetryAndPermanentCategories`, `TestLifecycleDiscoveryRespectsLiveLeaseAtExpiry`, `TestCleanupModelRejectsInvalidPoliciesItemsAndOverflow` | `internal/environment/cleanup/edge_test.go` | pass |
| R5 | `TestInventoryDiscoveryAdoptsOnlyAfterGraceAndBothAbsences`, `TestInventoryDiscoveryBeforeGraceOrAmbiguousAuthorityDoesNotAdopt`, `TestMalformedOwnedInventoryBlocksProjectAdmission` | `internal/environment/cleanup/edge_test.go` | pass |
| R6 | `TestLifecycleCleanupRequiresCompleteSealedBinding`, `TestLifecycleAdapterRejectsInvalidUncertainAuthorityBeforePort`, `TestKubernetesDeleteResponseLossConvergesThroughExactReread` | `internal/environment/cleanup/edge_test.go` | pass |
| R7 | `TestRetryBudgetPersistsAcrossClaimsAndRequiresAuthorizedRequeue`, `TestCleanupRetriesAreTypedBoundedAndDurableAcrossRestart`, `TestPermanentFailureQuarantinesAndDoesNotRetry` | `internal/environment/cleanup/edge_test.go`, `internal/environment/cleanup/cleanup_test.go` | pass |
| R8 | `TestConcurrentRunOnceUsesSingleLiveProviderOwner`, `TestConcurrentControllersHandoffWithoutDuplicateLogicalCleanup`, `TestLocalStoreLeaseCASRenewEventsAndCursor` | `internal/environment/cleanup/cleanup_test.go`, `internal/environment/cleanup/edge_test.go` | pass + race ×20 |
| R9 | `TestManagedCapacityDenialPreventsProviderIO`, `TestMemoryCapacityAdmissionSerializesConcurrentLimit`, `TestMemoryCapacityAdmissionRecoveryMatrix`, `TestUnknownInventoryLedgerBlocksAcrossReopen` | `internal/environment/environment_test.go`, `internal/environment/cleanup/recovery_matrix_test.go` | pass + race |
| R10 | `TestMemoryCapacityAdmissionRecoveryMatrix`, `TestCapacityReleaseRecoversAfterConfirmedCrash`, `TestCleanupUsesStableIdentityAndConfirmsAbsenceIdempotently`, `TestOrphanLedgerRecoveryMatrix` | `internal/environment/environment_test.go`, `internal/environment/cleanup/edge_test.go`, `internal/environment/cleanup/recovery_matrix_test.go` | pass |
| R11 | `TestCleanupIntentSourceProjectsEveryOutcomeWithoutMutation`, `TestStatusProjectionAndObserverFailureRemainOperational` | `internal/validationworkflow/cleanup_intents_test.go`, `internal/environment/cleanup/edge_test.go` | pass |
| R12 | `TestRunOnceCancellationDuringProviderLeavesLeaseForHandoff`, `TestControllerCancellationStopsNewWorkAndLeavesRecoverableIntent`, `TestControllerRunLoopIsCallerOwnedAndCancelable` | `internal/environment/cleanup/edge_test.go`, `internal/environment/cleanup/cleanup_test.go` | pass |
| R13 | `TestCleanupAuditIsExactlyOnceContentFreeAndReopenable`, `TestOrphanLedgerRecoveryMatrix`, `TestUnknownInventoryLedgerBlocksAcrossReopen`, `TestStatusProjectionAndObserverFailureRemainOperational` | `internal/environment/cleanup/cleanup_test.go`, `internal/environment/cleanup/recovery_matrix_test.go`, `internal/environment/cleanup/edge_test.go` | pass |
| R14 | `TestComposeWorkflowCleanupControllerIsLocalExplicitAndInert`, `TestBindingAndAdapterConstructorsRejectIncompleteAuthority` | `cmd/featureproof/environment_cleanup_test.go`, `internal/environment/cleanup/edge_test.go` | pass |
| R15 | `TestLedgerPublicationFaultsRollbackInMemoryAndDisk`, `TestStoreOperationalFailureMatrix`, suites focales/globales/race y runtime Linux | `internal/environment/cleanup/recovery_matrix_test.go` + gates | pass |

## Tasks

| Tasks | Estado | Evidencia |
|---|---|---|
| T1–T4 | done | modelo/strict decode, store/ledger y capacity vertical antes de Acquire |
| T5–T10 | done | intents reales, discovery/cursors, TTL, orphan y reconciler idempotente |
| T11–T15 | done | scheduler, leases/race, shutdown, Kubernetes, status/composición |
| T16 | done | recovery matrix, trazabilidad, coverage exacta y gates multiplataforma repetidos tras review 2 |

## Verificación realizada

| Comando | Resultado |
|---|---|
| `gofmt -w internal/environment/cleanup` | pass |
| `go vet ./internal/environment/cleanup` | pass |
| `go test ./internal/environment/cleanup` | pass |
| `go test ./internal/environment ./internal/environment/kubernetes ./internal/environment/cleanup ./cmd/featureproof` | pass |
| `go test -count=1 ./internal/environment/cleanup` tras T5–T10 | pass |
| `go test -count=1 ./internal/validationworkflow -run TestCleanupIntent` | pass |
| `go vet ./internal/environment/cleanup ./internal/validationworkflow` | pass |
| `go test -count=1 ./...` | pass |
| `go test -count=1 ./internal/environment ./internal/environment/kubernetes ./internal/environment/cleanup` | pass |
| `go test -count=1 ./cmd/featureproof` | pass |
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -count=20 ./internal/environment/cleanup` | pass |
| `go test -count=10 ./internal/validationworkflow -run TestCleanupIntent` | pass |
| `go test -count=10 ./cmd/featureproof -run TestComposeWorkflowCleanupController` | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass |
| UCRT64 `go test -race -count=5 ./internal/environment` | pass, incluida matriz real de admission |
| `go test -count=1 -p=1 -coverprofile=coverage-feature34.out ./...` | pass; exact global `36546/42430 = 86,132453452746 %` |
| focal `go test -coverprofile=cleanup.cover ./internal/environment/cleanup` | pass; exact `830/975 = 85,1282051282051 %` |
| script de statements añadidos/modificados sobre `git diff --unified=0` + `coverage-feature34.out` | pass; exact diff `996/1163 = 85,640584694755 %` |
| `go test -c`, 4 paquetes × Windows/Linux/Darwin amd64 | pass 12/12 |
| binario Linux Alpine 3.22.2: cleanup `-test.count=20`, validationworkflow/cmd `-test.count=1` | pass 3/3 |

El primer intento paralelo instrumentado de coverage expuso timeouts en
`contextassembly`/`validationworkflow` ajenos al diff; la ejecución canónica
serializada `-p=1` pasó completa. Un primer race halló una carrera real en el
fake `testCapacity` modificado por esta feature; se corrigió con mutex y tanto
el focal como el race global posteriores pasaron.

## Coverage

- Código de producción añadido/modificado: `996/1163` statements,
  `85,640584694755 %` exacto, sin redondear. La medición cruza los rangos
  añadidos de `git diff --unified=0` (y todos los rangos de archivos nuevos)
  con los statement blocks del profile global; excluye tests.
- Código principal nuevo `internal/environment/cleanup`: `830/975` statements,
  `85,1282051282051 %` exacto, sin redondear.
- Suite global final: `36546/42430`, `86,132453452746 %` exacto.
- Las pruebas añadidas cubren fault boundaries antes de write/publish,
  reopen/corrupción, todos los terminales, TTL exacto, orphan/unknown quota,
  response loss, retry/requeue, contenders, cancelación y handoff. La matriz
  real de admission cubre además cancelación, TTL no autorizado, redelivery
  exacta/conflictiva, binding provider inmutable, release exactly-once y
  overflow vectorial sin sustituir esas ramas por un fake.

## Auditoría final

- `feature_list.json` no fue modificado; no hubo commit ni push.
- No se añadieron PostgreSQL, queue, worker remoto, config versionada, tenant o
  RBAC. Los tests de producto no usan red externa, DNS, cluster, cloud ni modelo.
- Los canarios remotos aparecen solo en tests y no atraviesan errores públicos;
  no se persistieron credentials, bearer tokens, endpoints ni bodies remotos.
- La descarga de Alpine usada exclusivamente para el gate runtime se verificó
  contra SHA-256 oficial. La distro WSL efímera, rootfs, binarios, cross-builds
  y perfiles de coverage se eliminaron tras registrar los resultados.
- Se revisaron comentarios sistémicos de entidades, constantes, funciones,
  métodos y helpers nuevos/modificados. No quedan procesos Go ni temporales de
  feature 34.

## Riesgos o limitaciones

- El controller es local y explícitamente caller-owned; distribución,
  PostgreSQL/queue/workers, configuración versionada y tenant/RBAC pertenecen a
  features 35–37 y no se adelantaron.
- El filesystem local usa receipts inmutables, head durable, fsync y file lock
  crash-released para coordinar procesos del mismo host; no afirma semántica de
  consenso sobre filesystems remotos ni coordinación distribuida multi-host.

## Cierre T16 tras review 1

### Reauditoría REV-1–REV-6

| Hallazgo | Evidencia final | Estado |
|---|---|---|
| REV-1 | `TestWorkflowEnvironmentRuntimeSharesDurableCapacityAndCleansTerminalIntent` usa composición real, dos adquisiciones y una cuota compartida, reopen del ledger, intent terminal real, release ejecutado por el controller, confirmación previa a liberar capacidad y `Cancel`/`Join`. `TestCleanupBridgeProjectsAndReleasesDurableLifecycleAuthority` prueba paginación, binding exacto y transición present→absent. | corregido |
| REV-2 | `TestOptimisticPersistenceAllowsIndependentProgress` bloquea un write y demuestra progreso independiente sin mutex durante filesystem I/O. | corregido |
| REV-3 | `TestStoreMutationFailureRollsBackLiveAndReopenState` y `TestLedgerPublicationFaultsRollbackInMemoryAndDisk` cubren Reserve/Claim/Renew/SaveOwned/ReleaseOwner/Requeue/cursor antes de write/publish, estado vivo y reopen. | corregido |
| REV-4 | `TestAuditEventPayloadConflictsAreRejected`, `TestCleanupAuditIsExactlyOnceContentFreeAndReopenable` y `TestCapacityAuditTamperFailsReopen` verifican payload integral sellado, conflicto de ID y tamper. | corregido |
| REV-5 | `TestReleaseConfirmationIsExactAcrossReopen`, `TestMemoryCapacityAdmissionRecoveryMatrix` y `TestCapacityReleaseRecoversAfterConfirmedCrash` ligan cleanup/environment/resource/receipt antes y después de release/reopen. | corregido |
| REV-6 | `TestKubernetesPrivateAuthoritySurvivesRestartMidPagination` conserva nombre/UID privados ligados al binding y aplica UID precondition después de restart. | corregido |

La reauditoría de cobertura descubrió y corrigió una rama vertical adicional:
`lifecycle_record_digest` ya no incluye estado, lease, retry o revisión mutables;
sella únicamente la autoridad lifecycle inmutable. Así el mismo binding aprobado
puede comprobar `absent` después de que el controller libere un lifecycle vivo,
sin permitir sustitución de project/run/provider/selection/acquisition,
environment/resource, cleanup key o receipt.

### Gates finales frescos

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -count=20 ./internal/environment/cleanup` | pass |
| focales cleanup intent, bridge y composición vertical ×10 | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass |
| UCRT64 vertical composición `-race -count=20` | pass |
| UCRT64 bridge productivo `-race -count=20` | pass |
| `go test -count=1 -p=1 "-coverprofile=coverage-feature34-final.out" ./...` | pass |
| script de intersección de statement blocks con `git diff --unified=0`, excluyendo tests | `1357/1586 = 85,56116015132408575031525851 %` exacto |
| suma exacta del profile global | `36887/42834 = 86,11616939814166316477564552 %` |
| `go test -c` para cmd/environment/cleanup/kubernetes en Windows, Linux y Darwin amd64 | pass 12/12 |
| binarios Linux amd64 reales en Alpine 3.22.2: cleanup ×20, environment, kubernetes y cmd | pass 4/4 |

El rootfs Alpine fue descargado desde el release oficial y validado con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
WSL2 no pudo crear la VM por recursos del host; la misma imagen autenticada se
importó en WSL1 y ejecutó los cuatro binarios. La distro, rootfs, perfiles y los
12 cross-builds se eliminaron después del gate.

R1–R15 permanecen trazados por la tabla anterior; las nuevas pruebas refuerzan
R1/R3/R10/R14/R15 sin sustituir ninguna matriz previa. T1–T16 quedan `[x]`.
Se reauditaron comentarios sistémicos, scope, canarios, secretos, red externa y
temporales: no hay credenciales ni payloads remotos persistidos, los tests de
producto son herméticos, no se adelantó PostgreSQL/queue/workers/config/tenant,
y `feature_list.json` sigue intacto sin commit ni push.

## Review 2 — bloque A: REV-3 y REV-6

- `LocalStore`, `CapacityLedger` y el store privado de `KubernetesInventory`
  dejaron de publicar un snapshot sobrescribible con versión solo en memoria.
  Cada commit crea un receipt de versión inmutable, sincroniza archivo/directorio
  y avanza un head pequeño mediante rename bajo un file lock crash-released.
- El head liga kind, versión, nombre y digest del receipt. Reopen verifica el
  receipt seleccionado y toda la cadena `previous_digest`; versión forjada,
  receipt alterado o historia truncada fallan cerrados.
- Cada mutación recarga el head bajo el lock de filesystem antes de calcular el
  candidato. Ningún I/O ni fsync ocurre bajo mutex Go; el mutex protege solo la
  sustitución del cache detached después del commit/reload.
- Lecturas de items/cursors/events/receipts/quota y autoridad Kubernetes
  refrescan el head para observar commits de otra instancia. La identidad raw
  namespace/UID se confirma antes de devolver el candidate; por tanto el cursor
  externo nunca puede avanzar sin autoridad privada ya seleccionada por head.

Evidencia nueva:

| Garantía | Test |
|---|---|
| Reserve simultáneo sin lost update; Claim con un solo lease; Renew, SaveOwned, ReleaseOwner, Requeue, cursor CAS y events entre dos stores abiertos previamente | `TestCleanupStoreCoordinatesIndependentInstances` |
| quota concurrente con un ganador, confirm/release cruzados, adopt/block y events compartidos | `TestCapacityLedgerCoordinatesIndependentInstances` |
| faults before/after event, index, receipt, head y publish; receipt huérfano no visible y response-loss después del head sí visible | `TestDurableHeadsDefineCrashOutcomes` |
| versión de head forjada e historia de receipts truncada rechazadas | `TestDurableHeadRejectsForgedVersionAndBrokenHistory` |
| dos adapters Kubernetes publican UIDs distintos sin pérdida; faults de index/receipt/head/publish y restart recuperan delete con UID exacto | `TestKubernetesPrivateAuthorityCoordinatesInstancesAndCrashBoundaries` |

Comandos del bloque:

- `go test -count=1 ./internal/environment/cleanup` — pass.
- `go test -count=1 ./cmd/featureproof ./internal/environment` — pass.
- `go vet ./internal/environment/cleanup ./cmd/featureproof ./internal/environment` — pass.
- focales nuevas `-count=10` — pass.
- UCRT64 focales nuevas `go test -race -count=20` — pass.
- UCRT64 suite cleanup completa `go test -race -count=10 ./internal/environment/cleanup` — pass.

REV-3 y REV-6 quedan corregidas. T2, T3, T12 y T14 vuelven a `[x]`; T16
permanece abierta y REV-1/REV-2/REV-4/REV-5 no se adelantan en este bloque.
`feature_list.json` permanece intacto y no hubo commit ni push.

## Review 2 — bloque B: REV-1, REV-2, REV-4 y REV-5

- `CapacityLedger.ScanReceipts` pagina el índice durable real por receipt ID y
  verifica cada receipt al cargar la cadena. `workflowCapacitySource` ya no
  duplica lifecycle discovery: clasifica receipts unbound, consulta el store
  lifecycle exacto y solo libera el receipt si nunca cruzó la frontera durable
  previa a provider I/O.
- `LifecycleService` invoca el puerto opcional `CapacityLifecycleBinder`
  inmediatamente después de `EnvironmentStore.Reserve` y antes de cualquier
  Acquire/Reconcile. Si un reconciler libera el receipt en la ventana exacta,
  el bind falla y el lifecycle aborta antes del provider; si el record ya existe,
  la fuente capacity no lo libera.
- `validationworkflow.LocalStore.CleanupPending(cursor, limit)` ejecuta ReadDir
  sin mutex Go ni file lock, selecciona como máximo `limit` handles y carga cada
  checkpoint con su lock corto/cancelable. La fuente usa directamente su cursor
  y límite durable en vez de cargar todos los workflows antes de paginar.
- `LocalStore.Claim` sella `cleanup_claimed` junto al item, owner, fencing,
  attempt, state, category y timestamp en el mismo receipt/head. Un fault en la
  frontera del evento deja ambos sin publicar; retry crea un solo evento y el
  mismo ID con payload divergente produce conflict.
- `cleanupLifecycleRecordDigest` incluye commit, selección sellada completa,
  created/expiry/TTL, demand, receipt ID/digest, project/run/acquisition,
  environment/resource, lifecycle y cleanup key. Excluye únicamente revision,
  state, owner/lease, attempts, updated time y flags operativos documentados.

Evidencia nueva:

| Hallazgo | Test |
|---|---|
| receipt-only tras Reserve de quota y antes de environment Reserve; reopen/controller libera sin provider assumption | `TestWorkflowCapacitySourceRecoversReceiptOnlyCrashWindow` |
| binder durable ocurre una vez entre reserve y provider lifecycle | `TestManagedCapacitySurroundsProviderLifecycle` |
| scan workflow limitado a un load y otro run progresa durante barrera | `TestCleanupPendingScanIsBoundedAndDoesNotBlockOtherRuns` |
| filtro/sort/cancel/corrupción del scan productivo | `TestLocalStoreCleanupPendingReturnsOnlyDurableCleanupObligations` |
| Claim+evento atómico, crash/reopen, retry exactly-once y payload conflictivo | `TestClaimPublishesItemAndAuditAtomically` |
| tabla de tamper de commit/selection/time/TTL/demand/receipt/identidades y transición mutable legítima | `TestCleanupLifecycleRecordDigestCoversEveryImmutableAuthorityField` |
| scan real de receipts, cursor/limit/digest y mutaciones cruzadas | `TestCapacityLedgerCoordinatesIndependentInstances` |

Verificación del bloque:

- `go test -count=1 ./internal/environment/cleanup ./internal/environment ./internal/validationworkflow ./cmd/featureproof` — pass.
- focales de Claim/capacity/digest/bounded scan/composición ×10 — pass.
- `go vet ./internal/environment/cleanup ./internal/environment ./internal/validationworkflow ./cmd/featureproof` — pass.
- UCRT64 cleanup/Claim/capacity `-race -count=20` — pass.
- UCRT64 validationworkflow bounded scan `-race -count=20` — pass.
- UCRT64 environment digest y cmd capacity/vertical `-race -count=20` — pass.
- `gofmt -l` y `git diff --check` — pass; solo avisos CRLF informativos.

REV-1, REV-2, REV-4 y REV-5 quedan corregidas. T1, T7 y T15 permanecen `[x]`
tras revalidación; solo T16 continúa abierta para gates/coverage globales del
estado completo. `feature_list.json` permanece intacto y no hubo commit/push.

## Cierre T16 tras review 2

### Reauditoría REV-1–REV-6

| Hallazgo | Evidencia sobre el código final | Estado |
|---|---|---|
| REV-1 | `TestWorkflowCapacitySourceRecoversReceiptOnlyCrashWindow` recorre la ventana exacta entre quota Reserve y environment Reserve; `TestCapacityLifecycleHandoffAndReceiptOnlyResolution` distingue durablemente receipt bound de receipt pre-lifecycle y prueba redelivery/reopen. | corregido |
| REV-2 | `TestCleanupPendingScanIsBoundedAndDoesNotBlockOtherRuns` bloquea la carga de una página y demuestra progreso de otro run; `CleanupPending` aplica cursor/limit antes de cargar records y no conserva mutex Go/file lock durante `ReadDir`. | corregido |
| REV-3 | `TestCleanupStoreCoordinatesIndependentInstances`, `TestCapacityLedgerCoordinatesIndependentInstances` y `TestDurableHeadsDefineCrashOutcomes` prueban dos instancias, CAS bajo file lock, receipts/head, faults before/after receipt/head/publish y ausencia de lost updates. | corregido |
| REV-4 | `TestClaimPublishesItemAndAuditAtomically` prueba Claim+`cleanup_claimed` en un único commit, crash/reopen, exactly-once y conflicto por payload divergente. | corregido |
| REV-5 | `TestCleanupLifecycleRecordDigestCoversEveryImmutableAuthorityField` altera uno por uno commit, selección, tiempos/TTL, demand, receipt e identidades y permite únicamente transiciones operativas mutables. | corregido |
| REV-6 | `TestKubernetesPrivateAuthorityCoordinatesInstancesAndCrashBoundaries` prueba dos adapters, restart a mitad de paginación, receipts/head privados y delete posterior con UID precondition exacta. | corregido |

La matriz final añadió `TestDurableSnapshotIntegrityMatrix`, que rechaza head
malformado, digest de head distinto, receipt seleccionado ausente/malformado,
payload del receipt alterado, previous digest inválido e historia truncada. La
redelivery de un receipt inmutable solo converge si sus bytes son idénticos.

### R1–R15 revalidados

La tabla `Requirements y tests` anterior sigue siendo el mapa canónico. La
segunda remediación refuerza específicamente R1/R3/R8/R9/R10/R13/R14/R15 con
los tests multiproceso, receipt-only, Claim atómico, bounded scan, lifecycle
tamper y autoridad Kubernetes enumerados arriba; ninguna requirement depende
solo de un test vertical de `cmd`.

### Gates finales frescos

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; únicamente avisos CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -count=20 ./internal/environment/cleanup` | pass |
| validationworkflow bounded scan/intents `-count=10` | pass |
| environment admission/digest/bridge `-count=10` | pass |
| cmd receipt-only/composición vertical `-count=10` | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass, repetido después de las pruebas finales |
| UCRT64 multiproceso/Claim/Kubernetes `-race -count=20` | pass |
| UCRT64 bounded workflow scan `-race -count=20` | pass |
| UCRT64 admission/digest/bridge `-race -count=20` | pass |
| UCRT64 receipt-only/composición real `-race -count=20` | pass |
| `go test -count=1 -p=1 "-coverprofile=coverage-feature34-review2.out" ./...` | pass |
| script de intersección `git diff --unified=0`/statement blocks, tests excluidos | `1643/1926 = 85,306334371754932502596054 %` exacto |
| suma exacta del profile global | `37176/43174 = 86,1073794413304303516005003 %` |
| `go test -c` para cmd/environment/cleanup/kubernetes en Windows, Linux y Darwin amd64 | pass 12/12 |
| binarios Linux amd64 reales en Alpine 3.22.2: cleanup ×20, environment, kubernetes y cmd | pass 4/4 |

El runtime usó el minirootfs oficial Alpine 3.22.2 validado con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.

### Auditoría final de seguridad y scope

- Todas las adquisiciones del file lock tienen release diferido; el lock se
  libera también por crash del proceso. Files, receipts y head se sincronizan
  antes de éxito; Unix sincroniza además ambos directorios y Windows documenta
  su frontera soportada de file sync + atomic rename.
- Ningún filesystem I/O, fsync, callback de observer, provider I/O o espera se
  ejecuta bajo un mutex Go. El mutex solo publica/lee copias detached del cache.
- Los tests de producto son herméticos: no acceden a red, cluster, credentials,
  modelo ni secretos reales. La única red externa fue la descarga explícita del
  rootfs oficial para el gate Linux, verificada antes de importarlo.
- No se añadieron PostgreSQL, queue, workers, configuración versionada,
  tenant/RBAC, dual-write ni autoridad destructiva genérica. Kubernetes sigue
  limitado a Namespace owner-scoped y UID-safe.
- Se revisaron comentarios sistémicos de símbolos y helpers añadidos. La distro
  WSL, rootfs, cross-builds y perfiles se eliminaron; no quedan procesos Go ni
  temporales de la verificación.
- `feature_list.json` permanece byte-intacto. No hubo commit ni push.

T1–T16 quedan `[x]` y la feature está preparada para review independiente 3.

## Cierre tras review 3

### REV-1 — redelivery de capacity avanzada

`CapacityLedger.Reserve` compara exclusivamente la autoridad inmutable de la
solicitud original: ID/proyecto/run/acquisition/selection/policy, demand y TTL.
Un receipt inicial, ligado o confirmado devuelve el receipt durable corriente;
un receipt released, unknown, con identidad provider parcial o con cualquier
campo inmutable divergente falla con conflict. `LifecycleService` conserva el
receipt corriente devuelto por Reserve aunque el record durable aún tenga el
digest anterior, exige el mismo receipt ID y ejecuta Bind antes de provider I/O.

Evidencia:

- `TestCapacityLifecycleHandoffAndReceiptOnlyResolution` prueba Reserve tras
  Bind y Confirm, divergencia de run/selection/demand/TTL/policy y rechazo tras
  Release.
- `TestWorkflowCapacityRedeliverySurvivesBoundDigestCrashAndConfirmation`
  usa composición productiva, inyecta pérdida de respuesta después del commit
  de Bind pero antes de guardar el nuevo digest, expira el lease sin espera
  real, reabre stores/ledger, recupera y verifica exactamente una llamada
  provider. La redelivery post-Confirm no repite I/O.

### REV-2 — página workflow filtrada

`CleanupPending` valida el cursor, pagina handles antes de cargar records y
mantiene el límite dentro del store. `CleanupIntentSource` marca `Scanned`
únicamente cuando recibió una página del store durable. `discoverIntents`
acepta una página filtrada vacía con continuation solo bajo esa marca, rechaza
providers que no la ofrecen y detecta token repetido antes de publicar cursor.

`TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart` crea
un primer handle activo y un segundo terminal con `PageSize=1/MaxPages=1`; el
primer ciclo persiste el cursor sin intent, reabre el cleanup store y el segundo
ciclo descubre el terminal. También rechaza cursor store inválido. Las pruebas
previas de bounds, barrera y token-cycle permanecen verdes.

### REV-3 — anchor durable anti-rollback

Cada backend crea un anchor sellado `initialized` antes del receipt y publica
el anchor de versión antes de seleccionar el head. Por tanto:

- crash/fault después del receipt y antes del anchor/head deja un orphan
  legítimo reintentable;
- crash entre anchor y head falla cerrado, nunca vuelve a vacío/legacy;
- pérdida de un head confirmado, rollback de versión, marker ausente/corrupto,
  historia no anclada y genesis con `PreviousDigest` no vacío fallan integrity;
- receipt, anchor, head y directorios se sincronizan antes de éxito, siempre
  fuera del mutex Go y bajo file lock crash-released/cancelable.

Evidencia:

- `TestDurableAnchorDistinguishesOrphanReceiptFromHeadLoss` cubre orphan
  pre-head, retry byte-idéntico, missing head con/sin legacy, historia sin
  anchor con/sin legacy y genesis con padre forjado.
- `TestDurableHeadLossFailsAcrossStoreLedgerAndKubernetesInstances` abre dos
  instancias de cleanup store, capacity ledger y Kubernetes authority, observa
  el commit cruzado y demuestra que ambas fallan cerradas tras retirar el head.
- `TestDurableSnapshotIntegrityMatrix` añade marker ausente a la matriz previa
  de head/receipt/cadena alterados.

### Gates finales review 3

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos LF/CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| focales REV-1–REV-3 `-count=20` | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass |
| UCRT64 multiproceso/anchor/capacity `-race -count=20` | pass |
| UCRT64 workflow filtered-page vertical `-race -count=20` | pass |
| UCRT64 composición capacity crash/redelivery `-race -count=20` | pass |
| `go test -count=1 -p=1 "-coverprofile=coverage-feature34-review3.out" ./...` | pass |
| intersección exacta diff productivo/statement blocks | `1713/2009 = 85,26630164260826281732205077 %` |
| suma exacta del profile global | `37246/43257 = 86,10398317035393115565110849 %` |
| `go test -c` cmd/environment/cleanup/kubernetes × Windows/Linux/Darwin amd64 | pass 12/12 |
| binarios Linux amd64 reales en Alpine 3.22.2: cleanup ×20, environment, kubernetes y cmd | pass 4/4 |

El rootfs oficial Alpine 3.22.2 se validó con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
La distro, rootfs, cross-builds y profiles fueron eliminados después del gate.
No quedan procesos Go ni temporales; no hubo red real en tests, cluster,
credentials o secretos. No se amplió autoridad destructiva ni se adelantaron
PostgreSQL/queue/workers/config/tenant. `feature_list.json` permanece intacto y
no hubo commit/push. T1–T16 quedan `[x]` para review independiente 4.

## Remediación review 4 y gates para review 5

### REV-1 — cursor workflow monotónico sin starvation

`validationworkflow.LocalStore.CleanupPending` devuelve como continuation el
último handle realmente escaneado aunque la página filtrada quede vacía. El
controller solo acepta ese avance con `Scanned=true`, recuerda todos los tokens
visitados durante el scan y rechaza repetición o retroceso. El store aplica un
segundo guard durable para `workflow-intents`: un cursor nuevo debe ser
estrictamente mayor que el high-watermark actual. Al terminar el conjunto no se
borra el cursor, por lo que restart no puede rebobinarlo.

Evidencia:

- `TestIntentPaginationRejectsEmptyContinuationAndCycles`: A→B→A dentro del
  mismo scan, página vacía intermedia marcada como escaneada, intent previo al
  ciclo no hambriento y A→B→A repartido entre tres instancias/reopens con
  `MaxPages=1`.
- `TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart`:
  primera página activa, segunda terminal, tercer ciclo vacío; prueba cursor
  durable y ausencia de starvation sobre el store productivo.

### REV-2 — roll-forward anchor/head y wrappers estrictos

`publishDurableSnapshot` sincroniza receipt, directorio, anchor y finalmente
head. Los hooks `after_anchor` y `before_head` modelan pérdida de respuesta en
la única frontera commit/head. Bajo el file lock compartido,
`loadDurableSnapshot` valida que el anchor seleccione exactamente un receipt,
recorre toda la cadena hasta un genesis con `PreviousDigest` vacío y publica de
forma idempotente el head ausente o atrasado. Un head adelantado, digest
divergente, receipt/cadena ausente o legacy no anclado sigue fallando integrity.

Head, receipt y anchor se decodifican con `DisallowUnknownFields`, EOF estricto,
rechazo de `null` y recorrido de tokens que rechaza claves duplicadas incluso
dentro de objetos anidados. La recuperación conserva fsync de archivo y
directorio y todo el I/O ocurre sin mutex Go, bajo el file lock cancelable.

Evidencia:

- `TestAnchorAheadRecoveryConvergesAcrossTwoInstances`: `after_anchor` y
  `before_head` sobre cleanup store, capacity ledger y autoridad Kubernetes;
  la segunda instancia y el reopen observan N+1 sin pérdida.
- `TestDurableHeadsDefineCrashOutcomes`: matriz live+reopen de todas las
  fronteras de receipt/anchor/head para store y ledger.
- `TestDurableHeadLossRecoversAcrossStoreLedgerAndKubernetesInstances`:
  reconstrucción de head confirmado ausente en dos instancias de cada backend.
- `TestDurableWrappersUseStrictDecode`: matriz head/receipt/anchor ×
  unknown/duplicate/null/trailing.
- `TestKubernetesPrivateAuthorityCoordinatesInstancesAndCrashBoundaries`:
  autoridad UID privada recuperable live+reopen en las nuevas fronteras.

### Cierre de tareas y requirements

T2/T3/T7/T12/T14 se reabrieron y cerraron únicamente después de los focales;
T16 se cerró después de todos los gates. El mapa R1–R15 y las matrices de
review 1–3 anteriores permanecen aplicables. En particular, REV-1 refuerza
R3/R12/R14/R15 y REV-2 refuerza R1/R3/R8/R9/R12/R14/R15 sin cambiar verdict,
evidence, package o report.

### Comandos finales review 4

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos LF/CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| focales REV-1/REV-2 `-count=20` en cleanup y validationworkflow | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass |
| UCRT64 verticales capacity/composición y workflow cursor `-race -count=20` | pass |
| `go test -count=1 -p=1 '-coverprofile=coverage-feature34-review4.out' ./...` | pass |
| intersección exacta diff productivo/statement blocks | `1771/2081 = 85,10331571359923113887554061 %` |
| suma exacta del profile global | `37302/43329 = 86,09014747628609014747628609 %` |
| `go test -c` cmd/environment/cleanup/kubernetes × Windows/Linux/Darwin amd64 | pass 12/12 |
| binarios Linux amd64 reales en Alpine 3.22.2: cleanup ×20, environment, kubernetes y cmd | pass 4/4 |

El rootfs oficial Alpine 3.22.2 se volvió a validar con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
La única red externa del gate fue esa descarga autenticada del rootfs; los
tests no accedieron a red, cluster ni credenciales. Distro, rootfs, perfiles y
cross-builds fueron eliminados; no quedan procesos Go ni temporales de esta
review. No se amplió autoridad destructiva ni se adelantaron
PostgreSQL/queue/workers/config/tenant. `feature_list.json` permanece intacto y
no hubo commit/push. T1–T16 quedan `[x]` para review independiente 5.

## Remediación review 5 y gates para review 6

### REV-1 — pasadas workflow durables y reevaluables

El cursor de workflow ya no es un high-watermark permanente. `CursorPass`
separa la epoch durable de su continuation: `SaveCursorPass` acepta únicamente
un token lexicográficamente mayor dentro de la misma epoch o un completion
exacto hacia `epoch+1` con cursor vacío. Ambas transiciones usan el mismo
receipt/head CAS del cleanup store; un stale writer, rewind, salto de epoch o
reset sin completion falla cerrado.

`validationworkflow.LocalStore.CleanupPending` calcula si el conjunto posterior
al cursor quedó exhaustivamente escaneado antes de truncar a `limit`. Una página
intermedia, aunque quede filtrada, publica `Scanned=true`, continuation no vacía
y `Complete=false`. El EOF inequívoco publica `Complete=true` y `Next=""`.
Después de reservar todos sus intents, el controller confirma completion y
crea atómicamente la siguiente epoch desde el inicio. Un crash anterior repite
la página idempotentemente; una pérdida de respuesta posterior reabre la epoch
nueva ya confirmada.

Evidencia principal:

- `TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart`
  usa el store productivo con `PageSize=1/MaxPages=1`: filtra `active-a`,
  descubre `terminal-b`, completa la primera pasada, terminaliza después
  `active-a`, crea `aaa-new` por debajo del watermark anterior y descubre ambos
  durante la segunda pasada, reabriendo entre todas las páginas.
- `TestIntentPaginationRejectsEmptyContinuationAndCycles` conserva la matriz
  A→B→A dentro de un scan y repartida entre reopens, incluida página filtrada
  vacía, sin saltar trabajo reservado antes del ciclo.
- `TestIntentPassCompletionCrashConverges` inyecta `before_write` y
  `after_publish` alrededor del completion y demuestra estado live/reopen,
  progreso posterior, ausencia de pérdida y una sola epoch autoritativa.
- `TestCursorPassRequiresMonotonicEpochTransitions` cubre advance, completion,
  rewind, stale CAS y epoch skip.
- `TestCleanupPendingScanIsBoundedAndDoesNotBlockOtherRuns` mantiene límite
  aplicado en store y progreso concurrente mientras una carga está bloqueada.

T5/T7 se reabrieron y cerraron después de los focales. T16 se cerró solo tras
repetir todos los gates; T13 permaneció sin cambios porque el loop periódico ya
invoca cada nueva pasada mediante `RunOnce` y sus gates/races siguieron verdes.
El protocolo refuerza R2/R3/R12/R15 sin tocar anchor/head ni las autoridades de
capacity/Kubernetes ya aceptadas.

### Comandos finales review 5

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos LF/CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| focales epoch/cycle/crash/vertical workflow `-count=20` | pass |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 `go test -race -count=20 ./internal/environment/cleanup` | pass |
| UCRT64 focales epoch/cycle/crash y vertical workflow `-race -count=20` | pass |
| UCRT64 verticales capacity/composición `-race -count=20` | pass |
| `go test -count=1 -p=1 '-coverprofile=coverage-feature34-review5.out' ./...` | pass |
| intersección exacta diff productivo/statement blocks | `1812/2130 = 85,07042253521126760563380282 %` |
| suma exacta del profile global | `37344/43378 = 86,08972290100972843376826963 %` |
| `go test -c` cmd/environment/cleanup/kubernetes × Windows/Linux/Darwin amd64 | pass 12/12 |
| binarios Linux amd64 reales en Alpine 3.22.2: cleanup ×20, environment, kubernetes y cmd | pass 4/4 |

El minirootfs oficial volvió a validarse con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
La única red externa fue su descarga autenticada; los tests no usaron red,
cluster ni credenciales. Distro, rootfs, profiles y cross-builds se eliminaron;
no quedan procesos ni temporales. `feature_list.json` permanece intacto, no se
adelantó alcance de features 35–37 y no hubo commit/push. T1–T16 quedan `[x]`
para review independiente 6.
