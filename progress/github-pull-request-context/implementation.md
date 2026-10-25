# Implementación de github-pull-request-context

## Resultado

La feature 30 incorpora una adquisición read-only, coherente, acotada e
idempotente del contexto de una pull request autorizada. El nuevo paquete
`internal/githubcontext` no conoce credenciales ni transporte: consume un puerto
neutral, sella `PullRequestContext/v1`, persiste objetos inmutables y receipts
content-free, produce `SourceDiff` con trust `untrusted_data` y permite reconstruir
la observación actual durante la revalidación.

`internal/githubpr.RESTAdapter` implementa el puerto con el token de instalación
existente y permisos exactos `metadata:read` + `pull_requests:read`. La composición
productiva resuelve policy, gateway, secret gate, clock y store; ninguna de esas
capacidades cruza al harness, assembler o modelo. No se han añadido webhooks,
checks, comentarios, checkout, entornos ni configuración de proyecto.

## Decisiones materiales

- La lectura coherente es `metadata -> files paginados -> metadata`; cualquier
  drift descarta el intento completo antes de normalizar o persistir.
- Cada respuesta REST recibe el `MaxResponseBytes` de la policy antes de ser
  decodificada. Las rutas de página se construyen localmente y nunca se siguen
  URLs incluidas en la respuesta.
- Los límites de archivos, páginas, patch, hunks, líneas, constraints e items
  generan omisiones tipadas y digests content-free. `MaxItems` limita la
  proyección a un item de metadata/manifest más los archivos admitidos.
- Constraints y referencias de contratos viajan dentro del productor neutral
  `SourceDiff`; no se crea un segundo productor `SourceConstraints` que compita
  con la fuente autoritativa de la especificación.
- Los contratos afectados se derivan solo de rutas exactas configuradas y paths
  allowlisted observados. Rename/delete y patch ausente se representan sin
  descargar ni inventar un body.
- `Acquire` usa receipts idempotentes; `Refresh` omite el receipt histórico y
  repite la observación remota con los mismos budgets para los checkpoints.
- En Windows se confía en flush del candidato y publicación por hard-link; en
  Unix se sincroniza además el directorio antes de publicar el receipt.
- El review 1 sustituyó el observer no-op por `LocalRecordObserver`: el primer
  record terminal confirmado gana mediante publicación inmutable, la redelivery
  converge tras pérdida de respuesta y nunca persiste title/body/path/patch.
- `strictNextLink` usa el target solo como prueba estructural: exige el mismo
  origen, endpoint/repo/PR y la página siguiente exacta, pero la request real se
  sigue construyendo localmente.
- El body remoto se examina completo dentro del response budget para extraer
  unidades completas; si `MaxBodyBytes` excluye una fuente configurada, el
  resultado es `insufficient` y no una constraint truncada.
- Context, Binding, records y relaciones del store se validan semánticamente
  antes de aceptar un digest, incluido rechazo recursivo de claves JSON
  duplicadas. Cancelación y deadline conservan causa y categorías distintas en
  cada frontera durable y HTTP.

## Remediación del review 1

| Hallazgo | Cierre y evidencia |
|---|---|
| REV-1 | Observer durable exactly-once y content-free: `TestLocalRecordObserverIsDurableExactlyOnceAndContentFree`, `TestTerminalRecordsPreserveFailureOutcomes`, `TestDurableBoundaryValidationMatrix`. |
| REV-2 | Link ligado a origen/ruta/repo/página: `TestStrictContextPaginationBindsOriginRouteAndPage`, `TestContextFilesConsumesOnlyLocallyExpectedPages`. |
| REV-3 | No se extrae desde bytes truncados: `TestConstraintExtractionNeverPublishesByteTruncatedUnits`. |
| REV-4 | Policy/catálogo/extensiones/aliases/refs estrictos y canónicos: `TestPolicyCatalogAndExtensionsAreCanonical`. |
| REV-5 | Verificación semántica y relacional aun con digest recalculado: `TestSelfSignedContextAndBindingRequireSemanticValidity`, `TestDurableStorageCorruptionMatrix`. |
| REV-6 | Cause, cancel y timeout preservados en store/token/HTTP/paginación: `TestStoreCancellationWinsOverPersistenceWrapping`, `TestContextBoundariesPreserveCancelAndTimeout`. |

## Remediación del review 2

