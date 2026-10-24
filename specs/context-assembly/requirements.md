# Ensamblado seguro de contexto para agentes — Requirements

## Objetivo y problema

FeatureProof ya dispone de restricciones, contratos reducidos, planes,
evidencias verificadas y memoria de proyecto validada. Entregar esas fuentes
directamente a un modelo mezclaría autoridad con contenido no confiable,
permitiría que cada consumidor aplicase filtros distintos y dificultaría saber
qué información produjo una decisión.

Esta feature introduce un `ContextAssembler` determinista. Para cada rol
agéntico construye una proyección mínima, acotada y trazable; separa
instrucciones confiables, configuración aprobada y datos no confiables; y sella
la selección exacta para detectar drift antes de aprobar o ejecutar un plan.

## Alcance

- Definir un contrato versionado para solicitudes, elementos, secciones,
  omisiones, contradicciones, snapshots y bindings de contexto.
- Admitir proyecciones ya verificadas de restricciones, contrato, diff,
  configuración, memoria, historial y evidencias permitidas.
- Aplicar perfiles cerrados para `planner`, `analyzer` y `diagnostic`.
- Filtrar de forma determinista por identidad, scope, revisión, autoridad,
  estado, validez, sensibilidad y política del rol antes de cualquier ranking.
- Separar por frontera de confianza las instrucciones del sistema, la
  configuración aprobada y todos los datos no confiables.
- Aplicar presupuestos explícitos de elementos, bytes y tokens sin partir una
  unidad semántica.
- Permitir un ranker semántico opcional sobre candidatos ya autorizados, sin
  convertir su resultado en autoridad.
- Registrar inclusión, omisión, truncado, conflicto e insuficiencia mediante
  IDs y motivos seguros.
- Producir un snapshot canónico y un binding ligado al run, revisión,
  configuración, memoria y plan.
- Integrar la proyección de planificación con el workflow existente y ofrecer
  el mismo contrato a consumidores de análisis y diagnóstico.

## Fuera de alcance

- Obtener título, descripción, head, archivos o diff desde GitHub; corresponde
  a la feature 30. Esta feature solo acepta una proyección de diff ya autorizada
  y sellada por un productor anterior.
- Crear, reservar, atestar o liberar entornos; corresponde a la feature 31.
- Cargar o administrar la configuración versionada de proyecto; corresponde a
  la feature 36. El ensamblador recibe una proyección aprobada y su digest.
- Ejecutar herramientas, exponer handles ejecutables, credenciales, targets,
  permisos, identities privadas o ampliar capacidades del agente.
- Aprobar planes, decidir assertions, calcular veredictos o convertir texto del
  modelo en evidencia.
- Generar embeddings mediante un servicio externo, definir un proveedor
  vectorial concreto o conservar un índice vectorial como fuente de verdad.
- Persistir prompts completos, respuestas del modelo o chain of thought.

## Actores y términos

- **Composition authority:** configuración confiable que fija perfiles,
  fuentes, sensibilidad, filtros, presupuestos, instrucciones y tokenizador.
- **Source producer:** componente que entrega una proyección ya validada junto
  con identidad, digest, procedencia, autoridad y binding verificable.
- **Role:** consumidor cerrado (`planner`, `analyzer` o `diagnostic`) con una
  allowlist propia de clases y campos; un caller no puede definir roles nuevos.
- **Context item:** unidad atómica versionada que puede incluirse u omitirse
  completa y conserva ID, clase, trust class, autoridad, procedencia, digest y
  referencia segura.
- **Trusted instructions:** instrucciones estáticas y versionadas aportadas por
  composition authority; ninguna otra fuente puede originarlas.
- **Approved configuration:** datos de configuración autenticados y
  proyectados como datos separados; su texto no se convierte en instrucciones.
- **Untrusted data:** restricciones, contratos, diffs, memoria, historial,
  evidencias y cualquier contenido externo, aunque posea procedencia o una
  autoridad de dominio alta.
- **Eligibility:** resultado de superar todos los filtros estructurales. No
  implica relevancia, verdad funcional ni permiso operativo.
- **Semantic ranker:** optimización opcional y reconstruible que solo ordena el
  conjunto elegible y nunca añade, modifica o autoriza elementos.
