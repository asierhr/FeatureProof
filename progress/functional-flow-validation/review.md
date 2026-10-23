# Review independiente — Feature 27: Validación de flujos funcionales reales

## Veredicto

`rejected`

La entrega cumple las precondiciones formales de review (feature 27 es la única
`in_progress`, la spec registra aprobación humana, T1–T16 figuran `[x]` y existe
`implementation.md`), pero no satisface R4, R6–R13 ni R15. Las suites actuales
pasan porque no ejercitan varios enlaces críticos entre admisión, recovery,
veredicto, package y superficies reales.

## Hallazgos

## REV-1 — Una comparación fallida no puede cambiar el veredicto

- Severidad: blocker
- Requirement o regla: R6, R7, ARCH-003, ARCH-004
- Archivo y línea: `internal/functionalflow/executor.go:159-170`; `internal/functionalflow/ledger.go:40-92`; `internal/functionalflow/validate.go:70-74`
- Evidencia: el executor añade las comparaciones al ledger con su ID, pero
  `Validate` solo permite que `Step.RequiredEvidence` referencie IDs de
  `step.Assertions`. `Ledger.Aggregate` consulta exclusivamente esos
  `RequiredEvidence`; nunca consulta `Result.Comparisons` ni exige sus IDs. Por
  tanto, una comparación `not_equal` fallida puede coexistir con assertions
  locales passed y producir `OutcomePassed`. Los tests de comparación llaman
  `EvaluateComparison` aisladamente y no prueban su efecto en el outcome.
- Impacto: FeatureProof puede declarar passed aunque la observación post no
  pruebe la mutación, la ausencia de mutación o el estado final solicitado.
- Corrección requerida: hacer que cada comparación requerida forme parte del
  conjunto de prueba que agrega el veredicto, validar sus IDs y ownership, y
  añadir un test end-to-end donde una comparación failed/inconclusive impida
  `passed`.

## REV-2 — Un fallo funcional confirmado se agrega como passed

- Severidad: blocker
- Requirement o regla: R7, R8, ARCH-002, ARCH-003
- Archivo y línea: `internal/functionalflow/executor.go:239-241`; `internal/functionalflow/ledger.go:62-81`
- Evidencia: `executeStep` convierte `ExecutionResponse.FunctionalFailure` en
  `StepFailed`, pero `Ledger.Aggregate` solo trata de forma especial
  `StepBlocked` y `StepInconclusive`. Para un `StepFailed` con assertions
  passed, recorre el ledger y deja el constraint en passed.
- Impacto: un status funcional no aceptado puede terminar en un resultado
  global passed, contradiciendo además la separación entre fallo funcional y
  fallo operativo.
- Corrección requerida: definir y probar la contribución determinista de un
  terminal `StepFailed`; ningún required functional failure puede agregarse
  como passed.

## REV-3 — Recovery repite actions inciertas y la lease puede expirar durante I/O

- Severidad: blocker
- Requirement o regla: R9, R13, ARCH-006, CONV-005, CONV-007
- Archivo y línea: `internal/functionalflow/executor.go:122-140,248-260`; `internal/functionalflow/store.go:53-82,184-186`
- Evidencia: tras un crash después de I/O y antes de `ConfirmTerminal`, queda un
  intent. Cuando expira la claim, otro owner la reclama; `confirmActionIntent`
  considera válido el intent previo ignorando el nuevo token y ejecuta de nuevo
  la action sin comprobar `Idempotent`/`ReconciliationSafe`. Además, la claim
  tiene TTL fijo y no existe heartbeat ni relación validada con
  `StepTimeout`; puede expirar mientras el primer tool sigue activo. Los tests
  de redelivery solo repiten después de un terminal confirmado y la supuesta
  crash matrix tampoco simula crash entre intent, I/O, evidencia y terminal.
- Impacto: dos owners o una redelivery pueden duplicar un efecto no idempotente.
- Corrección requerida: persistir/reconstruir el checkpoint de efecto incierto,
  no reejecutar un act no reconciliable con intent sin terminal, reutilizar la
  misma key solo bajo garantía verificada, y mantener ownership válido durante
  el blocking boundary. Añadir la matriz real de crashes y dos procesos bajo
  `-race`.

## REV-4 — La preparación ocurre antes de componer y validar el FlowArtifact

- Severidad: blocker
- Requirement o regla: R3, R4, diseño “persistir `FlowArtifact` antes de prepare”
- Archivo y línea: `internal/validationworkflow/service.go:609-625`; `internal/validationworkflow/functional_flow.go:43-51`
- Evidencia: `Service.execute` llama primero a `Fixtures.Prepare`; solo después
  entra en `FunctionalFlowExecutionAdapter.Execute`, donde `Composer.Compose`
  normaliza/valida/bindea el flujo. El artefacto que Service comprobó antes es
  el artefacto histórico del plan, no `FlowArtifact/v1`. El test de ordering usa
  un `flowExecutionPortFake` y oculta que la composición está dentro de la
  llamada `flow` posterior a prepare.
- Impacto: un provided/generated flow inválido, con grafo, referencia, binding
  o autoridad incorrectos, puede crear fixtures antes de ser rechazado.
- Corrección requerida: componer, validar, bindear, aprobar y persistir el
  `FlowArtifact` exacto antes de cualquier fixture; hacer que Service ejecute
  después ese snapshot, y probar cero efectos de fixture/tool ante cada fallo de
  admisión.

## REV-5 — No existe una vertical productiva para ejecutar la feature

- Severidad: blocker
- Requirement o regla: R1, R4, R10, R15; aceptación de feature “puede ejecutar un flujo completo generado o proporcionado”
- Archivo y línea: `internal/validationworkflow/model.go:153-161`; `internal/validationworkflow/functional_flow.go:16-60`; `internal/validationworkflow/service.go:43,623-628`
- Evidencia: el cambio añade puertos y un selector/proyector, pero no existe una
  composición concreta de `FunctionalFlowExecutionAdapter`, composer,
  functional executor o publisher en `cmd/featureproof` ni en otra superficie.
  `StartCommand` tampoco puede seleccionar un manifest provided. `RevisionSet`
  no tiene ningún consumidor productivo. La búsqueda productiva de
  `FunctionalFlowExecutionAdapter`, `DecodeProvided`, `NewRevisionSet` y
  `functionalflow.NewLocalStore` solo encuentra definiciones; los tests
  inyectan fakes directamente en `service.deps`.
- Impacto: CLI, API, MCP y GitHub siguen usando el runner anterior; ningún
  usuario puede iniciar el flujo nuevo, proporcionar un flow, recuperarlo ni
  replanificarlo. La “paridad” probada es únicamente cuatro métodos Go que
  llaman al mismo projector.
- Corrección requerida: cablear una composición real y compartida detrás de
  `validationworkflow.Service`, incorporar selección local/remota conforme a la
  spec, y probar las cuatro entradas reales start/status/cancel/result y su
  redelivery, no solo la proyección de un package construido en memoria.

## REV-6 — El package no contiene ni autentica la evidencia que dice verificar

- Severidad: blocker
- Requirement o regla: R12, R14, ARCH-003, ARCH-005, ARCH-007
- Archivo y línea: `internal/functionalflow/package.go:24-33,36-51,54-118`
- Evidencia: `Package` no contiene records/manifests de evidence ni sus digests.
  `VerifyPackage` considera existente cualquier ID mencionado por cualquier
  evento y luego acepta ese string como respaldo de un ledger passed. Tampoco
  valida la causalidad completa: basta que el primer evento sea
  `flow_artifact_committed` y que haya exactamente un `primary_terminal` y un
  `cleanup_terminal`; permite cleanup antes del primary, evidence de un evento
  no relacionado y pasos/assertions ausentes o reordenados. El package digest es
  autocontenido y no sustituye el digest de la evidencia externa.
