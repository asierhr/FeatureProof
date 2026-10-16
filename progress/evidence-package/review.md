# Review independiente: evidence-package

## Veredicto

**approved**. La feature 15 es la única `in_progress`; la spec registra aprobación humana y T1–T8 están completas. Se inspeccionaron el diff, todos los Go nuevos y el informe de implementación. Los hallazgos comunicados durante la review sobre procedencia de facts, batch, estados, lecturas semánticas y eventos fueron corregidos y tienen regresiones adversarias. No quedan hallazgos bloqueantes.

## Trazabilidad verificada

| Requirement | Tests observados y comprobación |
|---|---|
| R1 | `TestPackageV1StableFieldsAndExplicitSections`, `TestPackageV1RejectsVersionAmbiguousAndIncompleteShapes`, `TestRunnerPublishesOfflineReadableFunctionalPackages`: esquema v1, secciones explícitas y versión desconocida. |
| R2 | `TestPackageProjectionUsesSealedIdentityAndDeterministicStatus`, `TestOfflinePackageRejectsRehashedArtifactTampering`, `TestOfflinePackageRejectsRehashedEvidenceTampering`: binding, cadena y registros del mismo run. |
| R3 | `TestPackageCapturesFailedTreeBeforePayloadRemoval`, `TestOfflinePackageRejectsRehashedTopologyForgery`: facts deterministas, estatus y procedencia frente a referencias alteradas. |
| R4 | `TestPackageCapturesFailedTreeBeforePayloadRemoval`, `TestPackageFailurePreservesConfirmedTruncationState`, `TestOfflinePackageRejectsRehashedTopologyForgery`, `TestOfflinePackageRejectsContradictoryAssertionGroup`: fallo hoja/grupo, valores seguros, batch y árbol `all`/`any`. |
| R5 | `TestOfflinePackageInventoryAndMutation`, `TestOfflinePackageRejectsForgedManifestEntries`, `TestOfflinePackageRejectsSymlinkAndCancellation`, `TestOfflinePackageRejectsDetectableHardLink`, `TestOfflinePackageRejectsSemanticReadBetweenMatchingScans`: inventario cerrado, bytes exactos, rutas/enlaces y sustitución entre pasadas. |
| R6 | `TestPublisherCommitsOnlyValidatedPackage`, `TestPublisherDoesNotCommitWhenPackageWriteOrValidationFails`, `TestPublisherCancellationDuringStagingNeverCommits`, `TestPublisherRejectsCollisionWithoutOverwrite`: staging validado y commit sin reemplazo. |
| R7 | `TestSafeAssertionValuesRejectSecretsAndUnsupportedNumbers`, `TestPackageFailurePreservesConfirmedTruncationState`, `TestPublisherCommitsOnlyValidatedPackage`: canarios, redacción, límites y rechazo de proyecciones inseguras. |
| R8 | `TestOfflinePackageRejectsCrossDocumentTampering`, `TestOfflinePackageRejectsRehashedTopologyForgery`, `TestOfflinePackageRejectsForgedPassingTree`, `TestOfflinePackageRejectsRehashedDanglingEventIDs`, `TestOfflinePackageRejectsSemanticReadBetweenMatchingScans`: refs y estados entre índice, plan, report, eventos y evidencia; el lector no llama red ni herramientas. |
| R9 | `TestPackageReproductionContainsSelectorsWithoutCredentialsOrTarget`, `TestRunnerPublishesOfflineReadableFunctionalPackages`: selectores y digests declarativos, sin target ni credencial en claro. |
| R10 | `TestPackageInventoryCancellationDuringHash`, `TestSignalCancellationDuringHTTPPropagatesAndPublishesConfirmedPrefix`, `TestCancellationFinalizationFailureDoesNotPublishPartialRun`: cancelación, resultado operativo y ausencia de final parcial. |

Los tests adversarios mutan artefactos y rehacen el manifest; por ello prueban las relaciones semánticas además de la detección trivial de un digest desactualizado. El paquete distingue `failed`, `inconclusive` y `cancelled`, y `report.json` sigue sin expected/observed.

## Gates del snapshot final

| Comprobación | Resultado |
|---|---|
| `go test -count=1 ./...` | pass |
| `go vet ./...` | pass |
| `CGO_ENABLED=1`, `CC=C:\Users\asier.haro\Documents\Codex\2026-10-15\c-users-asier-haro-desktop-proyectos\work\toolchain\winlibs\mingw64\bin\gcc.exe`, `go test -race ./...` | pass |
| `go test -coverprofile=<TEMP> ./...` y `go tool cover -func=<TEMP>` | pass; global **88,9 %**, `internal/localvalidation` **85,2 %** |
| `GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOOS={windows,linux,darwin} GOARCH=amd64 go build ./...` | pass en los tres targets |
| `git diff --check` y `gofmt -l` sobre todos los Go nuevos/modificados | pass; cero archivos tocados sin formato |
| `gofmt -l .` literal | enumera 135 Go preexistentes fuera de la feature; baseline previo, no atribuible a feature 15 |

**Justificación de cobertura global aprobada explícitamente.** El descenso de 89,2 % a 88,9 % es de 0,3 puntos; el paquete modificado supera 85 % y las ramas materiales del lector, publicación, integridad, cancelación y redacción tienen pruebas conductuales adversarias. No se justifica añadir tests triviales para compensar un cambio global tan pequeño. El fallo literal de formato global corresponde al baseline de 135 archivos ajenos; los Go de esta feature están formateados.

## Límite del contrato

El manifest prueba integridad respecto a los bytes recibidos, no autenticidad del origen. Las instrucciones de reproducción exigen preparación manual y omiten literales y credenciales. Esto coincide con el alcance v1 aprobado.
