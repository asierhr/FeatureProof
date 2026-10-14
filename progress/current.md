# Trabajo actual

## Estado

La feature `openapi-inspector` está en estado `done`. La tercera review
independiente aprobó R1-R11, T1-T7 y el cierre de REV-1 a REV-7.

## Última actualización

- Fecha: 2026-10-14
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 y T1-T7 verificados; overlays REV-1 a REV-7, formato, vet,
  suite, race fresca, race x20, coverage, módulos y builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-14
- Trabajo: correcciones REV-5 a REV-7 de `openapi-inspector` completadas.
- Resultado: paths URI raíz denegados antes de filesystem; siblings de `$ref`
  3.1 preservados mediante composición; headers duplicados por casing
  rechazados de forma determinista, incluido `Content-Type`.
- Verificación: formato, vet, suite fresca, race completa, race x20, coverage,
  `go mod verify`, builds y compilación de tests Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Estado: preparada para tercera review; continúa `in_progress`.

- Fecha: 2026-10-14
- Trabajo: segunda review de `openapi-inspector` rechazada; REV-1 a REV-4
  confirmadas como corregidas y REV-5 a REV-7 devueltas al implementer.
- Hallazgos: path URI con raíz cruza filesystem en Windows; OpenAPI 3.1 pierde
  siblings de `$ref`; headers observados duplicados por casing producen un
  resultado no determinista.
- Estado: requiere correcciones multiplataforma, regresiones de catálogo/
  response/context y tercera review.

- Fecha: 2026-10-14
- Trabajo: correcciones REV-1 a REV-4 de `openapi-inspector` completadas.
- Resultado: matriz de campos OpenAPI obligatorios; denegación URI previa a
  lectura; copia profunda de enums JSON; locations opacas y serialización de
  errores sin tokens externos.
- Verificación: formato, vet, suite fresca, race completa, race x20, coverage,
  `go mod verify` y builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,2 % en `internal/openapi`.
- Estado: preparada para segunda review; continúa `in_progress`.

- Fecha: 2026-10-14
- Trabajo: primera review de `openapi-inspector` rechazada; REV-1 a REV-4
  devueltas al implementer.
- Hallazgos: validación incompleta de campos OpenAPI obligatorios, esquemas URI
  no locales mal clasificados, copia superficial de `Schema.Enum` y locations
  que filtran nombres no confiables.
- Verificación base: formato, vet, suite, race, coverage 91,1 %, cross-builds
  y `go mod verify` pasan; los cuatro casos adversariales fallan.
- Estado: `in_progress`; requiere correcciones, regresiones y nueva review.

- Fecha: 2026-10-14
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-14T17:46:38+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  fixtures locales, canarios y verificación canónica.

- Fecha: 2026-10-14
- Trabajo: spec de `openapi-inspector` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/openapi-inspector/`.
- Verificación: R1-R11 contienen criterios observables; T1-T7 cubren todos los
  requisitos; las tareas siguen pendientes y la aprobación humana está
  pendiente.
- Alcance: carga local OpenAPI 3.0/3.1, refs acotadas, catálogo/security,
  validación determinista de responses y contexto reducido seleccionado.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: preflight de `openapi-inspector` completado e inicio de la redacción
  de `specs/openapi-inspector/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-7 `done` y ninguna feature `in_progress`.
- Contexto clave: carga local OpenAPI 3.0/3.1, referencias acotadas,
  inspección de operaciones, validación determinista de respuestas y contexto
  reducido para el modelo.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 y T1-T7 verificados; formato, vet, suite, race fresca,
  repetición race x20, builds Linux/macOS y regresiones de canarios en pass.
- Coverage: 91,0 % global; todos los paquetes modificados superan 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: REV-6 de `local-validation-cli` corregida tras la segunda review.
- Resultado: paths de errores derivados de mapas externos usan índices
  `members[n]`; nombres del schema siguen siendo legibles; la salida CLI aplica
  un vocabulario cerrado y cae en `document` ante cualquier segmento ajeno.
- Regresiones: claves con CR/LF, tabulador, control, separadores y canarios
  cubiertas en specification, manifest, duplicate keys y planning; stderr se
  mantiene en una sola línea sin contenido externo.
- Verificación: formato, vet, compilación, suite fresca, race completa y race
  x20 en los siete paquetes afectados en pass.
- Coverage: 91,0 % global; 100,0 % cmd, 92,3 % localvalidation, 92,7 %
  planmanifest, 89,9 % planning, 90,6 % assertion, 87,5 % specification/yaml
  y 91,1 % tools/http.
- Estado: preparada para tercera review; continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `local-validation-cli` rechazada; REV-1, REV-3,
  REV-4 y REV-5 confirmadas como corregidas; REV-6 devuelta al implementer.
- Hallazgo: claves JSON controladas pueden filtrarse e inyectar saltos de línea
  en el `field` seguro de stderr.
