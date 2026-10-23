# Memoria de proyecto validada y versionada — Requirements

## Objetivo y problema

FeatureProof ya produce contratos, planes, evidencias y resultados verificables,
pero cada ejecución parte sin una forma segura de conservar conocimiento útil del
proyecto. Reutilizar texto libre del agente, logs o respuestas externas como
memoria permitiría propagar datos obsoletos, secretos o instrucciones maliciosas.

Esta feature introduce una memoria tipada, versionada y auditable. El sistema
conserva propuestas, observaciones y decisiones con su autoridad y procedencia;
solo devuelve recuerdos compatibles con el proyecto, scope, revisión, validez y
política de consulta. El agente puede proponer candidatos y consultar una
proyección autorizada, pero nunca convierte su propia salida en autoridad.

## Alcance

- Definir el contrato estricto `ProjectMemory/v1` y valores canónicos acotados.
- Distinguir scopes `run`, `revision` y `project` y las clases `fact`,
  `decision`, `convention` y `diagnostic`.
- Modelar los estados `proposed`, `observed`, `approved`, `invalidated`,
  `superseded` y `expired` mediante una máquina de estados cerrada.
- Registrar procedencia exacta, actor, productor, commit/configuración,
  evidencia, autoridad, confianza, sensibilidad y ventana de validez.
- Permitir propuestas del agente sin aprobación, elevación, invalidación ni
  ampliación de scope implícitas.
- Proporcionar un `MemoryStore` propio y un adapter local bajo
  `.featureproof/memory` con journal append-only, snapshots autenticados,
  publicación atómica, CAS, idempotencia y recovery.
- Rechazar secretos y contenido inseguro antes de persistirlo o indexarlo.
- Consultar mediante filtros estructurales obligatorios y orden estable; el
  índice es derivado y reconstruible.
- Vincular a runs y planes las versiones exactas de memoria utilizadas sin
  cambiar retrospectivamente su resultado.
- Compartir un servicio de aplicación y proyecciones seguras entre las
  superficies que expongan memoria.

### Contrato funcional observable

`ProjectMemory/v1` es un snapshot inmutable con estos campos obligatorios:

- `id` y `version`, identificadores opacos generados por el sistema;
- `project_id` exacto y un `scope` cerrado:
  `run(run_id, revision_id)`, `revision(repository_id, commit_oid)` o
  `project(repository_id)`;
- `class`: `fact`, `decision`, `convention` o `diagnostic`;
- `state`: uno de los seis estados cerrados;
- `subject`, `predicate` y un valor tipado `string`, `boolean`, `integer` o
  JSON canónico acotado; no se admiten templates, expresiones ni valores no
  finitos;
- `provenance`: fuente y referencia exactas, actor tipado e ID, productor y
  versión, instante, repository ID, y al menos un commit OID completo o digest
  de configuración aplicable;
- referencias no vacías a evidencia verificada cuando la autoridad sea
  `deterministic_observation`;
- `authority`: `agent_proposal`, `deterministic_observation`,
  `trusted_configuration` o `human_approval`;
- `confidence`: `low`, `medium` o `high`, que no concede autoridad;
- `sensitivity`: `public`, `internal` o `restricted`, que limita proyecciones
  pero nunca autoriza secretos;
- `valid_from` y, opcionalmente, `valid_until`, más los digests canónicos del
  contenido, la procedencia y el registro completo.

Los límites efectivos, clases consultables, autoridades admitidas por consumidor,
política de staleness, sensibilidad máxima, actores humanos/configuraciones de
confianza y claves de integridad pertenecen a configuración confiable. Ningún
documento de memoria, caller remoto o modelo puede ampliarlos.

## Fuera de alcance

- Ensamblar prompts, seleccionar memoria por rol, ranking semántico, embeddings
  o presupuestos de contexto; corresponden a la feature 29.
- Convertir recuerdos en nuevas capacidades, permisos, operaciones, targets,
  identidades, credenciales o aprobaciones de planes.
- Aprender automáticamente de texto del agente, del chain of thought, de bodies
  HTTP, logs o documentos externos.
- Resolver contradicciones mediante un modelo o elegir silenciosamente un
  recuerdo ganador.
- Sincronización remota, base de datos distribuida, tenancy/RBAC general o
  edición manual de ficheros del store.
