# Implementación de http-tool

## Resultado

Se añadió `internal/tools/http` como adapter registrable `http.request`. El
constructor fija el target, permisos, límites y dependencias fuera del input;
cada ejecución valida antes de red, resuelve una sola vez, conecta únicamente
a IPs fijadas, revalida redirects y devuelve una observación estructurada y
acotada. La herramienta no conoce run, journal, evidence store ni assertions.

## Requirements y tests

| Requirement | Tests | Archivo | Resultado |
|---|---|---|---|
| R1 | `TestValidateInputAcceptsStructuredRequest`; `TestValidateInputRejectsMalformedFieldsBeforeNetwork`; `TestJSONValidationCoversEverySupportedShape` | `internal/tools/http/tool_test.go` | pass |
| R2 | `TestRequestURLStaysBelowTargetBasePath`; `TestInputCannotProvideAbsoluteOrEscapingURL`; `TestTargetAndTokenBoundaries` | `internal/tools/http/tool_test.go` | pass |
| R3 | `TestExecuteDialsOnlyPinnedAuthorizedDestination`; `TestExplicitLocalTargetIsAllowed`; `TestExecuteIgnoresEnvironmentProxy`; `TestTransportRejectsUnauthorizedAuthorityBeforeDial`; `TestResolveProducesOneDeduplicatedOperationSnapshot` | `internal/tools/http/tool_test.go`; `internal/tools/http/execute_test.go`; `internal/tools/http/boundaries_test.go` | pass |
| R4 | `TestRedirectIsRevalidatedAtEveryHop`; `TestRedirectCannotEscapeAuthorizedTarget`; `TestRedirectLimitAndMethodPolicy`; `TestRedirectResultingMethodMustRemainAllowed`; `TestRedirectPolicyValidatesHeadersAndDestination` | `internal/tools/http/execute_test.go`; `internal/tools/http/boundaries_test.go` | pass |
| R5 | `TestExecuteDistinguishesToolTimeoutFromCallerCancellation`; `TestExecuteClosesBlockedOperation` | `internal/tools/http/execute_test.go` | pass |
| R6 | `TestRequestLimitsFailBeforeNetwork`; `TestAutomaticRequestHeadersRespectLimitBeforeNetwork`; `TestSerializedRequestHeadersIncludeControlledDefaults`; `TestResponseHeadersAndBodyAreBounded`; `TestHeaderLimitConnRejectsBeforeParser`; `TestHeaderLimitConnHandlesSplitBoundaryAndStopsCountingBody` | `internal/tools/http/tool_test.go`; `internal/tools/http/execute_test.go`; `internal/tools/http/boundaries_test.go` | pass |
| R7 | `TestExecuteReturnsStructuredHTTPResponse`; `TestHTTPErrorStatusRemainsObservable`; `TestOperationalFailureIsStructured`; `TestErrorClassificationAndClockAnomalies` | `internal/tools/http/execute_test.go`; `internal/tools/http/boundaries_test.go` | pass |
| R8 | `TestSensitiveRequestHeadersAreRejected`; `TestOutputContainsOnlyAllowedNonSensitiveHeaders`; `TestSecretsNeverAppearInResultsOrErrors` | `internal/tools/http/tool_test.go`; `internal/tools/http/execute_test.go` | pass |
| R9 | `TestHTTPToolReturnsWithoutMutatingExecutorState`; `TestConfigurationAndResultsAreIsolatedAcrossExecutions` | `internal/planning/http_tool_integration_test.go`; `internal/tools/http/tool_test.go` | pass |
| R10 | `TestHTTPSpecDeclaresConservativeEffects`; `TestHTTPToolDoesNotRetry`; `TestHTTPToolReturnsWithoutMutatingExecutorState` | `internal/tools/http/tool_test.go`; `internal/tools/http/execute_test.go`; `internal/planning/http_tool_integration_test.go` | pass |
| R11 | `TestNewRejectsIncompleteConfiguration`; `TestHTTPToolSuiteUsesOnlyControlledLocalServers` | `internal/tools/http/tool_test.go` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Contratos, configuración, errores tipados y spec conservador. |
| T2 | done | Input v1 exacto, JSON finito, headers y límites pre-red. |
| T3 | done | Scope por autoridad/segmentos y URL derivada del target. |
| T4 | done | DNS por operación, dial fijado, redirects, timeout y cancelación. |
| T5 | done | Headers/body acotados, decoder estable y errores estructurados. |
| T6 | done | Registry y plan executor integrados sin transferir ownership. |
| T7 | done | Suite hermética, race, cobertura y trazabilidad completadas. |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin archivos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `CGO_ENABLED=1 go test -race ./...` | pass con GCC local de MSYS2 |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/tools/http ./internal/planning` | pass |
| `go test -coverprofile=<temp>/featureproof-http-global.out ./...` | pass |
| `go tool cover -func=<temp>/featureproof-http-global.out` | pass |

## Coverage

- Global: **90,3 %**, por encima del baseline previo de 90,2 %.
- `internal/tools/http`: **90,4 %**.
- `internal/planning`: **90,3 %**.
- El perfil se generó fuera del repositorio.

## Refactor mínimo

Se añadió únicamente `internal/planning/http_tool_integration_test.go` al
paquete existente para demostrar con sus fakes reales que el executor conserva
ownership de journal, evidencia y evaluación. No se modificó código productivo
de planning.

## Correcciones de review

- **REV-1:** cada `Execute` selecciona la primera IP del snapshot ordenado y
  realiza una sola llamada al dialer. `TestHTTPToolDoesNotRetry` usa dos IPs,
  fuerza el fallo de la primera y demuestra que la segunda no se intenta.
- **REV-2:** el preflight serializa una copia del request con `Request.Write` y
  cuenta el bloque completo que realmente emitirá HTTP/1.1, incluidos `Host`,
  `User-Agent`, `Content-Length`, `Content-Type` y el terminador. El transporte
  desactiva compresión automática para impedir `Accept-Encoding` no medido y
  mantiene keep-alive para no inyectar `Connection: close`. La misma medición
  se aplica a redirects antes de su dial. Los tests prueban los defaults y que
  un límite pequeño produce cero llamadas a resolver o dialer.

## Riesgos o limitaciones

- El adapter usa el trust store TLS del proceso y conserva SNI con el hostname
  autorizado. Certificados de cliente y configuración de CA específica quedan
  fuera del alcance aprobado.
- La redacción configurable de query y body corresponde a
  `evidence-redaction`; esta feature elimina los cuatro headers sensibles y
  mantiene errores sin dumps ni URLs completas.
