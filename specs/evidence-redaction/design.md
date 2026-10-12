# Redacción y limitación de evidencias — Design

## Resumen de la solución

Se reemplazará el sanitizer superficial por una política compilada y una
transformación recursiva cancelable. El executor no podrá persistir un record
hasta disponer de input, output y metadata completamente tratados. Las
assertions se validarán contra la política antes de ejecutar el plan y se
evaluarán sobre la evidencia confirmada. El contexto del modelo se derivará de
esa evidencia y aplicará una segunda capa que solo puede ocultar más.

```text
config confiable -> validar/compilar policy
plan + policy -> validar solapamiento de assertions
tool result -> sanitize input/output + truncation metadata
            -> persist immutable evidence
            -> assertion engine
            -> report/events from treated values or stable codes
persisted evidence -> model projection (additional redaction) -> model context
```

## Fuentes de contexto consultadas

| Fuente | Evidencia aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Alcance único, acceptance criteria y proceso SDD. |
| `docs/specs.md` | EARS, puerta humana y trazabilidad R → task → test. |
| `docs/architecture.md` | ARCH-003, ARCH-006, ARCH-007, ARCH-008, ARCH-009, ARCH-010 y frontera model/evidence. |
| `docs/conventions.md` | Configuración temprana, errores tipados, contextos, inmutabilidad y tests herméticos. |
| `internal/evidence/store.go` | `Record`, `Store`, hash de integridad y `Sanitizer` superficial existente. |
| `internal/harness/{protocol,harness,journal}.go` | Observaciones del modelo, punto actual de saneamiento y eventos sin payload. |
| `internal/planning/{model,validate,executor}.go` | Validación previa, ejecución, evidencia terminal y propagación a assertions. |
| `internal/assertion/` y `specs/assertion-engine/` | JSON Pointer subset, operadores, árboles y causas inconclusas. |
| `internal/tools/http/` y `specs/http-tool/` | Headers protegidos, forma de body y truncación previa del transporte. |
| `progress/evidence-redaction/spec.md` | Bloqueo resuelto por el usuario sobre assertions en rutas sensibles. |

No se consultaron servicios, repositorios ni contratos externos.

## Contratos externos observados

No se añade un formato externo. La configuración entra por composición interna
y más adelante podrá mapearse desde CLI o una versión futura de specification.
Se conserva el contrato de salida HTTP existente. Si `http.request` ya entregó
un prefijo acotado, el sanitizer mide ese valor recibido; no intenta leer bytes
descartados por el adapter.

## Componentes y responsabilidades

### `evidence.Policy`

Modelo inmutable obtenido por constructor. Contiene el límite de cuerpos,
headers normalizados, paths compilados de persistencia y paths adicionales del
modelo. El constructor valida toda configuración y devuelve `ConfigError` con
categoría y field path. Los cuatro headers default se insertan siempre.

Los paths se compilan a tokens usando la misma gramática del assertion engine.
Para evitar dos implementaciones divergentes, la gramática se moverá a un
paquete interno cohesionado consumido por assertion y evidence, o se expondrá
como contrato pequeño desde el propietario adecuado; no se duplicará mediante
regex distintas.

### `evidence.Sanitizer`

Recibe `context.Context`, una policy compilada y un root estructurado. Produce
`SanitizedValue` con copia profunda y `[]Truncation`. La API no devuelve una
vista parcial junto a error. Recorre arrays por índice y objetos en orden de key
estable para que cancelación, primer error y metadata sean reproducibles.

Antes de visitar hijos comprueba si el path está cubierto por una regla
sensible. En ese caso escribe `[REDACTED]` y no atraviesa el subtree. Solo en el
objeto situado exactamente en `/headers` de cada raíz normaliza los nombres HTTP
para compararlos y conserva las keys no sensibles. No busca colecciones de
headers por nombre durante el recorrido: otras tools deben convertir sus
ubicaciones anidadas en `sensitive_json_paths` explícitos al componer la policy.
Para cada miembro `body` primero completa la redacción de todos sus
hijos y solo después forma el prefijo acotado; el tamaño original se calcula
sobre la representación recibida y el conservado sobre la vista ya tratada.

