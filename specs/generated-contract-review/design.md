# Revisión y exportación de contratos derivados — Design

## Resumen de la solución

Se añadirá un paquete cohesionado `internal/contractreview` que será dueño del
modelo de review, el diff, las decisiones, la proyección aprobada y los contratos
de persistencia/publicación. Recibirá `contractcatalog.OperationCatalog`
sellados, sin conocer parsers Go/Spring/OpenAPI ni resolver repositorios.

El flujo separa tres identidades: el artifact inmutable, el decision set
revisionado y el approval binding resultante. Esta separación permite cambiar
una decisión sin reescribir la evidencia revisada y hace que cualquier cambio
invalide el approval anterior de forma comprobable.

```text
approved baseline + derived candidate + trusted binding
                         │
                         ▼
               canonical structural differ
                         │
                  ReviewArtifact/v1
                         │ durable create
                         ▼
              authorization + CAS decision
                  ├─ reject_all ──> rejected
                  ├─ pending ─────> partially_decided
                  └─ complete decisions
                         │
                         ▼
             ApprovedProjection + ApprovalBinding
                  ├─ verified ContractSource
                  └─ OpenAPI 3.1 exporter
                         │ pre-I/O revalidation
                         ▼
             atomic local publication + audit event
```

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md` | Feature única, tres artefactos SDD, puerta humana y estados gestionados por leader. |
| `docs/architecture.md` | El agente no concede autoridad; datos fuente son inertes; default deny, límites, evidencia y adapters sustituibles. |
| `docs/conventions.md`, `docs/verification.md` | Interfaces consumidoras, errores tipados, contextos, CAS, race, coverage, trazabilidad y comentarios sistémicos. |
| `specs/contract-source-abstraction` e `internal/contractcatalog` | Modelo neutral v1, claims, provenance, confianza, completitud, conflicts, omissions, policy y digest canónico. |
| `specs/go-backend-inspector`, `specs/spring-boot-backend-inspector` y sus informes/reviews | Inputs derivados pasivos, analyzer versions, repository/revision, diagnósticos, source sets y limitaciones demostrables. |
| `internal/openapi` | Adaptación authoritative existente, subset estructural soportado, schemas, seguridad, responses y restricciones de referencias. |
| `internal/validationworkflow`, `internal/githubpr`, `internal/externalagent` y `cmd/featureproof` | Servicio compartido por CLI/API/MCP/GitHub, stores locales, revalidación durable, autoridad GitHub y composición sin credenciales en core. |

No se consultan contratos externos en red. OpenAPI 3.1 se trata mediante el
contrato ya soportado en el repositorio y una proyección propia; no se copian
reglas o tipos de una librería externa al dominio.

## Paquetes y áreas afectadas

- `internal/contractreview`: dominio/application de review, diff, proyección,
  service, errores, límites, eventos y puertos consumidores.
- `internal/contractreview` o archivos adapter separados dentro del paquete:
  store local íntegro y publisher local atómico. Si crecen, podrán separarse en
  paquetes adapter sin mover invariantes fuera del dominio.
- `internal/contractcatalog`: mínima extensión para importar una proyección
  aprobada preservando provenance derivada y provenance de review; no se cambia
  el significado de fuentes existentes.
- `internal/openapi`: proyección acotada OpenAPI 3.1 y validación del documento
  exportado; el loader actual no se convierte en store de reviews.
- `internal/validationworkflow`: revalidación de un approval binding antes de
  admitir un contrato aprobado y marca proposal-only para derivados pendientes.
- `internal/githubpr`: adapter de comandos/estado de review reutilizando su
  snapshot autenticado, delivery idempotency y publisher seguro.
- `cmd/featureproof`: composición, configuración confiable y comandos CLI; no
  contiene reglas de diff, decisión o export.

## Componentes y responsabilidades

### Modelo de review

`ReviewArtifact` contiene versión, ID/digest, repository, commit, scope,
candidate identity, baseline identity, analyzer/source manifests, límites,
conteos y `ReviewItem` canónicos. Sus colecciones se copian profundamente.

`ReviewItem` tiene ID estable, categoría, operation key, structural path,
fingerprints before/after, cause codes y provenance acotada. El ID se deriva de
la versión del algoritmo, artifact inputs y contenido canónico del item; no usa
índice de slice ni texto libre.

`DecisionRevision` contiene revisión CAS, actor autorizado, instante confiable,
acción, IDs objetivo, idempotency identity y digest. Los eventos nunca alteran
el `ReviewArtifact`.

`ApprovalBinding` cubre al menos:

- review ID/digest y decision revision/digest;
- candidate y baseline catalog/version/digest;
- repository ID, full commit y comparison scope;
- cada source ID/digest/class/repository/revision/adapter version;
- analyzer IDs y versiones exactas;
- projection digest y policy/config digest relevante.

### Baseline verifier

Una interfaz consumidora resuelve la baseline seleccionada por identidad opaca
y devuelve un snapshot verificado. Acepta una fuente OpenAPI authoritative ya
aprobada o una `ApprovedProjection` anterior. Comprueba repository, scope,
versiones y digest antes de entregar el catálogo al differ. No lee paths ni
elige “la última” baseline.

### Structural differ

El differ recorre colecciones canónicas con checkpoints de contexto y límites.
Compara en este orden:

1. operation key;
2. metadata de operación y logical ID;
3. parámetros por `(in,name)` y request body por media type;
4. schema mediante structural paths, cycle guards y fingerprints;
5. responses por selector, header y media type;
6. security requirements como alternativas OR de conjuntos AND canónicos;
7. declarative constraints, provenance, confianza y completitud.

Una presencia/ausencia solo produce added/removed si el lado correspondiente
demuestra exhaustividad para ese scope. Conflicts, diagnostics, partialidad y
omissions aplicables crean `uncertain`. El algoritmo no decide qué versión es
correcta.

### Decision reducer y approved projection

El reducer valida whole-action o conjunto exacto de IDs y aplica decisiones
sobre una copia de baseline:

- accepted added/modified reemplaza exactamente la unidad seleccionada;
- accepted removed elimina exactamente la unidad;
- rejected/pending conserva baseline;
- unchanged conserva baseline/candidate equivalente;
- accepted uncertain conserva causes/confidence/completeness.

La selección no puede dejar estructuras inválidas: si un item hijo depende de
un cambio padre, el artifact declara un grupo atómico o dependencias explícitas
y el reducer rechaza conjuntos no cerrados. Cuando todos los items están
decididos, calcula la proyección y el approval binding. `reject_all` no produce
proyección exportable. Si todas las decisiones selectivas son reject, se
conserva baseline como resultado pero el candidate continúa rechazado.

### Review service

El servicio expone operaciones conceptuales:

```text
Create(ctx, CreateCommand) -> ReviewView
Get(ctx, principal, reviewID) -> ReviewView
Decide(ctx, DecisionCommand) -> ReviewView
Export(ctx, ExportCommand) -> ExportReceipt
ResolveApproved(ctx, ApprovedReference) -> SourceSnapshot
```

Cada comando valida principal, IDs, versión, expected revision, idempotency key
y binding antes de mutar. `Create`, `Decide` y `Export` usan interfaces pequeñas
de autorización, resolución de inputs, clock, store y publisher. El servicio no
recibe secretos ni paths de repositorio sin validar.

### Store local durable

El store vive bajo `.featureproof/contract-reviews` y persiste un checkpoint
canónico más journal append-only íntegro. Reserva idempotency identity antes del
efecto, usa CAS interproceso por revisión, publica mediante archivo temporal en
el mismo directorio y rename, y valida versión/digest al recuperar. Rechaza
symlinks, reparse points, non-regular files, escapes y checkpoints manipulados.
No mantiene locks durante resolvers, autorización ni publisher callbacks.

### Approved contract source

Un adapter convierte solo una aprobación vigente en `SourceSnapshot`. La
provenance de review aporta autoridad humana y enlaza el approval digest; cada
claim conserva también sus procedencias derivadas originales. La clase original
no se reescribe retrospectivamente. La policy distingue:

- catálogo derived pendiente: proposal-only si está permitido;
- proyección aprobada vigente: usable como autoridad humana para hechos
  presentes y completos;
- hecho partial/unknown: no demuestra ausencias aunque esté aprobado;
- rejected/invalidated: sin autoridad nueva.

`validationworkflow` solicita revalidación antes de aprobación del plan y otra
vez inmediatamente antes de ejecución, siguiendo el patrón durable existente.

### OpenAPI 3.1 exporter

El exporter construye un DTO propio y cerrado con `openapi: "3.1.0"`, `info`,
`paths` y `components.securitySchemes/schemas` alcanzables. Serializa JSON
canónico. No copia descriptions, examples ni extensiones arbitrarias de las
fuentes.

Cada unidad material incluye, cuando corresponda:

- `x-featureproof-provenance`;
- `x-featureproof-source-classes`;
- `x-featureproof-confidence`;
- `x-featureproof-completeness`;
- `x-featureproof-review` con review/approval/projection digests;
- `x-featureproof-omissions` y conflicts estructurales.

Schemas se traducen al subset JSON Schema de OpenAPI 3.1. `nullable` se expresa
solo cuando la unión con `null` es exacta; construcciones no expresables se
omiten por unidad completa o conservan su parte demostrable con omission code.
El documento resultante se valida con el loader/validador local antes de
publicarlo.

### Atomic publisher

El publisher recibe root autorizado, path relativo canónico, bytes, expected
state y contexto. Para crear exige target ausente. Para reemplazar exige
`replace=true` y SHA-256 exacto del archivo regular existente. Revalida parent y
target antes de temp, antes de rename y después de adquirir el lock/CAS de
publicación; escribe, sincroniza, renombra y sincroniza directorio. Registra el
receipt solo después del commit atómico.

### Adapters CLI, API, MCP y GitHub

Todos proyectan al mismo servicio:

- CLI: `contract-review create|show|decide|export` con aliases configurados, IDs,
  expected revision e idempotency key; export exige path explícito y flags de
  replacement/digest.
- API: DTO versionado equivalente, principal desde autenticación y status codes
  derivados de categorías cerradas.
- MCP: tools versionadas con schemas cerrados; nunca acepta catálogo crudo,
  autoridad, root ni credenciales propuestos por el modelo.
- GitHub: crea/muestra review y acepta decisiones solo desde comandos/eventos
  explícitos ya autorizados. Reutiliza installation/repository/PR/actor/head y
  changed paths autenticados; refresca head antes de decidir y no usa texto de
  PR como path o autoridad. Publica únicamente resumen seguro e idempotente.

## Flujos principales

### Crear revisión

1. Adapter resuelve aliases autorizados y principal.
2. Servicio autoriza `review.create` para repository/commit.
3. Resolvers entregan candidate y baseline sellados.
4. Se validan baseline, manifests, límites y binding.
5. Differ genera items all-or-nothing y artifact digest.
6. Store reserva idempotency key y publica artifact/checkpoint atómicamente.
7. Se devuelve una vista acotada con IDs, counts, state y digests.

### Decidir

1. Se carga checkpoint íntegro por review ID.
2. Se autoriza actor y se refrescan candidate/baseline/source/analyzer/head.
3. Se compara el binding completo y expected revision.
4. Se valida el conjunto whole/selective y su cierre estructural.
5. Se calcula decision digest y, si está completo, approved projection/binding.
6. CAS añade el evento y publica el nuevo checkpoint exactamente una vez.

### Exportar

1. Se autoriza `review.export`, carga aprobación y revalida binding.
2. Exporter produce y valida bytes OpenAPI canónicos bajo límites.
3. Se revalida binding inmediatamente antes del publisher.
4. Publisher valida path/expected state y hace publicación atómica.
5. Store registra receipt con output digest y target relativo seguro.

### Consumir para planning/validación

1. Composition resuelve `ApprovedReference` explícita.
2. Servicio restaura y revalida el binding.
3. Adapter produce snapshot neutral con lineage completo.
4. Catalog policy aplica autoridad, confidence/completeness/conflicts.
5. Workflow vuelve a revalidar antes de plan approval y tool execution.

## Flujos de error

- Baseline inválida/incompatible: no se calcula review utilizable.
- Límite/cancelación en diff: no se guarda artifact parcial.
- Actor no autorizado: no hay evento ni efecto externo.
- Expected revision obsoleta: conflicto CAS sin aplicar ningún item.
- Drift: evento `invalidated` durable; se bloquean decisión/export/use.
- Decision set no cerrado: invalid input sin proyección parcial.
- OpenAPI no representable: omission explícita; si impide un documento válido,
  export falla all-or-nothing.
- Target existente o mutado: publisher no reemplaza y no registra receipt.
- Corrupción/recovery ambiguo: integrity error; nunca reconstrucción por mejor
  esfuerzo.
- GitHub head/changed paths distintos: invalidación antes de decisión/publish.

## Estado y transiciones

```text
created
  ├─ selective decision incomplete ──> partially_decided
  ├─ reject_all ─────────────────────> rejected
  └─ complete decisions ─────────────> approved

