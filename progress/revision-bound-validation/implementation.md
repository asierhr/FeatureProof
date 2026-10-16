# Implementación de revision-bound-validation

## Estado

T1–T8 implementadas y verificadas. La spec fue aprobada por el usuario el 2026-10-16. No se ha cambiado el estado `in_progress` ni el historial de esta feature; falta review independiente.

## Correcciones durante la primera review

- `GitResolver` ahora elimina todas las variables `GIT_*` heredadas y fija únicamente opciones seguras para sus subprocesos. `TestGitResolverIgnoresHostileGitEnvironment` cubre repositorio, worktree, objeto, índice y configuración hostiles del proceso padre.
- `Publisher.Publish` verifica todas las referencias publicadas, incluidas las de pasos, evidencia terminal y assertions anidadas. Ausencia, corrupción, ID/run ajeno y binding ajeno se clasifican como `binding_integrity` antes de hacer visible el bundle. `TestReportOnlyEvidenceReferencesMustBelongToBinding`, `TestPublisherRejectsEvidenceIdentityMismatchWithTypedError` y `TestPublisherRejectsCorruptEvidenceWithTypedError` cubren la frontera.
- `verifyBoundSequence` exige un único evento terminal al final que coincida con `RunSnapshot.Status`. `TestPublisherRejectsMissingOrMismatchedTerminal` cubre omisión, estado distinto y duplicación.
- `Publisher.Publish` reobserva cancelación antes de cada escritura y de la visibilidad final. `TestPublisherCancellationDuringStagingNeverCommits` cancela después de un artefacto y tras sincronizar staging. `Runner.Run` clasifica cancelación tras el último plan/recheck antes del terminal y mantiene un contexto cancelable para publicar veredictos activos. `TestCancellationAfterLastPlanCannotPublishPassedVerdict` exige `run_cancelled` y nunca `run_completed`.

## Requirements y tests

| Requirement | Tests de comportamiento | Paquete | Resultado |
|---|---|---|---|
| R1 | `TestResolveFullCommitAndFreezeMovedRef`; `TestRejectMissingAmbiguousOrForeignCommit`; `TestGitResolverRejectsSubdirectoryAndCancellation`; `TestGitResolverIgnoresHostileGitEnvironment`; `TestRunnerPublishesAttestedRevisionBinding`; `TestRunnerWithLocalGitAndTrustedAttestor` | `revisionbinding`, `localvalidation` | pass |
| R2 | `TestDirtyWorktreeNeverAttributedToHEAD`; `TestGitResolverIgnoresHostileGitEnvironment` (índice ajeno); `TestAdmissionStopsAfterDirtyOrMalformedSource`; `TestUnclassifiedLiteralStopsBeforeRunOrTool`; `TestRunnerWithLocalGitAndTrustedAttestor` | `revisionbinding`, `localvalidation` | pass |
| R3 | `TestEnvironmentMustAttestExactRepositoryAndCommit`; `TestAbsentOrUntrustedAttestationStopsExecution`; `TestRecheckDetectsRevisionAndAttestationDrift`; `TestRevisionGuardStopsDriftAndStaleApprovalBeforeExecutor`; `TestRevisionOrConfigurationDriftStopsBeforeTool`; `TestRunCommandRejectsUnattestedProductionBoundary` | `revisionbinding`, `replanning`, `localvalidation` | pass |
| R4 | `TestBindingHashesAreStableAndSensitiveToSemanticChanges`; `TestStaleApprovalCannotBindChangedPlan`; `TestRevisionGuardStopsDriftAndStaleApprovalBeforeExecutor`; `TestExecuteBoundRejectsStaleGrantBeforeAuditAndTools`; `TestStaleGrantCannotConfirmExecutableBatch` | `revisionbinding`, `replanning`, `planapproval` | pass |
| R5 | `TestEventsAndEvidenceShareBinding`; `TestForeignBindingPreventsBundleCommit`; `TestEventReferencedForeignEvidencePreventsBundleCommit`; `TestReportOnlyEvidenceReferencesMustBelongToBinding`; `TestPublisherRejectsMissingOrMismatchedTerminal`; `TestPublishedArtifactsShareBinding`; `TestFunctionalEventRequiresConfirmedPlanBinding`; `TestReplanBindingTamperAndOmissionFailsProjection` | `localvalidation`, `replanning` | pass |
| R6 | `TestPublishedArtifactsShareBinding` (corrección con nuevo run y bundle anterior intacto); `TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision` | `localvalidation`, `replanning` | pass |
| R7 | `TestSecretCanariesNeverEnterBindingOrPublicDigests`; `TestUnclassifiedLiteralStopsBeforeRunOrTool`; `TestEvidenceBindingIsSealedAndVerified`; `TestRunnerPublishesAttestedRevisionBinding` | `revisionbinding`, `localvalidation`, `evidence` | pass |
| R8 | `TestBindingCancellationAndTamperFailClosed`; `TestProjectionBoundsAndDomainSeparation`; `TestProjectionCancellationStartsNoRun`; `TestPublisherCancellationDuringStagingNeverCommits`; `TestCancellationAfterLastPlanCannotPublishPassedVerdict`; `TestPublisherRejectsEvidenceIdentityMismatchWithTypedError`; `TestPublisherRejectsCorruptEvidenceWithTypedError`; `TestSignalCancellationBeforeAdmissionStartsNoRun`; `TestReplanBindingTamperAndOmissionFailsProjection`; `TestUncertainStartedBatchFailsClosedWithoutToolReplay` | `revisionbinding`, `localvalidation`, `replanning` | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | `Binding`, `PlanBinding`, errores tipados, verificación de digest e identidad. |
| T2 | done | `GitResolver` lee Git local sin fetch/checkout, fija OID completo y rechaza dirty state. |
| T3 | done | `Admit`/`Recheck` y `Runner` exigen atestación confiable, vigente y coincidente antes de ejecutar. |
| T4 | done | `SafeValue` y `HashSafe` separan cuatro dominios versionados y rechazan literales no clasificados. |
| T5 | done | `NewCoordinator` exige guard/base; el plan inicial y cada replan se registran en secuencia; `ExecuteBound` consume grant antes de confirmar lote ejecutable y llamar herramientas. |
| T6 | done | Journal y evidencia sellada portan `BindingSHA256`; recuperación y publisher rechazan mezclas. |
| T7 | done | `run.json`, `events.jsonl`, evidencia, `report.json` y `report.md` incluyen binding seguro; publisher verifica y hace commit no-replace. |
| T8 | done | Flujo con Git temporal, atestador/planificación/proyección fake, aprobación y filesystem local; mapa R→test y gates registrados abajo. |

