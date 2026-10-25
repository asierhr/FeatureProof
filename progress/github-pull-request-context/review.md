# Review de github-pull-request-context

## Revisión 1 — 2026-10-25

**Veredicto: rejected**

La feature 30 es la única `in_progress`, la spec contiene aprobación humana,
T1–T16 están marcadas y existe el informe de implementación. Se inspeccionó el
diff completo desde `4d0a0da`, incluidos los archivos no trackeados. Las suites
existentes pasan, pero probes independientes reproducen seis incumplimientos
del contrato aprobado.

## REV-1 — La composición productiva descarta el terminal record

- Severidad: blocker
- Requirement o regla: R14, ARCH-007, T15
- Archivo y línea: `internal/githubcontext/acquirer.go:101-113`,
  `internal/githubcontext/acquirer.go:730-734`, `cmd/featureproof/workflow.go:674`
- Evidencia: `Acquire` construye un `Record` y llama al observer exactamente una
  vez, pero la composición instala `JSONRecordObserver{}` y su método `Observe`
  se limita a devolver `ctx.Err()`. No serializa, persiste ni publica el record;
  tampoco existe otro journal/observer de adquisición en el diff. Por tanto no
  queda ningún `ContextAcquisitionRecord/v1` tras éxito, stale, insufficient,
  cancelación o error.
- Impacto: la auditoría exigida por R14 y el journal append-only de ARCH-007 no
  existen en producto. Intentos, counts, omission reasons y resultado terminal
  desaparecen al retornar.
- Corrección requerida: componer un observer durable/content-free con
  publicación append-only o CAS/receipt, recuperación e idempotencia exactly-once;
  probar reopen, redelivery, response loss y cada outcome sin payload externo.

## REV-2 — Un Link de paginación de un host externo se acepta como autoridad

- Severidad: blocker
- Requirement o regla: R12
- Archivo y línea: `internal/githubpr/rest.go:255-286`,
  `internal/githubpr/rest.go:105-136`
- Evidencia: `strictNextLink` valida únicamente `https` y host no vacío; no liga
  scheme/host/path/query al endpoint autorizado. Un probe llamó
  `strictNextLink(<https://evil.invalid/steal>; rel="next")` y obtuvo
  `next=true, err=nil`. Aunque la URL no se navega, su presencia controla una
  llamada posterior y el acceptance de R12 exige fallar o declarar partial ante
  un Link externo.
- Impacto: metadata no autorizada del provider puede ampliar la paginación y el
  consumo de tokens/requests en vez de fallar cerrado.
- Corrección requerida: validar el Link contra el origen y la ruta/query local
  esperados, o ignorar completamente su target usando una señal autenticada que
  no acepte hosts externos. Añadir matrices external/cross-repo/page repetida.

## REV-3 — El truncado por bytes puede convertir media constraint en constraint válida

- Severidad: blocker
- Requirement o regla: R5, R6
- Archivo y línea: `internal/githubcontext/acquirer.go:247-253`,
  `internal/githubcontext/acquirer.go:467-510`,
  `internal/githubcontext/acquirer.go:619-628`
- Evidencia: el body se corta al mayor prefijo UTF-8 y después se entrega al
  extractor Markdown. Un probe truncó
  `- R1: complete-description` cinco bytes antes del final; la adquisición
  publicó una constraint con descripción `complete-descrip`. No hubo error ni
  omisión de la unidad estructural.
- Impacto: el contexto puede contener una obligación distinta/incompleta a la
  escrita por el autor, contradiciendo el packing por unidades completas y la
  extracción exacta.
- Corrección requerida: identificar primero la sección/referencia dentro de un
  input bounded y aceptar constraints completas solamente; si el límite corta
  la sección o una línea, omitir/fallar como insufficient con motivo tipado.

## REV-4 — La policy sellada admite catálogos ambiguos y allowlists no canónicas

- Severidad: blocker
- Requirement o regla: R3, R5, T2, T7
- Archivo y línea: `internal/githubcontext/model.go:370-405`,
  `internal/githubcontext/model.go:478-492`,
  `internal/githubcontext/acquirer.go:511-535`
