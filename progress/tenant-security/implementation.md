# Implementación de tenant-security

Fecha: 2026-11-20
Estado final del leader: `done` tras review independiente aprobada.

## Resultado de esta iteración

Se completaron T1–T8. Además del modelo, RBAC y resolución de principal, la
persistencia base queda cerrada con un contrato común local/PostgreSQL. El
adapter local publica snapshots y receipts checksummed bajo lock de proceso y
OS, con CAS, fsync+rename y resolución de commit incierto. PostgreSQL 16 aplica
la migración `0021`, backfill/compatibilidad N/N+1, security epoch, predicates
compuestos y transacciones serializables sin fallback.

La feature todavía no está preparada para review final: faltan garantías explícitas
de T6–T24, especialmente administración transaccional con audit, propagación tenant a stores/queue/jobs,
integración productiva en CLI/API/MCP/GitHub/external-agent/workers, controller
de retención completo, quotas delante de cada consumer y gate PostgreSQL real.

## Trazabilidad disponible

| Requirement | Tests actuales | Resultado |
|---|---|---|
| R1, R2, R3, R4 | `TestRBACScopeAndRevocationFailClosed`, `TestRoleCompilationDefensiveAndClosed` | pass |
| R8, R9, R10 | `TestSecretCiphertextLeaseSingleUseAndRevocation` | pass |
| R11 | `TestScopedCacheTransportAndConvergentDeletion` | pass parcial |
| R12 | `TestAuditChainDetectsTamper` | pass |
| R15 | `TestScopedCacheTransportAndConvergentDeletion` | pass parcial |
| R16 | `TestQuotaConcurrentLastSlotAndReleaseOnce` | pass |
| R17, R18 | cancellation/hidden errors y canary tests | pass parcial |
| R5, R13, R17, R19 | `TestStoreContract`, `TestStoreSerializesSecondProcess`, `TestStoreUnknownCommitAndFaultBoundaries`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityFaultsAndCorruption`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` | local/PG16 normal+race pass |

R5–R7, R13–R14 y R20 aún no tienen cobertura vertical suficiente.

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1–T8 | done | Marcadas `[x]`; core, local, PG16 y N/N+1 verificados |
| T9–T10 | done | Stores, queries, queue/inbox/outbox, workers, caches y objetos tenant-scoped; PG16 normal/Linux/race pass |
| T11–T16 | done | Secrets, leases, rotation/revocation, transport, audit y sensitive transaction verificados |
| T17–T24 | pending | No se marcan hasta cerrar todos sus criterios |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `go test ./internal/tenantsecurity/... -count=3` | pass |
| `CGO_ENABLED=1 go test -race ./internal/tenantsecurity/... -count=3` (UCRT64) | pass |
| `scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -CCompiler C:\msys64\ucrt64\bin\gcc.exe` | pass; PG16 normal/Linux/race y N/N+1; cleanup vacío |
| `go test ./internal/controlplane -run TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -p=1 ./... -count=1 -timeout=12m` | pass |
| `git diff --check` | pass; solo avisos CRLF del checkout |

## Archivos principales

- `internal/tenantsecurity/`: modelo y servicios.
- `internal/tenantsecurity/local/`: snapshot local atómico.
- `internal/controlplane/postgres/tenant_security.go`: adapter PostgreSQL.
- `internal/controlplane/postgres/migrations/0021_tenant_security.sql`: expand schema.

## Continuación

No existe bloqueo externo. T4–T5 están terminadas y verificadas; la feature no
se envía aún a review porque T9–T24 siguen pendientes. `feature_list` permanece
intacto y no se hizo commit/push.

## Handoff T9–T10 parcial, superado por el cierre posterior (2026-11-20)

En este punto intermedio T6–T8 estaban completas y T9/T10 permanecían abiertas. El árbol compilaba normal y
con `postgres_integration`, y `git diff --check` no presenta errores. El focal
PostgreSQL 16 real de cleanup-capacity, cleanup scheduling y
environment-capacity pasa; también pasan
`TestPostgresConsumerAdapterMatrix/correction_lineage_budget_and_recovery` y
`TestPostgresCorrectionStoreSharedContract` (`7.990s`).

Cambios ya estabilizados: `BackendCoordinate` incluye organización/proyecto;
projectconfig y sus artifacts usan la key triple; queue/bindings/inbox/effects
usan predicates compuestos; environment/capacity/cleanup y las mutaciones de
correction verifican el mismo scope/fence; retention references escriben la FK
compuesta. Los fixtures PG registran ownership de forma explícita y no existe
bootstrap productivo.

Pendiente exacto para el relevo: migrar los contratos de lecturas, scans y
cursores de correction/fixture evidence que hoy no reciben scope (incluido
`ReleaseOwner`); workflow/materials y durable reserve; functional/context;
offline import y retention/delete; authority SQL restante y auditoría final de
job events/outbox. La repetición Linux de la gate sigue roja precisamente en
esas rutas (`external agent`, workflow material, import sin coordinate,
functional, context, outbox y retention). No se marcaron T9/T10 ni se usó esa
gate roja como evidencia de cierre; cada bootstrap WSL terminó su cleanup.

## Cierre T9–T10 (2026-11-20)

Se completó la migración de contratos consumer-owned a scope compuesto. Las
lecturas y mutaciones de workflow/materials/packages, correction/fixtures,
functional/context, import/retention, environment/capacity/cleanup,
external-agent y approval requieren autoridad explícita y ocultan same-ID de
otro tenant. La migración `0021` incorpora keys, FKs, backfill e índices
compuestos; cursors y counts quedan ligados al tenant sin oracle global.

