# Generación agéntica de planes de validación — Design

## Resumen de la solución

Se añadirá un paquete de aplicación `internal/agentplanning` que posee el
protocolo de generación v1, coordina una sola inferencia y convierte una
propuesta no confiable en un conjunto inmutable de planes existentes. La salida
del modelo nunca es un plan ejecutable por sí sola.

```text
validated constraints + ReducedContext + capability descriptors
                     │ snapshot + preflight limits
                     ▼
              planning model port ── one bounded inference
                     │ strict protocol v1
          ┌──────────┴───────────┐
          │                      │
 insufficient_context      candidate plan
   validate missing          schema/provenance
        items                     │
          │                 canonical dedupe
          │                      │
          │             materialize planning plans
          │                      │
          │          graph/assertion + registry + policy
          │                      │
          ▼                      ▼
 typed missing context      PreparedPlan
          (no execution, no persistence, no approval)
```

`harness.ModelDecision` v1 permanece como protocolo incremental del harness.
La feature introduce `PlanningDecision` v1 porque un plan completo y un
resultado de contexto insuficiente tienen invariantes incompatibles con
`propose_plan`/`complete_analysis`. El runtime de feature 9 se amplía con una
llamada estructurada genérica y acotada; `Session.Decide` continúa adaptando el
protocolo del harness como hasta ahora.

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Alcance de feature 10 y puerta SDD. |
| `.codex/agents/spec.md` | Artefactos, EARS, bloqueo y salida del spec author. |
| `docs/architecture.md` | Modelo no autoritativo, datos no confiables, registry/policy y cero ejecución directa. |
| `docs/conventions.md` | Interfaces consumidoras, context, errores tipados, secretos, tests y comentarios. |
| `docs/specs.md`, `docs/verification.md` | Trazabilidad R→T→test y verificación canónica. |
| Specs 2, 3 y 4 | Restricciones internas, `ValidationPlan`, grafo, referencias y assertions deterministas. |
| Specs 6 y 8 | Proyección tratada y `ReducedContext` canónico/acotado/no confiable. |
| Spec 9 | Runtime neutral, Responses API, OAuth aislado, presupuestos y telemetría segura. |
| `internal/specification` | `Constraint` validada, identificadores y target fuera del modelo. |
| `internal/planning` | Modelo ejecutable existente y `ValidatedPlan` inmutable. |
| `internal/openapi` | Operaciones normalizadas y bytes reducidos; hoy falta exponer el índice retenido. |
| `internal/harness`, `internal/tools`, `internal/policy` | Contratos existentes y razón para no entregar handles ejecutables al generador. |
| `internal/model`, `internal/model/openai` | `Session.Decide` y decoder OpenAI actualmente fijados a `ModelDecision` v1. |

No se necesita un contrato externo nuevo. La integración OpenAI sigue el
contrato Responses API y Sign in with ChatGPT ya aprobado y documentado en la
feature 9; esta feature cambia el protocolo interno consumido sobre ese mismo
transporte.

## Contratos propuestos

### Solicitud de generación

`GenerationRequest` contiene IDs de run/plan, snapshots de constraints,
`openapi.ReducedContext`, `[]CapabilityDescriptor` y `Limits`. El catálogo
describe nombre exacto, clase de capacidad, contrato de input, efectos e
idempotencia. No contiene `harness.Tool`, credenciales, target base URL ni
callbacks.

Para HTTP, el modelo referencia un `openapi.OperationKey`; no escribe `method`,
`path` ni URL en el input libre. El materializador recupera método y template
del índice confiable, exige bindings tipados para cada parámetro de path,
expande/escapa segmentos sin permitir separadores y añade query/headers/body
únicamente según el contrato descriptor y OpenAPI. La versión v1 no contiene
campos de comandos ni credenciales. Un catálogo explícito puede estar vacío;
en ese caso solo puede producirse contexto insuficiente, nunca un plan.

### Protocolo `PlanningDecision` v1

La unión cerrada tiene:

- `version: 1`;
- `kind: plan | insufficient_context`;
- exactamente uno de `plan` o `missing_context`.

