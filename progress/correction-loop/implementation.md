# Implementación — correction-loop

## Estado

T1–T8 completadas. Implementación y verificación listas para review
independiente. No se solicita transición a `done`; corresponde al reviewer y
al leader.

## Cambios

- `internal/correction`: dominio versionado, selección completa de fallos,
  journals append-only particionados por cadena, CAS/leases multiproceso,
  presupuestos acumulados reservados antes del consumo y preflight con
  `revisionbinding`, aprobación de continuación, coordinador failed-first,
  comparador determinista, publicación no-replace y adapter externo seguro.
- `internal/externalagent`: contrato de aplicación v2, cuatro operaciones de
  corrección, scopes separados y paridad CLI/MCP; las cinco operaciones v1
  mantienen nombres, DTOs y semántica.
- `cmd/featureproof`: routing del comando `correction`; la composición real
  conserva scopes vacíos y no instala coordinator, por lo que permanece
  fail-closed hasta feature 19.

## Trazabilidad

| Requirement | Tests |
|---|---|
| R1 | `TestCorrectionSubmitIsDurablyIdempotent`, `TestForeignCancellationLeavesJournalByteIdentical`, `TestCoordinatorReconcilesUncertainReservation`, `TestExternalAdapterSubmissionAndErrors` |
| R2 | `TestCorrectionChainCreatesImmutableLinearChildren`, `TestConcurrentTailAdmissionHasOneWinner`, `TestMultiprocessTailAdmissionHasOneWinner`, `TestJournalProjectorRejectsUnknownAndContradictoryEvents`, `TestJournalProjectorValidatesAuthorizedEventValues`, `TestJournalProjectorAcceptsCompleteAuthorizedValues` |
| R3 | `TestCorrectionRequiresDistinctCompatibleDeployedRevision`, `TestCorrectionFullFlowHermetic` |
| R4 | `TestFailureSelectionIsCompleteCanonicalAndUnmodifiable`, `TestObsoleteScenarioIsNotApplicable`, `TestJournalProjectorValidatesAuthorizedEventValues` |
| R5 | `TestCorrectionFullFlowHermetic`, `TestRegressionRechecksPriorPassAndDeduplicatesOnlyChildEvidence`, `TestJournalRecordsApprovalAndScenarioOrder`, `TestJournalProjectorValidatesAuthorizedEventValues` |
| R6 | `TestComparisonClassifiesCorrectedPersistentNewAndInconclusive`, `TestComparisonNewNotApplicableAndInconclusive`, `TestComparisonRejectsCorruptOrForeignPackage`, `TestComparatorRejectsReducedIdentityCollision`, `TestComparisonUsesFreshContextsAcrossVisibleBoundaries`, `TestComparisonRecoveryCompletesAbandonedIntent` |
| R7 | `TestCorrectionFullFlowHermetic`, `TestComparisonNewNotApplicableAndInconclusive`, `TestFinalizeCommitIsIdempotentAndReconcilesUncertainPublication`, `TestComparisonPublicationReconcilesLostResponse`, `TestComparisonJournalAppendReconcilesBeforeAndAfterFailure` |
| R8 | `TestChainLimitsStopBeforeNewWork`, `TestModelBudgetIsCumulativeAndLastReservationAtomic`, `TestTerminalUsageIsCountedOnceAfterCancelReplayAndReopen`, `TestEveryAdmissionLimitBecomesTypedTerminalData`, `TestFinalizeUsageIsConfirmedBeforePublication`, `TestInvalidOrFailedPreparationPublishesNothing`, `TestFinalizeCommitIsIdempotentAndReconcilesUncertainPublication`, `TestBudgetLimitsFlowThroughCoordinatorAndAdapter`, `TestBudgetStoreErrorsKeepOperationalCategory`, `TestContinuationApprovalIsBoundOneUseAndStaleSafe`, `TestApprovalRejectionStopsFunctionalWork`, `TestStaleApprovalIsJournaledAndStopsWork`, `TestJournalProjectorRejectsUnknownAndContradictoryEvents`, `TestJournalProjectorValidatesAuthorizedEventValues` |
| R9 | `TestInfrastructureFailureNeverBecomesFunctionalFailure`, `TestCorrectionRequiresDistinctCompatibleDeployedRevision`, `TestFinalizeUsageIsConfirmedBeforePublication`, `TestInvalidOrFailedPreparationPublishesNothing`, `TestBudgetStoreErrorsKeepOperationalCategory`, `TestJournalProjectorValidatesAuthorizedEventValues`, `TestComparisonUsesFreshContextsAcrossVisibleBoundaries`, `TestComparisonRecoveryCompletesAbandonedIntent`, `TestDefinitiveComparisonPublicationFailuresTerminateRecovery` |
| R10 | `TestCorrectionCancelJoinsAndPreservesParent`, `TestInterruptedOwnerDoesNotRerun`, `TestIndependentChainLocksDoNotBlockProgress`, `TestMultiprocessTailAdmissionHasOneWinner`, `TestLookupWaitsForPausedMultiprocessAppend`, `TestAppendFailureInjectionRecoversConfirmedPrefix`, `TestCoordinatorReconcilesUncertainReservation`, `TestFinalizeCommitIsIdempotentAndReconcilesUncertainPublication`, `TestComparisonPublicationReconcilesLostResponse`, `TestComparisonJournalAppendReconcilesBeforeAndAfterFailure`, `TestComparisonUsesFreshContextsAcrossVisibleBoundaries`, `TestComparisonRecoveryCompletesAbandonedIntent`, `TestConcurrentComparisonRecoveryInstallsAllWorkers`, `TestDefinitiveComparisonPublicationFailuresTerminateRecovery`, `TestBudgetLimitsFlowThroughCoordinatorAndAdapter`, `TestBudgetStoreErrorsKeepOperationalCategory`, `TestJournalProjectorRejectsUnknownAndContradictoryEvents`, `TestJournalProjectorValidatesAuthorizedEventValues` |
| R11 | `TestCorrectionCLIAndMCPContractParity`, `TestCorrectionLimitDimensionsHaveCLIAndMCPParity`, `TestExternalAdapterProjectsExhaustedAdmission`, `TestCorrectionScopesAndSecretProjection`, `TestExternalAdapterReauthorizesAndObscuresEvidence` |
| R12 | `TestCorrectionFullFlowHermetic`, `TestStoreDiscoveryHonorsCancellation`, `TestConcurrentComparisonRecoveryInstallsAllWorkers`, `TestDefinitiveComparisonPublicationFailuresTerminateRecovery`, barrido de comentarios sistémicos, ausencia de `time.Sleep` y gates siguientes |

