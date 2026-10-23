# Validación de flujos funcionales reales — Design

## Resumen de la solución

Se añadirá un paquete de dominio/aplicación `internal/functionalflow` que
normaliza un flujo generado o proporcionado, sella su autoridad y coordina el
grafo de operaciones de negocio. No reemplaza `planning`, `assertion`,
`testidentity`, `fixture` ni `validationworkflow`: compone sus contratos y
añade la unidad que falta para referencias de fixture, pasos multi-restricción,
observaciones pre/post y un ledger funcional común.

```text
provided manifest | generated PreparedPlan/replan
                 \   /
          FlowNormalizer (untrusted source)
                    |
     FlowValidator + Binder + canonical artifact
                    |
     validationworkflow lease/CAS + durable revision
                    |
            fixture.Prepare (optional)
                    |
      stable topological business-step executor
        observe -> act -> observe -> comparisons
                    |
       immutable assertion/evidence ledger
                    |
          deterministic primary verdict
                    |
       fixture.Cleanup (finite own scope)
                    |
 verified package/report + safe surface projection
```

El executor conserva el orden de controles de arquitectura. Las referencias se
resuelven en copias privadas después de comprobar autoridad. El transporte
autenticado es la única frontera que ve credenciales; el resolver privado de
fixtures es la única frontera adicional que ve captures. El modelo recibe
descriptores y referencias, no valores ni handles ejecutables.

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `feature_list.json` | Feature 27 única, estado pending, SDD y acceptance observable. |
| `docs/architecture.md` | Modelo no autoritativo, policy/budget antes de tools, assertions deterministas, journal y secretos. |
| `docs/conventions.md`, `docs/specs.md`, `docs/verification.md` | Interfaces consumidoras, contextos, ownership, comentarios sistémicos, trazabilidad, race y coverage. |
| `specs/multi-step-validation-plan`, `internal/planning` | Grafo estable, referencias a outputs, estados de step y bloqueos. |
| `specs/assertion-engine`, `internal/assertion` | Assertions locales, igualdad JSON, evidence IDs y distinción failed/inconclusive/error. |
| `specs/agent-plan-generation`, `internal/agentplanning` | Propuesta no confiable, `PreparedPlan`, evidence requirements y bindings de operación. |
| `specs/bounded-replanning`, `internal/replanning` | Revisiones adicionales, monotonicidad, fingerprints, budgets y approval por lote. |
| `specs/validation-workflow-orchestration`, `internal/validationworkflow` | Admisión, fases, artifact store, lease/CAS, recovery y paridad de adapters. |
| `specs/generated-contract-review` | Solo un catálogo aprobado concede operaciones ejecutables. |
| `specs/test-identities`, `internal/testidentity` | Identity binding exacto e inyección tardía sin secretos. |
| `specs/fixture-api`, `internal/fixture` | Captures privadas, prepare/cleanup, intents, recovery y resultado de cleanup separado. |
| `specs/evidence-package`, `internal/evidence`, `internal/localvalidation` | Evidence confirmada, package verificable, redacción y publicación atómica. |

No se consultó documentación externa. La feature compone contratos internos y
el transporte HTTP existente; no fija un nuevo proveedor, backend de secretos o
servicio remoto.

## Contratos observables

### `FunctionalFlow/v1`

`Flow` contiene `ID`, `Source`, `Revision`, `ContextDigest`, `Steps` y
`RequiredEvidence`. `Source` distingue `provided` de `generated` para auditoría,
sin alterar authority ni ejecución. `Revision` empieza en uno y aumenta solo por
un lote adicional confirmado.

`Step` contiene ID, kind, constraint ID, dependencias, `OperationReference`,
identity ID obligatorio, input estructurado, assertions locales, comparaciones
y evidence requirements. El modelo y los manifests usan IDs/referencias; los bindings
ejecutables se derivan del contexto retenido.

El input reutiliza nodos literal/object/array, pero la referencia se eleva a
una unión propia:

```text
Reference = FixtureCaptureReference | StepEvidenceReference
```

