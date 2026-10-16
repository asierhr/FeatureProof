# Validación vinculada a una revisión — Design

## Resumen

Un caso de uso de admisión resuelve la revisión Git y exige una atestación confiable del entorno antes de planificar o ejecutar. Construye un `RevisionBinding` inmutable con hashes de proyecciones no secretas. La aprobación del plan, si está habilitada, añade el digest del lote autorizado antes de ejecutar; los replans añaden digests de lote al mismo run. El journal es la fuente de verdad y el publisher verifica la consistencia de todo el bundle antes del commit atómico.

```text
solicitud explícita ──> resolver Git ──> atestar entorno ──> binding base
                                                       │
                           plan preparado/aprobado ──> digest de lote
                                                       │
                        herramientas ──> evidencia ──> journal
                                                       │
                       verificación cruzada ──> bundle terminal
```

## Fuentes de contexto y contratos observados

- `feature_list.json`, feature 14: ocho criterios sobre identidad, hashes, discrepancias, dirty tree, propagación, secretos, runs nuevos y tests.
- `progress/history.md`: features 1–13 completadas; feature 13 está en el working tree local y no se modifica.
- `docs/architecture.md`: ejecución bajo demanda, dominio independiente de adapters, evidencia inmutable, journal append-only, veredicto determinista y publicación atómica.
- `docs/conventions.md`: interfaces en consumidor, dependencias inyectadas, contexto/cancelación, errores tipados y pruebas herméticas.
- `docs/specs.md` y `.codex/agents/spec.md`: EARS, tres artefactos, trazabilidad y puerta de aprobación humana.
- Contratos actuales: `internal/localvalidation/{command,runner,model,publisher}.go` reciben spec/plan/target, ejecutan secuencialmente y publican `run.json`, `events.jsonl`, `plan.json`, evidencia y reportes; `internal/harness/{state,journal}.go` poseen run y eventos; `internal/evidence/store.go` sella records y no sustituye IDs; `internal/planapproval/{model,canonical,workflow}.go` calcula fingerprints de plan/contexto y entrega autorización de un uso; `internal/replanning/{coordinator,ledger,journal}.go` añade lotes y lineage. Los fingerprints internos de aprobación no se publican automáticamente si pudieran depender de secretos.
- Contratos externos: Git identifica un commit por su OID completo dentro de un repositorio; un ref de rama puede moverse y un working tree puede contener cambios fuera del commit. No se presupone un proveedor concreto de entornos ni un endpoint externo. El consumidor exige una atestación verificable de procedencia, identidad y commit desplegado.

## Componentes y responsabilidades

| Componente propuesto | Responsabilidad |
|---|---|
| `internal/revisionbinding` | Modelo `RevisionBinding`, invariantes, proyección canónica versionada, digests, verificación y categorías de error. Interfaces mínimas `RevisionResolver` y `EnvironmentAttestor` en el consumidor. |
| Adapter Git local | Resuelve un repositorio autorizado a una identidad canónica estable y ref a OID completo de objeto commit; inspecciona dirty state sin enviar paths al dominio. Lee solo; no hace checkout ni fetch implícito. |
| Composición de admisión | Recibe selección explícita de repositorio/revisión/entorno y fuente confiable de attestation. Fija binding antes de generar ID ejecutable, planificar o ejecutar. Donde no hay fuente confiable, rechaza el run con diagnóstico seguro. |
| Puente aprobación/plan | Obtiene el plan preparado vigente y, si se exige aprobación, consume solo la autorización exacta. Emite digest de proyección segura de cada lote. No toma autoridad de la vista humana ni de la salida del modelo. |
| `harness` / `replanning` | Mantienen binding base inmutable, añaden eventos de vinculación y de lotes y preservan el lineage de la feature 13. No alteran assertions ni presupuesto. |
| `evidence` | Añade referencia/digest del binding a metadatos sellados y comprueba run/binding al recuperar. No persiste payload de atestación. |
| `localvalidation` publisher/reporting | Proyecta binding a artefactos seguros; compara binding de snapshot, eventos y evidencia; conserva el commit no-replace. |

