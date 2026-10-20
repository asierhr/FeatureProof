# Implementación — Inspector estático de backends Go

## Resultado de octava corrección

Se corrigieron REV-22 y REV-23 de la séptima revisión sin reabrir los cierres
REV-1..REV-21.

`ReplanJournal` expone ahora `ClaimRun`, `RenewRun` y `ReleaseRun` con una
`RunLease` generacional. `Coordinator.Run` adquiere esa autoridad antes de
leer checkpoints, restaurar/consumir presupuestos o invocar el modelo. Un
heartbeat unido al ciclo renueva durante modelo, aprobación y tools; perder la
generación cancela la operación. El release usa un contexto acotado independiente
y un owner antiguo no puede renovar ni borrar a su sucesor.

`LocalJournal` guarda owner, expiración y generation en el mismo snapshot
atómico que events/checkpoint. Cada transacción queda protegida por lock de
fichero multiproceso (`LockFileEx` en Windows, `flock` en Unix), además de la
optimización local. Dos handles y dos `Coordinator` independientes sobre el
mismo root producen una sola inferencia; otro test ejecuta dos procesos del SO
y confirma un solo ganador. Cancelación, error del modelo, heartbeat, expiración
y recovery conservan journal íntegro y fencing monotónico.

El cierre GitHub separa el presupuesto interno de cleanup/join del contexto del
caller. Tras señalizar cancelación, el worker conserva su contexto acotado para
cancelar el run, publicar el check final y salir; `Close` da prioridad a un join
ya confirmado antes de clasificar una expiración como `CodeCancelled`. El test
usa barreras `submitted`/`statusRead` y un deadline de caller distinto de
`CloseTimeout`, sin sleeps.

### Evidencia REV-22..REV-23

- `TestDurableRunLeaseSerializesIndependentCoordinators`: dos coordinators y
  wrappers diferentes, un modelo compartido, una inferencia y terminal íntegro.
- `TestDurableRunLeaseRecoversCancellationErrorAndExpiry`: cancel, error,
  expiración, generation fence y recovery.
- `TestDurableRunLeaseRenewsAcrossBlockingInference`: heartbeat confirmado
  durante una inferencia bloqueada y reclaim rechazado.
- `TestDurableRunLeaseElectsOneOperatingSystemProcess`: dos procesos reales,
  un ganador durable.
- `TestSupervisorCancellationFinalizesAndJoins`: pasa `-count=1000`; confirma
  cancelación, publicación final y join mediante eventos.

## Resultado de séptima corrección

Se corrigieron REV-20..REV-22 de la sexta revisión y se revalidaron T1–T12,
además de reauditar los cierres REV-1..REV-19.

La autoridad de autenticación es ahora una identidad tipada y sellada. El
dominio `local` usa import path canónico y símbolo; el dominio
`external_explicit` usa import path literal, alias explícito y símbolo. La
clave canónica incluye longitudes y dominio, la configuración se copia
profundamente y todo el valor participa en el digest de `Input`. Los
adversarios demuestran que un selector textual idéntico local/externo no
colisiona y que dos archivos con el mismo alias/símbolo pero import paths
externos distintos solo autorizan el matcher exacto.

`FreezeConfiguredRepositoryWithClock` fija una única `DeadlineAt` absoluta
con el mínimo entre el deadline padre y `MaxDuration`. El valor queda dentro
del `Input` sellado y su digest, y `DerivedSource`/`Inspector` lo conservan en
indexación, routes y proyección. Las pruebas usan un reloj inyectado y barreras
de fase; después de agotar la ventana, ninguna entrada pública reinicia el
presupuesto.

`Coordinator.Run` adquiere la propiedad de la lineage antes de leer/restaurar
presupuestos y antes de cualquier inferencia. Los seguidores esperan al owner,
releen el journal confirmado y obtienen el mismo terminal. La reserva CAS
`EventCycleReserved` y el checkpoint `InferenceStarted` siguen siendo los
hechos durables previos al modelo. La prueba adversaria usa 32 workers, un fake
sincronizado y exige 32 resultados correctos, una sola inferencia y journal
íntegro; el focal race pasa 500 repeticiones.

