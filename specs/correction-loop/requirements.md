# Bucle verificable de corrección — Requirements

## Objetivo y problema

Permitir que un agente externo presente una revisión nueva como corrección de
un run con fallos funcionales y obtenga una revalidación comparable, sin
alterar el run padre ni confiar en resultados antiguos. El sistema debe
comprobar primero los escenarios que fallaron, completar después la regresión
exigida para la revisión nueva y producir una comparación determinista entre
paquetes íntegros.

## Alcance

- Admitir una corrección desde la interfaz externa mediante selecciones locales
  autorizadas e idempotencia durable.
- Crear una cadena lineal de runs con identidad padre, raíz e intento.
- Vincular cada hijo a un commit nuevo, resoluble y atestado en el mismo
  repositorio y entorno autorizados.
- Derivar de forma determinista los escenarios fallidos que motivan la
  corrección y comprobar su aplicabilidad en el contrato actual.
- Ejecutar en orden una fase `failed_first` y una fase `required_regression`.
- Comparar paquetes verificados y clasificar escenarios corregidos,
  persistentes, nuevos, no aplicables o inconclusive.
- Aplicar límites acumulados de intentos, duración, llamadas y tokens de modelo
  a toda la cadena, y una puerta humana cuando la política lo requiera.
- Distinguir resultados funcionales de fallos de infraestructura o de una
  revisión todavía no desplegada.

## Fuera de alcance

- Modificar código, crear commits, hacer push, fusionar ramas o elegir una
  corrección por cuenta del modelo.
- Crear, modificar, destruir o desplegar entornos preview. La conexión y la
  atestación productivas pertenecen a la feature 19.
- Inferir una revisión desde una URL, ruta, rama o texto libre aportado por el
  agente; la composición resuelve aliases autorizados.
- Sustituir la aprobación de cada plan o replan definida por las features 12 y
  13, ni relajar registry, policy, assertions, redacción o presupuestos.
- Publicar en GitHub; esa superficie pertenece a la feature 18.
- Usar precios, monedas o presupuestos monetarios.
- Heredar evidencias, assertions o aprobaciones entre revisiones.

## Actores y términos

- **Agente externo:** principal autenticado que presenta una selección de
  corrección autorizada y consulta su progreso; no ejecuta herramientas.
- **Run padre:** run terminal `failed` cuyo paquete íntegro aporta los fallos
  que motivan el siguiente intento.
- **Run hijo:** run nuevo e inmutable creado para la revisión corregida. Tiene
  un único padre directo, una raíz de cadena y un ordinal de intento.
- **Cadena de corrección:** sucesión lineal formada por el run raíz y sus hijos.
  Un nuevo hijo solo puede partir del tail actual y no se crean ramas.
- **Escenario comparable:** identidad estructural formada por constraint,
  scenario, check, step y assertion estables, no por posición ni texto libre.
- **Escenario aplicable:** escenario padre que conserva constraint y semántica
  resoluble en la especificación, contrato y catálogo actuales.
- **Regresión requerida:** conjunto cerrado de escenarios de la revisión nueva
  fijado por una política confiable de composición; no lo amplía ni reduce el
  agente.
- **Intento:** admisión confirmada de un hijo. El run raíz no consume el límite
  de intentos de corrección.
- **Fallo funcional:** assertion determinista `failed` en un paquete válido.
- **Fallo operativo:** error de infraestructura, binding, atestación,
  persistencia o integridad; no se atribuye al comportamiento del código.

## Requirements EARS

### R1 — Admisión externa autorizada e idempotente

WHEN un principal autorizado solicita corregir un run mediante su handle, un
alias local de corrección y una clave de idempotencia, THE correction service
SHALL reautorizar el principal y ambos recursos, verificar un paquete padre
terminal `failed`, reservar durablemente una única solicitud y SHALL NOT
aceptar rutas, URLs, commits, targets, credenciales, políticas o límites libres
en la petición.

