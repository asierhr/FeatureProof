# Feature 28 — Validated project memory implementation

## Estado

`ready_for_review`

Base aprobada: `9c8aa34`. Se implementó únicamente feature 28. El leader sigue
siendo el único owner de `feature_list.json`, que no se modificó. No se añadió
selección por rol, ranking semántico, embeddings, presupuesto de prompt ni
ensamblado de contexto de feature 29.

## Entrega

- `internal/projectmemory` contiene el contrato cerrado e inmutable
  `ProjectMemory/v1`, lifecycle, autoridad, provenance, evidence, staleness,
  secret gate, query estructural, conflictos, bindings históricos y el servicio.
- El store local mantiene un journal autenticado append-only, snapshots e índice
  atómicos, CAS, receipts, locks y recovery. Un registry HMAC durable vive en un
  trust root externo al root mutable y conserva el head y los receipts conocidos.
  La frescura se contrasta con un `MonotonicWitness` externo, linealizable y no
  replayable que no pertenece a ningún root o fichero administrado por el store.
- Las primitives Windows y Unix fijan directorios mediante handles/dirfd, abren
  objetos no-follow, verifican identidad efectiva, links y permisos, y publican
  mediante replace atómico. Windows valida una DACL protegida y niega escritura
  a principals ajenos al usuario del proceso, SYSTEM y Administrators.
- `agentplanning` consulta y sella descriptores desde el servicio compuesto;
  `validationworkflow` persiste el binding confiable y lo revalida antes de
  approval y antes de I/O; `functionalflow` verifica el package terminal y solo
  entonces crea una observation ligada al digest y lineage de evidence.
- `cmd/featureproof` compone una sola instancia del servicio/authority para todo
  el workflow y la comparte con las entradas API, CLI, MCP y GitHub. Los claims
  de binding/authority del caller se rechazan y se sustituyen por la selección
  derivada dentro de composición.

## Séptima remediación (REV-16)

| Finding | Cambio y evidencia |
|---|---|
| REV-16 | Cada commit se prepara primero en un staging exclusivo autenticado y sincronizado. El witness avanza por CAS con el digest opaco del envelope pendiente y solo entonces se materializa el journal definitivo y sus proyecciones. Un rechazo anterior al avance retira el staging y deja journal/head byte-for-byte intactos; si el witness avanzó, restart recupera exactamente el evento ligado al witness sin repetir efectos ni receipts. Pending missing, alterado, sustituido o con coordenadas incompatibles falla cerrado. `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestWitnessCrashBoundariesAndConcurrentCAS` y `TestMemoryStoreMultiprocessCAS` cubren la matriz. Las rutas reales relevantes son `TestWitnessCrashBoundariesAndConcurrentCAS/before-advance-fails-closed`, `/partial-append-recovers-from-staging`, `/after-append-before-projection-recovers` y `/missing-tampered-or-aliased-staging-fails-closed/{deleted,tampered,hardlink,symlink}`. |

## Decimotercera remediación (REV-17)

| Finding | Cambio y evidencia |
|---|---|
| REV-17 | El runner abre `remote_binary` exactamente una vez con `exec 3<`; `exec 4<&3` y `exec 5<&3` duplican esa misma open file description antes de retirar el pathname. Hash pre, ejecución y hash post resuelven únicamente `/proc/$$/fd/{3,4,5}`, nunca vuelven al nombre original. `test_exact_linux_runner_bytes_pass_bash_syntax` exige una sola redirección desde `$remote_binary` y las dos duplicaciones. `test_remote_path_swap_after_single_open_executes_original_identity` detiene el runner tras duplicar FDs, sustituye atómicamente el pathname y comprueba que se ejecutan los bytes originales y se devuelve su profile; la sustitución no entra en hash ni ejecución. `bash -n`, la integración focal Linux y los once probes pasan sobre los bytes exactos. El gate fresco devuelve `2027/2359 = 85,926240 %`. |

## Duodécima remediación histórica (REV-17, reemplazada)

