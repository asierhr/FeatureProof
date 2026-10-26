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

## 2026-10-17 — preview-environment-adapter

- Leader preflight completado: feature 19 `pending`, `sdd: true`, criterios de aceptación suficientes y ninguna otra feature activa.
- Se inicia la fase de especificación. El `spec_author` debe producir `requirements.md`, `design.md` y `tasks.md`; no se modificará código antes de aprobación humana explícita.
- Spec completada: 11 requirements EARS y 12 tasks trazables, sin decisiones abiertas ni cambios de código. El leader cambia la feature 19 de `pending` a `spec_ready`.
- Estado: pausa obligatoria en la puerta humana. La implementación comenzará únicamente después de una aprobación explícita de esta spec.
- El usuario aprueba explícitamente la spec el `2026-10-17T18:03:28+02:00`. Se registra la aprobación y el leader cambia la feature 19 de `spec_ready` a `in_progress`.
- Se inicia la implementación de T1–T12; el estado permanecerá `in_progress` hasta una review independiente aprobada.

### Implementer feature 19 — 2026-10-17
- T1–T7 en implementación: creado `internal/previewenv` con modelos sellados, catálogo explícito, contrato Provider no destructivo, autorización de target, bridge de atestación, coordinator acotado, release/recovery durable, fake auditable y bridges de consumidores.
- Suite dirigida inicial cubre selección, idempotencia, target, attestation, readiness, cancelación, timeout, release y clasificación funcional/operacional.
- Estado: in_progress; pendientes composición de secretos, cierre de integración runner, trazabilidad y gates.
- T1–T12 completadas por implementer; trazabilidad y gates registrados en `progress/preview-environment-adapter/implementation.md`.
- Verificación: suite dirigida y repetida, race dirigida/global comparable, vet, compile-only, módulos y builds multiplataforma pasan; cobertura del paquete nuevo 85,6%.
- `go test ./...` conserva únicamente la limitación ambiental preexistente de symlinks Windows en feature 15.
- Estado solicitado al leader: `ready_for_review`; el implementer no cambia `feature_list.json` ni marca la feature done.

- Primera review independiente de feature 19: `rejected`. Se devuelven REV-1–REV-9 al implementador: composición productiva ausente, límites inciertos de reserva, ownership/CAS, lifecycle GitHub, clasificación externalagent, resolución secretless, snapshot seguro, cobertura real de timeouts y `diff --check`.
- Corrección REV-1–REV-9 en curso: los roots CLI, MCP y GitHub ya componen una configuración preview explícita y conservan fail-closed cuando está ausente o incompleta; los tres entrypoints tienen pruebas verticales.
- El lifecycle persiste intención antes de reserve, reconcilia estados `selected`, usa owner/lease/CAS y lock entre procesos, y el gateway GitHub expone handles activos durables, cancelación, restart y join.
- La selección filtra provider/perfil antes de resolver secretos; snapshots hostiles no se persisten; supervisor real distingue error previo a admisión de `inconclusive/cancelled` posterior.
- Tests dirigidos de límites independientes, deadline padre, cancelación, cleanup, multiproceso y shutdown pasan. Cobertura actual de `internal/previewenv`: 85,4 %. Pendientes gates repetidos/globales y actualización final del informe.
- Corrección REV-1–REV-9 completada. Paquete ×20, race paquete y race ×3 pasan; coverage final 85,1 %. Suite global comparable y race comparable, vet, compile-only, módulos y builds Windows/Linux/macOS pasan.
- El gate literal global conserva únicamente el fallo ambiental preexistente del symlink Windows de feature 15, sin cambiar assertions. `git diff --check` queda limpio tras normalizar EOF.
- T1–T12 permanecen completas y el informe actualizado solicita nuevamente `ready_for_review`; el implementer no cambia el estado de la feature.
- Segunda review independiente de feature 19: `rejected`. REV-1–REV-9 quedan cerrados o mejorados, pero se devuelven REV-10–REV-15 sobre recovery tras restart/lease viva, terminales GitHub, inconclusive consumible, target ligado al binding, selección durable completa y observabilidad real.
- Corrección REV-10–REV-15 en curso: un supervisor durable reclama tras expirar
  la lease y siempre reconcilia por key antes de liberar, incluso con store,
  coordinator y provider reconstruidos. Cada salida persiste un terminal; los
  consumidores externalagent/GitHub aceptan la proyección operacional segura
  sin paquete y el polling publica check/comentario terminal reales.
- El binding incluye target canónico completo y policy; la selección durable y
  su CAS cubren secretless, referencia de credencial y el resto de propiedades
  de seguridad. El lifecycle emite eventos y métricas allowlisted persistentes
  para selección, reserva, readiness, ready, drift, release y recovery. Los
  tests dirigidos pasan; quedan coverage y gates finales.
- Corrección REV-10–REV-15 completada. El restart real reconstruye store,
  coordinator y provider con lease inicialmente viva; la publicación
  operacional se valida en externalagent y en check/comentario GitHub reales.
- Verificación final: paquete ×20, race ×3 afectada, suite/race globales
  comparables, vet, compile-only, módulos y builds nativo/Windows/Linux/macOS
  pasan. Coverage: previewenv 85,4 %, githubpr 85,1 % y externalagent 87,0 %.
  El gate literal conserva solo el symlink Windows preexistente; diff y formato
  del conjunto modificado están limpios. Se solicita `ready_for_review`.
- Tercera review independiente: `rejected` por REV-16 y REV-17. El MCP podía
  confirmar `queued` antes del intent preview y el test de restart dependía de
  lease/reloj reales; un CAS final de release tampoco dejaba siempre una
  intención inequívoca.
- Corrección REV-16/17 en curso: `Submit` ejecuta un preflight idempotente que
  persiste `selected` sin owner antes de responder; cierre inmediato y restart
  recuperan ese handoff. Recovery renueva ownership tras claim y Reconcile, y
  persiste `release_pending` antes del provider para que cualquier CAS final
  fallido converja.
- Las regresiones usan reloj lógico y barreras, prueban lease viva, expiry,
  reclaim, orden Reconcile→Release y final CAS fallido. MCP y las tres
  regresiones pasan ×200; quedan coverage y gates globales finales.
- REV-16/17 completadas. El gate ×20 detectó y eliminó un timeout de pared del
  test GitHub operacional; la señal de idle durable pasa las 20 iteraciones.
  Bajo carga global también se probó la renovación segura de una lease expirada
  únicamente cuando su versión no cambió, sin permitir overwrite de otro owner.
- Gates finales: suite y race global comparables, vet, compile-only, módulos,
  builds nativo/Windows/Linux/macOS, AST y diff pasan. Coverage final:
  previewenv 85,4 %, githubpr 85,0 % y externalagent 87,5 %. El único límite
  ambiental continúa siendo el symlink Windows preexistente. Se solicita de
  nuevo `ready_for_review`.
- Tercera review independiente de feature 19: `rejected`. REV-11–REV-15 quedaron cerrados; REV-16 y REV-17 reproducen una aceptación MCP sin intención preview durable y una pérdida de ownership durante recovery/reconcile/release. Se devuelven al implementador con tests deterministas y repetidos.
- Cuarta review independiente de feature 19: `rejected`. REV-16/REV-17 quedaron cerrados; REV-18–REV-20 exigen separar el checkpoint durable del presupuesto de release, hacer linealizable el handoff preflight→worker→recovery y eliminar un deadline de pared frágil bajo race.
- Corrección REV-18–REV-20: cleanup usa presupuestos independientes para el
  checkpoint durable y `Provider.Release`; un fallo controlado del checkpoint
  conserva estado reconciliable y no evita el intento obligatorio de release.
- El preflight persiste `handoff` con owner/lease antes del ACK. Recovery
  respeta el owner vivo; el worker adopta por CAS exactamente una vez y solo un
  crash con lease expirada permite reclaim, Reconcile y Release sin runner.
- La regresión de finalización externalagent sustituyó el deadline real de 40
  ms por barreras explícitas de lifecycle. Regresiones nuevas ×200 y paquetes
  afectados ×20 pasan.
- REV-18–REV-20 completadas. Race afectada ×2 y race global comparable pasan
  con GCC/UCRT64. Suite global comparable, vet, compile-only, módulos, builds
  nativo/Windows/Linux/macOS, AST y diff pasan. Coverage: previewenv 85,4 %,
  githubpr 85,1 % y externalagent 87,4 %. El único límite ambiental sigue
  siendo el symlink Windows preexistente. Se solicita `ready_for_review`.
- Quinta review independiente: REV-18/19 cerrados y REV-20 corregido, pero
  `Cancel` podía observar un terminal antes de que el worker liberase su lease.
  REV-21 vuelve al implementer como único bloqueo.
- Corrección REV-21 en curso: el worker permanece registrado hasta completar
  `ReleaseOwner`, y `Cancel` une `entry.done` aun cuando el registro ya es
  terminal. Una barrera determinista fuerza exactamente esa ventana y prueba
  que Cancel no retorna antes de liberar ownership.
- REV-21 corregida: el orden de cleanup es ReleaseOwner→remove worker→close
  done; por ello una observación terminal une siempre el worker registrado y
  no deja una ventana sin join. La ruta sin worker sigue siendo idempotente y
  una segunda barrera verifica cancelación del contexto durante el join.
- La regresión terminal/finalización pasa ×1000. Race conjunto y cierre de
  coverage/gates están en ejecución.
- REV-21 completada. Race del conjunto afectado ×2, suite afectada y global
  comparable, vet, compile-only, módulos, AST y diff pasan. La cobertura de
  externalagent permanece en 87,4 %. T1–T12 siguen completas y la entrega
  vuelve a `ready_for_review` sin cambiar `feature_list.json`.
- Quinta review independiente de feature 19: `rejected`. REV-18/REV-19 quedaron cerrados; REV-21 exige que `externalagent.Cancel` una cualquier worker local aún registrado aunque el estado durable ya sea terminal, antes de devolver y liberar recursos.
- Sexta review independiente de feature 19: `approved`. REV-1–REV-21 están cerrados, R1–R11 y T1–T12 pasan. El leader cambia `preview-environment-adapter` de `in_progress` a `done` y registra el cierre en `progress/history.md`.

## 2026-10-19 — validation-workflow-orchestration

- Leader preflight completado: feature 20 `pending`, `sdd: true`, criterios suficientes y ninguna feature activa.
- Se inicia la fase de especificación para componer carga de spec, generación agéntica, validación, aprobación y ejecución sin exigir un plan JSON manual.
- Spec completada: 13 requirements EARS y 12 tasks trazables, sin decisiones abiertas ni cambios de código. El leader cambia la feature 20 de `pending` a `spec_ready`.
- Estado: pausa obligatoria en la puerta humana antes de implementar.
- El usuario aprueba explícitamente la spec el `2026-10-19T11:12:00+02:00`. El leader registra la aprobación y cambia únicamente la feature 20 de `spec_ready` a `in_progress`.
- Se inicia la implementación de T1–T12; el estado permanecerá `in_progress` hasta una review independiente aprobada.
- Implementer feature 20: T1-T8 y T11 implementadas en primera pasada. `internal/validationworkflow` contiene modelo cerrado, preflight, store durable CAS/leases/journal, artefacto canónico, aprobación exacta, ejecución/finalización, cancelación y recovery conservador.
- T7 reutiliza el runner histórico mediante `localvalidation.NewPreparedRunner`; el test vertical ejecuta HTTP local, publica package y proyecta el outcome exclusivamente desde `localvalidation.ReadPackage`.
- CLI/API/MCP/GitHub comparten `validationworkflow.Service`; CLI exige flags completos y aliases confiables, MCP no acepta targets crudos y GitHub nunca autoaprueba el comentario de inicio. Queda cerrar el wiring productivo en `cmd/featureproof` y las extensiones del MCP/GitHub existentes antes de marcar T9/T10.
- Tests herméticos del paquete pasan; cobertura actual 85,1 %. Cubiertos pre-admission sin efectos, idempotencia concurrente, approval exacta/stale/reject/disabled, missing context, tampering, recovery por checkpoint, cancel, canarios, paridad de adapters y runner/package vertical.
- T9 ya dispone de composición productiva en `cmd/featureproof`, configuración estricta en snake_case, selección explícita sin modelo por defecto y resolución OAuth confinada al composition root; `workflow start|status|view|approve|reject|cancel|result` está conectado sin alterar `validate --plan`.
- T10 conecta el gateway GitHub al mismo `Service` y añade `workflow mcp serve` con JSON-RPC/MCP versionado, discovery, listado cerrado de tools, argumentos alias-only y límites de entrada. Los tests dirigidos de CLI/API/MCP/GitHub y la compilación del command pasan.
- El artefacto canónico fusiona de forma determinista múltiples scenarios de una constraint para el contrato histórico de un plan por constraint, con IDs de steps namespaced para evitar colisiones. La regresión confirma que ningún check preparado se pierde.
- Implementación feature 20 cerrada para review: T1–T12 están marcadas, el informe R1–R13 está en `progress/validation-workflow-orchestration/implementation.md` y `internal/validationworkflow` alcanza 85,7 % statements.
- Gates: gofmt, vet, compile-only, suite global ordinaria, repetición ×20, race afectada ×3, módulos, builds y diff pasan. La race global conserva únicamente la excepción ambiental conocida del symlink Windows; no se modificó el test.
- 2026-10-19T11:40:00+02:00 — Leader: primera revisión independiente de feature 20 rechazada. Se devuelven al implementer REV-1..REV-9: integración real de aprobación y preview, ownership/leases multiproceso, persistencia y cleanup recuperables, validación productiva, binding canónico completo y saneado de secretos. Estado conservado en `in_progress` hasta una nueva aprobación independiente.
- Review feature 20 rechazada con REV-1–REV-9. Se reabren T3–T8 y T10–T12: aprobación, ownership/leases, atomicidad, cleanup, preview, policy/package y redacción requieren corrección material; no se conservarán como completas hasta verificar cada frontera.
- 2026-10-19 — Correcciones REV-1..REV-9 de feature 20 completadas. Aprobación real con grant one-use, owners multiproceso, heartbeat, checkpoint atómico, intents de package/cleanup, previewenv real, descriptor/policy productivos y proyección canónica saneada quedan integrados y cubiertos.
- Gates finales: validationworkflow y previewenv 85,0 %; pruebas repetidas ×10 y race ×3 afectada pasan; vet, compile-only, módulos, builds Windows/Linux/macOS y diff pasan. La suite/race amplia conserva únicamente el fallo ambiental preexistente de symlink Windows sin privilegio.
- T1–T12 vuelven a completas y se solicita nueva revisión independiente; `feature_list.json` permanece `in_progress` sin cambios de implementer.
- 2026-10-19T12:25:00+02:00 — Implementer: REV-1..REV-9 corregidos y entrega devuelta a revisión independiente. Se reportan aprobación/grant reales, ownership y heartbeat multiproceso, checkpoints e intents recuperables, lifecycle previewenv, policy/descriptor productivos, proyección canónica saneada y binding por digest. El leader mantiene la feature en `in_progress` hasta el veredicto.
- 2026-10-19T13:05:00+02:00 — Leader: segunda revisión independiente rechazada. Cerradas REV-1/2-direct/3/4/5/7; vuelven al implementer los blockers de preview durable, commitments semánticos del plan, sellado del snapshot privado, recovery correcto de plan_ready, deadline durable de aprobación y propagación de contexto en RestoreRun. Estado sigue `in_progress`.
- 2026-10-19 — Correcciones de segunda review completadas: preview sellado se restaura en el mismo proceso y entre owners; valores redactados usan commitments privados por ubicación; el snapshot ejecutable queda ligado al artefacto y se retira al terminal; `plan_ready` restagea exactamente o conserva approval disabled; la espera de aprobación tiene deadline durable y `RestoreRun` recibe el contexto cancelable.
- Pruebas nuevas cubren approve+execute preview, coordinador reconstruido, literals/headers/references/assertions/expected distintos, tampering required y disabled recovery, restage real, expiración cross-owner y cancelación durante restore. Repetición ×10 y race ×3 afectada pasan con GCC/UCRT64; coverage validationworkflow/previewenv queda en 85,0 %. Se solicita tercera revisión sin cambiar `feature_list.json`.
- 2026-10-19 — Tercera review de feature 20 rechazada por REV-13/14: el runner histórico publicaba SHA-256 sin clave para valores redactados y recovery no recalculaba el contexto completo recompuesto.
- REV-13/14 corregidos: el camino histórico no emite commitments sin clave privada fuerte; el workflow mantiene HMAC con nonce aleatorio privado. Recovery recalcula un digest de spec, contrato/OpenAPI, revisión, target/attestation, runtime/policy, provider/model, constraints, capabilities y límites antes de approval o tools.
- Tests de dictionary oracle/package histórico y canarios de clave pasan; drift separado de spec, contrato, OpenAPI, runtime/policy, capability, limits y constraints se rechaza en required, antes de Stage y en disabled recovery cross-owner. Focales ×10 y race ×3 pasan; validationworkflow queda en 85,3 %. Se solicita cuarta revisión sin cambiar `feature_list.json`.
- 2026-10-19T13:42:00+02:00 — Implementer: segunda ronda de correcciones completa (REV-6/8/9/10/11/12), con pruebas cross-owner, tamper, deadlines y cancelación. Entrega enviada a tercera revisión independiente; feature conserva `in_progress`.
- 2026-10-19T14:10:00+02:00 — Leader: tercera revisión rechazada con solo REV-13 y REV-14 abiertos. Se corrigen commitments sin clave en el camino histórico y la recomputación/verificación íntegra de ContextDigest durante recovery. Resto de REV-1..REV-12 cerrado; estado permanece `in_progress`.
- 2026-10-19 — Cuarta review mantiene abierto un remanente de REV-14: target/runtime productivos todavía se resumían por IDs parciales. Se reemplazaron las fronteras de digest por `BindingContext` y `RuntimeContext` efectivos y canónicos; el builder deriva hashes y no acepta hashes del caller como contenido.
- El contexto liga target, EnvironmentID, attestation y validUntil, snapshot preview, manifest HTTP, redaction, registry/schema/validator, policy y network rules. Drift con IDs estables se rechaza antes de Stage/approval/tools en required y disabled recovery cross-owner. Focales ×10 y race ×3 pasan; coverage validationworkflow 85,8 %, previewenv 85,0 %. Se solicita quinta revisión sin cambiar `feature_list.json`.
- 2026-10-19T14:38:00+02:00 — Implementer: REV-13 y REV-14 corregidos; commitments públicos sin clave quedan redactados y recovery verifica el ContextDigest recompuesto antes de cada frontera sensible. Entrega enviada a cuarta revisión independiente; feature sigue `in_progress`.
- 2026-10-19T15:02:00+02:00 — Leader: cuarta revisión rechazada; REV-13 cerrada y REV-14 reducida a canonicalización incompleta de target/runtime/policy/network bajo IDs estables. Se exige ligar contenido efectivo y pruebas de drift subyacente cross-owner. Estado permanece `in_progress`.
- 2026-10-19T15:31:00+02:00 — Implementer: canonicalización efectiva de target/runtime/policy/network completada con pruebas de drift bajo IDs estables. Entrega enviada a quinta revisión independiente; feature sigue `in_progress`.
- 2026-10-19T15:50:00+02:00 — Reviewer/Leader: quinta revisión independiente aprobada; REV-1..REV-14 cerradas, T1–T12 y R1–R13 verificadas. Smoke final de `validationworkflow`, `previewenv` y CLI pasa; feature 20 cambia de `in_progress` a `done` y se registra en `progress/history.md`.
- 2026-10-19T16:05:00+02:00 — Leader: inicia la fase SDD de feature 21 `contract-source-abstraction`. Preflight correcto: estado `pending`, `sdd: true`, acceptance criteria suficientes y ninguna feature activa. Se asigna al spec author; no se autoriza código ni tests antes de aprobación humana.
- 2026-10-19T16:18:00+02:00 — Spec author/Leader: feature 21 especificada en `specs/contract-source-abstraction/` con R1–R12 y T1–T12. Los tres artefactos existen, no hubo cambios de código ni bloqueo y la feature pasa de `pending` a `spec_ready`. Implementación detenida hasta aprobación humana explícita.
- 2026-10-19T13:52:53+02:00 — User/Leader: spec de feature 21 aprobada explícitamente. Se registra la aprobación en requirements, la feature pasa de `spec_ready` a `in_progress` y se autoriza al implementer a ejecutar T1–T12.
- 2026-10-19 — Implementer feature 21: T1–T12 completadas. `internal/contractcatalog` introduce catálogo neutral v1, registry pasivo, procedencia/confianza/completitud por claim, merge canónico con conflictos explícitos, policy obligatoria y reducción relevante/acotada. OpenAPI queda detrás de un adapter desde `Contract` normalizado.
- `agentplanning`, aprobación, replanning, workflow, agenteval y la composición CLI usan el contexto neutral. Catálogo y policy quedan ligados por digest y se revalidan después de inferencia, en recovery y antes de ejecución. `validate --plan` conserva su contrato histórico.
- Verificación: suites afectadas y global comparable pasan, determinismo ×20 y race afectada con GCC/UCRT64 pasan; vet, compile-only y diff pasan. Cobertura del paquete nuevo 87,6 %. La policy copia sus mapas de configuración y una regresión prueba que el caller no puede mutar sus umbrales tras construirla. El único fallo global es el symlink Windows preexistente sin privilegio. Se solicita review independiente sin cambiar `feature_list.json`.
- 2026-10-19T14:35:00+02:00 — Leader: primera review de feature 21 rechazada con REV-1..REV-9. Se reabren T2–T6/T9/T10/T12 por autoridad mutable, pérdida de security/provenance, trust autoafirmado, límites tardíos, canonización/conflictos incompletos, policy no granular, selección productiva indiscriminada, filtración estructural y evidencia de tests/coverage insuficiente. Estado permanece `in_progress`.

