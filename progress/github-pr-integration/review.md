# Novena revisión independiente — github-pr-integration

`approved`

## Veredicto

REV-23 queda cerrada. El drain de arranque excluye de los claims posteriores
las asociaciones que ya entregó a su supervisor, sin impedir que otro proceso
las reclame después de una liberación durable. La regresión usa barreras antes
y después de la liberación, mantiene en paralelo un recovery independiente y
demuestra un único handoff para cada uno de los dos coordinators. El supervisor
expone además un `WaitIdle` ligado a la generación real de workers, por lo que
el test que antes dependía de un timeout ya espera el lifecycle que pretende
observar.

No encontré regresiones ni defectos nuevos tras revalidar R1–R12, T1–T11, los
hallazgos REV-1–REV-22 y las fronteras sistémicas de la feature. La única falla
de la suite global continúa siendo el subtest preexistente de symlink en
Windows, permitido expresamente como excepción ambiental comparable.

## Cierre de REV-23

| Aspecto | Resultado | Evidencia |
|---|---|---|
| Exclusión del drain original | pass | `NewCoordinator` crea `delivered`, lo pasa a `ClaimPendingExcept` en cada iteración y añade el ID antes de `start` (`internal/githubpr/coordinator.go:45-47`, `91-92`). Una liberación rápida del worker ya no vuelve a introducir ese ID en el mismo drain. |
| Claim durable acotado | pass | `ClaimPendingExcept` omite asociaciones e intents cuyo ID está excluido antes de construir candidatos, selecciona un único grupo asociación+intents y lo confirma en un solo evento (`internal/githubpr/store.go:460-587`). No deja lease nuevo para un handoff descartado. |
| Segundo coordinator | pass | `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff` comprueba que owner B no puede reclamar antes del release, sí recibe el trabajo después y observa exactamente un submit de A y uno de B (`internal/githubpr/eighth_review_test.go:111-208`). |
| Progreso del backlog | pass | Mientras el worker no terminal cruza su release, el mismo test bloquea un intent terminal independiente; A recupera y libera ese grupo y termina su constructor sin volver a reclamar ni quedar en starvation. El caso pasó 500 ejecuciones consecutivas. |
| Sin doble owner | pass | La prueba consulta el segundo store antes y después de la frontera durable de `ReleaseAssociation`; el package completo con `-race` no detectó acceso concurrente inválido ni doble handoff. |
| Lifecycle de `WaitIdle` | pass | `start` abre el canal de generación bajo el mismo mutex con el que registra al worker; el defer libera el lease, retira el worker y cierra el canal solo al quedar vacío (`internal/githubpr/coordinator.go:208-239`). `WaitIdle` toma ese canal bajo mutex (`300-310`). `TestCoordinatorBindsFreshHeadAndJoins` lo invoca después de `Admit` y comprueba submit y comentario sin espera temporal (`internal/githubpr/githubpr_test.go:620-667`). |
| Estabilidad | pass | El paquete pasó 20 veces, el escenario REV-23 pasó 500 veces y el paquete pasó tres veces con detector de carreras. No reapareció el flake previo. |

## Revalidación R1–R12 y T1–T11

