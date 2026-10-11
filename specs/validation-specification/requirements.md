# Especificación versionada de validación — Requirements

## Objetivo y problema

FeatureProof necesita una entrada estable y verificable que describa qué
feature se valida, contra qué target autorizado, qué restricciones deben
comprobarse y qué presupuesto limita la ejecución. El objetivo es cargar una
especificación YAML de versión 1 y convertirla a modelos internos solo después
de comprobar su sintaxis, versión y coherencia semántica.

## Alcance

- Cargar una especificación desde un archivo YAML local.
- Reconocer explícitamente la versión 1 del contrato externo.
- Traducir el documento a modelos internos independientes de la librería YAML.
- Representar una feature, un target, una o más restricciones y límites de
  pasos, llamadas a herramientas y retries.
- Validar campos obligatorios, identificadores, valores, duplicados y
  referencias al target antes de que pueda crearse un run.
- Devolver errores accionables con la ruta del campo afectado.
- Ejecutar toda la carga y validación de forma local y determinista.

### Contrato YAML v1 observable

La versión 1 acepta esta forma:

```yaml
version: 1
feature:
  id: checkout-search
  description: La búsqueda devuelve productos disponibles
target:
  id: staging
  base_url: https://staging.example.test
constraints:
  - id: search-returns-results
    description: Una búsqueda conocida devuelve al menos un resultado
    target_id: staging
limits:
  max_steps: 8
  max_tool_calls: 4
  max_retries: 1
```

Los identificadores comienzan con un carácter alfanumérico y solo contienen
caracteres alfanuméricos, `.`, `_` o `-`. `base_url` es una URL absoluta con
esquema `http` o `https`, sin credenciales, query ni fragment. Las descripciones
no pueden quedar vacías tras eliminar espacios exteriores. Cada `target_id`
debe referenciar `target.id`. `max_steps` y `max_tool_calls` son enteros mayores
que cero; `max_retries` es un entero mayor o igual que cero.

## Fuera de alcance

- Ejecutar un run, una herramienta o una assertion.
- Definir planes de varios pasos, dependencias u outputs entre pasos.
- Definir el lenguaje de assertions o contratos específicos de herramientas.
- Sustituir valores desde variables de entorno, descargar includes o resolver
  secretos.
- Aceptar múltiples targets, migrar automáticamente versiones futuras o
  reescribir el archivo de entrada.
- Incorporar una CLI, API pública, watcher de archivos o acceso remoto.

## Actores y términos

- **Solicitante:** componente local que proporciona la ruta del archivo.
- **Loader YAML:** adapter que lee YAML v1, conserva allí los DTO y traduce a
  modelos internos.
- **Specification:** modelo interno validado que puede consumir una fase
  posterior para crear un run.
- **Feature:** funcionalidad descrita mediante identificador y descripción.
- **Target:** aplicación autorizada identificada por un ID y una URL base.
- **Constraint:** condición descrita e identificada que referencia el target.
- **Limits:** presupuesto máximo de pasos, llamadas a herramientas y retries.
- **Field path:** localización estable del dato erróneo, por ejemplo
  `constraints[1].target_id`.

## R1 — Carga local de YAML v1

WHEN a requester provides a readable local YAML file with `version: 1`, THE
specification loader SHALL decode the feature, target, constraints and execution
limits defined by the v1 contract.

### Acceptance criteria

- GIVEN un archivo que cumple el contrato YAML v1
  WHEN se carga
  THEN el resultado interno contiene los mismos valores normalizados de
  feature, target, constraints y límites.
- GIVEN que la ruta no existe, no es legible o el contenido no es YAML válido
  WHEN se intenta cargar
  THEN no se produce una specification y el error identifica la operación o la
  localización sintáctica disponible.
- GIVEN un documento YAML con más de un documento (`---`)
  WHEN se carga
  THEN se rechaza el contenido adicional en lugar de ignorarlo.

## R2 — Aislamiento del contrato YAML

WHEN a valid v1 document is decoded, THE YAML adapter SHALL translate it to
internal specification models and SHALL NOT expose YAML-library types outside
the adapter boundary.

### Acceptance criteria

- GIVEN una carga válida
  WHEN otro paquete consume el resultado
  THEN solo observa tipos internos de FeatureProof y no nodos, tags ni errores
  como tipos de su API pública procedentes de la librería YAML.
- GIVEN los mismos bytes válidos cargados repetidamente
  WHEN se comparan los modelos internos
  THEN contienen los mismos valores y el loader no conserva estado mutable
  entre cargas.

## R3 — Validación completa antes del run

