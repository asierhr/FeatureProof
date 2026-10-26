# Entornos efímeros sobre Kubernetes — Design

## Resumen de la solución

La feature añade `internal/environment/kubernetes`, un adapter gestionado que
implementa el `environment.EnvironmentProvider` aprobado en la feature 31. El
adapter recibe una selección sellada y la resuelve contra un catálogo privado
de plantillas y policies. Renderiza un conjunto cerrado de objetos tipados,
provisiona un namespace por run en orden seguro, observa readiness y traduce el
estado efectivo a `environment.Observation`.

`environment.LifecycleService` continúa siendo dueño de acquisition keys,
budgets, reintentos, leases, binding, release intent y resultados operativos.
El adapter Kubernetes no muta el run ni produce veredictos. Su lifecycle handle
codifica de forma opaca el nombre y UID confirmados del namespace para que un
delete nunca alcance una recreación con el mismo nombre.

```text
trusted Selection + private Kubernetes catalog + secret-bound transport
                              │
                              ▼
                     KubernetesProvider
                              │
          ┌───────────────────┴───────────────────┐
          ▼                                       ▼
 namespace + security bootstrap          reconcile/readiness
          │                                       │
          ▼                                       ▼
 fixed workload + internal Service   deployment/pods/imageID/endpoints
          └───────────────────┬───────────────────┘
                              ▼
                  attested Observation ready
                              │
                              ▼
 LifecycleService → ValidationTarget → validationworkflow
                              │
                              ▼
 durable release intent → UID-preconditioned namespace delete
```

La entrega soporta un Deployment y un Service internos por plantilla. Esta
restricción mantiene pequeño el renderer y evita anticipar la herramienta de
Kubernetes de la feature 33. Nuevos kinds requieren ampliar explícitamente el
vocabulario y su spec; no se aceptan objetos libres.

## Fuentes de contexto consultadas

- `AGENTS.md`, `docs/specs.md`, `docs/architecture.md` y
  `docs/conventions.md`.
- Feature 32 de `feature_list.json` y fronteras expresas de las features 33–36.
- `specs/environment-provider/{requirements,design,tasks}.md` y la
  implementación de `internal/environment`: `Selection`, `EnvironmentProvider`,
  `LifecycleService`, `Observation`, `ValidationTarget`, store y release.
- Contratos públicos de Kubernetes relevantes para diseño: Namespaces y
  metadata/UID/resourceVersion; ResourceQuota; LimitRange; Pod Security
  Admission mediante labels; NetworkPolicy; Deployment status y
  `observedGeneration`; Pod conditions y `containerStatuses.imageID`; Service y
  EndpointSlice; Secret; delete preconditions y errores API tipados.

## Contratos externos observados

Los contratos Kubernetes se traducen a modelos propios; no forman parte del
dominio de FeatureProof:

- Un namespace separa nombres, pero por sí solo no garantiza red, permisos ni
  recursos. Por eso el bootstrap añade quota, límites, Pod Security y políticas
  default-deny antes del workload.
- `ResourceQuota` limita consumo agregado y `LimitRange` valida/aporta límites
  por objeto; los containers siguen declarando requests/limits explícitos.
- Las NetworkPolicies son aditivas y requieren soporte del dataplane. El perfil
  de cluster declara esa capacidad; sin ella se falla cerrado.
- `Deployment.status.observedGeneration` y sus replicas no bastan por sí solos:
  se combinan con Pods Ready, revisión exacta, image IDs y EndpointSlices.
- El `image` deseado puede conservar una referencia, mientras
  `containerStatuses.imageID` representa la imagen resuelta. Ambos deben
  contener el digest autorizado.
- `AlreadyExists` no acredita ownership y exige readback completo. `NotFound`
  solo confirma eliminación idempotente. Delete usa precondición UID para no
  borrar una recreación homónima.
- Los objetos Secret son sensibles aunque el API los represente en base64. Sus
  valores no cruzan DTOs internos seguros ni observabilidad.

El adapter productivo usa un cliente oficial compatible y fijado en `go.mod`,
encapsulado bajo un puerto consumidor. La configuración del cliente se crea en
`cmd/featureproof` con cluster y autenticación explícitos; no lee kubeconfig,
variables de entorno ni defaults dentro del provider.

