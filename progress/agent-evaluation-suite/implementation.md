# Implementación — Suite de evaluación del agente

## Estado

`ready_for_review`

Se completaron T1-T9 en orden. La implementación no cambia
`feature_list.json`, no ejecuta evaluaciones live durante tests y no compone
executor, tool registry, target, evidence store ni journal dentro del runner.

La primera revisión independiente fue corregida íntegramente. T1-T9
permanecen completas y `progress/agent-evaluation-suite/review.md` se conserva
como evidencia inmutable del dictamen anterior.

La segunda revisión confirmó REV-1–REV-9 y planteó REV-10–REV-13. Esos cuatro
hallazgos también están corregidos; el archivo de review conserva ambos
dictámenes sin modificaciones del implementer.

## Resultado implementado

- `internal/agenteval` contiene formatos v1, JSON estricto, snapshots locales,
  reconstrucción de `ReducedContext`, registry descriptor-only, fuentes replay
  y live, runner secuencial, captura del prompt real, oráculo estructural,
  métricas ppm, criterios, comparación y writer atómico.
- `featureproof eval agent` se despacha desde `cmd/featureproof`. Replay no usa
  red. Live exige opt-in, provider, model, perfil y todos los budgets; la
  composición OAuth ocurre en `cmd` después del preflight local.
- `evals/agentplanning/v1/` incluye cinco casos: plan válido, canarios de
  injection como datos, contexto insuficiente, endpoint inventado y rechazo
  por límite. Dataset, casos, OpenAPI y replay están ligados por SHA-256.
- Los bundles publican `cases.jsonl`, `metrics.json`, comparación opcional y
  `manifest.json` al final. Un directorio incompleto nunca es baseline válido.
- Cada entidad, interfaz, función, método y helper nuevo o modificado incluye
  un comentario de responsabilidad dentro del sistema.

## Correcciones de la primera review

| Hallazgo | Corrección y prueba directa |
|---|---|
| REV-1 | Cada path se resuelve contra el root real antes de abrirlo y OpenAPI se compone mediante `openAPISnapshot`, que sirve los bytes ya verificados. `TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot` prueba un junction/symlink intermedio que escapa y sustituye el archivo tras el snapshot. |
| REV-2 | Los kinds y constraints esperados son sets cerrados, validados y comparados exactamente; un mismatch recibe numerador cero. `TestMissingContextExpectationsAreClosedAndCompared` cubre kind/constraint incorrecto, extra, ausente y duplicado. |
| REV-3 | `casePasses` exige category, stage y path. `TestRejectionMatchingIncludesTypedPath` cambia solo el path. |
| REV-4 | Un fallo calificable conserva su `CaseResult`; `fail_fast=false` continúa en orden y el run conserva el error terminal de sistema. `TestFailFastFalseContinuesAfterQualifiableFailure` usa dos casos. |
| REV-5 | El runner deriva y propaga un deadline total. Calls, tokens, duration, replans y work se calculan con deltas comprobados y se contrastan con budgets explícitos del caso. `TestRunDeadlineAndEveryLimitDimensionAreEnforced` cubre cada dimensión y una inferencia bloqueada hasta deadline. |
| REV-6 | El manifest v1 incluye `safe_configuration` con límites de carga/run/generación/evaluación y budgets source/I/O, sin profile. `TestManifestPublishesSafeBudgetsWithoutProfile` verifica valores y confidencialidad. |
| REV-7 | `LoadBundle` exige los artifacts obligatorios autenticados, lee una vez desde bytes verificados, limita el total y comprueba case digests, IDs y métricas/result digest recalculados. `TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants` cubre entry y archivo omitidos. |
| REV-8 | `affectedCases` compara la contribución de cada caso a la categoría solicitada. `TestAffectedCasesAreComputedPerCategory` cubre un cambio endpoint→credential con igual cardinalidad y un cambio de outcome sin delta métrico. |
| REV-9 | `ValidateBaselineCompatibility` se ejecuta antes de componer la fuente. `TestIncompatibleBaselineStopsBeforeLiveComposition` usa una baseline completa con drift y confirma cero composiciones. |

