# Inspector de contratos OpenAPI — Design

## Resumen de la solución

Se incorporará un paquete cohesionado `internal/openapi` con modelos internos
inmutables y tres casos de uso: cargar/normalizar un contrato local, validar una
respuesta observada y construir contexto reducido. Un adapter de documento
decodificará JSON/YAML y resolverá `$ref` a través de una interfaz de filesystem
definida por el consumidor. Ninguna URL remota dispondrá de resolver.

La reducción será deliberadamente de dos etapas. `Catalog` ofrece identidades y
metadata estructural compacta; un caller posterior asocia restricciones a
operation keys y solicita `BuildContext`. De este modo esta feature no introduce
un modelo, búsqueda semántica ni heurísticas difíciles de verificar.

```text
authorized root + entry + limits
        -> local source boundary
        -> parse/version/schema validation
        -> bounded local $ref resolution
        -> immutable Contract
             |-> Catalog
             |-> ValidateResponse(operation key, observed response)
             `-> BuildContext(constraints + selected keys, byte limit)
```

## Fuentes de contexto consultadas

| Fuente | Evidencia aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Alcance único, aceptación y puerta SDD. |
| `.codex/agents/spec.md`, `docs/specs.md` | Requirements EARS, trazabilidad y límites del spec author. |
| `docs/architecture.md` | Context builder, datos no confiables, determinismo, cancelación y arquitectura hexagonal. |
| `docs/conventions.md` | Interfaces en consumidor, errores tipados, contextos, comentarios y tests herméticos. |
| `specs/validation-specification/` | Separación adapter/modelo y diagnósticos por field path. |
| `specs/http-tool/`, `internal/tools/http/` | Forma observada de status, headers y body; tool sin persistencia directa. |
| `specs/assertion-engine/`, `internal/assertion/` | Resultados deterministas y separación entre configuración inválida e incumplimiento. |
| `specs/evidence-redaction/` | Tratamiento fail-closed y exclusión de valores sensibles. |
| `specs/local-validation-cli/`, `internal/localvalidation/` | La CLI v1 y su manifest quedan fuera del cambio. |

## Contratos externos observados

El adapter interpreta OpenAPI 3.0.x y 3.1.x: Path Item/Operation, Parameters,
Request Body, Responses, Header, Security Scheme/Requirement y Schema Objects.
Respeta la sobrescritura de parámetros por `name`/`in`, seguridad global frente
a la específica y selección de response exacta, rango de clase y `default`.

JSON Pointer se usa solo como fragmento de referencias locales. Los paths de
filesystem se resuelven contra el archivo que contiene el `$ref`, se
canonicalizan y se comprueban contra la raíz autorizada antes de abrirse. Los
tipos de una librería OpenAPI/YAML permanecen privados al adapter y se traducen
a modelos propios. La dependencia concreta se elegirá por soporte probado de
3.0 y 3.1; no se expone como API del paquete.

## Componentes y responsabilidades

### `Config` y límites compilados

El constructor valida `AuthorizedRoot`, tamaños máximos de documento y total,
número de documentos/referencias, profundidad de resolución, body observado y
bytes de contexto. Canonicaliza la raíz una sola vez y rechaza un envelope de
contexto que no pueda contener versión y metadata de omisiones. No hay defaults
silenciosos.

### `SourceFS`

Interfaz pequeña propiedad de `openapi` para canonicalización y lectura local
cancelable. La implementación de OS abre únicamente paths que ya pasaron la
comprobación de containment. La validación de symlinks ocurre antes de lectura;
un test double permite demostrar que un target denegado nunca se abre.

### Loader y resolver

El loader detecta JSON/YAML desde contenido, conserva locations estructurales y
rechaza mappings duplicados. Comprueba la versión declarada antes de normalizar.
El resolver mantiene por carga un registro de archivos canónicos, pila de refs,
conteos y bytes. No usa caches globales. Cada traversal observa contexto y
produce errores tipados sin interpolar nodos o valores.

### `Contract`

Snapshot inmutable que posee operations, security schemes y schemas
normalizados. Maps y slices no se exponen directamente: `Catalog`, validación y
reducción devuelven copias. La operación se indexa por `OperationKey{Method,
Path}`; `operationId` se valida según OpenAPI, pero no controla identidad.

### Catálogo y descriptores

El catálogo ordena path lexicográficamente y método en orden HTTP estable.
Cada `OperationDescriptor` contiene key, operationId opcional, parámetros
efectivos, request body, responses y referencias internas a schemas
normalizados. Security representa alternativas OR; cada alternativa contiene
los schemes AND y scopes ordenados. No existe campo para credenciales.

### Response validator

`ValidateResponse(ctx, key, ObservedResponse)` valida input y el límite del body,
elige response exacta/rango/default y devuelve `ResponseValidation` con
`Conforms` y violations ordenadas. Un input inválido o fallo interno es `error`;
un status/header/body que contradice el contrato es una violation y no error.

Los headers se normalizan case-insensitivamente. El media type elimina
parámetros de `Content-Type`, aplica exact match antes de wildcards y decodifica
el body según el media type seleccionado. La validación de schema usa el
dialecto correspondiente a 3.0 o 3.1, dirección response y solo formatos cuya
semántica implemente y pruebe la dependencia. Ningún valor observado se copia a
violations.

### Context builder

`BuildContext(ctx, ContextRequest)` recibe constraints ordenadas, cada una con
ID y operation keys explícitas. Rechaza IDs/keys duplicados o desconocidos. Un
reachability walk parte de parámetros, request bodies, responses y security de
las seleccionadas, deduplica schemas y produce una representación canónica.

El serializer usa una lista blanca. Omite siempre examples, extensions,
descriptions, titles y external docs, y cuenta cada categoría. Las operaciones
se empaquetan como unidades completas en orden estable. Antes de añadir una
unidad se codifica de forma aislada y se comprueba el límite restante; si no
cabe, se descarta la unidad y aumenta `size_limit`. La metadata usa categorías
y conteos acotados, por lo que el output completo nunca excede el máximo. El
resultado se etiqueta estructuralmente como contenido externo no confiable;
esta etiqueta es dato para el futuro adapter y no una instrucción textual.

### Errores y violations

`Error` contiene `Category`, `Location` segura y `Cause`; las categorías
incluyen `invalid_config`, `read_failed`, `invalid_syntax`,
`unsupported_version`, `invalid_document`, `reference_denied`,
`reference_broken`, `reference_cycle`, `limit_exceeded`, `invalid_input` y
`cancelled` cuando corresponda conservar explícitamente contexto. `Error()` no
incluye contenido documental.

`Violation` contiene `Category`, location y keyword esperada, pero nunca
expected/observed values. Categorías iniciales: `status_not_declared`,
`required_header_missing`, `header_invalid`, `media_type_not_declared`,
`body_invalid` y `schema_violation`.

## Paquetes afectados

```text
internal/openapi/
  model.go               # modelos internos inmutables y errores
  loader.go              # composición de carga/normalización
  references.go          # ámbito y presupuestos de $ref locales
  catalog.go             # descriptores deterministas
  response.go            # selección y validación de response
  context.go             # reachability y reducción acotada
  localfs.go             # adapter de filesystem local
  *_test.go
  testdata/              # fixtures 3.0/3.1 JSON/YAML sin secretos reales
