# CLI local de validación — Design

## Resumen de la solución

Se añadirá un entrypoint pequeño en `cmd/featureproof` y un paquete de
aplicación `internal/localvalidation`. El entrypoint traduce argumentos y
señales; la aplicación carga ambos documentos, construye el target efectivo,
valida el manifiesto completo, compone las fronteras existentes, ejecuta los
planes por constraint y entrega un modelo terminal a un publicador atómico.

```text
argv -> parse -> load spec + plan manifest -> validate all
     -> generate run ID + create staging -> execute constraints sequentially
     -> aggregate -> render safe artifacts -> fsync/close -> atomic rename
```

No interviene un modelo. El manifiesto es el productor determinista del plan.

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Alcance único, criterios y puerta SDD. |
| `docs/{architecture,conventions,specs,verification}.md` | Separación entrypoint/aplicación, cancelación, auditabilidad, tests y trazabilidad. |
| `internal/specification` | YAML v1 permanece intacto; target y constraints internos. |
| `internal/planning` | Modelo, validación, orden, executor y resultados. |
| `internal/assertion` | Definiciones y evaluación determinista. |
| `internal/tools/http` | Configuración obligatoria y target-scoped. |
| `internal/evidence` | Policy, sanitizer, FileStore e integridad. |
| `internal/harness` | Run, estados, límites y journal. |
| Specs de features 1–6 | Contratos aprobados que esta composición no relaja. |
| `progress/local-validation-cli/spec.md` | Decisiones humanas sobre plan, target, códigos, runtime y publicación. |

No se consultaron contratos externos ni servicios remotos.

## Contratos externos

El binario acepta solo `validate --spec PATH --plan PATH --target URL`. El
parser no usa variables de entorno para completar valores. La raíz de salida
es el working directory y el parent fijo `.featureproof/runs`.

El manifiesto usa JSON v1 y DTOs privados del adapter. Un decoder estricto con
`UseNumber` y detección de duplicate keys/trailing data traduce a:

- configuración `httptool.Config` (más dependencias de infraestructura);
- `evidence.Config`;
- policy allowlist cerrada;
- `[]planning.ValidationPlan`.

Los DTOs representan la unión explícita de input para detectar cero o varias
variantes. Los errores exponen categoría, ruta de campo y causa segura.

stdout usa líneas estables `key=value`; listas de IDs se serializan como JSON
compacto para evitar separadores ambiguos. stderr contiene uso o diagnóstico
seguro. Los payloads tratados solo viven en evidence JSON, nunca en progreso o
Markdown.

## Componentes y responsabilidades

### `cmd/featureproof`

Define `main` y delega en un `RunCommand(ctx, args, stdin, stdout, stderr)` que
retorna el exit code. Instala el contexto de señales solo en `main`, lo que
permite tests sin señales del proceso.

### `internal/planmanifest`

Posee DTOs JSON v1, loader estricto, errores tipados y traducción a modelos/config
internos. Valida estructura, claves, números, runtime, tool allowlist,
correspondencia exacta de constraints y todos los planes. No crea directories,
no resuelve DNS y no ejecuta tools.

### `internal/localvalidation.Runner`

Coordina una invocación. Recibe factories de IDs/reloj, HTTP boundaries,
filesystem/publicador y output sink. Después de la validación genera el ID,
abre staging y crea un run de aplicación que comparte presupuesto entre los
planes de constraints. Ejecuta constraints secuencialmente y agrega resultados.

Aunque `PlanExecutor` recibe un `harness.Run` ligado a una constraint, la capa
de aplicación conserva una identidad de validación y crea la vista de ejecución
necesaria por constraint con el mismo run ID y un contador de consumo común o
un adaptador mínimo que impida reiniciar presupuestos. Los límites del YAML son
globales al comando, no por constraint. Cualquier refactor de harness será
mínimo y mantendrá compatibilidad con features anteriores.

### Composición de capacidades

El effective target copia `target.id` y usa el string de `--target` como
`BaseURL`. Se construyen redaction policy, sanitizer, assertion validator,
HTTP tool, registry y static policy antes del run. `allowed_tools` solo admite
`http.request`; omitirlo o repetirlo es error. La policy lo autoriza de forma
explícita y cualquier capacidad no registrada continúa denegada.

