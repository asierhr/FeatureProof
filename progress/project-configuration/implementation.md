# Feature 36 — project configuration — implementation

Fecha: 2026-11-16  
Estado solicitado al leader: `ready_for_review`.

## Resultado

La feature introduce un control plane de configuración por proyecto con schema v1 estricto, representación canónica, snapshots tipados de catálogo/policy, defaults monotónicos, versiones y activaciones append-only, rollback por nueva activación y binding inmutable por run. La autoridad queda scopeada por `(project_id, repository_id)`; RBAC/membership sigue siendo responsabilidad de feature 37.

Los adapters local y PostgreSQL implementan el mismo contrato. PostgreSQL comparte pool y migraciones del persistent control plane y confirma versión, activación, effective snapshot, head, receipt y evento en una transacción serializable. El arranque de workflow integra el binding con run y primer evento en el mismo checkpoint causal; recovery solo acepta el binding sellado persistido.

No se almacenan ni resuelven valores secretos: el documento solo admite referencias opacas. Administración converge en `projectconfig.Gateway`; CLI, API/MCP y GitHub convergen en `AdmissionResolver`, que deriva project key desde una identidad de repositorio confiable. El workflow rechaza aliases, approval y consumo de modelo fuera del snapshot efectivo.

## Correcciones posteriores a review 1

- REV-1/REV-2: composición productiva obligatoria en workflow CLI/MCP, GitHub y external-agent; `validationworkflow.NewService` rechaza `ProjectConfig` ausente. El active snapshot sustituye selectores antes de construir adapters, y calls/tokens/duration/replans, approval y retention proceden del binding. Recovery valida exclusivamente el binding histórico sellado.
- REV-3: PostgreSQL recompone digests de canonical/effective/activation, enlaza head con el digest de la versión y valida el effective completo dentro de bindings. La matriz real corrompe version, effective, activation, head y binding.
- REV-4: `MaxDurableCounter` es el máximo común `bigint`; publish y rollback cercan version/generation/sequence en local y PostgreSQL y devuelven `exhausted` sin mutación.
- REV-5: validate/auth/publish/rollback/active/version/history/export/ingress emiten únicamente metadata allowlisted de éxito, fallo o cancelación. Panic/fallo del sink no cambia receipts ni estado.
- REV-6: `CatalogLimits` acota descriptors, operations, capabilities, dependencies, aristas y trabajo DFS antes de copiar o recorrer, con tests exactos y límite+1.
- REV-7: bootstrap reproducible fijado a Alpine 3.21.9 por SHA-256 y PostgreSQL 16.16-r0; gate exacto, `Repeat 3`, race, Linux, N/N+1 y coverage determinista ejecutados realmente.

## Trazabilidad R1–R18

| Req | Evidencia principal |
|---|---|
| R1 | `TestAuthorityRejectsScopeBeforeCollaborators`, `TestAdmissionResolverRejectsInvalidComposition`, `TestGatewayConvergesAdministrativeOperations` |
| R2 | `TestLoadStrictRejectsAmbiguityAndSecrets`, `TestCanonicalizeIsDeterministic`, `TestCanonicalizeRejectsEveryStructuralAuthorityViolation` |
| R3 | `TestValidateAndMergeRejectsMissingStaleAndPolicyExpansion`, matrices de loader/validator |
| R4 | `TestValidateAndMergeRejectsCatalogCyclesAndProviderMismatch`, `TestAdmissionResolverUsesOneTrustedMapping` |
| R5 | rechazo de secret-shaped fields, canonical export y snapshots descriptor-only |
| R6 | `TestValidateAndMergeAppliesMonotonicPolicy`, policy-expansion negatives |
| R7 | `projectconfig/contracttest.Run` sobre local y PostgreSQL; publish/version/effective/history |
| R8 | shared contract CAS/idempotency/concurrent publishers; publish fault boundaries |
| R9 | shared rollback contract y `TestPostgresProjectConfigurationRollbackFaultBoundaries` |
| R10 | `TestHistoryCursorRejectsTamperScopeAndExpiry`, paginación y query/export safe |
| R11 | binding seal/shared contract, `TestStartPersistsProjectConfigurationBinding` |
| R12 | `TestBoundConfigurationEnforcesApprovalAndConfirmedUsage`, binding estable tras head nuevo |
| R13 | `TestPostgresProjectConfigurationWorkflowCausalCommit`, local atomic snapshot y restart |
| R14 | cancellation, corruption, cross-project, second-process y unknown-commit tests |
| R15 | `TestPublishObservesOnlyAfterCommitAndHistoryCursorPaginates`, errores tipados allowlisted |
| R16 | `AdmissionResolver`, `Gateway`, selección fail-closed en `validationworkflow` |
| R17 | `TestPersistentControlPlaneSystemicCommentsCoverDeclaredSymbols` y auditoría del diff |
| R18 | migración `0018_project_configuration.sql`, manifest N/N+1 y PostgreSQL 16 real |

## Verificación ejecutada

