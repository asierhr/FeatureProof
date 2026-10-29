# Feature 35 — Persistent control plane implementation

## Estado final tras remediación de review 9 — 2026-10-29

La feature vuelve a quedar lista para revisión con T1–T18 cerradas. Esta
sección sustituye las cifras y estados históricos inferiores.

- El cleanup desacoplado conserva ownership tras la cancelación del caller y
  queda acotado por el lease validado de la operación completa. Reconcile y
  release mantienen además sus deadlines de fase (`AcquireTimeout` y
  `ReleaseTimeout`), de modo que persistencia y observación posteriores no
  heredan por accidente el presupuesto ya consumido por una llamada externa.
- La matriz contractual de environment pasa con el entorno exacto de coverage:
  `go test -p=1 -count=3 -coverpkg=./internal/environment
  ./internal/environment` (88.0%); la repetición ampliada `-count=10` y el
  global race UCRT64 también pasan.
- Se normalizaron con `gofmt` exclusivamente los 53 archivos Go que ya estaban
  afectados por la feature. `gofmt -l .` queda vacío y la inspección del diff
  confirma que la normalización no incorporó archivos ajenos.
- El gate PostgreSQL 16 completo terminó con `Repeat=3`, race, runtime Linux y
  dos perfiles independientes idénticos. La cobertura diferencial exacta es
  **5420/6370 = 85.08634222919937205651491365777080062794%**.
- Verificación final verde: global serial, global race UCRT64, vet,
  compile-only, `go mod verify`, gofmt, diff audit y seis cross-builds
  (`windows/linux/darwin × amd64/arm64`). El cleanup final no deja distros WSL,
  roots `featureproof-pg35-gate-*`, perfiles `.cover`/`block` ni `__pycache__`
  propios. No se modificó `feature_list.json` ni se hizo commit/push.

## Estado final tras enmienda R16 y remediación de review 8 — 2026-10-29

La feature queda lista para revisión con T1–T18 cerradas. Esta sección
sustituye las cifras y estados históricos inferiores.

- Enmienda aprobada: `r16-comment-scope` fue aprobada por el usuario a las
  `2026-10-29T15:47:22+01:00`. Se retiraron los comentarios mecánicos de
  locales, parámetros y receivers triviales; se conservaron comentarios
  concretos para entidades/tipos, funciones/métodos, helpers, símbolos de
  paquete no evidentes y las pocas fronteras locales donde autoridad, fencing,
  durabilidad u ownership no resultan visibles por el código.
- Auditoría R16: el inventario cerrado cubre 81 archivos de producción y 48 de
  test del diff completo. La auditoría AST exige comentarios útiles en los
  símbolos del alcance aprobado y rechaza paráfrasis, boilerplate y familias
  interpoladas; ya no convierte cada variable local en una obligación.
- Cobertura determinista: `verifyActiveJobTx` tiene una cancelación con
  transacción ya abierta, por lo que la rama de error deja de depender de si la
  cancelación ocurre antes o después de `BeginTx`. El gate genera dos perfiles
  por ejecución, compara todos sus bloques y repite la medición en dos bases
  aisladas sin reducir `-count`, inventario ni timeouts.
- Dos gates PostgreSQL 16 completos consecutivos con `Repeat=3`, race, runtime
  Linux y coverage produjeron exactamente **5406/6356 =
  85.05349276274386406544996853366897419761%** en todos los perfiles.
- Verificación final verde: global serial, global race UCRT64, focal race ×20,
  vet, compile-only, `go mod verify`, gofmt, diff audit y seis cross-builds
  (`windows/linux/darwin × amd64/arm64`). El cleanup final observa cero distros
  WSL, cero roots `featureproof-pg35-gate-*`, cero perfiles `.cover`/`block` y
  cero `__pycache__` propio. No se modificó `feature_list.json` ni se hizo
  commit/push.

## Estado final tras remediación de review 7 — 2026-10-29

La feature vuelve a estar lista para revisión con T1–T17 cerradas. Esta sección
sustituye las cifras y estados históricos inferiores.

- Claim funcional estable (R9/R10/R15): `Executor` conserva y propaga el claim
  vivo más reciente —mismo token, revisión y lease renovado— entre intent,
  progress, renew y terminal; cada checkpoint mantiene el CAS/fencing y usa una
  confirmación acotada independiente del deadline del efecto. La reconstrucción
  real fsync-backed pasa ×100 y
  `TestExecutorPropagatesLatestLiveClaimAcrossEveryCausalCAS` junto con ella
  pasa bajo race ×20.
- Gramática cerrada de import (R12/R15): roots, número exacto de componentes,
  IDs, decimales positivos y schema de cada stream/relation se validan como una
  tupla. Los casos cross-stream, roots parecidos, componentes extra, traversal,
  separators y nombres reservados se rechazan antes de staging/verificación.
- Comentarios sistémicos (R16): se sustituyeron las familias generadas por
  comentarios ligados a fronteras, invariantes y consumidores concretos. La
  auditoría AST cubre declaraciones y locales del diff y su meta-test rechaza
  explícitamente las plantillas genéricas detectadas en review 6.
- Gate final: global serial, vet, compile-only, global race UCRT64, focal race
  ×20, dos gates PostgreSQL 16.15 consecutivos con `Repeat=3` + race + runtime
  Linux + coverage, seis cross-builds y smoke de lifecycle/cleanup pasan. La
  cobertura diferencial reproducible de ambos gates es exactamente
  **5405/6356 = 85.03775959723096286972938955317809943361%**, sin redondear.
- El cleanup del gate normaliza la salida NUL-padded de `wsl.exe`, reintenta
  unregister por el nombre UUID exacto y espera locks transitorios antes de
  borrar exclusivamente su raíz. La verificación final observa cero distros,
  cero raíces `featureproof-pg35-gate-*` y cero profiles temporales. No se
  modificó `feature_list.json` ni se hizo commit/push.

## Histórico: estado FINAL tras remediación de review 5 — 2026-10-28

La feature queda lista para revisión con T1–T17 cerradas. El estado vigente es
el siguiente; cualquier cifra o estado de las secciones inferiores está
etiquetado como histórico y no describe el diff actual.

