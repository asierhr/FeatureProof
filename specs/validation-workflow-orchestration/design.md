# Flujo completo de planificación y validación — Design

## Resumen de la solución

Se añadirá `internal/validationworkflow`, un paquete de aplicación que compone
las puertas existentes sin reimplementar sus reglas. Un workflow posee un
journal durable, checkpoints de fase, el artefacto canónico del plan y una
referencia al package terminal. Los adapters solo traducen solicitudes.

```text
explicit StartCommand
       │
       ▼
preflight: spec + revision/environment + target/preview
           + OpenAPI reduced context + descriptors/policy/budgets
       │                  invalid ──> pre-admission error (no handle)
       ▼
durable admission + lease
       │
       ▼
model Session -> agentplanning.Generator
       │
       ├─ insufficient_context ──> terminal (no tools/package)
       │
       ▼
PreparedPlan -> canonical PlanArtifact + digest
       │
       ├─ approval required -> planapproval -> awaiting_approval
       │                                      │ exact one-use grant
       └─ approval disabled, policy permits ───┘
                                              ▼
                         recheck binding/policy/budget/preview
                                              ▼
                  existing localvalidation/planning runner
                                              ▼
                    evidence -> assertions -> verdict -> package
                                              ▼
                      terminal projection + preview release
```

La admisión queda después de todo preflight puramente verificable y antes de la
primera inferencia. Así los errores de input no crean runs, mientras los fallos
de operaciones ya iniciadas quedan auditados bajo un handle. El modelo produce
una propuesta; `PreparedPlan`, approval grant, policy runtime y runner
determinista conservan la autoridad.

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `feature_list.json` | Alcance único de feature 20, SDD y acceptance. |
| `docs/architecture.md` | Modelo no autoritativo, herramientas tras policy/budget, journal, estados y secretos. |
| `docs/conventions.md`, `docs/specs.md`, `docs/verification.md` | Interfaces consumidoras, cancelación, CAS, comentarios, trazabilidad, race y coverage. |
| `specs/model-adapter`, `internal/model` | Selección explícita, sesión neutral, budgets, structured calls, OAuth solo en composición y telemetría segura. |
| `specs/agent-plan-generation`, `internal/agentplanning` | `GenerationRequest`, protocolo estricto, `PreparedPlan`, procedencia y preparación all-or-nothing. |
| `specs/plan-approval`, `internal/planapproval` | Vista saneada, hashes de contexto, CAS, acciones cerradas y grant opaco de un uso. |
| `specs/bounded-replanning`, `internal/replanning` | Budgets/ledger y regla de volver a validar todo plan adicional; esta feature no duplica el loop. |
| `specs/revision-bound-validation`, `internal/revisionbinding` | Repository/OID/environment/binding inmutables y rechecks. |
| `specs/evidence-package`, `internal/localvalidation` | Publicación atómica, package verificado y runner actual. |
| `specs/external-agent-interface`, `internal/externalagent` | API común, identidad, aliases, handles, idempotencia, CLI/MCP y supervisión durable. |
| `specs/github-pr-integration`, `internal/githubpr` | Head exacto, authorizer, check/comment, delivery idempotente y gateway de validación. |
| `specs/preview-environment-adapter`, `internal/previewenv` | Reserva, attestation, target policy, secretos de composición, release y recovery. |
| `specs/local-validation-cli`, `internal/planmanifest`, `internal/localvalidation` | Contrato histórico con plan explícito, ejecución/reporting/publisher reutilizables. |
| `internal/openapi`, `internal/planning`, `internal/policy`, `internal/evidence` | Contexto reducido, validación/ejecutor, default-deny y redacción. |

No se introduce un contrato externo nuevo. MCP y GitHub siguen los contratos ya
observados y documentados por features 16 y 18; esta feature amplía adapters
propios y sus versiones de aplicación.

## Componentes y responsabilidades

### `validationworkflow.Service`

Caso de uso compartido con operaciones `Start`, `Status`, `ApprovalView`,
`Decide`, `Cancel`, `Result` y `Recover`. No lee argv, environment, archivos de
configuración ni credenciales. Coordina fases, checkpoints y puertos propios.

### `AdmissionBuilder`

