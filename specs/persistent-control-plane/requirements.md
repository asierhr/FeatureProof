# Control plane persistente y distribuible — Requirements

## Objetivo y problema

FeatureProof dispone de casos de uso durables sobre stores locales, pero su
fuente de verdad y sus leases todavía pertenecen a un único filesystem. Eso no
permite que una API, varios workers y los controladores de cleanup sobrevivan y
se coordinen a través de reinicios o procesos distintos sin perder las
invariantes ya aprobadas.

Esta feature incorpora un backend PostgreSQL versionado y una cola durable para
distribuir fases de validación. El control plane persiste identidad e intención
antes de reconocer entradas o cruzar un efecto externo, concede a cada worker
una autoridad mínima y temporal, y converge ante cancelación, shutdown,
redelivery o pérdida del owner. Los contratos siguen perteneciendo a sus
consumidores; PostgreSQL coordina el trabajo, pero no absorbe reglas de dominio.

## Alcance

- Contratos consumer-owned y adapters PostgreSQL para runs, eventos,
  aprobaciones, bindings, jobs, packages, entornos y relaciones de corrección.
- Migraciones versionadas, verificables y compatibles con despliegues
  progresivos y rollback de aplicación dentro de una ventana declarada.
- Transacciones que conservan append-only, CAS, idempotencia, integridad y la
  creación atómica de intents/jobs/outbox junto con el cambio causal.
- Cola durable con claim, lease, heartbeat, fencing, retries acotados,
  redelivery y recuperación de ejecuciones abandonadas.
- Inbox idempotente para webhooks y solicitudes externas con persist-before-ack.
- Envelopes de trabajo mínimos por `ProjectID`, fase, binding, capacidades,
  referencias de secretos y vencimiento.
- Convergencia durable de cancelación, shutdown, pérdida del worker y respuestas
  externas inciertas sin duplicar efectos lógicos.
- Selección explícita de backend local o PostgreSQL, bindings append-only por
  epoch con exactamente una epoch activa por run y migración local offline sin
  dual-write.
- Consultas operacionales paginadas, retención segura de datos terminales y
  métricas/logs content-free.
- Tests unitarios herméticos y una suite de integración explícita con PostgreSQL
  aislado y workers controlados.

## Fuera de alcance

- Configuración versionada, herencia o resolución general por proyecto; es la
  feature 36. Esta feature recibe límites y policies desde composición.
- Organizaciones, tenants, miembros, RBAC multi-tenant, cuotas o aislamiento de
  tenant; es la feature 37. El único scope de equipo es el `ProjectID` existente.
- Crear un gestor de secretos, persistir valores secretos en PostgreSQL o
  transportar credenciales dentro de jobs, eventos, packages o logs.
- Cambiar cómo se genera, aprueba, ejecuta o evalúa un plan, ni conceder al
  modelo acceso a la cola, SQL o al control plane.
- Sustituir los providers de entorno, la tool Kubernetes, la política de
  cleanup o sus bindings ya aprobados en las features 31–34.
- Garantizar exactamente una llamada física a un sistema externo que no ofrece
  idempotencia o reconciliación; se garantiza una sola confirmación lógica.
- Replicación PostgreSQL, backups gestionados, elección de proveedor cloud,
  autoscaling, service mesh o una UI operacional.
- Borrar evidencias o artefactos activos, redefinir su política de producto o
  introducir object storage en esta entrega.

## Actores y términos

- **Control plane:** composición de stores, queue, inbox/outbox y coordinadores
  que mantiene la fuente de verdad; no ejecuta herramientas por sí mismo.
- **Backend binding epoch:** elección inmutable `local|postgres` asociada al run,
  una generación monotónica, identidad del backend, versión de schema y digest.
  Un head CAS señala la única epoch activa; las anteriores quedan selladas y
  archivadas, nunca se mutan ni recuperan autoridad silenciosamente.
- **Inbox:** registro durable de una entrada externa y su digest/idempotency key
  antes de responder al emisor.
- **Job:** unidad durable de una fase ya admitida, con inputs por referencia,
  estado, attempt, disponibilidad, lease y fencing token.
- **Authority envelope:** concesión sellada y temporal que limita un job a un
  `ProjectID`, run, fase, target/binding, capacidades y secret references.
- **Lease:** ownership temporal renovable. **Fencing token:** número monotónico
  que invalida a owners anteriores aunque aún conserven memoria o red.
