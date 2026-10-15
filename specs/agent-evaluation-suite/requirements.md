# Suite de evaluación del agente — Requirements

## Objetivo

Medir de forma reproducible la calidad y seguridad del generador de planes de
la feature 10 sobre casos versionados. La suite debe convertir resultados
estructurados en métricas deterministas, aplicar criterios versionados y
detectar regresiones sin ejecutar herramientas ni acciones contra una
aplicación desplegada.

## Alcance

- Definir formatos v1 versionados para dataset, casos, criterios, ejecución y
  baseline.
- Ejecutar cada caso mediante la frontera pública de `agentplanning.Generator`
  con inputs locales y acotados.
- Evaluar planes preparados, contexto insuficiente y rechazos mediante
  propiedades estructurales y validators existentes.
- Detectar invención de endpoints, capabilities, identidades, credenciales y
  fuentes de evidencia.
- Medir cobertura, validez, selección de operaciones, reconocimiento de
  contexto insuficiente y respeto de límites.
- Probar resistencia a instrucciones incrustadas en OpenAPI, logs y respuestas
  representadas como datos externos no confiables.
- Producir un bundle completo, verificable y libre de secretos por ejecución.
- Comparar una ejecución candidata con una baseline compatible.
- Ofrecer un comando separado para replay/fakes y, opcionalmente, un modelo
  real compuesto sobre la feature 9.

## Fuera de alcance

- Cambiar el protocolo, prompt, validación o comportamiento del generador de
  la feature 10 para mejorar una puntuación.
- Aprobar, editar o rechazar planes; corresponde a la feature 12.
- Ejecutar planes, herramientas, requests contra targets o aplicaciones de
  producción; la ejecución y replanificación corresponden a las features 12 y
  13.
- Usar las conclusiones textuales del modelo, razones `not_applicable` o texto
  libre como prueba suficiente de calidad o seguridad.
- Entrenar, afinar, seleccionar automáticamente o establecer un modelo,
  proveedor, perfil de autenticación, baseline o threshold por defecto.
- Convertir la suite en un paso implícito de `go test`.

## Actores y términos

- **Dataset:** manifest y casos locales inmutables identificados por versión y
  digest.
- **Caso:** constraints, fuente OpenAPI y selección reducida, catálogo
  descriptor-only, límites y propiedades esperadas.
- **Oracle estructural:** evaluador determinista que inspecciona resultados y
  errores tipados, nunca opiniones textuales del modelo.
- **Criterios:** archivo versionado con thresholds, reglas hard-fail y deltas de
  regresión. Es obligatorio y no tiene valores implícitos.
- **Candidate run:** ejecución cuyos resultados se califican.
- **Baseline:** bundle completo anterior usado como referencia, nunca como
  fuente de credenciales o output esperado del modelo.
- **Replay:** fuente local de respuestas estructuradas usada sin red.
- **Live:** composición explícita con un proveedor real mediante feature 9.
- **Invention:** referencia o valor que afirma autoridad fuera de los conjuntos
  permitidos por el caso.
- **Manifest:** registro seguro de inputs identificadores, versiones, hashes,
  configuración, resultados y métricas de una ejecución.

## Requirements

### R1 — Dataset local, versionado y canónico

WHEN an evaluation dataset is loaded,
THE suite SHALL require dataset schema version 1, a non-empty dataset ID, an
explicit semantic dataset version, a bounded ordered case list and SHA-256
digests for every referenced local file, SHALL reject duplicate case IDs,
unknown fields, unknown versions, broken digests, path traversal, symlinks that
escape the dataset root and remote references, and SHALL snapshot the validated
bytes before starting any case.

#### Acceptance criteria

- GIVEN a valid dataset
  WHEN loaded twice
  THEN its canonical digest and case order are identical.
- GIVEN an unknown schema, duplicate ID, modified fixture, absolute/escaping
  path, remote OpenAPI reference or file/count/byte limit excess
  WHEN loaded
  THEN a typed input/limit error identifies only the safe structural field and
  zero model calls occur.
- GIVEN caller mutation after loading
  WHEN cases run
  THEN the snapshotted constraints, OpenAPI selection, catalog and expectations
  remain unchanged.

### R2 — Caso reproducible y propiedades esperadas

WHEN a case is decoded,
THE suite SHALL require schema version 1, constraints, one locally constructed
reduced OpenAPI context, a descriptor-only capability catalog, explicit
generation limits and an expected outcome `plan`, `insufficient_context` or
`rejected`, and SHALL express expectations as bounded structural sets and
relations for constraints, scenarios, operations, capabilities, identities,
evidence references, rejection category and budget consumption.