- Autoridad atómica (R4): `BoundWorkflowStore.ReserveBound` y `LoadBound`
  devuelven la proyección y su `BackendCoordinate` desde una única
  transacción/snapshot. `Start`, `Decide`, `Cancel` y `Recover` propagan esa
  coordenada inmutable a todas las mutaciones. PostgreSQL la exige desde epoch
  1 y valida backend identity, epoch y digest contra el head bloqueado en la
  misma transacción de escritura. La barrera crítica queda cubierta por
  `TestPostgresDomainSessionFenceRejectsCutoverLateWrite` y
  `TestMutatingUseCasesPropagateAtomicReadAuthority`: read N, cutover antes de
  primera write, rechazo stale y progreso tras un `LoadBound` N+1.
- Import cerrado (R12/R15): el allowlist exacto valida
  `(stream, relation, path/schema)` antes de staging, materialización,
  verificación o cutover; ninguna relación desconocida dentro de un stream
  permitido puede convertirse en no-op. Lo prueban
  `TestImportRelationAllowlistRejectsEveryUnknownBranch` y
  `TestPostgresImportUnknownRelationsNeverVerifyOrCutOver`, que demuestran que
  cada formato opaco queda no verificado y nunca mueve el head.
- Comentarios sistémicos (R16):
  `TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` deriva el
  inventario Go completo del diff, tracked y untracked, y recorre declaraciones,
  funciones/métodos/helpers, parámetros, receivers, `DeclStmt`, asignaciones
  `:=` y variables de range en producción, comandos, tests y contract suites.
- Vertical productiva: `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker`
  entra por webhook firmado, cierra runtime y pools de N, elimina
  `.featureproof`, recompone N+1 y continúa únicamente por queue, authority JIT
  y outbox hasta environment/capacity, execution, cleanup, correction,
  package/publication y references/retention PostgreSQL.
- Cobertura reproducible: el gate crea una base PostgreSQL limpia, separada de
  normal/race, con ownership y cleanup propios; las suites unitarias usan
  `-count=3`. Dos ejecuciones consecutivas sobre el mismo diff, y después el
  gate final `Repeat=3 -Race -Coverage`, produjeron exactamente **5297/6223 =
  85.11971717820986662381488028282179013338%** sin redondear.

Gates finales verdes: global serial, vet, compile-only, global race UCRT64,
focal race ×20, PostgreSQL 16.15 Repeat=3 + race + runtime Linux + cobertura,
cross-builds `windows/linux/darwin × amd64/arm64`, `go mod verify`, gofmt,
diff-check y limpieza sin distros WSL ni recursos PostgreSQL propios. No se
modificó `feature_list.json` ni se hizo commit/push.

## Histórico: estado tras remediación de review 4 — 2026-10-28

T2–T17 quedan cerradas y la feature está lista para una nueva revisión. La
vertical `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker` entra por
el webhook firmado y la composición GitHub productiva. El proceso N persiste
inbox/job/authority/outbox y alcanza aprobación; luego cierra runtime y pools y
se elimina `.featureproof`. Un proceso N+1 recompone todo desde PostgreSQL y la
segunda orden firmada avanza únicamente por scheduler, claim/heartbeat,
reconcile, efecto JIT y confirmación causal hasta fixture create, ejecución,
fixture cleanup, package y publicación. La prueba bloquea el efecto real para
observar referencias vivas de workflow/environment/fixture/cleanup, comprueba
su cierre causal y falla si reaparece el root local.

REV-1 elimina la anterior pseudovvertical de llamadas directas a `Service`.
REV-2 elimina `sessionBindings`: la operación captura una coordenada inmutable
identity/epoch/digest y cada mutación posterior al cutover la valida contra el
head bloqueado; la prueba crítica prepara bajo N, hace cutover antes de la
primera escritura y solo permite continuar al adapter recompuesto N+1. REV-3
cierra exactamente manifest/staging/allowlist, rechaza streams opacos, compara
las proyecciones consumidoras de cleanup, capacity, packages y el inbox GitHub,
y vuelve a verificar dentro del cutover. REV-4 conecta referencias causales a
workflow, environment, ambos ledgers de capacity, cleanup, correction, fixture
y package. REV-5 audita mediante el diff completo producción, comandos,
contract tests y helpers de test.

Las pruebas nuevas cubren stream extra, divergencia `ON CONFLICT` de package,
redelivery GitHub con receipt incompatible, fencing de primera mutación y el
grafo de referencias productivo. No hay checkpoints paralelos ni llamadas
directas a adapters para hacer avanzar la vertical.

### Trazabilidad final R1–R16

| Requirement | Evidencia concreta vigente |
|---|---|
| R1 | `TestPostgresConsumerAdapterMatrix`, shared contracts PostgreSQL de workflow material, functional, environment, capacity, cleanup y correction. |
| R2 | `TestMigrationManifestIsLinear`, `TestPostgresMigrationLedgerFailsClosed`, artefactos N/N+1 del gate y `TestPostgresMigrationRequiresDDLPermission`. |
| R3 | `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker`, `TestPostgresGateRejectsInvalidAndStaleOperations` y fault injection causal de stores/outbox. |
| R4 | `TestPostgresDomainSessionFenceRejectsCutoverLateWrite`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets`, cutover/import concurrente. |
| R5 | `TestPostgresIngressRollbackAndUnknownCommit`, webhook persist-before-ack y reconstrucción allowlisted del comando. |
| R6 | contract de queue y `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets`. |
| R7 | scheduler/recovery contracts, cancel/lease boundary y global race. |
| R8 | authority closed-matrix, secret pre-checks y capability JIT de la vertical productiva. |
| R9 | `ClaimEffect`→heartbeat/reconcile/execute/confirm en `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker` y fault injection de outbox. |
| R10 | shared contracts consumer-owned y corrupción/CAS stale en `TestPostgresConsumerAdapterMatrix`. |
| R11 | runtime start/stop/join, handlers tracked, global race y restart de la vertical productiva. |
| R12 | `TestFreezeAggregatesConsumerExportersWithoutPrivateFiles`, `TestPostgresImportMaterializersRejectMalformedConsumerDocuments`, stream extra/package conflict/GitHub redelivery y cutover recovery. |
| R13 | `TestPostgresKeysetRetentionAndObserver`, writers causales de workflow/environment/capacity/cleanup/correction/fixture/package y referencias observadas por la vertical. |
| R14 | SQLSTATE mapping, observer content-free y fail-closed ante pérdida de DB. |
| R15 | gate PostgreSQL 16.15 normal ×3/race, runtime Linux tagged, reopen de todos los stores e import materializado. |
| R16 | `TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols`, inventario cerrado de cobertura, formato/vet/compile/global/cross-build. |

### Evidencia final reproducible

```text
GOTOOLCHAIN=go1.25.1 go vet ./...                                      PASS
GOTOOLCHAIN=go1.25.1 go test -run '^$' ./...                          PASS
GOTOOLCHAIN=go1.25.1 go test -p=1 -count=1 ./...                      PASS
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe \
  go test -p=1 -race -count=1 ./...                                   PASS
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe \
  go test -race -count=20 ./internal/controlplane/...                 PASS
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe \
  go test -race -count=20 -run 'Test(WebhookControlPlane|ControlPlaneWorker|ControlPlaneCommand)' ./internal/githubpr PASS
