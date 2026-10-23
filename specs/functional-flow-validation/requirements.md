# Validación de flujos funcionales reales — Requirements

## Objetivo y problema

FeatureProof ya puede preparar fixtures, ejecutar planes con herramientas
controladas, evaluar assertions deterministas y limpiar recursos. Falta una
unidad de ejecución que combine esas capacidades en un flujo funcional completo:
observar un estado, ejecutar acciones con actores explícitos, volver a observar,
comparar evidencias y producir un único resultado reconstruible.

Esta feature introduce un flujo canónico independiente de su procedencia. Un
flujo proporcionado por una persona o propuesto por el agente obtiene la misma
validación, autorización, ejecución y evaluación. El modelo puede proponer trabajo,
pero no puede autorizar operaciones, aportar valores capturados, ejecutar
herramientas ni cambiar un resultado determinista.

## Alcance

- Definir un `FunctionalFlow/v1` canónico para flujos proporcionados y
  generados, con pasos `observe` y `act`, dependencias y evidencia requerida.
- Referenciar restricciones, operaciones del contrato aprobado, identidades de
  prueba y el target ya vinculados al run.
- Permitir inputs literales y referencias tipadas a capturas confirmadas de
  fixtures o a evidencia terminal de pasos antecesores.
- Validar el documento, el grafo, bindings, referencias, assertions, policy y
  límites antes de iniciar la preparación o una operación de negocio.
- Ejecutar secuencialmente en orden topológico estable, aplicando controles y
  presupuestos a cada paso.
- Observar el mismo recurso antes y después de una acción y evaluar tanto
  assertions locales como comparaciones deterministas entre observaciones.
- Propagar bloqueos sin convertir fallos operativos en incumplimientos
  funcionales ni impedir ramas independientes ya admitidas.
- Integrar replanificación acotada como revisiones adicionales del flujo sin
  reescribir resultados confirmados ni repetir fixtures.
- Persistir intents, eventos, evidencias y artefactos suficientes para recovery,
  redelivery, informe y paquete reconstruibles.
- Intentar siempre el cleanup de fixtures mediante el lifecycle de feature 26,
  conservando separado su resultado del veredicto funcional.
- Exponer la misma autoridad y proyección segura mediante CLI, API, MCP y
  GitHub.

### Contrato funcional observable

El contrato interno canónico es `FunctionalFlow/v1`. Puede proceder de un
manifest proporcionado ya autorizado o de un `PreparedPlan` generado, pero el
origen no cambia sus controles. Contiene ID, source, revisión, digests del
contexto aprobado y una lista no vacía de pasos. Cada paso declara:

- `id` único y case-sensitive;
- `kind`: exactamente `observe` o `act`;
- `constraint_id` existente;
- `depends_on` explícito;
- una operación HTTP exacta del catálogo aprobado;
- un `identity_id` explícito que identifica una identidad declarada y
  autorizada para esa operación;
- un input estructurado con literales o referencias tipadas;
- una lista no vacía de assertions deterministas;
- los IDs exactos de assertions y evidencias requeridas para completar la
  restricción.

Una referencia es una unión cerrada:

- `fixture`: `fixture_id`, `output` y `value_type`; o
- `step`: `step_id`, path tipado no vacío y `value_type`.

Las referencias a fixture no crean dependencias y solo apuntan a captures
declaradas y confirmadas del mismo run. Las referencias a pasos solo apuntan al
cierre transitivo de `depends_on` y se resuelven desde evidencia terminal
confirmada. No existen templates, expresiones, variables de entorno ni valores
aportados por el modelo.

Las assertions locales conservan el contrato del assertion engine. Una
comparación de observaciones identifica un paso `before`, uno `after`, un path
tipado en cada evidencia y exactamente `equal` o `not_equal`. Ambos pasos deben
ser `observe`, pertenecer al mismo target, operación canónica y selector de
recurso (path parameters y query canónicos), y `before` debe ser antecesor de
`after`. La igualdad usa semántica JSON exacta, sin coerción.

Los límites efectivos son configuración confiable. Un manifest o el modelo no
pueden aumentarlos. Como mínimo acotan pasos, profundidad/referencias,
assertions, bytes resueltos, llamadas, retries, trabajo determinista, ciclos de
replanificación, duración total y timeout por paso. Cada reserva pertenece al
run y al paso exacto y no se devuelve tras comenzar trabajo externo.

## Fuera de alcance

- Generar credenciales, fixtures, contratos, targets o nuevas capacidades.
- Permitir SQL, shell, browser automation, SDKs cloud, Kubernetes, filesystem
  remoto o URLs arbitrarias.
