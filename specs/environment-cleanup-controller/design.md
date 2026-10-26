# Controlador durable de limpieza de entornos — Design

## Resumen de la solución

La feature añade `internal/environment/cleanup`, un caso de uso independiente
que consume el lifecycle provider-neutral existente sin convertirse en tool ni
mutar el run. Un ciclo acotado combina cuatro fuentes durables: cleanup intents
del workflow, records de `EnvironmentStore`, capacity receipts e inventarios
explícitos de providers gestionados. Cada item se persiste, reclama mediante
lease/CAS, reconcilia con una identidad estable y solo termina cuando el provider
confirma ausencia y el ledger libera capacidad.

`internal/environment` amplía sus contratos neutrales con metadata temporal,
demanda y un `CapacityAdmission` invocado antes de Acquire. La implementación
local mantiene un índice durable por proyecto y por cleanup identity; un futuro
adapter PostgreSQL podrá implementar los mismos ports, pero esta feature no
añade SQL, colas ni workers. Kubernetes implementa el primer inventario real:
lista de forma acotada solo namespaces del owner configurado y elimina mediante
nombre privado + UID después de revalidar toda la metadata.

```text
workflow cleanup intents ─┐
environment records ──────┼── bounded discovery ── durable cleanup item
capacity receipts ────────┤                              │
managed inventories ──────┘                              ▼
                                                claim lease + CAS
                                                        │
                       ┌────────────────────────────────┴──────────────┐
                       ▼                                               ▼
            known lifecycle Release                         orphan RecoveryCleanup
                       │                                               │
                       └──────────── provider confirms absent ─────────┘
                                                        │
                                                        ▼
                                         cleanup confirmed + quota release
```

## Fuentes de contexto consultadas

| Fuente | Evidencia utilizada |
|---|---|
| `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md` | Feature única, SDD, puerta humana y outputs permitidos. |
| `docs/architecture.md`, `docs/conventions.md`, `docs/verification.md` | Intents/eventos durables, contextos, ownership, interfaces consumidoras, tests/race/coverage. |
| `feature_list.json`, features 34–37 | Cleanup, cuotas y concurrencia actuales; PostgreSQL/workers/config/tenant quedan separados. |
| `specs/environment-provider/*`, `internal/environment/*` | `LifecycleService`, `Record`, release intent/pending/released, provider port, CAS store y events content-free. |
| `specs/kubernetes-environment/*`, `internal/environment/kubernetes/*` | Metadata owner/run/project/expiry, handle name+UID, delete precondition y ausencia de force-finalizer. |
| `specs/kubernetes-validation-tool/*` | La tool es read-only y no debe adquirir autoridad de cleanup ni compartir su RBAC. |
| `internal/validationworkflow/*` | Cleanup intent previo a finalización, desired outcome durable y separación `CleanupPending`/verdict. |
| `progress/history.md` | Invariantes ya aprobadas de recovery, leases, publicación terminal y cleanup Kubernetes. |

No se requiere consultar ni llamar a servicios externos. Los contratos
Kubernetes ya observados en la feature 32 se traducen a modelos propios; no se
añaden DTOs del SDK al controller.

## Reglas arquitectónicas aplicadas

- ARCH-001/002/011: el modelo no conoce ni invoca cleanup; composition registra
  providers, inventarios, policies y owners exactos.
- ARCH-003/004/012: cleanup no crea evidencia funcional, assertions ni verdict,
  y no muta el run directamente.
- ARCH-005/006: ciclo, páginas, items, attempts, waits, provider calls y shutdown
  tienen límites explícitos y propagan contexto.
- ARCH-007: intent, claim, retry, confirmation y capacity receipts son durables
  y auditables antes de depender de logs.
- ARCH-008/009: metadata/provider errors son datos no confiables; solo IDs,
  digests, categorías y counts allowlisted cruzan el adapter.
- ARCH-010: reloj, waiter, providers, inventories y stores son sustituibles y
  herméticos.
