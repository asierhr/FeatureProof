# Tareas — Bucle verificable de corrección

Cada tarea comienza con tests que fallen, implementa el comportamiento mínimo
y conserva comentarios de responsabilidad sistémica en toda entidad, función
y helper de test nuevo o modificado. No se ejecuta ninguna tarea antes de la
aprobación humana de la spec.

- [x] T1. Definir modelos, invariantes, canonización y errores del dominio de corrección. [R1, R2, R4, R6, R12]
  - Añadir `TestCorrectionModelsRejectAmbiguousIdentityAndUnsafeLimits`, `TestFailureSelectionIsCompleteCanonicalAndUnmodifiable` y tablas de clasificación exhaustivas.
  - Implementar `CorrectionLink`, `ScenarioIdentity`, plan de fases, outcomes, comparison y hashes versionados con copias defensivas y límites.

- [x] T2. Implementar journal, projector, presupuestos de cadena e idempotencia durable. [R1, R2, R8, R10, R12]
  - Añadir `TestCorrectionSubmitIsDurablyIdempotent`, `TestCorrectionChainCreatesImmutableLinearChildren`, `TestConcurrentTailAdmissionHasOneWinner` y recuperación desde journal corrupto/interrumpido.
  - Implementar compare-and-append por cadena, índice de key hasheada, ordinal/tail, deadline y contadores sin overflow, snapshot atómico y lock multiproceso sin callbacks bajo lock.

- [x] T3. Construir admisión, preflight, selección y aprobación de continuación. [R1, R3, R4, R8, R9, R12]
  - Añadir `TestCorrectionSubmitRequiresOwnedFailedParent`, `TestCorrectionRequiresDistinctCompatibleDeployedRevision`, `TestRevisionNotDeployedStopsBeforeFunctionalWork`, `TestObsoleteScenarioIsNotApplicable` y `TestContinuationApprovalIsBoundOneUseAndStaleSafe`.
  - Leer el parent solo mediante package reader, resolver alias/policy confiables, reservar intento, exigir binding hijo compatible y distinto, clasificar aplicabilidad y consumir la autorización exacta o fallar cerrado.

- [x] T4. Orquestar failed-first, regresión requerida, límites y cancelación del hijo. [R5, R7, R8, R9, R10, R12]
  - Añadir `TestFailedScenariosRunBeforeRequiredRegression`, `TestRegressionRechecksPriorPassAndDeduplicatesOnlyChildEvidence`, `TestChainLimitsStopBeforeNewWork`, `TestInfrastructureFailureNeverBecomesFunctionalFailure` y `TestCorrectionCancelJoinsAndPreservesParent`.
  - Implementar coordinator secuencial con barrera de fase, pipeline ordinario inyectado, accounting confirmado, supervisor con owner/join y finalización acotada; mantener executor productivo fail-closed sin feature 19.

- [x] T5. Comparar paquetes verificados y publicar el resultado no-replace. [R6, R7, R9, R12]
  - Añadir `TestComparisonClassifiesCorrectedPersistentNewAndInconclusive`, `TestComparisonRejectsCorruptOrForeignPackage`, `TestChildVerdictUsesOnlyCurrentEvidence`, `TestNewRegressionKeepsCorrectedChildFailed` y fallo de publicación/canarios.
  - Implementar unión por identidad estable, tabla cerrada, orden canónico, referencias con origen y comparison artifact atómico; no devolver comparación parcial ante error operativo o de integridad.

- [x] T6. Completar concurrencia, cancelación y recuperación multiproceso. [R2, R8, R10, R12]
  - Añadir `TestInterruptedOwnerDoesNotRerun`, `TestIndependentChainsProgressConcurrently`, cancelación en cada frontera, retry de cancelación y failure injection antes/después de append/publicación.
  - Verificar leases, CAS, preservación byte-for-byte del parent, terminal único, ausencia de goroutines huérfanas y recuperación sin crear ni ejecutar hijos automáticamente.

- [x] T7. Extender el contrato de aplicación, CLI y MCP con operaciones de corrección. [R1, R10, R11, R12]
  - Añadir `TestCorrectionCLIAndMCPContractParity`, `TestCorrectionScopesAndSecretProjection` y casos contractuales de submit/status/result/cancel, in-progress, conflicto, principal ajeno, límites, revisión no desplegada y paquete corrupto.
  - Publicar application contract v2 y schemas cerrados, añadir comandos CLI y tools MCP sobre el mismo service, reautorizar handles/scopes por operación y conservar semántica v1.

- [x] T8. Ejecutar integración vertical y verificación canónica. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12]
  - Añadir `TestCorrectionFullFlowHermetic` para corrección exitosa, fallo persistente, regresión nueva, mismo commit, escenario obsoleto, aprobación, límite e infraestructura con fakes deterministas.
  - Ejecutar formato, `go vet ./...`, compile-only, `go test ./...`, race completo y repetido en paquetes afectados, coverage y builds aplicables según `docs/verification.md`.
  - Registrar durante implementación el mapa `R→test→resultado`, tasks, comandos, coverage y limitaciones en `progress/correction-loop/implementation.md`, y revisar que no se mezclaron otras features.
