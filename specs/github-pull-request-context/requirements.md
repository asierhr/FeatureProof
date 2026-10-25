# Contexto acotado de pull requests de GitHub — Requirements

## Objetivo y problema

La integración GitHub existente autentica solicitudes, autoriza una pull request,
liga el run a su `head` y publica resultados. El `ContextAssembler` existente
puede admitir un diff neutral ya autorizado, pero todavía no existe una frontera
que obtenga de GitHub el título, descripción, archivos y fragmentos de diff, los
normalice de forma segura y demuestre qué observación exacta recibió el agente.

Esta feature añade una adquisición read-only, determinista y acotada del contexto
de una pull request autorizada. La observación queda sellada al repositorio,
número, `head` y `base`; separa configuración confiable de contenido externo;
declara toda omisión; extrae restricciones únicamente mediante extractores
versionados configurados; identifica contratos OpenAPI afectados sin checkout ni
ejecución; y alimenta el productor neutral de diff de la feature 29.

## Alcance

- Obtener mediante credenciales de instalación existentes la identidad estructural,
  título, cuerpo raw, `head` completo, `base` completo y colección paginada de
  archivos de una pull request previamente autorizada.
- Normalizar cada archivo a una unidad canónica con path actual/anterior, estado,
  conteos y fragmento textual de patch permitido.
- Aplicar límites confiables de páginas, archivos, bytes de metadata, bytes por
  patch, bytes totales, hunks, líneas e intentos, declarando omisiones cerradas.
- Filtrar paths, estados y tipos mediante política aprobada antes de proyectar
  contenido al ensamblador.
- Extraer restricciones solo desde una sección Markdown exacta o una referencia
  de catálogo, ambas con kind y versión fijados por configuración confiable.
- Seleccionar referencias seguras a contratos OpenAPI afectados a partir de
  rutas configuradas y archivos observados.
- Sellar la observación y su política, producir bindings revalidables y detectar
  cambios antes de generación, aprobación, ejecución y recovery.
- Integrar la proyección como `SourceDiff` y las restricciones autorizadas con
  `ContextAssembler`, manteniendo todo texto de GitHub en `untrusted_data`.
- Reutilizar autenticación, retry, rate-limit y transporte hermético de la
  integración GitHub existente sin exponer credenciales al workflow o modelo.

## Fuera de alcance

- Recibir webhooks, interpretar comandos, autorizar actores/forks/selections,
  crear runs o publicar checks/comentarios; pertenece a `github-pr-integration`.
- Crear, reservar, atestar o liberar entornos de validación; corresponde a la
  feature 31.
- Hacer clone, checkout, fetch de Git, montar un workspace, ejecutar hooks,
  compilar código o interpretar instrucciones contenidas en el repositorio.
- Obtener archivos arbitrarios mediante URLs devueltas por GitHub, seguir
  `raw_url`, `blob_url`, `contents_url` o resolver submódulos/LFS.
- Interpretar el diff mediante un modelo para conceder autoridad, elegir
  herramientas, aprobar planes, decidir assertions o calcular veredictos.
- Descargar o expandir patches binarios, contenido completo de archivos,
  archivos fuera de paths configurados o contratos no afectados.
- Administrar la configuración versionada del proyecto; la feature 36 aportará
  esa configuración. Esta feature recibe una política ya aprobada y su digest.
- Añadir permisos de escritura, credenciales nuevas o llamadas reales a GitHub
  dentro de `go test`.

## Actores y términos

- **Authorized PR identity:** instalación, repositorio base, número y política de
  fork ya validados por la integración GitHub; no procede del texto de la PR.
- **Context policy:** configuración confiable y versionada que fija extractor,
  catálogo de referencias, rutas OpenAPI, allowlists y límites.
- **Coherent observation:** metadata leída antes y después de los archivos con
  la misma identidad, `head`, `base` y campos contextuales; cualquier drift
  invalida el intento completo.
- **PR context:** modelo interno canónico con metadata proyectada, archivos,
  omisiones, restricciones, contratos afectados, bindings y digests.
- **File projection:** unidad atómica de un archivo cambiado. Su patch es texto
  externo no confiable y puede estar ausente o truncado explícitamente.
- **Constraint extractor:** parser determinista registrado por kind/version que
  reconoce una sección exacta o una referencia exacta; nunca ejecuta Markdown.
