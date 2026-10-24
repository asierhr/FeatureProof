# Implementación de context-assembly

## Alcance final

La feature añade `ContextSnapshot/v1` y `ContextBinding/v1`, perfiles sellados
para planner, analyzer y diagnostic, producers neutrales, filtrado estructural,
secret gate, packing exacto, persistencia inmutable y revalidación de drift.
`agentplanning` recibe instrucciones y envelope por canales separados;
`validationworkflow` conserva el snapshot ejecutable privado y revalida su
binding antes de approval, execution y recovery. CLI, API, MCP y GitHub pasan
por el mismo service y autoridad de composición.

No se obtiene contexto desde GitHub, no se adquieren environments y el diff
source permanece neutral. El harness y el modelo no reciben credenciales,
stores, handles, closures, evidence bodies ni capacidades ejecutables.

## Findings de review cerrados

REV-1–REV-11 siguen cerrados: composición asigna trust classes; instructions y
untrusted son canales distintos; el packing mide el envelope final; los
perfiles modelan required/optional/conflict/partial; allowlists y budgets se
aplican antes de materializar; redaction falla cerrado; journal y store son
canónicos/autenticados; provenance, consumers productivos y superficies reales
ligan el mismo contexto; recovery no reentra tras terminal.

| Finding | Cambio final y evidencia principal |
|---|---|
| REV-12 | `Assemble` deriva `min(caller deadline, profile duration)` y propaga el contexto a producer, filter, gate, ranker, counter, canonicalización, store y observer, comprobando después de cada frontera y join. `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary` bloquea cada fase. |
| REV-13 | El CAS del witness es el commit point. Antes del CAS cualquier fallo elimina target/temporales; después completa journal/receipt con contexto acotado y devuelve confirmación. `TestAtomicWriteBoundaryCancellationAndIdentitySwaps`, `TestWitnessCommitPointFinishesAfterCallerCancellation`, `TestWitnessConflictRollsBackEveryPrecommitFile` y `TestPostCommitReceiptRetriesReturnConfirmed` cubren link/remove/parent-sync y cancelación. |
| REV-14 | `LocalStore` exige el `projectmemory.MonotonicWitness` externo, composition-owned y no replayable. Witness, tail, receipt y object se verifican como un estado coordinado. `TestExternalWitnessRejectsCoordinatedRollbackAndRecoversConfirmedReceipt` y `TestConfirmedTailRecoveryRequiresExactObjectAndReceiptName` cubren rollback y reopen. |
| REV-15 | El sello canónico liga version, role, plan_id, instructions, configuration, untrusted y context digest. Provenance, `PreparedSnapshot`, artifact y recovery conservan los bytes/digests efectivos. `TestGeneratorRejectsContextEnvelopeSubstitution`, `TestPreparedPlanProvenanceBindsGenerationInputs` y `TestArtifactVerificationRequiresCanonicalContextSealFields` cubren sustitución coordinada, versiones/roles exactos y JSON duplicate/unknown/trailing/no canónico. |
| REV-16 | Composición posee authority y producer registries; `RoleContextRequest` solo lleva key/run/plan/role. Analyzer y diagnostic son consumers productivos. `TestProductRoleConsumerRejectsCallerAuthority`, `TestContextRoleConsumerAndPlannerAdapterFailClosed` y `TestProductAnalyzerAndDiagnosticUseSharedAssembly` prueban cross-role/source/authority con cero calls. |
| REV-17 | El inspector recursivo cubre token, credential y toda la taxonomía R14 en metadata y payload. `TestRedactionStateAndSensitiveTaxonomyFailClosed` prueba canarios anidados en todas las superficies. |
| REV-18 | Create/read/publish/remove operan relativos a handles de directorio pinned y no-follow. Linux publica el fd con `AT_EMPTY_PATH`; el fallback Unix autentica inode antes/después del link; Windows usa handles relativos. `TestStoreRejectsSubdirectorySwapDuringEveryPublicationBoundary`, `TestOpenTemporaryHandleDefeatsNameSwap`, `TestLinkNamedOpenFileAtAuthenticatesBothSides` y `TestLocalStoreRejectsCoordinatedRootReplacement` realizan swaps reales. |
| REV-19 | La auditoría de comentarios resuelve root mediante `FEATUREPROOF_SOURCE_ROOT` o cwd, sin rutas de `runtime.Caller`. Los cinco binarios instrumentados se ejecutaron realmente en Linux sobre el árbol final. |
| REV-20 | Cada operación emite exactamente un terminal record. Tras cancelar y esperar el trabajo propio, la finalización usa un contexto interno separado y acotado por `ObserverTimeout`, incluso cuando expiró el deadline de assembly; el fallo del observer permanece fail-closed. `TestTerminalObserverExactlyOnceForEveryOutcome`, `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary` y `TestObserverFailureFailsClosedAtEveryTerminalBoundary` cubren outcomes y fronteras. |
| REV-21 | Todo error de `CompareAndAdvance` se reconcilia mediante `MonotonicWitness.Read` con timeout separado: `next` confirma y completa receipt/journal, `expected` revierte el precommit y cualquier estado desconocido conserva el objeto recuperable y retorna `uncertain`. `TestWitnessCASReconciliationConvergesAcrossResponseLossRestartAndRedelivery` cubre respuesta CAS perdida, fallo anterior al CAS, lectura ambigua, restart y redelivery sin duplicar. |
| REV-22 | El snapshot persiste un `RegistryDescriptor` canónico sellado con IDs/versiones de profile, instructions, authority y sources. Un adapter nuevo reconstruye producers composition-owned desde ese descriptor y configuración durable; ya no existen registries activos en maps de proceso. La composición productiva exige `LocalStore` y witness durable. `TestContextAdapterRestartRebuildsDurableRegistry` y `TestComposeWorkflowCLIRequiresDurableContextWitness` prueban restart, drift, redelivery y rechazo de composición no durable. |
| REV-23 | `RegistryDescriptor` solo identifica y liga contratos de producer. Tras restart, `ProducerRegistry` resuelve esos IDs contra workflow/admission actuales y cada producer compara su binding actual; nunca se crea un `StaticProducer` desde un binding histórico. `TestContextAdapterRestartRebuildsDurableRegistry`, `TestProductRegistryReloadsCurrentAdmissionBeforeRevalidation` y `TestCurrentRegistryRejectsMissingAndVersionMismatchedProducers` cubren unchanged, drift real, missing, version y authority mismatch antes de efectos. |
| REV-24 | `LocalStore` no mantiene ningún lock durante filesystem, witness, timers o callbacks. Cada writer crea un candidato de journal no direccionable, compite mediante CAS y, si pierde la generación, espera fuera de locks la publicación confirmada antes de reintentar. `TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge` prueba progreso independiente y `TestConcurrentSameIdentityConverges` prueba una sola historia por identidad. |
| REV-25 | `NewLocalStore` recibe caller context y composición lo propaga a witness `Read`, `Bootstrap` y HTTP. Solo las reconciliaciones ambiguas post-CAS usan contextos detached acotados, como exige REV-21. `TestLocalStoreConstructionPropagatesCallerCancellationToWitnessRead` y los casos `caller-cancellation`/`bootstrap-cancellation` de `TestComposeWorkflowCLIRequiresDurableContextWitness` prueban retorno acotado y cierre del request sin goroutines pendientes. |
| REV-26 | Snapshot y binding solo son direccionables cuando un receipt autenticado corresponde a una entrada confirmada del journal; el candidato permanece en `staging` hasta CAS. `TestPrecommitObjectsRemainUnaddressable`, `TestWitnessCASReconciliationConvergesAcrossResponseLossRestartAndRedelivery`, `TestRestartDiscardsUnconfirmedStaging` y `TestRestartRejectsConfirmedCandidateWithoutObject` cubren antes/durante CAS, rollback y restart. |
| REV-27 | Las fronteras dueñas usan `context.Cause(ctx)`: preservan el sentinel mediante `Error.Unwrap`, no filtran su texto y separan `CategoryTimeout` de `CategoryCancelled`. `NewLocalStore` prioriza un contexto ya cancelado antes de validar layout; composición conserva causa/categoría en `Read` y `Bootstrap`. `TestLocalStoreRejectsAlreadyCancelledConstruction`, `TestLocalStoreConstructionClassifiesRealDeadline` y los casos HTTP de `TestComposeWorkflowCLIRequiresDurableContextWitness` cubren cancel cause y deadline real con `errors.Is`. |
| REV-28 | La intercalación de dos writers queda subsumida por la política final de retención de REV-29. `TestSharedDigestWritersPreserveWinnerObject` mantiene el caso original y lo amplía a un tercer contender para snapshot y binding. |
| REV-29 | Rollback, contención, fallo al crear staging y recovery solo retiran metadata no direccionable; nunca borran objetos content-addressed durante commit. Los objetos huérfanos permanecen fuera de receipts/journal y una GC posterior deberá partir de un snapshot autoritativo estable. `TestSharedDigestWritersPreserveWinnerObject` coordina tres writers: el primero alcanza retention, el segundo gana CAS y queda antes de journal/receipt, y el tercero publica staging del mismo digest antes de liberar al ganador. La matriz combinada con `TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge` pasa `-count=50` normal y `-race -count=20`. |

