# Feature 28 — Validated project memory review

## Veredicto

`rejected`

La implementación no satisface todavía R2, R4, R5, R8, R10, R11, R12, R13 y R15. La suite existente pasa, pero varios tests trazados prueban únicamente wrappers del paquete y no las propiedades normativas. No se detectó alcance de feature 29 ni modificación de `feature_list.json`.

## Hallazgos

## REV-1 — El límite de scope y la procedencia confiable se pueden ampliar desde una propuesta

- Severidad: blocker
- Requirement o regla: R2, R4, R6
- Archivo y línea: `internal/projectmemory/service.go:321-342`; `internal/projectmemory/model.go:268-295`; `internal/projectmemory/model.go:351-367`
- Evidencia: `Propose` solo rechaza `project` cuando `MaxScope != project`; una autoridad limitada a `run` puede crear un candidato `revision`. Tampoco cruza `decision.MaxSensitivity` con la sensibilidad solicitada. `NormalizeRecord` copia `Candidate.Provenance` íntegra, incluido `Actor`, `Source`, `Producer`, `ObservedAt` y bindings aportados por el agente. La validación solo comprueba forma. Por tanto una propuesta puede declarar actor humano y fuente aparentemente confiable y esa procedencia se devuelve en `ReviewView`. El test de scope solo cubre `revision -> project` y el de poisoning solo cubre los campos DTO `RequestedState/RequestedAuthority`.
- Impacto: una salida del modelo puede ampliar el scope concedido y fabricar identidad/procedencia auditada, contradiciendo que todos sus campos sean no confiables y que la fuente se fuerce en servidor.
- Corrección requerida: modelar y aplicar un orden cerrado de scopes y sensibilidad en todas las creaciones; construir en el servicio la procedencia controlada (actor/source/producer/time/policy) o validarla contra una decisión confiable exacta; añadir matrices `run -> revision/project`, sensibilidad y actor/source/producer falsificados.

## REV-2 — Query trunca antes de ordenar y puede ocultar conflictos

- Severidad: blocker
- Requirement o regla: R11
- Archivo y línea: `internal/projectmemory/service.go:450-499`
- Evidencia: el bucle corta por bytes/items en líneas 472-483 mientras `Scan` entrega registros por ID; el orden normativo se aplica después, en líneas 485-493, y `memoryConflicts` recibe solo el subconjunto ya truncado. Con límite 1, `memory-a` con subject `z` se selecciona antes que `memory-z` con subject `a`, aunque el orden normativo exige lo contrario. Dos valores contradictorios pueden quedar a lados distintos del corte y devolverse sin `Conflict`. `TestMemoryQueryFiltersBeforeStableOrderingAndIndex` usa límite 10 y no ejercita esta interacción.
- Impacto: la paginación/truncación no es estable según el contrato y el sistema puede elegir de hecho un candidato de una clave contradictoria sin informar el conflicto.
- Corrección requerida: filtrar todos los candidatos dentro del budget, detectar conflictos y ordenar por la tupla normativa antes de truncar por records completos; añadir tests adversarios con orden de ID opuesto al normativo, límites 1/bytes y contradicciones que crucen el corte.

## REV-3 — No existe integración real con workflow ni con las cuatro superficies

- Severidad: blocker
- Requirement o regla: R12, R13; T11, T12, T13
- Archivo y línea: `internal/projectmemory/adapters.go:110-251`; ausencia de cambios en `internal/agentplanning`, `internal/functionalflow`, `internal/validationworkflow`, `cmd/featureproof` y adapters API/MCP/GitHub
- Evidencia: el diff productivo solo añade `internal/projectmemory`. `WorkflowGuard`, `ObservationAdapter` y `Surface` son wrappers aislados que ningún flujo existente compone o invoca. El test de “cuatro superficies” itera cuatro strings pero llama cuatro veces al mismo `Surface.Invoke`; no atraviesa CLI, API, MCP ni GitHub. Además `SurfaceRequest` transporta directamente `*QueryAuthority`, precisamente la autoridad raw que R13 prohíbe aceptar desde una superficie.
- Impacto: planning y ejecución no guardan ni revalidan bindings antes de aprobación/I/O, functional flow no produce observaciones, y ningún usuario puede ejecutar las operaciones de memoria desde las superficies declaradas. Un adapter futuro podría aceptar autoridad fabricada porque el contrato actual la incluye en el request.
- Corrección requerida: integrar puertos aditivos en los owners reales del workflow/planning/functional flow y en la composición; crear adapters reales por superficie con principal/authority resueltos desde configuración/autenticación; eliminar autoridad del DTO no confiable; probar verticalmente cada entrada real y el bloqueo antes de efectos.

## REV-4 — El digest funcional se descarta y la evidencia no se revalida para staleness

- Severidad: blocker
- Requirement o regla: R4, R5, R12
- Archivo y línea: `internal/projectmemory/adapters.go:235-251`; `internal/projectmemory/service.go:513-531`; `internal/projectmemory/service.go:652-676`
- Evidencia: `ObservationAdapter.Observe` comprueba únicamente que `FunctionalResultDigest` tenga formato y después lo descarta; no queda ligado al comando, procedencia, evidencia ni record digest. `eligible` y `Revalidate` no llaman a `EvidenceAuthority`, por lo que una evidencia revocada, ausente o corrupta después del commit sigue siendo elegible mientras tiempo/config/scope coincidan. Los tests usan una autoridad de evidencia que siempre permite y no prueban revocación posterior.
- Impacto: no se puede demostrar qué resultado funcional originó una observación y memoria basada en evidencia revocada continúa autorizando trabajo nuevo.
- Corrección requerida: sellar el digest del resultado terminal y su identidad verificada en lineage; revalidar la política/evidencia requerida al consultar y antes de aprobación/ejecución, con resultados stale tipados; añadir tests de digest distinto y revocación/ausencia/corrupción posterior.

## REV-5 — El journal acepta bytes no autenticados y puede perder silenciosamente el sufijo

- Severidad: blocker
- Requirement o regla: R8, R9
- Archivo y línea: `internal/projectmemory/localstore.go:409-482`; `internal/projectmemory/localstore.go:547-569`; `internal/projectmemory/projectmemory_test.go:892-921`
- Evidencia: `readJournal` decodifica un objeto por línea pero no exige EOF después del primer JSON, por lo que bytes u otro objeto añadidos tras un envelope válido no se autentican ni se rechazan. La detección de truncación depende de conservar un snapshot con head mayor. Si se trunca el journal y se elimina snapshot/index, `loadVerified` reconstruye y publica desde el prefijo válido sin ningún head externo/ancla durable que pruebe que faltan eventos. El test de truncación conserva deliberadamente el snapshot, por lo que no cubre el caso combinado.
- Impacto: manipulación o borrado coordinado puede hacer desaparecer versiones confirmadas y receipts; una redelivery puede ejecutar otra mutación creyendo que no existió la anterior.
- Corrección requerida: exigir consumo exacto de cada línea y diseñar un ancla autenticada no derivable/eliminable junto con las proyecciones para detectar truncación/deletion del tail; cubrir trailing JSON, truncación + borrado de ambas proyecciones, journal ausente tras commits y reinicio.

## REV-6 — Las defensas filesystem son comprobaciones por path vulnerables a TOCTOU y el replace de Windows no es atómico

- Severidad: blocker
- Requirement o regla: R8, R10
- Archivo y línea: `internal/projectmemory/localstore.go:362-406`; `internal/projectmemory/localstore.go:627-751`; `internal/projectmemory/file_identity_windows.go:11-27`; `internal/projectmemory/file_identity_unix.go:11-23`
- Evidencia: journal/locks/proyecciones se verifican con `Lstat`/`os.Open` y después se vuelven a abrir por pathname; no se mantienen handles de directorio ni se usa open-no-follow para impedir un swap entre check y use. `verifyFileIdentity` vuelve a abrir o hacer stat por path, por lo que tampoco liga la identidad verificada al handle usado. En Windows, si `Rename` falla se elimina primero el destino y luego se renombra, dejando una ventana no atómica. `verifyRegularFile` tampoco rechaza permisos inseguros de archivos existentes. Los tests crean links estáticos; no ejercitan root/component swap ni el boundary entre verificación y apertura.
- Impacto: un atacante local concurrente puede redirigir lecturas/escrituras fuera del root o observar proyecciones ausentes/parciales; el contrato fail-closed y la publicación atómica no están demostrados.
- Corrección requerida: implementar primitivas por plataforma basadas en handles/dirfd y no-follow con revalidación de identidad del handle efectivo; usar replace atómico real en Windows; validar permisos/ACL definidos; añadir harnesses deterministas de swap en cada boundary y runtime tests por plataforma.

## REV-7 — La revalidación no liga el scope exacto del binding

- Severidad: high
- Requirement o regla: R2, R5, R12
- Archivo y línea: `internal/projectmemory/service.go:502-531`
- Evidencia: `Revalidate` solo compara digest, project y configuration digest. No compara `RepositoryID`, `RevisionID`, `CommitOID` ni `QueryDigest` contra la autoridad actual. Si el binding no contiene entries, un binding de `repo-a` puede revalidarse como válido con autoridad de `repo-b` siempre que project/config/head coincidan; el loop que indirectamente verifica scope no se ejecuta. `Bind` también acepta un `QueryResult` arbitrario sin comprobar que pertenezca a esa autoridad.
- Impacto: un binding vacío o fabricado puede cruzar revisión/repositorio y superar el guard antes de una operación privilegiada.
- Corrección requerida: validar todos los campos sellados y recomputar/verificar el query digest y la selección contra la autoridad exacta; impedir `Bind` sobre resultados que no provengan de una query verificada; añadir casos de binding vacío y drift individual de repository/revision/commit/query.

## REV-8 — La trazabilidad de R15 afirma pruebas que no existen

- Severidad: blocker
- Requirement o regla: R15, CONV-009
- Archivo y línea: `progress/validated-project-memory/implementation.md` (tabla R1-R15); `internal/projectmemory/projectmemory_test.go:286-379`, `467-513`, `892-921`
- Evidencia: las pruebas citadas para R11 no prueban sort-before-limit/conflict-before-limit; las citadas para R12/R13 no atraviesan integraciones reales; las de R8/R10 no cubren truncation con proyecciones borradas ni swaps TOCTOU; las de R5 no prueban revocación de evidence. La suite verde no fallaría si esos comportamientos continuaran ausentes.
- Impacto: faltan tests concretos que demuestren varios acceptance criteria, lo que es bloqueante según `reviewer.md` y hace inválido marcar T11-T16 como cerradas.
- Corrección requerida: reabrir las tareas afectadas, añadir tests que fallen con la implementación actual y actualizar el informe solo con evidencia reproducible.

## Trazabilidad revisada

| Requirement | Estado | Evidencia |
|---|---|---|
| R1 | parcial | Contrato/canonicalización cubiertos; la frontera estricta no compensa procedencia server-owned ausente. |
| R2 | rechazado | REV-1 y REV-7. |
| R3 | parcial | Máquina cerrada básica; actor/authority source y expiración confiable no quedan ligados de forma suficiente. |
| R4 | rechazado | REV-1 y REV-4. |
| R5 | rechazado | REV-4 y REV-7. |
| R6 | rechazado | REV-1. |
| R7 | parcial | Secret gate y errores seguros tienen cobertura; debe repetirse tras integrar superficies reales. |
| R8 | rechazado | REV-5 y REV-6. |
| R9 | parcial | CAS/redelivery nominales pasan; truncación permite perder receipts. |
| R10 | rechazado | REV-6. |
| R11 | rechazado | REV-2. |
| R12 | rechazado | REV-3, REV-4 y REV-7. |
| R13 | rechazado | REV-3. |
| R14 | parcial | Cancelación/retry focal pasan; las integraciones y las reparaciones concurrentes no están cerradas. |
| R15 | rechazado | REV-8. |

