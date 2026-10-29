# Control plane persistente y distribuible — Design

## Resumen de la solución

La feature añade un plano de persistencia PostgreSQL debajo de los contratos de
dominio existentes y un scheduler durable encima de una tabla de jobs. Cada
caso de uso conserva ownership de su interfaz y sus invariantes; el adapter
comparte infraestructura transaccional, migrations e instrumentos, no un
repositorio genérico.

Una transacción causal escribe proyección + evento + job/outbox. Los workers
reclaman jobs brevemente mediante row locking, salen de la transacción con una
lease/fence y ejecutan I/O solo con un envelope mínimo. Inbox y outbox cierran
las dos fronteras no atómicas: entrada externa antes del ack y efecto externo
después del commit. Local y PostgreSQL se eligen explícitamente; cada run posee
un historial append-only de epochs y exactamente una epoch activa señalada por
CAS.

```text
external request/webhook
        │ verify input/signature/scope
        ▼
  inbox transaction ── run/event/job committed ──► ACK
                              │
                              ▼
                    durable ordered queue
                              │ claim + lease + fence
                              ▼
                 phase-scoped authority envelope
                              │
                 ┌────────────┴────────────┐
                 ▼                         ▼
          internal computation     intent/outbox commit
                                           │
                                           ▼
                                   external side effect
                                           │
                                           ▼
                                fenced terminal transaction
```

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md` | Una feature, SDD, puerta humana, trazabilidad y outputs permitidos. |
| `docs/architecture.md`, `docs/conventions.md`, `docs/verification.md` | Interfaces del consumidor, intent-before-effect, límites/cancelación, errores tipados, tests herméticos/race/coverage. |
| `feature_list.json` | Entidades obligatorias, PostgreSQL, queue, workers, persist-before-ack, autoridad mínima y modo local. |
| `internal/validationworkflow/*` | `WorkflowStore`, events append-only, CAS/lease, cancelación, plan/package y cleanup intent. |
| `internal/planapproval/*` | `ApprovalStore`, ordering, snapshots privados, revision CAS y journal de aprobación. |
| `internal/functionalflow/*` | `IntentStore`, `EventJournal`, claim/terminal, effect intent y reconciliación. |
| `internal/environment/*`, `internal/environment/cleanup/*` | lifecycle/binding, capacity, cleanup store, cursors, leases/fencing y separación del verdict. |
| `internal/correction/*` | reserva lineal, relaciones parent/child, budget, events y recovery. |
| `internal/githubpr/*` y adapters API/webhook | delivery receipts, associations, publication intents, leases y redelivery. |
| Specs 31–34 | Provider-neutral lifecycle, Kubernetes, tools read-only y cleanup distribuible mediante los mismos ports. |
| Feature 36/37 en `feature_list.json` | Configuración versionada y tenant security quedan explícitamente fuera. |
| PostgreSQL `SELECT` | `FOR UPDATE ... SKIP LOCKED` es apropiado para consumidores tipo queue, aunque ofrece una vista inconsistente y requiere orden/budget explícitos. |
| PostgreSQL MVCC e isolation | Locks, snapshots e isolation se diseñan por transacción; el dominio no mantiene locks durante I/O. |
| PostgreSQL serialization failures | `40001` y, cuando corresponda, `40P01` se reintentan como transacción completa, con budget. |
| PostgreSQL error codes | Los adapters clasifican por SQLSTATE, no por texto del mensaje. |
| PostgreSQL `INSERT ... ON CONFLICT` | La reserva idempotente se apoya en constraints únicas y resultado verificado, no en check-then-insert. |
| PostgreSQL `NOTIFY` | Las notificaciones se entregan tras commit, pero son solo wake-up; no sustituyen la tabla durable. |

Fuentes PostgreSQL oficiales:

- <https://www.postgresql.org/docs/current/sql-select.html>
- <https://www.postgresql.org/docs/current/mvcc.html>
- <https://www.postgresql.org/docs/16/mvcc-serialization-failure-handling.html>
- <https://www.postgresql.org/docs/current/errcodes-appendix.html>
- <https://www.postgresql.org/docs/current/sql-insert.html>
- <https://www.postgresql.org/docs/15/sql-notify.html>

## Reglas arquitectónicas aplicadas

- ARCH-001/002/011: solo fases/capabilities registradas y policy admitida crean
  jobs; un worker no amplía autoridad a partir de IDs.
- ARCH-003/004/012: SQL y workers no generan assertions/verdict ni mutan el run
  por fuera de los casos de uso; persisten comandos/resultados validados.
- ARCH-005/006: páginas, transacciones, claims, leases, payloads, retries,
  shutdown y retention son acotados y propagados por `context.Context`.
- ARCH-007: inbox, events, intents, jobs y outbox son durables antes de depender
  de logs, memoria o procesos.
- ARCH-008/009: payload externo, errores SQL/remotos y worker output son no
  confiables; se validan, acotan y redactan.
- ARCH-010: unit tests continúan herméticos; PostgreSQL real vive en un gate de
  integración explícito y aislado.
- CONV-001..010: nombres ingleses, puertos consumer-owned, ownership explícito,
  errores tipados, goroutines poseídas por caller y ninguna espera real en tests.

No se propone ninguna excepción.

## Convención obligatoria de comentarios

Toda entidad o tipo, función, método y helper de test nuevo o modificado incluirá
un comentario breve sobre su responsabilidad sistémica, frontera o invariante.
También lo incluirán constantes y variables de paquete cuyo papel compartido no
sea evidente por el tipo, nombre y API contenedora. Abarca símbolos privados,
sentinels, migration descriptors, fakes, factories, fixtures y helpers de
assertion dentro de ese alcance.

Variables locales, parámetros y receivers no se comentan mecánicamente. Cuando
alguno materializa autoridad, fencing, durabilidad u ownership que no resulta
evidente en el tipo y la operación, la implementación debe expresarlo mediante
un tipo u operación más explícitos o documentar puntualmente esa frontera.

La auditoría deriva el inventario del diff completo, comprueba los símbolos
alcanzados y combina análisis estático con muestreo manual. Rechaza
paráfrasis, familias repetidas y variantes de una plantilla interpolada; no
cuenta líneas de comentario como sustituto de utilidad sistémica. Esta
convención concreta la petición original de comentar entidades y funciones y
evita el ruido contrario a `docs/conventions.md` §12.

## Áreas y paquetes previstos

| Área | Responsabilidad |
|---|---|
| `internal/controlplane` | Modelo de backend binding, jobs, authority envelope, inbox/outbox, errores y puertos consumer-owned del scheduler. |
| `internal/controlplane/postgres` | Pool/transactions, migrator, SQLSTATE mapping, queue/inbox/outbox y adapters de contratos. |
| `internal/controlplane/local` | Composición explícita de los stores locales existentes bajo el mismo selector; nunca emula distribución. |
| `internal/validationworkflow` | Adapter PostgreSQL de `WorkflowStore`, events, artifacts/packages y binding del run; semántica intacta. |
| `internal/planapproval` | Adapter PostgreSQL de approvals y snapshots privados. |
| `internal/functionalflow` | Persistencia PostgreSQL de intents, terminals y event journal; bridge de jobs por fase. |
| `internal/environment` y `internal/environment/cleanup` | Adapters PostgreSQL de lifecycle, bindings, capacity, cleanup, cursor e intents. |
| `internal/correction` | Adapter PostgreSQL para records, relaciones y events de correction. |
| `internal/githubpr` / ingress API | Inbox persist-before-ack y outbox de publicación sobre contratos existentes. |
| `cmd/featureproof` | Composición explícita backend/migrations/API/scheduler/workers y lifecycle start/cancel/join. |
| `internal/controlplane/postgresintegration` o tests tagged | Harness PostgreSQL aislado y workers controlados, nunca importado por producción. |

La ubicación final puede subdividir el adapter para evitar ciclos, pero no puede
mover interfaces de dominio a un paquete de infraestructura ni crear un
`Repository` genérico.

## Modelo relacional propuesto

Todas las tablas viven en un schema dedicado configurable y usan tipos SQL
estrechos para identidad, scope, estado, revision y tiempo. `jsonb`/`bytea` se
reservan para documentos canónicos versionados cuyo contrato ya pertenece al
dominio; no sustituyen constraints ni columnas indexables.

| Tabla lógica | Contenido e invariantes |
|---|---|
| `schema_migrations` | version, name, checksum, applied_at y estado; historia lineal e inmutable. |
| `backend_binding_epochs` | `(run_id, epoch)` único, mode, backend identity, schema version, project_id y digest inmutables/append-only. |
| `backend_binding_heads` / `backend_binding_events` | head CAS con la única epoch activa y journal append-only de admit/cutover/archive; el head no duplica autoridad. |
| `runs` | proyección workflow, revision CAS, desired/terminal state, project y digest. |
| `run_events` | `(run_id, sequence)` y event_id únicos, payload canónico/digest; solo INSERT. |
| `approvals` / `approval_events` | snapshot privado cifrado/gestionado por el contrato existente, revision y journal append-only. No secretos nuevos. |
| `environment_records` / `environment_bindings` | lifecycle projection, provider/project/run relations, CAS/digest y binding sellado. |
| `cleanup_items` / `cleanup_events` / `capacity_receipts` | estado/cursor/lease/fence, journal y quota receipts de feature 34. |
| `correction_records` / `correction_relations` / `correction_events` | chain parent-child, lineage/digests, budget y estados. |
| `packages` | documento canónico inmutable y acotado (`bytea`), media/version, digest y relaciones; no path local. |
| `jobs` | backend epoch/digest, phase, state, inputs por referencia/digest, scheduling, attempt, owner, lease_until, fence, cancel revision y terminal. |
| `job_events` | auditoría append-only content-free de enqueue/claim/heartbeat/retry/terminal/cancel. |
| `authority_envelopes` | envelope canónico/digest/expiry unido a job+attempt+fence; referencias de secreto, nunca valores. |
| `ingress_inbox` | source, delivery/request id, idempotency key, project, request digest, receipt y causal run/job. |
| `effect_intents` | operation id/key, phase, binding/input digests y estado previo a tool/provider effect. |
| `outbox` | publicación/efecto externo, idempotency key, payload allowlisted o referencia, lease/fence y terminal. |
| `import_manifests` | source identity, snapshot digest, counts, per-part checksums, state y cutover receipt. |

Las tablas pueden compartir primitivas, pero sus constraints reflejan contratos
específicos. Foreign keys preservan scope/relaciones; unique constraints fijan
idempotency; check constraints fijan states/attempts/times; digests permiten
detectar mutación o decode incompatible. Un load siempre valida versión,
canonical bytes, digest y relaciones antes de devolver dominio.

### Package storage

El package completo confirmado se guarda como documento canónico bounded en
PostgreSQL, con digest calculado antes del INSERT. No se persiste únicamente un
path local. Evidencias grandes que ya pertenezcan a otro contrato permanecen por
referencia/digest; añadir object storage no forma parte de esta feature. El
oversize falla antes de iniciar la transacción causal.

## Contratos consumer-owned

No se cambia la semántica de los puertos existentes. Se añaden solo contratos
en el consumidor que necesita coordinar trabajo:

```text
BackendBinder
  ReserveInitial(run identity, project, backend descriptor) -> epoch, new
  LoadHead(run identity) -> head + active epoch
  Verify(run identity, epoch, digest, adapter identity) -> error
  PublishCutover(expected head, verified candidate) -> new head + epoch

IngressStore
  Reserve(envelope, causal command) -> receipt, new
  Load(source, idempotency key) -> receipt

JobQueue
  Enqueue(job spec) -> job, new
  Claim(owner, selector, now, lease, limit) -> jobs
  Heartbeat(job, owner, fence, now, lease) -> job
  Complete(job, owner, fence, terminal command) -> job
  Retry(job, owner, fence, retry decision) -> job
  Cancel(run, expected revision, intent) -> projection
  Recover(now, page) -> page

OutboxStore
  Prepare(effect intent) -> intent, new
  Claim/Heartbeat/Confirm/Retry(... owner, fence ...)
```

Cada `Complete` recibe un comando ya validado y, cuando debe mutar agregado,
participa en una unidad transaccional del adapter. No recibe callbacks que
hagan I/O dentro de SQL. Los adapters específicos implementan además
`WorkflowStore`, `ApprovalStore`, `functionalflow.IntentStore/EventJournal`,
environment/cleanup stores y `correction.Store` directamente.

## Backend binding por epochs y composición

`BackendDescriptor` contiene `mode`, identidad no secreta del backend, rango de
schema aceptado y digest. `BackendBindingEpoch` añade `run_id`, `project_id`,
generación monotónica y su propio digest. La composición construye exactamente
un `BackendSet` local o PostgreSQL. `ReserveInitial` crea epoch 1 y head en la
admisión; todos los adapters, jobs y envelopes presentan epoch/digest y vuelven
a verificar que el head todavía los señala antes de mutar o confirmar.

Las epochs son append-only. `backend_binding_heads` es la única proyección CAS
mutable y contiene exactamente un puntero activo por run. Un cutover confirmado
inserta la epoch nueva, avanza el head esperado y añade events `cutover` y
`archived` en una sola transacción. La epoch anterior no cambia: queda sellada
por el event/head histórico y pierde autoridad al instante del commit. Dos
cutovers sobre el mismo head no pueden ganar. Un worker con epoch anterior queda
stale incluso si conserva lease/fence; job fence y backend epoch son pruebas
independientes y ambas deben ser actuales.

No existe un adapter que escriba local y PostgreSQL, ni fallback en errores.
Los handles de conexión/DSN viven en composición y jamás entran al descriptor.
Runs locales previos permanecen en su epoch local hasta import verificado y
cutover. Tras el cutover, el filesystem queda archivado/read-only y no recibe
mutaciones.

## Migraciones y compatibilidad operacional

Las migraciones son assets numerados e inmutables con nombre y SHA-256 fijados
en un manifest compilado. El migrator:

1. abre una conexión administrativa acotada;
2. adquiere exclusión PostgreSQL para el schema/migration stream;
3. verifica server/schema, historia, checksums y dirty state;
4. aplica cada migración en una transacción, salvo una excepción declarada en
   el descriptor que exige protocolo expand/contract explícito;
5. registra versión/checksum en el mismo boundary exitoso;
6. libera exclusión y publica únicamente métricas allowlisted.

La aplicación declara `[min_schema, max_schema]`. Los cambios siguen
expand/backfill/contract: N+1 añade estructuras compatibles; backfill es
restartable/paginado; eliminar columnas/constraints ocurre solo cuando N ya no
está soportado. Rollback normal baja binario, no migrations. Checksum alterado,
schema futuro, historia con huecos o permiso insuficiente bloquean readiness.

## Fronteras transaccionales e isolation

La isolation por defecto es `READ COMMITTED` con row-level CAS/unique/check
constraints. Se eleva a `SERIALIZABLE` únicamente en invariantes agregadas que
no pueden expresarse como una fila bloqueada/constraint, por ejemplo capacidad
multi-fila. Cada frontera tiene un callback puro respecto a red/filesystem/
tools y un budget de retries de transacción completa.

Fronteras principales:

- **Ingress:** insert/verify inbox + reserve initial binding epoch/head + run +
  first event + jobs.
- **Transition:** compare revision/digest + update projection + append event +
  enqueue next job/outbox.
- **Claim:** seleccionar IDs elegibles ordered/limited con `FOR UPDATE SKIP
  LOCKED`, incrementar fence, asignar owner/lease y append event; commit rápido.
- **Heartbeat:** update condicionado por owner/fence/state/lease viva.
- **Terminal/retry:** update condicionado + terminal/event + downstream job/
  outbox. El resultado worker se valida antes de entrar a la transacción.
- **Cancel:** CAS run desired-state + cancel queued/retry jobs + event + jobs de
  convergencia/cleanup autorizados.
- **Outbox/inbox:** intent causal comparte transacción interna; la llamada
  externa ocurre siempre después del commit.

No se usa `SKIP LOCKED` para comprobar autoridad, integridad o ausencia. Una
query que no encuentra por lock/snapshot no equivale a NotFound.

## Estados de job y transiciones

```text
queued ──claim──► leased ──complete──► succeeded
   ▲                 │  ├─permanent──► failed/dead
   │                 │  ├─cancel─────► cancelled
   │                 │  └─transient──► retry_wait
   │                 │                    │ available_at reached
   └──lease expiry/recovery───────────────┘

queued/retry_wait ──run cancellation──► cancelled
leased ──cancellation/lease expiry──► stale owner rejected; recovery decides
```

`attempt` cuenta ejecuciones; `fence` avanza en cada ownership. `now >=
lease_until` expira. Un heartbeat nunca puede superar deadline/maximum lease.
Recovery pagina expired leases y retry-wait due; no escanea sin límite. Estados
terminales son inmutables salvo metadata de retención separada.

## Authority envelope y worker protocol

El scheduler deriva el envelope desde records/bindings ya verificados, nunca
desde campos libres del worker:

```text
AuthorityEnvelope {
  version, envelope_id,
  project_id, run_id, job_id, phase,
  backend_epoch, backend_binding_digest,
  attempt, fencing_token, expires_at,
  input_references + digests,
  allowed_capabilities,
  optional target_binding_reference + digest,
  optional secret_reference_ids,
  digest
}
```

La matriz de fase es cerrada y composition-owned. Planning/evaluation no recibe
target ni secretos si no los necesita; execution recibe solo operaciones y
target admitidos; publication recibe el destino/intento exacto; cleanup recibe
solo su binding sellado. Las secret references son IDs opacos y se resuelven
just-in-time por un puerto privado que vuelve a validar project/phase/fence.
Feature 37 podrá añadir tenant constraints sin cambiar que `ProjectID` sea
obligatorio hoy.

## Ingress, inbox y acknowledgement

El adapter HTTP/webhook valida tamaño, forma, firma/autenticación y scope antes
de llamar a `IngressStore.Reserve`. La transacción inserta una unique key
`(source, external_id/idempotency_key)`, request digest y el comando allowlisted;
reserva causal run/job. Si la key existe, compara digest y devuelve el mismo
receipt. Solo después de commit devuelve 2xx/accepted.

Ante resultado de commit desconocido no se envía éxito inventado. El caller
repite la key; el siguiente intento lee el receipt. No se guarda por defecto el
body raw del webhook. GitHub delivery/association existente se adapta a esta
frontera sin alterar su verificación ni publication intents.

## Outbox, efectos e idempotencia

Tool execution, publicación y cleanup conservan sus stores/intents específicos.
Cuando el paso siguiente cruza proceso o red, su intent/outbox se inserta junto
con el estado causal. El dispatcher reclama con lease/fence, llama después del
commit y confirma el resultado por CAS.

La identidad de operación incluye run/revision/phase/step o publication kind y
binding/input digest. Una respuesta perdida reusa la misma key. Antes de repetir
se consulta terminal/reconciliador. Si el destino carece de idempotencia y no
puede reconciliar un resultado incierto, el sistema bloquea/termina de manera
operacional; no duplica a ciegas. `LISTEN/NOTIFY` puede reducir latencia, pero un
poll acotado siempre recupera trabajo si se pierde la notificación.

## Cancelación, shutdown y recovery

Cancelar es un comando durable, no solo `context.CancelFunc`. La transacción
avanza desired state del run, anula queued/retry jobs, inserta evento y conserva
o crea jobs autorizados de convergencia/cleanup. Jobs leased observan la
cancelación en heartbeat/complete. Si un efecto ya quedó confirmado se conserva;
si quedó incierto, su intent se reconcilia.

En shutdown, API deja de admitir después de cerrar readiness; workers dejan de
claimar, cancelan waits/I/O y esperan un grace explícito. Solo pueden liberar o
reencolar por CAS si saben que no cruzaron el efecto. En los demás casos dejan
la lease expirar. Kill abrupto sigue el mismo camino mediante recovery. Un fence
viejo nunca confirma terminal aun si la respuesta llega tarde.

## Migración desde almacenamiento local

La migración es una operación offline explícita con candidato sin autoridad:

1. valida versión y adquiere exclusión sobre el root local;
2. lo pone read-only/frozen; la epoch local continúa siendo el único head activo
   y construye manifest canónico con source identity, expected head/epoch,
   counts, relations y per-stream digests;
3. crea un candidato PostgreSQL ligado al import pero sin autoridad de backend,
   e importa en orden de dependencias con `import_id` estable y transactions
   paginadas; mismo import/bytes converge y discrepancias abortan;
4. verifica events/projections, digests, foreign keys y counts en PostgreSQL y
   registra el receipt candidato `verified`;
5. el operador solicita cutover; una única transacción compara el head local
   esperado, inserta la nueva epoch PostgreSQL, avanza el head y añade los events
   de cutover/archive;
6. desde ese commit solo la epoch PostgreSQL acepta operaciones; el origen queda
   archivado/read-only, nunca sincronizado.

El freeze deja en el origen un receipt durable que obliga al adapter local a
rechazar nuevas mutaciones mientras el import está abierto. El candidato copia
ese expected head/receipt al registro de cutover de destino. La transacción de
cutover valida ambos digests y publica el nuevo head; el receipt local no se
elimina ni se vuelve writable después de un cutover confirmado.

Un import fallido o crash pre-cutover puede reanudarse, pero no concede autoridad
a datos parciales. Un crash con commit desconocido consulta head y cutover
receipt: si avanzó, no reescribe local; si no avanzó, el candidato sigue sin
autoridad. Dos cutovers concurrentes compiten por el mismo head y uno falla CAS.

Antes del cutover, rollback elimina/abandona el candidato de forma explícita y
puede descongelar la epoch local. Después del cutover no se revive esa epoch:
volver a local exige snapshot/import verificado en sentido inverso y publicar
otra epoch monotónica mediante el mismo CAS. No se migran secret values. El
comando y UX concretos pertenecen a composición, no al modelo de dominio.

## Paginación y retención

Listados usan keyset cursor sellado con versión, `ProjectID`, filtros, snapshot
boundary cuando el contrato exige una vista estable, last ordering key y digest.
Nunca usan offset para recovery autoritativo. Cada API fija `limit <= max` y
orden total estable. Un cursor cross-project/alterado falla antes de query útil.

Retention es un controller independiente y cancelable. Solo considera terminales
fuera del periodo composition-owned y sin referencias vivas. Borra por lotes y
en orden dependiente, preservando inbox/idempotency tombstones, migration ledger
y summaries exigidos por auditoría. Sin policy explícita no borra nada. Esta
feature no define periodos comerciales ni tenant-specific retention.

## Errores y observabilidad

Categorías cerradas: `invalid`, `not_found`, `conflict`, `unauthorized`,
`integrity`, `cancelled`, `deadline`, `transient`, `unavailable`, `exhausted` y
`internal`. El adapter usa SQLSTATE y contexto de operación, nunca parsing de
mensajes. `40001` y `40P01` son candidatos a retry de transaction completa;
constraints conocidas se traducen según el contrato; schema/checksum/decoding
son integrity; conexión es unavailable/transient bajo budget.

Métricas propuestas: transaction latency/retry by operation/category, pool
usage, queue depth/age por phase, claim/heartbeat/lease-expiry, attempts,
cancel-to-terminal, inbox duplicate/conflict, outbox pending/age, migration
version/failure y retention count. Logs/traces usan IDs o digests allowlisted,
project/phase/category/counts; no DSN, SQL payload, package bytes, target,
endpoint, raw remote error, secret value ni token. Telemetría nunca es fuente de
verdad ni participa en commit.

## Estrategia de pruebas

### Unitarias herméticas (`go test ./...`)

- Modelo/canonicalización/digests/backend epochs/heads/authority envelopes.
- Scheduler y worker loop con store falso contractual, clock, waiter, ID source
  y barreras por canales; sin `time.Sleep`.
- Matrices de cancelación, shutdown, retry, budget, stale fence, authority
  minimization, redelivery y effect reconciliation.
- Ingress/outbox con transactor fake que inyecta before/after/unknown commit.
- Local selector e import planner con filesystem temporal controlado.
- SQLSTATE mapping y migration manifest sin abrir red.

### Integración PostgreSQL explícita

Un build tag o comando dedicado requiere un DSN de test proporcionado
explícitamente y restringido. El harness crea un database/schema con UUID bajo
esa instancia, registra ownership, aplica migrations y elimina únicamente ese
namespace al terminar. Nunca infiere localhost/producción y audita que el target
permite tests destructivos.

La suite cubre:

- migrate empty/latest/idempotent, checksum/dirty/future version y dos migrators;
- adapters de todas las entidades, canonical reload y constraints;
- atomic rollback en cada statement boundary;
- competing CAS/enqueue/claim bajo alta concurrencia y `SKIP LOCKED`;
- API/worker restart, expired lease, heartbeat, stale fence y late response;
- inbox same/different digest, unknown commit y persist-before-ack;
- tool/publication/cleanup outbox response-loss y logical exactly-once;
- cancellation races, shutdown/kill recovery y transaction retries;
- pagination/cursors y retention safe references;
- import retry/conflict/verification, crash pre/post-cutover, dos cutovers,
  worker stale y exactly-one-active-backend sin dual-write.

Workers controlados esperan en canales tras claim/intent/effect/terminal para
producir interleavings exactos. El gate se ejecuta normal y race donde sea
aplicable; cada test usa timeout y cleanup propio. Skips por DSN ausente no
cuentan como resultado de aprobación del gate.

## Alternativas consideradas

- **Repositorio SQL genérico:** descartado; mezcla ownership y facilita saltar
  invariantes de cada consumidor.
- **Queue externa ahora:** descartada; la aceptación exige PostgreSQL y una
  tabla queue/outbox conserva atomicidad con el estado causal. Un broker futuro
  puede consumir outbox sin ser source of truth.
- **`LISTEN/NOTIFY` como queue:** descartado como autoridad porque las
  notificaciones no son un log durable ni sustituyen recovery.
- **Locks/advisory locks durante I/O:** descartado; reduce disponibilidad y no
  protege de procesos pausados. Lease + fence + idempotency es verificable.
- **Dual-write local/PostgreSQL:** descartado por divergencia imposible de
  resolver de forma genérica. Se usa snapshot offline y cutover.
- **Guardar secrets en job cifrados:** descartado; un gestor/cifrado no es
  alcance. Solo refs mínimas y resolución privada just-in-time.
- **Exactly-once físico:** descartado cuando el destino no lo ofrece. Se
  especifica logical exactly-once y estado incierto fail-closed.
- **Offsets para pagination:** descartados por drift y coste; keyset cursor.
- **Down migrations automáticas:** descartadas por riesgo de pérdida; rollback
  de binario dentro de ventana y restore/migración explícitos.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | adapters específicos, packages | round-trip por entidad, contract tests |
| R2 | migration manifest/runner | empty/latest/concurrent/dirty/checksum/compatibility |
| R3 | transactor, projections/events | concurrent CAS, rollback injection, tamper |
| R4 | backend epoch/head/selector | initial epoch, exactly one active, concurrent cutover, stale worker, outage no fallback |
| R5 | ingress inbox | ack-after-commit, redelivery/conflict/unknown commit |
| R6 | job queue | concurrent claim, heartbeat, stale fence, ordering |
| R7 | scheduler/recovery | lease expiry, retry budgets, restart, overflow |
| R8 | authority builder/resolver | phase matrix, cross-project/fence/expiry, no secrets |
| R9 | effect intent/outbox | crash boundaries, response loss, logical exactly-once |
| R10 | cancellation transaction | cancel races, active/queued/cleanup convergence |
| R11 | worker/API lifecycle | graceful shutdown, kill, late result, restart |
| R12 | local selector/import | candidate without authority, partial import, pre/post-cutover crash/rollback, archived local |
| R13 | query/retention controllers | keyset drift, invalid cursor, live refs, batches |
| R14 | error mapper/telemetry | SQLSTATE matrix, tx retry, cancellation, canaries |
| R15 | integration harness/fakes | real PostgreSQL matrix and controlled workers |
| R16 | all touched areas | doc/static/diff audit for every changed symbol |

## Decisiones cerradas y compatibilidad futura

- PostgreSQL es source of truth en modo distribuido; un broker futuro es un
  delivery optimization, no cambia commits causales.
- `ProjectID` existente es el único scope actual. Feature 37 puede reforzar
  tenant isolation sin que esta spec invente org/RBAC.
- Todos los límites/policies entran por composición. Feature 36 podrá proveerlos
  de forma versionada; esta feature no define su jerarquía.
- Adapters PostgreSQL implementan puertos ya usados por features 31–34, por lo
  que cleanup/provider/tool mantienen comportamiento observable.
- Local sigue siendo un modo de desarrollo explícito, no HA y no fallback. Un
  cambio de backend siempre publica una epoch nueva verificada; nunca muta o
  reactiva una anterior.
