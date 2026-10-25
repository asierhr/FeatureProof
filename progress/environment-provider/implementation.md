# Implementación de environment-provider

## Resultado

Feature 31 incorpora una abstracción provider-neutral de entornos, un
`LifecycleService` durable y un único `EnvironmentPort` para direct y preview.
La composición exige un catálogo explícito, versionado y sellado; no existe
provider, modelo, template, capability ni policy por defecto. El único adapter
productivo es `ExistingURLProvider`, que reutiliza el lifecycle no destructivo
de feature 19. El provider gestionado es exclusivamente un fake hermético de la
suite contractual: no se añadieron Kubernetes, cloud, shell ni infraestructura
real.

Tras cuatro reviews se corrigieron REV-1–REV-10, REV2-1–REV2-7, REV3-1–REV3-4 y REV4-1/REV4-2.
Las remediaciones conservan autoridad de acquire incierto, impiden adopción
durante la cadena provider completa, separan preparación y promoción visible,
endurecen los fallbacks Linux y fijan el toolchain de cobertura. Todos los gates Windows,
Linux y cross-platform están verdes; T1–T16 quedan completadas sin rebajar las
garantías de publicación durable.

## Trazabilidad R1–R15

| Requirement | Tests principales | Evidencia |
|---|---|---|
| R1 | `TestEnvironmentSelectionComesOnlyFromTrustedCatalog`, `TestWorkflowEnvironmentCompositionFailsClosedAtAuthorityBoundaries` | Catálogo explícito; ausencia, alias, kind, project, repo y policy incorrectos fallan antes del provider. |
| R2 | `TestEnvironmentProviderFullContractMatrix`, `TestEnvironmentValidationAndAdapterFailureMatrix` | Puerto mínimo, errores cerrados y misma matriz para existing/managed. |
| R3 | `TestValidationTargetIsCanonicalDetachedAndStrict`, `TestEnvironmentModelClosedValidationBranches`, `TestLocalStoreRejectsRollbackHolesAndImpossibleState` | Target/binding canónicos, strict decode, relaciones por estado y anti-rollback. |
| R4 | `TestEnvironmentBecomesVisibleOnlyAfterReadyAttestedAndAuthorized`, `TestEnvironmentDriftStopsExecutionRecoveryAndPublication` | Readiness, attestation, network y checkpoints de execution/publication. |
| R5 | `TestEnvironmentProviderFullContractMatrix`, `TestPreviewLifecycleBindsArtifactAndCleanup` | `ExistingURLProvider` conserva identidad feature 19 y no provisiona. |
| R6 | `TestValidationTargetIsCanonicalDetachedAndStrict`, `TestEnvironmentRecordsAreExactlyOnceContentFreeAndRecoverable` | Handle opaco, digests y records content-free. |
| R7 | `TestEnvironmentDriftStopsExecutionRecoveryAndPublication`, `TestEnvironmentRecoveryDriftAndObserverFailure` | Drift independiente bloquea execution, recovery y publicación. |
| R8 | `TestUncertainAcquireAlwaysPersistsAndExecutesRelease`, `TestEnvironmentProviderFullContractMatrix/double_uncertain_restart`, `TestDurableReleaseConvergesAcrossUncertainty`, `TestLifecycleLeaseOwnershipPreventsDuplicateProviderIO` | Release intent previo, authority estable, response loss doble, restart, CAS/adopción. |
| R9 | `TestEnvironmentLifecycleCancellationRetriesAndConcurrencyAreBounded`, `TestEnvironmentProviderFullContractMatrix` | Cancel/deadline, retries/budgets y concurrencia acotada. |
| R10 | canary/credential tests existentes más `TestWorkflowEnvironmentCompositionFailsClosedAtAuthorityBoundaries` | Credentials quedan en adapter/composición y no llegan al harness ni a records. |
| R11 | `TestEnvironmentOperationalFailuresAndPrimaryOutcomeProjectSeparately`, `TestPreviewReleasePendingRecovers` | Mapping cerrado y verdict primario visible junto a cleanup pending. |
| R12 | `TestLocalStoreCrashBoundariesConvergeRecordAndEvent`, `TestCommitDirectoryNoReplaceCoordinatesRaceAndCrash`, `TestAtomicCommitInDirectoryLinuxFallbackConvergesLinkCrash` | Staging/sync/publicación atómica, chain/head, claim descriptor-relative y redelivery exactly-once. |
| R13 | `TestFourSurfacesShareOneEnvironmentLifecycleAndDigest`, entrypoints preview de `cmd/featureproof` | CLI/API/MCP/GitHub comparten digests, transiciones y acquire/release exactly-once para direct/preview. |
| R14 | `TestEnvironmentProviderFullContractMatrix` | Pending, unavailable, drift, cancel, deadline, response loss, reconcile, redelivery, recovery y concurrency idénticos. |
| R15 | `scripts/feature31_coverage.py` y gates de este informe | Coverage exacta completa `1181/1384 = 85,332370 %`; perfiles Go 1.25.1 Windows/Linux autenticados y cross-builds verdes. |

