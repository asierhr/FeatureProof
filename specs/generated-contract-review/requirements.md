# Revisión y exportación de contratos derivados — Requirements

## Objetivo y problema

Los inspectores de Go y Spring Boot pueden producir un `OperationCatalog`
derivado con procedencia, confianza, completitud, conflictos y omisiones. Ese
resultado ayuda a planificar, pero no es por sí solo un contrato oficial. La
feature introduce una revisión humana durable que compara el candidato con una
baseline aprobada, registra decisiones explícitas y permite publicar la
proyección aprobada como OpenAPI 3.1 sin ocultar incertidumbre ni procedencia.

## Alcance

- Crear un artefacto de revisión versionado, canónico e inmutable para un
  catálogo candidato y una baseline aprobada.
- Calcular un diff estructural estable de operaciones, parámetros, schemas,
  request bodies, respuestas y seguridad.
- Clasificar cada diferencia como `added`, `modified`, `removed` o `uncertain`,
  conservando conflictos, omisiones, completitud y procedencia.
- Registrar decisiones totales o selectivas de aprobación y rechazo mediante
  una autoridad humana explícita.
- Construir una proyección aprobada sin mutar el candidato ni la baseline.
- Invalidar aprobaciones ante drift de revisión, fuentes, analizadores,
  catálogos, baseline o decisiones.
- Exportar explícitamente la proyección aprobada como OpenAPI 3.1 JSON mediante
  una publicación local atómica y protegida contra sobrescritura.
- Exponer el mismo servicio de aplicación a CLI, API, MCP y la integración
  GitHub, con autorización e idempotencia propias de cada frontera.
- Persistir reviews, decisiones, aprobaciones y publicaciones con un adapter
  local durable bajo `.featureproof/contract-reviews`.

## Fuera de alcance

- Editar código fuente, OpenAPI de entrada o artefactos internos para resolver
  una diferencia.
- Generar endpoints o schemas mediante un modelo, lenguaje natural o ejecución
  de la aplicación.
- Resolver conflictos automáticamente, elevar confianza o convertir
  completitud `partial/unknown` en `complete`.
- Aprobar por ausencia de respuesta, por una label genérica o por una conclusión
  del agente.
- Descargar baselines, consultar repositorios, ejecutar analizadores o resolver
  credenciales desde el dominio de review; composición entrega snapshots ya
  autorizados y sellados.
- Exportar YAML en v1, publicar a un repositorio remoto, crear commits, abrir
  pull requests o modificar una OpenAPI existente sin la operación explícita de
  exportación.
- Usar un catálogo derivado no aprobado como única prueba determinista de
  conformidad.

## Actores y términos

- **Candidate catalog:** `OperationCatalog` derivado y sellado que se somete a
  revisión.
- **Approved baseline:** catálogo authoritative local ya validado o snapshot de
  una aprobación anterior cuya identidad y vigencia pueden verificarse.
- **Review artifact:** snapshot inmutable con identidad, bindings, diff y
  diagnósticos seguros; no contiene decisiones mutables.
- **Review item:** diferencia atómica e identificada de forma estable para una
  operación o campo estructural.
- **Decision set:** conjunto canónico de decisiones `accept`/`reject` sobre todo
  el candidato o sobre IDs concretos, con revisión CAS.
- **Approved projection:** baseline más exactamente las diferencias aceptadas;
  las rechazadas o pendientes no modifican la baseline.
- **Approval binding:** identidad canónica de review, candidate, baseline,
  repositorio, commit, analizadores, fuentes y decision set.
- **Drift:** cualquier diferencia respecto del approval binding, aunque los
  campos visibles de alto nivel parezcan iguales.
- **Representational omission:** hecho aprobado que OpenAPI 3.1 no puede
  expresar exactamente y que se declara mediante una extensión acotada.
- **Authorized reviewer:** principal autenticado al que una policy confiable
  concede la acción concreta sobre repositorio y revisión.

## R1 — Artefacto de revisión versionado e inmutable

WHEN an authorized candidate and approved baseline are submitted, THE review
service SHALL produce a versioned canonical artifact that binds repository,
full commit, candidate catalog, baseline, sources, analyzer versions, limits and
every review item without mutable aliases or machine-local locations.

