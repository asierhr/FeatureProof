# CLI local de validación — Tasks

- [x] T1. Crear el entrypoint y el contrato de comando. [R1, R9, R11]
  - Probar subcomando, flags obligatorios/duplicados/desconocidos, positional,
    separación stdout/stderr y códigos previos al run.
  - Implementar `RunCommand` inyectable y un `main` mínimo con señales.
  - Comentar la responsabilidad sistémica de cada entidad y función.

- [x] T2. Implementar el loader estricto del manifiesto JSON v1. [R2, R3, R4, R11]
  - Probar versión, shape, duplicate/unknown keys, trailing data, números exactos,
    unión de inputs, assertions y correspondencia ordenada de constraints.
  - Traducir DTOs privados a configuración y modelos internos sin modificar
    specification YAML v1.
  - Validar el documento completo antes de devolver un manifest utilizable.

- [x] T3. Componer target, HTTP, redacción, policy y validadores. [R2, R3, R11]
  - Probar que `--target` sustituye solo BaseURL, que configuración inválida no
    produce run/artefactos/red y que no existen defaults silenciosos.
  - Registrar exclusivamente `http.request` con permisos explícitos.
  - Reutilizar las fronteras de features 1–6 y mantener errores tipados/seguros.

- [x] T4. Implementar el runner secuencial cancelable. [R4, R5, R6, R9, R10, R11]
  - Probar orden de constraints/steps, presupuesto global, continuidad tras
    resultados funcionales, parada por error interno y agregación.
  - Propagar contexto a todas las fronteras y finalizar señal con estado
    cancelled sin fabricar resultados pendientes.
  - Emitir progreso seguro y códigos 0/1/2/3/130 exactos.

- [x] T5. Definir snapshots terminales y reportes JSON/Markdown. [R6, R7, R11]
  - Probar schemas, orden, timestamps/duraciones, estados y resolución de todos
    los evidence IDs.
  - Escapar Markdown y excluir payloads, descriptions y valores de assertions.
  - Verificar equivalencia semántica de `report.json` y `report.md`.

- [x] T6. Implementar staging y publicación atómica del bundle. [R5, R7, R8, R10, R11]
  - Probar contenido completo, events JSONL, evidence, permisos, colisiones,
    fallos inyectados en cada fase, rename final y limpieza en éxito.
  - Conservar `.incomplete` en fallo; nunca sobrescribir ni publicar parcial.
  - Publicar cancelled con finalización acotada independiente del contexto
    cancelado cuando filesystem/journal lo permitan.

- [x] T7. Completar integración hermética, trazabilidad y verificación. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10, R11]
  - Ejecutar flujos completos passed, failed, inconclusive, error y cancelled
    con servidor/doubles locales, tmpdir, IDs/reloj/señales controlados.
  - Escanear canarios en stdout, stderr, events, evidence, plan y reportes.
  - Ejecutar gofmt check, vet, compilación, tests, race y coverage; mantener al
    menos 85 % en código nuevo/modificado.
  - Registrar `R<n> -> test`, comandos, cobertura y riesgos en
    `progress/local-validation-cli/implementation.md`.
