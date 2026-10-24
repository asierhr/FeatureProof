# Review independiente de context-assembly

## Revisión 1 — 2026-10-24

**Veredicto: rejected**

Se verificaron las precondiciones del proceso: existe una sola feature
`in_progress`, la spec tiene aprobación humana, T1–T16 aparecen marcadas y el
informe del implementer existe. La implementación no satisface todavía varios
requirements materiales.

## REV-1 — Producers pueden elevar contenido a instrucciones confiables

- Severidad: blocker
- Requirement o regla: R4, R14, ARCH-008, ARCH-009
- Archivo y línea: `internal/contextassembly/assembler.go:421`, `internal/contextassembly/assembler.go:427`
- Evidencia: `eligibilityReason` acepta `TrustInstructions` para cualquier
  source si el propio item declara `AuthorityComposition`; también permite a
  `SourceConfig` declarar `TrustInstructions`. La trust class procede del item
  entregado por el producer, en vez de asignarse de forma irreversible por el
  `InstructionCatalog` y el tipo de producer registrado. El test solo prueba
  `TrustInstructions` con autoridad `verified`, no la combinación adversaria
  `composition + trusted_instructions`.
- Impacto: un diff, contrato, memoria u otra proyección registrada puede colocar
  texto controlado por una fuente en la sección de instrucciones del sistema.
- Corrección requerida: asignar la sección desde composición mediante contratos
  de producer separados; reservar `trusted_instructions` exclusivamente al
  catálogo sellado y `approved_configuration` exclusivamente al producer de
  configuración. Añadir la matriz adversaria para todos los source kinds y
  autoridades.

## REV-2 — El runtime no conserva la frontera Instructions/Untrusted del snapshot

- Severidad: blocker
- Requirement o regla: R4, R12
- Archivo y línea: `internal/agentplanning/generator.go:69`, `internal/agentplanning/generator.go:258`, `internal/validationworkflow/context_assembly.go:80`
- Evidencia: `Envelope` incluye `Instructions`, `Configuration` y `Untrusted` en
  un único JSON. `marshalModelInput` envía ese JSON completo como
  `StructuredRequest.Untrusted`, mientras `Generate` sigue enviando la constante
  legacy `planningInstructions` como `StructuredRequest.Instructions`. El test
  `TestGeneratorUsesAssembledEnvelopeExclusively` solo compara `Untrusted` y no
  inspecciona `Instructions`.
- Impacto: las instrucciones selladas por el perfil no ocupan el canal de
  instrucciones del modelo y permanece una autoridad legacy paralela; la
  separación exigida no llega al boundary real del proveedor.
- Corrección requerida: transportar las instrucciones del snapshot en
  `StructuredRequest.Instructions`, enviar solo configuración y datos con
  delimitación tipada en `Untrusted`, y eliminar la autoridad legacy cuando hay
  assembly. Verificar ambos campos en el fake de modelo y rechazar envelope y
  digest no coincidentes.

## REV-3 — Bytes y tokens no se miden sobre el payload model-facing final

- Severidad: blocker
- Requirement o regla: R6, R9
- Archivo y línea: `internal/contextassembly/assembler.go:516`, `internal/contextassembly/assembler.go:545`, `internal/validationworkflow/context_assembly.go:80`
- Evidencia: `measureSnapshot` cuenta el JSON completo de `Snapshot`, pero el
  modelo recibe otro tipo, `ModelEnvelope`. Además, `PlanID` se añade al envelope
  después de cerrar packing, counts y digest. Los tests comprueban únicamente
  `MaxItems`; no prueban los bordes exactos de bytes/tokens sobre los bytes
  realmente enviados ni un counter no monótono.
- Impacto: `Counts.Bytes/Tokens` no describen la request real y no demuestran que
  el input final respete sus límites para el provider/model seleccionado.
- Corrección requerida: construir primero el envelope model-facing completo,
  incluido `plan_id`, contar exactamente esos mismos bytes y usar ese resultado
  para packing y confirmación final. Cubrir igualdad exacta, overhead y counters
  controlados en los límites.

## REV-4 — La suficiencia no distingue fuentes obligatorias y opcionales

- Severidad: blocker
- Requirement o regla: R6, R8
- Archivo y línea: `internal/contextassembly/assembler.go:505`, `internal/contextassembly/assembler.go:614`
- Evidencia: `buildSnapshot` convierte cualquier conflicto en
  `insufficient_context`, aunque pertenezca a una fuente opcional. En sentido
  contrario, una omisión por budget, state, validity o sensitivity solo marca
  `partial`, aunque elimine todos los items de una fuente obligatoria. La
  configuración obligatoria se empaqueta como un candidato normal y puede ser
  descartada con resultado utilizable. El profile tampoco contiene la política
  de contradicción/parcialidad aprobada por el diseño.
- Impacto: se puede iniciar inferencia sin contexto obligatorio y se bloquea
  innecesariamente ante conflictos opcionales.
- Corrección requerida: calcular suficiencia por source kind y política del
  perfil después del packing; reservar instrucciones y configuración
  obligatoria antes de candidatos, y diferenciar conflictos required/optional.
  Añadir tests de fuente obligatoria totalmente filtrada/oversized y conflicto
  opcional.

## REV-5 — El modelo de perfiles omite allowlists de campos y límites de trabajo requeridos

- Severidad: high
- Requirement o regla: R1, R2, R13, ARCH-005
- Archivo y línea: `internal/contextassembly/assembler.go:12`, `internal/contextassembly/model.go:199`, `internal/contextassembly/model.go:219`
- Evidencia: `Profile` solo limita source kinds y sensibilidad; no modela clases
  o campos por source ni permite que el request los estreche. `Budgets` tampoco
  limita número de projections/candidatos o duración. Un producer puede devolver
  una colección o payload arbitrariamente grande antes de que `MaxItems` actúe.
- Impacto: no puede demostrarse least privilege por campos ni trabajo finito en
  source read, validación, canonicalización y secret scan.
- Corrección requerida: incorporar allowlists de campos/clases, narrowing
  explícito, máximos de sources/candidatos/payload y deadline/duración; validar
  esos límites antes de materializar o canonicalizar colecciones grandes.

## REV-6 — El secret gate no representa redaction state ni cubre las clases sensibles de R14

- Severidad: blocker
- Requirement o regla: R14, ARCH-009
- Archivo y línea: `internal/contextassembly/adapters.go:75`, `internal/contextassembly/model.go:240`
- Evidencia: `Item` no posee estado de redacción conocido y `CanaryGate` solo
  busca canarios configurados, bearer, private key y dos nombres de credential.
  No existe fail-closed para estado de redacción desconocido ni detección
  estructural de cookies, connection strings, variables sensibles, bodies no
  redactados, prompts o chain of thought. Los tests solo ejercitan un canario y
  un bearer.
- Impacto: material prohibido por R14 puede alcanzar ranking, persistencia y el
  modelo sin activar el gate.
- Corrección requerida: exigir provenance/redaction state autenticado en cada
  proyección y un detector compuesto fail-closed que cubra la taxonomía de la
  spec, con canarios anidados en metadata, payloads, errors, manifests, journal y
  model request.

## REV-7 — El store no implementa el journal append-only ni carga bytes canónicos strict

- Severidad: blocker
- Requirement o regla: R1, R9, R11, T10, ARCH-007
- Archivo y línea: `internal/contextassembly/store.go:15`, `internal/contextassembly/store.go:109`, `internal/contextassembly/store.go:202`, `internal/contextassembly/store.go:253`
- Evidencia: el store solo crea `snapshots`, `bindings` y `receipts`; no existe el
  journal encadenado especificado. `loadSnapshotLocked` y `loadBindingLocked`
  usan `json.Unmarshal`, que acepta unknown y duplicate fields, y recalculan el
  digest sobre el valor tipado sin comparar los bytes cargados con la
  serialización canónica. Un objeto con whitespace, campo desconocido o clave
  duplicada equivalente puede ser aceptado.
- Impacto: no hay historial auditable append-only y recovery puede aceptar una
  representación persistida no canónica que no fue la publicada originalmente.
- Corrección requerida: añadir journal content-free append-only con integridad y
  receipts idempotentes; efectuar decode strict y comparar bytes canónicos antes
  de aceptar objetos. Probar truncado, reordenado, unknown/duplicate fields y
  rollback coordinado.

## REV-8 — Persistencia local mantiene ventanas TOCTOU y no observa cancelación en boundaries