## Resultado de sexta corrección

Se corrigieron REV-16..REV-19 de la quinta revisión y se revalidaron T1–T12.

Todos los accesos del request conservan ahora el `ast.Object` del parámetro demostrado por la firma. `PathValue`, `chi.URLParam`, `URL.Query().Get/Has`, indexación de query y `json.NewDecoder(request.Body)` comparan ese objeto exacto. Un parámetro homónimo introducido con `var`, `:=`, bloque anidado o rama no puede producir parámetros, body ni requiredness.

Auth usa una identidad canónica sin `#`: para código local es `import/path.Symbol` cuando existe módulo y `directory.Symbol` como identidad de repositorio en snapshots sin module path; para imports externos ausentes solo se admite el alias explícito como identidad sintáctica. Dos directorios con el mismo package declarado no colisionan. La configuración rechaza identidades o nombres de scheme duplicados, se copia profundamente y participa en el digest sellado.

Cada middleware conserva identidad, sintaxis, lookup local y ubicación exacta de aplicación `Use`/`With`. Al hacer match, el scheme recibe únicamente esa aplicación y, si existe, la declaración local. Las claims `security/<i>`, `security/<i>/schemes/<name>` y `security_schemes/<name>` se asignan por nombre de scheme canónico; una claim de ausencia no recibe provenance de middleware no allowlisted. El closure changed-only incorpora la declaración local exacta.

`inspectionContext` siempre calcula el mínimo entre el deadline padre y `now + MaxDuration`. Las entradas públicas reutilizan ese contexto durante freeze, sellado, registry, inspección y proyección. La prueba del helper usa un instante explícito y barreras de contexto, sin `time.Sleep`, para demostrar que gana tanto el presupuesto corto como el padre corto y que una unidad posterior observa `DeadlineExceeded`.

## Trazabilidad R → tests

| R | Evidencia principal |
|---|---|
| R1–R2 | `TestCompleteAuthorityValidationPrecedesRepositoryRead`, `TestOneDeadlineCancelsBetweenFreezeAndInput`, tests descriptor-relative y stable read previos |
| R3–R4 | patterns net/http/Chi previos, `TestUnavailableImplicitImportCannotAuthorizeMiddlewareButExplicitAliasCan`, packages/aliases/shadowing |
| R5 | `TestRequestFactsRequireExactParameterObject`, `TestClaimProvenanceSeparatesParametersBodiesAndStatuses`, guardas terminales previas |
| R6 | custom JSON, objeto exacto Decode/Encode y closure nested previos |
| R7 | writer first-commit y control flow parcial previos; `TestTypedAuthIdentitySeparatesLocalAndExternalDomains` |
| R8–R9 | diagnósticos parciales, separación exacta de provenance, `TestTypedExternalIdentityIncludesLiteralImportPath` |
| R10 | `TestSealedAbsoluteDeadlineCannotRestartAfterFreeze`, `TestFreezeSealsTheShorterOuterDeadline`, `TestSealedDeadlineExpiresInsideEveryInspectionPhase`, límites/cancelación por traversal previos |
| R11 | `TestAuthIdentityAndProvenanceArePerMatcher`, changed-only de schemas/builders/middleware |
| R12 | verticales OpenAPI+Go CLI/API/GitHub, adapter derived y `TestConcurrentCycleHasOneModelCall` |
| R13 | adversarios auth/deadline/concurrencia de séptima review, matriz de canarios y manifest de coverage exhaustivo |

## Evidencia específica REV-16..REV-19

