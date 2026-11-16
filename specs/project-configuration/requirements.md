# Configuración versionada por proyecto — Requirements

## Objetivo

FeatureProof debe sustituir la configuración productiva dispersa y cargada al
arrancar por una configuración de proyecto versionada, verificable y vinculada
a cada run. Un repositorio autorizado podrá publicar y activar una configuración
que seleccione contratos, entornos, identidades, fixtures, perfiles, límites,
aprobación y retención sin almacenar secretos. Los cambios posteriores solo
afectarán nuevas admisiones; un run conservará el snapshot efectivo con el que
fue admitido.

## Alcance

- Documento estricto `featureproof.project-config/v1`, representación canónica,
  digest y límites explícitos.
- Asociación de una configuración a un `ProjectID` y `RepositoryID`
  autorizados.
- Referencias tipadas y versionadas a contratos, providers/targets de entorno,
  identidades, fixture sets, runtime/model profiles, policies y retención.
- Validación estructural y semántica contra catálogos confiables.
- Defaults y restricciones organizativas entregadas como snapshot confiable por
  composición, sin introducir un modelo de organizaciones.
- Versiones inmutables, activación CAS, historial append-only, idempotencia y
  rollback mediante una nueva activación.
- Store local para desarrollo y adapter PostgreSQL para el control plane, sin
  dual-write ni fallback.
- Resolución fail-closed de la configuración activa durante admisión desde CLI,
  API/MCP y GitHub, y binding inmutable al run.
- Consulta y export seguro sin valores secretos.

## Fuera de alcance

- Organizaciones, miembros, service accounts, roles o RBAC; pertenecen a la
  feature 37 `tenant-security`.
- Aislamiento multi-tenant, cifrado de secretos, rotación/revocación de
  credenciales y cuotas por tenant.
- Un servicio externo de configuración, una UI web o edición colaborativa.
- Inferir referencias, providers, límites o policies desde el repositorio, el
  prompt o el modelo.
- Mutar una versión publicada, reescribir historial o eliminar versiones que
  todavía estén referenciadas por runs.
- Resolver valores secretos durante validación, publicación, consulta o export.

## Actores y términos

- **Project key:** par inmutable `(project_id, repository_id)` que delimita una
  configuración activa. No representa todavía un tenant.
- **Configuration authority:** capacidad creada por composición para un actor,
  project key y acción concreta. No es un rol ni un token aportado por el
  documento.
- **Candidate document:** bytes no confiables presentados para validación o
  publicación.
- **Configuration version:** documento canónico e inmutable identificado por
  versión monotónica y SHA-256.
- **Activation:** generación append-only que selecciona una configuration
  version como head activo; una activación de rollback referencia una versión
  histórica, no la modifica.
- **Organization policy snapshot:** defaults, allowlists y límites máximos o
  mínimos proporcionados por una fuente confiable. Esta feature consume el
  snapshot, pero no administra organizaciones.
- **Resource catalog snapshot:** vista confiable y digestada de contratos,
  environments, identities, fixtures, profiles y policies que pueden ser
  referenciados.
- **Effective configuration:** resultado canónico de combinar una versión de
  proyecto con un organization policy snapshot y resource catalog snapshot.
- **Run configuration binding:** identidad inmutable de la versión, activación,
  policy, catálogo y configuración efectiva confirmada durante admisión.
- **Opaque secret reference:** identificador validado que puede ser resuelto
  únicamente por composición en la frontera privada; nunca contiene el valor.

## R1 — Autoridad y scope del project key

WHEN una operación valida, publica, activa, revierte, consulta o exporta una
configuración, THE configuration service SHALL exigir una authority creada por
composición para la acción, `ProjectID` y `RepositoryID` exactos antes de
acceder al estado del proyecto.

### Acceptance criteria

- GIVEN una authority para otro project, repository o acción WHEN se usa THEN
  la operación devuelve `unauthorized` sin revelar si el recurso existe.
- GIVEN IDs opacos conocidos pero sin authority WHEN se consulta o publica THEN
  el store no es invocado.
- GIVEN una identity autenticada por CLI, API/MCP o GitHub WHEN composición crea
  authority THEN el payload no puede cambiar actor, project, repository ni
  acciones concedidas.
- GIVEN feature 37 ausente WHEN se autoriza THEN esta feature no inventa roles,
  membresías ni decisiones RBAC; consume únicamente la authority scoped.

## R2 — Documento versionado, estricto y canónico

WHEN se carga un candidate document, THE loader SHALL aceptar exclusivamente el
schema `featureproof.project-config/v1`, decodificarlo de forma estricta,
normalizarlo determinísticamente y calcular su digest sobre bytes canónicos.

