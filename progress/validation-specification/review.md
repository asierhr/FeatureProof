# Review de validation-specification

## Veredicto

**Rechazada.** Las comprobaciones automáticas pasan, pero permanecen tres
hallazgos bloqueantes: el adapter acepta escalares YAML con tipos ajenos al
contrato en campos `string`, los errores de parser pierden su causa original y
el árbol contiene un perfil de coverage fuera del alcance declarado.

## Precondiciones

- Existe exactamente una feature `in_progress`: `validation-specification`
  (id 2).
- `requirements.md` registra aprobación humana de `user` en
  `2026-10-11T16:51:43+02:00`.
- T1–T5 están marcadas `[x]`.
- Existe `progress/validation-specification/implementation.md`.
- La spec completa y los archivos declarados como modificados fueron
  inspeccionados. El checkout no contiene metadatos Git, por lo que el alcance
  se contrastó contra el informe, el árbol actual y los archivos indicados por
  el leader.

## Trazabilidad revisada

| Requirement | Implementación | Tests reales inspeccionados | Resultado de review |
|---|---|---|---|
| R1 | `specyaml.Load`, `decodeOne`, `translate` | `TestLoadV1Specification`, `TestLoadRejectsUnreadableMalformedAndMultipleDocuments`, `TestLoadRejectsInvalidRoots` | Conforme |
| R2 | DTO v1 privados y traducción a `specification.Specification` | `TestLoadV1Specification`, `TestV1LoadIsDeterministicAndStateless`, `TestV1LoadsConcurrentlyWithoutSharedState` | Conforme |
| R3 | `checkV1Fields` y `specification.Validate` | Tests de required, duplicados, referencias, campos desconocidos y valores | No conforme: REV-1 |
| R4 | extracción de versión antes del esquema v1 y `UnsupportedVersionError` | `TestLoadRejectsUnsupportedVersionWithReceivedAndSupported`, `TestUnsupportedVersionErrorReportsContract` | Conforme |
| R5 | `ValidationError`, categorías, field paths y `syntaxError` | tests de categorías, primer error y localización | No conforme: REV-1 y REV-2 |
| R6 | lectura local mediante `os.ReadFile`, sin imports de red/environment | `TestV1CompatibilityFixtures`, `TestLoadDoesNotRequireEnvironment`, `TestLoadObservesPreCancelledContext` | Conforme |
| R7 | contrato v1 y fixtures locales | `TestV1CompatibilityFixtures`, `TestV1InvalidFixturesRemainRejected` | Conforme, sujeto a corregir REV-1 |

La inspección confirma DTO y tipos de `go.yaml.in/yaml/v3` confinados al
adapter, modelo interno independiente de `harness`, loader sin estado global
mutable, ausencia de goroutines de producción, cancelación antes y después de
la lectura, y ausencia de accesos a red o variables de entorno. La suite
completa confirma que el harness conserva su comportamiento observable.

## Verificación independiente

