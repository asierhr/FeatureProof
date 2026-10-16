# Implementación — bounded-replanning

## Resultado

Se añadió `internal/replanning` como coordinador secuencial posterior a un lote inicial confirmado. Construye un resumen JSON saneado y acotado, reserva presupuestos antes del trabajo, acepta solo las tres decisiones v1, prepara los checks adicionales mediante `agentplanning`, solicita una revisión nueva cuando procede, ejecuta el lote por el puerto del executor, confirma assertions en un ledger monotónico y registra cada transición en un journal CAS proyectable. La terminación usa exclusivamente resultados deterministas y conserva la evidencia inicial ante rechazo, agotamiento o cancelación.

La preparación adicional factoriza `agentplanning.PrepareAdditional` y `BatchFingerprint`; los checks pasan por resolución de operación OpenAPI, registry, assertions, policy, plan y provenance antes de obtener un `PreparedPlan`. `planapproval.ReopenForReplan` permite otra revisión tras consumir el grant anterior; el grant inicial no autoriza el nuevo lote. Los eventos de agotamiento registran dimensión, consumo y límite no monetarios.

## Tasks

- T1–T2: límites y protocolo cerrado; resumen estable, saneado y ligado a evidencia del run.
- T3–T4: journal/proyector CAS, ledger, fingerprint semántico, preparación ordinaria y revisión fresca.
- T5–T6: admisión previa de presupuestos y veredicto monotónico.
- T7–T8: coordinador secuencial; pruebas herméticas end-to-end, adversariales, de cancelación y concurrencia.
- T9: gates completos, cobertura y trazabilidad R1–R12 verificadas.

## Trazabilidad

| Requirement | Tests concretos y resultado |
|---|---|
| R1 | `TestSummaryIsExactStableRedactedAndBounded`, `TestSummaryAppliesModelOnlyPaths`, `TestSummaryRejectsUnsafeContextAndNestedEvidence`, `TestCoordinatorSummaryFailureStartsNoInference`: pass. |
| R2 | `TestDecisionProtocolAcceptsExactlyThreeExclusiveBranches`, `TestDecisionProtocolRejectsAdversarialShapes`, `TestCoordinatorMalformedDecisionStartsNoDownstreamWork`: pass. |
| R3 | `TestAdditionalBatchReusesAllPlanningGates`, `TestInvalidOrDeniedAdditionalBatchExecutesNothing`, `TestBoundedReplanningEndToEndWithFakes`: pass. |
| R4 | `TestEveryBudgetIsReservedBeforeItsWork`, `TestConcurrentLastDeterministicUnitHasOneWinner`, `TestConcurrentLastReplanHasOneWinner`, `TestModelBudgetDimensionsBlockBeforeInference`: pass. |
| R5 | `TestExhaustionStartsNoWorkAndKeepsFailedPrecedence`, `TestExhaustionWithoutEvidenceIsInconclusive`, `TestBudgetAuditUsesRequestedDimension`: pass. |
| R6 | `TestLedgerReplayIsIdempotentAndConflictFailsClosed`, `TestVerdictUsesOnlyCompleteAssertionLedger`, `TestModelClaimCannotOverrideDeterministicResult`: pass. |
| R7 | `TestEquivalentAdditionalBatchesAreRejectedBeforeApprovalAndTools`, `TestEquivalentProposalStopsBeforeSecondApprovalAndTool`: pass. |
| R8 | `TestLineageReconstructsDecisionEvidenceAndActions`, `TestJournalRejectsCorruptLineage`, `TestAmbiguousCancelledAppendRecoversConfirmedPrefix`, `TestToolLineageRejectsForeignAndUnsafeEvidence`: pass. |
| R9 | `TestAdditionalBatchRequiresFreshApproval`, `TestWorkflowApprovalStagesFreshRevision`, `TestApprovalReplanReturnsThroughPreparation`: pass. |
| R10 | `TestConcurrentCycleHasOneConfirmedWinner`, `TestConcurrentCycleHasOneModelCall`, `TestCancelledInferenceWritesOneTerminal`, `TestCancellationStopsNextWorkAndPreservesPrefix`: pass; race completa y repetida pasan. |
| R11 | `TestModelClaimCannotOverrideDeterministicResult`, `TestVerdictUsesOnlyCompleteAssertionLedger`, `TestExactNodeVerdictRequiresConfirmedEvidence`, `TestBoundedReplanningEndToEndWithFakes`: pass. |
| R12 | Todos los anteriores son fakes herméticos sin red/proveedor/credenciales; formato, vet, suite, coverage y race pasan. |

