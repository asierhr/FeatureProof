# Configuración versionada por proyecto — Design

## Resumen de la solución

La feature introduce `internal/projectconfig` como propietario del documento,
su validación, las versiones inmutables, el head activo y el binding de run. La
composición entrega dos snapshots confiables —policy organizativa y catálogo de
recursos— y una authority ya acotada; el dominio no deriva roles, membresías ni
secretos. Publicar crea versión, activación, head, evento y receipt en una única
frontera atómica. Rollback añade otra activación hacia una versión histórica.

Toda admisión resuelve el head, revalida sus dependencias y persiste el snapshot
efectivo junto al run antes de crear trabajo. Desde entonces el run usa su
binding, incluso si cambian configuración, catálogo o defaults.

```text
trusted identity ──► scoped authority
                            │
candidate JSON ──► strict loader ──► structural validator
                                             │
organization policy snapshot ────────────────┤
resource catalog snapshot ───────────────────┤
                                             ▼
                                  effective configuration
                                             │
                      publish ── atomic version/activation/head/event
                                             │
new admission ── atomic head resolution + run binding + causal run/job
                                             │
existing run ───────────────────────► immutable bound snapshot
```

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md` | SDD, una sola feature, puerta humana, outputs permitidos y trazabilidad. |
| `docs/architecture.md`, `docs/conventions.md`, `docs/verification.md` | Puertos consumer-owned, inputs no confiables, límites, cancelación, errores tipados y gates. |
| `feature_list.json` | Acceptance de feature 36 y frontera explícita con feature 37. |
| Spec 35 y `internal/controlplane` | PostgreSQL como source of truth, transacciones causales, epochs, migrations, local explícito y no fallback. |
| `cmd/featureproof/workflow.go` | `workflowConfig` actual mezcla bootstrap privado con opciones que deben pasar a configuración versionada. |
| `internal/validationworkflow` | Comando de admisión, creación del run y puntos donde debe fijarse el binding. |
| `internal/specification`, `internal/contractcatalog` | Specs y contratos versionados/digestados que alimentan el catálogo. |
| `internal/environment`, `internal/testidentity`, `internal/fixture` | Providers/targets, identities y fixtures referenciables y sus compatibilidades. |
| `internal/planapproval`, policy/limits/retention existentes | Consumers de approval, límites y retención efectivos. |
| Adapters CLI, API/MCP y `internal/githubpr` | Superficies que deben converger en una única resolución fail-closed. |

## Convención obligatoria de comentarios

Toda entidad o tipo, función, método y helper de test nuevo o modificado tendrá
un comentario breve sobre su responsabilidad sistémica, frontera o invariante.
También lo tendrán constantes y variables de paquete cuyo papel compartido no
sea evidente. Incluye símbolos privados, errores, descriptors de migración,
fakes, factories, fixtures y assertion helpers.

No se comentan mecánicamente locales, parámetros ni receivers triviales. Cuando
materialicen autoridad, fencing, durabilidad u ownership ocultos, se expresará
con un tipo u operación explícitos o con un comentario puntual. La revisión
rechazará paráfrasis, boilerplate y familias generadas por plantilla.

## Reglas arquitectónicas aplicadas

- El documento, payload externo y referencias son no confiables hasta completar
  strict decode, validación estructural y validación semántica.
- `projectconfig` posee sus puertos; adapters de catálogo, policy y store no
  introducen dependencias inversas hacia composición.
- Authorization llega como capability scoped. Esta feature no interpreta roles
  ni administra tenants.
- Ningún valor secreto atraviesa loader, validator, store, binding, query,
  export, logs o eventos; solo referencias opacas.
- Las operaciones tienen límites de bytes, cardinalidad, páginas, retries,
  transacciones y tiempo, y propagan `context.Context`.
- El estado causal durable precede a jobs o efectos. Observabilidad no es source
  of truth.
- Local y PostgreSQL comparten contrato observable, pero se selecciona un solo
  backend; nunca existe dual-write o fallback.

No se propone ninguna excepción a arquitectura o convenciones.

## Áreas y ownership

| Área | Responsabilidad |
|---|---|
| `internal/projectconfig` | Modelo, strict loader, canonicalización, validator, policy merger, service, errors y puertos consumer-owned. |
| `internal/projectconfig/local` | Store local durable/atómico para desarrollo y contract suite. |
| `internal/projectconfig/postgres` | Adapter PostgreSQL sobre infraestructura/migrator del control plane. |
| `internal/validationworkflow` | Exigir `RunConfigurationBinding` al admitir y recuperar runs. |
| `internal/controlplane` | Unidad causal PostgreSQL que persiste binding, run, event y first job. |
| `cmd/featureproof` | Composición de authorities/snapshots/adapters; comandos de administración y reducción de `workflowConfig`. |
| CLI, API/MCP, `internal/githubpr` | Traducir identidad confiable a project key y llamar al mismo service. |

`workflowConfig` conservará solo bootstrap de despliegue: selección de backend,
DSN/handles, ubicaciones privadas de resolvers, trust roots y wiring. Selección
de contratos, environments, identities, fixtures, runtime/model profiles,
approval, límites y retención procede exclusivamente de la configuración activa.

## Modelo de dominio

- `ProjectKey`: `(ProjectID, RepositoryID)` canónico e inmutable.
- `Document`: schema, project key y selecciones/referencias tipadas.
- `ResourceReference`: kind, ID, versión y expected digest; no contiene payload
  mutable ni credenciales.
- `ProjectConstraints`: límites, allowlists, approval y retention solicitados.
- `ConfigurationVersion`: número monotónico, bytes canónicos, document digest,
  policy/catalog digests usados al publicar, actor y timestamps informativos.
- `Activation`: generación monotónica, target version/digest, reason
  `publish|rollback`, expected previous generation y digest.
- `ConfigurationHead`: proyección CAS hacia una activation.
- `OrganizationPolicySnapshot`: versión/digest, defaults, ceilings, floors y
  allowlists emitidos por fuente confiable.
- `ResourceCatalogSnapshot`: digest y descriptors seguros/versionados de
  recursos disponibles y relaciones de compatibilidad.
- `EffectiveConfiguration`: documento cerrado tras aplicar policy y materializar
  referencias seguras; canónico y digestado.
- `RunConfigurationBinding`: project key y digests/versiones de activation,
  config, policy, catálogo y effective snapshot.
- `ConfigurationAuthority`: capability opaca creada por composición para actor,
  project key y acción exacta.

Constructores validan tamaños/formato y copian slices/maps. Entidades devueltas
no comparten memoria mutable con store o caller. Versiones, generaciones y
sequences fallan `exhausted` antes de wrap.

## Documento y canonicalización

El formato publicado es JSON `featureproof.project-config/v1`. El loader usa un
reader acotado, rechaza unknown/duplicate fields, trailing data y nulls no
permitidos. La normalización ordena mapas y conjuntos semánticos, conserva el
orden solo donde sea significativo y emite una representación canónica única.
SHA-256 se calcula sobre esos bytes, nunca sobre el input original.

La estructura separa referencias por kind y explicita todos los defaults
project-owned; no existen campos abiertos que puedan convertirse en autoridad.
Un adapter YAML futuro podría producir el mismo JSON canónico, pero queda fuera.

## Catálogo de recursos y validación semántica

`ResourceCatalog.LoadSnapshot(ctx, projectKey)` devuelve una vista única,
acotada y digestada. Adapters sobre contract catalog, environment providers,
test identities, fixture registry, runtime/model/auth profiles y policies
traducen únicamente metadata segura. La configuración presenta expected digest
para referencias mutables/versionadas.

El validator ejecuta en orden determinista:

1. schema, scope, formatos, cardinalidades y unicidad;
2. existencia, kind, scope, versión y digest de cada referencia;
3. relaciones cruzadas: operaciones de fixture presentes en contratos,
   identities compatibles, provider/target/profile admitidos y policy aplicable;
4. merge con organization policy y generación del snapshot efectivo.

No llama a secret resolvers. Los diagnostics contienen path y categoría
allowlisted, nunca raw collaborator errors o valores.

## Defaults y restricciones organizativas

`OrganizationPolicySource.LoadSnapshot` entrega una policy versionada y
digestada para el project key. La implementación de feature 36 no conoce cómo
se determinó pertenencia o rol. El merge es monotónico:

- ausencia usa un default explícito; ausencia de policy bloquea;
- límites numéricos usan el valor de proyecto solo si no supera el ceiling;
- approval `required` domina y el proyecto puede ser más estricto;
- retention debe respetar el intervalo organizativo declarado;
- allowlists se validan por subconjunto/intersección, nunca por unión;
- capacidades no declaradas no se conceden por inferencia.

El policy digest entra en effective digest y binding. Cambiar defaults afecta
solo admisiones nuevas.

## Puertos consumer-owned

```text
Store
  PublishAndActivate(command) -> PublishReceipt
  ActivateHistorical(command) -> ActivationReceipt
  LoadHead(project key) -> HeadSnapshot
  LoadVersion(project key, version) -> ConfigurationVersion
  ListHistory(project key, cursor, limit) -> HistoryPage
  ResolveAndBind(admission identity, expected head, snapshots) -> RunBindingReceipt
  LoadRunBinding(run identity) -> RunConfigurationBinding

