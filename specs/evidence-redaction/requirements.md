# Redacción y limitación de evidencias — Requirements

## Objetivo y problema

FeatureProof debe conservar evidencias útiles y auditables sin convertir inputs,
outputs, eventos, reportes o contexto del modelo en canales de filtración. Esta
feature define una política validada antes del run, una transformación recursiva
y fail-closed, metadatos explícitos de truncación y una proyección para el
modelo que puede ocultar más información que la evidencia persistida.

## Alcance

- Validar una configuración de redacción antes de ejecutar herramientas.
- Proteger siempre headers sensibles conocidos y permitir nombres adicionales.
- Redactar rutas JSON adicionales con la sintaxis ya aprobada para assertions.
- Aplicar la transformación a inputs y outputs estructurados sin mutarlos.
- Limitar cuerpos y registrar tamaños exactos del valor recibido y conservado.
- Restringir assertions que se solapen con rutas sensibles.
- Derivar eventos, resultados, reportes y contexto únicamente de vistas tratadas.
- Permitir una vista del modelo más restrictiva que la evidencia persistida.
- Fallar de forma cerrada ante tipos, ciclos o errores de transformación.
- Probar ausencia de secretos con valores canario, sin red ni modelo real.

### Contrato de configuración

La configuración confiable de un run contiene:

| Campo | Regla |
|---|---|
| `additional_sensitive_headers` | lista opcional de nombres HTTP adicionales; se suma a los defaults y no puede eliminarlos |
| `sensitive_json_paths` | lista opcional de paths sensibles aplicada por separado a la raíz de input y output persistidos |
| `model_sensitive_json_paths` | lista opcional de paths adicionales que solo restringe la proyección enviada al modelo |
| `max_body_bytes` | entero obligatorio mayor que cero; limita cada miembro `body` encontrado recursivamente |

Los paths usan exactamente el subconjunto estricto de JSON Pointer documentado
por `assertion-engine`: `""` selecciona la raíz, los demás empiezan por `/`,
usan `~0` y `~1`, y no admiten wildcards, filtros, índices negativos ni tokens
numéricos no canónicos. Los paths se interpretan contra input y output como
raíces independientes. Un path válido ausente en un valor concreto no es error.

Los headers protegidos por defecto son `Authorization`, `Cookie`, `Set-Cookie`
y `Proxy-Authorization`. Sus nombres y los adicionales se comparan sin distinguir
mayúsculas. El marcador persistido es la string fija `[REDACTED]`; no es
configurable ni contiene información sobre el tipo o longitud del secreto.

Solo el miembro `/headers` de la raíz independiente de input y output se trata
como colección de headers de `http.request`; el nombre estructural `headers` es
exacto y sus keys de nombres HTTP se comparan sin distinguir mayúsculas. Un
objeto `headers` anidado no activa implícitamente los defaults: una herramienta
que produzca headers en otra ubicación debe declarar el subtree o sus valores
mediante `sensitive_json_paths`. Esta frontera hace que todos los paths sensibles
se conozcan antes de ejecutar el plan.

Un miembro llamado `body`, sin distinguir mayúsculas, se trata
como cuerpo y se mide sobre su representación JSON UTF-8 determinista; para una
string se miden sus bytes UTF-8 sin las comillas JSON. Si excede el máximo, se
conserva un prefijo de como máximo `max_body_bytes` que no divide una secuencia
UTF-8 y se registra metadata fuera del contenido con path, representación,
`original_bytes`, `retained_bytes` y `truncated=true`. Un cuerpo JSON compuesto
truncado se conserva como prefijo textual de su representación JSON y deja de
presentarse como JSON completo. La metadata permite distinguirlo sin interpretar
el prefijo. La redacción recursiva ocurre antes de formar ese prefijo, de modo
que un secreto anidado nunca pueda quedar incluido. `original_bytes` mide la
representación recibida antes de descartar contenido y `retained_bytes` mide la
vista tratada finalmente conservada.

Los valores tratables son los valores JSON: objeto, array, string, número finito,
booleano y null. Tipos Go ajenos al contrato, números no finitos, ciclos o una
serialización imposible son errores de redacción; nunca autorizan conservar el
valor original.

### Relación con assertions

