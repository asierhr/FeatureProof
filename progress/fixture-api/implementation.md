# Feature 26 — Fixture API implementation

## Estado de entrega

`ready_for_review`. Las nueve revisiones rechazadas quedaron remediadas de REV-1 a REV-37. El alcance sigue limitado a fixtures HTTP declarativos de feature 26; no incluye feature 27.

## Implementación final

- Specification/manifest v3: unión interna exclusiva, literales JSON finitos, tipos capturables cerrados sin `null`, compatibilidad estricta v1/v2 y consumidores de runtime/package compatibles con v3.
- Binding: el digest canónico liga run, target, repositorio, revisión, órdenes, declarations completas, operaciones, catálogo, identities, statuses, captures e inputs no secretos. Se recomputa en cada delivery mediante `ReducedAuthority` y se valida antes de state o I/O.
- Estado privado: valida claves, transiciones, attempts, terminales, claims y captures. Cada capture autentica run/fixture/name/type/value digest/bytes/create binding y declaración exacta.
- Prepare/recovery: el intent y la elegibilidad conservadora quedan durables antes del create. Un intent pendiente tras crash o fallo post-I/O vuelve como `uncertain`, nunca repite create y conserva cleanup. Los no iniciados se proyectan en orden como `blocked` con causa cerrada.
- Cleanup: scope finito propio, orden inverso, retries/calls/deadline propios, claim/lease CAS durable antes de cada efecto, revalidación tras claim y antes de red, terminal durable por obligación y report global solo después de cerrar todas.
- Workflow: un error no confirmado de cleanup mantiene `CleanupPending`, desired outcome y material privado; redelivery/`Recover` retoma cleanup sin repetir primary. El primary result no se modifica.
- Composición productiva: `cmd/featureproof/workflow.go` construye store, lifecycle, HTTP controlado, late identities, evidence, journal, scopes y authority; inyecta `Fixtures` en el único `validationworkflow.Service` compartido por API, CLI, MCP y GitHub.
- Seguridad: fixtures solo proceden de specification/configuración confiable; el harness/model no recibe declaration, captures, credential refs, secrets, URL libre, shell, SQL ni idempotency keys. Las credenciales se resuelven y liberan por intento.

## Remediación de revisión

| Hallazgo | Evidencia de cierre |
|---|---|
| REV-1 | `composeWorkflowFixtures`; `TestProductFixtureLifecycleParityAcrossAllSurfaces` atraviesa composición real en API/CLI/MCP/GitHub con create→primary→cleanup. |
| REV-2 | `TestPostIOPersistenceCrashRecoversAsCleanupOnly` demuestra intent durable, cero replay de create y cleanup conservador tras fallo post-I/O. |
| REV-3 | `TestFixtureCleanupPersistenceFailureStaysPendingAndRecovers` prueba cleanup pending, preservación del primary y convergencia por `Recover`. |
| REV-4 | `TestBoundSealAndCaptureAuthenticationRejectFieldTampering` altera campo por campo authority/capture y todo se rechaza antes de I/O. |
| REV-5 | `TestCleanupClaimSerializesOwnersAndExpiredLeaseHandsOff` usa barreras sin sleeps para dos owners y handoff tras lease expiry. |
| REV-6 | `TestCleanupExhaustionTerminalsAreDurableAndRedeliveryIsNoOp` cubre budget/deadline/cancel/policy denial y redelivery sin efectos. |
| REV-7 | `TestPreparationFailureStartsCleanupWithoutValidationPlans` verifica fail-fast y `blocked` estable con causas cerradas. |
| REV-8 | `TestFixtureValueModelRejectsAmbiguousNonFiniteAndNullReferenceTypes`, `TestV3RejectsNonFiniteFixtureLiteral` y clone error tests. |
| REV-9 | Crash post-I/O, owners/lease, report persist, cuatro superficies, redelivery, canarios y `TestProductFixtureRetryAndCleanupResolveRotatedCredentials`. |

