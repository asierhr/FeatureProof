# Review de http-tool

## Veredicto

**Aprobado.** La segunda revisión confirma que la implementación satisface
R1–R11, respeta el diseño aprobado y corrige los dos hallazgos de la revisión
anterior. No quedan hallazgos bloqueantes.

## Precondiciones

- Feature única `in_progress`: `http-tool` (id 5).
- Aprobación humana registrada: `user`,
  `2026-10-11T19:14:19+02:00`.
- Tasks: 7 completadas, 0 abiertas.
- Informe de implementación y trazabilidad presentes.
- Revisión limitada a la feature 5 y a su integración de test con planning.

## Cierre de hallazgos anteriores

### REV-1 — Cerrado

`transport` selecciona únicamente la primera IP del snapshot ordenado y hace
una sola llamada a `DialContext`. `TestHTTPToolDoesNotRetry` suministra dos IPs,
fuerza el fallo de la primera y comprueba que la segunda no se intenta. El
adapter conserva un solo lookup, dial y request por intento; los redirects
autorizados continúan siendo hops explícitos del intercambio y no retries.

### REV-2 — Cerrado

El preflight construye el request final y usa `Request.Write` con
`requestHeaderCounter` para medir el bloque HTTP/1.1 completo antes de DNS.
Esto incluye `Host`, `User-Agent`, `Content-Length`, `Content-Type` y el
terminador de headers. `TestAutomaticRequestHeadersRespectLimitBeforeNetwork`
demuestra que un límite insuficiente produce `request_limit` sin llamadas al
resolver ni al dialer, y
`TestSerializedRequestHeadersIncludeControlledDefaults` contrasta el conteo
exacto con los defaults efectivos.

El transporte usa `DisableCompression=true`, por lo que no inyecta
`Accept-Encoding`, y mantiene keep-alive para no añadir `Connection: close`.
La política de redirect vuelve a medir el request resultante después de
validar destino, método y headers, dentro de `CheckRedirect` y antes del dial
del siguiente hop. Un exceso se clasifica como `request_limit`.

## Trazabilidad revisada

| Requirement | Evidencia concreta | Resultado |
|---|---|---|
| R1 | `TestValidateInputAcceptsStructuredRequest`, `TestValidateInputRejectsMalformedFieldsBeforeNetwork`, `TestJSONValidationCoversEverySupportedShape` | pass |
| R2 | `TestRequestURLStaysBelowTargetBasePath`, `TestInputCannotProvideAbsoluteOrEscapingURL`, `TestTargetAndTokenBoundaries` | pass |
| R3 | `TestExecuteDialsOnlyPinnedAuthorizedDestination`, `TestExplicitLocalTargetIsAllowed`, `TestExecuteIgnoresEnvironmentProxy`, `TestTransportRejectsUnauthorizedAuthorityBeforeDial`, `TestResolveProducesOneDeduplicatedOperationSnapshot` | pass |
| R4 | `TestRedirectIsRevalidatedAtEveryHop`, `TestRedirectCannotEscapeAuthorizedTarget`, `TestRedirectLimitAndMethodPolicy`, `TestRedirectResultingMethodMustRemainAllowed`, `TestRedirectPolicyValidatesHeadersAndDestination` | pass |
| R5 | `TestExecuteDistinguishesToolTimeoutFromCallerCancellation`, `TestExecuteClosesBlockedOperation` | pass |
| R6 | `TestRequestLimitsFailBeforeNetwork`, `TestAutomaticRequestHeadersRespectLimitBeforeNetwork`, `TestSerializedRequestHeadersIncludeControlledDefaults`, `TestResponseHeadersAndBodyAreBounded`, tests de `headerLimitConn` | pass |
| R7 | `TestExecuteReturnsStructuredHTTPResponse`, `TestHTTPErrorStatusRemainsObservable`, `TestOperationalFailureIsStructured`, `TestErrorClassificationAndClockAnomalies` | pass |
| R8 | `TestSensitiveRequestHeadersAreRejected`, `TestOutputContainsOnlyAllowedNonSensitiveHeaders`, `TestSecretsNeverAppearInResultsOrErrors` | pass |
| R9 | `TestHTTPToolReturnsWithoutMutatingExecutorState`, `TestConfigurationAndResultsAreIsolatedAcrossExecutions` | pass |
| R10 | `TestHTTPSpecDeclaresConservativeEffects`, `TestHTTPToolDoesNotRetry`, `TestHTTPToolReturnsWithoutMutatingExecutorState` | pass |
| R11 | `TestNewRejectsIncompleteConfiguration`, `TestHTTPToolSuiteUsesOnlyControlledLocalServers` | pass |

Los tests fallarían al retirar los controles que trazan: observan contadores de
resolver/dial, hits de servidores, destinos exactos, errores tipados, outputs,
ownership de store/journal y límites antes y después del transporte.

## Revisión arquitectónica

- El paquete `internal/tools/http` actúa como adapter y solo depende de
  contratos internos existentes; no introduce ciclos.
- Target, permisos, límites y dependencias quedan fijados en construcción. El
  input no puede ampliarlos.
- Cada ejecución posee request, timeout, transporte, snapshot DNS, buffers y
  resultado. La herramienta no retiene contexto ni estado entre ejecuciones.
- Resolver, dialer, reloj y timeout son sustituibles. No hay I/O bajo locks ni
  goroutines de aplicación sin propietario.
- La cancelación se propaga a resolución, dial, intercambio y lectura; response
  bodies, conexiones idle y timers se cierran mediante `defer`.
- Proxy ambiental y compresión automática están desactivados. Todos los dials
  usan una IP fijada y el puerto autorizado.
- Redirects revalidan esquema, autoridad, puerto, path por segmentos, método,
  headers, límite y tamaño serializado antes del siguiente hop.
- El adapter no recibe store, journal ni run. El executor conserva saneado,
  persistencia, assertions, transición y decisión de retry.
- Los cuatro headers sensibles se rechazan u omiten sin exponer sus valores.
  Los outputs y errores visibles usan categorías acotadas.
- Las entidades y funciones nuevas o modificadas contienen comentarios
  funcionales acordes con la convención del proyecto.

## Verificaciones ejecutadas

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin archivos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=1 ./...` con GCC de MSYS2 | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/tools/http ./internal/planning` | pass |
| `go test -count=1 -coverprofile=<temp>/featureproof-http-review2.out ./...` | pass |
| `go tool cover -func=<temp>/featureproof-http-review2.out` | 90,3 % global; 90,4 % en `internal/tools/http`; 90,3 % en `internal/planning` |

La cobertura del código afectado supera el mínimo de 85 % y el baseline global
previo de 90,2 %. Los tests usan servidores locales y dobles controlados, no
dependen de `time.Sleep`, Internet, producción, variables de entorno para
configuración funcional ni un modelo real.

## Hallazgos abiertos

Ninguno.
