# Fuentes de contrato independientes de OpenAPI — Requirements

## Objetivo y problema

FeatureProof necesita planificar validaciones a partir de contratos que pueden
proceder de OpenAPI, análisis estático, observaciones autorizadas o configuración
declarada. Actualmente el planificador recibe un tipo `openapi.ReducedContext`,
lo que convierte un formato de entrada concreto en parte del contrato del núcleo.

Esta feature introduce un `OperationCatalog` interno, versionado y neutral al
formato. Cada dato conserva procedencia, clase de fuente, confianza y completitud.
La combinación es determinista, hace visibles los conflictos y falla de forma
cerrada cuando la política no puede justificar una operación ejecutable.

## Alcance

- Definir el modelo interno versionado de operaciones HTTP, parámetros, schemas,
  seguridad y respuestas que consume el planificador.
- Definir una interfaz `ContractSource` pequeña para cargar snapshots acotados de
  OpenAPI aprobada, análisis estático, observaciones autorizadas y configuración
  declarada.
- Adaptar el inspector OpenAPI existente al catálogo neutral sin reducir sus
  garantías de validación, referencias locales, seguridad o contexto acotado.
- Combinar una o varias fuentes mediante reglas deterministas y conflictos
  explícitos.
- Aplicar una política explícita de confianza y completitud antes de exponer
  operaciones al modelo o permitir que se materialicen como pasos ejecutables.
- Migrar `agentplanning`, aprobación, replanning y `validationworkflow` al
  contrato neutral conservando compatibilidad observable de las features ya
  terminadas.
- Probar las fronteras sin red, credenciales, ejecución de código, modelo real ni
  aplicación desplegada.

## Fuera de alcance

- Implementar un analizador de lenguaje o framework productivo concreto; esta
  entrega incluye el adapter contractual y fixtures/fakes de una fuente derivada.
- Capturar tráfico real, inspeccionar producción o autorizar una observación. El
  adapter recibe observaciones ya autorizadas y acotadas.
- Descubrir, solicitar o aplicar credenciales.
- Descargar contratos, resolver referencias remotas o acceder a repositorios.
- Ejecutar operaciones HTTP, herramientas, código analizado o scripts.
- Inferir por lenguaje natural qué fuente es verdadera o resolver conflictos con
  un modelo.
- Cambiar el cálculo determinista de assertions, evidencias o veredictos.
- Presentar datos `derived`, `observed` o `declared` como contrato oficial.

## Actores y términos

- **Contract source:** adapter que traduce una entrada ya autorizada a claims del
  modelo interno sin efectuar red, ejecución ni resolución de credenciales.
- **Source instance:** identidad estable de una entrada concreta, separada de su
  clase y de ubicaciones de máquina.
- **Source class:** `authoritative`, `derived`, `observed` o `declared`.
- **Claim:** afirmación estructural sobre una operación o uno de sus campos,
  acompañada de procedencia y estado epistemológico.
- **Provenance:** source instance, clase, digest de contenido, versión del adapter
  y ubicación segura estructural dentro de la fuente.
- **Safe location:** ubicación relativa o identificador estructural acotado; no
  contiene path absoluto, URL con credenciales, payload ni texto libre.
- **Confidence:** nivel cerrado asignado por configuración confiable; una fuente
  no puede elevarlo mediante su propio contenido.
- **Completeness:** estado cerrado `complete`, `partial` o `unknown`, aplicable al
  claim y a la operación resultante.
- **Operation key:** método HTTP normalizado más path template exacto.
- **Conflict:** dos claims aplicables que afirman valores incompatibles para la
  identidad, schema, seguridad o respuesta de una operación.
- **Eligible operation:** operación cuya confianza, completitud y conflictos
  satisfacen la política explícita del caller.
- **Reduced catalog context:** envelope canónico, no confiable y acotado que solo
  contiene operaciones seleccionadas y elegibles para una restricción.

## R1 — Modelo interno neutral y versionado

THE planning core SHALL consume a versioned `OperationCatalog` whose public
types contain no OpenAPI parser types, OpenAPI-specific source handles, static
analysis AST types or observation transport DTOs.

