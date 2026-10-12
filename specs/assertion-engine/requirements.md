# Motor determinista de assertions — Requirements

## Objetivo y problema

FeatureProof necesita convertir evidencias estructuradas en resultados
verificables sin delegar el veredicto a un modelo. Esta feature define un motor
que valida declaraciones de assertions antes de ejecutar un plan, localiza
valores dentro del output confirmado de una herramienta, evalúa operadores y
grupos con reglas deterministas y entrega al plan resultados auditables.

## Alcance

- Definir el contrato estructurado de assertions hoja y grupos `all`/`any`.
- Validar de forma completa las declaraciones antes de ejecutar herramientas.
- Documentar y validar una sintaxis acotada de paths sobre valores JSON.
- Evaluar igualdad, existencia, ausencia y comparaciones numéricas.
- Evaluar grupos anidados con agregación ternaria determinista.
- Producir resultados estructurados con esperado, observado, estado y evidencia.
- Implementar el puerto `planning.StepAssertionEvaluator` de la feature 3.
- Distinguir configuración inválida, incumplimiento funcional, falta de
  evidencia y error operativo/cancelación.
- Probar el motor sin red ni modelo real.

### Contrato de declaración

Cada `planning.AssertionDeclaration` conserva su `id` y contiene en
`definition` exactamente uno de estos nodos:

- **Hoja:** `operator` y `path`; además `expected` solo cuando el operador lo
  requiere.
- **Grupo:** una única clave `all` o `any`, cuyo valor es una lista no vacía de
  nodos hijos.

Los operadores hoja soportados son `eq`, `exists`, `absent`, `gt`, `gte`, `lt`
y `lte`. `eq` y los cuatro operadores numéricos requieren `expected`;
`exists` y `absent` lo prohíben. No se aceptan claves desconocidas ni nodos que
mezclen una hoja con un grupo. Los IDs continúan siendo únicos dentro del paso,
tal como exige la feature 3. Los hijos de un grupo se identifican en resultados
y errores mediante su field path estable dentro de la declaración.

### Sintaxis de paths

`path` usa un subconjunto estricto de JSON Pointer:

- la string vacía `""` selecciona el output raíz;
- cualquier otro path empieza por `/` y contiene tokens separados por `/`;
- dentro de un token, `~0` representa `~` y `~1` representa `/`; cualquier otra
  secuencia iniciada por `~` es inválida;
- después de desescapar, un token con forma numérica —un signo `+` o `-`
  opcional seguido de uno o más dígitos ASCII— solo es válido si cumple la forma
  canónica `0|[1-9][0-9]*`; por tanto `01`, `-1` y `+1` son inválidos en
  cualquier posición;
- sobre un objeto, el token decodificado selecciona una key exacta y sensible a
  mayúsculas, salvo las keys de apariencia numérica no canónica excluidas por
  la regla anterior;
- sobre un array, solo un token numérico canónico se interpreta como índice; un
  token no numérico o un índice fuera de rango se considera path ausente;
- no hay wildcards, filtros, segmentos implícitos, índices negativos ni
  coerción entre objeto y array.

Esta restricción léxica permite validar el path sin conocer todavía el tipo del
output. Como consecuencia deliberada del subset, no se pueden seleccionar keys
de objeto llamadas `01`, `-1`, `+1` u otra forma numérica no canónica. Un path
bien formado puede no existir en un output concreto. Esa ausencia es un hecho
observado y no convierte la declaración en configuración inválida.

### Estados y valores de resultado

Cada nodo produce `passed`, `failed` o `inconclusive`. Un resultado contiene el
ID de la declaración raíz, el field path estable del nodo, operador, path cuando
aplique, valor esperado, valor observado, estado, evidence IDs usados, hijos y
una causa tipada opcional. Esperado y observado se representan mediante
`{present, value}` para distinguir un `null` observado de un valor ausente.

- En `exists`, el esperado es `true` y el observado indica si el path existe.
- En `absent`, el esperado es `true` y el observado indica si el path está
  ausente.
- En grupos, el esperado es `true` y el observado es el resultado booleano
  cuando puede decidirse; queda no presente si el grupo es `inconclusive`.
- En `eq` y comparaciones, se conservan los valores esperado y observado.

La evidencia utilizada es exclusivamente la evidencia terminal confirmada que
aportó el output. El contrato existente entrega los evidence IDs en orden de
intento; el último ID es el terminal y es el único referenciado por cada
resultado. Los intentos anteriores permanecen auditables en el step, pero no
se presentan como fuente del valor evaluado.

## Fuera de alcance

- Incorporar assertions al YAML v1 o definir una representación externa nueva.
- Ejecutar herramientas, leer directamente el evidence store o recalcular el
  output desde eventos.
- Operadores de texto, regex, cabeceras HTTP, duración, esquemas, colecciones o
  tolerancia numérica.
- Coerciones entre strings, números, booleanos y null.
- Permitir código, plantillas, funciones, wildcards o expresiones arbitrarias
  dentro de paths o assertions.