## Áreas y paquetes afectados

| Área | Responsabilidad prevista |
|---|---|
| `internal/environment/kubernetes` | Catálogo privado, modelos Kubernetes propios, renderer cerrado, provider, atestación, error mapping y cliente concreto. |
| `internal/environment` | Conserva el contrato neutral; solo se amplía si una regla neutral imprescindible falta, sin importar tipos Kubernetes. |
| `cmd/featureproof` | Compone catálogo, cliente/transport autenticado y bundles privados; registra el provider de forma explícita. |
| `go.mod` / `go.sum` | Fijan la versión compatible del cliente Kubernetes y dependencias transitivas. |
| tests del adapter | Fake hermético, contract matrix neutral, fault injection, canarios, race y composición. |

No se añade código a `internal/tools/kubernetes`: ese espacio corresponde a la
feature 33. Tampoco se crea un cleanup worker de background.

## Componentes y responsabilidades

### `Catalog`

Catálogo inmutable privado del adapter. Cada entrada se identifica por la
tupla `template_reference + profile_id + network_policy_id` ya sellada en
`environment.Selection` e incluye:

- versión y digest del template;
- cluster profile y capacidades requeridas;
- imagen OCI completa por digest;
- replicas, container port, Service port, scheme y base path;
- probes tipadas y command/args fijos del template, si existen;
- CPU/memoria requests y limits;
- quota y LimitRange máximos;
- perfil Pod Security y security context restrictivo;
- ingress/egress allowlisted y soporte requerido de NetworkPolicy;
- TTL y TTL máximo;
- referencias opacas a bundles secretos y nombres lógicos permitidos.

El constructor canonicaliza y sella todas las entradas, rechaza duplicados,
campos desconocidos, relaciones inválidas y cualquier ampliación de policy. Las
colecciones se copian al entrar/salir. Los valores secretos no pertenecen al
catálogo ni a sus digests.

### `KubernetesProvider`

Implementa las cuatro operaciones neutrales:

```text
Acquire(ctx, AcquisitionRequest)     → Observation
Reconcile(ctx, AcquisitionRequest)   → Observation
Observe(ctx, ObservationRequest)     → Observation
Release(ctx, ReleaseRequest)         → error
```

Valida de nuevo selection, provider/version/kind y catálogo en cada boundary.
Acquire y Reconcile llaman al mismo reconciliador idempotente; la diferencia es
observacional para auditoría, no una segunda identidad. Observe es read-only y
no repara drift. Release solo puede eliminar el namespace exacto contenido en
el handle y verificado contra selection/request.

### `ClusterClient`

Puerto consumer-owned mínimo que expresa operaciones específicas, nunca
`Apply(any)`, raw REST ni patch libre:

```text
CreateNamespace / GetNamespace / DeleteNamespace(uid precondition)
CreateOrGetQuota / CreateOrGetLimitRange
CreateOrGetServiceAccount / CreateOrGetNetworkPolicy
CreateOrGetSecret / CreateOrGetDeployment / CreateOrGetService
GetDeployment / ListOwnedPods / GetService / ListOwnedEndpointSlices
```

Cada método recibe `context.Context`, namespace/identidad ya derivados y un DTO
privado tipado. El adapter productivo traduce a objetos del SDK y clasifica
`AlreadyExists`, `NotFound`, `Conflict`, `Forbidden`, `Invalid`, throttling,
timeout y fallos transitorios sin devolver bodies. El fake implementa el mismo
port en memoria y ofrece barreras deterministas para interleavings.

No se expone list global: todas las lecturas namespaced usan nombres exactos o
labels owner/acquisition selladas y un límite. Namespace get/delete es por
nombre derivado y UID esperado.

### `TemplateRenderer`

Produce DTOs internos de los únicos recursos soportados. No deserializa YAML o
JSON, no admite mapas arbitrarios ni copia campos desde request. Los nombres se
derivan de constantes de versión y acquisition digest.

Invariantes del Pod template:

