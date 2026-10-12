# Herramienta HTTP controlada — Tasks

Las tareas se ejecutan en orden. Cada cambio de comportamiento se desarrolla
con su test trazable y sin modificar el alcance aprobado.

- [x] T1. Definir contratos, configuración, errores y `ToolSpec` seguro. [R1, R2, R3, R6, R7, R8, R10, R11]
  - Crear los tipos internos para configuración inmutable, request validado,
    target scope, resultado y categorías de error.
  - Escribir `TestNewRejectsIncompleteConfiguration` para target, dependencias,
    allowlists, timeout, tamaños, redirects y duplicados normalizados.
  - Escribir `TestHTTPSpecDeclaresConservativeEffects` y verificar nombre
    `http.request`, `Effects=true`, `Idempotent=false` y validator obligatorio.
  - Añadir comentarios funcionales a toda entidad y función nueva o modificada.

- [x] T2. Implementar el validador y encoder del input v1. [R1, R6, R8]
  - Escribir `TestValidateInputAcceptsStructuredRequest` para método, path,
    query, headers, body, ausencia de body y `null` explícito.
  - Escribir `TestValidateInputRejectsMalformedFieldsBeforeNetwork` con field
    paths, claves desconocidas, tipos, JSON no finito, tokens y CR/LF.
  - Escribir `TestRequestLimitsFailBeforeNetwork` para URL, request headers y
    body codificado.
  - Escribir `TestSensitiveRequestHeadersAreRejected` con variantes de
    capitalización y headers hop-by-hop.

- [x] T3. Implementar scope del target y construcción de URL sin escapes. [R2, R3, R4]
  - Escribir `TestRequestURLStaysBelowTargetBasePath` para paths con y sin slash
    inicial y query estructurada.
  - Escribir `TestInputCannotProvideAbsoluteOrEscapingURL` para esquema,
    autoridad, query/fragment embebidos, backslash, dot-segments y separadores
    codificados.
  - Validar por segmentos esquema, hostname, puerto efectivo y path base para
    requests iniciales y redirects.

- [x] T4. Implementar resolución fijada, transporte, redirects, timeout y cancelación. [R3, R4, R5, R6, R10]
  - Definir interfaces consumidoras mínimas para resolver, clock y dial
    cancelable; desactivar proxy ambiental y evitar clientes globales.
  - Escribir `TestExecuteDialsOnlyPinnedAuthorizedDestination`,
    `TestExplicitLocalTargetIsAllowed` y `TestExecuteIgnoresEnvironmentProxy`.
  - Escribir `TestRedirectIsRevalidatedAtEveryHop`,
    `TestRedirectCannotEscapeAuthorizedTarget` y
    `TestRedirectLimitAndMethodPolicy` con dos servidores locales.
  - Escribir `TestExecuteDistinguishesToolTimeoutFromCallerCancellation` y
    `TestExecuteClosesBlockedOperation` mediante canales y reloj controlado.
  - Escribir `TestHTTPToolDoesNotRetry` y verificar un único intento por
    `Execute`, incluso ante fallo transitorio.

- [x] T5. Implementar captura acotada y traducción del resultado. [R6, R7, R8]
  - Escribir `TestExecuteReturnsStructuredHTTPResponse` para JSON, texto,
    binario, vacío, duración y normalización estable de headers.
  - Escribir `TestHTTPErrorStatusRemainsObservable` para 4xx/5xx sin error
    operativo.
  - Escribir `TestOperationalFailureIsStructured` para categorías, causa y
    clasificación transient/permanent.
  - Escribir `TestResponseHeadersAndBodyAreBounded` para límite del transporte,
    prefijo de body y truncación.
  - Escribir `TestOutputContainsOnlyAllowedNonSensitiveHeaders` y
    `TestSecretsNeverAppearInResultsOrErrors` usando secretos canario.

- [x] T6. Integrar la herramienta con registry y plan executor sin transferir ownership. [R9, R10]
  - Registrar el spec mediante el `tools.Registry` actual y ejecutar un paso de
    plan permitido contra `httptest.Server`.
  - Escribir `TestHTTPToolReturnsWithoutMutatingExecutorState` y comprobar que
    solo el executor crea journal/evidence después del retorno de la tool.
  - Verificar que el output persistido permite assertions deterministas sobre
    status y body JSON, y que un fallo operativo queda inconcluso según el
    contrato existente.

- [x] T7. Completar verificación hermética y trazabilidad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Escribir `TestHTTPToolSuiteUsesOnlyControlledLocalServers` o una comprobación
    equivalente que falle ante resolución o dial no previstos.
  - Confirmar casos de éxito, status de error, timeout, cancelación, redirect
    permitido/denegado, destino denegado, límites y secretos con servidores
    locales controlados.
  - Ejecutar formato, `go vet ./...`, `go test ./...`, `go test -race ./...` y
    cobertura según `docs/verification.md`.
  - Registrar en `progress/http-tool/implementation.md` el mapa exacto
    `R<n> → test`, comandos, resultados y cualquier refactor mínimo necesario.
