# Inspector estático de backends Go — Design

## Resumen de la solución

Se añadirá un paquete cohesionado `internal/goinspector` que recibe un snapshot
de repositorio ya autorizado y produce un resultado de inspección inmutable. El
resultado contiene un `contractcatalog.SourceSnapshot` de clase `derived`,
diagnósticos seguros y metadata de impacto PR. Un adapter pequeño implementa
`contractcatalog.ContractSource` y permite combinar esta contribución con las
demás fuentes de la feature 21.

El inspector no descubre autoridad ni dispone de capacidades generales. La
composición crea un input sellado con repository/revision, filesystem enraizado,
roots, cambios, build context, matchers de auth, límites y metadata de source. El
paquete analiza bytes mediante `go/parser`, `go/ast`, `go/token` y una resolución
de símbolos/tipos limitada a fuentes dentro del snapshot. No utiliza
`go/packages`, `go/build` con acceso al host, importers externos ni comandos.

La extracción se divide en hechos demostrables. Primero se descubre el grafo
estático de registros y handlers; después se proyectan request/response/security.
Una operación solo existe si método, path efectivo y handler son resolubles. Las
partes no demostrables generan diagnósticos y reducen completitud. El catálogo y
la policy existente deciden finalmente si el contexto es suficiente.

## Evidencia y contexto consultados

- `AGENTS.md`, `docs/specs.md`, `docs/architecture.md` y
  `docs/conventions.md` establecen SDD, separación modelo/sistema, fail-closed,
  límites, cancelación, pruebas herméticas y trazabilidad.
- `specs/contract-source-abstraction/*` y `internal/contractcatalog` definen
  `ContractSource`, `SourceRequest`, `SourceSnapshot`, clase `derived`, trusted
  metadata, límites, provenance, merge, conflicts, policy y contexto neutral.
- `internal/revisionbinding` define repository/commit exactos, OIDs válidos y
  bindings que invalidan drift. El inspector consume esa identidad ya admitida;
  no ejecuta el `GitResolver`.
- `internal/githubpr` aporta el head OID refrescado y paths de cambios desde una
  frontera confiable cuando la composición PR los habilite. Payloads/webhooks no
  entran directamente al inspector.
- `internal/validationworkflow` ya sella catalog/policy/revision antes de
  planificar y revalida antes de ejecutar; la nueva source se conecta en su
  composición sin crear una ruta de ejecución alternativa.

No se consultan contratos externos en runtime. Los nombres de APIs de Chi v5
enumerados en requirements se tratan como patrones de entrada no confiable y se
traducen a modelos propios; no se copian tipos internos de Chi.

## Contratos propuestos

### Input sellado

`InspectionDescriptor` será construido por trusted composition y validado antes
de cualquier lectura. Conceptualmente contiene:

- `RepositoryID`, `CommitOID`, `SourceID`, `ExpectedDigest`, adapter version;
- `RepositoryFS`, una interfaz mínima de lectura/listado/stat relativo sin acceso
  a paths del host;
- roots explícitos de paquetes/archivos y changed paths opcionales;
- build context explícito (`GOOS`, `GOARCH`, tags) sin defaults del host;
- auth matchers `qualified symbol -> neutral scheme ID`;
- límites escalares y projection/redaction policy de `contractcatalog`.

Todas las colecciones se copian y ordenan al sellar. `ExpectedDigest` liga
revision, configuración semántica, roots, change selection y versión del adapter;
el source digest neutral usa esa identidad y no un path de máquina.

### Resultado

`InspectionResult` contiene:

- snapshot neutral listo para validación por `contractcatalog.Registry`;
- lista ordenada de `InspectionDiagnostic` con code cerrado, stage, safe location,
  framework y operation key opcional;
- conteo de diagnósticos omitidos;
- operation keys afectadas ordenadas;
- completeness efectiva y digest canónico del resultado.

El adapter devuelve el snapshot por `ContractSource.Load`. Para no perder
diagnósticos en composición se ampliará mínimamente el modelo neutral con metadata
opcional, canónica y segura de source diagnostics/impact, o una proyección
equivalente propiedad de `contractcatalog`. No se añadirá texto libre ni un DTO
específico de Go al planner. Merge conserva esta metadata como auditoría; policy
solo usa claims, conflictos, confidence y completeness.

### Diagnósticos cerrados

