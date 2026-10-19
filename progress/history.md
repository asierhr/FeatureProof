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

## 2026-10-11 — assertion-engine

- Estado final: `done`.
- Resultado: motor determinista de assertions con paths acotados, igualdad,
  existencia, ausencia, comparaciones numéricas exactas, grupos `all`/`any`
  anidados y resultados auditables derivados de evidencia terminal.
- Spec aprobada por el usuario el `2026-10-11T18:41:50+02:00`.
- Tasks: T1-T6 completadas.
- Trazabilidad: R1-R9 verificados mediante tests concretos en
  `progress/assertion-engine/implementation.md`.
- Verificación independiente: `gofmt -l .`, `go vet ./...`, compilación,
  `go test ./...`, `go test -race ./...`, 20 repeticiones race y coverage en
  pass.
- Coverage: 90,2 % global; 90,0 % en `internal/assertion` y 90,3 % en
  `internal/planning`.
- Review: aprobada tras corregir números con exponentes extremos, cancelación
  durante compilación y construcción sin evidencia, y recuperar el baseline de
  coverage; evidencia en `progress/assertion-engine/review.md`.

## 2026-10-11 — http-tool

- Estado final: `done`.
- Resultado: adapter `http.request` controlado con target y DNS fijados,
  validación previa a red, redirects revalidados, timeout/cancelación, límites
  de request/response, output estructurado y headers sensibles excluidos.
- Spec aprobada por el usuario el `2026-10-11T19:14:19+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos en
  `progress/http-tool/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race,
  20 repeticiones race y coverage en pass usando servidores locales.
- Coverage: 90,3 % global; 90,4 % en `internal/tools/http` y 90,3 % en
  `internal/planning`.
- Review: aprobada tras limitar cada ejecución a un único dial y medir todos
  los headers realmente serializados antes de DNS; evidencia en
  `progress/http-tool/review.md`.

## 2026-10-11 — evidence-redaction

- Estado final: `done`.
- Resultado: policy inmutable, redacción recursiva y cancelable, truncación
  auditable, evidencia sellada fail-closed y proyección del modelo más
  restrictiva derivada de records confirmados.
- Spec revisada aprobada por el usuario el `2026-10-11T20:43:32+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R10 verificados mediante tests concretos y secretos canario
  en `progress/evidence-redaction/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite, race completa,
  20 repeticiones race y coverage en pass.
- Coverage: 90,5 % global; todos los paquetes modificados superan 85 %.
- Review: aprobada tras fijar `/headers` raíz como ubicación implícita,
  endurecer sellado/copia de evidencias, detectar ciclos nombrados y preservar
  números JSON exactos; evidencia en
  `progress/evidence-redaction/review.md`.

## 2026-10-11 — local-validation-cli

- Estado final: `done`.
- Resultado: comando `featureproof validate` determinista con specification,
  plan manifest JSON v1, target explícito, runtime confiable, progreso seguro,
  códigos 0/1/2/3/130 y bundle terminal publicado atómicamente.
- Spec aprobada por el usuario el `2026-10-11T21:15:19+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos, flujos locales y
  canarios en `progress/local-validation-cli/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race
  fresca, 20 repeticiones race, builds Linux/macOS y coverage en pass.
- Coverage: 91,0 % global; `cmd/featureproof` 100,0 %,
  `internal/localvalidation` 92,3 % e `internal/planmanifest` 92,7 %; todos los
  paquetes modificados superan 85 %.
- Review: aprobada tras endurecer JSON y diagnósticos, introducir filesystem
  inyectable con commit no-replace, cubrir cancelación en todas las fronteras
  y cerrar la convención de comentarios; evidencia en
  `progress/local-validation-cli/review.md`.

## 2026-10-14 — openapi-inspector

- Estado final: `done`.
- Resultado: inspector local de OpenAPI 3.0/3.1 para JSON/YAML, resolución de
  referencias acotada, catálogo determinista con seguridad efectiva,
  validación de responses y contexto reducido con límite y omisiones.