| Comando | Resultado |
|---|---|
| `gofmt -w ...`; `git diff --check` | PASS; solo avisos informativos CRLF de Git |
| `go vet ./...` | PASS |
| `go test ./... -run '^$' -count=1` | PASS |
| `go test ./... -count=1` | PASS |
| `go test ./internal/projectconfig/... ./internal/validationworkflow ./internal/controlplane/postgres -count=3` | PASS |
| focal `go test -race ... -count=1` con `C:\msys64\ucrt64\bin\gcc.exe` | PASS |
| `go test -race -p 1 ./... -count=1` con el mismo compilador | PASS |
| `feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 3` | PASS |
| gate exacto reviewer `-Repeat 1 -Race -Coverage -Feature36Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe` | PASS |
| gate `-Repeat 3` | PASS |
| gate cleanup corregido `-Repeat 1` + auditoría WSL/root | PASS; lista de distros y roots temporales vacía |
| coverage diferencial determinista | PASS, `1442/1694 = 85.12396694214876033057851239669421487603%`; firmas repetidas idénticas, sin redondeo/exclusiones |
| `GOOS/GOARCH` Windows amd64, Linux amd64/arm64, Darwin amd64/arm64 con `CGO_ENABLED=0 go build ./...` | PASS |
| auditoría sistémica de comentarios | PASS |

El primer race global paralelo agotó los timeouts de dos tests históricos (`contextassembly` y `githubpr`) por contención; no reportó data races. La repetición global completa con `-p 1` pasó, igual que el race focal y el gate PostgreSQL race.

## Invariantes verificadas

- Sin fallback ni dual-write entre local y PostgreSQL.
- Authority se comprueba antes de store/catalog/policy.
- Publicación, rollback, binding y workflow causal son atómicos; los receipt resuelven commit desconocido.
- Versiones/effective snapshots/bindings son inmutables y checksum-sealed.
- Drift, corrupción, cancelación, stale head y scope cruzado fallan cerrados.
- No hay valores secretos en documento, binding, export, events o telemetría.
- Feature 37 (membership/RBAC/tenant isolation) no se implementa aquí.

## Cleanup

Los gates utilizaron una distro WSL Alpine y bases PostgreSQL 16 con UUID y ownership token. Tras el gate de coverage se detectaron una distro y su root temporal UUID residuales pese al exit 0 del `finally`; ambos se eliminaron explícitamente por nombre/ruta exactos. El script se corrigió para emitir `--unregister` antes de observar y exigir dos ausencias consecutivas; un gate PostgreSQL 16 `Repeat 1` posterior terminó con `wsl.exe --list --quiet` y la búsqueda `%TEMP%/featureproof-pg35-gate-*` vacías. También se verificaron y eliminaron `.coverage36`, `feature36-local` y únicamente `scripts/__pycache__` dentro del workspace. No quedan procesos, DSN, distros, roots o perfiles temporales de la verificación.

No se modificó `feature_list.json`, no se hizo commit y no se hizo push.

## Correcciones posteriores a review 2 — 2026-11-17

- REV2-1: `validationworkflow.Service` exige un `ProjectResourceMaterializer`. Cada admisión captura primero su `RunConfigurationBinding` y materializa después admission, modelo, runtime, contrato, target, identities/fixtures, policy, approval y puertos de ejecución desde ese effective snapshot. La composición productiva recompone por `EffectiveDigest`, conserva recursos históricos en caché para runs ya admitidos y no usa `ShowActive` durante recovery. `ApprovalView`, `Decide`, `Cancel` y `Recover` rematerializan desde el binding durable del run.
- REV2-1 tests: `TestRunningServiceMaterializesEveryCapturedHead` cubre un proceso único con dos heads, tanto selecciones distintas como mismo ID/nuevo digest; `TestRecoveryMaterializesTheStoredBinding` prueba que una operación posterior reutiliza el digest histórico sin una segunda resolución del head. Las pruebas productivas de API/CLI/MCP/GitHub/external-agent siguen convergiendo en el mismo `Service` y pasaron tras el cambio.
- REV2-2: `Postgres.Bind` selecciona y compara `binding_digest` en redelivery y `effective_digest` al crear un binding. Los helpers compartidos `ValidateDurableRunBinding` y `ValidateDurableEffectiveSnapshot` recomputan payload y comparan la columna durable. El gate PostgreSQL real prueba corrupción exclusiva de cada columna, categoría `integrity` y ausencia de inserción/mutación.

## Verificación de review 2

| Comando | Resultado |
|---|---|
| `gofmt -l` focal; `git diff --check`; `go vet ./...`; `go test ./... -run '^$' -count=1` | PASS; solo avisos CRLF informativos |
| focal serial `go test -p=1 ./internal/validationworkflow ./internal/projectconfig ./internal/projectconfig/local ./internal/controlplane/postgres ./cmd/featureproof -count=1 -timeout=12m` | PASS |
| race focal con `CGO_ENABLED=1`, UCRT en `PATH` y `C:\msys64\ucrt64\bin\gcc.exe` | PASS |
| global serial `go test -p=1 ./... -count=1 -timeout=15m` | PASS en dos ejecuciones; la última terminó con `validationworkflow 47.027s` |
| gate exacto PostgreSQL 16 reviewer, límite operativo 15 min | TIMEOUT durante coverage; normal/race y PG real habían pasado; cleanup estricto posterior PASS |
| único reintento exacto PostgreSQL 16 reviewer, límite operativo 25 min | TIMEOUT durante la tercera repetición determinista de coverage, después de normal/race PASS, dos ejecuciones reales PG coverage PASS y cálculo `1529/1797 = 85.08625486922648…%`; no llegó al exit 0 terminal |

Estado solicitado al leader: `blocked`. El código y las suites serial/race focal están verdes, pero el gate exacto completo excede el límite explícito autorizado de 25 minutos. No se declara `ready_for_review` sin su exit 0. Tras ambos timeouts se eliminaron por nombre UUID exacto las distros WSL y roots temporales; comprobación final: `wsl_empty=True`, `roots=0`, `processes=0`. También se eliminó únicamente `scripts/__pycache__`; no quedan `.coverage36` ni `feature36-local`.

## Gate terminal autorizado — 2026-11-17

