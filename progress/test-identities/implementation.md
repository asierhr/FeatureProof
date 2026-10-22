# Feature 25 — Test identities — Implementation

## Estado de entrega

`ready_for_review`. T1–T13 están implementadas y verificadas. La feature se
mantiene `in_progress`; este implementer no ha modificado `feature_list.json`.

## Implementación y primera remediación

- Specification/YAML v2 declara identidades, referencias opacas, mecanismos
  cerrados y permisos HTTP exactos; v1 conserva su schema y semántica.
- `internal/testidentity` posee el catálogo inmutable, descriptores canónicos,
  bindings, errores tipados, leases efímeros y transporte de inyección tardía.
- Agent planning recibe solo descriptores aplicables y exige una selección
  explícita. Plan, manifest, provenance, approval, recovery, executor y journal
  conservan únicamente identity ID, operación y digest seguros.
- El executor revalida identidad y policy por cada intento antes del resolver y
  de I/O. El HTTP tool resuelve e inyecta después de los guards, mide la request
  final y vuelve a validar redirects.
- La composición productiva valida resolvers y observer antes de construir el
  modelo. Los resolvers leen únicamente la variable de entorno confiable
  mapeada durante el intento terminal; el material no entra en config, modelo,
  plan, journal, evidence ni artifacts.
- CLI, API, MCP y GitHub alcanzan el mismo servicio, snapshot y binding. La
  policy de redacción se amplía una vez antes de planning y se comparte con la
  ejecución.

| Review | Corrección y evidencia |
|---|---|
| REV-1 | Manifest v2 rechaza `method`/`path` referenciados antes de construir policy, resolver o red: `TestManifestV2RejectsReferencedAuthenticatedOperationBeforeExecution`. |
| REV-2 | Policy y binding se reevalúan en cada retry: `TestRetryReevaluatesPolicyBeforeSecondToolEffect`, `TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect`. |
| REV-3 | Composición productiva previa al modelo, restart/no-resolver y paridad de superficies: `TestWorkflowIdentityCompositionIsLateRestartableAndFailClosed`, `TestIdentityWorkflowParityAcrossAPICLIAndIntegrations`. |
| REV-4 | Ownership inmediato de lease incluso con error/cancel simultáneo: `TestLeaseReturnedWithErrorIsReleasedExactlyOnce`. |
| REV-5 | Observer síncrono con timeout configurado en éxito/error/cancel: `TestTerminalObservationUsesConfiguredBound`. |
| REV-6 | Deep-copy, orden canónico, digest y sellado en admission/approval/recovery/pre-I/O: `TestAdmissionSnapshotDeepCopiesAndSealsIdentityDescriptors`, `TestServiceIsolationSurvivesCallerIdentityMutation`, `TestApprovalRecoveryRejectsIdentityAuthorizationDrift`. |
| REV-7 | Una policy efectiva aumentada antes de planning rechaza headers y JSON paths de autenticación: `TestWorkflowEffectiveRedactionRejectsIdentityAssertionsBeforePlanning`, `TestIdentityAssertionRedactionIsSharedBeforeRunnerComposition`. |
| REV-8 | `gofmt -w .`, comentario de `packagePlanDocument`, auditoría de declaraciones nuevas/modificadas y matriz completa de gates. |

## Segunda remediación independiente

