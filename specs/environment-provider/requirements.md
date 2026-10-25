# Abstracción de obtención de entornos — Requirements

## Objetivo y problema

El workflow actual distingue targets directos y previews mediante contratos
específicos, y conoce detalles del ciclo de reserva de `previewenv`. Las futuras
integraciones gestionadas necesitarán crear o reservar infraestructura, pero el
harness no debe conocer proveedores, credenciales ni operaciones de
provisioning.

Esta feature introduce un contrato propio y provider-neutral para seleccionar,
adquirir, verificar, restaurar y liberar un entorno de validación. El workflow
recibe únicamente un `ValidationTarget` sellado y un handle opaco de lifecycle.
El primer adapter productivo, `ExistingURLProvider`, reutiliza el coordinador de
previews existente de la feature 19 sin relajar ni duplicar sus reglas de
atestación, red, idempotencia o release. Un provider gestionado falso permite
probar el contrato; Kubernetes y cualquier provider gestionado real permanecen
fuera de alcance.

## Alcance

- Catálogo confiable y versionado de selecciones de provider, template o
  referencia, perfil, policy de red y referencia opaca de credencial.
- Modelo canónico `ValidationTarget/v1` ligado a proyecto/repositorio, commit,
  environment, atestación, endpoint autorizado, capacidades y lifecycle.
- Puerto mínimo `EnvironmentProvider` para adquisición, observación/readiness,
  reconciliación y liberación idempotentes.
- Caso de uso provider-neutral que persiste intentos y estados de lifecycle,
  aplica budgets y traduce fallos operativos sin alterar el veredicto funcional.
- `ExistingURLProvider` como adapter sobre el lifecycle ya aprobado de
  `previewenv`, incluida restauración tras restart y release lógico.
- Fake gestionado hermético que demuestra adquisición y cleanup sin SDK, red o
  infraestructura real.
- Revalidación de identidad, revisión, atestación, target y policy antes de
  planificación/approval cuando corresponda, ejecución, recovery y publicación.
- Integración común para CLI, API, MCP y GitHub a través del mismo servicio.

## Fuera de alcance

- Implementar Kubernetes, namespaces, workloads, manifests, imágenes, quotas,
  service accounts, volúmenes, ingress o network policies de Kubernetes; es la
  feature 32.
- Implementar providers de Terraform, cloud, PaaS, CI/CD o cualquier API real de
  infraestructura.
- Diseñar la configuración versionada completa del proyecto; la feature recibe
  un catálogo ya aprobado y su digest.
- Aceptar manifests, comandos, URLs, provider IDs, templates, credenciales o
  capacidades producidos por el modelo o por contenido de repositorio/PR.
- Sustituir o reimplementar las reglas de `previewenv` para reserva, readiness,
  atestación, red, recovery o release.
- Exponer el lifecycle handle al modelo, herramientas, planes, evidencias,
  packages o reportes.
- Convertir un fallo operacional de adquisición/cleanup en un assertion failure
  o en un veredicto funcional `failed`.

## Actores y términos

- **Environment selection:** alias sellado de catálogo que resuelve provider,
  kind, template/reference, perfil, policy y credencial opaca.
- **EnvironmentProvider:** adapter composition-owned que conoce una familia de
  entornos y devuelve observaciones provider-neutral.
- **ExistingURLProvider:** adapter que delega en el coordinador `previewenv`
  existente para targets ya desplegados o reservables, sin provisionarlos.
- **Managed fake:** provider hermético usado para demostrar el contrato de un
  lifecycle gestionado; no representa un provider productivo.
- **ValidationTarget:** target seguro y canónico que el workflow puede entregar
  al executor después de binding y policy.
- **Lifecycle handle:** identidad opaca, no secreta y sellada que permite
  reconciliar y liberar la misma adquisición; no concede por sí sola acceso.
- **Acquisition:** intento idempotente de obtener o reservar un entorno para un
  run y revisión exactos.
