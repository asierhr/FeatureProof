# Historial y comparación de validaciones — Design

## Resumen de la solución

Se incorpora un paquete de aplicación `internal/history` que posee modelos
seguros, puertos de consulta, proyección terminal, comparación pura,
agregación y resolución autorizada de evidencia. El historial no reconstruye
la verdad desde logs ni llama al modelo: recibe terminales y relaciones ya
confirmados, comprueba sus bindings/digests y guarda una proyección append-only
tenant-scoped.

El backend local y el adapter PostgreSQL implementan el mismo contrato. Una
consulta autoriza primero, fija scope/filtros/snapshot y recorre keyset por
`terminal_at DESC, run_id DESC`. La comparación resuelve dos lados compatibles,
obtiene restricciones de proyecciones verificadas y ejecuta una función pura.
Los agregados recorren páginas internas acotadas sobre el mismo snapshot. La
evidencia solo se abre a través del lector de paquetes de Feature 15 y su estado
de disponibilidad permanece separado del resultado histórico.

## Fuentes de contexto consultadas

| Fuente | Evidencia aplicada |
|---|---|
| `AGENTS.md`, `docs/specs.md` | Flujo SDD, puerta humana, una feature y trazabilidad `R → task → test`. |
| `docs/architecture.md` | Veredicto fuera del modelo, evidencia verificable, límites, cancelación, journal append-only y arquitectura hexagonal. |
| `docs/conventions.md` | Interfaces consumer-owned, contextos, errores tipados, concurrencia, comentarios y tests deterministas. |
| `specs/persistent-control-plane/` e `internal/controlplane` | Backends local/PostgreSQL, migraciones lineales, transacciones, query keyset, cursor HMAC, retención y observabilidad content-free. |
| `specs/project-configuration/` | `ProjectKey`, binding de configuración y valores composition-owned versionados. |
| `specs/tenant-security/` e `internal/tenantsecurity` | Scope organization/project, principal/capability/RBAC, isolation compuesto, audit, retention, quotas y fallo cerrado. |
| `specs/evidence-package/` e `internal/localvalidation` | `ReadPackage`, manifest/digests, `PackageIndex`, constraints, plan bindings, evidence inventory y disponibilidad offline. |
| `specs/correction-loop/` e `internal/correction` | Lineage padre/hijo, identidad estructural de escenarios, comparison determinista y usage confirmado. |
| `internal/model`, `internal/replanning` | Calls/tokens/duración confirmados y presupuestos deliberadamente no monetarios. |
| `internal/revisionbinding`, `internal/planapproval` | Binding de repositorio/commit/entorno y aprobación ligada a hashes/plan. |

No se consultó ni se introduce un contrato web externo. Tampoco se adopta una
API de billing o pricing. Los DTO externos futuros deberán traducirse a la
atribución monetaria interna ya confirmada.

## Reglas arquitectónicas aplicadas

- `ARCH-003/004`: entry, comparación y estadística consumen veredictos y
  evidence IDs deterministas; nunca aceptan una conclusión del modelo.
- `ARCH-006`: stores, readers, comparador acotado y agregador reciben y observan
  `context.Context`.
- `ARCH-007`: terminales y correction links se proyectan append-only con
  receipts; las consultas no mutan verdad histórica.
- `ARCH-008/009`: texto externo no se interpreta y las proyecciones se
  allowlistean antes de persistir o devolver.
- `ARCH-010`: toda lógica central se prueba con stores/readers falsos, sin red,
  modelo ni PostgreSQL obligatorio.
- `ARCH-012`: los adapters devuelven datos; el caso de uso posee autorización,
  validación y composición de la respuesta.
- Tenant security se revalida en cada operación. Un cursor o handle no es una
  capability y no se usa como prueba de ownership.
- Toda entidad y función nueva o modificada, incluidos helpers de test, tendrá
  un comentario que explique su rol/invariante dentro del sistema. Los
  exportados además seguirán Go doc. Una auditoría AST cerrada del diff y una
  revisión semántica impedirán cumplirlo con comentarios mecánicos.

## Componentes y responsabilidades

### `HistoryService`