- Estado: requiere paths públicos con segmentos confiables, regresiones de
  control characters/canarios y tercera review.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 a REV-5 de `local-validation-cli` completadas.
- Resultado: decode anidado estricto; diagnósticos CLI tipados y seguros;
  filesystem/publicación inyectable con commit no-replace; cancelación
  determinista en policy/evidence/assertions; auditoría de comentarios.
- Verificación: formato, vet, compilación, suite completa, race completa, race
  x20 y compilación cruzada Linux/macOS en pass.
- Coverage: 90,8 % global, 100,0 % en `cmd/featureproof`, 92,3 % en
  `internal/localvalidation` y 92,4 % en `internal/planmanifest`.
- Estado: preparada para segunda review; continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: primera review de `local-validation-cli` rechazada; REV-1 a REV-5
  devueltas al implementer.
- Hallazgos: strict JSON incompleto en referencias, diagnósticos CLI sin
  categoría/path, filesystem/publicación no totalmente sustituibles,
  cancelación no cubierta durante policy/evidence/assertions y dos comentarios
  sistémicos ausentes.
- Verificación base: formato, vet, suite, race y coverage global 90,5 % pasan.
- Estado: `in_progress`; requiere correcciones, regresiones y nueva review.

- Fecha: 2026-10-11
- Trabajo: T7 completada; `local-validation-cli` preparada para review
  independiente.
- Verificación: formato, vet, compilación, suite completa, race completa y race
  x20 en paquetes afectados en pass.
- Coverage: 90,5 % global, 100,0 % en `cmd/featureproof`, 89,6 % en
  `internal/localvalidation` y 93,8 % en `internal/planmanifest`.
- Trazabilidad: R1–R11 enlazados con tests reales en
  `progress/local-validation-cli/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: T1–T6 completadas para `local-validation-cli`.
- Resultado: comando estricto, manifiesto JSON v1, composición de features
  1–6, runner secuencial, reportes seguros y bundle atómico implementados.
- Verificación intermedia: suite completa en pass; coverage de paquetes nuevos
  `localvalidation` 85,2 % y `planmanifest` 85,0 %. Pendiente T7 y verificación
  canónica con race detector.

- Fecha: 2026-10-11
- Trabajo: implementación iniciada por T1 tras verificar la aprobación SDD
  y los contratos reutilizables de las features 1–6.
- Estrategia: contrato del comando primero, manifiesto estricto después y
  composición, ejecución y publicación en el orden aprobado.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T21:15:19+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  pruebas herméticas, canarios y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `local-validation-cli` completada y transición de `pending`
  a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/local-validation-cli/`.
- Verificación: R1-R11 contienen criterios observables; T1-T7 cubren todos los
  requisitos; las tareas siguen pendientes y la aprobación humana está vacía.
- Contrato: `--plan` JSON v1 incluye planes, assertions y runtime explícito;
  target, exit codes, cancelación y publicación atómica quedan cerrados.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: la instrucción humana de continuar se registra como aceptación de
  la propuesta `runtime`; transición de `blocked` a `pending` realizada por el
  leader.
- Decisión: el documento versionado de `--plan` incluye configuración explícita
  de HTTP, redacción y policy, validada antes de componer capacidades.
- Siguiente fase: cerrar la spec concreta y presentarla para aprobación SDD.

- Fecha: 2026-10-11
- Trabajo: tercer bloqueo de la spec de `local-validation-cli` y transición de
  `pending` a `blocked` realizada por el leader.
- Ambigüedad: las fronteras HTTP, redaction y policy requieren configuración
  explícita que no existe en YAML v1 ni en los flags acordados.
- Propuesta: incluir una sección `runtime` confiable y validada dentro del
  documento versionado recibido mediante `--plan`.
- Informe y pregunta: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: segundo conjunto de decisiones humanas registrado y transición de
  `blocked` a `pending` realizada por el leader.
- Decisión: `--target` obligatorio sustituye la URL del YAML; exit codes
  0/1/2/3/130; publicación atómica sin sobrescritura, bundle cancelado cuando
  sea seguro y temporal incompleto conservado para diagnóstico si falla la
  persistencia.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: segundo bloqueo de la spec de `local-validation-cli` y transición
  de `pending` a `blocked` realizada por el leader.
- Decisiones pendientes: precedencia de `--target`, valores de exit code y
  semántica atómica de colisiones, cancelación y fallos de persistencia.
- Propuesta completa: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: decisión humana registrada y transición de `blocked` a `pending`
  realizada por el leader.
- Decisión: `featureproof validate` recibirá un segundo archivo versionado con
  planes y assertions mediante `--plan <ruta>`, además de `--spec` y
  `--target`.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: spec de `local-validation-cli` bloqueada y transición de `pending`
  a `blocked` realizada por el leader.
- Ambigüedad: el YAML v1 no contiene planes ni assertions y no existe todavía
  un productor determinista que los derive del texto de las constraints.
