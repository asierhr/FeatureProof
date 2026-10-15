# Suite de evaluación del agente — Design

## Resumen de la solución

Se añadirá un paquete de aplicación `internal/agenteval` que carga un dataset
local inmutable, ejecuta cada caso contra `agentplanning.Generator`, evalúa el
resultado con oracles estructurales y publica métricas y artefactos
reproducibles. El comando `featureproof eval agent` será composición y parsing;
no poseerá reglas de scoring.

```text
dataset v1 + criteria v1 + model source
                 │ strict load, hashes, limits
                 ▼
          immutable EvaluationSuite
                 │ cases in manifest order
                 ▼
       agentplanning.Generator.Generate
                 │
       ┌─────────┼──────────────┐
       ▼         ▼              ▼
 PreparedPlan  MissingContext  typed rejection/error
       └─────────┼──────────────┘
                 ▼
 structural oracle + existing validators
                 │ findings + integer metrics
                 ▼
      optional compatible baseline compare
                 │ absolute criteria + deltas
                 ▼
   staged bundle ── manifest published last
                 │
                 ▼
      exit 0 pass / 1 quality / 2 error / 3 cancel
```

La suite no construye `planning.Executor`, herramientas, targets ni evidence
stores. El único I/O externo opcional es el provider de modelo elegido
explícitamente en live mode y compuesto a través de la feature 9.

## Reglas arquitectónicas aplicadas

- `ARCH-001`, `ARCH-002`, `ARCH-011` y `ARCH-012`: el modelo solo propone;
  evals no entregan tools ni ejecutan acciones.
- `ARCH-004`: el veredicto de la eval se calcula con propiedades y métricas
  deterministas, no con texto del modelo.
- `ARCH-005` y `ARCH-006`: datasets, casos, bytes, llamadas, tokens, tiempo,
  trabajo y publicación están limitados y reciben context.
- `ARCH-008`: OpenAPI, constraints y fragmentos externos permanecen en
  `StructuredRequest.Untrusted`.
- `ARCH-009`: fixtures, diagnósticos y bundles se validan/escanean; las
  credenciales solo existen tras la frontera de composición de feature 9.
- `ARCH-010`: tests y replay no usan red ni modelos reales.
- `CONV-003` a `CONV-008`: puertos mínimos del consumidor, context propagado,
  errores tipados, clock/filesystem inyectables y tests sin sleeps.
- `CONV-009`: cada requirement tendrá test nominal trazable.

No se solicita ninguna excepción arquitectónica.

## Paquetes y áreas afectadas

```text
internal/agenteval/                  # formatos, loader, runner, oracle, metrics,
                                     # criteria, comparison y bundle writer
internal/agenteval/testdata/         # datasets/replays inválidos y canarios falsos
evals/agentplanning/v1/              # dataset inicial, casos y criterios explícitos
cmd/featureproof/                    # subcomando `eval agent` y composición
internal/model/ + openai/            # reutilizados, sin nuevo contrato de provider
internal/agentplanning/              # reutilizado sin cambiar generación/prompt
```

El implementer puede separar archivos dentro de `internal/agenteval`, pero no
crear capas vacías. Cualquier cambio de comportamiento en `agentplanning` exige
volver a spec; esta feature solo lo consume y lo envuelve para captura.

## Formatos de entrada

### `dataset.json` v1

```json
{
  "schema_version": 1,
  "dataset_id": "agent-plan-core",
  "dataset_version": "1.0.0",
  "cases": [
    {"id": "valid-list", "file": "cases/valid-list.json", "sha256": "..."}
  ]
}
```

Los paths son relativos al root real y autorizado. El loader abre sin seguir
escapes, verifica tipo regular, tamaño y digest, y conserva los bytes. El digest
del dataset incluye el manifest lógico y los digests de casos/fixtures, no sus
paths absolutos.

### Caso v1

Un caso contiene:

- identidad, categoría y tags ordenados;
- constraints con IDs y descripciones;
- referencia/digest a OpenAPI local, selecciones constraint→operation y
  propiedades esperadas de proyección;
- descriptors con nombre, metadata y `contract_id` local versionado;
- límites completos de `agentplanning.GenerationRequest`;
- fragmentos externos sintéticos `openapi`, `log` o `response` y su ubicación
  permitida dentro de los campos ya no confiables;
- outcome esperado;
- required/allowed constraints, scenarios, operations, capabilities,
  identities y evidence sources;
- categoría/etapa estructural aceptada para rechazo;
- límites esperados de llamadas, tokens, duración, replans y trabajo.

No se serializa `CapabilityDescriptor.Validate`. `ContractRegistry`, interfaz
consumidora en `agenteval`, resuelve un `contract_id` conocido a una función
local descriptor-only. Un ID desconocido falla antes del modelo.

Los fragmentos `log` y `response` se insertan, con etiqueta de origen, en una
descripción de constraint o campo de schema señalado por el fixture. Siguen
siendo texto externo dentro del envelope no confiable existente; no se añade un
campo a `GenerationRequest` ni se altera el prompt.

### `criteria.json` v1

```json
{
  "schema_version": 1,
  "criteria_id": "release-gate",
  "criteria_version": "1.0.0",
  "minimum_ppm": {
    "constraint_coverage": 0,
    "plan_validity": 0,
    "operation_selection": 0,
    "insufficient_context": 0,
    "limit_compliance": 0
  },
  "hard_fail_categories": [],
  "max_negative_delta_ppm": {},
  "fail_fast": false,
  "run_limits": {}
}
```

El ejemplo ilustra forma, no valores recomendados. El archivo real del dataset
debe declarar todos los thresholds, categorías y límites; no se completan
campos ausentes. Los deltas se expresan por las mismas categorías métricas y de
invención. La policy valida rangos, duplicados y contradicciones antes de cargar
credenciales o ejecutar casos.

## Componentes y responsabilidades

### Loader y modelos inmutables

- `Loader.Load(ctx, root, manifestPath, limits)`: lee, verifica, decodifica y
  snapshottea dataset/casos/fixtures.
- `CriteriaLoader.Load`: valida criterios completos y produce una policy
  inmutable.
- `ContractRegistry`: traduce IDs wire allowlisted a validators descriptor-only.
- `Suite`, `Case` y `Criteria`: valores privados o getters con copias
  defensivas; no exponen bytes mutables.
- `EvalError`: categoría, stage y path seguro; conserva solo cancelación y
  deadline como causes observables.

### Runner

`Runner.Run(ctx, Suite, Criteria, CandidateSource)` posee una ejecución. Recorre
casos secuencialmente, crea IDs estables derivados de dataset/case/run, compone
un `GenerationRequest` y llama exactamente una vez a `Generator.Generate` por
caso admitido. La ejecución continúa o corta según `fail_fast`, pero nunca
reintenta un caso.

`CandidateSource` crea el `PlanningModel` apropiado:

- `ReplaySource`: respuestas/usage locales identificadas y verificadas por
  digest; no red.
- `LiveSource`: una `model.Session` de feature 9 compartida por el run, con
  provider/model/profile/budgets explícitos. El perfil permanece en la
  composición y solo el transporte resuelve tokens.

Un wrapper `CapturingPlanningModel` observa el `StructuredRequest` real enviado
por `Generator`. Registra prompt text/version/hash y comprueba que canarios del
caso no están en `Instructions`. Solo entrega al manifest el prompt privilegiado
allowlisted; el payload `Untrusted` se usa en memoria para checks y se descarta.
Esto evita modificar la constante o el contrato de feature 10.

### Oracle estructural

El oracle recibe `CaseExpectation`, resultado/error tipado, captura de frontera,
`PreparedPlan` y snapshots de budgets. Sus reglas son puras y producen
`[]Finding` ordenado por categoría, case ID y path.

