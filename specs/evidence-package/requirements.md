# Paquete de evidencias interoperable — Requirements

## Objetivo y problema

Entregar una representación local, versionada y verificable de una validación terminada, para que un consumidor pueda inspeccionar el resultado y sus hechos sin ejecutar FeatureProof ni interpretar afirmaciones del modelo.

## Alcance

- Publicar el paquete dentro del directorio final del run local, junto a los artefactos actuales.
- Inventariar íntegramente los archivos del paquete y comprobar sus referencias e identidad de revisión.
- Exponer restricciones, fallos deterministas, hechos observados y una guía segura de reproducción.
- Leer y validar el paquete sin red ni herramientas de validación.

## Fuera de alcance

- API/MCP para agentes externos, control de acceso remoto y descarga (feature 16).
- Crear un run de corrección o comparar revisiones (feature 17).
- Publicación en GitHub (feature 18) y aprovisionamiento de entornos (feature 19).
- Ejecutar automáticamente las instrucciones de reproducción, certificar que el entorno remoto sigue disponible o firmar criptográficamente el paquete. El digest detecta alteraciones respecto al manifest recibido; no acredita la identidad de quien lo publicó.

## Actores y términos

- **Productor:** proyección determinista del run, journal y registros de evidencia confirmados.
- **Lector offline:** consumidor de un directorio local que solo lee y valida.
- **Hecho:** resultado estructurado de herramienta o assertion confirmada; conserva procedencia.
- **Diagnóstico:** texto o hipótesis del agente, siempre identificado como no autoritativo.
- **Fallo funcional:** assertion determinista `failed`, distinta de `inconclusive`, cancelación y error operativo.
- **Valor seguro:** valor esperado u observado tras la política de redacción y límites; distingue valor presente, ausente, redactado y truncado.
- **Escenario:** identidad estructurada del check cuando existe; en planes locales sin esa identidad se declara `unspecified`, sin inventar una categoría.

## R1 — Esquema versionado y resultado completo

WHEN a bound run reaches publication, THE producer SHALL publish one machine-readable package v1 with manifest, outcome per constraint, failures, observed facts, evidence references and reproduction instructions.

### Acceptance criteria

- GIVEN a terminal passed, failed or inconclusive run WHEN it is published THEN the package contains all six sections with a version integer and stable field names; arrays vacíos son explícitos.
- GIVEN an unknown schema version WHEN the offline reader opens it THEN it rejects it with a typed `unsupported_version` result, without guessing fields.
- GIVEN an inconclusive constraint WHEN published THEN its outcome and cause remain inconclusive and no functional failure is fabricated.

## R2 — Identidad de run y revisión

WHEN a package is produced or read, THE system SHALL verify its run identity and revision binding against the existing sealed `revisionbinding.Binding` and ordered `PlanBinding` chain.

### Acceptance criteria

- GIVEN a valid run WHEN packaged THEN the manifest has the same run ID and binding digest as `run.json`, `report.json` and every referenced evidence record.
- GIVEN a changed commit, repository, environment, binding digest or plan-binding order in one artifact WHEN read THEN integrity validation rejects the package.

## R3 — Separación de hechos y diagnósticos

WHEN the producer projects package content, THE system SHALL derive authoritative statuses and observed facts only from confirmed tool evidence and deterministic assertion outcomes and SHALL identify any agent diagnosis as non-authoritative.

### Acceptance criteria

- GIVEN a model hypothesis contradicting a failed assertion WHEN packaged THEN the failed assertion and constraint status remain unchanged.
- GIVEN text from an agent WHEN included THEN it appears only in a labeled diagnostic field and cannot supply a failure, passed result or evidence reference.

## R4 — Fallos trazables y valores seguros

WHEN a deterministic assertion fails, THE producer SHALL emit a failure with constraint, scenario, check/batch when available, step, assertion declaration and node path, operator, safe expected and observed value representations, and supporting evidence IDs.

### Acceptance criteria

- GIVEN one failed leaf assertion WHEN packaged THEN its IDs locate the result unambiguously and its evidence references resolve to confirmed records in the same run.
- GIVEN a sensitive, absent or truncated expected/observed value WHEN packaged THEN its representation states that condition explicitly and does not reveal the removed bytes.
- GIVEN a failed group assertion WHEN packaged THEN the group/tree path and supporting failed child nodes remain distinguishable; a blocked step without a failed assertion is not mislabeled as a functional failure.

## R5 — Inventario cerrado e integridad de bytes

WHEN publishing or reading a package, THE system SHALL require a manifest with byte size and lowercase SHA-256 digest for every regular artifact except the manifest itself, and SHALL reject missing, modified, duplicated, undeclared or non-regular entries.

