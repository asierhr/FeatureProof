# Herramienta de validación operativa de Kubernetes — Requirements

## Objetivo y problema

FeatureProof ya puede adquirir un entorno efímero de Kubernetes y producir un
`ValidationTarget` atestado, pero sus planes todavía no pueden observar de forma
segura el estado operativo del namespace asociado. Esta feature incorpora una
herramienta de solo lectura que transforma un conjunto cerrado de comprobaciones
Kubernetes en resultados estructurados, acotados y aptos para evidencia.

La herramienta no concede al modelo una API genérica de Kubernetes. Composición
deriva namespace, UID, recursos, selectores y relaciones permitidas del target
atestado y de un catálogo sellado. El modelo solo selecciona una comprobación y
un alias lógico autorizados; el motor determinista de assertions, no el modelo
ni la herramienta, calcula cumplimiento y veredictos.

## Alcance

- Una tool registrada y read-only para un `ValidationTarget` Kubernetes vigente.
- Catálogo de inspección inmutable, composition-owned y ligado al target.
- Comprobaciones cerradas de Deployment, Pods, Jobs, Services,
  EndpointSlices, Events y conectividad declarada entre Services.
- Identidad Kubernetes de mínimo privilegio, sin lectura de Secrets ni verbos de
  mutación.
- Lecturas paginadas, cancelables y limitadas por tiempo, objetos y bytes.
- Resultados normalizados con versiones observadas, drift y truncación explícitos.
- Redacción antes de persistencia, assertions o contexto del modelo.
- Integración con evidencia y assertions deterministas existentes.
- Tests herméticos mediante una API Kubernetes falsa.

## Fuera de alcance

- Crear, reservar, reconciliar o eliminar entornos; corresponde a la feature 32.
- El controlador periódico de cleanup, TTL y huérfanos; corresponde a la
  feature 34.
- Workers, leases distribuidas o control plane persistente; corresponde a la
  feature 35.
- Configuración general por proyecto o tenant; corresponde a las features 36 y
  37.
- Una API Kubernetes genérica, manifests, JSONPath libre, field selectors,
  label selectors, nombres o namespaces proporcionados por el modelo.
- `watch`, logs, `exec`, `attach`, `port-forward`, proxy, shell, lectura de
  Secrets, métricas, mutaciones, escalado o probes activos desde Pods.
- Demostrar conectividad de red real. La comprobación de conectividad declarada
  verifica relaciones de Service, EndpointSlice y política allowlisted; no
  sustituye una prueba funcional HTTP.
- Permitir que la herramienta o el modelo emitan `passed`, `failed` o un
  veredicto final.

## Actores y términos

- **Composition:** frontera confiable que posee credenciales, target, binding,
  catálogo de inspección, límites y cliente Kubernetes.
- **Validation target:** `environment.ValidationTarget/v1` vigente de la
  feature 31; para Kubernetes, `EnvironmentID` identifica el namespace y
  `ReservationID` su UID atestado.
- **Inspection catalog:** conjunto sellado de recursos lógicos y relaciones que
  pueden observarse para un template/profile concretos.
- **Logical resource alias:** ID seguro que el plan puede seleccionar sin conocer
  el nombre Kubernetes ni el selector real.
- **Check kind:** operación cerrada: `deployment_availability`, `pod_restarts`,
  `job_status`, `service_endpoints`, `events` o `declared_connectivity`.
- **Observed version:** `resourceVersion` de cada objeto o lista cuando Kubernetes
  lo proporciona; no es un número que FeatureProof ordene o interprete.
- **Snapshot:** conjunto de observaciones de una llamada, coherente con una
  misma autoridad y con drift explícito; Kubernetes no ofrece una transacción
  multi-recurso.
- **Drift:** cambio de UID, resourceVersion, ownership, attestation o membresía
  relevante durante una comprobación compuesta.
- **Outcome category:** `observed`, `resource_absent`, `insufficient_evidence`,
  `access_denied`, `timeout`, `cancelled`, `transient_error` o
  `integrity_error`. No es un veredicto funcional.

## R1 — Autoridad ligada al entorno atestado