- 2026-10-19 — Primera review de feature 21 rechazada con REV-1..REV-9. Se reabren T2–T6, T9, T10 y T12 para corregir autoridad inmutable, procedencia granular, trusted descriptors, límites tempranos, canonicalización, suficiencia tipada, selección productiva, redacción y evidencia de verificación. Feature permanece in_progress.
- 2026-10-19 — Implementer: REV-1–REV-9 corregidos. Catálogo, policy y contexto son inmutables y autoverificables; registry opaco, security/provenance granular, límites anticipados, canonicalización/conflictos, policy por claim, decisiones tipadas, selección productiva estructurada y proyección sensible quedan cubiertos por pruebas adversarias y verticales.
- Verificación de segunda entrega: focales afectados, repetición ×20, race UCRT64, vet, compile-only y diff pasan. Coverage: contractcatalog 88,4 %, openapi 91,6 %, agentplanning 87,3 %, planapproval 86,4 %, replanning 85,2 % y validationworkflow 85,9 %. La suite global reproduce únicamente el symlink Windows preexistente sin privilegio; los demás subtests del parent pasan. T1–T12 vuelven a completas y se solicita segunda review sin cambiar `feature_list.json`.
- 2026-10-19 — Segunda review de feature 21 rechazada con REV-10–REV-13. Se reabren T2, T6 y T9–T12 para congelar metadata de proyección, configurar redacción productiva, corregir compatibilidad OpenAPI protegida/cancelable y demostrar cobertura diferencial y fronteras estáticas reales. Feature permanece `in_progress`.
- 2026-10-19T15:40:00+02:00 — Implementer: REV-1..REV-9 corregidos y T2–T6/T9/T10/T12 cerradas con evidencia. Focales/repetidas/race pasan; coverage afectado entre 85,2 % y 91,6 %, incluido planapproval 86,4 %. Entrega enviada a segunda revisión independiente; feature sigue `in_progress`.
- 2026-10-19T16:20:00+02:00 — Leader: segunda review rechazada; REV-1/2/4/5/6/7 cerradas y se reabren T2/T6/T9–T12 por REV-10..REV-13: aliasing de ProjectionPolicy, redacción productiva no configurable, pérdida de security schemes/contexto cancelable en compatibilidad y cobertura/spies insuficientes. Estado sigue `in_progress`.
- 2026-10-19 — Implementer: REV-10–REV-13 corregidos. `SourceRequest` y projection quedan sellados con copias separadas para caller/registry/adapter; la composición exige redacción productiva completa y la prueba vertical bloquea nueve superficies antes de modelo/approval/tools; la compatibilidad preserva OAuth 3.0/3.1 y propaga cancelación; un adversario efectivo más AST sustituyen el spy inerte. Focales ×20, race UCRT64 y coverage reproducible >=85 % pasan. La suite global conserva únicamente el symlink Windows preexistente. T1–T12 vuelven a completas y se solicita tercera review sin cambiar `feature_list.json`.
- 2026-10-19 — Tercera review de feature 21 rechazada con un único blocker REV-14. Se reabren T3/T12 para acotar y cancelar el traversal de defaults/examples y tipar toda cancelación de `NeutralizeReducedContext` como `contractcatalog.CategoryCancelled`; feature permanece `in_progress`.
- 2026-10-19 — Implementer: REV-14 corregido. La extracción de defaults/examples es iterativa, cancelable, detecta ciclos y limita depth/nodes/pending/bytes/values antes de publicar el contrato; cancel y deadline de compatibilidad usan `contractcatalog.CategoryCancelled`, ubicación estructural y causa desempaquetable. Pruebas focales ×20, race UCRT64, coverage y gates pasan; la suite global conserva solo el symlink Windows preexistente. T3/T12 vuelven a completas y se solicita cuarta review sin cambiar `feature_list.json`.
- 2026-10-19T17:05:00+02:00 — Implementer: REV-10..REV-13 corregidos; focales ×20/race pasan y coverage de composición/reducer supera 85 %. T2/T6/T9–T12 se cierran con evidencia y la entrega pasa a tercera review independiente. Feature continúa `in_progress`.
- 2026-10-19T17:42:00+02:00 — Leader: tercera review rechazada con un único blocker REV-14. REV-10/11/13 cerrados; se corrige cancelación y límites del recorrido de defaults/examples y la clasificación segura de cancel/deadline en compatibilidad. Estado permanece `in_progress`.
- 2026-10-19T18:08:00+02:00 — Implementer: REV-14 corregido con traversal iterativo, cancelable, acotado y all-or-nothing; errores cancel/deadline tipados y seguros. Focales/race/coverage/gates pasan y la entrega se envía a cuarta review. Feature sigue `in_progress`.
- 2026-10-19T16:09:42+02:00 — Reviewer/Leader: cuarta revisión independiente aprobada; REV-1..REV-14, R1–R12 y T1–T12 verificados. Smoke final de todos los paquetes afectados pasa; feature 21 cambia de `in_progress` a `done` y se registra en `progress/history.md`.
- 2026-10-19T16:10:40+02:00 — Leader: inicia fase SDD de feature 22 `go-backend-inspector`. Preflight correcto: `pending`, `sdd: true`, acceptance suficiente, feature 21 cerrada y ninguna feature activa. Se asigna al spec author; código y tests quedan prohibidos hasta aprobación humana.
- 2026-10-19T16:15:42+02:00 — Spec author/Leader: feature 22 especificada en `specs/go-backend-inspector/` con R1–R13 y T1–T12. Los tres artefactos existen, cubren patrones cerrados net/http/Chi, hermeticidad, límites, procedencia, cierre PR e integración derived; no hubo cambios de código ni bloqueo. Pasa de `pending` a `spec_ready` y se detiene hasta aprobación humana explícita.
- 2026-10-19T16:16:15+02:00 — User/Leader: spec de feature 22 aprobada explícitamente. Se registra la aprobación en requirements, la feature pasa de `spec_ready` a `in_progress` y se autoriza al implementer a ejecutar T1–T12.
- 2026-10-19 — Implementer feature 22: T1–T12 completadas. `internal/goinspector` incorpora snapshot/digest sellados, filesystem hermético, índice AST local, patterns cerrados net/http y Chi v5, parámetros, schemas, responses/auth, diagnostics/impact y adapter `go-static-v1`.
- La composición workflow congela revisión/roots/build/auth/generated-policy/límites, carga la source derived y exige sufficiency policy explícita antes de inferencia. `contractcatalog` conserva metadata neutral de diagnostics e impacto ligada a su digest.
- Verificación: focales ×20, race UCRT64, vet y compile-only pasan; coverage goinspector 85,9 % y contractcatalog 87,4 %. La suite global conserva únicamente el fallo ambiental preexistente de symlink Windows sin privilegio. Informe en `progress/go-backend-inspector/implementation.md`; feature permanece `in_progress` para review independiente.
- 2026-10-19T17:10:00+02:00 — Leader: primera review de feature 22 rechazada con REV-1..REV-9. Se reabren T2–T12 por snapshot/TOCTOU, closure y scopes, identidad/control-flow de handlers, índices/build selection, provenance/impacto, límites tardíos, redacción, integración multi-source/API/GitHub y cobertura productiva. Estado permanece `in_progress`.
- 2026-10-19 — Primera review feature 22 rechazada con REV-1–REV-9. Se reabren T2–T12 por TOCTOU, closure/scope, identidad/flujo de handlers, índices/build context, provenance/impacto, budgets/cancelación, redacción, composición multi-source y cobertura vertical. Corrección material en curso; feature permanece `in_progress`.

- 2026-10-19: Feature 22 review corrections REV-1..REV-9 implemented: handle-bound no-follow RepositoryFS, explicit entry closure/package identities, exact handler bindings/control flow, sealed build matrix, per-claim provenance/full PR closure, unified cancellation, projection redaction, and single combined OpenAPI+Go catalog. Focal x20, UCRT64 race, vet and compile gates pass; goinspector coverage 85.0%. T2-T12 reclosed with evidence; awaiting second review.
- 2026-10-19T18:25:00+02:00 — Implementer: REV-1..REV-9 corregidos y T2–T12 cerradas con evidencia. Snapshot no-follow, closure/identidades/control-flow, build matrix, provenance/impacto, budgets/redacción y catálogo combinado quedan implementados; focales ×20/race/gates pasan. Entrega enviada a segunda review; feature sigue `in_progress`.
- 2026-10-19T19:05:00+02:00 — Leader: segunda review rechazada; REV-1..REV-9 se reducen a siete blockers: handles de ancestors/lectura concurrente, identidad package+scope, responses por control-flow, provenance/impacto de schemas, límites truncados como éxito, projection incompleta y composición API/GitHub/cobertura. Se reabren tasks afectadas; feature sigue `in_progress`.

- 2026-10-19: Segunda review de feature 22 rechazada (REV-1..REV-7); T2-T12 reabiertas. En corrección: traversal descriptor-relative, package/scope identity, responses por path, provenance schema/impact, límites atómicos, ProjectionPolicy única y composición vertical compartida.
- 2026-10-19 — Implementer: REV-1..REV-7 de la segunda review corregidos. RepositoryFS retiene la cadena completa de handles y rechaza reemplazos/junctions; índices y scopes usan identidades locales; responses se separan por path; provenance e impacto incluyen tipos/campos/middleware; límites son atómicos y ProjectionPolicy queda sellada en toda salida.
- CLI, API/MCP y GitHub comparten el puerto neutral de autoridad y un único catálogo OpenAPI+Go. GitHub liga head, roots y changed paths confiables antes de admisión; las pruebas verticales cubren coalescing/conflicto, policy y drift sin efectos posteriores.
- Evidencia final: focales afectados ×20, junction Windows ×20, race UCRT64, vet y coverage diferencial pasan; goinspector alcanza 87,7 %. La suite global conserva únicamente el fallo ambiental histórico de symlink Windows sin privilegio. T2–T12 vuelven a completas y se solicita tercera review sin cambiar `feature_list.json`.
- 2026-10-19 — Tercera review de feature 22 rechazada con REV-1..REV-7; se reabren T2–T9 y T11–T12 para estabilidad in-place, imports/scopes, guardas, interpretación de responses, closure de schemas, coverage completo de composición y race global.
- 2026-10-19 — Implementer: REV-1..REV-7 corregidos. RepositoryFS compara metadata y doble lectura del mismo handle; imports usan package declarado; scopes invalidan parámetros/var/bloques/fields; guardas y responses usan objetos exactos e intérprete por path; schemas devuelven closure/provenance anidada.
- El gate diferencial cubre configuration/composition/reducers/authority/admission/GitHub entre 85,7 % y 100 %. `fakeModelBudget` queda sincronizado; focal race ×100 y conjunto afectado race ×2 pasan. Dos suites race globales no detectan carreras; la pasada final solo falla por el symlink Windows histórico. T2–T12 vuelven a completas y se solicita cuarta review sin modificar `feature_list.json`.
- 2026-10-19T20:05:00+02:00 — Implementer: siete blockers de segunda review corregidos; T2–T12 cerradas con evidencia. Focales/junction ×20, race UCRT64 y gates pasan; goinspector 87,7 % y reducers/composición >=85 %. Entrega enviada a tercera review; feature continúa `in_progress`.
- 2026-10-19T21:05:00+02:00 — Leader: tercera review rechazada con REV-1..REV-7: write in-place no detectado, imports/sombras léxicas incompletos, guard homónimo, estado HTTP por path, cierre de schemas anidados, coverage diferencial incompleto y carrera real en fakeModelBudget. Se reabren tasks afectadas; feature permanece `in_progress`.
- 2026-10-19T22:00:00+02:00 — Implementer: REV-1..REV-7 de tercera review corregidos; T2–T12 completas. Focales ×20, race focal ×100, race afectada ×2 y race global ×2 pasan salvo symlink histórico; coverage diferencial de 17 funciones supera 85 %. Entrega enviada a cuarta review, feature sigue `in_progress`.
- 2026-10-19T23:05:00+02:00 — Leader: cuarta review rechazada con REV-8..REV-15: fail-before-I/O/deadline único, custom JSON marshalers, object identity Decode/Encode, writer commit/control-flow, provenance por fact, coverage exhaustivo, timeout no determinista GitHub e import externo implícito inseguro. Tasks afectadas reabiertas; feature sigue `in_progress`.
- 2026-10-19 — Quinta corrección feature 22: REV-8..REV-15 cerrados. La autoridad completa se valida antes de RepositoryFS y un único deadline atraviesa freeze→registry→inspect→projection; custom JSON y Decode/Encode usan identidad de receiver/objeto exacta; writer commit y control flow conservan solo facts demostrables; provenance queda separada por fact/schema/status/middleware; imports externos implícitos no elevan autoridad.
- Gate diferencial migrado a manifest versionado exhaustivo con marcadores fuente y `canonicalBindingContext`; focales y conjunto combinado pasan ×20, race afectada y global ×2 no detectan carreras. La global reproduce únicamente el symlink Windows histórico sin privilegio. Goinspector queda en 86,9 %. T1–T12 continúan completas y se solicita quinta review sin cambiar `feature_list.json`.
- 2026-10-20T00:05:00+02:00 — Implementer: REV-8..REV-15 corregidos; T1–T12 completas. Focales y conjunto CLI/workflow/GitHub ×20, coverage exhaustivo y race afectada/global ×2 pasan salvo symlink histórico. Entrega enviada a quinta review; feature continúa `in_progress`.
- 2026-10-20T01:00:00+02:00 — Leader: quinta review rechazada con cuatro blockers REV-16..REV-19: request receivers por nombre, provenance auth cruzada, identidad auth local colisionable y MaxDuration ignorado ante deadline padre posterior. Resto de REV-1..REV-15 cerrado; feature permanece `in_progress`.
- 2026-10-19 — Sexta corrección feature 22: REV-16..REV-19 cerrados. Todos los facts request usan el ast.Object exacto; auth usa identidad canónica por import path/directorio, provenance por matcher/scheme/aplicación y colisiones rechazadas; inspectionContext toma siempre el deadline mínimo sin reiniciar presupuesto.
- Pruebas nuevas cubren shadows request sin claims, dos schemes con negativas cruzadas/alias externo/changed impact, absence sin provenance middleware, digest/copia/colisiones auth y deadlines deterministas sin sleeps. Focales y conjunto combinado pasan ×20; race afectada UCRT64 ×2 pasa y race global ×2 reproduce solo el symlink histórico, sin carreras. Coverage goinspector 86,4 % y gate diferencial pasan. T1–T12 siguen completas; se solicita sexta review sin cambiar `feature_list.json`.
- 2026-10-20T01:35:00+02:00 — Implementer: REV-16..REV-19 corregidos; focales goinspector/combined ×20, coverage y race afectada/global ×2 pasan salvo symlink histórico. Sexta entrega enviada a review independiente; feature sigue `in_progress`.
- 2026-10-20T02:00:00+02:00 — Leader: sexta review rechazada con tres blockers REV-20..REV-22: identidad auth sin separación de dominio, presupuesto temporal reiniciable en APIs públicas y doble inferencia concurrente real en replanning. Feature 22 se mantiene `in_progress`; se exige ownership antes del modelo y race global limpio.
- 2026-10-19 — Implementer: séptima corrección de feature 22 completa. REV-20 separa auth local/external-explicit con identidad tipada, import path/alias/símbolo y digest sellado; REV-21 conserva un DeadlineAt absoluto desde freeze hasta registry/inspect/projection con reloj y barreras; REV-22 adquiere ownership antes de presupuesto/modelo y seguidores releen el terminal durable.
- Evidencia final: adversario concurrente de 32 workers pasa `-race -count=500` con una inferencia; goinspector y combined OpenAPI+Go pasan ×20; coverage exhaustivo pasa (goinspector 86,7 %); race afectada ×2 y global UCRT64 ×2 no detectan carreras. La global final solo falla en las dos repeticiones del symlink Windows histórico sin privilegio. El temporal `goinspector-seventh` fue eliminado. T1–T12 continúan completas y se solicita séptima review sin modificar `feature_list.json`.
- 2026-10-19T18:00:00+02:00 — Leader: séptima review rechazada. REV-20/21 cerradas; REV-22 exige ownership durable/CAS multiproceso antes del modelo y REV-23 exige cierre GitHub determinista bajo repetición. Feature 22 permanece `in_progress`; no se inicia feature 23 hasta cerrar estos blockers.
- 2026-10-19 — Implementer: octava corrección de feature 22 completa. REV-22 sustituye la autoridad process-local por lease durable generacional en ReplanJournal; LocalJournal persiste owner/expiry/generation bajo lock multiproceso y Coordinator adquiere/renueva/libera antes de restore, budgets y modelo. Dos coordinators/handles y dos procesos reales prueban una sola inferencia, heartbeat, cancel/error y recovery tras expiry.
- REV-23 separa el contexto interno de cleanup/join del caller y prioriza un join ya confirmado; el focal GitHub pasa ×1000 sin sleeps. Goinspector/replanning/GitHub y combined pasan ×20; focal durable race ×50 y concurrencia ×500 pasan; coverage: goinspector 86,7 %, replanning 85,0 %, GitHub 85,1 % y manifest completo. Race afectada y global UCRT64 ×2 no detectan carreras; la global solo reproduce el symlink Windows histórico. T1–T12 continúan completas; se solicita octava review sin modificar feature_list.json.
- 2026-10-19T19:00:00+02:00 — Leader: octava review rechazada. Lease durable existe, pero REV-22 exige fencing generacional atómico en checkpoint/eventos antes del modelo y coordinators reales multiproceso; REV-23 exige un único presupuesto cancel→finalize→release→join sin deadlines competidores. Feature 22 sigue `in_progress`.
- 2026-10-20T11:11:14+02:00 — Reviewer/Leader: duodécima review aprobada; REV-1–REV-26, R1–R13 y T1–T12 verificados. Smoke final de inspector, catálogo, replanning, workflow, GitHub y CLI pasa; feature 22 cambia de `in_progress` a `done` y se registra en history.
- 2026-10-19 — Implementer: novena corrección de feature 22 completa. REV-22 añade fencing atómico owner+generation+expiry a lecturas, checkpoints, CAS y `ReserveInference`; dos coordinators en procesos distintos demuestran que un owner reanudado tras reclaim no cruza el modelo. REV-23 unifica cancel→finalize→release→join bajo un único lifecycle y conserva ownership aunque el caller esté cancelado o venza el presupuesto.
- Evidencia: multiproceso ×20 y race ×20, stale fence race ×500, focal GitHub ×2000 y conjunto afectado ×20 pasan. Coverage goinspector 86,7 %, replanning 85,2 % y githubpr 85,5 %; race afectada/global UCRT64 ×2 solo reproduce el symlink Windows histórico. Temporales de build eliminados. T1–T12 siguen completas y se solicita novena review sin cambiar `feature_list.json`.
- 2026-10-20 — Implementer: décima corrección de feature 22 completa. REV-24 hace explícitos y validados TTL/renew/store timeout/Clock/Waiter, acota cada operación owned y da prioridad determinista al heartbeat encolado frente a presión posterior del store. El adversario sin tiempo real prueba progreso más allá de la expiración original, rehidratación cross-handle y rechazo de la generación stale tras reclaim.
- Evidencia REV-24: rehidratación histórica ×200, replanning ×50, adversarios scheduler ×500, combined afectado ×20 y multiproceso/race ×20 pasan. Race afectada ×2 y global ×2 no muestran carreras; la global conserva únicamente el symlink Windows histórico sin privilegio. Coverage y gates pasan; T11–T12 siguen completas y se solicita décima review sin modificar `feature_list.json`.
- 2026-10-20 — Implementer: undécima corrección de feature 22 completa. REV-25 valida sin overflow la envolvente TTL/renew/store timeout y rechaza toda suma no representable; tabla de límites, propiedad con oráculo `big.Int`, fuzz sin panic y composición cubren igualdad, `+1`, `MaxInt64` y negativos.
- Evidencia REV-25: focal ×1000, replanning ×50, combined ×20, fencing multiproceso/race ×20 y race afectada ×2 pasan. Race global ×2 no detecta carreras y conserva solo el symlink histórico sin privilegio; coverage replanning 85,3 %, manifest, vet, compile, formato y diff pasan. Se solicita undécima review sin modificar `feature_list.json`.
- 2026-10-20 — Implementer: duodécima corrección de feature 22 completa. REV-26 sustituye los dos cierres fijos de un segundo por `FinalizeTimeout` explícito y validado dentro del envelope TTL/renew/store; heartbeat continúa durante cleanup independiente y recovery reutiliza el prefijo proyectado para acotar su lectura+CAS.
- Evidencia REV-26: model_error histórico ×1000, adversarios deterministas y race ×100, combined estándar ×20, correction/race ×100 y race afectada ×2 pasan. Race global ×2 conserva solo el symlink histórico; coverage replanning 85,5 %, manifest, vet, compile, formato y diff pasan. Se solicita duodécima review sin modificar `feature_list.json`.
- 2026-10-20T11:15:00+02:00 — Leader: inicia fase SDD de feature 23 `spring-boot-backend-inspector`. Preflight correcto: feature 22 cerrada, 23 `pending`, `sdd: true`, acceptance suficiente y ninguna feature activa. Se asigna al spec author; no se autoriza código ni tests antes de aprobación humana.
- 2026-10-20T11:17:44+02:00 — Spec author/Leader: feature 23 especificada en `specs/spring-boot-backend-inspector/` con R1–R13 y T1–T13. Los artefactos cubren patrones Spring MVC cerrados, DTOs/responses, seguridad declarativa no ejecutable, hermeticidad, límites, changed closure e integración derived. Pasa de `pending` a `spec_ready`; implementación detenida hasta aprobación humana explícita.
- 2026-10-20T11:18:38+02:00 — User/Leader: spec de feature 23 aprobada explícitamente. Se registra aprobación en requirements, pasa de `spec_ready` a `in_progress` y se autoriza al implementer a ejecutar T1–T13.
- 2026-10-20 — Implementer feature 23: T1–T13 completadas. `internal/springinspector` incorpora input/revisión/source sets/release/límites sellados, parser Java pasivo, lista cerrada Spring MVC, parámetros, DTOs, respuestas, seguridad declarativa inerte, diagnostics/changed closure y adapter `spring-static-v1`. La composición workflow reutiliza el RepositoryFS descriptor-relative de feature 22 y liga autoridad PR/local antes de inferencia. Focales, repetición, compile, vet, diff y race UCRT64 pasan; coverage springinspector 88,4 % y funciones productivas modificadas de composición >=90,5 %. Entrega preparada para review independiente; feature permanece `in_progress`.
- 2026-10-20T12:05:00+02:00 — Leader: primera review de feature 23 rechazada con blockers REV-1–REV-10 y high REV-11. Se requieren correcciones de sellado de autoridad, resolución Java ambigua, aliases/constantes, semántica por método, DTOs y `ResponseEntity`, límites/cancelación, diagnósticos, revalidación durable, composición multifuente y procedencia explícita. Feature 23 permanece `in_progress`; todos los hallazgos se devuelven al implementer antes de una nueva review independiente.
- 2026-10-20T13:00:00+02:00 — Implementer/Leader: REV-1–REV-11 corregidas con pruebas adversariales. El inspector sella root/framework/repo/revision, falla cerrado ante ambigüedad, conserva branches y procedencia, propaga límites/cancelación, revalida inmediatamente antes de consumir aprobación/ejecutar y compone una lista explícita OpenAPI/Go/Spring. Focales, repeticiones, race UCRT64, vet, compile-only, formato y cobertura pasan; se solicita segunda review independiente y la feature sigue `in_progress`.
- 2026-10-20T14:00:00+02:00 — Leader: segunda review rechazada. REV-1–REV-11 quedan cerradas; aparecen REV-12 (persistir y refrescar autoridad GitHub `head`/changed paths en `Decide` y pre-tools, incluso cross-owner/restart) y REV-13 (aplicar degradación Lombok/Jackson/custom shape a DTOs anidados). T3/T6/T12/T13 se reabren y feature 23 permanece `in_progress`.
- 2026-10-20T15:00:00+02:00 — Implementer/Leader: REV-12 y REV-13 corregidas. La referencia GitHub completa se persiste y se refresca de forma autenticada pre-aprobación y pre-ejecución, con pruebas cross-owner/restart y cero efectos ante drift; DTOs anidados aplican la misma degradación conservadora para Lombok/Jackson/custom serializers. Gates focales/repetidos/race/vet/compile/formato/cobertura pasan; se solicita tercera review y la feature sigue `in_progress`.
- 2026-10-20T16:00:00+02:00 — Leader: tercera review rechazada por residuo de REV-12. Persistencia, refresh cross-owner y checkpoints son correctos, y REV-13 queda cerrada, pero `githubpr.RESTAdapter` productivo no consulta `/pulls/{number}/files`; changed-path drift solo estaba cubierto por fakes. Se exige paginación REST real, fail-closed y prueba vertical; feature 23 continúa `in_progress`.
- 2026-10-20T17:00:00+02:00 — Implementer/Leader: residuo REV-12 corregido en producción. `RESTAdapter` obtiene changed files autenticados y paginados con límites, canonicalización estricta, rechazo fail-closed y doble metadata para evitar TOCTOU; una prueba vertical TLS real demuestra 0 aprobación/consumo/tools ante drift de paths con head estable tras restart/cambio de owner. Gates y cobertura pasan; se solicita cuarta review y feature 23 sigue `in_progress`.
- 2026-10-20T18:00:00+02:00 — Reviewer/Leader: cuarta review aprobada; REV-1–REV-13, R1–R13 y T1–T13 verificados sin nuevos blockers. Smoke final de Spring inspector, catálogo, workflow, GitHub y CLI pasa. Feature 23 cambia de `in_progress` a `done` y se registra el cierre en history.

