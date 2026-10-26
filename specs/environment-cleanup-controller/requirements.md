# Controlador durable de limpieza de entornos — Requirements

## Objetivo y problema

FeatureProof ya persiste el lifecycle de cada entorno y Kubernetes deja una
identidad, ownership y expiración verificables, pero la limpieza todavía depende
de que el proceso que ejecuta el run alcance su finalización síncrona. Un crash,
una cancelación, una respuesta perdida o un namespace huérfano pueden conservar
capacidad indefinidamente. Además, la adquisición actual no reserva de forma
atómica una cuota agregada por proyecto antes de llamar al provider.

Esta feature incorpora un controlador independiente que descubre trabajo de
cleanup desde estados durables y desde inventarios gestionados explícitamente,
reclama cada obligación con lease/CAS y converge hasta confirmar ausencia. La
misma entrega añade reservas de capacidad provider-neutral antes de Acquire, de
modo que ambientes simultáneos, recursos acumulados y TTL no superen la policy
confiable. Los fallos de cleanup permanecen operacionales y nunca reescriben el
veredicto funcional de un run.

## Alcance

- Identidad y estado durable de cleanup ligados a provider, owner, proyecto,
  run, acquisition, environment, UID/fingerprint, TTL y expiración.
- Fuente durable de intents producidos por terminales, cancelación, errores y
  expiración, más recuperación de intents interrumpidos tras restart.
- Controlador cancelable con `RunOnce` acotado y loop periódico poseído por el
  caller, sin goroutines ocultas en constructores.
- Claims con lease, CAS y fencing de versión para varios controladores sobre el
  mismo store local.
- Cleanup idempotente con identidad estable, retries/backoff durables y
  taxonomía de errores segura.
- Inventario provider-specific y acotado para detectar entornos gestionados
  huérfanos, sin API genérica ni autoridad aportada por modelo/caller.
- Reserva de capacidad antes de Acquire con límites por proyecto para cantidad
  simultánea, CPU, memoria, objetos y TTL máximo.
- Proyección de estado y journal append-only content-free para auditoría.
- Adapter Kubernetes para inventariar y eliminar únicamente namespaces
  FeatureProof inequívocos mediante precondición de UID.
- Tests herméticos con reloj, waiter, store, provider e inventario falsos.

## Fuera de alcance

- PostgreSQL, migraciones, colas durables, workers remotos o protocolo de
  distribución; corresponden a la feature 35.
- Configuración versionada general por proyecto y herencia organizativa;
  corresponde a la feature 36. Esta entrega recibe policies selladas desde
  composición.
- Organizaciones, tenants, miembros, RBAC multi-tenant o rate limits de tenant;
  corresponden a la feature 37. En esta entrega la clave de equipo es el
  `ProjectID` sellado ya existente.
- Borrar runs, evidencias, packages, journals o configuración del proyecto.
- Forzar finalizers, eliminar un namespace por nombre/selector, adoptar recursos
  ambiguos o corregir metadata ajena.
- Proporcionar al modelo una tool de cleanup, inventario o Kubernetes.
- Alterar la evaluación funcional, el package o el report ya confirmados.
- Reemplazar los controles de release de las features 31 y 32; el controlador
  los invoca o amplía solo mediante puertos estrechos de recovery.

## Actores y términos

- **Cleanup controller:** caso de uso independiente que descubre, reclama y
  reconcilia obligaciones durables; no es una tool ni un worker remoto.
- **Cleanup intent:** obligación persistida antes del primer efecto destructivo,
  identificada de forma estable e idempotente.
- **Cleanup item:** proyección durable de un lifecycle conocido o de un orphan
  receipt confirmado, con estado, attempts, next-attempt y lease.
- **Lifecycle cleanup binding:** autoridad privada y sellada que liga proyecto,
  run, provider/version, selection/acquisition, environment, UID o fingerprint,
  lifecycle/cleanup digests y cleanup key; conocer un `RunID` no la sustituye.
- **Managed inventory:** listado paginado y provider-specific de recursos
  creados por FeatureProof dentro de un owner/cluster configurados.
- **Orphan:** recurso gestionado cuya metadata FeatureProof es íntegra pero cuyo
  lifecycle/run no existe de forma confirmada o ya no posee un owner vivo,
  después de un grace period explícito.
- **Quarantine:** estado no destructivo para inventario ambiguo, alterado o sin
  prueba suficiente; requiere nueva autoridad o intervención antes de retry.
- **Capacity reservation:** receipt durable que contabiliza un entorno y su
  demanda desde antes de Acquire hasta cleanup confirmado.
