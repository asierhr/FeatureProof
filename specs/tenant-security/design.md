# Seguridad y aislamiento multi-tenant — Design

## Resumen de la solución

Se incorpora un módulo cohesionado `internal/tenantsecurity` que convierte una
identidad autenticada por composición en un `Principal` y un `TenantScope`,
evalúa RBAC sobre acciones cerradas y emite capabilities selladas para los
consumers existentes. El módulo también posee administración tenant, secret
metadata/envelopes, auditoría, políticas de retención y reservas de quota. Los
adapters locales y PostgreSQL implementan contratos consumer-owned equivalentes.

La regla central es **scope before lookup**: toda entrada obtiene principal,
tenant, project, acción y resource class antes de consultar el recurso. Ningún
consumer vuelve a derivar organization, membership o permisos desde un ID. El
scope confirmado se propaga a config bindings, runs, jobs, authority envelopes,
packages, environments, caches, objects, logs y métricas.

La feature no implementa login, UI, IAM o KMS gestionado. Recibe un
`AuthenticatedSubject` y puertos de key/decrypt/transport desde composition.
El modelo no recibe acceso a estas fronteras.

## Fuentes de contexto consultadas

- `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md`.
- `docs/architecture.md`: ARCH-001–ARCH-012, hexagonalidad, autoridad del
  journal, límites y determinismo alrededor del modelo.
- `docs/conventions.md`: interfaces consumer-owned, context, concurrencia,
  errores tipados, secretos, comments y tests aislados.
- `feature_list.json`, feature 37 y sus ocho acceptance criteria.
- `specs/persistent-control-plane/`: project-scoped stores, queue, authority
  envelopes, epochs, inbox/outbox, retención y gate PostgreSQL.
- `specs/project-configuration/`: `ProjectKey`, authorities por acción,
  organization policy snapshots, secret refs y run bindings.
- Implementación existente en `internal/projectconfig`,
  `internal/controlplane/postgres`, `internal/validationworkflow`,
  `internal/externalagent` y adapters de entornos/evidencias/packages.
- No se consultó ni se adopta un contrato SaaS/IAM/KMS externo.

## Reglas arquitectónicas aplicadas

- La identidad autenticada es una entrada de composition; el dominio no lee
  tokens, headers, env vars o claims de proveedores.
- Autorización, secrets, audit, retention y quota son deterministas y quedan
  fuera del modelo (ARCH-001, ARCH-004, ARCH-011).
- Los puertos pertenecen al consumer y reciben `context.Context` primero
  (CONV-003, CONV-004).
- Los stores nunca reciben plaintext secret; los observers nunca son autoridad
  (ARCH-009).
- Mutaciones sensibles y audit se confirman causalmente; events append-only
  siguen siendo source of truth (ARCH-007).
- No hay fallback entre local/PostgreSQL ni lookup global por IDs.
- No se introduce microservicio ni frontera de red nueva.
- No se solicita excepción a arquitectura o convenciones.

## Convención obligatoria de comentarios

Cada entidad/tipo, función, método y test helper nuevo o modificado incluirá un
comentario breve que explique su función en FeatureProof, la frontera que
representa o el invariante que protege. Constantes/variables de paquete se
comentan cuando su responsabilidad no sea evidente. No se añaden comentarios
mecánicos a variables locales, parámetros o receivers. La auditoría combina
`go doc`, inventario del diff y muestreo manual; boilerplate interpolado no
cuenta.

## Áreas y ownership

| Área | Responsabilidad |
|---|---|
| `internal/tenantsecurity` | Entidades tenant, catálogo de acciones, identity mapping, RBAC, admin service, secret service, audit, retention y quota. |
| Consumers existentes | Definen puertos mínimos (`PrincipalResolver`, `Authorizer`, `SecretLeaseResolver`, `QuotaReserver`) y exigen capabilities antes de sus stores. |
| `internal/controlplane/postgres` | Adapter PostgreSQL, migrations, constraints, transacciones, locks/receipts y contract gate. |
| Adapter local de tenant security | Aggregate/journal cifrado y checksummed, locks, fsync/rename, cursor sellado y contract suite. |
| `cmd/featureproof` | Composition root: identity/key/transport providers, selección local/PG y wiring de superficies. |
| Ingress/worker adapters | Traducen identidad confiable, propagan `TenantScope` sellado y no aceptan overrides del payload. |

