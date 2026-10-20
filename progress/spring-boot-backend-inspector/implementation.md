# Implementación — Inspector estático de backends Spring Boot

## Resultado

Se implementó `internal/springinspector` como fuente hermética
`spring-static-v1`. La composición sella repository/revision, source sets,
changed paths, Java release, generated policy, límites y `ProjectionPolicy`
antes de entregar bytes al parser. El parser recibe exclusivamente `[]byte` y
no dispone de filesystem, classpath, procesos, red, environment, credenciales
ni callbacks de build.

La lista v1 reconoce por identidad FQCN exacta `RestController`, casos
demostrables de `Controller`+`ResponseBody`, `RequestMapping` y los cinco
mappings verbales. Compone prefixes, aliases, arrays, constantes literales,
consumes/produces, parámetros Spring, DTOs locales, records, enums y
contenedores acotados. Lo dinámico, ambiguo, externo, cíclico o sujeto a
serialización custom se omite o degrada mediante diagnósticos seguros.

`PreAuthorize`, `Secured` y `RolesAllowed` se proyectan como
`DeclarativeConstraint` neutral. Forman parte de canonicalización, claims y
digest, pero no crean `SecurityRequirement`, schemes, credenciales, tools,
targets ni permisos de red. La proyección elimina la unidad completa cuando
contiene un valor prohibido.

La integración productiva añade configuración Spring explícita al workflow.
CLI, API, MCP y GitHub alcanzan el mismo `ContractReducer` compartido. El
reducer vuelve a verificar repository/head, limita roots a source sets
preautorizados y conserva changed paths. La lectura local reutiliza
`goinspector.LocalRepositoryFS`, la frontera descriptor-relative/no-follow y
TOCTOU ya verificada en feature 22; los bytes resultantes atraviesan el puerto
pasivo `springinspector.RepositoryFS`.

## Trazabilidad R → tests

| Requirement | Evidencia principal |
|---|---|
| R1 | `TestSpringInspectionRejectsUnsealedRevisionSourceSetsAndEscapingPaths`; `TestSnapshotAndInputOwnMutableAliases`; `TestWorkflowSpringReducerRejectsRevisionAndRootDriftBeforeInspection` |
| R2 | `TestSpringInspectionNeverImportsProcessNetworkEnvironmentOrHostFilesystem`; `TestFreezeValidationPrecedesIOAndSourceClassifiesFailures` |
| R3 | `TestMappingsComposeAliasesArraysConstantsAndPrefixes`; `TestMappingsResolveLocalConstantsAndArrays`; `TestDynamicAndLookalikeAnnotationsDoNotCreateOperations` |
| R4 | `TestMappingsComposeAliasesArraysConstantsAndPrefixes` cubre path/query, aliases y requiredness; los casos dinámicos se omiten |
| R5 | `TestLocalDTORecordsEnumsNestedAndGenericsProjectBoundedly`; `TestSchemaContainersFieldsCyclesAndUnsupportedSerialization` |
| R6 | `TestRequestMappingMethodsResponseStatusAndControllerResponseBody`; `TestMappingsComposeAliasesArraysConstantsAndPrefixes` |
| R7 | `TestSecurityAnnotationsRemainStructuredNonExecutableData`; `TestSpringErrorsDiagnosticsEventsAndCatalogExcludeCanaries`; tests `DeclarativeConstraints` del catálogo |
| R8 | `TestSpringInspectorLoadsAsDerivedContractSource`; `TestEverySpringClaimUsesItsExactDeclarationProvenance` |
| R9 | `TestDynamicAndLookalikeAnnotationsDoNotCreateOperations`; `TestMappingAliasesDynamicCrossProductAndDiagnostics`; `TestSpringErrorsDiagnosticsEventsAndCatalogExcludeCanaries` |
| R10 | `TestChangedDTOMarksDependentOperations`; `TestSourceSetIsolationPreventsCrossSetDTOResolution`; changed constant/import; `TestWorkflowSpringReducerRejectsRevisionAndRootDriftBeforeInspection` |
| R11 | `TestInspectionCancellationAcrossPhases`; `TestPermutationAndConcurrentInspectionRemainDeterministic`; límites de token/cross-product/schema/symbol/diagnostic; repetición y race |
| R12 | `TestSpringInspectorLoadsAsDerivedContractSource`; `TestWorkflowSpringReducerUsesExplicitRevisionSourceSetsAndSelections`; `TestWorkflowContractReducerSelectsSpringOnlyWhenExplicit` |
| R13 | `TestInspectionEventsContainOnlyBoundedMetadata`; boundary AST de imports, canarios, copias profundas, errores seguros, matriz parser y gates finales |

