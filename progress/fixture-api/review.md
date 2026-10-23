# Feature 26 — Fixture API review

## Veredicto

`rejected`

La implementación no satisface R1, R4, R5, R7, R8, R9, R11, R12 y R13. Los gates mecánicos focales pasan, pero faltan integración productiva y garantías de recovery/ownership requeridas por la spec.

## Precondiciones

- Existe exactamente una feature `in_progress`: feature 26 `fixture-api`.
- La aprobación humana consta en `specs/fixture-api/requirements.md` (`approved_by: user`, `approved_at: 2026-10-22T18:29:01+02:00`).
- T1–T14 aparecen marcadas `[x]`.
- Existe `progress/fixture-api/implementation.md`.

## Hallazgos

## REV-1 — El lifecycle no está compuesto en ningún flujo productivo

- Severidad: blocker
- Requirement o regla: R2, R6, R12; T10, T12, T13; ARCH-010/011
- Archivo y línea: `cmd/featureproof/workflow.go:523`; `internal/validationworkflow/service.go:23-42`
- Evidencia: se añadió el puerto opcional `Dependencies.Fixtures`, pero la única composición productiva de `validationworkflow.NewService` no lo proporciona. La búsqueda `rg -n "Fixtures:|\\.Fixtures\\s*=|WorkflowAdapter|NewLifecycle\\(" --glob '*.go' --glob '!**/*_test.go' .` solo encuentra definiciones y usos internos; no existe construcción productiva de `fixture.NewLifecycle`, `fixture.WorkflowAdapter`, `fixture.FileStore`, binder, scopes, executor ni authority. Tampoco hay cambios en adapters CLI/API/MCP/GitHub. El test `TestFixtureLifecyclePreservesPrimaryAndProjectsCleanupAcrossWorkflow` inyecta manualmente un fake en el service y no atraviesa ninguna de las cuatro superficies.
- Impacto: una specification v3 puede declarar fixtures, pero el binario real ejecuta el workflow con `Fixtures == nil`; no prepara ni limpia nada. La paridad declarada en R12 no existe.
- Corrección requerida: componer el lifecycle completo desde configuración confiable y conectar el mismo service a las cuatro superficies. Añadir pruebas verticales reales de CLI, API, MCP y GitHub que prueben admisión, prepare, primary, cleanup y proyección segura.

## REV-2 — El intent durable no conserva elegibilidad tras crash o fallo de persistencia posterior al I/O

- Severidad: blocker
- Requirement o regla: R7, R8, R13; T8, T10
- Archivo y línea: `internal/fixture/lifecycle.go:67-76`, `internal/fixture/lifecycle.go:104-143`, `internal/fixture/lifecycle.go:166-169`
- Evidencia: antes de red solo se persiste `Intents[fixtureID] = true`. `Eligible` se vuelve `true` en memoria después de I/O y se intenta persistir después de la respuesta/capturas. Si el proceso cae o ese `Save` falla entre ambos puntos, el estado durable conserva `Intent=true` y `Eligible=false`. `Cleanup` ignora el intent y filtra exclusivamente por `state.Eligible`; por tanto no evalúa cleanup. Una redelivery de `Prepare` tampoco interpreta el intent pendiente como incierto y vuelve a llamar a create. El test de recovery solo cubre un create ya confirmado (`Prepared=true`), no los checkpoints posteriores a I/O reclamados por el informe.
- Impacto: un recurso realmente creado puede quedar sin cleanup; además una recuperación puede repetir create cuando su resultado anterior quedó incierto.
- Corrección requerida: definir y persistir una máquina de recovery donde todo intent pendiente sea conservadoramente evaluable para cleanup, distinguir checkpoints sin inventar confirmación y probar crash/save failure después de write, respuesta, evidencia y captura. La recuperación no debe repetir un create confirmado ni abandonar uno posiblemente creado.

## REV-3 — El workflow confirma terminal aunque fixture cleanup devuelva error

- Severidad: blocker
- Requirement o regla: R8, R11, R13; T10, T11
- Archivo y línea: `internal/validationworkflow/service.go:745-791`, especialmente `:762-784`
- Evidencia: `terminal` recibe `fixtureErr`, pero solo usa el error para rellenar status cuando el report viene vacío. Después continúa liberando preview/finalizer, elimina el executable privado y guarda `PhaseTerminal` aun si el fixture store, el journal o la confirmación del reporte fallaron. Una redelivery entra por `record.Phase == PhaseTerminal` y retorna inmediatamente, de modo que no existe recovery del cleanup pendiente.
- Impacto: se puede publicar un run terminal y perder la autoridad privada necesaria para converger una obligación de cleanup no confirmada. Contradice el criterio de R11 que exige que un fallo de persistencia no publique falso éxito y quede recuperable.
- Corrección requerida: conservar el workflow en cleanup pending ante cualquier error no confirmado, no borrar material de recovery, persistir la incidencia segura y reanudar idempotentemente en redelivery hasta terminal durable o agotamiento terminal explícito.

## REV-4 — El estado durable y el digest no autentican la semántica ejecutable ni las capturas

- Severidad: blocker
- Requirement o regla: R3, R5, R7, R10, R13; T4, T10; ARCH-007/009
- Archivo y línea: `internal/fixture/binder.go:58-75`; `internal/fixture/model.go:88-105`; `internal/fixture/store.go:97-114`; `internal/fixture/capture.go:132-144`; `internal/fixture/lifecycle.go:254-267`
- Evidencia: `BoundFixture.Declaration` tiene `json:"-"`, por lo que `json.Marshal(bound)` usado para calcular `BoundSet.Digest` no liga inputs, accepted statuses, capture declarations ni cleanup inputs. Al cargar, solo se compara el string almacenado con `bound.Digest`; nunca se recomputa el digest de la autoridad recibida. El store solo comprueba mapas no nulos. `ResolveInput` verifica IDs/tipo, pero no recomputa `Capture.Digest`, no compara `Capture.BindingDigest` con el binding esperado y no valida que `Value` conserve bytes/digest. También se aceptan mapas `Prepared`, `Eligible`, `Intents` y terminales con claves/estados ajenos al bound set.
- Impacto: una declaration o captura manipulada puede conservar el mismo digest y alimentar path/body de una llamada autorizada; también se puede marcar un fixture como preparado o limpio sin una transición válida.
- Corrección requerida: sellar toda la semántica ejecutable canónica (sin valores secretos), recomputar/verificar el sello en cada delivery, validar exhaustivamente el estado contra el bound set y verificar run/fixture/name/type/value digest/binding digest de cada captura antes de usarla. Añadir tests de tampering por cada campo.

## REV-5 — Cleanup carece de claim durable previo al efecto y permite efectos duplicados entre owners

- Severidad: blocker
- Requirement o regla: R9, R10, R13; T9, T10; CONV-005
- Archivo y línea: `internal/fixture/lifecycle.go:150-231`; `internal/fixture/store.go:25-66`
- Evidencia: dos owners pueden ejecutar `Load` del mismo estado, observar el mismo fixture elegible y ambos cruzar red en `executor.Execute` antes de que cualquiera haga `Save`. El lock de `FileStore` solo cubre operaciones individuales de Load/Save, no el intervalo de policy/HTTP. No existe transición/lease/claim CAS `cleanup_started` antes del efecto. La suite no contiene una carrera con dos lifecycle owners ni barreras; `TestFileStoreCASCorruptionAndCancellation` solo comprueba dos Saves secuenciales y `TestWorkflowAdapterDelegatesSharedLifecycleAndRejectsHandleDrift` también es secuencial.
- Impacto: R13 exige un único efecto por intento lógico. Con redelivery concurrente pueden emitirse dos DELETE y sus resultados competir en CAS después del efecto.
- Corrección requerida: adquirir/renovar ownership real o confirmar por CAS un claim durable antes de cada intento, revalidar contexto después de obtenerlo y antes de red, y hacer que el perdedor recupere el estado confirmado. Probar dos owners con barreras y race detector, sin sleeps.

## REV-6 — Agotamiento, cancelación y denegación de cleanup no producen terminales durables por fixture

- Severidad: high
- Requirement o regla: R8, R9, R12; T9
- Archivo y línea: `internal/fixture/lifecycle.go:166-202`, `internal/fixture/lifecycle.go:233-250`
- Evidencia: las ramas de call budget (`:171-173`, `:193-196`), deadline/cancel (`:184-191`) y revalidation denial (`:198-201`) solo agregan incidencias al report. No actualizan `CleanupTerminal`, no guardan un attempt/event y aun así el método puede marcar `ReportConfirmed=true`. Los fixtures quedan pendientes en state mientras el report afirma un cierre global. La prueba de budget solo verifica número de requests y status del report; no inspecciona durabilidad, eventos ni redelivery.
- Impacto: el journal no puede reconstruir cada terminal, una nueva invocación puede volver a intentar trabajo ya reportado como agotado y el workflow puede finalizar con obligaciones sin clasificación durable.
- Corrección requerida: persistir de forma CAS cada terminal seguro o estado recuperable, emitir eventos ordenados, no confirmar el reporte hasta que todos los elegibles tengan terminal durable, y probar redelivery tras cada clase de agotamiento/cancelación.

## REV-7 — La preparación no proyecta dependientes bloqueados

- Severidad: high
- Requirement o regla: R4
- Archivo y línea: `internal/fixture/lifecycle.go:54-84`; `internal/fixture/model.go:276-281`
- Evidencia: ante el primer fallo, `Prepare` añade únicamente el fixture fallido y retorna. Los restantes no aparecen en `FixtureSummary.Fixtures` con estado `blocked` ni con causa segura/ordenada. `CreateStatus` es un string abierto y no existe representación de la causa de bloqueo. El test `TestPreparationFailureStartsCleanupWithoutValidationPlans` solo verifica que no se hagan más requests.
- Impacto: se evita el efecto posterior, pero no se cumple el comportamiento observable requerido y la auditoría no distingue fixture fallido de dependientes bloqueados.
- Corrección requerida: modelar estados cerrados y causas seguras, proyectar todos los dependientes/no iniciados en orden determinista y probar ramas independientes y cierre transitivo.

## REV-8 — El contrato v3 acepta tipos y literales prohibidos

- Severidad: high
- Requirement o regla: R1, R5
- Archivo y línea: `internal/specification/validate.go:317-359`; `internal/specification/yaml/loader.go:847-878`; `internal/fixture/capture.go:75-99`
- Evidencia: `validFixtureType` acepta `null` y `matchesType(nil, "null")` lo confirma, aunque la spec establece que null no satisface ninguno de los tipos capturables iniciales. Además, la rama `Kind == "literal"` retorna sin comprobar que el valor sea JSON finito; la traducción YAML decodifica a `any` sin rechazar números no finitos. `cloneJSON` ignora errores de marshal/unmarshal y puede transformar silenciosamente un literal no representable en `nil`. La unión del modelo interno tampoco rechaza campos de otros brazos cuando `Kind` selecciona uno.
- Impacto: una v3 inválida puede superar validación y cambiar de semántica durante clone/resolution, rompiendo el contrato estricto y los límites de captura.
- Corrección requerida: separar tipos permitidos de capture/reference, excluir null inicialmente, validar recursivamente JSON finito y la exclusividad real de cada unión también en el modelo interno, y propagar errores de clone en vez de ignorarlos.

## REV-9 — T14 declara matrices que no existen

- Severidad: blocker
- Requirement o regla: R6, R7, R8, R9, R12, R13; T14; `docs/verification.md`
- Archivo y línea: `specs/fixture-api/tasks.md:81-110`; `progress/fixture-api/implementation.md`
- Evidencia: no existen pruebas equivalentes a cancelación en cada frontera, dos owners/redelivery, lease expiry/handoff, crash matrix post-I/O, persistencia fallida del cleanup report, cuatro superficies productivas, matriz recursiva sobre logs/métricas/model/evidence ni credenciales rotadas por retry/recovery. Los tests encontrados son unitarios y secuenciales. El informe atribuye paridad a “suites existentes” aunque ninguna recibe un lifecycle productivo. Los defectos REV-1..REV-8 son precisamente caminos sin prueba.
- Impacto: R6/R7/R8/R9/R12/R13 carecen de trazabilidad ejecutable suficiente; una task marcada completa no cumple la puerta de verificación.
- Corrección requerida: implementar los comportamientos y añadir pruebas que fallen sin ellos, con nombres/evidencia actualizados en el informe. No marcar T14 completa mientras falte cualquier matriz obligatoria.

## Comentarios de código

- `internal/fixture/store.go:13`: `safeRunID` es una entidad package-level nueva sin comentario sistémico. Debe documentarse según la convención aprobada. Severidad: low.

## Trazabilidad independiente

| Requirement | Implementación/test localizado | Resultado |
|---|---|---|
| R1 | specification v3 + `fixture_v3_test.go` | Fail: null/no-finite/union interna (REV-8) |
| R2 | validator/binder/HTTP unit tests | Fail vertical: no composición productiva (REV-1) |
| R3 | `ValidateSet`; graph tests | Pass unitario; digest no liga declaration (REV-4) |
| R4 | `Lifecycle.Prepare`; workflow fake | Fail: dependientes no se proyectan blocked (REV-7) |
| R5 | capture extractor/resolver | Fail: captura durable no se autentica y null permitido (REV-4/8) |
| R6 | identity binding/HTTP adapter | Fail vertical y matriz de recovery ausente (REV-1/9) |
| R7 | intent/state/recovery | Fail: intent pendiente se omite para cleanup (REV-2) |
| R8 | reverse cleanup tests | Fail en recovery/agotinamiento durable (REV-2/6) |
| R9 | cleanup scope/budgets | Fail: terminales no durables y ownership incompleto (REV-5/6) |
| R10 | policy/key/retry unit tests | Fail en redelivery concurrente/sello (REV-4/5) |
| R11 | primary result/workflow fake | Fail: workflow traga cleanup error y finaliza (REV-3) |
| R12 | proyecciones unitarias | Fail: sin adapters productivos/paridad/auditoría completa (REV-1/6/9) |
| R13 | CAS unitario + race | Fail: no claim previo a cleanup ni test multi-owner (REV-5/9) |