## Trazabilidad R → tests

| Requirement | Tests principales |
|---|---|
| R1 | `TestV3LoadsStrictFixtureDeclarations`; `TestV3RejectsUnknownDuplicateAndInvalidFixtureFields`; `TestFixtureValueModelRejectsAmbiguousNonFiniteAndNullReferenceTypes`; `TestV3RejectsNonFiniteFixtureLiteral`; `TestV1V2CompatibilityRemainsFixtureFree` |
| R2 | `TestFixtureOperationsRequireExactApprovedContractBindings`; `TestReducedAuthorityRebindsEveryDelivery`; `TestDerivedCatalogAndIdentityDriftCannotAuthorizeFixtures`; vertical productiva de cuatro superficies |
| R3 | `TestFixtureGraphProducesStableCreateAndReverseCleanupOrder`; `TestFixtureGraphRejectsCyclesAndImplicitReferenceDependencies`; tampering del sello |
| R4 | `TestPreparationUsesConfirmedCapturesAndBlocksDependentWork`; `TestPreparationFailureStartsCleanupWithoutValidationPlans`; crash post-I/O |
| R5 | `TestCapturesAreTypedBoundedSanitizedAndDurable`; tampering de capture; tipos/clone estrictos |
| R6 | `TestHTTPAdapterUsesControlledAuthenticatedTool`; `TestProductFixtureRetryAndCleanupResolveRotatedCredentials`; vertical productiva |
| R7 | `TestPostIOPersistenceCrashRecoversAsCleanupOnly`; `TestRecoverySkipsConfirmedCreateAndRejectsDigestDrift`; `TestFileStoreCASCorruptionAndCancellation` |
| R8 | `TestCleanupRunsInReverseAfterEveryPrimaryOutcome`; terminales durables; workflow cleanup pending/recovery |
| R9 | carrera de owners/handoff; `TestCleanupExhaustionTerminalsAreDurableAndRedeliveryIsNoOp`; retry cleanup |
| R10 | tampering sello/state/capture; `TestFixtureRetryRequiresStableKeyAndRevalidation`; `TestProductRevalidatorRejectsDriftAndCancellation` |
| R11 | `TestFixtureCleanupPersistenceFailureStaysPendingAndRecovers`; `TestCleanupReportPersistenceFailureRedeliversWithoutRepeatingDelete`; primary outcome matrix existente |
| R12 | `TestProductFixtureLifecycleParityAcrossAllSurfaces`; `TestFixtureCanariesNeverCrossExternalProjections`; `TestProductPortsPersistOnlySanitizedEvidenceAndEvents` |
| R13 | carrera dos owners, lease expiry/handoff, crash/redelivery, report persist y cuatro superficies productivas |

## Gates ejecutados

- `gofmt` en todos los Go modificados: limpio.
- `go vet ./internal/fixture ./internal/specification/... ./internal/validationworkflow ./internal/localvalidation ./internal/planmanifest ./cmd/featureproof`: correcto.
- `go test -run '^$' ./...`: correcto.
- `go test -count=10 ./internal/fixture ./internal/specification/...`: correcto.
- `go test -count=5 ./internal/validationworkflow`: correcto.
- Vertical real repetida tres veces: `TestProductFixtureLifecycleParityAcrossAllSurfaces` y `TestProductFixtureRetryAndCleanupResolveRotatedCredentials`: correcto.
- `go test -race -count=1 ./internal/fixture ./internal/specification/... ./internal/validationworkflow` con UCRT64/CGO: correcto.
- `go test -count=1 ./...`: correcto; el subtest de symlink se omite explícitamente en Windows cuando el SO exige privilegio opcional.
- Cross-build `go test -c`: Linux/amd64 y Darwin/amd64 para fixture, validationworkflow y cmd/featureproof: correcto.
- Coverage: `internal/fixture` 85,5 %; `internal/validationworkflow` 85,7 %.
- `git diff --check`: correcto, solo avisos de normalización CRLF del checkout.
- Árbol: sin artefactos temporales de coverage/build dentro del repositorio.

