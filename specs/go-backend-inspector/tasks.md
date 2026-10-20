# Inspector estático de backends Go — Tasks

- [x] T1. Definir input sellado, límites, resultado y diagnósticos. [R1, R8, R10, R13]
  - Escribir primero tests de revisión/source metadata inválidas, copias defensivas,
    enums cerrados, canonicalización, digests y errores seguros.
  - Modelar repository snapshot, roots, build context, changed paths, auth matchers,
    límites, diagnostics, affected operations y completeness sin exponer paths del
    host ni tipos AST fuera del paquete.
  - Añadir comentarios sistémicos a cada entidad, función, método y helper nuevo o
    modificado, incluidos tests.

- [x] T2. Implementar la frontera de filesystem enraizada y hermética. [R1, R2, R10, R13]
  - Probar paths absolutos, `..`, separadores, casing, symlinks, reemplazo entre
    comprobación/lectura y límites de bytes/files con fixtures locales.
  - Implementar una capacidad read-only que solo devuelva paths relativos seguros
    y que no consulte environment, Git, module cache ni filesystem exterior.
  - Añadir spies que fallen ante procesos, red, credentials o lecturas fuera del
    snapshot; demostrar que directivas y binarios nunca se ejecutan.

- [x] T3. Construir índice local, constants, build selection y cierre estático. [R2, R4, R6, R9, R10, R11, R13]
  - Probar module path local, aliases de imports, constants, declarations/methods,
    build tags explícitos, imports ausentes, syntax errors, ciclos y cancelación.
  - Implementar parsing/AST/token y resolución local suficiente sin `go list`,
    `go/packages`, importer del host ni ejecución.
  - Recorrer roots/changed roots y todas las dependencias locales necesarias bajo
    límites; mantener resultado parcial seguro cuando una rama independiente falla.

- [x] T4. Extraer registros `net/http` soportados. [R3, R8, R9, R10, R13]
  - Crear fixtures de funciones globales, `ServeMux`, import aliases, constants,
    handlers identificador/método/literal y patterns Go 1.22.
  - Verificar que patterns legacy, host patterns, métodos/paths dinámicos y wrappers
    no soportados no crean operaciones y sí diagnósticos acotados.
  - Proyectar identity/provenance exactas en orden determinista.

- [x] T5. Extraer Chi v5 y composición estática de routers. [R4, R8, R9, R10, R11, R13]
  - Probar todos los verbs v1, `Method`, `Route`, `Group`, `Use`, `With`, builders,
    nesting, scopes e impacto de router/handler cambiado.
  - Implementar composición de prefix/path/middleware solo mediante flujo estático
    acíclico; incluir closure no cambiado necesario.
  - Cubrir Mount/Handle resolubles y branches/ciclos/receivers/paths dinámicos como
    parciales sin inventar registros.

- [x] T6. Derivar parámetros path/query de forma conservadora. [R5, R8, R9, R10, R13]
  - Probar `PathValue`, `chi.URLParam`, Query Get/Has/index, guards terminales,
    nombres dinámicos, mismatch con template y conversiones no soportadas.
  - Emitir solo name/location/schema/required demostrables y preservar provenance
    del registro y acceso relevante.
  - Verificar que una lectura sola nunca convierte query en required.

- [x] T7. Proyectar request bodies y schemas Go locales. [R6, R8, R9, R10, R13]
  - Probar decode JSON con structs/tags, aliases, escalares, slices/arrays, maps,
    pointers, nested types, ignored/unexported fields, cycles, generics, interfaces,
    custom unmarshalling y tipos externos ausentes.
  - Implementar proyección neutral bajo profundidad/nodos/bytes y reglas required
    conservadoras; no ejecutar validators, constructors ni marshalers.
  - Omitir unidades completas ante límites y degradar completeness con diagnóstico.

- [x] T8. Derivar respuestas explícitas y auth configurada. [R7, R8, R9, R10, R13]
  - Probar WriteHeader/http.Error/escritura/JSON Encode, status implícito 200,
    media type, control-flow divergente y responses dinámicas.
  - Probar scopes Chi de middleware y matchers exactos; asegurar que nombres,
    headers o middleware no allowlisted no producen security claims.
  - Proyectar status/schema/security únicamente cuando sean demostrables y marcar
    exhaustividad parcial cuando existan branches no resueltas.

