# Abstracción de obtención de entornos — Design

## Resumen de la solución

La feature introduce un paquete cohesionado `environment` que posee el modelo
provider-neutral, el catálogo sellado, la máquina durable de adquisición y el
contrato consumido por `validationworkflow`. El workflow deja de distinguir
targets directos y previews: recibe un `ValidationTarget` canónico desde un único
`EnvironmentPort`, lo revalida en los checkpoints existentes y solicita release
mediante el mismo handle lógico.

`ExistingURLProvider` es un adapter del lifecycle existente. No copia el
coordinador `previewenv`: resuelve una selección aprobada, delega acquire/restore/
release en el port actual y traduce su `PreviewBinding` a la observación neutral.
Un `ContractManagedProvider` fake vive solo en tests y demuestra que un provider
capaz de adquirir/liberar infraestructura puede satisfacer el mismo contrato sin
exponer detalles al harness. Feature 32 implementará el primer managed provider
real.

```text
trusted environment catalog + run/revision
                    │
                    ▼
          Environment lifecycle service
           │ selection/CAS/recovery │
           ▼                        ▼
 ExistingURLProvider          managed fake (tests)
           │
     previewenv coordinator
           │
           ▼
       provider observation
                    │
                    ▼
 ValidationTarget seal → attestation/network binding → validationworkflow
                    │
                    ▼
 deterministic execution → durable release intent → provider release
```

No se añade una nueva frontera de red productiva. Las credenciales y APIs de un
provider futuro quedan por debajo de `EnvironmentProvider`; el dominio solo ve
tipos internos.

## Fuentes de contexto consultadas

- `AGENTS.md`, `docs/specs.md`, `docs/architecture.md`,
  `docs/conventions.md` y `docs/verification.md`.
- Feature 31 de `feature_list.json` y límites explícitos de feature 32
  `kubernetes-environment`.
- `specs/preview-environment-adapter/`: contrato no destructivo de selección,
  reserve/reconcile/readiness/release, atestación, policy de red, recovery y
  secretos.
- `internal/previewenv`: `Provider`, `Coordinator`, `AuthorizedExecution`,
  `Snapshot`, `Store`, `NetworkPolicy`, `SafeProjector` y adapters productivos.
- `internal/validationworkflow`: `PreviewPort`, `PreviewBinding`,
  `TargetContext`, `BindingContext`, checkpoints de restore/approval/execution,
  cleanup intent y `Finalizer`.
- `cmd/featureproof`: composición de direct/preview, `workflowPreviewPort`,
  credenciales late-bound, attestation y network context.
- `internal/revisionbinding` y `internal/tools/http`: identidades exactas,
  atestación y controles de red ya aprobados.
- Features 29–30: autoridad composition-owned, producers actuales tras restart,
  content-free journals y separación irreversible de trust.

## Contratos externos observados

Esta feature no elige ni llama a un provider externo real. El contrato externo
observable es el ya interno y aprobado de feature 19:

- `previewenv.Provider.Reserve/Reconcile/Readiness/Release` opera sobre una
  reserva lógica existente y no provisiona infraestructura.
- `previewenv.Coordinator` posee retries, readiness, leases, recovery y release.
- `validationworkflow.PreviewPort` proyecta `Acquire/Restore/Release` mediante
  un `PreviewBinding` ya seguro.

`ExistingURLProvider` depende de un port consumidor pequeño que representa esas
tres operaciones; no importa DTOs internos innecesarios ni accede a stores del
coordinator. Para managed providers futuros se fija solo un protocolo propio:
acquire/reconcile, observe y release. Ninguna semántica de Kubernetes, cloud o
Terraform se anticipa.

## Paquetes y áreas afectados

| Área | Responsabilidad prevista |
|---|---|
| `internal/environment` | Modelo canónico, catálogo, provider port, lifecycle service, store, observer y suite contractual. |
| `internal/previewenv` | Mantiene comportamiento existente; solo expone el bridge mínimo si falta una frontera estable. |
| `internal/validationworkflow` | Sustituye `PreviewPort` por `EnvironmentPort`, persiste target/lifecycle digests y revalida/restaura/libera. |
| `cmd/featureproof` | Compone catálogo, ExistingURLProvider, stores, clocks, policies y transportes/credenciales. |
| tests | Managed fake, contract suite común, verticales de cuatro superficies y recovery/race. |

