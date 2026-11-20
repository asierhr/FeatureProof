# Seguridad y aislamiento multi-tenant — Requirements

## Objetivo y problema

FeatureProof ya dispone de `ProjectKey`, authorities acotadas, configuración por
proyecto y un control plane local/PostgreSQL, pero todavía no representa qué
organización posee un proyecto, qué identidades pertenecen a ella ni qué
acciones puede realizar cada identidad. Conocer un ID opaco no debe conceder
acceso ni permitir inferir existencia. Esta feature introduce una frontera de
seguridad multi-tenant verificable para admisión, ejecución, administración,
secretos, auditoría, retención y consumo de recursos.

La solución debe preservar runs ya vinculados y los contratos de las features
35 y 36, reforzándolos con un `TenantScope` y una decisión RBAC confirmados. El
modelo nunca participa en autenticación, autorización, resolución de secretos,
cuotas, retención ni auditoría.

## Alcance

- Modelo versionado de organizaciones, proyectos, memberships, identidades de
  servicio, roles, bindings y políticas de tenant.
- Resolución de una identidad autenticada externa a un principal interno; el
  proveedor de autenticación permanece fuera del dominio.
- RBAC fail-closed para las operaciones existentes sobre runs, configuración,
  evidencias/packages, entornos, credenciales, cuotas y administración.
- Scope obligatorio de organización y proyecto en ingress, stores, queue,
  caches, objetos, workers, métricas y logs.
- Secret references opacas, almacenamiento envelope-encrypted, resolución JIT
  de mínimo privilegio, rotación y revocación.
- Auditoría append-only, tamper-evident y no sensible para operaciones
  autorizadas y denegadas que sean auditables.
- Políticas versionadas de retención/eliminación y purga idempotente que
  preserve referencias vivas y registros mínimos obligatorios.
- Rate limits, cuotas y presupuestos durables por organización/proyecto antes
  de reservar worker, modelo, entorno o capacidad.
- Adapters equivalentes local y PostgreSQL, migraciones compatibles y tests
  adversariales/concurrentes.

## Fuera de alcance

- Pantallas, sesiones web, CSRF o UX de administración; pertenecen a feature 39.
- Historial analítico y comparación de runs; pertenecen a feature 38.
- Elegir o implementar un proveedor gestionado concreto de IAM, OIDC, KMS,
  vault, SIEM, object storage o métricas.
- Guardar contraseñas de usuario, emitir tokens de login o verificar protocolos
  de autenticación externos dentro del dominio.
- Billing, invoices, precios monetarios o estimaciones de coste embebidas.
- Atributos ABAC arbitrarios, lenguaje general de políticas o permisos creados
  por prompts. Esta entrega usa RBAC sobre un catálogo cerrado de acciones y
  clases de recurso.
- Compartición pública/cross-tenant de evidencias, proyectos o secretos.
- Hard delete inmediato de auditoría o tombstones que la policy de seguridad
  declare obligatorios.
- Migrar microservicios o introducir una frontera de red nueva.

## Actores y términos

- **Organization/Tenant:** scope superior e inmutable que posee proyectos,
  policies, roles, memberships, cuotas y key references. En esta feature ambos
  términos son equivalentes.
- **Project:** unidad administrable dentro de una sola organización. Un
  `ProjectKey` de feature 36 queda ligado a exactamente un `OrganizationID`.
- **Authenticated subject:** identidad estable entregada por un adapter de
  autenticación confiable; no es un ID aceptado desde el payload del usuario.
- **Principal:** identidad interna sellada, humana o de servicio, derivada de
  un authenticated subject y su tenant membership vigente.
- **Membership:** relación versionada entre principal y organización, con
  estado, roles y alcance de proyectos.
- **Service identity:** principal no humano con owner, propósito, expiración y
  permisos explícitos; no es una credencial ni contiene su valor.