- REV-16: `TestRequestFactsRequireExactParameterObject` cubre shadows de path, query, URLParam y Decode con ausencia de claims positivas.
- REV-17: `TestAuthIdentityAndProvenanceArePerMatcher` cubre dos schemes, middleware no allowlisted, alias externo, negativas cruzadas y changed impact; `TestSecurityAbsenceClaimDoesNotInheritUnmatchedMiddleware` cubre la claim vacía.
- REV-18: el mismo test separa dos directorios con package `auth` y autoriza solo un import path; `TestAuthConfigurationRejectsCollisionsAndOwnsItsDigest` cubre colisiones, copia y digest.
- REV-19: `TestInspectionContextChoosesShortestDeadline` prueba ambos órdenes de deadline y expiración determinista sin sleeps.

## Evidencia específica REV-20..REV-22

- REV-20: `TestTypedAuthIdentitySeparatesLocalAndExternalDomains` prueba la
  colisión textual `ext.Require` entre una declaración local y un alias externo;
  `TestTypedExternalIdentityIncludesLiteralImportPath` separa dos imports
  externos con el mismo alias y símbolo en archivos distintos.
- REV-21: `TestSealedAbsoluteDeadlineCannotRestartAfterFreeze` agota la ventana
  sellada y comprueba `Inspector` y `DerivedSource`; las pruebas shorter-deadline
  y phase-barrier cubren parent/config e index/routes/projection.
- REV-22: `TestConcurrentCycleHasOneModelCall` ejecuta 32 seguidores y exige una
  sola llamada. Pasa con `-race -count=500`, sin data races ni forks del journal.

## Gates finales ejecutados

- `go test -count=20 ./internal/goinspector`: pasa.
- `go test -count=20 ./internal/replanning ./internal/githubpr`: pasa.
- combined OpenAPI+Go compatible/conflictivo en CLI con `-count=20`: pasa.
- `TestSupervisorCancellationFinalizesAndJoins -count=1000`: pasa tras separar
  los contextos de caller y cleanup/join.
- focal durable multiproceso/handles/cancel/error/expiry/heartbeat con
  `-race -count=50`: pasa.
- `go test -race -run '^TestConcurrentCycleHasOneModelCall$' -count=500 ./internal/replanning`: pasa.
- `go test -race -count=2` sobre inspector, catálogo, workflow, GitHub, replanning y CLI con GCC/UCRT64: pasa sin carreras.
- `go test -race -count=2 ./...` con GCC/UCRT64: todos los paquetes pasan salvo las dos repeticiones del symlink Windows histórico en `internal/localvalidation`; no aparece ninguna carrera.
- `scripts/verify-feature22-coverage.ps1`: pasa. Perfiles globales: CLI 72,1 %, validationworkflow 86,1 %, githubpr 85,1 %; cada función obligatoria del manifest alcanza ≥85 %. Replanning completo alcanza 85,0 %.
- `internal/goinspector`: 86,7 % statements.
- `go vet` de paquetes afectados, compile-only, `gofmt -l` y `git diff --check`: pasan.

## Límites y seguridad

La lista v1 sigue cerrada. Wrappers, reflexión, routers externos, imports implícitos no demostrables, tipos externos, custom marshalers y flujo no exhaustivo se omiten o degradan. El inspector no usa `go list`, `go/packages`, importers del host, comandos, red, credenciales, environment ni module cache, y nunca ejecuta fixtures. Revision, roots, changed paths, build context, auth, limits y ProjectionPolicy proceden exclusivamente de composición sellada y se copian defensivamente.

## Resultado de novena corrección — REV-22 y REV-23

REV-22 queda cerrado con fencing generacional en el store. `RunLease` acompaña todas las lecturas, checkpoints y CAS que usa `Coordinator`; `OwnedSaveCheckpoint` y `OwnedCompareAndAppend` validan owner, generation y expiry bajo el mismo mutex/lock de fichero que publica el estado. `ReserveInference` valida además el prefijo esperado y persiste `InferenceStarted` en esa misma transacción justo antes de `InferStructured`. Un resultado de persistencia incierto se relee y falla cerrado sin llamar al modelo. La generación obsoleta nunca puede reservar, escribir ni inferir.