## Gates ejecutados

- `gofmt -d internal/projectmemory`: pass, sin diff.
- `git diff --check 9c8aa34`: pass.
- `go vet ./internal/projectmemory`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=10`: pass.
- `go test ./...`: pass.
- `go test -race ./internal/projectmemory` con UCRT64: pass.
- Cross-compile tests `linux/amd64` y `darwin/amd64`: pass.
- Coverage host Windows: `87.3%` statements; el informe declara `837/984 = 85.0610%` al contabilizar Unix como no cubierto.
- `feature_list.json`: sin diff y feature 28 permanece `in_progress`.
- Alcance: no se encontró implementación de selección por rol, ranking semántico, prompt budget o context assembly de feature 29.

Los gates mecánicos verdes no cambian el rechazo porque los acceptance criteria anteriores carecen de implementación y de tests capaces de detectar su ausencia.

---

# Segunda revisión independiente

## Veredicto

`rejected`

REV-1, REV-2, REV-4 y REV-7 quedan corregidas en el núcleo. REV-3, REV-5, REV-6 y REV-8 siguen parciales. Se abren REV-9 a REV-12 por comportamientos que los nuevos tests no ejercitan.

## Estado de los hallazgos anteriores

| Finding | Estado | Evidencia |
|---|---|---|
| REV-1 | cerrado | `scopeRank`/`sensitivityRank` limitan create y `serverCandidate` sustituye provenance con actor, reloj y policy confiables. |
| REV-2 | cerrado | Query filtra, revalida evidence, ordena y calcula conflictos antes del corte por items/bytes. |
| REV-3 | parcial | Existen puertos y clases de transportes, pero no están conectados al workflow productivo; véase REV-9. |
| REV-4 | cerrado | El digest funcional queda ligado a provenance y Query/Revalidate revalidan EvidenceAuthority. |
| REV-5 | parcial | EOF exacto y anchor detectan ataques aislados; borrar journal y anchor converge a stream vacío. Véase REV-10. |
| REV-6 | parcial | Read/append usan handles finales no-follow y Windows tiene replace atómico; locks, proyecciones, anchor y temporales siguen usando paths sin identidad efectiva. Véase REV-11. |
| REV-7 | cerrado | Binding sella query/authority/selection y revalida todas las coordenadas, incluso selección vacía. |
| REV-8 | abierto | La nueva trazabilidad sigue declarando verticales productivas y matrices filesystem que no existen en composición o en todos los boundaries. |

## REV-9 — Los puertos de memoria no están compuestos en el workflow productivo

- Severidad: blocker
- Requirement o regla: R12, R13; T11, T12, T13
- Archivo y línea: `cmd/featureproof/workflow.go:590`; `cmd/featureproof/project_memory.go:61-106`; `internal/agentplanning/project_memory.go:9-43`; `internal/functionalflow/project_memory.go:9-25`; `internal/validationworkflow/model.go:154-166`
- Evidencia: `composeWorkflowCLIWithOptions` construye `validationworkflow.Dependencies` sin `Memory`; tampoco consulta memoria mediante `agentplanning.LoadValidatedMemory` ni instala `ObservationAdapter` en functional flow. La única composición de `projectmemory.Service` vive en el comando independiente `memory`. `projectMemoryEvidence.Verify` siempre devuelve error, de modo que esa composición no puede crear ninguna observation real. `StartCommand` transporta `MemoryBinding` y `MemoryAuthority`, pero ningún adapter productivo los deriva desde la query confiable; CLI/MCP/GitHub de workflow no los establecen. Los tests de owners llaman funciones o fakes directamente y no recorren la composición de `workflow.go`.
- Impacto: una validación real no consume memoria durante planning, no revalida un binding antes de aprobación/I/O y no publica observations terminales. Los nuevos tipos son integración potencial, no el flujo requerido por R12.
- Corrección requerida: componer un único servicio/authority de memoria dentro del workflow real, hacer que planning obtenga query+binding desde ese servicio, persistirlo sin aceptar `QueryAuthority` del caller, instalar el revalidator en `Dependencies.Memory` y conectar el package terminal a un EvidenceAuthority real y `ObservationAdapter`; añadir un test vertical desde el entrypoint workflow hasta planning, approval, execution y observation/restart.

## REV-10 — Borrar conjuntamente journal y anchor elimina historia confirmada sin detección

- Severidad: blocker
- Requirement o regla: R8, R9
- Archivo y línea: `internal/projectmemory/localstore.go:474-545`; `internal/projectmemory/localstore.go:548-601`; `internal/projectmemory/projectmemory_test.go:1538-1570`
- Evidencia: el anchor se guarda bajo el mismo root (`v1/anchors`). Si un proyecto confirmado pierde tanto `journal.log` como su anchor, `readJournal` recibe `os.ErrNotExist`, llama `verifyAnchor(..., nil)` y este devuelve éxito cuando anchor no existe y `len(events)==0` (líneas 568-569). El siguiente load/recovery publica snapshot/index vacíos. El test nuevo borra solo el anchor mientras conserva el journal, por lo que no detecta este caso. La key HMAC externa impide reseal, pero no permite distinguir “proyecto nuevo” de “stream borrado”.
- Impacto: desaparecen versiones y receipts confirmados; una redelivery puede volver a ejecutar una mutación y el historial deja de ser append-only/auditable tras restart.
- Corrección requerida: mantener fuera del root mutable una registry/ancla durable autenticada de streams creados y heads, o una autoridad equivalente que distinga proyecto inexistente de stream borrado; probar reinicio tras borrar `journal + anchor + snapshot + index`, borrado completo del directorio de proyecto y rollback coordinado.

## REV-11 — Locks, anchor y publicación derivada aún permiten path swaps y symlinks

- Severidad: blocker
- Requirement o regla: R8, R10
- Archivo y línea: `internal/projectmemory/localstore.go:275-320`; `internal/projectmemory/localstore.go:548-601`; `internal/projectmemory/localstore.go:635-688`; `internal/projectmemory/localstore.go:746-785`; `internal/projectmemory/store_lock_windows.go:13-41`; `internal/projectmemory/store_lock_unix.go:13-35`
- Evidencia: los locks siguen abriéndose con `os.OpenFile` sin no-follow ni verificación del handle; `verifyAnchor`, `verifySnapshot` e `verifyIndex` leen con `os.ReadFile`; `atomicJSON` crea el temporal y hace replace por path sin fijar/revalidar la identidad del directorio. Un swap del directorio después de `projectDir` y antes de `atomicJSON` puede dirigir el temporal/publicación a otro árbol; un symlink en `write.lock` se sigue. El único hook adversario nuevo cubre `read.before_open` del journal. En Windows `insecurePermissions` devuelve siempre false y las primitivas no validan DACL/ACL, aunque R10 exige rechazar permisos inseguros.
- Impacto: no está garantizado “cero escape” ni no-follow para todos los objetos enumerados por la spec; un atacante local concurrente puede causar I/O o locks fuera del root y la publicación no queda ligada al directorio validado.
- Corrección requerida: abrir lock/anchor/snapshot/index/temp y publicar mediante handles/dirfd del directorio validado, con no-follow y comparación de identidad antes/después; añadir validación ACL Windows acorde a la política; instrumentar swaps deterministas en lock, anchor read/write, snapshot/index read/write y replace, además de symlink/junction/hardlink en cada objeto.

## REV-12 — Las transiciones públicas permiten fabricar `observed` y la autoridad de auditoría es incorrecta

- Severidad: blocker
- Requirement o regla: R3, R4, R13
- Archivo y línea: `internal/projectmemory/service.go:404-459`; `internal/projectmemory/service.go:462-480`; `internal/projectmemory/service.go:688-701`; `cmd/featureproof/project_memory.go:25-31`; `cmd/featureproof/project_memory.go:53-58`; `internal/projectmemory/transports.go:105-125`
- Evidencia: la operación pública `memory.transition.v1` acepta cualquier `Target` permitido por `transitionAllowed`. Esta tabla permite `proposed -> observed`; `Transition` no verifica evidence y asigna `AuthorityHuman`, por lo que un caller administrativo puede crear un estado observed sin trusted observer ni evidencia. Tampoco exige `DecisionID` para approve/invalidate/supersede. `ReconcileExpiry` usa la misma transición y registra cada expiry como `AuthorityHuman`, aunque no hubo decisión humana. En composición, `Authorize` permite cualquier action no vacía y `ResolveGitHubPrincipal` acepta cualquier `actor.Action` no vacío; `GitHubAdapter.Submit` no liga ese action a la operación/target solicitados. Un actor validado para review puede enviar invalidate/supersede.
- Impacto: la máquina de estados no conserva la fuente real de autoridad y el mapping GitHub no es una allowlist de acción efectiva. Esto permite elevar una propuesta a observación o ejecutar una transición administrativa con una autorización destinada a otra acción.
- Corrección requerida: separar comandos/puertos de observer, human y trusted configuration; retirar `observed` de la transición pública; derivar authority/source por acción; exigir decision ID tipado donde aplica; registrar expiry con autoridad de reconciler/config, no humana; hacer que GitHub resolver reciba y compare operación+target con una allowlist exacta. Añadir matrices de cross-action y `proposed -> observed` sin evidence.

## Trazabilidad de segunda revisión

| Requirement | Estado |
|---|---|
| R1 | verificado en contrato/normalización; repetir sobre los DTO productivos finales. |
| R2 | verificado en service/binding. |
| R3 | rechazado por REV-12. |
| R4 | rechazado por REV-12; lineage funcional del núcleo sí fue corregido. |
| R5 | verificado en Query/Revalidate para evidence/config/time del núcleo. |
| R6 | verificado en proposal core. |
| R7 | verificado en gate y proyecciones focales; falta vertical productiva completa de REV-9. |
| R8 | rechazado por REV-10 y REV-11. |
| R9 | rechazado por REV-10. |
| R10 | rechazado por REV-11. |
| R11 | verificado; sort/conflicts preceden truncation. |
| R12 | rechazado por REV-9. |
| R13 | rechazado por REV-9 y REV-12. |
| R14 | gates de cancelación/race pasan; deben extenderse a los boundaries corregidos. |
| R15 | rechazado mientras falten los acceptance tests de REV-9 a REV-12. |

## Gates de segunda revisión

- `gofmt -d` sobre todos los archivos modificados: pass.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- Paquetes afectados ×3: pass.
- `go test ./...`: pass.
- `go test -race ./...` con UCRT64: pass.
- Cross-build de `internal/projectmemory` para `linux/amd64` y `darwin/amd64`: pass.
- Coverage focal Windows de `internal/projectmemory`: el implementer declara 89.0%; el cálculo multiplataforma declarado es `1181/1338 = 88.2661%`. El perfil agregado de paquetes afectados da 84.3% porque incluye fuentes históricas no pertenecientes al diff; no contradice el gate de diff, pero tampoco prueba por sí solo la cifra multiplataforma.
- `feature_list.json`: sin cambios; feature 28 continúa `in_progress`.
- Scope feature 29: no se encontraron role selection, semantic ranking, embeddings, prompt budgets ni context assembly.

Los gates mecánicos pasan. El rechazo se debe a cuatro propiedades normativas aún ausentes o no conectadas al producto.

---

# Tercera revisión independiente

## Veredicto

`rejected`

La remediación cierra la parte central de REV-3 (binding y checkpoints dentro del workflow), REV-5 para ataques que no alteran el trust root y buena parte de REV-6. REV-8 sigue abierto porque el informe presenta como verticales productivas pruebas que usan fakes o adapters no enroutados. REV-9, REV-11 y REV-12 siguen parciales; se abre REV-13 por rollback coordinado del registry y REV-14 por la autoridad de query derivada del propio request.

## Estado de hallazgos previos

| Finding | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-1 | cerrado | `scopeRank`, `sensitivityRank` y `serverCandidate` mantienen scope/sensitivity/provenance bajo control del servicio. |
| REV-2 | cerrado | Query filtra, verifica evidence, ordena y calcula conflictos antes del corte por items/bytes. |
| REV-3 | parcial | El workflow real ya obtiene y revalida un binding, pero las superficies de memoria API/MCP/GitHub continúan sin entrypoint productivo; REV-9. |
| REV-4 | cerrado | Functional result digest y evidence se sellan y Query/Revalidate vuelven a verificarlos. |
| REV-5 | parcial | EOF exacto y registry externo detectan tail deletion con registry actual; no detectan rollback coordinado a journal+registry antiguos válidos; REV-13. |
| REV-6 | parcial | Hay handles/dirfd y replace atómico, pero la apertura inicial sigue atravesando ancestros por pathname y el temporal se cierra antes del replace; REV-11. |
| REV-7 | cerrado | Binding vuelve a verificar proyecto, repo, revision/commit/config/query/head y selección exacta, incluida la vacía. |
| REV-8 | abierto | La trazabilidad productiva y adversaria excede lo que prueban los tests citados; REV-9, REV-11 y REV-13. |
| REV-9 | parcial | Existe composición workflow real, pero no integración productiva de las operaciones de memoria por API/MCP/GitHub. |
| REV-10 | cerrado bajo el trust root declarado | Borrar el mutable root conservando el registry se detecta y los receipts externos impiden reutilización. |
| REV-11 | parcial | Objetos finales usan no-follow y hardlink checks, pero quedan ventanas de ancestor/source replacement. |
| REV-12 | parcial | `observed` salió de la transición pública, DecisionID y GitHub action+target son exactos; observer/human/reconciler aún comparten una autoridad y actor. |

## REV-9 — Solo la CLI de memoria está enroutada en el producto

- Severidad: blocker
- Requirement o regla: R12, R13; T13
- Archivo y línea: `cmd/featureproof/main.go:103-114`; `cmd/featureproof/project_memory.go:157-198`; `internal/validationworkflow/functional_flow_test.go:1660-1785`
- Evidencia: `runWithContext` solo reconoce `memory <operation>` y llama a `composition.CLI`. `composeProjectMemory` construye campos `API`, `MCP` y `GitHub`, pero ningún handler, servidor MCP ni gateway GitHub productivo los referencia. La búsqueda en fuentes productivas devuelve usos de `composeProjectMemory` únicamente desde ese comando CLI y desde la composición interna del workflow. `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces` crea servicios y observers fake de `validationworkflow`; sus subtests no atraviesan `projectmemory.APIHandler`, `MCPAdapter`, `GitHubAdapter` ni `composeProjectMemory`. El test productivo de `workflow_identity_test.go:895` verifica un digest de binding y restart, no esas tres entradas de memoria.
- Impacto: T13 no entrega operaciones de memoria utilizables por API/MCP/GitHub y la supuesta paridad de principal, DTO, CAS, error e idempotencia no está probada en composición real. Construir adapters inertes no satisface el flujo end-to-end solicitado.
- Corrección requerida: enrutar explícitamente API y MCP de memoria, conectar el adapter GitHub al runtime/webhook con actor/repository/action verificados y demostrar que los cuatro entrypoints creados por composición comparten el mismo servicio/store. Los tests deben entrar por los entrypoints productivos, no por `Surface` o por fakes de workflow.

## REV-11 — El confinamiento aún depende de aperturas por pathname y conserva una ventana antes del replace

- Severidad: blocker
- Requirement o regla: R8, R10
- Archivo y línea: `internal/projectmemory/localstore.go:412-471`, `488-536`, `856-926`; `internal/projectmemory/directory_handle_unix.go:18-24`; `internal/projectmemory/directory_handle_windows.go:19-32`
- Evidencia: `projectDir` verifica la cadena por pathname y devuelve un string. Después `appendEvent`, `readJournal`, locks y publicación vuelven a abrir el directorio completo por pathname. En Unix `O_NOFOLLOW` protege solo el componente final; un swap de `v1` o `projects` por symlink entre `verifyPathChain` y `open(path)` hace que los ancestros se sigan. En Windows `FILE_FLAG_OPEN_REPARSE_POINT` también aplica al objeto final, no convierte toda la resolución de ancestros en handle-relative. `rejectCrossProjectReceipt` usa además `os.ReadDir(projectsRoot)` directamente. En `atomicJSON`, el temporal se cierra, se verifica por path y luego se hace `Renameat`/`MoveFileEx`; no se conserva/compara la identidad del source efectivo en el replace, por lo que existe un intervalo entre `replace_checked` y `Replace` en el que el nombre temporal puede sustituirse. Los hooks prueban swaps en los checkpoints anteriores, no esa intercalación final ni un swap de ancestro intermedio.
- Impacto: un actor local concurrente puede redirigir la apertura a un árbol fuera del root mediante un ancestro o sustituir el source publicado después del último check. Esto contradice el requisito de fallo cerrado ante TOCTOU, junction/symlink y root/component swaps.
- Corrección requerida: fijar el root y atravesar todos los componentes mediante handles/dirfd relativos con política no-follow/beneath; eliminar `os.ReadDir` por pathname en receipts; mantener identidad del temporal efectivo hasta la publicación y verificar el destino contra ese handle. Añadir probes deterministas de swap de `v1`/`projects` y de sustitución después del último check, en Windows y Unix.

## REV-12 — Observer, autoridad humana y reconciler siguen usando el mismo principal/actor

- Severidad: blocker
- Requirement o regla: R3, R4, R13
- Archivo y línea: `cmd/featureproof/project_memory.go:25-38`; `internal/projectmemory/service.go:409-500`
- Evidencia: `projectMemoryAuthority.Authorize` concede al mismo `principal` las acciones `propose`, `observe`, `approved`, `invalidated`, `superseded` y `expire`, y siempre devuelve `Actor{Kind:"authenticated_user", ID:principal}`. `Observe` convierte esa decisión en `deterministic_observation`; `ReconcileExpiry` cambia source/authority a reconciler/configuration, pero conserva ese actor humano. La separación actual es solo por método público: la autoridad no distingue un observer determinista de una decisión humana ni de configuración. El test nuevo comprueba target/DecisionID/source, pero no prueba que una credencial humana sea incapaz de autorizar `observe`/`expire` ni la compatibilidad del actor.
- Impacto: la auditoría afirma autoridad determinista/configurada con identidad humana y cualquier caller interno con el principal configurado obtiene las tres funciones de autoridad. No existe la allowlist separada requerida para trusted observer, human authority y trusted configuration authority.
- Corrección requerida: componer authorities/identidades distintas por rol y acción, o puertos que no compartan la credencial humana; validar actor kind contra la autoridad creada y probar cross-role denial (`human -> observe/expire`, `observer -> approve`, `reconciler -> human transitions`).

## REV-13 — Una copia antigua válida de journal y registry revierte historia sin detección

- Severidad: blocker
- Requirement o regla: R8, R9, R15
- Archivo y línea: `internal/projectmemory/localstore.go:647-711`; `internal/projectmemory/projectmemory_test.go:1733-1813`
- Evidencia: `durableAnchor` es un único JSON HMAC reemplazable. `verifyAnchor` acepta igualdad exacta entre el head reproducido y el head del registry, sin una generación monotónica fuera de ambos archivos; además, si el journal está por delante de un anchor antiguo cuyo digest es prefijo, lo trata como crash forward y vuelve a publicar el anchor. Por ello restaurar simultáneamente una copia antigua autenticada de `journal.log` y la copia contemporánea antigua del registry produce heads/receipts iguales y pasa tras restart. `TestExternalRegistryDetectsWholeMemoryRootDeletionAndJournalRollback` solo restaura `oldJournal` mientras deja el registry nuevo, por lo que su nombre y el informe no demuestran rollback coordinado. `TestIndependentAnchor...` restaura solo el anchor antiguo con el journal nuevo, caso que se auto-repara.
- Impacto: pueden desaparecer eventos y receipts confirmados sin conocer la key, y una idempotency identity posterior puede reutilizarse después del rollback. El journal deja de ser append-only verificable frente al ataque exigido.
- Corrección requerida: usar una autoridad monotónica/no reemplazable independiente (por ejemplo, ledger append-only protegido con un head externo monotónico) y probar restart tras restaurar juntos journal, projections y registry a un head anterior válido. Si escribir el trust root se declara fuera del threat model, requirements/tasks/informe deben decirlo explícitamente; hoy R8 exige detectar evento borrado/truncado y la trazabilidad afirma “rollback coordinado”.

## REV-14 — La allowlist de query se deriva de los filtros pedidos por el caller

- Severidad: high
- Requirement o regla: R5, R11, R13
- Archivo y línea: `cmd/featureproof/project_memory.go:41-50`
- Evidencia: `Resolve` devuelve `Classes: request.Classes`, `States: request.States` y `Authorities: request.Authorities`, fija `Review:true` y permite sensibilidad `restricted`. No hay allowlist independiente en configuración que se cruce con esos campos. Un principal autenticado convierte cualquier combinación pedida, incluidos `proposed`, estados terminales y cualquier authority, en la propia autoridad efectiva.
- Impacto: la frontera no demuestra que clases/estados/autoridades/sensibilidad pertenezcan a configuración confiable ni que una consulta operacional excluya propuestas/terminales. El request decide si es consulta de review.
- Corrección requerida: configurar allowlists y modo review por consumidor/principal fuera del payload, intersectar/rechazar el request contra ellas y añadir tests donde un caller operacional pide `proposed`, terminales, `restricted` o authorities no concedidas.

## Trazabilidad de tercera revisión

| Requirement | Estado |
|---|---|
| R1 | verificado. |
| R2 | verificado en núcleo; la autoridad productiva de query requiere REV-14. |
| R3 | rechazado por REV-12. |
| R4 | rechazado por REV-12; lineage y digest funcional del núcleo sí están cubiertos. |
| R5 | rechazado en composición por REV-14. |
| R6 | verificado en proposal core. |
| R7 | verificado en gates/core y proyecciones; repetir sobre entrypoints reales pendientes de REV-9. |
| R8 | rechazado por REV-11 y REV-13. |
| R9 | rechazado por REV-13. |
| R10 | rechazado por REV-11. |
| R11 | verificado en el servicio; rechazado en la derivación de authority productiva por REV-14. |
| R12 | parcial: binding/revalidation/observation están conectados al workflow; faltan garantías de autoridad de REV-12. |
| R13 | rechazado por REV-9, REV-12 y REV-14. |
| R14 | focales, cancelación y race pasan; no compensa las fronteras anteriores. |
| R15 | rechazado: tests/informe no prueban entrypoints productivos, ancestor/source swaps ni rollback coordinado. |

## Gates de tercera revisión

- `gofmt -l` sobre todos los Go modificados desde `9c8aa34`: pass, sin archivos listados.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=3`: pass.
- `go test ./internal/agentplanning ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof -count=2`: pass.
- `go test ./...`: pass.
- `go test -race` focal y `go test -race ./...` con UCRT64: pass.
- Cross-build `internal/projectmemory` para Windows, Linux y Darwin `amd64`: pass.
- El informe declara coverage completo del diff `1464/1705 = 85,865103%`, pero no incluye el comando/script que enumera statement blocks del diff y combina perfiles Windows/Linux; los comandos mostrados solo producen `pm-linux.out`. La cifra no es reproducible de forma independiente desde T16 y no corrige REV-8/R15.
- `feature_list.json`: sin diff. No se encontró scope de feature 29.
- `git status`: no mostró binarios, perfiles ni scripts temporales dentro del repositorio.