`Truncation` contiene `Path`, `Representation`, `OriginalBytes`,
`RetainedBytes` y `Truncated`. La representación es `utf8` para strings y
`json` para valores JSON compuestos/escalares no string serializados. El
contenido truncado se convierte en prefijo textual y la metadata impide que se
confunda con el valor JSON original.

### Integración de `evidence.Record`

El record incorporará metadata separada para truncaciones de input y output.
`Input` y `Output` pasarán a representar cualquier raíz JSON, no solo objects,
para que una policy pueda redactar de forma coherente el path raíz `""`. Los
consumidores que requieren un object validan esa forma antes de recorrerlo.
El hash de integridad cubrirá también esa metadata. `Save` y `Get` seguirán
aislando slices/maps y rechazando reescritura o manipulación. El store no
redacta por su cuenta: recibe únicamente records construidos por la frontera
segura del executor y valida que lleven la marca interna de transformación
completa, sin serializar contenido bruto al informar errores.

`FileStore.Get` decodificará con `json.Decoder.UseNumber` y exigirá EOF después
del único valor JSON. Así los valores dentro de raíces `any` conservan tokens
numéricos que `float64` alteraría —enteros mayores que 2^53, decimales y
exponentes extremos— y el recálculo del hash usa la misma representación exacta.
Un segundo valor o trailing garbage se rechaza antes de devolver el record.

### Validador plan/policy

La validación de assertions recibe la policy de redacción. Compara tokens para
detectar igualdad, ancestro o descendiente. Recorre todas las hojas de grupos en
orden y permite en solapamientos solo `exists` y `absent`. Los operadores de
valor devuelven `PlanError`/error de assertion con category y field path antes
de policy o tools.

Los paths exclusivos del modelo no restringen assertions porque no cambian la
evidencia persistida usada por el evaluator. Los defaults y headers adicionales
se traducen estáticamente a paths bajo `/headers`; por ello el validador puede
reconocer antes de efectos una assertion que apunte a esa colección, a un nombre
de header con cualquier casing o a sus ancestros/descendientes. Una ubicación
anidada solo participa si está declarada en `sensitive_json_paths`.

### Assertion evaluator y truncación

El evaluator recibe, además del output tratado, metadata de truncación. Si una
hoja de valor depende del body truncado o de un descendiente que dejó de estar
disponible, devuelve `inconclusive` con causa `truncated_evidence`. `exists` y
`absent` sobre el miembro body usan su presencia; una comparación sobre un body
completo mantiene la semántica existente. Los resultados nunca contienen el
prefijo como si fuese el JSON original cuando hay truncación.

### Persisted view y model view

El executor llama al sanitizer una vez por intento antes de construir el
record. Solo tras éxito llama a `Store.Save`. Assertions, plan references y
reportes usan el record confirmado.

Un `ModelEvidenceProjector` lee/copia la persisted view y aplica la unión de
paths persistentes y exclusivos del modelo. No acepta raw tool results. La
proyección reemplaza por completo cualquier body truncado con `[REDACTED]`, ya
que su prefijo textual no conserva una estructura sobre la que aplicar paths
adicionales con seguridad. En cuerpos completos añade las rutas exclusivas del
modelo sobre la estructura persistida. Nunca existe una operación inversa.

### Eventos y reportes

Los eventos continúan guardando IDs, estados, categorías y metadata segura, no
payloads. Se añadirá un evento o detalle estructurado de redacción/truncación
solo con paths y tamaños. Los reportes consumen evidencias y assertion results
tratados. Los errores operativos usan códigos estables y no llaman a formato
sobre valores sensibles o documentos completos.

## Paquetes afectados

```text
internal/
  evidence/
    redaction.go              # policy, sanitizer y metadata
    redaction_test.go
    store.go                  # record/hash con metadata tratada
    store_test.go
  jsonpath/                   # gramática compartida si la extracción es necesaria
  assertion/
    compile.go                # restricción por sensitive paths
    evaluator.go              # evidencia truncada -> inconclusive
    *_test.go
  planning/
    validate.go               # policy en validación completa
    executor.go               # frontera fail-closed antes del store
    *_test.go
  harness/
    protocol.go               # proyección segura de Observation
    harness.go                # usa la nueva frontera
    journal.go                # metadata segura si se añade al evento
    *_test.go
  reporting/                  # solo si ya existe consumidor en esta iteración
```

No se crea un paquete genérico. La extracción de `jsonpath` solo se hará si
permite compartir exactamente la gramática sin introducir ciclos.