- Spec aprobada por el usuario el `2026-10-14T17:46:38+02:00`.
- Tasks: T1-T7 completadas.
- Trazabilidad: R1-R11 verificados mediante tests concretos y casos
  adversariales en `progress/openapi-inspector/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite completa, race
  fresca, 20 repeticiones race, coverage, `go mod verify`, builds y compilación
  de tests Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Review: aprobada tras cerrar validación estructural OpenAPI, denegación de
  URIs y paths fuera de ámbito, copias profundas, locations opacas, siblings
  de `$ref` 3.1 y canonicalización determinista de headers; evidencia en
  `progress/openapi-inspector/review.md`.

## 2026-10-14 — model-adapter

- Estado final: `done`.
- Resultado: runtime de modelo independiente del proveedor con selección
  explícita de provider, modelo y perfil; presupuestos de llamadas, tokens,
  duración y replanificaciones; adapter OpenAI Responses API y perfiles Sign
  in with ChatGPT OAuth confinados a composición.
- Spec aprobada por el usuario el `2026-10-14T19:28:41+02:00`.
- Tasks: T1-T8 completadas.
- Trazabilidad: R1-R12 verificados mediante tests concretos en
  `progress/model-adapter/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite fresca, race
  completa, 20 repeticiones race, coverage, `go mod verify` y builds
  Windows/Linux/macOS en pass; ningún test ordinario usó red o credenciales.
- Coverage: 91,1 % global; 97,2 % en `internal/model`, 95,0 % en
  `internal/model/openai`, 88,4 % en OAuth y 87,3 % en el smoke.
- Review: aprobada tras preservar categorías de error extremo a extremo,
  cerrar overflow de tokens, independizar refresh compartido del iniciador y
  eliminar locks durante I/O del credential store; evidencia en
  `progress/model-adapter/review.md`.
- Smoke real: no ejecutado; permanece opt-in, separado de `go test` y requiere
  una cuenta open source elegible, perfil y modelo seleccionados.

## 2026-10-15 — agent-plan-generation

- Estado final: `done`.
- Resultado: generación agéntica de planes mediante protocolo
  `PlanningDecision` v1, contexto OpenAPI reducido, catálogo descriptor-only,
  policy de solo lectura y materialización validada sobre
  `planning.ValidationPlan`, sin capacidades ejecutables.
- Spec aprobada por el usuario el `2026-10-15T15:37:20+02:00`.
- Tasks: T1-T8 completadas.
- Trazabilidad: R1-R12 verificados mediante tests concretos en
  `progress/agent-plan-generation/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite fresca, race
  completa, 20 repeticiones race, coverage, `go mod verify` y builds
  Windows/Linux/macOS en pass; tests herméticos sin red ni credenciales.
- Coverage: 91,1 % global; 91,0 % en `internal/agentplanning`, 96,5 % en model,
  93,9 % en OpenAI, 91,8 % en OpenAPI y 89,5 % en planning.
- Review: aprobada tras cerrar filtrado de assertions, trabajo profundo
  acotado, cancelación completa, causas externas opacas, fingerprint canónico
  y ciclo de vida síncrono del observer; evidencia en
  `progress/agent-plan-generation/review.md`.
- Alcance preservado: generar no ejecuta, persiste, aprueba ni replantea; las
  features 11, 12 y 13 mantienen esas responsabilidades.

## 2026-10-15 — agent-evaluation-suite

- Estado final: `done`.
- Resultado: suite versionada para evaluar la generación de planes mediante
  replay hermético o live opt-in, con oráculo estructural anti-invención,
  métricas enteras, criterios explícitos, comparación con baseline y bundles
  autenticados sin secretos ni acciones contra aplicaciones desplegadas.
- Spec aprobada por el usuario el `2026-10-15T16:53:38+02:00`.
- Tasks: T1-T9 completadas.
- Trazabilidad: R1-R12 verificados mediante tests concretos en
  `progress/agent-evaluation-suite/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite fresca, race
  completa, 20 repeticiones race, coverage, `go mod verify` y builds
  Windows/Linux/macOS en pass; evaluaciones live no ejecutadas.
- Coverage: 91,1 % global; 91,3 % en `internal/agenteval` y 89,2 % en
  `cmd/featureproof`.
- Review: aprobada tras cerrar REV-1 a REV-13 sobre confinement y snapshots,
  expectativas estructurales, fail-fast y límites, configuración auditable,
  autenticidad y compatibilidad de baseline, digests lógicos y preflight
  previo a composición live; evidencia en
  `progress/agent-evaluation-suite/review.md`.

## 2026-10-15 — plan-approval