`FixtureCaptureReference` identifica fixture/output/type. El valor se recupera
del store privado de feature 26. `StepEvidenceReference` identifica step/path/
type y recupera el output terminal mediante evidence ID confirmado. Ninguna de
las dos serializa el valor resuelto en el artefacto.

`ObservationComparison` contiene ID, before step/path, after step/path,
value type y operator `equal|not_equal`. El validator exige dos steps
`observe`, misma operación canónica, target y selector estructural de recurso,
ancestry before→after e IDs únicos. Justo antes de evaluar se comprueba además
que path parameters y query resueltos producen el mismo resource-binding digest
opaco. El evaluador devuelve un `AssertionResult` propio con ambos evidence IDs,
valores ya saneados/permitidos y estado ternario.

### Artefacto canónico y procedencia

`FlowArtifact/v1` contiene semantic digest, grafo canónico, revisión, source
seguro y digests de
specification, revision, target, contrato aprobado, identities, policy,
fixtures, capabilities y runtime limits. Incluye el digest del artefacto y la
provenance de generación o del manifest, pero no paths, prompts, responses,
captures, credential refs, idempotency keys ni handles ejecutables.

Un flujo generado se obtiene de checks ya preparados y vuelve a validarse al
normalizarlo. Un flujo proporcionado usa un adapter JSON estricto versionado.
CLI puede cargarlo desde una ruta local controlada; API/MCP/GitHub solo reciben
un alias o artifact ID autorizado resuelto durante composición. Ambos caminos
producen el mismo `FlowArtifact`.

### Límites y policy

`FlowLimits` es configuración confiable con máximos de pasos, assertions,
comparaciones, referencias, profundidad, bytes resueltos, tool calls, retries,
trabajo determinista, revisiones/replans, timeout por paso y duración del run.
El runtime reserva globalmente y por step antes del trabajo. La fuente puede
solicitar menos capacidad, nunca más. Cleanup conserva sus budgets separados de
feature 26.

### Resultado observable

`FunctionalFlowResult` contiene flow/revision IDs, estado de cada step en orden
declarado, ledger de assertions, constraints agregadas, `PrimaryOutcome`,
evidence IDs y lineage. `FixtureSummary` y `CleanupReport` se adjuntan como
sección operativa independiente. La proyección segura no incluye inputs
resueltos, responses completas ni valores de captures.

## Componentes y responsabilidades

### `internal/functionalflow`

- `Normalizer`: traduce fuentes generadas/proporcionadas al modelo canónico sin
  otorgar autoridad.
- `Validator`: valida IDs, tipos, grafo, ancestry, evidence requirements,
  observations y límites; produce un snapshot inmutable y orden estable.
- `Binder`: liga cada step a constraint, target, catálogo aprobado,
  capability, identity explícita y digests; rechaza drift y clasificaciones de efecto
  incompatibles.
- `InputResolver`: crea inputs efímeros desde literales, captures privadas y
  evidencia terminal confirmada, con límites y revalidación.
- `ComparisonEvaluator`: evalúa `equal|not_equal` entre dos observaciones sin
  coerciones y conserva ambos evidence IDs.
- `Ledger`: snapshot append-only de resultados deterministas y required
  evidence; agrega constraints/flow sin modelo.
- `Executor`: posee estado de steps, intents, retries, bloqueos, budgets y orden
  secuencial. Depende de puertos mínimos para policy, tool, evidence, store,
  clock y wait.
- `Projector`: genera vistas allowlisted para workflow, package y adapters.

### `internal/planning`, `internal/assertion` y `internal/agentplanning`

Se reutilizan validación de input, assertions locales, operaciones preparadas y
evidence requirements. Las extracciones necesarias deben ser mínimas: no se
debilitan reglas de planes históricos ni se enseña al assertion engine a leer
stores. El protocolo generado puede incorporar kind, dependencias globales y
referencias nominales, pero el normalizer vuelve a aplicar todos los guards.

### `internal/fixture`