- Decisión propuesta: recibir un archivo versionado explícito mediante
  `--plan <ruta>` junto con `--spec` y `--target`.
- Informe y pregunta: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `local-validation-cli` completado e inicio de la
  redacción de `specs/local-validation-cli/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-6 `done` y ninguna feature `in_progress`.
- Contexto clave: componer el flujo local, persistir el layout completo,
  presentar progreso seguro y mapear resultados a códigos de salida.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R10 trazados a tests; formato, vet, compilación, suite, race,
  repetición race, canarios y coverage en pass.
- Coverage: 90,5 % global; todos los paquetes modificados superan 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: T7 de la spec revisada de `evidence-redaction` completada y feature
  preparada para una nueva revisión independiente.
- Resultado: canarios cubren headers raíz default/adicionales, header anidado
  con path explícito, paths JSON, body, input, output y proyección del modelo;
  persistencia numérica y fin de stream quedan trazados a regresiones.
- Verificación: formato, vet, compilación, suite completa, repetición funcional
  x20, race completa y race x20 en pass. Coverage global 90,5 %; `evidence`
  91,0 %, `assertion` 90,6 %, `planning` 89,9 %, `harness` 88,0 % y `jsonpath`
  100,0 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: T2-T4 reabiertas de `evidence-redaction` completadas conforme a la
  spec revisada y aprobada.
- Resultado: headers implícitos limitados a `/headers` raíz exacto; headers
  anidados protegidos solo por paths explícitos; validación estática alineada;
  `FileStore.Get` conserva tokens numéricos con `UseNumber` y exige EOF.
- Verificación intermedia: paquetes afectados repetidos x20 en pass.
- Siguiente fase: T7, suite canónica, race, coverage y trazabilidad actualizada.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: spec revisada aprobada y transición de `spec_ready` a `in_progress`
  realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T20:43:32+02:00`.
- Alcance revisado: headers automáticos solo en `/headers` raíz; ubicaciones
  anidadas mediante paths explícitos; round-trip exacto de números JSON.
- Siguiente fase: completar tareas reabiertas y repetir review completa.

- Fecha: 2026-10-11
- Trabajo: segunda review rechazada y transición de `in_progress` a
  `spec_ready` por cambio material requerido en la spec.
- Hallazgo de diseño: sin un esquema de output, no puede conocerse antes de
  ejecutar si un path arbitrario contendrá una colección `headers` recursiva.
- Trabajo independiente autorizado: corregir round-trip exacto de
  `json.Number` en `FileStore`; la regla de headers queda pausada.
- Estado: requiere spec revisada y nueva aprobación humana antes de reanudar.

- Fecha: 2026-10-11
- Trabajo: REV-1 a REV-4 de `evidence-redaction` corregidas tras la primera
  revisión y preparadas para una nueva revisión independiente.
- Resultado: ancestros de headers protegidos en assertions; resultado del
  sanitizer opaco; records con copia profunda y sello revalidable; ciclos de
  colecciones nombradas detectados desde su identidad original; race
  reproducible mediante la toolchain UCRT64 de MSYS2.
- Verificación: formato, vet, compilación, suite completa, race completa y race
  x20 en paquetes afectados en pass. Coverage global 90,5 %; `evidence` 91,2 %,
  `assertion` 90,6 %, `planning` 89,9 %, `harness` 88,0 % y `jsonpath` 100,0 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: primera review de `evidence-redaction` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: solapamiento incompleto de ancestros de headers sensibles, sello
  de evidence fabricable/aliases mutables, ciclos en colecciones nombradas y
  race detector no reproducible con la configuración usada.
- Estado: `in_progress`; requiere correcciones, tests de regresión, toolchain
  race reproducible y nueva review.

- Fecha: 2026-10-11
- Trabajo: implementación T1-T7 de `evidence-redaction` completada y preparada
  para revisión independiente.
- Resultado: policy inmutable, sanitizer recursivo/cancelable, persistencia
  sellada fail-closed, assertions sensibles restringidas, truncación auditable,
  proyección monotónica del modelo y journal sin payloads.
- Verificación: formato, vet, compilación, suite, race completa, race x20 y
  coverage en pass; 90,5 % global y todos los paquetes modificados >= 85 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: T1-T3 de `evidence-redaction` completadas. Policy inmutable,
  JSON Pointer compartido, redacción recursiva/cancelable, headers protegidos,
  truncación UTF-8 y rechazo previo de comparadores sobre paths sensibles.
