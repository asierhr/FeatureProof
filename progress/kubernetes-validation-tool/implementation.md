# Implementación de kubernetes-validation-tool

## Resultado

`kubernetes.observe` queda implementada como `harness.Tool` read-only,
idempotente y ligada a un único target Kubernetes. El plan solo puede elegir un
`check_kind` y un alias sellado. Namespace, UIDs, owners, selectors, puertos y
relaciones proceden de composición y nunca del modelo.

Las remediaciones de reviews 1–3 cierran REV-1–REV-13 sin modificar la spec aprobada.
La autoridad de namespace es literalmente `ValidationTarget.EnvironmentID`;
`EnvironmentRef` conserva su función de referencia de template.

## Cambios de remediación

| Hallazgo | Corrección | Tests de regresión |
|---|---|---|
| REV-1 | Pods propios vacíos producen `resource_absent`; identidad/owner incompletos producen `insufficient_evidence`; owner ajeno se excluye; contradicción real de autoridad conserva `integrity_error`; Deployment owner se lee antes y después. | `TestPodEmptyInvalidIdentityAndOwnerAnchorRemainDistinct`, `TestAuthorityReplacementExpiryAndRunMismatchFailBeforeEvidence/pod_owner_*` |
| REV-2 | Pods, EndpointSlices y Events fijan la `resourceVersion` opaca en páginas posteriores, registran la versión de lista y rechazan cambio de versión o página vacía con continuation. | `TestPageVersionsArePinnedAndEmptyContinuationFailsClosed` |
| REV-3 | Cero endpoints es insuficiente; policy requerida ausente o distinta es integridad; drift conserva categoría insuficiente. | `TestConnectivityMissingEndpointsPolicyAndDigestUseClosedCategories`, `TestConnectivityPolicyDriftRemainsInsufficient` |
| REV-4 | Éxito, ausencia y todos los errores operativos comparten un encoder/bounder final; el fallback elimina observación/versiones y conserva categoría segura. | `TestCollectionLimitsFailClosed/bytes`, `TestMaxOutputBytesBoundsEveryOperationalOutcome` |
| REV-5 | Los puertos se comparan contra el catálogo y se proyectan desde él; conditions, Events y versions usan orden total. | `TestPortNamesAreCatalogBoundAndTiedCollectionsAreCanonical`, `TestServicePortAuthorityIsExactAndCanonical` |
| REV-6 | `New` exige `IdentityBinding` sellado con ServiceAccount, namespace, digest y reglas efectivas exactamente iguales al RBAC mínimo; reglas ampliadas impiden registro. | `TestNewDetachesTargetSlicesAndRejectsDuplicateReasons`, `TestRequiredRBACRulesAreMinimalDetachedAndSecretFree` |
| REV-7 | Se comprueba cancelación antes de cada página, I/O posterior, reread y normalización final. | `TestCancellationPreventsEveryNextPageAndAnchorRead` |
| REV-8 | Catálogo, identidad y APIReader usan `Target.EnvironmentID`, incluso cuando difiere de `EnvironmentRef`. | `TestNewBindsVerifiedKubernetesTargetAndNamespaceUID`, `TestAuthorityMismatchFailsBeforeAPI` |
| REV-9 | La integración ejecuta planes validados, evidence store y `internal/assertion.Engine` real; no existe evaluator ad hoc. | `TestKubernetesObservedConditionUsesRealAssertionEngine`, `TestKubernetesAbsenceFailsRequiredExistenceWithRealEngine`, `TestKubernetesOperationalOutcomesBypassRealAssertions` |
| REV-10 | Cada evaluator valida contadores e invariantes antes de proyectar; negativos, relaciones imposibles y overflow de agregados son `integrity_error` content-free. | `TestInvalidCountersAndImpossibleRelationsFailContentFree` |
| REV-11 | `New` exige que el target sellado incluya explícitamente `kubernetes.observe`; un target válido con otra capability no registra la tool ni alcanza I/O. | `TestNewRejectsTargetWithoutKubernetesCapability` |
| REV-12 | Connectivity registra cada Service, lista, slice y policy segura antes de clasificar truncación, cero endpoints o contradicción. | `TestConnectivityIncompleteOutcomesRetainObservedVersions` |
| REV-13 | Pods, Events y anchors acumulan versiones seguras en el momento de lectura; `finalize` centraliza orden total, deduplicación, digest y byte bound para toda categoría. | `TestOperationalVersionFinalizationIsCanonical` |

## Trazabilidad R1–R15

