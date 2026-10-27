# Control plane persistente y distribuible — Tasks

Las tareas se ejecutan en orden y con TDD. Cada comportamiento incorpora sus
tests en la misma tarea. Toda entidad, constante, variable, función, método y
helper de test nuevo o modificado debe llevar un comentario sobre su
responsabilidad sistémica, frontera o invariante; repetir el nombre no basta.

La implementación no añade configuración versionada de proyecto (feature 36)
ni tenant/RBAC (feature 37), no persiste valores secretos y no mezcla local con
PostgreSQL dentro de un run.

- [ ] T1. Definir el modelo del control plane y sus errores. [R4, R6, R7, R8, R10, R14, R16]
  - Escribir tests de `BackendBinding`, job states, lease/fence, retry decision,
    authority envelope, inbox/outbox identity y categorías de error.
  - Implementar canonicalización, strict decode, digests, copies, límites,
    UTC/overflow checks y matrices de transición cerradas.
  - Probar unknown/duplicate/null/oversized fields, tamper, cross-project,
    expired authority, stale fence y mutación posterior.

- [ ] T2. Crear el manifest y runner de migraciones PostgreSQL. [R2, R14, R15, R16]
  - Escribir tests unitarios del orden/checksum/rango/dirty state y tests reales
    desde base vacía, latest, rerun y dos migrators concurrentes.
  - Implementar assets numerados, ledger, exclusión, transacciones y readiness
    compatible con expand/backfill/contract.
  - Probar checksum alterado, hueco, versión futura, permiso insuficiente,
    migration abortada y rollback de binario dentro/fuera de ventana.

- [ ] T3. Implementar primitivas PostgreSQL compartidas sin repositorio genérico. [R2, R3, R14, R15, R16]
  - Escribir tests de pool/transactor, context cancellation, statement/tx
    timeouts, SQLSTATE mapping y retry budget de transacción completa.
  - Implementar helpers estrechos para transactions, canonical bytes, cursor y
    instrumentation; no filtrar driver/SQL a contratos de dominio.
  - Inyectar `40001`, `40P01`, constraints, disconnect y unknown commit; probar
    cero parsing de mensajes y cero I/O externo dentro de callback.

- [ ] T4. Implementar backend epochs, head CAS y selección exclusiva. [R4, R12, R16]
  - Escribir tests local/postgres de initial epoch, load/verify idempotentes,
    exactly one active head, mismatch, restart, outage y ausencia de
    fallback/dual-write.
  - Persistir epoch 1/head junto con admisión y exigir epoch, binding digest y
    adapter identity en cada frontera relacionada.
  - Probar dos cutovers concurrentes, events append-only, epoch anterior
    inmutable/archivada y worker con epoch/fence anterior rechazado.
  - Auditar que DSN/credentials no entren en binding, evento o logs.

- [ ] T5. Implementar runs, eventos, artifacts y packages PostgreSQL. [R1, R3, R4, R13, R15, R16]
  - Crear contract tests compartidos para `WorkflowStore`, plan/executable
    artifacts y package storage contra local/PostgreSQL donde aplique.
  - Implementar Reserve/Load/SaveOwned/Cancel/Pending/Claim/Renew/Release y
    events append-only con CAS, relations y canonical package bytes bounded.
  - Probar writers concurrentes, reopen desde otro proceso, corruption/tamper,
    rollback parcial, package oversize y package independiente del filesystem.

- [ ] T6. Implementar approvals y functional-flow stores PostgreSQL. [R1, R3, R9, R10, R15, R16]
  - Escribir contract tests de approval ordering/snapshots/CAS y de
    `IntentStore`/`EventJournal` claim-intent-terminal-reconcile.
  - Implementar adapters sin cambiar semántica ni exponer snapshots privados al
    job; effect intent debe existir antes de tool I/O.
  - Probar concurrent decisions, stale revision, crash en cada boundary,
    response loss, redelivery y terminal exactly-once lógico.

- [ ] T7. Implementar environment, binding, capacity y cleanup stores PostgreSQL. [R1, R3, R9, R10, R13, R15, R16]
  - Reutilizar contract tests de features 31/34 para lifecycle, capacity,
    cleanup item/events y cursors contra PostgreSQL real.
  - Preservar binding sellado, CAS/lease/fence, quota transaction y cleanup
    intent/outbox sin introducir reglas provider-specific en SQL.
  - Probar cross-project, UID/fingerprint drift, concurrent quota, expiry,
    cancellation, restart y cleanup confirmation/capacity release una vez.

- [ ] T8. Implementar correction records y relaciones PostgreSQL. [R1, R3, R10, R13, R15, R16]
  - Crear contract tests de Reserve/Get/Update/Cancel/Budget/Events/Recover y
    parent-child lineage.
  - Implementar constraints de cadena, project/run scope, CAS y events
    append-only sin aceptar child parcial o cross-backend.
  - Probar forks concurrentes, budget race, broken relation, restart,
    redelivery y cancelación del root/child.