Se ejecutó una última vez, sin modificar flags ni añadir atajos, el comando exacto del reviewer con límite operativo de 45 minutos:

`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage -Feature36Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`

Resultado terminal: **FAIL, exit 1 antes del timeout**. Las fases normales y race de `cmd/featureproof`, `internal/controlplane/postgres` y `internal/planapproval` pasaron. La primera generación determinista de coverage falló en `go test -p=1 -count=3 -coverpkg=./cmd/featureproof ./cmd/featureproof`: `TestProductIdentityFailureParityAcrossAllSurfaces/mcp` devolvió `MCPResponse.Error="invalid_request"`; por ello `feature36_coverage.py` terminó con `CalledProcessError` y el gate reportó `feature 35 differential coverage failed with exit code 1`. No hubo coverage diferencial final en esta ejecución y no se reutiliza la fracción parcial de intentos anteriores como PASS.

Cleanup estricto posterior: sin procesos `go/powershell/wsl/wslhost`, lista WSL vacía, cero roots `%TEMP%/featureproof-pg35-gate-*`, cero `.coverage36`/`feature36-local` y `scripts/__pycache__` eliminado por ruta exacta. Estado definitivo solicitado: `blocked`; no se ejecutó otra repetición, no se modificó `feature_list.json` y no hubo commit/push.

## Corrección del fallo determinista de coverage — 2026-11-17

Causa: `TestProductIdentityFailureParityAcrossAllSurfaces` simulaba la revocación tardía mediante `os.Unsetenv("FEATUREPROOF_PRODUCT_IDENTITY")`. Esa escritura no estaba ligada al ciclo de vida de `testing.T` y alteraba estado global del proceso justo cuando `-count=3` repetía el paquete y la composición por admisión reconstruía resolvers. El síntoma observable fue exclusivamente MCP `invalid_request`, pero la causa era aislamiento defectuoso del fixture, no una diferencia autorizada entre superficies.

Corrección: el fixture usa ahora `workflowProductUnavailableResolver`, inyectado por la misma factory productiva y fallando en la frontera JIT `Resolve`, sin leer ni mutar environment global. El idempotency suffix temporal se sustituyó por un valor determinista; cada superficie conserva root, stores, service, cache y run IDs propios.

Evidencia focal:

- `go test -p=1 -count=20 -run '^TestProductIdentityFailureParityAcrossAllSurfaces$' ./cmd/featureproof -timeout=15m`: PASS (`78.087s`).
- mismo test con `-race -count=10`, CGO/UCRT: PASS (`66.532s`).
- comando exacto del generador de coverage para el paquete, `go test -p=1 -count=3 -coverpkg=./cmd/featureproof -coverprofile=<temp> ./cmd/featureproof`: PASS (`75.787s`, package coverage `74.8%`). El profile temporal se eliminó.

Estado solicitado: `ready_for_gate`. No se lanzó el gate WSL completo, no se modificó `feature_list.json` y no hubo commit/push.

## Gate PostgreSQL posterior al fix MCP — 2026-11-17

Comando exacto, única ejecución autorizada, ventana 45 minutos:

`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage -Feature36Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`

Resultado terminal: **FAIL, exit 1 antes del timeout**. Pasaron normal y race de `cmd/featureproof`, `internal/controlplane/postgres` y `internal/planapproval`; la primera base independiente completó cmd/projectconfig/local y PostgreSQL coverage (`PG 200.558s`, local `85.5%`). Durante la segunda base independiente, `go test -p=1 -count=3 -coverpkg=./internal/validationworkflow ./internal/validationworkflow` falló en `TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork/automatic-denial`: obtuvo un view vacío y `validation workflow executable_cleanup_pending: unavailable`. `feature36_coverage.py` propagó `CalledProcessError` y el gate terminó con `feature 35 differential coverage failed with exit code 1`.

No existe fracción diferencial final ni firmas comparables para esta ejecución porque la segunda base no terminó; no se declara la cobertura parcial como PASS. No se repitió el gate. Cleanup estricto automático verificado: cero procesos, distros WSL y roots `%TEMP%/featureproof-pg35-gate-*`; se eliminaron `scripts/__pycache__` y no quedan `.coverage36`/`feature36-local`. Estado solicitado: `blocked`. `feature_list.json` intacto; sin commit/push.

## Corrección del deadline de finalización bajo coverage — 2026-11-17

Causa: `newRejectedFlowHarness` fijaba `FinalizeTimeout=5s`. La subprueba `automatic-denial` recorre deliberadamente package publication, environment/finalizer cleanup y borrado del executable privado; con coverage completa tardó `5.66s`. El contexto de finalización expiró antes de `Executables.Delete`, que correctamente devolvió `executable_cleanup_pending: unavailable`. No había cache, port agotado, env global ni estado compartido entre repeticiones: era un límite del harness menor que la latencia instrumentada.

Corrección: el harness semántico conserva un límite explícito de 30 s, suficiente para I/O instrumentada. No se modificaron los tests específicos de expiración/cleanup pendiente ni la configuración productiva.

Evidencia:

- subprueba exacta `count=20`: PASS (`2.229s`).
- subprueba exacta race `count=20`, CGO/UCRT: PASS (`4.395s`).
- comando exacto de coverage del paquete `go test -p=1 -count=3 -coverpkg=./internal/validationworkflow -coverprofile=<temp> ./internal/validationworkflow`: PASS (`141.860s`, `84.9%`). Profile temporal eliminado.

Estado solicitado: `ready_for_gate`. No se ejecutó WSL, no se modificó `feature_list.json` y no hubo commit/push.

## Gate posterior a ambos fixes de estabilidad — 2026-11-17

