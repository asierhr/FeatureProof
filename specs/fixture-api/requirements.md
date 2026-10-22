# Preparación y limpieza de fixtures mediante API — Requirements

## Objetivo y problema

FeatureProof necesita preparar datos de prueba mediante la misma aplicación
desplegada que valida y retirar esos datos al terminar, sin conceder al agente
acceso a shells, bases de datos, SDKs de infraestructura ni destinos de red
adicionales. Esta feature añade fixtures HTTP declarativos: cada fixture ejecuta
una operación de creación autorizada, captura un conjunto pequeño de valores
tipados y ejecuta una operación de limpieza incluso cuando el run no termina de
forma satisfactoria.

La preparación y la limpieza son trabajo operativo de FeatureProof. Sus fallos
pueden impedir o degradar una validación, pero nunca constituyen por sí mismos
evidencia de que la funcionalidad validada cumple o incumple sus restricciones.

## Alcance

- Evolucionar la specification con una versión nueva que declare fixtures sin
  reinterpretar los contratos v1 y v2.
- Declarar por fixture una operación HTTP de `create`, una de `cleanup`,
  dependencias explícitas, identidades explícitas y capturas tipadas.
- Vincular ambas operaciones al target de la specification y a operaciones del
  catálogo de contrato aprobado para la revisión del run.
- Validar todo el grafo, las referencias, permisos, identidades e idempotencia
  antes de iniciar la preparación.
- Preparar fixtures secuencialmente en orden topológico estable y hacer
  disponibles únicamente sus capturas confirmadas a fixtures posteriores.
- Registrar intención de limpieza antes de cruzar la frontera de red de una
  creación y limpiar en el orden inverso de preparación.
- Intentar la limpieza tras éxito, fallo funcional, resultado inconclusive,
  cancelación o error operativo del run.
- Ejecutar cleanup con un contexto propio, finito y auditable y con retries
  acotados e idempotentes.
- Mantener separado el resultado principal del run y el resultado operativo de
  cleanup.
- Integrar el lifecycle compartido en los flujos productivos existentes sin
  añadir autoridad a CLI, API, MCP, GitHub o al modelo.

### Contrato YAML v3 observable

La v3 conserva feature, target, constraints, limits e identities de v2 y añade
una lista `fixtures`. La forma normativa es:

```yaml
version: 3
feature:
  id: account-summary
  description: El resumen muestra la cuenta creada
target:
  id: deployed
  base_url: https://preview.example.test
constraints:
  - id: summary-visible
    description: La cuenta preparada aparece en el resumen
    target_id: deployed
limits:
  max_steps: 8
  max_tool_calls: 8
  max_retries: 1
identities:
  - id: fixture-writer
    target_id: deployed
    credential_ref: fixture_writer
    mechanism:
      type: bearer
    operations:
      - capability: http.request
        method: POST
        path: /api/accounts
      - capability: http.request
        method: DELETE
        path: /api/accounts/{account_id}
fixtures:
  - id: account
    depends_on: []
    create:
      operation:
        method: POST
        path: /api/accounts
      identity_id: fixture-writer
      input:
        body:
          literal:
            name: FeatureProof
      accepted_statuses: [201]
      idempotency: contract
    capture:
      - name: account_id
        path: [body, id]
        value_type: string
        max_bytes: 128
    cleanup:
      operation:
        method: DELETE
        path: /api/accounts/{account_id}
      identity_id: fixture-writer
      input:
        path_parameters:
          account_id:
            reference:
              fixture_id: account
              output: account_id
              value_type: string
      accepted_statuses: [204, 404]
      idempotency: contract
```

`fixtures` puede estar vacío. Cuando contiene elementos, cada fixture exige ID,
`depends_on`, `create`, `capture` y `cleanup`. Los IDs y referencias son
case-sensitive. Una operación solo expresa método, path template aprobado,
input HTTP estructurado, statuses enteros únicos de 100 a 599, identity ID e
idempotencia. No acepta base URL, host, esquema, puerto, credential reference,
credencial, shell, consulta de base de datos ni nombre de herramienta.

