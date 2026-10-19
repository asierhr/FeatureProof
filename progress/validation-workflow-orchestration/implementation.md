# Implementación de validation-workflow-orchestration

## Resultado

Feature 20 queda corregida para una quinta revisión. `internal/validationworkflow` es el caso de uso durable compartido por CLI, MCP, API Go y GitHub. Las correcciones conservan preview y aprobación entre procesos, commitments privados no correlacionables, snapshot ejecutable ligado al artefacto y contexto efectivo recompuesto, recovery por modo real y deadline durable de aprobación.

## Cierre final de REV-14

`BindingVerifier` y `DescriptorCatalog` ya no aceptan digests como sustituto de contenido: devuelven `BindingContext` y `RuntimeContext`, y `TrustedAdmissionBuilder` deriva los hashes internamente. La representación canónica liga source, target kind/alias/url, EnvironmentID, contenido y vencimiento de attestation, snapshot preview, manifest HTTP, redaction, registry, schema, versiones de validators, policy efectiva y reglas de red. `validateSnapshot` y `verifyExecutableContext` recalculan los tres digests desde esos objetos antes de Restore, Stage/Restage, approval o tools.

`TestAdmissionContextDriftIsRejectedAcrossOwners` cubre required y disabled/recovery cross-owner cambiando cada objeto con alias, profile y policy ID estables. `TestPlanReadyContextDriftNeverStagesApproval` demuestra que los mismos cambios no crean eventos de aprobación. `TestWorkflowEffectiveContextDigestsBindConfigurationBehindStableIDs` cubre las proyecciones usadas por la composición productiva.

## Cierre de REV-13 y REV-14

| Hallazgo | Corrección y evidencia |
|---|---|
| REV-13 | `ProjectPlans` solo emite commitments con una clave privada de 32 bytes; sin clave o con clave débil conserva únicamente `[REDACTED]`. `validate --plan` no publica hashes deterministas, mientras el workflow usa una clave aleatoria por run que permanece en memoria/store privado y nunca llega a `plan.json`. Tests: `TestProjectPlansWithoutPrivateKeyPublishesNoDictionaryOracle`, `TestHistoricalValidatePlanPackageHasNoCommitmentOracle`, `TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage`. |
| REV-14 | `canonicalContextDigest` liga spec, contrato y bytes OpenAPI reducidos, revisión, target/attestation, runtime/policy, provider/model, constraints, capacidades y límites. `restorePrepared` valida el snapshot recompuesto y compara ese digest contra artifact/material antes de policy, Stage/Restage, Consume o tools. Tests cross-owner required y disabled/recovery: `TestAdmissionContextDriftIsRejectedAcrossOwners`, `TestPlanReadyContextDriftNeverStagesApproval`. |

## Cierre de REV-6, REV-8..REV-12