No se crea paquete `common`, `provider` genérico ni un SDK público.

## Componentes y responsabilidades

### `SelectionCatalog`

Recibe configuraciones confiables ya cargadas y las sella. Cada entrada contiene:

- alias y versión;
- provider ID y kind cerrado `existing_url|managed`;
- template/reference opaca y versión;
- runtime/profile y network policy ID;
- credential reference opaca o `secretless`;
- capabilities allowlisted;
- policy/catalog digest.

El catálogo resuelve copias inmutables. No recibe el prompt, diff, PR body ni
target URL. No existe entrada default.

### `EnvironmentProvider`

Interfaz consumer-owned mínima:

```text
Acquire(ctx, request)   -> Observation
Reconcile(ctx, request) -> Observation
Observe(ctx, handle)    -> Observation
Release(ctx, request)   -> ReleaseResult
```

`AcquireRequest` incluye selección sellada, run, repository/commit esperados,
acquisition key y budgets ya validados. `Observation` contiene solo identidades
opacas, estado cerrado, target candidate, attestation projection, capabilities y
handle sellable. `ReleaseRequest` usa el handle confirmado y release key. El
provider no muta el run ni publica verdicts.

### `ValidationTarget/v1`

Entidad canónica con:

- version, target ID y digest;
- selection/provider/template digests;
- project/repository/commit/run;
- environment y lifecycle digest;
- endpoint seguro normalizado;
- attestation reference, valid-until y verified;
- network policy digest y proyección efectiva;
- capabilities cerradas y ordenadas;
- observed version/fingerprint/completeness.

El target no guarda credential reference, token, provider payload, URL de API ni
handle raw. Un binding separado, no model-facing, liga target digest y lifecycle
digest al record del run.

### `LifecycleService`

Orquesta catálogo, provider, store, attestation/network authorization, reloj y
observer. Posee la mutación de un lifecycle y ejecuta transiciones mediante CAS.
No lanza goroutines ocultas. Devuelve `ValidationTarget` o un error operacional
tipado.

### `ExistingURLProvider`

Adapter que recibe un `ExistingPreviewLifecycle` consumer-owned:

```text
Acquire(ctx, run, selection) -> ExistingBinding
Restore(ctx, run, selection) -> ExistingBinding
Release(ctx, run)            -> error
```

La implementación productiva delega al `workflowPreviewPort`/coordinator de
feature 19. Traduce `TargetContext` y sus digests; no vuelve a interpretar URL,
attestation o network rules como fuente de autoridad. La validación neutral
puede comprobar consistencia, pero no crea una segunda reserva ni un segundo
cleanup.

### Managed fake contractual

El fake programable implementa el port real, soporta pending/ready/drift,
response loss y release, registra llamadas y canarios, bloquea por context y usa
barreras/canales. No realiza red, filesystem de infraestructura, shell ni sleeps.
Nunca se compone en producto.

### `EnvironmentStore`

Persiste records content-free direccionados por identidad de run/selección:

- selección y target digests;
- provider/environment/lifecycle IDs opacos;
- phase, owner/lease, version CAS;
- acquisition/release key digests;
- target/binding canónicos seguros;
- desired outcome y cleanup intent;
- attempts/durations/categories.

Las escrituras usan staging, sync y publicación atómica/hard-link según
plataforma. Los loads verifican schema, relaciones y digests. No se borra un
objeto content-addressed durante contención; una GC futura partirá de receipts
autoritativos.

### `EnvironmentPort` de validationworkflow

El consumidor ve:

```text
Acquire(ctx, handle, command) -> EnvironmentBinding
Restore(ctx, handle, command) -> EnvironmentBinding
Revalidate(ctx, binding)      -> ValidationTarget
Release(ctx, handle)          -> error
```

`EnvironmentBinding` contiene el target seguro y digests de lifecycle/selection.
No expone el provider. Este port reemplaza el camino `PreviewPort`; la selección
directa o preview se traduce en composición a una selección de entorno sellada.

## Flujo principal

1. La superficie autoriza el run y referencia una selección sellada.
2. Composición resuelve catálogo, provider registrado, policies y transporte.
3. `LifecycleService` reserva durablemente acquisition intent/key.
4. Llama `Acquire`; ante respuesta incierta usa `Reconcile` con la misma key.
5. Persiste el handle seguro y observa readiness dentro de budgets.
6. Cada observación se valida contra selección, repository/commit, lifecycle,
   atestación vigente, capabilities y network policy.
