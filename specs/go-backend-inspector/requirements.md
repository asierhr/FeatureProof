# Inspector estático de backends Go — Requirements

## Objetivo y problema

FeatureProof ya puede combinar fuentes de contrato mediante un
`OperationCatalog`, pero todavía no obtiene operaciones directamente de un
backend Go. Esta feature añade una fuente `derived` que inspecciona una revisión
local ya autorizada y deriva únicamente hechos demostrables por sintaxis y tipos
disponibles. El análisis debe ser hermético: leer código no concede permiso para
ejecutarlo, resolver dependencias dinámicamente ni acceder a red.

El resultado permite planificar con información parcial sin presentarla como un
contrato oficial. La ausencia, ambigüedad o construcción dinámica se conserva
como diagnóstico y reduce la completitud; nunca se rellena mediante convenciones
o suposiciones.

## Alcance

- Inspeccionar un snapshot de filesystem de solo lectura, acotado a un repositorio
  Go local y ligado a una revisión autorizada por composición.
- Reconocer un subconjunto explícito de registros de rutas de `net/http` y Chi.
- Resolver composición estática de routers, grupos, prefijos, handlers y tipos
  locales necesaria para las rutas seleccionadas.
- Derivar conservadoramente método, path, handler, parámetros de path/query,
  request bodies JSON, respuestas explícitas y middleware de autenticación.
- Producir un `SourceSnapshot` de clase `derived`, integrable en el
  `OperationCatalog` de la feature 21, con confianza y completitud controladas por
  metadata confiable externa.
- Emitir diagnósticos tipados, acotados y seguros, además de destacar operaciones
  afectadas por un conjunto opcional de cambios de pull request.
- Aplicar límites explícitos de archivos, bytes, paquetes, nodos, profundidad,
  tipos, operaciones, diagnósticos y duración, con cancelación propagada.
- Verificar todo el comportamiento con repositorios fixture locales y fronteras
  que detecten cualquier intento de proceso, red o lectura fuera del snapshot.

## Fuera de alcance

- Ejecutar el backend, tests, benchmarks, `go list`, `go test`, `go generate`,
  scripts, plugins, compiladores, binarios del repositorio o hooks de Git.
- Descargar módulos, consultar proxies, VCS, Internet, servicios desplegados o
  almacenes de credenciales.
- Evaluar reflection, inicializadores con efectos, generación de código,
  configuración en runtime, service discovery o registros creados dinámicamente.
- Cubrir routers distintos de `net/http` y `github.com/go-chi/chi/v5` en v1.
- Demostrar semántica completa de middleware arbitrario, validadores, encoding,
  reglas de negocio o todas las respuestas posibles de un handler.
- Presentar el catálogo derivado como `authoritative` o resolver conflictos con
  fuentes de mayor confianza.
- Modificar la autorización de repositorios/revisiones, la política de
  suficiencia, el planificador, el ejecutor o el cálculo de veredictos salvo la
  composición mínima necesaria para registrar esta fuente.

## Actores y términos

- **Trusted composition:** capa que autoriza repository ID, revisión, snapshot de
  filesystem, roots, cambios PR, límites, clase, confianza, completitud y policy.
- **Repository snapshot:** capacidad de lectura inmutable y enraizada; devuelve
  paths relativos y no permite escapar por `..`, paths absolutos o symlinks.
- **Inspection root:** paquete o archivo de entrada desde el que comienza el
  descubrimiento. Los cambios PR pueden seleccionar roots, pero no recortar su
  cierre estático necesario.
- **Static closure:** archivos, declaraciones, builders, handlers y tipos locales
  alcanzables necesarios para resolver una ruta; incluirlos no afirma que otras
  rutas del repositorio no existan.
- **Resolved fact:** hecho demostrado por uno de los patrones v1 y, cuando sea
  necesario, por información de tipos local disponible.
- **Partial fact:** hecho demostrado solo en parte; no autoriza inferir las partes
  ausentes.
- **Unresolved diagnostic:** razón tipada por la que un constructo no puede
  proyectarse con seguridad.
- **Affected operation:** operación cuya ruta, handler o cierre estático toca un
  path marcado como cambiado; es metadata de impacto y no eleva confianza.

