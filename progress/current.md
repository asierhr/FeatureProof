# Trabajo actual

## 2026-10-16 — Cierre de external-agent-interface

- Feature 16 `done` tras tercera review independiente aprobada; T1–T8 y
  R1–R10 verificados.
- REV-1 a REV-5 cerradas: journal append-only, schemas satisfacibles, join
  real, contrato común CLI/MCP y finalización acotada ante fallos de
  publicación.
- Suite dirigida, race global comparable, race ×20, vet, módulos, diff y
  builds Windows/Linux/macOS pasan. Coverage: 88,9 % `internal/externalagent`
  y 88,7 % global comparable.
- El único fallo literal global es el test de symlink preexistente de feature
  15 en Windows; fue reproducido como limitación de privilegios y el mismo
  subtest pasó bajo WSL2 sin modificarlo.
- Informes finales en `progress/external-agent-interface/`; resumen añadido a
  `progress/history.md`.

## 2026-10-16 — Segunda review de external-agent-interface rechazada

- REV-1 a REV-3 cerradas; REV-4 sigue parcial y REV-5 fue devuelta al
  implementer.
- Pendiente: añadir a la tabla contractual común `invalid_request`,
  `unauthorized` y replay/conflict de cancelación; acotar con deadline y tratar
  errores al persistir `publication_failed` antes de la transición terminal.
- Estado: `in_progress`; requiere corrección, reverificación y tercera review.

## 2026-10-16 — Primera review de external-agent-interface rechazada

- REV-1 a REV-4 devueltas al implementer: journal append-only del registro,
  schemas de salida completos y satisfacibles, join real del supervisor tras
  cleanup y suite contractual común CLI/MCP para todos los escenarios R10.
- La review confirmó que el fallo de symlink pertenece a feature 15 y es
  ambiental en Windows; el mismo subtest pasó bajo WSL2. No se considera un
  defecto de feature 16.
- Estado: `in_progress`; requiere correcciones, verificación completa y nueva
  review independiente.

## 2026-10-16 — Inicio de implementación de external-agent-interface

- El usuario aprobó la spec de feature 16 con la instrucción explícita de
  continuar la implementación, registrada el
  `2026-10-16T15:56:50+02:00`.
- El leader cambió `external-agent-interface` de `spec_ready` a
  `in_progress`.
- Alcance: T1–T8 y R1–R10, con caso de uso compartido, adaptadores CLI/MCP,
  identidad confiable, registro durable, idempotencia, supervisión cancelable
  y proyección segura de paquetes; features 17–19 quedan fuera.
- Siguiente fase: implementer, verificación canónica y review independiente.

## 2026-10-16 — Spec de external-agent-interface completada

- Feature 16 pasó de `pending` a `spec_ready` tras verificar los tres
  artefactos SDD en `specs/external-agent-interface/`.
- R1–R10 cubren ciclo durable de runs, paridad CLI/MCP, resultado seguro,
  identidad y ámbitos, aislamiento de handles, errores, idempotencia,
  cancelación, descubrimiento y tests contractuales. T1–T8 enlazan todos los
  requirements con pruebas previstas.
- Se fijó MCP `2026-07-28` por stdio y un caso de uso compartido; la identidad
  no procede de `clientInfo` y la admisión productiva mantiene fallo cerrado
  hasta disponer de proveedor preview confiable.
- Aprobación humana pendiente en `requirements.md`. No se modificó código ni
  tests de feature 16; la implementación espera esa puerta.

## 2026-10-16 — Preflight de external-agent-interface

- Feature 16 seleccionada: `pending`, `sdd: true`, con criterios de aceptación
  en `feature_list.json`. Features 1–15 `done`; ninguna `in_progress`.
- El working tree conserva cambios locales de feature 15 ya revisada; se
  preservan. Alcance de esta sesión: solo definir la spec de feature 16,
  sin código ni tests antes de la puerta de aprobación humana.
- Contexto externo consultado: especificación MCP oficial 2026-07-28 para
  capacidades, tools y contrato de transporte. Siguiente fase: spec author.

## 2026-10-16 — Cierre de evidence-package

- Feature 15 `done` tras review independiente aprobada; T1–T8 y R1–R10
  verificados. Informes en `progress/evidence-package/` y resumen en
  `progress/history.md`.
- Suite, vet, race completa y builds offline Windows/Linux/macOS pasan.
  Cobertura global 88,9 %; `internal/localvalidation` 85,2 % y justificación
  del descenso global aprobada por el reviewer.
- Los Go tocados están formateados; `gofmt -l .` conserva 135 archivos
  preexistentes fuera del cambio. No hay feature `in_progress`; la siguiente
  pendiente es `external-agent-interface` (feature 16).

## 2026-10-16 — Inicio de implementación de evidence-package

- El usuario aprobó la spec de feature 15 con la instrucción explícita de
  continuar la implementación, registrada el 2026-10-16T12:02:02+02:00.
- El leader cambió `evidence-package` de `spec_ready` a `in_progress`.
- Alcance: T1–T8 y R1–R10, con paquete v1 verificable offline y publicación
  atómica; las features 16–19 quedan fuera. Siguiente fase: implementer,
  verificación y review independiente.

## 2026-10-16 — Spec de evidence-package completada

- Feature 15 pasó de `pending` a `spec_ready` tras verificar los tres
  artefactos SDD en `specs/evidence-package/`.
- R1–R10 cubren formato e identidad del paquete, separación de hechos,
  fallos trazables, inventario íntegro, publicación atómica, redacción,
  lector offline, reproducción segura y cancelación. T1–T8 referencian
  los requirements y prevén pruebas concretas.
- La aprobación humana de `requirements.md` está pendiente. No se ha
  modificado código ni tests de la feature; siguiente fase: revisión de la
  spec antes de `in_progress`.

## 2026-10-16 — Preflight de evidence-package

- Feature 15 seleccionada: `pending`, `sdd: true`, con criterios de aceptación
  en `feature_list.json`. Features 1–14 `done`; ninguna `in_progress`.
- Working tree limpio al iniciar. Alcance actual: redactar la spec de
  `evidence-package`; el código y los tests esperan aprobación humana.
- Siguiente fase: spec author y comprobación de `requirements.md`, `design.md`
  y `tasks.md` antes de pasar a `spec_ready`.

## 2026-10-16 — Cierre de revision-bound-validation

- Feature 14 `done` tras review independiente aprobada; T1–T8 y R1–R8
  verificados. Informes en `progress/revision-bound-validation/` y resumen
  añadido a `progress/history.md`.
- Suite, vet, race completa, race ×20 de `localvalidation` y builds
  Windows/Linux/macOS en pass. Cobertura global 89,2 %; el reviewer aprobó
  expresamente la justificación de la caída de 0,4 puntos.
- El gate global `gofmt -l .` conserva 135 archivos anteriores fuera del cambio;
  los 48 Go nuevos o modificados por esta feature están formateados.
- No queda ninguna feature `in_progress`; la siguiente pendiente es
  `evidence-package` (feature 15).

## 2026-10-16 — Inicio de implementación de revision-bound-validation

- El usuario aprobó la spec de feature 14 con la instrucción explícita de continuar la implementación, registrada el 2026-10-16T10:22:13+02:00.
- El leader cambió `revision-bound-validation` de `spec_ready` a `in_progress`.
- Alcance: implementar T1-T8 y verificar R1-R8 de feature 14; la feature 13 permanece `done` y sus cambios locales se preservan.
- Siguiente fase: implementer, verificación y review independiente.

## 2026-10-16 — Spec de revision-bound-validation completada

- Feature 14 pasó de `pending` a `spec_ready` tras comprobar los tres artefactos SDD.
- `specs/revision-bound-validation/{requirements,design,tasks}.md` contiene R1-R8, mapa de trazabilidad y T1-T8 sin marcar.
- La aprobación humana en `requirements.md` permanece vacía. No se ha implementado código de feature 14.
- Alcance decidido en la spec: working tree sucio se rechaza; la revisión desplegada debe acreditarse por una fuente confiable. El adapter preview concreto pertenece a feature 19.
- Siguiente puerta: lectura y aprobación explícita del usuario antes de `in_progress`.

## 2026-10-16 — Preflight de revision-bound-validation

- Feature 14 seleccionada: `pending`, `sdd: true`; ninguna feature está `in_progress`.
- Acceptance criteria disponibles en `feature_list.json`; features 1-13 cerradas, con cambios locales no comprometidos de feature 13 que se preservan.
- Alcance de esta sesión: redactar exclusivamente `requirements.md`, `design.md` y `tasks.md` de feature 14. No se implementa código ni se presume aprobación humana.
- Siguiente fase: spec author, verificación de artefactos y transición del leader a `spec_ready`.

## 2026-10-16 — Cierre de bounded-replanning

- Estado final: `done`; quinta revisión independiente aprobada.
- T1-T9 completas; R1-R12 trazados a tests y REV-1 a REV-9 cerradas.
- Pasan suite, vet, compilación, race completa y repetida, módulos, diff y builds Windows/Linux/macOS.
- Cobertura: 89,6 % global; `internal/replanning` 85,2 % y demás paquetes modificados al menos 85 %.
- `gofmt -l` de los Go modificados o nuevos no emite rutas. El comando global `gofmt -l .` sigue enumerando 146 archivos anteriores fuera del diff.
- Evidencia: `progress/bounded-replanning/{implementation,review}.md` y `progress/history.md`.

## 2026-10-16 — Inicio de bounded-replanning

- El usuario aprobó explícitamente `specs/bounded-replanning/` el 2026-10-16T08:27:07+02:00.
- El leader registró la aprobación y pasó la feature 13 a `in_progress`.
- Alcance: solo implementación y verificación de `bounded-replanning`; features 1-12 cerradas.

## Estado

La feature `evidence-package` está en estado `done`, con spec y review
aprobadas. No hay feature `in_progress`.

## Última actualización

- Fecha: 2026-10-15
- Trabajo: especificación de `bounded-replanning` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/bounded-replanning/`.
- Verificación: R1-R12 cubren resumen saneado, protocolo cerrado, pipeline
  completo, presupuestos, agotamiento, ledger monotónico, anti-loop, lineage,
  aprobación, concurrencia y verdict; T1-T9 referencian todos los requirements
  y la aprobación humana permanece vacía.
- Frontera: no se introducen costes monetarios; se reutilizan llamadas,
  tokens, duración y replans de feature 9 junto con steps, tools y trabajo
  determinista.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: preflight de `bounded-replanning` completado e inicio de la
  redacción de `specs/bounded-replanning/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, features 1-12
  `done` y ninguna feature `in_progress`.
- Contexto clave: la replanificación parte únicamente de resultados y
  evidencias redactadas; cualquier comprobación adicional debe volver a pasar
  por validación, registry, policy, budgets y aprobación cuando corresponda.
- Invariante: una replanificación nunca puede borrar o mejorar un fallo
  determinista ya observado, ni iniciar trabajo al agotarse un límite.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: quinta revisión independiente aprobada y transición de
  `in_progress` a `done` realizada por el leader.
- Evidencia: R1-R12 y T1-T9 verificados; REV-1 a REV-9 cerradas; formato, vet,
  compilación, suite fresca, race, race x20, coverage, módulos y builds
  Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 91,5 % `internal/planapproval`, 90,8 %
  `internal/agentplanning`, 88,3 % `internal/harness` y 89,3 %
  `internal/planning`.
- Invariantes finales: cero ejecución sin grant vigente, provenance ligada al
  contexto, consumo único durable, journal estricto y carga local aislada por
  run sin I/O bajo locks globales.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-15
- Trabajo: cuarta revisión independiente rechazada; REV-1 a REV-8 cerradas y
  REV-9 devuelta al implementer.
- Hallazgo pendiente: `LocalStore.ensureLoaded` conserva un mutex global por
  root durante recovery y lectura de disco, bloqueando runs independientes.
- Estado: `in_progress`; requiere carga coordinada por run, pruebas de
  bloqueo/cancelación/retry y quinta review.

- Fecha: 2026-10-15
- Trabajo: tercera revisión independiente rechazada; REV-1 a REV-7 cerradas
  y REV-8 devuelta al implementer.
- Hallazgo pendiente: el lifecycle mantiene su mutex durante store,
  sanitizer, hashing y la ejecución externa de `PlanExecutor`, creando una
  ruta de bloqueo y reentrada contraria a las convenciones.
- Estado: `in_progress`; requiere reducir la sección crítica, reverificar y
  realizar una cuarta review independiente.

- Fecha: 2026-10-15
- Trabajo: segunda revisión independiente rechazada; REV-2 a REV-4 cerradas,
  REV-1/REV-5 parciales y REV-6/REV-7 devueltas al implementer.
- Hallazgos pendientes: propiedad única y race-free del aggregate `Run` unida
  al CAS autoritativo, y validación estricta de SHA-256/UTC en eventos
  reconstruidos.
- Estado: `in_progress`; requiere tercera review tras correcciones y
  reverificación completa.

- Fecha: 2026-10-15
- Trabajo: primera revisión independiente de `plan-approval` rechazada;
  correcciones devueltas al implementer.
- Hallazgos: REV-1 a REV-5 cubren integración obligatoria en la ruta real de
  `PlanExecutor`, provenance entre plan y restricciones, recuperación local
  transaccional, append resistente a escrituras parciales y validación cerrada
  de eventos auditables.
- Estado: `in_progress`; requiere corrección, reverificación completa y nueva
  review independiente.

- Fecha: 2026-10-15
- Trabajo: T1-T9 de `plan-approval` completadas; implementación preparada para
  revisión independiente en `progress/plan-approval/implementation.md`.
- Verificación: formato, vet, compilación, suite completa, race completa, race
  x20 de paquetes afectados, coverage, módulos, `git diff --check` y builds
  Linux/macOS en pass.
- Coverage: 91,4 % global; 94,3 % `internal/planapproval`, 91,0 %
  `internal/evidence` y 88,0 % `internal/harness`; baseline global preservado.
- Seguridad: cero red, credenciales, targets reales o ejecución previa a una
  autorización confirmada y consumida una sola vez.
- Estado: la feature continúa `in_progress`; se solicita review independiente.

- Fecha: 2026-10-15
- Trabajo: T1-T7 de `plan-approval` implementadas con workflow, proyección
  saneada, hashes canónicos, journal/CAS en memoria y local, decisiones e
  invalidaciones cerradas, grant opaco consumible una vez e integración con
  la frontera de `planning`.
- Estado: tests dirigidos en pass y coverage del paquete nuevo en 85,1 %; T8
  y T9 continúan con race, verificación canónica y trazabilidad final.
- Seguridad: no se han usado red, credenciales, targets reales ni una CLI
  interactiva; el executor solo recibe planes después de consumo persistido.

- Fecha: 2026-10-15
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`,
  `approved_at: 2026-10-15T18:30:28+02:00`.
- Siguiente fase: implementación ordenada de T1-T9, verificación canónica y
  revisión independiente.

- Fecha: 2026-10-15
- Trabajo: especificación de `plan-approval` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/plan-approval/`.
- Verificación: R1-R12 cubren staging sin efectos, vista saneada, decisiones,
  hashes, invalidación, grant opaco, consumo único, CAS, journal y
  cancelación; T1-T9 referencian todos los requirements y la aprobación
  humana permanece vacía.
