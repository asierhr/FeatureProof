# Entornos efímeros sobre Kubernetes — Requirements

## Objetivo y problema

FeatureProof ya obtiene targets mediante el contrato provider-neutral de la
feature 31, pero todavía no dispone de un provider gestionado productivo. Esta
feature incorpora `KubernetesProvider`: crea un namespace efímero por run desde
una plantilla y una política aprobadas, espera disponibilidad y devuelve un
`ValidationTarget` únicamente después de demostrar qué repositorio, revisión e
imagen están realmente desplegados.

El provider no es una capacidad genérica de Kubernetes. El modelo y las
superficies solo seleccionan un alias ya autorizado; composición fija cluster,
plantilla, imagen, límites, identidad, red y secretos. Los fallos de
infraestructura siguen siendo resultados operativos separados del veredicto
funcional.

## Alcance

- Catálogo Kubernetes inmutable, composition-owned y ligado a las selecciones
  selladas de la feature 31.
- Plantillas versionadas que producen exclusivamente recursos tipados y
  allowlisted.
- Imagen OCI obligatoriamente identificada mediante digest `sha256`.
- Namespace determinista por run con identidad, ownership y expiración
  verificables.
- Bootstrap previo de quota, defaults/límites, Pod Security, service account y
  NetworkPolicy antes de crear workloads.
- Provisioning y reconciliación idempotentes de workload y Service.
- Readiness acotado y atestación de workload, Pods, imagen y endpoint efectivos.
- Resolución de credenciales y secretos únicamente en composición y entrega
  directa al transporte Kubernetes o al namespace autorizado.
- Eliminación idempotente del namespace mediante precondición de UID, integrada
  con el release intent durable de la feature 31.
- Cliente Kubernetes productivo detrás de un puerto estrecho y fake hermético
  para tests sin cluster real.

## Fuera de alcance

- Aceptar YAML, JSON, manifests, patches, Helm charts, Kustomize overlays,
  comandos, args, scripts, nombres de recursos, namespaces, service accounts,
  volúmenes, imágenes, Secrets o configuración Kubernetes desde el modelo, la
  specification, el repositorio, una PR o el caller.
- Proporcionar la herramienta de inspección funcional de Kubernetes; pertenece
  a la feature 33.
- Ejecutar `exec`, `attach`, `port-forward`, logs, shell o comandos en Pods.
- Implementar el controlador periódico de expiración, huérfanos y cleanup; es
  la feature 34. Esta feature solo deja metadata e intención suficientes y
  ejecuta el release solicitado por el lifecycle actual.
- Introducir PostgreSQL, workers, leases distribuidas o un control plane; es la
  feature 35.
- Diseñar el formato general de configuración por proyecto; es la feature 36.
- Exponer un endpoint público mediante Ingress o LoadBalancer. La primera
  plantilla soportada publica un Service interno alcanzable por el transporte
  autorizado de FeatureProof.
- Considerar readiness o atestación del entorno como evidencia funcional de una
  restricción.

## Actores y términos

- **Kubernetes selection:** selección `managed` sellada por la feature 31 cuyos
  IDs resuelven un perfil y una plantilla Kubernetes autorizados.
- **Kubernetes template:** receta tipada, versionada e inmutable que fija
  workload, Service, puertos, replicas, probes y parámetros permitidos.
- **Project policy:** límites efectivos de CPU, memoria, objetos, TTL, seguridad
  y conectividad que la plantilla no puede ampliar.
- **Immutable image:** referencia OCI completa `repository@sha256:<digest>`;
  tags y digests abreviados no son autoridad.
- **Environment namespace:** namespace exclusivo del run, identificado además
  por UID para impedir adopción o borrado de un namespace recreado.
- **Bootstrap resources:** labels de Pod Security, ResourceQuota, LimitRange,
  service account mínima y NetworkPolicies que deben verificarse antes de crear
  workloads.
- **Readiness:** observación operacional de Deployment, Pods, Service y
  EndpointSlices; no es una assertion funcional.
- **Deployment attestation:** proyección sellada que liga namespace UID,
  plantilla, imagen observada, repositorio, commit, endpoint y expiración.