Códigos previstos: `syntax_error`, `unsupported_registration`, `dynamic_method`,
`dynamic_path`, `unresolved_handler`, `unresolved_import`, `unresolved_type`,
`unsupported_schema`, `dynamic_response`, `unconfigured_auth`, `cycle`,
`outside_snapshot`, `limit_exceeded` y `cancelled`. Las ubicaciones son paths
relativos normalizados más línea acotada y símbolo seguro. Nunca incluyen parser
errors crudos, snippets o literals.

## Componentes y responsabilidades

### `InspectionInput`

Posee una copia sellada de toda autoridad. Valida revisión, roots, changed paths,
build context, matchers y límites antes de exponer una lectura.

### `RepositoryFS`

Interfaz definida por `goinspector` con las operaciones mínimas para leer un
snapshot inmutable. El adapter local resuelve/canoniza el root una vez, rechaza
escapes y symlinks, aplica bytes/files y no devuelve paths absolutos.

### `PackageIndex`

Parsea `go.mod` únicamente para conocer el module path y parsea archivos Go
seleccionados. Construye índices de imports, constants, declarations, methods y
calls. Respeta build constraints con el build context explícito. No carga export
data ni consulta el entorno.

### `ClosureResolver`

Parte de roots explícitos o roots afectados y recorre declaraciones locales
necesarias para routers, handlers y schemas. Mantiene estados de visita para
cortar ciclos. Las dependencias estáticas no cambiadas permanecen en el cierre;
changed paths solo afectan selección inicial e impacto.

### `RouteExtractor`

Reconoce exactamente los patrones v1. Usa identidad de import path/símbolo y
flujo estático, no nombres heurísticos. Compone prefixes y scopes de middleware,
y solo crea identidad de operación completa con method/path/handler.

### `HandlerExtractor`

Recorre el handler local dentro de límites y obtiene path/query reads, decode
JSON, responses y auth scope. El análisis de control-flow es deliberadamente
limitado a bloques, guards terminales, switch/if estáticos y returns; cualquier
ambigüedad reduce completitud.

### `SchemaProjector`

Traduce tipos Go locales soportados a `contractcatalog.Schema`, con ownership de
visited/depth/items y reglas conservadoras para required. Custom marshalers,
interfaces y tipos externos no se ejecutan ni se presumen.

### `DiagnosticCollector`

Deduplica, ordena y limita códigos/ubicaciones seguros. Al agotar capacidad solo
incrementa un contador. Decide qué diagnóstico degrada source/operation/claim,
sin transformar warnings en facts.

### `DerivedSource`

Implementa `ContractSource`. Valida que el request tenga kind exacto,
`ClassDerived`, input sellado y digests/version/limits coincidentes; ejecuta una
inspección y proyecta el snapshot neutral. No puede alterar metadata confiable.

### Composición

La composición CLI/API/GitHub/workflow crea `SourceRequest` solo tras admitir la
revisión y snapshot local. Registra el kind estable `go-static-v1`. Configura una
`SufficiencyPolicy` que menciona explícitamente `derived`; sin esa regla el flujo
falla cerrado. No se añaden defaults silenciosos.

## Paquetes afectados

```text
internal/goinspector/          # input, FS, parse/index, closure, routes, handlers, schemas, diagnostics, source adapter
internal/contractcatalog/      # metadata neutral mínima para diagnostics/impact si resulta necesaria
internal/validationworkflow/   # composición/admisión de la nueva source y bindings existentes
internal/githubpr/             # proyección trusted de changed paths/head hacia composición, sin análisis
cmd/featureproof/              # configuración explícita de source/roots/limits/policy cuando se exponga por CLI
```

`agentplanning`, `planapproval`, `replanning`, tools, evidence y verdict no deben
conocer tipos de `goinspector`; reciben únicamente el catálogo neutral.

## Flujo principal

1. El caller autoriza repository, commit y modo de selección; obtiene un snapshot
   de filesystem inmutable correspondiente a esa revisión.
2. Trusted composition valida roots, changes, build context, auth matchers,
   límites y source metadata, y crea un `SourceRequest` derived.
3. Registry invoca `DerivedSource.Load` con context.
4. El inspector valida el input completo antes de leer.
5. Se enumeran y parsean unidades en orden canónico bajo límites.
6. `PackageIndex` crea índices locales; imports externos solo aportan identidad
   sintáctica allowlisted para `net/http`, `encoding/json` y Chi.
7. `ClosureResolver` expande roots y dependencias locales necesarias.
8. `RouteExtractor` compone method/path/handler/middleware; descarta identidades
   incompletas y registra diagnostics.
9. `HandlerExtractor` y `SchemaProjector` añaden solo facts demostrables.
10. Se calcula completeness, provenance relativa, affected keys y digest.
11. Registry verifica snapshot contra trusted metadata y límites.
12. `contractcatalog` combina fuentes y aplica policy. Solo una selección
    elegible entra al planner; approval/execution quedan ligados a catalog,
    policy y revisión ya sellados.

