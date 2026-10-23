# Implementación — Feature 27: Validación de flujos funcionales reales

## Estado

`ready_for_review`

T1–T16 se ejecutaron desde la spec aprobada en `b81cee0` y se reabrieron tras
seis revisiones independientes. Esta entrega remedia REV-1..REV-21 en la rama
`feature-27-functional-flow-validation`. `feature_list.json` permanece intacto.

## Vertical resultante

- `FunctionalFlow/v1` es un contrato estricto, versionado e inmutable. Los
  orígenes generated y provided convergen en normalización, validación global,
  binding exacto y `FlowArtifact/v1` seguro.
- `validationworkflow.Service.Start` compone, valida, bindea y persiste el
  artifact exacto antes de approval, `Fixtures.Prepare` o cualquier tool. La
  ejecución recarga el snapshot admitido y vuelve a verificar artifact↔flow.
- El executor secuencial aplica claim, referencias privadas, binding, policy,
  budgets, identidad, tool, redacción, evidence y assertions. Los acts guardan
  intent/effect/evidence/terminal y un intent incierto no reconciliable no se
  repite. La lease cubre el peor caso completo de intentos, timeouts, backoff y
  confirmación, incluso durante el último intento concurrente. Ownership se
  renueva durablemente antes de evidence, journal, progreso del act, assertions
  y terminal bajo un `ConfirmationTimeout` efectivo.
- Comparaciones y fallos funcionales gobiernan el ledger. `RequiredEvidence`
  tiene una única semántica canónica global y un required failed,
  inconclusive, unknown o missing impide `passed`.
- El journal durable append-only conserva admission, prepare, claims, intents,
  attempts, retries, evidence, assertions, comparisons, recovery, primary y
  cleanup mediante digests encadenados. El package consume ese journal real,
  manifiesta evidence autenticada y valida ownership, bindings, enlaces y la
  máquina causal artifact→prepare→steps→primary→cleanup. Se sella después de
  cleanup y recovery actualiza ese mismo package sin cambiar el primary.
- Un terminal posterior a admission pero anterior a prepare persiste
  `fixtures_terminal/rejected`; reject, cancel y auto-denial no fabrican steps,
  evidence ni trabajo de fixtures y aun así producen un package post-cleanup
  verificable y recuperable.
- La composición productiva local usa stores durables para artifacts, estado,
  evidence y packages. CLI, API, MCP y GitHub llegan al mismo Service para
  start/status/cancel/result; CLI admite ruta local y las superficies remotas
  solo alias/artifact autorizado.
- La approval sella la proyección segura y digest del `FlowArtifact` exacto.
  Los flujos provided no invocan generación agentica y `Execute` consume el
  mismo artifact aprobado, por lo que una aprobación de A nunca autoriza B.
- `RevisionSet` sella assertions y requirements, liga cada revisión al digest
  del resultado previo y ya forma parte de la ejecución productiva para validar
  el resultado inicial. El producto mantiene `MaxReplans=0`; por tanto no pide
  revisiones autónomas. La conducta condicional cuando replanning está
  habilitado queda cerrada y probada en el componente de revisiones.

## Remediación de la primera revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-1 | Comparaciones tienen IDs/ownership canónicos, pertenecen al required proof y controlan la agregación: `TestRequiredComparisonControlsEndToEndVerdict`. |
| REV-2 | `StepFailed` contribuye siempre como failed: `TestFunctionalStepFailureControlsVerdict`. |
| REV-3 | Checkpoints intent/effect/evidence/terminal, replay conservador, key solo con garantía y lease que cubre I/O/confirmación: `TestActionCrashMatrixRecoversWithoutUnsafeReplay`, `TestLeaseMustCoverStepAndConfirmationBoundaries`, `TestConcurrentOwnersAndRedeliveryDoNotDuplicateEffects`. |
| REV-4 | `Admit` ocurre y persiste antes de approval/fixtures; cualquier fallo deja cero fixtures/tools: `TestFlowAdmissionFailureCreatesNoFixtureOrBusinessEffect`, `TestFixturesPrepareBeforeAnyBusinessStep`. |
| REV-5 | Composición productiva compartida y paridad de entradas reales, aliases, restart/redelivery y credenciales rotadas: `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces`, `TestProductWorkflowMCPEntrypointComposesFunctionalAliases`, `TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial`. |
| REV-6 | Package con evidence manifests autenticados por digest/ownership y causalidad completa: `TestFlowPackageReconstructsFullOrderedLifecycle`, `TestSafeProjectionAndPackageTamperingFailClosed`, `TestMissingForkedOrForeignEvidenceFailsClosed`. |
| REV-7 | Primary staged inmutable y finalización owned posterior a cleanup, idempotente en recovery: `TestLocalFunctionalFlowArtifactsAndPackageConvergeAfterCleanup`, `TestCleanupRecoveryConvergesSeparately`. |
| REV-8 | Digests por assertion/requirement, prefijo canónico y enlace al resultado previo: `TestRevisionRejectsAssertionDefinitionRewrite`, `TestReplanCannotMutateFixturesAuthorityOrConfirmedResults`. |
| REV-9 | `ValidateArtifactAgainst` revalida operación, identidad, efecto, retry e inputs al admitir y recuperar: `TestArtifactCannotResealDifferentOperationIdentityEffectOrRetryAuthority`, `TestFunctionalFlowExecutionAdapterUsesApprovedMaterial`. |
| REV-10 | Required evidence global rechaza nil/duplicado/unknown/missing y se usa en validación, ledger y package: `TestGlobalRequiredEvidenceIsCanonicalAndControlsVerdict`, `TestFlowVerdictRequiresAllRequiredEvidence`. |