Entorno: `GOPATH=C:\Users\asier\AppData\Local\Temp\featureproof-go125-gopath`,
`GOTOOLCHAIN=go1.25.1`; para race también `CGO_ENABLED=1`, `CC=gcc` y
`C:\msys64\ucrt64\bin` al inicio de `PATH`.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -race -count=20 ./internal/specification/...` | pass |
| `go test -coverprofile=<temp> ./...` | pass |
| `go tool cover -func=<temp>` | pass; total 90,8 % |

Coverage de los paquetes nuevos: `internal/specification` 97,2 % e
`internal/specification/yaml` 89,3 %. Ambos superan el mínimo de 85 %, pero el
porcentaje no cubre los defectos contractuales siguientes.

## REV-1 — Se aceptan tipos YAML ajenos al contrato en campos string

- Severidad: blocker
- Requirement o regla: R3, R5, contrato YAML v1 de `requirements.md`,
  CONV-009
- Archivo y línea: `internal/specification/yaml/loader.go:202` y
  `internal/specification/yaml/loader.go:85`; cobertura insuficiente en
  `internal/specification/yaml/loader_test.go:174`
- Evidencia: `checkV1Fields` comprueba mappings, claves y secuencias, pero no
  comprueba que `feature.id`, descripciones, `target.id`, `base_url` y los
  campos de cada constraint tengan tag YAML `!!str`. Después, `root.Decode`
  decodifica directamente a campos Go `string`. La implementación usada de
  `go.yaml.in/yaml/v3` convierte cualquier escalar a `string` en
  `decode.go:608-617` mediante `out.SetString(n.Value)`. Por tanto, por ejemplo,
  `feature.id: 123` o `feature.description: true` se convierten en `"123"` y
  `"true"` y pueden superar `Validate`, aunque el contrato exige tipo YAML
  `string`. El único test de tipos añade `max_steps: "8"`; no cubre escalares
  no string en campos string. Los errores de tipo producidos por el decode de
  enteros tampoco incluyen el field path determinable y se reclasifican como
  `syntax`.
- Impacto: un documento no conforme puede producir una `Specification`
  utilizable, incumpliendo la validación completa previa al run y debilitando
  la compatibilidad estable de v1. En otros casos el usuario recibe una
  categoría/ruta no accionable.
- Corrección requerida: validar `Kind`/`Tag` de todos los escalares del contrato
  antes de traducir, devolver `ValidationError` con categoría y field path
  estables para cada tipo inválido, y añadir tests que demuestren rechazo sin
  modelo parcial para números, booleanos y `null` en campos string, además de
  strings en campos integer.

## REV-2 — `syntaxError` no conserva la causa original

- Severidad: blocker
- Requirement o regla: R5, CONV-005
- Archivo y línea: `internal/specification/yaml/loader.go:138` y
  `internal/specification/yaml/loader_test.go:132`
- Evidencia: `syntaxError` crea la causa con
  `fmt.Errorf("decode YAML: %s", cause)`. El verbo `%s` copia el texto y rompe la
  cadena de errores, de modo que `errors.Is`/`errors.As` no pueden recuperar la
  causa original. `TestSyntaxErrorPreservesLocation` solo comprueba que
  `Unwrap()` no sea `nil`, por lo que pasa aunque la identidad de la causa se
  haya perdido. Esto contradice la regla explícita de conservar causas con
  `%w` y el criterio de R5 que exige conservar la causa del parser.
- Impacto: consumidores y diagnóstico no pueden distinguir ni inspeccionar la
  causa real de un fallo YAML; el error propio aparenta envolverla, pero solo
  contiene un error textual nuevo.
- Corrección requerida: envolver la causa con `%w` (o conservarla directamente
  añadiendo contexto sin perder identidad) y reforzar el test con una causa
  identificable comprobada mediante `errors.Is`/`errors.As`.

## REV-3 — Perfil de coverage generado dentro del repositorio

- Severidad: blocker
- Requirement o regla: alcance de `design.md`, CONV-010; cambio fuera de
  alcance según `.codex/agents/reviewer.md`
- Archivo y línea: `specification` (raíz del repositorio; archivo completo)
- Evidencia: existe un archivo de 13.679 bytes creado durante la implementación
  cuyo contenido comienza por `mode: set` y enumera bloques de coverage de
  `internal/specification`. No figura entre los archivos modificados del
  informe y no es código, fixture ni artefacto aprobado de la feature.
- Impacto: queda basura generada en el árbol entregable y el informe de alcance
  no representa todos los archivos creados.
- Corrección requerida: eliminar el perfil `specification`, generar futuros
  perfiles fuera del repositorio o en una ruta ignorada acordada y actualizar
  la evidencia de archivos modificados si corresponde.

## Condición para nueva review

Resolver REV-1, REV-2 y REV-3, añadir los tests señalados y repetir todas las
comprobaciones canónicas, incluido race completo, repetición del paquete y
coverage.