#### Acceptance criteria

- GIVEN un run propio `failed` y un alias permitido
  WHEN se presenta una corrección
  THEN se reserva exactamente una identidad hija y se devuelve un handle
  opaco consultable.
- GIVEN un handle ajeno, desconocido, caducado, no terminal, `passed`,
  `inconclusive`, `cancelled` o `error`
  WHEN se solicita corregirlo
  THEN no se revela su existencia ni se crea un hijo.
- GIVEN dos peticiones iguales con el mismo principal, clave y bytes canónicos
  WHEN se reciben en serie, en concurrencia o tras reinicio
  THEN ambas reconstruyen el mismo hijo; reutilizar la clave con contenido
  distinto devuelve conflicto.

### R2 — Identidad padre/hijo inmutable y cadena lineal

WHEN una corrección queda admitida, THE system SHALL crear un run y binding
nuevos que registren el run padre directo, la raíz, el ordinal monotónico, el
binding padre y la selección de política, SHALL preserve el paquete padre
byte-for-byte y SHALL allow solo un hijo confirmado por tail de cadena.

#### Acceptance criteria

- GIVEN un padre A en el intento 0
  WHEN se admite B y después C
  THEN B declara `parent=A, root=A, attempt=1`, C declara
  `parent=B, root=A, attempt=2`, y cada run tiene ID y binding propios.
- GIVEN escritura, comparación o cancelación del hijo
  WHEN se vuelven a leer manifest y artefactos del padre
  THEN sus bytes y digests no han cambiado.
- GIVEN dos solicitudes diferentes contra el mismo tail
  WHEN compiten
  THEN una sola reserva el siguiente ordinal y la otra obtiene conflicto; una
  solicitud posterior contra un ancestro que ya no es tail también falla.

### R3 — Revisión nueva compatible y desplegada

WHEN se prepara el hijo, THE system SHALL resolver y atestar su selección con
`revisionbinding`, SHALL require un commit completo distinto del padre en el
mismo repositorio y entorno autorizados, y SHALL stop before model, policy or
tool work if the revision is unresolved, dirty, incompatible, unattested,
expired or not deployed.

#### Acceptance criteria

- GIVEN el mismo commit que el padre, otro repositorio u otro entorno fuera
  del scope autorizado
  WHEN se ejecuta preflight
  THEN la corrección se rechaza operacionalmente y no se inicia validación.
- GIVEN un commit nuevo resoluble pero la atestación aún indica el commit padre
  WHEN se ejecuta preflight
  THEN el intento termina o permanece bloqueado con categoría
  `revision_not_deployed`, sin assertion funcional nueva.
- GIVEN commit, repositorio y entorno compatibles y atestados
  WHEN se sella el hijo
  THEN su binding referencia exclusivamente la revisión nueva y nunca reutiliza
  hashes, plan bindings ni aprobación del padre.

### R4 — Selección determinista de fallos y aplicabilidad

WHEN el paquete padre se acepta, THE system SHALL derive the motivating set
from every deterministic failed assertion in that package, deduplicate it by
stable scenario identity, and classify each item against the child's validated
constraints, retained contract and authorized catalog as `applicable` or
`not_applicable` before planning execution.

#### Acceptance criteria

- GIVEN varios fallos en una o más restricciones
  WHEN se construye la selección
  THEN todos aparecen en orden canónico y el agente no puede omitir, añadir ni
  reordenar elementos mediante la petición.
- GIVEN el mismo escenario todavía resoluble con la misma semántica relevante
  WHEN se comprueba aplicabilidad
  THEN entra en `failed_first`.
- GIVEN una restricción eliminada o una operación, capability, assertion o
  schema incompatible con la revisión nueva
  WHEN se comprueba aplicabilidad
  THEN se registra `not_applicable` con categoría estructural y no se inventa
  un pass, failure ni evidencia.

