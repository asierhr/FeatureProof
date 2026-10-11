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
