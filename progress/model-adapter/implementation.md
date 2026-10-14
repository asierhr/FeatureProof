# Implementación de model-adapter

## Resultado

Se implementó un runtime por sesión independiente del proveedor, con selección
explícita de provider, model y auth profile; no existe modelo por defecto. La
sesión implementa `harness.Model`, aplica límites de llamadas, tokens
confirmados, duración y replanificaciones, y entrega al harness únicamente una
`ModelDecision` validada.

El primer adapter real usa exclusivamente OpenAI Responses API. Las
credenciales Sign in with ChatGPT permanecen en composición: el perfil OAuth
las persiste con protección owner-only, las rota de forma serializada y el
transporte autenticado añade el bearer después de que el adapter haya creado la
petición. Ni el runtime neutral ni el harness reciben tokens.

La sustitución del antiguo criterio de coste por presupuestos no monetarios fue
autorizada explícitamente: no se introdujeron precios, monedas, tablas de
pricing ni estimaciones monetarias.

## Requirements y tests

| Requirement | Tests concretos | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestSessionRequiresExplicitProviderModelProfileTimeoutAndBudgets`, `TestSessionsSelectOnlyConfiguredProviderAndOwnState` | `internal/model/runtime_test.go` | pass |
| R2 | `TestOAuthRegistrationUsesHostPKCEStateNonceScopesAndIssuedClient`, `TestOAuthReauthorizationReusesIssuedClientAndRetainedIdentityHint`, `TestOAuthRefreshIsSerializedAndRotatesAtomically`, `TestProfilesNeverMixCredentialsAndSignOutRevokes`, `TestJWKSValidatorVerifiesSignatureIssuerAudienceExpiryAndNonce` | `internal/model/openai/oauth/*_test.go` | pass |
| R3 | `TestOpenAIRequestUsesOnlyResponsesOAuthStoreFalseStreamTrueAndSelectedModel`, `TestOpenAIRequestOmitsPreviewUnsupportedFieldsToolsAndSecrets`, `TestProviderUsesTheStricterSessionByteBounds` | `internal/model/openai/adapter_test.go` | pass |
| R4 | `TestOpenAIStreamReturnsOnlyCompletedExactValidatedDecisionWithUsage`, `TestOpenAIStreamRejectsMalformedTrailingOversizedFailedIncompleteAndPrematureResponses`, `TestDecisionJSONRejectsDuplicateKeysInvalidClaimsAndEvidenceIDs` | `internal/model/openai/adapter_test.go` | pass |
| R5 | `TestModelErrorsClassifyConfigContextHTTPProviderAndResponseWithoutTextMatching`, `TestRuntimeNormalizesContextAndOpaqueProviderFailures`, `TestOpenAIHeaderBoundaryPreservesAuthenticationAndExactContextCategories`, `TestOAuthBoundariesPreserveExactSafeCategories`, `TestOAuthHTTPAndKeyBoundariesPreserveExactCategories`, `TestProductionCompositionFailsSafelyBeforeExternalWork` | runtime, adapter, OAuth y smoke tests | pass |
| R6 | `TestSessionUsesShortestDeadlineAndPropagatesCancellationAcrossAllBoundaries`, `TestOpenAIRequestCancellationBeforeHeadersAndDuringBody`, `TestOAuthSystemBoundariesAreCancelableAndBounded`, `TestOAuthPreflightCancellationAndSharedOwnerBounds`, `TestLocalStoreCoordinationWaitIsCancelableWithoutHoldingIOLock` | paquetes `internal/model/...` | pass |
| R7 | `TestEveryAttemptEmitsOneSafeTerminalRecordWithConfirmedUsage`, `TestObserverFailureIsFailClosedAndAllExcludedCanariesRemainAbsent`, `TestSmokeSuccessPrintsOnlySafeUsageMetadata` | runtime y smoke tests | pass |
| R8 | `TestSessionReservesCallsAndReplansAtomicallyAndBlocksExhaustedDimensions`, `TestTokenUsageMayCrossOnceThenBlocksNextCall`, `TestMissingUsageClosesSession`, `TestTokenAccountingRejectsOverflowAndNeverReopensBudget`, `TestTokenAccountingAllowsExactRepresentableLimitThenBlocks` | `internal/model/runtime_test.go` | pass |
| R9 | `TestAdapterNeverRetriesAndExplicitRetryConsumesAnotherCall`, `TestConcurrentRefreshUsesOneRotatingTokenExchange`, `TestRefreshSurvivesInitiatorCancellationForActiveWaiter`, `TestHTTPGatewayUsesOnlyOfficialEndpointsAndDocumentedForms` | runtime y OAuth tests | pass |
| R10 | `TestAdapterHasNoExecutableCapabilitiesAndNeverMutatesRunEvidenceOrVerdict`, `TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies` | `internal/model/runtime_test.go`, `internal/model/integration_test.go` | pass |
| R11 | `TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies`, regresiones REV-1 a REV-4 y suite canónica completa | integración, runtime, adapter, OAuth y smoke tests | pass |
| R12 | `TestSmokeRequiresOptInBeforeProfileOrNetwork`, `TestSmokeRejectsIncompleteExplicitSelection`, `TestSmokeReportsAuthAndProviderFailureWithoutCanaries`, `TestSmokeSuccessPrintsOnlySafeUsageMetadata`, `TestSmokeProvisionUsesExplicitProfileComposition`, `TestProductionCompositionFailsSafelyBeforeExternalWork` | `cmd/featureproof-model-smoke/main_test.go` | pass |

## Correcciones tras la primera review

| Hallazgo | Corrección y regresión | Resultado |
|---|---|---|
| REV-1 | Los límites del runtime, Responses, SSE, OAuth, discovery y JWKS priorizan `context.Canceled`/`DeadlineExceeded`, conservan `model.Error` ya tipado y solo normalizan errores opacos. La resolución de perfil inexistente del smoke produce exactamente `authentication`; la lectura SSE interrumpida produce exactamente `cancelled`. Regresiones: `TestOpenAIHeaderBoundaryPreservesAuthenticationAndExactContextCategories`, `TestOpenAIRequestCancellationBeforeHeadersAndDuringBody`, `TestOAuthBoundariesPreserveExactSafeCategories`, `TestOAuthHTTPAndKeyBoundariesPreserveExactCategories`, `TestProductionCompositionFailsSafelyBeforeExternalWork`. | pass |
| REV-2 | `Usage` valida overflow de input+output y la sesión valida overflow al acumular el snapshot. Un uso imposible satura a `math.MaxInt64`, cierra la sesión y devuelve `invalid_response`; el máximo exacto se contabiliza y bloquea la llamada siguiente. Regresiones: `TestTokenAccountingRejectsOverflowAndNeverReopensBudget`, `TestTokenAccountingAllowsExactRepresentableLimitThenBlocks`. | pass |
| REV-3 | Cada refresh coalesced tiene contexto, cancelación, resultado y waiters propios, con un timeout finito independiente del caller que lo inició. Cada caller espera con su contexto y el trabajo común se cancela solo al salir el último waiter. `TestRefreshSurvivesInitiatorCancellationForActiveWaiter` prueba cancelación del iniciador, waiter activo, un único exchange y token rotado. | pass |
| REV-4 | `LocalStore` usa turnos de transacción: el mutex protege solo la cola y nunca permanece tomado durante `ReadFile`, escritura, `Sync` o rename. Los callers esperan de forma cancelable y los turnos abandonados se saltan sin perder la serialización del commit atómico. Regresión: `TestLocalStoreCoordinationWaitIsCancelableWithoutHoldingIOLock`. | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Contratos neutrales, configuración y taxonomía tipada en `internal/model`. |
| T2 | done | Sesión aislada, reservas atómicas, deadlines, usage y observer seguro. |
| T3 | done | Puertos OAuth, records privados y store local atómico/protegido. |
| T4 | done | Registro/reauthorización, PKCE, JWKS, refresh coalesced y revocación. |
| T5 | done | Petición Responses oficial y bearer confinado al transporte. |
| T6 | done | Decoder SSE acotado, terminal exacto, JSON estricto y clasificación. |
| T7 | done | Ejecutable opt-in separado y documentación manual. |
| T8 | done | Integración hermética, verificación, plataformas y trazabilidad cerradas. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` con CGO y MinGW GCC | pass |
| `go test -race -count=20 ./internal/model/... ./cmd/featureproof-model-smoke` | pass |
| `go test ./... -coverprofile <temp>/featureproof-model-adapter-coverage.out` | pass |
| `go tool cover -func=<temp>/featureproof-model-adapter-coverage.out` | pass |
| `go build ./...` | pass |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...` | pass |
| `go mod verify` | pass |

## Coverage

- Global: **91,1 %**, baseline de la feature anterior preservado.
- `internal/model`: **97,2 %**.
- `internal/model/openai`: **95,0 %**.
- `internal/model/openai/oauth`: **88,5 %**.
- `cmd/featureproof-model-smoke`: **87,3 %**.
- Las reservas concurrentes, refresh coalesced, cancelación, streams
  fragmentados, límites, taxonomía y ramas fail-closed tienen pruebas
  concretas. Todos los paquetes nuevos superan el mínimo del 85 %.

## Hermeticidad y seguridad

- Los tests ordinarios usan únicamente HTTP, OAuth, browser, listener, clock,
  randomness, validator y stores controlados; no requieren cuenta ni secreto.
- La integración completa usa un callback fake y un transporte local en
  memoria. El único socket utilizado por tests es loopback y pertenece al
  propio test.
- No se añadió SDK de proveedor ni dependencia nueva en `go.mod`.
- El código productivo contiene solo los endpoints oficiales de autorización,
  token, discovery/JWKS y Responses. No contiene `backend-api`, API keys,
  pricing, moneda, modelo por defecto ni endpoint configurable.
- Los errores y records observables se construyen por allowlist y los tests de
  canarios confirman que bodies, prompts, outputs, URLs de autorización y
  tokens no aparecen.

## Smoke real

El ejecutable y su invocación están documentados en
`cmd/featureproof-model-smoke/README.md`. La prueba real **no se ejecutó**, tal
como exige la task: requiere opt-in humano, una cuenta/app OSS elegible, un
perfil elegido y un modelo disponible para esa cuenta. Ninguna credencial fue
leída o utilizada durante esta implementación.

## Riesgos o limitaciones

- Sign in with ChatGPT para uso de plan continúa siendo una capability preview
  y depende de la elegibilidad y modelos de la cuenta; el runtime no promete
  disponibilidad.
- El smoke abre el navegador del SO para provisionar y usa un callback
  `127.0.0.1` explícito. No está integrado en `featureproof validate`.
- Esta feature reserva replanificaciones, pero su ejecución pertenece a una
  iteración posterior.

No quedan limitaciones adicionales derivadas de REV-1 a REV-4. La prueba real
no se ejecutó y ninguna credencial fue resuelta durante las correcciones.