- imagen `repository@sha256:<64 hex>` y pull policy compatible con digest;
- user no root, privilege escalation deshabilitada, root filesystem read-only,
  capabilities eliminadas y seccomp restrictivo;
- token de service account no automontado;
- requests/limits explícitos dentro de policy;
- sin host namespaces, hostPath, privileged, device, arbitrary volume, init o
  sidecar no definidos por el vocabulario;
- labels/annotations exactas de owner, run digest, project, revision, template,
  image y expiry.

### `BootstrapReconciler`

Converge recursos en este orden estricto:

1. crea o verifica namespace y captura UID;
2. verifica labels de Pod Security en namespace;
3. crea/verifica ResourceQuota y LimitRange;
4. crea/verifica service account sin token y NetworkPolicies default-deny +
   allowlists;
5. crea Secrets privados autorizados, si existen;
6. crea/verifica Deployment;
7. crea/verifica Service.

Cada `AlreadyExists` dispara GET y comparación canónica completa. Un mismatch
es integrity/conflict; el reconciliador no actualiza, parchea, adopta ni borra
el objeto. Así una respuesta perdida converge y un recurso hostil no se
sobrescribe. Un fallo previo al paso 6 prueba que ningún workload fue creado.

### `ReadinessObserver`

Realiza una observación finita por llamada a `Observe`; el loop, attempts,
waiter y elapsed budget siguen en `LifecycleService`. Una observación devuelve:

- `pending` si el rollout o los endpoints aún pueden converger;
- `ready` solo si todas las condiciones y atestación coinciden;
- `unhealthy` para condición terminal del rollout/Pod;
- error tipado para acceso, drift, exceso de cardinalidad o API failure.

No abre watches de duración indefinida. Las listas exigen selector exacto y
máximo de items; superar el máximo falla sin decidir sobre datos truncados.

### `DeploymentAttestor`

Compara recursos leídos contra el desired digest y la solicitud neutral:

- namespace name/UID y metadata de owner, run, proyecto, acquisition, expiry;
- Deployment UID/generation/revision, repository, full commit y template/image
  digests del Pod template;
- cantidad exacta de Pods propios, revisión común, Ready y container imageID;
- Service selector/ports y EndpointSlices con direcciones ready pertenecientes
  a los Pods esperados;
- policy/profile/network IDs y recursos efectivos.

Genera una referencia opaca/digest, `ValidUntil` no posterior a la expiración
del entorno y `ObservedVersion` derivada de resourceVersions/UIDs sin exponerlos
como autoridad al caller. La `Observation` usa endpoint interno canónico
`scheme://service.namespace.svc:port/basePath`; luego el `NetworkAuthorizer` de
feature 31 vuelve a validarlo.

### `PrivateSecretBundle`

La composición resuelve cluster auth, pull credentials y application secrets y
construye una instancia privada, no serializable, ligada al digest de template.
El provider entrega cluster auth solo al transporte y material de aplicación
solo a DTOs de Secret durante create. Los DTOs se zeroizan/liberan cuando el
lenguaje lo permite razonablemente, pero la garantía verificable principal es
no proyectar, persistir, comparar en errores ni observar valores.

Un perfil `secretless` exige bundle vacío. Las referencias de workload son
nombres provider-owned fijos; ni caller ni template externo eligen nombres
arbitrarios.

### Lifecycle handle y metadata

El handle privado contiene versión, namespace derivado y UID, serializados de
forma canónica y autenticados/digeridos conforme al límite `safeID` del core.
No contiene token, endpoint ni manifest. En Release se valida junto con run,
project, repository, commit, selection y lifecycle digest antes de I/O.

Labels de baja cardinalidad contienen valores DNS-safe o digests acotados;
annotations conservan los IDs seguros completos:

```text
managed-by=featureproof
owner=<provider owner>
run-digest=<sha256>
project=<safe project id>
acquisition-digest=<sha256>
template-digest=<sha256>
image-digest=<sha256>
expires-at=<RFC3339 UTC>
ttl-seconds=<bounded integer>
```

No se guarda repository URL, endpoint, secret reference sensible ni payload de
provider.

## Flujo principal

