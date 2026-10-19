# Adapter de entornos preview existentes — Design

## Resumen de la solución

La solución añade un paquete `internal/previewenv` que consume un contrato
neutral de provider y posee el ciclo de vida de una reserva. Una selección
confiable fija provider, referencia de entorno, revisión esperada, política de
target y perfiles opacos. El coordinador reserva el preview existente,
comprueba attestation y target, espera readiness, entrega un contexto inmutable
al caso de uso de validación y libera la reserva en todos los caminos.

```text
alias autorizado
      │
      ▼
catálogo ──> selección sellada ──> provider.Reserve ──> snapshot
                                                    │
                       revisionbinding <── attestation/repository/commit
                                                    │
                         target policy <── scheme/host/port/base path
                                                    │
                 provider.Readiness ──> ready + misma identidad
                                                    │
                      validación existente ──> package/veredicto
                                                    │
                                     provider.Release (idempotente)
```

La reserva es una operación lógica sobre un entorno ya existente. El contrato
no permite crear, desplegar, modificar ni destruir infraestructura.

## Fuentes de contexto consultadas

- `feature_list.json`, feature 19: interfaz propia, snapshot seguro,
  atestación, policy de red, secretos en composición, timeout/cancelación,
  clasificación operacional, no aprovisionamiento y fake provider.
- `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md`: SDD, EARS,
  trazabilidad y aprobación humana previa a código.
- `docs/architecture.md`: dominio independiente de adapters, políticas antes
  de efectos, cancelación, evidencia verificable, secretos fuera de artefactos
  y modelo sin acceso directo a sistemas externos.
- `docs/conventions.md` y `docs/verification.md`: interfaces en consumidor,
  dependencias explícitas, contextos, concurrencia, errores tipados, tests
  deterministas, race, coverage y trazabilidad.
- `specs/revision-bound-validation/`: `EnvironmentAttestor`, binding inmutable,
  rechecks antes de batches/publicación y prohibición de atribuir una revisión
  no demostrada.
- `specs/github-pr-integration/`: selección confiable, `ValidationGateway`,
  separación de GitHub/harness y gate productivo `FailClosedValidation` hasta
  que exista un preview confiable.
- Fronteras actuales: `internal/revisionbinding`, `internal/localvalidation`,
  `internal/tools/http`, `internal/externalagent`, `internal/githubpr` y
  `cmd/featureproof`. El runner hoy recibe target y attestor separados; la
  composición de agente externo y GitHub permanece cerrada por falta de esta
  feature.

No se observa un contrato externo de provider porque la feature no elige uno.
Por ello no se incorpora SDK, DTO ni protocolo ajeno y no se necesita una
fuente web externa. Una implementación futura documentará su API oficial en su
propia spec o adapter sin alterar estos modelos.

## Componentes y responsabilidades

| Componente propuesto | Responsabilidad sistémica |
|---|---|
| `internal/previewenv` modelo | Define selection, expected revision, reservation, snapshot, target candidate, availability y errores cerrados independientes del provider. |
| `Provider` | Interfaz mínima del consumidor para reservar/reconciliar un entorno existente, observar readiness y liberar la reserva lógica. |
| `Catalog` | Resuelve un alias ya autorizado a una selección sellada; copia defensivamente la configuración y nunca acepta target/secret del request. |
| `Coordinator` | Posee la máquina de estados, deadlines, retries, rechecks y release; no mantiene locks durante fronteras externas. |
| `TargetAuthorizer` | Normaliza y comprueba scheme, hostname, puerto y base path contra configuración confiable antes de crear capacidades. |
| `AttestorBridge` | Proyecta el snapshot vigente a `revisionbinding.Attestation` y rechaza identidad, revisión o vigencia cambiadas. |
| `ValidationExecutor` | Frontera mínima que recibe un `AuthorizedExecution` inmutable y ejecuta el caso de uso existente sin poder cambiar target o selección. |
| `SecretTransportFactory` | Vive solo en composición; resuelve referencias de credencial y construye transportes ya autenticados sin devolver bytes al core. |
| Bridges `externalagent`/`githubpr` | Traducen sus selecciones confiables al mismo `previewenv.Selection` y proyectan estado seguro a sus contratos actuales. |
| Composición `cmd/featureproof` | Valida catálogo, providers, perfiles, políticas y timeouts; sustituye gates fail-closed solo cuando todas las dependencias confiables están presentes. |

