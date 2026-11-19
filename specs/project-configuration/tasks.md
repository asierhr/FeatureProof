# Configuración versionada por proyecto — Tasks

> La implementación no comienza hasta aprobación humana explícita y transición
> de la feature a `in_progress` por el leader. Cada tarea sigue TDD: test que
> falla, implementación mínima, refactor y verificación. No se marcan checks en
> fase de spec.

- [x] T1. Modelo, strict loader y representación canónica [R2, R3, R5, R17]

  - Crear tests table/property-based para schema v1, límites de bytes,
  unknown/duplicate fields, trailing data, nulls, IDs, cardinalidades y ciclos.
  - Definir `ProjectKey`, `Document`, referencias tipadas, constraints y
  errores cerrados con constructores que copien memoria mutable.
  - Implementar strict loader, normalización determinista, JSON canónico y
  SHA-256, sin aceptar campos de valores secretos.
  - Probar equivalencia semántica, cambio material de digest, overflow y
  diagnostics ordenados/acotados.
  - Añadir comentarios sistémicos a todos los símbolos alcanzados por R17.

- [x] T2. Authority y frontera del application service [R1, R14, R15, R17]

  - Escribir tests de action/project/repository mismatch que demuestren que
  ningún store, catalog o policy source se invoca sin authority válida.
  - Definir capability opaca `ConfigurationAuthority`, actions cerradas y
  service consumer-owned; impedir reconstrucción desde payload/IDs libres.
  - Añadir propagación de context, clasificación de cancellation/deadline y
  metadata de observer allowlisted.
  - Documentar explícitamente que roles/membership/RBAC se delegan a feature
  37 y no se modelan en este paquete.

- [x] T3. Catálogo y validación semántica [R3, R4, R5, R14]

  - Crear fakes contractuales de `ResourceCatalog` y matrices para contracts,
  providers/targets, identities, fixtures, runtime/model/auth profiles y policies.
  - Implementar snapshot acotado/versionado/digestado y validación de kind,
  scope, versión, expected digest y referencias inexistentes/stale.
  - Validar compatibilidad cruzada fixture-operation-contract, identity-
  operation, provider-target-profile y policies con orden determinista.
  - Probar drift entre validación preliminar y confirmación y asegurar que
  collaborator errors/raw payloads no cruzan la frontera.

- [x] T4. Defaults organizativos monotónicos [R6, R12, R17]

  - Escribir tests de tabla/property para defaults ausentes, ceilings, valores
  más restrictivos, approval floor, retention range y allowlist subset.
  - Definir `OrganizationPolicySnapshot` y `OrganizationPolicySource` como
  entradas confiables, versionadas y digestadas, sin modelo tenant/RBAC.
  - Implementar merger fail-closed: ninguna unión o default embebido puede
  ampliar permisos, capacidades o recursos.
  - Probar policy ausente/corrupta/drift y cambio de effective digest.

- [x] T5. Contrato de persistencia e invariantes compartidas [R7, R8, R9, R10, R11, R13]

  - Definir contract suite para versions, activations, heads, effective
  snapshots, events, receipts, history y run bindings.
  - Cubrir immutability, monotonic version/generation, CAS, same/different
  idempotency digest, defensive copies, ordering y keyset cursor.
  - Incluir fault points before/after/unknown commit y contrato de resolución
  por receipt/head, sin depender de timestamps.
  - Definir comandos store ya canonicalizados, sin callbacks/I/O externo en
  locks o transacciones.

- [x] T6. Adapter local durable [R7, R8, R9, R10, R11, R13, R14]

  - Hacer fallar la contract suite contra un root temporal explícito y añadir
  tests de restart, segundo proceso, corrupción, cancelación y stale head.
  - Implementar paths derivados solo de IDs validados, lock, temp files,
  checksums, fsync/rename atómico, receipts y recovery fail-closed.
  - Persistir binding y estado causal local con publicación atómica equivalente
  para no observar run sin binding.
  - Verificar que outage/integrity nunca activa fallback a otro backend.

- [x] T7. Migrations y adapter PostgreSQL [R7, R8, R9, R10, R11, R13, R14, R18]

  - Añadir tests de manifest/migrations para empty/latest/idempotent/concurrent,
  checksums, schema future y ventana N/N+1 del control plane.
  - Añadir tablas/constraints/indexes de versions, activations, heads,
  effective snapshots, events, receipts y run bindings.
  - Implementar el store PostgreSQL con CAS/unique constraints, transacciones
  cortas, SQLSTATE mapping y full-transaction retry bajo budget.
  - Ejecutar la misma contract suite del adapter local e inyectar rollback en
  cada statement boundary y resultado de commit desconocido.

- [x] T8. Publicación y activación atómicas [R1, R4, R7, R8, R14, R15]

  - Escribir tests del flujo authority → load → snapshots → validate → commit,
  incluidos competing publishers y catalog/policy drift.
  - Implementar `Validate` y `PublishAndActivate`, bloqueando snapshots antes
  de commit y revalidando sus digests dentro de la frontera autoritativa.
  - Confirmar version + effective snapshot + activation + head + event +
  receipt como una unidad, con idempotencia y overflow checks.
  - Emitir observabilidad best effort solo después de commit y probar que un
  evento durable fallido aborta la operación.

