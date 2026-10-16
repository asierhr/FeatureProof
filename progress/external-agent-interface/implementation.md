# Implementación de external-agent-interface

## Resultado

Se implementó un único caso de uso `internal/externalagent.Service` con contrato
de aplicación v1. CLI JSON y MCP stdio `2026-07-28` traducen hacia
`Service.Invoke`; ninguno accede al harness. El registro durable reserva
idempotencia antes del worker, usa lock entre procesos, publicación atómica y
lease OS por run. El supervisor propaga cancelación y timeout, observa la señal
durable de cancelación entre procesos y espera todo trabajo hijo.

Tras la primera review, el registro conserva además un journal inmutable dentro
de la misma publicación durable que el snapshot. Registra reserva, inicio,
solicitud de cancelación, fallo de publicación, recovery y terminal; recovery
preserva la causa cerrada `interrupted`. El canal de join se cierra únicamente
después de retirar el worker y liberar su lease.

Tras la segunda review, la idempotencia de cancelación también se particiona
globalmente por principal y operación: repetir clave y petición devuelve el
mismo resultado, mientras reutilizar la clave con otro handle produce
`conflict`. La secuencia posterior al executor usa un único contexto nuevo y
acotado para evento de fallo, transiciones de finalización y terminal. Un fallo
rápido continúa hacia `error`; un bloqueo consume el deadline, libera worker y
lease, y deja un snapshot no terminal que recovery cierra como `interrupted`.

La salida terminal funcional se construye exclusivamente desde
`localvalidation.ReadPackage`. La proyección no cruza paths, targets, cuerpos,
credenciales ni texto libre: produce handles opacos y vuelve a autorizar el run
en cada operación. La composición productiva queda deliberadamente cerrada con
`FailClosedExecutor` y sin scopes/aliases de ejecución hasta que exista el
proveedor preview confiable previsto; ningún test usa red, modelo o credenciales.

## Requirements y tests