Única ejecución del comando exacto reviewer, ventana 45 minutos: **FAIL, exit 1 antes del timeout**. Las fases normal y race de cmd/PostgreSQL/planapproval pasaron. `feature36_coverage.py` detuvo correctamente la cobertura antes de generar perfiles con:

`RuntimeError: test inventory mismatch missing=['internal/validationworkflow/functional_flow_test.go'] stale=[]`

El fix del deadline había modificado ese archivo de test, pero el manifest de cobertura aún no lo declaraba. Por tanto no existe fracción ni firma diferencial para esta ejecución y no se reutiliza evidencia parcial como PASS. Sin reintento, se añadió `internal/validationworkflow/functional_flow_test.go` al inventario `tests` de `feature36-coverage-manifest.json` para que el artefacto corresponda al diff actual.

Cleanup estricto automático PASS: cero procesos, distros WSL y roots del gate; sin profiles workspace, `feature36-local` o `scripts/__pycache__`. Estado solicitado: `blocked`. No se modificó `feature_list.json`; sin commit/push.

## Gate exacto final tras sincronizar manifest — 2026-11-17

Comando reviewer sin cambios, una sola ejecución con ventana 45 minutos: **PASS, exit 0**.

- normal y race: cmd, PostgreSQL y planapproval PASS;
- las dos bases PostgreSQL coverage independientes completaron;
- la comparación interna SHA-256 de las dos firmas por statement fue igual (de otro modo el gate habría fallado en `independent coverage databases produced different changed-statement signatures`);
- cobertura diferencial exacta: `1529/1797 = 85.08625486922648859209794101279910962716%`;
- evidencia Linux local incluida: `85.5%`.

El gate deliberadamente no imprime los valores SHA-256 y elimina las firmas dentro del root UUID en su `finally`; por ello los hashes literales no quedan disponibles después del exit 0. Sí queda verificada su igualdad por la condición bloqueante versionada del propio gate, sin conservar perfiles fuera del recurso aislado.

Cleanup final estricto: `wsl_empty=True`, cero roots `%TEMP%/featureproof-pg35-gate-*`, procesos o artefactos workspace; `scripts/__pycache__` eliminado. Estado solicitado: `ready_for_review`. `feature_list.json` intacto; sin commit/push.

## Round 3 — external-agent y artifacts durables (2026-11-17)

REV3-1 queda implementado: cada `externalagent.Submit` captura un `RunConfigurationBinding`, persiste binding/retention/timeout en `RunRecord` y entrega al supervisor catálogo, executor y límites materializados para ese run. Status/result/cancel vuelven a materializar el binding histórico. `TestExternalAgentCapturesEachHeadAndReadsHistoricalBinding` mantiene un servicio vivo y verifica head 1/head 2 sin reescribir el primero (`count=20` PASS).

REV3-2 queda implementado mediante el puerto común `projectconfig.ResourceArtifactStore`. Local usa envelopes JSON inmutables; PostgreSQL usa la tabla 0019, namespace `ProjectKey` y clave `(kind,id,version,digest)`, con `payload_digest` verificado en Put/Get/redelivery. El workflow persiste y recompone specification/runtime/contract desde artifacts históricos antes de construir consumers. `TestLocalResourceArtifactStoreSurvivesRestartAndRejectsMutableBacking` y `TestWorkflowResourceMaterializerRestartsFromHistoricalArtifact` prueban restart real, backing mutable y corrupción (`count=20`/`count=10` PASS). `TestPostgresResourceArtifactsSurviveRestartAndRejectCorruption` cubre restart/corrupción real PostgreSQL.

Evidencia local: gofmt/diff-check/vet/compile PASS; paquetes focales PASS; `go test -p=1 -timeout=12m ./...` PASS. El primer gate completo round 3 falló porque la composición PostgreSQL todavía creaba el registry local; se reemplazó por el adapter PostgreSQL. La primera focal WSL posterior validó migración N/N+1, adapter y una composición, pero descubrió colisión entre ProjectKey de fixtures; se añadió namespace de proyecto. La segunda focal autorizada obtuvo PASS en los tres tests Windows PostgreSQL (`cmd 5.492s`, adapter `0.998s`) y luego terminó exit 1 únicamente por un defecto del filtro Linux: el regex con `|` llegó al shell WSL sin preservarse y `/bin/sh` intentó ejecutar nombres de test. No es un fallo de producto, pero el exit terminal no fue 0 y por instrucción no se repite.

Cleanup de ambas focales: PASS, sin distros `FeatureProofPG35-*` ni roots `%TEMP%/featureproof-pg35-gate-*`. Manifest actualizado con artifacts/external-agent/tests. Estado solicitado: `blocked` hasta una nueva autorización de gate; no se lanzó gate completa posterior, no se modificó `feature_list.json`, sin commit/push.

Corrección de evidencia: el global serial exit 0 se ejecutó antes de añadir el namespace `ProjectKey` final a la migración/adapter PostgreSQL. Tras ese cambio sí pasaron compile/vet y los tres tests PostgreSQL reales de la matriz focal Windows; no se declara un segundo global del árbol final porque la instrucción tras el exit 1 Linux fue detenerse. El runner temporal filtrado se retiró del script versionado, por lo que el gate reviewer exacto conserva su interfaz anterior. También se eliminó el cache temporal materializado `featureproof-bound` tras verificar el target absoluto.

## Verificación local del árbol final round 3 — 2026-11-17