- **Catalog reference:** identificador opaco presente en el cuerpo y resuelto
  solo contra un catálogo confiable de restricciones ya configurado.
- **Affected contract:** ruta OpenAPI configurada cuyo path actual o anterior
  aparece en la colección observada; es una referencia segura, no el body del
  contrato.
- **Omission:** razón tipada y content-free que explica por qué una unidad o parte
  de ella no se proyectó.
- **Stale:** binding cuya identidad, `head`, `base`, policy digest o observation
  digest ya no coincide con la autoridad actual.

## R1 — Identidad autorizada de la adquisición

WHEN una solicitud autorizada requiere contexto de pull request, THE context
acquisition service SHALL aceptar exclusivamente la instalación, repositorio
base, número, `head` esperado y policy digest aportados por composición y SHALL
rechazar identidades, repositorios, refs o políticas aportadas por el modelo o
por el texto de GitHub.

### Acceptance criteria

- GIVEN una identidad autorizada exacta WHEN comienza la adquisición THEN solo
  se consulta esa instalación, repositorio y pull request.
- GIVEN repository, PR, expected head o policy digest vacío/no canónico WHEN se
  valida THEN no se llama al gateway.
- GIVEN título, cuerpo o patch que intenta declarar otro repositorio, ref,
  extractor, límite o contrato WHEN se procesa THEN permanece como texto inerte
  y no modifica la autoridad.

## R2 — Observación coherente y ligada a revisión

WHEN se adquiere contexto, THE service SHALL leer metadata antes y después de la
colección de archivos y SHALL publicar una observación únicamente si repository,
PR, state, `head` OID completo, `base` OID completo y campos contextuales
normalizados permanecen idénticos durante el intento.

### Acceptance criteria

- GIVEN una PR abierta y estable WHEN termina la lectura THEN el snapshot contiene
  OIDs completos de head/base y un observation digest verificable.
- GIVEN cambio de head, base, título, cuerpo, estado, repositorio o número entre
  ambas lecturas WHEN se compara THEN el intento se descarta completo y no llega
  al ensamblador.
- GIVEN una PR cerrada o una respuesta cuya identidad no coincide WHEN se valida
  THEN el resultado es un error tipado de integridad/estado.

## R3 — Modelo canónico de metadata y archivos

THE feature SHALL representar `PullRequestContext/v1` mediante tipos internos
estrictos, inmutables al cruzar fronteras, con orden canónico, digests sobre bytes
canónicos y sin DTOs, URLs navegables o tipos del SDK de GitHub fuera del adapter.

### Acceptance criteria

- GIVEN los mismos valores en distinto orden de páginas WHEN se sellan THEN se
  obtiene el mismo orden y digest.
- GIVEN campo desconocido, enum desconocido, path no canónico, digest alterado,
  duplicado ambiguo o mutación posterior del input WHEN se verifica THEN se
  rechaza o no altera el snapshot ya sellado.
- GIVEN URLs `raw/blob/contents` en la respuesta WHEN se traduce THEN no aparecen
  en el modelo interno, journal, contexto ni llamadas posteriores.

## R4 — Separación irreversible de confianza

THE feature SHALL mantener policy, perfiles, límites, extractor y catálogo como
autoridad de composición, SHALL clasificar título, descripción, constraints,
paths, patches y metadata textual de GitHub como `untrusted_data`, y SHALL NOT
convertir contenido externo en instrucciones o configuración aprobada.

### Acceptance criteria

- GIVEN prompt injection en título, cuerpo, filename o patch WHEN se construye el
  envelope THEN el texto permanece en items `untrusted_data` tipados.
- GIVEN un heading que imita configuración o instrucciones WHEN se procesa THEN
  no sustituye el extractor, policy, perfiles ni instrucciones del sistema.
- GIVEN el snapshot de modelo WHEN se inspecciona THEN no existe un payload legacy
  paralelo que pierda trust labels.

## R5 — Extracción explícita y versionada de restricciones

WHERE la policy habilita restricciones desde la PR, THE extractor registry SHALL
usar exactamente el kind/version configurado y SHALL aceptar solo una sección
Markdown con marcador exacto o una referencia de catálogo con sintaxis exacta;
todo el resto del cuerpo SHALL permanecer como descripción no confiable.

