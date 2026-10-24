# Ensamblado seguro de contexto para agentes — Design

## Resumen de la solución

Se añade un paquete cohesionado `internal/contextassembly` que recibe identidad
y autoridad selladas, consulta productores registrados de proyecciones seguras,
aplica filtros estructurales, separa las fronteras de confianza, empaqueta items
completos dentro de budgets y publica un `ContextSnapshot/v1` canónico.

El ensamblador no descubre fuentes ni interpreta texto para conceder autoridad.
La composición registra perfiles y productores. Cada productor transforma un
contrato existente —restricciones, contrato reducido, diff ya autorizado,
configuración aprobada, memoria validada, historial o evidencia verificada— en
items tipados con bindings comprobables. Un registro desconocido o sin autoridad
no entra en el conjunto candidato.

La selección tiene cuatro barreras ordenadas:

1. validación de identidad, integridad y binding de la fuente;
2. elegibilidad estructural según proyecto/revisión/scope/estado/autoridad/
   validez/sensibilidad/rol;
3. ranking opcional exclusivamente sobre IDs elegibles;
4. packing determinista por unidades completas con budgets de items, bytes y
   tokens.

El resultado mantiene tres secciones distintas: `trusted_instructions`,
`approved_configuration` y `untrusted_data`. La serialización para el runtime de
modelo conserva las instrucciones en `StructuredRequest.Instructions`; las dos
secciones de datos viajan como un envelope JSON tipado en `Untrusted`. Ningún
contenido de fuente puede cambiar de sección.

El snapshot se liga inicialmente a un `plan_id` estable. Cuando existe el plan
preparado, un `ContextBinding/v1` añade el digest exacto del plan sin crear un
ciclo entre ambos digests. El workflow revalida ese binding antes de approval y
de ejecución.

## Fuentes de contexto consultadas

- `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md`,
  `docs/architecture.md` y `docs/conventions.md`.
- Acceptance criteria de feature 29 en `feature_list.json`.
- `specs/validated-project-memory/*` y los contratos reales de
  `internal/projectmemory`: `AgentMemoryDescriptor`, `Conflict`,
  `MemoryBinding`, `QueryAuthority` y `Revalidate`.
- `specs/agent-plan-generation/*` e `internal/agentplanning`: request de
  generación, separación `Instructions`/`Untrusted`, descriptores no
  ejecutables para el modelo, prepared plan y provenance.
- `specs/validation-workflow-orchestration/*` e
  `internal/validationworkflow`: admission snapshot, artifacts, checkpoints de
  memoria, approval, ejecución, recovery y superficies compartidas.
- `specs/model-adapter/*` e `internal/model`: provider-neutral
  `StructuredRequest`, selección explícita provider/model y budgets de sesión.
- `specs/external-agent-interface/*`: proyecciones públicas cerradas y ausencia
  de paths/bodies/capabilities internas.
- `specs/evidence-package/*`, `internal/functionalflow` y package readers: los
  resultados funcionales proceden de packages verificados; el diagnóstico del
  agente no es evidencia.

No se ha consultado un contrato externo de proveedor. El ranker y token counter
son puertos propios; esta feature no fija un API vectorial ni un tokenizer de
terceros.

## Decisiones de contrato

### Roles y perfiles

`Role` es un enum cerrado: `planner`, `analyzer`, `diagnostic`. Un `RoleProfile`
versionado, aportado por composition authority, fija:

- source kinds obligatorios y opcionales;
- clases/campos permitidos por source kind;
- autoridades, estados y sensibilidad máximos;
- instrucciones y output protocol version;
- budgets de candidatos, trabajo, items, bytes y tokens;
- política explícita ante contradicción y parcialidad.

El caller puede pedir menos elementos o menor sensibilidad, pero no editar el
perfil ni aportar instrucciones. Un perfil no contiene secretos, handles
ejecutables o funciones serializables.

Proyección v1 prevista:

| Rol | Contenido mínimo | Exclusiones características |
|---|---|---|
| `planner` | restricciones, contrato reducido, configuración segura, memoria elegible y descriptores declarativos | evidence bodies, historial exhaustivo, diagnostics no relacionados |
| `analyzer` | plan ejecutado, assertions/facts y referencias de evidencia permitidas, configuración y memoria analítica elegible | tool handles, credentials, raw responses |
| `diagnostic` | errores estructurales, hechos acotados, omissions y fragmentos de historial permitidos | memoria completa, datos funcionales no relacionados, capacidades |

