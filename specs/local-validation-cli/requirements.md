# CLI local de validación — Requirements

## Objetivo y problema

FeatureProof necesita un punto de entrada local que conecte las capacidades ya
implementadas y produzca un resultado auditable sin depender de un modelo real.
El comando debe cargar una specification, un manifiesto determinista de planes
y configuración, fijar explícitamente el target autorizado, ejecutar todas las
constraints y publicar un bundle autocontenido de artefactos.

## Alcance

- Añadir `featureproof validate --spec <ruta> --plan <ruta> --target <URL>`.
- Definir y cargar un manifiesto JSON versionado de planes, assertions y
  configuración runtime explícita.
- Validar argumentos, specification, manifiesto, configuración, relaciones y
  planes completos antes de crear el run o producir efectos externos.
- Sustituir `target.base_url` por `--target` conservando `target.id`.
- Ejecutar un plan por cada constraint, secuencialmente y en el orden de la
  specification, usando el executor y assertion engine existentes.
- Agregar resultados de constraints y del run de forma determinista.
- Mostrar progreso y resúmenes sin payloads sensibles.
- Publicar atómicamente los artefactos bajo `.featureproof/runs/<run-id>`.
- Propagar señales del terminal y devolver códigos de salida estables.
- Verificar el flujo con filesystem, reloj, IDs, señales y red local controlados.

## Fuera de alcance

- Modificar el contrato YAML v1 de specification.
- Generar, reparar o interpretar planes mediante un modelo.
- Añadir otras tools, credenciales, aprobación interactiva o prompts.
- Ejecutar constraints o pasos en paralelo, reanudar runs o sobrescribirlos.
- Convertir la CLI en daemon, API o integración de CI/GitHub.
- Ofrecer formatos configurables para stdout, reportes o artefactos.

## Actores y términos

- **Operador:** persona o automatización que invoca la CLI.
- **Plan manifest:** documento JSON v1 confiable recibido por `--plan`.
- **Effective target:** target con el ID del YAML y la URL exacta de `--target`.
- **Run bundle:** directorio terminal publicado como una unidad.
- **Staging bundle:** directorio hermano `<run-id>.incomplete` que todavía no es
  un run válido.
- **FeatureProof error:** error de argumentos, configuración, carga,
  validación, composición, ejecución interna o persistencia; no es un
  incumplimiento funcional.

## Contrato externo del manifiesto v1

El archivo de `--plan` es un único objeto JSON UTF-8, sin contenido posterior y
con claves desconocidas prohibidas:

```json
{
  "version": 1,
  "runtime": {
    "http": {
      "allowed_methods": ["GET"],
      "allowed_request_headers": [],
      "allowed_response_headers": ["Content-Type"],
      "follow_redirects_for_methods": [],
      "max_redirects": 0,
      "timeout_ms": 5000,
      "max_url_bytes": 2048,
      "max_request_header_bytes": 8192,
      "max_request_body_bytes": 65536,
      "max_response_header_bytes": 8192,
      "max_response_body_bytes": 65536
    },
    "redaction": {
      "max_body_bytes": 65536,
      "additional_sensitive_headers": [],
      "sensitive_json_paths": [],
      "model_sensitive_json_paths": []
    },
    "policy": { "allowed_tools": ["http.request"] }
  },
  "plans": []
}
```

`plans` contiene exactamente un plan por cada constraint de la specification,
en el mismo orden. Cada plan serializa el contrato aprobado de
`planning.ValidationPlan`: `id`, `constraint_id` y `steps`; cada step contiene
`id`, `tool`, `input`, `assertions` y `depends_on`. Un input literal se expresa
como `{"literal": <valor JSON>}` y una referencia como
`{"reference":{"step_id":"...","path":[{"field":"..."}|{"index":0}],"value_type":"..."}}`;
objetos y arrays usan respectivamente `{"object":{...}}` y
`{"array":[...]}`. Cada nodo tiene exactamente una variante. Una assertion es
`{"id":"...","definition":{...}}` y conserva el lenguaje aprobado del
assertion engine.

Todas las listas y límites runtime son explícitos. Las listas pueden estar
vacías solo donde el contrato de la tool lo permita. No existen defaults de
producto; únicamente se mantienen los cuatro headers sensibles protegidos por
el contrato previo de redacción. El único tool soportado por esta versión es
`http.request`, debe aparecer una vez en `allowed_tools`, y cualquier otro
nombre se rechaza.

## R1 — Comando y argumentos exactos

WHEN an operator invokes `featureproof validate`, THE CLI SHALL require exactly
one non-empty value for `--spec`, `--plan` and `--target`, SHALL reject unknown
or repeated options and positional arguments, and SHALL provide diagnostics on
stderr without starting a run.

### Acceptance criteria

- GIVEN los tres flags una vez en cualquier orden WHEN se parsean THEN se
  obtiene una solicitud completa sin leer aún archivos ni red.