## Verificaciones ejecutadas

- `gofmt -d` sobre todos los Go modificados/nuevos: limpio.
- `git diff --check`: correcto; solo warnings CRLF del checkout.
- `go vet ./internal/fixture ./internal/specification/... ./internal/validationworkflow`: correcto.
- `go test -run '^$' ./...`: correcto.
- `go test -count=10 ./internal/fixture ./internal/specification/...`: correcto.
- `go test -count=5 ./internal/validationworkflow`: correcto.
- `CGO_ENABLED=1 CC=gcc go test -race -count=1 ./internal/fixture ./internal/specification/... ./internal/validationworkflow` con UCRT64 en `PATH`: correcto.
- Cross compile mediante `go test -c` para Linux/amd64 y Darwin/amd64 en fixture, specification, specification/yaml y validationworkflow: correcto.
- Coverage: `internal/fixture` 86,4 %; `internal/validationworkflow` 85,6 %.
- `go test ./...`: falla únicamente `internal/localvalidation/TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink` porque Windows rechaza `os.Symlink` con `A required privilege is not held by the client`; el resto de paquetes pasa. La limitación alegada queda reproducida, pero la suite global no está verde en este host.

## Decisión

La feature requiere remediación y una nueva revisión independiente. No debe pasar a `done`.

---

# Segunda revisión independiente

## Veredicto de segunda revisión

`rejected`

La remediación cierra materialmente REV-1, REV-2, REV-3 y REV-7, y mejora REV-4/REV-6/REV-8. Sin embargo, REV-5 y REV-9 siguen abiertos y aparecen nuevos incumplimientos bloqueantes en idempotencia, policy/budget, ownership y autenticidad del estado durable.

## Revalidación de REV-1..REV-9

| Hallazgo previo | Estado | Evidencia de segunda revisión |
|---|---|---|
| REV-1 | cerrado | `composeWorkflowFixtures` construye e inyecta el lifecycle en el único service productivo; la vertical API/CLI/MCP/GitHub pasa repetida. |
| REV-2 | cerrado | El intent se vuelve conservadoramente cleanup-eligible antes de I/O y `TestPostIOPersistenceCrashRecoversAsCleanupOnly` prueba cero replay tras fallo post-I/O. |
| REV-3 | cerrado | `validationworkflow.Service.terminal` conserva `CleanupPending` y devuelve unavailable cuando fixture cleanup no se confirma; recovery no repite primary. |
| REV-4 | parcial | El bound y las captures se sellan/revalidan, pero el estado acepta terminales/intents forjados con forma válida (REV-14). |
| REV-5 | abierto | El claim CAS existe, pero varias intercalaciones todavía permiten efectos duplicados (REV-10). |
| REV-6 | parcial | Budget/deadline/cancel/denial escriben terminal por fixture; con contexto vencido, state y journal no se confirman atómicamente, y faltan pruebas productivas en esas fronteras. |
| REV-7 | cerrado | La proyección usa estados cerrados y distingue dependientes de ramas abortadas en orden estable. |
| REV-8 | parcial | Null capture y float no finito se rechazan; `json.Number("NaN")` sigue superando la validación del modelo (REV-17). |
| REV-9 | abierto | Hay nuevas matrices útiles, pero faltan cancelación en cada frontera, carreras terminal/same-owner, policy/budget real, capture sensible y package durable. |

## Nuevos hallazgos

## REV-10 — El claim de cleanup todavía permite DELETE duplicado

- Severidad: blocker
- Requirement o regla: R8, R10, R13; T9, T10; CONV-005
- Archivo y línea: `internal/fixture/lifecycle.go:319-350`, seguido de `internal/fixture/lifecycle.go:250-277`
- Evidencia: si dos invocaciones comparten `config.Owner`, la condición de exclusión en `:327` no bloquea al segundo owner lógico; ambos pueden renovar/sobrescribir el mismo claim y cruzar red. Con owners distintos también hay una carrera: tras perder CAS, si el ganador confirma `CleanupTerminal` antes del reload, `acquireCleanupClaim` retorna estado terminal y `nil` en `:345-346`; el caller no vuelve a comprobar el terminal y continúa hasta `executor.Execute`. El claim tampoco se renueva ni se verifica justo antes de I/O, así que puede expirar durante policy/HTTP y habilitar handoff concurrente.
- Impacto: una misma fase lógica puede ejecutar cleanup dos veces. La prueba existente solo bloquea al segundo owner mientras el primer claim está activo; no cubre same-owner, terminal entre conflict/reload ni expiry durante I/O.
- Corrección requerida: identificar cada invocación con token de claim único, retornar un resultado explícito `already_terminal`, revalidar ownership/expiry inmediatamente antes de red y asegurar que LeaseTTL domina el timeout o renovar de forma owned. Añadir barreras deterministas para las tres intercalaciones y ejecutarlas bajo race.

## REV-11 — La composición concede idempotencia a POST sin garantía del contrato

- Severidad: blocker
- Requirement o regla: R10; T4, T7; contrato YAML `idempotency: contract`
- Archivo y línea: `cmd/featureproof/workflow.go:610-616`; `internal/fixture/binder.go:19-38`
- Evidencia: si el runtime permite el header `Idempotency-Key`, la composición marca **todas** las operaciones retenidas como `KeyCapable`, sin comprobar que cada operación del contrato declare ese header o una semántica idempotente. El contrato usado por `TestProductFixtureLifecycleParityAcrossAllSurfaces` no declara `Idempotency-Key` en el POST `/fixtures`, pero el POST queda admitido y el test pasa porque el manifest runtime sí permite el header.
- Impacto: una allowlist de transporte se convierte en autoridad contractual y habilita retries/redelivery de un POST cuyo servidor no prometió deduplicación.
- Corrección requerida: derivar key capability exclusivamente de metadata verificable de la operación aprobada, incluida la ubicación exacta del header, o de semántica idempotente contractual. La allowlist runtime solo debe limitar el envío, nunca acreditar la garantía.

## REV-12 — Create y cleanup no atraviesan policy ni un budget de ejecución real

- Severidad: blocker
- Requirement o regla: R2, R4, R9, R10; ARCH-001/005/011; T5, T7, T9, T12
- Archivo y línea: `internal/fixture/product.go:67-82`; `internal/fixture/http_adapter.go:43-80`; `internal/localvalidation/fixture_http.go:12-42`
- Evidencia: `SealedRevalidator` solo recomputa el sello y compara binding. `HTTPAdapter.Execute` llama directamente a `httptool.Tool.Execute`; no existe puerto ni evaluación de policy y tampoco reserva el budget de tool calls del run antes de create. Cleanup tiene contador local de calls, pero la revalidación de policy que exige la spec tampoco existe. La búsqueda productiva de `Policy|Budget` en `internal/fixture` solo encuentra comentarios, idempotencia y el contador cleanup.
- Impacto: una fixture aprobada por contrato/identidad puede ejecutarse aunque la policy efectiva la deniegue o el presupuesto de preparación esté agotado. La documentación de interfaces afirma una frontera que el código no implementa.
- Corrección requerida: inyectar puertos concretos de registry/policy/budget, evaluar y reservar antes de cada efecto, clasificar denials y revalidar en retry/recovery. Añadir tests productivos de policy denial y límites exactos con spy de red a cero.

## REV-13 — El scope productivo de cleanup se desliga del shutdown mediante context.Background

- Severidad: high
- Requirement o regla: R9, R13; diseño aprobado de cancelación/ownership
- Archivo y línea: `internal/fixture/product.go:51-65`; `internal/fixture/workflow.go:55-64`
- Evidencia: `FiniteScopes.New` crea el scope con `context.WithTimeout(context.Background(), ...)`; su firma no recibe el shutdown scope. `WorkflowAdapter.Cleanup` recibe un contexto owned, pero `Lifecycle.Cleanup` lo descarta completamente.
- Impacto: cleanup tiene deadline, pero no observa shutdown del proceso/owner y contradice el requisito explícito de scope externo finito sin background oculto.
- Corrección requerida: hacer que el factory derive del shutdown scope provisto por composición (separado del run cancelado), propagar ownership y probar cancelación de shutdown durante claim, policy, credential resolution, HTTP, redacción y persistencia.

## REV-14 — Un estado durable forjado puede declarar cleanup success sin intento

- Severidad: blocker
- Requirement o regla: R7, R11, R13; ARCH-007; T10
- Archivo y línea: `internal/fixture/integrity.go:72-131`; `internal/fixture/lifecycle.go:361-412`; `internal/fixture/store.go:98-114`
- Evidencia: `ValidateState` acepta `Intents[fixture]=true`, `CleanupTerminal[fixture]=success` y `ReportConfirmed=true` aunque no exista attempt terminal, claim previo ni evento relacionado. También acepta eliminar/cambiar flags de intent/eligible con combinaciones estructuralmente válidas. El estado no posee sello/MAC encadenado y el loader no verifica transición o digest integral. `Cleanup` reconstruye el report solo desde esos mapas y publicaría `succeeded` sin DELETE.
- Impacto: la manipulación de intent/terminal requerida por R7 no falla cerrado; puede omitir cleanup o fabricar éxito operacional.
- Corrección requerida: autenticar el snapshot durable completo y validar invariantes cruzadas attempt/claim/intent/terminal/report, o reconstruir desde journal encadenado verificable. Añadir mutaciones de valores válidos, no solo claves extranjeras/categorías abiertas.

## REV-15 — Una captura sensible se convierte en una captura válida del marcador de redacción

- Severidad: high
- Requirement o regla: R5, R12; T6, T12
- Archivo y línea: `cmd/featureproof/workflow.go:579-586`; `internal/evidence/redaction.go:321-329`; `internal/fixture/capture.go:18-45`
- Evidencia: el sanitizer productivo sustituye una ruta sensible por el string `[REDACTED]`. `ExtractCaptures` no conoce la policy ni el path sensible; si la capture declara `value_type: string`, extrae ese marcador, lo vuelve a sanear como scalar root y lo confirma como capture. No existe validación previa con `Policy.OverlapsSensitive`, y la matriz de canarios no prueba una capture apuntando a una ruta sensible.
- Impacto: el valor crudo no se filtra, pero el fixture se marca preparado y cleanup recibe un identificador inventado, en vez de fallar cerrado como exige R5.
- Corrección requerida: rechazar en composición todo selector de capture que solape redacción persistida, o hacer que el sanitizer entregue metadata de redacción que `ExtractCaptures` trate como error terminal.

## REV-16 — Fixture summary, intents y audit trail no forman parte del package durable

- Severidad: high
- Requirement o regla: R11, R12; T11, T12
- Archivo y línea: `internal/validationworkflow/service.go:762-788`; `internal/validationworkflow/model.go:244-262`; `internal/localvalidation/publisher.go:164`
- Evidencia: el service adjunta `Fixtures` a la `PackageProjection` guardada en el workflow record después de leer el package, pero no reescribe ni extiende `report.json`, `report.md`, `package.json` o `events.jsonl` del package verificado. El journal/evidence de fixtures vive aparte bajo `.featureproof/workflows/fixtures`. La búsqueda productiva de `Fixtures` en `internal/localvalidation` no encuentra integración de package/report.
- Impacto: el resultado consultado al service muestra summary, pero el paquete verificable publicado no contiene la relación intents/attempts/terminales requerida y su verificador offline no puede auditarla.
- Corrección requerida: incorporar una sección allowlisted de fixtures y referencias a audit records en el package antes de su sello final, actualizar el verificador offline y probar tampering/redacción del artefacto resultante.

## REV-17 — El modelo interno todavía acepta json.Number no finito

- Severidad: high
- Requirement o regla: R1
- Archivo y línea: `internal/specification/validate.go:370-400`
- Evidencia: para `json.Number`, `validFixtureJSON` comprueba solo el error de `Float64`. `json.Number("NaN").Float64()` produce NaN sin error, por lo que el modelo se valida aunque no sea JSON finito; posteriormente `json.Marshal` lo rechaza. El test nuevo solo cubre `math.NaN()` como float64.
- Impacto: la validación interna acepta una specification que cambia a error en una frontera posterior, rompiendo el contrato v3 estricto.
- Corrección requerida: comprobar también `math.IsNaN/IsInf` para `json.Number` y añadir casos NaN/±Inf/exponente fuera de rango.

## Convención de comentarios

- La mayor parte del código nuevo está documentada. Siguen sin comentario sistémico algunas funciones modificadas en tests históricos, por ejemplo `internal/localvalidation/package_reader_test.go:1333`, pese a que la convención incluye helpers y tests modificados. Severidad: low.

## Trazabilidad de segunda revisión

| Requirement | Resultado |
|---|---|
| R1 | Fail: `json.Number` no finito aceptado (REV-17). |
| R2 | Fail: binding productivo existe, pero falta policy/budget obligatoria (REV-12). |
| R3 | Pass: grafo, orden y sello de declarations cubiertos. |
| R4 | Fail: blocked projection cerrada; revalidación de policy/budget ausente (REV-12). |
| R5 | Fail: tampering de capture mejorado; capture sensible no falla cerrado (REV-15). |
| R6 | Pass parcial: identidad late-bound y rotación productiva verificadas. |
| R7 | Fail: recovery post-I/O cubierto; estado válido forjado aún fabrica/omite cleanup (REV-14). |
| R8 | Fail: orden inverso nominal; claim aún permite efecto duplicado (REV-10). |
| R9 | Fail: budgets terminales mejorados; scope ignora shutdown y policy (REV-12/13). |
| R10 | Fail: claim concurrente e idempotencia contractual no demostrada (REV-10/11). |
| R11 | Fail: primary se preserva; package durable carece del cleanup audit (REV-16). |
| R12 | Fail: paridad nominal pasa; faltan policy, sensitive capture y package verificable (REV-12/15/16). |
| R13 | Fail: race suite pasa, pero no cubre intercalaciones duplicadoras ni shutdown (REV-10/13). |

## Verificaciones de segunda revisión