- CONV-001..010: contratos pequeños del consumidor, estado con owner explícito,
  errores tipados, ninguna espera/I/O bajo lock y tests sin sleeps.

No se propone ninguna excepción.

## Convención de documentación del código

Toda entidad, constante, variable, función, método y helper de test nuevo o
modificado incluirá un comentario breve que explique su responsabilidad dentro
del sistema, su frontera o el invariante que protege. Incluye símbolos no
exportados, variables de paquete, errores centinela, fakes, factories y helpers
de assertions; el comentario no repetirá meramente el nombre.

## Áreas y paquetes afectados

| Área | Responsabilidad prevista |
|---|---|
| `internal/environment/cleanup` | Modelo del controller, store/ledger local, scanner, leases, reconciler, policies, status y fake contractual. |
| `internal/environment` | Campos temporales/demand seguros, `CapacityAdmission` y hooks exactos reserve/activate/release alrededor del lifecycle. |
| `internal/environment/kubernetes` | Inventario gestionado cerrado y recovery cleanup UID-safe; no cambia provisioning ni tool read-only. |
| `internal/validationworkflow` | Fuente read-only de cleanup intents terminales y proyección operacional sin alterar outcome. |
| `cmd/featureproof` | Composición explícita del controller local, policies, stores, providers, identity, clock y límites. |
| tests | Matriz con clock/waiter/store/provider/inventory falsos, restart/fault/race y verticales workflow/lifecycle. |

No se crea un paquete de PostgreSQL, queue, worker, tenant o project config.

## Contratos y modelos

### `ResourceDemand` y `CapacityPolicy`

El contrato neutral usa un vector fijo, no un mapa libre:

```text
ResourceDemand {
  environments: 1,
  cpu_milli: uint64,
  memory_bytes: uint64,
  object_count: uint64
}

CapacityPolicy {
  project_id,
  max_environments,
  max_cpu_milli,
  max_memory_bytes,
  max_object_count,
  max_ttl,
  digest
}
```

Un `DemandResolver` composition-owned deriva el vector y TTL de la selección
sellada. Para Kubernetes se deriva del template/profile privado ya aprobado;
caller/modelo nunca aportan números. Providers `existing_url` no consumen
capacidad gestionada y no se inventa una demanda.

`CapacityAdmission.Reserve` recibe project, run, acquisition key, selection
digest, demand, TTL y policy digest. La key estable hace redelivery idempotente;
misma key con otro input es conflict. `ConfirmEnvironment` liga environment/UID
al receipt y `Release` solo acepta cleanup confirmation exacta.

### Metadata durable del lifecycle

Los managed `environment.Record` añaden, sin exposición model-facing:

- `CreatedAt`, `ExpiresAt`, `TTL`;
- `ResourceDemand` y capacity receipt digest;
- cleanup intent/retry status cuando sea necesario para indexación;
- relaciones provider/selection/acquisition ya existentes.

Created/expiry se fijan mediante clock de composición antes de provider I/O. El
provider puede devolver una expiración efectiva igual o más restrictiva, nunca
ampliarla. Los loads verifican `created + TTL == expiry` sin overflow y que la
policy vigente para la reserva coincide por digest.

### `CleanupItem`

Envelope privado y canónico:

```text
{
  version, cleanup_id, source,
  project_id, run_id,
  provider_id, provider_version,
  managed_owner_digest,
  selection_digest, acquisition_digest,
  environment_digest, resource_uid_digest,
  created_at, expires_at, ttl,
  demand, capacity_receipt_digest,
  state, attempts, next_attempt_at,
  lease_owner, lease_until, fencing_version,
  last_category, digest
}
```

`source` es `lifecycle|workflow_intent|orphan`. Handles raw, namespace, endpoint,
credential references y provider payload quedan en el store privado del adapter
o lifecycle existente y no aparecen en status/eventos.

### `LifecycleCleanupBinding`