- **Secret bundle:** material privado resuelto en composición y accesible solo
  por el provider/transporte durante la aplicación al cluster.

## R1 — Selección y catálogo Kubernetes confiables

WHEN una selección gestionada resuelve `KubernetesProvider`, THE provider SHALL
resolver exactamente una plantilla versionada, un perfil de proyecto, un
cluster autorizado y una imagen inmutable desde un catálogo composition-owned,
sin defaults ni autoridad aportada por el caller o el modelo.

### Acceptance criteria

- GIVEN una selección sellada compatible WHEN se adquiere THEN sus IDs y
  digests resuelven una sola entrada de catálogo.
- GIVEN template, profile, cluster, network policy o digest ausente, duplicado,
  incompatible o alterado WHEN se valida THEN no se llama al API de Kubernetes.
- GIVEN una imagen por tag, digest abreviado, algoritmo distinto o referencia
  no canónica WHEN se compone THEN el provider se rechaza antes de I/O.
- GIVEN un intento desde request/modelo de sustituir imagen, plantilla, cluster
  o límites WHEN se procesa THEN no cambia la autoridad sellada.

## R2 — Namespace aislado e identidad exacta

WHEN comienza una adquisición nueva, THE provider SHALL derivar un namespace
estable y exclusivo del run, SHALL crearlo con ownership verificable y SHALL
rechazar cualquier namespace existente cuya identidad, UID o metadata no
coincida exactamente con la adquisición.

### Acceptance criteria

- GIVEN dos runs distintos WHEN adquieren THEN reciben nombres y UIDs de
  namespace distintos aunque compartan proyecto, template e imagen.
- GIVEN redelivery del mismo run y acquisition key WHEN reconcilia THEN adopta
  únicamente el mismo namespace con owner, run, proyecto y digests exactos.
- GIVEN namespace preexistente sin labels/annotations FeatureProof o con una
  relación alterada WHEN se observa THEN falla `integrity/conflict` y no crea
  workloads ni lo elimina.
- GIVEN nombre derivado WHEN se valida THEN cumple la sintaxis y longitud DNS de
  Kubernetes sin incluir IDs sensibles o no acotados en claro.

## R3 — Ausencia de operaciones y recursos arbitrarios

THE KubernetesProvider SHALL construir recursos mediante modelos tipados y una
allowlist cerrada, y SHALL NOT aceptar manifests, patches, comandos, args,
scripts, namespaces, service accounts, volúmenes, nombres de recursos o campos
Kubernetes libres procedentes del modelo, caller o contenido no confiable.

### Acceptance criteria

- GIVEN payload con YAML/JSON, command, args, volume, hostPath, privileged,
  serviceAccountName o namespace WHEN llega por cualquier superficie THEN no
  alcanza el renderer ni el cliente Kubernetes.
- GIVEN inspección del `EnvironmentProvider` y sus requests WHEN se revisan THEN
  no contienen tipos del SDK, manifests, mapas libres ni operaciones genéricas.
- GIVEN plantilla aprobada con un campo fuera del vocabulario soportado WHEN se
  carga THEN falla cerrada en composición.
- GIVEN el provider productivo WHEN se audita THEN no expone operaciones
  `exec`, `attach`, logs, proxy, patch arbitrario ni eliminación de recursos
  fuera de su namespace confirmado.

## R4 — Bootstrap de seguridad anterior a workloads

WHEN se crea un namespace, THE provider SHALL aplicar y verificar Pod Security,
ResourceQuota, LimitRange, service account de mínimo privilegio y políticas de
red efectivas antes de crear cualquier Pod, Deployment, Job u otro workload.

### Acceptance criteria

- GIVEN el flujo nominal WHEN se inspecciona el fake THEN el orden confirmado es
  namespace → controles de bootstrap → workload → Service.
- GIVEN fallo, denegación o mismatch de cualquier control WHEN ocurre THEN no se
  crea ningún workload y queda solicitado el cleanup.
- GIVEN service account efectiva WHEN se observa THEN no monta token por defecto
  ni posee Role/ClusterRoleBinding concedido por esta feature.
- GIVEN Pod Security profile configurado WHEN se crea THEN el namespace y el
  pod template conservan la política restrictiva exacta; una plantilla no puede
  rebajarla.

