# Review de Feature 31 — environment-provider

## Veredicto

**rejected**

La revisión independiente se realizó sobre el diff completo desde la aprobación
`6e8742f`, incluidos archivos sin seguimiento. La feature cumple las
precondiciones formales (es la única `in_progress`, la spec está aprobada, T1–T16
figuran `[x]` y existe informe de implementación), pero mantiene incumplimientos
funcionales y gates obligatorios fallidos.

## Hallazgos

## REV-1 — La composición introduce una selección por defecto

- Severidad: blocker
- Requirement o regla: R1, T1, T12
- Archivo y línea: `cmd/featureproof/workflow.go:1299-1305`
- Evidencia: si el target no trae `ProviderID`, composición asigna
  `existing-url`; también fija `ProviderVersion: "v1"`, `KindExistingURL`,
  `TemplateReference` igual al alias, `Secretless: true` y capability `http`.
  No consume un catálogo explícito y versionado proporcionado por configuración.
- Impacto: un target directo sin provider explícito obtiene provider, versión,
  template y capacidades por fallback. Contradice el criterio expreso de R1:
  no debe existir provider ni template por defecto.
- Corrección requerida: cargar y sellar una selección explícita
  composition-owned; rechazar cualquier alias sin provider/versión/kind/template
  completos y eliminar los defaults productivos.

## REV-2 — Una adquisición incierta puede quedar sin release intent

- Severidad: blocker
- Requirement o regla: R8, R9, ARCH-005, ARCH-006, ARCH-007, CONV-004
- Archivo y línea: `internal/environment/service.go:187-215`,
  `internal/environment/service.go:363-404`,
  `internal/environment/service.go:407-424`
- Evidencia: antes del I/O se persiste `StateAcquiring` con
  `ReleaseRequired=false`. Si `Acquire` produce un recurso pero pierde la
  respuesta, vence o se cancela, `acquireObservation` retorna directamente sin
  persistir `release_intent` y sin invocar cleanup. El test de cancelación
  (`environment_test.go:352-359`) solo comprueba categoría/cause y no inspecciona
  el record ni la liberación. Cuando sí se ejecuta cleanup, `cleanupDetached`
  reemplaza el contexto recibido por `context.Background()` en lugar de derivar
  el contexto independiente desde el owner de cleanup. Además, `ReleaseKey` se
  crea antes de conocer el lifecycle (`service.go:163-164`) y no queda ligado al
  lifecycle digest como exige el diseño.
- Impacto: una cancelación/timeout o response loss puede dejar una reserva real
  huérfana y durablemente marcada solo como `acquiring`; recovery no sabe que
  existe obligación de liberar. Se rompe la garantía central de cleanup
  recuperable y la propagación de contexto.
- Corrección requerida: persistir obligación de release antes o como parte del
  boundary incierto de acquire, reconciliar siempre con la misma identidad,
  derivar cleanup mediante `context.WithoutCancel` exclusivamente en su owner
  con timeout finito, y cubrir cancelación/timeout/response loss comprobando el
  estado durable y la liberación.

## REV-3 — El store durable no garantiza relaciones, rollback ni publicación atómica

- Severidad: blocker
- Requirement o regla: R3, R6, R8, R12, T6, ARCH-007
- Archivo y línea: `internal/environment/store.go:257-309`,
  `internal/environment/store.go:311-344`,
  `internal/environment/store.go:346-409`
- Evidencia:
  - `validateRecord` solo valida target/binding cuando `Target.Version` no está
    vacío; acepta un record auto-firmado `StateBound` sin target, binding,
    environment ni lifecycle, y no valida relaciones específicas por estado.
  - `versions` ordena los nombres presentes y `Load` toma el mayor; no exige una
    secuencia contigua ni conserva un witness, por lo que borrar la última
    revisión produce rollback silencioso a la anterior.
  - el record se publica con `os.Link` sin sincronizar el directorio; el evento
    se crea directamente en su ruta canónica y se escribe después. Un crash o
    lector concurrente puede observar un evento vacío/parcial que bloquea para
    siempre el mismo ID. Tampoco se sincroniza el directorio de eventos.
  - si dos writers convergen en el mismo record, el perdedor puede devolver el
    winner en `Save` sin asegurar que el evento del winner haya sido publicado.
- Impacto: recovery puede aceptar estados imposibles o revertidos y una
  transición confirmada puede carecer de su evento exactly-once. Los tests solo
  sobrescriben un receipt con JSON inválido y prueban redelivery nominal; no
  cubren self-signed relations, huecos/rollback ni fallos entre publicación del
  record y del evento.
- Corrección requerida: validar la máquina y todas sus relaciones por estado,
  añadir detección durable de secuencia/rollback, publicar records y eventos con
  staging + sync + publicación atómica + sync del directorio, y añadir fault
  injection/crash tests para cada boundary.

## REV-4 — No hay ownership durable para redeliveries concurrentes

- Severidad: blocker
- Requirement o regla: R8, R9, T5, T7, CONV-005
- Archivo y línea: `internal/environment/service.go:101-115`,
  `internal/environment/service.go:165-222`,
  `internal/environment/store.go:183-215`
- Evidencia: `Record` declara `Owner` y `LeaseUntil`, pero el servicio/store no
  los adquiere, renueva, verifica ni adopta. Dos llamadas concurrentes del mismo
  run pueden converger en el mismo `StateAcquiring` y ambas llamar al provider
  con la misma key. La prueba concurrente (`environment_test.go:361-376`) usa
  ocho run IDs distintos; no prueba contención/redelivery del mismo run ni crash
  entre owners.
- Impacto: no se cumple el modelo aprobado de un owner/lease por mutación; la
  no duplicación queda delegada al provider y CAS puede devolver conflictos en
  vez de converger. La recuperación multi-owner solicitada por R8 no está
  demostrada.
- Corrección requerida: implementar ownership/lease y adopción con CAS, impedir
  I/O provider simultáneo para la misma adquisición y probar same-run,
  different-run, lease viva/expirada y crash boundaries bajo race.

## REV-5 — Falta revalidación inmediatamente anterior a publicación

- Severidad: blocker
- Requirement o regla: R7, T8, T13
- Archivo y línea: `internal/validationworkflow/service.go:736-760`,
  `internal/validationworkflow/service.go:944-976`,
  `internal/validationworkflow/service.go:1041-1124`
- Evidencia: `restorePrepared` revalida el entorno antes de ejecución/recovery,
  pero `terminal` finaliza/publica packages y records sin volver a consultar
  `Environment.Revalidate`. Entre la ejecución y la publicación pueden cambiar
  attestation, endpoint, capabilities o network policy. No existe el test
  trazado `TestEnvironmentDriftStopsExecutionRecoveryAndPublication`; no hay
  ningún test nuevo de drift en publication.
