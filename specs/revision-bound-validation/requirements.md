# Validación vinculada a una revisión — Requirements

## Objetivo y problema

Una conclusión funcional solo es atribuible a código y entorno identificados. Esta feature fija una identidad de revisión y una vinculación verificable antes de ejecutar, y la conserva en todos los artefactos de un run. Una nueva corrección inicia otro run.

## Alcance

- Resolver una identidad canónica de repositorio y un objeto commit completo mediante una frontera Git inyectable.
- Rechazar el working tree sucio en esta primera versión.
- Obtener de una fuente de entorno configurada y confiable la identidad del entorno y el commit desplegado; fallar cerrado si no ofrece una atestación verificable.
- Fijar hashes versionados de restricciones, plan aprobado, contrato relevante y configuración no secreta, y propagar una vinculación única al journal, evidencia y artefactos finales.
- Comprobar la vinculación antes de la primera acción y al publicar; no reatribuir runs terminados.

## Fuera de alcance

- Aprovisionar, modificar o destruir entornos; la implementación concreta del adapter preview pertenece a la feature 19.
- Integración GitHub PR, paquetes interoperables y bucle de corrección (features 18, 15 y 17).
- Validar contenido sin commit mediante un digest de working tree, o aceptar una afirmación manual del commit desplegado como atestación.
- Incorporar credenciales, valores sensibles o sus digests derivados a metadatos públicos.

## Actores y términos

- **Solicitante:** inicia un run con repositorio, revisión, configuración y entorno seleccionados explícitamente.
- **Resolvedor de revisión:** frontera confiable que devuelve identidad canónica del repositorio, OID completo existente y estado del working tree.
- **Atestador de entorno:** frontera confiable configurada fuera del modelo que devuelve identidad estable del entorno y commit efectivamente desplegado, con procedencia comprobable; un valor autodeclarado por el solicitante no basta.
- **Binding:** snapshot inmutable y versionado de `run_id`, repositorio, commit, entorno y hashes no secretos. Los hashes SHA-256 se codifican en hexadecimal minúscula sobre proyecciones canónicas versionadas.
- **Plan aprobado:** plan exacto que recibió autorización humana cuando la política la exige; sin aprobación, el hash corresponde al plan preparado validado y se registra ese modo explícitamente.

## R1 — Revisión canónica antes de ejecutar

WHEN a validation run is admitted, THE system SHALL resolve the selected repository to one canonical identity and the selected revision to one existing, complete, immutable commit OID before starting model, policy or tool work.

### Acceptance criteria

- GIVEN an unambiguous commit in the selected repository WHEN the run starts THEN the binding contains the canonical repository ID and full OID, not an abbreviated ref or mutable branch name.
- GIVEN an unknown, ambiguous, malformed or cross-repository ref WHEN resolution runs THEN no functional execution starts and a typed safe diagnostic identifies the revision boundary.
- GIVEN a branch advances after resolution WHEN the run continues THEN its bound OID remains unchanged.

## R2 — Working tree explícito

IF the selected repository has tracked, staged, untracked or otherwise relevant uncommitted changes, THEN THE system SHALL reject attribution to its HEAD commit before functional execution.

### Acceptance criteria

- GIVEN a dirty worktree WHEN preflight runs THEN no run reports `passed` or `failed` for HEAD and the diagnostic says `dirty_worktree` without file content or paths.
- GIVEN a clean worktree WHEN preflight runs THEN the commit alone identifies source revision; ignored files are excluded according to the configured Git source boundary.

## R3 — Entorno identificado y atestado

WHEN an environment is selected, THE system SHALL obtain its stable identity and deployed commit from a configured trusted attestation source and compare its canonical repository and complete commit to R1 before starting functional checks.

### Acceptance criteria

- GIVEN a matching trusted attestation WHEN admission completes THEN binding includes environment ID and attestation reference or safe provenance ID.
- GIVEN a different repository or commit WHEN admission runs THEN no functional result is published; the outcome is an operational rejection or `inconclusive` with `revision_mismatch`.
- GIVEN an absent, unverifiable, expired or inconsistent attestation WHEN admission runs THEN no functional work starts and no claim about the deployed revision is made.

## R4 — Hashes contractuales seguros y estables

WHEN a binding is created, THE system SHALL record separate versioned SHA-256 hashes for source constraints, the exact validated plan authorized for execution, relevant contract context and effective non-secret runtime configuration.

### Acceptance criteria