| Review | Corrección y evidencia |
|---|---|
| REV-3 | `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces` recorre la composición real hasta terminal por API, CLI, MCP y GitHub y compara identity ID, digest, outcome, código y contadores de modelo/resolución/red. `TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial` demuestra redelivery sin duplicación y recuperación de pendiente tras recrear `Service` y resolver material fresco. `TestProductIdentityCompositionStopsBeforeInferenceWhenResolverCoverageIsMissing` corta antes del modelo y red. |
| REV-9 | La reserva atómica de retry+tool-call ocurre tras revalidar identity y policy del intento y antes del resolver. `TestRetryReevaluatesPolicyBeforeSecondToolEffect` y `TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect` verifican budgets exactos, orden y cero efectos nuevos; `TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt` cubre el retry autorizado. |
| REV-10 | `identity_attempt_terminal` registra run/plan/step/tool-call, identity ID, digest, ordinal y categoría cerrada para éxito, drift, policy, budget, credencial, cancelación inicial y cancelación de retry. Lo cubren `TestInitialIdentityRejectionsEmitOneTerminalWithoutBudgetOrEffects`, `TestIdentityTerminalAuditClassifiesCredentialFailureAndCancellation` y las pruebas de retry anteriores. |
| REV-11 | `composeWorkflowIdentities` compara exactamente referencias únicas. `TestWorkflowIdentityCompositionUsesExactUniqueCredentialReferenceSet` permite referencias compartidas y rechaza mappings faltantes y sobrantes. |

La vertical productiva también cerró tres defectos de integración que impedían
la paridad real: `planningInputValue` entrega el payload literal al validador
HTTP, `workflowApprovalSanitizer` normaliza structs tipados a JSON antes de
redactar y `verifyBoundSequence` canonicaliza los bindings aprobados conservando
su referencia opaca. Sus regresiones están cubiertas por
`TestPlanningInputValueExposesLiteralPayloadToExecutableValidation`,
`TestApprovedPlanBindingRetainsItsOpaqueReferenceDuringPublication` y la ruta
GitHub del test vertical.

## Tercera remediación independiente

| Review | Corrección y evidencia |
|---|---|
| REV-3 | La composición acepta un `workflowCredentialResolverFactory` confiable y el test usa un wrapper del resolver de entorno productivo para contar `Resolve`, sin inferirlo de eventos. `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces` verifica éxito real; `TestProductIdentityFailureParityAcrossAllSurfaces` verifica el mismo fallo de credencial, categoría `inconclusive`, códigos/proyecciones propios y cero red en API, CLI, MCP y GitHub. `TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial` comprueba una sola resolución en redelivery y recrea `Service`/composición, llama explícitamente a `Service.Recover` sin efectos y termina con resolver/material frescos. |
| REV-12 | `TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets` ejecuta dos runs completos mediante planning, transport y journal con dos valores rotados. Observa exactamente dos llamadas reales a `Resolve`, un valor distinto por request, identity ID/digest estables, un terminal ordinal 1 `success` por run y el primer journal inmutable. El escaneo recursivo de `.featureproof` rechaza ambos canarios y `cred_customer` en journal, evidence, reports, artifacts y demás archivos durables. |

El publisher durable conserva ahora en `identity_attempt_terminal` los cuatro
campos seguros del evento interno: identity ID, operation binding digest,
ordinal y categoría cerrada. El package reader los reconstruye sin incorporar
reference, material, expiry ni texto de colaboradores.

## Cuarta remediación independiente

| Review | Corrección y evidencia |
|---|---|
| REV-13 | `packagePlanTopology` conserva el identity ID y operation-binding digest sellados de cada step. `validatePackageEvents` exige en cada `identity_attempt_terminal` presencia conjunta, coincidencia exacta, attempt positivo y contiguo por step y categoría del vocabulario cerrado; también rechaza identity ID/digest en tipos incompatibles y valida los eventos compatibles. `TestOfflinePackageRejectsRehashedIdentityEventTampering` altera individualmente ausencia/mismatch de ID y digest, attempt cero/repetido/discontinuo, categoría arbitraria, campos en evento ajeno y metadatos incompatibles de started/finished. Cada caso regenera inventario y manifest y aun así falla por `binding_integrity`. |
| REV-14 | `validatePlanTopology`, `validatePackageEvents`, la expectativa durable y todos los helpers/fixtures nuevos tienen comentario sistémico. `TestOfflinePackageAcceptsBoundIdentityEvents` confirma que el productor normal permanece legible. |

