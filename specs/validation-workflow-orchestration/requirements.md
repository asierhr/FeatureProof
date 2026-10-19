# Flujo completo de planificación y validación — Requirements

## Objetivo y problema

FeatureProof ya puede generar un plan mediante un modelo, someterlo a una
aprobación y ejecutar un plan proporcionado explícitamente. Sin embargo, esas
capacidades todavía no forman un único caso de uso productivo: el operador de
la CLI local debe escribir `plan.json` a mano y las integraciones no disponen
de una operación durable que recorra generación, aprobación y ejecución.

Esta feature conecta los contratos existentes en un flujo auditable que parte
de una specification y de selecciones explícitas, genera un `PreparedPlan`, lo
valida y, cuando corresponda, solicita una aprobación ligada a sus hashes antes
de ejecutar con el runner existente. El plan generado pasa a ser un artefacto
del run; nunca se convierte en autoridad por haber sido producido por el
modelo.

## Alcance

- Añadir un caso de uso de aplicación durable para admitir, generar, aprobar,
  ejecutar, cancelar, consultar y recuperar un workflow de validación.
- Exigir proveedor, modelo y perfil de autenticación explícitos, además de una
  selección explícita de target directo o preview existente y un perfil de
  runtime autorizado.
- Construir el contexto acotado a partir de specification, revisión, entorno,
  contrato OpenAPI reducido y catálogo descriptor-only.
- Reutilizar `agentplanning`, `planapproval`, `revisionbinding`, `previewenv`,
  `planning`, `localvalidation` y el runtime de modelo, sin duplicar sus reglas.
- Persistir el plan canónico y su procedencia segura antes de aprobación o
  ejecución, y publicar el mismo plan en el paquete terminal.
- Exponer el caso de uso por una API Go propia, una CLI nueva, MCP y el gateway
  de GitHub, manteniendo compatible `featureproof validate --plan`.
- Distinguir admisión fallida, contexto insuficiente, espera de aprobación,
  cancelación, resultado inconclusive, fallo funcional y error operativo.
- Probar todo el flujo con modelo, preview, approval, herramientas, reloj,
  filesystem y transports falsos, sin red real.

## Fuera de alcance

- Eliminar, cambiar la semántica o generar implícitamente el plan en el comando
  histórico `featureproof validate --spec --plan --target`.
- Añadir un proveedor de modelo, preview o herramienta nuevos.
- Permitir que el modelo seleccione proveedor, modelo, perfil, target, preview,
  revisión, policy, budgets o credenciales.
- Editar un plan generado manualmente o aprobarlo por texto libre.
- Aprovisionar, desplegar, modificar o destruir entornos preview.
- Reanudar una llamada al modelo o una tool call cuyo resultado quedó incierto
  tras una caída; la recuperación falla cerrada sin repetir efectos.
- Implementar una API HTTP remota o una interfaz web de aprobación.
- Cambiar los formatos versionados de specification, paquete de evidencias o
  contratos externos incompatibles sin incrementar su versión.

## Actores y términos

- **Operador:** persona o integración autenticada que inicia o decide un
  workflow.
- **Workflow:** lifecycle durable que une una admisión, una generación y, como
  máximo, una ejecución terminal.
- **Start selection:** selección explícita de specification, contrato,
  proveedor, modelo, perfil de autenticación, perfil runtime y target directo o
  preview.
- **Runtime profile:** configuración no secreta y autorizada de capabilities,
  policy, redacción, límites y red permitida. No contiene un plan.
- **Composition reference:** referencia privada que permite a la composición
  reconstruir transports; el core no recibe el perfil ni credenciales.
- **PreparedPlan:** conjunto inmutable que ya superó protocolo, procedencia,
  schema, semántica, registry descriptor-only, policy de generación y budgets.
- **Plan artifact:** serialización canónica, tratada e íntegra del plan
  preparado y su procedencia segura; no incluye prompt, output crudo, secretos
  ni contexto contractual completo.
- **Pre-admission error:** rechazo anterior a reservar un workflow/run; no crea
  handle, journal, directorio ni llama al modelo o tools.
- **Insufficient context:** resultado estructurado del generador que enumera
  información ausente y termina el workflow sin ejecutar tools.
