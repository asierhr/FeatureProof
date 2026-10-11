# Implementación de multi-step-validation-plan

## Resultado

Se implementó el paquete interno `planning`, propietario del modelo de plan,
la validación completa del grafo, el orden topológico estable, la resolución de
referencias tipadas desde evidencia confirmada y la ejecución secuencial. El
ejecutor reutiliza registry, validación de input, policy, presupuestos, retries,
saneado, evidence store y journal del harness existente; el modelo no participa
en ningún estado funcional.

Las evidencias identifican ahora `run`, `plan`, `step` y `tool call`. El journal
incorpora eventos estructurados de plan, selección, input resuelto, ejecución,
estado terminal y bloqueo, y protege sus slices frente a mutaciones posteriores.

## Requirements y tests

| Requirement | Tests reales | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestValidatePlanRequiresUniqueStepsAndKnownConstraint`; `TestPlanIDsRemainCaseSensitive`; `TestValidatedPlanIsAnImmutableSnapshot` | `internal/planning/validate_test.go` | pass |
| R2 | `TestValidatePlanRequiresStructuredStepFields`; `TestInvalidPlanNeverReachesPolicyOrTools`; `TestNonFiniteLiteralsAreRejectedBeforeEffects`; `TestValidatedPlanIsAnImmutableSnapshot` | `internal/planning/validate_test.go` | pass |
| R3 | `TestRejectInvalidOrNonAncestorReferences`; `TestNullReferenceTypeIsRejectedBeforeEffects`; `TestValidatedPlanIsAnImmutableSnapshot`; `TestResolveTypedReferencesFromPersistedAncestorEvidence`; `TestResolutionRejectsForeignEvidenceMissingPathAndWrongType`; `TestResolveRejectsUnconfirmedTerminalEvidence`; `TestMultiStepIntegrationResolvesEvidenceAndContinuesIndependentBranch` | `internal/planning/{validate,resolve,executor}_test.go` | pass |
| R4 | `TestRejectUnknownDuplicateAndSelfDependencies`; `TestRejectCyclesDeterministicallyBeforeExecution`; `TestCycleDiagnosticExcludesEarlierDescendantAndIndependentBranch`; `TestInvalidPlanNeverReachesPolicyOrTools` | `internal/planning/validate_test.go` | pass |
| R5 | `TestExecutePlanUsesStableTopologicalDeclarationOrder`; `TestResolveValidatePolicyBudgetToolEvidenceEvaluatorOrder`; `TestToolRemovedAfterValidationBecomesInconclusive` | `internal/planning/{validate,executor}_test.go` | pass |
| R6 | `TestStepAndPlanResultsAreDerivedDeterministically`; `TestExpectedControlRejectionsBecomeInconclusive`; `TestEvaluatorCannotReturnBlocked` | `internal/planning/executor_test.go` | pass |
| R7 | `TestFailedAndInconclusiveDependenciesBlockDescendants`; `TestMultiStepIntegrationResolvesEvidenceAndContinuesIndependentBranch` | `internal/planning/executor_test.go` | pass |
| R8 | `TestJournalReconstructsExecutedAndBlockedStepsInOrder`; `TestJournalFailureBeforeToolExecutionStopsSelectionAndPreservesConfirmedPrefix`; `TestJournalFailureAfterEvidenceStopsSelectionAndPreservesEvidence`; `TestJournalPlanEventsAreImmutable`; `TestEvidenceStoreDetectsTamperingAndRelatesIDs` | `internal/planning/executor_test.go`; `internal/harness/journal_test.go`; `internal/evidence/store_test.go` | pass |
| R9 | `TestPlanRetriesOnlySafeTransientOperationsWithinBudget`; `TestPlanCancellationPreservesConfirmedWorkAndStopsSelection`; `TestCancellationAtEachExecutionBoundary`; `TestExecutionBoundaryFailuresStopPlanWithoutFunctionalReclassification`; `TestJournalFailureBeforeToolExecutionStopsSelectionAndPreservesConfirmedPrefix`; `TestJournalFailureAfterEvidenceStopsSelectionAndPreservesEvidence` | `internal/planning/executor_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Entidades, estados y errores tipados del plan probados y documentados. |
| T2 | done | Validación unitaria, ciclos, cierre transitivo y orden topológico estable verificados. |
| T3 | done | Referencias field/index y tipos resueltos solo desde evidencia terminal del mismo run/step. |
| T4 | done | Ejecución secuencial, controles, agregación, bloqueos, retries y cancelación verificados. |
| T5 | done | Evidencia relacionada y journal reconstruible e inmutable verificados. |
| T6 | done | Integración hermética, regresiones, race detector, coverage y trazabilidad completados. |