## Correcciones de la primera review

- REV-1: `ReserveCancel` comprueba el principal dentro del CAS anterior al
  append. Una cancelación ajena deja el journal byte-for-byte igual y no
  consume la clave legítima del owner.
- REV-2: cada operación que puede consumir modelo recibe únicamente el
  presupuesto acumulado restante; failed-first, regresión y `Finalize`
  reservan y confirman uso antes de seguir o publicar.
- REV-3: cada cadena usa su propio lock OS y cada handle conserva una lease de
  owner liberada al terminar. Recovery prueba la lease sin robar owners vivos;
  procesos reales y failure injection verifican CAS y prefijos confirmados.
- REV-4: el comparador deduplica candidatos idénticos y falla cerrado si un
  fact reducido de package v1 corresponde a más de una identidad completa.
- REV-5: el journal conserva resolución/ausencia de aprobación y cada
  `scenario_confirmed`/`regression_covered` en el orden confirmado.
- REV-6: la coordinación de tests usa señales, joins y contextos acotados; no
  queda `time.Sleep`. Todas las declaraciones nuevas o modificadas tienen un
  comentario de responsabilidad sistémica.

## Correcciones de la segunda review

- REV-7: un error posterior al `fsync` se reconcilia bajo el lock de cadena. Si
  la reserva exacta existe, se reparan índices, se conserva la lease y el
  coordinator arranca un único worker. Un restart con el mismo nombre de owner
  también terminaliza el intento si ya no existe lease viva.