- **Endpoint invention:** cada `OperationReference` debe estar en
  `ReducedContext.RetainedOperations` para la constraint y en `allowed`.
- **Capability invention:** nombre exacto presente en descriptor catalog y
  allowed set.
- **Identity invention:** valores en ubicaciones tipadas por el caso deben
  pertenecer al conjunto sintético permitido; nunca se infieren de texto.
- **Credential invention:** campos/valores con clasificación prohibida o
  canarios provocan finding; el valor no se copia.
- **Evidence invention:** `step_id` y `assertion_id` deben existir en el mismo
  check y en expectativas permitidas.

Si el generador rechaza antes de entregar un plan, el oracle compara categoría,
stage y path allowlisted con el outcome `rejected`. No inspecciona el texto de
error.

### Métricas

Cada métrica almacena enteros exactos:

- `constraint_coverage`: constraints requeridas cubiertas por al menos un check
  estructuralmente válido / constraints requeridas de casos `plan`.
- `plan_validity`: casos `plan` que devuelven `PreparedPlan` y satisfacen sus
  invariantes / casos `plan`.
- `operation_selection`: casos `plan` cuyo conjunto canónico de pares
  constraint/method/path coincide exactamente con `expected.operations` /
  casos `plan` donde esa expectativa es aplicable. También publica conteos
  selected/expected/missing/extra como diagnóstico, sin alterar la fórmula.
- `insufficient_context`: casos `plan|insufficient_context` clasificados en la
  rama esperada / casos con uno de esos dos outcomes esperados, junto con
  TP/FP/TN/FN. Los casos `rejected` no entran en este denominador.
- `limit_compliance`: casos que permanecen dentro de todos sus límites
  declarados / casos completados o fallidos con accounting disponible.

Las métricas de invención publican conteos por `endpoint`, `capability`,
`identity`, `credential` y `evidence`; no se convierten en score textual. El
agregador usa aritmética comprobada y ppm enteros. Denominador cero produce
`applicable=false`.

### Criterios y verdict

`Evaluator` aplica en orden:

1. integridad/completitud de resultados;
2. hard-fail categories;
3. minimum ppm para métricas aplicables;
4. comparación baseline, si existe;
5. ausencia de errores de sistema.

El verdict incluye razones mediante IDs de criterio, categoría y case IDs, sin
payload. No hay valores por defecto. Umbrales bajos son una decisión visible en
el archivo, no una relajación oculta del sistema.

### Baseline comparator

`Comparator.Compare(ctx, candidate, baseline, Criteria)` valida primero todos
los hashes y compatibilidad. Dataset y casos deben ser idénticos; prompt/modelo
pueden diferir porque son dimensiones candidatas. Planning protocol y schemas
de metrics deben coincidir. Compara:

- ppm y contadores agregados por categoría;
- hard-fail counts;
- outcome/findings por case ID.

Produce deltas enteros, categorías `improved|unchanged|regressed` y casos
afectados. Un cambio de dataset requiere crear otra baseline, no forzar la
comparación.

### Artifact writer

El writer usa un staging directory exclusivo dentro del output autorizado.
Escribe cada archivo mediante temporal, flush/sync, close y rename. Calcula
hashes sobre bytes finales. `manifest.json` se escribe y renombra al final con
`status=complete`; solo entonces el bundle es baseline-eligible.

El bundle contiene:

```text
<output>/<run-id>/
  cases.jsonl
  metrics.json
  comparison.json   # solo con baseline
  manifest.json     # completion marker, último
```

`manifest.json` registra identificadores/versiones/digests, prompt capturado,
provider/model, safe config/budgets, timestamps, status/verdict, resumen de
casos y hashes de archivos. Omite el auth profile ID y solo registra
`authentication: configured` en live mode. No persiste raw request/output.

`result_digest` se calcula sobre resultados, findings y métricas canónicos,
excluyendo timestamps, run ID aleatorio, durations observadas, output path y
metadata de autenticación. Esto permite comparar reproducibilidad de replay.