- [x] T9. Finalizar diagnostics, completitud, impacto y snapshot canónico. [R8, R9, R10, R11, R12, R13]
  - Probar deduplicación/orden/caps, canarios, paths relativos/líneas/símbolos,
    equivalencia Windows/POSIX y permutaciones del filesystem.
  - Calcular completeness por claim/operation/source y affected keys sin elevar
    confidence ni ocultar closure estático.
  - Exigir insufficient/limit/cancelled cuando no sobrevive una identidad segura;
    sellar resultado/digest determinista sin estado parcial mutable.

- [x] T10. Integrar `DerivedSource` con `OperationCatalog` y policy. [R1, R12, R13]
  - Escribir tests contractuales del registry para kind `go-static-v1`, metadata
    trusted, class derived, expected digest, limits, deep-copy y concurrencia.
  - Añadir la metadata neutral mínima de diagnostics/impact si es necesaria,
    manteniendo `contractcatalog` independiente de Go y canónico.
  - Probar merge compatible/conflictivo con OpenAPI, policy allow/deny/review y
    que source content no puede elevar clase/confianza/completitud.

- [x] T11. Conectar composición autorizada y revalidación del workflow. [R2, R10, R12, R13]
  - Probar flujo local y PR con revisión/head exactos, roots y changed paths
    autorizados, y ausencia total de inferencia/tool calls cuando falla inspección
    o policy.
  - Sellar revision/source/catalog/policy digests en admisión y reutilizar la
    revalidación existente para detectar drift antes de aprobación/ejecución.
  - Verificar que changed-only conserva dependencias y que CLI/API/GitHub no
    aceptan paths, matchers o confianza desde input no autorizado.

- [x] T12. Completar regresiones, trazabilidad y revisión de seguridad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13]
  - Ejecutar fixtures completos de net/http/Chi y suites afectadas de
    `contractcatalog`, `validationworkflow`, `githubpr` y entrypoints, además de
    compile-only, `go vet`, repetición, race y coverage mínimo del 85 %.
  - Crear `progress/go-backend-inspector/implementation.md` con mapa R→tests
    reales, comandos/resultados, coverage, límites y evidencia de ausencia de
    proceso/red/credentials/lectura exterior.
  - Auditar todo el diff para comentarios sistémicos en entidades, constantes,
    funciones, métodos y helpers, incluidos fakes/spies/tests; corregir cualquier
    omisión antes de review.
  - Solicitar revisión independiente de patterns cerrados, no fabricación,
    hermeticidad, cierre PR, trusted metadata, policy fail-closed y trazabilidad;
    el implementer no marca la feature como `done`.



## Evidencia de novena corrección (REV-22/REV-23)

- [x] T11 revalidada: fencing durable atómico atraviesa restore, eventos, checkpoints, presupuesto e inferencia; stale generation se rechaza en store antes del modelo y dos coordinators reales multiproceso producen un único terminal.
- [x] T12 revalidada: cancel→finalize→release→join tiene un solo lifecycle acotado; gates multiproceso/race/repetición/coverage/global pasan con la única excepción histórica del symlink Windows sin privilegio.

## Evidencia de décima corrección (REV-24)

- [x] T11 revalidada: TTL, renovación, timeout de store, reloj y waiter son explícitos; la composición rechaza una envolvente sin margen para completar una operación y renovar.
- [x] T12 revalidada: el handoff prioritario evita starvation bajo presión determinista, conserva fencing generacional y rehidrata el mismo checkpoint desde otro handle; repetición, multiproceso, race y coverage pasan.

## Evidencia de undécima corrección (REV-25)

- [x] T11 revalidada: la composición rechaza sin overflow toda envolvente temporal no representable y exige que TTL sea estrictamente mayor que dos operaciones de store más una renovación.
- [x] T12 revalidada: límites exactos de `time.Duration`, propiedad/fuzz sin panic, repetición, multiproceso, race y coverage pasan; se conserva únicamente la excepción histórica de symlink Windows.

## Evidencia de duodécima corrección (REV-26)

- [x] T11 revalidada: finalización y recovery usan un timeout compuesto explícito, mayor o igual al envelope read/write+renew y menor que TTL; el heartbeat mantiene ownership durante cleanup tras cancelación.
- [x] T12 revalidada: barreras sin sleeps demuestran terminal por error, recovery cancelado, renovación con progreso y fencing stale; focal ×1000, combined estándar, correction, race y coverage pasan con solo la excepción histórica de symlink Windows.