Caso de uso de entrada para `List`, `Get`, `Compare`, `Aggregate` y
`ResolveEvidence`. Recibe un `PrincipalContext` confiable, valida request y
límites, solicita una capability exacta a `HistoryAuthorizer`, y solo entonces
llama a puertos consumer-owned. Reautoriza cada request/página; no confía en el
cursor ni en la identidad enviada por un adapter.

### `TerminalProjector`

Consume un `TerminalProjectionInput` creado por la frontera que confirma el
terminal. Verifica tenant binding, revision/config/plan bindings, resultado,
timestamps, usage y digests; carga el package mediante `VerifiedPackageReader`
cuando corresponde; construye una `HistoryEntry` segura y la confirma junto a
un receipt idempotente. No ejecuta tools ni callbacks externos dentro del lock
o transacción del store.

Para PostgreSQL, la ruta productiva que ya confirma terminal/package añadirá la
entrada y receipt en la misma transacción cuando comparta database. Cuando la
fuente sea un store externo/local, persistirá primero un `projection_intent` y
reconciliará por operation ID y source digest. Nunca se reconoce el terminal
como indexado basándose solo en memoria.

### `CorrectionLinkProjector`

Añade a un run padre/hijo relaciones mínimas y selladas cuando el correction
journal las confirma. La relación contiene IDs, direction, attempt, outcome y
digest; no package path, selección sensible ni texto. Un replay idéntico es
no-op; conflicto falla. La entry base permanece inmutable y las relaciones son
un subregistro append-only.

### `HistoryStore`

Puerto definido por `history` con operaciones pequeñas:

```text
ConfirmEntry(ctx, scope, operation, entry) -> receipt
ConfirmCorrectionLink(ctx, scope, operation, link) -> receipt
GetEntry(ctx, scope, runID) -> entry
ListEntries(ctx, query, cursorState) -> page
ResolveSelector(ctx, selectorQuery) -> entry
ScanAggregate(ctx, aggregateQuery, cursorState) -> aggregatePage
GetEvidenceLocator(ctx, scope, runID, evidenceID) -> locator/tombstone
```

Implementaciones local y PostgreSQL aplican predicates organization/project en
cada key. Ninguna operación busca globalmente por `run_id`.

### `CursorCodec`

Reutiliza el patrón HMAC de control plane, con tipos propios. El payload v1
incluye organization/project, digest canónico de filtros, snapshot UTC,
`last_terminal_at`, `last_run_id`, límite/version y expiry. Se valida y
autoriza scope antes de query. Una key rota invalida cursores antiguos de forma
explícita; no hay fallback offset.

### `ComparisonResolver` y `Comparator`

El resolver valida mismo scope/repository/feature y resuelve cada selector. Un
run ID es exacto. Un commit usa el mismo filtro/snapshot y el primer terminal
por orden canónico. El comparator recibe clones de constraints acotadas y
devuelve entries ordenadas por stable key con los dos estados, clase, evidence
refs y digest.

La stable key primaria es `constraint_id`. Si existe fingerprint semántica en
ambos lados debe coincidir; ausencia histórica puede representarse como
fingerprint unavailable, pero una divergencia o duplicado no se adivina y se
clasifica `inconclusive`/integrity según afecte una entry o todo el documento.
No se reutiliza el comparator privado de correction si su semántica de
escenario no coincide; se pueden reutilizar algoritmos por composición, no
tipos que mezclen dominios.

### `AggregationService`

Itera páginas internas con el snapshot ya autorizado y límites de trabajo. Un
acumulador checked produce:

- por constraint/fingerprint: observation, passed, failed, inconclusive y
  ratio como enteros;
- duration: count/sum/min/max y percentiles con algoritmo/version de
  composición;
- stability: pares comparables, transiciones y unchanged;
- model usage por provider/model: count de runs, calls, tokens y duración solo
  cuando cada dimensión está confirmada;
- monetary attributions agrupadas por currency/source/provenance compatible.

No carga bodies de evidencia ni texto. Un aggregate result incluye snapshot,
filters digest y counts excluded/unavailable para que la ausencia no parezca
cero observado.

### `VerifiedPackageReader` y `EvidenceResolver`