WHEN composición construye la herramienta, THE tool SHALL verificar el
`ValidationTarget` y su `Binding`, exigir provider Kubernetes y ligar de forma
inmutable namespace, UID, run, revisión, selección, lifecycle, catálogo y
vigencia antes de permitir una lectura.

### Acceptance criteria

- GIVEN target y binding válidos del run actual WHEN se compone THEN el
  namespace efectivo es exactamente `Target.EnvironmentID` y el UID esperado
  exactamente `Target.ReservationID`.
- GIVEN target expirado, alterado, de otro run/provider o binding incompatible
  WHEN se compone o revalida THEN no se llama al API server.
- GIVEN catálogo cuyo target/template/profile/digest no coincide WHEN se compone
  THEN la capacidad no se registra.
- GIVEN namespace con el mismo nombre pero UID distinto WHEN se inspecciona THEN
  se devuelve `integrity_error` sin observar sus recursos.

## R2 — Vocabulario cerrado sin queries arbitrarias

WHEN un plan solicita la tool, THE tool SHALL aceptar solo un `check_kind` y un
`resource_alias` o `relationship_alias` presentes en su catálogo sellado y
SHALL derive todos los nombres, namespaces, selectors y relaciones desde esa
autoridad, nunca desde texto libre del input.

### Acceptance criteria

- GIVEN input con exactamente `check_kind` y el alias requerido WHEN ambos están
  autorizados y son compatibles THEN la validación puede continuar.
- GIVEN `namespace`, nombre Kubernetes, label/field selector, query, URL,
  manifest, JSONPath o clave desconocida WHEN se valida THEN se rechaza antes
  del cliente.
- GIVEN alias ausente, desconocido o autorizado para otro check WHEN se valida
  THEN se rechaza con categoría y field path estables.
- GIVEN mapas o slices del catálogo mutados por el caller tras construcción WHEN
  se ejecuta THEN no cambian la autoridad de la tool.

## R3 — Deployment y réplicas preparadas

WHEN se ejecuta `deployment_availability`, THE tool SHALL observar el
Deployment autorizado y normalizar generación, generación observada, réplicas
deseadas, actualizadas, disponibles y condiciones necesarias sin incorporar
mensajes libres.

### Acceptance criteria

- GIVEN Deployment existente WHEN se observa THEN el resultado contiene los
  contadores, flags y resourceVersion en campos estables.
- GIVEN rollout incompleto o réplicas disponibles inferiores a las deseadas
  WHEN se evalúa una assertion declarada THEN puede producir incumplimiento
  determinista a partir de esos campos.
- GIVEN Deployment inexistente THEN el resultado es `resource_absent`, no una
  assertion fallida creada por la tool.
- GIVEN generation/resourceVersion cambiado durante la lectura compuesta THEN
  el snapshot se marca `insufficient_evidence` por drift.

## R4 — Reinicios y readiness de Pods

WHEN se ejecuta `pod_restarts`, THE tool SHALL listar únicamente Pods propiedad
del workload autorizado y normalizar por contenedor el restart count, readiness,
fase, termination flag y resourceVersion con orden determinista.

### Acceptance criteria

- GIVEN varios Pods y contenedores WHEN se observan THEN se ordenan por identidad
  lógica estable y se exponen conteos enteros no negativos.
- GIVEN Pod que no pertenece al owner/selector autorizado WHEN aparece en el
  namespace THEN no se incluye.
- GIVEN cero Pods coincidentes THEN se devuelve `resource_absent`.
- GIVEN lista parcial, objeto sin identidad/owner suficiente o truncación que
  impide evaluar todos los reinicios THEN se devuelve
  `insufficient_evidence`, nunca éxito implícito.

## R5 — Estado de Jobs

WHEN se ejecuta `job_status`, THE tool SHALL observar exclusivamente el Job
autorizado y normalizar generation, completions, succeeded, failed, active y
condiciones terminales sin leer logs ni Pods mediante `exec`.

### Acceptance criteria

- GIVEN Job completo, fallido o activo WHEN se observa THEN su estado queda
  representado por contadores y reasons allowlisted.