El controller nunca convierte un `RunID` en autoridad. Para un lifecycle conocido
construye y verifica una request consumer-owned sellada:

```text
LifecycleCleanupBinding {
  version, project_id, run_id,
  provider_id, provider_version,
  selection_digest, acquisition_key, acquisition_digest,
  environment_digest, resource_identity_digest,
  lifecycle_digest, cleanup_key, capacity_receipt_digest,
  lifecycle_record_digest, digest
}
```

`resource_identity_digest` liga el UID Kubernetes o fingerprint equivalente sin
exponerlo en status. El `environment` store conserva handle/UID raw privados y,
al recibir la request, carga el record exacto y compara todos los campos antes
de resolver provider o hacer I/O. Mismo `RunID` con project, acquisition,
environment identity, provider o key distintos no es una redelivery.

Un acquire incierto que todavía no conoce environment/UID usa un
`UncertainAcquisitionBinding` separado, ligado a project/run/provider/version,
selection, acquisition key y digest integral del record. Solo habilita
`Reconcile`; no puede llamar `Release`. Reconcile puede devolver un binding
conocido sellado, tras lo cual el controller persiste el nuevo item antes de
cleanup. Un binding parcial no se completa buscando por `RunID`.

Estados cerrados:

```text
discovered → intent_committed → pending → claimed → reconciling
                                   ▲             │
                                   └── retry_wait┘
claimed/reconciling → blocked | quarantine
claimed/reconciling → confirmed → capacity_released
```

`blocked/quarantine` nunca se transforma automáticamente en delete mediante una
policy más permisiva; un requeue autorizado debe conservar auditoría y volver a
validar toda la autoridad.

### Puertos del controller

El paquete consumidor define interfaces pequeñas:

```text
CleanupStore:
  Scan(ctx, cursor, limit) -> page
  Reserve(ctx, candidate) -> item
  Claim(ctx, id, owner, now, ttl) -> item/claimed
  Renew(ctx, item, owner, version, now, ttl) -> item
  SaveOwned(ctx, item, owner, version, event) -> item
  ReleaseOwner(ctx, item, owner, version) -> item
  Events(ctx, id) -> []Event

IntentSource:
  ScanCleanupIntents(ctx, cursor, limit) -> page
  LookupRun(ctx, project, run) -> active|terminal|cancelled|absent

LifecycleCleanup:
  Reconcile(ctx, sealed binding) -> lifecycle status/new sealed binding
  Release(ctx, sealed known binding) -> error
  Status(ctx, sealed binding) -> lifecycle status

ManagedInventory:
  List(ctx, page request) -> candidates
  ReconcileCleanup(ctx, orphan request) -> present|absent
```

No hay `Delete(any)`, GVR, manifest, selector o namespace aportado por caller.
`ManagedInventory` se registra por provider/version y owner scope; un provider
sin inventario solo participa mediante lifecycle records conocidos.

## Componentes y responsabilidades

### `Controller`

`RunOnce(ctx)` procesa como máximo `MaxPages`, `PageSize`, `MaxItems`,
`MaxClaims`, `MaxAttemptsPerItem`, `MaxCycleDuration` y `MaxCumulativeWait`.
Mantiene un cursor durable por fuente solo después de persistir cada discovery;
una página incompleta o token inválido no prueba ausencia.

`Run(ctx)` invoca ciclos y espera `Interval` mediante un `Waiter` inyectado. El
caller posee la goroutine si desea background execution; constructor y `RunOnce`
no lanzan ninguna. Cancelación detiene nuevos claims y llega al scan/wait/I/O.

### `DiscoveryReconciler`

Une sin duplicar:

1. workflow intents aún no reflejados en environment release;
2. lifecycle records no released, release-pending o expirados;
3. capacity receipts sin environment terminal coherente;
4. provider inventory con metadata exacta.

Las identidades se deduplican por cleanup ID estable, no por nombre. Una fuente
contradictoria produce integrity/quarantine. Un store timeout no se interpreta
como NotFound.

