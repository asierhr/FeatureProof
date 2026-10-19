# Adapter de entornos preview existentes — Tasks

- [x] T1. Definir modelos provider-neutral, categorías de error y sellado de selección/reserva. [R1, R2, R3, R8, R10]
  - Escribir `TestExplicitAliasSelectsOneConfiguredProvider`,
    `TestReserveReturnsStableSafeSnapshot` y casos inválidos antes de la
    implementación.
  - Modelar selección, expected revision, reservation identity, availability,
    snapshot, target candidate y attestation segura con límites explícitos.
  - Sellar/fingerprint las identidades y copiar maps/slices defensivamente.
  - Definir errores comprobables con `errors.Is/As`, sin payloads del provider.
  - Añadir comentarios sistémicos a cada entidad, constante, variable, función,
    método y helper de test creado o modificado.

- [x] T2. Implementar el catálogo confiable y la resolución explícita de providers. [R1, R5]
  - Escribir `TestUntrustedFieldsCannotSelectProviderOrTarget` y tests de alias
    desconocido, duplicado, principal/PR no autorizado y falta de provider.
  - Resolver solo alias a configuración tipada; no aceptar URL, environment
    ref, provider o credencial desde inputs no confiables.
  - Validar que no existe provider por defecto y que perfiles `secretless`
    carecen de referencias secretas.
  - Documentar sistémicamente símbolos productivos y helpers del catálogo.

- [x] T3. Introducir la interfaz mínima `Provider` y un fake determinista auditable. [R2, R6, R9, R10]
  - Escribir `TestProviderContractHasNoInfrastructureMutation`,
    `TestReserveReconcilesSameKeyAndRejectsConflict` y
    `TestMissingPreviewIsNeverProvisioned`.
  - Exponer únicamente reserve/reconcile, readiness/observe y release de
    reserva lógica.
  - Hacer que el fake programe respuestas, bloquee por contexto, registre
    llamadas y detecte claves conflictivas sin red ni sleeps.
  - Comentar responsabilidad sistémica de la interfaz, sus DTOs y cada helper.

- [x] T4. Autorizar el target retornado y conservar las defensas HTTP existentes. [R4]
  - Escribir `TestProviderTargetPassesNetworkPolicy`,
    `TestDeniedTargetOpensNoDNSOrSocket` y
    `TestModelCannotReplaceAuthorizedTarget`.
  - Validar scheme, hostname, puerto efectivo, base path, userinfo, query,
    fragment y tamaños desde una policy seleccionada por ID.
  - Producir un `AuthorizedTarget` inmutable y reutilizarlo al componer
    `internal/tools/http`; no duplicar o relajar DNS, dial y redirect checks.
  - Incluir target/policy canónicos en la proyección segura del binding.
  - Comentar funciones internas de parsing/canonicalización y fakes de red.

- [x] T5. Construir el bridge de atestación y rechecks de identidad/revisión. [R3, R6]
  - Escribir `TestExactRepositoryCommitAttestationPrecedesExecution` y
    `TestAttestationMismatchOrDriftStopsWork`.
  - Traducir solo snapshots sellados a `revisionbinding.Attestation`.
  - Comparar environment, reservation, repository, commit, attestation
    reference, vigencia y target en admisión, cada batch y publicación.
  - Rechazar OID parcial, attestation ausente/expirada y cualquier deriva antes
    de trabajo nuevo.
  - Añadir comentarios sistémicos al bridge y helpers de snapshots.

- [x] T6. Implementar el coordinator de reserva, readiness y release con estado explícito. [R2, R6, R7, R8, R9]
  - Escribir `TestReadinessPendingThenReadyStartsOnce`,
    `TestUnavailableUnhealthyOrDriftNeverExecutes` y
    `TestEveryReservedPathReleases` antes de completar el ciclo.
  - Implementar transiciones selected→reserved→waiting→ready→executing→
    releasing→released y el estado recuperable `release_pending`.
  - Revalidar cada snapshot, no mantener locks en fronteras y garantizar que
    todo camino posterior a reserve intenta release.
  - Separar errores operativos/inconclusive/cancelled de functional failed.
  - Documentar toda transición, guard y helper de test con su papel sistémico.