## Resolución de hallazgos REV-1–REV-10

| Hallazgo | Resolución | Estado |
|---|---|---|
| REV-1 | `workflowConfig` carga y sella `EnvironmentCatalog`; toda dimensión obligatoria se valida antes de composición. | cerrado |
| REV-2 | `StateAcquiring` persiste `ReleaseRequired`; la incertidumbre reconcilia y libera con key ligada al lifecycle digest y `context.WithoutCancel(parent)` más timeout. | cerrado |
| REV-3 | `LocalStore` valida relaciones por estado, cadena/sequence/head anti-rollback y publicación record/event/head con staging, sync y fault injection. | cerrado |
| REV-4 | Owner/lease durable con CAS, renovación y adopción; una lease viva impide segundo I/O del provider. | cerrado |
| REV-5 | Revalidación composition-owned inmediatamente antes de execution, package publication y finalización. | cerrado |
| REV-6 | Mapping cerrado operational/cancel/timeout y proyección separada de outcome primario, estado operacional y cleanup pending. | cerrado |
| REV-7 | Suite contractual completa idéntica para existing/managed; existing valida kind e identidad en todos sus métodos. | cerrado |
| REV-8 | Vertical hermético de cuatro superficies para direct/preview sobre el mismo port y las mismas transiciones. | cerrado |
| REV-9 | Medición reproducible del diff completo desde `6e8742f`, finalmente corregida por REV2-6 con identidad de bloque completa. | cerrado |
| REV-10 | Compatibilidad Linux ante `ENOSYS`, finalmente endurecida por REV2-5 con claim descriptor-relative y recovery. | cerrado |

## Resolución de hallazgos REV2-1–REV2-7

| Hallazgo | Resolución | Estado |
|---|---|---|
| REV2-1 | `ReleaseRequest` conserva `AcquisitionKey` y autoridad de proyecto/repositorio/commit; un lifecycle desconocido permanece `release_pending`, se reconcilia tras restart y nunca libera un handle vacío ni confirma release sin autoridad. | cerrado |
| REV2-2 | La composición rechaza `LeaseTTL` que no cubra todos los boundaries provider y la espera máxima; tests deterministas prueban que una lease viva impide adopción durante I/O. | cerrado |
| REV2-3 | Runner y functional flow preparan resultados privados; el service revalida inmediatamente antes de `Promote`, y drift impide bundle/primary visible mientras cleanup confirmado converge a terminal. | cerrado |
| REV2-4 | La factory común ejecuta service/store durable para existing y managed sobre selección inválida, drift de authority, cancelación, response loss doble, restart, release incierto y concurrencia. | cerrado |
| REV2-5 | El fallback de bundles usa claim persistente, `flock`, descriptores de directorio y `renameat`; tests coordinados prueban crash antes/después, winner único, no-clobber y redelivery. El fallback de archivos reconoce hard-links equivalentes tras response loss. | cerrado |
| REV2-6 | El gate genera profile host, ingiere profiles Linux autenticados por SHA-256 y une bloques por archivo, ambas coordenadas y statement count, sin counts hardcoded. Resultado: `1122/1313 = 85,453161 %`. | cerrado |
| REV2-7 | Se verificaron y eliminaron solo los paths temporales enumerados; runner, perfiles y cross-builds de esta remediación también se retiran antes de entrega. | cerrado |

## Resolución de hallazgos REV3-1–REV3-4

| Hallazgo | Resolución | Estado |
|---|---|---|
| REV3-1 | `minimumLeaseTTL` suma Acquire+Reconcile por todos los attempts, reconcile de recovery, waits, readiness/Observe y Release, con aritmética anti-overflow; TTL igual o inferior falla antes de provider. | cerrado |
| REV3-2 | El reconcile de cleanup valida provider/version/profile, handle, availability, capabilities y un `ValidationTarget` completo con repo/commit, attestation y network antes de derivar digest o liberar. | cerrado |
| REV3-3 | Toda la tabla común compone `LifecycleService` con `LocalStore`, sin branches por adapter; cubre cancel/deadline, drift, faults/reopen/adoption, double uncertainty/tamper/restart, release recovery y concurrency con assertions idénticas. | cerrado |
| REV3-4 | El script lee Go 1.25.1 de `go.mod`, fija `GOTOOLCHAIN`, autentica manifest con versión/GOOS/GOARCH/SHA-256 y rechaza layouts incompatibles antes del merge. | cerrado |

