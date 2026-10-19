# Adapter de entornos preview existentes — Requirements

## Objetivo y problema

FeatureProof necesita ejecutar una validación contra un despliegue real sin
confiar en un target o una revisión aportados libremente por quien solicita el
run. Esta feature introduce una frontera independiente del proveedor que
selecciona y reserva un preview ya existente, demuestra qué repositorio y commit
están desplegados, autoriza su target de red y libera la reserva al finalizar.

## Alcance

- Selección explícita mediante un alias de catálogo confiable fijado para el
  run; no hay búsqueda implícita ni proveedor por defecto.
- Contrato propio y neutral para descubrir/reservar, consultar readiness,
  refrescar la atestación y liberar un entorno preview existente.
- Snapshot seguro con identidad estable, target, disponibilidad y metadatos de
  revisión, sin credenciales ni payloads del proveedor.
- Integración con `revision-bound-validation`, la política de red, el ejecutor
  autorizado y los consumidores CLI/MCP/GitHub existentes.
- Timeouts, cancelación, reintentos acotados e idempotencia de reserva y
  liberación.
- Tests herméticos mediante un provider falso programable.

## Fuera de alcance

- Crear, desplegar, modificar, reiniciar, escalar o destruir infraestructura.
- Hacer checkout, merge, push o cualquier mutación del repositorio.
- Elegir automáticamente un entorno a partir de nombres de rama, URLs, texto
  del modelo, payloads de GitHub u otros datos no confiables.
- Definir un API de un proveedor cloud concreto, un scheduler de capacidad, un
  protocolo entre control plane y runners remotos o una estrategia
  multi-tenant.
- Entregar credenciales de control plane o de la aplicación al modelo, al
  journal, al binding, a evidencias o a reportes.
- Considerar una afirmación manual del solicitante como prueba de la revisión
  desplegada.

## Actores y términos

- **Solicitante:** inicia un run y selecciona un alias previamente autorizado;
  no aporta URLs, credenciales ni identificadores internos del proveedor.
- **Catálogo de previews:** configuración confiable que relaciona un alias con
  un provider ID, una referencia opaca de entorno y un perfil de ejecución.
- **Provider:** implementación inyectada del contrato propio. Solo observa y
  reserva lógicamente entornos ya existentes.
- **Reserva:** concesión opaca e idempotente que impide atribuir dos usos
  incompatibles al mismo entorno cuando el provider exige exclusividad. No es
  una creación ni una mutación de infraestructura.
- **Snapshot:** observación segura y acotada de identidad, target,
  disponibilidad, repositorio, commit, referencia/expiración de atestación y
  versión de observación.
- **Target autorizado:** base URL normalizada que ha superado la política local
  de esquema, host, puerto y path; el HTTP tool conserva además sus controles
  por petición, DNS y redirección.
- **Ejecutor autorizado:** composición de aplicación que recibe el perfil
  confiable, fija el binding y registra únicamente capacidades permitidas.

## Requirements EARS

### R1 — Selección explícita e independiente del proveedor

WHEN un run solicita un entorno preview, THE system SHALL resolve exactly one
explicit authorized selection alias to one configured provider and environment
reference before discovery; THE system SHALL NOT accept a provider, target,
environment reference or credential supplied through model output or other
untrusted request fields.

#### Acceptance criteria

- GIVEN un alias autorizado para el principal o la política GitHub actual
  WHEN se inicia el run
  THEN el adapter recibe exactamente el provider ID y la referencia opaca
  fijados por el catálogo.
- GIVEN un alias ausente, desconocido, duplicado o no autorizado
  WHEN se intenta iniciar
  THEN no se llama a ningún provider ni se crea trabajo funcional.
- GIVEN texto del modelo, una URL o metadatos de un webhook
  WHEN se construye la selección
  THEN esos datos no pueden sustituir provider, entorno, target ni perfil.
- GIVEN dos implementaciones del contrato propio
  WHEN se componen por separado
  THEN el caso de uso utiliza los mismos modelos internos y no importa DTOs ni
  SDKs de ninguna de ellas.

### R2 — Reserva e identidad segura del entorno