Cada entidad, constante, variable de paquete, interfaz, función, método,
constructor y helper de test nuevo o modificado debe llevar un comentario que
describa su función dentro del sistema. Las tasks incluyen una comprobación AST
que cubre símbolos exportados e internos, incluidos `var` y `const` de paquete.

## Paquetes afectados

- Nuevo `internal/previewenv`: modelos, catálogo, provider contract,
  coordinator, target authorization, retries y bridges de attestation.
- `internal/externalagent`: sustituye `FailClosedExecutor` por un executor que
  delega al coordinator; conserva DTOs, scopes, registry y supervisor.
- `internal/githubpr`: sustituye `FailClosedValidation` mediante un bridge al
  mismo caso de uso; la selección GitHub continúa procediendo de su authorizer.
- `internal/localvalidation`: refactor mínimo para ejecutar desde inputs ya
  cargados/autorizados o mediante una factory inyectable; target y attestor
  proceden del snapshot y no de argumentos libres.
- `internal/tools/http`: se reutiliza su target scope y validación por petición;
  solo se extrae una frontera de autorización reutilizable si hace falta para
  evitar reglas divergentes.
- `cmd/featureproof`: composición de catálogo, providers y transportes secretos
  con validación fail-fast. Si falta cualquier dependencia, mantiene el fallo
  cerrado actual.

No se modifican paquetes de modelo, assertions o veredicto para que el adapter
no participe en conclusiones funcionales.

## Contratos propios

### Selección

`Selection/v1` contiene `alias`, `provider_id`, `environment_ref`,
`repository_id`, `commit_oid`, `target_policy_id`, `execution_profile_id` y
`credential_profile_refs` opacas. Se crea desde configuración confiable y se
sella con un digest canónico no secreto. El request exterior contiene solo el
alias ya autorizado; GitHub añade su head exacto como expected revision y la
composición local fija repository/ref mediante su catálogo.

No se establece proveedor por defecto. Provider ID y environment ref son
obligatorios y la combinación debe ser única en el catálogo efectivo.

### Provider

La interfaz del consumidor tiene tres operaciones:

- `Reserve(ctx, ReserveRequest) (ReservationSnapshot, error)`: descubre el
  entorno existente y crea o reconcilia la reserva por una clave estable.
- `Readiness(ctx, ReservationIdentity) (ReservationSnapshot, error)`: observa
  estado, attestation y target actuales de la misma reserva.
- `Release(ctx, ReleaseRequest) error`: libera idempotentemente la reserva
  lógica; `already released` equivale a éxito.

`ReserveRequest` incluye selección sellada, run ID/key y expected repository +
commit. No incluye secretos. `ReleaseRequest` incluye provider ID,
reservation ID y release key derivada. El contrato no contiene operaciones de
create/update/delete/restart/deploy.

`ReservationSnapshot/v1` contiene:

- provider ID, environment ID estable y reservation ID opaco;
- estado cerrado `pending|ready|unavailable|unhealthy`;
- target candidato estructurado o URL canónica limitada;
- repository ID y commit OID completo;
- attestation reference, `observed_at`, `valid_until` y version/fingerprint.

El core valida tamaños, identificadores, timestamps, OID y fingerprint. Nunca
persiste DTOs externos ni payloads de atestación. Los providers traducen sus
respuestas a este contrato dentro de su adapter.

### Target autorizado

`AuthorizedTarget/v1` conserva scheme, hostname ASCII normalizado, puerto
efectivo y base path canónico, además del policy ID. No admite userinfo, query o
fragment. Su representación canónica participa en el contract/configuration
digest del binding, de modo que un cambio invalida autorización y aprobación.

`TargetAuthorizer` aplica allowlists exactas por selección. Después se construye
la capacidad HTTP existente con esa base URL. `httptool` vuelve a comprobar
paths, DNS, conexión y cada redirección; el adapter no relaja sus controles.

### Ejecución autorizada

`AuthorizedExecution` agrupa selection digest, reservation identity,
`AuthorizedTarget`, attestation segura y referencias opacas a perfiles. Es un
valor defensivamente copiado. El modelo recibe solo el target contextual que ya
usa planning; no recibe provider/environment refs libres ni secretos, y nunca
invoca el provider.

## Máquina de estados y propiedad

```text
selected
   └─ reserve ─> reserved ─> waiting_ready ─> ready ─> executing ─> releasing ─> released
                      │              │            │          │
                      └──────────────┴────────────┴──────────┘
                                   error/cancel ─> releasing
```

