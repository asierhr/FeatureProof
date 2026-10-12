# Herramienta HTTP controlada — Requirements

## Objetivo y problema

FeatureProof necesita obtener evidencia HTTP de una aplicación desplegada sin
convertir una propuesta del modelo en acceso de red arbitrario. La herramienta
`http.request` debe recibir una petición estructurada, limitarla al target que
el run autorizó fuera del prompt y devolver una observación acotada que el
harness pueda sanear, persistir y evaluar.

## Alcance

- Una herramienta HTTP registrable mediante los contratos actuales de
  `harness.ToolSpec` y `harness.Tool`.
- Configuración explícita, externa al input propuesto, de métodos, headers,
  redirects, timeout y límites de tamaño.
- Resolución segura de paths y redirects dentro de la autoridad y path base del
  `specification.Target` autorizado.
- Resolución DNS acotada a la operación y conexión exclusiva a las direcciones
  resueltas para ese target.
- Peticiones con query, headers no sensibles y body JSON estructurados.
- Resultados estructurados y acotados para respuestas HTTP y errores
  operativos.
- Clasificación conservadora de efectos e idempotencia para integrarse con el
  ejecutor y su política de retries.
- Tests con servidores, resolver, reloj y transporte locales controlados.

## Fuera de alcance

- Descubrir targets desde el modelo, OpenAPI o DNS de servicio.
- Aceptar URLs absolutas, proxies, Unix sockets o protocolos distintos de HTTP
  y HTTPS.
- Inyectar credenciales, cookies o certificados de cliente.
- Implementar redacción configurable de cuerpos o campos JSON; corresponde a
  `evidence-redaction`.
- Persistir evidencias, mutar el run, evaluar assertions o calcular veredictos.
- Implementar retries dentro del adapter HTTP.
- Definir defaults globales de métodos, puertos, tamaños, timeout o redirects.
- Ejecutar tests contra Internet o servicios compartidos.

## Actores y términos

- **Caller confiable:** composición de FeatureProof que construye la
  herramienta a partir del target del run y de permisos explícitos.
- **Input propuesto:** mapa no confiable originado en un plan. No puede ampliar
  la configuración de la herramienta.
- **Target autorizado:** `specification.Target.BaseURL`, fijado fuera del
  prompt. Su esquema, hostname, puerto efectivo y path base forman el ámbito de
  destino.
- **Autoridad:** hostname normalizado y puerto efectivo (`80` para HTTP y `443`
  para HTTPS cuando no se declara otro).
- **Path base:** path normalizado de `BaseURL`; una petición y cada redirect
  deben permanecer en él o en uno de sus descendientes por segmentos.
- **Header sensible:** `Authorization`, `Cookie`, `Set-Cookie` o
  `Proxy-Authorization`, comparado sin distinguir mayúsculas.
- **Error operativo:** fallo de transporte, DNS, TLS, timeout, límites o
  validación de un redirect. Un status HTTP 4xx/5xx es una respuesta observada,
  no un error operativo.

## Contrato de input v1

El input de `http.request` es un objeto con estas claves y no admite claves
desconocidas:

| Campo | Obligatorio | Forma |
|---|---:|---|
| `method` | sí | string en mayúsculas incluido en la allowlist configurada |
| `path` | sí | string de path relativo; no contiene esquema, autoridad, query ni fragment |
| `query` | no | objeto `string -> array de string` |
| `headers` | no | objeto `string -> array de string` |
| `body` | no | cualquier valor JSON estructurado válido, incluido `null` |

El path `/health` y `health` significan ambos un descendiente `health` del path
base. Se rechazan segmentos `.` o `..`, backslashes, un prefijo `//`, bytes de
control y separadores codificados que pudieran cambiar la jerarquía al
decodificarse. Query y headers solo se incorporan desde sus campos
estructurados. Si existe `body`, se codifica una vez como JSON antes de abrir
la conexión.

## Requirements

### R1 — Validación estructural antes de red

WHEN `http.request` receives an input,
THE HTTP tool SHALL validate its exact v1 shape, method, path, query, headers
and JSON body before resolving DNS or opening a connection.

#### Acceptance criteria

