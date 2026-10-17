# Integración con pull requests de GitHub — Requirements

## Objetivo

Permitir que una GitHub App de FeatureProof reciba una solicitud explícita de
validación desde una pull request autorizada, vincule el run al `head` exacto,
y publique un check y un comentario resumen verificables sin ampliar la
autoridad del harness ni exponer credenciales o evidencia sin redactar.

## Alcance

- Recepción de webhooks de GitHub App autenticados y acotados.
- Disparo bajo demanda mediante el comando exacto `/featureproof validate` en
  un comentario general de una pull request.
- Autorización independiente de instalación, repositorio, pull request, actor,
  revisión y selección de validación.
- Creación o asociación de un run ordinario a la revisión exacta de la pull
  request mediante contratos propios.
- Publicación y actualización idempotente de un check por run/revisión y de un
  único comentario resumen por pull request.
- Política segura para pull requests desde forks.
- Retries acotados, rate limits, cancelación, recuperación y pruebas con un
  GitHub falso hermético.

## Fuera de alcance

- Crear, modificar, hacer checkout, merge, rebase o push de código o ramas.
- Aprobar revisiones, modificar branch protection, labels, workflows,
  deployments u otros checks.
- Ejecutar automáticamente en cada commit o en cada evento de pull request.
- Aprovisionar entornos preview; la selección debe resolver a una configuración
  confiable y la feature 19 aportará el adapter productivo correspondiente.
- Exponer artefactos locales, payloads completos de GitHub, respuestas del
  modelo o secretos mediante checks, comentarios o enlaces.
- Copiar DTOs o tipos de SDK de GitHub al núcleo.

## Actores y términos

- **GitHub App:** identidad instalada que recibe webhooks y obtiene tokens de
  instalación efímeros para un repositorio autorizado.
- **Delivery:** petición webhook identificada por `X-GitHub-Delivery`, firmada
  sobre sus bytes originales mediante `X-Hub-Signature-256`.
- **Solicitud bajo demanda:** comentario `issue_comment.created` cuyo cuerpo
  normalizado es exactamente `/featureproof validate`.
- **PR autorizada:** tupla de instalación, repositorio base, número de pull
  request, actor, estado y selección que una política confiable permite.
- **Head:** OID completo e inmutable leído de una consulta actual de la pull
  request, no de texto aportado por el usuario.
- **Asociación:** registro durable entre instalación, repositorio, PR, head,
  run, binding, check y comentario.
- **Fork no confiable:** pull request cuyo repositorio head difiere del
  repositorio base y que no satisface una política explícita de ejecución sin
  secretos.
- **Publicación propia:** check o comentario creado por esta instalación y
  registrado o reconciliado mediante identidad estable; la integración no
  actualiza publicaciones ajenas.

## Requirements EARS

### R1 — Disparo explícito y asociación autorizada

WHEN una delivery autenticada contiene un `issue_comment.created` sobre una
pull request abierta y el cuerpo normalizado es exactamente
`/featureproof validate`, THE integration SHALL consultar el estado actual de
la PR, autorizar instalación, repositorio, PR, actor y selección, y reservar
durablemente una única asociación antes de solicitar o asociar un run; THE
integration SHALL NOT iniciar validaciones por otros comentarios o eventos.

#### Acceptance criteria

- GIVEN un comando exacto en una PR abierta y autorizada
  WHEN se procesa la delivery
  THEN se reserva una asociación con una selección configurada y se solicita
  un único run mediante el caso de uso compartido de FeatureProof.
- GIVEN texto adicional, diferencias internas del comando, un comentario de
  issue, un evento `pull_request.synchronize` o un comentario editado
  WHEN se procesa
  THEN no se inicia un run.
- GIVEN instalación, repositorio, PR, actor o selección no autorizados
  WHEN llega un comando válido
  THEN se rechaza con categoría segura y no se invoca el harness.
- GIVEN una solicitud interna confiable para asociar un run ya existente
  WHEN el run y su binding pertenecen a la misma tupla autorizada y head
  THEN se crea la misma clase de asociación sin aceptar paths, URLs, targets,
  commits, credenciales o límites libres.

