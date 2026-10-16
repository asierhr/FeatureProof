# Review independiente — bounded-replanning

## Veredicto

**Rechazado.** La feature 13 es la única `in_progress`; la spec registra aprobación humana el 2026-10-16T08:27:07+02:00, T1–T9 están marcadas y existe el informe del implementer. La trazabilidad declarada R1–R12 se contrastó con los tests reales y los archivos modificados. Hay defectos funcionales en R4, R8, R10 y R11 que las pruebas existentes no cubren.

## Verificación independiente

- `go vet ./...` y `go test ./... -count=1`: pass.
- `go test -race ./...` y repetición `-race -count=20` en `internal/replanning`, `internal/agentplanning`, `internal/planapproval` e `internal/evidence`: pass.
- Coverage global: 90,2 %; paquetes modificados: `replanning` 85,5 %, `agentplanning` 88,3 %, `planapproval` 90,3 %, `evidence` 89,8 %.
- `go mod verify` y `git diff --check`: pass. Los archivos Go modificados o nuevos no figuran en `gofmt -l`.
- El gate literal `gofmt -l .` **falla** por numerosos archivos Go preexistentes fuera del diff. No lo atribuyo a esta feature, pero el gate canónico de `docs/verification.md` no queda satisfecho en el checkout actual.

## REV-1 — Un grupo `any` aprobado produce veredicto fallido

- Severidad: blocker
- Requirement o regla: R11, R6, ARCH-004
- Archivo y línea: `internal/replanning/ledger.go:126-155`; `internal/replanning/coordinator.go:474-486`
- Evidencia: `confirmNode` incorpora raíz e hijos del árbol con la misma declaración. `VerdictRequired` devuelve `failed` al encontrar **cualquier** hijo fallido, aunque la raíz de un grupo `any` esté `passed`. `internal/assertion/evaluator.go:90-112,159-198` define expresamente que `any` pasa con un hijo aprobado y otro fallido. Los tests del ledger solo usan nodos sin esta mezcla.
- Impacto: una restricción satisfecha se informa como incumplida, contradiciendo la evaluación determinista ya confirmada.
- Corrección requerida: conservar los hijos para auditoría, pero calcular el resultado requerido desde la raíz de cada declaración; añadir una prueba con `any(passed, failed)` y evidencia válida que termine `passed`.

## REV-2 — El trabajo de fingerprint elude el presupuesto de validación

- Severidad: high
- Requirement o regla: R4, R5, ARCH-005
- Archivo y línea: `internal/replanning/coordinator.go:162-180`; `internal/replanning/fingerprint.go:11-19`
- Evidencia: `Fingerprint` ejecuta la canonicalización del lote antes de `budgets.Reserve(BudgetValidationWork, ...)`. Con `validation_work` agotado se sigue recorriendo la propuesta y creando su digest antes de rechazarla. Las pruebas de presupuesto ejercitan `Reserve` directamente, sin comprobar esta secuencia del coordinador.
- Impacto: se inicia trabajo determinista nuevo tras el agotamiento y el límite global no gobierna todo el trabajo de validación.
- Corrección requerida: admitir y cargar la canonicalización antes de ejecutarla, incluyendo las unidades consumidas, y probar que una propuesta nueva no inicia esa labor cuando no queda presupuesto.

## REV-3 — Un ciclo confirmado parcialmente no admite reintento

- Severidad: high
- Requirement o regla: R10, R8, ARCH-007
- Archivo y línea: `internal/replanning/coordinator.go:68-83`
- Evidencia: tras proyectar eventos, `Run` devuelve `CategoryIntegrity/unfinished_cycle` para **cualquier** prefijo no terminal. Esto incluye un `EventCycleReserved`, `EventSummaryConfirmed` o `EventBatchCompleted` válidos tras interrupción. `MemoryJournal` tiene CAS y claves idempotentes, pero el coordinador nunca reconstruye ni continúa ese trabajo. Los tests cubren replay de entradas individuales y recuperación de cancelación, no reinicio de un ciclo parcial.
- Impacto: un retry autorizado no recupera el prefijo confirmado; un fallo entre etapas deja el run sin posibilidad de avanzar de forma idempotente.
- Corrección requerida: reanudar de forma segura desde la proyección y los stores confirmados, evitando repetir inferencia o tools, y probar reinicio tras fronteras persistidas representativas.