Puerto estrecho adaptado a `localvalidation.ReadPackage`. Devuelve una vista
defensiva de `PackageIndex`, plan seguro e inventory verificados, nunca raw
paths al caller. `EvidenceResolver` obtiene locator bajo scope, vuelve a
autorizar, verifica package manifest/binding/evidence ID y proyecta bytes
saneados dentro del límite. Si hay tombstone devuelve `expired`; sin locator ni
tombstone, `missing`; corrupción/foreign, `invalid`.

### `CostAttributionSource`

Frontera opcional que recibe atribuciones ya confirmadas. Su modelo contiene
amount decimal canónica, currency, `source=provider|configuration`, provider,
usage digest, pricing/config revision digest y confirmation reference. El core
solo valida identidad/provenance y almacena el valor; no conoce rates ni hace
tokens×precio. Si no está configurada, el terminal se proyecta normalmente sin
coste.

### `HistoryRetentionAdapter`

Integra las entries, links, evidence locators y tombstones con el controller de
retención tenant. La policy decide periodos/floors fuera del core. Un graph
acotado impide borrar metadata referenciada por una comparación/corrección
retenida. Los deleters usan intent/fence/receipt y conservan tombstone seguro
para package/evidence expirado.

## Paquetes afectados

```text
internal/
  history/                         # nuevo dominio/casos de uso consumer-owned
    model.go
    query.go
    cursor.go
    projector.go
    compare.go
    aggregate.go
    evidence.go
    retention.go
    local_store.go
    *_test.go
  localvalidation/                 # adapter de proyección verificada acotada
  correction/                      # emisión/adaptación mínima de lineage seguro
  model/                           # adapter de usage confirmado; core sigue no monetario
  tenantsecurity/                  # catálogo cerrado de acciones/recursos si falta history
  controlplane/postgres/           # migration y HistoryStore PostgreSQL
  controlplane/contracttest/       # contrato compartido local/PostgreSQL
cmd/featureproof/                  # solo composición/superficie si la implementación
                                   # existente requiere exponer el caso de uso
```

La ubicación exacta de archivos puede ajustarse si conserva cohesión y
dirección de dependencias. No se crea `utils`, un analytics service remoto ni
una tabla de precios. Cualquier integración CLI/API debe delegar al mismo
`HistoryService` y no repetir autorización o cálculo.

## Contratos de datos

### `HistoryScope`

```text
organization_id
project_id
security_revision
policy_digest
```

Se construye desde capability viva, no desde filtros libres. Repository y
feature son filtros/resource identity dentro del scope.

### `HistoryQuery/v1`

```text
scope
repository_id?
pull_request_id?
commit_oid?
feature_id?
result?
from_utc
to_utc
limit
cursor?
```

IDs son exactos/canónicos. El periodo es `[from,to)`. El digest de filtros
incluye todos los campos normalizados, límite y versión; no incluye el secreto
HMAC.

### `HistoryEntry/v1`

```text
organization_id, project_id, repository_id, pull_request_id?, feature_id
run_id, terminal_at, started_at, duration_ns, result
revision {commit_oid, binding_digest, configuration_digest}
environment {environment_id, attestation_ref_digest?}
approved_plans[] {plan_id, plan_digest, approval_digest, steps[]}
constraints[] {constraint_id, semantic_fingerprint?, status, evidence_ids[]}
evidence_refs[] {evidence_id, package_digest, availability_hint}
correction_links[] {correction_id, parent_run_id, child_run_id, attempt, outcome, digest}
model_usage? {provider, model, calls?, tokens?, duration_ns?, usage_digest}
cost? {amount_decimal, currency, source, provenance_digest, confirmed_ref}
source_digest
```

Los arrays tienen orden canónico y límites. `approved_plans.steps` contiene
solo ID, tool/capability estable, assertion IDs/operators y dependencia; no
input/output libre. `attestation_ref` se representa solo si el contrato seguro
existente lo permite; de lo contrario se conserva su digest.

### `HistoryPage/v1`

Contiene entries detached, `snapshot_at`, `filters_digest` y `next_cursor?`.
No expone counts totales salvo que se calculen dentro de un aggregate
autorizado/acotado, para evitar queries costosas y oracles.

### `Comparison/v1`

