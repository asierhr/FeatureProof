# Paquete de evidencias interoperable — Diseño

## Resumen y contexto comprobado

El paquete v1 es una proyección segura del run terminado dentro de `.featureproof/runs/<run-id>/`. El publicador construye documentos derivados en el staging existente, calcula un inventario cerrado, escribe `manifest.json`, valida el staging con el mismo lector offline y usa `CommitNoReplace` para hacerlo visible. El lector no recalcula assertions ni llama al modelo: comprueba integridad y relaciones entre hechos ya confirmados.

Fuentes consultadas: `feature_list.json` feature 15 y límites de features 16–19; `AGENTS.md`, `.codex/agents/spec.md`, `docs/specs.md`, `docs/architecture.md` (ARCH-003/004/006/007/008/009/010/012) y `docs/conventions.md` (CONV-003/004/005/007/008/009); `internal/revisionbinding/{binding,projection}.go`, `internal/localvalidation/{publisher,model,filesystem,bound}.go`, `internal/evidence/{store,redaction}.go`, `internal/harness/journal.go`, `internal/planning/model.go`, `internal/agentplanning/model.go`, `internal/replanning/{journal,ledger,summary,coordinator}.go`. No hay contrato externo de terceros.

## Contratos actuales y adaptación

- `revisionbinding.Binding.Verify` verifica la identidad sellada y `VerifyPlanChain` verifica los lotes ordenados. El paquete guarda/copía estos valores; no define otra identidad de revisión ni recalcula digests de configuración desde datos crudos.
- `localvalidation.RunSnapshot` ya lleva binding, plan bindings, estados y árbol de pasos; `Publisher` verifica secuencia y evidencia, escribe `run.json`, `events.jsonl`, `plan.json`, `report.json`, `report.md` en staging y confirma con `CommitNoReplace`. Se extiende esa transacción, no se agrega un segundo directorio final.
- `evidence.Store.Get` confirma registros saneados con `IntegrityHash`; el registro tiene `RunID`, `BindingSHA256`, plan/step/tool IDs, input/output y truncamientos. El paquete no permite evidencia tomada de otro store ni de una ruta construida con un ID sin validar.
- `planning.AssertionResult` contiene `Expected/Observed` con `Present`, status, operador, `NodePath` y evidencia. Hoy `localvalidation.safeAssertions` borra esos valores del `RunSnapshot` y `report.json` no los contiene. La implementación debe captar una proyección segura en la frontera determinista antes de ese borrado, manteniendo `report.json` sin payload. No se deducen valores de la prosa del modelo o de `plan.json`, que redacta literales.
- La procedencia de escenarios/checks vive en `agentplanning.CandidateCheck` y, para replanning, `ConfirmedCheck`/`ConfirmedAssertion` con identidad de batch, check, step, declaration y node path. El journal de harness y el de replanning aportan secuencia/linaje confirmado. Se usa esta procedencia cuando existe; un plan local sin escenario declara `unspecified`.

## Formato v1 propuesto

Raíz final: los seis artefactos existentes, `evidence/<id>.json`, y nuevos `package.json` y `manifest.json`. `package.json` es el índice semántico y contiene `version: 1`, `run_id`, `binding`, `plan_bindings`, `status`, `constraints[]`, `failures[]`, `facts[]`, `evidence[]`, `reproduction[]` y `diagnostics[]` (solo si hay datos seguros). El manifest contiene `version: 1`, `run_id`, `binding_sha256`, `artifacts[]` ordenado por ruta relativa, con `{path, size_bytes, sha256}`. `manifest.json` no se autoenumera ni autohashea; su tamaño se limita y su forma se valida estrictamente. SHA-256 de cada entrada se calcula sobre bytes exactos y tamaño en bytes, nunca sobre JSON recanonizado. Los registros de evidencia conservan además su hash interno existente.

Identidades de fallo: `{constraint_id, scenario, batch_id?, check_id?, step_id, declaration_id, node_path, operator, status, expected, observed, evidence_ids}`. Cada valor usa `{state: present|absent|redacted|truncated, value?}`; `value` solo existe para una proyección declarada no sensible y dentro del límite. No se usa un valor de texto marcador como prueba de ausencia. Un grupo fallido mantiene su nodo y sus hijos referenciados. `facts[]` contiene hechos de herramienta y assertions con tipo, IDs, status, procedencia y referencias; no cambia el veredicto. `diagnostics[]` se marca `source: agent`, se limita y se trata como dato inerte. En v1 puede quedar vacío si no hay diagnóstico seguro confirmado.

`evidence[]` mapea cada ID único a `evidence/<id>.json`. El lector resuelve únicamente esa tabla, compara el ID interno del registro, el run y el binding, y rechaza rutas duplicadas o no canónicas. Las referencias desde outcomes, hechos y fallos deben apuntar a entradas de esa tabla; step/assertion/check/batch deben existir en la topología confirmada del paquete o artefactos existentes. Se verifica igualdad del resultado publicado con la proyección determinista: ningún `passed` sin evidencia y ningún fallo funcional sin assertion fallida. `report.json`/`run.json` conservan sus contratos actuales.

`reproduction[]` guarda instrucciones declarativas, nunca shell/script: commit completo, repository/environment/attestation IDs, digests no secretos de constraints/contract/config y lotes de plan, IDs de restricción/escenario/check/step, evidencia inspeccionable, prerrequisito de atestación y referencias opacas de credenciales. Cuando un input literal fue ocultado o falta un dato seguro, `replayability: manual_prerequisite` y una causa tipada indican que no se puede reproducir exactamente con el paquete solo. La URL del target y tokens no se copian. Ninguna instrucción habilita ejecución directa en el lector.

## Seguridad, límites y rutas

