# Herramienta de validación operativa de Kubernetes — Tasks

Las tareas se ejecutan en orden y con TDD: cada bloque comienza por los tests
descritos, implementa el comportamiento mínimo y refactoriza sin ampliar el
alcance aprobado. Al modificar o crear código, toda entidad, constante,
variable, función, método y helper de test llevará un comentario sobre su
responsabilidad sistémica.

- [x] T1. Definir y sellar el contrato de catálogo, configuración, outcomes y ToolSpec. [R1, R2, R10, R15]
  - Escribir tests de catálogo canónico/inmutable, digests, duplicados,
    relaciones incompatibles, límites obligatorios y metadata
    `Effects=false`/`Idempotent=true`.
  - Definir el envelope normalizado v1, check kinds y categorías cerradas.
  - Implementar constructor fail-closed sin defaults y registro único de
    `kubernetes.observe`.

- [x] T2. Construir la autoridad desde ValidationTarget/Binding y el namespace atestado. [R1, R9]
  - Escribir tests para target/binding válidos, expirados, alterados, cross-run,
    provider distinto, ReservationID ausente y catálogo incompatible.
  - Derivar namespace y UID solo del target; copiar catálogo/target y revalidar
    vigencia al ejecutar.
  - Confirmar mediante tests que ningún fallo previo a autoridad llama al API.

- [x] T3. Implementar input exacto, paginador, límites, snapshot y taxonomía de errores. [R2, R10, R11, R12, R13]
  - Escribir tests que acepten solo check+alias y rechacen names, namespaces,
    selectors, queries, manifests, JSONPath y claves extra antes de I/O.
  - Escribir tests de múltiples páginas, token repetido/vacío, límite exacto,
    exceso de páginas/items/bytes, versions opacas, orden/digest canónico,
    anchors, drift y reemplazo por UID.
  - Implementar el puerto read-only, pager por llamada, normalizer, double-read
    de anchors y error mapper content-free.
  - Escribir tests coordinados por canales para cancelación y timeout, sin
    `time.Sleep`, confirmando que no comienzan lecturas posteriores.

- [x] T4. Implementar la comprobación de disponibilidad de Deployment. [R3, R11, R13]
  - Escribir tests de generation/observedGeneration, réplicas
    desired/updated/available, conditions allowlisted, NotFound y estados
    incompletos.
  - Verificar namespace, UID/owner y anchor final; proyectar solo contadores,
    flags, reasons permitidos y resourceVersion.
  - Probar que rollout incompleto sigue siendo observación y que la tool no
    genera assertion ni verdict.

- [x] T5. Implementar la comprobación paginada de Pods y reinicios. [R4, R10, R11, R13]
  - Escribir tests con varios Pods/contenedores, distintos órdenes, owner ajeno,
    restart counts, readiness, terminating, cero items y página truncada.
  - Filtrar defensivamente por owner/selector precompilado, crear aliases
    efímeros seguros y ordenar la proyección.
  - Confirmar que ausencia, evidencia parcial y datos inválidos permanecen
    categorías distintas.

- [x] T6. Implementar la comprobación de estado de Jobs. [R5, R11, R13]
  - Escribir tests para Job activo, completo, fallido, ausente, reemplazado y
    conditions con reason conocido/desconocido y message canario.
  - Proyectar contadores y conditions allowlisted sin logs, messages ni acceso a
    Pods interactivo.
  - Verificar anchor/UID y clasificación de integridad antes del retorno.

- [x] T7. Implementar la comprobación de Services y EndpointSlices. [R6, R10, R11, R13]
  - Escribir tests de Service interno, puertos declarados, múltiples páginas de
    EndpointSlices, owners/service labels ajenos, ready/not-ready, ausencia y
    truncación.
  - Agregar únicamente counts y versions autorizadas; no exponer IPs ni Pod
    names.
  - Releer Service/namespace y distinguir cero endpoints confirmado de conjunto
    parcial.

- [x] T8. Implementar la comprobación acotada de Events. [R7, R10, R13]
  - Escribir tests con recursos autorizados/ajenos, APIs de Events soportadas,
    duplicados, reasons desconocidos, messages/notes canario, paginación y
    MaxEvents.
  - Proyectar tipo, reason normalizado, count y tiempos en orden estable; omitir
    todo texto/reporting identity no confiable.
  - Marcar pérdida de exhaustividad como insufficient evidence con truncación.

- [x] T9. Implementar conectividad declarada sin probe activo. [R8, R11, R13]
  - Escribir tests de relaciones y puertos catalogados/no catalogados, Service,
    EndpointSlices, NetworkPolicy presente/ausente/alterada y drift concurrente.
  - Resolver origen/destino exclusivamente por relationship alias y comparar
    protocolo/puerto/policy sellados.
  - Confirmar mediante fake que no existe dial, exec, port-forward ni request de
    red y que el output no afirma reachability real.

- [x] T10. Implementar el adapter productivo read-only y la integración con planning/evidence/assertions. [R9, R12, R14, R15]
  - Escribir tests del cliente fake/API server que registren cada verbo,
    resource, namespace y selector; exigir solo `get/list` allowlisted y cero
    Secrets, logs, subrecursos o mutaciones.
  - Mapear client-go a modelos propios y Forbidden/transient/cancelación a la
    taxonomía segura sin bodies remotos.
  - Añadir tests de executor que persistan output tratado y demuestren:
    condición observada falsa → assertion failed; truncación/drift/acceso/error
    → inconclusive; ausencia solo falla mediante assertion explícita.
  - Verificar que retries pertenecen al executor y cada intento empieza un
    snapshot nuevo.

- [x] T11. Completar pruebas de seguridad, trazabilidad y gates de verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Ejecutar la matriz hermética sobre Deployment, Pods, Job, Service,
    EndpointSlice, Events y NetworkPolicy, incluyendo aislamiento de namespace,
    Forbidden, pagination, truncation, drift, timeout y cancelación.
  - Insertar canarios en Secrets y en todos los campos omitidos; escanear output,
    errores, journal, evidencia persistida y contexto del modelo para demostrar
    ausencia byte-for-byte.
  - Auditar y añadir comentarios sistémicos a toda entidad, constante, variable,
    función, método y helper de test nuevo o modificado.
  - Ejecutar gofmt check, `go vet ./...`, `go test ./...`, `go test -race ./...`,
    repetición race del paquete afectado y coverage >= 85 % sin redondear.
  - Registrar `R<n> → test concreto → resultado`, comandos, coverage y riesgos
    en `progress/kubernetes-validation-tool/implementation.md` durante la futura
    fase de implementación.
