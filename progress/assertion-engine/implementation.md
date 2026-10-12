# Implementación de assertion-engine

## Requirements y tests

| Requirement | Test | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestValidateDefinitionsBeforeAnyPlanEffect`, `TestDefinitionValidationPreservesNestedPlanPath`, `TestRejectMalformedLeavesGroupsAndUnknownFieldsWithStablePath` | `internal/planning/assertion_integration_test.go`, `internal/assertion/integration_test.go`, `internal/assertion/compile_test.go` | pass |
| R2 | `TestResolveDocumentedPointerPaths`, `TestRejectMalformedPointerAndArrayTokens`, `TestMissingPathIsObservedDeterministically` | `internal/assertion/evaluator_test.go`, `internal/assertion/compile_test.go` | pass |
| R3 | `TestMissingPathIsObservedDeterministically` | `internal/assertion/evaluator_test.go` | pass |
| R4 | `TestJSONEqualityAcrossStructuredValuesAndNumericRepresentations`, `TestNumericOperatorsAndIncompatibleTypes`, `TestExtremeJSONExponentsRemainCompactAndExact`, `TestNumericOperatorsCompareExtremeExponentsSymbolically`, `TestSymbolicDecimalOrderingCoversSignsMagnitudesAndDigits` | `internal/assertion/evaluator_test.go`, `internal/assertion/compile_test.go` | pass |
| R5 | `TestNestedAllAnyTruthTablesAndCompleteOrder`, `TestGroupTruthTablesIncludeInconclusiveChildren` | `internal/assertion/evaluator_test.go` | pass |
| R6 | `TestNestedAllAnyTruthTablesAndCompleteOrder`, `TestEvaluationDoesNotMutateInputsAndIsRepeatable`, `TestCloneAssertionResultsIsolatesCompleteTrees` | `internal/assertion/evaluator_test.go`, `internal/planning/assertion_integration_test.go` | pass |
| R7 | `TestInvalidFailedAndInconclusiveRemainDistinct`, `TestConfigErrorMapsToPlanningCategories` | `internal/assertion/evaluator_test.go`, `internal/assertion/compile_test.go` | pass |
| R8 | `TestStepEvaluatorAggregatesMultipleRootDeclarationsWithoutModel`, `TestAssertionEngineIntegratesWithPlanBlockingAndAggregation` | `internal/assertion/evaluator_test.go`, `internal/assertion/integration_test.go` | pass |
| R9 | `TestCancelledEvaluationReturnsContextCauseWithoutFunctionalResult`, `TestCancellationDuringNestedGroupStopsWithoutFunctionalResult`, `TestCancellationDuringDefensiveCompilationReturnsNoFunctionalResult`, `TestCancellationWhileBuildingMissingEvidenceRootsReturnsNoInconclusiveResult` | `internal/assertion/evaluator_test.go`, `internal/assertion/integration_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Contratos tipados de assertion y resultados inmutables añadidos en `planning`. |
| T2 | done | Compilador completo y puerto obligatorio de validación integrado antes de efectos. |
| T3 | done | JSON Pointer acotado, presencia, igualdad JSON y números exactos implementados. |
| T4 | done | Evaluación depth-first de `all`/`any`, árboles auditables y cancelación implementadas. |
| T5 | done | `Engine` implementa `StepAssertionEvaluator`; resultado tipado llega al executor y conserva bloqueos. |
| T6 | done | Suite canónica, race repetido, coverage y trazabilidad completados. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race ./...` | pass |
| `CGO_ENABLED=1 CC=C:\msys64\ucrt64\bin\gcc.exe go test -race -count=20 ./internal/assertion ./internal/planning` | pass |
| `go test -coverprofile=coverage.out ./...` | pass |
| `go tool cover -func=coverage.out` | pass |

La configuración explícita de `CGO_ENABLED` y `CC` usa el compilador local de MSYS2 porque el entorno de PowerShell inicia con CGO desactivado y `gcc` fuera de `PATH`.

## Coverage

- Global: 90,2 % de statements.
- `internal/assertion`: 90,0 %.
- `internal/planning`: 90,3 %.
- Ramas relevantes: validación de variantes y errores, paths, todos los operadores, tipos incompatibles, grupos ternarios, falta de evidencia, integración, bloqueo y cancelación.

## Archivos modificados

- `internal/assertion/{model,compile,path,compare,evaluator}.go` y tests del paquete.
- `internal/planning/{model,validate,executor}.go` y tests de contratos/integración.
- `specs/assertion-engine/tasks.md`.
- `progress/current.md` y este informe.

## Riesgos o limitaciones

- La entrada continúa siendo `planning.AssertionDeclaration.Definition`; la representación YAML/JSON externa permanece fuera de alcance según la spec.
- El motor evalúa únicamente el output terminal confirmado que entrega el executor y no lee el evidence store por su cuenta.

## Correcciones tras primera revisión

- REV-1: los números usan ahora coeficiente decimal compacto y exponente decimal arbitrario (`big.Int`). Comparación y almacenamiento dependen del tamaño del literal y nunca materializan `10^exponent`. Los tests cubren exponentes positivos y negativos fuera de `int32`, equivalencias exactas, orden y representación acotada.
- REV-2: `Evaluate` propaga contexto por compilación, paths y copias recursivas; comprueba cancelación en cada declaration, cada nodo, cada root sin evidencia y antes de todo retorno funcional. Los tests cancelan de forma determinista durante compilación y durante la construcción de un root `inconclusive`, sin temporizadores.

## Corrección tras segunda revisión

- REV-3: añadidas pruebas de comportamiento para límites de configuración del union de declarations, ciclos en valores esperados y grupos, igualdad estricta de formas JSON, valores escalares, observaciones numéricas ausentes y agregación de varias roots. El coverage reproducible queda en 90,2 % global, por encima del baseline aprobado de 90,1 %, con 90,0 % en `internal/assertion` y 90,3 % en `internal/planning`.