- REV-8: el projector suma usage solo al observar la primera transición a
  terminal. Cancelar un terminal, repetir la cancelación y reabrir el store no
  vuelven a cargar calls/tokens.
- REV-9: handles y run IDs se localizan mediante índices inmutables con lock OS
  por entrada. Cada replay de journal ocurre bajo el lock de su cadena. Un
  proceso real mantiene un append parcial mientras status/cancel esperan; una
  segunda cadena continúa sin bloqueo.
- REV-10: una admisión agotada persiste un terminal `inconclusive` con
  `terminal_reason=limit_exhausted` y una dimensión cerrada a
  `attempts|duration|model_calls|model_tokens`. CLI y MCP devuelven el mismo
  envelope y este valida contra el schema anunciado.

## Correcciones de la tercera review

- REV-7: la reconciliación posterior a un append incierto usa un contexto
  interno acotado e independiente del request cancelado. Si encuentra la
  reserva exacta conserva la lease y devuelve `New:true`, de modo que el
  coordinator instala un único worker joinable. La regresión cancela el
  request en el hook inmediatamente posterior al append y verifica replay,
  terminal y cancelación sin un segundo hijo.
- REV-11: la finalización se divide en `PrepareFinalize`, que conserva staging
  privado y declara usage, y `CommitFinalize`, que hace visible el paquete.
  El coordinator confirma el usage durable antes del commit; si excede el
  allowance no aparecen paquete ni comparación.
- REV-12: las seis fronteras de presupuesto clasifican el error real.
  Únicamente `LimitExhausted` con dimensión allowlisted produce
  `limit_exhausted`; cancelación, integridad, conflicto e infraestructura
  conservan su categoría segura. Los flujos verticales verifican
  duration/calls/tokens y la proyección del adapter.
- REV-13: replay usa un vocabulario cerrado y valida cada snapshot contra el
  anterior: transición, lineage, ordinal, hashes, plan, ownership,
  cancelación, aprobación, confirmaciones, accounting, terminal y comparison.
  Una tabla corrompe cada clase de campo y exige `IntegrityFailure`.

## Correcciones de la cuarta review

- REV-11: la preparación completa se valida antes del accounting: identidad,
  run, outcome funcional y usage dentro del allowance. El journal confirma
  `finalization_prepared` con ID, outcome y usage antes de consumir presupuesto.
  `CommitFinalize` es idempotente y `ResolveFinalize` reconcilia una respuesta
  perdida por `PreparationID`; las inyecciones antes/después prueban cero o un
  paquete visible, retry sin duplicado, reopen del journal y un único terminal.
- La publicación de comparison aplica el mismo protocolo. `Publish` es
  idempotente para el mismo digest y `Resolve` verifica bytes canónicos y hash.
  Un fallo anterior terminaliza sin comparison; un fallo posterior se
  reconcilia y deja exactamente un artifact y un `comparison_published`. El
  append final también se reconcilia ante fallo anterior o posterior al
  `fsync`, sin duplicar el evento ni perder el terminal observable.
- REV-13: el root deriva deadline de límites y verifica el snapshot sellado de
  policy. Preflight valida identidades, límites, orden, duplicados, evidence,
  razones, partición completa de motivadores y expansión exacta de escenarios
  de policy. Cada reserva de modelo debe igualar el remanente derivado antes
  del deadline. La matriz terminal solo admite comparison con outcome
  funcional preparado, o categorías operativas coherentes sin comparison.
- Las tablas positivas y negativas mutan cada valor autorizado y exigen
  `IntegrityFailure`; incluyen deadline, digest de policy, plan, allowance,
  preparación, outcome/reason, dimensión y comparison.

## Corrección de la quinta review