- `gofmt -d` sobre Go modificado/nuevo: limpio.
- `git diff --check`: correcto; solo warnings de normalización CRLF.
- `go vet ./internal/fixture ./internal/specification/... ./internal/validationworkflow ./internal/localvalidation ./internal/planmanifest ./cmd/featureproof`: correcto.
- `go test -run '^$' ./...`: correcto.
- `go test -count=10 ./internal/fixture ./internal/specification/...`: correcto.
- `go test -count=5 ./internal/validationworkflow`: correcto.
- `go test -count=3 ./cmd/featureproof -run 'TestProductFixtureLifecycleParityAcrossAllSurfaces|TestProductFixtureRetryAndCleanupResolveRotatedCredentials'`: correcto.
- `CGO_ENABLED=1 CC=gcc go test -race -count=1 ./internal/fixture ./internal/specification/... ./internal/validationworkflow ./cmd/featureproof` con UCRT64: correcto.
- Coverage: `internal/fixture` 85,5 %; `internal/validationworkflow` 85,7 %.
- Cross-build `go test -c` para Linux/amd64 y Darwin/amd64 de fixture, validationworkflow y cmd/featureproof: correcto.
- `go test -count=1 ./...`: correcto en todos los paquetes; el caso de symlink se omite únicamente cuando Windows devuelve privilegio no disponible.
- Sin artefactos `.cover`, `.test`, `.out` o `.tmp` dentro del repositorio.

## Decisión de segunda revisión

La feature continúa requiriendo remediación y otra revisión independiente. No debe pasar a `done`.

---

# Tercera revisión independiente

## Veredicto de tercera revisión

`rejected`

La tercera remediación cierra el parsing no finito, el rechazo de selectors sensibles y parte de los claims. Los gates automáticos pasan, pero la ejecución productiva aún ensancha headers respecto del contrato, evalúa policy sobre un request incompleto, comparte un budget efímero entre runs y no valida semánticamente el audit de fixtures al verificar el package offline. Además, la matriz obligatoria de cancelación/crash/lease sigue incompleta.

## Revalidación explícita de hallazgos anteriores

| Hallazgo | Estado en tercera revisión | Evidencia independiente |
|---|---|---|
| REV-4 | parcial | El sello de `BoundSet` y los digests de captures cubren sus campos. La autenticidad semántica del snapshot sigue abierta: se pueden fabricar attempts/terminales con valores válidos, recalcular el digest público y superar `ValidateState` (REV-21). |
| REV-5 | parcial | El token único bloquea invocaciones same-owner/different-owner y el reload distingue terminal. No hay renovación ni prueba de lease expiry durante I/O; otra invocación puede adquirir tras expiry mientras el primer executor sigue activo (REV-20). |
| REV-6 | parcial | Existen terminales por budget/deadline/cancel/denial. La cancelación durante policy/identity/HTTP/persistencia usa el contexto ya cancelado para guardar/eventar y no tiene matriz productiva por frontera (REV-23). |
| REV-8 | cerrado | `null` deja de ser capture/reference type, la unión interna es exclusiva y float/json.Number NaN, ±Inf y overflow se rechazan. |
| REV-9 | abierto | Siguen faltando lease expiry durante I/O, conflict/terminal interleaving determinista, repeated runs/restart budget, cancelación en cada frontera, resellado semántico de state/package y matriz completa de canarios. |
| REV-10 | parcial | Same-owner, different-owner y `AlreadyTerminal` están corregidos. Lease expiry durante un executor bloqueado aún permite una segunda llamada con el mismo key; no existe el test exigido (REV-20). |
| REV-11 | abierto | El binder exige garantía del descriptor, pero el adapter inyecta el header globalmente incluso en operaciones que solo son idempotentes por semántica y cuyo contrato no declara ese header (REV-18). |
| REV-12 | abierto | Se añadió `ProductRevalidator`, pero policy/budget no reciben el request final y el `harness.Run` de budget tiene scope incorrecto (REV-19). |
| REV-13 | cerrado | `FiniteScopes.Parent` deriva del shutdown scope entregado por composición y el código no crea background work. |
| REV-14 | abierto | El digest es recalculable y las invariantes permiten snapshots inventados coherentes en forma (REV-21). |
| REV-15 | cerrado | La composición convierte cada capture path a selector persistido y rechaza `Policy.OverlapsSensitive` antes de crear el lifecycle. |
| REV-16 | parcial | `fixtures.json` entra en el inventario, pero el verificador solo compara dos copias y hashes; no valida estados/audit ni actualiza los reports (REV-22). |
| REV-17 | cerrado | `validFixtureJSON` comprueba finitud también para `json.Number`; la matriz incluye NaN, ±Inf y overflow. |

## Nuevos hallazgos accionables

## REV-18 — El header de idempotencia contractual se inyecta también en operaciones que no lo permiten

- Severidad: blocker
- Requirement o regla: R2, R10; T4, T7; diseño, líneas 75-80 y 138-147
- Archivo y línea: `internal/fixture/http_adapter.go:72-74`; `cmd/featureproof/workflow.go:637-644`; contrato productivo de test en `cmd/featureproof/workflow_identity_test.go:287-288`
- Evidencia: `ContractIdempotencyPolicy` distingue correctamente una operación POST con parámetro `Idempotency-Key` de una DELETE idempotente por semántica HTTP. Esa decisión no llega a `BoundOperation` ni al request. `HTTPAdapter` recibe un único `IdempotencyHeader` global derivado del manifest y lo añade a **todas** las llamadas. En la vertical productiva, POST `/fixtures` declara el header, DELETE `/fixtures/{id}` no lo declara, pero ambos lo reciben porque el manifest lo permite.
- Impacto: la allowlist runtime vuelve a ampliar la forma de una operación contractual exacta. Un servidor estricto puede rechazar cleanup y FeatureProof envía un header fuera del contrato aprobado.
- Corrección requerida: ligar ubicación/nombre de key por `BoundOperation`; inyectarla solo cuando esa operación acredita esa ubicación. Para semántica HTTP/declarativa sin header, no enviarlo. Añadir un spy que compruebe presencia en POST y ausencia en DELETE.

## REV-19 — Policy y budget productivos no están ligados al request ni al run ejecutado

- Severidad: blocker
- Requirement o regla: R2, R4, R9, R10, R13; ARCH-005; T5, T7, T9, T12
- Archivo y línea: `internal/fixture/lifecycle.go:117-140`; `internal/fixture/product.go:88-121`; `cmd/featureproof/workflow.go:624-629`
- Evidencia: create llama `ProductRevalidator` antes de `ResolveInput`; el guard construye un `ToolCall` que solo contiene method y path template, sin path expandido, query, body ni identity binding. Por ello policy nunca evalúa el request final que cruza red. Además, composición crea una sola vez `harness.Run` con ID `environment_id + ":fixtures"` y comparte su consumo con todos los workflow handles atendidos por ese service. Dos runs consumen el mismo budget; un restart lo reinicia. `MaxRetries` se configura pero `ProductRevalidator` solo llama `ReserveToolCall`, nunca `ReserveRetry`/`ReserveRetryToolCall`.
- Impacto: policy puede autorizar el template aunque deniegue el input final; ejecuciones independientes interfieren entre sí y recovery recupera presupuesto consumido. Los límites no describen el run real ni son durables.
- Corrección requerida: revalidar y evaluar la llamada totalmente resuelta inmediatamente antes de I/O, incluida identity/binding; mantener budget por handle con consumo durable y reservar call+retry atómicamente según ordinal. Probar policy sobre path/body resueltos, tres runs en el mismo service y restart entre retries.

## REV-20 — Lease expiry durante I/O puede producir dos llamadas concurrentes

- Severidad: blocker
- Requirement o regla: R8, R10, R13; T9, T10, T14
- Archivo y línea: `internal/fixture/lifecycle.go:281-287`, `:329-383`; `internal/fixture/remediation_test.go:157-194`
- Evidencia: ownership se revalida una vez antes de `executor.Execute`, pero no se renueva durante la llamada. Al expirar el claim, otro owner puede adquirirlo y cruzar red mientras el primer executor sigue bloqueado. El primer owner solo descubrirá el CAS perdido después del efecto. La key estable puede permitir deduplicación del servidor, pero el requisito y la task exigen que el lifecycle no produzca efectos duplicados y la suite no prueba esta intercalación. El test de handoff expira un claim editado cuando ningún executor está activo.
- Impacto: dos DELETE pueden estar simultáneamente en vuelo para una fase lógica; adapters/servidores que prometen semántica idempotente, pero no deduplicación por key, pueden observar dos efectos.
- Corrección requerida: asegurar que el lease cubra el máximo timeout hijo y no pueda expirar con I/O activo, o renovar ownership de manera owned y terminable; validar token en la transición terminal. Añadir barrera con owner A dentro del executor, avance de reloj/expiry y owner B, tanto same-owner como different-owner, bajo race.

## REV-21 — El snapshot durable puede resellarse con una historia operacional inventada

- Severidad: blocker
- Requirement o regla: R7, R11, R12, R13; ARCH-007; T10
- Archivo y línea: `internal/fixture/integrity.go:72-154`; `internal/fixture/store.go:102-122`
- Evidencia: `SnapshotDigest` es un SHA-256 público del propio snapshot. Cualquier modificación puede recalcularlo mediante `SealState`. `ValidateState` permite añadir un cleanup attempt terminal `success`, el terminal correspondiente y `ReportConfirmed=true` sin una transición/claim/event auténticos. Tampoco valida categoría cerrada, unicidad u orden de attempts no terminales; una categoría arbitraria puede entrar en `CleanupReport.Audit`. Las pruebas alteran campos sin resellar, por lo que no ejercitan tampering con valores válidos y digest actualizado.
- Impacto: recovery puede omitir DELETE, fabricar `cleanup_succeeded` o publicar texto no allowlisted desde state manipulado, todo con un snapshot cuyo digest coincide.
- Corrección requerida: autenticar/encadenar transiciones con autoridad no reescribible por el snapshot, o reconstruir desde un journal verificable; imponer invariantes de máquina, ordinales únicos/monótonos y categorías cerradas. Probar snapshots resellados que fabrican success, eliminan intent, duplican attempts y añaden categoría canario.

## REV-22 — El package offline acepta un audit de fixtures semánticamente falso si se resella

- Severidad: blocker
- Requirement o regla: R11, R12; T11, T12
- Archivo y línea: `internal/localvalidation/fixture_package.go:12-42`; `internal/localvalidation/package_reader.go:133-143`
- Evidencia: `ReadPackage` solo exige que `package.json.fixtures` sea `DeepEqual` a `fixtures.json` y que los bytes coincidan con el manifest. No valida enums, binding digests, relación intent/attempt/terminal, status agregado ni categorías allowlisted. Un consumidor puede cambiar ambas copias a `cleanup: succeeded`, inventar audit coherente en forma y regenerar el manifest; el verifier lo acepta. El test solo modifica `fixtures.json` sin actualizar la otra copia/manifest. `AttachFixtureAudit` tampoco actualiza `report.json` o `report.md`, pese al requirement de sección operacional en package/report.
- Impacto: el paquete verificable no demuestra el resultado de cleanup y el reporte humano/machine report puede contradecir el nuevo audit.
- Corrección requerida: definir y validar schema/invariantes cerradas de `FixtureSummary` y audit durante lectura offline, ligarlo al run/binding, y publicar la sección en reports antes del sello final. Añadir tampering que actualice las dos copias y regenere hashes.

## REV-23 — Falta el audit de captures y la cancelación no confirma evento junto con estado

- Severidad: high
- Requirement o regla: R5, R7, R9, R12, R13; T6, T9, T12, T14; diseño, líneas 149-161 y 317-336
- Archivo y línea: `internal/fixture/lifecycle.go:174-184`, `:230-239`, `:497-510`; `internal/fixture/product.go:130-152`
- Evidencia: después de confirmar captures, el lifecycle guarda solo `fixture_create_terminal`; no emite el `capture_confirmed` diseñado ni publica `CaptureMetadata` (el tipo no tiene usos). `save` confirma state antes de `Observer.Record`; si journal falla o el contexto se cancela, el estado queda avanzado y redelivery salta la transición sin reparar el evento ausente. Las terminales de deadline/cancel usan el contexto ya vencido; `FileObserver` las rechaza inmediatamente. No hay pruebas de cancelación durante policy, credential resolution, HTTP, redacción, state save y journal, ni de reconstrucción del prefijo auditado.
- Impacto: state/package pueden afirmar captures o terminales sin el journal ordenado requerido para auditoría. Cancelación deja huecos permanentes y la matriz de canarios no recorre records de capture porque no existen.
- Corrección requerida: persistir records allowlisted de capture name/type/bytes/digest y hacer recuperable/confirmable la relación state-event. Usar un scope de finalización finito para terminales tras cancelación y probar cada frontera con stores/observer productivos.

## REV-24 — Quedan artefactos temporales y una función modificada sin comentario sistémico

- Severidad: low
- Requirement o regla: T14; convención aprobada de documentación
- Archivo y línea: `fixture_cov`; `workflow_cov`; `internal/localvalidation/package_reader_test.go:1333`
- Evidencia: el status actual contiene dos coverage profiles sin trackear en la raíz (57.188 y 96.740 bytes), aunque `implementation.md` declara que no existen temporales. `TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors`, modificado en este diff, sigue sin comentario sistémico.
- Impacto: el árbol no está limpio y la auditoría de comentarios requerida por T14 no está completa.
- Corrección requerida: retirar los artefactos antes del commit y documentar la responsabilidad/invariante del test modificado.

## Trazabilidad de tercera revisión

