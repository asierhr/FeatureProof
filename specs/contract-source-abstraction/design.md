# Fuentes de contrato independientes de OpenAPI — Design

## Resumen de la solución

Se añadirá un paquete cohesionado `internal/contractcatalog` que será dueño del
modelo neutral, la combinación, la política de suficiencia y la reducción para
el modelo. Los adapters de fuente traducirán inputs autorizados a `SourceSnapshot`
sin efectos externos. El paquete `openapi` conservará su loader, normalización,
validación de respuestas y límites; añadirá una proyección hacia el catálogo
neutral.

`agentplanning` dejará de importar `openapi` y recibirá un `ReducedContext`
neutral con un retained index exacto. `validationworkflow` compondrá fuentes,
merge, policy y reducción antes de inferencia, y sellará el digest canónico en
los contratos de aprobación, replanning y ejecución.

```text
authorized source descriptors + trusted metadata + limits
                │
                ▼
     ContractSource registry (kind exacto)
       ├─ OpenAPI adapter ──> validated openapi.Contract
       ├─ Derived adapter/fake
       ├─ Observed adapter/fake
       └─ Declared adapter
                │ SourceSnapshot[]
                ▼
       deterministic CatalogBuilder
       ├─ coalesce equal claims + all provenance
       └─ explicit typed conflicts
                │ OperationCatalog/v1
                ▼
       SufficiencyPolicy + explicit selection
       ├─ eligible ──> bounded ReducedContext/v1 ──> agentplanning
       ├─ insufficient_context
       └─ review_required
```

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `feature_list.json` | Alcance único de feature 21, SDD, acceptance y puerta humana. |
| `docs/architecture.md` | Modelo no autoritativo, datos externos inertes, default deny, límites, cancelación, adapters sustituibles y secretos fuera del core. |
| `docs/conventions.md`, `docs/specs.md`, `docs/verification.md` | Interfaces consumidoras, errores tipados, comentarios sistémicos, race, coverage y trazabilidad. |
| `specs/openapi-inspector`, `internal/openapi` | Contrato validado actual, operación HTTP, schema, seguridad, respuesta, reducción, omisiones, referencias locales y response validation. |
| `specs/agent-plan-generation`, `internal/agentplanning` | Acoplamiento actual a `openapi.ReducedContext`, retained operation authority, materialización y fingerprint de procedencia. |
| `specs/validation-workflow-orchestration`, `internal/validationworkflow` | `ContractReducer`, admisión antes de inferencia, binding canónico y ejecución desde plan aprobado. |
| `internal/planapproval`, `internal/replanning` | Sellado actual denominado OpenAPI y contexto reducido reutilizado en aprobación/replanificación. |
| `cmd/featureproof/workflow.go` | Composición local actual del loader OpenAPI y selección explícita por constraints. |

No se consulta un contrato externo nuevo. El único formato externo productivo
interpretado sigue siendo OpenAPI 3.0/3.1 mediante el inspector ya aprobado. Los
snapshots derived/observed/declared son DTOs propios y no prometen un formato de
análisis o captura externo.

## Componentes y responsabilidades

### `contractcatalog.SourceClass`, `Confidence` y `Completeness`

Enums cerrados validados en construcción. `SourceClass` distingue
`authoritative`, `derived`, `observed`, `declared`. `Confidence` usa niveles
ordenables cerrados (`untrusted`, `low`, `medium`, `high`, `verified`) solo para
comparar con una policy explícita. `Completeness` usa `unknown`, `partial`,
`complete`; el agregado toma el estado más conservador de los campos requeridos.
Ningún adapter puede ascender valores: los recibe dentro de `SourceRequest`
desde composición y el registry los vuelve a aplicar al snapshot.

### `SourceDescriptor` y `ContractSource`

`SourceDescriptor` contiene kind, source instance ID seguro, digest esperado,
clase/confianza confiables, referencia opaca autorizada y límites. La referencia
solo la interpreta el adapter elegido; nunca aparece en contexto o errores.

La interfaz, propiedad del servicio consumidor, será mínima:

```text
Load(ctx, SourceRequest) -> SourceSnapshot | typed error
```

`SourceSnapshot` contiene versión, identity/digest/adapter version seguros,
completitud y claims ya traducidos. El registry selecciona por kind exacto,
rechaza duplicados y valida el snapshot completo antes de devolverlo. No existe
fallback entre kinds.

### Modelo de claims

`OperationClaim` tiene `LogicalID` estable dentro de la fuente, `OperationKey`,
operation name opcional y claims anidados:

- `ParameterClaim`: name, location, required y schema.
- `RequestClaim`: required y schemas por media type.
- `ResponseClaim`: selector de status, headers y schemas por media type.
- `SecurityClaim`: alternativas OR, schemes/scopes AND y acceso anónimo explícito.
- `SchemaClaim`: subset neutral ya admitido por el inspector, con nodos
  estructurales y completitud por nodo.

Cada valor material lleva `ClaimMetadata{provenance[], confidence,
completeness}`. Las colecciones se poseen y copian profundamente. Los tipos no
contienen validators ejecutables, ASTs, documentos crudos ni callbacks.

### Procedencia

`Provenance` contiene `SourceID`, `SourceClass`, `SourceDigest`,
`AdapterVersion` y `SafeLocation`. `SafeLocation` es una secuencia estructural
limitada de segmentos allowlisted o un índice opaco; nunca un path absoluto o
URL. El adapter OpenAPI traduce sus locations a documento relativo autorizado +
pointer/índice seguro y conserva el digest del conjunto cargado.

Para claims iguales se ordena y deduplica procedencia por tuple completa. La
clase continúa asociada a cada procedencia; el resumen de operación enumera las
clases presentes y jamás usa una etiqueta authoritative si los hechos
relevantes dependen solo de otra clase.

### `CatalogBuilder`

Valida todos los snapshots primero y construye de forma all-or-nothing. Ordena
fuentes por `(source_id, digest, adapter_version)`, operaciones por `(path,
method, logical_id)` y campos por sus claves naturales. Serializa JSON canónico
propio y calcula SHA-256 sobre version, claims, metadata, conflictos y policy
independiente del orden de entrada.

Claims estructuralmente iguales se fusionan conservando procedencias. Claims
distintos nunca se sobrescriben. Los mapas se convierten en listas ordenadas en
la proyección canónica; schemas se normalizan recursivamente y tienen límites de
profundidad/trabajo.

### Detección de conflictos

`Conflict` contiene ID derivado, categoría cerrada, operation/logical identity,
field path seguro y referencias a las procedencias incompatibles, no los valores
potencialmente sensibles. Categorías v1: `operation_identity`, `schema`,
`security`, `response`.

- `operation_identity`: un mismo `(source namespace, logical_id)` afirma method
  o path distintos, o una identidad global explícita colisiona.
- `schema`: mismo campo de una operation key tiene formas canónicas distintas.
- `security`: alternativas o requisitos efectivos incompatibles.
- `response`: mismo selector/header/media type afirma formas incompatibles.

La coincidencia de `operationId` por sí sola no une operaciones entre fuentes,
pues no es identidad universal. Una policy no elimina conflictos; puede marcar
una operación `review_required` o inelegible. La resolución humana futura debe
producir una nueva fuente/snapshot autorizado, nunca mutar el catálogo en sitio.

### `OperationCatalog`

Snapshot inmutable v1 con operaciones, source summaries, conflictos, límites,
omissions y digest. Una operación puede permanecer auditable aunque no sea
elegible. La API devuelve copias profundas y ofrece lookup exacto por
`OperationKey`; no expone mapas internos mutables.

### `SufficiencyPolicy`

Valor explícito aportado por composición: versión/ID, clases permitidas, mínimo
de confianza por clase/uso, completitud requerida para identidad/request/
security/response, tratamiento de conflictos y decisión ante revisión. No hay
defaults. El constructor rechaza policies parciales o que pretendan etiquetar
una clase como otra.

`Evaluate(selection, catalog)` retorna una unión cerrada:

- `EligibleSelection`: operations exactas con digest de catalog+policy.
- `InsufficientContext`: missing kinds/fields y operation keys seguros.
- `ReviewRequired`: conflict IDs y operation keys seguros.

No devuelve textos de fuentes. Para ejecución se evalúa de nuevo el mismo
selection y se compara el binding digest; cualquier drift invalida el plan.

### Reducer neutral

`BuildContext(ctx, catalog, policy, ConstraintSelection[], Limits)` incluye solo
selecciones elegibles y schemas alcanzables. Produce
`contractcatalog.ReducedContext` con media type
`application/vnd.featureproof.operation-catalog-context+json;v=1`, `Untrusted:
true`, bytes canónicos, omisiones y retained claims.

El retained index liga constraint ID, operation key, field fingerprints,
provenances, confidence/completeness y catalog/policy digests. La materialización
del planner exige coincidencia exacta con ese índice. Un bloque que no cabe se
omite completo y hace la selección insuficiente; no existe autoridad implícita
desde el catálogo completo.

### Adapter OpenAPI

Vive junto a `internal/openapi` o en un subarchivo sin invertir dependencias: el
adapter puede importar `contractcatalog`, mientras el catálogo neutral no
importa `openapi`. Reutiliza exclusivamente el `Contract` normalizado y sus APIs
seguras; no vuelve a parsear bytes. Traduce catalog/descriptores, seguridad,
schemas y omisiones, y conserva el response validator existente como capacidad
del contrato OpenAPI fuera del modelo neutral.

OpenAPI aprobada se compone como `authoritative`; esa clase viene de trusted
composition y no del documento. El adapter no añade red ni resolución de
credenciales y continúa sujeto a root/references/bytes actuales.

### Adapters derived, observed y declared

La entrega define contratos y fakes/fixtures seguros para derived y observed, y
puede incluir un loader local declarativo mínimo si hace falta demostrar la
frontera. Ninguno ejecuta analizadores o captura red. Un adapter derivado recibe
un resultado ya materializado y acotado; uno observado recibe hechos ya
autorizados; uno declarado recibe configuración ya validada. Sus límites
epistemológicos se reflejan en completeness y nunca generan afirmaciones
negativas por ausencia.

### Migración del planificador y workflows

`agentplanning.GenerationRequest` cambia `OpenAPI` por un contexto neutral
(`Contract` o nombre equivalente), y provenance/fingerprint/materialization se
reescriben contra el retained index neutral. El protocolo del modelo sustituye
la clave/media type específica conservando la etiqueta `untrusted_data` y las
defensas contra prompt injection.

`validationworkflow.ContractReducer` pasa a una frontera de composición que
carga fuentes, construye catálogo, aplica policy y reduce. `AdmissionSnapshot`
conserva catalog y policy digests. `planapproval` y `replanning` renombran su
binding de contexto sin perder invalidación. Si se necesita leer artefactos ya
creados durante tests/compatibilidad, la traducción v1 OpenAPI se realiza en un
adapter explícito; no se mantiene el tipo OpenAPI dentro del planner.

La CLI histórica `validate --plan` continúa independiente. El workflow actual
con una fuente OpenAPI se compone de forma equivalente sin obligar a los users a
crear manualmente un catálogo.

## Paquetes afectados

```text
internal/contractcatalog/     # modelo neutral, sources, merge, policy, reducer
internal/openapi/             # proyección desde Contract validado
internal/agentplanning/       # contexto/procedencia neutrales
internal/validationworkflow/  # composición y digests de catálogo/policy
internal/planapproval/        # binding neutral del contexto contractual
internal/replanning/          # resumen y revalidación neutrales
cmd/featureproof/             # composición OpenAPI existente mediante registry
```

Los cambios en paquetes terminados serán migraciones mínimas del contrato. No se
modifican reglas de tools, assertions, evidence, target, OAuth ni preview.

## Flujos principales

### Una fuente OpenAPI