Una assertion cuyo path coincide con una ruta sensible, es descendiente de ella
o es ancestro cuyo valor incluiría esa ruta solo puede usar `exists` o `absent`.
Esta regla se aplica a cada hoja, también dentro de grupos, y se valida antes del
run. `eq`, `gt`, `gte`, `lt` y `lte` en ese solapamiento rechazan el plan. El
valor original y el marcador nunca se usan para esas comparaciones.

Una assertion de valor sobre un cuerpo que finalmente resulte truncado produce
`inconclusive`, porque la evidencia confirmada no contiene el valor completo.
`exists` y `absent` pueden decidir la presencia del propio cuerpo. Una assertion
de valor sobre un cuerpo no truncado conserva la semántica de la feature 4.

## Fuera de alcance

- Detectar secretos por entropía, expresiones regulares o clasificación de texto.
- Cifrar evidencias, gestionar claves o crear un secret store.
- Recuperar, hashear o comparar el valor original de una ruta sensible.
- Permitir excepciones que desactiven los cuatro headers protegidos por defecto.
- Leer completamente una respuesta que una herramienta ya truncó; el tamaño
  original de esta feature es el tamaño del valor entregado al redactor.
- Cambiar la política de límites de red o memoria propia de `http.request`.
- Añadir configuración de redacción al YAML v1 o construir la CLI final.
- Publicar cuerpos completos en logs para diagnóstico.

## Actores y términos

- **Redaction policy:** configuración validada e inmutable de un run.
- **Evidence sanitizer:** componente que produce una copia tratada de input y
  output junto con metadata de truncación.
- **Persisted view:** única representación que puede entrar en `evidence.Store`.
- **Model view:** proyección derivada de la persisted view con reglas iguales o
  más restrictivas.
- **Sensitive path:** path cuyo valor debe sustituirse por `[REDACTED]`.
- **Overlap:** relación de igualdad, ancestro o descendiente entre el path de una
  assertion y una ruta sensible.
- **Canary secret:** valor único usado en tests cuya presencia en cualquier
  artefacto indica una filtración.

## R1 — Configuración válida antes del run

WHEN a run is configured, THE redaction policy SHALL validate every limit,
header name and JSON path before any model call, policy evaluation, tool call or
artifact write.

### Acceptance criteria

- GIVEN `max_body_bytes > 0`, headers válidos y paths del subset documentado
  WHEN se construye la política
  THEN se obtiene una configuración inmutable normalizada.
- GIVEN límite ausente/no positivo, header vacío/inválido, path mal escapado,
  relativo o con índice no canónico
  WHEN se valida
  THEN se devuelve error tipado con field path y no comienza el run.
- GIVEN nombres o paths repetidos tras su normalización aplicable
  WHEN se valida
  THEN se rechazan de forma determinista en vez de aplicar reglas ambiguas.

## R2 — Transformación recursiva e inmutable

WHEN structured input or output is sanitized, THE evidence sanitizer SHALL walk
all JSON objects and arrays deterministically, return an isolated treated copy
and SHALL NOT mutate or retain mutable aliases to the source.

### Acceptance criteria

- GIVEN secretos anidados en objetos y arrays de input y output
  WHEN se sanea
  THEN cada coincidencia se trata y la estructura no sensible permanece en el
  mismo orden lógico y con los mismos tipos JSON.
- GIVEN un resultado saneado que el caller modifica
  WHEN se inspecciona el original o otra ejecución del sanitizer
  THEN ninguno ha cambiado y el resultado repetido es equivalente.
- GIVEN un tipo no JSON, número no finito, ciclo o fallo de copia/serialización
  WHEN se sanea
  THEN no se devuelve vista parcial ni el valor original; se retorna un error
  de redacción distinguible.

## R3 — Headers protegidos por defecto y configurables

WHEN the root `/headers` object is encountered in input or output, THE sanitizer SHALL
replace every default or additionally configured sensitive header value with the
fixed marker using case-insensitive header-name matching.

### Acceptance criteria

- GIVEN cualquier combinación de mayúsculas de `Authorization`, `Cookie`,
  `Set-Cookie` o `Proxy-Authorization`
  WHEN aparece dentro de `/headers` en la raíz de input u output
  THEN su valor completo se sustituye por `[REDACTED]`.
- GIVEN `X-Api-Key` como header sensible adicional
  WHEN aparece dentro de `/headers` en la raíz de input u output
  THEN se redacta con la misma regla; un header no configurado se conserva.
