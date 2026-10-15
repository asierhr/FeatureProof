# Especificación — bounded-replanning

## Estado

`spec_ready`

## Resultado

Se redactaron los tres artefactos SDD de la feature 13:

- `specs/bounded-replanning/requirements.md`;
- `specs/bounded-replanning/design.md`;
- `specs/bounded-replanning/tasks.md`.

La especificación convierte todos los acceptance criteria en R1–R12
verificables. Define el resumen exacto y saneado visible al modelo, el
protocolo cerrado `terminate|insufficient_context|additional_checks`, el
pipeline completo de checks adicionales, la nueva aprobación por batch,
presupuestos no monetarios previos al trabajo, ledger monotónico, fingerprint
semántico anti-loop, lineage, CAS/cancelación/idempotencia, verdict
determinista y pruebas herméticas.

La frase `coste estimado` se resuelve de acuerdo con la decisión ya aprobada en
feature 9: no se incorpora precio, moneda ni tarifas; llamadas, tokens,
duración, replans, pasos, tools y trabajo determinista son las dimensiones
explícitas.

## Alcance respetado

No se modificaron código, tests ni `feature_list.json`. La aprobación humana
permanece vacía en `requirements.md` y debe producirse antes de implementar.

## Transición solicitada

`pending -> spec_ready`
