# Agent harness acotado — Requirements

## Objetivo y problema

FeatureProof necesita un primer ciclo agéntico local en el que un modelo pueda
proponer una operación, pero sea el sistema quien valide, autorice, limite,
ejecute y evalúe esa operación. El objetivo es demostrar que el harness puede
obtener una observación mediante una herramienta registrada y producir un
veredicto auditable sin confiar en una afirmación del modelo.

## Alcance

- Orquestar secuencialmente una ejecución iniciada con un objetivo, una
  restricción verificable y presupuestos explícitos.
- Consumir decisiones estructuradas de un modelo falso programable.
- Resolver exclusivamente herramientas registradas y validar sus inputs.
- Evaluar política y presupuesto antes de cada ejecución de herramienta.
- Ejecutar herramientas falsas mediante un contrato cancelable.
- Persistir resultados saneados como evidencias vinculadas al run y a la
  llamada de herramienta;
- registrar en un journal append-only los eventos necesarios para auditar
  decisiones rechazadas, pasos y terminación;
- calcular assertions y el veredicto fuera del modelo;
- terminar de manera explícita al completar, cancelar, agotar pasos o sufrir un
  error interno.

## Fuera de alcance

- Un proveedor o modelo real y sus evaluaciones.
- Acceso de red y herramientas HTTP, OpenAPI, Kubernetes, shell o GitHub.
- Una CLI o API pública, aprobación humana y ejecución distribuida.
- Concurrencia entre pasos o runs dentro del harness.
- Un lenguaje general de assertions, un formato definitivo de specification o
  generación de informes de presentación.
- Reintentos de operaciones con efectos que no ofrezcan una garantía de
  idempotencia.

## Actores y términos

- **Solicitante:** componente que inicia el run con objetivo, restricción,
  límites y dependencias configuradas.
- **Harness:** único propietario de las transiciones y del estado mutable del
  run.
- **Modelo falso:** adapter programable que devuelve decisiones estructuradas
  y recibe en la siguiente petición las observaciones acumuladas.
- **Tool registry:** catálogo explícito de herramientas y sus contratos de
  entrada.
- **Policy evaluator:** decide `allowed` o `denied` con una razón estable. La
  aprobación humana queda fuera de esta feature.
- **Presupuesto:** límites configurados de pasos agénticos y llamadas de
  herramienta disponibles para el run.
- **Evidencia:** registro inmutable y saneado de un resultado ejecutado,
  identificado y relacionado con el run y la llamada de origen.
- **Assertion:** condición determinista sobre una evidencia.
- **Veredicto:** resultado calculado por FeatureProof: `passed`, `failed`,
  `inconclusive` o `error`.
- **Paso agéntico:** una petición de decisión al modelo y el procesamiento de
  su respuesta.

## R1 — Interacción controlada con el modelo falso

WHEN a programmed fake model proposes a structurally valid call to a registered
tool, THE harness SHALL execute the authorized call and SHALL include its
persisted observation in the next model request.

### Acceptance criteria

- GIVEN un modelo falso programado para solicitar una herramienta registrada y
  después finalizar el análisis
  WHEN se ejecuta el run
  THEN la herramienta se ejecuta una vez y la segunda petición al modelo
  contiene el identificador y el resultado saneado de la evidencia producida.
- GIVEN una observación obtenida
  WHEN se construye la siguiente petición al modelo
  THEN el modelo recibe datos estructurados y no una capacidad para invocar la
  herramienta directamente.

## R2 — Validación estructural previa

WHEN the model proposes a tool call, THE harness SHALL validate the decision,
tool name and input against their declared structures before policy evaluation
or execution.

### Acceptance criteria

- GIVEN una decisión mal formada o un input que no cumple el contrato de la
  herramienta
  WHEN el harness procesa la decisión
  THEN no consulta la política, no consume una llamada de herramienta, no
  ejecuta la herramienta y registra un evento auditable con una clasificación
  estructural estable.

## R3 — Registro explícito y política segura por defecto

WHEN a structurally valid tool call is proposed, THE harness SHALL resolve it
from the registry and SHALL obtain an `allowed` policy decision before executing
it.