## REV-4 — El journal no vincula las assertions confirmadas

- Severidad: high
- Requirement o regla: R8, R11, ARCH-007
- Archivo y línea: `internal/replanning/coordinator.go:280`; `internal/replanning/journal.go:48,219-230,287-288`
- Evidencia: el evento real `EventAssertionsConfirmed` se anexa sin `AssertionIDs`; `Project` solo valida decisión y batch y no reconstruye identidades ni resultados de assertions. El test `TestLineageReconstructsDecisionEvidenceAndActions` arma manualmente un evento con `AssertionIDs`, que el coordinador no emite. El ledger es únicamente memoria y tampoco se rehidrata desde eventos.
- Impacto: el journal no permite reconstruir qué resultados deterministas se confirmaron para cada lote tras reinicio o auditoría, aunque sí registra el lote y la evidencia.
- Corrección requerida: confirmar en un store auditable las identidades/resultados del ledger y vincularlas al evento de batch; probar la proyección de eventos producidos por `Coordinator.Run` y su reconstrucción.

## Trazabilidad restante

Los tests nombrados en `progress/bounded-replanning/implementation.md` existen y pasan para R1–R3, R5, R7, R9 y R12 en los escenarios ensayados. R6, R8, R10 y R11 tienen tests nominales, pero los casos anteriores muestran que no verifican íntegramente sus criterios de aceptación. No observé acceso a producción, credenciales reales, goroutines nuevas ni I/O bajo locks en el cambio revisado.

---

## Segunda revisión tras correcciones

**Veredicto: rechazado.** REV-1, REV-2, REV-3 y REV-4 quedan corregidos en sus escenarios originales. `TestAnyGroupUsesRootVerdictPreservingFailedChild` y `TestCoordinatorAnyGroupUsesEvaluatedRoot` verifican la raíz del grupo `any`; el fingerprint carga visitas antes de canonicalizar; los nuevos tests reanudan prefijos confirmados sin repetir modelo ni tools; los eventos reales registran y proyectan assertions, y `LocalJournal` permite reconstruirlas entre instancias. Los tests de R1–R12 declarados en el informe existen y pasan. Persisten los siguientes hallazgos nuevos.

### Verificación independiente de esta revisión

- `go vet ./...`, `go test ./... -count=1`, `go test -race ./...` y `go test -race -count=20 ./internal/replanning ./internal/agentplanning ./internal/planapproval ./internal/evidence`: pass.
- Coverage: global 89,8 %; `replanning` 85,3 %, `agentplanning` 88,0 %, `planapproval` 89,1 % y `evidence` 89,8 %.
- `go mod verify`, `git diff --check` y `gofmt -l` limitado a archivos Go modificados o nuevos: pass. El gate literal `gofmt -l .` conserva fallos preexistentes ajenos al diff.

## REV-5 — La restauración omite los contadores deterministas si la sesión ya se usó

- Severidad: high
- Requirement o regla: R4, R5, R10, ARCH-005
- Archivo y línea: `internal/replanning/budget.go:109-123`
- Evidencia: `Restore` sale con `nil` cuando `b.model.Snapshot()` tiene cualquier consumo. En un nuevo `BudgetController` asociado a la misma sesión de modelo tras un retry, `b.used.AgentSteps`, `ToolCalls` y `ValidationWork` quedan a cero aunque el checkpoint durable contenga consumos previos. Los tests de recuperación usan una sesión nueva con snapshot inicialmente vacío.
- Impacto: un retry con sesión conservada puede admitir más pasos agenticos, tools o trabajo de validación que los límites del run.
- Corrección requerida: restaurar siempre los contadores deterministas desde el checkpoint, reconciliar el snapshot de modelo ya existente sin doble cómputo y probar recuperación con sesión de modelo no vacía.

## REV-6 — Una aprobación confirmada vuelve a pedir decisión humana al reanudar

