# Tercera review independiente — Redacción y limitación de evidencias

## Veredicto

`approved`

La feature `evidence-redaction` satisface R1–R10 bajo el contrato revisado y
aprobado el 2026-10-11T20:43:32+02:00. No quedan hallazgos bloqueantes.

## Precondiciones

- `evidence-redaction` es la única feature en `in_progress`.
- La nueva aprobación humana consta en `requirements.md`.
- T1–T7 están marcadas `[x]`.
- Existe un informe de implementación actualizado con trazabilidad R1–R10.
- Los cambios revisados corresponden a redacción, assertions, evidencia,
  planning y harness requeridos por esta feature; las features 1–5 ya estaban
  aprobadas.

## Resolución de hallazgos

| Hallazgo | Estado | Evidencia |
|---|---|---|
| REV-1 | corregido mediante contrato revisado | Los nombres de headers default/adicionales aplican solo a `/headers` raíz exacto. `OverlapsSensitive` protege raíz, colección, nombre sensible y sus descendientes; permite headers públicos y paths anidados no configurados. Los headers anidados requieren `sensitive_json_paths`. |
| REV-2 | corregido | `SanitizedValue` no puede fabricarse fuera de `evidence`; `NewRecord` exige completitud, copia profundamente y sella. Store y projector revalidan el sello. |
| REV-3 | corregido | Maps y slices, incluidos tipos nombrados, registran su identidad antes de descender. Ciclos directos e indirectos fallan cerrados y observan cancelación. |
| REV-4 | corregido | La suite race completa y la repetición `-count=20` pasan con UCRT64 en `PATH`, `CGO_ENABLED=1` y `CC=gcc`. |
| REV-5 | corregido | `FileStore.Get` usa `Decoder.UseNumber`, exige EOF después del único record y conserva tokens numéricos exactos e integridad. |

## Trazabilidad revisada

| Requirement | Implementación y tests verificados | Resultado |
|---|---|---|
| R1 | `NewPolicy`; `TestPolicyRejectsInvalidLimitsHeadersAndPathsBeforeRun`; tests de composición fail-fast | pass |
| R2 | `Sanitizer.walk`; copias profundas; ciclos nombrados; `TestNewRecordCopiesSanitizedValuesAndRejectsPostSealMutation` | pass |
| R3 | `/headers` raíz exacto; defaults y adicionales case-insensitive; `TestRootDefaultAndAdditionalSensitiveHeadersAreRedactedCaseInsensitively`; `TestNestedHeadersRequireExplicitSensitivePaths` | pass |
| R4 | paths compilados, raíz, arrays y escapes; projector monotónico | pass |
| R5 | `Policy.OverlapsSensitive`; validación de grupos y planes; tests de raíz, colección, nombres, descendientes, paths públicos y cero efectos | pass |
| R6 | truncación de texto/JSON, límite UTF-8, metadata exacta y assertions inconclusas | pass |
| R7 | resultado infalsificable, sello, store fail-closed, hash, `UseNumber`, EOF y round-trip exacto | pass |
| R8 | proyección desde records confirmados, paths exclusivos del modelo y ocultación completa de bodies truncados | pass |
| R9 | canarios en headers raíz, header adicional, path anidado explícito, JSON, body, input, evidencia, eventos y contexto del modelo | pass |
| R10 | cancelación en traversal/proyección, determinismo, idempotencia y race detector | pass |

Los tests asociados fallarían al retirar el comportamiento: comprueban outputs
y errores tipados, ausencia de llamadas a policy/tool, bytes persistidos,
integridad, tokens numéricos, canarios y estados funcionales. No se basan solo
en coverage.

## Arquitectura y convenciones

- La policy se valida y congela antes de efectos.
- Sanitizer, store, assertion engine y projector mantienen fronteras separadas.
- La evidencia confirmada sigue siendo la fuente de assertions y contexto.
- No se añadieron goroutines ni I/O bajo locks; los contextos se propagan.
- Los errores de redacción contienen categoría y path seguro sin payload.
- Los eventos y resultados contienen IDs, presencia y tamaños, sin secretos.
- Las entidades y funciones nuevas o modificadas incluyen comentarios sobre su
  responsabilidad dentro del sistema.
- Los tests son herméticos, deterministas y no usan red, modelo real ni
  producción.

## Verificación ejecutada

| Comando | Resultado |
|---|---|
| `gofmt -l .` | pass, sin archivos |
| `go vet ./...` | pass |
| `go test -run '^$' ./...` | pass |
| `go test ./...` | pass |
| `go test ./internal/evidence ./internal/assertion ./internal/planning ./internal/harness ./internal/jsonpath -count=20` | pass |
| `$env:PATH='C:\msys64\ucrt64\bin;' + $env:PATH; $env:CGO_ENABLED='1'; $env:CC='gcc'; go test -race ./...` | pass |
| misma configuración + `go test -race -count=20 ./internal/evidence ./internal/assertion ./internal/planning ./internal/harness ./internal/jsonpath` | pass |
| `go test '-coverprofile=coverage_review.out' ./...` | pass |
| `go tool cover '-func=coverage_review.out'` | pass, 90,5 % global |

Coverage observado: 90,5 % global; 91,0 % en `internal/evidence`, 90,6 % en
`internal/assertion`, 89,9 % en `internal/planning`, 88,0 % en
`internal/harness` y 100,0 % en `internal/jsonpath`. Todos los paquetes
modificados superan el mínimo de 85 % y el global supera el baseline de 90,3 %.

## Riesgos residuales

No se identifican riesgos que impidan cerrar la feature. Las ubicaciones de
headers distintas de `/headers` raíz deben declararse explícitamente mediante
`sensitive_json_paths`, tal como establece el contrato aprobado.
