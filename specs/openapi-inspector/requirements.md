# Inspector de contratos OpenAPI — Requirements

## Objetivo y problema

FeatureProof necesita convertir un contrato OpenAPI local y no confiable en una
vista interna acotada que permita descubrir operaciones, conocer su seguridad y
validar respuestas HTTP de forma determinista. El inspector debe impedir que la
resolución de referencias amplíe el ámbito local autorizado y debe producir un
contexto reducido que una feature posterior pueda entregar a un modelo sin
confundir contenido documental con instrucciones.

## Alcance

- Cargar un documento raíz OpenAPI 3.0.x o 3.1.x en JSON o YAML desde una ruta
  situada dentro de una raíz local autorizada explícitamente.
- Resolver referencias internas y referencias a otros archivos situados dentro
  de esa misma raíz con límites explícitos.
- Validar y normalizar operaciones, parámetros, request bodies, respuestas,
  schemas y requisitos de seguridad.
- Enumerar un catálogo determinista de operaciones.
- Validar una respuesta HTTP observada contra una operación seleccionada.
- Construir contexto acotado para una selección explícita de operaciones
  asociada por el caller a las restricciones solicitadas.
- Clasificar errores y violaciones con ubicaciones estables, sin incluir el
  documento ni valores de la respuesta en diagnósticos.
- Verificar todo el comportamiento con archivos temporales y sin red.

## Fuera de alcance

- Descargar contratos o referencias mediante HTTP, HTTPS u otro protocolo.
- Inferir mediante lenguaje natural qué operación corresponde a una
  restricción. La feature de generación de planes será responsable de elegir
  operaciones a partir del catálogo.
- Modificar el formato YAML de specification, el manifiesto de planes o la CLI
  `validate` existente.
- Validar requests HTTP, callbacks, links, webhooks o ejecutar operaciones.
- Leer, solicitar, resolver o aplicar credenciales reales.
- Entregar ejemplos, extensiones `x-*`, descripciones libres o `externalDocs`
  al contexto destinado al modelo.
- Generar planes, assertions o veredictos de constraints.

## Actores y términos

- **Caller:** componente interno que autoriza una raíz, carga un contrato,
  selecciona operaciones y consume resultados.
- **Authorized root:** directorio canónico que contiene el documento raíz y
  todos los archivos que pueden resolverse.
- **Operation key:** identidad estable formada por método HTTP normalizado y
  path template exacto. `operationId` es metadata y no sustituye esta clave.
- **Effective security:** seguridad específica de la operación o, si no existe,
  la seguridad global; una lista vacía explícita significa acceso anónimo.
- **Observed response:** status, headers y body acotados que el caller entrega
  al validador; el inspector no realiza la petición.
- **Violation:** incumplimiento determinista del contrato por una respuesta.
- **Reduced context:** representación canónica y limitada de las operaciones
  seleccionadas y los schemas alcanzables desde ellas.

## Contrato de selección y reducción

El inspector expone dos pasos separados. Primero enumera un catálogo compacto
de todas las operaciones válidas. Después el caller solicita contexto mediante
restricciones identificadas y una lista explícita de `Operation key` por cada
una. El inspector comprueba que todas las claves existen y solo incluye esas
operaciones. No intenta deducir relevancia desde texto libre.

La representación reducida usa una lista blanca de datos estructurales:
método, path, `operationId`, parámetros, request body, respuestas, effective
security y keywords de schema necesarias para describir tipos y validación.
Siempre excluye ejemplos, extensiones, descripciones libres y documentación
externa. Un bloque de omisiones dentro del mismo límite indica categorías y
cantidades descartadas. Si una operación completa no cabe, se omite como
unidad y se contabiliza bajo `size_limit`; nunca se publica un schema parcial
como si estuviera completo.

## R1 — Carga local versionada

WHEN a caller supplies an authorized root and an entry file, THE inspector SHALL
load exactly one JSON or YAML OpenAPI document whose declared version belongs to
3.0.x or 3.1.x, and SHALL validate it before returning an inspectable contract.

### Acceptance criteria

- GIVEN documentos equivalentes OpenAPI 3.0.x y 3.1.x en JSON y YAML WHEN se
  cargan THEN producen el mismo catálogo normalizado y no exponen DTOs del
  parser externo.
- GIVEN versión ausente, mal formada, 2.x o distinta de 3.0/3.1 WHEN se carga
  THEN se devuelve `unsupported_version` o `invalid_document` con ubicación y
  no se devuelve un contrato parcial.
- GIVEN JSON/YAML inválido, mappings duplicados o estructura OpenAPI inválida
  WHEN se carga THEN se rechaza antes de enumerar o validar respuestas.

## R2 — Ámbito y límites de referencias

