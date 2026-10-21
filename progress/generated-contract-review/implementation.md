# Feature 24 — Revisión y exportación de contratos derivados

## Estado de entrega

`ready_for_review`. T1–T13 tienen evidencia reproducible en la rama
`feature-24-generated-contract-review`. La feature permanece `in_progress` y
`feature_list.json` no fue modificado por el implementer.

## Resultado

`internal/contractreview` implementa el artifact canónico, manifests separados,
diff atómico, decisiones CAS, aprobación durable, baseline derivada recursiva,
export OpenAPI 3.1, publisher y store locales, y las superficies compartidas.
La composición productiva registra CLI, API HTTP y MCP; el webhook `githubpr`
autenticado traduce create/show/decide, deriva el principal del actor del evento
y conserva PR, head y changed paths en la autoridad durable. El workflow consume
la referencia aprobada y vuelve a validarla antes de aprobación y herramientas.

## Octava remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-19 | Toda confirmación exitosa del publisher revalida `ctx` después del sync y la verificación final. El flujo normal comprueba tras `directory_synced`; collision recovery comprueba después de limpiar y sincronizar el temp; marker recovery e `Inspect` también cierran su última ventana. Si el efecto ya es durable se devuelve `CategoryCancelled` con `errors.Is(context.Canceled)` y un retry del mismo `AttemptID` adopta el target sin duplicarlo | `TestPublishCancelsAfterDirectorySync` ×50 y race ×20; `TestPreparedRecoveryCollisionCancelsAfterDirectorySync` ×50 y race ×20 |

La collision se induce de forma determinista justo antes del commit: otro writer
materializa el mismo digest, el recovery verifica el target, limpia su temp y
cancela exactamente en `directory_synced`. En ambos caminos el primer caller no
observa falso éxito y el retry converge a un único target. REV-1–REV-18
permanecen cerradas.

Evidencia final: conjunto REV-15–REV-19 ×50 y race ×5; recovery/crash ×10;
paquetes afectados ×10 y race completo; vet, compile-only, cross-build
Windows/Linux/Darwin, `gofmt` y diff-check verdes. Cobertura: `contractreview`
85,2 % y `validationworkflow` 85,6 %. La suite global pasa salvo el subtest
histórico `evidence_symlink`, bloqueado por el privilegio de symlink de Windows.

## Séptima remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-18 | El publisher revalida `ctx` inmediatamente después de adquirir el owner lock y después del checkpoint `ownership_acquired`. La recuperación preparada vuelve a comprobarlo antes de limpiar temp, esperar/adoptar target, renombrar o confirmar éxito. Una cancelación conserva `CategoryCancelled` y `errors.Is(context.Canceled)`, no crea el target y deja marker+temp recuperables por el mismo intento | `TestPreparedRecoveryCancelsAfterOwnershipAcquired` ×20, `TestPreparedRecoveryOwnerReleaseRacesFollowerCancellation` ×20, `TestPreparedRecoveryCleansLinkedTempAfterCommittedTarget` ×20 |

La carrera release/cancel usa una barrera y canales acotados, sin sleeps. El
follower termina tanto si observa primero la cancelación como si adquiere primero
el lock; en este último caso, el checkpoint fuerza la revalidación antes de todo
efecto. El retry no cancelado consume el mismo estado preparado. REV-1–REV-17
permanecen cerradas.

Evidencia final de esta remediación: focal REV-18 ×20; conjunto concurrente
REV-15–REV-18 ×50; race focal ×5 y race de todos los paquetes afectados;
recovery/crash ×10; focal completa afectada ×10; vet, compile-only, builds
Windows/Linux/Darwin, `gofmt` y `git diff --check` verdes. Cobertura:
`contractreview` 85,1 % y `validationworkflow` 85,6 %. La suite global pasa en
todos los paquetes salvo el subtest histórico `evidence_symlink`, que no puede
crear symlinks en este host Windows sin el privilegio del sistema.