| Requirement | Resultado |
|---|---|
| R1 | Pass: v3 estricta, compatibilidad v1/v2, unión/null y JSON finito cubiertos. |
| R2 | Fail: binding exacto pasa, pero header y policy final amplían/no observan la operación efectiva (REV-18/19). |
| R3 | Pass: grafo, referencias, orden y deep-copy cubiertos. |
| R4 | Fail: bloqueo pasa; policy/budget no se aplican al input final ni por run (REV-19). |
| R5 | Fail: selector sensible se rechaza; falta confirmación/audit de metadata de captures (REV-23). |
| R6 | Pass parcial: late identity y rotación pasan; faltan cancelaciones en resolution/transport. |
| R7 | Fail: intent precede I/O y crash evita replay; snapshot resellado y state/event gap permanecen (REV-21/23). |
| R8 | Fail: orden nominal pasa; lease expiry durante I/O no garantiza un solo efecto (REV-20). |
| R9 | Fail: scope finito existe; budget tiene scope de service/restart incorrecto y cancelación por frontera no está demostrada (REV-19/23). |
| R10 | Fail: key estable existe; ubicación por operación, retry budget durable y lease in-flight fallan (REV-18/19/20). |
| R11 | Fail: primary permanece separado; state y package pueden fabricar cleanup success (REV-21/22). |
| R12 | Fail: paridad nominal pasa; audit de capture/package y matriz recursiva de canarios no (REV-22/23). |
| R13 | Fail: race detector pasa, pero faltan lease in-flight, budget por owner/run y cancelación completa (REV-19/20/23). |

## Verificaciones de tercera revisión

- `gofmt -d` sobre todo Go modificado/nuevo: limpio.
- `git diff --check 1daa391`: correcto; solo warnings CRLF del checkout.
- `go vet ./internal/fixture ./internal/specification/... ./internal/validationworkflow ./internal/localvalidation ./internal/planmanifest ./cmd/featureproof`: correcto.
- `go test -run '^$' ./...`: correcto.
- Claims/crash/terminal/policy focales: `go test -count=20 ./internal/fixture -run ...`: correcto.
- Specification v1/v2/v3: `go test -count=10 ./internal/specification/... -run 'TestFixture|TestV3|TestV1V2'`: correcto.
- Package fixture: `go test -count=10 ./internal/localvalidation -run TestFixtureAuditIsSealedAndOfflineVerified`: correcto.
- Workflow fixture: `go test -count=10 ./internal/validationworkflow -run TestFixture`: correcto.
- Vertical productiva: `go test -count=5 ./cmd/featureproof -run 'TestProductFixtureLifecycleParityAcrossAllSurfaces|TestProductFixtureRetryAndCleanupResolveRotatedCredentials'`: correcto.
- Race con UCRT64/CGO sobre fixture, specification, localvalidation, validationworkflow y cmd: correcto.
- Coverage: fixture 85,0 %, validationworkflow 85,4 %, localvalidation 81,5 %.
- Cross-build compile-only Linux/amd64 y Darwin/amd64 para fixture, validationworkflow y cmd: correcto.
- `go test -count=1 ./...`: correcto en todos los paquetes.

## Alcance, secretos y cierre

No encontré implementación de escenarios/acciones/compensaciones de feature 27 ni fugas directas de credenciales en las verticales ejecutadas. La matriz actual solo busca el canario de credencial en archivos productivos; no cubre keys de idempotencia, categorías arbitrarias reselladas, captures sensibles en todas las superficies ni errores externos en cada checkpoint. La feature requiere otra remediación y revisión independiente; no debe pasar a `done`.

---

# Cuarta revisión independiente

## Veredicto de cuarta revisión

`rejected`

La cuarta remediación cierra el header contractual por operación, autentica el state con una key consumer-owned, protege el lease durante el I/O productivo y recupera el journal mediante outbox. Sin embargo, los budgets todavía no limitan cada efecto ni sobreviven al restart de la autoridad productiva, los límites obligatorios siguen hardcoded en composición y el package offline acepta un audit de fixtures semánticamente contradictorio después de actualizar las dos copias, ambos reports y el manifest. R9, R10, R11, R12 y R13 no están satisfechos.

## Revalidación de REV-18..REV-24 y pendientes previos

| Hallazgo | Estado en cuarta revisión | Evidencia independiente |
|---|---|---|
| REV-4 / REV-14 / REV-21 | cerrado | `FileStore` autentica el snapshot completo con HMAC-SHA256 y una key aleatoria creada con `O_EXCL`; el digest recalculable ya no concede autoridad. `ValidateState` cierra categorías, attempts, terminales, reservations y outbox, y los tests de tampering ejercitan snapshots resellados sin la key. |
| REV-5 / REV-10 / REV-20 | cerrado | El claim usa token único aun con el mismo owner, se revalida por reload justo antes de I/O, su expiry cubre el deadline del scope más `LeaseTTL`, y `activeCleanupClaims` impide handoff en proceso mientras el executor sigue activo. La barrera de test cubre same-owner, different-owner y reloj avanzado; la carrera pasa bajo `-race`. |
| REV-6 | parcial | Los terminales y el outbox sobreviven a fallos/cancelación, pero la autoridad de budget que precede a I/O no es durable ni respeta el call cap en retry (REV-25). |
| REV-8 / REV-17 | cerrado | La unión v3 rechaza null ambiguo y números no finitos, incluidos `json.Number`; v1/v2 mantienen su interpretación. |
| REV-9 | abierto | Las matrices nominales pasan, pero el probe de budget cruza el límite exacto y la composición no permite suministrar los límites obligatorios (REV-25/27). |
| REV-11 / REV-18 | cerrado | `IdempotencyHeader` forma parte de `BoundOperation` y del sello; POST recibe el header contractual exacto y DELETE semánticamente idempotente no recibe uno ausente de su contrato. |
| REV-12 / REV-19 | parcial | Policy recibe path expandido, query, body, identity y binding, y los runs en memoria se separan por handle. `FixtureState.Reservations` no restaura `harness.Run.Consumed`, el call cap se reinicia y el retry puede cruzarlo (REV-25). |
| REV-13 | cerrado | El cleanup usa un scope propio finito derivado del shutdown scope y no crea goroutines/background work. |
| REV-15 | cerrado | La composición convierte todo capture path en selector y rechaza solapamientos sensibles antes del lifecycle. |
| REV-16 / REV-22 | abierto | Las cuatro copias se actualizan y sus hashes se regeneran, pero `ValidateSummary` no valida la concordancia entre status agregado, fixtures y terminales. El probe rehasheado es aceptado (REV-26). |
| REV-23 | cerrado | `capture_confirmed` publica solo name/type/bytes/digest; `PendingEvents` precede al append y `FileObserver` deduplica por event ID. Un fallo entre append y confirmación converge por redelivery. |
| REV-24 | cerrado | No quedan perfiles ni temporales en el repositorio y las entidades/funciones nuevas o modificadas, incluidos helpers de test, tienen comentario sistémico. |

## Nuevos hallazgos accionables

## REV-25 — El call budget de cleanup se cruza en retry y el consumo productivo se reinicia

- Severidad: blocker
- Requirement o regla: R9, R10, R13; T9, T10, T14; `requirements.md:342-360`, `:364-377`
- Archivo y línea: `internal/fixture/lifecycle.go:248-259`, `:277-345`, `:548-558`; `internal/fixture/product.go:89-164`
- Evidencia: `calls >= scope.MaxCalls` solo se comprueba antes de entrar al fixture. Dentro del loop de retries, cada `executor.Execute` incrementa `calls`, pero no vuelve a comprobar el límite. Un probe independiente compuesto con `MaxCalls=1`, `MaxRetries=1`, primer DELETE transitorio y segundo DELETE exitoso observó **dos** llamadas de cleanup y pasó: `TestFourthReviewProbeCleanupRetryCrossesMaxCalls`. Además, `ProductRevalidator` reconstruye `harness.Run` exclusivamente en memoria; `FixtureState.Reservations` solo contiene booleanos y nunca hidrata `Consumed.ToolCalls/Retries`, por lo que una nueva composición recupera todo el budget tras restart.
- Impacto: un retry cruza autoridad explícita antes de I/O y restart/redelivery puede ampliar otra vez el número total de efectos autorizados.
- Corrección requerida: reservar call y retry de forma atómica y durable inmediatamente antes de cada intento, incluido cada retry; comprobar el límite dentro del loop y reconstruir consumo por run desde reservations validadas. Añadir tests con `MaxCalls=1`, restart entre attempts y concurrencia de owners.

## REV-26 — El package rehasheado acepta un cleanup agregado que contradice sus terminales

- Severidad: blocker
- Requirement o regla: R11, R12; T11, T12, T14; `requirements.md:384-401`, `:403-424`
- Archivo y línea: `internal/fixture/integrity.go:211-261`; `internal/localvalidation/fixture_package.go:13-72`; `internal/localvalidation/package_reader.go:133-154`, `:234-253`
- Evidencia: `ValidateSummary` solo exige concordancia de terminales cuando el status es `CleanupSucceeded`. No exige que `CleanupFailed` tenga cero éxitos, que `Partial` tenga éxito e incidencia, que `NotRequired` carezca de intents, ni relaciona `FixtureSummary.Fixtures`, capture names o binding digests con el audit. Un probe independiente creó mediante `AttachFixtureAudit` un summary con `CleanupFailed`, un intent y un único terminal `CategorySuccess`, además de un fixture ajeno y digests elegidos por el probe. El writer actualizó `package.json`, `fixtures.json`, `report.json`, `report.md` y manifest; `ReadPackage` lo aceptó (`TestFourthReviewProbeAcceptsContradictoryResealedFixtureAudit`: PASS).
- Impacto: la verificación offline puede publicar un cleanup falso o contradictorio aunque todas las copias y hashes sean coherentes. El package no demuestra la relación requerida entre intents, attempts, terminales y status.
- Corrección requerida: validar todos los agregados en ambas direcciones, exigir correspondencia exacta fixture/result/audit/binding y hacer que el writer rechace summaries inválidos antes de re-sellar. Añadir una matriz de rehash que altere cada status, fixture ID, create status, capture name y binding digest manteniendo copias/report/manifest concordantes.

## REV-27 — La composición productiva inventa límites que la spec exige recibir de configuración confiable

- Severidad: high
- Requirement o regla: R9, R10; decisión cerrada de `requirements.md:470-474`; T9, T12
- Archivo y línea: `cmd/featureproof/workflow.go:615-625`
- Evidencia: `MaxToolCalls=len(fixtures)*4`, `MaxRetries=len(fixtures)*2`, cleanup `Timeout=10s`, `MaxCalls=len(fixtures)*2`, `MaxRetries=1`, create retries `1`, backoff `100ms` y lease `30s` se construyen con constantes locales. `workflowConfig` no aporta estas dimensiones y no hay validación de configuración obligatoria. El número de fixtures, controlado por la specification, modifica parte de la autoridad productiva.
- Impacto: operadores no pueden fijar ni auditar los budgets/deadlines aprobados y dos despliegues con la misma configuración declarada reciben autoridad implícita distinta según el documento.
- Corrección requerida: incorporar un bloque trusted config explícito para todos los límites, rechazar dimensiones ausentes/no positivas/incoherentes y pasarlas sin derivarlas de la spec. Probar composición fail-closed y límites exactos en create, retry y cleanup.

## Trazabilidad de cuarta revisión

| Requirement | Resultado |
|---|---|
| R1 | Pass: v3 estricta, compatibilidad v1/v2, null/uniones y finitud cubiertos. |
| R2 | Pass: target, operación, catálogo y header contractual exactos; policy observa el request resuelto. |
| R3 | Pass: grafo completo, referencias, orden estable e aislamiento por deep-copy. |
| R4 | Pass: preparación secuencial bloquea dependientes, ramas posteriores y primary ante fallo. |
| R5 | Pass: captures tipadas/acotadas/saneadas, selector sensible fail-closed y metadata durable sin valor. |
| R6 | Pass: identity explícita y late resolution; la matriz productiva de rotación no persiste credenciales. |
| R7 | Pass: intent durable precede I/O; recovery no repite create y estado manipulado sin MAC falla cerrado. |
| R8 | Pass: cleanup inverso continúa tras incidencias y claims evitan handoff durante el I/O controlado. |
| R9 | Fail: el retry cruza `MaxCalls` y los límites obligatorios no proceden de configuración (REV-25/27). |
| R10 | Fail: header/key/revalidación son correctos, pero call/retry budget no es exacto ni durable (REV-25/27). |
| R11 | Fail: primary queda separado, pero package/report aceptan un cleanup agregado contradictorio (REV-26). |
| R12 | Fail: cuatro superficies comparten el workflow y los canarios nominales no fugan; la auditoría offline puede reescribirse semánticamente (REV-26). |
| R13 | Fail: claims/race/outbox convergen, pero budget restart/retry todavía permite efectos fuera de autoridad (REV-25). |

## Verificaciones de cuarta revisión

- `gofmt -l` sobre Go modificado/nuevo: limpio.
- `git diff --check 1daa391`: correcto; solo warnings CRLF del checkout.
- `go vet ./internal/fixture ./internal/localvalidation ./internal/validationworkflow ./internal/specification/... ./cmd/featureproof`: correcto.
- `go test ./... -run '^$'`: correcto.
- Focales repetidas: `go test ./internal/fixture ./internal/localvalidation ./internal/validationworkflow ./internal/specification/... ./cmd/featureproof -count=3`: correcto.
- Claims/outbox/tampering/budget/package/product vertical: fixture `-count=20`, package `-count=10` y cuatro superficies `-count=5`: correcto para la suite existente.
- Probe de presupuesto con overlay temporal, sin cambiar el repositorio: `TestFourthReviewProbeCleanupRetryCrossesMaxCalls`: PASS y reprodujo 2 llamadas con `MaxCalls=1` (REV-25).
- Probe de package con overlay temporal: `TestFourthReviewProbeAcceptsContradictoryResealedFixtureAudit`: PASS; writer y reader aceptaron `failed` con único terminal `success` tras re-sellar todas las copias (REV-26).
- Race: la primera invocación con ruta absoluta a GCC falló al enlazar porque el runtime UCRT64 no estaba en `PATH`; repetida con `C:\\msys64\\ucrt64\\bin` en `PATH`, `CGO_ENABLED=1 CC=gcc go test -race ./internal/fixture ./internal/localvalidation ./internal/validationworkflow ./cmd/featureproof`: correcto.
- Coverage focal: fixture 85,1 %, localvalidation 81,4 %, validationworkflow 85,4 %, cmd 77,1 %.
- Cross-build del binario productivo: Windows/amd64, Linux/amd64 y Darwin/amd64 correctos.
- `go test ./... -count=1`: correcto en todos los paquetes.
- Revisión de alcance: no hay implementación de escenarios/acciones/compensaciones de feature 27. No encontré credenciales, valores capturados, idempotency keys ni errores externos crudos en vistas, journal, package o superficies; la key HMAC es aleatoria, privada y no hardcoded.
- Revisión de documentación/árbol: comentarios sistémicos presentes en entidades, funciones y helpers nuevos/modificados; sin temporales de coverage/build dentro del repositorio.