### `ArtifactPublisher`

Posee staging y publicación. Crea `.featureproof/runs/<id>.incomplete` con
permisos restrictivos y sin reemplazo. Evidence se escribe dentro de
`evidence/`. Al finalizar obtiene snapshots, valida referencias, serializa con
orden estable y escribe cada archivo mediante temp+rename dentro de staging.
Después cierra/sincroniza archivos y directorios y renombra staging a `<id>`.

Si falla, no crea el nombre final y deja `.incomplete`. Si final/staging existe,
devuelve colisión sin modificarlo. La abstracción de filesystem permite fallos
inyectados portables; la garantía de rename se limita al mismo parent.

### Reporting

`run.json` contiene metadata terminal y constraints; `events.jsonl` refleja el
journal por sequence; `plan.json` es una representación tratada y canónica del
manifiesto validado, incluida la configuración pero sin secretos (el contrato
no admite credenciales); evidence conserva records íntegros; `report.json`
contiene run status y resultados de constraints/steps/assertions con evidence
IDs; `report.md` presenta una tabla de constraint/status/evidence IDs.

El renderer Markdown escapa caracteres activos y omite descriptions, expected,
observed, inputs y outputs. JSON usa nombres `snake_case`, timestamps RFC3339
UTC y duraciones en milisegundos.

## Flujo principal

1. Parsear argumentos sin efectos.
2. Cargar/validar YAML y manifiesto desde rutas explícitas.
3. Sustituir BaseURL y validar todas las configuraciones y planes.
4. Generar ID y preparar staging después de completar toda la validación.
5. Registrar creación y emitir `run_id`, después `phase=executing`.
6. Ejecutar cada constraint en orden, persistir evidencia tratada y emitir su
   resumen seguro.
7. Agregar el resultado y cerrar el estado/journal una sola vez.
8. Emitir `phase=reporting`, renderizar el bundle y validarlo.
9. Renombrar staging atómicamente, emitir resultado final y retornar código.

## Flujos de error

- Parse/carga/config/plan inválidos: stderr seguro, exit 3, sin run ni staging.
- Error operativo representable por el executor: produce inconclusive y sigue
  las reglas funcionales existentes.
- Error interno de journal/evidence/application: detiene más ejecución, intenta
  cerrar como failed/error para diagnóstico y retorna 3; nunca se presenta como
  incumplimiento.
- Colisión: exit 3 y cero modificación del path existente.
- Fallo de publicación: final ausente, staging incomplete preservado, exit 3.
- Señal: cancela ejecución; finaliza con contexto separado y timeout inyectable.
  Si puede formar bundle terminal cancelled lo publica; si no, deja incomplete;
  en ambos casos retorna 130.

## Estado, concurrencia y cancelación

`Runner` es propietario del estado mutable de una invocación. No hay ejecución
paralela. Journal y evidence se particionan por run ID. Invocaciones distintas
pueden correr si sus IDs no colisionan.

El contexto de señal se propaga sin sustitución a policy, resolver, dialer,
tool, sanitizer, store y evaluator. Al cancelarse no comienza otra unidad. La
finalización usa `context.WithoutCancel` más un timeout explícito provisto por
factory; solo escribe datos ya tratados/confirmados y no ejecuta tools.

## Retries e idempotencia

Los retries permanecen bajo `PlanExecutor` y presupuesto YAML. La CLI no repite
automáticamente comandos, constraints, HTTP ni publicación. La persistencia es
idempotente por IDs y fail-on-exists. Repetir manualmente el comando crea otro
run ID y bundle.

## Observabilidad y seguridad

Fases, IDs, estados, categorías y evidence IDs son observables. URLs, inputs,
outputs, headers, bodies, expected y observed no se escriben en stdout/stderr
ni Markdown. Los errores no interpolan documentos ni valores. Los cuatro
headers sensibles siguen protegidos aunque la lista adicional esté vacía.

## Paquetes afectados

