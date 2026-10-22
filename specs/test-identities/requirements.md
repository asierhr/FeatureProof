# Identidades seguras de prueba — Requirements

## Objetivo y problema

FeatureProof necesita validar comportamientos autenticados contra una aplicación
desplegada sin convertir al modelo, al plan o a los artefactos de auditoría en
portadores de credenciales. Esta feature introduce identidades de prueba
declaradas y acotadas a operaciones concretas, junto con una frontera de
composición que resuelve e inyecta el material secreto únicamente después de
superar todas las validaciones y autorizaciones del paso.

## Alcance

- Ampliar la specification versionada con identidades de prueba, referencias
  opacas de credencial, mecanismos de autenticación y permisos por target y
  operación.
- Mantener compatibilidad estricta con specifications v1 mediante una versión
  nueva del contrato; v1 continúa representando ejecuciones sin identidades.
- Exigir que cada operación autenticada seleccione una identidad declarada por
  su ID y quede vinculada a una operación autorizada.
- Proyectar al modelo solo descriptores no secretos de identidades y permisos.
- Resolver credenciales mediante un puerto propio de composición y añadirlas en
  la última frontera del transporte HTTP autorizado.
- Revalidar plan, identidad, operación, target, policy y vigencia antes de abrir
  una conexión al target.
- Redactar material de autenticación en errores, journal, evidencias, contexto,
  reportes y las superficies CLI, API, MCP y GitHub.
- Distinguir fallos de identidad y credenciales de incumplimientos funcionales
  de la aplicación.

## Fuera de alcance

- Crear, rotar, revocar o administrar usuarios en la aplicación validada.
- Definir un vault, un gestor de secretos remoto o una configuración
  multi-tenant; composición recibe resolvers autorizados para referencias ya
  configuradas.
- Aceptar tokens, contraseñas, cookies o API keys en la specification, el plan,
  flags, requests API/MCP, comentarios de GitHub o respuestas del modelo.
- Permitir autenticación inventada por el modelo, autenticación interactiva
  durante un run o negociación automática de mecanismos.
- Entregar credenciales al harness, al agente, al policy evaluator, al evidence
  store o a assertions.
- Ampliar targets, redirects, métodos, paths o operaciones autorizadas por el
  hecho de seleccionar una identidad.
- Persistir material secreto para reanudar un run; tras un reinicio se vuelve a
  resolver la misma referencia opaca y se revalida su vigencia.
- Incorporar en esta versión autenticación mTLS, firmas de requests, OAuth
  interactivo o credenciales inyectadas en bodies. Los mecanismos iniciales son
  bearer, header dedicado y cookie dedicada, todos declarados explícitamente.

## Actores y términos

- **Test identity:** descriptor versionado con ID, target, mecanismo, permisos
  de operación y referencia opaca de credencial.
- **Credential reference:** identificador no secreto que composición relaciona
  con una fuente autorizada; no es una ruta, variable de entorno ni valor que
  el dominio interprete.
- **Credential material:** bytes secretos y metadata mínima de vigencia que
  solo observan el resolver y el transporte autenticado.
- **Identity descriptor:** proyección no secreta que contiene ID, mecanismo y
  operaciones permitidas, pero nunca la referencia ni el material secreto.
- **Operation binding:** unión canónica de capability, target, método y path
  template autorizados, más la operación concreta ya validada.
- **Authenticated transport:** última frontera HTTP que resuelve, comprueba e
  inyecta la credencial sobre una request ya autorizada.
- **Operational failure:** imposibilidad de FeatureProof para ejecutar el paso;
  produce resultado inconclusive/error según el workflow y nunca un fallo
  funcional de la feature validada.

## R1 — Contrato versionado de identidad sin secretos

WHERE a validation needs authenticated operations, THE specification SHALL
declare each test identity with a unique ID, target, closed authentication
mechanism, opaque credential reference and non-empty authorized operation set,
without accepting credential values.

### Acceptance criteria

- GIVEN una specification v2 válida WHEN se carga THEN cada identidad conserva
  ID, target, mecanismo, configuración no secreta, referencia opaca y permisos
  en modelos internos independientes del parser.
