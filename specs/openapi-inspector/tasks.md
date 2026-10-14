# Inspector de contratos OpenAPI — Tasks

- [x] T1. Definir modelos propios, configuración, errores y fronteras locales. [R1, R2, R5, R11]
  - Escribir tests de límites obligatorios, root/entry, categorías y redacción de
    canarios en errores.
  - Definir `SourceFS`, modelos de operación/security/schema/response y copias
    inmutables sin exponer DTOs externos.
  - Comentar la responsabilidad sistémica de cada entidad y función nueva o
    modificada, exportada o interna.

- [x] T2. Implementar carga JSON/YAML y resolución local acotada. [R1, R2, R5, R10, R11]
  - Probar OpenAPI 3.0/3.1, sintaxis, mappings duplicados, versión y documento
    inválidos antes de aceptar un contrato.
  - Probar refs internas, multiarchivo dentro de root, fragments, referencias
    rotas/cíclicas, traversal, absolutos, remotas y symlink escape.
  - Aplicar límites de documentos, bytes, refs y profundidad, propagando
    cancelación y demostrando que targets denegados no se leen.

- [x] T3. Normalizar catálogo, schemas y seguridad efectiva. [R3, R4, R10]
  - Probar orden determinista, operation keys, operationId opcional, merge de
    parámetros por `name`/`in`, request bodies y responses.
  - Probar seguridad global/específica/anónima, alternativas OR, requisitos AND
    y scopes sin representar credenciales.
  - Congelar el snapshot y devolver copias para permitir lecturas concurrentes.

- [x] T4. Implementar selección y validación determinista de responses. [R5, R6, R7, R10]
  - Probar precedencia exact/range/default, status no declarado y operation key
    o input inválidos.
  - Probar headers case-insensitive, required, media types exactos/wildcard,
    body vacío/mal formado/sobredimensionado y schemas 3.0/3.1.
  - Ordenar violations y excluir por completo valores observados y source
    snippets de resultados/errores.

- [x] T5. Construir contexto reducido canónico y acotado. [R5, R8, R9, R10]
  - Probar selección explícita constraint → operation key, keys desconocidas,
    reachability y deduplicación de schemas.
  - Aplicar lista blanca y demostrar con canarios que ejemplos, extensiones,
    free-form docs y operaciones no seleccionadas no aparecen.
  - Empaquetar operaciones completas, declarar conteos de omisión y verificar
    que todos los outputs quedan dentro del límite exacto.

- [x] T6. Completar fixtures y tests de frontera herméticos. [R2, R5, R7, R9, R11]
  - Añadir fixtures mínimos equivalentes 3.0/3.1 JSON/YAML, contratos externos y
    responses estructuradas sin credenciales ni datos de producción.
  - Instrumentar filesystem para fallos/lecturas y demostrar ausencia de red,
    lectura fuera de scope, estado parcial y filtración de canarios.
  - Cubrir JSON Pointer escapes, recursive schemas permitidos, formatos
    annotation-only, `readOnly`/`writeOnly` y múltiples violations.

- [x] T7. Completar integración, trazabilidad y verificación canónica. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Ejecutar flujos completos load → catalog → response validation → reduced
    context para 3.0 y 3.1 con cancelación e inspecciones concurrentes.
  - Ejecutar gofmt check, vet, compilación, tests, race y coverage; mantener al
    menos 85 % en código nuevo/modificado.
  - Registrar `R<n> -> test`, tasks, comandos, cobertura y riesgos en
    `progress/openapi-inspector/implementation.md`.