- GIVEN Job ausente THEN la categoría es `resource_absent`.
- GIVEN Job con UID/owner distinto del autorizado THEN la categoría es
  `integrity_error` y no se adopta por nombre.
- GIVEN mensaje de condición con un canario sensible THEN el canario no aparece
  en output, error, evento, evidencia ni contexto.

## R6 — Services y EndpointSlices

WHEN se ejecuta `service_endpoints`, THE tool SHALL observar el Service
autorizado y sus EndpointSlices asociados, normalizando puertos, cantidad de
endpoints ready/not-ready y targets permitidos sin exponer direcciones IP ni
nombres de Pods.

### Acceptance criteria

- GIVEN Service y EndpointSlices válidos WHEN se observan THEN el output indica
  tipo interno, puertos allowlisted, totales de endpoints y versiones.
- GIVEN EndpointSlice de otro Service, namespace u owner WHEN se lista THEN se
  excluye y el intento de mezclarla no amplía el resultado.
- GIVEN Service ausente THEN `resource_absent`; GIVEN Service presente sin
  EndpointSlice completa THEN `insufficient_evidence` o un estado observado con
  cero endpoints solo cuando la lista completa ha sido confirmada.
- GIVEN respuestas mayores a los límites THEN no se exponen items parciales como
  snapshot completo y la truncación queda explícita.

## R7 — Eventos acotados y no confiables

WHEN se ejecuta `events`, THE tool SHALL listar solo Events asociados a los UIDs
autorizados, proyectar tipo, reason allowlisted, count y tiempos, y SHALL NOT
retornar message, note, reportingInstance ni payload libre.

### Acceptance criteria

- GIVEN Events de recursos autorizados y ajenos WHEN se listan THEN solo los
  primeros se incluyen y quedan ordenados de forma determinista.
- GIVEN reason desconocido WHEN se proyecta THEN se usa `other`, sin copiar el
  texto remoto.
- GIVEN message/note con secretos o instrucciones WHEN se procesa THEN esos bytes
  no cruzan el adapter.
- GIVEN demasiados Events o páginas WHEN se alcanza un límite THEN el resultado
  declara truncación y cualquier assertion dependiente de exhaustividad queda
  inconclusa.

## R8 — Conectividad declarada

WHEN se ejecuta `declared_connectivity`, THE tool SHALL evaluar una relación
autorizada de origen, Service destino, puerto y protocolo contra el catálogo,
el Service/EndpointSlices observados y la política de red sellada, sin realizar
un probe activo ni aceptar destinos propuestos por el modelo.

### Acceptance criteria

- GIVEN relación catalogada, puerto permitido, Service presente y endpoint ready
  WHEN se observa THEN el output expresa que la conectividad está declarada y
  resoluble, no que una conexión real haya sido exitosa.
- GIVEN relación o puerto no allowlisted WHEN se valida THEN no se llama al
  cluster.
- GIVEN policy observada distinta de la policy sellada, Service sin endpoints o
  cambio concurrente THEN se distingue `integrity_error` o
  `insufficient_evidence` según exista contradicción o falta de snapshot.
- GIVEN una assertion sobre `declared` o `ready_endpoints` THEN el assertion
  engine calcula el resultado; la tool no emite verdict.

## R9 — Permisos Kubernetes mínimos y read-only

THE production composition SHALL usar una identidad namespaced con solo `get`
y `list` sobre Deployments, Pods, Jobs, Services, EndpointSlices, Events y las
NetworkPolicies necesarias, y SHALL NOT conceder lectura de Secrets ni verbos
de mutación o subrecursos interactivos.

### Acceptance criteria

- GIVEN el Role esperado WHEN se audita THEN no contiene `create`, `update`,
  `patch`, `delete`, `deletecollection`, `watch`, `bind`, `escalate`, `impersonate`
  ni acceso a `pods/exec`, `pods/attach`, `pods/portforward`, logs o Secrets.
- GIVEN Forbidden en cualquier lectura THEN el resultado es `access_denied`, no
  se reintenta como transitorio y no se intenta una operación alternativa.