- **Functional result:** `passed` o `failed` calculado por assertions
  deterministas; un error operativo no es un fallo funcional.

## Requirements

### R1 — Solicitud explícita y compatibilidad

WHEN an operator starts a generated validation workflow,
THE orchestration boundary SHALL require exactly one validated specification,
contract selection, provider, model, authentication profile, runtime profile,
repository and immutable revision, SHALL require exactly one of an authorized
direct target or preview selection, SHALL require an explicit approval mode,
and SHALL NOT choose a default for any of those selections or alter the
existing explicit-plan validation command.

#### Acceptance criteria

- GIVEN una solicitud completa con target directo o preview
  WHEN se admite
  THEN cada selección queda fijada antes de construir contexto y solo la
  composición conoce el perfil de autenticación.
- GIVEN proveedor, modelo, perfil, runtime, revisión, contrato, target/preview
  o modo de aprobación ausente, duplicado o ambiguo
  WHEN se valida
  THEN se devuelve pre-admission error sin handle, journal, modelo ni tool.
- GIVEN `featureproof validate --spec S --plan P --target T`
  WHEN se invoca después de esta feature
  THEN conserva su parser, semántica, códigos de salida y ejecución sin modelo.

### R2 — Admisión completa antes del modelo

WHEN a workflow start request is received,
THE orchestrator SHALL validate and snapshot the specification, full revision,
environment attestation, authorized target, reduced contract, capability
catalog, network/policy configuration and all generation/execution budgets
before reserving a model attempt, and SHALL reject any inconsistency as a
pre-admission error.

#### Acceptance criteria

- GIVEN specification, OID, preview attestation, OpenAPI, target policy o
  catálogo inválido
  WHEN se hace preflight
  THEN no se llama al modelo, no se abre DNS/socket y no se crea run.
- GIVEN un target directo
  WHEN se admite
  THEN `revisionbinding` verifica repositorio, commit y entorno, y la policy de
  red autoriza el target antes de incluir su identidad tratada en el contexto.
- GIVEN un preview
  WHEN se admite
  THEN `previewenv` reserva/reconcilia, atesta la revisión y autoriza el target;
  toda reserva confirmada se libera por su lifecycle existente aun si falla una
  fase posterior.
- GIVEN contexto externo con instrucciones
  WHEN se reduce
  THEN permanece etiquetado como dato no confiable y no modifica selecciones,
  policy, budgets ni instrucciones privilegiadas.

### R3 — Generación y preparación all-or-nothing

WHEN admission succeeds,
THE orchestrator SHALL invoke the selected model session through
`agentplanning.Generator` exactly once per explicit generation attempt, SHALL
accept only a complete `PreparedPlan` that passes protocol/schema, provenance,
semantic graph/assertion, descriptor registry, policy and budget validation,
and SHALL execute no tool during generation.

#### Acceptance criteria

- GIVEN una decisión `plan` válida
  WHEN finaliza la generación
  THEN todos los checks se preparan y se conserva un conjunto completo e
  inmutable; no se acepta un subconjunto.
- GIVEN JSON inválido, capability/endpoint inventado, referencia fuera del
  contexto, grafo/assertion inválido, policy denied o budget agotado
  WHEN se procesa
  THEN no se persiste plan ejecutable, no se solicita aprobación y ninguna tool
  se ejecuta.
- GIVEN un error transitorio del proveedor
  WHEN termina el intento
  THEN el orquestador no lo reintenta ocultamente; otro intento requiere una
  transición explícita, otra reserva de budget y una identidad de intento nueva.

### R4 — Contexto insuficiente como resultado propio

WHEN the planning agent returns a valid `insufficient_context` decision,
THE orchestrator SHALL persist only its bounded sanitized missing-context
projection, SHALL terminate the workflow with outcome `insufficient_context`,
and SHALL NOT stage approval, execute tools or present the condition as a
functional failure or internal error.

#### Acceptance criteria

- GIVEN preguntas/evidencias ausentes válidas
  WHEN se recibe la decisión
  THEN status/result las devuelve en orden estable sin prompt, contrato crudo ni
  secretos y el usuario puede iniciar un workflow nuevo tras aportar contexto.