- Severidad: blocker
- Requirement o regla: R11, R13, R14, T10
- Archivo y línea: `internal/contextassembly/store.go:176`, `internal/contextassembly/store.go:270`, `internal/contextassembly/store.go:326`, `internal/contextassembly/store.go:370`
- Evidencia: `validateLayout` termina antes de las operaciones pathname de
  `atomicWrite`; `CreateTemp`, `Link`, `Remove` y apertura del parent vuelven a
  resolver directorios reemplazables. No se verifican identidades después de
  cada boundary, los errores de parent sync se ignoran y no se consulta el
  contexto durante write/sync/publication. Los tests solo reemplazan el root
  antes de una operación y no inyectan swaps/fallos en cada boundary.
- Impacto: un swap concurrente puede redirigir publicación o lectura, y una
  cancelación durante persistencia puede dejar un snapshot publicable.
- Corrección requerida: operar mediante handles/dirfd no-follow estables o una
  primitiva equivalente por plataforma, validar identidades alrededor de cada
  boundary, propagar fallos de sync y cancelación, y añadir matrices
  deterministas de root/subdir/temp/object swap, symlink/reparse/hardlink y
  write/sync/rename/parent-sync.

## REV-9 — El plan preparado y su provenance no sellan el contexto ensamblado

- Severidad: high
- Requirement o regla: R9, R10, R12
- Archivo y línea: `internal/agentplanning/generator.go:172`, `internal/agentplanning/model.go:281`, `internal/validationworkflow/artifact.go:31`
- Evidencia: `generationProvenance` sigue recibiendo únicamente constraints,
  contract, capabilities e identities. `PreparedSnapshot` solo contiene ID y
  checks, y `artifactContext` omite `ContextDigest/ContextEnvelope`. El comentario
  añadido a `GenerationRequest.ContextDigest` afirma que liga provenance, pero
  ninguna de esas funciones lo incorpora. El snapshot de context assembly
  tampoco contiene el `plan_id`; se agrega solo al envelope posterior.
- Impacto: el prepared plan digest no prueba qué contexto model-facing produjo
  la decisión y los checks de provenance no detectan su sustitución.
- Corrección requerida: incluir el digest del snapshot en provenance y en la
  representación privada restaurable del plan, validar correspondencia
  envelope/digest y sellar el `plan_id` inicial dentro del snapshot canónico.

## REV-10 — Analyzer/diagnostic y paridad de superficies no tienen integración productiva verificable

- Severidad: high
- Requirement o regla: R2, R12, T14
- Archivo y línea: `internal/validationworkflow/context_assembly.go:33`, `internal/contextassembly/assembler.go:737`, `cmd/featureproof/workflow.go:604`
- Evidencia: `ContextAssemblyAdapter.Prepare` está fijado a `RolePlanner`. Los
  roles analyzer/diagnostic solo atraviesan el método genérico `Consume` en un
  test unitario; no existe consumer productivo controlado en workflow. El test
  citado para CLI/API/MCP/GitHub es histórico y no compara snapshot/context
  digest ni prueba que las cuatro superficies rechacen claims de contexto.
- Impacto: R12 solo está demostrado para parte del planner y no para los tres
  consumers ni para la paridad observable exigida.
- Corrección requerida: componer consumers reales y estrechos para analyzer y
  diagnostic sobre el mismo service, y añadir una vertical de las cuatro
  superficies que capture y compare context digest, binding y separación de
  roles, sin evidence bodies, memoria extra, stores o handles.

## REV-11 — El gate race no fue limpio en reproducción independiente

- Severidad: blocker
- Requirement o regla: R13, R15, `docs/verification.md`
- Archivo y línea: `internal/validationworkflow/functional_flow_test.go:1579`
- Evidencia: una ejecución fresca de
  `go test -race ./internal/contextassembly ./internal/agentplanning ./internal/validationworkflow -count=1`
  falló en
  `TestAuthenticatedSemanticDivergenceFailsBeforeStage/evidence-revision`: la
  ejecución quedó pendiente con dos llamadas de execute/tool y recovery devolvió
  `functional flow execution pending`. La repetición aislada exacta con
  `-run '^TestAuthenticatedSemanticDivergenceFailsBeforeStage$' -count=3` pasó.
- Impacto: el gate obligatorio no es reproducible de forma limpia bajo race y
  carga conjunta.
- Corrección requerida: eliminar la dependencia de timing o la carrera de
  lifecycle que deja el flujo pendiente y repetir race focal/global varias
  veces sin fallos.

## Verificación independiente

- `gofmt -l` sobre todos los Go modificados/nuevos: pass.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- suite focal normal: pass.
- `go test -p 1 ./... -count=1 -timeout 2m`: pass.
- race focal: un fallo descrito en REV-11; repetición aislada x3: pass.
- binary Linux de `internal/contextassembly` ejecutado en `docker-desktop`: pass.
- cross-build test binaries de los cuatro paquetes para Linux, Darwin y Windows
  amd64: pass.
- cobertura del diff calculada desde profiles frescos de los cuatro paquetes en
  Windows más profile Linux real, fusionando bloques por máximo e incluyendo
  build tags: **966/1122 = 86.096257 %**. El umbral R15 se cumple.

No se detectó implementación de adquisición GitHub (feature 30) ni de lifecycle
de environments (feature 31). El rechazo se debe a los hallazgos funcionales y
de seguridad anteriores, no a scope creep.

## Revisión 2 — 2026-10-24

**Veredicto: rejected**

Se volvió a revisar el diff completo desde `45d325a` tras la remediación. Las
precondiciones siguen satisfechas: existe una sola feature `in_progress`, la
spec conserva aprobación humana, T1–T16 figuran marcadas y existe informe del
implementer. La remediación cierra varias observaciones de la primera revisión,
pero todavía quedan requirements y tasks materiales sin cumplir.

### Estado de REV-1..REV-11

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1 | Cerrado: composición fija `TrustClass()` por source kind y la matriz adversaria rechaza elevación. |
| REV-2 | Cerrado en el flujo productivo planner: `Instructions` y `Untrusted` llegan por canales distintos y no se añade el payload legacy. La validación de esos canales en `agentplanning` sigue abierta en REV-15. |
| REV-3 | Cerrado: `plan_id` participa en el snapshot y el packing confirma los bytes/tokens del envelope lógico final, incluidos límites exactos. |
| REV-4 | Cerrado: mandatory/optional, required-complete y conflict policy se evalúan después del packing; configuración e instrucciones quedan reservadas. |
| REV-5 | Parcial: existen field/source/candidate/payload bounds, pero el límite de duración no acota el trabajo real; continúa como REV-12. |
| REV-6 | Parcial: existe `RedactionVerified` y aumentó la taxonomía, pero tokens/credentials genéricos aún atraviesan el gate; continúa como REV-17. |
| REV-7 | Parcial: decode strict y journal encadenado existen, pero no detectan rollback coordinado de cola; continúa como REV-14. |
| REV-8 | Abierto: cancelación posterior a publicación y operaciones pathname siguen dejando efectos; continúa como REV-13 y REV-18. |
| REV-9 | Parcial: digest/envelope aparecen en provenance, snapshot y artifact, pero las instrucciones privilegiadas no quedan verificadas ni selladas de extremo a extremo; continúa como REV-15. |
| REV-10 | Abierto: el consumer de roles solo aparece en tests y acepta authority/producers del caller; continúa como REV-16. |
| REV-11 | Cerrado: race focal y global pasan en reproducción independiente. |

## REV-12 — `MaxDurationMS` no limita el trabajo de ensamblado

- Severidad: blocker
- Requirement o regla: R13, ARCH-005, T5, T15
- Archivo y línea: `internal/contextassembly/assembler.go:200`, `internal/contextassembly/assembler.go:210`, `internal/contextassembly/assembler.go:223`
- Evidencia: `Assemble` compara un `Clock` únicamente antes de cada
  `Producer.Produce`; no crea un contexto con deadline del budget, no comprueba
  el límite al volver del último producer y tampoco lo aplica a filter, secret
  gate, ranker, tokenizer, canonicalización o store. Un probe overlay puso un
  producer de contrato de 40 ms como última fuente y `MaxDurationMS=10`; el
  ensamblado retornó éxito tras 40.9977 ms.
- Impacto: un collaborator lento o bloqueado puede exceder indefinidamente el
  límite sellado aun cuando el caller no haya proporcionado un deadline menor.
- Corrección requerida: derivar un contexto con deadline del menor límite
  caller/profile y pasarlo a todas las fronteras; comprobar expiración después
  de cada llamada y antes de publicar. Añadir spies que bloqueen en cada fase,
  incluido el último producer, y probar que todo trabajo queda joined.

## REV-13 — Cancelar después del link deja un objeto publicable