### R5 — Orden failed-first y regresión completa

WHEN el preflight y cualquier aprobación requerida se completan, THE
correction coordinator SHALL execute every applicable motivating scenario in
the `failed_first` phase before admitting `required_regression`, and SHALL then
cover every scenario required by the trusted regression-policy snapshot for
the child through the ordinary validation pipeline.

#### Acceptance criteria

- GIVEN fallos aplicables F1 y F2 y regresiones R1 y R2
  WHEN el hijo se ejecuta
  THEN ninguna operación de regresión empieza antes de terminar la fase de
  fallos y el journal conserva el orden de fase y escenario.
- GIVEN un escenario failed-first también pertenece a la regresión con huella
  semántica idéntica
  WHEN empieza la segunda fase
  THEN su evidencia confirmada en el mismo hijo satisface esa entrada sin una
  ejecución duplicada; una semántica distinta exige otra comprobación.
- GIVEN un escenario previamente passed o ausente
  WHEN la política lo exige para regresión
  THEN se vuelve a comprobar en el hijo; ningún resultado ni evidencia del
  padre lo satisface.
- GIVEN schema, registry, policy, budget o aprobación deniega un escenario
  WHEN se procesa una fase
  THEN no se salta la puerta y el resultado queda `inconclusive` u operativo
  según el contrato existente, nunca passed por herencia.

### R6 — Comparación determinista entre paquetes verificados

WHEN el hijo alcanza un resultado funcional publicable, THE comparator SHALL
read parent and child only through the verified package reader, match stable
scenario identities, and emit an ordered comparison that classifies each
entry exactly once as `corrected`, `persistent`, `new`, `not_applicable` or
`inconclusive`, with references to evidence from its run of origin.

#### Acceptance criteria

- GIVEN un fallo padre cuyo escenario pasa en el hijo
  WHEN se compara
  THEN aparece como `corrected` con referencias separadas a padre e hijo.
- GIVEN un fallo padre que vuelve a fallar
  WHEN se compara
  THEN aparece como `persistent`; GIVEN un escenario que no falló en el padre
  y falla en el hijo THEN aparece como `new`.
- GIVEN un fallo padre clasificado no aplicable en R4
  WHEN se compara
  THEN aparece como `not_applicable`, aunque el veredicto agregado se calcule
  únicamente con resultados actuales.
- GIVEN evidencia actual insuficiente o resultado actual inconclusive para un
  escenario comparable
  WHEN se compara
  THEN aparece como `inconclusive`; posiciones, texto del modelo y diagnóstico
  libre no afectan la clasificación.
- GIVEN paquete padre o hijo corrupto, foreign o con binding/lineage
  inconsistente
  WHEN se compara
  THEN no se devuelve comparación parcialmente confiable.

### R7 — Veredicto del hijo sin herencia

WHEN finaliza la validación del hijo, THE system SHALL calculate its verdict
only from assertions and evidences confirmed for the child under the existing
deterministic rules; THE comparison SHALL describe change but SHALL NOT
determine or override that verdict.

#### Acceptance criteria

- GIVEN todos los fallos padres corregidos pero una regresión nueva
  WHEN el hijo termina
  THEN el hijo es `failed` y la comparación contiene `corrected` y `new`.
- GIVEN un fallo padre `not_applicable` y todas las restricciones actuales
  requeridas pasan
  WHEN el hijo termina
  THEN puede ser `passed` sin presentar el escenario retirado como evidencia.
- GIVEN falta evidencia actual obligatoria
  WHEN se agrega el hijo
  THEN es `inconclusive` aunque el padre hubiera pasado esa comprobación.

### R8 — Límites acumulados y aprobación de continuación

THE correction chain SHALL own positive or zero limits for correction
attempts, total elapsed duration, model calls and confirmed model tokens,
SHALL reserve each required unit atomically before new work, and WHERE trusted
policy requires continuation approval SHALL wait for a one-use authorization
bound to parent, child selection, revision, motivating set, regression policy,
budget snapshot and attempt ordinal before starting planning or tools.