Queue, inbox/outbox, effects, authority y job events comparten
organization/project/run/epoch/fence. El worker productivo presenta una
`BackendCoordinate` derivada del `AuthorityEnvelope` live antes de cruzar al
workflow y éste la sustituye por su binding exacto tras la lectura atómica. No
hay seeds productivos implícitos: el ownership se registra sólo desde
administration/composition confiable.

Verificación terminal: focales PostgreSQL exactos verdes; gate PG16 normal y
Linux/N-N+1 verde; gate race UCRT verde y cleanup aislado exit 0. También pasan
global serial, `go vet ./...` y `git diff --check`. T9 y T10 se marcan `[x]`;
T11–T24 siguen pendientes. No se modificó `feature_list.json` y no hubo
commit/push.

## Cierre T11–T16 (2026-11-20)

Se sustituyó el prototipo de secretos en memoria por un servicio durable sobre
el contrato consumer-owned. `KeyProvider` y `RandomSource` permanecen privados;
la AAD canónica liga organization/project/reference/version/purpose/key/algorithm
y los stores reciben únicamente metadata, nonce y ciphertext. Versiones,
tombstones y leases son inmutables o avanzan por CAS; rotación/revocación,
consumo single-use y service identities conservan receipts y audit causal.

Las leases JIT quedan ligadas a run, phase, consumer, purpose, fence y expiry.
El servicio descifra fuera de la transacción, confirma consume+audit antes del
callback privado y borra el buffer al salir. Replay, expiry, lease forjada,
cancelación, revocation-vs-resolve y carrera de dos consumers fallan cerrados.
Los canaries no aparecen en snapshot JSON ni en filas PostgreSQL de
metadata+ciphertext.

`SensitiveExecutor` generaliza mutation+audit+receipt atómicos, recupera unknown
commit mediante receipt y mantiene el deny aunque su auditoría best-effort
falle. Audit usa eventos append-only, sequence/previous digest y un head CAS
independiente que detecta alteración, reorden y borrado de cola; las consultas
son project-scoped, limitadas y usan cursor sellado con expiry.

La composición PostgreSQL valida TLS, peer authentication y server name antes
de conectar; reconnect no puede degradar la postura. Plaintext loopback sólo se
acepta con autorización explícita del gate aislado y host realmente loopback.
La migración `0022_tenant_secret_audit.sql` añade leases y audit heads, hace
posibles tombstones sin ciphertext, e impone append-only a versions/events.
La proyección PostgreSQL verifica counts/digests y lee aggregate+projection en
una transacción read-only repeatable-read para evitar falsos integrity durante
publicación concurrente.

Verificación terminal: focales core/local, comentarios sistémicos y compile con
`postgres_integration` pasan. Gate hermético PostgreSQL 16/N-N+1/Linux/race UCRT
exit 0: normal `cmd 26.989s`, `postgres 99.720s`, `planapproval 0.530s`; race
`cmd 45.658s`, `postgres 103.315s`, `planapproval 3.152s`. Cleanup comprobado:
cero distros, roots o procesos del gate. Global serial, `go vet ./...` y
`git diff --check` pasan sobre el árbol final. T11–T16 quedan `[x]`; T17–T24 siguen
pendientes. `feature_list.json` permanece intacto y no hubo commit/push.

## Cierre T17–T20 (2026-11-20)

Retención usa siete clases cerradas con floors organizativos, periodos de
proyecto, records/classes protegidos, límites de batch/grafo y digest canónico
sin alias de maps/slices. Cada candidato queda sellado con terminalidad,
referencias, jobs/outbox/cleanup, secret activo y boundary temporal; el intent
persiste policy version/digest y eligibility digest, por lo que cambios futuros
no reatribuyen trabajo ya planificado.

El controller durable confirma intent+audit antes de observar o borrar, entrega
al deleter consumer-owned candidate/delete digest y fence, revalida referencias
vivas, conserva estados inciertos y converge tras restart mediante ausencia
observada. Dependency graph ordena environment/evidence/secret antes de
run/config/project; project deletion suspende admisión y revoca service
identities/leases mientras mantiene autoridad mínima de retention. Tombstones
protegidos cierran causalmente cada eliminación.

Quota evalúa tenant y project juntos con window, burst, maximum outstanding y
TTL versionados. Reservation, ambos counters y audit se confirman por CAS antes
del efecto; redelivery idéntica devuelve receipt y demanda/policy distinta
conflict. Unknown conserva capacity, release/expiry decrementa ambos niveles
exactamente una vez y overflow/policy drift falla cerrado. La contract suite
local/PostgreSQL disputa el último slot desde dos handles sin sleeps.

Los consumers productivos tienen guards obligatorios: `QuotaJobQueue` antes de
queue claim, `QuotaGenerationPort` antes de modelo y tokens,
`QuotaEnvironmentProvider` antes de provider/capacity y
`QuotaScopedRepository` reserva object+storage antes del put. Constructors sin
guard fallan y los tests comprueban denial con cero llamadas al consumer; la
composición concreta de capability/policy permanece en los puertos
consumer-owned que T21 conectará a cada superficie autenticada.

La migration append-only `0023_tenant_retention_quota.sql` amplía las tablas
N/N+1 y añade counters normalizados, policy/eligibility/fence, índices scopeados
y checksums verificados contra el aggregate. Gate aislado PostgreSQL 16 completo
verde: normal `cmd 28.183s`, `postgres 103.261s`, `planapproval 0.489s`; race
UCRT `cmd 44.851s`, `postgres 104.307s`, `planapproval 6.316s`; N/N+1/Linux y
cleanup sin distro/root también pasan. Global serial, `go vet ./...`, auditoría
de comentarios y `git diff --check` pasan. T17–T20 quedan `[x]`; T21–T24 siguen
pendientes, sin tocar `feature_list.json`, commit ni push.