- Severidad: blocker
- Requirement o regla: R13, T10, T15
- Archivo y línea: `internal/contextassembly/store.go:519`, `internal/contextassembly/store.go:522`, `internal/contextassembly/store.go:525`
- Evidencia: `atomicWriteChecked` crea el hardlink final antes de consultar de
  nuevo cancelación. Si el contexto se cancela en el boundary `link`, retorna
  `CategoryCancelled`, pero el defer solo elimina el temporal y no el destino.
  Un probe overlay canceló exactamente en ese boundary y `os.Lstat` confirmó
  que el objeto final seguía existiendo. El test productivo cancela únicamente
  en `write`, antes de publicar.
- Impacto: el caller recibe cancelación aunque ha quedado un snapshot/binding
  direccionable por digest, contrario a la garantía de cero publicación tras
  cancelación.
- Corrección requerida: definir un punto de commit inequívoco; antes de él,
  rollback completo del destino ante cancelación/fallo, y después de él,
  completar journal/receipt de forma acotada y devolver el resultado confirmado.
  Probar cancelación en cada boundary, especialmente link/remove/parent-sync.

## REV-14 — El journal acepta rollback coordinado de cola

- Severidad: blocker
- Requirement o regla: R9, R11, T10, ARCH-007
- Archivo y línea: `internal/contextassembly/store.go:345`, `internal/contextassembly/store.go:351`, `internal/contextassembly/store.go:383`
- Evidencia: cada entry usa un SHA-256 autocontenido y `validateJournalLocked`
  reconstruye la cadena solo con archivos presentes bajo el mismo root. No hay
  ancla monotónica, MAC/key de composición ni tail confirmado fuera del conjunto
  mutable. Un probe creó dos commits, eliminó conjuntamente el journal tail, su
  receipt y su objeto, y `NewLocalStore` aceptó el root reducido. El test
  `TestJournalChainDetectsTamperingRollbackAndContentLeak` elimina solo el
  journal y deja el receipt, por lo que no cubre el rollback coordinado pedido.
- Impacto: un rollback puede borrar silenciosamente una publicación confirmada
  y presentar una historia anterior como íntegra y append-only.
- Corrección requerida: persistir/verificar un witness de tail protegido fuera
  del conjunto que se revierte, o usar el mecanismo monotónico/autenticado ya
  aprobado por la arquitectura. Añadir rollback conjunto de journal, receipt y
  objeto, incluida reapertura/recovery.

## REV-15 — El boundary de generación acepta sustitución del contexto privilegiado

- Severidad: blocker
- Requirement o regla: R1, R4, R9, R12, R14
- Archivo y línea: `internal/agentplanning/generator.go:211`, `internal/agentplanning/generator.go:253`, `internal/agentplanning/provenance.go:36`, `internal/agentplanning/model.go:320`
- Evidencia: `snapshotRequest` solo exige version/role no vacíos y coincidencia
  textual del digest declarado. Usa `json.Decoder` sin rechazo de claves
  duplicadas ni comparación canónica. Probes overlay demostraron que llegan al
  modelo: `version="evil"`, role `diagnostic`, whitespace inicial, role
  duplicado y una sustitución coordinada de `ContextDigest`, digest del envelope
  e `ContextInstructions`. `generationProvenance`, `PreparedSnapshot`,
  `MatchesProvenance` y `canonicalContextDigest` sellan digest/hash del envelope,
  pero no los bytes de `ContextInstructions`; el envelope tampoco permite
  recomputar el digest real del snapshot. Los tests actuales mutan un campo cada
  vez y no cubren sustitución coherente de todos los strings.
- Impacto: una instrucción o envelope distinto puede cruzar el model boundary y
  conservar la misma comprobación de provenance preparada, incluido tras
  restore, sin demostrar que procede del snapshot ensamblado.
- Corrección requerida: transportar un valor sellado que vincule
  criptográficamente instrucciones + configuración/untrusted + plan/role/version
  al snapshot, validar versión y role exactos, duplicate/unknown/canonical JSON
  y recomputar/verificar el binding antes del modelo. Incluir las instrucciones
  efectivas en provenance, `PreparedSnapshot`, artifact/recovery y tests de
  sustitución coordinada.

## REV-16 — Analyzer/diagnostic eluden el registry de composición

- Severidad: blocker
- Requirement o regla: R2, R12, T3, T14; diseño `SourceRegistry`
- Archivo y línea: `internal/validationworkflow/context_assembly.go:33`, `internal/validationworkflow/context_assembly.go:41`, `internal/validationworkflow/context_assembly.go:49`
- Evidencia: `RoleContextRequest` expone `Authority` y `[]Producer`, y
  `ConsumeRole` construye el registry directamente desde esos valores. La única
  invocación de `ConsumeRole` en el repositorio está en
  `context_roles_test.go`; no existe un consumer productivo de analyzer o
  diagnostic. El test llamado `RejectsCallerAuthority` solo rechaza una
  instruction version distinta, pero acepta project/repo/revision/provider,
  authority y producers proporcionados por el caller.
- Impacto: no se demuestra aislamiento productivo de roles y un caller interno
  puede elegir las fuentes/autoridad que el diseño reserva a composición.
- Corrección requerida: registrar producers y derivar authority dentro de la
  composición productiva, exponer al caller solo un request estrecho e integrar
  los consumers controlados reales. Los tests deben intentar sustituir cada
  authority/source y confirmar cero consumer/model calls.

## REV-17 — El detector permite `token` y `credential` explícitos

- Severidad: blocker
- Requirement o regla: R14, ARCH-009
- Archivo y línea: `internal/contextassembly/adapters.go:144`, `internal/contextassembly/adapters.go:149`
- Evidencia: el patrón enumera `access_token`, `private_token`, varios secrets y
  cookies, pero no las claves genéricas `token` ni `credential` que R14 nombra
  explícitamente. Un probe llamó `CanaryGate.Check` con payloads anidados
  `{"token":"abcdefghijk"}` y `{"credential":"abcdefghijk"}`; ambos
  fueron aceptados. El test de taxonomía tampoco incluye esas dos formas.
- Impacto: credenciales prohibidas pueden llegar byte-for-byte a ranking,
  snapshot, persistencia y request del modelo aun con redaction state marcado.
- Corrección requerida: usar inspección estructural recursiva de claves y tipos
  sensibles, además de detectores de valores, y cubrir todas las familias
  literales de R14 a cualquier profundidad y superficie.

## REV-18 — La publicación sigue resolviendo paths tras validar identidades

- Severidad: blocker
- Requirement o regla: R13, R14, T10
- Archivo y línea: `internal/contextassembly/store.go:486`, `internal/contextassembly/store.go:519`, `internal/contextassembly/file_open_windows.go:36`
- Evidencia: `atomicWriteChecked` usa `os.CreateTemp`, `os.Link` y `os.Remove`
  sobre pathnames; cada `validateLayout` termina antes de la syscall siguiente.
  En Windows, `openRegularAt` conserva un handle pero vuelve a construir un path
  con `filepath.Join(directory.Name(), name)`, por lo que no abre relativo al
  handle fijado. La matriz anunciada como swaps solo inyecta errores en el hook;
  no sustituye root/subdir/temp/object entre validación y syscall.
- Impacto: un swap dentro de la ventana puede redirigir una lectura/publicación
  antes de que el check posterior detecte el cambio, dejando un efecto fuera del
  directorio cuya identidad fue aprobada.
- Corrección requerida: realizar create/publish/remove/read mediante dirfd o
  handles relativos no-follow estables, o una primitiva equivalente que impida
  redirección, y probar swaps efectivos en cada boundary en Unix y Windows.

## REV-19 — El gate Linux de la integración no es ejecutable herméticamente

- Severidad: blocker
- Requirement o regla: R15, T16
- Archivo y línea: `internal/validationworkflow/workflow_test.go:1879`, `internal/validationworkflow/workflow_test.go:1881`
- Evidencia: el binary Linux instrumentado de `internal/validationworkflow`
  falla en `TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols` porque
  `runtime.Caller` conserva el path de compilación Windows
  `C:/Users/asier/.../internal/validationworkflow`, inexistente al ejecutar en
  Linux. El binary Linux de `contextassembly` sí pasa y fue suficiente para
  medir los build tags Unix, pero T16 exige runtime Linux de la integración
  aplicable.
- Impacto: el resultado Linux declarado no es reproducible para uno de los
  paquetes productivos modificados y la suite deja de ser hermética al mover el
  binary.
- Corrección requerida: hacer que la auditoría de comentarios reciba/descubra
  fuentes de forma portable o separarla de los tests runtime; repetir los cinco
  binaries modificados en Linux real.