WHEN una selección autorizada se descubre, THE adapter SHALL create or
reconcile one logical reservation for the run idempotency key and return a
bounded snapshot containing stable environment identity, reservation identity,
normalized target candidate, availability and revision metadata without secret
material.

#### Acceptance criteria

- GIVEN un preview existente y disponible
  WHEN se reserva
  THEN el snapshot contiene provider ID, environment ID estable, reservation
  ID opaco, target candidato, estado, repository ID, commit OID completo,
  attestation reference, vigencia y versión de observación.
- GIVEN un retry, recuperación o llamada concurrente con la misma clave y
  selección
  WHEN el provider reconcilia la reserva
  THEN devuelve la misma identidad lógica y no consume otra reserva.
- GIVEN la misma clave con otra selección, repositorio o commit esperado
  WHEN se reserva
  THEN se devuelve conflicto de integridad y no se amplía la reserva anterior.
- GIVEN logs, estado durable, binding, eventos, reportes y snapshots
  WHEN se inspeccionan
  THEN no contienen tokens, passwords, cookies, claves, headers de auth ni
  payloads opacos de credenciales.

### R3 — Atestación exacta antes de validar

WHEN a reservation snapshot is admitted, THE system SHALL verify through the
configured trusted attestation mechanism that its canonical repository and
complete commit OID exactly match the revision bound to the run, and SHALL
recheck the same environment, revision and attestation freshness before each
new executable batch and before terminal publication.

#### Acceptance criteria

- GIVEN una atestación confiable, vigente y coincidente
  WHEN finaliza la admisión
  THEN `revisionbinding` sella environment ID, repository ID, commit OID y una
  referencia segura de atestación antes de ejecutar herramientas.
- GIVEN repositorio o commit distinto, OID parcial, atestación ausente,
  expirada, no verificada o con environment ID distinto
  WHEN se admite
  THEN ninguna herramienta se ejecuta ni se publica un resultado funcional.
- GIVEN que target, environment ID, repository, commit o attestation reference
  derivan durante el run
  WHEN se revalida
  THEN no comienza otro batch y el run queda operacional o `inconclusive`,
  nunca `failed` por esa causa.
- GIVEN una afirmación del solicitante o del modelo sobre el commit desplegado
  WHEN no existe observación confiable del provider
  THEN no se considera atestación.

### R4 — Target sometido de nuevo a política de red

WHEN the provider returns a target candidate, THE system SHALL normalize and
authorize its scheme, hostname, effective port and base-path boundary through a
trusted local network policy before constructing any network capability; the
authorized target SHALL be immutable for that reservation and SHALL NOT be
replaceable by the model.

#### Acceptance criteria

- GIVEN un target permitido
  WHEN se autoriza
  THEN su identidad normalizada se incorpora al contexto contractual y a la
  configuración segura del binding.
- GIVEN esquema, host, puerto, userinfo, query, fragment, path o longitud
  denegados
  WHEN se evalúa
  THEN no se resuelve DNS ni se abre un socket y la reserva se libera.
- GIVEN un plan o respuesta del modelo con una URL absoluta distinta
  WHEN se valida el plan o la llamada
  THEN se rechaza antes de red y no altera el target fijado.
- GIVEN una petición o redirección durante la ejecución
  WHEN el HTTP tool la procesa
  THEN mantiene sus comprobaciones existentes de destino, DNS pinning,
  esquema, autoridad, path y redirección dentro del target autorizado.

### R5 — Credenciales confinadas a composición y transporte autorizado

WHEN a configured provider or execution profile requires credentials, THE
composition root SHALL resolve them by opaque profile reference and pass secret
bytes only to the specific provider transport or authorized execution transport
that needs them; core, adapter models, harness, model context and publication
SHALL receive no secret bytes.

#### Acceptance criteria

- GIVEN provider credentials
  WHEN discovery, readiness or release calls are made
  THEN only that provider transport observes them.
- GIVEN application credentials required by the selected profile
  WHEN una herramienta autorizada se compone
  THEN solo su transporte recibe el material y el plan/binding conserva como
  máximo una referencia opaca clasificada.
- GIVEN un perfil sin secretos
  WHEN se usa para un fork autorizado
  THEN no se resuelve ningún perfil secreto de provider o aplicación que la
  política haya prohibido.