## Trazabilidad R1–R15

| Requirement | Tests reales |
|---|---|
| R1 | `TestStrictRequestProfilesAndCanonicalProjection`, `TestCompositionOwnsEveryTrustClass`, `TestAdapterBoundariesFailClosed` |
| R2 | `TestCompositionOwnsEveryTrustClass`, `TestAnalyzerAndDiagnosticConsumersReceiveDistinctLeastPrivilegeSnapshots`, `TestProductRoleConsumerRejectsCallerAuthority` |
| R3 | `TestAssemblySeparatesTrustFiltersBeforeRankingAndIsDeterministic`, `TestPreMaterializationWorkBoundsRejectOversizedSources`, `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary`, `TestCurrentRegistryRejectsMissingAndVersionMismatchedProducers` |
| R4 | `TestEnvelopeCarriesPlanAndTypedChannels`, `TestGeneratorUsesAssembledEnvelopeExclusively`, `TestProductAnalyzerAndDiagnosticUseSharedAssembly` |
| R5 | `TestProfileFieldsAndRequiredCompleteness`, `TestPreMaterializationWorkBoundsRejectOversizedSources`, `TestPackingReservesMandatoryGuardsBeforeReportingInsufficient` |
| R6 | `TestModelFacingCountsUseFinalEnvelope`, `TestGeneratorRejectsContextEnvelopeSubstitution` |
| R7 | `TestSecretsConflictsRankingAndTokenizerFailClosed`, `TestClosedBranchesAndStoreReceipts` |
| R8 | `TestProfileFieldsAndRequiredCompleteness`, `TestModelFacingCountsUseFinalEnvelope`, `TestBudgetsIdempotencyBindingDriftCancellationAndConsumer` |
| R9 | `TestEnvelopeCarriesPlanAndTypedChannels`, `TestPreparedPlanProvenanceBindsGenerationInputs`, `TestContextAssemblyBindingPersistsAndDriftBlocksApproval` |
| R10 | `TestBudgetsIdempotencyBindingDriftCancellationAndConsumer`, `TestContextAssemblyBindingPersistsAndDriftBlocksApproval`, `TestContextAdapterRestartRebuildsDurableRegistry`, `TestProductRegistryReloadsCurrentAdmissionBeforeRevalidation` |
| R11 | `TestJournalChainDetectsTamperingRollbackAndContentLeak`, `TestExternalWitnessRejectsCoordinatedRollbackAndRecoversConfirmedReceipt`, `TestWitnessCASReconciliationConvergesAcrossResponseLossRestartAndRedelivery`, `TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge`, `TestSharedDigestWritersPreserveWinnerObject`, `TestConcurrentSameIdentityConverges`, `TestPrecommitObjectsRemainUnaddressable`, `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces` |
| R12 | `TestGeneratorUsesAssembledEnvelopeExclusively`, `TestProductAnalyzerAndDiagnosticUseSharedAssembly`, `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces` |
| R13 | `TestConcurrentAssemblyConvergesUnderRace`, `TestPostCommitReceiptRetriesReturnConfirmed`, `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary`, `TestTerminalObserverExactlyOnceForEveryOutcome`, `TestLocalStoreConstructionPropagatesCallerCancellationToWitnessRead`, `TestLocalStoreRejectsAlreadyCancelledConstruction`, `TestLocalStoreConstructionClassifiesRealDeadline`, `TestSharedDigestWritersPreserveWinnerObject`, `TestRestartDiscardsUnconfirmedStaging` |
| R14 | `TestRedactionStateAndSensitiveTaxonomyFailClosed`, `TestStoreRejectsSubdirectorySwapDuringEveryPublicationBoundary`, `TestLinkNamedOpenFileAtAuthenticatesBothSides`, `TestWindowsStoreRejectsBroadWritableDACL` |
| R15 | `TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols`, coverage del diff y gates de la sección siguiente |