- Calcular el resultado de una constraint, run o reporte final.
- Pedir a un modelo que interprete, repare o confirme una assertion.

## Actores y términos

- **Assertion definition validator:** frontera consumida por el plan validator
  para rechazar toda declaración inválida antes de efectos.
- **Assertion engine:** implementación determinista que evalúa declaraciones ya
  validadas contra un output confirmado.
- **Leaf assertion:** comparación concreta sobre un path.
- **Group assertion:** composición `all` o `any` de uno o más nodos.
- **Present value:** path localizado, incluso cuando su valor es JSON `null`.
- **Missing value:** path bien formado que no existe en el output observado.
- **Invalid:** configuración que no cumple el contrato y rechaza el plan; no es
  un estado funcional de assertion.
- **Inconclusive:** declaración válida que no puede evaluarse porque falta la
  evidencia terminal necesaria.

## R1 — Validación completa antes de efectos

WHEN a plan contains assertion declarations, THE plan validator SHALL validate
every definition completely before policy evaluation, tool execution or
evidence creation, and SHALL reject unsupported or malformed definitions with a
typed error and stable field path.

### Acceptance criteria

- GIVEN una hoja con un operador soportado, las claves exactas exigidas y tipos
  JSON compatibles
  WHEN se valida el plan
  THEN la declaración se acepta sin ejecutarla.
- GIVEN operador desconocido, clave desconocida, `path` ausente/no string,
  `expected` ausente/prohibido, valor no JSON compatible o nodo que mezcla
  variantes
  WHEN se valida
  THEN se rechaza el plan completo, el error identifica
  `steps[i].assertions[j].definition...` y policy/tool reciben cero llamadas.
- GIVEN un grupo vacío o un hijo inválido a cualquier profundidad
  WHEN se valida
  THEN se rechaza toda la declaración y el field path identifica el primer nodo
  inválido en orden de declaración.

## R2 — Resolución de paths documentada y determinista

WHEN a valid leaf assertion is evaluated, THE assertion engine SHALL resolve
its path according to the documented JSON Pointer subset without wildcard,
coercion or map-iteration-dependent behavior.

### Acceptance criteria

- GIVEN `""`, `/status`, `/items/0/name` y `/a~1b/~0key`
  WHEN se evalúan contra outputs compatibles
  THEN se seleccionan respectivamente la raíz, keys, índices y keys escapadas
  exactas.
- GIVEN un path sin `/` inicial, escape `~` inválido o cualquier token de
  apariencia numérica negativo, con signo positivo o con cero inicial
  WHEN se valida la definición
  THEN se rechaza globalmente antes de ejecutar el plan, aunque el output
  runtime pudiera ser un objeto con una key escrita de esa forma.
- GIVEN un path válido cuyo key o índice no existe, o que intenta atravesar un
  escalar
  WHEN se evalúa
  THEN el valor observado queda marcado como no presente de forma determinista.

## R3 — Existencia y ausencia

WHEN `exists` or `absent` is evaluated with confirmed evidence, THE assertion
engine SHALL decide from path presence, treating an explicit JSON null as
present.

### Acceptance criteria

- GIVEN un path presente con cualquier valor, incluido null
  WHEN se evalúa `exists`
  THEN el resultado es `passed`; `absent` sobre el mismo path es `failed`.
- GIVEN un path bien formado que no existe
  WHEN se evalúan `exists` y `absent`
  THEN producen respectivamente `failed` y `passed` con observado booleano.
- GIVEN `exists` o `absent` con `expected`
  WHEN se valida
  THEN la configuración es inválida y no se ejecuta ninguna herramienta.

## R4 — Igualdad y comparaciones numéricas

WHEN `eq`, `gt`, `gte`, `lt` or `lte` is evaluated with confirmed evidence,
THE assertion engine SHALL compare values using explicit JSON type rules and
SHALL NOT coerce incompatible types.

### Acceptance criteria

- GIVEN `eq`
  WHEN esperado y observado tienen el mismo tipo JSON y valor —objetos sin
  depender del orden de keys, arrays respetando orden y números por valor
  matemático finito—
  THEN el resultado es `passed`; cualquier diferencia o tipo incompatible
  produce `failed`.
- GIVEN `gt`, `gte`, `lt` o `lte` con expected numérico finito y observado
  numérico finito
  WHEN se evalúa
  THEN aplica exactamente la relación indicada sin convertir strings a
  números.
- GIVEN expected no numérico/NaN/infinito para un operador numérico
  WHEN se valida
  THEN la definición es inválida; GIVEN observed ausente o no numérico con
  evidencia confirmada WHEN se evalúa THEN el resultado es `failed` y conserva
  el observado.

## R5 — Grupos anidados `all` y `any`

WHEN assertions are composed in nested `all` or `any` groups, THE assertion
engine SHALL evaluate children in declaration order and aggregate their three
states deterministically.

### Acceptance criteria

