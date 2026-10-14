# Adapter de modelo real — Tasks

- [x] T1. Definir los contratos neutrales, configuración y errores mediante tests. [R1, R5, R10, R11]
  - Escribir `TestSessionRequiresExplicitProviderModelProfileTimeoutAndBudgets`,
    `TestSessionsSelectOnlyConfiguredProviderAndOwnState`,
    `TestModelErrorsClassifyConfigContextHTTPProviderAndResponseWithoutTextMatching`
    y `TestAdapterHasNoExecutableCapabilitiesAndNeverMutatesRunEvidenceOrVerdict`.
  - Definir provider/model/profile explícitos, límites de bytes/tiempo/budget,
    `Provider`, request/result/usage, observer, categorías y snapshots propios.
  - Mantener `harness.Model` como puerto consumidor sin DTOs externos y
    conservar el fake offline; no introducir SDK, endpoint alternativo, modelo
    por defecto, precio, moneda ni handles ejecutables.
  - Documentar cada entidad y función nueva o modificada con su responsabilidad
    sistémica.

- [x] T2. Implementar la sesión por run, presupuestos y telemetría segura. [R1, R5, R6, R7, R8, R9, R10, R11]
  - Escribir `TestSessionUsesShortestDeadlineAndPropagatesCancellationAcrossAllBoundaries`,
    `TestCancelledOrExpiredSessionDoesNoExternalWork`,
    `TestEveryAttemptEmitsOneSafeTerminalRecordWithConfirmedUsage`,
    `TestObserverFailureIsFailClosedAndAllExcludedCanariesRemainAbsent`,
    `TestSessionReservesCallsAndReplansAtomicallyAndBlocksExhaustedDimensions`,
    `TestTokenUsageMayCrossOnceThenBlocksNextCall`,
    `TestMissingUsageClosesSession`, `TestBudgetModelContainsNoCostOrPricing` y
    `TestAdapterNeverRetriesAndExplicitRetryConsumesAnotherCall`.
  - Reservar call/replan antes del trabajo, aplicar el menor deadline, exigir y
    contabilizar usage terminal, cerrar nueva actividad si usage queda
    desconocido y devolver snapshots aislados.
  - Emitir exactamente un attempt record allowlisted por intento; hacer
    fail-closed el observer sin publicar una decisión no auditada.
  - Probar reservas concurrentes y snapshots bajo `-race`, sin locks durante
    callbacks o provider I/O y sin `time.Sleep`.
  - Añadir comentarios funcionales a toda entidad/función tocada.

- [x] T3. Definir perfiles OAuth y fronteras sustituibles de composición. [R2, R5, R6, R7, R9, R11]
  - Escribir primero tests de configuración de host ID, profile identity,
    callback, scopes/resource y separación de registrations.
  - Definir puertos mínimos para browser, loopback listener, OAuth HTTP,
    randomness, clock, ID-token validator y credential store; mantener todos
    los token records privados al paquete de autenticación.
  - Implementar store local con ruta inyectada, commit atómico y protección
    owner-only equivalente del SO, junto con fake store hermético.
  - Rechazar records parciales, perfiles duplicados/inconsistentes y cualquier
    intento de serializar tokens en configuración neutral o observabilidad.
  - Documentar la responsabilidad sistémica de entidades y funciones.

- [x] T4. Implementar registro, sign-in, refresh y revocación OpenAI OAuth. [R2, R5, R6, R7, R9, R11]
  - Escribir `TestOAuthRegistrationUsesHostPKCEStateNonceScopesAndIssuedClient`,
    `TestOAuthRejectsInvalidCallbackIdentityTokenAndScope`,
    `TestOAuthRefreshIsSerializedAndRotatesAtomically`,
    `TestProfilesNeverMixCredentialsAndSignOutRevokes` y
    `TestConcurrentRefreshUsesOneRotatingTokenExchange` con endpoints/browser/
    validator/store falsos.
  - Implementar state/nonce/PKCE nuevos, loopback exacto, registro dinámico,
    exchange con issued client, validación de ID token/identidad/scope y
    activación atómica del perfil.
  - Implementar refresh coalesced sin I/O bajo lock y revocación con resultado
    explícito; distinguir fallos temporales de tokens terminalmente inutilizables.
  - Propagar cancelación en listener, exchange, validator y store; demostrar
    que canarios en URLs, hints, claims y tokens no aparecen en errores/records.
  - Añadir comentarios sistémicos a cada símbolo nuevo o modificado.