Recibe un `StartCommand` ya autenticado y una selección confiable resuelta por
composición. Carga/valida specification y contrato por ports acotados, obtiene
revision/attestation/target autorizados, reduce OpenAPI y produce un
`AdmissionSnapshot` inmutable con digests. Para preview delega reserva y
cleanup al coordinator existente. No llama al modelo.

### `WorkflowStore`

Interfaz consumidora para reservar idempotencia+handle, anexar eventos mediante
CAS, confirmar artefactos, reclamar/renovar/liberar leases y cargar snapshots.
El adapter local usa un journal append-only y archivos atómicos particionados
por handle, con lock entre procesos y `fsync` equivalente a stores existentes.
Nunca persiste dependencias ejecutables ni secretos.

### `GenerationPort`

Adapter fino sobre `agentplanning.Generator`. Traduce `AdmissionSnapshot` a
`GenerationRequest` y retorna exclusivamente `PreparedPlan` o
`MissingContext`. El `model.Session` ya construido vive detrás de la frontera;
el service solo conoce provider/model/prompt version y usage seguros.

### `PlanArtifactStore`

Serializa el plan preparado a un formato interno versionado y canónico,
incluye los hashes de binding/procedencia y metadata segura, calcula digest y
publica con temp+rename. Al leer, revalida formato, digest, IDs y hashes antes de
reconstruir el valor preparado a través de validadores, nunca por type cast o
vista humana. El mismo contenido alimenta `plan.json` terminal.

### `ApprovalPort`

Bridge a `planapproval.ApprovalWorkflow`. En modo required crea/stagea la vista
y conserva revision/hash. `Decide` recibe actor autenticado, action cerrada,
decision ID y expected revision; solo un `ExecutionAuthorization` consumible
permite ejecutar. En modo disabled un `AutoAuthorizationPolicy` explícito
produce un evento distinto, sin actor humano ni grant falso, y la policy se
revalida antes de ejecutar.

### `ExecutionPort`

Bridge sobre el runner compartido extraído de `localvalidation`. Recibe spec,
target/binding autorizado, runtime profile, planes preparados y package root;
resuelve registry ejecutable justo antes de ejecutar. Reutiliza plan executor,
evidence store, assertion evaluator, verdict, reporting y publisher. El comando
histórico construye el mismo port desde `planmanifest`; no cambia su contrato.

### `PackageReader`

Reutiliza el lector íntegro existente para proyectar terminales. El workflow no
calcula `passed`/`failed` desde eventos de control y no acepta un path del
cliente.

### `CompositionResolver`

Vive en `cmd/featureproof`/adapters. Valida provider/model/auth profile/runtime
profile y target/preview, resuelve secretos, construye transports y devuelve
ports ya autenticados más una `composition_ref` privada. Un registro protegido
de composición permite recovery; el journal público solo conserva su digest.

### Adapters CLI, API, MCP y GitHub

- La API Go es `validationworkflow.Service` y DTOs propios.
- CLI añade `featureproof workflow start|status|view|approve|reject|cancel|result`.
  `start` exige spec, contract, provider, model, auth profile, runtime profile,
  repository, revision, approval mode, idempotency key y exactamente target o
  preview. Los comandos posteriores usan handle y valores CAS/idempotentes.
- MCP incrementa el contract version y anuncia herramientas workflow paralelas
  a las actuales. Para callers externos, paths, URLs y perfiles se sustituyen
  por aliases autorizados; cada alias sella todas las selecciones explícitas.
- GitHub conserva `/featureproof validate`. Su `SelectionAlias` debe resolver a
  una configuración completa de instalación; no existe selección implícita en
  core. El gateway inicia/consulta/cancela el mismo workflow y proyecta
  `awaiting_approval` al check. La aprobación no se infiere del comentario.

## Paquetes afectados

```text
internal/validationworkflow/ # modelo, service, store, artifacts, recovery
internal/localvalidation/    # extracción mínima de ExecutionPort preloaded
internal/agentplanning/      # codec/rehidratación mínima si el artefacto lo requiere
internal/planapproval/       # bridge, sin duplicar workflow ni hashes
internal/externalagent/      # API/CLI/MCP vNext y supervisor adapter
internal/githubpr/           # gateway/proyección de fase workflow
internal/previewenv/         # bridge al mismo ExecutionPort; lifecycle intacto
cmd/featureproof/            # parsing y composición de secretos/selecciones
```