- Verificación intermedia: `go test ./internal/...` en pass.
- Siguiente fase: cerrar la frontera sellada de persistencia (T4), proyección
  del modelo (T5) y propagación/canarios (T6-T7).
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T19:51:22+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  canarios y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `evidence-redaction` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/evidence-redaction/`.
- Verificación: R1-R10 contienen criterios de aceptación; T1-T7 cubren todos
  los requisitos; tareas pendientes y aprobación humana vacía.
- Decisión incorporada: solo `exists`/`absent` pueden solaparse con rutas
  sensibles; cualquier comparación de valor se rechaza antes del run.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: decisión humana registrada y transición de `blocked` al estado
  anterior `pending` realizada por el leader.
- Decisión: las rutas sensibles solo admiten assertions `exists`/`absent`; los
  operadores que necesiten leer el valor se rechazan antes del run.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: spec de `evidence-redaction` bloqueada y transición de `pending` a
  `blocked` realizada por el leader.
- Ambigüedad: no está definido si una assertion puede evaluar el valor original
  de una ruta sensible o debe limitarse a comprobar su presencia/ausencia.
- Informe y pregunta: `progress/evidence-redaction/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `evidence-redaction` completado e inicio de la redacción
  de `specs/evidence-redaction/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-5 `done` y ninguna feature `in_progress`.
- Contexto clave: sustituir el sanitizer superficial por una frontera previa a
  persistencia/modelo que soporte rutas anidadas, límites y vistas distintas.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: segunda review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,3 % global; 90,4 % en `internal/tools/http` y 90,3 % en
  `internal/planning`.
- Limpieza: eliminado el perfil temporal `$cov` generado en la raíz.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: primera review de `http-tool` rechazada; correcciones devueltas al
  implementer.
- Hallazgos: el dial probaba varias IPs tras un fallo, incumpliendo cero
  retries, y el límite de request headers no incluía headers automáticos del
  transporte.
- Estado: `in_progress`; requiere un único dial, tamaño completo previo a DNS,
  tests de regresión y nueva review.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T19:14:19+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales y
  verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `http-tool` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/http-tool/`.
- Verificación: R1-R11 contienen criterios de aceptación; T1-T7 cubren todos
  los requisitos; tareas pendientes y aprobación humana vacía.
- Decisión de alcance: no hay defaults silenciosos; target, métodos, headers,
  timeout, límites y redirects se configuran explícitamente fuera del input.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `http-tool` completado e inicio de la redacción de
  `specs/http-tool/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-4 `done` y ninguna feature `in_progress`.
- Contexto clave: el adapter debe quedar detrás del registry, resolver siempre
  contra el target autorizado, revalidar redirects y no persistir evidencia.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R9 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,2 % global; 90,0 % en `internal/assertion` y 90,3 % en
  `internal/planning`.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `assertion-engine` rechazada; REV-1 y REV-2
  confirmadas como corregidas y REV-3 devuelta al implementer.
- Hallazgo: coverage global reproducible de 89,0 %, por debajo del 90,1 % de la
  feature anterior y sin justificación aprobada.
- Estado: `in_progress`; requiere tests de comportamiento relevantes hasta
  recuperar el baseline, reverificación y tercera review.

- Fecha: 2026-10-11
- Trabajo: primera review de `assertion-engine` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: la representación numérica expandía o rechazaba exponentes JSON
  extremos y la cancelación no se observaba durante compilación ni al construir
  resultados sin evidencia.
- Estado: `in_progress`; requiere comparación decimal acotada, propagación de
  contexto por ambas rutas, tests deterministas y nueva review.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T18:41:50+02:00`.
- Siguiente fase: implementación T1-T6 con TDD, comentarios funcionales y
  verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `assertion-engine` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/assertion-engine/`.
- Verificación: R1-R9 contienen criterios de aceptación; T1-T6 cubren todos los
  requisitos; bloque de aprobación humana pendiente y código productivo sin
  cambios.
- Revisión del leader: se eliminó la ambigüedad de tokens numéricos en paths;
  las formas con signo o ceros iniciales son inválidas globalmente.

- Fecha: 2026-10-11
- Trabajo: preflight de `assertion-engine` completado e inicio de la redacción
  de `specs/assertion-engine/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes, features 1-3 `done` y ninguna feature
  `in_progress`.
- Contexto clave: debe implementar el puerto `StepAssertionEvaluator` creado
  por la feature 3 sin introducir participación del modelo en el resultado.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: quinta review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R9 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,1 % global; 89,8 % en `internal/planning`; paquetes modificados
  por encima del mínimo de 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-5 y REV-6 de `multi-step-validation-plan`
  implementadas tras la cuarta revisión.
- REV-5: el diagnóstico comprueba pertenencia real al ciclo y escoge el nodo
  cíclico de menor declaration order, sin confundir descendientes bloqueados.
- REV-6: literales `NaN`, `+Inf` y `-Inf` se rechazan como `invalid_value`
  antes de policy o herramienta.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 90,1 % y `planning` 89,8 %.
- Estado: correcciones preparadas para una quinta revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: cuarta review de `multi-step-validation-plan` rechazada; REV-1 a
  REV-4 confirmadas como corregidas y REV-5/REV-6 devueltas al implementer.