### `CleanupReconciler`

Para `lifecycle|workflow_intent`, llama `LifecycleService.Release` con el binding
sellado completo, que conserva el release intent y provider key aprobados. El
service verifica primero project, run, provider/version, selection/acquisition,
environment/UID o fingerprint, lifecycle/cleanup digests, capacity receipt y
record digest contra su store privado. Después consulta status con la misma
autoridad: solo `released`/absence confirmada permite `confirmed`.

Si el lifecycle está en adquisición incierta, el controller solo puede llamar
`Reconcile` con `UncertainAcquisitionBinding`. El binding conocido resultante se
persiste por CAS antes de un Release posterior. `Status`, `Reconcile` y `Release`
rechazan autoridad incompleta o divergente antes de provider I/O.

Para `orphan`, llama `ManagedInventory.ReconcileCleanup` con el receipt privado,
cleanup key y fencing identity. El adapter vuelve a leer owner/UID/metadata antes
de delete y confirma NotFound después de respuestas inciertas. El controller no
interpreta handles ni objetos Kubernetes.

### `CapacityLedger`

Implementa `environment.CapacityAdmission` y un índice local por `ProjectID`.
Cada reserva publica primero un receipt content-addressed y después avanza un
head de aggregate mediante CAS. Suma con overflow checks; ninguna dimensión
puede superar policy. Contenders del mismo proyecto serializan solo el commit
local y nunca sostienen el lock durante provider I/O.

Los receipts cuentan desde antes de Acquire. Cleanup pending, inventory
incompleto y demanda orphan desconocida siguen consumiendo; la última puede
representarse como `unknown=true`, lo que deniega nuevas reservas del proyecto
hasta reconciliar. Liberar usa el receipt y cleanup confirmation digest exactos.

### Store local e índices

El store local conserva receipts/versiones inmutables y heads CAS siguiendo los
patrones existentes de `environment.LocalStore` y `validationworkflow.LocalStore`.
Índices por estado/proyecto son reconstruibles desde receipts y se verifican
contra sus digests antes de usarse. No se borra historial durante esta feature.

El scan es paginado, ordenado por ID canónico y bounded. Locks protegen solo
elección/publicación local; lectura de provider, espera, observer y fsync fuera
de la sección crítica.

### Inventario y cleanup Kubernetes

El adapter reutiliza transporte/configuración Kubernetes, pero usa una identidad
de cleanup distinta y mínima. El puerto concreto solo necesita:

- listar namespaces paginados con labels exactas `managed-by=featureproof` y
  owner configurado;
- obtener un namespace exacto;
- eliminarlo con precondición UID.

La metadata se traduce de inmediato a `InventoryCandidate`. Se validan owner,
run digest, project, acquisition/template/image digests, TTL, expiry, UID y
shape cerrada. Labels/annotations libres no salen. Un namespace `Terminating`
se observa como pending; no se fuerza ni se retira un finalizer. La identidad no
incluye Secrets, workload RBAC ni permisos de la tool feature 33.

### `WorkflowIntentSource`

Un adapter read-only sobre el store de workflow proyecta únicamente handle/run,
project, `CleanupIntentID`, `CleanupPending`, desired outcome digest y terminal/
cancel state. No entrega package/evidence ni permite al controller editar el
record. La creación del intent sigue perteneciendo a `Service.terminal`; el
controller solo garantiza eventual release si el proceso original cae.

### Estado operacional visible

`Status(cleanupID)` devuelve proyección segura: state, project, attempts, next
attempt, category, timestamps y digests. Esta consulta local no resuelve un
`RunID` a lifecycle authority. `validationworkflow.WorkflowView`
mantiene outcome/evidence y solo refleja `CleanupPending`/categoría. Confirmar
cleanup no reabre ni reemite package/report.

## Flujos principales

### Adquisición con cuota