- Ejecutar pasos en paralelo o distribuir un flujo entre servicios nuevos.
- Aceptar scripts, expresiones, JSONPath libre, regex o assertions escritas en
  lenguaje natural.
- Compensar acciones de negocio. Solo los fixtures tienen cleanup declarado;
  una acción del flujo no obtiene rollback implícito.
- Convertir cleanup, logs, texto del agente o respuestas no confirmadas en
  evidencia funcional.
- Modificar el lenguaje general de constraints o sustituir el assertion engine.
- Aprovisionar entornos, descubrir contratos, crear identidades o implementar
  memoria/contexto de proyecto de las features 28 y 29.
- Añadir precios o presupuestos monetarios.

## Actores y términos

- **Flow producer:** manifest confiable o agente que propone un flujo; no
  concede autoridad de ejecución.
- **Functional flow:** snapshot canónico, inmutable y validado del grafo completo.
- **Business step:** observación o acción que usa una operación registrada.
- **Observation:** operación sin efecto contractual que obtiene evidencia del
  estado actual.
- **Action:** operación cuyo contrato declara que puede producir efectos.
- **Observation comparison:** assertion determinista que relaciona dos
  evidencias terminales confirmadas del mismo recurso.
- **Primary result:** veredicto funcional calculado exclusivamente desde el
  ledger de assertions y sus evidencias.
- **Flow revision:** lote canónico inicial o adicional aprobado y ligado a la
  misma autoridad del run.
- **Execution intent:** checkpoint durable anterior a una operación que puede
  producir efectos y que impide repetirla de forma incierta.
- **Cleanup result:** dimensión operativa separada producida por el lifecycle de
  fixtures.

## R1 — Un único contrato canónico para ambos orígenes

WHEN a provided or generated flow is admitted, THE system SHALL normalize it
into one immutable strict `FunctionalFlow/v1` contract before fixtures or
business operations execute.

### Acceptance criteria

- GIVEN dos fuentes con la misma semántica WHEN se normalizan THEN producen el
  mismo grafo y semantic digest; sus artifact digests pueden diferir solo por
  metadata allowlisted de procedencia.
- GIVEN campos desconocidos, keys duplicadas, unión ambigua, IDs duplicados,
  colecciones nil obligatorias o límites excedidos WHEN se decodifica THEN se
  rechaza con categoría y field path estables y cero efectos.
- GIVEN mutación posterior de mapas, slices o DTOs de entrada WHEN se usa el
  flujo validado THEN el snapshot, orden y digest no cambian.
- GIVEN un plan histórico que no solicita esta feature WHEN se carga THEN
  conserva su interpretación y no obtiene pasos, identidades o permisos por
  defecto.

## R2 — Autoridad exacta de restricción, operación e identidad

WHEN a flow step is validated, THE system SHALL bind its existing constraint,
exact approved operation, authorized target and explicit test identity before
the step can become executable.

### Acceptance criteria

- GIVEN un binding exacto de constraint, target, method, path template,
  capability, contract digest e identity WHEN se sella el flujo THEN el paso
  conserva ese binding no secreto y su digest.
- GIVEN una restricción, operación o identidad ausente, desconocida, ambigua,
  no aprobada o de otro target/revisión WHEN se valida THEN todo el flujo se
  rechaza antes de resolver credenciales, policy runtime o red.
- GIVEN una operación con efectos declarada como `observe` o una operación sin
  efectos declarada como `act` WHEN se valida THEN se rechaza la clasificación.
- GIVEN un paso sin `identity_id`, incluso si existe una sola identidad
  compatible o la operación no exige autenticación en transporte, WHEN se valida
  THEN no se elige un actor implícitamente y el flujo se rechaza.

## R3 — Grafo completo, orden estable y referencias explícitas

WHEN a functional flow is submitted, THE validator SHALL validate the whole
dependency graph and every typed reference before effects and SHALL derive a
stable sequential topological order using declaration order as tie breaker.

### Acceptance criteria

- GIVEN un grafo válido WHEN se valida repetidamente THEN obtiene el mismo orden
  y todas las dependencias preceden a sus consumidores.
- GIVEN dependencia desconocida, duplicada, propia, ciclo o referencia a un
  paso fuera del cierre transitivo WHEN se valida THEN ningún fixture, policy,
  resolver o tool se invoca y el primer error es estable.
- GIVEN una referencia a fixture WHEN se valida THEN selecciona exactamente una
  capture declarada con tipo compatible; no crea una arista implícita.
- GIVEN una referencia a paso WHEN se resuelve THEN usa solo evidencia terminal
  confirmada del mismo run/revisión; un output embebido por producer o modelo no
  la sustituye.