- Estado final: `done`.
- Resultado: puerta humana entre planificación y ejecución con estado
  `awaiting_approval`, vista exhaustiva saneada, fingerprints canónicos,
  decisiones auditables, invalidación por cambios y autorización opaca de un
  solo uso antes de `PlanExecutor`.
- Spec aprobada por el usuario el `2026-10-15T18:30:28+02:00`.
- Tasks: T1-T9 completadas.
- Trazabilidad: R1-R12 verificados mediante tests concretos en
  `progress/plan-approval/implementation.md`.
- Verificación independiente: formato, vet, compilación, suite fresca, race
  completa, 20 repeticiones race, coverage, `go mod verify` y builds
  Windows/Linux/macOS en pass; sin red, credenciales ni targets reales.
- Coverage: 91,1 % global; 91,5 % en `internal/planapproval`, 90,8 % en
  `internal/agentplanning`, 88,3 % en `internal/harness` y 89,3 % en
  `internal/planning`.
- Review: aprobada tras cerrar REV-1 a REV-9 sobre integración obligatoria,
  provenance, recuperación/publicación transaccional, schema de eventos,
  propiedad concurrente del run, locks acotados y carga local aislada por
  run; evidencia en `progress/plan-approval/review.md`.

## 2026-10-16 — bounded-replanning

- Estado final: `done`.
- Resultado: coordinador secuencial de replanificación que entrega al modelo un
  resumen redactado, acepta decisiones estructuradas, admite checks adicionales
  por las fronteras existentes y preserva presupuestos, aprobación, assertions
  deterministas y lineage auditable.
- Spec aprobada por el usuario el `2026-10-16T08:27:07+02:00`.
- Tasks: T1-T9 completadas. Trazabilidad R1-R12 en
  `progress/bounded-replanning/implementation.md`.
- Verificación independiente: suite completa, vet, compilación, race completa y
  repetida, módulos, diff y builds Windows/Linux/macOS en pass. Cobertura global
  89,6 %; `internal/replanning` 85,2 % y todos los paquetes modificados al menos
  85 %.
- Review: aprobada en la quinta revisión tras cerrar REV-1 a REV-9; evidencia
  en `progress/bounded-replanning/review.md`.
- El formato de los archivos Go nuevos y modificados pasa. El gate global
  `gofmt -l .` conserva 146 archivos previos fuera del diff; se mantiene
  documentado como límite del checkout.

## 2026-10-16 — revision-bound-validation

- Estado final: `done`.
- Resultado: cada run admitido queda vinculado a un commit Git completo y limpio,
  una atestación de entorno y hashes públicos seguros de constraints, plan,
  contrato y configuración. Journal, evidencia y reportes comprueban el mismo
  binding; cada replan conserva la revisión y registra su propio digest.
- Spec aprobada por el usuario el `2026-10-16T10:22:13+02:00`.
- Tasks T1–T8 completas; R1–R8 trazados a tests en
  `progress/revision-bound-validation/implementation.md`.
- Verificación: suite, vet, compilación, race completa y race ×20 de
  `localvalidation`, módulos, diff y builds Windows/Linux/macOS en pass.
  Cobertura global 89,2 %; todos los paquetes modificados al menos 85,2 %.
- Review independiente aprobada tras cerrar REV-1 a REV-4; el reviewer aprobó
  expresamente la justificación del descenso global de 0,4 puntos. Evidencia
  en `progress/revision-bound-validation/review.md`.
- Los 48 Go nuevos o modificados pasan formato; `gofmt -l .` aún lista 135
  archivos preexistentes fuera del cambio. La CLI falla cerrada hasta que se
  configure una fuente preview y proyección segura de producción; la spec
  reserva el adapter preview para la feature 19.

## 2026-10-16 — evidence-package

- Estado final: `done`.
- Resultado: paquete local v1 con índice semántico y manifest de tamaño y
  SHA-256 para cada artefacto; lector offline que verifica inventario cerrado,
  binding, evidencias, referencias y coherencia de estados antes de entregar
  una vista confiable. El publicador valida staging antes del commit atómico.
- Spec aprobada por el usuario el `2026-10-16T12:02:02+02:00`. T1–T8
  completas; R1–R10 trazados a tests en
  `progress/evidence-package/implementation.md`.
- Verificación independiente: suite fresca, vet, race completa, builds offline
  Windows/Linux/macOS, formato dirigido y diff en pass. Cobertura global
  88,9 % y `internal/localvalidation` 85,2 %.
