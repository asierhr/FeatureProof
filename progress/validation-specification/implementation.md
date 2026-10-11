# Implementación de validation-specification

## Resultado

Se implementó el modelo interno `specification`, su validación semántica y
un adapter YAML v1 local, estricto, cancelable y sin estado. El adapter devuelve
exclusivamente modelos internos y errores propios de FeatureProof; no crea runs
ni modifica `internal/harness`.

## Requirements y tests

| Requirement | Tests reales | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestLoadV1Specification`, `TestLoadRejectsUnreadableMalformedAndMultipleDocuments`, `TestLoadRejectsInvalidRoots` | `internal/specification/yaml/loader_test.go` | pass |
| R2 | `TestLoadV1Specification`, `TestV1LoadIsDeterministicAndStateless`, `TestV1LoadsConcurrentlyWithoutSharedState` | `internal/specification/yaml/{loader,compatibility}_test.go` | pass |
| R3 | `TestValidateRejectsMissingRequiredFields`, `TestValidateRejectsDuplicateConstraintIDs`, `TestValidateRejectsUnknownTargetReference`, `TestLoadRejectsUnknownAndDuplicateKeys`, `TestLoadRejectsSemanticErrorsWithoutPartialModel`, `TestLoadRejectsWrongScalarTypesWithFieldPathAndNoPartialModel`, `TestLoadRejectsNullConstraintWithFieldPathAndNoPartialModel` | `internal/specification/validate_test.go`, `internal/specification/yaml/loader_test.go` | pass |
| R4 | `TestLoadRejectsUnsupportedVersionWithReceivedAndSupported`, `TestUnsupportedVersionErrorReportsContract` | `internal/specification/yaml/loader_test.go`, `internal/specification/validate_test.go` | pass |
| R5 | `TestValidationErrorIdentifiesCategoryAndField`, `TestValidationReturnsStableFirstError`, `TestValidationErrorsExposeStableCategoriesAndContext`, `TestSyntaxErrorPreservesLocation`, `TestSyntaxErrorPreservesOriginalCauseIdentity`, `TestLoadRejectsWrongScalarTypesWithFieldPathAndNoPartialModel` | `internal/specification/validate_test.go`, `internal/specification/yaml/{loader,loader_internal}_test.go` | pass |
| R6 | `TestV1CompatibilityFixtures`, `TestLoadDoesNotRequireEnvironment`, `TestLoadObservesPreCancelledContext` | `internal/specification/yaml/{compatibility,loader}_test.go` | pass |
| R7 | `TestV1CompatibilityFixtures`, `TestV1InvalidFixturesRemainRejected`, `TestV1LoadIsDeterministicAndStateless` | `internal/specification/yaml/compatibility_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Modelo interno y fronteras de IDs, URL, constraints y límites verificadas. |
| T2 | done | Errores tipados, categorías, field paths, duplicados, referencias y orden determinista verificados. |
| T3 | done | Adapter v1 estricto, versión prioritaria, sintaxis, claves, documentos extra, lectura y cancelación verificados. |
| T4 | done | Fixtures locales, literales de environment, URL no consultada, repetición y concurrencia verificadas. |
| T5 | done | Suite canónica, race detector, coverage y trazabilidad completados. |

## Comandos ejecutados

Todos los comandos finales se ejecutaron con `GOTOOLCHAIN=go1.25.1`,
`CGO_ENABLED=1`, `CC=gcc`, `C:\msys64\ucrt64\bin` al inicio de `PATH` y un
`GOPATH` temporal fuera del repositorio.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -race -count=20 ./internal/specification/...` | pass |
| `go test -coverprofile=<ruta temporal fuera del repo> ./...` | pass |
| `go tool cover -func=<ruta temporal fuera del repo>` | pass |

## Coverage

- Global: 90,1 % de statements.
- `internal/specification`: 97,2 %.
- `internal/specification/yaml`: 87,0 %.
- Todo el código nuevo supera el mínimo del 85 %.
- Las ramas relevantes de campos requeridos, tipos y valores inválidos,
  duplicados, referencias, versiones, roots, documentos extra, lectura,
  sintaxis, cancelación y concurrencia tienen pruebas observables.

## Archivos modificados

- `go.mod`, `go.sum`: dependencia mantenida `go.yaml.in/yaml/v3 v3.0.4` fijada.
- `internal/specification/model.go`.
- `internal/specification/validate.go` y `validate_test.go`.
- `internal/specification/yaml/loader.go`, `loader_test.go`,
  `loader_internal_test.go` y `compatibility_test.go`.
- `internal/specification/yaml/testdata/{valid_v1,invalid_syntax_v1,invalid_semantic_v1,unsupported_version}.yaml`.
- `specs/validation-specification/tasks.md`.
- `progress/current.md`.
- `progress/validation-specification/implementation.md`.

No se modificó `internal/harness` ni ninguna feature distinta de
`validation-specification`.

El perfil de coverage no forma parte del entregable: se generó en el directorio
temporal del sistema. También se eliminó el perfil no aprobado `specification`
que la primera verificación había dejado en la raíz.

## Correcciones tras la primera review

- **REV-1:** el adapter comprueba `Kind` y `Tag` YAML de todos los escalares
  antes de traducir. Números, booleanos y `null` en campos string, y strings
  en campos integer, devuelven `invalid_value` con field path estable y modelo
  interno cero.
- **REV-2:** `syntaxError` envuelve la causa original con `%w`; un test con
  causa canario demuestra su identidad mediante `errors.Is`.
- **REV-3:** se eliminó el perfil `specification`; toda cobertura posterior se
  generó fuera del repositorio.

## Riesgos o limitaciones

- La carga usa `os.ReadFile`; observa cancelación inmediatamente antes y
  después de la lectura, tal como define el diseño, pero el sistema operativo
  no interrumpe una lectura de archivo ya iniciada.
- La validación de `base_url` es exclusivamente sintáctica. No resuelve DNS ni
  comprueba disponibilidad, de acuerdo con el alcance aprobado.
- La librería YAML reporta algunos errores de parser con línea pero sin
  columna; el adapter conserva toda localización proporcionada y no inventa
  una columna ausente.