### Acceptance criteria

- GIVEN a valid package WHEN one artifact byte changes, is removed or an extra file appears THEN offline validation fails before displaying a trusted result.
- GIVEN a symlink, hard-link ambiguity, path traversal, absolute path, duplicate normalized path or unsupported file type WHEN read THEN validation fails without following it outside the package.
- GIVEN a manifest entry for the manifest itself WHEN read THEN it is rejected; the manifest has a bounded size and strict schema, and the inventory covers every other regular file in the package tree.

## R6 — Publicación indivisible

WHEN publication succeeds, THE producer SHALL validate the complete staged package before the existing no-replace atomic commit makes the final run directory visible.

### Acceptance criteria

- GIVEN an interruption, encoding error, missing evidence or integrity mismatch before commit WHEN publishing THEN the final package path does not appear as valid; staging remains incomplete.
- GIVEN a collision at the final run path WHEN publishing THEN existing content is not replaced.
- GIVEN a successful commit WHEN opening the final path THEN all manifest entries and references validate.

## R7 — Seguridad y límites

WHEN projecting evidence, values, metadata and reproduction instructions, THE producer SHALL apply the established validated redaction policy and bounded output rules before writing package bytes, and SHALL fail closed if safe projection cannot be proven.

### Acceptance criteria

- GIVEN secret canaries in input, output, expected values, diagnostics and credentials WHEN packaged THEN none appears in any artifact or manifest, including strings used for reproduction.
- GIVEN an oversized evidence body WHEN packaged THEN the existing truncation record and safe retained representation survive without restoring removed content.
- GIVEN unsafe arbitrary text or a projection failure WHEN packaging THEN publication fails rather than copying the source verbatim.

## R8 — Lector offline y referencias internas

WHEN an offline reader validates a package, THE reader SHALL check schema, bounded file inventory, digests, revision binding and every internal ID-to-file and cross-document reference before returning a trusted view, without network access or tool execution.

### Acceptance criteria

- GIVEN valid bytes WHEN read in a disconnected environment THEN the reader returns ordered outcomes, failures, facts and reproduction instructions.
- GIVEN a nonexistent evidence ID, wrong evidence filename/ID, foreign run, dangling step/assertion/constraint reference or inconsistent status WHEN read THEN it returns a typed invalid-package error and no trusted view.
- GIVEN model text that claims a reference exists WHEN read THEN it has no effect on validation.

## R9 — Reproducción segura y verificable

WHEN a package is built, THE producer SHALL include bounded reproduction instructions that identify the immutable revision, environment identity, non-secret configuration/plan digests, scenario and step selectors, and required credential references without embedding credential values or executable commands.

### Acceptance criteria

- GIVEN a failed step WHEN packaged THEN the instructions identify its constraint/scenario/step and the evidence IDs to inspect, plus the prerequisite that the selected environment attest to the bound commit.
- GIVEN an authentication requirement WHEN packaged THEN only an opaque credential reference or an explicit `credential_required` marker is present; no secret, raw target URL with credentials or copy/paste command is emitted.
- GIVEN missing safe reproduction metadata WHEN packaged THEN the instruction marks the gap and cannot claim exact automatic replay.

## R10 — Cancelación y resultado operativo

IF cancellation or a publication error occurs, THEN THE producer SHALL propagate the condition and SHALL NOT expose a partial package as a valid final result.

### Acceptance criteria

- GIVEN cancellation during inventory, hashing, validation or before commit WHEN publishing THEN the final path remains absent and the caller receives cancellation.
- GIVEN a terminal cancelled run that can be durably published under a live cleanup context WHEN packaged THEN its status is `cancelled` and it does not contain fabricated functional failures.
- GIVEN an internal packaging error WHEN reporting THEN it is distinct from a functional `failed` outcome.

## Errores y casos límite

Se rechazan versiones desconocidas, JSON inválido o con campos incompatibles, nombres no canónicos, archivos especiales, symlinks, inventarios incompletos, referencias duplicadas o rotas, digests incorrectos, inconsistencias de status, binding o plan, y límites de tamaño/cantidad superados. Un paquete inválido nunca produce una vista parcialmente confiable. El lector puede informar categoría y ruta relativa segura del error, sin copiar bytes no confiables.

## Decisiones abiertas

Ninguna bloqueante. La v1 usa un directorio local como formato transportable; el transporte externo y la autenticación quedan para la feature 16. Los límites concretos del formato y sus valores predeterminados se fijan en el diseño para permitir interoperabilidad.

## Aprobación humana

El usuario aprobó explícitamente esta spec al pedir continuar con la implementación.

```yaml
approved_by: user
approved_at: 2026-10-16T12:02:02+02:00
```