Los perfiles de analyzer y diagnostic quedan implementados y probados como
contrato reusable aunque la vertical productiva inicial que genera planes use
principalmente `planner`. No se inventan nuevos loops agénticos ni decisiones
del modelo.

### Taxonomía de fuentes y confianza

`SourceKind` es cerrado: `constraint`, `contract`, `diff`, `configuration`,
`memory`, `history`, `evidence`, `plan` y `diagnostic`. Cada item contiene:

- `version`, `id`, `kind`, `logical_key` y digest del contenido canónico;
- `project_id`, `repository_id`, scope y revision binding aplicables;
- `authority`, lifecycle/validity cuando aplique y `sensitivity`;
- provenance tipada con producer/version, source ID/digest y safe reference;
- payload JSON canónico ya proyectado y redactado;
- flags `complete|partial` y reason codes del productor.

La trust class no se deriva de `authority`:

- solo el `InstructionCatalog` de composición produce
  `trusted_instructions`;
- solo un `ConfigurationSource` autenticado produce
  `approved_configuration`;
- todos los demás items, incluida memoria approved y evidencia determinista,
  son `untrusted_data` respecto al protocolo del modelo.

La autoridad sigue siendo útil para filtrar y explicar procedencia, pero nunca
permite reclasificar texto como instrucción.

### Completitud, conflictos y omisiones

Cada producer devuelve `SourceProjection{Binding, Items, Conflicts,
Completeness}`. El assembler verifica el binding antes de mirar payloads. Los
conflictos son conjuntos ordenados de IDs sobre una `logical_key`; ningún score,
fecha o confidence elige un ganador.

`Omission` conserva únicamente `item_id`, `source_kind`, `reason_code` y digest
cuando sea seguro. Los reasons cerrados incluyen: `role`, `scope`, `revision`,
`state`, `authority`, `validity`, `sensitivity`, `integrity`, `secret`,
`conflict`, `duplicate`, `ranking`, `budget` y `source_partial`.

Un perfil distingue fuentes obligatorias de opcionales y define si un conflicto
no obligatorio puede omitirse. Si falta una fuente obligatoria, sus items
conflictivos son necesarios o el envelope mínimo no cabe, el resultado es
`insufficient_context`; no se produce un snapshot apto para inferencia.

### Budgets y tokenización

`AssemblyLimits` usa valores positivos y límites independientes:

- max source projections y candidate items;
- max structural work y ranking work;
- max selected items y canonical bytes;
- max model tokens, omissions y conflicts;
- duración máxima, acotada además por el deadline del caller.

El packing calcula bytes sobre el envelope canónico final, incluida metadata de
secciones. El `TokenCounter` se resuelve en composición para la pareja exacta
provider/model y cuenta esos mismos bytes model-facing. No se embebe una
estimación genérica en core. Si no puede confirmar el conteo, no se llama al
modelo.

Las instrucciones, labels de seguridad, esquema y configuración obligatoria se
reservan primero. Los items restantes se evalúan en orden estable y se añaden
solo si el snapshot completo continúa dentro de los tres budgets. Un item es
atómico; productores que necesiten granularidad deben emitir varios items
independientes antes del ensamblado.

### Ranking semántico

`SemanticRanker` recibe IDs elegibles, metadata estructural y una representación
segura acotada. Devuelve exactamente una lista de `{id, score}`; no devuelve
contenido. El assembler rechaza IDs desconocidos/duplicados, scores no finitos y
resultados incompletos cuando el contrato los exige.

La clave estable final es `(rank bucket/score desc, source priority,
logical_key, item_id, content_digest)`. Sin ranker, o ante un índice derivado
missing/stale reconstruible, se usa la parte estructural. Un fallo operativo del
ranker no cambia filtros ni autoridad; según el perfil produce fallback
determinista registrado o `insufficient_context`, nunca selección parcial
silenciosa. La implementación inicial puede usar solo ranker fake/no-op; no
requiere red ni embeddings reales.

### Snapshot y binding

`ContextSnapshot/v1` incluye:

- identity: role, run, project, repository, revision y `plan_id`;
- provider/model identifiers no secretos;
- profile, instructions, configuration y source binding digests;
- secciones tipadas con items completos;
- selection/omission/conflict manifests acotados;
- completeness y counts de items/bytes/tokens;
- límites efectivos, assembler version y digest canónico.