El input usa una unión estricta entre `literal` y `reference`. Una referencia
contiene exactamente `fixture_id`, `output` y `value_type`; solo puede apuntar a
una captura declarada. Durante create solo puede referenciar una dependencia
transitiva; durante cleanup puede referenciar el propio fixture o una de sus
dependencias transitivas. Los literales son valores JSON finitos. El path de una
captura es una secuencia no vacía cuyos segmentos son exactamente un nombre de
campo string no vacío o un índice entero mayor o igual que cero. Los límites
globales y por captura son obligatorios y positivos en configuración confiable;
no existen capturas sin límite.

`idempotency: contract` significa que la operación aprobada aporta una garantía
de idempotencia verificable: semántica idempotente declarada o clave estable
inyectada por el sistema en una ubicación permitida por el contrato. La
specification no aporta la clave ni el nombre de un header libre. Una operación
de fixture sin esa garantía se rechaza antes de ejecutar.

## Fuera de alcance

- Generar fixtures o sus operaciones mediante el modelo.
- Permitir URLs absolutas, targets adicionales, redirects más amplios, headers
  de autenticación libres o capacidades distintas de `http.request`.
- Ejecutar SQL, shells, scripts, SDKs cloud, Kubernetes, filesystem remoto o
  llamadas directas a bases de datos.
- Definir flujos funcionales completos con varios actores, pasos de negocio,
  assertions y compensaciones; ese alcance pertenece a la feature 27.
- Convertir create/cleanup en pasos de un plan agéntico o permitir que sus
  respuestas determinen assertions funcionales.
- Capturar bodies completos, headers sensibles, cookies, credenciales, valores
  sin tipo o estructuras sin límites explícitos.
- Inventar una operación cleanup, seleccionar automáticamente una identidad o
  cambiar el contrato aprobado cuando falta información.
- Proporcionar una garantía transaccional distribuida entre la aplicación y
  FeatureProof. Los estados inciertos se conservan como incidencias operativas.
- Crear una cola o controlador distribuido independiente de cleanup; esta
  entrega usa el ownership y recovery del workflow local existente.

## Actores y términos

- **Fixture declaration:** definición confiable y versionada de un recurso de
  prueba, sus dependencias, creación, capturas y limpieza.
- **Fixture lifecycle:** coordinador determinista que prepara y limpia fixtures
  alrededor de la ejecución principal.
- **Approved operation:** método y path template presentes en el catálogo de
  contrato aprobado y vinculados al target/revisión del run.
- **Capture:** valor JSON tipado y acotado extraído de una respuesta HTTP
  confirmada; es el único output referenciable de un fixture.
- **Cleanup intent:** registro durable creado antes de una operación `create`
  que puede producir efectos y que permite saber que cleanup debe evaluarse.
- **Prepared fixture:** fixture cuya respuesta de create coincide con un status
  aceptado y cuyas capturas obligatorias se confirmaron.
- **Possibly created fixture:** fixture cuya llamada create cruzó la frontera de
  red pero cuyo resultado no pudo confirmarse; también conserva intención de
  cleanup.
- **Primary result:** resultado funcional u operativo que el workflow habría
  producido sin considerar incidencias de cleanup.
- **Cleanup incident:** resultado operativo seguro de un intento de limpieza;
  no es una assertion ni modifica retroactivamente el primary result.

## R1 — Contrato v3 estricto y compatible

WHERE fixtures are required, THE specification SHALL use a strict version 3
contract that declares fixtures, operations, dependencies, captures and
cleanup, while versions 1 and 2 SHALL retain their existing interpretation.

### Acceptance criteria

- GIVEN una specification v3 válida WHEN se carga THEN el modelo interno
  conserva una copia tipada e inmutable de cada fixture y de sus campos.
