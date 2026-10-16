# Paquete de evidencias interoperable — Tasks

Ejecutar en orden. Cada task comienza con tests deterministas que fallen, sigue con implementación mínima y termina con refactorización acotada. No cambiar el estado de la feature ni ampliar el alcance a las features 16–19.

- [x] T1. Definir el contrato v1, límites y errores tipados del paquete y del lector. [R1, R5, R8]
  - Tests de serialización estable, campos obligatorios, versión desconocida y rechazo de formas ambiguas/duplicadas.
  - Documentar en el código el esquema exacto de manifest, índice, valores seguros e instrucciones declarativas.

- [x] T2. Proyectar identidad y resultados desde el run confirmado. [R1, R2, R3, R8]
  - Tests de passed, failed e inconclusive, binding alterado, cadena de planes desordenada, status incoherente e hipótesis de modelo contradictoria.
  - Reutilizar `Binding.Verify`, `VerifyPlanChain`, las verificaciones de secuencia existentes y los resultados deterministas; no derivar estados del texto del modelo.

- [x] T3. Capturar valores seguros y procedencia de fallos antes de la proyección que borra payloads. [R3, R4, R7, R8]
  - Tests de assertion hoja/grupo, constraint/escenario/check/batch/step/declaration/node, valor presente, ausente, redactado y truncado, y referencia ajena.
  - Mantener `report.json` sin payload; fallar cerrado si no se puede probar que expected/observed está saneado.

- [x] T4. Construir tabla de evidencias y hechos observados desde registros confirmados. [R2, R3, R4, R7, R8]
  - Tests de evidencia faltante, ID duplicado, nombre distinto de ID, run/binding ajeno y truncamiento conservado.
  - Verificar `Store.Get` e ID→archivo sin confiar en rutas derivadas de texto externo.

- [x] T5. Generar instrucciones seguras de reproducción. [R7, R9]
  - Tests con credenciales canario, target sensible, revisión/entorno, digests de plan y metadata insuficiente.
  - Emitir solo campos declarativos, referencia opaca de credencial o marcador de prerrequisito, nunca comando ejecutable o secreto.

- [x] T6. Implementar inventario cerrado y lector offline limitado. [R1, R2, R5, R8]
  - Tests de byte alterado, archivo ausente/extra, entrada duplicada, digest/tamaño incorrecto, manifest autoenumerado, límites, symlink/reparse point, hard link detectable, ruta absoluta o traversal y referencia rota.
  - Validar tipo/ruta de archivo antes de abrir, hashear bytes exactos y devolver vista solo después de toda la verificación; probar sin red ni herramientas.

- [x] T7. Integrar escritura y validación en staging con `Publisher`. [R5, R6, R7, R10]
  - Tests de fallo de escritura, canario, interrupción tras distintos artefactos, fallo de fsync, colisión no-replace, cancelación durante hashing y ausencia de directorio final válido.
  - Escribir `package.json`, inventariar, escribir `manifest.json`, validar y confirmar con el commit atómico ya existente.

- [x] T8. Verificar el recorrido completo de paquete y lector. [R1, R2, R3, R4, R5, R6, R7, R8, R9, R10]
  - Test de integración local con herramientas/modelo falsos para resultado funcional fallido, inconclusive y cancelled publicable; lectura offline de cada paquete y mutación adversaria posterior.
  - Confirmar trazabilidad R→test real, compatibilidad de `run.json`/`report.json`, ausencia de canarios y ninguna dependencia de servicios externos.