## Correcciones de la segunda review

| Hallazgo | Corrección y prueba directa |
|---|---|
| REV-10 | Se rechazan duplicados en required constraints, capabilities, identities, evidence y operations. Todos los sets, incluidos missing kinds/constraints, se ordenan por clave canónica. El SHA del archivo conserva la integridad wire y `Case.digest`/dataset identity usan el documento lógico normalizado. `TestEveryStructuralExpectationSetRejectsDuplicates` cubre cada duplicado y `TestLogicalDigestsIgnoreOrderOfEveryStructuralSet` prueba SHA wire distinto con identidad lógica idéntica ante cada reordenación. |
| REV-11 | `durationFromMilliseconds` centraliza la conversión checked. Criteria y case evaluation clasifican límite+1 como `limit_exceeded`; runner y oracle consumen únicamente el valor representable. `TestMillisecondLimitsRejectOverflowAtCriteriaAndCaseBoundaries` cubre límite y límite+1 en ambas fronteras. |
| REV-12 | `SourceBudget` v1 publica `call_timeout_millis`; el entrypoint lo copia desde `CommandOptions.CallTimeout`, `NewLiveSource` exige un registro live completo y el manifest lo serializa sin profile. `TestLiveSourceUsesFeatureNineSession` y `TestProductionEvalComposerDefersCredentialResolution` verifican serialización, propagación y confidencialidad. |
| REV-13 | Se añadieron pruebas de comportamiento para las nuevas fronteras y ramas. El comando exacto `go test -count=1 "-coverprofile=<temp>" ./...` seguido de `go tool cover "-func=<temp>"` produce 91,1 % global, igual al baseline; `internal/agenteval` queda en 91,3 %. |

## Trazabilidad requirements → pruebas reales

| Requirement | Pruebas |
|---|---|
| R1 | `TestLoaderAcceptsCanonicalVersionedDataset`; `TestLoaderRejectsIntegrityAndPathEscapes`; `TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot`; `TestLogicalDigestsIgnoreOrderOfEveryStructuralSet`; `TestDatasetManifestRejectsVersionIdentityAndDuplicates`; `TestCaseCompositionRejectsEveryClosedBoundary` |
| R2 | `TestCaseRequiresConsistentStructuralExpectations`; `TestEveryStructuralExpectationSetRejectsDuplicates`; `TestMissingContextExpectationsAreClosedAndCompared`; `TestLogicalDigestsIgnoreOrderOfEveryStructuralSet`; `TestInitialDatasetRunsEndToEnd`; `TestCaseCompositionRejectsEveryClosedBoundary` |
| R3 | `TestRunnerNeverComposesOrExecutesApplicationActions`; `TestFailFastFalseContinuesAfterQualifiableFailure`; `TestHermeticEvaluationNeedsNoNetworkCredentialsOrTarget`; `TestSeparateRunsRemainIsolatedUnderConcurrentTests` |
| R4 | `TestOracleDetectsEveryInventionCategoryWithoutValues`; `TestRejectionMatchingIncludesTypedPath`; `TestOutcomeClassificationCoversEveryExpectedBranch`; `TestBoundaryBranchTablesEnforceFailClosedBehavior` |
| R5 | `TestMetricsUseExactStructuralNumeratorsAndStablePPM`; `TestMissingContextExpectationsAreClosedAndCompared`; `TestRunDeadlineAndEveryLimitDimensionAreEnforced`; `TestCriteriaDriveThresholdHardFailAndExitCodes` |
| R6 | `TestInjectionCanariesRemainOnlyInUntrustedChannel`; `TestInitialDatasetRunsEndToEnd`; `TestRunnerAndOracleErrorBranchesRemainTyped` |
| R7 | `TestBundlePublishesManifestLastAndLeaksNoCanaries`; `TestManifestPublishesSafeBudgetsWithoutProfile`; `TestLiveSourceUsesFeatureNineSession`; `TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants`; `TestWriterFailuresNeverPublishCompleteManifest`; `TestAtomicWriterChecksEveryFailureBoundary` |
| R8 | `TestCriteriaDriveThresholdHardFailAndExitCodes`; `TestRunCommandMapsConfigurationQualityAndCancellation`; `TestStrictInputsRejectUnsupportedAndUnsafeVariants` |
| R9 | `TestComparatorReportsCategoryRegressionsAndRejectsDrift`; `TestAffectedCasesAreComputedPerCategory`; `TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants`; `TestBaselineLoaderRejectsEveryIncompleteArtifact` |
| R10 | `TestEvalAgentRequiresExplicitSourceAndLiveSelection`; `TestIncompatibleBaselineStopsBeforeLiveComposition`; `TestLiveSourceUsesFeatureNineSession`; `TestRunDispatchesHermeticAgentEvaluation`; `TestProductionEvalComposerDefersCredentialResolution` |
| R11 | `TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot`; `TestRunDeadlineAndEveryLimitDimensionAreEnforced`; `TestMillisecondLimitsRejectOverflowAtCriteriaAndCaseBoundaries`; `TestHardenedBoundaryHelpersRejectAllInvalidBranches`; `TestRunnerStopsOnCancellationAndOverflowWithoutCompleteBundle`; `TestAtomicWriterChecksEveryFailureBoundary` |
| R12 | `TestHermeticEvaluationNeedsNoNetworkCredentialsOrTarget`; `TestInitialDatasetRunsEndToEnd`; `TestSeparateRunsRemainIsolatedUnderConcurrentTests`; suite y race completas |