`tenantsecurity` no importa CLI, GitHub, Kubernetes, pgx, SDK de identidad ni
KMS. Los adapters dependen de sus tipos/puertos; los consumers no dependen de
adapters concretos.

## Modelo de dominio

### Identidad y scope

```text
Organization
  ID, state, version, policy_digest, created_at, digest

ProjectRegistration
  OrganizationID, ProjectID, state, version, repository bindings, digest

Principal
  ID, kind(human|service), subject_ref_digest, state, version, digest

Membership
  OrganizationID, PrincipalID, project scopes, role IDs,
  state(active|suspended|revoked), valid_from/until, version, digest

ServiceIdentity
  PrincipalID, owner principal, purpose, expiry, state, version, digest

Role
  OrganizationID, RoleID, closed Action set, resource classes,
  optional project scope, version, digest

TenantScope
  OrganizationID, ProjectID, RepositoryID?, PrincipalID,
  membership/policy versions+digests, issued_at, expires_at, digest
```

IDs usan formato canónico y límites composition-owned. Project registration es
la única relación entre el `ProjectID` existente y organization. No se permite
reasignar; mover un proyecto requiere alta nueva/migración explícita fuera de
esta feature.

### Catálogo cerrado de acciones

El catálogo es un enum versionado en código, no strings arbitrarios. Incluye al
menos clases y acciones para:

- run: admit/read/cancel/approve;
- project configuration: validate/publish/rollback/read/export;
- evidence/package: write/read/delete;
- environment/capacity/cleanup: reserve/read/cancel/release;
- secret reference: create/rotate/revoke/resolve/read-metadata;
- tenant administration: project/member/service identity/role/policy;
- audit: read/export/verify;
- retention: plan/execute;
- quota: read/reserve/release/override.

Los consumers solicitan una acción concreta; no existe `admin.*` wildcard ni
acción derivada del modelo. El policy compiler valida roles y produce snapshots
canónicos/digestados.

### Secretos

```text
SecretMetadata
  Scope, ReferenceID, Version, State, Purpose, KeyReference,
  Algorithm, CreatedAt, RotatedFrom?, Digest

EncryptedEnvelope
  MetadataDigest, KeyReference, Algorithm, Nonce, Ciphertext, AADDigest

SecretLease
  Scope, RunID, Phase, Consumer, ReferenceID, Version,
  Purpose, Fence, IssuedAt, ExpiresAt, SingleUseID, Digest
```

`KeyProvider.Encrypt/Decrypt` es un puerto privado. AAD canonical incluye
organization, project, reference, version, purpose, metadata digest y algorithm.
Los stores solo ven `SecretMetadata` y `EncryptedEnvelope`. Los tipos que
contienen plaintext se limitan al stack/callback de resolución y no implementan
serialización/logging.

### Auditoría

```text
AuditEvent
  TenantSequence, PreviousDigest, Scope, Actor/PrincipalKind,
  Action, ResourceClass, ResourceIDDigest, OperationID,
  Outcome, ReasonCode, At, PolicyDigest, EventDigest

AuditHead
  OrganizationID, LastSequence, LastDigest, Version
```

El digest usa canonical bytes. PostgreSQL impone sequence/head CAS y append-only
por tenant. Local escribe el aggregate de seguridad, event y receipt mediante
un único journal/manifest atómico. Export verifica toda la cadena/página y usa
cursor HMAC tenant-scoped.

### Retención y eliminación