## Decisión de cuarta revisión

La feature requiere remediar REV-25, REV-26 y REV-27 y pasar otra revisión independiente. No debe marcarse `done`.

---

# Quinta revisión independiente

## Veredicto de quinta revisión

`rejected`

REV-25 queda corregido para `MaxCalls=1`, retry, restart, redelivery, separación entre runs y owners concurrentes. REV-27 también queda corregido: `fixture_runtime` es obligatorio cuando hay fixtures, rechaza cero/negativos/incoherencias y sus valores llegan sin derivarse del documento al lifecycle productivo. REV-26 permanece abierto: el writer y reader aceptan un package re-sellado cuya preparación contradice su terminal de create y cuyo `report.md` contiene una segunda sección contradictoria. Además, el nuevo rechazo de budget antes del primer I/O deja elegible para cleanup un fixture que nunca cruzó red, y la evidencia de coverage no alcanza ni demuestra el 85 % requerido para todo el código modificado.

## Revalidación de REV-25, REV-26 y REV-27

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-25 | cerrado en su alcance original | `reservationCount` se consulta dentro del loop antes de cada DELETE y las reservas autenticadas sobreviven restart. Un probe propio con `MaxCalls=1`, fallo transitorio, redelivery con otro owner y segundo run independiente observó un solo DELETE, cero efectos en redelivery y budget aislado por run. La carrera de claims repetida bajo `-race` también pasa. Se detectó una regresión distinta en el rechazo inicial pre-I/O (REV-28). |
| REV-26 | abierto | La matriz añadida rechaza cambios aislados de aggregate, fixture ID, create status, capture name y cleanup binding. No valida la semántica del terminal de create, la forma criptográfica/autoridad del fixture digest, los ordinals ni los valores allowlisted de evidence; tampoco exige una única sección fixture exacta en `report.md`. Dos probes re-sellados pasan (REV-29). |
| REV-27 | cerrado | `workflowFixtureRuntimeConfig.validate` rechaza ausencia, cero, negativo e incoherencias. El probe propio conservó exactamente 7/5/3 para capture/tool/cleanup calls y la composición pasa esos valores a `ProductRevalidator`, `FiniteScopes` y `Lifecycle` sin usar el número de fixtures. |

## Nuevos hallazgos accionables

## REV-28 — El rechazo de budget antes del primer I/O conserva un cleanup intent falso

- Severidad: blocker
- Requirement o regla: R7, R8, R9; T8, T9, T10; `requirements.md:300-316`
- Archivo y línea: `internal/fixture/lifecycle.go:94-104`, `:144-151`, `:567-580`
- Evidencia: `Prepare` marca `Intents[id]=true` y `Eligible[id]=true` antes de llamar `create`. Si `reserveAttempt` devuelve `CategoryBudget`, `create` retorna directamente sin pasar por `confirmPreIOCreateRejection`; por tanto ambas flags quedan verdaderas aunque ese fixture no ejecutó policy ni red. Un probe propio con dos fixtures y `MaxToolCalls=1` preparó el primero, denegó por budget el intento 1 del segundo y confirmó en state `Intents["profile"]==true` y `Eligible["profile"]==true`.
- Impacto: recovery y cleanup tratan como posiblemente creado un recurso que se sabe no cruzó I/O. Esto contradice el criterio explícito de R7 y puede publicar una incidencia `cleanup_input_unavailable` falsa o intentar cleanup si sus inputs no dependen de una captura propia.
- Corrección requerida: distinguir budget denial anterior a I/O y cerrar el intent mediante la transición durable pre-I/O; conservar elegibilidad únicamente si un intento anterior del mismo fixture ya cruzó red. Añadir matriz para initial call budget, retry budget tras I/O y fallo al persistir el cierre.

## REV-29 — El package offline aún acepta historia y Markdown contradictorios re-sellados

- Severidad: blocker
- Requirement o regla: R11, R12; T11, T12, T14; `requirements.md:384-424`
- Archivo y línea: `internal/fixture/integrity.go:240-355`; `internal/localvalidation/package_reader.go:245-253`
- Evidencia: `ValidateSummary` considera suficiente cualquier terminal de create para un `CreatePrepared`; no exige categoría de éxito, ordinal esperado ni evidence ID/digests con forma cerrada. Un probe creó `PreparationPrepared` con terminal de create `CategoryUnavailable`, attempt 99, `FixtureSetDigest="x"` y `EvidenceID="EXTERNAL-CANARY"`, mantuvo cleanup internamente agregado como success, y `AttachFixtureAudit` más `ReadPackage` lo aceptaron. Las cuatro copias y el manifest se regeneraron mediante el writer real. En un segundo probe, se añadió a `report.md` otra sección `Fixture lifecycle` con binding/preparation/cleanup contradictorios y canario, se regeneró el manifest y `ReadPackage` también la aceptó porque solo usa `strings.Contains` para buscar el marcador esperado.
- Impacto: un package offline verificado puede afirmar preparación sobre un create fallido, transportar texto no allowlisted y presentar dos resultados humanos incompatibles aunque JSON y hashes sean coherentes.
- Corrección requerida: modelar la máquina completa de create en `ValidateSummary`; exigir ordinals contiguos, terminal exitoso sin categoría para `prepared`, IDs/digests/evidence con formato seguro y correspondencia con autoridad sellada. Verificar `report.md` de manera exacta o estructurada, rechazando secciones duplicadas y sufijos no generados. Ampliar la matriz de re-sellado con mutaciones concordantes simultáneas, no solo campos aislados.

## REV-30 — La entrega no demuestra el 85 % de coverage del código nuevo o modificado

- Severidad: high
- Requirement o regla: T14; `docs/verification.md:104-111`
- Archivo y línea: `progress/fixture-api/implementation.md`, sección “Gates de cuarta remediación”
- Evidencia: el informe declara únicamente `internal/fixture` 85,3 % e `internal/validationworkflow` 85,4 %, aunque el diff también añade/modifica lógica en cmd, localvalidation, specification y otros adapters. La ejecución independiente obtiene package coverage de 77,2 % para `cmd/featureproof`, 81,4 % para `internal/localvalidation`, 63,2 % para `internal/specification` y 78,3 % para `internal/specification/yaml`. Una intersección statement-weighted entre perfiles nativos y líneas productivas añadidas/modificadas desde `1daa391` da 77,6 % (1459/1880); entre los focos bajos están `workflow.go` 75,0 %, `fixture_package.go` 76,0 %, `specification/validate.go` 19,5 % y `yaml/loader.go` 66,3 %.
- Impacto: faltan ramas relevantes justo en composición, package y validación; los dos defectos reproducidos no estaban cubiertos por la suite existente. La evidencia declarada no satisface el criterio de cierre.
- Corrección requerida: añadir tests conductuales para las ramas faltantes y presentar medición reproducible de todo el código nuevo/modificado >=85 %, incluida la cobertura ejercida por integraciones dependientes mediante `-coverpkg`/merge cuando corresponda; no basta citar dos paquetes.

## Regresión R1–R13

| Requirement | Resultado |
|---|---|
| R1 | Pass funcional: v3 estricta, v1/v2 compatibles, null/uniones y números no finitos se rechazan; coverage del código de specification queda pendiente en REV-30. |
| R2 | Pass: contrato/target exactos, POST usa el header contractual y DELETE no lo recibe; policy observa el request final. |
| R3 | Pass: grafo completo, referencias, orden determinista y deep-copy se mantienen. |
| R4 | Pass: preparación secuencial bloquea dependientes y primary ante fallo. |
| R5 | Pass: captura tipada/acotada/saneada, selector sensible y outbox de metadata se mantienen. |
| R6 | Pass: identidad explícita, resolución tardía y canarios de credencial no cruzan superficies persistentes. |
| R7 | Fail: budget denial del primer intento conserva intent/eligibility pese a cero I/O (REV-28). |
| R8 | Fail parcial: orden inverso/continuación/claims pasan, pero puede aparecer una obligación de cleanup fabricada por REV-28. |
| R9 | Pass para límites/retry/restart y scope finito; REV-28 afecta la clasificación pre-I/O, no el cap exacto. |
| R10 | Pass: garantía/header/key/retry estable, budget durable por run y revalidación por intento. |
| R11 | Fail: primary sigue separado, pero el package acepta preparación y reporte humano contradictorios (REV-29). |
| R12 | Fail: paridad y canarios nominales pasan; el verifier acepta evidence/Markdown canario re-sellado (REV-29). |
| R13 | Pass: CAS/lease, owners, redelivery, outbox y full race convergen sin efectos duplicados en los casos ejercitados. |

## Probes independientes

- `TestFifthProbeMaxCallsRetryRestartAndRunIsolation`: PASS; un solo DELETE con `MaxCalls=1`, redelivery sin repetición y run independiente sin compartir budget.
- `TestFifthProbeInitialBudgetDenialLeavesCleanupIntent`: PASS; reprodujo intent y eligibility verdaderos para el segundo fixture rechazado antes de I/O (REV-28).
- `TestFifthProbeFixtureRuntimeClosedMatrix`: PASS; nil, cero, negativo e incoherencia rechazados y valores válidos no ensanchados.
- `TestFifthProbeResealedContradictorySummary`: PASS; writer/reader aceptaron prepared respaldado por create unavailable/attempt 99 y evidence canario (REV-29).
- `TestFifthProbeResealedContradictoryMarkdown`: PASS; reader aceptó segunda sección fixture contradictoria tras regenerar manifest (REV-29).

## Verificaciones de quinta revisión

- `gofmt -l` sobre Go modificado/nuevo: limpio.
- `git diff --check 1daa391`: correcto; únicamente warnings CRLF del checkout.
- `go vet ./...`: correcto.
- `go test ./... -run '^$'`: correcto.
- Focales REV-25/claims/outbox/MAC/product policy x20, package x20 y config/paridad/rotación x10: correctos para la suite existente.
- `CGO_ENABLED=1 CC=gcc go test -race ./... -count=1` con UCRT64 en `PATH`: correcto en todos los paquetes.
- Coverage independiente: fixture 85,3 %, validationworkflow 85,4 %, localvalidation 81,4 %, cmd 77,2 %, specification 63,2 % y specification/yaml 78,3 %; el cálculo del diff productivo queda en 77,6 % (REV-30).
- Cross-build del binario productivo: Windows/amd64, Linux/amd64 y Darwin/amd64 correctos.
- `go test ./... -count=1`: correcto en todos los paquetes.
- Headers/policy/lease/outbox/MAC/paridad/canarios: focales repetidos correctos; los probes adversariales de package encuentran REV-29.
- Comentarios sistémicos: no se detectaron entidades, funciones ni helpers nuevos/modificados sin comentario.
- Alcance/árbol: no hay lógica de feature 27 ni temporales coverage/build dentro del repositorio; los probes y perfiles de revisión se mantuvieron fuera del workspace.

## Decisión de quinta revisión

La feature requiere remediar REV-28, REV-29 y REV-30 y pasar otra revisión independiente. No debe marcarse `done`.

## Sexta revisión independiente

### Veredicto

**rejected**

REV-28 queda cerrado: la denegación inicial de budget cierra durablemente el intent conocido como pre-I/O, el retry denegado después de un efecto conserva la obligación de cleanup y un fallo al persistir el cierre mantiene el snapshot conservador. REV-29 queda cerrado solo para sus contradicciones estructurales explícitas (`create=unavailable`, ordinal 99 y Markdown duplicado), pero la autenticidad del audit offline sigue abierta como REV-31. REV-30 permanece abierto: el script nuevo imprime 85,7 %, pero omite código Unix productivo del diff y el agregado completo conservador queda en 84,9792 %.

### Revalidación REV-28..REV-30

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-28 | cerrado | `TestCreateBudgetDenialClosesOnlyKnownPreIOIntents` pasó 20 veces. La inspección de `create` confirma cierre mediante `confirmPreIOCreateRejection` cuando ningún attempt durable cruzó red, conservación de intent/eligibility tras retry post-I/O y retorno `internal` si el CAS del cierre falla. |
| REV-29 | parcial; sustituido por REV-31 para autenticidad | Probes separados rechazaron `create unavailable`, `attempt=99` y una segunda sección `## Fixture lifecycle`; ya no existe el bypass estructural de la quinta revisión. Un probe distinto demostró que cuatro canarios con forma válida aún se aceptan tras reescribir todas las copias y el manifest (REV-31). |
| REV-30 | abierto | `python scripts/feature26_coverage.py` termina en cero e informa `1635/1908 = 85,7 %`, pero `changed_lines()` incluye `store_lock_unix.go` y `store_replace_unix.go` mientras `measure()` solo suma bloques presentes en el profile Windows y no comprueba archivos faltantes. Ambos archivos desaparecen del desglose. Una compilación aislada exacta de esos mismos fuentes contabiliza 16 statements; incluidos como no cubiertos, el resultado es `1635/1924 = 84,9792 %`, inferior a 85 %. |

## REV-31 — El audit offline acepta canarios re-sellados con forma válida

