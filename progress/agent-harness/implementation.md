# Implementación de agent-harness

## Estado

Implementación y verificación canónica completadas para T1–T7 con Go 1.25.1.
Todas las comprobaciones obligatorias, incluido el detector de carreras,
terminan correctamente.

## Requirements y tests

| Requirement | Test concreto | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestHarnessReturnsPersistedToolObservationToModel` | `internal/harness/harness_integration_test.go` | pass |
| R2 | `TestHarnessRejectsMalformedDecisionBeforePolicyAndExecution`; `TestHarnessRejectsInvalidToolInputBeforePolicyAndExecution` | `internal/harness/harness_integration_test.go` | pass |
| R3 | `TestHarnessExecutesOnlyRegisteredAllowedTool`; `TestHarnessAuditsUnknownAndDeniedTools` | `internal/harness/harness_integration_test.go` | pass |
| R4 | `TestHarnessReservesToolBudgetBeforeExecution`; `TestHarnessStopsWhenToolBudgetIsExhausted`; `TestRetryAndToolBudgetsReserveAtomically`; `TestHarnessDoesNotPartiallyReserveWhenToolBudgetIsExhaustedBeforeRetry` | `internal/harness/state_test.go`; `internal/harness/harness_integration_test.go` | pass |
| R5 | `TestHarnessPersistsRelatedEvidenceBeforeUse`; `TestHarnessFailsWhenEvidenceCannotBePersisted`; `TestFileEvidenceStoreDoesNotRewriteConfirmedEvidence` | `internal/harness/harness_integration_test.go`; `internal/evidence/store_test.go` | pass |
| R6 | `TestVerdictIsDerivedFromPersistedEvidence`; `TestVerdictCannotPassWithoutEvidence`; `TestVerdictIgnoresModelClaim`; `TestVerdictIsDeterministicForSameEvidence` | `internal/harness/harness_integration_test.go` | pass |
| R7 | `TestHarnessStopsAtConfiguredStepLimit` | `internal/harness/harness_integration_test.go` | pass |
| R8 | `TestJournalAuditsDecisionOrder`; `TestHarnessEmitsExactlyOneTerminalEvent`; `TestJournalSequenceIsMonotonic`; `TestHarnessStopsWhenJournalFailsBeforeOrDuringExecution`; `TestHarnessConfirmsTerminalAfterTransientJournalFailure`; `TestHarnessPropagatesMandatoryJournalFailures` | `internal/harness/harness_integration_test.go`; `internal/harness/state_test.go`; `internal/harness/journal_test.go` | pass |
| R9 | `TestHarnessPropagatesCancellationAndPreservesConfirmedRecords`; `TestHarnessCancelsDuringEvidencePersistenceAndPreservesConfirmedRecords`; `TestHarnessCancelsDuringEvidenceRecoveryAndPreservesConfirmedRecords` | `internal/harness/harness_integration_test.go` | pass |
| R10 | `TestHarnessRetriesOnlyTransientIdempotentOperationsWithinBudget`; `TestHarnessStopsWhenRetryBudgetIsExhausted`; `TestHarnessDoesNotPartiallyReserveWhenToolBudgetIsExhaustedBeforeRetry`; `TestHarnessDoesNotRetryEffectfulToolWithoutIdempotency`; `TestHarnessRetriesTransientModelErrorWithinBudget`; `TestHarnessPersistsEveryExecutedAttempt` | `internal/harness/harness_integration_test.go` | pass |
| R11 | `TestHarnessSuiteUsesOnlyControlledLocalDependencies`; tests de fake model, policy, registry, fake tool y filesystem temporal | `internal/harness/harness_integration_test.go`; `internal/model/fake_test.go`; `internal/policy/policy_test.go`; `internal/tools/tools_test.go`; `internal/evidence/store_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Estado tipado, transiciones y presupuestos con tests. |
| T2 | done | Protocolo versionado, validación, fake model y fake tool cancelables. |
| T3 | done | Journal secuencial, saneado y evidencia local inmutable con hash. |
| T4 | done | Registry seguro, policy default-deny, orden de checks y retries acotados. |
| T5 | done | Assertion mínima y veredicto determinista respaldado por evidencia. |
| T6 | done | Ciclo secuencial integrado, cancelación y terminación única. |
| T7 | done | Formato, vet, compilación, suite, race detector y coverage verificados con Go 1.25.1. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; sin archivos Go sin formato tras retirar el toolchain temporal |
| `go vet ./...` | pass con Go 1.25.1 |
| `go test -run '^$' ./...` | pass con Go 1.25.1 |
| `go test ./...` | pass en los cinco paquetes con Go 1.25.1 |
| `CGO_ENABLED=1 CC=gcc go test -race ./...` | pass en los cinco paquetes con Go 1.25.1 y GCC 13.2.0; ejecutado fuera del sandbox para permitir que cgo invoque GCC |
| `go test '-coverprofile=coverage.out' ./...` | pass con Go 1.25.1 |
| `go tool cover '-func=coverage.out'` | pass; total 90.1 % tras las correcciones de review |

La reverificación posterior a la primera review usó
`GOPATH=C:\Users\asier\AppData\Local\Temp\featureproof-go-toolchain-1.25.1` y
`GOTOOLCHAIN=go1.25.1`. El race detector se ejecutó con CGO, GCC 13.2.0 y el
directorio de GCC al inicio de `PATH`. Los resultados finales son: formato,
vet, compilación, suite funcional, race detector y coverage en `pass`.

## Coverage

- Global: 90.1 % de statements.
- `internal/evidence`: 87.3 %.
- `internal/harness`: 88.5 %.
- `internal/model`: 100.0 %.
- `internal/policy`: 100.0 %.
- `internal/tools`: 97.4 %.
- Ramas relevantes cubiertas: validación estructural e input, herramienta
  desconocida, policy denegada, presupuesto agotado, persistencia fallida,
  evidencia alterada, veredicto sin evidencia, cancelación, retries
  transitorios, operaciones con efectos sin idempotencia, cancelación durante
  persistencia/recuperación y fallos del journal antes, durante y al confirmar
  el terminal.

## Riesgos o limitaciones

- En Windows, la comprobación `-race` requiere CGO y un compilador GCC
  accesible. La verificación reproducible usó Go 1.25.1, CGO habilitado y GCC
  13.2.0; el sandbox debe permitir a cgo lanzar el compilador.
- El alcance aprobado usa únicamente fakes y filesystem temporal; no se han
  añadido modelo real, red, CLI ni integraciones externas.

## Correcciones de review

- REV-1: `Evidence.Save` y `Evidence.Get` propagan cancelación y finalizan el
  run una sola vez como `cancelled`; los tests bloqueables comprueban además
  que las evidencias confirmadas siguen recuperables.
- REV-2: todos los eventos obligatorios comprueban la confirmación del journal.
  Un fallo detiene trabajo posterior, se propaga con contexto y el terminal se
  considera escrito únicamente después de un append satisfactorio. Los tests
  cubren fallos antes de decidir, durante la ejecución y al escribir el
  terminal.
- REV-3: la reserva de retry y llamada de herramienta es atómica. Si cualquiera
  de los dos presupuestos está agotado, ningún contador cambia, se registra un
  rechazo tipado (`retry_budget_exhausted` o `budget_exhausted`), no se solicita
  otra decisión y el run termina `inconclusive`, sin posibilidad de producir
  `passed` con trabajo posterior.