`RetentionPolicySnapshot` contiene version/digest, clases, minimum/maximum age,
batch/page limits, protected record classes y tombstone duration. `DeletionIntent`
fija scope, resource identity/digest, policy digest, eligibility boundary,
state, fencing y receipt. No transporta path o SQL aportado por usuario.

Estados:

```text
prepared -> deleting -> reconciliating -> confirmed
    |            |              |
    +---------- blocked/cancelled (recuperable según causa)
```

Project deletion añade primero `deleting` al project registration, impide nuevas
admisiones y revoca service identities/unused secret leases. Cada consumer
declara referencias vivas y ejecuta su delete tipado; el controller no tiene un
delete genérico.

### Quotas y rate limits

`QuotaPolicySnapshot` define limits tenant/project por resource kind, window,
burst y maximum outstanding. `QuotaDemand` usa cantidades enteras acotadas;
`QuotaReservation` contiene operation key/digest, window identity, reserved
amount, state, expiry y release receipt. No incluye precios monetarios.

El tenant y project limit se evalúan juntos; gana el más restrictivo. Los
contadores usan máximo durable común a local/`bigint`, sin wrap. Una reservation
incierta no se libera hasta reconciliación.

## Puertos consumer-owned

Los nombres definitivos pueden ajustarse al vocabulario de cada paquete, pero
la forma mínima prevista es:

```text
AuthenticatedPrincipalResolver
  Resolve(ctx, AuthenticatedSubject, TrustedResourceIdentity) -> PrincipalContext

ActionAuthorizer
  Authorize(ctx, PrincipalContext, Action, ResourceIdentity) -> Capability

TenantBindingResolver
  BindAdmission(ctx, Capability, ProjectKey, RunIdentity) -> TenantRunBinding
  LoadRunBinding(ctx, Capability, RunIdentity) -> TenantRunBinding

SecretLeaseIssuer
  Issue(ctx, Capability, SecretUse) -> SecretLease

SecretConsumer
  WithSecret(ctx, SecretLease, callback/private adapter) -> SafeOutcome

QuotaReserver
  Reserve(ctx, Capability, Demand, OperationIdentity) -> Reservation
  Release(ctx, Capability, Receipt, TerminalProof) -> ReleaseReceipt

AuditReader / RetentionCandidateSource / ScopedObjectStore / ScopedCache
  operaciones explícitamente tenant-scoped
```

No se crea un `SecurityManager` genérico. El application service administrativo
usa un `Store` cohesionado que aplica commands + audit en una transacción.

## Flujo de identidad y autorización

1. El adapter autentica externamente y entrega `AuthenticatedSubject` ya
   verificado más una resource identity confiable (repo installation, CLI
   profile, worker envelope, etc.).
2. `PrincipalResolver` busca el subject mapping por issuer+subject digest,
   comprueba principal/membership/service identity vigentes y construye scope.
3. Antes del lookup del recurso, el consumer solicita `Authorize(action,
   resource identity)`.
4. RBAC verifica tenant/project, action/resource class, role/policy versions,
   expiry y límites. Produce capability sellada o deny indistinguible.
5. El consumer verifica capability y scope nuevamente al entrar en su adapter.
6. Mutación, receipt y audit se confirman causalmente. Observabilidad ocurre
   después y no cambia el resultado.

El cache de decisiones, si existe, usa key completa por digests y expiry corta;
revocation increments una security revision incluida en la key. Un cache miss
reevalúa; nunca usa una decisión de otro scope.

## Integración con project configuration y runs

- `ProjectRegistration` liga cada `ProjectKey.ProjectID` a organization.
- `OrganizationPolicySource` de feature 36 se implementa desde el snapshot
  tenant policy autorizado; tenant/version/digest entran en effective binding.
- `AdmissionResolver` recibe capability, no una authority creada desde flags.
- Binding tenant + project configuration + run + primer event/job se confirma
  en la misma transacción PostgreSQL o publicación local atómica.
- Aliases no pueden cambiar scope, secret refs, roles o quotas.
- Recovery usa binding histórico, pero secret lease y privileged action se
  validan contra revocation/security revision vigente.