### R2 — Vinculación al head exacto y deriva de revisión

WHEN una solicitud queda autorizada, THE integration SHALL obtain the current
PR head as a complete immutable OID, bind the run through
`revision-bound-validation`, and recheck repository, PR and head before every
terminal publication; IF the head changes, THEN the old association and check
SHALL remain attached to the old OID, any active work SHALL be cancelled or
retired safely, and a later validation SHALL require a new explicit request
without reusing approval, binding or evidence.

#### Acceptance criteria

- GIVEN una PR con head H1
  WHEN se admite el comando
  THEN el run, binding, asociación y check declaran exactamente H1.
- GIVEN que la PR cambia a H2 antes de ejecutar o publicar el resultado
  WHEN la integración lo detecta por evento o relectura
  THEN no publica el resultado de H1 sobre H2, marca el check de H1 como
  `stale` cuando puede hacerlo y no inicia H2 automáticamente.
- GIVEN un nuevo comando sobre H2
  WHEN se admite
  THEN crea un run y check nuevos, y no reutiliza aprobación, evidencia ni
  conclusión de H1.
- GIVEN head ausente, abreviado, no resoluble, PR cerrada o datos refrescados
  incompatibles con el payload
  WHEN se intenta admitir
  THEN se detiene antes del trabajo funcional con un error operativo seguro.

### R3 — Autenticación del webhook e idempotencia de deliveries

WHEN una petición webhook llega, THE webhook boundary SHALL enforce a bounded
body, verify `X-Hub-Signature-256` as HMAC-SHA256 over the exact received bytes
with constant-time comparison before JSON decoding, validate the event and
delivery headers, and reserve `X-GitHub-Delivery` durably with a payload digest;
redelivery of the same ID and digest SHALL reconstruct the recorded response
without repeating work, while the same ID with different bytes SHALL fail as
an integrity conflict.

#### Acceptance criteria

- GIVEN firma ausente, mal formada o incorrecta
  WHEN se recibe el webhook
  THEN no se decodifica para negocio, no se reserva trabajo y se rechaza.
- GIVEN un payload válido dentro del límite
  WHEN se confirma su receipt durable
  THEN el endpoint puede responder sin esperar al run y el trabajo reservado
  puede recuperarse tras reinicio.
- GIVEN la misma delivery original o redelivered varias veces, incluso de
  forma concurrente
  WHEN se procesa
  THEN existe una sola reserva y como máximo un efecto por fase.
- GIVEN el mismo delivery ID con digest diferente
  WHEN se procesa
  THEN se devuelve conflicto y ninguno de los payloads amplía el trabajo ya
  confirmado.

### R4 — Autorización de instalación, repositorio y pull request

WHEN la integración procesa o reanuda cualquier operación, THE authorization
boundary SHALL verify the current installation identity, configured base
repository ID, pull request number and state, actor policy, app repository
access and head tuple; possession of a delivery ID, run ID, check ID, comment
ID or URL SHALL NOT confer authority.

#### Acceptance criteria

- GIVEN una instalación válida para otro repositorio, una instalación
  suspendida/eliminada o un repositorio no allowlisted
  WHEN se procesa el evento
  THEN no se consulta ni modifica un recurso fuera del ámbito autorizado.
- GIVEN una PR inexistente, cerrada, transferida o con repositorio/head distinto
  WHEN se reautoriza
  THEN se detiene el trabajo nuevo y no se revela información de runs ajenos.
- GIVEN un registro durable manipulado con IDs de otra instalación, repo o PR
  WHEN se reanuda o publica
  THEN la verificación de integridad falla cerrada antes de llamar a GitHub.
- GIVEN un token o payload que declara permisos
  WHEN se autoriza
  THEN la decisión se deriva de configuración confiable y observaciones
  refrescadas, no de `_meta`, texto del comentario ni campos libres.

### R5 — GitHub App y credenciales de mínimo privilegio