`ContextBinding/v1` es content-free e incluye snapshot digest, plan ID, prepared
plan digest, revision/configuration/profile/instruction digests, memory binding
digest cuando exista, source bindings ordenados y su propio digest.

El snapshot no contiene closures, clients, store handles, credentials,
authenticators, target secrets o capability executors. Un descriptor de
capability model-facing solo conserva nombre, clase, efectos, idempotencia y
schema/operation identity permitida. Los validadores reales continúan fuera del
modelo en `agentplanning` y `planning`.

## Componentes y responsabilidades

### `internal/contextassembly`

- **Model/canonicalizer:** enums cerrados, valores detached, JSON canónico,
  límites, digest y validación de snapshots/bindings.
- **ProfileCatalog:** devuelve un perfil sellado por role; el request solo lo
  puede estrechar.
- **InstructionCatalog:** resuelve instrucciones versionadas por role desde
  composición confiable.
- **SourceRegistry:** asocia cada source kind a un producer configurado; no
  permite producers aportados por el caller.
- **SourceProducer ports:** entregan proyecciones seguras y revalidan bindings
  sin exponer stores o gateways al assembler.
- **EligibilityEvaluator:** aplica filtros estructurales antes de acceder al
  ranker.
- **ConflictDetector:** une conflictos declarados y detectados sin resolverlos.
- **SemanticRanker port:** orden opcional de IDs ya elegibles.
- **TokenCounter port:** conteo confirmado para provider/model seleccionado.
- **SecretGate port:** confirma que todo payload/metadata es apto para el modelo
  antes de ranking/tokenización/publicación.
- **Assembler:** coordina una invocación secuencial y cancelable, construye el
  snapshot o insufficient context y emite observación terminal.
- **Binder/Revalidator:** liga snapshot con plan y verifica drift antes de fases
  privilegiadas.
- **ContextStore port:** commit/load por digest, idempotencia y journal de
  manifestos seguros; no es fuente de autoridad.
- **Observer/Clock:** telemetry content-free y tiempo determinista.

Toda entidad, constante, variable de paquete, función, método y helper nuevo o
modificado, incluidos tests, tendrá un comentario que explique su
responsabilidad dentro del sistema.

### Producers/adapters

- `constraint`: adapta `agentplanning.Constraint` ya validada en unidades por
  constraint.
- `contract`: adapta `contractcatalog.ReducedContext` en unidades estables sin
  reabrir el contrato ni acceder al filesystem.
- `configuration`: recibe una proyección aprobada y digest desde composición;
  no carga feature 36.
- `memory`: adapta `AgentMemoryDescriptor`, `Conflict` y `MemoryBinding` de
  feature 28 y delega revalidación al `projectmemory.Service`.
- `diff`: contrato neutral para una proyección autorizada. La feature 29 ofrece
  tipos/fake; feature 30 conectará GitHub.
- `history`, `evidence`, `plan`, `diagnostic`: adaptan únicamente views y
  packages verificados existentes; no resuelven evidence bodies.

Cada adapter conserva la autoridad del productor original y nunca inventa
binding, completitud o procedencia.

### Integración `agentplanning`

`GenerationRequest` mantiene constraints/capabilities/identities privados que
necesita la validación determinista, pero incorpora un snapshot `planner`
inmutable. `marshalModelInput` serializa solo el envelope model-facing del
snapshot; deja de reconstruir contexto desde los campos crudos.

La provenance del `PreparedPlan` incorpora el context digest además de los
catálogos deterministas usados para validarlo. El resultado preparado expone un
digest estable apto para `ContextBinding`, nunca un token de ejecución.

### Integración `validationworkflow`

`TrustedAdmissionBuilder` continúa verificando spec, contrato, revisión,
target y catálogo. Después de admission, un `ContextPort` construye/persiste el
snapshot planner usando esos valores y la selección de memoria obtenida por el
workflow. El artifact seguro registra context snapshot/binding handles y
digests.

El orden de checkpoints queda:

```text
trusted admission + memory selection
        -> assemble/persist planner snapshot
        -> generation from snapshot
        -> bind snapshot to prepared plan
        -> persist artifact/context binding
        -> revalidate context + contract + memory
        -> approval
        -> revalidate context + contract + memory
        -> execution
```

