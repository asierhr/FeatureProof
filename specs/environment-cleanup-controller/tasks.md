# Controlador durable de limpieza de entornos — Tasks

Las tareas se ejecutan en orden y con TDD. Cada comportamiento incorpora sus
tests en la misma tarea. Toda entidad, constante, variable, función, método y
helper de test nuevo o modificado debe incluir un comentario que explique su
responsabilidad sistémica, frontera o invariante.

Esta feature usa stores/loops locales y puertos sustituibles. No implementa
PostgreSQL, colas o workers (feature 35), configuración versionada (feature 36),
tenant/RBAC (feature 37) ni convierte cleanup en una tool del modelo.

- [ ] T1. Definir el modelo canónico de cleanup y capacidad. [R1, R4, R9, R13]
  - Escribir tests de states, IDs/digests, owner/project/run/provider,
    created/expiry/TTL, demand, receipts, attempts, lease y fencing.
  - Implementar strict decode, canonicalización, deep copy y relaciones
    content-free con overflow checks.
  - Probar unknown/duplicate/null fields, mutación posterior, cross-project,
    timestamp/TTL inconsistente, demand negativa/imposible y self-signed tamper.
  - Definir `LifecycleCleanupBinding` completo y
    `UncertainAcquisitionBinding` restringido a Reconcile; probar que ningún
    binding parcial se completa buscando por `RunID`.

- [ ] T2. Implementar el store local de cleanup, índices y journal. [R1, R3, R8, R13, R14]
  - Escribir tests de Reserve/Scan/Claim/Renew/SaveOwned/ReleaseOwner y Events
    mediante receipts/versiones inmutables, heads CAS e índices reconstruibles.
  - Implementar paginación/cursor canónicos, live/expired lease y fencing sin
    mantener locks durante fsync, observer o callbacks.
  - Inyectar crash antes/después de publicación, head, event e index; probar
    reopen, rollback, corrupción, redelivery bytes iguales y conflicto distinto.

- [ ] T3. Implementar el capacity ledger y admisión atómica por proyecto. [R9, R10, R14]
  - Escribir tests de límites exactos/one-over para environments, CPU, memory,
    objects y max TTL, además de aritmética overflow-safe.
  - Reservar receipt durable con acquisition key antes de provider I/O y ligar
    environment/UID después de Acquire; misma key/input converge y distinto
    input produce conflict.
  - Probar CAS de dos adquisiciones que juntas exceden, proyectos independientes,
    unknown orphan demand fail-closed y ninguna reserva parcial.

- [ ] T4. Integrar capacity admission en `LifecycleService`. [R1, R4, R9, R10]
  - Añadir un `DemandResolver`/`CapacityAdmission` consumer-owned explícito para
    selections managed, sin números aportados por request/modelo.
  - Persistir created/TTL/expiry/demand/receipt antes de Acquire, confirmar la
    identidad observada y liberar solo tras `StateReleased` confirmado.
  - Probar denial antes de provider call, crash en cada boundary, response loss,
    redelivery y que existing-url no adquiere capacidad managed por fallback.

- [ ] T5. Proyectar todos los cleanup intents del workflow. [R2, R3, R11]
  - Escribir verticales para passed, failed funcional, inconclusive, error,
    cancelled y expiry que demuestren intent durable antes de release.
  - Implementar `IntentSource` read-only sobre workflow store, sin conceder al
    controller mutación de outcome, package, evidence o run record.
  - Inyectar crash entre workflow intent y environment release intent y probar
    recuperación con desired outcome intacto.

- [ ] T6. Definir los puertos provider-neutral de inventario y cleanup y el fake contractual. [R5, R6, R7, R12, R14]
  - Definir `ManagedInventory` paginado y `LifecycleCleanup` mínimo, sin delete
    genérico, manifests, names/selectors del caller, SDKs o credentials.
  - Construir fake concurrent-safe con present/absent, UID/owner drift,
    terminating, response loss, errores tipados y barreras por canales.
  - Probar cancelación en cada método, limits/tokens/snapshot y ausencia de red,
    shell, filesystem externo o waits reales.
  - Exigir binding sellado en Status/Reconcile/Release y probar mismo RunID con
    project, provider, selection/acquisition, UID/fingerprint, lifecycle digest
    o cleanup key divergentes, además de redelivery exacta.

- [ ] T7. Implementar discovery y `RunOnce` acotados. [R2, R3, R4, R5, R12]
  - Escanear intents, lifecycle records, capacity receipts e inventories con
    budgets de páginas/items/claims/duración y cursor durable por fuente.
  - Deduplicar por cleanup identity y clasificar active, terminal, cancelled,
    pending, released y expired sin inferir ausencia desde errores.
  - Probar restart, página vacía con token, token repetido, snapshot drift,
    partial scan y continuación posterior sin perder ni duplicar items.

- [ ] T8. Implementar eligibility por TTL y expiración. [R1, R3, R4]
  - Evaluar el límite inclusivo con clock inyectado y validar TTL contra policy
    sin extender expiración efectiva.
  - Persistir intent al expirar, respetar live lifecycle lease y procesar tras
    handoff seguro.
  - Probar before/equal/after expiry, clock rollback, duration/timestamp overflow,
    expiry durante Acquire y una sola confirmación tras ciclos repetidos.