## Remediación de la segunda revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-11 | Approval view/context/grant sella el digest y la proyección segura del artifact exacto; provided evita generación y ejecución verifica el mismo binding: `TestProvidedFlowNeverInvokesModelGeneration`, `TestFunctionalFlowExecutionAdapterUsesApprovedMaterial`. |
| REV-12 | Toda operación lleva identidad explícita o autoridad anonymous/public declarada; method/path/target/capability se derivan del binding y cualquier drift se rechaza antes de red: `TestFunctionalToolRejectsOperationDriftAndSeparatesConcreteResources` y suites de `testidentity`/HTTP. |
| REV-13 | El selector privado resuelto produce un resource digest canónico; recursos distintos de la misma operación no son comparables: `TestFunctionalToolRejectsOperationDriftAndSeparatesConcreteResources`, `TestComparisonRequiresTwoCompatibleConfirmedEvidences`. |
| REV-14 | Assertions se identifican por `(stepID,declarationID)` y required evidence, ledger y package conservan ese namespace: `TestRepeatedLocalAssertionIDsRetainStepOwnership`. |
| REV-15 | El store persiste un journal append-only real con hash-chain y el package valida su causalidad, bindings y orden topológico: `TestDurableJournalReconstructsRealExecution`, `TestEventJournalRejectsInvalidBindingsAndConvergesDuplicateAppend`. |
| REV-16 | La lease cubre todos los retries, timeouts, backoff y confirmación; barreras controladas prueban que ningún owner reclama durante el último intento: `TestLeaseMustCoverAllRetriesBackoffAndConfirmation`, `TestFinalAttemptLeaseCannotBeReclaimedBySameOrDifferentOwner`. |
| REV-3/5/6 | Se cierran los gaps parciales con checkpoint durable conservador, composición productiva común incluida la lectura de Result en GitHub, journal/evidence reales y validación integral del package. |

## Remediación de la tercera revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-17 | `RecordPreparation`, `Finalize` y `VerifyPackage` comparten una unión cerrada que incluye `rejected`; los terminales pre-prepare publican cuatro eventos sin trabajo sintético y convergen tras recovery: `TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork`, `TestRejectedResultParityAcrossFourSurfaces`, `TestRejectedPreparationPackageUsesClosedStatusAndNoSyntheticSteps`. |
| REV-18 | `RenewClaim` conserva owner/token de forma durable y cada frontera post-tool renueva bajo `ConfirmationTimeout`; reloj y barreras prueban no-reclaim del mismo u otro owner y fallo cerrado recuperable: `TestPostToolOwnershipRenewsAcrossEvidenceAssertionsAndTerminal`, `TestPostToolRenewalFailureStopsBeforeEvidenceAndAllowsSafeObserveRecovery`, `TestEveryPostToolRenewalFailureFailsClosed`, `TestLocalStoreRenewClaimExtendsOnlyTheCurrentUnexpiredOwner`. |

## Remediación de la cuarta revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-19 | `Service` persiste por separado ejecución funcional pendiente y primary durable, incluida la referencia segura de approval ya consumida. Un error recuperable conserva `PhaseExecuting` sin cleanup/package; `Recover` recompone y revalida el mismo artifact/material y reanuda checkpoints con otro owner. Solo un primary staged habilita cleanup y finalización. La matriz vertical cubre claim, intent, Evidence.Confirm, journal, progreso del act, assertions y terminal, restart/redelivery, cero segundo efecto simulado, cancelación, paridad API/CLI/MCP/GitHub y fallo no recuperable terminal: `TestServiceRecoversEveryPostToolBoundaryBeforeCleanup`, `TestCancelResumesPendingFlowToACancelledPrimary`, `TestServiceTerminalizesUnrecoverableFunctionalFailureWithDurablePrimary`, `TestFunctionalRuntimeErrorClassificationClosesRecoveryVocabulary`. |