- GIVEN IDs duplicados, miembros desconocidos, uniones con cero o varios
  miembros, status fuera de 100..599, límites no positivos u operaciones
  incompletas WHEN se carga THEN falla con categoría y field path estables.
- GIVEN una specification v1 o v2 válida WHEN se carga con el nuevo código THEN
  mantiene exactamente su aceptación y semántica anteriores y no obtiene
  fixtures por defecto.
- GIVEN el campo `fixtures` en v1/v2 o datos de v3 bajo otra versión WHEN se
  carga THEN se rechaza; no se migra ni reinterpreta silenciosamente.

## R2 — Operaciones limitadas a target y contrato conocidos

WHEN a fixture declaration is validated, THE system SHALL bind both create and
cleanup to `http.request`, the specification target and exact operations in the
approved contract catalog before any fixture effect occurs.

### Acceptance criteria

- GIVEN método y path template presentes en el catálogo aprobado para el target
  y revisión WHEN se compone el fixture THEN se producen bindings canónicos de
  create y cleanup ligados al digest del catálogo.
- GIVEN operación ausente, ambigua, derivada solamente de un catálogo no
  aprobado, otro target, otro método o path por prefijo textual WHEN se compone
  THEN todo el fixture set se rechaza antes de resolver identidades o usar red.
- GIVEN un input con URL absoluta, esquema, host, puerto, nombre de herramienta,
  comando, SQL, SDK o capacidad externa WHEN se decodifica THEN se rechaza por
  schema estricto.
- GIVEN redirects WHEN el HTTP tool los evalúa THEN conserva sus reglas actuales
  de target, path, método, identidad y límites; un fixture no las amplía.

## R3 — Grafo de dependencias válido y orden estable

WHEN a fixture set is admitted, THE fixture lifecycle SHALL validate the whole
dependency graph before execution and SHALL derive a stable topological create
order and its exact reverse cleanup order.

### Acceptance criteria

- GIVEN dependencias válidas WHEN se ordenan repetidamente THEN create respeta
  precedencias y deshace empates por orden de declaración; cleanup usa el orden
  inverso de los fixtures que requieren limpieza.
- GIVEN dependencia desconocida, duplicada, propia o un ciclo directo o
  transitivo WHEN se valida THEN no se ejecuta ninguna operación y el primer
  error es estable según orden de declaración.
- GIVEN una referencia a otro fixture WHEN se valida THEN su `fixture_id` debe
  pertenecer al cierre transitivo de `depends_on`; una referencia no crea una
  dependencia implícita.
- GIVEN una referencia al propio fixture WHEN aparece en create THEN se rechaza;
  WHEN aparece en cleanup y selecciona una captura declarada THEN es válida.
- GIVEN mutación posterior de slices/maps de entrada WHEN se usa el set validado
  THEN orden, bindings y referencias no cambian.

## R4 — Preparación secuencial y bloqueo de dependientes

WHEN fixture preparation begins, THE lifecycle SHALL execute creates
sequentially in stable topological order and SHALL prevent every dependent
fixture and validation scenario from running unless all required fixtures are
prepared.

### Acceptance criteria

- GIVEN un fixture raíz preparado WHEN el siguiente fixture dependiente se
  ejecuta THEN sus referencias se resuelven solo desde capturas confirmadas y
  pasan de nuevo por validación de input, policy, budget e identidad.
- GIVEN fallo pre-I/O, status no aceptado, captura inválida o error operativo en
  create WHEN se alcanza un dependiente THEN el dependiente no ejecuta create y
  queda `blocked` con causas seguras y ordenadas.
- GIVEN un fallo de preparación WHEN comienza la fase principal THEN ningún plan
  que dependa del fixture se ejecuta y el resultado es operacional/inconclusive,
  nunca un `failed` funcional fabricado.
- GIVEN una rama independiente ya admitida WHEN otra rama falla THEN no se
  inicia trabajo nuevo después del fallo de preparación; únicamente continúa
  la limpieza aplicable en orden inverso.