## Patrones sintácticos v1 soportados

La lista es cerrada. Deben usarse string constants o concatenaciones de constants
resueltas localmente; valores variables, `fmt.Sprintf`, reflection y llamadas
arbitrarias son dinámicos.

### `net/http`

- `http.Handle`, `http.HandleFunc`, `(*http.ServeMux).Handle` y
  `(*http.ServeMux).HandleFunc`.
- Solo producen operación cuando el pattern Go 1.22 contiene método explícito,
  por ejemplo `"GET /users/{id}"`. Un pattern sin método conserva la ruta como
  diagnóstico parcial y no fabrica uno o varios métodos.
- Se resuelven handlers como identificador, selector de método o function literal
  local. Wrappers arbitrarios se marcan no resolubles.
- `(*http.Request).PathValue("name")` se reconoce como parámetro de path solo si
  el nombre coincide con el template estático.

### Chi v5

- Métodos verbales `Get`, `Post`, `Put`, `Patch`, `Delete`, `Head`, `Options`,
  `Connect` y `Trace`, además de `Method(<constant-method>, path, handler)`.
- `Route(prefix, func(r chi.Router) {...})`, `Group(func(r chi.Router) {...})`,
  `With(middleware...).<verb|Method>`, `Use(middleware...)` y builders locales que
  reciben/devuelven el mismo router cuando el flujo y los argumentos son
  estáticos y acíclicos.
- `Mount` y `Handle` sin método solo se resuelven si el router/handler montado se
  reduce completamente a registros soportados con métodos explícitos; en otro
  caso generan diagnóstico parcial.
- `chi.URLParam(request, "name")` se reconoce como parámetro de path únicamente
  si coincide con el template efectivo.

### Request, response y auth

- Query: `request.URL.Query().Get("name")`, `Has("name")` y acceso indexado con
  key constante. La lectura demuestra existencia del parámetro, no obligatoriedad;
  `required` permanece `false` salvo una guarda soportada que rechace ausencia y
  termine el flujo.
- Body JSON: `json.NewDecoder(request.Body).Decode(&value)` hacia structs, aliases,
  punteros, arrays/slices, maps de string y escalares locales resolubles. Se usan
  tags `json`; campos ignorados no aparecen. La obligatoriedad del body o de un
  campo solo se afirma cuando el patrón soportado la demuestra; punteros y
  `omitempty` no se marcan obligatorios.
- Responses: status constants/int literals en `WriteHeader` y `http.Error`, y
  status 200 implícito únicamente en una escritura explícita alcanzable. Un schema
  de response se deriva solo para `json.NewEncoder(writer).Encode(value)` con tipo
  resoluble y media type JSON explícita; los demás cuerpos no se inventan.
- Auth: `Use`/`With` o wrappers coincidentes con una allowlist externa de símbolos
  de middleware exactos pueden producir un requisito de seguridad cuyo scheme ID
  también viene de trusted composition. Leer `Authorization`, nombres sugerentes
  o middleware no registrado no demuestra autenticación.

## R1 — Snapshot y revisión autorizados

WHEN an inspection is requested, THE Go inspector SHALL accept only a bounded
read-only repository snapshot bound by trusted composition to an exact repository
and commit, and SHALL NOT resolve or change that authority itself.

### Acceptance criteria

- GIVEN repository ID, commit completo, source metadata, roots y filesystem
  autorizados WHEN comienza el análisis THEN quedan sellados en el input y en el
  digest de la fuente derivada.
- GIVEN una revisión ausente/inválida, snapshot no enraizado, source class distinta
  de `derived`, metadata incompleta o digest inesperado WHEN se carga la fuente
  THEN falla antes de leer archivos o crear un catálogo.
- GIVEN un path absoluto, `..`, symlink que escapa o cambio PR fuera del root WHEN
  se valida el input THEN se rechaza con error tipado y sin revelar la ubicación.

## R2 — Análisis hermético sin ejecución ni red

WHILE inspecting a repository, THE Go inspector SHALL use only bounded file reads,
Go parsing, AST traversal and locally available type information, and SHALL NOT
start processes, execute repository code or access network, credentials or files
outside the authorized snapshot.

