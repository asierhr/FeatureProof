# Contexto acotado de pull requests de GitHub — Tasks

Las tareas se ejecutan en orden y cada cambio de comportamiento incorpora sus
tests en la misma tarea. Todo símbolo nuevo o modificado —entidad, constante,
variable, función, método o helper de test— debe incluir un comentario sobre su
responsabilidad dentro del sistema.

No se implementan webhooks, autorización de comandos, checks/comentarios,
checkout de repositorios, resolución de entornos ni configuración de proyecto.
La feature reutiliza la autoridad de `github-pr-integration`, entrega una
proyección neutral a `context-assembly` y mantiene todo acceso real a GitHub
fuera de `go test`.

- [x] T1. Definir el modelo canónico `PullRequestContext/v1`. [R1, R2, R3, R14]
  - Crear enums cerrados y tipos para request, authority, policy reference,
    metadata, file/hunk, omission, constraint, contract ref, completeness,
    context, binding, counts, record y errores.
  - Implementar strict validation, deep-copy, canonical JSON, orden y digests.
  - Probar unknown/null/duplicate fields, OIDs/IDs/paths/enums inválidos,
    tampering, mutación posterior y equivalencia canónica.

- [x] T2. Modelar y validar la policy de composición. [R1, R4, R6, R7, R8]
  - Sellar kind/version de extractor, catálogo, routes OpenAPI, path/status/type
    allowlists y todos los budgets positivos/finitos.
  - Permitir solo narrowing explícito de límites; rechazar policy/authority
    aportada por contenido o callers no GitHub.
  - Probar overlap/duplicados/ambigüedad de rutas, widening y límites exactos.

- [x] T3. Añadir el puerto mínimo de lectura GitHub y DTOs privados. [R1, R2, R3, R11]
  - Extender el adapter existente con metadata title/body/base/head y archivos
    con current/previous path, status, counts y patch opcional.
  - Reutilizar token scope, API version, HTTPS y clasificación segura sin
    devolver URLs, headers, remote bodies ni credentials al dominio.
  - Probar endpoints/métodos/headers exactos con servidor local y confirmar que
    solo se requieren lecturas autorizadas de pull requests.

- [x] T4. Implementar la lectura coherente metadata/files/metadata. [R2, R10, R12]
  - Comparar fingerprints completos y descartar el intento entero ante drift de
    repository, PR, state, head, base, title o body.
  - Leer páginas en serie construyendo localmente cada ruta y no seguir URLs de
    response.
  - Probar drift de cada campo, cambios entre páginas, PR cerrada, identidad
    cruzada y ausencia de output parcial.

- [x] T5. Implementar canonicalización y filtering de archivos. [R3, R6, R7]
  - Validar paths current/previous, status, counts, duplicados, rename/delete,
    tipo textual y allowlists antes de materializar payload.
  - Clasificar binary/submodule/LFS, patch ausente y paths fuera de scope como
    omissions sin descargar contenido alternativo.
  - Probar traversal/absolute/device/backslash/colon, conteos inconsistentes,
    statuses desconocidos y links maliciosos en DTO/patch.

- [x] T6. Implementar el parser y packer bounded de unified diff. [R4, R6, R7]
  - Dividir patches autorizados en hunks/líneas completas y aplicar límites por
    file y globales sin cortar UTF-8, JSON o unidades semánticas.
  - Producir IDs/digests/referencias seguras y omission counts en orden estable.
  - Probar bordes exactos, oversized, diff malicioso, prompt injection, CRLF,
    UTF-8 inválido, patch ausente y determinismo repetido.

- [x] T7. Implementar extractores versionados de restricciones. [R4, R5, R6]
  - Añadir registry cerrado con `markdown_section/v1` y
    `catalog_reference/v1`, sin fallback ni parser/modelo libre.
  - Ligar referencia a catálogo/digest composition-owned y mantener constraints
    extraídas como `untrusted_data` con procedencia.
  - Probar sección/referencia válida, duplicada, anidada, desconocida,
    oversized, alias/version drift y texto imperativo fuera de la sección.

- [x] T8. Implementar selección de contratos OpenAPI afectados. [R6, R7, R8]
  - Comparar solo current/previous paths con routes confiables y emitir safe refs
    deduplicadas ligadas a contract ID/version/head/status.
  - Propagar partial/insufficient ante colección incompleta y representar
    rename/delete sin inventar contenido.
  - Probar match exacto/patrón aprobado, no-match, mención textual adversaria,
    varios paths por contrato, delete, rename y file/page limit.