7. Cuando queda ready, sella `ValidationTarget` y `EnvironmentBinding`.
8. `validationworkflow` persiste el digest antes de plan/approval/execution.
9. Antes de efectos/recovery/publicación llama `Revalidate` contra autoridad
   actual.
10. Al terminar o cancelar, el workflow confirma release intent y solicita
    `Release`; solo después confirma cleanup/released.

## ExistingURLProvider

```text
Environment selection(existing_url)
        │
        ▼
ExistingURLProvider.Acquire
        │ delegates once
        ▼
existing PreviewPort.Acquire → previewenv Coordinator
        │
        ▼
PreviewBinding/TargetContext
        │ translate + seal
        ▼
ValidationTarget
```

`Observe/Reconcile` usa restore/attest de la autoridad existente. `Release`
delega una vez al port existente. Durante migración no se mantienen
`deps.Preview` y `deps.Environment` activos simultáneamente para un mismo run.
Tests con spies exigen una sola adquisición y liberación.

## Provider gestionado futuro

El managed provider real recibirá una selección ya autorizada y resolverá
credenciales en su adapter. Podrá crear/reservar un recurso y devolver un handle.
El core no conoce el mecanismo. Feature 32 deberá implementar este contrato,
además de sus reglas específicas, sin ampliar la interfaz con manifests o
clientes Kubernetes.

## Estados y transiciones

```text
selected → acquiring → acquired → waiting → ready → bound → in_use
    │           │           │         │        │         │
    └───────────┴───────────┴─────────┴────────┴─────────┤
                                                        ▼
                                                release_intent
                                                        │
                                          releasing ↔ release_pending
                                                        │
                                                     released
```

- `selected`: catálogo confirmado, sin provider I/O.
- `acquiring`: intent/key persistidos.
- `acquired`: handle/identity confirmados, no necesariamente ready.
- `waiting`: readiness bounded.
- `ready`: target observado y validado.
- `bound`: target digest ligado al workflow.
- `in_use`: puede comenzar trabajo determinista.
- `release_intent`: obligación durable previa a cleanup.
- `release_pending`: cleanup incierto o agotado, recuperable.
- `released`: confirmación terminal idempotente.

No se salta de selected a ready ni de in_use a released sin intent. Un error
después de posible acquire converge hacia release.

## Propiedad del estado y concurrencia

Un owner/lease posee las transiciones de un record. Store aplica CAS; providers
devuelven observaciones y no mutan records. No se mantienen locks durante
provider, disk, network authorization, clock, observer o callback. Runs
independientes progresan concurrentemente. Redeliveries de un mismo run esperan,
adoptan tras lease o leen el target confirmado.

No hay goroutine necesaria en el flujo nominal. Cualquier worker futuro declara
owner, cancel, join y shutdown. Los tests coordinan interleavings con canales,
nunca `time.Sleep`.

## Cancelación, timeouts y retries

Cada frontera recibe el context del caller y un child deadline igual al menor
entre budget y deadline padre. Categorías distinguen `cancelled` y `timeout` y
conservan `context.Cause` mediante `Unwrap` sin filtrar texto.

Solo errores explícitamente transitorios/rate-limited se reintentan. Acquire
incierto llama Reconcile antes de repetir. Readiness es lectura. Release reutiliza
key y reconcilia `already_released`. Clock/waiter y jitter son inyectados; una
espera debe caber completa.

Tras cancelación, release usa `context.WithoutCancel` únicamente en el owner de
cleanup y aplica inmediatamente un timeout finito. El intento durable se escribe
antes. Si persistencia del intent es incierta, recovery conserva suficiente
identidad para reconciliar sin asumir liberación.

## Idempotencia y recovery

- Acquisition key: hash de run + selection digest + repository + commit.
- Release key: hash de acquisition identity + lifecycle digest.
- Misma key con input distinto: conflict.
- Response loss: Reconcile con la misma key.
- Target confirmado: redelivery carga binding/receipt sin nuevo acquire.
- Restart: nueva composición resuelve catálogo/provider/policies actuales.
- Release confirmado con CAS perdido: recovery reconcilia y publica released.
- Release no confirmado: permanece `release_pending`.

