# Especificación — model-adapter

## Resolución

Resuelto por decisión explícita del usuario el 2026-10-14. El contrato aprobado
para redactar la spec es:

- runtime independiente del proveedor y selección explícita de proveedor,
  modelo y perfil, sin modelo por defecto;
- primer adapter OpenAI Responses API con Sign in with ChatGPT OAuth para apps
  open source elegibles;
- secrets resueltos solo en composición y excluidos de harness/artefactos/
  telemetría;
- budgets de llamadas, tokens, duración y replanificaciones en sustitución
  expresa del coste monetario, sin pricing;
- tests ordinarios herméticos y prueba real opt-in separada de `go test`.

El bloqueo queda cerrado. Los contratos resultantes están en
`specs/model-adapter/{requirements,design,tasks}.md` y requieren la puerta de
aprobación humana antes de implementar.

## Información disponible

- La feature 9 debe mantener el puerto `harness.Model` como contrato propio y
  añadir al menos un adapter que invoque un modelo real sin exponer tipos del
  SDK al harness.
- La respuesta debe validarse antes de devolverse, y los fallos deben distinguir
  respuesta inválida, timeout, cancelación, autenticación, rate limit y error
  del proveedor.
- La configuración no secreta debe validarse al arrancar; las credenciales se
  leen desde variables de entorno y no se persisten ni aparecen en telemetría.
- Cada llamada debe exponer telemetría segura de proveedor, modelo, duración y
  uso de tokens cuando exista.
- El sistema debe reservar antes de cada llamada presupuestos de llamadas,
  tokens y coste estimado. Los tests ordinarios serán herméticos y la prueba
  real será opt-in y separada de `go test`.
- `docs/architecture.md` aplaza expresamente la elección de proveedor y modelo
  hasta una spec que la necesite. El repositorio no contiene SDK, endpoint,
  nombre de variable de credencial, modelo ni tabla de precios acordados.

## Ambigüedad concreta

No está decidido qué proveedor y API constituyen el adapter real de esta
primera entrega. Esa elección determina el transporte, el formato estructurado,
la autenticación, la taxonomía de errores, los campos de usage, la prueba real
opt-in y el contrato de configuración. Tampoco existe una fuente de precios que
permita convertir tokens a coste sin fijar tarifas que pueden cambiar.

## Impacto

Sin esta decisión no se pueden redactar requirements verificables ni un diseño
cerrado para la llamada real. Elegir nombres de modelos, endpoint, variable de
entorno, SDK o precios desde el spec introduciría comportamiento público no
autorizado y podría producir un adapter que no coincida con el proveedor que el
proyecto pretende usar.

## Pregunta original y respuesta

La pregunta era qué proveedor/API, autenticación y presupuesto debían formar el
primer contrato real. La respuesta descartó API key y pricing: fijó OpenAI
Responses con Sign in with ChatGPT OAuth, selección explícita sin defaults y
budgets no monetarios. La sección **Resolución** contiene el contrato vigente.