| Requirement | Evidencia concreta | Resultado |
|---|---|---|
| R1 | `TestNewBindsVerifiedKubernetesTargetAndNamespaceUID`; `TestNewRejectsTargetWithoutKubernetesCapability`; `TestAuthorityReplacementExpiryAndRunMismatchFailBeforeEvidence` | pass |
| R2 | `TestValidateAcceptsOnlyAuthorizedCheckAlias`; `TestInputTypeAndRelationshipValidationRejectBeforeIO` | pass |
| R3 | `TestDeploymentObservationNormalizesAvailabilityAndVersion`; `TestConcurrentResourceChangeProducesDrift`; `TestInvalidCountersAndImpossibleRelationsFailContentFree/deployment_*` | pass |
| R4 | `TestPodObservationFiltersOwnerAndOrdersRestarts`; `TestPodEmptyInvalidIdentityAndOwnerAnchorRemainDistinct`; `TestInvalidCountersAndImpossibleRelationsFailContentFree/pod_restart` | pass |
| R5 | `TestJobObservationNormalizesActiveCompleteAndFailed`; `TestInvalidCountersAndImpossibleRelationsFailContentFree/job_*` | pass |
| R6 | `TestServiceEndpointObservationAggregatesOnlyOwnedSlices`; `TestInvalidCountersAndImpossibleRelationsFailContentFree/endpoint_*`; `TestServicePortAuthorityIsExactAndCanonical` | pass |
| R7 | `TestEventsAreScopedAllowlistedOrderedAndBounded`; `TestInvalidCountersAndImpossibleRelationsFailContentFree/event_negative`; `TestPortNamesAreCatalogBoundAndTiedCollectionsAreCanonical` | pass |
| R8 | `TestDeclaredConnectivityUsesOnlyCatalogRelationPolicyAndEndpoints`; `TestConnectivityIncompleteOutcomesRetainObservedVersions`; `TestConnectivityPolicyDriftRemainsInsufficient` | pass |
| R9 | `TestRequiredRBACRulesAreMinimalDetachedAndSecretFree`; `TestNewDetachesTargetSlicesAndRejectsDuplicateReasons`; `TestAdapterUsesOnlyGetAndListOnAllowlistedResources` | pass |
| R10 | `TestCollectionLimitsFailClosed`; `TestPageVersionsArePinnedAndEmptyContinuationFailsClosed`; `TestMaxOutputBytesBoundsEveryOperationalOutcome` | pass |
| R11 | `TestSnapshotRecordsOpaqueVersionsAndCanonicalDigest`; `TestConnectivityIncompleteOutcomesRetainObservedVersions`; `TestOperationalVersionFinalizationIsCanonical` | pass |
| R12 | `TestCancellationPreventsEveryNextPageAndAnchorRead`; `TestCallerCancellationAndToolTimeoutStopFurtherReads`; `TestErrorTaxonomyMapsOfficialKubernetesFailures` | pass |
| R13 | `TestOperationalVersionFinalizationIsCanonical`; `TestPortNamesAreCatalogBoundAndTiedCollectionsAreCanonical`; `TestMaxOutputBytesBoundsEveryOperationalOutcome` | pass |
| R14 | Los tres tests `TestKubernetes*` de `internal/assertion/kubernetes_tool_integration_test.go` | pass |
| R15 | `TestNewRejectsTargetWithoutKubernetesCapability`; `TestAdapterUsesOnlyGetAndListOnAllowlistedResources`; canarios y gates globales | pass |

## Tareas

T1–T11 están cerradas en `specs/kubernetes-validation-tool/tasks.md`. Durante
review 2 se reabrieron T1–T9 y T11; T10 permaneció cerrada porque sus fronteras
de adapter e integración vertical no estaban afectadas. Durante review 3 se
reabrieron y cerraron T3, T5, T8 y T11 con evidencia específica de REV-13.

## Contratos relevantes

- `Config.Identity` representa la identidad efectiva que composición va a
  registrar. `SealIdentityBinding` canoniza y exige igualdad exacta con
  `RequiredRBACRules(EnvironmentID)`; no acepta wildcards, Secrets, logs,
  subrecursos, watch ni mutaciones.
- El permiso de Namespace es cluster-scoped `get` limitado por
  `resourceNames` al `EnvironmentID`; los demás permisos son namespaced y solo
  contienen el `get` o `list` necesario.
- `APIReader` recibe un `kubernetes.Interface` ya autenticado. Credenciales y
  kubeconfig no cruzan al harness ni se descubren dentro de la tool.
- Las páginas usan tokens y resourceVersions solo en memoria. Los tokens nunca
  aparecen en output; cada lista aporta una versión opaca al snapshot.
- Cada versión con identidad segura se añade al envelope en la lectura. La
  finalización común la ordena y deduplica antes de calcular el digest y aplicar
  el budget, también cuando el evaluator termina en insufficient o integrity.
- Los outcomes operativos no llegan al engine de assertions. `resource_absent`
  sí es dato explícito y solo falla cuando una assertion real lo exige.

## Verificación final

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; sin archivos |
| `git diff --check` | pass; solo avisos LF/CRLF del checkout |
| `go vet ./...` | pass |
| `go test ./...` | pass |
| `CGO_ENABLED=1 go test -race ./...` con `C:\msys64\ucrt64\bin` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/tools/kubernetes` | pass, 20/20 |
| `go test -count=1 -coverprofile=... ./internal/tools/kubernetes` | pass |
| integración focal `go test ./internal/assertion` | pass dentro de suite global y focal |

## Coverage exacta

El profile fresco del paquete nuevo/modificado `internal/tools/kubernetes`
contiene `734/854` statements cubiertos:

`734 / 854 × 100 = 85,948477751756 %`

La cifra supera el gate del 85 % sin redondear.

## Riesgos residuales

- Kubernetes no ofrece snapshot transaccional entre kinds. La tool fija cada
  lista por resourceVersion y hace double-read de Namespace y anchors; cualquier
  drift observable queda inconclusive, pero no se promete serialización global.
- El binding RBAC verifica el contrato efectivo declarado por composición sin
  hacer `SelfSubjectAccessReview`, porque leer autorización ampliaría el puerto
  y los permisos de esta feature. La composición sigue siendo responsable de
  materializar exactamente ese Role/RoleBinding.
- Una lista completa sin resourceVersion y sin continuation se conserva como
  versión no disponible, conforme R11; una ausencia con continuation o un cambio
  entre páginas falla cerrado.

No se modificó `feature_list.json` y no se hizo commit ni push.