- `gofmt -l .`: PASS, salida vacía.
- `git diff --check`: PASS (solo avisos de conversión LF/CRLF, sin errores de whitespace).
- `go vet ./...`: PASS.
- `go test -p=1 -run '^$' -timeout=12m ./...`: PASS.
- `go test -p=1 -timeout=12m ./...`: PASS, exit 0; `cmd/featureproof` 31.807s y `internal/validationworkflow` completó dentro del límite.
- Manifest JSON válido y sincronizado con los adapters/tests Go finales: `resource_artifacts.go`, external-agent, artifacts y restart tests. La migración `0019_project_resource_artifacts.sql` se verificó como presente y modificada; no se añade al array `production` porque el auditor versionado acepta exclusivamente inventario Go (`changed_go_inventory(..., "*.go")`) y la marcaría como stale.
- Cleanup local: sin procesos Go/test y cache temporal `featureproof-bound` eliminado tras validar el path absoluto.

Estado solicitado: `ready_for_gate`. No se ejecutó WSL ni gate completa en esta verificación; `feature_list.json` intacto, sin commit/push.

## Gate PostgreSQL 16 final del árbol round 3 — 2026-11-17

Única ejecución autorizada del comando exacto reviewer:

`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage -Feature36Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe`

Resultado terminal: **FAIL, exit 1 antes del timeout de 45 minutos**. La matriz normal pasó (`cmd/featureproof` 33.615s, PostgreSQL 44.225s, planapproval 0.529s), igual que migración N/N+1 y runtime Linux. En la matriz race, `internal/controlplane/postgres` (62.946s) y `planapproval` (3.231s) pasaron, pero `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker` falló en `cmd/featureproof` con `compose process N project resource inventory drift`.

Causa observada: normal y race comparten la base aislada de la gate; el fixture vuelve a componer el mismo `ProjectKey` y descriptor/digest sintético con paths temporales distintos. El registry inmutable detecta correctamente que el mismo identity/digest intenta representar otro payload y falla cerrado. No se modificó código ni test y no hubo reintento, según la instrucción.

Coverage no comenzó; por tanto no existe fracción diferencial ni firmas SHA-256 para esta ejecución y no se presenta evidencia parcial como coverage final. Cleanup estricto PASS: sin procesos Go/test, distros `FeatureProofPG35-*` ni roots `%TEMP%/featureproof-pg35-gate-*`; el cache `featureproof-bound` se eliminó tras validar el path absoluto. Estado solicitado: `blocked`. `feature_list.json` intacto; sin commit/push.

## Aislamiento del fixture PostgreSQL normal/race — 2026-11-17

Causa corregida exclusivamente en test: `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker` derivaba siempre `ProjectKey=project/202` aunque cada invocación construía paths temporales nuevos. Normal y race comparten la base de la gate, por lo que el segundo fixture reutilizaba la misma identidad/digest para otro payload. Ahora el `ProjectID` de test se deriva del root aislado de cada invocación; los descriptors efectivos heredan ese ProjectKey. El comportamiento productivo fail-closed no cambia.

El mismo test muta después `environment_id` manteniendo ProjectKey/descriptor/digest y exige el error `project resource inventory drift`, demostrando que drift real continúa rechazado.

Evidencia:

- focal PostgreSQL 16 aislada normal: PASS, `TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker` 5.207s;
- focal PostgreSQL 16 aislada race UCRT: PASS, 10.101s;
- migración N/N+1 y runtime Linux del harness focal: PASS;
- `go test -p=1 -timeout=12m ./...`: PASS, exit 0 (`cmd/featureproof` 26.867s);
- inventario exacto del manifest frente a `changed_go_inventory`: PASS;
- gofmt/diff-check, vet/compile y auditoría de comentarios: PASS.

Cleanup estricto: sin distro/root UUID, procesos Go/test, `featureproof-bound` ni `scripts/__pycache__`. El filtro focal temporal fue retirado; el gate reviewer versionado conserva su comando exacto. Estado solicitado: `ready_for_gate`. `feature_list.json` intacto; sin commit/push.

## Gate exacto final posterior al aislamiento del fixture — 2026-11-17

Única ejecución autorizada del comando reviewer completo, sin modificar el gate y dentro de la ventana de 45 minutos: **FAIL, exit 1**.

Evidencia verde previa al umbral:

- normal: cmd 36.804s, PostgreSQL 51.044s, planapproval 0.536s;
- runtime Linux: PASS;
- race UCRT: cmd 56.467s, PostgreSQL 70.465s, planapproval 4.101s;
- perfiles: cmd 75.0%, external-agent 87.2%, projectconfig 79.3%, validationworkflow 84.8%, PostgreSQL combinado 41.7% y local Linux 85.5%;
- ambas ejecuciones de los perfiles llegaron a completar sus suites sin fallo funcional.

Fallo terminal: coverage diferencial exacta `1791/2121 = 84.44130127298444130127298444130127298444%`, por debajo del mínimo 85%. El script devolvió `feature 35 differential coverage failed with exit code 1`. La gate no alcanzó la comparación final de firmas SHA-256; por tanto no existen hashes finales comparables y no se afirma igualdad de firmas. No hubo reintento ni cambio posterior.

Detalle por archivos relevantes: `resource_artifacts.go` 20/26, `externalagent/service.go` 30/35, `projectconfig/artifacts.go` 47/68, `cmd/main.go` 82/124, `cmd/workflow.go` 278/337. Cleanup estricto PASS: sin procesos, distro/root UUID, `featureproof-bound` ni `scripts/__pycache__`. Estado solicitado: `blocked`. `feature_list.json` intacto; sin commit/push.

## Cierre focal del gap de coverage REV3 — 2026-11-17

