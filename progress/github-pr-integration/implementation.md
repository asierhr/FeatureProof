# Implementación — github-pr-integration

## Estado

`ready_for_review`

## Resultado tras REV-1–REV-9

- **REV-1:** `ComposeRuntime` y `featureproof github serve` conectan webhook, store, GitHub App token source, REST, retry, publisher y coordinator. Los secretos se leen solo en el entrypoint; el preview permanece fail-closed hasta feature 19 y el shutdown tiene deadline y join.
- **REV-2:** el autor de la PR y el actor del comando son identidades distintas. El coordinator relee instalación, repositorio, PR, acceso y head antes de submit, recovery, status, cancel y cada publicación.
- **REV-3:** deliveries, asociaciones e intents usan CAS, owner y leases en el journal. Los leases expiran, se renuevan antes de fronteras externas y se recuperan, incluido un intent de publicación asociado a un run terminal. Una regresión con dos procesos del SO demuestra un único owner.
- **REV-4:** `RetryPolicy` envuelve lecturas y mutaciones GitHub; un create incierto se reconcilia antes de repetir. Los 403 con headers de límite se clasifican como rate limit. El supervisor hace polling hasta terminal, finaliza cancelación con contexto independiente acotado y hace join.
- **REV-5:** toda operación y token conserva el `installationID` exacto. Las búsquedas de checks y comentarios siguen `Link` con límite de páginas y fallan cerradas al agotarlo.
- **REV-6:** la asociación sella también IDs de check/comentario, owner, lease, lineage y CAS. Antes de cada update se obtiene el recurso y se verifica app o bot, head, external ID/marker e ID persistido; un ID ajeno produce cero mutaciones.
- **REV-7:** la proyección exige coincidencia de association, run y package en run ID, binding, repository, head y outcome. Proyecta constraints, inconclusos como `unverified`, failures estructurados y todas las referencias de evidencia dentro de límites.
- **REV-8:** el comando admite exclusivamente espacios ASCII exteriores y una EOL opcional. El HMAC se verifica sobre bytes originales antes del decode y el DTO tolera campos GitHub externos no consumidos.
- **REV-9:** el verificador AST cubre funciones, tipos, constantes y variables de package. Las garantías anteriores tienen regresiones verticales, no solo tests de helpers.

## Resultado tras REV-10–REV-14

- **REV-10:** una delivery `received` con lease vigente es replay/no-op incluso
  para el mismo owner. La regresión concurrente atraviesa HMAC, handler,
  coordinator y validation y demuestra una admisión y un submit.
- **REV-11:** `ExistingRunRequest`, `ExistingRunLookup`,
  `ValidationGateway.ResolveExisting` y `Coordinator.AssociateExisting`
  forman el caso de uso interno tipado. Solo reciben identidades opacas y
  verifican la tupla refrescada/autorizada, run handle, repository, head,
  package run y binding; casos foreign fallan cerrados.
- **REV-12:** una asociación pre-run no tiene external check ID. Al confirmar
  el run, `BindAssociationRun` deriva y sella el ID desde instalación, repo, PR,
  head y run estable. El store deduplica esa identidad lógica y dos runs sobre
  el mismo head producen IDs distintos.
- **REV-13:** `RetryPolicy` posee un contexto con deadline real y lo entrega a
  cada operación; el cliente HTTP productivo añade timeout. El REST adapter
  conserva categorías seguras transient, rate-limited y cancelled del token
  source. Las pruebas bloquean el transporte hasta `ctx.Done()` y verifican
  retry de credenciales transient/rate-limited.
- **REV-14:** checks de ownership y búsquedas REST exigen el marker versionado
  como primera línea completa. Sufijos, espacios, marker sin EOL y otra versión
  se rechazan en una prueba que atraviesa el adapter.

## Resultado tras REV-15–REV-17

- **REV-15:** producción y los constructores rechazan leases que no superen
  `RunTimeout`, `CloseTimeout` y el deadline de retry con margen explícito. La
  asociación se deduplica durablemente también por delivery. Una regresión
  vertical bloquea el proceso A, expira y reclama la delivery con B, y prueba
  un único submit y una única escritura de check.
- **REV-16:** cada intent guarda el `CheckPublication` o `CommentPublication`
  seguro original y verifica su digest antes de persistir o recuperar.
  `NewCoordinator` reclama, refresca autoridad y reconcilia todos los intents
  originales antes de iniciar workers o fases nuevas. La regresión de restart
  parte de efectos remotos inciertos y converge en los IDs existentes sin
  create/update adicional.
- **REV-17:** el linaje durable es atómico: run handle, package run, binding y
  external check ID están todos vacíos o todos presentes y coherentes. Las
  catorce combinaciones parciales fallan cerradas; verify, store y publisher
  rechazan también estado parcial manipulado.

## Resultado tras REV-18–REV-20

