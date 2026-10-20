# Inspector estático de backends Spring Boot — Design

## Resumen de la solución

Se añadirá `internal/springinspector`, un paquete cohesionado que recibe un input
sellado sobre un snapshot Java autorizado y devuelve un
`contractcatalog.SourceSnapshot` de clase `derived`. La composición es la única
dueña de repository/revision, source sets, roots, build context, confianza,
completitud, límites y projection policy. El contenido inspeccionado no puede
modificar esa autoridad.

El inspector opera sobre bytes ya leídos por una capacidad de filesystem
enraizada. Un parser Java pasivo produce un AST sin resolver classpath ni iniciar
procesos. Sobre el AST se construyen índices locales de packages, imports,
declaraciones, constants y referencias. Extractores cerrados reconocen solo los
patrones v1; todo lo demás genera diagnostics y degrada completitud.

La salida entra por `contractcatalog.ContractSource`, se combina mediante las
reglas deterministas existentes y atraviesa la misma sufficiency policy y los
bindings de revisión/catálogo/policy del workflow. No se crea una ruta paralela
de planificación o ejecución.

## Evidencia y contexto consultados

- `AGENTS.md`, `docs/specs.md`, `docs/architecture.md` y
  `docs/conventions.md`: SDD, fail-closed, autoridad separada de datos, límites,
  cancelación, comentarios sistémicos y trazabilidad.
- `specs/contract-source-abstraction/*` e `internal/contractcatalog`: catálogo
  neutral, `ContractSource`, trusted `SourceRequest`, provenance, confidence,
  completeness, diagnostics, merge, conflicts, policy y proyección segura.
- `specs/go-backend-inspector/*` e `internal/goinspector`: input sellado,
  filesystem con handle retenido/TOCTOU, changed-path closure, límites,
  diagnostics y adaptación derived. Se reutilizan garantías y contratos, no sus
  heurísticas específicas de Go.
- `internal/revisionbinding`, `internal/githubpr` e
  `internal/validationworkflow`: repository/commit exactos, head/changed paths y
  revalidación antes de aprobación/ejecución.

Los nombres de annotations y APIs Spring/Java incluidos en requirements son el
contrato de patrones de entrada. Se traducen a modelos propios; ningún tipo Spring
ni AST Java cruza al catálogo o al planner.

## Contratos propuestos

### Input sellado

`InspectionDescriptor` contendrá conceptualmente:

- repository ID, commit OID completo, source ID y adapter version;
- snapshot filesystem con root handle retenido;
- source sets nombrados y sus roots relativos, inspection roots y changed paths;
- build context explícito: release Java soportado, packages de annotations/API
  allowlisted y policy de generated sources;
- confidence/completeness y projection policy asignadas externamente;
- límites escalares del inspector y límites neutrales de catálogo.

El constructor copia, normaliza y ordena colecciones. Valida todos los campos
antes de leer. El digest incluye revisión, source sets/roots, build context,
changed selection, límites semánticos, projection policy y adapter version. No
incluye paths absolutos, tiempos ni orden del filesystem.

### Filesystem y snapshot

La implementación extraerá de `goinspector` una capacidad neutral
`internal/sourcefs` o reutilizará una abstracción equivalente con las mismas
garantías verificadas:

- resolución relativa desde un directory handle autorizado;
- rechazo de paths absolutos, `..`, symlinks y reparse points;
- apertura no-follow y validación de identidad/tipo/tamaño antes/después;
- lectura y segunda verificación desde el mismo file handle;
- límites reservados antes de retener bytes;
- enumeración estable y ausencia de reapertura mediante path del host.

El refactor, si se realiza, conservará tests contractuales compartidos para que
Go y Spring no diverjan en path safety, TOCTOU o cancelación.

### Parser y resolución local

`JavaParser` será una interfaz pequeña consumida por `springinspector`. La
implementación productiva debe aceptar bytes+relative path+release y devolver un
AST aislado. Puede usar una librería Go pasiva/vendorizada, pero su frontera no
incluye filesystem, resolver de dependencias, shell, JNI ni callbacks capaces de
ejecutar código. Errores externos se convierten inmediatamente en códigos
cerrados.

`SymbolIndex` indexará packages, explicit/wildcard imports, top-level/nested
types, methods, fields, record components, enum constants y compile-time
constants dentro de source sets autorizados. La resolución usa identidad
fully-qualified. Un wildcard ambiguo no se resuelve por orden.

