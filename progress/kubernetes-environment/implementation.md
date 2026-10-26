# Implementación de kubernetes-environment

## Resultado

Feature 32 implementa `KubernetesProvider` como adapter del contrato neutral de
feature 31. El provider resuelve un catálogo privado sellado, crea un namespace
por adquisición, confirma controles de seguridad antes del workload, observa
readiness, atesta la revisión efectiva y libera con precondición de UID. El
cliente productivo usa exclusivamente operaciones tipadas de `client-go`; el
core no recibe SDK, manifests arbitrarios ni credenciales.

La composición productiva exige `rest.Config`, cluster, catálogo y bundles
explícitos. No consulta kubeconfig, variables de entorno ni un cluster durante
`go test`. Los tests usan un `ClusterClient` hermético, concurrent-safe y capaz
de simular errores tipados y pérdida de respuesta.

Las tres remediaciones de review mantienen el vocabulario cerrado: el constructor
usado por `main` resuelve toda la autoridad, planning, runner y functional-flow
se reconstruyen desde el target adquirido, los readbacks oficiales y fake son
fail-closed, y lifecycle/secret/OCI quedan autenticados antes de I/O.

## Contratos externos contrastados

- Namespaces y límites de aislamiento: <https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/>
- ResourceQuota: <https://kubernetes.io/docs/concepts/policy/resource-quotas/>
- Requests y limits de contenedores: <https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/>
- NetworkPolicy deny-by-default y allowlists: <https://kubernetes.io/docs/concepts/services-networking/network-policies/>
- Readiness mediante EndpointSlices: <https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/>
- Matriz de compatibilidad del cliente oficial: <https://github.com/kubernetes/client-go/blob/v0.35.9/INSTALL.md>

Se fijaron `k8s.io/api`, `k8s.io/apimachinery` y `k8s.io/client-go` en
`v0.35.9`. No se copió código externo.

## Requirements y tests