### Acceptance criteria

- GIVEN campos desconocidos, duplicados, tipos incorrectos, trailing data,
  valores nulos ambiguos o una versión desconocida WHEN se carga THEN falla
  antes de resolver referencias o persistir estado.
- GIVEN mapas/listas semánticamente equivalentes en distinto orden WHEN se
  canonicalizan THEN producen los mismos bytes y digest.
- GIVEN un cambio material en project, repository, referencias, límites,
  approval o retention WHEN se canonicaliza THEN cambia el digest.
- GIVEN input vacío o mayor que el límite composition-owned WHEN se carga THEN
  se rechaza sin reservar memoria sin límite.

## R3 — Validación estructural y límites

WHEN un candidate document se valida, THE validator SHALL comprobar
obligatoriedad, cardinalidad, unicidad, formatos, relaciones internas y límites
antes de cualquier activación.

### Acceptance criteria

- GIVEN project/repository ausente o distinto del scope, IDs no canónicos,
  referencias duplicadas, listas vacías requeridas o ciclos de fixture WHEN se
  valida THEN devuelve `invalid` con location segura.
- GIVEN contratos, providers, identities, fixtures, profiles, policies o
  límites por encima de sus máximos WHEN se valida THEN falla de forma acotada.
- GIVEN approval o retention fuera de su vocabulario cerrado WHEN se valida
  THEN no se aplica ningún default implícito.
- GIVEN una configuración válida WHEN se valida repetidamente THEN el resultado
  y el orden de diagnósticos son deterministas.

## R4 — Referencias existentes y compatibles

WHEN se valida semánticamente una configuración, THE validator SHALL resolver
cada referencia contra un único resource catalog snapshot y comprobar kind,
scope, versión, expected digest y compatibilidad cruzada.

### Acceptance criteria

- GIVEN una referencia inexistente, stale, de otro project/repository, con kind
  distinto o digest diferente WHEN se valida THEN devuelve
  `missing_reference` o `incompatible_reference` sin activar.
- GIVEN un fixture que usa una operación no presente en los contratos, una
  identity que no autoriza esa operación, o un provider/profile incompatible
  WHEN se valida THEN falla antes de persistir una versión activa.
- GIVEN runtime/model/auth profile, projection policy o retention profile WHEN
  se referencia THEN su versión y digest forman parte de la validación y del
  snapshot efectivo.
- GIVEN el catálogo cambia entre validación preliminar y publicación WHEN se
  publica THEN se revalida bajo el digest bloqueado y el drift falla cerrado.

## R5 — Referencias secretas opacas y cero valores secretos

THE project configuration SHALL representar credenciales únicamente mediante
opaque secret references y SHALL NOT resolver, aceptar, persistir, registrar,
consultar ni exportar valores secretos.

### Acceptance criteria

- GIVEN identities, providers o auth profiles WHEN se materializan THEN solo
  contienen referencias opacas validadas; el resolver secreto no forma parte
  del loader, validator, store ni query service.
- GIVEN campos como token, password, private key, authorization header o bytes
  de credencial WHEN aparecen en el documento THEN el strict decoder o validator
  los rechaza sin incluir el valor en el error.
- GIVEN canaries en errores de catálogo, store, observer o export WHEN se
  inspeccionan eventos, logs y respuestas THEN no aparece ningún valor.
- GIVEN un run bound WHEN necesita una credencial THEN la referencia solo se
  resuelve just-in-time por la frontera privada ya autorizada; el binding sigue
  siendo no secreto.

## R6 — Defaults organizativos monotónicos

WHEN se calcula la configuración efectiva, THE policy merger SHALL aplicar
defaults explícitos del organization policy snapshot y SHALL impedir que la
configuración de proyecto amplíe permisos, recursos o límites organizativos.

### Acceptance criteria

- GIVEN un campo de proyecto ausente WHEN existe default organizativo THEN se
  usa el default y su policy digest queda ligado al resultado efectivo.
- GIVEN límites numéricos de proyecto WHEN exceden ceilings organizativos THEN
  falla; valores más restrictivos se conservan.
- GIVEN approval organizativa `required` WHEN el proyecto intenta desactivarla
  THEN falla; el proyecto sí puede exigir aprobación cuando el default no la
  exige.
- GIVEN retention con mínimo/máximo organizativo WHEN el proyecto sale del
  rango THEN falla, sin convertir menor TTL en permiso para violar retención
  mínima.