`ConstantEvaluator` solo evalúa literales Java, arrays/annotation arrays,
concatenaciones de strings constantes, references a `static final` locales y enum
constants allowlisted. Mantiene visited/depth/size y nunca evalúa initializers,
métodos, reflection o properties.

### Proyección de constraints declarativos

`contractcatalog` recibirá, si no existe un contenedor neutral suficiente, una
extensión aditiva y source-neutral `DeclarativeConstraint` asociada a una
operación. Contendrá categoría (`authorization_expression` o
`authorization_roles`), lenguaje/namespace, valores acotados y provenance. No
será `SecurityRequirement`, no concederá auth schemes y policy no la usará para
autorizar ejecución.

El merger la canonicalizará y preservará todas las procedencias. La proyección al
modelo la ubicará en una sección de datos no confiables y aplicará antes límites,
redacción y forbidden values. Si una extensión neutral no resulta necesaria, la
misma información se conservará como metadata neutral equivalente, nunca como
texto libre o DTO Spring expuesto al core.

### Resultado

`InspectionResult` contendrá una copia inmutable de:

- `SourceSnapshot` neutral;
- diagnostics cerrados, ordenados y acotados;
- affected operation keys;
- completeness efectiva, omissions y digest canónico.

El adapter `DerivedSource` usará kind/version estable `spring-static-v1`, validará
el `SourceRequest` completo antes de inspeccionar y permitirá al registry volver
a verificar snapshot, límites y projection.

## Componentes y responsabilidades

### `InspectionInput`

Sella autoridad y configuración. Impide que aliases mutables o source content
cambien roots, confianza, límites o projection tras la admisión.

### `RepositoryFS` / `sourcefs.Snapshot`

Proporciona la única lectura. Conserva las invariantes de root, handles, no-follow,
TOCTOU, bytes/files y cancelación que ya protege el inspector Go.

### `JavaParser`

Convierte exclusivamente bytes autorizados en AST. No resuelve tipos externos ni
interpreta comments, resources o build descriptors como instrucciones.

### `SourceSetIndex`

Enumera solo roots explícitos, aplica policy de test/generated sources y produce
un orden canónico de compilation units. `module-info.java`, resources y bytecode
no aportan facts v1.

### `SymbolIndex`

Resuelve packages/imports/types/nested members/constants locales y detecta
ambigüedad. Los símbolos Spring/Jakarta se reconocen por FQCN allowlisted aunque
su source/dependency no esté disponible.

### `ClosureResolver`

Expande controllers, constants, DTOs y referencias locales necesarias, además de
relaciones inversas para changed paths. Corta ciclos y profundidad sin tratar
ausencia como evidencia.

### `ControllerExtractor`

Reconoce `RestController` y los casos demostrables de `Controller`; combina
mappings de clase/método, paths/methods y consumes/produces bajo un límite de
producto. Solo crea una identity completa con method, path y Java method exactos.

### `ParameterExtractor`

Proyecta las cuatro annotations soportadas, sus aliases/defaults/requiredness y
tipos locales. Valida que cada `PathVariable` corresponda al template efectivo.

### `SchemaProjector`

Traduce el subconjunto estructural de Java a `contractcatalog.Schema`. Mantiene
visited types, substitutions genéricas, depth/nodes/bytes y reglas conservadoras
para required/nullability. Nunca materializa Lombok ni invoca Jackson.

### `ResponseExtractor`

Combina media types estáticos, `ResponseStatus`, tipo de retorno y construcciones
`ResponseEntity` allowlisted. Mantiene status/schema parciales cuando control-flow
o builders no son demostrables.

### `SecurityAnnotationExtractor`

Registra presencia, argumentos y provenance de `PreAuthorize`, `Secured` y
`RolesAllowed`. Trata expresiones/roles como datos no confiables y no interpreta
herencia, SpEL o habilitación runtime.

### `DiagnosticCollector`

Acepta solo code/stage/safe location cerrados, deduplica y ordena. Al alcanzar el
límite incrementa omissions sin conservar texto descartado.

### `DerivedSource`

Adapta la inspección al registry neutral y verifica kind/class/digests/version,
límites y projection. No puede elevar confianza o convertir partial en complete.

### Composición y workflow

CLI/API/GitHub/workflow crean el source request solo después de autorizar revisión
y filesystem. La configuración selecciona explícitamente `spring-static-v1`,
source sets, roots, release y policy derived. El catálogo, approval artifact y
execution binding conservan revision/source/catalog/policy digests; cualquier
drift invalida la continuación.

