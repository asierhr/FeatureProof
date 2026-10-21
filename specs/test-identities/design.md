# Identidades seguras de prueba — Design

## Resumen de la solución

La solución separa tres representaciones: declaración confiable, descriptor
para planificación y material secreto efímero. La specification v2 declara
identidades y referencias opacas; un catálogo inmutable produce descriptores
sin secretos y valida la selección del plan; finalmente un transporte HTTP
autenticado resuelve e inyecta bytes solo tras plan, operation binding, target,
policy y budget.

```text
specification v2                      composición confiable
  identities + opaque refs                 ref -> resolver
            │                                      │
            ▼                                      │
  IdentityCatalog ── safe descriptors ──> model    │
            │                  │ proposed ID        │
            ▼                  ▼                    │
 plan/materializer -> operation binding -> validator
                                           │
                       policy + budget + target recheck
                                           │
                                           ▼
                                authenticated transport
                                  resolve -> validate
                                  inject -> size check
                                           │
                                           ▼
                                    HTTP exchange
                                           │
                                sanitize before evidence
```

La credencial no forma parte de `Specification`, `PlanStep`, `ToolCall`,
`Evidence`, eventos ni DTO externos. Es un buffer efímero propiedad de la
frontera autenticada y se libera al terminar cada intento.

## Fuentes de contexto consultadas

| Fuente | Decisión aplicada |
|---|---|
| `AGENTS.md`, `feature_list.json` | Alcance de feature 25, aceptación, única feature y puerta SDD. |
| `.codex/agents/spec.md`, `docs/specs.md` | Artefactos Kiro, EARS, bloqueo, trazabilidad y aprobación humana. |
| `docs/architecture.md` | Modelo no autoritativo, registry/policy antes de efectos, secretos fuera del modelo y evidencia tratada. |
| `docs/conventions.md` | Interfaces del consumidor, context, errores tipados, dependencias explícitas, tests herméticos y comentarios sistémicos. |
| `internal/specification` y adapter YAML | Contrato v1 estricto, modelos internos y necesidad de despachar una v2 sin reinterpretar fixtures v1. |
| `internal/planmanifest`, `internal/planning`, `internal/agentplanning` | Planes canónicos, resolución de inputs, operación HTTP retenida y catálogo descriptor-only. |
| `internal/policy`, `internal/harness` | `ToolCall`, evaluación previa, budgets, journal y separación entre fallo operativo y funcional. |
| `internal/tools/http` | Scope de target, validación de inputs, DNS/dial controlados, redirects y prohibición actual de headers sensibles. |
| `internal/evidence` | Sanitizer fail-closed, records sellados y proyección model-only. |
| `internal/localvalidation`, `internal/validationworkflow` | Composición del runner, artefactos privados, binding a revisión/target y recovery durable. |
| `internal/externalagent`, `internal/githubpr`, `cmd/featureproof` | Superficies CLI/API/MCP/GitHub que deben compartir el servicio y no recibir secretos. |
| Specs 2, 4, 5, 6, 9, 10, 12, 17, 18, 19 y 24 | Compatibilidad de specification, planes, HTTP, redacción, runtime de modelo, workflow, MCP, GitHub, preview y contratos aprobados. |

No se consultó documentación externa. La feature define contratos internos y
usa `net/http`; los secret backends concretos quedan detrás de un puerto y no
se fija un protocolo de proveedor.

## Contratos externos observados

### Specification YAML v2

La v1 permanece sin cambios. La v2 añade `identities` al documento estricto:

```yaml
version: 2
identities:
  - id: customer
    target_id: deployed
    credential_ref: cred_customer
    mechanism:
      type: bearer
    operations:
      - capability: http.request
        method: GET
        path: /api/accounts/{account_id}
```

`identities` es una lista explícita y puede estar vacía. Cada identidad exige
al menos una operación. `credential_ref` usa la misma gramática acotada de IDs,
es case-sensitive y no acepta `/`, `\\`, `:`, `$`, prefijos de environment ni
URIs. Se interpreta únicamente como clave opaca.

Los mecanismos v2 son una unión cerrada:

- `bearer`: no tiene configuración adicional y usa `Authorization: Bearer …`;
- `header`: exige un nombre de header no reservado y no permite
  `Authorization`, `Cookie`, framing ni hop-by-hop;
- `cookie`: exige un nombre de cookie válido y produce una única cookie
  dedicada sin aceptar una cabecera `Cookie` libre.

El valor secreto nunca existe en YAML. Campos desconocidos, claves repetidas,
miembros de unión incompatibles, permisos duplicados o versiones desconocidas
se rechazan como en v1.