- Frontera: la feature no inventa una CLI interactiva; ofrece un workflow de
  aplicación reutilizable y ninguna herramienta cruza la puerta sin una
  autorización vigente y consumible una sola vez.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: preflight de `plan-approval` completado e inicio de la redacción de
  `specs/plan-approval/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, features 1-11
  `done` y ninguna feature `in_progress`.
- Contexto clave: la feature 10 entrega un plan preparado sin ejecutarlo; esta
  feature debe introducir una decisión humana auditable, vinculada por hashes
  al plan, target, restricciones y contexto contractual.
- Invariante: ninguna herramienta puede ejecutarse mientras el run permanezca
  en `awaiting_approval` o cuando la aprobación sea inexistente, rechazada,
  duplicada u obsoleta.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: tercera revisión independiente aprobada y transición de
  `in_progress` a `done` realizada por el leader.
- Evidencia: R1-R12 y T1-T9 verificados; REV-1 a REV-13 cerradas; formato,
  vet, compilación, suite fresca, race, race x20, coverage, módulos y builds
  Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 91,3 % `internal/agenteval` y 89,2 %
  `cmd/featureproof`.
- Invariantes finales: replay hermético, live opt-in con preflight previo a
  OAuth, cero ejecución de herramientas/targets, datasets y baselines
  autenticados, métricas estructurales y bundles sin secretos.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-15
- Trabajo: segunda revisión independiente rechazada; REV-1 a REV-9 cerradas
  y REV-10 a REV-13 devueltas al implementer.
- Hallazgos pendientes: normalización/unicidad de sets y digest lógico,
  conversión checked de milisegundos, publicación del timeout por llamada en
  la configuración segura y recuperación verificable del baseline global de
  coverage.
- Estado: `in_progress`; requiere tercera review tras correcciones y
  reverificación canónica.

- Fecha: 2026-10-15
- Trabajo: primera revisión independiente de `agent-evaluation-suite`
  rechazada; correcciones devueltas al implementer.
- Hallazgos: REV-1 a REV-9 cubren confinamiento/snapshot OpenAPI,
  expectativas de contexto insuficiente, matching de rechazo, fail-fast,
  límites, configuración del manifest, autenticidad de baseline, casos por
  categoría y preflight de baseline antes de live.
- Estado: `in_progress`; requiere corrección, reverificación completa y nueva
  review independiente.

- Fecha: 2026-10-15
- Trabajo: implementación T1-T9 de `agent-evaluation-suite` cerrada y lista
  para review independiente.
- Verificación final: formato, vet, compilación, suite fresca, race completa,
  race ×20, coverage, módulos y builds Windows/Linux/macOS en pass.
- Coverage: 91,1 % global preservado; 91,2 % `internal/agenteval`.
- Evidencia: mapa R1-R12, tareas, comandos y riesgos registrado en
  `progress/agent-evaluation-suite/implementation.md`.
- Estado: `in_progress`; el implementer solicita review y no cambia el estado
  de la feature.

- Fecha: 2026-10-15
- Trabajo: implementación de T1-T8 de `agent-evaluation-suite` completada;
  cierre de T9 y verificación canónica en curso.
- Resultado: nuevo paquete `internal/agenteval` con loaders JSON locales
  estrictos, replay/live sobre el runtime de feature 9, runner secuencial,
  captura de frontera, oráculo anti-invención, métricas ppm, criterios,
  bundles con manifest final, baseline y comando `featureproof eval agent`.
- Dataset: `evals/agentplanning/v1/` cubre plan válido, prompt injection como
  datos, contexto insuficiente, endpoint inventado y límites; el replay
  hermético completo produce veredicto `passed`.
- Seguridad: cero executor/tools/target/evidence store; el modo live exige
  opt-in, selección y todos los budgets antes de resolver OAuth.
- Verificación parcial: tests nuevos de `internal/agenteval` y
  `cmd/featureproof` pasan sin red ni credenciales.

- Fecha: 2026-10-15
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`,
  `approved_at: 2026-10-15T16:53:38+02:00`.
- Siguiente fase: implementación ordenada de T1-T9, verificación canónica y
  revisión independiente.

- Fecha: 2026-10-15
- Trabajo: especificación de `agent-evaluation-suite` completada y transición
  de `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/agent-evaluation-suite/`.
- Verificación: R1-R12 definen dataset y criterios versionados, oráculo y
  métricas deterministas, bundles seguros, comparación con baseline y comando
  separado; T1-T9 referencian todos los requirements y la aprobación humana
  permanece vacía.
- Frontera: las evaluaciones no ejecutan herramientas ni acciones contra un
  target; replay es local y el modo live exige opt-in y selección explícita.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: preflight de `agent-evaluation-suite` completado e inicio de la
  redacción de `specs/agent-evaluation-suite/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, features 1-10 `done`
  y ninguna feature `in_progress`.
- Contexto clave: la feature 10 produce PreparedPlan o insufficient_context
  sin ejecutar; la suite debe medir propiedades deterministas, versionar
  dataset/umbrales y comparar resultados candidatos con una baseline.
- Invariante: las evals no ejecutarán herramientas ni acciones contra una
  aplicación desplegada y vivirán en un comando separado de `go test`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-15
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R12 y T1-T8 verificados; REV-1 a REV-6 corregidas; formato,
  vet, compilación, suite fresca, race, race x20, coverage, módulos y builds
  Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 91,0 % `internal/agentplanning`, 96,5 % model,
  93,9 % OpenAI, 91,8 % OpenAPI y 89,5 % planning.
- Invariantes finales: una inferencia, cero ejecución/retry/replan, procedencia
  cerrada, validación all-or-nothing y observer unido al ciclo de vida.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-15
- Trabajo: tercera review independiente de `agent-plan-generation` aprobada.
- Resultado: REV-1 a REV-6 corregidas; R1-R12 y T1-T8 verificados sin hallazgos
  pendientes. El observer es síncrono, cooperativo, acotado y unido a
  `Generate`, sin goroutines ni terminales tardíos.
- Verificación fresca: formato, vet, compilación, suite sin caché, race
  completa, race ×20, módulos y builds Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 91,0 % `internal/agentplanning`, 96,5 % model,
  93,9 % OpenAI, 91,8 % OpenAPI y 89,5 % planning.
- Estado: `in_progress`; reviewer solicita al leader transición a `done` y
  registro en `progress/history.md`.

- Fecha: 2026-10-15
- Trabajo: REV-6 corregida y preparada para tercera review.
- Resultado: `observeTerminal` ejecuta `Observer.Observe` síncronamente con un
  deadline finito; el contrato exige detener todo trabajo propio antes de
  retornar al vencer `ctx`. No existe goroutine fire-and-forget.
- Regresión: el test coordinado observa deadline/cancelación, demuestra que
  `Generate` espera la limpieza del observer y verifica ausencia de trabajo y
  escrituras terminales posteriores al retorno; observer error sigue
  fail-closed.
- Verificación: formato, vet, compilación, suite fresca, race completa, race
  ×20, módulos y builds Windows/Linux/macOS en pass; coverage global 91,1 %
  y `internal/agentplanning` 91,0 %.
- Estado: `in_progress`; `ready_for_review` en
  `progress/agent-plan-generation/implementation.md`.

- Fecha: 2026-10-15
- Trabajo: segunda review rechazada por REV-6; corrección del ciclo de vida
  del observer iniciada.
- Hallazgo: el timeout asíncrono podía abandonar una goroutine y permitir una
  confirmación terminal posterior al retorno de `Generate`.
- Corrección en curso: observer síncrono con contexto de finalización acotado,
  contrato cooperativo y regresiones de join/orden sin trabajo huérfano.
- Estado: `in_progress`; pendiente verificación canónica y nueva review.

- Fecha: 2026-10-15
- Trabajo: segunda review independiente de `agent-plan-generation` rechazada.
- Resultado: REV-1, REV-2, REV-4 y REV-5 corregidas; REV-3 corregida salvo la
  estrategia de finalización del observer.
- Nuevo hallazgo: REV-6 blocker. `observeTerminal` abandona una goroutine cuando
  el observer ignora contexto; viola el diseño sin goroutines, no garantiza
  terminación y permite una confirmación terminal posterior al retorno.
- Verificación fresca: formato, vet, suite sin caché, race completa, race ×20,
  módulos y builds Windows/Linux/macOS en pass; coverage global 91,1 %.
- Estado: `in_progress`; requiere corregir REV-6 y repetir review.

- Fecha: 2026-10-15
- Trabajo: REV-1 a REV-5 corregidas y preparadas para segunda review.
- Resultado: traversal completo y acotado de datos model-authored; presupuesto
  overflow-safe; cancelación post-colaborador; observer con deadline; causas
  externas opacas; fingerprint canónico de grafo y evidence.
- Regresiones: canarios en assertions, URL con whitespace/userinfo, comandos,
  credenciales/capabilities, deep/wide, límite exacto, `math.MaxInt`, overflow,
  cancelación coordinada y permutaciones no semánticas en pass.
- Verificación: formato, vet, compilación, suite fresca, race completa, race
  ×20, módulos y builds Windows/Linux/macOS en pass; coverage global 91,1 %
  y `internal/agentplanning` 91,0 %.
- Estado: `in_progress`; `ready_for_review` en
  `progress/agent-plan-generation/implementation.md`.

- Fecha: 2026-10-15
- Trabajo: corrección de REV-1 a REV-5 iniciada con regresiones adversariales.
- En curso: recorrido profundo compartido y overflow-safe, cancelación entre
  unidades y colaboradores, causas externas opacas, observer con finalización
  acotada y fingerprint canónico independiente de IDs y orden no semántico.
- Estado: `in_progress`; pendiente completar verificación canónica y devolver
  a review independiente.

- Fecha: 2026-10-15
- Trabajo: primera review de `agent-plan-generation` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: REV-1 filtra valores prohibidos solo en inputs y no en assertions;
  REV-2 no contabiliza trabajo profundo; REV-3 deja recorridos, policy y
  observer fuera de una cancelación acotada; REV-4 expone causas externas;
  REV-5 no canonicaliza por completo grafo y evidence requirements.
- Verificación base: formato, vet, suite fresca, race completa, race x20,
  coverage 91,2 %, módulos y builds Windows/Linux/macOS pasan.
- Estado: `in_progress`; requiere correcciones y regresiones adversariales.

- Fecha: 2026-10-15
- Trabajo: T1-T8 de `agent-plan-generation` completadas y preparadas para
  review independiente.
- Verificación final: formato, vet, compilación, suite completa, race completa,
  race ×20 de paquetes afectados, coverage, módulos y builds
  Windows/Linux/macOS en pass.
- Coverage: 91,2 % global; 91,9 % `internal/agentplanning`, 96,5 % model,
  93,9 % OpenAI, 91,8 % OpenAPI y 89,5 % planning. Baseline 91,1 % preservado.
- Invariantes: una inferencia, cero retry/replan, cero ejecución/persistencia,
  procedencia solo desde operaciones retenidas y terminales sin payloads.
- Estado: `in_progress`; el implementer solicita review en
  `progress/agent-plan-generation/implementation.md` y no modifica
  `feature_list.json` ni `review.md`.

- Fecha: 2026-10-15
- Trabajo: T1-T7 de `agent-plan-generation` implementadas y verificadas de
  forma incremental; T8 y verificación canónica en curso.
- Resultado: protocolo PlanningDecision v1 estricto, procedencia exacta desde
  ReducedContext, deduplicación canónica, materialización sobre ValidationPlan,
  catálogo descriptor-only, policy read-only, sesión estructurada con budgets
  de feature 9 y Responses/SSE compartido.
- Seguridad: cero handles ejecutables, persistencia o mutación de run; método y
  path HTTP se derivan de operaciones retenidas; instrucciones privilegiadas y
  datos no confiables viajan separados.
- Verificación incremental: suite completa en pass; cobertura del paquete nuevo
  `internal/agentplanning` 86,8 %, por encima del mínimo del 85 %.
- Siguiente paso: endurecimiento final, race/repeticiones, coverage global,
  builds, trazabilidad R→test y entrega a review independiente.

- Fecha: 2026-10-15
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`,
  `approved_at: 2026-10-15T15:37:20+02:00`.
- Siguiente fase: implementación T1-T8, comentarios funcionales en cada
  entidad y función, verificación canónica y review independiente.
- Límite: generación y validación únicamente; sin ejecución, evals,
  aprobación de plan ni replanificación.

- Fecha: 2026-10-14
- Trabajo: spec de `agent-plan-generation` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/agent-plan-generation/`.
- Verificación: R1-R12 contienen criterios comprobables; T1-T8 cubren todos
  los requisitos; las tareas y el bloque de aprobación humana siguen
  pendientes.
- Diseño: protocolo PlanningDecision v1 separado del harness incremental,
  materialización sobre ValidationPlan, procedencia OpenAPI cerrada, catálogo
  descriptor-only, policy de solo lectura y cero ejecución durante generación.
- Fuera de alcance: evals (feature 11), aprobación humana (feature 12) y
  replanificación (feature 13).
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: preflight de `agent-plan-generation` completado e inicio de la
  redacción de `specs/agent-plan-generation/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, features 1-9 `done`
  y ninguna feature `in_progress`.
- Contexto clave: restricciones versionadas, contexto OpenAPI reducido,
  catálogo descriptivo de herramientas, modelo estructurado y validación de
  planes ya existen como fronteras separadas.
- Invariante: generar un plan no ejecutará herramientas ni modificará la
  aplicación, evidencias, veredictos o estado ejecutable del run.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: segunda review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R12 y T1-T8 verificados; REV-1 a REV-4 corregidas; formato,
  vet, compilación, suite fresca, race, race x20, coverage, módulos y builds
  Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 97,2 % `internal/model`, 95,0 %
  `internal/model/openai`, 88,4 % OAuth y 87,3 % smoke.
- Prueba real: no ejecutada; permanece opt-in y requiere perfil elegible y
  modelo seleccionado explícitamente.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-14
- Trabajo: segunda review independiente de `model-adapter` aprobada.
- Correcciones verificadas: REV-1 taxonomía/cancelación exacta; REV-2 overflow
  de tokens fail-closed; REV-3 refresh compartido independiente del iniciador;
  REV-4 credential store sin mutex durante I/O y con espera cancelable.
- Verificación fresca: formato, vet, compilación, suite, race completa, race
  x20, módulos, builds Windows/Linux/macOS y coverage global 91,1 % en pass.
- Evidencia: `progress/model-adapter/review.md`; no se ejecutó el smoke real ni
  se resolvieron credenciales.
- Estado: `in_progress`; reviewer solicita al leader la transición a `done`.

- Fecha: 2026-10-14
- Trabajo: primera review de `model-adapter` rechazada; correcciones devueltas
  al implementer.
- Hallazgos: REV-1 pierde categorías de auth/cancelación/timeout; REV-2 permite
  overflow de tokens; REV-3 liga el refresh compartido al primer caller; REV-4
  mantiene un mutex durante I/O del credential store.
- Verificación: formato, vet, compilación, suite, race, coverage 91,1 %,
  módulos y builds Windows/Linux/macOS pasan; la semántica indicada requiere
  regresiones y corrección.
- Estado: `in_progress`; no se ejecutó ninguna prueba real.

- Fecha: 2026-10-14
- Trabajo: T1-T8 de `model-adapter` implementadas y verificadas por el
  implementer; entrega preparada para primera review independiente.
- Resultado: runtime neutral, budgets no monetarios, OAuth Sign in with
  ChatGPT, adapter Responses/SSE, telemetría segura y smoke opt-in.
- Evidencia: `progress/model-adapter/implementation.md`; coverage reportada
  91,1 % global y superior al 85 % en todos los paquetes nuevos.
- Prueba real: no ejecutada; tests ordinarios herméticos.

- Fecha: 2026-10-14
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`,
  `approved_at: 2026-10-14T19:28:41+02:00`.
- Siguiente fase: implementación T1-T8, comentarios funcionales en cada
  entidad y función, verificación hermética y review independiente.
- Prueba real: no se ejecutará sin invocación opt-in explícita y un perfil
  elegible seleccionado.

- Fecha: 2026-10-14
- Trabajo: spec de `model-adapter` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/model-adapter/`.
- Verificación: R1-R12 contienen criterios de aceptación; T1-T8 cubren todos
  los requisitos; el bloque de aprobación humana permanece vacío.