## Limitaciones reales

- No se contactan servicios externos: el contrato productivo HTTP, identities y cuatro superficies se prueban contra `httptest` controlado y transporte de modelo controlado.
- En Windows sin `SeCreateSymbolicLinkPrivilege`, el único caso que necesita crear un symlink se omite; las defensas de lectura de symlink siguen cubiertas donde el SO permite construir el fixture.

## Segunda remediación independiente (REV-10..REV-17)

- REV-10 / R8,R10,R13: `CleanupClaim.Token` es único por invocación; `CleanupClaimResult` distingue convergencia terminal; el lifecycle recarga y valida token/owner/expiry inmediatamente antes de red. `TestCleanupClaimSerializesOwnersAndExpiredLeaseHandsOff` cubre owners distintos, mismo owner lógico y handoff sin sleeps.
- REV-11 / R10: `ContractIdempotencyPolicy` solo reconoce semántica HTTP, constraint contractual verificable o parámetro `Idempotency-Key` de tipo string en ubicación `header`; la allowlist runtime únicamente limita el envío.
- REV-12 / R2,R4,R9,R10: `ProductRevalidator` usa el policy factory real y un `harness.Run` con reserva atómica antes de cada efecto. `TestProductRevalidatorEvaluatesPolicyAndReservesExactBudget` demuestra denial/exhaustion sin reserva posterior.
- REV-13 / R9,R13: `FiniteScopes.Parent` deriva cada cleanup del shutdown scope provisto por composición; no existe `context.Background` oculto.
- REV-14 / R7,R11,R13: `SnapshotDigest` autentica todos los campos semánticos durables y `ValidateState` enlaza terminales con attempts. La matriz de `TestBoundSealAndCaptureAuthenticationRejectFieldTampering` incluye mutaciones estructuralmente válidas.
- REV-15 / R5,R12: composición convierte cada capture path a selector persistido y rechaza cualquier solape sensible antes de lifecycle/I/O.
- REV-16 / R11,R12: cleanup proyecta audit allowlisted de intents/attempts/terminales; `AttachFixtureAudit` lo incorpora a `package.json` y `fixtures.json`, regenera `manifest.json`, y `ReadPackage` verifica igualdad y digest. `TestFixtureAuditIsSealedAndOfflineVerified` prueba lectura offline y tampering.
- REV-17 / R1: `validFixtureJSON` rechaza `json.Number` NaN, +Inf, -Inf y overflow; la matriz está en `TestFixtureValueModelRejectsAmbiguousNonFiniteAndNullReferenceTypes`.

## Gates de segunda remediación

- `go vet` focal: correcto.
- `go test -run '^$' ./...`: correcto.
- `go test ./... -count=1`: correcto.
- Focales fixture/specification/localvalidation/validationworkflow/cmd: correctos.
- Race con `CGO_ENABLED=1`, `CC=gcc` y UCRT64: correcto para fixture, specification, validationworkflow y cmd.
- Coverage: `internal/fixture` 85,0 %; `internal/validationworkflow` 85,4 %.
- Cross-build compile-only Linux/amd64 y Darwin/amd64: correcto para fixture y validationworkflow.
- `git diff --check`: correcto tras normalizar finales de archivo.
- No quedan artefactos temporales de coverage/test en el repositorio y `feature_list.json` no fue modificado.

## Tercera remediación independiente (REV-18..REV-24)