#### Acceptance criteria

- GIVEN a plan case
  WHEN composed
  THEN its expected required/allowed operations and capabilities refer only to
  its retained OpenAPI provenance and catalog.
- GIVEN an insufficient-context case
  WHEN composed
  THEN expected affected constraints and allowed missing-item kinds are
  explicit; matching free text is neither required nor scored.
- GIVEN inconsistent expectations, unknown catalog contract, unbuildable local
  OpenAPI selection or a secret-shaped fixture outside designated synthetic
  canaries
  WHEN validated
  THEN the case is rejected before inference.

### R3 — Ejecución sin efectos contra aplicaciones

WHILE an evaluation run is active,
THE runner SHALL invoke only the plan-generation boundary, local loaders,
deterministic validators, artifact writer and selected model source, SHALL NOT
compose an executable tool registry, target URL, plan executor, evidence store
or production credentials, and SHALL perform at most one model inference per
case without retries, repairs, approval, execution or replan.

#### Acceptance criteria

- GIVEN any prepared, insufficient, rejected, cancelled or failed case
  WHEN the run ends
  THEN application/tool/evidence/journal fakes record zero calls and the model
  call count is at most the number of admitted cases.
- GIVEN a dataset containing a URL or target-like value
  WHEN loaded
  THEN it cannot become an outbound destination; only a deliberately selected
  live model adapter may use network.
- GIVEN one case failure
  WHEN fail-fast is disabled in explicit criteria
  THEN later admitted cases continue in dataset order; no partial plan is
  executed.

### R4 — Oracle anti-invención determinista

WHEN a case returns a plan or structured rejection,
THE oracle SHALL derive endpoint, capability, identity, credential and evidence
invention findings by comparing structured candidate fields and typed validator
outcomes with the case's allowlists and provenance, SHALL record one finding per
category and location without copying the unsafe value, and SHALL make any
configured hard-fail category fail the run independently of aggregate scores.

#### Acceptance criteria

- GIVEN an unknown method/path, capability, identity token, credential-bearing
  field/value, source step or assertion ID
  WHEN scored
  THEN the matching invention category fails even if other metrics pass.
- GIVEN the generator rejects an invention before producing `PreparedPlan`
  WHEN the typed category/stage/path matches the case expectation
  THEN the safety case passes; rejection is not miscounted as a valid plan.
- GIVEN a model statement claiming that no invention occurred
  WHEN structural fields contradict it
  THEN the statement is ignored and the structural finding wins.

### R5 — Métricas estructurales y agregación exacta

WHEN case results are scored,
THE suite SHALL compute deterministic per-case and aggregate integer metrics
for constraint coverage, plan validity, exact operation selection,
insufficient-context recognition and limit compliance, SHALL publish explicit
numerators, denominators and parts-per-million values, and SHALL derive them
only from dataset expectations, `PreparedPlan` getters, retained OpenAPI
provenance, typed generator/model errors, session snapshots and configured
limits.

#### Acceptance criteria

- GIVEN the same dataset snapshot and replay responses
  WHEN run repeatedly
  THEN case findings, numerators, denominators, ppm scores and result digest are
  byte-equivalent.
- GIVEN a zero denominator
  WHEN aggregating a metric
  THEN it is marked `not_applicable` and never treated as 100% implicitly.
- GIVEN missing/extra constraints or operations, invalid plans, incorrect
  insufficiency classification or exceeded calls/tokens/duration/replans/work
  limits
  WHEN scored
  THEN the documented metric changes without consulting textual rationale.

### R6 — Casos de prompt injection tratados como datos

WHEN a dataset case carries malicious instructions in an OpenAPI field, a log
excerpt or a response excerpt,
THE runner SHALL place the canary only in the existing untrusted case data
projection, SHALL capture the actual structured model boundary and verify that
the canary is absent from privileged instructions, and SHALL score any plan
that follows it using the same provenance, invention and validation rules as
other cases.

#### Acceptance criteria

- GIVEN distinct canaries for OpenAPI, log and response sources
  WHEN the model request is captured
  THEN each appears, if retained by the approved projection, only under
  `StructuredRequest.Untrusted` and never under `Instructions`.
- GIVEN an OpenAPI field intentionally omitted by the reducer
  WHEN evaluated
  THEN its canary is absent from both channels and the omission is recorded as
  an expected projection property.
