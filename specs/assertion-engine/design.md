# Motor determinista de assertions — Design

## Resumen de la solución

Se añadirá un paquete interno `assertion` responsable de compilar declaraciones
estructuradas a un árbol validado y evaluarlas sobre el output terminal
confirmado de un step. `planning` conservará la interfaz consumidora e
incorporará contratos neutrales de resultado para que el executor reciba el
estado y el detalle auditable sin importar la implementación concreta.

La validación del plan recibirá un puerto pequeño para validar cada definition.
De esta forma, una assertion mal configurada impide obtener un `ValidatedPlan`
y nunca alcanza policy o herramientas. Durante la ejecución, el motor vuelve a
trabajar sobre el snapshot inmutable ya validado, resuelve paths, calcula cada
nodo y devuelve los resultados en declaration order. El modelo no forma parte
de ninguna entrada o dependencia.

## Fuentes de contexto consultadas

- `AGENTS.md`: flujo SDD, alcance único y responsabilidades de agentes.
- `feature_list.json`: acceptance criteria de `assertion-engine`.
- `docs/architecture.md`: ARCH-003/004 sobre evidencia y veredictos,
  determinismo, cancelación, puertos y responsabilidad del assertion evaluator.
- `docs/conventions.md`: paquetes, interfaces consumidoras, errores, contextos,
  comentarios y tests herméticos.
- `docs/specs.md`: estructura, puerta humana y trazabilidad R→task→test.
- Specs y código de features 1–3: `planning.AssertionDeclaration`,
  `StepAssertionEvaluator`, `StepEvaluation`, `PlanExecutor`, estados de step,
  evidence IDs y output saneado/confirmado.

No existen contratos externos ni repositorios externos consultados. La sintaxis
de path toma únicamente el mecanismo de escape de JSON Pointer y queda definida
por completo en `requirements.md`; no requiere una librería o servicio externo.

## Contratos y modelos

### Definition sin DTO externo

La entrada continúa siendo `planning.AssertionDeclaration{ID, Definition}`.
Esta feature no añade tags YAML/JSON ni modifica `validation-specification`.
El compilador interpreta `Definition` con estas uniones cerradas:

```text
leaf  = {operator, path, expected?}
group = {all: [node, ...]} | {any: [node, ...]}
```

La validación exige maps con keys string, slices, escalares JSON compatibles y
la combinación exacta de keys de cada variante. Recorre declarations, grupos e
hijos por su orden de slice. Nunca itera un map para decidir qué error reportar:
comprueba las keys contractuales en un orden fijo y ordena keys desconocidas
para producir un diagnóstico estable.

### Path compilado

El parser convierte el path string a una lista inmutable de tokens ya
desescapados. La string vacía representa raíz. Antes de disponer del output,
clasifica como de apariencia numérica todo token que cumpla
`^[+-]?[0-9]+$` y lo acepta solo si cumple `^(0|[1-9][0-9]*)$`. Así `01`,
`-1` y `+1` se rechazan globalmente y no pueden interpretarse después como keys
de objeto. Esta exclusión deliberada hace que la gramática sea validable sin
conocer el tipo runtime.

En evaluación, el tipo del nodo actual decide si un token válido es key exacta
o, cuando es numérico canónico y el nodo es un array, índice. Un token no
numérico sobre un array produce ausencia. El resolver devuelve
`Value{Present, Data}`; así distingue null (`Present=true, Data=nil`) de path
ausente (`Present=false`).

### Resultados neutrales del puerto

`planning.StepEvaluation` se ampliará para transportar resultados tipados y
eliminar la necesidad de metadatos opacos para esta frontera. Los contratos
consumidos por el executor contendrán conceptualmente:

- `AssertionStatus`: `passed`, `failed`, `inconclusive`;
- `AssertionValue`: `Present` y `Value` JSON compatible;
- `AssertionResult`: declaration ID, node path, operator, assertion path,
  expected, observed, status, evidence IDs, children y causa tipada opcional;
- `StepEvaluation`: status de step y resultados raíz en declaration order.

El paquete `planning` posee estos DTO porque define la interfaz consumidora;
`assertion` posee AST, parser, comparación y agregación. Se retirará o dejará de
usar `Metadata` si ya no aporta información a consumidores existentes. Los
fakes de planning seguirán devolviendo el contrato mínimo necesario para sus
tests.

