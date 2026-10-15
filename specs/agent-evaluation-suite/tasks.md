# Suite de evaluación del agente — Tasks

- [x] T1. Definir schemas v1, modelos inmutables y loaders locales estrictos. [R1, R2, R11, R12]
  - Escribir primero tests de dataset/case/criteria válidos e inválidos:
    versiones, claves duplicadas, unknown fields, IDs, semver, hashes, paths,
    symlinks, referencias remotas, tamaños, conteos y cancelación.
  - Implementar snapshots y canonical digest con JSON compacto, claves ordenadas
    y aritmética checked; construir `ReducedContext` desde OpenAPI local y
    selección, sin deserializar autoridad privada.
  - Añadir el registry allowlisted de `contract_id` a validators descriptor-only
    y rechazar expectativas incoherentes antes de inferencia.
  - Añadir comentario de responsabilidad sistémica a toda entidad y función
    nueva o modificada, incluidas internas y tests.

- [x] T2. Implementar el runner secuencial y las fuentes replay/live sin efectos. [R3, R10, R11, R12]
  - Escribir tests de orden estable, una llamada máxima por caso, fail-fast
    explícito, zero-call preflight, propagación de context y ausencia de
    executor/tools/target/evidence/journal.
  - Implementar `ReplaySource` sobre respuestas locales con digest y
    `LiveSource` sobre `model.Session`, sin retry/replan ni nuevo provider.
  - Implementar el wrapper que captura el `StructuredRequest` real, conserva
    prompt/version/hash seguros para manifest y descarta payload/output crudo.
  - Probar que live incompleto no resuelve el auth profile y que provider,
    model, profile y todos los budgets carecen de defaults.

- [x] T3. Construir el oracle estructural anti-invención. [R2, R3, R4, R6, R12]
  - Escribir tests de endpoint, capability, identity, credential y evidence
    invention tanto en plan como en rechazo temprano esperado.
  - Comparar solo getters de `PreparedPlan`, provenance retenida, catálogo,
    expectativas y errores tipados; no analizar conclusiones textuales.
  - Emitir findings canónicos con categoría/case/path sin valor externo y
    aplicar hard-fails independientemente de scores agregados.
  - Verificar que valores URL/command/credential no pueden convertirse en
    llamadas ni aparecer en diagnósticos.

- [x] T4. Añadir dataset inicial e invariantes de prompt injection. [R1, R2, R4, R6, R12]
  - Crear `evals/agentplanning/v1/` con manifest, casos, OpenAPI local, catálogos,
    expectativas, replay de prueba y criterios explícitos, todos con hashes.
  - Incluir casos válidos, invalid/rejected, insufficient context, límites y
    canarios distintos en OpenAPI, fragmento de log y fragmento de response.
  - Probar con captura de frontera que cada canario retenido está solo en
    `Untrusted`, que los campos omitidos no cruzan la frontera y que seguir la
    inyección falla por propiedades estructurales.
  - Usar únicamente secretos sintéticos marcados y mantener el dataset libre de
    endpoints/credenciales reales.

- [x] T5. Implementar métricas exactas y evaluación de criterios versionados. [R4, R5, R8, R11, R12]
  - Escribir tests para numerador/denominador/ppm de constraint coverage, plan
    validity, exact operation selection, insufficient-context confusion matrix
    y limit compliance, incluidos denominator cero y overflow.
  - Agregar invention counts por categoría y resultados por caso en orden
    estable; calcular `result_digest` independiente de metadata runtime.
  - Aplicar archivos criteria completos sin defaults: minimum ppm, hard-fails,
    fail-fast, límites y reglas de delta.
  - Probar verdict y códigos 0/1/2/3 sin comparar strings de error.

- [x] T6. Implementar bundles atómicos, confidenciales y verificables. [R7, R8, R11, R12]
  - Escribir tests con filesystem inyectable para create/write/sync/close/rename,
    cancelación y fallos en cada fase; `manifest.json` debe ser último y ningún
    staging incompleto puede ser baseline.
  - Serializar `cases.jsonl`, `metrics.json`, comparison opcional y manifest con
    schemas/versiones/hashes/config segura completos.
  - Excluir profile ID, tokens, payloads, provider bodies y external errors;
    escanear todos los artifacts y errores con canarios sintéticos.
  - Probar reproducibilidad del result digest ante clock, run ID y output path
    distintos.

- [x] T7. Añadir comparación candidata↔baseline por categoría. [R8, R9, R12]
  - Escribir tests de bundle completo/hash-valid, compatibilidad de dataset,
    cases, metric schema, planning protocol y criteria schema.
  - Calcular deltas enteros agregados y por caso, clasificar
    improved/unchanged/regressed y aplicar tolerancias del criteria explícito.
  - Rechazar corrupción, missing cases, drift e incompatibilidad con error
    tipado/exit 2; demostrar que una mejora no salta thresholds absolutos.
  - Publicar `comparison.json` canónico sin copiar payloads de baseline.

- [x] T8. Exponer `featureproof eval agent` como comando separado. [R3, R7, R8, R9, R10, R11, R12]
  - Escribir tests de argumentos obligatorios, paths, source replay/live,
    opt-in, selección/budgets explícitos, baseline opcional, señales y códigos.
  - Mantener parsing/composición en `cmd`; ejecutar reglas mediante
    `internal/agenteval` y reutilizar composición OAuth/Responses de feature 9.
  - Verificar que replay funciona offline y que `go test`, `init`, `TestMain` y
    otros comandos no disparan evals ni provider real implícitamente.
  - Documentar invocation replay y live, formatos, seguridad, artifacts y
    significado de cada exit code sin incluir credenciales reales.

- [x] T9. Cerrar integración, trazabilidad y verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Ejecutar el dataset inicial end-to-end con modelo scripted para pass,
    quality fail, system fail y cancellation; comprobar cero acciones contra
    aplicaciones y artifacts sin canarios.
  - Añadir concurrencia solo a tests de aislamiento de runs separados y usar
    context/canales, sin sleeps; mantener ejecución v1 de cada run secuencial.
  - Verificar que toda entidad/función nueva o modificada, exportada o interna,
    incluida test helper, conserva comentario de responsabilidad sistémica.
  - Ejecutar `gofmt -l .`, `go vet ./...`, compilación, `go test ./...`,
    `go test -race ./...`, repeticiones race de paquetes afectados y coverage;
    exigir al menos 85 % en código nuevo/modificado sin reducir el global.
  - Crear `progress/agent-evaluation-suite/implementation.md` con mapa R→test,
    tasks, comandos, coverage y riesgos antes de solicitar review.