Esto mantiene reproducibilidad sin permitir que un run antiguo conserve acceso
a una credencial revocada.

## Integración de stores, queue, caches y objetos

### Stores

Todas las filas/archivos tenant-owned añaden organization/project canónicos.
PostgreSQL usa composite PK/FK/unique indexes; cada query incluye ambos
predicates. Para nuevas tablas se aplica RLS como defensa adicional cuando la
transacción puede establecer `SET LOCAL` desde scope confiable. RLS no sustituye
predicates ni se habilita mediante valores de request.

### Queue/workers

Jobs, claims, leases, inbox/outbox, effects y authorities incluyen
`OrganizationID`. Claim recibe scope permitido; complete/heartbeat/cancel
verifican organization+project+run+fence. Worker no dispone de un lookup global
por `RunID`.

### Caches

Se introduce un tipo `ScopedCacheKey` construido solo desde `TenantScope` y
resource/version/digest. Los caches tocados por la feature migran a esa key. Un
test estático/dinámico impide usar raw resource ID como key tenant-owned.

### Objetos/packages/evidencias

Object keys son derivados internamente de scope+resource ID validado; metadata
incluye scope y content digest. `Get/Delete` vuelve a comparar metadata y
capability. Un signed URL público/cross-tenant no forma parte de esta feature.

### Logs/métricas/traces

Solo aceptan un `Observation` construido desde capability. Organization/project
se emiten como IDs allowlisted o pseudonymous digests de cardinalidad acotada.
No aceptan labels libres del payload.

## Secrets: escritura, uso, rotación y revocación

### Crear/rotar

1. Autorizar antes de lookup y validar command/idempotency/limits.
2. Obtener policy/key reference y random nonce desde puertos confiables.
3. Cifrar plaintext fuera de la transacción store usando AAD canónica.
4. Abrir transacción corta; revalidar expected version/security revision.
5. Insertar immutable version/envelope, activation state, receipt y audit.
6. Commit; descartar plaintext/cipher buffers y observar metadata segura.

Si falla antes de commit, no hay secret activo. Ciphertext calculado pero no
persistido se descarta; no necesita compensación externa.

### Resolver JIT

1. Autorizar `secret.resolve` y confirmar una lease single-use con audit intent.
2. Cargar metadata/envelope exactos por scope y validar digest/revocation.
3. Decrypt fuera de transacción; no entregar todavía al consumer.
4. Confirmar consumo de lease y audit outcome por CAS.
5. Invocar el adapter privado con el buffer; después descartarlo. El callback
   solo devuelve resultado safe, nunca el valor.

Un failure de key provider confirma outcome seguro cuando sea posible y nunca
entrega plaintext. No hay fallback a otra versión/ref/env var.

### Rotar/revocar

Rotation crea versión y activation nuevas. Revocation avanza revision y marca
leases no consumidas inválidas en la misma transacción que audit. Runs
históricos conservan metadata pero una nueva resolución falla `revoked`.

## Auditoría y transacciones sensibles

El `SecurityStore.Apply(command)` recibe un command ya autorizado y datos
canónicos. En PostgreSQL ejecuta command-specific SQL, receipt y audit chain
update bajo isolation serializable/CAS; no hay callbacks de red dentro. En
local toma lock multiproceso, verifica manifest, aplica a copia, sella evento y
publica por temp+fsync+rename.

Denials auditables usan un operation identity independiente. El resource store
no se toca. Si audit de denial falla, el deny permanece y se devuelve una
categoría operacional que no revela existencia.

## Cuota antes del efecto

```text
authenticated request
  -> authorize
  -> quota Reserve (tenant + project atomically)
  -> existing capacity/model/worker reservation or call
  -> terminal/reconcile proof
  -> quota Release exactly once
```

Si el consumer reserve falla con ausencia confirmada, se libera mediante
receipt. Si el resultado es unknown, la quota permanece reservada. En
PostgreSQL, integraciones que comparten pool pueden confirmar quota+job/capacity
en la misma transacción consumer-owned; de lo contrario se usa intent/outbox y
reconciliación, nunca liberación optimista.

