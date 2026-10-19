# Implementation — preview-environment-adapter

## Estado

`ready_for_review`

La implementación y las correcciones REV-1–REV-21 están completas. La feature reserva lógicamente previews ya desplegados, valida revisión, target y policy, ejecuta el runner compartido, publica un terminal seguro y libera la reserva. No incorpora operaciones de provisión, despliegue, actualización, reinicio, escalado, borrado o destrucción.

## Implementación

- `cmd/featureproof/preview.go` compone provider, policy, store, telemetría, runner y coordinator desde configuración confiable explícita. CLI, MCP y GitHub conservan fail-closed cuando falta o es incompleta.
- `internal/previewenv/coordinator.go`, `store.go` y `store_lock_{windows,unix}.go` persisten intención antes de reconocer trabajo asíncrono o llamar a Reserve, usan owner/lease/CAS y lock de proceso, sellan el terminal y garantizan cleanup acotado. Una misma run converge sin duplicar provider ni runner.
- `internal/previewenv/recovery.go` recupera al arrancar y al expirar la lease. Un provider reconstruido siempre ejecuta Reconcile con la key durable antes de Release; el supervisor se cancela y une con el consumidor.
- `internal/previewenv/model.go`, `catalog.go`, `target.go`, `attestor.go` y `projector.go` sellan la selección completa, canonicalizan el target antes de persistirlo y ligan target, policy, repositorio y commit al binding.
- `internal/previewenv/composition.go` filtra provider y perfil antes de resolver una referencia. Un catálogo mixto secretless realiza cero resoluciones de credenciales no seleccionadas.
- `internal/previewenv/bridges.go`, `internal/externalagent/service.go` e `internal/githubpr/*` mantienen handles durables y publican `error` previo a admisión o `inconclusive/cancelled` posterior. Un terminal operacional sin paquete usa una proyección consumer-owned segura, ligada al handle/head.
- `internal/previewenv/telemetry.go` persiste solo eventos y métricas del vocabulario cerrado: selection, reserved, readiness, ready, drift, release_requested, released, release_pending y recovery.
- `externalagent.Submit` invoca `SubmissionPreparer` de forma síncrona antes de responder `queued`. El handoff queda asociado a owner/lease durables; el worker lo adopta por CAS y recovery solo lo reclama tras crash/expiry.
- Recovery renueva una lease propia sin cambios de versión, vuelve a renovar tras Reconcile y persiste `release_pending` antes del provider. Un CAS final fallido deja cleanup durable e idempotente.

## Cierre de reviews

| Hallazgo | Corrección y evidencia | Resultado |
|---|---|---|
| REV-1 | Roots CLI/MCP/GitHub y tests de cada entrypoint | cerrado |
| REV-2 | Intent `selected`, Reconcile de resultado incierto y cleanup reserve→CAS | cerrado |
| REV-3 | Ownership, CAS, lease, lock multiproceso y convergencia de una run | cerrado |
| REV-4 | Handle GitHub durable; Status/Cancel/ResolveExisting/Wait/Close y restart | cerrado |
| REV-5 | Supervisor real conserva error pre-admission y terminal post-admission | cerrado |
| REV-6 | Filtrado antes de secretos; catálogo mixto con cero resolves prohibidos | cerrado |
| REV-7 | Provider/target hostiles rechazados antes de persistencia | cerrado |
| REV-8 | Límites independientes, parent deadline, cancel, cleanup y join reales | cerrado |
| REV-9 | EOF y `git diff --check` limpios | cerrado |
| REV-10 | `TestRecoverySupervisorReclaimsLiveLeaseAfterRealRestart` reconstruye store/coordinator/provider con lease viva y exige Reconcile antes de Release | cerrado |
| REV-11 | `TestOperationalTerminalsAreDurableForGitHub` prueba `error` e `inconclusive` terminales; recovery persiste todos los terminales que posee | cerrado |
| REV-12 | `TestExternalSupervisorPreservesAdmittedOperationalOutcome` verifica `Result`; `TestOperationalTerminalPublishesRealCheckAndComment` verifica check/comentario reales | cerrado |
| REV-13 | `TestSafeProjectorBindsCanonicalTargetAndPolicy` prueba cambios de host, puerto, path y policy en el digest | cerrado |
| REV-14 | `TestSelectionDigestRejectsCredentialModeMutation` prueba CAS/digest de `Secretless` y `CredentialReference`; Run usa la selección durable reclamada | cerrado |
| REV-15 | Tests de lifecycle, drift y recovery prueban eventos/métricas allowlisted y canarios remotos ausentes | cerrado |
| REV-16 | `TestSubmitAcknowledgesOnlyAfterDurablePreparation`, `TestExternalQueuedAcknowledgementSurvivesImmediateCloseAndRestart` y MCP ×200 prueban intent previo a `queued`, EOF inmediato y recuperación/release tras restart | cerrado |
| REV-17 | `TestRecoverySupervisorReclaimsLiveLeaseAfterRealRestart`, `TestRecoveryFinalCASFailureLeavesReleasePendingAndConverges` y `TestUnchangedExpiredOwnerRenewsBeforeCleanup` usan reloj lógico/barreras, prueban lease viva→expiry→reclaim, orden Reconcile→Release y CAS final fallido | cerrado |
| REV-18 | `TestReleaseAttemptHasBudgetIndependentFromCheckpoint` bloquea/falla el checkpoint con un store controlado y exige un intento de Release con presupuesto independiente y estado durable recuperable | cerrado |
| REV-19 | `TestHandoffLeaseSerializesWorkerAndRecovery` fuerza recovery entre prepare y worker; `TestExpiredHandoffRecoversWithoutRunner` prueba reclaim tras crash/expiry, Reconcile→Release y cero ejecuciones | cerrado |
| REV-20 | `TestPublicationFailureFinalizationIsBoundedAndRecoverable` usa barreras de lifecycle y `RunTimeout` holgado; no depende de deadlines de pared diminutos | cerrado |
| REV-21 | `Cancel` une un worker local aunque ya observe terminal; `TestCancelJoinsTerminalWorkerThroughLeaseRelease` fuerza terminal→cleanup y prueba que lease/worker se liberan antes de retornar; la espera respeta cancelación del contexto | cerrado |