#### Acceptance criteria

- GIVEN cualquier límite agotado
  WHEN se solicita o reanuda el siguiente intento
  THEN no empiezan resolución que consuma el intento, modelo, aprobación de
  plan ni herramientas, y se devuelve `limit_exhausted` con dimensión segura.
- GIVEN una llamada de modelo confirmada por un hijo
  WHEN termina
  THEN sus llamadas y tokens se suman a la cadena sin sustituir los límites
  propios del run; no existe campo monetario.
- GIVEN aprobación de continuación obligatoria ausente, rechazada, consumida o
  stale
  WHEN se intenta avanzar
  THEN no empieza trabajo funcional y ninguna aprobación del padre o de un
  plan anterior se reutiliza.
- GIVEN dos reservas concurrentes de la última unidad
  WHEN compiten
  THEN a lo sumo una continúa y los contadores no desbordan ni recuperan
  unidades confirmadas.

### R9 — Infraestructura y revisión no desplegada no son fallos funcionales

IF revision resolution, environment attestation/readiness, model transport,
tool transport, storage, package publication or integrity fails, THEN THE
system SHALL stop the current iteration with a typed operational outcome,
preserve confirmed state, and SHALL NOT create `persistent` or `new` functional
failure classifications from that condition.

#### Acceptance criteria

- GIVEN timeout o indisponibilidad del entorno antes o durante una fase
  WHEN se detiene el intento
  THEN el hijo termina `inconclusive` o `error` según la categoría existente y
  el código no recibe un failed funcional.
- GIVEN la revisión no está desplegada
  WHEN falla la atestación
  THEN se informa `revision_not_deployed` y no se compara como fallo
  persistente.
- GIVEN algunas assertions funcionales ya fueron confirmadas antes de un fallo
  operativo
  WHEN se recupera el intento
  THEN permanecen auditables, pero no se publica una comparación completa ni
  un `passed`.

### R10 — Cancelación, concurrencia y recuperación

WHEN a correction is cancelled or loses its owner, THE coordinator SHALL
propagate cancellation to the active child, join its owned work, persist one
terminal transition and preserve parent/confirmed child artifacts; state
changes SHALL use per-chain compare-and-swap without holding locks across Git,
attestation, approval, model, tool or filesystem I/O.

#### Acceptance criteria

- GIVEN cancelación durante espera de aprobación, failed-first, regresión o
  publicación
  WHEN se observa
  THEN no comienza trabajo posterior, las operaciones activas reciben el
  contexto cancelado y el parent permanece intacto.
- GIVEN retry de la misma cancelación
  WHEN la primera quedó confirmada
  THEN se reconstruye el mismo resultado sin transición duplicada.
- GIVEN pérdida del proceso propietario
  WHEN otro proceso recupera el registro
  THEN marca el intento interrumpido de forma segura y no lo reejecuta ni crea
  otro hijo automáticamente.
- GIVEN cadenas distintas
  WHEN progresan concurrentemente
  THEN no se bloquean entre sí por callbacks externos y no hay carreras de
  estado compartido.

### R11 — Proyección externa segura y versionada

WHEN CLI or MCP exposes correction capabilities, submission, status or
comparison, THE interface SHALL use one shared versioned application contract,
opaque authorized handles and bounded safe fields, SHALL reauthorize every
operation, and SHALL NOT expose paths, targets, commits outside the authorized
result, raw evidence, secrets, provider payloads or arbitrary errors.

#### Acceptance criteria

- GIVEN una misma operación y fakes
  WHEN se invoca por CLI y MCP
  THEN los envelopes de aplicación, estados, errores y comparación son
  semánticamente equivalentes.