## R5 — Recursos acotados por policy

THE provider SHALL fijar requests y limits positivos de CPU y memoria para cada
contenedor soportado y SHALL aplicar quotas y límites máximos efectivos que no
excedan la policy del proyecto.

### Acceptance criteria

- GIVEN plantilla dentro de policy WHEN se renderiza THEN workload,
  ResourceQuota y LimitRange expresan los límites efectivos esperados.
- GIVEN CPU, memoria, replicas, objetos o TTL cero, inválido o superior a policy
  WHEN se compone THEN se rechaza antes de crear el namespace.
- GIVEN un contenedor sin request/limit o con limit inferior al request WHEN se
  valida THEN no se llama al cluster.
- GIVEN readback con quota, límites o replicas distintos WHEN se reconcilia THEN
  se clasifica como integrity/policy y no se considera ready.

## R6 — Conectividad deny-by-default

WHEN se prepara el namespace, THE provider SHALL instalar una política de red
deny-by-default y únicamente las reglas de ingress/egress derivadas de la policy
sellada antes de crear workloads o publicar el endpoint.

### Acceptance criteria

- GIVEN policy válida WHEN se aplica THEN existen reglas default-deny para
  ingress y egress y solo peers/ports allowlisted adicionales.
- GIVEN una plantilla que solicita CIDR, namespace selector, host o puerto no
  autorizado WHEN se valida THEN se rechaza antes de I/O.
- GIVEN cluster/profile que no declara soporte exigido de NetworkPolicy WHEN se
  compone THEN la adquisición queda no admitida; no continúa de forma insegura.
- GIVEN endpoint candidato WHEN se publica THEN también supera el
  `NetworkAuthorizer` provider-neutral de la feature 31.

## R7 — Provisioning y reconciliación idempotentes

WHEN Acquire o Reconcile se ejecutan con la misma acquisition key, THE provider
SHALL converger sobre el mismo namespace y los mismos objetos, verificando UID,
digests y contenido autoritativo antes de crear cada recurso faltante, sin
adoptar ni sobrescribir objetos incompatibles.

### Acceptance criteria

- GIVEN respuesta perdida después de cualquier create WHEN reconcilia THEN lee,
  verifica y continúa sin crear un segundo namespace u objeto lógico.
- GIVEN objeto ya existente e idéntico WHEN se repite THEN se acepta; GIVEN el
  mismo nombre con UID, owner o spec digest distinto THEN falla cerrado.
- GIVEN fallo parcial después del bootstrap WHEN se reentrega THEN crea solo los
  recursos autorizados que faltan y conserva el orden de seguridad.
- GIVEN llamadas concurrentes del mismo run WHEN compiten THEN como máximo una
  identidad queda confirmada y las demás convergen o reciben conflicto tipado.

## R8 — Readiness finito y completo

WHILE el lifecycle observa el entorno, THE provider SHALL informar `pending`,
`ready`, `unavailable` o `unhealthy` a partir de lecturas acotadas de workload,
Pods, Service y EndpointSlices, y SHALL NOT informar `ready` hasta que todos los
criterios configurados se cumplan simultáneamente.

### Acceptance criteria

- GIVEN Deployment con generación observada, replicas requeridas disponibles,
  Pods Ready y endpoint listo WHEN se observa THEN puede devolver `ready`.
- GIVEN Pod pendiente/fallido, probe no ready, rollout incompleto, Service sin
  endpoint o generación antigua WHEN se observa THEN no devuelve `ready`.
- GIVEN budget agotado, deadline o cancelación WHEN ocurre THEN termina como
  timeout/cancelled operacional sin goroutine o watch filtrado.
- GIVEN recursos desaparecidos o reemplazados durante readiness WHEN se leen
  THEN se detecta drift y no se publica un target.

## R9 — Atestación de revisión, imagen y endpoint efectivos

WHEN readiness queda confirmado, THE provider SHALL atestar namespace UID,
entorno, repositorio, commit completo, template/profile digests, imagen esperada
y `imageID` observado de todos los contenedores, Service y endpoint antes de
producir una `Observation` ready.

### Acceptance criteria

