# Quinta revisión independiente — Feature 20

## Resultado

**approved**

REV-14 queda cerrada. La admisión y la recuperación ya no aceptan IDs o digests parciales como sustituto de los objetos efectivos. `BindingVerifier` devuelve `BindingContext`, `DescriptorCatalog` devuelve descriptors más `RuntimeContext`, y `TrustedAdmissionBuilder` canonicaliza esos objetos y deriva internamente `RevisionDigest`, `TargetDigest` y `RuntimeDigest`.

La revisión directa del código y las pruebas confirma que el contexto canónico liga specification, contrato reducido y bytes OpenAPI, revisión, target directo o preview, attestation y vencimiento, manifest HTTP, redacción, registry, schema, versiones de validators, policy, red, proveedor/modelo, constraints, capabilities y límites. `validateSnapshot` recompone los digests antes de reservar/generar; `restorePrepared` vuelve a construir la admisión actual y `verifyExecutableContext` recompone y contrasta el contexto contra artefacto y material privado antes de Restore, Stage/Restage, decisión/consumo de approval o tools.

No quedan blockers abiertos ni aparecieron regresiones en REV-1–REV-13.

## Cierre de REV-14

- `internal/validationworkflow/admission.go`: las fronteras retornan objetos efectivos. El builder calcula los hashes tras canonicalizarlos, valida su completitud y solo compara el `TargetDigest` resultante con la selección confiable; ningún digest de binding/runtime recibido reemplaza el contenido.
- `internal/validationworkflow/context.go`: la representación de target contiene kind, alias, URL, IDs de provider/environment/profile, EnvironmentID, reservation, observed version, fingerprint, attestation completa y network rules. La representación runtime contiene el manifest HTTP completo, redaction, registry, schema/reference, versiones de tool/assertion validator, policy y network rules. Los conjuntos se ordenan y los timestamps se llevan a UTC antes del hash.
- `cmd/featureproof/workflow.go`: la composición productiva construye esos objetos desde la configuración y los componentes ejecutables. Para preview, la proyección incorpora la reserva sellada; `previewenv.VerifySnapshot` verifica el fingerprint sobre el snapshot completo, incluidos versión y availability, antes de que se proyecten sus campos. La configuración preview y la configuración workflow deben coincidir exactamente en repositorio, commit, EnvironmentID, target, attestation y vencimiento.
- `internal/validationworkflow/artifact.go`: `canonicalContextDigest` incorpora tanto los objetos Binding/Runtime como todos los demás campos que gobiernan planificación y ejecución.
- `internal/validationworkflow/service.go`: `validateSnapshot` deriva nuevamente los tres digests. En recovery, `restorePrepared` recompone la admisión; para preview restaura el snapshot sellado y exige el mismo digest en record y artefacto. `verifyExecutableContext` vuelve a derivar binding/runtime/context y los compara con snapshot, artefacto y material privado antes de cualquier frontera sensible.
- Required, disabled recovery, `PlanReady` y cambio de owner están cubiertos. Los tests conservan alias/profile/policy ID y alteran URL, EnvironmentID, attestation, `validUntil`, HTTP, redacción, registry, schema, validators, policy y red; ninguna variación alcanza Stage, approval ni ejecución.

## Estado de REV-1–REV-14

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-1 | cerrado | `PlanApprovalBridge` usa `planapproval`, hashes exactos y grant opaco de un uso. |
| REV-2 | cerrado | approve/reject/cancel reclaman ownership liberado mediante CAS entre owners distintos. |
| REV-3 | cerrado | heartbeat renueva leases, propaga cancelación y se une; race repetido pasa. |
| REV-4 | cerrado | record+journal se publican como checkpoint único y failure injection no divide el estado. |
| REV-5 | cerrado | package, finalization y cleanup conservan intents durables y recuperables. |
| REV-6 | cerrado | preview restaura snapshot/target/attestor sellados y libera same/cross owner sin reservar de nuevo. |
| REV-7 | cerrado | descriptor/validator/policy y runner son productivos y se revalidan entre batches. |
| REV-8 | cerrado | cada valor y ubicación usa HMAC con clave privada por run; artifact y package comparten proyección. |
| REV-9 | cerrado | material privado sellado se contrasta con artifact/context/plan antes de approval/tools y se elimina al terminar. |
| REV-10 | cerrado | `plan_ready` restaura y stagea/restagea exactamente o conserva approval disabled. |
| REV-11 | cerrado | deadline durable e inclusivo en Decide, recovery y claim cross-owner. |
| REV-12 | cerrado | `RestoreRun` recibe y respeta el contexto cancelable original. |
| REV-13 | cerrado | callers sin clave, incluido `validate --plan`, no publican commitments reproducibles; el workflow exige clave aleatoria privada de 32 bytes. |
| REV-14 | cerrado | Binding/Runtime efectivos se canonicalizan, derivan y recomponen antes de Restore/Stage/approval/tools en todas las ramas revisadas. |