- **Context snapshot:** proyección inmutable, canónica y secret-free que recibe
  un rol, con manifiestos de selección y omisión.
- **Context binding:** sello content-free que vincula el snapshot y el plan
  preparado con el run y los digests de autoridad actuales.

## R1 — Contrato estricto y detached

WHEN an assembly request or source projection enters the application boundary,
THE system SHALL validate it against one closed versioned schema and copy it
into immutable internal values before selection.

### Acceptance criteria

- GIVEN versión, role, source kind, trust class, authority, ID, digest, safe
  reference o binding desconocido/inválido WHEN se admite THEN falla con
  categoría y field path estables antes de invocar ranker, token counter o
  persistencia.
- GIVEN campos desconocidos, uniones ambiguas, JSON duplicado/no canónico,
  números no finitos, maps/slices mutados después de la llamada o límites
  excedidos WHEN se procesa THEN no alteran ni producen un snapshot parcial.
- GIVEN dos solicitudes semánticamente iguales con distinto orden de mapas WHEN
  se ensamblan THEN producen el mismo orden y digest.
- GIVEN un source producer que no confirma el digest o binding declarado WHEN
  se lee THEN la fuente completa se rechaza; no se usa contenido sin verificar.

## R2 — Perfiles mínimos y cerrados por rol

WHEN context is requested for an agent role, THE system SHALL apply the exact
trusted profile for `planner`, `analyzer` or `diagnostic` and SHALL include only
source kinds, fields, sensitivity and instruction versions allowed to that role.

### Acceptance criteria

- GIVEN las mismas fuentes WHEN se ensamblan para los tres roles THEN los
  snapshots son diferentes y cada uno contiene únicamente su allowlist.
- GIVEN `planner` THEN recibe restricciones, contrato reducido, configuración de
  planificación, memoria elegible y descriptores no ejecutables; no recibe
  evidencias completas ni historial diagnóstico irrelevante.
- GIVEN `analyzer` THEN recibe hechos/evidencias permitidos y plan ejecutado
  necesarios para interpretar resultados; no recibe credenciales, bodies
  completos ni capacidades ejecutables.
- GIVEN `diagnostic` THEN recibe errores estructurales, hechos acotados e
  historial permitido; no recibe automáticamente toda la memoria o evidencia.
- GIVEN un caller que solicita campos, sensibilidad o fuentes fuera del perfil
  WHEN se cruza con la autoridad THEN solo puede estrechar la selección y todo
  intento de ampliación se rechaza.

## R3 — Filtros estructurales antes del ranking

WHEN candidate items are selected, THE system SHALL filter them first by exact
project, repository, run/review scope, revision, lifecycle state, authority,
validity, sensitivity, source integrity and role capability before any semantic
ranking or content-based selection.

### Acceptance criteria

- GIVEN memoria de otro proyecto, repo, run o commit, memoria stale/terminal,
  autoridad insuficiente o sensibilidad excesiva WHEN se selecciona THEN nunca
  llega al ranker ni al snapshot.
- GIVEN una fuente sin semántica de memoria WHEN se filtra THEN sus bindings de
  proyecto/revisión/configuración y política específica se verifican con la
  misma regla fail-closed.
- GIVEN un ranker que devuelve un ID no elegible, duplicado, desconocido o con
  contenido modificado WHEN se valida su salida THEN el ensamblado falla sin
  incluirlo.
- GIVEN ausencia del ranker WHEN se ensambla THEN existe un orden estructural
  estable y verificable.

## R4 — Separación irreversible por trust class

WHEN a snapshot is built, THE system SHALL place trusted instructions,
approved configuration and untrusted data in distinct typed sections and SHALL
NOT permit source content or a ranker to change an item's section.

### Acceptance criteria

- GIVEN texto en diff, contrato, log, evidencia, memoria o documento que afirma
  ser instrucción de sistema WHEN se proyecta THEN permanece dato no confiable
  etiquetado o se omite; nunca aparece en trusted instructions.
- GIVEN memoria `approved` o evidencia determinista WHEN se proyecta THEN su
  autoridad de dominio se conserva como metadata, pero su contenido sigue en la
  sección de datos no confiables.
- GIVEN approved configuration WHEN se proyecta THEN queda separada de las
  instrucciones, conserva digest/procedencia y no puede aportar nuevos source
  kinds, roles, capabilities o budgets.
