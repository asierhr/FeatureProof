# Resultado de especificación — local-validation-cli

## Estado

Las decisiones bloqueantes fueron resueltas por el usuario y la especificación
completa está disponible en `specs/local-validation-cli/`.

Transición solicitada: `pending -> spec_ready`.

## Información disponible

- La feature requiere el comando `featureproof validate`, una specification y
  una opción explícita para el target autorizado.
- La CLI debe validar sus entradas antes del run, ejecutar un flujo completo
  determinista sin credenciales de un modelo real, mostrar progreso seguro,
  persistir los artefactos del run y devolver códigos de salida distinguibles.
- El contrato YAML v1 vigente contiene feature, un target, constraints y
  límites. Por decisión aprobada en `validation-specification`, no contiene
  planes ni assertions.
- `multi-step-validation-plan` define planes internos, pero deja fuera de su
  alcance el formato externo del plan y su incorporación al YAML.
- `assertion-engine` evalúa declaraciones estructuradas ya compiladas; no crea
  assertions a partir de la descripción de una constraint.
- No existe todavía un modelo real ni un productor determinista que convierta
  automáticamente cada constraint en un plan ejecutable.

## Decisión resuelta

El usuario confirmó que esta primera CLI recibe los planes y assertions en un
segundo archivo explícito y versionado mediante `--plan <ruta>`. El comando
también recibe `--spec <ruta>` y `--target <URL>`. El archivo declara un plan
por cada constraint que se pretenda validar; YAML v1 permanece intacto y la
generación mediante modelo queda fuera de alcance.

El usuario también confirmó el contrato de target, códigos de salida y
publicación atómica descrito en la versión anterior de este informe.

## Decisiones cerradas

El documento versionado recibido por `--plan` incluye una sección `runtime`
con la configuración confiable y obligatoria de las fronteras implementadas:

- `http.request` exige allowlists de métodos y headers, métodos de redirect,
  máximo de redirects, timeout y límites positivos de URL, headers y bodies;
- evidence redaction exige `max_body_bytes` y permite headers y paths sensibles;
- policy debe autorizar explícitamente la herramienta porque su comportamiento
  base es deny-by-default.

No se aplican defaults de producto salvo los cuatro headers protegidos ya
contractuales. El loader valida el documento completo antes de componer
capacidades o crear un run.

## Trazabilidad de la spec

- `requirements.md` define R1–R11 con criterios observables.
- `design.md` mapea cada requirement a componentes, pruebas y tasks.
- `tasks.md` contiene T1–T7, todas inicialmente pendientes y con tests junto al
  comportamiento correspondiente.
- No se modificaron código productivo, tests ni `feature_list.json`.