Recovery carga por digest, verifica canonical bytes y repite revalidación. CLI,
API, MCP y GitHub atraviesan el mismo `validationworkflow.Service`, por lo que
no montan contextos propios.

### Persistencia local

Un adapter local puede residir en `internal/contextassembly` y usar un root
configurado bajo `.featureproof/context/v1`. Publica snapshots seguros y
bindings mediante temporal exclusivo, sync, replace/rename atómico y parent
sync, con verificación de path/no-follow equivalente a los stores existentes.

El journal append-only contiene solo event ID, run/role, idempotency digest,
snapshot/binding digests, counts, outcome, previous digest y timestamps. El
payload del snapshot se conserva en un objeto direccionado por contenido,
separado del journal; nunca se persiste un prompt concatenado ni una request de
proveedor. La carga recalcula el digest y falla ante corrupción, path escape,
symlink/reparse/hardlink no autorizado o objeto parcial.

## Contratos internos propuestos

Los nombres son orientativos; el comportamiento anterior es normativo.

```text
Assembler.Assemble(ctx, AssemblyRequest, AssemblyAuthority)
  -> AssemblyResult{Snapshot | InsufficientContext}

AssemblyRequest
  {idempotency_key, run_id, plan_id, role, provider, model,
   project/repository/revision, requested_sources, narrower_limits}

AssemblyAuthority
  {profile_digest, configuration_digest, instruction_version,
   source_bindings, effective_limits, sensitivity, as_of}

SourceProducer.Project(ctx, SourceQuery, SourceAuthority)
  -> SourceProjection{binding, items, conflicts, completeness}
SourceProducer.Revalidate(ctx, SourceBinding, SourceAuthority)
  -> BindingStatus

SemanticRanker.Rank(ctx, RankRequest{eligible IDs, safe features, max_work})
  -> []Rank
TokenCounter.Count(ctx, provider, model, canonical_model_input) -> confirmed count

Binder.Bind(snapshot, prepared_plan_digest) -> ContextBinding
Revalidator.Revalidate(ctx, ContextBinding, AssemblyAuthority) -> BindingStatus

ContextStore.CommitSnapshot / LoadSnapshot
ContextStore.CommitBinding / LoadBinding
```

Los errores públicos contienen `category`, `stage`, `path` y causa estándar de
cancelación/timeout cuando aplique. Categorías: `invalid_input`,
`unauthorized`, `integrity`, `secret_rejected`, `insufficient_context`,
`limit_exceeded`, `stale_context`, `conflict`, `cancelled`, `timeout`,
`unavailable` e `internal`. Texto externo no se serializa ni se usa para lógica.

## Flujo principal

### Ensamblado planner

1. El workflow valida `StartCommand`, rechaza claims de contexto aportados por
   superficies y construye `AdmissionSnapshot` y selección de memoria.
2. Composition authority resuelve perfil, instrucciones, token counter y
   producers para provider/model/role exactos.
3. El assembler valida request/authority, reserva budgets y solicita cada source
   en orden canónico.
4. Verifica binding/integridad/completitud antes de inspeccionar items.
5. Secret gate valida metadata y payloads. Eligibility aplica todos los filtros
   estructurales y registra omisiones seguras.
6. Conflict detector separa grupos contradictorios. Si una fuente obligatoria no
   queda utilizable, devuelve insufficient context.
7. El ranker opcional ordena exclusivamente IDs elegibles; su salida se valida.
8. Packer reserva el envelope obligatorio y añade items completos mientras
   respeta items/bytes/tokens.
9. Se canoniza, calcula digest, confirma counts y publica snapshot + evento
   terminal idempotente.
10. `agentplanning` construye la model request desde el snapshot y valida la
    respuesta con sus catálogos privados existentes.
11. Binder sella el prepared plan digest y el workflow persiste artifact y
    binding antes de exponer approval.

### Analyzer y diagnostic

El flujo es el mismo con otro `RoleProfile` y productores aplicables. Cada
consumer recibe una copia detached del snapshot, nunca acceso al assembler,
store o producers. V1 prueba esas proyecciones mediante consumers controlados y
no añade nuevas decisiones de verdict, remediation o ejecución.

## Flujos de error

- **Fuente inválida/corrupta/no autorizada:** error operativo y cero rank/model
  calls; no se degrada a fuente opcional si el perfil la declara obligatoria.
