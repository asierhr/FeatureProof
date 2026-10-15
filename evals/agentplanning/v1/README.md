# Agent planning evaluation dataset v1

Este dataset local evalúa la generación de planes sin ejecutar herramientas ni
contactar una aplicación. `dataset.json` fija el orden y el SHA-256 de cada
caso; cada caso fija a su vez el SHA-256 del OpenAPI local. `criteria.json`
declara todos los umbrales, hard-fails, tolerancias y límites. `replay.json` es
una fuente offline y `replay.sha256` contiene su digest esperado.

Replay hermético desde la raíz del repositorio:

```powershell
$sha = (Get-Content evals/agentplanning/v1/replay.sha256 -Raw).Trim()
go run ./cmd/featureproof eval agent `
  -dataset evals/agentplanning/v1/dataset.json `
  -criteria evals/agentplanning/v1/criteria.json `
  -output .featureproof/evals `
  -source replay `
  -replay evals/agentplanning/v1/replay.json `
  -replay-sha256 $sha
```

El modo live requiere además `-live`, `-provider`, `-model`, `-auth-profile`,
`-profile-store`, `-call-timeout`, `-max-duration`, `-max-calls`, `-max-tokens`,
`-max-replans`, `-max-request-bytes`, `-max-header-bytes`,
`-max-stream-bytes` y `-max-output-bytes`, todos explícitos. La resolución del
perfil ocurre después de validar dataset, criterios y baseline. El identificador
del perfil y las credenciales no entran en el runner ni en los artefactos.

El bundle completo contiene `cases.jsonl`, `metrics.json`, un
`comparison.json` opcional y `manifest.json`, publicado al final. Los códigos
de salida son: `0` aprobado, `1` medición completa que no supera criterios,
`2` error de configuración/sistema/publicación y `3` cancelación o deadline.

Los evals reales solo se inician con este comando. `go test` usa modelos
scripted y fixtures locales; nunca inicia el comando ni un proveedor real.
