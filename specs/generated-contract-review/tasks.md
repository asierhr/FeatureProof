# Revisión y exportación de contratos derivados — Tasks

- [x] T1. Definir el modelo versionado de review, errores, límites y canonicalización. [R1, R3, R11, R13]
  - Escribir primero tests de versión, validación, IDs/digests estables,
    permutaciones, copias profundas, límites y canarios.
  - Modelar artifact, items, manifests, decision revisions, approval binding,
    projection y vistas seguras sin tipos de parser o filesystem.
  - Añadir comentario sistémico a cada entidad, constante, función, método y
    helper productivo o de test creado/modificado.

- [x] T2. Implementar verificación explícita de baseline aprobada. [R2, R5, R11]
  - Probar OpenAPI authoritative, aprobación previa, repository/scope/version
    incompatibles, digest manipulado y selección ambigua.
  - Definir el puerto consumidor mínimo para resolver candidate/baseline ya
    autorizados sin red, credenciales ni elección implícita.

- [x] T3. Implementar el diff estructural canónico. [R3, R11, R13]
  - Cubrir added/modified/removed de operación, parámetro, request/schema,
    response y seguridad, además de metadata/provenance.
  - Cubrir uncertain por conflicts, partial/unknown, diagnostics, omissions y
    límites; ausencia no demostrable nunca será removal.
  - Probar recursión/ciclos, dependencias padre-hijo, orden aleatorio,
    cancelación entre fases y all-or-nothing.

- [x] T4. Implementar decisiones totales/selectivas y approved projection. [R4, R5, R7, R10]
  - Probar accept_all, reject_all, mezcla selectiva, accepted removal, rejected o
    pending conserva baseline, accepted uncertain y todos-rejected.
  - Rechazar IDs ajenos/desconocidos/duplicados, whole+items, expected revision
    obsoleta y conjuntos no cerrados sin aplicar efectos parciales.
  - Conservar confidence/completeness/conflicts/provenance originales y calcular
    decision/projection digests canónicos.

- [x] T5. Construir el servicio con autorización, reloj y revalidación completa. [R5, R6, R10, R11]
  - Implementar Create/Get/Decide/Export/ResolveApproved sobre interfaces del
    consumidor e inyectar actor y tiempo desde fronteras confiables.
  - Probar unauthorized con cero writes, binding completo y drift individual de
    repo, commit, catalog, baseline, source, analyzer, decisions y projection.
  - Probar reinicio/cambio de owner y segundo checkpoint pre-I/O con cero
    publicación, tool calls o autoridad después del drift.

- [x] T6. Implementar store local durable, CAS e idempotencia. [R5, R6, R10, R11]
  - Persistir checkpoint íntegro y journal append-only bajo
    `.featureproof/contract-reviews` mediante publicaciones atómicas.
  - Probar retries iguales/conflictivos, dos procesos, crash points, corrupción,
    prefijos, symlink/reparse/traversal, recuperación y aislamiento concurrente.
  - Garantizar que no se mantienen locks durante resolvers/callbacks/I/O externo
    y ejecutar focales con race.

- [x] T7. Integrar la proyección aprobada con `contractcatalog` y workflow. [R6, R7]
  - Crear una fuente de review explícita que añada provenance de aprobación y
    preserve lineage derivado sin reescribirlo retrospectivamente.
  - Probar proposal-only para pending/rejected/invalidated, authoritative
    approval vigente, partialidad que no demuestra ausencia y conflict pendiente.
  - Revalidar antes de aprobación del plan y de ejecución, incluidos restart y
    cambio entre ambos checkpoints.

- [x] T8. Implementar el exporter determinista OpenAPI 3.1 JSON. [R8, R11]
  - Mapear el subset neutral de operaciones, parámetros, bodies, responses,
    schemas y seguridad y validar el resultado con el inspector local.
  - Añadir extensions FeatureProof por unidad para review, provenance, classes,
    confidence, completeness, conflicts y omissions.
  - Probar no representable/nullable/partial/declarative constraints, orden
    estable, bytes repetibles, límites, cancelación y ausencia de secretos.

- [x] T9. Implementar publisher local atómico y overwrite protegido. [R9, R10, R11]
  - Exigir root/path relativo autorizado; crear con temp+sync+rename y reemplazar
    solo con `replace=true` más expected digest exacto.
  - Probar target existente, digest distinto, TOCTOU, symlink/reparse/hard-link,
    traversal, directorio, cancelación y fallos de write/sync/rename.
  - Registrar receipt idempotente únicamente después de confirmar bytes/digest
    publicados.

- [x] T10. Exponer CLI, API y MCP sobre el servicio compartido. [R5, R9, R11, R12]
  - Añadir contratos versionados de create/show/decide/export sin aceptar actor,
    autoridad, root, catálogo crudo ni credenciales desde input no confiable.
  - Probar paridad de estado/digests/códigos, autorización, idempotencia,
    payloads malformados/sobredimensionados y redacción de errores.
  - Exigir en export path explícito y, para replace, expected existing digest.

- [x] T11. Integrar la revisión con GitHub de forma head-bound. [R5, R6, R10, R11, R12]
  - Reutilizar installation/repository/PR/actor/head/changed paths autenticados y
    refrescarlos antes de toda decisión o efecto.
  - Probar head y changed-path drift, fork/actor no autorizado, delivery
    redelivered, restart/cambio de owner y resumen seguro sin payloads ni paths.
  - Impedir que texto de PR/comentario suministre export path, credenciales,
    authority o IDs fuera de la review sellada.

- [x] T12. Completar observabilidad, redacción y matrices adversarias. [R1, R5, R6, R8, R9, R10, R11, R12]
  - Emitir eventos/metrics acotados para create, decision, approval,
    invalidation y export sin usar logs como estado.
  - Inyectar secretos canario en cada frontera y verificar ausencia en artifact,
    journal, export, logs, CLI/API/MCP/GitHub y errores.
  - Repetir determinismo y concurrencia; verificar que límites/cancelación en
    cada fase detienen todo trabajo posterior.

- [x] T13. Ejecutar gates, trazar requirements y documentar la entrega. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13]
  - Auditar el diff completo para comentarios sistémicos en entidades,
    constantes, funciones, métodos, helpers, fakes y fixtures.
  - Ejecutar `gofmt`, compile-only, `go vet`, suites completas/focales repetidas,
    race y coverage mínimo del 85 % del código nuevo/modificado.
  - Crear `progress/generated-contract-review/implementation.md` con mapa real
    R→test, tasks, comandos, coverage, limitaciones y evidencia reproducible.
  - Marcar T1–T13 `[x]` solo tras cumplir su evidencia y solicitar revisión
    independiente sin modificar `feature_list.json`.