THE GitHub adapter SHALL use installation authentication scoped to the exact
authorized repository and only the repository permissions `Metadata: read`,
`Pull requests: read`, `Checks: write` and `Issues: write`; credentials SHALL be
resolved in composition, bounded by expiry, redacted from all state and passed
only to the GitHub transport, while all unrelated permissions remain absent.

#### Acceptance criteria

- GIVEN la configuración de la app o el token emitido
  WHEN se valida al arrancar y antes de usarlo
  THEN cualquier permiso adicional, ausencia de permiso requerido, repository
  scope distinto o expiración provoca fallo cerrado.
- GIVEN logs, journal, asociaciones, packages, checks, comentarios y errores
  WHEN se inspeccionan con secretos centinela
  THEN no contienen private key, JWT, installation token, webhook secret ni
  cabeceras de autenticación.
- GIVEN la ejecución del harness
  WHEN procesa una validación
  THEN no recibe credenciales de GitHub ni acceso al cliente de GitHub.

### R6 — Un check propio por run y revisión

WHEN una asociación obtiene un run, THE publisher SHALL create or reconcile
exactly one FeatureProof check run identified by a deterministic external ID
bound to installation, repository, PR, head and run, update only its recorded
check ID, and map lifecycle and outcome to GitHub without replacing or editing
another check.

#### Acceptance criteria

- GIVEN un run reservado o ejecutándose
  WHEN se publica progreso
  THEN el mismo check usa `queued` o `in_progress` y muestra un resumen seguro.
- GIVEN un resultado terminal verificado
  WHEN se publica
  THEN el check pasa a `completed` y mapea `passed → success`,
  `failed → failure`, `inconclusive → neutral`, `error → action_required` y
  `cancelled → cancelled`, conservando además el outcome FeatureProof explícito
  en el resumen.
- GIVEN retries, redelivery, reinicio o respuesta incierta al create/update
  WHEN se reconcilia
  THEN se recupera el check propio por identidad y no se crea otro.
- GIVEN un check ID no registrado, identidad externa distinta, app distinta,
  head distinto o run distinto
  WHEN se intenta actualizar
  THEN se rechaza sin mutarlo.

### R7 — Resumen y evidencias seguros

WHEN a check or comment is rendered, THE projection SHALL derive terminal
facts only from the verified evidence-package reader and emit bounded,
allowlisted fields for outcome, constraints, failures, unverified items and
opaque evidence references; evidence links SHALL be generated by a configured
link builder and accepted only as HTTPS URLs on exact allowlisted origins with
no userinfo, query, fragment, host substitution or local path.

#### Acceptance criteria

- GIVEN un paquete válido con passed, failed e inconclusive
  WHEN se renderiza
  THEN el resumen conserva cada estado, IDs estructurales y enlaces opacos a
  las evidencias correspondientes.
- GIVEN paquete ausente, corrupto, foreign o con binding/head inconsistente
  WHEN se publica
  THEN el check termina como error operativo y no presenta una conclusión
  funcional parcial.
- GIVEN secretos centinela, diagnósticos del modelo, payloads hostiles, paths o
  URLs manipuladas
  WHEN se proyecta
  THEN ninguno cruza la frontera y el enlace inválido se omite con indicación
  segura de evidencia no enlazada.
- GIVEN una proyección que excede los límites de GitHub o los límites locales
  WHEN se construye
  THEN se trunca por elementos completos, declara el recuento omitido y no
  corta marcadores ni escapes de forma insegura.

### R8 — Comentario resumen único y resistente a suplantación

WHERE comment publication is enabled, THE publisher SHALL create or update one
summary issue comment per authorized pull request using an exact versioned
hidden marker, persist its comment ID, and on reconciliation accept a discovered
comment only when both marker and configured GitHub App bot identity match;
untrusted matching text SHALL NOT be updated or treated as ownership proof.

#### Acceptance criteria

- GIVEN la primera asociación publicable de una PR
  WHEN se publica el resumen
  THEN se crea un comentario propio con marcador fijo y se registra su ID.
- GIVEN nuevas fases, resultado, redelivery o retry
  WHEN se publica de nuevo
  THEN se actualiza ese comentario y no aparece un duplicado.
