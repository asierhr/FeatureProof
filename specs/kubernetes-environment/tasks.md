# Entornos efímeros sobre Kubernetes — Tasks

Las tareas se ejecutan en orden y cada comportamiento nuevo incorpora sus tests
en la misma tarea. Toda entidad, constante, variable, función, método y helper
de test nuevo o modificado debe incluir un comentario que explique su
responsabilidad dentro del sistema.

Esta feature implementa únicamente el provider de entornos. No añade la tool de
inspección Kubernetes (feature 33), un cleanup controller periódico (feature
34), un control plane distribuido (feature 35) ni configuración general de
proyecto (feature 36).

- [ ] T1. Definir y sellar el catálogo privado Kubernetes. [R1, R3, R5, R6, R11]
  - Modelar template/profile/network policy, cluster capabilities, imagen OCI
    por digest, recursos, security, TTL y referencias secretas opacas.
  - Implementar strict decode, canonicalización, deep copy, relaciones y digest
    sin valores secretos ni defaults.
  - Probar campos desconocidos/duplicados, imagen tag/abreviada, widening de
    policy, límites inválidos, selección incompatible y tampering recalculado.

- [ ] T2. Introducir el puerto Kubernetes específico y el fake hermético. [R3, R7, R14, R15]
  - Definir `ClusterClient` consumer-owned con operaciones tipadas, acotadas y
    namespaced; excluir raw REST, apply/patch libre, exec, logs y list global.
  - Construir fake concurrent-safe con UIDs/resourceVersions, reactors,
    barreras, response loss y errores tipados, sin red ni sleeps.
  - Probar context cancel/deadline, límites de cardinalidad/paginación y que no
    existe camino de operación arbitraria.

- [ ] T3. Implementar renderer cerrado y desired-state canónico. [R1, R3, R5, R6, R11]
  - Renderizar solo Namespace, ResourceQuota, LimitRange, ServiceAccount,
    NetworkPolicies, Secrets autorizados, Deployment y Service.
  - Fijar nombres, owner/run/project/digests/expiry, security context,
    requests/limits, probes, selectors y endpoint desde catálogo sellado.
  - Probar snapshots tipados, orden estable, aislamiento de dos runs y rechazo
    de command/args/volume/service account/namespace no autorizados.

- [ ] T4. Implementar identidad de namespace y lifecycle handle. [R2, R7, R11, R12]
  - Derivar nombre DNS-safe sin IDs sin acotar, capturar UID y sellar un handle
    opaco ligado a run, acquisition, selection y project.
  - Verificar metadata y relaciones exactas en acquire, observe y release.
  - Probar collision, namespace hostil, UID reemplazado, cross-run/project,
    restart y expiración/TTL alterados.

- [ ] T5. Implementar bootstrap de seguridad anterior a workloads. [R4, R5, R6, R7]
  - Crear/verificar namespace, Pod Security, quota, LimitRange, service account
    sin token y NetworkPolicies default-deny + allowlists en orden estricto.
  - Tratar AlreadyExists como readback + igualdad canónica; no actualizar,
    parchear, adoptar ni borrar mismatches.
  - Inyectar fallo antes/después de cada boundary y probar cero workloads ante
    bootstrap incompleto, policy no soportada o admission/quota denial.

- [ ] T6. Confinar credenciales y application secrets. [R3, R10, R15]
  - Construir cluster transport y `PrivateSecretBundle` solo en composición;
    mantener valores fuera de catálogo, requests, handles y DTOs seguros.
  - Crear únicamente Secrets provider-owned referenciados por el template;
    `secretless` no resuelve ni crea material.
  - Probar canarios en auth/pull/application secret y escanear errores, logs,
    store, eventos, target, model, evidence, package y report byte-for-byte.

- [ ] T7. Crear workload y Service inmutables. [R3, R5, R7]
  - Crear/verificar Deployment con imagen por digest, revisión exacta, security
    context y recursos; crear/verificar después el Service interno.
  - Reanudar después de respuesta perdida mediante GET y desired digest sin
    sobrescribir objetos incompatibles.
  - Probar pérdida de respuesta en cada create, objetos preexistentes idénticos
    y hostiles, replicas/selector/port/image drift y ausencia de doble objeto.