### Acceptance criteria

- GIVEN una llamada a una herramienta registrada y permitida
  WHEN el harness la procesa
  THEN la política se evalúa antes de la ejecución.
- GIVEN una llamada a una herramienta desconocida
  WHEN el harness intenta resolverla
  THEN no consulta una implementación externa, no ejecuta ninguna herramienta
  y registra un evento `tool_call_rejected` con `reason=unknown_tool`.
- GIVEN una llamada a una herramienta registrada que la política deniega
  WHEN el harness procesa la decisión
  THEN la herramienta no se ejecuta y se registra un evento
  `tool_call_rejected` con `reason=policy_denied` y la razón estable devuelta
  por la política.

## R4 — Presupuesto previo a la ejecución

WHEN a tool call has passed structural and policy checks, THE harness SHALL
verify and reserve the required tool-call budget before executing the tool.

### Acceptance criteria

- GIVEN presupuesto de llamada disponible
  WHEN una llamada supera estructura y política
  THEN el harness reserva una unidad antes de ejecutar y el presupuesto
  restante reflejado en el estado disminuye exactamente en una unidad.
- GIVEN presupuesto de llamadas agotado
  WHEN una llamada supera estructura y política
  THEN la herramienta no se ejecuta, se registra
  `tool_call_rejected` con `reason=budget_exhausted` y el run termina
  `inconclusive`.

## R5 — Evidencia persistida y relacionada

WHEN an authorized tool execution attempt returns a result, THE harness SHALL
sanitize and persist an immutable evidence record related to the run, attempt
and originating tool call before exposing the observation to the model or
evaluating it.

### Acceptance criteria

- GIVEN una herramienta que devuelve uno o varios resultados por sus intentos
  inicial y reintentados
  WHEN cada intento finaliza
  THEN existe una evidencia recuperable por intento con identificador estable,
  `run_id`, `tool_call_id`, número de intento, nombre de herramienta, input
  saneado, output saneado, instante inicial, duración y clasificación de error
  cuando corresponda.
- GIVEN que la persistencia de la evidencia falla
  WHEN el harness procesa el resultado
  THEN no entrega ese resultado al modelo, no lo usa para una assertion y el
  run termina con estado `error`.
- GIVEN una evidencia ya confirmada
  WHEN se consulta después
  THEN su contenido no ha sido reescrito por el harness.

## R6 — Veredicto determinista respaldado por evidencia

WHEN the run is evaluated, THE harness SHALL calculate assertion results and
the verdict deterministically from persisted evidence rather than from a model
claim.

### Acceptance criteria

- GIVEN evidencia persistida que satisface la assertion obligatoria
  WHEN el harness evalúa el run
  THEN el veredicto es `passed` y referencia el identificador de esa evidencia.
- GIVEN evidencia persistida que incumple la assertion obligatoria
  WHEN el harness evalúa el run
  THEN el veredicto es `failed` aunque el modelo afirme que pasó.
- GIVEN que el modelo solicita finalizar sin evidencia válida para la
  restricción
  WHEN el harness evalúa el run
  THEN el veredicto es `inconclusive`, nunca `passed`.
- GIVEN las mismas assertions y evidencias ordenadas de la misma forma
  WHEN se evalúan repetidamente
  THEN se obtiene el mismo veredicto y las mismas referencias de evidencia.

## R7 — Límite de pasos agénticos

WHILE a run has exhausted its configured agent-step budget, THE harness SHALL
NOT request another model decision and SHALL terminate the run as
`inconclusive`.

### Acceptance criteria

- GIVEN un límite de pasos `N`
  WHEN el modelo no produce una finalización evaluable durante esos `N` pasos
  THEN el modelo recibe exactamente `N` peticiones, no se ejecutan decisiones
  adicionales y el run termina `inconclusive` con razón `step_budget_exhausted`.
- GIVEN un límite de pasos agotado después de persistir evidencias parciales
  WHEN el run termina
  THEN las evidencias y eventos ya confirmados siguen siendo recuperables.

## R8 — Journal auditable y finalización única