- **Fuente opcional unavailable/partial:** omission tipada; completeness final
  refleja partial o insufficient según el perfil.
- **Conflicto:** se registran IDs; nunca winner implícito. Una contradicción
  requerida produce insufficient context.
- **Budget:** se omiten unidades completas. Si no caben guardias/fuentes
  obligatorias, no hay inferencia.
- **Ranker/índice:** output inválido se rechaza. Missing/stale derivado permite
  fallback solo si el perfil lo autoriza explícitamente.
- **Tokenizer:** ausencia/incompatibilidad/fallo impide inferencia; no se usa una
  aproximación local distinta.
- **Drift tras generación:** `stale_context` detiene approval/ejecución; se
  requiere un nuevo snapshot y plan, no una modificación del existente.
- **Store incierto tras commit:** retry con la misma idempotency identity consulta
  receipt/digest y converge; payload distinto es conflicto.

## Propiedad del estado, concurrencia y cancelación

Cada `Assemble` posee buffers, counters, candidates y manifests. Los catálogos y
registries son inmutables tras composición. No hay cache mutable global ni
goroutines lanzadas desde constructores.

La implementación v1 puede recorrer producers e items secuencialmente para
garantizar orden y work accounting. Si paraleliza lecturas, debe asignar owner,
join y orden canónico posterior; cancelación o primer error cancela y espera a
todas las operaciones propias. `context.Context` cruza producers, ranker,
tokenizer, secret gate y store.

El store serializa una misma idempotency identity mediante CAS/receipt. Runs y
roles distintos no comparten estado mutable. Snapshot y binding devueltos son
deep-copy o inmutables.

## Retries e idempotencia

Core no reintenta rank/model/tokenization automáticamente. Un producer puede
clasificar un fallo transitorio, pero el caller decide retry dentro del mismo
deadline y reutiliza request/digests originales. Tras publicar snapshot o
binding, redelivery retorna exactamente el objeto confirmado; una misma key con
otro digest falla.

El índice vectorial es reconstruible y no necesita receipt autoritativo. Su
rebuild nunca cambia un snapshot ya persistido.

## Observabilidad y seguridad

Eventos estructurados sugeridos:

- `context.assembly_started`;
- `context.assembly_completed|insufficient|failed`;
- `context.binding_committed`;
- `context.revalidation_valid|stale|failed`.

Campos permitidos: run ID, role, provider/model IDs seguros, profile/context/
binding digests, source counts, eligible/included/omitted/conflict counts,
bytes/tokens, duration, outcome/category y reason counts. No se registran
payloads, prompts, scores por item, safe references privadas, paths, credentials
ni textos de errores externos.

Secret gate se ejecuta antes de ranker/tokenizer/store/model. La serialización
mantiene datos externos dentro de JSON y no interpola delimitadores de prompt.
Los source refs son opacos/allowlisted y nunca se resuelven desde el modelo.

## Estrategia de pruebas

### Unitarias

- Validación estricta, deep-copy, canonización/digest y enums cerrados.
- Matrices de role profile, trust class, filtros, scope/revision, authority,
  state, validity, sensitivity y source binding.
- Separación de secciones ante prompt injection y autoridad de dominio alta.
- Conflict detection, completeness, omission reasons y stable order.
- Packing en bordes de item/bytes/tokens y token counter incompatible/fallido.
- Ranker ausente, ties, unknown/duplicate IDs, non-finite scores y max work.
- Secret/canary traversal en payloads, metadata, errors, index y manifests.

### Store, concurrencia y recovery

- Commit/load/reopen por digest; receipt/idempotency conflict; crash/fallo en
  write/sync/replace; objeto/journal corrupto/truncado/reordenado.
- Traversal, absolute/device names, symlink/junction/reparse/hardlink, root swap
  y temporales ajenos según plataforma.
- Cancelación en cada boundary, redelivery y ensamblados concurrentes bajo
  `-race`, sin goroutines o ficheros temporales filtrados.

### Integración vertical

- Workflow real: admission + memoria → snapshot planner → model fake → prepared
  plan → context binding → approval/execution revalidation → recovery.
- Drift de revision/config/profile/instructions/memory/evidence entre generación,
  approval y execution detiene trabajo.
- CLI/API/MCP/GitHub atraviesan el mismo service y producen el mismo context
  digest para la misma autoridad; ninguna acepta claims de contexto.
