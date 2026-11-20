# Seguridad y aislamiento multi-tenant — Tasks

- [ ] T1. Definir y sellar el modelo tenant y sus límites estructurales. [R1, R17, R19]
  - Añadir tests de IDs canónicos, digests, defensive copies, estados, límites y relaciones organization/project/principal/membership/service identity.
  - Rechazar duplicados, cross-tenant relations, expiry inválida, ciclos y máximo+1 antes de copiar o persistir.
  - Documentar la responsabilidad sistémica de tipos, funciones y helpers tocados.

- [ ] T2. Implementar el catálogo cerrado de acciones, recursos y policies RBAC. [R3, R17]
  - Escribir matrices TDD de roles/actions/resource classes/project scopes, combinación de roles y denegación estructural no anulable.
  - Compilar role/policy snapshots canónicos, versionados y digestados sin wildcards o acciones desconocidas.
  - Probar que un deny ocurre antes de cualquier collaborator/store call.

- [ ] T3. Implementar resolución de subject a principal y `TenantScope`. [R2, R3, R18]
  - Definir el puerto consumer-owned para authenticated subjects y resource identities confiables.
  - Cubrir subject ambiguo/obsoleto, issuer no permitido, suspended/revoked/expired y payload spoofing en tests herméticos.
  - Propagar cancelación/deadline y observar solo metadata allowlisted.

- [ ] T4. Crear el store local transaccional de seguridad y su contract base. [R1, R5, R13, R17, R19]
  - Implementar manifest/checksums, lock multiproceso, receipts, temp+fsync+rename y reload fail-closed bajo paths derivados de scope validado.
  - Confirmar command+audit+receipt de forma atómica y resolver unknown commit por operation identity.
  - Probar restart, segundo proceso, corrupción, cross-tenant same IDs, cancelación, contadores agotados y ausencia de fallback.

- [ ] T5. Añadir migrations y store PostgreSQL tenant-aware con security epoch. [R5, R13, R17, R19]
  - Crear migrations append-only para organizations/projects/principals/memberships/roles/policies/receipts y columnas `organization_id` necesarias.
  - Implementar backfill verificado, triggers de compatibilidad N/N+1 y activation de security epoch que impida multi-tenancy con writers antiguos.
  - Añadir composite PK/FK/index/predicates y RLS de defensa donde sea segura; tests de empty/latest/concurrent/dirty/future y cutover unknown.

- [ ] T6. Integrar administration service para projects, memberships, roles y service identities. [R1, R3, R12, R13]
  - Añadir tests de create/update/suspend/revoke/expire, expected version, idempotency y cross-tenant denial.
  - Confirmar mutación, audit event y receipt en una sola frontera local/PostgreSQL.
  - Hacer fail-closed audit/store fault y comprobar que no existe estado parcial.

- [ ] T7. Sellar capabilities y binding tenant/config/run causal. [R4, R7, R13]
  - Extender authority envelopes y run bindings con organization/principal/policy/security revision y digests sin rederivar desde IDs.
  - Probar admisión atómica con configuration binding, run y primer event/job en local y PostgreSQL.
  - Cubrir expiry, phase minimization, fence/digest drift, replay/conflict y head/policy posterior sin reatribución.

- [ ] T8. Reforzar project configuration y resource catalogs con organization scope. [R3, R4, R7]
  - Implementar `OrganizationPolicySource`/catalog authority desde tenant policy y project registration.
  - Añadir tests de ProjectKey ligado a otro tenant, descriptors/secret refs cross-scope y effective digest con tenant policy.
  - Verificar recovery histórico exacto y revocación de secret ref sin fallback/rebinding.

- [ ] T9. Propagar scope compuesto a stores y queries tenant-owned existentes. [R5, R17, R19]
  - Inventariar runs, config, packages/evidence, environments/capacity/cleanup, corrections y demás estado productivo.
  - Añadir organization/project a keys, cursores, FKs y predicates; eliminar lookups globales por ID.
  - Ejecutar tests adversariales same-ID, cursor manipulado, counts ocultos, corrupción y contract equivalence local/PostgreSQL.

- [ ] T10. Aislar queue, inbox/outbox, workers, caches y objetos. [R4, R6]
  - Scopear jobs/claims/leases/effects/idempotency y verificar organization+project+run+fence en cada transición.
  - Introducir `ScopedCacheKey` y object metadata/key derivados internamente; probar eviction/get/delete cross-tenant denegados.
  - Cubrir worker cross-tenant, redelivery same/different scope y labels de observabilidad no controlados por payload.

- [ ] T11. Implementar envelope encryption y persistencia de secret versions. [R8, R13, R17]
  - Definir key-provider/rand puertos privados y AAD canonical tenant-scoped; stores reciben solo metadata/ciphertext.
  - Añadir fake crypto controlado y canaries para filesystem, rows, journal, errors, logs y backups de test.
  - Probar wrong tenant/ref/version/AAD/key/algorithm, oversize, fault boundaries y cero plaintext persistido.

- [ ] T12. Implementar secret leases single-use y entrega JIT privada. [R9, R17, R18]
  - Confirmar lease con run/phase/consumer/purpose/fence/expiry y consumir/auditar antes de entregar al callback privado.
  - Probar replay, expiry, cross-run/phase/consumer, cancellation, decrypt failure y buffer discard.
  - Auditar que model context, evidences, packages, events, jobs, caches y observability no contienen canaries.