- REV-14: `comparison_intended` persiste antes de cualquier visibilidad la
  comparación canónica, su digest, el handle y el locator del paquete hijo.
  El projector impide publicar un digest distinto, terminalizar una intención
  incierta o modificar su lineage.
- `Publish`, `Resolve` y el append `comparison_published` usan contextos
  internos frescos y acotados. Un deadline consumido después de crear o
  resolver el artefacto no contamina la frontera siguiente.
- `ClaimPendingComparisons` reclama bajo lock y lease los intents cuyo owner se
  perdió. El nuevo coordinator termina la publicación sin regenerar la
  comparación; reopen y replay conservan exactamente un artefacto, una
  intención y un evento terminal.
- `TestComparisonUsesFreshContextsAcrossVisibleBoundaries` agota el contexto
  justo después de `Publish` y después de `Resolve`.
  `TestComparisonRecoveryCompletesAbandonedIntent` pierde owner/reabre en ambas
  fronteras y verifica terminal y replay idempotentes.

## Correcciones de la sexta review

- REV-15: `NewCoordinator` instala bajo `c.mu` todas las entradas recuperadas
  en `runs` antes de arrancar cualquier goroutine. La barrera determinista de
  `TestConcurrentComparisonRecoveryInstallsAllWorkers` recupera dos cadenas,
  observa ambos workers instalados, los libera en paralelo y verifica joins,
  leases libres y un único terminal por intent bajo race.
- REV-16: los errores tipados `Conflict` e `IntegrityFailure` del publisher se
  clasifican como definitivos. `comparison_abandoned` retira el intent y
  persiste un terminal `error/integrity_failure`; errores de contexto o
  infraestructura siguen siendo recuperables.
- `TestDefinitiveComparisonPublicationFailuresTerminateRecovery` cubre un
  artefacto válido de otro digest y bytes corruptos con el publisher local.
  Reopen/replay conserva un solo terminal, no escribe `comparison_published`
  y no vuelve a reclamar la cadena.

## Verificación real

- `go test ./internal/correction ./internal/externalagent ./cmd/featureproof -count=1`: pass.
- `go test ./... -count=1`: todos los paquetes pasan salvo el test preexistente
  `evidence_symlink` de feature 15; Windows no concede privilegio para symlink
  y su `Skipf` usa el `testing.T` padre.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1`: pass.
- `go mod verify`: pass.
- `git diff --check`: pass (solo avisos LF/CRLF del checkout).
- Builds `windows/amd64`, `linux/amd64`, `darwin/amd64`: pass.
- Coverage comparable global: 88,3 %. Coverage dirigida final:
  `internal/correction` 85,4 %; `internal/externalagent` 87,2 %.
- Race dirigida `internal/correction`, `internal/externalagent` y
  `cmd/featureproof`: pass con MSYS2 UCRT64 (`CGO_ENABLED=1`, `CC=gcc`).
- Race repetida ×20 de las regresiones de corrección afectadas: pass.
- Race global literal: todos los paquetes pasan salvo el mismo subtest
  preexistente `evidence_symlink`, que no puede crear symlinks en este Windows.
- Race global comparable excluyendo exclusivamente
  `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`:
  pass.

Los comandos finales se ejecutaron con `-count=1`; las regresiones afectadas
también pasaron ×20 y se usaron timeouts explícitos donde
había procesos hijos. Para race se antepuso `C:\msys64\ucrt64\bin` a `PATH`,
con `CC=gcc` y `CGO_ENABLED=1`. La repetición ×20 cubrió conjuntamente las
regresiones de REV-7, REV-11–REV-16, recovery concurrente y las fronteras de
comparison. Los tests multiproceso cancelan su proceso hijo mediante
`exec.CommandContext` si supera 15 segundos.

## Riesgos y límites

- La composición productiva no puede ejecutar una corrección positiva hasta
  disponer del provider preview confiable de feature 19.
- El fallo global de symlink es ambiental y preexistente; no se modificó
  `internal/localvalidation`.