Un `CandidatePlan` contiene ID, matriz de escenarios y checks. Cada
`CandidateCheck` contiene ID, un `constraint_id`, escenario, capability,
referencia opcional tipada a operación OpenAPI, pasos, dependencias,
assertions y `EvidenceRequirement` estructurados. La matriz registra para cada
constraint y cada escenario `planned` o `not_applicable`; este último exige una
razón acotada.

`EvidenceRequirement` referencia `step_id` y una lista no vacía de
`assertion_ids`. No acepta prosa como sustituto de una fuente verificable.

`MissingContext` contiene items ordenados con kind `question` o `evidence`, IDs
de constraints existentes y una petición segura/acotada. Este tipo es resultado
de dominio, no un error de proveedor.

Los DTO externos de OpenAI permanecen privados. El decoder usa `UseNumber`,
rechaza claves duplicadas, campos desconocidos y trailing data antes de crear
modelos internos.

### Resultado preparado

`PreparedPlan` conserva una copia inmutable del plan canónico, sus evidence
requirements y un `planning.ValidatedPlan` por check. Solo su constructor
interno puede crearlo después de todas las validaciones. No implementa una
interfaz de ejecución y no representa aprobación humana.

## Componentes y responsabilidades

### `internal/agentplanning`

- `Generator`: propietario de una solicitud; snapshot, inferencia, validación,
  deduplicación, materialización, policy y observación terminal.
- `PlanningModel`: interfaz mínima del consumidor que solicita una respuesta
  JSON estructurada bajo context y límites; no conoce providers.
- `ProtocolDecoder`: valida la unión v1 y traduce JSON a candidatos internos.
- `Normalizer`: ordena campos, calcula fingerprints y elimina equivalentes
  conservando la primera declaración.
- `ProvenanceValidator`: comprueba constraints, escenarios, operaciones
  retenidas, capabilities y ausencia de valores prohibidos.
- `Materializer`: deriva inputs confiables y construye
  `planning.ValidationPlan` sin inventar datos.
- `CapabilityCatalog`: snapshot descriptor-only y validador de input; nunca
  resuelve implementaciones.
- `GenerationPolicy`: policy read-only/default-deny sobre `PlanIntent`
  inmutable; retorna allow/deny y razón segura.
- `Observer`: acepta un único `GenerationRecord` terminal payload-free.

### Evolución mínima de `internal/openapi`

`ReducedContext` mantendrá internamente el índice exacto de operaciones que
quedaron en el envelope después del límite de tamaño y expondrá una copia con
la relación constraint→operation. El caller no puede reconstruir una operación
omitida mirando el catálogo completo. Bytes e índice se producen en la misma
operación y permanecen aislados de mutaciones.

### Evolución mínima de `internal/planning`

El validador separará la comprobación descriptor-only de existencia/contrato
de capability de la resolución de una implementación. Se preservará el
constructor existente mediante un adapter compatible para callers actuales.
La validación de grafo, referencias y assertions no cambia.

### Evolución compatible de `internal/model`

El runtime añadirá un contrato `StructuredRequest`/`StructuredResult` acotado y
un método de sesión que consume la misma reserva de llamada, tokens, deadline y
telemetría de feature 9. El caller aporta instrucciones privilegiadas,
contenido no confiable y un decoder/validator consumidor; no aporta endpoint,
credenciales ni tools.

`Session.Decide` se implementará sobre esa primitiva o un núcleo común, pero su
firma y semántica quedan intactas. La sesión de planificación se adapta al
puerto `agentplanning.PlanningModel` en composición.

### Evolución de `internal/model/openai`

El adapter separará:

1. construcción de un Responses request a partir de un structured request;
2. transporte autenticado existente;
3. consumo SSE bounded hasta `response.completed`;
4. devolución de un único output JSON y usage;
5. decoder específico elegido por el consumidor.

El decoder del harness y el decoder de planificación permanecen distintos. No
se duplica endpoint, OAuth, streaming, clasificación de errores o accounting.

## Fingerprint y orden canónico

El fingerprint se calcula sobre una serialización canónica que excluye IDs
decorativos e incluye constraint, escenario, capability, operation key, inputs
materializables, grafo relativo, assertions y evidence requirements. Las keys
de mapas se ordenan y los números JSON conservan su representación válida.