`TestCoordinatorProcessFencing` lanza dos procesos reales con dos coordinators y handles `LocalJournal` sobre el mismo root. El owner A queda suspendido antes del fence sin heartbeat; B observa una hora lógica posterior, reclama generation+1, crea el único efecto de modelo mediante `O_EXCL` y deja un terminal íntegro. Al reanudar A, su reserva es rechazada por el store. `TestInferenceReservationRejectsStaleGeneration`, `TestUncertainInferenceReservationFailsClosed` y `TestOwnedJournalFencingBranches` cubren stores memory/local, expiry/reclaim, cancelación, regresiones, errores pre/post commit y recovery sin replay.

REV-23 usa un único lifecycle instalado antes de cancelar: `beginShutdown` crea un solo contexto acotado compartido por finalización y release, señaliza cancelación y mantiene ownership hasta `wg.Wait`. El contexto del caller solo clasifica el resultado; incluso ya cancelado, `Close` no retorna antes del join. Al vencer el presupuesto interno, el mismo contexto cancela dependencias y `Close` espera sus defers antes de devolver `CodeCancelled`. `TestCloseHonorsCallerCancellationAndConfirmedJoin` y `TestCloseInternalBudgetCancelsCleanupAndStillJoins` usan barreras para caller cancelado, timeout interno, cleanup/release y join sin sleeps ni waiter huérfano.

### Evidencia final de la novena corrección

- `go test -count=20 -run '^TestCoordinatorProcessFencing$' ./internal/replanning`: pasa; dos coordinators reales por repetición, una inferencia y terminal único.
- `go test -race -count=20 -run '^TestCoordinatorProcessFencing$' ./internal/replanning`: pasa con GCC/UCRT64.
- `go test -race -count=500 -run '^TestInferenceReservationRejectsStaleGeneration$' ./internal/replanning`: pasa.
- `go test -count=2000 -run '^TestSupervisorCancellationFinalizesAndJoins$' ./internal/githubpr`: pasa.
- `go test -count=20 ./internal/replanning ./internal/githubpr ./internal/goinspector`: pasa.
- Race afectada `-count=2`: pasa en goinspector, contractcatalog, openapi, agentplanning, planapproval, replanning, validationworkflow, githubpr y CLI.
- `go test -race -count=2 ./...`: pasa en todos los paquetes salvo las dos repeticiones del symlink histórico de Windows sin privilegio; no hay data races.
- Coverage: goinspector 86,7 %, replanning 85,2 %, githubpr 85,5 %; `scripts/verify-feature22-coverage.ps1` pasa su manifest exhaustivo.
- `go vet ./...`, compile-only global y `git diff --check`: pasan.
- Los binarios temporales `goinspector-eighth` y `replan-eighth` fueron confirmados como artefactos y eliminados; `feature_list.json` no fue modificado por el implementer.

REV-1..REV-21 se reauditaron mediante las suites afectadas y globales sin regresiones nuevas. T1–T12 permanecen completas. Se solicita novena revisión independiente.

## Resultado de décima corrección — REV-24

REV-24 queda corregido sin reabrir REV-1..REV-23. `OwnershipConfig` obliga a
componer de forma explícita `LeaseTTL`, `RenewInterval`,
`StoreOperationTimeout`, `Clock` y `Waiter`. La composición rechaza cualquier
envolvente donde `TTL <= 2 × timeout de store + intervalo de renovación`, y
cada acceso owned del coordinator recibe un contexto limitado por ese timeout.
La configuración productiva queda centralizada en `ProductionOwnershipConfig`
con 30 s, 5 s y 10 s respectivamente.

`LocalJournal` da turno prioritario a una renovación ya encolada. Las
transacciones posteriores del mismo owner esperan el handoff, de modo que la
actividad de journal no puede privar indefinidamente al heartbeat. La
renovación sigue validando owner, generation y expiry bajo el mismo lock de
fichero; una generación expirada o sustituida no puede ampliarse ni escribir.