- **Role:** nombre estable que agrupa acciones de un catálogo cerrado. Un role
  binding no puede crear acciones desconocidas.
- **Tenant scope:** tupla sellada `organization_id`, `project_id` y, cuando
  aplica, `repository_id`, ligada a principal, policy/version y digest.
- **Authorization decision:** resultado `allowed` o `denied` con action,
  resource class, scope y reason code estable; no contiene explicaciones del
  modelo.
- **Secret reference:** ID opaco tenant-scoped que apunta a una versión de un
  secreto cifrado. Nunca transporta el valor.
- **Secret lease:** capacidad temporal y de un solo propósito que autoriza una
  resolución JIT para run, fase y consumer concretos.
- **Quota reservation:** receipt durable que descuenta capacidad antes de un
  efecto y se libera una vez al alcanzar el terminal definido.
- **Audit event:** registro append-only y tamper-evident de actor, scope,
  acción, recurso, instante, resultado y reason code allowlisted.
- **Protected record:** audit event, tombstone, receipt o digest mínimo que una
  policy obliga a conservar aunque el payload operativo expire.

## R1 — Modelo tenant y relaciones explícitas

THE tenant security domain SHALL representar organizations, projects,
principals, memberships, service identities, roles y role bindings mediante
identidades canónicas, versiones y relaciones explícitas, sin inferir acceso
por conocer un ID.

### Acceptance criteria

- GIVEN un project WHEN se registra THEN pertenece a exactamente una
  organization y su relación no puede reescribirse hacia otro tenant.
- GIVEN membership o service identity inexistente, suspendida, revocada o
  expirada WHEN se resuelve THEN no produce principal autorizado.
- GIVEN roles, projects o bindings duplicados, desconocidos, cíclicos,
  cross-tenant o por encima de límites estructurales WHEN se publican THEN se
  rechazan antes de mutar estado.
- GIVEN IDs opacos válidos pero no relacionados WHEN se presentan THEN no se
  crea ninguna relación implícita.

## R2 — Resolución confiable del principal

WHEN una superficie recibe una solicitud, THE identity resolver SHALL construir
el principal exclusivamente desde un authenticated subject entregado por
composición y SHALL NOT aceptar actor, tenant, membership, roles o service
identity desde el payload no confiable.

### Acceptance criteria

- GIVEN CLI, API/MCP, GitHub, external-agent o worker WHEN entra una operación
  THEN el mismo resolver liga subject, organization, project y policy digest.
- GIVEN payload que altera actor, tenant, project o roles WHEN se procesa THEN
  esos campos no cambian el principal y la discrepancia se rechaza.
- GIVEN subject ambiguo, proveedor/issuer no permitido o mapping obsoleto WHEN
  se resuelve THEN falla `unauthenticated` sin consultar el recurso objetivo.
- GIVEN cancelación o deadline WHEN resolver espera una frontera externa THEN
  propaga la categoría y no continúa con autorización.

## R3 — RBAC por acción, recurso y scope exactos

WHEN un principal intenta una operación, THE authorization service SHALL
evaluar un catálogo cerrado de acciones contra roles/bindings vigentes y el
scope exacto antes de leer, mutar, reservar o revelar existencia del recurso.

### Acceptance criteria

- GIVEN operaciones sobre runs, project config, evidences/packages,
  environments, secret references, tenant policy, memberships, audit,
  retention o quotas WHEN se invocan THEN cada una declara una acción y clase
  de recurso cerradas.
- GIVEN role sin acción, project fuera del binding o resource de otro tenant
  WHEN se autoriza THEN devuelve `unauthorized`/`not_found_hidden` antes de
  tocar el consumer store.
- GIVEN dos roles WHEN se combinan THEN solo agregan acciones declaradas; una
  denegación estructural de tenant/scope no puede ser anulada por otro role.
- GIVEN policy/version o membership cambia después de una decisión WHEN se
  inicia otra operación THEN se reevalúa; una decisión no se reutiliza fuera
  de su expiry, action, resource, project o digest.

