# Verificación y revisión

## 1. Propósito

Este documento define la evidencia necesaria para afirmar que una feature
funciona y puede pasar a `done`.

Una feature no está terminada porque compile, alcance un porcentaje de coverage
o parezca correcta durante la revisión. Debe satisfacer todos sus requirements,
superar las comprobaciones automáticas y recibir la aprobación del reviewer.

## 2. Precondiciones

Antes de verificar una feature:

- es la única feature `in_progress`;
- su spec fue aprobada por una persona;
- existen `requirements.md`, `design.md` y `tasks.md`;
- todas las tasks aplicables están `[x]`;
- el implementer ha creado `progress/<feature>/implementation.md`;
- no existen cambios intencionados de otras features.

## 3. Punto de entrada de verificación

La verificación canónica está formada por comandos Go multiplataforma. Deben
ejecutarse sin interacción y devolver un código distinto de cero ante cualquier
fallo:

```text
gofmt check
go vet ./...
go test ./...
go test -race ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

La comprobación de `gofmt` consiste en ejecutar `gofmt -l .` y fallar si produce
algún nombre de archivo. Puede envolverse en PowerShell, Bash o CI sin que el
shell elegido se convierta en parte del contrato del proyecto.

Cuando se añada un comando de verificación propio, deberá limitarse a ejecutar
estas comprobaciones de forma equivalente; no puede ocultar fallos ni modificar
código para conseguir que pasen.

## 4. Fases de verificación

### 4.1 Formato

Todo archivo Go debe estar formateado. El implementer puede ejecutar:

```bash
go fmt ./...
```

La validación y el reviewer comprueban sin editar:

```bash
test -z "$(gofmt -l .)"
```

### 4.2 Compilación y análisis estático

```bash
go vet ./...
go test -run '^$' ./...
```

No se ignoran warnings relevantes ni se desactivan analizadores para cerrar una
feature sin una justificación aprobada.

### 4.3 Tests funcionales

```bash
go test ./...
```

Todos los tests deben pasar. No se permiten tests omitidos, comentados o
debilitados para ocultar un defecto.

### 4.4 Detector de carreras

```bash
go test -race ./...
```

Es obligatorio para todas las features. Los componentes concurrentes críticos
se repiten para explorar distintos interleavings:

```bash
go test -race -count=20 ./internal/...
```

El comando concreto puede limitarse a los paquetes afectados cuando la suite
crezca, pero el cierre final conserva una ejecución completa con `-race`.

### 4.5 Coverage

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

El criterio inicial es:

- cada `R<n>` está cubierto por al menos un test concreto;
- el código nuevo o modificado alcanza al menos 85 % de statement coverage;
- todas las transiciones de estado y ramas de error relevantes están probadas;
- el coverage global no disminuye sin una justificación aprobada.

El 85 % es una señal mínima, no el objetivo final. No se escriben tests triviales
para inflar el porcentaje. Si una línea no puede probarse razonablemente, se
documenta el motivo y el riesgo en el informe de implementación.

### 4.6 Tests de contrato e integración

Cuando una feature modifica una frontera externa, se verifican además:

- rutas y métodos HTTP;
- serialización de request y response;
- códigos de error;
- timeouts y cancelación;
- compatibilidad con una instancia aislada de la aplicación.

Las pruebas de integración no utilizan producción y deben preparar y limpiar su
propio estado.

## 5. Trazabilidad de requirements

El implementer crea `progress/<feature>/implementation.md` con este mapa:

```markdown
# Implementación de <feature>

## Requirements y tests

| Requirement | Test | Archivo | Resultado |
|---|---|---|---|
| R1 | TestHarnessExecutesAllowedTool | internal/harness/harness_test.go | pass |
| R2 | TestHarnessRejectsUnknownTool | internal/harness/harness_test.go | pass |

## Tasks

| Task | Estado | Evidencia |
|---|---|---|
| T1 | done | Modelo y tests añadidos |

## Comandos ejecutados

| Comando | Resultado |
|---|---|
| go test ./... | pass |
| go test -race ./... | pass |

## Coverage

- Global:
- Código modificado:
- Ramas relevantes:

## Riesgos o limitaciones