Se añadieron pruebas significativas, sin reducir umbral ni retirar paths del manifest: external-agent rechaza composición parcial, binding corrupto/no disponible, materialización incompleta y recovery histórico no materializable; workflow rechaza artefactos históricos ausentes o cuyo descriptor no coincide; Local artifact store cubre cancelación, identidad inválida, missing, redelivery exacto/divergente, corrupción de payload/envelope/tamaño y fallos de filesystem; el contrato compartido comprueba propagación de setup/restart/cancel y detecta drift/error en cada checkpoint durable. PostgreSQL añade missing, validación ProjectKey, redelivery exacto/divergente y conserva corrupción fail-closed.

Evidencia focal:

- `count=10`: PASS en `projectconfig/local`, `projectconfig`, `externalagent` y `cmd/featureproof`.
- race focal UCRT `count=3`: PASS en los mismos cuatro paquetes.
- perfiles unitarios: `projectconfig/artifacts.go` sube de `47/68` a `64/68`; `contracttest/store.go` sube de `62/83` a `80/83` (`Run` 96.3%); external-agent materializa las ramas nuevas de binding/head/history/error observables en la focal.
- inventario cerrado: production/tests missing y stale vacíos contra `feature36-coverage-manifest.json`; migración 0019/adapters/tests permanecen declarados según el auditor Go-only.
- `git diff --check`: PASS; `go test -p=1 -timeout=12m ./...`: PASS, exit 0.

No se ejecutó WSL ni la gate completa en esta ronda. Se solicita la única gate PostgreSQL 16 exacta para obtener la fracción y firmas finales. `feature_list.json` intacto; sin commit/push.

## Gate exacta posterior al cierre de coverage y aislamiento GitHub — 2026-11-17

La única gate reviewer exacta terminó **FAIL, exit 1** antes de 45 minutos. Normal PASS (`cmd` 32.732s, PostgreSQL 39.915s, planapproval 2.958s), race UCRT PASS (`cmd` 51.712s, PostgreSQL 65.858s, planapproval 3.397s) y la primera base coverage completa PASS (`cmd` 75.0%, external-agent 87.6%, projectconfig 80.9%, validationworkflow 84.8%, PostgreSQL combinado 41.7%, Linux local 85.5%).

La segunda base abortó en `TestProductIdentityFailureParityAcrossAllSurfaces/github`: `github pull request workflow: invalid_request`. `feature36_coverage.py` propagó `CalledProcessError` durante el perfil cmd `-count=3`. Por ello no existe N/D decimal final ni firmas comparables; el agregado y su comparación no llegaron a ejecutarse. No hubo reintento.

Cleanup de la gate: cero procesos externos, distros WSL y roots UUID; `scripts/__pycache__` propio eliminado tras validar la ruta absoluta.

El fixture se aisló sin cambiar producción: cada superficie crea root, witness, service/store, target, resolver, transport y contexto semántico de 30s propios; las aserciones viven dentro de cada subtest, por lo que cada superficie puede ejecutarse individualmente. GitHub, única ruta con autoridad PR adicional, se ejecuta primero y ya no depende de presión/estado de las tres composiciones simples. Los timeouts productivos y sus tests específicos permanecen intactos.

Evidencia posterior:

- cada superficie individual `count=10`: PASS;
- conjunto completo/permutado `count=20`: PASS (`83.827s`);
- race focal UCRT `count=10`: PASS (`71.534s`);
- comando exacto de coverage cmd `go test -p=1 -count=3 -coverpkg=./cmd/featureproof -coverprofile=<temp> -timeout=12m ./cmd/featureproof`: PASS (`77.192s`, 75.1%).

Estado solicitado: `ready_for_gate`. No se ejecutó otra gate WSL; `feature_list.json` intacto, sin commit/push.

## Auditoría de la gate exacta interrumpida — 2026-11-19

La ejecución posterior al aislamiento real **no obtuvo exit terminal**: la sesión fue interrumpida externamente mientras trabajaba la segunda base PostgreSQL exterior, no por fallo de test ni por alcanzar la ventana de 45 minutos. El root UUID nació a las `17:29:41`; la primera firma se escribió a las `17:49:37` (19m56s) y el último temporal de la segunda base se modificó a las `17:52:01` (~22m20s), por debajo del límite operativo.

Evidencia completada antes de la interrupción:

- normal PASS: cmd `31.057s`, PostgreSQL `39.592s`, planapproval `3.025s`;
- race UCRT PASS: cmd `48.592s`, PostgreSQL `68.307s`, planapproval `3.136s`;
- primera base exterior completa, incluidas sus dos colecciones internas y Linux local: PASS;
- cobertura diferencial exacta de esa base: `1837/2125 = 86.44705882352941176470588235294117647059%`;
- `coverage-signature-1.json` existía con SHA-256 `f87050446cb1fd8732100ba905821a758fdc0f625f28c059790f4f59eb88ac35`.

La segunda base exterior había completado cmd (`84.437s`), PostgreSQL unitario, external-agent y projectconfig/local de su primera colección interna cuando se interrumpió. `coverage-signature-2.json` no llegó a crearse, por lo que no hubo comparación final de firmas y el agregado verde de la primera base no se declara gate PASS.

Cleanup estricto manual posterior: ningún proceso exacto seguía vivo; se desregistró únicamente `FeatureProofPG35-aea27da73f1b4abd9cd53127e5f1b88d`, se eliminaron su root UUID y el temporal `feature35-coverage-3yjzz9_l`, y se retiró solo `scripts/__pycache__` propio. Dos comprobaciones consecutivas terminaron con cero procesos, distros, roots y artefactos workspace.