| Hallazgo | Cierre y evidencia |
|---|---|
| REV-1 | `acquisition_digest` y terminal ID recomputable separan outcomes estables, saneando input inválido; los fallos de observer se propagan y stale→success/redelivery/response-loss convergen: `TestTerminalIdentityHandlesInvalidFailureAndLaterSuccess`, `TestLocalRecordObserverIsDurableExactlyOnceAndContentFree`, `TestDurableBoundaryValidationMatrix`. |
| REV-7 | Parser y verificador común de rangos unified diff exige headers canónicos, coordenadas sin overflow y conteos old/new exactos para cada hunk: `TestUnifiedDiffRangesRequireCompleteDeclaredUnits`. |
| REV-8 | `CanonicalRepositoryPath` comparte gramática entre dominio y adapter y rechaza controles, segmentos vacíos/trailing y devices reservados: `TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments`, `TestGitHubChangedPathsUseSharedCanonicalGrammar`. |

## Remediación del review 3

| Hallazgo | Cierre y evidencia |
|---|---|
| REV-9 | Fallos generic/cancel/timeout del observer conservan Context+Binding confirmados y su cause; redelivery usa el receipt sin nuevas lecturas GitHub: `TestObserverFailuresPreserveConfirmedBindingAndRedeliver`, `TestLocalRecordObserverIsDurableExactlyOnceAndContentFree`. |
| REV-10 | `Record.ID` es key terminal estable y `Record.Digest` cubre canónicamente todos los campos, incluidos attempts/duration; ambos se verifican en publish/read/redelivery: `TestDurableBoundaryValidationMatrix`. |
| REV-11 | Hunk conserva rangos old/new half-open y la secuencia rechaza overlap, duplicado, retroceso y puntos zero-count ambiguos, incluso self-signed: `TestUnifiedDiffRangesRequireCompleteDeclaredUnits`. |
| REV-12 | UTF-8 se valida antes de canonicalizar paths y antes de decodificar respuestas para title/body/filename/patch: `TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments`, `TestContextResponsesRejectInvalidUTF8BeforeJSONDecode`. |

## Requirements y tests

