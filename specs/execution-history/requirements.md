# Historial y comparación de validaciones — Requirements

## Objetivo y problema

FeatureProof conserva runs, bindings, journals, paquetes de evidencia y
relaciones de corrección, pero todavía no ofrece una vista histórica unificada,
consultable y comparable. Esta feature incorpora una proyección de terminales
confirmados que permite localizar validaciones anteriores, inspeccionar sus
hechos seguros, comparar dos revisiones y calcular agregados deterministas sin
convertir texto del modelo, logs o artefactos no verificados en fuente de
verdad.

## Alcance

- Consulta autorizada por organización y proyecto con filtros exactos de
  repositorio, pull request, commit, feature, resultado y periodo UTC.
- Entrada histórica acotada con revisión, entorno, restricciones, plan
  aprobado, resultado, duración, referencias de evidencia, uso confirmado del
  modelo y lineage de correcciones.
- Paginación keyset estable mediante cursores sellados, scopeados y
  expirables.
- Comparación determinista de dos runs o de los terminales resueltos para dos
  commits dentro del mismo proyecto, repositorio y feature.
- Agregados estructurados de fallos, duración, estabilidad y uso del modelo.
- Coste monetario opcional únicamente cuando llega como atribución verificable
  del proveedor o de la composición.
- Resolución de evidencias por el lector verificado de paquetes, distinguiendo
  disponibilidad, expiración, ausencia e integridad fallida.
- Persistencia local y PostgreSQL con contratos equivalentes, aislamiento
  tenant, RBAC, retención y límites de tamaño/trabajo.

## Fuera de alcance

- Una UI, dashboard concreto, exportación BI, alertas o ejecución periódica.
- Recalcular assertions, veredictos, planes o relaciones de corrección desde
  texto, prompts, logs o respuestas del modelo.
- Reejecutar una validación, iniciar una corrección o modificar un run desde el
  historial.
- Facturación, invoices, conversión de divisas, impuestos, presupuestos
  monetarios o una tabla de precios embebida en core.
- Recuperar un paquete eliminado, prolongar su retención o cambiar un resultado
  histórico porque su evidencia ya no esté disponible.
- Búsqueda textual libre, filtros aproximados o comparación entre tenants,
  proyectos o repositorios distintos.
- Definir un protocolo HTTP, una UI o un proveedor de analytics externo; esta
  entrega expone casos de uso consumer-owned reutilizables por superficies
  autorizadas.

## Actores y términos

- **Lector autorizado:** principal autenticado cuya capability vigente permite
  leer historial para una organización y proyecto concretos.
- **Entrada histórica:** proyección segura e inmutable de un run terminal
  confirmado; no es una copia mutable del agregado operativo.
- **Terminal confirmado:** resultado durable `passed`, `failed`,
  `inconclusive`, `cancelled` o `error` cuya identidad, binding y paquete o
  terminal operativo han sido confirmados por la frontera productora.
- **Snapshot de consulta:** instante fijado en la primera página que impide que
  inserciones posteriores alteren las páginas ya iniciadas.
- **Selector de comparación:** exactamente un `run_id` o un `commit_oid`; un
  commit se resuelve al terminal más reciente dentro de los mismos filtros y
  snapshot.
- **Identidad de restricción:** `constraint_id` más su huella semántica
  confirmada cuando exista; nunca texto descriptivo ni posición en un array.
- **Atribución monetaria:** cantidad decimal exacta, moneda, fuente,
  provenance/digest y estado confirmado aportados por proveedor o composición.
- **Estado de evidencia:** `available`, `expired`, `missing` o `invalid`; es
  independiente del resultado histórico.

## R1 — Autorización y scope antes de consultar

WHEN un principal solicita historial, comparación, agregados o evidencia, THE
history service SHALL resolver identidad y autorizar la acción cerrada para la
organización y proyecto exactos antes de consultar stores, abrir cursores o
revelar existencia.

### Acceptance criteria

- GIVEN una capability vigente de lectura WHEN consulta su proyecto THEN solo
  recibe datos de ese scope.
- GIVEN principal ausente, revocado, expirado o sin acción de lectura WHEN
  consulta THEN se rechaza antes de invocar store o package reader.
- GIVEN run, cursor, commit o evidence ID de otro tenant/proyecto WHEN se usa
  THEN la respuesta pública no revela si existe ni diferencias de counts.
- GIVEN cambio de membership/policy después de la primera página WHEN continúa
  THEN se reautoriza y una capability obsoleta no conserva acceso.