| Requirement | Test concreto | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestKubernetesCatalogRequiresExactTrustedImmutableSelection`, `TestComposeWorkflowCLIResolvesCompleteKubernetesAuthorityThroughMainConstructor` | `internal/environment/kubernetes/provider_test.go`, `cmd/featureproof/main_test.go` | pass |
| R2 | `TestKubernetesProviderIsolatesAndAuthenticatesNamespacePerRun` | `internal/environment/kubernetes/provider_test.go` | pass |
| R3 | `TestKubernetesProviderHasNoArbitraryManifestOrCommandPath`, `TestAPIClientRejectsWidenedAuthoritativeReadbacks`, `TestKubernetesClosedShapeReadbackFailsDuringAdoptionAndObserve` | `internal/environment/kubernetes/provider_test.go`, `internal/environment/kubernetes/client_test.go` | pass |
| R4 | `TestKubernetesBootstrapPrecedesEveryWorkloadAndFailsClosed` | `internal/environment/kubernetes/provider_test.go` | pass |
| R5 | `TestKubernetesResourcesCannotExceedProjectPolicy`, `TestAPIClientRejectsWidenedAuthoritativeReadbacks` | `internal/environment/kubernetes/provider_test.go`, `internal/environment/kubernetes/client_test.go` | pass |
| R6 | `TestKubernetesEnvironmentIsDenyByDefaultBeforeWorkload`, `TestAPIClientRejectsWidenedAuthoritativeReadbacks` | `internal/environment/kubernetes/provider_test.go`, `internal/environment/kubernetes/client_test.go` | pass |
| R7 | `TestKubernetesAcquireAndReconcileConvergeAcrossEveryCreateBoundary`, `TestKubernetesReconcileRecoversEveryLostCreateResponse`, `TestKubernetesClosedShapeReadbackFailsDuringAdoptionAndObserve` | `internal/environment/kubernetes/provider_test.go` | pass |
| R8 | `TestKubernetesReadinessRequiresRolloutPodsServiceAndEndpoints`, `TestKubernetesReadinessRejectsNonExactPodAndEndpointSets` | `internal/environment/kubernetes/provider_test.go` | pass |
| R9 | `TestKubernetesAttestationBindsRunningImageRepositoryCommitAndEndpoint`, `TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage`, `TestWorkflowFunctionalHTTPUsesOnlyAcquiredTarget` | `internal/environment/kubernetes/provider_test.go`, `internal/validationworkflow/workflow_test.go`, `cmd/featureproof/main_test.go` | pass |
| R10 | `TestKubernetesSecretsNeverCrossProviderBoundary`, `TestKubernetesSelectionSecretAuthorityMustMatchCatalogBeforeIO` | `internal/environment/kubernetes/provider_test.go` | pass |
| R11 | `TestKubernetesEnvironmentCarriesVerifiableOwnerAndExpiry` | `internal/environment/kubernetes/provider_test.go` | pass |
| R12 | `TestKubernetesReleaseIsDurableIdempotentAndUIDSafe`, `TestKubernetesLifecycleDigestTamperingFailsBeforeObserveOrReleaseIO` | `internal/environment/kubernetes/provider_test.go` | pass |
| R13 | `TestKubernetesFailuresRemainOperational`, `TestKubernetesProviderSatisfiesNeutralLifecycleContract` | `internal/environment/kubernetes/provider_test.go` | pass |
| R14 | `TestKubernetesOperationsAreBoundedCancelableAndRaceSafe`, `TestFlowAuthorityRetainedAcrossRetryableTerminalFailures`, `TestFlowAuthorityRollsBackUnpersistedAdmission`, `TestFlowAuthorityIntegrityTerminalSkipsFinalizeButReleases`, `TestFlowAuthorityTerminalRedeliveryIsIdempotent`, `TestFlowAuthorityManyRunsRemainBounded`, `TestWorkflowFunctionalTargetToolsReleaseOnlyAfterServiceConfirmation` | `internal/environment/kubernetes/provider_test.go`, `internal/validationworkflow/authority_lifecycle_test.go`, `cmd/featureproof/main_test.go` | pass, incluido race x20 |
| R15 | `TestAPIClientTranslatesClosedTypedResources`, `TestAPIClientRejectsWidenedAuthoritativeReadbacks`, `TestAPIClientPreservesAndRejectsEndpointSliceAuthorityDrift`, `TestAPIClientClassifiesOfficialErrors`, `TestWorkflowKubernetesCompositionBuildsRealClientFromExplicitAuthority`, `TestWorkflowKubernetesRuntimeRequiresEveryExplicitDependency` | `internal/environment/kubernetes/client_test.go`, `cmd/featureproof/main_test.go` | pass |

## Remediación de review 1

| Hallazgo | Cierre verificable |
|---|---|
| REV-1 | `composeWorkflowCLI` resuelve config, CA, token, catálogo y bundles explícitos; el test vertical atraviesa el constructor exacto de `main` y la matriz de dependencias falla temprano. |
| REV-2 | `TargetPlanningBinder`, `LocalExecutionPort` y el proxy functional-flow derivan URL/environment únicamente del snapshot adquirido/revalidado; dos servidores centinela prueban cero tráfico al configurado. |
| REV-3 | Deployment y Pod se comparan contra su forma API completa, incluidos template metadata, containers, volumes, host flags y security contexts; cliente oficial y fake rechazan widening. |
| REV-4 | Quota, LimitRange, NetworkPolicy y Service validan la forma completa permitida; los readbacks hostiles fallan en adopción y Observe. |
| REV-5 | Pods y EndpointSlices forman conjuntos exactos y únicos; UID, metadata, targetRef, dirección, owner, protocolo y cardinalidad ajenos se rechazan. |
| REV-6 | `Secretless`/`CredentialReference` se sellan contra entry y bundle exacto antes de la primera operación del cliente. |
| REV-7 | Observe/Release recomputan exactamente el lifecycle digest neutral antes de GET/delete, incluido recovery. |
| REV-8 | `distribution/reference` + `opencontainers/go-digest` exigen round-trip canónico SHA-256 completo y rechazan traversal, nombre normalizable, algoritmo/longitud incorrectos. |

## Remediación de review 2

| Hallazgo | Cierre verificable |
|---|---|
| REV-4 | `ServiceSpec` se compara completo tras retirar únicamente ClusterIP/ClusterIPs, familia/policy e `InternalTrafficPolicy=Cluster` asignados por API; Local, session config, load-balancer class y cualquier campo adicional quedan rechazados en cliente oficial y adopción/Observe fake. |
| REV-5 | El DTO conserva `PortName` y todos los campos relevantes de `TargetRef`; provider exige nombre igual al puerto Service, Kind Pod y APIVersion/ResourceVersion/FieldPath vacíos. Cliente oficial y Observe fake prueban cada mutación. |
| REV-8 | `validImmutableImage` rechaza explícitamente `NamedTagged`; la tabla incluye `name:tag@sha256:digest`. |
| REV-9 | La matriz oficial cubre Deployment, Pod, NetworkPolicy, Service, ResourceQuota y LimitRange; esta última prueba type, listas, resource keys y ratios, y la matriz fake cubre adopción+Observe más Pod `ClosedShape=false`. |
| REV-10 | `Finalize` conserva tools/digests cuando falla y ejecuta `Unbind` idempotente solo tras terminal durable; 128 runs prueban retención reintentable y cardinalidad final cero. |

## Remediación de review 3

| Hallazgo | Cierre verificable |
|---|---|
| REV-10 | `validationworkflow.Service` posee ahora la frontera de liberación mediante `FunctionalFlowAuthorityLifecyclePort`. Un `Admit` exitoso cuya transición `PhasePlanReady` falla ejecuta rollback; `Finalize` nunca libera; solo un `SaveOwned` terminal exitoso confirma la liberación. Los terminales integrity, que omiten packaging, usan la misma confirmación. Finalize fallido, persistencia de paquete fallida, Save terminal fallido y cleanup fallido conservan el binding para recovery. Redelivery vuelve a notificar sin una segunda liberación y 128 runs dejan cardinalidad cero. |

## Tasks

| Task | Estado | Evidencia principal |
|---|---|---|
| T1 | done | `model.go`: strict decode, modelo canónico y digests sin secretos. |
| T2 | done | `resources.go` y fake hermético en `test_support_test.go`. |
| T3 | done | Desired-state cerrado en `provider.go`; snapshots tipados en tests. |
| T4 | done | Namespace DNS-safe, UID y handle autenticado contra toda la adquisición. |
| T5 | done | Orden namespace → quota/limits/SA/network → secrets/workload/service. |
| T6 | done | `PrivateSecretBundle`, verificación privada de bytes y canarios. |
| T7 | done | Deployment/Service inmutables y create-or-get sin update/patch. |
| T8 | done | Acquire idempotente ante redelivery, parcialidad y concurrencia. |
| T9 | done | Lecturas finitas de Deployment, Pods, Service y EndpointSlices. |
| T10 | done | Atestación de UID, repo, commit, imagen/imageID y endpoint. |
| T11 | done | Delete namespace con UID y confirmación posterior a respuesta incierta. |
| T12 | done | Cliente oficial tipado y composición CLI explícita `kubernetes@v1`. |
| T13 | done | Suite vertical contra `environment.LifecycleService`. |
| T14 | done | Matrices de boundary, response loss, drift, cancelación y race. |
| T15 | done | Gates, cobertura exacta, Linux, cross-build y auditorías documentados. |

T1–T15 se marcaron `[x]` únicamente después de completar los gates de este
informe.

## Comandos ejecutados

Todos los comandos Go finales usaron `GOTOOLCHAIN=go1.25.1`.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía |
| `git diff --check a7b4a98` | pass; solo avisos informativos LF/CRLF del checkout |
| `go mod verify` | pass, todos los módulos verificados |
| `go list -m k8s.io/api k8s.io/apimachinery k8s.io/client-go` | pass, los tres en `v0.35.9` |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=10 ./internal/environment/kubernetes ./internal/validationworkflow ./cmd/featureproof` | pass: Kubernetes `1.524s`, workflow `314.752s`, CLI `220.176s` |
| `go test -count=1 -p=1 ./...` | pass |
| `go test -race -count=20 ./internal/environment/kubernetes` con CGO/UCRT64 | pass |
| `go test -race -count=1 -p=1 ./...` con CGO/UCRT64 | pass completo |
| `py -3 scripts/feature32_coverage.py` | pass: `1005/1155 = 87,012987 %` |
| `go test -c`, 3 paquetes x Windows/Linux/Darwin amd64 | pass 9/9 |
| Binario Linux fresco `internal/environment/kubernetes`, `-test.count 3` | pass en Alpine 3.22.2 WSL1 |
| Binarios Linux frescos `internal/validationworkflow` y `cmd/featureproof`, `-test.count 1` | pass en Alpine 3.22.2 WSL1 desde el directorio de cada paquete |
| Auditoría de comentarios top-level, canarios, secretos, operaciones prohibidas y scope | pass |