### Acceptance criteria

- GIVEN las mismas entradas en distinto orden WHEN se crea la revisión THEN el
  artefacto, IDs de items, bytes canónicos y digest son idénticos.
- GIVEN cualquier cambio de repositorio, commit completo, source digest,
  adapter/analyzer version, catalog digest, baseline digest o límites WHEN se
  crea otra revisión THEN cambia el digest y no reutiliza autoridad anterior.
- GIVEN que un caller modifica slices, maps o bytes devueltos WHEN vuelve a leer
  la revisión THEN el estado durable y su digest permanecen intactos.
- GIVEN versión ausente o no soportada WHEN se carga el artefacto THEN falla de
  forma tipada antes de decidir, aprobar, exportar o usar la proyección.

## R2 — Baseline aprobada y compatible

WHEN a review is created, THE service SHALL verify that the baseline is
approved, belongs to the same repository and declared comparison scope, has a
verifiable digest, and is structurally compatible with the candidate catalog
version.

### Acceptance criteria

- GIVEN una OpenAPI aprobada adaptada como fuente authoritative o una proyección
  aprobada previa WHEN coincide repository y scope THEN puede actuar como
  baseline y conserva su identidad original.
- GIVEN baseline derivada sin aprobación, manipulada, de otro repositorio,
  versión incompatible o digest incorrecto WHEN se crea la revisión THEN se
  rechaza antes de calcular un diff utilizable.
- GIVEN dos baselines posibles WHEN el caller no selecciona una identidad exacta
  THEN no se elige por antigüedad, nombre de archivo ni orden de almacenamiento.

## R3 — Diff estructural determinista e incertidumbre explícita

WHEN candidate and baseline are compared, THE differ SHALL classify atomic
changes to operations, parameters, request schemas, response selectors and
schemas, and security as `added`, `modified`, `removed` or `uncertain`, using
structural canonical equality rather than source text or map order.

### Acceptance criteria

- GIVEN una operación o campo presente solo en un candidato completo y sin
  conflictos u omisiones relevantes WHEN se compara THEN se clasifica como
  `added`; el caso simétrico se clasifica como `removed`.
- GIVEN la misma identidad con valores estructurales distintos WHEN ambos lados
  son demostrables THEN se clasifica como `modified` con fingerprints before y
  after, nunca con payloads arbitrarios.
- GIVEN conflicto, completitud `partial/unknown`, diagnóstico, límite u omisión
  que impide demostrar presencia, ausencia o valor exacto WHEN se compara THEN
  el item es `uncertain` y declara códigos estructurales de causa.
- GIVEN schemas recursivos, arrays, parámetros reordenados, alternativas de
  seguridad o responses en distinto orden WHEN son semánticamente iguales THEN
  no se genera una diferencia falsa ni se entra en recursión ilimitada.
- GIVEN una operación sin cambios WHEN se produce el artifact THEN queda
  contabilizada como unchanged, pero no se inventa un review item accionable.

## R4 — Decisiones humanas explícitas y proyección selectiva

WHEN an authorized reviewer submits a decision, THE service SHALL accept either
one whole-candidate action or an explicit set of review item IDs, SHALL record
`accept` or `reject` for every targeted item, and SHALL derive the approved
projection only from accepted differences.

### Acceptance criteria

- GIVEN `accept_all` sobre el digest exacto del artifact WHEN no existe drift
  THEN la proyección representa el candidato exacto, incluida su incertidumbre
  declarada, y no eleva confianza ni completitud.
- GIVEN decisiones selectivas WHEN se proyectan THEN los items aceptados se
  aplican sobre la baseline; los rechazados o aún pendientes conservan el valor
  de baseline, y una eliminación solo ocurre si su item fue aceptado.
- GIVEN `reject_all` WHEN se decide THEN queda una revisión rechazada auditable y
  no existe un contrato candidato aprobado ni exportable.
- GIVEN un ID inexistente, duplicado, perteneciente a otro artifact, un conjunto
  que mezcla whole-action con items o una revisión CAS obsoleta WHEN se decide
  THEN se rechaza todo el comando sin decisión parcial.
