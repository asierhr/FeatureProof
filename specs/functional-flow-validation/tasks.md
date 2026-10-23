# Validación de flujos funcionales reales — Tasks

- [x] T1. Definir el modelo canónico e inmutable `FunctionalFlow/v1`. [R1]
  - Escribir primero tests de IDs, step kinds, constraints, operations,
    identities, inputs, assertions, evidence requirements, source y revisión.
  - Implementar deep-copy/snapshot/digest sin valores resueltos ni handles.
  - Añadir comentario sistémico a toda entidad, constante, función, método y
    helper productivo o de test creado o modificado.

- [x] T2. Normalizar flujos proporcionados y generados con compatibilidad histórica. [R1, R15]
  - Crear adapter JSON estricto versionado para provided flow y bridge desde
    `PreparedPlan`/checks generados al mismo modelo.
  - Rechazar unknown/duplicate/trailing/oversized fields y unions ambiguas con
    categoría/field path estables.
  - Probar equivalencia semántica de ambos orígenes y regresión de manifests,
    specifications y planes existentes sin defaults nuevos.

- [x] T3. Validar grafo global, steps, evidence requirements y orden estable. [R2, R3]
  - Cubrir dependencies unknown/duplicate/self, ciclos, IDs colisionados,
    constraint inexistente y primer error determinista.
  - Validar todo el flujo antes de policy, fixtures o tools y derivar orden
    topológico secuencial con declaration-order tie break.
  - Probar inmutabilidad ante mutación posterior de maps/slices/DTOs.

- [x] T4. Sellar bindings exactos de contrato, target, efecto e identidad. [R2]
  - Reutilizar catálogo aprobado y identity catalog para method/path/capability/
    target e identity ID obligatorios, sin prefijos ni fallback implícito.
  - Comprobar que `observe` solo usa operaciones sin efectos y `act` conserva
    effect/idempotency/reconciliation metadata contractual.
  - Probar drift, contrato derivado no aprobado, identity incompatible,
    operación ambigua y cero resolver/DNS/dial ante rechazo.

- [x] T5. Implementar referencias tipadas de fixture y evidencia de step. [R3, R14]
  - Resolver en copias privadas solo captures confirmadas del mismo run o
    evidence terminal de antecesores, con tipo/path/bytes/depth acotados.
  - Mantener raw captures fuera de artifacts, model, journal, package y
    superficies; persistir solo metadata/digest permitidos.
  - Probar ancestry, tipos, null, arrays/objects, path ausente, evidencia
    foreign/truncated/corrupt y fallo cerrado de redacción.

- [x] T6. Integrar prepare de fixtures como precondición del flujo. [R4]
  - Invocar el lifecycle compartido una vez tras artifact/approval y antes del
    primer business step.
  - Bloquear todo el primary flow ante preparation failed/uncertain/cancelled
    sin fabricar un functional failed.
  - Probar fixture-free, múltiples fixtures, varias identities y reuse del
    `PreparedSnapshot` durante replan/recovery.

- [x] T7. Implementar scheduler secuencial, budgets y bloqueos causales. [R5, R8]
  - Seleccionar ready steps por orden estable, reservar trabajo/run/step antes
    de efectos y propagar todas las causas directas de bloqueo.
  - Continuar ramas independientes tras functional failure mientras no exista
    cancelación, corrupción o pérdida de autoridad global.
  - Probar límites exactos, policy denial, exhausted dimensions, bloqueos
    múltiples y ausencia de goroutines ocultas.

- [x] T8. Ejecutar cada operación mediante el pipeline controlado. [R5, R9, R14]
  - Aplicar claim/intent, referencias, input validation, binding, policy,
    budget, identidad, HTTP tool, redacción y evidence en el orden aprobado.
  - Resolver credenciales solo en transporte y no serializar inputs resueltos,
    captures, credential refs, keys ni errores externos.
  - Probar dos usuarios, target/redirect guards, status funcional, cancelación
    en cada frontera y spies de red a cero ante rechazo pre-I/O.