## R5 — Capturas tipadas, acotadas y confirmadas

WHEN a create response has an accepted status, THE lifecycle SHALL extract only
declared typed captures within configured limits and SHALL expose them by exact
fixture and output identifiers after durable confirmation.

### Acceptance criteria

- GIVEN un path declarado cuyo valor existe, coincide con `value_type` y cabe en
  `max_bytes` y límites globales WHEN se confirma la respuesta THEN la captura
  puede ser referenciada por fixtures posteriores y por el cleanup autorizado.
- GIVEN path ausente, tipo incompatible, valor truncado, tamaño excesivo,
  captura duplicada o respuesta sin status aceptado WHEN se procesa create THEN
  el fixture no queda preparado y el dato no se publica como captura válida.
- GIVEN una referencia WHEN se resuelve THEN se usa la captura confirmada del
  mismo run y fixture; valores aportados por el modelo, caller o un run anterior
  no la sustituyen.
- GIVEN una captura sensible, un header/cookie de autenticación o un selector
  no permitido por la policy de redacción WHEN se valida o extrae THEN falla
  cerrado sin persistir ni proyectar el valor crudo.

## R6 — Identidad explícita por operación sin secretos

WHEN create or cleanup is declared, THE operation SHALL explicitly select one
test identity authorized for its target, method and path, and credential
material SHALL remain confined to the authenticated transport.

### Acceptance criteria

- GIVEN identidades diferentes para create y cleanup WHEN ambas están
  autorizadas THEN cada operación usa únicamente su identity binding explícito.
- GIVEN identity ID ausente, desconocido, no permitido o perteneciente a otra
  operación WHEN se valida THEN se rechaza antes de credential resolution, DNS
  o socket; no se elige la única identidad disponible por defecto.
- GIVEN retries o recovery WHEN comienza cada intento THEN se revalida el mismo
  binding y se resuelve material nuevo mediante composición, sin persistir
  credential reference, lease ni secreto.
- GIVEN canarios de credencial WHEN se recorren model context, plan, state,
  journal, evidence, captures, reports y superficies externas THEN no aparece
  ninguno.

## R7 — Intención durable y elegibilidad conservadora de cleanup

BEFORE a create operation may cross the network boundary, THE lifecycle SHALL
durably record a cleanup intent, and IF creation is confirmed or becomes
uncertain after I/O, THEN THE fixture SHALL remain eligible for cleanup until a
terminal cleanup result is durably recorded.

### Acceptance criteria

- GIVEN rechazo definitivo antes de red WHEN termina create THEN no se fabrica
  un recurso creado ni se ejecuta cleanup innecesario.
- GIVEN respuesta confirmada, timeout/read error tras enviar la request o caída
  en un checkpoint posterior al I/O WHEN se recupera THEN la intención obliga a
  evaluar cleanup y no se declara limpieza confirmada por ausencia de respuesta.
- GIVEN crash/restart con intención pendiente WHEN otro owner recupera el run
  THEN reconstruye bindings y capturas confirmadas desde estado durable y
  continúa cleanup sin repetir un create confirmado.
- GIVEN un intent, captura o evento manipulado, fuera del run o con digest
  distinto WHEN se recupera THEN falla cerrado y no llama a un destino.

## R8 — Cleanup siempre intentado en orden inverso

WHEN primary execution finishes, fails, becomes inconclusive, is cancelled or
encounters an operational error, THE lifecycle SHALL attempt cleanup for every
eligible fixture in exact reverse create order and SHALL continue with older
fixtures after an individual cleanup failure.

### Acceptance criteria

- GIVEN varios fixtures preparados WHEN termina el run por cualquier estado
  terminal THEN sus cleanups se intentan en orden inverso y cada intento obtiene
  un resultado terminal auditable.
- GIVEN un fallo en el cleanup de un fixture WHEN quedan otros elegibles THEN se
  registra la incidencia y se siguen intentando los restantes dentro del
  presupuesto de cleanup.
