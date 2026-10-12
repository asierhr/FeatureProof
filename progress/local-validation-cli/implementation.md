# Implementación de local-validation-cli

## Resultado

Se implementó `featureproof validate --spec PATH --plan PATH --target URL`
como flujo determinista sin modelo. El comando valida todos los contratos antes
de crear el run, reutiliza la tool HTTP, redacción, policy, executor y assertion
engine, ejecuta constraints secuencialmente con presupuesto global y publica un
bundle mediante staging y rename dentro del mismo parent.

El bundle contiene `run.json`, `events.jsonl`, `plan.json`, `evidence/`,
`report.json` y `report.md`. Los reportes y el progreso omiten payloads; el
snapshot del plan conserva runtime, topología, referencias, operadores y paths,
pero sustituye literales y valores esperados por `[REDACTED]`.

## Correcciones de la primera review

- **REV-1:** la unión `input` aplica decode estricto también a
  `outputReference` y sus `pathSegment`; las regresiones rechazan campos
  desconocidos en ambos niveles.
- **REV-2:** `RunCommand` proyecta errores tipados de specification, manifest,
  redaction, HTTP y planning a `category` y `field`, sin interpolar valores.
- **REV-3:** `Publisher` usa una frontera `FileSystem` inyectable. La matriz de
  fallos cubre mkdir, create, write, file sync, artifact rename, directory
  open/sync y final rename. El commit usa no-replace atómico en Windows,
  Linux y macOS y una prueba concurrente demuestra que solo un publisher gana.
- **REV-4:** policy, evidence store y assertion evaluator son fronteras
  sustituibles. Tests sin sleeps cancelan dentro de cada una y comprueban 130,
  bundle cancelled, prefijo confirmado y ausencia de trabajo posterior.
- **REV-5:** `constraintCatalog` y `jsonMarshal` tienen comentarios sistémicos;
  se auditó el resto de entidades y funciones nuevas o modificadas.

## Corrección de la segunda review

- **REV-6:** los paths originados en mapas externos ya no concatenan claves.
  Specification YAML, duplicate-key JSON, inputs del manifest, validación y
  resolución de planning, y definitions de assertions usan `members[n]` con
  índices estables; los nombres fijos del schema conservan su ruta legible.
- La proyección final de `RunCommand` aplica además un vocabulario cerrado de
  segmentos internos e índices decimales. Cualquier path ajeno cae en
  `document`, por lo que una regresión aguas arriba tampoco puede inyectar una
  clave en stderr.
- `TestRunCommandNeverRendersExternalMapKeysInDiagnostics` atraviesa
  specification, manifest, duplicate keys y planning con una clave que contiene
  saltos de línea, tabulador, control, separadores y canarios. Cada caso produce
  una sola línea, conserva category+field útil y no reproduce contenido externo.
- La auditoría de HTTP endurece también `InputError.Field` para claves de input,
  query, headers y body; `TestInputErrorsUseStableMemberIndices` prueba esa
  frontera aunque dichos errores de ejecución no se impriman por `RunCommand`.

## Requirements y tests