## Tasks

| Task | Resultado |
|---|---|
| T1 | Schemas/modelos/loaders estrictos, canonical digest y registry local completos. |
| T2 | Runner secuencial y fuentes replay/live sin efectos completos. |
| T3 | Oráculo para endpoint, capability, identity, credential y evidence completo. |
| T4 | Dataset inicial versionado y canarios de OpenAPI/log/response completo. |
| T5 | Métricas exactas, matriz de confusión, criterios y códigos completos. |
| T6 | Bundle seguro, hashes, manifest final y fallos inyectados completos. |
| T7 | Baseline compatible y deltas por categoría completos. |
| T8 | Comando separado, replay offline y composición live explícita completos. |
| T9 | Integración, aislamiento concurrente, comentarios, trazabilidad y verificación completos. |

## Verificación ejecutada

Entorno: Go 1.25.1 en Windows. El race detector se ejecutó con CGO y
`C:\msys64\ucrt64\bin` en `PATH`. Todas las pruebas son herméticas; no se
resolvieron perfiles, no se contactó un provider y no se usó un target.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./... -count=1` | pass |
| `CGO_ENABLED=1 go test -race ./... -count=1` con UCRT64 | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/agenteval ./cmd/featureproof` | pass |
| `go test -count=1 "-coverprofile=<temp>" ./...` + `go tool cover "-func=<temp>"` | pass; 91,1 % global, baseline de 91,1 % preservado |
| `go test -count=1 -cover ./internal/agenteval` | pass; 91,3 % |
| `go test -count=1 -cover ./cmd/featureproof` | pass; 89,2 % |
| `go mod verify` | pass |
| `go build ./...` en Windows | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` y test compile de `internal/agenteval` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` y test compile de `internal/agenteval` | pass |
| `git diff --check` | pass; únicamente avisos esperados LF/CRLF |

## Riesgos y límites restantes

- La versión v1 procesa casos secuencialmente. La concurrencia solo se prueba
  entre runs independientes y no altera admisión, fail-fast ni orden interno.
- Una baseline requiere identidad y digests idénticos de dataset/casos; cambiar
  el benchmark exige crear una baseline nueva.
- Un fallo de publicación puede dejar archivos temporales o un directorio sin
  manifest; dicho estado es deliberadamente incompleto y no es comparable.
- El único adapter live actual es OpenAI Responses mediante el OAuth existente.
  Su validación real sigue siendo opt-in y está separada de `go test`.