- Reescribir evidencia, resultados, planes o memoria histórica de runs previos.
- Introducir nuevas assertions, modificar el veredicto determinista o permitir
  que memoria sustituya al contrato/revisión/configuración vinculados.

## Actores y términos

- **Agent proposer:** modelo que emite un candidato no confiable y solo puede
  originar `proposed` con autoridad `agent_proposal`.
- **Trusted observer:** componente determinista configurado que deriva un hecho
  de evidencia verificada y puede originar `observed`.
- **Human authority:** principal autenticado y autorizado por configuración para
  aprobar, invalidar o sustituir recuerdos de un proyecto.
- **Trusted configuration authority:** configuración local sellada que importa
  o transiciona recuerdos dentro de una allowlist explícita.
- **Memory record:** snapshot tipado e inmutable de una versión de recuerdo.
- **Memory event:** transición append-only ligada a la versión anterior, CAS e
  idempotency key.
- **Eligible memory:** versión que supera todos los filtros estructurales para
  una consulta concreta; no equivale a instrucción del sistema.
- **Historical binding:** lista sellada de IDs, versiones, digests y secuencia
  del store que un run consumió.
- **Stale:** recuerdo cuya revisión, configuración, evidencia o ventana temporal
  dejó de ser compatible con la consulta actual.

## R1 — Contrato estricto, tipado e inmutable

WHEN a memory candidate enters the application boundary, THE system SHALL
validate and normalize it into one immutable `ProjectMemory/v1` record before
any persistence, indexing or query visibility.

### Acceptance criteria

- GIVEN campos desconocidos, ausentes, nulos, enums abiertos, IDs inválidos,
  JSON no finito, profundidad/tamaño excedidos o unión de scope/valor ambigua
  WHEN se admite THEN se rechaza con categoría y field path estables y no se
  escribe ningún byte.
- GIVEN dos entradas semánticamente iguales con distinto orden de mapas WHEN se
  normalizan THEN producen el mismo content digest.
- GIVEN una mutación posterior de DTOs, maps o slices de entrada WHEN se carga el
  registro THEN no cambia su valor ni digest.
- GIVEN una versión de contrato desconocida WHEN se carga o consulta THEN falla
  cerrado sin interpretarla como v1.

## R2 — Scopes exactos y sin ampliación

WHEN a memory is created or queried, THE system SHALL enforce its exact project
and `run`, `revision` or `project` scope and SHALL NOT infer a broader scope.

### Acceptance criteria

- GIVEN scope `run` WHEN se consulta THEN solo es elegible para el mismo
  `project_id`, `run_id` y `revision_id`.
- GIVEN scope `revision` WHEN se consulta THEN solo es elegible para el mismo
  proyecto, repository ID y commit OID completo; refs, prefijos y branches no
  cuentan como equivalencia.
- GIVEN scope `project` WHEN se consulta THEN exige el mismo proyecto y
  repository ID y además supera validez/staleness; no se aplica a otro repo.
- GIVEN un agente que propone scope más amplio que el máximo configurado WHEN se
  admite THEN se rechaza, sin degradarlo ni ampliarlo silenciosamente.

## R3 — Máquina de estados y autoridad de transición

WHEN a memory changes lifecycle state, THE system SHALL apply one explicit
authorized transition and append a new immutable version without rewriting any
prior version.

### Acceptance criteria

- GIVEN un agente WHEN propone THEN la única creación permitida es
  `proposed/agent_proposal`; no puede crear `observed` o `approved`.
- GIVEN un trusted observer con evidencia verificada WHEN crea o valida una
  propuesta compatible THEN puede producir `observed/deterministic_observation`.
- GIVEN una autoridad humana o configuración confiable allowlisted WHEN aprueba
  THEN `proposed` u `observed` pasa a `approved` y registra la decisión exacta.
- GIVEN `proposed`, `observed` o `approved` WHEN una autoridad humana/configurada
  invalida o sustituye THEN se añade `invalidated` o `superseded`; el agente y
  datos externos no pueden iniciar esas transiciones.
- GIVEN `valid_until` alcanzado o staleness determinista confirmada bajo la
  política sellada WHEN se reconcilia THEN el sistema registra `expired` una sola
  vez; todos los estados terminales rechazan más transiciones.

## R4 — Procedencia, confianza y evidencia exactas