- GIVEN un create intermedio fallido o incierto WHEN comienza cleanup THEN se
  incluye todo fixture elegible hasta ese punto, no se ejecutan fixtures que
  nunca alcanzaron I/O y se preserva el orden inverso.
- GIVEN que una referencia necesaria para cleanup no fue capturada WHEN se
  evalúa esa limpieza THEN se registra `cleanup_input_unavailable`, no se inventa
  el valor y se continúa con los demás fixtures.

## R9 — Contexto y presupuesto propios de cleanup

WHEN cleanup starts after the run context is cancelled or exhausted, THE
lifecycle SHALL use an explicitly supplied cleanup scope with a finite deadline,
bounded calls and bounded retries, without inheriting additional product
authority.

### Acceptance criteria

- GIVEN cancelación del contexto principal WHEN comienza cleanup THEN el intento
  usa el scope de cleanup provisto por composición y puede progresar hasta su
  deadline finita; no usa `context.Background()` ni ignora su propia cancelación.
- GIVEN deadline, call budget o retry budget de cleanup agotado WHEN quedan
  operaciones THEN no se inicia otra llamada, cada pendiente obtiene una
  incidencia segura y la función termina sin trabajo en background.
- GIVEN cancelación del scope de cleanup durante policy, credential resolution,
  HTTP, redacción o persistencia WHEN se observa THEN se detiene ese intento,
  registra el prefijo confirmado y continúa solo si el scope y budgets lo
  permiten.
- GIVEN el primary run con presupuesto agotado WHEN se limpia THEN el presupuesto
  reservado de cleanup no rehabilita pasos funcionales ni llamadas del agente.

## R10 — Idempotencia y retries acotados

WHEN a fixture operation may be retried, redelivered or recovered, THE lifecycle
SHALL require a verified idempotency guarantee, use a stable system-owned
operation key and retry only transient failures within explicit budgets.

### Acceptance criteria

- GIVEN una operación sin semántica idempotente aprobada ni ubicación contractual
  para clave estable WHEN se admite el fixture THEN se rechaza antes de efectos.
- GIVEN reintento o redelivery de la misma fase/run/fixture WHEN se construye la
  request THEN usa la misma clave opaca del sistema y no acepta una clave del
  modelo, specification input o superficie externa.
- GIVEN error transitorio y budgets disponibles WHEN se reintenta THEN cada
  intento revalida contrato, target, policy, identidad y contexto y queda
  registrado por separado con backoff controlado.
- GIVEN status HTTP no aceptado, captura inválida, policy denial, identidad
  permanente o error permanente WHEN se clasifica THEN no se reintenta para
  obtener un resultado más favorable.

## R11 — Resultado principal separado de incidencias de cleanup

WHEN cleanup produces success, partial completion, failure or timeout, THE
workflow SHALL preserve the previously determined primary result and SHALL
publish cleanup as a separate operational report.

### Acceptance criteria

- GIVEN primary result `passed`, `failed`, `inconclusive`, `error` o `cancelled`
  WHEN cualquier cleanup falla THEN el primary result conserva exactamente su
  valor y evidencia originales.
- GIVEN cleanup parcial WHEN se consulta el resultado THEN se muestran estado
  global de cleanup e incidencias por fixture con fase, intento y categoría
  segura, sin presentarlas como assertions funcionales.
- GIVEN cleanup completo WHEN se publica el paquete THEN contiene la relación
  entre intents, intentos y terminales sin copiar bodies, capturas sensibles ni
  credenciales.
- GIVEN fallo al persistir el reporte de cleanup WHEN se finaliza THEN no se
  publica un falso `cleanup_succeeded`; la incidencia queda recuperable y el
  primary result confirmado no se reescribe.

## R12 — Auditoría, redacción y superficies compartidas

WHEN fixture lifecycle work is requested through CLI, API, MCP or GitHub, THE
adapters SHALL use the same trusted workflow and SHALL expose only bounded safe
status while journal and evidence retain an ordered, redactable audit trail.