scripts/feature35-postgres-gate.ps1 -BootstrapWsl \
  -AuthorizeIsolatedTestResources -Repeat 3 -Race -Coverage \
  -CCompiler C:\msys64\ucrt64\bin\gcc.exe                            PASS
CGO_ENABLED=0 go build ./... para windows/linux/darwin × amd64/arm64  PASS 6/6
go mod verify; gofmt -l .; git diff --check                           PASS
```

Históricamente, ese gate creó PostgreSQL **16.15-r0** sobre Alpine **3.22.2**
WSL1, ejecutó
integración normal ×3, race, binarios Linux hermético/tagged y coverage, y
eliminó database, distro, cluster y temporales propios. La cobertura
diferencial de aquella iteración fue **5233/6154 =
85,03412414689632759181020474488137796555 %** sin redondear; está invalidada
por el diff posterior y no es la cifra final. El inventario
incluye todos los fuentes productivos/tests modificados y combina perfiles por
máximo de bloque sin duplicar el denominador. `feature_list.json` no se tocó.

## Histórico: estado tras review 3 (superado)

En ese momento la feature continuaba `in_progress` y T2–T17 permanecían abiertas. Las secciones
históricas inferiores describen gates de iteraciones anteriores, pero no son
una declaración de cierre del diff actual. La última cifra verificada por el
reviewer antes de esta remediación fue **3058/3596 =
85,03893214682981090100111234705228031146 %**; los cambios posteriores
invalidan esa medición y exigen recalcularla antes de marcar T17.

Ya está demostrado en el estado actual:

- composición productiva PostgreSQL de workflow, artifacts, executables,
  approvals, functional, environment/capacity, cleanup, correction y packages;
- inbox causal persist-before-ack y efecto productivo gobernado por
  claim/heartbeat/reconcile/retry/confirm de outbox, con terminal del job en la
  misma transacción de confirmación;
- cliente GitHub efímero construido desde el secreto JIT del envelope, fencing
  de head/backend y tracking de handlers hasta su terminación real;
- controllers autenticados de query/retention/import, retention aislada por
  proyecto y retries serializables completos para environment, functional,
  cleanup/capacity y correction;
- binarios N/N+1 distintos, auditoría AST de comentarios y gate PostgreSQL 16
  aislado reproducible.

Siguen faltando para solicitar review:

- completar la materialización del export local tipado. Workflow,
  workflow-events, plan artifacts y executables ya se materializan y verifican
  contra tablas consumidoras; siguen pendientes approval, functional,
  environment/capacity, cleanup, correction, packages y GitHub/inbox;
- completar las suites contractuales compartidas restantes. Ya se ejecuta el
  mismo código contra local/PostgreSQL para workflow, plan/executable,
  approval, functional, environment, capacity, cleanup y correction; package
  conserva por ahora solo su contrato PostgreSQL porque no existe un
  `PackageStore` local equivalente;
- una prueba vertical única que admita un run, cierre proceso/pools, retire el
  root local y continúe approval→execution→environment→cleanup→correction→package
  exclusivamente desde PostgreSQL;
- repetir la matriz final normal/race/repeated/cross-build y recalcular coverage
  diferencial sobre el diff definitivo.

Evidencia fresca de esta iteración:

```text
go test -p=1 ./...                                                        PASS
go vet ./internal/controlplane/...                                       PASS
go test ./internal/controlplane/... -run TestPersistentControlPlane...   PASS
powershell -NoProfile -ExecutionPolicy Bypass -File \
  scripts/feature35-postgres-gate.ps1 -BootstrapWsl \
  -AuthorizeIsolatedTestResources -Repeat 1                              PASS
go test -race -count=20 ./internal/controlplane ./internal/validationworkflow \
  ./internal/environment ./internal/environment/cleanup \
  ./internal/functionalflow ./internal/correction \
  -run 'SharedContract$|JoinContext'                                     PASS