1. Composición autoriza root/ref y fija source metadata, policy y límites.
2. Registry selecciona el adapter OpenAPI.
3. El inspector carga y valida el contrato como hoy.
4. El adapter proyecta un `SourceSnapshot` authoritative.
5. Builder valida, canoniza y crea `OperationCatalog`.
6. Policy evalúa selecciones de constraints.
7. Reducer crea contexto neutral; planner genera y materializa solo contra su
   retained index.
8. Approval/execution sellan catalog+policy digests y revalidan drift.

### Varias fuentes compatibles

1. Se cargan secuencialmente en orden configurado, cada una con límites propios
   y contexto cancelable; el agregado se construye solo tras validar todas.
2. Builder ordena snapshots independientemente del orden recibido.
3. Claims iguales se coalescen y conservan todas las procedencias.
4. Policy decide elegibilidad explícita y el reducer expone únicamente selección
   relevante.

### Conflicto o contexto insuficiente

1. Builder registra conflicto tipado sin escoger ganador.
2. Policy retorna `review_required` para conflictos configurados como revisables,
   o `insufficient_context` para claims que no alcanzan mínimos.
3. No se crea contexto para esa operación, no se llama al modelo y no se ejecuta.
4. El workflow proyecta categorías y IDs seguros; los detalles siguen en el
   catálogo auditable.

## Propiedad del estado y concurrencia

Cada `Load` posee sus buffers y traversal. `CatalogBuilder` no retiene referencias
mutables del caller; produce un snapshot inmutable. Registry, policy y limits se
construyen antes de uso y son read-only. Cargas/workflows independientes pueden
progresar concurrentemente; dentro de una admisión se conserva orden explícito
para que límites y cancelación tengan resultados reproducibles.

No se lanzan goroutines ocultas. Si composición paralelizara fuentes en el
futuro, deberá preservar la regla de no empezar trabajo tras el primer límite o
cancelación, por lo que no forma parte de v1.

## Cancelación, retries e idempotencia

Context se propaga por carga, merge, policy traversal y reducción. Se comprueba
antes de cada fuente y en recorridos acotados. No hay retries dentro de
`contractcatalog`: una fuente local determinista se reintenta desde composición
con la misma identity/digest. Repetir exactamente el conjunto produce el mismo
digest; reutilizar source ID con distinto digest falla.

No existen operaciones externas que reconciliar. El workflow conserva su
idempotency key y sella el digest final, no referencias mutables de adapters.

## Seguridad y secretos

Sources reciben contenido ya autorizado y no tienen ports de red, procesos,
tools o credentials. El core no lee environment/files directamente. El adapter
OpenAPI conserva `SourceFS` local acotado. Referencias opacas, paths absolutos,
payloads crudos y errores externos no atraviesan el snapshot seguro.

Todo contenido de fuentes se serializa bajo `untrusted_data`. La reducción usa
allowlist estructural y límites; no incluye descripción/documentación libre. Los
diagnósticos usan categorías, source IDs allowlisted, safe locations y conflict
IDs. Tests con canarios cubren todas las superficies.

## Observabilidad

Eventos seguros sugeridos: `contract_source_load_started`,
`contract_source_loaded`, `contract_catalog_built`, `contract_conflict_found`,
`contract_selection_insufficient`, `contract_review_required`,
`contract_context_reduced`.

Campos: workflow/run ID cuando exista, source kind/class, source ID seguro,
adapter/catalog/policy version, conteos, digest no secreto, duración, outcome y
categoría. No se registran source refs, paths absolutos, schemas/payloads,
credenciales ni contenido del modelo. Logs no son autoridad ni estado.

## Convención obligatoria de comentarios sistémicos

Toda entidad, tipo, constante de dominio, función, método y helper nuevo o
modificado debe incluir un comentario que explique su responsabilidad dentro de
FeatureProof, la frontera que protege o la invariante que mantiene. Esto aplica
también a helpers y fakes de tests. El comentario no debe limitarse a repetir el
nombre o la implementación. El reviewer verificará esta obligación sobre todo el
diff de la feature.

## Estrategia de pruebas

