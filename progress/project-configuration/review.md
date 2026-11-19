# Review independiente — Feature 36 `project-configuration`

## Novena revisión — 2026-11-19

Resultado: **APPROVED**

Los únicos blockers de Round6–Round8 eran artefactos temporales de verificación
y quedan corregidos por ruta exacta. No hubo cambios funcionales posteriores a
la aprobación sustantiva de Round6: REV4 y REV5 permanecen cerrados, R1–R18
tienen implementación y tests concretos, T1–T16 están completas y la gate
terminal conserva evidencia reproducible suficiente.

### Evidencia de cleanup y estado final

- Ausentes en el workspace por ruta exacta: `$profile`, `.coverage36`,
  `feature36-local`, `scripts/__pycache__` y
  `scripts/__pycache__/feature35_coverage.cpython-314.pyc`.
- El scan recursivo del workspace no encuentra perfiles, firmas, pycache,
  runners ni roots temporales. `scripts/feature36_coverage.py` y
  `scripts/feature36-coverage-manifest.json` son los dos artefactos fuente
  esperados de la feature, no temporales.
- Ausentes en `%TEMP%`: `featureproof-bound`,
  `featureproof-pg35-gate-*`, `feature36-runner-*`, `feature36-local-*` y
  `.coverage36-*`.
- Cero procesos y distros WSL coincidentes con `feature36`,
  `featureproof-bound` o `.coverage36`.
- `git status --short` contiene solo el diff esperado de feature 36;
  `git diff --check`: PASS salvo avisos CRLF informativos.
- El scan se realizó solo con PowerShell, Git, `rg` y consulta read-only de WSL;
  no se ejecutó/importó Python ni se ejecutaron tests, por lo que no se
  recrearon temporales.
- Los archivos centrales de causalidad external-agent y el script de coverage
  mantienen tamaño/fecha de Round6. Solo `progress/current.md` incorporó el
  registro autorizado de cleanup.

### Verificación funcional heredada y aceptada

- Replay reclama la idempotency key antes de IDs/head/snapshot; preparación no
  persistente y materialización histórica Get-only.
- Local publica artifact + `RunRecord(binding)` + primer evento en una unidad
  fsync/rename; PostgreSQL 0020 confirma la misma unidad serializable junto al
  artifact 0019, sin backend híbrido ni bindings huérfanos.
- Restart, fault rollback, concurrencia, cambio de head, corrupción,
  redelivery, aislamiento cross-project, lifecycle GitHub y manifest completo
  quedaron verificados en Round6.
- Gate final: exit 0 `23:05:34–23:40:16`; normal, Linux y race PASS; dos bases
  idénticas `2230/2592 = 86.03395061728395061728395061728395061728%` y
  comparación bloqueante de firmas PASS.

### Decisión de la novena revisión

No quedan hallazgos bloqueantes. La feature 36 queda aprobada para que el
leader realice la transición final y el cierre de historial.

## Octava revisión — 2026-11-19

Resultado: **REJECTED**

REV7-1 queda corregido: no existen `scripts/__pycache__` ni su bytecode, y el
scan read-only no ha ejecutado/importado Python ni los ha recreado. El workspace
del repositorio está libre de `$profile`, `.coverage36`, `feature36-local`,
pycache, perfiles, firmas y runners. Persiste una raíz temporal observable fuera
del workspace.

### REV8-1 — Permanece la raíz temporal `featureproof-bound`

- Severidad: blocker
- Requirement o regla: R18; T16; `docs/verification.md` §4.6, §8 y §9
- Archivo y línea:
  `C:\Users\asier\AppData\Local\Temp\featureproof-bound` (directorio temporal,
  sin línea)
- Evidencia: la ruta exacta existe, contiene materializaciones de contratos,
  runtimes y specifications, y su `LastWriteTime` es
  `2026-11-19 23:46:49.5274182 +01:00`, posterior a la gate terminal. Entre los
  candidatos `feature36*`, `.coverage36*` y `featureproof-bound*` del temporal
  del sistema, es el único modificado desde las 23:00. La evidencia terminal
  afirmaba que `featureproof-bound` había sido eliminado.
- Impacto: la afirmación de cleanup total todavía no coincide con el estado
  observable. Aunque no afecta al diff funcional, deja datos de fixtures fuera
  del workspace después de la verificación.
- Corrección requerida: verificar y eliminar únicamente la ruta absoluta
  `C:\Users\asier\AppData\Local\Temp\featureproof-bound`; repetir el scan
  read-only de rutas/procesos/distros. No hace falta repetir tests ni la gate
  WSL si no cambia el repositorio.

### Evidencia positiva y comandos de round 8

- Ausentes por ruta exacta: `$profile`, `.coverage36`, `feature36-local`,
  `scripts/__pycache__` y
  `scripts/__pycache__/feature35_coverage.cpython-314.pyc`.
- Scan recursivo del workspace: cero temporales coincidentes; `git status`
  contiene solo el diff esperado de feature 36; `git diff --check`: PASS salvo
  avisos CRLF informativos.
- Cero procesos y distros WSL coincidentes con `feature36`,
  `featureproof-bound` o `.coverage36`.
- Los archivos funcionales centrales conservan tamaño y fecha de Round6; no
  surgió cambio funcional y permanece la aprobación sustantiva de R1–R18 y
  T1–T16.

### Decisión de la octava revisión

La implementación funcional continúa aprobada, pero el cierre permanece
rechazado hasta retirar la raíz temporal exacta y observar cleanup completo.

## Séptima revisión — 2026-11-19

Resultado: **REJECTED**

REV6-1 queda corregido para el perfil literal: la ruta exacta
`C:\Users\asier\Desktop\proyectos\FeatureProof\$profile` ya no existe. Tampoco
existen `.coverage36`, `feature36-local`, runners temporales ni procesos
asociados. No hay cambios funcionales posteriores a Round6 y sus conclusiones
sobre R1–R18/T1–T16 permanecen válidas. Queda un único artefacto temporal en
el workspace.