- Severidad: **blocker**.
- Requisitos/tareas: R11, R12; T11, T12, T14.
- Evidencia: un overlay de revisión fuera del workspace creó cuatro packages válidos, cambió por separado `FixtureSetDigest`, el `EvidenceID` del terminal create, el digest de capture y ambos bindings de cleanup por valores hex SHA-256 bien formados, actualizó `package.json`, `fixtures.json`, `report.json`, `report.md` y regeneró `manifest.json`. `ReadPackage` aceptó los cuatro. El mismo probe rechazó correctamente `create unavailable`, ordinal 99 y Markdown duplicado, por lo que el resultado no está enmascarado por esos errores.
- Causa: `ValidateSummary` verifica forma y coherencia interna, pero para estos campos acepta cualquier hex de 64 caracteres; `ReadPackage` comprueba igualdad entre copias y hashes del manifest regenerable sin ligar el audit de fixtures a una autoridad autenticada que el re-sellador no pueda sustituir.
- Riesgo: un package manipulado puede atribuir otro fixture set, evidence, capture o cleanup binding y seguir apareciendo como verificable offline. La matriz de canarios productiva no cubre este adversario de forma válida.
- Corrección requerida: autenticar el digest canónico del audit completo desde una autoridad no regenerable por quien modifica el package y verificarla offline antes de aceptar las copias; añadir mutaciones independientes con digests hex válidos para cada campo, además de las negativas estructurales actuales.

## REV-32 — El gate de coverage omite statements productivos condicionados por plataforma

- Severidad: **blocker**.
- Requisitos/tareas: R1–R13; T14.
- Evidencia: las líneas 30–46 de `scripts/feature26_coverage.py` registran ambos fuentes Unix como cambiados; las líneas 59–76 solo crean denominador para bloques presentes en el profile de la ejecución Windows. El output no contiene `internal/fixture/store_lock_unix.go` ni `internal/fixture/store_replace_unix.go`, aunque ambos son archivos productivos nuevos. Sus fuentes contienen 16 statements según un módulo temporal que compiló literalmente ambos archivos con un stub de `x/sys/unix`; ninguno puede cubrirse en el run Windows. El total conservador es `1635/(1908+16) = 84,9792 %`.
- Riesgo: el script da un pass falso para el umbral contractual y permite que cualquier archivo productivo ausente del profile desaparezca silenciosamente del denominador.
- Corrección requerida: hacer fallar el script cuando un fuente ejecutable cambiado no aparece en el profile, medir por plataforma y agregar denominadores de forma explícita, o contabilizar esos statements como no cubiertos. Después deben añadirse pruebas Unix reales suficientes para que el diff completo alcance al menos 85 % sin redondeo.

### Regresión R1–R13

| Requisito | Resultado de sexta revisión |
|---|---|
| R1 | Pass funcional: dispatch v3 estricto, compatibilidad v1/v2, uniones/null y números no finitos permanecen cubiertos. El gate cuantitativo global queda abierto por REV-32. |
| R2 | Pass: grafo, referencias, orden determinista y aislamiento por copia no muestran regresión. |
| R3 | Pass: binding de contrato, target, identity e idempotencia exacta permanece sellado y revalidado. |
| R4 | Pass: policy/budget bloquean antes de red y la denegación inicial cierra el intent; fallo de persistencia conserva recuperación. |
| R5 | Pass: captures siguen tipadas, acotadas, saneadas y confinadas al estado privado. |
| R6 | Pass: resolución tardía de identities y ausencia de secretos en el harness permanecen cubiertas. |
| R7 | Pass: intent durable precede al I/O, los creates inciertos no se repiten y REV-28 está corregido. |
| R8 | Pass: cleanup inverso y continuado para obligaciones reales, incluido `cleanup_pending` y redelivery. |
| R9 | Pass: scopes finitos, budgets/retries/backoff y restart/owners permanecen acotados. |
| R10 | Pass: retry exige idempotencia contractual exacta y conserva la key estable sin exponerla. |
| R11 | Fail parcial: primary/cleanup siguen separados e inmutables, pero el audit publicado puede re-sellarse con historia falsa válida (REV-31). |
| R12 | Fail: paridad y redacción nominal pasan, pero el verifier offline acepta canarios autenticables solo por forma (REV-31). |
| R13 | Pass: claims, lease/handoff, CAS, cancelación, recovery y race no muestran regresión. |

### Probes y matrices ejecutadas

- Probe propio `TestSixthProbeResealedValidShapeCanaries`: **FAIL esperado del producto**; `ReadPackage` aceptó los cuatro subcasos `fixture_set_digest`, `create_evidence_id`, `capture_digest` y `cleanup_binding` tras re-sell completo.
- Probes propios `TestSixthProbeResealedStructuralContradictions` y `TestSixthProbeDuplicateMarkdown`: PASS; `create unavailable`, `attempt=99` y sección Markdown duplicada fueron rechazados por separado.
- Budget: `TestCreateBudgetDenialClosesOnlyKnownPreIOIntents` y `TestCleanupBudgetReservationsSurviveRetryAndRestart` ×20 PASS; cubren denial inicial, retry post-I/O, persistencia fallida, restart y consumo durable.
- Package implementado: `TestFixtureAuditIsSealedAndOfflineVerified` ×20 PASS, aunque su canario combinado usa `EXTERNAL-CANARY` no hexadecimal y queda rechazado antes de probar autenticidad.
- Paridad/configuración productiva/rotación: verticales de las cuatro superficies y `fixture_runtime` ×10 PASS.
- Canarios/redacción: proyección fixture ×20 y journal/artifacts/superficies ×10 PASS; no se observaron secretos, valores capturados, claves de idempotencia ni errores externos crudos en las superficies nominales.

### Investigación de timeouts

No se reprodujo un timeout atribuible a feature 26. `go test ./... -count=2` pasó completo; los paquetes más lentos fueron `internal/correction` (56,422 s), `internal/localvalidation` (50,620 s), `cmd/featureproof` (37,853 s) y `internal/revisionbinding` (32,032 s), todos dentro del proceso normal. El conjunto afectado pasó ×3 y las verticales productivas ×10. `go test -race ./... -count=1` también pasó completo. El informe de implementación no identifica los dos tests históricos, por lo que la evidencia reproducible disponible no permite asociarlos a esta feature; las repeticiones actuales no muestran flake ni deadlock.

### Gates de sexta revisión