- [ ] T8. Implementar Acquire/Reconcile idempotentes. [R2, R4, R7, R11, R14]
  - Validar en cada boundary selection/provider/version/catalog y ejecutar el
    reconciliador desde el primer recurso no confirmado.
  - Devolver una observación pending con namespace/UID/handle exactos sin
    publicar aún un target.
  - Probar redelivery, fallo parcial, misma/diferente key, llamadas concurrentes
    y convergencia sin locks durante I/O.

- [ ] T9. Implementar readiness completa y acotada. [R8, R14]
  - Leer Deployment, Pods propios, Service y EndpointSlices con selectors y
    límites estrictos; no abrir watches indefinidos.
  - Clasificar pending/ready/unhealthy y mapear cancelación, timeout, resource
    missing, cardinalidad excesiva y API failure.
  - Probar matriz de generation/replicas/conditions/probes/endpoints, mutación
    concurrente y join/leak bajo normal y race sin `time.Sleep`.

- [ ] T10. Implementar atestación y revalidación del despliegue efectivo. [R8, R9, R11]
  - Verificar namespace UID, ownership, repo/full commit, template/profile,
    image esperada e imageID de todos los Pods, revision y endpoint.
  - Producir referencia/digest y `ValidUntil` acotado por expiry, más una
    `Observation` que feature 31 pueda sellar como `ValidationTarget/v1`.
  - Probar imageID ausente/distinto, Pods mezclados, annotations/UID/endpoint
    drift y revalidación antes de execution/recovery/publication.

- [ ] T11. Implementar release UID-safe e idempotente. [R2, R7, R11, R12, R14]
  - Verificar handle, request, selection, owner, namespace y UID antes de delete
    y usar precondición UID; nunca borrar por selector ni retirar finalizers.
  - Traducir NotFound a already released y conservar timeout/respuesta perdida/
    terminating como cleanup pendiente mediante el lifecycle neutral.
  - Probar release tras cada outcome, namespace recreado, response loss,
    cancelación y redelivery sin declarar éxito incierto.

- [ ] T12. Implementar el cliente productivo y composición explícita. [R1, R6, R10, R14, R15]
  - Fijar una versión compatible del cliente oficial y traducir solo los DTOs y
    métodos del port; mapear errores sin bodies ni mensajes remotos.
  - Componer cluster/profile/catalog/transport/bundles y registrar el provider
    de forma explícita en `cmd/featureproof`, sin kubeconfig/env/default/fake.
  - Añadir compile/composition tests que prueben identidad/version/capabilities,
    policy de red soportada y fallo temprano de dependencias ausentes.

- [ ] T13. Integrar contrato neutral y outcomes operacionales. [R7, R8, R9, R12, R13, R15]
  - Ejecutar la suite común de `EnvironmentProvider` de feature 31 contra
    KubernetesProvider sin ramas especiales ni cambios de autoridad.
  - Verificar acquire→pending→ready→binding→revalidate→release intent→release y
    cleanup pending tras efectos inciertos.
  - Probar que admission/quota/security/readiness/attestation/cleanup producen
    outcomes operacionales, mientras un assertion real conserva `failed`.

- [ ] T14. Ejecutar matrices adversarias, de recovery y concurrencia. [R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R14]
  - Inyectar crash/response loss antes y después de cada boundary, tampering de
    cada objeto/relación y errores API tipados.
  - Repetir same/different run, contender, cancel/deadline y release incierto
    bajo normal/race alta con canales deterministas.
  - Auditar cero adopción/update/delete de recursos ajenos, cero leaks, cero
    red real y ausencia de secretos/manifests arbitrarios en superficies.

- [ ] T15. Cerrar trazabilidad y verificación multiplataforma. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Registrar `R1–R15→test`, tasks, comandos, coverage, dependencias y riesgos en
    `progress/kubernetes-environment/implementation.md`.
  - Ejecutar gofmt, comentarios, diff-check, vet, compile-only, focales
    repetidos, global normal/race, runtime Linux y cross-builds aplicables.
  - Medir el diff productivo completo y demostrar coverage sin redondear ≥85%;
    `go test` no contacta cluster, registry, DNS, cloud, modelo ni red externa.
  - Auditar temporales, secretos, scope y comentarios sistémicos en toda
    entidad, constante, variable, función, método y helper nuevo/modificado.
