# Inspector estático de backends Spring Boot — Requirements

## Objetivo y problema

FeatureProof dispone de un catálogo neutral de operaciones y de un inspector
estático para backends Go, pero todavía no puede derivar de forma segura las
operaciones declaradas por un backend Spring Boot Java. Esta feature añade una
fuente `derived` que inspecciona una revisión local ya autorizada y transforma
únicamente construcciones Java/Spring demostrables en un `OperationCatalog`.

El análisis es hermético y pasivo. Leer el repositorio no autoriza ejecutar
Maven, Gradle, `javac`, annotation processors, tests, la aplicación, plugins ni
código del proyecto. Las construcciones dinámicas o incompletas se conservan
como diagnósticos y reducen la completitud; nunca se sustituyen por convenciones
del framework ni por inferencias basadas en nombres.

## Alcance

- Inspeccionar un snapshot local, inmutable, de solo lectura y ligado a un
  repository ID y commit autorizados.
- Recibir source sets, source roots, release Java y contexto semántico de Spring
  explícitos desde trusted composition, sin deducirlos del host o del build.
- Parsear Java y resolver de forma local y acotada packages, imports, constantes,
  clases anidadas, jerarquía local necesaria, genéricos y DTOs.
- Reconocer una lista cerrada de controllers, mappings, parámetros, bodies,
  content types, responses y declaraciones de seguridad Spring soportadas.
- Producir un `SourceSnapshot` de clase `derived` mediante el contrato neutral de
  la feature 21, con procedencia, confianza, completitud, diagnósticos e impacto
  de changed paths.
- Aplicar límites de archivos, bytes, unidades, nodos, símbolos, profundidad,
  combinaciones de mappings, operaciones, schemas, diagnósticos y duración.
- Integrarse con merge, sufficiency policy y revalidación del workflow existente.
- Probar el comportamiento con proyectos fixture locales y fronteras que fallen
  ante cualquier ejecución, red, descarga o acceso exterior.

## Fuera de alcance

- Ejecutar Maven Wrapper, Maven, Gradle Wrapper, Gradle, `javac`, Spring, tests,
  annotation processors, agentes Java, scripts, plugins o binarios del repo.
- Descargar dependencias, consultar repositorios Maven, caches del host, JDKs,
  variables de entorno, Git, Internet, servicios o credenciales.
- Resolver configuración externa, profiles, properties/YAML, SpEL, reflection,
  runtime route registration, filters/interceptors dinámicos o auto-configuration.
- Ejecutar o simular Lombok, MapStruct, Immutables, Jackson serializers,
  deserializers, modules, mixins o cualquier generación/transformación dinámica.
- Cubrir Kotlin, Groovy, Scala, Spring WebFlux functional routing, JAX-RS o
  frameworks distintos del subconjunto Spring MVC Java definido aquí.
- Demostrar la semántica de autorización de `PreAuthorize`, `Secured` o
  `RolesAllowed`, ni convertirlas por sí solas en credenciales o permisos.
- Presentar información derivada como contrato oficial, resolver conflictos entre
  fuentes o cambiar assertions, evidencias y veredictos.

## Actores y términos

- **Trusted composition:** capa que autoriza repositorio, revisión, snapshot,
  source sets, roots, contexto Java/Spring, límites, confianza y policy.
- **Repository snapshot:** capacidad enraizada de lectura inmutable que devuelve
  paths relativos y no permite escapes, symlinks/reparse points ni reaperturas por
  path del host.
- **Source set:** conjunto nombrado y explícito de roots Java incluidos en una
  inspección, por ejemplo `main`; no se deriva de Maven o Gradle.
- **Build context:** release Java, source sets, roots, packages framework
  allowlisted y policy de fuentes generadas suministrados por composición.
- **Static closure:** unidades, declaraciones, constantes, controllers, DTOs y
  tipos locales necesarios para resolver las operaciones seleccionadas.
- **Resolved fact:** hecho demostrado por AST y resolución local dentro de la
  lista v1.
- **Partial fact:** hecho demostrado solo en parte; no autoriza completar lo
  ausente.
- **Declarative security constraint:** presencia estructural de una annotation y
  sus argumentos; no prueba que method security esté habilitada ni cómo se evalúa.
- **Affected operation:** operación cuyo controller, mapping, DTO, constante o
  cierre toca un changed path; no incrementa confianza.

## Patrones Java/Spring v1 soportados

La lista es cerrada. Las annotations se reconocen por identidad fully-qualified
resuelta mediante package/imports, no por el simple name aislado.

### Controllers y mappings

