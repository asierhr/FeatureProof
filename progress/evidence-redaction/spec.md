# Evidence redaction — bloqueo de especificación

## Información disponible

- La feature debe redactar valores sensibles antes de persistirlos, registrarlos
  o exponerlos al modelo, y debe fallar de forma cerrada si la redacción no
  puede completarse.
- `ARCH-003` exige que un resultado `passed` esté respaldado por evidencia
  persistida y `ARCH-009` prohíbe persistir o enviar al modelo secretos y datos
  configurados como sensibles.
- El flujo actual sanea `Input` y `Output` antes de crear `evidence.Record`; el
  motor de assertions evalúa después el `Output` confirmado de esa evidencia.
- El repositorio ya usa `[REDACTED]` como marcador y protege por defecto los
  headers `Authorization`, `Cookie`, `Set-Cookie` y `Proxy-Authorization` en la
  herramienta HTTP. La sintaxis de paths JSON del motor de assertions también
  está documentada y validada.
- Los acceptance criteria requieren conservar estructura suficiente para
  evaluar «assertions permitidas», pero no definen cuáles son permitidas cuando
  su path apunta a un valor sensible.

## Ambigüedad bloqueante

Falta definir la relación observable entre redacción y assertions que apuntan
a una ruta configurada como sensible. Hay al menos tres comportamientos
compatibles con parte del texto disponible:

1. rechazar antes del run las assertions que necesiten observar el valor de una
   ruta sensible, permitiendo solo comprobaciones estructurales compatibles;
2. evaluar el valor original antes de redactarlo y publicar únicamente un
   resultado saneado;
3. evaluar sobre el marcador redactado, lo que cambiaría el significado de
   igualdad y comparaciones y podría producir resultados engañosos.

Ningún documento aprobado decide entre estas alternativas. Elegir una fijaría
un contrato de seguridad y de veredicto que el spec author no puede inventar.

## Impacto

Sin esta decisión no se pueden redactar requirements verificables para la
validación previa al run, la evaluación determinista, el contenido de
`AssertionResult`, los reportes ni los tests canario. También queda
indeterminado si una assertion existente debe rechazarse, resultar
inconclusa o evaluarse con acceso temporal al secreto.

## Pregunta mínima

Cuando una assertion apunta a una ruta declarada sensible, ¿debe FeatureProof
rechazar antes del run cualquier operador que necesite leer o revelar su valor
y permitir únicamente `exists`/`absent` sobre la estructura ya redactada?

**Opción recomendada: sí.** Mantiene una sola vista persistida como fuente de
verdad, evita que valores originales lleguen a resultados o reportes y conserva
comprobaciones de presencia sin exponer el contenido. Si se necesita validar
el valor de un secreto, haría falta una feature posterior con un comparador
seguro explícito (por ejemplo, contra un digest o una referencia protegida).

Si la respuesta es no, debe indicarse si la assertion se evalúa antes de
redactar y qué campos exactos de su resultado pueden persistirse para que el
veredicto siga siendo auditable sin filtrar el valor.