- Impacto: un package fabricado o incompleto puede proyectar passed sin una
  evidencia verificable y no reconstruye el orden semántico prometido.
- Corrección requerida: incluir o referenciar mediante manifest autenticado los
  records seguros de evidence, verificar digest/owner/run/revision/step/binding,
  enlazar cada assertion/comparison a sus records y validar la máquina causal
  completa de artifact → prepare → steps → assertions → primary → cleanup.

## REV-7 — El package funcional se publica antes del cleanup que debe contener

- Severidad: high
- Requirement o regla: R11, R12
- Archivo y línea: `internal/validationworkflow/functional_flow.go:31-33,55`; `internal/validationworkflow/service.go:628-650,755-817`
- Evidencia: `FunctionalFlowPublisher.Publish` solo recibe artifact y primary y
  se invoca dentro de la ejecución, antes de que `Service.terminal` ejecute
  cleanup. El cleanup posterior solo actualiza el package histórico a través de
  `FixturePackageWriter`; no hay reader/writer concreto del nuevo
  `functionalflow.Package`. Por construcción, el publisher nuevo no puede
  publicar el cleanup real ni su evento terminal.
- Impacto: el package proyectado por API/CLI/MCP/GitHub no puede ser el package
  final reconstruible que la spec exige, y recovery de cleanup puede dejarlo
  divergente.
- Corrección requerida: definir ownership único del package final, sellarlo o
  completarlo idempotentemente después del cleanup y verificar que recovery
  produce exactamente la misma primary y el cleanup actualizado por todas las
  superficies.

## REV-8 — Una revisión adicional puede reescribir assertions previas

- Severidad: blocker
- Requirement o regla: R7, R10
- Archivo y línea: `internal/functionalflow/bind.go:125-162`; `internal/functionalflow/revision.go:22-39`
- Evidencia: `safeArtifactStep` borra `AssertionDeclaration.Definition` y no
  conserva un digest por assertion. `RevisionSet.Add` protege el prefijo
  comparando esos `BoundStep` saneados. Una revisión con el mismo ID de
  assertion pero operador/esperado distintos produce un semantic digest nuevo,
  mientras el prefijo saneado sigue siendo idéntico, por lo que se acepta. El
  set tampoco está integrado con ledger/resultados confirmados.
- Impacto: una replanificación puede reinterpretar prueba ya ejecutada o cambiar
  el significado de un fallo previo.
- Corrección requerida: sellar definición y requirement lineage mediante
  digests seguros por step/assertion, comparar el prefijo canónico completo y
  ligar la admisión a resultados previos inmutables; añadir un test que cambie
  solo el operador/expected de una assertion anterior.

## REV-9 — El executor no demuestra que la autoridad del artifact corresponde al flujo validado

- Severidad: high
- Requirement o regla: R2, R5, R13, ARCH-001, ARCH-011
- Archivo y línea: `internal/functionalflow/executor.go:45-48,107-118`; `internal/functionalflow/bind.go:73-88`
- Evidencia: el executor compara semantic digest/flow/revision y únicamente
  revalida `InputDigest` por step. Después sustituye `bound.Step` con el step
  validado, pero conserva `bound.Operation` del artifact sin comprobar que su
  operation reference, effect metadata e identity binding fueron derivados de
  ese step. `ValidateArtifact` comprueba la huella autocontenida del artifact,
  no la relación artifact↔validated flow.
- Impacto: un composer/recovery defectuoso puede entregar un artifact
  internamente consistente con una operación o identidad distinta y esa
  autoridad llega al tool.
- Corrección requerida: verificar de forma determinista todos los enlaces entre
  cada step canónico y su bound operation/identity/effect/retry metadata, o
  encapsular el artifact para que solo pueda construirse por el binder y
  revalidarlo contra la autoridad exacta al recuperar.

## REV-10 — `Flow.RequiredEvidence` es decorativo y sus invariantes no se validan

- Severidad: high
- Requirement o regla: R3, R7, R12
- Archivo y línea: `internal/functionalflow/model.go:153-161`; `internal/functionalflow/validate.go:18-108`; `internal/functionalflow/ledger.go:40-92`
- Evidencia: el normalizer generated rellena `Flow.RequiredEvidence`, pero
  `Validate` no comprueba nil, duplicados, existencia ni correspondencia con
  declarations, y `Aggregate` nunca lo usa. Un provided flow puede declarar un
  required evidence global inexistente y aun así pasar.
- Impacto: el contrato canónico y el veredicto divergen; la reconstrucción no
  puede demostrar que satisfizo todos los requirements declarados.
- Corrección requerida: definir una única semántica requerida, validar la lista
  global y hacer que admisión, agregación y package usen el mismo conjunto
  canónico, con casos duplicate/unknown/missing.

## Trazabilidad R → revisión

| Requirement | Estado | Evidencia de review |
|---|---|---|
| R1 | parcial | Normalizadores existen; no hay entrada productiva provided ni equivalencia completa en la vertical (REV-5). |
| R2 | rechazado | Binding unitario existe; falta probar/forzar artifact↔flow en runtime (REV-9). |
| R3 | parcial | Grafo/ancestry se validan; required evidence global queda sin semántica (REV-10). |
| R4 | rechazado | Prepare precede a Compose/Validate/Bind (REV-4). |
| R5 | parcial | Scheduler secuencial existe; no hay vertical productiva y el artifact puede derivar (REV-5, REV-9). |
| R6 | rechazado | La comparación se calcula pero no gobierna el veredicto (REV-1). |
| R7 | rechazado | Comparaciones y functional failures pueden terminar passed (REV-1, REV-2). |
| R8 | rechazado | El estado StepFailed no se agrega correctamente (REV-2). |
| R9 | rechazado | Recovery puede duplicar un act incierto (REV-3). |
| R10 | rechazado | RevisionSet no está integrado y permite drift de assertions (REV-5, REV-8). |
| R11 | rechazado | Cleanup del workflow no puede formar el package funcional publicado previamente (REV-7). |
| R12 | rechazado | Package no autentica evidence ni causalidad completa (REV-6, REV-7, REV-10). |
| R13 | rechazado | Lease sin heartbeat + replay de intent permite owners concurrentes/duplicación (REV-3). |
| R14 | parcial | Canarios y DTOs omiten valores; faltan la vertical y evidence package real para cerrar fronteras (REV-5, REV-6). |
| R15 | rechazado | La prueba de paridad solo compara projectors en memoria; no hay adapters reales cableados (REV-5). |

## Tasks

T1–T16 están marcadas `[x]`, pero T6, T9–T15 y T16 no tienen evidencia
suficiente frente a los hallazgos anteriores. No se acepta la excepción
implícita de dejar composición, publisher, entradas y recovery como puertos sin
implementación porque son comportamiento requerido por esta misma feature.

## Gates reproducidos

Ejecutados desde `C:\Users\asier\Desktop\proyectos\FeatureProof`:

- `git diff --check`: pasa (solo avisos de normalización LF/CRLF al consultar algunos diffs).
- `go test -run '^$' ./...`: pasa.
- `go vet ./...`: pasa.
- `go test -count=3 ./internal/functionalflow ./internal/validationworkflow`: pasa.
- `go test ./...`: pasa.
- `CGO_ENABLED=1 go test -race ./internal/functionalflow ./internal/validationworkflow` con UCRT64: pasa.
- `CGO_ENABLED=1 go test -race ./...` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de ambos paquetes: pasa.
- Coverage conjunta con `-coverpkg` sobre `internal/functionalflow` e
  `internal/validationworkflow`: **85,7 % statements**, supera el 85 %.
- `feature_list.json` no cambia respecto de `b81cee0`; no se encontraron
  binarios/coverage temporales dentro del repositorio.

