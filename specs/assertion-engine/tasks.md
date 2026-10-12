# Motor determinista de assertions — Tasks

- [x] T1. Definir mediante tests el AST compilado y los contratos tipados de resultado. [R1, R5, R6]
  - Añadir estados, valores con presencia explícita, resultados hoja/grupo,
    causas y árbol de hijos en declaration order.
  - Ampliar `planning.StepEvaluation` con resultados tipados y actualizar los
    fakes existentes sin cambiar la semántica de feature 3.
  - Verificar copias inmutables de expected, observed, definitions y output.
  - Comentar toda entidad y función nueva o modificada conforme al design.

- [x] T2. Compilar y validar definitions completas durante la validación del plan. [R1, R2, R7]
  - Escribir tests para variantes hoja/grupo, combinación exacta de keys,
    operadores, expected requerido/prohibido, grupos vacíos y nodos anidados.
  - Añadir errores tipados con categoría, field path estable y causa conservada.
  - Introducir el puerto consumidor `AssertionDefinitionValidator` en planning e
    integrarlo antes de policy/tool/evidence; actualizar constructores y fakes.
  - Cubrir valores no JSON compatibles, ciclos de estructuras y primer error
    determinista sin depender de iteración de maps.

- [x] T3. Implementar paths, existencia, igualdad y comparadores numéricos. [R2, R3, R4]
  - Probar raíz, objetos, arrays, escapes `~0`/`~1`, casing, índices inválidos,
    fuera de rango, traversal sobre escalares y null presente.
  - Clasificar tokens de apariencia numérica durante el parseo y rechazar
    globalmente signos y ceros iniciales, incluso si podrían nombrar una key de
    objeto, sin depender del tipo runtime.
  - Implementar resolver `{Present, Value}` sin mutar el output.
  - Probar igualdad estricta de null/bool/string/number/array/object, incluido
    orden de arrays y orden irrelevante de object keys.
  - Implementar números finitos exactos y `gt/gte/lt/lte` sin coerciones;
    probar tipos incompatibles, enteros grandes, `json.Number`, NaN e infinito.

- [x] T4. Evaluar árboles `all`/`any` y producir resultados completos. [R5, R6, R7, R9]
  - Escribir tests de las tablas ternarias, grupos anidados y varias roots.
  - Demostrar que todos los hijos se evalúan depth-first en declaration order y
    aparecen en el resultado sin short-circuit.
  - Incluir operador, path de nodo, expected/observed, estado, hijos, causa y
    evidence ID terminal en cada resultado.
  - Probar cancelación antes y durante el recorrido sin resultados funcionales
    fabricados ni `time.Sleep`.

- [x] T5. Implementar e integrar `planning.StepAssertionEvaluator`. [R6, R7, R8, R9]
  - Agregar roots con semántica `all` para derivar `passed`, `failed` o
    `inconclusive` del step sin modelo.
  - Usar únicamente el output confirmado y el último evidence ID; probar varios
    intentos, evidence IDs vacíos y output nil.
  - Propagar errores internos/cancelación al executor y mantener separados los
    resultados funcionales.
  - Añadir integración con un plan que haga pasar, fallar y bloquear steps y
    confirmar que la agregación/propagación de feature 3 no regresa.

- [x] T6. Verificar trazabilidad, compatibilidad y calidad de la feature. [R1, R2, R3, R4, R5, R6, R7, R8, R9]
  - Ejecutar tests herméticos de todos los operadores, tipos incompatibles,
    paths inexistentes, grupos anidados y errores de configuración.
  - Ejecutar `gofmt`, `go vet`, compilación, `go test ./...`, race detector y
    coverage según `docs/verification.md`; alcanzar al menos 85 % en código
    nuevo/modificado sin reducir coverage global sin justificación.
  - Confirmar que harness, specification y multi-step planning mantienen sus
    contratos y que ninguna prueba usa red ni modelo real.
  - Registrar en `progress/assertion-engine/implementation.md` el mapa R→test,
    tasks, comandos, coverage, archivos modificados y limitaciones.