Se recorre en orden de constraints de la request, orden fijo de escenarios
`positive`, `negative`, `error` y orden de declaración. El primer fingerprint
se conserva. Después de deduplicar se revalida cobertura de escenarios y
evidence requirements; nunca se acepta un plan debilitado por normalización.

## Cadena de validación y frontera de seguridad

1. Preflight de request/config/límites, sin modelo.
2. Una inferencia bounded mediante feature 9.
3. Decode estricto del protocolo.
4. Si es contexto insuficiente, validar y devolver sin registry/policy.
5. Si es plan, validar constraints, matriz, procedencia y valores prohibidos.
6. Normalizar/deduplicar y revalidar cobertura.
7. Materializar un `planning.ValidationPlan` por check.
8. Validar cada plan con grafo/assertions y el catálogo descriptor-only.
9. Evaluar policy read-only por check en orden estable, recogiendo denials.
10. Crear `PreparedPlan`, observar un terminal seguro y devolver.

Un fallo descarta el conjunto completo. La futura ejecución deberá resolver
tools reales y volver a aplicar la policy vigente; el tipo preparado no permite
saltarse esos controles.

## Flujos de error

- Request inválida o límite preflight: error tipado y cero inferencias.
- Fallo/cancelación/timeout del modelo: se conserva la taxonomía segura de
  feature 9 y se emite terminal de generación.
- JSON/protocolo inválido: `invalid_model_response`, sin interpolar payload.
- Contexto insuficiente válido: resultado terminal no erróneo, sin plan.
- Referencia/contexto inventado: `unknown_context_reference` o
  `semantic_rejection` antes de registry/policy.
- Grafo/assertion/input inválido: traducción estable del `planning.PlanError`.
- Registry descriptor denegado o policy denied: resultado completo descartado.
- Observer fallido: error interno fail-closed; no se entrega el plan.

## Propiedad del estado, concurrencia y cancelación

Cada llamada a `Generate` posee su snapshot, candidato y buffers. El `Generator`
es reutilizable porque no guarda estado de request. Catálogos y plans se copian
en construcción/retorno. El estado mutable compartido se limita a la sesión de
modelo de feature 9, que ya serializa reservas con mutex.

No se lanzan goroutines. Context es primer parámetro y se comprueba entre
unidades de traversal, antes/después de inferencia y en policy/observer. Una
cancelación devuelve cero resultado parcial. El observer terminal puede usar
un contexto de finalización acotado y solo recibe metadata tratada.

## Límites, retries e idempotencia

`Limits` define máximos positivos de input/output bytes, constraints,
capabilities, checks, steps por check, assertions por step, missing items y
unidades totales de validación. La serialización hacia el modelo usa el mínimo
entre límites del generador, sesión y adapter.

Una generación hace una llamada al modelo. No reintenta ni repara. Repetir
desde el caller consume otra reserva de feature 9 y produce otro terminal. No
se reserva `replan`: esa transición pertenece a feature 13. La operación es
funcionalmente idempotente con fake/model output idéntico, pero cada intento
tiene telemetría y budget propios.

## Observabilidad y seguridad

`GenerationRecord` contiene run ID, plan ID, outcome/category, provider/model
ya expuestos por la sesión, duración y contadores agregados de constraints,
checks y steps. Omite descriptions, bytes OpenAPI, schemas, inputs, assertions,
preguntas, evidence requests, prompts, output, perfiles y tokens.

Los errores contienen categoría, stage y path estructural generado por el
sistema. Nunca contienen keys/values externos. Los textos de razones
`not_applicable` y missing-context solo llegan al resultado explícito y no a
logs/telemetría. Todos los inputs al modelo se etiquetan como untrusted data; la
policy y los validators ignoran instrucciones contenidas en ellos.

## Paquetes afectados

```text
internal/agentplanning/       # protocolo, generador, validación, modelos y fakes
internal/openapi/             # índice inmutable de operaciones retenidas
internal/planning/            # validación descriptor-only compatible
internal/model/               # inferencia JSON estructurada con budgets existentes
internal/model/openai/        # núcleo Responses/SSE compartido y codecs separados
```

No se necesita un entrypoint en `cmd/`, cambio a la specification YAML, ni
integración con CLI en esta feature.

