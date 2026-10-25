# Abstracción de obtención de entornos — Tasks

Las tareas se ejecutan en orden y cada cambio de comportamiento incorpora sus
tests en la misma tarea. Toda entidad, constante, variable, función, método y
helper de test nuevo o modificado debe incluir un comentario que explique su
responsabilidad dentro del sistema.

No se implementan Kubernetes, cloud/PaaS/Terraform, manifests, comandos,
namespaces, imágenes, quotas ni infraestructura real. `ExistingURLProvider`
adapta feature 19 sin modificar su contrato no destructivo; un managed provider
existe únicamente como fake hermético de la suite contractual.

- [x] T1. Definir el modelo canónico `ValidationTarget/v1`. [R3, R6, R7, R12]
  - Crear enums cerrados y tipos para target, attestation, network projection,
    capabilities, lifecycle digest, binding, counts y errores.
  - Implementar strict decode, deep-copy, canonical JSON, relaciones y digests.
  - Probar unknown/null/duplicate fields, IDs/OIDs/URLs/enums inválidos,
    self-signed tampering, mutación posterior y orden canónico.

- [x] T2. Implementar el catálogo sellado de selecciones. [R1, R3, R10]
  - Modelar alias/version, provider/kind, template/reference, perfil, network
    policy, credential reference/secretless, capabilities y policy digest.
  - Rechazar default, duplicados, widening y cualquier autoridad procedente de
    request/modelo/contenido externo.
  - Probar permutations, aliases desconocidos y cross-project/repository/run.

- [x] T3. Introducir el puerto mínimo `EnvironmentProvider`. [R2, R6, R9]
  - Definir acquire/reconcile, observe/readiness y release con DTOs propios,
    contexts, keys y budgets acotados.
  - Mantener SDKs, transports, manifests, comandos y credential resolver fuera
    del interface.
  - Añadir compile assertions y tests de contrato sobre invalid/cancel/timeout.

- [x] T4. Construir el managed fake y la suite contractual reusable. [R2, R6, R9, R14]
  - Programar pending/ready/drift, bloqueo cancelable, response loss, conflict,
    already released y auditoría de llamadas mediante canales.
  - Ejecutar la misma matriz por factory, sin branches por provider, contra el
    fake y posteriormente ExistingURLProvider.
  - Confirmar ausencia de red, SDK, shell, filesystem de infraestructura y
    sleeps reales.

- [x] T5. Implementar `LifecycleService` y estados provider-neutral. [R3, R4, R9, R11]
  - Coordinar selected→acquiring→acquired→waiting→ready→bound con guards y
    ownership explícitos.
  - Validar cada observación y publicar target solo tras readiness, attestation
    y network authorization.
  - Probar unavailable/unhealthy/denied/mismatch/budget y cero model/tools antes
    de target confirmado.

- [x] T6. Implementar persistencia, leases, CAS e idempotencia. [R6, R8, R12]
  - Persistir records content-free, receipts y acquisition/release key digests
    con schema/digests/relaciones estrictos.
  - Cubrir staging, sync, publicación atómica, response loss, reopen, corrupción,
    rollback, contenders y objetos content-addressed compartidos.
  - Probar que runs independientes progresan y que no hay disco/red bajo locks.

- [x] T7. Añadir readiness, retries, cancelación y timeouts acotados. [R4, R9, R11]
  - Aplicar deadlines hijos, max attempts/elapsed/wait y clock/waiter/jitter
    inyectados; reconcile antes de repetir acquire incierto.
  - Conservar `context.Cause`, distinguir cancelled/timeout y unir todo trabajo
    propio antes de retornar.
  - Probar cada boundary, espera fuera de deadline, errores permanentes y
    matrices repeated/race sin `time.Sleep`.

- [x] T8. Implementar release durable y recovery. [R8, R9, R11, R12]
  - Persistir `release_intent` antes de I/O y converger releasing/
    release_pending/released con la misma key.
  - Usar contexto de cleanup independiente y finito tras cancelación sin cambiar
    el resultado primario.
  - Inyectar crash antes/después de cada boundary, CAS perdido, response loss,
    already released, owner handoff y restart.

