# Implementación de openapi-inspector

## Resultado

Se añadió `internal/openapi` como paquete aislado para cargar contratos OpenAPI
3.0.x y 3.1.x desde un filesystem local autorizado, enumerar un catálogo
determinista, validar respuestas observadas y construir contexto reducido. El
paquete no crea clientes de red, no persiste evidencias ni modifica runs.

Los DTO del parser YAML y del validador JSON Schema permanecen dentro del
adapter. La API pública devuelve únicamente modelos propios. La dependencia
`github.com/santhosh-tekuri/jsonschema/v6` se usa detrás del puerto interno
`schemaValidator`: OpenAPI 3.0 se compila con Draft 4 tras normalizar
`nullable` y obligaciones `writeOnly` en dirección response; OpenAPI 3.1 se
compila con Draft 2020-12. Cualquier carga externa del validador está denegada.

## Correcciones tras primera review

- REV-1: la carga valida antes de crear `Contract` los campos obligatorios y
  tipos de Info, Operation, Response, Parameter, RequestBody, Header, content,
  components y security para 3.0/3.1, incluidos componentes no alcanzados.
- REV-2: cada `$ref` se parsea como URI; cualquier scheme, authority, query,
  forma opaca, absoluta o network-path se clasifica `reference_denied` antes de
  canonicalizar o leer un target.
- REV-3: los valores JSON de `Schema.Enum` se clonan recursivamente al
  normalizar y en cada snapshot público, incluidos objetos y arrays anidados.
- REV-4: documentos y claves externas usan índices opacos estables en errors y
  violations. `Error.MarshalJSON` excluye causas concretas que pudieran contener
  paths o payloads externos.

## Correcciones tras segunda review

- REV-5: los paths con raíz URI (`/absolute.yaml`) se deniegan mediante una
  comprobación independiente del sistema operativo antes de `Canonicalize` o
  `ReadFile`; la regresión comprueba exactamente dos canonicalizaciones —root
  y entry— y una única lectura del documento raíz.
- REV-6: la resolución distingue familia y posición. En Schema Objects 3.1,
  el target y los siblings de `$ref` se componen como `allOf`, conservando
  `required`, `properties` y `minProperties` en validación, catálogo y contexto.
  En 3.0 se mantiene la semántica de Reference Object que ignora siblings.
- REV-7: los headers observados se canonicalizan antes de seleccionar y validar
  el response. Dos claves equivalentes por casing se rechazan de forma estable
  como `invalid_input`, tanto con valores iguales como distintos; la misma
  regla cubre `Content-Type` y una sola clave sigue siendo case-insensitive.

## Requirements y tests