- GIVEN respuesta incierta al crear el comentario
  WHEN se reconcilia
  THEN una búsqueda paginada y acotada recupera únicamente el comentario del
  bot con marcador exacto antes de decidir otro create.
- GIVEN un usuario que copia el marcador o un comment ID ajeno
  WHEN se procesa
  THEN la integración no modifica ese comentario.

### R9 — Política de forks sin secretos

IF the PR head repository differs from the base repository or the head source
is unavailable, THEN THE integration SHALL deny execution by default; WHERE a
trusted fork policy explicitly authorizes the exact repository and head, THE
integration SHALL use a credential-free validation profile with no repository,
organization, user, environment or application secrets, keep GitHub
credentials outside the harness, and require the ordinary policy and approval
gates.

#### Acceptance criteria

- GIVEN un fork sin política explícita
  WHEN llega un comando válido
  THEN no se crea un run funcional ni se resuelven secretos y se publica solo
  una razón segura cuando la autorización permite informar.
- GIVEN un fork autorizado para perfil sin secretos
  WHEN se valida
  THEN target, capabilities y entorno están preconfigurados como públicos o
  efímeros sin credenciales y permanecen vinculados al head exacto.
- GIVEN cualquier selección que requiere un secreto o un perfil privilegiado
  WHEN el head procede de fork
  THEN la admisión falla aunque el actor tenga permisos sobre el repositorio
  base.
- GIVEN código o texto controlado por el fork
  WHEN se ejecuta y publica
  THEN nunca recibe private key, token de instalación, webhook secret ni
  credenciales de la aplicación validada.

### R10 — Retries, rate limits y cancelación acotados

IF a GitHub operation returns a retryable transport failure, `408`, `429`,
`5xx`, primary exhaustion or secondary rate limiting, THEN THE adapter SHALL
classify it, honor a bounded `Retry-After` or `X-RateLimit-Reset` when present,
otherwise use configured exponential backoff with injected jitter, and stop at
the earlier of context cancellation, operation deadline, maximum attempts or
maximum cumulative wait; non-idempotent creates SHALL be reconciled before any
retry and permanent `4xx` SHALL NOT be retried.

#### Acceptance criteria

- GIVEN `Retry-After` o primary exhaustion con reset dentro del presupuesto
  WHEN se reintenta
  THEN el reloj falso observa la espera indicada y no hay petición anticipada.
- GIVEN secondary limit sin espera válida o `5xx`
  WHEN se reintenta
  THEN el backoff es acotado, serializa escrituras de la asociación y termina
  al agotar la política.
- GIVEN create de check o comentario con resultado incierto
  WHEN se considera un retry
  THEN se ejecuta primero la reconciliación y no se duplica el recurso.
- GIVEN cancelación durante petición, espera, run o publicación
  WHEN se observa
  THEN se propaga, no comienza trabajo posterior, se unen los workers propios y
  solo una finalización acotada intenta dejar el check consistente.

### R11 — Estado durable, recuperación y mutaciones restringidas

WHEN work is admitted, THE integration SHALL persist an append-only audit of
delivery receipt, authorization snapshot, association, head, run handle,
publication intents and confirmed GitHub IDs, use per-association atomic
compare-and-swap/leases without holding locks across external calls, and recover
unfinished intents idempotently; THE adapter SHALL expose no mutation besides
create/update of its own check and create/update of its own summary comment.

#### Acceptance criteria

- GIVEN caída tras reservar, enviar o recibir una operación
  WHEN reinicia otro proceso
  THEN reclama el trabajo vencido, reconcilia efectos inciertos y no reinicia
  automáticamente un run confirmado.
- GIVEN dos deliveries o workers para la misma PR/head/run
  WHEN compiten
  THEN CAS y lease permiten un único owner por efecto sin bloquear otras PRs
  durante red, espera o disco ajeno al commit local.
- GIVEN inspección del fake GitHub
  WHEN termina cualquier escenario
  THEN no existen llamadas para modificar contents, refs, branches, reviews,
  merges, labels, workflows, deployments, branch protection u otros checks.