## R2 — Filtros exactos y periodo bien definido

WHEN se lista historial, THE service SHALL aplicar organización y proyecto
obligatorios y filtros opcionales exactos por repositorio, pull request,
commit, feature, resultado y periodo UTC semiabierto `[from, to)`, rechazando
combinaciones inválidas antes de almacenamiento.

### Acceptance criteria

- GIVEN runs que difieren en cada dimensión WHEN se aplica un filtro o una
  combinación THEN solo aparecen coincidencias exactas.
- GIVEN `from == to`, `from > to`, resultado desconocido, ID no canónico o
  periodo que excede el máximo configurado WHEN se valida THEN falla sin query.
- GIVEN un límite de página omitido, cero o superior al máximo WHEN se consulta
  THEN se rechaza; el core no inventa un page size permisivo.
- GIVEN timestamps en distintos offsets WHEN se reciben THEN se normalizan a
  UTC y conservan la semántica `[from, to)` sin depender del locale.

## R3 — Proyección histórica completa e inmutable

WHEN un run alcanza un terminal confirmado, THE projector SHALL confirmar de
forma idempotente una entrada que vincule run, revisión, entorno, restricciones,
plan aprobado, resultado, duración, evidencias, uso y correcciones, sin
reescribirla por consultas o cambios posteriores.

### Acceptance criteria

- GIVEN terminal funcional WHEN se proyecta THEN la entrada contiene binding
  de repositorio/commit/entorno, feature, tiempos, resultado, restricciones y
  referencias a plan/evidencia verificables.
- GIVEN terminal operativo sin paquete funcional WHEN se proyecta THEN conserva
  su categoría terminal y campos disponibles, y marca ausencias explícitas sin
  fabricar restricciones o evidencia.
- GIVEN replay con misma identidad y digest WHEN se confirma THEN devuelve la
  misma recepción; mismos IDs con bytes distintos producen conflicto de
  integridad.
- GIVEN corrección confirmada después del run padre WHEN se vincula THEN se
  añade mediante una relación append-only idempotente, sin mutar el resultado,
  binding ni hechos del run.

## R4 — Contenido seguro derivado de fuentes autoritativas

THE history projector SHALL construir campos funcionales únicamente desde
bindings, eventos/proyecciones durables, aprobaciones, relaciones de corrección
y paquetes verificados, SHALL aplicar proyecciones allowlisted y SHALL NOT usar
texto del modelo, conclusiones narrativas, logs o payloads no saneados como
hechos.

### Acceptance criteria

- GIVEN texto que afirma `passed` frente a assertions deterministas `failed`
  WHEN se proyecta THEN el historial conserva `failed`.
- GIVEN plan aprobado WHEN se muestra THEN expone solo IDs, digest, pasos,
  herramientas/assertions seguras y binding de aprobación verificados; no
  inputs secretos ni texto libre no confiable.
- GIVEN canaries en prompts, headers, URLs, provider payloads, errores o
  artifacts WHEN se inspeccionan entrada, errores y observabilidad THEN no
  aparecen.
- GIVEN referencia/digest/lineage inconsistente WHEN se proyecta o carga THEN
  falla cerrado y no devuelve una entrada parcial como válida.

## R5 — Orden y paginación estables

WHEN se pagina una consulta, THE store SHALL usar orden total
`terminal_at DESC, run_id DESC`, keyset y un cursor autenticado ligado al scope,
filtros, snapshot, posición, versión y expiración.

### Acceptance criteria

- GIVEN varias filas con el mismo timestamp WHEN se recorren páginas THEN cada
  run aparece exactamente una vez en orden por `run_id`.
- GIVEN nuevos terminales después de la primera página WHEN se continúa THEN no
  desplazan, duplican ni omiten filas del snapshot fijado.
- GIVEN cursor alterado, expirado, de otro filtro/scope/versión o demasiado
  grande WHEN se abre THEN se rechaza antes de query y sin fallback a página 1.
- GIVEN backends local y PostgreSQL con el mismo dataset WHEN se pagina THEN
  producen las mismas páginas y cursores semánticamente equivalentes.

## R6 — Selección determinista de los dos lados de una comparación

WHEN se solicita comparar, THE comparison service SHALL exigir dos selectores,
resolver cada `run_id` exactamente o cada `commit_oid` al terminal más reciente
por el orden canónico dentro del mismo scope, repositorio, feature, filtros y
snapshot, y SHALL rechazar lados ambiguos o incompatibles.