T1–T16 se ejecutaron en orden. La séptima remediación reabrió T10, T15 y T16
y solo se volvieron a marcar completas después de la evidencia final descrita
aquí; REV-1–REV-28 permanecieron cerrados.

## Verificación final

| Comando | Resultado |
|---|---|
| `gofmt` sobre todos los Go modificados/nuevos y `gofmt -l` | pass, salida vacía |
| `go vet ./...` | pass |
| `go test ./... -run '^$' -count=1 -timeout 10m` | pass |
| `go test` focal de los cinco paquetes afectados `-count=3 -timeout 10m` | pass |
| `go test ./... -count=1 -timeout 10m` | pass |
| `go test -race` focal con UCRT64 | pass |
| `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64 | pass |
| interleaving compartido y witness bloqueado `-count=50`; mismos casos con race `-count=20` | pass |
| cinco binaries instrumentados Linux en Docker/Alpine, cada uno desde su package cwd | pass: `cmd/featureproof`, `agentplanning`, `contextassembly`, `planapproval`, `validationworkflow` |
| `GOOS={windows,linux,darwin} GOARCH=amd64 CGO_ENABLED=0 go test -c` para los cinco paquetes | pass, 15/15 |
| `TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols` | pass en Windows y dentro de los binaries Linux |
| `git diff --check`, comprobación de `feature_list.json` y limpieza | pass |

El primer comando race del cierre usó un `CC` absoluto sin añadir UCRT64 al
`PATH` y falló en linker. Se corrigió el entorno (`PATH` + `CC=gcc.exe`) y tanto
race focal como global pasaron completos. No fue un fallo de código o test.

## Coverage reproducible del diff

- Base aprobada: `45d325a`.
- Se generaron desde cero cinco profiles Windows y cinco profiles mediante
  binaries Linux reales con
  `-coverpkg=./internal/contextassembly,./internal/agentplanning,./internal/planapproval,./internal/validationworkflow,./cmd/featureproof`.
- La fusión toma el máximo count por bloque y lo intersecta con líneas
  añadidas/modificadas de `git diff --unified=0 45d325a -- '*.go'`; archivos Go
  productivos no trackeados entran completos y `_test.go` queda fuera.
- `publish_unix_other.go`, compilable en Darwin/BSD pero no ejecutable en los
  hosts Windows/Linux disponibles, aporta explícitamente **0/1**. Su algoritmo
  común `linkNamedOpenFileAt` sí se ejecuta en Linux contra publicación válida y
  sustitución real del inode temporal.
- Resultado exacto final: **1874/2179 = 86.002754 %**.
- Umbral entero: `ceil(2179 × 0.85) = 1853`; margen: **21 statements**.

Los profiles corresponden al árbol final de producto. No se añadió código
productivo después de generarlos.

## Límites y limpieza

Las matrices cubren authority/source, widening, required/optional,
conflict/partial, budgets, contador y ranker adversarios, secretos, drift,
tampering, journal/receipt/witness rollback, cancelación y swaps de filesystem.
Los terminal records contienen solo metadata allowlisted y se emiten una vez,
incluso tras agotar el deadline de trabajo. La reconciliación incierta conserva
estado recuperable y el registry durable permite revalidar con un adapter nuevo
sin depender de memoria de proceso. La resolución usa fuentes productivas
actuales. Un CAS bloqueado no detiene reads ni writers independientes; snapshots
y bindings solo son direccionables con receipt confirmado. Tres identidades que
comparten digest conservan bytes durante cleanup, CAS y la ventana CAS–receipt.
Los huérfanos canónicos son deliberadamente retenidos para una GC futura que
parta de estado autoritativo estable; nunca se infiere ausencia desde scans
concurrentes de staging, receipts o journal.
La cancelación conserva su causa desde composición hasta HTTP y un deadline se
clasifica separadamente como timeout.

No se modificó `feature_list.json`, no se implementó feature 30/31 y no quedan
binaries, profiles ni temporales dentro del repositorio. La adquisición de diff
desde GitHub y la de environments siguen fuera del alcance aprobado.