No se cambian reglas de specification, assertions, policy, binding, package ni
provider. Cualquier cambio allí será una extracción mínima para exponer el
contrato ya aprobado.

## Contratos propios

### `StartCommand`

Contiene principal/surface ya autenticados, idempotency key, referencias
confiables a spec/contract/runtime, provider/model, approval mode, repository,
commit completo y una unión exacta `DirectTargetSelection | PreviewSelection`.
El auth profile no entra al command del service: composición lo consume y
entrega una `composition_ref` privada junto al `GenerationPort`.

La versión local de CLI puede aceptar paths y URL porque el operador controla
su proceso. MCP/API/GitHub aceptan aliases autorizados, nunca paths o URL libre.
Ambos adapters terminan en el mismo snapshot canónico.

### Estado durable

```text
admitted -> generating -> plan_ready
    │             │            │
    │             └-> terminal: insufficient_context|inconclusive|error|cancelled
    │                          │
    │                          ├-> awaiting_approval -> authorized
    │                          └-> authorized (approval disabled by policy)
    │                                                │
    └────────────────────────────────────────────────┴-> executing
                                                         -> finalizing
                                                         -> terminal
```

Estados persistidos: `admitted`, `generating`, `plan_ready`,
`awaiting_approval`, `authorized`, `executing`, `finalizing`, `terminal`.
Outcomes terminales: `passed`, `failed`, `insufficient_context`,
`inconclusive`, `cancelled`, `error`. Pre-admission no es un estado durable.

Cada evento v1 incluye sequence, event ID, workflow/run handle, type, attempt,
occurred_at, digests y categorías cerradas. No incluye selection values,
prompts, missing-context libre sin sanear, perfil o errores externos.

### `PlanArtifact/v1`

Incluye:

- workflow/run/plan IDs y artifact version;
- canonical prepared checks, steps, inputs tratados, assertions y evidence
  requirements;
- hashes de constraints, revision binding, target, reduced contract retained
  index, capability descriptors, runtime y generation contract;
- provider, model, prompt/protocol versions y usage confirmado;
- artifact SHA-256.

El formato excluye secrets, auth profile, target credentials, OpenAPI bytes,
prompt/output crudos, approval actor y callbacks. Los mapas se ordenan y los
números conservan representación exacta.

### Resultado público

`WorkflowView` expone handle, phase, outcome opcional, updated_at, approval
revision opcional y categorías cerradas. `WorkflowResult` contiene o bien
missing-context tratado, o bien la proyección del package verificado. No mezcla
ambas variantes. Los errores pre-admission usan códigos `invalid_request`,
`unauthorized`, `conflict`, `unavailable`, `cancelled` o `internal_error` sin
handle; los fallos ya admitidos se registran como outcome.

## Flujo principal

1. Adapter autentica y autoriza la superficie; composición valida selecciones y
   construye ports sin exponer secretos.
2. `AdmissionBuilder` carga spec/contract, fija revisión y target/preview,
   reduce contexto y valida catálogo/policy/budgets.
3. Store reserva key+request digest+handle y lease. Repetición exacta devuelve
   el registro existente.
4. Se confirma `generation_started` antes de invocar al modelo.
5. `GenerationPort` llama una vez a `Generator`.
6. Missing context válido se sanea y termina. Un plan completo se convierte y
   publica como `PlanArtifact`, luego se confirma `plan_ready`.
7. Approval required stagea y espera; disabled requiere policy explícita.
8. Al autorizar, se vuelve a cargar/verificar artefacto y se revalidan
   approval/binding/target/policy/budgets.
9. `ExecutionPort` ejecuta secuencialmente y publica el package usando el plan
   canónico.
10. Se verifica el package, se confirma terminal y el finalizer libera preview,
    lease y recursos. Las superficies proyectan estado desde el store.

## Flujos de error

- Error antes del paso 3: pre-admission; cero estado/side effects del workflow.
  Si preview ya se reservó durante admission, su coordinator posee release aun
  sin handle de workflow.