### Acceptance criteria

- GIVEN `markdown_section/v1` y una única sección exacta dentro de límites WHEN se
  extrae THEN cada restricción conserva ID determinista, orden y referencia a la
  sección de origen.
- GIVEN `catalog_reference/v1` y un alias/version configurado WHEN se resuelve THEN
  se usan únicamente constraints del catálogo confiable ligado a su digest; el
  cuerpo no define su contenido.
- GIVEN sección duplicada, anidada de forma ambigua, versión desconocida, alias no
  configurado, unidad oversized o sintaxis inválida WHEN se extrae THEN falla
  cerrado o produce `insufficient_context` según la policy, sin inferencia libre.
- GIVEN texto fuera de la sección exacta WHEN se procesa THEN nunca se convierte
  en constraint aunque use lenguaje imperativo.

## R6 — Límites y omisiones explícitas

THE acquisition SHALL aplicar antes de persistencia o modelo límites positivos y
finitos para respuesta, páginas, archivos, title/body, patch por archivo, patch
total, hunks, líneas, items y duración; SHALL truncar solo en fronteras semánticas
permitidas y SHALL registrar toda exclusión mediante motivos cerrados y counts.

### Acceptance criteria

- GIVEN exactamente el límite WHEN se procesa THEN la unidad cabe completa sin
  marcarse omitida.
- GIVEN exceso de archivos/páginas/bytes/hunks/líneas WHEN se procesa THEN no se
  excede el presupuesto y el manifest declara clase, count y motivo de omisión.
- GIVEN un hunk o constraint que no cabe completo WHEN se empaqueta THEN se omite
  entero; no se corta JSON, UTF-8, líneas o estructura Markdown a mitad.
- GIVEN lista incompleta por límite local o máximo documentado del proveedor WHEN
  se seleccionan contratos THEN la completitud queda `partial/insufficient`; no
  se afirma que la lista sea completa.

## R7 — Paths, tipos y patches permitidos

WHEN se proyectan archivos, THE service SHALL validar path actual/anterior,
status, conteos y patch contra allowlists configuradas; SHALL omitir binarios,
submódulos, tipos no autorizados, paths fuera de scope y patches ausentes sin
descargar contenido alternativo.

### Acceptance criteria

- GIVEN archivo textual permitido con patch válido WHEN se proyecta THEN conserva
  path, status, hunks acotados, digest y referencia segura.
- GIVEN rename WHEN se procesa THEN path actual y anterior se canonicalizan y
  participan en scope/contratos sin tratar uno como URL.
- GIVEN patch ausente, `binary`, filename traversal/absolute/device, conteos
  negativos/inconsistentes, duplicado o status desconocido WHEN se valida THEN no
  se expone contenido y queda error u omisión tipada según corresponda.
- GIVEN `contents_url`, `raw_url` o un link en el patch WHEN se observa THEN el
  sistema no lo sigue.

## R8 — Selección determinista de contratos OpenAPI afectados

WHERE la policy contiene rutas OpenAPI configuradas, THE selector SHALL derivar
los contratos afectados solo de paths actuales/anteriores ya observados y la
tabla confiable path→contract ID/version, sin ejecutar código, hacer checkout,
leer paths adicionales ni usar similitud o instrucciones del diff.

### Acceptance criteria

- GIVEN un path exacto configurado añadido/modificado/renombrado/eliminado WHEN se
  selecciona THEN se emite una referencia segura con contract ID/version, path,
  status y head OID.
- GIVEN archivos no configurados o texto que menciona un contrato WHEN se evalúa
  THEN no se selecciona contrato.
- GIVEN varios archivos del mismo contrato WHEN se reduce THEN se deduplican por
  identidad y conservan todos los paths causales en orden estable.
- GIVEN lista parcial o contrato eliminado WHEN se proyecta THEN se declara la
  condición; no se inventa body ni se marca completo.

## R9 — Proyección mínima hacia ContextAssembler

WHEN la observación supera validación y secret gate, THE adapter SHALL producir
items neutrales `SourceDiff` y constraints autorizadas con IDs, procedencia,
autoridad, safe references, completeness y binding, y SHALL entregar a cada rol
solo campos admitidos por su perfil y presupuesto.

### Acceptance criteria