## Sexta remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-15 | Journal y checkpoint comprueban el contexto antes/después de serializar, por cada encode, tras write y tras sync. La cancelación retorna directamente `CategoryCancelled`, conserva `errors.Is(context.Canceled)` y no alcanza el commit siguiente; un retry termina la generación reservada | `TestLocalStoreCancellationAtDurableCASBoundaries` cubre 12 fronteras sincronizadas; `TestLocalStoreCancellationAfterCreationReservationRecovers` |
| REV-16 | Cada `AttemptID` usa un owner file descriptor-relative con lock de proceso no bloqueante (`flock`/`LockFileEx`). El lock precede al intent y se retiene hasta terminar/cancelar: ningún timeout concede autoridad para truncar un temp vivo. Tras release o muerte del proceso, el siguiente owner puede recuperar el intent/temp; intent distinto sigue fallando cerrado | `TestLocalPublisherConcurrentIdenticalAttemptConverges`, `TestLocalPublisherOwnershipCancellation`, crash matrix existente, cross-build de tres OS |
| REV-17 | Las carreras ya no esperan que dos callers entren en `intent_absent`. Los tests arrancan winner/follower por estados reales `intent_synced`/`ownership_wait`, tienen contexto y timeout, mantienen al writer vivo 350 ms y recogen siempre ambas goroutines. No contienen bucles internos de repetición | focal REV-15/16/17 ×50 en 135,6 s; race focal ×5 en 46,5 s; `TestServiceConcurrentIdenticalExportsReturnOneReceipt` ×50 |

Las colisiones Windows al reemplazar journal/checkpoint reintentan la misma
generación inmutable y solo aceptan éxito ajeno cuando los bytes durables son
idénticos. REV-1–REV-14 permanecen cerradas.

## Quinta remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-15 | `context.Context` atraviesa reserva, observación, journal, checkpoint y aplicación de transiciones. Hay checkpoints cancelables después de reservar y después de cada frontera durable; un retry completa la misma transición sin usar `time.Sleep` ni `context.Background` en la ruta CAS | `TestLocalStoreCancellationAfterCreationReservationRecovers`, `TestLocalStoreCancellationAtDurableCASBoundaries` |
| REV-16 | Un intento de publicación elige una intención durable. El perdedor elimina exclusivamente su temp por descriptor, espera marker/target del ganador y reconcilia ventanas rename/visibilidad; el store observa el checkpoint ganador sin iniciar I/O competidor. Dos exports idénticos convergen al mismo receipt y dejan un target, marker y receipt | `TestLocalPublisherConcurrentIdenticalAttemptConverges` ×50, `TestServiceConcurrentIdenticalExportsReturnOneReceipt` ×50, `TestPreparedRecoveryWaitsForConcurrentRename`, race `internal/contractreview` ×5 |

REV-1–REV-14 permanecen cerradas. Las suites focales de superficies, exporter,
filesystem, lifecycle y GitHub siguen verdes; los builds Windows/Linux/Darwin
continúan compilando.

## Cuarta remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-1 | La CLI define flags permitidas y requeridas por subcomando. El dispatcher productivo ejecuta la API sobre un listener loopback real, deriva bearer por request, persiste create/decision/export, reinicia desde disco y hace shutdown joinable | `TestCLIExercisesShowDecideExportAndRejectsMalformedFlags`, `TestContractReviewCLICompositionCreatesDurableReview` |
| REV-9 | Cada intento publica una intención inmutable antes del primer byte del temp. Un retry reabre y trunca exclusivamente el temp ligado; los markers tienen temp/commit observables y Darwin expone el checkpoint entre `linkat` y `unlinkat` | `TestLocalPublisherCrashMatrix`, `TestLocalPublisherRecoversAfterTempSyncFailure`, `TestPublicationIntentValidationAndCleanup` |
| REV-10 | Las reservas creation/transition se publican completas mediante temp+sync+commit exclusivo. Un proceso que encuentra la misma reserva observa brevemente al winner y luego puede completar su transición; nunca devuelve aggregate cero | `TestLocalStoreMultiprocessIdempotency` ×20, `TestLocalStoreConcurrentCreateHasOneImmutableWinner` ×20, `TestLocalStoreCompletesItsOwnExistingReservation` |
| REV-14 | `githubpr.Runtime` recibe ownership explícito del review service, lo cierra después de coordinator/validation y también en cualquier error de composición. La composición local cierra store si falla publisher | `TestGitHubRuntimeOwnsProductReviewLifecycle` |

