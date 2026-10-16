# Diseño — Interfaz para agentes externos

## Fuentes y contratos observados

- `AGENTS.md`, `docs/specs.md`, `docs/architecture.md` (§§3, 7–13, 18), `docs/conventions.md` (§§5–13) y los nueve criterios de `feature_list.json` guían alcance y reglas. La arquitectura exige adapters → casos de uso → dominio, evidencia antes de `passed`, política/budget no sorteables y contextos propagados.
- `cmd/featureproof/main.go` deriva hoy `validate` a `localvalidation.RunCommand`; `command.go` acepta rutas locales y selección de revisión; `runner.go` ejecuta sincrónicamente y publica al final. La nueva entrada no debe exponer esas rutas como parámetros de agente ni usar `RunCommand` como sustituto de un ciclo durable.
- `internal/localvalidation/package_reader.go` ofrece `ReadPackage(ctx, dir)`, que verifica inventario, digests, binding y referencias antes de devolver `PackageIndex`. `package_model.go` expone fallos, hechos, evidencias y reproducción; contiene también `PackageEvidence.Path`, que no cruzará esta interfaz. `publisher.go` usa staging `.incomplete` y renombre final. `revisionbinding`, `planapproval` y `harness` ya son puertas de admisión, aprobación, política y presupuestos; el compositor nuevo no puede inventar atestación o aprobación. La admisión productiva actual falla cerrada si no hay atestador/proyector de entorno verificable; la implementación debe mantenerlo y usar fakes para caminos positivos hasta disponer de composición productiva autorizada.
- [MCP Tools 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/server/tools): `tools/list` describe herramientas con `inputSchema` y `outputSchema`; `tools/call` transporta `structuredContent`; un handle es una cadena y se reautoriza en cada llamada; errores de ejecución usan `isError`, mientras herramienta desconocida/JSON-RPC inválido son errores de protocolo. No hay estado de sesión implícito.
- [MCP Versioning 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning) y [anuncio oficial de la release](https://blog.modelcontextprotocol.io/posts/2026-07-28/): el protocolo moderno elimina `initialize`/`initialized` y la sesión; cada petición lleva metadatos de versión, puede descubrirse por `server/discover` y debe tratarse por petición. `clientInfo` es autorreportado y no sirve para identidad ni autorización. La versión del contrato FeatureProof es distinta de la versión de protocolo MCP.

## Alcance y estructura

Adaptador CLI (`featureproof capabilities|run submit|run status|run result|run cancel --json`) y adaptador MCP stdio (`featureproof mcp serve`) llaman a `internal/externalagent` (nombre orientativo) con métodos `Capabilities`, `Submit`, `Status`, `Result`, `Cancel`. Los adapters traducen transporte y no importan internals del harness. El caso de uso consume interfaces pequeñas para identidad, catálogo de solicitudes autorizadas, almacén de registros/idempotencia, supervisor de trabajo, ejecutor aprobado y lector de paquetes. Las interfaces viven en el paquete consumidor; adaptadores filesystem/SO y de validación quedan fuera. No se crea API remota ni dependencia de modelo real.

### Identidad y admisión

El host local resuelve el principal desde credenciales del SO del proceso y un perfil/ámbitos preconfigurados en almacenamiento protegido. La CLI y el servidor stdio tienen exactamente esa fuente de autoridad; argumentos, variables de petición, `clientInfo`, `_meta` no confiable y nombre de herramienta no cambian el principal. La configuración vincula principal a aliases de `spec`, `plan`, target, repository/ref/environment y presupuesto permitido. La petición selecciona solo aliases y parámetros enumerados, jamás rutas, URL libre, credenciales o límites mayores. Se reevalúan ámbitos `runs:create`, `runs:read`, `runs:cancel` y acceso al run en cada operación; revocación prevalece sobre handle emitido. El adaptador MCP expone herramientas según ámbitos de la petición, no según la conexión. Si falta fuente de identidad confiable o configuración, fail closed.

### Contrato de aplicación v1

Se serializa JSON estricto, con campos desconocidos/duplicados rechazados y tamaño/profundidad máximos. Ejemplos esquemáticos:

| Operación | Entrada | Salida exitosa |
|---|---|---|
| `capabilities` | `{}` | `{contract_version:1, operations:[...], result_schema_version:1}` |
| `submit` | `{selection_alias, idempotency_key}` | `{run_handle, phase, status:null}` |
| `status` | `{run_handle}` | `{run_handle, phase, status, updated_at}` |
| `result` | `{run_handle}` | `{run_handle, phase:"terminal", status, constraints, failures, facts, evidence, reproduction}` |
| `cancel` | `{run_handle, idempotency_key}` | `{run_handle, phase, status}` |

`phase` es `queued|running|finalizing|terminal`; `status` solo está presente en terminal y vale `passed|failed|inconclusive|cancelled|error`. `failures` conserva identificadores, operador y `SafeValue` de estados `present|absent|redacted|truncated` ya seguros; `facts` conserva hechos de aserción/paso; `evidence` ofrece `evidence_handle` y metadatos acotados, nunca `Path`; `reproduction` conserva digests y requisitos declarativos, sin comando ejecutable ni ruta. Se revisan también strings semánticos de `PackageIndex` que pudieran contener texto no confiable: solo se aceptan campos de vocabulario/ID cerrado o se sustituyen por marcador seguro. El lector del paquete es la única fuente del resultado; no se lee directamente `report.json` sin verificación.

Errores de aplicación: `{error:{code,field?,retryable:false|true}}`, sin valor rechazado ni mensaje externo. `code` cerrado: `invalid_request`, `unauthorized`, `not_found`, `conflict`, `in_progress`, `internal_error`. `field` viene de allowlist de nombres de esquema. `not_found` cubre ajeno/desconocido/expirado. `in_progress` es recuperable con `status` y no insinúa fallo funcional. `failed` e `inconclusive` son resultados terminales, no errores. CLI imprime un único objeto JSON por comando y usa códigos de salida documentados y estables; MCP usa `structuredContent` con ese mismo objeto y `isError:true` para error de aplicación, manteniendo errores JSON-RPC solo para estructura de protocolo o herramienta desconocida. El `content` textual, si existe, es la misma serialización segura. Ninguna superficie imprime stack, ruta o valor de entrada rechazado.

### Estado durable, idempotencia y concurrencia

Un registro local separado del paquete guarda `run_handle`, principal, scope de selección, hash de solicitud canónica, clave de idempotencia protegida/hasheada, fase, estado terminal, owner y eventos de transición. Reserva de clave+handle+solicitud es una transacción atómica antes de ejecutar; locks de archivo entre procesos y escritura atómica/fsync evitan dos admisiones simultáneas. Índice de idempotencia se particiona por principal y operación. Igual clave+hash devuelve el mismo run, incluso tras reinicio; igual clave con otro hash produce `conflict`. `cancel` repetido con igual clave devuelve la misma respuesta y terminal no cambia. Los handles son aleatorios de alta entropía y no codifican paths, identidad ni revisión. El almacén fija una política de retención explícita publicada en `capabilities`; al vencer se elimina o inaccesibiliza de forma consistente y responde `not_found`.

El worker corre dentro del proceso propietario, bajo un supervisor con contexto, `WaitGroup`/join y lock de ownership mantenido hasta que toda herramienta y publicación terminen. La CLI `submit` emite el handle tras reserva y permanece viva supervisando hasta terminal; otras invocaciones CLI pueden consultar el registro. El MCP stdio responde con handle mientras su proceso mantiene el worker; el cierre del servidor cancela y espera los workers. No se lanza trabajo detached. El contexto de una llamada MCP ya respondida no es propietario del worker; el contexto del supervisor sí. `cancel` señala al supervisor por un mecanismo local protegido y espera su cierre con plazo; timeout del run se origina en el supervisor, no en el polling. Si el proceso muere, el lock OS se libera; la recuperación exclusiva marca un `running/finalizing` sin propietario como terminal `error` con causa segura `interrupted`, sin reejecución automática. El staging incompleto no se presenta como resultado. Finalización usa plazo acotado independiente del contexto cancelado solo para journal/publicación terminal y luego cierra recursos; si falla, registra `error`/incompleto. Ningún lock se mantiene durante trabajo de red/disco prolongado o callbacks, salvo el lock de ownership de ciclo de vida.

### Flujos

1. `submit`: autenticar → autorizar ámbito/alias → canonizar/validar → reservar idempotencia+handle → crear supervisor → usar compositor de validación existente (binding, aprobación, policy, budgets) → transición durable a running/finalizing/terminal. Si la puerta de aprobación devuelve pendiente, conservar fase consultable; no convertir aprobación pendiente en aprobación automática. Si no existe el compositor productivo verificable, devolver `internal_error` seguro antes de ejecutar herramientas.
2. `status/result`: autenticar y reautorizar handle → cargar registro → `result` devuelve `in_progress` hasta terminal → localizar paquete por ID interno (nunca ruta solicitada por cliente) → `ReadPackage` → comprobar run/binding/estado y proyectar DTO. Paquete corrupto, ausente o discrepante es `internal_error`, no `inconclusive` inventado.
3. `cancel`: autenticar, autorizar, reservar idempotencia de cancelación → señalar worker y esperar cierre → registrar terminal. Una cancelación después de terminal devuelve el terminal sin modificarlo; clave diferente no duplica transiciones.

## MCP y versiones

El servidor soporta solo MCP `2026-07-28` por stdio. `server/discover` anuncia esa versión y capacidad `tools`; `tools/list` enumera de forma determinista cinco herramientas de aplicación (`featureproof.capabilities`, `featureproof.submit`, `featureproof.status`, `featureproof.result`, `featureproof.cancel`) con esquemas cerrados de entrada/salida; `tools/call` invoca exactamente una. Cada petición incluye versión MCP conforme al protocolo. No se implementa `initialize` ni se infiere sesión/identidad de `clientInfo`. Sin ámbitos, la lista puede ser vacía; la invocación sigue reautorizándose. Un cambio de protocolo exige versión explícita; un cambio incompatible de DTO exige nuevo `contract_version`.

## Observabilidad y pruebas

Eventos durables: solicitud reservada, inicio, aprobación pendiente/decidida, cancelación solicitada, terminal y fallo de publicación, con `run_handle` y causa codificada, sin clave, prompt, ruta o credencial. Logs estructurados reflejan esas categorías sin ser fuente de estado. Tests unitarios con reloj/IDs/adapters falsos verifican máquina de estados, canonización, aislamiento, idempotencia concurrente y recuperación. Suite contractual común ejecuta exactamente fixtures por CLI y MCP (incluido wire `server/discover`/`tools/list`/`tools/call`) y compara DTOs; fakes inyectan ejecución bloqueada, aprobaciones, denegación, paquete válido/corrupto y dos principales. Tests de integración local verifican atomicidad, reinicio, lock y staging, sin red ni modelo. `go test ./...` y trazabilidad R→test son puerta de implementación.

## Decisiones y alternativas

Se usa stdio local porque cubre el criterio MCP verificablemente sin diseñar autorización remota antes de tiempo. No se usan rutas/URLs libres ni `clientInfo` para identidad. No se usa `RunCommand` como caso de uso porque su stdout/exit síncronos no ofrecen reserva durable ni estado consultable. No se expone directamente `PackageIndex` porque contiene rutas relativas de artefactos y campos que requieren proyección. No se usa token bearer por handle; toda operación se reautoriza. No se reintenta automáticamente una ejecución interrumpida: una nueva validación requiere nueva clave.

## Trazabilidad inicial

| Requirement | Componente y test previsto |
|---|---|
| R1, R7 | Registro/servicio; `TestSubmitDurableLifecycle`, `TestConcurrentIdempotency`, `TestRestartReservation` |
| R2, R10 | Adaptadores/fixtures; `TestCLIAndMCPContractParity` |
| R3 | Proyector/reader; `TestResultSafeProjection`, `TestCorruptPackageRejected` |
| R4, R5 | Identidad/admisión; `TestScopeAndApprovalGate`, `TestForeignHandleNotFound` |
| R6 | DTO/adaptadores; `TestStructuredErrorsAndOutcomes` |
| R8 | Supervisor; `TestCancelJoinsWorker`, `TestCrashRecovery` |
| R9 | Descubrimiento; `TestMCPDiscoveryAndTools`, `TestCLICapabilities` |