Expone un puerto consumidor para resolver una capture confirmada mediante
run/fixture/output/type sin sacar el valor de la frontera privada. El lifecycle
prepare/cleanup, sus estados, scopes, budgets, retries, intents y reports no se
reimplementan. El flow solo puede comenzar tras `PreparedSnapshot` confirmado.

### `internal/validationworkflow`

Sigue siendo propietario del run y único mutador lógico. Añade fases/checkpoints
para `flow_ready`, `fixtures_prepared`, `flow_executing`, `flow_replanning` y
`flow_finalizing`, y guarda `FlowArtifact`/ledger mediante CAS. Delega business
steps a `functionalflow.Executor`, replanning al coordinator existente y
cleanup a fixture lifecycle.

### Evidence, package y report

Cada intento produce evidencia saneada con run/revision/step/binding. El ledger
referencia evidence IDs, no payloads. El package incorpora artifact digest,
orden, steps, assertions locales/comparativas, primary outcome y sección de
fixture/cleanup. Un reader reconstruye y verifica todas las relaciones antes de
proyectar `passed`.

### Adapters y composición

`cmd/featureproof`, API Go, MCP y GitHub delegan en el mismo
`validationworkflow.Service`. La composición resuelve aliases, provided
artifacts, modelo, contrato, identity resolvers, transports, stores y límites.
Core/application no leen argv, environment, credenciales ni paths.

Las cuatro superficies exponen handle, phase, safe step/constraint status,
primary outcome, evidence references y cleanup status. Solo CLI local puede
aceptar una ruta de manifest; las superficies remotas usan selecciones
preautorizadas y no reciben operaciones/captures inline.

## Paquetes afectados

```text
internal/functionalflow/      # modelo, normalización, validación, binding, executor, ledger
internal/planning/            # extracciones mínimas para inputs/evidence, sin cambiar semántica previa
internal/assertion/           # reutilización de paths/igualdad mediante contrato explícito
internal/agentplanning/       # forma generada de flow y normalización all-or-nothing
internal/replanning/          # revisiones adicionales monotónicas del mismo flow
internal/fixture/             # puerto privado de captures y lifecycle compartido
internal/validationworkflow/  # orquestación, artifacts, CAS, recovery y finalización
internal/localvalidation/     # adapter de manifest proporcionado al mismo ExecutionPort
internal/planmanifest/        # compatibilidad histórica o bridge a manifest funcional versionado
internal/evidence/            # binding/lineage y redacción de nuevas referencias
internal/report/              # proyección ordenada del flujo si el paquete existente lo requiere
internal/externalagent/       # API/MCP y schemas seguros
internal/githubpr/            # gateway/proyección del mismo workflow
cmd/featureproof/             # wiring y selección local, sin reglas de dominio
```

La distribución exacta puede variar si mantiene cohesión y dirección de
dependencias. No se crea un microservicio, scheduler distribuido ni backend de
secretos nuevo.

## Flujo principal

1. El adapter autentica al caller y composición resuelve specification v3,
   revision, target, contrato aprobado, identities, fixture set, policy,
   runtime y origen del flujo.
2. El normalizer traduce el manifest o `PreparedPlan` sin handles. Validator
   comprueba todo el contrato, grafo, references, assertions y límites.
3. Binder deriva operaciones/identidades exactas, comprueba effect class y
   sella `FlowArtifact/v1` con digests. Approval, cuando está habilitado, liga
   exactamente ese artifact/revisión.
4. Workflow admite idempotentemente el run, reclama lease y confirma artifact
   antes de efectos.
5. Fixture lifecycle prepara en su orden estable. Un fallo salta directamente
   a primary terminal operativo y cleanup.
6. Executor selecciona el siguiente step ready. Confirma claim y, para `act`,
   execution intent antes de cualquier posible I/O.
7. Resuelve references en copia, revalida binding/policy/budgets/identity/
   contexto y ejecuta mediante el HTTP tool controlado.
8. Redacta y persiste evidencia terminal. Evalúa assertions locales y, cuando
   ambas observaciones están disponibles, comparaciones. Anexa ledger y estado.