```text
base {run_id, commit_oid, source_digest}
target {run_id, commit_oid, source_digest}
entries[] {
  stable_key, constraint_id, semantic_fingerprint?,
  base_status?, target_status?, class,
  base_evidence_ids[], target_evidence_ids[]
}
summary {corrected, persistent, new, retired, inconclusive}
digest
```

Tabla de clasificación, en orden:

1. duplicado, fingerprint conflict o cualquiera `inconclusive` →
   `inconclusive`;
2. solo target → `new`;
3. solo base → `retired`;
4. base `failed`, target `passed`, misma semántica → `corrected`;
5. cualquier otra pareja concluyente con misma semántica → `persistent`.

Así `passed -> failed` permanece visible como `persistent` con ambos estados;
la clase expresa persistencia estructural, no oculta la regresión.

### `AggregateResult/v1`

Incluye snapshot/filtros digest, runs considered, terminal counts,
constraint stats ordenados, duration stats, stability transitions, usage groups
y cost groups. Ratios son `{numerator, denominator}`, no floats. Percentiles
incluyen `algorithm` y `version`. Campos sin observaciones se omiten.

### `MoneyAttribution/v1`

`amount_decimal` usa gramática decimal canónica no negativa, precisión/largo
acotados y sin exponentes. `currency` es código canónico validado por
composición; no implica conversión. `source` es `provider|configuration` y debe
incluir digests/confirmation reference ligados al run y usage. La agregación
solo suma el mismo tuple de moneda y semantics/provenance compatible usando
decimal checked, nunca `float64`.

## Persistencia PostgreSQL

Una migration append-only posterior a `0023` añade, con nombres finales que el
implementer documentará:

- `history_entries` con PK/FK compuestas por organization/project/run,
  terminal order, filtros indexables, source digest y payload seguro acotado;
- `history_projection_receipts` para idempotencia/unknown commit;
- `history_correction_links` append-only y FKs compuestas;
- `history_evidence_tombstones`/locators tenant-scoped;
- columnas/índices para keyset por
  `(organization_id, project_id, terminal_at DESC, run_id DESC)` y filtros
  selectivos sin lookup global;
- triggers/checks compatibles N/N+1 y manifest/schema checksums.

RLS puede servir de defensa adicional, pero cada statement conserva predicates
de aplicación. Confirm entry + receipt y, cuando comparte la transacción
terminal, package/binding/terminal se confirman juntos. No se guarda un
`COUNT(*)` mutable como fuente de verdad.

El local store usa scope-derived paths, manifest/checksums, lock multiproceso,
temp+fsync+rename, receipts y páginas por keyset. No guarda cursores como
autoridad. Export/import y cutover siguen el protocolo frozen/verified/new
epoch del control plane; no existe dual-write.

## Estados y transiciones

La entry no replica la máquina completa del run:

```text
terminal_confirmed
       ↓ prepare projection / intent
projection_pending
       ↓ confirm entry + receipt
indexed
       ├─ append correction link
       ├─ package/evidence retained → expired/missing/invalid availability
       └─ metadata retention eligible → tombstoned/deleted
```

- `projection_pending` solo es un estado de reconciliación; no se expone como
  historial válido.
- `indexed` es inmutable salvo subregistros append-only (correction links,
  availability tombstones).
- Un cambio de availability nunca modifica `result`, constraints, binding,
  source digest o comparisons ya materializadas.
- Una query/comparison/aggregate es efímera; puede cachearse por scope + source
  digests, pero el cache no es fuente de verdad.

## Flujo principal de proyección

1. La frontera terminal crea operation ID y snapshot de scope/run/bindings.
2. `TerminalProjector` valida límites, timestamps, resultado y provenance.
3. Autoriza/proyecta solo el scope ya ligado al run; no acepta organization o
   project desde payload libre.
4. Para terminal funcional, abre el package por locator interno usando el
   verified reader, verifica run/binding/plan/evidence inventory y obtiene
   constraints/plan seguro.
5. Une usage confirmado y, si existe, money attribution validada.
6. Canoniza arrays, calcula source digest y confirma entry + receipt; en PG se
   une a terminal/package cuando comparten transactor o se reconcilia intent.
