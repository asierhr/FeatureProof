# Review final independiente de tenant-security

Fecha: 2026-11-26

## Veredicto

**APPROVED.** No quedan hallazgos bloqueantes. Los tres blockers de la tercera
review están cerrados con implementación productiva, verticales conductuales y
el race canónico exacto verde. R1-R20 tienen implementación y tests concretos;
T1-T24 están completas.

## Precondiciones y alcance

- Feature 37 `tenant-security` es la única feature `in_progress`.
- La spec conserva aprobación humana explícita de
  `2026-11-20T08:52:19+01:00`.
- Las 24 tasks están `[x]` y existe informe vigente del implementer.
- Rama revisada: `feature-37-tenant-security`.
- Base aprobada: `1b0162237d941a72429a519076e9ea14e331064d`.
- Se leyeron AGENTS, arquitectura, convenciones, proceso SDD, verificación,
  spec completa, informe actual, review histórica y diff completo contra la
  base; no se modificó código, tests, spec ni estado de feature.

## Cierre de los blockers de la tercera review

| Blocker | Resultado independiente |
|---|---|
| B1 — attribution y recovery MCP multi-principal | Cerrado. `tenantMCPAuthenticator.authorize` retorna `capability.Scope.PrincipalID`; workflow y external-agent persisten ese principal interno. `PrepareHistorical` sella una capability mínima y secret-free en el record; `PendingHistorical`, `ConfirmHistorical` y `AbortHistorical` reconstruyen el scope histórico por intent. El vertical productivo usa `second-subject -> second-principal`, proceso reiniciado y replay de confirm sin conflicto de digest. |
| B2 — quota tras `EffectUnknown` | Cerrado. El error de proveedor marca la reservation `Uncertain` y conserva counters. Una terminalidad de aplicación no basta: `releaseEnvironment` exige `UnknownEffectReconciler`, propiedad de la frontera del proveedor, antes de `Release`. Error/ausencia del reconciliador conserva la reserva; tras restart, prueba de cleanup permite un único release y redelivery no decrementa otra vez. |
| B3 — race causal | Cerrado. `TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary` usa entrada causal por canal y separa el timeout reservado del observer. El comando canónico exacto, sin `-p=1`, terminó exit 0 con UCRT correctamente compuesto. |

Evidencia focal repetida:

- MCP, principal interno, authority histórica y saga recovery:
  `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`,
  `TestTenantAdmissionCrashBoundariesReconcileOnFreshService`,
  `TestLegacyTenantAdmissionRecoveryConvergesBothCrashWindows` y
  `TestTenantAdmissionRecoveryPropagatesEveryAuthorityFailure`, `-count=3`,
  pass.
- Quota external-agent normal/unknown/restart:
  `TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle`,
  `TestProductExternalUnknownEffectRequiresProviderReconciliation` y
  `TestScopedExternalRegistryPreservesTenantLifecycle`, `-count=3`, pass.
- Race global exacto:
  `CGO_ENABLED=1`, `CC=C:\msys64\ucrt64\bin\gcc.exe`, UCRT en `PATH`,
  `go test -race -count=1 -timeout=25m ./...`, sin `-p=1`, exit 0.
  Tiempos relevantes: `cmd/featureproof 140.846s`,
  `contextassembly 50.720s`, `contractreview 76.217s`,
  `githubpr 81.602s`, `goinspector 4.425s` y
  `validationworkflow 92.209s`.

Dos intentos previos sin anteponer UCRT a `PATH` fallaron durante link por no
poder cargar el runtime de GCC y no ejecutaron tests. No se cuentan como gate
de producto; la repetición con el entorno exacto del gate fue la ejecución
válida y terminó verde.

## Trazabilidad R1-R20