```

Podrán dividirse archivos adicionales si mantienen esas responsabilidades. No
se modifica `cmd/`, specification YAML v1, plan manifest ni la herramienta HTTP.

## Flujos principales

### Carga e inspección

1. El caller construye configuración explícita y un loader.
2. El loader canonicaliza root y entry, comprueba containment y lee el root.
3. Detecta JSON/YAML, versión y forma; valida el documento.
4. Resuelve refs internas/locales observando scope y presupuestos.
5. Traduce a un snapshot propio, valida identidades y ordena operaciones.
6. Devuelve `Contract`; `Catalog` produce copias deterministas.

### Validación de respuesta

1. El caller aporta operation key y response ya acotada.
2. Se valida key, status, headers y tamaño antes de decodificar body.
3. Se elige response exacta, rango o default.
4. Se validan headers declarados y media type/schema en dirección response.
5. Se ordenan violations y se calcula `Conforms = len(violations) == 0`.

### Contexto reducido

1. El caller asocia constraints a keys tomadas del catálogo.
2. Se validan IDs, keys y duplicados antes de producir output.
3. Se recorre solo el grafo alcanzable desde las operaciones seleccionadas.
4. Se aplica la lista blanca y se cuentan omisiones permanentes.
5. Se añaden unidades completas mientras caben; el resto aumenta omisiones.
6. Se codifica canónicamente y se verifica el límite como postcondición.

## Flujos de error

- Config, root o entry inválidos: falla antes de leer documentos.
- Documento/version/syntax inválidos: no se inicia resolución.
- Ref remota o fuera de scope: se rechaza antes de cualquier read/network.
- Ref rota/cíclica/límite agotado: no se devuelve `Contract` parcial.
- Operación desconocida/input inválido: no se inicia schema validation.
- Response no conforme: resultado válido con violations, no error del sistema.
- Body/contexto demasiado grande: fail-closed o unidad omitida según la
  frontera definida; nunca se valida/publica un fragmento como completo.
- Cancelación: se conserva `ctx.Err()` como causa y no se devuelve salida
  parcial.

## Propiedad del estado

Cada `Loader.Load` posee contador, cache local, pila y AST. `Contract` posee el
snapshot normalizado y no retiene handles abiertos. Cada validación/context
build posee sus buffers y resultados. El caller posee las copias devueltas.
No existen registries o caches mutables globales.

## Concurrencia y cancelación

No se crean goroutines. Un `Contract` inmutable puede leerse concurrentemente;
cada operación usa estado local. Los recorridos consultan `ctx.Err()` antes de
lecturas, resolución de refs, nodos de schema, validación y serialización. No se
mantienen locks durante I/O porque no se necesita estado compartido mutable.

## Retries e idempotencia

No hay retries: disco local, parsing y validación son deterministas, y repetir
una lectura fallida ocultaría el error original. Cargar los mismos bytes con la
misma configuración produce el mismo catálogo; validar la misma respuesta
produce violations iguales; reducir la misma selección produce bytes iguales.
El inspector no escribe archivos ni tiene efectos sobre la aplicación.

## Observabilidad

El paquete devuelve categorías, locations, operation keys, conteos de
documentos/refs y omisiones. No registra por su cuenta. El caller podrá emitir
eventos usando esos metadatos, sin source snippets, bodies, examples,
descriptions, URLs con posibles datos, credenciales ni valores observados.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro de FeatureProof y
su relación con el flujo o frontera correspondiente. El comentario expresará
intención, invariantes o motivo arquitectónico y no repetirá la sintaxis. Se
actualizarán también los comentarios existentes cuya responsabilidad cambie.

## Estrategia de pruebas

Los tests usan `t.TempDir`, un `SourceFS` instrumentado y fixtures mínimos
equivalentes en JSON/YAML y 3.0/3.1. Ningún test crea un cliente de red. Los
tests de cancelación usan contextos/fakes coordinados, sin `time.Sleep`.

| Requirement | Componente | Pruebas previstas |
|---|---|---|
| R1 | loader/version adapter | `TestLoadAcceptsOpenAPI30And31JSONAndYAML`; `TestLoadRejectsSyntaxDuplicateKeysUnsupportedVersionAndInvalidDocument` |
| R2 | reference resolver | `TestResolverAllowsInternalAndInRootFiles`; `TestResolverRejectsRemoteTraversalAbsoluteAndSymlinkEscapeBeforeRead`; `TestResolverReportsBrokenCycleAndLimits` |
| R3 | catalog | `TestCatalogNormalizesParametersBodiesResponsesAndSchemasInStableOrder` |
| R4 | security normalization | `TestCatalogComputesGlobalOperationAnonymousAndAlternativeSecurityWithoutCredentials` |
| R5 | error boundary | `TestErrorsExposeCategoryAndLocationWithoutCanaryPayloadsAndPreserveCause` |
| R6 | response selection | `TestValidateResponsePrefersExactThenRangeThenDefault`; `TestValidateResponseRejectsUnknownOperationAndUndeclaredStatus` |
| R7 | response/schema validation | `TestValidateResponseChecksRequiredHeadersMediaTypesAndVersionedSchemas`; `TestValidateResponseRejectsOversizedOrMalformedBodiesWithoutValues` |
| R8 | context reachability | `TestContextContainsOnlyExplicitlySelectedOperationsAndReachableDeduplicatedSchemas` |
| R9 | context budget | `TestContextOmitsUntrustedFreeTextExamplesExtensionsAndWholeOversizedOperationsWithinBudget` |
| R10 | cancellation/immutability | `TestAllTraversalsObserveCancellationWithoutPartialOutput`; `TestContractsAreImmutableAndIndependentUnderRace` |
| R11 | boundary tests | `TestInspectorNeverUsesNetworkAndDoesNotReadDeniedReferences`; `TestReadFailureIsTypedWithoutPartialContract` |

Además se cubren media types wildcard, `Content-Type` con parámetros, headers
con casing distinto, schema recursion permitida, JSON Pointer escaping,
`readOnly`/`writeOnly`, body vacío, violations múltiples ordenadas y límites
exactos.

## Alternativas y decisiones

- **Inferir operaciones desde la descripción de constraints:** descartado;
  requeriría heurística o modelo y pertenece a `agent-plan-generation`.
- **Incluir todo OpenAPI en el contexto:** descartado por límite, superficie de
  prompt injection y falta de relevancia.
- **Truncar JSON/schema por bytes:** descartado; un fragmento podría parecer un
  contrato completo. Se omiten unidades y se declara el conteo.
- **Resolver refs HTTP(S):** descartado; rompe el ámbito local, introduce SSRF y
  haría los tests dependientes de red.
- **Usar `operationId` como identidad:** descartado porque es opcional; método y
  path forman una clave disponible para toda operación.
- **Persistir ASTs o cache global:** descartado; complica aislamiento,
  invalidación y concurrencia sin necesidad demostrada.
- **Integrar ya la CLI:** descartado; cambiaría el contrato aprobado de feature
  7 y no es necesario para entregar el inspector verificable.

## Conformidad arquitectónica

El diseño aplica ARCH-006 con contexto en operaciones bloqueables, ARCH-008 al
tratar OpenAPI como dato no confiable, ARCH-009 al excluir payloads de errores y
contexto, ARCH-010 con tests herméticos y ARCH-012 porque el inspector devuelve
resultados sin mutar runs. Conserva modelos propios y dirección hexagonal. Se
ajusta a CONV-002, CONV-003, CONV-004, CONV-005, CONV-007, CONV-008 y CONV-009.
No se solicita excepción.

## Mapa inicial R → tasks

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T7 | Contratos 3.0/3.1 JSON/YAML válidos producen snapshot propio. |
| R2 | T1, T2, T7 | Solo refs locales dentro de root y presupuestos resuelven. |
| R3 | T3, T7 | Catálogo completo y estable con parámetros/schemas efectivos. |
| R4 | T3, T7 | Seguridad efectiva preserva alternativas sin credenciales. |
| R5 | T1, T2, T4, T5, T7 | Todos los fallos exponen categoría/location segura. |
| R6 | T4, T7 | Status selecciona exact/range/default determinísticamente. |
| R7 | T4, T7 | Headers/media/body se validan con violations seguras. |
| R8 | T5, T7 | Contexto solo contiene selección y schemas alcanzables. |
| R9 | T5, T7 | Contexto canónico respeta bytes y declara omisiones. |
| R10 | T2, T4, T5, T7 | Cancelación, aislamiento e inmutabilidad probados. |
| R11 | T1, T2, T6, T7 | Fronteras sustituibles y suite sin red. |