## Contrato de datos

`RevisionBinding/v1` contiene `run_id`, `repository_id` (identidad configurada y verificada, no URL con credenciales), `commit_oid` completo, `environment_id`, `attestation_ref` opaco, `constraints_sha256`, `contract_sha256`, `configuration_sha256` y `binding_sha256`. Este binding base y su digest son inmutables. Cada lote produce un `PlanBinding/v1` append-only con `binding_sha256`, orden de lote, `plan_sha256` y modo `approved`/`approval_not_required`; el reporte final lista la secuencia confirmada. El digest base usa un envelope versionado sin el propio digest. El orden de constraints, checks y lotes respeta semántica; las claves de mapas se serializan canónicamente. Campos y límites son explícitos y se validan antes de calcular digests.

Los hashes son de **proyecciones semánticas seguras**: constraints normalizadas; plan preparado que realmente se ejecutará con credenciales representadas por IDs opacos no secretos; contrato OpenAPI relevante retenido, target, capabilities y policy aplicables; configuración efectiva no secreta, incluyendo versiones y límites. El hash público puede diferir del fingerprint privado de aprobación. Se conserva una relación verificable entre ambos mediante el plan preparado autorizado, sin serializar secretos. Si no existe proyección segura completa para un plan, la admisión falla cerrada.

Eventos nuevos de tipo `binding_confirmed` y `plan_binding_confirmed` llevan binding ID/digest y categoría de lote, no payload sensible. Los eventos existentes conservan run ID y heredan binding por una referencia explícita en su proyección persistida. Cada record de evidencia sellado lleva `run_id` y `binding_sha256`; los reportes muestran identidad y hashes, no atestación cruda. El esquema versionado permite distinguir bundles anteriores que carecen de esta garantía; no se reinterpretan retroactivamente.

## Flujo principal

1. Validar entradas no secretas y resolver el repositorio/revisión seleccionados; rechazar dirty state, OID parcial/no-commit o repositorio ajeno.
2. Consultar atestador confiable para el entorno seleccionado; comprobar procedencia, vigencia y coincidencia exacta de repositorio y OID.
3. Construir binding base con snapshot de constraints, contrato y configuración; confirmar evento de admisión antes del primer trabajo funcional.
4. Preparar plan; con aprobación obligatoria, esperar la autorización exacta y comprobar de nuevo inputs y attestation antes de ejecutar. Añadir hash del plan ejecutable y evento confirmado. Sin aprobación, registrar explícitamente el modo.
5. En cada replan, fijar nuevo digest de lote preparado/aprobado y mantener el binding base. Antes de nueva ejecución comprobar que la revisión/entorno no cambió. Fallar cerrado ante deriva.
6. Crear evidencia con la misma vinculación. Al finalizar, el publisher reconstruye y compara IDs/digests en eventos, evidencia, run y reportes; confirma el bundle atómicamente solo si todo coincide.

## Flujos de error y estados

- Ref inválido, commit inexistente, dirty tree o atestación ausente: rechazo operativo de admisión, sin conclusión funcional. Si el run ya tiene ID/journal confirmado, finaliza como `inconclusive`/error con causa segura.
- Commit desplegado distinto o deriva después de admisión: detener nueva ejecución y finalizar `inconclusive` con `revision_mismatch`; no convertir assertions ya confirmadas ni publicar `passed` sobre un entorno distinto.
- Cambio de plan, constraints, contrato o configuración frente a binding/autorización: invalidar la autorización según feature 12, volver a preparar/aprobar cuando proceda o detener con conflicto; nunca usar el hash anterior para el plan nuevo.
- Hash no reproducible, evento o evidencia ajenos, falla de escritura: error de integridad/publicación, bundle final ausente y staging diagnóstico `incomplete` según contrato local existente.

## Estado, concurrencia y cancelación