### REV7-1 — Permanece un bytecode Python temporal sin seguimiento

- Severidad: blocker
- Requirement o regla: R18; T16; `docs/verification.md` §4.6, §8 y §9
- Archivo y línea:
  `scripts/__pycache__/feature35_coverage.cpython-314.pyc` (binario, sin línea)
- Evidencia: `git status --short` muestra `?? scripts/__pycache__/`. La
  inspección exacta encuentra un único archivo de 16815 bytes, creado y
  modificado el `2026-11-19 23:46:28`. La evidencia de cierre y
  `progress/current.md` afirman que el pycache propio fue eliminado.
- Impacto: el workspace no satisface el cleanup documentado y `git add -A`
  puede incorporar un artefacto generado ajeno a la feature.
- Corrección requerida: verificar la ruta absoluta y eliminar únicamente
  `C:\Users\asier\Desktop\proyectos\FeatureProof\scripts\__pycache__`; repetir
  `git status --short` y el scan de temporales. No hace falta repetir tests ni
  la gate WSL si no cambia ningún archivo funcional, test, manifest o script.

### Evidencia positiva y comandos de round 7

- `$profile`, `.coverage36` y `feature36-local`: ausentes por ruta exacta.
- Cero runners/procesos `feature36`, `featureproof-bound` o `.coverage36`.
- `git diff --check`: PASS, salvo avisos CRLF informativos; `gofmt -l .`:
  PASS.
- El diff funcional conserva los mismos archivos y contenido revisados en
  Round6; los tres archivos centrales de causalidad external-agent no cambiaron
  desde antes de la gate terminal.
- No se repitieron focales ni gate: Round6 ya aprobó la implementación y el
  único trabajo autorizado entre rondas fue cleanup/documentación.

### Decisión de la séptima revisión

La implementación funcional continúa aprobada, pero el cierre permanece
rechazado hasta retirar el pycache residual y verificar un workspace sin
artefactos temporales.

## Sexta revisión — 2026-11-19

Resultado: **REJECTED**

REV5-1 queda corregido. `Submit` reclama y resuelve la idempotency key antes
de generar IDs, consultar el head o preparar el snapshot; replay materializa
solo el binding/artifact histórico. Local confirma artifact, `RunRecord` con
binding y primer evento en el mismo documento publicado mediante fsync/rename,
y PostgreSQL confirma la misma unidad en una transacción serializable de la
migración 0020. Las pruebas focales de concurrencia, fault, restart y cambio de
head pasan. Persiste una contradicción bloqueante en la evidencia de cierre.

### REV6-1 — El cleanup declarado no coincide con el workspace entregado

- Severidad: blocker
- Requirement o regla: R18; T16; `docs/verification.md` §4.5, §8 y §9
  (coverage reproducible, limpieza de integraciones y evidencia falsa o no
  reproducible bloqueante)
- Archivo y línea: `$profile:1`; `progress/project-configuration/implementation.md`
  (sección `Cierre de coverage y gate final REV5`)
- Evidencia: `git status --short` muestra `?? $profile`. El archivo literal
  `C:\Users\asier\Desktop\proyectos\FeatureProof\$profile` tiene 866768 bytes,
  fecha `2026-11-19 22:44:40` y comienza por `mode: set` seguido de entradas de
  coverage Go. El informe terminal posterior afirma que el cleanup dejó cero
  perfiles/firmas en workspace. Dos inspecciones posteriores a los checks
  focales siguen encontrando exactamente ese perfil.
- Impacto: el estado entregado contradice la evidencia usada para cerrar T16 y
  puede incorporar accidentalmente un artefacto de coverage con `git add -A`.
  La gate funcional puede haber pasado, pero su afirmación de cleanup no es
  reproducible sobre el árbol revisado.
- Corrección requerida: verificar la ruta absoluta y eliminar únicamente el
  archivo literal `$profile`; repetir `git status --short` y la auditoría de
  perfiles/firmas, y corregir la evidencia de cleanup. No es necesario repetir
  la gate WSL de 34 minutos si no cambia código, tests, manifest o scripts.

### Evidencia positiva y comandos de round 6

- Precondiciones PASS: feature 36 es la única `in_progress`, spec aprobada,
  T1–T16 `[x]` e informe del implementer presente.
- `gofmt -l .`, `git diff --check` y `go vet ./...`: PASS (solo avisos CRLF
  informativos).
- `go test -p=1 -count=5 -timeout=12m ./internal/externalagent
  ./internal/projectconfig/... ./cmd/featureproof`: PASS; external-agent,
  projectconfig/local y composición productiva completaron sin fallos.
- `internal/externalagent/service.go:268-319` decide replay antes de IDs/head y
  usa `PrepareForWorkflow` no persistente solo para una admisión nueva.
- `internal/externalagent/registry.go:67-124,535-570` publica artifact, run con
  binding y primer evento en un único `runs.json`, con file fsync, rename en el
  mismo directorio y directory fsync. Los tests prueban restart antes/después
  del commit, fault sin estado parcial, corrupción y replay concurrente.
- `internal/controlplane/postgres/external_agent.go:30-131` conserva el claim
  mediante advisory lock con identidad UTF-8 `principal:keyHash`; la reserva
  serializable inserta/verifica artifact 0019, run/binding y evento antes del
  mismo commit. La migración 0020 está embebida por el manifest gap-free y las
  pruebas verifican restart, 12 replays concurrentes, rollback sin huérfanos y
  cero filas en `project_configuration_run_bindings`.
- REV4 permanece intacto: `SnapshotForAdmission` es la ruta productiva de
  `Put`; materialización/recovery/read/status/result/cancel son `Get` estricto;
  identidad local de artifact incluye `ProjectKey`, y el contrato compartido
  local/PostgreSQL cubre aislamiento, key inválida y redelivery.