- GIVEN un principal sin scope de corrección o lectura
  WHEN descubre o invoca las herramientas
  THEN la capacidad se omite o se rechaza sin revelar handles ajenos.
- GIVEN secretos centinela y texto hostil en paquetes o adapters
  WHEN se serializan respuestas, eventos y logs
  THEN ningún byte secreto o texto no allowlisted cruza la frontera.
- GIVEN un cliente del contrato anterior
  WHEN consulta capacidades
  THEN las operaciones previas conservan su semántica y la nueva versión
  anuncia explícitamente las operaciones de corrección soportadas.

### R12 — Verificación hermética y comentarios sistémicos

THE feature SHALL be verified with deterministic fake package reader,
revision/environment boundaries, policy, approval, child executor, model
accounting, registry, clock and ID sources, without network, real provider,
credentials or preview environment; every introduced or modified entity,
function and test helper SHALL include a comment describing its responsibility
in the system.

#### Acceptance criteria

- GIVEN ordinary repository verification
  WHEN feature tests run
  THEN successful correction, persistent failure, new regression, same commit,
  obsolete scenario, exhausted limit, approval, infrastructure failure,
  cancellation, idempotent retry and concurrent admission are covered.
- GIVEN R1–R11
  WHEN traceability is reviewed
  THEN each requirement maps to at least one named test, affected code reaches
  at least 85% statement coverage and race verification passes.
- GIVEN new or modified exported, internal or test-only symbols
  WHEN source is reviewed
  THEN each has a systemic-responsibility comment rather than a mechanical
  syntax description.

## Errores y casos límite

- La identidad comparable se obtiene de IDs estructurales y huellas semánticas
  canónicas versionadas. IDs duplicados, vacíos o ambiguos invalidan la
  selección; no se emparejan por índice, descripción o diagnóstico.
- Un padre válido para una nueva corrección es exclusivamente el tail terminal
  `failed`. Un hijo `inconclusive`, `cancelled` o `error` puede reintentarse con
  la misma idempotency key para recuperar su resultado, pero no avanzar la
  cadena; reanudar tras resolver infraestructura requiere una nueva operación
  explícita sobre el último padre funcional fallido y queda sujeta a CAS y
  límites.
- La fase failed-first se considera completa cuando cada elemento aplicable
  tiene resultado actual confirmado o una causa actual inconclusive. Los no
  aplicables no consumen tool calls.
- La política de regresión es un snapshot versionado, no secreto y hasheado;
  contiene los escenarios requeridos de la revisión nueva. Una política
  ausente, ilegible o cambiante falla cerrada.
- El comparator ordena por constraint, scenario, check, step y assertion. Para
  una identidad padre failed: child passed → `corrected`; child failed →
  `persistent`; ausente por inaplicabilidad confirmada → `not_applicable`;
  cualquier evidencia insuficiente → `inconclusive`. Un child failed sin
  contraparte failed → `new`.
- Si un escenario failed-first y uno de regresión comparten huella idéntica,
  solo evidencia confirmada en el hijo puede deduplicarlos. Nunca se deduplica
  contra el padre.
- Cada idempotency key se particiona por principal, cadena y operación. Un
  replay no vuelve reutilizable una aprobación ni recupera presupuesto.
- La duración total se mide con reloj monotónico inyectado donde el proceso lo
  permita y se persisten instantes UTC para auditoría. Reiniciar no amplía el
  deadline confirmado.
- Los errores públicos usan categorías cerradas y campos allowlisted. Los
  textos de Git, entorno, modelo, herramienta y filesystem no se propagan.
- La finalización tras cancelación usa únicamente el plazo acotado ya definido
  por la composición para confirmar journal/publicación y después hace join.

## Decisiones abiertas

Ninguna para esta entrega. La composición productiva continúa fallando cerrada
mientras no exista el adapter confiable de entornos preview de la feature 19;
los caminos positivos se verifican con fakes y no inventan un proveedor.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-16T17:25:00+02:00
```