WHEN a memory version is committed, THE system SHALL bind its source, actor,
producer, time, repository, applicable commit/configuration and evidence lineage
into its authenticated digest.

### Acceptance criteria

- GIVEN autoridad `deterministic_observation` WHEN se confirma THEN cada
  evidence reference existe, está verificada, pertenece al mismo proyecto/scope
  y su digest queda ligado al record.
- GIVEN aprobación humana o configuración confiable WHEN se confirma THEN actor,
  authority source, decision ID, instante y digest de política quedan auditados.
- GIVEN procedencia incompleta, actor incompatible, commit/config ausentes o
  evidencia de otro run/proyecto WHEN se valida THEN no persiste.
- GIVEN `confidence=high` sin autoridad suficiente WHEN se consulta THEN no se
  trata como approved ni obtiene permisos adicionales.

## R5 — Validez temporal, revisión y staleness

WHEN a memory is evaluated at a trusted `as_of`, THE system SHALL exclude it if
it is outside its validity window or stale under the exact revision,
configuration and evidence policy of the query.

### Acceptance criteria

- GIVEN `as_of < valid_from` o `as_of >= valid_until` WHEN se consulta THEN el
  recuerdo no es elegible; al expirar se añade el evento terminal idempotente.
- GIVEN cambio de commit/configuration digest para un recuerdo que los vincula
  exactamente WHEN se consulta THEN se declara stale y no se aplica por
  similitud textual.
- GIVEN evidencia ausente, revocada, corrupta o no verificable WHEN es obligatoria
  por clase/política THEN queda stale y nunca aprobado de facto.
- GIVEN reloj retrocedido o `as_of` aportado por caller no confiable WHEN se
  consulta THEN se usa el reloj/configuración confiable y se rechaza el intento
  de ampliar validez.

## R6 — Propuestas del agente y defensa contra memory poisoning

WHEN an agent proposes memory, THE system SHALL treat every field as untrusted
data, apply allowlisted schema and policy, and preserve it only as a bounded
proposal without operational authority.

### Acceptance criteria

- GIVEN texto que intenta cambiar instrucciones, autoridad, scope, estado,
  política o capacidades WHEN se propone THEN permanece contenido etiquetado o
  se rechaza; nunca altera control flow.
- GIVEN una propuesta duplicada con la misma semantic identity e idempotency key
  WHEN se redelivera THEN converge a la misma versión sin evento adicional.
- GIVEN la misma key con payload distinto WHEN se reintenta THEN se rechaza como
  conflicto y no sustituye la primera propuesta.
- GIVEN una propuesta válida WHEN se proyecta para revisión THEN declara
  `agent_proposal`, fuente no confiable y no se mezcla con instrucciones del
  sistema.

## R7 — Rechazo de secretos antes de persistir o indexar

BEFORE a memory payload, provenance field or derived index crosses a durable
boundary, THE system SHALL reject secret-bearing or prohibited content using
configured names, paths, values, shapes and canaries.

### Acceptance criteria

- GIVEN token, cookie, credential, private key, connection string, variable
  sensible, body sin redactar, prompt completo, chain of thought o canario en
  cualquier profundidad WHEN se valida THEN journal, snapshot e índice quedan
  byte-for-byte sin ese valor.
- GIVEN un error externo que contiene un secreto WHEN se devuelve o audita THEN
  solo se expone categoría, stage y path generados por el sistema.
- GIVEN sensibilidad `restricted` WHEN se persiste contenido seguro THEN solo
  consumidores configurados pueden obtener su proyección; la etiqueta no permite
  almacenar secretos.
- GIVEN un detector cancelado o fallido WHEN se intenta escribir THEN falla
  cerrado antes de reservar secuencia o publicar un temporal.

## R8 — MemoryStore, journal y snapshots verificables

WHEN a memory mutation commits, THE `MemoryStore` SHALL append an authenticated,
hash-chained event and atomically publish an authenticated snapshot under the
configured `.featureproof/memory` root without coupling domain code to files.

### Acceptance criteria

- GIVEN un commit exitoso WHEN se reinicia THEN journal y snapshot reconstruyen
  exactamente versiones, estados, secuencia y head digest.
- GIVEN evento borrado, insertado, reordenado, truncado o alterado, snapshot
  manipulado, authenticator inválido o key ID no confiable WHEN se abre THEN el
  store falla cerrado y no devuelve memoria.