- Impacto: evidencia o resultado puede publicarse para una autoridad de entorno
  ya stale, incumpliendo el checkpoint explícito de R7.
- Corrección requerida: revalidar con autoridad composition-owned justo antes
  de cada publicación/efecto terminal aplicable, conservar cleanup pendiente
  ante drift y probar cada dimensión sellada en ejecución, recovery y
  publication.

## REV-6 — Los fallos operativos no conservan la semántica requerida

- Severidad: blocker
- Requirement o regla: R11, T14
- Archivo y línea: `internal/validationworkflow/service.go:148-155`,
  `internal/validationworkflow/service.go:1048-1124`,
  `internal/validationworkflow/service.go:1211-1213`
- Evidencia: cualquier error de `Environment.Acquire`, incluida cancelación o
  deadline, se convierte siempre en `OutcomeInconclusive`; no se proyecta
  `cancelled` ni se conserva su cause. Si release falla después de obtener un
  outcome determinista, `DesiredOutcome` queda privado mientras `viewOf` solo
  expone `record.Outcome`, que permanece `nil` durante cleanup pendiente.
- Impacto: las superficies no distinguen correctamente cancelled/timeout y no
  pueden mostrar el veredicto primario junto al estado operacional de cleanup,
  como exige R11.
- Corrección requerida: mapear categorías operativas de forma cerrada,
  conservar cause internamente, y proyectar por separado outcome/veredicto
  primario y `cleanup_pending` sin sobrescribir ni ocultar el primero.

## REV-7 — La suite contractual común no cubre el contrato declarado

- Severidad: blocker
- Requirement o regla: R2, R5, R14, T3, T10, T15
- Archivo y línea: `internal/environment/environment_test.go:247-288`
- Evidencia: `TestEnvironmentProviderContractMatrix` ejecuta para ambos
  providers únicamente acquire nominal, restore nominal y release dos veces.
  Pending/readiness, unavailable/unhealthy, drift, cancelación/deadline,
  response loss/reconcile, redelivery, recovery y concurrencia están fuera de
  la tabla común y se prueban, cuando existen, solo contra `managedFake`.
  `ExistingURLProvider.Reconcile` y `Observe` tampoco verifican
  `KindExistingURL` (`internal/environment/existing.go:50-67`).
- Impacto: no existe evidencia de que ExistingURLProvider y managed fake
  satisfagan las mismas reglas; un adapter existing puede operar durante
  reconcile/observe con una selección de kind incorrecto.
- Corrección requerida: convertir todos los escenarios enumerados por R14 en
  una suite/factory realmente idéntica para ambos providers y cerrar kind,
  identidad, cancelación y recovery en cada método.

## REV-8 — No existe prueba vertical de lifecycle común en las cuatro superficies

- Severidad: blocker
- Requirement o regla: R13, T12, T13
- Archivo y línea: `internal/validationworkflow/workflow_test.go:1610-1628`,
  `cmd/featureproof/main_test.go:354-393`,
  `cmd/featureproof/main_test.go:1024-1049`
- Evidencia: las pruebas heredadas de paridad verifican status/result o entradas
  aisladas; no ejecutan CLI, API, MCP y GitHub contra el mismo
  `EnvironmentPort` ni comparan target/binding digest y transiciones. No existe
  el test trazado `TestFourSurfacesShareOneEnvironmentLifecycleAndDigest`.
- Impacto: aunque el workflow ya declara un único `EnvironmentPort`, no queda
  demostrada la aceptación observable de R13 ni que ninguna superficie omita o
  duplique acquire/release.
- Corrección requerida: añadir un vertical hermético de las cuatro superficies
  con un spy común, misma selección/autoridad, digests/transiciones iguales y
  conteo exactly-once de acquire/release; incluir direct y preview.

## REV-9 — Coverage exacta del diff inferior al mínimo

- Severidad: blocker
- Requirement o regla: R15, T16, `docs/verification.md` §4.5
- Archivo y línea: diff productivo completo desde `6e8742f`
- Evidencia: medición independiente por statement blocks, excluyendo únicamente
  `_test.go` y contabilizando `model.go` como declaración sin statements:
  **707/839 = 84.266985 %**. Desglose:
  - `cmd/featureproof/workflow.go`: 37/62
  - `internal/environment/existing.go`: 26/27
  - `internal/environment/model.go`: 158/169
  - `internal/environment/service.go`: 210/239
  - `internal/environment/store.go`: 216/267
  - `internal/validationworkflow/environment.go`: 29/41
  - `internal/validationworkflow/service.go`: 31/34
- Impacto: no alcanza el 85 % sin redondear. El 86.647727 % del informe mide
  solo el paquete nuevo y omite cambios productivos de composición/workflow.
- Corrección requerida: añadir tests conductuales para ramas relevantes del
  diff completo y repetir la medición exacta, incluidas variantes afectadas por
  build tags/plataforma.

## REV-10 — El gate runtime Linux completo falla

- Severidad: blocker
- Requirement o regla: R15, T16, `docs/verification.md` §4
- Archivo y línea: suites de `internal/validationworkflow` y
  `cmd/featureproof`
- Evidencia: se verificó el SHA-256 del Alpine 3.22.2 minirootfs informado, se
  importó un runner WSL1 efímero y se ejecutaron binarios Linux recién
  compilados. `internal/environment` pasó, pero:
  - `internal/validationworkflow` falló
    `TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage` en publication;
  - `cmd/featureproof` falló
    `TestContractReviewCLICompositionCreatesDurableReview`,
    `TestRunDispatchesHermeticAgentEvaluation` y
    `TestConfiguredPreviewRunsThroughExternalCLIEntrypoint`.
  Los nueve cross-builds Windows/Linux/Darwin amd64 sí pasaron.
- Impacto: cross-build no sustituye ejecución; el gate Linux obligatorio no
  está verde. Que los fallos se atribuyan a semántica WSL1 o tests históricos
  no constituye una excepción aprobada.
- Corrección requerida: hacer reproducible una suite Linux completa verde en
  un runtime soportado o documentar y obtener previamente una excepción humana
  explícita al gate; no marcar R15/T16 como cumplidos mientras falle.

## Trazabilidad revisada

| Requirement | Evidencia localizada | Resultado |
|---|---|---|
| R1 | `TestEnvironmentSelectionComesOnlyFromTrustedCatalog` + composición | fail: defaults productivos |
| R2 | interface + contract matrix | fail: contrato común incompleto |
| R3 | `TestValidationTargetIsCanonicalDetachedAndStrict` | parcial: store acepta relaciones imposibles |
| R4 | `TestEnvironmentBecomesVisibleOnlyAfterReadyAttestedAndAuthorized` | parcial |
| R5 | tests de adapter/delegación | parcial: suite común y kind incompletos |
| R6 | opacidad y canarios | parcial: validación durable débil |
| R7 | restore/revalidate workflow | fail: sin checkpoint de publicación |
| R8 | release uncertainty/response loss | fail: acquire cancelado sin intent; sin ownership |
| R9 | cancel/retry/concurrency | fail: cleanup context y same-run no cubiertos |
| R10 | secretless/canary scans | pass para el único adapter compuesto |
| R11 | operational failure tests | fail: mapping/proyección incorrectos |
| R12 | local store/events/observer tests | fail: atomicidad, rollback y relaciones |
| R13 | port único + tests de superficies heredados | fail: falta vertical común |
| R14 | `TestEnvironmentProviderContractMatrix` | fail: solo nominal |
| R15 | gates | fail: coverage y Linux |

