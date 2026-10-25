# Contexto acotado de pull requests de GitHub — Design

## Resumen de la solución

La feature amplía la frontera GitHub read-only existente con una operación de
adquisición coherente y añade un dominio propio `internal/githubcontext`. La
integración ya resuelve instalación, repositorio, PR, actor/fork y token; el nuevo
caso de uso recibe esa identidad sellada junto con una `ContextPolicy/v1`
composition-owned y obtiene únicamente los campos necesarios para contexto.

La lectura sigue un protocolo metadata → files → metadata. Las dos lecturas de
metadata incluyen repository/PR/state, title/body raw, `head` y `base` completos.
Solo si sus fingerprints coinciden se normalizan los archivos. Cada página se
solicita mediante una ruta construida localmente; ninguna URL del response se
sigue. El resultado canónico conserva digests, counts, omissions y referencias
seguras, mientras todo texto remoto permanece como datos no confiables.

El flujo aplica barreras en este orden:

1. identidad y policy de composición;
2. adquisición coherente con budgets de HTTP/retry/paginación;
3. validación estructural/canonicalización de metadata y files;
4. filtros de paths/status/tipos y packing de hunks completos;
5. extracción determinista de constraints y selección de contract refs;
6. secret gate sobre cada item antes de store/ranking/model;
7. conversión a producers neutrales de `contextassembly`;
8. binding y revalidación contra GitHub/policy/catálogo actuales.

La feature no crea un segundo workflow GitHub. El webhook/coordinator de
`internal/githubpr` continúa siendo dueño de admisión y publicación. El
`validationworkflow` compartido solicita el contexto después de admitir la PR y
antes de generar el plan; las cuatro superficies siguen entrando por el mismo
servicio. El modelo nunca recibe gateway, tokens, HTTP, URLs o closures.

## Fuentes de contexto consultadas

- `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md`,
  `docs/architecture.md`, `docs/conventions.md` y `docs/verification.md`.
- Acceptance criteria de feature 30 en `feature_list.json`.
- `specs/context-assembly/*` e `internal/contextassembly`: `SourceDiff`,
  `NeutralDiffProducer`, profiles/field policy, trust sections, registry,
  snapshot/binding y revalidación.
- `internal/validationworkflow/context_assembly.go`: composición actual de
  producers, `CurrentContextProducerRegistry` y checkpoints compartidos.
- `specs/github-pr-integration/*` e `internal/githubpr`: webhook, autorización,
  instalación, token, retry/rate-limit, `PullRequestSnapshot`, changed paths,
  publication ownership y store durable.
- `internal/contractcatalog`, loaders OpenAPI y reducers del workflow: los
  contratos internos consumen rutas/refs aprobadas; esta feature solo identifica
  cuáles están afectados.