- GIVEN fallo antes del rename atómico WHEN se recupera THEN conserva el último
  head confirmado y limpia solo temporales propios verificables.
- GIVEN journal válido y snapshot ausente/antiguo WHEN se recupera THEN lo
  reconstruye y autentica sin cambiar eventos históricos.

## R9 — CAS, idempotencia, concurrencia y recovery

WHEN writers mutate one project memory stream, THE store SHALL serialize the
transition with expected sequence/version CAS and idempotency identity so retries,
concurrent owners and restart converge without lost or duplicate events.

### Acceptance criteria

- GIVEN dos writers con el mismo expected version WHEN compiten THEN exactamente
  uno confirma y el otro recibe conflicto tipado antes de sobrescribir.
- GIVEN retry/redelivery idéntico tras resultado incierto WHEN se recupera THEN
  devuelve el commit ya confirmado; no crea una segunda versión.
- GIVEN misma idempotency key con distinto project, transition o digest WHEN se
  recibe THEN falla cerrado.
- GIVEN cancelación en lock, append, sync, snapshot o publish WHEN retorna THEN
  no queda lock/worker vivo y recovery observa el último boundary durable sin
  aceptar un estado parcial.

## R10 — Confinamiento del adapter local

WHEN the local adapter resolves storage paths, THE system SHALL derive them only
from a configured canonical root and encoded system identifiers and SHALL reject
symlinks, reparse points, traversal, aliases and objects outside that root.

### Acceptance criteria

- GIVEN project/memory IDs con separadores, `..`, Unicode ambiguo, nombres de
  dispositivo o rutas absolutas WHEN se resuelven THEN no controlan paths y se
  rechazan o codifican de forma canónica.
- GIVEN symlink/reparse point en root, proyecto, journal, snapshot, lock o
  temporal WHEN se abre o publica THEN falla antes de leer/escribir el destino.
- GIVEN cambio TOCTOU de un componente o hardlink de un archivo mutable WHEN se
  verifica/publica THEN la operación falla cerrada y no sale del root.
- GIVEN permisos inseguros o un objeto inesperado WHEN se compone el store THEN
  no se sirve memoria y el error no revela contenido.

## R11 — Query determinista e índice derivado

WHEN memory is queried, THE reader SHALL apply project, scope, revision, state,
authority, validity, sensitivity and class filters before returning a bounded,
stably ordered detached result; no index SHALL grant eligibility.

### Acceptance criteria

- GIVEN recuerdos mezclados WHEN se consulta THEN solo los elegibles se ordenan
  por `(class, subject, predicate, valid_from, id, version)` y se truncan por
  unidades completas bajo límites de items/bytes/work.
- GIVEN `proposed`, terminales o una autoridad/sensibilidad no admitida WHEN se
  hace una consulta operacional THEN no aparecen; una consulta de revisión debe
  pedirlos explícitamente y estar autorizada.
- GIVEN índice ausente, corrupto u obsoleto WHEN se consulta THEN se reconstruye
  desde records verificados o falla cerrado; nunca cambia elegibilidad ni orden.
- GIVEN recuerdos elegibles contradictorios para la misma clave WHEN se consulta
  THEN devuelve un conflicto tipado con todos sus IDs y no elige ganador.

## R12 — Uso histórico inmutable e integración acotada

WHEN planning or a functional flow consumes memory, THE workflow SHALL seal the
exact memory IDs, versions, digests, query digest and store sequence into a
historical binding and SHALL revalidate it before approval or execution.

### Acceptance criteria

- GIVEN memoria invalidada, sustituida o expirada después del binding pero antes
  de aprobar/ejecutar WHEN se revalida THEN el trabajo nuevo se detiene como
  stale; no cambia silenciosamente a otra memoria.
- GIVEN un run ya terminal WHEN cambia la memoria actual THEN su binding,
  journal, plan, evidencias y resultado histórico permanecen iguales y el lector
  puede recuperar las versiones exactas para auditoría.
- GIVEN una observación confirmada del functional flow WHEN se propone como
  memoria THEN solo un trusted observer puede convertir la evidencia verificada
  en `observed`; el resultado funcional no cambia.
- GIVEN planning/model integration WHEN opera THEN recibe/produce contratos
  tipados sin handle de store, secretos, autoridad ejecutable ni prompt assembly;
  la selección por rol se reserva para feature 29.