## Comandos ejecutados

Los comandos finales se ejecutaron con Go 1.25.1. Para `-race` se usaron
`CGO_ENABLED=1`, `CC=gcc` y `C:\msys64\ucrt64\bin` al inicio de `PATH`.
El perfil de cobertura se generó en el directorio temporal del sistema.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -race -count=20 ./internal/...` | pass |
| `go test -coverprofile=<ruta temporal> ./...` | pass |
| `go tool cover -func=<ruta temporal>` | pass |

## Coverage

- Global: 90,1 % de statements.
- `internal/planning`: 89,8 %.
- `internal/evidence`: 87,3 %.
- `internal/harness`: 89,0 %.
- El código nuevo y todos los paquetes modificados superan el mínimo de 85 %.
- Las ramas cubiertas incluyen estructura inválida, referencias ajenas o
  ausentes, ciclos, orden topológico, tool desaparecida, input inválido,
  policy denegada, presupuesto agotado, errores permanentes/transitorios,
  retries seguros, fallo de store/evaluator, cancelación en cada frontera,
  bloqueos múltiples y continuación de ramas independientes.

## Archivos modificados

- `internal/planning/model.go`, `validate.go`, `resolve.go`, `executor.go` y sus
  tests.
- `internal/evidence/store.go` y `store_test.go`.
- `internal/harness/journal.go` y `journal_test.go`.
- `specs/multi-step-validation-plan/tasks.md`.
- `progress/current.md`.
- `progress/multi-step-validation-plan/implementation.md`.

## Compatibilidad y límites

- La suite existente del harness de una sola restricción y del adapter YAML v1
  permanece sin cambios de comportamiento y pasa completa.
- El lenguaje y los operadores de assertions siguen siendo opacos. El ejecutor
  usa `StepAssertionEvaluator`; su implementación productiva corresponde a la
  feature 4.
- La ejecución es secuencial y no reanuda un plan tras reiniciar el proceso,
  conforme al alcance aprobado.
- El formato externo/YAML del plan queda fuera de esta feature.

## Correcciones tras la primera review

- **REV-1:** se eliminó `ValueNull` del dominio y de la resolución. El conjunto
  admitido queda cerrado a `string`, `number`, `boolean`, `object` y `array`.
  `TestNullReferenceTypeIsRejectedBeforeEffects` comprueba categoría
  `invalid_reference`, field path estable y cero llamadas a policy/tool.
- **REV-2:** `clonePlan` copia recursivamente maps, slices, arrays e interfaces
  contenidos en `AssertionDeclaration.Definition`, preservando sus tipos
  concretos. `TestValidatedPlanIsAnImmutableSnapshot` muta una definición
  anidada desde el plan original y desde el resultado de `Plan()` y demuestra
  que la instantánea interna conserva el valor validado.
- **REV-3:** `clonePathSegments` crea nuevos escalares y punteros para cada
  selector `Field` e `Index`. `TestValidatedPlanIsAnImmutableSnapshot` muta
  ambos punteros desde el plan original y desde el resultado de `Plan()` y
  confirma que la referencia interna conserva el path validado.
- **REV-4:** se añadió `failingPlanJournal`, que falla una vez en un evento
  configurable conservando el prefijo confirmado. Dos tests ejercitan un
  fallo antes de la herramienta y otro después de persistir evidencia;
  comprueban error operativo, cero selección posterior, conservación de
  eventos/evidencia y ausencia de `plan_completed`. La implementación ya
  propagaba correctamente todos los fallos de `Append`, por lo que no necesitó
  cambios productivos para esta corrección.
- **REV-5:** al bloquearse Kahn, `firstCyclicStep` comprueba qué nodos pueden
  alcanzarse a sí mismos y elige el de menor declaration order. El test incluye
  un descendiente declarado antes, el ciclo y una rama independiente, y repite
  el diagnóstico exacto `steps[2].depends_on` sin cruzar policy/tool.
- **REV-6:** `jsonCompatible` distingue floats finitos de `NaN`, `+Inf` y
  `-Inf`. Los tres valores no JSON devuelven `invalid_value` en
  `steps[0].input.value` antes de policy o herramienta.