- GIVEN un input con las cinco clases de datos válidas
  WHEN se valida
  THEN queda listo para construir una única petición sin reinterpretar strings
  como URL, query o JSON crudo.
- GIVEN un campo obligatorio ausente, tipo incorrecto, clave desconocida,
  número JSON no finito, nombre/valor de header inválido o método fuera de la
  allowlist
  WHEN se valida
  THEN devuelve un error tipado con field path y no consulta resolver,
  transporte ni servidor.
- GIVEN `body: null` explícito
  WHEN se construye la petición
  THEN se envía el literal JSON `null`; la ausencia de `body` no envía body.

### R2 — URL derivada exclusivamente del target

WHEN a validated HTTP input is converted to a request,
THE HTTP tool SHALL derive its URL from the authorized target and the relative
path and structured query, without accepting an input-controlled absolute URL.

#### Acceptance criteria

- GIVEN target `https://example.test/api` y path `/health`
  WHEN se construye la petición
  THEN la URL resultante conserva HTTPS, autoridad `example.test` y path
  `/api/health`.
- GIVEN un path con autoridad, esquema, fragment, query embebida, backslash,
  dot-segment o separador codificado
  WHEN se valida
  THEN se rechaza antes de red.
- GIVEN un target con esquema distinto de HTTP/HTTPS o con puerto ausente,
  inválido o fuera de rango
  WHEN se construye la herramienta
  THEN la construcción falla y no existe herramienta ejecutable.

### R3 — Destino, DNS y conexión acotados

WHEN the HTTP tool prepares a connection,
THE HTTP tool SHALL connect only to the authorized scheme, normalized hostname
and effective port, using an operation-local set of IP addresses resolved for
that exact hostname.

#### Acceptance criteria

- GIVEN un input que no contiene un destino
  WHEN se ejecuta
  THEN hostname, puerto y esquema proceden únicamente del target configurado.
- GIVEN un hostname
  WHEN comienza la operación
  THEN se resuelve mediante el resolver inyectado, se fija el conjunto de IPs
  para toda la operación y cada dial usa una IP de ese conjunto con el puerto
  autorizado.
- GIVEN que un dial o redirect intenta otra autoridad, otro puerto o una IP no
  fijada
  WHEN se comprueba el destino
  THEN se rechaza antes de esa conexión con causa `destination_denied`.
- GIVEN un target explícito con IP privada, loopback o link-local
  WHEN coincide exactamente con la autoridad autorizada
  THEN no se rechaza solo por su clase de IP; estos targets son necesarios para
  entornos aislados y tests. La protección consiste en impedir que el input o
  un redirect cambien el target y en fijar la resolución durante la operación.
- GIVEN variables de entorno de proxy configuradas
  WHEN se ejecuta
  THEN no se consultan ni se usa un proxy implícito.

### R4 — Redirecciones revalidadas y acotadas

IF an HTTP response requests a redirect,
THEN THE HTTP tool SHALL validate the new URL against the complete target scope
before following it and SHALL enforce the configured redirect limit and method
allowlist.

#### Acceptance criteria

- GIVEN un redirect al mismo esquema, autoridad, puerto y path base
  WHEN quedan redirects y el método resultante está permitido para seguimiento
  THEN se sigue y se vuelve a aplicar la validación en el siguiente hop.
- GIVEN un redirect que cambia esquema, hostname o puerto, escapa del path base,
  incluye userinfo, usa una IP no fijada o produce un método no permitido
  WHEN se recibe
  THEN no se abre la conexión de destino y se devuelve
  `redirect_denied`.
- GIVEN más hops que `max_redirects`
  WHEN se alcanza el límite
  THEN la operación termina con `redirect_limit` sin seguir otro hop.
- GIVEN un método no incluido en `follow_redirects_for_methods`
  WHEN recibe un 3xx
  THEN devuelve esa respuesta 3xx como observación terminal sin ejecutar el
  redirect.

### R5 — Timeout obligatorio y cancelación

WHILE an HTTP operation is pending,
THE HTTP tool SHALL honor caller cancellation and an independently configured,
positive operation timeout.

#### Acceptance criteria

- GIVEN timeout no positivo
  WHEN se construye la herramienta
  THEN la configuración se rechaza.
