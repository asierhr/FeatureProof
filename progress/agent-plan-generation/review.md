# Tercera review independiente — Generación agéntica de planes de validación

## Veredicto

`approved`

La implementación satisface R1-R12 y T1-T8. REV-1 a REV-6 están corregidas,
las regresiones adversariales ejercitan el comportamiento real y la
verificación canónica completa pasa sin red ni credenciales.

## Precondiciones

- `agent-plan-generation` es la única feature `in_progress`.
- La spec contiene aprobación humana de 2026-10-15.
- T1-T8 están `[x]`.
- Existe un informe del implementer actualizado con trazabilidad R→test.
- El cambio permanece dentro del alcance de generación de planes.

## Resolución de hallazgos

| Hallazgo | Resultado | Evidencia |
|---|---|---|
| REV-1 | corregido | El recorrido estructural cubre inputs, path params y assertions; rechaza URL/userinfo, comandos, credenciales y capabilities antes de registry/policy y no filtra canarios. |
| REV-2 | corregido | `validationWork` cuenta nodos profundos y anchos, referencias/path, assertions, evidence y fingerprint mediante resta previa overflow-safe, límite exacto, profundidad 128 y checks de contexto. |
| REV-3 | corregido | Hay cancelación entre unidades y comprobaciones post-return de model, planning/assertions, registry y policy. Ningún éxito sobrevive a cancelación. |
| REV-4 | corregido | La cadena causal pública conserva solo `context.Canceled` y `context.DeadlineExceeded`; los errores externos se traducen a metadata segura. |
| REV-5 | corregido | El fingerprint es independiente de IDs y orden no semántico del DAG, evidence y maps; conserva diferencias semánticas y first-wins. |
| REV-6 | corregido | `observeTerminal` llama a `Observer.Observe` síncronamente con deadline finito. No crea goroutines y espera limpieza/escritura antes de que `Generate` retorne. |

## Revisión específica de REV-6

- `internal/agentplanning/generator.go:174-188` construye un contexto detached
  con timeout finito y realiza la llamada de observer en la misma goroutine.
- No existe `go` ni fire-and-forget en código productivo de
  `internal/agentplanning`.
- El contrato es cooperativo: el observer recibe `ctx` con deadline, termina su
  trabajo al vencerlo y retorna; `Generate` no puede finalizar hasta ese
  retorno.
- `observer-joined-deadline` comprueba que el contexto vence, que `Generate`
  permanece bloqueado durante la limpieza, que retorna fail-closed y que no
  queda trabajo ni escritura posterior.
- `observer-success-is-joined` comprueba que la escritura terminal se completa
  antes del retorno exitoso y permanece estable después.
- Las goroutines usadas por esos tests pertenecen al propio test y se esperan
  explícitamente con `generateDone`; no encubren trabajo de producción.

## Trazabilidad R1-R12

| Requirement | Estado | Verificación independiente |
|---|---|---|
| R1 | pass | Request acotada, snapshots y unión v1 estricta; duplicate keys, unknown fields y trailing data rechazados. |
| R2 | pass | Cobertura exacta de constraints y evidence requirements existentes preservados en `PreparedPlan`. |
| R3 | pass | Matriz positive/negative/error, `not_applicable`, canonicalización y first-wins sin debilitamiento. |
| R4 | pass | Operaciones retenidas y capabilities exactas; method/path derivados y valores prohibidos rechazados. |
| R5 | pass | Orden schema→semántica→materialización→planning→registry descriptor-only→policy, con short-circuit y resultado all-or-nothing. |
| R6 | pass | `insufficient_context` tipado, seguro, acotado y sin registry/policy. |
| R7 | pass | Cero handles ejecutables, ejecución, persistencia o mutación de run/aplicación. |
| R8 | pass | Una inferencia, sin retry/replan, presupuestos compartidos, límites profundos y cancelación completa. |
| R9 | pass | Instrucciones privilegiadas separadas de datos no confiables; payload inyectado validado determinísticamente. |
| R10 | pass | Categorías/stages/paths seguros, causas externas opacas y exactamente un terminal unido al ciclo de vida. |
| R11 | pass | Sesión neutral, Responses/SSE/accounting compartidos y protocolo del harness sin cambios semánticos. |
| R12 | pass | Tests herméticos, adversariales, concurrentes y trazables para R1-R11. |

## Arquitectura y convenciones

- La dependencia apunta de `agentplanning` a contratos internos; no incorpora
  SDKs de proveedor ni handles de tools.
- `ReducedContext` expone copias de procedencia solo para operaciones realmente
  serializadas.
- `PreparedPlan`, checks, evidence y `ValidatedPlan` preservan snapshots
  defensivos.
- No hay I/O bajo locks ni goroutines nuevas en la implementación.
- El adapter OpenAI mantiene instrucciones y datos no confiables en canales
  separados, y comparte el transporte autenticado y el decoder SSE.
- Todas las entidades y funciones nuevas o modificadas, incluidas internas y
  tests, tienen comentario funcional.

## Verificación reproducida

Entorno: Go 1.25.1, Windows; race detector con UCRT64.

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -count=1 -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=1 ./...` | pass |
| `CGO_ENABLED=1 go test -race -count=20 ./internal/agentplanning ./internal/model ./internal/model/openai ./internal/openapi ./internal/planning` | pass |
| `go test -count=1 -coverprofile <temp> ./...` | pass; 91,1 % global |
| Coverage afectado | `agentplanning` 91,0 %; `model` 96,5 %; `openai` 93,9 %; `openapi` 91,8 %; `planning` 89,5 % |
| `go mod verify` | pass |
| `go build ./...` Windows/Linux amd64/macOS amd64 | pass |
| `go test -c` de `internal/agentplanning` Linux/macOS amd64 | pass |
| `git diff --check` | pass; solo avisos LF/CRLF |

La cobertura de todos los paquetes modificados supera el 85 % y el baseline
global de 91,1 % se conserva. No se detectaron tests omitidos, sleeps de
sincronización, acceso a producción, secretos ni dependencias de Internet.