## Retención y eliminación

Un planner pagina candidates dentro de scope y policy snapshot. Antes de cada
intent verifica terminalidad, refs, holds/protected classes, current version y
quota/secret/audit constraints. El executor llama a deletes tipados con
idempotency key y expected digest. Tombstone+audit cierran el intent.

Orden orientativo de project deletion:

1. CAS project a `deleting`, bloquear admission/admin no necesaria;
2. revocar service identities y leases no consumidas;
3. cancelar/converger queue, environments y cleanup;
4. purgar secret ciphertext elegible, operational objects, packages/evidence y
   runs según dependencias;
5. conservar audit/tombstones/receipts mínimos;
6. CAS project a `deleted` con digest final.

El orden concreto lo fija el dependency graph del candidate, no paths ni SQL
dinámico. Los batches y graph work tienen máximos previos a copiar/recorrer.

## Persistencia local

El root local usa organization/project segments derivados de IDs validados. El
security aggregate mantiene manifests, relationships, encrypted envelopes,
quota reservations, retention intents, audit events y receipts checksummed.
Locks multiproceso, temp files, fsync y rename garantizan publicación atómica.
Recovery verifica digests/chain y falla cerrado; no reconstruye scope desde el
directorio ni repara corrupción.

## Persistencia PostgreSQL y migraciones

Las migrations posteriores a `0020` añaden conceptualmente:

| Tabla | Invariante |
|---|---|
| `organizations` | organization immutable ID, state/version/policy/security revision. |
| `organization_projects` | project pertenece a una sola organization; repository scope validado. |
| `principals`, `subject_mappings` | subject digest único por issuer scope; sin tokens. |
| `memberships`, `roles`, `role_bindings` | composite tenant FKs, states/version/digests. |
| `service_identities` | owner/purpose/expiry/revocation tenant-scoped. |
| `tenant_policies` | snapshots canonical/versioned para RBAC/retention/quota/crypto policy. |
| `secret_metadata`, `secret_envelopes`, `secret_leases` | immutable versions/ciphertext, activation/revocation y single-use CAS. |
| `audit_events`, `audit_heads` | append-only chain/sequence por organization. |
| `quota_reservations`, `quota_counters` | tenant+project windows, receipts y no oversubscription. |
| `deletion_intents`, `security_receipts` | idempotencia, fencing y audit causal. |

Tablas tenant-owned existentes reciben `organization_id` y composite FKs/indexes
mediante migraciones por fases. Durante N/N+1:

1. crear tablas/security mapping y columnas nullable no autorizativas;
2. backfill desde `organization_projects` bajo manifest/receipt y verificar
   counts/relations;
3. triggers controlados pueden derivar `organization_id` para writers N durante
   la ventana, nunca desde payload;
4. activar un `tenant_security_epoch` solo cuando todos los writers/readers
   compatibles estén verificados;
5. desde activation, binarios antiguos fallan startup y se habilitan NOT NULL,
   composite FK y RLS/policies donde aplique.

No existe una ventana donde dos tenants sean operables con un binario que no
comprueba scope. Cutover desconocido se resuelve por receipt/epoch durable. No
hay dual-write local/PostgreSQL.

## Concurrencia, cancelación, retries e idempotencia

- Membership/role/policy/secret/quota/retention counters avanzan por CAS y no
  hacen wrap.
- Transactions son cortas y no realizan decrypt, network, object delete o
  observer I/O.
- Serialization/deadlock retry reejecuta la transaction completa con budget y
  waiter/clock inyectados.
- Locks locales no se mantienen durante callbacks externos.
- Cada operation key se scopea por organization+project+action; replay mismo
  digest converge y digest distinto conflict.
- Context cancellation detiene trabajo no empezado; intent/receipt durable
  gobierna recovery tras cruzar una frontera.
