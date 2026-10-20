# Cuarta revisión independiente final — Feature 23

Fecha: 2026-10-20
Feature: `spring-boot-backend-inspector`
Veredicto: **approved**

## Precondiciones

- `spring-boot-backend-inspector` es la única feature `in_progress`.
- La spec tiene aprobación humana explícita.
- T1–T13 están marcadas `[x]` y existe informe actualizado del implementer.
- Se revisaron nuevamente requirements, design, tasks, código productivo, tests y
  diff. No se usó el informe como sustituto de la verificación independiente.

## Hallazgos REV-1..REV-13

No quedan hallazgos abiertos.

- REV-1..REV-11 permanecen cerrados: autoridad pre-I/O, identidad FQCN,
  mappings/aliases/constants, DTOs, responses, límites, diagnósticos,
  revalidación, composición multi-source y provenance no muestran regresiones.
- REV-13 permanece cerrado: nested Lombok/Jackson/custom serialization se
  degrada incluso con nesting profundo; annotations homónimas locales no
  adquieren semántica externa.
- REV-12 queda cerrado en producción. La referencia GitHub es durable y cada
  reconstrucción de admission refresca autoridad antes de aprobación y otra vez
  antes de consumir el grant/tools.

## Verificación productiva de REV-12

`RESTAdapter.PullRequest` forma una observación coherente en este orden:

1. Obtiene metadata con installation token para installation/repository exactos.
2. Valida `base.repo.id`, número y `full_name`, y deriva solo owner/repository
   seguros.
3. Lee todas las páginas de `/repos/{owner}/{repo}/pulls/{number}/files` sin
   seguir URLs entregadas por el proveedor.
4. Aplica `MaxResponse`, `MaxPages`, `MaxChangedPaths`, máximo de 100 elementos
   por página, duplicados, paths relativos canónicos y vocabulario estricto de
   `Link`.
5. Vuelve a obtener metadata y exige el mismo fingerprint, owner y repository.
6. Ordena los paths y sella un nuevo `PullRequestSnapshot`; los paths forman
   parte del fingerprint que se persiste en `GitHubAuthorityReference`.

Errores HTTP, token inválido, respuesta malformada, límite, paginación
inconclusa, TOCTOU y cancelación fallan con categorías cerradas. La composición
real de `main.go` inyecta este mismo adapter autenticado tanto al runtime GitHub
como a `GitHubRefreshingAdmission`.

`TestProductionRESTChangedPathDriftStopsRestartedWorkflow` usa el
`RESTAdapter` real sobre TLS, realiza `Submit`, cambia solo los paths manteniendo
el head, reabre stores con otro owner y llama `Decide`. El refresh detecta el
fingerprint distinto antes de `Approval.Decide`; los contadores prueban cero
`Decide`, cero `Consume` y cero ejecución. Los tests durables complementarios
cubren head drift, refresh pre-approval, un segundo refresh pre-tools y el caso
estable con una única ejecución.

## Trazabilidad R1–R13

| Requirement | Evidencia revisada | Resultado |
|---|---|---|
| R1 | input/root/framework/source sets sellados; scope del snapshot validado antes de parse | cumple |
| R2 | parser pasivo, snapshot-only, sin process/build/JDK/classpath/network/env/cache | cumple |
| R3 | controllers/mappings cerrados, aliases/arrays/constants/cartesiano y lookalikes | cumple |
| R4 | parámetros/body por annotation exacta, aliases, requiredness, schema y provenance | cumple |
| R5 | records/enums/generics/cycles/nested DTOs; Lombok/Jackson/custom/external conservadores | cumple |
| R6 | consumes/produces, ResponseStatus y ResponseEntity exactos sin cruzar branches | cumple |
| R7 | PreAuthorize/Secured/RolesAllowed permanecen datos inertes y nunca capabilities | cumple |
| R8 | provenance file/line/class/method/annotation/type/field/repository/revision, copy y digest | cumple |
| R9 | errores por unidad, diagnostics acotados y canarios sin filtración | cumple |
| R10 | changed closure transitivo, unchanged DTOs y aislamiento por source set | cumple |
| R11 | límites/deadlines/cancelación/all-or-nothing/determinismo/concurrencia | cumple |
| R12 | registry/policy/conflicts/multi-source y revalidación durable GitHub real | cumple |
| R13 | comentarios sistémicos, trazabilidad, matrices adversarias, race y cobertura | cumple |

## Trazabilidad T1–T13

T1–T13 tienen implementación y pruebas concordantes. En particular, T3/T6
quedan respaldadas por la matriz nested; T12 por el adapter REST autenticado y
los checkpoints cross-owner; T13 por los tests focales, canarios, comentarios,
repetición, race y cobertura. No hay excepción de task pendiente.

## Comentarios y fronteras

Las entidades, interfaces, constantes, funciones, métodos y helpers añadidos o
modificados para la corrección explican su responsabilidad sistémica o la
frontera que protegen. Los fixtures y spies nuevos también están documentados.
No se introducen secretos en snapshots/journal, ni capacidades de GitHub en el
harness o el inspector.

## Gates reproducidos

- `go test ./internal/githubpr -run 'TestRESTPullRequest' -count=20`: PASS.
- `go test ./internal/validationworkflow -run
  'TestProductionRESTChangedPathDriftStopsRestartedWorkflow|TestGitHubAuthority'
  -count=20`: PASS.
- `go test ./internal/springinspector -run 'TestNested|TestReview' -count=20`:
  PASS.
- Suites completas afectadas ×5 y Spring/catálogo ×20: PASS.
- UCRT64 `go test -race -count=2` sobre GitHub, workflow, CLI, Spring y catálogo:
  PASS.
- `go vet` afectado: PASS.
- `go test -run '^$' ./...`: PASS.
- `gofmt -l` sobre Go modificados y `git diff --check`: PASS; solo se imprimen
  avisos LF/CRLF del checkout.
- Cobertura: GitHub 85.8 %, validationworkflow 86.5 %, Spring 88.7 % y catálogo
  85.0 %. El paquete CLI completo queda en 74.5 %, pero las funciones de
  composición nuevas/modificadas tienen cobertura focal superior al 85 % según
  perfiles del informe.
- `go test ./...`: único fallo en
  `internal/localvalidation/TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors/evidence_symlink`
  porque Windows no concede privilegio para crear el symlink. Es la excepción
  histórica documentada; todos los demás paquetes pasan.

## Veredicto

**approved**. R1–R13 y T1–T13 quedan satisfechos, REV-1..REV-13 están cerrados y
no se encontraron defects nuevos bloqueantes. El leader puede marcar la feature
como `done` y registrar el cierre en history.

approved -> progress/spring-boot-backend-inspector/review.md
