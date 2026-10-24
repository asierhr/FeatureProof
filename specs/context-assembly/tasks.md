# Ensamblado seguro de contexto para agentes — Tasks

Las tareas se ejecutan en orden y cada cambio de comportamiento incorpora sus
tests en la misma tarea. Todo símbolo nuevo o modificado —entidad, constante,
variable, función, método o helper de test— debe incluir un comentario sobre su
responsabilidad dentro del sistema.

No se obtiene contexto desde GitHub, no se adquieren entornos y no se añade
ninguna capability ejecutable. Las features 30 y 31 conectarán esas fronteras
mediante los contratos neutrales definidos aquí.

- [x] T1. Definir el modelo canónico `ContextSnapshot/v1`. [R1, R4, R5, R9, R15]
  - Crear enums cerrados para role, source kind, trust class, authority,
    completeness, omission/conflict reason y outcome.
  - Modelar request, authority, source/item projection, secciones, manifests,
    snapshot y errores sin stores ni adapters.
  - Implementar strict validation, deep-copy, canonical JSON y digests.
  - Probar unknown/null/duplicate fields, uniones ambiguas, límites, mutación
    posterior, orden de maps y equivalencia canónica.

- [x] T2. Implementar catálogos sellados de perfiles e instrucciones. [R2, R4, R6, R12]
  - Definir perfiles versionados para planner, analyzer y diagnostic con fuentes,
    campos, sensibilidad, completitud, budgets e instruction/protocol versions.
  - Permitir que request limits estrechen autoridad y rechazar role/source/field/
    sensitivity widening.
  - Probar matriz completa de diferencias entre roles e instrucciones imposibles
    de aportar o reemplazar desde una fuente/caller.

- [x] T3. Implementar registry y contrato de producers. [R1, R3, R5, R13]
  - Definir puertos mínimos en `contextassembly` para source projection y
    revalidación de bindings; registrar producers exclusivamente en composición.
  - Validar project/repository/revision, producer/version, source digest,
    safe references, completeness y límites antes de materializar items.
  - Probar producer missing/duplicado, binding inválido, cross-project/cross-repo,
    corrupción, mutación posterior, cancelación y orden estable de lectura.

- [x] T4. Añadir secret gate y fronteras de trust class. [R4, R5, R14]
  - Escanear metadata y payload canónico antes de ranking, tokenización, store o
    model; aceptar únicamente proyecciones redactadas con estado conocido.
  - Construir secciones tipadas sin interpolar contenido y forzar que memoria,
    evidencia, diff, contrato, logs/documentos sigan como untrusted data.
  - Probar canarios anidados, credentials/bodies/prompt/chain-of-thought, detector
    cancelado/fallido e intentos de reclasificación mediante prompt injection.

- [x] T5. Implementar elegibilidad estructural y deduplicación. [R2, R3, R5]
  - Cruzar request, role profile, source authority y item metadata en el orden
    project/repository/scope/revision/state/authority/validity/sensitivity/kind.
  - Generar omissions cerradas y deduplicar por identidad/digest sin usar texto,
    score, recencia o confidence para conceder autoridad.
  - Usar spies que prueben que memoria/source inelegible nunca llega al ranker,
    tokenizer ni snapshot; cubrir todos los scopes y bordes temporales.

- [x] T6. Implementar conflictos, parcialidad e insufficient context. [R5, R8]
  - Fusionar conflictos declarados por producers con contradicciones por
    logical key, manteniendo todos los IDs y orden canónico.
  - Aplicar política sellada para fuentes/items obligatorios y opcionales sin
    elegir ganador.
  - Probar conflict requerido/opcional, fuente missing/stale/partial/truncated,
    manifests capados y resultado insufficient sin model call.

- [x] T7. Implementar ranking opcional y derivado. [R3, R7, R13]
  - Definir `SemanticRanker` sobre IDs/metadatos elegibles y work budget;
    validar cardinalidad, IDs, duplicados y scores finitos.
  - Añadir orden estructural estable, desempate normativo y fallback solo cuando
    el perfil lo autorice; ningún índice es fuente de verdad.
  - Probar ranker ausente, ties, índice missing/stale/corrupt, output adversario,
    budget/cancelación y contenido filtrado nunca observado por el fake.

- [x] T8. Implementar packing exacto de items, bytes y tokens. [R5, R6, R8]
  - Definir `TokenCounter` resuelto para provider/model y contar el envelope
    canónico final, incluida metadata obligatoria.
  - Reservar guardias/configuración primero y añadir items completos siguiendo
    orden estable; recalcular/confirmar counts antes de publicar.
  - Probar límites cero/inválidos y bordes exactos, item oversized, overhead,
    instrucciones obligatorias que no caben, counter incompatible/fallido/
    cancelado y determinismo repetido.

