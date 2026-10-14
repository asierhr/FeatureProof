# Historial

Bitácora append-only de features completadas.

## 2026-10-04 — agent-harness

- Estado final: `done`.
- Resultado: harness agéntico secuencial y acotado con modelo y herramientas
  falsas, validación estructural, registry, policy segura por defecto,
  presupuestos atómicos, retries seguros, evidencia inmutable, journal
  auditable, cancelación propagada y veredicto determinista.
- Spec aprobada por el usuario el `2026-10-04T18:24:43+02:00`.
- Tasks: T1–T7 completadas.
- Trazabilidad: R1–R11 verificados mediante tests concretos en
  `progress/agent-harness/implementation.md`.
- Verificación independiente: `gofmt -l .`, `go vet ./...`, compilación,
  `go test ./...`, `go test -race ./...` y coverage en pass.
- Coverage global: 90.1 %; todos los paquetes modificados superan 85 %.
- Review: aprobada tras resolver REV-1, REV-2 y REV-3; evidencia en
  `progress/agent-harness/review.md`.

## 2026-10-11 — validation-specification

- Estado final: `done`.
- Resultado: loader local de especificaciones YAML v1 con modelos internos
  independientes, decode estricto, validación semántica, versiones explícitas
  y errores tipados con field paths accionables.
- Spec aprobada por el usuario el `2026-10-11T16:51:43+02:00`.
- Tasks: T1–T5 completadas.
- Trazabilidad: R1–R7 verificados mediante tests concretos en
  `progress/validation-specification/implementation.md`.
- Verificación independiente: `gofmt -l .`, `go vet ./...`, compilación,
  `go test ./...`, `go test -race ./...`, 20 repeticiones race de los paquetes
  afectados y coverage en pass.
- Coverage: 90,1 % global; 97,2 % en `internal/specification` y 87,0 % en el
  adapter YAML.
- Review: aprobada tras resolver validación estricta de tipos YAML, wrapping
  de causas del parser y limpieza de artefactos de coverage; evidencia en
  `progress/validation-specification/review.md`.

## 2026-10-11 — multi-step-validation-plan

- Estado final: `done`.
- Resultado: modelo y ejecutor determinista de planes multipaso con validación
  completa del grafo, orden topológico estable, referencias tipadas resueltas
  desde evidencia confirmada, bloqueos y continuación de ramas independientes.
- Spec aprobada por el usuario el `2026-10-11T17:46:51+02:00`.
- Tasks: T1-T6 completadas.
- Trazabilidad: R1-R9 verificados mediante tests concretos en
  `progress/multi-step-validation-plan/implementation.md`.
- Verificación independiente: `gofmt -l .`, `go vet ./...`, compilación,
  `go test ./...`, `go test -race ./...`, 20 repeticiones race y coverage en
  pass.
- Coverage: 90,1 % global; 89,8 % en `internal/planning`; todos los paquetes
  modificados superan 85 %.
- Review: aprobada tras corregir tipos de referencia fuera de contrato,
  snapshots mutables, cobertura de fallos del journal, diagnóstico de ciclos y
  números no finitos; evidencia en
  `progress/multi-step-validation-plan/review.md`.

## 2026-10-11 — assertion-engine

- Estado final: `done`.
- Resultado: motor determinista de assertions con paths acotados, igualdad,
  existencia, ausencia, comparaciones numéricas exactas, grupos `all`/`any`
  anidados y resultados auditables derivados de evidencia terminal.
- Spec aprobada por el usuario el `2026-10-11T18:41:50+02:00`.
- Tasks: T1-T6 completadas.
- Trazabilidad: R1-R9 verificados mediante tests concretos en
  `progress/assertion-engine/implementation.md`.
- Verificación independiente: `gofmt -l .`, `go vet ./...`, compilación,
  `go test ./...`, `go test -race ./...`, 20 repeticiones race y coverage en
  pass.
- Coverage: 90,2 % global; 90,0 % en `internal/assertion` y 90,3 % en
  `internal/planning`.
- Review: aprobada tras corregir números con exponentes extremos, cancelación
  durante compilación y construcción sin evidencia, y recuperar el baseline de
  coverage; evidencia en `progress/assertion-engine/review.md`.

## 2026-10-11 — http-tool

- Estado final: `done`.
- Resultado: adapter `http.request` controlado con target y DNS fijados,
  validación previa a red, redirects revalidados, timeout/cancelación, límites
  de request/response, output estructurado y headers sensibles excluidos.
- Spec aprobada por el usuario el `2026-10-11T19:14:19+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos en
  `progress/http-tool/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race,
  20 repeticiones race y coverage en pass usando servidores locales.
- Coverage: 90,3 % global; 90,4 % en `internal/tools/http` y 90,3 % en
  `internal/planning`.
- Review: aprobada tras limitar cada ejecución a un único dial y medir todos
  los headers realmente serializados antes de DNS; evidencia en
  `progress/http-tool/review.md`.

## 2026-10-11 — evidence-redaction

- Estado final: `done`.
- Resultado: policy inmutable, redacción recursiva y cancelable, truncación
  auditable, evidencia sellada fail-closed y proyección del modelo más
  restrictiva derivada de records confirmados.
- Spec revisada aprobada por el usuario el `2026-10-11T20:43:32+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R10 verificados mediante tests concretos y secretos canario
  en `progress/evidence-redaction/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite, race completa,
  20 repeticiones race y coverage en pass.
- Coverage: 90,5 % global; todos los paquetes modificados superan 85 %.
- Review: aprobada tras fijar `/headers` raíz como ubicación implícita,
  endurecer sellado/copia de evidencias, detectar ciclos nombrados y preservar
  números JSON exactos; evidencia en
  `progress/evidence-redaction/review.md`.

## 2026-10-11 — local-validation-cli

- Estado final: `done`.
- Resultado: comando `featureproof validate` determinista con specification,
  plan manifest JSON v1, target explícito, runtime confiable, progreso seguro,
  códigos 0/1/2/3/130 y bundle terminal publicado atómicamente.
- Spec aprobada por el usuario el `2026-10-11T21:15:19+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos, flujos locales y
  canarios en `progress/local-validation-cli/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race
  fresca, 20 repeticiones race, builds Linux/macOS y coverage en pass.
- Coverage: 91,0 % global; `cmd/featureproof` 100,0 %,
  `internal/localvalidation` 92,3 % e `internal/planmanifest` 92,7 %; todos los
  paquetes modificados superan 85 %.
- Review: aprobada tras endurecer JSON y diagnósticos, introducir filesystem
  inyectable con commit no-replace, cubrir cancelación en todas las fronteras
  y cerrar la convención de comentarios; evidencia en
  `progress/local-validation-cli/review.md`.

## 2026-10-14 — openapi-inspector

- Estado final: `done`.
- Resultado: inspector local de OpenAPI 3.0/3.1 para JSON/YAML, resolución de
  referencias acotada, catálogo determinista con seguridad efectiva,
  validación de responses y contexto reducido con límite y omisiones.
- Spec aprobada por el usuario el `2026-10-14T17:46:38+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos y casos
  adversariales en `progress/openapi-inspector/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race
  fresca, 20 repeticiones race, coverage, `go mod verify`, builds y compilación
  de tests Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Review: aprobada tras cerrar validación estructural OpenAPI, denegación de
  URIs y paths fuera de ámbito, copias profundas, locations opacas, siblings
  de `$ref` 3.1 y canonicalización determinista de headers; evidencia en
  `progress/openapi-inspector/review.md`.
