# Review — preview-environment-adapter

## Veredicto

**approved**

Fecha: 2026-10-17  
Reviewer: `reviewer_feature_19`  
Alcance: sexta revisión independiente de REV-21, regresiones y trazabilidad completa R1–R11 / T1–T12.

## Precondiciones

- `preview-environment-adapter` es la única feature `in_progress`.
- La spec conserva aprobación humana, T1–T12 están `[x]` y el informe del implementer declara `ready_for_review`.
- El diff permanece limitado a la feature y sus fronteras aprobadas de CLI, externalagent y GitHub; no quedaron artefactos de verificación.

## REV-21 — cerrado

La corrección elimina la ventana terminal→cleanup encontrada en la quinta revisión:

- `Supervisor.run` ejecuta `ReleaseOwner` antes de retirar el worker del mapa y cerrar `entry.done`. Mientras la liberación está bloqueada, el worker sigue registrado.
- `Service.Cancel` llama a `Supervisor.Wait` aunque `ReserveCancel` ya devuelva un registro terminal. Tras el join vuelve a leer el registro durable antes de proyectar la respuesta.
- Si no existe worker local, `Wait` retorna de forma idempotente; los replays terminales sin worker siguen cubiertos por la suite de contrato.
- Si el contexto del caller se cancela durante el join, la espera retorna un `AppError` retryable y no bloquea indefinidamente; el worker continúa su cleanup poseído.
- `TestCancelJoinsTerminalWorkerThroughLeaseRelease` fuerza exactamente el estado terminal persistido con `ReleaseOwner` bloqueado y comprueba que `Cancel` no retorna, que el worker continúa registrado y que lease y registro se liberan antes del retorno.
- `TestTerminalWorkerJoinHonorsCallerCancellation` sitúa una barrera después de persistir la intención de cancelación y prueba el límite del contexto.

Los tests del interleaving REV-21, join y finalización pasan 1.000 repeticiones; todas las pruebas cuyo nombre comienza por `TestCancel` pasan otras 1.000. El conjunto afectado pasa `-race -count=2`.

## Trazabilidad R1–R11

| Requirement | Resultado | Evidencia revisada |
|---|---|---|
| R1 | pass | Selección explícita, catálogo y roots CLI/MCP/GitHub configurados; composición incompleta falla cerrada. |
| R2 | pass | Intent previo al ACK, handoff, reserva, snapshot canónico, CAS, lease y recuperación durable. |
| R3 | pass | Atestación exacta y rechecks antes de batch y publicación. |
| R4 | pass | Target canónico y policy forman parte del binding; denegación ocurre antes de DNS/socket y se conservan controles HTTP. |
| R5 | pass | Filtrado secretless previo a resolución; secretos confinados al transporte y canarios ausentes de estado/artefactos/telemetría. |
| R6 | pass | Readiness, deriva e identidad estable se validan con waiter/reloj controlados. |
| R7 | pass | Presupuestos de provider, release y persistencia están separados; cancelación y shutdown unen workers. REV-21 demuestra join hasta liberar ownership. |
| R8 | pass | Error pre-admission y terminal `inconclusive`/`cancelled` post-admission se conservan en externalagent y GitHub. |
| R9 | pass | `release_pending`, Reconcile→Release, claves estables, reclaim y CAS final convergen sin duplicar cleanup. |
| R10 | pass | El contrato provider solo expone Reserve/Reconcile, Readiness y Release lógico; no contiene operaciones destructivas. |
| R11 | pass | CLI, MCP, externalagent y GitHub comparten lifecycle; coverage mínimo, race y verificador AST de comentarios pasan. |

## Trazabilidad T1–T12

T1–T12 están completas y conservan tests observables para sus requirements. En particular, T10 ya cumple cancel/join en la frontera externalagent incluso si el registro durable alcanzó terminal antes del cleanup local, y T12 pasa las verificaciones dirigidas, repetidas, race, coverage y comentarios sistémicos.

## Verificación ejecutada

| Comando o grupo | Resultado |
|---|---|
| REV-21 + join + finalización externalagent `-count=1000` | pass |
| todas las rutas `TestCancel*` externalagent `-count=1000` | pass |
| recovery/handoff/checkpoint/CAS previewenv `-count=200` | pass |
| entrypoints configurados CLI/MCP/GitHub `-count=200` | pass |
| paquetes afectados `-count=20` | pass |
| paquetes afectados `-race -count=2` con GCC/UCRT64 | pass |
| `TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore -race -count=50` aislado | pass |
| suite global comparable sin `internal/localvalidation` | pass |
| race global comparable sin `internal/localvalidation` | pass |
| `go test ./... -count=1` | solo falla la excepción ambiental preexistente del symlink Windows |
| `go vet ./...`, compile-only global, `go mod verify`, build nativo | pass |
| builds windows/amd64, linux/amd64 y darwin/amd64 | pass |
| gofmt de Go modificado/nuevo y `git diff --check` | pass; únicamente avisos LF→CRLF del checkout |
| comentarios sistémicos y pruebas verticales/operacionales | pass |

Una primera ejecución del race global se lanzó a la vez que el stress de 1.000 iteraciones y builds cruzados; esa sobresuscripción agotó el deadline de pared de un test histórico de GitHub. Repetido aisladamente, el test pasa 50 veces con race y el race global comparable pasa completo, por lo que no constituye un defecto reproducible de la feature.

## Coverage

- `internal/previewenv`: 85,4 %.
- `internal/externalagent`: 87,4 %.
- `internal/githubpr`: 85,0 %.

## Limitación ambiental

`go test ./...` reproduce únicamente `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`: Windows rechaza crear el symlink sin el privilegio requerido y el helper termina el parent test. No se modificó ni debilitó esa prueba. La suite normal y race comparables, excluyendo solo `internal/localvalidation`, pasan.

## Resultado

REV-21 queda cerrado y no quedan hallazgos bloqueantes. La implementación satisface R1–R11 y T1–T12 con evidencia ejecutada de durabilidad, cancelación, join, recuperación, seguridad y composición productiva.