- GIVEN todos los Pods ready con el mismo digest de imagen esperado y metadata
  exacta WHEN se atesta THEN la observación liga repositorio y commit solicitados.
- GIVEN tag mutable, imageID ausente o distinto, mezcla de revisiones, annotations
  alteradas, namespace UID cambiado o endpoint distinto WHEN se observa THEN se
  clasifica como integrity y no devuelve target.
- GIVEN observación ready válida WHEN cruza `LifecycleService` THEN genera un
  `ValidationTarget/v1` cuyo environment, revision, attestation y network
  binding verifican sin autoridad adicional.
- GIVEN atestación expirada o recursos cambiados antes de ejecución/publicación
  WHEN se revalida THEN bloquea efectos nuevos y conserva cleanup pendiente.

## R10 — Secretos confinados a composición y Kubernetes

THE composition SHALL resolver cluster credentials, image pull credentials y
application secrets fuera del core y SHALL entregar sus valores únicamente al
transporte Kubernetes o a objetos Secret del namespace confirmado, sin
incluirlos en selecciones, requests, handles, observaciones, errores, eventos,
evidencias, modelo, packages o reportes.

### Acceptance criteria

- GIVEN perfil secretless WHEN se compone THEN no se resuelve ni crea ningún
  Secret.
- GIVEN bundle autorizado WHEN se aplica THEN solo el cliente privado recibe el
  canario y el workload referencia nombres provider-owned, no valores.
- GIVEN error remoto, serialización, evento o proyección durable WHEN se escanea
  byte-for-byte THEN no contiene el canario, bytes base64, headers o kubeconfig.
- GIVEN resolución ausente, fallida o cancelada WHEN se compone THEN no comienza
  adquisición; no se sustituye con credenciales por defecto.

## R11 — Ownership, expiración y TTL verificables

THE provider SHALL incluir y verificar en namespace y recursos gestionados el
owner, run ID seguro, proyecto, acquisition/template/image digests, instante de
expiración y TTL positivo y acotado por policy.

### Acceptance criteria

- GIVEN adquisición válida WHEN se lee el namespace THEN la metadata basta para
  atribuirlo inequívocamente y calcular si expiró sin consultar al modelo.
- GIVEN TTL superior a policy, expiry inconsistente con reloj/TTL o metadata
  ausente WHEN se valida THEN se rechaza o clasifica integrity.
- GIVEN restart WHEN Reconcile lee el namespace THEN reconstruye y verifica la
  misma identidad desde metadata y UID, no desde nombres por sí solos.
- GIVEN futura limpieza independiente WHEN inspecciona metadata THEN dispone de
  owner/run/proyecto/expiry/TTL sin necesitar secretos ni payloads de evidencia.

## R12 — Release seguro e idempotente

WHEN `LifecycleService` entrega un release request después de persistir su
intención durable, THE provider SHALL eliminar únicamente el namespace ligado al
handle confirmado, usando precondición de UID y tratando ausencia confirmada
como release idempotente.

### Acceptance criteria

- GIVEN namespace con nombre y UID exactos WHEN se libera THEN solicita una sola
  eliminación acotada y las reentregas convergen.
- GIVEN namespace ya ausente WHEN se libera THEN devuelve `already_released`;
  GIVEN mismo nombre con UID distinto THEN no lo elimina y falla integrity.
- GIVEN timeout, cancelación o respuesta perdida WHEN se libera THEN no declara
  éxito sin confirmación y la feature 31 conserva `release_pending` recuperable.
- GIVEN finalización correcta, fallo funcional, error operativo o cancelación
  posterior a posible create WHEN termina THEN existe release intent durable
  antes del delete.

## R13 — Fallos Kubernetes son operativos

IF deployment, admission, quota, security, connectivity, readiness, atestación
o cleanup falla, THEN THE workflow SHALL registrar una categoría operacional
segura y SHALL NOT convertirla en assertion incumplida ni veredicto funcional
`failed`.

### Acceptance criteria

- GIVEN Forbidden, quota exceeded, admission denied, unavailable o rollout
  unhealthy WHEN se mapea THEN produce una categoría estable sin body remoto.
- GIVEN timeout/cancelación WHEN se proyecta THEN conserva su causa tipada y no
  inventa evidencia funcional.