## Verificación observada

| Comando | Resultado |
|---|---|
| `go test ./... -count=1` | pass |
| `go vet ./...` | pass |
| `go test -race ./...` | pass con GCC portable y CGO |
| `go test -race -count=20 ./internal/replanning ./internal/localvalidation ./internal/harness ./internal/evidence ./internal/planapproval` | pass antes de las correcciones de review; `localvalidation` se repite abajo |
| `go test -race -count=20 ./internal/localvalidation` | pass tras las correcciones de review (222,9 s) |
| `go build ./...` (Windows/Linux/macOS) | pass; cross-build sin CGO |
| `go test -run '^$' ./...`; `go mod verify`; `git diff --check` | pass |
| `go test ./... -coverprofile=coverage_feature14_final`; `go tool cover -func=coverage_feature14_final` | pass; global 89,2 % |

## Cobertura y limitaciones

- Cobertura observada tras la primera review: global 89,2 %, frente al 89,6 % de feature 13; baja 0,4 puntos porcentuales. `revisionbinding` 88,9 %; `replanning` 85,2 %; `localvalidation` 86,6 %; `harness` 88,3 %; `evidence` 89,6 %; `planapproval` 86,8 %. Todos los paquetes modificados superan 85 %. Se añadieron pruebas conductuales del Git temporal con atestación, de deriva de revisión/configuración, secuencia de PlanBinding, grant obsoleto, evidencia anidada/ajena/corrupta, entorno Git hostil, terminal inconsistente y cancelación en publicación. La nueva superficie de fallos de Git, IO y callbacks recuperables aumenta el denominador; los errores de infraestructura difíciles de inducir sin duplicar la lógica dejan statements no ejecutados. El reviewer debe evaluar esta justificación de descenso global; no se afirma aprobación del criterio.
- El comando de producción falla cerrado porque esta feature define el contrato consumidor, pero no configura un proveedor real de atestación preview ni un clasificador completo de literales; el adapter preview pertenece a feature 19. Los tests usan fronteras falsas y repositorios Git locales sin remoto.
- `gofmt -l .` sigue listando 135 archivos preexistentes fuera del cambio; los Go introducidos o modificados por esta feature pasan `gofmt` dirigido. El gate literal global no está limpio y necesita evaluación del leader/reviewer contra la línea base anterior.
