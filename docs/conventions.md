# Convenciones del proyecto

## 1. Propósito

Este documento define las convenciones que deben seguir implementadores y
revisores. Las excepciones deben justificarse en el `design.md` de la feature y
ser aprobadas antes de implementar.

## 2. Idioma y nombres

- Los identificadores del código se escriben en inglés.
- La documentación del proyecto y los mensajes dirigidos al usuario pueden
  escribirse en español.
- Los nombres recibidos de una API externa se conservan únicamente en su DTO o
  adapter y se traducen al modelo interno.
- Se evitan nombres genéricos como `Manager`, `Helper`, `Utils`, `Data` o
  `Processor` cuando no indiquen una responsabilidad precisa.

## 3. Paquetes y archivos Go

- Los paquetes tienen nombres cortos, en minúsculas y sin guiones bajos.
- Un paquete representa una responsabilidad cohesionada, no una capa vacía.
- Los archivos usan nombres descriptivos en `snake_case.go`.
- Los tests terminan en `_test.go`.
- Se evita un paquete `common` o `shared` usado como destino de código sin dueño.
- No se usan importaciones con punto. Los alias solo se emplean para resolver
  una colisión o mejorar claramente la lectura.
- Todo el código debe quedar formateado con `gofmt`.

## 4. Tipos y funciones

- Se prefieren tipos pequeños con invariantes claras.
- Los constructores validan dependencias y configuración obligatorias.
- Las dependencias se reciben explícitamente; no se usan singletons ni
  localizadores de servicios.
- Las funciones mantienen una responsabilidad y devuelven pronto ante errores.
- No se introducen getters y setters mecánicos si una operación de dominio
  expresa mejor la intención.
- Los estados complejos se modelan con tipos y transiciones explícitas, no con
  combinaciones ambiguas de booleanos.

## 5. Interfaces

- Una interfaz pertenece al paquete que la consume.
- Se define con el menor conjunto de métodos necesario.
- Su nombre describe una capacidad, no la implementación.
- No se crea una interfaz por cada struct.
- Se introduce una interfaz cuando separa una frontera externa, permite una
  sustitución necesaria en pruebas o representa implementaciones realmente
  intercambiables.
- La conformidad puede verificarse en compilación cuando aporte claridad:

```go
var _ PRGateway = (*HTTPPRGateway)(nil)
```

## 6. Contextos y cancelación

- `context.Context` es el primer parámetro de las operaciones que pueden
  bloquearse, cancelarse o atravesar una frontera externa.
- No se guarda un contexto dentro de un struct de larga vida.
- No se sustituye un contexto recibido por `context.Background()`.
- La cancelación se propaga hacia las operaciones hijas.
- Los bucles y esperas prolongadas observan `ctx.Done()`.

## 7. Concurrencia

- Toda goroutine tiene propietario, condición de salida y mecanismo de espera.
- No se lanzan goroutines ocultas desde constructores.
- El productor es responsable de cerrar el canal que crea.
- Un consumidor no cierra canales ajenos.
- Los canales comunican valores o fin de secuencia; la cancelación general se
  comunica mediante contexto.
- No se copia una estructura que contiene un mutex.
- La región protegida por un lock debe ser pequeña y evidente.
- No se mantienen locks durante red, disco, timers o callbacks externos.
- No se utiliza `time.Sleep` para coordinar tests concurrentes.

## 8. Tiempo y aleatoriedad

- Las duraciones internas utilizan `time.Duration`.
- Core y Application reciben reloj y aleatoriedad como dependencias cuando su
  comportamiento depende de ellos.
- Una ejecución registra la semilla aleatoria utilizada.
- Los tests controlan el avance del tiempo sin pausas reales.

## 9. Errores

- Un error incluye la operación y el dato relevante para diagnosticarlo.
- Los errores envueltos conservan la causa mediante `%w`.
- Los errores que el llamador debe distinguir utilizan tipos o valores
  comprobables con `errors.Is` o `errors.As`.
- No se comparan textos de error para decidir lógica.
- No se ignora un error sin una razón documentada.
- No se usa `panic` para errores operativos recuperables.
- Una capa registra un error solo cuando añade contexto operativo suficiente;
  no se registra y devuelve repetidamente el mismo error.

Ejemplo:

```go
evidence, err := tool.Execute(ctx, call)
if err != nil {
	return fmt.Errorf("execute tool call %s: %w", call.ID, err)
}
```

## 10. Configuración y secretos

- La configuración se carga y valida al arrancar.
- Core y Application no leen variables de entorno ni archivos.
- Los valores obligatorios producen un error temprano y comprensible.
- No se guardan contraseñas, tokens o cadenas de conexión en el repositorio.
- Los ejemplos usan valores ficticios y documentan las variables necesarias.

## 11. Logging y eventos

- Los logs son estructurados e incluyen identificadores como `run_id`,
  `constraint_id` y `tool_call_id` cuando existan.
- No se registran credenciales ni cuerpos sensibles.
- Los cambios de estado relevantes generan eventos observables.
- No se utiliza el log como mecanismo de coordinación o estado.
- Los mensajes explican qué operación falló y sobre qué entidad.

## 12. Comentarios y documentación

- Los comentarios explican decisiones, invariantes o motivos; no repiten el
  código.
- Los símbolos exportados incluyen comentario cuando forman parte de una API de
  paquete.
- Las decisiones generales se documentan en `docs/`.
- El comportamiento de una feature se documenta en `specs/<feature>/`.
- El progreso y la evidencia de una ejecución se escriben en `progress/`.

## 13. Tests

- Los nombres describen comportamiento observable.
- Se prefieren tablas cuando varios casos comparten el mismo flujo.
- Un test sigue Arrange, Act y Assert aunque no necesite comentarios con esos
  nombres.
- Los tests no dependen del orden de ejecución de otros tests.
- Los tests paralelos no comparten estado mutable sin aislamiento.
- Se prefieren fakes pequeños y seguros para concurrencia frente a mocks que
  replican detalles internos.
- No se debilita una aserción para conseguir que una implementación pase.
- Cada requirement `R<n>` tiene al menos un test concreto trazable.

## 14. Cambios y alcance

- Se implementa una sola feature de `feature_list.json` por sesión.
- No se mezclan refactors ajenos con la feature en curso.
- Una refactorización necesaria mantiene el alcance mínimo y se explica en
  `progress/<feature>/implementation.md`.
- No se cambia una spec aprobada desde la implementación.
- Si aparece una contradicción, se bloquea la task y se solicita al leader que
  devuelva la feature a la fase de spec.

## 15. Reglas verificables

- **CONV-001:** identificadores internos en inglés y términos del dominio.
- **CONV-002:** paquetes cohesionados y sin dependencias circulares.
- **CONV-003:** interfaces pequeñas definidas por el consumidor.
- **CONV-004:** contextos propagados y nunca almacenados en servicios de larga
  vida.
- **CONV-005:** errores con contexto, causa conservada y sin comparación textual.
- **CONV-006:** toda goroutine tiene ciclo de vida explícito.
- **CONV-007:** no hay secretos en código, tests o documentación.
- **CONV-008:** tests deterministas, aislados y sin `time.Sleep` como
  sincronización.
- **CONV-009:** cada `R<n>` se relaciona con al menos un test.
- **CONV-010:** una sesión modifica una única feature.