Estado exacto: `blocked` por interrupción sin exit terminal ni segunda firma. No se ejecutaron tests ni otra gate durante esta auditoría; `feature_list.json` intacto, sin commit/push.

## Gate exacta terminal dentro de ventana real — 2026-11-19

Única ejecución reviewer exacta, sin modificar código, gate, threshold ni manifest: **PASS, exit 0**.

- inicio: `2026-11-19T18:53:41.1631580+01:00`;
- fin: `2026-11-19T19:22:59.3766572+01:00`;
- duración medida por el proceso: `1758.213s` (`29m18.213s`), dentro de la ventana real de 45 minutos;
- normal PASS: cmd `26.601s`, PostgreSQL `40.186s`, planapproval `0.547s`;
- race UCRT PASS: cmd `44.528s`, PostgreSQL `50.604s`, planapproval `3.182s`;
- ambas bases PostgreSQL exteriores completaron sus dos colecciones internas y runtime Linux;
- las dos bases imprimieron exactamente `1837/2125 = 86.44705882352941176470588235294117647059%`.

La comparación bloqueante de firmas terminó igual y permitió el exit 0; una diferencia habría abortado con `independent coverage databases produced different changed-statement signatures`. Los JSON de firma se eliminaron junto al root UUID en el `finally` de la gate, por lo que sus hashes literales no estaban disponibles después del exit terminal y no se inventan.

Cleanup: el `finally` dejó cero procesos, distros, roots UUID y temporales coverage de esta ejecución. Se eliminó únicamente `scripts/__pycache__` propio tras validar su path; dos comprobaciones consecutivas confirmaron cero procesos/distros/roots/temporales/artefactos workspace.

Estado solicitado: `ready_for_review`. `feature_list.json` intacto; sin commit/push.

## Corrección REV4 — snapshot de admisión, recovery estricto y scope de artifacts — 2026-11-19

REV4-1 queda separado por contrato: `SnapshotForAdmission` es la única operación que lee backing mutable y publica artifacts antes de que workflow/external-agent persistan el run; `Materialize` hace exclusivamente `Get` por el binding histórico. Recovery, status, result y cancel no pueden hacer `Put`. Los reproductores productivos local y PostgreSQL admiten A, eliminan su artifact, conservan backing actual válido y distinto, reinician y comprueban fallo cerrado sin repoblación. External-agent tiene el mismo reproductor sobre su materializador real.

REV4-2 incorpora `ProjectKey` a `ResourceArtifactKey`, al envelope verificado y a la ruta local `project/repository/kind/id/version/digest`; envelopes/rutas legacy quedan fail-closed. La suite contractual compartida local/PostgreSQL comprueba dos proyectos con la misma tuple y payloads distintos, ProjectKey inválido, redelivery exacto y divergente. El manifest conserva todos los archivos productivos/tests (`missing=0`, `changed Go unlisted=0`).

Evidencia estable previa a gate:

- focales artifact/restart local `count=10`: PASS;
- race focal REV4 UCRT `count=3`: PASS en projectconfig/local, external-agent, validationworkflow y cmd;
- global serial `go test -p=1 -count=1 -timeout=12m ./...`: PASS;
- gofmt, diff-check, vet y compile: PASS;
- focal PostgreSQL normal completa posterior a la clasificación de drift: PASS, `176.47s`, cleanup vacío.

La primera gate exacta posterior a REV4 falló antes de race/coverage en `TestGitHubRuntimePostgresCompositionOwnsLifecycle`: el fixture cancelaba por temporizador mientras `runGitHubServer` todavía componía/migraba. Se reemplazó por ownership explícito: el servidor adquiere el listener, señala readiness y el test cancela solo entonces, esperando el join con deadline. El count alto reveló además que `TestPostgresWorkflowCompositionSurvivesFilesystemRemoval` reutilizaba ProjectKey entre invocaciones; ahora deriva un namespace de su root, manteniendo la misma identidad entre las dos composiciones del mismo test y el drift real fail-closed.

Evidencia posterior a ambos fixes de estabilidad:

- PostgreSQL 16 aislado, ambos tests exactos `count=3`: PASS (`1.600s`);
- PostgreSQL 16 aislado, ambos tests exactos race UCRT `count=3`: PASS (`2.096s`);
- global serial final: PASS, incluido cmd `20.618s` y validationworkflow `37.678s`;
- manifest/gofmt/diff/vet: PASS; cleanup UUID: cero procesos, distros y roots.

No se ejecutó otra gate exacta completa después de estos fixes. Estado solicitado: `ready_for_gate`; `feature_list.json` intacto, sin commit/push.

## Gate exacta final posterior a lifecycle/fixture — 2026-11-19

La única ejecución completa autorizada terminó **PASS, exit 0** dentro de la ventana real:

- inicio `2026-11-19T20:18:19.8629377+01:00`;
- fin `2026-11-19T20:47:06.6760088+01:00`;
- duración `1726.8130711s` (`28m46.813s`), inferior a 45 minutos;
- normal PASS: cmd `26.501s`, PostgreSQL `40.004s`, planapproval `1.613s`;
- runtime Linux PASS;
- race UCRT PASS: cmd `44.783s`, PostgreSQL `50.921s`, planapproval `3.184s`;
- ambas bases exteriores y sus dos colecciones internas PASS, incluido Linux local `85.5%`.

Las dos bases produjeron exactamente `1870/2194 = 85.23245214220601640838650865998176845943%`. La comparación bloqueante de firmas terminó igual y permitió el exit 0; una diferencia habría abortado con el error explícito del gate. Los JSON de firma y sus hashes literales se eliminaron con el root UUID, por lo que no se inventan hashes posteriores.