## Trazabilidad R1–R13 y tasks

| Requirement | Estado | Evidencia revisada |
|---|---|---|
| R1 | conforme | Selecciones explícitas, sin modelo por defecto; el comando histórico mantiene parser, ejecución y códigos. |
| R2 | conforme | Preflight construye spec, contrato, binding, target/attestation, runtime/policy/network y budgets antes del modelo; preview se sella y verifica. |
| R3 | conforme | `agentplanning.Generator` y preparación all-or-nothing validan schema, provenance, policy y budgets sin tools. |
| R4 | conforme | Missing context es terminal seguro, acotado y sin approval/package/tools. |
| R5 | conforme | Artefacto canónico/atómico, material privado y package comparten plan y bindings verificables. |
| R6 | conforme | Required usa `planapproval` exacto; disabled requiere policy explícita; drift impide Stage/Consume/ejecución. |
| R7 | conforme | El runner histórico es la autoridad de ejecución/package y revalida binding/attestation/policy entre batches. |
| R8 | conforme | Estados/outcomes son cerrados y el package verificado es la única autoridad funcional. |
| R9 | conforme | Idempotencia, checkpoint, journal, CAS, leases, recovery e intents evitan repetir inferencia o tools inciertas. |
| R10 | conforme | Cancelación, deadlines, cleanup con contexto propio y join de workers/heartbeat están cubiertos. |
| R11 | conforme | OAuth/credenciales permanecen en composición; clave de commitments y valores ejecutables permanecen privados; canarios públicos pasan. |
| R12 | conforme | API Go, CLI workflow, MCP y GitHub comparten `Service` y store; `validate --plan` sigue compatible. |
| R13 | conforme | Fakes herméticos, trazabilidad, comentarios, cobertura mínima y race pasan salvo la excepción ambiental de symlink indicada abajo. |

T1–T12 están `[x]`, la spec conserva aprobación humana y `validation-workflow-orchestration` es la única feature `in_progress`.

## Verificación ejecutada

| Comando | Resultado |
|---|---|
| focales de drift/contexto/preview/material, `-count=20` | pass |
| `go test ./internal/validationworkflow ./internal/previewenv ./internal/planapproval ./cmd/featureproof -count=10` | pass |
| race focal de drift, preview, leases y cancelación, `-count=3`, GCC/UCRT64 | pass |
| race de `validationworkflow`, `previewenv`, `planapproval`, `localvalidation` y command | todos pass salvo el subtest ambiental de symlink |
| coverage `internal/validationworkflow` | 85,8 % statements |
| coverage `internal/previewenv` | 85,0 % statements |
| `go test ./... -count=1` | todos los paquetes pasan salvo `internal/localvalidation/TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink` |
| focales de compatibilidad/oracle histórico, `-count=10` | pass |
| test AST de comentarios sistémicos | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| builds linux/amd64 y darwin/amd64; compilación Windows cubierta por gates locales | pass |
| `go mod verify` / `go mod tidy -diff` | pass / sin diff |
| `git diff --check` | sin errores; solo avisos CRLF del checkout |

La única falla reproducida es la conocida de Windows al crear el symlink del subtest `evidence_symlink`: `A required privilege is not held by the client`. El test no fue omitido ni debilitado y el resto de su paquete se ejecutó. No se observaron secretos, valores privados, claves, credenciales ni canarios en superficies públicas.