- Manifest: 43 archivos Go cambiados, 43 listados, cero cambiados sin listar y
  cero entradas ausentes. Los comentarios sistémicos revisados permanecen en
  las entidades y funciones nuevas.
- Se valida el informe de gate exit 0 `23:05:34–23:40:16`: normal, Linux y race
  PASS; dos bases `2230/2592 = 86.03395061728395061728395061728395061728%`
  y comparación de firmas PASS. No se repitió la gate WSL larga.

### Decisión de la sexta revisión

R1–R18 y T1–T16 tienen implementación y cobertura suficiente, incluida la
frontera causal corregida de REV5. La feature permanece rechazada solamente
hasta reconciliar el perfil residual con la evidencia de cleanup terminal.

## Quinta revisión — 2026-11-19

Resultado: **REJECTED**

REV4-1 y REV4-2 quedan corregidos para workflow y para los dos artifact
backends: snapshot y lectura histórica son operaciones distintas, recovery es
`Get`-only, el store local incluye `ProjectKey` y la suite compartida comprueba
aislamiento local/PostgreSQL. Persiste una frontera causal en external-agent.

### REV5-1 — External-agent persiste un binding antes de reservar la admisión idempotente

- Severidad: blocker
- Requirement o regla: R8, R11, R12, R16; T11, T12, T14
- Archivo y línea: `internal/externalagent/service.go:49-51`,
  `internal/externalagent/service.go:229-249`,
  `internal/projectconfig/admission.go:36-44`,
  `internal/projectconfig/service.go:318-327`
- Evidencia: `ProjectConfigurationResolver` expone únicamente
  `BindForWorkflow`. En cada `Submit`, external-agent genera IDs nuevos, llama
  a ese método, ejecuta `SnapshotForAdmission` y solo después llama a
  `Registry.Reserve`. El `AdmissionResolver.BindForWorkflow` productivo delega
  en `ResolveForAdmission`, que ejecuta `store.Bind` y persiste el binding. Por
  tanto snapshot sí precede al `RunRecord`, pero no al binding durable. Además,
  una redelivery con la misma idempotency key repite bind/snapshot para un
  internal ID nuevo antes de que `Registry.Reserve` devuelva el run anterior.
  Los tests external-agent usan resolvers fake sin store durable y no
  comprueban ausencia de un segundo binding/artifact en redelivery.
- Impacto: un crash o fallo de snapshot deja un binding sin su run causal. Una
  redelivery después de cambiar el head puede persistir un binding huérfano y
  artifacts de la generación nueva, o fallar por el head actual, en vez de
  converger directamente al run ya admitido. No se cumple que binding/run
  existan juntos o ninguno ni que la redelivery coincidente sea no mutante.
- Corrección requerida: usar una preparación no persistente
  (`PrepareForWorkflow`/candidate) para external-agent, snapshotear ese único
  candidate y confirmar binding + `RunRecord` + primer evento bajo una frontera
  idempotente común; como mínimo, el store que posee la idempotencia debe evitar
  cualquier bind/snapshot nuevo en replay. Añadir un test vertical con store
  real: primera admisión, cambio de head y redelivery de la misma key; debe
  devolver el primer binding sin nuevas filas/artifacts. Añadir fault test entre
  prepare/snapshot/commit que observe ambos binding+run o ninguno.

### Evidencia positiva y comandos de round 5

- Precondiciones PASS: feature 36 única `in_progress`, aprobación humana,
  T1–T16 `[x]` e informe existente.
- `gofmt -l .`, `git diff --check` y `go vet ./...`: PASS (solo avisos CRLF
  informativos).
- `go test -p=1 -count=3 -timeout=12m ./internal/projectconfig/... ./internal/externalagent ./internal/validationworkflow ./cmd/featureproof`: PASS.
- `SnapshotForAdmission` es la única ruta de `Put` de los materializadores;
  `Materialize`, recovery, status, result y cancel usan `Get` histórico.
- Los tests local y PostgreSQL eliminan artifacts, mantienen backing válido y
  distinto tras restart, y comprueban fallo cerrado sin repoblación.
- `ResourceArtifactKey`, envelope y path local incluyen `ProjectKey`; la suite
  `RunResourceArtifacts` se ejecuta contra local y PostgreSQL con cross-project,
  key inválida y redelivery exacta/divergente.
- El lifecycle GitHub espera readiness real y join con deadline, sin sleeps ni
  aceptar code 3; los namespaces de fixtures son únicos y no cambian límites
  productivos.
- La gate exacta documentada terminó exit 0 entre `20:18:19` y `20:47:06`:
  normal/Linux/race PASS, dos bases iguales, firmas bloqueantes iguales,
  coverage `1870/2194 = 85.23245214220601640838650865998176845943%` y cleanup
  vacío. No se repitió WSL en esta revisión.

### Decisión de la quinta revisión

R4/R13 y las rutas de recovery histórico de REV4 tienen evidencia suficiente,
pero la admisión external-agent todavía viola causalidad e idempotencia de
R8/R11/R12/R16. La feature permanece rechazada hasta cerrar esa frontera con
persistencia y tests reales.

## Cuarta revisión — 2026-11-19

Resultado: **REJECTED**

REV3-1 queda corregido en el flujo normal: external-agent captura un binding
por `Submit`, persiste binding/límites en `RunRecord`, ejecuta con recursos
materializados para ese run y vuelve a comprobar el binding histórico en
status/result/cancel. REV3-2 también dispone ya de stores durables local y
PostgreSQL, restart real, checksums de payload y una gate PostgreSQL terminal
verde. Persisten dos rutas que impiden afirmar recuperación histórica exacta y
equivalencia entre backends.

### REV4-1 — Recovery repuebla artifacts ausentes desde el backing actual