## Paquetes afectados

```text
internal/springinspector/      # input, parser boundary, index, closure, extractors, schemas, diagnostics, source adapter
internal/sourcefs/             # capacidad neutral opcional extraída con contratos de seguridad compartidos
internal/goinspector/          # solo adaptación mínima si se factoriza sourcefs, sin cambiar heurísticas
internal/contractcatalog/      # metadata neutral mínima para constraints declarativos, si es necesaria
internal/validationworkflow/   # composición y bindings de la nueva source
internal/githubpr/             # changed paths/head confiables ya existentes
cmd/featureproof/              # configuración explícita cuando se exponga por CLI
```

`agentplanning`, `planapproval`, `replanning`, tools, evidence y verdict no
importarán `springinspector` ni tipos AST. Solo reciben catálogo/proyecciones
neutrales.

## Flujo principal

1. El caller autoriza repository y commit y obtiene el snapshot retenido.
2. Trusted composition valida source sets, roots, Java release, framework
   identities, changed paths, límites y policy; sella el input.
3. Registry selecciona explícitamente `spring-static-v1` e invoca `Load(ctx)`.
4. El adapter verifica todos los bindings antes de la primera lectura.
5. `SourceSetIndex` enumera compilation units autorizadas en orden estable.
6. `JavaParser` parsea bytes; `SymbolIndex` registra símbolos e imports locales.
7. `ClosureResolver` alcanza controllers, mappings, constants y DTOs necesarios.
8. Los extractores producen identities, parámetros, schemas, responses y
   constraints únicamente con patrones v1 demostrables.
9. Se calculan completeness, diagnostics, affected keys, omissions y digest.
10. Registry revalida el snapshot y `contractcatalog` combina las fuentes.
11. Sufficiency policy selecciona operaciones elegibles. Constraints
    declarativos siguen siendo datos no ejecutables.
12. Workflow liga catálogo/policy/revisión a planning/approval/execution y vuelve
    a validar drift en sus checkpoints existentes.

## Flujos parciales y de error

- **Syntax error aislado:** se diagnostica la unidad; operaciones independientes
  continúan. Cualquier cierre que la requiera queda parcial/insuficiente.
- **Import/símbolo ambiguo:** no se elige por proximidad u orden; se corta el fact
  dependiente.
- **Mapping dinámico o sin verb:** no existe operation identity ejecutable.
- **Controller de vista:** se registra diagnóstico y no se proyecta como endpoint
  v1 mientras response-body semantics no sean demostrables.
- **DTO parcial:** identity/parameters seguros pueden sobrevivir; schema se omite
  o degrada por unidad completa.
- **Security expression no proyectable:** se omite el constraint, nunca la
  operación independiente; se degrada metadata si afecta completitud declarada.
- **Límite local no esencial:** se termina la unidad afectada y se devuelve
  partial si quedan identities seguras.
- **Root/binding/cancelación/límite de identidad:** error tipado sin publicar un
  snapshot engañoso.
- **Conflicto multi-source:** lo conserva el merger; Spring no prioriza.
- **Policy insuficiente o drift:** no se llama al modelo ni se autoriza ejecución.

## Propiedad del estado y concurrencia

Cada `Inspect` posee parser session, índices, visited sets, substitutions,
budgets, diagnostics y buffers. Inputs y resultados se copian al cruzar
fronteras. El source adapter es read-only y no guarda estado por ejecución.

V1 recorre secuencialmente para hacer reproducibles budgets, diagnostics y
cancelación. No crea goroutines. El registry puede cargar fuentes concurrentes;
cada inspección usa estado y snapshot independientes y no comparte caches
mutables del parser.

## Cancelación, deadlines, retries e idempotencia

Se consulta `ctx.Err()` antes y durante enumeración, read, parse de unidades,
indexación, expansión de closure, evaluación de constants, mapping cross-product
y cada schema node. El timeout configurado se acota por el deadline del caller.
No se sustituye context ni se lanzan timers/goroutines ocultos.

El inspector local no reintenta. Repetir el mismo input sellado produce el mismo
snapshot/digest. Cambiar bytes, revisión, roots, source sets, release, allowlists,
generated policy, projection, límites semánticos o adapter version cambia la
identidad e invalida reutilización.

## Seguridad y secretos

- El paquete no recibe puertos de proceso, shell, red, credentials, environment,
  classpath ni dependency resolver.
