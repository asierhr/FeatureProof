# Especificación de agent-plan-generation

## Estado

- Fase: especificación completada.
- Resultado solicitado al leader: `spec_ready`.
- Bloqueos: ninguno.
- Aprobación humana: pendiente.

## Información utilizada

- Acceptance criteria de la feature 10 en `feature_list.json`.
- Proceso y arquitectura en `AGENTS.md`, `docs/specs.md`,
  `docs/architecture.md`, `docs/conventions.md` y `docs/verification.md`.
- Contratos aprobados de specification, planning, assertions, redaction,
  OpenAPI inspector y model adapter.
- Código actual de `internal/specification`, `internal/planning`,
  `internal/openapi`, `internal/harness`, `internal/model`, registry y policy.

## Decisiones de diseño registradas

- La generación usa un protocolo `PlanningDecision` v1 propio porque
  `harness.ModelDecision` v1 representa decisiones incrementales y no puede
  expresar cobertura multi-constraint ni contexto insuficiente sin romper su
  contrato.
- El resultado se materializa sobre `planning.ValidationPlan`; no se reemplaza
  el modelo ejecutable existente.
- El generador recibe snapshots descriptor-only y una policy read-only, nunca
  implementaciones de tools ni estado mutable del run.
- Las operaciones HTTP se referencian por `OperationKey` retenida; method/path
  se derivan del contexto confiable y el protocolo no admite URLs, comandos o
  credenciales libres.
- `not_applicable` queda estructurado y sujeto a consistencia; la calidad
  semántica del modelo se medirá en la feature 11.
- La generación realiza una inferencia y no ejecuta, persiste, aprueba ni
  replantea. Features 12 y 13 conservan esas responsabilidades.
- Toda entidad y función nueva o modificada deberá incluir comentario de su
  responsabilidad dentro del sistema.

## Artefactos

- `specs/agent-plan-generation/requirements.md`
- `specs/agent-plan-generation/design.md`
- `specs/agent-plan-generation/tasks.md`

Los doce requirements están vinculados a tasks y tests previstos. El bloque de
aprobación humana permanece vacío.