## Remediación de la quinta revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-20 | `Executor.RecoverCompleted` valida el journal autenticado completo, reconstruye Result/ledger/comparisons desde state+evidence y promueve el primary sin entrar en `Execute`; `TerminalReconciler` recupera por CAS state perdido probado por el journal. El executor parcial consulta terminales ya journaled para no duplicar `recovery_resumed`/`step_terminal`. Crashes antes/después de `Publisher.Stage`, otro owner/restart y redelivery concurrente conservan Execute=1/tool=1 y package/Result paritario; journal alterado falla cerrado y un crash parcial sí reanuda: `TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay`, `TestCompletedJournalReconcilesMissingStateWithoutExecutorReplay`, `TestCompletedJournalTamperingFailsClosedBeforeStageOrCleanup`, `TestPartialPrimaryCrashResumesRealExecutor`, `TestDurableJournalReconstructsRealExecution`. |

## Remediación de la sexta revisión

| Hallazgo | Corrección y evidencia principal |
|---|---|
| REV-21 | `RecoverCompleted` construye una proyección de verificación y delega en el mismo `BuildPackage`/`VerifyPackage` que gobierna la publicación final antes de reconciliar state, Stage o cleanup. Cada `evidence_confirmed` liga ID, binding de operación, binding de recurso, content digest y digest del `EvidenceRecord`; assertions, comparisons, terminal/state y primary deben coincidir con el ledger y el manifest real. Journals reencadenados y autenticados con evidence ID, assertion ref, record digest o terminal divergentes, y stores reales con binding/content/step/revision divergentes, fallan dejando el workflow pending, Execute=1, tool=1, Stage durable=0 y cleanup=0: `TestAuthenticatedSemanticDivergenceFailsBeforeStage`, `TestDurableJournalReconstructsRealExecution`. Las pruebas nominales `TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay` y `TestCompletedJournalReconcilesMissingStateWithoutExecutorReplay` preservan promoción concurrente y CAS. |

## Trazabilidad R1–R15