## R4 — Authorities mínimas y binding de runs

WHEN una admisión o job se confirma, THE system SHALL sellar organization,
project, repository, principal, acciones/capabilities, policy digest y secret
references mínimas en su authority/binding durable; los consumers posteriores
SHALL usar ese binding y no reconstruir permisos desde IDs.

### Acceptance criteria

- GIVEN nuevo run WHEN se admite THEN tenant authority, configuration binding,
  run y primer evento/job se confirman en una única frontera causal.
- GIVEN planning, execution, evaluation, publication, cleanup o recovery WHEN
  opera un run THEN usa el binding histórico exacto y una capability mínima por
  fase.
- GIVEN head/policy/membership posterior WHEN cambia THEN no reatribuye el run
  histórico; nuevas operaciones privilegiadas o secret leases sí comprueban
  revocación vigente según R10.
- GIVEN authority con tenant, project, phase, fence, expiry o digest divergente
  WHEN se usa THEN falla antes de resolver secretos o realizar efectos.

## R5 — Aislamiento de stores y consultas

THE local and PostgreSQL adapters SHALL incluir organization y project en toda
identidad, key, cursor, índice, foreign key y consulta de datos tenant-owned y
SHALL verificar el scope antes de devolver o modificar filas/archivos.

### Acceptance criteria

- GIVEN la misma run/resource/idempotency key en dos tenants WHEN se persiste
  THEN no colisiona ni permite lectura cruzada.
- GIVEN ID manipulado o cursor de otro tenant/project WHEN se consulta THEN se
  rechaza sin distinguir ausencia de falta de permiso ni filtrar counts.
- GIVEN query/update/delete sin predicate tenant completo WHEN se verifica el
  adapter THEN contract tests la hacen fallar; no existe fallback a búsqueda
  global por ID.
- GIVEN corrupción de scope, digest o relación durable WHEN se recarga THEN
  devuelve `integrity` y no repara ni adopta estado de otro tenant.

## R6 — Aislamiento de queue, caches, objetos y ejecución

THE system SHALL namespacear queue/jobs, leases, inbox/outbox, idempotency,
caches, object/package/evidence references, environments, workers y material de
recovery por tenant y project, y SHALL volver a verificar scope en cada
frontera de consumo.

### Acceptance criteria

- GIVEN cache key o object key igual en tenants distintos WHEN se usa THEN los
  valores y eviction no se comparten.
- GIVEN worker con authority para tenant A WHEN reclama, completa, heartbeat o
  publica sobre tenant B THEN se rechaza por scope/fence antes del efecto.
- GIVEN inbox/outbox redelivery con misma external key pero tenant distinto WHEN
  ocurre THEN se trata como identidad diferente; misma key y scope con digest
  distinto es conflict.
- GIVEN logs, métricas o traces WHEN se emiten THEN sus dimensiones tenant y
  project proceden de authority sellada, no del payload ni de labels del modelo.

## R7 — Catálogo de recursos y configuración tenant-scoped

WHEN feature 36 carga catalog, organization policy o project configuration,
THE tenant security layer SHALL comprobar que cada descriptor, policy, secret
reference y `ProjectKey` pertenece al mismo tenant/project antes de producir
authority o snapshot.

### Acceptance criteria

- GIVEN `ProjectKey` existente pero ligado a otra organization WHEN se
  configura/admite THEN falla sin revelar su head/version.
- GIVEN catalog descriptor o secret reference cross-project/cross-tenant WHEN
  se valida THEN devuelve categoría cerrada antes de activar.
- GIVEN organization policy snapshot WHEN se aplica THEN su version/digest y
  tenant forman parte del effective digest y run binding.
- GIVEN config antigua de un run WHEN se recupera THEN conserva descriptores
  históricos exactos, pero no puede resolver un secret reference revocado.

## R8 — Almacenamiento cifrado de secretos