- Evidencia: `SealPolicy` ordena `Catalog` pero no valida alias/version únicos ni
  IDs/descripciones/safe references de sus constraints. Un probe selló dos
  entradas `same@v1` diferentes sin error; el extractor selecciona la última en
  orden en vez de rechazar autoridad ambigua. Otro probe confirmó que una
  extensión `go` sin punto se acepta y después coincide por suffix con paths no
  equivalentes.
- Impacto: una policy aparentemente sellada no define una interpretación única
  ni una allowlist exacta. El resultado depende de orden/strings permisivos.
- Corrección requerida: validar completamente extractor, extensiones, catálogo y
  constraints; rechazar identidades duplicadas/ambiguas y añadir pruebas de
  permutations, duplicados y campos inválidos.

## REV-5 — Los verificadores aceptan snapshots semánticamente inválidos si el digest se recalcula

- Severidad: blocker
- Requirement o regla: R3, R10, T1, T11
- Archivo y línea: `internal/githubcontext/model.go:407-442`
- Evidencia: `VerifyContext` y `VerifyBinding` solo comparan SHA-256; no validan
  version, enums, OIDs, paths, counts, orden, duplicados ni relaciones. Un probe
  creó un `Context` con `Completeness("evil")` y repository ID negativo,
  recalculó su digest y tanto `VerifyContext` como `DecodeContext` lo aceptaron.
- Impacto: corrupción coordinada o bytes locales manipulados pueden crear un
  objeto self-signed que atraviesa `LocalStore`, projection y revalidation. El
  decode no es el contrato estricto descrito por R3.
- Corrección requerida: implementar validación semántica completa de Context y
  Binding antes/durante digest verification y al cargar; probar todos los enums,
  relaciones, orden, duplicados y referencias con digest recalculado.

## REV-6 — Cancelación y deadline se reclasifican en fronteras productivas

- Severidad: blocker
- Requirement o regla: R13, R14, ARCH-006, CONV-005
- Archivo y línea: `internal/githubcontext/acquirer.go:127-143`,
  `internal/githubpr/rest.go:137-174`
- Evidencia: cualquier error de `Store.Load/Commit` se envuelve como
  `CategoryPersistence` sin volver a consultar el contexto. Un probe hizo que
  `Load` cancelara el caller con un sentinel y la salida fue `persistence at
  load`, no `cancelled`, aunque `errors.Is` conservó la causa. Además,
  `mapContextError` asigna siempre `CategoryCancelled` cuando `ctx.Err()!=nil`,
  incluso para `DeadlineExceeded`.
- Impacto: las cuatro superficies no pueden aplicar política distinta a una
  cancelación solicitada, un timeout y un fallo durable; telemetría y retry
  reciben una categoría incorrecta.
- Corrección requerida: usar `context.Cause`/`ctx.Err` antes de envolver cada
  frontera y distinguir timeout de cancelación; añadir probes durante Load,
  Commit, token, HTTP y paginación.

## Verificación independiente

- precondiciones SDD, estado único, aprobación y T1–T16: pass.
- `git diff --check`: pass.
- `gofmt -l` sobre los 19 Go nuevos/modificados: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- `go test -p 1 ./... -count=1 -timeout 10m`: pass completo.
- cuatro paquetes afectados `-count=10`: pass.
- `go test -race -p 1 ./... -count=1 -timeout 15m` con UCRT64: pass completo.
- cross-build de los cuatro paquetes en Windows/Linux/Darwin amd64: 12/12 pass.
- los cuatro binarios Linux fueron ejecutados 4/4 por el implementer en Alpine
  WSL2; el reintento independiente posterior no pudo arrancar la VM por
  `ERROR_NO_SYSTEM_RESOURCES`, una limitación del host y no la causa del rechazo.
- coverage fresca documentada Windows+Linux: `656/770 = 85,194805 %`, por encima
  del 85 % sin redondear. Los blockers son funcionales y no se compensan con
  coverage.
- probes overlay externos al repositorio reproducen REV-2..REV-6. No quedaron
  archivos de probe dentro del worktree.

No se observó implementación de webhooks/checks/comentarios, checkout,
environment provider ni gestión de configuración fuera del alcance. El rechazo
se limita a requirements aprobados de la feature 30.

## Revisión 2 — 2026-10-25

**Veredicto: rejected**