- **Capacity policy:** límites composition-owned por `ProjectID` para ambientes,
  CPU millicores, memoria bytes, objetos y TTL.
- **Logical exactly-once cleanup:** una identidad de cleanup produce una sola
  confirmación terminal; una llamada física puede repetirse tras respuesta
  incierta, pero siempre con la misma key y precondiciones.

## R1 — Identidad y estado durable suficientes

WHEN un entorno gestionado se reserva o se descubre, THE system SHALL persistir
una identidad canónica que ligue owner, proyecto, run, provider/version,
selection/acquisition, environment, UID o fingerprint, demanda, TTL, creación y
expiración antes de depender del proceso que inició el run.

### Acceptance criteria

- GIVEN un Acquire gestionado admitido WHEN comienza THEN el record contiene
  `ProjectID`, `RunID`, owner configurado, TTL/expiry, demand y acquisition key
  antes del primer provider I/O.
- GIVEN la misma identidad con campos en distinto orden WHEN se sella THEN
  produce el mismo digest; una mutación posterior no cambia el record.
- GIVEN UID, relation digest, TTL, expiry o provider identity ausente,
  inconsistente, oversized o alterado WHEN se carga THEN se clasifica integrity
  y no se intenta cleanup.
- GIVEN un `RunID` válido pero project, acquisition, environment UID/fingerprint,
  provider o digest divergente/incompleto WHEN se construye el cleanup binding
  THEN se rechaza antes de lifecycle/provider I/O.
- GIVEN restart con solo stores y composición actuales WHEN se recupera THEN no
  requiere memoria del proceso, prompt, evidencia ni credenciales persistidas.

## R2 — Intención de cleanup para todo resultado

WHEN un run finaliza correctamente, falla funcionalmente, termina por error
operativo, se cancela o su entorno expira, THE system SHALL persistir un cleanup
intent antes de ejecutar release y SHALL conservarlo hasta cleanup confirmado.

### Acceptance criteria

- GIVEN outcomes `passed`, `failed`, `inconclusive`, `error` o `cancelled` WHEN
  terminaliza el workflow THEN existe el mismo tipo de intent durable antes de
  provider I/O.
- GIVEN crash después del intent y antes/durante release WHEN otro proceso
  reconcilia THEN reutiliza identidad e idempotency key y no pierde el entorno.
- GIVEN fallo antes de conocer completamente el lifecycle WHEN Acquire pudo
  tener efecto THEN queda un intent/recovery item que primero reconcilia
  identidad y nunca asume ausencia.
- GIVEN cleanup confirmado WHEN hay redelivery del terminal THEN no se crea una
  segunda obligación ni se llama de nuevo salvo para confirmar una respuesta
  previamente incierta.

## R3 — Reconciliación independiente y periódica

WHEN el controlador ejecuta un ciclo, THE controller SHALL escanear de forma
acotada intents, lifecycle records, capacity receipts e inventarios registrados,
SHALL clasificar cada item por estado/expiración y SHALL reconciliar únicamente
trabajo elegible bajo una lease propia.

### Acceptance criteria

- GIVEN records activos no expirados WHEN se escanean THEN permanecen intactos;
  records terminales, cancelados, release-pending o expirados se vuelven
  elegibles.
- GIVEN restart del controlador WHEN ejecuta `RunOnce` THEN continúa desde
  estado durable sin cursor, timer o retry solo en memoria.
- GIVEN más items/páginas que los límites WHEN se alcanza el budget THEN termina
  el ciclo sin tratar el prefijo como inventario completo y continúa en ciclos
  posteriores mediante cursor durable.
- GIVEN `Run` periódico WHEN se cancela THEN deja de reclamar trabajo nuevo,
  propaga cancelación al I/O en curso y retorna sin goroutines/timers filtrados.

## R4 — Expiración y TTL fail-closed

WHEN `now >= expires_at` para un entorno gestionado íntegro, THE controller
SHALL crear o promover su cleanup intent aunque el run siga no terminal, y SHALL
NOT extender TTL ni aceptar una expiración superior a la policy sellada.

### Acceptance criteria

- GIVEN instante anterior a expiry WHEN se reconcilia THEN no se elimina;
  GIVEN instante exactamente igual o posterior THEN queda elegible.
- GIVEN TTL cero, negativo, overflow, superior al máximo o inconsistente con
  created/expiry WHEN se reserva o descubre THEN se rechaza/quarantina antes de
  provider cleanup.
