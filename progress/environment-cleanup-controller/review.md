# Review 6 de environment-cleanup-controller

## Veredicto

`approved`

No quedan hallazgos bloqueantes. El único blocker de review 5 se corrige con
pasadas durables: el cursor separa epoch y continuation, solo avanza de forma
monotónica dentro de una pasada y únicamente un EOF inequívoco autoriza por CAS
el salto atómico a la siguiente epoch con cursor vacío. Así se conservan la
detección A→B→A y el restart con `MaxPages=1`, pero workflows ya escaneados que
terminalizan después y handles nuevos inferiores vuelven a evaluarse sin
starvation, pérdida ni duplicación lógica.

## Precondiciones y alcance

- Feature 34 es la única `in_progress`.
- `requirements.md` conserva aprobación humana explícita.
- T1–T16 figuran `[x]` y existe `implementation.md` actualizado.
- Se inspeccionaron todos los cambios tracked y untracked, la spec completa,
  informes anteriores y las reglas de arquitectura, convenciones y
  verificación.
- El alcance sigue limitado a feature 34: no hay PostgreSQL/queue/workers,
  configuración versionada, tenant/RBAC ni dual-write de features 35–37.

## Cierre de REV-1 de review 5

- `CursorPass` persiste `Epoch` y `Value`; `SaveCursorPass` admite únicamente
  avance lexical estricto en la misma epoch o completion exacto a `epoch+1`
  con cursor vacío. Stale CAS, rewind, repetición, salto y overflow fallan
  cerrados.
- `CleanupPending` decide `Complete` antes de truncar, carga como máximo
  `limit`, conserva páginas filtradas con `Scanned=true` y solo emite
  `Complete=true/Next=""` cuando el conjunto posterior al cursor se agotó.
- El controller reserva todos los intents de la página antes de confirmar la
  completion. Crash previo repite idempotentemente; pérdida de respuesta tras
  el head reabre la epoch siguiente ya confirmada.
- `TestCleanupIntentPaginationAdvancesFilteredPageAcrossControllerRestart`
  recorre `active-a`, `terminal-b`, `PageSize=1`, `MaxPages=1` y reopen por
  página; después terminaliza `active-a`, añade `aaa-new` por debajo del
  watermark anterior y descubre ambos en la epoch siguiente.
- `TestIntentPaginationRejectsEmptyContinuationAndCycles` prueba empty filtered
  page, repetición y A→B→A intra-run y entre reopens sin perder el intent ya
  reservado.
- `TestIntentPassCompletionCrashConverges` prueba `before_write` y
  `after_publish` alrededor del completion, live/reopen y progreso posterior.
- `TestCursorPassRequiresMonotonicEpochTransitions` cubre advance, completion,
  stale CAS, rewind y epoch skip. Los tests fallarían sin la separación
  epoch/continuation y sin el reset autorizado.

## Revalidación de hallazgos anteriores

| Hallazgo | Estado final |
|---|---|
| Capacity receipt-only y redelivery Bind/Confirm | cerrado: scan independiente, autoridad inmutable, crash Bind→Save, divergencia y released probados sin repetir provider I/O |
| Workflow filtered page | cerrado: scan acotado/cancelable, `Scanned` productivo, epochs, restart y cambio active→terminal probados |
| Store/ledger multiproceso | cerrado: receipts inmutables, file lock, fsync, CAS, dos instancias y fault boundaries sin lost updates |
| Claim/event | cerrado: item, lease/fencing y `cleanup_claimed` comparten commit y redelivery conflictiva falla |
| Lifecycle digest | cerrado: conserva commit, selección, tiempos/TTL, demand, receipt e identidades inmutables |
| Kubernetes private authority | cerrado: raw name/UID durable, paginación/restart, CAS y delete Namespace con UID precondition |
| Anchor/head | cerrado: N+1/N o missing hace roll-forward tras validar receipt/cadena/genesis; head adelantado/divergente falla |
| Strict wrappers | cerrado: head/receipt/anchor rechazan unknown, duplicate anidado, null y trailing con EOF estricto |

## R1–R15 y arquitectura

- R1–R3: identidad completa y reopenable; intents de todos los outcomes y
  expiración; cuatro fuentes con páginas/items/ciclos acotados, cursor durable y
  epochs reevaluables.
- R4–R6: TTL inclusivo/fail-closed, orphan solo tras owner/metadata/grace y
  NotFound autoritativos, cleanup con binding integral y uncertain
  Reconcile-only. Kubernetes conserva owner scope y UID exacta.
- R7–R8: retries/backoff/attempts durables, categorías cerradas, leases/CAS/
  fencing/handoff y cancelación. No hay provider I/O ni waits bajo locks.
- R9–R10: admisión antes de Acquire, cuotas agregadas multiproceso y release
  exactamente una vez solo tras absence/`StateReleased` con proof exacta.
- R11–R13: cleanup/status permanecen separados del verdict; evidence/package/
  report no cambian; eventos content-free y append-only cubren discover,
  intent, claim, retry, blocked, confirmación y capacity.
- R14: composición local explícita, caller-owned y sustituible; no hay
  goroutines ocultas, dual-write ni control plane adelantado.
- R15: tests herméticos con reloj/waiter/provider/inventory controlados, matrices
  de restart/crash/concurrencia y cobertura exacta superior al mínimo.
- Los file locks son cancelables y siempre se liberan; filesystem I/O, fsync,
  provider I/O, callbacks y waits ocurren fuera de mutex Go. Head, anchor,
  receipts y directorios se sincronizan antes de éxito.
- No se detectaron secretos, red/cluster real en `go test`, autoridad
  destructiva ampliada, handles raw en status/audit, goroutines filtradas ni
  scope creep. Los comentarios sistémicos y la dirección de dependencias son
  adecuados.

## Gates reproducidos

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos LF/CRLF informativos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -count=20 ./internal/environment/cleanup` | pass, 20/20 |
| workflow epoch/filtered/bounded focal `-count=20` | pass, 20/20 |
| capacity/composición vertical `-count=20` | pass, 20/20 |
| UCRT64 `CGO_ENABLED=1 CC=gcc go test -race -count=1 -p=1 ./...` | pass |
| UCRT64 cleanup completa `-race -count=20` | pass, 20/20 |
| UCRT64 workflow epoch/filtered `-race -count=20` | pass, 20/20 |
| UCRT64 capacity/composición `-race -count=20` | pass, 20/20 |
| coverage global fresca | `37344/43378 = 86,08972290100973 %` |
| coverage exacta de statements productivos del diff | `1812/2130 = 85,07042253521127 %` |
| builds `linux/amd64`, `windows/386`, `darwin/amd64`, CGO off | pass |

Una invocación inicial del focal race de `cmd/featureproof` contenía un typo en
el directorio UCRT64 y no encontró `gcc`; se corrigió el comando y la ejecución
real terminó 20/20. No fue un fallo de código ni se omitió ningún gate.

La feature está preparada para que el leader la marque `done` y registre su
cierre en `progress/history.md`.
