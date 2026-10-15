# Tercera revisión independiente — Suite de evaluación del agente

## Dictamen

`approved`

La implementación satisface R1–R12 y T1–T9. Las correcciones REV-10–REV-13
cierran los cuatro bloqueos de la segunda revisión sin reabrir REV-1–REV-9. No
se encontraron nuevos hallazgos bloqueantes, accesos a producción, efectos de
aplicación, secretos persistidos ni credenciales expuestas al harness.

## Precondiciones

- `agent-evaluation-suite` es la única feature `in_progress`.
- La spec registra aprobación humana: `approved_by: user` y
  `approved_at: 2026-10-15T16:53:38+02:00`.
- T1–T9 están marcadas `[x]` y existe el informe del implementador.
- Se revisaron AGENTS, proceso SDD, arquitectura, convenciones, verificación,
  spec aprobada completa, implementación, dataset, tests y todo el diff.

## Cierre comprobado de REV-1–REV-13

| Hallazgo | Evidencia revisada | Estado |
|---|---|---|
| REV-1 | Confinement del handle abierto mediante `SameFile`, rechazo de enlaces intermedios y snapshot OpenAPI ligado al digest; `TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot`. | Cerrado |
| REV-2 | Comparación exacta de kinds y constraints ausentes, incluidos faltantes y extras; `TestMissingContextExpectationsAreClosedAndCompared`. | Cerrado |
| REV-3 | Category, stage y path forman la firma del rechazo; `TestRejectionMatchingIncludesTypedPath`. | Cerrado |
| REV-4 | `fail_fast=false` conserva el fallo calificable y continúa en orden; `TestFailFastFalseContinuesAfterQualifiableFailure`. | Cerrado |
| REV-5 | Deadline propagado y límites independientes de calls, tokens, duration, replans y work; `TestRunDeadlineAndEveryLimitDimensionAreEnforced`. | Cerrado |
| REV-6 | `safe_configuration` incluye límites load/run/case/source/I/O y omite perfil; `TestManifestPublishesSafeBudgetsWithoutProfile`. | Cerrado |
| REV-7 | Baseline exige artifacts autenticados y valida invariantes cruzadas; `TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants`. | Cerrado |
| REV-8 | `affectedCases` se calcula por categoría y por caso; `TestAffectedCasesAreComputedPerCategory`. | Cerrado |
| REV-9 | Compatibilidad baseline se valida antes de componer live; `TestIncompatibleBaselineStopsBeforeLiveComposition`. | Cerrado |
| REV-10 | Se rechazan duplicados de todos los sets y relaciones de expectativas, la clave de operación normaliza método y evita colisiones, y todos los sets se ordenan antes del hash. El SHA wire continúa verificando bytes mientras el digest de caso/dataset usa la representación lógica; `TestEveryStructuralExpectationSetRejectsDuplicates`, `TestMissingContextExpectationsAreClosedAndCompared` y `TestLogicalDigestsIgnoreOrderOfEveryStructuralSet`. | Cerrado |
| REV-11 | `durationFromMilliseconds` comprueba positividad y máximo representable antes de multiplicar; criteria y case devuelven `limit_exceeded` en límite+1; `TestMillisecondLimitsRejectOverflowAtCriteriaAndCaseBoundaries`. | Cerrado |
| REV-12 | `call_timeout_millis` es obligatorio en composición live, se copia desde las opciones reales y se publica en `safe_configuration` sin profile ni secretos; `TestLiveSourceUsesFeatureNineSession` y `TestProductionEvalComposerDefersCredentialResolution`. | Cerrado |
| REV-13 | Coverage global fresco y reproducible: **91,1 %**; paquete `internal/agenteval`: **91,3 %**. Iguala el baseline global aprobado y supera el mínimo del código nuevo. | Cerrado |

## Trazabilidad R1–R12

| Requirement | Test real y comprobación principal | Estado |
|---|---|---|
| R1 | `TestLoaderAcceptsCanonicalVersionedDataset`, `TestLoaderRejectsIntegrityAndPathEscapes`, tests REV-1 y REV-10. | Cumple |
| R2 | `TestCaseRequiresConsistentStructuralExpectations`, tests REV-2, REV-3 y REV-10. | Cumple |
| R3 | `TestRunnerNeverComposesOrExecutesApplicationActions`, test REV-4. | Cumple |
| R4 | `TestOracleDetectsEveryInventionCategoryWithoutValues`, test REV-3. | Cumple |
| R5 | `TestMetricsUseExactStructuralNumeratorsAndStablePPM`, tests REV-5 y REV-11. | Cumple |
| R6 | `TestInjectionCanariesRemainOnlyInUntrustedChannel`. | Cumple |
| R7 | `TestBundlePublishesManifestLastAndLeaksNoCanaries`, tests REV-6, REV-7 y REV-12. | Cumple |
| R8 | `TestCriteriaDriveThresholdHardFailAndExitCodes`, `TestRunCommandMapsConfigurationQualityAndCancellation`. Los códigos 0/1/2/3 quedan cubiertos. | Cumple |
| R9 | `TestComparatorReportsCategoryRegressionsAndRejectsDrift`, tests REV-7, REV-8 y REV-9. | Cumple |
| R10 | `TestEvalAgentRequiresExplicitSourceAndLiveSelection`, `TestLiveSourceUsesFeatureNineSession`, `TestProductionEvalComposerDefersCredentialResolution`. Replay y live mantienen fronteras separadas y las credenciales se resuelven después del preflight. | Cumple |
| R11 | `TestRunnerStopsOnCancellationAndOverflowWithoutCompleteBundle`, tests REV-5 y REV-11. | Cumple |
| R12 | `TestHermeticEvaluationNeedsNoNetworkCredentialsOrTarget`, `TestInitialDatasetRunsEndToEnd` y verificación canónica completa. | Cumple |

## Tasks y arquitectura

T1–T9 están completas y su comportamiento está cubierto por tests. El paquete
`internal/agenteval` conserva interfaces en el consumidor, ejecución
secuencial con estado propietario, context propagado, reloj/fuentes
sustituibles y errores tipados. El runner solo consume la frontera de
generación: no recibe tool registry ejecutable, target, evidence store ni
credenciales. Replay es local y hermético; live exige opt-in y la composición
OAuth queda en el entrypoint. El writer publica el manifest completo al final
y las pruebas ejercitan sus fallos atómicos. Las entidades y funciones nuevas o
modificadas incluyen comentarios sobre su responsabilidad sistémica.

## Verificación fresca

Entorno: Go 1.25.1 en Windows; race con CGO y UCRT64.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./... -count=1` | pass |
| tests REV-1–REV-13 en aislamiento | pass |
| `CGO_ENABLED=1 go test -race ./... -count=1` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/agenteval ./cmd/featureproof` | pass |
| `go test -count=1 -cover ./internal/agenteval ./cmd/featureproof` | pass; 91,3 % y 89,2 % |
| coverage global con profile + `go tool cover -func` | pass; **91,1 %** |
| `go mod verify` | pass |
| `git diff --check` | pass; solo avisos LF/CRLF |
| `go build ./...` | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| compilación de tests `internal/agenteval` para Linux y Darwin | pass |

Durante esta tercera revisión solo se actualizó este informe; no se modificó
código, tests, specs, `feature_list.json` ni `implementation.md`.