WHEN se crea o rota un secreto, THE secret service SHALL aceptar plaintext solo
en la frontera privada, cifrarlo mediante un key provider inyectado y persistir
únicamente un envelope autenticado tenant-scoped con key reference, algorithm,
nonce/ciphertext y AAD digest, nunca la key ni el plaintext.

### Acceptance criteria

- GIVEN store local o PostgreSQL WHEN se inspeccionan bytes, rows, journal,
  backups de test, errores o telemetry THEN el canary plaintext no aparece.
- GIVEN envelope copiado a otro tenant/project/ref/version o AAD alterada WHEN
  se descifra THEN falla `integrity` sin devolver bytes parciales.
- GIVEN key provider ausente, key ref inválida, algoritmo no permitido o
  randomness insuficiente WHEN se escribe THEN no persiste secret metadata
  activa ni ciphertext incompleto.
- GIVEN operación ordinaria de config/query/export WHEN ocurre THEN solo ve
  secret reference metadata allowlisted, nunca envelope o valor.

## R9 — Resolución JIT y mínimo privilegio

WHEN un consumer necesita un secreto, THE secret service SHALL exigir una
secret lease sellada para tenant, project, run, phase, consumer, reference,
version, purpose y expiry exactos, resolver/decrypt just-in-time y entregar el
valor solo al callback/adapter privado autorizado.

### Acceptance criteria

- GIVEN lease válida WHEN se usa una vez dentro de scope/expiry THEN solo el
  consumer declarado recibe bytes y el valor no entra en dominio, model
  context, evidence, package, event, log, metric, cache o error.
- GIVEN lease replay, expirada, cross-run, cross-phase, cross-consumer o con
  digest/fence distinto WHEN se usa THEN falla antes de decrypt/consumer.
- GIVEN consumer cancelado/fallido WHEN termina THEN el buffer se descarta y no
  existe fallback a otra versión, tenant, env var o config.
- GIVEN secret no requerido por la fase WHEN se construye authority THEN la
  referencia y lease no aparecen.

## R10 — Rotación y revocación de secretos e identidades

WHEN un secret reference o service identity se rota o revoca, THE system SHALL
publicar una nueva versión/estado monotónico, invalidar leases no usadas y
rechazar nuevas resoluciones sin reescribir auditoría o runs históricos.

### Acceptance criteria

- GIVEN rotación concurrente WHEN ocurre THEN una sola versión se activa por
  CAS y las versiones previas permanecen cifradas/inmutables hasta retención.
- GIVEN revocación WHEN se confirma THEN una lease todavía no consumida falla;
  una operación ya confirmada conserva su outcome auditable y no se repite.
- GIVEN run histórico con ref revocada WHEN recovery intenta continuar THEN
  falla cerrado con `revoked`, sin rebinding ni versión más nueva automática.
- GIVEN idempotency key igual y mismo command digest WHEN rota/revoca THEN
  converge; misma key con otro digest es conflict.

## R11 — Transporte seguro y composición de credenciales

WHERE una frontera cruza proceso o host en modo productivo, THE composition
root SHALL exigir transporte autenticado y cifrado según un `TransportSecurity`
validado; core/application SHALL NOT desactivar o inferir seguridad de red.

### Acceptance criteria

- GIVEN PostgreSQL, identity/key provider, object store o worker remoto en modo
  productivo WHEN se compone THEN configuración sin TLS/auth peer verificable
  se rechaza antes de aceptar trabajo.
- GIVEN tests herméticos/loopback explícitamente marcados como isolated test
  WHEN se ejecutan THEN pueden usar transporte local controlado sin convertirse
  en default productivo.
- GIVEN DSN, certificates, tokens o key material WHEN se observan errores/logs
  THEN se muestran solo IDs/digests allowlisted.
- GIVEN reconnect/retry WHEN ocurre THEN no degrada a plaintext ni cambia
  tenant authority.