- GIVEN un missing item inválido, no acotado o que referencia otra constraint
  WHEN se valida
  THEN se clasifica como respuesta de modelo inválida, no como contexto
  insuficiente confiable.
- GIVEN contexto insuficiente
  WHEN se inspecciona el runner
  THEN tiene cero llamadas a tools y cero package funcional publicado.

### R5 — Artefacto de plan auditable e íntegro

WHEN a `PreparedPlan` is accepted,
THE orchestrator SHALL atomically persist one versioned canonical plan artifact
bound to workflow/run, revision, target, constraints, contract, capabilities,
runtime and generation-contract hashes, together with safe provider, model,
prompt version and confirmed usage metadata, before approval or execution.

#### Acceptance criteria

- GIVEN plan preparado
  WHEN se confirma el artefacto
  THEN su digest coincide con el plan que se entrega a `planapproval` y al
  runner, y una mutación posterior falla por integridad.
- GIVEN canarios en auth profile, credenciales, prompt, output crudo, OpenAPI,
  headers y contexto externo
  WHEN se escanean artefacto, journal, logs y errores
  THEN no aparece ningún valor excluido.
- GIVEN crash durante escritura
  WHEN se recupera
  THEN no se expone artefacto parcial como plan válido y no se genera un segundo
  plan automáticamente.
- GIVEN publicación terminal
  WHEN se lee el paquete
  THEN `plan.json` representa el mismo plan canónico/digest aprobado y ejecutado.

### R6 — Aprobación exacta y explícita

WHERE approval mode is `required`, WHEN a plan artifact is confirmed,
THE orchestrator SHALL stage it through `planapproval`, expose the sanitized
approval view, remain in `awaiting_approval`, and execute nothing until an
authenticated decision authorizes the exact plan, context, revision and target
hashes with a one-use grant.

#### Acceptance criteria

- GIVEN un plan pendiente
  WHEN se consulta
  THEN la respuesta distingue `awaiting_approval` y contiene run handle,
  revisión de approval y la vista segura suficiente para decidir.
- GIVEN approve sobre los hashes vigentes
  WHEN se consume el grant una vez
  THEN comienza ejecución del snapshot aprobado.
- GIVEN target, revisión, constraints, contrato, runtime, modelo/prompt o plan
  cambiado; decisión duplicada/conflictiva; identidad no autorizada
  WHEN se decide
  THEN la aprobación es stale/conflict/unauthorized y no se ejecuta.
- GIVEN reject
  WHEN se confirma
  THEN el workflow termina `cancelled` o vuelve explícitamente a una nueva
  generación según la acción cerrada, sin ejecutar el plan rechazado.
- GIVEN approval mode `disabled` autorizado explícitamente por policy
  WHEN se confirma el artefacto
  THEN la puerta registra esa policy y pasa a ejecución sin fabricar una
  identidad de aprobador.

### R7 — Ejecución mediante el runner existente

WHEN a current plan is authorized for execution,
THE orchestrator SHALL resolve executable tools only after the approval gate,
re-evaluate current policy, budgets, revision/attestation and target, and SHALL
execute the canonical prepared plans through the existing deterministic runner,
evidence, assertion, verdict, report and evidence-package pipeline.

#### Acceptance criteria

- GIVEN un grant vigente o approval disabled permitido
  WHEN empieza ejecución
  THEN no existe un executor alternativo: se producen los mismos eventos,
  evidencias tratadas, assertions, binding, `report.json`, `report.md` y package
  íntegro que en la ruta con plan explícito.
- GIVEN un drift de revisión, attestation, target, policy o budget antes de la
  primera tool o entre batches
  WHEN se revalida
  THEN no empieza trabajo nuevo y el resultado es operacional/inconclusive,
  nunca un fallo funcional inventado.
- GIVEN assertion incumplida
  WHEN finaliza el runner
  THEN el workflow termina con resultado funcional `failed`; solo todas las
  assertions requeridas respaldadas por evidencia permiten `passed`.

### R8 — Estados y errores sin ambigüedad