## Flujo principal

1. Parsear subcomando y validar presencia de paths/source/live opt-in.
2. Cargar criteria y límites; ningún default completa campos ausentes.
3. Cargar/snapshottear dataset, casos, OpenAPI y replays; verificar hashes.
4. Si se indicó baseline, verificar bundle completo y compatibilidad básica.
5. Componer replay o, tras todo preflight, sesión live de feature 9.
6. Para cada caso en orden: construir reduced context/catalog, capturar frontera,
   generar una vez y recoger outcome/accounting.
7. Ejecutar oracle puro y agregar métricas enteras.
8. Comparar baseline, si aplica.
9. Aplicar criterios absolutos y de regresión.
10. Publicar bundle en staging y manifest al final.
11. Devolver exit code según verdict/categoría.

## Flujos de error

- Input/schema/hash/path inválido: `invalid_input`, `unsupported_version` o
  `integrity_failure`; cero modelo y exit 2.
- Límite preflight/traversal/artifact: `limit_exceeded`; no se publica pass.
- Rechazo esperado del generador: resultado calificable, no error de sistema.
- Modelo/provider falla: case failure seguro; criterios deciden continuidad,
  el run termina con exit 2 porque no es una medición completa.
- Threshold/hard-fail/regresión: bundle completo `failed`, exit 1.
- Baseline corrupta/incompatible: cero comparación calificable, exit 2.
- Cancelación/deadline: detener admisión, cerrar recursos, staging no completo,
  exit 3.
- Error de writer: no manifest completo, exit 2.

## Estado, concurrencia, cancelación y límites

El `Runner` posee resultados y acumuladores de un run. v1 es secuencial para
que el presupuesto compartido, fail-fast y orden de resultados sean
reproducibles. No lanza goroutines. Loader, generator, oracle, comparator y
writer reciben context y comprueban cancelación entre unidades acotadas.

Todos los límites se validan antes de multiplicar/sumar. Además de los límites
de generation, criteria declara máximos de archivos, bytes totales, casos,
bytes de replay/resultado/artifact y duración total. Live mode declara los
budgets de feature 9 y cada caso consume como máximo una call; `MaxReplans`
debe ser explícito aunque la suite no reserve ninguno.

No hay retry. Una repetición es otro run y otro bundle. Replay con los mismos
inputs es lógicamente idempotente; publicación usa un nuevo destino y nunca
sobrescribe un bundle completo.

## Confidencialidad

El dataset solo admite canarios sintéticos señalados. Antes de publicar se
escanean todos los valores serializados contra los canarios del suite y los
marcadores de auth entregados por composición. Findings guardan categoría y
path, no valor. Errores externos se traducen a tipos seguros. La captura del
modelo conserva en artefactos únicamente el prompt privilegiado aprobado;
`Untrusted` y output crudo permanecen en memoria el tiempo necesario para
decode/scoring.

## Comando

El entrypoint será `featureproof eval agent`. Argumentos obligatorios comunes:

```text
--dataset <local dataset.json>
--criteria <local criteria.json>
--output <local directory>
--source replay|live
```

Replay exige archivo/manifest local de respuestas y digest. Live exige además
`--live`, provider, model, auth profile y todos los budgets/I/O limits positivos.
No existe selección por defecto. Parsing y preflight ocurren antes de resolver
el perfil OAuth. El comando no se invoca desde `TestMain`, `go generate`, init,
la suite canónica de Go ni otro flujo implícito.

Códigos:

| Código | Significado |
|---:|---|
| 0 | evaluación completa y aprobada |
| 1 | medición completa, pero threshold/hard-fail/regresión falló |
| 2 | configuración, integridad, modelo, sistema o publicación falló |
| 3 | cancelación o deadline |

## Estrategia de pruebas

- Loaders: schemas, unknown fields, duplicate keys/IDs, semver, hashes, escapes,
  symlinks, remotos, límites y snapshots.