- Hallazgos: el diagnóstico de ciclos podía señalar un descendiente externo al
  ciclo y los literales aceptaban `NaN` e infinitos, incompatibles con JSON.
- Estado: `in_progress`; requiere diagnóstico sobre nodos realmente cíclicos,
  rechazo de floats no finitos, tests de regresión y quinta review.

- Fecha: 2026-10-11
- Trabajo: corrección REV-4 de `multi-step-validation-plan` implementada tras
  la tercera revisión.
- REV-4: añadido journal fake configurable y tests de fallo antes de ejecutar
  y después de confirmar evidencia; prueban error operativo, detención de
  selección, preservación del prefijo confirmado y ausencia de
  `plan_completed`.
- Código productivo: revisado; la propagación existente ya detenía el ejecutor
  correctamente, por lo que la corrección requerida es de evidencia de tests.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 90,1 % y `planning` 89,8 %.
- Estado: corrección preparada para una cuarta revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: tercera review de `multi-step-validation-plan` rechazada; REV-1 a
  REV-3 confirmadas como corregidas y REV-4 devuelta al implementer.
- Hallazgo: faltaban tests del fallo operativo del journal antes de ejecutar y
  después de confirmar evidencia, incluida la preservación de hechos previos.
- Estado: `in_progress`; requiere fake de journal configurable, tests de ambas
  fronteras, reverificación y cuarta review.

- Fecha: 2026-10-11
- Trabajo: corrección REV-3 de `multi-step-validation-plan` implementada tras
  la segunda revisión.
- REV-3: cada `PathSegment` clona ahora individualmente sus punteros `Field` e
  `Index`; el test de snapshot muta ambos desde el plan original y desde
  `ValidatedPlan.Plan()` sin alterar el path validado.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 89,8 % y `planning` 89,2 %.
- Estado: corrección preparada para una tercera revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `multi-step-validation-plan` rechazada; REV-1 y
  REV-2 confirmadas como corregidas y REV-3 devuelta al implementer.
- Hallazgo: los punteros `Field`/`Index` de `PathSegment` seguían compartidos
  entre el plan original, el snapshot interno y las copias devueltas.
- Estado: `in_progress`; requiere clonado individual de esos punteros, tests de
  regresión, reverificación y tercera review.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `multi-step-validation-plan`
  implementadas tras la primera revisión.
- REV-1: eliminado `null` del contrato cerrado de `value_type`; un test prueba
  `invalid_reference` antes de policy o herramienta.
- REV-2: las definitions de assertions se copian recursivamente, incluidos
  maps, slices y arrays anidados; el test de snapshot cubre mutaciones desde el
  plan original y desde `Plan()`.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 89,7 % y `planning` 89,0 %.
- Estado: correcciones preparadas para una nueva revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: primera review de `multi-step-validation-plan` rechazada; corrección
  devuelta al implementer.
- Hallazgos: `value_type: null` se aceptaba fuera del conjunto cerrado de R3
  y la copia de definitions de assertions no era profunda para maps/slices.
- Estado: `in_progress`; requiere corrección, reverificación y nueva review.

- Fecha: 2026-10-11
- Trabajo: T6 completada; implementación de `multi-step-validation-plan`
  preparada para revisión independiente.
- Verificación final: formato, vet, compilación, suite completa, `-race`,
  `-race -count=20` y coverage en pass. Coverage global 90,4 % y paquete
  `internal/planning` 90,5 %.
- Trazabilidad: R1-R9 enlazados con tests reales en
  `progress/multi-step-validation-plan/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: T1-T5 completadas para `multi-step-validation-plan`.
- Resultado: modelo y errores tipados, validador de grafo con orden topológico
  estable, referencias desde evidencia confirmada, ejecutor secuencial con
  controles/retries/bloqueos y eventos reconstruibles implementados.
- Verificación intermedia: `go test ./internal/planning ./internal/harness
  ./internal/evidence` en pass; coverage del paquete nuevo `planning`: 89,1 %.
- Siguiente paso: T6, suite canónica completa, race detector y trazabilidad.

- Fecha: 2026-10-11
- Trabajo: T1 iniciada para `multi-step-validation-plan`; definición mediante
  tests del modelo de plan, referencias tipadas, estados y errores estructurados.
- Integración prevista: nuevo paquete `internal/planning` sobre los puertos ya
  existentes de registry, policy, presupuesto, evidence y journal.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T17:46:51+02:00`.
- Siguiente fase: implementación T1-T6, comentarios funcionales en cada
  entidad y función, y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `multi-step-validation-plan` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/multi-step-validation-plan/`.
- Verificación: R1–R9 contienen criterios de aceptación; T1–T6 cubren todos
  los requisitos; las tareas siguen pendientes y el bloque de aprobación
  humana permanece vacío.
- Alcance acordado: la semántica completa de assertions queda para la feature
  4; esta feature define su declaración y el puerto de evaluación.
- Código productivo modificado: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `multi-step-validation-plan` completado e inicio de la
  redacción de `specs/multi-step-validation-plan/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes, dependencias previas `done` y ninguna feature
  `in_progress`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: convención de comentarios aplicada también a `agent-harness`.
