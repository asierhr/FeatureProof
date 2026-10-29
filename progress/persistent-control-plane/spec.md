# Enmienda R16 aprobada

## Contexto disponible

La petición original estableció comentarios de funcionalidad para entidades y
funciones y pidió conservar esa convención en `design.md`. R16 amplió después el
alcance a toda variable local, parámetro y receiver. Las reviews independientes
6–8 demostraron que esa obligación produjo miles de comentarios generados que
atribuyen fronteras incorrectas y reducen la legibilidad, en conflicto con
`docs/conventions.md` §12.

## Cambio propuesto

- Mantener comentarios sistémicos obligatorios para entidades/tipos,
  funciones, métodos y helpers de test nuevos o modificados.
- Mantenerlos para constantes y variables de paquete cuando su responsabilidad
  compartida no resulte evidente.
- Excluir locales, parámetros y receivers triviales de la obligación mecánica.
- Exigir contexto puntual o un tipo más explícito cuando un local represente
  autoridad, fencing, durabilidad u ownership no evidentes.
- Auditar el diff completo y rechazar paráfrasis, boilerplate y familias
  interpoladas.

## Impacto

La enmienda cambia los acceptance criteria de R16 y añade T18. No modifica el
comportamiento del control plane ni relaja sus garantías funcionales, pero sí
requiere retirar ruido documental y reejecutar los gates antes de una nueva
revisión independiente.

## Decisión

La persona usuaria aprobó explícitamente la enmienda
`r16-comment-scope` el `2026-10-29T15:47:22+01:00`. La implementación puede
reanudar T18 y debe someter el resultado a una nueva revisión independiente.