`TestOwnerProgressRenewsBeforeQueuedStorePressure` usa reloj y waiter
controlados, bloquea una transacción, encola el heartbeat y añade 32 lectores
sin sleeps. Confirma que la renovación obtiene el siguiente turno, el owner
progresa más allá de la expiración original, otro handle rehidrata el
checkpoint exacto y la generación antigua no puede renovarse después del
reclaim. Los dos gates históricos de heartbeat se hicieron también
deterministas para que la carga del filesystem no cambie cuándo se induce la
pérdida de autoridad.

### Evidencia final REV-24

- `TestLocalJournalRehydratesCompletedBatchAcrossHandles -count=200`: pasa.
- `go test -count=50 ./internal/replanning`: pasa.
- Adversarios controlados de presión/configuración y pérdida de lease: pasan
  500 repeticiones; el adversario nuevo pasa 200 repeticiones aisladas.
- Suite combinada afectada `-count=20` sobre inspector, catálogo, workflow,
  GitHub, replanning y CLI: pasa.
- Fencing multiproceso, presión y generación obsoleta con `-race -count=20`:
  pasa con GCC/UCRT64.
- Race afectada `-count=2`: pasa.
- `go test -race -count=2 ./...`: todos los paquetes pasan salvo las dos
  repeticiones del symlink histórico de Windows sin privilegio; no aparece
  ninguna data race.
- Coverage: replanning 85,2 %, goinspector 86,7 %, githubpr 85,5 %,
  validationworkflow 86,1 % y CLI 72,1 %; el manifest de feature 22 pasa.
- `go vet ./...`, compile-only global, `gofmt -l .` y `git diff --check`:
  pasan.

T11–T12 y R10/R12/R13 quedan revalidados. Se solicita décima revisión
independiente.

## Resultado de undécima corrección — REV-25

REV-25 queda corregido sin alterar REV-1..REV-24. `OwnershipConfig.Validate`
delega la relación temporal en `validOwnershipEnvelope`: valida primero las
duraciones positivas, demuestra mediante división que
`2 × StoreOperationTimeout + RenewInterval` cabe en `time.Duration` y solo
después calcula la suma y exige `LeaseTTL` estrictamente mayor. Una envolvente
no representable se rechaza en vez de aceptar un valor envuelto.

`TestOwnershipConfigDurationBoundaries` cubre ceros, negativos,
`MinInt64`, `MaxInt64`, `MaxInt64/2`, overflow en la suma de renovación,
igualdad exacta y el sucesor `+1` cuando todavía es representable.
`TestOwnershipConfigValidationMatchesArbitraryPrecision` contrasta 100.000
entradas del dominio `int64` con un oráculo `big.Int`, y
`FuzzOwnershipConfigValidateNeverPanics` conserva esa equivalencia para todos
los bit patterns generados. `TestNewCoordinatorRejectsOverflowingOwnershipConfig`
confirma el rechazo en composición y la aceptación del mayor caso válido.

La auditoría de aritmética temporal no encontró otra suma o multiplicación
variable en TTL, intervalo o timeout: `time.Time.Add` recibe una duración ya
validada, `Time.Sub` satura en los extremos, y `context.WithTimeout`/
`time.NewTimer` reciben valores positivos sin recomponer duraciones. Las
constantes productivas son representables y también atraviesan la nueva
validación.

### Evidencia final REV-25

- Focal de fronteras y composición `-count=1000`: pasa.
- `go test -count=50 ./internal/replanning`: pasa.
- Suite combinada afectada `-count=20` sobre inspector, catálogo, workflow,
  GitHub, replanning y CLI: pasa.
- Fencing multiproceso, presión y generación stale `-count=20`: pasa; el mismo
  gate con `-race -count=20` pasa con GCC/UCRT64.
