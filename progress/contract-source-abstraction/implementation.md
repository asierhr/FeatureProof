# Implementación — Feature 21: fuentes de contrato independientes de OpenAPI

## Resultado

La cuarta entrega cierra REV-14 y conserva REV-1–REV-13. `internal/contractcatalog` mantiene la autoridad neutral v1 en snapshots sellados: `SourceRequest`, `OperationCatalog`, `Policy` y `ReducedContext` aíslan sus colecciones, recalculan bindings canónicos y rechazan incoherencias antes de inferencia, aprobación o ejecución.

La composición productiva exige una policy completa. `SensitiveNames`, `SensitivePaths`, `SensitiveHeaderNames`, `ForbiddenValues`, esquemas URL y límites llegan desde configuración confiable sin defaults. OpenAPI conserva en privado defaults/examples excluidos hasta aplicar esa policy; nunca entran en catálogo o contexto. La prueba vertical recorre `composeWorkflowCLI` y `start` para path, logical ID, header, parameter, enum, default/example, OAuth URL, scope y response, sin red/modelo/tools, approval ni persistencia sensible.

La compatibilidad OpenAPI propaga el `context.Context` recibido y transporta security schemes alcanzables para operaciones protegidas 3.0/3.1. El límite de capacidades se prueba con un adapter adversario efectivo y una comprobación AST: core y adapter OpenAPI no poseen puertos de red, credenciales, procesos ni tools.

El recorrido privado de defaults/examples es iterativo y all-or-nothing. Antes de publicar el `Contract` limita profundidad, nodos totales y pendientes, bytes y valores usando los presupuestos explícitos del loader; comprueba cancelación en cada paso y detecta ciclos. Toda cancelación de la compatibilidad y del adapter neutral se proyecta como `contractcatalog.CategoryCancelled` con ubicación estructural fija y causa compatible con `errors.Is`.

## Cierre de cuarta revisión

| Blocker | Evidencia de cierre |
|---|---|
| REV-14 | `TestExcludedValueTraversalIsDeterministicCancelableAndAllOrNothing` cubre cancelación y deadline; `TestExcludedValueTraversalRejectsEveryWorkDimensionAndCycles` cubre depth/nodes/bytes/values/ciclos; `TestLoaderPublishesNoContractWhenExcludedTraversalExhausts` prueba publicación atómica; `TestNeutralCompatibilityReturnsSafeTypedCancellation` exige categoría, ubicación, unwrap y error sin payload. |

## Cierre de blockers de tercera revisión

| Blocker | Evidencia de cierre |
|---|---|
| REV-10 | `SourceDescriptor` se sella mediante `NewSourceRequest`; registry conserva una copia privada y entrega otra al adapter. `TestSourceRequestFreezesProjectionAgainstAdapterAndConcurrentCallerMutation` muta las cinco slices desde adapter y caller concurrente, y pasa con `-race`. |
| REV-11 | `workflowProjectionConfig` exige todos los campos y deriva `ProjectionPolicy` validada. `TestComposedWorkflowRejectsConfiguredSensitiveContractSurfacesBeforeEffects` atraviesa composición y CLI para nueve superficies, verifica cero red/modelo/tool/approval y ausencia del valor en output, errores y stores. |
| REV-12 | `NeutralizeReducedContext(ctx, ...)` no crea contextos de fondo y conserva schemes alcanzables. `TestNeutralCompatibilityPreservesProtectedSchemesAndCancellation` cubre OAS 3.0.3/3.1.0, OAuth y cancelación; `TestNeutralCompatibilityAdapterPreservesRetainedSelection` conserva la regresión anónima. |
| REV-13 | `TestCoreAndAdaptersOwnNoExternalCapabilityPorts` combina adapter adversario con frontera AST. `scripts/verify_feature21_coverage.ps1` exige >=85 % en siete paquetes y en `policy`, `loadWorkflowConfig`, `composeWorkflowCLI` y `Reduce`; focales ×20 y race UCRT64 pasan. |