### Acceptance criteria

- GIVEN dos run IDs autorizados y compatibles WHEN se resuelven THEN se usan
  exactamente esos runs aunque existan terminales posteriores.
- GIVEN un commit con varios runs elegibles WHEN se resuelve THEN gana el
  primero por `terminal_at DESC, run_id DESC` dentro del snapshot.
- GIVEN commit sin terminal elegible WHEN se compara THEN devuelve `not_found`
  seguro, no el run activo ni uno de otro filtro.
- GIVEN lados de organización, proyecto, repositorio o feature diferentes WHEN
  se comparan THEN se rechazan antes de leer paquetes.

## R7 — Clasificación determinista de restricciones

WHEN ambos lados están resueltos, THE comparator SHALL unir restricciones por
identidad estructural, conservar los dos estados y clasificarlas exactamente
una vez como `corrected`, `persistent`, `new`, `retired` o `inconclusive`, sin
usar el modelo.

### Acceptance criteria

- GIVEN misma identidad/huella con `failed -> passed` WHEN se compara THEN es
  `corrected`.
- GIVEN misma identidad/huella en ambos lados con cualquier otra pareja de
  estados concluyentes WHEN se compara THEN es `persistent` y expone ambos
  estados, incluida una regresión `passed -> failed` sin ocultarla.
- GIVEN identidad solo en el lado nuevo WHEN se compara THEN es `new`; solo en
  el lado base THEN es `retired`.
- GIVEN estado `inconclusive`, identidad duplicada/ambigua, huella semántica
  incompatible o paquete inválido WHEN se clasifica THEN la entrada o la
  comparación queda `inconclusive`/error tipado; nunca se fuerza una clase más
  favorable.
- GIVEN dos datasets iguales en distinto orden WHEN se comparan THEN producen
  el mismo orden canónico y digest.

## R8 — Agregados basados en hechos retenidos

WHEN se solicitan estadísticas, THE aggregation service SHALL calcular sobre
el conjunto autorizado, filtrado y retenido counts/rates estructurados de
resultados por restricción, duración y estabilidad, usando solo estados y
timestamps confirmados.

### Acceptance criteria

- GIVEN fallos repetidos de una restricción WHEN se agrega THEN devuelve
  observaciones, `failed`, `passed`, `inconclusive` y una razón exacta
  numerador/denominador; no un ranking derivado de texto.
- GIVEN duraciones confirmadas WHEN se agrega THEN devuelve count, sum, min y
  max en nanosegundos y percentiles mediante un algoritmo/versionado definido;
  overflow o valor negativo falla cerrado.
- GIVEN secuencia de observaciones comparables WHEN se mide estabilidad THEN
  informa transiciones de estado y pares comparables; runs retirados,
  inconclusive o sin identidad compatible no mejoran artificialmente la tasa.
- GIVEN datos fuera de retención, scope, filtros o snapshot WHEN se agrega THEN
  no participan; counts de otro tenant nunca son observables.

## R9 — Uso del modelo explícito y opcional

WHERE existen métricas confirmadas de modelo, THE history service SHALL mostrar
llamadas, tokens y duración con proveedor/modelo y provenance disponibles;
WHERE una dimensión no fue confirmada, SHALL representarla como ausente y SHALL
NOT inferirla.

### Acceptance criteria

- GIVEN usage confirmado por runtime/journal WHEN se consulta entrada o
  agregado THEN calls, tokens y duración conservan los valores exactos.
- GIVEN tokens parciales o provider sin usage confirmado WHEN se consulta THEN
  el campo afectado está ausente/unavailable, no vale cero estimado.
- GIVEN suma que desborda o contadores decrecientes/corruptos WHEN se agrega
  THEN falla con integridad y no publica el total.
- GIVEN múltiples proveedores/modelos WHEN se agrega THEN se agrupa por
  identidad confirmada o se mantiene separado; no se atribuye al default actual.

## R10 — Coste monetario solo con atribución verificable

WHERE proveedor o composición entrega una atribución monetaria confirmada, THE
history service SHALL exponer cantidad decimal exacta, moneda, fuente y
provenance; OTHERWISE SHALL omitir coste, y el core SHALL NOT contener precios,
consultar tarifas ni estimar coste desde tokens.

### Acceptance criteria

- GIVEN atribución válida ligada al run/usage digest WHEN se proyecta THEN se
  conserva exactamente y puede agregarse solo con moneda y semantics
  compatibles.