- Alcance: runtime neutral, selección explícita, OAuth de ChatGPT en
  composición, Responses API acotada, presupuestos no monetarios y smoke real
  opt-in separado.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: bloqueo de producto resuelto por decisión explícita del usuario;
  transición de `blocked` a `pending` realizada por el leader y spec reanudada.
- Contrato: runtime independiente del proveedor; proveedor, modelo y perfil de
  autenticación explícitos; ningún modelo por defecto; límites del núcleo por
  llamadas, tokens, duración y replanificaciones, sin precios monetarios.
- Primer adapter: OpenAI Responses API mediante Sign in with ChatGPT OAuth para
  aplicaciones open source elegibles; credenciales resueltas únicamente en la
  composición y ausentes del contrato del harness.
- Restricciones oficiales incorporadas: OAuth público dinámico con PKCE;
  catálogo de modelos por cuenta; `POST /v1/responses` con `store: false` y
  `stream: true`; tokens protegidos fuera de logs, diagnósticos y evidencias.
- Pruebas reales: opt-in y separadas de `go test`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: spec de `model-adapter` bloqueada y transición de `pending` a
  `blocked` realizada por el leader.
- Ambigüedad: proveedor/API, modelo, credencial, endpoint, pricing y prueba real
  no están fijados por el repositorio.
- Propuesta: OpenAI Responses API, modelo explícito, `OPENAI_API_KEY`, endpoint
  oficial, pricing aportado por configuración y prueba real opt-in separada.
- Informe y pregunta: `progress/model-adapter/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: preflight de `model-adapter` completado e inicio de la redacción de
  `specs/model-adapter/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-8 `done` y ninguna feature `in_progress`.
- Contexto clave: adapter real detrás de `harness.Model`, respuesta
  estructurada, clasificación de errores, secretos por entorno, telemetría y
  presupuestos de tokens/llamadas/coste.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 y T1-T7 verificados; overlays REV-1 a REV-7, formato, vet,
  suite, race fresca, race x20, coverage, módulos y builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-14
- Trabajo: correcciones REV-5 a REV-7 de `openapi-inspector` completadas.
- Resultado: paths URI raíz denegados antes de filesystem; siblings de `$ref`
  3.1 preservados mediante composición; headers duplicados por casing
  rechazados de forma determinista, incluido `Content-Type`.
- Verificación: formato, vet, suite fresca, race completa, race x20, coverage,
  `go mod verify`, builds y compilación de tests Linux/macOS en pass.
- Coverage: 91,1 % global y 91,4 % en `internal/openapi`.
- Estado: preparada para tercera review; continúa `in_progress`.

- Fecha: 2026-10-14
- Trabajo: segunda review de `openapi-inspector` rechazada; REV-1 a REV-4
  confirmadas como corregidas y REV-5 a REV-7 devueltas al implementer.
- Hallazgos: path URI con raíz cruza filesystem en Windows; OpenAPI 3.1 pierde
  siblings de `$ref`; headers observados duplicados por casing producen un
  resultado no determinista.
- Estado: requiere correcciones multiplataforma, regresiones de catálogo/
  response/context y tercera review.

- Fecha: 2026-10-14
- Trabajo: correcciones REV-1 a REV-4 de `openapi-inspector` completadas.
- Resultado: matriz de campos OpenAPI obligatorios; denegación URI previa a
  lectura; copia profunda de enums JSON; locations opacas y serialización de
  errores sin tokens externos.
- Verificación: formato, vet, suite fresca, race completa, race x20, coverage,
  `go mod verify` y builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,2 % en `internal/openapi`.
- Estado: preparada para segunda review; continúa `in_progress`.

- Fecha: 2026-10-14
- Trabajo: primera review de `openapi-inspector` rechazada; REV-1 a REV-4
  devueltas al implementer.
- Hallazgos: validación incompleta de campos OpenAPI obligatorios, esquemas URI
  no locales mal clasificados, copia superficial de `Schema.Enum` y locations
  que filtran nombres no confiables.
- Verificación base: formato, vet, suite, race, coverage 91,1 %, cross-builds
  y `go mod verify` pasan; los cuatro casos adversariales fallan.
- Estado: `in_progress`; requiere correcciones, regresiones y nueva review.

- Fecha: 2026-10-14
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-14T17:46:38+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  fixtures locales, canarios y verificación canónica.

- Fecha: 2026-10-14
- Trabajo: spec de `openapi-inspector` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/openapi-inspector/`.
- Verificación: R1-R11 contienen criterios observables; T1-T7 cubren todos los
  requisitos; las tareas siguen pendientes y la aprobación humana está
  pendiente.
- Alcance: carga local OpenAPI 3.0/3.1, refs acotadas, catálogo/security,
  validación determinista de responses y contexto reducido seleccionado.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-14
- Trabajo: preflight de `openapi-inspector` completado e inicio de la redacción
  de `specs/openapi-inspector/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-7 `done` y ninguna feature `in_progress`.
- Contexto clave: carga local OpenAPI 3.0/3.1, referencias acotadas,
  inspección de operaciones, validación determinista de respuestas y contexto
  reducido para el modelo.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 y T1-T7 verificados; formato, vet, suite, race fresca,
  repetición race x20, builds Linux/macOS y regresiones de canarios en pass.
- Coverage: 91,0 % global; todos los paquetes modificados superan 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: REV-6 de `local-validation-cli` corregida tras la segunda review.
- Resultado: paths de errores derivados de mapas externos usan índices
  `members[n]`; nombres del schema siguen siendo legibles; la salida CLI aplica
  un vocabulario cerrado y cae en `document` ante cualquier segmento ajeno.
- Regresiones: claves con CR/LF, tabulador, control, separadores y canarios
  cubiertas en specification, manifest, duplicate keys y planning; stderr se
  mantiene en una sola línea sin contenido externo.
- Verificación: formato, vet, compilación, suite fresca, race completa y race
  x20 en los siete paquetes afectados en pass.
- Coverage: 91,0 % global; 100,0 % cmd, 92,3 % localvalidation, 92,7 %
  planmanifest, 89,9 % planning, 90,6 % assertion, 87,5 % specification/yaml
  y 91,1 % tools/http.
- Estado: preparada para tercera review; continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `local-validation-cli` rechazada; REV-1, REV-3,
  REV-4 y REV-5 confirmadas como corregidas; REV-6 devuelta al implementer.
- Hallazgo: claves JSON controladas pueden filtrarse e inyectar saltos de línea
  en el `field` seguro de stderr.
- Estado: requiere paths públicos con segmentos confiables, regresiones de
  control characters/canarios y tercera review.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 a REV-5 de `local-validation-cli` completadas.
- Resultado: decode anidado estricto; diagnósticos CLI tipados y seguros;
  filesystem/publicación inyectable con commit no-replace; cancelación
  determinista en policy/evidence/assertions; auditoría de comentarios.
- Verificación: formato, vet, compilación, suite completa, race completa, race
  x20 y compilación cruzada Linux/macOS en pass.
- Coverage: 90,8 % global, 100,0 % en `cmd/featureproof`, 92,3 % en
  `internal/localvalidation` y 92,4 % en `internal/planmanifest`.
- Estado: preparada para segunda review; continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: primera review de `local-validation-cli` rechazada; REV-1 a REV-5
  devueltas al implementer.
- Hallazgos: strict JSON incompleto en referencias, diagnósticos CLI sin
  categoría/path, filesystem/publicación no totalmente sustituibles,
  cancelación no cubierta durante policy/evidence/assertions y dos comentarios
  sistémicos ausentes.
- Verificación base: formato, vet, suite, race y coverage global 90,5 % pasan.
- Estado: `in_progress`; requiere correcciones, regresiones y nueva review.

- Fecha: 2026-10-11
- Trabajo: T7 completada; `local-validation-cli` preparada para review
  independiente.
- Verificación: formato, vet, compilación, suite completa, race completa y race
  x20 en paquetes afectados en pass.
- Coverage: 90,5 % global, 100,0 % en `cmd/featureproof`, 89,6 % en
  `internal/localvalidation` y 93,8 % en `internal/planmanifest`.
- Trazabilidad: R1–R11 enlazados con tests reales en
  `progress/local-validation-cli/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: T1–T6 completadas para `local-validation-cli`.
- Resultado: comando estricto, manifiesto JSON v1, composición de features
  1–6, runner secuencial, reportes seguros y bundle atómico implementados.
- Verificación intermedia: suite completa en pass; coverage de paquetes nuevos
  `localvalidation` 85,2 % y `planmanifest` 85,0 %. Pendiente T7 y verificación
  canónica con race detector.

- Fecha: 2026-10-11
- Trabajo: implementación iniciada por T1 tras verificar la aprobación SDD
  y los contratos reutilizables de las features 1–6.
- Estrategia: contrato del comando primero, manifiesto estricto después y
  composición, ejecución y publicación en el orden aprobado.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T21:15:19+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  pruebas herméticas, canarios y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `local-validation-cli` completada y transición de `pending`
  a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/local-validation-cli/`.
- Verificación: R1-R11 contienen criterios observables; T1-T7 cubren todos los
  requisitos; las tareas siguen pendientes y la aprobación humana está vacía.
- Contrato: `--plan` JSON v1 incluye planes, assertions y runtime explícito;
  target, exit codes, cancelación y publicación atómica quedan cerrados.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: la instrucción humana de continuar se registra como aceptación de
  la propuesta `runtime`; transición de `blocked` a `pending` realizada por el
  leader.
- Decisión: el documento versionado de `--plan` incluye configuración explícita
  de HTTP, redacción y policy, validada antes de componer capacidades.
- Siguiente fase: cerrar la spec concreta y presentarla para aprobación SDD.

- Fecha: 2026-10-11
- Trabajo: tercer bloqueo de la spec de `local-validation-cli` y transición de
  `pending` a `blocked` realizada por el leader.
- Ambigüedad: las fronteras HTTP, redaction y policy requieren configuración
  explícita que no existe en YAML v1 ni en los flags acordados.
- Propuesta: incluir una sección `runtime` confiable y validada dentro del
  documento versionado recibido mediante `--plan`.
- Informe y pregunta: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: segundo conjunto de decisiones humanas registrado y transición de
  `blocked` a `pending` realizada por el leader.
- Decisión: `--target` obligatorio sustituye la URL del YAML; exit codes
  0/1/2/3/130; publicación atómica sin sobrescritura, bundle cancelado cuando
  sea seguro y temporal incompleto conservado para diagnóstico si falla la
  persistencia.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: segundo bloqueo de la spec de `local-validation-cli` y transición
  de `pending` a `blocked` realizada por el leader.
- Decisiones pendientes: precedencia de `--target`, valores de exit code y
  semántica atómica de colisiones, cancelación y fallos de persistencia.
- Propuesta completa: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: decisión humana registrada y transición de `blocked` a `pending`
  realizada por el leader.
- Decisión: `featureproof validate` recibirá un segundo archivo versionado con
  planes y assertions mediante `--plan <ruta>`, además de `--spec` y
  `--target`.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: spec de `local-validation-cli` bloqueada y transición de `pending`
  a `blocked` realizada por el leader.
- Ambigüedad: el YAML v1 no contiene planes ni assertions y no existe todavía
  un productor determinista que los derive del texto de las constraints.
- Decisión propuesta: recibir un archivo versionado explícito mediante
  `--plan <ruta>` junto con `--spec` y `--target`.
- Informe y pregunta: `progress/local-validation-cli/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `local-validation-cli` completado e inicio de la
  redacción de `specs/local-validation-cli/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-6 `done` y ninguna feature `in_progress`.
- Contexto clave: componer el flujo local, persistir el layout completo,
  presentar progreso seguro y mapear resultados a códigos de salida.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R10 trazados a tests; formato, vet, compilación, suite, race,
  repetición race, canarios y coverage en pass.
- Coverage: 90,5 % global; todos los paquetes modificados superan 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: T7 de la spec revisada de `evidence-redaction` completada y feature
  preparada para una nueva revisión independiente.
- Resultado: canarios cubren headers raíz default/adicionales, header anidado
  con path explícito, paths JSON, body, input, output y proyección del modelo;
  persistencia numérica y fin de stream quedan trazados a regresiones.
- Verificación: formato, vet, compilación, suite completa, repetición funcional
  x20, race completa y race x20 en pass. Coverage global 90,5 %; `evidence`
  91,0 %, `assertion` 90,6 %, `planning` 89,9 %, `harness` 88,0 % y `jsonpath`
  100,0 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: T2-T4 reabiertas de `evidence-redaction` completadas conforme a la
  spec revisada y aprobada.
- Resultado: headers implícitos limitados a `/headers` raíz exacto; headers
  anidados protegidos solo por paths explícitos; validación estática alineada;
  `FileStore.Get` conserva tokens numéricos con `UseNumber` y exige EOF.
- Verificación intermedia: paquetes afectados repetidos x20 en pass.
- Siguiente fase: T7, suite canónica, race, coverage y trazabilidad actualizada.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: spec revisada aprobada y transición de `spec_ready` a `in_progress`
  realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T20:43:32+02:00`.
- Alcance revisado: headers automáticos solo en `/headers` raíz; ubicaciones
  anidadas mediante paths explícitos; round-trip exacto de números JSON.
- Siguiente fase: completar tareas reabiertas y repetir review completa.

- Fecha: 2026-10-11
- Trabajo: segunda review rechazada y transición de `in_progress` a
  `spec_ready` por cambio material requerido en la spec.
- Hallazgo de diseño: sin un esquema de output, no puede conocerse antes de
  ejecutar si un path arbitrario contendrá una colección `headers` recursiva.
- Trabajo independiente autorizado: corregir round-trip exacto de
  `json.Number` en `FileStore`; la regla de headers queda pausada.
- Estado: requiere spec revisada y nueva aprobación humana antes de reanudar.

- Fecha: 2026-10-11
- Trabajo: REV-1 a REV-4 de `evidence-redaction` corregidas tras la primera
  revisión y preparadas para una nueva revisión independiente.
- Resultado: ancestros de headers protegidos en assertions; resultado del
  sanitizer opaco; records con copia profunda y sello revalidable; ciclos de
  colecciones nombradas detectados desde su identidad original; race
  reproducible mediante la toolchain UCRT64 de MSYS2.
- Verificación: formato, vet, compilación, suite completa, race completa y race
  x20 en paquetes afectados en pass. Coverage global 90,5 %; `evidence` 91,2 %,
  `assertion` 90,6 %, `planning` 89,9 %, `harness` 88,0 % y `jsonpath` 100,0 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: primera review de `evidence-redaction` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: solapamiento incompleto de ancestros de headers sensibles, sello
  de evidence fabricable/aliases mutables, ciclos en colecciones nombradas y
  race detector no reproducible con la configuración usada.
- Estado: `in_progress`; requiere correcciones, tests de regresión, toolchain
  race reproducible y nueva review.

- Fecha: 2026-10-11
- Trabajo: implementación T1-T7 de `evidence-redaction` completada y preparada
  para revisión independiente.
- Resultado: policy inmutable, sanitizer recursivo/cancelable, persistencia
  sellada fail-closed, assertions sensibles restringidas, truncación auditable,
  proyección monotónica del modelo y journal sin payloads.
- Verificación: formato, vet, compilación, suite, race completa, race x20 y
  coverage en pass; 90,5 % global y todos los paquetes modificados >= 85 %.
- Evidencia: `progress/evidence-redaction/implementation.md`.
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: T1-T3 de `evidence-redaction` completadas. Policy inmutable,
  JSON Pointer compartido, redacción recursiva/cancelable, headers protegidos,
  truncación UTF-8 y rechazo previo de comparadores sobre paths sensibles.
- Verificación intermedia: `go test ./internal/...` en pass.
- Siguiente fase: cerrar la frontera sellada de persistencia (T4), proyección
  del modelo (T5) y propagación/canarios (T6-T7).
- Bloqueos: ninguno.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T19:51:22+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales,
  canarios y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `evidence-redaction` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/evidence-redaction/`.