| Requirement | Tests reales | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestSubmitDurableLifecycle`, `TestRestartReservation`, `TestDurableJournalPreservesOrderedPrefixAndSafeCauses`, `TestRecoveryAppendsInterruptedWithoutLosingConfirmedPrefix` | `internal/externalagent/service_test.go`, `review_fixes_test.go` | pass |
| R2 | `TestCLIAndMCPContractParity`, `TestContractTerminalOutcomesAndCorruption` | `internal/externalagent/adapters_test.go` | pass |
| R3 | `TestResultSafeProjection`, `TestCorruptPackageRejected`, `TestProjectionRejectsUnsafeSemanticVariants` | `internal/externalagent/service_test.go`, `boundaries_test.go` | pass |
| R4 | `TestScopeAndApprovalGate`, `TestProductionBoundaryDefaults`, `TestMCPDiscoveryAndTools` | `internal/externalagent/service_test.go`, `boundaries_test.go`, `adapters_test.go` | pass |
| R5 | `TestForeignHandleNotFound`, `TestResultSafeProjection`, `TestTrustedIdentityAndCatalogBoundaries` | `internal/externalagent/service_test.go`, `boundaries_test.go` | pass |
| R6 | `TestStructuredErrorsAndOutcomes`, `TestAdapterErrorBranches`, `TestCLIAndMCPFullContractTable` | `internal/externalagent/service_test.go`, `boundaries_test.go`, `contract_suite_test.go` | pass |
| R7 | `TestConcurrentIdempotency`, `TestRestartReservation`, `TestLocalRegistryMultiprocessIdempotency`, `TestCLIAndMCPFullContractTable` | `internal/externalagent/service_test.go`, `contract_suite_test.go` | pass |
| R8 | `TestCancelJoinsWorker`, `TestCrashRecovery`, `TestLiveOwnerIsNotRecovered`, `TestMCPServeCancelsWorkersOnEOF`, `TestJoinWaitsForWorkerRemovalAndLeaseRelease`, `TestPublicationFailureFinalizationIsBoundedAndRecoverable` | `internal/externalagent/service_test.go`, `adapters_test.go`, `review_fixes_test.go` | pass |
| R9 | `TestMCPDiscoveryAndTools`, `TestCLICapabilities`, `TestAdvertisedOutputSchemasValidateRealEnvelopes`, `TestCLIAndMCPFullContractTable` | `internal/externalagent/adapters_test.go`, `review_fixes_test.go`, `contract_suite_test.go`, `cmd/featureproof/main_test.go` | pass |
| R10 | `TestCLIAndMCPFullContractTable` | `internal/externalagent/contract_suite_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | DTO v1, JSON estricto, errores/estados cerrados y schemas. |
| T2 | done | `LocalIdentity`, revocación por operación y `AliasCatalog`. |
| T3 | done | Registro durable, CAS, hashes de claves, locks y leases multiproceso. |
| T4 | done | Supervisor cancelable, recovery sin rerun y composición productiva fail-closed. |
| T5 | done | `VerifiedPackageReader` y proyección acotada con handles opacos. |
| T6 | done | Comandos `capabilities` y `run submit/status/result/cancel --json`. |
| T7 | done | MCP stateless `server/discover`, `tools/list`, `tools/call`; `initialize` rechazado. |
| T8 | done | Contract suite común y gates finales ejecutados. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `go test ./internal/externalagent ./cmd/featureproof -count=1` | pass |
| `go test -count=20 ./internal/externalagent ./cmd/featureproof` | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -skip '^TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors$' ./... -count=1` | pass |
| `go test -race -skip '^TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors$' ./... -count=1` con MSYS2 UCRT64 GCC | pass |
| `go test -race -count=20 ./internal/externalagent ./cmd/featureproof` con MSYS2 UCRT64 GCC | pass |
| `go test -coverprofile=review_externalagent_coverage.out ./internal/externalagent` | pass |
| `go test -coverprofile=coverage.out -skip '^TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors$' ./...` | pass |
| `go mod verify` | pass |
| formato dirigido de todos los Go modificados y `git diff --check` | pass |
| `GOOS=windows/linux/darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |

## Coverage

- `internal/externalagent`: **88,9 %**.
- Global comparable: **88,7 %**, excluyendo únicamente el test ambiental de
  symlink indicado abajo.
- Las ramas relevantes de identidad, scopes, alias, JSON hostil, idempotencia,
  colisión, CAS, multiproceso, crash, worker vivo, cancelación local y remota,
  paquete corrupto, referencias ajenas, secretos, errores de transporte y
  descubrimiento están cubiertas.

## Riesgos y limitaciones

- La composición productiva no concede scopes ni aliases mutables y usa
  `FailClosedExecutor`. Es la condición segura exigida mientras feature 19 no
  aporte el proveedor preview/atestador confiable. Los casos positivos se
  prueban mediante el puerto `ApprovedExecutor` con fakes.
- En este host Windows, el comando literal `go test ./...` llega a todos los
  paquetes pero falla en el test preexistente
  `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`:
  `os.Symlink` devuelve `A required privilege is not held by the client` y el
  `Skipf` existente está ligado al `testing.T` padre, produciendo
  `test executed panic(nil) or runtime.Goexit`. No se modificó ese test porque
  pertenece a feature 15. La suite completa, coverage y race pasan al excluir
  solo ese caso ambiental. La review independiente confirmó el mismo subtest
  bajo WSL2, donde pasó sin modificar feature 15.
- `gofmt -l .` conserva el baseline histórico de archivos ajenos al diff. Todos
  los archivos Go nuevos o modificados por feature 16 pasan el check dirigido.

## Alcance

No se modificaron `feature_list.json` ni `review.md` y no se marcó la feature
como `done`. Los cambios previos del leader en `feature_list.json` y
`requirements.md` se conservaron sin alterarlos.