| Requirement | Pruebas principales |
|---|---|
| R1 | `TestGeneratedAndProvidedFlowsNormalizeToSameCanonicalGraph`; `TestFlowContractIsStrictImmutableAndBackwardCompatible`; `TestGeneratedSnapshotCoversCanonicalInputUnion` |
| R2 | `TestFlowStepsRequireExactConstraintOperationTargetAndIdentity`; `TestObserveActClassificationCannotLieAboutEffects`; `TestArtifactCannotResealDifferentOperationIdentityEffectOrRetryAuthority` |
| R3 | `TestFlowGraphOrderAndTypedReferencesAreStable`; `TestInvalidGraphOrReferenceProducesZeroEffects`; `TestValidationRejectsEveryClosedUnionAndLimitBoundary` |
| R4 | `TestFlowAdmissionFailureCreatesNoFixtureOrBusinessEffect`; `TestFixturesPrepareBeforeAnyBusinessStep`; `TestPreparationFailureBlocksFlowAndStillCleans` |
| R5 | `TestEveryStepRechecksControlsInRequiredOrder`; `TestPerStepDenialAndExhaustionOpenNoNetwork`; `TestObserveTransportFailureIsOperationallyInconclusive` |
| R6 | `TestBeforeAfterObservationsProveMutationAndNoMutation`; `TestComparisonRequiresTwoCompatibleConfirmedEvidences`; `TestRequiredComparisonControlsEndToEndVerdict`; `TestFunctionalToolRejectsOperationDriftAndSeparatesConcreteResources` |
| R7 | `TestFunctionalStepFailureControlsVerdict`; `TestGlobalRequiredEvidenceIsCanonicalAndControlsVerdict`; `TestLedgerDeduplicationAndConflictAreDeterministic` |
| R8 | `TestBlockedStepsRecordAllCausesAndIndependentBranchesContinue`; `TestCancellationStopsEveryUnstartedBranch` |
| R9 | `TestActionIntentPrecedesNetworkAndUncertainEffectsAreNotDuplicated`; `TestActionCrashMatrixRecoversWithoutUnsafeReplay`; `TestOnlyVerifiedTransientOperationsRetryWithStableKey`; `TestLeaseMustCoverAllRetriesBackoffAndConfirmation`; `TestPostToolOwnershipRenewsAcrossEvidenceAssertionsAndTerminal`; `TestServiceRecoversEveryPostToolBoundaryBeforeCleanup`; `TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay`; `TestPartialPrimaryCrashResumesRealExecutor` |
| R10 | `TestAdditionalRevisionIsAtomicApprovedAndMonotonic`; `TestReplanCannotMutateFixturesAuthorityOrConfirmedResults`; `TestRevisionRejectsAssertionDefinitionRewrite` |
| R11 | `TestCleanupRunsAfterEveryPrimaryOutcomeWithoutChangingVerdict`; `TestCleanupRecoveryConvergesSeparately`; `TestLocalFunctionalFlowArtifactsAndPackageConvergeAfterCleanup`; `TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork`; `TestCancelResumesPendingFlowToACancelledPrimary`; `TestAuthenticatedSemanticDivergenceFailsBeforeStage` |
| R12 | `TestFlowPackageReconstructsFullOrderedLifecycle`; `TestDurableJournalReconstructsRealExecution`; `TestRejectedPreparationPackageUsesClosedStatusAndNoSyntheticSteps`; `TestMissingForkedOrForeignEvidenceFailsClosed`; `TestSafeProjectionAndPackageTamperingFailClosed`; `TestServiceTerminalizesUnrecoverableFunctionalFailureWithDurablePrimary`; `TestCompletedJournalTamperingFailsClosedBeforeStageOrCleanup`; `TestAuthenticatedSemanticDivergenceFailsBeforeStage` |
| R13 | `TestConcurrentOwnersAndRedeliveryDoNotDuplicateEffects`; `TestFinalAttemptLeaseCannotBeReclaimedBySameOrDifferentOwner`; `TestPostToolOwnershipRenewsAcrossEvidenceAssertionsAndTerminal`; `TestPostToolRenewalFailureStopsBeforeEvidenceAndAllowsSafeObserveRecovery`; `TestServiceRecoversEveryPostToolBoundaryBeforeCleanup`; `TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay`; `TestCompletedJournalReconcilesMissingStateWithoutExecutorReplay`; `TestAuthenticatedSemanticDivergenceFailsBeforeStage`; `TestCrashAndCancellationMatrixConvergesAndJoins`; `TestLocalStoreConcurrentClaimsSelectOneOwner`; `TestLocalStoreRejectsInvalidAndCancelledTransitions` |
| R14 | `TestFlowCanariesNeverCrossDurableModelOrExternalBoundaries`; `TestFunctionalRuntimePrivateBoundariesAndConservativeFinalization`; `TestLocalProvidedFlowCatalogRestrictsAndAuthenticatesDocuments` |
| R15 | `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces` (incluye `Result` de GitHub); `TestServiceRecoversEveryPostToolBoundaryBeforeCleanup`; `TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay` (Result API/CLI/MCP/GitHub tras restart); `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces`; `TestProductWorkflowMCPEntrypointComposesFunctionalAliases`; `TestProvidedFlowNeverInvokesModelGeneration` |

## Gates reproducibles

| Gate | Comando | Resultado |
|---|---|---|
| Formato/diff | `gofmt -w ...` y `git diff --check` | pasa |
| Compile-only | `go test -run '^$' ./...` | pasa |
| Vet | `go vet ./...` | pasa |
| Focal repetida | `go test -count=3 ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof` | pasa |
| Suite global | `go test ./...` | pasa |
| Race focal/global | `CGO_ENABLED=1 go test -race ...` con `C:\msys64\ucrt64\bin` en `PATH` | pasa |
| Cross-build | `GOOS=linux/darwin GOARCH=amd64 CGO_ENABLED=0 go test -c` para los tres paquetes afectados | pasa |
| Coverage del diff | `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py` | **2674/3137 = 85,2407 %** |

El script de coverage calcula statements ejecutables nuevos/modificados desde
`b81cee0`, incluye archivos untracked y usa denominadores simétricos para los
helpers Windows/Unix. Los tests son herméticos: no usan sleeps, red externa,
OAuth, proveedor real ni credenciales.

## Límites comprobados

- Raw captures, credenciales, keys, inputs resueltos y errores externos no
  cruzan model, artifact, journal, package ni superficies.
- Las pruebas de producto usan un transporte controlado local; las pruebas
  reales contra proveedor o aplicación siguen siendo opt-in y separadas de
  `go test`.
- La replanificación autónoma permanece deshabilitada en la composición de
  producto (`MaxReplans=0`). No se simula una revisión adicional desde las
  superficies cuando esa autoridad no está habilitada.

T1–T16 quedan completas con la evidencia anterior y requieren nueva revisión
independiente antes de que el leader cambie el estado de la feature.
