# AGENTS.md - FeatureProof

> Este archivo es el punto de entrada para cualquier agente que trabaje en este
> repositorio. Lee solo lo que necesites cuando lo necesites.

## 1. Contexto de producto obligatorio

FeatureProof es una plataforma de validación bajo demanda que permite a un equipo describir qué condiciones debe cumplir una nueva funcionalidad y obtener evidencias de su funcionamiento en una aplicación realmente desplegada.
La plataforma no se ejecuta en cada commit ni pretende sustituir toda la suite de CI. El equipo solicita una validación cuando considera que una feature está preparada, por ejemplo:
- mediante un comando en una pull request;
- añadiendo una etiqueta;
- desde la CLI;
- o mediante una API.
El sistema crea o reserva un entorno aislado, analiza las restricciones de la feature, ejecuta comprobaciones permitidas y publica un informe verificable.

Antes de redactar una spec o implementar una integración:

1. Lee `feature_list.json`. Toda feature nueva (`"sdd": true`) pasa por
   **Spec Driven Development**; consulta `docs/specs.md`.
2. Lee `docs/specs.md` antes de modificar una spec o una feature.

## 2. Mapa del repositorio

| Archivo / carpeta | Qué contiene | Cuándo leerlo |
|------------------------------|--------------------------------------------------------------------------------------------|---------------------|
| `feature_list.json`          | Lista de tareas con estado (`pending` / `spec_ready` / `in_progress` / `done` / `blocked`) | Siempre, al empezar |
| `progress/history.md`        | Bitácora append-only de sesiones anteriores                                                | Si necesitas contexto histórico |
| `specs/<feature>/`           | `requirements.md` + `design.md` + `tasks.md` (Kiro-style)                                  | Antes de implementar cualquier feature con `"sdd": true` |
| `docs/architecture.md`       | Qué significa "hacer un buen trabajo" en este proyecto                                     | Antes de implementar |
| `docs/specs.md`              | Proceso SDD: EARS notation, los 3 archivos, puerta de aprobación humana                    | Antes de redactar o leer un spec |
| `docs/verification.md`       | Cómo verificar que tu trabajo funciona (incluye trazabilidad requirements)                 | Antes de declarar una tarea como `done` |
| `.codex/agents/`             | Definiciones de subagentes (`leader`, `spec_author`, `implementer`, `reviewer`)            | Si orquestas trabajo |
| `cmd/`                       | Entradas ejecutables de la aplicación                                                      | Para implementar entrypoints |
| `internal/`                  | Paquetes internos de dominio, aplicación y adapters                                        | Para implementar comportamiento |

## 3. Reglas no negociables
- **Una sola feature a la vez**: no mezcles cambios de varias tareas en la misma
  sesión.
- **No declares una feature como `done` sin pruebas verídicas**: asegúrate de
  que todas las verificaciones obligatorias pasan.
- **No saltes la fase de spec**: toda feature con `"sdd": true` debe pasar por
  el spec author y obtener aprobación humana antes de tocar código.
- **Documenta lo que haces en `progress/current.md`** mientras trabajas, no al
  final.
- **Solo el leader cambia estados en `feature_list.json`**: los demás agentes
  producen artefactos y solicitan una transición.
- **Si no sabes algo**, busca en `docs/` antes de inventarlo.

## 4. Flujo de trabajo
pending → [spec_author] → spec_ready → ⏸ HUMANO → in_progress → [implementer → reviewer] → done

1. El leader detecta la primera feature `pending` con `"sdd": true`.
2. El leader lanza `spec_author`, que crea
   `specs/<feature>/{requirements,design,tasks}.md` y solicita la transición a
   `spec_ready`.
3. **Pausa.** El humano lee el spec en `specs/<feature>/` y aprueba (o pide
   cambios).
4. Una vez aprobado, el leader cambia el status a `in_progress` y lanza `implementer`.
5. El implementer ejecuta `tasks.md` una a una, marcándolas `[x]`.
6. El reviewer verifica trazabilidad `R<n>` ↔ test y tasks completas; aprueba o rechaza.
7. Si aprueba, el leader marca `done` y añade el resumen a
   `progress/history.md`.