- [ ] T9. Implementar adopción fail-closed de huérfanos. [R3, R5, R9, R10, R13]
  - Exigir metadata íntegra, owner/provider registrados, grace cumplido y
    NotFound confirmado en lifecycle/run stores antes de reservar orphan receipt.
  - Quarantinar foreign/incomplete/tampered/ambiguous candidates y conservar
    capacidad desconocida como bloqueo de admisión.
  - Probar store unavailable, record que reaparece, cross-project, UID reuse,
    orphan already absent y crash antes/después de receipt/intent.

- [ ] T10. Implementar cleanup reconciler idempotente. [R2, R6, R10, R11]
  - Para lifecycles conocidos delegar a release de feature 31 con el binding
    integral verificado; para orphan usar recovery cleanup exacto y confirmar
    ausencia antes de terminalizar.
  - Persistir intent antes de I/O y usar la misma cleanup key/UID en response
    loss, timeout, redelivery y CAS terminal perdido.
  - Probar drift/unknown provider/Forbidden/terminating/finalizer, cero fallback
    destructivo y capacity release exactamente una vez tras confirmation.
  - Probar que Status/Reconcile/Release rechazan autoridad incompleta o
    cross-project antes de provider I/O y que adquisición incierta debe producir
    por CAS un binding conocido antes de Release.

- [ ] T11. Implementar retry scheduler durable y taxonomía. [R7, R12, R13]
  - Mapear transient/rate-limit/timeout/uncertain frente a invalid/unauthorized/
    integrity/conflict sin comparar textos ni exponer cuerpos remotos.
  - Persistir attempts/category/next-attempt antes de ceder ownership y aplicar
    MaxAttemptsPerCycle, MaxBackoff, MaxCumulativeWait y cycle deadline.
  - Probar budget exacto, wait que no cabe, attempts overflow, retry en otro
    restart, permanent quarantine y requeue autorizado/auditado.

- [ ] T12. Implementar concurrencia, leases, fencing y handoff. [R3, R8, R12]
  - Probar dos controllers sobre el mismo item, live lease, expiración inclusiva,
    owner crash, renewal, stale fencing token y CAS perdido tras provider effect.
  - Asegurar que solo un owner con lease viva inicia I/O y que llamadas físicas
    repetidas tras crash convergen mediante key/UID sin doble confirmación lógica.
  - Repetir same/different item y same/different project bajo race alta; verificar
    progreso independiente y ausencia de locks durante I/O/wait.
  - Confirmar que handoff conserva exactamente el cleanup binding original y
    que un contender no lo reconstruye a partir de `RunID`.

- [ ] T13. Implementar loop periódico, cancelación y shutdown. [R3, R7, R12]
  - Construir `Run(ctx)` sobre `RunOnce` y waiter/ticker inyectado, sin goroutine
    oculta en constructor; el caller posee start/cancel/join.
  - Comprobar ctx antes de scan/claim/I/O/save/wait y no comenzar trabajo nuevo
    durante shutdown.
  - Probar cancelación coordinada en cada boundary, provider incierto recuperable,
    lease para handoff y cero goroutine/timer leak sin `time.Sleep`.

- [ ] T14. Implementar inventario/cleanup Kubernetes mínimo. [R1, R5, R6, R12, R13]
  - Extender un puerto específico para list/get/delete Namespace con paginación,
    owner scope y UID precondition; no reutilizar RBAC de la tool feature 33.
  - Traducir metadata cerrada a candidate, revalidar owner/run/project/digests/
    TTL/expiry/UID antes de delete y tratar Terminating sin force/finalizer patch.
  - Usar fake/client reactors para verb audit, pagination, Forbidden, NotFound,
    UID replacement, response loss y canarios; nunca cluster/credentials reales.

- [ ] T15. Integrar status, auditoría y composición local. [R11, R13, R14]
  - Exponer proyección segura cleanup/capacity separada del outcome y conservar
    package/evidence/report bytes antes y después de retry tardío.
  - Componer controller, stores, policies, providers, inventory, owner, clock,
    waiter y límites explícitos en `cmd/featureproof`; rechazar dependencias
    ausentes y no iniciar goroutines desde composición.
  - Probar eventos exactly-once, observer failure no autoritativo y canarios
    ausentes; confirmar cero PostgreSQL/queue/worker/dual-write/config de repo.

- [ ] T16. Ejecutar matrices de recovery, seguridad y verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Ejecutar fault injection en cada boundary, outcome terminal, TTL, orphan,
    quota, retry, ownership, cancelación y handoff con clock/provider falsos.
  - Registrar `R1–R15 → test concreto`, tasks, comandos, coverage y riesgos en
    `progress/environment-cleanup-controller/implementation.md`.
  - Ejecutar gofmt/comments/diff-check, vet, compile-only, focales repetidos,
    global normal/race, focal race ×20, runtime Linux y cross-builds aplicables.
  - Medir statements nuevos/modificados y demostrar ≥85% sin redondear; auditar
    temporales, secretos, red real, scope y comentarios sistémicos.