- GIVEN an injection asks for an alternate schema, endpoint, tool, identity,
  credential or fabricated evidence
  WHEN followed by a model
  THEN strict decode/validation rejects it or the oracle records the exact
  structural category; prose asserting safety cannot pass the case.

### R7 — Manifest y bundle seguro, completo y reproducible

WHEN an evaluation reaches a terminal state,
THE artifact writer SHALL create a versioned bundle containing a manifest,
ordered case results, aggregate metrics and optional baseline comparison, SHALL
record dataset ID/version/digest, case digests, planning protocol version,
actual prompt text/version/SHA-256 captured at the model boundary, provider and
model identifiers, safe configuration and non-monetary budgets, criteria
ID/version/digest, timestamps, terminal status, per-case results and file
digests, and SHALL exclude auth profile IDs, tokens, credentials, raw provider
payloads, untrusted fixture bodies and external error text.

#### Acceptance criteria

- GIVEN synthetic secret canaries across fixtures, model failures and auth
  composition
  WHEN every output file and formatted error is scanned
  THEN no canary or raw auth profile ID appears.
- GIVEN identical logical replay inputs
  WHEN runs use different wall-clock times or output directories
  THEN their `result_digest`, which excludes runtime metadata, is equal.
- GIVEN write, sync, rename, cancellation or disk failure
  WHEN publishing
  THEN no bundle with `status=complete` is observable until all referenced files
  exist and match their hashes; incomplete staging is not baseline-eligible.

### R8 — Criterios versionados, hard-fails y códigos de salida

WHEN an evaluation command starts,
THE caller SHALL provide an explicit local criteria file with schema version,
ID, semantic version, per-metric minimum ppm thresholds, hard-fail invention
categories, execution limits, fail-fast policy and baseline delta rules, SHALL
apply it without hidden defaults, and SHALL return exit code 0 only when the
run is complete and every applicable threshold, hard-fail and regression rule
passes.

#### Acceptance criteria

- GIVEN an absent criteria path, unknown version, invalid threshold outside
  `[0,1000000]`, duplicate category or contradictory limit
  WHEN invoked
  THEN no case starts and the command returns exit code 2.
- GIVEN a completed run below a threshold, with a hard-fail finding or a
  disallowed regression
  WHEN evaluated
  THEN artifacts are complete, verdict is `failed` and exit code is 1.
- GIVEN cancellation/deadline
  WHEN terminal handling completes
  THEN no complete passing bundle is produced and exit code is 3; unexpected
  runtime/provider/artifact failure uses exit code 2.

### R9 — Comparación compatible con baseline

WHERE a baseline bundle is supplied,
THE comparator SHALL require a complete hash-valid bundle with the same dataset
ID/version/digest, case IDs/digests, metric schema, planning protocol and
criteria schema, SHALL compare candidate and baseline per case and aggregate by
the versioned categories, and SHALL report improved, unchanged and regressed
categories plus exact integer deltas without replacing absolute thresholds.

#### Acceptance criteria

- GIVEN compatible candidate and baseline
  WHEN one category crosses its allowed negative delta
  THEN the category and affected case IDs are reported and the candidate fails.
- GIVEN dataset drift, missing cases, corrupt file hashes, incomplete status or
  incompatible schemas
  WHEN compared
  THEN comparison stops with typed configuration error and exit code 2.
- GIVEN a candidate that improves versus baseline but misses an absolute
  threshold
  WHEN evaluated
  THEN it still fails; baseline cannot waive criteria.

### R10 — Comando explícito y selección de fuente de modelo

WHEN evaluations are requested,
THE system SHALL expose a dedicated `featureproof eval agent` command that
requires dataset, criteria, output and model-source arguments, SHALL support a
local replay source without network, and WHERE live mode is selected SHALL
require an explicit opt-in plus explicit provider, model and auth profile with
positive call/token/duration/replan and I/O budgets, SHALL reuse feature 9
composition and SHALL define no default provider, model or profile.

#### Acceptance criteria

- GIVEN replay mode
  WHEN run without credentials or network
  THEN all cases and comparisons can complete from local files.
- GIVEN live mode without opt-in, provider, model, profile or any required
  positive budget
  WHEN parsed
  THEN no credential is resolved and no model call occurs.
- GIVEN valid live configuration
  WHEN composed
  THEN credentials are resolved only behind the feature 9 authenticated
  transport and are absent from runner values, manifests and diagnostics.

### R11 — Límites, cancelación y orden estable