Tests herméticos usan filesystem temporal/in-memory, fake sources, spy ports sin
capacidades externas, reloj/IDs cuando correspondan y contextos controlados. No
usan Internet, backend desplegado, modelo real, credenciales, procesos ni
`time.Sleep`.

| Requirement | Pruebas previstas |
|---|---|
| R1 | `TestPlannerConsumesOnlyVersionedNeutralCatalog`; dependency/AST boundary test |
| R2 | `TestAllSourceClassesProduceCommonSnapshot`; `TestRegistryRejectsUnknownAndDuplicateSources`; concurrent isolation/race |
| R3 | `TestEveryMaterialClaimRetainsSafeProvenance`; `TestUnsafeLocationsAndCanariesNeverEscape`; immutability test |
| R4 | `TestSourceCannotElevateConfiguredClassOrConfidence`; `TestCompletenessAggregationIsConservative`; projection labels test |
| R5 | `TestOpenAPIAdapterPreservesNormalizedCatalogSecurityResponsesAndOmissions`; existing `internal/openapi` suite and response validation regression |
| R6 | `TestMergeIsOrderIndependentAndCoalescesEqualClaims`; canonical digest permutation/property test |
| R7 | `TestMethodPathSchemaSecurityAndResponseConflictsAreExplicit`; `TestConflictedOperationFailsClosedButUnselectedOperationRemainsUsable` |
| R8 | `TestPolicyReturnsEligibleInsufficientOrReviewRequired`; `TestCatalogOrPolicyDriftInvalidatesPreparedPlanBeforeExecution` |
| R9 | `TestReducedContextContainsOnlyEligibleSelectedReachableClaims`; `TestContextLimitsOmitWholeUnitsAndRetainedIndexBindsAuthority` |
| R10 | `TestSourcesCannotUseNetworkCredentialsProcessesOrTools`; `TestLimitsAndCancellationReturnNoPartialState` |
| R11 | `TestOpenAPIPlanningWorkflowCompatibility`; approval/replanning binding regressions; historical CLI regression |
| R12 | `TestTypedErrorsAndTelemetryExcludeCanaries`; contract suite across source classes; systemic comment audit |

Además se ejecutan suites de `openapi`, `agentplanning`, `planapproval`,
`replanning`, `validationworkflow` y `cmd/featureproof`, detector de carreras y
coverage mínimo del 85 % del código nuevo/modificado. Cada R se trazará a tests
reales en el informe de implementación.

## Alternativas y decisiones

- Generalizar `openapi.Catalog` dentro del mismo paquete: descartado porque el
  nombre, tipos y response validator mantendrían la dependencia de formato en el
  planner.
- Convertir cada fuente directamente a bytes para el modelo: descartado porque
  impediría combinar, aplicar policy y verificar procedencia de forma
  determinista.
- Elegir automáticamente la fuente de mayor confianza: descartado porque
  ocultaría contradicciones. La policy clasifica la suficiencia; no reescribe
  hechos.
- Tratar observaciones como schemas completos: descartado porque observar un
  ejemplo no demuestra el universo contractual.
- Crear analizadores productivos ahora: descartado por alcance; la abstracción y
  los tests contractuales permiten incorporarlos después.
- Mantener simultáneamente `OpenAPI` y `OperationCatalog` en el request del
  planner: descartado porque perpetuaría dos autoridades y bindings divergentes.
- Permitir referencias HTTP para OpenAPI: descartado; viola el límite local ya
  aprobado y añadiría red a una carga que debe ser pasiva.

No se solicita excepción a la arquitectura ni a las convenciones.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T7, T10, T12 |
| R2 | T2, T3, T12 |
| R3 | T1, T2, T4, T12 |
| R4 | T1, T4, T5, T12 |
| R5 | T3, T6, T10, T12 |
| R6 | T4, T12 |
| R7 | T4, T5, T12 |
| R8 | T5, T8, T9, T12 |
| R9 | T6, T7, T12 |
| R10 | T2, T3, T4, T6, T12 |
| R11 | T7, T8, T9, T10, T11, T12 |
| R12 | T1–T12 |