## Implementación T21–T24 en curso (2026-11-20)

`DurableAuthority` resuelve subject y autoriza siempre contra el snapshot durable
más reciente; `SurfaceGateway` exige providers confiables para las seis
superficies y no acepta principal, membership, tenant ni resource identity desde
payload. La composición productiva carga un documento no secreto obligatorio,
selecciona store local explícito o PostgreSQL sin fallback, valida limits y quota
policy, y falla antes del consumer si falta una superficie/provider.

| Superficie | Acción revalidada | Quota guard productivo | Consumer protegido |
|---|---|---|---|
| CLI workflow | `run.admit` + `quota.reserve` | `QuotaGenerationPort`; managed provider mediante `QuotaEnvironmentProvider` | modelo y environment provider |
| API/MCP | `config.read` o `run.admit`; external-agent revalida admit/read/cancel por llamada | guards del workflow o external-agent | service/handler compartido |
| GitHub local | `run.admit` por cada `Coordinator.Authorize` | generation/environment del workflow GitHub | validación síncrona; no existe claim worker local |
| GitHub PostgreSQL | `run.admit` por admission y `run.read` worker | anteriores + `QuotaJobQueue` | workflow, provider y scheduler claim |
| external-agent | admit/read/cancel en cada `Identity.Resolve` | `QuotaGuard` delante del executor y reservas nested object+storage | preview environment y commit run/artifact |
| worker | `run.read` desde provider `sealed_worker_envelope` | `QuotaJobQueue` | claim organization/project scoped |

Los tests productivos retiran config/surface/guard y verifican fallo cerrado;
quota exhaustion del external-agent produce cero llamadas al executor y una
revocación live de GitHub produce cero llamadas al authorizer downstream. La
identidad de quota de generación procede del workflow handle en context privado,
no del model/payload.

T22 añade clasificación PostgreSQL por SQLSTATE sin comparar mensajes, errores
redactados y observer allowlisted aislado de panic. El manifest exacto de feature
37 incluye todo el inventario Go desde la base aprobada y el gate admite
`-Feature37Coverage`.

## Cierre canónico T24 (2026-11-25)

Esta sección sustituye el estado parcial de los handoffs históricos anteriores.
La implementación completa T1–T24 queda preparada para revisión independiente;
`feature_list.json` permanece intacto y no se hizo commit ni push.

### Requirements y tests

| Requirement | Tests conductuales | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestAdministrationMutationsRemainAtomicAndVersioned`, `TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates` | `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R2 | `TestDurableAuthorityReloadsMembershipAndSecurityHead`, `TestDurableAuthorityHidesCrossTenantExistence` | `internal/tenantsecurity/authority_test.go` | pass |
| R3 | `TestRBACScopeAndRevocationFailClosed`, `TestRoleCompilationDefensiveAndClosed` | `internal/tenantsecurity/tenantsecurity_test.go` | pass |
| R4 | `TestScopedRuntimeVerticalCoversAdmissionRecordsQueueAndMessages`, `TestScopedRuntimeRejectsInvalidCompositionAndCoordinates` | `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R5 | `TestStoreContract`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` | `internal/tenantsecurity/local/store_test.go`, `internal/controlplane/postgres/integration_test.go` | pass local/PG16 |
| R6 | `TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys`, `TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim` | `internal/tenantsecurity/coverage_vertical_test.go`, `internal/controlplane/quota_queue_test.go` | pass |
| R7 | `TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability`, `TestScopedRuntimeUpdatesAndIntegrityChecks` | `internal/projectconfig/tenant_security_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R8 | `TestSecretRotationCASAndCiphertextOnlyPersistence`, `TestPostgresTenantSecretsAuditAndLeaseRace` | `internal/tenantsecurity/security_advanced_test.go`, `internal/controlplane/postgres/integration_test.go` | pass local/PG16 |
| R9 | `TestSecretLeaseConsumesAndAuditsBeforePrivateCallback`, `TestSecretConsumeRejectsDurableLeaseDrift` | `internal/tenantsecurity/security_advanced_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R10 | `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretRotationCASAndCiphertextOnlyPersistence` | `internal/tenantsecurity/security_advanced_test.go` | pass |
| R11 | `TestTransportSecurityFailsClosedAndPinsReconnect`, `TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention` | `internal/tenantsecurity/security_advanced_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R12 | `TestAuditChainDetectsTamper`, `TestAuditPaginationScopesCursorsAndDetectsTailTamper` | `internal/tenantsecurity/tenantsecurity_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R13 | `TestAdministrationFaultsAreAtomicAndUnknownCommitResolves`, `TestSensitiveUnknownCommitAndDenyNeverGrants` | `internal/tenantsecurity/local/store_test.go`, `internal/tenantsecurity/security_advanced_test.go` | pass |
| R14 | `TestRetentionPolicyEligibilityFreezesBoundary`, `TestRetentionControllerRejectsStalePlansAndTamperedIntents` | `internal/tenantsecurity/retention_quota_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R15 | `TestRetentionControllerConvergesUnknownDelete`, `TestProjectDeletionBlocksAdmission`, `TestRetentionBatchAndGraphRespectDependencyOrdering` | `internal/tenantsecurity/retention_quota_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R16 | `TestDurableQuotaHierarchyLastSlotReplayAndRelease`, `TestQuotaGuardOrdersEffectsAndRetainsUnknownReservation`, `TestProductExternalQuotaDenialDoesNotCallExecutor` | `internal/tenantsecurity/retention_quota_test.go`, `cmd/featureproof/tenant_security_test.go` | pass |
| R17 | `TestClosedBoundaryAdversarialMatrix`, `TestSnapshotValidationRejectsEveryForgedProjectionFamily`, `TestTenantSecurityErrorMappingCoversContextAndTypedFailures` | `internal/tenantsecurity/coverage_vertical_test.go`, `internal/controlplane/postgres/tenant_security_error_test.go` | pass |
| R18 | `TestSubjectDigestDoesNotPersistRawSubject`, `TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift` | `internal/tenantsecurity/tenantsecurity_test.go`, `internal/tenantsecurity/coverage_vertical_test.go` | pass |
| R19 | `TestStoreRestartAndCorruption`, `TestStoreSerializesIndependentHandles`, `TestPostgresTenantSecurityFaultsAndCorruption`, `TestPostgresGate` | `internal/tenantsecurity/local/store_test.go`, `internal/controlplane/postgres/integration_test.go` | pass local/PG16/N-N+1 |
| R20 | `TestSurfaceGatewayRequiresAndConvergesEveryProductSurface`, `TestEveryProductIngressFailsClosedWithoutTenantComposition`, manifest y gate diferencial | `internal/tenantsecurity/authority_test.go`, `cmd/featureproof/tenant_security_test.go`, `scripts/feature37_coverage.py` | pass |