## Flujos parciales y de error

- **Syntax error aislado:** se diagnostica el archivo; rutas independientes
  continúan. Cualquier ruta cuyo closure lo necesite no se emite o queda parcial.
- **Método/path/handler dinámico:** no se crea operation identity. Se conserva
  diagnóstico seguro y se continúa con registros independientes.
- **Tipo externo/no resoluble:** puede conservarse identity y parámetros ya
  demostrados; body/response schema se omite y completeness se degrada.
- **Límite local no esencial:** se detiene el traversal afectado y se devuelve
  resultado parcial si sobreviven operaciones seguras.
- **Límite de autoridad, cancelación, root/cierre obligatorio o ninguna operación
  segura:** error tipado sin snapshot parcial presentado como válido.
- **Conflicto con OpenAPI/otra source:** el merger neutral registra conflicto; el
  inspector no prioriza su salida.
- **Policy insuficiente:** no se invoca al modelo ni se crea autorización.

## Propiedad del estado y concurrencia

Cada llamada a `Inspect` posee token.FileSet, índices, visited sets, budgets,
diagnostics y buffers. Inputs/snapshots se copian al cruzar fronteras. El
`DerivedSource` puede ser read-only y compartirse; no retiene estado de una
ejecución. Dos inspecciones concurrentes de snapshots distintos no comparten
maps ni caches mutables.

V1 usa traversal secuencial para que límites, diagnostics y cancelación sean
reproducibles. No se lanzan goroutines internas. Si el caller ejecuta sources en
paralelo, cada una conserva context y estado propios.

## Cancelación, deadlines, retries e idempotencia

Se consulta `ctx.Err()` antes de cada lectura, archivo parseado, expansión de
closure, declaración/AST block relevante y schema node. El deadline procede del
caller y el tiempo máximo del inspector no puede excederlo. No se sustituye el
context ni se crean timers/goroutines ocultos.

El análisis local no hace retries. Repetir el mismo input sellado produce el
mismo resultado/digest. Un cambio de bytes, revisión, roots, build context,
matcher, límites semánticos o adapter version cambia el binding esperado e
invalida reutilización. Los resultados no se persisten parcialmente.

## Seguridad y secretos

- El inspector carece de puertos de proceso, red, credentials y environment.
- `RepositoryFS` es la única lectura y está enraizada; no sigue symlinks fuera ni
  abre imports mediante rutas del host.
- Comments, string literals, parser messages, file contents y custom tags son
  untrusted. Solo pasan campos estructurales allowlisted y redacción de feature21.
- Provenance usa path relativo slash-normalized, línea y símbolo acotados. No
  persiste root absoluto.
- Auth schemes provienen de configuración confiable; middleware no puede
  autodeclararse autorizado.
- Generated code ya presente puede analizarse solo bajo policy explícita, pero
  nunca se ejecuta su generador.

## Estado, canonicalización y digest

El resultado no necesita una máquina durable. Estados internos conceptuales:

```text
sealed → indexed → routes_resolved → projected → snapshot_ready
   └──────── invalid/cancelled/limit_exceeded ────────┘
```

Antes de digest se ordenan files, packages, declarations, operations, parameters,
responses, provenance, diagnostics y affected keys. Paths usan `/`, métodos se
normalizan según el catálogo y constants se evalúan sin ejecutar código. El
digest incluye versión de adapter y configuración semántica, no duración, orden
de filesystem ni paths absolutos.

## Observabilidad

Eventos sugeridos: `go_inspection_started`, `go_package_indexed`,
`go_route_resolved`, `go_route_unresolved`, `go_inspection_limited`,
`go_inspection_completed` y `go_inspection_failed`.

Campos permitidos: run/workflow ID si existe, repository ID seguro, commit OID,
source ID/version, framework, counts, limit kind, completeness, duration, digest
y outcome. Se excluyen roots absolutos, changed filenames no redactados, source
snippets, literals, parser messages, import errors crudos y secrets.

## Convención obligatoria de comentarios sistémicos

Toda entidad, tipo, constante de dominio, función, método y helper nuevo o
modificado debe incluir un comentario que explique su responsabilidad dentro de
FeatureProof, la frontera que protege o la invariante que mantiene. Aplica a
helpers, fakes, spies y fixtures programáticos de tests. Repetir el nombre o la
implementación no cumple la convención. El reviewer auditará todo el diff.