- [ ] T13. Implementar rotación y revocación de secrets/service identities. [R10, R12, R13]
  - Añadir CAS monotónico de versions/activation/security revision con receipts y audit causal.
  - Probar rotation concurrente, same/different digest replay, revocation-vs-resolve y leases no consumidas.
  - Verificar que runs históricos no se reescriben y que ref revocada falla sin fallback.

- [ ] T14. Validar seguridad de transporte en composition. [R11, R18]
  - Definir `TransportSecurity` para PostgreSQL y puertos remotos sin importar SDKs en core.
  - Probar rechazo productivo de TLS/peer auth ausente, reconnect sin downgrade y loopback solo bajo test isolation explícito.
  - Incluir canaries de DSN/cert/token/key material en errores y telemetry.

- [ ] T15. Implementar audit journal, chain verification y consultas seguras. [R12, R17, R18]
  - Añadir sequence/head CAS, previous/event digest, actor/scope/action/resource/outcome allowlisted y append-only local/PostgreSQL.
  - Probar alteration/deletion/reorder/duplicate/cross-tenant, restart, pagination y cursores sellados.
  - Integrar allow/deny, secret access, membership/role/policy, quota override y deletion sin payloads sensibles.

- [ ] T16. Generalizar la frontera transaccional de acción sensible + audit. [R13, R17]
  - Ejecutar fault matrix antes/durante/después de cada statement/rename para administración, secrets, quota y retention.
  - Comprobar ambos-o-ninguno, unknown commit por receipt, no duplicate audit y deny que nunca concede acceso aunque audit falle.
  - Mantener callbacks de red/crypto/observer fuera de transacciones/locks.

- [ ] T17. Implementar policies versionadas de retención y eligibility. [R14, R17]
  - Validar floors organizativos, class periods, protected records, batch/graph limits y policy digest.
  - Añadir tests de active/live refs, pending jobs/outbox/cleanup, active secret version y config expansiva/ausente.
  - Fijar policy snapshot/eligibility boundary en cada candidate de forma determinista.

- [ ] T18. Implementar retention/deletion controller convergente. [R15, R17]
  - Persistir intent antes del delete y usar deleters consumer-owned para run/package/evidence/environment/secret/project data.
  - Probar crash/restart/unknown delete, dependency order, reference race, fencing, cancellation y tombstone/audit.
  - Cubrir project deletion: bloquear admission, revocar identities/leases, converger trabajo y preservar protected records.

- [ ] T19. Implementar quota/rate/budget hierarchy y reservations durables. [R16, R17]
  - Evaluar tenant/project conjuntamente, confirmar reservation pre-effect y emitir receipts idempotentes.
  - Probar last-slot multiproceso, window/burst, same/different demand replay, unknown effect, release exactly once y overflow.
  - Compartir contract suite local/PostgreSQL y fake clock sin `time.Sleep`.

- [ ] T20. Integrar quotas antes de workers, modelo, environments e infraestructura. [R4, R6, R16]
  - Conectar admission/queue claim, model call/token budget, capacity/environment acquire y object/storage reservation.
  - Añadir tests que retiren el quota guard y fallen; comprobar que denial no invoca consumer y que uncertainty conserva reservation.
  - Confirmar quota+consumer causalmente cuando comparten transactor o mediante intent/reconcile cuando no.

- [ ] T21. Conectar identidad/RBAC/binding a todas las superficies productivas. [R2, R3, R4, R6, R7]
  - CLI, API/MCP, GitHub, external-agent y workers deben converger en principal resolver/authorizer comunes sin overrides.
  - Añadir verticales de allow/deny/spoof/replay/recovery por superficie y prueba de cambio de membership/head en proceso vivo.
  - Verificar que conocer IDs no permite consultar existencia, audit, quota o secrets.

- [ ] T22. Completar errores tipados y observabilidad allowlisted. [R17, R18]
  - Mapear categorías locales/SQLSTATE sin comparar mensajes ni confundir permission/unavailable con not-found.
  - Probar retries de transacción completos, budgets, cancellation/deadline y observer panic/failure.
  - Ejecutar auditoría de canaries sobre errors/events/logs/metrics/traces y cardinalidad de labels.

- [ ] T23. Ejecutar contract suite de seguridad local/PostgreSQL y migración N/N+1. [R5, R8, R10, R12, R13, R14, R15, R16, R19]
  - Cubrir isolation, secrets ciphertext, audit chain, quota, retention, receipts, corruption, restart y cancellation con el mismo contrato.
  - Ejecutar PostgreSQL 16 real sobre database UUID autorizado, migrations empty/latest/concurrent, backfill/security epoch, normal/race y cleanup estricto.
  - Verificar ausencia de fallback/dual-write y operación segura durante la compatibility window.

- [ ] T24. Ejecutar adversarial verticals y verificación trazable de cierre. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15, R16, R17, R18, R19, R20]
  - Cubrir acceso cruzado, role escalation, IDs/cursors manipulados, shared caches/objects, rotation/revocation, deletion, audit y concurrency.
  - Ejecutar format, vet, compile-only, global normal/race, focales race repetidas y builds Windows/Linux/Darwin aplicables.
  - Generar manifest completo y coverage diferencial exacta `>=85%` sin redondeo; auditar comentarios sistémicos y ausencia de secretos/temporales.
  - Registrar `R<n> → test`, comandos, N/D de coverage, gate PostgreSQL terminal y cleanup en `progress/tenant-security/implementation.md`.