- Concurrent last-slot quota, revocation-vs-resolve,
  role-change-vs-authorize y retention-vs-reference se prueban con barriers.

## Categorías de error

Catálogo cerrado compartido: `invalid`, `unauthenticated`, `unauthorized`,
`not_found_hidden`, `conflict`, `stale`, `revoked`, `rate_limited`,
`quota_exhausted`, `missing_reference`, `incompatible_reference`, `integrity`,
`unsupported`, `cancelled`, `deadline`, `unavailable`, `exhausted`.

Adapters mapean por tipos/SQLSTATE, nunca texto. Permission denied/unavailable
no se convierte en not-found. Las respuestas externas no incluyen role names,
tenant existence, remaining quota de otro scope, raw collaborator errors o
secret metadata no autorizada.

## Observabilidad

`SecurityObserver` recibe facts allowlisted post-commit: operation, category,
outcome, duration, pseudonymous scope digest, resource class, count y policy
version. Audit es separado y autoritativo. Panic/fallo/timeout del observer se
contiene y no altera decisiones o commits. Tests con canaries inspeccionan
events, logs, errors, metrics y traces.

## Límites estructurales

Composition entrega máximos positivos para:

- organizations/projects/memberships/roles/bindings/actions por command;
- subject mappings, role/project scopes y policy bytes;
- secret plaintext/ciphertext/metadata, versions y leases;
- audit event/page/export, cursor TTL y verification work;
- quota dimensions/windows/outstanding receipts;
- retention candidates/batch/dependency edges/DFS work;
- transaction retries, observer calls y payload sizes.

Se validan antes de copy/sort/graph/crypto/store. El máximo durable común es
compatible con PostgreSQL `bigint`; límite+1 falla `invalid`/`exhausted` sin
mutación.

## Estrategia de pruebas

### Unitarias herméticas

- canonical IDs/models/digests, structural limits y defensive copies;
- subject mapping, membership states, service identity expiry;
- RBAC matrix por action/resource/scope y no-store-call on deny;
- capability/binding sealing, expiry/fence/policy drift;
- envelope AAD, canary absence, wrong tenant/version/key, rotation/revocation;
- secret lease single-use, callback-only delivery y cancellation;
- audit chain/tamper/cursor, mutation+audit fault boundaries;
- retention eligibility/dependency graph/cancel/restart;
- quota windows, last slot, release once, unknown effect and overflow;
- scoped cache/object keys, telemetry canaries and observer failure.

Fakes de identity/key provider/crypto usan material efímero controlado y no
persisten plaintext. Interleavings usan channels/barriers y fake clock.

### Contract suite local/PostgreSQL

La misma suite cubre:

- organization/project isolation y same-ID cross-tenant;
- memberships/roles/policies CAS, receipts y restart;
- encrypted secret create/rotate/revoke/resolve metadata (decrypt vía fake
  privado), tamper y no-plaintext storage;
- audit append/chain/tamper/pagination;
- quota concurrent reservation/replay/release;
- retention intent/reference/delete/tombstone;
- corruption, cancellation, unknown commit y error categories.

### Verticales productivas

CLI, API/MCP, GitHub, validation workflow, external-agent y workers prueban
principal resolution/RBAC antes de stores, tenant binding causal, denial
indistinguishable, quota pre-effect, secret JIT y recovery histórico.

### Gate PostgreSQL 16 opt-in

Extiende el gate aislado existente con migrations empty/latest/concurrent,
N/N+1/backfill/security epoch, constraints/RLS, tenant predicates, tx fault
matrix, high-concurrency cross-tenant, restart, tamper, rotation/revocation,
retention y cleanup UUID. Se ejecuta normal y race con DSN explícito; un skip no
cuenta. `go test ./...` permanece hermético.

### Verificación final

- `gofmt -l`, `go vet ./...`, compile-only, global normal/race;
- focales críticas repetidas con race;
- builds Windows/Linux/Darwin aplicables;
- auditoría de secrets/canaries y comentarios sistémicos;
- manifest completo y coverage diferencial exacta `>= 85%` sin redondeo;
- cleanup sin procesos, distros, roots, profiles, plaintext fixtures o caches.