- GIVEN un flag ausente, vacío, duplicado, desconocido o un positional WHEN se
  invoca THEN retorna `3`, explica la opción afectada en stderr y no crea
  `.featureproof/runs`.
- GIVEN otro subcomando o ausencia de subcomando WHEN se invoca THEN retorna
  `3`, muestra uso seguro y no inicia un run.

## R2 — Target explícito y único

WHEN the inputs are valid, THE CLI SHALL replace `target.base_url` with the
exact `--target` value while preserving `target.id`, and SHALL validate the
effective target before run creation or network access.

### Acceptance criteria

- GIVEN una URL válida distinta de la del YAML WHEN se compone THEN todas las
  requests se acotan a la URL del flag y ningún componente usa la anterior.
- GIVEN esquema, autoridad, puerto o path inválido WHEN se valida THEN retorna
  `3` antes de crear run, resolver DNS o abrir sockets.
- GIVEN target con userinfo, query o fragment WHEN se compone `http.request`
  THEN se rechaza mediante el contrato existente de la tool.

## R3 — Manifiesto versionado y configuración previa

WHEN a plan manifest is loaded, THE CLI SHALL decode JSON numbers exactly,
reject unknown versions, fields, duplicate keys, trailing content and invalid
runtime configuration, and SHALL complete all specification, manifest,
redaction, policy and plan validation before creating a run.

### Acceptance criteria

- GIVEN un manifiesto v1 válido WHEN se carga THEN se traduce a modelos
  internos sin exponer tipos del decoder y sin perder `json.Number`.
- GIVEN versión distinta de 1, JSON inválido, key duplicada/desconocida,
  configuración ausente/inválida o plan inválido WHEN se valida THEN retorna
  `3` con categoría y field path seguros, sin run, DNS, tool ni artefactos.
- GIVEN configuración válida WHEN se compone THEN `http.request`, redacción y
  policy usan exactamente los permisos y límites declarados.

## R4 — Correspondencia total y flujo determinista

WHEN specification and manifest are valid, THE CLI SHALL require exactly one
plan for each constraint in specification order and SHALL execute those plans
sequentially in that order without consulting a model.

### Acceptance criteria

- GIVEN un plan ausente, duplicado, extra, reordenado o con constraint ID
  distinto WHEN se valida THEN se rechaza todo antes del run.
- GIVEN inputs válidos WHEN se ejecutan repetidamente con los mismos doubles
  THEN constraints, steps, events y resultados mantienen el mismo orden.
- GIVEN un resultado no passing en una constraint WHEN quedan constraints
  posteriores THEN estas también se ejecutan salvo cancelación o error interno.

## R5 — Identidad, fases y progreso seguro

WHEN validation starts, THE CLI SHALL generate one non-empty run ID, print it
once and emit ordered phase and constraint summary lines containing only IDs,
statuses and evidence IDs from treated persisted records.

### Acceptance criteria

- GIVEN validación aceptada WHEN comienza THEN stdout inicia con
  `run_id=<id>` y después emite `phase=executing` y `phase=reporting` en orden
  observable. La validación previa no se presenta como un run iniciado.
- GIVEN una constraint terminal WHEN se resume THEN la línea contiene
  `constraint_id`, `status` y sus `evidence_ids` ordenados, sin input, output,
  URL, headers, body, expected u observed.
- GIVEN secretos canario en configuración, request o response WHEN se escanean
  stdout, stderr y artefactos THEN no aparece ningún valor sin tratar.

## R6 — Resultado de constraints y run

WHEN all plans reach functional completion, THE CLI SHALL derive each
constraint status from its plan and aggregate the run as `failed` if any
constraint failed, `passed` if all passed, and `inconclusive` otherwise,
preserving the ordered evidence IDs that support each result.

### Acceptance criteria

- GIVEN steps passed/failed/inconclusive/blocked WHEN se genera el resultado
  THEN el estado de constraint coincide con el plan y referencia solo evidence
  IDs persistidos del mismo run.
- GIVEN una mezcla de constraints WHEN se agrega THEN `failed` tiene
  precedencia, todos passed produce passed y cualquier resto produce
  inconclusive.
- GIVEN una afirmación externa o texto descriptivo WHEN se calcula THEN no
  puede modificar el resultado determinista.

## R7 — Bundle terminal completo y atómico

WHEN a run reaches a terminal state, THE CLI SHALL stage and publish atomically
one bundle containing `run.json`, `events.jsonl`, `plan.json`, `evidence/`,
`report.json` and `report.md` under `.featureproof/runs/<run-id>`.

### Acceptance criteria

- GIVEN finalización funcional WHEN se publica THEN todos los JSON son válidos,
  cada línea de events es un evento, evidence contiene un JSON por evidence ID,
  y las referencias del reporte resuelven dentro del bundle.
