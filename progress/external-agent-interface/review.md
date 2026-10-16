# Tercera revisión independiente — external-agent-interface

## Veredicto

**Aprobado.** No quedan hallazgos bloqueantes. REV-1–REV-5 están corregidas y
la implementación satisface R1–R10 y T1–T8 con pruebas concretas.

## Precondiciones

- Existe exactamente una feature `in_progress`: `external-agent-interface`.
- La aprobación humana está registrada en `requirements.md`.
- T1–T8 están marcadas `[x]`.
- Existe un informe de implementación actualizado.
- Esta revisión solo modifica `progress/external-agent-interface/review.md`.

## Cierre de hallazgos

### REV-1 — Cerrada

El registro persiste un journal ordenado junto al snapshot en la misma
publicación atómica. Reserva, inicio, cancelación solicitada, fallo de
publicación, recuperación y terminal conservan una secuencia monótona. Recovery
añade `recovered` y `terminal` con causa cerrada `interrupted` sin perder el
prefijo confirmado. Las pruebas reabren el registro y comparan eventos y estado.

### REV-2 — Cerrada

Discovery publica esquemas Draft 2020-12 cerrados y satisfacibles para los
envelopes y todos los DTO anidados. Un validador independiente compila los
esquemas y valida respuestas reales de CLI y MCP, tanto de datos como de error.

### REV-3 — Cerrada

El worker se retira del supervisor y libera su lease antes de cerrar `done`.
Pruebas con cleanup bloqueado demuestran que `Wait`, `Cancel` y `Close` realizan
un join real y no retornan anticipadamente.

### REV-4 — Cerrada

`TestCLIAndMCPFullContractTable` ejecuta fixtures aislados y equivalentes por
ambos adapters. Incluye `invalid_request`, `unauthorized` tras revocar scope,
submit, replay/conflicto de submit, cancelación, replay de cancelación con la
misma clave, conflicto al reutilizarla con otra petición, paquete válido y
corrupto, principal ajeno, `in_progress` y los cinco estados terminales. Cada
paso compara el `Envelope` y lo valida contra el esquema descubierto por su
propia superficie.

### REV-5 — Cerrada

La secuencia posterior al executor crea un único contexto de finalización nuevo
y acotado antes de `MarkPublicationFailure`; ese contexto gobierna el evento y
ambas transiciones restantes. Un fallo rápido continúa hacia terminal `error`.
Un bloqueo termina por deadline, el worker se retira, la lease se libera y el
snapshot no terminal queda recuperable como `error/interrupted`. Las pruebas
cubren modos bloqueado/fallido a través de `Wait`, `Cancel` y `Close`, verifican
que no queden callbacks activos ni workers/goroutines registrados y comprueban
la lease con el lock OS real.

## Trazabilidad verificada

| Requirement | Implementación y prueba principal | Resultado |
|---|---|---|
| R1 | servicio, registro durable, lifecycle/restart/journal | pass |
| R2 | `Service.Invoke`, parity y tabla contractual CLI/MCP | pass |
| R3 | `ReadPackage`, proyección acotada y casos hostiles | pass |
| R4 | identidad confiable, scopes, catálogo y executor aprobado/fail-closed | pass |
| R5 | reautorización por operación y aislamiento de handles/evidencia | pass |
| R6 | errores cerrados, estados terminales y tabla de adapters | pass |
| R7 | CAS, idempotencia concurrente, multiproceso y reinicio | pass |
| R8 | cancelación, timeout, join, leases, crash y recovery | pass |
| R9 | CLI capabilities, MCP 2026-07-28, discovery y schemas | pass |
| R10 | `TestCLIAndMCPFullContractTable` | pass |

T1–T8 están completas y cada requirement tiene al menos un test que ejercita el
comportamiento observable y sus errores relevantes.

## Seguridad y arquitectura

- `clientInfo` y `_meta` permanecen como datos sin autoridad. La identidad y
  los scopes se resuelven desde la frontera confiable en cada operación.
- La petición acepta aliases cerrados, nunca paths, URLs, comandos, targets,
  credenciales ni presupuestos arbitrarios. El catálogo protegido resuelve el
  valor interno por principal.
- Un handle no concede autoridad por posesión; principal y scope se comprueban
  de nuevo antes de estado, resultado o cancelación. Ajeno, desconocido,
  malformado y caducado no revelan existencia.
- Idempotencia se particiona por principal y operación, persiste hashes y usa
  lock multiproceso, CAS, fsync/rename y leases de ownership.
- El supervisor no mantiene su mutex durante callbacks o I/O y espera sus dos
  goroutines. Cancelación local/remota, timeout, cierre y crash tienen salida y
  recuperación explícitas.
- El resultado se obtiene únicamente desde un paquete verificado. La proyección
  elimina paths, targets, cuerpos y texto libre, redacta valores no escalares y
  transforma evidencia en handles opacos ligados al run.
- CLI y MCP llaman al mismo caso de uso. MCP mantiene framing acotado,
  discovery determinista, schemas cerrados, errores JSON-RPC separados de los
  de aplicación y rechazo explícito de `initialize` y versiones distintas.
- La composición productiva no concede scopes ni aliases y usa
  `FailClosedExecutor`; no puede ejecutar herramientas, red, modelo ni resolver
  credenciales hasta disponer de una composición confiable.
- El código conserva dirección de dependencias, comentarios de responsabilidad,
  contextos propagados y ausencia de secretos o accesos reales.

## Verificación fresca

| Comando | Resultado |
|---|---|
| `go test -count=1 ./internal/externalagent ./cmd/featureproof` | pass |
| `go test -count=20 ./internal/externalagent ./cmd/featureproof` | pass |
| `go test -race -skip '^TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors$' -count=1 ./...` | pass |
| `go test -race -count=20 ./internal/externalagent ./cmd/featureproof` | pass |
| coverage `internal/externalagent` | 88,9 % |
| coverage global comparable | 88,7 % |
| `go vet ./...` | pass |
| `go mod verify` | pass |
| compile-only `go test -run '^$' ./...` | pass |
| formato dirigido y `git diff --check` | pass |
| builds Windows/Linux/macOS amd64 con `CGO_ENABLED=0` | pass |

## Test ambiental preexistente

El comando literal `go test -count=1 ./...` falla únicamente en
`TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`,
sin cambios desde feature 15. Este Windows deniega `os.Symlink` y el test llama
`Skipf` sobre el `testing.T` padre, por lo que Go convierte la omisión prevista
en fallo. La primera revisión compiló el mismo binario de tests, sin modificar
el código, y ejecutó exactamente ese subtest bajo WSL2; pasó. Todas las demás
pruebas globales, incluido race, pasan en Windows. Se considera una limitación
ambiental/preexistente verificada, no un defecto de feature 16.

## Conclusión

La feature puede transicionar de `in_progress` a `done`.