Los gates verdes no cierran los defectos semánticos: los tests actuales prueban
funciones aisladas o fakes y omiten exactamente los escenarios descritos en
REV-1–REV-10.

---

# Segunda revisión independiente — remediación REV-1–REV-10

## Veredicto

`rejected`

La remediación cierra materialmente REV-1, REV-2, REV-4, REV-6, REV-7,
REV-8, REV-9 y REV-10. REV-3 queda parcialmente abierto por ownership durante
retries y REV-5 queda parcialmente abierto porque la vertical productiva ya
existe, pero el flow realmente ejecutado no forma parte de la aprobación humana
y las superficies no prueban el mismo contrato completo. Se encontraron seis
defectos adicionales.

## Estado de los hallazgos anteriores

| Hallazgo | Estado en segunda revisión | Evidencia |
|---|---|---|
| REV-1 | cerrado | Comparaciones pueden ser required declarations y `Ledger.Aggregate` consume su entrada; `TestRequiredComparisonControlsEndToEndVerdict` cubre failed e inconclusive. |
| REV-2 | cerrado | `StepFailed` fuerza `OutcomeFailed`; `TestFunctionalStepFailureControlsVerdict` lo prueba. |
| REV-3 | parcial | Intent/effect/evidence checkpoints impiden replay no seguro, pero la lease no cubre el máximo de retries (REV-16). |
| REV-4 | cerrado | `Service.Start` llama `FunctionalFlow.Admit` y persiste el artifact antes de approval y prepare. |
| REV-5 | parcial | Existe composición local real y entradas CLI/MCP/GitHub/API, pero el provided flow sigue subordinado al plan agentico y fuera de la aprobación exacta (REV-11); GitHub tampoco ofrece un `Result` propio y el test llama `Service.Result` directamente. |
| REV-6 | parcial | Hay manifests de evidence y verificación estructural; el journal del package sigue siendo sintetizado y puede afirmar un orden falso (REV-15). |
| REV-7 | cerrado | Primary se stagea y el package se sella idempotentemente después del cleanup de fixtures. |
| REV-8 | cerrado | Declaration/requirements digests y prior-result digest impiden reescribir el prefijo confirmado. |
| REV-9 | cerrado | `ValidateArtifactAuthority` rederiva operación, identidad, effect y retry metadata desde autoridad trusted. |
| REV-10 | cerrado | La lista global required se valida y comparte semántica con steps, ledger y package. |

## Hallazgos de segunda revisión

## REV-11 — La aprobación humana autoriza el plan agentico, no el flow que se ejecuta

- Severidad: blocker
- Requirement o regla: R1, R4, R15; plan approval exacta; ARCH-001, ARCH-011
- Archivo y línea: `internal/validationworkflow/service.go:184-209,619`; `internal/validationworkflow/approval.go:17-36`
- Evidencia: `FunctionalFlow.Admit` compone y persiste el flow antes de approval,
  pero `Approval.Stage`, `Approval.Decide` y `Approval.Consume` siguen recibiendo
  solo `PreparedPlan`, `AdmissionSnapshot` y el `PlanArtifact` histórico. El
  `FlowArtifact/v1` ni su digest aparecen en la vista, contexto o grant. Para un
  `FunctionalFlowSelection` provided, Service además ejecuta primero generación
  agentica y exige que produzca un plan válido; la persona ve y aprueba ese plan
  generado aunque después `FunctionalFlow.Execute` recompone y ejecuta el
  provided flow, que puede tener otros steps, identities, effects y assertions.
  `TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces` usa un flow port
  fake y solo comprueba que circula el artifact ID; no compara el contenido de
  la approval view/grant con el artifact ejecutado.
- Impacto: una aprobación válida para A permite ejecutar B. También un provided
  flow completo no es un origen independiente: puede quedar bloqueado por un
  fallo del modelo que no participa en su contenido.
- Corrección requerida: hacer que aprobación/autorización selle y muestre el
  digest y la proyección segura del `FlowArtifact` exacto (provided o generated),
  consumir ese mismo binding al ejecutar y separar el origen provided de una
  generación irrelevante. Añadir tests donde el provided flow difiere del plan
  generado y una aprobación del plan viejo no abre ninguna tool.

## REV-12 — La identidad pública implícita elimina el binding que debía restringir la operación

- Severidad: blocker
- Requirement o regla: R2, R5, R14; “identity ID obligatorio, sin fallback implícito”; ARCH-001, ARCH-011
- Archivo y línea: `internal/functionalflow/normalize.go:98-105`; `cmd/featureproof/workflow.go:651-652`; `internal/validationworkflow/functional_flow_runtime.go:220-225`
- Evidencia: cuando el plan generated omite identity, el normalizer inserta
  automáticamente `featureproof-public`. La composición crea ese descriptor
  como `MechanismBearer` con autoridad sobre todas las operaciones y el tool
  adapter convierte después su binding a `nil`. El HTTP tool solo revalida
  method/path exactos cuando recibe `IdentityBinding`; con `nil`, ejecuta el
  method/path de `request.Input`. Un provided flow también puede seleccionar el
  ID sintético. No hay comprobación en el adapter de que los campos efímeros
  `method` y `path` coincidan con `BoundOperation.Reference`.
- Impacto: una operación bindeada como `/approved` puede transportar un input
  `/other` dentro del target permitido, y un step sin actor explícito obtiene
  autoridad implícita. El artifact parece exacto mientras el request real no lo
  es.
- Corrección requerida: exigir identidad explícita o modelar anonymous/public
  como autoridad declarada real, sin cambiar su binding a nil; validar/injectar
  method, path, target y capability desde el bound operation inmediatamente
  antes del tool y rechazar cualquier drift antes de red/DNS/dial. Probar el
  ataque tanto para generated como provided.

## REV-13 — Las comparaciones consideran iguales recursos distintos

- Severidad: blocker
- Requirement o regla: R6, R7
- Archivo y línea: `internal/validationworkflow/functional_flow_runtime.go:220-250,308-313`; `internal/functionalflow/resolve.go:102-133`
- Evidencia: `functionalResourceDigest` solo hashea `OperationReference`.
  Omite el input resuelto que selecciona el recurso (path params, query, body o
  ID capturado). Dos observaciones de `/users/1` y `/users/2` bajo la misma
  operación contractual reciben el mismo `ResourceBindingDigest`, por lo que
  `EvaluateComparison` las acepta como el mismo recurso.
- Impacto: una comparación before/after puede “probar” mutación o ausencia de
  mutación comparando objetos diferentes.
- Corrección requerida: derivar un selector canónico privado a partir de la
  operación exacta y los componentes de input que identifican el recurso,
  guardar solo su digest y exigir igualdad. Añadir casos same operation con
  path/query/ID diferentes que queden inconclusive antes de verdict passed.

## REV-14 — Flujos generated históricamente válidos fallan por IDs de assertion repetidos

- Severidad: high
- Requirement o regla: R1, compatibilidad histórica sin defaults nuevos
- Archivo y línea: `internal/functionalflow/normalize.go:84-125`; `internal/functionalflow/validate.go:36-81`; `internal/planning/validate.go:111-143`
- Evidencia: planning exige IDs de assertion únicos solo dentro de cada step;
  por tanto dos steps válidos pueden usar ambos `assert`. El normalizer
  namespacea step IDs con el check ID, pero no namespacea assertion IDs.
  `functionalflow.Validate` usa un `declarationOwner` global y rechaza el segundo
  ID repetido. No hay test de compatibilidad con dos steps/checks que reutilicen
  el mismo ID local, aunque es una forma aceptada por el contrato histórico.
- Impacto: activar feature 27 rompe planes generados que antes eran válidos y
  puede dejar runs inconclusive antes de fixtures/tools.
- Corrección requerida: preservar ownership como `(stepID, declarationID)` o
  aplicar un namespace canónico consistente también a required evidence y
  ledger; añadir regresión con IDs locales repetidos en varios steps/checks.