## Cierres anteriores conservados

| Blocker | Evidencia principal |
|---|---|
| REV-1 | `TestAuthorityBindingsAreImmutableAndSelfVerifying`. |
| REV-2 | `TestOpenAPIAdapterPreservesNormalizedCatalogSecurityResponsesAndOmissions`; paridad 3.0/3.1. |
| REV-3 | `TestRegistryPrevalidatesIdentityAndCoalescesExactDuplicates`. |
| REV-4 | `TestRegistryReservesEveryAggregateDimensionBeforeNextSource`; límites completos del reducer. |
| REV-5 | `TestCanonicalizationIgnoresEverySetLikePermutationAndDetectsLogicalIdentityConflict`. |
| REV-6 | `TestClaimPolicyUsesAnySufficientProvenanceAndPreservesTypedDecisions`. |
| REV-7 | `TestWorkflowContractReducerUsesOnlyTrustedStructuredSelections`. |
| REV-8 | `TestProjectionRejectsConfiguredSensitiveMaterialAcrossStructuralSurfaces`. |
| REV-9 | Sustituido y reforzado por la evidencia efectiva de REV-13. |

## Trazabilidad R → tests reales

| Requirement | Evidencia principal |
|---|---|
| R1 | `TestPlannerConsumesOnlyVersionedNeutralCatalog`; `TestUnsupportedVersionsAndPartialPoliciesFailBeforeSelection`; tests de frontera de imports/tipos públicos. |
| R2 | `TestAllSourceClassesProduceCommonSnapshot`; `TestRegistryPrevalidatesIdentityAndCoalescesExactDuplicates`; `TestSourceRequestFreezesProjectionAgainstAdapterAndConcurrentCallerMutation`. |
| R3 | `TestAuthorityBindingsAreImmutableAndSelfVerifying`; `TestOpenAPIAdapterPreservesNormalizedCatalogSecurityResponsesAndOmissions`. |
| R4 | `TestRegistryRejectsUnknownDuplicateAndAuthorityElevation`; `TestClaimPolicyUsesAnySufficientProvenanceAndPreservesTypedDecisions`; `TestPolicyCopiesMutableConfiguration`. |
| R5 | `TestOpenAPIAdapterAcceptsSupportedFamiliesAndFormats`; `TestNeutralCompatibilityPreservesProtectedSchemesAndCancellation`; suite de response validation OpenAPI. |
| R6 | `TestCanonicalizationIgnoresEverySetLikePermutationAndDetectsLogicalIdentityConflict`; `TestMergeIsOrderIndependentAndCoalescesEqualClaims`; repetición ×20. |
| R7 | `TestMethodPathSchemaSecurityAndResponseConflictsAreExplicit`; `TestConflictedOperationFailsClosedButUnselectedOperationRemainsUsable`. |
| R8 | `TestPolicyReturnsEligibleInsufficientOrReviewRequired`; `TestTypedContractDecisionStopsAdmissionBeforeLaterAuthorityPorts`; tests de drift neutral. |
| R9 | `TestReducedContextContainsOnlyEligibleSelectedClaimsAndBindsDigests`; `TestReducerFailsClosedForEveryBoundedDimension`; materialización de `agentplanning`. |
| R10 | `TestRegistryReservesEveryAggregateDimensionBeforeNextSource`; `TestCancellationDuringSourceAndReductionTraversalReturnsNoPartialState`; `TestExcludedValueTraversalRejectsEveryWorkDimensionAndCycles`; `TestCoreAndAdaptersOwnNoExternalCapabilityPorts`. |
| R11 | `TestWorkflowContractReducerUsesOnlyTrustedStructuredSelections`; `TestComposedWorkflowRejectsConfiguredSensitiveContractSurfacesBeforeEffects`; suites planner/approval/replanning/workflow/CLI; `validate --plan` sin cambios. |
| R12 | `TestNeutralCompatibilityReturnsSafeTypedCancellation`; tests de redacción vertical, autoridad sellada, cancelación, límites, errores tipados, comments y gates canónicos. |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Modelo neutral privado, canónico y versionado. |
| T2 | done | Registry sellado, adapter copy, prevalidación y frontera de capacidades. |
| T3 | done | Adapter OpenAPI 3.0/3.1 con schemes, claims y provenance por nodo. |
| T4 | done | Merge determinista, canonización y conflictos explícitos. |
| T5 | done | SufficiencyPolicy completa y resultados cerrados. |
| T6 | done | Reducer acotado, verificable, cancelable y sin aliases. |
| T7 | done | Planner migrado al contexto neutral. |
| T8 | done | Approval/replanning ligados a catálogo, policy y retained claims. |
| T9 | done | Workflow bloquea decisiones insuficientes antes de efectos. |
| T10 | done | Composición OpenAPI productiva con configuración explícita y compatibilidad. |
| T11 | done | Regresiones afectadas ejecutadas. |
| T12 | done | Trazabilidad, seguridad, comentarios, coverage y gates documentados. |