| Finding | Cambio y evidencia |
|---|---|
| REV-17 | Introdujo bytes shell literales por stdin y eliminó la doble interpretación de WSL, pero abría el pathname remoto tres veces antes de unlink. Fue reemplazado por la apertura única y duplicación de FD de la decimotercera remediación. |

## Undécima remediación histórica (REV-17, reemplazada)

| Finding | Cambio y evidencia |
|---|---|
| REV-17 | Introdujo buffers de lectura única y cerró el TOCTOU de profiles, pero construía el runner con escapes dependientes de la capa `sh -lc` de WSL. Fue reemplazado por los bytes literales y `sh -s` de la duodécima remediación. |

## Décima remediación histórica (REV-17, reemplazada)

| Finding | Cambio y evidencia |
|---|---|
| REV-17 | El gate cerrado ya producía `2027/2359 = 85,926240 %`, pero conservaba reaperturas por pathname entre validación y merge. Ese resultado es histórico y no se usa como evidencia de la undécima remediación. |

## Novena remediación histórica (REV-17, reemplazada)

El diseño de sidecars/checksums públicos fue retirado porque permitía fabricar
attestations después del perfil. No forma parte del gate ni de sus comandos.

## Sexta remediación (REV-13)

| Finding | Cambio y evidencia |
|---|---|
| REV-13 | Se eliminaron ledger y manifest locales. `MonotonicWitness` expone `Read`, bootstrap create-once explícito y `CompareAndAdvance`; sella project/generation/head/receipts fuera de todos los datos que `LocalStore` puede escribir, restaurar o reconstruir. Missing, unavailable, behind o ahead incompatible fallan cerrados y no existe repair desde journal. `HTTPMonotonicWitness` usa una autoridad HTTPS separada, bearer, redirects denegados y protocolo estricto/acotado. La composición exige URL, credential y la garantía exacta `linearizable-durable-nonreplayable-v1`, o falla configuración. `TestExternalWitnessRejectsCoordinatedLocalRollback` restaura juntos journal/snapshot/index/registry de seq1 con witness en seq2: restart/redelivery fallan, el receipt no se reutiliza y el witness permanece en seq2. `TestWitnessRequiresExplicitBootstrapAndFailsClosed`, `TestWitnessCrashBoundariesAndConcurrentCAS`, `TestHTTPMonotonicWitnessEnforcesExternalProtocol`, `TestHTTPMonotonicWitnessRejectsMalformedAuthorityResponses` y `TestMemoryStoreMultiprocessCAS` cubren deletion/unavailable/behind/ahead/replay, restart sobre la misma autoridad, CAS concurrente, conflictos, confirmación incompatible o indisponible, crash antes/después y bootstrap explícito. |

## Quinta remediación histórica (REV-13 y REV-15, reemplazada)

REV-15 quedó cerrado con cobertura reproducible. La solución REV-13 descrita a
continuación fue rechazada porque ledger y manifest HMAC podían restaurarse
juntos; se conserva únicamente como historial y fue eliminada en la sexta
remediación.

| Finding | Cambio y evidencia |
|---|---|
| REV-13 | Intento reemplazado: ledger y manifest HMAC detectaban pérdida individual, pero no rollback coordinado de ambos. Ya no forman parte del producto. |
| REV-15 | Se regeneraron desde cero los cuatro perfiles Windows/Linux después del último cambio productivo. El gate versionado combina statement blocks por coordenada, cubre build tags sin contar bloques comunes dos veces y obtiene `1941/2266 = 85,657546 %`, con 15 statements de margen. La cobertura añadida entra por el servidor API productivo y ejercita branches reales del protocolo monotónico; no replica la implementación. `scripts/test_feature28_coverage.py` verifica el cálculo, la deduplicación y el fallo bajo el umbral. |

## Cuarta remediación (REV-9, REV-11, REV-12, REV-13 y REV-14)