- **Readiness observation:** observación acotada del target adquirido; no ejecuta
  comprobaciones funcionales de la feature.
- **Release intent:** obligación durable e idempotente de liberar la adquisición
  cuando el run termina, se cancela o falla.
- **Operational outcome:** resultado del lifecycle (`unavailable`, `timeout`,
  `cancelled`, `cleanup_pending`, etc.) separado del veredicto funcional.

## R1 — Selección explícita y autoridad de composición

WHEN un run requiere un entorno, THE environment service SHALL resolver una sola
selección sellada desde un catálogo composition-owned y SHALL NOT aceptar
provider, kind, template/reference, endpoint, policy, capacidades o credenciales
aportados por el modelo, el repositorio o payloads de las superficies.

### Acceptance criteria

- GIVEN un alias configurado WHEN se admite el run THEN resuelve exactamente un
  provider y una versión de selección.
- GIVEN alias ausente, duplicado, digest alterado o provider no registrado WHEN
  se valida THEN no se llama a ningún provider.
- GIVEN payload/modelo que intenta cambiar provider, template, endpoint o
  capabilities WHEN se procesa THEN no modifica la selección sellada.
- GIVEN una selección sin provider explícito WHEN se valida THEN falla; no hay
  provider, template ni modelo por defecto.

## R2 — Contrato provider-neutral mínimo

THE feature SHALL definir un `EnvironmentProvider` consumer-owned con operaciones
cancelables de acquire/reconcile, readiness/observe y release, usando únicamente
DTOs internos acotados y sin exponer SDKs, transportes, credenciales o detalles
de infraestructura al core o al workflow.

### Acceptance criteria

- GIVEN ExistingURLProvider y managed fake WHEN ejecutan la suite contractual
  THEN satisfacen las mismas reglas de identidad, idempotencia, readiness,
  binding, cancelación y release.
- GIVEN inspección del interface WHEN se revisa THEN no contiene operaciones
  arbitrarias, manifests, comandos, shell, clientes cloud ni secret resolver.
- GIVEN un DTO externo del adapter WHEN cruza la frontera THEN se traduce a un
  tipo interno y el DTO no aparece en packages consumidores.

## R3 — ValidationTarget canónico y seguro

WHEN una adquisición queda ready, THE service SHALL producir un
`ValidationTarget/v1` canónico e inmutable ligado a run, selección, provider,
proyecto, repositorio, commit completo, environment, atestación, endpoint,
policy de red, capacidades allowlisted y lifecycle handle digest.

### Acceptance criteria

- GIVEN los mismos valores WHEN se sellan en distinto orden THEN producen los
  mismos bytes y digest.
- GIVEN enum/versión desconocida, ID/OID/path inválido, campo duplicado, target
  no canónico, capability no permitida, attestation expirada o digest alterado
  WHEN se verifica THEN se rechaza antes del workflow.
- GIVEN mutación del input después de seal WHEN se observa el target THEN no
  cambia.
- GIVEN target proyectado al harness/modelo THEN excluye credenciales, provider
  payload, APIs y lifecycle handle raw.

## R4 — Adquisición, readiness y binding acotados

WHEN se adquiere un entorno, THE service SHALL ejecutar acquire/reconcile,
readiness y binding dentro de budgets positivos y finitos, SHALL publicar el
target solo tras verificar identidad, revisión, atestación y policy de red, y
SHALL NOT iniciar planificación ni tools antes de readiness confirmado.

### Acceptance criteria

- GIVEN pending seguido de ready WHEN queda presupuesto THEN el workflow recibe
  un único target sellado y continúa una vez.
- GIVEN unavailable, unhealthy, denied, mismatch, timeout o budget agotado WHEN
  se observa THEN no se llama al modelo ni a tools.
- GIVEN endpoint denegado WHEN se autoriza THEN no se abre DNS/socket y la
  adquisición entra en cleanup.