- Severidad: blocker
- Requirement o regla: R11, R12, R13, R16; T12, T13, T15
- Archivo y línea: `cmd/featureproof/workflow.go:251-281`,
  `cmd/featureproof/main.go:1022-1031`,
  `cmd/featureproof/workflow_resource_restart_test.go:61-112`,
  `internal/externalagent/project_configuration_test.go:131-192`
- Evidencia: los dos materializadores interpretan `CategoryMissingReference`
  como una primera admisión aunque también se invocan desde recovery/read de
  un run ya persistido. Workflow vuelve a ejecutar `loadWorkflowConfig()`, lee
  specification/runtime/contract actuales y hace `Put`; external-agent vuelve
  a serializar `materializer.selection.Value` y hace `Put` si el alias actual
  coincide. No existe una señal de fase que distinga snapshot inicial de reload
  histórico. El test workflow de missing cambia la variable a un path ausente,
  por lo que solo demuestra fallo cuando tampoco existe backing actual; el test
  external-agent sustituye el materializador por un fake que devuelve error.
- Impacto: si el artifact durable de un run se elimina o falta tras restart,
  pero el selector/config actual continúa disponible, el run histórico puede
  adoptar bytes actuales bajo su descriptor antiguo en vez de fallar
  `integrity`/`missing_reference`. El binding permanece nominalmente igual pero
  deja de gobernar los consumers, exactamente la condición que REV3-2 debía
  cerrar.
- Corrección requerida: separar explícitamente la operación de snapshot de
  admisión de la operación estricta de materialización histórica. Los artifacts
  deben confirmarse antes/junto al binding/run; una vez persistido el run,
  recovery/read solo puede hacer `Get` exacto y debe fallar cerrado si falta.
  Añadir tests productivos local y PostgreSQL que admitan A, eliminen/omitan su
  artifact, mantengan un backing actual válido y distinto, reinicien el proceso
  y demuestren que A nunca se repuebla ni adopta el backing actual.

### REV4-2 — El artifact store local omite `ProjectKey` de la identidad durable

- Severidad: blocker
- Requirement o regla: R4, R13; T5, T6, T7
- Archivo y línea: `internal/projectconfig/artifacts.go:18-23`,
  `internal/projectconfig/artifacts.go:145-160`,
  `internal/controlplane/postgres/resource_artifacts.go:20-63`,
  `internal/controlplane/postgres/migrations/0019_project_resource_artifacts.sql:1-11`
- Evidencia: `ResourceArtifactKey` y el path local contienen únicamente
  `(kind,id,version,digest)`; `artifactKey` ni valida ni incorpora
  `descriptor.ProjectKey`. PostgreSQL, en cambio, valida el `ProjectKey` y usa
  `(project_id,repository_id,kind,id,version,digest)` tanto en PK como en todas
  las queries. No hay contract test de dos proyectos con la misma tuple de
  descriptor y payloads distintos, ni test local de `ProjectKey` inválido.
- Impacto: dos proyectos/repositorios bajo el mismo root local pueden leer el
  mismo artifact o colisionar con `integrity`, mientras PostgreSQL los aísla.
  Los payloads de workflow contienen paths/configuración de composición y no
  quedan identificados por el digest del descriptor (la propia implementación
  necesitó namespace de proyecto en PostgreSQL tras observar esa colisión).
  Local y PostgreSQL no preservan el mismo contrato observable ni el mismo
  scope.
- Corrección requerida: incluir y validar `ProjectKey` en la identidad/envelope
  y derivación de path local, manteniendo compatibilidad fail-closed; añadir una
  contract suite compartida para aislamiento cross-project, `ProjectKey`
  inválido, redelivery exacto y divergente en ambos adapters.

### Evidencia positiva y comandos de round 4

- Precondiciones PASS: feature 36 es la única `in_progress`, aprobación humana
  presente, T1–T16 `[x]` e informe del implementer existente.
- `gofmt -l .`: PASS; `git diff --check`: PASS salvo avisos CRLF informativos.
- `go vet ./...`: PASS.
- `go test -p=1 -count=3 -timeout=12m ./internal/projectconfig/... ./internal/externalagent ./internal/validationworkflow ./cmd/featureproof`: PASS.
- REV3-1: `TestExternalAgentCapturesEachHeadAndReadsHistoricalBinding` y las
  ramas fail-closed de external-agent pasan; `RunRecord` conserva binding,
  retention y timeout.
- REV3-2 positiva: restart con artifact presente, corrupción/redelivery y el
  adapter PostgreSQL/migración 0019 tienen evidencia real. La gate exacta
  documentada terminó exit 0 entre `18:53:41` y `19:22:59`, normal/race PASS,
  dos bases idénticas y coverage `1837/2125 =
  86.44705882352941176470588235294117647059%`, con cleanup vacío.
- Los ajustes de estabilidad permanecen acotados a fixtures/harnesses; no se
  observó reducción del threshold ni exclusión del inventario para inflar
  coverage.

### Decisión de la cuarta revisión

La implementación mejora materialmente external-agent, restart y persistencia
PostgreSQL, pero R11/R12/R13/R16 aún no se cumplen en las rutas de artifact
ausente y en el adapter local cross-project. La feature permanece rechazada
hasta que ambas garantías tengan implementación y tests compartidos.

## Tercera revisión — 2026-11-17

Resultado: **REJECTED**

REV2-2 queda corregido: `Postgres.Bind` compara los checksums separados de
binding y effective, y el test PostgreSQL comprueba `integrity` y ausencia de
mutación. La nueva materialización también corrige el cambio de head dentro de
un workflow ya arrancado. Sin embargo, REV2-1 sigue incompleto en dos fronteras
productivas exigidas por R11/R12/R16.

### REV3-1 — External-agent continúa fijado al head de arranque