- [x] T9. Implementar `ExistingURLProvider` como adapter. [R2, R5, R14]
  - Delegar exactamente a acquire/restore/release del lifecycle preview existente
    y traducir su binding sin reinterpretar autoridad.
  - Conservar provider/environment/reservation, repository/commit, fingerprint,
    attestation, target y network policy efectivos.
  - Probar cero provision/deploy/update/restart/scale/delete/destroy y una sola
    adquisición/liberación incluso durante migración/recovery.

- [x] T10. Revalidar target, lifecycle, atestación y policy. [R3, R4, R7]
  - Reconstruir provider/catalog/policies actuales tras restart y observar el
    mismo lifecycle antes de execution/recovery/publication.
  - Detectar drift independiente de selection, template, environment,
    repository, commit, attestation, endpoint, capabilities y network policy.
  - Probar atestación expirada entre batches y cero nuevas tools tras drift.

- [x] T11. Confinar credenciales y payloads al adapter. [R1, R2, R10, R12]
  - Resolver referencias solo en composición y entregar material únicamente al
    transporte concreto; secretless no resuelve nada.
  - Mapear errores sin bodies/headers y aplicar secret gate antes de superficies
    durables/model-facing.
  - Probar canarios byte-for-byte en target, binding, lifecycle, errors, events,
    model, plan, evidence, package, report y journal.

- [x] T12. Integrar un único `EnvironmentPort` en validationworkflow. [R4, R7, R8, R11, R13]
  - Sustituir el branch `PreviewPort` por acquire/restore/revalidate/release
    provider-neutral sin type switches por provider.
  - Persistir target/lifecycle digests antes de plan/approval/execution y cleanup
    intent antes de release.
  - Probar que fallo operacional produce error/inconclusive/cancelled o cleanup
    pending, mientras assertion real conserva `failed`.

- [x] T13. Migrar composición y cuatro superficies. [R1, R5, R10, R13]
  - Componer catálogo, ExistingURLProvider, store, policies, clocks, observer y
    transportes exclusivamente desde configuración confiable.
  - Traducir direct/preview autorizados a selecciones selladas y eliminar caminos
    paralelos que omitan lifecycle o dupliquen cleanup.
  - Probar paridad CLI/API/MCP/GitHub, payload adversario y mismo binding digest.

- [x] T14. Cerrar observabilidad content-free y exactly-once. [R8, R11, R12]
  - Emitir transiciones/terminal records con IDs, digests, phase, attempts,
    durations y categorías allowlisted.
  - Separar stable key y content digest integral; observer failure no muta target,
    binding, release intent ni veredicto.
  - Probar reopen, redelivery, response loss, tamper, cancel/timeout y ausencia de
    endpoint/handle/secrets/payload externo.

- [x] T15. Ejecutar matrices contractuales, adversarias y de concurrencia. [R2, R5, R6, R7, R8, R9, R10, R13, R14]
  - Ejecutar la misma suite completa contra ExistingURLProvider y managed fake.
  - Repetir same/different lifecycle, owner contention, acquire/release inciertos,
    drift y cancelación bajo normal/race alta.
  - Auditar que ExistingURLProvider no provisiona y que el fake gestionado nunca
    se compone en producto; confirmar cero goroutine/timer/temp leaks.

- [x] T16. Completar trazabilidad y verificación multiplataforma. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Registrar `R1–R15→test`, tasks, comandos, coverage y riesgos en
    `progress/environment-provider/implementation.md`.
  - Ejecutar gofmt, auditoría de comentarios, diff-check, vet, compile-only,
    focales repetidos, global normal/race, runtime Linux y cross-builds
    Windows/Linux/Darwin aplicables.
  - Medir statements nuevos/modificados con build tags y demostrar coverage sin
    redondear >=85%; confirmar que tests no usan providers/red/Kubernetes/modelo
    reales.
  - Auditar temporales, secretos, scope y comentarios sistémicos en toda entidad,
    constante, variable, función, método y helper nuevo/modificado.