- Verificación: R1-R10 contienen criterios de aceptación; T1-T7 cubren todos
  los requisitos; tareas pendientes y aprobación humana vacía.
- Decisión incorporada: solo `exists`/`absent` pueden solaparse con rutas
  sensibles; cualquier comparación de valor se rechaza antes del run.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: decisión humana registrada y transición de `blocked` al estado
  anterior `pending` realizada por el leader.
- Decisión: las rutas sensibles solo admiten assertions `exists`/`absent`; los
  operadores que necesiten leer el valor se rechazan antes del run.
- Siguiente fase: completar y validar los tres documentos de la spec.

- Fecha: 2026-10-11
- Trabajo: spec de `evidence-redaction` bloqueada y transición de `pending` a
  `blocked` realizada por el leader.
- Ambigüedad: no está definido si una assertion puede evaluar el valor original
  de una ruta sensible o debe limitarse a comprobar su presencia/ausencia.
- Informe y pregunta: `progress/evidence-redaction/spec.md`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `evidence-redaction` completado e inicio de la redacción
  de `specs/evidence-redaction/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-5 `done` y ninguna feature `in_progress`.
- Contexto clave: sustituir el sanitizer superficial por una frontera previa a
  persistencia/modelo que soporte rutas anidadas, límites y vistas distintas.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: segunda review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R11 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,3 % global; 90,4 % en `internal/tools/http` y 90,3 % en
  `internal/planning`.
- Limpieza: eliminado el perfil temporal `$cov` generado en la raíz.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: primera review de `http-tool` rechazada; correcciones devueltas al
  implementer.
- Hallazgos: el dial probaba varias IPs tras un fallo, incumpliendo cero
  retries, y el límite de request headers no incluía headers automáticos del
  transporte.
- Estado: `in_progress`; requiere un único dial, tamaño completo previo a DNS,
  tests de regresión y nueva review.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T19:14:19+02:00`.
- Siguiente fase: implementación T1-T7 con TDD, comentarios funcionales y
  verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `http-tool` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/http-tool/`.
- Verificación: R1-R11 contienen criterios de aceptación; T1-T7 cubren todos
  los requisitos; tareas pendientes y aprobación humana vacía.
- Decisión de alcance: no hay defaults silenciosos; target, métodos, headers,
  timeout, límites y redirects se configuran explícitamente fuera del input.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `http-tool` completado e inicio de la redacción de
  `specs/http-tool/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación presentes, features 1-4 `done` y ninguna feature `in_progress`.
- Contexto clave: el adapter debe quedar detrás del registry, resolver siempre
  contra el target autorizado, revalidar redirects y no persistir evidencia.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R9 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,2 % global; 90,0 % en `internal/assertion` y 90,3 % en
  `internal/planning`.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `assertion-engine` rechazada; REV-1 y REV-2
  confirmadas como corregidas y REV-3 devuelta al implementer.
- Hallazgo: coverage global reproducible de 89,0 %, por debajo del 90,1 % de la
  feature anterior y sin justificación aprobada.
- Estado: `in_progress`; requiere tests de comportamiento relevantes hasta
  recuperar el baseline, reverificación y tercera review.

- Fecha: 2026-10-11
- Trabajo: primera review de `assertion-engine` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: la representación numérica expandía o rechazaba exponentes JSON
  extremos y la cancelación no se observaba durante compilación ni al construir
  resultados sin evidencia.
- Estado: `in_progress`; requiere comparación decimal acotada, propagación de
  contexto por ambas rutas, tests deterministas y nueva review.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T18:41:50+02:00`.
- Siguiente fase: implementación T1-T6 con TDD, comentarios funcionales y
  verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `assertion-engine` completada y transición de `pending` a
  `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/assertion-engine/`.
- Verificación: R1-R9 contienen criterios de aceptación; T1-T6 cubren todos los
  requisitos; bloque de aprobación humana pendiente y código productivo sin
  cambios.
- Revisión del leader: se eliminó la ambigüedad de tokens numéricos en paths;
  las formas con signo o ceros iniciales son inválidas globalmente.

- Fecha: 2026-10-11
- Trabajo: preflight de `assertion-engine` completado e inicio de la redacción
  de `specs/assertion-engine/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes, features 1-3 `done` y ninguna feature
  `in_progress`.
- Contexto clave: debe implementar el puerto `StepAssertionEvaluator` creado
  por la feature 3 sin introducir participación del modelo en el resultado.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: quinta review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1-R9 trazados a tests; formato, vet, compilación, suite, race,
  repetición race y coverage en pass.
- Coverage: 90,1 % global; 89,8 % en `internal/planning`; paquetes modificados
  por encima del mínimo de 85 %.
- Resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-5 y REV-6 de `multi-step-validation-plan`
  implementadas tras la cuarta revisión.
- REV-5: el diagnóstico comprueba pertenencia real al ciclo y escoge el nodo
  cíclico de menor declaration order, sin confundir descendientes bloqueados.
- REV-6: literales `NaN`, `+Inf` y `-Inf` se rechazan como `invalid_value`
  antes de policy o herramienta.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 90,1 % y `planning` 89,8 %.
- Estado: correcciones preparadas para una quinta revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: cuarta review de `multi-step-validation-plan` rechazada; REV-1 a
  REV-4 confirmadas como corregidas y REV-5/REV-6 devueltas al implementer.
- Hallazgos: el diagnóstico de ciclos podía señalar un descendiente externo al
  ciclo y los literales aceptaban `NaN` e infinitos, incompatibles con JSON.
- Estado: `in_progress`; requiere diagnóstico sobre nodos realmente cíclicos,
  rechazo de floats no finitos, tests de regresión y quinta review.

- Fecha: 2026-10-11
- Trabajo: corrección REV-4 de `multi-step-validation-plan` implementada tras
  la tercera revisión.
- REV-4: añadido journal fake configurable y tests de fallo antes de ejecutar
  y después de confirmar evidencia; prueban error operativo, detención de
  selección, preservación del prefijo confirmado y ausencia de
  `plan_completed`.
- Código productivo: revisado; la propagación existente ya detenía el ejecutor
  correctamente, por lo que la corrección requerida es de evidencia de tests.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 90,1 % y `planning` 89,8 %.
- Estado: corrección preparada para una cuarta revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: tercera review de `multi-step-validation-plan` rechazada; REV-1 a
  REV-3 confirmadas como corregidas y REV-4 devuelta al implementer.
- Hallazgo: faltaban tests del fallo operativo del journal antes de ejecutar y
  después de confirmar evidencia, incluida la preservación de hechos previos.
- Estado: `in_progress`; requiere fake de journal configurable, tests de ambas
  fronteras, reverificación y cuarta review.

- Fecha: 2026-10-11
- Trabajo: corrección REV-3 de `multi-step-validation-plan` implementada tras
  la segunda revisión.
- REV-3: cada `PathSegment` clona ahora individualmente sus punteros `Field` e
  `Index`; el test de snapshot muta ambos desde el plan original y desde
  `ValidatedPlan.Plan()` sin alterar el path validado.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 89,8 % y `planning` 89,2 %.
- Estado: corrección preparada para una tercera revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: segunda review de `multi-step-validation-plan` rechazada; REV-1 y
  REV-2 confirmadas como corregidas y REV-3 devuelta al implementer.
- Hallazgo: los punteros `Field`/`Index` de `PathSegment` seguían compartidos
  entre el plan original, el snapshot interno y las copias devueltas.
- Estado: `in_progress`; requiere clonado individual de esos punteros, tests de
  regresión, reverificación y tercera review.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `multi-step-validation-plan`
  implementadas tras la primera revisión.
- REV-1: eliminado `null` del contrato cerrado de `value_type`; un test prueba
  `invalid_reference` antes de policy o herramienta.
- REV-2: las definitions de assertions se copian recursivamente, incluidos
  maps, slices y arrays anidados; el test de snapshot cubre mutaciones desde el
  plan original y desde `Plan()`.
- Verificación final: formato, vet, compilación, suite, `-race`, repetición
  `-race -count=20` y coverage en pass; global 89,7 % y `planning` 89,0 %.
- Estado: correcciones preparadas para una nueva revisión; feature continúa
  `in_progress`.

- Fecha: 2026-10-11
- Trabajo: primera review de `multi-step-validation-plan` rechazada; corrección
  devuelta al implementer.
- Hallazgos: `value_type: null` se aceptaba fuera del conjunto cerrado de R3
  y la copia de definitions de assertions no era profunda para maps/slices.
- Estado: `in_progress`; requiere corrección, reverificación y nueva review.

- Fecha: 2026-10-11
- Trabajo: T6 completada; implementación de `multi-step-validation-plan`
  preparada para revisión independiente.
- Verificación final: formato, vet, compilación, suite completa, `-race`,
  `-race -count=20` y coverage en pass. Coverage global 90,4 % y paquete
  `internal/planning` 90,5 %.
- Trazabilidad: R1-R9 enlazados con tests reales en
  `progress/multi-step-validation-plan/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: T1-T5 completadas para `multi-step-validation-plan`.
- Resultado: modelo y errores tipados, validador de grafo con orden topológico
  estable, referencias desde evidencia confirmada, ejecutor secuencial con
  controles/retries/bloqueos y eventos reconstruibles implementados.
- Verificación intermedia: `go test ./internal/planning ./internal/harness
  ./internal/evidence` en pass; coverage del paquete nuevo `planning`: 89,1 %.
- Siguiente paso: T6, suite canónica completa, race detector y trazabilidad.

- Fecha: 2026-10-11
- Trabajo: T1 iniciada para `multi-step-validation-plan`; definición mediante
  tests del modelo de plan, referencias tipadas, estados y errores estructurados.
- Integración prevista: nuevo paquete `internal/planning` sobre los puertos ya
  existentes de registry, policy, presupuesto, evidence y journal.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T17:46:51+02:00`.
- Siguiente fase: implementación T1-T6, comentarios funcionales en cada
  entidad y función, y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `multi-step-validation-plan` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/multi-step-validation-plan/`.
- Verificación: R1–R9 contienen criterios de aceptación; T1–T6 cubren todos
  los requisitos; las tareas siguen pendientes y el bloque de aprobación
  humana permanece vacío.
- Alcance acordado: la semántica completa de assertions queda para la feature
  4; esta feature define su declaración y el puerto de evaluación.
- Código productivo modificado: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `multi-step-validation-plan` completado e inicio de la
  redacción de `specs/multi-step-validation-plan/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes, dependencias previas `done` y ninguna feature
  `in_progress`.
- Código productivo modificado para esta feature: no.

- Fecha: 2026-10-11
- Trabajo: convención de comentarios aplicada también a `agent-harness`.
- Cambios: entidades, interfaces, adapters, constructores, métodos, helpers y
  closures de orquestación documentados por su responsabilidad sistémica.
- Verificación: `gofmt` sin diferencias, tests de los cinco paquetes de la
  feature 1 y `go vet` en pass.
- Comportamiento y estado de la feature: sin cambios; continúa `done`.

- Fecha: 2026-10-11
- Trabajo: documentación editorial de `validation-specification` ampliada.
- Cambios: comentarios de responsabilidad sistémica añadidos a entidades,
  errores, DTO y funciones; convención registrada en `design.md` para futuras
  iteraciones.
- Verificación: `gofmt -l internal/specification` sin salida,
  `go test ./internal/specification/...` y `go vet` en pass.
- Comportamiento y estado de la feature: sin cambios; continúa `done`.

- Fecha: 2026-10-11
- Trabajo: segunda review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1–R7 trazados a tests; suite, race detector, repetición race y
  coverage 90,1 % en pass; resumen añadido a `progress/history.md`.

- Fecha: 2026-10-11
- Trabajo: primera review de `validation-specification` rechazada; corrección
  iniciada para tipos escalares YAML estrictos, preservación de causas y
  limpieza de perfiles de coverage generados en la raíz.
- Hallazgos: REV-1, REV-2 y REV-3.
- Correcciones: validación estricta de tipos escalares y field paths añadida;
  causa original envuelta con `%w`; perfil `specification` eliminado.
- Reverificación con Go 1.25.1: formato, vet, compilación, tests, race,
  `race -count=20` y coverage pasan. Coverage global 90,1 %; paquetes nuevos
  97,2 % y 87,0 %. Perfil generado en el directorio temporal del sistema.

- Fecha: 2026-10-11
- Trabajo: primera review de `validation-specification` rechazada; correcciones
  devueltas al implementer.
- Hallazgos: tipos escalares YAML no comprobados en campos string (R3/R5),
  causa original del parser no preservada (R5) y perfil de coverage generado
  dentro del repositorio.
- Estado: `in_progress`; requiere corrección, reverificación y nueva review.

- Fecha: 2026-10-11
- Trabajo: precondiciones de implementación verificadas; iniciada T1 con tests
  del modelo interno, identificadores, URL, constraints y límites.
- Alcance: exclusivamente `validation-specification`; `internal/harness` no se
  modifica.
- T1: completada; modelo interno independiente y validación de fronteras
  verificados con `go test ./internal/specification`.
- T2: iniciada; tests de categorías, field paths, duplicados, referencias y
  primer error determinista.
- T2: completada; `ValidationError` soporta `errors.Is`/`errors.As` y el orden
  del primer error es determinista.
- T3: iniciada; adapter YAML v1 estricto con lectura local y cancelación.
- T3: completada; carga v1, versión prioritaria, campos/claves estrictos,
  documentos extra, errores de lectura/sintaxis y cancelación pasan tests.
- T4: iniciada; fixtures de compatibilidad, hermeticidad y cargas concurrentes.
- T4: completada; fixtures v1 válidos/inválidos, literales sin expansión,
  ausencia de red y cargas concurrentes sin estado compartido verificadas.
- T5: iniciada; verificación canónica y trazabilidad final.
- T5: completada con Go 1.25.1; formato, vet, compilación, tests, race y
  coverage pasan. Coverage global 90,8 %, specification 97,2 % y adapter YAML
  89,3 %. Informe: `progress/validation-specification/implementation.md`.

- Fecha: 2026-10-11
- Trabajo: aprobación humana registrada y transición de `spec_ready` a
  `in_progress` realizada por el leader.
- Aprobación: `approved_by: user`, `approved_at: 2026-10-11T16:51:43+02:00`.
- Siguiente fase: implementación T1–T5 y verificación canónica.

- Fecha: 2026-10-11
- Trabajo: spec de `validation-specification` completada y transición de
  `pending` a `spec_ready` realizada por el leader.
- Artefactos: `requirements.md`, `design.md` y `tasks.md` bajo
  `specs/validation-specification/`.
- Verificación: R1–R7 con criterios de aceptación; T1–T5 referencian todos los
  requirements; bloque de aprobación humana pendiente.
- Código productivo modificado: no.

- Fecha: 2026-10-11
- Trabajo: preflight de `validation-specification` completado e inicio de la
  redacción de `specs/validation-specification/{requirements,design,tasks}.md`.
- Precondiciones: primera feature pendiente, `sdd: true`, criterios de
  aceptación suficientes y ninguna feature `in_progress`.
- Código productivo modificado: no.

- Fecha: 2026-10-04
- Trabajo: tercera review aprobada y transición de `in_progress` a `done`
  realizada por el leader.
- Evidencia: R1–R11 trazados a tests; suite, race detector y coverage 90.1 %
  en pass; resumen añadido a `progress/history.md`.

- Fecha: 2026-10-04
- Trabajo: segunda review rechazada; REV-1 y REV-2 resueltos, REV-3 devuelto
  al implementer.
- Hallazgo: agotar retries o tool-call budget durante un retry no termina el
  run de forma inconclusa y puede consumir presupuesto parcialmente (R4/R10).