## Gates ejecutados

- `go test -count=20 ./internal/springinspector ./internal/contractcatalog`: pasa.
- `go test -count=10 ./cmd/featureproof ./internal/validationworkflow ./internal/githubpr`: pasa.
- `go test -race -count=2 ./internal/springinspector ./internal/contractcatalog ./cmd/featureproof` con `C:\msys64\ucrt64\bin`: pasa sin carreras.
- `go vet` en inspector, catálogo, CLI, workflow y GitHub: pasa.
- `go test -run '^$' ./...`: compile-only global pasa.
- `git diff --check`: pasa; solo aparecen avisos de conversión LF/CRLF del checkout.
- `internal/springinspector`: 88,4 % statements.
- Funciones de composición modificadas: `workflowContractReducerFor` 100 %, `workflowSpringRepository.ReadSnapshot` 100 % y `workflowSpringContractReducer.Reduce` 90,5 %.
- `contractcatalog` completo: 85,1 %; `canonicalOperation` 88,6 % y `ClaimsForOperation` 97,2 %. Los branches nuevos de constraints tienen tests directos de canonicalización, clone, claim y redacción.

## Límites conservadores

No se ejecutan Maven, Gradle, javac, annotation processors, tests ni la
aplicación. No se leen build files, resources, bytecode, classpath, caches o
environment. V1 no interpreta custom composed annotations, SpEL, jerarquías de
roles, Jackson custom, Lombok, reflection ni tipos externos. Un mapping sin
method/path estáticos no crea identidad; una ausencia derivada nunca se eleva a
contrato completo o confianza alta.

T1–T13 están completas. El implementer no modifica el estado de la feature y
solicita revisión independiente.

- `go test ./...`: todos los paquetes pasan salvo `internal/localvalidation/.../evidence_symlink`, fallo ambiental histórico de Windows por ausencia de privilegio de symlink; el paquete Spring y todos los afectados pasan.

## Segunda iteración — hallazgos REV-1..REV-11

| Hallazgo | Cambio aplicado | Prueba principal |
|---|---|---|
| REV-1 | `Configuration` sella `RootIdentity` y la allowlist exacta `SupportedFrameworkIdentities`; ambos entran en el digest. `FreezeConfiguredRepository` rechaza bytes fuera de source sets activos después del callback. | `TestReviewRootFrameworkAndReturnedScopeAreSealed` |
| REV-2 | El parser conserva colisiones de import como ambigüedad, omite unidades con explicit/wildcard conflicts y el índice elimina FQCN duplicados sin ganador. | `TestReviewAmbiguousImportsAndDuplicateTypesNeverChooseByOrder` |
| REV-3 | `annotationAttributes` valida aliases `path/value` y `name/value`; `constantStringExpression` evalúa solo concatenaciones de literales/referencias estáticas acotadas y el closure conserva sus archivos. | `TestReviewAliasConflictAndConstantConcatenation` |
| REV-4 | `methodHasResponseBody` decide por método usando RestController, ResponseBody efectiva o ResponseEntity exacto; cada método de vista se diagnostica y omite. | `TestReviewControllerResponseBodyIsResolvedPerMethod` |
| REV-5 | Schemas de clases usan solo record components y fields públicos presentes; Lombok/Jackson degradan. Containers y wrappers requieren FQCN JDK/Spring resuelto y los homónimos locales permanecen DTOs. | `TestReviewDTOAndWrapperIdentityRemainDemonstrable`, `TestReviewSchemaAndSecurityOmissionsProduceSafeDiagnostics` |
| REV-6 | `responseEntityStatuses` inspecciona únicamente expresiones `return`, conserva todos los selectors de branches y no mezcla builders muertos ni lookalikes. | `TestReviewResponseEntityBranchesAndDeadCalls` |
| REV-7 | Límites/cancelación tipados se propagan inmediatamente; index, métodos, mappings, params, security, schemas y closure tienen checkpoints. | `TestReviewTokenLimitPropagatesAfterValidUnit` y suite de cancelación existente |
| REV-8 | Schema missing/cycle/custom/depth y security dynamic/forbidden producen diagnósticos cerrados y acotados con safe location. | `TestReviewSchemaAndSecurityOmissionsProduceSafeDiagnostics` |
| REV-9 | `execute` recompone admission y prepared material inmediatamente antes de consumir aprobación y antes de cualquier tool call; drift termina inconcluso. | `TestDurableApprovalRevalidatesContractImmediatelyBeforeToolExecution` y `TestAdmissionContextDriftIsRejectedAcrossOwners` |
| REV-10 | `contract_sources` es una lista explícita, cerrada y sin duplicados; `workflowCombinedContractReducer` construye dinámicamente un registry/request batch OpenAPI/Go/Spring único, con merge/policy comunes. | `TestWorkflowCombinedContractReducerCoalescesSources`, `TestWorkflowCombinedContractReducerDeniesConflictingIdentity`, `TestWorkflowContractSourceListValidationCoversClosedAdapters` |
| REV-11 | `SourceSummary` y `Provenance` incluyen repository/revision; projection, registry validation, canonical sort, claims y deep-copy los preservan. | `TestRepositoryRevisionProvenanceParticipatesInCanonicalDigest`, `TestEverySpringClaimUsesItsExactDeclarationProvenance` |