| Finding | Cambio y evidencia |
|---|---|
| REV-9 | `runWithContext` enruta CLI/MCP/API y el runtime GitHub firmado llama el mismo `projectmemory.Service` y store. `TestComposedMemoryEntrypointsShareServiceCASIdempotencyAndDTO` y `TestProjectMemoryProductDispatchersOwnMCPAndAPILifecycles` entran por los adapters productivos y prueban DTO, principal, CAS e idempotencia compartidos. |
| REV-11 | El root se fija antes de recorrer cada componente mediante handles relativos no-follow; `v1`, `projects` y cada project dir se comparan con identidades fijadas. La enumeración usa el handle, el temporal permanece abierto hasta replace y el destino se compara con la identidad efectiva. `TestAncestorAndPostCheckSourceSwapsFailClosed` y `TestPublishBoundarySwapsAreBlockedOrDetected` corren en Windows y Linux. El runtime Linux encontró durante esta remediación una ventana tras renombrar el directorio; se corrigió reabriendo y comparando la ruta efectiva en cada checkpoint. |
| REV-12 | Human, observer y reconciler tienen authorities, principals y actor kinds distintos. Cada comando exige su rol y la matriz `TestRoleAuthoritiesDenyEveryCrossRoleCommand` niega todas las combinaciones cruzadas. GitHub solo acepta comandos exactos cubiertos por `TestMemoryWebhookCommandsRequireExactOperationAndCoordinates`. |
| REV-13 | Intento histórico reemplazado por el witness externo de la sexta remediación. |
| REV-14 | `projectMemoryQueryPolicy` se configura por consumidor en composición; el request solo puede estrechar classes/states/authorities/sensitivity y no decide el modo review. `TestProjectMemoryOperationalQueryPolicyRejectsCallerSelectedEligibility` niega proposed, terminal, restricted, authority y class no concedidas. |

## Remediación de la segunda revisión

| Finding | Cambio y evidencia |
|---|---|
| REV-9 | El workflow productivo compone un único servicio. Planning hace query+binding, el workflow revalida y el observer consume un package terminal verificado. `TestProductWorkflowCompositionPersistsTrustedMemoryBinding`, `TestWorkflowOwnsTrustedMemoryFromPlanningThroughExecution` y `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces`. |
| REV-10 | El registry autenticado está fuera del mutable memory root y distingue un stream nunca creado de uno borrado. Sella head y receipts. `TestExternalRegistryDetectsWholeMemoryRootDeletionAndJournalRollback`. |
| REV-11 | Lock, journal, registry, snapshot, index, temporales y replace usan directorios fijados y handles efectivos no-follow; cada boundary revalida identidad. Windows aplica DACL estricta. `TestPublishBoundarySwapsAreBlockedOrDetected`, `TestLockBoundarySwapsFailClosed`, `TestEveryMutableStoreObjectRejectsHardlinkAliases`, `TestOwnedTemporaryHardlinkInjectionFailsBeforePublication`, `TestStoreObjectsAndProjectDirectoryRejectReparseAliases` y `TestWindowsDACLRejectsBroadWriteAccess`. |
| REV-12 | Se separaron observer, human y reconciler/configuration. `observed` ya no es una transición pública, approve/invalidate/supersede requieren `DecisionID`, expiry registra autoridad del reconciler y GitHub liga acción exacta a operación+target. `TestSeparatedTransitionAuthoritiesRejectFabricatedObservationAndMissingDecisions` y las matrices de resolver en `cmd/featureproof`. |
| REV-3/5/6/8 | Las integraciones, el anchor externo, el hardening de todos los objetos y la trazabilidad se sustituyeron por verticales y matrices de aceptación reales citadas en este informe. |

## Trazabilidad R1–R15