ResourceCatalog
  LoadSnapshot(project key) -> ResourceCatalogSnapshot

OrganizationPolicySource
  LoadSnapshot(project key) -> OrganizationPolicySnapshot

Observer
  Observe(bounded metadata) // best effort, nunca autoridad
```

El store recibe comandos ya canonicalizados/validados y aun verifica scope,
expected generation, digests e idempotency. No recibe callbacks que hagan I/O
dentro de locks o transacciones.

## Application service y authority

`Service.Validate`, `PublishAndActivate`, `Rollback`, `GetActive`, `GetVersion`,
`ListHistory`, `Export` y `ResolveForAdmission` exigen authority antes de tocar
stores/catálogos. La authority se construye en composición desde la identidad
autenticada y no puede reconstruirse desde IDs del payload.

Feature 37 podrá implementar el factory con RBAC/tenant isolation. El contrato
de feature 36 solo comprueba action y project key scoped; no define roles,
miembros, service accounts, tenant quotas ni administración organizativa.

## Publicación, activación e idempotencia

Publicar sigue este protocolo:

1. comprobar authority y límites de comando;
2. strict load/canonicalize del candidate;
3. obtener policy/catalog snapshots fuera de la transacción;
4. validar y construir effective snapshot;
5. abrir transacción corta, verificar expected head y que policy/catalog siguen
   siendo los digests fijados;
6. verificar/reservar operation receipt;
7. insertar version, activation, effective snapshot, event y receipt, y avanzar
   head por CAS;
8. commit; después emitir observabilidad best effort.

Misma operation key y digest devuelve el receipt previo. Misma key y otro
digest es conflict. Ante commit desconocido, el retry resuelve receipt/head. Los
retries por serialization/deadlock repiten la transacción completa con budget,
sin repetir authorization ni I/O externo dentro de ella.

## Rollback

Rollback carga la versión histórica, obtiene policy/catalog actuales y la
revalida. Si continúa compatible, una transacción CAS crea otra activation con
reason `rollback`, actualiza head y añade evento/receipt. La versión no se copia
ni modifica. Incompatibilidad, corrupción o stale head dejan el head intacto.

## Binding de admisión y compatibilidad

`ResolveForAdmission` no devuelve meras opciones mutables. Captura una única
generación activa, carga versión/effective snapshot, comprueba schema support,
policy/catalog digests actuales y produce `RunConfigurationBinding`.

En PostgreSQL, binding + run + primer event/job se confirman en la misma unidad
causal del control plane. En local, una publicación atómica equivalente usa
locks/rename/fsync y receipts para que recovery observe ambos o ninguno. Una
run identity ya bound converge si todos los digests coinciden y falla conflict
en caso contrario.

Una vez admitido, planning, execution, evaluation, publication, cleanup y
recovery cargan el binding del run; no consultan el active head. Drift o retirada
de recursos bloquea runs nuevos. Un run previo conserva selección; si su secret
ref fue revocada, falla en resolución privada sin fallback o rebinding.

## Store local

El adapter local usa un root explícito bajo el workspace autorizado, con
subdirectorios por project/repository derivados de IDs validados, no paths del
documento. Mantiene manifests/versiones/events/receipts/bindings checksummed,
lock de proceso, escrituras temporales, fsync y rename atómico. Recovery valida
digests y no repara silenciosamente corrupción. Un segundo proceso observa CAS
y receipts equivalentes al adapter PostgreSQL.

## Persistencia PostgreSQL

Las migrations de control plane añaden lógicamente:

| Tabla | Invariante |
|---|---|
| `project_configuration_versions` | `(project_id, repository_id, version)` único; canonical bytes/digests inmutables. |
| `project_configuration_activations` | generation append-only, version FK, reason y previous generation. |
| `project_configuration_heads` | una fila CAS por project key apuntando a activation. |
| `project_configuration_effective_snapshots` | snapshot seguro/digestado ligado a version+policy+catalog. |
| `project_configuration_events` | journal causal append-only sin raw input ni secrets. |
| `project_configuration_operation_receipts` | idempotency key, command digest y receipt durable. |
| `run_configuration_bindings` | una fila inmutable por run, con FKs/digests/snapshot efectivo. |

Constraints preservan scope, monotonicidad, relaciones y estados. La aplicación
soporta la ventana N/N+1 y migrations numeradas/checksummed de feature 35. No se
añade un repositorio SQL genérico ni se usa `jsonb` como sustituto de invariantes.

## Consulta, export y paginación

Queries vuelven a comprobar authority y devuelven copias acotadas del documento
canónico o snapshot efectivo. Los opaque secret reference IDs son exportables;
ningún resolver o valor lo es. History usa keyset cursor sellado con version,
project key, filtros, boundary y last ordering key. Un cursor cross-project o
alterado se rechaza antes de leer datos útiles.

## Integración de superficies

- Administración: CLI `config validate/publish/rollback/show/history/export`
  (nombres finales pueden adaptarse al estilo existente) llama al mismo service.
- CLI/API/MCP/GitHub de ejecución derivan project key desde identidad confiable,
  nunca desde una config arbitraria presentada junto a la solicitud.
- Aliases de run solo pueden escoger dentro de selecciones ya materializadas;
  no introducen recursos, secret refs o profiles.
- `validationworkflow.StartCommand` recibe el binding o un handle sellado hacia
  él; no vuelve a aceptar defaults productivos dispersos.
- Controllers existentes consumen limits/approval/retention del binding.

## Cancelación, concurrencia y errores

Categorías cerradas: `invalid`, `unauthorized`, `not_found`, `conflict`,
`stale_head`, `missing_reference`, `incompatible_reference`, `integrity`,
`unsupported`, `obsolete`, `cancelled`, `deadline`, `unavailable` y `exhausted`.
Adapters clasifican por errores tipados/SQLSTATE, no texto.

Cada espera y operación propaga context. Transactions son breves; ningún
resolver, callback, filesystem externo, red, observer o secret operation ocurre
dentro. Competing publish/rollback usa CAS sobre generation. Overflow, retry
budget agotado o snapshot drift falla cerrado.

## Auditoría y observabilidad

Publish/activation/rollback escriben su evento causal dentro del mismo commit.
Incluye actor seguro, project key, action, versions/digests, operation ID,
resultado y reason estable. Rechazos y queries emiten solo metadata allowlisted.
No se registran canonical bytes, raw documents, DSN, URLs target, collaborator
errors o credenciales. Un observer fallido después del commit no altera receipt;
un event durable fallido impide el commit.

## Estrategia de pruebas

### Unitarias herméticas

- strict JSON, duplicate/unknown fields, bounds, canonicalization y digests;
- validator estructural/semántico y diagnostics deterministas;
- matriz de compatibilidad de contratos/fixtures/identities/providers/profiles;
- property/table tests del merge monotónico de policy;
- authority cross-scope/action y ausencia de store calls;
- publish/rollback/idempotency/stale head/overflow/cancelación con fakes;
- no-secret canaries en errores, events, export y telemetry;
- run binding, drift, old-run recovery y no rebinding;
- cursor sealing y copias defensivas.

### Contract suite local/PostgreSQL

La misma suite comprueba immutable versions, atomic head, concurrent publishers,
rollback, restart, corruption/integrity, receipts, unknown commit, pagination y
run binding. Interleavings se coordinan con barreras/canales, nunca `time.Sleep`.

### Gate PostgreSQL 16 opt-in

El harness de feature 35 recibe un DSN explícito de test, crea un namespace UUID
propio, aplica migrations y elimina solo sus recursos. Cubre migrate empty/latest/
concurrent, N/N+1, rollback por boundary, serialization retry, publish contra
rollback, binding+run+job causal, crash/restart y no fallback. Sin DSN el gate
no cuenta como aprobado; `go test ./...` permanece sin red ni secretos.

## Alternativas consideradas

- **Config mutable por project:** descartada; impediría reproducir runs y haría
  rollback/attribution ambiguos.
- **Copiar una versión en rollback:** descartado; duplica identidad y oculta que
  se seleccionó una versión histórica. Se crea activation nueva.
- **Releer active config en cada fase:** descartado; mezclaría generaciones y
  rompería runs ante cambios posteriores.
- **Organization defaults embebidos:** descartado; podrían ensanchar authority
  y separar despliegues. Deben llegar como snapshot explícito.
- **Guardar secretos cifrados en config:** descartado; encryption/rotation son
  feature 37 y ampliarían exposición. Solo refs opacas.
- **Config por flags con fallback:** descartada; crea dos fuentes de verdad. Los
  flags administrativos no sustituyen el head.
- **Un store SQL genérico:** descartado; diluye ownership e invariantes.

## Frontera explícita con feature 37 `tenant-security`

Feature 36 define scope por `ProjectKey`, authority capability, snapshots de
policy y secret reference IDs. No define ni persiste organizations, membership,
roles, service accounts, tenant identity, RBAC evaluation, cross-tenant
isolation, encryption/rotation/revocation de secrets o tenant quotas. Feature 37
podrá producir authorities y organization policy snapshots más fuertes sin
cambiar la semántica de versión, activation o run binding aquí descrita.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | authority, service, ingress adapters | action/scope matrix, no store call, cross-project |
| R2 | loader/canonicalizer | strict decode, equivalence, digest/property tests, bounds |
| R3 | structural validator | formats/cardinality/cycles/deterministic diagnostics |
| R4 | resource catalog/semantic validator | missing/stale/kind/scope/digest/compatibility/drift |
| R5 | document model/query/export | forbidden fields and secret canaries across boundaries |
| R6 | policy merger | defaults, ceilings, approval floor, retention range, allowlist subset |
| R7 | store/version/event | immutability, monotonic history, tamper/restart |
| R8 | publish transaction | concurrent CAS, idempotency, fault injection, unknown commit |
| R9 | rollback service | historical compatibility, new generation, concurrency/redelivery |
| R10 | query/history/export | cursor scope/tamper, defensive copies, bounded responses |
| R11 | admission resolver/store | atomic binding, redelivery/conflict, later head changes |
| R12 | all ingress/recovery | absent/obsolete/drift fail-closed and old run continuity |
| R13 | local/PostgreSQL adapters | shared contract suite, restart, outage, migrations |
| R14 | service/adapters | cancellation, tx retries, overflow and typed error matrix |
| R15 | events/observer | transactional audit and non-sensitive telemetry canaries |
| R16 | CLI/API/MCP/GitHub/workflow | vertical admission and no override/default fallback |
| R17 | complete diff | static inventory plus manual systemic-comment audit |
| R18 | verification harness | hermetic/race/cross-build/coverage and PostgreSQL gate |

## Decisiones cerradas

- JSON v1 y SHA-256 canónico son la identidad publicada.
- Rollback crea activation, no version.
- Catalog y organization policy son snapshots confiables provistos por
  composición; su administración no pertenece a esta feature.
- Toda nueva admisión, incluido local, requiere configuración activa válida.
- Local y PostgreSQL son alternativas exclusivas con el mismo port observable.
- Runs existentes usan exclusivamente su binding inmutable.

No quedan decisiones abiertas que impidan implementar tras aprobación humana.