## Verificación independiente de la revisión 2

- `gofmt -l` sobre Go modificado/nuevo: pass, salida vacía.
- `go vet ./...`: pass.
- `go test -run '^$' ./...`: pass.
- suite focal normal de los cinco paquetes: pass.
- `go test -race` focal de los cinco paquetes con UCRT64: pass.
- `go test -race -p 1 ./... -count=1 -timeout 8m`: pass completo.
- cross-build test binaries Linux/Darwin/Windows amd64 para los cinco paquetes:
  pass.
- ejecución Linux real del binary instrumentado de `contextassembly`: pass;
  ejecución de `validationworkflow`: fail descrito en REV-19.
- coverage regenerado desde profiles frescos Windows de los cinco paquetes y
  profile Linux real de `contextassembly`, fusionando bloques por máximo e
  intersectando líneas modificadas/nuevas desde `45d325a`, incluidos ambos
  lados de build tags: **1278/1485 = 86.060606 %**. El umbral R15 pasa. La cifra
  difiere ligeramente del informe (1273/1479), pero ambas quedan sobre 85 %.
- `git diff --check`: pass; tasks sin `[ ]`; no se encontraron binarios,
  profiles ni temporales dentro del repositorio.

No se detectó adquisición de GitHub propia de feature 30 ni lifecycle de
environments propio de feature 31. La paridad planner API/CLI/MCP/GitHub usa el
service compartido y conserva digest/binding en la redelivery, pero no compensa
la ausencia de consumers productivos composition-owned para analyzer/diagnostic.

## Revisión 3 — 2026-10-24

**Veredicto: rejected**

Se revisó nuevamente el diff completo desde `45d325a`. Las precondiciones se
mantienen: feature 29 es la única `in_progress`, la spec contiene aprobación
humana, T1–T16 están marcadas y existe informe del implementer. La tercera
remediación cierra REV-12..REV-19 y no reabre REV-1..REV-11, pero tres probes
adversariales descubren incumplimientos bloqueantes de R10, R11 y R13.

### Estado de REV-1..REV-19

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-11 | Sin regresión observada: trust class procede del catálogo de composición; request estructurado sin payload legacy; budgets y políticas se aplican antes de ranking/modelo; seal/provenance, roles y superficies conservan las separaciones introducidas. Race focal y global pasan. |
| REV-12 | Cerrado: `Assemble` deriva el deadline efectivo y lo propaga por producers, filter, gate, ranker, tokenizer y store, con comprobaciones al retorno y join secuencial. |
| REV-13 | Cerrado para cancelación explícita pre/post commit: publicación usa destino rollbackable antes del witness y finalización acotada tras confirmación. La respuesta ambigua del witness abre un caso distinto en REV-21. |
| REV-14 | Cerrado: el estado externo monotónico liga generación, tail y digest de receipts, y detecta rollback coordinado al reabrir. |
| REV-15 | Cerrado: decoder strict/canónico rechaza duplicados, unknowns y trailing data; version, role, plan, instrucciones y untrusted/config quedan ligados al seal y llegan a provenance, snapshot preparado, artifact y recovery. |
| REV-16 | Cerrado: authority y producers ya no son campos del request de rol; la composición registra las fuentes y usa consumers controlados de planner/analyzer/diagnostic. |
| REV-17 | Cerrado: la inspección estructural recursiva cubre `token`, `credential` y el resto de la taxonomía de R14. |
| REV-18 | Cerrado: Unix usa operaciones relativas a dirfd/no-follow y Windows usa handles con `RootDirectory`; las pruebas realizan swaps reales. |
| REV-19 | Cerrado: los cinco binaries Linux instrumentados se ejecutan desde su package cwd con source root explícito y pasan. |

## REV-20 — El deadline puede suprimir el único evento terminal

- Severidad: blocker
- Requirement o regla: R11, R13, T15
- Archivo y línea: `internal/contextassembly/assembler.go:160-175`
- Evidencia: el defer reduce `ObserverTimeout` al tiempo restante del deadline de
  assembly y, si ese tiempo ya es cero, retorna en las líneas 166–171 sin llamar
  a `Observer.Observe`. Un probe overlay usó un producer que espera
  `ctx.Done()` con `MaxDurationMS=10`; `Assemble` devolvió `cancelled` y el spy
  registró **0** eventos terminales. La prueba existente de deadline comprueba
  propagación/error, pero no exige una llamada terminal en cada fase fallida.
- Impacto: las ejecuciones que agotan el budget durante cualquier collaborator
  pueden desaparecer de observabilidad, aunque R11 exige exactamente un evento
  para assembled, insufficient, stale, cancelled y failed.
- Corrección requerida: reservar un presupuesto de observación o ejecutar la
  emisión terminal con una finalización interna acotada tras cancelar y esperar
  el trabajo propio. Añadir una tabla por cada frontera que exija exactamente
  una llamada, incluso cuando la fase consume por completo el deadline.

## REV-21 — Un CAS confirmado con respuesta perdida se revierte localmente

- Severidad: blocker
- Requirement o regla: R9, R13, T10, T15; diseño «store incierto tras commit»
- Archivo y línea: `internal/contextassembly/store.go:358-375`
- Evidencia: cualquier error de `CompareAndAdvance` entra directamente en las
  líneas 365–370, elimina journal y objeto y devuelve persistence, sin leer el
  witness para decidir si el CAS ocurrió. Un witness adversarial avanzó
  efectivamente de generación 0 a 1 y después devolvió `response lost after
  commit`; el caller recibió error, el estado externo quedó en generación 1 y
  `NewLocalStore` ya no pudo recuperar porque los bytes confirmados habían sido
  eliminados. El store de `projectmemory` ya trata este caso mediante `Read`
  posterior al error, pero `contextassembly` no lo hace.
- Impacto: una pérdida de respuesta normal en el witness convierte un commit
  irreversible y válido en un root local inconsistente; retry/restart no
  convergen y se pierde la publicación confirmada.
- Corrección requerida: reconciliar todo error de CAS mediante lectura acotada
  del witness. Si contiene `nextState`, finalizar receipt y devolver el commit;
  si conserva `expectedState`, hacer rollback; si no puede confirmarse, mantener
  el staging recuperable y devolver estado incierto sin borrar datos que puedan
  estar confirmados. Probar respuesta perdida tras CAS y restart/redelivery.

## REV-22 — Un adapter reconstruido no puede revalidar un binding persistido

- Severidad: blocker
- Requirement o regla: R10, R12, T13, T14
- Archivo y línea: `internal/validationworkflow/context_assembly.go:20-31`, `internal/validationworkflow/context_assembly.go:138-162`, `internal/validationworkflow/context_assembly.go:191-206`, `cmd/featureproof/workflow.go:611-635`
- Evidencia: `Prepare` guarda assembler, authorities y producers solo en los maps
  de proceso `active`, `roleAuthorities` y `roleProducers`. `Revalidate` carga el
  binding del Store, pero acto seguido exige `active[binding.ContextDigest]`; un
  adapter recién compuesto tiene el map vacío. Un probe vertical preparó y ligó
  con un adapter, creó otro adapter sobre el mismo Store y llamó `Revalidate`:
  obtuvo `validation workflow context_snapshot: conflict`. La prueba de restart
  vigente restaura el workflow conservando el mismo adapter, por lo que no
  simula una reconstrucción del proceso. Además, la composición productiva usa
  `MemoryStore` salvo que haya witness externo configurado.
- Impacto: después de un restart real no se pueden ejecutar los checkpoints de
  drift antes de approval, execution o recovery, aun cuando snapshot y binding
  estén persistidos; la aceptación explícita de R10 queda incumplida. Los maps
  tampoco liberan estado por run terminado.
- Corrección requerida: reconstruir el assembler y el registry
  composition-owned a partir de configuración/identidades durables, o persistir
  un descriptor mínimo verificable que permita resolverlos tras restart. Añadir
  un test con nueva composición y nuevo adapter sobre LocalStore, seguido de
  revalidation antes de approval/execution/recovery, y retirar estado terminal.

## Verificación independiente de la revisión 3