- GIVEN un item `uncertain` aceptado WHEN se proyecta THEN su causa, confianza y
  completitud permanecen explícitas; la aprobación no afirma hechos ausentes.

## R5 — Autoridad y auditoría completa de la aprobación

WHEN a decision changes review state, THE system SHALL authorize the actor and
record actor ID, trusted UTC instant, repository, commit, review revision,
candidate and baseline digests, source digests, analyzer/adapter versions,
decision digest and resulting projection digest.

### Acceptance criteria

- GIVEN un principal sin permiso de review para el repositorio WHEN intenta
  decidir THEN se devuelve `unauthorized` sin escribir eventos ni publicar.
- GIVEN una decisión autorizada WHEN se consulta la auditoría THEN todos los
  bindings exigidos están presentes y el instante procede del reloj inyectado,
  no del request.
- GIVEN actor, tiempo, repository, commit o digest manipulados por el request
  WHEN se decide THEN composición usa identidades confiables o rechaza el
  comando; ningún campo autoafirmado concede autoridad.
- GIVEN una lectura pública o error WHEN se serializa THEN no contiene tokens,
  cookies, cabeceras, paths absolutos, payloads fuente ni secretos canario.

## R6 — Invalidación por drift antes de decidir, exportar o usar

WHILE a review approval may grant contract authority, THE service SHALL
re-resolve and compare its complete approval binding immediately before a
decision, export, ingestion into planning, and deterministic validation, and
SHALL invalidate the approval if any bound value changed.

### Acceptance criteria

- GIVEN cambio de commit, catalog, baseline, fuente, analyzer/adapter version,
  decision set o projection digest WHEN se revalida THEN la aprobación anterior
  pasa a `invalidated` y no autoriza exportación ni validación.
- GIVEN otro proceso o reinicio entre aprobación y uso WHEN restaura el review
  THEN repite la revalidación desde snapshots verificables, sin confiar en
  memoria del proceso anterior.
- GIVEN drift detectado justo después de una primera comprobación WHEN va a
  publicarse o consumirse el contrato THEN un segundo checkpoint pre-I/O impide
  escritura, tool calls o uso determinista.
- GIVEN invalidación WHEN se crea una revisión nueva THEN la auditoría anterior
  permanece append-only y no se reescribe como si nunca hubiera sido aprobada.

## R7 — Autoridad acotada del contrato derivado

THE system SHALL treat an unapproved derived catalog only as proposal or
analysis input when an explicit policy allows it, and SHALL NOT use it as the
sole deterministic proof of conformance or as an authoritative baseline.

### Acceptance criteria

- GIVEN candidato pendiente, parcialmente decidido, rechazado o invalidado WHEN
  planning policy permite datos derivados THEN conserva `derived`, confidence,
  completeness y review state, pero no satisface por sí solo una policy que
  exige contrato aprobado.
- GIVEN proyección aprobada y vigente WHEN entra en `contractcatalog` THEN una
  fuente de review explícita aporta la autoridad humana y conserva además todas
  las procedencias derivadas originales.
- GIVEN contrato aprobado parcial WHEN una assertion depende de una ausencia no
  demostrada THEN la evaluación queda inconclusive; aprobación no convierte
  parcialidad en evidencia negativa.
- GIVEN conflicto no resuelto o item pendiente que afecta una operación WHEN se
  solicita como única autoridad THEN se exige review o contexto insuficiente,
  nunca se selecciona silenciosamente un ganador.

## R8 — Exportación OpenAPI 3.1 fiel y auditable

WHEN an authorized caller explicitly exports a current approved projection,
THE exporter SHALL produce deterministic OpenAPI 3.1 JSON and SHALL attach
bounded FeatureProof extensions for approval, provenance, source class,
confidence, completeness, conflicts and representational omissions at the
smallest applicable structural unit.

### Acceptance criteria

- GIVEN operaciones, parámetros, request bodies, responses, schemas y security
  schemes representables WHEN se exportan THEN el documento valida como
  OpenAPI `3.1.0` y conserva su semántica estructural y orden canónico.
- GIVEN procedencia múltiple WHEN se exporta THEN cada unidad conserva source
  ID, repository, revision, source digest, adapter version y safe location,
  ordenados y acotados, sin paths de máquina ni contenido fuente.