- [x] T9. Rollback por nueva activación [R1, R7, R9, R14, R15]

  - Crear tests de versión histórica compatible/incompatible/corrupta,
  expected head stale, competing publish y redelivery idempotente.
  - Implementar revalidación contra policy/catalog actuales y nueva activation
  reason `rollback` sin copiar ni mutar la versión histórica.
  - Verificar journal causal, receipt y ausencia de cambios ante fallo.

- [x] T10. Query, history y export seguro [R1, R5, R7, R10, R15]

  - Probar active/version/effective/history, copias defensivas, response limits,
  stable ordering y cursors alterados/cross-project/expirados.
  - Implementar query service con authority previa, cursor sellado y errores
  que no revelen existencia cross-scope.
  - Añadir canaries para demostrar ausencia de secret values, resolver state,
  raw input y collaborator errors en DTOs, export, logs y events.

- [x] T11. Resolver de admisión y binding inmutable [R4, R6, R11, R12, R14]

  - Escribir tests de no-head, unsupported/obsolete schema, policy/catalog
  drift, reference retirada, concurrent head change y configuración válida.
  - Implementar `ResolveForAdmission` para fijar activation/config/policy/
  catalog/effective versions y digests en un binding canónico.
  - Probar same-run/same-digest convergence y same-run/different-digest
  conflict, además de copy/reload integrity.
  - Probar que un run bound continúa tras publish/rollback/drift sin consultar
  ni sustituir el active head.

- [x] T12. Transacción causal con workflow/control plane [R11, R12, R13, R16]

  - Añadir tests verticales que inyecten crash en cada boundary entre binding,
  run, first event/job e ingress receipt.
  - Integrar binding obligatorio en `validationworkflow` y en la unidad causal
  PostgreSQL de feature 35, preservando receipts/recovery.
  - Implementar frontera equivalente local y rechazar todo run/job/environment/
  model call si el binding no se confirmó.
  - Adaptar recovery para cargar siempre el binding del run y nunca el head.

- [x] T13. Adapters de recursos y separación de bootstrap [R4, R5, R6, R16]

  - Crear tests para adapters de contract catalog, environments, identities,
  fixtures, runtime/model/auth profiles, approval, limits y retention.
  - Construir `ResourceCatalogSnapshot` solo con descriptors no secretos y
  relaciones/digests verificables.
  - Reducir `workflowConfig` a bootstrap privado de composición; migrar toda
  selección project-owned al effective configuration sin fallback por flags/env.
  - Mantener secret resolver y credential env mappings únicamente en la
  frontera privada, resolviendo refs just-in-time para el binding/fence correcto.

- [x] T14. Superficies CLI, API/MCP y GitHub [R1, R10, R12, R16]

  - Probar comandos administrativos validate/publish/rollback/show/history/
  export sobre el mismo service y authority, sin escrituras directas al store.
  - Integrar admisión CLI, API/MCP y GitHub derivando project key de identidad
  confiable antes de consultar configuración.
  - Rechazar overrides que añadan contracts/providers/identities/fixtures,
  profiles, secret refs o límites fuera del snapshot efectivo.
  - Cubrir redelivery de webhook/API y ausencia de trabajo ante configuración
  ausente, inválida, obsolete o incompatible.

- [x] T15. Consumers de límites, approval, retención y refs [R5, R6, R11, R12, R16]

  - Añadir tests que demuestren que planning/execution/evaluation/publication/
  cleanup usan límites, approval y retention del binding.
  - Eliminar defaults productivos implícitos en prompt/composición para esos
  consumers y conservar únicamente límites defensivos de proceso.
  - Probar old-run recovery y que secret ref revocada produce fallo operacional
  sin fallback, nueva identity o mutación del binding.

- [x] T16. Verificación y cierre [R1–R18]

  - Completar matriz `R<n>` ↔ tests concretos y ejecutar `gofmt`, diff-check,
  `go vet`, compile-only, `go test ./...`, race y cross-builds aplicables.
  - Ejecutar contract suites local/PostgreSQL y gate PostgreSQL 16 aislado:
  migrations, concurrency, restart, rollback, unknown commit y causal binding.
  - Medir statement coverage exacta del diff (mínimo 85%, sin redondeo) y
  añadir tests donde falte evidencia, no exclusiones artificiales.
  - Auditar el diff completo para la convención de comentarios sistémicos,
  incluidos símbolos privados y helpers, evitando comentarios mecánicos.
  - Confirmar ausencia de secretos/canaries, red y reloj real en unit tests;
  documentar comandos y resultados en `progress/current.md` durante ejecución.
  - Solicitar revisión independiente de trazabilidad, invariantes, frontera
  feature 37 y gate; solo el leader podrá marcar `done` tras aprobación.