Cobertura directa de la frontera modificada: `validatePlanTopology` 89,3 %,
`validatePackageEvents` 86,0 %, `validIdentityTerminalCategory` 100 % y
`validIdentityExecutionCategory` 100 %.

La cuarta remediación pasó los adversarios ×20, la suite completa de
`internal/localvalidation` excluyendo únicamente el subtest de symlink, race
UCRT64 focal, regresiones productivas de identidad/paridad/rotación, planning
terminal ×20, `go vet ./...`, compile-only, cross-build Linux/Darwin, `gofmt`
y `git diff --check`. La suite global conserva exclusivamente la limitación
histórica del symlink de Windows sin privilegio.

## Quinta remediación independiente

REV-15 queda cerrado mediante contratos explícitos por tipo y step. En steps
autenticados, `tool_execution_started`, `tool_execution_finished`,
`retry_scheduled` e `identity_attempt_terminal` exigen su tuple aplicable y lo
comparan con el plan sellado. Policy/budget admiten únicamente su ordinal, y
los eventos ajenos prohíben individualmente identity ID, binding digest,
attempt y outcome. El reader registra intentos observados, terminales por clave
step+attempt y exige correspondencia exacta al final; un terminal que representa
un rechazo pre-I/O solo puede iniciar el intento bajo el vocabulario cerrado y,
desde el segundo intento, tras un terminal transitorio previo.

`TestOfflinePackageRejectsRehashedIdentityEventTampering` incluye los seis
adversarios exactos de review: terminal único borrado, terminal extra ordinal 2,
solo attempt o solo outcome en `run_created`, finished sin ID/digest con
categoría arbitraria y started sin ID/digest/attempt. Todos regeneran inventario
y manifest y fallan por `binding_integrity`. Se preservan los doce adversarios
anteriores y `TestOfflinePackageAcceptsBoundIdentityEvents` confirma el productor
normal. `TestIdentityTerminalStandaloneVocabulary` cubre completamente el
vocabulario pre-I/O.

Cobertura directa actual: `validatePlanTopology` 89,3 %,
`validatePackageEvents` 88,6 %, y los tres validadores de vocabulario 100 %.

## Sexta remediación independiente

REV-16 queda cerrado con una máquina de fases por step+attempt en el package
reader. Policy abre el intento, budget requiere policy, los marcadores de retry
solo aparecen desde el segundo intento y started/finished son únicos y
ordenados. Los terminales `success`, `transient`, `permanent` y
`credential_rejected` requieren una ejecución completa y concuerdan con la
categoría de `tool_execution_finished`; cancelación y rechazos pre-I/O recorren
rutas cerradas explícitas. El terminal cierra el intento y ningún evento
compatible posterior puede modificarlo. Un intento posterior solo puede abrir
cuando su predecesor terminó como `transient`.

`TestOfflinePackageRejectsRehashedIdentityEventTampering` incorpora los tres
adversarios exactos de review: terminal success antes de started/finished,
policy+budget+success sin eventos de ejecución y terminal permanent frente a
finished exitoso. También cubre eventos posteriores al terminal, fases
duplicadas/desordenadas, retry en intento inicial y apertura prematura del
siguiente intento. Cada journal manipulado regenera inventario y manifest antes
de ser rechazado por `binding_integrity`.

`TestIdentityAttemptPhaseMachineAcceptsValidPaths` conserva rutas válidas de
retry, cancelación antes/después de policy y durante ejecución, además de
composition unavailable, drift, policy error/denied y budget denied.
`TestIdentityAttemptPhaseMachineRejectsEventsAfterTerminal` y
`TestIdentityAttemptOrderingRequiresClosedTransientPredecessor` prueban cierre
y orden entre intentos. La frontera modificada alcanza 89,7 % en
`validatePackageEvents` y 100 % en la máquina de fases y sus validadores.

