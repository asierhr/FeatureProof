# Especificación de agent-evaluation-suite

## Estado

- Fase: especificación completada.
- Resultado solicitado al leader: `spec_ready`.
- Bloqueos: ninguno.
- Aprobación humana: pendiente.

## Información utilizada

- Acceptance criteria de la feature 11 en `feature_list.json`.
- Proceso y arquitectura de `AGENTS.md`, `docs/specs.md`,
  `docs/architecture.md`, `docs/conventions.md` y `docs/verification.md`.
- Spec, implementación y review aprobadas de `agent-plan-generation`.
- Contratos actuales de `internal/agentplanning`, `internal/openapi`,
  `internal/model` y el adapter OpenAI de feature 9.

La definición `.codex/agents/spec_author.md` referenciada por el leader no está
presente en el checkout actual. Se aplicaron directamente las obligaciones del
rol descritas en `AGENTS.md` y `docs/specs.md`.

## Decisiones de diseño registradas

- Dataset, casos, criteria, manifests y baselines tienen schemas v1, digests y
  decode estricto; inputs externos solo pueden ser archivos locales dentro de
  un root autorizado.
- Thresholds, hard-fails, fail-fast y tolerancias de regresión son obligatorios
  y versionados en un criteria file explícito. No existen valores ocultos ni
  defaults, por lo que no queda pendiente una decisión de producto.
- El oracle usa `PreparedPlan`, provenance, catálogo, errores tipados y budget
  snapshots; nunca usa conclusiones textuales del modelo como métrica.
- Las invenciones se clasifican por endpoint, capability, identity, credential
  y evidence, con findings sin valores y hard-fail configurable.
- OpenAPI/log/response injection se introduce en campos existentes no
  confiables. Un wrapper captura la frontera real para comprobar canales sin
  modificar el generador de feature 10.
- Replay es offline y reproducible. Live requiere opt-in y selección/budgets
  completos, reutiliza feature 9 y nunca expone el perfil al runner/artifacts.
- La eval no compone executor, tools, target ni evidence store y no ejecuta
  ninguna acción contra producción.
- Los bundles publican `manifest.json` al final; una baseline debe ser completa,
  íntegra y compatible. La comparación no sustituye thresholds absolutos.
- `featureproof eval agent` es un comando explícito separado y nunca forma parte
  implícita de `go test`.
- Toda entidad y función futura nueva o modificada, incluida interna/de test,
  deberá llevar comentario de responsabilidad sistémica.

## Trazabilidad

Los doce requirements cubren todos los acceptance criteria de feature 11 y se
relacionan con nueve tasks que incluyen sus tests. `design.md` contiene el mapa
inicial R→componente→test previsto.

## Artefactos

- `specs/agent-evaluation-suite/requirements.md`
- `specs/agent-evaluation-suite/design.md`
- `specs/agent-evaluation-suite/tasks.md`

El bloque de aprobación humana permanece vacío.