## Gates de la segunda iteración

- `go test ./internal/springinspector ./internal/contractcatalog ./internal/validationworkflow ./cmd/featureproof -count=1`: PASS.
- `go test -count=20 ./internal/springinspector ./internal/contractcatalog`: PASS.
- `go test -count=10 ./internal/validationworkflow ./cmd/featureproof ./internal/githubpr`: PASS.
- UCRT64 `go test -race -count=2` sobre inspector, catálogo, workflow y CLI: PASS.
- `go vet` sobre inspector, catálogo, workflow y CLI: PASS.
- `go test -run '^$' ./...`: PASS.
- Cobertura: `springinspector` 88.4%, `contractcatalog` 85.0%, `validationworkflow` 86.1%; funciones productivas tocadas en CLI: lista/selector 100%, combined reducer 89.7%, Spring reducer 90.5%, repository bridge 100%.
- Los únicos límites deliberados siguen siendo los declarados en la spec: parser Java v1 pasivo, sin classpath/build/runtime, y degradación de construcciones fuera de la lista cerrada.
- `go test ./...`: todos los paquetes pasan salvo el fallo histórico de privilegio de symlink en `internal/localvalidation/.../evidence_symlink` bajo Windows (`A required privilege is not held by the client`); no pertenece al alcance de feature 23.

## Tercera iteración — hallazgos REV-12 y REV-13

| Hallazgo | Cambio aplicado | Prueba principal |
|---|---|---|
| REV-12 | `GitHubGateway.Submit` valida el snapshot y persiste en `StartCommand` una `GitHubAuthorityReference` completa y copiada: instalación, repositorio, PR, autor, estado, head repository, fork, `head`, changed paths y fingerprint. `GitHubRefreshingAdmission` usa un puerto autenticado propiedad de composición para refrescar esa autoridad en cada `Build`; por tanto `restorePrepared` la consulta antes de `Approval.Decide` y nuevamente dentro de `execute`, antes de consumir el grant o llamar tools. Cualquier diferencia falla cerrada. La composición GitHub inyecta un `RESTAdapter` autorizado y la referencia sobrevive al journal/reinicio. | `TestGitHubAuthorityRefreshFailsClosedAcrossRestart` muta por separado `head` y changed paths tras `Submit`, crea otro `Service`/owner sobre el store durable y demuestra cero `Approval.Decide`, `Consume` y `Execution`. `TestGitHubAuthorityRefreshAllowsExactRestartedExecution` cubre el caso estable con cuatro refreshes y una ejecución. `TestGitHubAuthorityRefreshRunsAgainBeforeTools` muta tras `Approval.Decide` y demuestra cero consume/tools. |
| REV-13 | `parseNestedTypes` recibe la unidad/imports, calcula `customSerialization` y `unsupportedShape` con las mismas reglas exactas del tipo superior y recurre en anidamientos profundos. También ignora el token `.class` como declaración y resuelve nombres relativos como `Envelope.Reply` bajo el owner léxico. Lombok, Jackson y métodos `serialize`/`deserialize` degradan el schema completo con diagnóstico `unsupported_construct`; annotations homónimas locales no adquieren semántica externa. | `TestNestedDTOUnsupportedShapesAreOmitted` cubre Lombok antes/después del método, Jackson custom, método serializer y nesting profundo. `TestNestedLookalikeAnnotationsDoNotTriggerFrameworkDegradation` cubre negativos locales. |