- `python scripts/feature26_coverage.py`: comando PASS, `1635/1908 = 85,7 %`; auditoría del denominador FAIL por omitir 16 statements Unix, agregado conservador `84,9792 %` (REV-32).
- `gofmt -l` sobre todo Go cambiado/nuevo: sin archivos listados.
- `git diff --check 1daa391`: PASS.
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- `go test ./... -count=2`: PASS, sin timeout.
- Paquetes afectados ×3: PASS.
- Focales budget/package ×20; composición/paridad/configuración ×10: PASS salvo el probe adversarial documentado de REV-31.
- `go test -race` de paquetes afectados y `go test -race ./... -count=1` con UCRT64: PASS, sin race ni timeout.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` y equivalente Darwin: PASS.
- Comentarios sistémicos: auditoría de símbolos nuevos/modificados, incluidas entidades, helpers y tests, sin omisiones.
- Alcance/árbol: no hay implementación de feature 27 ni temporales de build/coverage en el repositorio; `feature_list.json` permanece intacto. Los overlays, perfiles y módulos auxiliares de revisión se mantuvieron fuera del workspace.

### Decisión de sexta revisión

La feature no debe marcarse `done`. Debe cerrar REV-31 con autenticidad offline no re-sellable y REV-32 con un gate que mida todo el código productivo del diff y alcance realmente al menos 85 %, y después pasar una nueva revisión independiente.

## Séptima revisión independiente

### Veredicto

**rejected**

REV-31 solo queda cerrado frente a un atacante limitado al directorio del package. La implementación rechaza correctamente los cuatro canarios SHA válidos, firma, clave pública y audit cuando el sidecar no cambia, pero el verificador usa la clave privada como trust anchor y acepta sustituirla junto con todo el package; REV-33 concreta este bloqueo. REV-32 también permanece abierto: el gate supera 85 % aun corrigiendo el conteo, pero el conteo Unix está infravalorado y el algoritmo falla al ejecutarse en Unix porque no incorpora los fuentes Windows ausentes; REV-34 concreta el defecto reproducible.

### Revalidación REV-31/REV-32

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-31 | abierto, refinado como REV-33 | La clave privada queda físicamente fuera del directorio inventariado y el package no la contiene en el caso nominal. Sustituir solo `fixture-authority.json` falla cerrado. Sin embargo, `ReadPackage` lee `dir + ".fixture-audit.key"`, deriva de ahí la clave pública esperada y por tanto concede capacidad de firma al propio verificador. Un probe sustituyó ese sidecar, clave pública, firma, audit, las tres copias JSON, Markdown y manifest; los cuatro canarios SHA fueron aceptados. |
| REV-32 | abierto, refinado como REV-34 | `python scripts/feature26_coverage.py` informa `1666/1958 = 85,0868 %`. El compilador cuenta 15 statements en `store_lock_unix.go`, no los 14 hardcodeados; el total corregido es `1666/1959 = 85,0434 %`, aún sobre el umbral. En Unix, `measure()` añade fallback solo para Unix y marca como ausentes `store_lock_windows.go` y `store_replace_windows.go`, por lo que el comando no es multiplataforma ni demuestra todos los fuentes en ambos hosts. |

## REV-33 — El verificador posee la clave privada y acepta sustituir todo el trust anchor

- Severidad: **blocker**.
- Requisitos/tareas: R11, R12; T11, T12, T14.
- Archivos: `internal/localvalidation/fixture_package.go:92-111`; `internal/localvalidation/package_reader.go:187-205`.
- Evidencia: `signFixtureAudit` genera y guarda la clave privada cruda en `<package-dir>.fixture-audit.key`. `verifyFixtureAuditAuthority` vuelve a abrir esa clave privada para derivar la pública y decidir confianza. El probe propio `TestSeventhProbeAuthoritySubstitution` generó otro par Ed25519 y, para cada caso, sustituyó el sidecar, `fixture-authority.json`, `package.json`, `fixtures.json`, `report.json`, `report.md` y `manifest.json`. `ReadPackage` aceptó cambios independientes de `FixtureSetDigest`, create `EvidenceID`, capture digest y cleanup binding. El control `TestSeventhProbePackageOnlyAuthoritySubstitution` sí rechazó cambiar solo la autoridad pública dentro del package.
- Impacto: no existe separación entre firmante y verificador. Todo consumidor con la capacidad requerida para verificar obtiene también el material que permite re-firmar una historia falsa. El modo 0600 y la ubicación adyacente evitan inventariar el secreto en el caso nominal, pero no convierten una clave privada sustituible en un trust anchor público estable. La firma tampoco domain-separa ni liga explícitamente versión de package y `BindingSHA256`; firma únicamente `SHA256(summary)`.
- Corrección requerida: inyectar al writer una clave privada consumer-owned persistente desde un almacén protegido que el reader nunca abra; inyectar al reader una clave pública o key ID confiable fuera del package. Firmar un mensaje canónico domain-separated que incluya al menos versión, run ID, package binding y digest completo del audit. Rechazar ausencia, sustitución y rotación no autorizada del anchor, y repetir los cuatro ataques cambiando package, firma y clave pública sin acceso a la clave privada.

## REV-34 — El coverage gate no es simétrico entre Windows y Unix y subcuenta Unix

- Severidad: **high**.
- Requisitos/tareas: R1–R13; T14.
- Archivo: `scripts/feature26_coverage.py:24-37,74-87`.
- Evidencia: el mapa fijo declara 14 statements para `store_lock_unix.go`. Una compilación aislada literal del fuente con `go test -coverprofile` produce 15 para ese archivo y 1 para `store_replace_unix.go`. Corrigiendo el denominador, la cobertura es `1666/1959 = 85,0434 %`, no 85,0868 %. Además, una ejecución simulada con profile Unix hace que `measure()` lance `RuntimeError: coverage profile omitted changed production sources: internal/fixture/store_lock_windows.go, internal/fixture/store_replace_windows.go`, porque no existe fallback simétrico para Windows.
- Impacto: el umbral actualmente se alcanza, pero el gate no mide honestamente la misma totalidad del diff en todos los sistemas soportados y sus números declarados no son reproducibles desde el compilador. Un run Unix real del comando falla antes de publicar cobertura.
- Corrección requerida: obtener los bloques/statement counts por compilación reproducible para cada GOOS o mantener metadata simétrica verificada para Windows y Unix; agregar ambos lados una sola vez y hacer fallar cualquier fuente ejecutable desconocido. Ejecutar el test Unix real en CI Unix y el test Windows real en Windows, combinando perfiles o contabilizando explícitamente lo no ejecutado sin hardcodes incorrectos.

### Auditoría Ed25519

- PASS: la private key nominal está fuera del directorio inventariado; `fixture-authority.json` contiene solo digest, public key y firma; no se observó la clave en package/report/journal/evidence/superficies.
- PASS: ausencia o forma inválida de key, firma inválida, public key cambiada solo dentro del package y cualquiera de los cuatro audits SHA modificados sin cambiar el sidecar fallan cerrado.
- FAIL: el reader requiere y abre la private key, por lo que no implementa verificación asimétrica con separación de capacidades ni un trust anchor público offline.
- FAIL: sustitución concordante del sidecar privado y todas las copias públicas acepta los cuatro canarios.
- FAIL parcial: la firma autentica el summary con su run ID, pero no liga explícitamente dominio/version del package ni `BindingSHA256`; la ubicación del sidecar actúa como identidad implícita y sustituible.

### Regresión R1–R13

| Requisito | Resultado de séptima revisión |
|---|---|
| R1 | Pass funcional para v3 estricta, compatibilidad v1/v2, null/uniones y números no finitos; el gate multiplataforma queda abierto en REV-34. |
| R2 | Pass: grafo, referencias, orden y deep-copy permanecen correctos. |
| R3 | Pass: contrato, target, identity, header e idempotencia exactos permanecen ligados. |
| R4 | Pass: policy/budget y cierre pre-I/O no muestran regresión. |
| R5 | Pass: captures tipadas, acotadas, saneadas y privadas permanecen cubiertas. |
| R6 | Pass: resolución tardía y ausencia nominal de secretos en harness/superficies. |
| R7 | Pass: intent durable, crash post-I/O y no replay de create confirmado. |
| R8 | Pass: cleanup inverso, continuado y recuperable para toda obligación real. |
| R9 | Pass: scope, llamadas, retries, backoff, restart y owners permanecen finitos. |
| R10 | Pass: retry conserva idempotencia contractual y key estable sin exposición. |
| R11 | Fail: separación primary/cleanup funciona, pero el audit offline puede re-firmarse tras sustituir el anchor (REV-33). |
| R12 | Fail: paridad y redacción nominal pasan; autenticidad offline y canarios fallan bajo sustitución completa (REV-33). |
| R13 | Pass: CAS/lease/ownership/cancel/recovery y focales race no muestran duplicación ni data race. |

### Probes y verificación ejecutada

- `TestSeventhProbeAuthoritySubstitution`: **FAIL esperado del producto** en sus cuatro subcasos; el reader aceptó sidecar, firma y audit sustituidos concordantemente.
- `TestSeventhProbePackageOnlyAuthoritySubstitution`: PASS; cambiar firma/public key solo dentro del package se rechaza.
- Matriz implementada de firma/artifacts ×20: PASS; confirma la protección cuando el sidecar permanece confiable.
- Budget, restart, cleanup y canarios de proyección ×20: PASS.
- Paridad de las cuatro superficies, rotación y `fixture_runtime` ×10: PASS.
- `python scripts/feature26_coverage.py`: comando PASS en Windows, `1666/1958 = 85,0868 %`; auditoría independiente obtiene 15+1 statements Unix y total corregido `85,0434 %`.
- Probe del algoritmo con profile sin fuentes Windows: FAIL cerrado con `RuntimeError`, confirmando que el comando no funciona en un host Unix.
- Test Unix real: el checkout incluye `store_unix_test.go` y el binario de tests Linux cross-compila. No pudo ejecutarse en este host: solo existe la distro `docker-desktop`, Docker daemon no está disponible y WSL falla al crear la VM con `ERROR_NO_SYSTEM_RESOURCES`. Esta limitación ambiental no corrige REV-34.

### Gates de séptima revisión

- `gofmt -l` sobre todo Go cambiado/nuevo: limpio.
- `git diff --check 1daa391`: PASS, solo warnings CRLF del checkout.
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- Paquetes afectados ×3 y suite global `go test ./... -count=2`: PASS, sin timeout.
- Race focal de firma/fixtures/composición: PASS repetido. Dos ejecuciones `go test -race ./... -count=1` fallaron por carga en dos tests históricos distintos (`TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial/terminal_redelivery` y `TestRevisionOrConfigurationDriftStopsBeforeTool/revision`), sin reporte de data race; cada focal pasó aislado ×20 y ninguna pertenece al cambio Ed25519/coverage. El gate race global no queda limpio, aunque no hay evidencia de una carrera de feature 26.
- Cross-build Linux/amd64 y Darwin/amd64: PASS; `go test -c` del paquete fixture para Linux, incluido `store_unix_test.go`: PASS.
- Comentarios sistémicos: no se detectaron símbolos nuevos/modificados sin comentario.
- Alcance/árbol: no hay feature 27, `feature_list.json` sigue intacto y no quedan temporales de build/coverage ni `__pycache__` en el workspace.

### Decisión de séptima revisión

La feature no debe marcarse `done`. Debe separar las capacidades Ed25519 y anclar la public key fuera del package sin exponer la private key al verifier (REV-33), además de hacer el gate de coverage simétrico y reproducible en Windows/Unix (REV-34). Después requiere otra revisión independiente.

## Octava revisión independiente

### Veredicto

**rejected**

REV-34 queda cerrado: la metadata de cobertura es simétrica, está ligada por SHA-256 a los cuatro fuentes de plataforma, usa los conteos verificados 17+7/15+1, agrega cada archivo una sola vez y el diff completo alcanza 85,0477 % sin depender del redondeo. REV-33 queda corregido durante una ejecución, porque el reader recibe solo `FixtureAuditTrust` y no abre la private key, pero la raíz de confianza no sobrevive de forma autenticada a restart. REV-35 concreta tres bypasses de reload/identidad reproducidos de forma independiente.

### Revalidación REV-33/REV-34

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-33 | parcial; abierto como REV-35 | `ReadPackageWithFixtureTrust` no conoce una ruta privada y verifica con public key externa. Dominio, versión, run, `BindingSHA256` y audit digest están en el mensaje firmado; cambiar package/public key/firma/audit sin la private key falla. Sin embargo, composición reconstruye signer y trust desde el mismo archivo privado en cada arranque: reemplazar ese archivo por otro keypair produce un nuevo trust aceptado sin pin ni autorización. |
| REV-34 | cerrado | Los unit tests simulan profile Windows y Unix y obtienen 40 statements de plataforma en ambos casos. El comando real verifica los hashes fuente, cuenta Windows 17+7 y Unix 15+1 exactamente una vez y da `1695/1993 = 85,0477 %`; la comparación usa el cociente sin redondear. Cambios/faltas/count mismatch causan `RuntimeError`. |

## REV-35 — Restart adopta una private key sustituida como nueva raíz de confianza

- Severidad: **blocker**.
- Requisitos/tareas: R11, R12, R13; T10, T11, T12, T14.
- Archivos: `internal/localvalidation/fixture_package.go:25-66`; `internal/localvalidation/package_reader.go:200-215`; `cmd/featureproof/workflow.go:570-582`.
- Evidencia 1: `TestEighthProbePersistentAuthorityRejectsSilentRotation` abrió una autoridad, reemplazó el archivo privado por otro keypair Ed25519 válido y volvió a ejecutar `OpenFixtureAuditAuthority`. La segunda apertura terminó correctamente y publicó otro `KeyID`; no existe un pin público independiente que distinga rotación autorizada de sustitución.
- Evidencia 2: `TestEighthProbePersistentAuthorityRejectsSameLengthCorruption` alteró un byte del seed conservando los 64 bytes. `OpenFixtureAuditAuthority` aceptó el material aunque seed y public half ya no correspondían. La implementación valida longitud, pero no deriva la public key desde el seed ni comprueba coherencia criptográfica.
- Evidencia 3: `TestEighthProbeTrustKeyIDMustMatchPublicKey` cambió el `KeyID` del trust externo y el `key_id` público del package al mismo SHA-256 formalmente válido, manteniendo public key y firma originales. El reader aceptó porque comprueba forma e igualdad, pero no exige `KeyID == SHA256(PublicKey)` y el key ID tampoco forma parte del mensaje firmado.
- Auditoría de persistencia: `O_EXCL` protege la creación inicial y una carrera que pierde vuelve a leer; esto evita overwrite durante create. En reload se usa `os.ReadFile` sin `Lstat`, single-link, permisos/owner ni pin previo, por lo que symlinks/hardlinks o reemplazo válido se aceptan. La escritura tampoco comprueba `n == ed25519.PrivateKeySize`, de modo que una escritura parcial puede permitir el arranque actual con la copia en memoria y romper el siguiente restart.
- Impacto: un atacante que sustituya el archivo de autoridad entre procesos controla signer y trust tras restart y puede volver a firmar cualquiera de los cuatro canarios que la ejecución viva ya rechaza. Incluso sin la private key, se puede falsificar la identidad auditada del anchor cambiando `key_id` de trust/package sin romper la firma.
- Corrección requerida: persistir/pinear la public key o su fingerprint en una fuente confiable independiente de la private key y exigir coincidencia en todo restart; hacer explícita y autenticada cualquier rotación. Validar `SHA256(PublicKey) == KeyID`, incluir key ID en el mensaje firmado, comprobar que la private key deriva coherentemente de su seed, y cargar solo un archivo regular single-link con permisos/propiedad esperados. Completar escritura con `io.WriteFull`, cleanup ante error y fsync del directorio. Separar también los componentes de workflow para que el objeto lector no almacene `AuditSigner`, aunque su método `Read` actualmente no lo use.

## REV-36 — Quedan artefactos Python sin trackear en el repositorio

- Severidad: **low**.
- Requisito/tarea: T14.
- Evidencia: `git status --short` muestra `?? scripts/__pycache__/` con `feature26_coverage.cpython-314.pyc` y `test_feature26_coverage.cpython-314.pyc`, aunque `implementation.md` declara que no quedan temporales.
- Corrección requerida: eliminar el directorio y ejecutar los gates Python con `PYTHONDONTWRITEBYTECODE=1` o equivalente para conservar el árbol limpio.

### Ataques de autoridad

- PASS: el reader no abre ni conoce la ruta de la private key; un sidecar adyacente atacante no afecta un trust ya cargado.
- PASS: public key extranjera, trust ausente, firma mutada, audit mutado y cuatro canarios SHA re-sellados sin private key fallan cerrado.
- PASS: el mensaje está separado por `featureproof.fixture-audit/v1` y liga package version, run ID, binding y audit digest.
- PASS: reload sin modificación conserva el mismo key ID; clave de longitud inválida falla cerrado; creación usa `O_EXCL`.
- FAIL: reemplazo persistente por otro keypair válido se adopta silenciosamente tras restart.
- FAIL: corrupción interna de 64 bytes se acepta al abrir.
- FAIL: `key_id` no está criptográficamente ligado a `PublicKey` ni al mensaje firmado.

### Regresión R1–R13

| Requisito | Resultado de octava revisión |
|---|---|
| R1 | Pass: v3 estricta, compatibilidad v1/v2, null/uniones y finitud JSON permanecen correctos; coverage total 85,0477 %. |
| R2 | Pass: grafo, referencias, orden y deep-copy no muestran regresión. |
| R3 | Pass: contrato, target, identity, idempotencia y header exactos permanecen ligados. |
| R4 | Pass: policy/budget y cierre pre-I/O permanecen fail-closed. |
| R5 | Pass: captures siguen tipadas, acotadas, saneadas y privadas. |
| R6 | Pass: credenciales se resuelven tarde y no cruzan harness/superficies. |
| R7 | Pass: intent durable, create incierto y no replay permanecen correctos. |
| R8 | Pass: cleanup inverso/continuado/pending y redelivery convergen. |
| R9 | Pass: scopes, calls, retries, backoff, restart y owners permanecen finitos. |
| R10 | Pass: idempotencia contractual y key estable permanecen cerradas. |
| R11 | Fail: primary/cleanup están separados, pero la autenticidad del audit puede sustituirse tras restart (REV-35). |
| R12 | Fail: paridad y redacción pasan; el trust anchor persistente y key ID no fallan cerrado (REV-35). |
| R13 | Fail parcial: lifecycle/claims/CAS/race pasan, pero ownership de la autoridad cambia silenciosamente entre owners/restarts (REV-35). |

### Coverage y plataformas

- `python -m unittest scripts/test_feature26_coverage.py`: 2/2 PASS; perfiles Windows y Unix terminan con 40 statements de plataforma sin doble conteo.
- `python scripts/feature26_coverage.py`: PASS, `1695/1993 = 85,0477 %`.
- Metadata verificada en el comando: Windows `store_lock=17`, `store_replace=7`; Unix `store_lock=15`, `store_replace=1`; SHA-256 de cada fuente coincide.
- Todos los fuentes productivos modificados aparecen una vez en `by_file` o en la allowlist explícita de cero statements; cualquier fuente ejecutable restante ausente causa error.
- Cross-build Linux/amd64 y Darwin/amd64 PASS; `go test -c` de fixture para Linux, incluido el test Unix real, PASS. El host sigue sin runtime Unix disponible, por lo que no se ejecutó el binario cross-compilado.

### Gates de octava revisión

- Autoridad/package ×20, budget/cleanup/canarios ×20 y verticales productivas/paridad/configuración ×10: PASS para la suite implementada.
- Probes propios de rotación, corrupción y key ID: tres FAIL esperados del producto, reproducen REV-35.
- `gofmt -l` sobre Go cambiado/nuevo: limpio.
- `git diff --check 1daa391`: PASS; solo warnings CRLF.
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- Paquetes afectados ×3 y `go test ./... -count=2`: PASS.
- `go test -race` de todos los paquetes afectados: PASS. `go test -race ./... -count=1`: PASS global en esta revisión, sin data races ni timeouts.
- Comentarios sistémicos: auditoría de entidades, funciones y helpers nuevos/modificados sin omisiones.
- Alcance: no hay implementación de feature 27 y `feature_list.json` permanece intacto.
- Temporales: FAIL por `scripts/__pycache__/` sin trackear (REV-36); no hay perfiles, binarios ni temporales en la raíz.

### Decisión de octava revisión

La feature no debe marcarse `done`. Debe anclar la autoridad pública de forma independiente y segura a través de restart/rotación, validar coherencia de private/public/key ID y endurecer el archivo persistente (REV-35). También debe retirar los temporales Python (REV-36) antes de una nueva revisión independiente.

## Novena revisión independiente

### Veredicto

**rejected**

REV-35 queda cerrado para identidad criptográfica, separación de capacidades y restart: el pin público se persiste en una ruta distinta de la private key, un keypair sustituido o una private key de longitud correcta pero incoherente fallan cerrado, `KeyID` es el SHA-256 de la public key y forma parte del mensaje firmado. REV-36 también queda cerrado: no quedan `__pycache__` ni otros temporales en el workspace. Sin embargo, la protección persistente sigue siendo evadible en Windows porque la validación acepta ACL peligrosas mientras el owner continúe siendo el usuario actual. REV-37 reproduce el bypass tanto para la confidencialidad de la private key como para la integridad del pin.

### Revalidación REV-35/REV-36

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-35 | cerrado en sus bypasses originales; reemplazado por REV-37 para ACL Windows | Probes overlay externos al árbol pasaron ×10 al sustituir el keypair tras restart y al corromper un seed conservando 64 bytes: ambos reloads fueron rechazados. `FixtureAuditTrust` contiene solo `KeyID`/public key; `LocalPackageReader` almacena solo ese trust y el writer concentra `AuditSigner`. El mensaje usa `featureproof.fixture-audit/v1` y cambia al variar versión, run, binding, key ID o digest del audit. El reader exige `KeyID == SHA256(PublicKey)` y verifica la firma sobre ese mensaje. |
| REV-36 | cerrado | `git status --short` no muestra `scripts/__pycache__/`; las pruebas Python se ejecutaron con `PYTHONDONTWRITEBYTECODE=1` y no dejaron bytecode, perfiles ni binarios. |

## REV-37 — Windows acepta private key legible y pin modificable por Everyone

- Severidad: **blocker**.
- Requirements/tareas: R12; T11, T12, T14; `ARCH-006`, `ARCH-007`, `CONV-007`.
- Archivos y líneas: `internal/localvalidation/fixture_authority_owner_windows.go:12-20`; `internal/localvalidation/fixture_package.go:120-138`.
- Evidencia: `fixtureAuthorityOwnerOK` consulta únicamente el owner SID. No inspecciona la DACL ni comprueba quién puede leer/escribir/borrar la private key o escribir/borrar el pin. Un probe propio inyectado mediante `go test -overlay` creó una autoridad válida, concedió `Everyone (S-1-1-0)` permiso de lectura sobre la private key con `icacls` y volvió a abrirla: `OpenFixtureAuditAuthority` la aceptó. Un segundo probe concedió `Everyone` control total sobre el pin y también fue aceptado. Los tests productivos saltan expresamente la comprobación `Chmod(0644)` en Windows, por lo que esta frontera no tiene cobertura negativa real en esa plataforma.
- Impacto: con una ACL heredada o alterada que conserve el mismo owner, otro principal puede leer la private key y firmar un audit manipulado, o modificar el pin y la autoridad persistente. Las comprobaciones de coherencia, fingerprint y firma dejan de aportar autenticidad porque el material confiable ya es accesible/modificable. Un package de cleanup podría verificarse como auténtico después de alterar sus hechos allowlisted.
- Corrección requerida: endurecer y validar la DACL en creación y reload. La private key debe rechazar cualquier lectura, escritura, cambio de permisos o borrado por principals no confiables; el pin puede ser público, pero debe rechazar escritura, cambio de permisos o borrado no confiables. No basta comprobar owner. Añadir tests Windows reales con ACEs para `Everyone`/`Users`, incluyendo lectura de key y escritura/delete de key y pin, y mantener los checks actuales de regular file, single-link, coherencia, pin y replacement race.

### Ataques de autoridad y package

- PASS: pin público persistido fuera de la ruta privada usada por composición; sustituir solo el keypair por otro válido tras restart falla por mismatch contra el pin.
- PASS: corrupción same-length del seed falla porque se deriva y compara el par completo.
- PASS: `KeyID` coincide con `SHA256(PublicKey)` en apertura y verificación; un cambio concordante de IDs sin private signature no verifica.
- PASS: el mensaje liga domain/version/run/binding/key ID/audit digest; los cinco cambios produjeron mensajes distintos en probe propio ×10.
- PASS: file symlink, hardlink, replacement race, short write y error de escritura están cubiertos y fallan cerrado; bootstrap concurrente conserva una identidad.
- PASS: el reader no contiene signer ni ruta privada; writer y reader reciben capacidades separadas.
- FAIL: private key world-readable en Windows se acepta mientras conserve owner.
- FAIL: pin world-writable en Windows se acepta mientras conserve owner.

### Regresión R1–R13

| Requirement | Resultado de novena revisión |
|---|---|
| R1 | Pass: v3 estricta, v1/v2 compatibles y límites/JSON recursivo sin regresión. |
| R2 | Pass: grafo, referencias, orden estable y copias inmutables sin regresión. |
| R3 | Pass: contrato aprobado, target, identidad, idempotencia y operación exacta permanecen ligados. |
| R4 | Pass: guards y bloqueo pre-I/O permanecen cerrados. |
| R5 | Pass: captures tipadas, acotadas, confirmadas, privadas y saneadas. |
| R6 | Pass: resolución tardía y separación de credenciales permanecen correctas. |
| R7 | Pass: intent durable, create incierto y recovery sin replay permanecen correctos. |
| R8 | Pass: cleanup inverso, continuado y siempre intentado sin regresión. |
| R9 | Pass: scope propio finito, budgets, retries y ausencia de trabajo background. |
| R10 | Pass: idempotency guarantee/key estable y revalidación por intento. |
| R11 | Pass: primary inmutable, fixture summary y cleanup report separados y relacionados. |
| R12 | **Fail**: package/audit y superficies son seguras en contenido, pero la autoridad persistente acepta ACL Windows que exponen la clave o permiten alterar el pin (REV-37). |
| R13 | Pass: ownership del lifecycle, CAS, recovery/redelivery y paridad de superficies no muestran regresión. |

### Coverage y plataformas

- `python -m unittest scripts/test_feature26_coverage.py`: 2/2 PASS.
- `python scripts/feature26_coverage.py`: PASS, `1769/2080 = 85,0481 %`, calculado sin redondear para el gate.
- El script incluyó cada fuente productiva modificada una vez, verificó SHA-256 y statement counts para las implementaciones Windows/Unix de store y autoridad, y mantuvo 53 statements de plataforma en ambos perfiles. Las fuentes solo declarativas están en allowlist explícita.
- Cross-compile de todos los paquetes para Linux/amd64 y Darwin/amd64: PASS usando `go test -exec='C:\\Windows\\System32\\cmd.exe /c exit 0' ./... -run '^$'`. Un intento inicial con `-exec=true` falló porque Windows no tenía un ejecutable llamado `true`; fue un error del comando de review, no de compilación del producto, y el gate corregido pasó en ambas plataformas.
- Los probes de ACL se ejecutaron en Windows real. El runtime Unix no se ejecutó en este host; sus fuentes y tests cross-compilaron y su denominador permanece incluido.

### Gates de novena revisión

- Autoridad/package focal ×30: PASS. Probes propios de keypair sustituido, private incoherente y binding del mensaje ×10: PASS. Probes propios de ACL: dos FAIL esperados del producto, reproducen REV-37.
- Paquetes afectados ×3 y suite global `go test ./... -count=2`: PASS.
- `go test -race` de todos los paquetes afectados: PASS. `go test -race ./... -count=1`: PASS global usando UCRT64, sin data races ni timeouts. El primer intento sin `CGO_ENABLED=1` fue rechazado por la toolchain antes de compilar; no fue un fallo del producto.
- `gofmt -l` sobre Go cambiado/nuevo: limpio.
- `git diff --check 1daa391`: PASS; solo warnings CRLF del checkout.
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- Comentarios sistémicos: no se detectaron omisiones nuevas en entidades, funciones, métodos o helpers modificados.
- Alcance/árbol: `feature_list.json` permanece intacto, no hay código de feature 27 y no quedan perfiles, binarios, `.tmp` ni `__pycache__` en el workspace.

### Decisión de novena revisión

La feature no debe marcarse `done`. Debe cerrar REV-37 validando y endureciendo las ACL Windows de la private key y del pin, con probes reales que demuestren confidencialidad de la clave e integridad del pin bajo ACEs hostiles. Después requiere otra revisión independiente.

## Décima revisión independiente

### Veredicto

**approved**

REV-37 queda cerrado. En Windows, los dos archivos de autoridad reciben antes de escribir una DACL protegida y exacta limitada al owner, LocalSystem y Administrators. Cada reload vuelve a comprobar owner, presencia/protección/no-default de la DACL, número y tipo de ACEs, ausencia de herencia, máscara exacta, principals exactos y ausencia de duplicados. Los ataques propios contra confidencialidad de la private key e integridad del pin fallaron cerrado, mientras la allowlist canónica, restart, bootstrap concurrente y recuperación del mismo key ID permanecieron válidos.

### Revalidación REV-37

| Vector | Resultado | Evidencia independiente |
|---|---|---|
| `Everyone` lee private key | cerrado | Probe overlay concedió `FILE_GENERIC_READ`; apertura/reload rechazado ×20. |
| `Users` escribe o borra private key | cerrado | Probe overlay añadió `FILE_GENERIC_WRITE | DELETE`; apertura/reload rechazado ×20. |
| `Authenticated Users` controla private key | cerrado | Probe overlay añadió full control; apertura/reload rechazado ×20. |
| `Everyone` controla pin | cerrado | Probe overlay añadió full control; apertura/reload rechazado ×20. |
| `Users` escribe o borra pin | cerrado | Probe overlay añadió `FILE_GENERIC_WRITE | DELETE`; apertura/reload rechazado ×20. |
| `Authenticated Users` cambia DACL/owner del pin | cerrado | Probe overlay añadió `WRITE_DAC | WRITE_OWNER`; apertura/reload rechazado ×20. |
| DACL no protegida/heredable | cerrado | Test productivo convierte la DACL a no protegida y reload falla. Probe propio creó key/pin bajo directorios con ACE heredable hostil: create sustituyó la herencia antes de escribir, dejó ambos archivos con la allowlist protegida y el reload fue válido ×20. |
| Máscara reducida | cerrado | Un principal confiable con `FILE_GENERIC_READ` en lugar de la máscara canónica es rechazado. |
| ACE deny, duplicada o principal extra | cerrado | Probes propios con deny explícito y owner duplicado sustituyendo Administrators fueron rechazados ×20; la matriz productiva rechaza además principal extra/reemplazado. |
| DACL ausente/defaulted/malformada | cerrado por implementación fail-closed | `GetNamedSecurityInfo`, `IsValid`, `DACL`, `Control`, `GetAce`, DACL nil y flags defaulted/present se validan antes de usar SIDs. Windows no permitió persistir un `INHERITED_ACE` contradictorio dentro de una DACL protegida: lo normalizó a flags `0x0`; el estado leído era canónico, no un bypass. Missing descriptor y estructuras no canónicas fallan cerrado. |
| Allowlist owner + SYSTEM + Administrators | válido | Create y reload canónicos pasaron en tests productivos ×30 y probes propios; cada SID aparece exactamente una vez con full control y sin herencia. |
| Symlink/hardlink/replacement/race | cerrado | Los checks de `Lstat`, regular file, single link, `SameFile`, lock y pin se mantienen; la matriz de autoridad completa pasó ×30. |

La política no amplía privilegios: `protectFixtureAuthorityFile` reemplaza la DACL heredada, no añade principals desde input, y se ejecuta antes de escribir bytes. En Unix la única variación es un `chmod(0600)` previo a la escritura; reload sigue exigiendo owner UID y ausencia de bits group/other. Linux/amd64 y Darwin/amd64 cross-compilan con los fuentes Unix y el gate de cobertura conserva sus statements en el denominador.

### Regresión R1–R13

| Requirement | Resultado de décima revisión |
|---|---|
| R1 | Pass: v3 estricta, compatibilidad v1/v2, límites y JSON recursivo. |
| R2 | Pass: grafo acíclico, referencias, orden estable y copias inmutables. |
| R3 | Pass: operación exacta ligada a contrato aprobado, target, identidad e idempotencia. |
| R4 | Pass: revalidación, policy y budget antes de I/O; dependientes/primary bloqueados tras fallo. |
| R5 | Pass: captures tipadas, acotadas, confirmadas, privadas y saneadas. |
| R6 | Pass: identidad explícita, resolución tardía y ausencia de secretos en harness/superficies. |
| R7 | Pass: intent durable pre-I/O, create incierto y recovery sin repetir create confirmado. |
| R8 | Pass: cleanup siempre intentado, inverso y continuado tras incidencias. |
| R9 | Pass: scope propio finito, calls/retries/backoff acotados y sin trabajo background. |
| R10 | Pass: garantía y key de idempotencia system-owned, estable y revalidada por intento. |
| R11 | Pass: primary inmutable separado de `FixtureSummary` y `CleanupReport`. |
| R12 | Pass: audit firmado, redacción, package offline verificable, paridad de superficies y autoridad persistente protegida en Windows/Unix. |
| R13 | Pass: un owner lógico, CAS/leases, recovery/redelivery y lifecycle compartido sin duplicar efectos. |

### Coverage y plataformas

- `python -m unittest scripts/test_feature26_coverage.py`: 2/2 PASS con `PYTHONDONTWRITEBYTECODE=1`.
- `python scripts/feature26_coverage.py`: PASS, `1812/2131 = 85,0305 %`; la comparación usa el cociente real, no su redondeo.
- La medición incluye cada fuente productiva modificada una vez, allowlist explícita de fuentes declarativas y metadata SHA-256/statement counts para store y autoridad en Windows/Unix. La implementación Windows de autoridad obtuvo `47/54`; los statements Unix no ejecutables en este host permanecen en el denominador.
- Todos los paquetes cross-compilaron para Linux/amd64 y Darwin/amd64 mediante `go test -exec='C:\\Windows\\System32\\cmd.exe /c exit 0' ./... -run '^$' -count=1`.

### Gates de décima revisión

- Matriz productiva de DACL/autoridad/restart/race/symlink/hardlink ×30: PASS.
- Probes overlay propios de principals hostiles ×20, allowlist, deny y duplicados ×20, parent DACL heredable/create/reload ×20 y matriz criptográfica previa ×10: PASS.
- `gofmt -l` sobre todos los Go modificados/nuevos: limpio.
- `git diff --check 1daa391`: PASS; solo warnings CRLF del checkout.
- `go vet ./...`: PASS.
- `go test ./... -run '^$' -count=1`: PASS.
- Paquetes afectados ×3: PASS.
- Suite global `go test ./... -count=2`: PASS.
- `go test -race ./... -count=1` con UCRT64/CGO: PASS global, sin data races ni timeouts.
- Comentarios sistémicos: entidades, constantes, funciones, métodos y helpers nuevos/modificados conservan comentario de responsabilidad.
- Precondiciones/alcance: aprobación humana presente, T1–T14 `[x]`, informe del implementer presente, solo feature 26 `in_progress`; `feature_list.json` permanece intacto y no hay código de feature 27.
- Árbol: sin `__pycache__`, perfiles de cobertura, binarios de test, `.tmp` ni ejecutables generados.

### Decisión de décima revisión

No quedan hallazgos bloqueantes ni abiertos para feature 26. La implementación satisface R1–R13, cierra REV-1..REV-37 y supera los gates obligatorios. Puede transicionar a `done` por el leader.