- REV-18: `BoundOperation.IdempotencyHeader` queda dentro del sello canónico y fluye a `HTTPRequest`; `HTTPAdapter` exige coincidencia con la allowlist runtime e inyecta únicamente en esa operación. La vertical productiva afirma key presente en POST y ausente en DELETE.
- REV-19: `RequestRevalidator` recibe el request resuelto completo con identity/binding. `ProductRevalidator` mantiene budgets separados por run y reserva retry+tool call atómicamente; `FixtureState.Reservations` confirma el ordinal antes de policy/I/O para recovery.
- REV-20: el claim cubre deadline más gracia, el terminal valida token/owner/expiry y `activeCleanupClaims` impide handoff dentro del proceso mientras el executor está activo. La carrera prueba same-owner, different-owner y reloj posterior a expiry con una barrera sin sleeps bajo race.
- REV-21: `FileStore` crea/reutiliza mediante O_EXCL una key consumer-owned y autentica el snapshot completo con HMAC además del digest. `ValidateState` exige categorías cerradas, ordinals únicos/monótonos, terminal→attempt, reservations y outbox válido. Tests rechazan success falso resellado, intent/flags mutados, attempts duplicados y categoría canario.
- REV-22: `ValidateSummary` liga run/digest y valida enums, intent/attempt/terminal y status agregado. `AttachFixtureAudit` actualiza `package.json`, `fixtures.json`, `report.json` y `report.md` antes de regenerar manifest; el reader verifica las copias y semántica. La prueba resella ambas copias/report/manifest con historia falsa y falla cerrado.
- REV-23: cada capture emite `capture_confirmed` con name/type/bytes/digest. `FixtureState.PendingEvents` funciona como outbox durable; recovery confirma eventos idempotentes y terminales usan un scope finito aun tras cancelación. `TestDurableOutboxRecoversJournalFailureAndPublishesCaptureMetadata` prueba el gap state/journal y su convergencia.
- REV-24: retirados perfiles temporales `$cov`, `$fixtureProfile`, `$localProfile`, `$workflowProfile`, `fixture_cov` y `workflow_cov`; el test histórico modificado tiene comentario sistémico.

### Gates de tercera remediación

- `go vet` focal y `go test -run '^$' ./...`: correctos.
- `go test ./... -count=1`: correcto.
- Race UCRT64/CGO para fixture, specification, localvalidation, validationworkflow y cmd: correcto.
- Coverage: fixture 85,1 %; validationworkflow 85,4 %.
- Cross-build compile-only Linux/amd64 y Darwin/amd64 para fixture, validationworkflow y cmd: correcto.
- `gofmt` y `git diff --check`: correctos; solo avisos CRLF del checkout.
- No hay temporales de test/coverage y `feature_list.json` no fue modificado.

## Cuarta remediación independiente (REV-25..REV-27)

- REV-25 / R9,R10,R13: cada intento create/cleanup reserva durablemente su coordenada `fixture/phase/ordinal` antes de policy o red. Cleanup reconstruye calls/retries desde `FixtureState.Reservations`, comprueba el límite dentro del loop y conserva consumo tras restart/owner handoff. `TestCleanupBudgetReservationsSurviveRetryAndRestart` demuestra `MaxCalls=1` con transitorio y un solo DELETE, redelivery sin efecto y reanudación en ordinal 2; la carrera de owners permanece cubierta sin sleeps.
- REV-26 / R11,R12: `ValidateSummary` valida en ambas direcciones preparación, fixture result, intent, attempts, terminales, captures, incidents, bindings y todos los agregados `not_required/succeeded/partial/failed/deadline_exceeded`. `AttachFixtureAudit` valida antes de escribir. `TestFixtureAuditIsSealedAndOfflineVerified` reescribe `package.json`, `fixtures.json`, `report.json`, `report.md` y manifest, y rechaza cambios concordantes de status, fixture ID, create status, capture name y binding digest.
- REV-27 / R9,R10: `workflowConfig.fixture_runtime` exige límites trusted explícitos para bytes capturados, tool calls, retries globales, create retries, cleanup timeout/calls/retries, backoff y lease. La composición no deriva autoridad del documento y rechaza ausencia, cero o incoherencias. `TestWorkflowFixtureRuntimeConfigFailsClosed` cubre cada dimensión y las verticales productivas consumen los valores exactos.

