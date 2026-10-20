# Cuarta revisión independiente — Feature 21: fuentes de contrato independientes de OpenAPI

## Veredicto

**APPROVED**. REV-14 está cerrado y no se encontraron regresiones bloqueantes en
REV-1–REV-13. La implementación satisface R1–R12 y T1–T12.

## Precondiciones

- Existe exactamente una feature `in_progress`: `contract-source-abstraction`.
- La spec conserva aprobación humana de `2026-10-19T13:52:53+02:00`.
- T1–T12 están marcadas `[x]` y existe informe de implementación actualizado.
- Se inspeccionaron directamente el diff, el código, los tests y la spec; el
  informe del implementer no se usó como sustituto de evidencia.

## Cierre de REV-14

REV-14 queda cerrado por estas comprobaciones directas:

- `internal/openapi/loader.go:92-97` ejecuta la extracción antes de construir y
  publicar `Contract`; cualquier error devuelve `nil, err`.
- `collectExcludedValues` es iterativo. Comprueba `ctx.Err()` en cada frame,
  limita profundidad, nodos totales, nodos pendientes, bytes y valores, reserva
  hijos contra el presupuesto antes de apilarlos y usa frames de salida para
  detectar ciclos activos de mapas y slices. Ordena las keys y el resultado,
  por lo que su salida no depende del orden de mapas.
- Ante cancelación, deadline, límite o ciclo devuelve siempre una slice `nil` y
  un error tipado. No conserva ni publica la colección parcial.
- `TestExcludedValueTraversalIsDeterministicCancelableAndAllOrNothing` falla si
  se elimina la observación de cancelación durante el recorrido o el manejo de
  deadline. `TestExcludedValueTraversalRejectsEveryWorkDimensionAndCycles`
  falla si falta cualquiera de los límites de depth/nodes/bytes/values o la
  detección de ciclos. `TestLoaderPublishesNoContractWhenExcludedTraversalExhausts`
  exige que no exista `Contract` parcial.
- `NeutralizeReducedContext` propaga el `context.Context` por conversión,
  registry, merge y reducción. `ContractSourceAdapter.Load`, `translateJSON` y
  la compatibilidad convierten cancelación y deadline en
  `contractcatalog.Error{Category: CategoryCancelled}` con ubicaciones
  estructurales fijas y causa preservada por `Unwrap`/`errors.Is`.
- `TestNeutralCompatibilityReturnsSafeTypedCancellation` cubre cancelación
  inicial, deadline y cancelación durante conversión. También exige
  `errors.As`, categoría, ubicación, causa y serialización sin el canario. La
  ruta directa del adapter comprueba la misma unión tipada.

No quedan hallazgos abiertos.

## Reauditoría de REV-1–REV-13

| Hallazgo | Resultado |
|---|---|
| REV-1 | Cerrado: catálogo, policy, contexto e índice retenido mantienen autoridad privada, copias profundas y bindings recalculados. |
| REV-2 | Cerrado: provenance por claim y security schemes/responses conservan paridad OAS 3.0/3.1. |
| REV-3 | Cerrado: descriptors confiables se prevalidan; snapshots opacos e identidad/digest/metadata se validan antes del adapter. |
| REV-4 | Cerrado: límites per-source, agregados y de reducción son all-or-nothing y se reservan antes de la siguiente source. |
| REV-5 | Cerrado: canonización recursiva, igualdad estructural y conflictos explícitos son independientes del orden. |
| REV-6 | Cerrado: suficiencia por dimensión/claim conserva provenance fuerte y la unión tipada hasta workflow/API/CLI. |
| REV-7 | Cerrado: composición productiva selecciona operation keys estructuradas y no entrega operaciones no seleccionadas. |
| REV-8 | Cerrado: redacción productiva es explícita, validada y configurable en todas las superficies cubiertas. |
| REV-9 | Cerrado: spies efectivos, tests verticales/adversarios y coverage reproducible. |
| REV-10 | Cerrado: `SourceRequest` y `ProjectionPolicy` separan copias del caller, registry y adapter; race pasa. |
| REV-11 | Cerrado: composición exige configuración completa y bloquea canarios antes de red/modelo/tools/approval/persistencia. |
| REV-12 | Cerrado: compatibilidad propaga ctx y conserva schemes alcanzables para operaciones protegidas 3.0/3.1. |
| REV-13 | Cerrado: adapter adversario y frontera AST prueban ausencia de puertos de capacidades; cobertura relevante supera 85 %. |