Se revisó de nuevo el diff completo desde `4d0a0da`, incluidos todos los
archivos trackeados y no trackeados. Las correcciones de REV-2 a REV-6 están
presentes: el Link queda ligado a origen/ruta/repo/PR/página, ninguna constraint
sale de un body truncado, policy/catálogo/extractor son estrictos, los objetos
durables rechazan JSON ambiguo y relaciones semánticas inválidas, y las
fronteras de store/token/HTTP/paginación preservan `context.Cause` y distinguen
cancelación de timeout. REV-1, sin embargo, sigue incumpliendo R14 y dos probes
adversarios adicionales encuentran incumplimientos de R6/R7.

## REV-1 — El record durable no cubre entradas inválidas ni converge tras fallo→éxito

- Severidad: blocker
- Requirement o regla: R13, R14, T15
- Archivo y línea: `internal/githubcontext/acquirer.go:98-115`,
  `internal/githubcontext/observer.go:33-59`,
  `internal/githubcontext/observer.go:85-110`
- Evidencia: `Acquire` deriva el ID de la solicitud y construye el record con
  sus campos crudos antes de validarla; después ignora incondicionalmente el
  error de `Observer.Observe`. Un probe con `RunID="invalid run id"` recibió
  correctamente `CategoryInvalid`, pero `VerifyRecord` rechazó el propio record
  y no quedó ningún archivo durable. Otro probe ejecutó la misma identidad
  primero con resultado `stale` y después con resultado exitoso: la segunda
  adquisición devolvió éxito, pero `Observe` aceptó como redelivery cualquier
  objeto existente con el mismo ID y el único record siguió diciendo `stale`.
- Impacto: no existe un terminal record para toda clase de fallo y un resultado
  exitoso puede quedar auditado permanentemente como un fallo anterior. La
  publicación es durable y content-free cuando acepta el record, pero no
  satisface el contrato exactly-once por adquisición/redelivery.
- Corrección requerida: separar una identidad de intento/terminal válida de los
  campos no validados, no descartar fallos que impidan confirmar el record y
  definir convergencia idempotente que no permita que un fallo previo envenene
  una redelivery exitosa; probar invalid input y transición fallo→éxito además
  de response-loss/reopen.

## REV-7 — El parser acepta hunks que no son unified diff completos

- Severidad: blocker
- Requirement o regla: R6, R7, R15, T6
- Archivo y línea: `internal/githubcontext/acquirer.go:482-504`,
  `internal/githubcontext/model.go:639-659`
- Evidencia: `parsePatch` solo exige que el header empiece por `@@ ` y que las
  líneas empiecen por `+`, `-` o espacio. Un probe obtuvo éxito para
  `@@ forged header\n+line`; también falta validar los rangos old/new y que la
  cantidad de líneas complete el hunk declarado, por ejemplo
  `@@ -1,3 +1,3 @@\n-old\n+new`.
- Impacto: contenido remoto hostil puede cruzar como un `Hunk` y un Context
  self-signed semánticamente válido aunque no sea un fragmento unified-diff
  completo, rompiendo las fronteras semánticas de truncado y patch válido.
- Corrección requerida: parsear la gramática completa `@@ -a[,n] +b[,m] @@`,
  contabilizar contexto/adiciones/eliminaciones contra ambos rangos y rechazar
  headers, rangos o unidades incompletas; añadir probes adversarios.

## REV-8 — La gramática de paths admite devices y caracteres de control

- Severidad: blocker
- Requirement o regla: R7, R15, T5
- Archivo y línea: `internal/githubcontext/model.go:774-776`,
  `internal/githubpr/rest.go:267-270`
- Evidencia: las dos validaciones se limitan a `path.Clean`, traversal,
  backslash y colon. Un probe confirmó que `validPath("src/CON.go")` devuelve
  true; la misma gramática también acepta segmentos Windows reservados como
  `NUL.yaml` y no contiene rechazo explícito de NUL/control characters.
- Impacto: el contrato aprobado exige rechazar `filename ... device`; esos
  paths hostiles pueden entrar en metadata, digests, projection y contexto.
- Corrección requerida: compartir una gramática canónica que rechace controles,
  NUL, segmentos vacíos/trailing y nombres device reservados en cualquier
  segmento, con matriz equivalente en adapter y dominio.

## Verificación independiente de la revisión 2