9. Marca dependientes blocked cuando corresponde y continúa ramas independientes
   secuencialmente.
10. Si replanning está habilitado, construye resumen seguro desde el ledger. Un
    lote adicional vuelve a pasos 2–9 con nueva revisión/approval, sin repetir
    prepare ni modificar resultados anteriores.
11. Agrega constraints y primary outcome desde el ledger.
12. Inicia siempre cleanup con scope propio. Adjunta su report sin cambiar el
    primary outcome.
13. Publica y verifica package/report atómicos; las superficies proyectan la
    misma vista segura.

## Flujos de error

1. **Fuente/flow/grafo inválido:** pre-admission o terminal seguro sin fixture,
   modelo adicional, resolver ni red.
2. **Binding/identity/policy/límites inválidos:** fail-closed antes de prepare.
3. **Preparación fallida o incierta:** cero business steps; primary
   inconclusive/error y cleanup aplicable.
4. **Reference no resoluble:** step inconclusive, dependientes blocked; no input
   parcial ni tool call.
5. **Action incierta:** si no hay idempotencia/reconcile no se repite; primary
   inconclusive y cleanup.
6. **Assertion failed:** fallo funcional confirmado; dependientes se bloquean,
   ramas independientes pueden continuar.
7. **Evidence/assertion/journal corrupto:** error de FeatureProof; no se publica
   passed y se inicia cleanup.
8. **Replan inválido/repetido/agotado:** conserva ledger; finaliza según
   evidencia disponible y limpia.
9. **Cancelación:** no empieza otra unidad; conserva prefijo, detiene owners y
   cambia a cleanup finito.
10. **Cleanup parcial/fallido:** primary queda inmutable; report operativo
    conserva incidencias y recovery pendiente.
11. **Package/publish incierto:** recovery verifica intent/digest; no publica
    dos packages ni recalcula el veredicto desde logs.

## Estados y transiciones

Estado agregado del flujo dentro del workflow:

```text
validating -> flow_ready -> preparing_fixtures -> executing
                                           \        |
                                            \       v
                                             -> replanning -> flow_ready(revision+1)
executing/replanning/preparing_fixtures -> primary_terminal
primary_terminal -> cleaning -> finalizing -> terminal
```

`primary_terminal` conserva `passed|failed|inconclusive|cancelled|error` y no se
reescribe. `cleaning` conserva por separado
`not_required|pending|cleaned|partial|failed|exhausted`.

Estado por step:

```text
declared -> ready -> claimed -> resolving -> authorized -> executing
executing -> evidence_confirmed -> evaluating -> passed|failed|inconclusive
declared/ready -> blocked
claimed/resolving/authorized/executing/evaluating -> cancelled|error
```

Un `act` intercala `intent_confirmed` entre `claimed` y `executing`. Solo un
estado con evidencia terminal puede llegar a `evaluating`. `blocked` nunca tiene
tool start. Repetir una transición con misma identity converge; una transición
contradictoria falla CAS.

## Propiedad del estado

- `ValidatedFlow` posee copias inmutables del grafo, orden y referencias.
- `BoundFlow` posee authority/digests no secretos; no posee transports.
- `validationworkflow` posee lifecycle, lease, revisiones y primary result.
- `functionalflow.Executor` es el único mutador lógico de steps/ledger durante
  una revisión.
- `FixtureStore` posee captures crudas confirmadas; `InputResolver` solo las
  toma prestadas para un input efímero.
- `EvidenceStore` posee outputs saneados; ledger/package solo sus IDs y
  proyecciones permitidas.
- Cada tool attempt posee request, credential lease e idempotency key; no
  comparte buffers con otros pasos.
- Stores poseen eventos/intents/artifacts; logs y observers no son estado.

## Concurrencia, ownership y cancelación

Un flujo es secuencial. Cada run/revisión/step usa claim por lease y CAS; runs
independientes pueden progresar en paralelo. Los locks protegen solo memoria o
reemplazo atómico y nunca se mantienen durante I/O, callbacks o waits.