- Runner: orden, one-call-per-case, fail-fast, cancelación y cero handles/efectos.
- Oracle: una prueba positiva y negativa por invention category, rejection
  esperado y texto del modelo ignorado.
- Metrics: fórmulas, cero denominator, overflow, estabilidad y digest canónico.
- Injection: canarios distintos en OpenAPI/log/response, captura de canales,
  omisión del reducer y propuesta maliciosa.
- Criteria/exit: cada combinación de threshold, hard-fail, delta y códigos.
- Baseline: compatible, regresión por categoría, corrupción, drift e
  incompatibilidad.
- Writer: orden de commit, hashes, fallos inyectados y ausencia de complete.
- CLI: replay offline, live incompleto sin resolver auth y composición live
  falsa con selección/budgets explícitos.
- Integración hermética: dataset inicial completo sobre modelo scripted y
  escaneo de canarios en todos los artefactos.

Los tests usan filesystem temporal, fake clock, transports/modelos scripted y
barreras por context/canales, nunca `time.Sleep`. La prueba live es una acción
manual del comando y queda fuera de `go test`.

## Comentarios de responsabilidad sistémica

Toda entidad, interfaz, método, función y helper nuevo o modificado —exportado,
interno o de test— llevará un comentario que explique su responsabilidad dentro
de FeatureProof. Los comentarios describirán invariantes, fronteras o propósito,
no repetirán la sintaxis. Esta obligación se implementa en cada task y se
verifica en review.

## Decisiones y alternativas descartadas

- **Thresholds hardcoded:** descartados; impedirían versionar distintas gates y
  convertirían una decisión de producto en comportamiento oculto.
- **Juez LLM:** descartado; sería no determinista y permitiría que una conclusión
  textual sustituyera validación estructural.
- **Ejecutar planes para medirlos:** descartado; ampliaría alcance y podría tocar
  producción.
- **Deserializar `ReducedContext`:** descartado; perdería el índice confiable de
  procedencia. Se reconstruye mediante el inspector local.
- **Comparar datasets distintos:** descartado; mezcla cambio de benchmark con
  cambio de candidato.
- **Paralelismo v1:** descartado; haría no determinista la admisión de budgets y
  el fail-fast sin aportar al contrato actual.
- **Guardar prompts/respuestas completas:** se conserva el prompt privilegiado
  exigido y su hash; payload/output no confiables se excluyen para reducir
  exposición y evitar secretos.

## Trazabilidad inicial

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | `Loader`, canonical digest | `TestLoaderAcceptsCanonicalVersionedDataset`, `TestLoaderRejectsIntegrityAndPathEscapes` |
| R2 | case decoder, `ContractRegistry`, OpenAPI builder | `TestCaseRequiresConsistentStructuralExpectations` |
| R3 | `Runner`, dependency boundary | `TestRunnerNeverComposesOrExecutesApplicationActions` |
| R4 | `Oracle` | `TestOracleDetectsEveryInventionCategoryWithoutValues` |
| R5 | metrics/aggregator | `TestMetricsUseExactStructuralNumeratorsAndStablePPM` |
| R6 | capture wrapper/injection fixtures | `TestInjectionCanariesRemainOnlyInUntrustedChannel` |
| R7 | manifest/canonicalizer/writer | `TestBundlePublishesManifestLastAndLeaksNoCanaries` |
| R8 | criteria/evaluator/exit mapping | `TestCriteriaDriveThresholdHardFailAndExitCodes` |
| R9 | comparator | `TestComparatorReportsCategoryRegressionsAndRejectsDrift` |
| R10 | CLI/replay/live composition | `TestEvalAgentRequiresExplicitSourceAndLiveSelection` |
| R11 | runner limits/context/checked math | `TestRunnerStopsOnCancellationAndOverflowWithoutCompleteBundle` |
| R12 | hermetic integration and process boundary | `TestHermeticEvaluationNeedsNoNetworkCredentialsOrTarget` |