La sexta remediación pasó adversarios y rutas válidas ×20, race UCRT64 ×5,
regresiones de planning/workflow/superficies ×5, compile-only, `go vet`,
cross-build Linux/Darwin, `gofmt` y `git diff --check`. La suite global conserva
únicamente el fallo histórico `evidence_symlink` porque Windows no concede el
privilegio de creación de symlinks; todos los demás paquetes pasan.

## Séptima remediación independiente

REV-17 queda cerrado en productor y reader. `PlanExecutor` vuelve a observar el
contexto inmediatamente después de confirmar budget y después del marker de
retry. Una cancelación en cualquiera de esos checkpoints, o un append de
`tool_execution_started` que retorna cancel/deadline antes de confirmar el
evento, emite exactamente un `identity_attempt_terminal: cancelled`. El append
terminal usa un contexto sin la cancelación original y con timeout explícito;
si falla, el error conserva mediante `errors.Join` tanto la cancelación como el
fallo de cierre.

El reader acepta `cancelled` en los prefijos válidos vacío, policy,
policy+budget y policy+budget+retry, además de la ruta completa de ejecución
cancelada. El terminal sigue cerrando el intento y las reglas de orden,
ordinal, retry y concordancia permanecen intactas.

`TestIdentityCancellationAfterBudgetAndRetryMarkerClosesAttempt` cancela de
forma determinista tras budget inicial, budget de retry y retry marker: observa
un terminal por intento, ausencia de execution-started posterior y cero nuevas
ejecuciones de tool. `TestCancelledExecutionStartAppendReconcilesTerminal`
cubre `context.Canceled` y `DeadlineExceeded`; `TestCancelledAttemptPreservesTerminalAppendFailure`
demuestra causalidad compuesta y deadline de cierre.
`TestValidatePackageEventsAcceptsCancelledPreIOPrefixes` valida secuencias
completas y bound para cancelación inicial y de retry, y la matriz de fases
cubre ambos prefijos.

Pasaron focales deterministas ×20/×30, race UCRT64 ×5, regresiones de identidad
y superficies ×5, planning 88,5 % de cobertura, `validatePackageEvents` 89,7 %,
helpers de cierre/fase 100 %, compile-only, `go vet`, cross-build Linux/Darwin,
`gofmt` y diff-check. La suite global conserva solo el subtest histórico de
symlink de Windows sin privilegio.

## Octava remediación independiente

REV-18 queda cerrado mediante una única frontera `appendIdentityPreIO` para
policy, budget, retry y execution-started. Si cualquier append devuelve
`context.Canceled` o `DeadlineExceeded`, el executor conserva el error original
y escribe un solo terminal `cancelled` con el último prefijo confirmado: vacío
si falla policy, policy si falla budget, y policy+budget si falla el marker de
retry. Si el terminal también falla, `errors.Join` conserva las dos causas.
Errores operacionales ajenos a cancelación permanecen sin terminal inventado.

`TestIdentityPreIOAppendCancellationReconcilesLastConfirmedPrefix` cubre cancel
y deadline en policy/budget de intentos iniciales y retry, y en el marker de
retry. Verifica orden/cardinalidad exactos, contexto terminal acotado, budgets y
contadores separados de tool/resolver/red sin efectos posteriores.
`TestIdentityPreIOAppendOperationalFailureDoesNotInventCancellationTerminal`
cubre la rama no cancelada y `TestCancelledAttemptPreservesTerminalAppendFailure`
la causalidad doble.

`TestValidatePackageEventsAcceptsCancelledPreIOPrefixes` construye secuencias
bound completas para los siete prefijos legítimos: vacío, policy y budget en el
intento inicial; vacío, policy, budget y retry marker en el segundo intento tras
un transitorio. El reader conserva cierre terminal y los adversarios previos
siguen bloqueando cualquier fase posterior.

Los focales pasan ×30, race UCRT64 ×5 y regresiones de identidad/superficies ×5.
Planning alcanza 89,1 %, `appendIdentityPreIO` y helpers de cierre 100 %,
`validatePackageEvents` 89,7 %. Compile-only, vet, cross-build Linux/Darwin,
gofmt y diff-check pasan; la suite global conserva solo el symlink histórico de
Windows sin privilegio.