## Gates independientes

| Gate | Resultado |
|---|---|
| `gofmt -d` sobre Go del diff | pass |
| `git diff --check 6e8742f` | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| afectados `environment`, `validationworkflow`, `cmd/featureproof` x3 | pass |
| race afectados | pass |
| `go test -race -count=20 ./internal/environment` | pass |
| `go test -race -count=1 -p=1 ./...` con UCRT64 | pass completo |
| cross-build 3 paquetes × Windows/Linux/Darwin amd64 | pass 9/9 |
| runtime Linux `internal/environment` | pass |
| runtime Linux `internal/validationworkflow` + `cmd/featureproof` | fail |
| coverage statements del diff productivo | fail: 707/839 = 84.266985 % |

No se observó acceso a infraestructura real, red externa ni providers reales en
las suites. El managed fake permanece en tests y no se compone en producto. Los
comentarios sistémicos están presentes en los símbolos nuevos inspeccionados.
El runner WSL de revisión fue desregistrado y sus temporales propios eliminados;
permanece el directorio externo ya reportado por implementación
`%TEMP%\featureproof31-wsl` con el minirootfs descargado, por lo que T16 tampoco
puede afirmar ausencia total de temporales.

rejected -> progress/environment-provider/review.md

---

## Cierre cronológico de Review 5

El veredicto vigente es **rejected** por REV5-1. Aunque REV4-1 y REV4-2 quedan
cerrados y la cobertura exacta fresh alcanza **1181/1384 = 85.332370 %**, los
gates obligatorios no son reproduciblemente verdes: `go test -count=1 ./...`
falló en githubpr, `go test -count=2 ./...` falló en contextassembly,
correction y la matriz contractual de environment antes de quedar sin progreso,
y el primer gate Linux de `cmd/featureproof` falló la paridad API. Los reruns
aislados o seriales no invalidan esos fallos del gate prescrito. No se repitió
la ejecución global colgada. WSL quedó sin la distro efímera y no quedan
temporales `featureproof31*` en `%TEMP%`.

rejected -> progress/environment-provider/review.md

---

# Review 5 — revalidación independiente desde `6e8742f`

## Veredicto

**rejected**

REV4-1 y REV4-2 quedan funcionalmente cerrados: la autoridad completa se
valida antes de acquired/bind/cleanup, y network/observer tienen child budgets
incluidos en la lease. Sin embargo, los gates normales y Linux no son estables.
El fallo se reprodujo dentro de la propia matriz contractual bajo carga, por lo
que R15/T16 no pueden aprobarse.

## Estado de REV4-1 y REV4-2

| Hallazgo | Estado review 5 | Evidencia |
|---|---|---|
| REV4-1 | cerrado | `validateObservationAuthority` liga provider/version/template/profile/network policy/capabilities, attestation, repo/commit y target canónico antes de `StateAcquired`, antes de bind y al reconciliar cleanup. La matriz común prueba tamper acquire y reopen para ambos adapters; `Release` permanece en cero hasta autoridad válida. |
| REV4-2 | cerrado | `NetworkTimeout` y `ObserverTimeout` son obligatorios, usan child contexts, preservan cause y forman parte de `minimumLeaseTTL` con overflow fail-closed. Tests bloqueados prueban timeout/cancel, join y que un segundo delivery no adopta ni duplica I/O. |

## Hallazgos

## REV5-1 — Los gates obligatorios son sensibles a carga y fallan de forma reproducible

- Severidad: blocker
- Requirement o regla: R9, R14, R15, T15, T16, CONV-007
- Archivo y línea: `internal/environment/environment_test.go:555-626`,
  `cmd/featureproof/workflow_identity_test.go:619-645`
- Evidencia:
  - La primera ejecución `go test -count=1 ./...` falló en
    `internal/githubpr/TestSubmitBudgetPrecedesExternalCallAndLeaseExcludesSecondCoordinator`
    con `github pull request close: cancelled`.
  - `internal/githubpr` aislado `-count=10` y el global serial pasaron, pero
    `go test -count=2 ./...` volvió a fallar en contextassembly, correction y en
    `TestEnvironmentProviderFullContractMatrix/managed_fake/cancel_and_deadline`.
    Este último dejó un record durable `selected`, cero provider calls, porque
    el deadline real de 100 ms expiró durante el observer inicial. El test solo
    acepta `acquiring` sin calls o `released` con cleanup, y rechazó esa frontera
    recuperable. El comando dejó de progresar durante varios minutos y fue
    interrumpido; no se volvió a ejecutar el global colgado.
  - En Alpine, el primer perfil de `cmd/featureproof` falló
    `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces`: la
    superficie API terminó `inconclusive`. El test aislado `-count=3` y un
    segundo profile pasaron.
- Impacto: una entrega no puede afirmar global normal ni Linux reproduciblemente
  verdes. La matriz nueva usa deadlines de pared y assertions que no aceptan
  todas las fronteras durables posibles bajo carga, contrariando los tests
  coordinados y deterministas exigidos por la spec.
- Corrección requerida: reemplazar el deadline real por clock/barrier inyectado
  o aceptar y recuperar explícitamente `selected` con lease; eliminar la
  sensibilidad temporal del vertical de superficies. Repetir global paralelo,
  Linux5 y profiles fresh hasta obtener resultados estables.

## Revalidación funcional y arquitectónica

R1–R14 están implementados y cubiertos en el diff inspeccionado. Las
correcciones REV-1–REV4-2 permanecen presentes: catálogo explícito, lifecycle
durable/CAS, release intent, common suite con `LocalStore`, único
`EnvironmentPort`, separación operational/verdict, `Execute → checkpoint →
Promote`, fallbacks ENOSYS no-clobber y coverage pinneada. R15 falla únicamente
por REV5-1.

## Gates independientes de review 5