1. Lifecycle resuelve selección y demand/policy confiables.
2. Capacity ledger valida TTL/vector y reserva mediante CAS.
3. Solo tras receipt durable se crea/reserva el environment record.
4. Acquire/Reconcile confirma environment y liga UID/fingerprint al receipt.
5. Si no puede probarse que Acquire no tuvo efecto, receipt sigue consumiendo.
6. Cleanup confirmado libera el receipt una sola vez.

### Terminal o cancelación

1. Workflow persiste desired outcome y cleanup intent.
2. El camino síncrono intenta `Environment.Release` como hasta ahora.
3. Si el proceso cae o release queda pending, controller descubre el intent.
4. Claim/CAS concede ownership; controller verifica el cleanup binding completo
   y lifecycle persiste/recupera release intent sin resolver por `RunID`.
5. Provider confirma ausencia.
6. Controller confirma cleanup y libera quota sin tocar outcome.

### Expiración

1. Scan carga record íntegro y usa clock inyectado.
2. Antes de expiry se omite; en el límite se reserva el cleanup item.
3. Si hay lease lifecycle viva, se conserva pending hasta handoff.
4. Después de claim seguro sigue el mismo flujo de release.

### Orphan

1. Inventory lista una página bajo owner/provider sellados.
2. Controller valida candidate y consulta lifecycle/run/capacity stores.
3. Solo NotFound confirmado + grace cumplido autoriza orphan receipt.
4. Persiste receipt e intent; demanda desconocida bloquea quota del proyecto.
5. Provider reread valida UID/owner antes de delete y confirma NotFound.
6. Evento y capacity release se publican exactamente una vez.

## Flujos de error

1. Config/policy/record inválido falla antes de scan/provider.
2. Store unavailable impide inferir orphan y deja el recurso intacto.
3. Live lease produce conflicto benigno; otro item puede progresar.
4. Transient/rate-limit/timeout persiste retry_wait y next attempt.
5. Cancelled no consume nuevos attempts destructivos; intent permanece.
6. Integrity/unauthorized/conflict permanente produce blocked/quarantine.
7. Response lost llama reconcile con la misma key; no se declara confirmed.
8. Provider confirms absence y CAS final falla: siguiente owner reconcilia y
   converge, sin doble capacity decrement.
9. Observer falla después del state commit: estado autoritativo se conserva y
   el error queda operacional.
10. Overflow/count/page/token inválido falla cerrado y no autoriza cleanup ni
    quota.

## Propiedad del estado y concurrencia

- Lifecycle service posee acquisition/release state; workflow posee outcome;
  controller posee exclusivamente cleanup scheduling/claims y capacity ledger.
- Providers e inventories devuelven observaciones y nunca mutan run/verdict.
- Cada item tiene un owner, lease y fencing version; toda escritura usa CAS.
- `LeaseTTL` debe exceder el máximo indivisible de provider call + store commits
  o la composición se rechaza.
- Se renueva antes de otra frontera; no se renueva durante un I/O bloqueado.
- No hay lock durante red, disco durable, wait, observer o callback.
- Runs/proyectos distintos pueden progresar concurrentemente; aggregate de un
  proyecto se serializa solo al confirmar el CAS.
- Una llamada física repetida tras crash es segura por cleanup key + UID; la
  confirmación lógica y quota release permanecen exactly-once.

## Cancelación, shutdown, retries e idempotencia

El context del caller llega a scan, load, claim, provider, store, observer y
waiter. `Run` comprueba cancelación antes de cada ciclo/claim. No usa
`context.Background`; no inicia cleanup detached después de shutdown.

El intent se confirma antes de provider I/O, por lo que una cancelación durante
la llamada puede dejar expirar la lease sin perder trabajo. Un nuevo owner usa
la misma cleanup key. Errores tipados determinan retry; no se comparan mensajes.
Backoff es exponencial acotado con jitter determinista opcional inyectado,
`MaxBackoff`, `MaxCumulativeWait` y deadline de ciclo.