- precondiciones SDD, aprobación humana, única feature `in_progress`, T1–T16
  marcadas e informe de implementación: pass documental; T5/T6/T15 no están
  materialmente completas por los blockers anteriores.
- `gofmt -d` sobre todos los Go trackeados y no trackeados, `git diff --check`,
  `go vet ./...` y `go test ./... -run '^$'`: pass.
- cuatro paquetes afectados normales `-count=1` y `-count=3`: pass.
- suite global serial `go test -p 1 ./... -count=1`: pass.
- race focal y `go test -race -p 1 ./...` con UCRT64: pass.
- cross-build/test-compile Windows/Linux/Darwin amd64 para los cuatro paquetes:
  12/12 pass.
- los cuatro binarios Linux quedaron compilados; dos intentos independientes de
  ejecutarlos en `FeatureProof-Test` no pudieron crear la VM por
  `WSL/.../ERROR_NO_SYSTEM_RESOURCES`. El implementer documenta 4/4 y el fallo
  del host no es la causa del rechazo.
- coverage fresca Windows combinando por coordenada el máximo de cuatro profiles
  con `-coverpkg`: `908/1067 = 85,098407 %`. `sync_unix.go` compila y la evidencia
  Linux de implementación registra `4/5`; el reintento fresco del tag Unix quedó
  impedido por el mismo fallo WSL. El umbral no es la causa del rechazo.
- probes overlay externos reprodujeron REV-1, REV-7 y REV-8; no modificaron el
  worktree. Las suites usan fakes/`httptest`; no se observó acceso real a GitHub,
  credenciales reales, modelo ni red externa.
- auditoría de comentarios y alcance: las entidades/helpers nuevos de feature 30
  tienen comentario sistémico. No se añadieron webhooks, checks/comentarios,
  checkout, environment provider ni config manager. No quedaron artefactos de
  build/coverage dentro del repositorio.

R1–R5 y R8–R13 quedan respaldados por implementación y suites existentes; R6,
R7 y R14 fallan por REV-7, REV-8 y REV-1 respectivamente, y R15 falla en
trazabilidad adversaria mientras esas ramas no estén cubiertas/corregidas.

## Revisión 3 — 2026-10-25

**Veredicto: rejected**

Se volvió a inspeccionar el diff completo desde `4d0a0da`, incluidos los
archivos no trackeados. Las remediaciones cubren correctamente input inválido,
response-loss/reopen, redelivery estable, transición stale→success, gramática y
counts básicos de cada hunk, overflow, Context self-signed y devices DOS/path
traversal. REV-2–REV-6 permanecen cerrados. Cuatro probes independientes
encuentran, no obstante, requisitos todavía incumplidos.

## REV-9 — Un fallo del observer borra el binding ya confirmado

- Severidad: blocker
- Requirement o regla: R14, acceptance «fallo/cancelación del observer no
  cambia el binding», T15
- Archivo y línea: `internal/githubcontext/acquirer.go:120-131`
- Evidencia: el deferred observer hace visible su fallo como
  `CategoryPersistence`, pero asigna `Context{}` y `Binding{}` a los resultados
  nombrados. Un probe dejó que gateway y store confirmasen una adquisición
  válida y usó un observer que devolvió un sentinel: `errors.Is` preservó el
  sentinel, pero tanto context digest como binding digest retornaron vacíos.
- Impacto: una dependencia de observabilidad modifica el resultado autoritativo
  ya adquirido/persistido. El caller no puede conservar o verificar el binding
  aunque R14 exige que el observer no lo cambie.
- Corrección requerida: propagar el fallo de confirmación sin mutar Context ni
  Binding ni repetir gateway/model/tools; probar éxito confirmado + observer
  error/cancel/timeout y posterior redelivery.

## REV-10 — Attempts y duration no están cubiertos por ninguna identidad verificable

- Severidad: blocker
- Requirement o regla: R3, R14, exigencia de identidad/digest recomputable, T15
- Archivo y línea: `internal/githubcontext/acquirer.go:194-234`,
  `internal/githubcontext/observer.go:84-110`
- Evidencia: `terminalRecordID` incluye counts/outcome/digests, pero excluye
  `Attempts` y `Duration`, aunque ambos son hechos obligatorios del record.
  `VerifyRecord` solo comprueba que sean no negativos. Un probe publicó un
  record real, reescribió su JSON cambiando attempts en +99 y duration en +1h,
  y `LocalRecordObserver.Read` lo aceptó bajo el mismo ID.