- GIVEN dos identidades con el mismo ID, una referencia vacía o con sintaxis de
  path/URL, un target inexistente, un mecanismo desconocido, una operación
  duplicada o incompleta WHEN se carga THEN falla con categoría y field path
  estables antes de crear un run.
- GIVEN un miembro inline `token`, `cookie`, `password`, `api_key` o `value`, o
  un objeto en lugar de `credential_ref`, WHEN se carga THEN se rechaza por
  schema estricto y el valor no aparece en el error.
- GIVEN una specification v1 válida WHEN se carga con el nuevo código THEN
  mantiene exactamente su semántica anterior y no obtiene una identidad por
  defecto.

## R2 — Selección explícita y binding de operación

WHEN a plan step targets an operation that requires authentication, THE plan
validator SHALL require exactly one declared identity ID and SHALL prove that
its target, capability, method and path template match an authorized operation.

### Acceptance criteria

- GIVEN una operación autenticada y una identidad compatible WHEN el paso la
  referencia por ID THEN el plan conserva solo ese ID y el binding canónico de
  operación.
- GIVEN identidad omitida, desconocida, de otro target, de otro mecanismo o sin
  permiso para la operación WHEN se valida el plan THEN se rechaza antes de
  policy, credential resolution, DNS o socket.
- GIVEN una operación pública WHEN el paso omite identidad THEN continúa sin
  autenticación; si selecciona una identidad, esta debe autorizar exactamente
  la operación y no amplía el alcance de red.
- GIVEN un método/path concreto con parámetros WHEN se valida contra su template
  autorizado THEN solo los bindings estructuralmente válidos coinciden; prefijos
  textuales, traversal, escapes ambiguos y otro método se rechazan.

## R3 — Descriptores mínimos para el modelo

WHEN planning context includes test identities, THE context builder SHALL expose
only immutable descriptors containing identity ID, mechanism class and exact
authorized operation bindings needed for the current target and constraints.

### Acceptance criteria

- GIVEN varias identidades WHEN se crea contexto para una operación THEN el
  modelo recibe solo las candidatas autorizadas para ese target y operación.
- GIVEN un descriptor WHEN se serializa THEN no contiene credential reference,
  credential material, expiry exacta, resolver, headers/cookies con valores ni
  handles ejecutables.
- GIVEN que el modelo propone un nombre, mecanismo u operación no presentes en
  los descriptores WHEN se valida su decisión THEN se rechaza; el sistema no lo
  corrige ni busca una alternativa implícita.
- GIVEN mutación posterior de las colecciones de origen WHEN se usa el snapshot
  de contexto THEN sus descriptores y orden canónico no cambian.

## R4 — Orden obligatorio de controles e inyección tardía

WHEN an authenticated HTTP step is about to execute, THE system SHALL validate
the plan, identity, operation, target, runtime policy and budget before resolving
or injecting credential material, and SHALL inject it only in the authorized
HTTP transport immediately before the exchange.

### Acceptance criteria

- GIVEN cualquier fallo estructural, de identidad, target, operación, policy o
  budget WHEN se procesa el paso THEN el resolver no se invoca y no existe DNS,
  dial ni request.
- GIVEN todos los controles aprobados WHEN se ejecuta el paso THEN el HTTP tool,
  harness y policy observan solo el identity ID/binding no secreto, mientras el
  transporte añade el valor al header bearer, header dedicado o cookie dedicada.
- GIVEN una request ya validada WHEN la inyección cambia headers THEN se vuelven
  a comprobar límites finales y nombres reservados antes de DNS o dial.
- GIVEN un redirect WHEN se considera seguirlo THEN se revalidan target,
  operación y autorización; nunca se reenvía una credencial a otra autoridad,
  path no autorizado o método incompatible.

## R5 — Resolución acotada, vigencia y aislamiento

WHEN the authenticated transport resolves an opaque credential reference, THE
credential boundary SHALL return material scoped to the selected identity and
operation, SHALL verify its validity at the trusted clock, and SHALL release it
after the attempt without exposing it to another identity or retry.

### Acceptance criteria

- GIVEN dos identidades y secretos canario distintos WHEN se ejecutan en
  cualquier orden o concurrentemente THEN cada request usa solo el material de
  su referencia y no existe contaminación cruzada.