- [x] T5. Implementar la petición OpenAI Responses y el transporte autenticado. [R3, R5, R6, R9, R10, R11]
  - Escribir `TestOpenAIRequestUsesOnlyResponsesOAuthStoreFalseStreamTrueAndSelectedModel`,
    `TestOpenAIRequestOmitsPreviewUnsupportedFieldsToolsAndSecrets` y casos de
    cancelación antes de headers/durante body mediante transporte fake.
  - Traducir request neutral a DTO privado con input autocontenido,
    instrucciones separadas del contexto no confiable y schema textual de la
    decisión JSON v1.
  - Usar exclusivamente endpoint/método oficial, `store:false`, `stream:true`,
    modelo explícito y bearer añadido por `AuthenticatedDoer`; desactivar
    redirects y retries automáticos.
  - No enviar fields preview incompatibles, tools, registry, profile ID ni
    secretos; aplicar límites de request/headers/stream antes de acumular.
  - Comentar entidades y funciones por su papel en la frontera provider.

- [x] T6. Consumir SSE, validar la decisión y clasificar resultados. [R4, R5, R6, R7, R11]
  - Escribir `TestOpenAIStreamReturnsOnlyCompletedExactValidatedDecisionWithUsage`,
    `TestOpenAIStreamRejectsMalformedTrailingOversizedFailedIncompleteAndPrematureResponses`,
    `TestModelErrorsNeverExposeCanaries` y tablas para admission/status/codes
    documentados.
  - Procesar frames SSE fragmentados/CRLF con presupuestos; aceptar solo una
    secuencia coherente que termine en `response.completed` y usage válido.
  - Agregar output text dentro del límite, decodificar exactamente un JSON con
    números conservados, validar versión/kind/campos/constraint y devolver
    modelos propios.
  - Mapear status/code a categorías sin comparar messages ni publicar body,
    detail, prompt/output, token o authorization URL; preservar solo metadata
    segura acotada y causas estándar.
  - Incluir comentarios funcionales en decoder, mappers y helpers.

- [x] T7. Añadir el smoke executable real y su composición opt-in. [R10, R11, R12]
  - Escribir tests de proceso/composición con OAuth y provider falsos para
    opt-in ausente, argumentos incompletos, auth/provider failure, éxito y
    stdout/stderr sin canarios.
  - Implementar acciones explícitas para provisionar/actualizar un perfil y
    ejecutar una única inferencia con provider/model/profile/budgets indicados;
    no aceptar bearer/API key por flag o variable.
  - Exigir opt-in antes de profile/network, usar el mismo runtime/adapter real y
    mostrar solo provider, model, duración, usage y outcome/category.
  - Mantener el executable fuera de `featureproof validate`, `go test`,
    generación y verificación implícita; documentar la invocación manual sin
    modelos o credenciales de ejemplo reales.
  - Comentar toda función y entidad por su responsabilidad en la composición.

- [x] T8. Cerrar integración, hermeticidad y trazabilidad. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Añadir `TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies`, tests de
    compatibilidad del harness/fake y una prueba integral fake OAuth → session
    → OpenAI stream → decisión → harness sin red ni herramientas ejecutadas por
    el adapter.
  - Verificar SSE fragmentado, límites exactos, races de budget/refresh,
    cancelación en cada frontera, store fail-closed y canarios en todos los
    artefactos publicables.
  - Ejecutar `gofmt` check, `go vet ./...`, compilación, `go test ./...`,
    `go test -race ./...`, repetición race de paquetes concurrentes y coverage
    según `docs/verification.md`; la prueba real no se ejecuta.
  - Confirmar que módulos y builds soportados no contienen secretos, pricing,
    modelo por defecto, endpoint alternativo o acceso externo en tests.
  - Registrar en `progress/model-adapter/implementation.md` el mapa
    `R<n> → test concreto → resultado`, la sustitución autorizada de coste por
    budgets no monetarios y la invocación/resultado manual del smoke solo si un
    humano decide ejecutarlo con una cuenta elegible.
  - No marcar la feature `done`; entregar la evidencia al reviewer.