- Severidad: blocker
- Requirement o regla: R11, R12, R16; T14, T15
- Archivo y línea: `cmd/featureproof/main.go:996-1028`,
  `cmd/featureproof/main.go:1054-1059`
- Evidencia: `composeExternalAgentService` ejecuta `OperationShowActive` una
  sola vez y construye catálogo, executor, retention y timeout con ese
  effective snapshot. El `AdmissionResolver` creado en composición no se
  entrega a `externalagent.Service`; cada `Submit` tampoco produce ni persiste
  un `RunConfigurationBinding`. El nuevo `ProjectResourceMaterializer` solo
  pertenece a `validationworkflow.Service`.
- Impacto: un proceso external-agent vivo sigue admitiendo con la selección,
  límites y retención anteriores después de publicar otro head. Esa superficie
  no tiene binding inmutable para recovery/redelivery y contradice el caso
  explícito de R16 para external agent.
- Corrección requerida: resolver y persistir el binding en cada admisión de
  external-agent y materializar catálogo/executor/límites desde él, conservando
  el binding histórico en read/cancel/recovery. Añadir un test vertical que
  mantenga el mismo servicio, publique otro head y demuestre que el segundo run
  usa el nuevo snapshot mientras el primero conserva el anterior.

### REV3-2 — Recovery tras reinicio no verifica ni recupera recursos por digest

- Severidad: blocker
- Requirement o regla: R4, R11, R12, R13, R16; T12, T13, T15
- Archivo y línea: `cmd/featureproof/workflow.go:166-212`,
  `cmd/featureproof/workflow.go:849-927`,
  `cmd/featureproof/workflow.go:1296-1332`,
  `internal/validationworkflow/project_configuration_test.go:206-250`
- Evidencia: la caché por `EffectiveDigest` conserva resources históricos solo
  durante la vida del proceso. Tras reiniciar queda vacía y la materialización
  vuelve a leer `FEATUREPROOF_WORKFLOW_CONFIG`, specification, manifest,
  contract path, environment inventory y resolvers actuales. La proyección de
  descriptors usa únicamente `Selection`; no compara `ResourceDescriptor`
  version/digest con el artefacto materializado. Los dos tests nuevos usan un
  fake que copia `descriptor.Selection` y `TestRecoveryMaterializes...` llama a
  `Decide` sobre la misma instancia; no cierra/reabre el servicio ni muta el
  recurso backing.
- Impacto: un run histórico recuperado tras restart puede ejecutar con contrato,
  runtime, target o resolver actuales aunque su binding declare otro digest; o
  no puede reconstruirse si la selección antigua ya no está en el inventario.
  El binding queda nominalmente intacto pero no gobierna los consumers reales.
- Corrección requerida: resolver cada descriptor por `(kind, id, version,
  digest)` desde un registry/versioned artifact durable y verificar el digest
  antes de construir consumers. Añadir un test real de restart con store
  durable: admitir run A, cambiar backing/head reutilizando selector con digest
  nuevo, crear otro service/materializer y recuperar A; debe usar el artefacto
  histórico exacto o fallar cerrado, nunca adoptar el actual.

### Evidencia positiva y gates de round 3

- REV2-2: `project_configuration.go:301-308` lee/compara
  `binding_digest`; `:327-334` hace lo mismo con `effective_digest`.
  `TestPostgresProjectConfigurationNegativeQueriesAndCorruption` verifica ambas
  corrupciones y comprueba 1/0 filas después del fallo.
- Los fixes de estabilidad están acotados a tests: resolver MCP test-local sin
  environment global y `FinalizeTimeout=30s` solo en `newRejectedFlowHarness`.
  `functional_flow_test.go` figura en el manifest completo.
- `gofmt -l .`, `git diff --check` y `go vet ./...`: PASS.
- tests de head/recovery nuevos `-count=20`: PASS.
- test MCP de identidad `-count=5`: PASS.
- subprueba de finalización `-count=10`: PASS.
- El gate PostgreSQL final documenta exit 0, firmas internas iguales y cobertura
  exacta `1529/1797 = 85.08625486922648859209794101279910962716%`, con cleanup
  vacío. No se repitió WSL en esta ronda, conforme a la instrucción del leader.

### Decisión de la tercera revisión

R1–R10, R13–R15, R17–R18 y REV2-2 tienen evidencia suficiente en el estado
revisado. R11, R12 y R16 siguen fallando para external-agent y recovery real
tras reinicio; por tanto T12–T15 no pueden considerarse completas y la feature
permanece rechazada.

## Segunda revisión — 2026-11-17

Resultado: **REJECTED**

Las correcciones resuelven materialmente los límites estructurales, el máximo
durable común, la obligatoriedad de `ProjectConfig` en `NewService` y las
comprobaciones directas de version/activation/head/effective. Persisten dos
defectos bloqueantes.

### REV2-1 — Un proceso ya compuesto no adopta un nuevo head activo

- Severidad: blocker
- Requirement o regla: R11, R12, R16; T13–T15
- Archivo y línea: `cmd/featureproof/workflow.go:811-825`,
  `internal/validationworkflow/service.go:127-151`,
  `internal/validationworkflow/service.go:426-451`
- Evidencia: composición lee `OperationShowActive` una sola vez, aplica sus
  selecciones a `workflowConfig` y construye adapters/catálogos con ellas. En
  cada `Start`, el resolver sí captura el head vigente, pero el único puente
  posterior compara IDs del comando ya compuesto contra el binding. Si publish
  activa otra selección, la admisión nueva se rechaza por mismatch en vez de
  reconstruirse desde el nuevo effective snapshot; si conserva IDs y cambia
  versión/digest, los adapters siguen siendo los construidos al arrancar.
- Impacto: un cambio confirmado no gobierna las nuevas admisiones mientras el
  proceso siga vivo. Se incumple que cada ingreso construya contratos, target,
  identities, fixtures y profiles desde su binding actual, y se convierte el
  reinicio del proceso en requisito implícito no aprobado.