### Acceptance criteria

- GIVEN fixtures que contienen `go:generate`, scripts, tests, init functions,
  tool directives y binarios WHEN se inspeccionan THEN ninguno se ejecuta.
- GIVEN imports no disponibles localmente WHEN se analizan THEN se conserva el
  resultado demostrable por AST y se emite diagnóstico parcial; no se llama a
  `go list`, module proxy, VCS ni un importer que lea fuera del snapshot.
- GIVEN spies de filesystem/proceso/red/credenciales WHEN corre la suite THEN solo
  se observan lecturas permitidas del snapshot y cero llamadas a las otras
  capacidades.

## R3 — Descubrimiento de operaciones `net/http`

WHEN code uses a supported `net/http` registration pattern with a statically
resolved method, path and handler, THE inspector SHALL emit the corresponding
derived operation and exact handler symbol.

### Acceptance criteria

- GIVEN funciones globales y `ServeMux` con patterns Go 1.22 estáticos WHEN se
  analizan THEN método, template efectivo y handler corresponden al código.
- GIVEN un pattern legacy sin método, host pattern, valor dinámico o wrapper no
  soportado WHEN se analiza THEN no se fabrican métodos ni una operación ejecutable;
  se emite un diagnóstico acotado con completitud parcial.
- GIVEN aliases de imports o de constants locales WHEN son inequívocos THEN se
  resuelven sin depender del nombre textual de la variable o import.

## R4 — Descubrimiento de operaciones Chi y composición estática

WHEN code uses supported Chi v5 registrations, groups or prefixes, THE inspector
SHALL compose their statically provable effective method/path/handler while
preserving inherited middleware.

### Acceptance criteria

- GIVEN verb methods, `Method`, `Route`, `Group`, `Use` y `With` anidados con
  constants WHEN se inspeccionan THEN los paths efectivos, handlers y middleware
  respetan orden y scope.
- GIVEN un builder local estático llamado desde un root WHEN se necesita para una
  ruta THEN su cierre se recorre aunque el archivo no esté marcado como cambiado.
- GIVEN ciclos, routing condicional, loops no acotables, `Mount` dinámico, receiver
  desconocido o prefix calculado WHEN se encuentra THEN se corta esa rama, se
  marca parcial/unresolved y no se concatena un path supuesto.

## R5 — Parámetros de path y query conservadores

WHEN a supported handler reads a statically named path or query value, THE
inspector SHALL derive only the parameter name, location and schema facts proven
by that access and the effective route template.

### Acceptance criteria

- GIVEN `PathValue` o `chi.URLParam` cuyo nombre coincide con el template WHEN se
  inspecciona THEN aparece un parámetro path string requerido.
- GIVEN `Query().Get`, `Has` o indexación con key constante WHEN se inspecciona
  THEN aparece un parámetro query string, sin marcarlo requerido por la lectura
  sola.
- GIVEN nombre dinámico, parámetro de path ausente del template, conversión o
  validación no soportada WHEN se analiza THEN no se atribuye schema/required
  adicional y se registra diagnóstico cuando afecta a la completitud.

## R6 — Request bodies y schemas locales

WHEN a handler decodes JSON through the supported pattern into a locally resolved
Go type, THE inspector SHALL project the bounded structural subset it can prove
without executing validators or constructors.

### Acceptance criteria

- GIVEN structs locales con tags JSON, composición de tipos soportada y límites
  suficientes WHEN se analizan THEN el request body y schema neutral conservan
  nombres, tipos, items y propiedades demostrables.
- GIVEN campos ignorados, no exportados, punteros, `omitempty`, interfaces,
  generics no resolubles, custom unmarshalling o tipos externos ausentes WHEN se
  proyectan THEN no se inventa obligatoriedad ni estructura; el nodo se omite o
  queda parcial con diagnóstico tipado.
- GIVEN un ciclo de tipos o profundidad/tamaño agotado WHEN se recorre THEN se
  detiene de forma determinista y no emite un schema truncado como completo.

## R7 — Respuestas explícitas y middleware de autenticación

WHEN a handler contains supported explicit response or configured authentication
patterns, THE inspector SHALL derive only those exact response selectors, JSON
schemas and security requirements whose control-flow scope is provable.