- GIVEN allowlists de contracts/providers/identities/fixtures/capabilities WHEN
  el proyecto añade un elemento no permitido THEN falla; nunca se usa unión que
  ensanche autoridad.

## R7 — Versiones inmutables e historial

WHEN una configuración validada se publica, THE store SHALL crear una versión
monotónica e inmutable y un evento append-only que conserven actor, project key,
schema, digests y relación causal sin guardar payloads sensibles.

### Acceptance criteria

- GIVEN una versión publicada WHEN se intenta sobrescribir THEN se rechaza aun
  si todavía no está activa.
- GIVEN reload/restart WHEN se carga una versión THEN sus bytes canónicos y
  digests coinciden y cualquier alteración produce `integrity`.
- GIVEN history WHEN se consulta THEN mantiene orden total por versión y
  activación, sin derivarlo de timestamps ni logs.
- GIVEN una versión referenciada por un run WHEN cambia el head THEN la versión
  histórica sigue disponible y no se modifica.

## R8 — Publicación y activación atómicas e idempotentes

WHEN un actor publica y activa una actualización, THE service SHALL revalidar
authority, expected head, policy/catalog snapshots y candidate digest y SHALL
confirmar version, activation, head, audit event y operation receipt en una
única transacción o publicación atómica equivalente.

### Acceptance criteria

- GIVEN expected generation correcta WHEN se publica THEN el head avanza una
  vez y ninguna admisión observa estado parcial.
- GIVEN dos publishers concurrentes sobre el mismo head WHEN confirman THEN
  exactamente uno avanza y el otro recibe `stale_head`.
- GIVEN la misma idempotency key y mismos bytes WHEN se redeliver THEN devuelve
  el mismo receipt; misma key con digest distinto devuelve conflict.
- GIVEN fallo antes de commit WHEN se reintenta THEN no quedan version/head/event
  parciales; GIVEN commit desconocido THEN se resuelve por receipt/head sin
  publicar otra versión.
- GIVEN fallo del audit event durable WHEN se publica THEN no avanza el head.

## R9 — Rollback mediante nueva activación

WHEN un actor solicita rollback, THE service SHALL validar una versión
histórica contra la policy y el catálogo actuales y crear una nueva activation
generation que la seleccione sin mutar ni duplicar esa versión.

### Acceptance criteria

- GIVEN una versión histórica compatible y expected head vigente WHEN se
  revierte THEN el nuevo head referencia esa versión y el historial conserva
  origen, destino, actor y razón estable `rollback`.
- GIVEN una versión histórica ahora incompatible, corrupta o de otro project
  WHEN se revierte THEN no cambia el head.
- GIVEN rollback concurrente con publish WHEN compiten por la misma generation
  THEN solo una operación confirma por CAS.
- GIVEN rollback redelivered WHEN la operation identity coincide THEN converge
  al mismo activation receipt.

## R10 — Consulta y export seguros

WHEN una authority autorizada consulta active, version, effective snapshot o
history, THE query service SHALL devolver una copia canónica, acotada y no
secreta con orden y paginación estables.

### Acceptance criteria

- GIVEN configuración con secret references WHEN se consulta/exporta THEN las
  referencias opacas pueden aparecer, pero nunca sus valores ni material de
  resolvers.
- GIVEN cursor alterado, expirado o de otro project key WHEN se pagina history
  THEN se rechaza antes de leer filas útiles.
- GIVEN caller que muta el DTO devuelto WHEN vuelve a consultar THEN no altera
  la versión persistida.
- GIVEN documento activo inexistente WHEN se consulta con authority válida THEN
  devuelve `not_found` sin defaults implícitos.

## R11 — Binding inmutable al run

WHEN se admite un run, THE admission resolver SHALL capturar atómicamente la
activation activa, configuration version, organization policy snapshot,
resource catalog snapshot y effective configuration, y SHALL persistir un run
configuration binding canónico antes de crear trabajo ejecutable.

### Acceptance criteria

- GIVEN una admisión válida WHEN confirma THEN el binding incluye project key,
  version/digest, activation generation/digest, policy version/digest, catalog
  digest y effective digest.
- GIVEN activación o defaults nuevos después de admitir WHEN el run continúa o
  reinicia THEN usa únicamente su binding/snapshot persistido.
- GIVEN retry/redelivery de la misma admisión WHEN el binding coincide THEN
  converge; si la misma run identity intenta otro digest devuelve conflict.
- GIVEN crash entre binding y creación causal de run/job WHEN se recupera THEN
  ambos existen juntos o ninguno, según el backend, sin run no vinculado.