- Fecha: 2026-10-04
- Trabajo: primera review rechazada; correcciones devueltas al implementer.
- Hallazgos: cancelación del evidence store clasificada incorrectamente (R9)
  y errores obligatorios del journal ignorados (R8).
- Siguiente paso: corregir, repetir la suite canónica y solicitar nueva review.

- Fecha: 2026-10-04
- Trabajo: precondiciones de implementación verificadas; iniciada T1 con tests
  de configuración, transiciones y reserva de presupuesto.
- Alcance: exclusivamente `agent-harness`.
- T1: completada; tests de estado y presupuesto pasan.
- T2/T3: contratos, journal y store local en curso; aún no cerradas.
- T2–T6: completadas y verificadas con tests locales herméticos.
- T7: completada con Go 1.25.1. `gofmt`, `go vet`, compilación, tests,
  `go test -race ./...` y coverage pasan; coverage global 90.8 %.
- Informe: `progress/agent-harness/implementation.md`.
- Primera review corregida: cancelación en persistencia/recuperación y
  confirmación obligatoria de todos los eventos del journal.
- Reverificación completa con Go 1.25.1: formato, vet, compilación, tests, race
  y coverage pasan; coverage global final 90.1 %.
- Segunda review corregida: agotamiento de retries y tool calls reserva ambos
  presupuestos atómicamente y termina el run de forma inconclusa y auditable.

- Fecha: 2026-10-11
- Trabajo: T1-T5 de `assertion-engine` implementadas y verificadas incrementalmente.
- Código: contratos tipados de resultado en `planning`; puerto obligatorio de validación; paquete `internal/assertion` con compilador, paths, comparación exacta, grupos y adapter del executor.
- Tests: validación previa, paths, operadores, grupos completos, resultados inmutables, falta de evidencia, cancelación e integración real con bloqueo de dependientes.
- Estado: pendiente T6, verificación canónica y trazabilidad final.

- Fecha: 2026-10-11
- Trabajo: T6 completada para `assertion-engine`; implementación preparada para revisión independiente.
- Verificación: gofmt, vet, compilación, suite completa, race completa y repetición race x20 en paquetes afectados en pass.
- Coverage: 90,6 % global; 91,6 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Trazabilidad: R1-R9 vinculados a tests reales en `progress/assertion-engine/implementation.md`.
- Estado: `in_progress`; el implementer solicita review y no modifica `feature_list.json`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `http-tool` implementadas tras la primera review.
- REV-1: selección determinista de una sola IP fijada y exactamente un intento de dial por `Execute`, incluso si DNS devuelve alternativas.
- REV-2: medición pre-red del bloque completo de headers serializados y supresión de `Accept-Encoding`/`Connection: close` automáticos no medidos; redirects usan el mismo límite antes del siguiente dial.
- Tests de regresión: varias IPs con primer dial fallido; límite menor que headers automáticos con cero resolver/dial; conteo explícito de Host, User-Agent, Content-Length y headers controlados.
- Estado: pendiente reverificación canónica y nueva review independiente.

- Fecha: 2026-10-11
- Trabajo: REV-1 y REV-2 reverificadas y preparadas para segunda review independiente.
- Verificación: formato, vet, compilación, suite completa, race completa, race x20 y coverage en pass.
- Coverage final: 90,3 % global, 90,4 % en `internal/tools/http` y 90,3 % en `internal/planning`; baseline global 90,2 % preservado.
- Estado: `in_progress`; el implementer solicita nueva review y no modifica `feature_list.json` ni `review.md`.

- Fecha: 2026-10-11
- Trabajo: correcciones REV-1 y REV-2 de `assertion-engine` implementadas tras la primera revisión.
- REV-1: representación decimal simbólica con exponente arbitrario y comparación exacta sin expandir potencias; tests con exponentes fuera de `int32` y coste acotado.
- REV-2: cancelación observada en compilación, construcción de roots sin evidencia y antes de retornos funcionales; tests deterministas sin `time.Sleep`.
- Verificación: gofmt, vet, compilación, suite, race completa y race x20 en pass.
- Coverage: 89,0 % global; 86,1 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Estado: correcciones preparadas para una segunda revisión; feature continúa `in_progress`.

- Fecha: 2026-10-11
- Trabajo: corrección REV-3 de `assertion-engine` implementada tras la segunda revisión.
- Tests añadidos: límites reales de configuración, ciclos de colecciones, igualdad estricta de objetos/arrays/null/bool, numeric missing y agregación de múltiples roots.
- Coverage: 90,2 % global, recuperando el baseline de 90,1 %; 90,0 % en `internal/assertion`; 90,3 % en `internal/planning`.
- Verificación: gofmt, vet, compilación, suite completa, race completa y race x20 en paquetes afectados en pass.
- Estado: corrección preparada para tercera revisión independiente; feature continúa `in_progress`.
- Fecha: 2026-10-11
- Trabajo: implementación de `http-tool` iniciada; precondiciones SDD verificadas y T1 en curso.
- Alcance: adapter `internal/tools/http` con target y permisos externos al input, sin ownership de run, journal o evidence.
- Estrategia: contratos y errores tipados primero; después validación, scope, transporte fijado, captura acotada e integración.

- Fecha: 2026-10-11
- Trabajo: T1-T5 de `http-tool` implementadas y verificadas con tests locales controlados.
- Resultado: configuración inmutable, input v1, target scope, DNS fijado, transporte sin proxy, redirects, timeout/cancelación, límites, filtrado de secretos y output estructurado.
- Verificación intermedia: `go test -cover ./internal/tools/http ./internal/planning` en pass; cobertura `internal/tools/http` 88,3 % y `internal/planning` 90,3 %.
- Siguiente paso: cerrar integración T6, endurecer límites de headers en transporte y ejecutar T7 canónica.

- Fecha: 2026-10-11
- Trabajo: T6 completada para `http-tool` mediante integración real con registry y plan executor.
- Evidencia: la ejecución directa no toca store/journal; al ejecutar el plan, solo el executor persiste y evalúa status/body estructurados.
- Endurecimiento: el transporte corta el bloque de headers en el límite configurado antes de entregarlo al parser HTTP y clasifica `response_limit`.
- Siguiente paso: T7, suite hermética completa, race, coverage global y trazabilidad R1-R11.

- Fecha: 2026-10-11
- Trabajo: T7 completada; `http-tool` preparada para review independiente.
- Verificación: formato, vet, compilación, suite completa, race completa y repetición race x20 en paquetes afectados en pass.
- Coverage: 90,4 % global, 91,2 % en `internal/tools/http` y 90,3 % en `internal/planning`.
- Trazabilidad: R1-R11 vinculados a tests reales en `progress/http-tool/implementation.md`.
- Estado: `in_progress`; el implementer solicita review y no modifica `feature_list.json`.
# Feature activa: model-adapter

- Fecha: 2026-10-14
- Trabajo: correcciones REV-1 a REV-4 implementadas; reverificación canónica en curso.
- Resultado provisional: categorías tipadas y causas de contexto preservadas en
  OAuth/HTTP/SSE; overflow de usage y snapshot cierra la sesión de forma
  saturada; refresh compartido sobrevive a la cancelación del iniciador cuando
  quedan waiters; LocalStore coordina turnos cancelables sin mutex durante I/O.
- Regresiones dirigidas: profile inexistente, browser/listener,
  exchange/refresh, discovery/JWKS, headers, body SSE, `math.MaxInt64`, refresh
  concurrente y espera del store pasan; race ×20 de paquetes afectados pasa.
- Prueba real: no ejecutada; no se usaron credenciales.

- Fecha: 2026-10-14
- Trabajo: correcciones REV-1 a REV-4 reverificadas y preparadas para segunda
  review independiente.
- Verificación final: formato, vet, compilación, suite completa, race completa,
  race ×20, coverage, módulos y builds Windows/Linux/macOS en pass.
- Coverage: 91,1 % global; 97,2 % model, 95,0 % openai, 88,5 % oauth y
  87,3 % smoke. Baseline global preservado.
- Estado: `in_progress`; T1-T8 continúan completas y no se modificaron spec,
  `feature_list.json` ni `review.md` durante esta corrección.
- Prueba real: no ejecutada; no se usaron credenciales.

- Fecha: 2026-10-14
- Trabajo: implementación iniciada por el implementer tras aprobación humana; T1-T2 completadas.
- Resultado: contratos neutrales, selección explícita, taxonomía segura, sesión aislada, deadlines, presupuestos de llamadas/tokens/duración/replans y observer fail-closed implementados.
- Verificación incremental: `go test ./internal/model` en pass. La ejecución local de `-race` requiere habilitar CGO y se repetirá en la verificación canónica con el entorno soportado.
- Siguiente paso: T3-T4, perfiles OAuth, persistencia protegida, sign-in, refresh coalesced y revocación.
- T3-T4 completadas: puertos OAuth sustituibles, callback loopback exacto, PKCE/state/nonce, issued client, scope/identidad, store local atómico con protección owner-only por SO, refresh coalesced y revocación implementados.
- Verificación incremental: `go test -count=20 ./internal/model/openai/oauth` en pass, sin red, navegador ni credenciales reales.
- Siguiente paso: T5-T6, petición Responses, transporte autenticado, SSE y validación estricta de decisión/usage.
- T5-T6 completadas: endpoint oficial fijo, `store:false`, `stream:true`, bearer solo en transporte, campos preview omitidos, SSE acotado y fragmentable, terminal/usage obligatorios, JSON exacto y clasificación por status/code.
- Verificación incremental: `go test ./internal/model/openai` en pass; casos de request, cancelación, CRLF/fragmentación, terminales inválidos, límites y canarios cubiertos.
- Siguiente paso: T7, ejecutable smoke opt-in y composición falsa verificable sin cuenta real.
- T7 completada: ejecutable independiente `featureproof-model-smoke` con acciones `provision`/`infer`, opt-in previo a toda composición, selección explícita y salida allowlisted.
- OAuth real compuesto con autorización dinámica, token/refresh/revocación oficiales, JWKS RS256, loopback y navegador del SO; ninguna credencial se acepta por flags o variables.
- Verificación incremental: tests de proceso para opt-in, argumentos, auth/provider failure, provisioning y éxito en pass. El smoke real no se ejecutó.
- Siguiente paso: T8, integración hermética completa, análisis estático, race, builds y coverage.
- T8 completada: integración fake OAuth → session → OpenAI SSE → harness en pass; no se ejecutan herramientas desde el adapter.
- Verificación canónica final: formato, vet, compilación, suite, race completa, race ×20 de paquetes afectados, coverage, `go mod verify` y builds Windows/Linux/macOS en pass.
- Coverage final: 91,1 % global; model 96,9 %, openai 94,0 %, oauth 88,8 % y smoke 87,3 %.
- Smoke real no ejecutado: permanece opt-in y requiere cuenta OSS elegible, perfil y modelo elegidos por una persona.
- Estado: T1-T8 completas; implementación preparada para review independiente en `progress/model-adapter/implementation.md`.

# Historial inmediato: openapi-inspector

- Fecha: 2026-10-14
- Trabajo: implementación iniciada por el implementer tras aprobación humana; T1 en curso.
- Alcance: nuevo paquete `internal/openapi`, sin cambios en CLI, specification ni plan manifest.
- Estrategia: modelos propios y fronteras locales primero; carga/resolución, catálogo, validación y contexto después.
- T1-T2 completadas: modelos/errores/SourceFS propios, parser JSON/YAML estricto y resolución local acotada con cancelación, containment y presupuestos.
- Verificación incremental: `go test -v ./internal/openapi` en pass; T3-T6 y endurecimiento de coverage en curso.
- T3-T6 completadas: catálogo/security estable, schema validation 3.0/3.1 aislada tras puerto propio, responses deterministas, contexto reducido y fixtures herméticos.
- Coverage incremental de `internal/openapi`: 87,8 %; flujo integral 3.0/3.1, cancelación, scope, canarios e inmutabilidad en pass.
- Siguiente paso: T7, verificación canónica completa y trazabilidad final.
- T7 completada: flujo integral load → catalog → response validation → reduced context probado para OpenAPI 3.0/3.1.
- Verificación final: formato, vet, compilación, suite completa, race completa, race ×20 del paquete afectado, coverage y builds Linux/macOS en pass.
- Coverage final: 91,2 % en `internal/openapi` y 91,1 % global; implementación preparada para review independiente.
- Primera review rechazada con REV-1–REV-4: validación estructural incompleta, clasificación URI insuficiente, copia superficial de enums y locations con tokens externos.
- Correcciones implementadas: matriz estructural 3.0/3.1 previa a `Contract`, parsing URI deny-first, clonado JSON recursivo y diagnósticos/violations con identidades opacas y serialización segura.
- Tests de regresión añadidos para campos obligatorios, `urn:`/`mailto:`/`file:`/network-path, mutación concurrente de enums y canarios en nombres dinámicos.
- Estado: reverificación canónica fresca en curso; T1-T7 siguen completas.
- REV-1–REV-4 reverificadas: suite fresca, race completa, race ×20, coverage,
  `go mod verify` y builds Linux/macOS en pass.
- Coverage final tras correcciones: 91,2 % en `internal/openapi` y 91,1 % global.
- Estado: correcciones preparadas para segunda review independiente; feature continúa `in_progress`.
- Segunda review rechazada con REV-5–REV-7: path URI con raíz dependiente del
  SO, pérdida de siblings de `$ref` en Schema Objects 3.1 y headers observados
  ambiguos por casing.
- Correcciones implementadas: denegación URI-root previa al filesystem;
  composición de `$ref` y siblings 3.1 conservada en validación, catálogo y
  contexto con semántica 3.0 intacta; canonicalización estable de headers con
  rechazo `invalid_input` de claves equivalentes.
- Regresiones añadidas para contadores de frontera, semántica 3.0/3.1,
  `required`/`properties`/`minProperties`, contexto reducido y duplicados de
  headers con valores iguales o distintos repetidos 1000 veces.
- Estado: reverificación canónica fresca para tercera review en curso; T1-T7
  siguen completas.
- REV-5–REV-7 reverificadas: formato, vet, compilación, suite fresca, race
  completa, race ×20, coverage, `go mod verify`, builds y compilación de tests
  de `internal/openapi` para Linux/macOS en pass.
- Coverage final tras la segunda ronda: 91,4 % en `internal/openapi` y 91,1 %
  global; baseline preservado.
- Estado: correcciones preparadas para tercera review independiente; feature
  continúa `in_progress`.

# Feature activa: agent-evaluation-suite