### Acceptance criteria

- GIVEN `WriteHeader`, `http.Error` o escritura/encode explícita con status
  resoluble WHEN se inspecciona THEN se conservan los status demostrables; JSON
  incluye schema solo con tipo y media type demostrables.
- GIVEN paths de control divergentes o respuesta dinámica WHEN no pueden
  enumerarse dentro de límites THEN se mantienen los status resueltos y la
  respuesta/operación queda parcial, sin afirmar exhaustividad.
- GIVEN middleware cuyo símbolo exacto y scheme están en trusted configuration
  WHEN aplica por scope THEN la operación incluye ese requisito; middleware no
  allowlisted, nombres o lecturas de headers no crean auth.

## R8 — Procedencia, confianza y completitud

WHEN the inspector emits a claim, THE derived source SHALL retain bounded
provenance for the exact file, line, symbol, framework, repository revision and
configured confidence, and SHALL conservatively summarize completeness.

### Acceptance criteria

- GIVEN cualquier claim de identity/schema/security/response WHEN entra al
  catálogo THEN su safe location permite recuperar path relativo normalizado,
  línea, símbolo y framework, y su source metadata liga la revisión exacta.
- GIVEN paths Windows/POSIX, roots equivalentes o distinto orden de lectura WHEN
  se inspecciona THEN provenance, operaciones y digest son canónicos y estables.
- GIVEN un diagnóstico material, dependencia ausente o branch no resuelto WHEN
  afecta una operación THEN esta no puede quedar `complete`; contenido de fuente
  nunca eleva clase o confianza configuradas externamente.

## R9 — Resultados parciales y diagnósticos seguros

IF syntax, types, packages, registrations or schemas cannot be resolved by the
supported static rules, THEN THE inspector SHALL return resolved independent
facts plus bounded typed diagnostics, or typed insufficient context when no safe
operation remains, without fabricating claims.

### Acceptance criteria

- GIVEN errores de sintaxis en un archivo no necesario WHEN otras rutas son
  independientes THEN se devuelven esas rutas y un diagnóstico; la fuente queda
  parcial.
- GIVEN error en identidad method/path de una ruta WHEN ocurre THEN esa ruta no se
  emite, aunque otros facts del handler parezcan reconocibles.
- GIVEN cero operaciones seguras, límite que impide conocer identidad o fallo del
  root/cierre obligatorio WHEN termina THEN el resultado es
  `insufficient_context`/`limit_exceeded`, no un catálogo vacío presentado como
  completo.
- GIVEN diagnósticos excedentes WHEN se alcanza el máximo THEN se agrega un conteo
  de omitidos sin texto libre, snippets, paths absolutos ni errores crudos.

## R10 — Límites, cancelación y determinismo

WHILE parsing, resolving or projecting, THE inspector SHALL enforce explicit
limits and context cancellation at every potentially long traversal and SHALL
produce deterministic all-or-nothing state for the completed portion.

### Acceptance criteria

- GIVEN límites de archivos, bytes, paquetes, AST nodes, profundidad, tipos,
  operaciones o diagnósticos WHEN uno se agota THEN no comienzan nuevas unidades,
  el resultado/error identifica la categoría segura y no excede el límite.
- GIVEN cancelación o deadline antes o durante lectura, parse, closure, types o
  projection WHEN se observa THEN finaliza sin goroutines pendientes y preserva
  `context.Canceled`/`DeadlineExceeded` como causa tipada.
- GIVEN el mismo snapshot/configuración en diferente orden de directory entries
  WHEN se inspecciona repetidamente THEN snapshot, diagnósticos, impacto y digest
  son idénticos.

## R11 — Cambios PR e integridad del cierre estático

WHERE trusted composition supplies changed relative paths, THE inspector SHALL
use them only to select/highlight affected roots and SHALL include every bounded
static dependency required to resolve those roots.

### Acceptance criteria

- GIVEN un handler cambiado que depende de router/types no cambiados WHEN se
  inspecciona en modo PR THEN esas dependencias se leen y la operación se resuelve.