- **Outbox:** intención durable de un efecto/publicación externo confirmada en
  la misma transacción que su causa y enviada fuera de la transacción.
- **Logical exactly-once:** una identidad e input producen un único terminal
  durable; una llamada física puede repetirse tras resultado incierto usando la
  misma idempotency key y reconciliación.
- **Worker controlado:** fake de test que expone barreras de claim, heartbeat,
  efecto y confirmación sin red, reloj real ni sleeps.

## R1 — Fuente PostgreSQL completa mediante contratos consumidores

THE control plane SHALL almacenar en PostgreSQL runs, eventos, aprobaciones,
bindings, jobs, packages, entornos y relaciones de corrección mediante
interfaces pequeñas propiedad del paquete consumidor, sin exponer SQL ni DTOs
del driver al dominio.

### Acceptance criteria

- GIVEN un recorrido admitido que crea o actualiza cada entidad WHEN se reabre
  desde otro proceso THEN se reconstruye el mismo estado y los mismos digests.
- GIVEN un package confirmado WHEN se carga THEN su documento canónico, digest,
  relaciones y metadata son íntegros; no depende del filesystem original.
- GIVEN evidencia o payload sensible referenciado por una entidad WHEN se
  persiste THEN solo cruza el dato previsto por su contrato, no un agregado SQL
  genérico ni credenciales accidentales.
- GIVEN inspección de los puertos WHEN se revisa THEN cada consumidor define las
  operaciones que necesita y el adapter PostgreSQL las implementa sin importar
  tipos del driver en modelos públicos.

## R2 — Migraciones versionadas y despliegue compatible

WHEN una instancia arranca contra PostgreSQL, THE migrator SHALL verificar una
historia lineal de migraciones con versión y checksum, serializar su aplicación,
ejecutar cada cambio en su frontera transaccional declarada y rechazar schemas
dirty, alterados o fuera del rango compatible antes de aceptar tráfico.

### Acceptance criteria

- GIVEN una base vacía WHEN se migra THEN alcanza la versión objetivo una vez;
  una segunda ejecución no cambia schema ni datos.
- GIVEN dos instancias migrando simultáneamente WHEN compiten THEN una serializa
  el cambio y ambas observan finalmente la misma historia completa.
- GIVEN checksum cambiado, versión desconocida, migración parcial o server/schema
  incompatible WHEN arranca THEN falla cerrado antes de claims o escrituras.
- GIVEN despliegue gradual N/N+1 WHEN comparte schema dentro de la ventana
  declarada THEN ambas versiones operan; cambios destructivos requieren una
  migración posterior después de retirar la versión antigua.
- GIVEN rollback de aplicación dentro de la ventana declarada WHEN ocurre THEN
  no necesita revertir datos; un rollback destructivo fuera de ella se rechaza
  y exige restore/migración operacional explícita.

## R3 — Append-only, CAS e integridad transaccional

WHEN el dominio confirma una transición, THE PostgreSQL adapter SHALL validar
la revisión/digest esperados y confirmar atómicamente la proyección, el evento
append-only y cualquier job/outbox causal, o no confirmar ninguno.

### Acceptance criteria

- GIVEN dos writers con la misma revisión WHEN compiten THEN exactamente uno
  avanza y el otro recibe conflict tipado sin sobrescritura perdida.
- GIVEN redelivery con la misma identidad y bytes canónicos WHEN se escribe THEN
  devuelve la recepción existente; contenido distinto bajo la misma identidad
  produce idempotency/integrity conflict.
- GIVEN fallo inyectado entre record, event, job y outbox WHEN la transacción
  aborta THEN no queda estado parcial visible.
- GIVEN evento ya confirmado WHEN otra operación intenta modificarlo o reutilizar
  su secuencia THEN se rechaza; la proyección puede reconstruirse desde la
  historia definida por su contrato.
- GIVEN foreign key, digest, scope, orden o relación inválidos WHEN se carga o
  escribe THEN falla cerrado sin reparar silenciosamente ni ejecutar trabajo.

## R4 — Una sola epoch de backend activa por run

WHEN se admite o migra un run, THE system SHALL persistir bindings de backend
inmutables y append-only por epoch monotónica, SHALL publicar exactamente una
epoch activa mediante un head CAS y SHALL exigir su epoch/digest en toda
operación, sin fallback ni dual-write.

### Acceptance criteria