El coordinator posee el lifecycle de una ejecución. El provider posee su
estado externo; la registry existente posee el lifecycle público del run. Una
reserva confirmada implica que todo camino debe pasar por `releasing`. El
estado `release_pending` permite recuperación cuando se agotó el presupuesto,
sin afirmar que la reserva fue liberada.

El snapshot inicial se copia, sella y compara con cada observación. Environment
ID, reservation ID, repository, commit, attestation reference y target
autorizado no pueden cambiar. `observed_at`, `valid_until` y estado pueden
avanzar de forma coherente. Un cambio estructural es `integrity_failure` o
`revision_mismatch`, no una actualización in-place.

## Flujo principal

1. Reautorizar principal/PR y resolver el alias en el catálogo confiable.
2. Sellar `Selection` con repository + commit esperados. Validar configuración,
   policies y perfiles sin resolver aún secretos innecesarios.
3. Llamar `Reserve` con timeout y clave idempotente. Ante resultado incierto,
   repetir la misma operación para reconciliar, nunca con una clave nueva.
4. Validar/sellar snapshot; comparar repository/commit y construir la
   attestation de `revisionbinding`.
5. Autorizar el target localmente. Un rechazo salta a release sin DNS/socket.
6. Consultar `Readiness` con reloj/waiter inyectados. Cada snapshot debe
   conservar identidad, revision, attestation y target. Solo `ready` continúa.
7. Crear el runner/caso de uso con el target autorizado y el attestor bridge.
   `revisionbinding.Admit` fija el binding antes del primer trabajo funcional.
8. Antes de cada batch y publicación, el bridge refresca readiness/attestation
   y el runner mantiene sus rechecks actuales. Deriva detiene trabajo nuevo.
9. Ejecutar y publicar mediante reglas existentes. El adapter no calcula
   assertions ni veredicto.
10. En un `defer`/finalizer poseído, liberar la reserva con contexto acotado.
    El consumidor solo observa terminalidad una vez registrado el resultado de
    release o `release_pending` operacional.

## Consumidores actuales

### External agent CLI/MCP

`externalagent.Selection.Value` deja de ser `any` no interpretado por la
composición productiva y contiene una selección tipada o handle interno. El
`ApprovedExecutor` concreto llama al coordinator y devuelve status/package del
runner. El principal nunca aporta target, spec path, plan path ni credenciales;
todos pertenecen al catálogo confiable. Registry/supervisor siguen poseyendo
idempotencia, cancelación y workers.

### GitHub PR

El `ValidationGateway` concreto traduce `githubpr.Selection` más el snapshot
refrescado a expected repository/head. El coordinator de GitHub continúa
poseyendo webhooks/checks/comentarios, mientras previewenv posee solo la
reserva/ejecución. `Status` y `Cancel` se apoyan en el servicio de aplicación;
cancelar propaga contexto y activa release. Forks solo pueden resolver una
selección `secretless` ya permitida; el preview coordinator no puede elevarla.

### Validación local

La CLI histórica `featureproof validate --target ...` conserva su contrato para
ejecución local explícita. Los nuevos caminos preview no pasan el target desde
el usuario: usan una factory interna que recibe `AuthorizedExecution`. El
refactor compartido evita duplicar runner/publisher y no convierte la CLI local
en un provider.

## Secretos y composición

Configuración no secreta se valida antes de resolver perfiles. Un registro de
providers guarda factories que reciben transportes autenticados ya construidos
en `cmd/featureproof`. Las factories de secretos no forman parte de
`previewenv.Provider`; así ningún fake/core puede pedir credenciales por ID.

Para credenciales de aplicación, la composición instala un transporte o
inyector concreto directamente en la herramienta autorizada. La selección y
el binding conservan solo `CredentialReference(profileID)`. Un perfil
`secretless` rechaza cualquier referencia antes de resolverla. Los closures que
contienen secretos no se serializan, no se pasan al harness/modelo y se liberan
con el lifetime de la composición.

## Política de red

La policy se carga por ID desde configuración y contiene esquemas, hosts,
puertos y base paths exactos permitidos. Target authorization es puramente de
configuración y parsing, por lo que ocurre antes de DNS. La ejecución conserva:

- resolución por operación y snapshot de IP;
- rechazo de destinos no autorizados antes del dial;
- pinning de la conexión a la IP validada;
- validación de cada redirección y autoridad;
- métodos, headers, body, timeout y tamaños del manifest/perfil.

Un hostname permitido no implica que cualquier puerto o subdominio lo esté.
No se usan suffix matches ambiguos. Si una policy permite loopback/local para
tests o desarrollo, debe hacerlo explícitamente por selección.