WHEN the document contains a reference, THE inspector SHALL resolve internal
references and local file references only when their canonical targets remain
inside the authorized root and all configured document, byte, depth and
reference-count limits remain available.

### Acceptance criteria

- GIVEN `$ref` interno o a archivo hermano dentro de la raíz WHEN resuelve THEN
  el elemento referenciado participa en operaciones y validación.
- GIVEN referencia HTTP(S), esquema no local, path absoluto, traversal, symlink
  de escape o target fuera de raíz WHEN se encuentra THEN se devuelve
  `reference_denied` antes de leer el target prohibido y nunca se accede a red.
- GIVEN referencia rota, ciclo no resoluble o límite agotado WHEN se resuelve
  THEN se devuelve una categoría estable con la ubicación del `$ref`, sin loop,
  stack overflow ni contrato parcial.

## R3 — Catálogo determinista de operaciones

WHEN a valid contract is inspected, THE inspector SHALL enumerate every HTTP
operation in deterministic path-and-method order with its operation key,
optional operationId, effective parameters, request body, responses and
associated schemas.

### Acceptance criteria

- GIVEN parámetros a nivel de path y operación WHEN se enumeran THEN se aplica
  la sobrescritura OpenAPI por pareja `name`/`in` y el resultado no contiene
  duplicados ambiguos.
- GIVEN request body, respuestas por status/default y schemas referenciados
  WHEN se inspecciona THEN el descriptor permite localizar sus formas
  normalizadas sin reabrir archivos.
- GIVEN dos cargas equivalentes WHEN se enumeran THEN el orden y las operation
  keys son idénticos con independencia del orden de maps del decoder.

## R4 — Seguridad efectiva sin credenciales

WHEN operations are normalized, THE inspector SHALL expose each operation's
effective security alternatives and referenced security-scheme metadata
without reading or representing credential values.

### Acceptance criteria

- GIVEN seguridad global sin override WHEN se inspecciona una operación THEN
  hereda las alternativas globales.
- GIVEN seguridad específica, lista vacía o alternativa vacía WHEN se
  inspecciona THEN se preservan respectivamente override, acceso anónimo y la
  semántica OR/AND de OpenAPI con scopes ordenados.
- GIVEN apiKey, HTTP, OAuth2 u OpenID Connect WHEN se describe THEN solo se
  incluyen tipo, ubicación/nombre, scheme, flows/scopes y URLs contractuales
  necesarias; no se consulta entorno, headers, ficheros de secretos ni red.

## R5 — Diagnósticos seguros y localizables

IF loading, parsing, reference resolution or contract validation fails, THEN THE
inspector SHALL return a typed category and a stable document location while
preserving the cause and excluding source payloads, response values and
credentials from the diagnostic text.

### Acceptance criteria

- GIVEN un schema inválido o referencia rota anidada WHEN falla THEN el error
  identifica archivo relativo y JSON Pointer o posición estructural afectada.
- GIVEN nombre, body o ejemplo con secreto canario WHEN aparece un error THEN
  category, location y texto serializado no contienen el valor canario.
- GIVEN un error distinguible WHEN el caller lo recibe THEN puede usar tipos o
  categorías, sin comparar texto.

## R6 — Selección determinista de respuesta

WHEN a caller validates an observed response for an existing operation key, THE
inspector SHALL select the response contract by exact status first, status-class
range second and `default` last, or return a response violation when none
matches.

### Acceptance criteria

- GIVEN respuestas `200`, `2XX` y `default` WHEN llega status 200 THEN gana
  `200`; para 201 gana `2XX`; otro status usa `default`.
- GIVEN una operation key desconocida o status fuera de 100..599 WHEN se
  solicita THEN se devuelve error de input antes de validar body.
- GIVEN status sin respuesta exacta, de clase ni default WHEN se valida THEN el
  resultado es no conforme con violación `status_not_declared`, no un error
  interno ni un passed.

## R7 — Validación de headers y body

WHEN a response contract is selected, THE inspector SHALL validate declared
response headers case-insensitively and validate the observed body against the
schema selected by its media type using the semantics of the document's
OpenAPI version.

### Acceptance criteria

- GIVEN header de respuesta requerido ausente o presente con tipo/formato
  inválido WHEN se valida THEN se emite una violación localizada; headers no
  declarados no invalidan la respuesta.
- GIVEN media type exacto o wildcard compatible y body válido WHEN se valida
  THEN no se emiten violaciones de body; parámetros de `Content-Type` no
  cambian el media type base.
- GIVEN body mal formado, media type no declarado o valor contrario al schema
  WHEN se valida THEN se emiten violaciones ordenadas con keyword/location,
  nunca el body ni el valor observado.
- GIVEN body que supera el límite configurado WHEN se recibe THEN se devuelve
  `limit_exceeded` sin intentar validar un prefijo como documento completo.