- GIVEN secretos centinela
  WHEN se inspeccionan errores, logs, eventos, snapshots, package, evidencias y
  contexto del modelo
  THEN no aparece ningún valor ni un digest derivado de sus bytes.

### R6 — Readiness verificable y estable

WHEN a reservation exists, THE system SHALL poll readiness through the provider
under configured bounds until the same environment is healthy and ready, or a
terminal operational condition occurs; only a ready snapshot that still
satisfies R3 and R4 SHALL permit functional execution.

#### Acceptance criteria

- GIVEN estados `pending` seguidos por `ready`
  WHEN se consulta readiness
  THEN se respetan intervalo, máximo de intentos y deadline configurados y se
  inicia la validación una sola vez.
- GIVEN `unavailable`, `unhealthy`, estado desconocido o respuesta inconsistente
  WHEN se consulta
  THEN no se ejecuta una herramienta y se conserva una categoría operativa
  cerrada.
- GIVEN `ready` con otra identidad, revisión, target o atestación
  WHEN se compara con la reserva
  THEN se trata como deriva y no como readiness válida.
- GIVEN un provider que permanece `pending`
  WHEN se agota el presupuesto
  THEN termina de forma acotada sin espera infinita ni `time.Sleep` en tests.

### R7 — Timeout y cancelación en toda operación bloqueable

WHEN discovery/reservation, readiness, attestation refresh or release crosses a
provider boundary, THE adapter SHALL propagate context cancellation and apply a
positive operation timeout bounded by the run deadline; after main-context
cancellation, release MAY use a separately owned cleanup context only with its
own configured finite timeout.

#### Acceptance criteria

- GIVEN un provider bloqueado en cualquier operación
  WHEN vence su timeout
  THEN la llamada termina con categoría `timeout` y no comienza la fase
  siguiente.
- GIVEN cancelación del run durante una llamada o espera
  WHEN se observa
  THEN la operación termina, no se inicia nuevo trabajo y se intenta release
  dentro del presupuesto de cleanup.
- GIVEN un deadline del caller más corto que el timeout configurado
  WHEN se ejecuta
  THEN prevalece el deadline más corto.
- GIVEN shutdown
  WHEN existen workers del adapter
  THEN se cancelan y se hace join dentro del límite configurado.

### R8 — Clasificación operacional separada del veredicto funcional

IF the environment is unavailable, unhealthy, unverified, revision-mismatched,
network-denied, timed out or cancelled before evidence can establish a
constraint, THEN THE system SHALL report a typed operational error before run
creation or an `inconclusive`/`cancelled` run after durable admission, and SHALL
NOT classify that condition as a functional `failed` constraint.

#### Acceptance criteria

- GIVEN fallo antes de crear el run/journal
  WHEN se devuelve al consumidor
  THEN se expone solo una categoría segura y no existe resultado funcional.
- GIVEN deriva o pérdida de readiness después del evento de admisión
  WHEN se cierra el run
  THEN el outcome es `inconclusive` o `cancelled` según la causa y no `failed`.
- GIVEN assertions ejecutadas que contradicen la feature en un entorno aún
  válido
  WHEN finaliza
  THEN el resultado `failed` sigue perteneciendo al motor determinista y no al
  adapter.
- GIVEN un mensaje o body arbitrario del provider
  WHEN se proyecta el error
  THEN no cruza la frontera pública.

### R9 — Liberación idempotente y obligatoria

WHEN a reservation has been confirmed, THE system SHALL attempt its idempotent
release after every success, functional failure, inconclusive result,
operational error or cancellation; retries and recovery with the same release
key SHALL converge on `released` without releasing another reservation or
destroying the environment.

#### Acceptance criteria

- GIVEN cualquier salida posterior a la reserva
  WHEN finaliza el owner del run
  THEN se invoca release exactamente con provider ID, reservation ID y clave
  estable correspondientes.
- GIVEN release repetido, respuesta incierta o `already released`
  WHEN se reconcilia
  THEN el resultado es éxito idempotente y no hay una segunda mutación lógica.