| Requirement | Tests | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestLoadAcceptsOpenAPI30And31JSONAndYAML`; `TestRequiredOpenAPIFieldMatrixRejectsMissingAndWrongTypes`; `TestReusableComponentsAndContentParametersRemainValid`; `TestCompleteInspectorFlowForBothFamilies` | `internal/openapi/openapi_test.go`, `internal/openapi/review_regression_test.go`, `internal/openapi/edge_test.go` | pass |
| R2 | `TestResolverAllowsInternalAndInRootFiles`; `TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead`; `TestNonLocalURIReferencesAreDeniedBeforeCanonicalizeOrRead`; `TestURIRootReferenceIsDeniedBeforeFilesystem`; `TestSymlinkEscapeIsDeniedBeforeRead`; `TestJSONPointerEscapingArraysAndFailures` | `internal/openapi/openapi_test.go`, `internal/openapi/review_regression_test.go`, `internal/openapi/review_second_regression_test.go`, `internal/openapi/edge_test.go` | pass |
| R3 | `TestCatalogNormalizesParametersBodiesResponsesAndSchemasInStableOrder`; `TestNormalizationCoversHeadersArraysUnionAndRecursiveSchemas`; `TestOpenAPI31SchemaReferenceSiblingsRemainEffective`; `TestOpenAPI30ReferenceObjectIgnoresSiblings` | `internal/openapi/openapi_test.go`, `internal/openapi/edge_test.go`, `internal/openapi/review_second_regression_test.go` | pass |
| R4 | `TestCatalogComputesGlobalOperationAnonymousAndAlternativeSecurityWithoutCredentials`; `TestCatalogAndContextCopiesProtectNestedSecurityAndSchemas` | `internal/openapi/openapi_test.go`, `internal/openapi/edge_test.go` | pass |
| R5 | `TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause`; `TestDiagnosticLocationsAndSerializationNeverExposeExternalTokens`; `TestInvalidSchemaFailsLoadBeforeCatalog`; `TestStructuralErrorMatrixRejectsMalformedOpenAPIObjects` | `internal/openapi/openapi_test.go`, `internal/openapi/review_regression_test.go`, `internal/openapi/edge_test.go`, `internal/openapi/branches_test.go` | pass |
| R6 | `TestValidateResponsePrefersExactThenRangeThenDefault`; `TestValidateResponseRejectsUnknownOperationAndUndeclaredStatus`; `TestInternalUtilitiesCoverFailureAndFallbackBranches` | `internal/openapi/openapi_test.go`, `internal/openapi/branches_test.go` | pass |
| R7 | `TestValidateResponseChecksRequiredHeadersMediaTypesAndVersionedSchemas`; `TestSchemaValidationCoversPrimitiveArrayEnumAndNullSemantics`; `TestVersionedCompiledSchemasEnforceKeywordsAndTreatFormatsAsAnnotations`; `TestNormalizationCoversHeadersArraysUnionAndRecursiveSchemas`; `TestOpenAPI31SchemaReferenceSiblingsRemainEffective`; `TestOpenAPI30ReferenceObjectIgnoresSiblings`; `TestObservedHeaderCasingCollisionsAreDeterministicallyRejected`; `TestSchemaCountKeywordConversionRejectsInvalidValues` | `internal/openapi/openapi_test.go`, `internal/openapi/edge_test.go`, `internal/openapi/review_second_regression_test.go` | pass |
| R8 | `TestContextContainsOnlyExplicitlySelectedOperationsAndReachableDeduplicatedSchemas`; `TestContextSerializesRequestHeadersAndSecurity`; `TestOpenAPI31SchemaReferenceSiblingsRemainEffective` | `internal/openapi/openapi_test.go`, `internal/openapi/branches_test.go`, `internal/openapi/review_second_regression_test.go` | pass |
| R9 | `TestContextOmitsUntrustedFreeTextExamplesExtensionsAndWholeOversizedOperationsWithinBudget`; `TestContextRejectsDuplicateKeysAndAccountsWholeUnitSize`; `TestConfigAndContextInputValidationRejectBeforeWork` | `internal/openapi/openapi_test.go`, `internal/openapi/edge_test.go` | pass |
| R10 | `TestAllTraversalsObserveCancellationWithoutPartialOutput`; `TestResponseCancellationDuringSchemaValidationReturnsNoPartialResult`; `TestSchemaEnumIsDeeplyFrozenAndCatalogCopiesAreIndependent`; `TestContractsAreImmutableAndIndependentUnderRace` | `internal/openapi/openapi_test.go`, `internal/openapi/branches_test.go`, `internal/openapi/review_regression_test.go` | pass |
| R11 | `TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead`; `TestSymlinkEscapeIsDeniedBeforeRead`; `TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause`; `TestReadAndTotalDocumentBudgetsFailWithoutPartialContract` | `internal/openapi/openapi_test.go`, `internal/openapi/edge_test.go`, `internal/openapi/branches_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Modelos propios, configuración explícita, errores seguros y `SourceFS`. |
| T2 | done | Parser JSON/YAML estricto, referencias locales, containment y presupuestos. |
| T3 | done | Catálogo estable, parámetros efectivos, schemas y security normalizados. |
| T4 | done | Selección exact/range/default y validación de headers, media type y schema. |
| T5 | done | Contexto allowlist, schemas deduplicados, packing por unidad y omisiones. |
| T6 | done | Fixtures 3.0/3.1, fakes de filesystem, canarios, recursion y JSON Pointer. |
| T7 | done | Flujo integral, trazabilidad y verificación canónica completa. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=1 ./...` con UCRT64 en `PATH` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/openapi` con UCRT64 en `PATH` | pass |
| `go test -count=1 -coverprofile=<temp>/featureproof-feature8-review2-final.out ./...` | pass |
| `go tool cover -func=<temp>/featureproof-feature8-review2-final.out` | pass |
| `go mod verify` | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o <temp>/featureproof-openapi-linux.test ./internal/openapi` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c -o <temp>/featureproof-openapi-darwin.test ./internal/openapi` | pass |

Los perfiles de coverage se escribieron en el directorio temporal del sistema,
fuera del repositorio.

## Coverage

- Global: **91,1 %** de statements; el baseline previo de 91,1 % se preserva.
- Código nuevo `internal/openapi`: **91,4 %** de statements.
- Ramas relevantes: versiones/formatos, sintaxis duplicada, scope/ref limits,
  symlink escape, response selection, headers/media/schema, context budget,
  cancelación coordinada, matriz estructural, URIs no locales, locations opacas,
  copias JSON profundas, rutas URI-root, `$ref` siblings 3.1, colisiones de
  headers y concurrencia cubiertas.

## Dependencias contrastadas

- `go.yaml.in/yaml/v3`: se usa solo para construir un AST local; la conversión
  propia rechaza mappings duplicados, documentos múltiples y claves no string.
- `github.com/santhosh-tekuri/jsonschema/v6`: soporta Draft 4 y Draft 2020-12;
  sus tipos no forman parte de la API del paquete y su loader externo se
  sustituye por uno deny-all. Los formatos se conservan como annotations y no
  se presentan como comprobados.

## Riesgos o limitaciones

- Los formatos JSON Schema son annotations en esta entrega; no se emite una
  afirmación de conformidad de formato sin semántica específica probada.
- Una observación con dos claves de header equivalentes por casing se considera
  ambigua y se rechaza como `invalid_input`, incluso si sus valores coinciden.
- La feature expone el inspector como paquete interno. Integrarlo en la CLI o
  inferir operaciones desde constraints pertenece a features posteriores según
  la spec aprobada.
