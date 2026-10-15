# Aprobación humana del plan — Tasks

- [x] T1. Definir estados, acciones, eventos, errores, identidad, revisiones y proyección append-only. [R1, R3, R5, R10, R11, R12]
  - Añadir `awaiting_approval` y probar todas las transiciones permitidas/prohibidas y terminales.
  - Implementar el projector estricto con secuencias, revisiones y enums cerrados.
  - Definir límites explícitos y comentarios de responsabilidad sistémica en entidades, funciones y helpers de test.

- [x] T2. Implementar snapshots aislados y fingerprints canónicos versionados. [R4, R8, R12]
  - Cubrir plan completo y target/restricciones/OpenAPI/capabilities/runtime/contrato de generación.
  - Probar map-order, números, mutation isolation, límites, cancelación y cambios individuales de cada categoría.
  - Verificar que hashes y errores no contienen valores no confiables ni secretos.

- [x] T3. Construir la solicitud inmutable y el staging fail-closed. [R1, R2, R12]
  - Aceptar exclusivamente un `PreparedPlan` y contexto completo ya validado.
  - Confirmar request/revisión mediante CAS antes de exponer `awaiting_approval`.
  - Probar cero llamadas a executor, tools y evidence store, también en errores.

- [x] T4. Implementar la vista exhaustiva con la frontera de saneado existente. [R2, R12]
  - Mostrar constraints, escenarios, steps/dependencies, identidad, capabilities, operaciones, assertions, effects/idempotency y evidence requirements.
  - Probar orden estable, clones y canarios en headers, cookies, paths sensibles, inputs y contexto.
  - Fallar antes de publicar ante un error de saneado; no producir vistas parciales.

- [x] T5. Implementar decisiones, invalidaciones, CAS e idempotencia. [R3, R4, R5, R6, R7, R8, R10, R11, R12]
  - Aprobar solo tras rederivar hashes actuales y persistir identidad/instante/hashes.
  - Implementar rechazo terminal/replan, regenerate y edit como transiciones cerradas sin grant.
  - Añadir store en memoria y local append-only con atomicidad por run, recovery, decision ID idempotente y failure injection.
  - Probar pares concurrentes, runs independientes, revisiones obsoletas, decisiones dobles y streams corruptos sin sleeps.

- [x] T6. Implementar autorización opaca y consumo único en el execution gate. [R4, R9, R10, R11, R12]
  - Mantener plan/context privados y devolver únicamente snapshots clonados.
  - Rechazar grants ausentes, falsificados, mismatched, stale o consumidos antes de policy/presupuesto/tool.
  - Probar que fallo de persistencia/consumo nunca deja salir un plan ejecutable.

- [x] T7. Integrar la puerta con harness/planning sin debilitar controles runtime. [R1, R9, R12]
  - Exigir el grant en la composición con approval enabled y conservar registry, policy, budgets, sanitizer y cancelación existentes.
  - Añadir una prueba de integración que demuestre ausencia de ejecución previa y exactamente un inicio después de aprobación válida.
  - Mantener intacto el flujo preexistente cuando composición declara approval disabled.

- [x] T8. Completar pruebas por requirement, seguridad y concurrencia. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Añadir los tests nominales del mapa de design y tablas de errores/branches.
  - Ejecutar race tests repetidos sobre CAS, idempotencia y consumo del grant.
  - Verificar hermeticidad, ausencia de secretos y al menos 85% de coverage del código nuevo/modificado.

- [x] T9. Registrar trazabilidad y ejecutar la verificación canónica. [R12]
  - Crear `progress/plan-approval/implementation.md` con `R → test`, tasks, comandos, coverage y riesgos.
  - Ejecutar gofmt check, `go vet ./...`, compile-only, `go test ./...`, `go test -race ./...`, repetición race de paquetes afectados y coverage.
  - Confirmar que todos los comentarios describen responsabilidad dentro del sistema y que no se necesita red, credenciales ni target real.