## Verificación

Go: `go1.25.1`, Windows/amd64. Gates ejecutados en el checkout compartido:

- `gofmt -w internal/replanning` y archivos Go nuevos/modificados correspondientes: pass.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -count=1 -coverprofile rp.cover.out ./...`: pass; global **89,6 %**.
- Coverage: `internal/replanning` **85,2 %**, `internal/agentplanning` **88,0 %**, `internal/planapproval` **86,8 %**, `internal/evidence` **89,8 %**.
- `go mod verify`: pass.
- `git diff --check`: pass; solo avisos LF/CRLF del checkout para archivos existentes.
- `GOOS=windows/linux/darwin GOARCH=amd64 go build ./...`: pass para los tres targets.
- `go test -race ./...`: pass con `CGO_ENABLED=1`, `CC` y `PATH` limitados al proceso PowerShell, usando MinGW portable verificado en `C:\Users\asier.haro\Documents\Codex\2026-10-15\c-users-asier-haro-desktop-proyectos\work\toolchain\winlibs\mingw64\bin`.
- `go test -race -count=20 ./internal/replanning ./internal/agentplanning ./internal/planapproval ./internal/evidence`: pass.

## Adaptaciones mínimas

- `internal/agentplanning`: dos archivos productivos nuevos para materializar un lote ya decodificado y reutilizar la canonicalización de fingerprint; los gates previos permanecen activos.
- `internal/evidence/redaction.go`: `SanitizeModel` aplica también `ModelSensitiveJSONPaths` a los valores de assertions antes de construir el resumen para el modelo.
- `internal/planapproval/model.go`: tipo de evento `replan_ready` y revisión de estado.
- `internal/planapproval/projector.go`: transición validada de grant consumido a planificación para una nueva revisión.
- `internal/planapproval/workflow.go`: operación explícita `ReopenForReplan` con CAS y staging nuevo. Ningún archivo adicional existente de ese paquete fue modificado.
- `progress/current.md` y `specs/bounded-replanning/tasks.md`: estado y evidencia incremental de T1–T8.

## Estado solicitado

T1–T9 están completas. La feature permanece `in_progress`. No se modificó `progress/history.md` ni se marcó `done`. La primera review independiente quedó registrada en `review.md`; se solicita una nueva revisión.

## Correcciones de la primera review

- REV-1: solo la raíz de cada declaración es `Required`; los hijos permanecen auditables y `any(passed, failed)` termina `passed`. Regresiones `TestAnyGroupUsesRootVerdictPreservingFailedChild` y `TestCoordinatorAnyGroupUsesEvaluatedRoot`.
- REV-2: cada visita de canonicalización reserva `validation_work` antes de procesarla. El coordinador comprueba agotamiento antes de invocar fingerprint. Regresiones `TestFingerprintChargesEachVisitBeforeCanonicalization` y `TestExhaustedValidationBudgetSkipsFingerprint`.
- REV-3: checkpoints tipados guardan resumen, decisión, fingerprint, resultados seguros, assertions y consumo. El retry reanuda prefijos confirmados sin repetir inferencia ni tools; un `batch_started` sin resultados confirmados cierra con error operativo. Regresiones `TestCoordinatorResumesConfirmedPrefixWithoutRepeatingModelOrTools`, `TestCoordinatorResumesPreparedAndApprovalPrefixes`, `TestCheckpointedToolResultsCloseStartedBatchWithoutReplay`, `TestUncertainStartedBatchFailsClosedWithoutToolReplay`, `TestRecoveredBudgetPreventsDuplicateWork` y `TestConcurrentCycleHasOneModelCall` repetida 30 veces.
- REV-4: eventos `initial_assertions` y `assertions_confirmed` registran identidades SHA-256, facts reales y declaraciones requeridas. `Project` valida identidad, evidencia de lote y veredicto contra un ledger reconstruido. `LocalJournal` conserva eventos y checkpoint por run mediante publicación atómica. Regresiones `TestFreshCoordinatorRehydratesLedgerAfterCompletedBatch`, `TestLocalJournalRehydratesCompletedBatchAcrossHandles`, `TestLocalJournalRejectsStaleAndCorruptRecovery` y `TestProjectedRealRunRejectsAssertionTampering`.

Las regresiones REV-1 y REV-2 fallaron antes de la corrección. Las de REV-3/4 se añadieron contra las fronteras de fallo señaladas; el código previo rechazaba cualquier prefijo no terminal o emitía eventos sin facts. `TestRecoveryCheckpointSanitizesNestedAssertionValues` confirma que el checkpoint retiene solo valores seguros para el modelo.

## Formato literal

`gofmt -l .` sigue mostrando archivos Go preexistentes fuera del diff de feature 13. Ningún archivo Go nuevo o modificado de esta feature figura en la salida de `gofmt -l` dirigida a `internal/replanning`, `internal/agentplanning/{batch_fingerprint,prepare_additional,prepare_additional_test,validation_work}.go`, `internal/evidence/redaction.go` e `internal/planapproval/{model,projector,workflow,replan_revision_test}.go`. Se preservó el baseline de formato ajeno.

## Correcciones de la segunda review

- REV-5: `BudgetController.Restore` repone siempre los contadores deterministas. Para el modelo, reconcilia el checkpoint con el snapshot de una sesión conservada, toma el mayor valor confirmado como base y cuenta solo incrementos posteriores. `TestRestoreWithExistingModelConsumption` comprueba límites de tools y ausencia de doble cómputo.
- REV-6: tras `approval_resolved`, el coordinador llama a `ApprovalRecovery` y evita `Wait`. `WorkflowApproval.Recover` reconstruye un grant desde la revisión aprobada, todavía no consumida, con hashes de plan y contrato revalidados. El projector reinicia `Consumed` al abrir cada revisión nueva. `TestCoordinatorRecoversResolvedWorkflowApprovalWithoutSecondHumanDecision` interrumpe después del evento confirmado y reanuda con un bridge nuevo cuyo waiter falla si se invoca; `TestWorkflowApprovalStagesFreshRevision` también rechaza un segundo recovery tras consumir el grant.
- REV-7: `LocalJournal` reserva la transacción por run bajo una sección crítica corta, libera el mutex antes de `Stat`, `ReadFile`, `CreateTemp`, `Write`, `Sync` y `Rename`, y despierta a los waiters tras publicar. La reserva conserva CAS entre handles y admite cancelación de un waiter. `TestLocalJournalDiskWaitDoesNotHoldStateLock` bloquea load y publish, comprueba `TryLock` del estado y la independencia de otro run.

La suite completa, `go vet`, módulos, diff, builds Windows/Linux/macOS y `go test -race ./...` pasan tras estas correcciones. La repetición race ×20 de los cuatro paquetes afectados se volvió a ejecutar. El `review.md` de ambas revisiones permanece intacto.

## Corrección de la tercera review

- REV-8: `BudgetController.Restore` marca una reserva de restauración con el mutex breve, invoca `ModelBudget.Snapshot` después de liberarlo y reconcilia el checkpoint al volver a la sección crítica. Una reserva o segunda restauración concurrente recibe un error inmediato y no consume trabajo. Un snapshot de sesión inválido deja intactos los contadores deterministas. `TestRestoreDoesNotHoldBudgetMutexDuringModelSnapshot` bloquea el callback de la sesión, comprueba que el mutex queda disponible, que ambas operaciones responden sin esperar al callback y que los contadores confirmados aparecen completos al terminar; `TestRecoveredBudgetPreventsDuplicateWork` cubre el rechazo atómico del snapshot inválido.

La suite, compilación, vet, módulos, diff, builds Windows/Linux/macOS y race completa pasan tras REV-8. `gofmt -l` de todos los archivos Go modificados o nuevos no emite rutas; el gate literal de todo el repositorio conserva el baseline ajeno. La repetición race ×20 volvió a ejecutarse sobre los cuatro paquetes afectados.

## Corrección de la cuarta review

- REV-9: se añadieron comentarios sistémicos propios a `localJournalOwner`, `forbiddenApprovalWaiter.Wait`, `failBeforeEventJournal.CompareAndAppend`, `blockingBudgetSnapshot.ReserveReplan` y `blockingBudgetSnapshot.Snapshot`. Un recorrido de todas las declaraciones top-level `type` y `func` de los archivos Go nuevos o modificados no encontró otras omisiones. Solo cambiaron comentarios.

Tras REV-9 pasan `gofmt -l` dirigido a los Go de la feature, `go test ./... -count=1`, `go vet ./...`, `go mod verify`, `git diff --check` y `go test -race ./internal/replanning`. La race completa y repetida, cobertura y builds multiplataforma de la cuarta review permanecen como evidencia funcional de la misma implementación.