## Alternativas consideradas

- **Confiar en IDs UUID:** descartado; opacidad no es autorización.
- **RBAC solo en handlers:** descartado; workers, recovery y adapters podrían
  saltarlo. Cada consumer y store verifica capability/scope.
- **Role names hardcoded con permisos implícitos:** descartado; el catálogo de
  acciones es cerrado pero role definitions son snapshots versionados.
- **RLS como única defensa:** descartado; no cubre local/caches/objects y un
  session setting incorrecto puede filtrar. Predicates y capabilities son
  obligatorios; RLS es defensa adicional.
- **Secret plaintext cifrado solo por disco/DB:** descartado; stores/backups y
  operadores verían el valor. Se usa envelope encryption antes del store.
- **Key/KMS concreto:** descartado; se mantiene puerto sustituible y tests fake.
- **Guardar secrets en project config/jobs:** descartado; solo references y
  leases mínimas.
- **Audit best effort:** descartado para acciones sensibles; rompería
  explicación e integridad. Telemetry sí es best effort.
- **Liberar quota ante timeout:** descartado; un efecto incierto podría causar
  oversubscription.
- **Borrado genérico por path/tabla:** descartado; cada consumer posee sus refs y
  delete tipado.
- **Activar multi-tenancy durante mixed-version inseguro:** descartado; se usa
  security epoch/cutover verificado.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | tenant models/admin store | relationship/state/bounds/cross-tenant matrix |
| R2 | subject adapters/principal resolver | all surfaces, spoofed payload, ambiguity/cancel |
| R3 | action catalog/RBAC service | role/action/resource/project matrix, no store call |
| R4 | capability/run binding | causal admission, phase minimization, drift/revocation |
| R5 | local/PG stores | same-ID isolation, cursor, predicates, corruption |
| R6 | queue/cache/object/worker boundaries | cross-tenant claim/cache/object/redelivery |
| R7 | projectconfig catalog/policy source | project relation, cross-scope refs, historical binding |
| R8 | secret service/key provider/envelope store | canaries, AAD tamper, fault boundaries |
| R9 | secret leases/private consumer | single-use/scope/expiry/cancel/no leakage |
| R10 | rotation/revocation state | CAS, replay, active lease race, historical run |
| R11 | composition/transport validator | production insecure reject, isolated loopback, redaction |
| R12 | audit journal/query | chain/tamper/order/cursor/export/canaries |
| R13 | transactional admin/secret/quota stores | boundary faults, unknown commit, denial audit |
| R14 | tenant policy/eligibility | floors, refs/protected records, policy drift |
| R15 | retention controller/deleters | intent/restart/races/project deletion/cancel |
| R16 | quota service/consumer integration | last slot, hierarchy, pre-effect, release/unknown |
| R17 | services/adapters | typed errors, retry/cancel/overflow/no fallback |
| R18 | observer/log/metric adapters | canaries, bounded labels, sink panic/failure |
| R19 | local/PG adapters/migrations/all diff | shared contract, N/N+1, mode selection, comment audit |
| R20 | verification harness | adversarial verticals, global/race/build/coverage/PG gate |

## Decisiones cerradas

- Feature 37 parte de feature 36 y refuerza `ProjectKey`; no reemplaza sus
  versions/bindings ni el persistent control plane.
- `OrganizationID` y `ProjectID` son scopes separados y obligatorios en estado
  tenant-owned.
- Authentication provider, key provider y remote transport son composition
  ports; no se fija vendor.
- Plaintext no cruza stores, journals, jobs, evidence, model context o
  observability.
- Audit y quota son durables y causales; telemetry no es autoridad.
- Security epoch impide activar multi-tenancy mientras existan binarios sin
  soporte de scope.
- UI, analytics/history y billing monetario permanecen fuera de alcance.