- Solo `RepositoryFS` puede leer; roots y files se mantienen por handles ya
  validados y no por paths reabiertos.
- Source, comments, Javadocs, annotations, strings, parser messages y resources
  son datos no confiables, no instrucciones.
- Provenance usa paths relativos slash-normalized, líneas y símbolos acotados.
- Expresiones/roles pasan projection/redaction y se separan de trusted policy.
- Diagnostics/events nunca incluyen snippets, mensajes crudos, paths absolutos
  ni forbidden values.
- El catálogo derived nunca concede tools, network targets, credentials o auth.

## Canonicalización y digest

Antes del digest se ordenan source sets, files, units, symbols, operations,
parameters, content types, responses, constraints, provenance, diagnostics y
affected keys. Se preserva el orden solo cuando tenga semántica contractual; roles
tipo set se deduplican/ordenan. Paths usan `/`; methods/media types se normalizan
según contratos neutrales. Digests excluyen duración y ubicaciones del host.

Estados conceptuales:

```text
sealed → snapshot_read → parsed → indexed → closure_resolved → projected → ready
   └──────── invalid / cancelled / limit_exceeded / insufficient ─────────┘
```

No se persiste ni publica un estado intermedio.

## Observabilidad

Eventos sugeridos: `spring_inspection_started`, `spring_unit_parsed`,
`spring_mapping_resolved`, `spring_mapping_unresolved`,
`spring_inspection_limited`, `spring_inspection_completed` y
`spring_inspection_failed`.

Campos permitidos: run/workflow ID si existe, repository ID seguro, commit OID,
source/version, source-set IDs, counts, limit kind, completeness, duration,
digest y outcome. Se excluyen source snippets, expressions/roles, filenames no
proyectados, parser text y paths absolutos.

## Convención obligatoria de comentarios sistémicos

Toda entidad, tipo, constante de dominio, función, método y helper nuevo o
modificado debe incluir un comentario que explique su responsabilidad dentro de
FeatureProof, la frontera que protege o la invariante que mantiene. Aplica a
helpers, fakes, spies y fixtures programáticos de tests. Repetir el nombre o la
implementación no cumple la convención.

## Estrategia de pruebas

Los tests crean proyectos Spring Boot fixture temporales/in-memory por la misma
frontera de snapshot. No usan checkout real, Maven/Gradle/JDK, commands, module
caches, red, credentials, backend desplegado ni `time.Sleep`. Spies fallan ante
cualquier capacidad prohibida. Parser, context y límites son deterministas.

| Requirement | Pruebas previstas |
|---|---|
| R1 | `TestSpringInspectionRejectsUnsealedRevisionSourceSetsAndEscapingPaths`; `TestSpringSourceCannotElevateAuthority` |
| R2 | `TestSpringInspectionNeverExecutesBuildCompilerProcessorsOrNetwork`; `TestMissingClasspathRemainsPartial` |
| R3 | `TestMappingsComposeAliasesArraysConstantsAndPrefixes`; `TestDynamicAndLookalikeAnnotationsDoNotCreateOperations` |
| R4 | `TestSpringParametersProjectExactNamesRequirednessAndTypes`; alias/path mismatch tests |
| R5 | `TestLocalDTORecordsEnumsNestedAndGenericsProjectBoundedly`; `TestLombokSerializersCyclesAndExternalTypesStayPartial` |
| R6 | `TestConsumesProducesResponseStatusAndResponseEntityProjectExactResponses`; controller-view/dynamic branch tests |
| R7 | `TestSecurityAnnotationsRemainStructuredNonExecutableData`; redaction, class/method provenance and no-auth-claim tests |
| R8 | `TestEverySpringClaimHasExactCanonicalRevisionProvenance`; `TestPartialClosureCannotBecomeCompleteOrHighConfidence` |
| R9 | `TestIndependentControllersSurviveSyntaxAndTypeErrors`; insufficient/diagnostic cap/canary tests |
| R10 | `TestChangedControllerRetainsUnchangedDTOClosure`; `TestChangedDTOMarksDependentOperations`; source-set isolation tests |
| R11 | table tests por límite; cancelación en todas las fases; permutation, concurrency y race tests |
| R12 | `TestSpringInspectorLoadsAsDerivedContractSource`; merge/policy deny-allow/conflict/drift workflow tests |
| R13 | `TestSpringErrorsDiagnosticsEventsAndCatalogExcludeCanaries`; systemic comment audit y fixture matrix completa |