### Acceptance criteria

- GIVEN catálogos semánticamente equivalentes originados en OpenAPI, análisis o
  configuración WHEN llegan al planificador THEN usan el mismo contrato interno
  de operación, parámetro, schema, seguridad y respuesta.
- GIVEN una búsqueda de dependencias y tipos públicos de `agentplanning` WHEN se
  verifica THEN no importan `internal/openapi` ni tipos de adapters de fuente.
- GIVEN una versión ausente o no soportada WHEN se valida el catálogo THEN falla
  antes de selección, inferencia o ejecución y no devuelve contexto parcial.

## R2 — Frontera común de fuentes

WHEN composition supplies an authorized source request, THE contract ingestion
service SHALL invoke a `ContractSource` selected explicitly by source kind and
SHALL receive only bounded internal claims plus safe source metadata.

### Acceptance criteria

- GIVEN fuentes de clase authoritative, derived, observed y declared WHEN se
  cargan mediante adapters/fakes THEN todas producen el mismo tipo de snapshot y
  conservan su clase original.
- GIVEN kind desconocido, adapter ausente o source instance duplicada con digest
  distinto WHEN se compone THEN se devuelve error tipado antes de combinar.
- GIVEN dos cargas independientes WHEN se ejecutan concurrentemente THEN no
  comparten estado mutable y pasan el detector de carreras.

## R3 — Procedencia íntegra y ubicación segura

WHEN a source contributes an operation, parameter, schema node, security
requirement or response, THE catalog SHALL retain provenance for that exact
claim, including source instance, source class, source digest, adapter version
and a bounded safe location.

### Acceptance criteria

- GIVEN un campo compuesto por claims idénticos de varias fuentes WHEN se
  consulta THEN se conservan todas sus procedencias ordenadas, sin elegir una y
  descartar silenciosamente las demás.
- GIVEN una ubicación con path absoluto, query, userinfo, secreto canario o texto
  mayor al límite WHEN se carga THEN se rechaza o se sustituye por una ubicación
  estructural segura, y el valor prohibido no aparece en catálogo, contexto,
  error ni observabilidad.
- GIVEN snapshots y colecciones devueltos al caller WHEN este los muta THEN no
  altera el catálogo retenido ni llamadas posteriores.

## R4 — Clase, confianza y completitud explícitas

THE catalog SHALL preserve source class, configured confidence and completeness
for every claim and operation, and SHALL NOT represent `derived`, `observed` or
`declared` information as `authoritative`.

### Acceptance criteria

- GIVEN contenido que se autodeclara authoritative o de confianza superior WHEN
  el adapter lo lee THEN la clase y confianza siguen siendo las fijadas por la
  configuración confiable de composición.
- GIVEN claims con distinta confianza o completitud WHEN se combinan THEN el
  catálogo conserva esos valores por claim y calcula el resumen de la operación
  de forma determinista, sin elevar confianza ni convertir `partial/unknown` en
  `complete`.
- GIVEN una proyección para el modelo o aprobación WHEN contiene una operación
  no authoritative THEN incluye su clase efectiva y no utiliza etiquetas que la
  presenten como contrato oficial.

## R5 — Adaptación OpenAPI sin regresión

WHEN an approved local OpenAPI contract is used as an authoritative source, THE
OpenAPI adapter SHALL translate the validated normalized contract into the
neutral catalog while preserving operation identity, effective parameters,
schemas, effective security, responses, omissions and safe retained provenance.

### Acceptance criteria

- GIVEN los fixtures OpenAPI 3.0/3.1 JSON/YAML ya soportados WHEN se adaptan THEN
  el catálogo neutral conserva la misma semántica normalizada y orden estable.
- GIVEN referencias locales, seguridad global/override y selección de responses
  WHEN se adaptan THEN continúan aplicándose las reglas y límites verificados por
  el inspector existente; no se reinterpreta el documento crudo en el adapter.
