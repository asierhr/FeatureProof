# Fuentes de contrato independientes de OpenAPI — Tasks

- [x] T1. Definir el modelo neutral, versionado e inmutable del catálogo. [R1, R3, R4, R12]
  - Escribir tests de versiones, enums cerrados, validación all-or-nothing,
    deep-copy y errores seguros antes de la implementación mínima.
  - Modelar operation keys, claims estructurales, provenance, source summaries,
    confidence, completeness, conflicts, omissions y digest sin importar tipos
    de OpenAPI u otros adapters.
  - Añadir comentarios sistémicos a cada entidad, función, método y helper nuevo
    o modificado, incluidos los tests.

- [x] T2. Implementar el registry y la frontera `ContractSource`. [R2, R3, R10, R12]
  - Probar selección exacta por kind, source IDs/digests duplicados, aislamiento
    concurrente, cancelación y ausencia de estado parcial.
  - Validar trusted metadata y límites antes de invocar cada source; impedir que
    un snapshot eleve clase o confianza.
  - Usar spies que fallen si se intenta acceder a red, credenciales, procesos o
    tools.

- [x] T3. Adaptar el inspector OpenAPI al `SourceSnapshot` neutral. [R2, R5, R10, R12]
  - Escribir tests contractuales con fixtures 3.0/3.1 JSON/YAML, referencias,
    parameters, schemas, effective security, responses y omisiones.
  - Traducir solo desde el `openapi.Contract` ya validado; conservar límites,
    safe locations y response validation existente.
  - Añadir fixtures/fakes seguros para sources derived, observed y declared sin
    implementar análisis, captura o ejecución externos.

- [x] T4. Construir el merge canónico con procedencia y conflictos explícitos. [R3, R4, R6, R7, R10, R12]
  - Probar permutaciones de source/input/map order y exigir catálogo+digest
    idénticos.
  - Coalescer claims estructuralmente iguales conservando todas las procedencias;
    agregar completeness de forma conservadora.
  - Detectar conflictos de method/path, schema, security y response sin
    sobrescribir ni resolver por prioridad; probar límites y cancelación en cada
    traversal relevante.

- [x] T5. Implementar la policy explícita de suficiencia. [R4, R7, R8, R12]
  - Probar perfiles con clases/confianzas/completitud distintas y los tres
    resultados cerrados: eligible, insufficient context y review required.
  - Asegurar que conflicts o campos obligatorios insuficientes bloquean la
    operación y que una operación independiente sigue siendo seleccionable.
  - Rechazar configuración parcial y ausencia de policy; no añadir defaults.

- [x] T6. Implementar la reducción neutral, relevante y acotada. [R5, R9, R10, R12]
  - Probar selección constraint→operation, schemas alcanzables, exclusión de
    contenido no seleccionado, orden canónico y tratamiento como untrusted data.
  - Omitir unidades completas ante límites, mantener omission metadata y hacer
    insuficiente una selección truncada.
  - Construir el retained index con provenance, confidence, completeness y
    digests exactos; probar canarios y cancelación.

- [x] T7. Migrar `agentplanning` al contexto neutral. [R1, R9, R11, R12]
  - Añadir primero tests de frontera que impidan imports/tipos OpenAPI en la API
    pública y tests de protocolo/materialización contra retained claims.
  - Sustituir request, payload, provenance, fingerprint y preparación adicional
    por el catálogo neutral, manteniendo validación estructural, límites y
    defensas contra prompt injection.
  - Actualizar fixtures existentes sin debilitar assertions y demostrar que no
    se puede materializar una operación omitida o inelegible.

- [x] T8. Migrar aprobación y replanning al binding contractual neutral. [R8, R11, R12]
  - Probar que cambios en catalog, policy, retained claims, provenance,
    confidence, completeness o conflicts invalidan approval/replanning.
  - Renombrar proyecciones y razones específicas de OpenAPI donde sean públicas
    internamente, manteniendo compatibilidad de estados y CAS.
  - Verificar que contexto insuficiente o review required no crea autorización
    ejecutable.

- [x] T9. Integrar catálogo y policy en la admisión y revalidación del workflow. [R8, R11, R12]
  - Probar que sources se cargan y evalúan antes de inferencia, y que fallos no
    crean plan, approval ni tool calls.
  - Sellar catalog+policy digests en admission/artifact y revalidarlos antes de
    ejecución; cubrir drift entre ambas fases.
  - Conservar idempotencia, recuperación, cancelación y redacción del workflow.

- [x] T10. Actualizar la composición OpenAPI existente. [R1, R5, R11, R12]
  - Componer el workflow actual mediante registry, adapter OpenAPI y policy
    explícita equivalente, sin pedir al usuario un catálogo manual.
  - Probar paridad funcional CLI/API del flujo OpenAPI y que `validate --plan`
    conserva exactamente su contrato histórico.
  - Verificar que composición, no el core, sigue siendo dueña de roots,
    referencias autorizadas y cualquier valor sensible.

- [x] T11. Ejecutar y corregir regresiones de features terminadas. [R11, R12]
  - Ejecutar suites completas de `openapi`, `agentplanning`, `planapproval`,
    `replanning`, `validationworkflow` y entrypoints afectados.
  - Mantener selección, aprobación, plan preparado, ejecución, evidencia y
    resultados observables de fixtures existentes.
  - Limitar cualquier extracción adicional al contrato necesario y documentarla
    en el informe de implementación.

- [x] T12. Completar verificación, trazabilidad y revisión de seguridad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Ejecutar formato, compile-only, `go vet`, tests, tests repetidos de
    determinismo/concurrencia, race detector y coverage del código modificado de
    al menos 85 %.
  - Crear `progress/contract-source-abstraction/implementation.md` con mapa
    R→test real, tasks, comandos, coverage, limitaciones y evidencia de ausencia
    de red/credenciales/ejecución.
  - Auditar el diff completo para confirmar comentarios sistémicos en todas las
    entidades, funciones, métodos y helpers nuevos o modificados, incluidos los
    de test.
  - Solicitar review independiente; no marcar la feature `done` desde el rol de
    implementer.