1. `LifecycleService` resuelve la selección y el provider registrados.
2. `KubernetesProvider` verifica kind `managed`, identidad/version y catálogo.
3. Deriva desired state, namespace, expiry y handle candidate desde la misma
   acquisition key.
4. Persiste acquisition intent el lifecycle neutral antes de llamar al provider.
5. Acquire crea/verifica namespace y bootstrap en orden; después crea/verifica
   Secret, Deployment y Service.
6. Devuelve `pending` con namespace/UID/handle confirmados.
7. El lifecycle invoca Observe dentro de su budget; cada lectura verifica de
   nuevo ownership, desired state y cardinalidad.
8. Cuando rollout, Pods, imageIDs, Service y endpoints coinciden, el attestor
   produce una observación `ready`.
9. Feature 31 valida authority, atestación y network policy y sella
   `ValidationTarget/v1` y binding antes de ejecutar.
10. Antes de ejecución/recovery/publicación, Observe vuelve a demostrar la misma
    autoridad; cualquier drift bloquea efectos nuevos.
11. Al finalizar, feature 31 persiste `release_intent` y llama Release; el
    provider GET-verifica name/UID/owner y solicita delete con UID precondition.

## Flujos de error y recovery

### Create incierto o parcial

Una respuesta perdida no provoca un nuevo nombre. Reconcile hace GET del mismo
objeto, compara su spec digest y continúa desde el primer recurso ausente. Si el
namespace no puede atribuirse inequívocamente, no se toca y se devuelve
integrity/conflict. Como el intent neutral ya existe, cleanup permanece
recuperable.

### Readiness no alcanzada

Pending consume los attempts/waits del lifecycle. Un rollout terminal pasa a
unhealthy. Timeout/cancel conservan cause y desencadenan release con el contexto
independiente finito ya definido por feature 31. Ningún caso crea una assertion
funcional.

### Drift

Observe es estricto y no repara: UID, metadata, resource spec, imageID,
revision, endpoint o policy distintos producen integrity. No se ejecutan nuevas
tools. Cleanup solo elimina el namespace si el UID y ownership originales aún
coinciden.

### Delete incierto

Release valida primero el namespace. Delete usa UID precondition. `NotFound` es
already released; timeout o respuesta perdida permanecen cleanup pending y se
reconcilian en una reentrega. Un namespace `Terminating` no se fuerza y puede
quedar pending para el controlador futuro.

## Estados y transiciones

El provider no añade una segunda máquina durable. Proyecta subfases seguras
dentro de los estados de feature 31:

```text
acquiring
  namespace_confirmed
    bootstrap_confirmed
      workload_confirmed
        service_confirmed
          waiting ──observe──► ready
             │                  │
             └─unhealthy/drift──┴──► release_intent
                                         │
                                  delete_requested
                                    │          │
                              release_pending released
```

Las subfases se reconstruyen por readback y desired digests; no necesitan un
store paralelo que compita con `EnvironmentStore`. El estado autoritativo del
run y la obligación de cleanup siguen en feature 31.

## Propiedad del estado y concurrencia

`LifecycleService` posee la lease del record. El provider no lanza goroutines ni
mantiene locks durante I/O. Su catálogo y secret bundle son inmutables después
de composición; el cliente es seguro para concurrencia o documenta
serialización interna sin compartir estado de run.

Kubernetes arbitra create por nombre; FeatureProof además compara UID y desired
digest. Contenders del mismo run convergen sobre el mismo recurso, mientras runs
distintos usan namespaces distintos. Tests coordinan carreras con canales y
reactores del fake, nunca con `time.Sleep`.

## Cancelación, retries e idempotencia

- Cada operación recibe el child context de `LifecycleService`.
- El provider no crea deadlines mayores ni reemplaza el contexto.
- Las listas llevan limit y no siguen páginas más allá del budget configurado.
- El mapping marca throttling/5xx/transport como transitorio solo cuando repetir
  create-or-get con la misma key es seguro.
- Forbidden, Invalid, quota/admission, integrity y policy son permanentes.
- No se hace update/patch para “arreglar” mismatch.
- Desired resource names y digests dependen de template version + acquisition
  key, por lo que redelivery produce los mismos objetos.