- GIVEN identical semantic inputs with different map insertion orders WHEN hashed THEN each corresponding digest is identical.
- GIVEN a semantic change to constraints, plan, contract or effective safe configuration WHEN hashed THEN its category digest changes and an existing binding cannot authorize the changed execution.
- GIVEN a plan awaiting approval WHEN its authorization is absent or stale THEN no executable binding is finalized; the digest is recomputed against the plan and contract current at authorization consumption.
- GIVEN a credential or secret-bearing literal in a would-be hash input WHEN projection runs THEN it is excluded by a declared credential reference or input is rejected; no published digest is computed over secret bytes.

## R5 — Una vinculación en todo el run

WHEN a run creates events or evidence and terminal artifacts, THE system SHALL carry one binding ID and equivalent binding fields or reference in `run.json`, `events.jsonl`, evidence records, `report.json` and `report.md`, and SHALL verify each artifact's run and binding origin before final publication.

### Acceptance criteria

- GIVEN a complete valid run WHEN its artifacts are read THEN every event and evidence reference resolves to the same run ID and binding digest as the report and run metadata.
- GIVEN an event or evidence with a foreign, missing or mutated binding WHEN publication runs THEN the final bundle is not committed as valid and an integrity error is returned.
- GIVEN a cancelled or inconclusive run with confirmed audit state WHEN published THEN its revision binding remains visible without implying functional success.

## R6 — Inmutabilidad y nueva corrección

WHEN the selected revision or any bound semantic input changes, THE system SHALL create a new run ID and binding; THE system SHALL NOT rewrite the revision, hashes, events, evidence or report of an earlier run.

### Acceptance criteria

- GIVEN a correction at commit B after a run for commit A WHEN validated THEN B has a distinct run ID and binding while A's files remain byte-for-byte unchanged.
- GIVEN a repeated publication with an existing run ID WHEN attempted THEN no existing artifact is replaced.

## R7 — Confidencialidad de metadatos

THE binding and its public hashes SHALL contain only allowlisted non-secret fields and SHALL NOT include credentials, tokens, raw environment variables, connection strings, sensitive literals, raw attestation payloads or their secret-dependent digests.

### Acceptance criteria

- GIVEN secret canaries in configuration, auth and attestation WHEN all output surfaces are scanned THEN none occurs in IDs, hashes, events, evidence metadata, reports, CLI diagnostics or staged final artifacts.
- GIVEN only a secret value rotates WHEN binding is recomputed THEN public non-secret configuration hash is unchanged, and credential selection remains an opaque non-secret reference if relevant to authorization.

## R8 — Cancelación, integridad y verificación hermética

THE binding workflow SHALL propagate cancellation through revision resolution, attestation, hashing and publication, SHALL fail closed on unverifiable persisted state, and SHALL be testable with fake Git, environment, clock, approval and filesystem boundaries without network or real credentials.

### Acceptance criteria

- GIVEN cancellation at each blocking boundary WHEN observed THEN no further model or tool action starts and confirmed state remains recoverable.
- GIVEN a tampered digest, malformed OID or inconsistent persisted binding WHEN loaded or published THEN verification fails with a typed integrity category.
- GIVEN the ordinary test suite WHEN these cases run THEN no live Git remote, preview service or provider is required.

## Errores y casos límite

- Una referencia mutable se resuelve una sola vez; el OID resuelto gobierna todo el run. Se detecta cambio de inputs entre preflight, aprobación, ejecución y publicación antes de iniciar trabajo nuevo o confirmar un resultado funcional.
- Un commit de otro repositorio, un objeto no-commit, un OID parcial o ausencia de repositorio se rechaza. El formato completo depende del algoritmo de objetos del repositorio; no se trunca.
- La atestación debe estar ligada al entorno seleccionado y a la revisión observada. Si no puede verificarse su procedencia o vigencia, se usa `inconclusive` o error operativo, nunca `failed` funcional.
- Un plan replanificado es un nuevo lote preparado y, cuando corresponda, aprobado; el binding conserva una secuencia de digests de planes/lotes dentro del mismo run sin cambiar repositorio, commit ni entorno.
- Los digests de contenido sirven para detectar cambios semánticos, no para ocultar secretos. La entrada sensible se excluye antes de serializar y hashear.

## Decisiones abiertas

Ninguna para la spec. La forma concreta de obtener atestación preview queda en la feature 19; esta feature define el contrato consumidor y falla cerrado si no existe una fuente confiable configurada.

## Aprobación humana

```yaml
approved_by: user
approved_at: 2026-10-16T10:22:13+02:00
```
