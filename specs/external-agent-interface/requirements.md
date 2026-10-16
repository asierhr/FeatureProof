# Feature 16 — Interfaz para agentes externos

## Objetivo

Un agente solicita una validación local bajo demanda, consulta su progreso y obtiene un resultado verificable por CLI o MCP sin entrar en el harness ni recibir secretos o rutas del host.

## Alcance y términos

- Se añade una API de aplicación versionada y dos adaptadores locales: comandos CLI con JSON y servidor MCP `2026-07-28` por stdio. La CLI `validate` existente conserva su contrato; los nuevos comandos usan el caso de uso compartido.
- **Principal/ámbito**: identidad autenticada por el sistema operativo y ámbitos concedidos por configuración local protegida, nunca por un campo de la petición. Dos procesos del mismo usuario del SO comparten identidad si la configuración no los separa; no se promete aislamiento entre procesos de ese usuario.
- **Handle**: identificador aleatorio opaco para run o evidencia. No concede acceso por posesión.
- **Solicitud**: selección de entradas previamente autorizadas por alias local, revisión y entorno; no admite rutas, URLs, comandos ni política de ejecución arbitrarios enviados por el agente.
- **Resultado**: proyección limitada y validada del paquete de evidencia de la feature 15. `failed` es incumplimiento funcional; `error` es fallo operativo.

## Fuera de alcance

HTTP remoto, GitHub, multi tenant entre usuarios con la misma identidad del SO, descarga de bytes de evidencia, ejecución de instrucciones de reproducción, cambio de approvals por MCP, y sustitución de los controles existentes de admisión, binding, política o presupuestos.

## Requirements EARS

### R1 — Solicitud y ciclo consultable

WHEN un principal con ámbito `runs:create` solicita una validación con entradas autorizadas y clave de idempotencia, THE interfaz SHALL reservar durablemente un run, devolver su handle y permitir consultar su estado y resultado desde CLI y MCP incluso tras reiniciar el adaptador.

- GIVEN una solicitud admitida, WHEN se consulta su handle, THEN se observa `queued` o `running` hasta un estado terminal; la transición y el resultado sobreviven al reinicio.
- GIVEN un run no terminal, WHEN se solicita resultado, THEN se devuelve `in_progress`, sin resultado parcial que parezca final.

### R2 — Caso de uso compartido

WHEN CLI o MCP invoca la misma operación, THE interfaz SHALL usar los mismos casos de uso de aplicación y DTO versionados, con igual estado, categorías de error y contenido semántico.

- GIVEN la misma petición y dependencias falsas, WHEN se invoca por ambas superficies, THEN la respuesta JSON de aplicación coincide tras excluir solo metadatos del transporte.

### R3 — Resultado seguro

WHEN un run termina y su paquete pasa `ReadPackage`, THE interfaz SHALL exponer restricciones, fallos, hechos, handles de evidencia y reproducción declarativa únicamente mediante la proyección permitida y acotada.

- GIVEN un paquete íntegro, WHEN se consulta resultado, THEN aparecen `status`, restricciones, fallos, hechos, referencias y datos de reproducción sin ruta de artefacto, target URL, credencial, cuerpo HTTP ni valor de texto libre.
- GIVEN paquete ausente, corrupto o incompatible, WHEN se consulta resultado, THEN no se emite resultado funcional y se devuelve error estructurado seguro.

### R4 — Admisión y controles

WHEN una operación crea o cancela trabajo, THE interfaz SHALL exigir identidad confiable, ámbito, autorización sobre selección y clave de idempotencia; SHALL pasar por las mismas puertas de binding, aprobación, política y presupuesto que la ejecución local aprobada.

- GIVEN selección fuera del catálogo o aprobación pendiente/rechazada, WHEN se solicita validación, THEN no se ejecuta ninguna herramienta y se comunica error o estado pendiente según la puerta existente.
- GIVEN `clientInfo` u otro campo controlado por el agente que afirma otro usuario, WHEN se invoca una operación, THEN la autorización no cambia.

### R5 — Aislamiento de handles