## R12 — Auditoría inmutable y tamper-evident

WHEN se intenta o confirma una acción auditable, THE audit journal SHALL añadir
un evento inmutable tenant-scoped con sequence, previous digest, actor,
principal kind, organization, project opcional, action, resource class/ID
digest, instante, outcome y reason code allowlisted.

### Acceptance criteria

- GIVEN autorización allow/deny, cambio de membership/role/policy, acceso a
  secret, rotación/revocación, quota override o purge WHEN ocurre THEN existe
  evento que no contiene payload/secret/credential.
- GIVEN evento alterado, eliminado, reordenado, duplicado o enlazado a otro
  tenant WHEN se verifica/reinicia THEN se detecta `integrity`.
- GIVEN paginación/export autorizada WHEN consulta audit THEN usa cursor sellado
  tenant-scoped y copias acotadas; un cursor cross-tenant se rechaza.
- GIVEN sink externo de observabilidad caído WHEN falla THEN no modifica el
  journal durable ni se usa como fuente de verdad.

## R13 — Atomicidad de acciones sensibles y auditoría

WHEN una acción sensible muta seguridad, secretos, quotas, retention o
administración, THE system SHALL confirmar mutación, receipt y audit event en
una única unidad causal o no confirmar ninguno.

### Acceptance criteria

- GIVEN fault antes/durante/después de cada boundary WHEN se recupera THEN
  observa mutación+audit ambos o ninguno, y unknown commit se resuelve por
  receipt.
- GIVEN audit store indisponible o contador/digest agotado WHEN se intenta una
  mutación sensible THEN falla cerrado sin mutar.
- GIVEN denegación de acción sensible WHEN se audita THEN no toca el recurso;
  si el journal de denegación no puede confirmarse, la respuesta sigue siendo
  deny y se expone fallo operacional seguro sin conceder acceso.
- GIVEN redelivery WHEN misma operation key/digest THEN devuelve receipt previo
  sin duplicar evento; digest distinto es conflict.

## R14 — Políticas de retención versionadas

THE tenant policy SHALL declarar periodos y clases retenibles para runs,
packages/evidence, project data, secret versions, audit y tombstones, con floors
organizativos que un proyecto no puede reducir.

### Acceptance criteria

- GIVEN policy ausente, inválida, expansiva o con periodos/limits fuera de
  bounds WHEN se publica THEN falla antes de programar deletion.
- GIVEN run/resource activo, lease/outbox/cleanup pendiente, package/evidence
  referenciada, secret version activa o record protegido WHEN se escanea THEN
  no es elegible.
- GIVEN policy cambia WHEN aplica THEN solo afecta decisiones futuras; cada
  deletion intent registra policy version/digest y eligibility boundary.
- GIVEN audit/tombstone obligado WHEN expira payload operativo THEN conserva el
  mínimo allowlisted requerido para integridad, idempotencia y explicación.

## R15 — Eliminación convergente e idempotente

WHEN datos terminales son elegibles, THE retention controller SHALL persistir
un deletion intent tenant-scoped antes de borrar, eliminar en orden seguro y
confirmar tombstone/audit sin atravesar tenants ni romper referencias vivas.

### Acceptance criteria

- GIVEN crash/restart o respuesta unknown durante object/row delete WHEN
  recupera THEN reconcilia por identity/digest y converge sin borrar otro
  recurso.
- GIVEN project deletion WHEN se ejecuta THEN bloquea nuevas admisiones,
  revoca identities/leases, espera/cancela trabajo según policy y purga por
  lotes acotados preservando protected records.
- GIVEN concurrent create/reference/retention WHEN compiten THEN CAS/fencing
  decide; el controller no borra un recurso que adquirió referencia viva.
- GIVEN cancelación/deadline WHEN escanea o purga THEN detiene trabajo nuevo y
  deja intents durables recuperables.

## R16 — Rate limits, quotas y budgets antes de reservar