- GIVEN ejemplos, extensiones, documentación libre, operaciones no seleccionadas
  o bloques omitidos por tamaño WHEN se reduce contexto THEN siguen excluidos y
  sus omisiones permanecen explícitas.
- GIVEN validación de una respuesta observada contra el contrato OpenAPI WHEN se
  usa después de la migración THEN conserva el comportamiento existente y no se
  sustituye por claims menos fiables de otras fuentes.

## R6 — Combinación determinista

WHEN multiple source snapshots are combined, THE merger SHALL produce the same
canonical catalog and digest independently of input order or map iteration,
using exact structural equality to coalesce compatible claims.

### Acceptance criteria

- GIVEN las mismas fuentes en distinto orden WHEN se combinan THEN catálogo,
  orden de operaciones/procedencias/conflictos y digest son idénticos.
- GIVEN claims idénticos para un campo WHEN se combinan THEN el valor aparece una
  vez y conserva todas las procedencias.
- GIVEN dos operaciones distintas sin conflicto WHEN se combinan THEN ambas
  permanecen disponibles según la política, sin que una clase borre a otra.

## R7 — Conflictos explícitos y fail-closed

IF applicable claims disagree on method, path, schema, security requirement or
response contract, THEN THE merger SHALL record a typed conflict with safe
locations and SHALL NOT silently overwrite, rank or resolve the incompatible
values.

### Acceptance criteria

- GIVEN el mismo identificador lógico asociado a métodos o paths diferentes, o
  la misma operation key con schemas, seguridad o responses incompatibles WHEN
  se combina THEN se genera un conflicto tipado y reproducible por campo.
- GIVEN un conflicto en un campo ejecutable de una operación WHEN se reduce o
  materializa un plan THEN esa operación no es elegible hasta que una política
  explícita requiera revisión y esta se resuelva fuera del merger.
- GIVEN un conflicto en una operación no seleccionada WHEN se construye contexto
  para otra operación THEN el conflicto sigue auditable en el catálogo, pero no
  contamina ni amplía la selección independiente.

## R8 — Política de suficiencia antes de inferencia y ejecución

WHEN operations are selected for planning or revalidated for execution, THE
system SHALL evaluate an explicit policy over source class, confidence,
completeness and conflicts and SHALL return eligible context, insufficient
context or review required without guessing missing contract facts.

### Acceptance criteria

- GIVEN una operación que satisface el mínimo configurado y no tiene conflictos
  bloqueantes WHEN se selecciona THEN puede entrar en el contexto reducido con
  su procedencia y estado explícitos.
- GIVEN confianza inferior al mínimo, completitud `partial/unknown`, campo
  requerido ausente o conflicto bloqueante WHEN la política no lo permite THEN
  no llega al modelo ni a ejecución y el resultado tipado distingue
  `insufficient_context` de `review_required`.
- GIVEN un plan preparado y un catálogo/policy/digest distintos antes de
  ejecutar WHEN se revalida THEN se rechaza por drift; una selección anterior no
  concede autoridad sobre datos nuevos.

## R9 — Contexto reducido, canónico y relevante

WHEN a caller requests model context for identified constraints and explicit
operation keys, THE reducer SHALL include only eligible selected operations and
their reachable structural claims within configured count, depth and byte
limits, treating all source content as untrusted data.

### Acceptance criteria

- GIVEN operaciones o schemas no seleccionados WHEN se reduce THEN ninguna
  identidad o contenido exclusivo aparece en los bytes destinados al modelo.
- GIVEN una unidad estructural que no cabe WHEN se reduce THEN se omite completa,
  se registra el motivo y nunca se presenta un schema truncado como completo.
- GIVEN entradas equivalentes WHEN se reducen THEN producen bytes, retained index
  y digest iguales; el retained index vincula constraint, operation key y
  procedencias exactas que autorizan la materialización.

## R10 — Carga pasiva y límites de seguridad

WHILE a contract source is loaded or combined, THE system SHALL enforce explicit
per-source and aggregate byte, item, depth, claim and diagnostic limits and
SHALL NOT access network, execute source code, invoke tools or resolve
credentials.

### Acceptance criteria