- Corrección requerida: hacer que la composición de recursos ocurra por
  admisión desde el `RunConfigurationBinding` capturado, o instalar un registry
  versionado capaz de resolver exactamente sus descriptors/digests. Añadir un
  test vertical que componga una vez, publique un segundo head con selecciones
  distintas, admita un segundo run y pruebe que usa exclusivamente el segundo
  binding sin reinicio; añadir el caso mismo ID/nuevo digest.

### REV2-2 — `Postgres.Bind` acepta checksums durables alterados

- Severidad: blocker
- Requirement o regla: R7, R11, R13, R14; T5, T7, T11
- Archivo y línea: `internal/controlplane/postgres/project_configuration.go:299-312`,
  `internal/controlplane/postgres/project_configuration.go:326-332`
- Evidencia: el camino idempotente selecciona únicamente `binding`, no
  `binding_digest`; valida el digest embebido en JSON pero nunca lo compara con
  la columna durable. El camino de binding nuevo selecciona únicamente
  `effective`, no `effective_digest`. Por tanto, alterar solo cualquiera de
  esas columnas —casos que `Binding`/`Effective` sí rechazan— puede ser aceptado
  por `Bind` y devolver éxito/convergencia.
- Impacto: PostgreSQL no preserva una única garantía de integridad en todas las
  rutas de reload/admission y diverge del adapter local. Una redelivery puede
  ocultar corrupción durable en vez de fallar `integrity`.
- Corrección requerida: seleccionar y comparar ambos digests en `Bind`, además
  de mantener la recomputación del payload. Extender la contract suite y el
  gate PostgreSQL real con: corrupción exclusiva de `binding_digest` seguida de
  redelivery `Bind`, y corrupción exclusiva de `effective_digest` seguida de
  binding de un run nuevo; ambos deben fallar `integrity` sin mutación.

### Verificación de la segunda revisión

- `gofmt -l .`: PASS.
- `git diff --check`: PASS (solo avisos CRLF informativos).
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- `go test ./... -count=1`: FAIL bajo ejecución paralela por timeouts en tests
  históricos de `internal/contextassembly` y `internal/correction`; los
  paquetes de feature 36 ya emitidos en esa ejecución pasaron. No se considera
  evidencia verde canónica.
- La repetición focal `-count=3` fue detenida por límite de tiempo después de
  PASS en `internal/projectconfig` y `internal/projectconfig/local`, antes de
  resultado para `validationworkflow`.
- No se repitieron el race global ni el gate WSL/PostgreSQL largo en esta ronda;
  los resultados declarados por el implementer no eliminan los dos defectos
  deterministas anteriores.

### Decisión de la segunda revisión

La feature permanece rechazada. R11/R13/R16 no están satisfechos en todas las
rutas productivas/durables y faltan los dos tests reproductores indicados.

Fecha: 2026-11-16  
Resultado: **REJECTED**

## Precondiciones

- Existe exactamente una feature `in_progress`: feature 36.
- La spec contiene aprobación humana (`user`, `2026-11-16T10:24:25+01:00`).
- T1–T16 aparecen marcadas `[x]`.
- Existe `progress/project-configuration/implementation.md`.
- Se inspeccionaron los documentos obligatorios, la spec completa, el informe, el diff tracked y los archivos untracked de la feature.
- `progress/current.md` conserva la historia previa y añade ocho líneas; no se observó truncado.

## Hallazgos

## REV-1 — La configuración no está integrada en ninguna superficie productiva

- Severidad: blocker
- Requirement o regla: R1, R12, R16; T12, T13, T14
- Archivo y línea: `cmd/featureproof/workflow.go:63`, `cmd/featureproof/workflow.go:562`, `cmd/featureproof/workflow.go:577`, `cmd/featureproof/workflow.go:1069`, `internal/validationworkflow/service.go:132`
- Evidencia: `workflowConfig` sigue siendo la fuente productiva de provider, model, auth/runtime profile, contract, target, policy, identities/fixtures y demás selecciones project-owned. `loadWorkflowConfig` sigue exigiéndolas desde el bootstrap. La composición productiva de `validationworkflow.NewService` no suministra `Dependencies.ProjectConfig`; el servicio, además, trata ese dependency como opcional mediante `if s.deps.ProjectConfig != nil`. Una búsqueda de usos productivos de `projectconfig`, `AdmissionResolver`, `Gateway` y `ProjectConfig` fuera del propio paquete solo encuentra `validationworkflow` y los stores PostgreSQL; no hay adapters CLI administrativos, API/MCP, external-agent o GitHub. Los tests citados ejercitan el gateway/resolver aislados y mutan manualmente `service.deps.ProjectConfig`; no atraviesan una superficie productiva.
- Impacto: todos los ingresos reales continúan admitiendo runs sin active configuration ni binding, incluido el modo local. Flags/env/bootstrap siguen seleccionando recursos. GitHub no mapea repository identity a project key y CLI/API/MCP no convergen en el application service. La garantía central de la feature no existe en producto.
- Corrección requerida: componer obligatoriamente un único backend, `Service`, authorities y `AdmissionResolver`; conectar CLI, API/MCP, external-agent y GitHub; hacer fail-closed la ausencia de configuración; implementar los comandos administrativos sobre `Gateway`; eliminar del bootstrap las selecciones project-owned y añadir tests verticales por cada superficie y redelivery.

## REV-2 — Los consumers no usan el snapshot efectivo completo