- Impacto: corrupción local coordinada puede alterar dos hechos auditables sin
  invalidar el supuesto digest recomputable del record. Excluirlos del address
  estabiliza redelivery, pero deja el contenido durable parcialmente unsigned.
- Corrección requerida: separar un key estable de exactly-once de un digest de
  contenido que cubra todos los campos, o definir una representación canónica
  completa verificable; validar ambos en publish/read/redelivery.

## REV-11 — Hunks múltiples solapados, duplicados o en retroceso se aceptan

- Severidad: blocker
- Requirement o regla: R3, R6, R7, T6
- Archivo y línea: `internal/githubcontext/acquirer.go:542-659`
- Evidencia: el parser valida cada header/count de forma aislada, pero
  `parseHunkHeader` descarta los starts y nunca compara un hunk con el anterior.
  Probes con rangos `-10/+10` seguidos por `-1/+1`, y con dos hunks idénticos
  `-1/+1`, fueron aceptados. `validHunk` repite únicamente la validación local,
  por lo que un Context self-signed con la misma secuencia también puede pasar.
- Impacto: una secuencia que no constituye una proyección unified-diff canónica
  puede cruzar como patch válido; duplicados/overlap son autoridad ambigua y
  alteran counts/ranking del contexto.
- Corrección requerida: conservar start/end old/new y exigir orden monótono sin
  overlap ni duplicados entre hunks; repetir la relación al verificar Context y
  añadir matrices de inserción/eliminación zero-count.

## REV-12 — La gramática compartida acepta paths con UTF-8 inválido

- Severidad: blocker
- Requirement o regla: R3, R7 y caso límite explícito de UTF-8 inválido, T5
- Archivo y línea: `internal/githubcontext/model.go:768-792`,
  `internal/githubpr/rest.go:266-268`
- Evidencia: `CanonicalRepositoryPath` itera runes para controles, pero no llama
  a `utf8.ValidString`; los bytes inválidos se convierten en `RuneError` durante
  el range y no se rechazan. Un probe confirmó que `src/\xff.go` se devuelve
  como path válido. Adapter y dominio comparten esta misma función.
- Impacto: el caso límite que la spec obliga a fallar puede entrar en DTOs de
  gateway, digests y `untrusted_data`; además `encoding/json` puede sustituir
  bytes inválidos antes de las validaciones posteriores.
- Corrección requerida: rechazar bytes no UTF-8 antes de canonicalizar y antes
  de decodificar respuestas remotas, con pruebas de path/title/body/patch raw.

## Verificación independiente de la revisión 3

- SDD/aprobación/estado único/informe: pass; T1–T16 figuran `[x]`, aunque
  T5/T6/T15 no están materialmente completas por REV-9–REV-12.
- `gofmt -d` trackeado+no trackeado, `git diff --check`, `go vet ./...` y
  compile-only global: pass.
- cuatro paquetes afectados normales y race: pass; suite global serial normal:
  pass.
- la primera race global reprodujo un único fallo no determinista preexistente
  en `TestProductIdentityFailureParityAcrossAllSurfaces/mcp`; el rerun dirigido
  con race pasó en sus cuatro superficies y un segundo gate global race pasó
  completo. No se observó data race.
- cross-build/test-compile Windows/Linux/Darwin amd64: 12/12 pass.
- runtime Linux: la lógica de los cuatro binarios pasa en `FeatureProof-Test`;
  para los cross-binaries se omitió únicamente el audit test que contiene paths
  fuente Windows embebidos, y `cmd/featureproof` se ejecutó desde su cwd de repo.
  El implementer documenta además 4/4 mediante compilación Linux nativa.
- coverage fresca combinando cuatro profiles Windows y uno Linux real por
  coordenada y máximo count: `992/1165 = 85,150215 %`; `sync_unix.go` queda 4/5
  y `sync_windows.go` 1/1. El umbral pasa y no causa el rechazo.
- probes overlay fuera del worktree reproducen REV-9–REV-12. No quedaron
  temporales de build/coverage dentro del repositorio.