- GIVEN que vence el timeout propio mientras el contexto padre sigue activo
  WHEN la operación retorna
  THEN produce un error operativo `timeout`, clasificado transitorio y
  distinguible de la cancelación del run.
- GIVEN que se cancela o vence el contexto padre antes
  WHEN la operación está resolviendo, conectando, enviando o leyendo
  THEN retorna la causa del contexto y no inicia trabajo posterior.
- GIVEN una operación terminada o cancelada
  WHEN `Execute` retorna
  THEN no quedan goroutines propias ni cuerpos de respuesta abiertos.

### R6 — Límites explícitos de request y response

WHEN the HTTP tool handles request or response data,
THE HTTP tool SHALL enforce the positive configured limits for encoded URL,
request headers, encoded JSON body, response headers and captured response
body.

#### Acceptance criteria

- GIVEN una URL, headers o body de request que exceden su límite
  WHEN se prepara la petición
  THEN se devuelve `request_limit` antes de DNS o conexión.
- GIVEN headers de response que exceden el máximo
  WHEN el transporte los recibe
  THEN retorna un error operativo `response_limit` sin exponer sus valores.
- GIVEN un body de response mayor que el máximo
  WHEN se lee
  THEN se conservan como máximo los bytes configurados, `body_truncated` es
  `true` y nunca se almacena en memoria el resto completo.
- GIVEN un límite obligatorio ausente o no positivo
  WHEN se construye la herramienta
  THEN la configuración se rechaza.

### R7 — Resultado HTTP estructurado y estable

WHEN an HTTP exchange produces a response or an operational failure,
THE HTTP tool SHALL return a bounded structured result containing the available
status, allowed headers, body representation, duration and typed operational
error.

#### Acceptance criteria

- GIVEN una respuesta completa
  WHEN retorna
  THEN el output contiene `status`, headers permitidos, `body`, `body_format`,
  `body_bytes`, `body_truncated=false`, `duration_ms` y `error=null`.
- GIVEN media type JSON y body completo válido
  WHEN retorna
  THEN `body` contiene el valor JSON decodificado; para otro contenido contiene
  texto UTF-8 o base64 y `body_format` identifica la representación.
- GIVEN respuesta 4xx o 5xx
  WHEN retorna
  THEN conserva status, headers y body como observación exitosa del transporte;
  las assertions deciden el cumplimiento funcional.
- GIVEN un fallo operativo no causado por el contexto padre
  WHEN retorna
  THEN el output conserva solo datos disponibles y un objeto `error` con
  categoría estable; el error Go preserva la causa y la clasificación
  transitoria o permanente para el ejecutor.
- GIVEN cualquier resultado
  WHEN se calcula duración
  THEN es no negativa, usa el reloj inyectado y no decide por sí misma ningún
  veredicto.

### R8 — Headers permitidos y secretos ausentes

WHEN request or response headers are handled,
THE HTTP tool SHALL admit or expose only explicitly allowed names and SHALL
never return the real value of a sensitive header.

#### Acceptance criteria

- GIVEN un request header no incluido en `allowed_request_headers`
  WHEN se valida
  THEN se rechaza antes de red.
- GIVEN `Authorization`, `Cookie`, `Set-Cookie` o `Proxy-Authorization` con
  cualquier capitalización en el input
  WHEN se valida
  THEN se rechaza incluso si aparece por error en la allowlist.
- GIVEN response headers
  WHEN se construye el output
  THEN solo aparecen nombres de `allowed_response_headers`, normalizados
  canónicamente y con sus valores en el orden observado.
- GIVEN uno de los cuatro headers sensibles en una respuesta o un error
  WHEN se construye output o diagnóstico
  THEN su valor real no aparece; el header se omite y el error no incluye
  dumps de request/response.

### R9 — Adapter sin propiedad del run ni persistencia

WHEN the HTTP tool executes,
THE HTTP tool SHALL limit its side effects to the authorized HTTP exchange and
return values without writing evidence, events or run state.

#### Acceptance criteria

- GIVEN stores y journal usados por el executor
  WHEN la herramienta se ejecuta
  THEN no recibe sus handles ni los modifica.
