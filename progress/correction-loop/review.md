# Review independiente de `correction-loop` — séptima revisión

Fecha: 2026-10-16

## Veredicto

**Aprobado.** REV-15 y REV-16 están cerrados, REV-14 no presenta regresiones y
no quedan hallazgos bloqueantes. La implementación satisface R1–R12, las tasks
están completas y los gates relevantes pasan con la única excepción ambiental
preexistente del subtest de symlink de feature 15.

## Precondiciones

- `correction-loop` es la única feature `in_progress`.
- La aprobación humana consta en `specs/correction-loop/requirements.md`.
- T1–T8 están marcadas `[x]`.
- Existe `progress/correction-loop/implementation.md` con trazabilidad
  R1–R12.
- Se inspeccionaron el snapshot completo, los archivos nuevos de
  `internal/correction/`, la integración externa y los tests reales.

## Cierre de hallazgos

### REV-15 — Recovery concurrente

**Cerrado.**

- `NewCoordinator` obtiene todos los intents, crea sus `ownedRun` e instala
  todas las entradas de `runs` bajo `c.mu` antes de arrancar cualquier
  goroutine.
- Los deletes de `resumeComparison` usan el mismo mutex; ya no existe acceso
  concurrente no sincronizado al mapa.
- `TestConcurrentComparisonRecoveryInstallsAllWorkers` prepara intents en dos
  cadenas, libera las leases anteriores, reabre el store y bloquea ambos
  publishers con una barrera determinista.
- El test observa los dos workers instalados antes de liberarlos, hace join de
  ambos, comprueba una comparación terminal por intent y adquiere cada lease
  para demostrar que quedó libre.
- Pasó 50 repeticiones ordinarias y 20 repeticiones con race.

### REV-16 — Conflicto o corrupción del publisher

**Cerrado.**

- `definitivePublicationFailure` clasifica `Conflict` e
  `IntegrityFailure` como fallos definitivos y conserva como recuperables los
  errores transitorios/de contexto.
- `abandonComparison` retira el intent mediante
  `comparison_abandoned` y persiste un único terminal
  `error/integrity_failure`.
- El projector solo acepta `comparison_abandoned` desde finalizing con intent,
  sin comparación funcional, owner vacío y reason
  `infrastructure|integrity_failure`.
- `TestDefinitiveComparisonPublicationFailuresTerminateRecovery` usa el
  publisher local real para un archivo válido de otro digest y para bytes
  corruptos. En ambos casos conserva el artefacto byte-for-byte, registra un
  solo `comparison_abandoned`, cero `comparison_published`, reabre/reproduce
  el mismo terminal y no crea `comparison_recovery_claimed`.
- Pasó 50 repeticiones ordinarias y 20 repeticiones con race.

### REV-14 y cierres previos

**Sin regresión.**

- `comparison_intended` continúa siendo durable antes de la visibilidad y
  enlaza comparación, digest, child locator y lineage.
- Publish, resolve, append y lookup conservan contextos internos frescos.
- Los fallos después de publish y después de resolve convergen tras reopen en
  exactamente un artefacto, un intent, un evento
  `comparison_published` y un terminal coincidente.
- Preparación/commit no publican antes de confirmar usage; los commits y
  publicaciones inciertos se reconcilian idempotentemente.
- Projector, presupuestos acumulados, terminal usage, locks por cadena,
  leases, cancelación/join, failed-first, regresión, contrato v1 y paridad v2
  mantienen sus invariantes y regresiones.

## Trazabilidad R1–R12

| Requirement | Resultado |
|---|---|
| R1 | pass |
| R2 | pass |
| R3 | pass |
| R4 | pass |
| R5 | pass |
| R6 | pass |
| R7 | pass |
| R8 | pass |
| R9 | pass |
| R10 | pass |
| R11 | pass |
| R12 | pass |

## Gates ejecutados

| Comando | Resultado |
|---|---|
| `git diff --check` | pass; solo avisos LF/CRLF |
| `gofmt -l` sobre Go afectado y nuevo | pass |
| `go vet ./...` | pass |
| `go test -run '^$' -count=1 ./...` | pass |
| `go test -count=1 ./internal/correction ./internal/externalagent ./cmd/featureproof` | pass |
| REV-15/REV-16 `-count=50` | pass, 35.356 s |
| REV-14 y cierres previos `-count=20` | pass, 113.883 s |
| REV-15/REV-16 con MSYS2 UCRT64, `-race -count=20` | pass, 28.893 s |
| paquetes afectados con MSYS2 UCRT64, `-race -count=1` | pass |
| `go test -count=1 ./...` | único fallo: subtest preexistente de symlink por privilegio Windows |
| suite global comparable excluyendo el test padre de symlink | pass |
| race global comparable excluyendo el test padre de symlink | pass |
| coverage `internal/correction` | 85.4% statements |
| `go mod verify` | pass |

Para race se antepuso `C:\msys64\ucrt64\bin` a `PATH`, con `CC=gcc` y
`CGO_ENABLED=1`. La exclusión comparable afecta únicamente
`TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors`, cuyo helper
preexistente intenta crear un symlink sin el privilegio requerido en Windows;
el diff de esta feature no modifica `internal/localvalidation`.

## Conclusión

No quedan requirements sin implementación o test, tasks incompletas, fallos
race reproducibles, regresiones contractuales ni violaciones arquitectónicas
materiales. La feature está lista para que el leader la transicione a `done`.