## Estrategia de pruebas

Los tests crean repositorios fixture temporales/in-memory mediante el mismo
`RepositoryFS`; no usan el checkout real, Internet, Git, comandos, module cache,
backend desplegado ni `time.Sleep`. Spies fallan ante lectura externa o capacidad
prohibida. Contexts y límites son deterministas.

| Requirement | Pruebas previstas |
|---|---|
| R1 | `TestInspectionRejectsUnsealedRevisionAndEscapingPaths`; `TestSourceAuthorityCannotBeElevated` |
| R2 | `TestInspectionNeverExecutesOrUsesNetwork`; `TestUnavailableImportsStayPartialWithoutResolver` |
| R3 | `TestNetHTTPGo122RegistrationsProduceExactOperations`; `TestLegacyAndDynamicPatternsDoNotInventMethods` |
| R4 | `TestChiComposesRoutesGroupsBuildersAndMiddleware`; `TestDynamicOrCyclicRouterBranchesStayUnresolved` |
| R5 | `TestPathAndQueryParametersRequireProvenStaticAccess`; `TestDynamicNamesAndUnsupportedConversionsDoNotAddClaims` |
| R6 | `TestJSONDecodeProjectsBoundedLocalSchemas`; `TestPointersTagsCyclesAndExternalTypesStayConservative` |
| R7 | `TestExplicitResponsesAndConfiguredAuthAreProjected`; `TestDynamicResponsesAndUnregisteredMiddlewareDoNotCreateClaims` |
| R8 | `TestEveryClaimHasCanonicalRevisionProvenance`; `TestPartialClosureCannotBecomeCompleteOrHighConfidence` |
| R9 | `TestIndependentRoutesSurviveSyntaxAndTypeErrors`; `TestNoSafeIdentityReturnsInsufficientContext`; diagnostic cap/canary test |
| R10 | table tests per limit; cancellation at read/parse/closure/types/projection; permutation and race tests |
| R11 | `TestChangedRootsRetainUnchangedStaticClosure`; `TestAffectedOperationsIncludeChangedHandlerAndRouter`; closure limit test |
| R12 | `TestGoInspectorLoadsAsDerivedContractSource`; policy deny/allow/conflict/drift workflow tests |
| R13 | `TestErrorsDiagnosticsEventsAndCatalogExcludeCanaries`; systemic comment audit; full fixture matrix |

Se ejecutarán suites de `goinspector`, `contractcatalog`, `validationworkflow`,
`githubpr` y `cmd/featureproof` afectadas, además de compile-only, `go vet`, race,
repetición de determinismo y coverage mínimo del 85 % del código nuevo/modificado.
El informe de implementación debe mapear cada R a tests reales y resultados.

## Alternativas y decisiones

- **Usar `go/packages` o `go list`:** descartado porque puede iniciar procesos,
  consultar módulo/cache/entorno y ampliar la frontera hermética.
- **Type-check completo con importer del host:** descartado porque leería
  dependencias fuera del snapshot. Se usa resolución local y stubs/patrones
  allowlisted mínimos para APIs estándar/Chi relevantes.
- **Inferir métodos para patterns legacy `net/http`:** descartado; registrar un
  path no demuestra el método aceptado.
- **Reconocer auth por nombres o header reads:** descartado; no demuestra
  enforcement. Solo trusted symbol matchers crean security claims.
- **Analizar únicamente archivos cambiados:** descartado; produciría rutas/tipos
  falsamente incompletos. Los cambios seleccionan roots/impact, no el cierre.
- **Escanear todo el repositorio siempre:** descartado; roots explícitos y límites
  preservan autorización, coste y reproducibilidad.
- **Ejecutar generadores para completar schemas:** descartado por seguridad y
  porque convertiría source en efecto.
- **Resolver conflictos dentro del inspector:** descartado; pertenece al catálogo
  neutral y a sufficiency policy.
- **Exponer AST/Go types al planner:** descartado; rompería neutralidad y haría al
  modelo dependiente de la fuente.

No se solicita excepción a `docs/architecture.md` ni `docs/conventions.md`.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T2, T10, T12 |
| R2 | T2, T3, T11, T12 |
| R3 | T4, T12 |
| R4 | T3, T5, T12 |
| R5 | T6, T12 |
| R6 | T3, T7, T12 |
| R7 | T8, T12 |
| R8 | T1, T4–T9, T12 |
| R9 | T3–T9, T12 |
| R10 | T1–T9, T11, T12 |
| R11 | T3, T5, T9, T12 |
| R12 | T9, T10, T12 |
| R13 | T1–T12 |