### Plan y operación

`planning.PlanStep` incorpora `IdentityID` opcional y un
`AuthorizedOperation` no secreto con capability, target ID, método y path
template canónicos. Para una operación marcada como autenticada por el contrato
aprobado o la configuración confiable, ambos son obligatorios. Para HTTP, el
materializador de `agentplanning` deriva el binding de su
`OperationReference`; el modelo solo elige un identity ID anunciado.

El manifest local evoluciona mediante una versión nueva para transportar estos
campos sin cambiar la interpretación del JSON v1. API, MCP y GitHub continúan
enviando referencias a specification/configuración y comandos de workflow; sus
payloads no añaden un campo de secreto ni de credential reference.

### Contrato de credenciales

El paquete consumidor define:

- `CredentialResolver.Resolve(ctx, ResolutionRequest) (CredentialLease, error)`;
- `ResolutionRequest`: identity ID, referencia opaca interna, mecanismo,
  repository/project/run y operation binding ya autorizados;
- `CredentialLease`: material secreto en bytes, mecanismo confirmado, instante
  de expiración opcional y función idempotente de liberación;
- `AuthenticatedRoundTripper`: wrapper terminal que resuelve, verifica, clona la
  request, inyecta y delega al transporte de red ya acotado.

Ni request ni lease son serializables. Los errores públicos contienen solo
categoría, fase y field path. Las categorías iniciales son `unknown_identity`,
`identity_not_allowed`, `credential_unavailable`, `credential_expired`,
`mechanism_mismatch`, `request_limit`, `cancelled`, `timeout` e `internal`.

## Componentes y responsabilidades

### `internal/specification`

Añade `TestIdentity`, `AuthenticationMechanism` y `AuthorizedOperation` como
modelos propios. El validador comprueba IDs, targets, unión de mecanismo,
operaciones y duplicados. El adapter YAML conserva DTO privados v1/v2 y
despacha por versión antes de decodificar el cuerpo.

### `internal/testidentity`

Paquete cohesionado propietario de la seguridad de identidades:

- `Catalog`: snapshot inmutable por ID, validación exacta de operación y
  producción de descriptores seguros.
- `Descriptor`: única representación permitida en contexto/modelo.
- `Binding`: identidad + operación canónica, sin referencia ni secreto.
- `CredentialResolver`: puerto mínimo definido por el consumidor.
- `CredentialLease`: propietario temporal de bytes y liberación.
- `Transport`: autenticación tardía, vigencia, límites finales y traducción de
  errores.
- `Error`: categorías estables y causas publicables allowlisted.
- `Observer`: recibe un record terminal payload-free por resolución/intento.

El catálogo no ejecuta, no lee environment/files y no resuelve credenciales.
El transporte no decide policy ni descubre operaciones.

### `internal/agentplanning` y `internal/planning`

El descriptor de capability incorpora las identidades seguras aplicables a
cada operación. El protocolo acepta solo `identity_id`; el decoder rechaza
campos de credencial. Provenance/canonicalización incluyen identity ID y
operation binding, nunca la referencia o material. El validator comprueba el
catálogo antes de producir `PreparedPlan`.

El executor conserva los controles actuales y añade un guard de identidad
entre input resolution y policy, seguido de un checkpoint tras policy/budget.
Construye un `ToolCall` con binding no secreto. El tool call puede registrarse y
sanearse sin contener auth material.

### `internal/tools/http`

El tool mantiene target, método, path, DNS y redirects. Recibe un transporte
autenticado por composición y una identidad ya validada. Los headers sensibles
siguen prohibidos en input de plan; solo el wrapper terminal puede añadirlos.
Después de inyectar se miden los headers finales. Redirects vuelven a pasar por
la autorización exacta y eliminan credenciales antes de evaluar otro hop.

### `internal/evidence` y journal

La policy de redacción se amplía automáticamente con nombres de headers,
cookies y JSON paths de autenticación definidos por composición. La ampliación
es monotónica: una configuración de proyecto puede añadir campos, nunca retirar
los sensibles estándar. La evidencia recibe input pre-auth y output saneado.

Los eventos añaden identity ID, operation-binding digest, intento y categoría
segura. No añaden credential ref, mecanismo con valores, expiry exacta o
errores externos. La proyección hacia el modelo vuelve a sanear records ya
tratados.

### Composición y superficies