REV-2–REV-8 y REV-11–REV-13 permanecen cerradas: focales completos, repetición,
race y cross-build no muestran regresiones en baseline, diff, exporter, metadata,
filesystem descriptor-relative, redacción ni binding GitHub.

## Tercera remediación REV → cambio → prueba

| Hallazgo | Cambio final | Evidencia principal |
|---|---|---|
| REV-1 | HTTP exige bearer configurado, solo escucha loopback, deriva el principal por request y mapea todas las categorías; MCP publica schemas cerrados; CLI/MCP/API atraviesan composición y estado durable | `TestContractReviewCLICompositionCreatesDurableReview`, `TestProductiveHTTPAndMCPRegisterCompleteLifecycle`, `TestRuntimeFailureEnvelopesAndProtocolBranches` |
| REV-6 | Security alternatives/scopes, headers, media y schemas reciben audit local; diagnostics se filtran por safe location y schemes compartidos unen todas las procedencias | `TestExporterPlacesAuditAtSmallestUnits` |
| REV-8 | Publisher y store recorren desde root handles sin follow; target/temp/hash/marker/checkpoint/journal/idempotency/rename operan respecto del parent descriptor. Los paths solo verifican drift de identidad | `TestLocalPublisherRejectsSynchronizedAncestorSwaps`, `TestLocalStoreRejectsSynchronizedAggregateSwap`, cross-build Windows/Linux/Darwin |
| REV-9 | La intención marker se sincroniza antes del commit y conserva target/temp/digests; cualquier proceso reconcilia cada frontera de fsync/link/rename, incluido el caso Darwin target+temp | `TestLocalPublisherCrashMatrix`, `TestPreparedExportReconcilesAfterPublicationCrash` |
| REV-10 | Bounds agregados preceden todo I/O. Idempotency usa reserva atómica; cada review y expected revision eligen una creación/transición inmutable; journal completo y checkpoint son generaciones atómicas recuperables sin locks de aplicación durante I/O. Store, publisher y composición cierran handles | `TestStoreBoundsLifecycleGrowthAndClosesDescriptors`, `TestLocalStoreMultiprocessIdempotency` ×100, `TestLocalStoreConcurrentCreateHasOneImmutableWinner` ×100, `TestLocalStoreConcurrentCASHasOneWinner` ×100, `TestLocalStoreAllowsRecoveryWhileMutationIOIsPaused` ×100 |
| REV-12 | Show/decide comparan installation/repository/PR/head/changed paths con el artifact; el actor autenticado decisor entra en binding/audit y el gateway reconcilia un comentario seguro bot-owned en redelivery/restart | `TestGitHubAdapterBindsCreateAndDecisionToAuthenticatedHead`, `TestGitHubRuntimeGatewayRoutesAuthenticatedWebhookCommands`, `TestDurableGitHubSummaryReconcilesCreateRedeliveryAndUpdate`, `TestWebhookRoutesAuthenticatedContractReviewAndReplaysReceipt` |

REV-2/3/4/5/7/11/13 no regresan: las suites de baseline recursiva,
dependencias/selectividad, provenance agregada, autoridad epistémica, redacción,
comentarios y verticales productivos continúan verdes.

## Segunda remediación REV → cambio → prueba

