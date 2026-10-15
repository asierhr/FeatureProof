# Quinta revisión independiente — plan-approval

## Veredicto

`approved`

La feature es la única en `in_progress`, la spec conserva aprobación humana,
T1–T9 están completas y existe informe del implementer. No quedan hallazgos
bloqueantes.

REV-9 queda corregida. `LocalStore` elige un owner por run mediante un mutex
global limitado a consultas y actualizaciones de los mapas `loaded/loading`.
Recovery, apertura, lectura, decode, proyección y limpieza ocurren después de
liberar ese mutex. Los waiters del mismo run observan un canal de finalización
con cancelación; un error o cancelación del owner elimina el estado in-flight y
permite un retry limpio. Runs diferentes del mismo root progresan de forma
independiente. La suite bajo race confirma que la publicación de `state.err`,
el cierre del canal y el prefijo en memoria no presentan carreras.

## Estado de REV-1–REV-9

| Hallazgo | Estado | Evidencia |
|---|---|---|
| REV-1 | corregido | `ExecutionComposition` fija modo, aggregate, gate y `PlanExecutor` real; no hay efectos antes del grant y disabled conserva el flujo previo. |
| REV-2 | corregido | Provenance privada de feature 10 rechaza plan antiguo con constraints editadas y acepta el plan regenerado. |
| REV-3 | corregido | Recovery proyecta en temporal y publica solo el stream completo; error/cancelación permiten retry sin contaminación. |
| REV-4 | corregido | Append local valida write/short write/sync/close/rename y conserva un prefijo recuperable. |
| REV-5 | corregido | `Project` exige schema cerrado completo por tipo de evento. |
| REV-6 | corregido | Propietario único por run, aggregate no sustituible, reserva tokenizada y alineación journal/run bajo concurrencia, fallo y replay. |
| REV-7 | corregido | Hashes SHA-256 minúsculos exactos y timestamp UTC obligatorio con fixtures positivos y negativos. |
| REV-8 | corregido | Lifecycle no conserva mutex durante hashing, sanitizer, store, callbacks ni `PlanExecutor`; pruebas bloqueantes y reentrantes terminan sin deadlock. |
| REV-9 | corregido | Carga coordinada por run sin I/O bajo `loadMu`; progreso entre runs, owner único, waiter cancelable y retry del owner están probados. |

## Verificación específica de REV-9

- `claimLoad` y `finishLoad` mantienen `loadMu` únicamente durante secciones
  cortas sobre mapas y señales; ninguna frontera de filesystem se ejecuta bajo
  ese lock.
- `TestLocalLoadsForDifferentRunsProgressIndependently` bloquea el owner de
  run A y demuestra que run B completa desde otro handle del mismo root.
- `TestSameRunCallersShareOneLocalLoad` demuestra una sola carga y el mismo
  prefijo publicado para dos callers concurrentes.
- `TestCancelledLocalLoadWaiterLeavesOwnerAndRetryStateUntouched` demuestra que
  un waiter cancelado no roba ni reinicia el owner.
- `TestLocalLoadOwnerFailuresPermitCleanRetry` cubre error y cancelación del
  owner, retry posterior y ausencia de prefijo parcial o duplicado.
- Estas pruebas y todas las regresiones pasan con detector de carreras repetido
  veinte veces.

## Trazabilidad R1–R12

| Requirement | Resultado | Tests/evidencia principal |
|---|---|---|
| R1 | verificado | Staging solo de `PreparedPlan`, snapshot/hashes, transición a awaiting, cero ejecución previa y disabled intacto. |
| R2 | verificado | Vista exhaustiva, determinista, saneada, aislada y fail-closed. |
| R3 | verificado | Acciones cerradas, identidad estable, campos obligatorios y schema estricto. |
| R4 | verificado | Rehash actual, evento durable con actor/UTC/hashes y grant opaco posterior al append. |
| R5 | verificado | Rechazo terminal/replan sin grant ni ejecución y transiciones correctas. |
| R6 | verificado | Edición invalida; provenance impide reutilizar el plan viejo y exige regeneración validada. |
| R7 | verificado | Regeneración invalida y requiere revisión y decisión nuevas, incluso con contenido equivalente. |
| R8 | verificado | Stale por plan, target, constraints, OpenAPI, capabilities, runtime y generación; canonicalización estable y acotada. |
| R9 | verificado | Grant no forjable, binding exacto, consumo único durable y ejecución real tras el gate sin debilitar policy/budgets. |
| R10 | verificado | CAS/idempotencia, pares concurrentes sobre el mismo aggregate, runs independientes en memoria y carga local independiente por run. |
| R11 | verificado | Journal como fuente de verdad, errores/cancelación fail-closed, prefix recovery, append atómico y retry limpio. |
| R12 | verificado | Tests herméticos, comentarios sistémicos, trazabilidad completa, race y coverage superior al mínimo. |

## Tasks T1–T9

T1–T9 están marcadas `[x]` y la implementación observada satisface su alcance.
No se encontraron cambios de otra feature ni dependencias de red, credenciales,
OAuth o targets reales.

## Verificación canónica fresca

- `gofmt -d` sobre todos los Go modificados y nuevos: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- `go test ./... -count=1`: pass.
- `go test -race ./...`: pass con GCC UCRT64 disponible en `PATH`.
- `go test -race -count=20 ./internal/planapproval ./internal/agentplanning ./internal/harness ./internal/evidence ./internal/planning`: pass.
- `go test -count=1 -coverprofile=coverage_review.out ./...`: pass.
- Coverage global: 91,1 %; `internal/planapproval`: 91,5 %; `internal/agentplanning`: 90,8 %; `internal/harness`: 88,3 %; `internal/planning`: 89,3 %.
- `go mod verify`: pass.
- `git diff --check`: pass, con avisos esperados LF/CRLF.
- Builds `linux/amd64` y `darwin/amd64` con `CGO_ENABLED=0`: pass.

El artifact temporal de coverage creado por el reviewer fue eliminado.