- Fecha: 2026-10-15
- Trabajo: primera review rechazada; correcciones REV-1–REV-9 implementadas y pruebas dirigidas en pass.
- Resultado: confinement resuelto para links intermedios y OpenAPI compuesto desde bytes digest-bound; expectations de missing context y rejection path cerradas; fail-fast uniforme; deadline total y cinco dimensiones de budget comprobadas; safe configuration serializada; baselines autenticadas e invariantes; affected cases por categoría; compatibilidad previa a composición live.
- Evidencia incremental: `go test ./... -count=1`, `go vet ./...`, compilación sin tests y coverage de paquetes afectados en pass. Ninguna eval live, credencial o target real fue utilizado.
- Estado: T1-T9 permanecen completas; verificación canónica fresca en curso para solicitar segunda review.
- Fecha: 2026-10-15
- Trabajo: correcciones REV-1–REV-9 reverificadas y preparadas para segunda review independiente.
- Verificación final: formato, vet, build, compilación sin tests, suite completa, race completa, race ×20 de paquetes afectados, coverage, módulos, `git diff --check` y builds/test-compile Linux/macOS en pass.
- Coverage final: 91,1 % global; 91,0 % en `internal/agenteval`; 89,2 % en `cmd/featureproof`. Baseline global preservado.
- Estado: T1-T9 siguen `[x]`; implementación y trazabilidad actualizadas en `progress/agent-evaluation-suite/implementation.md`. No se ejecutaron evals live, no se usaron credenciales/targets reales y no se modificó el review.
- Fecha: 2026-10-15
- Trabajo: segunda review rechazada; correcciones REV-10–REV-13 implementadas y pruebas dirigidas en pass.
- Resultado: todos los sets de expectations son únicos y se normalizan antes del digest lógico; el SHA wire queda separado como control de integridad. La conversión millis→`time.Duration` es checked en criteria, casos, runner y oracle. `safe_configuration` incorpora `call_timeout_millis` live desde el entrypoint y lo serializa sin profile. Nuevas pruebas de comportamiento recuperan el baseline global.
- Evidencia incremental: coverage exacto mediante `go test -count=1 -coverprofile=<temp> ./...` + `go tool cover -func=<temp>` da 91,1 % global; `internal/agenteval` 91,3 % y `cmd/featureproof` 89,2 %.
- Estado: T1-T9 permanecen completas; verificación canónica fresca en curso para tercera review. No se ejecutaron evals live ni se usaron credenciales o targets reales.
- Fecha: 2026-10-15
- Trabajo: correcciones REV-10–REV-13 reverificadas y preparadas para tercera review independiente.
- Verificación final: formato, vet, build, compilación sin tests, suite completa, race completa, race ×20, coverage exacto con profile + `go tool cover`, módulos, `git diff --check` y builds/test-compile Linux/macOS en pass.
- Coverage reproducible: 91,1 % global, igual al baseline aprobado; 91,3 % en `internal/agenteval` y 89,2 % en `cmd/featureproof`.
- Estado: T1-T9 siguen `[x]`; REV-1–REV-13 cuentan con pruebas directas y trazabilidad actualizada. No se modificaron `feature_list.json` ni el review, y no se ejecutaron evals live ni se usaron credenciales/targets reales.

- 2026-10-15: La primera revisión de plan-approval fue rechazada. Se corrigen REV-1–REV-5: composición obligatoria, provenance, recuperación transaccional, publicación atómica y schema cerrado de eventos.
- Fecha: 2026-10-15
- Trabajo: REV-1–REV-5 de plan-approval corregidas íntegramente.
- Resultado: composición fija con PlanExecutor real y run transicionado por journal; provenance privada de feature 10; recovery transaccional; publicación atómica con rollback/recovery; schema cerrado completo por evento.
- Evidencia: suite canónica, race completa, race ×20, coverage, módulos, diff y builds Linux/macOS en pass. Coverage 91,2 % global y 92,8 % en planapproval; baseline global 91,1 % preservado.
- Estado: T1–T9 continúan completas; implementación preparada para segunda review. No se modificaron feature_list.json ni review.md y no se usaron red, credenciales o targets reales.
- Fecha: 2026-10-15
- Trabajo: REV-6 y REV-7 de plan-approval corregidas tras la segunda review.
- Resultado: propietario único y race-free por run con aggregate no sustituible; comandos sin puntero de run; CAS/proyección/transición serializados; estado privado sincronizado en harness; executor verificado contra el mismo aggregate. Projector exige hashes SHA-256 hex minúsculos exactos y timestamps UTC.
- Evidencia: approve/approve, approve/reject y reject/regenerate sobre el mismo aggregate, append failure y replay alineados con Project; suite completa, race completa, race ×20, coverage, módulos, diff y builds Linux/macOS en pass. Coverage 91,3 % global y 93,1 % planapproval.
- Estado: T1–T9 siguen completas y la implementación queda preparada para tercera review. No se modificaron feature_list.json ni review.md.
- Fecha: 2026-10-15
- Trabajo: REV-8 de `plan-approval` corregida tras la tercera review.
- Resultado: el propietario por run usa una reserva tokenizada y mantiene su
  mutex únicamente durante reserva/verificación/commit; store, hashing,
  sanitizer y `PlanExecutor` operan sin ese lock. Stage/Decide revalidan el
  prefijo y proyectan el evento confirmado antes de publicar el estado; el
  consumo CAS se confirma y libera antes de policy, budgets, tools, waits,
  evidence o callbacks del executor.
- Evidencia: regresiones bloqueantes y reentrantes de store, sanitizer y
  policy/runner terminan sin deadlock; concurrencia, replay y alineación de
  journal conservan un único consumo y estado coherente. Suite completa, race
  completa, race ×20, coverage, módulos, diff y builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,3 % `internal/planapproval`; baseline global
  preservado. T1–T9 permanecen completas y se solicita cuarta review.
- Fecha: 2026-10-15
- Trabajo: REV-9 de `plan-approval` corregida tras la cuarta review.
- Resultado: `LocalStore` coordina carga y recovery por run mediante un owner
  y un canal de finalización cancelable. El mapa compartido se bloquea solo al
  reclamar o publicar el resultado; recovery, lectura, decode y `Project`
  ocurren sin ese mutex. Los fallos eliminan el estado in-flight y permiten un
  retry limpio; el prefijo validado se publica atómicamente.
- Evidencia: un run bloqueado no impide completar otro del mismo root; dos
  handles del mismo run comparten una carga; un waiter cancelado no roba el
  owner; error y cancelación del owner admiten retry sin duplicados ni prefijo
  parcial. Suite completa, race completa, race ×20, coverage, módulos, diff y
  builds Linux/macOS en pass.
- Coverage: 91,1 % global y 91,5 % `internal/planapproval`; baseline global
  preservado. T1–T9 permanecen completas y se solicita quinta review.
# Feature activa: bounded-replanning

- Fecha: 2026-10-16.
- Spec aprobada por el usuario; estado `in_progress` fijado por el leader.
- T1 completada: paquete `internal/replanning`, unión JSON v1 estricta, límites no monetarios y reservas deterministas atómicas. Tests herméticos de ramas, malformed/mixed JSON y concurrencia pasan.
- Siguiente paso: T2, resumen redactado y digest estable.
- T2 completada: summary v1 con orden canónico, SHA-256, pertenencia al run de cada evidence ID, sanitizer en expected/observed, contexto contractual dentro de JSON no confiable y límites de tamaño/nodos. Tests de canario, estabilidad y error operativo pasan.
- T3 en curso: journal CAS/projector, ledger append-only y fingerprint semántico implementados; endurecimiento de schema y tests adversariales pendiente.
- T3 completada: projector valida schema, secuencia, ciclo, decisión, fingerprint, batch y revisión; CAS/idempotencia por run, ledger monotónico y fingerprint de DAG completo. Tests de corrupción, replay conflictivo, carrera CAS y renombrado semánticamente equivalente pasan.
- T4 en curso: preparación de lote mediante `agentplanning.PrepareAdditional` y nueva revisión de `planapproval` tras consumo de grant; faltan pruebas específicas de aprobación fresca.
- T4 completada: check adicional validado por operación retenida, registry, assertions, policy de generación, plan y provenance; un check inválido rechaza lote entero. `ReopenForReplan` permite nueva revisión tras consumir la anterior y el test demuestra que el grant inicial no se reutiliza. Adaptaciones de planapproval limitadas a modelo de evento, projector y workflow.
- T5 en curso: admisión previa de pasos, replans, modelo, validación y tools; faltan pruebas exhaustivas de todas las dimensiones.
- T5 completada: reservas checked de agent steps/tools/validation work; feature-9 Session conserva model calls/tokens/duration/replans; pruebas de cada dimensión, último replan concurrente y agotamiento sin inferencia pasan.
- T6 completada: ledger inmutable por identidad completa, replay idempotente, conflicto fail-closed y agregación `failed > inconclusive > passed` solo con declaraciones requeridas y evidencia confirmada.
- T7 en curso: coordinador secuencial y end-to-end fake funcionan; faltan casos de aprobación requerida, repetición y cancelación por frontera.
- T7 completada: coordinador secuencial une resumen, admisión, decisión, preparación, aprobación, ejecución, ledger y terminal CAS. Tests end-to-end cubren lote permitido, denegación, aprobación nueva, regeneración, repetición y cancelación en modelo/aprobación/persistencia.
- T8 completada: suite adversarial hermética cubre JSON hostil, contexto redactado, lineage corrupto, replays, límite concurrente y CAS ambiguo cancelado; no hay red ni provider real en tests.
- T9 en curso: suite completa, vet y mod verify en pass; coverage de paquetes modificados supera 85%; faltan gates race/builds y documentación final.
- T9 completada: suite completa, compilación, vet, race completa, race ×20 en cuatro paquetes afectados, coverage global 90,2 %, coverage por paquete >=85 %, mod verify, diff y builds Windows/Linux/macOS en pass. Se añadió redacción model-only de assertions y lineage de llamadas reales desde evidencia persistida. Informe en `progress/bounded-replanning/implementation.md`; pendiente de review independiente.
- Primera review independiente de `bounded-replanning` rechazada con REV-1 a REV-4. Se corrigió la agregación de grupos `any` desde sus raíces, la reserva de `validation_work` antes del fingerprint, la reanudación de prefijos confirmados mediante checkpoints tipados y el journal durable con identidades y resultados reales de assertions. Las regresiones prueban recuperación entre coordinadores y handles sin repetir inferencia ni tools, corrupción del lineage y preservación de presupuestos.
- Reverificación posterior: suite, compilación, vet, módulos, diff y builds Windows/Linux/macOS en pass; cobertura global 89,8 % y paquetes afectados `replanning` 85,3 %, `agentplanning` 88,0 %, `planapproval` 89,1 %, `evidence` 89,8 %. Race completa y race ×20 de paquetes afectados en pass. `gofmt -l .` conserva archivos preexistentes fuera del diff, mientras todos los Go nuevos/modificados están formateados. Estado `in_progress`; pendiente de nueva review independiente.
- Segunda review independiente de `bounded-replanning` rechazada: REV-1–4 cerradas; REV-5–7 corregidas. `Restore` reconcilia sesión modelo reutilizada sin perder contadores deterministas; la reanudación de `approval_resolved` reconstruye un grant vigente sin consultar otra vez al humano; `LocalJournal` reserva la transacción por run sin retener el mutex durante I/O. Regresiones con sesión consumida, workflow real y waiter prohibido, y load/publish bloqueados pasan.
- Reverificación actual: suite, vet, módulos, diff, builds Windows/Linux/macOS, race completa y race ×20 en paquetes afectados pasan. Cobertura global 89,6 %; `replanning` 85,2 %, `agentplanning` 88,0 %, `planapproval` 86,8 %, `evidence` 89,8 %. Formato de todos los Go nuevos/modificados en pass; el gate literal `gofmt -l .` conserva baseline ajeno. Estado `in_progress`; pendiente de tercera review independiente.
- Tercera review independiente de `bounded-replanning` rechazada solo por REV-8: `Restore` llamaba a `ModelBudget.Snapshot` bajo `b.mu`. Se corrigió con reserva `restoring` bajo mutex breve, callback externo sin lock y reconciliación final atómica. La regresión bloquea el callback y demuestra que otra reserva y otra restauración responden sin esperar ni consumir trabajo, y que el checkpoint queda completo al terminar.
- Reverificación posterior a REV-8: suite, compilación, vet, módulos, diff, builds Windows/Linux/macOS, race completa y race ×20 de paquetes afectados pasan. Cobertura global 89,6 %; `replanning` 85,2 %, `agentplanning` 88,0 %, `planapproval` 86,8 %, `evidence` 89,8 %. El formato de Go cambiado pasa; el gate literal de repositorio conserva archivos preexistentes fuera del diff. Feature `in_progress`, pendiente de cuarta review independiente.
- Cuarta review independiente de `bounded-replanning` rechazó solo REV-9 documental. Se añadieron comentarios de responsabilidad sistémica a cinco símbolos introducidos en las correcciones; el barrido de todas las declaraciones top-level `type`/`func` de los Go nuevos o modificados no encontró otras omisiones. Sin cambios de comportamiento.
- Verificación posterior a REV-9: formato dirigido, suite completa, vet, módulos, diff y race de `internal/replanning` en pass; la race completa/repetida, cobertura y builds de la cuarta review continúan vigentes. Estado `in_progress`; pendiente de quinta review independiente.

# Feature activa: revision-bound-validation

- Fecha: 2026-10-16. Spec aprobada por el usuario y feature en `in_progress`.
- T1-T2 en curso: se ha creado el modelo versionado e inmutable de binding, las fronteras inyectables de Git/attestation y el resolvedor Git local de solo lectura. Se está verificando con repositorios temporales.
- T3-T4 en curso: admisión fail-closed exige atestación verificada, vigente y coincidente; proyecciones tipadas obligan a declarar material público o referencia opaca de credencial antes del hash.
- T1-T7 completadas: Git local rechaza OID parciales, ref ambiguo y worktree sucio; admisión exige atestación confiable y vigente. Hashes safe separados se revalidan antes de lotes y publicación. El coordinador de replanificación exige binding base y guard, confirma lote inicial y cada digest adicional en su journal. Harness, evidencia sellada y publisher llevan/rechazan referencias de binding inconsistentes.
- Evidencia incremental: `go test ./... -count=1`, `go vet ./...`, `go test -race ./...` en pass. Cobertura dirigida `internal/revisionbinding` 86,8 % y `internal/replanning` 85,0 %; race repetida, builds y cobertura global en curso.
- Producción falla cerrada mientras no exista el atestador concreto y clasificador seguro configurados; el adapter preview pertenece a feature 19.
- T8 completada: tests de flujo con Git temporal, atestador fake, worktree dirty, revisión inexistente, drift de revisión/configuración, PlanBinding previo a eventos funcionales y grant obsoleto pasan. Suite, vet, race completa, race ×20 en paquetes afectados, módulos, diff, compilación y builds Windows/Linux/macOS pasan. Cobertura global 89,2 % (baseline 89,6 %); todos los paquetes afectados superan 85 %. Informe de implementación en `progress/revision-bound-validation/implementation.md`; pendiente de review independiente y evaluación de la justificación de cobertura. El gate literal `gofmt -l .` conserva 135 archivos preexistentes fuera del cambio.
- Correcciones de primera review: GitResolver aísla `GIT_*` heredado; Publisher valida evidencia en todo el árbol de report, exige terminal único congruente y falla cerrado si se cancela staging; Runner clasifica cancelación posterior al último plan antes de publicar. Tests adversariales dirigidos, suite/race completa y race ×20 de `localvalidation` posteriores pasan. Cobertura nueva: global 89,2 %, `revisionbinding` 88,9 %, `localvalidation` 86,6 %. Pendiente del veredicto independiente.
- 2026-10-16: evidence-package T1 completo: esquema v1, límites, error tipado y rechazo de JSON ambiguo/versiones desconocidas; tests dirigidos en pass.
- evidence-package T2: proyección determinista de outcomes ligada a Binding.Verify y VerifyPlanChain; variantes passed/failed/inconclusive y manipulación de identidad/status comprobadas.
- evidence-package T3: fallos hoja/grupo capturados antes de safeAssertions; valores absent/present/redacted/truncated y fallo cerrado ante tipos no clasificables comprobados.
- evidence-package T4: tabla ID→archivo y hechos de herramienta desde Store.Get, con rechazo de ID/ruta/identidad ajenos y preservación del registro truncado.
- evidence-package T5: reproducción declarativa con commit/entorno/digests/selectores y marcador de credencial; sin target ni comandos, test canario en pass.
- evidence-package T6: lector offline con os.Root, inventario SHA-256, límites, rutas canónicas, no-follow, hard links y referencias cruzadas; mutaciones, symlink y cancelación en tests.
- evidence-package T7: Publisher escribe índice e inventario en staging, ejecuta ReadPackage y solo entonces CommitNoReplace; tests de éxito, cancelación y fallos de filesystem en pass.
- evidence-package T8: integración failed/inconclusive/cancelled releída offline y mutaciones adversarias en pass; suite, vet, race completa, builds offline y formato dirigido pasan; cobertura localvalidation 85,0 %, global 88,9 % frente a 89,2 % previa (justificación en implementation.md). Review independiente pendiente.
- evidence-package: endurecimiento final del lector para manifest re-leído y referencias batch/check/escenario/status; suite y race frescas de localvalidation en pass. Race completa anterior al ajuste también en pass; reviewer repetirá gates completos del snapshot final.
- evidence-package: review adicional corrigió referencias de evidencia de steps, BatchID contra topología, procedencia PlanID de hechos de herramienta, coherencia de assertions raíz y SHA/tamaño en cada lectura semántica. Regresiones con dos batches, IDs de step repetidos, manifest rehasheado y cambio entre scans pasan; gates completos en curso.
- evidence-package: events.jsonl verifica IDs de plan, constraint, step y tool call contra topología y registros de evidencia; manipulación de cada ID con manifest rehasheado rechazada. Tests dirigidos pasan; suite y race dirigida posteriores en curso.
- evidence-package: tras correcciones de review pasan suite completa, vet, test sin caché de localvalidation, race dirigida de localvalidation y race fresca de las seis regresiones. Formato dirigido y diff check pasan. Race global/builds offline pertenecen al snapshot previo; reviewer repetirá el gate final.
- evidence-package: la coherencia del árbol también verifica grupos `all`/`any` ternarios. Cobertura final de localvalidation 85,2 %, suite completa, vet y race fresca de regresiones en pass sobre el snapshot final. Ready for review; reviewer repetirá race global y builds offline.

