# Especificación versionada de validación — Design

## Resumen de la solución

Se añadirá un modelo interno de specification y un adapter YAML v1. El adapter
lee un archivo local, decodifica de forma estricta un DTO privado, comprueba la
versión, traduce sus campos y entrega el modelo al validador semántico. Solo una
specification completamente válida sale de esta frontera. La feature no crea
runs ni conecta todavía el resultado al harness.

```text
archivo local
    -> lectura acotada a la ruta recibida
    -> documento YAML / comprobación de versión
    -> DTO privado v1 y campos conocidos
    -> traducción a modelo interno
    -> validación semántica
    -> Specification válida | error tipado con field path
```

## Fuentes de contexto consultadas

- `AGENTS.md` y `feature_list.json`: alcance, flujo SDD y acceptance criteria de
  `validation-specification`.
- `docs/specs.md`: formato Kiro, EARS, trazabilidad y puerta humana.
- `docs/architecture.md`: responsabilidades del specification loader, modelo
  de dominio, arquitectura hexagonal, carga local y límites del harness.
- `docs/conventions.md`: DTO externos confinados al adapter, errores tipados,
  contextos, dependencias explícitas y tests herméticos.
- `internal/harness/{state.go,protocol.go,harness.go}`: contratos internos
  existentes para `Run`, `Limits` y configuración del harness; se consultaron
  como contexto y no se modifican en esta feature.
- `specs/agent-harness/`: alcance aprobado y decisiones aplazadas que esta
  feature debe respetar.

No se consultaron repositorios, APIs ni documentación externa. La dependencia
YAML concreta se elegirá como detalle del adapter durante la implementación;
sus tipos y mensajes no forman parte del contrato de FeatureProof.

## Contrato externo observado

No existe un contrato externo previo. Esta spec establece el contrato YAML v1
documentado en `requirements.md` con estas claves exactas:

| Ruta | Tipo YAML | Regla v1 |
|---|---|---|
| `version` | integer | requerido, valor `1` |
| `feature.id` | string | requerido, ID válido |
| `feature.description` | string | requerida, no vacía |
| `target.id` | string | requerido, ID válido |
| `target.base_url` | string | URL absoluta HTTP(S), sin userinfo/query/fragment |
| `constraints` | sequence | requerida, al menos un elemento |
| `constraints[].id` | string | requerido, ID válido y único |
| `constraints[].description` | string | requerida, no vacía |
| `constraints[].target_id` | string | requerido, referencia exacta a `target.id` |
| `limits.max_steps` | integer | requerido, `> 0` |
| `limits.max_tool_calls` | integer | requerido, `> 0` |
| `limits.max_retries` | integer | requerido, `>= 0` |

Se rechazan claves desconocidas, claves repetidas, coerciones de string a
número y documentos YAML adicionales. Esto hace observables los errores
tipográficos y mantiene estable la interpretación de v1.

## Componentes y responsabilidades

### Modelo interno `specification`

Define tipos propios `Specification`, `Feature`, `Target`, `Constraint` y
`Limits`. No contienen tags ni tipos de la librería YAML. `Limits` usa nombres
equivalentes a los del harness, pero continúa siendo propiedad de la
specification; una integración posterior hará la conversión explícita al crear
un run.

### Validador semántico

Valida en orden determinista campos requeridos, formato de IDs, URL, presencia
y unicidad de restricciones, referencias y límites. Devuelve el primer
`ValidationError`, con `Category`, `Field` y causa opcional. No modifica el
modelo ni intenta corregir datos.

Las categorías estables previstas son `required`, `invalid_value`,
`duplicate_id`, `unknown_reference`, `unknown_field`, `syntax` y
`unsupported_version`. La lógica de los consumidores usa `errors.Is` o
`errors.As`, nunca comparación textual.

### Adapter YAML v1