Los gates mecánicos pasan. El veredicto permanece rechazado por cuatro blockers de seguridad/integración y un finding high de autoridad de consulta.

---

# Cuarta revisión independiente

## Veredicto

`rejected`

REV-9, REV-11, REV-12 y REV-14 quedan cerrados tras revisar las nuevas composiciones, primitivas y matrices. REV-13 sigue abierto: el ledger solo detecta rollback mientras su fichero permanezca presente; si desaparece, el store lo reconstruye desde el journal revertido. Además, el gate versionado de coverage falla por debajo del mínimo contractual.

## Estado de los hallazgos priorizados

| Finding | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-9 | cerrado | `runWithContext` expone CLI/MCP/API; el runtime GitHub firmado instala `projectMemoryGitHubHandler`; los adapters de una composición comparten `Service` y store y los tests atraviesan HTTP, JSON-RPC y webhook reales. |
| REV-11 | cerrado | `openStoreDirectory` atraviesa componentes desde handles/dirfd no-follow, fija identidades de infraestructura/proyecto, enumera desde handle y conserva la identidad del temporal hasta verificar el destino publicado. Las matrices de ancestor y post-check swaps pasan en Windows y Linux. |
| REV-12 | cerrado | `HumanPrincipals`, `ObserverPrincipals` y `ReconcilerPrincipals` son puertos separados; `authorize` exige actor kind exacto y composición usa principals distintos. Observed/expiry no están en superficies humanas y GitHub limita su gramática a query/approve/invalidate. |
| REV-13 | abierto | El ledger detecta la restauración antigua probada cuando el ledger actual permanece, pero su ausencia se interpreta como ledger nuevo y se reconstruye; véase el probe reproducible siguiente. |
| REV-14 | cerrado | `projectMemoryQueryPolicy` fija classes/states/authorities/sensitivity/review en servidor y `Resolve` solo admite restricciones contenidas en esa política. |