- GIVEN serialización para un modelo WHEN se inspecciona THEN cada sección tiene
  delimitación estructural inequívoca y el modelo no recibe un string compuesto
  que pierda las etiquetas de confianza.

## R5 — Trazabilidad de inclusión y omisión

WHEN an item is considered for context, THE system SHALL retain its stable ID,
source kind, provenance, authority, safe reference and digest, and SHALL record
one controlled inclusion or omission reason.

### Acceptance criteria

- GIVEN un elemento incluido WHEN se consulta el manifiesto THEN se puede
  relacionar con su fuente exacta sin exponer paths privados, authenticators ni
  contenido adicional.
- GIVEN exclusión por scope, role, state, authority, validity, sensitivity,
  secreto, contradicción, presupuesto, ranking o duplicado WHEN se registra
  THEN usa un reason code cerrado y no copia contenido externo.
- GIVEN una fuente truncada por su productor WHEN se admite THEN el snapshot
  declara esa parcialidad y nunca la presenta como completa.
- GIVEN motivos o procedencia con canarios WHEN se proyecta THEN solo sobreviven
  IDs/referencias previamente validados y categorías generadas por el sistema.

## R6 — Presupuestos de elementos, bytes y tokens

WHEN context is assembled, THE system SHALL enforce trusted positive budgets
for items, canonical bytes and model tokens, including envelope and section
overhead, and SHALL truncate only at whole-item boundaries.

### Acceptance criteria

- GIVEN un item que no cabe en cualquiera de los tres presupuestos WHEN se
  empaqueta THEN se omite completo con motivo `budget`; no se corta texto, JSON,
  provenance ni una lista interna.
- GIVEN un snapshot final WHEN se cuenta con el token counter configurado para
  el provider/model seleccionado THEN su conteo confirmado no supera el límite.
- GIVEN token counter ausente, incompatible, cancelado o fallido WHEN el rol
  requiere inferencia THEN no se estima silenciosamente y se devuelve error o
  contexto insuficiente tipado antes de llamar al modelo.
- GIVEN instrucciones o configuración obligatorias que por sí solas exceden el
  presupuesto WHEN se ensambla THEN no se elimina una guardia de seguridad para
  hacer sitio; el resultado es insuficiente.
- GIVEN límites exactos en el borde WHEN se repite THEN el mismo conjunto de
  unidades queda incluido y los contadores coinciden con los bytes serializados.

## R7 — Ranking opcional sin autoridad

WHERE a semantic ranker is configured, THE system SHALL allow it to reorder only
the already eligible candidate IDs under a finite work budget and SHALL verify
its output before packing.

### Acceptance criteria

- GIVEN un índice vectorial ausente, stale o corrupto WHEN se selecciona THEN se
  ignora/reconstruye como derivado y el orden estructural sigue disponible.
- GIVEN el mismo score para varios elementos WHEN se ordena THEN se desempata
  por la tupla canónica definida por el sistema, no por orden de mapa o llegada.
- GIVEN un score no finito, cantidad incorrecta, ID ajeno o budget agotado WHEN
  se valida THEN ningún candidato gana autoridad y el resultado usa un outcome
  seguro y determinista.
- GIVEN contenido prohibido por filtros estructurales WHEN existe en el índice
  THEN no se materializa ni se envía al ranker.

## R8 — Contradicciones e insuficiencia explícitas

WHEN eligible items contradict one another or mandatory context is absent, THE
system SHALL declare the conflict or insufficiency and SHALL NOT silently choose
a winner or present a complete context.

### Acceptance criteria

- GIVEN conflictos de memoria reportados por `ProjectMemory` o items con la
  misma clave lógica y valores incompatibles WHEN se ensamblan THEN se conservan
  todos sus IDs en un conflicto acotado y ninguno se eleva por recencia,
  confidence o score.
- GIVEN una fuente obligatoria ausente, stale, íntegramente omitida o demasiado
  grande WHEN el perfil la exige THEN el resultado es `insufficient_context`
  con kinds/reasons cerrados y no comienza inferencia.
- GIVEN conflictos no obligatorios para el rol WHEN se continúa THEN el snapshot
  los declara y omite los items afectados según la política sellada.
- GIVEN contenido parcial permitido WHEN se incluye THEN la sección y el
  snapshot se marcan `partial`; nunca `complete`.