- GIVEN un grupo `all`
  WHEN al menos un hijo es `failed`
  THEN el grupo es `failed`; si todos pasan es `passed`; en otro caso es
  `inconclusive`.
- GIVEN un grupo `any`
  WHEN al menos un hijo pasa
  THEN el grupo es `passed`; si todos fallan es `failed`; en otro caso es
  `inconclusive`.
- GIVEN grupos anidados
  WHEN se evalúan repetidamente
  THEN todos los hijos se evalúan y aparecen en declaration order, sin
  short-circuit que oculte resultados, y el árbol y estado agregado coinciden.

## R6 — Resultado auditable y evidencia utilizada

WHEN a declaration is evaluated, THE assertion engine SHALL return a complete
result tree containing operator, expected, observed, status and the evidence IDs
actually used by every node.

### Acceptance criteria

- GIVEN una hoja o grupo evaluado
  WHEN se inspecciona su resultado
  THEN contiene declaration ID, node field path, operador, expected/observed
  con presencia explícita, estado y evidence IDs usados; con evidencia
  confirmada la lista contiene solo el ID terminal y sin evidencia queda vacía.
- GIVEN varios intentos de un step y un output terminal confirmado
  WHEN se evalúa
  THEN los resultados referencian solo el último evidence ID y no atribuyen el
  valor a intentos transitorios anteriores.
- GIVEN las mismas declaraciones, output y evidence IDs
  WHEN se evalúan repetidamente
  THEN los resultados tienen el mismo orden y contenido, y no mutan input,
  definitions ni output.

## R7 — Distinción entre invalid, failed e inconclusive

THE assertion engine SHALL distinguish invalid configuration from a valid
condition that is false and from a valid condition lacking its required
confirmed evidence.

### Acceptance criteria

- GIVEN una definición inválida
  WHEN se valida
  THEN se devuelve un error tipado y no se crea un resultado funcional.
- GIVEN una definición válida y evidencia terminal confirmada que contradice la
  condición o presenta un tipo observado incompatible
  WHEN se evalúa
  THEN el resultado es `failed`, nunca `inconclusive`.
- GIVEN definiciones válidas pero ningún evidence ID terminal o ningún output
  confirmado utilizable
  WHEN se evalúan
  THEN cada raíz es `inconclusive`, el step es `inconclusive` y ningún resultado
  es `passed`.

## R8 — Integración determinista con el plan sin modelo

WHEN the assertion engine implements `planning.StepAssertionEvaluator`, THE
plan executor SHALL derive each step status from the declaration results using
all semantics and SHALL never accept a model claim as an input to that
calculation.

### Acceptance criteria

- GIVEN varias declaraciones raíz
  WHEN se agregan
  THEN el step es `failed` si alguna falla, `passed` si todas pasan y, en
  cualquier otro caso, `inconclusive`.
- GIVEN output y declaraciones iguales aunque exista una afirmación externa
  distinta
  WHEN se ejecuta el evaluador
  THEN el resultado no cambia y el plan conserva las reglas de propagación de
  la feature 3.
- GIVEN tests del motor y de integración
  WHEN se ejecutan
  THEN no acceden a red, servicios externos ni un modelo real.

## R9 — Cancelación y errores operativos

IF evaluation context is cancelled or reaches its deadline, THEN THE assertion
engine SHALL stop at the next traversal boundary, return the context cause and
SHALL NOT fabricate a functional assertion result.

### Acceptance criteria

- GIVEN contexto cancelado antes de evaluar
  WHEN se invoca el motor
  THEN devuelve la causa del contexto sin resultados funcionales.
- GIVEN cancelación durante un árbol anidado
  WHEN se alcanza el siguiente nodo
  THEN cesa la evaluación, el executor no inicia otro step y los resultados ya
  confirmados por el plan permanecen intactos.
- GIVEN un error interno inesperado
  WHEN se propaga al plan executor
  THEN se trata como error de FeatureProof y no como `failed` ni
  `inconclusive` funcional.

## Errores y casos límite

- Una declaración nil, vacía, con más de una variante o con claves extra es
  inválida.
- Los nombres de operadores y keys del contrato son exactos y sensibles a
  mayúsculas.
- JSON null es un valor presente y solo es igual a null.
- Un objeto con keys en distinto orden puede ser igual; un array reordenado no.
- Los números finitos de distintas representaciones Go se normalizan por su
  valor matemático; NaN e infinitos nunca son valores JSON válidos.
- Un path presente con valor incompatible falla la condición; un output no
  respaldado por evidencia confirmada deja la evaluación inconclusa.
- La lista de evidence IDs vacía y el output nil no autorizan un `passed`.
- Los errores conservan causa con `%w` y se distinguen por tipo/categoría, no
  por comparación textual.

## Decisiones abiertas

No hay decisiones abiertas que impidan implementar los acceptance criteria. La
serialización externa de estas declaraciones se definirá en una feature futura
si se incorpora a un formato de specification.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-11T18:41:50+02:00
```