### Verificación terminal

| Gate | Resultado |
|---|---|
| `gofmt -l .` y `git diff --check` | pass; sin archivos sin formato ni whitespace errors; sólo avisos informativos LF/CRLF |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -p=1 -count=1 -timeout=20m ./...` | pass |
| `CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -p=1 -race -count=1 -timeout=25m ./...` | pass |
| focal UCRT `go test -p=1 -race -count=20 -timeout=30m` sobre tenantsecurity, controlplane, environment, projectconfig y CLI | pass; CLI `864.535s`, controlplane `219.849s`, environment `177.250s` |
| `CGO_ENABLED=0 go build ./...` para Windows/Linux/Darwin, amd64/arm64 | seis builds pass |
| `go mod verify` | pass, `all modules verified` |
| auditoría AST `TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` | pass |
| secrets/canaries (`Test(Secret|Sensitive|Transport|Audit)` ×20 y composición productiva ×3) | pass; scan de private keys/tokens limpio |

### Cobertura diferencial y PostgreSQL 16

- Base exacta del manifest: `1b0162237d941a72429a519076e9ea14e331064d`.
- Inventario cerrado: 75 fuentes productivas, 41 fuentes de test y 2 fuentes
  productivas sin statements.
- Comando terminal:
  `scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage -Feature37Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`.
- Normal, race UCRT, runtime Linux, migrations N/N+1, contract suites y ambos
  stores Unix pasaron sobre PostgreSQL 16 aislado.
- Cada database independiente generó dos juegos internos de perfiles. Ambas
  mediciones produjeron exactamente
  `4008/4715 = 85.00530222693531283138918345705196182397%`.
- Las firmas completas por identidad de bloque+boolean fueron iguales dentro de
  cada medición y entre las dos databases; el gate compara SHA-256 y terminó
  con exit 0. El hash era efímero dentro del root UUID y no se imprime.
- Un gate previo descubrió no determinismo en el rechazo TOCTOU de consumo de
  lease. `TestSecretConsumeRejectsDurableLeaseDrift` reproduce el cambio entre
  preflight y mutación, exige `revoked` y callback cero; focal ×10, race y el
  gate terminal quedaron verdes sin relajar el comparador ni excluir ramas.

### Cleanup y riesgos

- Tras el gate: 0 distros WSL, 0 roots `featureproof-pg35-gate-*`, 0 procesos
  `go`/`postgres` pertenecientes al gate.
- Se eliminaron únicamente los perfiles/binarios Feature37 demostrados
  (`$p1`, `$p2`, `$profile`, `$taskCoverage`, `feature37-local`), los dos `.pyc`
  del generador, `tenant-security/security.lock` y los perfiles focales exactos
  `feature37-secret-*` creados durante el diagnóstico; no se tocó el estado
  `github-pr` preexistente.
- No quedan secretos, canaries, perfiles, binarios, pycache o locks de esta
  verificación. No hay limitaciones conocidas ni gates pendientes.

## Remediación REV-1–REV-7 y cierre terminal definitivo (2026-11-25)

Esta sección sustituye íntegramente las cifras y comandos del cierre canónico
anterior. La revisión independiente descubrió siete defectos reales; todos se
corrigieron con verticales conductuales antes de repetir los gates completos.

### Hallazgos de review cerrados

| Review | Causa y corrección productiva | Evidencia conductual | Estado |
|---|---|---|---|
| REV-1 | Las superficies remotas podían usar identidad estática. API, MCP, GitHub, external-agent y worker resuelven ahora un subject autenticado por request desde transporte confiable; MCP deriva la credencial del protocolo, nunca de config o argumentos. | `TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects`, `TestMCPAuthenticatesEachToolCallIndependently`, `TestContractMCPAuthenticatesEveryRequestSubject`, `TestProjectMemoryTenantResolverAuthenticatesEveryRequest` | cerrado |
| REV-2 | El binding tenant-aware existía sólo en tests y el flujo productivo conservaba admisión legacy. La composición usa `TenantRunBinding`, projectconfig tenant-aware y una saga durable `Prepare`/`Confirm`: persiste intent no runnable antes del workflow y reconcilia restart sin rebinding. | `TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation`, `TestPostgresProjectConfigurationWorkflowCausalCommit`, `TestPostgresExternalAdmissionIsCausalAtomicAndRestartable` | cerrado |
| REV-3 | `WithSecret` no autenticaba toda la identidad del caller. El consumo exige coincidencia exacta de run, phase, consumer, purpose y fence antes del callback privado. | `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretConsumeRejectsDurableLeaseDrift` | cerrado |
| REV-4 | Allow/deny podía perder audit y un fallo al auditar denial era descartado. La autoridad confirma ambos outcomes en journal durable, falla cerrada ante fallo de audit y pseudonimiza subjects desconocidos atribuibles sin revelar existencia. | `TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure`, `TestDurableAuthorityFailsClosedBeforeDecisionAudit` | cerrado |
| REV-5 | Environment/worker podían liberar quota por asociación sólo en memoria. Reservations ligan duraderamente resource IDs, sobreviven restart y liberan exactamente una vez sólo tras terminalidad de todo el batch. | `TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion`, `TestQuotaJobQueueRestartReleasesExactRecoveredJobs`, `TestQuotaJobQueueRecoveryCompatibilityFailsClosed`, `TestTenantQuotaGateCoversProductReservationLifecycles` | cerrado |
| REV-6 | La trazabilidad R20 no demostraba las superficies corregidas. Se añadieron verticales de dos tenants/requests, spoof, ausencia de identidad, causal admission, audit denial y lifecycle quota; el mapa R1–R20 inferior las referencia. | verticales de REV-1–REV-5 y `TestComposeTenantSecurityRejectsEveryIncompleteTrustBoundary`, `TestProductSurfaceHelpersFailClosedWithoutRequestIdentity` | cerrado |
| REV-7 | El race canónico fallaba bajo carga por deadlines de sincronización y polling wall-clock. Las esperas de test usan `t.Context()`/eventos causales y los polls productivos se acotan por observaciones, conservando timeouts contractuales. | focales race repetidas y `go test -race -count=1 -timeout=25m ./...` exacto, sin `-p=1` | cerrado |

### Trazabilidad R1–R20 definitiva

| Requirement | Evidencia principal |
|---|---|
| R1 | `TestAdministrationMutationsRemainAtomicAndVersioned`, `TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates` |
| R2 | `TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects`, `TestMCPAuthenticatesEachToolCallIndependently`, `TestProjectMemoryTenantResolverAuthenticatesEveryRequest` |
| R3 | `TestRBACScopeAndRevocationFailClosed`, `TestContractMCPAuthenticatesEveryRequestSubject`, `TestProductSurfaceHelpersFailClosedWithoutRequestIdentity` |
| R4 | `TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation`, `TestPostgresProjectConfigurationWorkflowCausalCommit`, `TestScopedRuntimeVerticalCoversAdmissionRecordsQueueAndMessages` |
| R5 | `TestStoreContract`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` |
| R6 | `TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys`, `TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim` |
| R7 | `TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability`, `TestPostgresProjectConfigurationWorkflowCausalCommit` |
| R8 | `TestSecretRotationCASAndCiphertextOnlyPersistence`, `TestPostgresTenantSecretsAuditAndLeaseRace` |
| R9 | `TestSecretLeaseConsumesAndAuditsBeforePrivateCallback`, `TestSecretTamperExpiryRevocationAndCancellationFailClosed` |
| R10 | `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretRotationCASAndCiphertextOnlyPersistence` |
| R11 | `TestTransportSecurityFailsClosedAndPinsReconnect`, `TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention` |
| R12 | `TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure`, `TestAuditPaginationScopesCursorsAndDetectsTailTamper` |
| R13 | `TestDurableAuthorityFailsClosedBeforeDecisionAudit`, `TestSensitiveUnknownCommitAndDenyNeverGrants`, `TestAdministrationFaultsAreAtomicAndUnknownCommitResolves` |
| R14 | `TestRetentionPolicyEligibilityFreezesBoundary`, `TestRetentionControllerRejectsStalePlansAndTamperedIntents` |
| R15 | `TestRetentionControllerConvergesUnknownDelete`, `TestProjectDeletionBlocksAdmission`, `TestRetentionBatchAndGraphRespectDependencyOrdering` |
| R16 | `TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion`, `TestQuotaJobQueueRestartReleasesExactRecoveredJobs`, `TestTenantQuotaGateCoversProductReservationLifecycles` |
| R17 | `TestClosedBoundaryAdversarialMatrix`, `TestSnapshotValidationRejectsEveryForgedProjectionFamily`, `TestTenantSecurityErrorMappingCoversContextAndTypedFailures` |
| R18 | `TestSubjectDigestDoesNotPersistRawSubject`, `TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift` |
| R19 | `TestStoreRestartAndCorruption`, `TestStoreSerializesIndependentHandles`, `TestPostgresTenantSecurityFaultsAndCorruption`, gate PostgreSQL 16 N/N+1 |
| R20 | Todas las verticales REV anteriores, manifest exacto y gates global/race/coverage terminales |