## R4 — Preparación de fixtures como precondición

WHERE a flow depends on fixtures, THE workflow SHALL complete the validated
fixture preparation before selecting any business step and SHALL block primary
execution when preparation is not fully confirmed.

### Acceptance criteria

- GIVEN fixtures preparados WHEN comienza el flujo THEN sus captures privadas
  pueden resolverse solo por las referencias declaradas.
- GIVEN preparation failed, uncertain, cancelled or corrupt WHEN se evalúa el
  primer paso THEN ninguna operación de negocio comienza y el primary result es
  inconclusive/error operativo, nunca failed funcional.
- GIVEN un flujo sin fixtures WHEN se ejecuta THEN el lifecycle confirma
  `not_required` sin inventar captures ni llamadas.
- GIVEN replanificación posterior WHEN añade comprobaciones THEN reutiliza el
  snapshot confirmado y no repite create.

## R5 — Ejecución secuencial con controles por paso

WHEN a business step becomes ready, THE executor SHALL revalidate ownership,
binding, references, input, policy, budgets, identity and cancellation before
executing the controlled operation and persisting sanitized evidence.

### Acceptance criteria

- GIVEN un paso ready WHEN ejecuta THEN el orden observable es claim/intention,
  referencias, input, binding, policy, budget, identidad, tool, saneado,
  evidencia y assertions.
- GIVEN denial, drift, límite agotado, referencia no resoluble o identidad
  inválida en cualquier recheck WHEN se alcanza THEN no abre conexión y termina
  inconclusive/error con causa segura.
- GIVEN varias ramas ready WHEN se ejecutan THEN lo hacen secuencialmente y con
  orden estable; no existen goroutines de paso ocultas.
- GIVEN policy o presupuesto más restrictivos que los solicitados por la fuente
  WHEN se compone THEN prevalece la configuración confiable y la fuente no
  amplía autoridad.

## R6 — Observaciones pre/post y comparaciones deterministas

WHEN a flow declares a before/after observation comparison, THE system SHALL
evaluate it only from both confirmed terminal evidences and SHALL preserve the
two evidence references in the assertion result.

### Acceptance criteria

- GIVEN observaciones del mismo recurso antes y después de una acción WHEN sus
  selectors resueltos coinciden y sus paths tipados existen THEN `equal`
  demuestra ausencia de cambio y `not_equal` demuestra cambio mediante
  igualdad JSON exacta.
- GIVEN distinto target, operación canónica, selector de recurso, tipo, run o
  revisión, o un `before` no antecesor WHEN se valida o resuelve THEN la
  comparación se rechaza o queda inconclusive antes de afirmar un resultado.
- GIVEN una de las evidencias ausente, no terminal, truncada, corrupta o con
  path/tipo incompatible WHEN se evalúa THEN el resultado es inconclusive o
  error operativo según la causa, nunca passed.
- GIVEN texto del agente que contradice la comparación WHEN se calcula el
  resultado THEN no altera la assertion ni sus evidencias.

## R7 — Assertions y veredicto funcional inmutables

WHEN evidence is confirmed for a step, THE deterministic assertion engines
SHALL append local and comparison results to an immutable ledger, and THE
workflow SHALL derive every constraint and flow verdict exclusively from that
ledger and required evidence.

### Acceptance criteria

- GIVEN cualquier assertion requerida failed WHEN se agrega THEN su constraint
  y el flujo son failed aunque el agente afirme éxito o haya cleanup exitoso.
- GIVEN todas las assertions requeridas passed con evidence IDs válidos WHEN se
  agrega THEN la constraint y el flujo pueden ser passed.
- GIVEN ninguna failure confirmada pero evidencia requerida missing,
  inconclusive o blocked WHEN se agrega THEN el resultado es inconclusive.
- GIVEN corrupción del journal/evidence/ledger o imposibilidad de confirmar
  auditoría obligatoria WHEN se finaliza THEN el outcome es error y no se
  publica un veredicto funcional fabricado.

## R8 — Bloqueos causales y ramas independientes

IF a step dependency does not pass or produce its required evidence, THEN THE
executor SHALL omit the dependent step, mark it blocked with every direct
cause, and SHALL continue independent ready branches while authority remains.

### Acceptance criteria

- GIVEN dependencia failed, inconclusive, blocked o error WHEN se visita el
  consumidor THEN no reserva tool call ni resuelve credencial y registra causas
  directas en declaration order.
- GIVEN varias causas bloqueantes WHEN se proyecta el step THEN aparecen todas
  con estado y evidence references seguras.
