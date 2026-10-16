# Validación vinculada a una revisión — Tasks

Cada task sigue el ciclo prueba que falla, implementación mínima, refactorización y verificación. No se toca código antes de aprobación humana de la spec.

- [x] T1. Definir el modelo inmutable de binding y sus errores tipados. [R1, R3, R4, R8]
  - Escribir `TestBindingRejectsIncompleteAndMalformedIdentity` y `TestBindingCancellationAndTamperFailClosed`.
  - Implementar versiones, invariantes, límites de entrada y comparación exacta de identidades; conservar context cancellation.

- [x] T2. Resolver revisión Git local con identidad canónica y dirty preflight. [R1, R2, R8]
  - Escribir `TestResolveFullCommitAndFreezeMovedRef`, `TestRejectMissingAmbiguousOrForeignCommit` y `TestDirtyWorktreeNeverAttributedToHEAD` con repositorios temporales.
  - Implementar adapter de lectura, sin fetch/checkout ni publicación de paths; distinguir ref inválido, objeto no-commit y modificaciones tracked/staged/untracked.

- [x] T3. Incorporar atestación de entorno al caso de uso de admisión. [R1, R3, R8]
  - Escribir `TestEnvironmentMustAttestExactRepositoryAndCommit` y `TestAbsentOrUntrustedAttestationStopsExecution` con fake confiable y fake ausente.
  - Validar procedencia/vigencia e identidad de entorno; impedir generación de plan o tool work hasta que coincida el commit desplegado.

- [x] T4. Hashear las cuatro proyecciones semánticas seguras. [R4, R7, R8]
  - Escribir `TestBindingHashesAreStableAndSensitiveToSemanticChanges` y `TestSecretCanariesNeverEnterBindingOrPublicDigests`.
  - Implementar serialización canónica versionada y acotada de constraints, plan preparado, contrato relevante y configuración no secreta; excluir secret bytes antes de hashear y rechazar inputs cuya semántica segura no pueda representarse.

- [x] T5. Integrar aprobación y lotes de replanificación sin mutar la revisión. [R4, R5, R6]
  - Escribir `TestStaleApprovalCannotBindChangedPlan` y `TestReplanAddsOrderedPlanDigestWithoutChangingBaseRevision`.
  - Confirmar cada digest de lote preparado únicamente contra autorización vigente cuando se requiere; verificar contexto/revisión antes de cada nuevo lote y conservar lineage existente.

- [x] T6. Propagar y verificar binding en journal y evidencia. [R5, R7, R8]
  - Escribir `TestEventsAndEvidenceShareBinding` y `TestForeignBindingPreventsBundleCommit`.
  - Añadir referencias a metadatos sellados, validar al recuperar y reconstruir eventos confirmados sin aceptar mezcla de runs o bindings.

- [x] T7. Extender artefactos de run y reportes, y conservar publicación atómica. [R5, R6, R7]
  - Escribir `TestPublishedArtifactsShareBinding`, `TestCorrectionCreatesNewRunWithoutChangingPriorBundle` y un escaneo de canarios de staging y final.
  - Publicar identidad/hashes seguros en `run.json`, `events.jsonl`, evidencia, `report.json` y `report.md`; verificar todas las referencias antes de commit no-replace.

- [x] T8. Completar integración y revisión de trazabilidad. [R1, R2, R3, R4, R5, R6, R7, R8]
  - Ejecutar flujo completo con repositorio temporal, fake de entorno/approval/clock/filesystem; cubrir revisión válida, commit inexistente, dirty tree, discrepancia, cambio de configuración y hash estable.
  - Registrar mapa R→test y resultados en `progress/revision-bound-validation/implementation.md`; ejecutar gates de `docs/verification.md`, race y builds sin red/credenciales reales.