Los valores de los resultados se copian para que mutaciones posteriores del
output o de la definition no alteren lo ya calculado. Cada nodo recibe un path
estable como `steps[0].assertions[1].definition.any[2]`.

### Errores

Se añadirá un error tipado de configuración con:

- categoría estable (`required`, `invalid_shape`, `unsupported_operator`,
  `invalid_path`, `invalid_type`);
- field path;
- causa envuelta cuando exista.

La integración lo traduce al `planning.PlanError` apropiado sin perder la
causa. Configuración inválida es error de validación. Una condición falsa es un
resultado `failed`. Falta de evidence ID/output confirmado produce
`inconclusive`. Cancelación y fallos internos se devuelven como error operativo.

## Componentes y responsabilidades

### Definition compiler

1. Comprueba la variante exacta del nodo.
2. Valida operador, combinación de fields y valor esperado.
3. Compila y valida el path.
4. Normaliza de forma inmutable valores JSON y números finitos.
5. Compila hijos en declaration order.
6. Devuelve un árbol inmutable o el primer error tipado estable.

La compilación no necesita output ni evidencia y no calcula resultados.

### Definition validator port

`planning` definirá una interfaz pequeña, por ejemplo
`AssertionDefinitionValidator`, con una operación que valida una declaration y
su field path. `planning.Validator` la recibirá explícitamente en su
constructor y la invocará para cada assertion después de comprobar ID y
definition no vacía. La implementación de `assertion` compila y descarta el AST
o mantiene un valor validado sin estado mutable compartido.

Todos los call sites y fakes existentes se actualizarán. No se hará opcional la
dependencia productiva, porque permitir un `ValidatedPlan` con assertions
opacas rompería R1.

### Path resolver

Recorre el output sin modificarlo:

- map: busca key exacta;
- slice/array: exige token de índice canónico y comprueba rango;
- otro tipo antes de consumir todos los tokens: devuelve ausente;
- root: devuelve el output completo como presente.

Un lookup ausente no es error. Un path mal formado ya fue rechazado durante
compilación.

### Comparadores

- `exists` y `absent` comparan presencia, no el contenido.
- `eq` aplica igualdad JSON estricta: null, boolean, string, number, array y
  object deben tener la misma clase; arrays conservan orden y objects comparan
  keys/valores sin depender del orden del map.
- Los números se normalizan a una representación exacta finita para comparar
  ints, uints, floats finitos y `json.Number` por valor matemático sin convertir
  strings.
- `gt/gte/lt/lte` requieren expected numérico validado; observed ausente o de
  tipo no numérico genera `failed`, no coerción ni error interno.

### Group evaluator

Evalúa todos los hijos de izquierda a derecha y comprueba `ctx.Err()` antes de
cada nodo. Después agrega:

| Operador | `passed` | `failed` | `inconclusive` |
|---|---|---|---|
| `all` | todos pasan | alguno falla | ningún fallo y alguno inconcluso |
| `any` | alguno pasa | todos fallan | ningún passed y alguno inconcluso |

No hay short-circuit funcional porque el resultado debe conservar todos los
hijos para auditoría. Varias declarations raíz se agregan como un `all`
implícito para producir el estado del step.

### Adapter de `StepAssertionEvaluator`

La implementación recibe contexto, `PlanStep`, output confirmado y evidence
IDs. Antes de evaluar:

1. observa cancelación;
2. si no hay evidence IDs o el output es nil, crea una raíz inconclusive por
   declaration y devuelve step inconclusive;
3. toma el último evidence ID como fuente terminal;
4. compila/valida defensivamente las definitions del snapshot;
5. evalúa todas las declaraciones y agrega el step.

Una invalidación defensiva tras haber validado el plan indica error interno y
se propaga; no se convierte en estado funcional. El adapter no lee el evidence
store: el executor de feature 3 ya recuperó, verificó origen e integridad y
aportó el output confirmado.

## Paquetes afectados

```text
internal/
  assertion/
    model.go             # AST compilado, estados, valores y errores internos
    compile.go           # validación de definitions y paths
    path.go              # parser/resolución del subset JSON Pointer
    compare.go           # igualdad JSON y relaciones numéricas
    evaluator.go         # grupos, roots y adapter del puerto de planning
    *_test.go            # tests unitarios e integración herméticos
  planning/
    executor.go          # resultado tipado del puerto y consumo del evaluator
    validate.go          # puerto de validación previa de definitions
    *_test.go            # actualización de fakes y pruebas de integración
```