## R12 — Admisión fail-closed y compatibilidad de runs existentes

WHEN se solicita un run nuevo, THE ingress SHALL rechazar una configuración
ausente, inválida, unsupported, obsolete o incompatible, mientras SHALL permitir
que runs ya vinculados continúen sin releer el active head.

### Acceptance criteria

- GIVEN no active head, schema fuera de ventana, policy/catalog drift, referencia
  retirada o effective config inválida WHEN se admite THEN no se crea inbox,
  run, job, environment ni model call.
- GIVEN un run bound antes del drift WHEN se reanuda THEN no se rebindea ni se
  altera su snapshot; una credencial revocada puede causar fallo operacional,
  nunca selección silenciosa de otra referencia.
- GIVEN active head cambia durante admisión WHEN se bloquea/valida THEN la
  operación usa una única generación o falla stale; no mezcla snapshots.
- GIVEN modo local de desarrollo WHEN falta configuración THEN también falla
  cerrado; local no es bypass ni fallback.

## R13 — Persistencia local y PostgreSQL equivalentes

THE implementation SHALL ofrecer un store consumer-owned con adapters local y
PostgreSQL que preserven las mismas invariantes de versiones, heads, receipts,
events, bindings, CAS e integridad, seleccionando exactamente un backend.

### Acceptance criteria

- GIVEN la misma contract suite WHEN se ejecuta contra local y PostgreSQL THEN
  observa los mismos resultados y errores de dominio.
- GIVEN restart y segundo proceso WHEN se reabre el backend THEN active,
  history, receipts y run bindings permanecen disponibles.
- GIVEN backend configurado no disponible WHEN se opera THEN devuelve
  `unavailable`; nunca cae al otro adapter ni dual-write.
- GIVEN migración PostgreSQL desde cero/latest/concurrente WHEN se ejecuta THEN
  es versionada, idempotente y compatible con la ventana N/N+1 del control
  plane.

## R14 — Cancelación, concurrencia, retries y errores

WHEN una operación de configuración bloquea o atraviesa persistencia, THE
implementation SHALL propagar context, limitar waits/retries y clasificar
errores sin parsing de mensajes ni I/O externo dentro de locks/transactions.

### Acceptance criteria

- GIVEN cancelación antes/durante validate, publish, rollback, query o admission
  WHEN se observa THEN termina acotadamente sin publicación parcial.
- GIVEN serialization/deadlock transitorio sobre una operación idempotente WHEN
  se reintenta THEN se repite la transacción completa bajo budget; validation,
  auth, secret resolution o callbacks no ocurren dentro de ella.
- GIVEN contadores/versiones/generations al máximo WHEN se avanza THEN falla
  `exhausted` sin wrap.
- GIVEN constraint, unavailable, integrity, stale, unauthorized o timeout WHEN
  cruza adapters THEN conserva una categoría cerrada comprobable.

## R15 — Auditoría y observabilidad no sensibles

WHEN cambia o se consulta configuración, THE system SHALL emitir eventos y
telemetría acotados con actor seguro, project key, acción, versiones, digests,
resultado y categoría, sin convertir observabilidad en fuente de verdad.

### Acceptance criteria

- GIVEN publish/activate/rollback confirmado WHEN se inspecciona journal THEN el
  evento causal está en el mismo commit que el cambio autoritativo.
- GIVEN validación o autorización rechazada WHEN se observa THEN solo se emite
  categoría y metadatos allowlisted; el raw document y collaborator errors no
  aparecen.
- GIVEN observer/sink de métricas fallido WHEN la verdad durable ya confirmó
  THEN no cambia receipt/head; el evento transaccional sí es obligatorio.
- GIVEN consultas e ingress WHEN se miden THEN no registran secret values,
  canonical document bytes, DSN, target URL ni credential material.

## R16 — Integración de superficies productivas

WHEN CLI, API/MCP o GitHub admiten una validación, THE composition SHALL derivar
el project key de identidad confiable, resolver la effective configuration
activa y construir únicamente desde ella los contratos, provider/target,
identities, fixtures, runtime/model profile, policies, limits, approval y
retention del run.

### Acceptance criteria

- GIVEN alias/flags/payload que intentan seleccionar recursos no incluidos WHEN
  se admite THEN no amplían la configuración ni sustituyen sus digests.
- GIVEN CLI de administración WHEN valida, publica, revierte, muestra o lista
  THEN usa el mismo application service y authority que otros adapters; no
  escribe stores directamente.
- GIVEN GitHub repository identity autorizada WHEN llega webhook THEN se mapea a
  un único project key antes de consultar active config.