## Flujo principal

1. La composición construye `evidence.Policy` antes de crear el run.
2. El constructor valida límite, headers y todos los paths; congela copias.
3. El plan validator valida estructura y solapamiento de cada hoja con paths
   sensibles antes de policy y tool execution.
4. El executor resuelve input y ejecuta una herramienta autorizada.
5. El sanitizer copia y trata input/output; genera metadata de truncación.
6. Si ambas transformaciones terminan, se construye y persiste el record.
7. El record confirmado alimenta assertions, referencias y reportes.
8. Si hay otra decisión del modelo, el projector crea una copia más restrictiva
   desde el record persistido y la incorpora al contexto.

## Flujos de error

- **Config inválida:** no se crea policy ni run; error tipado con field path.
- **Assertion incompatible:** se rechaza el plan completo antes de efectos.
- **Valor no JSON/cíclico/no finito:** sanitizer falla sin record parcial.
- **Cancelación durante recorrido:** retorna causa del contexto sin publicar.
- **Truncación:** no es error; metadata explícita y assertions dependientes
  resultan inconclusas.
- **Persistencia:** mantiene el error de FeatureProof existente; el mensaje no
  contiene input/output.
- **Proyección del modelo:** si falla, no se llama al modelo y el run termina
  como error, sin sustituir la vista por evidencia menos restrictiva.
- **Journal/reporte:** un fallo no autoriza reusar contenido bruto para
  diagnóstico.

## Propiedad del estado e inmutabilidad

`Policy` posee copias de configuración y no expone maps/slices mutables. Cada
llamada del sanitizer posee su traversal, copia y metadata. El source pertenece
al caller y nunca se modifica. `Record` posee las copias tratadas; store,
assertion y projector reciben nuevas copias o snapshots verificados.

La string marcador es una constante de dominio. No se conserva side table con
valores originales, hashes, longitudes de secretos ni closures que los
capturen. Los únicos tamaños registrados pertenecen a cuerpos truncados, no a
subtrees redactados.

## Concurrencia y cancelación

No se crean goroutines. Una policy inmutable puede compartirse; cada llamada es
independiente. El recorrido consulta `ctx.Err()` antes de cada nodo, antes de
serializar cuerpos y antes de devolver el resultado. La cancelación no borra
evidencias ya confirmadas y no usa `context.WithoutCancel` para sanear o
proyectar. El evento terminal conserva las reglas existentes del harness.

## Retries e idempotencia

Saneamiento, validación y proyección no se reintentan: son deterministas y un
segundo intento con la misma entrada no corrige una configuración o tipo
inválido. Los retries de herramienta generan un record tratado separado por
intento. La transformación repetida es idempotente: `[REDACTED]` permanece igual
y una vista ya acotada no recupera contenido.

## Observabilidad

Se observan categorías `invalid_redaction_config`, `redaction_failed`,
`assertion_sensitive_path` y `truncated_evidence`; paths y tamaños de
truncación son seguros. Métricas o eventos pueden contar valores redactados y
cuerpos truncados, pero no incluyen valor, hash, prefijo en logs ni nombre de
header no necesario. El journal sigue siendo fuente de auditoría.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro de FeatureProof y
su relación con el flujo o frontera correspondiente. El comentario expresará
intención, invariantes o motivo arquitectónico y no repetirá sintaxis. Se
actualizarán también los comentarios existentes cuya responsabilidad cambie.

## Estrategia de pruebas