- GIVEN una configuración que omite los defaults
  WHEN se sanea
  THEN los cuatro defaults continúan protegidos y no existe opción para
  deshabilitarlos.
- GIVEN una tool que representa headers bajo `/exchange/headers`
  WHEN esa ubicación no figura en `sensitive_json_paths`
  THEN no se presume que sea una colección HTTP; la integración se considera
  insegura hasta configurar explícitamente los paths del subtree o sus valores.

## R4 — Rutas JSON sensibles

WHEN a configured sensitive JSON path exists in an input or output root, THE
sanitizer SHALL replace the complete selected subtree with the fixed marker
without traversing or exposing its former value.

### Acceptance criteria

- GIVEN paths `""`, `/credentials/token`, `/items/0/secret` y `/a~1b/~0key`
  WHEN seleccionan valores
  THEN se redactan respectivamente raíz, keys escapadas e índice exacto.
- GIVEN un path válido que no existe o no puede atravesar el tipo observado
  WHEN se sanea
  THEN no se inventa el miembro y el resto del documento se conserva.
- GIVEN paths de persistencia y paths exclusivos del modelo
  WHEN se construye la persisted view
  THEN solo se aplican los de persistencia; la model view aplica la unión sobre
  una copia de esa vista y nunca recupera datos ya redactados.

## R5 — Assertions compatibles con datos sensibles

WHEN a plan is validated against a redaction policy, THE plan validator SHALL
allow only `exists` and `absent` for assertion leaves overlapping a sensitive
path and SHALL reject every value-reading or comparison operator before effects.

### Acceptance criteria

- GIVEN `exists` o `absent` sobre una ruta sensible, ancestro o descendiente
  WHEN se valida el plan
  THEN se acepta y después se evalúa solo la presencia sobre la vista tratada.
- GIVEN `eq`, `gt`, `gte`, `lt` o `lte` sobre el mismo solapamiento
  WHEN se valida
  THEN el plan completo se rechaza con categoría y field path estables; policy y
  tools reciben cero llamadas.
- GIVEN grupos anidados con una hoja prohibida
  WHEN se valida
  THEN se identifica la primera hoja inválida en declaration order y ni el
  secreto original ni `[REDACTED]` se comparan.

## R6 — Truncación explícita y auditable

WHEN a body representation exceeds `max_body_bytes`, THE sanitizer SHALL retain
at most the configured bytes and SHALL record exact original and retained byte
sizes plus the affected path and representation.

### Acceptance criteria

- GIVEN un cuerpo de tamaño igual o menor al máximo
  WHEN se sanea
  THEN se conserva completo y no se registra truncación.
- GIVEN string, texto multibyte o JSON compuesto mayor que el máximo
  WHEN se sanea
  THEN el contenido conservado no supera el límite, no divide UTF-8, metadata
  informa tamaño original exacto, tamaño retenido exacto y `truncated=true`.
- GIVEN una assertion de valor cuya observación depende de un cuerpo truncado
  WHEN se evalúa
  THEN resulta `inconclusive` con causa de evidencia truncada; `exists`/`absent`
  sobre el cuerpo continúan usando su presencia.

## R7 — Persistencia fail-closed

IF sanitization fails for input, output or truncation metadata, THEN THE
executor SHALL NOT persist or publish any untreated content and SHALL terminate
the run as a FeatureProof error with an auditable safe cause.

### Acceptance criteria

- GIVEN una herramienta que devuelve contenido no tratable
  WHEN falla el sanitizer
  THEN `evidence.Store.Save` no recibe records, no se crea observación y el run
  no produce resultado funcional.
- GIVEN persistencia válida
  WHEN se confirma un record
  THEN input, output y metadata provienen de una transformación completada y el
  record continúa siendo inmutable y verificable por integridad.
- GIVEN un record tratado con un entero mayor que 2^53, un decimal o un número
  con exponente extremo válido
  WHEN `FileStore` lo guarda y recupera
  THEN conserva exactamente el token numérico y el hash continúa verificándose;
  no lo convierte a `float64`.
- GIVEN un archivo de evidencia con JSON válido seguido de otro valor o basura
  WHEN `FileStore` lo recupera
  THEN rechaza el stream completo aunque el primer valor y su hash parezcan
  válidos.