## REV-15 — El package fabrica un journal idealizado y puede publicar un orden falso

- Severidad: blocker
- Requirement o regla: R3, R9, R12, R13, ARCH-007
- Archivo y línea: `internal/functionalflow/package.go:74-101,254-282`; `internal/validationworkflow/functional_flow_publisher.go:87`
- Evidencia: `BuildLifecycleEvents` no consume el journal durable real; inventa
  eventos al final desde `Result`. Siempre emite `fixtures_prepared`, incluso si
  la aprobación fue rechazada o preparation falló/no era necesaria. Emite un
  único `step_terminal` por step y omite claims, intents, attempts, retries,
  policy decisions, uncertain effects y recovery. Además itera
  `primary.Steps`, que `sortStepRecords` ordena por declaración, no por el orden
  topológico ejecutado: si B se declara antes que A pero depende de A, el
  package afirma B→A aunque la ejecución fue A→B. `VerifyPackage` exige esa misma
  secuencia fabricada y por eso no detecta la falsedad.
- Impacto: el package no reconstruye cómo ocurrió el run y puede certificar
  preparación y orden de efectos que nunca sucedieron, invalidando auditoría y
  recovery.
- Corrección requerida: persistir y empaquetar el journal append-only real con
  fases/attempts/checkpoints/categorías, ligar cada evento a su digest y validar
  su máquina causal. Preparation failed/not-required/rejected deben conservarse
  como tales y el orden debe ser el topológico/real, no declaration order.

## REV-16 — La lease solo cubre un intento aunque el executor permite varios

- Severidad: high
- Requirement o regla: R9, R13, CONV-005, CONV-007
- Archivo y línea: `internal/functionalflow/executor.go:114,217-254`; `cmd/featureproof/workflow.go:667,674-675`
- Evidencia: antes de abrir el tool se exige únicamente
  `ExpiresAt > now + StepTimeout + ConfirmationTimeout`. `executeStep` puede
  realizar `MaxRetries+1` intentos y waits. La composición productiva fija la
  lease en un solo `StepTimeout + confirmation + 1m`, sin multiplicar por el
  número de intentos ni incluir backoff. Un segundo/tercer intento lento puede
  seguir en I/O tras expirar la claim y otro owner puede reclamar el mismo step.
  `TestLeaseMustCoverStepAndConfirmationBoundaries` solo usa `MaxRetries=0` y no
  alcanza esta frontera.
- Impacto: dos owners pueden solapar una action durante retry; la key reduce el
  riesgo solo en operaciones que ya garantizan idempotencia y no protege
  observaciones, reconciliación ni terminal ownership.
- Corrección requerida: renovar ownership de forma durable antes de cada
  blocking boundary o dimensionar/verificar la lease contra el peor caso total
  `(retries+1)*StepTimeout + retries*RetryDelay + confirmation`, con tests de
  reloj/barriers para reclaim durante el último intento.

## Trazabilidad de segunda revisión

| Requirement | Estado | Motivo |
|---|---|---|
| R1 | rechazado | Provided no es independiente de generación/aprobación y generated rompe IDs locales repetidos (REV-11, REV-14). |
| R2 | rechazado | Fallback público e input ejecutado pueden derivar de la operación sellada (REV-12). |
| R3 | parcial | Validación de grafo pasa; package ordena por declaración (REV-15). |
| R4 | rechazado | Admisión ya precede prepare, pero approval no está ligada al FlowArtifact (REV-11). |
| R5 | rechazado | Request final puede no coincidir con el bound operation (REV-12). |
| R6 | rechazado | Resource binding omite el selector real (REV-13). |
| R7 | parcial | Failed/inconclusive ya gobiernan verdict; una comparación entre recursos distintos aún puede pasar (REV-13). |
| R8 | aprobado | Bloqueos y ramas independientes mantienen semántica determinista. |
| R9 | rechazado | Checkpoints mejorados; lease y journal de intent/retry siguen incompletos (REV-15, REV-16). |
| R10 | aprobado condicionado | `RevisionSet` cierra mutación y el producto declara replanning autónomo deshabilitado (`MaxReplans=0`). |
| R11 | aprobado para fixtures | Primary se conserva y fixture cleanup se finaliza aparte. |
| R12 | rechazado | Evidence manifests mejoraron; los lifecycle events no son el journal real (REV-15). |
| R13 | rechazado | Lease insuficiente durante retries y package no reconstruye recovery real (REV-15, REV-16). |
| R14 | rechazado | Public identity elimina el binding exacto antes del transporte (REV-12). |
| R15 | rechazado | Hay wiring real, pero approval/result no prueban la misma autoridad en todas las superficies (REV-11). |

## Gates de segunda revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- `git diff --check`: pasa; solo muestra avisos informativos LF/CRLF.
- `go test -run '^$' ./...`: pasa.
- `go vet ./...`: pasa.
- `go test -count=3 ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof`: pasa.
- `go test ./...`: pasa.
- `CGO_ENABLED=1 go test -race ./...` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de los tres paquetes afectados: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **1795/2111 = 85,0308 %**. El script incluye todos los `.go` productivos
  tracked/untracked del diff, excluye solo tests y declaraciones sin statements,
  y añade el denominador autenticado de ambos lados de platform files.
- `feature_list.json` permanece intacto, feature 27 es la única
  `in_progress` y no se encontraron temporales de build/coverage dentro del repo.

Los seis hallazgos requieren nuevos tests que fallen contra el código actual;
las pruebas añadidas en esta remediación no ejercitan esas fronteras.

# Tercera revisión independiente — 2026-10-23

## Veredicto: rejected

La tercera remediación cierra REV-11, REV-12, REV-13, REV-14 y la parte
principal de REV-15. También cierra REV-5: las cuatro superficies productivas
usan el mismo `Service` y GitHub ya expone `Result`. REV-3 y REV-16 siguen
abiertos en una frontera posterior al tool; REV-6 queda parcial porque el
journal real no puede finalizar correctamente los runs rechazados antes de
prepare.

| Hallazgo previo | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-3 | parcial | El intent no idempotente evita replay inseguro, pero la claim puede expirar durante confirmación de evidence/assertions (REV-18). |
| REV-5 | cerrado | CLI, API, MCP y GitHub alcanzan start/status/cancel/result en la composición común; GitHub implementa `Result`. |
| REV-6 | parcial | Evidence manifests y package están ligados al resultado real; el terminal pre-prepare no forma un journal verificable (REV-17). |
| REV-11 | cerrado | Approval context/view/grant incluyen el artifact digest y la proyección exacta; A no autoriza B y provided no llama a `Generation.Generate`. |
| REV-12 | cerrado | La autoridad pública es una identidad anonymous explícita, nunca `nil`; method/path/target/capability se validan antes del tool. |
| REV-13 | cerrado | El resource digest incluye operación e input resuelto, incluidos path/query/ID; selectores distintos no comparan. |
| REV-14 | cerrado | La identidad de declaración usa `(stepID, declarationID)` en required evidence, ledger y package; IDs locales repetidos permanecen válidos. |
| REV-15 | parcial | El store conserva journal real con MAC y hash chain y el package valida orden topológico/checkpoints; falla el terminal sin prepare (REV-17). |
| REV-16 | rechazado | El cálculo cubre intentos/backoff/tool timeout, pero no las fronteras posteriores al tool bajo la misma claim (REV-18). |

## REV-17 — Rechazar o cancelar antes de prepare no puede producir un package terminal

- Severidad: blocker
- Requirement o regla: R11, R12, R13; diseño § Flujo end-to-end y § Recovery
- Archivo y línea: `internal/validationworkflow/service.go:828-880`;
  `internal/validationworkflow/functional_flow_publisher.go:75-104,141-143`;
  `internal/functionalflow/package.go:286-307`
