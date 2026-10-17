# Integración con pull requests de GitHub — Tasks

- [x] T1. Definir el modelo propio, invariantes, errores y configuración de la integración. [R2, R4, R5, R12]
  - Escribir tests de identidades, OID completo, snapshots, permisos mínimos,
    sealing y detección de manipulación.
  - Definir interfaces pequeñas del consumidor para GitHub, autorización,
    validación, packages, credenciales, reloj, jitter e IDs sin tipos de SDK.
  - Añadir comentario de responsabilidad sistémica a cada entidad, función y
    helper nuevo o modificado.

- [x] T2. Implementar el boundary de webhook y el registro durable de deliveries. [R1, R3, R11, R12]
  - Probar límite de body, HMAC-SHA256 en tiempo constante antes del decode,
    cabeceras y traducción estricta de `issue_comment.created`.
  - Probar command parser exacto, issue que no es PR, evento ignorado, replay
    serial/concurrente y collision de delivery ID con digest distinto.
  - Implementar journal append-only, snapshot, CAS y lease para confirmar el
    receipt antes de responder.

- [x] T3. Implementar refresh y autorización de la PR, head y política de forks. [R1, R2, R4, R9]
  - Probar instalación, repo, PR, actor y selección válidos/denegados, PR
    cerrada, head inválido y evento tardío.
  - Probar fork denegado por defecto, excepción exacta secretless y rechazo de
    cualquier selección con credenciales.
  - Traducir el snapshot autorizado a `revisionbinding` sin aceptar commits,
    targets, URLs o configuración libres.

- [x] T4. Implementar asociación durable y gateway al caso de uso de validación. [R1, R2, R4, R11]
  - Probar inicio y asociación confiable de un run, idempotencia, handles
    reautorizados y binding exacto a repository/head.
  - Probar comandos concurrentes, CAS por asociación y que reiniciar no vuelve
    a solicitar un run confirmado.
  - Probar H1→H2: cancelación/retirada segura de H1, ningún run automático para
    H2 y una solicitud nueva sin heredar aprobación o evidencia.

- [x] T5. Implementar la proyección segura de resultados y enlaces. [R2, R7, R12]
  - Probar summaries para passed, failed e inconclusive desde package verificado,
    incluidos fallos, no verificados y evidence handles.
  - Probar package corrupto/foreign/binding inconsistente como error operativo.
  - Probar origins HTTPS exactos, ausencia de userinfo/query/fragment/path
    local, truncación por elementos y secretos/texto hostil centinela.

- [x] T6. Implementar publicación idempotente del check propio. [R2, R6, R7, R10, R11]
  - Probar `queued`, `in_progress`, las cinco conclusiones terminales y `stale`
    con outcome FeatureProof explícito.
  - Probar un check por run/revisión, update del ID confirmado, foreign check
    rechazado, create/update incierto, reconciliación y conflicto por duplicado.
  - Persistir y recuperar intents sin mantener locks durante GitHub o packages.

- [x] T7. Implementar el comentario resumen único y seguro. [R7, R8, R10, R11]
  - Probar create inicial, updates de progreso/resultado y ausencia de
    duplicados ante replay, retry y reinicio.
  - Probar reconciliación paginada/acotada de create incierto por marker + bot,
    y rechazo de marker o comment ID suplantados.
  - Compartir la proyección segura con el check sin publicar payloads ni
    evidencia raw.

- [x] T8. Implementar autenticación de instalación y adapter REST con mínimo privilegio. [R4, R5, R10, R12]
  - Probar token restringido al repository ID y permisos exactos, expiry,
    rotación y errores sin exponer private key, JWT, token o headers.
  - Mantener DTOs/HTTP/API version dentro del adapter y traducir todas las
    respuestas y errores al contrato propio.
  - Auditar con el fake que solo se leen PR/check/comment y solo se crean o
    actualizan check/comentario propios; ningún endpoint de código, ramas,
    reviews, otros checks o configuración es invocado.

- [x] T9. Implementar retry, rate-limit, cancelación y supervisor. [R3, R6, R8, R10, R11]
  - Probar `Retry-After`, primary reset, secondary limit, 408/429/5xx/transporte,
    4xx permanente y presupuestos con reloj/jitter falsos, sin `time.Sleep`.
  - Exigir reconciliación antes de repetir creates y serializar writes por
    asociación.
  - Probar cancelación durante HTTP, backoff, run y publicación, join de cada
    worker y finalización independiente acotada.

- [x] T10. Integrar composición y recuperación productiva cerrada. [R1, R4, R5, R9, R11]
  - Validar al arranque secret source, permisos, allowlists, limits, API version,
    evidence origins y stores; no guardar contextos en servicios de larga vida.
  - Recuperar receipts, leases e intents pendientes de forma idempotente y
    aislar asociaciones concurrentes.
  - Mantener fail-closed cualquier selección que necesite el adapter preview de
    la feature 19.

- [x] T11. Completar contrato hermético, trazabilidad y verificaciones. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Ejecutar los tests nominales del mapa R1–R12 con fake GitHub y demás fakes,
    sin red, credenciales ni entorno real.
  - Verificar comentarios sistémicos de todos los símbolos y helpers afectados,
    `go test -race`, `go vet`, `go test ./...`, coverage >=85% del paquete y
    builds soportadas según `docs/verification.md`.
  - Registrar durante implementación el mapa requirement → test → resultado y
    cualquier limitación ambiental, sin debilitar assertions.
