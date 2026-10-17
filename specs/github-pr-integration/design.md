# Integración con pull requests de GitHub — Design

## Resumen de la solución

Se añade un módulo `internal/githubpr` que traduce webhooks y operaciones de
GitHub a un modelo propio. El límite HTTP autentica los bytes originales antes
de decodificarlos; el servicio reconsulta y autoriza la PR; un coordinador
durable reserva una asociación entre PR, head y run; y un publisher proyecta
estado seguro a un único check y comentario propios. El harness continúa detrás
del caso de uso de validación y nunca conoce tokens ni tipos de GitHub.

```text
GitHub webhook bytes
    │ HMAC + límite + delivery receipt
    ▼
Webhook adapter ──> DTO GitHub ──> Event propio
                                      │
                        refresh PR + autorización
                                      │
                                      ▼
                           asociación PR/head/run
                                      │
                  validation gateway + revision binding
                                      │
                                      ▼
                         paquete verificado / estado
                                      │
                 proyección segura + intents durables
                                      │
                                      ▼
                       check propio + comentario propio
```

## Fuentes de contexto consultadas

### Repositorio

- `feature_list.json`, feature 18: asociación exacta a head, checks,
  comentario único, cambio de revisión, autenticación, forks, mínimo privilegio
  y fake GitHub.
- `docs/architecture.md`: adaptadores sustituibles, interfaces del consumidor,
  evidencia antes de conclusiones, cancelación, journal append-only y
  publicación atómica.
- `docs/conventions.md`: contextos propagados, errores tipados, dependencias
  explícitas, tests herméticos y comentarios que expliquen responsabilidad.
- `docs/specs.md` y `.codex/agents/spec.md`: EARS, trazabilidad y aprobación
  humana previa a código.
- `internal/revisionbinding`: binding inmutable por repository ID, OID completo,
  entorno y hashes seguros; la integración suministra una selección confiable
  y no altera sus invariantes.
- `internal/localvalidation` package reader: única fuente para proyectar hechos,
  fallos y evidencias terminales después de validar manifest, digests,
  referencias y binding.
- `internal/externalagent`: contratos de aplicación, handles opacos,
  reautorización, idempotencia, supervisor y proyección segura; el gateway de
  GitHub reutiliza esos casos de uso en lugar de acceder al harness.
- `internal/correction`: runs hijo inmutables, CAS/leases, recuperación y
  comparación; una corrección asociada a PR se publica como otro run/check sin
  reinterpretar su lineage.

### Contratos oficiales de GitHub observados

- [Validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries): la firma llega en
  `X-Hub-Signature-256` y se calcula con el webhook secret sobre el payload; se
  valida antes de procesar.
- [Best practices for using webhooks](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks):
  `X-GitHub-Delivery` identifica la delivery y se conserva en redelivery; las
  deliveries pueden recuperarse y requieren deduplicación.
- [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads):
  los eventos de GitHub App incluyen instalación; `issue_comment` se usa para
  comentarios generales de issues y PRs, y `pull_request` expone actividad y
  datos de head. Estos payloads se traducen a tipos propios y luego se refrescan.
