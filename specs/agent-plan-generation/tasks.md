# Generación agéntica de planes de validación — Tasks

- [x] T1. Definir el protocolo v1 y los modelos de resultado seguros. [R1, R2, R6, R10, R12]
  - Escribir primero tests de decode estricto para `plan` e
    `insufficient_context`, versiones/kinds desconocidos, unión inválida,
    duplicate keys, unknown fields, trailing data y números JSON exactos.
  - Definir request, limits, scenario matrix, checks, evidence requirements,
    missing-context, errores tipados, `PreparedPlan` y terminal record con
    copias defensivas.
  - Añadir comentario de responsabilidad sistémica a toda entidad y función
    nueva o modificada, incluidas las internas y de test.

- [x] T2. Exponer la procedencia exacta del contexto OpenAPI y la normalización canónica. [R3, R4, R12]
  - Escribir tests que demuestren que `ReducedContext` expone solo operaciones
    realmente retenidas y mantiene la relación constraint→operation después de
    omisiones por tamaño y mutaciones del caller.
  - Implementar el índice inmutable/copias defensivas sin exponer DTOs privados
    ni ampliar los bytes dados al modelo.
  - Escribir tests de fingerprints estables ante map order/IDs distintos,
    deduplicación first-wins, orden constraint/scenario/declaración y
    revalidación de cobertura.

- [x] T3. Implementar procedencia, materialización y validación semántica sin handles ejecutables. [R2, R3, R4, R5, R7, R12]
  - Escribir tests de cobertura exacta, escenarios, evidencia, endpoint
    inexistente/asignado a otra constraint, capability inventada y rechazo de
    URL, comando, credencial o campo no tipado.
  - Materializar un `planning.ValidationPlan` por check derivando method/path
    HTTP desde `OperationKey`, con expansión y escape de path parameters
    declarados; conservar evidence requirements aparte del plan ejecutable.
  - Adaptar `internal/planning` para validar con un catálogo descriptor-only y
    preservar el constructor/flujo existente.
  - Probar dependencias, referencias, assertions e inputs inválidos antes de
    policy y comprobar que no hay tool/evidence/journal/run handles.

- [x] T4. Construir el generador cancelable y fail-closed. [R1, R2, R5, R6, R8, R10, R12]
  - Escribir tests del orden preflight→modelo→decode→semántica→dedupe→planning→
    registry→policy→observer y de short-circuit en cada fallo.
  - Implementar una sola inferencia por request, límites de bytes/conteos/trabajo,
    checks de context entre unidades y cero retry/replan/reparación automática.
  - Validar contexto insuficiente como resultado de dominio estable, sin
    registry/policy ni plan parcial.
  - Emitir exactamente un terminal de generación seguro; un observer fallido
    impide entregar el plan.

- [x] T5. Añadir catálogo y policy de generación de solo lectura. [R4, R5, R7, R12]
  - Escribir tests de catálogo duplicado/desconocido, contrato de input,
    metadata de efectos y policy default-deny.
  - Definir `PlanIntent` inmutable y evaluar todos los checks en orden estable,
    sin resolver implementaciones; una denegación descarta todo el conjunto.
  - Verificar que la policy de generación no sustituye el control runtime y que
    ningún tipo retornado permite ejecutar directamente.

- [x] T6. Generalizar la sesión de modelo para respuestas JSON estructuradas. [R8, R9, R10, R11, R12]
  - Escribir tests de una llamada bounded que consume las reservas existentes
    de calls/tokens/duration, propaga deadline/cancelación, no reserva replans y
    conserva taxonomía/telemetría segura.
  - Introducir el contrato estructurado neutral y compartir el núcleo de
    admisión/accounting/observación con `Session.Decide`.
  - Mantener firmas y comportamiento del puerto `harness.Model`, incluida su
    clasificación de retries y todas las pruebas previas.

- [x] T7. Adaptar OpenAI Responses al protocolo de planificación sin duplicar transporte. [R9, R11, R12]
  - Escribir tests del request con instrucciones privilegiadas separadas de
    constraints/OpenAPI/catalog como datos no confiables y con canarios.
  - Refactorizar encode/SSE/output/usage compartidos; mantener codecs estrictos
    separados para harness y planning.
  - Probar protocolo cruzado inválido, output/stream limits, terminales fallidos,
    cancelación y exactamente un request con el transporte/OAuth falso.

- [x] T8. Cerrar integración, trazabilidad y verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Añadir un test end-to-end hermético para plan válido con escenarios y otro
    por cada acceptance adversarial principal: endpoint/tool inventado,
    contexto insuficiente y respuesta estructurada inválida.
  - Escanear secretos/injection canaries en errores, records y requests; probar
    cancelación y concurrencia con barreras/context, sin `time.Sleep`.
  - Ejecutar `gofmt -l .`, `go vet ./...`, compilación, `go test ./...`,
    `go test -race ./...`, repeticiones race de paquetes afectados y coverage;
    exigir al menos 85 % en código nuevo/modificado sin reducir el global.
  - Crear `progress/agent-plan-generation/implementation.md` con mapa concreto
    R→test, tasks, comandos, coverage y riesgos antes de solicitar review.
