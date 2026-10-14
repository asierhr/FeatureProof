# Tercera review de openapi-inspector

## Veredicto

**approved**

La feature cumple las precondiciones de revisión: `openapi-inspector` es la
única feature en `in_progress`, la spec conserva aprobación humana, T1–T7 están
marcadas `[x]` y existe un informe de implementación completo. La inspección
del código y los tests reales, los overlays adversariales y toda la verificación
canónica terminan sin hallazgos bloqueantes.

## Estado de los hallazgos anteriores

| Hallazgo | Resultado de la tercera review |
|---|---|
| REV-1 — Validación de campos OpenAPI obligatorios | resuelto: la matriz 3.0/3.1 rechaza campos ausentes y tipos inválidos antes de construir un contrato. |
| REV-2 — URI no local clasificada como referencia rota | resuelto: schemes, authority, query, forma opaca y network-path se deniegan como `reference_denied` antes del filesystem. |
| REV-3 — Copia superficial de `Schema.Enum` | resuelto: maps y slices anidados se clonan al normalizar y en cada snapshot; las pruebas concurrentes pasan bajo `-race`. |
| REV-4 — Canarios en locations y serialización | resuelto: paths, components, properties, headers, operation IDs y nombres dinámicos se sustituyen por ubicaciones opacas; `Error()`, `Location` y JSON no filtran los canarios. |
| REV-5 — Path URI absoluto en Windows | resuelto: `/absolute.yaml#/R` se deniega antes de canonicalizar o leer el target. El overlay observa solo dos canonicalizaciones —root y entry— y una lectura —entry—. |
| REV-6 — Siblings de `$ref` perdidos en OpenAPI 3.1 | resuelto: un Schema Object 3.1 compone target y siblings mediante `allOf`; validación, catálogo y contexto conservan `required`, `properties` y `minProperties`. OpenAPI 3.0 mantiene la semántica que ignora siblings del Reference Object. |
| REV-7 — Headers equivalentes por casing no deterministas | resuelto: el input se canonicaliza en orden estable y cualquier colisión case-insensitive se rechaza como `invalid_input`, incluidos valores iguales, distintos y `Content-Type`. |

## Trazabilidad revisada

| Requirement | Implementación y tests inspeccionados | Resultado |
|---|---|---|
| R1 | carga 3.0/3.1 JSON/YAML, decoder estricto, duplicate keys, versiones y matriz estructural en `TestLoadAcceptsOpenAPI30And31JSONAndYAML`, `TestRequiredOpenAPIFieldMatrixRejectsMissingAndWrongTypes` y `TestCompleteInspectorFlowForBothFamilies` | pass |
| R2 | resolución interna/multiarchivo, URI no local, `/absolute.yaml`, traversal, symlink, fragments, ciclos y presupuestos en `TestResolverAllowsInternalAndInRootFiles`, `TestNonLocalURIReferencesAreDeniedBeforeCanonicalizeOrRead`, `TestURIRootReferenceIsDeniedBeforeFilesystem`, `TestSymlinkEscapeIsDeniedBeforeRead` y `TestJSONPointerEscapingArraysAndFailures` | pass |
| R3 | catálogo propio, orden estable, parámetros efectivos, bodies, responses y schemas en `TestCatalogNormalizesParametersBodiesResponsesAndSchemasInStableOrder`, `TestOpenAPI31SchemaReferenceSiblingsRemainEffective` y `TestOpenAPI30ReferenceObjectIgnoresSiblings` | pass |
| R4 | seguridad global/específica/anónima, alternativas OR/AND y metadatos apiKey/http/OAuth2/OpenID sin credenciales en `TestCatalogComputesGlobalOperationAnonymousAndAlternativeSecurityWithoutCredentials` y `TestCatalogAndContextCopiesProtectNestedSecurityAndSchemas` | pass |
| R5 | errores tipados, causas preservadas, locations opacas y redacción de canarios en `TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause`, `TestDiagnosticLocationsAndSerializationNeverExposeExternalTokens` y `TestInvalidSchemaFailsLoadBeforeCatalog` | pass |
| R6 | precedencia exact/range/default, status no declarado y inputs inválidos en `TestValidateResponsePrefersExactThenRangeThenDefault` y `TestValidateResponseRejectsUnknownOperationAndUndeclaredStatus` | pass |
| R7 | headers, colisiones de casing, media types, body, límites y semántica JSON Schema 3.0/3.1 en `TestValidateResponseChecksRequiredHeadersMediaTypesAndVersionedSchemas`, `TestVersionedCompiledSchemasEnforceKeywordsAndTreatFormatsAsAnnotations`, `TestObservedHeaderCasingCollisionsAreDeterministicallyRejected`, `TestOpenAPI31SchemaReferenceSiblingsRemainEffective` y `TestOpenAPI30ReferenceObjectIgnoresSiblings` | pass |
| R8 | selección explícita, reachability, deduplicación, orden canónico y schemas 3.1 compuestos en `TestContextContainsOnlyExplicitlySelectedOperationsAndReachableDeduplicatedSchemas`, `TestContextSerializesRequestHeadersAndSecurity` y `TestOpenAPI31SchemaReferenceSiblingsRemainEffective` | pass |
| R9 | allowlist, canarios, omisiones, packing de operaciones completas y límite exacto en `TestContextOmitsUntrustedFreeTextExamplesExtensionsAndWholeOversizedOperationsWithinBudget`, `TestContextRejectsDuplicateKeysAndAccountsWholeUnitSize` y `TestConfigAndContextInputValidationRejectBeforeWork` | pass |
| R10 | cancelación sin parciales, contratos independientes, deep freeze y copias de `Schema.Enum` en `TestAllTraversalsObserveCancellationWithoutPartialOutput`, `TestResponseCancellationDuringSchemaValidationReturnsNoPartialResult`, `TestSchemaEnumIsDeeplyFrozenAndCatalogCopiesAreIndependent` y `TestContractsAreImmutableAndIndependentUnderRace` | pass |
| R11 | `SourceFS` inyectable, errores de lectura, loader JSON Schema deny-all y ausencia de cliente de red en `TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead`, `TestSymlinkEscapeIsDeniedBeforeRead`, `TestReadAndTotalDocumentBudgetsFailWithoutPartialContract` y `TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause` | pass |