# Feature activa: external-agent-interface

- 2026-10-16: implementación iniciada tras aprobación humana. Precondiciones verificadas: feature 16 en `in_progress`, spec completa y T1–T8 pendientes. T1 en curso: contrato de aplicación v1, estados y errores cerrados compartidos por CLI/MCP.
- T1–T2 completadas: contrato v1 estricto, categorías/estados cerrados, descubrimiento con schemas y autoridad proveniente exclusivamente de identidad local más catálogo protegido. JSON duplicado, desconocido, profundo o sobredimensionado se rechaza sin valores.
- T3 completada: registro JSON privado con publicación atómica, fsync, lock entre procesos, CAS de fases, hashes de idempotencia y lease OS por run. Concurrencia, reinicio y dos procesos conservan una sola reserva.
- T4 completada: supervisor propietario propaga timeout/cancelación, espera workers, recupera owners perdidos sin rerun y no confunde un lease vivo con crash. La composición productiva permanece fail-closed mediante `FailClosedExecutor`; los positivos usan fakes que representan las puertas aprobadas.
- T5 completada: `VerifiedPackageReader` delega exclusivamente en `localvalidation.ReadPackage`; la proyección revalida run/status/referencias, emite handles opacos, restringe vocabularios y elimina paths y texto libre.
- T6–T7 completadas: CLI JSON y MCP stdio `2026-07-28` llaman a `Service.Invoke`. MCP es stateless, reautoriza cada petición, soporta `server/discover`, `tools/list` y `tools/call`, rechaza `initialize`, y separa protocolo de error de aplicación.
- T8 en curso: tests dirigidos y contract suite común pasan. La primera suite global solo falla en el test preexistente de symlink de `internal/localvalidation` porque Windows devuelve `A required privilege is not held by the client`; no es un fallo del diff. Resto de paquetes, incluido `externalagent`, en pass. Pendientes vet/race/coverage/builds y documentación final.
- T8 completada: suite contractual CLI/MCP, idempotencia multiproceso, cancelación cruzada entre procesos, aislamiento, recovery y proyección segura pasan. `go vet`, compilación, módulos, diff, formato dirigido, builds Windows/Linux/macOS, race global con exclusión ambiental y race ×20 de paquetes afectados pasan. Coverage `internal/externalagent` 88,9 % y global comparable 88,8 % al excluir únicamente el test preexistente de symlink que este Windows no puede crear. El `go test ./...` literal sigue bloqueado solo por ese `Skipf` preexistente ejecutado sobre el test padre; el código de feature 16 no interviene. Informe en `progress/external-agent-interface/implementation.md`; listo para review independiente.
- Primera review de `external-agent-interface` rechazada con REV-1–REV-4. Se añadió un journal durable con prefijo ordenado para reserva, inicio, cancelación, terminal, recovery y fallo de publicación, incluida causa cerrada `interrupted`; snapshot y evento se publican en una única transacción. Los schemas de salida ahora describen el envelope y todos los DTO anidados con versiones y enums cerrados. El supervisor cierra `done` después de retirar el worker y liberar la lease. Una tabla contractual única ejecuta por CLI y MCP submit, replay/conflicto idempotente, cancelación, resultado saneado, corrupción, principal ajeno, `in_progress` y cada terminal, valida cada respuesta contra discovery y compara el envelope semántico.
- Reverificación tras REV-1–REV-4: tests dirigidos y 20 repeticiones, suite global comparable, compilación, vet, módulos, diff, formato dirigido, race global comparable, race ×20 y builds Windows/Linux/macOS pasan. Coverage `internal/externalagent` 88,8 % y global comparable 88,7 %. El gate literal conserva únicamente el fallo ambiental/preexistente del symlink de feature 15 en Windows; la review anterior confirmó el mismo subtest en WSL2. No se modificó feature 15. Estado `in_progress`; preparado para segunda review independiente.
- Segunda review de `external-agent-interface`: REV-1–REV-3 cerradas; REV-4 incompleta y nueva REV-5. La tabla contractual común ahora añade `invalid_request` representable en ambas superficies, revocación de scope tras discovery, replay de cancelación y conflicto al reutilizar la misma clave con otro handle. Cada respuesta real continúa validándose contra el schema descubierto por su propio transporte antes de comparar envelopes.
- Corrección REV-5: el supervisor crea un contexto de finalización independiente con deadline antes de registrar `publication_failed` y lo reutiliza para las transiciones finalizing/terminal. Un registro bloqueado respeta el plazo y deja el run recuperable tras liberar worker/lease; un fallo inmediato continúa a terminal `error`. `TestPublicationFailureFinalizationIsBoundedAndRecoverable` prueba ambas rutas mediante `Wait`, `Cancel` y `Close`, sin callback ni worker huérfanos.
- Reverificación tras segunda review: suite dirigida y ×20, suite global comparable, compile-only, vet, módulos, race global comparable, race ×20 y builds Windows/Linux/macOS pasan. Coverage `internal/externalagent` 88,9 % y global comparable 88,7 %. El único bloqueo literal sigue siendo el symlink preexistente de feature 15 ya confirmado bajo WSL2; no se modificaron feature 15, `feature_list.json` ni `review.md`. Preparado para tercera review independiente.

# Feature activa: correction-loop

- 2026-10-16: el leader inicia la fase SDD de la feature 17 desde estado `pending`.
- Preflight completado: no existe otra feature `in_progress`; los nueve criterios de aceptación, la arquitectura, las convenciones, el proceso SDD y los contratos de features 14–16 están disponibles.
- Alcance actual: solo especificación (`requirements.md`, `design.md`, `tasks.md`); no se modificará código ni tests antes de aprobación humana explícita.
- 2026-10-16: el spec author completó `requirements.md`, `design.md` y `tasks.md`; el leader verificó los tres artefactos, R1–R12, referencias T1–T8 y el bloque de aprobación vacío.
- Estado: `in_progress`. El usuario aprobó la especificación al ordenar continuar con la implementación.
- 2026-10-16: implementer inicia T1–T11 en orden. Se implementará `internal/githubpr` con contratos propios, boundary HMAC, stores durables, autorización/head/forks, proyección verificada, publicación idempotente, adapter REST hermético, retry/supervisor y composición productiva fail-closed.
- 2026-10-16: T1–T10 implementadas y verificadas con fake GitHub hermético. El módulo autentica HMAC antes de decode, conserva receipts/asociaciones/intents en journal append-only, refresca y sella el head, aplica forks secretless, proyecta solo packages verificados y reconcilia check/comentario por identidad propia. El adapter REST limita DTOs, permisos y tokens al boundary; retries usan reloj/jitter inyectados y el coordinator hace join. Composición sin preview de feature 19 usa `FailClosedValidation`.
- Cobertura dirigida provisional de `internal/githubpr`: 85,0 %. Falta completar T11 con race, vet, suite global, builds y limpieza de artefactos temporales.
- 2026-10-16: T11 completada. Suite hermética ×20, race dirigida ×10, vet global, compile-only global, módulos, diff y builds Windows/Linux/macOS pasan. Coverage final `internal/githubpr`: 85,1 %. La suite literal global conserva únicamente el fallo ambiental preexistente del symlink de feature 15; race global comparable sin ese paquete pasa. Se limpiaron `githubpr` y `githubpr.cover` de la raíz. T1–T11 quedan completas y la implementación está lista para review independiente.
- 2026-10-16: aprobación explícita del usuario registrada (`approved_at: 2026-10-16T21:51:08+02:00`) y transición del leader de `spec_ready` a `in_progress`.
- Siguiente fase: implementación ordenada de T1–T11, verificación canónica y review independiente.
- 2026-10-16: aprobación explícita del usuario registrada (`approved_at: 2026-10-16T17:25:00+02:00`) y transición del leader de `spec_ready` a `in_progress`.
- Siguiente fase: implementación ordenada de T1–T8, verificación canónica y review independiente.
- 2026-10-16: implementer inicia T1. Se fijan como invariantes la lectura exclusiva mediante `ReadPackage`, reserva durable previa a callbacks, cadena lineal y comparación separada del veredicto actual.
- 2026-10-16: correction-loop T1–T7 completadas. Flujo hermético, idempotencia durable, CAS lineal, preflight, aprobación, failed-first, regresión, comparación y contrato CLI/MCP v2 pasan; coverage 85,1 % en `correction` y 85,9 % en `externalagent`.
- 2026-10-16: T8 completada al localizar MSYS2 UCRT64. Race dirigida, race repetida ×20 y race global comparable pasan; el gate global literal conserva únicamente el fallo ambiental preexistente de symlink de feature 15. Implementación lista para review independiente; informe en `progress/correction-loop/implementation.md`.
- 2026-10-16: primera review independiente rechazada con REV-1–REV-6. Quedan por corregir autorización previa a cancelación, reserva acumulada de llamadas/tokens, store/leases multiproceso por cadena, comparación sin identidades ambiguas, journal detallado de aprobación/escenarios y tests sin `time.Sleep` con comentarios sistémicos completos.
- Estado: `in_progress`; hallazgos devueltos al implementer para corrección y reverificación completa.
- 2026-10-16: REV-1–REV-6 corregidas. La cancelación reautoriza dentro del CAS;
  llamadas/tokens se reservan acumulativamente antes de failed-first, regresión
  y finalización; el store usa locks OS y leases por cadena/handle con recovery
  de owner perdido; comparación ambigua falla cerrada; aprobación y resultados
  por escenario quedan journalizados en orden; los tests no usan `time.Sleep`.
- Regresiones nuevas cubren journal byte-for-byte ante principal ajeno, última
  unidad de presupuesto concurrente, uso de `Finalize`, procesos reales,
  failure injection, lease viva/perdida, identidades v1 ambiguas y resolución
  de aprobación rechazada/stale. El proceso hijo multiproceso tiene timeout
  determinista de 15 segundos.
- Reverificación final: suite comparable, compile-only, vet, módulos, diff,
  builds Windows/Linux/macOS, race global comparable y race ×20 por paquete
  afectado pasan con MSYS2 UCRT64. Coverage: `correction` 85,2 % y
  `externalagent` 85,9 %. T1–T8 permanecen completas; preparado para segunda
  review independiente.
- 2026-10-16: segunda review independiente rechazada. REV-1, REV-2, REV-4, REV-5 y REV-6 quedan cerradas; REV-3 es parcial. REV-7–REV-10 requieren reconciliar append incierto, contabilizar usage terminal una vez, bloquear lecturas de journals activos por cadena y proyectar `limit_exhausted` con paridad CLI/MCP.
- Estado: `in_progress`; hallazgos activos devueltos al implementer para tercera iteración.
- 2026-10-16: REV-7–REV-10 y el remanente de REV-3 corregidos. Las reservas
  con resultado incierto se reconcilian y conservan ownership para un único
  worker; recovery comprueba lease incluso si el proceso reinicia con el mismo
  owner. El ledger suma usage solo en la primera transición terminal.
- El discovery usa índices inmutables con locks OS por handle/run; todo replay
  de journal ocurre bajo el lock de su cadena. Una regresión multiproceso pausa
  un append parcial y demuestra que status/cancel esperan mientras otra cadena
  progresa. Las admisiones agotadas son terminales tipados con las cuatro
  dimensiones allowlisted y paridad/schema CLI-MCP.
- Verificación de tercera iteración: regresiones ×10, suite comparable,
  compile-only, vet, módulos, diff, builds Windows/Linux/macOS, race dirigida,
  race global comparable y race ×20 separada de `correction`/`externalagent`
  pasan. Coverage global 88,4 %, `correction` 85,0 % y `externalagent` 87,2 %.
  T1–T8 permanecen completas; preparado para tercera review independiente.
- 2026-10-16: tercera review independiente rechazada. REV-8–REV-10 y la protección de leases vivas quedan cerradas. Permanecen REV-7 y REV-11–REV-13: reconciliación independiente del contexto cancelado, publicación posterior al accounting de `Finalize`, clasificación tipada de errores de presupuesto y projector estricto de eventos/transiciones.
- Estado: `in_progress`; cuarta iteración asignada al implementer.
- 2026-10-16: REV-7 y REV-11–REV-13 corregidas. La reconciliación de append
  confirmado usa contexto interno acotado y mantiene ownership hasta instalar
  worker o terminal; la regresión cancela exactamente después del append y
  prueba replay/join/cancel sin duplicar hijo.
- Finalize prepara staging y usage antes de cualquier publicación, confirma el
  ledger y solo entonces hace commit del paquete. Los errores de presupuesto
  conservan cancelación/integridad/infraestructura y solo el agotamiento real
  expone una dimensión allowlisted.
- El projector aplica vocabulario cerrado y valida cada evento contra el
  snapshot previo, incluidos lineage, fase, ordinal/tail, hashes, usage,
  approval, confirmaciones, owner y comparison. La tabla adversarial de
  journals desconocidos o contradictorios falla cerrada.
- Reverificación de cuarta iteración: suite afectada y regresiones ×20, vet,
  compile-only, módulos, diff, builds Windows/Linux/macOS, race afectada y
  race global comparable pasan. Coverage global comparable 88,3 %,
  `correction` 85,1 % y `externalagent` 87,2 %. Los gates literales de test y
  race conservan únicamente el fallo ambiental preexistente del symlink de
  feature 15. T1–T8 siguen completas; preparado para cuarta review.
- 2026-10-16: cuarta review independiente rechazada con REV-11 parcial y
  REV-13. Se cerró el protocolo de publicación: preparación durable validada,
  commit idempotente/reconciliable por `PreparationID` y comparison
  idempotente/reconciliable por digest canónico. Failure injection antes y
  después demuestra cero o un artifact, retry, reopen y journal coherente.
- El projector deriva deadline y allowance desde el prefijo, verifica el
  snapshot sellado de policy y la partición/expansión canónica del plan, y usa
  una matriz cerrada de outcome, terminal reason y comparison. Tablas
  positivas/negativas cubren cada invariante autorizada.
- Reverificación de quinta iteración: suite afectada, regresiones y race ×20,
  vet, compile-only, módulos, diff, builds Windows/Linux/macOS y race global
  comparable pasan. Coverage global comparable 88,3 %, `correction` 85,4 % y
  `externalagent` 87,2 %. Test/race literal conservan solo el symlink
  preexistente de feature 15. T1–T8 siguen completas; listo para review.