- Cambios: entidades, interfaces, adapters, constructores, métodos, helpers y
  closures de orquestación documentados por su responsabilidad sistémica.
- Verificación: `gofmt` sin diferencias, tests de los cinco paquetes de la
  feature 1 y `go vet` en pass.
- Comportamiento y estado de la feature: sin cambios; continúa `done`.

- Fecha: 2026-10-11
- Trabajo: documentación editorial de `validation-specification` ampliada.
- Cambios: comentarios de responsabilidad sistémica añadidos a entidades,
  errores, DTO y funciones; convención registrada en `design.md` para futuras
  iteraciones.
- Verificación: `gofmt -l internal/specification` sin salida,
  `go test ./internal/specification/...` y `go vet` en pass.
- Comportamiento y estado de la feature: sin cambios; continúa `done`.

- Fecha: 2026-10-11
- Trabajo: segunda review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1–R7 trazados a tests; suite, race detector, repetición race y
  coverage 90,1 % en pass; resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: primera review de `validation-specification` rechazada; corrección
  iniciada para tipos escalares YAML estrictos, preservación de causas y
  limpieza de perfiles de coverage generados en la raíz.
- Hallazgos: REV-1, REV-2 y REV-3.
- Correcciones: validación estricta de tipos escalares y field paths añadida;
  causa original envuelta con `%w`; perfil `specification` eliminado.
- Reverificación con Go 1.25.1: formato, vet, compilación, tests, race,
  `race -count=20` y coverage pasan. Coverage global 90,1 %; paquetes nuevos
  97,2 % y 87,0 %. Perfil generado en el directorio temporal del sistema.

- Fecha: 2026-10-11
- Trabajo: primera review de `validation-specification` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: tipos escalares YAML no comprobados en campos string (R3/R5),
  causa original del parser no preservada (R5) y perfil de coverage generado
  dentro del repositorio.
- Estado: `in_progress`; requiere corrección, reverificación y nueva review.

- Fecha: 2026-10-11
- Trabajo: precondiciones de implementación verificadas; iniciada T1 con tests
  del modelo interno, identificadores, URL, constraints y límites.
- Alcance: exclusivamente `validation-specification`; `internal/harness` no se
  modifica.
- T1: completada; modelo interno independiente y validación de fronteras
  verificados con `go test ./internal/specification`.
- T2: iniciada; tests de categorías, field paths, duplicados, referencias y
  primer error determinista.
- T2: completada; `ValidationError` soporta `errors.Is`/`errors.As` y el orden
  del primer error es determinista.
- T3: iniciada; adapter YAML v1 estricto con lectura local y cancelación.
- T3: completada; carga v1, versión prioritaria, campos/claves estrictos,
  documentos extra, errores de lectura/sintaxis y cancelación pasan tests.
- T4: iniciada; fixtures de compatibilidad, hermeticidad y cargas concurrentes.
- T4: completada; fixtures v1 válidos/inválidos, literales sin expansión,
  ausencia de red y cargas concurrentes sin estado compartido verificadas.
- T5: iniciada; verificación canónica y trazabilidad final.
- T5: completada con Go 1.25.1; formato, vet, compilación, tests, race y
  coverage pasan. Coverage global 90,8 %, specification 97,2 % y adapter YAML
  89,3 %. Informe: `progress/validation-specification/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T16:51:43+02:00`.
- Siguiente fase: implementación T1–T5 y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `validation-specification` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/validation-specification/`.
- Verificación: R1–R7 con criterios de aceptación; T1–T5 referencian todos los
  requirements; bloque de aprobación humana pendiente.
- Código productivo modificado: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `validation-specification` completado e inicio de la
  redacción de `specs/validation-specification/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes y ninguna feature `in_progress`.
- Código productivo modificado: no.

- Fecha: 2026-10-04
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1–R11 trazados a tests; suite, race detector y coverage 90.1 %
  en pass; resumen añadido a `progress/history.md`.

- Fecha: 2026-10-04
- Trabajo: segunda review rechazada; REV-1 y REV-2 resueltos, REV-3 devuelto
  al implementer.
- Hallazgo: agotar retries o tool-call budget durante un retry no termina el
  run de forma inconclusa y puede consumir presupuesto parcialmente (R4/R10).

- Fecha: 2026-10-04
- Trabajo: primera review rechazada; correcciones devueltas al implementer.
- Hallazgos: cancelación del evidence store clasificada incorrectamente (R9)
  y errores obligatorios del journal ignorados (R8).
- Siguiente paso: corregir, repetir la suite canónica y solicitar nueva review.

- Fecha: 2026-10-04
- Trabajo: precondiciones de implementación verificadas; iniciada T1 con tests
  de configuración, transiciones y reserva de presupuesto.