- GIVEN composición inicial `postgres` WHEN se reserva el run THEN epoch 1 y su
  head activo quedan confirmados en la misma frontera durable que su identidad.
- GIVEN un adapter local presentado a una epoch PostgreSQL, o viceversa, WHEN se
  intenta leer/escribir THEN se rechaza antes de mutación o efecto externo.
- GIVEN backend activo indisponible WHEN opera el run THEN devuelve error
  operacional retryable; no continúa sobre otro backend o epoch.
- GIVEN restart de API o worker WHEN recupera el run THEN resuelve head, epoch y
  digest confiables, no un default de proceso.
- GIVEN dos cutovers concurrentes desde el mismo head WHEN compiten THEN como
  máximo uno publica la siguiente epoch y el otro recibe conflict; nunca hay
  dos heads/epochs activos.
- GIVEN un worker con backend epoch/digest anterior WHEN heartbeat o complete
  llega después del cutover THEN queda stale y no confirma estado, efecto ni
  terminal, aunque su job fence local pareciera vigente.
- GIVEN historial de bindings WHEN se audita THEN cada epoch y cutover event es
  append-only; mover el head sella/archiva lógicamente la anterior sin mutarla.

## R5 — Persist-before-ack de entradas externas

WHEN un webhook o solicitud externa válida solicita trabajo, THE ingress SHALL
confirmar en una transacción su identidad, digest, `ProjectID` e intención
idempotente antes de responder como aceptada.

### Acceptance criteria

- GIVEN primera entrega válida WHEN la transacción hace commit THEN la respuesta
  aceptada se emite después del commit y el trabajo es recuperable tras restart.
- GIVEN fallo/timeout PostgreSQL antes o con resultado de commit desconocido
  WHEN se atiende THEN no se afirma aceptación; el emisor puede repetir la misma
  key y resolver el receipt durable.
- GIVEN misma source/id/key y mismo digest WHEN se redelivery THEN devuelve el
  receipt/result previo sin crear otro run/job; digest distinto produce conflict.
- GIVEN autenticación, firma, scope o límites inválidos WHEN se recibe THEN se
  rechaza antes de reservar trabajo; el inbox no convierte input no confiable en
  autoridad.
- GIVEN payload externo con secretos o campos no usados WHEN se persiste THEN se
  conserva solo el envelope allowlisted/digest necesario para idempotencia.

## R6 — Claim, lease, heartbeat y fencing durables

WHEN un worker solicita trabajo, THE queue SHALL reclamar atómicamente un job
elegible en orden determinista, asignar owner, lease y fencing token monotónico,
y SHALL exigir los tres en heartbeat y confirmación terminal.

### Acceptance criteria

- GIVEN varios workers concurrentes WHEN reclaman una página THEN ningún job
  tiene dos leases vivas y jobs/proyectos independientes pueden progresar.
- GIVEN lease viva y heartbeat válido WHEN se renueva THEN extiende solo dentro
  del máximo configurado y avanza CAS sin cambiar inputs o autoridad.
- GIVEN owner o fencing token viejo, lease expirada o job cancelado WHEN intenta
  heartbeat/terminal THEN se rechaza antes de guardar resultado o nuevo efecto.
- GIVEN más jobs que el límite WHEN se reclama THEN se respeta orden
  `(available_at, created_at, job_id)`, página y budget sin bloquear contenders.
- GIVEN espera o I/O del worker WHEN ocurre THEN no mantiene una transacción ni
  row lock abiertos.

## R7 — Retries acotados y recuperación de abandono

IF un job falla de forma transitoria, pierde su worker o expira su lease, THEN
THE scheduler SHALL persistir la clasificación y reprogramarlo con attempt,
backoff y budgets acotados, o llevarlo a un terminal operacional explícito.

### Acceptance criteria

- GIVEN worker desaparecido y `now >= lease_until` WHEN recovery escanea THEN el
  job vuelve a ser elegible con fencing mayor y conserva identidad/inputs.
- GIVEN transient/rate-limit/timeout/result-unknown WHEN queda budget THEN
  persiste `available_at`, categoría y attempt antes de ceder ownership.
- GIVEN invalid/unauthorized/integrity/conflict permanente o attempts/deadline
  agotados WHEN ocurre THEN no hace retry automático y publica terminal tipado.
- GIVEN restart entre retry y siguiente claim WHEN recupera THEN usa tiempos y
  contadores durables, no timers o memoria del proceso.
