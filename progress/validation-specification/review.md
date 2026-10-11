# Segunda review de validation-specification

## Veredicto

**Aprobada.** La implementación satisface R1–R7, las correcciones de REV-1,
REV-2 y REV-3 son completas, y todas las comprobaciones obligatorias pasan de
forma independiente. No quedan hallazgos bloqueantes.

## Precondiciones

- Existe exactamente una feature `in_progress`: `validation-specification`
  (id 2).
- La aprobación humana sigue registrada en `requirements.md`:
  `approved_by: user`, `approved_at: 2026-10-11T16:51:43+02:00`.
- T1–T5 están marcadas `[x]`.
- Existe y fue revisado
  `progress/validation-specification/implementation.md`.
- Se releyeron la spec completa, el modelo, el validador, el adapter, todos los
  tests y fixtures de la feature, `go.mod` y `go.sum`.

## Correcciones de la primera review

### REV-1 — Resuelta

`internal/specification/yaml/loader.go` valida `Kind` y `Tag` antes de traducir
los campos v1: `requireString` exige escalar `!!str`, `requireInteger` exige
escalar `!!int`, y los mappings y la secuencia de constraints también validan
su clase de nodo. La versión se inspecciona antes del decode v1. Ningún fallo
devuelve un modelo parcial.

`TestLoadRejectsWrongScalarTypesWithFieldPathAndNoPartialModel` demuestra
`invalid_value`, field path estable y modelo cero para números, booleanos y
`null` en campos string, y strings en los tres campos integer. El test cubre
feature, target, constraints y limits. Además,
`TestLoadRejectsNullConstraintWithFieldPathAndNoPartialModel` comprueba una
entrada `constraints[0]` nula con el mismo contrato de error.

### REV-2 — Resuelta

`syntaxError` usa `fmt.Errorf("decode YAML: %w", cause)`, por lo que conserva la
identidad de la causa original a través de `ValidationError.Unwrap`.
`TestSyntaxErrorPreservesOriginalCauseIdentity` utiliza una causa canario y
demuestra la cadena real mediante `errors.Is`; los tests públicos mantienen las
comprobaciones de categoría y tipo mediante `errors.Is`/`errors.As`.

### REV-3 — Resuelta

El perfil `specification` ya no existe. Una búsqueda recursiva antes y después
de la verificación no encontró `specification`, `coverage.out`, nombres de
coverage ni archivos cuyo encabezado fuera `mode: set`. El perfil de esta
review se creó en el directorio temporal del sistema y se eliminó al terminar.

## Trazabilidad R1–R7

| Requirement | Evidencia revisada | Resultado |
|---|---|---|
| R1 | `Load`, `decodeOne`, `translate`; carga válida, lectura, sintaxis, roots y múltiples documentos | Conforme |
| R2 | DTO YAML privados, retorno exclusivo de modelos internos, cargas repetidas y concurrentes | Conforme |
| R3 | required, tipos, IDs, URL, límites, duplicados, referencias y campos desconocidos; siempre modelo cero al fallar | Conforme |
| R4 | extracción prioritaria de versión y `UnsupportedVersionError{Received, Supported}` | Conforme |
| R5 | categorías propias, field paths estables, localización sintáctica, orden determinista y causa envuelta | Conforme |
| R6 | solo lectura del archivo local; sin imports o llamadas de red/environment; literales no expandidos | Conforme |
| R7 | fixtures válidos e inválidos v1 y equivalencia determinista | Conforme |

La dirección de dependencias permanece correcta: el dominio `specification`
no importa YAML ni harness, mientras que el adapter depende del modelo interno.
Los tipos externos quedan confinados al subpaquete YAML. El loader no conserva
estado mutable, no crea goroutines, observa el contexto antes y después de la
lectura y no realiza I/O bajo locks. El harness no fue modificado y su suite
completa conserva el comportamiento previo.

## Verificación independiente

Entorno usado: `GOPATH=C:\Users\asier\AppData\Local\Temp\featureproof-go125-gopath`,
`GOTOOLCHAIN=go1.25.1`; para race, `CGO_ENABLED=1`, `CC=gcc` y
`C:\msys64\ucrt64\bin` al inicio de `PATH`.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -race -count=20 ./internal/specification/...` | pass |
| `go test -coverprofile=<ruta temporal> ./...` | pass |
| `go tool cover -func=<ruta temporal>` | pass |

Coverage observado:

- global: 90,1 %;
- `internal/specification`: 97,2 %;
- `internal/specification/yaml`: 87,0 %.

Los dos paquetes modificados superan el mínimo de 85 %, y las ramas relevantes
de R1–R7 tienen tests de comportamiento concretos. El race detector completo y
las 20 repeticiones de los paquetes afectados terminaron sin carreras.

## Hallazgos

Ninguno.
