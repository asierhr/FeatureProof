# Implementación — Redacción y limitación de evidencias

## Resultado

La feature se implementó como una frontera fail-closed compartida por harness y
executor. Una policy validada congela límite, headers y JSON Pointers; el
sanitizer produce copias tratadas y metadata de truncación; únicamente un
`evidence.Record` sellado puede entrar en `FileStore`. Las assertions de valor
que solapan datos sensibles se rechazan antes de efectos y las que dependen de
un body truncado resultan `inconclusive`.

La proyección del modelo parte de evidencia confirmada, conserva toda redacción
persistente, añade paths exclusivos del modelo y reemplaza completamente bodies
truncados. Eventos y resultados contienen IDs, presencia, paths y tamaños, sin
payloads.

## Tareas

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Policy inmutable, errores tipados y gramática JSON Pointer compartida. |
| T2 | done | Copia recursiva, `/headers` raíz, paths explícitos anidados, truncación UTF-8, cancelación e idempotencia. |
| T3 | done | Validación previa alineada con rutas sensibles estáticas y semántica intacta fuera del solapamiento. |
| T4 | done | Records sellados, metadata bajo hash, números exactos, stream estricto y executor/harness fail-closed. |
| T5 | done | Proyección monotónica desde records confirmados e integración con harness. |
| T6 | done | Metadata segura en journal y resultados de existencia sin valores. |
| T7 | done | Canarios, regresiones, race repetido, coverage y trazabilidad. |

## Trazabilidad R → tests

| Requirement | Tests concretos |
|---|---|
| R1 | `TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun`; `TestPolicyFreezesNormalizedConfiguration`; `TestPolicyAccessorsAndTypedErrors` |
| R2 | `TestSanitizerRedactsNestedInputsAndOutputsWithoutMutation`; `TestSanitizerFailsClosedForUnsupportedCyclicAndNonFiniteValues`; `TestSanitizerRejectsDirectAndIndirectNamedCollectionCycles`; `TestSanitizerCoversJSONScalarsTypedCollectionsAndMissingPaths`; `TestSanitizedValueCannotBeFabricatedOutsideEvidence`; `TestNewRecordCopiesSanitizedValuesAndRejectsPostSealMutation` |
| R3 | `TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively`; `TestNestedHeadersRequireExplicitSensitivePaths`; `TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects` |
| R4 | `TestSensitiveJSONPathsRedactExactSubtrees`; `TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive`; `TestModelProjectorRejectsUnconfirmedRecordsAndSupportsRootAndArrayRestrictions` |
| R5 | `TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects`; `TestNestedSensitiveValueAssertionRejectsWholePlan`; `TestSensitiveHeaderAncestorsRejectWholePlanBeforePolicyAndTool` |
| R6 | `TestBodyTruncationRecordsExactOriginalAndRetainedBytes`; `TestTruncationPreservesUTF8Boundary`; `TestValueAssertionOnTruncatedBodyIsInconclusive`; `TestExecutorEvaluatesStoredTruncationMetadata` |
| R7 | `TestRedactionFailurePreventsEvidencePersistenceAndPublication`; `TestFileEvidenceStoreRejectsUntreatedRecord`; `TestSanitizedValueCannotBeFabricatedOutsideEvidence`; `TestNewRecordCopiesSanitizedValuesAndRejectsPostSealMutation`; `TestPersistedMetadataIsIntegrityProtected`; `TestFileEvidenceStorePersistsRedactedRoot`; `TestFileStorePreservesExactJSONNumbersAndRejectsTrailingData` |
| R8 | `TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive`; `TestModelProjectionFailurePreventsModelCall`; `TestModelProjectorRejectsUnconfirmedRecordsAndSupportsRootAndArrayRestrictions` |
| R9 | `TestCanarySecretsNeverAppearInArtifactsEventsReportsOrModelContext`; `TestSensitiveExistenceResultContainsPresenceOnly`; `TestPersistedMetadataIsIntegrityProtected` |
| R10 | `TestSanitizationAndProjectionObserveCancellationWithoutPartialOutput`; `TestSanitizerRejectsDirectAndIndirectNamedCollectionCycles`; `TestRedactionIsDeterministicAndIdempotent`; `TestHarnessPropagatesCancellationAndPreservesConfirmedRecords` |

## Correcciones y spec revisada

- La regla aprobada revisada aplica headers implícitos solo a `/headers` raíz,
  con key estructural exacta y nombres HTTP case-insensitive. El sanitizer no
  infiere colecciones anidadas; estas requieren `sensitive_json_paths`.
- `OverlapsSensitive` representa estáticamente `/headers`, headers protegidos y
  sus descendientes. Los tests recorren el plan completo para todos los
  comparadores, aceptan solo `exists`/`absent` y verifican cero llamadas a
  policy y tool, mientras paths anidados no configurados conservan su semántica.
- REV-2: `SanitizedValue` oculta payload, completitud y metadata; `NewRecord`
  exige un resultado completo, copia profundamente y sella el contenido. Store
  y proyector revalidan el sello para rechazar mutaciones posteriores.
- REV-3: maps y slices, incluidos tipos nombrados, registran la identidad
  reflectiva original antes de copiar o descender. Hay regresiones para ciclos
  directos, indirectos y cancelación durante el recorrido.
- REV-4: race se ejecuta con el directorio UCRT64 de MSYS2 en `PATH`, CGO
  habilitado y `gcc` fijado como compilador.
- `FileStore.Get` decodifica con `UseNumber`, exige EOF tras un único record y
  conserva tokens de enteros mayores que 2^53, decimales y exponentes extremos.

## Verificación

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin archivos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; $env:CGO_ENABLED='1'; $env:CC='gcc'; go test -race ./...` | pass |
| `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; $env:CGO_ENABLED='1'; $env:CC='gcc'; go test -race -count=20 ./internal/evidence ./internal/assertion ./internal/planning ./internal/harness ./internal/jsonpath` | pass |
| `go test "-coverprofile=coverage.out" ./...` | pass |
| `go tool cover "-func=coverage.out"` | pass, 90,5 % global |

Coverage final: 90,5 % global; 91,0 % en `internal/evidence`, 90,6 % en
`internal/assertion`, 89,9 % en `internal/planning`, 88,0 % en
`internal/harness` y 100,0 % en `internal/jsonpath`. Todos los paquetes
modificados superan el mínimo de 85 % y el global supera el baseline de 90,3 %.

## Notas de alcance

No existe todavía un paquete `reporting`. La representación serializable que
consumirá esa feature queda cubierta mediante `Record`, `Event`,
`AssertionResult` y el test canario end-to-end, sin crear una capa vacía.

La ejecución race requiere activar CGO, anteponer
`C:\msys64\ucrt64\bin` a `PATH` y usar `CC=gcc`; así el linker encuentra las
dependencias UCRT64 de la toolchain además del ejecutable del compilador.
