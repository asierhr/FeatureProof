# Planes de validación de varios pasos — Requirements

## Objetivo y problema

FeatureProof necesita representar y ejecutar validaciones que requieren varias
operaciones relacionadas. El objetivo es aceptar un plan estructurado, validarlo
completamente antes de ejecutarlo y recorrer sus pasos en un orden reproducible,
de modo que cada operación pueda consumir de forma segura outputs ya obtenidos y
que cualquier paso omitido o bloqueado quede explicado en el journal.

## Alcance

- Definir un modelo interno de plan con uno o más pasos identificados.
- Relacionar cada paso con una restricción existente de la specification.
- Declarar herramienta, input estructurado, assertions y dependencias por paso.
- Representar referencias explícitas y tipadas a outputs de dependencias.
- Validar el grafo completo antes de ejecutar ninguna herramienta.
- Calcular un orden topológico determinista y ejecutar secuencialmente.
- Resolver referencias solo desde outputs persistidos de pasos anteriores.
- Mantener los controles existentes de registry, validación de input, policy,
  presupuesto, cancelación, retries, saneado y evidencia.
- Modelar los estados terminales de cada paso y propagar bloqueos a sus
  dependientes sin impedir que continúen ramas independientes.
- Emitir eventos suficientes para reconstruir orden, ejecución y causas.

### Contrato estructural observable

Un `ValidationPlan` contiene un ID, el `constraint_id` exacto de una restricción
existente y una secuencia no vacía de `PlanStep`. Todos los pasos del plan
pertenecen a esa restricción. Cada paso declara:

- `id`: identificador único dentro del plan;
- `tool`: nombre exacto de una herramienta registrada;
- `input`: valor estructurado cuyos nodos pueden ser literales o referencias;
- `assertions`: lista no vacía de declaraciones estructuradas con ID único;
- `depends_on`: lista de IDs de pasos, sin duplicados.

Una referencia a output contiene `step_id`, un path tipado y `value_type`. El
path es una secuencia no vacía de segmentos; cada segmento es exactamente un
campo string no vacío o un índice entero mayor o igual que cero. `value_type`
es exactamente uno de `string`, `number`, `boolean`, `object` o `array`. La
referencia fuente debe pertenecer al cierre transitivo de `depends_on` del paso
consumidor. Las referencias no crean dependencias implícitas.

Los literales del input son valores JSON compatibles: null, boolean, número,
string, lista o mapping con claves string. Esta feature solo exige que cada
assertion tenga ID y una definición mapping no vacía con claves string. No
interpreta operadores ni calcula assertions: esa
semántica pertenece a `assertion-engine`. Para ejecutar el plan, un puerto
determinista sustituible devuelve el resultado `passed`, `failed` o
`inconclusive` de las assertions del paso; los tests usan un fake local.

## Fuera de alcance

- Definir operadores, paths de assertions, grupos `all`/`any` o sus reglas de
  evaluación.
- Añadir el plan al contrato YAML v1 o cambiar su interpretación.
- Pedir a un modelo real que genere, repare o apruebe planes.
- Ejecutar pasos en paralelo, distribuirlos entre workers o reanudar un plan
  desde un proceso diferente.
- Implementar una herramienta HTTP, CLI, reporte final o almacenamiento remoto.
- Permitir referencias dinámicas, expresiones, plantillas de texto o
  referencias a datos que no sean outputs confirmados del mismo plan y run.

## Actores y términos

- **Plan producer:** componente que entrega un `ValidationPlan` estructurado;
  puede ser sustituido por un fake y no autoriza ejecuciones.
- **Plan validator:** valida entidades, referencias y grafo antes de ejecutar.
- **Plan executor:** propietario del estado mutable del plan y sus pasos.
- **Plan step:** unidad que realiza una llamada a herramienta y cuyas assertions
  producen un resultado determinista a través del puerto evaluador.
- **Dependency:** arista explícita desde un paso consumidor a un predecesor.
- **Output reference:** selector tipado que apunta al output persistido de una
  dependencia ya completada.
- **Ready step:** paso cuyas dependencias terminaron en `passed`.
- **Blocked step:** paso que no se ejecuta porque al menos una dependencia
  terminó en `failed`, `inconclusive` o `blocked`.
- **Declaration order:** posición del paso dentro de la secuencia original del
  plan; deshace empates entre pasos listos.

## R1 — Plan y pasos vinculados a restricciones

THE plan model SHALL reference one existing constraint by its exact identifier
and SHALL contain at least one uniquely identified step belonging to that
constraint.

### Acceptance criteria