## Verificación ejecutada

```text
go test ./internal/contractcatalog ./internal/openapi ./cmd/featureproof -count=20 -timeout=8m
PASS

PATH=C:\msys64\ucrt64\bin;%PATH% CGO_ENABLED=1 go test -race ./internal/contractcatalog ./internal/openapi ./internal/agentplanning ./internal/planapproval ./internal/replanning ./internal/validationworkflow ./internal/agenteval ./cmd/featureproof -count=1 -timeout=12m
PASS

powershell -NoProfile -ExecutionPolicy Bypass -File scripts/verify_feature21_coverage.ps1
contractcatalog 87,7%; openapi 91,5%; agentplanning 87,3%; planapproval 86,4%;
replanning 85,2%; validationworkflow 85,9%; agenteval 91,2%;
policy 100%; loadWorkflowConfig 93,8%; composeWorkflowCLI 85,1%; Reduce 86,5%
PASS

gofmt -l .
PASS (sin archivos)

go vet ./...
PASS

go test -run '^$' ./...
PASS

git diff --check
PASS (solo avisos CRLF del checkout Windows)

go test ./... -count=1 -timeout=12m
Único fallo: internal/localvalidation/TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink,
porque Windows devuelve `A required privilege is not held by the client`.

PATH=C:\msys64\ucrt64\bin;%PATH% CGO_ENABLED=1 go test -race ./internal/localvalidation -count=1
Mismo único fallo ambiental de symlink.

go test ./internal/localvalidation -run '^TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/(undeclared_evidence|missing_evidence_file|duplicate_package_key)$' -count=1
PASS
```

## Seguridad y límites

- El core neutral y el adapter OpenAPI no importan ni poseen red, credential stores, procesos o tool registries; el test AST falla ante esos imports.
- El adapter adversario invoca las cuatro capacidades si se le entregan, demostrando que el canario es efectivo; con la entrada productiva pasiva no encuentra ninguna.
- La policy productiva rechaza configuración ausente o parcial y se congela antes del adapter. No hay detector hard-coded de canarios.
- Defaults/examples permanecen privados hasta la validación y luego se omiten; no alcanzan catálogo, contexto, errores ni persistencia.
- Bytes, operaciones, claims, profundidad y diagnostics tienen límites per-source, agregados y de reducción.
- La cancelación se propaga por carga, merge, reducción y compatibilidad OpenAPI sin publicar estado parcial.

## Limitación ambiental

La única incidencia global reproducida es el test preexistente de symlink de `internal/localvalidation`, que requiere privilegio de creación de enlaces simbólicos en Windows. No se omitió ni debilitó; los demás subtests del parent pasan explícitamente.

## Estado solicitado

T1–T12 están completas con evidencia. Se solicita cuarta review independiente. El implementer no cambia `feature_list.json` ni marca la feature `done`.