- **REV-18:** el contexto de `RunTimeout` nace al entrar en el worker, antes
  del primer refresh y de `Validation.Submit`. La renovación inicial deja la
  lease por encima de todo el presupuesto de run más margen. Una regresión con
  dos coordinators y dos stores sobre el mismo journal bloquea Submit de A,
  comprueba el deadline recibido, demuestra que B no entra bajo la lease viva
  y solo lo admite tras cancel, join y liberación durable de A.
- **REV-19:** `NewCoordinator` cancela su contexto y cede todos los intents y
  asociaciones reclamados si refresh, autorización, reconciliación o release
  fallan. `ReleaseIntent` conserva el intent preparado pero elimina owner y
  lease. Una prueba recupera el primer intent, falla el segundo y verifica que
  ambas asociaciones y otra no relacionada quedan libres, y que el intent
  fallido se puede reclamar inmediatamente.
- **REV-20:** recovery valida el DTO durable contra association, ownership y
  vocabulario permitido, y entrega directamente ese `CheckPublication` o
  `CommentPublication` a reconcile/create/update. No reconstruye payload desde
  `DetailsURL` u otra configuración actual. Las pruebas cambian DetailsURL tras
  el crash y cubren efecto remoto presente y ausente, conservando exactamente
  los bytes lógicos persistidos.

## Cierre de remanentes REV-19–REV-20

- **REV-19:** `ClaimPending` verifica asociaciones, intents, ownership y sealing
  del lote completo antes de publicar claims. Todos los claims se escriben en
  un único `ClaimBatch` del journal; un short write se trunca al offset previo
  y el snapshot derivado nunca convierte un append durable en error ambiguo.
  Un intent huérfano se abandona sin reclamar trabajo válido. Las regresiones
  prueban intent huérfano y fallo pre-append: owner B reclama inmediatamente y
  no observa ownership parcial de A.
- **REV-20:** reconciliar bot+marker ya no confirma por identidad solamente.
  El publisher compara el body completo; si coincide adopta el ID sin write, y
  si está desactualizado persiste el ID y ejecuta exactamente un UpdateComment
  con el payload durable. Solo confirma después de observar en create/get/update
  el body exacto. Las rutas normal y recovery cubren comentario descubierto o
  ya persistido, body igual y body antiguo.

## Resultado tras REV-21

- `ClaimPending` calcula el tamaño del `StoreEvent` JSON completo, incluidos
  sequence, timestamp, kind, claves y salto de línea, y solo confirma un lote
  estrictamente menor que el límite real de replay de 1 MiB.
- Asociaciones e intents con lease viva del mismo owner no vuelven a emitirse.
  Llamadas sucesivas reclaman el backlog restante en lotes atómicos.
- `TestClaimPendingDrainsBacklogInReplayableBoundedBatches` crea más de 1 MiB
  de payload pendiente, reabre el store entre lotes, verifica el tamaño de cada
  línea, ausencia de pérdida/duplicado, renovación, confirmación,
  terminalización y release, y ausencia de remanentes para owner B.

## Resultado tras REV-22

- `ClaimPending` entrega como máximo una asociación y todos sus intents
  recuperables. El tamaño individual de intent deja espacio determinista para
  los dos kinds permitidos y la asociación sellada.
- `NewCoordinator` refresca la autoridad y la lease antes de cada recovery,
  confirma el lote y lo entrega al supervisor o lo libera si ya es terminal
  antes de reclamar el siguiente. No acumula claims detrás de un backlog sin
  límite.
- Un fallo cede únicamente el lote actual no entregado; los lotes anteriores
  ya pertenecen al supervisor y se cancelan/join por esa vía, sin liberaciones
  cruzadas desde rollback.
- `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore` usa más de 1 MiB,
  avanza el reloj dentro de cada recovery y ejecuta dos coordinators contra el
  mismo journal. Cada owner confirma antes de su siguiente claim, ninguna lease
  expira, cada efecto se observa una vez y la composición de ambos termina.
- `TestCoordinatorRecoversIntentFromItsAlreadyOwnedLiveAssociation` cubre el
  crash entre el claim de asociación y el claim de intent: el restart carga la
  asociación viva sin reemitirla, confirma el intent y libera el terminal.

## Resultado tras REV-23

- El drain conserva `delivered` y usa `ClaimPendingExcept`: un ID entregado a
  un worker queda excluido del resto de ese arranque aunque el worker falle y
  libere inmediatamente. Los demás IDs continúan por orden y no sufren
  starvation.
- La lease liberada queda durablemente disponible para otro coordinator. Cada
  coordinator solo entrega ese ID una vez por drain, por lo que el handoff se
  reprograma de forma acotada y nunca queda una lease sin worker.
- El supervisor mantiene un canal de idle por generación. `WaitIdle` observa la
  salida real de los workers; la regresión que antes usaba un timeout de pared
  de un segundo ahora espera ese lifecycle y luego valida submit y comentario.
  `TestCoordinatorBindsFreshHeadAndJoins` pasa 500 ejecuciones consecutivas.
- `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff` bloquea un
  recovery independiente, fuerza un submit fallido, observa las barreras antes
  y después del release, niega reclaim prematuro y prueba un único handoff a un
  segundo coordinator mientras el primer drain termina sin duplicar el ID.