- Severidad: blocker
- Requirement o regla: R5, R6, R11, R12, R16; T13, T15
- Archivo y línea: `internal/validationworkflow/service.go:305`, `internal/validationworkflow/service.go:452`, `cmd/featureproof/workflow.go:782`, `cmd/featureproof/workflow.go:908`, `cmd/featureproof/workflow.go:1069`, `cmd/featureproof/workflow.go:1217`, `cmd/featureproof/workflow.go:1317`
- Evidencia: el único consumo nuevo del binding comprueba approval y, después de la llamada al modelo, `ToolCalls`/`ModelTokens`. `DurationSeconds` y `Replans` no se aplican; retention no alimenta ningún controller; contracts, provider/target, identities, fixtures, runtime/model/auth profiles, policies y secret refs siguen construyéndose desde `workflowConfig`, manifests y límites/defaults hard-coded. No hay test de planning, execution, evaluation, publication, cleanup, credencial revocada o old-run recovery consumiendo esos campos; `TestBoundConfigurationEnforcesApprovalAndConfirmedUsage` solo prueba una helper de calls/tokens y approval.
- Impacto: aun conectando el resolver, el binding no gobierna el run como exige la spec. Un run puede ejecutar con recursos y budgets distintos de su effective snapshot, y retention/secret-reference semantics quedan sin implementar.
- Corrección requerida: adaptar todos los consumers listados en R16/T15 para construir sus contratos desde el binding, resolver secret refs just-in-time sin fallback y probar cada límite, approval, retention, selección y recovery retirando la garantía correspondiente.

## REV-3 — PostgreSQL devuelve estado durable corrupto sin verificar integridad

- Severidad: blocker
- Requirement o regla: R7, R10, R13, R14
- Archivo y línea: `internal/controlplane/postgres/project_configuration.go:222`, `internal/controlplane/postgres/project_configuration.go:235`, `internal/controlplane/postgres/project_configuration.go:248`, `internal/controlplane/postgres/project_configuration.go:433`, `internal/controlplane/postgres/integration_test.go:284`
- Evidencia: `Version` devuelve `canonical`, schema, digests, actor y effective JSON sin recomputar/validar el canonical digest ni llamar a la validación de effective. `Head`/`scanActivation` tampoco recomputan el activation digest. El test negativo solo altera `run_bindings.binding_digest` y `effective_snapshots.effective_digest`; no altera una version, activation/head ni ambos campos de un effective snapshot. El adapter local sí protege toda la proyección con un checksum, por lo que la contract suite compartida no demuestra equivalencia de corrupción.
- Impacto: una consulta autorizada puede exportar bytes alterados como versión válida y una admisión puede confiar en una activation corrupta. PostgreSQL y local no preservan la misma garantía de integridad/reload.
- Corrección requerida: verificar todas las identidades canónicas/digests al leer versions, activations, effective snapshots y heads; ampliar la contract suite compartida y el gate real con corrupción de cada proyección.

## REV-4 — Counters pueden hacer wrap y no producen `exhausted`

- Severidad: high
- Requirement o regla: R14; T5, T6, T7, T9
- Archivo y línea: `internal/projectconfig/local/store.go:114`, `internal/controlplane/postgres/project_configuration.go:55`, `internal/controlplane/postgres/project_configuration.go:144`, `internal/projectconfig/local/store_test.go:212`
- Evidencia: local comprueba `NextVersion`/`NextGeneration == MaxUint64` solo en publish; rollback hace `NextGeneration + 1` sin fence. PostgreSQL incrementa valores Go leídos de columnas `bigint` sin una categoría `exhausted`; su máximo durable real es `MaxInt64`, y overflow termina como error SQL genérico/unavailable o wrap en Go. El único test de overflow configura `NextVersion = MaxUint64` y llama a Publish; no prueba generation/sequence ni Rollback en ninguno de los adapters.
- Impacto: se incumple la garantía global de monotonicidad sin wrap y la categoría cerrada exigida; rollback puede producir una generación inválida o un error no determinista.
- Corrección requerida: definir el máximo durable común, cercar version/generation/sequence antes de todo incremento y añadir casos compartidos para publish/rollback/event sequence en local y PostgreSQL.

## REV-5 — Falta observabilidad exigida para rechazos, queries y rollback

- Severidad: high
- Requirement o regla: R15; T2, T8, T9, T10
- Archivo y línea: `internal/projectconfig/service.go:191`, `internal/projectconfig/service.go:229`, `internal/projectconfig/service.go:234`, `internal/projectconfig/service.go:255`, `internal/projectconfig/service.go:333`
- Evidencia: `service.observe` se invoca únicamente después de publish correcto. Validate/auth rejection, rollback, active/version/history/export e ingress no emiten telemetry allowlisted. `TestPublishObservesOnlyAfterCommitAndHistoryCursorPaginates` solo demuestra publish post-commit.
- Impacto: no existe evidencia operativa acotada para operaciones y rechazos que R15 exige medir, ni tests que fallarían si esa observabilidad desaparece.
- Corrección requerida: emitir hechos no autoritativos y no sensibles para los resultados requeridos, conservando el journal transaccional como verdad, y probar éxito/fallo/sink failure/canaries en cada frontera.

## REV-6 — Los límites estructurales no cubren todas las colecciones y trabajo del grafo

- Severidad: high
- Requirement o regla: R3, R4, R14; T1, T3
- Archivo y línea: `internal/projectconfig/model.go:16`, `internal/projectconfig/loader.go:38`, `internal/projectconfig/sources.go:12`, `internal/projectconfig/validator.go:102`
- Evidencia: solo existen límites explícitos de bytes y número global de references/descriptors. No hay máximo explícito para capabilities del documento, operations/capabilities/dependencies por descriptor, aristas totales ni trabajo del DFS. `NewSnapshotCatalog` limita descriptors pero copia y recorre slices internas sin budget. Los tests de límites cubren bytes/refs, no esas colecciones o el trabajo total del grafo.
- Impacto: snapshots confiables defectuosos o input grande dentro de 1 MiB pueden forzar trabajo/memoria no acotados respecto de los límites composition-owned exigidos, y R3 carece de un test que falle al retirar esa garantía.
- Corrección requerida: introducir limits composition-owned explícitos y aplicarlos antes de copiar/recorrer todas las colecciones y aristas; añadir tests exactos en límite, límite+1 y grafos adversariales.