| Requirement | Tests | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestValidateCommandRequiresExactFlagsBeforeEffects`, `TestRunDelegatesInvalidArgumentsWithoutStartingAValidation` | `internal/localvalidation/command_test.go`, `cmd/featureproof/main_test.go` | pass |
| R2 | `TestInvalidCompositionProducesNoRunArtifactsOrNetwork`, `TestRunnerExecutesConstraintsSequentiallyWithoutModel` | `internal/localvalidation/runner_test.go` | pass |
| R3 | `TestManifestV1StrictlyValidatesRuntimeAndPlansBeforeRun`, `TestInputJSONDecoderPreservesAllUnionMembers`, `TestRunCommandReportsSafeTypedValidationDiagnostics`, `TestRunCommandNeverRendersExternalMapKeysInDiagnostics`, `TestSafeFieldFailsClosedForUntrustedSegments`, `TestNewRunnerRejectsRedactionAndAssertionConfigurationBeforeRun` | `internal/planmanifest/manifest_test.go`, `internal/localvalidation/runner_test.go` | pass |
| R4 | `TestRunnerRequiresOneOrderedPlanPerConstraint`, `TestRunnerExecutesConstraintsSequentiallyWithoutModel` | `internal/planmanifest/manifest_test.go`, `internal/localvalidation/runner_test.go` | pass |
| R5 | `TestRunnerExecutesConstraintsSequentiallyWithoutModel`, `TestPlanSnapshotPreservesSafeStructureWithoutLiteralValues`, `TestRunCommandNeverRendersExternalMapKeysInDiagnostics` | `internal/localvalidation/runner_test.go` | pass |
| R6 | `TestRunnerExecutesConstraintsSequentiallyWithoutModel`, `TestCommandExitCodesForFunctionalOutcomes` | `internal/localvalidation/runner_test.go`, `internal/localvalidation/command_test.go` | pass |
| R7 | `TestPublisherRejectsCollisionWithoutOverwrite`, `TestCommitNoReplaceAllowsOneConcurrentPublisher`, `TestPublisherValidatesEvidenceLinksBeforeArtifactWrites`, `TestPlanSnapshotPreservesSafeStructureWithoutLiteralValues` | `internal/localvalidation/runner_test.go`, `internal/localvalidation/filesystem_test.go` | pass |
| R8 | `TestPublisherFailureMatrixNeverExposesFinalBundle`, `TestPublisherFailureLeavesOnlyIncompleteDiagnosticDirectory`, `TestOperationalSetupFailuresStayOutsideFunctionalResults` | `internal/localvalidation/filesystem_test.go`, `internal/localvalidation/runner_test.go` | pass |
| R9 | `TestCommandExitCodesForFunctionalOutcomes`, `TestOrdinaryContextCancellationIsAFeatureProofError`, `TestMainPropagatesTheCommandExitCode` | `internal/localvalidation/command_test.go`, `internal/localvalidation/runner_test.go`, `cmd/featureproof/main_test.go` | pass |
| R10 | `TestSignalCancellationStopsWorkAndPublishesCancelledBundle`, `TestSignalCancellationDuringHTTPPropagatesAndPublishesConfirmedPrefix`, `TestCancellationDuringPolicyStopsBeforeToolExecution`, `TestCancellationDuringEvidencePersistenceStopsBeforeConfirmation`, `TestCancellationDuringAssertionEvaluationPreservesConfirmedEvidence`, `TestCancellationFinalizationFailureDoesNotPublishPartialRun` | `internal/localvalidation/runner_test.go` | pass |
| R11 | `TestRunCommandCompletesWithProductionBoundaries`, `TestIndependentRunnersDoNotShareMutableState`, `TestPublisherFailureMatrixNeverExposesFinalBundle`, suite race completa | `internal/localvalidation/command_test.go`, `internal/localvalidation/runner_test.go`, `internal/localvalidation/filesystem_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Parser exacto, wrapper de proceso y códigos previos al run. |
| T2 | done | Loader JSON v1 con números exactos, claves duplicadas/desconocidas, uniones y relaciones. |
| T3 | done | Target efectivo y composición explícita de HTTP, redacción, policy y validadores. |
| T4 | done | Runner secuencial, presupuesto compartido, progreso seguro y cancelación clasificada. |
| T5 | done | Schemas terminales JSON y Markdown sin payloads. |
| T6 | done | Staging restrictivo, escrituras sincronizadas, referencias verificadas y rename atómico. |
| T7 | done | Flujos herméticos, canarios, concurrencia, race, coverage y trazabilidad. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `CGO_ENABLED=1 go test -race ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./cmd/featureproof ./internal/localvalidation ./internal/planmanifest ./internal/planning ./internal/assertion ./internal/specification/yaml ./internal/tools/http` | pass |
| `GOOS=linux go test -c ./internal/localvalidation` y equivalente Darwin | pass |
| `go test -coverprofile=<temp>/featureproof-feature7.cover ./...` | pass |
| `go tool cover -func=<temp>/featureproof-feature7.cover` | pass |

En Windows, los comandos race usaron `C:\msys64\ucrt64\bin\gcc.exe` como
compilador C porque `CGO_ENABLED` estaba desactivado por defecto.

## Coverage

- Global: **91,0 %**.
- `cmd/featureproof`: **100,0 %**.
- `internal/localvalidation`: **92,3 %**.
- `internal/planmanifest`: **92,7 %**.
- `internal/planning`: **89,9 %**.
- `internal/assertion`: **90,6 %**.
- `internal/specification/yaml`: **87,5 %**.
- `internal/tools/http`: **91,1 %**.
- Los paquetes modificados conservan al menos 85 % y el global conserva el
  baseline de la feature anterior.

## Seguridad y hermeticidad

- Los tests usan `httptest`, directorios temporales, IDs/reloj controlados y no
  requieren Internet, credenciales ni modelo.
- Un canario presente en request, response y configuración sensible no aparece
  en stdout, stderr ni ningún archivo del bundle.
- Claves inválidas con canarios, controles y separadores tampoco aparecen en
  stderr: los paths públicos usan únicamente schema fijo e índices estables.
- La cancelación durante HTTP detiene el trabajo y publica `cancelled` con un
  contexto de finalización independiente y acotado.
- Los fallos de publicación conservan exclusivamente `<run-id>.incomplete`.

## Riesgos o limitaciones

- Windows no ofrece el mismo `fsync` de directorios que Unix; se sincronizan
  todos los archivos y el rename permanece en el mismo parent. La frontera
  sigue ejecutando y probando el paso de directory sync de forma inyectable.
- El commit no-replace está implementado para Windows, Linux y macOS. Otros
  sistemas fallan cerrados antes de publicar en vez de degradar a un rename
  que pudiera reemplazar un bundle concurrente.
- `plan.json` redacta conservadoramente todos los literales y valores esperados,
  aunque no estén marcados como secretos. Esto reduce detalle del snapshot para
  impedir fugas y conserva la estructura necesaria para auditar el flujo.
- Las señales de proceso se traducen mediante `signal.NotifyContext`; los tests
  inyectan la procedencia de señal sin enviar señales reales al proceso.

## Solicitud

Implementación preparada para review independiente. El implementer no modificó
el estado de la feature ni `progress/local-validation-cli/review.md`.