- [x] T9. Implementar observaciones pre/post y comparaciones deterministas. [R6, R7]
  - Validar dos `observe` de la misma operación/target/selector de recurso con
    ancestry y paths/tipos exactos, y revalidar el digest tras resolver.
  - Evaluar `equal`/`not_equal` sin coerción desde evidencia confirmada y
    conservar before/after evidence IDs en resultados.
  - Probar mutación, ausencia de mutación, missing/truncated evidence, tipos
    incompatibles, input malicioso y contradicción textual del agente.

- [x] T10. Crear ledger monotónico, agregación y resultado funcional. [R7, R8, R12]
  - Anexar assertions locales/comparativas con identities/lineage estables y
    deduplicación idempotente.
  - Derivar constraint/flow passed, failed, inconclusive o error exclusivamente
    desde required evidence y resultados confirmados.
  - Probar fallo irreversible, missing evidence, mixed branches, blocked,
    corruption y que agent/cleanup claims nunca cambian el veredicto.

- [x] T11. Implementar intents, retries, ownership y recovery de steps. [R9, R10, R13]
  - Confirmar intent durable antes de una action, usar key estable del sistema y
    reintentar solo transitorios con garantía contractual y budgets.
  - Reclamar leases expiradas y converger desde todos los checkpoints; no
    repetir una action incierta no reconciliable ni trabajo confirmado.
  - Probar crash matrix, dos owners, CAS conflict, handoff, store corruption,
    uncertain I/O y repeated delivery bajo `-race`.

- [x] T12. Integrar revisiones y lifecycle en `validationworkflow`. [R4, R10, R11, R13]
  - Persistir `FlowArtifact`, fases, revision lineage y approval exacta; cada
    replan vuelve por normalizer/validator/binder/policy/budgets.
  - Impedir que un lote adicional cambie fixtures, authority o ledger previo y
    detectar fingerprints equivalentes antes de approval/tool work.
  - Coordinar primary terminal, cancelación y entrada única a cleanup sin
    recrear fixtures ni reabrir pasos.

- [x] T13. Extender evidence package, informe y reconstrucción. [R11, R12, R14]
  - Publicar orden de prepare, revisiones, actions, observations, assertions,
    bloqueos, primary outcome y cleanup report separado.
  - Verificar artifact/event/evidence/ledger digests y reconstruir la misma
    proyección sin confiar en logs o estado derivado.
  - Probar missing/reordered/forked/foreign records, publish crash, cleanup
    partial y matriz recursiva de canarios.

- [x] T14. Mantener paridad CLI, API, MCP y GitHub. [R14, R15]
  - Delegar start/status/cancel/result en el mismo workflow y proyectar la misma
    vista allowlisted de flow/primary/cleanup.
  - Permitir provided manifest path solo en CLI local; resolver aliases/artifact
    IDs autorizados en superficies remotas y rechazar autoridad inline.
  - Probar schemas estrictos, oversized/unknown fields, redelivery/conflict y
    texto PR/model output malicioso sin cambio de bindings.

- [x] T15. Cerrar concurrencia, cancelación y compatibilidad multiplataforma. [R13, R15]
  - Auditar todos los blocking boundaries, owners, goroutines, locks, deadlines,
    waiters y cleanup scope; ningún lock permanece durante I/O/callback.
  - Repetir suites focales y recovery con barriers/channels y `-race`, sin
    `time.Sleep`, red externa, credenciales ni proveedor real.
  - Ejecutar regresión de planning/assertion/replanning/fixture/workflow/
    localvalidation/externalagent/github y cross-builds aplicables.

- [x] T16. Ejecutar gates, trazabilidad y documentación de entrega. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Auditar el diff para comentarios sistémicos en entidades, constantes,
    funciones, métodos, DTOs, callbacks, factories, fakes y helpers de test.
  - Ejecutar `gofmt`, compile-only, `go vet`, suites focal/completa repetidas,
    `go test -race`, crash matrix y coverage mínimo del 85 % del código
    productivo nuevo/modificado.
  - Crear `progress/functional-flow-validation/implementation.md` con mapa real
    R→test, tasks, comandos/resultados, coverage, riesgos y evidencia
    reproducible.
  - Marcar T1–T16 `[x]` solo con evidencia completa y solicitar review
    independiente sin modificar `feature_list.json`.