## REV-13 — Borrar el ledger permite el rollback coordinado que debía impedir

- Severidad: blocker
- Requirement o regla: R8, R9, R15
- Archivo y línea: `internal/projectmemory/localstore.go:647-758`; `internal/projectmemory/projectmemory_test.go:1935-1978`
- Evidencia: `readMonotonic` devuelve un slice vacío cuando el fichero no existe. Después `verifyMonotonic` observa `len(entries) < len(events)` y llama a `advanceMonotonic`, que recrea todas las generaciones desde el journal que se está intentando autenticar. El test trazado restaura journal/snapshot/index/registry antiguos, pero deja intacto el ledger nuevo; no prueba deletion/missing del supuesto authority monotónico.
- Probe independiente: mediante el entrypoint productivo se confirmó `proposal(sequence=1)` y luego `approve(sequence=2)`. Se restauraron las cuatro copias antiguas de sequence 1, se movió fuera del root únicamente `monotonic/v1/ledger/cHJvamVjdC0x.log` y se redeliveró exactamente el approve. Resultado: exit code `0`, volvió a producir `Version:2/State:approved` y `LedgerRecreated:true`. El receipt de la segunda generación desapareció y fue reutilizable tras restart/composición nueva.
- Impacto: un borrado accidental o ataque al único fichero monotónico degrada silenciosamente la autoridad a la historia mutable revertida. HMAC impide fabricar entradas, pero no distingue “ledger nunca creado” de “ledger confirmado borrado”; la garantía append-only y los receipts no sobreviven el caso de pérdida que el ledger debía anclar.
- Corrección requerida: mantener un registro externo adicional de streams/última generación cuya ausencia sea error, o hacer que la autoridad monotónica sea un servicio/medio que no pueda recrearse desde el journal. Como mínimo, tras existir un stream, missing/truncated ledger debe fallar cerrado. Añadir restart con rollback de journal/projections/registry más ledger missing/truncated y comprobar que la idempotency identity posterior no se reutiliza.

## REV-15 — El gate versionado queda por debajo del 85% exacto

- Severidad: blocker
- Requirement o regla: R15, T16
- Archivo y línea: `scripts/feature28_coverage.py`; `progress/validated-project-memory/implementation.md:113-116`
- Evidencia: se generaron perfiles nuevos con los comandos documentados para Windows y se ejecutó el binario Linux instrumentado bajo WSL. `python scripts/feature28_coverage.py ...` terminó con exit code `1` y produjo `1857/2185 = 84.988558%`. El informe declara `1858/2185 = 85.034325%`, una diferencia de un statement que cambia el resultado del gate sin redondeo. Los unit tests del script sí pasan, pero no prueban el umbral sobre el diff real.
- Impacto: T16 exige cobertura statement-weighted completa del diff >=85% sin redondeo. El resultado reproducido incumple esa puerta y la trazabilidad publicada no coincide con los perfiles actuales.
- Corrección requerida: cubrir al menos el branch productivo faltante con una prueba de aceptación relevante, regenerar ambos perfiles y actualizar la cifra solo desde el gate versionado pasando. Evitar ajustar denominador/exclusiones para alcanzar el porcentaje.

## Trazabilidad de cuarta revisión

| Requirement | Estado |
|---|---|
| R1 | verificado. |
| R2 | verificado. |
| R3 | verificado con autoridades separadas y lifecycle cerrado. |
| R4 | verificado para provenance, evidence, decision y actor kinds. |
| R5 | verificado en Query/Revalidate y policy productiva. |
| R6 | verificado. |
| R7 | verificado en secret gate y cuatro entrypoints. |
| R8 | rechazado por REV-13. |
| R9 | rechazado por REV-13. |
| R10 | verificado por traversal handle-relative, identity y matrices Windows/Linux. |
| R11 | verificado en service y policy de composición. |
| R12 | verificado en binding/checkpoints/observation productivos. |
| R13 | verificado en los cuatro entrypoints y principals server-side. |
| R14 | verificado por límites/cancelación/race. |
| R15 | rechazado por REV-13 y REV-15. |

## Gates de cuarta revisión

- `gofmt -l` sobre el diff desde `9c8aa34`: pass, sin archivos listados.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=3`: pass.
- Paquetes focales afectados `-count=2`: pass.
- `go test ./...`: pass en ejecución serial de confirmación. Una primera ejecución paralela con otros tres gates produjo dos fallos temporales en paquetes históricos; no se reprodujeron ni en focales ni al repetir global de forma aislada.
- `go test -race ./...` con UCRT64: pass.
- Runtime Linux instrumentado de `internal/projectmemory`: pass, coverage focal 85.5%. Cross-build Darwin/amd64: pass.
- Unit tests de `scripts/feature28_coverage.py`: pass. Gate sobre perfiles recién generados: **fail**, `1857/2185 = 84.988558%`.
- `feature_list.json`: sin diff. No se encontró selección por rol, ranking, embeddings, prompt assembly ni otro scope de feature 29.
- Los perfiles, binarios y el probe se generaron únicamente bajo `C:\Users\asier\AppData\Local\Temp`; no aparecieron temporales nuevos dentro del repositorio.

Los entrypoints, las autoridades y el hardening de paths han mejorado y quedan verificados. La feature no puede aprobarse mientras el ledger missing permita rollback/receipt reuse y el gate exacto de cobertura siga por debajo de 85%.

# Quinta revisión independiente — cierre de REV-13 y REV-15

## Veredicto

`rejected`

La remediación cierra el caso exacto de ledger ausente cuando el manifest conserva la generación 2 y cierra el gate exacto de coverage. REV-13 continúa abierto porque el manifest y el ledger son dos archivos autenticados pero rollbackables dentro del mismo root: restaurarlos juntos a una generación antigua vuelve a autenticar como actual el journal y permite reutilizar receipts posteriores.

## REV-13 — El par manifest/ledger no aporta frescura frente a rollback coordinado

- Severidad: blocker
- Requirement o regla: R8, R9, R15; T6, T8, T15
- Archivo y línea: `internal/projectmemory/localstore.go:786-840`, `internal/projectmemory/localstore.go:891-1014`; `internal/projectmemory/projectmemory_test.go:1936-2202`
- Evidencia: `monotonicManifest` y cada `monotonicCheckpoint` están autenticados con HMAC, pero ambos se publican como ficheros reemplazables bajo el mismo `monotonicRoot`. `verifyMonotonic` solo contrasta `manifest.Generation`, el prefijo del ledger y el journal recibido. No existe una fuente de frescura fuera de ese conjunto que distinga el par válido actual de un par válido antiguo. El HMAC impide fabricar bytes, pero no impide reproducir bytes previamente firmados.
- Probe exacto pedido: `go test ./internal/projectmemory -run 'TestMonotonicLedgerRejectsCoordinatedOldJournalProjectionAndRegistryRollback|TestMonotonicAuthorityDistinguishesBootstrapDeletionCorruptionRollbackAndCrash' -count=3 -v` pasa. Confirma seq2 → restaurar journal/snapshot/index/registry de seq1 → borrar ledger → restart/redelivery: falla cerrado, no recrea ledger ni reutiliza receipt cuando el manifest de seq2 permanece.
- Probe independiente adversario: se copió el árbol actual fuera del repositorio a `%TEMP%`, se añadió únicamente allí `TestReviewerCoordinatedAuthorityRollback`, se confirmó seq2, se restauraron `journal`, `snapshot`, `index`, `registry`, `ledger` y `manifest` capturados en seq1, se reabrió el store y se redeliveró el mismo `manifest-second`. Resultado reproducido: `rollback accepted head=Sequence:1 and reused receipt as version=2 state=approved`; `go test` exit 1 por la expectativa fail-closed. El probe está en `C:\Users\asier\AppData\Local\Temp\fp-review28-r5-probe-334a2bb88e1d4e3f950dbca2e1da1b15`, fuera del workspace.
- Cobertura de bootstrap/crash: la matriz versionada sí cubre virgin bootstrap, manifest generation 0, corrupción, ausencia individual, ledger antiguo con manifest nuevo, manifest antiguo con ledger nuevo, borrado durante writer y recovery journal-ahead/ledger-ahead. No cubre rollback conjunto de manifest+ledger. Además, generation 0 no es una autoridad de head: restaurar ese marker junto con ledger ausente deja que `len(entries) < len(events)` alcance `advanceMonotonic` y regenere generaciones desde un journal antiguo.
- Impacto: quien pueda restaurar copias antiguas del trust root no necesita la key HMAC para ocultar generaciones confirmadas. El head posterior y su receipt dejan de existir para el proceso reiniciado; una redelivery vuelve a confirmar la transición. Esto rompe tail-deletion detection, restart convergence e idempotencia durable.
- Corrección requerida: anclar la última generación/receipt en una autoridad con monotonicidad real que no pueda retrocederse junto con estos archivos (servicio/counter append-only o medio con rollback protection), o declarar y hacer cumplir un boundary operativo que impida restaurar el conjunto completo. Añadir pruebas de rollback conjunto a generation 1 y de generation-0 manifest + ledger missing + journal antiguo; ambas deben fallar sin publicar ni reconstruir autoridad y sin reutilizar el receipt.

## REV-15 — Cerrado: coverage exacto supera el umbral

- Estado: closed
- Requirement o regla: R15, T16
- Evidencia: se regeneraron desde cero tres perfiles Windows y el binario Linux instrumentado. El runtime Linux pasó con `86.1%` focal. `python scripts/feature28_coverage.py --profile %TEMP%\feature28-review5-core.out --profile %TEMP%\feature28-review5-validation.out --profile %TEMP%\feature28-review5-cmd.out --profile %TEMP%\feature28-review5-linux.out` devolvió exit 0 y **`1941/2266 = 85.657546%`** sin redondear.
- El gate incluye los fuentes Windows y Unix del diff, une bloques por coordenada `(file,start,end,statements)` y usa el máximo count para no duplicar bloques comunes. `python -m unittest scripts/test_feature28_coverage.py` pasa.

## Regresión y trazabilidad

| Requirement | Resultado de quinta revisión |
|---|---|
| R1–R7 | Sin regresión observada; tests focales, globales y perfiles frescos pasan. |
| R8 | Rechazado por rollback conjunto de manifest+ledger en REV-13. |
| R9 | Rechazado: receipt posterior reutilizable después del rollback coordinado. |
| R10–R14 | Sin regresión observada; matrices versionadas y suite global pasan. |
| R15 | Coverage, comentarios y tests pasan; la matriz de durabilidad sigue incompleta para el caso reproducido de REV-13. |

T1–T16 permanecen marcadas `[x]`, la aprobación humana sigue presente y el informe mantiene mapa R1–R15. Los símbolos añadidos para la remediación monotónica tienen comentarios de responsabilidad sistémica. No se detectó código de ranking semántico, embeddings, selección por rol, prompt assembly ni presupuesto de contexto de feature 29. `feature_list.json` no tiene diff.

## Gates ejecutados

- `gofmt -l` sobre Go cambiado/nuevo desde `9c8aa34`: pass, sin nombres.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=3`: pass.
- `go test ./...`: pass.
- `go test -race ./internal/projectmemory -count=1` con UCRT64: pass.
- Linux/amd64 `internal/projectmemory` instrumentado ejecutado bajo WSL: pass; Darwin/amd64 cross-build: pass.
- Tests focales de manifest/ledger repetidos tres veces: pass.
- Unit tests del gate de coverage: pass.
- Gate fresco completo Windows+Linux: pass, `1941/2266 = 85.657546%`.
- El único fallo es el probe adversario externo descrito en REV-13, que demuestra aceptación de rollback y receipt reuse.
- Los perfiles/binarios y el probe viven bajo `%TEMP%`. Se retiró `scripts/__pycache__`; `git status` no muestra temporales nuevos. El estado restante corresponde al diff intencional de feature 28.

