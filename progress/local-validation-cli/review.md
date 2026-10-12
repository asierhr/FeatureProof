# Review final de local-validation-cli

## Veredicto

`approved`

La implementación satisface R1-R11, completa T1-T7 y conserva los contratos de
las features 1-6. Las tres rondas de revisión cerraron REV-1..REV-6 sin dejar
hallazgos bloqueantes.

## Precondiciones

- `local-validation-cli` es la única feature `in_progress`.
- La spec fue aprobada por el usuario el
  `2026-10-11T21:15:19+02:00` y no cambió después.
- T1-T7 están marcadas `[x]`.
- Existe trazabilidad actualizada en
  `progress/local-validation-cli/implementation.md`.
- Se inspeccionaron el código productivo, los tests y las regresiones de las
  tres iteraciones de review.

## Cierre de hallazgos

| Hallazgo | Resultado verificado |
|---|---|
| REV-1 | El decoder estricto rechaza campos desconocidos dentro de `reference` y `pathSegment`. |
| REV-2 | `RunCommand` conserva category y field seguros para specification, manifest, redaction, HTTP y planning. |
| REV-3 | Filesystem inyectable, matriz de fallos y commit no-replace sin sobrescritura. |
| REV-4 | Cancelación determinista probada durante policy, HTTP, evidence y assertions. |
| REV-5 | Entidades y funciones nuevas o modificadas incluyen comentario de responsabilidad sistémica. |
| REV-6 | Ninguna clave externa se reproduce en paths públicos ni en stderr; el fallback cerrado es `document`. |

## Auditoría de diagnósticos

Los orígenes dinámicos se revisaron de extremo a extremo:

- Specification YAML usa nombres fijos del schema o `members[n]`.
- La detección JSON de duplicate keys usa `members[n]` y no copia la key.
- Manifest inputs y objetos anidados se recorren en orden estable y usan
  `members[n]`.
- Planning conserva índices estables durante validación, ancestry y resolución.
- Assertion definitions solo muestran el vocabulario fijo v1; las keys externas
  usan `members[n]`.
- HTTP input, query, headers y body usan índices estables.
- `safeField` admite únicamente el vocabulario cerrado y los índices decimales;
  cualquier otro path se convierte en `document`.

Las regresiones reales de `RunCommand` cubren CR, LF, tabulador, control byte,
separadores y canarios. Cada escenario devuelve una sola línea, conserva una
categoría y ruta útil y no reproduce contenido externo. La prueba overlay que
demostró REV-6 en la segunda revisión ahora pasa.

## Publicación y cancelación

`Publisher` usa `FileSystem`/`SyncFile` para inyectar fallos de preparación,
creación, escritura, sync, commit de artefactos, sync de directorio y commit
final. Los fallos no exponen el directorio final y conservan staging
`.incomplete` cuando corresponde.

El commit final usa:

- Windows: rename de directorio, probado con dos publishers concurrentes en
  este host; solo uno publica.
- Linux: `renameat2` con `RENAME_NOREPLACE`.
- macOS: `renameatx_np` con `RENAME_EXCL`.
- Otras plataformas: fallo cerrado.

Linux y macOS compilan seleccionando sus adapters por build tags. La suite
concurrente real pasa en Windows; las primitivas Unix fueron verificadas por
inspección y cross-build.

Policy, evidence y evaluator son fronteras sustituibles. Los tests cancelan
con canales, sin sleeps, y prueban código 130, prefijo confirmado, ausencia de
trabajo posterior y bundle `cancelled` o staging incomplete según la
finalización.

## Trazabilidad R1-R11

| Requirement | Evidencia principal revisada | Estado |
|---|---|---|
| R1 | parser exacto, flags y wrapper del proceso | pass |
| R2 | target efectivo y validación previa a efectos | pass |
| R3 | strict JSON, configuración previa y diagnóstico seguro | pass |
| R4 | un plan ordenado por constraint y ejecución secuencial | pass |
| R5 | run ID, fases, resúmenes y canarios en todas las superficies | pass |
| R6 | agregación passed/failed/inconclusive y evidence IDs | pass |
| R7 | bundle completo, referencias verificadas y commit atómico | pass |
| R8 | matriz de fallos sin publicación parcial | pass |
| R9 | códigos 0/1/2/3/130 y separación stdout/stderr | pass |
| R10 | cancelación en todas las fronteras exigidas | pass |
| R11 | dobles locales, aislamiento, concurrencia y race | pass |

## Verificaciones ejecutadas

| Comando | Resultado |
|---|---|
| `gofmt -l .` con fallo ante salida | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `go test -race -count=1 ./...` con MSYS2 UCRT en `PATH` | pass fresco |
| `go test -race -count=20 ./cmd/featureproof ./internal/localvalidation ./internal/planmanifest ./internal/planning ./internal/assertion ./internal/specification/yaml ./internal/tools/http` | pass |
| `go test -coverprofile=<temp>/featureproof-review7-third.cover ./...` | pass |
| `go tool cover -func=<temp>/featureproof-review7-third.cover` | 91,0 % global |
| cross-build `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` | pass |
| cross-build `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0` | pass |
| overlay de REV-1: unknown nested reference field | pass |
| overlay de REV-2: category+field de versión inválida | pass |
| overlay de REV-6: key hostil en diagnóstico | pass |

Coverage observado:

- Global: **91,0 %**.
- `cmd/featureproof`: **100,0 %**.
- `internal/localvalidation`: **92,3 %**.
- `internal/planmanifest`: **92,7 %**.
- `internal/planning`: **89,9 %**.
- `internal/assertion`: **90,6 %**.
- `internal/specification/yaml`: **87,5 %**.
- `internal/tools/http`: **91,1 %**.

## Solicitud

Se solicita la transición de `local-validation-cli` de `in_progress` a `done`.