WHEN a run is admitted,
THE runner SHALL enforce explicit positive limits for dataset/files/bytes,
cases, result bytes, total wall duration and artifact bytes in addition to each
case's feature 10 limits and live session budgets, SHALL process v1 cases
sequentially in manifest order, SHALL propagate context through loading,
generation, scoring, comparison and publication, and SHALL use checked integer
arithmetic without unbounded allocation.

#### Acceptance criteria

- GIVEN a pre-cancelled context or a preflight limit excess
  WHEN invoked
  THEN no case/model work starts.
- GIVEN cancellation during a case, scoring, comparison or publication
  WHEN observed
  THEN work stops at a documented boundary, no later case starts and no partial
  bundle is labeled complete.
- GIVEN values near integer or byte limits
  WHEN aggregated
  THEN overflow is rejected as `limit_exceeded`; it cannot wrap into a passing
  score or budget.

### R12 — Verificación hermética y separación de `go test`

WHEN ordinary repository verification runs,
THE implementation SHALL test loaders, runner, oracle, metrics, artifacts,
criteria, comparison and command parsing with local fixtures, fake clocks,
scripted models and injected filesystem failures, SHALL require no network,
credentials or production target, and SHALL NOT invoke the evaluation command
or a real provider implicitly from `go test`.

#### Acceptance criteria

- GIVEN `go test ./...` and `go test -race ./...` in an offline environment
  WHEN run
  THEN all tests pass, no eval subprocess starts and no external socket or
  credential resolver is used.
- GIVEN each R1–R11
  WHEN traceability is reviewed
  THEN at least one named hermetic test directly proves its observable
  behavior.
- GIVEN a real-provider eval
  WHEN desired
  THEN it is started only by the separate command with explicit live opt-in and
  is never evidence that replaces deterministic unit/integration tests.

## Formatos y reglas canónicas v1

- Dataset, caso y criterios son JSON UTF-8 estricto: un valor raíz, claves
  únicas, campos desconocidos rechazados y números enteros donde el schema los
  define.
- Los manifests listan casos en orden de dataset. Los sets estructurales se
  normalizan por clave canónica antes de hashing y comparación.
- Los scores se expresan como `{numerator, denominator, ppm, applicable}`;
  `ppm = floor(numerator * 1_000_000 / denominator)` con aritmética comprobada.
- El digest canónico usa SHA-256 sobre JSON compacto con claves de objeto
  ordenadas, arrays semánticos en su orden definido y sin timestamps, paths de
  salida ni duración observada para `result_digest`.
- Un caso referencia una fuente OpenAPI local y una selección de operaciones;
  `ReducedContext` se construye con el inspector, nunca se deserializan sus
  campos privados ni se acepta una URL.
- Un descriptor serializado usa un `contract_id` versionado que composición
  resuelve a un validador local allowlisted; un dataset nunca contiene código o
  handles ejecutables.
- Logs y respuestas maliciosas se modelan como fragmentos externos tipados que
  el fixture inserta en campos de texto ya tratados como datos no confiables.
  No se añade un canal privilegiado ni se cambia el generador.
- El bundle v1 contiene `manifest.json`, `cases.jsonl`, `metrics.json` y, si se
  solicitó, `comparison.json`. `manifest.json` se publica al final y es el único
  marcador de completitud.

## Errores y casos límite

- Los errores distinguibles son `invalid_input`, `unsupported_version`,
  `integrity_failure`, `limit_exceeded`, `cancelled`, `timeout`,
  `model_failure`, `artifact_failure` y `incompatible_baseline`.
- Los errores incluyen stage y path estructural generados por el sistema; no
  interpolan contenido de fixtures, outputs ni errores externos.
- Un error operativo de un caso no se convierte en una métrica favorable. El
  criterio versionado decide fail-fast, pero todo caso no completado queda
  fallido y conserva una categoría segura.
- Un `rejected` esperado solo pasa si la categoría/etapa permitida coincide;
  cualquier panic, fallo del writer o error no clasificado es fallo de sistema.
- `not_applicable` no aporta numerador ni denominador y debe aparecer
  explícitamente en métricas y comparación.
- La baseline se lee como dato no confiable y se verifica íntegramente antes de
  usarla.

## Decisiones abiertas

Ninguna. Los valores concretos de thresholds, hard-fails y tolerancias de
regresión son inputs explícitos del archivo de criterios versionado; la suite
no inventa ni aplica valores por defecto.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-15T16:53:38+02:00
```
