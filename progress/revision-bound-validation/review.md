# Review independiente — revision-bound-validation

## Veredicto

**approved.** R1–R8 tienen implementación y tests conductuales; T1–T8 están marcadas `[x]`. Los hallazgos detectados durante esta review se corrigieron y verificaron antes de este veredicto. La feature 14 permanece `in_progress` para que el leader efectúe la transición.

## Trazabilidad comprobada

| Requirement | Evidencia de test real | Resultado |
|---|---|---|
| R1 | `TestResolveFullCommitAndFreezeMovedRef`, `TestRejectMissingAmbiguousOrForeignCommit`, `TestGitResolverIgnoresHostileGitEnvironment`, `TestRunnerWithLocalGitAndTrustedAttestor` | pass |
| R2 | `TestDirtyWorktreeNeverAttributedToHEAD`, `TestAdmissionStopsAfterDirtyOrMalformedSource`, `TestGitResolverIgnoresHostileGitEnvironment` | pass |
| R3 | `TestEnvironmentMustAttestExactRepositoryAndCommit`, `TestAbsentOrUntrustedAttestationStopsExecution`, `TestRevisionOrConfigurationDriftStopsBeforeTool` | pass |
| R4 | `TestBindingHashesAreStableAndSensitiveToSemanticChanges`, `TestStaleApprovalCannotBindChangedPlan`, `TestStaleGrantCannotConfirmExecutableBatch` | pass |
| R5 | `TestEventsAndEvidenceShareBinding`, `TestReportOnlyEvidenceReferencesMustBelongToBinding`, `TestPublisherRejectsMissingOrMismatchedTerminal`, `TestPublisherRejectsCorruptEvidenceWithTypedError` | pass |
| R6 | `TestPublishedArtifactsShareBinding`, `TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision`, `TestPublisherRejectsCollisionWithoutOverwrite` | pass |
| R7 | `TestSecretCanariesNeverEnterBindingOrPublicDigests`, `TestUnclassifiedLiteralStopsBeforeRunOrTool`, `TestRunnerPublishesAttestedRevisionBinding` | pass |
| R8 | `TestBindingCancellationAndTamperFailClosed`, `TestPublisherCancellationDuringStagingNeverCommits`, `TestCancellationAfterLastPlanCannotPublishPassedVerdict`, `TestReplanBindingTamperAndOmissionFailsProjection` | pass |

Los tests usan repositorios Git temporales, servidor local y fronteras falsas de atestación, aprobación, reloj y filesystem. No requieren remoto Git, proveedor preview ni credenciales reales.

## Hallazgos de la review, cerrados

### REV-1 — Referencias de evidencia del reporte sin verificar

- Severidad: high. Regla: R5/R8. Archivo: `internal/localvalidation/publisher.go`.
- Evidencia inicial: el publisher recorría referencias de constraints y eventos, pero no las de pasos, evidencia terminal ni assertions anidadas de `report.json`.
- Corrección verificada: ahora comprueba cada referencia y su ID, run y binding antes del commit; `TestReportOnlyEvidenceReferencesMustBelongToBinding` cubre referencias ausentes y ajenas en las cuatro ubicaciones.

### REV-2 — Variables Git heredadas podían cambiar el repositorio observado

- Severidad: high. Regla: R1/R2. Archivo: `internal/revisionbinding/git.go`.
- Evidencia inicial: los subprocesos Git heredaban `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` y variables de objetos/configuración, capaces de desviar resolución o dirty preflight.
- Corrección verificada: `gitEnvironment` excluye las variables `GIT_*` heredadas y fija solo las permitidas; `TestGitResolverIgnoresHostileGitEnvironment` prueba repo, object store, índice y configuración hostiles.

### REV-3 — Integridad terminal y cancelación al publicar

- Severidad: high. Regla: R5/R8. Archivos: `internal/localvalidation/bound.go`, `publisher.go`, `runner.go`.
- Evidencia inicial: un snapshot `passed` podía no concordar con el evento terminal; una cancelación tras el último plan podía omitir el recheck final, y el publisher no observaba el contexto entre escrituras.
- Corrección verificada: cierre terminal único y consistente, clasificación de la cancelación después del último plan, y checks de contexto antes de cada artefacto y commit. Lo cubren `TestPublisherRejectsMissingOrMismatchedTerminal`, `TestCancellationAfterLastPlanCannotPublishPassedVerdict` y `TestPublisherCancellationDuringStagingNeverCommits`.

### REV-4 — Error de evidencia persistida sin categoría de integridad

- Severidad: medium. Regla: R5/R8. Archivo: `internal/localvalidation/publisher.go`.
- Evidencia inicial: un record con ID/run ajeno o un `Get` fallido producía error genérico, aunque el estado persistido era inverificable.
- Corrección verificada: devuelve `binding_integrity` tipado, salvo cancelación propagada; `TestPublisherRejectsEvidenceIdentityMismatchWithTypedError` y `TestPublisherRejectsCorruptEvidenceWithTypedError` pasan.

## Gates y cobertura

| Comprobación | Resultado final |
|---|---|
| `go vet ./...`, `go test -run '^$' ./...`, `go test ./...` | pass |
| `go test -race ./...` con CGO y GCC portable | pass tras correcciones |
| `go test -race -count=20 ./internal/localvalidation` | pass tras correcciones; 222,9 s |
| `go test -coverprofile <perfil temporal> ./...`; `go tool cover -func <perfil temporal>` | pass; **89,2 %** global (9.337/10.465 statements) |
| Builds Windows/Linux/macOS, `go mod verify`, `git diff --check` | pass según informe final de implementación; `git diff --check` repetido en esta review |
| `gofmt -l` de los 48 archivos Go modificados o nuevos | cero rutas |
| `gofmt -l .` literal | **no pasa**: 135 archivos preexistentes fuera del cambio; el mismo tipo de deuda se documentó en la review aprobada de feature 13 |

**Apruebo expresamente la justificación de cobertura exigida por `docs/verification.md`.** El global baja de 89,6 % a 89,2 % (0,4 puntos): el nuevo código añade rutas de error de Git, atestación, proyección, journal, evidencia y publicación al denominador. Los paquetes afectados quedan al menos en 85,2 % (`replanning`), 86,6–86,7 % (`localvalidation`, según perfil), 86,8 % (`planapproval`), 88,3 % (`harness`), 88,9 % (`revisionbinding`) y 89,6 % (`evidence`); las ramas relevantes de fallo cerrado, grant, deriva, secretos, cancelación e integridad tienen pruebas de comportamiento. Los errores de infraestructura poco inducibles explican el resto sin justificar tests artificiales. La caída no oculta un requirement sin test.

La salida global de `gofmt` es deuda anterior fuera de los 48 Go de esta feature y de los cambios previos de feature 13 preservados en el working tree. No la presento como gate global satisfecho ni la atribuyo a feature 14. El contrato de admisión falla cerrado en la CLI de producción mientras no exista fuente preview y proyector seguro configurados; la spec aprobada asigna el adapter preview a feature 19. Los tests positivos demuestran la composición mediante puertos confiables inyectados.
