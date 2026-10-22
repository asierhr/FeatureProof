# Novena revisión independiente — Feature 25: test-identities

## Veredicto

`approved`

La remediación cierra REV-18. Todos los appends pre-I/O de un intento autenticado
reconcilian `context.Canceled` y `context.DeadlineExceeded` con un único terminal
`cancelled`, conservan el último prefijo durable confirmado y no permiten que el
tool, el resolver ni la red se ejecuten después de la cancelación. Si también
falla el append terminal, el error conserva ambas causas mediante `errors.Join`.

No encontré nuevos hallazgos bloqueantes. R1–R11 quedan cubiertos y T1–T13 están
completas.

## Precondiciones y alcance

- Revisé la spec aprobada completa, `implementation.md`, `tasks.md`, las reglas
  del repositorio y todo el diff desde `a83a54b`.
- `test-identities` continúa siendo la única feature `in_progress`; T1–T13
  figuran `[x]` y `feature_list.json` no fue alterado por esta revisión.
- Audité código y pruebas reales sin confiar en el resumen del implementer.
- Esta revisión solo modifica este archivo.

## Cierre de REV-18

`appendIdentityPreIO` centraliza los cuatro appends anteriores a I/O:
`policy_evaluated`, `budget_reserved`, `retry_scheduled` y
`execution_started`. Ante cancelación o deadline llama a
`closeCancelledIdentityAttempt`; otros errores operacionales se devuelven sin
inventar un terminal de cancelación. El cierre usa un contexto independiente,
acotado por `TerminalAppendTimeout`, y une la causa original con un eventual
fallo del append terminal.

La matriz comprometida reproduce cancelación y deadline en policy y budget del
intento inicial, policy, budget y retry marker del segundo intento, y
`execution_started` inicial. En todos los casos comprobé:

- prefijo durable exacto hasta el último evento confirmado;
- un solo terminal `cancelled` con causa coherente;
- `errors.Is` para la causa original y, cuando corresponde, para el fallo de
  cierre unido;
- cero efectos nuevos de tool, resolver y red;
- ningún evento posterior al terminal;
- aceptación del package reader para los prefijos legítimos y rechazo de
  secuencias fabricadas fuera de la máquina de estados.

Además ejecuté una sonda efímera con `go test -overlay` para cubrir
`execution_started` del segundo intento, tanto con cancelación como con
deadline, 30 veces. El primer intento produjo exactamente un efecto
tool/resolver/red; el segundo conservó el prefijo
policy→budget→retry→cancelled, no añadió efectos y mantuvo los terminales
`transient` y `cancelled` en sus ordinales respectivos. El overlay se eliminó y
no dejó temporales en el workspace.

## Estado de REV-1–REV-18

| Hallazgo | Estado | Evidencia independiente |
|---|---|---|
| REV-1 | cerrado | Schema v2 referenciado falla antes de efectos; compatibilidad v1 conserva validación estricta. |
| REV-2 | cerrado | Identity, target y policy se revalidan antes de cada intento y antes de resolver/DNS/dial. |
| REV-3 | cerrado | Factory productiva y verticales API/CLI/MCP/GitHub comparten snapshot, binding, outcome y códigos. |
| REV-4 | cerrado | Lease se libera exactamente una vez ante éxito, error y cancelación concurrente. |
| REV-5 | cerrado | Observer terminal permanece acotado en success, error y cancelación. |
| REV-6 | cerrado | Deep copy, digest y sello atraviesan admission, approval, recovery y pre-I/O, incluida mutación concurrente. |
| REV-7 | cerrado | Una policy efectiva de redacción se aplica antes de planning, assertions y runtime. |
| REV-8 | cerrado | Formato y comentarios sistémicos permanecen corregidos. |
| REV-9 | cerrado | Retry reserva tras identity+policy y antes de resolver; deny/drift no consume retry ni produce I/O. |
| REV-10 | cerrado | Todo intento autenticado termina con cardinalidad y categoría válidas, incluidos rechazos y cancelaciones pre-I/O. |
| REV-11 | cerrado | Shared ref funciona; mappings faltantes o sobrantes fallan cerrado. |
| REV-12 | cerrado | Rotación y recovery productivos resuelven material fresco y conservan journal append-only sin secretos. |
| REV-13 | cerrado | Presence conjunta, match ID/digest, ordinal, cardinalidad y outcome allowlist fallan cerrado. |
| REV-14 | cerrado | Helpers, entidades y fixtures nuevos o modificados tienen comentario sistémico. |
| REV-15 | cerrado | Tuple obligatorio/prohibido por tipo y step, y terminal único por intento, se validan. |
| REV-16 | cerrado | Orden de fases, concordancia de outcome y prohibición de eventos post-terminal se validan por intento. |
| REV-17 | cerrado | Cancelación tras budget/retry y durante started cierra el intento sin fases posteriores. |
| REV-18 | cerrado | Policy, budget, retry y started usan reconciliación común para cancel/deadline y preservan el prefijo confirmado. |