- Evidencia: un rechazo humano, cancelación o denegación automática ocurre
  después de `Admit` pero antes de `execute`, por lo que nunca llama a
  `RecordPreparation`. `terminal` construye el summary por defecto con
  `PreparationNotRequired`. `Finalize` ve solo `flow_artifact_committed` y
  añade `fixtures_terminal/not_required`; su fallback `rejected` solo se usa
  cuando el status está vacío, estado que `Service` no entrega. El verifier
  admite el terminal corto de cuatro eventos únicamente para `failed`,
  `cancelled` o `rejected`. Para `not_required` exige un `step_terminal` por
  cada step, pero el siguiente evento ya es `primary_terminal`. Por tanto
  `BuildPackage` falla y `Decide(reject)`/`Cancel` devuelve
  `functional_flow_package_pending` en vez de converger a terminal.
- Impacto: una ruta normal y requerida de aprobación queda permanentemente sin
  resultado/package verificable; el journal además afirma que prepare “no era
  necesario” cuando fue bloqueado por una decisión.
- Corrección requerida: persistir explícitamente `fixtures_terminal/rejected`
  para terminales anteriores a prepare (y conservar `failed`, `cancelled` y
  `not_required` solo en sus casos reales), cerrar la unión de statuses en
  `RecordPreparation`/`VerifyPackage` y probar reject, cancel y auto-denial
  end-to-end con package post-cleanup verificable y recovery idempotente.

## REV-18 — La claim puede expirar después del tool y antes del terminal durable

- Severidad: high
- Requirement o regla: R9, R13, CONV-005, CONV-007
- Archivo y línea: `internal/functionalflow/executor.go:134-138,356-374`;
  `internal/functionalflow/executor_test.go:731-757`
- Evidencia: `totalClaimWindow` se comprueba una sola vez al adquirir la claim.
  El timeout del intento termina en cuanto vuelve `Tool.Execute`, pero después
  `Evidence.Confirm`, los appends del journal, `markActionProgress` y
  `Assertions.Evaluate` usan el `runCtx`; ninguno renueva la lease ni está
  acotado por `ConfirmationTimeout`. Un store/evaluator bloqueado puede dejar
  expirar ownership mientras el primer owner aún confirma el efecto y un
  segundo owner reclama el step. El nuevo test de carrera bloquea dentro del
  último tool y avanza el reloj a 6 s con una TTL de 7 s; libera antes del
  vencimiento y no ejerce evidence/assertion/terminal después de expirar.
- Impacto: owners distintos pueden solaparse tras una llamada real. Para acts
  puede convertir evidencia confirmable en uncertain/conflict; para observes
  permite repetir la llamada y rompe la garantía de owner único y convergencia.
- Corrección requerida: renovar/verificar la claim antes de cada frontera
  bloqueante posterior al tool o ejecutar confirmación/evaluation bajo una
  lease y timeout que cubran toda la sección hasta `ConfirmTerminal`. Añadir un
  probe con reloj y barreras en `Evidence.Confirm` y assertions que avance más
  allá de expiry y pruebe same/different owner bajo `-race`.

## Trazabilidad de tercera revisión

| Requirement | Estado | Motivo |
|---|---|---|
| R1 | aprobado | Contrato estricto y equivalencia generated/provided; provided evita inferencia. |
| R2 | aprobado | Binding exacto, identidad explícita y clasificación de efecto sellada. |
| R3 | aprobado | Grafo/referencias y orden topológico se validan y reconstruyen. |
| R4 | aprobado | Compose/validate/bind/persist y approval preceden prepare. |
| R5 | aprobado | Cada intento revalida binding/policy/budget/identity y deriva el request sellado. |
| R6 | aprobado | Before/after requiere resource-binding digest idéntico del selector concreto. |
| R7 | aprobado | Failed/inconclusive y `StepFailed` gobiernan ledger/verdict. |
| R8 | aprobado | Ramas bloqueadas conservan causas y las independientes continúan. |
| R9 | rechazado | Checkpoints conservadores existen, pero ownership puede expirar tras el tool (REV-18). |
| R10 | aprobado | Revisions son monotónicas, no reescriben assertions/resultados; producto usa `MaxReplans=0`. |
| R11 | rechazado | Cleanup se intenta, pero rechazo/cancel pre-prepare no converge a package terminal (REV-17). |
| R12 | rechazado | Journal real/MAC/hash chain pasan salvo la máquina causal pre-prepare (REV-17). |
| R13 | rechazado | Persistencia/CAS/MAC pasan; queda overlap de owners en confirmación (REV-18). |
| R14 | aprobado | Canarios, inputs resueltos, captures y credenciales no cruzan artifacts/package/superficies. |
| R15 | aprobado | Paridad vertical real, aliases/artifacts/recovery y GitHub `Result` están cableados. |

## Gates de tercera revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- Probes focales de approval A/B, provided sin generación, drift cero-red,
  selectores de recurso, assertion IDs repetidos, journal y lease: pasan.
- `git diff --check`: pasa; solo avisos informativos LF/CRLF.
- `go test -run '^$' ./...`: pasa.
- `go vet ./...`: pasa.
- `go test -count=3 ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof`: pasa.
- `go test -count=1 ./...`: pasa.
- `CGO_ENABLED=1 go test -race ./...` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de
  `internal/functionalflow`, `internal/validationworkflow` y
  `cmd/featureproof`: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **2256/2653 = 85,0358 %**. La auditoría confirma inclusión de todos los `.go`
  productivos tracked/untracked del diff; las únicas exclusiones son tests y
  tres archivos de declaraciones sin statements, y los cuatro helpers de
  plataforma están fijados por hash y denominador.
- `feature_list.json` permanece idéntico a `b81cee0`, solo feature 27 está
  `in_progress`, no aparece alcance de feature 28/29 y no quedaron temporales
  de build dentro del repositorio.

Los dos hallazgos necesitan tests que fallen contra el código actual. Los gates
verdes no alcanzan las dos secuencias descritas.

# Cuarta revisión independiente — 2026-10-23

## Veredicto: rejected

REV-17 queda cerrado: reject humano, cancel y auto-denial posteriores a Admit
persisten `fixtures_terminal/rejected`, publican exactamente cuatro eventos y
no inventan steps, evidence, tools ni trabajo de fixtures. La proyección de
Result coincide en CLI, API, MCP y GitHub. REV-18 queda cerrado dentro de
`functionalflow.Executor`, pero la recuperación no está integrada en el
workflow productivo; por ello R9/R12/R13 siguen rechazados.

| Hallazgo previo | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-17 | cerrado | Unión cerrada de preparation, terminal corto verificable, package post fixture-cleanup y paridad de Result pasan repetidamente. |
| REV-18 | parcial | `RenewClaim` y `ConfirmationTimeout` protegen evidence/journal/intent/assertions/terminal; el `Service` descarta esa recovery al primer error (REV-19). |

## REV-19 — Un fallo de renovación recuperable se convierte en cleanup irreversible y package imposible

- Severidad: blocker
- Requirement o regla: R9, R12, R13; diseño § Recovery y ownership
- Archivo y línea: `internal/validationworkflow/functional_flow.go:186-203`;
  `internal/validationworkflow/service.go:485-491,550-551,692-702`;
  `internal/validationworkflow/functional_flow_publisher.go:72-79`;
  `internal/functionalflow/package.go:184-192`