## Tasks completadas

| Task | Evidencia principal |
|---|---|
| T1–T2 | Modelo y loader v2 estrictos con compatibilidad v1. |
| T3–T4 | Catálogo/descriptores canónicos, copia profunda, planning y provenance. |
| T5–T7 | Manifest/plan/executor fail-closed, policy por intento, lease/transporte y cancelación. |
| T8–T9 | Redacción monotónica, journal seguro, approval/recovery y drift. |
| T10–T11 | Composición confiable y paridad CLI/API/MCP/GitHub sin entradas secretas. |
| T12 | Observer terminal acotado, canarios, concurrencia y race. |
| T13 | Comentarios, formato, gates, cobertura y trazabilidad reproducible. |

## Trazabilidad requirements → tests

| Requirement | Tests concretos |
|---|---|
| R1 | `TestV2ValidatesTypedIdentitiesWithoutCredentialValues`, `TestV2LoadsTypedIdentitiesWithoutCredentialValues`, `TestV1CompatibilityRemainsIdentityFree`. |
| R2 | `TestAuthenticatedOperationRequiresExactDeclaredIdentityBinding`, `TestIdentityBindingRejectsTargetMethodPathAndTemplateConfusion`, `TestManifestV2RejectsReferencedAuthenticatedOperationBeforeExecution`. |
| R3 | `TestDescriptorSnapshotsCloneCanonicalizeValidateAndDigest`, `TestModelReceivesOnlyApplicableSafeIdentityDescriptors`, `TestAdmissionSnapshotDeepCopiesAndSealsIdentityDescriptors`, `TestServiceIsolationSurvivesCallerIdentityMutation`. |
| R4 | `TestCredentialInjectionOccursAfterAllGuardsAndOnlyInsideTransport`, `TestAuthenticatedHTTPInjectionOccursBeforeDNSOnlyAfterBindingGuards`, `TestTransportMechanismsLimitsAndNetworkClassification`. |
| R5 | `TestRetryReevaluatesPolicyBeforeSecondToolEffect`, `TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect`, `TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt`, `TestLeaseReturnedWithErrorIsReleasedExactlyOnce`, `TestCredentialLeasesAreIsolatedValidAndReleasedPerAttempt`, `TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets`. |
| R6 | `TestAuthRedactionIsCaseInsensitiveMonotonicAndFailClosed`, `TestWorkflowEffectiveRedactionRejectsIdentityAssertionsBeforePlanning`, `TestIdentityAssertionRedactionIsSharedBeforeRunnerComposition`, `TestOfflinePackageRejectsRehashedIdentityEventTampering`. |
| R7 | `TestIdentityBindingDriftStopsBeforeCredentialResolution`, `TestManifestV2RejectsReferencedAuthenticatedOperationBeforeExecution`, `TestAuthenticatedHTTPRedirectRevalidatesIdentityOperation`. |
| R8 | `TestRetryRevalidatesIdentityBeforeSecondPolicyAndToolEffect`, `TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt`, `TestApprovalRecoveryRejectsIdentityAuthorizationDrift`, `TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial`, `TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets`. |
| R9 | `TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces`, `TestProductIdentityFailureParityAcrossAllSurfaces`, `TestProductIdentityCompositionStopsBeforeInferenceWhenResolverCoverageIsMissing`, `TestWorkflowIdentityCompositionUsesExactUniqueCredentialReferenceSet`, `TestAllSurfacesRejectSecretInputsBeforeIdentityAwareWorkflow`. |
| R10 | `TestIdentityAuditContainsOnlySafeBindingFactsInOrder`, `TestInitialIdentityRejectionsEmitOneTerminalWithoutBudgetOrEffects`, `TestIdentityTerminalAuditClassifiesCredentialFailureAndCancellation`, `TestAuthorizedRetryReservesAtomicallyAndEmitsOneTerminalPerRotatedAttempt`, `TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets`, `TestOfflinePackageAcceptsBoundIdentityEvents`, `TestOfflinePackageRejectsRehashedIdentityEventTampering`, `TestIdentityTerminalStandaloneVocabulary`, `TestIdentityAttemptPhaseMachineAcceptsValidPaths`, `TestIdentityAttemptPhaseMachineRejectsEventsAfterTerminal`, `TestIdentityAttemptOrderingRequiresClosedTransientPredecessor`, `TestIdentityCancellationAfterBudgetAndRetryMarkerClosesAttempt`, `TestIdentityPreIOAppendCancellationReconcilesLastConfirmedPrefix`, `TestValidatePackageEventsAcceptsCancelledPreIOPrefixes`. |
| R11 | `TestCancellationAtEveryIdentityBoundaryStopsAndReleases`, `TestLeaseReturnedWithErrorIsReleasedExactlyOnce`, `TestTerminalObservationUsesConfiguredBound`, `TestIdentityCancellationAfterBudgetAndRetryMarkerClosesAttempt`, `TestIdentityPreIOAppendCancellationReconcilesLastConfirmedPrefix`, `TestIdentityPreIOAppendOperationalFailureDoesNotInventCancellationTerminal`, `TestCancelledExecutionStartAppendReconcilesTerminal`, `TestCancelledAttemptPreservesTerminalAppendFailure`, race focal. |