| Hallazgo | Cambio | Evidencia principal |
|---|---|---|
| REV-1 | API HTTP y MCP se registran en `main`; `githubpr.WebhookHandler` enruta comandos autenticados de review al gateway durable | `TestProductiveHTTPAndMCPRegisterCompleteLifecycle`, `TestWebhookRoutesAuthenticatedContractReviewAndReplaysReceipt`, `TestGitHubRuntimeGatewayRoutesAuthenticatedWebhookCommands` |
| REV-3 | Una baseline `approved_projection` usa `ResolveApproved` recursivo, acotado a 32 niveles y con detección de ciclos; cada review previo revalida candidate, baseline, manifests, analyzers, policy y GitHub | `TestDerivedBaselineRequiresAndConsumesCurrentDurableApproval` (incluye drift del baseline subyacente) |
| REV-4 | Dependencias security/scheme son bidireccionales y por operación; solo se proyectan schemes referenciados; eliminaciones `allOf` se aplican de índice mayor a menor | `TestProjectionDependenciesAndAllOfOrdering`, `TestDiffEmitsAtomicParameterResponseAndSchemeUnits`, `TestNoChangeAcceptAllCreatesApproval` |
| REV-5 | `MaxProvenance` es agregado sobre catálogos/proyección e incluye operaciones, claims y constraints; `makeItem` no trunca | `TestAggregateProvenanceLimitIncludesConstraintsAndAllOperations`, `TestAggregateSchemaAndSourceLimitsFailWithoutArtifact` |
| REV-6 | Claims, provenance, clases, confidence/completeness, conflicts, omissions y diagnostics se adjuntan en parameter/body/response/header/media/schema/scheme | `TestExporterPlacesAuditAtSmallestUnits`, `TestExporterDeclaresConflictsDiagnosticsOmissionsAndNeutralSchemaID` |
| REV-8 | Implementaciones Linux/Darwin/Windows separadas; root descriptor estable, parent FD en commit, no-follow/no-replace, link-count y comprobaciones TOCTOU | `TestStableRootDescriptorRejectsReplacement`, `TestLocalPublisherDetectsSynchronizedTargetTOCTOU`, `TestLocalPublisherRejectsHardLinkedTarget`; cross-compile Darwin verde |
| REV-9 | Cada publish lleva `AttemptID`; el publisher persiste marker sincronizado y reconcile exige marker+target+digest, sin adoptar un archivo idéntico previo | `TestPublicationMarkersRejectPreexistingIdenticalTargets`, `TestPreparedExportReconcilesAfterPublicationCrash` |
| REV-10 | Journal usa reader acotado por `MaxBytes`+overhead, checkpoint/journal se recuperan, root identity se comprueba antes de commit, locks OS son por clave/review e idempotencia es multiproceso | `TestStoreReadsJournalLargerThanOneMiB`, `TestStableRootDescriptorRejectsReplacement`, `TestLocalStoreMultiprocessIdempotency`, `TestLocalStoreRecoversEitherCrashOrderingFromIntegrityJournal` |
| REV-12 | Actor GitHub autenticado se mapea por policy a principal, se incluye en artifact/binding y audit; create/show/decide refrescan snapshot y redelivery es durable | `TestGitHubAdapterBindsCreateAndDecisionToAuthenticatedHead`, `TestGitHubRuntimeGatewayRoutesAuthenticatedWebhookCommands`, `TestWebhookRoutesAuthenticatedContractReviewAndReplaysReceipt` |

REV-2, REV-7, REV-11 y REV-13 permanecen cerradas: revalidación completa,
autoridad humana aditiva sin elevar evidencia, redacción ligada al artifact y
manifests candidate/baseline separados siguen cubiertos por sus pruebas previas.

## Trazabilidad requirements → tests

| Requirement | Evidencia |
|---|---|
| R1 | `TestArtifactCanonicalIdentityCopiesAndLimits`, `TestAggregateProvenanceLimitIncludesConstraintsAndAllOperations`, `TestLocalStoreMultiprocessIdempotency` |
| R2 | `TestBaselineVerificationFailsClosed`, `TestDerivedBaselineRequiresAndConsumesCurrentDurableApproval` |
| R3 | `TestStructuralDiffClassifiesAtomicChangesAndUncertainty`, `TestProjectionDependenciesAndAllOfOrdering` |
| R4 | `TestDecisionReducerRejectsInvalidSetsAndBuildsSelectiveProjection`, `TestProjectionDependenciesAndAllOfOrdering` |
| R5 | `TestServiceAuthorizationAuditAndDriftInvalidation`, `TestPolicyDriftInvalidatesBeforeDecisionCAS` |
| R6 | `TestRevalidationDetectsEveryBoundDimension`, `TestLocalStoreCancellationAtDurableCASBoundaries`, pruebas de checkpoints de `validationworkflow` |
| R7 | `TestApprovalLineageDoesNotElevatePartialEvidence`, `TestApprovedReviewReducerKeepsPartialAbsenceInconclusive` |
| R8 | `TestExporterProducesDeterministicValidOpenAPI31`, `TestExporterPlacesAuditAtSmallestUnits` |
| R9 | `TestPublicationMarkersRejectPreexistingIdenticalTargets`, `TestLocalPublisherCrashMatrix`, `TestLocalPublisherRecoversAfterTempSyncFailure`, `TestLocalPublisherConcurrentIdenticalAttemptConverges`, `TestLocalPublisherOwnershipCancellation` |
| R10 | tests `TestLocalStore*`, `TestStoreReadsJournalLargerThanOneMiB`, `TestServiceConcurrentIdenticalExportsReturnOneReceipt`, `TestDurableCollisionReconciliation`, `TestGitHubRuntimeOwnsProductReviewLifecycle` |
| R11 | `TestLocalStoreCancellationAtDurableCASBoundaries`, `TestLocalPublisherOwnershipCancellation`, matrices de límites y canarios de `redaction_test.go` |
| R12 | `TestContractReviewCLICompositionCreatesDurableReview`, `TestProductiveHTTPAndMCPRegisterCompleteLifecycle`, tests productivos GitHub citados arriba |
| R13 | auditoría de comentarios y gates siguientes |

