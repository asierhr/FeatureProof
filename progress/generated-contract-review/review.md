# Novena revisión independiente — Feature 24 `generated-contract-review`

## Veredicto

`approved`

REV-19 queda cerrado. Todo retorno exitoso de publicación pasa por
`confirmPublication`, que revalida el contexto después de que el efecto durable
haya sido sincronizado y verificado. `Inspect` también comprueba el contexto
inmediatamente antes de retornar. Las cancelaciones deterministas del flujo
normal y de collision recovery preservan `CategoryCancelled` y
`errors.Is(context.Canceled)`, conservan el target ya durable y permiten que el
mismo `AttemptID` converja en el retry. No quedan hallazgos abiertos.

## Estado de hallazgos

| Hallazgo | Estado | Evidencia de novena revisión |
|---|---|---|
| REV-1–REV-14 | cerrados, sin regresión | Los paquetes afectados pasan ×10 y bajo race. Las superficies reales, metadata/lineage, filesystem descriptor-relative, crash recovery, CAS/idempotencia y lifecycle GitHub conservan las garantías verificadas en revisiones anteriores. |
| REV-15 | cerrado | Journal y checkpoint mantienen categoría/causa de cancelación en sus fronteras durables y el retry converge. |
| REV-16 | cerrado | Ownership por `flock`/`LockFileEx`, writers lentos, followers y exports idénticos siguen convergiendo sin truncar estado vivo ni dejar temps/intents. |
| REV-17 | cerrado | Las esperas y retries permanecen acotados y cancelables; no se observan deadlocks bajo repetición o race. |
| REV-18 | cerrado | La revalidación posterior a ownership impide que un follower cancelado promueva marker+temp; el retry recupera el mismo intento. |
| REV-19 | cerrado | `confirmPublication` cubre el flujo normal, target ya confirmado, resultado de `awaitCommittedTarget`, collision cleanup+sync y commit final. `Inspect` tiene su revalidación final. Las dos reproducciones nuevas pasan ×500 y bajo race ×100. |

## Auditoría de retornos exitosos

- `publish` con marker previo llama a `recoverPrepared`, limpia el intent y vuelve
  a pasar por `confirmPublication`.
- El flujo normal sincroniza `directory_synced`, verifica el descriptor y digest
  del target, y termina en `confirmPublication`.
- `recoverPrepared` usa `confirmPublication` cuando encuentra un target ya
  correcto, cuando adopta el resultado de `awaitCommittedTarget`, después del
  cleanup+sync de una collision y después del commit/sync/verify propio.
- `awaitCommittedTarget` revalida `ctx` después de comprobar el digest y antes de
  devolver `true`; su caller confirma de nuevo antes de éxito.
- `Inspect` comprueba `ctx` al entrar, antes de cleanup y de nuevo tras verificar
  marker/digest, inmediatamente antes del retorno exitoso.

`TestPublishCancelsAfterDirectorySync` cancela con `AttemptID` real exactamente
en `directory_synced`: el primer caller obtiene `CategoryCancelled`, conserva
`errors.Is(context.Canceled)` y no recibe target como éxito; los bytes ya
committed permanecen íntegros y el retry adopta el mismo target. La variante
`TestPreparedRecoveryCollisionCancelsAfterDirectorySync` materializa el target
competidor antes del commit, fuerza la rama de collision, cancela después de
cleanup+sync y demuestra la misma convergencia idempotente.

## Verificación reproducida