THE workflow contract SHALL distinguish pre-admission errors from admitted
phases `generating`, `awaiting_approval`, `executing` and `finalizing`, and
terminal outcomes `passed`, `failed`, `insufficient_context`, `inconclusive`,
`cancelled` and `error`, SHALL keep functional failure as result data, and
SHALL expose only typed safe error categories.

#### Acceptance criteria

- GIVEN input/configuración inválida antes de durable admission
  WHEN falla
  THEN no existe run handle y la respuesta es un error de aplicación.
- GIVEN fallo de infraestructura, límite esperado, cancelación o assertion
  fallida después de admisión
  WHEN termina
  THEN se proyecta respectivamente `error`/`inconclusive`, `inconclusive`,
  `cancelled` o `failed`, sin intercambiar categorías.
- GIVEN un package verificado terminal
  WHEN se consulta result
  THEN `passed|failed|inconclusive|cancelled` procede del package; texto del
  modelo, journal o adapter no puede elevarlo.

### R9 — Idempotencia, ownership y recuperación por fases

WHEN a workflow is admitted,
THE durable workflow store SHALL atomically bind principal/surface,
idempotency key, canonical request hash and one run handle, SHALL serialize
phase transitions with CAS and expiring ownership leases, and SHALL recover a
confirmed prefix without creating duplicate runs, plans, approvals, model
calls, preview reservations, tool effects or publications.

#### Acceptance criteria

- GIVEN la misma identidad, key y request
  WHEN se repite start concurrentemente o tras reinicio
  THEN se devuelve el mismo handle y estado; misma key con request distinto es
  conflicto.
- GIVEN lease viva
  WHEN otro proceso intenta recuperar
  THEN no toma ownership; tras expirar solo un proceso reclama mediante CAS.
- GIVEN crash antes de modelo, después de plan, en awaiting approval o después
  de package terminal
  WHEN se recupera
  THEN respectivamente continúa desde el último checkpoint seguro, reutiliza el
  artefacto, conserva la decisión pendiente o publica/proyecta una sola vez.
- GIVEN crash con inferencia o tool call en vuelo y sin resultado confirmado
  WHEN se recupera
  THEN no repite la operación incierta; finaliza de forma segura como
  inconclusive/error con causa `interrupted` y conserva cleanup recuperable.

### R10 — Cancelación, deadlines y cierre poseído

WHILE an admitted workflow is non-terminal, IF cancellation is requested or a
phase deadline expires, THEN THE orchestrator SHALL durably record the request,
propagate context cancellation to model, approval wait, preview and runner,
stop before new work, finalize with a bounded non-cancelled cleanup context,
release reservations and join every owned worker.

#### Acceptance criteria

- GIVEN cancel durante generación, espera de aprobación, ejecución o
  finalización
  WHEN se procesa
  THEN no empieza la siguiente fase, no se acepta output parcial y el terminal
  converge en `cancelled` salvo un error de integridad/persistencia confirmado.
- GIVEN cancel repetido con la misma key
  WHEN se aplica
  THEN devuelve el mismo resultado; cancel tras terminal no cambia el terminal.
- GIVEN preview reservado o staging abierto
  WHEN se cancela
  THEN release/publicación de cancelled usa deadline propio, no queda goroutine
  huérfana y un fallo deja intención durable para recovery.

### R11 — Secretos confinados a composición

THE composition boundary SHALL resolve authentication profiles and credentials
only while constructing the selected model, preview/provider and authorized
tool transports, SHALL provide the orchestrator only safe identities,
capability descriptors and already-built ports, and SHALL NOT expose secret
bytes, environment access or auth-profile identity to the harness, plan,
approval view, evidence, package, telemetry or model context.

#### Acceptance criteria

- GIVEN perfiles con canarios distintos
  WHEN se compone un workflow
  THEN cada secreto llega solo a su transporte mínimo y se limpia tras construir
  el adapter cuando sea posible.
- GIVEN core/harness/generator/approval
  WHEN se enumeran dependencias
  THEN no reciben `SecretSource`, environment reader, token ni credential store.
- GIVEN recovery
  WHEN se recompone
  THEN usa una referencia privada protegida en composición; el journal y los
  artefactos públicos no guardan el perfil ni credenciales.

### R12 — Superficies coherentes CLI, API, MCP y GitHub