## R13 — Servicio compartido y proyecciones seguras

WHERE a CLI, API, MCP or GitHub adapter exposes memory operations, THE adapter
SHALL invoke the same application service, trusted principal mapping and safe
projection without accepting raw authority, filesystem paths or integrity keys.

### Acceptance criteria

- GIVEN la misma operación autorizada por dos superficies WHEN se ejecuta THEN
  comparte validación, CAS, idempotencia, estado y códigos de error; no existe un
  atajo de adapter.
- GIVEN list/get/propose/review/invalidate/supersede WHEN responde THEN solo
  incluye campos allowlisted, procedencia segura, IDs/digests y conflictos; no
  incluye payload externo crudo, evidence bodies, secretos ni authenticators.
- GIVEN un comentario GitHub o llamada MCP que declara ser humana/confiable WHEN
  no existe mapping autenticado y allowlisted THEN no aprueba, invalida ni
  sustituye memoria.
- GIVEN una superficie sin autoridad administrativa configurada WHEN se usa THEN
  puede quedar read-only o no exponer la operación, pero no cambia la semántica.

## R14 — Límites, cancelación, retries y observabilidad segura

WHILE memory work is active, THE service SHALL enforce trusted finite limits,
propagate cancellation through every blocking boundary and retry only classified
transient local operations within one owned scope.

### Acceptance criteria

- GIVEN límites de records, bytes, profundidad, eventos, query work, retries o
  duración agotados WHEN se opera THEN termina con categoría estable y no inicia
  otra unidad de trabajo.
- GIVEN cancelación en validación, detector, evidencia, store, índice o query
  WHEN retorna la frontera THEN no persiste trabajo posterior ni deja goroutines,
  locks o timers vivos.
- GIVEN corrupción, conflicto CAS, policy denial, secret detection o input
  inválido WHEN se clasifica THEN no se reintenta.
- GIVEN observabilidad WHEN registra una operación THEN incluye project, memory,
  version, transition, actor seguro y outcome, nunca contenido o errores externos
  crudos.

## R15 — Verificación hermética, trazabilidad y comentarios sistémicos

WHEN the feature is verified, THE test suite SHALL exercise every requirement
without real models, credentials, network or external accounts, SHALL cover at
least 85% of every new or modified production statement across build-tagged
platform files, and SHALL preserve systemic-responsibility comments on every new
or modified entity, constant, variable, function, method and test helper.

### Acceptance criteria

- GIVEN el mapa R→test WHEN se revisa THEN cada R1–R15 enlaza al menos un test
  concreto y resultado reproducible.
- GIVEN ejecuciones repetidas y `-race` WHEN se ejecutan THEN proposal,
  transitions, staleness, concurrency, crash recovery, corruption, poisoning,
  canaries y superficies permanecen deterministas.
- GIVEN fuentes condicionados por Windows/Unix WHEN se calcula coverage del diff
  THEN el denominador incluye ambos y el cociente sin redondear es >=85%.
- GIVEN un símbolo nuevo o modificado, incluso interno o de test WHEN se audita
  THEN tiene un comentario que explica su responsabilidad dentro del sistema.

## Errores y casos límite

- IDs, enums, scopes, actores, OIDs, digests, fechas o valores inválidos se
  rechazan antes de persistencia.
- Una contradicción es resultado tipado de query, no una elección automática.
- La pérdida de autoridad, evidencia o validez nunca degrada a un scope menor ni
  conserva elegibilidad por conveniencia.
- Un fallo del store, integridad, detector o autenticación es operativo; nunca
  fabrica memoria aprobada ni cambia un veredicto funcional.
- Un snapshot es una optimización verificable; el journal autenticado sigue
  siendo la historia causal y no se corrige editando archivos.
- Los retries reutilizan identidad y digest originales; no generan nuevos IDs
  para esconder incertidumbre.

## Decisiones abiertas

No hay decisiones de producto abiertas para iniciar la implementación. Los
algoritmos concretos de autenticación y locking pueden elegirse durante la
implementación si mantienen keys fuera del store, verificación fail-closed,
compatibilidad Windows/Unix y todos los comportamientos anteriores.

## Aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-23T13:23:48+02:00
```

La implementación no puede comenzar hasta que una persona apruebe explícitamente
esta especificación y el leader registre la transición a `in_progress`.