- GIVEN planner WHEN ensambla THEN recibe únicamente metadata mínima, constraints,
  archivos/hunks permitidos, omisiones y contract refs necesarios.
- GIVEN analyzer o diagnostic WHEN consume contexto THEN no recibe automáticamente
  diff completo, body completo, credenciales ni campos exclusivos del planner.
- GIVEN fuente partial/insufficient WHEN se ensambla THEN manifests y outcome lo
  reflejan sin fallback al payload GitHub original.
- GIVEN mismo contexto/policy WHEN entra por CLI/API/MCP/GitHub THEN el productor
  neutral genera el mismo digest bajo la misma autoridad.

## R10 — Revalidación antes de efectos

WHEN existe un binding de contexto GitHub, THE validation workflow SHALL consultar
la autoridad actual y comparar repository, PR, head, base, policy y observation
digests antes de generar o regenerar plan, exponer approval, ejecutar y recuperar;
IF cualquiera cambia, THEN SHALL marcar contexto `stale` y SHALL NOT llamar al
modelo ni a tools después del checkpoint que detecta drift.

### Acceptance criteria

- GIVEN head/base/body/title/files/policy/catalog/extractor drift WHEN se revalida
  THEN el binding queda stale con motivo seguro.
- GIVEN drift antes de approval o execution WHEN se detecta THEN no se aprueba ni
  ejecuta el plan previo y no existe fallback al snapshot histórico.
- GIVEN restart con composición nueva WHEN se revalida THEN se consultan gateway,
  policy y catálogo actuales, no producers/authority suministrados por el caller.
- GIVEN solo cambia un índice derivado no autoritativo WHEN se revalida THEN no se
  invalida por ese cambio.

## R11 — Credenciales, secretos y redacción

THE GitHub adapter SHALL resolver tokens de instalación solo en composición y
solo para el repositorio/permisos read-only necesarios; THE acquisition SHALL
aplicar secret detection/redaction antes de persistir o enviar contenido y SHALL
NOT exponer tokens, headers, cuerpos remotos completos o canarios en modelos,
errores, eventos, metrics, journal o reports.

### Acceptance criteria

- GIVEN inspección de core/workflow/context snapshot WHEN se revisa THEN no contiene
  `CredentialSource`, token, Authorization header ni capability HTTP.
- GIVEN secreto/canario anidado en title/body/path/patch/constraint WHEN se escanea
  THEN falla cerrado o redacta la unidad según policy antes de ranking/store/model.
- GIVEN detector cancelado o fallido WHEN se invoca THEN el contenido no cruza la
  frontera.
- GIVEN fake transport WHEN audita requests THEN usa únicamente endpoints GET y
  permisos read-only ya autorizados para la instalación/repositorio exactos.

## R12 — Paginación, retries y rate limits acotados

IF GitHub pagina, limita o falla transitoriamente, THEN THE acquisition SHALL
seguir solo páginas construidas localmente para el endpoint autorizado, reintentar
la observación read-only completa con attempt/duration budgets, respetar
`Retry-After`/rate-reset dentro del deadline y terminar con error tipado o contexto
insuficiente al agotar límites.

### Acceptance criteria

- GIVEN múltiples páginas válidas WHEN se leen THEN se consumen en serie, se
  deduplican y se termina solo al confirmar fin o límite explícito.
- GIVEN Link externo/malformado, página repetida/vacía con next o más del máximo
  WHEN se valida THEN no se sigue la URL y se falla/declara partial de forma segura.
- GIVEN `408`, `429`, `5xx` o transport failure retryable WHEN queda presupuesto
  THEN se espera mediante clock/context controlado y se reinicia la observación
  coherente; errores permanentes no se reintentan.
- GIVEN Retry-After/reset posterior al deadline WHEN se calcula THEN no se duerme
  fuera del presupuesto y no se inicia otra llamada.

## R13 — Cancelación, idempotencia y concurrencia

THE acquisition SHALL propagar `context.Context` por token, HTTP, paginación,
retry, extracción, redacción, selección y ensamblado; SHALL finalizar todo trabajo
propio antes de retornar; y SHALL make redelivery of the same authorized identity,
expected head and policy idempotent without sharing mutable state across runs.

### Acceptance criteria

- GIVEN cancelación/timeout en cada fase WHEN ocurre THEN no comienzan nuevas
  llamadas, el error conserva causa/categoría y no quedan goroutines/timers activos.