- Release repite delete con la misma name/UID y nunca elimina por selector.

## Observabilidad

El lifecycle neutral ya publica eventos content-free. El provider puede añadir
observaciones estructuradas allowlisted con:

- run/acquisition/template/image/environment digests;
- fase `namespace|bootstrap|workload|service|readiness|attestation|delete`;
- operation, attempt, duración y categoría;
- counts acotados, nunca nombres arbitrarios ni payload remoto.

Se excluyen manifests completos, specs de Secret, values, kubeconfig, tokens,
API server URL, endpoint completo, annotations libres, event messages del
cluster y bodies de error. Logs no coordinan recovery.

## Estrategia de pruebas

### Catálogo y renderer

- canonicalización, deep copy, unknown/duplicate fields y relation tampering;
- image digest obligatorio; tags/algoritmos/IDs inválidos;
- CPU/memoria/replicas/quota/TTL y policy widening;
- snapshots tipados exactos de todos los objetos permitidos;
- payloads adversarios no tienen camino al renderer.

### Fake Kubernetes hermético

Un fake del `ClusterClient`, no un cluster real, conserva objects + UID +
resourceVersion en memoria y expone reactors/barreras para:

- AlreadyExists/NotFound/Conflict/Forbidden/Invalid/rate-limit/5xx;
- response loss después de commit;
- mutación, reemplazo UID y eliminación concurrente;
- rollout, Pod readiness, imageID y EndpointSlice;
- cancelación antes/durante cada método y límites de listas.

Si el cliente oficial aporta fake, se usa detrás del mismo port solo donde
reproduzca fielmente el contrato; las reglas de admission/policy se comprueban
con el fake propio y asserts sobre objetos, no se presumen.

### Orden, aislamiento y recovery

- dos runs producen namespaces diferentes;
- mismo run/redelivery/concurrency converge;
- cada fallo de bootstrap demuestra cero workloads;
- crash/response loss después de cada create converge por GET;
- objeto hostil nunca se adopta, actualiza ni borra;
- release usa UID y preserva namespace recreado.

### Readiness y atestación

- matriz de observedGeneration/replicas/Pod conditions/Service/EndpointSlice;
- imageID correcto, ausente, tag-only, distinto y Pods mezclados;
- repo/commit/template/profile/network/expiry/UID drift;
- revalidación antes de effects/publication;
- timeout/cancel sin watches, timers o goroutines filtrados.

### Secretos y errores

- canarios en cluster auth, pull y application secret;
- scans byte-for-byte de selección, requests, handles, observation, target,
  binding, store, events, logs, errors, model, evidence, package y report;
- errores remotos con canario se convierten a categorías cerradas;
- secretless no resuelve ni crea Secret.

### Contrato neutral y vertical

- ejecutar la suite `EnvironmentProvider` de feature 31 contra
  `KubernetesProvider` además de sus casos específicos;
- composición real exige provider/client/catalog explícitos y nunca cae al fake;
- validationworkflow conserva outcomes operacionales y un único EnvironmentPort;
- `go test` no usa red; smoke real, si se añade, es opt-in y externo a la suite.

### Gates

- `gofmt -l`, auditoría de comentarios sistémicos y `git diff --check`;
- `go vet ./...`, compile-only, focales repetidos y global normal;
- suites focal/global `-race` y matrices concurrentes repetidas;
- runtime Linux y cross-build Windows/Linux/Darwin aplicables;
- coverage exacta del diff productivo ≥85% sin redondear;
- auditoría de dependencias, temporales, secretos y scope.

