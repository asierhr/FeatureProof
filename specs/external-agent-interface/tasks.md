# Tareas — Interfaz para agentes externos

Cada tarea empieza con tests que fallen, implementa el comportamiento mínimo y conserva la trazabilidad indicada. No se ejecutan antes de la aprobación humana.

- [x] T1. Definir DTO v1, estados, errores y esquemas cerrados. [R2, R6, R9]
  - Tests `TestStructuredErrorsAndOutcomes` para categorías, estados funcionales y campos seguros; rechazo de JSON duplicado/desconocido y límites.
  - Fijar serialización canónica y versión del contrato independiente de MCP.

- [x] T2. Implementar identidad local y catálogo de selecciones autorizadas. [R4, R5]
  - Tests `TestScopeAndApprovalGate` y `TestForeignHandleNotFound` con dos principales; `clientInfo` falso no modifica el principal; alias fuera de scope falla antes del ejecutor.
  - Resolver principal y scopes desde frontera SO/configuración protegida, sin rutas/URLs/presupuestos libres en la petición.

- [x] T3. Crear registro durable de runs, handles e idempotencia. [R1, R5, R7]
  - Tests `TestSubmitDurableLifecycle`, `TestConcurrentIdempotency`, `TestRestartReservation` y colisiones/retención con filesystem falso y prueba local multiproceso.
  - Reserva transaccional, transiciones válidas, ownership persistido y reautorización en cada lectura.

- [x] T4. Construir supervisor de ejecución cancelable y compositor aprobado. [R1, R4, R8]
  - Tests `TestCancelJoinsWorker`, `TestCrashRecovery`, timeout/publicación y rechazo fail-closed si faltan atestador/proyector o aprobación; fakes positivos atraviesan policy y budgets.
  - Worker propiedad del proceso, señal local protegida, join, recuperación sin rerun y finalización acotada.

- [x] T5. Proyectar resultado únicamente desde paquete verificado. [R3, R5, R6]
  - Tests `TestResultSafeProjection`, `TestCorruptPackageRejected`, estado `in_progress`, referencias ajenas, strings hostiles y ausencia de rutas/secretos.
  - Usar `ReadPackage`; comprobar binding/run/estado y construir DTO acotado con handles de evidencia.

- [x] T6. Añadir comandos CLI JSON sobre el caso de uso. [R1, R2, R6, R9]
  - Tests `TestCLICapabilities` y comandos submit/status/result/cancel con lector, escritor y runtime inyectados; verificar códigos de salida y un solo JSON por respuesta.
  - Mantener el comando `validate` existente; `submit` conserva proceso supervisor hasta terminal.

- [x] T7. Añadir servidor MCP 2026-07-28 stdio sobre el mismo caso de uso. [R2, R4, R6, R9]
  - Tests `TestMCPDiscoveryAndTools` para `server/discover`, `tools/list`, `tools/call`, `_meta`, esquemas, scopes por petición y rechazo de `initialize`.
  - Validar framing/JSON-RPC, propagar cierre a supervisor, mapear error de protocolo frente a ejecución y emitir `structuredContent` seguro.

- [x] T8. Ejecutar suite contractual compartida y verificación final. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10]
  - `TestCLIAndMCPContractParity`: mismos fixtures de éxito, fallo funcional, inconclusive, ejecución en curso, clave repetida/conflictiva, principal ajeno, cancelación, paquete corrupto y secreto centinela.
  - Correr `go test ./...`; registrar R→test→resultado en `progress/external-agent-interface/implementation.md` durante implementación y revisar diff para no mezclar features.