REV-15 queda cerrado. La feature continúa rechazada hasta que una autoridad externa no rollbackable permita distinguir la generación confirmada actual de una copia HMAC antigua del par manifest/ledger.

# Sexta revisión independiente — witness externo y cierre

## Veredicto

`rejected`

REV-13 queda cerrado: el nuevo `MonotonicWitness` está fuera de todos los ficheros administrados por `LocalStore`, no se reconstruye desde historia local y detecta el rollback coordinado completo. La feature sigue bloqueada por un defecto nuevo de recovery en R9: un fallo antes del advance deja un evento sincronizado en el journal y el stream no puede recuperarse ni mediante restart ni mediante redelivery idéntica.

## REV-13 — Cerrado: autoridad monotónica externa no replayable

- Estado: closed
- Requirement o regla: R8, R9
- Archivos: `internal/projectmemory/witness.go`, `internal/projectmemory/witness_http.go`, `internal/projectmemory/localstore.go:299-315`, `internal/projectmemory/localstore.go:754-803`, `cmd/featureproof/project_memory.go:25-26`, `cmd/featureproof/project_memory.go:359-389`
- Evidencia: `LocalStore` solo conoce el port `Read`/`Bootstrap`/`CompareAndAdvance`; ya no mantiene manifest ni ledger local. `verifyWitness` exige igualdad exacta de project, generation, head y digest de receipts y nunca avanza la autoridad durante lectura/recovery.
- Rollback reproducido: `TestExternalWitnessRejectsCoordinatedLocalRollback` ejecutado tres veces confirma seq2, restaura juntos journal/snapshot/index/registry de seq1 y reinicia con el witness en seq2. `Head` y redelivery fallan, el receipt no se reutiliza y el witness permanece en generación 2.
- Matriz reproducida: missing/deletion, unavailable, behind, ahead, replay de CAS, bootstrap create-once, conflicto concurrente, confirmación incompatible, pérdida de respuesta posterior al CAS y restart posterior al CAS pasan repetidamente.
- Composición HTTP: fuera de loopback se exige `https`; se rechazan credenciales ausentes y redirects; la petición usa bearer dedicado, context y timeout; la respuesta está limitada a 64 KiB y usa JSON estricto. Composición exige exactamente `FEATUREPROOF_MEMORY_WITNESS_GUARANTEE=linearizable-durable-nonreplayable-v1`, URL y credential, y falla cerrada si falta la autoridad. La garantía operativa del servicio sigue siendo un precondition confiable externo, documentado explícitamente.

## REV-16 — Un fallo antes de `CompareAndAdvance` bloquea permanentemente el stream

- Severidad: blocker
- Requirement o regla: R9; R15; T8; spec aprobada en `9c8aa34`
- Archivo y línea: `internal/projectmemory/localstore.go:281-288`, `internal/projectmemory/localstore.go:754-803`; `specs/validated-project-memory/requirements.md:256-272`; `specs/validated-project-memory/design.md:172-180`
- Evidencia: `Commit` ejecuta `appendEvent` y `file.Sync()` antes de llamar a `advanceWitness`. Si el witness está unavailable antes del advance, el método retorna con el evento siguiente ya duradero en `journal.log` y con el witness aún en la generación anterior. En cada restart, `readJournal` llama a `verifyWitness`; la desigualdad devuelve `integrity:store.witness:generation`. La misma redelivery ni siquiera alcanza CAS/idempotency porque la carga inicial falla.
- Probe independiente: en una copia del árbol actual bajo `%TEMP%`, `TestReviewerWitnessPreAdvanceRecovery` confirmó seq2, hizo unavailable el witness exactamente en `witness.before_advance`, restauró la disponibilidad, recompuso `LocalStore` y redeliveró el mismo transition/receipt. Resultado: `restart/redelivery did not converge; witness_generation=2 error=integrity:store.witness:generation`; `go test` exit 1. Probe: `C:\Users\asier\AppData\Local\Temp\fp-review28-r6-probe-1890af56e8c24b24af14fd8f97e1aa81`.
- Test versionado insuficiente: `before-advance-fails-closed` afirma únicamente que `Head` continúa fallando después de volver la autoridad; convierte el bloqueo permanente en expectativa. No demuestra recovery, retry ni convergencia.
- Contradicción de spec: R9 exige que writers/retries/restart converjan sin eventos perdidos/duplicados y que recovery observe el último boundary durable sin aceptar estado parcial. T8 exige recuperar journal confirmado con publicación incompleta. La modificación posterior a la aprobación en `design.md:177` añade “Un crash antes del advance deja el stream bloqueado”; esa decisión material no estaba en la spec aprobada y debilita el contrato en vez de implementarlo.
- Impacto: una indisponibilidad transitoria, cancelación o crash en una ventana normal inutiliza permanentemente toda la memoria del proyecto. El operador no puede leer el último head confirmado ni repetir la misma operación, aunque el witness vuelva sano; hace falta editar/truncar manualmente el journal, algo fuera del contrato y contrario a la autoridad append-only.
- Corrección requerida: introducir un protocolo recuperable que distinga datos staged de eventos confirmados. Por ejemplo, persistir el candidato en un staging autenticado, hacer CAS externo y conservar en el witness suficiente identidad/digest para que recovery finalice de forma determinista; si el witness sigue en el head anterior, descartar únicamente el staging no confirmado y permitir la redelivery. Añadir probes de proceso real para crash/cancel/unavailable antes del CAS y exigir que restart recupere el head confirmado y que la misma idempotency identity converja sin edición manual. Cualquier cambio material de la matriz de recovery requiere aprobación humana y no puede contradecir R9.

## Coverage y gates

- Perfiles frescos Windows: `feature28-review6-core.out`, `feature28-review6-validation.out`, `feature28-review6-cmd.out`.
- Se compiló el binario Linux/amd64 desde el source final y se transfirió/ejecutó bajo WSL; suite pass y focal Unix `86.8%`. No se reutilizó un perfil anterior ni hubo cambios productivos entre build y gate.
- `python scripts/feature28_coverage.py ...` pasó con **`1891/2199 = 85.993633%`**. Incluye fuentes build-tagged Windows/Unix y deduplica bloques comunes por coordenadas.
- `python -m unittest scripts/test_feature28_coverage.py`: pass.
- `gofmt -l` sobre el diff: pass.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=3`: pass.
- `go test ./...`: pass.
- `go test -race ./internal/projectmemory -count=1` con UCRT64: pass.
- Darwin/amd64 cross-build: pass.
- El único test fallido es el probe externo de recovery de REV-16, cuya expectativa expresa el contrato R9 ausente.

## Trazabilidad y alcance

| Requirement | Resultado |
|---|---|
| R1–R8 | Sin regresión observada; REV-13 cerrado. |
| R9 | Rechazado por REV-16: restart/redelivery no convergen tras pre-advance failure. |
| R10–R14 | Sin regresión observada en suite global/focal y gates anteriores revalidados. |
| R15 | Coverage, plataformas, comentarios y gates pasan; crash recovery de R9 no está cubierto con la expectativa correcta. |

T1–T16 figuran `[x]`, pero T8/T15 no pueden considerarse satisfechas mientras el pre-advance crash deje el stream bloqueado. Los nuevos tipos, funciones, métodos, variables de paquete y helpers del witness conservan comentarios de responsabilidad sistémica. `feature_list.json` no tiene diff. No se encontró implementación de ranking semántico, embeddings, selección por rol, prompt assembly ni presupuestos de contexto de feature 29. Los perfiles, binarios y probes están bajo `%TEMP%`; se retiró `scripts/__pycache__` y no quedaron temporales nuevos en el repositorio.

La autoridad no replayable resuelve el rollback, pero el protocolo entre journal y witness todavía no cumple recovery/idempotencia bajo el fallo anterior al CAS.

# Séptima revisión independiente — staging del witness y regresión completa

## Veredicto

`rejected`

REV-16 queda funcionalmente cerrado. El protocolo staged recupera correctamente los fallos anteriores y posteriores al CAS sin duplicar journal ni receipt. La implementación de producto y todos los gates mecánicos pasan. Queda un bloqueo de R15 y del proceso SDD: `design.md` todavía describe el protocolo rechazado que bloqueaba el stream, y `implementation.md` publica coverage no reproducible y nombres de tests que no existen.

## REV-16 — Cerrado: staging recuperable antes del CAS

- Estado: closed
- Requirement o regla: R8, R9, R10, R15; T8, T15
- Archivos: `internal/projectmemory/localstore.go:76-83`, `internal/projectmemory/localstore.go:223-315`, `internal/projectmemory/localstore.go:687-1019`, `internal/projectmemory/localstore.go:1266-1282`; `internal/projectmemory/witness.go:15-42`
- Orden verificado: `pending.json` se publica mediante temporal exclusivo, sync, replace y parent sync; el witness avanza por CAS con `CommitDigest`; solo después se hace append+sync del evento definitivo, registry/snapshot/index y cleanup sincronizado del pending.
- Probe original independiente: en una copia actual bajo `%TEMP%`, se inyectó indisponibilidad en `witness.before_advance`. El journal permaneció byte por byte idéntico; tras restaurar witness y recomponer, `Head` devolvió secuencia 2; el retry confirmó una sola versión 3; la redelivery devolvió esa misma versión; el journal quedó con exactamente tres eventos y no quedó `pending.json`. Pasó tres veces. Probe: `C:\Users\asier\AppData\Local\Temp\fp-review28-r7-probe-9c18abda627445f490a11012044cf3d0`.
- Matriz repetida tres veces: pre-CAS failure, response lost, post-CAS/pre-append, append parcial, post-append/pre-registry/snapshot/index, fallos durante recovery, pending missing/tampered/hardlink/symlink cuando el host lo permite, swaps before/after advance, coordenadas incompatibles, CAS concurrente y multiproceso. Todo pasa; symlink se omite en Windows únicamente por falta de privilegio y la misma matriz corre en Linux.
- Recovery: `Head`, `Recover` y la carga inicial de `Commit` comparten lock y `recoverPending`. El witness en `Expected` retira staging pre-CAS; el witness en `Next` exige el pending firmado exacto y finaliza el evento; journal parcial se reemplaza únicamente con el prefijo verificado más el evento cuyo digest está anclado.
- Confidencialidad/efectos: el witness recibe solo project, generation, head digest, receipts digest y commit digest. El payload y el envelope completo permanecen en staging local autenticado. Recovery opera store+witness y no vuelve a invocar EvidenceAuthority, modelo, herramientas ni otros efectos externos.

## REV-17 — Diseño y evidencia de cierre no corresponden al source final

- Severidad: blocker
- Requirement o regla: R15; `docs/verification.md` secciones 5, 8 y 9; proceso SDD
- Archivo y línea: `specs/validated-project-memory/design.md:172-180`; `progress/validated-project-memory/implementation.md:39`, `progress/validated-project-memory/implementation.md:160-166`; `scripts/feature28_coverage.py:69-87`
- Evidencia de diseño: el diseño actual aún afirma “Después de sincronizar el journal, el writer usa un compare-and-advance” y “Un crash antes del advance deja el stream bloqueado”. El source final hace exactamente lo contrario: staging → witness CAS → journal, y recupera el fallo pre-CAS. La documentación aprobada/modificada no describe la arquitectura entregada y conserva como decisión el incumplimiento que REV-16 acaba de corregir.
- Evidencia de coverage: `implementation.md` declara `2624/3068 = 85.528031%` mezclando perfiles Windows r7 con “bloques exclusivamente Unix” de una ejecución Linux anterior. El script no extrae solo bloques Unix ni vincula cada perfil a un hash de source; une por `(file,start,end,statements)`. Tras insertar staging en el archivo común `localstore.go`, las coordenadas del perfil Linux antiguo dejaron de coincidir con Windows y se contaron bloques comunes viejos y nuevos como unidades distintas. El denominador 3068 no se reproduce y los perfiles citados ya no están disponibles.
- Reproducción correcta: se regeneraron desde cero los tres perfiles Windows, se compiló `internal/projectmemory` Linux/amd64 desde el mismo source final, se transfirió y ejecutó bajo WSL y se usó su perfil nuevo. El gate devuelve **`2027/2359 = 85.926240%`**, exit 0; el focal Linux devuelve `86.8%`. Este es el denominador del diff actual y confirma que el producto sí supera 85%, pero contradice la evidencia publicada.
- Trazabilidad incorrecta: la tabla REV-16 cita `TestWitnessPreAdvanceFailureLeavesJournalUnchangedAndRetryConverges`, `TestWitnessPartialAppendRecovery`, `TestWitnessAfterAppendBeforeProjectionRecovery` y `TestWitnessPendingDeletionAndTamperFailClosed`; ninguno existe como test top-level. Los comportamientos reales son subtests de `TestWitnessCrashBoundariesAndConcurrentCAS` y deben citarse con su nombre jerárquico real.
- Impacto: un reviewer o CI no puede reproducir la cifra, distinguir perfiles stale ni seguir varias referencias R→test. `docs/verification.md` clasifica evidencia falsa/no reproducible como bloqueante. Además, el diseño induce a implementar o auditar el orden durable ya rechazado.
- Corrección requerida: actualizar `design.md` al protocolo final y someter cualquier cambio material de spec a la puerta humana correspondiente; corregir `implementation.md` con `2027/2359` generado desde perfiles frescos y nombres reales de tests/subtests; hacer que el gate reciba/verifique metadata o hashes de source/plataforma para rechazar perfiles stale, o ejecutarlo únicamente en un flujo hermético que genere todos los perfiles inmediatamente antes de medir.

## Gates y coverage

- Coverage fresco Windows+Linux final: **`2027/2359 = 85.926240%`**, pass. Linux focal: `86.8%`.
- `python -m unittest scripts/test_feature28_coverage.py` con bytecode deshabilitado: pass.
- `gofmt -l` sobre Go cambiado/nuevo: pass.
- `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass.
- `go test ./... -run '^$'`: pass.
- `go test ./internal/projectmemory -count=3`: pass.
- Focal de staging/witness/CAS/multiproceso `-count=3`: pass.
- `go test -race ./internal/projectmemory -count=1` con UCRT64: pass.
- Linux/amd64 runtime sobre source final: pass. Darwin/amd64 cross-build: pass.
- `go test ./... -count=1` en paralelo tuvo un fallo de temporización histórico en `validationworkflow/TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork/cancel`; el caso aislado pasó tres veces y `go test -p 1 ./... -count=1` pasó completo. No se relaciona con el diff productivo de staging.