`localvalidation` y `validationworkflow` construyen un catálogo desde la
specification validada, unen resolvers ya configurados y sellan el digest de
identidades/permisos dentro del contexto de ejecución. Recovery restaura el
binding no secreto y vuelve a resolver el material; no persiste leases.

CLI, API, MCP y GitHub continúan delegando en el mismo workflow. Sus schemas
estrictos permiten seleccionar specification/configuración ya autorizadas, no
valores ni fuentes secretas. El webhook solo puede usar el snapshot vinculado al
repository/head/preview; texto de PR y comentarios no cambia identidades.

## Paquetes afectados

```text
internal/specification/           # modelos v2, validación y DTO YAML v2
internal/testidentity/            # catálogo, binding, resolver, lease, transport, errores
internal/agentplanning/           # descriptores y selección identity_id
internal/planning/                # PlanStep/binding y guard pre-policy/pre-I/O
internal/planmanifest/            # contrato JSON nuevo sin reinterpretar v1
internal/tools/http/              # transporte inyectable y límites post-auth
internal/evidence/                # redacción monotónica de auth
internal/harness/                 # metadata segura en ToolCall/Event
internal/localvalidation/         # composición determinista local
internal/validationworkflow/      # binding, recovery y ejecución compartida
internal/externalagent/           # regresión de schemas API/MCP sin secretos
internal/githubpr/                # composición head-bound sin selección desde texto
cmd/featureproof/                 # wiring de resolvers autorizados, sin flags secretos
```

La distribución exacta en archivos puede variar manteniendo estas
responsabilidades. No se crea un secret backend concreto ni un nuevo servicio
de red.

## Flujo principal

1. El loader identifica v2, decodifica estrictamente identidades y valida sus
   targets, mecanismos y operaciones.
2. Composición construye `IdentityCatalog` y enlaza cada credential reference
   con un resolver registrado, sin solicitar todavía material.
3. El context builder filtra descriptores por target/operación y los entrega al
   planificador como datos confiables separados de contenido externo.
4. El modelo o manifest selecciona `identity_id`; el materializador obtiene la
   operación del contrato confiable y crea el binding no secreto.
5. Planning valida plan, capability, target, operación e identidad. Approval y
   workflow sellan el digest del catálogo/bindings junto con la revisión.
6. El executor resuelve inputs, revalida el binding, evalúa policy y reserva
   budget. Comprueba cancelación antes de continuar.
7. El HTTP tool valida request, destino y límites sin auth material.
8. El transporte resuelve la referencia, comprueba mecanismo/vigencia, clona e
   inyecta el header o cookie y comprueba los límites finales.
9. El transporte acotado realiza DNS/dial/exchange. En redirect, repite el guard
   y no propaga credenciales fuera del binding.
10. El lease se libera. Input/output y errores se saneen antes de evidence,
    journal, modelo o reporte; assertions usan solo evidencia confirmada.

## Flujos de error

- **Specification/manifest inválidos:** error tipado antes de composición; cero
  resolver/modelo/red.
- **Referencia sin resolver registrado:** fallo de composición fail-closed; no
  se prueba otra identidad.
- **Selección ausente/desconocida/no autorizada:** rechazo de plan o paso antes
  de policy y red.
- **Drift:** invalida el binding antes de approval/ejecución y no resuelve
  credenciales.
- **Policy/budget/cancelación previos:** no se llama al resolver.
- **Material ausente/expirado/mismatch:** error operacional antes de DNS/dial;
  liberación una vez si existe lease.
- **Inyección/límite final:** request rechazada antes de red y sin formatear el
  valor.
- **Target/redirect/transporte:** credencial permanece en el wrapper, se sanea
  cualquier error y el resultado es operacional salvo assertions explícitas
  sobre una respuesta real.
- **Redacción/persistencia:** fallo cerrado; no se publica evidencia parcial ni
  se envía payload al modelo.

## Estados y transiciones

Una identidad declarada no tiene lifecycle mutable dentro del run. Cada intento
usa estados efímeros:

```text
declared -> bound -> authorized -> resolving -> injected -> exchanged -> released
                \-> rejected
authorized -> cancelled
resolving/injected/exchanged -> failed -> released
resolving/injected/exchanged -> cancelled -> released
```

`bound` y el resultado terminal son auditables; `resolving` e `injected` no
persisten payload. Solo un intento que alcanzó una respuesta real puede producir
evidencia evaluable.

## Propiedad del estado

- `Specification` posee declaraciones sin material secreto.
- `IdentityCatalog` posee copias inmutables y la relación privada ID→reference;
  sus proyecciones nunca contienen esa relación.