7. Observer recibe solo operación/categoría/duración/count acotado fuera de
   locks/transacciones.

## Flujo de listado

1. Resolver principal y capability para organization/project/action.
2. Validar filtros, periodo, límite y now con clock inyectado.
3. Primera página fija snapshot; continuación abre cursor después de autorizar
   y comprueba scope/filter/version/expiry.
4. Store aplica predicates exactos y keyset `(< terminal_at, run_id)` según
   orden descendente, toma `limit+1` acotado y devuelve detached copies.
5. Service proyecta campos allowlisted y sella siguiente cursor si procede.

## Flujo de comparación

1. Autorizar acción compare y validar dos selectores/filtros/snapshot.
2. Resolver run exacto o commit determinista; exigir mismo scope/repo/feature.
3. Cargar constraints y source digests desde entries íntegras; si un contrato
   exige semántica adicional, usar verified package reader, nunca raw artifact.
4. Comparator construye mapas acotados, detecta duplicados/fingerprint drift,
   clasifica según tabla, ordena y sella digest.
5. Devuelve comparison; no muta runs. Cache opcional solo por digests exactos.

## Flujo de agregación

1. Autorizar y validar filtros, snapshot, max runs/groups/work.
2. Leer páginas internas deterministas; observar cancelación entre rows/grupos.
3. Acumular enteros/decimales checked. Separar dimensiones ausentes de cero.
4. Calcular percentiles mediante algoritmo/version configurado sobre muestra
   acotada o estructura exacta aprobada; no aproximación silenciosa.
5. Devolver grupos ordenados, denominadores y exclusiones. Un error de
   integridad cancela todo el aggregate; no se publica parcial como completo.

## Flujo de evidencia y retención

1. Autorizar `evidence:read`/acción histórica exacta y resolver entry bajo
   scope.
2. Consultar locator/tombstone. Tombstone confirmado → `expired`; ausencia →
   `missing`.
3. Si existe locator, verified reader valida manifest, run/binding y inventory;
   el resolver proyecta solo el evidence ID pedido dentro de byte limit.
4. Corrupción/foreign → `invalid`, sin bytes parciales. Entry/result no cambian.
5. Retention controller evalúa policy/version y references, persiste intent,
   ejecuta deleter con fence y confirma tombstone/audit/receipt.

## Flujos de error

- **Sin identidad/capability o cross-tenant:** `unauthorized`/`not_found_hidden`
  antes de store/reader; sin counts.
- **Request/cursor inválido:** `invalid_request|cursor_invalid|cursor_expired`;
  no fallback a filtros vacíos.
- **Terminal/package/binding conflictivo:** `integrity_failure`; no entry
  parcial ni repair silencioso.
- **Selector sin run:** `not_found`; selector incompatible `invalid_request`.
- **Package expired/missing:** availability explícita; entry sigue válida.
- **Package corrupto:** evidence `invalid`; comparación que necesita ese
  package termina inconclusive/integrity, nunca favorable.
- **Store/provider unavailable:** categoría retryable; no cambia backend.
- **Overflow/limit:** `limit_exhausted|integrity_failure` según input legítimo o
  dato durable corrupto.
- **Cancel/deadline:** se propaga; ninguna respuesta parcial se marca complete.
- **Observer failure:** best effort, saneado y fuera de transacción; no cambia
  autoridad ni resultado.

## Propiedad del estado, concurrencia y cancelación

- El productor terminal posee su snapshot; `TerminalProjector` posee la
  canonización; `HistoryStore` posee entries/receipts/links; package store posee
  artifacts; retention posee intents/tombstones.
- CAS/unique constraints serializan un mismo run/operation. Runs/proyectos
  distintos no comparten locks globales.
- Queries trabajan sobre un snapshot lógico y copias defensivas. Inserciones
  posteriores no cambian el recorrido de un cursor.
- El comparador y acumuladores son locales a la llamada. No hay goroutine
  oculta. Si una optimización futura usa workers, el caller será propietario de
  cancel/join y la reducción conservará orden determinista.
- No se mantiene mutex/transacción durante package read, crypto, observer,
  callbacks o filesystem/network externo.