## Trazabilidad y alcance

| Requirement | Resultado |
|---|---|
| R1–R14 | Comportamiento y regresión verificados; REV-13 y REV-16 cerrados. |
| R15 | Rechazado por diseño/evidencia no reproducibles en REV-17, aunque coverage real y gates técnicos pasan. |

T1–T16 están marcadas `[x]`, pero T16 no está cerrada con evidencia fiel al source. Los símbolos nuevos de staging, witness y helpers conservan comentarios sistémicos. `feature_list.json` no tiene diff. No se encontró ranking semántico, embeddings, selección por rol, prompt assembly, presupuesto de contexto ni otro alcance de feature 29. No quedó `__pycache__` ni otro temporal nuevo en el repositorio; perfiles, binarios y probes están bajo `%TEMP%`.

El código de REV-16 está listo, pero la feature no puede aprobarse con un diseño que describe el comportamiento anterior y una cifra de coverage obtenida mezclando perfiles de revisiones distintas.

# Octava revisión independiente — protocolo documentado y gate r8

## Veredicto

`rejected`

REV-16 y la parte documental de REV-17 quedan cerradas: el diseño ya describe el protocolo durable real y la trazabilidad cita los nombres existentes. El gate r8 todavía no demuestra que cada perfil se generó desde el source que declara: `--write-manifest` etiqueta a posteriori cualquier perfil con el hash del árbol actual y acepta incluso un perfil creado antes de cambiar esos fuentes.

## REV-17 — El manifiesto puede certificar perfiles stale a posteriori

- Severidad: blocker
- Requirement o regla: R15; T16; `docs/verification.md` sobre evidencia verídica y reproducible
- Archivo y línea: `scripts/feature28_coverage.py:83-101`, `scripts/feature28_coverage.py:236-238`; `scripts/test_feature28_coverage.py:39-132`; `progress/validated-project-memory/implementation.md:144-166`
- Evidencia: `write_profile_manifest` calcula un único `source_manifest_hash(base)` en el momento de escribir el manifiesto y copia ese valor en todas las entradas existentes. Nunca observa qué source estaba presente cuando cada `go test -coverprofile` o binario Linux produjo el perfil. La validación posterior comprueba fielmente el manifiesto recién escrito, no la procedencia del perfil.
- Probe independiente exacto: se creó `stale-linux.out` mientras `internal/x.go` contenía source A; después se cambió el source a B, se llamó `write_profile_manifest(...)` y a continuación `validate_profile_manifest(...)`. Resultado: exit 0 y `STALE_PROFILE_ACCEPTED`; el manifiesto adjudicó al perfil antiguo el SHA-256 de B. Es exactamente la mezcla stale que el gate debía impedir.
- Las seis unitarias pasan, pero no cubren ese orden. `test_manifest_rejects_source_hash_from_another_tree` cambia el source después de escribir el manifiesto; `test_manifest_rejects_shifted_profile_blocks` cambia el perfil después; `test_manifest_rejects_profiles_bound_to_mixed_sources` edita manualmente el manifiesto después. Ninguna genera un perfil bajo A y usa `--write-manifest` por primera vez bajo B.
- Los hashes global/per-profile, rechazo de tamper posterior, duplicados por contenido, bloques desplazados y deduplicación por coordenada funcionan. El defecto está en la creación no confiable de la supuesta prueba de procedencia.
- Impacto: se puede volver a ejecutar el comando publicado sobre tres perfiles Windows actuales y un perfil Linux de una revisión anterior; `--write-manifest` los declara del mismo source y el gate pierde la propiedad que debía corregir el denominador stale de la séptima revisión.
- Corrección requerida: el flujo confiable debe poseer la generación de cada perfil, registrar el hash del source antes de ejecutarla, comprobar que sigue idéntico después y emitir entonces su entrada; o ejecutar toda la matriz y medida de forma hermética. La operación de cierre no debe poder crear una atestación nueva para perfiles arbitrarios preexistentes. Añadir el probe A→perfil→B→primera escritura del manifiesto y exigir rechazo, además de mixed/stale entre perfiles generados en momentos distintos.

## REV-16 y documentación — Cerrados

- `specs/validated-project-memory/design.md` especifica staging autenticado y sincronizado → witness CAS con `CommitDigest` opaco → journal/registry/snapshot/index → cleanup sincronizado. Distingue recovery pre-CAS y post-CAS y ya no conserva el texto antiguo de stream bloqueado.
- `TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback` y `TestMemoryStoreMultiprocessCAS` pasan tres veces.
- La enumeración `-v` confirma los subtests citados: `before-advance-fails-closed`, `partial-append-recovers-from-staging`, `after-append-before-projection-recovers` y `missing-tampered-or-aliased-staging-fails-closed/{deleted,tampered,hardlink,symlink}`. En este host el caso symlink se omite por falta de privilegio; hardlink y el resto pasan.
- El informe usa ahora esos nombres jerárquicos reales y publica la cifra final coherente de la revisión anterior, `2027/2359 = 85.926240%`, en vez del denominador mezclado 3068.

## Coverage r8 reproducida

- Se regeneraron desde cero los perfiles Windows core, validation y cmd sobre el source actual; todos los comandos pasaron.
- El binario Linux instrumentado se compiló correctamente desde ese mismo árbol. El runner WSL no pudo iniciar ni un `echo` por `Wsl/Service/CreateInstance/CreateVm/HCS/ERROR_NO_SYSTEM_RESOURCES`, por lo que esta octava revisión no pudo producir el cuarto perfil fresco ni volver a medir independientemente `2027/2359`.
- Con los tres perfiles Windows, el gate falla de manera correcta porque omite los seis fuentes Unix build-tagged. La séptima revisión sí produjo en este mismo source final un perfil Linux fresco y midió `2027/2359`; no hay un cambio productivo posterior que explique otro denominador, pero el nuevo manifiesto r8 no puede aportar la procedencia que afirma por el blocker anterior.
- `python -m unittest scripts/test_feature28_coverage.py -v`: pass, 6 tests. El probe adversario adicional es el único fallo contractual.

## Regresión, alcance y gates

| Requirement | Resultado de octava revisión |
|---|---|
| R1–R14 | Sin regresión observada; focales REV-16 repetidos y suite global serial pasan. |
| R15 | Rechazado: la atestación r8 no vincula el momento de generación del perfil al source declarado. |

- T1–T16 siguen marcadas `[x]`; T16 no puede considerarse cerrada hasta que el gate rechace el probe stale real.
- `gofmt -d` sobre Go cambiado/nuevo: limpio. `git diff --check 9c8aa34`: pass.
- `go vet ./...`: pass. `go test ./... -run '^$'`: pass. `go test -p 1 ./... -count=1`: pass.
- Darwin/amd64 cross-build de `internal/projectmemory`: pass. Linux/amd64 cross-build instrumentado: pass; runtime no disponible por el error externo de recursos de WSL indicado arriba.
- Comentarios sistémicos, trazabilidad R1–R15 y nombres de tests revisados sin nueva regresión. `feature_list.json` no tiene diff y T1–T16 no introducen ranking, embeddings, selección por rol, prompt assembly ni otro comportamiento de feature 29.
- No apareció `__pycache__`, `.pyc`, perfil ni binario dentro del repositorio; los artefactos de esta revisión viven bajo `%TEMP%`.

El producto conserva el cierre funcional de REV-16. La feature sigue rechazada únicamente porque el gate r8 puede transformar evidencia stale en evidencia aparentemente vinculada al source actual.

# Novena revisión independiente — runners de coverage r9

## Veredicto

`rejected`