## Trazabilidad

| Requirement | Tests concretos | Resultado |
|---|---|---|
| R1 | `TestExplicitAliasSelectsOneConfiguredProvider`; tests de entrypoints | pass |
| R2 | `TestReserveReturnsStableSafeSnapshot`; restart real; CAS/concurrencia | pass |
| R3 | `TestExactRepositoryCommitAttestationPrecedesExecution`; vertical localvalidation | pass |
| R4 | `TestProviderTargetPassesNetworkPolicy`; `TestDeniedTargetOpensNoDNSOrSocket`; digest target/policy | pass |
| R5 | `TestSecretsReachOnlyAuthorizedTransport`; catálogo mixto secretless | pass |
| R6 | readiness, refresh, deriva y rechecks de atestación | pass |
| R7 | timeouts independientes; parent deadline; cancelación y join | pass |
| R8 | supervisor externalagent y publicación GitHub pre/post admisión | pass |
| R9 | release en todas las salidas; recovery tras lease viva; release pendiente | pass |
| R10 | `TestProviderContractHasNoInfrastructureMutation`; adapter estático lógico | pass |
| R11 | lifecycle compartido y telemetría real en CLI/MCP/GitHub | pass |

## Tasks

T1–T12 permanecen `[x]` en `specs/preview-environment-adapter/tasks.md`. Los tests requeridos, comentarios sistémicos y verificador AST están incluidos.

## Gates finales

| Comando | Resultado |
|---|---|
| suite dirigida de `previewenv`, `githubpr`, `externalagent` y root | pass |
| `go test -count=20 ./internal/previewenv` | pass |
| paquetes afectados ×20 | pass; el test GitHub operacional usa su señal durable de idle en vez de un timeout de pared |
| MCP configurado ×200 | pass |
| regresiones handoff/recovery/CAS/checkpoint ×200 | pass |
| regresión terminal→Cancel→ReleaseOwner ×1000 | pass |
| race ×2 de paquetes afectados con GCC/UCRT64 | pass |
| REV-21 terminal→Cancel→ReleaseOwner ×1000 | pass |
| suite global comparable, excluyendo solo el parent del symlink | pass |
| race global comparable con la misma exclusión | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go mod verify` | pass |
| `go build ./...` nativo, windows/amd64, linux/amd64 y darwin/amd64 | pass |
| gofmt de todos los archivos Go nuevos/modificados | pass |
| `git diff --check` | pass; solo avisos LF→CRLF del checkout |
| `TestPreviewSystemicCommentsCoverChangedSymbols` | pass |

Coverage final: `internal/previewenv` **85,4 %**, `internal/githubpr` **85,1 %** y `internal/externalagent` **87,4 %**.

## Limitaciones ambientales

`go test ./...` reproduce únicamente el fallo preexistente `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`: Windows rechaza crear el symlink sin el privilegio requerido y el helper termina el parent test. No se modificó ni debilitó esa prueba; la suite y race comparables excluyen exclusivamente ese paquete y pasan.

El chequeo `gofmt -l .` también enumera archivos históricos ajenos a feature 19. Para respetar el alcance de una sola feature no se reescribieron; todos los Go nuevos o modificados por esta implementación pasan `gofmt -l`.

## Seguridad y límites

- El estado durable contiene selección tipada, IDs/digests y snapshots seguros; nunca secretos, bodies o mensajes del provider.
- El target se autoriza antes de DNS/socket y se revalida durante readiness, ejecución y publicación.
- Recovery no repite una ejecución funcional confirmada y reutiliza keys estables de reserva/liberación.
- Toda goroutine tiene owner, cancelación y join; cleanup tiene deadline propio.