También se ejecutarán suites afectadas de `sourcefs`/`goinspector`,
`contractcatalog`, `validationworkflow`, `githubpr` y `cmd/featureproof`, además
de compile-only, `go vet`, race, repetición de determinismo y coverage mínimo del
85 % del código nuevo/modificado. El informe mapeará cada R a tests reales.

Las boundaries estáticas prohibirán dependencias/imports o llamadas a APIs de
process execution, HTTP/DNS, env, Git, Maven/Gradle/JDK y acceso filesystem
general desde `springinspector`. Las spies runtime constituyen una segunda prueba;
ninguna sustituye a la otra.

## Alternativas y decisiones

- **Invocar Maven/Gradle o `javac`:** descartado; ejecutaría configuración/código
  no confiable y podría acceder a red, caches y processors.
- **Usar classpath/reflection de Spring:** descartado; amplía autoridad y depende
  del host. V1 resuelve sources locales y FQCNs allowlisted.
- **Parsear con regex:** descartado; no preserva scopes, aliases, syntax ni
  provenance fiable. Se exige AST.
- **Inferir source sets desde build files:** descartado; se reciben explícitos de
  trusted composition.
- **Asumir defaults Spring para mappings dinámicos:** descartado; un method/path
  incompleto no genera operation identity.
- **Ejecutar Lombok/annotation processors:** descartado; la ausencia generada se
  representa como parcial.
- **Interpretar SpEL/roles como autorización efectiva:** descartado; se conserva
  presencia estructural sin semántica runtime.
- **Analizar solo changed files:** descartado; rompe constants/DTOs/controllers.
  Changed paths seleccionan impacto y closure.
- **Copiar el resolver Go:** descartado; solo se comparten filesystem y garantías
  neutrales. La semántica Java/Spring vive en su paquete.
- **Exponer AST Spring al planner:** descartado; rompería el catálogo neutral.

No se solicita excepción a `docs/architecture.md` ni `docs/conventions.md`.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T2, T11, T13 |
| R2 | T2, T3, T13 |
| R3 | T4, T13 |
| R4 | T5, T13 |
| R5 | T3, T6, T13 |
| R6 | T4, T7, T13 |
| R7 | T8, T10, T13 |
| R8 | T1, T4–T10, T13 |
| R9 | T3–T10, T13 |
| R10 | T3, T9, T13 |
| R11 | T1–T10, T12, T13 |
| R12 | T10–T12, T13 |
| R13 | T1–T13 |

## Convenciones consolidadas tras revisión independiente

- El input sellado incluye `RootIdentity` y la lista completa y exacta de identidades Spring/Jakarta v1. Ambas dimensiones participan en el digest; el filesystem retornado se vuelve a comprobar contra los source sets activos.
- La resolución mantiene candidatos de imports y tipos. Imports explícitos incompatibles, más de un wildcard y FQCN duplicados invalidan la unidad o el símbolo con diagnóstico; nunca se elige por orden.
- `value`/`path` y `value`/`name` son aliases: si ambos aparecen deben ser iguales. El evaluador de constantes admite exclusivamente literales, referencias `static final` locales y concatenación `+` de strings dentro de límites, conservando dependencias.
- Un `@Controller` se decide por método: `@ResponseBody` efectivo o `ResponseEntity` con identidad exacta. Un método de vista se omite con diagnóstico aunque otro método de la clase sí produzca body.
- Un DTO v1 expone únicamente record components y fields `public` escritos en source. Fields privados, Lombok y Jackson no allowlisted no fabrican propiedades. Los wrappers se reconocen por FQCN JDK/Spring resuelto, no por simple name.
- `ResponseEntity` se analiza por cada expresión `return`; se conservan todos los status demostrables y cualquier branch no resuelto mantiene completitud parcial. Llamadas que no son retornadas y tipos locales homónimos no crean respuestas Spring.
- Cancelación y límites globales se propagan de inmediato mediante checkpoints internos. Las pérdidas atómicas de schema y seguridad producen diagnósticos cerrados con ubicación segura.
- El workflow recompone admission antes de decidir y otra vez inmediatamente antes de consumir aprobación o ejecutar. La composición multi-source recibe una lista explícita y sellada de adapters y construye un solo registry/catalog/policy.
- `SourceSummary` y cada `Provenance` Spring publican repository ID y commit OID seguros y exactos; canonicalización, claims y merge los incluyen.