- Evidencia: los tests nuevos demuestran recuperación llamando manualmente una
  segunda vez a `Executor.Execute` después de que `RenewClaim` falle. La
  vertical real no hace esa llamada. `FunctionalFlowExecutionAdapter.Execute`
  propaga el error; `Service.execute` ya está en `PhaseExecuting` y llama
  inmediatamente a `terminal`, fijando `CleanupPending`. Si no se llegó a
  `Publisher.Stage`, `Finalize` fabrica un primary sin steps. Como preparation
  ya era `prepared/not_required`, `VerifyPackage` exige todos los steps y
  rechaza ese package. En redelivery, `Recover` da prioridad a
  `CleanupPending` y vuelve a `terminal`; si el crash ocurrió antes de ese flag,
  `PhaseExecuting` también va directamente a `terminal`. Ninguna rama vuelve a
  invocar el executor durable.
- Impacto: un fallo transitorio de renovación, journal, evidence o assertion
  posterior al tool no converge. Para observe se pierde la recuperación segura
  que prueba el test unitario; para act no se reconstruyen intent/evidence y el
  workflow queda en `functional_flow_package_pending`, sin Result terminal ni
  paridad de superficies.
- Corrección requerida: distinguir interrupción recuperable de resultado
  primario terminal. En `Recover`, recomponer el artifact aprobado y reanudar
  el executor desde sus checkpoints antes de iniciar cleanup; solo después de
  un `Result` primario durable debe fijarse cleanup/package. Añadir probes
  verticales `Service` con fallo en cada renovación post-tool, crash antes y
  después de evidence/intent/terminal, redelivery por otro owner y Result final
  en las cuatro superficies. Para un act incierto, la reanudación debe producir
  inconclusive sin repetir el efecto.

## Trazabilidad de cuarta revisión

| Requirement | Estado | Motivo |
|---|---|---|
| R1–R8 | aprobado | Regresión de contrato, binding, grafo, admission, request, comparaciones, verdict y ramas bloqueadas pasa. |
| R9 | rechazado | Checkpoints y renewal existen, pero `Service` no reanuda la ejecución durable (REV-19). |
| R10 | aprobado | Revisions monotónicas y `MaxReplans=0` conservan assertions/resultados. |
| R11 | aprobado | Reject/cancel/denial y resultados primarios normales conservan cleanup separado. |
| R12 | rechazado | El package real no puede cerrarse tras un error recuperable post-tool (REV-19). |
| R13 | rechazado | Store/lease/CAS/MAC pasan; falta recovery productivo desde checkpoints (REV-19). |
| R14 | aprobado | Canarios y credenciales permanecen fuera de límites durables/externos. |
| R15 | aprobado | CLI/API/MCP/GitHub comparten Start/Status/Cancel/Result y authority. |

## Gates de cuarta revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- Probes focales repetidos (`-count=5`) de reject/cancel/auto-denial, package
  corto, paridad de Result, renewals, timeout y LocalStore: pasan.
- `git diff --check`: pasa; solo avisos informativos LF/CRLF.
- `go test -run '^$' ./...`: pasa.
- `go vet ./...`: pasa.
- `go test -count=3 ./internal/functionalflow ./internal/validationworkflow ./cmd/featureproof`: pasa.
- `go test -count=1 ./...`: pasa.
- `CGO_ENABLED=1 go test -race ./...` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de
  `internal/fixture`, `internal/functionalflow`, `internal/validationworkflow`
  y `cmd/featureproof`: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **2349/2763 = 85,0163 %**. La auditoría incluye todo el diff productivo,
  añade `internal/fixture`, excluye solo declaraciones sin statements y fija
  los helpers de plataforma por hash/denominador.
- `feature_list.json` permanece idéntico a `b81cee0`, solo feature 27 está
  `in_progress`, no hay alcance de feature 28/29 ni temporales dentro del repo.

El hallazgo requiere una prueba vertical que falle contra el código actual;
los tests de renewal existentes terminan en el executor y no atraviesan
`validationworkflow.Service`.

# Quinta revisión independiente — 2026-10-23

## Veredicto: rejected

REV-19 queda parcialmente corregido: `Service` conserva una ejecución
recuperable en `PhaseExecuting`, recompone el request aprobado, no repite
`Prepare`, no inicia cleanup antes del primary y permite que otro owner retome
los checkpoints. Cancelación y fallos no recuperables terminan por la ruta
causal compartida. Sin embargo, la nueva integración todavía pierde una
ventana de crash situada entre el último checkpoint del executor y el flag del
workflow. Esa ventana deja un journal auténtico que la siguiente recuperación
corrompe semánticamente, por lo que R9, R12 y R13 continúan rechazados.

| Hallazgo previo | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-1–REV-18 | cerrado | Regresión completa, probes focales repetidos, carrera global y packages autenticados pasan. |
| REV-19 | parcial | Los errores propagados por el adapter quedan pending y se reanudan, pero una caída después de completar el executor y antes de persistir `FunctionalFlowPrimaryDurable` no converge (REV-20). |

## REV-20 — Reejecutar un flujo ya terminal añade eventos causales imposibles

- Severidad: blocker
- Requirement o regla: R9, R12, R13; design § Recovery, durable journal y
  package reconstruction
- Archivo y línea: `internal/functionalflow/executor.go:67-80`;
  `internal/functionalflow/package.go:358-423`;
  `internal/validationworkflow/functional_flow.go:203-237`;
  `internal/validationworkflow/service.go:716-737,756-791`
- Evidencia: `FunctionalFlowExecutionAdapter.Execute` ejecuta todos los pasos y
  después hace `Publisher.Stage`; solo tras retornar, `Service` guarda
  `FunctionalFlowPrimaryDurable=true`. Si el proceso cae después del último
  `step_terminal` (antes o después de `Stage`) pero antes de esa transición, el
  record durable sigue `ExecutionPending=true`. `Recover` vuelve a invocar
  `Executor.Execute`. Para cada terminal ya existente, el executor añade
  `recovery_resumed` y otro `step_terminal`. El verificador rico prohíbe
  `recovery_resumed` cuando `state.terminal` ya es verdadero y también prohíbe
  un segundo terminal.
- Probe propio: un test aplicado mediante `go test -overlay` fuera del repo
  ejecutó un flow real con `LocalStore`/journal, simuló el crash justo después
  del primer `Execute`, creó un executor de otro owner y ejecutó la recovery.
  No se repitieron tools, pero `BuildPackage` falló exactamente con
  `functional flow: integrity at package.lifecycle.recovery`; el journal
  contenía los terminales originales seguidos de
  `recovery_resumed/step_terminal` duplicados.
- Cobertura insuficiente del caso: `recoveringFlowExecutor` en
  `internal/validationworkflow/functional_flow_test.go:227-261` es un fake que
  falla antes de crear checkpoints reales y, al reanudar, solo añade
  `step_terminal`. No genera `step_claimed`, `attempt_started` ni evidence, así
  que el package usa deliberadamente la máquina legacy (`package.go:318-326`)
  y no prueba la recuperación del journal rico que usa el executor productivo.
- Impacto: un reinicio legítimo puede ejecutar cleanup y luego quedar sin
  package/Result verificable. La redelivery posterior repite los mismos eventos
  inválidos; otro owner y CAS no reparan la causalidad. El efecto `act` no se
  repite, pero la evidencia final deja de ser publicable.
- Corrección requerida: hacer durable e idempotente la frontera
  executor-primary. Antes de reejecutar, recovery debe reconocer un primary ya
  staged o reconstruirlo de terminales sin añadir eventos ya confirmados. Si un
  terminal de estado puede preceder a su evento, el append de recovery debe
  distinguir ese crash parcial y completar solo el evento ausente. Añadir dos
  probes verticales con el executor real: crash (a) tras el último
  `step_terminal` y antes de `Stage`, y (b) tras `Stage` y antes de
  `primary_result_staged`; ambos deben acabar con un único journal causal,
  package autenticado y Result idéntico en CLI/API/MCP/GitHub.

## Trazabilidad de quinta revisión

