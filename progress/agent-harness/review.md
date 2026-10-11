# Tercera review de agent-harness

## Veredicto

`approved`

La implementación satisface R1–R11, conserva las correcciones anteriores y
supera todas las verificaciones canónicas. No quedan hallazgos bloqueantes.

## Precondiciones

- `agent-harness` es la única feature `in_progress`.
- La spec contiene aprobación humana válida.
- T1–T7 están marcadas `[x]`.
- Existe `progress/agent-harness/implementation.md` con trazabilidad
  actualizada.
- Los archivos inspeccionados se mantienen dentro del alcance de esta única
  feature.

## Verificación de REV-1, REV-2 y REV-3

### REV-1 — resuelto

`Evidence.Save` y `Evidence.Get` propagan cancelación. El harness termina una
sola vez como `cancelled` y conserva eventos y evidencias ya confirmados.
`TestHarnessCancelsDuringEvidencePersistenceAndPreservesConfirmedRecords` y
`TestHarnessCancelsDuringEvidenceRecoveryAndPreservesConfirmedRecords` bloquean
la operación correspondiente y verifican estado, persistencia y terminal. Ambos
fallarían con el comportamiento anterior, que convertía `Save` en `failed` y
permitía que `Get` terminase `completed`.

### REV-2 — resuelto

Todos los `Journal.Append` obligatorios se confirman. Un fallo detiene trabajo
posterior y se propaga con contexto; `terminalWritten` solo cambia tras un
append terminal exitoso. Los tests de journal ejercitan cada evento obligatorio,
el fallo antes de ejecutar y el primer fallo al confirmar el terminal. Con el
código anterior esos tests continuarían trabajo no auditable o aceptarían un
terminal no confirmado.

### REV-3 — resuelto

`Run.ReserveRetryToolCall` comprueba retry y tool-call budget antes de mutar
ningún contador. Devuelve `retry_budget_exhausted` o `budget_exhausted` y el
harness registra el rechazo y termina `inconclusive` sin solicitar más trabajo.

- `TestRetryAndToolBudgetsReserveAtomically` comprueba que ninguno de los dos
  agotamientos modifica `Consumption`.
- `TestHarnessStopsWhenRetryBudgetIsExhausted` usa `MaxRetries=0` y comprueba
  una sola decisión, una sola ejecución, ausencia de veredicto `passed`,
  rechazo tipado y terminal inconcluso.
- `TestHarnessDoesNotPartiallyReserveWhenToolBudgetIsExhaustedBeforeRetry`
  comprueba que el agotamiento de tool calls no consume retry budget y corta el
  flujo con `budget_exhausted`.

Los tres tests fallarían con la versión anterior, que salía únicamente del
bucle de intento, solicitaba otra decisión y consumía retry antes de descubrir
que no quedaban tool calls.

## Trazabilidad R1–R11

| Requirement | Evidencia revisada | Resultado |
|---|---|---|
| R1 | observación persistida devuelta al fake model | pass |
| R2 | decisión e input inválidos se detienen antes de policy/tool | pass |
| R3 | registry explícito, default-deny y orden policy → ejecución | pass |
| R4 | reserva previa y reserva atómica en retries | pass |
| R5 | evidencia saneada, relacionada, inmutable y recuperable por intento | pass |
| R6 | verdict determinista desde evidencia; claim del modelo ignorado | pass |
| R7 | límite exacto de peticiones y terminación inconclusa | pass |
| R8 | journal ordenado, appends confirmados y terminal único | pass |
| R9 | cancelación en modelo, herramienta y store con confirmados preservados | pass |
| R10 | retries transitorios, seguros, acotados y terminales al agotar budget | pass |
| R11 | fakes, tiempo/espera controlados, filesystem temporal y ausencia de red | pass |

Los tests concretos coinciden con el mapa de
`progress/agent-harness/implementation.md` y ejercitan comportamiento
observable que desaparecería al retirar cada implementación asociada.

## Arquitectura y convenciones

- Se respetan ARCH-001 a ARCH-012: el modelo solo propone; registry, estructura,
  policy y presupuestos controlan la ejecución; ningún `passed` carece de
  evidencia recuperable; verdict y estado pertenecen al harness; journal y
  saneado permanecen en el orden aprobado.
- El harness ejecuta secuencialmente y posee el estado mutable. No crea
  goroutines internas. Las goroutines de tests tienen señal de salida y espera.
- Los contextos se propagan por todas las fronteras bloqueables. No se mantiene
  ningún lock durante I/O, espera o callbacks externos.
- Interfaces, paquetes, errores tipados, nombres y dependencias cumplen
  CONV-001 a CONV-010.
- No hay `time.Sleep`, tests omitidos, secretos, red, modelo real ni cambios de
  otra feature.

## Verificación canónica independiente

Se usó
`GOPATH=C:\Users\asier\AppData\Local\Temp\featureproof-go-toolchain-1.25.1`,
`GOTOOLCHAIN=go1.25.1` y un `GOCACHE` temporal. Para race se usaron
`CGO_ENABLED=1`, `CC=gcc` y `C:\msys64\ucrt64\bin` al inicio de `PATH`.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass en los cinco paquetes |
| `go test -coverprofile=<temp> ./...` | pass |
| `go tool cover -func=<temp>` | pass; total 90.1 % |

Coverage por paquete:

- `internal/evidence`: 87.3 %.
- `internal/harness`: 88.5 %.
- `internal/model`: 100.0 %.
- `internal/policy`: 100.0 %.
- `internal/tools`: 97.4 %.

## Resultado

No quedan hallazgos. La feature puede pasar a `done` mediante el leader.