- GIVEN readiness que cambia environment/repository/commit/attestation/endpoint
  WHEN se compara THEN se clasifica como drift y no se publica el target.

## R5 — Adapter ExistingURL sin duplicar previewenv

WHERE la selección usa `existing_url`, THE ExistingURLProvider SHALL adaptar la
reserva/restauración/readiness/release ya selladas por `previewenv` y SHALL NOT
reimplementar ni relajar sus reglas no destructivas, de atestación, red,
retries, ownership o recovery.

### Acceptance criteria

- GIVEN un preview válido de feature 19 WHEN se adquiere THEN el adapter conserva
  provider/environment/reservation, repository/commit, fingerprint, attestation
  y policy efectivos del binding existente.
- GIVEN target directo configurado o preview reservado WHEN se adapta THEN no se
  crea, actualiza, escala, reinicia ni destruye infraestructura.
- GIVEN restart WHEN se restaura THEN delega en la autoridad durable actual y no
  confía en un binding aportado por el caller.
- GIVEN release WHEN termina THEN se ejecuta una sola liberación lógica del
  coordinator existente; no hay doble cleanup por el adapter y el workflow.

## R6 — Lifecycle opaco para providers gestionados

WHERE un provider gestionado está registrado, THE service SHALL permitir que el
adapter adquiera y libere mediante un lifecycle handle opaco y SHALL entregar al
harness solo el `ValidationTarget`, la identidad/revisión, capabilities seguras
y el digest del lifecycle.

### Acceptance criteria

- GIVEN el managed fake WHEN adquiere THEN su estado interno/transport no aparece
  en target, plan, events, evidence, package o report.
- GIVEN inspección del harness WHEN se revisa THEN no puede llamar a operaciones
  del provider ni interpretar el handle.
- GIVEN handle tampered/cross-run/cross-provider WHEN se reconcilia o libera THEN
  falla cerrado sin operar sobre otro entorno.
- GIVEN provider real no registrado WHEN se selecciona THEN falla antes de
  efectos; no existe fallback al fake o ExistingURLProvider.

## R7 — Revalidación y drift antes de efectos

WHEN existe un binding de entorno, THE workflow SHALL reconsultar la autoridad
composition-owned y verificar selección, provider, lifecycle, environment,
repositorio, commit, atestación, endpoint, capabilities y network policy antes de
ejecutar, recuperar o publicar; IF existe drift, THEN SHALL bloquear nuevos
efectos y conservar cleanup pendiente.

### Acceptance criteria

- GIVEN drift de cualquiera de los campos sellados WHEN se revalida THEN el
  binding queda stale/integrity y no se llaman nuevas tools.
- GIVEN restart con nueva composición WHEN se revalida THEN usa provider,
  catálogo, attestor y policies actuales, no closures del snapshot histórico.
- GIVEN solo cambia metadata derivada no autoritativa WHEN se revalida THEN no se
  invalida el target.
- GIVEN atestación expira entre batches WHEN se detecta THEN se detiene trabajo
  nuevo y se inicia release sin convertirlo en failure funcional.

## R8 — Release durable, idempotente y recuperable

WHEN una adquisición puede haber producido un entorno o reserva, THE service
SHALL persistir un release intent antes de intentar cleanup, SHALL reutilizar la
misma idempotency key y SHALL reconciliar resultados inciertos hasta confirmar
released o mantener `release_pending` recuperable.

### Acceptance criteria

- GIVEN success, functional failure, operational error, rejection, cancelación o
  timeout después de acquire WHEN termina el run THEN existe release intent.
- GIVEN response lost durante acquire/release WHEN recovery ejecuta THEN
  reconcilia antes de repetir y no duplica entorno ni liberación.
- GIVEN crash antes/después de cada boundary WHEN otro owner recupera THEN no
  repite ejecución funcional confirmada y converge el cleanup.
