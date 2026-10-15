# Especificación de plan-approval

## Estado

- Fase: especificación completada.
- Resultado solicitado al leader: `spec_ready`.
- Bloqueos: ninguno.
- Aprobación humana: pendiente.

## Información utilizada

- Acceptance criteria de la feature 12 en `feature_list.json`.
- `AGENTS.md`, `.codex/agents/spec_author.md`, `docs/specs.md`,
  `docs/architecture.md`, `docs/conventions.md` y `docs/verification.md`.
- Specs y contratos implementados de agent harness, planes multi-step,
  redacción de evidencia, CLI local, OpenAPI, runtime de modelo y generación
  agéntica de planes.
- Estado real de `internal/harness`, `internal/planning`,
  `internal/localvalidation`, `internal/evidence`, `internal/openapi` e
  `internal/agentplanning`.

La definición `.codex/agents/spec_author.md` fue leída y se siguió junto con el
proceso SDD obligatorio.

## Decisiones cerradas

- Approval se modela como una frontera de aplicación reusable, no como una CLI
  inventada antes de que exista un comando productivo de generación reanudable.
- `awaiting_approval` es un estado real del run y solo approve vigente puede
  producir `executing`.
- La vista humana es exhaustiva y saneada, pero los hashes cubren snapshots
  semánticos completos y canónicos, no la presentación.
- Context hash cubre target, restricciones, ReducedContext, capabilities,
  runtime/policy y contrato de generación; plan hash cubre checks, steps,
  inputs, assertions y evidence requirements.
- La identidad del aprobador llega autenticada desde composición y siempre se
  registra como subject/source estable.
- Rechazo terminal produce `cancelled`; rechazo con replan, regenerate, edit o
  aprobación stale producen `planning` y nunca un grant.
- Edit y regenerate no ejecutan esos procesos: emiten acciones tipadas y exigen
  reutilizar loaders/generador/validadores antes de otro stage.
- Un grant opaco, ligado a run/plan/revisión/hashes y consumible una vez, es la
  única entrada a ejecución cuando approval está habilitado.
- El store usa compare-and-append por run, revisión monotónica y decision IDs
  idempotentes para impedir doble decisión bajo concurrencia.
- Toda entidad y función futura nueva o modificada, incluidas internas y
  helpers de test, lleva comentario de responsabilidad sistémica.

## Trazabilidad

Los doce requirements cubren todos los acceptance criteria de la feature 12 y
se relacionan con nueve tasks ordenadas que incluyen tests. `design.md`
contiene el mapa inicial `R → componente → prueba prevista`.

## Artefactos

- `specs/plan-approval/requirements.md`
- `specs/plan-approval/design.md`
- `specs/plan-approval/tasks.md`

El bloque de aprobación humana permanece vacío y no se ha modificado código,
tests ni `feature_list.json`.
