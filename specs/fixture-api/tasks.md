# Preparación y limpieza de fixtures mediante API — Tasks

- [ ] T1. Definir los modelos internos de specification v3 para fixtures. [R1]
  - Escribir primero tests para fixtures, operaciones, inputs union, referencias,
    capturas, statuses, dependencias e idempotencia.
  - Mantener modelos independientes de YAML, HTTP, stores y material secreto.
  - Añadir comentario sistémico a cada entidad, constante, función, método y
    helper productivo o de test creado o modificado.

- [ ] T2. Implementar el adapter YAML v3 y compatibilidad estricta v1/v2. [R1]
  - Despachar por versión antes del body y rechazar unknown/duplicate fields,
    unions ambiguas, valores inválidos y fixtures bajo versiones anteriores.
  - Añadir fixtures YAML válidos/adversarios y demostrar que v1/v2 conservan
    exactamente su aceptación, rechazo y ausencia de defaults.
  - Probar que errores muestran categoría/field path y nunca valores sensibles.

- [ ] T3. Validar el fixture set, grafo, references y órdenes deterministas. [R2, R3]
  - Escribir tests de unknown/duplicate/self dependencies, ciclos, references
    fuera del cierre transitivo y primer error estable.
  - Crear snapshot inmutable con create order topológico por declaration order y
    cleanup order inverso.
  - Probar deep copies y mutaciones posteriores de inputs/maps/slices.

- [ ] T4. Implementar binding de contrato, identidad e idempotencia. [R2, R5, R6, R10]
  - Unir cada create/cleanup al target, catálogo aprobado/revisión, operación
    estructural exacta, identity binding y garantía de idempotencia.
  - Rechazar catálogos derivados no aprobados, path prefix confusion, operación
    ambigua, identidad implícita y contrato sin garantía idempotente.
  - Sellar un digest canónico sin captures, credential refs, secrets ni keys.

- [ ] T5. Implementar resolución de inputs y preparación secuencial. [R4, R5]
  - Resolver literales/references en copias desde captures confirmadas del mismo
    run y revalidar el request antes de policy/budget/tool.
  - Ejecutar creates en orden estable, aceptar solo statuses declarados y
    bloquear fase principal/dependientes ante el primer fallo de preparación.
  - Probar ramas, referencias anidadas, path parameters y cero trabajo nuevo
    tras un create failed/uncertain.

- [ ] T6. Implementar extracción, saneado y store de captures. [R5]
  - Extraer paths de evidence confirmada, comprobar tipo, truncación, max bytes,
    límite agregado, nombres y sensibilidad antes de confirmar.
  - Persistir values solo en store privado ligado a run/fixture/binding y
    publicar únicamente nombre/type/size/digest.
  - Probar límites exactos, null, numbers, object/array, malformed evidence,
    redaction failure y captures canario nunca publicadas.

- [ ] T7. Adaptar HTTP e identidades para intentos de fixture. [R6, R10]
  - Reutilizar `http.request` con path/input estructurado e inyección interna de
    idempotency key solo tras todos los guards.
  - Resolver y liberar la identidad explícita de create/cleanup por intento,
    revalidando redirects, límites finales y binding antes de network.
  - Probar identidades distintas, retries, rotated credentials, canarios y spies
    de DNS/dial a cero ante cualquier rechazo.

- [ ] T8. Crear lifecycle y cleanup intents durables. [R4, R7, R8, R13]
  - Definir máquina de estados, store consumidor-owned, CAS e intent confirmado
    antes de cada create potencialmente efectivo.
  - Clasificar rechazo pre-I/O frente a create confirmado o incierto tras I/O y
    conservar elegibilidad hasta terminal de cleanup.
  - Añadir tests/crash checkpoints que demuestren cero I/O sin intent y que la
    recuperación no repite un create confirmado.

- [ ] T9. Implementar cleanup inverso con scope, budgets y retries propios. [R8, R9, R10, R13]
  - Recorrer todos los elegibles en orden inverso, continuar tras fallo y
    registrar input unavailable sin inventar captures.
  - Usar `CleanupScopeFactory` con deadline, calls, retries y backoff explícitos;
    revalidar contexto/ownership antes de cada efecto.
  - Probar primary context cancelado, deadline/budget exactos, transient retry,
    permanent no-retry, cancelación simultánea y ausencia de goroutines.

- [ ] T10. Integrar persistencia, recovery y ownership en validation workflow. [R7, R8, R9, R11, R13]
  - Sellar fixture set con run, revisión, target, catálogo, identities, policy y
    plan; revalidar drift/tampering antes de I/O tras restart/redelivery.
  - Recuperar intents/captures/terminales confirmados bajo lease/CAS y converger
    cleanup desde cada checkpoint sin duplicar efectos.
  - Probar dos owners, lease expiry/handoff, store corruption, crashes y
    cancelación durante adquisición/renovación.

- [ ] T11. Separar primary result, fixture summary y cleanup report. [R11, R12]
  - Mantener inmutable el resultado principal y añadir estados/incidencias de
    fixture como sección operacional allowlisted del package/report.
  - Verificar incidencias por fixture/attempt/category, estado partial y fallo
    de persistencia sin falso cleanup success.
  - Probar todos los primary outcomes combinados con cleanup success/failure,
    incluidos failed funcional e inconclusive.

- [ ] T12. Integrar composición local, policy, journal, evidence y redacción. [R6, R12]
  - Construir binder/lifecycle/stores/scopes desde configuración confiable sin
    leer environment en core ni resolver credenciales al cargar la spec.
  - Emitir eventos append-only para binding, intent, attempts, captures por
    metadata y cleanup report; no persistir payloads o external errors.
  - Ejecutar matriz recursiva de canarios sobre state, store, journal, evidence,
    package, report, logs, metrics y model projections.

- [ ] T13. Mantener paridad CLI, API, MCP y GitHub. [R12, R13]
  - Delegar en el mismo workflow y proyectar solo `FixtureSummary` seguro, sin
    endpoints o comandos que acepten fixtures inline o autoridad adicional.
  - Rechazar schemas con URL, tool, shell, SQL, credential/ref, idempotency key
    o claims de permiso antes de admission.
  - Probar resultados/códigos equivalentes, redelivery, payloads desconocidos o
    oversized y texto PR malicioso sin cambios en bindings.

- [ ] T14. Ejecutar gates, trazabilidad y documentación de entrega. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13]
  - Auditar el diff para comentarios sistémicos en entidades, constantes,
    funciones, métodos, DTOs, helpers, fakes y fixtures programáticas.
  - Ejecutar `gofmt`, compile-only, `go vet`, suites focal/completa repetidas,
    race, crash matrix y coverage mínimo del 85 % del código nuevo/modificado.
  - Crear `progress/fixture-api/implementation.md` con mapa real R→test,
    comandos, resultados, coverage, limitaciones y evidencia reproducible.
  - Marcar T1–T14 `[x]` solo con evidencia completa y solicitar review
    independiente sin modificar `feature_list.json`.