- `gofmt -l` sobre Go nuevo/modificado: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1`: pass.
- suite focal normal de los cinco paquetes modificados: pass.
- `go test -p 1 ./... -count=1 -timeout 10m`: pass completo.
- race focal de los cinco paquetes con UCRT64: pass.
- `go test -race -p 1 ./... -count=1` con UCRT64: pass completo.
- cinco binaries instrumentados Linux (`cmd/featureproof`, `agentplanning`,
  `contextassembly`, `planapproval`, `validationworkflow`): pass.
- cross-build de test binaries para los cinco paquetes en Linux, Darwin y
  Windows amd64: pass.
- coverage recalculado desde cinco profiles Windows y cinco Linux con
  `coverpkg`, máximo por bloque e intersección exacta con el diff desde
  `45d325a`; se incluyen build tags y `publish_unix_other.go` se cuenta
  conservadoramente como **0/1**: **1655/1919 = 86.242835 %**. No faltan bloques
  del diff y R15 supera el 85 % sin redondeo.
- auditoría AST de comentarios sobre `contextassembly` y
  `validationworkflow`: pass; `git diff --check`: pass; no hay tasks abiertas ni
  artefactos temporales de esta revisión dentro del repositorio.

No se detectó adquisición GitHub propia de feature 30 ni lifecycle de entornos
de feature 31. La paridad productiva de las cuatro superficies y el aislamiento
de roles pasan las pruebas existentes; los tres blockers se encuentran en el
lifecycle compartido y afectan a todas ellas.

## Revisión 4 — 2026-10-24

**Veredicto: rejected**

Se revisó el diff completo desde `45d325a` tras la cuarta entrega. Las
precondiciones siguen satisfechas: feature 29 es la única `in_progress`, la spec
está aprobada, T1–T16 figuran completas y existe informe actualizado. REV-20 y
REV-21 quedan cerrados. REV-22 solo se cierra respecto a persistencia y ausencia
de maps de proceso: la reconstrucción no consulta autoridad actual y por ello no
cumple la revalidación de restart de R10. Dos violaciones adicionales afectan
concurrencia y cancelación productiva.

### Estado de REV-1..REV-22

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-19 | Sin regresión observada en filtros/trust, typed channels, budgets, conflictos/insuficiencia, seal/provenance, filesystem no-follow, roles, cuatro superficies, Linux ni race. |
| REV-20 | Cerrado: assembly reserva una finalización interna acotada para observer aun con deadline agotado; los probes por producer/filter/ranker/tokenizer/store/observer y todos los outcomes registran una sola invocación. |
| REV-21 | Cerrado: tras error del CAS, `next` finaliza el receipt, `expected` revierte precommit y estado/lectura desconocidos conservan staging como `uncertain`; restart y redelivery convergen sin pérdida ni duplicado. |
| REV-22 | Abierto: snapshot/binding y `RegistryDescriptor` sobreviven al restart y ya no hay maps, pero `RevalidationProducers` crea la autoridad «actual» copiando el binding histórico; continúa como REV-23. La exigencia productiva de LocalStore+witness sí está implementada. |

## REV-23 — El registry reconstruido valida el snapshot contra sí mismo

- Severidad: blocker
- Requirement o regla: R10, R12, T13, T14
- Archivo y línea: `internal/contextassembly/assembler.go:780-795`, `internal/validationworkflow/context_assembly.go:192-210`
- Evidencia: `RevalidationProducers` verifica que el descriptor coincide con el
  snapshot persistido y después crea cada `StaticProducer` con `Current` igual
  al mismo `SourceBinding` persistido (líneas 791–794). No resuelve el ID del
  producer contra un registry de composición ni vuelve a observar commit,
  configuración, memoria, contrato o capabilities actuales. Un probe overlay
  mutó el binding actual del producer tras crear snapshot/binding: el assembler
  vivo devolvió `stale`, mientras un assembler nuevo construido mediante
  `RevalidationProducers(snapshot)` devolvió `valid` para ese mismo drift. El
  test `TestContextAdapterRestartRebuildsDurableRegistry` solo cambia la versión
  del profile; no cambia ninguna fuente externa y no atraviesa approval,
  execution ni recovery con una composición nueva.
- Impacto: un restart real convierte todas las fuentes en afirmaciones
  autocontenidas del snapshot histórico. Un commit, contrato, configuración o
  memoria obsoletos pueden superar los checkpoints previos a approval,
  execution y recovery.
- Corrección requerida: usar el descriptor únicamente como selector sellado de
  adapters composition-owned y pedir a esos adapters su estado actual. La
  composición nueva debe resolver IDs/versiones autorizados sin recibir
  authority/producers del caller. Añadir una vertical con LocalStore, nueva
  composición y drift real de cada clase antes de approval/execution/recovery,
  verificando cero modelo/tools tras detectar staleness.

## REV-24 — LocalStore mantiene su mutex durante disco y witness externo

- Severidad: blocker
- Requirement o regla: R13, CONV-006 (concurrencia), checklist de reviewer «no I/O bajo locks»
- Archivo y línea: `internal/contextassembly/store.go:218-256`, `internal/contextassembly/store.go:350-396`
- Evidencia: `CommitSnapshot` toma `s.mu` en 228–229 y, sin soltarlo, ejecuta
  validación/lecturas de disco, publicación, journal, `CompareAndAdvance`, la
  lectura de reconciliación del witness y receipt. `LoadSnapshot` también
  mantiene el mismo mutex durante `secureRead`. Un probe bloqueó
  `CompareAndAdvance` y lanzó en paralelo `LoadSnapshot` de un snapshot ya
  confirmado; la lectura independiente quedó bloqueada hasta liberar el I/O del
  witness (`LoadSnapshot blocked behind external witness I/O while LocalStore
  mutex was held`).
- Impacto: una llamada de red lenta o detenida serializa todas las lecturas y
  commits del store. La región crítica contiene red, disco, callbacks y timers,
  justo lo prohibido por las convenciones, y degrada la concurrencia de runs
  independientes aunque race detector no encuentre data races.
- Corrección requerida: limitar el mutex a reserva/transición de estado en
  memoria y efectuar disco/red fuera de él, usando locks por identidad,
  staging/CAS o una cola con ownership explícito. Probar que una CAS bloqueada no
  impide cargar otro objeto confirmado ni progresar un run independiente.

## REV-25 — La composición productiva ignora cancelación al abrir el witness

- Severidad: blocker
- Requirement o regla: R13, ARCH-006, `docs/conventions.md` §6 y constraints de la spec
- Archivo y línea: `cmd/featureproof/workflow.go:625-626`
- Evidencia: `composeWorkflowCLI` recibe `ctx`, pero las llamadas bloqueables
  `contextWitness.Read` y `Bootstrap` usan `context.Background()`. Un probe
  productivo con witness HTTP bloqueado canceló el contexto después de recibir
  la petición; la composición no retornó durante la ventana de cancelación y
  solo continuó al liberar el servidor. La excepción aprobada para contexto
  interno se limita a finalizar un commit terminal ya confirmado; esta lectura
  ocurre durante composición, antes de cualquier commit.
- Impacto: cancelar CLI/API/MCP/GitHub no interrumpe la adquisición inicial de
  autoridad. El proceso puede seguir bloqueado hasta el timeout global del
  cliente y ejecutar trabajo posterior con una solicitud ya cancelada.
- Corrección requerida: pasar el contexto recibido, con timeout acotado cuando
  corresponda, a Read/Bootstrap y comprobarlo antes de continuar. Añadir tests
  de cancelación durante ambas operaciones y confirmar cierre/join sin efectos.

## Verificación independiente de la revisión 4

- `gofmt -l` sobre todos los Go nuevos/modificados: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- `go test -p 1 ./... -count=1 -timeout 10m`: pass completo.
- `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64: pass completo.
- cinco binaries instrumentados Linux ejecutados desde sus package cwd: pass.
- cross-build de test binaries para los cinco paquetes afectados en Linux,
  Darwin y Windows amd64: pass.
- cobertura regenerada desde cinco profiles Windows y cinco Linux, fusionando
  máximo por bloque e intersectando líneas nuevas/modificadas desde `45d325a`:
  **1713/1987 = 86.210367 %**. Incluye todos los build tags; el fallback
  `publish_unix_other.go` cuenta conservadoramente **0/1**. El cambio declarativo
  de `internal/planapproval/model.go` no genera bloque ejecutable.
- precondiciones, tasks, aprobación, `git diff --check` y auditoría AST de
  comentarios: pass. No hay binaries/profiles/temporales de la revisión dentro
  del repositorio.

No se detectó implementación de adquisición GitHub de feature 30 ni lifecycle
de environments de feature 31. El rechazo procede de revalidación, concurrencia
y cancelación dentro del scope compartido de feature 29.

## Revisión 5 — 2026-10-24

**Veredicto: rejected**