## Trazabilidad R1–R11

| Requirement | Estado | Evidencia |
|---|---|---|
| R1 | cubierta | v2/v1, union fields, refs compartidas y mappings exactos pasan. |
| R2 | cubierta | Selección y binding exactos, incluido manifest dinámico, fallan cerrado ante drift. |
| R3 | cubierta | Descriptor mínimo, deep copy, digest, sellado y mutación/race pasan. |
| R4 | cubierta | Plan/identity/target/policy/budget preceden resolver/DNS/dial; credenciales solo llegan al transporte. |
| R5 | cubierta | Vigencia, aislamiento, lease/release, cancelación, retry y rotación pasan. |
| R6 | cubierta | Redacción monotónica fail-closed cubre journal, evidence, report, superficies y archivos. |
| R7 | cubierta | Redirect guard y errores operacionales no generan falso `functional failed`. |
| R8 | cubierta | Retry, redelivery y `Service.Recover` real resuelven material fresco sin duplicar efectos. |
| R9 | cubierta | API/CLI/MCP/GitHub comparten snapshot autenticado, identity/binding/outcome/código y contadores. |
| R10 | cubierta | Eventos seguros, ordinales, categorías y terminal único se validan en éxito, error, rechazo, retry y cancelación. |
| R11 | cubierta | Todas las carreras cancel/deadline pre-I/O se reconcilian sin tool/resolver/red posteriores. |

## Verificación ejecutada

| Comando o comprobación | Resultado |
|---|---|
| `gofmt -l .` | pass, salida vacía. |
| `git diff --check` | pass; únicamente avisos locales LF/CRLF. |
| `go vet ./...` | pass. |
| `go test ./... -run '^$'` | pass. |
| Matriz focal REV-18 de planning ×30 | pass. |
| Reader de prefijos cancelados y adversarios rehasheados ×20 | pass. |
| Planning/workflow/paridad/recovery/rotación focal `-count=10` | pass. |
| `go test ./... -count=1` | todos los paquetes pasan salvo el subtest histórico Windows `evidence_symlink`, que falla por privilegio de creación de symlink. |
| Race UCRT64 de testidentity/planning/validationworkflow/cmd y localvalidation focal ×5 | pass. |
| Coverage focal | testidentity 91,9 %, tools/http 91,1 %, agentplanning 87,3 %, planning 89,1 %, planmanifest 91,8 %, specification 95,5 %, yaml 87,3 %, evidence 90,2 %, validationworkflow 85,6 %; total 88,4 %. |
| Cross-build Linux/Darwin amd64 con `CGO_ENABLED=0 go build ./...` | pass. |
| Sonda retry-start con `go test -overlay -count=30` | pass para cancel y deadline, prefijo y efectos exactos. |

La limitación histórica de symlink fue reproducida y no afecta al veredicto: es
el único fallo del gate global y depende del privilegio local de Windows. El
scan final no encontró material de credencial ni `credential_ref` durable en
modelo, plan, journal, evidence, report o archivos generados. Los canarios solo
aparecen en tests adversarios. No quedaron overlays, perfiles, binarios ni
temporales, y el diff conserva comentarios sistémicos en toda entidad, función
y helper nuevo o modificado.
