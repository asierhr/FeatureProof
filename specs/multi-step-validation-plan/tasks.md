# Planes de validación de varios pasos — Tasks

- [ ] T1. Definir el modelo interno del plan, referencias y resultados mediante tests. [R1, R2, R3, R6]
  - Añadir tests de construcción/validación para plan, steps, declarations de
    assertions, path segments, value types y estados terminales.
  - Implementar tipos propios sin tags YAML ni semántica de operadores de
    assertions.
  - Añadir errores tipados con categoría y field path; documentar toda entidad
    y función nueva conforme a la convención del design.

- [ ] T2. Implementar la validación completa y el orden topológico estable. [R1, R2, R4, R5]
  - Escribir tests de plan vacío, IDs duplicados, constraint/tool desconocidos,
    dependencies duplicadas/desconocidas/self y assertions incompletas.
  - Escribir tests de ciclos directos/transitivos y demostrar que policy y tools
    reciben cero llamadas ante cualquier plan inválido.
  - Implementar detección de ciclos, cierre transitivo y topological sort con
    declaration order como desempate, sin depender del orden de maps.
  - Verificar el árbol de input y las referencias sin enviar placeholders a los
    validadores de herramientas; conservar `ToolSpec.Validate` para el input
    completamente resuelto.

- [ ] T3. Resolver referencias tipadas exclusivamente desde evidencia confirmada. [R3]
  - Añadir tests de referencias anidadas por field/index, varias referencias en
    un input y copia inmutable del plan original.
  - Añadir tests de source no ancestro, path inválido/ausente, índice fuera de
    rango, type mismatch y evidencia inexistente, corrupta, de otro run o step.
  - Implementar resolución desde evidence IDs terminales del source y devolver
    errores distinguibles sin producir inputs parciales.

- [ ] T4. Ejecutar el plan secuencialmente y propagar resultados/bloqueos. [R3, R5, R6, R7, R9]
  - Añadir tests del orden topológico estable con raíces y ramas, y del orden
    resolver → validar → policy → budget → tool → evidence → evaluator.
  - Integrar registry, policy, presupuestos, retries/idempotencia, sanitizer,
    store y el puerto fake `StepAssertionEvaluator` sin usar un modelo.
  - Añadir tests para `passed`, `failed`, `inconclusive`, agregación y la regla
    que impide `passed` sin evidencia confirmada.
  - Añadir tests donde failed/inconclusive/blocked propagan bloqueo con todas
    las causas directas y las ramas independientes continúan.
  - Cubrir cancelación en cada frontera y confirmar que no se selecciona otro
    step ni se alteran resultados ya confirmados.

- [ ] T5. Ampliar evidencia y journal para auditoría reconstruible del plan. [R8, R9]
  - Añadir `StepID` al origen de la evidencia y verificar que intentos y output
    terminal quedan relacionados con run, plan, step y tool call.
  - Añadir eventos/campos tipados de plan y step, incluidos status, evidence IDs
    y causas estructuradas de bloqueo.
  - Escribir tests que reconstruyan orden, steps ejecutados/omitidos, retries,
    evidencias y resultado usando solo eventos; demostrar que un blocked step no
    tiene inicio de herramienta.
  - Cubrir fallos de journal/store y preservación de eventos/evidencias ya
    confirmados.

- [ ] T6. Verificar integración, trazabilidad y ausencia de regresiones. [R1, R2, R3, R4, R5, R6, R7, R8, R9]
  - Añadir un test de integración con varios steps, referencias tipadas, una
    rama bloqueada y otra independiente usando únicamente fakes locales.
  - Ejecutar `gofmt`, `go vet`, compilación, `go test ./...`, race detector y
    coverage conforme a `docs/verification.md`.
  - Confirmar que YAML v1 y el harness de una sola restricción mantienen sus
    contratos y que la suite no usa red ni modelo real.
  - Registrar en `progress/multi-step-validation-plan/implementation.md` el mapa
    R→test real, comandos, resultados, archivos modificados y limitaciones.