WHEN se consulta, cancela o recupera un run o evidencia, THE interfaz SHALL comprobar el principal y ámbito actuales contra la titularidad persistida antes de leer el paquete o divulgar metadatos.

- GIVEN un handle ajeno, desconocido o caducado, WHEN se consulta, THEN se obtiene la misma categoría pública `not_found` sin ruta ni existencia distinguible.
- GIVEN un handle de evidencia válido pero de otro run, WHEN se consulta el resultado, THEN se rechaza la referencia.

### R6 — Errores y resultados diferenciados

WHEN una operación no puede completarse, THE interfaz SHALL devolver un error tipado, de vocabulario cerrado y sin datos sensibles que distinga `invalid_request`, `unauthorized`, `not_found`, `conflict`, `in_progress` e `internal_error`; SHALL distinguir en el resultado terminal `failed` de `inconclusive`, `cancelled`, `passed` y `error`.

- GIVEN cada categoría y cada estado terminal en fakes, WHEN se invoca CLI o MCP, THEN `code`, `field` seguro y estado coinciden; un `failed` funcional es respuesta válida, no error de protocolo.

### R7 — Idempotencia durable

WHEN se repite una mutación con la misma clave, principal, operación y petición canónica, THE interfaz SHALL devolver el mismo handle/resultado sin duplicar ejecución; IF cambia la petición, THEN SHALL devolver `conflict`.

- GIVEN dos solicitudes concurrentes con una clave, WHEN ambas se admiten, THEN existe una sola reserva y una sola ejecución.
- GIVEN reinicio tras reservar, WHEN se reintenta, THEN se recupera la reserva original y no se crea otro run.

### R8 — Cancelación y timeout

WHEN se solicita cancelación autorizada, se agota el timeout o termina el proceso propietario, THE interfaz SHALL detener el trabajo hijo, esperar su salida y finalizar o recuperar durablemente el run sin ejecución ni publicación huérfanas.

- GIVEN una herramienta bloqueada y cancelación, WHEN se cancela, THEN recibe el contexto cancelado, la publicación se cierra o queda marcada incompleta y el estado terminal es visible.
- GIVEN caída tras `running`, WHEN el siguiente proceso abre el registro, THEN detecta la pérdida de propietario, marca `error`/interrupción de forma durable y no reejecuta automáticamente.

### R9 — Descubrimiento y versión

WHEN un agente consulta capacidades, THE interfaz SHALL publicar versión del contrato de aplicación, operaciones soportadas y esquemas; MCP SHALL admitir `server/discover`, `tools/list` y `tools/call` conforme a `2026-07-28` sin depender de sesión implícita.

- GIVEN un cliente MCP compatible, WHEN descubre el servidor, THEN ve la versión MCP y herramientas permitidas para sus ámbitos con esquemas de entrada/salida.
- GIVEN CLI, WHEN ejecuta `capabilities --json`, THEN obtiene la misma versión y operaciones de aplicación.

### R10 — Pruebas contractuales

WHEN se prueban las superficies, THE suite SHALL ejecutar los mismos escenarios mediante CLI y MCP con adapters falsos y verificar idempotencia, aislamiento, errores, cancelación y ausencia de secretos sin red ni modelo reales.

- GIVEN fixtures de dos principales, paquetes válidos/corruptos y operaciones concurrentes, WHEN corre `go test ./...`, THEN los contratos de ambas superficies pasan y trazan R1–R9.

## Casos límite

Claves vacías o demasiado grandes, JSON duplicado o con campos desconocidos, handle malformado, run en publicación, paquete sin manifest íntegro, scope revocado entre petición y lectura, cancelación repetida o posterior a terminal, colisión de handle, caída del proceso, timeout de finalización y cambios de revisión durante ejecución. Estos casos deben producir categorías seguras y nunca un `passed` inferido.

## Decisiones abiertas

Ninguna para el alcance local. La política de identidades entre usuarios del mismo SO y una futura API remota requerirán spec separada.

## Aprobación humana

```yaml
approved_by:
approved_at:
```