- [REST pull requests](https://docs.github.com/en/rest/pulls/pulls): una PR
  expone `head.sha`, repositorio head y repositorio base; el adapter usa la
  lectura actual, no conserva el DTO como modelo de dominio.
- [Checks API](https://docs.github.com/en/rest/checks/runs): un check se crea
  para `head_sha`, acepta `external_id`, estados `queued`, `in_progress` y
  `completed`, y conclusions cerradas. Crear y actualizar checks requiere
  permiso `Checks: write` para la GitHub App.
- [Issue comments API](https://docs.github.com/en/rest/issues/comments): las
  pull requests también son issues para comentarios generales; crear contenido
  rápidamente puede activar secondary rate limiting.
- [Permissions required for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps):
  los endpoints de check requieren Checks y los de PR/comentarios requieren los
  permisos de repositorio correspondientes. La app solicita solo Metadata read,
  Pull requests read, Checks write e Issues write.
- [Authenticating as a GitHub App installation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation):
  el token de instalación puede restringirse por repositories y permissions,
  informa su expiración y expira aproximadamente en una hora.
- [REST API rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)
  y [REST best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api):
  primary exhaustion usa `X-RateLimit-Remaining/Reset`; secondary limits pueden
  usar `Retry-After`; GitHub recomienda esperas y backoff, escrituras seriales y
  un máximo de retries.
- [Secure use of pull_request_target](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target):
  ejecutar código de una PR no confiable junto a tokens privilegiados o secretos
  permite exfiltración. Aunque esta integración es una GitHub App y no un
  workflow, se adopta la misma frontera: código de fork y secretos nunca
  comparten ejecución.

La versión de REST se configura y valida en el adapter; no forma parte de los
modelos internos. Ningún tipo de Octokit ni de otro SDK cruza la frontera.

## Componentes y responsabilidades

| Componente propuesto | Responsabilidad |
|---|---|
| `internal/githubpr` model | Identidades propias `Delivery`, `PullRequestSnapshot`, `Association`, `PublicationIntent`, outcomes y errores cerrados. |
| `WebhookVerifier` | Comprueba límite, HMAC-SHA256 y cabeceras sobre bytes originales antes de entregar un evento decodificado. |
| `DeliveryStore` | Reserva delivery ID + digest, conserva resultado/replay y journal append-only con CAS/lease. |
| `PullRequestGateway` | Interfaz mínima consumida para refrescar PR, resolver instalación/token y crear, leer o actualizar solo checks/comentarios propios. |
| GitHub REST adapter | Mantiene DTOs HTTP, API version, auth, clasificación de errores/rate limits y traducción al modelo propio. |
| `RequestAuthorizer` | Decide sobre instalación, repo, PR, actor, head, fork y alias de selección desde configuración confiable. |
| `ValidationGateway` | Solicita, consulta y cancela runs mediante el contrato de aplicación existente; no expone harness ni paths. |
| `AssociationStore` | Posee la relación durable PR/head/run/check/comment, intents, CAS, leases y recuperación. |
| `Coordinator` | Orquesta admisión, refresh, autorización, run y publicación sin mantener locks durante fronteras externas. |
| `SafeProjector` | Lee estado y package verificado y construye resúmenes limitados con vocabulario allowlisted. |
| `EvidenceLinker` | Convierte handles opacos en enlaces HTTPS sobre origins configurados y validados. |
| `RetryPolicy` | Clasifica respuesta, calcula espera acotada con reloj/jitter inyectados y observa cancelación/deadline. |
| `CredentialSource` | Vive en composición, emite tokens efímeros restringidos y nunca entrega secretos al core o harness. |

Todas las entidades, funciones, métodos y helpers nuevos o modificados llevarán
un comentario que explique su responsabilidad sistémica, incluidos símbolos
internos y de test.

## Paquetes afectados

- Nuevo `internal/githubpr`: core de integración, store durable, webhook,
  coordinator, publisher, retry y adapter REST en archivos cohesionados.
- `internal/externalagent`: extensión mínima del consumidor para asociar o
  iniciar una validación con identidad confiable de PR/head; conserva el
  contrato existente y no añade tipos de GitHub al servicio.
- `internal/revisionbinding`: se reutiliza sin cambiar su modelo; un bridge
  traduce repository/head autorizados a `Selection`.
- `internal/localvalidation`: se consume `ReadPackage`; no se lee evidence raw.
- `cmd/featureproof`: composición explícita del servidor/webhook, secretos,
  store, permisos y timeouts. Hasta feature 19, una selección productiva sin
  preview confiable falla cerrada.

No se requiere importar un SDK. Si la implementación elige uno dentro del
adapter, sus tipos quedan encerrados allí y los tests del core usan el contrato
propio.

## Contratos de datos propios

`PullRequestSnapshot/v1` contiene IDs positivos de instalación, repositorio
base, PR y actor autorizado; estado; OID completo; ID de repositorio head;
indicador de fork; y un fingerprint canónico. No conserva títulos, body,
branch names, clone URLs ni credenciales. La representación se sella y se
compara con el estado refrescado.

`Association/v1` contiene ID/versión, delivery origin, snapshot digest,
selection alias, run handle e identidad/binding seguros, lifecycle, external ID
determinista del check, check ID confirmado, comment ID confirmado, intents y
timestamps. La clave lógica es instalación + base repository + PR + head + run.
Los handles de FeatureProof se reautorizan en cada lectura; no son autoridad por
posesión.

`DeliveryReceipt/v1` contiene delivery ID, event allowlisted, payload SHA-256,
estado y association ID opcional. No persiste el body. El mismo ID/digest
reconstruye la respuesta; otro digest es conflicto de integridad.

`PublicationIntent/v1` contiene asociación/version, kind, payload digest,
identidad esperada y estado `prepared|confirmed|abandoned`. El contenido
renderizado seguro puede persistirse con límites; nunca contiene headers,
tokens o cuerpos remotos. Create incierto se resuelve mediante list/get y
validación de app, head, external ID o bot+marker.

## Flujo principal

1. El endpoint limita los bytes, valida cabeceras y HMAC en tiempo constante y
   calcula el digest antes de decodificar.
2. Reserva durablemente la delivery. Un replay devuelve el resultado conocido;
   un receipt nuevo habilita trabajo recuperable y una respuesta rápida.
3. Traduce solo `issue_comment.created`; confirma que `issue.pull_request`
   existe y que el comentario contiene el comando exacto.
4. Refresca la PR mediante token de instalación restringido. El authorizer
   verifica instalación, repo base, PR abierta, actor, head, fork y selección.
5. Reserva la asociación y crea/consulta el run mediante `ValidationGateway`.
   El bridge sella `revisionbinding` con el repository ID y OID exactos.
6. Prepara y confirma un único check `queued/in_progress` sobre ese OID. Si la
   respuesta create es incierta, reconcilia antes de reintentar.
7. Consulta el run con esperas cancelables o recibe sus cambios mediante un
   mecanismo interno. Cada fase relevante actualiza el mismo check y comentario.
8. Antes de concluir, refresca la PR y comprueba snapshot/head. Si cambió,
   cancela trabajo activo cuando corresponda y termina el check antiguo como
   `stale`; no crea run para el head nuevo.
9. Para un run terminal, lee exclusivamente el package verificado, comprueba
   binding/head, proyecta campos seguros y confirma intents de update. Outcome
   de FeatureProof y conclusion de GitHub permanecen distinguibles.
10. Confirma el resultado de delivery/asociación. El shutdown cancela y hace
    join de workers dentro del timeout configurado.

## Publicación de checks

El nombre del check es estable, por ejemplo `FeatureProof validation`; la
identidad real es un `external_id` derivado canónicamente de instalación,
repository ID, PR, head y run, sin secretos. El check ID devuelto se confirma en
el store. La reconciliación lista de forma paginada y acotada solo checks de la
app/head relevante, exige external ID exacto y falla ante más de una
coincidencia en lugar de elegir arbitrariamente.

| FeatureProof | GitHub status | GitHub conclusion |
|---|---|---|
| reservado | `queued` | — |
| ejecutando/finalizando | `in_progress` | — |
| `passed` | `completed` | `success` |
| `failed` | `completed` | `failure` |
| `inconclusive` | `completed` | `neutral` |
| `error` | `completed` | `action_required` |
| `cancelled` | `completed` | `cancelled` |
| head sustituido | `completed` | `stale` |

El título y summary tienen límites inferiores o iguales a los de GitHub y se
construyen por elementos completos. No se publican annotations de archivos en
esta entrega porque los fallos actuales no prueban una ubicación fuente y no se
inventan rutas o líneas.

## Comentario resumen y enlaces

El comentario usa un marcador constante versionado que no incorpora datos de
usuario. El ID confirmado es la vía normal de update. Tras create incierto, la
reconciliación pagina hasta un máximo configurado y solo acepta marcador exacto
más la identidad numérica del bot configurada; un marcador por sí solo no
demuestra propiedad. El comentario refleja la asociación más reciente de la PR
y enlaza los checks/runs anteriores solo mediante referencias seguras si están
disponibles.

`EvidenceLinker` recibe handles opacos provenientes de la proyección verificada.
Ensambla rutas sobre una base configurada y vuelve a parsear/validar el URL:
HTTPS, origin exacto allowlisted, sin userinfo, query ni fragment y con límites
de longitud. Un enlace inválido se omite; no se sustituye por path local o URL
de un payload.

## Autorización, instalación y forks

La app se instala solo en repositorios elegidos. La composición requiere
`Metadata: read`, `Pull requests: read`, `Checks: write` e `Issues: write`, y
rechaza permisos extra conocidos. Cada token se pide para el repository ID
exacto y subconjunto de permisos; expiry se comprueba con reloj inyectado. La
private key, JWT, webhook secret y token viven en composición/transporte.

`RequestAuthorizer` recibe identidades estructurales, no texto. Puede usar una
allowlist local de instalaciones/repositorios/actores/selecciones y una
observación actual del acceso de la instalación. Una PR de fork falla por
defecto. La excepción requiere una regla exacta, aprobación normal y un alias
marcado `secretless`: entorno y target sin credenciales, capabilities sin
secretos y sin acceso a perfiles privilegiados. El token de GitHub solo lo usa
el publisher/control plane y nunca entra en el proceso o requests del harness.

## Flujos de error

- Firma/body/header inválido: rechazo en el boundary sin trabajo ni parseo de
  negocio.
- Delivery repetida: replay del receipt; digest distinto: integrity failure.
- Evento no soportado o comando ausente: receipt `ignored`, sin run.
- PR/instalación/repo/actor/fork no autorizado: rechazo seguro, sin revelar
  handles ni consultar otros recursos.
- Head inválido o cambiado: no se atribuye resultado al head nuevo; cancelación
  o `stale` sobre el check antiguo.
- Run/package/binding inválido: `action_required` operativo, nunca `failure`
  funcional fabricado.
- 4xx permanente: no retry. 401/403 de autorización invalidan el token y
  reautorizan una sola vez solo si la política lo permite; no se transforman en
  rate limit sin headers/categoría compatibles.
- 408/429/5xx/transporte/rate limit: retry acotado. Create incierto siempre
  reconcilia antes.
- Store corrupto o conflicto de ownership: se detiene; no se muta GitHub hasta
  recuperar una identidad verificable.

## Estado, concurrencia y recuperación

El journal append-only es la fuente auditable; un snapshot derivado acelera la
lectura. Las transiciones usan CAS por delivery/asociación y leases con owner y
expiry. Ningún lock permanece durante HTTP, validación, espera, callback o
filesystem externo al commit corto del store. Asociaciones distintas progresan
en paralelo; los writes de una misma asociación se serializan para preservar
orden y reducir secondary limiting.

Toda goroutine pertenece al coordinator, tiene contexto cancelable, `done` y
join. El presupuesto del run comienza antes de refresh y submit; la lease debe
superar ese presupuesto y cada deadline externo con margen validado. La
recuperación valida un grupo de una asociación y todos sus intents, y publica
sus claims en una única transición durable acotada por debajo del máximo real
de una línea del journal. El coordinator renueva y recupera ese grupo antes de
reclamar el siguiente; así ninguna lease espera detrás de un backlog sin límite.
Otro proceso puede tomar el siguiente grupo y ambos progresan sin compartir un
efecto. Cada drain mantiene un conjunto de IDs ya entregados y lo pasa al store:
si un worker falla y libera durante ese mismo drain, el owner no lo reclama de
nuevo ni bloquea IDs posteriores. Otro coordinator puede tomar la lease ya
liberada y efectuar un único handoff dentro de su propio drain acotado. Un owner
no vuelve a recibir un claim que ya conserva vivo. Después reanuda intents, pero
no vuelve a solicitar un run ya confirmado. Si el startup falla, cede el lote
actual todavía no entregado y cancela/join los workers ya entregados mediante
su supervisor. El supervisor expone una espera de idle ligada al ciclo real de
workers para shutdown y pruebas, sin depender de sleeps. Un intent conserva su DTO seguro completo y
se reintenta directamente, sin reconstruirlo desde configuración mutable. Un
resultado incierto se reconcilia; solo una ausencia definitiva autoriza repetir create. La
finalización posterior a cancelación usa un contexto independiente pero
acotado, registra el estado seguro alcanzado y después libera ownership.

La reconciliación del comentario exige bot, marker e ID propios. El body remoto
se compara completo con el DTO durable: igualdad confirma sin write y cualquier
versión propia desactualizada requiere un update exacto observado antes de
confirmar el intent.

## Retries y rate limits

`RetryPolicy` recibe máximo de intentos, duración total, espera acumulada,
backoff inicial/máximo, reloj y jitter. Prioridad de espera:

1. `Retry-After` válido;
2. si `X-RateLimit-Remaining=0`, instante `X-RateLimit-Reset`;
3. backoff exponencial con jitter inyectado para secondary/transient failures.

La espera se admite solo si cabe completa en los presupuestos y deadline. Cada
request observa el contexto. Las mutaciones permanentes 4xx no se reintentan.
Los bodies remotos se descartan o redactan y no forman parte del error público.
Las métricas conservan categoría, intento y duración, nunca headers sensibles.

## Observabilidad

Eventos estructurados: `delivery_received|replayed|ignored|rejected`,
`pr_authorized`, `association_reserved`, `run_linked`, `head_changed`,
`publication_prepared|confirmed|reconciled|abandoned`, `retry_scheduled`,
`rate_limited`, `association_terminal`. Incluyen IDs/digests seguros, fase y
categorías cerradas. No incluyen comment body, webhook payload, API body,
token, URL con query, evidencia raw ni mensajes arbitrarios.

Métricas: deliveries por resultado, asociaciones por outcome, latencia de
admisión/publicación, retries por categoría, rate-limit waits, reconciliaciones
y head drift. Logs añaden installation/repository/PR/run IDs allowlisted; el
journal, no el log, coordina la recuperación.

## Estrategia de pruebas

- Tests unitarios de HMAC, límites, command parser, canonicalización, digests,
  mapping de estados, projector, URL policy, marker ownership y errores.
- Tests de servicio con fake GitHub programable, fake validation gateway,
  package reader, authorizer, token source, store, reloj, jitter e IDs.
- Tablas de webhook: válido, firma inválida, replay, collision, evento tardío,
  issue no PR, comando no exacto, instalación/repo/actor no autorizados.
- Contrato de check/comment: progreso y cinco outcomes, update idempotente,
  create incierto, duplicado conflictivo, marker suplantado y truncación.
- Head/fork: H1→H2, cancelación, nueva solicitud sin herencia, fork denegado y
  fork secretless autorizado sin secretos centinela.
- Retry: Retry-After, primary reset, secondary backoff, 5xx, 4xx permanente,
  límite agotado y cancelación con reloj falso, sin `time.Sleep`.
- Durabilidad/concurrencia: reinicio en cada intent, lease vencido, CAS,
  deliveries y publishers concurrentes, recovery y shutdown/join.
- Test de mutaciones permitidas que inspecciona todas las llamadas del fake y
  demuestra que solo se leen PR/check/comment y se crean/actualizan el check y
  comentario propios.
- `go test -race` y coverage >=85% del paquete afectado. No hay test real de red
  en `go test`; una prueba real futura será opt-in y separada.

## Alternativas y decisiones

- Se elige `issue_comment.created` con comando exacto porque el producto pide
  validación bajo demanda. Labels y ejecución automática quedan fuera para no
  ampliar permisos ni triggers.
- Se refresca la PR en admisión y publicación; confiar solo en el webhook no
  protege contra eventos tardíos o head móvil.
- Se usa Checks API en lugar de commit statuses para expresar lifecycle,
  conclusión y details URL, y para aislar la publicación por GitHub App.
- Se mantiene un único comentario por PR, mientras que cada run/revisión tiene
  su check inmutable. Así el timeline no se llena de retries y el historial
  exacto permanece ligado al commit.
- Se usa REST detrás de interfaz propia. Adoptar tipos de SDK en el core
  acoplaría dominio, tests y errores al proveedor.
- Los forks se deniegan por defecto. Confiar en la identidad del actor o en que
  GitHub redacte secretos no evita que código hostil los exfiltre durante una
  validación.
- No se usan annotations de archivos: la evidencia actual no garantiza path y
  línea de código, y la integración no inventa localizaciones.

## Mapa inicial de trazabilidad

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | webhook parser, authorizer, coordinator, validation gateway | `TestCommandStartsOneAuthorizedValidation`, `TestNonExactCommandIsIgnored`, `TestTrustedAssociationRejectsFreeInputs` |
| R2 | PR gateway, revision bridge, association store | `TestAssociationUsesFreshExactHead`, `TestHeadChangeStalesOldCheckWithoutStartingNewRun`, `TestNewHeadDoesNotReuseApprovalOrEvidence` |
| R3 | webhook verifier, delivery store | `TestWebhookRejectsInvalidSignatureBeforeDecode`, `TestDeliveryReplayIsIdempotent`, `TestDeliveryIDDigestCollisionFails` |
| R4 | request authorizer, sealed association | `TestAuthorizationRejectsForeignInstallationRepositoryAndPR`, `TestEveryResumeReauthorizes`, `TestTamperedAssociationFailsClosed` |
| R5 | composition, credential source, REST adapter | `TestInstallationTokenHasExactRepositoryAndPermissions`, `TestSecretsNeverCrossBoundaries`, `TestExpiredTokenFailsClosed` |
| R6 | check publisher, publication intents | `TestCheckLifecycleAndOutcomeMapping`, `TestCheckCreateReconcilesUncertainResponse`, `TestPublisherNeverUpdatesForeignCheck` |
| R7 | package reader bridge, projector, evidence linker | `TestSummaryComesFromVerifiedPackage`, `TestCorruptPackagePublishesOperationalError`, `TestProjectionOmitsSecretsUnsafeLinksAndOversizeItems` |
| R8 | comment publisher, marker verifier | `TestSummaryCommentIsUpdatedWithoutDuplicates`, `TestUncertainCommentCreateReconcilesOwnedMarker`, `TestSpoofedMarkerIsNeverUpdated` |
| R9 | fork policy, selection resolver | `TestUntrustedForkNeverStartsOrReceivesSecrets`, `TestExplicitSecretlessForkPolicyBindsExactHead`, `TestForkRejectsCredentialedSelection` |
| R10 | retry policy, supervisor | `TestRetryHonorsRetryAfterAndPrimaryReset`, `TestSecondaryLimitBackoffIsBounded`, `TestCancellationStopsWaitAndJoinsWork` |
| R11 | journal, CAS, lease, recovery, fake audit | `TestRecoveryReconcilesIntentWithoutRepeatingRun`, `TestConcurrentWorkersConfirmOneEffect`, `TestOnlyAllowedGitHubMutationsOccur` |
| R12 | interfaces, adapters, fakes, repository checks | `TestGitHubContractUsesInternalDTOs`, `TestSystemicCommentsCoverChangedSymbols`, full package suite + race + coverage |