- GIVEN lease viva de una operación lifecycle en el instante de expiry WHEN el
  controller compite THEN no roba ownership; el intent durable se procesa tras
  handoff/expiración segura.
- GIVEN reloj falso que avanza sobre el límite WHEN corren ciclos repetidos THEN
  se observa una sola confirmación lógica de cleanup.

## R5 — Detección segura de huérfanos

WHERE un provider gestionado soporta inventario, THE controller SHALL descubrir
solo recursos con owner FeatureProof configurado, validar toda su metadata y
confirmar ausencia/inactividad en stores autoritativos antes de persistir un
orphan receipt y solicitar cleanup.

### Acceptance criteria

- GIVEN recurso con owner, run, proyecto, provider, acquisition digest, UID,
  created/expiry y TTL exactos, sin lifecycle/run confirmado y fuera del grace
  period WHEN se descubre THEN se persiste receipt e intent antes de delete.
- GIVEN metadata incompleta/alterada, owner distinto, UID vacío, store
  inaccesible o ausencia no concluyente WHEN se descubre THEN queda quarantine
  o se omite y no se llama a cleanup.
- GIVEN orphan ya ausente WHEN se reconcilia THEN se confirma de forma
  idempotente y libera su capacidad; nombre reutilizado con UID distinto nunca
  se elimina.
- GIVEN provider sin inventario registrado WHEN corre el controller THEN no usa
  listados genéricos, no amplía permisos y solo procesa records durables
  conocidos.

## R6 — Cleanup provider-neutral e idempotente

WHEN un item elegible se limpia, THE controller SHALL usar un puerto
provider-neutral estrecho con un lifecycle cleanup binding sellado, la misma
cleanup key y una precondición de identidad exacta, SHALL confirmar ausencia
mediante el provider y SHALL NOT eliminar recursos cuya pertenencia sea ambigua
ni aceptar un `RunID` aislado como autoridad.

### Acceptance criteria

- GIVEN lifecycle conocido WHEN se limpia THEN delega al release durable de la
  feature 31 presentando project, run, provider/version, selection/acquisition,
  environment UID/fingerprint, lifecycle/cleanup digests y key exactos; GIVEN
  orphan receipt THEN usa únicamente el recovery cleanup del provider registrado
  para esa identidad.
- GIVEN `RunID` existente con project, acquisition, UID/fingerprint o cleanup
  key divergentes, o binding incompleto WHEN se llama Status/Reconcile/Release
  THEN devuelve unauthorized/integrity antes de I/O destructivo.
- GIVEN redelivery con el binding exacto WHEN se reconcilia THEN converge sobre
  el mismo lifecycle y nunca resuelve autoridad nueva a partir del `RunID`.
- GIVEN timeout o respuesta perdida después del efecto WHEN se reintenta THEN la
  misma key/UID converge a confirmed absent sin crear otra identidad.
- GIVEN provider/version desconocido, key/input conflict, owner/UID/fingerprint
  drift o permiso denegado WHEN ocurre THEN falla cerrado sin fallback.
- GIVEN namespace Kubernetes exacto WHEN se elimina THEN usa precondición UID y
  no retira finalizers, parchea metadata ni borra por selector.

## R7 — Retries, backoff y clasificación acotados

IF cleanup devuelve un error, THEN THE controller SHALL clasificarlo mediante
categorías cerradas, reintentar solo outcomes transitorios/idempotentes dentro de
budgets por ciclo y persistir attempts, categoría y `next_attempt_at` antes de
ceder ownership.

### Acceptance criteria

- GIVEN transient, throttling, timeout o resultado incierto WHEN queda budget
  THEN usa backoff acotado con clock/waiter inyectados; otro ciclo puede continuar.
- GIVEN invalid, unauthorized, integrity o conflict permanente WHEN ocurre THEN
  no hace retry automático destructivo y deja estado blocked/quarantine visible.
- GIVEN espera que supera deadline, cumulative-wait o cycle budget WHEN se
  calcula THEN no comienza y el item permanece pendiente.
- GIVEN cancelación durante provider I/O o espera WHEN ocurre THEN conserva el
  intent durable y permite adopción posterior sin comparar textos de error.

## R8 — Concurrencia, leases y handoff

WHEN dos controladores observan el mismo item, THE store SHALL conceder una sola
lease viva mediante CAS/fencing, y cada transición SHALL demostrar owner,
versión y lease vigente sin mantener locks durante provider I/O o espera.

### Acceptance criteria