```text
cmd/featureproof/main.go
internal/planmanifest/       # JSON v1 y traducción estricta
internal/localvalidation/    # runner, agregado, reporting y publicación
internal/harness/            # ajuste mínimo si hace falta compartir presupuesto
```

Las features previas solo se modifican donde la composición requiera un
contrato mínimo; no se duplican executor, assertion engine, HTTP ni sanitizer.

## Convención de documentación del código

Toda entidad y toda función nueva o modificada, exportada o interna, incluirá
un comentario breve que explique su responsabilidad dentro de FeatureProof y
su relación con el flujo o frontera correspondiente. El comentario expresa
intención, invariantes o motivo arquitectónico, no repite la sintaxis. También
se actualizan comentarios existentes cuya responsabilidad cambie.

## Estrategia de pruebas

Los tests unitarios cubren parser, manifest loader, agregado, renderers y
publisher. Los tests de comando inyectan argv, streams, señal, ID, reloj,
filesystem y transporte local. La integración completa usa `httptest` y tmpdir,
escanea canarios en todas las superficies y verifica contenido/referencias del
bundle. No usa Internet, procesos hijos, señales reales, modelo ni sleeps.

| Requirement | Tests previstos |
|---|---|
| R1 | `TestValidateCommandRequiresExactFlagsBeforeEffects` |
| R2 | `TestExplicitTargetReplacesSpecificationURLAndScopesHTTP` |
| R3 | `TestManifestV1StrictlyValidatesRuntimeAndPlansBeforeRun`; `TestManifestPreservesJSONNumbers` |
| R4 | `TestRunnerRequiresOneOrderedPlanPerConstraint`; `TestRunnerExecutesConstraintsSequentiallyWithoutModel` |
| R5 | `TestProgressContainsOnlySafeRunConstraintStatusAndEvidenceIDs`; `TestCanaryNeverAppearsInCLIOrArtifacts` |
| R6 | `TestConstraintAndRunAggregationUsesDeterministicPrecedence` |
| R7 | `TestPublisherCommitsCompleteSelfConsistentBundleAtomically`; `TestPublisherRejectsCollisionWithoutOverwrite` |
| R8 | `TestPublisherFailureLeavesOnlyIncompleteDiagnosticDirectory` |
| R9 | `TestCommandExitCodesForPassedFailedInconclusiveErrorAndSignal` |
| R10 | `TestSignalCancellationStopsWorkAndPublishesCancelledBundle`; `TestCancellationFinalizationFailureDoesNotPublishPartialRun` |
| R11 | `TestEndToEndValidationUsesOnlyControlledLocalBoundaries`; race suite |

## Alternativas y decisiones

- Ampliar YAML v1: descartado; rompe su contrato aprobado.
- Planificador falso incorporado: descartado; ocultaría cómo se derivan
  assertions. El manifiesto declara el plan completo.
- Defaults HTTP/redaction: descartados; las fronteras previas exigen decisiones
  explícitas.
- URL YAML sobre flag: descartado; el usuario aprobó que `--target` sustituya
  BaseURL.
- Escribir directamente al directorio final: descartado; expondría bundles
  parciales.
- Eliminar staging fallido: descartado; se aprobó conservarlo como incomplete.
- Continuar después de error interno: descartado; podría producir un reporte
  engañoso. Solo resultados funcionales permiten continuar.

## Conformidad arquitectónica

El diseño conserva ARCH-002/003/004 al reutilizar registry, policy, evidence y
assertions; ARCH-005 con límites globales; ARCH-006 con contexto; ARCH-007 con
journal; ARCH-009 con saneamiento previo; y ARCH-010 con tests herméticos. No
se solicita excepción.

## Mapa inicial R → tasks

| Requirement | Tasks |
|---|---|
| R1 | T1, T7 |
| R2 | T2, T3, T7 |
| R3 | T2, T3, T7 |
| R4 | T2, T4, T7 |
| R5 | T4, T6, T7 |
| R6 | T4, T5, T7 |
| R7 | T5, T6, T7 |
| R8 | T6, T7 |
| R9 | T1, T4, T7 |
| R10 | T4, T6, T7 |
| R11 | T1, T2, T3, T4, T5, T6, T7 |