- GIVEN release agotado WHEN se proyecta estado THEN permanece operational
  `cleanup_pending`; no declara released ni cambia el veredicto funcional.

## R9 — Cancelación, timeouts, retries y concurrencia

THE environment lifecycle SHALL propagar `context.Context` y su cause por
catálogo, acquire, reconcile, readiness, attestation, network authorization,
store, observer y release; SHALL limitar attempts/duración/espera; y SHALL
permitir progreso de runs independientes sin locks durante red, disco, timers o
callbacks.

### Acceptance criteria

- GIVEN cancelación o deadline en cada fase WHEN ocurre THEN se distingue
  `cancelled` de `timeout`, termina trabajo propio y se intenta release con un
  contexto independiente pero finito.
- GIVEN error transitorio/rate limit WHEN queda presupuesto THEN se reintenta con
  clock/waiter inyectados; errores permanentes no se reintentan.
- GIVEN dos runs o redeliveries concurrentes WHEN se ejecutan bajo `-race` THEN
  progresan/convergen sin goroutines, timers, leases o locks filtrados.
- GIVEN espera posterior al deadline WHEN se calcula THEN no se inicia.

## R10 — Credenciales confinadas al adapter

THE composition SHALL resolver credenciales solo al construir el transporte del
provider seleccionado, y THE system SHALL NOT incluir secretos, resolvers,
headers, connection strings o provider payloads en selección, lifecycle handle,
target, plan, modelo, events, evidence, package, journal o report.

### Acceptance criteria

- GIVEN perfil secretless WHEN se compone THEN no se resuelve ninguna credencial.
- GIVEN provider autenticado WHEN opera THEN solo su transporte recibe el canario
  y el material se libera con la composición.
- GIVEN error remoto o secret detector fallido/cancelado WHEN cruza la frontera
  THEN no expone body/header/secreto y falla cerrado.
- GIVEN escaneo byte-for-byte de superficies durables/model-facing WHEN termina
  THEN no contiene canarios.

## R11 — Resultados operativos separados del veredicto

IF adquisición, readiness, revalidación o release falla, THEN THE workflow SHALL
registrar un outcome operacional tipado y SHALL NOT presentarlo como assertion
incumplida ni como veredicto funcional `failed`.

### Acceptance criteria

- GIVEN fallo antes de admisión WHEN se proyecta THEN el run termina `error` o
  `inconclusive` según la categoría, sin resultado funcional inventado.
- GIVEN cancelación WHEN se proyecta THEN termina `cancelled` con cause.
- GIVEN fallo de release después de un veredicto determinista WHEN se consulta
  THEN conserva el veredicto primario y muestra cleanup pendiente separado.
- GIVEN assertion real incumplida sobre target válido WHEN se evalúa THEN sigue
  produciendo el veredicto funcional `failed`.

## R12 — Persistencia y observabilidad content-free

THE service SHALL persistir transiciones y emitir records terminales exactamente
una vez usando solo IDs opacos, digests, estados, attempts, durations y categorías
allowlisted, sin endpoint completo, lifecycle raw, secretos o payloads externos.

### Acceptance criteria

- GIVEN cada transición relevante WHEN se confirma THEN el journal append-only
  permite recovery y rechaza tampering/rollback/conflicto.
- GIVEN redelivery o respuesta perdida WHEN se observa THEN no duplica el record
  ni acepta contenido distinto bajo la misma identidad.
- GIVEN observer lento/fallido/cancelado WHEN ocurre THEN no muta el target,
  binding, release intent ni veredicto; el fallo queda tipado.
- GIVEN eventos/logs/metrics WHEN se inspeccionan THEN contienen solo allowlist y
  nunca target URL, attestation raw o mensajes remotos.

## R13 — Integración única en workflow y superficies