- Review aprobada tras corregir procedencia de facts, lote y eventos,
  coherencia de assertions y comprobación de los bytes leídos contra el
  manifest. El reviewer aprobó expresamente la justificación del descenso
  global de 0,3 puntos; evidencia en `progress/evidence-package/review.md`.
- `gofmt -l .` conserva 135 Go anteriores fuera del cambio. El manifest
  detecta alteraciones respecto a los bytes recibidos, pero no firma el
  origen; las instrucciones de reproducción son declarativas y no incluyen
  credenciales ni comandos ejecutables.

## 2026-10-16 — external-agent-interface

- Estado final: `done`.
- Resultado: contrato de aplicación v1 compartido por CLI JSON y MCP stdio
  `2026-07-28`, con identidad y ámbitos confiables, aliases autorizados,
  handles opacos reautorizados, idempotencia durable, supervisor cancelable y
  resultados proyectados exclusivamente desde paquetes verificados.
- Spec aprobada por el usuario el `2026-10-16T15:56:50+02:00`.
- Tasks: T1–T8 completadas; R1–R10 trazados a tests concretos en
  `progress/external-agent-interface/implementation.md`.
- Verificación independiente: suite dirigida, race global comparable, 20
  repeticiones race, coverage, `go vet`, `go mod verify`, diff y builds
  Windows/Linux/macOS en pass; sin red, modelo ni credenciales reales.
- Coverage: 88,9 % en `internal/externalagent` y 88,7 % global comparable.
- Review aprobada tras cerrar REV-1 a REV-5 sobre journal append-only,
  schemas anunciados, join del supervisor, paridad contractual CLI/MCP y
  finalización acotada; evidencia en
  `progress/external-agent-interface/review.md`.
- Limitación del host: el test de symlink de feature 15 falla en Windows por
  privilegios y un `Skipf` heredado ligado al `testing.T` padre; el mismo
  subtest pasó bajo WSL2 sin cambios.

## 2026-10-16 — correction-loop

- Estado final: `done`.
- Resultado: bucle de corrección verificable con cadenas lineales de runs
  padre/hijo, nueva revisión atestada, fase `failed_first`, regresión completa,
  comparación determinista y veredicto derivado únicamente de evidencia del
  hijo.
- Spec aprobada por el usuario el `2026-10-16T17:25:00+02:00`.
- Tasks: T1–T8 completadas; R1–R12 trazados a tests concretos en
  `progress/correction-loop/implementation.md`.
- Persistencia: journal estricto append-only, CAS y locks/leases por cadena,
  idempotencia multiproceso, presupuestos acumulados y recovery sin robar
  owners vivos ni repetir trabajo confirmado.
- Publicación: package y comparación usan preparación/intención durable,
  commit no-replace y reconciliación de respuestas inciertas; conflictos o
  corrupción terminan una vez como fallo operativo de integridad.
- Superficies: application contract v2 compartido por CLI y MCP, con scopes,
  handles opacos, límites allowlisted y compatibilidad de las operaciones v1.
- Verificación independiente: siete reviews cerraron REV-1–REV-16; suite
  dirigida, regresiones repetidas, race de paquetes afectados y race global
  comparable, vet, compile-only, módulos, diff y builds Windows/Linux/macOS
  pasan. Coverage `internal/correction` 85,4 %.
- Limitación del host: el gate literal conserva únicamente el subtest de
  symlink preexistente de feature 15 que Windows no puede crear sin privilegio;
  la suite y race comparables excluyen solo ese caso y pasan.
## 2026-10-17 — github-pr-integration

- Estado final: `done`.
- Resultado: integración GitHub App para iniciar o asociar validaciones a una
  pull request autorizada y a su head commit exacto, con webhook HMAC,
  idempotencia durable, checks y comentario resumen propios, política segura
  para forks y permisos mínimos.
- Spec aprobada por el usuario el `2026-10-16T21:51:08+02:00`.
- Tasks T1–T11 completas; R1–R12 trazados a pruebas concretas en
  `progress/github-pr-integration/implementation.md`.
- Persistencia y recuperación: journal append-only con CAS, locks y leases
  multiproceso; receipts, asociaciones e intents se recuperan por lotes
  reproducibles menores de 1 MiB, con payloads exactos y ownership verificado.