WHEN an authorized caller uses CLI, application API, MCP or GitHub,
THE adapter SHALL invoke the same workflow use case and state store, SHALL
preserve idempotency and safe status semantics, and SHALL NOT bypass generation,
approval, policy, revision binding, preview lifecycle or package verification.

#### Acceptance criteria

- GIVEN la CLI nueva
  WHEN usa `workflow start|status|approve|reject|cancel|result`
  THEN traduce flags/JSON a la API común y no llama al runner o modelo
  directamente.
- GIVEN MCP
  WHEN descubre/invoca las operaciones workflow versionadas
  THEN sus DTOs equivalen a la API/CLI, no aceptan paths/URLs/perfiles fuera del
  ámbito autorizado y reautorizan cada operación.
- GIVEN el comando GitHub existente y una selección `default` configurada
  explícitamente por la instalación
  WHEN se admite la PR
  THEN esa selección sellada fija provider/model/profile/runtime/contract/
  preview; no existe fallback, y `awaiting_approval` se publica sin tratar el
  comentario inicial como aprobación del plan.
- GIVEN approval pendiente originada por GitHub
  WHEN no hay una superficie autenticada de decisión
  THEN no se ejecuta; la decisión se realiza por la API/CLI/MCP autorizada.

### R13 — Pruebas herméticas, trazabilidad y comentarios sistémicos

THE feature SHALL verify every requirement with deterministic fakes and no
real network, account or secret, SHALL preserve at least 85% statement coverage
for new or materially modified packages and pass race verification, and every
introduced or modified entity, constant, package variable, interface, method,
function, constructor and test helper SHALL include a comment describing its
responsibility in the system.

#### Acceptance criteria

- GIVEN modelo, preview, approval, tool, package, store, clock, waiter y
  filesystem falsos
  WHEN corre la suite
  THEN cubre success, failed, invalid plan, insufficient context, approval,
  stale approval, cancellation, crash recovery, idempotency y secret canaries.
- GIVEN dos workflows y reclamaciones concurrentes
  WHEN se ejecutan bajo race repetido
  THEN no comparten estado, no duplican ownership y toda goroutine termina sin
  `time.Sleep` como sincronización.
- GIVEN inspección AST de archivos nuevos/modificados
  WHEN encuentra cualquier símbolo o helper cubierto por este requirement
  THEN existe un comentario sistémico, incluidos símbolos internos, `var` y
  `const` de paquete.

## Errores y casos límite

- Los IDs, aliases y hashes son exactos y acotados; no se normalizan paths,
  modelos, perfiles ni revisiones para intentar hacerlos válidos.
- El proveedor y modelo son metadatos auditables seguros; el auth profile no se
  incluye en telemetría, plan, approval ni package.
- Una policy `denied` antes de admission es error de solicitud/configuración;
  una policy que cambia después de admission impide trabajo y produce
  inconclusive/error operacional según exista evidencia suficiente.
- El plan no se reconstruye desde la vista de aprobación. Siempre se carga el
  artefacto canónico y se verifican digest y binding.
- `awaiting_approval` no es resultado terminal y nunca tiene package funcional.
- `insufficient_context` no reutiliza el mismo run al aportar información: el
  usuario crea una nueva solicitud/idempotency key para que el snapshot sea
  inequívoco.
- Si approval disabled deja de estar autorizado antes de ejecutar, se detiene;
  no se degrada a ejecución automática ni se fabrica una aprobación.
- Un provider/model timeout no se presenta como insufficient context.
- Un HTTP 4xx/5xx solo es functional failure si una assertion determinista lo
  exige; los adapters no lo interpretan como veredicto.
- Un paquete ausente, corrupto o con binding distinto es `error`, nunca passed,
  failed funcional ni resultado parcial confiable.

## Decisiones abiertas

Ninguna para esta entrega. La CLI nueva usa el namespace `workflow`; la API es
un contrato Go interno y no implica servidor HTTP. MCP añade operaciones
versionadas sin retirar las actuales. GitHub reutiliza su comando existente y
una selección de instalación explícita; la aprobación, cuando se exige, se
resuelve por una superficie autenticada de decisión.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-19T11:12:00+02:00
```