- Consumers fake de analyzer/diagnostic prueban proyecciones distintas y ausencia
  de capability handles/evidence bodies/memoria no permitida.
- Provider/model fake confirma que `Instructions` y `Untrusted` permanecen
  separados y que no existe payload legacy paralelo.

### Gates

- `gofmt` en todo Go modificado.
- `go vet ./...`.
- `go test ./...`, focal repetido y `go test -race ./...`.
- Runtime Linux y cross-builds Linux/Darwin/Windows para ficheros condicionados.
- Coverage statement-level del diff >=85% sin redondear.
- Auditoría R→test y auditoría de comentarios sistémicos en toda entidad,
  constante, variable, función, método y helper nuevo/modificado.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | canonicalizer, source registry | strict schema, mutation/digest, invalid binding |
| R2 | profile catalog, projections | three-role matrix, caller narrowing/widening |
| R3 | eligibility evaluator | all structural filters before rank spy |
| R4 | section builder, serializer | injection cannot cross trust sections |
| R5 | manifests | inclusion/omission provenance and canary tests |
| R6 | packer, token counter | exact item/byte/token boundaries and failures |
| R7 | semantic ranker adapter | optional/fallback/ties/adversarial output |
| R8 | conflict/completeness engine | contradictions, mandatory missing, partial |
| R9 | snapshot, binder, store | canonical digest and cross-run/plan tampering |
| R10 | revalidator, workflow checkpoints | all drift kinds before approval/execution/restart |
| R11 | observer, audit journal | exactly-once terminal telemetry, zero payload leak |
| R12 | planning/workflow/consumer adapters | product planner vertical and role isolation |
| R13 | assembler/store lifecycle | cancellation, CAS, redelivery, `-race` |
| R14 | secret gate, serializer | nested canaries and prompt injection corpus |
| R15 | verification pipeline | trace audit, diff coverage, comments, cross-platform gates |

## Paquetes afectados

```text
internal/contextassembly/   # nuevo dominio/aplicación, adapters de sources y store local
internal/agentplanning/     # consume snapshot planner y liga provenance
internal/validationworkflow/ # composición, persistencia y checkpoints de revalidación
internal/model/             # solo ajustes compatibles si necesita transportar el envelope tipado
```

`internal/projectmemory`, `contractcatalog`, `functionalflow` y evidence package
se consumen mediante adapters/puertos estrechos. No se cambia su autoridad ni se
les añade ensamblado de contexto.

## Alternativas y decisiones descartadas

- **Concatenar strings en `agentplanning`:** pierde trust labels, trazabilidad y
  budgets exactos.
- **Dar todo el contexto a todos los roles:** viola mínimo privilegio y hace
  imposible justificar una selección.
- **Rankear antes de filtrar:** expone contenido no autorizado al índice y deja
  que similitud actúe como autoridad.
- **Truncar strings o JSON:** puede alterar semántica, procedencia o delimitación;
  v1 omite items completos.
- **Estimar tokens en core:** sería incorrecto para provider/model distintos; se
  exige counter configurado y confirmado.
- **Convertir memoria approved en instrucciones:** confunde autoridad de dominio
  con autoridad sobre el modelo y permite memory poisoning.
- **Guardar el prompt completo para auditoría:** amplía superficie sensible; se
  guardan snapshot seguro, IDs, digests, counts y motivos.
- **Implementar GitHub context o environment acquisition aquí:** mezclaría las
  features 30/31 y nuevas fronteras externas con el ensamblador neutral.

## Conformidad arquitectónica

- `ARCH-001/002`: el snapshot no contiene herramientas ni acceso externo.
- `ARCH-003/004`: contexto y diagnóstico no sustituyen evidencia ni verdict.
- `ARCH-005`: sources, work, ranking, items, bytes, tokens y duración son finitos.
- `ARCH-006`: toda frontera bloqueable propaga contexto.
- `ARCH-007`: eventos/journal de contexto son append-only y content-free.
- `ARCH-008`: trust sections impiden convertir datos en instrucciones.
- `ARCH-009`: secret gate precede persistencia y modelo.
- `CONV-001..010`: nombres internos en inglés, puertos del consumidor,
  dependencias explícitas, errores tipados, tests deterministas, una sola feature
  y trazabilidad requirement-test.

No se solicita excepción a arquitectura o convenciones.