- GIVEN overflow, backoff que excede deadline o clock anómalo WHEN calcula THEN
  falla cerrado sin crear un job inmediatamente reintentable infinito.

## R8 — Autoridad mínima por fase y proyecto

WHEN la queue entrega un job, THE system SHALL construir un authority envelope
sellado, temporal y mínimo para su `ProjectID`, run y fase, incluyendo solo
capabilities, target/binding y referencias de secretos necesarias.

### Acceptance criteria

- GIVEN fase sin target, secreto o capability WHEN se entrega THEN el campo no
  aparece y el worker no puede resolverlo por `RunID`.
- GIVEN ejecución de tool o cleanup WHEN se entrega THEN binding/target y
  capabilities coinciden exactamente con la fase admitida y sus digests.
- GIVEN worker, backend epoch/digest, project, phase, attempt, fence, expiry o
  envelope digest divergente WHEN se usa THEN se rechaza antes de resolver
  secreto, target o tool.
- GIVEN secret reference permitida WHEN el worker la necesita THEN se resuelve
  just-in-time en la frontera privada; el valor no se almacena en job, evento,
  package, log ni heartbeat.
- GIVEN feature 36/37 aún ausentes WHEN se autoriza THEN usa `ProjectID` y
  authority existentes; no inventa org, tenant, rol, jerarquía o config.

## R9 — Intención durable antes de cada efecto externo

WHEN un worker va a ejecutar una tool, publicar un resultado o iniciar cleanup,
THE system SHALL confirmar primero una identidad de operación e intent/outbox
durable, y SHALL reconciliar redelivery mediante esa identidad antes de repetir.

### Acceptance criteria

- GIVEN crash antes del intent WHEN recupera THEN no existe efecto autorizado;
  GIVEN crash después del intent THEN otro worker adopta la misma operación.
- GIVEN respuesta externa perdida WHEN se redelivery THEN reutiliza la misma
  idempotency key y consulta el terminal/reconciliador antes de repetir.
- GIVEN proveedor con idempotencia WHEN reintenta THEN presenta la key estable;
  GIVEN proveedor sin ella THEN no repite un efecto incierto sin reconciliación
  segura y puede terminar bloqueado/inconclusive operacionalmente.
- GIVEN confirmación externa y crash antes de cerrar outbox WHEN recupera THEN
  converge a una sola confirmación/evento lógico.
- GIVEN redelivery del mismo terminal WHEN se guarda THEN no duplica evidencia,
  package, publicación, cleanup ni relación de corrección.

## R10 — Cancelación durable y convergente

WHEN se solicita cancelar un run, THE system SHALL persistir primero la
intención de cancelación, invalidar trabajo no iniciado y hacer que jobs activos
observen cancellation/fencing sin perder cleanup ni reescribir terminales ya
confirmados.

### Acceptance criteria

- GIVEN jobs queued/retry-wait WHEN se confirma cancelación THEN no vuelven a
  reclamarse salvo fases de convergencia expresamente permitidas como cleanup.
- GIVEN worker activo WHEN heartbeat o complete compite con cancelación THEN una
  única transición CAS gana; un owner stale no publica resultado posterior.
- GIVEN efecto ya confirmado antes de cancelación WHEN converge THEN conserva
  su terminal idempotente y programa lo necesario para cierre/cleanup una vez.
- GIVEN múltiples solicitudes de cancelación WHEN se redelivery THEN producen
  una sola intención lógica y una proyección estable.
- GIVEN cancelación durante espera, tool o publicación WHEN ocurre THEN el
  contexto se propaga, pero el estado durable decide la recuperación posterior.

## R11 — Shutdown y pérdida del worker recuperables

IF una API o worker inicia shutdown o desaparece, THEN THE process SHALL dejar
de aceptar/claimar trabajo nuevo, cancelar operaciones propias y dejar leases e
intents en un estado que otro proceso pueda recuperar sin handoff en memoria.

### Acceptance criteria

- GIVEN shutdown ordenado WHEN comienza THEN el worker deja de claimar, detiene
  heartbeats nuevos después del límite, cancela I/O y permite join acotado.
- GIVEN operación que todavía no cruzó un efecto WHEN puede abandonarse por CAS
  THEN se reencola una vez; si el efecto es incierto, conserva intent y espera
  expiración/reconciliación en vez de fingir ausencia.
- GIVEN kill sin shutdown WHEN expira la lease THEN recovery progresa con fence
  nuevo y rechaza cualquier respuesta tardía del proceso perdido.