| Requirement | Tests concretos |
|---|---|
| R1 | `TestMemoryV1StrictCanonicalAndImmutable`, `TestClosedContractValidationMatrix` |
| R2 | `TestCreationAuthorityMatricesAndServerProvenance`, `TestBindingRequiresVerifiedQueryAndEveryAuthorityCoordinate` |
| R3 | `TestExplicitExpiryAndReplacementLifecycle`, `TestSeparatedTransitionAuthoritiesRejectFabricatedObservationAndMissingDecisions`, `TestRoleAuthoritiesDenyEveryCrossRoleCommand` |
| R4 | `TestCreationAuthorityMatricesAndServerProvenance`, `TestEvidenceRevocationAndFunctionalDigestBecomeStale`, `TestFlowPackageReconstructsFullOrderedLifecycle` |
| R5 | `TestEvidenceRevocationAndFunctionalDigestBecomeStale`, `TestBindingRequiresVerifiedQueryAndEveryAuthorityCoordinate` |
| R6 | `TestMemorySecretGateRunsBeforeStore`, `TestSeparatedTransitionAuthoritiesRejectFabricatedObservationAndMissingDecisions` |
| R7 | `TestMemorySecretGateRunsBeforeStore`, `TestRealTransportVerticalAndBindingDrift`, `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces` |
| R8 | `TestJournalRejectsTrailingDataAndIndependentAnchorDetectsDeletion`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestWitnessRequiresExplicitBootstrapAndFailsClosed`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestWitnessCrashBoundariesAndConcurrentCAS`, `TestPublishBoundarySwapsAreBlockedOrDetected` |
| R9 | `TestConcurrentMemoryWritersAndRedeliveryConverge`, `TestMemoryStoreMultiprocessCAS`, `TestExternalWitnessRejectsCoordinatedLocalRollback`, `TestWitnessCASPrecedesDefinitiveJournalAppend`, `TestWitnessCrashBoundariesAndConcurrentCAS` |
| R10 | `TestAncestorAndPostCheckSourceSwapsFailClosed`, `TestEveryMutableStoreObjectRejectsHardlinkAliases`, `TestStoreObjectsAndProjectDirectoryRejectReparseAliases`, `TestWindowsDACLRejectsBroadWriteAccess` |
| R11 | `TestQueryOrdersAndFindsConflictsBeforeTruncation`, `TestMemoryQueryFiltersBeforeStableOrderingAndIndex` |
| R12 | `TestWorkflowOwnsTrustedMemoryFromPlanningThroughExecution`, `TestProductWorkflowCompositionPersistsTrustedMemoryBinding`, `TestFlowPackageReconstructsFullOrderedLifecycle` |
| R13 | `TestComposedMemoryEntrypointsShareServiceCASIdempotencyAndDTO`, `TestProjectMemoryProductDispatchersOwnMCPAndAPILifecycles`, `TestMemoryWebhookCommandsRequireExactOperationAndCoordinates` |
| R14 | `TestMemoryCancellationLimitsAndRetriesOwnAllWork`, `TestQueryWorkAndIntegrityCancellationBranches`, race focal/global |
| R15 | T1–T16, matrices, gates, coverage del diff multiplataforma y este informe |

## Matrices verificadas

- Autoridad: proposal de agente sin elevación, scope/sensitivity cerrados,
  provenance server-owned, actor/action/target exactos, observed solo con evidence,
  decisiones humanas tipadas y expiry de reconciler.
- Query/binding: filtros dentro del budget, orden normativo y conflictos antes de
  truncar por item/bytes, selección vacía, scope/revision/commit/config/head/query
  exactos, historical versions y evidence revocada/ausente/corrupta.
- Durabilidad: trailing JSON/bytes, journal/tail missing, borrado de
  snapshot/index/directorio/root, rollback coordinado, restart/redelivery,
  receipts, witness missing/unavailable/behind/ahead/replay, staging autenticado,
  CAS anterior al append definitivo, partial append, publicación incompleta,
  crashes antes/después del advance y writers intra/interproceso.
- Filesystem: traversal, symlink/reparse, hardlink para cada objeto mutable,
  permisos/DACL, component swaps en read/write/publish/lock y temporales propios.
- Vertical: start→planning query/binding→approval→execution→observation→restart y
  drift por API, CLI, MCP y GitHub, sin efectos posteriores al fallo de revalidate.
- Secrets: poisoning y canarios se rechazan antes de persistencia/índice y no
  aparecen en errores, views, artifacts, journal, temporales ni superficies.

