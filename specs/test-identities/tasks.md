# Identidades seguras de prueba — Tasks

- [x] T1. Evolucionar el modelo interno de specification para identidades v2. [R1]
  - Escribir primero tests de entidades, IDs únicos, target references,
    credential refs opacas, mecanismos cerrados y operaciones exactas.
  - Mantener `Specification` independiente de YAML, resolvers y bytes secretos.
  - Añadir comentario sistémico a cada entidad, constante, función, método y
    helper productivo o de test creado/modificado.

- [x] T2. Implementar el adapter YAML v2 y la compatibilidad estricta v1. [R1]
  - Añadir fixtures válidos/inválidos con unknown/duplicate fields, unions,
    referencias, permisos y valores con apariencia de secreto.
  - Despachar por versión antes de decodificar y demostrar que fixtures v1
    conservan exactamente aceptación/rechazo y no reciben defaults.
  - Verificar que errores exponen solo categoría/field path y nunca el valor.

- [x] T3. Crear el catálogo inmutable y los operation bindings. [R2, R3]
  - Probar snapshots, copias profundas, orden canónico, varias identidades
    elegibles y ausencia de selección implícita.
  - Implementar matching estructural de target/capability/method/path template
    con parámetros, escapes, traversal y prefijos adversarios.
  - Exponer descriptores sin credential ref, resolver ni handles ejecutables.

- [x] T4. Integrar descriptores y selección en agent planning. [R3]
  - Ampliar protocolo/contexto con descriptores filtrados e `identity_id`,
    rechazando campos de material o fuente de credenciales.
  - Incluir identity ID y operation binding en canonicalización/provenance y
    validar la selección antes de crear `PreparedPlan`.
  - Probar modelo que inventa identidad/mecanismo/operación, mutaciones y
    prompt injection en constraints/contratos.

- [x] T5. Extender planes, manifest y executor con el guard de identidad. [R2, R4, R7, R8]
  - Añadir binding no secreto a `PlanStep`/`ToolCall` y una versión nueva del
    manifest sin reinterpretar v1.
  - Ejecutar validación de identidad tras resolver input y antes de policy;
    revalidar contexto tras policy/budget e inmediatamente antes de I/O.
  - Probar cada rechazo con spies de resolver, DNS, dial y transport a cero, y
    clasificación inconclusive/error/cancelled separada de assertion failed.

- [x] T6. Implementar resolver, lease y transporte autenticado. [R4, R5, R11]
  - Definir puertos consumidor-owned, errores tipados y material no
    serializable con liberación idempotente.
  - Inyectar bearer/header/cookie sobre una request clonada, medir límites
    finales y proteger redirects sin exponer valores al HTTP tool.
  - Probar vigencia con reloj, mecanismo mismatch, cancelación en cada frontera,
    carrera cancelación/adquisición y liberación exacta.

- [x] T7. Completar aislamiento, retry y concurrencia de credenciales. [R5, R7, R11]
  - Usar secretos canario distintos para identidades concurrentes y demostrar
    cero contaminación entre requests, errores o leases.
  - Revalidar y resolver por intento; clasificar fallos permanentes sin retry y
    conservar budgets/idempotencia para errores transitorios del target.
  - Probar coalescing opcional con barriers, waiters cancelados, owner/join
    explícitos, race detector y sin `time.Sleep` como coordinación.

- [x] T8. Ampliar redacción de autenticación de forma monotónica. [R6]
  - Incorporar headers/cookies estándar y configurados y JSON paths de auth sin
    permitir que configuración retire reglas base.
  - Probar capitalización, estructuras anidadas, respuestas reflejadas,
    `Set-Cookie`, truncado, errores de sanitizer y canarios en bodies.
  - Confirmar que evidence/model projection solo reciben records tratados y que
    fallo de redacción no persiste payload parcial.

- [x] T9. Integrar binding, journal, recovery y ejecución durable. [R7, R8, R10, R11]
  - Sellar identity catalog/operation bindings con run, revisión, target y plan,
    y revalidar drift en approval y pre-I/O.
  - Añadir eventos/records seguros por intento con identity ID, binding digest y
    categoría, sin reference, secret, expiry exacta ni error externo.
  - Probar restart, redelivery, retry, rotación, append-only, cancelación y que
    ningún store durable contiene material o leases.

- [x] T10. Integrar composición local y validation workflow. [R8, R9]
  - Construir catálogos/resolvers desde configuración confiable y fallar cerrado
    si una referencia no tiene resolver, sin resolver material al arrancar un
    run que no lo necesita.
  - Compartir el mismo servicio para planificación, aprobación, ejecución y
    recovery; preservar revision/preview bindings existentes.
  - Probar composición ausente/incompatible, drift entre fases, retry y
    recuperación sin fallback entre identidades.

- [x] T11. Mantener paridad CLI, API, MCP y GitHub. [R6, R9]
  - Actualizar schemas/versiones necesarios sin aceptar raw credentials,
    credential refs, resolver locations ni autoridad autoafirmada.
  - Probar resultados/códigos equivalentes, payloads unknown/oversized,
    redelivery y errores seguros en las cuatro superficies.
  - Probar que PR body, comments, labels y parámetros externos no seleccionan
    otra identidad ni alteran el snapshot repository/head-bound.

- [x] T12. Completar observabilidad y matriz adversaria de secretos. [R6, R10, R11]
  - Emitir un terminal record allowlisted por intento y verificar orden y
    cardinalidad ante éxito, rechazo, error, retry y cancelación.
  - Escanear recursivamente model requests/responses, journal, evidence,
    artifacts, reports, logs, metrics, errores, CLI/API/MCP/GitHub y filesystem
    con canarios distintos por superficie.
  - Repetir cancelación simultánea con checkpoints, concurrencia y límites
    exactos; confirmar que no queda trabajo en background.

- [x] T13. Ejecutar gates, trazar requirements y documentar la entrega. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Auditar el diff para comentarios sistémicos en entidades, constantes,
    funciones, métodos, DTOs, helpers, fakes y fixtures programáticas.
  - Ejecutar `gofmt`, compile-only, `go vet`, suites completas/focales repetidas,
    race y coverage mínimo del 85 % del código nuevo/modificado.
  - Crear `progress/test-identities/implementation.md` con mapa real R→test,
    tasks, comandos, coverage, limitaciones y evidencia reproducible.
  - Marcar T1–T13 `[x]` solo tras cumplir su evidencia y solicitar revisión
    independiente sin modificar `feature_list.json`.
