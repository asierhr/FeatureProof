# Implementación de evidence-package

## Estado

T1–T8 implementadas y verificadas. La feature sigue `in_progress` hasta review independiente. El paquete v1 se construye dentro del staging de `Publisher`: `package.json`, inventario exacto en `manifest.json`, validación con `ReadPackage` y `CommitNoReplace`. El lector usa `os.Root`, límites y hashes de bytes, y no ejecuta red ni herramientas.

## Requirements y tests

| Requirement | Tests concretos | Resultado |
|---|---|---|
| R1 | `TestPackageV1StableFieldsAndExplicitSections`; `TestPackageV1RejectsVersionAmbiguousAndIncompleteShapes`; `TestRunnerPublishesOfflineReadableFunctionalPackages` | pass |
| R2 | `TestPackageProjectionUsesSealedIdentityAndDeterministicStatus`; `TestOfflinePackageRejectsRehashedArtifactTampering`; `TestOfflinePackageRejectsRehashedEvidenceTampering` | pass |
| R3 | `TestPackageCapturesFailedTreeBeforePayloadRemoval`; `TestRunnerPublishesOfflineReadableFunctionalPackages`; `TestOfflinePackageRejectsCrossDocumentTampering` | pass |
| R4 | `TestPackageCapturesFailedTreeBeforePayloadRemoval`; `TestPackageFailurePreservesConfirmedTruncationState`; `TestOfflinePackageRejectsForgedReportTopologyAndReferences` | pass |
| R5 | `TestOfflinePackageInventoryAndMutation`; `TestOfflinePackageRejectsForgedManifestEntries`; `TestOfflinePackageRejectsSymlinkAndCancellation`; `TestOfflinePackageRejectsDetectableHardLink`; `TestPackageInventoryRejectsOversizedAndInvalidEntries` | pass |
| R6 | `TestPublisherCommitsOnlyValidatedPackage`; `TestPublisherDoesNotCommitWhenPackageWriteOrValidationFails`; `TestPublisherCancellationDuringStagingNeverCommits`; `TestOfflinePackageRejectsDisappearingFileBetweenInventoryPasses` | pass |
| R7 | `TestSafeAssertionValuesRejectSecretsAndUnsupportedNumbers`; `TestPackageFailurePreservesConfirmedTruncationState`; `TestPublisherCommitsOnlyValidatedPackage`; `TestRunnerExecutesConstraintsSequentiallyWithoutModel` | pass |
| R8 | `TestOfflinePackageRejectsForgedReferenceAfterRehash`; `TestOfflinePackageRejectsCrossDocumentTampering`; `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors`; `TestOfflinePackageRejectsForgedReportTopologyAndReferences` | pass |
| R9 | `TestPackageReproductionContainsSelectorsWithoutCredentialsOrTarget`; `TestRunnerPublishesOfflineReadableFunctionalPackages` | pass |
| R10 | `TestPackageInventoryCancellationDuringHash`; `TestSignalCancellationDuringHTTPPropagatesAndPublishesConfirmedPrefix`; `TestCancellationFinalizationFailureDoesNotPublishPartialRun` | pass |

Review adversaria adicional: `TestOfflinePackageRejectsRehashedTopologyForgery` liga las evidencias de cada fact de step al report, cada fallo a su batch confirmado y cada fact de herramienta a `PlanID`+`StepID` aun cuando dos planes reutilizan el mismo ID de step. `TestOfflinePackageRejectsForgedPassingTree` rechaza assertion fallida bajo un step falsificado como passed. `TestOfflinePackageRejectsSemanticReadBetweenMatchingScans` exige SHA-256/tamaño del inventario en cada lectura semántica, también si un archivo cambia después del primer scan. Estas pruebas amplían R3, R4, R5 y R8.

`TestOfflinePackageRejectsRehashedDanglingEventIDs` comprueba que IDs de plan, constraint, step y tool call en `events.jsonl` pertenecen a la topología y a la evidencia confirmada; cubre el cierre de referencias de R8 aun con manifest recalculado.

`TestOfflinePackageRejectsContradictoryAssertionGroup` comprueba las tablas ternarias de grupos `all`/`any` usadas para derivar el estado de cada raíz, step y constraint.

Los tests nuevos residen en `internal/localvalidation/package_*_test.go`; los dos tests de cancelación y el flujo local preexistente están en `internal/localvalidation/runner_test.go`. `report.json` conserva su esquema sin expected/observed; la captura de fallos ocurre en `Runner` antes de `safeAssertions`.

## Tasks

| Task | Resultado |
|---|---|
| T1 | Esquema, límites, decodificación estricta y errores tipados. |
| T2 | Proyección de binding/cadena y outcomes deterministas. |
| T3 | Fallos hoja/grupo y valores seguros capturados antes del borrado. |
| T4 | Tabla ID→archivo y hechos desde `Store.Get`. |
| T5 | Reproducción declarativa ligada a revisión/entorno/digests. |
| T6 | Inventario SHA-256 cerrado y lector offline. |
| T7 | Publicación en staging validada antes del commit. |
| T8 | Flujo local failed/inconclusive/cancelled y gates finales. |

## Verificaciones

| Gate | Resultado |
|---|---|
| `gofmt -l` dirigido a Go modificado/nuevo | pass |
| `git diff --check` | pass |
| `go test ./...` | pass |
| `go vet ./...` | pass |
| `CGO_ENABLED=1`, `CC=<gcc WinLibs local>` `go test -race ./...` | pass |
| `go test -cover ./internal/localvalidation -count=1` tras review | 85,2 % |
| `GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOOS={windows,linux,darwin} GOARCH=amd64 go build ./...` | pass en los tres targets |

Tras las correcciones de review y el ajuste final de grupos `all`/`any` pasaron `go test ./...`, `go vet ./...`, `go test -cover ./internal/localvalidation -count=1` (85,2 %) y una race fresca de las regresiones de topología, árbol, lecturas semánticas y eventos con CGO/GCC local. La race completa dirigida a `localvalidation` pasó antes del ajuste de grupos; la race completa del repositorio y los builds offline de la implementación inicial preceden a estas correcciones. El reviewer repetirá los gates completos del snapshot final.

## Cobertura y límites

La cobertura global anterior documentada fue 89,2 %. La nueva lógica añade numerosas ramas de rechazo de paquetes maliciosos; la cobertura global medida tras la implementación fue 88,9 % (caída de 0,3 puntos), pese a que `internal/localvalidation` supera el mínimo de 85 % (85,2 % tras review). El descenso se debe al crecimiento del lector y al inventario; las rutas adversarias de bytes, paths, links, referencias, cancelación, integridad y fallo de publicación tienen tests conductuales. Se solicita al reviewer evaluar esta justificación explícita.

La reproducción v1 es declarativa y requiere preparación manual: los literales y textos arbitrarios se ocultan, se emite `credential_required` sin valor y los planes locales declaran escenario `unspecified`. El lector verifica integridad interna, no una firma de origen. El staging pertenece al productor y `os.Root` confina rutas en Windows/Linux/macOS; las dos pasadas de inventario detectan sustituciones observadas, pero un escritor hostil con acceso concurrente al staging puede alternar bytes entre lecturas. El staging es privado (`0700`) y no se ofrece una garantía de snapshot atómico frente a ese actor.