### Gates terminales sobre el árbol definitivo

| Gate | Resultado |
|---|---|
| `gofmt -l cmd internal`; `git diff --check` | pass; 0 archivos sin formato, sólo avisos informativos CRLF |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -timeout=20m ./...` | pass |
| `CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=1 -timeout=25m ./...` | pass exacto, sin `-p=1`; `cmd 92.586s`, `contextassembly 35.237s`, `contractreview 38.012s`, `correction 63.166s`, `githubcontext 3.133s`, `localvalidation 38.279s`, `validationworkflow 93.044s` |
| seis `CGO_ENABLED=0 go build ./...` para Windows/Linux/Darwin amd64/arm64 | pass |
| `go mod verify` | pass, `all modules verified` |
| auditorías sistémicas de comments en controlplane, validationworkflow y githubpr | pass |
| secrets/authority focal ×20 y escaneo de 140 archivos cambiados | pass; 0 patrones de credenciales |

### Coverage exacta, PostgreSQL 16 y cleanup

- Manifest base: `1b0162237d941a72429a519076e9ea14e331064d`.
- Inventario exacto: 82 fuentes productivas, 47 fuentes de test, 2 fuentes
  productivas sin statements y 15 paquetes unitarios; no faltan Go modificados
  o untracked de Feature37.
- Comando terminal: `scripts/feature35-postgres-gate.ps1
  -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage
  -Feature37Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`.
- PostgreSQL 16 normal: `cmd 30.207s`, `postgres 125.385s`, `planapproval
  0.538s`. Race UCRT: `cmd 47.531s`, `postgres 133.660s`, `planapproval
  3.153s`. Migrations empty/N/N+1, runtime Linux y contract suites pasaron.
- Dos ejecuciones exteriores independientes, cada una con dos recolecciones
  internas Unix/Linux, produjeron exactamente `4379/5145 =
  85.11175898931000971817298347910592808552%`. El desglose fue idéntico
  (`cmd/featureproof/tenant_security.go` `188/200`) y el comparador estricto de
  identidad de bloque+boolean terminó con exit 0; no se relajó ni excluyó rama.
- Cleanup final: 0 roots `featureproof-pg35-gate-*`, 0 distros WSL FeatureProof,
  0 procesos owned. Se eliminaron sólo el `.pyc` exacto generado por coverage y
  17 perfiles diagnósticos `f37-*` demostrados de esta verificación; no quedan
  profiles, binaries, locks o temporales Feature37.
- `feature_list.json` permanece intacto; no se hizo commit ni push. T24 queda
  completo y la Feature37 está lista para revisión independiente.

## Cierre tras segunda re-review (2026-11-25)

Esta sección es la evidencia terminal definitiva y sustituye las cifras y
conclusiones de cierre anteriores. Los cuatro blockers de la segunda re-review
se reprodujeron, corrigieron y revalidaron sin cambiar `feature_list.json`, sin
commit y sin push.

### Blockers corregidos

| Blocker | Corrección productiva | Tests conductuales terminales |
|---|---|---|
| B1: identidad MCP por request | Workflow y external-agent reciben una credencial opaca desde el transporte MCP en cada `tools/list` y `tools/call`; el authenticator revalida el subject contra `SurfaceGateway` vivo. Config, argumentos y principal fallback no pueden suplantarla. | `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`, `TestContractMCPAuthenticatesEveryRequestSubject`, `TestWorkflowMCPAuthenticatesEveryRequest`, `TestAuthenticatedMCPResolvesEveryRequestPrincipal`, `TestRequestIdentityFallbackAndDenial` |
| B2: admisión causal | `TenantRunAdmission.Prepare` persiste un intent no runnable antes de reservar workflow; sólo `Confirm` publica el binding. `Recover` enumera intents durables y reconcilia tanto huérfanos como workflow/binding pendiente tras crash, incluido un proceso fresco. | `TestTenantAdmissionCrashBoundariesReconcileOnFreshService`, `TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation`, `TestPostgresExternalAgentProductCompositionOwnsCausalAdmission` |
| B3: quota external-agent | La reserva environment se liga al run durable y se libera sólo después de terminal/cancel/error o recovery observado. `RecoverTerminal` devuelve exactamente los runs terminalizados y el release es idempotente tras restart/replay; runs outstanding conservan capacidad. | `TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle`, `TestProductExternalQuotaWrappersResolveEffectsAndDelegateLifecycle`, `TestScopedExternalRegistryPreservesTenantLifecycle` |
| B4: race canónico | Las esperas de sincronización no contractuales en contract-review/GitHub usan contexto y señales causales; los timeouts de comportamiento permanecen. La suite canónica ejecuta paquetes concurrentemente, sin `-p=1`. | `go test -race -count=1 -timeout=25m ./...` y focal GitHub race x20 |

### Trazabilidad R1-R20 terminal

| Requirement | Evidencia principal |
|---|---|
| R1 | `TestAdministrationMutationsRemainAtomicAndVersioned`, `TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates` |
| R2 | `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`, `TestAuthenticatedMCPResolvesEveryRequestPrincipal`, `TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects` |
| R3 | `TestContractMCPAuthenticatesEveryRequestSubject`, `TestWorkflowMCPAuthenticatesEveryRequest`, `TestProductSurfaceHelpersFailClosedWithoutRequestIdentity` |
| R4 | `TestTenantAdmissionCrashBoundariesReconcileOnFreshService`, `TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation`, `TestPostgresProjectConfigurationWorkflowCausalCommit` |
| R5 | `TestStoreContract`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` |
| R6 | `TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys`, `TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim` |
| R7 | `TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability`, `TestPostgresProjectConfigurationWorkflowCausalCommit` |
| R8 | `TestSecretRotationCASAndCiphertextOnlyPersistence`, `TestPostgresTenantSecretsAuditAndLeaseRace` |
| R9 | `TestSecretLeaseConsumesAndAuditsBeforePrivateCallback`, `TestSecretConsumeRejectsDurableLeaseDrift` |
| R10 | `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretRotationCASAndCiphertextOnlyPersistence` |
| R11 | `TestTransportSecurityFailsClosedAndPinsReconnect`, `TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention` |
| R12 | `TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure`, `TestAuditPaginationScopesCursorsAndDetectsTailTamper` |
| R13 | `TestDurableAuthorityFailsClosedBeforeDecisionAudit`, `TestSensitiveUnknownCommitAndDenyNeverGrants`, `TestAdministrationFaultsAreAtomicAndUnknownCommitResolves` |
| R14 | `TestRetentionPolicyEligibilityFreezesBoundary`, `TestRetentionControllerRejectsStalePlansAndTamperedIntents` |
| R15 | `TestRetentionControllerConvergesUnknownDelete`, `TestProjectDeletionBlocksAdmission`, `TestRetentionBatchAndGraphRespectDependencyOrdering` |
| R16 | `TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle`, `TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion`, `TestQuotaJobQueueRestartReleasesExactRecoveredJobs` |
| R17 | `TestClosedBoundaryAdversarialMatrix`, `TestSnapshotValidationRejectsEveryForgedProjectionFamily`, `TestTenantSecurityErrorMappingCoversContextAndTypedFailures` |
| R18 | `TestSubjectDigestDoesNotPersistRawSubject`, `TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift` |
| R19 | `TestStoreRestartAndCorruption`, `TestStoreSerializesIndependentHandles`, `TestPostgresTenantSecurityFaultsAndCorruption`, gate PostgreSQL 16 N/N+1 |
| R20 | Todas las verticales B1-B4, `TestScopedExternalRegistryPreservesTenantLifecycle`, manifest exacto y gates terminales global/race/coverage |