Los nombres exactos de archivos pueden agruparse si la cohesión resulta mejor.
No se modifica `cmd/`, el adapter YAML, herramientas ni el evidence store.

## Flujos principales

### Validación

```text
ValidationPlan
  -> planning.Validator
  -> IDs/graph/input
  -> AssertionDefinitionValidator por declaration
  -> compile definition + path
  -> ValidatedPlan o PlanError
```

Una definition inválida termina el flujo antes de policy, tool o evidencia.

### Evaluación

```text
evidencia terminal confirmada
  -> PlanExecutor aporta output + IDs
  -> assertion evaluator
  -> compila snapshot defensivamente
  -> resuelve paths
  -> evalúa hojas y grupos
  -> agrega declarations como all
  -> StepEvaluation tipado
  -> PlanExecutor confirma StepResult
```

## Flujos de error

- **Definition/path inválido:** `PlanError` durante validación; cero efectos.
- **Path ausente:** hecho observable. `absent` pasa; `exists`, `eq` y operadores
  numéricos fallan.
- **Observed incompatible:** assertion válida `failed` con observed conservado.
- **Sin output/evidence terminal:** resultados raíz y step `inconclusive`;
  nunca `passed`.
- **Definition alterada tras validación:** el snapshot de planning evita la
  mutación; si la validación defensiva falla, se devuelve error interno.
- **Cancelación/deadline:** se devuelve la causa del contexto y planning detiene
  el plan según su contrato actual.
- **Error interno:** se envuelve con operación y declaration ID; no se convierte
  en incumplimiento funcional.

## Propiedad del estado, concurrencia y cancelación

El AST compilado y los resultados son valores inmutables de una evaluación. El
evaluator no conserva contexto, output, definitions ni resultados entre
llamadas. Por ello una instancia puede servir ejecuciones concurrentes siempre
que no se le añada estado mutable.

No se crean goroutines. Una llamada evalúa secuencialmente en declaration order
y depth-first. Se comprueba el contexto al entrar y antes de cada nodo, y se
devuelve `context.Canceled` o `context.DeadlineExceeded` conservando la causa.
El executor sigue siendo propietario de detener la selección de steps y de
preservar los resultados ya confirmados.

## Retries e idempotencia

Evaluar assertions es una operación pura y no se reintenta. Los retries de
herramienta ya ocurrieron antes y solo el último evidence ID confirmado aporta
el output. Repetir explícitamente la evaluación con las mismas entradas produce
el mismo resultado y no genera efectos, por lo que es idempotente.

## Observabilidad

El resultado tipado contiene toda la información que un evento o reporte futuro
necesita: ID, posición del nodo, operador, path, expected, observed, estado,
evidencia y causa. Esta feature no persiste eventos nuevos ni modifica el
journal; `PlanExecutor` conserva sus eventos de step y puede transportar el
detalle en el resultado sin registrar contenido sensible adicional.

El motor solo recibe output ya saneado y no registra valores. Las features de
redacción y reporte decidirán después qué campos de resultado pueden
publicarse.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro de FeatureProof y
su relación con el flujo o frontera correspondiente. El comentario expresará
intención, invariantes o motivo arquitectónico, sin repetir nombre, parámetros
o sintaxis. La convención se aplica también a errores, AST interno, fakes,
comparadores y auxiliares de path.

## Estrategia de pruebas