## Gates de la tercera iteración

- Focal REV-12: `go test ./internal/validationworkflow -run 'TestGitHubAuthority' -count=20`: PASS.
- Focal REV-13: `go test ./internal/springinspector -run 'TestNested' -count=20`: PASS, incluido race.
- Repetición: Spring/catálogo ×20 y workflow/CLI/GitHub ×10: PASS.
- UCRT64 `go test -race -count=2` sobre Spring, catálogo, workflow, CLI y GitHub: PASS; focales nuevas race ×20: PASS.
- `go vet` en todos los paquetes afectados, compile-only global, `gofmt -l` y `git diff --check`: PASS; los únicos mensajes son avisos LF/CRLF del checkout.
- Cobertura: `springinspector` 88,7 %, `validationworkflow` 86,4 %; `parseNestedTypes` 92,3 %, `schemaFor` 88,5 % y todas las funciones nuevas de `github_authority.go` 100 %. `composeWorkflowCLIWithGitHubAuthority` queda en 85,2 %.

T3, T6, T12 y T13 se reabrieron durante la corrección y vuelven a estar completas con esta evidencia. REV-1..REV-11 continúan cerradas. La feature permanece `in_progress` y se solicita una nueva revisión independiente sin modificar `feature_list.json`.

## Cuarta iteración — REV-12 productivo

La tercera revisión confirmó la persistencia/checkpoints de REV-12 y el cierre de REV-13, pero detectó que el `RESTAdapter` productivo no obtenía changed paths. `RESTAdapter.PullRequest` ahora construye una única observación coherente mediante metadata → colección completa de files → metadata. Extrae y valida el `full_name` del repositorio base, consulta con installation token `/repos/{owner}/{repo}/pulls/{number}/files`, y solo sella el snapshot si repository/PR/head/autor/estado/fork y ruta del repositorio permanecen idénticos antes y después de la paginación.

`RESTConfig` y `ProductionConfig` exigen `MaxPages` y `MaxChangedPaths`; cada página sigue limitada además por `MaxResponse`. La colección rechaza páginas de más de 100 elementos, paginación inconclusa, `Link` mal formado/no HTTPS/duplicado, next con página vacía, duplicados entre páginas, paths absolutos/no canónicos/backslash/colon/traversal y exceso de paths. Los paths válidos se ordenan antes de `SealSnapshot`, por lo que permutaciones de GitHub conservan el fingerprint. Cancelación y errores HTTP se propagan con categorías cerradas.

| Hallazgo | Cambio aplicado | Prueba principal |
|---|---|---|
| REV-12 residual | El mismo `githubpr.RESTAdapter` conectado en `main.go` incluye los changed paths reales y doble lectura contra TOCTOU; el coordinator, `GitHubGateway.Submit` y cada `GitHubRefreshingAdmission.Build` reciben snapshots sellados con esa colección. | `TestRESTPullRequestReadsEveryChangedPathPage`, `TestRESTPullRequestPathOrderDoesNotChangeAuthority`, `TestRESTPullRequestRejectsFilesAnomaliesAndLimits`, `TestRESTPullRequestDetectsChangedPathsWithStableHeadAndCancellation` |
| REV-12 vertical | Un servidor TLS local sirve los endpoints productivos, se crea el adapter real, se hace `Submit`, se muta únicamente files conservando head, se reabren workflow/artifact/executable stores con otro owner y `Decide` falla antes de aprobación o tools. | `TestProductionRESTChangedPathDriftStopsRestartedWorkflow` |

### Gates de la cuarta iteración

- Focal REST y vertical ×20: PASS.
- `githubpr` completo ×5; workflow/CLI ×10; Spring/catálogo ×20: PASS.
- UCRT64 `-race -count=2` sobre GitHub, workflow, CLI, Spring y catálogo: PASS.
- `go vet` afectado, compile-only global, `gofmt -l` y `git diff --check`: PASS.
- Cobertura: `githubpr` 85,8 %, `validationworkflow` 86,5 %; `PullRequest` 86,7 %, `readPullRequestMetadata` 100 %, `readChangedPaths` 93,5 %, `canonicalGitHubChangedPath` 100 % y `strictNextLink` 91,3 %.

T12/T13 se reabrieron al iniciar esta corrección y vuelven a completas. No se modificó `feature_list.json`; la feature permanece `in_progress` para revisión independiente.