- GIVEN un cliente fake que registra verbos/resources WHEN se ejecuta toda la
  suite THEN nunca registra una operación no allowlisted.
- GIVEN composición sin identidad explícita o con permisos ampliados detectables
  en su contrato THEN la tool no se registra.

## R10 — Paginación y límites fail-closed

THE tool SHALL limitar por configuración explícita timeout, páginas, items por
página, items totales, bytes normalizados y Events, y SHALL seguir tokens de
continuación solo dentro de la misma operación y snapshot autorizado.

### Acceptance criteria

- GIVEN varias páginas válidas WHEN quedan budgets THEN todas se agregan en orden
  determinista y el token nunca se expone al modelo.
- GIVEN token repetido, página vacía con token, token inesperado, más páginas,
  items o bytes que el máximo WHEN ocurre THEN termina como
  `insufficient_evidence` con truncación estructurada.
- GIVEN límite exacto WHEN se alcanza sin continuation THEN el snapshot es
  completo; un item adicional lo vuelve truncado.
- GIVEN configuración cero, negativa, incoherente o con defaults implícitos WHEN
  se construye THEN falla antes de registrar la tool.

## R11 — Snapshot, versiones y drift

WHILE una comprobación compuesta lee varios objetos o páginas, THE tool SHALL
registrar resourceVersions disponibles, verificar de nuevo el namespace y los
anchors relevantes, y SHALL representar cualquier falta de coherencia como
evidencia insuficiente o error de integridad, sin ordenar resourceVersions.

### Acceptance criteria

- GIVEN snapshot estable WHEN termina THEN el output contiene una versión por
  recurso/lista y un digest canónico del conjunto observado.
- GIVEN cambio de namespace UID, target/attestation expirada o resource UID
  autorizado reemplazado THEN `integrity_error`.
- GIVEN resourceVersion o membresía cambia entre anchor inicial y final sin
  sustitución de autoridad THEN `insufficient_evidence` con `drift=true`.
- GIVEN resourceVersion ausente en un fake/API compatible WHEN no es necesario
  para autoridad THEN se registra como no disponible y no se inventa.

## R12 — Cancelación, timeout y retries seguros

IF el contexto se cancela o el timeout de la tool vence, THEN THE tool SHALL
detener la llamada o paginación en curso, no iniciar la siguiente lectura y
clasificar la terminación sin publicar un snapshot parcial como completo.

### Acceptance criteria

- GIVEN cancelación antes o durante cada operación WHEN ocurre THEN el cliente
  recibe el contexto cancelado y no hay goroutines ni continuaciones filtradas.
- GIVEN timeout hijo con padre activo THEN `timeout`; GIVEN padre cancelado THEN
  prevalece `cancelled`/la causa del padre.
- GIVEN error transitorio y ToolSpec read-only/idempotente WHEN el executor
  reintenta dentro de su budget THEN cada intento realiza un snapshot nuevo y
  conserva su propia evidencia/versiones.
- GIVEN `access_denied`, `resource_absent`, input inválido o integrity error THEN
  la tool no los marca como transitorios.

## R13 — Normalización, truncación y redacción previa

WHEN Kubernetes devuelve datos, THE tool SHALL proyectar solo campos
allowlisted, ordenar colecciones, aplicar límites y redacción antes de devolver
el `ToolResult`; THE executor SHALL volver a aplicar la policy de evidencia
antes de persistencia o contexto.

### Acceptance criteria

- GIVEN el mismo snapshot en distinto orden WHEN se ejecuta THEN produce el
  mismo documento y digest normalizados.
- GIVEN Secrets existentes en el namespace WHEN se ejecuta cualquier check THEN
  no se listan, leen ni infieren sus nombres o valores.
- GIVEN canarios en labels, annotations, messages, object names, addresses o
  errores remotos WHEN se escanean outputs, errors, journal, evidencias y
  contexto THEN no aparecen.
- GIVEN truncación del adapter o sanitizer WHEN una assertion depende de la
  parte omitida THEN resulta `inconclusive` con causa estructurada.

