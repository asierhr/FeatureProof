# Implementación — Generación agéntica de planes de validación

## Estado

`ready_for_review`

La feature 10 está implementada según la especificación aprobada. No se
modificó `feature_list.json` desde el rol implementer, no se ejecutaron
herramientas contra una aplicación, no se persistieron planes o evidencias y
no se realizaron llamadas a un modelo real.

## Resultado

- `internal/agentplanning` posee el protocolo `PlanningDecision` v1 separado
  del protocolo incremental del harness, el generador de una sola inferencia,
  normalización, deduplicación, procedencia, materialización, policy y
  observación terminal.
- `openapi.ReducedContext` conserva un índice defensivo de las operaciones que
  realmente entraron en el envelope. Una operación omitida por tamaño no puede
  recuperarse desde el catálogo completo.
- Cada check se materializa como un `planning.ValidationPlan`; método y path
  HTTP se derivan de la operación retenida y los parámetros de path se validan
  y escapan por segmento.
- `planning.Validator` admite un registry descriptor-only mediante
  `NewDescriptorValidator`; el constructor histórico con registry ejecutable
  conserva su firma y comportamiento.
- `model.Session.InferStructured` comparte reserva de llamadas, tokens,
  duración, deadlines, clasificación y observer con `Session.Decide`, sin
  reservar replanificaciones.
- El adapter OpenAI comparte construcción HTTP, Responses API y consumo SSE;
  los codecs de harness y planificación permanecen separados.
- La solicitud a modelo mantiene instrucciones privilegiadas separadas del
  envelope marcado como `untrusted_data`. Ninguna credencial, target o handle
  ejecutable forma parte de la frontera.

## Correcciones tras la primera review

- **REV-1:** `validation_work.go` recorre de forma determinista y acotada
  inputs, arrays, referencias y paths, path params y árboles completos de
  `AssertionDeclaration.Definition`. Rechaza campos de URL/URI, credenciales,
  comandos, tools y capabilities, además de URL absolutas con whitespace o
  userinfo y valores con forma de comando, antes de registry y policy.
- **REV-2:** un presupuesto compartido usa resta previa para evitar overflow,
  comprueba contexto en cada visita, limita profundidad a 128 y comprueba
  amplitud antes de reservar scratch traversal. Seguridad, referencias,
  assertions, evidence, cobertura y fingerprint consumen trabajo real.
- **REV-3:** normalización, cobertura y fingerprint observan cancelación entre
  unidades; model, assertion/planning, registry y policy vuelven a comprobar
  `ctx` tras retornar con éxito. El observer se ejecuta con contexto detached,
  deadline explícito y contrato cooperativo de cancelación.
- **REV-4:** `typedError` solo conserva `context.Canceled` y
  `context.DeadlineExceeded`; los errores externos de model, assertion,
  registry, policy y observer se traducen a categoría/stage/path seguros.
- **REV-5:** el fingerprint colorea el DAG por estructura, sustituye IDs de
  referencias/dependencias por colores canónicos, ordena pasos independientes,
  maps, assertions y evidence requirements y mantiene first-wins. Los cambios
  semánticos siguen produciendo fingerprints distintos.
- **REV-6:** la observación terminal es síncrona y queda unida al ciclo de vida
  de `Generate`. `Observer` debe detener todo trabajo propio al finalizar su
  contexto; `Generate` espera su retorno, no crea goroutines y no puede dejar
  confirmaciones terminales posteriores al resultado entregado al caller.

## Tasks completadas

| Task | Evidencia |
|---|---|
| T1 | Protocolo y modelos en `internal/agentplanning/model.go` y `protocol.go`; decode estricto y copias defensivas en tests. |
| T2 | Índice retenido en `internal/openapi`; fingerprints canónicos y deduplicación first-wins. |
| T3 | Validación semántica/procedencia y materialización en `generator.go`; adaptación descriptor-only en `internal/planning`. |
| T4 | `Generator.Generate` cancelable, una inferencia, límites explícitos, resultado missing tipado y observer fail-closed. |
| T5 | `CapabilityCatalog`, `PlanIntent`, `GenerationPolicy` y `StaticPolicy` default-deny sin implementaciones de tools. |
| T6 | `model.StructuredRequest`, `StructuredResult`, `StructuredProvider` y núcleo común `runAttempt`. |
| T7 | `openai.Adapter.InferStructured`, transporte/SSE compartido y separación instructions/input. |
| T8 | Integración hermética, casos adversariales, concurrencia, verificación canónica y este informe. |

## Trazabilidad R → test → resultado