WHEN a v1 document has valid YAML syntax, THE specification loader SHALL reject
missing required fields, duplicate identifiers, invalid values and unresolved
target references before returning an internal specification.

### Acceptance criteria

- GIVEN la ausencia de cualquier campo requerido del contrato v1
  WHEN se valida
  THEN la carga falla y no devuelve una specification utilizable.
- GIVEN dos restricciones con el mismo `id`
  WHEN se valida
  THEN la carga falla en el segundo identificador y no devuelve una
  specification utilizable.
- GIVEN un identificador, URL, descripción o límite que no cumple sus reglas
  WHEN se valida
  THEN la carga falla antes de que pueda crearse un run.
- GIVEN una restricción cuyo `target_id` no coincide con `target.id`
  WHEN se valida
  THEN la carga falla antes de que pueda crearse un run.
- GIVEN campos YAML no declarados por v1
  WHEN se carga
  THEN se rechazan para impedir que un error tipográfico sea ignorado.

## R4 — Rechazo explícito de versiones desconocidas

IF a syntactically valid document declares a version other than 1, THEN THE
specification loader SHALL reject it with an unsupported-version error that
reports the received version and the supported versions.

### Acceptance criteria

- GIVEN `version: 2`
  WHEN se carga el documento
  THEN el error distinguible contiene `received=2` y `supported=[1]`.
- GIVEN una versión desconocida y campos que no pertenecen a v1
  WHEN se carga
  THEN el resultado principal es versión no soportada y no se reinterpretan
  esos campos como v1.

## R5 — Errores accionables por campo

IF syntax or semantic validation fails, THEN THE specification loader SHALL
return a distinguishable error with a stable category and the affected field
path whenever that path can be determined.

### Acceptance criteria

- GIVEN un fallo semántico en una restricción
  WHEN se inspecciona el error
  THEN expone una categoría comprobable sin comparar texto y la ruta, por
  ejemplo `constraints[1].target_id`.
- GIVEN YAML sintácticamente inválido
  WHEN el parser proporciona línea y columna
  THEN el error conserva esa localización y su causa sin filtrar tipos YAML a
  la firma del loader.
- GIVEN el mismo documento inválido
  WHEN se valida repetidamente
  THEN se informa el mismo primer error siguiendo el orden del documento y de
  los campos del contrato.

## R6 — Carga hermética

THE specification loading and validation process SHALL use only the supplied
local file content and SHALL NOT read environment variables or contact network
or external services.

### Acceptance criteria

- GIVEN un entorno sin red y sin variables de entorno configuradas
  WHEN se cargan specs válidas e inválidas
  THEN todos los resultados son deterministas y no dependen de recursos
  externos.
- GIVEN cadenas con formas de variable (`${NAME}`) o URL dentro de valores
  escalares
  WHEN se carga el archivo
  THEN no se expanden ni se consultan; se validan como texto literal conforme
  al contrato.

## R7 — Compatibilidad estable de v1

THE v1 loader SHALL preserve the documented v1 field names and semantics for
all conforming v1 documents.

### Acceptance criteria

- GIVEN un conjunto de fixtures válidos de v1
  WHEN se cargan después de cambios internos del adapter
  THEN siguen produciendo modelos internos equivalentes.
- GIVEN fixtures con errores sintácticos, semánticos y de versión
  WHEN se ejecuta la suite
  THEN cada clase permanece distinguible y ningún fixture inválido produce una
  specification.

## Errores y casos límite

- Un archivo vacío, un documento `null`, un root no mapping o `version` ausente
  no produce una specification.
- Un error de lectura conserva la causa del sistema y añade la ruta del archivo
  a la operación fallida.
- Claves repetidas dentro de un mismo mapping y campos desconocidos se
  rechazan; no se aplica una política silenciosa de "último valor gana".
- `constraints` debe contener al menos un elemento. Una entrada `null` o con
  descripción compuesta solo por espacios es inválida.
- Los IDs se comparan exactamente; no se normalizan mayúsculas ni se corrigen
  automáticamente.
- La URL no se consulta. Su validación es puramente sintáctica y no confirma
  DNS, disponibilidad ni permisos de red.
- Valores numéricos expresados como cadenas no se convierten implícitamente.
- El loader no retorna un modelo parcial junto a un error.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. Versiones posteriores
podrán añadir múltiples targets, otros límites o nuevas secciones, pero deberán
tener su propio contrato y no cambiar la interpretación de v1. El lenguaje de
assertions y los pasos del plan pertenecen a features posteriores.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-11T16:51:43+02:00
```