## R8 — Contexto relevante por selección explícita

WHEN a caller requests reduced context for identified constraints and explicit
operation keys, THE inspector SHALL reject unknown keys and SHALL include only
the selected operations, their effective security and schema fragments
reachable from their parameters, request bodies and responses.

### Acceptance criteria

- GIVEN un contrato con operaciones seleccionadas y no seleccionadas WHEN se
  construye contexto THEN ninguna identidad, path, metadata ni schema exclusivo
  de las no seleccionadas aparece en la salida.
- GIVEN schemas compartidos WHEN son alcanzables desde varias seleccionadas
  THEN aparecen una sola vez con referencias internas consistentes y orden
  estable.
- GIVEN la misma selección en distinto orden de maps WHEN se construye THEN los
  bytes canónicos y el vínculo constraint → operation key son reproducibles.

## R9 — Límite y omisiones explícitas de contexto

WHEN reduced context is built, THE inspector SHALL keep its canonical encoded
size within the configured maximum and SHALL include bounded omission metadata
for examples, extensions, free-form documentation, unselected content and
whole operations excluded by the size limit.

### Acceptance criteria

- GIVEN examples, `x-*`, descriptions o `externalDocs` con secretos/instrucciones
  WHEN se genera contexto THEN esos valores no aparecen y sus categorías se
  contabilizan como omitidas.
- GIVEN una operación seleccionada cuyo bloque completo no cabe WHEN se reduce
  THEN no aparece un fragmento ambiguo de esa operación y aumenta el contador
  `size_limit` dentro del mismo límite.
- GIVEN un límite inferior al envelope mínimo WHEN se configura THEN el
  inspector se rechaza antes de cargar documentos.

## R10 — Cancelación, aislamiento e inmutabilidad

WHILE loading, resolving, validating or reducing a contract, IF the context is
cancelled, THEN THE inspector SHALL stop without returning a partial contract,
validation result or reduced context; independent inspections SHALL NOT share
mutable document state.

### Acceptance criteria

- GIVEN cancelación antes o durante cada recorrido acotado WHEN se observa THEN
  se devuelve la causa del contexto y no comienza otra lectura o validación.
- GIVEN dos inspecciones concurrentes con roots distintos WHEN se ejecutan THEN
  no comparten cache mutable ni referencias y pasan el race detector.
- GIVEN descriptors o contexto devueltos WHEN el caller modifica sus slices o
  maps THEN no altera el contrato retenido ni resultados posteriores.

## R11 — Fronteras sustituibles y pruebas sin red

THE inspector SHALL receive filesystem access and all limits explicitly so its
complete behavior can be tested with local fixtures and fault injection without
Internet, production services, credentials or a model.

### Acceptance criteria

- GIVEN un filesystem controlado WHEN corre la suite THEN cubre JSON/YAML,
  versiones, parámetros, schemas, seguridad, referencias, respuestas, límites,
  cancelación y reducción.
- GIVEN cualquier contrato o `$ref` WHEN se prueban THEN no se crea cliente de
  red ni se resuelve una URL remota.
- GIVEN fallo de lectura inyectado WHEN ocurre THEN se conserva su causa bajo un
  error tipado y no se devuelve estado parcial.

## Errores y casos límite

- Los paths templates y nombres son sensibles a mayúsculas; los métodos se
  normalizan a mayúsculas y los headers se comparan sin distinguir casing.
- `operationId` puede faltar. Un documento con duplicados viola OpenAPI y se
  rechaza; la identidad interna sigue siendo método + path.
- Una referencia con fragmento debe resolver tanto el archivo como el JSON
  Pointer; escapes `~0` y `~1` siguen RFC 6901.
- Una referencia repetida cuenta una vez como documento leído y cada intento de
  resolución cuenta contra el límite de referencias.
- La validación de response no persiste evidencia ni calcula el estado de una
  constraint; devuelve observaciones deterministas al caller.
- Un response sin body solo se valida contra las obligaciones expresables por
  el response/schema seleccionado. El inspector no inventa obligatoriedad que
  OpenAPI no declare.
- Los campos `readOnly` y `writeOnly` se interpretan en dirección response.
- Formatos no afirmados por el validador se conservan como annotations y no se
  presentan como comprobados.
- Las violations se ordenan por location, keyword y category para evitar que el
  recorrido de maps afecte a la salida.
- El source OpenAPI completo y los observed responses son datos no confiables;
  nunca se convierten en instrucciones o mensajes de sistema.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. La selección explícita
de operaciones separa la reducción determinista de la futura selección
agéntica. Integrar un flag OpenAPI en la CLI o inferir relevancia pertenece a
features posteriores y requiere su propio contrato aprobado.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-14T17:46:38+02:00
```
