# Redacción y limitación de evidencias — Tasks

- [x] T1. Definir y validar la policy inmutable de redacción. [R1]
  - Escribir `TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun` para
    límite obligatorio, headers inválidos y todo path inválido del subset.
  - Escribir `TestPolicyFreezesNormalizedConfiguration` para defaults
    irrenunciables, comparación case-insensitive y aislamiento de slices/maps.
  - Implementar errores tipados con categoría y field path sin incluir valores.
  - Compartir la gramática JSON Pointer aprobada sin crear dependencias
    circulares ni una segunda interpretación.

- [x] T2. Implementar saneamiento recursivo y truncación con copia profunda. [R2, R3, R4, R6, R10]
  - Escribir `TestSanitizerRedactsNestedInputsAndOutputsWithoutMutation` para
    objetos, arrays, null, paths escapados y raíz sensible.
  - Escribir
    `TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively`
    para `/headers` en las raíces independientes de input y output, y
    `TestNestedHeadersRequireExplicitSensitivePaths` para otras tools.
  - Escribir `TestSensitiveJSONPathsRedactExactSubtrees` para coincidencias,
    paths ausentes y reglas ancestro/descendiente.
  - Escribir `TestBodyTruncationRecordsExactOriginalAndRetainedBytes` y
    `TestTruncationPreservesUTF8Boundary` para strings y JSON compuesto.
  - Escribir
    `TestSanitizerFailsClosedForUnsupportedCyclicAndNonFiniteValues` y
    `TestRedactionIsDeterministicAndIdempotent`.
  - Comprobar cancelación en cada frontera recursiva y no devolver resultados
    parciales.

- [x] T3. Integrar la policy con la validación previa de assertions. [R5]
  - Escribir `TestSensitivePathsAllowOnlyExistsAndAbsentBeforeEffects` para
    igualdad, ancestro y descendiente de rutas sensibles.
  - Escribir `TestNestedSensitiveValueAssertionRejectsWholePlan` para hojas en
    grupos `all`/`any`, primer field path estable y cero llamadas a policy/tool.
  - Tratar estáticamente `/headers/<nombre>` y sus ancestros/descendientes como
    protegidos, comparando el nombre HTTP sin distinguir casing. Para headers
    anidados usar solo `sensitive_json_paths` explícitos; no inferirlos por el
    nombre de una key durante ejecución.
  - Mantener sin cambios la semántica de assertions no solapadas.

- [x] T4. Hacer fail-closed la creación y persistencia de evidencia. [R6, R7, R9, R10]
  - Escribir
    `TestRedactionFailurePreventsEvidencePersistenceAndPublication` para tipos
    inválidos, cancelación y error de transformación de input/output.
  - Extender `evidence.Record` con metadata de truncación de input/output y
    permitir raíces JSON tratadas —incluido el marcador de raíz—; escribir
    `TestPersistedMetadataIsIntegrityProtected`.
  - Actualizar `FileStore.Get` para usar `json.Decoder.UseNumber`, exigir fin de
    stream y escribir `TestFileStorePreservesExactJSONNumbersAndRejectsTrailingData`
    con entero mayor que 2^53, decimal, exponentes extremos y trailing data.
  - Integrar el sanitizer antes de `Store.Save` en harness y planning executor;
    solo un resultado completo puede producir record u observación.
  - Escribir `TestValueAssertionOnTruncatedBodyIsInconclusive` y propagar la
    causa tipada sin presentar el prefijo como valor completo.
  - Conservar records confirmados de intentos anteriores ante cancelación o
    fallo posterior.

- [x] T5. Construir contexto del modelo desde evidencia tratada. [R4, R8, R9, R10]
  - Escribir `TestModelViewIsDerivedFromEvidenceAndCanBeMoreRestrictive` para
    paths persistentes, paths exclusivos del modelo y reemplazo completo de
    bodies truncados antes de enviarlos al modelo.
  - Escribir `TestModelProjectionFailurePreventsModelCall` y
    `TestSanitizationAndProjectionObserveCancellationWithoutPartialOutput`.
  - Sustituir cualquier resumen construido desde raw tool output por un
    projector que recibe únicamente records confirmados o copias tratadas.
  - Verificar que ninguna configuración puede recuperar o desredactar un valor.

- [x] T6. Propagar metadata segura a journal y reportes. [R9]
  - Añadir eventos/datos estructurados solo con IDs, categorías, paths y tamaños
    necesarios; no incorporar payloads ni errores que interpolen valores.
  - Escribir `TestSensitiveExistenceResultContainsPresenceOnly` para que
    `exists`/`absent` no filtren marcador ni contenido en observed/expected.
  - Adaptar generadores de reporte existentes si los hay; si aún no existe un
    paquete de reporting, probar la representación serializable consumible por
    la feature posterior sin crear una capa vacía.

- [x] T7. Verificar integración, canarios y trazabilidad completa. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10]
  - Escribir
    `TestCanarySecretsNeverAppearInArtifactsEventsReportsOrModelContext` con
    canarios distintos en default headers raíz, header adicional, header
    anidado protegido por path explícito, path JSON, body, input y output;
    escanear bytes y estructuras de todas las superficies.
  - Cubrir éxito, fallo de assertion, inconcluso por truncación, error del
    sanitizer, retry y cancelación usando fakes y almacenamiento temporal.
  - Ejecutar formato, vet, suite completa, race detector y cobertura conforme a
    `docs/verification.md`.
  - Registrar en `progress/evidence-redaction/implementation.md` el mapa exacto
    R1–R10 → tests y resultados, y marcar cada task solo al completarla.