- [ ] T9. Implementar inbox transaccional e integrar ingress. [R3, R5, R14, R15, R16]
  - Escribir tests HTTP/webhook de firma/scope/tamaño antes de reserve y ack solo
    después de commit confirmado.
  - Implementar inbox + creación causal de run/event/job con unique key y
    digest; conservar solo envelope allowlisted.
  - Probar same/different digest, timeout/disconnect/unknown commit, dos API
    concurrentes, restart y canarios ausentes de rows/logs/responses.

- [ ] T10. Implementar la queue PostgreSQL y contract fake. [R3, R6, R7, R10, R13, R15, R16]
  - Crear contract suite de Enqueue/Claim/Heartbeat/Complete/Retry/Cancel/Recover
    y un fake concurrent-safe controlado por clock/canales.
  - Implementar selección ordered/limited con `FOR UPDATE SKIP LOCKED`, commit
    corto, lease inclusiva y fencing monotónico.
  - Probar alta concurrencia same/different project, no double-live-lease,
    pagination, stale owner/fence, lease exact boundary y cero locks durante I/O.

- [ ] T11. Implementar scheduler, retries y recovery acotados. [R6, R7, R10, R11, R13, R14, R16]
  - Escribir tests con clock/waiter de transient/permanent/unknown, max attempts,
    backoff/deadline/overflow y restart sin timers en memoria.
  - Implementar `RunOnce` paginado y loop caller-owned para due/expired jobs;
    persistir retry/category/available_at antes de release.
  - Probar cancellation en cada boundary, scan parcial/cursor, two schedulers,
    process loss y late result rechazado por fence.

- [ ] T12. Implementar authority envelopes y protocolo del worker. [R8, R10, R11, R14, R16]
  - Escribir tests de matriz por fase que demuestren ausencia de capabilities,
    targets y secret refs innecesarios.
  - Construir/verificar envelope desde el head/epoch autoritativos; resolver
    secret refs just-in-time tras validar backend epoch/digest, project, phase,
    attempt, fence y expiry.
  - Probar worker/proyecto/fase cruzados, envelope tampered/expired, stale fence,
    secret resolver no llamado en rechazo y canarios ausentes de persistencia.

- [ ] T13. Implementar effect intents y outbox durable. [R3, R9, R10, R11, R14, R15, R16]
  - Escribir tests de Prepare/Claim/Heartbeat/Confirm/Retry para tool,
    publication y cleanup con operación/key estable.
  - Integrar outbox en la misma transacción causal y ejecutar el dispatcher solo
    después del commit; polling sigue siendo autoritativo aunque exista notify.
  - Inyectar crash before/after intent/effect/confirm, response loss, external
    duplicate y destino no reconciliable; verificar logical exactly-once.

- [ ] T14. Implementar cancelación y lifecycle de procesos. [R7, R10, R11, R14, R15, R16]
  - Escribir races controladas cancel-vs-claim/heartbeat/complete/retry y
    cancelación repetida sobre queued/leased/terminal/cleanup.
  - Implementar cancel transaction y workers/API start-readiness-stop-join sin
    goroutines ocultas; no liberar lease de efecto incierto.
  - Probar graceful shutdown, grace agotado, kill simulado, restart conjunto y
    convergencia de cleanup/publication sin duplicados.

- [ ] T15. Implementar selector local e import offline. [R4, R12, R15, R16]
  - Escribir tests de composición local sin PostgreSQL y de rechazo a adapters
    mezclados dentro del mismo run.
  - Implementar freeze/read-only, manifest con expected head, candidato
    PostgreSQL sin autoridad, import idempotente, verificación y cutover
    transaccional que publica una epoch nueva y archiva/sella la anterior.
  - Probar retry/resume, conflict/corruption, import parcial, relación rota,
    crash antes/después de cutover, commit desconocido, dos cutovers concurrentes
    y exactamente un backend activo.
  - Probar rollback pre-cutover por descarte explícito del candidato y rollback
    post-cutover solo mediante nueva importación/epoch verificada; verificar
    origen local archivado/read-only y ausencia de secret values/dual-write.

- [ ] T16. Implementar queries paginadas, retention y observabilidad. [R13, R14, R16]
  - Escribir tests de keyset cursors signed/versioned/project-scoped bajo
    inserciones concurrentes y límites exactos.
  - Implementar retention controller cancelable por lotes con referencias y
    tombstones mínimos; sin policy no borra.
  - Probar active/live reference preservation, rerun idempotente, partial batch,
    sink failure no autoritativo y canarios ausentes de logs/metrics/traces.

- [ ] T17. Construir y ejecutar el gate PostgreSQL aislado. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15, R16]
  - Crear comando/build tag documentado que exija DSN autorizado, genere
    database/schema único y limpie solo recursos cuyo ownership creó.
  - Ejecutar migraciones, adapters, concurrency, restart, redelivery, expired
    lease, cancellation, recovery, idempotency, import, cutover epochs, stale
    workers y exactly-one-active-backend con workers controlados.
  - Registrar `R1–R16 → test concreto`, comandos, versiones PostgreSQL, coverage
    y riesgos en `progress/persistent-control-plane/implementation.md`.
  - Ejecutar gofmt/comments/diff audit, vet, compile-only, global normal/race,
    focal race repetido, gate PostgreSQL y medir ≥85% statements sin redondear;
    un skip por DSN ausente no cuenta como gate aprobado.