El productor reutiliza la política `evidence.Policy` validada: redacción de headers y rutas JSON, truncamiento y proyección de valores de assertions antes de serializar. Una proyección no clasificable falla cerrada. Los valores seguros de expected/observed se limitan por campo (64 KiB codificados), y el texto diagnóstico se omite salvo que una fuente fiable garantice redacción. El manifest impone hasta 10 000 entradas, 64 MiB por archivo, 256 MiB total y 1 MiB para sí mismo; superar límites es error de publicación/lectura. Son límites del paquete, separados de los límites de ejecución/evidencia existentes.

Las rutas son relativas, con `/`, segmentos ASCII seguros, sin `.`/`..`, prefijos de unidad, barras inversas, segmento vacío ni duplicado tras normalización. Se hace `Lstat` de cada componente y solo se aceptan directorios ordinarios internos y archivos regulares con un único nombre; se rechazan symlinks, reparse points, hard links detectables y archivos especiales. El recorrido del árbol no sigue enlaces y compara el conjunto exacto de archivos regulares con `artifacts[] + manifest.json`; se limita cantidad/profundidad/tamaño antes de leer. Una relectura tras hashing y justo antes de commit detecta sustitución; el staging es de acceso restringido y propiedad del publicador. En lectura de un directorio no confiable, usar apertura confinada/no-follow donde el sistema operativo lo admita; ante imposibilidad de garantizar confinamiento por carrera de reemplazo, fallar cerrado.

## Flujo, estado y errores

1. El propietario único del run congela `RunSnapshot`, eventos, secuencia de lotes, topología de checks y proyecciones seguras de assertions; confirma que no hay acciones de herramienta pendientes.
2. `Publisher` conserva su verificación de binding/eventos/store. Proyecta el índice desde resultados deterministas y evidencia ya tratada. Escribe `package.json` en staging mediante el mecanismo temporal y fsync actual.
3. Recorre staging; valida nombres, tipo y límites; calcula tamaño/digest de todos los archivos menos manifest y escribe `manifest.json` al final. Sin archivos temporales sobrantes.
4. El lector offline verifica staging completo: versión/forma, inventario exacto, hashes, `Binding.Verify`, cadena de plan, coherencia de run/report/eventos/evidence e IDs cruzados. Solo entonces `syncDirectory` y `CommitNoReplace`.
5. El lector de un paquete recibido repite la misma verificación, devuelve una vista inmutable solo al terminar y no accede a red.

Un error clasificado (`unsupported_version`, `invalid_path`, `limit_exceeded`, `digest_mismatch`, `missing_artifact`, `extra_artifact`, `broken_reference`, `binding_integrity`, `cancelled`) lleva solo ruta relativa segura y causa técnica, no payload. Fallo antes de commit deja `.incomplete`; la ruta final no se crea. Colisión no reemplaza. No se reintenta escritura bajo el mismo ID sin revisar staging/collision: un nuevo intento independiente usa un nuevo run, y el lector es idempotente para bytes estables. Un final ya existente no se sobreescribe.

## Propiedad del estado, cancelación y observabilidad

El harness es propietario del run; el publicador del staging; el store de la evidencia confirmada; el lector solo observa. No se comparten estructuras mutables ni se crean goroutines. Todas las operaciones potencialmente bloqueantes reciben `context.Context`, revisan cancelación durante recorrido, hashing y lectura, y la respetan antes del commit. El tratamiento especial de cancelación terminal existente puede construir un snapshot `cancelled` con un contexto de limpieza vigente; nunca continúa con el contexto cancelado original. Se registran categoría, fase, run ID y recuentos/tamaños seguros, sin valores, rutas absolutas, secretos ni prosa del modelo. El estado `failed` del producto nunca se infiere de un error de empaquetado.

## Pruebas y mapa de trazabilidad

Tests con directorios temporales, store/FS fakes y reloj estable, sin red ni modelo. Fixtures de passed/failed/inconclusive/cancelled; bytes manipulados; inventario y rutas adversarias; binding ajeno; referencia rota; canarios en todas las superficies; cancelación e interrupción entre pasos de publicación. Cada test comprueba además que el directorio final no aparece si falla la publicación. Los tests de lectura no invocan herramientas.

| Requirement | Componente | Prueba prevista |
|---|---|---|
| R1 | índice + lector | paquete v1 de cuatro resultados; versión desconocida |
| R2 | proyector de identidad + lector | cambio de commit/binding/plan chain |
| R3 | proyector de hechos | hipótesis contradictoria no altera status |
| R4 | proyector de fallos | leaf/grupo fallido y valores absent/redacted/truncated |
| R5 | inventario/lector | modificación, ausencia, extra, symlink, ruta, límites |
| R6 | Publisher | interrupción y colisión antes de commit |
| R7 | frontera de redacción | canarios, truncamiento y fallo cerrado |
| R8 | lector offline | referencias rotas, run ajeno, vista solo al validar |
| R9 | instrucciones declarativas | credencial opaca y prerrequisito faltante |
| R10 | Publisher/lector | cancelación y error operativo separados |

## Decisiones y alternativas

Se mantiene un directorio en vez de ZIP para reutilizar la publicación atómica local y permitir inspección offline sencilla. El manifest no se incluye a sí mismo para evitar un digest recursivo imposible; su contenido se valida como esquema y el inventario cerrado detecta extras. Un hash no es una firma ni autentica el origen. Se mantiene `report.json` sin valores; el nuevo índice recibe únicamente una proyección tratada previa al borrado de `Expected/Observed`. No se serializa el texto libre del modelo como hecho, ni se intenta reconstruir expected desde `plan.json`. El alcance termina en archivos locales y lector; transporte, autorización, corrección y publicación externa pertenecen a features posteriores.