- Ninguno, o descripción concreta.
```

El reviewer verifica el test real. La existencia de una fila no demuestra por
sí sola la cobertura del requirement.

## 6. Responsabilidades del implementer

El implementer:

1. comprueba que la feature está `in_progress`;
2. ejecuta las tasks en orden;
3. añade los tests exigidos por cada task;
4. marca cada task completada con `[x]`;
5. ejecuta todas las comprobaciones canónicas hasta que pasen;
6. crea el mapa de trazabilidad;
7. documenta riesgos y limitaciones;
8. solicita al leader la transición correspondiente; nunca modifica el estado
   de la feature.

Su primera entrega termina con:

```text
ready_for_review -> progress/<feature>/implementation.md
```

`done` se reserva exclusivamente para el estado final de la feature en
`feature_list.json`.

## 7. Responsabilidades del reviewer

El reviewer no edita código. Revisa la única feature `in_progress` y:

1. lee arquitectura, convenciones y proceso SDD;
2. lee la spec completa;
3. inspecciona todos los archivos modificados;
4. comprueba que todas las tasks están `[x]`;
5. localiza un test real para cada `R<n>`;
6. ejecuta todas las comprobaciones canónicas;
7. revisa cobertura, concurrencia, cancelación y errores;
8. emite un veredicto con evidencia.

## 8. Criterios de revisión

### Funcionalidad

- Cada requirement está implementado.
- Los criterios de aceptación se prueban como comportamiento observable.
- Los errores y casos límite de la spec están cubiertos.
- No se ha añadido comportamiento fuera del alcance.

### Arquitectura

- Se respeta la dirección de dependencias.
- Las reglas permanecen fuera de adapters y entrypoints.
- El estado mutable tiene propietario.
- Tiempo, aleatoriedad e integraciones son sustituibles donde corresponde.

### Concurrencia

- Una ejecución no realiza transiciones incompatibles a la vez.
- Ejecuciones independientes pueden progresar concurrentemente cuando la
  implementación lo permita.
- Toda goroutine termina y puede esperarse.
- La cancelación se propaga.
- No se mantienen locks durante I/O.
- El detector de carreras pasa.

### Tests

- Cada `R<n>` tiene un test concreto.
- Los tests fallarían si se elimina el comportamiento.
- No dependen de `time.Sleep` para sincronizarse.
- Los fakes compartidos son seguros para concurrencia.
- Coverage y ramas relevantes cumplen los criterios.

### Convenciones

- Nombres, paquetes, contextos y errores cumplen `conventions.md`.
- No hay secretos ni datos de producción.
- El cambio se limita a una feature.

## 9. Hallazgos

Cada hallazgo del reviewer incluye:

```markdown
## REV-1 — Título concreto

- Severidad: blocker | high | medium | low
- Regla o requirement: R2 / ARCH-008 / CONV-005
- Archivo y línea:
- Evidencia:
- Impacto:
- Corrección requerida:
```

Se consideran bloqueantes:

- un requirement sin implementar o sin test;
- una task incompleta sin justificación aprobada;
- tests, `go vet` o race detector fallidos;
- una carrera de datos o goroutine sin terminación;
- una violación arquitectónica material;
- secretos o acceso a producción;
- evidencia falsa o no reproducible.

## 10. Veredicto

El reviewer escribe `progress/<feature>/review.md` y devuelve una sola línea:

```text
approved -> progress/<feature>/review.md
```

o:

```text
rejected -> progress/<feature>/review.md
```

Si rechaza, el leader devuelve los hallazgos al implementer. Después de las
correcciones se repiten todas las verificaciones y una nueva review.

## 11. Cierre de la feature

Solo después de `approved`:

1. el leader cambia la feature a `done`;
2. el leader actualiza `progress/history.md` con el resumen y la evidencia;
3. limpia `progress/current.md` o lo prepara para la siguiente feature;
4. conserva los informes de implementación y review.

## 12. Checklist final

- [ ] Feature única y en estado `in_progress` durante la verificación.
- [ ] Spec aprobada y sin cambios de comportamiento posteriores.
- [ ] Todas las tasks están `[x]`.
- [ ] Existe trazabilidad completa `R<n> → test`.
- [ ] Todas las comprobaciones canónicas terminan correctamente.
- [ ] `go test -race ./...` termina correctamente.
- [ ] Coverage registrado y criterios satisfechos.
- [ ] No existen hallazgos bloqueantes.
- [ ] El reviewer ha emitido `approved`.
- [ ] El resumen se ha añadido al historial antes de marcar `done`.