Se revisó de nuevo el diff completo desde `45d325a`. Las precondiciones siguen
cumplidas: feature 29 es la única `in_progress`, la spec mantiene aprobación
humana, T1–T16 figuran completas y el informe de implementación existe. La
resolución de producers actuales cierra REV-23. La propagación del contexto
hasta el witness corrige el bloqueo HTTP de REV-25, pero la cancelación todavía
se reclasifica como fallo de disponibilidad/persistencia. REV-24 permanece
abierto y el cambio a lecturas sin lock revela además una publicación visible
antes del commit externo.

### Estado de REV-1..REV-25

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-22 | Sin regresión observada en filtros antes de ranking, trust classes irreversibles, channels tipados sin payload legacy, budgets exactos, conflictos/insuficiencia, seal/provenance, drift, observer exactly-once, CAS ambiguo/recovery, filesystem no-follow, roles, consumers, cuatro superficies o runtime Linux. |
| REV-23 | Cerrado: `CurrentContextProducerRegistry` carga el workflow durable actual, vuelve a ejecutar `Admission.Build`, reconstruye los producers composition-owned y `RevalidationProducers` comprueba count, kind, trust e identidad/version del descriptor sellado. Los tests de nueva composición detectan drift de runtime e identidad. |
| REV-24 | Abierto: desapareció el mutex Go y las lecturas confirmadas ya no esperan al writer, pero el lock exclusivo de proceso sigue abarcando disco y el witness externo; además una lectura puede observar el objeto aún no confirmado. Se detalla junto con REV-26. |
| REV-25 | Parcialmente cerrado: `Read`, `Bootstrap`, `NewLocalStore` y HTTP reciben el contexto del caller y la petición se cancela. La frontera devuelve después un error genérico de disponibilidad/persistencia en vez de preservar `cancelled`; se detalla en REV-27. |

## REV-24 — El lock exclusivo todavía abarca disco y witness externo

- Severidad: blocker
- Requirement o regla: R13, CONV-006 (concurrencia), T10, T15
- Archivo y línea: `internal/contextassembly/store.go:219-243`,
  `internal/contextassembly/store.go:277-297`,
  `internal/contextassembly/store.go:329-405`
- Evidencia: `CommitSnapshot` y `CommitBinding` adquieren
  `acquireContextProcessLock` y solo lo liberan al retornar. Bajo ese lock,
  `commit` lee/escribe object, journal y receipt, llama a
  `CompareAndAdvance` y puede ejecutar la lectura de reconciliación. Cambiar el
  mutex en memoria por un file lock no altera la región crítica. La prueba
  añadida demuestra que un reader no espera, pero el segundo writer queda tras
  la CAS bloqueada y termina por cancelación.
- Impacto: una red o disco lentos serializan todos los writers del store aunque
  correspondan a runs o identidades independientes. Esto contradice
  explícitamente `docs/conventions.md` 75–76, que exige una región pequeña y
  prohíbe mantener locks durante red, disco, timers o callbacks externos.
- Corrección requerida: reservar/transicionar únicamente metadata mínima bajo
  lock y efectuar staging, sync y witness fuera de él; publicar mediante CAS o
  una transición corta posterior. Probar progreso real de writers
  independientes con witness bloqueado, además de convergencia para la misma
  identidad.

## REV-26 — LoadSnapshot expone un objeto antes de que el witness confirme el commit

- Severidad: blocker
- Requirement o regla: R9, R13, T10, T15; atomicidad de publicación
- Archivo y línea: `internal/contextassembly/store.go:247-273`,
  `internal/contextassembly/store.go:354-405`
- Evidencia: `commit` publica primero
  `snapshots/<digest>.json` en las líneas 364–369 y solo después intenta el CAS
  en 387. `LoadSnapshot` no comprueba receipt, journal confirmado ni estado del
  witness: acepta los bytes y su self-digest. Un probe overlay bloqueó
  `CompareAndAdvance`, obtuvo el digest del objeto ya escrito y llamó
  `LoadSnapshot`; la carga tuvo éxito mientras el CAS continuaba pendiente y no
  existía receipt confirmado (`unconfirmed pre-CAS snapshot became loadable`).
  La misma secuencia se aplica a bindings.
- Impacto: otro run puede consumir o ligar un snapshot que posteriormente será
  revertido si el CAS devuelve el estado esperado. La cancelación durante
  persistence deja temporalmente un snapshot publicable, en contradicción
  directa con R13, y la supuesta atomicidad del binding de R9 no se sostiene.
- Corrección requerida: conservar object/journal precommit en un namespace de
  staging no direccionable o hacer que `LoadSnapshot`/`LoadBinding` exijan una
  confirmación autoritativa ligada al receipt. Añadir probes concurrentes para
  snapshot y binding que fallen cerrado antes del CAS, durante resultado
  ambiguo y después de rollback/restart.

## REV-27 — La cancelación del witness se reescribe como disponibilidad o persistence

- Severidad: blocker
- Requirement o regla: R13, ARCH-006, constraints de cancelación
- Archivo y línea: `cmd/featureproof/workflow.go:625-634`,
  `internal/contextassembly/store.go:136-190`
- Evidencia: la composición ya pasa `ctx` a `Read`, `Bootstrap` y
  `NewLocalStore`, y el transporte HTTP se detiene al cancelarlo. Sin embargo,
  `workflow.go` reemplaza cualquier error de `Read` por `context witness
  unavailable`, y `NewLocalStore` reemplaza el error de `witness.Read` por
  `CategoryPersistence` en 188–189. Un probe con `Read` bloqueado canceló el
  caller y recibió `context assembly store: persistence at witness`, no
  `CategoryCancelled`. Incluso un contexto ya cancelado entra en la validación
  conjunta de 136–138 y se clasifica `CategoryInvalid`.
- Impacto: CLI/API/MCP/GitHub no pueden distinguir cancelación solicitada de
  corrupción o indisponibilidad. Los retry/verdict/telemetry pueden aplicar una
  política incorrecta aunque el trabajo externo sí se haya detenido.
- Corrección requerida: comprobar `ctx.Err()` en cada retorno de
  Read/Bootstrap/constructor y mapearlo al error tipado `cancelled`/`timeout`
  antes de envolver errores operativos. Añadir tests productivos que exijan la
  categoría en Read, Bootstrap y validación inicial de `NewLocalStore`.

## Verificación independiente de la revisión 5

- `gofmt -l` sobre Go nuevo/modificado: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- cinco binaries instrumentados Linux ejecutados desde sus package cwd: pass.
- cross-build de los cinco paquetes afectados para Linux, Darwin y Windows:
  15/15 pass.
- `go test -p 1 ./... -count=1 -timeout 10m`: la primera ejecución mostró un
  fallo intermitente en `functionalflow`; el test fallido pasó aislado y la
  repetición global pasó completa.