WHEN CLI, API, MCP o GitHub inicia un run autorizado, THE validation workflow
SHALL adquirir el target mediante un único `EnvironmentPort`, SHALL entregar al
executor la misma proyección sellada y SHALL NOT mantener caminos paralelos
`direct`/`preview` que omitan lifecycle, binding o policy.

### Acceptance criteria

- GIVEN la misma selección y autoridad WHEN entra por cuatro superficies THEN
  produce el mismo target/binding digest y transiciones.
- GIVEN payload que intenta aportar target o lifecycle WHEN entra por cualquier
  superficie THEN se rechaza antes del provider.
- GIVEN ejecución/recovery WHEN se inspecciona THEN el workflow conoce solo el
  port y `ValidationTarget`; no hace type switch por provider.
- GIVEN migración del preview existente WHEN se prueba THEN no se adquiere ni se
  libera dos veces por conservar simultáneamente el camino `PreviewPort`.

## R14 — Suite contractual reutilizable

THE implementation SHALL ejecutar la misma suite contractual de selección,
acquisition, readiness, binding, drift, cancelación, release, redelivery y
recovery contra ExistingURLProvider y un managed fake hermético.

### Acceptance criteria

- GIVEN ambos providers WHEN corre la suite THEN los mismos casos y assertions
  observables pasan sin ramas especiales del test.
- GIVEN el managed fake WHEN se audita THEN no usa red, SDK, shell, filesystem de
  infraestructura ni credenciales reales.
- GIVEN ExistingURLProvider WHEN se audita THEN demuestra delegación a
  `previewenv` y cero operaciones de provision/deploy/update/delete.
- GIVEN race/repeated runs WHEN se ejecutan THEN el contrato permanece estable.

## R15 — Verificación y trazabilidad

THE implementation SHALL mantener trazabilidad `R→task→test`, cubrir al menos
85% statement coverage del código nuevo/modificado sin redondear y superar
formato, análisis, suites globales/repetidas/race, runtime Linux y cross-builds
aplicables sin servicios reales.

### Acceptance criteria

- GIVEN `go test` WHEN se ejecuta THEN no contacta provider, cloud, Kubernetes,
  GitHub, modelo ni red externa reales.
- GIVEN todo símbolo nuevo/modificado WHEN se audita THEN entidades, constantes,
  variables, funciones, métodos y helpers de test tienen comentario de
  responsabilidad sistémica.
- GIVEN build tags/plataformas afectadas WHEN se mide THEN se incluyen en
  coverage/gates o se contabilizan conservadoramente con evidencia.
- GIVEN inspección final WHEN termina THEN no hay temporales, secretos ni cambios
  de features 32+.

## Casos límite y errores

- IDs, aliases y handles vacíos, oversized, no canónicos, duplicados o con path
  semantics se rechazan antes de I/O.
- Provider nil/missing/version mismatch y selección cruzada son `invalid` o
  `unauthorized`, no fallback.
- Snapshot/target con enum, capability, fingerprint o relación inválida es
  `integrity` aunque su digest haya sido recalculado.
- Respuesta incierta se reconcilia con la misma key; nunca se genera una segunda
  adquisición por conveniencia.
- Release puede quedar pendiente, pero nunca se declara exitoso por timeout o
  cancelación.
- Error de observer no borra target/binding confirmado ni release intent.
- Context cause se conserva con `errors.Is`; su texto no se filtra a surfaces.
- Un target aprobado sigue sujeto a controles DNS/dial/redirect de la política
  de red existente en cada operación.

## Decisiones abiertas

No hay decisiones de producto abiertas para implementar esta abstracción. El
catálogo, los límites numéricos y las policies concretas se reciben como
configuración confiable. Los contratos productivos de Kubernetes u otros
providers se decidirán en sus features; aquí solo se fija el puerto neutral y se
demuestra con un fake gestionado.

## Aprobación humana

```yaml
status: pending
approved_by:
approved_at:
```

La implementación no puede comenzar hasta aprobación humana explícita y cambio
del estado a `in_progress` por el leader.