- [x] T9. Añadir secret gate antes de toda frontera durable o model-facing. [R4, R9, R11]
  - Escanear metadata, constraints, paths y hunks bounded antes de store, ranking,
    observer o model; aplicar únicamente la policy explícita de redact/fail.
  - Mantener tokens/headers/capabilities dentro del adapter y excluir remote
    bodies de errores.
  - Probar canarios anidados en cada campo, detector cancelado/fallido y escaneo
    byte-for-byte de errors/events/journal/model request.

- [x] T10. Construir `Acquirer` con attempts y outcomes acotados. [R2, R6, R12, R13, R14]
  - Coordinar validación, intento coherente, normalization, extractors, selector,
    secret gate y seal bajo un único context/deadline.
  - Reintentar solo fallos read-only transitorios/rate-limited reiniciando la
    observación completa y respetando attempt/duration/wait budgets.
  - Probar 408/403/429/5xx/transport, Retry-After/reset, budget exhaustion,
    cancel cause/timeout en cada fase y cero trabajo propio vivo al retornar.

- [x] T11. Implementar idempotencia y persistencia content-free. [R3, R10, R13, R14]
  - Ligar key a run/PR/expected head/policy, confirmar context/binding por digest
    y rechazar redelivery conflictiva.
  - Mantener journal/receipts/records solo con IDs, digests, counts/reasons y
    proteger publicación/recovery ante respuesta perdida y contenders.
  - Probar reopen, same/different key, lost response, corrupción, CAS,
    adquisiciones concurrentes y ausencia de locks durante red/disco/timers.

- [x] T12. Adaptar el contexto GitHub a producers neutrales. [R3, R4, R6, R9, R11]
  - Convertir context validado en `SourceDiff` y constraints autorizadas con
    provenance, authority, completeness, safe refs y bindings verificables.
  - Actualizar field/profile policies mínimamente para metadata, hunks,
    omissions y contract refs; no entregar gateway/store/credentials/URLs.
  - Probar proyección mínima por planner/analyzer/diagnostic, orden/digest,
    partial/insufficient y ausencia de payload GitHub legacy.

- [x] T13. Integrar adquisición en admission y composición productiva. [R1, R9, R11, R13]
  - Resolver gateway, policy, extractor registry, catálogo, clock, store y
    observer exclusivamente en composición después de autoridad GitHub.
  - Mantener CLI/API/MCP sin capacidad de inventar PR identity y hacer que el
    service compartido produzca el mismo digest bajo la misma autoridad.
  - Probar vertical GitHub→acquire→assemble→plan y paridad interna de superficies
    con fakes, sin webhooks/publicaciones nuevas ni red real.

- [x] T14. Implementar binding y revalidación en todos los checkpoints. [R2, R8, R10]
  - Sellar repository/PR/head/base/observation/policy/catalog/extractor/producer
    y reconstruir authorities/producers actuales tras restart.
  - Revalidar antes de generate/regenerate, approval, execution y recovery;
    bloquear stale antes de nuevas model/tool calls.
  - Probar drift independiente de head, base, title, body, files, policy,
    catálogo, extractor y contract routes, además de no-drift derivado.

- [x] T15. Cerrar observabilidad, concurrencia y resistencia adversaria. [R4, R11, R12, R13, R14]
  - Emitir un terminal record exactamente una vez con allowlist content-free y
    observer timeout acotado.
  - Ejecutar matrices concurrentes/repeated/race para misma PR, PRs distintas,
    retries, cancellation, redelivery, CAS y restart sin sleeps de coordinación.
  - Probar que failures GitHub son operativos/insufficient, nunca verdict
    funcional, y que no quedan goroutines, timers, temporales o secretos.

- [x] T16. Completar trazabilidad y verificación multiplataforma. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15]
  - Registrar en `progress/github-pull-request-context/implementation.md` el mapa
    exacto R1–R15→tests y resultados, tasks, comandos, coverage y riesgos.
  - Ejecutar gofmt, comments audit, vet, compile-only, focales repetidos, suite
    global, race focal/global, runtime Linux y cross-builds aplicables.
  - Medir statements nuevos/modificados con build tags y demostrar coverage sin
    redondear >=85%; confirmar que `go test` no llamó a GitHub/red/modelo reales.
  - Auditar scope, temporales y comentarios de responsabilidad sistémica en toda
    entidad/constante/variable/función/método/helper nuevo o modificado.