- GIVEN material ausente, vacío, expirado o incompatible con el mecanismo WHEN
  se resuelve THEN el paso termina con error tipado antes de conectar al target.
- GIVEN un retry permitido WHEN comienza un nuevo intento THEN vuelve a validar
  identidad/policy/vigencia y obtiene material para ese intento; no reutiliza a
  ciegas bytes expirados ni amplía el presupuesto.
- GIVEN finalización, error o cancelación en cualquier frontera WHEN termina el
  intento THEN se ejecuta una sola liberación best-effort del material y no
  queda almacenado en estado durable del run.

## R6 — Redacción cerrada de autenticación

THE system SHALL redact authentication headers, cookies, configured credential
locations and authentication-related body fields before any value reaches a
model, journal, evidence, report, log, metric, error or external interface.

### Acceptance criteria

- GIVEN canarios en bearer, header dedicado, cookie, body de respuesta, error de
  resolver y error de transporte WHEN se recorren éxito y fallo THEN ninguno
  aparece en artefactos, eventos, proyecciones, mensajes ni error chains
  publicables.
- GIVEN nombres configurados con distinta capitalización WHEN se sanea una
  estructura THEN se redacted de forma case-insensitive y antes de truncar o
  persistir.
- GIVEN una respuesta que establece o refleja credenciales WHEN se crea
  evidencia THEN `Set-Cookie`, cookies, auth headers y paths de body configurados
  se sustituyen por el marcador estable de redacción.
- GIVEN una redacción que falla o un valor que no puede clasificarse con
  seguridad WHEN se intenta persistir o proyectar THEN se falla cerrado y no se
  conserva el payload crudo.

## R7 — Fallos tipados sin falso fallo funcional

IF an identity is unknown, unauthorized, unresolved, expired, incompatible or
cancelled, THEN THE executor SHALL stop the step before a target connection and
classify the condition as an operational outcome, never as evidence that the
validated feature failed.

### Acceptance criteria

- GIVEN cada condición anterior WHEN se ejecuta THEN el error ofrece categoría,
  fase y field path seguros mediante `errors.Is`/`errors.As`, sin comparar texto.
- GIVEN fallo de identidad o credencial WHEN el workflow agrega resultados THEN
  el paso/restricción queda inconclusive o el run queda en error/cancelled según
  la causa; nunca `failed` por una assertion de producto ni `passed`.
- GIVEN un dialer/transport spy WHEN ocurre el rechazo THEN registra cero
  conexiones y cero bytes de request.
- GIVEN errores externos con URLs, headers, bodies o secretos WHEN cruzan la
  frontera THEN la causa publicable conserva solo cancelación/deadline u otra
  metadata allowlisted.

## R8 — Revalidación, retries e idempotencia

WHILE an authenticated step may be retried or resumed, THE executor SHALL bind
identity and operation to the approved plan and current run context and SHALL
revalidate that binding before every attempt without treating credentials as
part of an idempotency key or persisted plan digest.

### Acceptance criteria

- GIVEN drift de specification, identidad, target, operation binding, policy o
  aprobación entre planificación y ejecución WHEN se alcanza el checkpoint
  pre-I/O THEN el paso se detiene sin resolver credenciales ni abrir red.
- GIVEN redelivery de CLI/API/MCP/GitHub o restart WHEN el mismo trabajo ya está
  confirmado THEN no se duplica la operación; para trabajo pendiente se vuelve
  a resolver la referencia mediante composición.
- GIVEN error transitorio del target WHEN la política permite retry THEN cada
  intento consume budget y genera eventos separados, manteniendo el mismo
  identity ID y operation binding.
- GIVEN error de identidad permanente o expiración WHEN se clasifica THEN no se
  reintenta como fallo transitorio de red.

## R9 — Paridad de superficies y autoridad de composición

WHEN validation is requested through CLI, API, MCP or GitHub, THE adapters SHALL
invoke the same identity-aware application service and SHALL NOT accept raw
credentials, credential-source locations or self-asserted authorization in the
request.

### Acceptance criteria

- GIVEN el mismo snapshot autorizado WHEN se inicia desde cualquiera de las
  cuatro superficies THEN identidad, operation binding, resultado y códigos de
  error son equivalentes.