| Requirement | Estado | Motivo |
|---|---|---|
| R1–R8 | aprobado | Strict v1, equivalencia provided/generated, bindings, grafo, privacidad, admission, request, assertions y blocked branches mantienen la regresión verde. |
| R9 | rechazado | La recovery de checkpoints terminales duplica eventos después del crash final (REV-20). |
| R10 | aprobado | Replans siguen monotónicos y conservan resultados/assertions anteriores. |
| R11 | aprobado | En rutas que convergen, primary durable precede cleanup y finalize; cancel y fallo no recuperable usan la ruta terminal compartida. |
| R12 | rechazado | El package real no puede reconstruirse después de la ventana de crash probada. |
| R13 | rechazado | HMAC/CAS/leases y owners concurrentes pasan, pero el restart durable no es idempotente al final de ejecución. |
| R14 | aprobado | Canarios y credenciales continúan fuera de artifacts, journals y superficies. |
| R15 | aprobado | Las cuatro superficies comparten Start/Status/Cancel/Result y la misma autoridad. |

## Gates de quinta revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- `gofmt -l` sobre todos los `.go`: limpio.
- `git diff --check b81cee0`: pasa; solo avisos informativos LF/CRLF.
- `go vet ./...`: pasa.
- `go test ./... -run '^$'`: pasa.
- `go test ./internal/functionalflow ./internal/validationworkflow -count=5`:
  pasa.
- `go test ./... -count=1`: pasa.
- `CGO_ENABLED=1 go test -race ./... -count=1` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de
  `cmd/featureproof`, `internal/functionalflow` e
  `internal/validationworkflow`: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **2435/2864 = 85,0209 %**. La auditoría incluye todos los fuentes productivos
  modificados/untracked, cuenta implementaciones de plataforma mediante hashes
  fijados y excluye únicamente cuatro archivos de declaraciones sin statements.
- Probe externo `go test -overlay ... ./internal/functionalflow -run
  TestReviewCrashAfterExecutorBeforeWorkflowFlag -count=1`: falla de forma
  esperada en `package.lifecycle.recovery` y demuestra REV-20.
- `feature_list.json` permanece idéntico a `b81cee0`; la rama es
  `feature-27-functional-flow-validation`, no hay alcance de feature 28/29 ni
  temporales del reviewer dentro del repositorio.

Los gates ordinarios no cubren la ventana REV-20. El veredicto seguirá siendo
rechazado hasta que una recovery real posterior al último terminal produzca un
único journal causal y un package verificable.

# Sexta revisión independiente — 2026-10-23

## Veredicto: rejected

REV-20 queda cerrado: con executor, LocalStore, journal, evidence y publisher
reales, los crashes antes de `Stage` y después de `Stage` pero antes del flag
promueven el primary sin volver a entrar en `Execute`, sin repetir tool,
`recovery_resumed` ni `step_terminal`. La redelivery concurrente converge por
lease/CAS, el terminal de state perdido se reconcilia desde journal y un
journal parcial sí reanuda el executor una sola vez. El package resultante es
verificable y las cuatro superficies leen el mismo Result.

La promoción, sin embargo, valida una máquina causal más débil que la del
package. Puede aceptar divergencia autenticada entre journal, evidence y state,
marcar el primary durable y habilitar cleanup; `Finalize` descubre el problema
demasiado tarde. Por ello la feature continúa rechazada.

| Hallazgo previo | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-1–REV-19 | cerrado | Regresión completa y gates mantienen los contratos anteriores. |
| REV-20 | cerrado | Los probes productivos de crash pre/post Stage conservan Execute=1, tool=1, un terminal, cero resumes duplicados, package válido y Results paritarios. |

## REV-21 — La promoción acepta evidence causalmente divergente y permite cleanup antes de fallar el package

- Severidad: blocker
- Requirement o regla: R11, R12, R13; design § Recovery, authenticated
  evidence y package reconstruction; mandato de fail closed sin cleanup ante
  tampering/divergencia
- Archivo y línea: `internal/functionalflow/recovery.go:87-203`;
  `internal/functionalflow/recovery.go:206-252`;
  `internal/functionalflow/package.go:358-430`;
  `internal/validationworkflow/functional_flow.go:226-252`;
  `internal/validationworkflow/service.go:779-791`
- Evidencia: `completedRuntimeEvents` comprueba que
  `evidence_confirmed` y `assertions_confirmed` contienen exactamente un ID,
  pero no que sea el mismo ID ni que coincida con el `step_terminal`, el
  terminal de state o el evidence realmente cargado. `terminalMatchesEvent`
  solo enlaza state con `step_terminal`. La promoción reconstruye desde state,
  ignora la divergencia de los eventos intermedios, devuelve `complete=true` y
  el adapter hace `Stage`. Service persiste entonces
  `FunctionalFlowPrimaryDurable=true` y entra en cleanup. La máquina rica del
  package sí exige ownership y enlaces exactos, por lo que `Finalize` termina
  rechazando la misma historia.
- Probe propio: mediante `go test -overlay` fuera del repo se ejecutó un flow
  real con `Executor`, `LocalStore`, journal y evidence reales. Después se
  cambió solo el ID de `evidence_confirmed`, se reconstruyeron la hash-chain y
  el MAC mediante el propio store, dejando intactos
  `assertions_confirmed`, state y `step_terminal`. `RecoverCompleted` promovió
  el checkpoint con `complete=true` y `err=nil`. Este es un desacuerdo
  semántico autenticado, no el caso trivial de bytes con MAC roto que cubre
  `TestCompletedJournalTamperingFailsClosedBeforeStageOrCleanup`.
- Impacto: corrupción o divergencia interna puede disparar cleanup irreversible
  y dejar el workflow sin package/Result verificable. No hay replay de tool,
  pero el requisito explícito era cerrar antes de Stage y cleanup.
- Corrección requerida: usar en promoción la misma causalidad que se exige al
  package. Como mínimo, ligar exactamente el evidence ID de
  `evidence_confirmed` → `assertions_confirmed` → `step_terminal` → state,
  cargar y validar ownership/run/revision/step/binding del evidence, y validar
  los resultados de assertions/ledger reconstruidos. Si ya existen
  `primary_terminal` o cleanup, verificar también outcome y conjunto completo
  de evidence. Añadir un probe vertical Service que introduzca divergencias
  MAC-válidas en cada enlace y demuestre cero `Stage`, cero cleanup, cero replay
  y record todavía pending.

## Trazabilidad de sexta revisión

| Requirement | Estado | Motivo |
|---|---|---|
| R1–R8 | aprobado | Contrato, binding, grafo, privacidad, admission, controles, comparisons, ledger y blocked branches mantienen la regresión verde. |
| R9 | aprobado | Intent/effect checkpoints, leases, recovery parcial y promoción completa evitan repetir I/O. |
| R10 | aprobado | Replans siguen monotónicos y preservan authority, assertions y resultados previos. |
| R11 | rechazado | Una promoción causalmente inválida habilita cleanup antes de que el package detecte la divergencia (REV-21). |
| R12 | rechazado | Recovery y package no validan la misma máquina causal; el primary promovido puede ser imposible de empaquetar. |
| R13 | rechazado | Store/CAS/leases/restart pasan, pero una divergencia autenticada no falla antes de Stage/cleanup. |
| R14 | aprobado | Canarios, captures, inputs y credenciales permanecen fuera de límites públicos/durables. |
| R15 | aprobado | CLI/API/MCP/GitHub conservan Start/Status/Cancel/Result y Results paritarios en recoveries válidas. |

## Gates de sexta revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- Probes de promoción, journal parcial, state perdido y tampering, repetidos
  diez veces: pasan.
- `gofmt -l` sobre todos los `.go`: limpio.
- `git diff --check b81cee0`: pasa; solo avisos informativos LF/CRLF.
- `go vet ./...`: pasa.
- `go test ./... -run '^$'`: pasa.
- `go test ./internal/functionalflow ./internal/validationworkflow
  ./cmd/featureproof -count=5`: pasa.