```text
go test ./internal/contractreview -run 'Test(PublishCancelsAfterDirectorySync|PreparedRecoveryCollisionCancelsAfterDirectorySync)$' -count=500 -timeout=10m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./internal/contractreview -run 'Test(PublishCancelsAfterDirectorySync|PreparedRecoveryCollisionCancelsAfterDirectorySync)$' -count=100 -timeout=10m
go test ./internal/contractreview -run 'Test(LocalPublisherOwnershipCancellation|LocalStoreCancellationAfterCreationReservationRecovers|LocalStoreCancellationAtDurableCASBoundaries|LocalPublisherConcurrentIdenticalAttemptConverges|ServiceConcurrentIdenticalExportsReturnOneReceipt|PreparedRecoveryWaitsForConcurrentRename|PreparedRecoveryCancelsAfterOwnershipAcquired|PreparedRecoveryOwnerReleaseRacesFollowerCancellation|PreparedRecoveryCleansLinkedTempAfterCommittedTarget|PublishCancelsAfterDirectorySync|PreparedRecoveryCollisionCancelsAfterDirectorySync)$' -count=50 -timeout=10m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./internal/contractreview -run '<conjunto anterior>' -count=5 -timeout=10m
go test ./internal/contractreview -run 'Test(LocalPublisherCrashMatrix|DurableCollisionReconciliation|PreparedRecoveryWaitsForConcurrentRename|PreparedRecoveryCommitsBoundTemp|LocalPublisherRecoversAfterTempSyncFailure|PublicationIntentValidationAndCleanup)$' -count=10 -timeout=10m
go test ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof -count=10 -timeout=12m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof -count=1 -timeout=15m
PATH=C:\msys64\ucrt64\bin;%PATH% go test -race ./... -count=1 -timeout=20m
go vet ./internal/contractreview ./internal/contractcatalog ./internal/validationworkflow ./internal/githubpr ./cmd/featureproof
go test ./... -run '^$' -count=1 -timeout=10m
GOOS={windows,linux,darwin} GOARCH=amd64 go test -c ./internal/contractreview
GOOS={windows,linux,darwin} GOARCH=amd64 go build ./cmd/featureproof
go test ./internal/contractreview -coverprofile=<temp> -count=1
go test ./internal/validationworkflow -coverprofile=<temp> -count=1
gofmt -l <Go modificados/nuevos>
git diff --check
go test ./... -count=1 -timeout=15m
```

Resultados:

- pruebas de REV-19 ×500 y race ×100: pasan;
- conjunto REV-15–REV-19 ×50: pasa en 143,2 s; race ×5: pasa en 50,3 s;
- recovery/crash ×10: pasa;
- paquetes afectados ×10: pasan. Una primera ejecución mostró un único timeout
  de cierre en `TestHeadChangeStalesOldCheckWithoutStartingNewRun`; el test pasó
  después ×50 aislado y la repetición completa ×10 pasó, sin reproducción;
- race de paquetes afectados: pasa;
- vet, compile-only, formato y diff-check: pasan;
- paquete y binario compilan para Windows, Linux y Darwin;
- cobertura `internal/contractreview`: **85,2 %**;
- cobertura `internal/validationworkflow`: **85,6 %**;
- las suites global y global race solo reproducen el fallo ambiental histórico
  `internal/localvalidation/evidence_symlink`: Windows no concede el privilegio
  para crear el symlink. Todos los paquetes afectados por Feature 24 pasan.

## Trazabilidad final

| Requirement | Estado |
|---|---|
| R1 | cubierto: identidad canónica, inmutabilidad, límites y provenance |
| R2 | cubierto: baseline aprobada, actual y compatible |
| R3 | cubierto: diff estructural determinista e incertidumbre explícita |
| R4 | cubierto: decisiones completas/selectivas y proyección |
| R5 | cubierto: autoridad, actor y auditoría durable |
| R6 | cubierto: revalidación de todas las dimensiones de drift |
| R7 | cubierto: autoridad acotada y ausencia inconclusa |
| R8 | cubierto: OpenAPI 3.1 determinista, validado y auditable |
| R9 | cubierto: publisher atómico, fail-closed, recovery y cancelación final |
| R10 | cubierto: CAS, idempotencia y concurrencia multiproceso |
| R11 | cubierto: límites, redacción, categorías y propagación de cancelación |
| R12 | cubierto: paridad CLI/API/MCP/GitHub sobre el mismo servicio |
| R13 | cubierto: trazabilidad, comentarios y gates reproducibles |

T1–T13 están marcadas y materialmente completas. El reviewer aprueba Feature 24;
corresponde al leader realizar la transición final de estado y registrar el
historial.