```

> Review 1: la evidencia inicial de adapters genéricos quedó invalidada. Los
> bloques A–C y el cierre T17 la sustituyen por adapters tipados y un gate
> PostgreSQL 16 reproducible descrito en este documento.

## Resultado

Se implementó un control plane neutral al proveedor con selección explícita de
backend, epochs append-only y head CAS único, queue durable con lease/fence,
scheduler caller-owned, authority mínima por fase, inbox persist-before-ack,
effect intent/outbox intent-before-effect, import offline, cursores keyset,
retention explícita y adapters PostgreSQL específicos para workflow, approvals,
functional flow, environment/capacity/cleanup y corrections. El modo local usa
el fake contractual concurrente y nunca hace fallback ni dual-write.

Las migraciones embebidas crean ledger, bindings, projections/events, packages,
jobs, inbox, intents/outbox, authority e imports. El runner verifica historia
lineal y checksums bajo advisory transaction lock. `pgx/v5` queda privado a la
infraestructura y los errores se clasifican por SQLSTATE, sin parsear mensajes.

## Trazabilidad R1–R16

| Requirement | Evidencia concreta |
|---|---|
| R1 | `TestPostgresGate`: round-trip de los cinco adapters nombrados y packages byte-exactos. |
| R2 | `TestMigrationManifestIsLinear`, `TestPostgresGate`: empty/latest/rerun y dos migrators concurrentes. |
| R3 | `TestPostgresGate`, `TestPostgresGateRejectsInvalidAndStaleOperations`: CAS de proyección+evento y errores de transacción. |
| R4 | `TestBackendCutoverCAS`, `TestPostgresGate`: epoch inicial, head único y cutover CAS stale. |
| R5 | `TestInboxAndEffectsEnforceStableIdentity`, `TestPostgresGate`: inbox y job en la misma transacción, redelivery/conflict. |
| R6 | `TestMemoryQueueFencesAndRecovers`, `TestPostgresGate`: claim concurrente `SKIP LOCKED`, lease y fence. |
| R7 | `TestSchedulerPersistsRetry`, `TestMemoryQueueLifecycle`, `TestPostgresGate`: heartbeat, retry, expiry y recovery. |
| R8 | `TestAuthorityMinimizesAndRejectsStaleFence`, `TestValidationMatrices`, `TestPostgresGate`: matriz por fase y persistencia verificada. |
| R9 | `TestInboxAndEffectsEnforceStableIdentity`, `TestPostgresGate`: prepare/claim/heartbeat/retry/confirm de outbox. |
| R10 | `TestMemoryQueueLifecycle`, `TestPostgresGate`: cancel idempotente, stale fence y terminales. |
| R11 | `TestSchedulerLoopReturnsCallerCancellation`, `TestSchedulerPersistsRetry`: loop caller-owned, cancelación y resultado tardío cercado. |
| R12 | `TestBackendCutoverCAS`, `TestImportManifestDetachesMaps`, `TestClosedErrorsAndImportTransitions`: selector exclusivo e import frozen→candidate→verified→cutover. |
| R13 | `TestCursorIsProjectScoped`, `TestPostgresGate`: cursor sellado por proyecto, packages y retention acotada. |
| R14 | `TestSQLStateMappingUsesCodes`, `TestClosedErrorsAndImportTransitions`: taxonomía cerrada y mapping sin mensaje. |
| R15 | `TestPostgresGate`, `TestPostgresGateRejectsInvalidAndStaleOperations`: PostgreSQL 16.15 real aislado, concurrencia y restart/reload. |
| R16 | comentarios sistémicos en símbolos nuevos, `gofmt`, `git diff --check`, `go vet ./...` y compile-only global. |

## Tareas

T1–T17 están marcadas `[x]` en `specs/persistent-control-plane/tasks.md`. REV-10
queda cerrada por la matriz final reproducible y la medición diferencial sobre
el diff definitivo.

## Comandos y evidencia

```text
$env:GOTOOLCHAIN = 'go1.25.1'
gofmt -l .
git diff --check
go vet ./...
go test -run '^$' ./...
go test -p=1 -count=1 ./...
$env:CGO_ENABLED = '1'; $env:CC = 'C:\msys64\ucrt64\bin\gcc.exe'; $env:PATH = 'C:\msys64\ucrt64\bin;' + $env:PATH
go test -p=1 -race -count=1 ./...
go test -race -count=20 ./internal/controlplane
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 3 -Race -CCompiler C:\msys64\ucrt64\bin\gcc.exe -Coverage
$env:CGO_ENABLED = '0'; foreach ($target in @(@('windows','amd64'),@('windows','arm64'),@('linux','amd64'),@('linux','arm64'),@('darwin','amd64'),@('darwin','arm64'))) { $env:GOOS=$target[0]; $env:GOARCH=$target[1]; go build ./...; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE } }
```

Gate real: PostgreSQL **16.15-r0** en Alpine **3.22.2** WSL1. Cada ejecución
descarga el rootfs autenticado por SHA-256, crea distro, cluster, puerto,
database UUID y token de ownership únicos, verifica el token antes del drop y
elimina exclusivamente esos recursos incluso al fallar. No infiere DSN ni
contacta producción. El mismo gate ejecuta binarios Linux `amd64` reales para
control plane hermético y PostgreSQL tagged antes del teardown.

Medición histórica de ese bloque, invalidada por el diff posterior:
**3057/3596 = 85,01112347052280311457174638487208008899 %** statements. El inventario
versionado de entonces enumeraba los fuentes productivos/tests
y combina perfiles unitarios y PostgreSQL por máximo de bloque, sin duplicar el
denominador.

## Riesgos residuales

- Logical exactly-once depende de que el destino respete la idempotency key o
  permita reconciliación; destinos sin ambas capacidades permanecen fail-closed.
- `LISTEN/NOTIFY` no se usa como autoridad: el polling durable puede añadir
  latencia hasta que composición defina su intervalo.
- Las ventanas de compatibilidad de migración requieren que despliegue mantenga
  binario y schema dentro de las versiones embebidas; versiones futuras fallan
  cerradas.
- Retention no ejecuta ninguna eliminación sin policy explícita y preserva
  inbox/migration ledger; la policy comercial queda fuera de esta feature.

## Remediación review 1 — bloque C

- REV-7: el runner usa advisory lock de sesión y ledger singleton con
  `dirty`, target/current, PostgreSQL major y compatibilidad min/max. Cada asset
  tiene descriptor `expand/backfill/contract` y boundary transaccional propio;
  el intent dirty se confirma antes de ejecutar el descriptor. Los tests reales
  cubren empty/latest/rerun/concurrencia, checksum, hole, future, dirty, abort,
  servidor, permisos y rollback binario dentro/fuera de N/N+1.
- REV-8: un receipt filesystem atómico congela el origen y el `LocalStore` real
  de ingress rechaza commits mientras está frozen/archived. El manifest incluye
  expected epoch/head, counts y digests de streams/relations/events; staging
  PostgreSQL es idempotente y no concede authority. Verify precede un cutover
  CAS que confirma epoch/head, eventos archive/cutover, manifest y receipt en
  una transacción. Se prueban partial/corrupt, abandon/unfreeze pre-cutover,
  archive permanente post-cutover, response loss y candidatos concurrentes.
- REV-9: `CursorCodec` usa HMAC-SHA256 con secreto solo de composición y scope
  version/project/filter/snapshot/expiry. `ListJobs` pagina por
  `(created_at,job_id)` excluyendo inserciones posteriores al snapshot.
  Retention trabaja por `terminal_at`, bloquea páginas y preserva referencias
  vivas, effects inciertos, packages, authority, inbox y ledger, insertando
  summaries antes de borrar. Retry completo está usado por import/retention y
  el observer content-free es best-effort; fallar el sink no afecta commits.

### Evidencia añadida en bloque C

- R2/R15/R16 → `TestPostgresMigrationLedgerFailsClosed`,
  `TestPostgresMigrationRequiresDDLPermission`,
  `TestMigrationManifestIsLinear`.
- R3/R14/R16 → `TestSQLStateMappingUsesCodes`,
  `TestObservedRetryIgnoresSinkFailure`,
  `TestPostgresKeysetRetentionAndObserver`.
- R4/R12/R15/R16 → `TestLocalImportFreezeResumeArchive`,
  `TestLocalImportPreCutoverUnfreeze`,
  `TestExistingLocalStoreRejectsWritesDuringOfflineImport`,
  `TestPostgresOfflineImportCutoverRecovery`,
  `TestPostgresConcurrentImportsPublishOneHead`.
- R13/R14/R16 → `TestCursorIsProjectScoped`,
  `TestPostgresKeysetRetentionAndObserver`.

PostgreSQL real usado por el bloque: **16.15** sobre Alpine 3.22.2 WSL1
efímero. Pasan gate completo normal y race, además de los focales race ×20. La
distro, cluster, schemas y temporales propios fueron eliminados. La cobertura
diferencial histórica queda invalidada por el nuevo diff y se recalculará en
T17; no se presenta como evidencia vigente de este bloque.

## Remediación review 1 — bloque A

- Eliminados `AggregateRecord`, `namedAdapter`, `AggregateKind` y las tablas
  `aggregates`/`aggregate_events`.
- Añadidas tablas/constraints específicas y adapters directos con assertions:
  `validationworkflow.WorkflowStore`, `PlanArtifactStore`,
  `ExecutablePlanStore`, `PackageStore`; `planapproval.ApprovalStore`;
  `functionalflow.IntentStore`, `EventJournal`, `TerminalReconciler`;
  `environment.Store`, `CapacityAdmission`; `cleanup.Store`, `CursorStore`,
  `PassCursorStore` y capacity ledger; `correction.Store`.
- Packages guardan bytes canónicos, media/schema version, run/project,
  artifact/binding digests y relación única, nunca paths.
- `SaveOwnedCausal` y `SaveCausal` confirman projection + event + job/outbox
  juntas. La matriz PostgreSQL inyecta fallos tras projection/event/job/outbox y
  tras commit; antes de commit no queda ninguna fila parcial y el commit de
  resultado desconocido se descubre por reload idempotente.
- `TestWorkflowStoreContractLocalAndPostgres` ejecuta la misma suite consumer
  sobre local y PostgreSQL. `TestPostgresGate` reabre workflow, functional
  intent/terminal, lifecycle/capacity, cleanup/cursors/ledger, correction y
  packages con tipos de dominio.
- Capacity release calcula con la función consumer-owned la prueba ligada a
  cleanup/environment/resource/receipt, rechaza otra prueba y persiste la
  confirmación para que la redelivery solo converja con bytes idénticos. El
  gate también construye y enlaza el adapter privado de approvals en PostgreSQL.
- Gates del bloque: paquetes focales y vet pass; normal ×10, race focal ×10,
  PostgreSQL 16 normal y PostgreSQL race ×5 pass; tras el cierre final pasan
  además suite global y race focal/PostgreSQL ×3. T3/T5–T8/T13 se revalidan;
  T17 sigue abierta para los hallazgos/bloques restantes de review 1.

## Remediación review 1 — bloque B

- REV-2: `cmd/featureproof github serve` selecciona `local` conservando el
  `githubpr.LocalStore` existente, o `postgres` explícito sin fallback. El modo
  PostgreSQL ejecuta `Migrate` + `Ready`, inyecta inbox/queue y mantiene el pool
  bajo el lifecycle del runtime. `WebhookHandler` verifica HMAC, traduce el
  comando y aplica scope estático antes de `IngressStore.Admit`; devuelve 202
  únicamente después del commit. El worker posterior recarga campos allowlisted
  del inbox, nunca el body raw.
- REV-4: jobs y outbox se unen en SQL al head único, epoch/digest activo y
  `backend_identity`. Enqueue, claim, heartbeat, complete, retry, cancel,
  recovery, intent prepare/confirm, outbox y authority revalidan esa autoridad;
  un cutover cerca inmediatamente worker y resolver secreto anteriores.
- REV-5: jobs persisten `created_at`, `backend_epoch` y `binding_digest`; claim
  ordena por `(available_at,created_at,job_id)`. Heartbeat está acotado por
  `MaxLease` y deadline; complete/retry verifican lease viva con reloj de DB,
  owner/fence/cancel revision. Cancel incrementa fence, terminaliza el trabajo y
  crea un job cleanup convergente si existía una lease activa.
- REV-6: `AuthorityGrant` persiste la intersección catalog/policy/binding y una
  matriz cerrada por fase. `DeriveAuthority` no recibe capabilities del worker;
  `ResolvePersistedSecrets` exige una revalidación durable inmediatamente antes
  del resolver JIT. Planning/evaluation/publication no reciben target/secrets,
  execution exige target y cleanup solo admite su binding sin secretos.
- Lifecycle: `WorkerRuntime` es inerte al componer y expone `Start`, `Stop` y
  `Join`. El scheduler mantiene heartbeat durante handlers, deja de claimar al
  cancelar y une el handler cooperativo antes de salir. `runGitHubServer` inicia
  el worker antes de readiness HTTP y lo detiene/une durante shutdown.

### Trazabilidad añadida en bloque B

- R4/R6/R7/R10 → `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets`,
  `TestMemoryQueueRejectsCancelledExpiredAndMisorderedOwners`.
- R5/R15 → `TestWebhookControlPlanePersistsAfterScopeAndBeforeACK`,
  `TestPostgresIngressRollbackAndUnknownCommit`.
- R8/R11 → `TestDerivedAuthorityUsesPersistedClosedMatrix`,
  `TestControlPlaneWorkerReloadsAllowlistedCommandAfterRestart`,
  `TestWorkerRuntimeIsExplicitlyStartedStoppedAndJoined`.
- R12 → `TestGitHubControlPlaneSelectorKeepsLocalExplicitAndPostgresFailClosed`.

### Gates del bloque B

```text
go test ./...
go vet ./internal/controlplane/... ./internal/githubpr ./cmd/featureproof
go test -count=10 ./internal/controlplane/... ./internal/githubpr
FEATUREPROOF_POSTGRES_TEST_DSN=<isolated> go test -tags=postgres_integration -count=3 ./internal/controlplane/postgres
CGO_ENABLED=1 go test -race -count=3 ./internal/controlplane/... ./internal/githubpr
FEATUREPROOF_POSTGRES_TEST_DSN=<isolated> CGO_ENABLED=1 go test -race -tags=postgres_integration -count=3 ./internal/controlplane/postgres
```

Todos pasan sobre PostgreSQL 16.15 aislado. T4/T9–T12/T14 vuelven a `[x]`;
T17 continúa abierta hasta completar la matriz de la review 1.

## Histórico: cierre T17 / REV-10 de una iteración anterior

El gate versionado `scripts/feature35-postgres-gate.ps1` exige autorización
explícita y un DSN administrativo autorizado o `-BootstrapWsl`; jamás deriva un
host. El helper `cmd/featureproof-postgres-gate` restringe create/verify/drop a
database UUID, registra ownership dentro del database y vuelve a verificar
database+token antes de terminar conexiones y eliminarlo. El bootstrap usa
Alpine autenticado, PostgreSQL 16 temporal y `finally` para detener,
desregistrar y borrar solo distro/root propios. Una ejecución fallida por
coverage y las ejecuciones exitosas dejaron cero distros, clusters, databases o
directorios `featureproof-pg35-gate-*`.

Trazabilidad final de requisitos a tests ejecutados:

| Requirement | Tests de cierre |
|---|---|
| R1 | `TestPostgresConsumerAdapterMatrix`, `TestWorkflowStoreContractLocalAndPostgres`, `TestPostgresApprovalStoreContract` |
| R2 | `TestMigrationManifestIsLinear`, `TestPostgresMigrationLedgerFailsClosed`, `TestPostgresMigrationRequiresDDLPermission` |
| R3 | `TestPostgresGateRejectsInvalidAndStaleOperations`, `TestPostgresIngressRollbackAndUnknownCommit`, `TestObservedRetryIgnoresSinkFailure` |
| R4 | `TestBackendCutoverCAS`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets`, `TestPostgresConcurrentImportsPublishOneHead` |
| R5 | `TestWebhookControlPlanePersistsAfterScopeAndBeforeACK`, `TestPostgresIngressRollbackAndUnknownCommit`, `TestControlPlaneCommandReconstructionAndDispatch` |
| R6 | `TestMemoryQueueFencesAndRecovers`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets` |
| R7 | `TestSchedulerHeartbeatSuccessAndFenceLoss`, `TestSchedulerPropagatesEveryDurableBoundaryFailure`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets` |
| R8 | `TestDerivedAuthorityUsesPersistedClosedMatrix`, `TestAuthorityResolvesSecretsOnlyAfterBothLiveChecks`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets` |
| R9 | `TestInboxAndEffectsEnforceStableIdentity`, `TestPostgresConsumerAdapterMatrix`, `TestPostgresGate` |
| R10 | `TestMemoryQueueRejectsCancelledExpiredAndMisorderedOwners`, `TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets` |
| R11 | `TestSchedulerCancellationAndLoopProgress`, `TestWorkerRuntimeIsExplicitlyStartedStoppedAndJoined`, global race |
| R12 | `TestLocalImportFreezeResumeArchive`, `TestLocalImportFilesystemAndReceiptFailures`, `TestPostgresOfflineImportCutoverRecovery`, `TestPostgresConcurrentImportsPublishOneHead` |
| R13 | `TestCursorIsProjectScoped`, `TestPostgresKeysetRetentionAndObserver`, `TestPostgresConsumerAdapterMatrix` |
| R14 | `TestSQLStateMappingUsesCodes`, `TestObservedRetryIgnoresSinkFailure`, `TestPostgresConsumerAdaptersFailClosedOnDatabaseLoss` |
| R15 | `TestOwnedDatabaseLifecycle`, `TestPostgresGate`, `TestPostgresApprovalStoreRejectsCorruptAndConflictingDurableState`, gate normal/race/runtime |
| R16 | `TestSystemicCommentsCoverChangedSymbols`, manifest/coverage audit, formato/vet/compile/global/cross-build |

Resultados finales: PostgreSQL normal ×3, PostgreSQL race, Linux runtime
hermético+tagged, global normal serial, global race serial y focal control-plane
race ×20 pasan con Go **1.25.1**. Cross-builds `windows/linux/darwin ×
amd64/arm64` pasan con `CGO_ENABLED=0`. El race global detectó una variable de
job compartida entre handler y heartbeat; se corrigió congelando la copia del
handler y la matriz completa se repitió después del fix. Cobertura histórica de
esa iteración, invalidada por el diff posterior: `2786/3267 =
85,27701254973982246709519436792164064891 %`.

## Remediación review 2 — cierre REV-1–REV-9

- REV-1: cleanup preserva elegibilidad, transición a claimed, eventos, revisión,
  owner/fence y lease; functional terminal exige `ClaimToken`; approval verifica
  `snapshot_digest`; packages incorpora project/run/backend identity, epoch,
  active head y bytes canónicos. Los adapters mantienen assertions directas de
  sus interfaces consumer-owned y las matrices local/PostgreSQL ejercitan los
  mismos invariantes, incluidos corrupción, CAS stale y reopen.
- REV-2/REV-3: `DomainStores` construye el grafo PostgreSQL completo exigido por
  el worker. `PrepareDispatch` usa el puerto causal de workflow y confirma
  proyección, evento e intent antes del efecto; environment admite job e intent
  simultáneos en la misma transacción. El vertical
  `TestPostgresProductDispatchSurvivesWorkerRestart` cierra el primer store,
  reabre otro pool y continúa el mismo run desde inbox, workflow, queue,
  effects, environments y referencias PostgreSQL.
- REV-4: grants quedan ligados atómicamente a backend epoch y binding digest;
  packages, jobs, authority, outbox y referencias revalidan active head,
  identidad de adapter, epoch/digest y, cuando aplica, owner/fence/lease viva y
  cancel revision. Cutover publica eventos separados `archived` y `cutover`, e
  invalida grants y workers de la epoch previa.
- REV-5/REV-6: scheduler y runtime ofrecen `Stop`/`JoinContext` acotados sin
  esperar handlers no cooperativos. El worker deriva exclusivamente el envelope
  persistido para la fase, lo vuelve a comprobar contra la lease viva y solo
  entonces cruza el resolver privado JIT. Los tests prueban rechazo pre-secret,
  pérdida de heartbeat, cancelación y cierre antes/después del efecto.
- REV-7: el ledger acepta únicamente historia futura declarada por el prefijo
  compatible de migraciones. Los tests usan manifests N y N+1 realmente
  distintos, cubren rollback compatible y rechazan checksum, hole, dirty,
  server/schema o futuro fuera de ventana.
- REV-8: el freeze es global para workflow, artifacts, executable, approval,
  functional, environment, cleanup/capacity, correction y GitHub. El snapshot
  recorre el root real, conserva bytes y relaciones estables, staging carece de
  authority y verify exige counts/digests/event streams antes del cutover CAS
  con archive receipt. Referencias de producto se crean y retiran mediante un
  writer cercado usado por retention.
- REV-9: capacity, import y retention reintentan la transacción serializable
  completa solo para SQLSTATE `40001/40P01`. El ejecutable inyecta un observer
  JSON content-free; un sink fallido no altera verdad durable ni autoridad.

### Evidencia final de review 2

```text
go vet ./...
go test -run '^$' ./...
go test -p=1 -count=1 ./...
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -p=1 -race -count=1 ./...
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=20 ./internal/controlplane/...
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=20 -run 'Test(WebhookControlPlane|ControlPlaneWorker|ControlPlaneCommand)' ./internal/githubpr
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 3 -Race -CCompiler C:\msys64\ucrt64\bin\gcc.exe -Coverage
go mod verify
CGO_ENABLED=0 GOOS/GOARCH={windows,linux,darwin}/{amd64,arm64} go build ./...
```

Todos los comandos pasan. El gate final ejecutó PostgreSQL **16.15-r0** en
Alpine **3.22.2** WSL1, integración normal repetida, race, runtime Linux,
migraciones y cobertura, y después eliminó la distro, cluster, database y
temporales propios; `wsl.exe --list --quiet` quedó vacío. Cobertura diferencial
exacta histórica de review 2, luego invalidada: **3057/3596 =
85,01112347052280311457174638487208008899 %**. T1–T17 se declararon `[x]` en esa iteración y
`feature_list.json` no se modificó.

## Remediación review 7 — barreras durables y comentarios sistémicos

### REV1: causa raíz y corrección

La reproducción con `go test -json`, diagnóstico durable de queue/workflow y
dump acotado identificó dos ventanas independientes en la vertical
`TestPostgresProductDispatchSurvivesWorkerRestart`:

1. `PhaseAwaitingApproval` era visible antes de que el worker liberase el owner;
   cerrar el proceso en esa ventana cancelaba la liberación y el siguiente
   proceso encontraba el workflow aún leased.
2. el proceso inicial podía cerrarse antes de que el outbox confirmase el job
   `<job>-dispatch`; el proceso siguiente heredaba un efecto no terminal.

El test instala triggers PostgreSQL de alcance exclusivo a su schema y usa
`LISTEN/NOTIFY` solo como wake-up. Cada wake-up vuelve a consultar la verdad
durable: fase esperada con owner vacío y effect intent exacto en estado
confirmed. Las esperas usan deadlines explícitos y sus diagnósticos exponen
únicamente categorías/estados allowlisted. No se aumentó ningún timeout, no se
redujo `-count` y no se estrechó el inventario.

La segunda inestabilidad era cobertura oportunista: la rama de error de la
lectura locked del binding head en `CutoverImport` se ejecutaba o no según el
interleaving de dos imports concurrentes. El nuevo
`TestPostgresImportCutoverRejectsMissingBindingHead` construye deliberadamente
un candidato verificado para un run sin head y prueba de forma determinista que
el cutover falla cerrado. Dos perfiles PostgreSQL completos `-count=3` dieron
cero diferencias booleanas antes de repetir los gates.

### REV2: comentarios y auditoría

Se retiraron completamente las familias `retains the ... next invariant`,
`holds canonical bytes...`, `bounds traversal...`, `records whether...
precondition`, `propagates ... partial state` y variantes equivalentes. No se
sustituyeron por otra plantilla. Los comentarios conservados explican por qué
el símbolo existe en FeatureProof, quién consume el valor y qué autoridad,
ownership, fence, durabilidad o invariante protege.

El muestreo manual recorrió los 181 archivos Go modificados (113 de producción,
68 de test) y leyó al menos una declaración añadida por archivo. Muestras
representativas del criterio aplicado:

- `cmd/featureproof/controlplane_integration_test.go`: cada barrera nombra la
  proyección durable y el proceso que puede cerrar con seguridad después de
  observarla.
- `internal/controlplane/postgres/import_retention.go`: los comentarios separan
  head esperado, epoch previa, authority de materialización y receipt de
  cutover; no describen meramente el tipo de la variable.
- `internal/controlplane/postgres/outbox.go` y `queue.go`: owner, fence, lease y
  estado terminal se documentan desde el consumidor que autoriza el efecto.
- `internal/functionalflow/*`: claim y terminal explican qué worker mantiene la
  autoridad causal y qué store consume la proyección.
- `internal/githubpr/*`: contratos de webhook, submit y autenticación indican
  qué frontera externa consumirá el valor y por qué falla cerrada.
- `internal/previewenv/*` y `internal/validationworkflow/*`: lifecycle,
  executable material y functional runtime documentan ownership y persistencia
  entre reinicios.
- tests y contract suites: las variables describen el oráculo independiente o
  la frontera durable que se intenta falsar, no la sintaxis de la asignación.

`TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` deriva el
inventario completo desde `5812d7a`. La auditoría de familias opera sobre todo
el diff y normaliza literales, identificadores, nombres calificados, números y
CamelCase. `TestSystemicCommentSkeletonCollapsesInterpolatedTemplates` incluye
fixtures interpoladas con nombres distintos para impedir evasiones por bytes o
por archivo.

### Evidencia final de review 7

```text
go mod verify
go vet ./...
go test -run '^$' ./...
go test -p=1 -count=1 ./...
CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -p=1 -race -count=1 ./...
go test ./internal/controlplane -run '^TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols$|^TestSystemicCommentSkeletonCollapsesInterpolatedTemplates$' -count=1
powershell -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 3 -Race -Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe
powershell -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 3 -Race -Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe
CGO_ENABLED=0 GOOS/GOARCH={windows,linux,darwin}/{amd64,arm64} go build ./...
```

Todos los comandos pasan. Ambos gates completos consecutivos produjeron
exactamente **5405/6356 =
85.03775959723096286972938955317809943361%** y limpiaron sus bases, distros y
temporales UUID-owned. T17 queda cerrado de nuevo. No se modificó
`feature_list.json` ni se hizo commit o push.
## Cuarta iteración — import tipado en progreso

- `functionalflow` cruza el import como DTO source-authenticated: la MAC se verifica bajo freeze con la clave local, mientras claims, clave y MAC se descartan antes de persistir staging.
- Approval journals se validan con el projector consumer-owned; snapshots pendientes no recuperables rechazan el candidato.
- Staging dejó de escribir tablas activas. La verificación ejecuta materialización bajo savepoint revertido y el cutover materializa definitivamente dentro de la misma transacción que cambia head/epoch y guarda el receipt.
- Workflow, plan artifact, executable, functional, approval y environment tienen materializadores tipados iniciales. Cleanup/capacity, correction, packages y GitHub siguen abiertos porque requieren exporters consumer-owned que validen sus receipts/anchors/journals; T2–T17 permanecen sin marcar.

## Vertical productiva y deuda de cobertura — 2026-10-28

- La prueba histórica `TestPostgresProductWorkflowContinuesAfterFilesystemRemoval` quedó invalidada por review 4 porque llamaba directamente a `Service`; la vertical vigente que la sustituye está documentada al inicio de este informe.
- La prueba reveló que context assembly permanecía en memoria. `PostgresContextStore` y `0016_context_store.sql` persisten snapshots/bindings idempotentes y los cercan por backend activo; `DomainStores` y `workflowPersistence` lo consumen directamente.
- Evidencia verde: gate PostgreSQL 16.15 normal ×3, tagged race, runtime Linux, `go vet ./...`, compile-only, suite global serial, gofmt y diff-check.
- Evidencia no satisfecha: cobertura diferencial `4261/5882 = 72,44134648078885 %`, por debajo del 85 %. La feature no está lista para review y T2–T17 siguen abiertas.
- Cleanup y capacity ya exponen exporters consumer-owned que reabren y validan sus cadenas de anchor/receipt antes de emitir DTOs públicos; el destino materializa items, eventos, cursores y receipts de cuota en la transacción de cutover. Falta agregarlos en el freeze sin volver a capturar sus archivos privados, y permanecen abiertos correction/packages/GitHub y la vertical total.

## Cierre review 5 — autoridad atómica, import cerrado y evidencia reproducible

- `BoundWorkflowStore.ReserveBound/LoadBound` devuelve proyección y
  `BackendCoordinate` desde la misma transacción/snapshot. `Start`, `Decide`,
  `Cancel` y `Recover` conservan esa coordenada inmutable hasta cada mutación;
  PostgreSQL la exige también en epoch 1 y valida identidad, epoch y digest
  contra el head bloqueado en la transacción de escritura.
- `TestPostgresDomainSessionFenceRejectsCutoverLateWrite` y
  `TestMutatingUseCasesPropagateAtomicReadAuthority` prueban la barrera crítica:
  lectura bajo N, cutover antes de la primera mutación, rechazo stale y progreso
  después de un `LoadBound` nuevo bajo N+1, incluidos Decide/Cancel/Recover.
- El import valida un allowlist exacto `(stream, relation, path/schema)` antes
  de staging, materialización, verificación o cutover. Los negativos
  `TestImportRelationAllowlistRejectsEveryUnknownBranch` y
  `TestPostgresImportUnknownRelationsNeverVerifyOrCutOver` cubren cada rama y
  demuestran que una relación opaca no alcanza `verified` ni mueve el head.
- `TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` deriva todo
  el diff Go (tracked y untracked) y recorre declaraciones, funciones, métodos,
  helpers, parámetros, receivers, `DeclStmt`, `:=` y variables de range en
  producción, comandos, tests y contract suites.
- La cobertura usa una base PostgreSQL exclusiva y vacía, con ownership y
  cleanup propios, distinta de la base ya recorrida por normal/race. Las suites
  unitarias se estabilizan con `-count=3`. Dos ejecuciones consecutivas sobre el
  mismo diff produjeron exactamente **5297/6223 =
  85.11971717820986662381488028282179013338%**.
- Evidencia verde final: `go vet ./...`, compile-only, global serial, global
  race con UCRT64, focal race ×20, `go mod verify`, `gofmt`, `git diff --check`,
  cross-builds `windows/linux/darwin × amd64/arm64`, PostgreSQL 16 aislado
  Repeat=3, tagged race, runtime Linux y cobertura. El gate retiró ambas bases,
  distro WSL y temporales propios. T1–T17 quedan `[x]`; no se modificó
  `feature_list.json` ni se hizo commit/push.