## Trazabilidad R1–R12

| Requirement | Resultado y evidencia principal |
|---|---|
| R1 | Cumple: API neutral/versionada del planner y tests de frontera sin imports/tipos OpenAPI productivos. |
| R2 | Cumple: registry exacto, request sellado, snapshots comunes y tests de duplicados/drift/aislamiento. |
| R3 | Cumple: provenance exacta por claim, safe locations y deep copies verificadas. |
| R4 | Cumple: clase, confianza y completitud son explícitas y no elevables por una source. |
| R5 | Cumple: adapter parte de `Contract` normalizado y conserva operations, schemas, security, responses, omissions y response validation. |
| R6 | Cumple: merge, digest y reducción son canónicos ante permutaciones. |
| R7 | Cumple: conflictos identity/method/path/schema/security/response son explícitos y fail-closed. |
| R8 | Cumple: policy completa por claim y revalidación de catálogo/policy/estado epistémico antes de ejecución. |
| R9 | Cumple: selección estructurada, whole-unit omissions e índice retenido exacto. |
| R10 | Cumple: sources pasivas, límites completos, recorrido OpenAPI acotado/cancelable y ausencia de estado parcial. |
| R11 | Cumple: suites de OpenAPI, planning, approval, replanning, workflow y `validate --plan` conservan compatibilidad observable. |
| R12 | Cumple: errores cerrados y seguros, cancelación tipada, canarios ausentes y comentarios sistémicos en los símbolos modificados. |

## Estado de T1–T12

T1, T2, T3, T4, T5, T6, T7, T8, T9, T10, T11 y T12 están completas. Cada
task tiene implementación y tests trazables a sus requirements; no se observó
una excepción pendiente ni trabajo marcado artificialmente como completo.

## Verificación ejecutada

```text
go test ./internal/openapi -run 'TestExcludedValueTraversal|TestLoaderPublishesNoContract|TestNeutralCompatibilityReturnsSafeTypedCancellation|TestNeutralCompatibilityPreservesProtectedSchemesAndCancellation' -count=20 -timeout=8m
PASS

go test ./internal/contractcatalog ./internal/openapi ./cmd/featureproof -count=20 -timeout=8m
PASS

PATH=C:\msys64\ucrt64\bin;%PATH% CGO_ENABLED=1 go test -race ./internal/contractcatalog ./internal/openapi ./internal/agentplanning ./internal/planapproval ./internal/replanning ./internal/validationworkflow ./internal/agenteval ./cmd/featureproof -count=1 -timeout=12m
PASS

powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify_feature21_coverage.ps1
contractcatalog 87,7%; openapi 91,5%; agentplanning 87,3%;
planapproval 86,4%; replanning 85,2%; validationworkflow 85,9%;
agenteval 91,2%; workflowProjectionConfig.policy 100,0%;
loadWorkflowConfig 93,8%; composeWorkflowCLI 85,1%;
workflowContractReducer.Reduce 86,5%
PASS

go test ./cmd/featureproof -run 'Validate|Plan|Workflow|ComposedWorkflow' -count=10 -timeout=5m
PASS

gofmt -l .
PASS (sin archivos)

go vet ./...
PASS

go test -run '^$' ./...
PASS

git diff --check
PASS (solo avisos CRLF del checkout Windows)

go test ./... -count=1 -timeout=12m
Único fallo: internal/localvalidation/TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink,
porque Windows devuelve `A required privilege is not held by the client`.
Todos los demás paquetes pasan.

PATH=C:\msys64\ucrt64\bin;%PATH% CGO_ENABLED=1 go test -race ./internal/localvalidation -count=1 -timeout=5m
Reproduce el mismo único fallo de privilegio de symlink.
```

El fallo de symlink es ambiental, preexistente y ajeno a esta feature. No se
detectaron fallos funcionales, carreras ni regresiones atribuibles al diff.
