# Review independiente 10 — Feature 35: `persistent-control-plane`

## Resultado

**approved**

La implementación satisface R1–R16 y T1–T18, incluida la enmienda R16
aprobada. Los dos bloqueos de la review 9 quedan cerrados: el contrato de
release de environment converge bajo cobertura repetida y todo el árbol pasa
`gofmt`. El gate PostgreSQL 16 aislado termina normal/race/runtime/coverage,
compara perfiles independientes y reproduce exactamente **5420/6370 =
85,08634222919937205651491365777080062794 %**.

## Precondiciones

- Existe exactamente una feature `in_progress`: feature 35.
- La spec original está aprobada por `user` desde
  `2026-10-27T08:20:19+01:00`.
- La enmienda `r16-comment-scope` está aprobada por `user` desde
  `2026-10-29T15:47:22+01:00`.
- T1–T18 están marcadas `[x]`.
- Existe el informe final del implementer y se inspeccionó el diff tracked y
  untracked.

## Matriz R1–R16

| Requirement | Resultado | Evidencia independiente |
|---|---|---|
| R1 | pass | Adapters consumer-owned, composición PostgreSQL y suites compartidas pasan global normal/race y el gate real. |
| R2 | pass | Migraciones lineales, ledger y artefactos N/N+1 distintos completan las fases PostgreSQL normal/race. |
| R3 | pass | CAS, transacciones causales, rollback/commit incierto y fault injection conservan tests concretos. |
| R4 | pass | `ReserveBound`/`LoadBound` capturan coordenada atómica y la primera mutación stale posterior a cutover queda rechazada. |
| R5 | pass | Ingress verifica firma/scope, persiste inbox/job/authority/outbox antes del ACK y cubre redelivery/commit incierto. |
| R6 | pass | Queue, ordering, lease, owner/fence y heartbeat pasan contratos y PostgreSQL normal/race. |
| R7 | pass | Recovery/retry son acotados; el flujo funcional durable y environment release convergen bajo repetición y race. |
| R8 | pass | La vertical productiva resuelve capability JIT tras las comprobaciones live y no conserva un adapter preautorizado. |
| R9 | pass | El efecto productivo usa outbox claim/heartbeat/reconcile/execute/confirm y cierre causal del job. |
| R10 | pass | Cancelación durable, revision/fence y terminales stale mantienen semántica convergente. |
| R11 | pass | Runtime y scheduler conservan ownership de handlers y `JoinContext` distingue timeout de terminación real. |
| R12 | pass | Freeze global, exporters tipados, gramática cerrada, materialización/verificación y cutover atómico pasan negativos cross-stream/traversal. |
| R13 | pass | Query/retention están autenticados; writers causales mantienen referencias vivas y su cierre habilita purga. |
| R14 | pass | SQLSTATE, retry completo, readiness y observer content-free pasan tests y pérdida de sink no altera verdad durable. |
| R15 | pass | Gate PostgreSQL 16 real, global normal/race, runtime Linux, cobertura ≥85 %, formato y limpieza pasan. |
| R16 | pass | La auditoría aplica el alcance enmendado al diff completo, conserva comentarios sistémicos útiles y rechaza boilerplate/interpolación. |

## Cierre de los hallazgos de review 9

### Environment release bajo coverage

`cleanupDetached` (`internal/environment/service.go:607`) conserva un límite
total ligado al lease, pero crea deadlines propios para reconcile y release.
Así, persistencia y observación posteriores no heredan un contexto externo ya
consumido. El comando exacto ampliado pasó:

```text
go test -p=1 -count=10 -coverpkg=./internal/environment ./internal/environment
ok ... coverage: 88.0% of statements
```

Los casos `observe_and_release_cancellation` y
`release_uncertainty_recovery` también pasaron dentro de todas las rondas de
coverage ejecutadas por el gate.

### Formato y alcance documental

`gofmt -l .` no lista archivos. La enmienda R16 ya no exige comentarios
mecánicos a locales triviales. La auditoría deriva el inventario del diff,
comprueba tipos/entidades, funciones, métodos, helpers y símbolos de paquete, y
el meta-test demuestra que identificadores interpolados no evaden la detección
de familias. No reaparecen las familias denunciadas en reviews 6–8.

## Verificación ejecutada

| Gate | Resultado |
|---|---|
| `go test -p=1 -count=10 -coverpkg=./internal/environment ./internal/environment` | pass, 88.0 % |
| `go test -p=1 -count=1 ./...` | pass |
| `go test -p=1 -race -count=1 ./...` con UCRT64 | pass |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| auditoría AST/meta-test R16 | pass |
| `go mod verify` | pass |
| `gofmt -l .` | pass, salida vacía |
| `git diff --check` | pass; solo avisos de conversión CRLF de Git |
| gate PG16 `-BootstrapWsl -AuthorizeIsolatedTestResources -Repeat 1 -Race -Coverage` | pass |
| cobertura diferencial reproducida | **5420/6370 = 85,08634222919937… %** |
| limpieza | pass: 0 distros WSL, 0 roots temporales, 0 perfiles `block`/`.cover`, 0 `__pycache__` |

No quedan hallazgos bloqueantes. El leader puede marcar la feature 35 como
`done`, registrar el cierre y realizar el commit/push de su rama conforme al
flujo del repositorio.