## R9 — Snapshot canónico y binding al plan

WHEN assembly succeeds, THE system SHALL produce one immutable versioned
snapshot with a canonical digest bound to role, run, project, repository,
revision, configuration, source bindings, instruction/profile versions and
budgets; after planning it SHALL bind that snapshot to the exact prepared plan.

### Acceptance criteria

- GIVEN dos ensamblados equivalentes WHEN se serializan THEN bytes, digest,
  selection manifest y omission manifest son idénticos.
- GIVEN cambio en contenido incluido, orden, metadata de autoridad, omisiones,
  budget, perfil, instrucción o source binding WHEN se sella THEN cambia el
  digest.
- GIVEN generación de un plan WHEN termina THEN `ContextBinding` contiene
  `context_digest`, `plan_id`, digest del plan preparado y los bindings de
  revisión/configuración/memoria, sin copiar el prompt.
- GIVEN un binding manipulado o perteneciente a otro run/proyecto/plan WHEN se
  usa THEN falla cerrado antes de approval o ejecución.

## R10 — Revalidación de contexto obsoleto

BEFORE plan approval and before execution, THE workflow SHALL revalidate the
context binding against current trusted revision, configuration, memory and
source authorities and SHALL stop when any required binding drifted.

### Acceptance criteria

- GIVEN cambio de commit, configuración, instrucciones/perfil, memoria
  invalidada/sustituida/expirada o evidencia revocada WHEN se revalida THEN el
  plan no se aprueba ni ejecuta y se devuelve `stale_context` tipado.
- GIVEN solo cambió un índice derivado sin cambiar el binding autoritativo WHEN
  se revalida THEN el snapshot sigue válido.
- GIVEN restart del workflow WHEN se restaura THEN carga snapshot/binding por
  digest y repite las mismas verificaciones antes de continuar.
- GIVEN un run histórico ya terminado WHEN cambian fuentes posteriores THEN su
  snapshot y binding conservan la selección original y no alteran el resultado.

## R11 — Journal y observabilidad sin contenido

WHEN assembly or revalidation emits events, logs or durable metadata, THE
system SHALL record only safe identities, digests, counts, reason codes,
duration and outcome, never complete prompts or unnecessary source content.

### Acceptance criteria

- GIVEN ensamblado exitoso, insuficiente, stale, cancelado o fallido WHEN se
  observa THEN existe un evento terminal exactamente una vez con run, role,
  digest cuando exista, counts por sección y outcome seguro.
- GIVEN inclusión u omisión WHEN se persiste el manifiesto THEN contiene IDs,
  digests y motivos, no valores, diff, logs, bodies, evidence payloads o memoria
  textual.
- GIVEN un error de source, ranker o tokenizer que contiene un secreto WHEN se
  expone THEN solo conserva category, stage y path controlados.
- GIVEN métricas WHEN se publican THEN distinguen candidatos, elegibles,
  incluidos, omitidos, bytes, tokens, conflictos y duración sin labels de alta
  cardinalidad derivados del contenido.

## R12 — Integración con consumidores sin capabilities ejecutables

WHEN planner, analyzer or diagnostic consumers invoke a model, THE system SHALL
provide only their assembled snapshot and SHALL keep executable capabilities,
credentials, stores and external clients outside the model boundary.

### Acceptance criteria

- GIVEN planificación en el workflow real WHEN genera THEN usa el snapshot de
  role `planner`, persiste su digest/binding en el artifact y deja de construir
  un payload paralelo desde fuentes crudas.
- GIVEN consumidores `analyzer` y `diagnostic` WHEN solicitan contexto THEN usan
  el mismo assembler con perfiles distintos y tests contractuales prueban que no
  reciben la proyección de otro rol.
- GIVEN descriptores de capability permitidos WHEN aparecen para planner THEN
  contienen identidad y schema declarativo, nunca closures, handles, tokens,
  clients o permiso de ejecución.
- GIVEN ausencia de context assembly configurado en un flujo que requiere
  agente WHEN se compone THEN falla temprano; no vuelve al payload legacy.

## R13 — Cancelación, concurrencia e idempotencia

WHEN assembly is cancelled, retried or performed concurrently, THE system SHALL
propagate cancellation, bound all work and converge to the same immutable
snapshot for the same idempotency identity and source bindings.

