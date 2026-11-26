# Historial y comparación de validaciones — Tasks

- [ ] T1. Definir modelos, límites y errores del historial. [R2, R3, R9, R10, R13, R15, R16]
  - Escribir tests de IDs, timestamps UTC, resultados cerrados, defensive copies,
    cardinalidades, overflow, usage opcional y cantidad decimal/moneda/provenance.
  - Implementar `HistoryEntry`, query, page, usage, money, availability y errores
    tipados sin precio ni provider DTO en core.
  - Comentar cada entidad/función/helper tocado explicando su rol o invariante
    sistémico, además de Go doc para exportados.

- [ ] T2. Implementar autorización y validación de consultas antes del store. [R1, R2, R13, R15, R16]
  - Añadir fakes que demuestren zero store/reader calls ante identidad ausente,
    capability obsoleta, deny, filtros inválidos o scope cross-tenant.
  - Integrar acciones/resource identities cerradas de tenant security para
    list/get/compare/aggregate/evidence sin confiar en IDs del request.
  - Probar reautorización en cada página y mapping público no-oracle.

- [ ] T3. Implementar cursor sellado y contrato de keyset estable. [R5, R13, R15, R16]
  - Probar HMAC, scope, filter digest, snapshot, versión, posición, TTL, tamaño,
    tampering y key rotation con clock controlado.
  - Fijar orden `terminal_at DESC, run_id DESC`, detached pages y `limit+1`
    acotado, sin fallback offset/página 1.
  - Cubrir timestamps iguales e inserción concurrente entre páginas sin
    duplicados u omisiones.

- [ ] T4. Implementar proyección segura de terminal y receipts idempotentes. [R3, R4, R9, R10, R13, R15, R16]
  - Escribir tests terminal funcional/operativo, replay igual, conflicto,
    binding/plan/package roto, cancelación y unknown commit.
  - Verificar package/approval/revision/configuration, canonizar constraints,
    plan seguro, evidence refs y usage; adjuntar money solo si viene confirmada.
  - Confirmar entry + receipt atómicamente o mediante intent/reconcile durable,
    sin callbacks bajo lock/transacción.

- [ ] T5. Adaptar el lector verificado a la proyección de plan y package. [R3, R4, R11, R13, R16]
  - Extender mediante un puerto consumer-owned la vista verificada de package,
    plan aprobado e inventory sin exponer paths o bytes no saneados.
  - Probar manifest/digest/run/binding/evidence/plan foreign, campos desconocidos,
    N/N+1 bytes/entries y canaries.
  - Mantener `localvalidation.ReadPackage` como única entrada semántica; no
    introducir un parser histórico alternativo.

- [ ] T6. Implementar store local tenant-scoped y contract base. [R3, R5, R12, R13, R14, R16]
  - Añadir manifest/checksums, scope-derived paths, lock multiproceso,
    temp+fsync+rename, receipts y reload fail-closed.
  - Cubrir restart, same IDs en tenants, corrupción, concurrency, filtros
    combinados, snapshot/keyset y backend unavailable sin fallback.
  - Implementar retention intent/tombstone local y comprobar referencias vivas.

- [ ] T7. Añadir migration y `HistoryStore` PostgreSQL. [R3, R5, R12, R13, R14, R15, R16]
  - Crear migration append-only posterior a `0023` con keys/FKs/predicates
    compuestas, receipts, correction links, evidence tombstones e índices
    keyset/filtros.
  - Implementar transacciones serializables/retry completo, source digest,
    isolation application-level y RLS solo como defensa adicional.
  - Probar empty/latest/concurrent/dirty/future/checksum, compatibility N/N+1,
    restart, unknown commit y ausencia de dual-write/fallback.

- [ ] T8. Ejecutar el contrato compartido de listado y filtros. [R1, R2, R5, R13, R14, R16]
  - Ejecutar los mismos fixtures local/PostgreSQL para filtros individuales y
    combinados de repository, PR, commit, feature, result y periodo `[from,to)`.
  - Verificar orden/páginas idénticos, cursor cross-scope/filter rechazado,
    snapshot bajo inserts y reautorización antes de cada store call.
  - Cubrir periodo/límite máximo, datasets vacíos y cancelación durante scan.