### Comandos y resultados terminales

| Gate | Resultado |
|---|---|
| `git diff --check`; `go vet ./...`; `go test -run '^$' ./...` | pass; sólo warnings informativos LF/CRLF |
| `go test -count=1 -timeout=20m ./...` | pass; `cmd` 58.240s, `contractreview` 21.639s, `githubpr` 24.758s, `validationworkflow` 56.061s |
| `PATH=C:\msys64\ucrt64\bin;%PATH% CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=1 -timeout=25m ./...` | pass exacto sin `-p=1`; `cmd` 133.864s, `contractreview` 64.378s, `githubpr` 82.762s, `validationworkflow` 95.133s |
| `CGO_ENABLED=0 go build ./...` Windows/Linux/Darwin amd64/arm64 | seis builds pass |
| `go mod verify` | pass, `all modules verified` |
| auditorías sistémicas de comments de controlplane/workflow/GitHub | pass |
| focales secret/sensitive/transport/audit y MCP/external-agent x3 | pass; scan del diff sin private keys/tokens |

### PostgreSQL 16, coverage exacta y cleanup

- Manifest base `1b0162237d941a72429a519076e9ea14e331064d`: 87 fuentes
  productivas, 50 fuentes de test, 3 productivas sin statements y 15 paquetes;
  todos los `.go` modificados o untracked de Feature37 están clasificados.