| Hallazgo | Corrección y evidencia |
|---|---|
| REV-1 | `PlanApprovalBridge` usa `planapproval.ApprovalWorkflow`; stagea plan/contexto exactos, consume `ExecutionAuthorization` una vez, propaga referencia a `revisionbinding.PlanBinding` y package, y rechaza replay con mismo decision ID y payload distinto. Tests: `TestRequiredApprovalBindsExactCurrentHashesAndOneUseGrant`, `TestSeparateServiceOwnersApproveAndRejectDurableWork`. |
| REV-2 | Al entrar en `awaiting_approval` se libera owner; decide/reject/cancel reclaman por CAS. Dos `Service`, owners, stores y bridges separados prueban approve, reject y cancel inmediatos. |
| REV-3 | Worker y heartbeat comparten contexto, `Renew` conserva el CAS semántico, cancellation/renew failure detienen trabajo y `endWork` une heartbeat. `TestHeartbeatPreventsOverlapAndReclaimRequiresExpiry` usa reloj y waiter controlados. |
| REV-4 | Un único `<handle>.state.json` contiene record+journal, publicado por temp, file fsync, rename y directory sync. La reserva idempotente se reconstruye desde checkpoints. `TestCheckpointFailureInjectionNeverSplitsProjectionAndJournal` inyecta temp-sync, rename y directory-sync. Windows no expone fsync de directorio por `os.File`; el rename atómico y el checkpoint único se mantienen. |
| REV-5 | Se persisten `PackageDir`, package verificado, `CleanupIntentID`, `CleanupPending` y `DesiredOutcome`. No se publica terminal antes de confirmar release. Recovery reabre package/finalización y cleanup. Tests: `TestCleanupIntentRecoversBeforeTerminal`, `TestPreviewReleasePendingRecovers`. |
| REV-6 | `PreviewPort.Restore` y `previewenv.Coordinator.RestoreAcquired` reconstruyen digest, target y attestor desde el snapshot sellado durable sin reservar otra vez. Al quedar ready se libera la lease por CAS y `ReleaseAcquired` reclama ownership antes del release, permitiendo approve/cleanup inmediato desde otro proceso. `restorePrepared` exige el mismo digest del record y artefacto. Tests: `TestPreviewApprovalRestoresSealedAuthorizationAcrossOwners`, `TestRestoreAcquiredRebuildsAuthorizationAcrossCoordinators`. |
| REV-7 | `localvalidation.BuildHTTPPlanningCapability` deriva schema/validator/metadata del mismo HTTP tool ejecutable. `workflowGenerationPolicy` consulta la misma `PolicyFactory`; el runner conserva policy por tool y revalida binding/attestation entre batches. |
| REV-8 | `PlanProjection` conserva la redacción visual y añade HMAC-SHA256 con clave privada por valor y ubicación. Inputs, headers, referencias, assertions y expected distintos producen proyecciones y `ArtifactSHA256` distintos; package recibe la misma clave únicamente en memoria. Test: `TestProjectPlansCommitsEveryPrivateSemanticLeaf`. |
| REV-9 | `ExecutableMaterial` guarda snapshot y clave en la frontera privada, ligados a workflow, artefacto, contexto, revisión y target. Antes de consumir approval o ejecutar se revalida el plan y se compara toda su proyección comprometida; al terminal se retira el fichero privado. `TestPrivateExecutableTamperingIsRejectedBeforeExecution` cubre required y recovery con approval disabled. |
| REV-10 | Recovery de `PhasePlanReady` restaura el plan y usa `planapproval.Restage` idempotente con hashes exactos, o aplica la rama disabled sin fabricar actor humano. Tests con Service/store reconstruidos: `TestPlanReadyRecoveryRestagesOrPreservesDisabledMode`, `TestPlanReadyRecoveryReconcilesAlreadyStagedApprovalAcrossProcesses`. |
| REV-11 | `ApprovalDeadline` se persiste al confirmar awaiting approval. Decide y recovery evalúan el límite inclusivo con el reloj inyectado y convergen a cancelled con cleanup. Test cross-owner: `TestApprovalDeadlineExpiresDurablyAcrossOwners`. |
| REV-12 | `PlanApprovalBridge.workflow` recibe y propaga el contexto original a `RestoreRun`. `TestApprovalRestorePropagatesCancellation` demuestra cancelación coordinada. |

## Trazabilidad R1-R13