- 2026-10-16: cuarta review independiente rechazada. REV-7 y REV-12 quedan cerradas y REV-3 está materialmente cerrada. Permanecen REV-11 y REV-13: publicación final/comparison idempotente y reconciliable ante respuesta incierta, y projector con validación semántica completa de deadline, plan, allowance y matriz terminal.
- Estado: `in_progress`; quinta iteración devuelta al implementer.
- 2026-10-16: quinta iteración completada tras el rechazo de la cuarta review.
  `CommitFinalize` valida y journaliza la preparación antes del accounting,
  reconcilia el commit durable por `PreparationID` y garantiza cero o una
  publicación. Comparison aplica el mismo protocolo por digest y reconcilia
  además el append terminal antes/después de `fsync`.
- El projector valida valores derivados y no solo formas: deadline, snapshot
  sellado de policy, plan completo/canónico, allowance remanente exacto y
  matriz cerrada outcome-reason-comparison. Las tablas positivas y negativas
  cubren las mutaciones autorizadas y rechazan corrupción como integridad.
- Verificación final de quinta iteración: suite global comparable, compile-only,
  vet, módulos, diff, builds Windows/Linux/macOS, race global comparable y race
  dirigida ×20 pasan. Coverage comparable global 88,3 % y `correction` 85,2 %.
  Los gates literales mantienen únicamente el symlink preexistente de feature
  15. Se limpiaron binarios y perfiles temporales; T1–T8 permanecen completas
  y la implementación queda lista para nueva review independiente.
- 2026-10-16: quinta review independiente rechazó únicamente REV-14. REV-11 y REV-13 quedan cerradas. Falta intención durable previa a hacer visible la comparación y recovery ante deadline/pérdida de owner entre publicación, resolución y append.
- Estado: `in_progress`; sexta iteración asignada al implementer.
- 2026-10-16: REV-14 corregido. El journal persiste `comparison_intended` con
  digest, comparación canónica y child locator antes de publicar. Cada frontera
  visible usa un contexto interno nuevo, y recovery reclama intents abandonados
  mediante lock/lease antes de completar publicación y append.
- Las regresiones agotan el contexto justo después de crear el artefacto y
  después de resolverlo; también pierden owner y reabren el store en ambas
  ventanas. Todos los casos convergen en un artefacto, un
  `comparison_published`, un terminal coincidente y replay idempotente.
- Verificación de sexta iteración: suite afectada y global comparable,
  compile-only, vet, módulos, diff, builds Windows/Linux/macOS, race global
  comparable y regresiones race ×20 pasan. Coverage global comparable 88,3 %
  y `correction` 85,3 %. T1–T8 permanecen completas; implementación lista para
  sexta review independiente.
- 2026-10-16: sexta review independiente cerró REV-14 y rechazó REV-15–REV-16. Falta sincronizar la instalación de varios workers recuperados y terminalizar de forma durable conflictos/corrupción definitivos del publisher para evitar recovery infinito.
- Estado: `in_progress`; séptima iteración asignada al implementer.
- 2026-10-16: séptima review independiente aprobada. REV-15 y REV-16 quedan cerradas; REV-1–REV-16 no presentan regresiones y R1–R12 están trazados a tests reales.
- Cierre del leader: T1–T8 completas, suite/race comparable y gates obligatorios verificados, coverage `internal/correction` 85,4 %. Feature 17 transicionada a `done`; no queda ninguna feature `in_progress`.
- 2026-10-16: REV-15 y REV-16 corregidos. El constructor instala todos los
  workers recuperados bajo el mismo mutex antes de iniciar goroutines. Una
  barrera con dos cadenas prueba join, terminal único y liberación de leases
  bajo race.
- Los conflictos de digest y bytes corruptos del publisher ahora retiran el
  intent con terminal durable `error/integrity_failure`. Reopen y replay no
  generan `comparison_published`, no duplican el terminal y no reclaman de
  nuevo la cadena.
- Verificación de séptima iteración: regresiones normales y race ×20, suite
  afectada/global comparable, compile-only, vet, módulos, diff, builds
  Windows/Linux/macOS y race global comparable pasan. Coverage comparable
  global 88,3 % y `correction` 85,4 %. El gate literal conserva solo el symlink
  preexistente de feature 15. T1–T8 siguen completas; listo para review.

# Feature activa: github-pr-integration

- 2026-10-16: el leader inicia la fase SDD de la feature 18 desde estado `pending`.
- Preflight completado: no existe otra feature `in_progress`; la feature contiene nueve criterios suficientes y dispone de contratos internos de revisión, evidencias, corrección e interfaz externa.
- Alcance actual: especificación de autenticación GitHub App/webhook, vinculación exacta al head commit, Checks, comentario resumen idempotente, forks no confiables, permisos mínimos y errores/retries. No se modificará código antes de aprobación humana explícita.
- 2026-10-16: el spec author completó `requirements.md`, `design.md` y `tasks.md`. El leader verificó R1–R12, T1–T11, trazabilidad completa, fuentes oficiales de GitHub y bloque de aprobación vacío.
- Estado: `spec_ready`. La implementación queda detenida hasta aprobación humana explícita.
- 2026-10-16: aprobación explícita ya registrada y feature en `in_progress`; T1–T11 implementadas en primera entrega.
- Primera review independiente rechazada con REV-1–REV-9: faltan composición/entrypoint real, reautorización refrescada, leases/recovery multiproceso, retry integrado, installation ID y paginación, ownership previo a update, binding completo del package, payload/command compatibles y trazabilidad R12 real.
- Estado: `in_progress`; hallazgos devueltos al implementer para corrección integral.
- 2026-10-16: corrección integral REV-1–REV-9 completada. La composición
  productiva y `github serve` conectan GitHub App, REST, store, retry,
  publisher, coordinator y webhook, conservando preview fail-closed hasta
  feature 19 y shutdown acotado.
- Se separaron actor del comando y autor de PR; cada frontera refresca autoridad
  y head. CAS+leases durables cubren deliveries, asociaciones e intents con
  renovación, reclaim y recovery, incluida una elección real entre dos procesos.
- Retry/reconcile, 403 rate-limit, installation ID exacto, paginación Link,
  ownership previo a updates, lineage completa del package, proyección de
  evidencias/failures/unverified y normalización ASCII exacta quedaron cubiertos
  por regresiones verticales. El checker de comentarios incluye const/var.
- Verificación: tests afectados ×20, race afectada ×3, suite/race global
  comparable, vet, compile-only, módulos y builds Windows/Linux/macOS pasan.
  Coverage `internal/githubpr` 85,1 %. El gate literal conserva únicamente el
  symlink preexistente de feature 15. T1–T11 siguen completas; listo para review.
- 2026-10-16: segunda review independiente rechazó REV-10–REV-14. Se corrigió
  el replay concurrente con lease vigente, incluido mismo owner, y una prueba
  vertical demuestra una sola admisión y un solo submit.
- Se añadió el caso de uso interno tipado para asociar runs existentes, con
  verificación de tupla autorizada/refrescada, run, repo, head, package run y
  binding. La identidad externa del check ahora nace al confirmar el run, queda
  sellada y distingue dos runs sobre el mismo head.
- Retry entrega deadlines reales a operaciones, producción usa cliente HTTP
  acotado y REST conserva errores transient/rate-limited del token source. El
  marker de comentario exige la primera línea versionada exacta y rechaza
  sufijos, espacios y versiones ajenas.
- Verificación tras REV-10–REV-14: paquete ×20, race paquete ×3, suite/race
  global comparable, cmd, vet, compile-only, módulos, diff y builds
  Windows/Linux/macOS pasan. Coverage `internal/githubpr` 85,0 %; T1–T11
  permanecen completas y la implementación vuelve a `ready_for_review`.
<!-- leader 2026-10-16: segunda revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: la segunda revisión independiente cerró REV-1, REV-2, REV-5, REV-6, REV-7 y REV-8; rechazó la entrega por REV-10–REV-14 (redelivery concurrente, asociación de run existente, identidad de check ligada al run, deadlines/retry de HTTP y marcador exacto de comentario). Se devolvió al implementador con pruebas de regresión obligatorias.
<!-- leader 2026-10-16: tercera revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-10–REV-14 quedaron cerrados en la tercera revisión. La entrega volvió al implementador por REV-15–REV-17: relación insegura entre lease y deadline, recuperación incompleta de intents preparados e invariantes incompletas de lineage durable.
- 2026-10-16: REV-15–REV-17 corregidos. Los leases se validan por encima de
  todos los deadlines con margen y la delivery deduplica durablemente la
  asociación; una prueba A bloqueado → expiry/reclaim B demuestra un submit y
  una escritura de check.
- Los intents conservan el payload seguro original y su digest. El constructor
  los refresca y reconcilia antes de iniciar una fase nueva; restart tras efecto
  incierto recupera checks y comentarios existentes sin otra mutación.
- El linaje durable exige sus cuatro componentes vacíos o completos/coherentes;
  las catorce combinaciones parciales y los boundaries verify/store/publisher
  fallan cerrados.
- Verificación tras REV-15–REV-17: paquete ×20, race paquete ×3, suite/race
  global comparable, cmd, vet, compile-only, módulos, diff y builds
  Windows/Linux/macOS pasan. Coverage `internal/githubpr` 85,1 %. El gate
  literal conserva solo el fallo ambiental de symlink de feature 15. T1–T11
  permanecen completas y la entrega vuelve a `ready_for_review`.
- 2026-10-16: cuarta review rechazó REV-18–REV-20. El presupuesto del run
  empezaba después de Submit, un error de startup podía dejar claims tomados y
  recovery regeneraba el check con configuración actual.
- REV-18–REV-20 corregidos: el deadline del run comienza antes de refresh y
  Submit; dos coordinators/stores prueban exclusión hasta cancel, join y release.
  Todo error de recovery cede asociaciones e intents, incluida asociación no
  relacionada, y el intent vuelve a ser reclamable de inmediato.
- Recovery usa directamente `CheckPublication` y `CommentPublication`
  persistidos tras validar identidad y allowlists. Un cambio de DetailsURL no
  altera el retry; efecto presente se reconcilia y efecto ausente publica el
  payload original exacto.
- Verificación tras REV-18–REV-20: paquete ×20, race paquete ×3, suite/race
  global comparable, cmd, vet, compile-only, módulos, diff y builds
  Windows/Linux/macOS pasan; coverage `internal/githubpr` 85,2 %. El gate
  literal conserva solo el symlink preexistente de feature 15. T4, T6, T10 y
  T11 vuelven a cumplir sus garantías y T1–T11 permanecen completas.
<!-- leader 2026-10-16: cuarta revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-17 quedó cerrado y REV-15/REV-16 mejoraron, pero la cuarta revisión rechazó por REV-18–REV-20: `Submit` fuera del deadline que respalda la lease, claims no liberados ante fallo de recovery y reconstrucción no exacta del payload persistido.
- Corrección posterior a cuarta review completada: REV-18–REV-20 quedan
  cubiertos por las regresiones nominales y gates descritos arriba; la entrega
  vuelve a `ready_for_review` sin modificar el estado de la feature.
<!-- leader 2026-10-16: quinta revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-18 quedó cerrado. La quinta revisión mantuvo abiertos dos caminos de REV-19/REV-20: claims parciales si `ClaimPending` falla internamente y confirmación de un comentario reconciliado cuyo body no coincide con el payload durable.
- Remanentes REV-19/REV-20 corregidos. `ClaimPending` valida primero y publica
  asociaciones e intents como un único lote durable; orphan y fallo de append
  dejan el trabajo válido libre para owner B sin ownership parcial.
- La reconciliación de comentarios compara el body exacto. Body coincidente
  adopta el ID con cero writes; body antiguo adopta el ID y hace un único update
  con el DTO durable antes de confirmar, tanto en publicación normal como tras
  restart.
- Verificación: paquete ×20, race paquete ×3, suite/race global comparable,
  cmd, vet, compile-only, módulos y builds Windows/Linux/macOS pasan; coverage
  85,4 %. T1–T11 permanecen completas y la entrega vuelve a
  `ready_for_review`.
<!-- leader 2026-10-16: sexta revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-19 y REV-20 quedaron cerrados. La sexta revisión dejó un único bloqueo, REV-21: `ClaimBatch` debe trocearse para que cada registro durable quede siempre bajo el límite de lectura del journal y el backlog completo pueda progresar.
- REV-21 corregido. Cada claim batch se mide con su JSON final y queda
  estrictamente por debajo de 1 MiB; el constructor drena lotes sucesivos sin
  volver a emitir claims vivos del mismo owner y revierte lotes anteriores si
  una iteración falla.
- La regresión >1 MiB reabre el store entre lotes, comprueba cada línea,
  procesa cada asociación/intent una vez y verifica renew, confirm, terminal y
  release sin remanentes para owner B. Paquete ×20, race ×3 y coverage 85,4 %
  pasan. Suite/race global comparable, cmd, vet, compile-only, módulos y builds
  Windows/Linux/macOS también pasan; T1–T11 permanecen completas y la entrega
  vuelve a `ready_for_review`.
<!-- leader 2026-10-17: séptima revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-21 quedó cerrado. La séptima revisión dejó REV-22 como único bloqueo: `NewCoordinator` debe recuperar y renovar lote a lote para que un backlog lento no consuma las leases de los claims aún en cola.

- REV-22 corregido: el store reclama un solo grupo de asociación e intents por
  batch y `NewCoordinator` lo refresca, recupera y entrega antes de pedir otro.
  El rollback solo cede el lote actual; el supervisor conserva la propiedad de
  lo ya entregado y lo cancela/join ante un fallo posterior.
- La regresión vertical usa un backlog >1 MiB, reloj que avanza durante cada
  recovery y dos coordinators sobre el mismo journal. Ambos componen sin error,
  ninguna lease expira, cada owner confirma antes de su siguiente claim y cada
  efecto se recupera exactamente una vez.
- Verificación REV-22: paquete ×20 y race paquete ×3 pasan; coverage vuelve a
  85,4 %. Suite y race global comparables, cmd, vet, compile-only, módulos y
  builds Windows/Linux/macOS pasan. El gate literal conserva únicamente el
  fallo ambiental preexistente del symlink de feature 15. T1–T11 permanecen
  completas y la entrega vuelve a `ready_for_review`.
<!-- leader 2026-10-17: octava revisión feature 18 -->
- `github-pr-integration` continúa `in_progress`: REV-22 quedó cerrado. La octava revisión dejó REV-23: durante el drain de startup una asociación no terminal ya entregada a un worker no puede volver a reclamarse si ese worker falla y libera; además debe eliminarse la intermitencia observada en el gate repetido.

- REV-23 corregido: cada startup excluye del store los IDs ya entregados al
  supervisor. Un fallo rápido puede liberar la lease para otro coordinator,
  pero el mismo drain no la reclama otra vez y continúa con el backlog restante.
- La regresión vertical bloquea un recovery independiente, fuerza fallo y
  release de un worker no terminal, verifica que no es reclamable antes del
  release y prueba un único handoff al segundo coordinator después del release.
- El flake se eliminó observando el canal de idle del supervisor en lugar de un
  timeout de pared de un segundo. El test nominal pasa 500 veces consecutivas;
  submit y comentario se validan después de la salida real del worker.
- Verificación REV-23: paquete ×20, race paquete ×3 y race global comparable
  pasan; coverage `internal/githubpr` 85,6 %. Suite comparable, cmd, vet,
  compile-only, módulos y builds Windows/Linux/macOS pasan. El gate literal
  conserva solo el fallo ambiental preexistente del symlink de feature 15.
  T1–T11 permanecen completas y la entrega vuelve a `ready_for_review`.
<!-- leader 2026-10-17: cierre feature 18 -->
- `github-pr-integration` aprobada en la novena revisión independiente. R1–R12 y T1–T11 pasan; REV-1–REV-23 están cerrados. El leader cambia la feature 18 de `in_progress` a `done` y registra el cierre en `progress/history.md`.