## Resolución de hallazgos REV4-1–REV4-2

| Hallazgo | Resolución | Estado |
|---|---|---|
| REV4-1 | `validateObservationAuthority` liga template, profile y network policy, además del resto de authority, antes de `environment_acquired`, antes de bind y durante reconcile de cleanup; mismatch queda `release_pending` sin `Release`. | cerrado |
| REV4-2 | `NetworkTimeout` y `ObserverTimeout` son obligatorios, usan child contexts con cause y forman parte de la lease agregada; tests bloqueados prueban timeout/cancel, join y ausencia de adopción/I/O duplicado. | cerrado |

## Tasks

| Tasks | Estado | Evidencia |
|---|---|---|
| T1–T4 | done | Modelo/catálogo/port y fake contractual con suite compartida. |
| T5–T8 | done | Lifecycle, store/lease/CAS, budgets y release/recovery durable. |
| T9–T11 | done | Adapter feature 19, revalidación y confinamiento de credenciales. |
| T12–T15 | done | Port único, cuatro superficies, observabilidad y matrices race/adversarias. |
| T16 | done | Gates Windows/Linux/cross, coverage exacta, trazabilidad y limpieza. |

## Decisiones de implementación

- `ValidationTarget/v1` y `EnvironmentBinding/v1` usan JSON canónico, enums
  cerrados, relaciones verificadas y SHA-256. El lifecycle handle raw nunca
  entra en target, binding público, evento ni superficie model-facing.
- `LifecycleService` persiste selección, acquire, readiness, binding y release.
  La obligación de cleanup existe antes del I/O incierto; una redelivery
  reconcilia la misma identidad y una lease viva impide efectos paralelos.
- `LocalStore` conserva receipts inmutables encadenados, un head sincronizado
  anti-rollback y el evento asociado a cada transición confirmada. Los fallos
  entre boundaries convergen al mismo record/event sin huecos.
- `validationworkflow` prepara resultados en una identidad privada, revalida
  inmediatamente antes de la promoción visible y conserva el outcome funcional
  aunque cleanup permanezca pendiente.
- Direct y preview se traducen en composición a selecciones selladas y pasan
  por el mismo `LifecycleEnvironmentPort`; no quedan caminos paralelos de
  `PreviewPort` ni doble cleanup.

## Gates

| Comando | Resultado |
|---|---|
| `gofmt` dirigido y `git diff --check 6e8742f` | pass; únicamente warnings LF/CRLF del checkout |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=3 ./internal/environment ./internal/validationworkflow ./cmd/featureproof ./internal/localvalidation ./internal/contractreview` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -race -count=1` en los cinco paquetes afectados con UCRT64 | pass |
| `go test -race -count=20 -run '^TestEnvironmentProviderFullContractMatrix$' ./internal/environment` con UCRT64 | pass |
| `go test -race -count=1 -p=1 ./...` con UCRT64 | pass completo |
| cross-build `go test -c`, 3 paquetes x Windows/Linux/Darwin amd64 | pass 9/9 |
| `py -3 scripts/feature31_coverage.py --manifest coverage-manifest.json` | pass: `1181/1384 = 85,332370 %`; manifest de toolchain incorrecto falla antes del test |
| runtime Alpine WSL1 `internal/environment`, `internal/validationworkflow`, `cmd/featureproof` | pass 3/3 con binarios Linux frescos |
| runtime Alpine WSL1 `internal/localvalidation`, `internal/contractreview` | pass; ejercita fallbacks `ENOSYS` y tests Linux específicos |

## Coverage exacta del diff productivo

`scripts/feature31_coverage.py` calcula bloques de statements sobre líneas
nuevas/modificadas desde `6e8742f`, excluye tests, lee Go 1.25.1 de `go.mod`,
genera el profile Windows con ese toolchain e ingiere solo profiles Linux cuyo
manifest liga versión, GOOS, GOARCH y SHA-256. Conserva start/end column en la
key exacta, compara layouts comunes y falla por debajo de 85 % sin redondear.