- Severidad: high
- Requirement o regla: R9, R10
- Archivo y línea: `internal/replanning/coordinator.go:373-387,406-412`; `internal/replanning/compose.go:70-98`
- Evidencia: cuando `projection.LastType == EventApprovalResolved`, el coordinador evita `Stage` pero llama igualmente a `c.approval.Wait`. `WorkflowApproval.Wait` vuelve a invocar `Waiter.Wait` y `Workflow.Decide`; con un bridge nuevo carga la vista anterior y vuelve a solicitar una decisión. `TestCoordinatorResumesPreparedAndApprovalPrefixes` comprueba `staged == 1`, pero no exige `waited == 1` ni que el waiter real no se invoque otra vez.
- Impacto: el mismo lote aprobado puede exigir otra interacción humana o quedar bloqueado tras una aprobación ya confirmada, incumpliendo el retry idempotente.
- Corrección requerida: recuperar una autorización vigente y de un solo uso sin volver a pedir decisión humana, o registrar la información de recuperación necesaria; probar `EventApprovalResolved` con `WorkflowApproval` y un waiter que falle si recibe una segunda llamada.

## REV-7 — El journal local mantiene locks durante I/O de disco

- Severidad: high
- Requirement o regla: R10, diseño aprobado «Ningún lock cubre callbacks o I/O», CONV-006
- Archivo y línea: `internal/replanning/local_journal.go:41-80,83-122,138-215`
- Evidencia: `Events`, `CompareAndAppend`, `Checkpoint` y `SaveCheckpoint` adquieren el mutex por run y lo mantienen mientras `load` ejecuta `os.Stat`/`os.ReadFile`, o `publish` ejecuta `os.CreateTemp`, `Write`, `Sync` y `Rename`. La garantía del diseño se aplica también a locks por run; el comentario de `lock` solo excluye el lock global.
- Impacto: una operación de disco lenta o bloqueada inmoviliza todas las transiciones y lecturas de ese run; la implementación infringe expresamente el requisito de concurrencia aprobado.
- Corrección requerida: serializar la reserva/CAS con una sección crítica corta y hacer el I/O fuera del lock, preservando atomicidad entre handles; añadir una prueba que demuestre que un I/O bloqueado no retiene el lock de estado.

---

## Tercera revisión tras correcciones

**Veredicto: rechazado.** REV-5, REV-6 y REV-7 están corregidos en los escenarios denunciados. `Restore` conserva los contadores deterministas con una sesión reutilizada; el coordinador usa `ApprovalRecovery` tras `EventApprovalResolved` y el test con `WorkflowApproval` real confirma que no hay segundo `Waiter.Wait`; `LocalJournal.claim` libera su mutex antes de I/O y mantiene una reserva cancelable por run para el CAS. La trazabilidad R1–R12 y las regresiones anteriores siguen pasando. Queda una infracción del diseño aprobado en la nueva ruta de restauración.

### Verificación independiente de esta revisión

- `go vet ./...`, `go test ./... -count=1`, `go test -race ./...` y `go test -race -count=20 ./internal/replanning ./internal/agentplanning ./internal/planapproval ./internal/evidence`: pass.
- Coverage: global 89,6 %; `replanning` 85,2 %, `agentplanning` 88,0 %, `planapproval` 86,8 % y `evidence` 89,8 %.
- `go mod verify`, `git diff --check` y `gofmt -l` de todos los Go modificados/nuevos: pass. El `gofmt -l .` literal sigue afectado por archivos anteriores fuera del alcance de esta feature.

## REV-8 — `Restore` invoca una dependencia bajo su mutex

- Severidad: high
- Requirement o regla: R10, diseño aprobado «Ningún lock cubre callbacks o I/O»
- Archivo y línea: `internal/replanning/budget.go:113-119`
- Evidencia: `BudgetController.Restore` toma `b.mu` y, antes de liberarlo, llama a `b.model.Snapshot()`. `ModelBudget` es una interfaz inyectable; la llamada puede adquirir otros locks o bloquearse. La corrección de REV-7 eliminó este patrón del journal local, pero permanece en la restauración del presupuesto.
- Impacto: una dependencia lenta o con orden inverso de locks bloquea reservas y snapshots del presupuesto del run; incumple la regla explícita de concurrencia y puede producir deadlock.
- Corrección requerida: obtener el snapshot del modelo fuera de `b.mu`, reconciliarlo bajo una sección crítica breve y probar con una implementación controlada que bloquee `Snapshot` mientras otra goroutine accede al presupuesto.