No se crean goroutines en constructores. Todo worker tiene owner, `Wait/Close`
y condición de salida. Cada operación bloqueable recibe `context.Context` como
primer argumento. Deadlines de step son el mínimo entre parent, run y límite de
step. Cancel se persiste antes de señalizar al worker.

Cleanup usa exclusivamente el `CleanupScopeFactory` de feature 26. Puede
derivarse de un shutdown scope explícito con deadline, no del run cancelado ni
de un `context.Background()` oculto. Finalization espera a todos los owners y
no deja retries en background.

## Recovery, redelivery, retries e idempotencia

Store confirma, como mínimo, `flow_artifact_committed`, `fixtures_prepared`,
`step_claimed`, `action_intent_confirmed`, `attempt_started`,
`evidence_confirmed`, `assertions_confirmed`, `step_terminal`,
`primary_terminal`, `cleanup_terminal/pending` y `package_published`.

Recovery revalida artifact/digests y reclama solo leases expiradas:

- artifact sin prepare: puede iniciar prepare una vez;
- fixture prepare parcial: delega recovery al lifecycle existente;
- step sin intento: puede reclamar y empezar;
- observación incierta: solo repite si el binding/idempotencia/budgets lo
  permiten; de lo contrario inconclusive;
- action intent sin terminal: reconcilia o reutiliza key contractual; sin
  garantía no repite;
- evidence confirmada sin assertions: evalúa desde la misma evidencia;
- assertions confirmadas sin step terminal: converge por CAS;
- primary terminal: nunca reabre ejecución, solo cleanup/finalization;
- cleanup/package: usa intents/digests de sus owners y converge.

Retries son propiedad del executor/lifecycle, no del HTTP transport. Requieren
categoría transitoria, garantía contractual, budget, contexto y misma key
lógica. Cada intento tiene ordinal y evidencia propia. Assertions failed,
denials permanentes, input/capture inválido, drift y corrupción no se reintentan.

## Replanificación

El coordinator existente recibe una proyección segura del ledger y remaining
budgets. Una propuesta adicional se normaliza como `FlowRevision`, se valida
atómicamente y obtiene fingerprint semántico. Puede referenciar pasos
confirmados anteriores, pero no mutarlos. No puede cambiar fixture set, target,
contrato, constraints o identities autorizadas. Approval exacta se repite si la
policy lo exige.

El ledger es monotónico entre revisiones. Los IDs globales de step/assertion no
colisionan y lineage une revisión, model decision, summary digest, approval,
steps y evidence. Fixtures se preparan una vez y cleanup comienza solo tras el
primary terminal definitivo.

## Seguridad y redacción

Artifacts, events, errors y projections usan allowlists. Nunca incluyen raw
captures, credential refs/material, leases, idempotency keys, resolved inputs,
URLs completas con valores, bodies sin sanear, external error strings, prompts,
model outputs ni chain of thought.

El modelo recibe constraint, operation/identity descriptors, graph metadata,
reference IDs/types y resumen de evidence/ledger saneado. Las respuestas,
captures, OpenAPI y texto de PR permanecen en envelopes no confiables. Ningún
contenido de datos puede crear una operación o cambiar privileged instructions.

Se ejecuta una matriz recursiva de canarios sobre artifacts, state, stores,
journal, evidence, package, report, logs, metrics, model requests, CLI, API, MCP
y GitHub. Fallar redacción detiene la publicación.

## Observabilidad

Eventos seguros incluyen run/flow/revision/step IDs, sequence, attempt,
operation-binding digest, identity ID, phase, outcome, category, duration,
budget counters y evidence IDs opacos. Comparaciones incluyen before/after step
IDs, resource-binding digest opaco y evidence IDs, nunca selectores resueltos ni
valores no permitidos. Fixture/cleanup conserva sus records propios enlazados
por run ID.

Métricas agregadas: flows por source/outcome, steps por kind/status, blocked,
replans, recovery, retries, budget exhaustion y cleanup status. Logs no son
fuente de estado ni contienen inputs/responses.

## Convención de documentación del código

