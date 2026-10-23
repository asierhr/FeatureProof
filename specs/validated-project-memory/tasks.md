# Memoria de proyecto validada y versionada — Tasks

Las tareas se ejecutan en orden. Cada cambio de comportamiento incorpora sus
tests en la misma tarea. Todo símbolo nuevo o modificado —entidad, constante,
variable, función, método o helper de test— debe incluir un comentario sobre su
responsabilidad dentro del sistema. No se implementa ensamblado de contexto,
ranking semántico ni ninguna parte de feature 29.

- [ ] T1. Crear el modelo canónico `ProjectMemory/v1` y sus límites. [R1, R2, R4, R15]
  - Añadir tipos cerrados para scope, class, state, authority, confidence,
    sensitivity, provenance, value y evidence reference.
  - Implementar validación estricta, deep-copy, JSON canónico y digests sin
    filesystem ni adapters.
  - Probar campos unknown/null, uniones ambiguas, números no finitos, IDs/OIDs/
    digests, límites, estabilidad de digest y mutación posterior.

- [ ] T2. Implementar la máquina de estados y la autoridad de transición. [R3, R4, R6]
  - Expresar la tabla completa de creación/promoción/terminales, expected version
    y replacement binding.
  - Separar actor, authority y confidence; impedir que proposals externas
    controlen state/scope/authority.
  - Probar todas las aristas válidas e inválidas, terminales, actor incompatible,
    replacement no confirmado y agente intentando aprobar/invalidate/supersede.

- [ ] T3. Implementar validez y staleness deterministas. [R2, R5, R14]
  - Añadir reloj inyectado, ventanas exclusivas, bindings exactos de revision/
    configuration/evidence y reconciliación explícita de expiry.
  - Probar borde `valid_from/valid_until`, rollback de reloj, commit/config drift,
    evidencia revocada y reconciliación idempotente sin workers ocultos.

- [ ] T4. Añadir el secret gate y las proyecciones de dominio seguras. [R6, R7, R13, R14]
  - Escanear recursivamente nombres, paths, shapes y valores antes de crear un
    comando persistible o un índice.
  - Implementar `ReviewView`, `AgentMemoryDescriptor`, `AuditView` y errors
    allowlisted con límites y deep-copy.
  - Probar tokens, cookies, keys, bodies, prompt completo, chain of thought,
    canarios anidados, detector cancelado/fallido y ausencia byte-for-byte en
    views/errors.

- [ ] T5. Definir `MemoryStore` y autoridades pequeñas en el consumidor. [R4, R8, R9, R14]
  - Introducir puertos mínimos para store, evidence, principal, integrity, clock
    y waiter; usar fakes deterministas.
  - Implementar el service de propose/observe/approve/invalidate/supersede/
    expire/get histórico con validación previa a persistencia.
  - Probar orden de guards, policy denial, evidencia cross-project, context
    propagation y cero store calls ante input/secret/authority inválidos.

- [ ] T6. Implementar journal local autenticado y apertura fail-closed. [R8, R10]
  - Crear el layout versionado bajo el root configurado, envelopes canónicos con
    sequence/previous digest y authenticators mediante la autoridad externa.
  - Verificar cadena, key ID, project binding, eventos y records antes de
    devolver memoria.
  - Probar reopen válido y manipulación, deletion, insertion, reorder,
    truncation, cross-project copy, unknown key y re-sell con key no anclada.

- [ ] T7. Implementar snapshots e índice con publicación atómica. [R7, R8, R11]
  - Publicar snapshots autenticados e índice estructural derivado mediante
    temporales exclusivos, sync, replace/rename y parent sync.
  - Reconstruir snapshot/index desde journal verificado y no materializar
    contenido inelegible antes de filtros.
  - Probar snapshot missing/stale/corrupt, index poisoning/corruption, fallo en
    cada boundary y ausencia de canarios en índice/temporales.

- [ ] T8. Implementar CAS, receipts, locks y recovery de writers. [R8, R9, R14]
  - Serializar por proyecto entre goroutines/procesos, revalidar head bajo lock y
    ligar receipt a project/transition/digest/idempotency identity.
  - Recuperar journal confirmado con publicación incompleta y limpiar solo
    temporales propios verificables.
  - Probar writers concurrentes, stale CAS, redelivery/restart, key reutilizada,
    respuesta perdida, lock abandonado y cancelación en lock/append/sync/publish
    sin leaks.