- GIVEN estado corrupto o una publicación que no puede reconciliarse
  WHEN se recupera
  THEN finaliza con categoría operativa o queda pendiente de retry acotado, sin
  fabricar success ni borrar la evidencia confirmada.

### R12 — Fronteras propias, verificación hermética y comentarios sistémicos

THE feature SHALL keep GitHub DTOs and SDK types inside its adapter, expose
small consumer-owned interfaces and internal models, and be verified with a
deterministic fake GitHub, fake validation service, package reader, store,
clock, jitter, IDs and credentials without network or real secrets; every
introduced or modified entity, function and test helper SHALL include a
comment describing its responsibility in the system.

#### Acceptance criteria

- GIVEN los tests ordinarios
  WHEN se ejecutan
  THEN cubren publicación, replay y concurrencia idempotentes, head nuevo,
  firma inválida, autorización, fork no confiable, paquete corrupto, rate limit,
  error transitorio, respuesta incierta, cancelación y recuperación sin red.
- GIVEN R1–R11
  WHEN se revisa la trazabilidad
  THEN cada requirement se relaciona con al menos un test nominal, el paquete
  afectado alcanza al menos 85% de statement coverage y race verification pasa.
- GIVEN un tipo, constante, variable de paquete, interfaz, método, función,
  constructor o helper de test nuevo o modificado
  WHEN se revisa el código
  THEN contiene un comentario que explica su responsabilidad dentro del
  sistema y no se limita a repetir su sintaxis.
- GIVEN el core de integración
  WHEN se inspeccionan imports y modelos
  THEN no importa SDKs de GitHub, HTTP ni credenciales; el fake implementa la
  misma interfaz mínima consumida por la aplicación.

## Errores y casos límite

- El body se normaliza únicamente retirando espacios ASCII al principio/final
  y una terminación de línea; cualquier texto, markdown, argumento o segundo
  comando impide el disparo.
- Los IDs numéricos de GitHub se validan positivos y se guardan como
  identidades externas, nunca como autoridad por sí solos. Owner/name son datos
  de presentación; `repository.id`, `installation.id`, número de PR y OID son
  la identidad de autorización.
- Un evento puede llegar tarde o desordenado. La relectura actual de la PR y el
  CAS prevalecen sobre el orden del webhook.
- La delivery se considera aceptada solo después de persistir el receipt. Un
  evento soportado pero irrelevante se registra como ignorado; uno inválido no
  crea estado de negocio.
- La asociación permite un único run por clave idempotente y tupla
  PR/head/selección. Un nuevo comando sobre el mismo head puede reconstruir esa
  solicitud mientras siga siendo la misma clave derivada de delivery; una
  solicitud explícitamente nueva puede crear otro run y, por tanto, otro check.
- `external_id` ayuda a reconciliar checks pero no se presupone una restricción
  de unicidad impuesta por GitHub; la unicidad procede del store, CAS, búsqueda
  por app/head y validación de identidad.
- El comentario es único por PR y representa la asociación más reciente; los
  checks anteriores siguen preservando cada resultado por run/revisión.
- Un límite indicado por GitHub que excede el deadline local no se duerme
  parcialmente: se registra como agotamiento operativo y se cede al scheduler
  o se finaliza según la política durable.
- Los errores públicos emplean categorías cerradas (`invalid_webhook`,
  `unauthorized`, `stale_head`, `rate_limited`, `github_unavailable`,
  `integrity_failure`, `cancelled`) sin incorporar bodies ni mensajes remotos.
- El token de instalación se solicita para la instalación y repositorio exactos
  y, cuando GitHub lo permite, se reduce también al subconjunto de permisos
  requerido. Nunca se persiste ni se comparte con el harness.

## Decisiones abiertas

Ninguna para esta entrega. El adapter de preview productivo continúa siendo
dependencia de la feature 19; hasta entonces, cualquier selección que lo
requiera falla cerrada aunque el contrato GitHub se pueda verificar con fakes.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-16T21:51:08+02:00
```