Los tests usan fakes, `MemoryJournal`, `FileStore` temporal y canarios distintos
por superficie. Tras cada caso se recorren bytes de todos los archivos, eventos,
errores seguros, reportes serializados y requests capturadas por el modelo falso
para asegurar que ningún canario aparece. No hay red ni modelo real.

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | policy constructor | `TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun`; `TestPolicyFreezesNormalizedConfiguration` |
| R2 | recursive sanitizer | `TestSanitizerRedactsNestedInputsAndOutputsWithoutMutation`; `TestSanitizerFailsClosedForUnsupportedCyclicAndNonFiniteValues` |
| R3 | header rules | `TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively`; `TestNestedHeadersRequireExplicitSensitivePaths` |
| R4 | path rules/projector | `TestSensitiveJSONPathsRedactExactSubtrees`; `TestModelProjectionOnlyAddsRedaction` |
| R5 | plan/assertion validator | `TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects`; `TestNestedSensitiveValueAssertionRejectsWholePlan` |
| R6 | truncation/evaluator | `TestBodyTruncationRecordsExactOriginalAndRetainedBytes`; `TestTruncationPreservesUTF8Boundary`; `TestValueAssertionOnTruncatedBodyIsInconclusive` |
| R7 | executor/store | `TestRedactionFailurePreventsEvidencePersistenceAndPublication`; `TestPersistedMetadataIsIntegrityProtected`; `TestFileStorePreservesExactJSONNumbersAndRejectsTrailingData` |
| R8 | model projector | `TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive`; `TestModelProjectionFailurePreventsModelCall` |
| R9 | end-to-end artifact scan | `TestCanarySecretsNeverAppearInArtifactsEventsReportsOrModelContext`; `TestSensitiveExistenceResultContainsPresenceOnly` |
| R10 | all traversals | `TestSanitizationAndProjectionObserveCancellationWithoutPartialOutput`; `TestRedactionIsDeterministicAndIdempotent` |

Además se prueban raíz sensible, paths ausentes, arrays, escapes, null, marker ya
presente, paths ancestro/descendiente, headers raíz, headers anidados configurados
explícitamente, bodies en input/output,
límite exacto, UTF-8 multibyte, JSON compuesto y múltiples intentos.

## Alternativas y decisiones

- **Evaluar secretos antes de redactar:** descartado por decisión humana; crearía
  otra vista con riesgo de filtración y resultados difíciles de auditar.
- **Comparar contra `[REDACTED]`:** descartado; el marcador no representa el
  valor original.
- **Permitir solo `exists`/`absent` en solapamientos:** aprobado explícitamente;
  conserva pruebas estructurales sin leer el contenido.
- **Configurar o retirar defaults:** descartado. Los cuatro headers ya forman el
  baseline de seguridad y la feature solo permite añadir.
- **Detectar recursivamente cualquier objeto llamado `headers`:** descartado.
  Sin esquema de output no permite conocer antes de efectos todos los ancestros
  sensibles de una assertion. Los defaults se limitan al `/headers` raíz del
  contrato HTTP; otras ubicaciones exigen paths explícitos.
- **Sintaxis nueva de paths:** descartada. Compartir el subset aprobado evita dos
  lenguajes para el mismo documento.
- **Leer un body HTTP remoto completo para medirlo:** descartado; violaría el
  límite de la herramienta. Se mide el valor que cruza la frontera.
- **Sanear dentro del store:** descartado; el store no conoce la policy del run
  y el contenido podría filtrarse antes en assertions o contexto.
- **Resumen del modelo desde raw output:** descartado; toda vista se deriva de
  evidencia tratada y confirmada.

## Conformidad arquitectónica

El diseño aplica ARCH-003 al mantener assertions sobre evidencia confirmada,
ARCH-006 al propagar contexto, ARCH-007 al auditar sin payload, ARCH-008 al no
convertir contenido en instrucciones, ARCH-009 mediante redacción previa y
ARCH-010 con tests herméticos. Mantiene interfaces en consumidores, errores
tipados e inmutabilidad según CONV-003, CONV-004, CONV-005, CONV-007,
CONV-008 y CONV-009. No se solicita excepción.

## Mapa inicial R → tasks

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T7 | Config inválida impide crear/iniciar el run. |
| R2 | T2, T7 | Copia recursiva determinista sin alias ni salida parcial. |
| R3 | T2, T7 | Defaults y headers adicionales se ocultan siempre. |
| R4 | T2, T5, T7 | Paths persistentes y exclusivos del modelo forman vistas monotónicas. |
| R5 | T3, T7 | Solo existencia/ausencia pasa validación en rutas sensibles. |
| R6 | T2, T4, T7 | Truncación y assertions inconclusas quedan explícitas. |
| R7 | T4, T7 | Ningún fallo de redacción llega al store o publicación y los números JSON conservan representación exacta. |
| R8 | T5, T7 | El modelo recibe una proyección derivada más restrictiva. |
| R9 | T4, T5, T6, T7 | Escaneo canario de todas las superficies no encuentra secretos. |
| R10 | T2, T4, T5, T7 | Cancelación, determinismo e idempotencia verificados. |