- GIVEN usage sin atribución WHEN se consulta THEN tokens/calls/duración siguen
  visibles y `cost` está ausente.
- GIVEN moneda, cantidad, fuente, digest o binding inválido WHEN se recibe THEN
  se rechaza antes de persistir.
- GIVEN búsqueda estática y tests del core WHEN se verifican THEN no existe
  rate table, precio por modelo ni multiplicación tokens×precio; diferentes
  monedas nunca se suman o convierten.

## R11 — Evidencia resuelta exclusivamente por el lector verificado

WHEN un lector autorizado abre una referencia de evidencia, THE evidence
resolver SHALL reautorizarla y usar el lector verificado del paquete ligado al
run/binding; SHALL devolver su disponibilidad separada del resultado histórico.

### Acceptance criteria

- GIVEN paquete íntegro y evidence ID declarado WHEN se resuelve THEN devuelve
  solo la proyección saneada y acotada verificada por manifest/digests.
- GIVEN tombstone de retención confirmado WHEN se resuelve THEN devuelve
  `expired`; GIVEN ausencia sin tombstone THEN `missing`.
- GIVEN paquete corrupto, foreign o evidence ID no inventariado WHEN se resuelve
  THEN devuelve `invalid`/integrity error sin bytes parciales.
- GIVEN evidencia expirada, ausente o inválida WHEN se vuelve a consultar la
  entrada THEN su resultado y clasificación histórica no cambian.

## R12 — Retención y referencias vivas

WHEN retención evalúa historial o paquetes, THE system SHALL aplicar la policy
tenant versionada, borrar solo terminales elegibles en lotes acotados y
preservar tombstones/lineage necesarios para distinguir expiración y mantener
integridad de comparaciones retenidas.

### Acceptance criteria

- GIVEN entrada aún dentro del periodo, protegida o referenciada por una
  corrección/comparación retenida WHEN se evalúa THEN no se elimina.
- GIVEN entrada elegible WHEN se elimina THEN intent, fencing, audit y tombstone
  convergen ante restart/unknown result sin borrar otro scope.
- GIVEN package/evidence expira antes que metadata histórica WHEN se consulta
  THEN la entrada continúa y muestra `expired`.
- GIVEN metadata histórica ya fuera de retención WHEN lista/agrega THEN deja de
  aparecer en snapshots nuevos sin reescribir snapshots ya materializados.

## R13 — Límites, cancelación y trabajo acotado

THE history services SHALL validar límites composition-owned de página,
periodo, cardinalidad, bytes, comparación, agregación y package read, y SHALL
propagar `context.Context` por toda operación bloqueable.

### Acceptance criteria

- GIVEN límite N y N+1 para filas, restricciones, evidencias o grupos WHEN se
  procesa THEN N termina y N+1 se rechaza/trunca solo según contrato explícito,
  nunca consume memoria ilimitada.
- GIVEN cancelación antes o durante query, comparación, agregado o lectura de
  paquete WHEN ocurre THEN termina sin publicar resultado parcial como válido.
- GIVEN callback/reader lento WHEN opera THEN no se mantiene lock global ni
  transacción SQL abierta durante I/O externo.
- GIVEN runs/proyectos independientes WHEN consultan concurrentemente THEN
  progresan sin compartir estado mutable ni alterar el orden determinista.

## R14 — Equivalencia durable local/PostgreSQL

WHERE el backend activo es local o PostgreSQL, THE history store SHALL conservar
los mismos contratos de scope compuesto, append-only, idempotencia, orden,
cursores, filtros, snapshots, relaciones y retención, sin fallback o dual-write.

### Acceptance criteria

- GIVEN el mismo fixture WHEN se ejecuta la contract suite contra ambos
  backends THEN entradas, páginas, comparación y agregados son equivalentes.
- GIVEN restart WHEN se reabre THEN proyecciones, receipts, relaciones y
  snapshots continúan íntegros.
- GIVEN writer N y N+1 durante la ventana declarada WHEN comparten PostgreSQL
  THEN schema/migrations conservan compatibilidad; dirty/future/checksum falla
  antes de servir.
- GIVEN backend activo indisponible WHEN se consulta/proyecta THEN devuelve
  error tipado; no usa otro backend ni datos parciales.

## R15 — Errores y observabilidad seguros

IF una consulta falla, THEN THE service SHALL devolver categorías estables sin
comparar textos externos, y SHALL emitir observabilidad allowlisted con scope
no sensible, operación, resultado, counts acotados y duración, nunca contenido.