- `org.springframework.web.bind.annotation.RestController`.
- `org.springframework.stereotype.Controller` únicamente cuando la semántica de
  response body queda demostrada mediante `ResponseBody` efectiva o un retorno
  `ResponseEntity` soportado. Los métodos de vistas quedan unresolved.
- `RequestMapping` en clase y método: aliases `value`/`path`, arrays constantes,
  `method`, `consumes` y `produces`. Se genera el producto cartesiano acotado de
  paths y métodos demostrables; un `RequestMapping` sin método no fabrica verbs.
- `GetMapping`, `PostMapping`, `PutMapping`, `PatchMapping` y `DeleteMapping` en
  método, con `value`/`path`, `consumes` y `produces` estáticos.
- Solo se evalúan literales, arrays y concatenaciones de compile-time constants
  Java locales (`static final`) y enum constants Spring conocidas. Llamadas,
  fields mutables, properties, placeholders y expresiones quedan unresolved.
- Custom composed annotations, inheritance/interface mappings y aliases distintos
  de los enumerados no se expanden en v1.

### Parámetros, bodies y DTOs

- `PathVariable`, `RequestParam`, `RequestHeader` y `RequestBody`, incluyendo los
  aliases `name`/`value` y flags/valores constantes soportados.
- El nombre puede proceder de la annotation o, si no se especifica, del nombre
  léxico presente en el source. No se presupone metadata `-parameters` generada.
- `defaultValue` en request param/header demuestra no obligatoriedad, pero su
  contenido no se expone al modelo ni se usa como credencial.
- Tipos locales: primitives/wrappers, `String`, arrays, enums, records, DTOs Java
  con propiedades estructurales demostrables y genéricos soportados como
  `Optional<T>`, `List<T>`, `Set<T>` y `Map<String,T>`.
- Las reglas de nulabilidad/required solo se afirman cuando annotations y tipos
  soportados lo demuestran. Lombok no crea getters, constructors ni propiedades;
  código generado ya presente solo se incluye bajo policy explícita.
- Tipos externos ausentes, wildcards/type variables no acotables, herencia
  externa, custom Jackson serializers/deserializers y annotations Jackson no
  allowlisted degradan u omiten el schema con diagnóstico.

### Responses y seguridad declarativa

- `ResponseStatus` en clase o método con `value`/`code` constante.
- Retornos `ResponseEntity<T>` y construcciones estáticas soportadas
  `ok`, `status(...).body(...)`, `noContent().build()` y constructor con status
  constante, siempre que tipo y status sean resolubles localmente.
- Un tipo de retorno demostrable puede aportar schema; la ausencia de status
  explícito no permite enumerar todos los status. Branches o builders dinámicos
  conservan lo resuelto y degradan completitud.
- `PreAuthorize`, `Secured` y `RolesAllowed` en clase o método se conservan como
  constraints estructurales con annotation exacta, expresión o roles acotados y
  procedencia. No se interpreta SpEL, no se expande jerarquía de roles y no se
  crea un `SecurityRequirement` ejecutable por su mera presencia.

## R1 — Snapshot, revisión y contexto autorizados

WHEN an inspection is requested, THE Spring inspector SHALL accept only a
bounded read-only repository snapshot bound by trusted composition to an exact
repository, commit, source set and explicit Java/Spring build context.

### Acceptance criteria

- GIVEN repository ID, commit completo, source ID, roots, source sets, release
  Java, framework identities, límites y filesystem autorizados WHEN se sella el
  input THEN todos forman parte de su identidad y digest.
- GIVEN revisión, source set, root o release ausente/no soportado, clase distinta
  de `derived`, metadata incoherente o digest inesperado WHEN se carga la source
  THEN falla antes de leer o parsear archivos.
- GIVEN un path absoluto, `..`, root solapado ambiguo, symlink/reparse point o
  changed path fuera de los roots WHEN se valida THEN se rechaza mediante error
  tipado sin revelar el path del host.

## R2 — Inspección hermética y pasiva

WHILE inspecting Java sources, THE Spring inspector SHALL use only bounded reads,
parsing/AST traversal and local symbol/type resolution, and SHALL NOT execute or
invoke build tools, compilers, processors, tests, application code, network,
credentials, host classpaths or dependency caches.

### Acceptance criteria

- GIVEN `pom.xml`, Gradle files/wrappers, plugins, processors, scripts, test
  classes, static initializers y malicious comments/resources WHEN se inspecciona
  THEN ninguno se ejecuta, interpreta como instrucción ni amplía el source set.