WHEN the harness accepts, rejects, executes or terminates processing of a model
decision, THE harness SHALL append an ordered event carrying the applicable run,
step and tool-call identifiers and SHALL record exactly one terminal event.

### Acceptance criteria

- GIVEN un run con llamadas permitidas y rechazadas
  WHEN se lee su journal
  THEN se puede reconstruir el orden de decisiones, validaciones, política,
  presupuesto, ejecuciones, evidencias y terminación mediante eventos
  append-only.
- GIVEN cualquier flujo terminal
  WHEN el harness devuelve
  THEN existe exactamente un evento terminal y no se aceptan nuevas
  transiciones de ese run.

## R9 — Cancelación propagada

IF the run context is cancelled before or during a blocking model, store or tool
operation, THEN THE harness SHALL propagate cancellation, SHALL stop requesting
new work and SHALL terminate the run as `cancelled` without losing confirmed
events or evidence.

### Acceptance criteria

- GIVEN una herramienta falsa bloqueada que observa el contexto
  WHEN se cancela el contexto del run
  THEN la herramienta retorna por cancelación, no se solicita otro paso al
  modelo y el run termina `cancelled`.
- GIVEN eventos o evidencias confirmados antes de cancelar
  WHEN termina el run
  THEN permanecen recuperables y el journal termina con un único evento de
  cancelación.

## R10 — Retries acotados y seguros

IF a model or tool operation fails with a configured transient classification,
THEN THE harness SHALL retry it only within the configured retry budget and,
for a tool with effects, only when an idempotency guarantee is present.

### Acceptance criteria

- GIVEN una herramienta sin efectos que falla transitoriamente una vez y tiene
  un retry disponible
  WHEN el harness la ejecuta
  THEN registra el intento fallido, realiza un único retry y persiste una
  evidencia separada para cada uno de los dos intentos ejecutados.
- GIVEN una herramienta con efectos sin clave ni garantía de idempotencia
  WHEN falla transitoriamente
  THEN no se reintenta y el resultado técnico se conserva de forma auditable.
- GIVEN retries agotados
  WHEN vuelve a ocurrir el fallo transitorio
  THEN no hay más intentos y el run termina `inconclusive` o `error` según la
  clasificación configurada, sin producir `passed`.

## R11 — Tests herméticos

THE agent harness test suite SHALL run with fake model, tools, policy, clock and
stores under test control and SHALL require neither network access nor a real
model provider.

### Acceptance criteria

- GIVEN un entorno sin credenciales y con acceso de red no disponible
  WHEN se ejecutan los tests del harness
  THEN todos los escenarios de R1 a R10 pueden ejecutarse de forma determinista.
- GIVEN tests de tiempo, cancelación o retry
  WHEN se ejecutan
  THEN no usan esperas reales para coordinar el resultado.

## Errores y casos límite

- Una decisión o input inválido se rechaza antes de política y ejecución; solo
  puede volver a solicitarse otra decisión mientras queden pasos y esté
  configurado como recuperable.
- Una herramienta desconocida, una denegación de política y un presupuesto
  agotado nunca producen ejecución ni evidencia de ejecución.
- Un error de persistencia es un error interno de FeatureProof y evita utilizar
  como prueba el resultado no confirmado.
- Un resultado técnico de herramienta puede persistirse con su clasificación,
  pero no satisface por sí solo una assertion funcional.
- `failed` expresa incumplimiento comprobado de la restricción; `error` expresa
  fallo interno del sistema; `inconclusive` expresa evidencia insuficiente o un
  límite esperado.
- Una cancelación gana frente a iniciar trabajo nuevo. Los registros ya
  confirmados no se eliminan.
- Ningún resultado terminal admite pasos, evidencias o eventos posteriores,
  salvo la lectura de lo ya persistido.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. Permanecen aplazadas para
features posteriores el proveedor de modelo, el formato definitivo de
specification, el lenguaje completo de assertions, la aprobación humana, el
motor de políticas externo y el almacenamiento de producción. Los valores
concretos de presupuestos y retries son configuración explícita de cada test o
run; esta spec no define valores por defecto de producto.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-04T18:24:43+02:00
```