### Gates de cuarta remediación

- `gofmt`, `go vet`, compile-only global y `git diff --check`: correctos; solo avisos CRLF del checkout.
- Focal REV-25 x20, package/rehash x10 y composición/cuatro superficies x5: correctos.
- `go test ./... -count=1`: correcto en todos los paquetes.
- Race UCRT64/CGO para fixture, localvalidation, validationworkflow y cmd/featureproof: correcto.
- Coverage: `internal/fixture` 85,3 %; `internal/validationworkflow` 85,4 %.
- Cross-build compile-only Linux/amd64 y Darwin/amd64 para fixture, validationworkflow y cmd/featureproof: correcto.
- Árbol sin temporales de coverage/build; `feature_list.json` intacto.


## Quinta remediación independiente (REV-28..REV-30)

- REV-28 / R4,R7,R8,R13: la denegación de presupuesto del primer intento confirma durablemente el cierre pre-I/O y elimina intent/eligible; si ya existe un attempt que cruzó red conserva la obligación de cleanup. `TestCreateBudgetDenialClosesOnlyKnownPreIOIntents` cubre denegación inicial, denegación de retry posterior a I/O y fallo al persistir el cierre.
- REV-29 / R11,R12: `ValidateSummary` modela create completo: ordinales contiguos, IDs y digests cerrados, terminal único, y `prepared` solo con terminal confirmada sin categoría de error. Writer y reader exigen copias concordantes y `report.md` canónico con una sola sección exacta. Las probes re-selladas rechazan attempt 99/unavailable, digest/evidence canario y Markdown duplicado.
- REV-30 / T14: `scripts/feature26_coverage.py` reproduce cobertura statement-weighted de todo Go productivo nuevo/modificado desde `1daa391`, usando `-coverpkg=./...` y tests conductuales. Resultado: **1635/1908 statements = 85,7 %**. Áreas señaladas: composition HTTP 95,2 %, fixture package 86,8 %, specification validate 91,2 %, YAML loader 81,0 %; el agregado del diff satisface el umbral >=85 %.

### Gates de quinta remediación

- `gofmt`, `go vet ./...`, compile-only global y `git diff --check`: correctos (solo avisos CRLF del checkout).
- Matrices focales fixture/localvalidation/specification/YAML y verticales productivas repetidas: correctas.
- `python scripts/feature26_coverage.py`: 1635/1908 = 85,7 %.
- Las verificaciones race y cross-build de la cuarta remediación siguen vigentes; los cambios de esta ronda se cubrieron además con focales race.
- La suite focal completa de feature 26 pasa. Una ejecución global bajo carga mostró dos timeouts históricos ajenos al comportamiento de fixture; sus pruebas aisladas pasan y no se relajó lógica productiva para ocultarlos.
- No quedan perfiles/binarios temporales en la raíz y `feature_list.json` permanece intacto.

## Sexta remediación independiente (REV-31..REV-32)

- REV-31 / T11,T12: `fixture-authority.json` contiene digest canónico, clave pública y firma Ed25519 del audit completo. La clave privada consumer-owned queda fuera del directorio re-sellable con modo 0600; `ReadPackage` deriva el trust anchor externo y verifica firma/digest antes de aceptar las cuatro copias públicas. La autoridad liga el fixture set, todos los evidence IDs, capture digests, cleanup bindings, estados y audit completo sin publicar material secreto. `TestFixtureAuditIsSealedAndOfflineVerified` incluye probes independientes con SHA-256 formalmente válidos para cada dimensión y reescribe package/fixtures/report JSON, Markdown y manifest.
- REV-32 / T14: `scripts/feature26_coverage.py` falla ante fuentes productivos ejecutables omitidos, declara explícitamente archivos de cero statements e incluye en el denominador los 15 statements Unix no ejecutables en Windows. `store_unix_test.go` ejercita en Unix el flock y rename reales. Comando exacto: `python scripts/feature26_coverage.py`; resultado **1666/1958 = 85,0868 %** sin redondeo del umbral.