Se retiraron `--write-manifest` y toda función con ese nombre, y el caso honesto A→perfil→B falla al validar y al intentar repetir sobre el mismo destino. REV-17 sigue abierto porque `run` acepta un comando arbitrario: ese comando puede copiar un perfil stale a su temporal `{profile}` y el propio runner lo certifica con el source actual. Además, la llamada `runner_signature` es un SHA-256 público sin key y permite reemplazar cualquier sidecar y recalcular su supuesto sello.

## REV-17 — `run` sigue ofreciendo certificación posterior de evidencia arbitraria

- Severidad: blocker
- Requirement o regla: R15; T16; evidencia verídica y reproducible
- Archivo y línea: `scripts/feature28_coverage.py:100-145`, `scripts/feature28_coverage.py:209-265`, `scripts/feature28_coverage.py:344-374`; `scripts/feature28_linux_runner.sh:44-49`; `scripts/test_feature28_coverage.py:39-202`
- Evidencia del comando: `run_profile` exige que algún argumento contenga `{profile}`, pero ejecuta sin allowlist cualquier comando restante. El `command_sha256` queda registrado, aunque `validate` nunca lo compara con una receta autorizada. Por tanto, controlar solo la ruta de salida no controla cómo se obtuvieron sus bytes.
- Probe independiente: se creó `stale-from-A.out` bajo source A, se cambió el source a B y se invocó el runner bajo B con un comando Python que copiaba `stale-from-A.out` a `{profile}`. `run_profile` publicó perfil+sidecar y `validate_profile_attestations` los aceptó. Resultado exacto: `POSTHOC_COPY_CERTIFIED_AND_VALIDATED`.
- Este probe usa la API oficial y no necesita editar el sidecar. Es equivalente a recertificar un perfil viejo bajo otro nombre; la comprobación de perfiles duplicados solo lo detectaría si el original se entrega también en la misma ejecución.
- Evidencia del sello: `runner_signature` es `sha256(canonical_json)` sin HMAC, secret, key pública verificable ni autoridad externa. En un segundo probe, después del rechazo A→B esperado, se sustituyó `source_hash` en el sidecar por el hash de B y se recalculó mediante la función pública. La validación devolvió éxito: `REPLACED_SIDECAR_ACCEPTED`. El runner Linux construye el mismo hash público con `sha256sum`, por lo que tampoco aporta un boundary distinto.
- Las seis pruebas versionadas pasan. `test_runner_rejects_a_profile_after_source_changes_from_a_to_b` solo reintenta exactamente el destino ya ocupado; `test_profile_and_sidecar_replacement_are_rejected` edita el documento sin recalcular el checksum; `test_replay_mixed_and_duplicate_profiles_are_rejected` presenta simultáneamente las dos copias. Ninguna cubre copia stale hacia un destino nuevo ni reemplazo con checksum recomputado.
- Impacto: la cifra puede volver a mezclar tres perfiles actuales y uno Unix antiguo y presentarlos como una generación fresca r9. El nuevo formato mejora el camino honesto, pero aún no prueba procedencia ni cierra el incidente de la séptima revisión.
- Corrección requerida: el gate debe poseer recetas cerradas de generación, construir internamente los comandos y rechazar argumentos ejecutables arbitrarios; idealmente una sola operación hermética genera y mide toda la matriz. Para un runner remoto, vincular un challenge/attestation que el productor no pueda fabricar a posteriori o usar una autoridad de build/CI verificable. No llamar firma o sello a un digest público. Añadir los dos probes anteriores y exigir rechazo.

## Auditoría del flujo r9

- `run` y `build-linux` rechazan destinos existentes, calculan source pre/post, escriben a temporales y limpian en errores ordinarios. Fallo de comando, missing sidecar, drift durante comando y binary mismatch quedan cubiertos y pasan.
- La publicación de dos archivos es secuencial: Python publica sidecar y luego profile; shell mueve profile y luego sidecar. Un kill entre replaces deja media pareja que falla cerrada, pero bloquea el rerun hasta limpieza manual. La prueba de interrupción cubre exit no cero del child, no esos boundaries de publicación.
- `build-linux` liga source y hash del binario antes de publicar. El runner Linux comprueba el binario antes/después y liga profile, build-attestation hash y source recibido. El host recomputa source, binary y build-attestation. Esa cadena detecta sustituciones accidentales, pero el script del runner y sus afirmaciones no están autenticados y el checksum público es recreable.
- Replacement sin recalcular, missing, child failure, drift, binary mismatch, replay simultáneo, mixed declarado, duplicate simultáneo y deduplicación de common blocks pasan en las unitarias. Los dos casos adversarios anteriores permanecen aceptados.

## Coverage fresca y smoke

- Se regeneraron mediante `run` tres perfiles Windows frescos (core, validation y cmd) y sus sidecars; todos los comandos pasaron. `build-linux` produjo binario y attestation frescos desde el mismo source.
- WSL continúa sin poder iniciar ni `echo` por `Wsl/Service/CreateInstance/CreateVm/HCS/ERROR_NO_SYSTEM_RESOURCES`. No fue posible ejecutar el binario Linux ni reproducir de manera independiente `2027/2359` y el focal `86,8 %` en esta novena revisión. Con los tres perfiles Windows, `validate` rechaza correctamente la ausencia de los seis fuentes Unix.
- La octava/séptima revisión ya verificó `2027/2359 = 85.926240%` con source final y perfil Linux fresco; el problema actual no es el porcentaje matemático, sino que r9 todavía puede atribuir esa procedencia a bytes antiguos.
- `TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback` y `TestMemoryStoreMultiprocessCAS`: pass con `-count=3`.
- `python -m unittest scripts/test_feature28_coverage.py -v`: pass, 6 tests. `go vet ./...`, compile-only global, `gofmt -d` y `git diff --check 9c8aa34`: pass.
- T1–T16 siguen `[x]`, pero T16 continúa sin evidencia de procedencia cerrada. `feature_list.json` no tiene diff; no apareció `__pycache__`, `.pyc` ni artefacto de cobertura dentro del repositorio. Los outputs de la revisión están bajo `%TEMP%`.

REV-16 conserva su cierre y no se observó regresión productiva. REV-17 permanece bloqueante hasta que el flujo no pueda certificar un perfil stale por copia a un destino nuevo ni aceptar sidecars cuyo sello puede recalcular cualquiera.

# Décima revisión independiente — gate efímero cerrado

## Veredicto

`rejected`

La interfaz y las recetas cierran la recertificación posterior detectada en la novena revisión: solo existe `verify`, no se aceptan perfiles, rutas, comandos, manifests ni sidecars, y los artefactos externos stale no entran en la operación. REV-17 permanece abierto por un TOCTOU entre la última validación de hashes de perfiles y su reapertura para medir: un reemplazo en esa ventana se acepta y altera numerador y denominador.

## REV-17 — Los perfiles se reabren después de su última validación

- Severidad: blocker
- Requirement o regla: R15; T16; evidencia exacta y reproducible
- Archivo y línea: `scripts/feature28_coverage.py:326-352`; `scripts/test_feature28_coverage.py:102-123`
- Evidencia: `verify_gate` calcula `identities` en la línea 342 y comprueba los hashes en 345-346. Después ejecuta `source_hash` y finalmente `merge_profiles(profiles)` vuelve a abrir cada path en la línea 350. Los bytes medidos no son los bytes cuya identidad se validó.
- Probe independiente determinista: los generadores produjeron los cuatro perfiles privados; después de la comparación de hashes, el segundo `source_hash` sustituyó `core.out` por un perfil válido con 99 statements. El gate continuó y devolvió `POST_REVALIDATION_PROFILE_SWAP_ACCEPTED (102, 102)`. No hizo falta aportar ninguna opción CLI ni fichero externo al inicio.
- La prueba `test_profile_replacement_hook_fails_before_measurement` cambia el perfil mediante el hook anterior a la comparación y por eso pasa. No existe un boundary posterior a la comparación ni una prueba que cambie el fichero cuando `source_hash`/`changed_lines` separan esa comparación de `merge_profiles`.
- Impacto: un cambio concurrente, interrupción controlada o proceso con acceso al temp puede hacer que el porcentaje impreso proceda de bytes distintos a los revalidados. La propiedad solicitada “perfiles revalidados” no se cumple en la operación final.
- Corrección requerida: leer cada perfil una sola vez a memoria después del último boundary, comprobar su digest y parsear exactamente ese buffer; no reabrir paths después. Hacer la misma operación con el snapshot de fuentes/diff o repetir el source hash después de medir para detectar drift durante `changed_lines` y parsing. Añadir un hook post-validación/pre-parse que deba fallar o demostrar que el reemplazo ya no afecta los buffers medidos.

## Interfaz, recetas y lifecycle

- `parse_args` solo acepta `verify`. `run`, `verify --profile`, `verify --manifest` y `verify --command` devuelven exit 2. No quedan funciones ni opciones de manifest, sidecar o firma fuera de referencias históricas append-only en este review.
- Las tres recetas Windows están fijadas en código. Linux compila `internal/projectmemory` con GOOS/GOARCH/CGO cerrados, usa un directorio host aleatorio, valida el token remoto, crea el binario con noclobber, compara SHA-256 remoto/local antes y después de ejecutar y recupera únicamente el perfil de esa ruta aleatoria.
- El source hash se calcula antes y después de la generación completa. El gate falla si Linux/WSL falta y no admite fallback ni perfiles proporcionados por el caller.
- `finally` elimina el temp host incluso en error. El `finally` Linux intenta borrar binario/profile remoto; si WSL está caído, ese cleanup también falla y reemplaza la excepción original, aunque el temp host exterior sí se elimina. No quedaron directorios `feature28-coverage-*` después del fallo real de esta revisión.
- Los seis probes versionados pasan: sintaxis cerrada, stale externo ignorado, drift/interrupción con limpieza, reemplazo en el hook previo, binary swap previo a transferencia y deduplicación de common blocks. Falta el boundary TOCTOU reproducido arriba.

## Gate real, coverage y regresión

- `python scripts/feature28_coverage.py verify` generó los tres perfiles Windows y el binario Linux fresco. Falló cerrado al iniciar WSL por `Wsl/Service/CreateInstance/CreateVm/HCS/ERROR_NO_SYSTEM_RESOURCES`; Docker tampoco tenía engine disponible. Por ello esta revisión no pudo ejecutar Linux ni confirmar de forma independiente `2027/2359` y `86,8 %`.
- La limpieza host posterior al fallo pasó. La evidencia previa del mismo source midió `2027/2359 = 85.926240%`, pero el gate actual no puede aprobarse basándose en el informe ni mientras acepte el swap anterior.
- `python -m unittest scripts/test_feature28_coverage.py -v`: pass, 6 tests.
- Smoke REV-16 (`TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestMemoryStoreMultiprocessCAS`) pasa con `-count=3`.
- `go vet ./...`, compile-only global, `gofmt -d` y `git diff --check 9c8aa34`: pass.
- T1–T16 figuran `[x]`; T16 sigue bloqueada por la ventana de sustitución. `feature_list.json` permanece sin diff, no hay implementación de feature 29 y no quedaron temporales, perfiles, binarios, `__pycache__` ni `.pyc` en el repositorio.

La operación cerrada resuelve la entrada de evidencia stale externa, pero aún no mide necesariamente los mismos bytes que acaba de validar. REV-17 continúa bloqueante hasta eliminar esa reapertura/ventana.

# Undécima revisión independiente — snapshots inmutables y cierre

## Veredicto

`rejected`

El TOCTOU de profiles de la décima revisión queda cerrado: cada path se abre una sola vez, el parser y el merge consumen el mismo `ProfileBuffer` inmutable y el source se vuelve a sellar después de medir. El gate no puede aprobarse porque el script enviado al shell Linux contiene escapes literales que lo hacen sintácticamente inválido; con WSL operativo fallaría antes de calcular el hash o ejecutar el binario.

## REV-17 — El runner Linux generado no es shell válido