- Comando terminal: `scripts/feature35-postgres-gate.ps1 -BootstrapWsl
  -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage
  -Feature37Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`.
- PostgreSQL normal pasó (`cmd` 31.127s, `postgres` 123.579s,
  `planapproval` 0.518s) y race UCRT pasó (`cmd` 53.561s, `postgres`
  123.705s, `planapproval` 3.256s). Migrations empty/N/N+1, compatibility
  runtime y contract suites pasaron.
- Dos mediciones exteriores independientes, cada una con dos generaciones
  internas y perfiles Unix/Linux (`projectconfig/local` y
  `tenantsecurity/local`), produjeron exactamente `4626/5417 =
  85.39782167251246077164482185711648513938%`. El comparador estricto de
  identidad de bloque + boolean aceptó las cuatro firmas; no se excluyó ninguna
  rama ni se relajó el comparador.
- Cleanup verificado: 0 distros WSL, 0 roots `featureproof-pg35-gate-*`, 0
  procesos PostgreSQL owned, 0 perfiles de coverage, 0 binarios diagnósticos,
  0 `__pycache__` y 0 patrones de credenciales. Se eliminaron sólo cinco
  perfiles `f37*.out`, dos perfiles accidentales del workspace, el probe GCC y
  el `.pyc` exacto creados por esta verificación.
- T24 queda `[x]`; no quedan gates ni limitaciones conocidas para la review
  independiente.

## Cierre tras tercera re-review (2026-11-26)

Esta sección sustituye toda cifra y conclusión terminal anterior. Los tres
blockers de la tercera review se corrigieron y se verificaron sobre el árbol
actual; además, el gate recuperado descubrió una clasificación zero-statement,
un déficit real de cobertura y dos tests sensibles a carga. Ningún gate se
relajó y `feature_list.json` no se modificó.

### Blockers B1-B3 cerrados

| Blocker | Corrección productiva | Evidencia conductual |
|---|---|---|
| B1: attribution y recovery MCP multi-principal | La autenticación devuelve el `PrincipalID` interno. Workflow y external-agent lo persisten; la capability histórica queda sellada como autoridad opaca, sin secretos, y `Prepare/Confirm/Pending/AbortHistorical` reconstruyen exactamente tenant, catálogo y principal tras restart. | `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`, `TestTenantAdmissionCrashBoundariesReconcileOnFreshService`, `TestLegacyTenantAdmissionRecoveryConvergesBothCrashWindows`, `TestTenantAdmissionRecoveryPropagatesEveryAuthorityFailure` |
| B2: quota con efecto proveedor unknown | `EffectUnknown` conserva reservation y counters. `UnknownEffectReconciler`, propiedad del provider, debe observar ausencia/cleanup antes de `Release`; restart y redelivery liberan exactamente una vez. | `TestProductExternalUnknownEffectRequiresProviderReconciliation`, `TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle`, `TestScopedExternalRegistryPreservesTenantLifecycle` |
| B3: race causal | Contextassembly separa el timeout contractual del observer de la señal causal de entrada; GitHub espera barreras con el contexto de suite. El gate detectó además que goinspector reutilizaba un presupuesto de un segundo en un test de confinement: ese test usa ahora un presupuesto aislado de diez segundos sin cambiar los asserts de escapes/symlinks. | `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary`, `TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff`, `TestFreezeRepositoryRejectsEscapesAndReadsOnlyAuthorizedRoots` race x50 |

Los verticales de recovery legacy y de propagación de fallos cubren orphan,
committed-confirm, discovery, abort, mismatch de authority, prepare y confirm.
Su comparación directa añadió 22 statements productivos cambiados: el gate
pasó de `4703/5545` a `4725/5545` sin excluir archivos ni ramas.