| Gate | Resultado |
|---|---|
| precondiciones, spec aprobada y T1–T16 `[x]` | pass |
| `gofmt -l`, `git diff --check 6e8742f`, `go vet ./...`, compile-only | pass |
| `go test -count=1 ./...` | **fail**: githubpr cancelled |
| `go test -count=10 ./internal/githubpr` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -count=2 ./...` | **fail**: contextassembly, correction y feature31 cancel/deadline; ejecución posteriormente sin progreso e interrumpida |
| race focal environment/workflow/cmd `-count=3` | pass |
| `go test -race -count=1 -p=1 ./...` | pass completo |
| Alpine Go 1.25.1: environment/workflow/localvalidation/contractreview | pass |
| Alpine `cmd/featureproof` profile, primer intento | **fail**: API parity `inconclusive` |
| Alpine test de paridad aislado `-count=3` y segundo profile cmd | pass |
| cross-build Windows/Linux/Darwin, 3 paquetes | pass 9/9 |
| coverage fresh Go 1.25.1 Windows/Linux | pass: **1181/1384 = 85.332370 %** |

No se observó red/provider/modelo/infraestructura real ni alcance Kubernetes.
Los comentarios y tasks están completos. La distro Alpine se autenticó con
SHA-256 `412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`
y fue desregistrada. Todos los temporales propios de review fueron eliminados.

rejected -> progress/environment-provider/review.md

---

# Review 3 — revalidación final desde `6e8742f`

## Veredicto

**rejected**

Se inspeccionó otra vez el diff completo, incluidos untracked, con foco en
REV2-1–REV2-7 y R1–R15. La publicación privada ya está separada de la promoción
visible, los fallbacks Linux ya preservan no-clobber y los gates normales/race,
Linux y cross-build son verdes. Persisten cuatro blockers: una lease aceptada
puede expirar durante la secuencia de acquire, el recovery de una adquisición
incierta libera una observación no autenticada, la matriz contractual no es
idéntica ni durable completa y el gate de coverage depende de un toolchain que
no registra ni valida.

## Estado de REV2-1–REV2-7

| Hallazgo previo | Estado review 3 | Evidencia |
|---|---|---|
| REV2-1 | cerrado en su forma original | La adquisición doblemente incierta conserva `AcquisitionKey`, queda `release_pending`, no libera handle vacío y, tras reopen, reconcilia antes de release. Queda el hallazgo distinto REV3-2 sobre validación de esa observación. |
| REV2-2 | abierto | `LeaseTTL` solo se compara con límites individuales; no cubre la secuencia máxima sin renew. |
| REV2-3 | cerrado | Runner y functional flow devuelven preparación privada; `checkpointEnvironment` precede `Promote`; drift no deja package/primary visible y release confirmado converge. |
| REV2-4 | abierto | La tabla creció, pero contiene ramas por adapter y salvo un caso usa `MemoryStore`; no demuestra la misma matriz durable completa. |
| REV2-5 | cerrado | Bundle fallback usa descriptor, claim persistente, `flock`, `Fstatat` y `Renameat`; file fallback usa `Linkat`; hay race/crash/redelivery coordinados. |
| REV2-6 | abierto | Ya no hay counts hardcoded y se conservan columnas/SHA-256, pero profiles de toolchains distintos se suman como bloques diferentes y cambian denominador/resultado. |
| REV2-7 | cerrado | No quedan temporales `featureproof31*`; la distro efímera de review fue desregistrada y sus artefactos eliminados. |

## Hallazgos

## REV3-1 — La lease no cubre la secuencia máxima sin renovación

- Severidad: blocker
- Requirement o regla: R9, T5, T7, CONV-005
- Archivo y línea: `internal/environment/service.go:151-156`,
  `internal/environment/service.go:390-430`,
  `internal/environment/environment_test.go:1349-1363`
- Evidencia: composición solo exige `LeaseTTL` mayor que `MaxElapsed`, cada
  timeout y `InitialBackoff` por separado. `acquireObservation` no renueva entre
  `Acquire`, `Reconcile` y wait. Una configuración aceptada puede usar, por
  ejemplo, `LeaseTTL=61s`, `MaxElapsed=60s` y `AcquireTimeout=60s`: el primer
  acquire y el reconcile posterior pueden consumir hasta 120s antes de volver
  al service. Otro owner puede adoptar durante ese I/O. Readiness tampoco valida
  explícitamente la cadena `wait + ObserveTimeout`. El único test negativo hace
  `LeaseTTL == AcquireTimeout`; no cubre sumas ni el límite inclusivo.
- Impacto: dos owners pueden ejecutar efectos provider para el mismo run pese a
  CAS/lease, violando la garantía que debía cerrar REV-4/REV2-2.
- Corrección requerida: renovar ownership entre todas las fronteras o validar
  que la lease cubra el peor tramo indivisible (`MaxElapsed`, boundaries y waits
  aplicables), con tests deterministas de suma, igualdad y adopción bloqueada.

## REV3-2 — Cleanup confía en una observación de reconcile sin verificar autoridad

- Severidad: blocker
- Requirement o regla: R6, R8, R10, T8
- Archivo y línea: `internal/environment/service.go:434-458`
- Evidencia: cuando el record no tiene handle, `cleanupDetached` acepta el
  resultado de `provider.Reconcile` si solo contiene `LifecycleHandle` y
  `EnvironmentID`. No comprueba `ProviderID`, `ProviderVersion`, availability ni
  su correspondencia con la selección. A continuación deriva
  `LifecycleDigest`, persiste intent y llama `Release`. `validateRecord` solo
  verifica forma de esos digests y no reconstruye esa relación. La suite
  double-uncertain siempre devuelve luego una observación válida.
- Impacto: una respuesta tampered/cross-provider puede convertirse en autoridad
  de cleanup y operar sobre un lifecycle diferente. No se satisface el fallo
  cerrado de handles cross-provider/cross-run.
- Corrección requerida: validar la observación reconciliada con la selección y
  la identidad durable completa antes de guardar lifecycle o liberar; ante
  mismatch conservar `release_pending`. Añadir casos comunes de provider/version
  drift y handle ajeno tras restart.

## REV3-3 — La suite contractual común aún contiene ramas y no usa store durable en toda la matriz

- Severidad: blocker
- Requirement o regla: R2, R5, R8, R12, R14, T4, T15
- Archivo y línea: `internal/environment/environment_test.go:446-661`
- Evidencia: `TestEnvironmentProviderFullContractMatrix` ramifica por kind en
  `499-503` y `602-604`, por lo que no aplica assertions observables idénticas a
  ExistingURLProvider y managed fake. `serviceFixture` usa `MemoryStore` en los
  escenarios nominal, unavailable/drift, response loss, authority,
  cancel/observe/release y concurrencia; solo `double_uncertain_restart`
  sustituye el store por `LocalStore`. El caso cancel/deadline de acquire llama
  al provider directamente, no al lifecycle durable.
- Impacto: la tabla no demuestra que ambos adapters satisfagan el mismo contrato
  durable de records, recovery, release intent, redelivery y cancelación; puede
  ocultar diferencias precisamente en los boundaries de R8/R12.
- Corrección requerida: ejecutar una factory única de service + `LocalStore`
  para todos los casos, sin branches por provider, y afirmar los mismos estados,
  records, eventos e I/O para ambos adapters.

## REV3-4 — Coverage cambia con el toolchain y el informe entregado ya no corresponde al diff

- Severidad: blocker
- Requirement o regla: R15, T16, `docs/verification.md` §4.5
- Archivo y línea: `scripts/feature31_coverage.py:48-97`,
  `progress/environment-provider/implementation.md` sección “Coverage exacta”
- Evidencia: el script autentica bytes del profile, pero no toolchain/GOOS ni
  compatibilidad de block layout. `merge_profiles` trata como distintos bloques
  con coordenadas diferentes. Con host por defecto Go 1.27.1 y profiles Linux
  Go 1.25.1 produjo **1654/2005 = 82.493766 %**; con solo los profiles Linux
  imprescindibles produjo **1540/1834 = 83.969466 %**. Forzando el host a
  Go 1.25.1, la misma evidencia produce **1130/1321 = 85.541257 %**. El informe
  declara todavía **1122/1313 = 85.453161 %** y hashes de otra ejecución.
- Impacto: el código supera 85 % al normalizar toolchain, pero el gate entregado
  falla en el entorno normal y puede duplicar statements silenciosamente; no es
  evidencia exacta ni reproducible del diff actual.
- Corrección requerida: fijar o registrar y validar la versión de Go de todos
  los perfiles (o rechazar layouts incompatibles), generar/ingerir los profiles
  Linux bajo ese toolchain y actualizar la evidencia del diff actual. La
  invocación estándar debe reproducir >=85 sin una variable externa oculta.

## R1–R15 y gates independientes

| Requirement | Resultado review 3 |
|---|---|
| R1, R3, R5, R7, R10–R13 | pass en implementación y tests inspeccionados |
| R2, R14 | fail por REV3-3 |
| R4, R9 | fail por REV3-1 |
| R6, R8 | fail por REV3-2 |
| R15 | fail por REV3-4 |

| Gate | Resultado |
|---|---|
| precondiciones: única feature 31 `in_progress`, spec aprobada, T1–T16 `[x]` | pass |
| `gofmt -l`, `git diff --check 6e8742f` | pass; solo warnings LF/CRLF |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| race focal environment/workflow/cmd `-count=3` con UCRT64 | pass |
| `go test -race -count=1 -p=1 ./...` con UCRT64 | pass completo |
| Alpine 3.22.2 WSL1: environment/workflow/cmd/localvalidation/contractreview | pass 5/5 |
| cross-build `go test -c`: 3 paquetes × Windows/Linux/Darwin amd64 | pass 9/9 |
| coverage, host y Linux Go 1.25.1 | pass umbral: 1130/1321 = 85.541257 % |
| coverage, invocación normal host Go 1.27.1 + Linux Go 1.25.1 | fail: 82.493766 % y denominador incompatible |

El minirootfs Alpine fue validado con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
No se observó provider, red, modelo o infraestructura real en tests, ni alcance
Kubernetes/feature 32. ExistingURLProvider sigue siendo no destructivo y el
managed fake permanece solo en tests. Los comentarios sistémicos inspeccionados
están presentes. No quedaron distros ni temporales propios de review.

rejected -> progress/environment-provider/review.md

---

# Review 2 — revalidación de correcciones

## Veredicto

**rejected**

Se volvió a inspeccionar el diff productivo y de tests completo desde `6e8742f`,
incluidos los nuevos fallbacks Linux y todos los archivos sin seguimiento. Los
gates normales, race, cross-build y runtime Linux son reproduciblemente verdes,
pero REV-2, REV-4, REV-5, REV-7 y REV-9 no están cerrados por completo. Además,
el fallback `ENOSYS` de bundles introduce una publicación no-replace no atómica
y faltan las pruebas race/crash expresamente requeridas.

## Estado de REV-1–REV-10

| Hallazgo previo | Estado review 2 | Evidencia |
|---|---|---|
| REV-1 | cerrado | `workflowConfig.EnvironmentCatalog` es obligatorio, se sella y se valida contra alias/kind/project/repository/policy; composición ya no inventa provider/template/capability. |
| REV-2 | abierto | el happy path incierto reconcilia y libera, pero una reconciliación que también falla pasa a release con handle vacío y digest sintético, sin identidad de acquire utilizable por un provider genérico. |
| REV-3 | cerrado | relaciones por estado, chain/head, staging, directory sync y fallos record/event/head están implementados y probados. |
| REV-4 | abierto | existen claim/renew/adoption CAS, pero una configuración aceptada puede expirar la lease durante acquire/observe/wait y permitir adopción concurrente. |
| REV-5 | abierto | los checkpoints denominados “publication” se ejecutan después de que los executors ya hayan producido/publicado el resultado primario. |
| REV-6 | cerrado | categorías de acquire y proyección separada de primary outcome/cleanup quedan cubiertas. |
| REV-7 | abierto | la matriz común sigue sin ejercer el lifecycle completo de cancelación, authority drift y recovery/crash en ambos adapters. |
| REV-8 | cerrado | el vertical API/CLI/MCP/GitHub recorre direct y preview, compara digest/transiciones y cuenta acquire/release. |
| REV-9 | abierto | el umbral real se supera, pero `feature31_coverage.py` no calcula exactamente los statement blocks ni consume evidencia Linux. |
| REV-10 | cerrado | runtime Linux fresco pasó para environment, workflow, cmd y los dos publicadores, con cwd correcto y dependencia `git` del paquete localvalidation. |

## Hallazgos abiertos y nuevos

## REV2-1 — Recovery puede declarar released sin conocer el lifecycle adquirido

- Severidad: blocker
- Requirement o regla: R8, R9, T7, ARCH-007
- Archivo y línea: `internal/environment/service.go:519-537`,
  `internal/environment/service.go:453-475`,
  `internal/environment/service.go:62-66`
- Evidencia: `recoverUncertainAcquire` intenta `Reconcile` una sola vez. Si
  también falla, `cleanupDetached` fabrica
  `stableKey("uncertain-lifecycle", AcquisitionKey)`, deriva otro release key y
  llama `Release` con `EnvironmentID` y `LifecycleHandle` vacíos. `ReleaseRequest`
  no transporta `AcquisitionKey`; el adapter no puede correlacionar de forma
  genérica el acquire incierto original. Si el provider devuelve `nil` ante ese
  request vacío, el core persiste `StateReleased` aunque nunca confirmó qué
  recurso creó. Los tests `TestUncertainAcquireAlwaysPersistsAndExecutesRelease`
  programan siempre un segundo `Reconcile` exitoso; la matriz común también
  cubre solo reconcile exitoso.
- Impacto: response loss doble o indisponibilidad temporal de reconcile puede
  perder una reserva o confirmar falsamente su liberación. Recovery posterior
  ya no reintenta reconcile porque el record pasó a release pending/released.
- Corrección requerida: conservar una identidad de adquisición que el provider
  pueda reconciliar durante cleanup, mantener estado incierto hasta obtener
  lifecycle o confirmación release autoritativa, y probar reconcile fallido,
  restart y convergencia para ambos adapters.

## REV2-2 — Las leases aceptadas pueden expirar durante I/O provider

- Severidad: blocker
- Requirement o regla: R9, T5, CONV-005
- Archivo y línea: `internal/environment/service.go:149-154`,
  `internal/environment/service.go:207-225`,
  `internal/environment/service.go:268-285`
- Evidencia: `NewLifecycleService` solo exige duraciones positivas; no exige que
  `LeaseTTL` cubra `AcquireTimeout`, `ObserveTimeout` o una espera completa. No
  existe heartbeat durante esas fronteras: la lease se renueva antes de la
  llamada, y en readiness incluso se espera antes de renovar. Una composición
  válida con lease corta permite a otro owner adoptar mientras el primer
  provider call sigue activo. El test de ownership usa siempre lease de 5 s y
  boundary de 1 s, por lo que no explora esta configuración aceptada.
- Impacto: el mismo run puede ejecutar I/O de provider concurrente pese al CAS,
  precisamente el efecto que REV-4 debía impedir.
- Corrección requerida: validar relaciones seguras entre budgets y lease o
  implementar renovación poseída durante las llamadas con lifecycle explícito;
  añadir un test determinista donde el boundary dura más que la lease.

## REV2-3 — La revalidación de publicación ocurre después del efecto

- Severidad: blocker
- Requirement o regla: R7, T8, T13
- Archivo y línea: `internal/validationworkflow/service.go:825-854`,
  `internal/validationworkflow/service.go:1133-1165`
- Evidencia: para functional flow se llama primero
  `FunctionalFlow.Execute`, se marca `FunctionalFlowPrimaryDurable=true` y solo
  después se ejecuta `checkpointEnvironment`. Para el runner clásico se llama
  primero `Execution.Execute`, que devuelve un package ya creado, y después se
  revalida. El checkpoint en `terminal` también es posterior al resultado
  primario. El test de drift confirma `execution.callCount()==1`; por diseño no
  demuestra “cero publicación”, solo evita adjuntar `record.Package`.
  Adicionalmente, en los branches de drift de `terminal` se ignora el resultado
  de `Environment.Release` y se devuelve siempre cleanup pending; recovery entra
  de nuevo en el mismo branch, aunque la liberación ya estuviera confirmada.
- Impacto: un target puede quedar stale durante la ejecución y aun así producir
  un bundle/primary durable antes de detectarse; después el workflow puede quedar
  permanentemente no terminal con `cleanup_pending` falso.
- Corrección requerida: separar ejecución de promoción/publicación y colocar el
  checkpoint composition-owned inmediatamente antes de hacer visible cada
  resultado; hacer converger el terminal tras release confirmado y comprobar
  ausencia del artefacto visible, no solo ausencia de su proyección en Record.

## REV2-4 — La matriz “idéntica” no cubre el lifecycle completo exigido

- Severidad: blocker
- Requirement o regla: R2, R5, R14, T3, T10, T15
- Archivo y línea: `internal/environment/environment_test.go:434-560`
- Evidencia: el caso `cancel_and_deadline` llama directamente a
  `harness.provider.Acquire`; no pasa por `LifecycleService` ni verifica intent,
  cleanup o recovery. El caso response loss solo programa reconcile exitoso.
  No hay en la tabla común invalid selection, attestation/network/capability
  drift, cancelación en observe/release, crash/restart con store durable ni
  reconcile incierto. Esos casos están fuera de la factory común o solo usan el
  managed fake.
- Impacto: la prueba no demuestra que ExistingURLProvider y un managed provider
  satisfagan las mismas reglas observables enumeradas por R14; el hueco oculta
  precisamente REV2-1.
- Corrección requerida: ejecutar cada escenario contractual completo mediante
  el mismo service/store/factory contra ambos adapters, incluidas todas las
  fronteras cancelables y recovery tras crash.

## REV2-5 — El fallback Linux de bundles no preserva no-clobber atómico

- Severidad: blocker
- Requirement o regla: R12, R15, ARCH-007, CONV-005
- Archivo y línea: `internal/localvalidation/commit_linux.go:19-48`,
  `internal/localvalidation/commit_linux_test.go:14-59`,
  `internal/contractreview/rename_linux_test.go:13-47`
- Evidencia: ante `ENOSYS` y staging directorio, localvalidation hace
  `Lstat(final)` y luego `os.Rename(staging, final)`. La comprobación y la
  publicación son dos operaciones; un destino vacío creado entre ambas puede
  ser reemplazado silenciosamente. El test solo prueba publicación y colisión
  secuenciales. Los tests de ambos fallbacks no tienen barreras concurrentes ni
  fault/crash injection entre link/rename/unlink/sync, pese a que review 2 lo
  exigía expresamente. El fallback file de contractreview usa `linkat` de forma
  no-clobber, pero tampoco demuestra convergencia tras crash entre link y
  unlink.
- Impacto: el fallback que hizo verde WSL1 debilita el contrato atómico del
  publicador y no aporta evidencia de race/crash. Un gate verde no compensa una
  primitiva de publicación más débil.
- Corrección requerida: usar una exclusión/claim descriptor-relative que haga
  indivisible la decisión no-replace para bundles o fallar cerrado si el kernel
  no puede ofrecerla; añadir tests concurrentes y de cada crash boundary que
  verifiquen winner único, no-clobber y redelivery.

## REV2-6 — El gate de coverage no es exacto ni autosuficiente

- Severidad: blocker
- Requirement o regla: R15, T16, `docs/verification.md` §4.5
- Archivo y línea: `scripts/feature31_coverage.py:17-24`,
  `scripts/feature31_coverage.py:68-99`
- Evidencia:
  - el script codifica manualmente coverage Linux (16/22 y 8/9) y solo autentica
    el hash del source; no genera, recibe ni autentica los perfiles que prueban
    esos counts;
  - el regex descarta las columnas de cada statement block y la clave de merge
    usa solo archivo/start-line/end-line/num-statements. Bloques distintos en la
    misma línea/rango colisionan. Por eso informa, entre otros, `model.go`
    165/169 en vez de 165/170, `store.go` 333/398 en vez de 333/399 y
    `sync_unix.go` 0/1 en vez de los 5/6 observados en el perfil Linux fresco.
  - el script imprime **1026/1191 = 86.146096 %**. Recalculando con columnas y
    perfiles frescos Windows+Linux, el resultado es
    **1031/1198 = 86.060100 %**.
- Impacto: el código sí supera 85 %, pero la evidencia entregada como gate
  “exacto” es incorrecta y no reproducible por sí sola. R15/T16 exigen medición
  exacta, no solo superar casualmente el umbral.
- Corrección requerida: conservar start/end column en la identidad del bloque,
  generar/ingerir perfiles de cada build tag dentro del gate y autenticar esos
  artefactos, sin covered counts escritos a mano.

## REV2-7 — Persisten temporales que el informe declara eliminados

- Severidad: blocker
- Requirement o regla: R15, T16
- Archivo y línea: fuera del repositorio, `%TEMP%`
- Evidencia: al iniciar review 2 seguían presentes
  `%TEMP%\featureproof31-cross` y
  `%TEMP%\featureproof31-cross-final`, cada uno con nueve binarios de test
  Windows/Linux/Darwin, además de `%TEMP%\featureproof31-global.cover`
  (2,286,526 bytes). El informe de implementación afirma que esos paths ya no
  existen.
- Impacto: falla la auditoría obligatoria de temporales y la evidencia del
  informe no coincide con el estado reproducible.
- Corrección requerida: eliminar únicamente esos artefactos verificados y
  repetir la auditoría antes de solicitar review.

## Gates independientes de review 2

| Gate | Resultado |
|---|---|
| precondiciones: única feature in_progress, spec aprobada, T1–T16 `[x]` | pass |
| `gofmt -d` sobre 19 archivos Go del diff | pass |
| `git diff --check 6e8742f` | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| afectados environment/workflow/cmd x3 | pass |
| race afectados | pass |
| environment race x20 | pass |
| `go test -race -count=1 -p=1 ./...` UCRT64 | pass completo |
| cross-build environment/workflow/cmd × Windows/Linux/Darwin amd64 | pass 9/9 |
| Alpine 3.22.2 WSL1: environment/workflow/cmd | pass 3/3 con cwd de paquete |
| Alpine WSL1: localvalidation/contractreview | pass 2/2; localvalidation requiere `git` |
| `py -3 scripts/feature31_coverage.py` | ejecuta/pass, pero resultado no exacto |
| coverage independiente con perfiles Windows+Linux y columnas | pass umbral: 1031/1198 = 86.060100 % |

El minirootfs usado en review se verificó contra SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
La distro efímera de review se desregistró y todos sus artefactos propios fueron
eliminados. No se observó red/provider/modelo/infraestructura real en los tests,
ni alcance Kubernetes/feature 32. Los símbolos nuevos inspeccionados mantienen
comentarios sistémicos.

rejected -> progress/environment-provider/review.md

---

## Cierre append-only de Review 3

El bloque **Review 3 — revalidación final desde `6e8742f`** anterior contiene la
evidencia y los cuatro blockers vigentes: lease insuficiente (REV3-1),
reconcile de cleanup sin autenticar (REV3-2), matriz común no idéntica/durable
completa (REV3-3) y coverage dependiente de toolchain (REV3-4). Este cierre es
el veredicto cronológico más reciente del informe.

rejected -> progress/environment-provider/review.md

---

# Review 4 — revalidación independiente desde `6e8742f`

## Veredicto

**rejected**

REV3-1, REV3-3 y REV3-4 quedan cerrados con evidencia reproducible: lease
agregada fail-closed, matriz común durable sin ramas y coverage exacta
pinneada. REV3-2 permanece abierto porque la observación reconciliada no se
liga a todas las dimensiones de la selección. La misma omisión existe en el
acquire nominal. Además, dos callbacks dentro de la lease carecen de budget
finito propio.

## Estado de REV3-1–REV3-4

| Hallazgo | Estado review 4 | Evidencia |
|---|---|---|
| REV3-1 | cerrado | `minimumLeaseTTL` agrega attempts acquire/reconcile, redelivery reconcile, waits, readiness/observe y release; suma/producto detectan overflow y `LeaseTTL <= minimum` falla antes de I/O. Tests cubren suma, igualdad, overflow y cero provider calls. |
| REV3-2 | abierto | Se validan provider/version/profile/capabilities y target sintáctico, pero faltan relaciones template/network policy; véase REV4-1. |
| REV3-3 | cerrado | Todos los escenarios de `TestEnvironmentProviderFullContractMatrix` usan `contractServiceFixture` con `LocalStore`; no hay branches por adapter y las assertions son comunes. |
| REV3-4 | cerrado | Toolchain `go1.25.1` se deriva de `go.mod`; manifest liga toolchain/GOOS/GOARCH/SHA-256, se comparan layouts y perfiles fresh reproducen 1166/1364. Toolchain, plataforma y hash incorrectos fallan antes del merge. |

## Hallazgos

## REV4-1 — El target y el cleanup no quedan ligados a template y network policy sellados

- Severidad: blocker
- Requirement o regla: R1, R3, R4, R6, R7, R8, T2, T5, T10
- Archivo y línea: `internal/environment/service.go:304-377`,
  `internal/environment/service.go:527-539`,
  `internal/environment/existing.go:37-87`,
  `internal/environment/environment_test.go:329-336`
- Evidencia: en adquisición solo se compara provider/version y capabilities.
  `ProfileID`, `EnvironmentRef` y `Network.PolicyID` procedentes de la
  observación no se comparan con `Selection.ProfileID`,
  `Selection.TemplateReference` y `Selection.NetworkPolicyID`. En cleanup,
  `validateCleanupObservation` añadió profile pero sigue sin comparar template
  ni policy ID. `SealTarget` valida forma, attestation y coherencia
  endpoint-policy, no la relación con `Selection`. El fixture nominal demuestra
  el hueco: la selección usa `TemplateReference: "template-v1"`, mientras
  `validObservation` deja `EnvironmentRef` vacío y aun así adquiere/bindea. Los
  casos tamper de double uncertainty solo cambian provider y commit.
- Impacto: un provider puede devolver un target de otra referencia/template o
  una policy con ID distinto, obtener un binding válido y, durante recovery,
  derivar lifecycle y ejecutar Release. La selección composition-owned deja de
  ser autoridad efectiva para dimensiones expresamente selladas.
- Corrección requerida: validar todas las relaciones selección↔observación antes
  de persistir `environment_acquired`, antes de bind y en reconcile de cleanup;
  ante mismatch mantener cleanup recuperable sin release. Añadir a la matriz
  común tamper independiente de template, profile y network policy tanto en
  acquire como tras reopen.

## REV4-2 — Network authorization y observer no tienen duración finita propia

- Severidad: blocker
- Requirement o regla: R4, R9, R12, ARCH-005, ARCH-006
- Archivo y línea: `internal/environment/service.go:373`,
  `internal/environment/service.go:542-554`,
  `internal/environment/service.go:13-18`
- Evidencia: `Network.Authorize` y `Observer.Observe` reciben directamente el
  contexto del caller. Con `context.Background`, usado también por las rutas
  normales y tests, no tienen deadline. `Limits` no define budget para esas
  fronteras y `minimumLeaseTTL` no puede incluirlas. Un authorizer u observer
  lento puede sobrepasar la lease o no retornar nunca; no existe test de
  callback bloqueado/cancelado bajo límite interno.
- Impacto: binding/observabilidad no cumplen duración finita y otro owner puede
  adoptar cuando la lease vence mientras el primero sigue dentro de un
  callback. El servicio tampoco garantiza terminar su trabajo propio.
- Corrección requerida: aplicar child deadlines positivos y finitos (o un
  deadline total del lifecycle que los incluya), incorporarlos a la ventana de
  lease y probar authorizer/observer bloqueados con cancelación y adopción
  impedida.

## Revalidación R1–R15

| Requirement | Resultado |
|---|---|
| R1, R3, R6–R8 | fail por REV4-1 |
| R4, R9, R12 | fail por REV4-1/REV4-2 |
| R2, R5, R10, R11, R13, R14 | pass en implementación y pruebas inspeccionadas |
| R15 | pass técnico de gates, pero la feature no es aprobable mientras existan blockers funcionales |

Se revalidaron también los hallazgos REV-1–REV-10 y REV2-1–REV2-7. Sus
correcciones permanecen presentes salvo las dimensiones de autoridad y budgets
explicitadas arriba. La separación `Execute → checkpoint → Promote` sigue en
runner y functional flow; drift bloquea artefacto visible y release confirmado
converge. Los fallbacks ENOSYS conservan descriptor-relative no-clobber,
winner único y recovery de crash/response loss.

## Gates independientes de review 4

| Gate | Resultado |
|---|---|
| precondiciones: única feature 31 `in_progress`, spec aprobada, T1–T16 `[x]` | pass |
| `gofmt -l`, `git diff --check 6e8742f` | pass; solo warnings LF/CRLF |
| `go vet ./...` con Go 1.25.1 | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| race focal environment/workflow/cmd `-count=3` con Go 1.25.1/UCRT64 | pass |
| `go test -race -count=1 -p=1 ./...` con Go 1.25.1/UCRT64 | pass completo |
| Alpine 3.22.2 WSL1, Go 1.25.1: environment/workflow/cmd/localvalidation/contractreview | pass 5/5 |
| cross-build `go test -c`: 3 paquetes × Windows/Linux/Darwin amd64 | pass 9/9 |
| perfiles fresh Windows/Linux + `feature31_coverage.py --manifest` | pass: **1166/1364 = 85.483871 %** |
| manifest con toolchain/plataforma/SHA-256 incorrectos | fail-closed antes del merge |

El minirootfs Alpine se autenticó con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
No se observó red/provider/modelo/infraestructura real ni alcance Kubernetes.
ExistingURLProvider sigue siendo no destructivo y el managed fake solo existe
en tests. Comentarios y tasks están completos. La distro y todos los temporales
propios de review fueron eliminados; no quedan paths `featureproof31*` en
`%TEMP%`.

rejected -> progress/environment-provider/review.md

---

## Cierre cronológico de Review 5

El veredicto vigente es **rejected** por REV5-1. REV4-1 y REV4-2 están cerrados
y la cobertura fresh exacta es **1181/1384 = 85.332370 %**, pero los gates
obligatorios no son reproduciblemente verdes: `go test -count=1 ./...` falló
en githubpr; `go test -count=2 ./...` falló en contextassembly, correction y
la matriz contractual de environment antes de quedar sin progreso; y el primer
gate Linux de `cmd/featureproof` falló la paridad API. Los reruns aislados o
seriales no invalidan estos fallos. No se repitió el global colgado. WSL quedó
sin la distro efímera y no quedan temporales `featureproof31*` en `%TEMP%`.

rejected -> progress/environment-provider/review.md

---

# Review 6 — revalidación independiente desde `6e8742f`

## Veredicto

**approved**

REV5-1 queda cerrado. La matriz contractual ya no comparte un parent deadline
corto: cada adapter y escenario obtiene un fixture nuevo, y cancelación y
deadline se sincronizan mediante `started` exactamente al entrar en `Acquire`.
El observer inicial conserva su budget propio, no hay `time.Sleep`/`time.After`
de feature 31 y las aserciones observan una frontera durable inequívoca.

## Estado de hallazgos y requirements

No quedan hallazgos abiertos. Se revalidaron REV-1–REV-10, REV2-1–REV2-7,
REV3-1–REV3-4, REV4-1/2 y REV5-1; sus correcciones permanecen presentes. R1–R15
tienen implementación y tests trazables, T1–T16 están marcadas `[x]`, existe
aprobación humana y feature 31 es la única `in_progress`.

La revalidación incluye catálogo composition-owned sin defaults, autoridad
completa antes de acquired/bind/cleanup, lifecycle/store/CAS durable, release
intent previo a I/O, cleanup detached finito, common suite idéntica y durable,
checkpoint antes de promoción visible, ExistingURLProvider no destructivo,
cuatro superficies sobre un único `EnvironmentPort`, outcomes operacionales
separados del verdict y records content-free exactly-once. Los fallbacks Linux
mantienen no-clobber, claim descriptor-relative y recovery ante crash/response
loss.

## Gates independientes de review 6

| Gate | Resultado |
|---|---|
| formato dirigido, `git diff --check 6e8742f`, `go vet ./...`, compile-only | pass |
| matriz contractual normal `-count=30` | pass 30/30, 185.394 s |
| matriz contractual race UCRT64 `-count=20` | pass 20/20, 199.310 s |
| global default `go test -count=2 ./...` ejecutado en aislamiento | pass completo |
| global serial `go test -count=1 -p=1 ./...` | pass completo |
| global race serial UCRT64 `go test -race -count=1 -p=1 ./...` | pass completo |
| Alpine 3.22.2 WSL1: environment/workflow/localvalidation/contractreview `count=2` | pass 4/4 repetido |
| Alpine 3.22.2 WSL1: `cmd/featureproof` `count=3` | pass 3/3 |
| cross-build environment/workflow/cmd × Windows/Linux/Darwin amd64 | pass 9/9 |
| coverage fresh Go 1.25.1 Windows/Linux con manifest autenticado | pass: **1182/1384 = 85.404624 %** |

Una ejecución exploratoria del global coincidente deliberadamente con el
estrés race agotó una lease de un test histórico de `functionalflow`; no era
un gate aislado. El mismo global prescrito se repitió sin carga competidora y
pasó completo, igual que serial y race serial.

El minirootfs se verificó con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
No se observó provider, modelo, red o infraestructura real en tests ni alcance
Kubernetes/feature 32. Comentarios sistémicos, tasks y scope están completos.
La distro efímera, binarios, profiles, cross-builds y manifest de review fueron
eliminados; no quedan distros registradas, binarios `.test` en el workspace ni
paths `featureproof31*` en `%TEMP%`.

approved -> progress/environment-provider/review.md