Profiles de la ejecución final: host Windows
`9ad48951d19470df0783ad22401971b77e72a59c7ef2dc2f9c01485bc3411be0`;
Linux contractreview
`3650b8f9bb2da87dc8343db9421caf4d8860acb01e7f46eaed35bd5bc1a00e61`,
environment `9b1e1c2037129ce1b063de48eb843c39eef67ae7ff2c37f69369c205b0c48540`,
cmd `1bac4dfae8767f836cb4808a59b05963d2ca96cad117235de159370309ca33c7`,
localvalidation `fe627dbe657862ca7cd30414df5d21b8a2c1db7ec79d66587dd59f4368b0fcac`
y validationworkflow
`5a58f191a0a1d594e51e2b379408e7cd8ee96b571772dbf41765781d5a9d9414`.
El minirootfs Alpine 3.22.2 se verificó con
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.

| Archivo | Cubiertos/total |
|---|---:|
| `cmd/featureproof/workflow.go` | 57/71 |
| `internal/contractreview/rename_linux.go` | 16/17 |
| `internal/environment/existing.go` | 46/48 |
| `internal/environment/model.go` | 166/170 |
| `internal/environment/service.go` | 297/342 |
| `internal/environment/store.go` | 337/401 |
| `internal/environment/sync_unix.go` | 5/6 |
| `internal/environment/sync_windows.go` | 1/1 |
| `internal/localvalidation/commit_linux.go` | 44/64 |
| `internal/localvalidation/publisher.go` | 15/15 |
| `internal/validationworkflow/context.go` | 6/6 |
| `internal/validationworkflow/environment.go` | 46/50 |
| `internal/validationworkflow/functional_flow.go` | 10/16 |
| `internal/validationworkflow/local_runner.go` | 23/30 |
| `internal/validationworkflow/service.go` | 112/147 |
| **Total** | **1181/1384 = 85,332370 %** |

## Riesgos y estado de cierre

- No hay acceso a provider, red, modelo o infraestructura real en los tests.
- No se cambió `feature_list.json` ni se amplió alcance hacia feature 32+.
- Los temporales de coverage/cross-build enumerados por review y los creados por
  esta remediación se verifican y eliminan antes de la entrega.
- El fallback WSL1 solo se activa ante `ENOSYS`: archivos usan hard-link
  no-clobber y convergen al reconocer el mismo inode; bundles no vacíos usan un
  claim persistente con lock y operaciones descriptor-relative antes del rename
  atómico. El path normal `renameat2(RENAME_NOREPLACE)` no cambia.

## Remediación de Review 5

REV5-1 queda cerrado sin cambios productivos. `TestEnvironmentProviderFullContractMatrix`
crea un fixture completo por adapter y escenario, usa contexts/budgets nuevos y
coordina cancelación y deadline mediante el canal `started` del fake. El observer
inicial conserva un budget holgado y ya no puede consumir el deadline destinado
a `Acquire`; las aserciones terminales corresponden únicamente a la frontera
confirmada. La auditoría del diff no encuentra `time.Sleep`, `time.After` ni
parent deadlines cortos añadidos por feature 31.

| Gate Review 5 | Resultado |
|---|---|
| matriz común Go 1.25.1 `-count=20` | pass 20/20 |
| matriz común UCRT64 `-race -count=20` | pass 20/20 |
| global default `-count=2 ./...` | pass completo |
| global serial `-count=1 -p=1 ./...` | pass completo |
| global race serial UCRT64 `-race -count=1 -p=1 ./...` | pass completo |
| Alpine WSL1: environment/workflow/cmd/localvalidation/contractreview | pass repetido 2/2; `cmd/featureproof` 3/3 |
| cross-build Windows/Linux/Darwin amd64 | pass 9/9 |
| formato, comentarios, diff, vet y compile-only | pass |
| coverage exacta pinned con perfiles fresh Windows/Linux | **1181/1384 = 85,332370 %** |

Los hashes SHA-256 frescos son: host Windows
`9ad48951d19470df0783ad22401971b77e72a59c7ef2dc2f9c01485bc3411be0`;
Linux contractreview
`e398abdd664c7c1efd77b576602e14b5c9c28c7d7aa7ac4f69f75407e3a9d35c`,
environment
`9b1e1c2037129ce1b063de48eb843c39eef67ae7ff2c37f69369c205b0c48540`,
cmd
`430ef404d59cb8de21e407e5ab5b14c2c8d774589a7864358c568949498ae1ed`,
localvalidation
`fe627dbe657862ca7cd30414df5d21b8a2c1db7ec79d66587dd59f4368b0fcac`
y validationworkflow
`5a58f191a0a1d594e51e2b379408e7cd8ee96b571772dbf41765781d5a9d9414`.
El minirootfs Alpine continúa autenticado por
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
No hubo flakes en los gates repetidos y no se alteraron tests históricos.