### Acceptance criteria

- GIVEN invalid request, unauthorized/hidden, not found, cursor, conflict,
  integrity, unavailable, limit, deadline o cancellation WHEN ocurre THEN se
  mapea a una categoría comprobable con `errors.Is/As`.
- GIVEN error del driver, reader, provider o paquete con canary WHEN se propaga
  THEN el mensaje público, logs y métricas no contienen el canary.
- GIVEN observer ausente, lento o con panic/error WHEN se notifica THEN no
  concede autoridad, no cambia el resultado y no rompe la transacción
  autoritativa.
- GIVEN métricas WHEN se etiquetan THEN no usan run, commit, PR, feature libre,
  cursor, principal o evidence ID como label de cardinalidad no acotada.

## R16 — Pruebas deterministas y documentación sistémica

THE implementation SHALL cubrir filtros, paginación, comparación, agregados,
coste opcional, retención, evidencia expirada y acceso cruzado denegado con
tests herméticos y contract/integration tests, y SHALL documentar el rol dentro
del sistema de cada entidad y función nueva o modificada.

### Acceptance criteria

- GIVEN la matriz de requirements WHEN se revisa THEN cada `R<n>` enlaza al
  menos un test concreto y ambos backends comparten contract fixtures.
- GIVEN tests de tiempo/concurrencia WHEN se ejecutan THEN usan clock y barreras
  controladas, sin `time.Sleep`, red real ni modelo real.
- GIVEN source Go tocado WHEN se audita mediante AST/revisión THEN cada entidad,
  función, método y helper de test nuevo o modificado tiene un comentario útil
  sobre su responsabilidad/invariante sistémica; los símbolos exportados además
  cumplen Go doc y los comentarios no se limitan a parafrasear el nombre.
- GIVEN gates de cierre WHEN se ejecutan THEN formato, vet, suite normal/race,
  PostgreSQL opt-in, builds aplicables, coverage/trazabilidad y auditoría de
  secretos/comentarios pasan según `docs/verification.md`.

## Errores y casos límite globales

- IDs, digests, timestamps, cantidades monetarias, monedas, filtros y cursores
  tienen formato y tamaño máximo; los duplicados y campos desconocidos fallan
  cerrado.
- Todos los slices/maps devueltos tienen ownership defensivo; una mutación del
  caller no altera store, cursor, comparación o agregado.
- Timestamps usan UTC y resolución definida; duración negativa, aritmética con
  overflow o reloj terminal anterior al inicio son integridad fallida.
- Conocer un run ID, commit, PR, cursor o evidence ID no concede acceso ni
  permite observar counts/timing diferenciados de otro tenant.
- Una evidencia no disponible afecta disponibilidad, nunca reevalúa un
  veredicto histórico ni convierte `failed`/`inconclusive` en `passed`.
- Un terminal operativo sin paquete no se mezcla con un fallo funcional en
  estadísticas de restricciones.
- Agregados vacíos devuelven counts cero y campos opcionales ausentes; no NaN,
  infinito, percentiles o rates inventados.
- Una comparación no persiste ni modifica los runs comparados; su cache
  opcional debe estar ligada a digests y puede descartarse sin perder verdad.

## Decisiones cerradas

- El orden canónico es `terminal_at DESC, run_id DESC`; la primera página fija
  snapshot y filtros en el cursor.
- Un selector de commit elige el terminal más reciente dentro del mismo scope,
  repositorio, feature, filtros y snapshot; un run selector nunca se sustituye.
- `persistent` significa que la misma restricción/semántica existe en ambos
  lados y no cumple el caso especial `failed -> passed`; ambos estados siempre
  se exponen, por lo que una regresión `passed -> failed` queda visible.
- El historial conserva resultado y hechos aunque package/evidence expire; la
  disponibilidad se informa por separado.
- El core acepta una atribución monetaria ya confirmada, pero no contiene
  precios ni calcula cantidades desde tokens. Los agregados monetarios se
  separan por moneda y provenance compatible.
- La fuente funcional es estructurada y verificable; texto narrativo y logs no
  participan en entries, comparación ni estadísticas.

## Decisiones abiertas

No quedan decisiones de producto necesarias para implementar esta feature.
TTL de cursor, tamaños de página, periodo máximo, límites de grupos/bytes,
retención y algoritmo/version de percentiles son configuración obligatoria,
versionada y validada por composición; no son defaults permisivos del core.

## Aprobación humana

```yaml
approved_by:
approved_at:
```