## Convención de comentarios funcionales

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro del sistema y su
relación con la frontera de generación, validación o modelo. El comentario
explicará intención, invariantes o motivo arquitectónico; no repetirá la
sintaxis. Esta obligación incluye helpers, DTOs internos, fakes y funciones
modificadas en paquetes de features anteriores.

## Estrategia de pruebas

Los tests usan un `PlanningModel` fake que devuelve JSON exacto, contextos
OpenAPI construidos localmente, catálogo descriptor-only, validators y policy
falsos con contadores/barreras. El adapter OpenAI usa transporte SSE falso. No
hay red, credenciales, sleeps ni aplicación desplegada.

| Requirement | Tests previstos |
|---|---|
| R1 | `TestGenerateRejectsInvalidRequestBeforeModel`; `TestPlanningProtocolV1StrictDecodeAndSnapshot` |
| R2 | `TestPlanRequiresExactConstraintCoverageAndEvidenceSources`; `TestMaterializesOneConstraintPerCheck` |
| R3 | `TestScenarioMatrixRequiresPlannedOrNotApplicable`; `TestEquivalentChecksDeduplicateCanonicallyAndRevalidateCoverage` |
| R4 | `TestHTTPProposalMustReferenceRetainedOperation`; `TestRejectsInventedToolURLCommandAndCredentialFields` |
| R5 | `TestValidationStagesRunInOrderBeforePreparedPlan`; `TestPolicyDenialReturnsNoPreparedSubset` |
| R6 | `TestInsufficientContextReturnsTypedQuestionsAndEvidence`; `TestInvalidMissingContextIsRejectedSafely` |
| R7 | `TestGenerationHasNoExecutableDependenciesOrSideEffects` |
| R8 | `TestGenerationPropagatesCancellationAndAllLimits`; `TestGenerationNeverRetriesOrReservesReplan` |
| R9 | `TestUntrustedInputsRemainDataInProviderRequest`; `TestInjectedInstructionsCannotBypassProtocol` |
| R10 | `TestGenerationErrorsAndTerminalTelemetryAreTypedAndCanaryFree` |
| R11 | `TestStructuredPlanningCallSharesOpenAIRuntimeWithoutBreakingHarnessProtocol`; existing feature 9 suite |
| R12 | `TestPlanGenerationEndToEndWithFakes`; full race and coverage suite |

Además se repiten los tests existentes de `internal/harness`, `internal/model`,
`internal/model/openai`, `internal/openapi` e `internal/planning` para demostrar
compatibilidad.

## Alternativas y decisiones

- Reutilizar `harness.ModelDecision`: descartado porque expresa una decisión
  incremental, no cobertura multi-constraint ni contexto insuficiente.
- Reemplazar `planning.ValidationPlan`: descartado; se materializa y reutiliza
  el contrato ejecutable aprobado.
- Entregar el registry real al generador: descartado; expondría handles de
  ejecución y violaría ARCH-001/002.
- Confiar en method/path escritos por el modelo: descartado; se derivan de una
  operation key retenida.
- Permitir reparación/retry del modelo: descartado; ocultaría consumo y se
  acercaría a la replanificación de feature 13.
- Considerar `not_applicable` como calidad probada: descartado; la consistencia
  se prueba aquí y la calidad semántica se mide en feature 11.
- Persistir el plan generado: descartado; publicación/aprobación pertenecen a
  composición posterior y feature 12.

## Conformidad arquitectónica

El diseño preserva ARCH-001 al no ejecutar ni entregar handles; ARCH-002 y
ARCH-011 mediante registry/policy obligatorios; ARCH-004 al no producir
veredictos; ARCH-005/006 con límites y context; ARCH-008 al separar instrucciones
de datos; ARCH-009 al recibir solo proyección tratada; y ARCH-010 con tests
herméticos. No se solicita excepción arquitectónica.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T4, T8 |
| R2 | T1, T3, T4, T8 |
| R3 | T2, T3, T8 |
| R4 | T2, T3, T5, T8 |
| R5 | T3, T4, T5, T8 |
| R6 | T1, T4, T8 |
| R7 | T3, T5, T8 |
| R8 | T4, T6, T8 |
| R9 | T6, T7, T8 |
| R10 | T1, T4, T6, T8 |
| R11 | T6, T7, T8 |
| R12 | T1, T2, T3, T4, T5, T6, T7, T8 |