- GIVEN dos contenders simultáneos WHEN reclaman THEN solo uno inicia provider
  I/O mientras su lease permanece viva.
- GIVEN owner detenido y lease expirada WHEN otro controller reclama THEN
  continúa con el mismo cleanup binding, key y estado durable.
- GIVEN respuesta confirmada pero CAS terminal perdido WHEN recovery ejecuta
  THEN reconcilia ausencia y publica una sola confirmación lógica.
- GIVEN runs/proyectos distintos bajo `-race` WHEN se procesan THEN progresan
  independientemente, sin deadlock, data race ni pérdida de item.

## R9 — Cuotas antes de adquirir capacidad

WHEN un run gestionado solicita capacidad, THE lifecycle SHALL reservar
atómicamente contra la policy sellada de su `ProjectID` antes de llamar a
Acquire, contando ambientes simultáneos y demanda agregada de CPU, memoria y
objetos, y SHALL rechazar TTL superior al máximo.

### Acceptance criteria

- GIVEN demanda dentro de límites WHEN se reserva THEN existe un receipt durable
  antes del primer provider call y redelivery reutiliza el mismo receipt.
- GIVEN el nuevo total supera environments, CPU, memory, objects o max TTL WHEN
  se evalúa THEN se deniega antes de provider I/O sin partial reservation.
- GIVEN dos adquisiciones concurrentes que juntas exceden una dimensión WHEN
  compiten THEN como máximo una reserva se confirma mediante CAS.
- GIVEN cleanup pendiente/incierto u orphan con demanda desconocida WHEN se
  calcula capacidad THEN no se libera ni infravalora; demanda desconocida
  bloquea nueva capacidad según la policy fail-closed.

## R10 — Liberación de cuota ligada a cleanup confirmado

WHEN un entorno se confirma ausente, THE capacity ledger SHALL liberar su
reservation exactamente una vez; terminalización, cancelación, timeout o un
simple delete solicitado SHALL NOT liberar capacidad antes de esa confirmación.

### Acceptance criteria

- GIVEN release confirmado/already absent con identidad exacta WHEN se persiste
  THEN el aggregate disminuye una vez y redelivery no lo vuelve negativo.
- GIVEN cleanup pending, blocked, cancelled o response unknown WHEN se consulta
  THEN la reserva sigue consumiendo todas sus dimensiones.
- GIVEN crash entre confirmación provider y release de cuota WHEN se recupera
  THEN reconcilia y converge sin doble decremento.
- GIVEN receipt corrupto, cross-project o con demand distinta bajo la misma key
  WHEN se carga THEN falla integrity y no modifica aggregates.

## R11 — Separación del veredicto funcional

IF cleanup falla, se retrasa o queda blocked, THEN THE workflow/report SHALL
conservar el outcome funcional ya confirmado y SHALL proyectar el estado de
cleanup como dimensión operacional separada.

### Acceptance criteria

- GIVEN verdict `passed` o `failed` y cleanup pending WHEN se consulta THEN el
  verdict y evidence IDs no cambian y `cleanup_status` explica la incidencia.
- GIVEN error de cleanup antes de terminalización completa WHEN se recupera THEN
  el desired outcome durable sigue siendo el mismo.
- GIVEN retry posterior exitoso WHEN confirma ausencia THEN solo cambia cleanup
  y capacity status; no reconstruye assertions, package ni report funcional.
- GIVEN texto de provider/modelo que propone un verdict WHEN se procesa THEN no
  controla cleanup ni resultado funcional.

## R12 — Cancelación y shutdown recuperables

IF el contexto del controller se cancela o su shutdown comienza, THEN THE
controller SHALL dejar de reclamar items, cancelar waits/provider calls, no
iniciar efectos nuevos y dejar suficiente estado durable para que otro owner
continúe después de la lease.

### Acceptance criteria

- GIVEN cancelación antes de scan/claim/I/O/save WHEN ocurre THEN cada frontera
  observa el contexto y no comienza la siguiente.
- GIVEN cancelación durante un cleanup incierto THEN el intent ya existe y no se
  marca confirmed; un controller posterior reconcilia.
- GIVEN loop periódico detenido WHEN retorna THEN todos los timers/goroutines que
  posee han terminado y pueden esperarse.
- GIVEN shutdown mientras otro item tiene lease viva WHEN ocurre THEN no espera
  indefinidamente ni roba/libera ownership ajeno.

## R13 — Auditoría y estado visible content-free