La idempotencia no descansa en logs ni en comparar mensajes de error.

## Binding, atestación y policy de red

El target se autoriza en dos pasos:

1. provider observation → identidad/revisión/attestation exactas;
2. endpoint → `NetworkPolicy` ya aprobada y controles existentes de HTTP.

`ValidationTarget` conserva la proyección de policy necesaria para ejecución,
pero cada operación mantiene DNS, IP pinning, redirect y authority checks
existentes. Un target no amplía paths, hosts, ports o schemes. Revalidation
compara digests y solicita observación actual; no confía solo en que la URL siga
igual.

## Credenciales y secretos

Catálogo contiene una referencia opaca, nunca material. `cmd/featureproof`
resuelve la referencia solo al construir el transporte concreto y no entrega un
resolver al core. `secretless` prohíbe resolución. Closures/transports se quedan
en composición.

Canarios se buscan en selección segura, records, target, binding, plan, model
request, events, evidence, package y report. Errores remotos se mapean a
categorías cerradas sin body. Un detector fallido/cancelado falla cerrado.

## Errores y resultados operativos

Taxonomía: `invalid`, `unauthorized`, `conflict`, `integrity`, `unavailable`,
`rate_limited`, `transient`, `cancelled`, `timeout`, `persistence`,
`cleanup_pending`, `already_released`, `internal`.

Antes de admisión, invalid/config/internal pueden producir `error`; falta de
entorno o budget puede ser `inconclusive`; cancelación es `cancelled`. Después de
un veredicto funcional, cleanup pending no lo sobrescribe. `failed` se reserva a
assertions deterministas sobre una aplicación ejecutada.

## Observabilidad

Records allowlisted: `environment_selected`, `acquisition_started`,
`environment_acquired`, `readiness_observed`, `target_bound`, `drift_detected`,
`release_intent`, `release_pending`, `environment_released`.

Incluyen run, selection/provider/environment/lifecycle digests, phase, attempts,
durations y category. Excluyen endpoint completo, handle raw, attestation raw,
credentials, headers, payloads y mensajes remotos. La publicación terminal es
exactly-once; observer failure no cambia target/binding ni estado autoritativo.

## Integración con cuatro superficies

CLI/API/MCP/GitHub llegan al mismo `validationworkflow.Service`. Los adapters de
superficie solo aportan una referencia sellada producida por configuración y
autoridad; no construyen targets. El service llama al único `EnvironmentPort` y
produce el mismo digest. Status/result proyectan estado operacional seguro, no
handles.

La migración elimina el branch de adquisición específico de previews una vez
conectado el adapter. El contrato CLI histórico de validación local puede
traducirse a un alias composition-owned de ExistingURLProvider; no conserva un
atajo target libre en el workflow compartido.

## Estrategia de pruebas

### Modelo y catálogo

- strict decode, canonicalización, deep-copy, enums/IDs/OIDs/capabilities;
- alias/provider/template/policy únicos, no default, authority widening;
- target/binding/lifecycle tamper y cross-run/provider/revision;
- JSON duplicado, relations self-signed y digests recalculados.

### Contract suite común

Tabla/factory ejecutada contra ExistingURLProvider y managed fake:

- selección y acquire nominal;
- pending→ready y unavailable/unhealthy/drift;
- idempotency conflict, response loss y reconcile;
- context cancel/deadline en cada método;
- target/attestation/network binding;
- release success/already/uncertain/recovery;
- concurrent same/different acquisition bajo race.

### ExistingURLProvider

- spies demuestran una delegación exacta a Acquire/Restore/Release;
- conserva todos los digests/campos del preview;
- no llama provision/deploy/update/restart/scale/delete/destroy;
- no duplica release con el camino legacy;
- rechecks usan autoridad actual tras restart.

### Lifecycle durable

- crash/fault injection antes/después de acquire, readiness, target publish,
  release intent, provider release y released CAS;
- leases vivas/expiradas, redelivery y owner contention;
- target nunca addressable antes de confirmación;
- no I/O bajo locks, progress de runs independientes y `-race`.

### Seguridad e integración

- canarios en credential, provider payload, error, endpoint metadata y handle;
- network denial antes de DNS/dial;
- verticales CLI/API/MCP/GitHub y recovery;
- failures operational vs functional assertion failure;
- ninguna red/provider/Kubernetes/modelo real en `go test`.

