# Model smoke opt-in

Este ejecutable no forma parte de `featureproof validate`, `go test` ni la
verificación ordinaria. Requiere una cuenta y una aplicación open source
elegibles para Sign in with ChatGPT.

Primero se crea o actualiza un perfil local mediante un callback loopback ya
reservado por el proceso:

```text
go run ./cmd/featureproof-model-smoke -opt-in -action=provision -provider=openai -profile=<PROFILE_ID> -profile-store=<ABSOLUTE_PATH> -host-id=<STABLE_OPAQUE_HOST_ID> -redirect-uri=http://127.0.0.1:<PORT>/auth/callback
```

Después se solicita exactamente una inferencia con un modelo seleccionado por
el usuario y presupuestos explícitos o sus límites conservadores del comando:

```text
go run ./cmd/featureproof-model-smoke -opt-in -action=infer -provider=openai -model=<SELECTED_MODEL> -profile=<PROFILE_ID> -profile-store=<ABSOLUTE_PATH> -max-calls=1 -max-tokens=10000 -duration=1m -timeout=30s
```

No se aceptan API keys ni bearer tokens por flags o variables de entorno. La
salida solo contiene proveedor, modelo, duración, uso confirmado y resultado o
categoría segura.