## Gates reproducibles

Ejecutados desde la raíz el 2026-10-23:

```powershell
gofmt -w <archivos Go modificados>
go vet ./...
go test ./... -run '^$'
go test ./internal/projectmemory -count=3
go test ./internal/agentplanning ./internal/functionalflow ./internal/githubpr ./internal/validationworkflow ./cmd/featureproof -count=1
go test ./... -count=1
$env:CGO_ENABLED='1'; $env:CC='gcc'; $env:Path='C:\msys64\ucrt64\bin;'+$env:Path; go test -race ./internal/projectmemory ./internal/agentplanning ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof -count=1
$env:CGO_ENABLED='1'; $env:CC='gcc'; $env:Path='C:\msys64\ucrt64\bin;'+$env:Path; go test -race -p 1 ./... -count=1
$env:PYTHONDONTWRITEBYTECODE='1'; python scripts/feature28_coverage.py verify
$env:GOOS='darwin'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go test -c ./internal/projectmemory -o $env:TEMP\feature28-r9-cross-darwin-internal-projectmemory.test
# Se repitió `go test -c` para agentplanning, functionalflow,
# validationworkflow y cmd/featureproof en linux/amd64 y darwin/amd64.
go test ./internal/projectmemory -run '^TestWitnessCrashBoundariesAndConcurrentCAS$/^(before-advance-fails-closed|partial-append-recovers-from-staging|after-append-before-projection-recovers|missing-tampered-or-aliased-staging-fails-closed)$' -count=3
$env:PYTHONDONTWRITEBYTECODE='1'; python -m unittest scripts/test_feature28_coverage.py
git diff --check 9c8aa34
```

Resultados:

- gofmt, auditoría de comentarios, vet, compile-only, focales repetidos, suite
  global, race focal y race global serial: pass. Dos ejecuciones race paralelas
  encontraron flakes de temporización preexistentes en functionalflow y
  validationworkflow; cada caso aislado pasó tres veces y la suite global serial
  posterior pasó completa, sin ocultar ni excluir paquetes.
- Runtime final de `internal/projectmemory`: Windows y Linux/amd64 pass. El gate
  compila el binary Linux desde el source final, transfiere exactamente su buffer
  verificado, abre su pathname una sola vez y conserva esa identidad mediante FDs
  duplicados durante hash pre, ejecución
  y hash post; el profile se recupera antes del cleanup. Linux/amd64 y
  Darwin/amd64 pasan el cross-build final. En Windows se omite symlink únicamente
  si el token no tiene ese privilegio; hardlinks, swaps y DACL sí corren.
- Coverage statement-weighted fresca de todo el diff productivo desde `9c8aa34`,
  incluidos fuentes nuevos y build-tagged: `2027/2359 = 85,926240 %`. Son 21
  statements de margen sobre los 2006 exigidos. `verify` genera cuatro perfiles,
  ejecuta, mide y elimina toda la
  evidencia en un único proceso con recetas cerradas. No acepta ni publica rutas,
  comandos, sidecars o manifests; stale/mixed/posthoc no pueden entrar. Linux es
  obligatorio y su binary/profile se comprueba antes de medir. El gate y sus once
  probes viven en `scripts/feature28_coverage.py` y
  `scripts/test_feature28_coverage.py`.
- `git diff --check`, limpieza de binarios/perfiles/scripts y ausencia de diff en
  `feature_list.json`: pass.


## Riesgos y límites materiales

- La protección contra rollback presupone que el servicio configurado como
  witness cumple su contrato linealizable, durable, sin delete y no replayable.
  El adapter verifica coincidencia exacta y falla cerrado, pero no puede aportar
  esa garantía mediante otro fichero o HMAC local. Si el operador no dispone de
  esa autoridad externa, la composición productiva rechaza la configuración.
- Darwin se verificó por cross-build; Windows y Linux ejecutaron runtime final.
- Las credenciales HMAC solo se resuelven en composición. No se serializan en el
  journal, snapshots, índice, bindings, harness ni superficies.
