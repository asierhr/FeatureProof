# Especificación versionada de validación — Tasks

- [x] T1. Definir modelos internos y el contrato v1 mediante tests. [R1, R2, R3]
  - Añadir tests del modelo esperado para feature, target, constraints y
    límites sin importar tipos YAML.
  - Añadir casos de IDs, descripciones, URL, lista de constraints y límites en
    sus fronteras válidas e inválidas.
  - Implementar los tipos internos mínimos, sin acoplarlos a `harness.Limits`.

- [x] T2. Implementar errores propios y validación semántica determinista. [R3, R5]
  - Escribir tests que comprueben categorías y field paths mediante
    `errors.Is`/`errors.As`, incluidos duplicados y target desconocido.
  - Comprobar que el mismo input informa siempre el mismo primer error y que no
    se devuelve un modelo parcial utilizable.
  - Implementar el validador semántico con causas envueltas cuando existan.

- [x] T3. Implementar el adapter YAML v1 estricto y cancelable. [R1, R2, R3, R4, R5, R6, R7]
  - Añadir tests de archivo válido, inexistente/no legible, YAML mal formado,
    root inválido, documentos extra, claves repetidas y campos desconocidos.
  - Añadir tests de versión ausente, tipo inválido y versión desconocida que
    verifiquen `Received` y `Supported`.
  - Mantener DTO, tags, nodos y errores de la librería YAML dentro del adapter;
    traducir a modelos internos antes de retornar.
  - Observar cancelación antes y después de la lectura y no crear goroutines ni
    retries ocultos.

- [x] T4. Fijar fixtures de compatibilidad y hermeticidad de v1. [R6, R7]
  - Crear fixtures locales representativos de v1 válidos y de errores
    sintácticos, semánticos y de versión.
  - Demostrar que `${NAME}` no se expande y que una URL solo se valida
    sintácticamente, sin abrir conexiones.
  - Verificar que cargas repetidas y concurrentes no comparten estado mutable y
    producen resultados equivalentes.

- [x] T5. Verificar la feature completa y registrar trazabilidad. [R1, R2, R3, R4, R5, R6, R7]
  - Ejecutar `gofmt`, `go vet`, compilación y `go test ./...`; ejecutar también
    race detector y coverage según `docs/verification.md`.
  - Confirmar que los tests no requieren red, variables de entorno ni servicios
    externos y que `internal/harness` conserva su comportamiento.
  - Registrar en `progress/validation-specification/implementation.md` el mapa
    R→test real, comandos, resultados, archivos modificados y cualquier
    limitación observada.