- `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64: la primera
  ejecución mostró dos fallos intermitentes de lease/journal en
  `functionalflow`; ambos pasaron juntos aislados y la repetición global pasó
  completa, sin reporte del race detector.
- cobertura regenerada desde cinco profiles Windows y cinco Linux, tomando el
  máximo por bloque e intersectando exactamente el diff desde `45d325a`:
  **1785/2078 = 85.899904 %**. Incluye build tags; el fallback
  `publish_unix_other.go` se contabiliza conservadoramente como **0/1**. El
  cambio declarativo de `internal/planapproval/model.go` no genera bloque
  ejecutable.
- aprobación, única feature activa, tasks, `git diff --check` y auditoría AST
  de comentarios sistémicos: pass. No hay profiles, binaries ni temporales de
  esta revisión dentro del repositorio.

No se detectó implementación productiva de adquisición GitHub propia de
feature 30 ni lifecycle de environments de feature 31; sus menciones se limitan
a exclusiones de scope en spec/progress. Los blockers están en el store y la
frontera de cancelación compartidos por las cuatro superficies.

## Revisión 6 — 2026-10-24

**Veredicto: rejected**

Se revisó el diff completo desde `45d325a` tras la sexta entrega. La feature 29
sigue siendo la única `in_progress`, la spec está aprobada y T1–T16 figuran
completas. El nuevo protocolo elimina los file locks de `LocalStore`, mantiene
precommit no direccionable y exige receipt+journal antes de un load público.
REV-26 queda cerrado. Sin embargo, una intercalación válida entre writers que
comparten object digest permite al perdedor borrar el objeto del ganador, y la
frontera de cancelación aún pierde `context.Cause` y no distingue timeout.

### Estado de REV-1..REV-27

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-23 | Sin regresión observada: filtros estructurales preceden ranking; trust, instructions/config/untrusted y roles conservan autoridad de composición; budgets, conflictos/insuficiencia, seal/provenance, drift, observer, CAS ambiguo, filesystem no-follow, consumers y cuatro superficies mantienen su comportamiento. |
| REV-24 | Parcialmente cerrado: no quedan file locks ni E/S externa bajo mutex en `LocalStore`, y un writer independiente puede ganar mientras otro CAS está bloqueado. La convergencia/durabilidad concurrente todavía falla para objetos compartidos; continúa como REV-28. |
| REV-25 | Cerrado respecto al transporte: caller context alcanza Read, Bootstrap, constructor y HTTP; una cancelación normal interrumpe la operación y no deja la petición activa. La fidelidad de causa/categoría continúa en REV-27. |
| REV-26 | Cerrado: el candidato de journal permanece en `staging`; `LoadSnapshot` y `LoadBinding` exigen un receipt ligado a una entrada canónica del journal. Los tests cubren pre-CAS, CAS ambiguo, rollback y restart, y el probe anterior ya no puede cargar el objeto precommit. |
| REV-27 | Abierto: `context.Canceled` simple se preserva, pero `context.Cause` se pierde y deadline no obtiene la categoría `timeout` prevista por el diseño. |

## REV-28 — El perdedor del CAS puede borrar el objeto confirmado del ganador

- Severidad: blocker
- Requirement o regla: R9, R11, R13, T10, T15; convergencia e idempotencia
- Archivo y línea: `internal/contextassembly/store.go:411-454`,
  `internal/contextassembly/store.go:567-590`
- Evidencia: dos solicitudes con idempotency keys distintas pueden producir el
  mismo snapshot digest. El primer writer crea el object y queda bloqueado en
  CAS; el segundo observa `os.ErrExist`, gana el CAS y queda antes de publicar
  su receipt. Al liberarse, el primero pierde la generación, entra en
  `discardCompetingCandidate` con `objectCreated=true` y
  `removeUnconfirmedObject` elimina el object porque el receipt del ganador aún
  no existe. Un probe overlay forzó exactamente esa secuencia; uno de los
  commits terminó con `file does not exist` aunque el witness ya había
  confirmado al ganador (`shared object commits failed: file does not exist /
  <nil>`). Además, al repetir 50 veces la matriz propia de writers,
  `TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge`
  falló con `context assembly store: persistence at witness`.
- Impacto: la autoridad externa puede confirmar un journal/receipt cuyo objeto
  desaparece por acción de un contender. El ganador no puede devolver/cargar
  su snapshot y restart queda ante un estado durable incompleto; la misma
  lógica común afecta bindings.
- Corrección requerida: el cleanup de un contender no debe inferir ownership a
  partir de `objectCreated` ni de la ausencia transitoria de receipt. Mantener
  objetos content-addressed hasta poder demostrar que ningún candidato
  confirmado o incierto los referencia, o publicar cada candidato en staging
  propio y promover el object del ganador. Añadir una prueba coordinada con dos
  keys y digest compartido que libere al perdedor después del CAS del ganador
  pero antes de journal/receipt, tanto para snapshot como binding, y repetirla
  bajo `-race`.

## REV-27 — Se pierde cancel cause y deadline se clasifica como cancelled

- Severidad: blocker
- Requirement o regla: R13, ARCH-006, CONV-005, diseño de errores 328–332
- Archivo y línea: `internal/contextassembly/store.go:136-142`,
  `internal/contextassembly/store.go:184-187`,
  `internal/contextassembly/model.go:174-175`,
  `internal/contextassembly/model.go:587-592`,
  `cmd/featureproof/workflow.go:625-636`
- Evidencia: las fronteras comprueban `ctx.Err()` y lo entregan a
  `contextFailure`; con `context.WithCancelCause`, `ctx.Err()` es
  `context.Canceled` y el sentinel de `context.Cause(ctx)` desaparece. Un probe
  construyó `NewLocalStore` con una causa propia y `errors.Is(err, sentinel)`
  fue falso (`cancel cause lost`). La misma composición devuelve `ctx.Err()`
  directamente. Además, `contextFailure` agrupa `context.DeadlineExceeded` en
  `CategoryCancelled`; ni siquiera existe `CategoryTimeout`, aunque el diseño
  exige categorías separadas `cancelled` y `timeout` y causa estándar.
- Impacto: callers y telemetry no pueden distinguir deadline de cancelación
  explícita ni conservar una causa de cancelación autorizada. Las cuatro
  superficies pierden información necesaria para aplicar retry y outcome sin
  comparación textual.
- Corrección requerida: mapear `context.Cause(ctx)` en las fronteras que poseen
  el contexto, conservarla mediante `Unwrap` sin exponer su texto, y clasificar
  `DeadlineExceeded` como `timeout`. Añadir tests para contexto pre-cancelado,
  Read y Bootstrap con cancel cause, además de deadline real, verificando
  categoría y `errors.Is`.

## Verificación independiente de la revisión 6

- `gofmt -l` sobre Go nuevo/modificado: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- `go test -p 1 ./... -count=1 -timeout 10m`: pass completo.
- `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64: pass completo,
  sin reporte del detector.
- cinco binaries instrumentados Linux ejecutados desde sus package cwd: pass.
- cross-build de los cinco paquetes afectados para Linux, Darwin y Windows:
  15/15 pass.
- cobertura regenerada desde cinco profiles Windows y cinco Linux, tomando el
  máximo por bloque e intersectando exactamente las líneas productivas del diff
  desde `45d325a`: **1860/2177 = 85.438677 %**. Incluye build tags Windows y
  Unix; `publish_unix_other.go` se contabiliza conservadoramente como **0/1**.
  `internal/planapproval/model.go` no genera bloque ejecutable.
- la matriz focal de store pasa 10 repeticiones bajo `-race`, pero falla en la
  repetición normal `-count=50` descrita en REV-28; el probe coordinado reproduce
  de forma determinista la pérdida del object.
- aprobación, única feature activa, tasks, `git diff --check`, auditoría AST de
  comentarios y limpieza de temporales del repositorio: pass.

No se encontró adquisición GitHub de feature 30 ni lifecycle de environments de
feature 31; sus menciones siguen limitadas a exclusiones documentales. El
rechazo procede exclusivamente de concurrencia/durabilidad del store y de la
taxonomía/causa de cancelación dentro del scope de feature 29.

## Revisión 7 — 2026-10-24

**Veredicto: rejected**

Se volvió a revisar el diff completo desde `45d325a`. La feature 29 continúa
como única `in_progress`, la spec está aprobada y T1–T16 están marcadas. REV-27
queda cerrado y la intercalación concreta de REV-28 ya preserva el objeto para
snapshot y binding. Sin embargo, el cleanup conserva una ventana TOCTOU entre
su scan de ownership y el borrado. Un writer nuevo puede adquirir el mismo
digest en esa ventana y perder sus bytes; las suites globales también
reproducen fallos propios del store bajo concurrencia.

### Estado de REV-1..REV-28

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-26 | Sin regresión observada en trust/filtros, typed channels, budgets, conflictos/insuficiencia, binding/drift, observer, CAS/recovery, addressability, filesystem, roles/consumers, cuatro superficies o Linux. |
| REV-27 | Cerrado: `CategoryTimeout` separa deadline de cancelación; `contextFailureFromContext` y `workflowContextFailure` preservan `context.Cause` mediante `Unwrap`, mientras `Error()` contiene solo texto estable del sistema. Read, Bootstrap, constructor pre-cancelado y deadline están cubiertos. |
| REV-28 | Cerrado para la intercalación reportada: antes de borrar se consideran staging, receipts y journal; los tests coordinados de dos keys/mismo digest pasan para snapshot y binding. La decisión scan-then-delete todavía no es atómica frente a un candidato que aparece después del scan; continúa como REV-29. |

## REV-29 — Un candidato nuevo puede adquirir el digest entre el scan y el borrado

- Severidad: blocker
- Requirement o regla: R9, R11, R13, T10, T15; atomicidad, concurrencia e
  idempotencia
- Archivo y línea: `internal/contextassembly/store.go:581-639`
- Evidencia: `removeUnconfirmedObject` llama a `objectReferencedOnce` y, si el
  resultado es falso, ejecuta después `removeChild` sin lock, CAS, rename de
  ownership ni segundo scan. Un probe determinista dejó que cleanup escaneara
  `staging`, lo bloqueó en un temporal válido de `receipts`, creó entonces una
  entrada canónica de staging que referenciaba el mismo object digest y retiró
  el temporal. Cleanup terminó el scan de receipts/journal y borró el object;
  el probe obtuvo `cleanup removed newly referenced object: ... file not
  found`, aunque `objectReferencedOnce` confirmó después la referencia staged.
  Esta secuencia corresponde a un tercer writer que publica su candidato entre
  el scan y el delete.