- GIVEN nullable, schema parcial, constraint declarativa, selector o conflicto
  no representable exactamente WHEN se exporta THEN no se fabrica una
  equivalencia; se omite la construcción insegura o se conserva solo la parte
  demostrable y se añade `x-featureproof-omissions` con código y ubicación.
- GIVEN el mismo approval binding y configuración segura de `info` WHEN se
  exporta repetidamente THEN produce exactamente los mismos bytes y digest.
- GIVEN un catálogo no aprobado, rechazado, incompleto de decisiones o
  invalidado WHEN se exporta THEN falla antes de crear archivos temporales.

## R9 — Publicación atómica y protección contra overwrite

WHEN an export target is supplied, THE local publisher SHALL require an
authorized canonical path and SHALL publish atomically; it SHALL NOT replace an
existing target unless the caller explicitly requests replacement and supplies
the exact digest of the existing regular file.

### Acceptance criteria

- GIVEN destino inexistente dentro del root autorizado WHEN se publica THEN el
  archivo completo aparece mediante temp en el mismo directorio, sync y rename,
  sin estado parcial observable.
- GIVEN destino existente WHEN falta `replace=true` o su expected digest no
  coincide THEN se devuelve `target_exists`/`conflict` y sus bytes no cambian.
- GIVEN symlink, reparse point, path absoluto no autorizado, traversal,
  directorio, hard-link inseguro o cambio TOCTOU WHEN se publica THEN falla de
  forma cerrada sin escribir fuera del root ni seguir el enlace.
- GIVEN cancelación o fallo de escritura/sync/rename WHEN se publica THEN no se
  presenta éxito, se limpia el temp propio cuando es seguro y el destino previo
  permanece íntegro.

## R10 — Durabilidad, concurrencia e idempotencia

WHILE reviews are created, decided, invalidated or exported concurrently, THE
store SHALL preserve append-only audit, compare-and-swap revisions, atomic
checkpoints and idempotency without duplicate decisions or publications.

### Acceptance criteria

- GIVEN dos procesos decidiendo la misma revisión CAS WHEN compiten THEN solo
  uno avanza desde la revisión esperada y el otro recibe conflicto recuperable.
- GIVEN el mismo principal, idempotency key y request digest WHEN se reintenta
  create/decide/export THEN devuelve la misma identidad y resultado sin añadir
  un segundo efecto; reutilizar la key con otro digest falla.
- GIVEN reinicio tras cada frontera de persistencia WHEN se recupera THEN journal
  y snapshot convergen al último estado completo o fallan por integridad, nunca
  aceptan un prefijo ambiguo.
- GIVEN reviews distintas WHEN avanzan concurrentemente THEN no comparten estado
  mutable ni mantienen locks durante callbacks o filesystem I/O y pasan race.

## R11 — Límites, cancelación, errores y redacción

WHILE diffing, deciding, projecting, persisting or exporting, THE system SHALL
enforce explicit limits for operations, items, schema depth/nodes, sources,
provenance entries, diagnostics and bytes, and SHALL propagate cancellation
without returning a successful partial artifact.

### Acceptance criteria

- GIVEN límite agotado durante cualquier fase WHEN se procesa THEN se devuelve
  `limit_exceeded`, no se inicia la fase siguiente y no se publica artefacto,
  aprobación o export parcial.
- GIVEN cancelación antes o durante diff, CAS, serialización o publicación WHEN
  se observa THEN se conserva `context.Canceled`/`DeadlineExceeded` bajo una
  categoría segura y cesa trabajo posterior.
- GIVEN secretos canario en IDs, locations, diagnostics, schemas o errores de
  colaboradores WHEN se rechazan/redactan THEN no aparecen en artifacts,
  journal, exports, logs, API, MCP, GitHub ni mensajes de error.
- GIVEN error distinguible WHEN llega a un adapter THEN se proyecta desde un
  código cerrado (`invalid_input`, `incompatible_baseline`, `unauthorized`,
  `conflict`, `invalidated`, `limit_exceeded`, `cancelled`, `target_exists`,
  `unavailable`, `internal`) sin comparar texto.

## R12 — Paridad de superficies y GitHub ligado al head