## Gates ejecutados

```text
go test ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof -count=1 -timeout=10m
go test ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof -count=10 -timeout=12m
go test ./internal/contractreview -run 'Test(LocalStoreCancellationAfterCreationReservationRecovers|LocalStoreCancellationAtDurableCASBoundaries|LocalPublisherConcurrentIdenticalAttemptConverges|ServiceConcurrentIdenticalExportsReturnOneReceipt|LocalPublisherOwnershipCancellation|PreparedRecoveryWaitsForConcurrentRename)$' -count=50 -timeout=5m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof -count=1 -timeout=15m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./internal/contractreview -count=5 -timeout=15m
go vet ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof
go test ./... -run '^$' -count=1 -timeout=10m
GOOS={windows,linux,darwin} GOARCH=amd64 go test -c ./internal/contractreview
GOOS={windows,linux,darwin} GOARCH=amd64 go build ./cmd/featureproof
go test ./internal/contractreview -coverprofile=<temp> -count=1
go test ./internal/validationworkflow -coverprofile=<temp> -count=1
gofmt -l <Go modificados/nuevos>
git diff --check
go test ./... -count=1 -timeout=15m
go test ./internal/goinspector -run TestRepositoryStableReadRejectsSameSizeInPlaceWrites -count=10 -timeout=5m
```

Resultados finales: focales afectados ×10 y conjunto REV-15/16/17 ×50
verdes; race UCRT64 completo y focal ×5, vet, compile-only,
cross-build Windows/Linux/Darwin, formato y diff verdes. Cobertura:
`internal/contractreview` **85,0 %** y `internal/validationworkflow` **85,6 %**.
La suite global reproduce únicamente el fallo ambiental histórico
`internal/localvalidation/evidence_symlink` porque Windows no concede el
privilegio de crear symlinks. Todos los paquetes afectados por la feature pasan.

## Límites de alcance

V1 publica OpenAPI 3.1 JSON bajo un root local configurado. YAML, commits y
publicación remota quedan fuera de la spec. Los catálogos, roots, policies,
principals y credenciales se resuelven en composición, nunca desde payloads de
review. La rama histórica de symlink global depende de un privilegio de Windows;
las pruebas de esta feature cubren root swap, TOCTOU, hard links y rechazo de
links disponibles en el host.

El gate se ejecutó en Windows. La rama `target_linked` de la matriz se activa en
Darwin exactamente entre `linkat` y `unlinkat`; compiló en el cross-build Darwin,
pero su subprocess solo puede ejecutarse en un host Darwin.

Los archivos `<attempt>.owner.lock` son autoridad durable deliberada por
`AttemptID`; permanecen junto a los markers, vacíos y sin secretos. El sistema
operativo libera su lock al cerrar o morir el owner, que es la prueba usada para
reclamar. No se dejan goroutines, temps ni intents después de converger.

Se auditó el diff de Go: las entidades, constantes, funciones, métodos, helpers,
fakes y fixtures nuevos o modificados incluyen comentario de responsabilidad
sistémica.