- [x] T7. Añadir timeouts, cancelación, retries e idempotencia acotados. [R2, R6, R7, R9]
  - Escribir `TestEveryProviderOperationHonorsTimeout`,
    `TestCancellationStopsWorkReleasesAndJoins` y
    `TestReleaseIsIdempotentAndRecoversUncertainResult`.
  - Aplicar child deadlines a reserve, cada readiness, release y shutdown; el
    deadline efectivo es el menor.
  - Usar reloj, waiter y jitter inyectados; limitar attempts, elapsed y espera
    acumulada sin `time.Sleep`.
  - Reutilizar reservation/release keys en retries; reconciliar resultados
    inciertos y tratar `already released` como éxito.
  - Poseer y unir cualquier goroutine; acotar el contexto independiente de
    cleanup tras cancelación.
  - Comentar policy, supervisor y todos los fakes temporales/concurrentes.

- [x] T8. Componer secretos únicamente en transportes autorizados. [R5]
  - Escribir `TestSecretsReachOnlyAuthorizedTransport`,
    `TestSecretlessSelectionResolvesNoSecrets` y
    `TestCanariesNeverEnterArtifactsOrModel`.
  - Resolver referencias de provider/aplicación en `cmd/featureproof` y
    construir transportes sin añadir un secret resolver al core/provider.
  - Pasar bytes solo al transporte concreto; usar referencias opacas en
    proyecciones seguras y rechazar secretos para perfiles secretless.
  - Recorrer errores, logs, registry, binding, eventos, package, evidencias y
    contexto del modelo con canarios.
  - Añadir comentarios sistémicos a configuración, factories y spies.

- [x] T9. Integrar el ciclo preview con el runner compartido. [R3, R4, R5, R6, R8, R9]
  - Escribir un test vertical que reserve, ateste, autorice target, espere ready,
    ejecute un plan determinista, publique package y libere.
  - Extraer la mínima factory/input autorizado de `localvalidation` para evitar
    target libre en caminos preview y conservar la CLI local existente.
  - Entregar el AttestorBridge al binding y mantener rechecks antes de batches y
    publicación.
  - Verificar `TestPreviewFailuresAreOperationalOrInconclusiveNeverFailed` y
    `TestFunctionalAssertionFailureRemainsFailed`.
  - Comentar cada función/entidad modificada del runner y sus helpers.

- [x] T10. Sustituir los gates fail-closed de externalagent y GitHub por bridges productivos. [R1, R3, R5, R7, R8, R9, R11]
  - Escribir `TestExternalAgentAndGitHubSharePreviewLifecycle` con el mismo fake
    provider y comprobar selección/repository/head exactos.
  - Implementar `ApprovedExecutor` y `githubpr.ValidationGateway` sobre el caso
    de uso compartido, sin exponer tipos preview/provider en transportes
    públicos.
  - Mantener scopes, reautorización, idempotencia, cancelación, package/binding
    ownership y política secretless de forks.
  - Hacer que composición incompleta conserve el fallo cerrado y que `Close`
    cancele/junte workers antes de retornar.
  - Documentar sistémicamente bridges, funciones de composición y helpers.

- [x] T11. Añadir estado durable mínimo, recuperación y observabilidad segura. [R2, R7, R8, R9]
  - Escribir tests de caída tras reserve, durante readiness y antes/después de
    release; demostrar que no se duplica reserva, run ni liberación.
  - Persistir solo digests e IDs/snapshot seguros con transición atómica; no
    persistir secretos ni DTOs externos.
  - Recuperar por el último estado confirmado, reautorizar selección e
    integridad y continuar o liberar sin repetir un run confirmado.
  - Emitir eventos/métricas allowlisted y probar que bodies/mensajes del
    provider no aparecen.
  - Comentar store, records, recuperación y helpers de fault injection.

- [x] T12. Completar trazabilidad, comentarios y gates de verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Crear `TestPreviewSystemicCommentsCoverChangedSymbols` mediante AST para
    tipos, interfaces, funciones, métodos, helpers, variables y constantes de
    paquete nuevos o modificados.
  - Registrar `R<n> → test concreto → resultado` en
    `progress/preview-environment-adapter/implementation.md`.
  - Ejecutar suites dirigidas y globales, race, vet, builds requeridos,
    `git diff --check` y coverage >=85% de cada paquete nuevo o materialmente
    modificado; documentar cualquier limitación ambiental preexistente sin
    debilitar assertions.
  - Auditar con el fake que no existen llamadas de provision/deploy/update/
    restart/scale/delete/destroy y que todos los símbolos tienen comentario.