- GIVEN un resultado devuelto
  WHEN el executor lo procesa
  THEN saneado, persistencia, assertions y transición siguen perteneciendo a
  las fronteras existentes.
- GIVEN dos ejecuciones
  WHEN terminan
  THEN el adapter no retiene body, headers, resultado ni contexto de una para la
  otra.

### R10 — Efectos, idempotencia y retries conservadores

WHEN `http.request` is registered or executed,
THE HTTP tool SHALL declare conservative effect metadata and SHALL perform no
automatic retry.

#### Acceptance criteria

- GIVEN que un método permitido puede tener efectos
  WHEN se construye el `ToolSpec`
  THEN se registra como `Effects=true` e `Idempotent=false`; el modelo no puede
  cambiar esos metadatos.
- GIVEN un fallo de transporte
  WHEN `Execute` termina
  THEN el adapter no repite DNS, dial ni request por su cuenta.
- GIVEN que el plan executor considera un retry
  WHEN aplica el contrato existente
  THEN solo él decide según error, presupuesto y garantía de idempotencia; esta
  feature no amplía esa garantía.

### R11 — Configuración segura y hermética

WHEN the HTTP tool is constructed or tested,
THE HTTP tool SHALL require all observable permissions and limits explicitly
and SHALL allow external effects to be replaced by controlled local doubles.

#### Acceptance criteria

- GIVEN `allowed_methods` vacío, una allowlist obligatoria ausente o inválida,
  nombres duplicados tras normalización, redirects negativos, un método de
  redirect no permitido, target inválido o dependencia obligatoria ausente
  WHEN se construye
  THEN falla antes de registrar una capacidad.
- GIVEN tests de éxito, status de error, timeout, cancelación, redirect válido,
  redirect denegado, DNS/IP denegado y límites
  WHEN se ejecuta la suite
  THEN usa `httptest`, resolver/transporte/reloj controlados y no accede a
  Internet, variables de entorno ni un modelo real.
- GIVEN la configuración
  WHEN se inspecciona el input de una llamada
  THEN el input no puede modificar métodos, headers permitidos, timeout,
  tamaños, redirects, resolver ni target.

## Errores y casos límite

- Los errores de configuración e input son tipados, conservan la causa cuando
  existe y señalan el field path. No alcanzan red ni se convierten en evidencia
  de una respuesta.
- Las categorías operativas mínimas son `dns`, `destination_denied`,
  `connect`, `tls`, `timeout`, `request_limit`, `response_limit`,
  `redirect_denied`, `redirect_limit`, `encode` y `read`. El error Go conserva
  la causa; el output solo contiene categoría estable, nunca secretos ni dumps.
- Un DNS sin resultados es `dns`. Las IPs quedan fijadas por operación, no entre
  runs; así se evita estado global y se permite que un despliegue cambie entre
  validaciones.
- Un status 204 o un body vacío devuelve `body=null`, `body_format=empty` y
  cero bytes.
- Un JSON inválido o truncado no se presenta como JSON válido: se conserva en
  representación de texto o base64 con su formato y flag de truncación.
- Los nombres de headers se comparan sin distinguir mayúsculas. Se rechazan
  nombres o valores con CR/LF y headers hop-by-hop que puedan alterar framing,
  conexión, proxy o upgrade.
- HTTPS conserva el hostname autorizado para SNI y validación del certificado
  aunque el dial use una IP fijada. No se permite desactivar TLS desde el input.
- La propia conexión HTTP puede causar un efecto remoto. Por ello la validación
  completa ocurre antes de DNS y el spec de herramienta usa metadatos
  conservadores.

## Decisiones abiertas

No hay decisiones abiertas que bloqueen esta entrega. Métodos, puertos,
allowlists, timeout, tamaños y redirects no reciben valores inventados: el
caller confiable debe declararlos al construir la herramienta y la construcción
falla si falta una decisión obligatoria. El único puerto autorizado es el
puerto efectivo del `Target.BaseURL`; ampliar a varios destinos requerirá otra
feature y otra aprobación.

## Aprobación humana

Esta spec requiere aprobación humana antes de implementar.

```yaml
approved_by: user
approved_at: 2026-10-11T19:14:19+02:00
```