- Cancelación o fallo de modelo: output parcial descartado; terminal
  cancelled/inconclusive/error según categoría confirmada.
- Plan inválido/denied: terminal inconclusive o error de modelo/policy; cero
  approval/execution.
- Persistencia de plan fallida: no `plan_ready`; recovery no regenera.
- Approval stale/conflict: permanece awaiting o vuelve a generación según
  acción confirmada; cero ejecución.
- Drift antes/durante ejecución: stop y terminal inconclusive/error operativo.
- Tool/assertion: las reglas existentes deciden functional failed,
  inconclusive o error.
- Package corrupto/ausente: error de integridad; no se proyecta un resultado
  funcional.
- Release pendiente: cleanup queda durable y terminal no afirma release.

## Propiedad, concurrencia y leases

Un owner con lease posee una transición bloqueable por workflow. El store hace
CAS por sequence/version y no mantiene file locks durante modelo, preview,
network, tool, waits o callbacks. El owner renueva antes/después de cada
frontera y antes de finalización. Workflows distintos progresan en paralelo;
uno solo mantiene orden secuencial del runner.

Los workers pertenecen al supervisor existente y exponen `Wait/Close`. No se
lanzan goroutines desde constructores. Los adapters de una sola invocación
esperan o entregan handle según su contrato, pero nunca dejan trabajo detached.

## Recuperación e idempotencia

Recovery reclama solo leases expiradas y reconstruye desde eventos+artefactos:

- `admitted`: puede iniciar el primer intento si no existe
  `generation_started`.
- `generating` sin terminal de intento: no repite inferencia incierta; termina
  `inconclusive(interrupted)`.
- `plan_ready`: verifica artefacto y stagea/reanuda approval o autorización.
- `awaiting_approval`: conserva exactamente revision/hashes; no decide.
- `authorized` antes de `execution_started`: puede iniciar una vez tras consumir
  autorización mediante CAS.
- `executing` incierto: consulta los stores existentes; nunca repite una tool
  sin prueba idempotente/reconciliación. Si no puede demostrar un checkpoint,
  termina inconclusive/error y finaliza cleanup.
- `finalizing`: verifica package/publish intent y converge por claves existentes.
- `terminal`: solo proyecta; no muta resultado.

Start, cancel y decisiones usan keys/fingerprints separados. Repetir el mismo
comando devuelve el resultado confirmado; reutilizar key con otro payload es
conflicto.

## Cancelación y timeouts

El config exige límites positivos para admission, model call, approval
lifetime, execution, finalization, release, shutdown y lease. Cada child
deadline es el mínimo del límite de fase, run y parent. Cancel se persiste antes
de señalar al worker. Los loops y waits usan context/waiter inyectado.

El finalizer usa `context.WithoutCancel` únicamente seguido de timeout explícito
y solo para persistencia, package confirmado, release y join; nunca ejecuta una
tool nueva. Fallar cleanup conserva una intención recuperable.

## Seguridad y secretos

Los loaders y context builder tratan spec, OpenAPI, responses y texto externo
como datos. Provider/model son IDs seguros; profile/credentials permanecen en
composición. El modelo recibe ReducedContext y descriptors, no base transports,
registry, policy handles, target credentials o filesystem.

Los snapshots hashados usan proyecciones seguras de `revisionbinding`; hashes
no se calculan sobre secretos. Errores y observación solo incluyen handle,
phase, category, provider/model, duración, usage y contadores. Missing context y
approval view pasan por sanitizer y límites antes de persistir/presentar.

## Observabilidad

Eventos seguros: `workflow_admitted`, `generation_started`,
`generation_finished`, `context_insufficient`, `plan_artifact_committed`,
`approval_requested`, `approval_decided`, `execution_started`,
`execution_finished`, `package_verified`, `cancellation_requested`,
`cleanup_pending`, `workflow_terminal`.

Métricas agregadas: workflows por phase/outcome, latencias de fase, generation
usage, approval wait, recoveries/interrupted, cancellations y cleanup pending.
Logs no son fuente de estado y no incluyen payloads.

## Estrategia de pruebas