- Race afectada sobre los seis paquetes `-count=2`: pasa.
- `go test -race -count=2 ./...`: no detecta carreras ni fallos nuevos; conserva
  únicamente las dos repeticiones del symlink histórico sin privilegio en
  `internal/localvalidation`.
- `scripts/verify-feature22-coverage.ps1`: pasa; replanning alcanza 85,3 %.
- `go vet ./...`, compile-only global, `gofmt -l` sobre el diff y
  `git diff --check`: pasan.

T11–T12 y R10/R12/R13 quedan revalidados. Se solicita undécima revisión
independiente sin modificar `feature_list.json`.

## Resultado de duodécima corrección — REV-26

REV-26 elimina los deadlines fijos de un segundo de `terminal` y
`recoverCancellation`. `OwnershipConfig` incorpora `FinalizeTimeout` y lo
valida dentro del mismo envelope overflow-safe: debe cubrir como mínimo
`2 × StoreOperationTimeout + RenewInterval` y ser estrictamente menor que
`LeaseTTL`. Producción compone 30 s de TTL, 5 s de renovación, 10 s por
operación de store y 25 s de finalización; los tests componen 120 s, 10 s,
30 s y 70 s. Ningún caller puede ocultar un presupuesto de cierre menor que
la operación de store autorizada.

El heartbeat usa ahora un contexto independiente de la cancelación del caller
y permanece unido al lifecycle hasta terminar terminal/recovery y release. La
cancelación sigue propagándose al trabajo normal mediante `ownedCtx`; una
pérdida real de lease también lo cancela. `appendProjected` permite que
recovery reutilice el prefijo ya leído y proyectado, de modo que el presupuesto
validado cubre exactamente una lectura y un CAS durable sin una tercera
transacción redundante. Cada transacción conserva su propio
`StoreOperationTimeout` y vuelve a comprobar owner, generation y expiry bajo el
lock del store.

`TestModelErrorFinalizationRenewsLease` bloquea el CAS terminal con barreras,
avanza solo el reloj inyectado y confirma una renovación antes de publicar el
terminal de dependencia. `TestCancellationRecoveryRenewsAndFencesStaleOwner`
fuerza un resultado post-commit incierto: la rama con progreso renueva y
publica un único terminal cancelado; la rama sin progreso deja expirar la
lease, permite reclaim y demuestra que la generación stale no puede terminar.
`TestFinalizeTimeoutEnvelopeBoundaries` cubre cero, justo por debajo del
envelope, la frontera válida, igualdad con TTL y exceso, sin sleeps.

### Evidencia final REV-26

- `TestDurableRunLeaseRecoversCancellationErrorAndExpiry/model_error
  -count=1000`: pasa en 99,95 s.
- Focales deterministas nuevos `-count=100` y `-race -count=100`: pasan.
- Suite combinada estándar afectada `-count=20`, con paralelismo normal: pasa
  en inspector, catálogo, workflow, GitHub, replanning y CLI.
- `TestCorrectionCancelJoinsAndPreservesParent -count=100`: pasa; su variante
  `-race -count=100` también pasa.
- Race afectada sobre correction y los seis paquetes feature 22 `-count=2`:
  pasa.
- `go test -race -count=2 ./...`: no detecta carreras ni timeouts adicionales;
  conserva únicamente las dos repeticiones del symlink histórico sin
  privilegio en `internal/localvalidation`.
- `scripts/verify-feature22-coverage.ps1`: pasa; replanning alcanza 85,5 %.
- `go vet ./...`, compile-only global, `gofmt -l internal/replanning` y
  `git diff --check`: pasan.

La auditoría no encuentra otro timeout fijo de cierre en replanning. El cierre
GitHub usa `CloseTimeout` compuesto y el timeout de shutdown del proceso CLI es
un presupuesto de servidor ajeno al ciclo durable de feature 22. T11–T12 y
R10/R12/R13 quedan revalidados. Se solicita duodécima revisión independiente
sin modificar `feature_list.json`.