Encapsula los DTO con tags YAML, el parser y cualquier nodo usado para línea y
columna. Primero obtiene un envelope mínimo para distinguir la versión. Si la
versión es 1, decodifica estrictamente el DTO v1, traduce a modelos internos y
llama al validador. Un `UnsupportedVersionError` lleva `Received` y
`Supported`, actualmente `[1]`.

El adapter ofrece una operación de carga local con `context.Context` y ruta
explícita. Comprueba cancelación antes y después de la lectura y no almacena el
contexto. No consulta environment, DNS, URLs ni servicios. El tamaño máximo de
archivo no se fija en esta feature porque no figura en los acceptance criteria;
la futura CLI podrá imponer su límite de entrada antes de llamar al decoder.

## Paquetes afectados

```text
internal/
  specification/
    model.go              # modelos internos
    validate.go           # reglas semánticas y errores propios
    validate_test.go
    yaml/
      loader.go           # lectura local, DTO privados y traducción v1
      loader_test.go
      testdata/           # fixtures v1 válidos e inválidos
```

El subpaquete adapter puede usar el nombre de paquete `specyaml` para evitar
confusión con la dependencia. `internal/harness` no se modifica. No se crea un
paquete genérico ni un entrypoint nuevo.

## Flujo principal

1. El solicitante llama al loader con contexto y ruta local.
2. El loader rechaza una cancelación ya activa y lee los bytes del archivo.
3. El parser exige exactamente un documento mapping y sintaxis válida.
4. El adapter extrae `version`; si no es 1 devuelve error de versión.
5. El decoder estricto rellena el DTO privado v1 y rechaza claves desconocidas
   o repetidas.
6. El adapter traduce todos los campos a tipos internos sin conservar nodos
   YAML.
7. El validador recorre los campos en el orden contractual y comprueba reglas
   y referencias.
8. Solo si todas las comprobaciones pasan se devuelve `Specification`.

## Flujos de error

- **Lectura:** se devuelve un error contextual con la ruta y la causa envuelta.
- **Sintaxis/root/documentos extra:** se devuelve `syntax`, incluyendo línea y
  columna si el parser las conoce.
- **Versión ausente o de tipo inválido:** se devuelve error en `version`.
- **Versión desconocida:** se devuelve `UnsupportedVersionError` antes de
  intentar interpretar el cuerpo como v1.
- **Campo desconocido o clave repetida:** se rechaza el documento sin modelo
  parcial.
- **Semántica:** se devuelve `ValidationError` para el primer campo inválido en
  orden estable; índices de secuencia usan notación `constraints[n]`.
- **Cancelación:** se conserva `context.Canceled` o
  `context.DeadlineExceeded`; no se inicia validación posterior.

## Propiedad del estado

Cada llamada posee sus bytes, DTO y modelo candidato. El loader no conserva
estado mutable global, cache ni último error. La `Specification` devuelta es un
valor independiente; sus slices no se comparten con buffers internos del
parser. El solicitante pasa a ser propietario del resultado.

## Concurrencia y cancelación

No se crean goroutines. Distintas cargas pueden ejecutarse concurrentemente
porque el loader es stateless. `context.Context` se propaga a la operación de
carga y se observa antes y después de la lectura local. La validación en memoria
es finita y secuencial; comprueba cancelación entre fases cuando ya se ha
detectado una cancelación.

## Retries e idempotencia

El loader no reintenta lecturas ni parseos. Cargar el mismo contenido es una
operación sin efectos y produce el mismo modelo o la misma clasificación de
error. La política de retry de errores de filesystem pertenece al solicitante;
la validación semántica y los errores de versión no son reintentables por sí
mismos.

## Observabilidad

Esta feature no añade eventos al journal porque ocurre antes de crear un run.
Los errores incluyen operación, ruta de archivo cuando corresponde, categoría
y field path. No se registra el cuerpo YAML, la URL completa ni descripciones.
Una futura CLI podrá mostrar estos errores sin inspeccionar código interno.