### Gates

- `gofmt -l`, comentarios sistémicos, `git diff --check`;
- `go vet ./...`, compile-only, focales repetidos y suite global;
- focal/global `-race` y matrices críticas altas;
- runtime Linux y cross-builds Windows/Linux/Darwin aplicables;
- coverage exacta del diff >=85% incluyendo build tags;
- auditoría de temporales, secretos y scope.

## Trazabilidad inicial

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | catálogo, composition, admission | `TestEnvironmentSelectionComesOnlyFromTrustedCatalog` |
| R2 | provider port, DTOs, contract harness | `TestEnvironmentProviderContractRunsAgainstExistingAndManagedFake` |
| R3 | target/binding model | `TestValidationTargetIsCanonicalDetachedAndStrict` |
| R4 | lifecycle service, authorizers | `TestEnvironmentBecomesVisibleOnlyAfterReadyAttestedAndAuthorized` |
| R5 | ExistingURLProvider/preview bridge | `TestExistingURLProviderDelegatesWithoutProvisioningOrDuplicateCleanup` |
| R6 | managed fake, safe projections | `TestManagedLifecycleRemainsOpaqueOutsideProvider` |
| R7 | revalidator/workflow checkpoints | `TestEnvironmentDriftStopsExecutionRecoveryAndPublication` |
| R8 | store/release/recovery | `TestDurableReleaseConvergesAcrossEveryCrashBoundary` |
| R9 | contexts, waiter, concurrency | `TestEnvironmentLifecycleCancellationRetriesAndConcurrencyAreBounded` |
| R10 | composition, secret transport | `TestProviderSecretsNeverCrossAdapterBoundary` |
| R11 | error mapper/results | `TestOperationalEnvironmentFailuresNeverBecomeFunctionalFailures` |
| R12 | journal/observer | `TestEnvironmentRecordsAreExactlyOnceContentFreeAndRecoverable` |
| R13 | workflow/surfaces | `TestFourSurfacesShareOneEnvironmentLifecycleAndDigest` |
| R14 | reusable contract suite | `TestEnvironmentProviderContractMatrix` |
| R15 | verification | global/race/Linux/cross/coverage/comment/scope gates |

## Decisiones y alternativas descartadas

- **Extender directamente `PreviewPort` con Kubernetes:** mezcla un adapter
  existente no destructivo con infraestructura gestionada y expone detalles al
  workflow.
- **Reutilizar `previewenv.Provider` como contrato universal:** su semántica está
  deliberadamente limitada a reservas existentes. Se adapta sin cambiarla.
- **Mantener caminos direct/preview/managed en service:** crea checkpoints y
  cleanup divergentes. Un único port expresa el lifecycle.
- **Entregar el handle al harness:** amplía autoridad y filtra detalles. Solo se
  conserva un digest en proyecciones seguras.
- **Provider por defecto o elegido por modelo:** viola autoridad explícita.
- **Liberar solo en memoria/defer:** pierde cleanup tras crash. El intent es
  durable antes de I/O.
- **Borrar/destruir desde ExistingURLProvider:** feature 19 solo libera reserva
  lógica; no cambia ese contrato.
- **Implementar Kubernetes aquí:** anticiparía manifests, seguridad y TTL de la
  feature 32 sin spec aprobada.
- **Guardar credenciales cifradas en el store:** el core no las necesita; la
  composición recrea el transporte.

## Conformidad arquitectónica

- `ARCH-001/002`: modelo no accede a providers; selección y port son
  composition-owned y tipados.
- `ARCH-003/004`: target/readiness no son evidencia funcional ni verdict.
- `ARCH-005`: attempts, waits, acquisition, readiness y release son finitos.
- `ARCH-006`: toda frontera propaga context/cause.
- `ARCH-007`: intents/transiciones/terminal records son durables append-only/CAS.
- `ARCH-008/009`: provider payload no se convierte en instrucciones y secretos
  quedan en transporte.
- `ARCH-010`: contract suite hermética sin servicios reales.
- `ARCH-011/012`: modelo no amplía selección y provider no muta el run.
- `CONV-001..010`: interfaces del consumidor, errores tipados, clocks
  inyectados, ownership explícito, sin locks durante I/O, tests deterministas,
  trazabilidad y una sola feature.

No se solicita excepción a arquitectura o convenciones.