WHEN una operación consume workers, model calls/tokens, environments,
infrastructure, storage u otra capacidad declarada, THE quota service SHALL
comprobar rate/budget y confirmar una reservation tenant/project-scoped antes
de queue claim, provider call o capacity acquisition.

### Acceptance criteria

- GIVEN límite tenant y project WHEN se reserva THEN se aplica el más
  restrictivo y nunca se amplía por config de proyecto, payload o modelo.
- GIVEN concurrencia multiproceso en el último slot/tokens WHEN ocurre THEN como
  máximo una reservation gana; las demás reciben categoría estable y no
  invocan consumer.
- GIVEN misma operation key/demand WHEN se redelivery THEN retorna el mismo
  receipt; misma key con demand distinto es conflict.
- GIVEN cancelación, terminal, unknown effect o expiry WHEN se libera THEN
  ocurre exactamente una vez según policy; incertidumbre conserva reservation
  hasta reconciliación y evita oversubscription.
- GIVEN reloj/window/budget overflow o policy/catalog drift WHEN se calcula
  THEN falla cerrado sin wrap ni bucket ilimitado.

## R17 — Concurrencia, retries y errores cerrados

IF authorization, tenant store, encryption, audit, retention o quota devuelve
cancelación, deadline, conflicto, serialización, indisponibilidad, corrupción o
revocación, THEN THE application SHALL mapear una categoría cerrada, reintentar
solo transacciones completas seguras con budget y no cambiar de backend/scope.

### Acceptance criteria

- GIVEN serialization/deadlock retryable WHEN queda budget THEN reejecuta la
  unidad completa sin repetir provider/consumer externo.
- GIVEN unavailable/permission/integrity WHEN ocurre THEN no se traduce a
  `not_found` ni prueba otro tenant, local store o secret version.
- GIVEN contexto cancelado WHEN una frontera espera THEN no inicia otra query,
  decrypt, reservation, audit mutante o deletion.
- GIVEN contadores/sequence/version/fence agotados WHEN incrementa THEN devuelve
  `exhausted` sin wrap ni estado parcial.

## R18 — Observabilidad segura y no autoritativa

THE tenant security layer SHALL emitir únicamente metadata allowlisted de
operación, categoría, outcome, duración, tenant/project pseudonymous IDs,
resource class y counts, y SHALL NOT usar logs, metrics o traces como autoridad.

### Acceptance criteria

- GIVEN canaries en subjects, payloads, plaintext, ciphertext, secret refs,
  DSN, cursor o collaborator errors WHEN se inspecciona observabilidad THEN no
  aparece el valor.
- GIVEN metrics de tenants distintos WHEN se agregan THEN labels no permiten
  colisión ni cardinalidad controlada por input; los IDs se derivan de scope
  validado o digest allowlisted.
- GIVEN observer panic/fallo/bloqueo WHEN ocurre THEN no concede acceso, cambia
  quota, muta audit ni altera receipts/commits.
- GIVEN denial WHEN se observa THEN reason code es estable y no revela
  existencia, rol interno o datos de otro tenant.

## R19 — Adapters local/PostgreSQL, migraciones y comentarios

THE implementation SHALL ofrecer contratos observables equivalentes en modo
local y PostgreSQL, migrations append-only compatibles N/N+1 y comentarios de
responsabilidad sistémica para entidades/tipos, funciones/métodos y test
helpers nuevos o modificados.

### Acceptance criteria

- GIVEN misma contract suite WHEN se ejecuta contra ambos adapters THEN
  authorization, isolation, encryption envelopes, audit, retention, quotas,
  receipts, restart y corruption producen categorías equivalentes.
- GIVEN upgrade/rollback binario dentro de ventana N/N+1 WHEN se prueba THEN
  mixed versions no omiten tenant predicates ni aceptan schema future/dirty.
- GIVEN modo PostgreSQL WHEN arranca THEN no existe fallback/dual-write local;
  modo local no abre PostgreSQL implícitamente.