WHEN contract review is exposed through CLI, API, MCP or GitHub, THE adapter
SHALL delegate create/get/decide/export to the same application service and
SHALL preserve identities, authorization, idempotency, errors and safe response
projections.

### Acceptance criteria

- GIVEN la misma operación autorizada mediante CLI, API o MCP WHEN se ejecuta
  THEN produce el mismo state/digest y ninguna superficie acepta campos que el
  servicio no valida.
- GIVEN GitHub WHEN crea o decide una revisión THEN instalación, repositorio,
  pull request, actor y full head OID proceden del snapshot autenticado y se
  vuelven a comprobar antes de la decisión; un head cambiado invalida el review.
- GIVEN redelivery de API, MCP o GitHub WHEN conserva idempotency/delivery ID y
  payload THEN no duplica decisiones, exports, checks ni comentarios.
- GIVEN una lectura GitHub WHEN se publica un resumen THEN solo incluye IDs,
  conteos, estado, commit y referencias seguras; export requiere una ruta local
  autorizada y nunca escribe una ruta recibida de texto de la pull request.
- GIVEN un error interno o secreto WHEN se proyecta en cualquiera de las cuatro
  superficies THEN los códigos son equivalentes y el contenido permanece
  acotado y redactado.

## R13 — Verificación, trazabilidad y comentarios sistémicos

THE implementation SHALL provide deterministic requirement-level tests and
system-responsibility comments for every new or modified entity, constant,
function, method and helper, including test helpers and fakes.

### Acceptance criteria

- GIVEN R1–R12 WHEN se revisa la trazabilidad THEN cada requirement referencia
  al menos un test real que falla al retirar el comportamiento correspondiente.
- GIVEN el diff Go WHEN se audita THEN cada símbolo nuevo o modificado explica
  su responsabilidad o frontera dentro del sistema, no repite solo la sintaxis.
- GIVEN las suites afectadas WHEN se ejecutan THEN pasan formato, compile-only,
  `go vet`, repetición determinista, race y coverage mínimo del 85 % sobre código
  nuevo/modificado.
- GIVEN fixtures adversarias WHEN se prueban THEN cubren diff, conflictos,
  aprobación total/selectiva, rechazo, drift, recovery, overwrite, TOCTOU,
  partialidad, concurrencia, cancelación, superficies y secretos canario.

## Errores y casos límite

- La identidad de operación sigue siendo método HTTP normalizado y path exacto;
  un logical ID nunca sustituye esa identidad.
- Parámetros se identifican por `(in, name)`; responses por selector; propiedades
  de schema por un path estructural escapado y estable.
- Ausencia no significa eliminación cuando el lado que debería demostrarla no
  es completo o tiene omisiones/diagnósticos aplicables.
- Un cambio de provenance, confianza o completitud sin cambio del valor
  estructural sigue siendo revisable como metadata modificada y participa en el
  approval binding.
- Aceptar una diferencia no elimina sus conflicts ni diagnostics; solo registra
  autoridad humana sobre la proyección exacta.
- Una revisión selectiva puede permanecer `partially_decided`. Solo una
  proyección total (`accept_all` o todos los items decididos) puede exportarse;
  items rechazados producen la versión de baseline correspondiente.
- Si todos los items se rechazan selectivamente, la proyección resultante es la
  baseline, pero el candidato consta como rechazado y no adquiere autoridad.
- Una baseline vacía solo es válida si existe como snapshot aprobado explícito;
  el servicio no inventa una baseline vacía para un candidato inicial.
- El export JSON usa únicamente configuración segura para `info.title` e
  `info.version`; textos del código o de GitHub no se copian como documentación.
- Los timestamps de auditoría no participan en los bytes OpenAPI deterministas;
  la identidad de approval sí aparece mediante digest estable.

## Decisiones abiertas

No existen decisiones abiertas que bloqueen esta entrega. V1 exporta JSON
canónico; YAML, commits y publicación remota quedan fuera de alcance. La
semántica selectiva conserva baseline para cambios no aceptados y exige que
todos los items tengan decisión antes de exportar.

## Aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-20T16:31:20+02:00
```

La implementación no puede comenzar hasta que una persona apruebe esta spec y
el leader cambie la feature a `in_progress`.