- Alcance: exclusivamente `agent-harness`.
- T1: completada; tests de estado y presupuesto pasan.
- T2/T3: contratos, journal y store local en curso; aún no cerradas.
- T2–T6: completadas y verificadas con tests locales herméticos.
- T7: completada con Go 1.25.1. `gofmt`, `go vet`, compilación, tests,
  `go test -race ./...` y coverage pasan; coverage global 90.8 %.
- Informe: `progress/agent-harness/implementation.md`.
- Primera review corregida: cancelación en persistencia/recuperación y
  confirmación obligatoria de todos los eventos del journal.
- Reverificación completa con Go 1.25.1: formato, vet, compilación, tests, race
  y coverage pasan; coverage global final 90.1 %.
- Segunda review corregida: agotamiento de retries y tool calls reserva ambos
  presupuestos atómicamente y termina el run de forma inconclusa y auditable.

- Fecha: 2026-10-11
- Trabajo: T1-T5 de `assertion-engine` implementadas y verificadas incrementalmente.
- Código: contratos tipados de resultado en `planning`; puerto obligatorio de validación; paquete `internal/assertion` con compilador, paths, comparación exacta, grupos y adapter del executor.
- Tests: validación previa, paths, operadores, grupos completos, resultados inmutables, falta de evidencia, cancelación e integración real con bloqueo de dependientes.
- Estado: pendiente T6, verificación canónica y trazabilidad final.

- Fecha: 2026-10-11
- Trabajo: T6 completada para `assertion-engine`; implementación preparada para revisión independiente.
- Verificación: gofmt, vet, compilación, suite completa, race completa y repetición race x20 en paquetes afectados en pass.
- Coverage: 90,6 % global; 91,6 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Trazabilidad: R1-R9 vinculados a tests reales en `progress/assertion-engine/implementation.md`.
- Estado: `in_progress`; el implementer solicita review y no modifica `feature_list.json`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `http-tool` implementadas tras la primera review.
- REV-1: selección determinista de una sola IP fijada y exactamente un intento de dial por `Execute`, incluso si DNS devuelve alternativas.
- REV-2: medición pre-red del bloque completo de headers serializados y supresión de `Accept-Encoding`/`Connection: close` automáticos no medidos; redirects usan el mismo límite antes del siguiente dial.
- Tests de regresión: varias IPs con primer dial fallido; límite menor que headers automáticos con cero resolver/dial; conteo explícito de Host, User-Agent, Content-Length y headers controlados.
- Estado: pendiente reverificación canónica y nueva review independiente.

- Fecha: 2026-10-11
- Trabajo: REV-1 y REV-2 reverificadas y preparadas para segunda review independiente.
- Verificación: formato, vet, compilación, suite completa, race completa, race x20 y coverage en pass.
- Coverage final: 90,3 % global, 90,4 % en `internal/tools/http` y 90,3 % en `internal/planning`; baseline global 90,2 % preservado.
- Estado: `in_progress`; el implementer solicita nueva review y no modifica `feature_list.json` ni `review.md`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `assertion-engine` implementadas tras la primera revisión.
- REV-1: representación decimal simbólica con exponente arbitrario y comparación exacta sin expandir potencias; tests con exponentes fuera de `int32` y coste acotado.
- REV-2: cancelación observada en compilación, construcción de roots sin evidencia y antes de retornos funcionales; tests deterministas sin `time.Sleep`.
- Verificación: gofmt, vet, compilación, suite, race completa y race x20 en pass.
- Coverage: 89,0 % global; 86,1 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Estado: correcciones preparadas para una segunda revisión; feature continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: corrección REV-3 de `assertion-engine` implementada tras la segunda revisión.
- Tests añadidos: límites reales de configuración, ciclos de colecciones, igualdad estricta de objetos/arrays/null/bool, numeric missing y agregación de múltiples roots.
- Coverage: 90,2 % global, recuperando el baseline de 90,1 %; 90,0 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Verificación: gofmt, vet, compilación, suite completa, race completa y race x20 en paquetes afectados en pass.
- Estado: corrección preparada para tercera revisión independiente; feature continúa `in_progress`.
- Fecha: 2026-10-11
- Trabajo: implementación de `http-tool` iniciada; precondiciones SDD verificadas y T1 en curso.
- Alcance: adapter `internal/tools/http` con target y permisos externos al input, sin ownership de run, journal o evidence.
- Estrategia: contratos y errores tipados primero; después validación, scope, transporte fijado, captura acotada e integración.

- Fecha: 2026-10-11
- Trabajo: T1-T5 de `http-tool` implementadas y verificadas con tests locales controlados.
- Resultado: configuración inmutable, input v1, target scope, DNS fijado, transporte sin proxy, redirects, timeout/cancelación, límites, filtrado de secretos y output estructurado.
- Verificación intermedia: `go test -cover ./internal/tools/http ./internal/planning` en pass; cobertura `internal/tools/http` 88,3 % y `internal/planning` 90,3 %.
- Siguiente paso: cerrar integración T6, endurecer límites de headers en transporte y ejecutar T7 canónica.