- GIVEN un error
  WHEN se registra o devuelve
  THEN incluye operación, categoría y path seguro, pero no serializa el valor
  causante ni otro contenido sin tratar.

## R8 — Vista del modelo monotónicamente más restrictiva

WHEN context is prepared for a model, THE context builder SHALL derive its
evidence summaries only from persisted treated records and SHALL apply all
model-only sensitive paths without weakening persistence redaction.

### Acceptance criteria

- GIVEN un campo visible en evidencia pero configurado como sensible para el
  modelo
  WHEN se construye contexto
  THEN el record persistido lo conserva y el resumen contiene `[REDACTED]`.
- GIVEN un campo ya redactado en evidencia
  WHEN se construye cualquier resumen
  THEN permanece redactado aunque no figure en la configuración exclusiva del
  modelo.
- GIVEN metadata de truncación
  WHEN se resume
  THEN puede conservar paths y tamaños, pero nunca reconstruye, amplía ni pide
  el contenido descartado.
- GIVEN un body truncado cuya forma interna ya no puede recorrerse con seguridad
  WHEN se construye la vista del modelo
  THEN el body completo se reemplaza por `[REDACTED]` y no se envía su prefijo.

## R9 — Propagación segura a artefactos

THE run SHALL derive journal details, assertion results, reports and model
context from treated structured data or stable codes and SHALL NOT copy raw
input/output or sensitive values into those artifacts.

### Acceptance criteria

- GIVEN secretos canario en headers raíz, rutas JSON, inputs y outputs
  WHEN el run completa, falla o queda inconcluso
  THEN ninguno aparece en evidencia, eventos, resultados, reportes ni requests
  capturadas por el modelo falso.
- GIVEN assertion `exists`/`absent` permitida sobre una ruta sensible
  WHEN se serializa su resultado
  THEN expected/observed expresan únicamente presencia y no contienen el valor
  original ni el marcador como dato observado.
- GIVEN errores de herramienta, sanitizer, store o contexto
  WHEN generan eventos o reportes
  THEN usan categorías estables y no interpolan cuerpos, headers ni valores.

## R10 — Cancelación y determinismo

IF the sanitization or model-projection context is cancelled, THEN THE operation
SHALL stop at the next traversal boundary, return the context cause and SHALL
NOT publish a partial view.

### Acceptance criteria

- GIVEN contexto cancelado antes de comenzar
  WHEN se solicita saneamiento o proyección
  THEN retorna la causa sin record ni resumen parcial.
- GIVEN cancelación durante un objeto/array grande
  WHEN se alcanza el siguiente nodo
  THEN cesa el recorrido; records previamente confirmados permanecen intactos.
- GIVEN la misma política y valor JSON
  WHEN se procesan repetidamente
  THEN contenido, metadata, orden y clasificación de errores son equivalentes.

## Errores y casos límite

- JSON null es un valor válido y puede ser redactado; un path ausente no se crea.
- Redactar un objeto o array sustituye el subtree completo y no expone su forma.
- La comparación de nombres dentro de `/headers` raíz es case-insensitive; la
  key estructural `headers` y las keys JSON ordinarias son case-sensitive.
- Un objeto `headers` fuera de la raíz solo queda protegido por paths JSON
  explícitos; cada adapter con otra forma debe declararlos en su configuración.
- La raíz sensible `""` se solapa con todas las assertions de valor.
- Un path sensible ancestro o descendiente de otro es válido; el resultado es
  idempotente y la regla más externa evita visitar contenido ya redactado.
- Los tamaños corresponden a los bytes entregados al sanitizer, no al tamaño
  remoto desconocido que una herramienta haya descartado previamente.
- El marcador nunca se interpreta como prueba de que el valor original era esa
  misma string; representa contenido oculto.
- Una truncación nunca se clasifica como `failed`; cuando impide comprobar el
  valor requerido produce `inconclusive`.
- Errores se distinguen por tipos/categorías y conservan causa segura con `%w`.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. Se aprobó explícitamente
que las rutas sensibles solo admiten assertions `exists`/`absent`; cualquier
operador que necesite el valor se rechaza antes del run. La incorporación de
esta configuración al YAML o CLI queda para una feature posterior.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-11T20:43:32+02:00
```