Cleanup estricto: cero procesos exactos, distros WSL, roots UUID y perfiles/firmas en workspace. Se verificó la ruta absoluta y se eliminó únicamente `scripts/__pycache__` generado por la gate. Estado solicitado: `ready_for_review`; `feature_list.json` intacto, sin commit/push.

## REV5 — admisión causal/idempotente de external-agent — 2026-11-19

External-agent decide idempotencia antes de generar IDs, consultar el head o preparar artifacts. `PrepareForWorkflow` produce un candidate no persistente; `ConfiguredAdmissionRegistry.ReserveConfigured` confirma artifact, `RunRecord` con binding y primer evento bajo una frontera común. Replay materializa exclusivamente el binding/artifact histórico y nunca vuelve a consultar el head actual.

Local mantiene runs, eventos y artifacts en un único `runs.json` publicado por fsync+rename mientras conserva el claim de admisión; un crash antes del commit deja cero estado y un restart posterior observa la unidad completa. PostgreSQL añade migración `0020_external_agent_admissions.sql`; el adapter confirma el artifact de 0019, run/binding y evento en una transacción serializable. La composición productiva PG usa exclusivamente este registry y `ProjectConfiguration` del mismo Store/pool, sin registry local híbrido. El servicio cierra el owner PG tras unir workers/executor.

Los tests cubren restart antes/después del boundary local, 12 submits concurrentes con la misma key, replay después de un segundo head, pool PG reiniciado, rollback de artifact ante conflicto posterior y ausencia de filas en `project_configuration_run_bindings`. La focal PG16 encontró y corrigió un NUL inválido en la identidad del advisory lock (`SQLSTATE 22021`); la identidad final usa principal más key hash fija en UTF-8.

Evidencia previa a gate: focal local `count=20` PASS; suite external-agent `count=3` PASS; focal PG16 normal `count=3` PASS (`6.530s`) y race UCRT PASS (`3.601s`); gofmt/diff/vet/compile/manifest PASS; global serial post-fix PASS. Cleanup focal: cero procesos, distro, BD y root UUID.

La única gate exacta completa posterior terminó **FAIL, exit 1**, sin reintento. Inicio `2026-11-19T21:33:50.2940522+01:00`; el fallo se observó antes de la auditoría `21:53:26.6865257+01:00`, dentro de 45 minutos. Normal PASS: cmd `33.386s`, PostgreSQL `47.125s`, planapproval `0.590s`; race UCRT PASS: cmd `49.672s`, PostgreSQL `56.781s`, planapproval `3.973s`; Linux PASS. Coverage diferencial exacta: `2092/2592 = 80.70987654320987654320987654320987654321%`, inferior al mínimo 85%. La segunda base exterior y comparación de firmas no se ejecutaron. Déficits principales nuevos: `external_agent.go` `99/182` y `externalagent/registry.go` `75/106`.

Cleanup de gate: cero procesos, distros, roots UUID y perfiles/firmas workspace. Se eliminó únicamente el pycache propio y el temporal propio creado por la invocación accidental anterior, tras validar paths; temporales históricos anteriores se preservaron. T16 vuelve a `[ ]` porque el umbral obligatorio ya no se satisface. Estado: `blocked`; `feature_list.json` intacto, sin commit/push.

## Cierre de coverage y gate final REV5 — 2026-11-19

El gap se cerró con tests de contrato, sin retirar archivos ni relajar el 85%: local cubre replay exacto/divergente sobre artifact ya existente, contexto cancelado, missing y corrupción; PostgreSQL cubre lifecycle completo, claim/replay/concurrency, recovery, cancel idempotente/divergente, rollback transaccional, documentos corruptos y closed-pool; el vertical productivo compone external-agent con ProjectConfiguration, run/eventos y artifacts exclusivamente PostgreSQL. Los materializadores productivos cubren composición inválida, selector divergente, payload no serializable/corrupto, artifact histórico ausente y binding inválido.

El perfil PostgreSQL de feature 36 incorpora `cmd/featureproof` además del adapter porque `controlplane_integration_test.go` ya formaba parte del inventario tagged pero sus verticales no contribuían al perfil. Esto amplía tests medidos; no excluye producción ni cambia threshold. Las dos colecciones del perfil previo a gate produjeron `2230/2592 = 86.03395061728395061728395061728395061728%`: main `141/169`, `external_agent.go` `155/182` y registry local `88/106`. Focal PG16 normal/race/coverage PASS; external-agent unitario 86.8%; gofmt/diff/vet/compile/manifest y global serial PASS.

La única gate exacta final terminó **PASS, exit 0**:

- inicio `2026-11-19T23:05:34.7930245+01:00`;
- fin `2026-11-19T23:40:16.5144552+01:00`;
- duración `2081.7214307s` (`34m41.721s`), dentro de 45 minutos;
- normal PASS: cmd `27.791s`, PostgreSQL `50.641s`, planapproval `0.511s`;
- runtime Linux PASS;
- race UCRT PASS: cmd `50.270s`, PostgreSQL `52.250s`, planapproval `3.182s`;
- ambas bases exteriores completaron sus dos colecciones internas y Linux con `2230/2592 = 86.03395061728395061728395061728395061728%`.

La comparación bloqueante de firmas terminó igual y permitió exit 0. Los JSON y hashes literales se eliminaron con el root UUID en el `finally`, por lo que no se inventan valores posteriores. Cleanup auditado: cero procesos, distros, roots UUID y perfiles/firmas workspace; se eliminó solo el pycache propio tras validar su ruta. T16 vuelve a `[x]`. Estado solicitado: `ready_for_review`; `feature_list.json` intacto, sin commit/push.