- GIVEN un fallo transitorio de release
  WHEN cabe en los presupuestos
  THEN se reintenta de forma acotada; un fallo permanente queda auditado como
  operacional sin fabricar un veredicto funcional.
- GIVEN cancelación del contexto principal
  WHEN se libera
  THEN cleanup usa únicamente su contexto acotado y al terminar no queda una
  goroutine huérfana.

### R10 — Operaciones provider estrictamente no destructivas

THE preview provider contract SHALL expose only discovery/reservation,
readiness/observation and release of a logical reservation for an existing
environment, and SHALL expose no operation to provision, deploy, mutate,
restart, scale or destroy infrastructure or source code.

#### Acceptance criteria

- GIVEN inspección de la interfaz propia y del fake provider
  WHEN se enumeran sus llamadas
  THEN solo existen reserve/discover, readiness/observe y release.
- GIVEN entorno ausente
  WHEN se descubre
  THEN se devuelve `unavailable`; no se crea uno.
- GIVEN entorno no saludable
  WHEN se observa
  THEN se devuelve la condición operativa; no se reinicia ni modifica.
- GIVEN finalización del run
  WHEN se libera
  THEN se cede la reserva lógica y el entorno sigue existiendo.

### R11 — Integración, pruebas herméticas y comentarios sistémicos

THE feature SHALL integrate the approved preview lifecycle with the existing
external-agent and GitHub validation gateways, preserve small consumer-owned
interfaces, and verify all behavior with deterministic fakes and no real
network or secrets; every introduced or modified entity, function and test
helper SHALL include a comment describing its responsibility in the system.

#### Acceptance criteria

- GIVEN composición válida
  WHEN un run llega por CLI/MCP o GitHub
  THEN ambos consumidores atraviesan la misma selección, reserva, binding,
  target policy, readiness, ejecución y release, sin duplicar reglas.
- GIVEN tests ordinarios
  WHEN se ejecutan
  THEN cubren conexión, readiness, revisión incorrecta, target denegado,
  timeout, cancelación, liberación, idempotencia y ausencia de secretos con un
  provider falso, reloj y waiter controlados.
- GIVEN R1–R10
  WHEN se revisa trazabilidad
  THEN cada requirement posee al menos un test nominal, los paquetes nuevos o
  materialmente modificados alcanzan al menos 85% de statement coverage y la
  verificación race pasa.
- GIVEN un tipo, constante, variable de paquete, interfaz, método, función,
  constructor o helper de test nuevo o modificado
  WHEN se revisa el código
  THEN contiene un comentario que explica su responsabilidad dentro del
  sistema y no se limita a repetir su sintaxis.

## Errores y casos límite

- Las identidades y referencias públicas son opacas, limitadas y no contienen
  paths, URLs, payloads ni material de autenticación.
- Los OID Git deben ser completos y compatibles con `revisionbinding`; no se
  aceptan branch names como revisión desplegada.
- La clave idempotente de reserva queda vinculada a run, selección, repository
  y commit esperados. Su reutilización con otros valores es conflicto.
- Una respuesta `ready` no basta por sí sola: debe conservar identidad,
  revisión, target y atestación vigentes.
- Una respuesta incierta al reservar se reconcilia por la misma clave antes de
  otro intento; no se reserva a ciegas por segunda vez.
- Los retries solo se aplican a operaciones clasificadas como transitorias y
  deben caber completos en intentos, espera acumulada y deadline.
- El adapter no registra bodies ni errores del provider. Proyecta categorías
  cerradas: `invalid_selection`, `unavailable`, `unhealthy`, `unverified`,
  `revision_mismatch`, `target_denied`, `conflict`, `timeout`, `cancelled`,
  `provider_unavailable`, `release_failed` e `integrity_failure`.
- Si release no se confirma al agotar su presupuesto, la reserva queda en
  estado seguro `release_pending`/operational para recuperación; nunca se
  declara destruido o libre sin confirmación.

## Decisiones abiertas

Ninguna para esta entrega. Un transporte o SDK específico de proveedor podrá
implementarse después detrás del contrato aprobado sin cambiar el core. Esta
feature no inventa un API externo ni exige un proveedor por defecto.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-17T18:03:28+02:00
```