## 2026-10-20 — Feature 23, remediación de revisión

Se corrigieron REV-1..REV-11: autoridad root/framework sellada, resolución ambigua fail-closed, aliases y constantes, semántica body por método, DTO/FQCN conservadores, ResponseEntity por returns, propagación de budgets, diagnósticos, revalidación durable, composición multi-source explícita y procedencia repository/revision. Los gates focales, `vet`, compile-only global, repeticiones, race UCRT64 y cobertura diferencial quedaron verdes; la feature permanece `in_progress` a la espera de nueva revisión independiente.

## 2026-10-20 — Feature 23, segunda remediación

La segunda revisión confirmó REV-1..REV-11 y reabrió T3/T6/T12/T13 por REV-12 y REV-13. En curso: persistencia de la referencia autenticada del PR y refresco de `head`/changed paths en cada reconstrucción durable, junto con degradación conservadora de Lombok/Jackson/serialización personalizada en DTOs Java anidados.

REV-12 y REV-13 quedan corregidas. La autoridad GitHub completa se persiste en el comando durable y se refresca mediante el adapter autenticado antes de aprobación y nuevamente antes de consumir el grant/tools, incluso con otro owner tras reinicio; drift de head o changed paths cierra el flujo sin efectos. Los DTO anidados aplican las mismas guardas Lombok/Jackson/custom serialization que los tipos superiores, incluidas declaraciones profundas y lookalikes negativos. T3/T6/T12/T13 vuelven a completas. Focales ×20, repeticiones, race UCRT64, vet, compile-only, formato y cobertura pasan; feature sigue `in_progress` para revisión independiente.

## 2026-10-20 — Feature 23, tercera remediación

La tercera revisión cerró REV-13 y la arquitectura durable de REV-12, pero reabrió T12/T13 porque `RESTAdapter.PullRequest` productivo todavía no leía los changed paths. En curso: lectura autenticada/paginada de `/repos/{owner}/{repo}/pulls/{number}/files`, doble observación de metadata para coherencia de head, límites explícitos y prueba vertical con el adapter real.

REV-12 residual queda corregido. `RESTAdapter.PullRequest` obtiene la colección completa de files con credenciales de instalación, límites explícitos, validación estricta y metadata antes/después para impedir TOCTOU; ordena paths antes de sellar el snapshot. La prueba vertical con TLS real, stores reabiertos y otro owner demuestra que mutar solo changed paths con head estable produce cero `Approval.Decide`, `Consume` y tools. T12/T13 vuelven a completas; focales/repeticiones/race/vet/compile/cobertura/formato pasan y la feature continúa `in_progress` para review.

- 2026-10-20T18:15:00+02:00 — Leader: inicia SDD de feature 24 `generated-contract-review` en `feature-24-generated-contract-review`, basada en el commit `3a89a7f` que consolida las features 21–23. Preflight correcto: estado `pending`, `sdd: true`, acceptance suficiente y ninguna feature activa. Se asigna al spec author; código y tests quedan prohibidos hasta aprobación humana.
- 2026-10-20T18:30:00+02:00 — Spec author/Leader: feature 24 especificada en `specs/generated-contract-review/` con R1–R13 y T1–T13. La spec cubre diff estructural, decisiones totales/selectivas, approval binding e invalidación, autoridad acotada, export OpenAPI 3.1, publicación atómica y superficies compartidas. Pasa de `pending` a `spec_ready`; implementación detenida hasta aprobación humana explícita.
- 2026-10-20T16:31:20+02:00 — User/Leader: spec de feature 24 aprobada explícitamente. Se registra la aprobación en requirements, pasa de `spec_ready` a `in_progress` y se autoriza al implementer a ejecutar T1–T13 en `feature-24-generated-contract-review`.
- 2026-10-20 — Implementer feature 24: primera entrega T1–T13 completa en `internal/contractreview`, con diff, decisiones, store, fuente aprobada, exporter, publisher y adapters; focales/repeticiones/race/vet/compile pasan y coverage alcanza 85,0 %. Se solicita review independiente sin cambiar el estado.
- 2026-10-20 — Leader: primera review de feature 24 rechazada con REV-1–REV-13 (12 blockers y 1 high). Los gates técnicos pasan, pero se reabren T2–T13 por integración productiva ausente, revalidación/baseline autoafirmada, diff no atómico, límites incompletos, export con pérdidas, elevación epistémica, publisher/store no robustos, idempotencia, redacción y binding GitHub/baseline incompletos. Feature permanece `in_progress`.
- 2026-10-20 — Leader: segunda review de feature 24 rechazada. REV-2/7/11/13 quedan cerradas; REV-1/3/4/5/6/8/9/10/12 permanecen abiertos por superficies sin composición productiva, baseline previa sin revalidación, dependencias/selectividad defectuosas, provenance truncada, metadata OpenAPI mal localizada, publisher no portable/descriptor-relative, reconcile ambiguo, journal/root/locks y actor GitHub no autenticado. T2–T6, T8–T11 y T13 se reabren; feature sigue `in_progress`.
- 2026-10-20 — Leader: tercera review de feature 24 rechazada. REV-2/3/4/5/7/11/13 quedan cerradas y builds Windows/Linux/Darwin pasan; REV-1/6/8/9/10/12 siguen abiertos por auth/schema de superficies, metadata OpenAPI localizada incompleta, operaciones aún path-based, ventana crash rename→marker, bounds/locks/lifecycle del store y cruce de PR/actor/resumen GitHub. T6 y T8–T13 se reabren; feature permanece `in_progress`.
- 2026-10-21 — Leader: cuarta review de feature 24 rechazada. REV-2–REV-8 y REV-11–REV-12 quedan cerradas; persisten REV-1 (flags cruzadas y falta vertical listener real), REV-9 (temp parcial pre-sync irreconciliable), REV-10 (retry create idéntico puede devolver aggregate vacío) y REV-14 (runtime GitHub no posee/cierra handles del review service). T6/T9/T10/T12/T13 se reabren; feature sigue `in_progress`.
- 2026-10-21 — Leader: quinta review de feature 24 rechazada. REV-1–REV-14 quedan cerradas; aparecen REV-15 (cancelación durante CAS no propagada) y REV-16 (dos exports idénticos concurrentes pueden divergir y dejar temp huérfano). T6/T9/T12/T13 se reabren; feature permanece `in_progress`.
- 2026-10-21 — Leader: sexta review de feature 24 rechazada. REV-1–REV-14 permanecen cerradas; REV-15 persiste por clasificación `unavailable` tras cancelación post-sync, REV-16 por takeover temporal sin lease que puede truncar un writer vivo y aparece REV-17 por bloqueo del estrés concurrente en `intent_absent`. T6/T9/T12/T13 siguen abiertas; feature permanece `in_progress`.
- 2026-10-20T16:40:00+02:00 — Implementer feature 24: preflight correcto en `feature-24-generated-contract-review`; spec aprobada y feature `in_progress`. Comienza T1 con el modelo versionado, canonicalización, límites y errores seguros; no se modifica `feature_list.json`.
- 2026-10-20T17:20:00+02:00 — Implementer feature 24: T1–T4 completas con pruebas adversariales. El artefacto es canónico/inmutable, baseline falla cerrado, el diff distingue add/modify/remove/uncertain sin inferir ausencias parciales y el reducer aplica decisiones totales/selectivas con revisión CAS. `go test ./internal/contractreview -count=1` pasa; continúa T5.
- 2026-10-20T18:10:00+02:00 — Implementer feature 24: T5 y T7 completas. El servicio aplica autorización, reloj confiable, revalidación durable e invalidación antes de decidir/exportar/usar; `ApprovedSource` entra por el Registry real de `contractcatalog`, añade autoridad humana y conserva lineage derived. T6 sigue abierta hasta completar adversarios filesystem/crash.
- 2026-10-20T19:30:00+02:00 — Implementer feature 24: T6 y T8–T12 completas. Store local recupera ambas ordenaciones de crash checkpoint/journal, CAS concurrente e idempotencia; exporter genera OpenAPI 3.1 validado por el loader local; publisher aplica replace por digest y bloquea traversal/links; CLI/API/MCP/GitHub delegan al mismo servicio con head/changed-path binding. Focales de review/catalog/cmd pasan; comienza T13 y gates.
- 2026-10-20T21:30:00+02:00 — Implementer feature 24: T13 completa y entrega `ready_for_review`. Focales afectados ×20, workflow/CLI ×20, race UCRT64 ×2, vet, compile global, formato y diff pasan. Coverage: contractreview 85.0 %, composición CLI nueva 87.7 %. La suite global solo reproduce el fallo ambiental histórico de symlink Windows sin privilegio. No se modifica `feature_list.json`.

## 2026-10-20 - Feature 24 remediation after independent review

- Independent review rejected the first implementation with REV-1..REV-13.
- Reopened T2-T13. T1 remains closed because its original model evidence remains valid; manifests and limits are extended under T2-T5.
- Current order: authoritative baseline and complete snapshot revalidation; atomic diff/decision; durable manifests, policy and export protocol; secure publisher/store; productive workflow/GitHub/API/MCP integration; adversarial gates.

## 2026-10-20 — Feature 24 remediation complete

- REV-1..REV-13 quedan corregidas con integración real en workflow, baseline
  durable sin booleano autoafirmado, diff atómico, límites completos, exporter
  explícito, autoridad aditiva, publication prepare/commit/reconcile, locks OS,
  redacción ligada por digest y snapshot GitHub durable.
- T1–T13 están completas. Focales, repetición ×10, race UCRT64, vet,
  compile-only, cross-compile Linux, formato y diff pasan. Coverage:
  `contractreview` 85,0 %, `contractcatalog` 85,3 % y
  `validationworkflow` 85,6 %.
- La global conserva únicamente el fallo histórico de symlink Windows sin
  privilegio. Se solicita segunda review independiente; la feature permanece
  `in_progress` y no se modifica `feature_list.json`.

## 2026-10-20 — Feature 24, segunda remediación

- Segunda review rechazada: REV-2/7/11/13 permanecen cerradas; se reabren T2–T6, T8–T11 y T13 para corregir REV-1/3/4/5/6/8/9/10/12.
- Orden activo: autoridad/baseline/diff/límites; exporter/publisher/store; composición productiva API/MCP/GitHub; gates y trazabilidad.

## 2026-10-20 — Feature 24, segunda remediación completa

REV-1/3/4/5/6/8/9/10/12 quedan corregidas. API HTTP y MCP están compuestos en el ejecutable; GitHub autentica y traduce create/show/decide desde el webhook, mapea actor a principal y conserva esa identidad en artifact, binding y audit. Baselines derivadas revalidan recursivamente toda aprobación previa. Diff/projection cierran dependencias y ordenan allOf; provenance es agregado; exporter coloca auditoría en la unidad mínima. Publisher usa marker durable por intento y commits por descriptor de parent con builds Linux/Darwin/Windows; store admite journals >1 MiB y rechaza root swaps. Focales, ×10, race UCRT64, vet, compile-only, Darwin, formato y diff pasan; coverage contractreview 85,0 % y workflow 85,6 %. La global solo reproduce el symlink Windows histórico. T1–T13 completas; se solicita nueva review sin modificar feature_list.json.

## 2026-10-20 — Feature 24, tercera remediación

Tercera review rechazada: REV-2/3/4/5/7/11/13 permanecen cerradas; se reabren T6, T8–T12 y T13 para corregir autenticación/contratos productivos, metadata OpenAPI local, filesystem descriptor-relative, recovery de publisher/store y binding/publicación GitHub.

## 2026-10-21 — Feature 24, tercera remediación completa

REV-1/6/8/9/10/12 quedan corregidas. Las superficies productivas autentican y
usan contratos cerrados; OpenAPI conserva metadata en la unidad mínima; publisher
y store operan respecto de descriptores, con intención previa al commit y recovery
de cada crash point. El store impone bounds y usa reservas/transiciones durables
sin locks durante I/O; el runtime cierra handles. GitHub enlaza review y comando a
la misma PR, atribuye al decisor y publica un resumen bot-owned idempotente.

Evidencia: focales afectados y repetición ×10, create/CAS/multiproceso/recovery
sin lock ×100, race UCRT64, vet, compile-only global, cross-build Windows/Linux/Darwin,
formato y diff pasan. Cobertura: contractreview 85,0 % y validationworkflow
85,6 %. La global conserva solo el symlink histórico de Windows sin privilegio.
T1–T13 quedan completas; feature sigue `in_progress` para revisión independiente.

## 2026-10-21 — Cuarta remediación iniciada

- Reabiertas T6, T9, T10, T12 y T13 por REV-1, REV-9, REV-10 y REV-14.
- Alcance: contrato cerrado por subcomando y vertical HTTP productiva; recuperación de crash antes de sync; idempotencia create con reserva idéntica; ownership y cierre de runtime GitHub.
- Se mantienen fuera de alcance cambios a `feature_list.json` y se preservan REV-2–REV-8 y REV-11–REV-13.

## 2026-10-21 — Feature 24, cuarta remediación completa

REV-1/9/10/14 quedan corregidas. La CLI rechaza flags cruzadas y ausentes; la vertical API atraviesa el dispatcher y listener loopback productivos con bearer por request, códigos, store/export durable, restart y shutdown. El publisher persiste intención antes de escribir, reanuda temps parciales, recupera fallos de sync y cubre marker temp/commit y el límite Darwin link/unlink. El store publica reservas completas atómicamente y dos procesos con principal/key/digest/artifact idénticos reciben el mismo aggregate completo. El runtime GitHub posee y cierra el review service en success, error y recomposición.

Evidencia: focal completo y repetición ×10 pasan; adversarios exactos multiproceso/concurrentes ×20; race UCRT64 afectado y contractreview ×5 pasan; vet, compile-only, cross-build Windows/Linux/Darwin, gofmt y diff pasan. Cobertura: contractreview 85,1 % y validationworkflow 85,6 %. La suite global conserva únicamente el fallo ambiental histórico de symlink Windows sin privilegio. T6/T9/T10/T12/T13 vuelven a completas; feature sigue `in_progress`, `feature_list.json` no se modifica y se solicita revisión independiente.

## 2026-10-21 — Quinta remediación iniciada

- Reabiertas T6, T9, T12 y T13 por REV-15 y REV-16.
- Alcance: cancelación propagada entre reserva/journal/checkpoint sin sleeps ni contextos de fondo, y convergencia concurrente de publish/export con un único intent/marker/target/receipt.
- REV-1–REV-14 se mantienen cerradas; `feature_list.json` no se modifica.

## 2026-10-21 — Feature 24, quinta remediación completa

REV-15 y REV-16 quedan corregidas. El store propaga cancelación por reserva,
journal y checkpoint y permite retry convergente de toda frontera durable. El
publisher coordina llamadas idénticas por una intención durable, limpia el temp
del perdedor por descriptor y espera/reconcilia el marker, rename y target del
ganador. El store observa la transición ganadora sin competir por journal o
checkpoint, de modo que dos exports concurrentes retornan el mismo receipt.

Evidencia: cancelaciones sincronizadas, convergencia publish/export ×50,
focales afectados ×10, race UCRT64 de `contractreview` ×5, vet, compile-only,
cross-build Windows/Linux/Darwin, formato y diff pasan. Cobertura:
`contractreview` 85,0 % y `validationworkflow` 85,6 %. La global conserva el
fallo ambiental de symlink Windows; un timeout transitorio ajeno en
`goinspector` pasa focal ×10. T6/T9/T12/T13 vuelven a completas; feature sigue
`in_progress` y `feature_list.json` no se modifica.

## 2026-10-21 — Sexta remediación iniciada

- Reabiertas T6, T9, T12 y T13 por los residuos REV-15/REV-16 y el nuevo REV-17.
- Alcance: cancelación preservada durante encode/write/sync; ownership durable de publicación sin takeover temporal; pruebas concurrentes acotadas sin barreras que presupongan una rama.
- REV-1–REV-14 permanecen cerradas y `feature_list.json` no se modifica.

## 2026-10-21 — Feature 24, sexta remediación completa

REV-15/16/17 quedan corregidas. Store conserva cancelación y `errors.Is` en
encode, write y sync, detiene el commit posterior y recupera la reserva en el
retry. Publisher reemplaza el takeover temporal por ownership durable de
`AttemptID` mediante locks de proceso descriptor-relative: un follower jamás
trunca el temp de un writer vivo y solo recupera tras release o muerte del
owner. Las pruebas concurrentes usan estados reales, contexto y timeout, sin
barreras dependientes de que ambos callers recorran la misma rama.

Evidencia: conjunto exacto ×50, Service/export ×50, race focal ×5, focal
completa ×10, race completo, vet, compile-only y cross-build Windows/Linux/
Darwin pasan. Cobertura `contractreview` 85,0 % y `validationworkflow` 85,6 %.
La global conserva únicamente el symlink Windows sin privilegio. T6/T9/T12/T13
vuelven a completas; feature sigue `in_progress` y `feature_list.json` no cambia.

## 2026-10-21 — Séptima remediación iniciada

- Reabiertas T9, T12 y T13 por REV-18.
- Alcance: revalidar cancelación tras adquirir ownership y antes de cleanup, rename o commit durante recovery; cubrir marker+temp preparado y la carrera release/cancel.
- REV-1–REV-17 permanecen cerradas; `feature_list.json` no se modifica.

## 2026-10-21 — Séptima revisión independiente

- Veredicto `rejected`: REV-15, REV-16 y REV-17 quedan cerrados sin regresión.
- Se abre REV-18: si cancelación y adquisición de ownership coinciden, el
  follower puede entrar en `recoverPrepared` y publicar un target con el
  contexto ya cancelado.
- Se reabren T9, T12 y T13 hasta añadir la revalidación tras adquirir ownership,
  antes de los efectos irreversibles y la prueba determinista de cancelación,
  ausencia de target y retry convergente.

## 2026-10-21 — Octava revisión independiente

- Veredicto `rejected`: REV-18 queda cerrado mediante estrés y race.
- Se abre REV-19: una cancelación en `directory_synced` permite que el flujo
  normal, y una salida de colisión en recuperación, devuelvan éxito sin
  revalidar el contexto al confirmar el resultado.
- Se reabren T9, T12 y T13 hasta cubrir ambas salidas con cancelación tipada,
  sin falso éxito y con retry idempotente.

## 2026-10-21 — Feature 24, séptima remediación completa

REV-18 queda corregida. El publisher revalida la cancelación justo después de
adquirir ownership, incluido el retorno del checkpoint `ownership_acquired`, y
en recovery antes de cleanup, espera/adopción, rename y confirmación. La prueba
con marker+temp preparado demuestra `CategoryCancelled`, preserva
`errors.Is(context.Canceled)`, no crea target y permite que un retry del mismo
intento termine. La carrera simultánea release/cancel es acotada y no usa sleeps.

Evidencia: pruebas REV-18 ×20, conjunto concurrente previo ×50, race focal ×5,
recovery/crash ×10, focal afectada ×10, race afectado, vet, compile-only y
cross-build Windows/Linux/Darwin pasan. Cobertura: `contractreview` 85,1 % y
`validationworkflow` 85,6 %. `gofmt` y diff-check pasan. La global conserva solo
el fallo ambiental conocido del symlink Windows sin privilegio. T9/T12/T13
vuelven a completas; feature sigue `in_progress` y `feature_list.json` no cambia.

## 2026-10-21 — Octava remediación iniciada

- Reabiertas T9, T12 y T13 por REV-19.
- Alcance: revalidar cancelación tras el sync/verify durable y antes de cada
  confirmación exitosa del publisher, incluido collision recovery.
- REV-1–REV-18 permanecen cerradas; `feature_list.json` no se modifica.

## 2026-10-21 — Feature 24, octava remediación completa

REV-19 queda corregida. El publisher comprueba cancelación después del sync y
verify finales y antes de toda confirmación exitosa. Esto incluye el flujo
normal, collision recovery tras cleanup+sync, recuperación por marker e
`Inspect`. Una cancelación posterior al efecto durable devuelve
`CategoryCancelled`, conserva `errors.Is(context.Canceled)` y deja que el retry
del mismo `AttemptID` reconcilie un único target sin duplicar salida.

Evidencia: escenarios REV-19 ×50 y race ×20; conjunto REV-15–REV-19 ×50 y race
×5; recovery/crash ×10; focal afectada ×10 y race completo pasan. Vet,
compile-only, cross-build Windows/Linux/Darwin, gofmt y diff-check pasan.
Cobertura: `contractreview` 85,2 % y `validationworkflow` 85,6 %. La global solo
reproduce el symlink Windows histórico sin privilegio. T9/T12/T13 vuelven a
completas; feature sigue `in_progress` y `feature_list.json` no cambia.

## 2026-10-21 — Novena revisión y cierre de Feature 24

- Review independiente `approved`: REV-19 cerrado y REV-1–REV-18 sin regresión.
- R1–R13 y T1–T13 quedan completos; cobertura final de `contractreview` 85,2 %
  y `validationworkflow` 85,6 %.
- El smoke final de `contractreview`, `contractcatalog`, `validationworkflow`,
  `githubpr` y `cmd/featureproof` pasa.
- El leader cambia `generated-contract-review` de `in_progress` a `done`.

## 2026-10-21 — Feature 25 `test-identities`, especificación iniciada

- Rama creada: `feature-25-test-identities`, basada en la feature 24 completada.
- Estado inicial comprobado: `pending`, `sdd: true`, sin otra feature
  `in_progress` y con criterios de aceptación suficientes.
- Se inicia `spec_author`; no se modificará código ni tests antes de la
  aprobación humana de `requirements.md`, `design.md` y `tasks.md`.

## 2026-10-21 — Feature 25, spec preparada

- Creados `specs/test-identities/{requirements,design,tasks}.md` sin modificar
  código productivo ni tests.
- La spec define R1–R11 y T1–T13, con selección explícita de identidad,
  resolución e inyección tardía, redacción cerrada, fallos operacionales,
  revalidación y paridad CLI/API/MCP/GitHub.
