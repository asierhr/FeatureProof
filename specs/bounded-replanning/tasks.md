# Replanificación agéntica acotada — Tasks

- [x] T1. Definir modelos, límites, errores y protocolo estricto v1. [R2, R4, R12]
  - Escribir tests de las tres ramas exclusivas, JSON inválido, campos
    desconocidos, duplicados, tamaños, snapshots y aritmética comprobada.
  - Modelar límites no monetarios y comprobar que no existe coste/precio.
  - Añadir comentarios de responsabilidad sistémica a entidades, funciones y
    helpers de test introducidos o modificados.

- [x] T2. Construir el resumen exacto y redactado para el modelo. [R1, R12]
  - Escribir tests de orden/digest estable, copias defensivas, pertenencia de
    evidencia, truncación, límites y canarios secretos.
  - Reutilizar sanitizer/evidence store y separar instrucciones privilegiadas
    del envelope no confiable.

- [x] T3. Implementar journal, projector, ledger y fingerprint set. [R2, R6, R7, R8, R10, R12]
  - Escribir tests de secuencias válidas/corruptas, replay idempotente,
    conflicto, failure monotónico y reconstrucción de lineage.
  - Probar equivalencia ante renombrado/orden decorativo y diferencia ante
    cambios semánticos.
  - Implementar CAS por run sin mantener locks durante callbacks o I/O externo.

- [x] T4. Preparar batches adicionales mediante las fronteras existentes. [R3, R7, R9, R12]
  - Escribir tests de operación/capability inventada, input/assertion inválida,
    policy denial y aceptación all-or-nothing.
  - Factorizar el mínimo necesario de `agentplanning` para materializar un
    batch ya decodificado sin omitir provenance, fingerprint ni validación.
  - Integrar nueva revisión de `planapproval` y demostrar que el grant inicial
    no autoriza checks posteriores.

- [x] T5. Implementar admisión y presupuestos antes de trabajo. [R4, R5, R10, R12]
  - Escribir tests por dimensión: cycles/replans, agent steps, model calls,
    tokens, tool calls, validation work y duration.
  - Probar último recurso concurrente, overflow, cruce de tokens y que no se
    invoquen auth/model/approval/policy/tool después de agotamiento.
  - Reutilizar `model.Session.ReserveReplan` e `InferStructured`.

- [x] T6. Implementar agregación monotónica y veredicto final. [R6, R11, R12]
  - Escribir tests de precedencia failed/inconclusive/passed/error, resultados
    distintos, replays y claims contradictorios del modelo.
  - Mantener referencias completas de evidencia y causas seguras sin permitir
    sobrescritura de resultados confirmados.

- [x] T7. Implementar el coordinador secuencial end-to-end. [R3, R5, R8, R9, R10, R11, R12]
  - Conectar ejecución inicial, summary, modelo, propuesta, aprobación,
    ejecución adicional, ledger y terminación.
  - Escribir tests de check permitido, policy denegada, approval requerida,
    rechazo/regeneración, repetición, contexto insuficiente, límites y
    cancelación en cada frontera.
  - Confirmar un único terminal y ausencia de goroutines/trabajo huérfano.

- [x] T8. Completar pruebas adversariales, de concurrencia e integración. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Añadir fakes coordinables y seguros para carreras sin `time.Sleep`.
  - Ejecutar carreras entre workers/runs, corrupción de stores, fallos de
    persistencia, secretos canario y repetición de decisiones.
  - Mantener todos los tests herméticos, sin red externa, OAuth, proveedor ni
    credenciales reales.

- [x] T9. Verificar, documentar y preparar revisión independiente. [R12]
  - Registrar en `progress/bounded-replanning/implementation.md` el mapa
    `R<n> → test concreto → resultado` y cualquier adaptación mínima.
  - Ejecutar `gofmt`, `go vet`, compilación, `go test ./...`, race, repetición
    race, coverage global/paquetes modificados, `go mod verify`,
    `git diff --check` y builds soportados.
  - Confirmar al menos 85% statement coverage en cada paquete modificado y
    solicitar revisión sin cambiar `feature_list.json`.