- [x] T9. Implementar snapshot binding y revalidación de drift. [R9, R10]
  - Sellar snapshot con run/role/project/repository/revision/config/profile/
    instructions/provider/model/source bindings y `plan_id`.
  - Añadir `ContextBinding/v1` content-free con prepared plan digest y adapter de
    revalidación que consulte cada autoridad actual.
  - Probar tampering/cross-run/cross-project/cross-plan y drift de revision,
    configuración, perfil, instrucciones, memoria, evidencia y otras fuentes;
    confirmar que cambios del índice derivado no invalidan autoridad.

- [x] T10. Implementar `ContextStore` local y journal seguro. [R9, R11, R13, R14]
  - Persistir snapshot/binding direccionados por digest y receipts de
    idempotencia; journal solo con IDs, digests, counts, reasons y outcome.
  - Publicar con temporales exclusivos, sync, atomic replace y parent sync;
    verificar canonical bytes y digest al cargar/recover.
  - Probar reopen, redelivery, key conflict, respuesta perdida y fallos en cada
    boundary sin snapshots parciales ni contenido en journal/temporales.
  - Cubrir traversal, absolute/device names, permissions, symlink/junction/
    reparse/hardlink, root swap y no-follow en Windows/Unix.

- [x] T11. Adaptar las fuentes de dominio existentes. [R1, R2, R3, R5, R8, R14]
  - Implementar producers para restricciones, contrato reducido, configuración
    aprobada, memoria validada, historial, plan, diagnostic y evidencia/package
    verificado mediante puertos estrechos.
  - Añadir contrato/fake neutral para diff autorizado sin acceso a GitHub y sin
    implementar feature 30.
  - Dividir colecciones en unidades atómicas estables, conservar provenance/
    bindings y no resolver evidence refs a bodies.
  - Probar adapters con snapshots reales/fakes, conflicts de memoria, fuentes
    parciales, cross-scope y ausencia de secrets/capability handles.

- [x] T12. Integrar el snapshot planner con `agentplanning`. [R4, R6, R9, R12, R14]
  - Incorporar el snapshot inmutable a generation sin exponer store/producers y
    mantener catálogos privados solo para validación determinista.
  - Cambiar la serialización model-facing para usar exclusivamente instrucciones
    y envelope del snapshot; eliminar el payload legacy paralelo.
  - Ligar provenance/prepared plan digest al context digest sin convertirlo en
    autorización de ejecución.
  - Probar separación `Instructions`/`Untrusted`, equivalencia canónica,
    descriptors declarativos sin closures y rechazo de plan/context cruzados.

- [x] T13. Integrar ensamblado y revalidación en `validationworkflow`. [R9, R10, R11, R12, R13]
  - Componer un único assembler/store/authority con admission y memoria; rechazar
    claims de snapshot/binding procedentes de CLI/API/MCP/GitHub.
  - Persistir context digest/binding en artifact/checkpoint y revalidar antes de
    approval, execution y recovery junto a contrato/memoria.
  - Tratar insufficient como resultado seguro y stale como bloqueo antes de
    efectos; no volver al flujo legacy si falta assembler.
  - Probar vertical completa, restart y cada clase de drift con cero model/tool
    calls después de detectar staleness.

- [x] T14. Integrar proyecciones por rol y superficies reales. [R2, R4, R11, R12]
  - Añadir consumers controlados de analyzer/diagnostic sobre el mismo assembler
    sin introducir nuevos verdicts, remediation loops o capacidades.
  - Verificar que las cuatro superficies reales atraviesan el service compartido
    y producen el mismo digest bajo la misma autoridad.
  - Probar que planner/analyzer/diagnostic no reciben la proyección de otro rol,
    toda la memoria, evidence bodies, credentials, stores o tool handles.

- [x] T15. Cerrar observabilidad, cancelación, concurrencia e idempotencia. [R11, R13, R14]
  - Emitir un terminal record exactamente una vez por assembly/binding/
    revalidation con metadata allowlisted y observer timeout acotado.
  - Propagar cancelación por producers, filters, ranker, tokenizer, store y
    consumers; esperar todo trabajo propio antes de retornar.
  - Probar cancelación en cada fase, concurrent assemblies, CAS/redelivery bajo
    `-race`, zero goroutine/temp leaks y escaneo byte-for-byte de canarios en
    errors/events/metrics/journal/model request.

- [x] T16. Ejecutar verificación y completar trazabilidad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Registrar en `progress/context-assembly/implementation.md` el mapa exacto
    R1–R15 → tests y resultados, incluidas matrices adversarias y de superficies.
  - Ejecutar `gofmt`, `go vet ./...`, compile global, suite focal repetida,
    `go test ./...`, `go test -race ./...`, runtime Linux y cross-builds
    Linux/Darwin/Windows aplicables.
  - Medir statements nuevos/modificados incluyendo build tags y demostrar
    coverage sin redondear >=85%.
  - Auditar comentarios sistémicos de toda entidad, constante, variable,
    función, método y helper nuevo/modificado, y confirmar ausencia de archivos
    temporales o cambios fuera de feature 29.