| Requirement | Tests concretos | Resultado |
|---|---|---|
| R1 | `TestAcquireRejectsDriftUnsafeFilesSecretsAndTampering`, `TestWorkflowGitHubContextCompositionRequiresSealedReadOnlyAuthority`, `TestPolicyCatalogAndExtensionsAreCanonical` | pass |
| R2 | `TestAcquireBuildsBoundedCanonicalContext`, `TestRevalidateDetectsCurrentObservationDrift`, `TestProviderIntegrityAndParsingEdges`, `TestSelfSignedContextAndBindingRequireSemanticValidity` | pass |
| R3 | `TestAcquireBuildsBoundedCanonicalContext`, `TestStrictPolicyAndExtractorEdges`, `TestLocalStoreFailsClosedForCorruptionAndCancellation`, `TestSelfSignedContextAndBindingRequireSemanticValidity`, `TestDurableBoundaryValidationMatrix`, `TestContextResponsesRejectInvalidUTF8BeforeJSONDecode` | pass |
| R4 | `TestAcquireRejectsDriftUnsafeFilesSecretsAndTampering`, `TestAcquisitionLimitAndOmissionMatrix` | pass |
| R5 | `TestBudgetsOmissionsCatalogAndContracts`, `TestStrictPolicyAndExtractorEdges`, `TestProviderIntegrityAndParsingEdges`, `TestConstraintExtractionNeverPublishesByteTruncatedUnits` | pass |
| R6 | `TestBudgetsOmissionsCatalogAndContracts`, `TestAcquisitionLimitAndOmissionMatrix`, `TestConstraintExtractionNeverPublishesByteTruncatedUnits`, `TestUnifiedDiffRangesRequireCompleteDeclaredUnits` (incluye relaciones multi-hunk y zero-count) | pass |
| R7 | `TestAcquireRejectsDriftUnsafeFilesSecretsAndTampering`, `TestAcquisitionLimitAndOmissionMatrix`, `TestProviderIntegrityAndParsingEdges`, `TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments`, `TestGitHubChangedPathsUseSharedCanonicalGrammar`, `TestContextResponsesRejectInvalidUTF8BeforeJSONDecode` | pass |
| R8 | `TestAcquireBuildsBoundedCanonicalContext`, `TestAcquisitionLimitAndOmissionMatrix` | pass |
| R9 | `TestAcquireBuildsBoundedCanonicalContext`, `TestGitHubContextAdapterUsesInitialReceiptsAndFreshCurrentAuthority` | pass |
| R10 | `TestRevalidateDetectsCurrentObservationDrift`, `TestGitHubContextAdapterUsesInitialReceiptsAndFreshCurrentAuthority`, `TestSelfSignedContextAndBindingRequireSemanticValidity` | pass |
| R11 | `TestCanaryGateAndCompositionFailures`, `TestRESTContextReadsOnlyBoundedFieldsAndPermissions` | pass |
| R12 | `TestRetryCancellationConflictAndConcurrency`, `TestRESTContextRejectsPaginationAnomaliesAndMapsRateLimits`, `TestStrictContextPaginationBindsOriginRouteAndPage` | pass |
| R13 | `TestRetryCancellationConflictAndConcurrency`, `TestLocalStoreConcurrentPublicationConverges`, `TestWorkflowGitHubContextCompositionRequiresSealedReadOnlyAuthority`, `TestStoreCancellationWinsOverPersistenceWrapping` | pass |
| R14 | `TestContextErrorMappingCoversClosedTaxonomy`, `TestCanaryGateAndCompositionFailures`, `TestLocalRecordObserverIsDurableExactlyOnceAndContentFree`, `TestTerminalRecordsPreserveFailureOutcomes`, `TestTerminalIdentityHandlesInvalidFailureAndLaterSuccess`, `TestObserverFailuresPreserveConfirmedBindingAndRedeliver`, `TestDurableBoundaryValidationMatrix` | pass |
| R15 | `TestUnifiedDiffRangesRequireCompleteDeclaredUnits`, `TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments`, `TestContextResponsesRejectInvalidUTF8BeforeJSONDecode`, suites focales/globales normal y race, runtime Linux, coverage del diff y cross-builds documentados abajo | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | modelo, enums, deep-copy, JSON estricto y digests en `model.go` |
| T2 | done | policy sellada, extractores, allowlists, rutas y budgets validados |
| T3 | done | puerto neutral y adapter REST con DTOs privados y scope read-only |
| T4 | done | observación metadata/files/metadata y pruebas de drift |
| T5 | done | paths/status/counts/tipos/rename/delete y omisiones |
| T6 | done | parser de unified diff y packer de unidades completas |
| T7 | done | `markdown_section/v1` y `catalog_reference/v1` |
| T8 | done | selector exacto de contratos con refs seguras |
| T9 | done | secret gate antes de store y producer |
| T10 | done | attempts, duration, rate-limit, retry y cancelación |
| T11 | done | MemoryStore/LocalStore, receipts, reopen, corrupción y contenders |
| T12 | done | productor neutral `SourceDiff` con trust y completeness |
| T13 | done | composición workflow y autoridad GitHub reutilizada |
| T14 | done | binding y reconstrucción de producer actual tras restart |
| T15 | done | terminal record content-free, matrices de concurrencia y race |
| T16 | done | trazabilidad, coverage Windows+Linux, cuatro runtimes Linux y cross-builds completos |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| `gofmt -l <archivos afectados>` / `git diff --check 4d0a0da` | pass final |
| `go vet ./...` | pass |
| `go test ./... -run '^$' -count=1 -timeout 10m` | pass |
| `go test ./internal/githubcontext ./internal/githubpr ./internal/validationworkflow ./cmd/featureproof -count=3 -timeout 10m` | pass |
| `go test -p 1 ./... -count=1 -timeout 10m` | pass |
| `go test -race ./internal/githubcontext ./internal/githubpr ./internal/validationworkflow ./cmd/featureproof -count=1 -timeout 15m` | pass |
| `go test -race -p 1 ./... -count=1 -timeout 15m` con UCRT64 | pass |
| focal race posterior de `githubcontext` y `githubpr` | pass |
| cuatro profiles Windows con `-coverpkg` sobre los paquetes afectados y profile Linux real de `githubcontext` | pass |
| `GOOS={windows,linux,darwin} GOARCH=amd64 CGO_ENABLED=0 go test -c` para cuatro paquetes | pass, 12/12 |
| cuatro binarios Linux `CGO_ENABLED=0` ejecutados en Alpine 3.22.2 / WSL2 `FeatureProof-Test` desde cwd compatible | pass, 4/4 |

Todos los tests de GitHub usan `httptest`, fakes y stores locales. Ningún
`go test` resolvió credenciales reales ni llamó a GitHub, un modelo o red externa.

## Coverage exacta del diff productivo

Base aprobada: `4d0a0da`. Se unieron por coordenada y máximo count cuatro
profiles Windows frescos (`githubcontext`, `githubpr`, `validationworkflow` y
`cmd/featureproof`) y un profile Linux real de `githubcontext`; después se
intersectaron con statements productivos nuevos o modificados, incluyendo ambos
build tags de sync.

- Resultado exacto multiplataforma tras review 3: **1009/1183 = 85,291631 %**.
- Umbral sin redondear: 1005,55 statements; 1009 cubiertos.
- `sync_unix.go`: **4/5**; `sync_windows.go`: **1/1**.

## Riesgos y limitaciones

- Los objetos content-addressed huérfanos se retienen; una GC futura debe partir
  de un snapshot estable de receipts. No se borra un objeto compartido durante
  contención o recovery.
- La policy se recibe de composición local aprobada. La administración
  versionada de proyecto sigue fuera de alcance hasta feature 36.
- La adquisición no publica checks/comentarios ni registra webhooks; esa
  autoridad continúa exclusivamente en feature 29.