| Requirement | Resultado | Evidencia principal |
|---|---|---|
| R1 | pass | Comando exacto, payload realista, asociación interna tipada y composición vertical: `TestWebhookRealisticPayloadAndExactASCIICommand`, `TestConcurrentWebhookReplayAdmitsAndSubmitsOnce`, `TestAssociateExistingRunVerifiesAuthorizedTupleAndBinding`. |
| R2 | pass | Head completo refrescado, binding exacto, nueva identidad por run y stale sin reutilización: `TestCoordinatorBindsFreshHeadAndJoins`, `TestExternalCheckIdentityDiffersForTwoRunsOnOneHead`, `TestHeadChangeStalesOldCheckWithoutStartingNewRun`. |
| R3 | pass | Body acotado, HMAC sobre bytes previos al decode, digest/receipt durable, replay concurrente y lease multiproceso: `TestWebhookAuthenticationReplayAndCollision`, `TestConcurrentWebhookReplayAdmitsAndSubmitsOnce`, `TestDurableLeaseElectsExactlyOneOperatingSystemProcess`. |
| R4 | pass | Instalación, repositorio, PR, actor, selección y head se refrescan en cada frontera; IDs persistidos no conceden autoridad: `TestCommandActorIsIndependentAndEveryBoundaryRefreshes`, `TestForeignPersistedIDsCauseZeroMutations`. |
| R5 | pass | Token efímero por instalación y repositorio, permisos exactos y secretos restringidos a composición/transporte: `TestGitHubAppTokenSourceUsesExactInstallationRepositoryAndPermissions`, `TestGitHubAppTokenSourceFailsClosed`. |
| R6 | pass | Check propio por run/head, external ID sellado tras bind, payload durable, ownership y reconciliación idempotente: `TestRunBoundPublicationAndLogicalAssociationRejectPreRunOrDuplicateIdentity`, `TestUncertainCreateReconcilesBeforeRetry`, `TestRecoverIntentUsesExactDurablePayloadAcrossConfigurationChange`. |
| R7 | pass | La proyección terminal parte solo del package verificado y limita campos, elementos y enlaces: `TestSummaryComesFromVerifiedPackage`, `TestForeignRunAndBindingPackagesFailClosed`, `TestProjectionBoundsRetainExplicitOmission`. |
| R8 | pass | Marker de primera línea exacta, bot+ID propios, paginación acotada y comparación del body durable con cero writes si coincide y un update si difiere: `TestCanonicalCommentMarkerRejectsPrefixVariants`, `TestPublisherReconcilesOwnedAndRejectsSpoofedRecords`, `TestCommentReconciliationComparesExactBody`. |
| R9 | pass | Fork denegado por defecto y única excepción mediante selección explícita sin secretos: `TestAuthorizationAndForkPolicy`, `TestComposedRuntimeAuthenticatesAndFailsClosedAtPreview`. |
| R10 | pass | Deadline anterior a la operación, retries/rate limits acotados, reconciliación previa a recreate, cancelación y join: `TestRetryDeadlineAndCredentialCategories`, `TestSubmitBudgetPrecedesExternalCallAndLeaseExcludesSecondCoordinator`, `TestSupervisorCancellationFinalizesAndJoins`, escenario REV-23. |
| R11 | pass | Journal/CAS/leases multiproceso, intents exactos recuperados antes del trabajo, batches menores de 1 MiB, rollback y handoff sin locks durante I/O: `TestClaimPendingValidatesAndCommitsOneAtomicBatch`, `TestClaimPendingDrainsBacklogInReplayableBoundedBatches`, `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore`, escenario REV-23. |
| R12 | pass | Contratos propios, adapter limitado a PR/check/comment, comentarios sistémicos verificados por AST, suite hermética, race, coverage y builds soportadas. |

T1–T11 permanecen marcadas `[x]` y su comportamiento está cubierto por pruebas
reales. No hay red ni credenciales reales y no existe `time.Sleep` en el
package. El adapter REST conserva DTOs en la frontera, no expone bodies remotos
ni tokens en errores y solo implementa los endpoints permitidos. La composición
productiva sigue fail-closed ante la dependencia de preview de feature 19.

## Gates ejecutados

| Gate | Resultado |
|---|---|
| `go test -count=20 ./internal/githubpr` | pass (20/20) |
| `go test -run '^TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff$' -count=500 ./internal/githubpr` | pass (500/500) |
| `go test -race -count=3 ./internal/githubpr` con `C:\msys64\ucrt64\bin`, `CC=gcc`, `CGO_ENABLED=1` | pass (3/3) |
| Coverage `internal/githubpr` | pass, 85,6 % |
| Race global comparable, excluyendo solo `internal/localvalidation` | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go mod verify` | pass |
| `go build ./...` | pass |
| Builds `linux/amd64`, `darwin/amd64`, `windows/amd64` | pass |
| `gofmt -l internal/githubpr cmd/featureproof` | pass |
| `git diff --check` | pass; solo avisos LF/CRLF preexistentes |
| `go test ./...` | único fallo: `internal/localvalidation/.../evidence_symlink`, Windows deniega el privilegio de symlink |

La excepción de symlink no afecta al veredicto. La feature cumple la spec y
puede pasar de `in_progress` a `done` por decisión del leader.