- Evidencia adicional: la matriz focal solicitada pasa `-count=50` normal, pero
  una ejecución combinada `-race -count=20` falló
  `TestSharedDigestWritersPreserveWinnerObject/snapshot` con
  `context assembly store: timeout at context`. El mismo test aislado pasa 20
  repeticiones race. La suite global race falló la variante binding porque el
  creator no alcanzó CAS dentro del timeout. En una segunda suite global normal,
  `TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge`
  falló con `persistence at staging`. Son fallos del paquete modificado, no solo
  flakes ajenos.
- Impacto: un contender puede dejar journal/staging con referencia válida a un
  snapshot o binding cuyo object ya fue eliminado. El run ganador falla al
  cargar y restart encuentra estado incompleto; además los gates requeridos no
  son estables bajo concurrencia real y race.
- Corrección requerida: hacer indivisible la transición ownership/delete. Una
  opción es no borrar objetos content-addressed durante contención y ejecutar
  recolección posterior desde un snapshot autoritativo; otra es promover y
  retirar candidatos dentro de un protocolo CAS que impida nuevas referencias
  al digest durante delete. Añadir un test coordinado con tres writers que
  inserte staging después del primer scan y antes del remove, para snapshot y
  binding, y exigir estabilidad normal/race alta sin ampliar timeouts para
  ocultar la carrera.

## Verificación independiente de la revisión 7

- `gofmt -l` sobre Go nuevo/modificado: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- matriz REV-27 para cancel cause/deadline y composición Read/Bootstrap:
  `-count=20`, pass.
- matriz de snapshot/binding compartido y cleanup: `-count=50` normal, pass;
  combinada `-race -count=20`, fail por timeout del test compartido; el test
  compartido aislado `-race -count=20` pasa.
- `go test -p 1 ./... -count=1 -timeout 10m`: primera ejecución falló en
  `functionalflow` y `previewenv`; la repetición ya no mostró esos fallos, pero
  falló el test de writers de `contextassembly` con `persistence at staging`.
- `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64: fail en
  `TestSharedDigestWritersPreserveWinnerObject/binding`; no hubo reporte de data
  race, pero el gate funcional no pasó.
- cinco binaries instrumentados Linux ejecutados desde sus package cwd: pass.
- cross-build de los cinco paquetes afectados para Linux, Darwin y Windows:
  15/15 pass.
- cobertura regenerada desde cinco profiles Windows y cinco Linux, fusionando
  máximo por bloque e intersectando el diff productivo desde `45d325a`:
  **1916/2242 = 85.459411 %**. Incluye tags Windows/Unix y cuenta
  `publish_unix_other.go` conservadoramente como **0/1**;
  `internal/planapproval/model.go` no produce bloque ejecutable.
- aprobación, única feature activa, tasks, `git diff --check`, auditoría AST de
  comentarios y ausencia de temporales en el repositorio: pass.

No se detectó adquisición GitHub de feature 30 ni lifecycle de environments de
feature 31; las menciones son exclusiones de scope. El rechazo se limita al
protocolo de cleanup/concurrencia de `LocalStore` dentro de feature 29.

## Revisión 8 — 2026-10-24

**Veredicto: approved**

Se revisó de nuevo el diff completo desde `45d325a`. La feature 29 continúa
como única `in_progress`, la spec permanece aprobada y T1–T16 están marcadas.
REV-29 queda cerrado: el protocolo transaccional ya no elimina objetos
content-addressed durante commit, rollback, contención ni recovery. Los
rollback y reconciliaciones retiran únicamente metadata no direccionable de
`staging` o el tail no confirmado de `journal`; los bytes inmutables quedan
retenidos para una recolección autoritativa posterior.

### Estado de REV-1..REV-29

| Finding previo | Estado en esta revisión |
|---|---|
| REV-1..REV-27 | Sin regresión observada en filtros/trust, canales tipados, budgets, contradicciones e insuficiencia, binding/drift, seal/provenance, observer exactly-once, CAS/recovery, filesystem no-follow, cancelación, roles/consumers, cuatro superficies y portabilidad. |
| REV-28 | Cerrado y estable: los objetos compartidos por distintas idempotency keys sobreviven a ganador, perdedores y restart; snapshot y binding cargan después de la intercalación. |
| REV-29 | Cerrado: `rollbackCandidate`, `discardCompetingCandidate`, `reconcileStagedCandidates` y `reconcilePendingTail` solo eliminan metadata de candidato/tail. No existe ya decisión scan-then-delete ni borrado transaccional en `snapshots` o `bindings`. |

El test coordinado `TestSharedDigestWritersPreserveWinnerObject` usa tres
writers con keys distintas y el mismo digest, tanto para snapshot como para
binding. El creator queda pausado en CAS, el ganador avanza y se pausa antes
de journal/receipt, el creator alcanza la frontera de retención y solo entonces
se publica el staging del tercer writer. Antes de liberar ganador y tercer
writer, el test comprueba dos candidatos staged y la existencia del objeto;
después exige éxito de los tres commits, un único objeto y carga pública
válida. Aunque ya no existe el scan inseguro de REV-29, esta secuencia conserva
el punto adversarial solicitado y verifica que ningún cleanup transaccional
puede retirar los bytes compartidos.

### Trazabilidad y comportamiento

- R1–R15 conservan pruebas reales y trazabilidad en T1–T16: filtros
  estructurales antes de ranking, secciones de trust irreversibles, budgets
  exactos por item/bytes/tokens, required/optional/conflict policies, guards y
  allowlists, límites de payload/trabajo/duración, redaction y taxonomía.
- El binding y la detección de drift preceden approval, execution y recovery;
  provenance, `PreparedSnapshot` y artifact incluyen digest/envelope sellados.
- Planner, analyzer y diagnostic permanecen aislados por rol y consumen sus
  contextos productivos; CLI, API, MCP y GitHub comparten la misma composición.
- Registry descriptors y producers siguen bajo autoridad de composición y se
  revalidan al recomponer. No reaparecen payloads legacy ni autoridad del
  caller.
- Journal/receipts siguen canónicos, autenticados, content-free y append-only;
  los loads solo direccionan objetos confirmados. Los boundaries conservan
  `context.Cause` por `Unwrap` sin filtrar su texto y distinguen `cancelled` de
  `timeout`.
- No quedan file locks ni E/S externa bajo mutex o región crítica. Writers
  independientes progresan con otro CAS bloqueado y la misma identidad
  converge sin duplicar journal/receipt.

### Verificación independiente de la revisión 8

- `gofmt -l` sobre Go nuevo/modificado: pass, salida vacía.
- `go vet ./...`: pass.
- `go test ./... -run '^$' -count=1 -timeout 10m`: pass.
- matriz focal exacta de objetos compartidos y writers bloqueados, normal
  `-count=50`: pass. La matriz ampliada bajo race `-count=20`: pass, sin reporte
  del detector.
- una primera ejecución de ambas matrices pesadas en paralelo agotó el timeout
  de `TestPrecommitObjectsRemainUnaddressable`; ejecutadas secuencialmente, las
  matrices exigidas pasaron. No se usó esa ejecución interferida como evidencia
  favorable.
- `go test -p 1 ./... -count=1 -timeout 10m`: pass completo.
- `go test -race -p 1 ./... -count=1 -timeout 10m` con UCRT64: una primera
  ejecución falló únicamente en el test ajeno
  `internal/githubpr/TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff`;
  el test pasó aislado y la repetición global pasó completa, sin reporte del
  race detector.
- cinco binaries instrumentados Linux ejecutados desde sus package cwd: 5/5
  pass.
- cross-build de los cinco paquetes afectados para Linux, Darwin y Windows:
  15/15 pass.
- cobertura regenerada desde cinco profiles Windows y cinco Linux, fusionando
  el máximo por bloque e intersectando las líneas productivas del diff desde
  `45d325a`: **1881/2187 = 86.008230 %**. Incluye build tags Windows/Unix y
  contabiliza `publish_unix_other.go` conservadoramente como **0/1**;
  `internal/planapproval/model.go` no produce bloque ejecutable.
- aprobación, única feature activa, tasks, `git diff --check`, auditoría AST de
  comentarios sistémicos y ausencia de temporales de test dentro del
  repositorio: pass.

No se detectó implementación productiva de adquisición GitHub de feature 30 ni
lifecycle de environments de feature 31; sus menciones siguen limitadas a
exclusiones documentales. No quedan blockers abiertos de REV-1..REV-29 ni se
encontraron regresiones de R1–R15.