## Trazabilidad inicial

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | catálogo, composición, image parser | `TestKubernetesCatalogRequiresExactTrustedImmutableSelection` |
| R2 | namespace identity, handle | `TestKubernetesProviderIsolatesAndAuthenticatesNamespacePerRun` |
| R3 | renderer, client port, surface boundary | `TestKubernetesProviderHasNoArbitraryManifestOrCommandPath` |
| R4 | bootstrap reconciler | `TestKubernetesBootstrapPrecedesEveryWorkloadAndFailsClosed` |
| R5 | policy validator, quota/limits renderer | `TestKubernetesResourcesCannotExceedProjectPolicy` |
| R6 | network policy renderer, cluster profile | `TestKubernetesEnvironmentIsDenyByDefaultBeforeWorkload` |
| R7 | reconciler, canonical comparison | `TestKubernetesAcquireAndReconcileConvergeAcrossEveryCreateBoundary` |
| R8 | readiness observer | `TestKubernetesReadinessRequiresRolloutPodsServiceAndEndpoints` |
| R9 | deployment attestor, neutral observation | `TestKubernetesAttestationBindsRunningImageRepositoryCommitAndEndpoint` |
| R10 | composition, private secret bundle | `TestKubernetesSecretsNeverCrossProviderBoundary` |
| R11 | metadata/clock/TTL | `TestKubernetesEnvironmentCarriesVerifiableOwnerAndExpiry` |
| R12 | release/delete precondition | `TestKubernetesReleaseIsDurableIdempotentAndUIDSafe` |
| R13 | error mapper/workflow result | `TestKubernetesFailuresRemainOperational` |
| R14 | context, pagination, concurrency | `TestKubernetesOperationsAreBoundedCancelableAndRaceSafe` |
| R15 | concrete client, observer, verification | contract/global/race/Linux/cross/coverage/comment gates |

## Decisiones y alternativas descartadas

- **Aceptar manifests/Helm desde el repositorio:** convierte datos no confiables
  en autoridad ejecutable y permite ampliar permisos. Se usa renderer cerrado.
- **Crear workload antes de NetworkPolicy/quota:** abre una ventana insegura de
  ejecución. Todo bootstrap se verifica primero.
- **Confiar solo en Namespace para aislamiento:** no controla red, recursos ni
  identidad del Pod. Se aplican controles independientes.
- **Aceptar una tag de imagen con pull-always:** el contenido puede cambiar entre
  aprobación y ejecución. Solo digest completo es autoridad.
- **Declarar ready por Deployment Available:** no acredita imagen, revisión ni
  endpoint. Se observa la cadena completa.
- **Usar server-side apply/update sobre mismatch:** podría apropiarse de recursos
  ajenos o encubrir drift. Solo create-or-get exacto.
- **Borrar namespace por nombre/labels:** puede eliminar una recreación o un
  entorno ajeno. Se exige nombre + UID + ownership y delete precondition.
- **Guardar secretos cifrados en lifecycle records:** siguen fuera de la
  responsabilidad del core. Se ligan privadamente en composición.
- **Añadir un watcher/cleanup goroutine:** duplica la feature 34 y complica
  ownership. Readiness es polling acotado por el lifecycle existente.
- **Usar el paquete de tool Kubernetes:** mezcla provisioning con observación
  funcional de feature 33. El adapter vive bajo environment.

## Conformidad arquitectónica

- `ARCH-001/002/008/011`: el modelo no controla Kubernetes; catálogo, renderer
  y cliente específico son composition-owned y fail-closed.
- `ARCH-003/004`: readiness y attestation habilitan el target pero no producen
  evidencia ni verdict funcional.
- `ARCH-005/006`: lifecycle y cliente propagan context y budgets finitos.
- `ARCH-007`: acquisition/release intents y estados permanecen en el store
  durable de feature 31; metadata Kubernetes permite reconciliación.
- `ARCH-009`: secretos solo llegan a transporte/Secret y se excluyen de toda
  proyección durable/model-facing.
- `ARCH-010`: fake hermético y cliente inyectado mantienen `go test` sin red.
- `ARCH-012`: el provider devuelve observaciones; no muta el run.
- `CONV-001..010`: interfaz del consumidor, tipos cohesionados, errores tipados,
  contexto propagado, sin goroutines ocultas, tiempo inyectado, tests
  deterministas, trazabilidad y una sola feature.
- Toda entidad, constante, variable, función, método y helper de test nuevo o
  modificado llevará un comentario que explique su responsabilidad sistémica,
  conforme a la convención acordada para iteraciones posteriores.

No se solicita ninguna excepción a arquitectura o convenciones.