---

## Cuarta revisión tras correcciones

**Veredicto: rechazado.** REV-8 está corregido: `BudgetController.Restore` marca `restoring` bajo una sección crítica breve, invoca `ModelBudget.Snapshot` después de liberar el mutex, rechaza reservas concurrentes y publica los contadores solo tras validar el snapshot. `TestRestoreDoesNotHoldBudgetMutexDuringModelSnapshot` verifica el bloqueo controlado y la ausencia de estado parcial. REV-1 a REV-7 siguen cerrados en código y tests. La revisión de R1–R12 detecta un incumplimiento documental de R12 en símbolos introducidos por las correcciones.

### Verificación independiente de esta revisión

- `go vet ./...`, `go test ./... -count=1`, `go test -race ./...` y `go test -race -count=20 ./internal/replanning ./internal/agentplanning ./internal/planapproval ./internal/evidence`: pass.
- Coverage: global 89,6 %; `replanning` 85,2 %, `agentplanning` 88,0 %, `planapproval` 86,8 % y `evidence` 89,8 %.
- `go mod verify`, `git diff --check` y `gofmt -l` sobre todos los Go modificados o nuevos: pass. El gate literal `gofmt -l .` sigue enumerando 146 archivos Go preexistentes fuera del diff de esta feature, por lo que permanece como limitación global del checkout.

## REV-9 — Faltan comentarios sistémicos en símbolos nuevos

- Severidad: medium
- Requirement o regla: R12; `specs/bounded-replanning/design.md`, «Toda entidad, función o helper de test nuevo o modificado tendrá un comentario sobre su responsabilidad sistémica»
- Archivo y línea: `internal/replanning/local_journal.go:17`; `internal/replanning/approval_bridge_test.go:42`; `internal/replanning/coordinator_test.go:58`; `internal/replanning/protocol_test.go:189,191`
- Evidencia: `localJournalOwner`, `forbiddenApprovalWaiter.Wait`, `failBeforeEventJournal.CompareAndAppend`, `blockingBudgetSnapshot.ReserveReplan` y `blockingBudgetSnapshot.Snapshot` carecen de comentario propio. La búsqueda de declaraciones `type`/`func` nuevas confirmó estos cinco casos; la mayoría de símbolos restantes sí documenta su responsabilidad.
- Impacto: R12 y la task T1/T9 no están completas según el criterio explícito aprobado, aunque las pruebas funcionales pasen.
- Corrección requerida: añadir comentarios que describan la responsabilidad sistémica de cada símbolo citado y repetir el control de comentarios y los gates sin alterar comportamiento.

---

## Quinta revisión tras correcciones

**Veredicto: aprobado.** REV-9 está corregido: los cinco símbolos citados tienen comentarios propios que describen su responsabilidad. Un barrido de todas las declaraciones top-level `type` y `func` de los Go nuevos o modificados no encontró omisiones. REV-1 a REV-8 siguen cerrados; la trazabilidad y los tests de R1–R12 permanecen conformes, sin hallazgos bloqueantes de esta feature.

### Verificación independiente final

- `go vet ./...`, `go test ./... -count=1`, `go test -race -count=1 ./internal/replanning`, `go mod verify` y `git diff --check`: pass.
- `gofmt -l` de todos los archivos Go nuevos o modificados de la feature: cero rutas.
- La race completa y la repetición ×20 de los cuatro paquetes afectados pasaron en la cuarta revisión; la única modificación posterior fue documental (comentarios de código).
- Cobertura de la cuarta revisión: global 89,6 %; `replanning` 85,2 %, `agentplanning` 88,0 %, `planapproval` 86,8 % y `evidence` 89,8 %.
- `gofmt -l .` literal sigue enumerando 146 archivos preexistentes ajenos al diff por el estado del checkout; ninguno pertenece al cambio de esta feature. El gate dirigido a los archivos afectados pasa.