### Acceptance criteria

- GIVEN cancelación durante source read, filtering, ranking, token counting,
  canonicalization o persistence WHEN retorna THEN no queda snapshot publicable,
  goroutine ni trabajo externo activo.
- GIVEN redelivery con misma idempotency key y solicitud/digests iguales WHEN se
  ensambla THEN devuelve el mismo snapshot sin un segundo evento terminal
  durable.
- GIVEN la misma key con request o bindings distintos WHEN se reintenta THEN se
  rechaza como conflicto.
- GIVEN ensamblados concurrentes para roles/runs distintos WHEN se ejecutan THEN
  no comparten buffers o estado mutable; para la misma identidad convergen por
  CAS o equivalente.

## R14 — Secretos y prompt injection

BEFORE any item reaches ranking, tokenization, serialization, persistence or a
model, THE system SHALL reject or use a pre-redacted projection for prohibited
content and SHALL treat all non-instruction source text as inert data.

### Acceptance criteria

- GIVEN token, cookie, credential, private key, connection string, variable
  sensible, body no redactado, prompt completo, chain of thought o canario en
  cualquier source/metadata depth WHEN se ensambla THEN no aparece byte-for-byte
  en snapshot, índice, manifests, events, errors ni model request.
- GIVEN prompt injection en diff, código, contrato, log, respuesta, documento o
  memoria WHEN se incluye como dato seguro THEN conserva etiqueta de fuente y no
  cambia role, instrucciones, filtros, budgets, capabilities ni output schema.
- GIVEN detector cancelado/fallido o redaction state desconocido WHEN se evalúa
  THEN falla cerrado antes del ranker y del modelo.
- GIVEN referencias a credenciales o evidencia WHEN se proyectan THEN son IDs
  opacos allowlisted; el assembler no las resuelve a valores o bodies.

## R15 — Verificación hermética, trazabilidad y comentarios sistémicos

WHEN the feature is verified, THE test suite SHALL cover every requirement with
controlled sources/ranker/tokenizer/model, SHALL cover at least 85% of every new
or modified production statement across build-tagged files, and SHALL preserve
systemic-responsibility comments on every new or modified entity, constant,
variable, function, method and test helper.

### Acceptance criteria

- GIVEN el mapa R→test WHEN se revisa THEN cada R1–R15 enlaza a al menos un test
  concreto y un resultado reproducible.
- GIVEN tests repetidos y `-race` WHEN se ejecutan THEN orden, budgets,
  idempotencia, cancelación y concurrencia permanecen deterministas sin red,
  credenciales, modelos o índices reales.
- GIVEN código condicionado por plataforma WHEN se calcula coverage del diff
  THEN se incluyen todos los statements aplicables y el porcentaje sin redondear
  es >=85%.
- GIVEN cualquier entidad o función nueva/modificada, incluidos helpers de test
  WHEN se audita THEN posee un comentario que explica su responsabilidad dentro
  del sistema.

## Errores y casos límite

- IDs, digests, roles, source kinds, trust classes, autoridades, referencias,
  revisiones o presupuestos inválidos se rechazan antes de leer contenido.
- `insufficient_context` es un resultado de dominio; corrupción, secreto,
  autorización inválida o fallo de persistencia son errores operativos.
- Un item no se divide para satisfacer un límite. Las colecciones internas que
  necesiten truncado deben llegar como items independientes desde el productor.
- Un ranker o token counter no puede devolver contenido, solo IDs/scores o
  conteos; cualquier discrepancia falla cerrado.
- La confidence o relevancia no resuelve contradicciones ni concede autoridad.
- La cancelación del caller no se sustituye por `context.Background()`; una
  confirmación terminal ya comprometida puede usar una finalización interna
  explícitamente acotada.

## Decisiones abiertas

No hay decisiones de producto abiertas para iniciar la implementación. El
algoritmo concreto del ranker y el formato físico del store pueden elegirse
durante la implementación si son opcionales/derivados, reproducibles mediante
fakes, no cambian elegibilidad y mantienen todos los contratos anteriores.

## Aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-24T15:52:24+02:00
```

La implementación no puede comenzar hasta que una persona apruebe explícitamente
esta especificación y el leader registre la transición a `in_progress`.