- Cada loop observa `ctx.Done()` y work budget. Solo reconciliación durable
  posterior a unknown commit puede usar un contexto acotado independiente.

## Retries e idempotencia

- Projection/correction link/retention usan operation IDs, request digests y
  receipts. Mismo ID+mismo digest reconstruye; mismo ID+otro digest conflictúa.
- PostgreSQL reintenta la transacción serializable completa solo para categorías
  transitorias y dentro de budget/deadline. Local reabre tras lock/unknown
  rename y resuelve receipt antes de repetir.
- List/Get/Compare/Aggregate son read-only y pueden reintentarse por caller con
  el mismo snapshot/cursor. Un cursor expirado no se renueva implícitamente.
- Package read corrupto no se reintenta para buscar un resultado favorable.
- Money attribution ausente no se solicita repetidamente desde el core; el
  productor/adaptador confirma o la omite.

## Seguridad y observabilidad

Eventos/logs permiten operation, category, organization/project en forma
aprobada, result bucket, page size, group count y duration. No incluyen
principal subject, cursor, run/commit/PR/feature libre, package locator,
evidence ID/value, plan input, prompt, URL, provider payload, money confirmation
secret ni errores del driver sin sanear.

Métricas previstas:

- request duration/outcome por operación y categoría cerrada;
- rows/groups procesados en buckets;
- package availability por estado cerrado;
- projection/reconcile conflicts y retries;
- retention deletes/tombstones por resource class;
- ninguna métrica analítica es fuente autoritativa del aggregate.

Las decisiones allow/deny, projection, retention y evidence access generan
audit durable mediante la infraestructura de tenant security. Audit failure en
una acción sensible falla cerrado según el contrato existente.

## Estrategia de pruebas

### Unitarias

- validación/canonización/defensive copies de entry, query, money y usage;
- cursor scope/filter/snapshot/order/tamper/expiry;
- tabla exhaustiva del comparator y determinismo por permutación;
- accumulators checked, ratios, stability, percentiles y currency separation;
- resolver de availability y mapping de errores;
- AST comment audit sobre todo Go nuevo/modificado.

### Contract suite local/PostgreSQL

El mismo fixture cubre confirm/replay/conflict, restart, same IDs en tenants,
filtros combinados, keyset con timestamps iguales, inserción entre páginas,
selector por commit, correction links, aggregate, retention/tombstone y
cancelación. PostgreSQL 16 se ejecuta opt-in con migrations empty/latest,
concurrent/dirty/future, N/N+1, normal/race y cleanup.

### Verticales de aplicación

- principal autorizado lista/get/compare/aggregate/evidence;
- deny/cross-tenant/cursor spoof no alcanza stores/readers;
- terminal + package + approval + usage + correction produce entry completa;
- package expired/missing/corrupt no cambia el resultado histórico;
- provider usage sin cost, provider-confirmed cost y config-confirmed cost;
- regression `passed -> failed` visible dentro de persistent;
- concurrent projection/retention/query y unknown commit convergen.

### Gates

Se ejecutan `docs/verification.md`, normal/race global, focal race repetida,
builds aplicables, PostgreSQL real opt-in, migraciones, coverage diferencial y
auditorías de secrets/comentarios/temporales. Cada R se enlaza a nombres de
test en el informe del implementer.

## Alternativas y decisiones

- **Consultar journals/packages en cada request:** descartado; es costoso,
  mezcla backends y hace paginación/retención no estable. La proyección
  inmutable conserva provenance.
- **Copiar bodies de evidencia al índice:** descartado; amplía exposición y
  rompe la autoridad del verified reader.
- **Offset pagination:** descartado; inserciones concurrentes duplican/omiten y
  facilita scans no acotados.
- **Usar texto/modelo para comparar o rankear:** descartado; viola
  determinismo y fuente de verdad.
- **Clasificar `passed -> failed` como inconclusive:** descartado; ambos estados
  son concluyentes. Se conserva como persistent con estados visibles.
- **Tabla de precios en model/runtime:** descartado; core continúa
  provider-neutral y no monetario. Solo acepta atribución confirmada.
- **Tratar package ausente como cambio de verdict:** descartado; availability y
  truth histórica son dimensiones distintas.