WHEN cambia cleanup, orphan o capacity state, THE system SHALL persistir un
evento append-only exactly-once con IDs/digests, owner, proyecto, fase,
attempt, categoría y tiempos allowlisted, sin handles raw, nombres de recursos,
endpoints, secretos ni mensajes remotos.

### Acceptance criteria

- GIVEN discover, intent, claim, retry, blocked, confirmed, quota reserve/deny o
  release WHEN se confirma THEN existe un evento estable y reabrible.
- GIVEN observer/log sink lento, fallido o cancelado WHEN ocurre THEN no cambia
  la autoridad, lease, intent, quota ni verdict.
- GIVEN redelivery del mismo evento WHEN se publica THEN bytes iguales convergen
  y contenido distinto bajo la misma identidad produce conflict.
- GIVEN canarios en provider error/metadata/handle WHEN se escanean records,
  events, logs, status y report THEN no aparecen.

## R14 — Composición local sustituible sin adelantar el control plane

THE implementation SHALL ejecutar sobre stores/loops locales mediante puertos
consumer-owned y composición explícita, y SHALL NOT introducir PostgreSQL,
colas, workers remotos, leader election de red ni mezclar backends dentro de un
mismo controller run.

### Acceptance criteria

- GIVEN composición local WHEN arranca THEN recibe store, providers, inventory,
  policies, clock, waiter, owner y límites explícitos; no hay defaults ocultos.
- GIVEN dependencia, policy o límite obligatorio ausente/inválido WHEN se
  construye THEN falla antes de scan, quota o provider I/O.
- GIVEN inspección de contratos WHEN se revisa THEN un futuro adapter PostgreSQL
  puede implementar los ports sin importar SDK/SQL al core.
- GIVEN un mismo controller run WHEN se ejecuta THEN usa una sola instancia de
  stores autoritativos y no realiza dual-write local/PostgreSQL.

## R15 — Suite hermética y verificación

THE implementation SHALL disponer de tests deterministas con reloj, waiter,
provider, inventory y store falsos que cubran TTL, restart, outcomes terminales,
cancelación, orphan, retry, ownership, cuotas y convergencia, manteniendo
trazabilidad y al menos 85% statement coverage del código nuevo/modificado sin
redondear.

### Acceptance criteria

- GIVEN `go test ./...` WHEN se ejecuta THEN no contacta Kubernetes, PostgreSQL,
  cloud, DNS, provider, modelo ni red externa reales.
- GIVEN tests concurrentes WHEN coordinan interleavings THEN usan canales/reloj
  falso y no `time.Sleep`.
- GIVEN todo símbolo nuevo/modificado WHEN se audita THEN entidades, constantes,
  variables, funciones, métodos y helpers de test tienen comentario de
  responsabilidad sistémica.
- GIVEN cierre de la feature WHEN se verifica THEN pasan gofmt, diff-check, vet,
  compile-only, suite global, race global/focal repetido, coverage y builds
  aplicables con mapa `R1–R15 → test` real.

## Casos límite y errores

- Un resource name, namespace, selector o provider elegido por caller/modelo no
  entra en inventory ni cleanup requests.
- Una lease expira solo al alcanzar exactamente su instante; no se compara
  resourceVersion como número ni tiempo con reloj del provider no atestado.
- Items con mismo run pero acquisition/UID distintos son conflict, nunca una
  redelivery equivalente.
- Un record `released` con capacity receipt activo se repara liberando cuota;
  un receipt released con entorno presente se marca integrity y no se borra sin
  nueva prueba.
- Paginación con token repetido, página vacía con continuation o cambio de
  snapshot termina incomplete y no autoriza ausencia/orphan.
- Overflow de attempts, durations, CPU, memoria, objetos, timestamps o sumas de
  quota falla cerrado.
- Un namespace `Terminating` no se fuerza; permanece pending hasta NotFound o un
  error permanente visible.
- Finalizers ajenos, Forbidden y ownership drift no se sortean con permisos o
  operaciones alternativas.
- La ausencia de run/environment solo cuenta cuando el store autoritativo
  devuelve NotFound confirmado; timeout/error no es ausencia.

## Decisiones abiertas

No quedan decisiones de producto necesarias. En esta entrega “equipo” se
materializa mediante el `ProjectID` sellado disponible; la jerarquía de tenant
se añadirá en la feature 37. Las dimensiones y valores concretos de policy,
periodo, grace, páginas, attempts, timeouts y backoff son configuración
composition-owned obligatoria, no defaults del controller.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-26T15:47:25+01:00
```

La implementación no puede comenzar hasta aprobación humana explícita y cambio
del estado a `in_progress` por el leader.