- GIVEN un adapter OpenAPI, derivado, observado o declarado con canarios de URL,
  token o código ejecutable WHEN se carga en tests THEN no se crea transporte,
  proceso, tool call ni consulta a credential stores.
- GIVEN un límite agotado WHEN se carga o combina THEN se devuelve
  `limit_exceeded` sin snapshot/catalog parcial y sin empezar la fuente siguiente.
- GIVEN cancelación antes o durante carga, combinación o reducción WHEN se
  observa THEN se conserva `context.Canceled`/`DeadlineExceeded`, se detiene el
  recorrido y no comienza trabajo posterior.

## R11 — Compatibilidad del flujo existente

WHEN the current OpenAPI-only workflow is composed through the new abstraction,
THE planner, approval, replanning, workflow execution and historical explicit
plan command SHALL preserve their established observable behavior and bind the
neutral catalog digest instead of relying on mutable source-specific values.

### Acceptance criteria

- GIVEN las fixtures existentes de generación OpenAPI WHEN se ejecutan por el
  nuevo adapter THEN selección, plan preparado, policy checks y ejecución
  producen los mismos resultados funcionales.
- GIVEN aprobación o replanning WHEN se sella contexto THEN un cambio en
  operaciones, claims retenidos, procedencia, confianza, completitud, conflictos
  o policy invalida el binding de forma determinista.
- GIVEN `featureproof validate --plan` WHEN se usa THEN no exige una fuente de
  contrato nueva ni realiza inferencia implícita.

## R12 — Errores seguros, observabilidad y verificabilidad

IF ingestion, adaptation, merge, policy or reduction fails, THEN THE system
SHALL return a typed safe category and structural location, preserve the
operational cause where applicable, and emit only bounded non-secret metadata.

### Acceptance criteria

- GIVEN payloads, paths, headers o schemas con secretos canario WHEN ocurre un
  error THEN errores serializados, logs, métricas, catálogo y contexto no
  contienen el canario.
- GIVEN un error distinguible WHEN lo recibe el caller THEN decide mediante
  tipo/categoría (`invalid_source`, `conflict`, `insufficient_context`,
  `review_required`, `limit_exceeded`, `cancelled`, `internal`) sin comparar
  texto.
- GIVEN la suite contractual WHEN se ejecuta THEN cubre OpenAPI, fuente derivada,
  combinación, conflicto, procedencia, confianza, contexto insuficiente,
  cancelación y compatibilidad del planificador sin red ni credenciales.

## Errores y casos límite

- Método se normaliza a mayúsculas; path template conserva casing y forma exacta.
  No se normalizan dos paths distintos a una identidad inventada.
- Una source instance repetida con el mismo digest es idempotente; con digest o
  metadata confiable distintos es conflicto de entrada.
- Ausencia de un claim no equivale a una afirmación negativa. Produce
  completitud parcial/desconocida según metadata validada.
- Una fuente de mayor confianza no borra automáticamente una incompatible de
  menor confianza. El conflicto se conserva y la policy decide si requiere
  revisión; el merger nunca resuelve por prioridad implícita.
- Observaciones describen únicamente lo visto y no demuestran que otros status,
  parámetros o schemas sean imposibles.
- Análisis estático describe únicamente lo demostrado por su adapter/version y
  no adquiere autoridad por coincidir con una fuente oficial.
- Un catálogo puede conservar operaciones no elegibles para auditoría. El
  contexto del modelo y la ejecución solo usan el subconjunto admitido.
- Los schemas se comparan mediante representación estructural canónica, no por
  direcciones de memoria, orden de maps o texto original.
- Contenido externo permanece dato inerte incluso cuando contiene instrucciones.

## Decisiones abiertas

No existen decisiones abiertas que bloqueen esta entrega. La política concreta
se configura explícitamente por composición y se prueba con varios perfiles; no
se define un umbral global implícito. Los analizadores y capturadores productivos
concretos requieren features posteriores.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-19T13:52:53+02:00
```

La implementación no puede comenzar hasta que una persona apruebe esta spec y
el leader registre la transición a `in_progress`.