- comentarios sistémicos: pass para símbolos de feature 30. Scope: no se
  añadieron webhooks, checks/comentarios, checkout, environment provider ni
  config manager. Tests y auditoría muestran fakes/`httptest`; no hubo acceso
  real a GitHub, credenciales, modelo ni red externa.

R1–R5 y R8–R13 mantienen trazabilidad suficiente. R6/R7 fallan por REV-11/12,
R14 por REV-9/10 y R15 mientras estos casos adversarios no estén corregidos y
cubiertos.

## Revisión 4 — 2026-10-25

**Veredicto: approved**

Se inspeccionó otra vez el diff completo desde `4d0a0da`, incluidos todos los
archivos no trackeados. No quedan blockers reproducibles. REV-1–REV-12 están
cerrados y R1–R15 mantienen trazabilidad efectiva a implementación y tests.

## Cierre adversario de REV-9–REV-12

- REV-9: `Acquire` conserva Context y Binding ya confirmados cuando el observer
  devuelve error, cancelación o timeout; el error tipado y su cause siguen
  visibles. La redelivery carga el receipt y confirma el record sin nuevas
  llamadas al gateway.
- REV-10: `Record.ID` actúa como key estable exactly-once y `Record.Digest`
  sella por separado la representación completa, incluidos `Attempts` y
  `Duration`. Publish y Read verifican ambos; los probes de tamper fallan
  cerrado y una redelivery con duración distinta no reemplaza el primer record.
- REV-11: cada `Hunk` conserva coordenadas old/new half-open derivadas de un
  header canónico. Parser y `VerifyContext` comparten validación de counts,
  overflow, orden monotónico, overlap, duplicados, retroceso y puntos
  zero-count; los probes multi-hunk y self-signed son rechazados.
- REV-12: `CanonicalRepositoryPath` comprueba UTF-8 antes de `path.Clean`, y el
  adapter rechaza la respuesta HTTP completa si sus bytes no son UTF-8 antes de
  invocar JSON decoder. Title, body, filename y patch raw están cubiertos.

También se revalidaron las correcciones anteriores: autoridad exclusiva de
composición, metadata/files/metadata y drift, límites/omisiones exactos,
extractores y catálogo versionados, OpenAPI sin patch, Link ligado al endpoint,
constraints nunca obtenidas de body truncado, verificación semántica de
Context/Binding/JSON, cause de cancel/timeout, store/recovery/concurrencia,
integración real con `ContextAssembler` y checkpoints de plan/approval/
execution/recovery. No se encontró payload paralelo que pierda trust ni I/O bajo
los locks del nuevo store.

## Verificación independiente de la revisión 4

- aprobación SDD, única feature `in_progress`, informe de implementación y
  T1–T16 `[x]`: pass.
- `gofmt -d` para Go trackeado+no trackeado, `git diff --check`, `go vet ./...`
  y compile-only global: pass.
- cuatro paquetes afectados `-count=3 -p=1`: pass.
- suite global normal serial `-p=1 -count=1`: pass.
- race focal de los cuatro paquetes: pass; race global serial UCRT64: pass
  completo, sin data races.
- test-compile Windows/Linux/Darwin amd64 para cuatro paquetes: 12/12 pass.
- runtime Linux real en WSL2 `FeatureProof-Test`: 4/4 binarios pasan desde cwd
  compatibles. En los cross-binaries se omitió solo el audit de comentarios que
  contiene paths fuente Windows embebidos; ese audit pasa en Windows y el
  implementer documenta además el gate Linux nativo completo.
- coverage fresca: cuatro profiles Windows más un profile Linux ejecutado,
  unidos por coordenada y máximo count e intersectados con el diff productivo:
  `1012/1186 = 85,328836 %`. Incluye `sync_unix.go` 4/5 y
  `sync_windows.go` 1/1; supera 85 % sin redondear.
- probes overlay externos de las revisiones 2–4 pasan contra el código actual.
  No se dejaron temporales de build/coverage dentro del worktree.
- comentarios sistémicos de símbolos productivos y helpers de test: pass.
- tests usan fakes, transports locales y `httptest`; no hubo acceso real a
  GitHub, credenciales, modelo ni red externa.
- scope: no se añadieron webhooks, publicación de checks/comentarios, checkout,
  environments ni config manager de features posteriores.

La implementación satisface el contrato aprobado y puede pasar a `done` por el
leader.