- GIVEN restart simultáneo de API y workers WHEN arrancan THEN migración/schema
  se verifica antes de ingress/claims y toda recuperación depende solo de datos
  confirmados.

## R12 — Modo local y cutover explícito por nueva epoch

WHERE desarrollo selecciona almacenamiento local, THE system SHALL conservar
el comportamiento existente mediante adapters explícitos; WHERE se migra a
PostgreSQL, THE migrator SHALL importar un snapshot detenido, verificarlo y
realizar el cutover completo publicando una nueva backend epoch mediante una
única transacción/CAS, sin dual-write ni mutar el binding anterior.

### Acceptance criteria

- GIVEN modo local WHEN arranca THEN no abre PostgreSQL ni workers distribuidos
  implícitamente y conserva los límites de un único host documentados.
- GIVEN migración WHEN comienza THEN la fuente local queda frozen/read-only, la
  epoch local sigue siendo la única activa y se registra un manifest/candidato
  PostgreSQL sin autoridad con identidad y digests antes de importar.
- GIVEN retry del mismo import y mismos bytes WHEN ocurre THEN converge; un
  conflicto o corrupción aborta sin marcar cutover.
- GIVEN import incompleto o crash antes del cutover WHEN se verifica/reinicia
  THEN ningún run se liga parcialmente a PostgreSQL, el candidato no adquiere
  autoridad y la epoch local frozen sigue siendo la única activa.
- GIVEN verificación de counts, relaciones, eventos y digests exitosa WHEN el
  operador selecciona cutover THEN una sola transacción verifica el head local
  esperado, publica la nueva epoch PostgreSQL, mueve el head y registra el
  archival seal de la anterior; desde ese commit solo PostgreSQL es autoritativo.
- GIVEN crash con resultado de cutover desconocido WHEN se recupera THEN lee el
  head/receipt durable para decidir qué epoch es activa y no repite escrituras
  entre ambos backends.
- GIVEN necesidad de rollback antes del cutover WHEN ocurre THEN se descarta el
  candidato y se descongela la epoch local por protocolo explícito; GIVEN
  rollback después del cutover THEN requiere una nueva importación/verificación
  y una epoch posterior, nunca revive o muta silenciosamente la epoch antigua.
- GIVEN filesystem local post-cutover WHEN se inspecciona THEN permanece
  archivado/read-only hasta la policy operacional; no recibe nuevas mutaciones.

## R13 — Consultas, paginación y retención seguras

WHEN API, recovery u operación consultan estado, THE adapters SHALL usar páginas
acotadas con cursor estable y SHALL aplicar retención solo a datos terminales no
referenciados después del periodo composition-owned.

### Acceptance criteria

- GIVEN más filas que el límite WHEN se lista THEN la página y cursor continúan
  en orden determinista sin omitir/duplicar por inserciones concurrentes.
- GIVEN cursor inválido, cross-project, alterado o expirado WHEN se presenta THEN
  se rechaza sin filtrar existencia de otro proyecto.
- GIVEN run/job/outbox/environment/correction activo o referenciado WHEN ejecuta
  retention THEN no lo elimina ni rompe foreign keys o reconstrucción auditada.
- GIVEN datos terminales elegibles WHEN se purgan por lotes THEN el proceso es
  cancelable, idempotente y deja tombstone/summary mínimo si el contrato de
  idempotencia aún lo requiere.
- GIVEN no se configura retention WHEN arranca THEN no existe borrado por
  default oculto.

## R14 — Aislamiento, errores operacionales y observabilidad

IF PostgreSQL devuelve cancelación, timeout, conflicto de serialización,
deadlock, indisponibilidad, constraint o corrupción, THEN THE adapter SHALL
mapearlo a categorías cerradas, reintentar solo transacciones completas seguras
dentro de un budget y emitir observabilidad allowlisted no autoritativa.

### Acceptance criteria

- GIVEN `context` cancelado/deadline WHEN una query espera THEN retorna la
  categoría correspondiente y no inicia otra frontera.
- GIVEN serialization/deadlock retryable WHEN queda budget THEN reejecuta la
  transacción completa con clock/waiter inyectados; nunca solo el último SQL.
- GIVEN unique/FK/check violation, schema/integrity error o permiso denegado WHEN
  ocurre THEN se clasifica sin comparar mensajes ni retry infinito.
