# Inspector estático de backends Spring Boot — Tasks

- [x] T1. Definir input sellado, build context, límites, resultado y errores. [R1, R8, R11, R13]
  - Escribir primero tests de repository/revision/source sets/release inválidos,
    aliases defensivos, enums cerrados, digests y errores seguros.
  - Modelar roots, changed paths, generated policy, framework identities,
    confianza/completitud, budgets, diagnostics y affected operations sin exponer
    paths del host ni AST al exterior.
  - Añadir comentario sistémico a cada entidad, función, método y helper nuevo o
    modificado, incluidos tests.

- [x] T2. Reutilizar una frontera de filesystem enraizada con garantías TOCTOU. [R1, R2, R11, R13]
  - Extraer un contrato neutral desde `goinspector` o implementar uno equivalente
    con tests compartidos de handles retenidos, no-follow, doble verificación y
    límites reservados antes de retener bytes.
  - Probar absolutos, `..`, casing/separadores, symlinks/reparse points, replacement
    races, source-set escapes y cancelación.
  - Añadir boundaries/spies que fallen ante filesystem exterior, process, shell,
    network, environment, credentials o host classpath.

- [x] T3. Implementar parser boundary, source-set index y resolución local. [R2, R5, R9, R10, R11, R13]
  - Probar packages, explicit/wildcard imports, nested types, duplicate/ambiguous
    symbols, records, enums, generics, syntax errors y Java release explícito.
  - Integrar un parser Java pasivo que reciba bytes y no disponga de callbacks de
    filesystem/build/classpath; traducir parser errors a categorías cerradas.
  - Construir índices locales y cierre estático con context checks, visited sets y
    límites; no leer build files/resources/bytecode como source.

- [x] T4. Extraer controllers y mappings Spring MVC soportados. [R3, R6, R8, R9, R11, R13]
  - Crear fixtures de `RestController` y casos demostrables de `Controller`,
    annotations FQCN/importadas, lookalikes, prefixes y aliases `value`/`path`.
  - Resolver `RequestMapping` y cinco mappings verbales, arrays/constants,
    consumes/produces y producto cartesiano bajo límite.
  - Rechazar mapping sin verb, placeholders, calls, custom composed annotations,
    view controllers y combinations dinámicas sin inventar operations.

- [x] T5. Derivar parámetros Spring de forma conservadora. [R4, R8, R9, R11, R13]
  - Probar `PathVariable`, `RequestParam`, `RequestHeader`, `RequestBody`, aliases,
    defaults, flags required, nombre léxico y mismatch con path.
  - Proyectar solo name/location/required/schema demostrables y ligar provenance a
    method parameter y annotation exactos.
  - Cubrir alias conflicts, values dinámicos, types externos/type-use no soportado
    y cancelación sin añadir claims parciales engañosos.

- [x] T6. Proyectar DTOs y schemas Java locales acotados. [R5, R8, R9, R10, R11, R13]
  - Probar primitives/wrappers/String, arrays, enums, records, nested DTOs,
    propiedades demostrables, Optional/List/Set/Map y substitutions genéricas.
  - Implementar visited/depth/node/byte budgets y reglas conservadoras de
    required/nullability; conservar closure no cambiado necesario.
  - Probar cycles, wildcards, unresolved type variables, external inheritance,
    Lombok, custom Jackson behavior y generated-source policy como partial/omitido.

- [x] T7. Derivar media types, `ResponseStatus` y `ResponseEntity`. [R6, R8, R9, R11, R13]
  - Probar consumes/produces efectivos, status class/method y constructores/builders
    ResponseEntity allowlisted con tipos/status constantes.
  - Implementar schema/status solo cuando sean demostrables; manejar branches,
    builder chains y return values dinámicos con completeness parcial.
  - Verificar que tipo Java no implica JSON/content type y que un view controller
    no se convierte en operación ejecutable.

- [x] T8. Conservar seguridad declarativa sin conceder autorización. [R7, R8, R9, R11, R13]
  - Probar `PreAuthorize`, `Secured`, `RolesAllowed` en clase/método, imports
    inequívocos, expressions/arrays constantes, duplicados y provenance exacta.
  - Proyectar constraints estructurales neutrales, bounded/untrusted, sin evaluar
    SpEL, override, AND/OR, role hierarchy o habilitación runtime.
  - Añadir tests negativos que demuestren que annotations no crean
    `SecurityRequirement`, tools, targets, credentials ni permisos de red.

