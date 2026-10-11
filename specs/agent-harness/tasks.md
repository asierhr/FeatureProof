# Agent harness acotado — Tasks

Las tasks se ejecutan en orden. Cada una comienza escribiendo el test de
comportamiento indicado, implementa el mínimo necesario y deja el resultado
formateado y verificable antes de avanzar.

- [x] T1. Definir el run, sus transiciones y presupuestos explícitos. [R4, R7, R8]
  - Escribir tests de tabla para configuración inválida, transiciones válidas e
    inválidas, reserva atómica de una unidad y rechazo sin saldo negativo.
  - Incluir los casos `TestHarnessReservesToolBudgetBeforeExecution` a nivel de
    componente de presupuesto y `TestHarnessEmitsExactlyOneTerminalEvent` a
    nivel de máquina de estados.
  - Modelar estados terminales, límites y consumos con tipos, sin booleanos
    ambiguos ni dependencias externas.

- [x] T2. Definir el protocolo interno del modelo, herramientas y validación estructural. [R1, R2, R11]
  - Escribir `TestHarnessRejectsMalformedDecisionBeforePolicyAndExecution` con
    spies que demuestren cero llamadas posteriores.
  - Implementar los datos versionados de `ModelRequest`, `ModelDecision`,
    llamada, assertion y observación; añadir el fake model programable y una
    fake tool cancelable.
  - Validar tipo de decisión, campos obligatorios, constraint ID, tool-call ID,
    input y assertion antes de entregar la llamada a policy.
  - Mantener interfaces mínimas en el consumidor y no introducir SDKs, red o
    proveedor real.

- [x] T3. Implementar journal append-only y evidencia local inmutable y saneada. [R5, R8, R10, R11]
  - Escribir `TestFileEvidenceStoreDoesNotRewriteConfirmedEvidence`,
    `TestHarnessPersistsEveryExecutedAttempt` en el nivel del store y tests de
    recuperación, relación de IDs, hash y secuencia monotónica de eventos.
  - Implementar sanitizer, generador de IDs y reloj como dependencias
    controlables; persistir input/output saneados y clasificación de error.
  - Usar directorios temporales y escritura atómica cuando el filesystem lo
    permita; rechazar la reescritura de IDs ya confirmados.
  - Verificar que logs y eventos no reciben datos sensibles sin sanear y que la
    suite de estos paquetes no usa red ni pausas reales.

- [x] T4. Implementar registry, policy, orden de checks y retries seguros. [R2, R3, R4, R10, R11]
  - Escribir `TestHarnessExecutesOnlyRegisteredAllowedTool`,
    `TestHarnessAuditsUnknownAndDeniedTools`,
    `TestHarnessStopsWhenToolBudgetIsExhausted` y
    `TestHarnessRetriesOnlyTransientIdempotentOperationsWithinBudget` con
    spies que registren el orden de invocación.
  - Implementar resolución segura por defecto, validación de input, decisiones
    tipadas `allowed`/`denied`, reserva previa y ejecución con contexto.
  - Implementar clasificación transitoria, retry budget, garantía de
    idempotencia y espera cancelable con reloj controlado.
  - Emitir eventos separados por validación, policy, reserva, intento, rechazo
    y retry; persistir cada intento que alcance la herramienta.

- [x] T5. Implementar assertion mínima y cálculo determinista del veredicto. [R5, R6, R11]
  - Escribir `TestVerdictIsDerivedFromPersistedEvidence`,
    `TestVerdictCannotPassWithoutEvidence` y
    `TestVerdictIgnoresModelClaim` para `passed`, `failed`, `inconclusive` y
    error interno.
  - Recuperar evidencias por ID y verificar relación e integridad antes de
    evaluar la comparación estructurada mínima.
  - Producir resultados de assertion y veredicto con evidence IDs; impedir
    `passed` sin evidencia válida.

- [x] T6. Integrar el ciclo secuencial y cancelable del harness. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Escribir primero `TestHarnessReturnsPersistedToolObservationToModel` como
    recorrido completo con dos decisiones programadas.
  - Añadir `TestHarnessPersistsRelatedEvidenceBeforeUse`,
    `TestHarnessFailsWhenEvidenceCannotBePersisted`,
    `TestHarnessStopsAtConfiguredStepLimit`,
    `TestJournalAuditsDecisionOrder` y
    `TestHarnessPropagatesCancellationAndPreservesConfirmedRecords`.
  - Coordinar las dependencias en el orden: decisión, estructura, registry,
    policy, presupuesto, ejecución, saneado, evidencia, observación, assertion
    y veredicto.
  - Comprobar cancelación antes de nuevo trabajo, no exponer resultados sin
    persistencia confirmada y cerrar cada camino con un único evento terminal.

- [x] T7. Verificar la entrega y registrar trazabilidad real. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Ejecutar formato, análisis estático y la suite completa según
    `docs/verification.md`, sin credenciales, modelo real ni servicios de red.
  - Confirmar con `TestHarnessSuiteUsesOnlyControlledLocalDependencies` o una
    verificación equivalente que todas las fronteras usan fakes o filesystem
    temporal controlado.
  - Registrar en `progress/agent-harness/implementation.md` el mapa
    `R<n> → test concreto → resultado` y cualquier limitación observada.
  - No marcar la feature `done`; entregar resultados al reviewer para la
    comprobación independiente.