## Estrategia de pruebas

Los tests usan `t.TempDir`, bytes y fixtures locales. No abren sockets, no leen
variables de entorno y no dependen del harness ni de un modelo. Se comprueba la
API de errores mediante tipos y campos.

| Requirement | Componente | Prueba prevista |
|---|---|---|
| R1 | loader/decoder | `TestLoadV1Specification`; `TestLoadRejectsUnreadableMalformedAndMultipleDocuments` |
| R2 | adapter/traductor | `TestV1LoaderReturnsOnlyInternalModels`; `TestV1LoadIsDeterministicAndStateless` |
| R3 | validador y decoder estricto | `TestValidateRejectsMissingRequiredFields`; `TestValidateRejectsDuplicateConstraintIDs`; `TestValidateRejectsInvalidValuesAndUnknownTarget`; `TestLoadRejectsUnknownAndDuplicateKeys` |
| R4 | dispatcher de versión | `TestLoadRejectsUnsupportedVersionWithReceivedAndSupported` |
| R5 | errores propios | `TestValidationErrorIdentifiesCategoryAndField`; `TestSyntaxErrorPreservesLocation`; `TestValidationReturnsStableFirstError` |
| R6 | loader local | `TestLoadDoesNotExpandEnvironmentOrAccessNetwork` |
| R7 | fixtures de compatibilidad | `TestV1CompatibilityFixtures`; `TestV1InvalidFixturesRemainRejected` |

Además se prueban root vacío/null/no mapping, lista de constraints vacía,
límites en sus fronteras, IDs case-sensitive, URL con credenciales/query o
fragment, strings numéricos, cancelación previa y ausencia de modelo parcial.

## Alternativas y decisiones

- **Usar structs internos directamente como destino YAML:** descartado porque
  filtraría el contrato externo al dominio y dificultaría versionar adapters.
- **Aceptar campos desconocidos:** descartado porque ocultaría typos y
  debilitaría la compatibilidad verificable.
- **Normalizar IDs o corregir URLs:** descartado porque cambiaría silenciosamente
  la intención del usuario y las referencias.
- **Validar disponibilidad del target:** descartado porque violaría la carga
  hermética; esta fase valida forma, no conectividad.
- **Añadir plans y assertions al v1:** descartado porque corresponden a
  `multi-step-validation-plan` y `assertion-engine`.
- **Reutilizar directamente `harness.Limits`:** descartado para mantener la
  specification independiente de la aplicación y hacer explícita la futura
  traducción entre contextos.
- **Acumular todos los errores:** aplazado. Un primer error estable con field
  path satisface la corrección guiada y evita fijar ahora un contrato de orden
  de agregación más complejo.

## Conformidad arquitectónica

El diseño preserva ARCH-006 al aceptar contexto en la lectura bloqueable y
ARCH-010 al mantener pruebas herméticas. Respeta la dirección de dependencias:
el modelo interno no importa YAML ni harness, y el adapter traduce hacia el
dominio. También cumple CONV-001, CONV-002, CONV-004, CONV-005, CONV-008 y
CONV-009. No se solicita ninguna excepción arquitectónica.

## Mapa inicial R → componente/prueba

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T3, T5 | Un archivo v1 válido produce el modelo completo; fallos de lectura/sintaxis no producen modelo. |
| R2 | T1, T3, T5 | Los DTO YAML quedan privados al adapter y cargas repetidas son equivalentes. |
| R3 | T1, T2, T3, T5 | Campos, IDs, valores y referencias inválidos se rechazan antes del run. |
| R4 | T3, T5 | El error de versión informa recibido y soportados. |
| R5 | T2, T3, T5 | Cada fallo expone categoría y field path estable cuando puede conocerse. |
| R6 | T3, T4, T5 | La carga no usa red, environment ni servicios externos. |
| R7 | T3, T4, T5 | Fixtures v1 fijan compatibilidad válida e inválida. |