Los tests anteriores ejercitan comportamiento observable y fallarían al retirar
la validación, resolución, normalización, copia, reducción o clasificación que
demuestran. No hay tests omitidos mediante `t.Skip`.

## Overlays y casos adversariales

| Caso | Resultado |
|---|---|
| overlay original REV-1: campos obligatorios y tipos OpenAPI 3.0/3.1 | pass |
| overlay original REV-2: schemes no locales rechazados | pass |
| overlay original REV-3: deep freeze de `Schema.Enum` con maps/slices anidados | pass |
| overlay original REV-4: locations, `Error()` y JSON sin canarios | pass |
| overlay independiente REV-5: schemes, authority, opaque, query, network-path, `C:/...` y `/absolute.yaml` antes de la frontera filesystem | pass |
| overlay independiente REV-6: siblings de `$ref` activos en 3.1 e ignorados en 3.0 | pass |
| tests de REV-6: validación, catálogo inmutable y contexto conservan `allOf`, `minProperties` y `properties` | pass ×100 |
| overlay independiente REV-7: headers duplicados por casing con valores iguales/distintos y `Content-Type` | pass ×5000 por caso |
| tests de REV-7 completos, incluida una sola clave con casing mixto y `Content-Type` multivalor | pass ×100 |

Los overlays están en el directorio temporal del sistema y no modifican el
repositorio.

## Verificación independiente

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=1 ./...` con UCRT64 en `PATH` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/openapi` con UCRT64 en `PATH` | pass |
| `go test -count=1 -coverprofile=<temp>/featureproof-review8-third.out ./...` | pass |
| `go tool cover -func=<temp>/featureproof-review8-third.out` | global 91,1 %; `internal/openapi` 91,4 % |
| `go mod verify` | pass: `all modules verified` |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/openapi` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/openapi` | pass |

## Arquitectura, alcance y documentación

- El paquete mantiene filesystem y compilación JSON Schema detrás de fronteras
  propias, no crea clientes de red ni goroutines y no persiste evidencias ni
  modifica runs.
- La resolución deniega referencias no locales antes de tocar el target y
  comprueba containment canónico, symlinks, profundidad, cantidad de refs,
  documentos y bytes.
- El snapshot retenido es inmutable; catálogo, schemas, enums, security y
  contexto se copian sin compartir estado mutable con callers.
- Cancelación e inputs inválidos no devuelven contratos, validaciones o
  contextos parciales.
- Todas las entidades y funciones del paquete tienen un comentario sobre su
  responsabilidad dentro del sistema.
- Las dependencias nuevas permanecen encapsuladas y `go.mod`/`go.sum` verifican
  correctamente. No se detectaron imports o clientes de red en código
  productivo.
- La implementación se limita a `internal/openapi`, sus tests, dependencias y
  artefactos documentales de la feature.
- Esta review solo actualizó este archivo; no modificó código productivo,
  tests, spec, tasks, informe del implementer ni `feature_list.json`.

## Hallazgos

No quedan hallazgos bloqueantes ni observaciones abiertas para R1–R11 o
T1–T7.