- Severidad: blocker
- Requirement o regla: R15; T16; gate multiplataforma reproducible
- Archivo y línea: `scripts/feature28_coverage.py:249-256`; `scripts/test_feature28_coverage.py:125-169`
- Evidencia: las strings Python generan literalmente `before=\$(...)`, `/proc/\$\$/fd/4`, `test "\$before"` y equivalentes. En el programa que recibe `sh -lc`, la barra escapa `$`; impide command substitution y expansión de PID/variables.
- Probe independiente: se interceptó únicamente `run_checked` para capturar exactamente `remote_script` después de un build simulado, sin modificar sus bytes, y se pasó a `C:\Program Files\Git\bin\bash.exe -n`. Resultado: exit 2, `syntax error near unexpected token '('`, línea `before=\$(sha256sum /proc/\$\$/fd/3 | awk '{print \$1}')`.
- La prueba `test_linux_binary_swap_after_single_load_cannot_change_transfer_bytes` no ejecuta ni analiza el script. Su fake afirma que contiene `"/proc/\\$\\$/fd/4"` y devuelve éxito, por lo que codifica los escapes incorrectos en vez de comprobar un runner real.
- Impacto: `python scripts/feature28_coverage.py verify` nunca puede completar el perfil Linux con este source, aunque WSL esté sano. Por tanto, `2027/2359` y `86,8 %` no son reproducibles mediante el comando versionado actual y la evidencia de undécima remediación no corresponde al gate entregado.
- Corrección requerida: construir shell válido (`before=$(...)`, `/proc/$$/fd/...`, `test "$before"`) sin escapes literales añadidos por Python; añadir como mínimo `sh -n` sobre el script exacto y un test de integración que lo ejecute con un binario/profile fixture o con el runner Linux real. Después ejecutar el `verify` completo desde cero y publicar su salida.

## REV-17 — TOCTOU anterior cerrado

- `read_once` abre un handle no-follow cuando la plataforma lo soporta, contrasta `fstat`/`lstat`, regularidad, link count, device/inode y size, aplica bounds y conserva los bytes leídos. `ProfileBuffer` lleva esos bytes y `merge_profiles` ya no recibe paths.
- El swap `profiles.after_load` solo cambia el pathname retirado del flujo; el resultado sigue usando el buffer original. El probe versionado pasa.
- Truncation/empty, oversize y duplicate bytes fallan antes del merge. Drift A→B y drift durante measure fallan y limpian el temp.
- El binario local se lee una vez a `binary_bytes`; un swap posterior de su pathname no cambia los bytes enviados. La intención del runner remoto de abrir tres FDs, retirar pathname y usar la misma identidad para hash/exec/hash es correcta, pero no llega a ejecutarse por el error de sintaxis anterior.
- El source se comprueba antes de generar, después de cargar y después de measure. El cambio post-load/pre-merge reproducido en la décima revisión ya no altera los bloques medidos.

## Gate fresco, regresión y alcance

- `python -m unittest scripts/test_feature28_coverage.py -v`: pass, 8 tests.
- El primer `python scripts/feature28_coverage.py verify` real terminó antes de Linux por un flake de timing en `validationworkflow/TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay/before-stage`; el focal aislado pasa tres veces. Aunque se superase ese flake, el runner Linux exacto falla `bash -n` como se demuestra arriba. En este host WSL además continúa sin recursos, por lo que no fue posible confirmar de forma independiente `2027/2359` ni `86,8 %`.
- El directorio host efímero se eliminó después del fallo; no quedaron `feature28-coverage-*`, profiles, binaries, `__pycache__` ni `.pyc` dentro del repositorio.
- Smoke REV-16 (`TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestMemoryStoreMultiprocessCAS`) pasa con `-count=3`.
- `go vet ./...`, compile-only global, `gofmt -d` y `git diff --check 9c8aa34`: pass.
- Diseño e informe describen correctamente los buffers/handles, pero su afirmación de gate fresco no es reproducible con el shell actual. T1–T16 están `[x]`; T16 sigue bloqueada. `feature_list.json` no tiene diff y no hay implementación de feature 29.

La carga inmutable cierra el hallazgo anterior. REV-17 permanece bloqueante únicamente porque el runner Linux versionado no puede ser interpretado por el shell que debe ejecutarlo.

# Duodécima revisión independiente — runner POSIX y cierre

## Veredicto

`rejected`

El error de sintaxis de la undécima revisión queda cerrado y el gate completo reproduce correctamente `2027/2359 = 85.926240%`; el focal Linux mide `86.8%`. REV-17 conserva una ventana de identidad remota: el runner abre el pathname del binario tres veces y no demuestra que los FDs usados para hash pre, ejecución y hash post pertenezcan al mismo inode.

## REV-17 — Hash y ejecución no están ligados a la misma identidad remota

- Severidad: blocker
- Requirement o regla: R15; T16; binary swap y “misma identidad/FD para hash+transfer/exec/hash”
- Archivo y línea: `scripts/feature28_coverage.py:208-228`; `scripts/test_feature28_coverage.py:125-174`
- Evidencia: `linux_runner_script()` ejecuta consecutivamente `exec 3< "$remote_binary"`, `exec 4< "$remote_binary"` y `exec 5< "$remote_binary"`. Cada redirección resuelve de nuevo el pathname. No duplica un FD ya abierto ni compara device/inode de los tres handles. Después borra el pathname, pero para entonces tres identidades distintas ya podrían estar abiertas.
- Impacto: un swap remoto entre esas aperturas puede dejar fd3 y fd5 apuntando al binario esperado mientras fd4 apunta a otro ejecutable. Los hashes pre/post pasan sobre fd3/fd5 y el código distinto se ejecuta por `/proc/$$/fd/4`, produciendo el profile que el gate acepta. El directorio 0700 bloquea otros usuarios, pero no otro proceso de la misma autoridad que ejecuta el gate; la prueba solicitada era precisamente resistir binary replacement.
- Cobertura de tests: `test_linux_binary_swap_after_single_load_cannot_change_transfer_bytes` reemplaza solo el pathname local después de cargar `binary_bytes`. Su fake comprueba stdin y devuelve un profile inventado; no sustituye el pathname remoto entre fd3/fd4/fd5 ni comprueba igualdad de identidad. La integración real prueba el camino sin carrera, no el boundary adversario.
- Corrección requerida: abrir el binario remoto una vez y duplicar ese descriptor (`exec 3< ...; exec 4<&3; exec 5<&3`) antes de unlink, o contrastar explícitamente dev/inode de todos los FDs y fallar antes de ejecutar. Añadir un probe que fuerce/reproduzca replacement entre aperturas; hash y ejecución deben consumir descriptores duplicados de la única identidad abierta.

## Runner POSIX, gate y coverage verificados

- `linux_runner_script()` devuelve bytes POSIX literales con `$(...)`, `/proc/$$/fd/...` y variables sin escapes intermedios. Los mismos bytes pasan `bash -n` y se entregan por stdin a `wsl ... sh -s -- ...`; los argumentos viajan separados del script.
- Transferencia del buffer local usa argv directos `mkdir`, `dd` y `chmod`; no vuelve a leer el pathname local después de `read_once`. El profile remoto se abre por FD y se elimina antes de enviarlo.
- Los diez probes versionados pasan, incluida la integración Linux real con el focal productivo.
- `python scripts/feature28_coverage.py verify`: pass fresco, **`2027/2359 = 85.926240%`**. Un probe adicional generó el profile Linux real y `go tool cover -func` confirmó **`86.8%`**.
- Tras ambos runs no quedaron directorios `feature28-coverage-*` en host ni `feature28-*` en `/tmp` de WSL.
- El TOCTOU de profiles permanece cerrado: swap post-load no cambia buffers; drift durante measure, truncation, oversize y duplicate fallan como corresponde.

## Regresión, documentación y alcance

- Smoke REV-16 (`TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestMemoryStoreMultiprocessCAS`) pasa con `-count=3`.
- `go vet ./...`, compile-only global, `gofmt -d` y `git diff --check 9c8aa34`: pass.
- Diseño e informe describen correctamente `sh -s`, buffers inmutables, source seals y cleanup. Su afirmación de una única identidad para los tres FDs todavía no está implementada.
- T1–T16 figuran `[x]`; T16 continúa bloqueada únicamente por la ventana remota anterior. `feature_list.json` no tiene diff, no hay implementación de feature 29 y no quedaron temporales, profiles, binaries, `__pycache__` ni `.pyc` en el repositorio.

La cifra de coverage y el runner POSIX ya son reproducibles. Falta ligar hash pre, ejecución y hash post a un único open remoto para cerrar REV-17.

# Decimotercera revisión independiente — identidad remota única y cierre

## Veredicto

`approved`

REV-17 queda cerrado. El runner abre el pathname remoto exactamente una vez, duplica fd4/fd5 desde fd3, retira el nombre y realiza hash pre, ejecución y hash post únicamente por `/proc/$$/fd/{3,4,5}`. El probe real sustituye el pathname después de duplicar los FDs y confirma que se ejecuta la identidad original. Los once probes, la integración Linux y el gate fresco completo pasan.

## REV-17 — Cerrado

- Estado: closed
- Requirement o regla: R15; T16
- Archivos: `scripts/feature28_coverage.py:208-295`; `scripts/test_feature28_coverage.py:194-339`
- Apertura/identidad: el script contiene una sola redirección `< "$remote_binary"`. `exec 4<&3` y `exec 5<&3` duplican la misma open file description; después `rm -- "$remote_binary"` elimina el pathname antes de consumirla.
- Consumo: SHA-256 pre usa `/proc/$$/fd/3`, la ejecución usa `/proc/$$/fd/4` y SHA-256 post usa `/proc/$$/fd/5`. No existe reapertura del nombre remoto entre esas operaciones.
- Probe adversario real: `test_remote_path_swap_after_single_open_executes_original_identity` pausa después de abrir/duplicar, reemplaza atómicamente el pathname por un ejecutable que produciría coverage distinta y reanuda. El runner devuelve el profile del ejecutable original y exit 0.
- Sintaxis/transporte: los bytes exactos pasan `bash -n` y llegan por stdin a `sh -s`; directorio/hash viajan como argv separados. El fake comprueba el buffer binario enviado por `dd`, los bytes exactos del runner y el output recuperado.
- Profiles/source: los cuatro profiles se cargan una sola vez por handles con identidad, link count, size y bounds; parser/merge consumen sus buffers inmutables. Source se sella antes de generar, después de cargar y después de measure.

## Evidencia de cierre

- `python -m unittest scripts/test_feature28_coverage.py -v`: pass, **11 tests**, incluida integración Linux real y swap remoto.
- `python scripts/feature28_coverage.py verify`: pass fresco, **`2027/2359 = 85.926240%`**.
- El perfil Linux real está incluido en ese resultado; la revisión anterior también midió el focal Linux en `86.8%` sobre el mismo source final.
- Smoke REV-16 (`TestWitnessCrashBoundariesAndConcurrentCAS`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestMemoryStoreMultiprocessCAS`) pasa con `-count=3`.
- `go vet ./...`, compile-only global, `gofmt -d` y `git diff --check 9c8aa34`: pass.
- No quedaron directorios `feature28-coverage-*` en host ni `feature28-*` en `/tmp` WSL. Tampoco aparecieron profiles, binaries, `__pycache__` o `.pyc` dentro del repositorio.

## Trazabilidad y alcance

- R1–R15 quedan cubiertos por la matriz acumulada de revisiones y sin regresión en el smoke final. REV-13, REV-16 y REV-17 están cerrados.
- T1–T16 figuran `[x]`; diseño e informe describen el protocolo final y los comandos reproducibles.
- `feature_list.json` permanece sin diff. No se introdujo selección por rol, ranking semántico, prompt assembly ni otro comportamiento de feature 29.

La feature 28 satisface el contrato aprobado y dispone de evidencia fresca, reproducible y vinculada al source final. No quedan blockers abiertos.