- El diseño conserva la convención de comentarios sistémicos para entidades,
  funciones, métodos y helpers productivos o de test.
- El leader cambia la feature de `pending` a `spec_ready`; queda detenida hasta
  aprobación humana explícita.

## 2026-10-22 — Feature 25, spec aprobada

- El usuario aprueba explícitamente la spec al solicitar continuar con la
  implementación.
- Se registra la aprobación en `requirements.md` y el leader cambia la feature
  de `spec_ready` a `in_progress`.
- Se autoriza al implementer a ejecutar T1–T13 en orden; el estado `done`
  seguirá reservado al leader tras review independiente aprobada.

## 2026-10-22 — Feature 25, primera revisión independiente

- Veredicto `rejected`; se reabren las tareas afectadas por REV-1–REV-8.
- Bloqueos: binding omitido con inputs referenciados, policy no reevaluada por
  retry, composición productiva sin resolvers, lease sin liberar en error,
  observer terminal no acotado y snapshot de identidades no sellado.
- Además debe unificarse la política efectiva de redacción antes de planning y
  corregirse el gate de formato/comentarios antes de volver a review.

## 2026-10-22 — Feature 25, segunda revisión independiente

- Veredicto `rejected`: REV-1/2/4/5/6/7/8 quedan cerrados; REV-3 sigue abierto.
- Se abren REV-9–REV-11: el retry reserva budget antes de policy, los rechazos
  pre-red carecen de evento terminal de identidad y composición exige
  incorrectamente una entrada por identidad en vez de por referencia única.
- Se reabren T10–T13 hasta demostrar paridad terminal y recovery/redelivery
  reales, corregir orden/contabilidad y completar auditoría segura.

## 2026-10-22 — Feature 25, tercera revisión independiente

- Veredicto `rejected`: REV-9–REV-11 quedan cerrados y REV-1/2/4–8 siguen
  cerrados; REV-3 permanece abierto por evidencia incompleta.
- Se abre REV-12: rotación real y journal append-only están probados por
  separado, pero falta una vertical que demuestre ambas propiedades juntas.
- Se reabren T9–T13 para medir llamadas reales a `Resolve`, comparar errores en
  las cuatro superficies, ejecutar `Service.Recover` y trazar la rotación sin
  persistir referencias ni canarios.

## 2026-10-22 — Feature 25, cuarta revisión independiente

- Veredicto `rejected`: REV-3 y REV-12 quedan cerrados; REV-1–REV-12 ya no
  conservan defectos abiertos en sus alcances originales.
- Se abren REV-13 y REV-14 en la frontera durable: el package reader debe
  verificar ID/digest/ordinal/categoría de eventos de identidad contra el plan
  sellado y faltan comentarios sistémicos en dos helpers modificados.
- Se reabren T9, T12 y T13 con alcance exclusivo en validación fail-closed de
  packages manipulados, comentarios y gates.

## 2026-10-22 — Feature 25, quinta revisión independiente

- Veredicto `rejected`: REV-14 queda cerrado y REV-13 se sustituye por REV-15.
- El reader aún acepta terminal ausente o extra contiguo y campos aislados de
  identidad en eventos incompatibles, incluso con package rehasheado.
- Se mantienen reabiertas T9, T12 y T13 hasta modelar el tuple completo por
  evento/step y exigir cardinalidad exacta entre intentos y terminales.

## 2026-10-22 — Feature 25, sexta revisión independiente

- Veredicto `rejected`: REV-15 queda cerrado para tuple y cardinalidad.
- Se abre REV-16: el reader necesita una fase por step+attempt para impedir
  terminal antes de ejecución, éxito sin ejecución, eventos posteriores al
  terminal y contradicción entre finished y outcome terminal.
- T9, T12 y T13 permanecen reabiertas con alcance exclusivo en la máquina de
  estados durable y sus adversarios rehasheados.

## 2026-10-22 — Feature 25, séptima revisión independiente

- Veredicto `rejected`: REV-16 queda cerrado.
- Se abre REV-17: una cancelación tras `budget_reserved` o el marker de retry
  puede impedir `execution_started` y dejar el intento sin terminal; el reader
  tampoco admite todavía ese terminal cancelado legítimo.
- T9, T12 y T13 siguen reabiertas para corregir productor y reader en ambas
  fronteras, con cero tool/resolver/red posterior.

## 2026-10-22 — Feature 25, octava revisión independiente

- Veredicto `rejected`: REV-17 queda cerrado.
- Se abre REV-18: si los appends de policy, budget o retry devuelven
  cancel/deadline antes de confirmar, el executor retorna sin reconciliar el
  terminal del intento afectado.
- T9, T12 y T13 siguen reabiertas hasta aplicar cierre acotado según el último
  prefijo durable y probar cancel/deadline en intento inicial y retry.

## 2026-10-22 — Feature 25, novena revisión y cierre

- Review independiente `approved`: REV-18 queda cerrado y REV-1–REV-17
  permanecen cerrados sin regresión.
- R1–R11 y T1–T13 quedan completos; los appends pre-I/O reconciliaron
  cancel/deadline con terminal único, prefijo durable exacto y cero efectos
  posteriores.
- El smoke final de los paquetes afectados y la matriz durable de
  `localvalidation` pasan.
- El leader cambia `test-identities` de `in_progress` a `done`.

## 2026-10-22 — Feature 25, implementación T1–T2

- T1 completa: el dominio admite specification v2 con identidades, mecanismos cerrados, referencias opacas y permisos HTTP exactos; v1 permanece sin identidades.
- T2 completa: el loader despacha v1/v2 antes de interpretar campos, rechaza secretos inline, duplicados y tipos ambiguos sin mostrar valores.
- Evidencia: `go test ./internal/specification/... -count=1` pasa.

## 2026-10-22 — Feature 25, implementación T3–T7

- T3–T4 completas: catálogo inmutable, matching estructural y descriptores
  mínimos alimentan agent planning; la selección explícita queda en provenance
  y no admite identidades inventadas ni referencias privadas.
- T5–T7 completas: plan/manifest v2, executor guard, leases y transporte tardío
  revalidan por intento, inyectan sobre clones y liberan material una vez.
- La integración HTTP impide resolver o abrir red ante binding inválido y
  revalida cada redirect contra la operación sellada antes del siguiente hop.
- Evidencia: suites focales de `testidentity`, `agentplanning`, `planning`,
  `planmanifest` y `tools/http` pasan, incluida concurrencia con canarios.

## 2026-10-22 — Feature 25, implementación T8–T12

- T8 completa: la redacción añade headers y JSON paths de autenticación de
  forma monotónica y falla cerrada ante valores no clasificables.
- T9–T10 completas: journal y proyección conservan solo ID/digest/categoría;
  composición exige un resolver exacto por referencia y vuelve a resolver en
  cada intento sin persistir leases.
- T11–T12 completas: CLI, API, MCP y GitHub rechazan campos secretos o de
  composición; la prueba vertical escanea todos los archivos durables y no
  encuentra canarios ni referencias opacas.
- Evidencia: pruebas focales repetidas, race y servidor HTTP real local pasan;
  `testidentity` alcanza 90,7 % de cobertura, specification 95,5 %, YAML 87,3 %
  y los demás paquetes afectados medidos permanecen por encima del 85 %.

## 2026-10-22 — Feature 25, implementación completa

- T1–T13 quedan completas con trazabilidad R1–R11 en
  `progress/test-identities/implementation.md`.
- Pasan gofmt, compile-only, vet, suites focales ×20, validation workflow ×20,
  localvalidation focal ×10, race focal y cross-build Windows/Linux/Darwin.
- La suite global conserva únicamente el fallo ambiental histórico al crear un
  symlink en Windows sin privilegio; todos los demás paquetes pasan.
- El diff no modifica `feature_list.json`, no marca la feature como `done` y no
  incorpora trabajo de feature 26. Se solicita review independiente.

## 2026-10-22 — Feature 25, primera remediación iniciada

- Review independiente rechazada con REV-1–REV-8.
- Se reabren T3–T13 para cerrar autorización dinámica fail-closed, policy por
  retry, composición productiva, ownership de leases, observación acotada,
  snapshots sellados, policy de redacción única y gates canónicos.
- T1–T2 permanecen completas: R1 no fue invalidada por la review.

## 2026-10-22 — Feature 25, primera remediación completa

- REV-1–REV-8 corregidas: manifests dinámicos fallan cerrados, policy e identidad
  se reevalúan por intento y la composición productiva se valida antes del
  modelo con resolución tardía.
- Lease+error/cancel libera exactamente una vez; observer terminal usa bound
  configurado y no deja trabajo detached.
- Identidades quedan deep-copied, canónicas y selladas en admission, approval,
  recovery y pre-I/O; una única redaction policy efectiva rige planning y
  ejecución.
- Paridad vertical CLI/API/MCP/GitHub verificada contra el mismo contrato de
  binding; restart y ausencia de resolver fallan cerrados sin material durable.
- T3–T13 vuelven a completas con focales ×20, race UCRT64, coverage >=85 % del
  alcance nuevo/modificado, vet, compile, cross-build, gofmt y diff-check.
- La única limitación global continúa siendo el symlink histórico de Windows
  sin privilegio. Se solicita nueva review independiente.

## 2026-10-22 — Feature 25, segunda remediación iniciada

- Segunda review rechazada con alcance cerrado REV-3 y REV-9–REV-11; se reabren T10–T13.
- Se preservan REV-1/2/4/5/6/7/8 y T1–T9; no se tocará `feature_list.json` ni feature 26.
- Trabajo pendiente: terminales reales en cuatro superficies y recovery/redelivery, reserva atómica de retry tras policy, eventos terminales de identidad pre-red y cobertura exacta de referencias únicas compartidas.

## 2026-10-22 — Feature 25, segunda remediación completa

- REV-3 queda cerrada con una vertical productiva terminal sobre API, CLI, MCP
  y GitHub: todas conservan identity/binding/outcome/código y exactamente una
  inferencia, resolución autenticada y llamada de red.
- Redelivery terminal no repite modelo, resolución ni red. Un workflow pendiente
  se recupera tras recrear `Service` y composición y usa material resuelto de
  nuevo después del restart; composición ausente corta antes del modelo.
- REV-9 mueve la reserva atómica retry+tool-call después de revalidar identidad
  y policy. Allow→deny, drift y cancelación de policy no consumen retry/tool-call
  ni alcanzan resolver/red.
- REV-10 añade un terminal seguro por intento autenticado, incluso drift,
  policy, budget, credencial y cancelación inicial o de retry, con correlación
  completa y categorías cerradas sin texto externo.
- REV-11 valida el conjunto exacto de referencias únicas: comparte resolver
  cuando corresponde y rechaza mappings faltantes o sobrantes.
- La vertical detectó y corrigió tres límites productivos: conversión de
  literales al validador HTTP, normalización JSON previa al sanitizer de
  aprobación y canonicalización de bindings con referencia de aprobación.
- T10–T13 vuelven a completas. Pasan focales repetidos, race UCRT64, gofmt,
  compile-only, vet, cross-build Linux/Darwin, cobertura del alcance y
  `git diff --check`. La suite global mantiene únicamente el fallo histórico
  de symlink sin privilegio en Windows.
### 2026-10-22 — Feature 25 tercera remediación en curso

- Reabiertas T9–T13 para cerrar REV-3 y REV-12.
- Trabajo limitado a instrumentación del resolver productivo, paridad de fallo de identidad, recovery explícito y rotación histórica unida al journal.

## 2026-10-22 — Feature 25, tercera remediación completa

- REV-3 queda cerrado con un puerto productivo de factory y un spy que cuenta
  llamadas reales a `CredentialResolver.Resolve`; éxito y fallo de credencial
  recorren API, CLI, MCP y GitHub con binding, outcome, categoría, código propio
  y contadores exactos.
- Redelivery conserva una sola resolución/red; el restart recrea composición y
  `Service`, invoca `Service.Recover` explícitamente sin efectos y completa la
  aprobación con resolver y material frescos.
- REV-12 queda cerrado con dos runs productivos y credenciales rotadas: cada run
  resuelve una vez, conserva identity ID/digest, escribe un terminal ordinal 1
  `success`, no altera el journal previo y excluye canarios y credential ref de
  journal, evidence, report, artifact y filesystem durable.
- T9–T13 vuelven a completas. Pasan focales ×20, race UCRT64, compile-only,
  vet, cross-build Linux/Darwin, gofmt y diff-check. La suite global conserva
  únicamente el fallo histórico de symlink sin privilegio en Windows.

## 2026-10-22 — Feature 25, cuarta remediación en curso

- Reabiertas T9, T12 y T13 para cerrar REV-13/REV-14 en la frontera durable.
- Se validarán los hechos terminales contra el step sellado, secuencia ordinal
  y vocabulario cerrado incluso tras rehash completo de un package manipulado.

## 2026-10-22 — Feature 25, cuarta remediación completa

- REV-13 cerrado: la topología durable conserva identity ID y operation-binding
  digest por step; el reader exige presencia conjunta, coincidencia exacta,
  ordinal positivo/contiguo, categoría allowlisted y tipos de evento compatibles.
- Doce adversarios regeneran inventario y manifest tras alterar individualmente
  ID, digest, attempt, categoría o tipo de evento; todos fallan por integridad y
  el package productivo normal continúa verificándose offline.
- REV-14 cerrado con comentarios sistémicos en `validatePlanTopology`,
  `validatePackageEvents` y todos los símbolos añadidos/modificados.
- T9, T12 y T13 vuelven a completas; cobertura directa: topology 89,3 %, events
  86,0 % y validadores de vocabulario 100 %.

## 2026-10-22 — Feature 25, quinta remediación en curso

- Reabiertas T9, T12 y T13 para cerrar REV-15.
- El reader modelará contratos explícitos por tipo/step y correlacionará cada
  intento observado con exactamente un terminal, incluidos campos aislados.

## 2026-10-22 — Feature 25, quinta remediación completa

- REV-15 cerrado: cada tipo durable declara si identity ID, digest, attempt y
  outcome son obligatorios o prohibidos según el step sea autenticado.
- El reader correlaciona intentos observados con terminales y exige al final
  exactamente uno por intento; rechaza terminal ausente, extra contiguo y
  cualquiera de los cuatro campos aislado en eventos ajenos.
- Los seis adversarios exactos de review regeneran inventario/manifest y fallan
  por integridad; productor normal, retry, rechazo y rotación se conservan.
- T9, T12 y T13 vuelven a completas. La frontera modificada alcanza 88,6 % en
  `validatePackageEvents` y 100 % en vocabularios auxiliares.

## 2026-10-22 — Feature 25, sexta remediación en curso

- Reabiertas T9, T12 y T13 para cerrar REV-16.
- Se añadirá una máquina durable por step+attempt que vincule fases únicas,
  categoría de execution-finished y terminal, sin aceptar hechos posteriores.

## 2026-10-22 — Feature 25, sexta remediación completa

- REV-16 cerrado: el package reader mantiene una fase durable por step+attempt,
  exige policy/budget/start/finish en orden y hace concordar finished con el
  terminal de ejecución.
- Cancelación y rechazos pre-I/O conservan rutas cerradas explícitas; retry solo
  abre tras un predecesor transient y ningún hecho compatible sigue al terminal.
- Los tres adversarios exactos rehasheados y seis mutaciones adicionales de
  fase fallan por integridad; productor normal, retry, cancel y rechazos válidos
  permanecen aceptados.
- T9, T12 y T13 vuelven a completas. Pasan focales ×20, race UCRT64,
  compile-only, vet, regresiones, cross-build Linux/Darwin, gofmt y diff-check;
  `validatePackageEvents` alcanza 89,7 % y la máquina auxiliar 100 %.
- La suite global mantiene únicamente el fallo histórico de symlink de Windows
  por falta de privilegio; `feature_list.json` no fue modificado.

## 2026-10-22 — Feature 25, séptima remediación en curso

- Reabiertas T9, T12 y T13 para cerrar REV-17.
- El productor reconciliará cancelación tras budget/retry y append cancelado de
  execution-started con un terminal único bajo contexto de cierre acotado.
- El reader admitirá esos prefijos cancelados sin relajar orden, concordancia ni
  cierre de las demás rutas durables.

## 2026-10-22 — Feature 25, séptima remediación completa

- REV-17 cerrado: el executor revalida cancelación tras budget y retry marker,
  y reconcilia fallos cancel/deadline de execution-started antes de cualquier
  tool, resolver o red posterior.
- Cada intento recibe un único terminal cancelled con contexto de cierre
  acotado; un fallo de ese cierre conserva ambas causas con `errors.Join`.
- El reader acepta cancelación tras policy+budget y policy+budget+retry y sigue
  rechazando cualquier fase posterior al terminal.
- Tests deterministas cubren intento inicial, retry, append cancelado y
  secuencias bound completas; pasan repetidos y bajo race UCRT64.
- T9, T12 y T13 vuelven a completas. Compile-only, vet, cross-build, gofmt,
  diff-check y cobertura pasan; la suite global solo conserva el symlink
  histórico de Windows sin privilegio. `feature_list.json` permanece intacto.

## 2026-10-22 — Feature 25, octava remediación en curso

- Reabiertas T9, T12 y T13 para cerrar REV-18 de forma sistemática.
- Policy, budget, retry y execution-started compartirán reconciliación acotada
  de cancel/deadline según el último prefijo durable confirmado.
- Se probarán cancelación y deadline por append en intentos iniciales y retry,
  incluida la causalidad cuando también falla el terminal.

## 2026-10-22 — Feature 25, octava remediación completa

- REV-18 cerrado sistemáticamente: todos los appends pre-I/O de identidad usan
  una reconciliación común para cancel/deadline con timeout de cierre.
- El terminal cancelled refleja exactamente el último prefijo confirmado:
  vacío, policy o policy+budget; retry añade su marker solo si fue confirmado.
- Cancel y deadline se prueban en policy/budget iniciales y de retry, retry
  marker y execution-started, con un terminal, orden exacto y cero efectos
  tool/resolver/red posteriores; también se conserva el doble error de cierre.
- El reader acepta siete secuencias bound legítimas y sigue bloqueando fases
  posteriores. T9, T12 y T13 vuelven a completas.
- Focales ×30, race ×5, coverage, compile-only, vet, cross-build, gofmt y
  diff-check pasan. La suite global solo reproduce el symlink histórico de
  Windows sin privilegio; `feature_list.json` permanece intacto.

## 2026-10-22 — Feature 26 `fixture-api`, especificación iniciada

- Rama creada: `feature-26-fixture-api`, basada en la feature 25 completada.
- Estado comprobado: `pending`, `sdd: true`, sin otra feature `in_progress` y
  con criterios de aceptación suficientes.
- La feature depende de las identidades seguras ya implementadas; se inicia
  `spec_author` y no se tocará código ni tests antes de aprobación humana.

## 2026-10-22 — Feature 26, spec preparada

- Creados `specs/fixture-api/{requirements,design,tasks}.md` sin modificar
  código productivo ni tests.
- La spec define R1–R13 y T1–T14: contrato v3, bindings de contrato/identidad,
  grafo y captures, cleanup intent durable, orden inverso, scope y budgets
  propios, recovery, reporte separado y paridad de superficies.
- El diseño mantiene fuera de alcance los flujos funcionales de feature 27 y
  exige comentarios sistémicos en todo símbolo nuevo o modificado.
- El leader cambia `fixture-api` de `pending` a `spec_ready`; el workflow queda
  detenido hasta aprobación humana explícita.

## 2026-10-22 — Feature 26, spec aprobada

- El usuario aprueba explícitamente la spec al solicitar continuar con la
  implementación.
- Se registra la aprobación en `requirements.md` y el leader cambia
  `fixture-api` de `spec_ready` a `in_progress`.
- Se autoriza al implementer a ejecutar T1–T14; el cierre seguirá condicionado
  a trazabilidad completa, gates y review independiente.

## 2026-10-22 — Feature 26 fixture-api (implementación)

- Specification v3 implementada manteniendo dispatch estricto de v1/v2.
- Nuevo paquete `internal/fixture`: grafo inmutable, binding de contrato/identidad/idempotencia, resolución y extracción tipadas, intent durable, lifecycle secuencial y cleanup inverso finito.
- Persistencia CAS atómica y adapter compartido conectado a `validationworkflow`; la proyección externa contiene solo `FixtureSummary`/`CleanupReport` allowlisted.
- En curso: endurecimiento de recovery/ownership, matriz adversaria, cobertura y gates finales.

- T1–T14 completadas con trazabilidad R1–R13 en `progress/fixture-api/implementation.md`.
- Gates: vet/compile/focales repetidas/race/cross-build/diff-check correctos; coverage fixture 86,2 % y validationworkflow 85,6 %.
- Suite global verde salvo la limitación histórica de symlink en Windows sin privilegio; la regresión de diagnóstico de versión futura fue corregida y verificada focalmente.
- Entrega solicitada para review independiente; feature permanece `in_progress` y `feature_list.json` no fue modificado.

## 2026-10-22 — Feature 26, primera revisión rechazada

- El reviewer independiente detectó REV-1..REV-9; la feature permanece `in_progress`.
- Bloqueantes: falta composición productiva del lifecycle, recovery inseguro después de I/O, terminalización del workflow pese a cleanup no confirmado, sello/estado/capturas manipulables y ausencia de claim durable entre owners.
- También deben cerrarse terminales durables de cleanup, proyección de dependientes bloqueados, restricciones estrictas del contrato v3 y las matrices reales de crash, concurrencia y paridad de superficies.
- Se reabren T1, T4, T8–T14 para remediación y nueva revisión independiente; `feature_list.json` permanece intacto.

## 2026-10-22 — Feature 26 fixture-api (primera revisión rechazada)

- Reabiertas T1, T4 y T8–T14 por REV-1..REV-9.
- La revisión detectó ausencia de composición productiva, ventanas post-I/O no conservadoras, cleanup terminalizado prematuramente, sellos incompletos, ausencia de ownership por efecto, estados de bloqueo incompletos y validación v3 insuficiente.
- En curso: rediseño del estado durable/claims/recovery y cierre de matrices adversarias antes de volver a marcar tareas.

## 2026-10-22 — Feature 26, segunda revisión rechazada

- La segunda revisión confirma cerrados REV-1, REV-2, REV-3 y REV-7; REV-4, REV-6 y REV-8 quedan parciales, y REV-5/REV-9 siguen abiertos.
- Se registran REV-10..REV-17: intercalaciones que duplican cleanup, idempotencia no acreditada por contrato, ausencia productiva de policy/budget, cleanup desligado de shutdown, estado durable forjable, captures sensibles aceptadas como `[REDACTED]`, audit de fixtures fuera del package verificable y `json.Number` no finito.
- Se reabren T1, T4–T14 para corregir garantías productivas y ampliar las matrices adversarias; la feature permanece `in_progress` y `feature_list.json` no cambia.

## 2026-10-22 — Feature 26, tercera revisión rechazada