### Gates de sexta remediación

- Focales de firma, re-sellado y límites de artefactos: correctos.
- Cobertura reproducible completa del diff: 1666/1958 = 85,0868 %; Unix explícito 0/15 en este host Windows y test real condicionado para hosts Unix.
- `feature_list.json` permanece intacto y no se generan perfiles dentro del repositorio.



## Séptima remediación independiente (REV-33..REV-34)

- REV-33 / T11,T12: `OpenFixtureAuditAuthority` persiste la private key Ed25519 consumer-owned fuera del package y entrega dos capacidades distintas: `FixtureAuditSigner` no exporta la clave y `FixtureAuditTrust` solo contiene key ID/public key. `AttachFixtureAudit` recibe exclusivamente signer; `ReadPackageWithFixtureTrust` recibe exclusivamente el anchor público y nunca conoce ruta ni abre private key. El mensaje firmado está separado por dominio `featureproof.fixture-audit/v1` y liga versión del package, run ID, `BindingSHA256` y SHA-256 canónico del audit completo. Ausencia, key ID mismatch, rotación/sustitución no autorizada, firma forjada y trust externo incorrecto fallan cerrado; un sidecar privado atacante no afecta al reader.
- REV-34 / T14: coverage usa metadata simétrica verificada por SHA-256 para Windows (17+7 statements) y Unix (15+1). Conserva los bloques medidos del host y agrega exactamente una vez el lado opuesto; falla si cambia/falta un fuente o diverge el count. `python -m unittest scripts/test_feature26_coverage.py` simula perfiles Windows y Unix. `store_unix_test.go` prueba flock/rename reales cuando el host es Unix; en este host Windows se hizo cross-build Linux/Darwin.

### Gates de séptima remediación

- `go vet ./...` y compile-only global: correctos.
- Focales authority/re-sell y cuatro superficies productivas: correctos.
- `go test ./... -count=1`: correcto en todos los paquetes.
- Race focal Ed25519/package con UCRT64: correcto.
- Cross-build fixture Linux/amd64 y Darwin/amd64: correcto; el test Unix real queda condicionado por GOOS y no puede ejecutarse en este host Windows sin runtime Go Unix.
- Coverage exacta: **1695/1993 = 85,0477 %**; incluye Unix 0/16 en el denominador Windows y ambos lados exactamente una vez.
- `git diff --check` limpio salvo avisos CRLF; `feature_list.json` intacto y sin temporales en la raíz.

## Octava remediación independiente (REV-35..REV-36)

- REV-35 / T11,T12,T14: `OpenFixtureAuditAuthority` persiste la private key en el store privado y un pin público en un trust store independiente. En cada restart deriva el keypair desde el seed, exige coincidencia byte a byte con la private key almacenada y valida `KeyID = SHA256(PublicKey)` contra el pin; no existe rotación implícita y cualquier sustitución o corrupción falla cerrado. El key ID forma parte del mensaje firmado domain-separated junto con versión, run, binding y audit digest. `LocalPackageReader` solo almacena `FixtureAuditTrust`; el signer reside exclusivamente en `LocalFixturePackageWriter`.
- La persistencia exige archivos regulares, single-link, con permisos/owner de plataforma y sin reemplazo entre `Lstat`/open. Bootstrap usa creación exclusiva, escritura completa, `Sync`, fsync del directorio cuando la plataforma lo permite y cleanup de parciales. La adquisición concurrente trata los sharing violations de Windows como contención acotada y falla cerrado al agotar el límite.
- `TestFixtureAuditAuthorityStoreFailsClosed` cubre orphans, keypair sustituido, corrupción same-length, pin corrupto o discordante, KeyID derivado, symlinks/hardlinks/permisos tanto en key como pin, short/error write, cleanup de parcial, reload y carrera de bootstrap. `TestFixtureAuditAuthoritySeparatesWriterAndReaderCapabilities` prueba trust ausente/ajeno, KeyID concordantemente sustituido, sidecar privado ignorado y firma forjada.
- REV-36 / T14: eliminado `scripts/__pycache__/`; unittest y coverage se ejecutan con `PYTHONDONTWRITEBYTECODE=1` y se comprueba que el directorio no reaparezca.