- Fecha: 2026-10-11
- Trabajo: T6 completada para `http-tool` mediante integración real con registry y plan executor.
- Evidencia: la ejecución directa no toca store/journal; al ejecutar el plan, solo el executor persiste y evalúa status/body estructurados.
- Endurecimiento: el transporte corta el bloque de headers en el límite configurado antes de entregarlo al parser HTTP y clasifica `response_limit`.
- Siguiente paso: T7, suite hermética completa, race, coverage global y trazabilidad R1-R11.

- Fecha: 2026-10-11
- Trabajo: T7 completada; `http-tool` preparada para review independiente.
- Verificación: formato, vet, compilación, suite completa, race completa y repetición race x20 en paquetes afectados en pass.
- Coverage: 90,4 % global, 91,2 % en `internal/tools/http` y 90,3 % en `internal/planning`.
- Trazabilidad: R1-R11 vinculados a tests reales en `progress/http-tool/implementation.md`.
- Estado: `in_progress`; el implementer solicita review y no modifica `feature_list.json`.
# Feature activa: openapi-inspector

- Fecha: 2026-10-14
- Trabajo: implementación iniciada por el implementer tras aprobación humana; T1 en curso.
- Alcance: nuevo paquete `internal/openapi`, sin cambios en CLI, specification ni plan manifest.
- Estrategia: modelos propios y fronteras locales primero; carga/resolución, catálogo, validación y contexto después.
- T1-T2 completadas: modelos/errores/SourceFS propios, parser JSON/YAML estricto y resolución local acotada con cancelación, containment y presupuestos.
- Verificación incremental: `go test -v ./internal/openapi` en pass; T3-T6 y endurecimiento de coverage en curso.
- T3-T6 completadas: catálogo/security estable, schema validation 3.0/3.1 aislada tras puerto propio, responses deterministas, contexto reducido y fixtures herméticos.
- Coverage incremental de `internal/openapi`: 87,8 %; flujo integral 3.0/3.1, cancelación, scope, canarios e inmutabilidad en pass.
- Siguiente paso: T7, verificación canónica completa y trazabilidad final.
- T7 completada: flujo integral load → catalog → response validation → reduced context probado para OpenAPI 3.0/3.1.
- Verificación final: formato, vet, compilación, suite completa, race completa, race ×20 del paquete afectado, coverage y builds Linux/macOS en pass.
- Coverage final: 91,2 % en `internal/openapi` y 91,1 % global; implementación preparada para review independiente.
- Primera review rechazada con REV-1–REV-4: validación estructural incompleta, clasificación URI insuficiente, copia superficial de enums y locations con tokens externos.
- Correcciones implementadas: matriz estructural 3.0/3.1 previa a `Contract`, parsing URI deny-first, clonado JSON recursivo y diagnósticos/violations con identidades opacas y serialización segura.
- Tests de regresión añadidos para campos obligatorios, `urn:`/`mailto:`/`file:`/network-path, mutación concurrente de enums y canarios en nombres dinámicos.
- Estado: reverificación canónica fresca en curso; T1-T7 siguen completas.
- REV-1–REV-4 reverificadas: suite fresca, race completa, race ×20, coverage,
  `go mod verify` y builds Linux/macOS en pass.
- Coverage final tras correcciones: 91,2 % en `internal/openapi` y 91,1 % global.
- Estado: correcciones preparadas para segunda review independiente; feature continúa `in_progress`.
- Segunda review rechazada con REV-5–REV-7: path URI con raíz dependiente del
  SO, pérdida de siblings de `$ref` en Schema Objects 3.1 y headers observados
  ambiguos por casing.
- Correcciones implementadas: denegación URI-root previa al filesystem;
  composición de `$ref` y siblings 3.1 conservada en validación, catálogo y
  contexto con semántica 3.0 intacta; canonicalización estable de headers con
  rechazo `invalid_input` de claves equivalentes.
- Regresiones añadidas para contadores de frontera, semántica 3.0/3.1,
  `required`/`properties`/`minProperties`, contexto reducido y duplicados de
  headers con valores iguales o distintos repetidos 1000 veces.
- Estado: reverificación canónica fresca para tercera review en curso; T1-T7
  siguen completas.
- REV-5–REV-7 reverificadas: formato, vet, compilación, suite fresca, race
  completa, race ×20, coverage, `go mod verify`, builds y compilación de tests
  de `internal/openapi` para Linux/macOS en pass.
- Coverage final tras la segunda ronda: 91,4 % en `internal/openapi` y 91,1 %
  global; baseline preservado.
- Estado: correcciones preparadas para tercera review independiente; feature
  continúa `in_progress`.