- La tercera revisión cierra REV-8, REV-13, REV-15 y REV-17, pero mantiene parciales REV-4/5/6/10/16 y abiertos REV-9/11/12/14.
- REV-18..REV-24 exigen key por operación exacta, policy sobre request resuelto, budget durable por run/retry, lease válido durante I/O, historia durable no resellable, verificación semántica offline, audit recuperable de captures/eventos y limpieza de temporales/comentarios.
- Se reabren T4–T14; la feature continúa `in_progress` hasta cerrar las intercalaciones y pruebas omitidas.

## 2026-10-22 — Feature 26, cuarta revisión rechazada

- La cuarta revisión valida headers por operación, claims/leases, autenticación del state, outbox/captures, paridad y alcance, pero detecta REV-25..REV-27.
- Debe hacerse exacto y durable el budget por intento/retry y restart, cerrar todas las invariantes semánticas del package re-sellado y recibir cada límite desde configuración confiable obligatoria.
- Se reabren T9–T14; la feature permanece `in_progress` hasta nueva revisión independiente.

## 2026-10-22 — Feature 26, quinta revisión rechazada

- REV-25 y REV-27 quedan cerrados; REV-26 continúa abierto y se concreta en REV-29.
- REV-28 exige cerrar durablemente el intent ante denial de budget anterior a I/O; REV-29 exige validar toda la máquina de create y una única sección Markdown exacta; REV-30 exige coverage reproducible >=85 % sobre todo el código productivo añadido/modificado.
- Se reabren T8 y T10–T14; la feature continúa `in_progress`.

## 2026-10-22 — Feature 26, sexta revisión rechazada

- REV-28 queda cerrado y no se reproducen los timeouts bajo carga.
- REV-31 exige autenticar offline el audit completo con una autoridad no regenerable por quien re-sella el package; forma y hashes internos no bastan.
- REV-32 detecta 16 statements Unix omitidos por el gate: el coverage conservador real es 84,9792 %, por debajo del 85 %.
- Se reabren T11, T12 y T14; la feature continúa `in_progress`.

## 2026-10-22 — Feature 26, séptima revisión rechazada

- REV-33 demuestra que el reader posee la clave privada y permite sustituir el trust anchor completo; writer y verifier deben recibir capacidades separadas desde configuración confiable.
- REV-34 corrige el coverage conservador a 1666/1959 (85,0434 %), pero exige conteo simétrico y reproducible entre Windows y Unix.
- Se mantienen reabiertas T11, T12 y T14; la feature continúa `in_progress`.

## 2026-10-22 — Feature 26, octava revisión rechazada

- REV-34 queda cerrado con coverage multiplataforma reproducible 1695/1993 (85,0477 %).
- REV-35 exige pin público independiente, rotación autenticada, coherencia seed/private/public/key ID, carga endurecida y separación del reader respecto al signer.
- REV-36 exige retirar `scripts/__pycache__/` y evitar su regeneración; T11, T12 y T14 siguen abiertas.

## 2026-10-23 — Feature 26, novena revisión rechazada

- REV-35 y REV-36 quedan cerrados; cobertura, race global, suite global y cross-build pasan.
- REV-37 detecta que Windows valida el owner SID pero no rechaza DACL hostiles: la private key puede ser legible y el pin modificable por `Everyone`.
- T11, T12 y T14 permanecen abiertas hasta endurecer y validar ACL de key/pin en create y reload.

## 2026-10-23 — Feature 26 `fixture-api`, cierre aprobado

- REV-37 queda cerrado con DACL Windows protegida y exacta para private key y pin; la matriz hostil real, reload, links y race pasan.
- La décima revisión independiente aprueba R1–R13 y cierra REV-1..REV-37.
- T1–T14 están completas; cobertura statement-weighted multiplataforma 1812/2131 (85,0305 %), suite global ×2, race global, vet, compile-only y cross-build pasan.
- El smoke final del leader pasa en fixture, localvalidation, validationworkflow, specification/YAML y cmd/featureproof.
- El leader cambia feature 26 de `in_progress` a `done`; feature 27 permanece `pending`.

## 2026-10-23 — Feature 27 `functional-flow-validation`, especificación iniciada

- Rama creada: `feature-27-functional-flow-validation`, basada en feature 26 completada.
- Preflight correcto: feature 27 está `pending`, usa SDD, tiene criterios de aceptación suficientes y no existe otra feature `in_progress`.
- La feature depende del lifecycle de fixtures, identidades, contratos, policy y budgets ya disponibles en feature 26.
- Se inicia `spec_author`; no se modificará código productivo ni tests antes de aprobación humana explícita.

## 2026-10-23 — Feature 27, spec preparada

- Creados `specs/functional-flow-validation/{requirements,design,tasks}.md` sin modificar código productivo ni tests.
- La spec define R1–R15 y T1–T16: contrato `FunctionalFlow/v1`, bindings exactos, referencias tipadas, observaciones pre/post, ledger determinista, replanning monotónico, recovery, cleanup separado, package reconstruible y paridad de superficies.
- No quedan decisiones abiertas ni excepciones arquitectónicas; la implementación de features 28/29 y nuevas herramientas permanece fuera de alcance.
- El leader cambia feature 27 de `pending` a `spec_ready`; el workflow queda detenido hasta aprobación humana explícita.

## 2026-10-23 — Feature 27, spec aprobada

- El usuario aprueba explícitamente la spec al solicitar continuar con la implementación.
- Se registra `approved_by: user` y `approved_at: 2026-10-23T09:21:17+02:00` en requirements.
- El leader cambia feature 27 de `spec_ready` a `in_progress` y autoriza T1–T16.
- El cierre seguirá condicionado a trazabilidad R1–R15, gates completos y review independiente.

## 2026-10-22 — fixture-api remediation checkpoint 2

- REV-1: el composition root productivo crea `fixture.WorkflowAdapter` con lifecycle/store/HTTP/identidad/evidence/journal/scopes reales y lo inyecta en el mismo `validationworkflow.Service`; prueba vertical v3 completa API/CLI/MCP/GitHub verde.
- REV-2..REV-8: máquina conservadora, cleanup pending recuperable, sello/state/capture autenticados, claim/lease CAS, terminales durables, bloqueos cerrados y JSON v3 estricto implementados con pruebas focales.
- REV-9: añadidas carreras de dos owners sin sleeps, lease handoff, crash post-I/O, fallo de persistencia del report, redelivery y rotación de credenciales en composición real. Pendientes auditoría de canarios/gates/coverage y actualizar informe final.
- Verificación estable: compile-only global; suites focales `internal/fixture`, `internal/specification/...`, `internal/validationworkflow`; vertical productiva `TestProductFixtureLifecycleParityAcrossAllSurfaces` y rotación productiva.

## 2026-10-22 — fixture-api remediación completada

- REV-1..REV-9 cerradas con composición productiva, máquina durable conservadora, recovery/ownership y matrices T14 verificadas.
- `go test ./...`, vet, compile-only, focales repetidas, race, coverage >=85 %, cross-build y diff-check pasan; symlink se omite únicamente en Windows sin privilegio del SO.
- T1–T14 marcadas completas con trazabilidad reproducible en `progress/fixture-api/implementation.md`.
- Se solicita nueva revisión independiente; no se modificó `feature_list.json` ni se implementó feature 27.

## 2026-10-22 — fixture-api segunda remediación en curso

- Reabiertas T1 y T4–T14 por REV-10..REV-17 y cierres parciales REV-4/5/6/8/9.
- En curso: token único/lease de cleanup, authority contractual de idempotencia, policy+budget productivos, scope de shutdown, snapshot durable autenticado, captures sensibles, package auditado y json.Number finito.
- Las tareas permanecerán abiertas hasta que sus pruebas adversarias y gates correspondientes pasen; feature_list.json no se modifica.

## 2026-10-22 — fixture-api segunda remediación completada

- Cerradas REV-10..REV-17 y los cierres parciales REV-4/5/6/8/9: claim por invocación, idempotencia contractual, policy/budget productivos, shutdown scope, snapshot sellado, captures sensibles y audit de fixtures offline-verificable.
- Matrices focales, suite global, vet, compile-only, race con UCRT64, coverage fixture 85,0 %, validationworkflow 85,4 %, cross-build Linux/Darwin y diff-check correctos.
- T1 y T4–T14 vuelven a completas con evidencia actualizada; no se modificó feature_list.json.

## 2026-10-22 — fixture-api tercera remediación en curso

- Reabiertas T4–T14 por REV-18..REV-24.
- En curso: idempotency por operación, request completo antes de policy, budget durable ordinal, lease vigente durante I/O, cadena de estado estricta, package/report auditado y captures enlazadas con journal.
- Las tareas permanecerán abiertas hasta completar matrices adversarias y gates; feature_list.json no se modifica.

## 2026-10-22 — fixture-api tercera remediación completada

- Cerradas REV-18..REV-24: header ligado a operación exacta, policy sobre request resuelto, budget aislado/durable por run y ordinal, exclusión durante I/O, MAC consumer-owned, máquina/audit cerrados, reports resealados y outbox recuperable de captures/eventos.
- Matrices nuevas cubren POST con key/DELETE sin key, tres runs, request final, retry budget, same/different owner con expiry activo, snapshot resellado falso, package doblemente resellado, metadata de capture y recovery del journal.
- Suite global, vet, compile-only, race UCRT64, coverage fixture 85,1 % / validationworkflow 85,4 %, cross-build Linux/Darwin y diff-check pasan; temporales retirados y feature_list.json intacto.
- T4–T14 vuelven a completas y se solicita nueva revisión independiente.

## 2026-10-22 — fixture-api cuarta remediación en curso

- Reabiertas T9–T14 por REV-25..REV-27.
- En curso: presupuesto durable exacto por ordinal/restart, invariantes bidireccionales summary/audit/package y límites trusted explícitos fail-closed.
- feature_list.json permanece intacto.

## 2026-10-22 — fixture-api cuarta remediación completada

- Cerradas REV-25..REV-27: reserva durable exacta dentro de cada intento/retry, reconstrucción de budget tras restart, summary/package bidireccional y límites trusted explícitos fail-closed.
- Las matrices cubren `MaxCalls=1` con transitorio, restart entre ordinals, ownership concurrente, cinco dimensiones rehasheadas en todas las copias y cada dimensión de configuración productiva.
- Suite global, vet, compile-only, focales repetidas, race UCRT64, coverage fixture 85,3 % / validationworkflow 85,4 %, cross-build Linux/Darwin y diff-check pasan; no hay temporales y feature_list.json sigue intacto.
- T9–T14 vuelven a completas y se solicita revisión independiente.

## 2026-10-22 — fixture-api quinta remediación en curso

- Reabiertas T8 y T10–T14 por REV-28..REV-30.
- En curso: cierre durable de budget pre-I/O, máquina pública completa de create/report Markdown exacto y cobertura statement-weighted reproducible de todo el diff productivo desde `1daa391`.
- `feature_list.json` permanece intacto.

## 2026-10-22 — fixture-api quinta remediación completada

- Cerradas REV-28..REV-30: cierre durable pre-I/O ante budget, conservación conservadora tras I/O, máquina create/report estricta y cobertura ponderada reproducible del diff.
- Las matrices adversarias cubren denegación inicial/retry/save, ordinal 99, categoría/evidence canario, re-sell concordante y Markdown duplicado.
- Vet, compile-only, focales y coverage 1635/1908 (85,7 %) pasan; no hay temporales y `feature_list.json` sigue intacto.
- T8 y T10–T14 vuelven a completas y se solicita revisión independiente.

## 2026-10-22 — fixture-api sexta remediación completada

- T11/T12/T14 se reabrieron para REV-31/REV-32 y vuelven a completas tras añadir autoridad Ed25519 consumer-owned offline y cobertura multiplataforma fail-closed.
- Probes SHA-256 válidas de fixture set/evidence/capture/cleanup fallan tras re-sellado; cobertura total del diff 1666/1958 = 85,0868 %.
- `feature_list.json` permanece intacto.

## 2026-10-22 — fixture-api séptima remediación completada

- T11/T12/T14 se reabrieron para REV-33/34 y vuelven a completas.
- Writer privado y reader público están separados; firma domain-separated ligada a package/run/binding/audit y matriz de ausencia/sustitución/rotación/sidecar falla cerrado.
- Coverage simétrica Windows/Unix pasa con 1695/1993 = 85,0477 %; vet, compile-only, race focal y cross-build pasan; feature_list intacto.

## 2026-10-23 — fixture-api octava remediación en curso

- Reabiertas T11, T12 y T14 por REV-35/REV-36.
- En curso: pin público independiente de la private key, coherencia Ed25519 completa, key ID criptográficamente ligado, persistencia endurecida y separación estricta de capacidades writer/reader.
- Retirado `scripts/__pycache__/`; los gates Python se ejecutan con `PYTHONDONTWRITEBYTECODE=1`. `feature_list.json` permanece intacto.

## 2026-10-23 — fixture-api octava remediación completada

- REV-35 cerrado con pin público independiente, keypair/key ID coherentes, persistencia endurecida, mensaje firmado ligado al key ID y capacidades writer/reader separadas.
- REV-36 cerrado: `__pycache__` retirado y gates Python sin bytecode dentro del repositorio.
- Suite global, vet, compile-only, afectados repetidos, race UCRT64, cross-compile Linux/Darwin, coverage 1769/2080 (85,0481 %) y diff-check pasan; T11/T12/T14 vuelven a completas y `feature_list.json` sigue intacto.

## 2026-10-23 — fixture-api novena remediación en curso

- Reabiertas T11, T12 y T14 por REV-37.
- En curso: DACL Windows explícita, protegida y validada en cada reload para private key y public pin, con owner/SYSTEM/Administrators como únicos principals autorizados.
- Se añadirán ataques reales de ACL contra lectura de la key y escritura/control del pin; `feature_list.json` permanece intacto.

## 2026-10-23 — fixture-api novena remediación completada

- REV-37 cerrado con DACL Windows protegida y allowlist exacta owner/LocalSystem/Administrators en private key y public pin, validada de nuevo en cada reload.
- La matriz real rechaza Everyone/Users/Authenticated Users con lectura, escritura, delete, FullControl o cambio de owner/DACL; reload válido, links y carrera permanecen verdes.
- Suite global ×2, vet, compile-only, afectados ×3, race UCRT64, cross-compile Linux/Darwin, coverage 1812/2131 (85,0305 %) y diff-check pasan; T11/T12/T14 vuelven a completas y `feature_list.json` sigue intacto.
## 2026-10-23 — Feature 27 implementation

- Implementer started T1–T16 from approved commit `b81cee0` on `feature-27-functional-flow-validation`.
- Scope is limited to `FunctionalFlow/v1`, its deterministic runtime, workflow bridge, safe projections, tests, and delivery evidence.
- Initial verification confirmed feature 27 is `in_progress` and the worktree was clean.
- T1–T13 implementation checkpoint: canonical strict model, generated/provided adapters, immutable graph, exact bindings, typed private resolution, sequential executor, action intents, deterministic comparisons/ledger, bounded revisions, authenticated cross-process store, lifecycle bridge, and verified package are implemented.
- Hermetic focal suites pass; statement coverage for the new `internal/functionalflow` package reached 86.3% before final audit.
- Remaining delivery work: surface/schema parity audit, systemic-comment audit, all verification gates, traceability report, and task check-off.
- T14–T16 completed: the shared `validationworkflow.Service` now composes the flow runtime; CLI/API/MCP/GitHub project the same verified package and remote selection rejects inline authority.
- Final gates pass after the vertical-integration refactor: compile-only and vet global, focal ×3, global suite, focal/global race with UCRT64, Linux/Darwin cross-build, comment/diff audits, and 85.7% statement-weighted coverage across affected packages.
- The shared service owns `prepare -> functional flow -> package verification -> cleanup`; cleanup recovery is independently retryable and does not replay the functional flow. CLI/API/MCP/GitHub parity uses one verified-package projector.
- T1–T16 are checked and the delivery evidence is in `progress/functional-flow-validation/implementation.md`; implementation is ready for independent review.

## 2026-10-23 — Feature 27, primera revisión rechazada

- El reviewer registra REV-1..REV-10; la feature permanece `in_progress`.
- Bloqueantes: comparaciones/fallos funcionales no gobiernan el veredicto, recovery duplica actions inciertas, fixtures se preparan antes del FlowArtifact, no hay composición productiva real, package sin evidence autenticada y publicación anterior al cleanup, y replans pueden reescribir assertions previas.
- También deben cerrarse el enlace artifact↔flow y la semántica global de required evidence.
- Se reabren T1–T16 para remediación y nueva revisión independiente; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, segunda revisión rechazada

- REV-1, REV-2, REV-4 y REV-7–REV-10 quedan cerrados; REV-3/5/6 siguen parciales.
- REV-11..REV-16 exigen ligar aprobación al FlowArtifact exacto, eliminar identidad implícita y drift del request, hashear el selector real, preservar IDs locales históricos, empaquetar el journal durable real y cubrir retries completos con lease.
- Se reabren T1–T6 y T8–T16; la feature continúa `in_progress` y `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, tercera revisión rechazada

- REV-11–REV-15 y la composición vertical quedan cerrados; solo permanecen REV-17 y REV-18.
- Los terminales reject/cancel/auto-denial anteriores a prepare deben registrar estado causal explícito y producir package verificable.
- La claim debe cubrir o renovarse durante evidence, journal, action progress, assertions y terminal, con carrera same/different owner después del expiry.
- Se reabren T11–T13, T15 y T16; la feature continúa `in_progress`.

## 2026-10-23 — Feature 27, cuarta revisión rechazada

- REV-17 queda cerrado y REV-18 funciona dentro del executor, pero REV-19 bloquea recovery productivo.
- `validationworkflow.Service` interpreta una interrupción post-tool recuperable como primary terminal, inicia cleanup y fabrica un package imposible sin reanudar checkpoints.
- Se reabren T11–T13, T15 y T16 para introducir flow pending durable y recovery vertical antes de cleanup/package.

## 2026-10-23 — Feature 27, quinta revisión rechazada

- REV-19 queda parcial; REV-20 identifica la ventana después de completar Executor y antes de persistir `FunctionalFlowPrimaryDurable`.
- Recovery no repite tools, pero reemite `recovery_resumed`/`step_terminal` para pasos terminales y vuelve imposible el package.
- Se mantienen abiertas T11–T13, T15 y T16 hasta promover el primary desde journal/estado completo sin reemitir terminales.

## 2026-10-23 — Feature 27, sexta revisión rechazada

- REV-20 queda cerrado; REV-21 detecta causalidad más débil en `RecoverCompleted` que en `VerifyPackage`.
- Una cadena/MAC válidas con `evidence_confirmed` divergente puede promover primary y comenzar cleanup antes de que Finalize rechace el package.
- T11–T13, T15 y T16 siguen abiertas hasta compartir la misma validación causal estricta antes de `Stage`.

## 2026-10-23 — Feature 27 `functional-flow-validation`, cierre aprobado

- REV-21 queda cerrado al compartir `BuildPackage/VerifyPackage` entre promoción de recovery y publicación final.
- La séptima revisión independiente aprueba R1–R15, T1–T16 y cierra REV-1..REV-21.
- Pasan suite global, race global serializada UCRT64, vet, compile-only, focales repetidos y cross-build Linux/Darwin; cobertura del diff: 2674/3137 (85,2407 %).
- El smoke final del leader pasa en functionalflow, validationworkflow, fixture y cmd/featureproof.
- El leader cambia feature 27 de `in_progress` a `done`; feature 28 permanece `pending`.

## 2026-10-23 — Feature 28 `validated-project-memory`, especificación iniciada

- Rama creada: `feature-28-validated-project-memory`, basada en feature 27 completada.
- Preflight correcto: feature 28 está `pending`, usa SDD, tiene criterios suficientes y no existe otra feature `in_progress` o `spec_ready`.
- La feature depende de artifacts, evidence, revisions, ownership y packages verificados disponibles hasta feature 27.
- Se inicia `spec_author`; no se modificará código productivo ni tests antes de aprobación humana explícita.

## 2026-10-23 — Feature 28, spec preparada

- Creados `specs/validated-project-memory/{requirements,design,tasks}.md` sin modificar código productivo ni tests.
- La spec define R1–R15 y T1–T16: contrato `ProjectMemory/v1`, scopes y estados cerrados, autoridad/provenance/evidence, staleness, secret gate, journal/snapshots/índice verificables, CAS/recovery y confinamiento multiplataforma.
- No quedan decisiones abiertas; el ensamblado de contexto y ranking semántico de feature 29 quedan explícitamente fuera de alcance.
- El leader cambia feature 28 de `pending` a `spec_ready`; el workflow queda detenido hasta aprobación humana explícita.

## 2026-10-23 — Feature 28, spec aprobada

- El usuario aprueba explícitamente la spec al solicitar continuar con la implementación.
- Se registra `approved_by: user` y `approved_at: 2026-10-23T13:23:48+02:00` en requirements.
- El leader cambia feature 28 de `spec_ready` a `in_progress` y autoriza T1–T16.
- El cierre seguirá condicionado a trazabilidad R1–R15, gates completos y review independiente.

## 2026-10-23 — Feature 27 first review remediation

- T1–T16 reopened after reviewer rejection REV-1..REV-10.
- Remediation started with canonical required proof, deterministic verdict, uncertain-effect recovery, exact artifact binding, vertical admission, authenticated package and cleanup ownership.
- feature_list.json remains unchanged.

## 2026-10-23 — Feature 27, remediación REV-1..REV-10 completada

- T1–T16 vuelven a completas tras cerrar comparaciones y fallos funcionales en el veredicto, recovery conservador de effects, admisión anterior a fixtures, artifact↔flow, required evidence global y revisiones inmutables.
- La vertical productiva compartida ejecuta generated/provided mediante Service y mantiene paridad real CLI/API/MCP/GitHub; alias remoto, restart/redelivery, rotación de identidad y fallos se prueban sin red externa.
- Evidence manifests y causalidad se verifican dentro del package owned; el primary se conserva y el mismo package se finaliza idempotentemente después de cleanup, incluido recovery.
- Compile-only, vet, focales ×3, suite global, race focal/global con UCRT64 y cross-build Linux/Darwin pasan. Coverage reproducible del diff desde `b81cee0`: 1795/2111 = 85,0308 %.
- `feature_list.json` permanece intacto; la evidencia completa queda en `progress/functional-flow-validation/implementation.md` y se solicita nueva revisión independiente.

## 2026-10-23 — Feature 27, segunda remediación en curso

- Segunda revisión rechazada; se reabren T1–T6 y T8–T16 para REV-11..REV-16 y cierres parciales REV-3/REV-5/REV-6.
- En curso: approval exacta del FlowArtifact, provided independiente, identidad/binding sin fallback, selector privado de recurso, assertion ownership por step, journal causal real y lease total de retries.
- `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, segunda remediación completada