- GIVEN token, cookie, password, API key, resolver o credential reference en un
  payload externo no permitido WHEN se decodifica THEN se rechaza por schema
  estricto antes de admission o red.
- GIVEN un webhook/comentario de GitHub WHEN contiene instrucciones para usar
  otra identidad o secreto THEN permanece como dato no confiable y no modifica
  la selección sellada.
- GIVEN una composición sin resolver registrado para una referencia declarada
  WHEN se inicia el trabajo THEN falla cerrado antes de inferencia, aprobación o
  ejecución; no existe fallback entre identidades.

## R10 — Auditoría segura y reproducible

WHEN identity-aware work changes state, THE journal SHALL record only safe
identity and authorization facts sufficient to audit selection and outcome,
including run, plan, step, identity ID, operation-binding digest and stable
outcome category, without credential references or material.

### Acceptance criteria

- GIVEN un intento autorizado WHEN se inspecciona el journal THEN puede
  demostrarse qué identity ID y operation binding no secreto fueron usados y en
  qué intento, sin reconstruir la credencial.
- GIVEN un rechazo previo a red WHEN se inspecciona THEN existe un evento seguro
  de rechazo solo después de que el run tenga journal; no se persiste input
  secreto ni texto externo.
- GIVEN reintentos o cancelación WHEN se ordenan eventos THEN cada intento posee
  una transición terminal y el run conserva orden append-only.
- GIVEN dos ejecuciones históricas WHEN la credencial subyacente rotó THEN cada
  una mantiene su binding no secreto original sin guardar versiones del secreto.

## R11 — Cancelación y concurrencia sin exposición

WHEN context cancellation or deadline occurs during validation, credential
resolution, injection, transport or redaction, THE system SHALL stop subsequent
work, release owned material and return a typed cancellation outcome without
leaking credentials or leaving background work.

### Acceptance criteria

- GIVEN cancelación en cada frontera WHEN termina la llamada THEN no hay trabajo
  posterior, goroutine viva, request tardía ni persistencia de material secreto.
- GIVEN resoluciones concurrentes de la misma referencia WHEN el resolver
  comparte trabajo THEN cada waiter puede cancelar sin recibir credenciales de
  otra identidad y el propietario tiene cierre y join explícitos.
- GIVEN una carrera entre cancelación y autorización/inyección WHEN se adquiere
  el derecho a continuar THEN se vuelve a comprobar `ctx` inmediatamente antes
  de resolver y antes de abrir red.
- GIVEN ejecución con race detector y barreras deterministas WHEN se repite THEN
  no hay data races, deadlocks ni coordinación mediante sleeps.

## Errores y casos límite

- Specifications v1 no aceptan el nuevo campo por reinterpretación silenciosa;
  el loader despacha v1 o v2 y conserva las reglas estrictas de cada contrato.
- IDs y referencias son case-sensitive. Nombres de headers se comparan sin
  distinguir mayúsculas, pero se conserva una forma canónica segura.
- Una identidad no puede autorizar otra identidad, targets comodín, métodos
  comodín, redirects globales ni paths por simple prefijo textual.
- Una misma operación puede permitir varias identidades; cada paso elige una y
  el sistema nunca selecciona la primera automáticamente.
- La ausencia de seguridad en un contrato no autoriza a omitir identidad cuando
  otra configuración confiable declara la operación como autenticada; una
  contradicción produce contexto/policy insuficiente.
- Un secret backend puede rotar el material entre intentos. La identidad y sus
  permisos permanecen estables; el valor no forma parte de la evidencia.
- La expiración se evalúa con reloj inyectado. No se usa hora enviada por un
  caller ni texto del proveedor.
- Un response 401/403 del target es evidencia funcional solo si la conexión se
  realizó con una credencial válida y el plan contiene una assertion explícita;
  un fallo local de resolución nunca se transforma en ese resultado.

## Decisiones abiertas

No quedan decisiones de producto abiertas para esta versión. La fuente concreta
de secretos se elige en composición detrás del puerto definido aquí; mTLS,
firmas, OAuth interactivo e inyección en bodies requerirán specs posteriores.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-22T15:27:02+02:00
```

La implementación no puede comenzar hasta que una persona revise y apruebe
esta spec y el leader cambie la feature a `in_progress`.