`RunOnce` no agota para siempre un item: limita attempts del ciclo y persiste
`next_attempt_at`; futuros ciclos pueden continuar mientras la categoría sea
retryable. Attempts usa aritmética con overflow checks. Items permanentes solo
se reanudan mediante requeue autorizado y auditable.

## Observabilidad y datos sensibles

Eventos allowlisted:

```text
cleanup_discovered, cleanup_intent_committed, cleanup_claimed,
cleanup_retry_scheduled, cleanup_blocked, cleanup_quarantined,
cleanup_confirmed, capacity_reserved, capacity_denied, capacity_released
```

Incluyen cleanup/run/project/provider/environment digests, state, attempt,
duration, category y timestamps. Excluyen lifecycle handle, namespace/name/UID
raw, endpoint, selectors, annotations, provider messages, credentials y
payloads. Métricas derivables: pending/blocked count, age, retry category,
cleanup duration y reserved capacity por project. Logs no son estado.

## Estrategia de pruebas

### Modelo, store y ledger

- strict decode, canonicalización, deep-copy, duplicate/unknown fields;
- TTL/expiry/demand/policy relations y overflow de toda suma;
- receipts/heads/event chain, rollback/tamper y reopen;
- quota exact limit, one-over-limit, redelivery, concurrent CAS y unknown
  orphan demand fail-closed.

### Controller y recovery

- active skip; passed/failed/error/cancelled/pending/expired eligibility;
- crash injection antes/después de discovery, intent, claim, provider effect,
  confirmation y capacity release;
- restart con cursor/retry durable y response loss reconcile;
- bounded pages/items/cycle y tokens empty/repeated/changing snapshot.
- lifecycle cleanup binding exacto, redelivery byte-equivalent y rechazo de
  mismo RunID con project/acquisition/UID/fingerprint/key divergentes;
- autoridad incompleta solo puede usar el binding de adquisición incierta y
  jamás alcanza Release hasta que Reconcile produzca identidad conocida.

### Concurrencia y cancelación

- dos controllers, live lease, expired lease, fencing/CAS loss y handoff;
- runs/proyectos independientes y same-project quota contention;
- cancelación antes/durante cada I/O/wait/save;
- `Run` start/cancel/join y fake barriers mediante canales, sin `time.Sleep`;
- normal/repeated/race con cero timer/goroutine leaks.

### Orphans y Kubernetes

- exact owner metadata, missing lifecycle/run y grace boundary;
- store unavailable, metadata missing/tampered, foreign owner, UID replacement,
  namespace Terminating y finalizer ajeno;
- pagination/snapshot drift y permission denied;
- verb audit: solo list/get/delete namespace con UID precondition; ninguna
  operación workload/Secret/tool RBAC ni cluster real.

### Workflow y outcomes

- cada outcome produce intent antes de release;
- crash entre workflow intent y environment intent recupera;
- cleanup pending/blocked no cambia passed/failed/evidence/package;
- cleanup success tardío solo cambia status/capacity;
- cuatro superficies conservan el único lifecycle sin exponer cleanup inputs.

### Gates

- `gofmt -l`, auditoría de comentarios, `git diff --check`;
- `go vet ./...`, compile-only, focales repetidos y global normal;
- global/focal `-race` y matrices críticas ×20;
- coverage exacta del diff productivo ≥85% sin redondear;
- runtime Linux y cross-builds aplicables;
- auditoría de red, temporales, secretos y scope.

## Alternativas y decisiones descartadas

- **Usar un `defer` o goroutine del run:** se pierde tras crash y no reconcilia
  huérfanos. El intent y controller son durables e independientes.
- **Borrar desde la tool Kubernetes:** mezcla observación funcional read-only
  con efectos destructivos y su RBAC. Se usa identidad/adapter de cleanup.
- **List/delete genérico del provider:** amplía autoridad y permite nombres o
  selectors arbitrarios. Inventarios están registrados y cerrados.