T2, T4, T6, T7, T9, T10 y T11 se mantienen `[x]` porque estas garantías ya
están implementadas y verificadas sin rebajar assertions.

## Trazabilidad R1–R12

| Requirement | Pruebas nominales |
|---|---|
| R1 | `TestComposedRuntimeAuthenticatesAndFailsClosedAtPreview`, `TestConcurrentWebhookReplayAdmitsAndSubmitsOnce`, `TestAssociateExistingRunVerifiesAuthorizedTupleAndBinding` |
| R2 | `TestCoordinatorBindsFreshHeadAndJoins`, `TestForeignRunAndBindingPackagesFailClosed`, `TestHeadChangeStalesOldCheckWithoutStartingNewRun` |
| R3 | `TestWebhookAuthenticationReplayAndCollision`, `TestConcurrentWebhookReplayAdmitsAndSubmitsOnce`, `TestDurableLeaseElectsExactlyOneOperatingSystemProcess`, `TestExpiredDeliveryReclaimStillSubmitsOneRun` |
| R4 | `TestAuthorizationAndForkPolicy`, `TestCoordinatorCancelReauthorizesHandle`, `TestForeignPersistedIDsCauseZeroMutations` |
| R5 | `TestGitHubAppTokenSourceUsesExactInstallationRepositoryAndPermissions`, `TestRESTUsesExactInstallationAndFollowsPagination` |
| R6 | `TestExternalCheckIdentityDiffersForTwoRunsOnOneHead`, `TestRunBoundPublicationAndLogicalAssociationRejectPreRunOrDuplicateIdentity`, `TestUncertainCreateReconcilesBeforeRetry`, `TestAssociationLineageIsAtomicAtEveryBoundary`, `TestRecoverIntentUsesExactDurablePayloadAcrossConfigurationChange` |
| R7 | `TestSummaryComesFromVerifiedPackage`, `TestForeignRunAndBindingPackagesFailClosed`, `TestProjectionBoundsRetainExplicitOmission` |
| R8 | `TestCanonicalCommentMarkerRejectsPrefixVariants`, `TestForeignPersistedIDsCauseZeroMutations`, `TestPublisherReconcilesOwnedAndRejectsSpoofedRecords`, `TestCommentReconciliationComparesExactBody` |
| R9 | `TestAuthorizationAndForkPolicy`, `TestComposedRuntimeAuthenticatesAndFailsClosedAtPreview` |
| R10 | `TestRetryDeadlineAndCredentialCategories`, `TestRESTErrorClassification`, `TestUncertainCreateReconcilesBeforeRetry`, `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore`, `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff`, `TestSupervisorCancellationFinalizesAndJoins` |
| R11 | `TestPreparedIntentRecoveryClaimsItsTerminalAssociation`, `TestCoordinatorRecoversOriginalPublicationIntentsBeforeStartingWork`, `TestExpiredDeliveryReclaimStillSubmitsOneRun`, `TestSubmitBudgetPrecedesExternalCallAndLeaseExcludesSecondCoordinator`, `TestRecoveryFailureReleasesEveryStartupClaim`, `TestClaimPendingValidatesAndCommitsOneAtomicBatch`, `TestClaimPendingDrainsBacklogInReplayableBoundedBatches`, `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore`, `TestCoordinatorRecoversIntentFromItsAlreadyOwnedLiveAssociation`, `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff`, `TestRecoverIntentUsesExactDurablePayloadAcrossConfigurationChange`, `TestCommentReconciliationComparesExactBody`, `TestSupervisorCancellationFinalizesAndJoins` |
| R12 | `TestSystemicCommentsCoverChangedSymbols`, `TestGitHubAppTokenSourceFailsClosed`, `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore`, `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff`, package suite, race y builds |

## Verificación real

- `go test -count=20 ./internal/githubpr`: **pass**.
- `go test -race -count=3 ./internal/githubpr` con MSYS2 UCRT64, `CC=gcc`, `CGO_ENABLED=1`: **pass**.
- Coverage `internal/githubpr`: **85,6 %**.
- `go test ./cmd/featureproof`: **pass**.
- Suite global comparable, excluyendo solo `internal/localvalidation`: **pass**.
- Race global comparable con la misma exclusión: **pass**.
- `go vet ./...`, `go test -run '^$' ./...`, `go mod verify`, `go build ./...` y builds `linux/amd64`, `darwin/amd64`, `windows/amd64`: **pass**.
- `gofmt -l` y `git diff --check`: **pass**; solo aparecen avisos de conversión LF/CRLF ya presentes en archivos compartidos.
- El `go test ./...` literal conserva únicamente el fallo ambiental preexistente de feature 15: Windows deniega el symlink de `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`.
- Tests herméticos: sin red, credenciales reales ni `time.Sleep`.

T1–T11 permanecen `[x]`. No se modificó el estado de la feature; la transición final corresponde al leader.