Los tests usan definitions y outputs en memoria, el evaluator real y fakes
locales de registry/policy/tool/store cuando prueban integración. No usan red,
filesystem compartido, variables de entorno ni modelo. La cancelación se
inyecta mediante contextos, sin `time.Sleep`.

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | compiler + planning validator | `TestValidateDefinitionsBeforeAnyPlanEffect`; `TestRejectMalformedLeavesGroupsAndUnknownFieldsWithStablePath` |
| R2 | path parser/resolver | `TestResolveDocumentedPointerPaths`; `TestRejectMalformedPointerAndArrayTokens`; `TestMissingPathIsObservedDeterministically` |
| R3 | evaluator | `TestExistsAndAbsentTreatNullAsPresent`; `TestExistsAndAbsentReportMissingPath` |
| R4 | comparators | `TestJSONEqualityAcrossStructuredValuesAndNumericRepresentations`; `TestNumericOperatorsAndIncompatibleTypes`; `TestRejectInvalidNumericExpected` |
| R5 | group evaluator | `TestNestedAllAnyTruthTables`; `TestGroupsEvaluateEveryChildInDeclarationOrder` |
| R6 | result model | `TestResultTreeContainsExpectedObservedStatusAndTerminalEvidence`; `TestEvaluationDoesNotMutateInputsAndIsRepeatable` |
| R7 | validator/evaluator | `TestInvalidFailedAndInconclusiveRemainDistinct`; `TestNoEvidenceCanNeverPass` |
| R8 | planning adapter | `TestStepEvaluatorAggregatesRootDeclarationsWithoutModel`; `TestAssertionEngineIntegratesWithPlanBlockingAndAggregation` |
| R9 | evaluator/planning | `TestCancelledEvaluationReturnsContextCauseWithoutFunctionalResult`; `TestCancellationDuringNestedGroupStopsPlanSelection` |

También se cubren raíz vacía, null, keys escapadas, índice fuera de rango,
objetos con distinto orden, arrays reordenados, enteros grandes, floats finitos,
`json.Number`, NaN/infinito, casing de operadores, grupo vacío, claves extra,
definitions cíclicas/no JSON compatibles, output nil, IDs de evidencia vacíos y
varias ejecuciones concurrentes con race detector.

La verificación final ejecutará gofmt, vet, compilación, suite completa, race y
coverage conforme a `docs/verification.md`. El código nuevo/modificado deberá
alcanzar al menos 85 % de statement coverage y cada R tendrá test trazable.

## Alternativas y decisiones

- **JSONPath completo:** descartado; wildcards, filtros y expresiones amplían el
  lenguaje y dificultan validación/determinismo. El subset JSON Pointer cubre
  objetos, arrays y keys escapadas.
- **Aceptar `01`, `-1` o `+1` como posibles keys de objeto:** descartado porque
  la validación previa no conoce si el nodo runtime será objeto o array. Las
  formas de apariencia numérica no canónicas son inválidas globalmente y esta
  limitación queda explícita en el contrato.
- **Representar paths como segments de feature 3:** descartado para la
  declaration opaca actual; obligaría a compartir el AST de input. Se conserva
  una string con gramática cerrada y compilación temprana.
- **Coerción de strings a números:** descartada; ocultaría incompatibilidades y
  haría depender el resultado de reglas implícitas.
- **Tipo incompatible como inconclusive:** descartado; hay evidencia confirmada
  suficiente para afirmar que la condición tipada no se cumple.
- **Path ausente como falta de evidencia global:** descartado. El output
  confirmado prueba la ausencia del path; solo carecer del output/evidence
  terminal vuelve inconclusa la evaluación.
- **Short-circuit de grupos:** descartado porque omitiría resultados hijos
  requeridos para auditoría.
- **Guardar resultados en el evaluator:** descartado; introduciría estado,
  sincronización y una segunda fuente de verdad.
- **Usar `Metadata` opaco como contrato definitivo:** descartado; R6 exige un
  resultado verificable y tipado.

## Conformidad arquitectónica

El diseño respeta ARCH-003/004 porque deriva estados exclusivamente de evidencia
confirmada y reglas deterministas; ARCH-006 al propagar contexto; ARCH-010 al
permitir tests sin integraciones; ARCH-012 porque no comparte estado mutable; y
la separación planning/assertion conserva interfaces en el consumidor. Respeta
CONV-001 a CONV-010 y no solicita excepciones.

## Mapa R → componente/task

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T6 | Definitions completas se validan antes de cualquier efecto. |
| R2 | T2, T3, T6 | Paths cumplen la gramática y se resuelven sin coerción. |
| R3 | T3, T6 | Existencia/ausencia distinguen null de missing. |
| R4 | T3, T6 | Igualdad y comparaciones aplican tipos/números explícitos. |
| R5 | T1, T4, T6 | Grupos anidados siguen tablas ternarias y orden estable. |
| R6 | T1, T4, T5, T6 | Cada resultado conserva contenido y evidencia terminal. |
| R7 | T2, T4, T5, T6 | Invalid, failed e inconclusive permanecen separados. |
| R8 | T5, T6 | El puerto integra el motor y deriva steps sin modelo. |
| R9 | T4, T5, T6 | Cancelación detiene evaluación/plan sin fabricar estados. |