- REV-11 cierra approval exacta: view/context/grant sellan el `FlowArtifact` y Execute consume el mismo digest; provided se prepara sin invocar generación.
- REV-12..REV-14 cierran identidad explícita anonymous/credentialed, rechazo pre-red de drift, digest privado del recurso resuelto y ownership de declarations por `(stepID, declarationID)`.
- REV-15 sustituye eventos sintetizados por journal durable append-only de admission, fixtures, claims, intents, attempts, retries, evidence, assertions, comparisons, terminals, recovery, primary y cleanup; el package valida hash-chain, evidence y topología real.
- REV-16 valida que la lease cubre todos los retries/timeouts/backoff/confirmation y una carrera con reloj+barrier demuestra que el último intento no puede ser reclamado por el mismo ni otro owner.
- Paridad real incluye GitHub `Result`; package recovery recompone desde el mismo journal sin cambiar primary. Compile-only, vet, focales ×3, suite global, race focal/global, cross-build Linux/Darwin y diff-check pasan.
- Coverage statement-weighted reproducible de todo el diff desde `b81cee0`: 2256/2653 = 85,0358 %. T1–T16 vuelven a completas; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, tercera remediación en curso

- La tercera review cierra REV-11..REV-16 salvo las fronteras descritas por REV-17/REV-18; se reabren T11–T13, T15 y T16.
- En curso: terminal explícito `rejected` para runs admitidos que no alcanzan prepare, unión cerrada de estados de fixtures y ownership durable hasta evidence/assertions/terminal con `ConfirmationTimeout` efectivo.
- Se añadirán verticales reject/cancel/auto-denial con Result en cuatro superficies y carreras deterministas post-tool; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, tercera remediación completada

- REV-17 cerrado: terminales admitidos que no llegan a prepare persisten `fixtures_terminal/rejected`; reject, cancel y auto-denial publican package post-cleanup verificable sin steps, evidence, tools ni fixtures sintéticos, con Result paritario API/CLI/MCP/GitHub y recovery idempotente.
- REV-18 cerrado: `RenewClaim` conserva owner/token durablemente antes de Evidence.Confirm, journal, progreso del act, assertions y ConfirmTerminal; `ConfirmationTimeout` acota toda la sección post-tool.
- Barreras con fake clock cruzan expiraciones anteriores durante evidence y assertions, impiden reclaim del mismo y otro owner bajo race, y prueban fallo de renovación cerrado con recovery seguro.
- Compile-only, vet, focales ×3, suite global, race global y focal UCRT64, cross-build Linux/Darwin y diff-check pasan. Coverage statement-weighted: 2349/2763 = 85,0163 %.
- T11–T13/T15/T16 vuelven a completas; `feature_list.json` permanece intacto y la entrega solicita cuarta revisión independiente.

## 2026-10-23 — Feature 27, cuarta remediación en curso

- La cuarta review cierra REV-17/REV-18 y abre REV-19; se reabren T11–T13, T15 y T16.
- En curso: distinguir interrupción recuperable de primary terminal, persistir ejecución funcional pendiente y reanudar el executor desde checkpoints antes de cleanup/package.
- Se añadirán verticales reales de Service para fallos post-tool, crashes y redelivery multi-owner, preservando cero replay y paridad de Result; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, cuarta remediación completada

- REV-19 cerrado: ejecución funcional pendiente y primary durable son checkpoints distintos; ningún error recuperable inicia cleanup ni package.
- Recovery recompone el artifact/material aprobado y reanuda claim/intent/evidence/journal/action-progress/assertions/terminal con otro owner; prepare no se repite y el efecto incierto no se duplica.
- Cancelación durante recovery obtiene primero un primary cancelled; un fallo no recuperable sella un primary error antes del cleanup. Result converge en API/CLI/MCP/GitHub.
- Compile-only, vet, focales ×3, suite global, race global UCRT64 y cross-build Linux/Darwin pasan. Coverage del diff: 2435/2864 = 85,0209 %.
- T11–T13/T15/T16 vuelven a completas; `feature_list.json` permanece intacto y se solicita quinta revisión independiente.

## 2026-10-23 — Feature 27, quinta remediación en curso

- La quinta review abre REV-20; se reabren T11–T13, T15 y T16.
- En curso: cerrar el crash window entre executor completo, primary staged y flag durable mediante reconstrucción autenticada desde state+journal/evidence sin reejecutar tools ni duplicar eventos.
- Se añadirán verticales con Executor real, restart/otro owner/redelivery concurrente, reconciliación CAS de state y fallo cerrado ante divergencia; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 27, quinta remediación completada

- REV-20 cerrado: un journal autenticado completo reconstruye/promueve el primary desde state+evidence sin reentrar en Executor.Execute; el state terminal perdido se reconcilia por CAS.
- Crashes antes y después de Stage, restart con otro owner y redelivery concurrente mantienen una sola llamada de tool y un solo terminal, sin `recovery_resumed` adicional. El package final es verificable y Result coincide en API/CLI/MCP/GitHub.
- Journal divergente/tampered falla cerrado sin cleanup/replay; un checkpoint parcial reanuda el Executor real y converge sin duplicar I/O.
- Compile-only, vet, focales ×3, suite global, race global UCRT64 y cross-build Linux/Darwin pasan. Coverage del diff: 2614/3075 = 85,0081 %.
- T11–T13/T15/T16 vuelven a completas; `feature_list.json` permanece intacto y se solicita sexta revisión independiente.

## 2026-10-23 — Feature 27, sexta remediación en curso

- La sexta review cierra REV-20 y abre únicamente REV-21: la promoción de un primary completo debe aplicar antes de Stage la misma causalidad estricta que el package final.
- T11–T13, T15 y T16 se reabren hasta probar enlaces exactos journal→evidence→assertions/comparisons→terminal/state, fallo cerrado ante divergencia autenticada y regresión nominal/CAS/concurrent recovery.

## 2026-10-23 — Feature 27, sexta remediación completada

- REV-21 cerrado: `RecoverCompleted` y el package final comparten `BuildPackage`/`VerifyPackage`; una promoción valida toda la máquina causal antes de reconciliar state, Stage o cleanup.
- `evidence_confirmed` liga ID, bindings de operación/recurso, content digest y digest del manifest. Assertions, comparisons, terminal/state y primary se contrastan con evidence y ledger reales.
- Mutaciones MAC-válidas y reencadenadas de evidence ID, assertion ref, record digest y terminal, además de stores reales con binding/content/step/revision divergentes, fallan con workflow pending, cero replay, cero Stage durable y cero cleanup. Promoción nominal, CAS y concurrent recovery permanecen verdes.
- Compile-only, vet, focales ×3, suite global, race global UCRT64 y cross-build Linux/Darwin pasan. Coverage del diff: 2674/3137 = 85,2407 %.
- T11–T13/T15/T16 vuelven a completas; `feature_list.json` permanece intacto y se solicita séptima revisión independiente.

## 2026-10-23 — Feature 28, implementación inicial completa

- T1–T16 ejecutadas sobre `internal/projectmemory`: contrato v1 estricto e inmutable, scopes y lifecycle con autoridad, procedencia/evidencia/staleness, secret gate, proyecciones seguras y servicio compartido.
- El adapter local bajo el root configurado publica journal autenticado append-only, snapshot e índice atómicos; aplica CAS/idempotencia, locks OS Windows/Unix, recovery y defensas de paths, links, reparse points y hardlinks.
- Query estructural aplica filtros antes de orden/conflictos/truncado; bindings históricos sellan versiones exactas y `WorkflowGuard` revalida drift antes de pasos privilegiados. Proposals del agente y observations de functional flow no elevan autoridad ni modifican resultados funcionales.
- La Surface versionada es común para CLI/API/MCP/GitHub tras mapping externo del principal; no acepta keys, paths ni autoridad cruda. No se implementó selección por rol, ranking ni context assembly de feature 29.
- Gates iniciales: focal ×10, suite global, compile-only, vet, race focal/global con UCRT64 y cross-compile Linux/Darwin pasan. Coverage host statement-weighted: 837/984 = 85,0610 % (gate conservador incluyendo 25 statements Unix como no cubiertos; host Windows 837/959 = 87,2784 %).
- `feature_list.json` permanece intacto; se prepara `progress/validated-project-memory/implementation.md` para revisión independiente.

## 2026-10-23 — Feature 28, primera revisión rechazada

- El reviewer registra REV-1..REV-8; la feature permanece `in_progress`.
- Bloqueantes: proposals amplían scope/provenance, query limita antes de ordenar/conflictos, no hay integración productiva real, evidence no se revalida, journal truncable/trailing no autenticado y filesystem vulnerable a TOCTOU/replace no atómico.
- También deben cerrarse el binding exacto de scope y la trazabilidad con pruebas reales.
- Se reabren T1–T16 para remediación; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, segunda revisión rechazada

- REV-1, REV-2, REV-4 y REV-7 quedan cerrados; REV-3/5/6/8 siguen parciales.
- REV-9..REV-12 exigen composición real en workflow, ancla externa al root mutable, handles/no-follow en todos los objetos y autoridad de transición separada por actor/acción.
- Se reabren T3–T16 para remediación; la feature continúa `in_progress`.

## Feature 28 review remediation — 2026-10-23

- Reabiertas T1–T16 tras REV-1..REV-8.
- En curso: contratos server-owned, query pre-limit, evidencia revalidada, binding verificable, journal/ancla, hardening e integraciones verticales.


## Feature 28 review remediation complete — 2026-10-23

- REV-1..REV-8 remediadas con acceptance tests reales y trazabilidad actualizada.
- Integración real: agentplanning, functionalflow, validationworkflow, composition root y CLI/API/MCP/GitHub.
- Runtime tests Windows/Linux, cross-build Darwin, focal x10, global, vet y race global pasan.
- Coverage diff productivo multiplataforma: 1181/1338 = 88.2661%.
- T1–T16 cerradas de nuevo; listo para segunda review.

## 2026-10-23 — Feature 28 segunda remediación

- Segunda review rechazada: se reabren T3–T16 para REV-9..REV-12 y cierres completos de REV-3/5/6/8.
- Orden de trabajo: authority/commands y composición vertical; registry externa y filesystem boundaries; acceptance matrices, coverage y gates.
- feature_list.json permanece reservado al leader y no se modifica.

## 2026-10-23 — Feature 28, segunda remediación completada

- REV-9 queda cerrado con un único `projectmemory.Service` compuesto en el
  workflow productivo: planning obtiene query y binding confiables, el workflow
  persiste y revalida antes de approval/I/O y functional flow observa únicamente
  un package terminal verificado. CLI, API, MCP y GitHub recorren esa vertical.
- REV-10 queda cerrado con un registry autenticado en un trust root externo al
  root mutable. Borrado total del root, journal ausente, borrado de proyecciones
  y rollback coordinado fallan cerrados y conservan receipts tras reinicio.
- REV-11 queda cerrado con handles de directorio, no-follow/reparse, identidad
  efectiva, validación de hardlinks y DACL Windows, replace atómico y swaps
  deterministas en locks, reads, temporales, sync y publicación. Las matrices
  pasan en Windows y Linux; Darwin se valida por cross-build.
- REV-12 queda cerrado al separar observer, human y reconciler/configuration:
  `observed` requiere evidence verificada, las decisiones humanas usan
  `DecisionID`, expiry conserva autoridad de reconciler y GitHub exige la acción
  exacta para cada operación/target.
- T3–T16 vuelven a completas. Pasan gofmt, vet, compile-only, focales repetidos,
  suite global, race focal/global, runtime Linux y cross-build Linux/Darwin.
  Coverage statement-weighted del diff productivo desde `9c8aa34`:
  1464/1705 = 85,865103 %. `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, tercera remediación iniciada

- La tercera review rechaza REV-9/REV-11/REV-12 y abre REV-13/REV-14; se reabren T6–T16.
- En curso: enrutar API/MCP/GitHub por entrypoints productivos; traversal handle-relative de todos los ancestros y source identity hasta replace; authorities separadas por rol; autoridad monotónica independiente contra rollback coordinado; allowlists de query server-side.
- T16 añadirá un gate de coverage versionado y reproducible que fusiona Windows/Linux sin doble conteo. `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, cuarta remediación completada

- REV-9 queda cerrado con rutas productivas CLI/API/MCP/GitHub que comparten una
  única composición, principal, service y store; los acceptance tests entran por
  el dispatcher, servidor HTTP, stream MCP y webhook GitHub firmado.
- REV-11 queda cerrado con traversal handle-relative no-follow desde cada root,
  identidades fijadas de `v1`/`projects`/project, enumeración por handle, source
  temporal retenido hasta replace y revalidación de la ruta efectiva en cada
  checkpoint. Las matrices de swaps pasan en Windows y Linux.
- REV-12/REV-14 quedan cerrados con authorities, principals y actor kinds
  separados por rol, denegación cruzada y policy de query server-side que el
  request solo puede estrechar.
- REV-13 queda cerrado mediante un ledger monotónico autenticado externo al
  journal, proyecciones y registry restaurables; el rollback coordinado antiguo
  falla en restart y no permite reutilizar receipts.
- El gate versionado fusiona perfiles Windows/Linux por statement block sin
  doble conteo y obtiene `1858/2185 = 85,034325 %`. Pasan gofmt, diff-check, vet,
  compile-only, suite global, race global, runtime Linux y cross-builds
  Linux/Darwin. T6–T16 vuelven a completas; `feature_list.json` sigue intacto.

## 2026-10-23 — Feature 28, quinta remediación iniciada

- La cuarta review mantiene abiertos REV-13 y REV-15; se reabren T6, T8, T15 y T16.
- En curso: manifest independiente para distinguir bootstrap virgen de ledger borrado, matrices de deletion/corruption/rollback/crash y cobertura reproducible con margen estable.
- feature_list.json permanece reservado al leader y no se modifica.

## 2026-10-23 — Feature 28, quinta remediación completada

- REV-13 queda cerrado con un manifest HMAC externo publicado en generación cero
  antes del primer append. Bootstrap virgen, deletion después de historia,
  corrupción, rollback y crash quedan distinguidos sin reconstruir autoridad
  eliminada ni reutilizar receipts.
- El probe exacto restaura journal/snapshot/index/registry de seq1 tras confirmar
  seq2, elimina el ledger y verifica que restart y redelivery de approve fallan
  cerrados, sin recreación del ledger.
- REV-15 queda cerrado con perfiles regenerados desde cero y coverage del diff
  productivo Windows/Linux de `1941/2266 = 85,657546 %`.
- Pasan gofmt, auditoría de comentarios, vet, compile-only, focal repetido, suite
  global, race global, runtime Linux, cross-builds Linux/Darwin, coverage gate y
  diff-check. T6/T8/T15/T16 vuelven a completas; `feature_list.json` sigue intacto.

## 2026-10-23 — Feature 28, sexta remediación iniciada

- La quinta review cierra REV-15 y mantiene REV-13: ledger y manifest HMAC son
  replayables si se restauran juntos con el estado mutable.
- Se reabren T5, T6, T8, T15 y T16 para sustituir esos archivos por un puerto de
  witness monotónico externo con read exacto, bootstrap explícito y
  compare-and-advance atómico.
- La composición productiva exigirá una autoridad durable configurada y fallará
  si no puede demostrar esa garantía; no habrá fallback ni reconstrucción desde
  journal, registry o projections. `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, sexta remediación completada

- REV-13 queda cerrado mediante `MonotonicWitness`: una autoridad externa con
  read exacto, bootstrap explícito create-once y compare-and-advance atómico.
  Ledger y manifest locales se eliminaron; el store no puede escribir,
  reconstruir ni restaurar el head monotónico.
- El rollback conjunto de journal/snapshot/index/registry a seq1 contra witness
  seq2 falla en restart y redelivery sin reutilizar receipt. También pasan las
  matrices missing/unavailable/behind/ahead/replay, crash antes/después del CAS,
  restart con la misma autoridad, CAS concurrente y protocolo HTTP estricto.
- La composición exige witness HTTPS, credential y la garantía explícita
  `linearizable-durable-nonreplayable-v1`; configuración incompleta o replayable
  falla sin fallback local.
- Coverage final del diff: `1879/2199 = 85,447931 %`, 9 statements sobre el
  mínimo entero de 1870. Los perfiles Windows se regeneraron desde cero y se
  combinaron con los bloques exclusivamente Unix de la ejecución Linux previa;
  WSL no pudo reiniciarse al cierre por `ERROR_NO_SYSTEM_RESOURCES`, después de
  haber pasado el runtime Linux sobre la implementación Unix final.
- Pasan vet, compile-only, focal x3, suite global, race global, cross-builds
  Linux/Darwin, coverage gate y diff-check. T5/T6/T8/T15/T16 vuelven a completas
  y `feature_list.json` sigue intacto.

## 2026-10-23 — Feature 28, séptima remediación iniciada

- La sexta review cierra el witness monotónico externo y abre solo REV-16: el
  journal definitivo no puede sincronizarse antes de un CAS que todavía puede
  fallar.
- Se reabren T8, T15 y T16 para introducir staging autenticado durable, ligar su
  identidad opaca al witness y recuperar exactamente commits cuyo CAS avanzó
  antes de materializar journal/proyecciones, sin repetir efectos externos ni
  receipts.
- Se preserva R9: los fallos anteriores al avance dejan journal/head intactos y
  los posteriores convergen desde el staging verificado. `feature_list.json`
  permanece intacto.

## 2026-10-23 — Feature 28, séptima remediación completada

- REV-16 queda cerrado con protocolo prepare→CAS witness→materialize: el staging
  exclusivo se autentica y sincroniza antes del CAS, el witness conserva solo el
  digest opaco y el journal definitivo nunca se sincroniza antes de confirmar el
  avance.
- Fallos pre-CAS dejan journal/head byte-for-byte intactos; crashes post-CAS se
  recuperan desde el pending exacto sin reconsultar efectos ni duplicar receipts.
  Missing, tamper, hardlink/symlink/swap, mismatch, partial append y publicación
  incompleta fallan cerrados o convergen según la matriz aprobada.
- Pasan focal projectmemory ×3, suite global, vet, compile-only, race global
  serial, cross-build Linux/Darwin, pruebas del gate de cobertura y diff-check.
  Las dos flakes de temporización observadas bajo race paralelo pasaron aisladas
  ×3 y no reaparecieron en la global serial.
- Cobertura del diff productivo: `2624/3068 = 85,528031 %`, 16 statements sobre
  el mínimo entero. Perfiles Windows r7 frescos más bloques Unix de la última
  ejecución Linux; los fuentes Unix no cambiaron y el reintento WSL quedó
  bloqueado por `ERROR_NO_SYSTEM_RESOURCES`. T8/T15/T16 vuelven a completas,
  los temporales se retiraron y `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, octava remediación iniciada

- La séptima review abre solo REV-17 documental/evidencia; T16 se reabre sin
  cambiar el comportamiento del runtime.
- Se corregirá el protocolo descrito en design, la trazabilidad a nombres y
  subtests reales y el gate de cobertura para ligar cada perfil por hash al
  source final y rechazar perfiles stale, mixed o duplicados.
- La cifra objetivo fresca verificada por reviewer es `2027/2359 = 85,926240 %`;
  se regenerarán todos los perfiles desde el source final antes de publicarla.

## 2026-10-23 — Feature 28, octava remediación completada

- REV-17 queda cerrado documentalmente: design describe el protocolo durable
  entregado y no conserva la decisión rechazada de bloqueo pre-CAS.
- La trazabilidad cita únicamente los tests top-level reales y las rutas exactas
  de subtests bajo `TestWitnessCrashBoundariesAndConcurrentCAS`; el comando
  `go test -run` documentado reproduce la matriz relevante.
- El gate liga cada perfil por SHA-256 al hash del conjunto de fuentes Go
  productivos cambiados. Rechaza source stale, perfil mixed/tampered/desplazado,
  nombres/hashes duplicados y conserva una sola copia de common blocks válidos.
- Los tres perfiles Windows r8 y el perfil Linux r8 se regeneraron desde el mismo
  source final. El binario Linux ejecutó en WSL con focal 86,8 %; coverage exacta
  `2027/2359 = 85,926240 %` y 21 statements de margen sobre el mínimo entero.
- Pasan los seis tests del gate, focal jerárquico ×3, focal projectmemory ×3,
  vet, compile-only, suite global serial, cross-build Linux/Darwin, formato y
  diff-check. T16 vuelve a completa, temporales retirados y `feature_list.json`
  permanece intacto.

## 2026-10-23 — Feature 28, novena remediación iniciada

- La octava review mantiene abierto solo REV-17: `--write-manifest` podía ligar a
  posteriori perfiles arbitrarios al source actual. T16 se reabre.
- El gate se convertirá en runner: captura source pre/post, ejecuta el comando
  controlado y publica profile+sidecar únicamente como resultado indivisible.
  Linux ligará source, test binary y perfil mediante attestations del build y del
  runner, sin ruta pública para certificar evidencia preexistente.

## 2026-10-23 — Feature 28, novena remediación completada

- REV-17 queda cerrado con runners indivisibles. Se eliminó `--write-manifest`:
  ningún comando acepta un perfil preexistente para producir su sidecar.
- Cada runner host captura source SHA-256 antes/después, escribe el coverprofile
  solo en un path temporal controlado y publica profile+sidecar sellado tras exit
  cero. Interrupted run o source drift no publican evidencia aceptable.
- `build-linux` liga source+binary; el runner Linux verifica el binary antes y
  después, ejecuta el test y emite profile+attestation con hashes de source,
  binary, build attestation y profile. El host vuelve a verificar toda la cadena.
- Los probes cubren A→profile→B con intento de recertificar/validar, replacement
  de profile y sidecar, missing sidecar, interrupción, source drift durante el
  comando, binary mismatch, replay, mixed, duplicate y common-block dedupe.
- Cuatro perfiles r9 frescos reproducen `2027/2359 = 85,926240 %`; Linux focal
  `86,8 %`. Gate tests, shell syntax, focal ×3, suite global serial, vet,
  compile-only, cross-build Linux/Darwin, formato y diff-check pasan. T16 vuelve
  a completa; temporales retirados y `feature_list.json` intacto.

## 2026-10-23 — Feature 28, décima remediación iniciada

- REV-17 continúa abierto: sidecars con checksum público no acreditan el momento
  de generación. T16 se reabre y se elimina ese diseño.
- El gate expondrá únicamente `verify`, sin rutas, comandos, manifests ni
  evidencia externa. Las cuatro recetas quedan hardcoded y se ejecutan dentro de
  un temp privado; el mismo proceso mide y elimina sus artefactos.
- Linux será obligatorio: build, transferencia, hash pre/post, ejecución WSL y
  lectura del perfil pertenecen a la misma operación. Sin WSL el gate falla y no
  existe fallback a perfiles stale.

## 2026-10-23 — Feature 28, décima remediación completada

- REV-17 queda cerrado mediante `feature28_coverage.py verify`, único comando
  aceptado. No hay opciones de profile, command, manifest, sidecar o input.
- El gate crea un temp privado exclusivo, captura source hash, ejecuta tres
  recetas Windows hardcoded, compila Linux, valida el binary antes/después de
  transferirlo y ejecutarlo en WSL, recupera el profile, revalida source/perfiles,
  mide y elimina host+Linux temps en el mismo proceso.
- Los probes niegan opciones externas, prueban que stale/posthoc/duplicate se
  ignoran, y fuerzan source drift, interruption, binary replacement y profile
  replacement. Todo falla cerrado sin evidencia residual; common blocks frescos
  se deduplican una sola vez.