- [ ] T9. Endurecer paths y root en Windows y Unix. [R10, R15]
  - Implementar primitives específicas de plataforma para no-follow/reparse,
    canonical root, tipo/permisos, hardlinks, handles y replace atómico.
  - Probar traversal, absolute/device names, Unicode ambiguo, symlink/junction/
    reparse/hardlink, root swap y TOCTOU en los hosts donde aplique.
  - Añadir pruebas de contrato comunes y cross-builds para que ambos adapters
    mantengan la misma semántica fail-closed.

- [ ] T10. Implementar query estructural, conflictos y orden estable. [R2, R5, R11, R14]
  - Cruzar request con `QueryAuthority`; filtrar project/scope/revision/state/
    authority/validity/sensitivity/class antes de ordenar y truncar.
  - Detectar valores contradictorios por logical key y devolver todos los IDs
    sin elegir por confidence, recency o índice.
  - Probar matriz de filtros, review versus operational query, stable ordering,
    pages/limits, índice ausente y resultados detached.

- [ ] T11. Añadir bindings históricos y revalidación de drift. [R5, R11, R12]
  - Sellar query digest, store head y `{id, version, digest}`; recuperar versiones
    exactas aunque ya sean terminales.
  - Integrar de forma opcional el binding seguro con artifacts de planning y
    validation workflow y revalidar antes de approval/execution.
  - Probar invalidation/supersede/expiry entre query y ejecución, run histórico
    inmutable, tampering del binding y compatibilidad de runs sin memoria.

- [ ] T12. Integrar proposals/observations sin ampliar autoridad. [R3, R4, R6, R12]
  - Adaptar output estructurado del modelo únicamente a `ProposalCommand`; no
    exponer store, paths, evidence bodies, keys ni handles.
  - Adaptar evidencia terminal verificada de functional flow a trusted observed
    memory sin modificar ledger, cleanup ni primary result.
  - Probar prompt injection, proposal que pide elevación, output malformed,
    evidence de otro scope, resultado funcional byte-for-byte estable y ausencia
    de ensamblado por rol.

- [ ] T13. Conectar el servicio compartido a superficies aplicables. [R7, R13, R14]
  - Exponer operaciones versionadas de lectura/proposal/review/transición en CLI,
    API y MCP; conectar GitHub solo con actor/repository/action allowlisted.
  - Resolver principals y autoridad en composición, nunca desde payload, aliases
    libres, paths o comentario natural.
  - Probar paridad de DTO/error/idempotencia entre adapters, modos read-only,
    caller desconocido, spoofing de actor y canarios ausentes de todas las
    respuestas.

- [ ] T14. Completar observabilidad, límites y shutdown. [R7, R9, R14]
  - Emitir records sin contenido con IDs, transition, safe actor, outcome,
    category y duration; aplicar budgets de records/bytes/work/events/retries/
    duration desde configuración confiable.
  - Asegurar que cada lock, timer y eventual goroutine tiene owner y join antes
    de retornar.
  - Probar agotamiento en cada dimensión, errores permanentes sin retry,
    transitorios acotados, cancelación en cada puerto y logs/events sin payloads
    ni errores externos.

- [ ] T15. Ejecutar las matrices verticales y adversarias. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14]
  - Cubrir proposal → review → approval → query → binding → stale recheck y
    evidence → observed → audit con fakes herméticos.
  - Ejecutar la matriz de crashes en cada durable boundary y la matriz de
    poisoning/corrupción/path/concurrencia de `design.md` repetidamente y con
    race detector.
  - Verificar mediante búsqueda de canarios journal, snapshot, índice,
    temporales, bindings, artifacts, logs, errors y las cuatro superficies.

- [ ] T16. Cerrar trazabilidad y gates reproducibles. [R15]
  - Crear `progress/validated-project-memory/implementation.md` con mapa
    R→test→resultado, decisiones, tasks, comandos, coverage y limitaciones.
  - Añadir un gate de coverage del diff que incluya todos los fuentes productivos
    modificados, también build-tagged Windows/Unix, y exija >=85% sin redondeo.
  - Ejecutar `gofmt`, auditoría de comentarios sistémicos, `go vet ./...`,
    compile-only, suite global, focal repetida, `-race` y cross-builds aplicables;
    retirar temporales y confirmar que no se mezcló feature 29.