- GIVEN diff completo WHEN se audita THEN cada entidad/tipo, función, método y
  test helper modificado explica su función/invariante; no se exigen
  comentarios mecánicos para variables locales.

## R20 — Verificación adversarial y trazabilidad

THE implementation SHALL incluir tests deterministas que demuestren acceso
cruzado denegado, escalada de rol fallida, IDs/cursors manipulados, caches y
objetos aislados, secret rotation/revocation, retention/deletion, auditoría,
quota y concurrencia con trazabilidad `R<n> → test`.

### Acceptance criteria

- GIVEN `go test ./...` WHEN se ejecuta THEN no requiere red, proveedor IAM/KMS
  real, modelo real ni PostgreSQL; usa fakes cryptográficos/identity controlados.
- GIVEN gate PostgreSQL 16 autorizada WHEN se ejecuta THEN cubre migrations,
  constraints, RLS/predicates de aplicación, tx faults, concurrencia, restart,
  corruption, N/N+1 y cleanup UUID, normal y race.
- GIVEN código nuevo/modificado WHEN se mide THEN alcanza al menos 85% de
  statement coverage diferencial exacta, sin redondeo ni exclusiones que
  oculten ramas de seguridad.
- GIVEN builds Windows/Linux/Darwin aplicables, format, vet, compile-only,
  normal/race y auditorías de secretos/comentarios WHEN se ejecutan THEN pasan
  antes de `done`.

## Errores y casos límite globales

- IDs, acciones, resource classes, role IDs, secret refs, operation keys y
  cursor payloads tienen límites de tamaño, formato y cardinalidad antes de
  reservar memoria o consultar stores.
- El scope se compara como datos canónicos, nunca mediante prefijos de path,
  substring, labels aportados por usuario o case folding dependiente de locale.
- `not_found_hidden` no puede convertirse en un oracle mediante timing, counts,
  errores de foreign key, audit export, quota restante o métricas.
- Ningún callback de transacción realiza red, KMS/decrypt, object delete,
  consumer call, espera, logging o telemetry potencialmente bloqueante.
- Plaintext y key material no se guardan en structs de larga vida, retries,
  closures, panic values, `%v`, JSON de diagnóstico o fixtures persistidos.
- Rotation/revocation, role/membership changes, quota releases y deletion
  transitions usan CAS/receipts y no hacen wrap.
- Una policy obsoleta, relation corrupta, key provider no disponible o audit
  integrity failure bloquea nuevas acciones sensibles; no se usa un default
  permisivo.
- Tests concurrentes usan barriers/channels y clock controlado, nunca
  `time.Sleep` como sincronización.

## Decisiones cerradas

- `OrganizationID` se añade como scope superior; `ProjectKey` sigue siendo la
  identidad de configuración y queda registrado bajo una organización única.
- La autenticación externa produce un subject confiable; FeatureProof posee el
  mapping a principal, memberships y autorización, no el login/token issuer.
- Roles y bindings son datos versionados sobre un catálogo cerrado de acciones;
  no hay política textual o agentic.
- El cifrado se realiza en application mediante envelope encryption y puertos
  consumer-owned; stores reciben ciphertext. No se fija proveedor KMS/vault.
- Audit journal es autoridad durable; logs/traces/SIEM son proyecciones best
  effort.
- Rate limits/cuotas son reservations durables pre-effect, no métricas
  retrospectivas.
- En producción toda frontera remota exige seguridad de transporte validada;
  loopback inseguro solo existe en tests aislados explícitos.

## Decisiones abiertas

No quedan decisiones de producto necesarias para implementar esta feature. Los
valores concretos de límites, ventanas, TTL, retención, algorithms/key refs,
catálogo de roles/actions y page sizes son inputs obligatorios versionados y
validados por composición; no son defaults embebidos en el dominio.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-11-20T08:52:19+01:00
```