- La ejecución real obligatoria con WSL devuelve `2027/2359 = 85,926240 %` y
  limpia su temp. Unit probes, focal ×3, suite global serial, vet, compile-only,
  cross-build Linux/Darwin, formato y diff-check pasan. T16 vuelve a completa y
  `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, undécima remediación iniciada

- REV-17 mantiene un TOCTOU en el gate: digest y merge reabrían paths. T16 se
  reabre sin cambios al producto.
- Profiles y binary Linux se leerán una sola vez por handle no-follow, con
  identidad/tipo/link/límite verificados; parser, merge y transferencia usarán
  exclusivamente esos buffers inmutables.
- Source se sellará antes de generar, tras cargar buffers y al terminar measure;
  probes deterministas cubrirán swap post-load, drift durante measure,
  truncation, oversize y duplicate bytes.

## 2026-10-23 — Feature 28, undécima remediación checkpoint

- Profiles se abren una vez por handle no-follow y parser/merge consumen solo
  buffers inmutables acotados. El binary Linux se transfiere desde su único
  buffer y el runner remoto conserva la identidad abierta para hash pre,
  ejecución y hash post, sin validate-then-reopen.
- Ocho probes del gate pasan: swap post-load/pre-merge, swap del binary, source
  drift durante measure, truncation, oversize, duplicate bytes, interrupción y
  rechazo de inputs/evidencia externa.
- Vet, compile-only, focal projectmemory ×3, suite global, race global serial y
  cross-builds Linux/Darwin pasan. La ejecución real del gate continúa pendiente:
  WSL/HCS falla al crear cualquier VM con `ERROR_NO_SYSTEM_RESOURCES`; Docker no
  dispone de engine y no hay Podman/QEMU/Lima local. El gate falla cerrado, no
  reutiliza perfiles stale y T16 permanece abierta.

## 2026-10-23 — Feature 28, undécima remediación entrega condicional

- Estado `ready_for_review_conditional`: implementación y ocho probes TOCTOU en
  pass; vet, compile-only, focal ×3, global, race global serial y cross-builds
  Linux/Darwin también pasan.
- El reintento final de `wsl.exe -l -q` devuelve lista vacía. El gate cerrado
  vuelve a fallar con `WSL_E_DISTRO_NOT_FOUND` tras generar desde cero su parte
  Windows y el binary Linux, elimina el temp y no acepta evidencia anterior.
- Para cerrar T16 después de recuperar WSL: `wsl.exe -l -q` debe listar
  `docker-desktop` ejecutable y `$env:PYTHONDONTWRITEBYTECODE='1'; python
  scripts/feature28_coverage.py verify` debe producir >=85 % en esa única
  operación. No se declara coverage fresca ni PASS del gate hasta entonces.

## 2026-10-23 — Feature 28, undécima remediación completada

- Docker/WSL vuelve a exponer `docker-desktop`. El gate cerrado se ejecutó desde
  cero sobre el source final y obtuvo `2027/2359 = 85,926240 %`, 21 statements
  por encima del mínimo entero; ningún profile previo entró en el cálculo.
- El runner Linux conserva por descriptor la identidad del binary transferido
  para hash pre, ejecución y hash post, y devuelve el profile desde un descriptor
  abierto tras retirar su pathname. Los cuatro buffers se cargan una sola vez y
  parser/merge no reabren filesystem.
- T16 vuelve a completa. Los ocho probes TOCTOU, vet, compile-only, focal ×3,
  global, race global serial, runtime Linux, cross-builds Linux/Darwin, formato,
  diff-check y limpieza pasan; `feature_list.json` permanece intacto.

## 2026-10-23 — Feature 28, duodécima remediación iniciada

- REV-17 reabre T16: el contenido lógico del remote runner era correcto tras la
  interpretación de WSL, pero sus bytes en Python contenían escapes destinados
  a una segunda capa `sh -lc`; la evidencia no demostraba sintaxis shell literal.
- El runner se entregará como bytes exactos por stdin a `sh -s`, con `$(...)`,
  `/proc/$$/fd/...` y `$before` literales. `bash -n` y una integración WSL con el
  binario focal ejecutarán exactamente esos mismos bytes antes de repetir el gate.

## 2026-10-23 — Feature 28, duodécima remediación completada

- `linux_runner_script()` produce bytes shell literales y `sh -s` los recibe por
  stdin. La transferencia previa del binary usa argv directos de WSL y su buffer
  inmutable; no queda ninguna capa `sh -lc` que reinterprete `$` o backslashes.
- Diez probes pasan. `bash -n` consume exactamente el mismo buffer del runner y
  la integración real compila el focal Linux, ejecuta esos bytes, conserva la
  identidad por descriptores y valida el profile devuelto. El fake contrasta el
  stdin/output completo y no substrings de una representación escapada.
- El gate real se regeneró desde cero con WSL y obtuvo `2027/2359 = 85,926240 %`.
  T16 vuelve a completa; temporales host/WSL se eliminan y `feature_list.json`
  permanece intacto.

## 2026-10-23 — Feature 28, decimotercera remediación iniciada

- REV-17 reabre T16 para cerrar la identidad remota: el runner literal todavía
  abría el pathname del binary tres veces antes de unlink.
- El runner abrirá el pathname exactamente una vez, duplicará ese FD para hash
  pre, ejecución y hash post, y retirará el nombre. Una barrera determinista de
  test sustituirá el path después de la apertura para demostrar que solo se usan
  los bytes originales o se falla cerrado.

## 2026-10-23 — Feature 28, decimotercera remediación completada

- El remote binary se abre una vez con `exec 3<`; los FDs 4 y 5 se duplican desde
  el 3 y el pathname se retira. Hash pre, focal real y hash post operan por
  `/proc/$$/fd`, ligados a la identidad abierta.
- El probe determinista pausa después de las duplicaciones, reemplaza el pathname
  con otro ejecutable y confirma que el runner usa los bytes originales. El test
  de sintaxis exige una sola apertura del path y dos duplicaciones de FD.
- Once probes, `bash -n`, integración focal Linux y gate real pasan. Coverage
  fresca: `2027/2359 = 85,926240 %`. T16 vuelve a completa; cleanup host/WSL y
  ausencia de cambios en `feature_list.json` confirmados.

## 2026-10-24 — Feature 29, especificación iniciada

- Seleccionada `context-assembly`, primera feature `pending` con SDD y sin otra
  feature `in_progress`.
- Rama `feature-29-context-assembly` creada desde la feature 28 completada,
  porque el ensamblado consume su memoria validada y sus bindings.
- El spec author redactará requirements, design y tasks. No se tocará código
  productivo antes de la aprobación humana explícita.

## 2026-10-24 — Feature 29, especificación preparada

- Creados `requirements.md`, `design.md` y `tasks.md` para
  `context-assembly`; la feature pasa de `pending` a `spec_ready`.
- La spec define R1–R15 y T1–T16: perfiles por rol, filtros estructurales antes
  de ranking, secciones de confianza, budgets exactos, snapshots/bindings,
  revalidación, store, integración vertical y verificación multiplataforma.
- GitHub PR context, environment providers y configuración de proyecto futura
  permanecen fuera de alcance. Se espera aprobación humana antes de implementar.

## 2026-10-24 — Feature 29 aprobada e implementación iniciada

- El usuario aprueba explícitamente la spec; `requirements.md` registra
  `approved_by: user` y `approved_at: 2026-10-24T15:52:24+02:00`.
- `context-assembly` cambia de `spec_ready` a `in_progress`.
- El implementer ejecutará T1–T16 en orden, con comentarios de responsabilidad
  sistémica, trazabilidad R→test y gates multiplataforma antes de review.

## 2026-10-24 — Feature 29, núcleo e integración implementados

- Añadido `internal/contextassembly` con contratos `ContextSnapshot/v1` y
  `ContextBinding/v1`, perfiles sellados por rol, producers neutrales, secret
  gate, filtros estructurales, ranking derivado, packing exacto, bindings,
  revalidación y stores inmutable en memoria y filesystem.
- Integrado el envelope estructurado como único input de `agentplanning` y el
  lifecycle prepare/bind/revalidate en `validationworkflow`, incluida la
  composición compartida por CLI, API, MCP y GitHub sin obtener contexto de
  GitHub ni implementar environments.
- Añadidas matrices adversarias para schemas, secretos, autoridad, conflicts,
  budgets, drift, tampering, root replacement, concurrencia, observabilidad y
  consumidores planner/analyzer/diagnostic.

## 2026-10-24 — Feature 29, verificación en curso

- Coverage fresco combinado en Windows y runtime Linux, calculado contra
  `45d325a` e incluyendo archivos con build tags: `966/1122 = 86,096257 %`.
- `go vet ./...`, compile-only global, runtime Linux focal, suite global y la
  flake histórica de `validationworkflow` aislada tres veces pasan; una primera
  repetición focal concurrente reprodujo flakes preexistentes de feature 28 y
  se repite de forma aislada/serial antes del cierre.
- La auditoría AST confirma comentarios de responsabilidad en todas las
  declaraciones nuevas de producción y helpers de test de feature 29.

## 2026-10-24 — Feature 29, implementación preparada para review

- El store local instala y revalida permisos owner-only en Unix y una DACL
  protegida allowlisted en Windows; también rechaza mutación de permisos,
  symlink/reparse, hardlink y reemplazo coordinado de root/directorios.
- Pasan vet, compile-only, focales ×3, global, global serial ×3, race focal ×3,
  race global serial UCRT64, runtime Linux y cross-builds
  Linux/Darwin/Windows para los cuatro paquetes afectados.
- T1–T16 quedan completas y la trazabilidad R1–R15, comandos, coverage exacta y
  limitaciones se registran en `progress/context-assembly/implementation.md`.

## 2026-10-24 — Feature 29, primera remediación iniciada

- Review 1 rechaza la entrega con REV-1–REV-11: trust classes aportadas por
  sources, boundary de modelo incorrecto, packing sobre bytes distintos,
  suficiencia/policies incompletas, budgets/allowlists ausentes, secret gate y
  redaction insuficientes, journal/store incompletos, provenance sin contexto,
  consumers productivos ausentes y una flake de lifecycle bajo race.
- Se reabren T1–T16 por afectación transversal. La remediación conservará el
  spec aprobado, añadirá matrices adversarias y regenerará toda la evidencia
  Windows/Linux desde `45d325a` antes de solicitar una segunda review.

## 2026-10-23 — Feature 28 cerrada

- Review independiente aprobada en la decimotercera revisión; REV-1–REV-17
  quedan cerrados y R1–R15 trazados.
- El smoke final de leader para witness, recovery, rollback y CAS multiproceso
  pasa. El gate fresco reproduce `2027/2359 = 85,926240 %`.
- T1–T16 completas. El estado de `validated-project-memory` cambia de
  `in_progress` a `done`; no se incluyó alcance de feature 29.

## 2026-10-24 — Feature 29, primera remediación completada

- Cerrados REV-1–REV-11 sin rebajar el spec: composición posee trust classes,
  instrucciones/model envelope quedan separados, el packing cuenta el envelope
  final, perfiles y budgets fallan cerrado, redaction/journal/store se autentican
  y provenance/workflow/superficies ligan el context digest completo.
- Analyzer y diagnostic consumen proyecciones productivas de mínimo privilegio;
  CLI, API, MCP y GitHub comparten el mismo run, digest, binding y service sin
  aceptar claims de autoridad del caller.
- Pasan focal ×3, global serial, vet, compile-only, race focal/global,
  compilación cruzada Linux/Darwin/Windows y auditoría de comentarios/cleanup.
  Coverage reproducible contra `45d325a`: `1273/1479 = 86,071670 %`.
- El runtime Linux ya pasó sobre el árbol productivo final. El reintento de
  cierre no pudo iniciar `docker-desktop` por `ERROR_NO_SYSTEM_RESOURCES`; queda
  documentado como limitación del host y no como fallo del código.

## 2026-10-24 — Feature 29, segunda remediación iniciada

- Review 2 rechaza la entrega con REV-12–REV-19: duration budget sin deadline
  efectivo, commit point ambiguo, journal sin witness monotónico externo, sello
  incompleto del contexto privilegiado, registry de roles controlable por el
  caller, taxonomía de secretos incompleta, operaciones pathname vulnerables y
  auditoría de comentarios no portable a Linux.
- Se reabren T3, T5, T9, T10, T12, T14, T15 y T16. La remediación mantendrá
  REV-1–REV-11 cerrados y repetirá coverage, race, runtimes Linux y gates
  multiplataforma antes de una nueva review.

## 2026-10-24 — Feature 29, segunda remediación completada

- Cerrados REV-12–REV-19: deadline efectivo y propagado; commit point en CAS;
  witness monotónico externo de feature 28; sello canónico end-to-end; registries
  privados de composición; taxonomía R14 recursiva; filesystem descriptor-relative
  no-follow en Windows/Unix; auditoría portable y cinco runtimes Linux reales.
- Pasan focal ×3, global, vet, compile-only, race focal/global serial, los cinco
  cross-builds Linux/Darwin y compilación Windows nativa. La auditoría de
  comentarios y los swaps reales de root/subdir/temp/object también pasan.
- Coverage fresca contra `45d325a`, incluidos build tags: `1655/1919 =
  86,242835 %`, umbral 1632 y margen 23 statements. T3, T5, T9, T10, T12,
  T14, T15 y T16 vuelven a completas con evidencia.

## 2026-10-24 — Feature 29, tercera remediación iniciada

- Review 3 reabre únicamente T10, T13, T14, T15 y T16 por REV-20–REV-22:
  terminal observer bajo deadline agotado, reconciliación de CAS ambiguo y
  revalidación durable reconstruible tras restart.
- La remediación mantendrá REV-1–REV-19 cerrados, exigirá store/witness durable
  en producción y repetirá coverage, race, Linux, cross-builds y cleanup antes
  de solicitar nueva review.

## 2026-10-24 — Feature 29, tercera remediación completada

- Cerrados REV-20–REV-22: el observer terminal usa finalización detached y
  acotada; todo error CAS se reconcilia contra el witness con estados confirmed,
  rollback o uncertain recuperable; el registry durable se reconstruye tras
  restart sin maps activos del proceso.
- La composición productiva exige store local y witness monotónico durable. Un
  adapter nuevo revalida snapshots, bindings y profiles persistidos antes de
  approval, execution y recovery; redelivery converge sin duplicar receipts.
- Pasan vet, compile-only, focales ×3, suite global, race focal/global, cinco
  runtimes Linux y diez cross-builds Linux/Darwin. Coverage fresca contra
  `45d325a`: `1713/1987 = 86,210367 %`, umbral 1689 y margen 24 statements.
- T10, T13, T14, T15 y T16 vuelven a completas; no se modificó
  `feature_list.json` ni se añadió alcance de feature 30/31.

## 2026-10-24 — Feature 29, cuarta remediación iniciada

- Review 4 reabre T3, T10, T13, T15 y T16 por REV-23–REV-25: el registry
  durable debe resolver producers productivos actuales; `LocalStore` no puede
  mantener locks Go durante filesystem o witness; la composición debe propagar
  cancelación del caller a Bootstrap/Read/HTTP del witness.
- La remediación mantiene REV-1–REV-22 cerrados y añadirá probes de restart con
  drift real, witness bloqueado, writers concurrentes y servidor HTTP cancelado
  antes de regenerar coverage y gates multiplataforma.

## 2026-10-24 — Feature 29, cuarta remediación completada

- Cerrados REV-23–REV-25: restart resuelve producers desde workflow/admission
  actuales; `LocalStore` no mantiene locks Go durante disco o witness; el caller
  context alcanza Read, Bootstrap y HTTP de la composición productiva.
- Probes nuevos demuestran drift seq1→seq2 antes de approval/execution/recovery,
  rechazo de registry missing/version/authority mismatch, reads confirmados con
  CAS bloqueado, writers concurrentes y cancelación HTTP sin goroutines.
- Pasan gofmt, auditoría comments, vet, compile-only, focales ×3, suite global,
  race focal/global, cinco runtimes Linux y diez cross-builds Linux/Darwin.
  Coverage fresca contra `45d325a`: `1777/2069 = 85,886902 %`, umbral 1759 y
  margen 18 statements.
- T3, T10, T13, T15 y T16 vuelven a completas. `feature_list.json` permanece
  sin cambios y no se implementa alcance de feature 30/31.
## Feature 29 — quinta remediación iniciada (2026-10-24)

- Reabiertas T10, T13, T15 y T16 por REV-24, REV-26 y REV-27 de la quinta revisión.
- REV-24: reducir la coordinación a reservas/transiciones breves sin mantener locks durante disco, witness, timers o callbacks, conservando convergencia por identidad.
- REV-26: impedir que snapshots o bindings precommit sean direccionables hasta que exista confirmación autoritativa ligada a receipt/journal.
- REV-27: conservar `context.Canceled`/`context.DeadlineExceeded` y su categoría tipada en Read, Bootstrap y construcción inicial del store.
- Alcance congelado a estos findings; feature 30/31 y `feature_list.json` permanecen fuera del cambio.
## Feature 29 — quinta remediación completada (2026-10-24)

- Cerrados REV-24, REV-26 y REV-27 sin ampliar alcance: commits optimistas sin locks durante I/O externo, objetos invisibles hasta receipt confirmado y cancelación preservada en composición/store.
- Los candidatos precommit viven en `staging`; writers independientes progresan con otro CAS bloqueado, la misma identidad converge y restart distingue rollback, confirmado y estado incierto.
- Probes cubren snapshot/binding antes y durante CAS, rollback/reopen, payload confirmado ausente, Read/Bootstrap HTTP cancelados y contexto cancelado antes de validar `NewLocalStore`.
- Pasan gofmt, auditoría de comentarios, vet, compile-only, focales ×3, focal race ×3, suite global serial normal/race, cinco runtimes Linux y cross-builds 15/15.
- Coverage fresca contra `45d325a`, incluyendo build tags: `1853/2169 = 85,431074 %`; umbral 1844 y margen 9 statements.
- T10, T13, T15 y T16 vuelven a completas; `feature_list.json` permanece sin cambios y feature 30/31 siguen fuera de alcance.
## Feature 29 — sexta remediación iniciada (2026-10-24)

- Reabiertas T1, T10, T13, T15 y T16 por REV-28 y la parte restante de REV-27.
- REV-28: eliminar el cleanup basado en `objectCreated`/ausencia transitoria de receipt y probar la intercalación exacta de dos keys con digest compartido para snapshot y binding.
- REV-27: conservar `context.Cause`, añadir `CategoryTimeout` y distinguir deadline de cancelación en store y composición productiva.
- REV-1–REV-26 permanecen cerrados salvo la concurrencia de REV-24 continuada como REV-28; `feature_list.json` y feature 30/31 quedan fuera de alcance.

## Feature 29 — sexta remediación completada (2026-10-24)

- REV-28 queda cerrado: objetos content-addressed compartidos sobreviven al interleaving perdedor-ganador entre CAS y receipt para snapshot y binding; cleanup autentica staging, journal y receipts y falla cerrado ante metadata/transitorios.
- REV-27 queda cerrado: `context.Cause` conserva sentinels sin filtrar texto, `CategoryTimeout` distingue deadlines y composición/store mantienen `errors.Is` en Read, Bootstrap y construcción pre-cancelada.
- Los interleavings snapshot/binding y witness bloqueado pasan ×50; focal race pasa ×20. Focales, compile-only, vet, global serial, race global serial y cinco binarios Linux permanecen verdes sin cambios productivos posteriores.
- Coverage fresca combinada contra `45d325a`: `1909/2234 = 85,452104 %`, umbral 1899 y margen 10 statements; cross-builds 15/15.
- T1, T10, T13, T15 y T16 vuelven a completas. `feature_list.json` no fue modificado en esta remediación y feature 30/31 siguen fuera de alcance.

## Feature 29 — séptima remediación iniciada (2026-10-24)

- Reabiertas T10, T15 y T16 por REV-29; REV-1–REV-28 permanecen cerrados.
- Se elimina el borrado transaccional de objetos content-addressed: rollback y contención solo retiran candidatos no direccionables; una GC futura deberá partir de un snapshot autoritativo estable.
- Se añade una intercalación coordinada de tres writers para snapshot y binding, colocando el tercer staging después de que el primer perdedor alcance cleanup y antes de liberar al ganador.
- `feature_list.json`, feature 30 y feature 31 permanecen fuera de alcance.

## Feature 29 — séptima remediación completada (2026-10-24)

- REV-29 cerrado mediante retención: ningún camino transaccional o de recovery borra objetos content-addressed; rollback y contención solo retiran candidatos no direccionables.
- El probe de tres writers pasa para snapshot y binding: matriz combinada con witness bloqueado ×50 normal y ×20 race, sin ampliar timeouts.
- Pasan focales ×3 normal/race, vet, compile-only, suite global serial normal/race, cinco binarios Linux reales en Alpine y cross-builds 15/15.
- Coverage fresca contra `45d325a`, con cinco profiles Windows y cinco Linux: `1874/2179 = 86,002754 %`; umbral 1853 y margen 21 statements.
- T10, T15 y T16 vuelven a completas; `feature_list.json` permanece sin cambios de worktree y feature 30/31 siguen fuera de alcance.

## Feature 29 — cierre aprobado (2026-10-24)

- La revisión independiente 8 aprobó el diff completo: R1–R15 y REV-1–REV-29 quedan cerrados sin regresiones.
- El smoke final del leader pasó en `contextassembly`, `agentplanning`, `planapproval`, `validationworkflow` y `cmd/featureproof`; `git diff --check` también pasó.
- Gates verificados por reviewer: suite global normal/race, cinco runtimes Linux y cross-builds 15/15.
- Cobertura fresca multiplataforma del diff productivo: `1881/2187 = 86,008230 %`, incluyendo conservadoramente el fallback Unix no ejecutado.
- Feature 29 marcada `done`; queda pendiente únicamente commit y push de la rama `feature-29-context-assembly`.

## Feature 30 — especificación iniciada (2026-10-25)

- Seleccionada `github-pull-request-context`, primera feature `pending` con SDD y sin otra feature `in_progress`.
- Rama `feature-30-github-pull-request-context` creada desde la feature 29, porque el contexto de pull request se integrará con `ContextAssembler` y sus contratos de diff autorizados.
- Alcance de esta fase: redactar `requirements.md`, `design.md` y `tasks.md`; no se modifica código productivo ni tests antes de aprobación humana.

## Feature 30 — especificación lista (2026-10-25)

- El spec author completó `requirements.md`, `design.md` y `tasks.md` para `github-pull-request-context`.
- La spec define R1–R15 y T1–T16, separa autoridad de composición y contenido GitHub no confiable, y delimita la feature frente a webhooks/publicación y entornos.
- Todos los requirements tienen criterios verificables y referencias de tasks; no quedan decisiones de producto abiertas ni excepciones arquitectónicas.
- Estado cambiado de `pending` a `spec_ready`. La implementación queda detenida hasta aprobación humana explícita.

## Feature 30 — especificación aprobada (2026-10-25)

- El usuario aprobó explícitamente la especificación de `github-pull-request-context`.
- Aprobación registrada a las `2026-10-25T11:52:20+01:00`; estado cambiado de `spec_ready` a `in_progress`.
- Comienza la implementación ordenada de T1–T16; el estado `done` queda reservado a una review independiente aprobada y gates completos.

## Feature 30 — implementación en curso (2026-10-25)

- Añadido `internal/githubcontext`: policy y modelos canónicos, adquisición coherente metadata/files/metadata, límites y omisiones, parser de hunks, extractores versionados, selección OpenAPI, secret gate, binding, refresh y stores idempotentes memory/local.
- `internal/githubpr.RESTAdapter` expone un puerto de contexto read-only con permisos exactos `metadata:read` + `pull_requests:read`; no sigue URLs remotas ni devuelve DTOs/credenciales.
- `validationworkflow` compone producers SourceDiff iniciales y actuales desde autoridad GitHub sellada; restart/revalidation usa observación fresca. `cmd/featureproof` acepta una policy sellada opcional y persiste context receipts bajo el workflow store.
- Los constraints extraídos viajan como datos no confiables dentro de SourceDiff para no competir con el producer autoritativo `SourceConstraints` existente.
- Focales actuales de `githubcontext`, `githubpr`, `validationworkflow` y `cmd/featureproof` pasan; cobertura focal de `githubcontext`: 85,2 %.

## Feature 30 — endurecimiento de límites (2026-10-25)

- La policy de contexto limita ahora los bytes de cada respuesta REST antes de decodificar; el adapter conserva el reloj inyectado al traducir `Retry-After`.
- Los límites globales de archivos, páginas e items producen omisiones content-free, marcan completitud parcial y evitan iniciar páginas adicionales una vez agotado el presupuesto.
- La selección de contratos parte de paths observados y allowlisted aunque el patch esté ausente, por lo que rename/delete siguen siendo representables sin inventar bodies.
- La proyección empaqueta metadata, constraints, contratos y omisiones en un único item neutral y mantiene `1 + files <= MaxItems`; todo contenido GitHub conserva `untrusted_data`.
- `Refresh` aplica los mismos intentos, espera y deadline que la adquisición inicial. Los focales de los cuatro paquetes afectados vuelven a pasar tras el endurecimiento.

## Feature 30 — verificación detenida por runtime Linux (2026-10-25)

- T1–T15 quedan implementadas, probadas y marcadas; T16 conserva pendiente únicamente la ejecución real de los cuatro binarios Linux.
- Pasan formato, auditoría de comentarios, vet, compile-only, focales ×3, suite global normal, focal race y race global serial con UCRT64.
- Cross-builds Windows/Linux/Darwin pasan 12/12. Coverage exacta conservadora del diff desde `4d0a0da`: `654/766 = 85,378590 %`, contando `sync_unix.go` como 0/1.
- El runtime Linux no está disponible en este host: Docker Desktop/WSL devolvió `ERROR_NO_SYSTEM_RESOURCES`; tras apagar WSL no queda ninguna distro registrada y el servicio Docker requiere privilegios ausentes.
- Informe y trazabilidad completa: `progress/github-pull-request-context/implementation.md`. `feature_list.json` permanece intacto y no hay temporales.

## Feature 30 — verificación Linux completada (2026-10-25)

- Recuperado un runtime hermético Alpine 3.22.2 en WSL2 `FeatureProof-Test` y compilados de cero cuatro test binaries Linux `CGO_ENABLED=0` fuera del repositorio.
- `githubcontext`, `githubpr`, `validationworkflow` y `cmd/featureproof` pasan en Linux desde cwd compatibles; el profile Linux ejecuta también `sync_unix.go`.
- Coverage exacta multiplataforma fresca del diff desde `4d0a0da`: `656/770 = 85,194805 %`; `sync_unix.go` 4/5 y `sync_windows.go` 1/1.
- T16 queda completada y todas las tasks T1–T16 están `[x]`; no hubo cambios productivos al reanudar este gate.

## Feature 30 — remediación de review 1 iniciada (2026-10-25)

- Reabiertas T1, T2, T3, T4, T6, T7, T10, T11, T13, T14, T15 y T16 por REV-1–REV-6.
- Alcance cerrado a los seis blockers: record durable exactly-once, Link ligado al endpoint, constraints sin truncado semántico, policy estricta, verificación semántica de Context/Binding y cancel/timeout preservados en fronteras.
- `feature_list.json`, webhooks/publicación y features 31/36 permanecen fuera de alcance.

## Feature 30 — remediación de review 1 completada (2026-10-25)

- REV-1: la composición productiva usa `LocalRecordObserver`, que publica records terminales content-free por hard-link inmutable; reopen, redelivery, respuesta perdida, outcomes y corrupción están probados.
- REV-2: cada `Link` se valida contra scheme/host, endpoint exacto, repo, PR, `page` y `per_page`; origen externo, cross-repo, página repetida y query adicional fallan cerrado.
- REV-3/REV-4: constraints se extraen del body completo antes de proyectarlo y nunca de un prefijo truncado; policy, extensiones, catálogo, aliases/versiones, constraints y safe refs quedan canónicos y no ambiguos.
- REV-5: `Context`, `Binding`, store y decode verifican semántica completa además del digest, incluidas relaciones entre objetos y miembros JSON duplicados.
- REV-6: token, HTTP, body, paginación, Load/Commit y publicación consultan `context.Cause`/`Err`; timeout y cancelación conservan categoría y sentinel distintos.
- Gates finales verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal, focal/global race UCRT64, cross-builds 12/12 y runtime Alpine WSL2 4/4.
- Coverage exacta fresca contra `4d0a0da`, uniendo cuatro profiles Windows y uno Linux por statement: `909/1069 = 85,032741 %`; incluye `sync_unix.go` 4/5 y `sync_windows.go` 1/1.
- T1–T16 vuelven a `[x]`; no se modificó `feature_list.json` ni se amplió el alcance.

## Feature 30 — remediación de review 2 iniciada (2026-10-25)

- Reabiertas T5, T6, T15 y T16 por REV-1, REV-7 y REV-8; REV-2–REV-6 permanecen cerrados.
- Alcance limitado a identidad terminal válida/exactly-once y propagación de fallo del observer, validación completa de rangos unified diff y una gramática canónica de paths compartida por dominio/adapter.
- `feature_list.json`, webhooks/publicación y features 31/36 permanecen fuera de alcance.

## Feature 30 — remediación de review 2 completada (2026-10-25)

- REV-1: el record incorpora `acquisition_digest`; su ID terminal se recalcula desde dimensiones content-free estables, sanea inputs inválidos, diferencia stale/success y converge redeliveries aunque cambien attempts/duration. Un fallo de confirmación del observer ya no se ignora.
- REV-7: cada header unified diff valida gramática/rangos canónicos sin overflow y cada hunk consume exactamente sus líneas old/new; el verificador de Context repite la comprobación ante objetos self-signed.
- REV-8: `CanonicalRepositoryPath` es la única gramática usada por dominio y adapter; rechaza controles Unicode, NUL, segmentos vacíos/trailing, traversal, separadores/colon y devices DOS reservados con/sin extensión.
- Gates completos verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal, focal/global race UCRT64, cross-builds 12/12 y runtime Alpine WSL2 4/4.
- Coverage exacta fresca contra `4d0a0da`, con cuatro profiles Windows y uno Linux unidos por coordenada: `989/1162 = 85,111876 %`; incluye ambos build tags.
- T5/T6/T15/T16 vuelven a `[x]`; `feature_list.json` permanece intacto y no se amplió alcance.

## Feature 30 — remediación de review 3 iniciada (2026-10-25)

- Reabiertas T5, T6, T15 y T16 por REV-9–REV-12; REV-2–REV-8 y recorridos nominales anteriores permanecen cerrados.
- Alcance limitado a preservar Context/Binding ante fallo del observer, separar key estable/digest integral del record, validar orden/overlap de hunks y rechazar UTF-8 inválido antes de canonicalizar/decodificar.
- `feature_list.json`, webhooks/publicación y features 31/36 permanecen fuera de alcance.

## Feature 30 — remediación de review 3 completada (2026-10-25)

- REV-9: error/cancel/timeout del observer conserva Context y Binding ya confirmados junto al error tipado/cause; una redelivery posterior publica el terminal record desde store sin repetir gateway.
- REV-10: `Record.ID` es la key exactly-once estable y `Record.Digest` sella canónicamente todos los campos, incluidos attempts/duration; publish, read y redelivery verifican ambos y el tamper falla cerrado.
- REV-11: cada hunk conserva coordenadas old/new half-open y la secuencia exige avance monotónico sin overlap, duplicado ni retroceso, también para rangos zero-count y Context self-signed.
- REV-12: paths rechazan bytes UTF-8 inválidos antes de canonicalización y el adapter rechaza toda respuesta no UTF-8 antes del decoder JSON, cubriendo title/body/filename/patch raw.
- Gates completos verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal, focal/global race UCRT64, cross-builds 12/12 y runtime Alpine WSL2 4/4.
- Coverage exacta fresca contra `4d0a0da`, uniendo cuatro profiles Windows y uno Linux: `1009/1183 = 85,291631 %`, incluidos ambos build tags.
- T5/T6/T15/T16 vuelven a `[x]`; `feature_list.json` permanece intacto y no se amplió alcance.

## Feature 30 — cierre aprobado (2026-10-25)

- La revisión independiente 4 aprobó el diff completo: R1–R15, T1–T16 y REV-1–REV-12 quedan cerrados.
- El smoke final del leader pasó en `githubcontext`, `githubpr`, `validationworkflow` y `cmd/featureproof`; `git diff --check` también pasó.
- Gates independientes: suite global normal/race, cuatro runtimes Linux y cross-builds Windows/Linux/Darwin 12/12.
- Cobertura fresca multiplataforma del diff productivo: `1012/1186 = 85,328836 %`, incluyendo `sync_unix.go` y `sync_windows.go`.
- Feature 30 marcada `done`; queda commit, push y retirada del runner WSL temporal.

## Feature 31 — especificación iniciada (2026-10-25)

- Seleccionada `environment-provider`, primera feature `pending` con SDD y sin otra feature `in_progress`.
- Rama `feature-31-environment-provider` creada desde la feature 30 para conservar el workflow, la atestación y las fronteras de contexto ya integradas.
- La spec deberá abstraer adquisición/liberación de targets y adaptar el preview existente sin implementar todavía Kubernetes ni otro provider gestionado real.
- No se modifica código productivo ni tests antes de aprobación humana.

## Feature 31 — especificación lista (2026-10-25)

- El spec author completó `requirements.md`, `design.md` y `tasks.md` para `environment-provider`.
- La spec define R1–R15 y T1–T16, unifica adquisición/revalidación/release mediante `EnvironmentPort` y adapta feature 19 sin duplicar su lifecycle.
- El provider gestionado queda limitado a un fake contractual; Kubernetes e infraestructura real permanecen fuera de alcance.
- No quedan decisiones de producto abiertas ni excepciones arquitectónicas. Estado cambiado de `pending` a `spec_ready`; la implementación espera aprobación humana explícita.

## Feature 31 — especificación aprobada (2026-10-25)

- El usuario aprobó explícitamente la especificación de `environment-provider`.
- Aprobación registrada a las `2026-10-25T15:47:03+01:00`; estado cambiado de `spec_ready` a `in_progress`.
- Comienza la implementación ordenada de T1–T16; el cierre requiere gates completos y review independiente aprobada.

## Feature 31 — implementación iniciada (2026-10-25)

- T1–T3 iniciadas: modelo canónico, catálogo sellado y puerto provider-neutral añadidos en `internal/environment`.
- El workflow está migrándose de `PreviewPort` a un único `EnvironmentPort`; direct y preview compartirán adquisición, restauración y cleanup.
- El alcance continúa cerrado: `ExistingURLProvider` solo delega el lifecycle existente y el provider gestionado será exclusivamente un fake contractual.

## Feature 31 — implementación lista para review (2026-10-25)

- T1–T16 completadas: modelo/catálogo canónicos, lifecycle provider-neutral durable, adapter `ExistingURLProvider`, store CAS append-only y un único `EnvironmentPort` integrado en workflow/composición.
- Direct y preview usan la misma adquisición, revalidación y liberación; feature 19 conserva una sola reserva/release lógica y no se añadió infraestructura gestionada real.
- Gates verdes: formato, diff, vet, compile-only, focal repetido, suite global normal, race focal/alta, cross-build Windows/Linux/Darwin y runtime Linux del paquete nuevo.
- Coverage exacta de `internal/environment`: `610/704 = 86,647727 %`; coverage global: `86,0 %`.
- La race global se intentó tres veces: los paquetes afectados pasan, pero la ejecución conjunta conserva timeouts no deterministas preexistentes en `localvalidation`, `contextassembly`, `functionalflow` y `githubpr`; cada fallo aislado comprobado pasa.
- WSL2 no pudo crear VM por `ERROR_NO_SYSTEM_RESOURCES`; Alpine WSL1 verificó `internal/environment`, mientras suites históricas de workflow/cmd fallan en publicación durable bajo WSL1. Los nueve cross-builds sí pasan.

## Feature 31 — remediación de review 1 iniciada (2026-10-25)

- Reabiertas T1–T10, T12–T16 por REV-1–REV-10; T11 permanece cerrada.
- El alcance de remediación queda limitado a catálogo explícito, acquire incierto/release, store durable, leases, revalidación terminal, outcomes operacionales, contrato común, vertical de superficies, coverage completa y runtime Linux.
- `feature_list.json`, providers gestionados reales, Kubernetes y features posteriores permanecen fuera de alcance.

## Feature 31 — remediación de review 1, código y gates Windows completados (2026-10-25)

- REV-1–REV-4: la composición exige catálogo explícito sellado; acquire incierto deja obligación durable, reconcilia la misma identidad y libera con contexto derivado/finito; el store encadena receipts con head anti-rollback y publica record/event/head con sync; owner/lease usa CAS y adopción sin I/O provider duplicado.
- REV-5–REV-8: el entorno se revalida antes de ejecución y publicación; outcome primario y estado operacional se proyectan por separado; managed fake y `ExistingURLProvider` ejecutan la misma matriz completa; CLI/API/MCP/GitHub comparten un único `EnvironmentPort` para direct y preview.
- REV-9: `scripts/feature31_coverage.py` mide el diff productivo completo desde `6e8742f`, autentica ambos build tags y obtiene `1002/1160 = 86,379310 %` sin redondear.
- Gates Windows verdes: formato/diff, vet, compile-only, focales x3, global normal, race focal, environment race x20, race global completa y cross-builds Windows/Linux/Darwin 9/9.
- REV-10 permanece abierto únicamente mientras se recupera un runtime Linux soportado: WSL1 devuelve `ENOSYS` en el `renameat2(RENAME_NOREPLACE)` histórico de publicación; no se debilitó esa garantía ni se amplió scope. T1–T15 están cerradas y T16 sigue abierta hasta completar runtime Linux y limpieza final.

## Feature 31 — remediación de review 1 completada (2026-10-25)

- REV-10 se cerró con compatibilidad Linux mínima ante `ENOSYS`: `localvalidation` conserva publicación atómica/no-clobber para archivos y bundles no vacíos; `contractreview` usa `linkat/unlinkat` para no-replace y `renameat` solo para replacement explícito. Tests Linux específicos verifican que un destino confirmado no se sobrescribe.
- Runtime Alpine WSL1 verde con binarios Linux frescos: `internal/environment`, `internal/validationworkflow` y `cmd/featureproof` pasan 3/3; también pasan completas `internal/localvalidation` y `internal/contractreview`.
- Coverage exacta final del diff desde `6e8742f`, combinando profile Windows y statements Linux autenticados por hash: `1026/1191 = 86,146096 %`.
- Todos los gates obligatorios quedan verdes y T1–T16 vuelven a `[x]`. Los temporales y la distro efímera se eliminan antes de entregar; `feature_list.json` permanece intacto.

## Feature 31 — remediación de review 2 iniciada (2026-10-25)

- Reabiertas T4, T7, T8, T10, T15 y T16 por REV2-1–REV2-7; los hallazgos cerrados de reviews anteriores permanecen cerrados.
- Alcance limitado a double-uncertain acquire/release autoritativa, lease segura, promoción posterior a checkpoint, contrato común completo, fallback `ENOSYS` fail-closed o con claim atómico, coverage multiplataforma exacta y temporales verificados.
- `feature_list.json`, providers reales, Kubernetes y features posteriores permanecen fuera de alcance.

## Feature 31 — remediación de review 2 completada (2026-10-25)

- REV2-1–REV2-4: acquire doblemente incierto conserva authority y queda pending hasta reconcile autoritativo; leases cubren todos los boundaries; ejecución privada se promociona solo tras revalidación; la misma matriz durable corre contra existing y managed fake.
- REV2-5: los fallbacks Linux preservan no-clobber y recovery con claim descriptor-relative/flock para bundles y hard-link convergente para archivos; tests coordinados cubren crash, concurrencia, winner único y redelivery.
- REV2-6: coverage exacta del diff completo desde `6e8742f`, con columnas y profiles Windows/Linux autenticados: `1122/1313 = 85,453161 %`.
- Gates verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal, race focal/environment ×20/global UCRT64, Linux 5/5 y cross-builds 9/9. Un primer race global bajo cross-build concurrente falló aisladamente en `functionalflow`; el test ×3 y el global repetido sin carga pasaron.
- T4/T7/T8/T10/T15/T16 vuelven a `[x]`; temporales verificados se retiran y `feature_list.json` permanece intacto.

## Feature 31 — remediación de review 3 iniciada (2026-10-25)

- Reabiertas T4, T7, T15 y T16 por REV3-1–REV3-4; hallazgos cerrados anteriores permanecen cerrados.
- Alcance limitado a lease agregada, autenticación completa de reconcile de cleanup, matriz contractual durable idéntica y cobertura con toolchain/plataforma autenticados.
- `feature_list.json`, providers reales, Kubernetes y features posteriores permanecen fuera de alcance.

## Feature 31 — remediación de review 3 completada (2026-10-25)

- REV3-1: la lease cubre la cadena provider completa y usa aritmética fail-closed; TTL mayor que cada boundary pero menor/igual que la suma se rechaza sin I/O.
- REV3-2: reconcile de cleanup autentica provider/version/profile, handle, availability, capabilities, repo/commit, attestation y network; authority tampered permanece recoverable sin release.
- REV3-3: la misma matriz `LifecycleService` + `LocalStore`, sin branches por adapter, corre contra ExistingURLProvider y managed fake con cancel/deadline, faults/reopen, incertidumbre doble, tamper, recovery y concurrency.
- REV3-4: coverage fija Go 1.25.1 desde `go.mod`, autentica manifest de plataforma/hash y rechaza mismatch/layout incompatible; resultado fresco `1166/1364 = 85,483871 %`.
- Gates verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal/race UCRT64, matriz contractual race ×20, Linux 5/5 y cross-builds 9/9. T4/T7/T15/T16 vuelven a `[x]`; `feature_list.json` permanece intacto.

## Feature 31 — remediación de review 4 iniciada (2026-10-25)

- Reabiertas T2, T5, T10, T14 y T16 por REV4-1/REV4-2; los hallazgos anteriores permanecen cerrados.
- Alcance limitado a ligar template/profile/network policy de Selection con Observation antes de persistir/bind/release, y a budgets finitos de Network/Observer incluidos en la lease agregada.
- `feature_list.json`, providers reales, Kubernetes y features posteriores permanecen fuera de alcance.

## Feature 31 — remediación de review 4 completada (2026-10-25)

- REV4-1: template/profile/network policy y toda authority de Selection se validan antes de persistir lifecycle, bind y cleanup; tamper en acquire/reopen queda recoverable sin Release para ambos adapters.
- REV4-2: Network/Observer tienen budgets propios, child contexts con cause y participación explícita en la lease; callbacks bloqueados prueban timeout/cancel, join y no adopción.
- Gates Go 1.25.1 verdes: formato/comments/diff, vet, compile-only, focal ×3, global normal/race UCRT64, matriz contractual race ×20, Linux 5/5 y cross 9/9.
- Coverage pinned fresh: `1181/1384 = 85,332370 %`; T2/T5/T10/T14/T16 vuelven a `[x]`, temporales se retiran y `feature_list.json` permanece intacto.

## Feature 31 — remediación de review 5 iniciada (2026-10-25)

- Reabiertas T15 y T16 por REV5-1; la implementación productiva permanece sin cambios.
- El caso deadline de la matriz se coordina con `started` y expira exclusivamente el child budget de Acquire; budgets generales del fixture quedan holgados para I/O durable/observer.
- Se repetirán matriz normal/race altas, global default múltiple/serial/race, Linux y cross antes del cierre; `feature_list.json` permanece fuera de cambios.

## Feature 31 — remediación de review 5 completada (2026-10-25)

- REV5-1: cada escenario/adaptador de la matriz contractual crea fixture, context y budgets propios; cancel/deadline esperan el canal que confirma entrada en `Acquire`, y el timeout expira exclusivamente el child budget de esa frontera.
- La matriz común pasa 20/20 normal y 20/20 race; la auditoría del diff confirma que los tests feature 31 no añaden `time.Sleep`, `time.After` ni parent deadlines cortos. No se modificaron tests históricos para ocultar flakes.
- Gates Go 1.25.1 verdes: global default ×2, global serial, global race serial UCRT64, Linux repetido en 5/5 paquetes (`cmd/featureproof` ×3), y cross-builds 9/9.
- Coverage pinned fresh: `1181/1384 = 85,332370 %`; T15/T16 vuelven a `[x]`, los temporales propios se retiran y `feature_list.json` permanece intacto.

## Feature 31 — cierre aprobado (2026-10-25)

- La revisión independiente 6 aprobó el diff completo: R1–R15, T1–T16 y todos los hallazgos REV quedan cerrados.
- El smoke final del leader pasó en `environment`, `validationworkflow` y `cmd/featureproof`; `git diff --check` también pasó.
- Gates independientes: global normal repetido/serial, global race serial, matriz contractual normal ×30/race ×20, Linux 5/5 repetido y cross-builds 9/9.
- Cobertura fresca exacta y toolchain-pinned del diff productivo: `1182/1384 = 85,404624 %`.
- Feature 31 marcada `done`; queda commit y push de `feature-31-environment-provider`.

## Feature 32 — especificación iniciada (2026-10-26)

- Seleccionada únicamente `kubernetes-environment` (ID 32), en estado `pending` y con SDD obligatorio.
- No existe otra feature `in_progress`; los criterios de aceptación son suficientes para iniciar la especificación.
- Creada la rama `feature-32-kubernetes-environment` desde la feature 31 terminada, porque el provider de Kubernetes implementará su contrato `EnvironmentProvider`.
- Se delega la redacción de `requirements.md`, `design.md` y `tasks.md` al agente `spec_author`; no se escribirá código ni tests antes de la aprobación humana.

## Feature 32 — especificación preparada (2026-10-26)

- El spec author produjo `specs/kubernetes-environment/{requirements,design,tasks}.md` sin modificar código ni tests.
- La revisión del leader confirma 15 requirements verificables, 15 tasks trazables, separación de fallos operacionales y límites explícitos respecto a las features 33–36.
- `kubernetes-environment` pasa de `pending` a `spec_ready`; la implementación queda detenida hasta aprobación humana explícita.