## Verificación reproducible

Pasaron:

```text
gofmt -w .
gofmt -l .                         # salida vacía
go test ./... -run '^$'
go vet ./...
go test <focales adversarios REV-1..REV-7> -count=20
go test ./internal/localvalidation -run 'TestIdentityAware|TestIdentityAssertion' -count=20
go test ./internal/planning -run 'TestIdentityTerminalAudit|TestAuthorizedRetry|TestInitialIdentity|TestRetryRe' -count=20
go test ./cmd/featureproof -run 'Test(ProductIdentity|WorkflowIdentityCompositionUsesExactUniqueCredentialReferenceSet)' -count=5
CGO_ENABLED=1 (UCRT64) go test -race ./internal/testidentity ./internal/planning ./internal/validationworkflow ./cmd/featureproof -run <focales> -count=1
GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build ./...
git diff --check
```

Cobertura final observada con `go test -cover -count=1`:

| Paquete | Cobertura |
|---|---:|
| `internal/testidentity` | 91,9 % |
| `internal/tools/http` | 91,1 % |
| `internal/agentplanning` | 87,3 % |
| `internal/planning` | 88,6 % |
| `internal/planmanifest` | 91,8 % |
| `internal/specification` | 95,5 % |
| `internal/specification/yaml` | 87,3 % |
| `internal/evidence` | 90,2 % |
| `internal/validationworkflow` | 85,6 % |

Los helpers nuevos de composición en `cmd/featureproof/workflow.go` tienen 100 %
de cobertura individual. La suite global `go test ./... -count=1` pasa en todos
los paquetes salvo el subtest histórico
`internal/localvalidation/evidence_symlink`: Windows devuelve
`A required privilege is not held by the client` al crear el symlink. Las
pruebas focales completas de identidad/localvalidation pasan repetidas y los
cross-build Linux/Darwin pasan.

## Auditoría de seguridad y alcance

- No hay material ni `credential_ref` en model requests, planes, journal,
  evidence, reportes, proyecciones o payloads de superficies.
- Errores externos se reducen a categorías tipadas; cancelación y deadlines se
  propagan sin trabajo detached.
- La liberación del lease es idempotente y ocurre una vez aun cuando resolver
  devuelve lease y error simultáneamente.
- El formateo del árbol incluye archivos históricos que `gofmt -l .` listaba;
  esos cambios adicionales son exclusivamente de formato para cerrar REV-8.
- No se añadió backend de secretos, selector externo de identidad ni trabajo de
  feature 26. `feature_list.json` permanece intacto.