- GIVEN un import o tipo externo sin source local WHEN se necesita THEN se usa
  únicamente su identidad sintáctica si está allowlisted, se degrada el resultado
  y no se consulta classpath/JDK/cache/red para resolverlo.
- GIVEN spies para procesos, red, environment, credentials y filesystem exterior
  WHEN corre la suite THEN registran cero usos y solo el snapshot autorizado
  recibe lecturas.

## R3 — Controllers y mappings estáticos

WHEN a supported controller contains class/method mappings whose method and path
are statically resolvable, THE inspector SHALL emit the bounded Cartesian product
of exact effective HTTP methods and normalized path templates.

### Acceptance criteria

- GIVEN `RestController`, class `RequestMapping` y mappings verbales de método con
  `path`/`value` literales, arrays o constants locales WHEN se inspeccionan THEN se
  combinan prefix, paths y verb exactos sin depender de nombres de variables.
- GIVEN `RequestMapping(method=...)` con múltiples paths/methods dentro del límite
  WHEN se resuelve THEN cada combinación aparece una vez en orden canónico.
- GIVEN mapping sin método, placeholder, llamada, custom composed annotation,
  controller no demostrable o producto que excede el límite WHEN se encuentra
  THEN no se fabrican operaciones y se emite diagnóstico parcial/limitado.
- GIVEN simple names que resuelven a annotations propias con el mismo nombre WHEN
  se inspeccionan THEN no se tratan como Spring.

## R4 — Parámetros Spring conservadores

WHEN a supported controller method declares a supported Spring parameter
annotation, THE inspector SHALL derive only name, location, requiredness and
schema facts proven by that annotation and its locally resolved Java type.

### Acceptance criteria

- GIVEN `PathVariable`, `RequestParam` o `RequestHeader` con aliases y constants
  resolubles WHEN se proyectan THEN nombre/location/required respetan los valores
  efectivos y el path variable existe en el template.
- GIVEN `RequestBody` con `required` explícito o default de Spring conocido WHEN
  se proyecta THEN el body refleja solo esa obligatoriedad y los content types
  demostrables.
- GIVEN annotation sin nombre y parámetro léxico disponible WHEN se analiza THEN
  se usa ese nombre; si no existe evidencia léxica segura o el path no coincide,
  el fact queda omitido/parcial.
- GIVEN alias conflictivo, nombre dinámico, type-use no soportado o tipo externo
  WHEN se encuentra THEN no se inventa schema ni requiredness adicional.

## R5 — DTOs Java locales y schemas acotados

WHEN a request or response references a locally resolvable supported Java type,
THE inspector SHALL project the structural schema proven from source without
running generators, serializers, validators, constructors or application code.

### Acceptance criteria

- GIVEN primitives/wrappers, strings, arrays, enums, records, DTOs y collections
  genéricas soportadas WHEN se resuelven THEN el schema neutral conserva tipos,
  propiedades, items y enum values demostrables.
- GIVEN nested classes/imports/aliases Java inequívocos WHEN forman parte del
  cierre local THEN se resuelven por identidad de símbolo, no por simple-name
  heuristics.
- GIVEN Lombok sin source generado, custom serializer/deserializer, wildcard,
  type variable no acotable, herencia externa, ciclo o generic/depth budget
  agotado WHEN se proyecta THEN la unidad se omite o queda parcial y nunca se
  presenta como schema completo.
- GIVEN records/enums o DTOs en archivos no cambiados requeridos por una operación
  afectada WHEN se inspecciona THEN permanecen en el cierre bajo los mismos límites.

## R6 — Content negotiation y responses demostrables

WHEN supported mappings or return constructs declare media types and response
statuses statically, THE inspector SHALL attach only the exact `consumes`,
`produces`, response selectors and response schemas it can prove.

### Acceptance criteria

- GIVEN `consumes`/`produces` en clase/método y aliases/arrays constantes WHEN se
  combinan THEN se calcula el conjunto efectivo con orden estable y conflictos
  explícitos, sin asumir JSON por el tipo Java.
- GIVEN `ResponseStatus` o un builder/constructor `ResponseEntity` soportado con
  status constante WHEN se analiza THEN se añade el selector exacto y el schema
  del tipo/body solo si es resoluble.
- GIVEN `ResponseEntity<T>` sin status fijo, branches con distintos status,
  status/body calculado o plain object sin media type demostrable WHEN se analiza
  THEN se preservan los facts seguros y la response queda parcial; no se inventa
  exhaustividad ni content type.
- GIVEN un método `Controller` de vista sin `ResponseBody` o `ResponseEntity`
  soportado WHEN se encuentra THEN no produce una operación ejecutable v1 y deja
  un diagnóstico cerrado.