## REV-7 — El gate PostgreSQL 16 y la cobertura declarada no son reproducibles

- Severidad: blocker
- Requirement o regla: R18; T7, T16; `docs/verification.md`
- Archivo y línea: `scripts/feature35-postgres-gate.ps1:148`, `scripts/feature36_coverage.py:1`, `progress/project-configuration/implementation.md`
- Evidencia: el comando autorizado requerido `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/feature35-postgres-gate.ps1 -BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage -Feature36Coverage -CCompiler C:\msys64\ucrt64\bin\gcc.exe` falla de forma temprana con `PostgreSQL 16 installation failed with exit code 2`. Por ello no se ejecutaron migrations/contract/concurrency/restart/unknown-commit/binding reales y no se pudo reproducir `1101/1289 >= 85%`. El informe y `progress/current.md` afirman PASS y cleanup completo, lo que no coincide con la ejecución independiente. El cleanup del intento fallido sí fue correcto: `wsl.exe --list --quiet` quedó vacío y no quedaron roots temporales del gate.
- Impacto: R18 es un gate bloqueante y la cifra de cobertura/garantía PostgreSQL carece de evidencia reproducible en el estado revisado.
- Corrección requerida: reparar/pinear de manera reproducible el bootstrap PostgreSQL 16, conservar el checksum de supply chain, ejecutar el comando exacto hasta PASS y adjuntar salida verificable con `1101/1289` (o la nueva fracción exacta >=85%) y cleanup comprobado.

## Trazabilidad R1–R18

| Requirement | Resultado de review | Evidencia |
|---|---|---|
| R1 | Parcial / FAIL productivo | Authority unit tests; ninguna surface productiva compone authority/gateway/resolver (REV-1). |
| R2 | PASS focal | `TestLoadStrictRejectsAmbiguityAndSecrets`, `TestCanonicalizeIsDeterministic`, strict decode/canonical digest. |
| R3 | FAIL | Límites incompletos y sin tests de colecciones/grafo (REV-6). |
| R4 | Parcial | Missing/stale/cycle/provider unit tests; adapters productivos de resources ausentes (REV-1/2). |
| R5 | Parcial | Loader rechaza nombres secret-shaped; no hay resolución JIT/product consumer/revocación (REV-2). |
| R6 | Parcial | Merger monotónico probado; defaults no alimentan consumers productivos (REV-2). |
| R7 | FAIL | Contract básico pasa; lecturas PostgreSQL no verifican varias corrupciones (REV-3). |
| R8 | Parcial | CAS/idempotency/fault tests existen; gate real no ejecuta (REV-7). |
| R9 | Parcial | Rollback funcional probado; overflow y observability faltan (REV-4/5). |
| R10 | FAIL | Cursor/defensive copies focales; PG puede exportar canonical corrupto y no hay surfaces (REV-1/3). |
| R11 | Parcial | Binding seal/store y causal PG focales; no hay composición productiva obligatoria (REV-1/2). |
| R12 | FAIL | `ProjectConfig` opcional y composición productiva nil permiten bypass local/productivo (REV-1). |
| R13 | FAIL | Suite compartida básica; integridad no equivalente y gate PG no ejecuta (REV-3/7). |
| R14 | FAIL | Cancellation/fault tests focales; counters no están totalmente cercados (REV-4/6). |
| R15 | FAIL | Journal durable existe; telemetry requerida está incompleta (REV-5). |
| R16 | FAIL | No implementación ni test vertical de CLI/API/MCP/GitHub/consumers (REV-1/2). |
| R17 | PASS | Gate global de comentarios pasó dentro de `go test -p=1 -count=1 ./...`; comentarios sistémicos observados en el diff. |
| R18 | FAIL | Gate PostgreSQL/coverage falla y no reproduce 1101/1289 (REV-7). |

## Verificación T1–T16

| Tasks | Resultado |
|---|---|
| T1–T4 | Parciales: núcleo/modelo presente, pero T1/T3 no cierran todos los límites de R3. |
| T5–T11 | Parciales: stores/services y tests focales presentes; faltan integridad PG, overflow completo y observability. |
| T12 | Parcial: causal commit PG/local modelado, pero binding sigue opcional y no existe vertical productiva. |
| T13 | FAIL: no resource adapters productivos ni reducción de `workflowConfig`. |
| T14 | FAIL: no adapters CLI/API/MCP/GitHub sobre Gateway/AdmissionResolver. |
| T15 | FAIL: consumers de duration/replans/retention/resources/secret refs no usan el binding. |
| T16 | FAIL: gate PostgreSQL 16/cobertura no reproducible; R1–R18 no quedan trazados con tests suficientes. |

## Gates ejecutados por el reviewer

- `gofmt -l .`: PASS, sin salida.
- `git diff --check`: PASS, solo warnings CRLF de Git.
- `go vet ./...`: PASS.
- `go test -run '^$' ./...`: PASS.
- `go test -p=1 -count=1 ./...`: PASS.
- `go test -p=1 -race -count=1 ./...` con UCRT64 y su directorio en `PATH`: PASS; race focal de projectconfig/validationworkflow/PostgreSQL también PASS.
- Cross-build CGO-disabled: PASS para windows/amd64, linux/amd64, linux/arm64, darwin/amd64 y darwin/arm64.
- Gate PostgreSQL 16 autorizado + race + coverage feature36: **FAIL** en instalación; coverage exacta no reproducida; cleanup PASS.

## Decisión

Se rechaza la feature. Hay requirements sin implementación productiva ni tests que fallen al retirar sus garantías (especialmente R12/R16), tasks T13–T16 marcadas completas sin estarlo y un gate obligatorio fallido.