- [x] T9. Implementar changed-path closure, impacto y diagnósticos parciales. [R8, R9, R10, R11, R13]
  - Probar controller↔DTO/constant/nested type en ambas direcciones, roots
    explícitos, source-set isolation y closure limit/ausencia.
  - Calcular affected operations sin omitir dependencias no cambiadas ni tratar
    changed-only como catálogo exhaustivo.
  - Deduplicar/ordenar/capar diagnostics y omissions; cubrir syntax errors,
    identidades no seguras, zero-safe-operation y canarios.

- [x] T10. Extender el catálogo neutral solo para constraints declarativos necesarios. [R7, R8, R9, R12, R13]
  - Escribir tests del modelo source-neutral, canonicalización, deep-copy, merge,
    projection/redaction y conflictos/provenance antes de modificarlo.
  - Representar expression/roles como datos estructurales no ejecutables y evitar
    tipos/nombres de AST Spring en APIs de planning.
  - Verificar que forbidden values se omiten por unidad completa y no aparecen en
    digests públicos, errors, diagnostics, model context o events.

- [x] T11. Adaptar `spring-static-v1` a `ContractSource` y registry. [R1, R12, R13]
  - Añadir contract tests de kind/version exactos, `ClassDerived`, expected digest,
    metadata trusted, limits/projection, deep-copy y concurrencia.
  - Rechazar mismatch antes de leer y volver a verificar el snapshot al retornar.
  - Probar merge compatible/conflictivo con OpenAPI/Go y policy
    allow/deny/review sin elevación de source content.

- [x] T12. Conectar composición autorizada y revalidación de workflow. [R2, R11, R12, R13]
  - Probar flujos local y PR con revision/head, source sets, roots, Java release y
    changed paths autorizados; no aceptar authority desde payload/model/source.
  - Sellar revision/source/catalog/policy digests y usar checkpoints existentes
    para detectar drift antes de contexto, aprobación y ejecución.
  - Demostrar que inspection/policy failure impide inference/tool calls y que el
    CLI/API/GitHub no introducen defaults del host ni rutas externas.

- [x] T13. Completar matriz, trazabilidad y revisión de seguridad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13]
  - Ejecutar fixtures completos Spring/Java, contractcatalog/sourcefs/goinspector
    regresions, validationworkflow, githubpr y entrypoints afectados, además de
    compile-only, `go vet`, repetición, race y coverage mínimo del 85 %.
  - Verificar boundaries estáticas y spies de cero Maven/Gradle/javac/process/
    processors/tests/app/network/downloads/classpath/credentials.
  - Crear `progress/spring-boot-backend-inspector/implementation.md` con mapa
    R→tests reales, comandos/resultados, coverage, límites y evidencia hermética.
  - Auditar todo el diff para comentarios sistémicos en entidades, constantes,
    funciones, métodos y helpers, incluidos tests; corregir omisiones.
  - Solicitar review independiente de lista cerrada, no fabricación, AST/type
    closure, DTOs, seguridad no ejecutable, sourcefs/TOCTOU, trusted metadata,
    revalidación y trazabilidad. El implementer no marca la feature `done`.

## Remediación de revisión independiente

- [x] REV-1 — Sellar root/framework identities y rechazar archivos fuera de source sets.
- [x] REV-2 — Rechazar candidatos ambiguos de imports, wildcard y FQCN duplicados.
- [x] REV-3 — Resolver aliases coherentes y concatenación acotada de constantes.
- [x] REV-4 — Resolver response body por método en controllers mixtos.
- [x] REV-5 — Aplicar regla DTO demostrable y wrappers por FQCN.
- [x] REV-6 — Proyectar cada branch retornado de ResponseEntity sin cruzar llamadas.
- [x] REV-7 — Propagar límites/cancelación con checkpoints internos.
- [x] REV-8 — Emitir diagnósticos seguros para schemas y seguridad omitidos.
- [x] REV-9 — Revalidar admission durable antes de aprobación y ejecución.
- [x] REV-10 — Componer lista explícita OpenAPI/Go/Spring en registry único.
- [x] REV-11 — Publicar repository/revision exactos en source y provenance.