## Timeouts, cancelación y concurrencia

La configuración exige timeout positivo para reserve, readiness call, espera
total de readiness, release y shutdown. Cada child context usa el menor entre
timeout y deadline padre. El waiter inyectado observa cancelación; tests no
usan `time.Sleep`.

El supervisor existente posee la goroutine del run. El preview coordinator no
lanza goroutines ocultas desde constructores. Si implementa workers, expone
`Close/Wait`, guarda cancel funcs y hace join. No mantiene locks durante
provider, DNS, red, runner o waits. Un worker externalagent permanece
registrado hasta liberar su ownership y cerrar `done`; por ello `Cancel` une
también un worker local cuando el journal ya muestra estado terminal. Si no
hay worker local, la cancelación terminal conserva su idempotencia.

Antes de que externalagent/MCP confirme `queued`, el executor compuesto ejecuta
un preflight síncrono e idempotente. Este preflight persiste un intent
`handoff` completo y adquiere para el worker un owner/lease durable. Recovery
respeta una lease viva. El worker adopta el handoff mediante CAS antes de
reservar; tras crash y expiración, otro proceso lo reclama, reconcilia y libera
sin ejecutar el runner funcional. Si el journal no acepta el handoff, Submit
falla cerrado y no arranca el worker.

Cancelación principal impide nuevas readiness/ejecución. Release usa
`context.WithoutCancel` solo en el owner/finalizer y se limita inmediatamente
con `release_timeout`; nunca hereda un contexto infinito.

## Retries, idempotencia y recuperación

Provider errors se traducen a categorías propias. Solo `provider_unavailable`,
rate limiting explícito y transporte transitorio son retryables. Invalid,
unauthorized, mismatch, denied y conflict son permanentes.

Retry policy contiene max attempts, max elapsed, max cumulative wait, initial y
max backoff, reloj y jitter. Una espera se realiza solo si cabe completa en
deadline. Reserve siempre reutiliza idempotency key y el provider debe
reconciliar antes de adquirir otra reserva. Readiness es lectura. Release
reutiliza release key y trata `already_released` como éxito.

El registro durable guarda solo selección digest, provider/environment/reserve
IDs opacos, snapshot seguro, estado y keys hasheadas. Nunca guarda secretos. En
recovery, un owner reautoriza selección e integridad, consulta la misma reserva
y continúa readiness, execution status o release según el último estado. No
repite un run ya confirmado.

Recovery renueva ownership al reclamar, después de Reconcile y antes de cada
frontera de cleanup. Release intenta persistir `release_pending` con un
presupuesto propio y siempre concede después un presupuesto completo e
independiente a `Provider.Release`. Si el checkpoint no se confirma, el estado
durable anterior y la reservation key siguen siendo recuperables; si el
provider libera y el CAS final falla, `release_pending` conserva la intención
inequívoca. La siguiente recuperación reconcilia y libera idempotentemente.
Ninguna prueba de este protocolo depende del reloj de pared: lease viva,
expiración, checkpoints y reanudación se controlan con clock/waiter y barreras
inyectadas.

## Flujos de error

- Alias/configuración inválida: rechazo antes del provider.
- Reserve unavailable/unhealthy: error operacional, sin run funcional; release
  si ya se confirmó una reserva.
- Snapshot inválido/secret-bearing: integrity failure y release acotado.
- Attestation/revision mismatch: operacional antes del journal o inconclusive
  después de admisión.
- Target denied: operacional, sin DNS/socket/tool; release.
- Readiness timeout: operacional/inconclusive según admisión; release.
- Context cancellation: cancelled; release con contexto separado y acotado.
- Runner error: conserva clasificación existente; release no lo convierte en
  functional failure.
- Release exhausted: registra `release_pending`/`release_failed`; no declara
  liberación ni destruye el preview.

## Observabilidad

Eventos seguros: `preview_selection_resolved`, `preview_reserved`,
`preview_readiness_observed`, `preview_ready`, `preview_drifted`,
`preview_release_requested`, `preview_released`, `preview_release_pending`.
Incluyen run handle, digests, provider/environment/reservation IDs opacos,
estado cerrado, intento y duración. No incluyen target completo si no es
necesario, payload del provider, attestation raw, headers, secrets o mensajes
remotos.

Métricas: reservas por estado, latencia de reserve/readiness/release, polls,
retries, timeouts, cancelaciones, drift y releases pendientes. Logs no son
fuente de coordinación.

## Estrategia de pruebas