La composición posee el binding base hasta confirmarlo. Después el run/journal posee una copia inmutable; adapters solo devuelven snapshots. La secuencia de lotes se serializa con la misma revisión CAS/idempotency de `replanning`. No se mantiene un lock durante Git, atestación, hashing de I/O, aprobación o filesystem. `context.Context` atraviesa cada operación bloqueante; cancelación no inicia trabajo nuevo y permite publicar únicamente estado ya confirmado bajo la finalización acotada existente.

## Retries e idempotencia

Resolver/atestar son operaciones de lectura y se pueden repetir solo bajo la política de retries existente y el mismo contexto. Cada relectura debe coincidir exactamente; no se actualiza una vinculación confirmada. Un evento o evidencia repetidos con igual idempotency key y bytes canónicos se reconoce como replay, mientras un payload diferente es conflicto. Crear una corrección usa ID nuevo y publicación no-replace.

## Observabilidad y seguridad

Eventos/logs exponen `run_id`, binding digest, IDs canónicos seguros, categorías (`unknown_commit`, `dirty_worktree`, `unverified_environment`, `revision_mismatch`, `binding_integrity`) y fase. No exponen paths Git, URLs con userinfo, claims de atestación ni contenido de secretos. Los errores se clasifican por tipo; no se comparan mensajes externos. Tests con canarios recorren staging y todos los artefactos publicados.

## Estrategia de pruebas y trazabilidad

| Requirement | Componente | Tests previstos |
|---|---|---|
| R1 | Resolver Git/admisión | `TestResolveFullCommitAndFreezeMovedRef`; `TestRejectMissingAmbiguousOrForeignCommit` |
| R2 | Resolver Git/admisión | `TestDirtyWorktreeNeverAttributedToHEAD` |
| R3 | Atestador/admisión | `TestEnvironmentMustAttestExactRepositoryAndCommit`; `TestAbsentOrUntrustedAttestationStopsExecution` |
| R4 | Canonicalización/approval bridge | `TestBindingHashesAreStableAndSensitiveToSemanticChanges`; `TestStaleApprovalCannotBindChangedPlan` |
| R5 | Journal/evidence/publisher | `TestPublishedArtifactsShareBinding`; `TestForeignBindingPreventsBundleCommit` |
| R6 | Publisher/caso de uso | `TestCorrectionCreatesNewRunWithoutChangingPriorBundle` |
| R7 | Proyección segura | `TestSecretCanariesNeverEnterBindingOrPublicDigests` |
| R8 | Fronteras y loader | `TestBindingCancellationAndTamperFailClosed` |

Tests de integración usan repositorio Git temporal sin remoto, fake atestador, fake plan/approval, reloj/IDs controlados y filesystem inyectable. `go test ./...`, `go test -race ./...`, `go vet ./...`, builds de plataformas admitidas y cobertura de paquetes tocados son gates según `docs/verification.md`; no se ejecuta entorno real en tests ordinarios.

## Alternativas y decisiones

- Aceptar un SHA suministrado por el usuario como prueba del despliegue: descartado; no demuestra qué está desplegado.
- Incorporar dirty state al hash de commit: descartado en v1; los criterios permiten rechazo y evita atribución falsa.
- Usar un ref mutable como identidad: descartado; se resuelve a OID antes de ejecutar.
- Publicar el fingerprint interno de aprobación sin revisar sus inputs: descartado; podría depender de secretos. El digest público se deriva de proyección segura y se verifica contra el plan autorizado.
- Construir el adapter preview de producción aquí: descartado; corresponde a feature 19. El contrato consumidor y el fallo cerrado sí pertenecen a esta feature.
- Reescribir bundles de runs antiguos: descartado; preserva evidencia histórica.

El diseño sigue las fronteras de `docs/architecture.md` y las interfaces, cancelación, errores y nombres de `docs/conventions.md`; no solicita excepción. Toda entidad o función nueva/modificada debe documentar su responsabilidad sistémica como establece el precedente de la feature 13.