## R14 — Evidencia y assertions sin verdict del modelo

WHEN una ejecución retorna un documento normalizado, THE plan executor SHALL
persistirlo mediante el flujo de evidencia existente y THE assertion engine
SHALL evaluar condiciones declaradas sobre paths estables, distinguiendo
incumplimiento confirmado de evidencia insuficiente, ausencia y acceso denegado.

### Acceptance criteria

- GIVEN `outcome.category=observed` y evidencia completa WHEN una condición es
  falsa THEN la assertion puede ser `failed` con evidence ID real.
- GIVEN `insufficient_evidence`, truncación o drift WHEN se evalúa una condición
  que requiere exhaustividad THEN la assertion/step es `inconclusive`, no
  `failed`.
- GIVEN `resource_absent` confirmado WHEN el plan declara que el recurso debe
  existir THEN una assertion explícita sobre la categoría puede fallar; sin esa
  declaración la tool no decide incumplimiento.
- GIVEN `access_denied`, timeout o error operativo WHEN termina el intento THEN
  el step queda inconclusive con causa separada y no se convierte en fallo
  funcional.
- GIVEN output o texto del modelo que afirma un verdict WHEN se procesa THEN no
  sustituye los estados calculados por assertions/evidence.

## R15 — Registro, observabilidad y tests herméticos

THE implementation SHALL registrar una única capacidad con metadata read-only,
emitir solo categorías/IDs/digests/conteos seguros y disponer de tests sin
cluster, red, credenciales ni reloj real que cubran todos los recursos y
fronteras de seguridad.

### Acceptance criteria

- GIVEN la ToolSpec WHEN se inspecciona THEN tiene nombre/version estable,
  `Effects=false`, `Idempotent=true` y validator asociado.
- GIVEN eventos/errores WHEN se inspeccionan THEN no incluyen manifests, names,
  selectors, namespace, IPs, event messages, credenciales ni payload remoto.
- GIVEN el fake API server WHEN se ejecutan tests THEN cubre Deployment, Pods,
  Job, Service/EndpointSlice, Events, NetworkPolicy, aislamiento de namespace,
  Forbidden, paginación, drift, truncación, timeout y cancelación.
- GIVEN `go test ./...` WHEN se ejecuta THEN no contacta un cluster o red real.
- GIVEN todo símbolo nuevo o modificado WHEN se audita THEN cada entidad,
  constante, variable, función, método y helper de test tiene comentario de su
  responsabilidad sistémica.
- GIVEN el diff productivo WHEN se verifica THEN supera formato, vet, tests,
  race y al menos 85 % statement coverage del código nuevo/modificado.

## Casos límite y errores

- Un namespace `Terminating`, ausente o recreado no se inspecciona como si fuese
  el entorno atestado.
- `NotFound` del recurso exacto se conserva como `resource_absent`; `NotFound`
  del namespace invalida la autoridad del snapshot.
- `Forbidden` nunca se degrada a lista vacía ni dispara una lectura más amplia.
- Una lista vacía solo es completa cuando el API confirmó fin de paginación y
  los anchors siguen estables.
- Un continuation token es opaco, no durable y nunca se reutiliza entre
  intentos, checks, namespaces o resourceVersions.
- UIDs, resourceVersions y owners no se aceptan por nombre equivalente.
- Counts negativos, condiciones imposibles o tipos no soportados producen
  `integrity_error`, no normalización silenciosa.
- Events legacy y `events.k8s.io/v1` se traducen a un único modelo propio; no se
  mezclan duplicados sin una identidad estable.
- La ausencia de NetworkPolicy necesaria para una relación declarada no prueba
  conectividad; se clasifica según contradicción o evidencia insuficiente.
- Ningún resultado parcial se presenta con `complete=true`.

## Decisiones abiertas

No quedan decisiones de producto necesarias para redactar esta entrega. Los
valores concretos de timeout, páginas, items y bytes son configuración
obligatoria de composición y no defaults de esta feature.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-26T12:59:16+01:00
```

La spec no autoriza implementación hasta que una persona complete este bloque y
el leader cambie el estado de la feature a `in_progress`.