- GIVEN API/MCP o external agent WHEN solicita run THEN transporta IDs/aliases
  permitidos, no documentos de configuración, secret refs nuevos ni valores.
- GIVEN retention/approval/limits efectivos WHEN el run se crea THEN quedan en
  el binding y alimentan los controllers existentes, no defaults del prompt.

## R17 — Comentarios de responsabilidad sistémica

THE implementation SHALL documentar entidades/tipos, funciones, métodos y
helpers de test nuevos o modificados con su responsabilidad, frontera o
invariante; constantes y variables de paquete no evidentes SHALL identificar su
consumidor o papel compartido.

### Acceptance criteria

- GIVEN símbolos dentro de ese alcance WHEN se audita el diff completo THEN
  poseen comentarios útiles, incluidos tipos privados, errors, migrations,
  fakes, factories y assertion helpers.
- GIVEN local, parámetro o receiver trivial WHEN se audita THEN no requiere
  comentario mecánico; autoridad, fencing, durabilidad u ownership ocultos se
  expresan con un tipo/operación explícitos o comentario puntual.
- GIVEN paráfrasis, boilerplate o familias interpoladas WHEN se auditan THEN no
  satisfacen el requisito.

## R18 — Verificación determinista

THE implementation SHALL demostrar los requirements mediante tests herméticos,
contract suites local/PostgreSQL y un gate PostgreSQL 16 aislado, reproducible y
opt-in, con trazabilidad y cobertura suficiente.

### Acceptance criteria

- GIVEN `go test ./...` WHEN se ejecuta THEN no necesita red, PostgreSQL,
  secretos ni reloj real y no usa `time.Sleep` para sincronización.
- GIVEN gate PostgreSQL autorizado WHEN se ejecuta THEN aplica migraciones,
  contract suites, concurrencia, restart, unknown commit y run binding, y limpia
  únicamente recursos propios aunque falle.
- GIVEN código nuevo/modificado WHEN se verifica THEN pasan gofmt, diff-check,
  vet, compile-only, normal/race, cross-builds aplicables y al menos 85% de
  statement coverage exacta sin redondear sobre el diff.
- GIVEN cada `R<n>` WHEN se revisa THEN existe al menos un test concreto que
  fallaría al retirar su garantía.

## Errores y casos límite globales

- Toda identidad durable usa formato y tamaño acotados; versiones, generations,
  sequences y counters no hacen wrap.
- Timestamps son UTC y provienen de clock inyectado; no ordenan autoridad.
- Canonicalización no sigue symlinks, URLs ni paths proporcionados por el
  documento. Los adapters de recursos poseen sus raíces autorizadas.
- Un `not_found` no se deduce de timeout, permiso, conexión o decode failure.
- Un organization policy snapshot ausente o no verificable bloquea nuevas
  admisiones; no se usan defaults embebidos en dominio.
- Cambiar defaults puede cambiar la effective configuration de nuevas
  admisiones, pero siempre crea otro effective digest y nunca altera bindings
  existentes.
- Una config activa puede quedar incompatible con catálogo/policy actuales; el
  head se conserva para auditoría, pero nuevas admisiones fallan hasta publish
  o rollback compatible.
- Config versions y audit events no se borran en esta feature. Retention de runs
  no rompe sus referencias; GC de configuración exige una feature posterior.
- Secret reference revocada no permite fallback a otra identidad. La ejecución
  bound falla según la frontera secreta, sin reescribir la configuración.

## Decisiones cerradas

- El formato canónico publicado es JSON versionado. Adapters de autoría futuros
  pueden traducir YAML, pero no forman parte de esta feature.
- Rollback crea una nueva activation generation hacia una versión histórica;
  no crea otra versión ni reescribe historial.
- Organization policy y resource catalog son snapshots confiables suministrados
  por composición. Su administración, RBAC y aislamiento pertenecen a feature
  37.
- Local y PostgreSQL implementan el mismo port, pero una composición selecciona
  solo uno. No existe dual-write ni fallback.
- La configuración productiva es obligatoria para toda nueva admisión. El modo
  local de desarrollo usa el adapter local, no un bypass.

## Decisiones abiertas

No quedan decisiones de producto necesarias para implementar esta feature. Los
valores concretos de tamaños, páginas, timeouts, retry budgets, policy defaults,
allowlists y compatibility windows son entradas obligatorias de composición o
manifests versionados; el dominio no inventa valores.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-11-16T10:24:25+01:00
```

La implementación no puede comenzar hasta aprobación humana explícita y cambio
de estado a `in_progress` por el leader.