- GIVEN un plan con varios IDs de paso únicos y una restricción existente
  WHEN se valida
  THEN todos sus pasos quedan relacionados con el `constraint_id` del plan.
- GIVEN un plan vacío, un ID de plan/paso vacío, un ID de paso duplicado o una restricción
  inexistente
  WHEN se valida
  THEN se rechaza el plan completo antes de ejecutar herramientas y el error
  identifica el field path del dato inválido.
- GIVEN IDs que solo difieren en mayúsculas
  WHEN se valida
  THEN se consideran distintos y no se normalizan.

## R2 — Declaración estructurada de cada paso

THE plan validator SHALL require every step to declare a registered tool, a
structured input, one or more structured assertions and an explicit dependency
list.

### Acceptance criteria

- GIVEN un paso completo
  WHEN se valida
  THEN su herramienta se resuelve por nombre exacto y el árbol de input solo
  contiene literales estructurados o referencias con la forma permitida.
- GIVEN una herramienta desconocida, input estructuralmente inválido, lista de
  assertions vacía, assertion sin ID/definición o IDs de assertion duplicados
  WHEN se valida el plan
  THEN ninguna herramienta del plan se ejecuta.
- GIVEN `depends_on` vacío
  WHEN el resto del paso es válido
  THEN el paso puede ser raíz del grafo.

## R3 — Referencias tipadas a outputs anteriores

WHEN a step input contains an output reference, THE plan SHALL identify an
upstream dependency, a typed non-empty path and the expected value type, and
THE executor SHALL resolve that reference only from the dependency's persisted
output before validating or executing the consumer call.

### Acceptance criteria

- GIVEN una referencia a un campo o índice existente de una dependencia
  `passed` cuyo valor coincide con `value_type`
  WHEN el consumidor pasa a estar listo
  THEN el valor resuelto se inserta en una copia del input y el input original
  del plan permanece inmutable.
- GIVEN una referencia a un paso desconocido, al propio paso, a un paso que no
  pertenece a su cierre de dependencias, un path vacío/mal formado, un path que
  no existe o un tipo observado incompatible
  WHEN se valida o resuelve
  THEN el consumidor no ejecuta su herramienta y se devuelve un error
  distinguible con el field path y la causa estable.
- GIVEN una referencia válida
  WHEN se resuelve
  THEN se recupera el output mediante evidence IDs confirmados del mismo run y
  paso fuente; un valor aportado directamente por el productor no sustituye a
  la evidencia.

## R4 — Validación completa del grafo

WHEN a plan is submitted, THE plan validator SHALL reject duplicate dependency
IDs, unknown dependencies, self-dependencies, invalid output references and
dependency cycles before any plan step is executed.

### Acceptance criteria

- GIVEN cualquier dependencia inexistente, duplicada o igual al ID del paso
  WHEN se valida
  THEN el error identifica `steps[n].depends_on[m]` y no se llama a policy ni a
  herramientas.
- GIVEN un ciclo directo o transitivo, aunque exista una rama independiente
  válida
  WHEN se valida
  THEN se rechaza todo el plan y el error identifica de forma estable el primer
  paso del ciclo según declaration order.
- GIVEN el mismo plan inválido validado repetidamente
  WHEN se comparan categoría y field path
  THEN se obtiene el mismo primer error.

## R5 — Orden topológico determinista y controles de ejecución

WHEN a valid plan is executed, THE plan executor SHALL run ready steps
sequentially in topological order, breaking ties by declaration order, and SHALL
apply all existing tool controls before each execution.

### Acceptance criteria

- GIVEN varias raíces y ramas válidas
  WHEN se ejecuta el plan repetidamente
  THEN la secuencia de pasos es idéntica, respeta dependencias y usa declaration
  order entre candidatos listos.
- GIVEN un paso listo
  WHEN se ejecuta
  THEN el orden es resolución de la herramienta registrada, resolución de
  referencias, validación del input resuelto, policy, presupuesto, herramienta,
  saneado y persistencia de evidencia antes de evaluar sus assertions.
- GIVEN una herramienta desconocida, policy denegada o presupuesto agotado que
  aparezca durante ejecución pese a haber validado el plan
  WHEN se alcanza el paso
  THEN la herramienta no se ejecuta y el paso termina `inconclusive` con causa
  auditable.

## R6 — Estados y resultado determinista del plan

WHEN a valid plan reaches functional completion, THE plan executor SHALL assign
every step exactly one terminal status from `passed`, `failed`, `inconclusive`
or `blocked`, and SHALL derive the plan result without using a model claim.

### Acceptance criteria