created/partially_decided/approved/rejected
  └─ binding drift ──────────────────> invalidated

approved -- export receipt --> approved (append-only publication event)
```

Una nueva decisión sobre el mismo artifact crea una nueva revisión CAS del
decision set e invalida el approval binding anterior antes de generar otro. No
se reescriben eventos. `invalidated` exige crear un artifact nuevo con inputs
actuales; no se reactiva por volver a coincidir accidentalmente.

## Propiedad del estado, concurrencia e idempotencia

El store es la fuente de verdad de artifacts, eventos, decision revisions,
approval bindings y receipts. Los catálogos se resuelven de nuevo en checkpoints
de autoridad, no se guardan punteros mutables. CAS serializa cambios sobre una
review; reviews distintas avanzan en paralelo.

La clave idempotente se enlaza a principal, operación y request digest. Un retry
idéntico devuelve el mismo resultado. Una key reutilizada con otro request es
conflict. El publisher usa además review/approval/output/target/expected digest
como identidad del efecto.

No se introducen goroutines en constructores. Si GitHub coordina publicación
asíncrona, el owner existente conserva wait/lease/cancel y el evento durable se
persiste antes de confirmar recepción.

## Cancelación y retries

`context.Context` es primer argumento de resolvers, diff, store, exporter y
publisher. Hay checkpoints por operación, item, schema node, provenance,
serialización y fase de publicación. La cancelación no se sustituye.

El core no reintenta decisiones. Adapters pueden reintentar lecturas/transient
I/O de forma acotada solo si no existe efecto; las mutaciones se recuperan por
idempotency y CAS. Un resultado de rename incierto se resuelve verificando el
digest del target y el journal antes de repetir.

## Contratos, seguridad y redacción

Los documentos y diffs son datos no confiables. IDs, safe locations y rutas se
validan con vocabularios/longitudes cerrados. Artifacts y export solo contienen
estructura necesaria. Projection policy rechaza forbidden values y nombres
sensibles antes de canonicalizar. Errors exponen categoría, operación y
ubicación estructural allowlisted; conservan causas solo para cancelación/I/O
interno no serializado.

Ningún token GitHub, OAuth profile, cookie, header, body, prompt o chain of
thought entra en modelo, journal, export o logs. Los adapters resuelven
credenciales únicamente durante composición y no las entregan al servicio.

## Observabilidad

Eventos seguros: `review_created`, `decision_recorded`, `approval_created`,
`review_invalidated`, `export_committed`, `export_failed`. Incluyen review ID,
repository ID, commit, revision, actor ID cuando procede, counts, digests,
categoría, duración y bytes; nunca contenido de schema/source ni path absoluto.
Métricas agregan outcomes y fases. Logs no son fuente de estado.

## Convención obligatoria de comentarios sistémicos

Cada entidad, constante, función, método y helper Go nuevo o modificado debe
incluir un comentario que explique su responsabilidad dentro de FeatureProof o
la frontera/invariante que protege. La obligación incluye símbolos no
exportados, adapters, fakes, fixtures y helpers de tests. El comentario no debe
limitarse a parafrasear el nombre o la sintaxis. El reviewer auditará el diff
completo y tratará cualquier omisión como task incompleta.

## Estrategia de pruebas

- Unitarias table-driven para canonicalización, diff de cada clase, schemas
  recursivos, incertidumbre, decisiones y proyección selectiva.
- Property/permutation tests para determinismo, deep copies y orden de maps.
- Tests contractuales del store y publisher con crash points, corrupción,
  symlink/reparse/hard-link, overwrite, TOCTOU, recovery, CAS e idempotencia.
- Tests del exporter que validan OpenAPI 3.1, mapping estructural, extensions,
  omissions y bytes repetibles.
- Tests de autoridad con actor, clock y resolvers falsos; drift individual de
  cada componente del binding antes de decide/export/use y entre checkpoints.
- Tests verticales CLI/API/MCP/GitHub contra el mismo servicio, incluido refresh
  de head, redelivery y equivalencia de códigos/vistas.
- Tests de propuesta-only y de workflow que prueban cero ejecución/validación
  determinista al faltar aprobación vigente.
- Canarios de secretos en toda entrada y todos los outputs/errores/eventos.
- Repetición, detector de carreras, compile-only, vet, gofmt y cobertura mínima
  del 85 % del código nuevo/modificado. Cada R tendrá un test nominal y adverso
  identificable en el informe.

## Mapa inicial de trazabilidad

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | modelo/canonicalizer | estabilidad, digest drift, deep copy, versión |
| R2 | baseline verifier | authoritative/prior approval, incompatible, ambiguous |
| R3 | structural differ | add/modify/remove/uncertain, recursion, permutations |
| R4 | decision reducer | total/selective/reject, unknown IDs, uncertain, dependencies |
| R5 | authorizer/audit | denied, trusted clock/actor, full binding, redaction |
| R6 | binding verifier/workflow | drift por campo, restart, pre-I/O TOCTOU |
| R7 | approved source/policy | proposal-only, lineage, partial absence, conflict |
| R8 | OpenAPI exporter | valid 3.1, extensions, omissions, determinism |
| R9 | atomic publisher | create, guarded replace, links/traversal, failures |
| R10 | store/service | concurrent CAS, idempotency, crash recovery, race |
| R11 | limits/errors | agotamiento por fase, cancelación, canarios, categories |
| R12 | surface adapters | parity CLI/API/MCP, GitHub head/redelivery/safe view |
| R13 | gates/comments | trazabilidad, AST/diff audit, race, coverage |

## Alternativas y decisiones

- **Mutar el candidate al decidir:** descartado porque borra lo revisado y hace
  imposible auditar cambios de decisión. Artifact, decisions y projection son
  objetos distintos.
- **Diff textual OpenAPI/código:** descartado; pierde equivalencia estructural y
  acopla review a un formato. El diff opera sobre `OperationCatalog`.
- **Resolver conflictos por prioridad/confidence:** descartado; el merger y el
  reviewer hacen visible el desacuerdo y solo una decisión humana explícita
  elige una proyección.
- **Tratar approval como aumento de confidence/completeness:** descartado. La
  aprobación aporta autoridad humana sobre bytes exactos, no nueva evidencia
  técnica.
- **Sobrescribir por flag booleano:** insuficiente frente a TOCTOU. V1 requiere
  flag explícito más digest esperado del archivo existente.
- **Exportar YAML y JSON:** se elige JSON canónico v1 para bytes/digest
  reproducibles. YAML puede añadirse después sin cambiar el dominio.
- **Implementar una regla distinta por superficie:** descartado. Todas delegan
  al mismo servicio y solo adaptan identidad, transporte y respuesta segura.

## Riesgos y mitigaciones

- El diff fino puede crear muchos items: límites y grupos atómicos evitan
  artifacts inmanejables o proyecciones inválidas.
- OpenAPI no representa todos los facts de analizadores: extensions y omissions
  impiden pérdida silenciosa.
- Una aprobación puede quedar obsoleta entre checks: binding completo,
  re-resolución y checkpoint inmediatamente pre-I/O reducen esa ventana; store
  y publisher aplican CAS/expected digest en el commit.
- Filesystem Windows y Unix difieren: tests contractuales cubren symlink/reparse
  y publicación; fallos de privilegio ambiental se documentan sin debilitar las
  garantías productivas.