- GIVEN una rama bloqueada y otra independiente ready WHEN no existe
  cancelación ni fallo de integridad THEN la independiente continúa.
- GIVEN cancelación o pérdida de autoridad global WHEN quedan ramas THEN no se
  inicia ninguna y se pasa a finalización/cleanup.

## R9 — Efectos, intents, retries y redelivery conservadores

BEFORE an `act` step may cross the network boundary, THE workflow SHALL confirm
a durable execution intent, and IF its outcome becomes uncertain, THEN THE
workflow SHALL NOT repeat the effect without a verified idempotency or
reconciliation guarantee.

### Acceptance criteria

- GIVEN una acción potencialmente efectiva WHEN va a ejecutarse THEN su intent,
  binding digest, step revision y key opaca del sistema quedan confirmados antes
  de I/O.
- GIVEN crash o timeout tras posible I/O sin resultado confirmado WHEN se
  recupera THEN reutiliza la misma key si el contrato permite retry/reconcile;
  si no, termina inconclusive y no repite la acción.
- GIVEN observación idempotente o error transitorio de acción con garantía
  contractual y budgets WHEN se reintenta THEN cada intento revalida todos los
  guards y queda auditado.
- GIVEN fallo funcional, status confirmado no aceptado, denial permanente,
  assertion failed o capture/reference inválida WHEN se clasifica THEN no se
  reintenta para buscar un resultado favorable.

## R10 — Replanificación acotada y monotónica

WHERE replanning is enabled, WHEN an executed revision requests additional
checks, THE workflow SHALL validate and approve a new bounded flow revision
through the ordinary pipeline without modifying prior steps, evidence or
assertion results.

### Acceptance criteria

- GIVEN un lote adicional válido WHEN se admite THEN solo puede depender de
  pasos confirmados anteriores o del propio lote y recibe nueva revisión,
  digest, budgets y approval cuando corresponda.
- GIVEN un lote que cambia constraints, fixtures, captures, target, contrato,
  identidad autorizada o resultados previos WHEN se valida THEN se rechaza
  atómicamente sin ejecutar su prefijo.
- GIVEN una propuesta semánticamente equivalente ya vista WHEN se recibe THEN
  se registra como repetida y no consume tool/approval adicional, aunque
  conserva los ciclos/model calls ya usados.
- GIVEN cancelación o agotamiento durante replanificación WHEN se observa THEN
  no comienza otra revisión, se conserva el ledger y se inicia cleanup.

## R11 — Cleanup garantizado y separado

WHEN primary execution reaches any terminal path, including cancellation,
replanning failure or operational error, THE workflow SHALL invoke fixture
cleanup exactly through the finite lifecycle of feature 26 and SHALL preserve
the primary result unchanged.

### Acceptance criteria

- GIVEN primary passed, failed, inconclusive, cancelled or error WHEN finaliza
  THEN se intenta cleanup inverso de todos los fixtures elegibles.
- GIVEN cleanup partial, failed, exhausted o timed out WHEN se publica THEN el
  primary verdict y su ledger permanecen byte-for-byte iguales y se añade una
  incidencia operativa separada.
- GIVEN cancelación del contexto principal WHEN comienza cleanup THEN usa el
  scope propio, finito y owned ya definido; no rehabilita pasos funcionales.
- GIVEN recovery o redelivery de finalización WHEN cleanup ya tiene terminales
  confirmados THEN converge sin repetirlos y sin publicar falso éxito.

## R12 — Journal, evidence, package e informe reconstruibles

WHEN a flow advances, THE system SHALL append versioned ordered records that
allow an offline reader to reconstruct preparation, revision order, step
selection, actions, observations, assertion evaluation, blocking, primary
verdict and cleanup from sealed artifacts and evidence references.

### Acceptance criteria

- GIVEN un run terminal WHEN se elimina cualquier proyección derivada y se
  reconstruye THEN obtiene el mismo orden, estados, ledger, verdict y cleanup
  report desde journal, flow artifacts y evidence package verificados.
- GIVEN un evento, digest, evidence ID o revision faltante, reordenado,
  duplicado contradictorio o perteneciente a otro run WHEN se reconstruye THEN
  falla cerrado y no proyecta passed.
- GIVEN una acción u observación omitida WHEN se inspecciona el informe THEN
  explica la causa y no contiene un evento de tool execution inexistente.
- GIVEN un package publicado WHEN se verifica THEN cada passed enlaza evidencia
  persistida y cada comparación enlaza sus dos observaciones.

## R13 — Ownership, recovery, cancelación y cierre

WHILE a flow is active, THE workflow SHALL serialize transitions per run with
lease and compare-and-swap, propagate cancellation through every blocking
boundary, and recover confirmed work without duplicate external effects.