| Requirement | Implementación y test verificados | Resultado |
|---|---|---|
| R1 | Modelo/admin versionados; `TestAdministrationMutationsRemainAtomicAndVersioned`, `TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates` | pass |
| R2 | Resolución por subject confiable y principal interno en todas las requests; `TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority`, `TestAuthenticatedMCPResolvesEveryRequestPrincipal` | pass |
| R3 | Catálogo/RBAC fail-closed antes del consumer; `TestContractMCPAuthenticatesEveryRequestSubject`, `TestProductSurfaceHelpersFailClosedWithoutRequestIdentity` | pass |
| R4 | `Prepare -> Reserve -> Confirm/Abort/Recover`, authority y binding históricos; verticales de crash/restart/replay citados arriba | pass |
| R5 | Keys, cursores, predicates y relaciones compuestas local/PG; `TestStoreContract`, `TestPostgresTenantSecurityStoreContract`, `TestPostgresTenantSecurityScopesRelationshipsAndEpoch` | pass |
| R6 | Queue, cache, objetos y claims scopeados; `TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys`, `TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim` | pass |
| R7 | Policy/catalog/config y recovery tenant-scoped; `TestTenantPolicyAndCatalogSourcesRequireCurrentOrganizationCapability` y vertical MCP histórico | pass |
| R8 | Envelope cifrado/AAD y persistencia ciphertext-only; `TestSecretRotationCASAndCiphertextOnlyPersistence`, `TestPostgresTenantSecretsAuditAndLeaseRace` | pass |
| R9 | Lease exacta, single-use y callback privado; `TestSecretLeaseConsumesAndAuditsBeforePrivateCallback`, `TestSecretConsumeRejectsDurableLeaseDrift` | pass |
| R10 | Rotación/revocación CAS sin fallback; `TestSecretTamperExpiryRevocationAndCancellationFailClosed`, `TestSecretRotationCASAndCiphertextOnlyPersistence` | pass |
| R11 | Transporte productivo fail-closed; `TestTransportSecurityFailsClosedAndPinsReconnect`, `TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention` | pass |
| R12 | Journal durable allow/deny y cadena/cursor; `TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure`, `TestAuditPaginationScopesCursorsAndDetectsTailTamper` | pass |
| R13 | Mutación+receipt+audit causal y unknown commit; `TestDurableAuthorityFailsClosedBeforeDecisionAudit`, `TestSensitiveUnknownCommitAndDenyNeverGrants`, `TestAdministrationFaultsAreAtomicAndUnknownCommitResolves` | pass |
| R14 | Policy/floors/boundary de elegibilidad; `TestRetentionPolicyEligibilityFreezesBoundary`, `TestRetentionControllerRejectsStalePlansAndTamperedIntents` | pass |
| R15 | Intent previo, delete convergente y project deletion; `TestRetentionControllerConvergesUnknownDelete`, `TestProjectDeletionBlocksAdmission`, `TestRetentionBatchAndGraphRespectDependencyOrdering` | pass |
| R16 | Reservation pre-effect, incertidumbre y release exactly-once; vertical unknown, `TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion`, `TestQuotaJobQueueRestartReleasesExactRecoveredJobs` | pass |
| R17 | Categorías cerradas, retries completos, cancelación y corrupción; `TestClosedBoundaryAdversarialMatrix`, `TestSnapshotValidationRejectsEveryForgedProjectionFamily`, `TestTenantSecurityErrorMappingCoversContextAndTypedFailures` | pass |
| R18 | Metadata allowlisted y observabilidad no autoritativa; `TestSubjectDigestDoesNotPersistRawSubject`, `TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift` | pass |
| R19 | Contratos local/PG, migraciones N/N+1, comentarios sistémicos y selección sin fallback | pass |
| R20 | Verticales adversariales, global normal/race, seis builds, coverage exacta y manifest cerrado | pass |

La matriz de superficies exige CLI, API, MCP, GitHub, external-agent y worker;
`TestSurfaceGatewayRequiresAndConvergesEveryProductSurface`,
`TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects` y los
verticales de composition rechazan provider o identidad ausente y spoofing.

## Verificaciones independientes

| Verificación | Resultado |
|---|---|
| `gofmt -l cmd internal` y `git diff --check` | pass; solo avisos informativos LF/CRLF de Git |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 -timeout=20m ./...` | pass; `cmd 50.674s`, `contextassembly 14.353s`, `validationworkflow 47.049s` |
| Race canónico exacto UCRT sin `-p=1` | pass, exit 0 |
| Seis `CGO_ENABLED=0 go build ./...`: Windows/Linux/Darwin amd64/arm64 | pass |
| `go mod verify` | pass, `all modules verified` |
| Auditorías de comentarios controlplane/workflow/GitHub/preview/functional | pass |
| Escaneo del diff por private keys, access keys, DSN con password y bearer material | sin coincidencias |

## Manifest, cobertura PostgreSQL y cleanup

- Inventario independiente real: **89 production + 53 tests = 142 archivos
  Go**, 4 fuentes zero-statement y 17 paquetes. El manifest no tiene entradas
  missing ni stale.
- Las cuatro fuentes zero-statement son correctas: `controlplane/contracts.go`
  añade campos/interfaces, `environment/cleanup/controller.go` añade una firma,
  `externalagent/service.go` añade dos interfaces y
  `validationworkflow/model.go` añade un campo y comentario; no contienen
  statements instrumentables nuevos. En particular,
  `validationworkflow/service.go` no está excluido y permanece medido.
- El gate PostgreSQL 16 terminal vigente del implementer usa la base exacta y
  terminó exit 0: normal `36.997/117.399/0.528s`, race
  `50.642/125.555/3.732s`, Linux y N/N+1 verdes.
- Dos bases independientes y cuatro generaciones produjeron la misma firma y
  `4725/5545 = 85.21190261496844003606853020739404869252%`, por encima del 85%
  exacto sin redondeo. El inventario que alimenta esa medición fue reproducido
  de forma independiente en esta review.
- Cleanup final independiente: 0 distros WSL owned, 0 roots
  `featureproof-pg35-gate-*`, 0 procesos owned, 0 perfiles de coverage/binarios
  diagnósticos y 0 `__pycache__` propios.

## Hallazgos

No se identificaron hallazgos blocker, high, medium o low pendientes. La
implementación conserva dirección de dependencias, ownership de estado,
cancelación, lifecycle de goroutines y ausencia de I/O externo bajo las
fronteras transaccionales/locks revisadas.

approved -> progress/tenant-security/review.md