## R7 — Seguridad declarativa como dato estructural

WHEN a supported `PreAuthorize`, `Secured` or `RolesAllowed` annotation is
statically present on a selected class or method, THE inspector SHALL preserve
its bounded expression or roles and exact provenance as declarative constraints
without asserting runtime authorization semantics.

### Acceptance criteria

- GIVEN expression `PreAuthorize` constante WHEN se lee THEN se conserva como
  dato no confiable, acotado y separado de instrucciones/policy, sin evaluar SpEL.
- GIVEN roles constantes en `Secured` o `RolesAllowed` WHEN se leen THEN se
  preservan deduplicados/canónicos junto a la ubicación exacta de la annotation,
  sin expandir jerarquías ni inferir un auth scheme.
- GIVEN annotations en clase y método WHEN ambas existen THEN se conservan ambas
  procedencias; el inspector no decide override, AND/OR ni si method security está
  habilitada.
- GIVEN expresión/rol dinámico, demasiado grande o con valor prohibido por
  projection policy WHEN se encuentra THEN se omite/redacta la unidad completa,
  se registra diagnóstico y no cruza catálogo, error ni observabilidad.

## R8 — Procedencia exacta, confianza y completitud

WHEN the inspector emits an operation, field, schema, response or declarative
constraint, THE derived source SHALL retain bounded provenance for relative file,
line, declaring class, method, annotation, framework, exact revision and
configured confidence, and SHALL summarize completeness conservatively.

### Acceptance criteria

- GIVEN cualquier claim WHEN entra al catálogo THEN su safe provenance identifica
  path relativo normalizado, línea positiva, clase/método y annotation/framework
  pertinentes, mientras source metadata liga repository/revision exactos.
- GIVEN inputs equivalentes con separadores u orden de lectura distintos WHEN se
  analizan THEN provenance, operations, diagnostics y digest son idénticos.
- GIVEN error material, dependencia ausente, custom behavior o branch no resuelto
  WHEN afecta un fact THEN este/operación/source no puede quedar `complete`.
- GIVEN contenido que intenta declararse authoritative/high confidence WHEN se
  parsea THEN clase y confianza siguen siendo las fijadas por trusted composition.

## R9 — Resultados parciales y diagnósticos seguros

IF syntax, symbols, types, annotations or mappings cannot be resolved under the
supported rules, THEN THE inspector SHALL return independent safe facts with
bounded typed diagnostics, or a typed insufficient/limit outcome when no safe
operation remains, without fabricating claims.

### Acceptance criteria

- GIVEN syntax error o clase incompleta independiente WHEN existen controllers
  válidos fuera de su cierre THEN sobreviven con source partial y diagnóstico.
- GIVEN identidad method/path no resoluble para un mapping WHEN ocurre THEN ese
  mapping no se emite aunque parámetros/return parezcan analizables.
- GIVEN cero operaciones seguras, fallo de root/cierre obligatorio o límite que
  impide identidad WHEN finaliza THEN devuelve `insufficient_context` o
  `limit_exceeded`, no un catálogo vacío presentado como completo.
- GIVEN diagnósticos excedentes o parser text con secretos canario WHEN se limita
  THEN solo aparecen code/stage/safe location allowlisted y conteo de omitidos.

## R10 — Source sets, cierre de changed paths y selección explícita

WHERE trusted composition supplies source sets, roots and changed relative
paths, THE inspector SHALL analyze only the authorized sets while retaining every
bounded local dependency required to resolve affected operations.

### Acceptance criteria

- GIVEN controller cambiado que depende de DTO/constant/base local no cambiado
  WHEN se inspecciona THEN el cierre lo incluye y resuelve la operación.
- GIVEN DTO o constant cambiado y controller no cambiado WHEN se calcula impacto
  THEN las operaciones dependientes se marcan affected mediante cierre inverso.
- GIVEN source set `main` autorizado y sources en `test`, generated o root no
  listado WHEN se enumera THEN no se leen salvo inclusión explícita por policy.
- GIVEN changed-only cuyo cierre excede límite o contiene símbolo ausente WHEN se
  ejecuta THEN queda parcial/insuficiente; ausencia no demuestra que no existan
  rutas.
- GIVEN ausencia de changed paths WHEN se ejecuta THEN se parte de roots
  explícitos y nunca se escanea fuera de source sets autorizados.

## R11 — Límites, cancelación, determinismo y concurrencia

WHILE reading, parsing, resolving or projecting, THE inspector SHALL enforce
explicit budgets and context cancellation at every potentially long traversal
and SHALL isolate all mutable state per inspection.