### Acceptance criteria

- GIVEN el mismo snapshot autorizado desde cualquiera de las cuatro superficies
  WHEN se ejecuta THEN bindings, orden, primary result y cleanup report son
  equivalentes.
- GIVEN payload externo con fixture inline, URL, command, SQL, credential,
  credential reference, idempotency key o autoridad autoafirmada WHEN se
  decodifica THEN se rechaza antes de admission; las superficies solo eligen una
  specification/configuración ya autorizada.
- GIVEN eventos de lifecycle WHEN se inspeccionan THEN permiten reconstruir
  validación, intent, create, capturas confirmadas, cleanup y retries por IDs y
  digests, sin payloads ni secretos.
- GIVEN canarios en inputs, outputs, errores HTTP, capturas rechazadas o
  credenciales WHEN se recorren logs, journal, evidence, state, artifacts,
  model requests, reports y respuestas externas THEN no aparece ninguno fuera
  de los valores explícitamente aprobados y saneados.

## R13 — Cancelación, concurrencia y cierre

WHILE fixture lifecycle work is active, THE system SHALL preserve single-owner
state transitions, observe cancellation at every blocking boundary and finish
without duplicate effects, races or orphan background work.

### Acceptance criteria

- GIVEN cancelación antes/durante intent, create, capture, primary execution o
  cleanup WHEN el workflow converge THEN no inicia trabajo funcional posterior,
  conserva el prefijo durable y ejecuta o deja recuperable el cleanup aplicable.
- GIVEN una carrera entre cancelación y adquisición de ownership o permiso para
  I/O WHEN se obtiene ownership THEN se revalida contexto inmediatamente antes
  del siguiente efecto.
- GIVEN dos owners o redeliveries para el mismo run WHEN compiten THEN CAS/lease
  permite un solo efecto por intento lógico y el otro recupera estado confirmado.
- GIVEN tests repetidos con barreras y race detector WHEN terminan THEN no hay
  data races, deadlocks, goroutines huérfanas ni coordinación mediante sleeps.

## Errores y casos límite

- `fixtures: []` es válido en v3 y conserva el flujo sin lifecycle; omitir un
  campo obligatorio dentro de un fixture no activa defaults.
- `depends_on` vacío permite un fixture raíz. Ningún fixture puede depender de
  un plan de validación ni de un cleanup.
- Los nombres de captura son únicos dentro de su fixture. Una captura puede ser
  escalar, objeto o array JSON, pero siempre tiene tipo y bytes máximos; `null`
  no satisface ninguno de los tipos capturables iniciales.
- Una respuesta create puede haber producido efecto aunque su status/body no se
  confirme. Se conserva la incertidumbre y se intenta cleanup solo con inputs
  confirmados; nunca se inventan IDs.
- Un status cleanup como `404` solo cuenta como éxito cuando está incluido de
  forma explícita en `accepted_statuses`.
- Un cleanup que obtiene respuesta HTTP real con status no aceptado produce una
  incidencia permanente; no es un fallo funcional del producto.
- El lifecycle no entrega capturas al modelo. Solo el resolver determinista de
  inputs de fixture y, posteriormente, inputs aprobados que las referencien
  pueden consumirlas.
- La idempotency key no contiene secretos y no se persiste en vistas públicas;
  su commitment/digest puede auditarse.
- La policy puede denegar cualquier operación aunque specification, contrato e
  identidad coincidan. Ninguna fase de fixture elude registry, policy, budget,
  redacción o evidencia.

## Decisiones abiertas

No quedan decisiones abiertas que bloqueen esta entrega. Los límites numéricos,
deadline, retries, backoff y ubicación contractual de idempotency se reciben de
configuración confiable y son obligatorios; la spec no inventa defaults. La
feature 27 podrá usar fixtures preparados como precondición de escenarios, pero
no cambia este lifecycle ni se implementa aquí.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-22T18:29:01+02:00
```