El minirootfs oficial Alpine 3.22.2 se autenticó con SHA-256
`412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be`.
Los binarios Linux frescos ejecutados en la remediación 3 tuvieron SHA-256
`3aa8ecc9b3a76fbafe6269a27fbf638dd291e4de88eea901a7f1e2b4bb5352ce`
(`kubernetes.test`),
`0f81f33618c11999060cb3221e4837190b54fc03b65d4b7292943e961467db14`
(`validationworkflow.test`) y
`3ecf2743a3178564d08cb3a04ac720ff7a86bee5c8c4a5412af796df10589afb`
(`featureproof.test`). El runner se desregistró y sus archivos se eliminaron.

## Coverage exacta del diff productivo

`scripts/feature32_coverage.py` fija la base aprobada `a7b4a98`, lee la versión
exacta de `go.mod`, exige identidad `go1.25.1 windows/amd64`, incluye fuentes
untracked productivas, conserva columnas de cada bloque y combina profiles por
su contador máximo. Usa un directorio temporal gestionado y falla antes de
redondear si el total es menor de 85 %.

| Archivo | Cubiertos/total |
|---|---:|
| `cmd/featureproof/workflow.go` | 134/165 (81,2 %) |
| `internal/environment/kubernetes/client.go` | 304/341 (89,1 %) |
| `internal/environment/kubernetes/model.go` | 137/160 (85,6 %) |
| `internal/environment/kubernetes/provider.go` | 377/428 (88,1 %) |
| `internal/environment/kubernetes/resources.go` | 4/4 (100,0 %) |
| `internal/validationworkflow/local_runner.go` | 20/23 (87,0 %) |
| `internal/validationworkflow/service.go` | 29/34 (85,3 %) |
| **Total** | **1005/1155 = 87,012987 %** |