### Acceptance criteria

- GIVEN límites de source sets, roots, files, bytes, units, AST nodes, symbols,
  closure depth, constants, mapping combinations, operations, schemas, generic
  depth o diagnostics WHEN se agotan THEN no comienzan nuevas unidades ni se
  excede el límite observable.
- GIVEN cancelación/deadline durante read, parse, index, closure, mapping o schema
  projection WHEN se observa THEN termina sin goroutines/tareas pendientes y
  conserva `context.Canceled`/`DeadlineExceeded` como causa.
- GIVEN mismo snapshot/config en distinto orden WHEN se repite o ejecuta en
  paralelo THEN snapshot/digest son iguales y el detector de carreras pasa.
- GIVEN duración configurada mayor que el deadline del caller WHEN se compone
  THEN prevalece el deadline más corto; el inspector no crea retries ni timers
  ocultos.

## R12 — Integración neutral y revalidación fail-closed

WHEN the Spring inspector is registered as a `ContractSource`, THE system SHALL
load it under a stable Spring source kind as class `derived`, apply the existing
sufficiency policy and revalidate revision, source, catalog and policy bindings
before model context, approval and execution.

### Acceptance criteria

- GIVEN input válido WHEN el registry carga `spring-static-v1` THEN verifica kind,
  class, adapter version, expected digest, limits/projection y snapshot mediante
  las invariantes de feature 21.
- GIVEN policy que no acepta derived, confianza/completitud insuficiente o
  conflictos con OpenAPI/Go/otra source WHEN se selecciona THEN la operación no
  llega al modelo ni a ejecución.
- GIVEN claims compatibles de Spring y otra source WHEN se combinan THEN conservan
  todas las procedencias; desacuerdos quedan como conflictos sin ganador local.
- GIVEN drift de revision, snapshot, build context, roots, catalog o policy entre
  planificación y ejecución WHEN se revalida THEN se invalida el binding.
- GIVEN declarative constraints Spring WHEN se proyectan THEN permanecen datos de
  contexto no ejecutables y no amplían tools, network policy ni autorización.

## R13 — Errores, observabilidad, comentarios y verificación

IF inspection emits errors, diagnostics, events or persisted metadata, THEN THE
system SHALL expose only closed categories and bounded safe identifiers, and
every new or modified entity/function/method/helper SHALL carry a systemic
responsibility comment.

### Acceptance criteria

- GIVEN canarios en filenames, packages, source, comments, string constants,
  annotations, expressions, roles o parser errors WHEN se procesa/falla THEN no
  aparecen en errors, logs, events ni catálogo fuera de campos estructurales
  permitidos y redactados.
- GIVEN eventos start/finish/partial/limit WHEN se observan THEN solo incluyen IDs
  seguros, revision/digests, framework, counts, limit kind, duration y outcome.
- GIVEN el diff WHEN se audita THEN cada entidad, constante de dominio, función,
  método y helper nuevo/modificado, incluidos fakes/spies/tests, explica su
  responsabilidad sistémica, frontera o invariante.
- GIVEN la suite WHEN se ejecuta THEN fixtures locales cubren la matriz v1,
  boundaries estáticas/spy prueban cero ejecución/red, race pasa y el código
  nuevo/modificado alcanza al menos 85 % de cobertura.

## Errores y casos límite

- Input o binding incoherente falla antes de leer el snapshot.
- Una unidad Java inválida no elimina operaciones independientes; sí degrada o
  invalida las que dependan de su cierre.
- Packages/imports ambiguos, wildcard imports conflictivos y duplicate classes se
  diagnostican; no se selecciona uno por orden de filesystem.
- Interfaces, inheritance y annotations heredadas solo se usan cuando la regla
  exacta esté definida en la lista v1; en otro caso quedan unresolved.
- `package-info.java`, `module-info.java`, resources, build files y bytecode no
  aportan mappings o schemas en v1.
- Duplicados equivalentes se canonizan; incompatibilidades se delegan al merger
  neutral.
- Líneas se calculan sobre los bytes retenidos por el snapshot y nunca reabriendo
  el archivo mediante un path del host.

## Decisiones abiertas

No hay decisiones de producto bloqueantes. Ampliar annotations, Spring WebFlux,
Kotlin, meta-annotations, inheritance compleja, Jackson runtime o integración con
un build requiere otra spec y no puede activarse como fallback silencioso.

## Registro de aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-20T11:18:38+02:00
```

La implementación no puede comenzar hasta que una persona apruebe explícitamente
requirements, diseño y tareas. El silencio o el paso del tiempo no constituyen
aprobación.