- GIVEN `run.json` WHEN se lee THEN contiene ID, effective target ID y URL,
  estado, límites/consumo, tiempos y constraints ordenadas; `plan.json`
  contiene el manifiesto validado tratado que se ejecutó.
- GIVEN `report.json` y `report.md` WHEN se comparan THEN expresan los mismos
  estados y evidence IDs; Markdown no incorpora payloads de evidencia.
- GIVEN el directorio final o staging del ID ya existe WHEN se prepara THEN
  falla sin borrar, truncar ni sobrescribir contenido.

## R8 — Fallos de persistencia sin publicación parcial

IF artifact encoding, writing, syncing or final rename fails, THEN THE CLI SHALL
return a FeatureProof error, SHALL NOT expose a final run directory, and SHALL
retain only a sibling directory whose name ends in `.incomplete` for diagnosis.

### Acceptance criteria

- GIVEN fallo inyectado en cualquier escritura WHEN termina THEN retorna `3`,
  no existe `<run-id>` y el staging no se presenta como bundle válido.
- GIVEN error antes de crear el run WHEN ocurre THEN no crea final ni staging.
- GIVEN éxito WHEN se publica THEN el rename dentro del mismo parent es el
  único punto que hace visible el bundle final y no queda staging.

## R9 — Códigos de salida y canales

WHEN the command terminates, THE CLI SHALL return `0` for all passed, `1` when
any constraint failed, `2` when none failed and any is inconclusive, `3` for a
FeatureProof error, and `130` for SIGINT or SIGTERM cancellation.

### Acceptance criteria

- GIVEN cada resultado terminal WHEN se ejecuta el command runner THEN devuelve
  exactamente el código definido y stdout termina con `result=<status>`.
- GIVEN error de argumentos, validación, composición, journal, evidence o
  publicación WHEN termina THEN usa `3`, escribe diagnóstico seguro en stderr y
  no lo presenta como failed funcional.
- GIVEN cancelación por señal WHEN termina THEN usa `130` aunque existan
  resultados funcionales previos; otras cancelaciones de contexto se tratan
  como error `3` salvo que el origen esté clasificado como señal terminal.

## R10 — Cancelación y bundle cancelled

WHILE a run is active, IF SIGINT or SIGTERM is received, THEN THE CLI SHALL
cancel the shared context, stop before starting more work, transition the run to
`cancelled` when possible and publish a complete cancelled bundle using a
non-cancelled bounded finalization context.

### Acceptance criteria

- GIVEN señal antes o durante policy, HTTP, evidence o assertions WHEN se
  propaga THEN las operaciones observan cancelación y no empieza otra
  constraint o step.
- GIVEN que journal y filesystem siguen disponibles WHEN se finaliza THEN el
  bundle publicado contiene estado `cancelled`, eventos/evidencias ya
  confirmados y reportes que no fabrican resultados pendientes.
- GIVEN que la finalización falla WHEN se cancela THEN devuelve `130`, no
  publica directorio final y conserva staging `.incomplete`.

## R11 — Tests herméticos y composición sustituible

THE CLI orchestration SHALL expose replaceable boundaries for filesystem,
signals, ID/time, DNS/dial and command output so tests can cover the complete
flow without Internet, production targets, process signals or a real model.

### Acceptance criteria

- GIVEN tools y servidores locales, IDs/reloj/señales controlados y directorio
  temporal WHEN corre la suite THEN cubre success, failed, inconclusive,
  invalid input, cancellation, collision, persistence failure and exit codes.
- GIVEN tests WHEN se ejecutan THEN no usan credenciales, Internet, modelo real
  ni `time.Sleep` para sincronización.
- GIVEN dos ejecuciones independientes WHEN usan raíces distintas THEN no
  comparten estado mutable y pasan el race detector.

## Errores y casos límite

- Rutas de spec/plan iguales son válidas solo si el contenido puede satisfacer
  ambos loaders; normalmente fallarán por contrato, sin tratamiento especial.
- Los IDs son exactos, sensibles a mayúsculas y no se normalizan.
- Un manifiesto vacío, `null`, array o con más de un valor JSON es inválido.
- El CLI no imprime el contenido de archivos ni valores rechazados en errores.
- `events.jsonl` conserva la secuencia del journal y termina en newline.
- Los evidence IDs repetidos o sin archivo verificable convierten la
  publicación en error interno.
- El Markdown escapa IDs y texto no confiable para que no cree links ni HTML
  activo; las descripciones no son necesarias en el reporte v1.
- Un status HTTP 4xx/5xx solo falla si una assertion lo determina.
- La publicación atómica se garantiza dentro del mismo filesystem del parent;
  no se copia el bundle a otro volumen.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. El formato plan v1, los
flags, precedencia del target, configuración runtime, códigos y política de
publicación fueron aprobados para esta feature. Añadir tools, formatos o
generación mediante modelo requiere otra spec/version.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-11T21:15:19+02:00
```