La cobertura es fresca, toolchain-pinned, statement-weighted y corresponde al
diff productivo completo de la feature; no se autenticó mediante un porcentaje
de paquete aproximado.

## Auditoría de seguridad, scope y limpieza

- `ClusterClient` no ofrece apply/patch/raw REST/exec/attach/logs/proxy ni list
  global; los listados se limitan por namespace, selector y cardinalidad.
- No hay `time.Sleep`, waits reales ni goroutines en los tests nuevos.
- Planning, runner normal y functional-flow no conservan una URL ejecutable de
  configuración: cada capability/tool se reconstruye desde el target adquirido
  y el proxy rechaza runs no ligados; tools/digests se conservan durante todo
  fallo reintentable y se liberan idempotentemente solo tras el terminal
  durable confirmado por `validationworkflow.Service`.
- Los valores Secret solo existen en `PrivateSecretBundle`, el create tipado y
  la comparación privada del transporte; los DTOs devueltos descartan bytes.
- Los canarios de auth, image-pull y application secret solo aparecen en tests
  y ninguna superficie serializada los contiene.
- `feature_list.json` permanece intacto y no hay cambios de features 33–36.
- No quedan perfiles, test binaries, cross-builds, rootfs, distro WSL ni otros
  temporales creados por esta implementación.

## Riesgos y limitaciones

- El vocabulario inicial crea un Deployment y un Service interno; no admite
  Jobs, Ingress, volúmenes, commands ni manifests libres por diseño.
- La suite valida el adapter oficial contra el fake oficial de `client-go`, no
  contra un cluster real. Un smoke contra infraestructura real debe ser opt-in
  y permanecer fuera de `go test` para no introducir credenciales ni red.
- La tool de inspección, el cleanup controller periódico y el control plane
  distribuido corresponden a features 33, 34 y 35 y no se implementaron aquí.