- `go test ./... -count=1`: pasa.
- `CGO_ENABLED=1 go test -race ./... -count=1` con UCRT64: pasa.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de
  `cmd/featureproof`, `internal/functionalflow` e
  `internal/validationworkflow`: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **2614/3075 = 85,0081 %**. La auditoría incluye todos los fuentes productivos
  modificados/untracked, incluido `recovery.go`; los únicos archivos omitidos
  no contienen statements y los helpers de plataforma quedan fijados por hash.
- Probe externo `go test -overlay ... ./internal/functionalflow -run
  TestReviewCompletedPromotionRejectsEvidenceEventDivergence -count=1`: falla
  porque la implementación devuelve `complete=true, err=nil`, demostrando
  REV-21.
- `feature_list.json` permanece idéntico a `b81cee0`; la rama es
  `feature-27-functional-flow-validation`, no aparece alcance de feature 28/29
  ni temporales del reviewer dentro del repositorio. Las funciones productivas
  nuevas conservan comentarios de función y propósito sistémico.

Los gates verdes prueban la recovery honesta. Falta cerrar la equivalencia
entre la validación de promoción y la validación final del package para que
ninguna historia que no pueda empaquetarse alcance cleanup.

# Séptima revisión independiente — 2026-10-23

## Veredicto: approved

REV-21 queda cerrado. `RecoverCompleted` reconstruye el primary y el manifest,
crea únicamente en memoria los eventos terminales que aún faltan y llama al
mismo `BuildPackage` que usa la publicación final **antes** de reconciliar
state, hacer CAS, `Stage`, persistir el primary o iniciar cleanup. Por tanto la
promoción ya no acepta una historia que el package rechazaría después.

Los probes con LocalStore y evidence store reales, cadenas rehechas y MAC
válidas rechazan divergencias de ID, record digest, binding de operación,
binding de recurso, content digest, step/revision, referencias de assertions y
comparisons, terminal/state, primary evidence y estructura causal. El rechazo
mantiene el record pending, Execute=1, tool=1, Stage durable=0 y cleanup=0. Las
promociones nominales, el terminal perdido, CAS idempotente y dos recoveries
concurrentes convergen en un solo primary/package sin duplicar eventos ni I/O.

| Hallazgo previo | Estado | Evidencia de esta revisión |
|---|---|---|
| REV-1–REV-20 | cerrado | Regresión completa R1–R15, suites focales y verticales permanecen verdes. |
| REV-21 | cerrado | La promoción delega en `BuildPackage` antes de cualquier mutación y los ataques semánticos autenticados fallan sin Stage/cleanup/replay. |

## Revalidación de REV-21

- `completedRuntimeEvents` rechaza gaps, duplicados, forks lineales, reorder,
  referencias desconocidas y orden terminal no topológico.
- `verifyCompletedRuntimeProof` carga cada evidence alcanzable por el ledger,
  sella su `EvidenceRecord` y pasa artifact, manifest, journal y primary a
  `BuildPackage`.
- La máquina rica liga
  `evidence_confirmed.{ID,binding,resource,content,record}` con el manifest;
  `assertions_confirmed` y `comparisons_confirmed` con las coordenadas exactas
  del ledger; `step_terminal` con state; y `primary_terminal` con el conjunto
  canónico completo de evidence.
- La lista de reconciliaciones se aplica solo después de esa verificación. Un
  error no puede modificar state ni habilitar publication/cleanup.
- El probe externo de la sexta revisión, que reautentica una divergencia de
  `evidence_confirmed`, ahora pasa cinco veces: la promoción devuelve error y
  `complete=false`.
- `TestAuthenticatedSemanticDivergenceFailsBeforeStage` atraviesa Service con
  journal/evidence reales y comprueba pending/error, cero segundo Execute/tool,
  cero Stage durable y cero cleanup. Los tests de package cubren missing,
  duplicate, fork/reorder, ownership y referencias foráneas.

## Trazabilidad final

| Requirement | Estado | Motivo |
|---|---|---|
| R1 | aprobado | Generated/provided convergen en el contrato estricto `FunctionalFlow/v1`. |
| R2 | aprobado | Operación, identidad y clasificación effectful quedan ligadas exactamente. |
| R3 | aprobado | Grafo, ancestry, referencias tipadas, límites y privacidad fallan antes de efectos. |
| R4 | aprobado | Admit compone, valida, bindea y persiste antes de approval y fixtures. |
| R5 | aprobado | Cada intento revalida policy, budget, identidad y request exacto antes de red. |
| R6 | aprobado | Observaciones y comparisons requieren evidence compatible y controlan el resultado. |
| R7 | aprobado | Assertions pre/post, required evidence, ledger y verdict no pueden producir falsos passed. |
| R8 | aprobado | Ramas bloqueadas conservan todas las causas y no detienen ramas independientes. |
| R9 | aprobado | Intents, uncertain effects, leases, retries y recovery impiden replay inseguro. |
| R10 | aprobado | Revisions son monotónicas; fixtures, assertions y resultados previos son inmutables. |
| R11 | aprobado | Primary precede siempre al cleanup; fallos de prueba quedan pending sin cleanup. |
| R12 | aprobado | Package post-cleanup autentica artifact, evidence, journal, primary y causalidad completa. |
| R13 | aprobado | HMAC/CAS/leases, restart, state perdido y promoción concurrente convergen de forma idempotente. |
| R14 | aprobado | Captures, literals, keys, credenciales y canarios no cruzan límites públicos/durables. |
| R15 | aprobado | CLI/API/MCP/GitHub comparten Service y Start/Status/Cancel/Result, incluido recovery. |

T1–T16 están completas y trazadas a pruebas verídicas. No aparece alcance de
feature 28/29.

## Gates de séptima revisión

Ejecutados sobre el diff completo desde `b81cee0`:

- Probes focales de recovery/promoción/divergencia repetidos diez veces:
  pasan.
- Probe externo reautenticado de REV-21 con `go test -overlay`, `-count=5`:
  pasa.
- `gofmt -l` sobre todos los `.go`: limpio.
- `git diff --check b81cee0`: pasa; solo avisos informativos LF/CRLF.
- `go vet ./...`: pasa.
- `go test ./... -run '^$'`: pasa.
- `go test ./internal/functionalflow ./internal/validationworkflow
  ./cmd/featureproof -count=5`: pasa.
- `go test ./... -count=1`: pasa.
- `CGO_ENABLED=1 go test -race ./... -count=1 -p=1`: pasa sobre todos los
  paquetes. Dos intentos paralelos previos fallaron por timeout funcional bajo
  carga, uno en `functionalflow` y otro en `previewenv`, sin reporte de data
  race; ambos probes pasan focalmente 20 y 10 veces respectivamente. La
  ejecución global serializada confirma el código bajo el detector sin esa
  contención entre paquetes.
- Cross compile `go test -c` para `linux/amd64` y `darwin/amd64` de
  `cmd/featureproof`, `internal/functionalflow` e
  `internal/validationworkflow`: pasa.
- `PYTHONDONTWRITEBYTECODE=1 python scripts/feature27_coverage.py`: pasa,
  **2674/3137 = 85,2407 %**. La auditoría cubre todo el diff productivo desde
  `b81cee0`, incluidos archivos untracked y `recovery.go`; solo excluye
  declaraciones sin statements y autentica los helpers de plataforma por hash.
- `feature_list.json` permanece idéntico a `b81cee0`; la rama es
  `feature-27-functional-flow-validation`, no hay temporales del reviewer en el
  repo y las funciones/entidades nuevas conservan comentarios sistémicos.

Feature 27 satisface R1–R15 y puede pasar de `in_progress` a `done` por el
leader.