- Composición: `featureproof github serve` conecta webhook, adapter REST,
  retries, publisher y coordinator; las credenciales se resuelven solo en la
  composición y la selección de preview permanece fail-closed hasta la
  feature 19.
- Verificación independiente aprobada en la novena revisión tras cerrar
  REV-1–REV-23. Pasan paquete ×20, escenario de concurrencia ×500, race del
  paquete ×3, race global comparable, vet, compile-only, módulos, diff y
  builds Windows/Linux/macOS. Coverage `internal/githubpr`: 85,6 %.
- Limitación del host: `go test ./...` conserva únicamente el subtest
  preexistente de symlink de feature 15, que Windows no puede crear sin el
  privilegio correspondiente; la suite comparable excluye solo ese caso y
  pasa.

## 2026-10-17 — preview-environment-adapter

- Estado final: `done`.
- Resultado: lifecycle provider-neutral para seleccionar y reservar previews
  existentes, atestar repository/commit, autorizar el target, esperar
  readiness, ejecutar la validación compartida y liberar la reserva sin crear,
  modificar ni destruir infraestructura.
- Spec aprobada por el usuario el `2026-10-17T18:03:28+02:00`.
- Tasks T1–T12 completas; R1–R11 trazados a pruebas concretas en
  `progress/preview-environment-adapter/implementation.md`.
- Integración: CLI, MCP y GitHub componen el mismo coordinator cuando existe
  configuración explícita y fallan cerrados cuando falta. El handoff queda
  durable antes del ACK y los terminales operacionales conservan su categoría.
- Seguridad: target y policy forman parte del binding; los secretos se
  resuelven únicamente para la selección autorizada y no entran en core,
  journal, harness, evidencias ni telemetría.
- Durabilidad: intención, handoff, owner, lease, CAS, reconcile, release pending
  y recovery multiproceso convergen con claves estables y sin repetir runs
  funcionales confirmados.
- Review independiente aprobada en la sexta revisión tras cerrar REV-1–REV-21.
  Pasan regresiones repetidas hasta ×1000, paquetes afectados ×20, race,
  suite global comparable, vet, compile-only, módulos, diff y builds
  Windows/Linux/macOS.
- Coverage: `internal/previewenv` 85,4 %, `internal/githubpr` 85,1 % e
  `internal/externalagent` 87,4 %.
- Limitación del host: `go test ./...` conserva únicamente el subtest
  preexistente de symlink de feature 15 que Windows no puede crear sin el
  privilegio correspondiente; suite y race comparables excluyen solo ese
  paquete y pasan.

## 2026-10-19 — validation-workflow-orchestration

- Estado final: `done`.
- Resultado: flujo completo desde specification hasta planificación por agente,
  aprobación exacta, ejecución con el runner compartido y publicación del
  package verificable, sin exigir un `ValidationPlan` escrito manualmente.
- Spec aprobada por el usuario el `2026-10-19T11:12:00+02:00`.
- Tasks T1–T12 completas; R1–R13 trazados a pruebas concretas en
  `progress/validation-workflow-orchestration/implementation.md`.
- Integración: API Go, CLI `workflow`, MCP y GitHub comparten el mismo service,
  store y modelo de estados. El comando histórico `validate --plan` conserva
  su comportamiento y usa el mismo runner de validación.
- Seguridad e integridad: `planapproval` autoriza el artefacto, contexto,
  revisión y target exactos mediante un grant de un solo uso; el plan público
  usa compromisos HMAC privados por workflow y el material ejecutable se sella
  y se revalida antes de aprobación y tools.
- Durabilidad: checkpoints atómicos, CAS, leases renovables, deadlines de
  aprobación, intents de package/cleanup y recovery multiproceso evitan repetir
  inferencia o efectos inciertos. Preview se restaura y libera mediante el
  lifecycle de `previewenv`.
- Review independiente aprobada en la quinta revisión tras cerrar REV-1–REV-14.
  Pasan focales ×20, paquetes afectados ×10, race focal ×3, vet, compile-only,
  módulos, diff y builds Windows/Linux/macOS.
- Coverage: `internal/validationworkflow` 85,8 % e `internal/previewenv` 85,0 %.
- Limitación del host: la suite global conserva únicamente el subtest
  preexistente de symlink de feature 15 que Windows no puede crear sin el
  privilegio correspondiente; suite y race comparables excluyen solo ese caso
  y pasan.