- GIVEN logs/métricas/traces WHEN se inspeccionan THEN muestran operación,
  categoría, latencia, counts, phase y IDs/digests allowlisted, nunca SQL con
  payloads, DSN, tokens, secret refs sensibles o cuerpos externos.
- GIVEN sink de observabilidad caído WHEN falla THEN no cambia commits, claims,
  ack, authority ni resultado funcional.

## R15 — Integración PostgreSQL y workers controlados

THE implementation SHALL incluir tests deterministas que cubran migraciones,
transacciones, concurrencia, restart, redelivery, lease expiry, cancelación,
recovery e idempotencia con PostgreSQL real aislado y workers controlados.

### Acceptance criteria

- GIVEN `go test ./...` WHEN se ejecuta THEN no necesita PostgreSQL, Docker, red,
  DNS ni sleeps y todos los tests unitarios permanecen herméticos.
- GIVEN el gate de integración explícito con DSN de test autorizado WHEN se
  ejecuta THEN crea un namespace/database único, aplica migraciones desde cero,
  corre en paralelo seguro y elimina solo su aislamiento al terminar.
- GIVEN DSN ausente WHEN se solicita el gate PostgreSQL THEN falla o se omite de
  forma explícita según el comando documentado, nunca contacta una base inferida
  ni convierte un skip en evidencia de aprobación.
- GIVEN workers falsos con barreras y reloj controlado WHEN se inyectan crashes
  en cada boundary THEN los asserts observan commits/locks/fences, no timing real.
- GIVEN código nuevo/modificado WHEN se verifica THEN existe trazabilidad
  `R<n> → test`, pasan formato, vet, tests normales/race/integración aplicables y
  se mide al menos 85% de statement coverage sin redondear.

## R16 — Comentarios de responsabilidad sistémica

THE implementation SHALL documentar toda entidad, constante, variable, función,
método y helper de test nuevo o modificado con un comentario breve que explique
su responsabilidad dentro del sistema, la frontera que representa o el
invariante que protege.

### Acceptance criteria

- GIVEN símbolo exportado o no exportado modificado por la feature WHEN se
  revisa THEN posee comentario útil, incluidas variables de paquete, errores,
  migraciones embebidas, fakes, factories y helpers de assertion.
- GIVEN comentario que solo parafrasea el identificador WHEN se audita THEN no
  satisface el requisito hasta explicar su papel sistémico.
- GIVEN `go doc`, revisión estática y diff manual WHEN se ejecutan THEN no quedan
  símbolos introducidos por la feature sin su contexto de sistema.

## Errores y casos límite globales

- Un resultado de commit desconocido se resuelve consultando la identidad
  idempotente; nunca se compensa a ciegas ni se cambia a local.
- Toda operación/job/envelope lleva backend epoch y digest. El job fence no
  sustituye esa prueba; cualquiera de ambos stale bloquea la confirmación.
- `now == lease_until` significa lease expirada. Tiempos se almacenan en UTC y
  se comparan con clock inyectado; overflow o timestamp no canónico falla cerrado.
- Contadores, revisiones, attempts, sequences y fencing tokens no hacen wrap;
  alcanzar su máximo produce terminal operacional/integrity.
- `FOR UPDATE SKIP LOCKED` se usa solo para selección de queue con orden/budget;
  una vista inconsistente nunca se usa para validar integridad o autoridad.
- `LISTEN/NOTIFY`, si se adopta, es únicamente una optimización de wake-up. La
  tabla durable y el polling acotado siguen siendo la fuente de verdad.
- Una conexión caída, timeout o permiso insuficiente no equivale a NotFound.
- Ningún transaction callback ejecuta tool, provider, webhook response,
  publicación, secret resolution, espera o telemetría potencialmente bloqueante.
- Los payloads canónicos y packages tienen límites explícitos; oversized se
  rechaza antes de reservar memoria/filas sin límite.

## Decisiones abiertas

No quedan decisiones de producto necesarias. Los valores concretos de pool,
timeouts, page size, lease TTL, heartbeat, retry/backoff, retention, payload y
compatibility window son límites obligatorios entregados por composición o por
el manifest versionado del adapter, sin defaults de dominio. La feature 36 podrá
versionar su procedencia y la 37 ampliar el modelo de tenant sin reinterpretar
los contratos de esta entrega.

## Aprobación humana

```yaml
approved_by: null
approved_at: null
```

La implementación no puede comenzar hasta aprobación humana explícita y cambio
del estado a `in_progress` por el leader.
