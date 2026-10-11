# Proceso de especificación SDD

## 1. Propósito

Toda feature de `feature_list.json` con `"sdd": true` se desarrolla mediante
Spec Driven Development. La spec define el contrato entre el usuario, el agente
implementador y el agente revisor.

No se escribe código productivo ni tests de una feature SDD hasta que su spec
haya sido redactada y aprobada por una persona.

## 2. Estados de una feature

```text
pending
   │
   ▼ spec_author
spec_ready
   │
   ▼ aprobación humana
in_progress
   │
   ▼ implementer + reviewer
done
```

`blocked` puede alcanzarse desde cualquier fase cuando falta información,
existe una contradicción o una dependencia impide continuar.

### `pending`

La feature todavía no dispone de una spec completa y aprobable.

### `spec_ready`

El `spec_author` ha creado los tres documentos y ha terminado su trabajo. Este
estado no significa aprobación.

### `in_progress`

Una persona ha aprobado la spec y el leader ha autorizado la implementación.

### `done`

El reviewer ha aprobado la implementación y las verificaciones obligatorias
han pasado.

### `blocked`

El impedimento se documenta en `progress/<feature>/spec.md` o
`progress/<feature>/implementation.md`, según la fase.

## 3. Selección de una feature

El leader selecciona exactamente una feature `pending` con `"sdd": true`.

No se agrupan varias features en una misma spec. Si el comportamiento solicitado
no puede entregarse o verificarse de forma independiente, se deben registrar
dependencias explícitas en `feature_list.json`.

## 4. Artefactos obligatorios

El `spec_author` crea:

```text
specs/<feature>/
  requirements.md
  design.md
  tasks.md
```

No edita `cmd/` ni `internal/`, y no cambia el estado de la feature.

## 5. `requirements.md`

Define qué comportamiento debe ser verdadero sin imponer una implementación.

Debe contener:

1. objetivo y problema;
2. alcance;
3. comportamiento fuera de alcance;
4. actores y términos relevantes;
5. requirements numerados `R1`, `R2`, ...;
6. criterios de aceptación verificables;
7. casos límite y errores;
8. decisiones abiertas;
9. registro de aprobación humana.

### 5.1 Reglas para requirements

- Cada requirement contiene una sola obligación observable.
- Cada `R<n>` debe poder verificarse con al menos un test concreto.
- No se usan términos ambiguos como “rápido”, “correctamente”, “robusto” o
  “cuando sea posible” sin una medida.
- Los requisitos no soportados por la petición o el producto no se inventan.
- Si falta una decisión que cambia el resultado, el spec_author devuelve
  `blocked` y solicita aclaración.

### 5.2 Notación EARS

Se utilizan estos patrones cuando resulten aplicables:

#### Requisito ubicuo

```text
R1. THE harness SHALL assign a stable identifier to every validation run.
```

#### Dirigido por evento

```text
R2. WHEN the model requests a registered tool,
THE harness SHALL evaluate policy before executing that tool.
```

#### Dirigido por estado

```text
R3. WHILE a run has exhausted its step budget,
THE harness SHALL NOT request another model decision.
```

#### Comportamiento no deseado

```text
R4. IF a tool operation times out,
THEN THE harness SHALL record the timeout and apply the configured policy.
```

#### Funcionalidad opcional

```text
R5. WHERE plan approval is required,
THE harness SHALL NOT execute the plan before explicit approval.
```

Los keywords EARS se mantienen en mayúsculas para facilitar su revisión.

### 5.3 Criterios de aceptación

Cada requirement incluye ejemplos Given/When/Then o condiciones equivalentes:

```markdown
## R3 — Una operación simultánea por máquina

WHILE a run has exhausted its tool-call budget,
THE harness SHALL NOT execute another tool call.

### Acceptance criteria

- GIVEN a run with no remaining tool calls
  WHEN the model requests another tool
  THEN the tool is not executed and the run becomes inconclusive.
```

## 6. `design.md`

Explica cómo encaja la feature en la arquitectura aprobada. No contiene el
código final, aunque puede incluir diagramas, contratos propuestos y
pseudocódigo.

Debe contener:

1. resumen de la solución;
2. áreas y paquetes afectados;
3. componentes y responsabilidades;
4. flujo principal y flujos de error;
5. estados y transiciones cuando existan;
6. propiedad del estado y concurrencia;
7. contratos o datos afectados;
8. cancelación, retries e idempotencia;
9. observabilidad;
10. estrategia de pruebas;
11. decisiones y alternativas descartadas;
12. mapa inicial `R<n> → componente/prueba prevista`.

El diseño debe hacer referencia a las reglas de `docs/architecture.md` y
`docs/conventions.md`. Una excepción debe identificarse y someterse a aprobación
humana dentro de esta fase.

## 7. `tasks.md`

Convierte el diseño aprobado en pasos pequeños y ordenados para el implementer.

Cada task:

- tiene identificador `T1`, `T2`, ...;
- utiliza checkbox `[ ]` o `[x]`;
- referencia uno o varios requirements;
- produce un resultado verificable;
- incluye tests cuando introduce comportamiento;
- puede completarse sin rediseñar toda la feature.

Formato recomendado:

```markdown
- [ ] T1. Definir el modelo de estado de una máquina. [R1, R3]
  - Añadir tests de transiciones válidas e inválidas.
  - No incluir todavía integración HTTP.

- [ ] T2. Implementar el ciclo cancelable de una máquina. [R2, R3, R4]
  - Añadir tests con reloj controlado.
  - Verificar terminación de la goroutine.
```

Las tasks se ordenan para permitir TDD: prueba que falla, implementación mínima
y refactorización. No se crea una task final genérica de “añadir todos los
tests”; los tests acompañan al comportamiento que verifican.

## 8. Puerta de aprobación humana

Cuando los tres documentos están completos:

1. El spec author solicita al leader la transición a `spec_ready`.
2. Devuelve `spec_ready -> specs/<feature>/`.
3. El harness se detiene.
4. Una persona revisa requirements, diseño, alcance y decisiones.
5. Si solicita cambios, la feature permanece en `spec_ready` y vuelve al
   spec_author.
6. Si aprueba, se registra en `requirements.md`:

```yaml
approved_by: <nombre>
approved_at: <fecha ISO-8601>
```

7. El leader cambia la feature a `in_progress` y puede lanzar al implementer.

El silencio, el paso del tiempo o la ausencia de comentarios no constituyen
aprobación.

## 9. Cambios posteriores a la aprobación

Una corrección editorial puede realizarse sin reiniciar el flujo si no cambia
el comportamiento, el diseño ni las tasks.

Si se modifica alcance, comportamiento observable, arquitectura o criterios de
aceptación:

1. se detiene la implementación;
2. el leader devuelve la feature a `spec_ready`;
3. se actualizan los tres documentos afectados;
4. se solicita una nueva aprobación humana;
5. los tests y el código se reanudan después de aprobar.

El implementer y el reviewer no reinterpretan unilateralmente un requirement
ambiguo.

## 10. Trazabilidad

La trazabilidad mínima es:

```text
Feature → R<n> → T<n> → test concreto → resultado
```

- `requirements.md` define `R<n>`.
- `tasks.md` referencia los requirements que implementa.
- El implementer registra `R<n> → test` en
  `progress/<feature>/implementation.md`.
- El reviewer verifica el mapa contra los tests reales.

Un porcentaje de coverage no reemplaza esta trazabilidad.

## 11. Condiciones de bloqueo

El spec_author devuelve `blocked` cuando:

- los acceptance criteria de `feature_list.json` no permiten deducir el
  comportamiento;
- falta una decisión de producto;
- dos requirements se contradicen;
- el alcance incluye varias features independientes;
- un requirement no puede expresarse de forma verificable.

El informe `progress/<feature>/spec.md` debe incluir:

- contexto disponible;
- punto exacto bloqueado;
- por qué afecta al comportamiento;
- pregunta concreta que debe resolver una persona.

## 12. Checklist del spec_author

- [ ] Se ha seleccionado una sola feature `pending` y SDD.
- [ ] Existen `requirements.md`, `design.md` y `tasks.md`.
- [ ] Todos los requirements tienen identificador único.
- [ ] Cada requirement es observable y verificable.
- [ ] No se han inventado decisiones no soportadas.
- [ ] El diseño respeta arquitectura y convenciones.
- [ ] Todas las tasks referencian requirements.
- [ ] Los tests forman parte de las tasks correspondientes.
- [ ] No se ha modificado código ni tests.
- [ ] Se ha solicitado al leader la transición a `spec_ready`.