- **Liberar quota al terminalizar:** puede admitir capacidad mientras el recurso
  sigue presente. Solo absence confirmada libera.
- **Confiar en el nombre del namespace:** una recreación homónima podría borrarse.
  Se exige UID, owner y metadata completa.
- **Resolver release/status únicamente por `RunID`:** un ID válido no demuestra
  project, provider, acquisition ni resource identity y permitiría cleanup
  cross-project. Cada llamada usa un binding integral sellado contra el record.
- **Forzar finalizers o parchear ownership:** podría destruir recursos ajenos;
  queda blocked/pending visible.
- **Mantener retries/timers solo en memoria:** restart perdería schedule y
  attempts. Se persisten category/next attempt.
- **Interpretar error de store como ausencia:** convertiría indisponibilidad en
  permiso destructivo. Solo NotFound confirmado cuenta.
- **Introducir PostgreSQL/queue/worker ahora:** adelanta feature 35. Los ports
  permiten el adapter futuro sobre la implementación local actual.
- **Introducir tenant/team model:** adelanta feature 37. `ProjectID` es la clave
  de policy disponible en esta entrega.
- **Cargar YAML de cuotas por repositorio:** adelanta feature 36 y convierte
  contenido externo en autoridad. Policies llegan selladas desde composición.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | durable model, lifecycle record | `TestManagedEnvironmentIdentityIsDurableCanonicalAndRecoverable`; `TestLifecycleCleanupBindingRejectsCrossAuthorityForSameRun` |
| R2 | workflow intent source, lifecycle release | `TestEveryTerminalOutcomePersistsCleanupIntentBeforeProviderIO` |
| R3 | controller/scanners/cursors | `TestControllerRunOnceRecoversAndBoundsEveryDiscoverySource` |
| R4 | expiry evaluator, clock | `TestCleanupEligibilityUsesExactTTLBoundaryWithoutStealingLiveLease` |
| R5 | orphan discovery/inventory | `TestOrphanCleanupRequiresCompleteOwnershipAndAuthoritativeAbsence` |
| R6 | cleanup ports/reconciler | `TestCleanupUsesStableIdentityAndConfirmsAbsenceIdempotently`; `TestLifecycleCleanupRequiresCompleteSealedBinding` |
| R7 | retry scheduler/error mapper | `TestCleanupRetriesAreTypedBoundedAndDurableAcrossRestart` |
| R8 | store leases/CAS/fencing | `TestConcurrentControllersHandoffWithoutDuplicateLogicalCleanup` |
| R9 | capacity admission/ledger | `TestProjectCapacityIsReservedAtomicallyBeforeAcquire` |
| R10 | capacity release/recovery | `TestCapacityRemainsReservedUntilCleanupConfirmation` |
| R11 | workflow projection | `TestCleanupFailureNeverChangesFunctionalVerdictOrEvidence` |
| R12 | run loop/context | `TestControllerCancellationStopsNewWorkAndLeavesRecoverableIntent` |
| R13 | event store/status/redaction | `TestCleanupAuditIsExactlyOnceContentFreeAndReopenable` |
| R14 | composition/ports | `TestLocalCleanupCompositionHasNoDatabaseQueueOrHiddenDefaults` |
| R15 | fake matrix/gates | `TestCleanupControllerHermeticContractMatrix` + global/race/coverage gates |

## Delimitación con la feature 35

La durabilidad de esta entrega significa receipts e índices locales verificables,
reopen, CAS y leases multiproceso sobre el backend local. No promete scheduling
distribuido, disponibilidad de PostgreSQL, entrega de jobs, heartbeats de worker
ni migraciones. Feature 35 podrá implementar `CleanupStore`, `CapacityAdmission`
e `IntentSource` con su backend y ejecutar el mismo controller, sin cambiar el
contrato observable aprobado aquí y sin dual-write dentro de un run.
