# Flujo completo de planificación y validación — Tasks

- [x] T1. Definir el modelo de workflow, estados, outcomes, errores y contratos públicos. [R1, R4, R8, R13]
  - Escribir tests de cada transición válida/inválida y de la diferencia entre
    pre-admission error, phase no terminal y outcome terminal.
  - Modelar uniones cerradas para target/preview y missing-context/package.
  - Añadir comentarios sistémicos a todas las entidades, constantes, variables,
    funciones, métodos y helpers introducidos o modificados.

- [x] T2. Implementar preflight y snapshot de admisión sin efectos de modelo. [R1, R2, R11, R13]
  - Validar selections explícitas, specification, contrato, runtime profile,
    budgets, revision y exactamente target o preview.
  - Construir contexto reducido y descriptor-only tratando datos externos como
    no confiables.
  - Probar que toda entrada inválida falla antes de handle, journal, modelo,
    DNS, socket o tool y que las copias no permiten mutar el snapshot.

- [x] T3. Implementar el store local durable, idempotencia, CAS y leases. [R2, R9, R10, R13]
  - Reservar atómicamente principal/surface, request digest, key y handle.
  - Añadir journal append-only, checkpoints, claim/renew/release y recuperación
    de archivos incompletos/corruptos con locks entre procesos.
  - Probar start concurrente, conflicto, lease viva/expirada, fsync/rename
    fallidos y workflows independientes bajo race.

- [x] T4. Componer binding, target/preview, modelo y secretos en sus fronteras. [R2, R3, R11, R13]
  - Adaptar `revisionbinding` y `previewenv` sin duplicar attestation, target
    policy, reserve/readiness/release o retries.
  - Construir la sesión de modelo desde provider/model/profile explícitos y
    entregar al service solo ports y metadata segura.
  - Probar direct target, preview, drift, secretless, credenciales separadas y
    canarios que solo llegan al transporte autorizado.

- [x] T5. Integrar `agentplanning.Generator` y persistir `PlanArtifact/v1`. [R3, R4, R5, R13]
  - Confirmar el intento antes de inferencia y aceptar solo PreparedPlan o
    MissingContext validado.
  - Implementar codec canónico, digest, publicación atómica, carga íntegra y
    binding de procedencia sin guardar output/prompt/contexto crudo.
  - Probar plan válido, protocolo/semántica/policy/budget inválidos, missing
    context, crash de escritura, tampering y secret canaries.

- [x] T6. Integrar aprobación required y disabled autorizada. [R6, R13]
  - Stagear el artefacto exacto con `planapproval`, exponer la vista saneada y
    consumir un grant una sola vez.
  - Registrar por separado approval disabled permitido por policy, sin actor
    humano sintético.
  - Probar approve, reject terminal/replan, stale hashes, doble decisión,
    identidad no autorizada, append fallido y cero ejecución previa.

- [x] T7. Extraer y reutilizar el runner preloaded para planes generados. [R7, R8, R10, R13]
  - Mantener intacto `validate --plan` y hacer que ambos caminos usen la misma
    composición de registry, policy runtime, budgets, evidence, assertions,
    verdict y reporting.
  - Revalidar binding/preview/target/policy antes de tools y entre batches.
  - Probar passed, failed funcional, inconclusive, error, cancel y drift sin un
    executor alternativo.

- [x] T8. Unir package, plan artifact, finalización y cleanup durable. [R5, R7, R9, R10, R13]
  - Publicar en `plan.json` el mismo plan/digest aprobado y ejecutado, verificar
    package y proyectar resultados solo desde su reader.
  - Implementar intents idempotentes de finalización y release con deadline
    independiente acotado.
  - Probar package íntegro/corrupto/ausente, publicación incierta, release
    pendiente, cancel bundle y ausencia de resultado funcional fabricado.

- [x] T9. Añadir composición productiva y CLI `workflow` manteniendo la CLI histórica. [R1, R11, R12, R13]
  - Implementar `start|status|view|approve|reject|cancel|result` con argumentos
    exactos, códigos y stdout/stderr seguros.
  - Resolver auth profile y credenciales únicamente en `cmd/featureproof`, con
    composition reference protegida para recovery.
  - Añadir tests de parser/comando y regresión completa de `validate --plan`.

- [x] T10. Exponer el contrato workflow por API/MCP y adaptar GitHub. [R4, R6, R8, R12, R13]
  - Versionar DTOs/capabilities MCP y ejecutar fixtures equivalentes contra API,
    CLI y MCP con reautorización por operación.
  - Hacer que GitHub use una selección de instalación completa, publique
    awaiting approval y nunca interprete el comentario de inicio como approve.
  - Probar aislamiento de principals, aliases no autorizados, paridad, webhook
    idempotente y resultados/checks seguros.

- [x] T11. Implementar recovery supervisor y cancelación por todas las fases. [R9, R10, R13]
  - Reconstruir el prefijo confirmado y aplicar la matriz de recovery sin
    repetir inferencia o tool incierta.
  - Persistir cancel antes de señalizar, detener trabajo nuevo, liberar preview
    y hacer join de todos los workers.
  - Probar crash/cancel en cada checkpoint con reloj, waiter y barreras, sin
    `time.Sleep`, incluidas carreras entre recovery, approve y cancel.

- [x] T12. Completar trazabilidad, seguridad y verificación canónica. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13]
  - Crear el mapa R→test y registrar comandos/coverage en
    `progress/validation-workflow-orchestration/implementation.md`.
  - Añadir un test AST que cubra comentarios sistémicos de símbolos exportados
    e internos, `const`, `var`, métodos, funciones y helpers de test modificados.
  - Ejecutar gofmt check, `go vet ./...`, compilación, tests ordinarios,
    contract/integration, race repetido, coverage >=85% de paquetes afectados,
    builds aplicables y `git diff --check`, documentando únicamente limitaciones
    ambientales reproducibles.