### Gates de octava remediación

- `gofmt` y `go vet ./...`: correctos.
- `go test ./... -run '^$' -count=1`: correcto.
- Paquetes afectados ×3 y matriz de autoridad repetida ×10/×20: correctos.
- `go test ./... -count=1`: correcto. Un primer run tuvo un fallo de temporización histórico en `internal/correction/TestCorrectionCancelJoinsAndPreservesParent`; aislado pasó ×20 y el siguiente run global completo pasó.
- Race focal UCRT64/CGO para localvalidation, validationworkflow y fixture: correcto.
- Cross-compile de tests Linux/amd64 y Darwin/amd64 para fixture, localvalidation, validationworkflow y cmd/featureproof: correcto.
- Coverage exacta del diff productivo: **1769/2080 = 85,0481 %**, incluida metadata simétrica de plataforma.
- Tests Python de coverage: correctos sin regenerar `__pycache__`.
- `git diff --check` limpio salvo avisos CRLF; `feature_list.json` intacto y sin temporales de build/coverage.

## Novena remediación independiente (REV-37)

- REV-37 / T11,T12,T14: en Windows, `protectFixtureAuthorityFile` instala antes de escribir contenido una DACL explícita, protegida y no heredada. La allowlist mínima contiene únicamente el owner actual, LocalSystem y Administrators con el FullControl canónico de archivo; la private key y el public pin reciben la misma protección de integridad estricta.
- Cada reload obtiene owner+DACL mediante las APIs nativas de Windows y falla cerrado ante descriptor inválido, owner o DACL defaulted, DACL ausente/no protegida, ACE heredada/denegada, máscara distinta, principal extra, principal trusted ausente o duplicado. La ruta Unix conserva `chmod 0600`, uid actual, archivo regular y single-link.
- `TestFixtureAuthorityWindowsDACLRejectsUntrustedAccess` aplica ACLs hostiles reales a los archivos: Everyone read sobre private key; Users write/delete; Authenticated Users FullControl; Everyone FullControl sobre pin; Users write/delete; Authenticated Users WRITE_DAC/WRITE_OWNER. También cubre ACL válida+reload, DACL no protegida, descriptor ausente, máscara trusted reducida y sustitución de un principal trusted por uno desconocido. La matriz previa mantiene symlink, hardlink y bootstrap race.
- El gate de coverage incorpora metadata simétrica ligada por SHA-256 para 54 statements Windows y 3 Unix del owner/ACL; ambos perfiles contabilizan ahora 102 statements específicos de plataforma sin omisiones ni doble conteo.

### Gates de novena remediación

- `gofmt`, `go vet ./...` y compile-only global: correctos.
- Matriz DACL Windows ×10, autoridad completa ×10 y paquetes afectados ×3: correctos.
- `go test ./... -count=2`: correcto en todos los paquetes.
- `go test -race` de todos los paquetes afectados con UCRT64/CGO: correcto.
- Cross-compile de tests Linux/amd64 y Darwin/amd64 para fixture, localvalidation, validationworkflow y cmd/featureproof: correcto.
- Coverage exacta del diff productivo: **1812/2131 = 85,0305 %**; unittest de profiles Windows/Unix correcto con `PYTHONDONTWRITEBYTECODE=1`.
- `git diff --check` limpio salvo avisos CRLF; `feature_list.json` intacto y sin temporales de build, coverage o Python.