- [ ] T9. Implementar lineage de correcciones append-only. [R3, R4, R12, R14, R16]
  - Adaptar solo IDs/outcome/digests confirmados desde correction journal y
    confirmar relación + receipt sin mutar la entry base.
  - Probar parent/child foreign, replay/conflict, attempts lineales, restart,
    retention references y canaries en selección/errores.
  - Mantener separados el comparator de correction y el de historial cuando
    difiera su identidad/semántica.

- [ ] T10. Implementar resolución de selectores y comparación pura. [R6, R7, R13, R15, R16]
  - Probar run exacto, commit con múltiples terminales, commit ausente y lados
    incompatibles por tenant/project/repository/feature.
  - Cubrir exhaustivamente `corrected`, `persistent`, `new`, `retired` e
    `inconclusive`, incluida regresión `passed -> failed`, fingerprints
    divergentes, duplicados y estados inconclusive.
  - Verificar orden/digest estable bajo permutaciones, límites N/N+1,
    cancelación y cero llamadas al modelo.

- [ ] T11. Implementar agregados de fallos, duración y estabilidad. [R8, R13, R15, R16]
  - Escribir tests de counts/ratios enteros, transitions comparables, empty set,
    min/max/sum/percentiles versionados, overflow y datos corruptos.
  - Iterar páginas internas sobre un único snapshot/work budget y excluir scope,
    filtros, retención e inconclusive según contrato explícito.
  - Probar que texto narrativo, logs y terminales operativos sin constraints no
    alteran estadísticas funcionales.

- [ ] T12. Agregar usage y coste opcional sin pricing en core. [R9, R10, R13, R16]
  - Probar calls/tokens/duration confirmados, dimensiones ausentes, counters
    decrecientes/overflow y agrupación provider/model.
  - Añadir adapter opcional para atribución provider/config confirmada y probar
    cantidad/digest/binding inválidos, ausencia de cost y separación de monedas.
  - Incorporar un test/auditoría estática que falle ante rate tables, precios
    por modelo o cálculo tokens×precio dentro del core.

- [ ] T13. Implementar resolución autorizada de evidencia y availability. [R1, R4, R11, R13, R15, R16]
  - Probar available mediante manifest/reader, expired con tombstone, missing
    sin tombstone e invalid por corrupción/foreign/uninventoried ID.
  - Verificar redacción y byte limits, zero bytes parciales ante error y deny
    antes de locator/package reader.
  - Demostrar que cada estado de availability deja inmutable resultado,
    constraints y comparisons históricos.

- [ ] T14. Integrar retention de metadata, packages y evidencias. [R11, R12, R13, R14, R16]
  - Aplicar policy tenant versionada y graph/batch bounds; proteger active/live
    refs y correction/comparison lineage retenido.
  - Probar intent/fencing/audit/receipt, crash/restart, unknown delete,
    concurrent reference, cancellation y aislamiento cross-tenant.
  - Confirmar package expiry antes de entry, metadata expiry en snapshots nuevos
    y tombstone suficiente para distinguir `expired` de `missing`.

- [ ] T15. Integrar superficies productivas y observabilidad segura. [R1, R2, R4, R15, R16]
  - Componer `HistoryService` con identidad/RBAC/store/reader/clock/limits en las
    superficies existentes que expongan el caso de uso, sin duplicar lógica.
  - Probar paridad del contrato de aplicación, errors `errors.Is/As`, observer
    ausente/error/panic y labels de cardinalidad cerrada.
  - Ejecutar auditoría de canaries sobre entradas, errors, events, audit,
    logs/metrics y adapters; conocer IDs/cursor no debe revelar existencia.

- [ ] T16. Ejecutar verticales adversariales y verificación trazable de cierre. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11, R12, R13, R14, R15, R16]
  - Cubrir flujo terminal→history→list/get→compare→aggregate→evidence→retention,
    acceso cruzado, cursor spoof, package expired/corrupt, cost opcional,
    concurrency y recovery.
  - Ejecutar format, vet, compile-only, global normal/race, focales race
    repetidas, builds aplicables y PostgreSQL 16 normal/race/N→N+1 con cleanup.
  - Generar coverage diferencial exacta según `docs/verification.md`, manifest
    completo y mapa `R<n> → test → resultado` en
    `progress/execution-history/implementation.md`.
  - Ejecutar auditoría AST de todas las entidades/funciones/métodos/helpers Go
    nuevos o modificados y revisión semántica de comentarios; rechazar
    comentarios ausentes o que solo repitan el identificador.