Toda entidad, constante, función, método y helper nuevo o modificado, exportado
o interno, incluirá un comentario breve que explique su funcionalidad dentro de
FeatureProof y la frontera o invariante que protege. La regla incluye DTOs,
adapters, callbacks, factories, fakes, fixtures programáticas y helpers de test.
Los comentarios se actualizarán cuando cambie la responsabilidad y no repetirán
solo el nombre, parámetros o sintaxis.

## Estrategia de pruebas

Los tests ordinarios son herméticos: manifests/modelo falsos, contract catalog
local, policy/identity/credential fakes con canarios, fixture lifecycle fake o
store temporal real, HTTP server local, evidence/journal/artifact stores
temporales, clock/waiter/IDs controlados y barriers/channels. No usan Internet,
OAuth, credenciales reales, backend desplegado, señales ni `time.Sleep`.

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | normalizer/model/artifact | `TestGeneratedAndProvidedFlowsNormalizeToSameCanonicalGraph`; `TestFlowContractIsStrictImmutableAndBackwardCompatible` |
| R2 | binder/catalog/identity | `TestFlowStepsRequireExactConstraintOperationTargetAndIdentity`; `TestObserveActClassificationCannotLieAboutEffects` |
| R3 | validator/resolver | `TestFlowGraphOrderAndTypedReferencesAreStable`; `TestInvalidGraphOrReferenceProducesZeroEffects` |
| R4 | workflow/fixture bridge | `TestFixturesPrepareBeforeAnyBusinessStep`; `TestPreparationFailureBlocksFlowAndStillCleans` |
| R5 | executor/policy/budgets | `TestEveryStepRechecksControlsInRequiredOrder`; `TestPerStepDenialAndExhaustionOpenNoNetwork` |
| R6 | comparison evaluator | `TestBeforeAfterObservationsProveMutationAndNoMutation`; `TestComparisonRequiresTwoCompatibleConfirmedEvidences` |
| R7 | ledger/verdict | `TestDeterministicLedgerOverridesAgentClaims`; `TestFlowVerdictRequiresAllRequiredEvidence` |
| R8 | scheduler | `TestBlockedStepsRecordAllCausesAndIndependentBranchesContinue`; `TestCancellationStopsEveryUnstartedBranch` |
| R9 | intents/retry/recovery | `TestActionIntentPrecedesNetworkAndUncertainEffectsAreNotDuplicated`; `TestOnlyVerifiedTransientOperationsRetryWithStableKey` |
| R10 | replanning | `TestAdditionalRevisionIsAtomicApprovedAndMonotonic`; `TestReplanCannotMutateFixturesAuthorityOrConfirmedResults` |
| R11 | fixture cleanup/result | `TestCleanupRunsAfterEveryPrimaryOutcomeWithoutChangingVerdict`; `TestCleanupRecoveryConvergesSeparately` |
| R12 | journal/package/report | `TestFlowPackageReconstructsFullOrderedLifecycle`; `TestMissingForkedOrForeignEvidenceFailsClosed` |
| R13 | store/ownership/context | `TestConcurrentOwnersAndRedeliveryDoNotDuplicateEffects`; `TestCrashAndCancellationMatrixConvergesAndJoins` |
| R14 | redaction/model/surfaces | `TestFlowCanariesNeverCrossDurableModelOrExternalBoundaries`; `TestUntrustedDataCannotCreateAuthority` |
| R15 | adapters/gates | `TestFunctionalFlowParityAcrossCLIAPIMCPAndGitHub`; `TestFunctionalFlowSystemicCommentsAndVerificationGates` |

Se añaden matrices para flujo positivo completo, autorización entre dos
usuarios, before/action/after, fallo funcional, dependencia bloqueada,
cancelación en cada frontera, cleanup parcial, exact budget boundaries,
provided/generated equivalence, malformed/oversized input, drift, duplicated
delivery, lease handoff, crash checkpoints, store corruption y canarios.