Tests de aplicación usan fake model session/Generator, OpenAPI local, preview
provider, attestor, policy, approval store, executor, package reader, clock,
waiter, IDs y store. Failure injection cubre cada checkpoint. Adapters CLI/MCP
comparten fixtures; GitHub usa fake gateway. Ningún test ordinario usa OAuth,
Internet, backend desplegado, señales reales o `time.Sleep`.

| Requirement | Pruebas previstas |
|---|---|
| R1 | `TestStartRequiresEveryExplicitSelectionAndPreservesValidatePlanCommand` |
| R2 | `TestAdmissionValidatesBindingPreviewContractPolicyAndBudgetsBeforeModel`; `TestDeniedTargetOpensNoNetworkAndCreatesNoRun` |
| R3 | `TestGenerationAcceptsOnlyCompletePreparedPlanWithoutTools`; `TestInvalidOrDeniedProposalNeverStagesOrExecutes` |
| R4 | `TestInsufficientContextIsSafeDistinctTerminalWithoutPackageOrTools` |
| R5 | `TestPlanArtifactIsAtomicCanonicalBoundAndReusedByPackage`; `TestArtifactAndJournalExcludeCanaries` |
| R6 | `TestRequiredApprovalBindsExactCurrentHashesAndOneUseGrant`; `TestStaleRejectedAndDisabledApprovalPaths` |
| R7 | `TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage`; `TestDriftStopsBeforeNextBatch`; `TestFunctionalFailureComesOnlyFromAssertions` |
| R8 | `TestPublicLifecycleDistinguishesEveryPhaseOutcomeAndPreAdmissionError`; `TestPackageIsOnlyFunctionalResultAuthority` |
| R9 | `TestConcurrentStartAndRecoveryNeverDuplicateWork`; `TestCrashAtEveryCheckpointConvergesSafely` |
| R10 | `TestCancellationAtEveryPhaseStopsFinalizesReleasesAndJoins`; `TestRepeatedCancelIsIdempotent` |
| R11 | `TestSecretsReachOnlyCompositionTransports`; boundary dependency/AST tests |
| R12 | `TestWorkflowCLIAPIAndMCPContractParity`; `TestGitHubUsesConfiguredWorkflowAndDoesNotAutoApprove` |
| R13 | `TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols`; full package/race/coverage gates |

Además se repiten suites de `agentplanning`, `model`, `planapproval`,
`revisionbinding`, `localvalidation`, `externalagent`, `githubpr` y `previewenv`
para demostrar compatibilidad.

## Alternativas y decisiones

- Generar dentro de `validate --plan`: descartado porque rompería el contrato
  exacto y mezclaría un modo determinista con inferencia implícita.
- Añadir flags opcionales a `run submit`: descartado para no hacer ambiguo el
  contrato external-agent v2; workflow tiene operaciones/versiones propias.
- Persistir output crudo del modelo: descartado por secretos, prompt injection
  y ausencia de autoridad. Se persiste el plan validado y metadata segura.
- Reconstruir plan desde ApprovalView: descartado porque la vista saneada es
  presentación, no autoridad.
- Reintentar modelo/tool tras crash: descartado cuando el resultado es incierto;
  podría duplicar consumo o efectos.
- Aprobar automáticamente al comentar en GitHub: descartado porque el plan aún
  no existía y no podía ser revisado.
- Duplicar runner en el nuevo paquete: descartado; se extrae un port preloaded
  del flujo local ya verificado.
- Guardar auth profile en journal para recovery: descartado; composición usa una
  referencia privada protegida y el core conserva solo digest.

No se solicita excepción a `docs/architecture.md` ni
`docs/conventions.md`.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T2, T9, T12 |
| R2 | T2, T3, T4, T12 |
| R3 | T4, T5, T12 |
| R4 | T1, T5, T10, T12 |
| R5 | T5, T8, T12 |
| R6 | T6, T10, T12 |
| R7 | T7, T8, T12 |
| R8 | T1, T7, T10, T12 |
| R9 | T3, T8, T11, T12 |
| R10 | T3, T7, T8, T11, T12 |
| R11 | T2, T4, T9, T12 |
| R12 | T9, T10, T12 |
| R13 | T1–T12 |