### Acceptance criteria

- GIVEN dos owners o deliveries concurrentes para el mismo run/revisión/step
  WHEN compiten THEN como máximo uno confirma claim/intent/tool start y el otro
  converge desde estado durable.
- GIVEN runs independientes WHEN progresan THEN no comparten captures, budgets,
  identities, evidence ni locks de I/O.
- GIVEN cancelación durante store, policy, budget, credential, tool, evidence,
  assertion, replan o publish WHEN retorna la frontera THEN no empieza otra
  unidad funcional, se conserva el prefijo confirmado y todo owner termina.
- GIVEN recovery desde cada checkpoint WHEN se ejecuta la matriz de crashes
  THEN no repite efectos confirmados, no salta assertions y siempre converge a
  primary terminal más cleanup terminal/pending auditable.

## R14 — Seguridad de referencias, captures y credenciales

THE flow runtime SHALL keep credential material, credential references, raw
fixture captures, system idempotency keys and unredacted external data outside
model context, canonical flow artifacts, journals, evidence packages, reports
and external surfaces.

### Acceptance criteria

- GIVEN canarios distintos para credential, capture, key, body y error externo
  WHEN se recorren todas las superficies publicables/durables/model-facing THEN
  no aparece ningún valor prohibido.
- GIVEN una referencia de fixture WHEN se proyecta al agente THEN solo contiene
  IDs, tipo y metadata segura; el valor se resuelve tras policy/budget y antes
  del transporte autorizado.
- GIVEN instrucciones dentro de responses, captures, logs o errores WHEN se
  usan como datos THEN no se convierten en operaciones, dependencias, identity
  IDs ni privileged instructions.
- GIVEN fallo de saneado, capture resolution o persistencia WHEN ocurre THEN
  falla cerrado antes de proyectar datos parciales o calcular passed.

## R15 — Paridad de superficies y verificación hermética

THE feature SHALL expose the same functional-flow service and safe result
semantics through CLI, API, MCP and GitHub, and SHALL be verified with
deterministic local fakes without real credentials, providers or Internet.

### Acceptance criteria

- GIVEN la misma selección autorizada por cualquiera de las cuatro superficies
  WHEN se inicia/consulta/cancela THEN obtiene el mismo flow digest, fases,
  primary outcome, evidence references y cleanup projection.
- GIVEN MCP/API/GitHub WHEN seleccionan un flujo proporcionado THEN usan un
  alias/artefacto ya autorizado; no aceptan paths, URLs, captures, credentials,
  idempotency keys ni operaciones inline que amplíen autoridad.
- GIVEN redelivery con la misma key y payload WHEN llega por cualquier adapter
  THEN devuelve el mismo handle/estado; la misma key con otro digest es
  conflicto.
- GIVEN la suite ordinaria WHEN se ejecuta THEN usa servers/stores/modelos/reloj
  locales, alcanza al menos 85 % de statement coverage del código productivo
  nuevo o modificado, pasa race/cross-platform y cada entidad, función, método,
  callback, fake y helper nuevo o modificado tiene comentario de responsabilidad
  sistémica.

## Errores y casos límite

- El flujo se admite como unidad; un nodo inválido impide preparar fixtures o
  ejecutar un prefijo aparentemente válido.
- Los IDs no se recortan ni normalizan. Diferencias de case son significativas.
- `null` es un valor JSON presente. No hay coerciones entre string, number,
  boolean, object, array y null.
- Una evidencia truncada puede conservar metadata, pero no soporta una
  comparación cuyo path no fue confirmado.
- Un 401/403 obtenido con credencial válidamente inyectada es evidencia HTTP;
  un fallo local de resolución/inyección es operacional.
- Una action sin garantía de idempotencia puede ejecutarse una vez bajo intent,
  pero no reintentarse ni redeliverarse tras resultado incierto.
- Un fallo de persistencia posterior a evidencia recibida impide confirmar el
  step y no autoriza repetir un efecto incierto.
- El ledger conserva fallos confirmados aunque una revisión posterior pase.
- El resultado de cleanup no se usa como assertion y sus evidence IDs no
  satisfacen requisitos funcionales.
- No se mantienen locks durante modelo, policy, red, credenciales, evidence,
  callbacks, timers o cleanup.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. El contrato canónico es
interno y versionado; el adapter concreto de un flujo proporcionado puede
evolucionar sin cambiar su semántica. Nuevos tipos de herramienta, operadores
de comparación, ejecución paralela, compensaciones de negocio, memoria y
ensamblado de contexto quedan para features posteriores.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-23T09:21:17+02:00
```