- Modelo/catálogo: alias exacto, selección defensiva, IDs/OID/timestamps,
  fingerprint, conflicto de idempotencia y payload secret-bearing rechazado.
- Provider fake programable: reserve/reconcile, secuencia pending→ready,
  unavailable/unhealthy, mismatch, target drift, respuesta incierta y release.
- Target policy: scheme/host/port/path permitidos y denegados; demuestra cero
  DNS/dial ante rechazo y conserva controles del HTTP tool.
- Coordinator: flujo completo, timeout por operación, deadline padre,
  cancelación en cada fase, retry budgets, release en todas las salidas y join.
- Seguridad: canarios de secretos con spies en provider, executor, model,
  journal, package y logs; solo el transporte autorizado observa su canario.
- Integración: externalagent y GitHub usan el mismo coordinator; binding exacto
  repository/commit/environment/target; package pertenece al run esperado.
- Race/concurrencia: reserve y release concurrentes con misma key convergen;
  no hay goroutines o locks retenidos en provider calls.
- AST: todas las entidades y funciones/métodos/helpers modificados, además de
  declaraciones `var`/`const` de paquete, tienen comentario sistémico.

Los tests ordinarios no llaman a red real ni a un provider real. Se ejecutan
`go test ./...`, suite comparable si persiste la limitación documentada de
symlinks en Windows, `go test -race`, `go vet`, builds requeridos,
`git diff --check` y coverage >=85% de paquetes nuevos/materialmente afectados.

## Trazabilidad inicial

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | Catalog, Selection, bridges | `TestExplicitAliasSelectsOneConfiguredProvider`, `TestUntrustedFieldsCannotSelectProviderOrTarget` |
| R2 | Provider, snapshot, reservation store | `TestReserveReturnsStableSafeSnapshot`, `TestReserveReconcilesSameKeyAndRejectsConflict` |
| R3 | AttestorBridge, coordinator, revisionbinding | `TestExactRepositoryCommitAttestationPrecedesExecution`, `TestAttestationMismatchOrDriftStopsWork` |
| R4 | TargetAuthorizer, HTTP composition | `TestProviderTargetPassesNetworkPolicy`, `TestDeniedTargetOpensNoDNSOrSocket`, `TestModelCannotReplaceAuthorizedTarget` |
| R5 | composition, secret transport factories | `TestSecretsReachOnlyAuthorizedTransport`, `TestSecretlessSelectionResolvesNoSecrets`, `TestCanariesNeverEnterArtifactsOrModel` |
| R6 | readiness loop | `TestReadinessPendingThenReadyStartsOnce`, `TestUnavailableUnhealthyOrDriftNeverExecutes` |
| R7 | child contexts, waiter, supervisor | `TestEveryProviderOperationHonorsTimeout`, `TestCancellationStopsWorkReleasesAndJoins` |
| R8 | error mapper, consumer projections | `TestPreviewFailuresAreOperationalOrInconclusiveNeverFailed`, `TestFunctionalAssertionFailureRemainsFailed` |
| R9 | release finalizer, retry/recovery | `TestEveryReservedPathReleases`, `TestReleaseIsIdempotentAndRecoversUncertainResult` |
| R10 | Provider interface, fake audit | `TestProviderContractHasNoInfrastructureMutation`, `TestMissingPreviewIsNeverProvisioned` |
| R11 | externalagent/GitHub bridges, repository AST check | `TestExternalAgentAndGitHubSharePreviewLifecycle`, `TestPreviewSystemicCommentsCoverChangedSymbols`, full test/race/coverage gates |

## Alternativas y decisiones

- Aceptar target y commit desde el request: descartado; entrega autoridad y
  atestación a datos no confiables.
- Inferir preview por branch/PR: descartado; el criterio exige selección
  explícita y las ramas son móviles.
- Definir un proveedor cloud concreto ahora: descartado; no hay proveedor ni
  contrato externo elegido. La interfaz propia permite añadirlo sin contaminar
  el core.
- Usar solo la URL como identidad: descartado; una URL puede rotar o servir
  otra revisión. Environment/reservation/repository/commit/attestation son
  identidades separadas.
- Confiar en readiness sin repetir attestation/policy: descartado; una
  observación posterior puede derivar.
- Destruir el entorno al finalizar: descartado; release cede únicamente la
  reserva lógica de un recurso preexistente.
- Guardar secretos cifrados en el journal: descartado; el core no los necesita
  y la composición puede recrear transportes por referencia.

El diseño preserva ARCH-001 a ARCH-012 y CONV-001 a CONV-010. No solicita
excepciones arquitectónicas.