| Requirement | Tests principales |
|---|---|
| R1 | `TestStartRequiresEveryExplicitSelectionAndPreservesPreAdmissionBoundary`, `TestWorkflowCLICommandsAndExactStartParser` |
| R2 | `TestAdmissionValidatesBindingContractPolicyAndBudgetsBeforeModel`, `TestAcquireAndReleaseExposeSealedPreviewWithoutExecuting` |
| R3 | `TestAdmissionFailsClosedAtEveryBoundary`, tests de capability productiva y policy compartida |
| R4 | `TestInsufficientContextIsSafeDistinctTerminalWithoutPackageOrTools`, paridad CLI/API/MCP |
| R5 | `TestPlanArtifactIsCanonicalBoundAndTamperDetected`, `TestProjectPlansCommitsEveryPrivateSemanticLeaf`, `TestAdmissionContextDriftIsRejectedAcrossOwners` |
| R6 | `TestRequiredApprovalBindsExactCurrentHashesAndOneUseGrant`, `TestPlanReadyRecoveryRestagesOrPreservesDisabledMode` |
| R7 | `TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage`, `TestPlanReadyContextDriftNeverStagesApproval`, suite `localvalidation` |
| R8 | `TestPackageIsOnlyFunctionalResultAuthority`, comparación completa de `PlanProjection` y digest |
| R9 | `TestCheckpointFailureInjectionNeverSplitsProjectionAndJournal`, `TestPrivateExecutableTamperingIsRejectedBeforeExecution`, `TestAdmissionContextDriftIsRejectedAcrossOwners` |
| R10 | `TestApprovalDeadlineExpiresDurablyAcrossOwners`, `TestApprovalRestorePropagatesCancellation`, tests de cancel join |
| R11 | `TestArtifactAndJournalExcludeCanaries`, `TestProjectPlansWithoutPrivateKeyPublishesNoDictionaryOracle`, `TestHistoricalValidatePlanPackageHasNoCommitmentOracle` |
| R12 | `TestMCPServeExposesDiscoveryAndSharedStatus`, tests GitHub lineage, composición preview productiva |
| R13 | `TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols` y gates canónicos |

## Tasks

T1-T12 están completas en `specs/validation-workflow-orchestration/tasks.md`. No se modificó el estado de `feature_list.json`.

## Verificación

| Comando | Resultado |
|---|---|
| `go test ./internal/validationworkflow ./internal/previewenv ./internal/planapproval -count=10` | pass |
| `go test -race ./internal/validationworkflow ./internal/previewenv ./internal/planapproval -count=3` con GCC/UCRT64 | pass |
| race dirigida REV-6/8/9/10/11/12, incluyendo localvalidation, `-count=3` | pass |
| focales REV-13/14 en validationworkflow/localvalidation, `-count=10` | pass |
| race focal REV-13/14, `-count=3` con GCC/UCRT64 | pass |
| drift de objetos efectivos con IDs estables, `-count=10` | pass |
| race de drift efectivo cross-owner y composición, `-count=3` | pass |
| race de todos los paquetes afectados | todos pass salvo el subtest preexistente de symlink Windows |
| `go test ./... -count=1` | todos pass salvo `internal/localvalidation/.../evidence_symlink`: privilegio Windows requerido |
| `go vet ./...` | pass |
| `go test ./... -run '^$'` | pass |
| `go mod verify`, `go mod tidy -diff` | pass, sin cambios |
| builds Windows, linux/amd64 y darwin/amd64 | pass |
| `git diff --check` | pass; solo avisos CRLF del checkout |

Coverage final: `internal/validationworkflow` 85,8 % y `internal/previewenv` 85,0 % statements. La única limitación ambiental reproducible es la creación de symlinks en Windows sin el privilegio correspondiente; el test no fue debilitado ni omitido.

## Archivos principales

- `internal/validationworkflow/service.go`: ownership, heartbeat, approval, ejecución, intents, recovery y cancel.
- `internal/validationworkflow/store.go`: checkpoint atómico record+journal, idempotencia, CAS y leases.
- `internal/validationworkflow/approval.go`: bridge a `planapproval` y grant opaco.
- `internal/validationworkflow/executable_store.go`: frontera privada de valores ejecutables.
- `internal/localvalidation/plan_projection.go`: proyección canónica saneada.
- `internal/localvalidation/planning_capability.go`: descriptor derivado del tool productivo.
- `internal/previewenv/lifecycle.go`: acquire/release sin duplicar ejecución.
- `cmd/featureproof/workflow.go`: composición OAuth, policy/runtime, preview, CLI/MCP y recovery.

Se solicita una quinta revisión independiente.