- GIVEN cleanup pendiente después de un veredicto existente WHEN se consulta
  THEN conserva el veredicto y muestra el estado operacional por separado.
- GIVEN assertion HTTP real sobre un target atestado WHEN falla THEN continúa
  siendo un `failed` funcional independiente del lifecycle Kubernetes.

## R14 — Límites, cancelación, retries y concurrencia

THE provider SHALL propagar `context.Context` por toda llamada Kubernetes,
limitar cada operación y listado, paginar de forma acotada, reintentar solo
errores transitorios seguros mediante el lifecycle existente y no mantener
locks durante red, espera o callbacks.

### Acceptance criteria

- GIVEN cancelación/deadline antes o durante cada operación WHEN ocurre THEN el
  cliente recibe la cancelación y no comienza la siguiente fase.
- GIVEN rate limit o error transitorio WHEN queda budget THEN Acquire/Reconcile
  converge con la misma key; Forbidden, Invalid e integrity no se reintentan.
- GIVEN respuestas/listados mayores al máximo configurado WHEN se observan THEN
  falla operacionalmente sin truncar una decisión de autoridad.
- GIVEN runs independientes y redeliveries concurrentes bajo `-race` WHEN se
  ejecutan THEN progresan sin estado compartido inseguro, waits reales o leaks.

## R15 — Cliente real, observabilidad y verificación hermética

THE implementation SHALL proporcionar un adapter Kubernetes productivo y una
suite hermética que cubra el provider completo sin usar cluster, red ni
credenciales reales, manteniendo trazabilidad `R→task→test` y observabilidad
content-free.

### Acceptance criteria

- GIVEN composición productiva WHEN se revisa THEN existe un cliente Kubernetes
  autenticado explícitamente y no un fake o fallback silencioso.
- GIVEN `go test ./...` WHEN se ejecuta THEN no contacta Kubernetes, registry,
  DNS, cloud, modelo ni red externa.
- GIVEN eventos/logs/errores WHEN se inspeccionan THEN contienen IDs/digests,
  fase, duración y categoría allowlisted, nunca manifests completos, endpoint
  completo, Secret, token, kubeconfig o body remoto.
- GIVEN todo símbolo nuevo o modificado WHEN se audita THEN entidades,
  constantes, variables, funciones, métodos y helpers de test incluyen un
  comentario sobre su responsabilidad dentro del sistema.
- GIVEN el diff productivo WHEN se verifica THEN obtiene al menos 85% statement
  coverage sin redondear y supera formato, vet, suites repetidas/race, runtime
  Linux y cross-builds aplicables.

## Casos límite y errores

- Selecciones no `managed`, provider/version mismatch y catálogo no sellado se
  rechazan antes del cliente Kubernetes.
- Un namespace `Terminating` nunca se adopta como entorno nuevo ni se declara
  ready; release puede permanecer pendiente.
- `AlreadyExists` solo es éxito después de GET y verificación exacta; `NotFound`
  solo confirma release, no readiness.
- Un objeto con managed fields, UID, owner, namespace o spec digest inesperado
  no se corrige mediante update/patch destructivo.
- Una policy válida en objetos pero no soportada por el cluster/profile no se
  considera aplicada.
- Cero Pods ready, Pods duplicados, rollout de revisión anterior, EndpointSlice
  vacío y `imageID` aún no informado mantienen pending o producen unhealthy
  según la condición tipada.
- Los valores de Secret nunca participan en digests, metadata, equality errors
  ni observabilidad; solo referencias opacas autorizadas pueden hacerlo.
- La eliminación no retira finalizers ajenos ni fuerza un namespace; esas
  incidencias quedan cleanup pending para la feature 34.

## Decisiones abiertas

No quedan decisiones de producto necesarias para esta entrega. Los valores
concretos de templates, quotas, TTL, perfiles de Pod Security, reglas de red,
cluster y secretos se reciben de configuración confiable y se validan contra
los límites anteriores. La implementación puede fijar una versión compatible
del cliente oficial de Kubernetes sin convertir sus DTOs en contratos internos.

## Aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-26T08:32:56+01:00
```

La implementación no puede comenzar hasta aprobación humana explícita y cambio
del estado a `in_progress` por el leader.