- El workflow posee el binding no secreto y su digest durante el run.
- Cada llamada de transporte posee un lease y una copia de request; no comparte
  buffers con otro intento.
- El resolver concreto posee el secret backend. Ni core ni adapters externos
  reciben su handle.
- Evidence store y journal solo poseen datos ya saneados y sellados.

## Concurrencia y cancelación

No se crean goroutines desde constructores. Resolver e injector aceptan
`context.Context` como primer parámetro. El executor revisa contexto antes y
después de policy/budget; el transporte lo revisa después de adquirir un lease y
justo antes de delegar a red. Liberación usa una operación idempotente acotada y
no prolonga indefinidamente el contexto cancelado.

Un resolver puede coalescer acceso a la misma referencia, pero locks protegen
solo estado corto y nunca permanecen durante callbacks, red o disco. Cada
waiter conserva su cancelación. Tests usan barriers/channels y reloj controlado,
nunca `time.Sleep` para coordinar.

## Retries e idempotencia

No existe retry oculto en resolución ni transporte. El executor conserva la
política actual: solo reintenta errores transitorios cuando la operación es
segura y queda budget. Cada intento vuelve a revalidar binding y obtiene un
lease; un error permanente de identidad no se reintenta.

La credencial y su digest no forman parte de claves de idempotencia, artifacts
o evidencia. La identidad y operation binding sí forman parte del plan aprobado
y del contexto de ejecución. Una redelivery confirmada recupera estado; una
pendiente resuelve de nuevo sin persistir el secreto.

## Observabilidad

`IdentityAttemptRecord` contiene run/plan/step/tool-call IDs, identity ID,
operation-binding digest, ordinal, duración, outcome y categoría. No contiene
credential ref, valores, header/cookie, exact expiry, request/response ni error
externo. El observer confirma un record terminal por intento y no se usa como
estado.

Logs, métricas, CLI, API, MCP y GitHub emplean allowlists. Los tests recorren
recursivamente errores, events, evidence, artifacts, model requests y outputs
externos buscando canarios distintos por superficie.

## Convención de documentación del código

Toda entidad, constante, función, método y helper nuevo o modificado, exportado
o interno, incluirá un comentario breve que explique su funcionalidad dentro de
FeatureProof y la frontera o invariante que protege. La regla incluye DTOs,
adapters, fakes, fixtures programáticas y helpers de test. Los comentarios se
actualizan cuando cambie la responsabilidad y no se limitan a repetir la
sintaxis, nombre o parámetros.

## Estrategia de pruebas

Todos los tests ordinarios son herméticos. Usan resolver fake con leases
canario, transporte/dialer spies, servidor HTTP local, reloj controlado,
barriers, journals/stores temporales y modelo falso. No leen secretos reales,
environment, almacenes del usuario ni servicios externos.

| Requirement | Componentes | Pruebas previstas |
|---|---|---|
| R1 | specification/YAML | `TestV2LoadsTypedIdentitiesWithoutCredentialValues`; `TestV2RejectsIdentityFieldsReferencesMechanismsAndOperations`; `TestV1CompatibilityRemainsStrict` |
| R2 | catálogo/planning/materializer | `TestAuthenticatedOperationRequiresExactDeclaredIdentityBinding`; `TestIdentityBindingRejectsTargetMethodPathAndTemplateConfusion` |
| R3 | catálogo/context/agentplanning | `TestModelReceivesOnlyApplicableSafeIdentityDescriptors`; `TestModelCannotInventOrMutateIdentitySelection` |
| R4 | executor/http transport | `TestCredentialInjectionOccursAfterAllGuardsAndOnlyInsideTransport`; `TestInjectedLimitsAndRedirectAuthorizationAreRechecked` |
| R5 | resolver/lease | `TestCredentialLeasesAreIsolatedValidAndReleasedPerAttempt`; `TestMissingExpiredAndMismatchedMaterialNeverDials` |
| R6 | evidence/context/adapters | `TestAuthenticationCanariesNeverCrossPublishableSurfaces`; `TestAuthRedactionIsCaseInsensitiveMonotonicAndFailClosed` |
| R7 | executor/workflow | `TestIdentityFailuresAreTypedOperationalAndNeverFunctionalFailures`; `TestRejectedIdentityProducesZeroNetworkEffects` |
| R8 | binding/retry/recovery | `TestIdentityBindingDriftStopsBeforeCredentialResolution`; `TestRetryAndRecoveryRevalidateAndResolveWithoutPersistingSecrets` |
| R9 | CLI/API/MCP/GitHub | `TestAllSurfacesShareIdentityAwareWorkflowWithoutSecretInputs`; `TestGitHubTextCannotSelectIdentityOrCredential` |
| R10 | journal/observer | `TestIdentityAuditContainsOnlySafeBindingFactsInOrder`; `TestCredentialRotationDoesNotAlterHistoricalSafeBindings` |
| R11 | all blocking boundaries | `TestCancellationAtEveryIdentityBoundaryStopsAndReleases`; `TestConcurrentIdentityResolutionIsRaceSafeAndIsolated` |