- GIVEN assertions evaluadas como satisfechas, incumplidas o sin evidencia
  suficiente
  WHEN termina el paso
  THEN su estado es respectivamente `passed`, `failed` o `inconclusive` y
  conserva sus evidence IDs.
- GIVEN todos los pasos `passed`
  WHEN se agrega el resultado
  THEN el plan es `passed`.
- GIVEN al menos un paso `failed`
  WHEN se agrega el resultado
  THEN el plan es `failed`; si no hay fallos pero existe un paso
  `inconclusive` o `blocked`, el plan es `inconclusive`.
- GIVEN cualquier afirmación externa sobre el resultado
  WHEN se calcula el estado
  THEN no altera los estados derivados de evidencia y del evaluador
  determinista.

## R7 — Propagación determinista de bloqueos

IF any dependency finishes as `failed`, `inconclusive` or `blocked`, THEN THE
plan executor SHALL NOT execute the dependent step and SHALL mark it `blocked`
with every direct blocking dependency and its terminal status.

### Acceptance criteria

- GIVEN un paso `failed` o `inconclusive`
  WHEN se procesa un dependiente directo o transitivo
  THEN no se llama a su policy ni herramienta y queda `blocked`.
- GIVEN un paso con varias dependencias no satisfactorias
  WHEN queda bloqueado
  THEN registra todas las causas directas en declaration order, sin escoger una
  arbitrariamente.
- GIVEN una rama bloqueada y otra independiente lista
  WHEN continúa el plan
  THEN la rama independiente sigue ejecutándose en el orden determinista.

## R8 — Journal reconstruible

WHEN a valid plan is executed, THE plan executor SHALL append ordered events
that allow a reader to reconstruct plan validation, step selection, execution
or omission, evidence linkage, terminal status and blocking causes.

### Acceptance criteria

- GIVEN una combinación de pasos `passed`, `failed`, `inconclusive` y
  `blocked`
  WHEN se leen los eventos por secuencia
  THEN se reconstruyen el orden elegido, qué herramientas llegaron a ejecutarse,
  los evidence IDs, el estado de cada paso y las causas de cada bloqueo.
- GIVEN un paso bloqueado
  WHEN se inspecciona el journal
  THEN existe un evento propio del paso con IDs y estados de sus dependencias
  bloqueantes, y no existe evento de inicio de herramienta para ese paso.
- GIVEN un evento confirmado
  WHEN avanza la ejecución
  THEN no se reescribe ni reordena.

## R9 — Cancelación, retries y pruebas herméticas

WHILE plan execution is active, THE executor SHALL propagate cancellation and
bounded retry rules through existing ports, and the feature SHALL be verifiable
without network access or a real model.

### Acceptance criteria

- GIVEN cancelación antes de empezar o durante policy, herramienta, espera,
  evidencia o evaluación
  WHEN se observa el contexto
  THEN no comienza otro paso, se conserva lo ya confirmado y el resultado
  distingue la cancelación de un fallo funcional.
- GIVEN un error transitorio
  WHEN la herramienta es segura para retry y quedan presupuestos
  THEN se aplica el contrato existente de retry e idempotencia; una assertion
  incumplida no se reintenta.
- GIVEN herramientas, evaluador, store y journal falsos locales
  WHEN se ejecuta la suite
  THEN cubre el flujo completo sin red, servicios externos ni modelo real.

## Errores y casos límite

- El plan se valida como unidad: no se ejecuta su prefijo válido si otra parte
  contiene una referencia o dependencia inválida.
- Los slices vacíos y `nil` son equivalentes únicamente para `depends_on`; el
  plan, las assertions y la definición de cada assertion no pueden quedar
  vacíos.
- Los IDs se comparan exactamente y no se recortan ni normalizan.
- Un plan pertenece a una sola restricción; coordinar resultados de varias
  restricciones corresponde a una capa de ejecución o reporte posterior.
- Una referencia no autoriza a leer evidencia de otro run o de otro paso.
- Un path que atraviesa un escalar, usa índice fuera de rango o encuentra un
  tipo diferente al declarado hace el paso `inconclusive`; no produce un valor
  cero ni ejecuta con input parcial.
- Una salida con error técnico persiste evidencia del intento, pero no puede
  producir `passed`.
- Un fallo del propio journal o evidence store es un error de FeatureProof y
  detiene el plan; no se presenta como incumplimiento funcional.
- La cancelación no cambia retrospectivamente estados ni eventos confirmados.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. El formato externo del
plan, su posible inclusión en una versión futura del YAML, el lenguaje completo
de assertions, la planificación mediante modelo y la ejecución paralela quedan
aplazados a sus features correspondientes.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-11T17:46:51+02:00
```
