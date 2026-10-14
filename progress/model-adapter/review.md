# Segunda review de model-adapter

## Veredicto

**Aprobada.** REV-1 a REV-4 están corregidas y cuentan con regresiones que
ejercitan el comportamiento requerido. No quedan hallazgos bloqueantes.

## Precondiciones

- `model-adapter` es la única feature en estado `in_progress`.
- La aprobación humana consta en `requirements.md` con
  `approved_by: user` y `approved_at: 2026-10-14T19:28:41+02:00`.
- T1-T8 están marcadas `[x]`.
- `progress/model-adapter/implementation.md` contiene la trazabilidad y la
  evidencia de las correcciones.
- El alcance continúa limitado a la feature 9.

## Revisión de correcciones

| Hallazgo | Evidencia revisada | Resultado |
|---|---|---|
| REV-1 | Runtime, adapter, SSE y fronteras OAuth priorizan cancelación/timeout, conservan `model.Error` seguro y normalizan solo fallos opacos. Los tests exigen categorías exactas en profile, browser/listener, exchange/refresh, admission, discovery y JWKS. El smoke compuesto con perfil inexistente devuelve `authentication`. | cerrado |
| REV-2 | `Usage.validTotal` comprueba valores negativos y overflow de input+output. La acumulación comprueba un segundo overflow, satura el consumo a `math.MaxInt64`, cierra la sesión y devuelve `invalid_response`; el máximo representable exacto se contabiliza y bloquea la siguiente llamada. | cerrado |
| REV-3 | El refresh coalesced posee contexto y timeout finitos independientes del iniciador, contabiliza waiters y cancela el trabajo común al desaparecer el último. La regresión cancela al iniciador con otro waiter activo, confirma un único exchange y entrega el token rotado al waiter. | cerrado |
| REV-4 | `LocalStore` usa una cola de turnos: el mutex protege únicamente contadores, cancelaciones y señales. `ReadFile`, creación, escritura, `Sync` y rename suceden fuera del mutex; la espera por turno observa contexto y el commit continúa siendo atómico. | cerrado |

## Trazabilidad R1-R12

| Requirement | Resultado de review |
|---|---|
| R1 | pass: provider/model/profile explícitos, límites validados, sin modelo por defecto y sin DTO del proveedor en el harness. |
| R2 | pass: OAuth dinámico, host ID, PKCE/state/nonce, issued client, scopes, identidad, perfiles aislados, refresh rotatorio y revocación. |
| R3 | pass: endpoint Responses fijo, bearer confinado, `store:false`, `stream:true`, input acotado y sin provider tools ni secretos. |
| R4 | pass: SSE acotado, `response.completed`, usage obligatorio, JSON único/exacto y decisión v1 validada. |
| R5 | pass: categorías distinguibles y metadata allowlisted; auth, cancelación y timeout se conservan de extremo a extremo. |
| R6 | pass: deadline mínimo, preflight sin trabajo externo y cancelación propagada durante auth, HTTP y SSE. |
| R7 | pass: exactamente un record terminal seguro por intento y observer fail-closed. |
| R8 | pass: reservas de calls/replans, tokens confirmados, duración y overflow fail-closed, sin pricing ni moneda. |
| R9 | pass: sin retry invisible; cada inferencia consume una reserva y el refresh concurrente usa un único exchange. |
| R10 | pass: el adapter no recibe registry/policy/evidence/run mutable ni ejecuta tools. |
| R11 | pass: suite hermética, determinista, sin `time.Sleep`, credenciales ni Internet, incluida bajo race. |
| R12 | pass: smoke explícitamente opt-in, separado de `go test`, con selección completa y salida segura. No se ejecutó contra OpenAI. |

## Verificación canónica fresca

Entorno: Go `go1.27.1`, Windows/amd64. Para race se habilitó CGO con el GCC ya
instalado en `C:\msys64\ucrt64\bin`.

| Comprobación | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test -count=1 ./...` | pass |
| `go test -race -count=1 ./...` | pass |
| `go test -race -count=20 ./internal/model/... ./cmd/featureproof-model-smoke` | pass |
| `go test -count=1 -coverprofile=<temp> ./...` | pass |
| `go tool cover -func=<temp>` | pass, global 91,1 % |
| Coverage `internal/model` | 97,2 % |
| Coverage `internal/model/openai` | 95,0 % |
| Coverage `internal/model/openai/oauth` | 88,4 % |
| Coverage `cmd/featureproof-model-smoke` | 87,3 % |
| `go mod verify` | pass |
| `go build ./...` | pass |
| build Linux amd64, CGO disabled | pass |
| build macOS amd64, CGO disabled | pass |

Todos los paquetes nuevos superan el 85 % y el coverage global permanece en
91,1 %. Los tests ordinarios no contactaron Internet ni usaron cuentas o
secretos reales. No se encontraron SDK del proveedor, API keys, pricing,
moneda, modelo por defecto, endpoint alternativo ni capacidades ejecutables en
el adapter. Las entidades y funciones nuevas o modificadas conservan los
comentarios funcionales exigidos por el diseño.

## Hallazgos abiertos

Ninguno.
