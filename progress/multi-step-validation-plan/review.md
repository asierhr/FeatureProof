# Quinta revisión de multi-step-validation-plan

## Veredicto

Aprobado. No quedan hallazgos bloqueantes ni correcciones pendientes.

## Precondiciones

- `multi-step-validation-plan` es la única feature en estado `in_progress`.
- La aprobación humana está registrada en `requirements.md`.
- T1-T6 están marcadas `[x]` y existe el informe del implementer actualizado.
- El cambio inspeccionado se limita a la feature 3 y a las extensiones previstas
  de evidencia y journal.

## Correcciones verificadas

- **REV-1:** `ValueNull` está eliminado y `value_type: null` se rechaza como
  `invalid_reference` antes de policy o herramienta.
- **REV-2:** las definitions anidadas de assertions se copian recursivamente y
  no comparten maps, slices, arrays ni interfaces con el snapshot validado.
- **REV-3:** cada `PathSegment` clona sus punteros `Field` e `Index`; las
  mutaciones desde el plan original y desde `Plan()` no redirigen la referencia
  validada.
- **REV-4:** los fallos del journal antes de ejecución y después de evidencia
  confirmada detienen la selección, conservan los hechos previos y no emiten
  `plan_completed`.
- **REV-5:** `firstCyclicStep` comprueba si cada nodo puede alcanzarse a sí
  mismo. El test declara primero un descendiente, mantiene una rama
  independiente y confirma repetidamente que el diagnóstico señala el primer
  nodo realmente cíclico según declaration order, sin efectos.
- **REV-6:** `jsonCompatible` rechaza `NaN`, `+Inf` y `-Inf`; los tres tests
  comprueban categoría, field path y cero llamadas a policy/herramienta.

## Trazabilidad revisada

| Requirement | Implementación y tests observados | Resultado |
|---|---|---|
| R1 | Modelo, constraint existente, steps únicos e IDs case-sensitive | pass |
| R2 | Tool/input/assertions/dependencies estructurados y números JSON válidos | pass |
| R3 | Referencias tipadas, ancestry, evidencia confirmada e input inmutable | pass |
| R4 | Duplicados, unknown/self dependencies y ciclos con diagnóstico estable | pass |
| R5 | Orden topológico por declaration order y cadena completa de controles | pass |
| R6 | Estados de step y agregación determinista sin claim de modelo | pass |
| R7 | Bloqueos con todas las causas directas y ramas independientes | pass |
| R8 | Journal reconstruible/inmutable y fallos operativos preservando prefijo | pass |
| R9 | Cancelación, retries seguros y acotados, suite hermética | pass |

Los tests señalados en `implementation.md` ejercitan comportamiento observable
y fallarían al retirar las validaciones, el orden, la persistencia, la
propagación o las correcciones revisadas.

## Verificaciones ejecutadas

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass; salida vacía |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -race -count=20 ./internal/...` | pass |
| `go test -coverprofile=<TEMP>/featureproof-review-feature3-fifth.out ./...` | pass |
| `go tool cover -func=<TEMP>/featureproof-review-feature3-fifth.out` | pass; global 90,1 %, `internal/planning` 89,8 % |

El código nuevo y los paquetes modificados mantienen al menos 85 % de coverage.
Las ramas funcionales, de validación, cancelación, retry, store y journal
relevantes quedan cubiertas.

## Arquitectura, comentarios y alcance

- La ejecución es secuencial, no crea goroutines y propaga contexto por las
  fronteras bloqueables; el race detector pasa.
- Registry, policy, presupuesto, herramienta, saneado, evidencia, journal y
  evaluador conservan la dirección de dependencias aprobada.
- Los estados funcionales se derivan del evaluador y de evidencia confirmada;
  los fallos internos permanecen errores operativos.
- Las entidades, funciones, fakes y auxiliares nuevos o modificados contienen
  comentarios funcionales acordes con el design.
- No se detectaron secretos, red, modelo real, I/O bajo locks ni cambios
  intencionados de otra feature.