- GIVEN un router no cambiado que registra un handler cambiado WHEN se calcula
  impacto THEN la operación aparece como afectada sin ocultar el registro.
- GIVEN un changed-only scope cuyo cierre excede límites o contiene una dependencia
  no disponible WHEN se ejecuta THEN queda parcial/insuficiente; no se trata la
  ausencia como prueba de que no existen rutas.
- GIVEN ausencia de changed paths WHEN se analiza THEN se usan roots explícitos y
  no se escanea implícitamente fuera de ellos.

## R12 — Integración como fuente `derived` y policy fail-closed

WHEN the Go inspector is registered as a `ContractSource`, THE system SHALL map
its output to the neutral catalog as class `derived` and SHALL require the
existing explicit sufficiency policy before model context, approval or execution.

### Acceptance criteria

- GIVEN input válido WHEN el registry carga `go-static-v1` THEN valida metadata y
  snapshot con las mismas invariantes de source/digest/limits de la feature 21.
- GIVEN policy que no acepta clase derived, confianza/completitud insuficiente o
  conflictos con otra fuente WHEN se selecciona THEN la operación no llega al
  modelo ni ejecución y se devuelve insufficient/review required.
- GIVEN catálogo combinado con OpenAPI y análisis Go compatibles WHEN se construye
  THEN claims iguales conservan ambas procedencias; desacuerdos permanecen como
  conflictos y el inspector no elige ganador.
- GIVEN drift de revisión, configuración, archivos, catálogo o policy entre
  planificación y ejecución WHEN se revalida THEN el binding queda invalidado.

## R13 — Errores, observabilidad, comentarios y pruebas

IF inspection or integration emits errors, diagnostics, events or persisted
metadata, THEN THE system SHALL expose only closed categories and bounded safe
identifiers, and every new or modified entity/function/method/helper SHALL carry
a systemic responsibility comment.

### Acceptance criteria

- GIVEN canarios en filenames, source, tags, literals, comments, imports o parser
  errors WHEN falla el análisis THEN no aparecen en errores, diagnósticos, logs,
  catálogo público ni eventos fuera de claims estructurales permitidos.
- GIVEN eventos de inicio/fin/límite/partial WHEN se observan THEN contienen IDs,
  revisión/digests, framework, conteos, duración y outcome seguros, sin source
  snippets ni paths absolutos.
- GIVEN el diff completo WHEN se audita THEN toda entidad, constante de dominio,
  función, método y helper nuevo o modificado, incluidos fakes/helpers de tests,
  explica su responsabilidad sistémica, frontera o invariante.
- GIVEN la suite de la feature WHEN se ejecuta THEN usa solo fixtures locales,
  cubre ambos frameworks y todas las fronteras anteriores, pasa race y alcanza al
  menos 85 % de cobertura del código nuevo/modificado.

## Errores y casos límite

- Input inválido, revisión/digest incoherente y escape del snapshot fallan antes
  de analizar.
- Un archivo con syntax error no invalida hechos independientes; sí invalida el
  cierre que dependa de él.
- Build tags se evalúan solo desde una configuración explícita y cerrada. Archivos
  cuya selección no pueda decidirse se omiten con diagnóstico; no se consulta el
  host para inferir GOOS/GOARCH/tags.
- `testdata`, vendor, generated files y `_test.go` no se leen salvo que trusted
  composition los incluya explícitamente en roots/policy; generated code nunca se
  regenera.
- Duplicados equivalentes se canonizan; identidades incompatibles se delegan al
  conflicto neutral del catálogo.
- Líneas y símbolos se calculan sobre los bytes del snapshot, nunca sobre paths o
  archivos reabiertos fuera de él.

## Decisiones abiertas

No hay decisiones de producto bloqueantes. El subconjunto v1 queda cerrado en
esta spec. Añadir frameworks, patrones de control-flow o inferencias requiere una
feature/spec posterior para no ampliar silenciosamente la autoridad derivada.

## Registro de aprobación humana

```yaml
status: approved
approved_by: user
approved_at: 2026-10-19T16:16:15+02:00
```

La implementación no puede comenzar hasta que una persona apruebe explícitamente
requirements, diseño y tareas. El silencio o el paso del tiempo no constituyen
aprobación.