Las suites afectadas se ejecutan repetidamente y bajo `-race`. Se verifica
cross-build Linux/Darwin/Windows cuando el host lo permita y al menos 85 % de
statement coverage sobre el código productivo nuevo o modificado, además de la
trazabilidad R→test real.

## Alternativas y decisiones

- **Ejecutar fixtures como pasos del agente:** descartado; prepare/cleanup son
  lifecycle confiable y no evidencia funcional.
- **Encadenar los planes existentes sin un modelo común:** descartado; no
  validaría referencias multi-restricción, observaciones comparadas ni recovery
  como una unidad.
- **Dar al agente los valores de captures:** descartado; solo necesita IDs y
  tipos, y el valor se resuelve dentro del runtime autorizado.
- **Inferir dependencias desde referencias:** descartado; el grafo debe ser
  explícito y revisable.
- **Comparar pre/post mediante texto del modelo:** descartado; se usan dos
  evidencias y un evaluador determinista.
- **Permitir herramientas diferentes para before/after:** descartado en v1;
  demostrar cambio/ausencia de cambio exige la misma operación y target.
- **Repetir una action incierta para averiguar el resultado:** descartado salvo
  idempotencia/reconciliación contractual.
- **Abortar todas las ramas ante un functional failed:** descartado; ramas
  independientes pueden aportar evidencia adicional mientras conserven
  autoridad y budgets.
- **Hacer cleanup parte del veredicto:** descartado; es higiene operativa y se
  reporta por separado.
- **Aceptar manifests inline en superficies remotas:** descartado; usar aliases
  impide ampliar operaciones o leer paths arbitrarios.
- **Crear otro workflow/runner:** descartado; la feature extiende el caso de uso
  compartido y conserva una sola autoridad.

## Conformidad arquitectónica

El diseño preserva ARCH-001/002 al mantener tools y bindings fuera del modelo;
ARCH-003/004 mediante ledger/evidence y veredicto determinista; ARCH-005 con
budgets por run/step/replan; ARCH-006 con contextos y cleanup scope; ARCH-007
con journal append-only; ARCH-008/009 mediante separación de datos no confiables,
captures y credenciales; ARCH-010 con fakes herméticos; ARCH-011 con policy en
cada intento; ARCH-012 porque tools no mutan el run.

Cumple CONV-001 a CONV-010: paquetes cohesionados, interfaces consumidoras,
errores tipados, ownership/CAS, cancelación, comentarios sistémicos, cero
secretos y tests deterministas. No se solicita excepción arquitectónica.

## Mapa inicial R → componente/prueba

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T16 | Ambos orígenes producen snapshot estricto, inmutable y compatible. |
| R2 | T3, T4, T16 | Cada step queda ligado exactamente a autoridad existente. |
| R3 | T3, T5, T16 | Grafo/references completos se validan antes de efectos y ejecutan en orden estable. |
| R4 | T6, T12, T16 | Prepare confirmado precede business work y no se repite en replans. |
| R5 | T7, T8, T16 | Cada step atraviesa policy/budget/identity/tool sin autoridad implícita. |
| R6 | T9, T16 | Pre/post produce comparaciones deterministas con dos evidencias. |
| R7 | T9, T10, T16 | Ledger y verdict son inmutables y externos al modelo. |
| R8 | T7, T10, T16 | Bloqueos causales no ejecutan tools y ramas independientes continúan. |
| R9 | T8, T11, T16 | Intents/retries/recovery no duplican efectos inciertos. |
| R10 | T11, T12, T16 | Replans son nuevas revisiones acotadas y monotónicas. |
| R11 | T12, T13, T16 | Cleanup siempre se intenta y nunca cambia primary result. |
| R12 | T10, T13, T16 | Journal/package/report reconstruyen todo el lifecycle. |
| R13 | T11, T12, T15, T16 | Lease/CAS/cancelación/crashes convergen y cierran owners. |
| R14 | T5, T8, T13, T14, T16 | Captures/credenciales/keys/datos no saneados no cruzan fronteras. |
| R15 | T2, T14, T15, T16 | Las cuatro superficies comparten servicio y gates herméticos. |