- GIVEN redelivery idéntica y observación estable WHEN se repite THEN produce el
  mismo digest/receipt sin duplicar efectos; las llamadas GET repetidas no mutan
  GitHub.
- GIVEN misma idempotency key con identidad/policy distinta WHEN se usa THEN se
  rechaza como conflicto.
- GIVEN adquisiciones concurrentes independientes o de la misma PR WHEN se ejecutan
  bajo `-race` THEN progresan/convergen sin locks durante red, disco o timers.

## R14 — Errores y observabilidad segura

THE feature SHALL distinguir invalid input, unauthorized, stale/conflict,
integrity, partial/insufficient, rate-limited, transient, cancelled, timeout and
internal failures; SHALL emitir un terminal record exactamente una vez con IDs,
digests, counts, duración, intentos y omission reasons allowlisted, sin contenido
externo ni secretos.

### Acceptance criteria

- GIVEN cada clase de fallo WHEN cruza una superficie THEN conserva categoría y
  causa comprobables sin decidir por texto.
- GIVEN fallo operativo de GitHub WHEN se proyecta THEN no se presenta como fallo
  funcional de la feature validada.
- GIVEN éxito, stale, insufficient, cancelación o error WHEN termina THEN existe
  un único record terminal con repository/PR/head/policy/observation digests y
  counts, pero sin title/body/path/patch/constraint bodies.
- GIVEN fallo/cancelación del observer WHEN ocurre THEN no cambia el binding ni
  duplica adquisición/model/tools.

## R15 — Verificación hermética y trazabilidad

THE implementation SHALL mantener trazabilidad `R→task→test`, cubrir código
nuevo/modificado al menos 85% statement coverage sin redondear e incluir tests
herméticos con fake GitHub para contexto válido, truncado, head/base drift, diff
malicioso, constraint extraction, contrato afectado, paginación, rate limit,
cancelación, idempotencia, concurrencia y ausencia byte-for-byte de secretos.

### Acceptance criteria

- GIVEN `go test` WHEN se ejecuta THEN no usa GitHub real, credenciales reales,
  checkout, modelo o red externa.
- GIVEN suites normal/repetida/race WHEN se ejecutan THEN orden, retry, redelivery,
  cancellation y concurrencia son deterministas sin `time.Sleep` como coordinación.
- GIVEN build tags/plataformas afectadas WHEN se verifica THEN compilan y toda
  lógica aplicable entra en el cálculo de coverage.
- GIVEN una entidad, constante, variable, función, método o helper de test nuevo o
  modificado WHEN se audita THEN posee un comentario sobre su responsabilidad
  dentro del sistema.

## Errores y casos límite

- Un OID abreviado, cero IDs, PR no abierta, base/head nulos, path no canónico,
  enum desconocido, count negativo, UTF-8 inválido o digest inconsistente falla
  antes de producir contexto.
- Los límites del proveedor no se interpretan como completitud. Si no puede
  demostrarse el final de la colección, se declara `partial/insufficient`.
- Un patch ausente no se recupera desde otra URL. Binarios, submódulos y LFS se
  registran como omitidos sin descargar bytes.
- Un rename evalúa ambos paths; un delete puede afectar un contrato, pero no
  aporta contenido actual inventado.
- Title/body edits con el mismo head invalidan el observation digest. Base drift
  con head estable también invalida el diff.
- La redelivery tras respuesta perdida solo reutiliza un resultado si identidad,
  head, base, policy y digest coinciden y su confirmación durable es verificable.
- Cancelación y deadline conservan `context.Cause`; rate limit no se confunde con
  timeout ni con fallo funcional.

## Decisiones abiertas

No hay decisiones de producto abiertas para iniciar la implementación. Los
valores numéricos concretos de límites y los paths/aliases de cada proyecto se
resuelven mediante configuración confiable; los tests cubren fronteras exactas.
La recolección de objetos huérfanos o caches derivados puede aplazarse siempre que
no afecte autoridad, addressability, secretos ni comportamiento observable.

## Aprobación humana

```yaml
status: pending
approved_by:
approved_at:
```

La implementación no puede comenzar hasta que una persona apruebe explícitamente
esta especificación y el leader registre la transición a `in_progress`.