Además se cubren union fields, duplicate keys, case sensitivity, empty lists,
multiple eligible identities with no implicit choice, exact limit boundaries,
401/403 with valid credentials, errors containing canaries, cancellation
simultaneous with lease acquisition, redirects and repeated runs under `-race`.

## Alternativas y decisiones

- **Añadir `identities` a YAML v1:** descartado porque una clave antes
  desconocida pasaría a ser válida y rompería la compatibilidad estricta. Se
  crea v2 y se mantiene v1 sin cambios.
- **Guardar la credencial en `PlanStep.Input`:** descartado porque llegaría al
  modelo, policy, journal y evidence y permitiría inventarla o copiarla.
- **Resolver en el loader:** descartado porque alargaría la vida del secreto y
  mezclaría parsing con I/O externo. Composición registra referencias; el
  transporte resuelve por intento.
- **Seleccionar automáticamente la única identidad compatible:** descartado;
  la selección debe quedar explícita y auditable.
- **Autorizar por prefijo de path:** descartado por confusiones como `/api` y
  `/apix`. Se usa template estructural canónico y parámetros acotados.
- **Permitir `Authorization`/`Cookie` en input del plan:** descartado. Los
  headers sensibles permanecen reservados para el transporte.
- **Incluir credential-ref en el descriptor:** descartado; el modelo no necesita
  conocer almacenamiento ni composición.
- **Persistir un hash del secreto:** descartado; aporta superficie de ataque y
  no es necesario para auditar identidad/operación.
- **Soportar todos los mecanismos desde v2:** descartado. Bearer, header y
  cookie cubren la primera frontera HTTP; mecanismos con criptografía,
  handshake o modificación de body necesitan contratos propios.
- **Clasificar 401/403 siempre como fallo de auth local:** descartado. Con una
  credencial válida son respuestas observadas que assertions explícitas pueden
  evaluar; resolución/inyección local fallida continúa siendo operacional.

## Conformidad arquitectónica

El diseño preserva ARCH-001/002 al limitar al modelo a descriptores y someter
cada selección a registry/policy; ARCH-003/004 porque solo una respuesta saneada
puede alimentar assertions; ARCH-005 mediante budgets; ARCH-006 mediante
contextos; ARCH-007 con eventos seguros; ARCH-008/009 al separar referencias,
material y datos no confiables; ARCH-010 con fakes herméticos; ARCH-011 con
policy no anulable; ARCH-012 porque el transporte no muta el run.

Cumple CONV-001 a CONV-010: paquete cohesionado, interfaces del consumidor,
errores tipados, cancelación, ciclos de vida explícitos, cero secretos en
artefactos y tests deterministas. No se solicita excepción arquitectónica.

## Mapa inicial R → componente/prueba

| Requirement | Tasks | Resultado verificable |
|---|---|---|
| R1 | T1, T2, T13 | YAML v2 tipado y estricto; v1 conserva semántica; ningún valor secreto es aceptado. |
| R2 | T3, T5, T13 | Toda operación autenticada lleva una selección exacta validada. |
| R3 | T3, T4, T13 | El modelo recibe solo descriptores aplicables e inmutables. |
| R4 | T5, T6, T13 | Resolver/injector ocurren tras controles y solo dentro del transporte. |
| R5 | T6, T7, T13 | Leases válidos, aislados, por intento y liberados. |
| R6 | T8, T11, T12, T13 | Ningún canario cruza superficies persistidas o externas. |
| R7 | T5, T7, T9, T13 | Fallos tipados operacionales producen cero red y cero falso fallo funcional. |
| R8 | T5, T9, T10, T13 | Drift/retry/recovery revalidan sin persistir secretos. |
| R9 | T10, T11, T13 | CLI/API/MCP/GitHub comparten servicio y rechazan secret inputs. |
| R10 | T9, T12, T13 | Auditoría ordenada contiene identidad/binding, no credenciales. |
| R11 | T6, T7, T9, T12, T13 | Cancelación/concurrencia terminan, liberan y no filtran datos. |