### Trazabilidad R1-R20 vigente

| Requirement | Evidencia principal |
|---|---|
| R1 | `TestAdministrationMutationsRemainAtomicAndVersioned`, `TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates` |
| R2 | `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`, `TestAuthenticatedMCPResolvesEveryRequestPrincipal` |
| R3 | `TestContractMCPAuthenticatesEveryRequestSubject`, `TestWorkflowMCPAuthenticatesEveryRequest`, `TestProductSurfaceHelpersFailClosedWithoutRequestIdentity` |
| R4 | `TestTenantAdmissionCrashBoundariesReconcileOnFreshService`, `TestLegacyTenantAdmissionRecoveryConvergesBothCrashWindows` |
| R5 | `TestStoreContract`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` |
| R6 | `TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys`, `TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim` |
| R7 | `TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability`, `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority` |
| R8 | `TestSecretRotationCASAndCiphertextOnlyPersistence`, `TestPostgresTenantSecretsAuditAndLeaseRace` |
| R9 | `TestSecretLeaseConsumesAndAuditsBeforePrivateCallback`, `TestSecretConsumeRejectsDurableLeaseDrift` |
| R10 | `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretRotationCASAndCiphertextOnlyPersistence` |
| R11 | `TestTransportSecurityFailsClosedAndPinsReconnect`, `TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention` |
| R12 | `TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure`, `TestAuditPaginationScopesCursorsAndDetectsTailTamper` |
| R13 | `TestDurableAuthorityFailsClosedBeforeDecisionAudit`, `TestSensitiveUnknownCommitAndDenyNeverGrants`, `TestAdministrationFaultsAreAtomicAndUnknownCommitResolves` |
| R14 | `TestRetentionPolicyEligibilityFreezesBoundary`, `TestRetentionControllerRejectsStalePlansAndTamperedIntents` |
| R15 | `TestRetentionControllerConvergesUnknownDelete`, `TestProjectDeletionBlocksAdmission`, `TestRetentionBatchAndGraphRespectDependencyOrdering` |
| R16 | `TestProductExternalUnknownEffectRequiresProviderReconciliation`, `TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion`, `TestQuotaJobQueueRestartReleasesExactRecoveredJobs` |
| R17 | `TestClosedBoundaryAdversarialMatrix`, `TestSnapshotValidationRejectsEveryForgedProjectionFamily`, `TestTenantSecurityErrorMappingCoversContextAndTypedFailures` |
| R18 | `TestSubjectDigestDoesNotPersistRawSubject`, `TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift` |
| R19 | local contract/corruption/restart, PostgreSQL 16 normal/race/Linux y migration N/N+1 terminales |
| R20 | verticales B1-B3, recovery failure matrix, manifest exacto y gates global/race/coverage terminales |

### Gates sobre el árbol definitivo

| Gate | Resultado |
|---|---|
| `gofmt -l cmd internal`; `git diff --check`; `go vet ./...`; `go test -run '^$' ./...` | pass; sólo warnings informativos LF/CRLF |
| focal tenant admission normal/race x20 | pass: normal `2.981s`, race `13.671s` |
| focal goinspector race x50 | pass: `2.090s` |
| `go test -count=1 -timeout=20m ./...` | pass: `cmd 57.241s`, `contextassembly 24.271s`, `contractreview 23.563s`, `githubpr 25.401s`, `goinspector 2.634s`, `validationworkflow 54.794s` |
| `CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=1 -timeout=25m ./...` | pass exacto sin `-p=1`: `cmd 87.256s`, `contextassembly 34.620s`, `contractreview 40.511s`, `githubpr 60.692s`, `goinspector 4.034s`, `validationworkflow 78.699s` |
| builds `CGO_ENABLED=0 go build ./...` Windows/Linux/Darwin amd64/arm64 | seis pass: `17.744/6.632/8.021/6.135/7.688/8.827s` |
| `go mod verify` | pass, `all modules verified` |
| auditorías de comentarios controlplane/workflow/GitHub/preview | pass |
| focales secret/sensitive/transport/audit y composición MCP/external x3 | pass; escaneo del diff sin credenciales |

### PostgreSQL 16, cobertura y cleanup terminal

- Base exacta: `1b0162237d941a72429a519076e9ea14e331064d`.
- Manifest exacto: `89 production / 53 tests / 4 zero-statement / 17
  packages`; `validationworkflow/model.go` permanece en producción y está
  clasificado zero-statement porque el diff sólo añade un campo y comentario.
- Comando terminal: `scripts/feature35-postgres-gate.ps1 -BootstrapWsl
  -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage
  -Feature37Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`.
- PostgreSQL normal pasó `36.997/117.399/0.528s`; race UCRT pasó
  `50.642/125.555/3.732s`. Linux, migrations empty/N/N+1 y contract suites
  pasaron dentro del mismo exit 0.
- Dos auditores exteriores independientes, cada uno con dos generaciones
  internas y perfiles Unix/Linux, produjeron exactamente `4725/5545 =
  85.21190261496844003606853020739404869252%`; las cuatro firmas de identidad
  de bloque + boolean fueron iguales. `validationworkflow/service.go` quedó en
  `60/66` statements cambiados.
- Cleanup final: cero distros WSL FeatureProof, roots
  `featureproof-pg35-gate-*`, procesos owned, perfiles o binarios diagnósticos.
  Se borraron sólo tres perfiles literales creados por el diagnóstico, el
  binario/perfiles temporales conocidos y el `.pyc` exacto de coverage.
- T24 queda completa; no hay gates ni limitaciones conocidas. No hubo commit ni
  push.
