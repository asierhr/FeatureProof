# Tercera revisión de assertion-engine

## Veredicto

Aprobado. R1-R9 están implementados y trazados a tests de comportamiento; las
tres correcciones solicitadas están resueltas y toda la verificación canónica
pasa sin hallazgos bloqueantes.

## Precondiciones

- Feature única `in_progress`: `assertion-engine`.
- Aprobación humana registrada: `user`, `2026-10-11T18:41:50+02:00`.
- Spec completa, T1-T6 marcadas `[x]` e informe del implementer presentes.
- Cambios limitados a la feature y a sus contratos necesarios en `planning`.

## Resolución de hallazgos anteriores

### REV-1 — Resuelto

`decimalNumber` conserva coeficiente significativo y exponente decimal
arbitrario en forma compacta. `Cmp` compara signo, magnitud y dígitos sin
materializar una potencia proporcional al exponente. Los tests cubren
exponentes positivos y negativos fuera de `int32`, equivalencia, orden y tamaño
de la representación. Volver a limitar el exponente o expandir `10^exponent`
rompería `TestExtremeJSONExponentsRemainCompactAndExact` y
`TestNumericOperatorsCompareExtremeExponentsSymbolically`.

### REV-2 — Resuelto

La evaluación observa contexto durante compilación recursiva, paths, copias,
declarations, nodos y roots sin evidencia, además de comprobarlo antes de cada
retorno funcional. Los tests cancelan de forma determinista durante
compilación y durante la construcción sin evidencia, exigen la causa de
contexto y cero resultados. Los tests del executor confirman que no se inicia
otro step y se conserva el prefijo confirmado.

### REV-3 — Resuelto

El coverage reproducible alcanza 90,2 % global, superando el baseline aprobado
de 90,1 %. Los paquetes modificados alcanzan 90,0 % en `internal/assertion` y
90,3 % en `internal/planning`. Las pruebas añadidas comprueban comportamiento
real: límites del union, paths de error, ciclos de maps/slices/grupos, igualdad
JSON estricta, escalares, observaciones numéricas ausentes y agregación de
varias roots.

## Trazabilidad R1-R9

| Requirement | Evidencia principal revisada | Resultado |
|---|---|---|
| R1 | validación completa antes de efectos, formas inválidas, claves extra y ciclos | pass |
| R2 | raíz, objetos, arrays, escapes y exclusión global de tokens numéricos no canónicos | pass |
| R3 | `null` presente y missing diferenciados para `exists` y `absent` | pass |
| R4 | igualdad JSON estricta y comparaciones exactas, incluidos exponentes extremos | pass |
| R5 | `all`/`any` anidados, tablas ternarias, orden y todos los hijos evaluados | pass |
| R6 | árbol auditable, valores aislados y solo evidence ID terminal en cada resultado | pass |
| R7 | configuración inválida, condición falsa y falta de evidencia separadas | pass |
| R8 | agregación de roots e integración real con executor y bloqueos, sin modelo | pass |
| R9 | cancelación previa, durante compilación, árbol, rama sin evidencia y fronteras del executor | pass |

Cada requirement tiene tests concretos que fallarían al retirar su
comportamiento. Los tests son herméticos, no usan red, modelo real ni
`time.Sleep`.

## Verificaciones ejecutadas

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin salida |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `PATH=C:\\msys64\\ucrt64\\bin;%PATH% CGO_ENABLED=1 CC=gcc go test -race -count=1 ./...` | pass |
| `PATH=C:\\msys64\\ucrt64\\bin;%PATH% CGO_ENABLED=1 CC=gcc go test -race -count=20 ./internal/assertion ./internal/planning` | pass |
| `go test '-coverprofile=coverage.out' ./...` | pass |
| `go tool cover '-func=coverage.out'` | 90,2 % global; 90,0 % `internal/assertion`; 90,3 % `internal/planning` |

Los artefactos temporales de coverage se eliminaron al terminar.

## Arquitectura y convenciones

- El modelo no participa en el cálculo; los estados se derivan exclusivamente
  de evidence terminal confirmada y reglas deterministas.
- Los contratos permanecen en el paquete consumidor, no hay dependencias
  circulares y el evaluador es stateless.
- No se crean goroutines ni se realiza I/O, red o acceso a servicios externos
  desde el motor. La suite race completa y repetida pasa.
- Los errores conservan causas y categorías estables; `invalid`, `failed` e
  `inconclusive` permanecen separados.
- Todas las entidades y funciones productivas nuevas o modificadas incluyen
  comentarios funcionales conforme al design.
- No se detectaron secretos, accesos a producción ni cambios fuera de alcance.

## Hallazgos

No quedan hallazgos bloqueantes, altos, medios ni bajos.