- Documentación oficial de GitHub REST observada el 2026-10-25:
  - [Get a pull request y List pull requests files](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2022-11-28): media type raw para body, páginas de hasta 100, máximo documentado de 3000 archivos y `patch` opcional por archivo.
  - [Rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api): `403/429`, `Retry-After`, `X-RateLimit-Remaining` y `X-RateLimit-Reset`.
  - [REST best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api): requests seriales, backoff acotado y respeto de rate limits.
  - [Repository contents](https://docs.github.com/en/rest/repos/contents?apiVersion=2022-11-28): existe lectura por `ref`, pero se descarta para v1 para no descargar archivos ni ampliar permisos.

No se copian DTOs ni reglas internas de GitHub al dominio. La spec fija una API
versionada en composición y prueba contratos mediante un servidor falso.

## Límites de responsabilidad

```text
Webhook GitHub ya autenticado
        │ authorized installation/repository/PR/expected head
        ▼
validationworkflow admission
        │ composition-owned ContextPolicy + idempotency
        ▼
githubcontext.Acquirer
        │
        ├── GitHubContextGateway ──> githubpr REST adapter ──> GitHub GET
        ├── ConstraintExtractorRegistry
        ├── ContractSelector
        └── SecretGate
        │ canonical PullRequestContext/v1 + binding
        ▼
GitHubContextProducer
        │ SourceDiff / SourceConstraints, untrusted_data
        ▼
ContextAssembler ──> plan generation
        │
        └── revalidation before approval/execution/recovery
```

### `internal/githubcontext`

Paquete cohesionado de dominio/aplicación para:

- modelos canónicos de request, policy, remote observation, file/hunk,
  constraint, contract reference, omission, context, binding y terminal record;
- validación estricta, deep-copy, orden, canonical JSON y digests;
- coordinación de adquisición coherente y retry del intento completo;
- canonicalización/filtering/packing de archivos y hunks;
- extractores versionados y selector de contratos;
- errores cerrados, observación segura e idempotencia read-only.

No importa `net/http`, SDK GitHub, `agentplanning` ni stores de workflow.

### `internal/githubpr`

Se amplía el adapter REST ya autenticado, sin alterar ownership de webhook o
publication. Añade DTOs privados para title/body/base SHA y file status,
previous filename, counts y patch. Implementa un puerto mínimo de lectura
consumido por `githubcontext` y reutiliza token source, HTTP doer, API version,
clasificación de errores, clock y retry.

El adapter no devuelve URLs ni remote bodies de error. Sus endpoints son GET y
se construyen a partir de IDs/slug ya comprobados. El permission set existente
`pull_requests: read` basta para metadata/files; v1 no solicita `contents: read`.

### `internal/validationworkflow`

La composición obtiene la identidad GitHub sellada de admission, resuelve una
policy/catálogo actuales y registra `GitHubContextProducer`. La authority no
puede venir de `StartCommand`, API, MCP o model payload salvo los IDs ya
autorizados por el flujo GitHub.

`CurrentContextProducerRegistry` reconstruye el producer desde las autoridades
actuales en restart/revalidation. Los checkpoints existentes antes de approval,
execution y recovery comparan el binding de feature 30 junto al binding general
de context assembly.

### `internal/contextassembly`

Se reutilizan `SourceDiff`, `SourceConstraints`, secciones y store. Solo se
ajusta el vocabulario de campos/perfiles si hace falta para representar metadata
mínima, omissions y contract refs. `NeutralDiffProducer` continúa sin acceso a
GitHub. El producer GitHub crea una `Projection` ya autorizada, no añade red al
assembler.

### `cmd/featureproof`

La composición productiva conecta gateway, policy, extractor registry, secret
gate, clock y observer. Credenciales/HTTP permanecen dentro del adapter GitHub.
No se añade un entrypoint distinto ni se permite configurar policy desde texto
de PR.

## Contratos propuestos

Los nombres son orientativos; el comportamiento es normativo.

### Request y policy

```text
ContextRequest/v1
  idempotency_key
  run_id
  installation_id
  repository_id
  pull_request_number
  expected_head_oid
  policy_digest

ContextPolicy/v1
  id, version, digest
  extractor { kind, version, section_marker | reference_prefix }
  constraint_catalog_digest
  path_allowlist[]
  allowed_statuses[]
  allowed_text_types[]
  openapi_routes[] { contract_id, version, path }
  limits { attempts, duration, response_bytes, pages, files,
           title_bytes, body_bytes, patch_bytes_per_file,
           patch_bytes_total, hunks, lines, constraints, items }
```

`ContextPolicy` llega validada desde configuración. Toda colección se copia,
ordena y deduplica al construirla. Un caller puede pedir límites menores si el
perfil lo permite, nunca ampliar la policy.

### Observación remota neutral

```text
RemotePullRequest
  installation_id, repository_id, number, state
  head_oid, base_oid
  title_raw, body_raw

RemoteFile
  path, previous_path?, status
  additions, deletions, changes
  patch?             # ausente para binary/oversized/provider omission
```

Estos tipos cruzan solo el puerto entre el adapter y `githubcontext`; no se
persisten directamente ni entran al modelo.

### Snapshot canónico

```text
PullRequestContext/v1
  authority { installation_id, repository_id, number, head_oid, base_oid }
  policy { id, version, digest, extractor_version, catalog_digest }
  metadata { title_item?, description_item? }
  constraints[]
  files[]
  affected_contracts[]
  omissions[]
  completeness
  counts
  bindings
  observation_digest
  digest

GitHubContextBinding/v1
  run_id
  context_digest
  observation_digest
  repository_id, pull_request_number
  head_oid, base_oid
  policy_digest, catalog_digest, extractor_version
  producer_id, producer_version
  digest
```

Los payloads permanecen en el snapshot seguro solo después de secret gate. El
journal/terminal record conserva IDs/digests/counts/reasons, no payloads. El
binding no contiene contenido.

### Omission reasons

Vocabulario inicial cerrado:

- `path_not_allowed`, `type_not_allowed`, `status_not_allowed`;
- `binary_or_patch_absent`, `patch_oversized`, `hunk_limit`, `line_limit`;
- `file_limit`, `page_limit`, `provider_collection_limit`;
- `title_oversized`, `description_truncated`, `constraint_oversized`;
- `contract_deleted`, `contract_selection_partial`;
- `secret_redacted`, únicamente si la policy permite redacción segura de la
  unidad; en otro caso se devuelve error `secret`.

Cada omission lleva kind, safe item/path digest y count. No lleva el texto
omitido, path sensible completo ni remote body.

## Protocolo de adquisición coherente

1. Validar request, policy, expected head y budgets sin red.
2. Resolver token read-only de la instalación/repositorio dentro del adapter.
3. GET metadata A; validar identidad, state y `head == expected_head`.
4. Leer files en serie usando `page=1..N`, `per_page=100`, rutas locales y
   límites. Acumular solo DTOs bounded; registrar incomplete al alcanzar un
   límite demostrable.
5. GET metadata B.
6. Comparar fingerprints de todos los campos relevantes, incluido base/head y
   raw title/body. Si difieren, descartar todo y clasificar `stale/conflict`.
7. Canonicalizar y filtrar paths/status/types. Dividir patches en hunks completos
   mediante parser de unified diff estricto; patch ausente nunca inicia otra
   descarga.
8. Ejecutar extractor configurado sobre el body raw bounded. Proyectar title/body
   dentro de budgets sin usar su contenido como control.
9. Seleccionar contratos por tabla confiable y current/previous paths.
10. Pasar cada unidad por secret gate y construir manifests/omissions.
11. Sellar observation digest y context digest; publicar mediante idempotency.
12. Convertir a projection neutral e invocar ContextAssembler.

Si un intento falla por transporte transitorio o rate limit dentro de budget,
se descarta todo el intento parcial y se reinicia desde metadata A. Nunca se
mezclan páginas de intentos distintos.

## Constraints explícitas

### `markdown_section/v1`

La policy fija un heading exacto ASCII, por ejemplo `## FeatureProof Constraints
(v1)`. El parser Markdown determinista reconoce exactamente una sección de ese
nivel hasta el siguiente heading de igual o menor nivel. Dentro admite un formato
versionado y acotado (lista de objetos con `id` y `description`); no interpreta
HTML, links, includes, YAML tags, code execution ni Markdown fuera de la sección.

IDs duplicados, sección duplicada, nesting ambiguo, bytes/items excedidos o
sintaxis desconocida fallan cerrado/insufficient según policy. El payload sigue
siendo `untrusted_data` aunque el extractor y la procedencia estén verificados.

### `catalog_reference/v1`

La policy fija un prefijo exacto y admite una única línea con
`<alias>@<version>`. El alias se busca en un catálogo composition-owned cuyo
digest forma parte del binding. El cuerpo solo selecciona una entrada permitida;
no aporta su contenido ni rutas. Alias/version desconocido o duplicado es
insufficient/conflict.

No existe fallback de un extractor a otro ni extracción libre por modelo.

## Patches, tipos y contratos

El file status es un enum cerrado compatible con los estados requeridos por el
endpoint. Paths deben llegar ya canónicos con `/`; se rechazan absolutos,
traversal, backslash, colon/device y duplicados incompatibles. Para rename se
conserva current/previous path.

La policy usa globs/path prefixes previamente compilados y extensiones/media
types textuales. La extensión no demuestra que el patch sea seguro; unified diff
se parsea con límites exactos de bytes/hunks/líneas y todo contenido permanece
no confiable. Un patch sin propiedad `patch` se clasifica, no se busca por URL.

`ContractRoute` asocia path canónico exacto o patrón aprobado a
contract ID/version. El selector compara exclusivamente current/previous paths.
No parsea código, no infiere por texto y no carga el contrato. El resultado es
una safe reference ligada a head y status. Un delete/rename conserva causalidad;
una lista partial impide afirmar selección completa.

## Binding, drift y checkpoints

El binding sella:

- installation/repository/PR;
- head y base completos;
- observation/context digests;
- policy/catalog/extractor/profile/producer versions;
- run/plan mediante el binding general de context assembly.

La revalidación no compara el binding consigo mismo. Una composición nueva
resuelve el `GitHubContextGateway`, policy y catálogo actuales, vuelve a adquirir
una observación coherente y compara sus bindings. Drift de title/body/files con
head estable cambia `observation_digest`; base drift cambia el diff; head drift
invalida la revisión. Los checkpoints son:

```text
admission → acquire → assemble → generate/prepare plan
                              ↓
                    revalidate before approval
                              ↓
                    revalidate before execution
                              ↓
                    revalidate on restart/recovery
```

Al detectar stale se detiene antes de model/tool en ese checkpoint. Para usar la
nueva revisión se requiere una nueva solicitud/plan; no se reescribe el binding
anterior.

## Paginación, retries y rate limits

- Las páginas se solicitan secuencialmente para limitar rate y simplificar
  coherencia. El `Link` solo indica presencia de `next`; su URL nunca se usa.
- `408`, transport failure y `5xx` son candidatos transitorios. `403/429` solo
  se consideran rate limit cuando headers seguros lo demuestran.
- `Retry-After` y reset se acotan por deadline, max wait, max attempts y max
  duration. El clock/waiter es inyectado; tests no usan sleeps reales.
- Un retry reinicia metadata/files/metadata. Resultados parciales no se mezclan.
- Errores permanentes, integridad, auth, invalid/stale y budgets agotados no se
  reintentan como transporte.
- La idempotency key liga run + PR identity + expected head + policy digest. La
  misma key con input distinto es conflicto. Un resultado confirmado puede
  redeliver el mismo digest; las únicas llamadas externas son GET.

## Propiedad del estado y concurrencia

Una invocación de `Acquirer` posee request, attempt state, páginas y buffers. El
servicio de larga vida conserva solo dependencias inmutables; no guarda context.
No hay goroutines necesarias en el flujo nominal. Si la implementación añade
trabajo paralelo interno, deberá tener owner/join y no cambiar el orden canónico;
la opción preferida es lectura serial.

El registro/store de resultados usa CAS/idempotency y no mantiene locks durante
token, HTTP, clock waits, secret detector, observer o disco. Requests diferentes
pueden progresar. Requests iguales convergen por digest/receipt sin permitir que
un contender borre estado confirmado o contenido compartido.

## Errores y flujos alternativos

Errores propios cerrados: `invalid`, `unauthorized`, `stale`, `conflict`,
`integrity`, `partial`, `insufficient`, `secret`, `rate_limited`, `transient`,
`cancelled`, `timeout`, `persistence`, `internal`.

- `partial/insufficient` describe falta esperada de contexto; no es evidence ni
  fallo funcional de la aplicación.
- `stale` obliga a nueva revisión/plan.
- `rate_limited/transient` aplica retry solo dentro de budgets.
- `cancelled/timeout` conserva `context.Cause` por `Unwrap` sin filtrar su texto.
- Corruption, response overflow, identity mismatch y URLs/paths inválidos fallan
  cerrado antes de store/model.

## Secretos y observabilidad

El token existe solo entre credential source y HTTP request. Los DTOs se
descartan tras traducción. Secret gate recibe cada unidad ya bounded antes de
ranking, persistence o model. Redaction nunca convierte contenido en autoridad.

`ContextAcquisitionRecord/v1` incluye outcome, run ID, repository ID, PR number,
head/base/policy/observation digests, attempt/page/file/hunk/item counts,
completeness, omission reason counts, duración y error category. Excluye title,
body, filenames raw, patch, constraint body, tokens, headers, URLs, remote error
bodies y chain of thought. Se emite exactamente un terminal record con observer
timeout acotado.

## Integración con las cuatro superficies

CLI/API/MCP no pueden inventar una identidad GitHub: solo el entrypoint GitHub
produce esa authority. Una vez admitido, las cuatro proyecciones internas del
servicio comparten el mismo `validationworkflow.Service`, ContextAssembler,
store y revalidation checkpoints. Tests de paridad usan autoridad pre-sellada y
demuestran el mismo digest; esto no abre GitHub context a callers arbitrarios.

El control plane existente conserva webhooks/checks/comments. Feature 30 no
publica nada y sus errores se traducen al lifecycle ya existente como operational
o insufficient, nunca como verdict funcional.

## Estrategia de pruebas

### Dominio `githubcontext`

- strict decode/validation, deep-copy, canonical order/digest y tampering;
- matrices de paths/status/rename/delete/binary/duplicates/counts;
- parser unified diff con límites exactos, UTF-8 y hunks completos;
- extractores section/reference válidos, duplicados, ambiguos y oversized;
- selector OpenAPI exacto, rename/delete/dedup/partial;
- trust separation, prompt injection y canarios en cada campo;
- idempotency/conflict/concurrency/cancel cause/timeout.

### Adapter GitHub hermético

Servidor HTTP local/fake transport valida:

- rutas, GET, headers permitidos, API version y scope de token;
- metadata A/files pages/metadata B y drift de cada campo;
- title/body raw, base/head completos y file DTOs;
- pagination anomalies, max provider collection, response overflow;
- 408/403/429/5xx, Retry-After/reset, backoff y cancellation;
- ninguna navegación a URLs incluidas en DTO/patch.

### Integración productiva

- PR válida → SourceDiff/constraints → ContextAssembler → prepared plan;
- head/base/body/file/policy/catalog drift antes de approval/execution/recovery
  con cero llamadas posteriores a model/tools;
- restart reconstruye producers actuales;
- paridad del service y aislamiento por rol;
- journal/events/model request escaneados byte-for-byte para canarios/tokens;
- repeated/race para adquisiciones iguales/independientes y response loss.

### Gates

- `gofmt -l`, `go vet ./...`, compile-only global.
- focales repetidos, `go test ./...`, `go test -race ./...` y matrices críticas
  race de interleavings/retry/cancellation.
- runtime Linux y cross-builds Windows/Linux/Darwin cuando apliquen.
- coverage statement-level del diff >=85% sin redondear e incluyendo build tags.
- auditoría `R→test`, temporales y comentarios sistémicos para toda entidad,
  constante, variable, función, método y helper nuevo/modificado.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | request/policy validation, composition | `TestAcquisitionUsesOnlyCompositionAuthority`, invalid identity matrix |
| R2 | coherent reader, fingerprints | `TestAcquireRejectsMetadataDriftAcrossFilePages` |
| R3 | canonical models | `TestContextCanonicalDigestAndDetachedInputs`, tamper/schema matrix |
| R4 | trust projection/envelope | `TestGitHubTextCannotBecomeInstructionsOrConfiguration` |
| R5 | extractor registry | `TestConfiguredConstraintExtractorsAreExactAndVersioned` |
| R6 | budgets/packer/omissions | `TestContextBudgetsUseWholeUnitsAndDeclareEveryOmission` |
| R7 | file validator/diff parser | `TestFilesRejectUnsafePathsAndNeverFetchMissingPatch` |
| R8 | contract selector | `TestAffectedOpenAPIContractsComeOnlyFromConfiguredPaths` |
| R9 | producer/contextassembly integration | `TestGitHubProjectionIsMinimalRoleScopedAndDeterministic` |
| R10 | binding/revalidator/checkpoints | `TestGitHubContextDriftStopsPlanApprovalExecutionAndRecovery` |
| R11 | credentials/secret gate | `TestCredentialsAndCanariesNeverCrossGitHubAdapter` |
| R12 | pagination/retry/rate limit | `TestGitHubContextPaginationAndRetryBudgetsAreBounded` |
| R13 | lifecycle/idempotency/concurrency | `TestContextRedeliveryAndConcurrentAcquisitionsConverge` |
| R14 | typed errors/observer | `TestContextTerminalRecordIsExactlyOnceAndContentFree` |
| R15 | verification pipeline | hermetic fake suite, race, coverage, cross-platform/comment audits |

## Alternativas y decisiones descartadas

- **Usar el snapshot mínimo actual de `githubpr`:** changed paths no incluyen
  title/body, base OID, status ni patch; no satisface contexto ni omissions.
- **Extender `PullRequestSnapshot` con cuerpos completos:** mezclaría autoridad de
  control plane con contenido no confiable y lo persistiría en associations.
- **Solicitar un diff monolítico por media type:** dificulta límites por archivo,
  clasificación de binarios/renames y omissions; files endpoint ofrece unidades.
- **Seguir `contents_url/raw_url` cuando falta patch:** amplía permisos y puede
  descargar contenido no solicitado. V1 declara omission.
- **Hacer checkout temporal:** introduce ejecución/filesystem, hooks/submodules y
  rompe el alcance explícito.
- **Extraer constraints con el modelo:** vuelve texto no confiable autoridad y no
  es reproducible.
- **Inferir OpenAPI por nombre/contenido/similitud:** puede seleccionar fuera de
  policy. Solo se usan rutas configuradas.
- **Confiar solo en head:** title/body o base pueden cambiar con head estable; el
  observation digest y base OID también se revalidan.
- **Cachear por PR number:** mezclaría revisiones. La identidad incluye expected
  head, policy y observation digests.
- **Añadir contents permission:** no es necesario para referencias y contradice
  mínimo privilegio de esta entrega.

## Conformidad arquitectónica

- `ARCH-001/002`: el modelo no accede a GitHub; el adapter read-only es una
  capability de composición, no una tool model-facing.
- `ARCH-003/004`: diff/constraints/context no son evidence ni verdict.
- `ARCH-005`: respuestas, páginas, archivos, bytes, hunks, intentos y duración
  tienen límites explícitos.
- `ARCH-006`: token, HTTP, waits, parsers, detector, store y observer propagan
  context y causa.
- `ARCH-007`: journal/records confirmados son append-only y content-free.
- `ARCH-008`: todo texto GitHub permanece `untrusted_data`.
- `ARCH-009`: secret gate precede persistence/model y token queda en transporte.
- `ARCH-010`: todos los tests usan fakes/servidor local, sin GitHub real.
- `ARCH-011/012`: texto/modelo no amplía policy y adapters no mutan el Run.
- `CONV-001..010`: paquetes cohesionados, puertos del consumidor, errores
  tipados, clocks inyectados, sin locks durante I/O, tests deterministas,
  trazabilidad y una sola feature.

No se solicita excepción a arquitectura o convenciones.