- **Un store analytics separado o microservicio:** descartado; el monolito
  modular y adapters existentes bastan para esta entrega.
- **Cursor reutilizable entre filtros/scopes:** descartado por seguridad e
  integridad del snapshot.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Tests previstos |
|---|---|---|
| R1 | HistoryService/authorizer | `TestHistoryAuthorizesBeforeStore`; `TestHistoryCrossTenantIsHidden`; `TestHistoryReauthorizesContinuation` |
| R2 | query validator/store | `TestHistoryFiltersExactDimensionsAndUTCPeriod`; `TestHistoryRejectsInvalidBoundsBeforeQuery` |
| R3 | TerminalProjector/store | `TestTerminalProjectionIsCompleteAndIdempotent`; `TestOperationalTerminalDoesNotInventFacts`; `TestCorrectionLinkIsAppendOnly` |
| R4 | projector/package adapter | `TestHistoryUsesDeterministicFactsOverNarrative`; `TestHistoryProjectionRedactsCanaries`; `TestHistoryRejectsBrokenProvenance` |
| R5 | cursor/list store | `TestHistoryKeysetIsStableAcrossConcurrentInsert`; `TestHistoryCursorBindsScopeFiltersAndExpiry`; shared store contract |
| R6 | ComparisonResolver | `TestComparisonResolvesRunExactlyAndCommitCanonically`; `TestComparisonRejectsIncompatibleSides` |
| R7 | Comparator | `TestComparisonClassificationTable`; `TestComparisonPermutationHasStableDigest`; `TestComparisonMakesRegressionVisible` |
| R8 | AggregationService | `TestAggregateCountsFailuresDurationAndStability`; `TestAggregateExcludesUnretainedAndUnauthorizedData`; `TestAggregateEmptyAndOverflow` |
| R9 | usage projector/aggregate | `TestHistoryUsagePreservesConfirmedDimensions`; `TestHistoryUsageNeverInfersMissingValues`; `TestUsageGroupsProviderAndModel` |
| R10 | money model/source | `TestHistoryCostIsOptionalAndVerified`; `TestCostGroupsNeverMixCurrency`; `TestCoreContainsNoEmbeddedPricing` |
| R11 | EvidenceResolver | `TestEvidenceUsesVerifiedPackageReader`; `TestEvidenceAvailabilityDoesNotChangeHistoricalResult`; `TestForeignOrCorruptEvidenceReturnsNoBytes` |
| R12 | retention adapter | `TestHistoryRetentionProtectsLiveReferences`; `TestExpiredPackageLeavesEntryAndTombstone`; `TestRetentionUnknownCommitConverges` |
| R13 | all services | `TestHistoryLimitsNAndNPlusOne`; `TestHistoryCancellationReturnsNoCompletePartial`; `TestIndependentHistoryQueriesProgress` |
| R14 | local/PG stores/migration | `RunHistoryStoreContract`; `TestPostgresHistoryMigrationNNPlusOne`; `TestHistoryBackendFailureHasNoFallback` |
| R15 | errors/observer | `TestHistoryErrorsAreTypedAndRedacted`; `TestHistoryObserverFailureCannotAffectResult`; `TestHistoryMetricsUseBoundedLabels` |
| R16 | suite/gates/comments | `TestHistoryFullFlowHermetic`; `TestHistorySourceCommentsDescribeSystemRole`; verification report and PostgreSQL gate |

## Convención de comentarios para la implementación

Esta feature hace vinculante la convención solicitada para su diff:

1. toda declaración de tipo/entidad, función, método y helper de test nuevo o
   modificado recibe un comentario sobre la función que cumple en FeatureProof,
   la frontera que protege o el invariante que mantiene;
2. los símbolos exportados comienzan además con el nombre correcto según Go
   doc;
3. no cuentan comentarios que solo repitan el identificador (`X hace X`), ni
   comentarios generados sin significado;
4. una auditoría AST compara el inventario de declaraciones del diff con
   comentarios asociados; el reviewer realiza revisión semántica de una
   muestra/todo el diff y puede rechazar comentarios mecánicos;
5. el informe `progress/execution-history/implementation.md` registra el
   inventario y resultado de esta auditoría junto con la trazabilidad.