| Requirement | Tests concretos | Resultado |
|---|---|---|
| R1 | `TestGenerateRejectsInvalidRequestBeforeModel`; `TestPlanningProtocolV1StrictDecodeAndSnapshot`; `TestPreflightRejectsContextCountsDuplicatesAndOversizedProjectionBeforeInference` | pass |
| R2 | `TestPlanGenerationEndToEndWithFakes`; `TestEvidenceAndPlanningFailuresShortCircuitPolicy` | pass |
| R3 | `TestEquivalentChecksDeduplicateCanonicallyAndRevalidateCoverage`; `TestCanonicalFingerprintIgnoresDecorativeGraphAndEvidenceOrder`; `TestScenarioMatrixSupportsExplicitNotApplicableAndRejectsCoverageContradictions` | pass |
| R4 | `TestAssertionSecurityTraversalPrecedesCollaborators`; `TestHTTPProposalMustReferenceRetainedOperation`; `TestRejectsInventedCapabilityBeforePlanningAndPolicy`; `TestPathParameterExpansionValidatesTypesEscapesSegmentsAndRejectsSubstitution`; `TestReducedContextProvenanceExcludesWholeOperationsOmittedBySize` | pass |
| R5 | `TestPolicyDenialReturnsNoPreparedSubset`; `TestEvidenceAndPlanningFailuresShortCircuitPolicy`; existing `internal/planning` validation suite | pass |
| R6 | `TestInsufficientContextReturnsTypedQuestionsAndEvidence`; `TestInvalidMissingContextForbiddenPlanAndLimitsFailClosed` | pass |
| R7 | `TestGenerationHasNoExecutableDependenciesOrSideEffects`; `TestPlanGenerationEndToEndWithFakes` | pass |
| R8 | `TestValidationWorkCountsDeepWideExactAndOverflow`; `TestCancellationDuringNormalizationPolicyAndObserver/observer-joined-deadline`; `TestSuccessfulCollaboratorsCannotMaskCancellation`; `TestTraversalBudgetErrorsPropagateFromEveryCanonicalShape`; `TestConcurrentGenerationSharesSessionBudgetWithoutRequestState` | pass |
| R9 | `TestAssertionSecurityTraversalPrecedesCollaborators`; `TestUntrustedInputsRemainDataInProviderRequest`; `TestStructuredPlanningCallSharesResponsesTransportAndSeparatesInstructions` | pass |
| R10 | `TestExternalAssertionCauseIsOpaqueAcrossEntireChain`; `TestCancellationDuringNormalizationPolicyAndObserver/observer-joined-deadline`; `TestGeneratorFailureStagesAreTypedSafeAndSingleAttempt`; `TestSafeHelperBoundariesCoverDefensiveCopiesAndContextPrecedence` | pass |
| R11 | `TestSessionModelUsesOneSharedBudgetedStructuredCall`; `TestStructuredCallSharesBudgetsTelemetryAndNeverReservesReplan`; `TestStructuredPlanningCallSharesResponsesTransportAndSeparatesInstructions`; suite existente de harness/model/OpenAI | pass |
| R12 | `TestAssertionSecurityTraversalPrecedesCollaborators`; `TestValidationWorkCountsDeepWideExactAndOverflow`; `TestCancellationDuringNormalizationPolicyAndObserver/observer-joined-deadline`; `TestCanonicalTraversalCoversEveryStructuredShape`; `TestValidateCheckRejectsEveryUnsafeGraphBoundary`; `TestCanonicalFingerprintIgnoresDecorativeGraphAndEvidenceOrder`; suite completa y race ×20 | pass |

## Verificación ejecutada

Entorno: Go 1.25.1 en Windows; race detector con CGO y
`C:\msys64\ucrt64\bin` en `PATH`, igual que las verificaciones anteriores del
repositorio.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `CGO_ENABLED=1 go test -race ./...` con UCRT64 | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/agentplanning ./internal/model ./internal/model/openai ./internal/openapi ./internal/planning` | pass |
| `go test -count=1 -coverprofile <temp> ./...` | pass; 91,1 % global |
| `go test -count=1 -cover ./internal/agentplanning` | pass; 91,0 % |
| Cobertura paquetes modificados | model 96,5 %; openai 93,9 %; openapi 91,8 %; planning 89,5 % |
| `go mod verify` | pass |
| `go build ./...` Windows | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` y test compile de agentplanning | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` y test compile de agentplanning | pass |
| `git diff --check` | pass; solo avisos de normalización LF/CRLF |

El baseline global de 91,1 % queda preservado.
Todos los tests ordinarios son herméticos: no requieren Internet, credenciales,
OAuth, un modelo real ni una aplicación desplegada.

## Riesgos y límites restantes

- `PreparedPlan` significa que estructura, procedencia, planning, catálogo y
  policy de generación pasaron. No implica aprobación humana y no autoriza a
  omitir registry/policy en ejecución.
- La exactitud semántica del plan se medirá en la feature 11; esta entrega
  garantiza contratos y fronteras deterministas.
- La aprobación/persistencia del plan y la replanificación siguen fuera de
  alcance y corresponden a las features 12 y 13.
