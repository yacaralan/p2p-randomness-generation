# p2p-randomness-generation

Protocolo P2P para generación distribuida de aleatoriedad con equidad temporal acotada. Implementado con [go-libp2p](https://github.com/libp2p/go-libp2p).

## Requisitos

- Go 1.22 o superior

## Build

```bash
go build ./...
```

## Levantar un nodo

```bash
go run ./cmd/node [flags]
```

### Flags de línea de comandos

| Flag | Tipo | Default | Descripción |
|------|------|---------|-------------|
| `--port` | int | `0` (aleatorio) | Puerto TCP en el que el nodo escucha |
| `--peer` | string | — | Multiaddr(s) de bootstrap separadas por comas |
| `--vdf-t` | int | `1000` | Iteraciones T de la VDF de Wesolowski (controla el delay secuencial) |
| `--timeout-ready` | duration | `2s` | Timeout esperando READY\_ACK de todos los peers antes de bloquear sesión con los que respondieron |
| `--attacker` | string | `honest` | Perfil de atacante (ver sección [Simulación de atacantes](#simulación-de-atacantes)) |

**Valores orientativos para `--vdf-t`:**

| Valor | Uso |
|-------|-----|
| `1000` | Demo rápido, sin delay perceptible |
| `100 000` | Delay de décimas de segundo en hardware moderno |
| `1 000 000+` | Delay de segundos; útil para experimentos de equidad temporal |

### Parámetros hardcodeados

Estos valores no tienen flag CLI; para cambiarlos hay que editar el archivo indicado y recompilar.

| Parámetro | Archivo | Valor actual | Descripción |
|-----------|---------|-------------|-------------|
| Peer exchange interval | `node/node.go:205` | `5s` | Con qué frecuencia el nodo intenta conectarse a peers conocidos del peerstore. |
| Shutdown timeout | `cmd/node/main.go:102` | `5s` | Tiempo máximo de espera para el cierre limpio del nodo antes de forzar salida. |
| `valueSize` | `protocol/doublecommitreveal.go:14` | `32` bytes | Tamaño del secreto `s_i` que cada nodo aporta (256 bits). |

## Comandos interactivos

Una vez iniciado el nodo, el prompt acepta estos comandos:

### Red

| Comando | Descripción |
|---------|-------------|
| `/connect <multiaddr>` | Conectarse manualmente a un peer |
| `/peers` | Listar peers conectados |
| `/mesh` | Ver peers en la malla gossipsub por topic |

### Protocolo Commit-Reveal²

Los comandos deben ejecutarse **en orden** desde cualquier nodo de la red. Cada comando dispara la acción en todos los nodos simultáneamente via gossipsub.

| Comando | Descripción |
|---------|-------------|
| `/commit` | Fase 1 — cada nodo genera `s_i`, calcula `c_i = H(H(s_i))` y lo publica |
| `/reveal1` | Fase 2 — cada nodo publica `r_i = H(s_i)`; al completarse, se calcula el orden de reveal2 |
| `/order` | Muestra el orden de reveal2 calculado (mayor `d_i` primero) |
| `/reveal2` | Fase 3 — los nodos publican `s_i` en el orden calculado |
| `/vdf` | Computa la VDF de Wesolowski sobre la concatenación de los `s_i` en orden |
| `/values2` | Muestra todos los valores de cada fase (`c_i`, `r_i`, `s_i`) e input/output/proof de la VDF |

## Probar con dos nodos

Abrí dos terminales en el directorio del proyecto.

**Terminal 1:**

```bash
go run ./cmd/node --port 4001
```

La salida muestra el PeerID y las direcciones del nodo:

```
[node] PeerID: 12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[node] escuchando en: /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
```

**Terminal 2** — si ambos nodos están en la misma máquina, `LocalDiscovery` los conecta automáticamente via `/tmp/p2p-randomness` sin necesidad del flag `--peer`:

```bash
go run ./cmd/node --port 4002
```

Para conectarse a un nodo en otra máquina de la misma LAN o manualmente:

```bash
go run ./cmd/node --port 4002 --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID del nodo 1>"
```

### Ejemplo de ronda completa

Con ambos nodos conectados, desde **cualquiera de las dos terminales**:

```
/commit     # genera y publica c_i en todos los nodos
/reveal1    # publica r_i; al completarse calcula el orden automáticamente
/reveal2    # publica s_i en el orden calculado
/vdf        # computa la VDF sobre el input combinado
/values2    # muestra el resultado final
```

## Simulación de atacantes

El flag `--attacker` levanta el nodo con un perfil de comportamiento adversarial. Cada perfil implementa exactamente un ataque de la taxonomía de la tesis. El nodo muestra en amarillo cada acción adversarial que realiza.

```bash
go run ./cmd/node --attacker <perfil> [otros flags]
```

### Perfiles disponibles

#### Fase SESSION\_LOCK

| Perfil | Ataque | Efecto observable |
|--------|--------|-------------------|
| `no-ready-ack` | No responde READY\_ACK | El nodo es abortado por Timeout |

#### Fase Commit

| Perfil | Ataque | Efecto observable |
|--------|--------|-------------------|
| `commit-invalid` | Publica `c_i` sin preimagen válida | Su `reveal1` falla verificación `H(r_i) = c_i`; nodo excluido en reveal1 |
| `no-commit` | No envía commit2 | Timer de commit expira en otros nodos; voto por timeout → nodo abortado |
| `equivocate-commit` | Envía dos `c_i` distintos vía gossipsub | Otros nodos detectan equivocación → `EQUIVOCATION_ABORT` → nodo abortado sin votación |

#### Fase Reveal1

| Perfil | Ataque | Efecto observable |
|--------|--------|-------------------|
| `no-reveal1` | No envía reveal1 | Timer de reveal1 expira; voto por timeout → nodo excluido del revealOrder |
| `reveal1-invalid` | Envía `r_i'` con `H(r_i') ≠ c_i` | Verificación falla; nodo excluido del revealOrder |
| `last-revealer-abort` | Aborto estratégico — publica `r_i` solo si quedaría último | Difiere su reveal1 hasta ver los ajenos y calcula el orden hipotético; si no quedaría último en reveal2, aborta (excluido por timeout) |

#### Fase Reveal2

| Perfil | Ataque | Efecto observable |
|--------|--------|-------------------|
| `no-reveal2` | No revela en su turno | Timer de reveal2 expira; siguiente peer avanza; fallback `c_{o,i}` en input VDF |
| `reveal2-invalid` | Envía `s_i'` con `H(s_i') ≠ r_i` | Verificación falla; fallback activado |
| `last-revealer-abort-r2` | Aborto estratégico — siendo último, elige entre revelar `s_i` o abortar | Compara los dos inputs posibles de la VDF (revelar `s_i` vs abortar usando `r_i` como fallback), loguea ambos y elige la acción que produce el input numéricamente menor |
| `last-revealer-vdf` | Ventajista temporal del último revelador (early-VDF + delay) | Solo si es el último: inicia la VDF con el input completo apenas lo conoce y demora su reveal2 ~90% del timeout, maximizando su ventana exclusiva de precómputo (la ventaja temporal Δ) |

#### Mecanismo de timeout

| Perfil | Ataque | Efecto observable |
|--------|--------|-------------------|
| `false-timeout-vote` | Vota timeout contra nodos que sí respondieron | Nodos honestos disputan reenviando el mensaje firmado; con mayoría honesta, los votos falsos son neutralizados |

### Cómo experimentar: escenario básico

Levantá 3 nodos honestos y 1 atacante. Ejemplo con `no-commit`:

```bash
# Terminal 1 — nodo honesto
go run ./cmd/node --port 4001

# Terminal 2 — nodo honesto
go run ./cmd/node --port 4002 --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID-1>"

# Terminal 3 — nodo honesto
go run ./cmd/node --port 4003 --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID-1>"

# Terminal 4 — atacante
go run ./cmd/node --port 4004 --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID-1>" --attacker no-commit
```

Desde cualquier terminal honesta, iniciá el protocolo con `/start`. El atacante omitirá el commit; cuando expire el timer, los nodos honestos votarán por abortarlo y el protocolo continuará sin él.

> **Nota:** En la misma máquina, `LocalDiscovery` conecta todos los nodos automáticamente y no hace falta `--peer`.

### Combinaciones útiles para los experimentos de la tesis

| Pregunta | Configuración sugerida |
|----------|----------------------|
| ¿El mecanismo de disputa protege contra `false-timeout-vote`? | 3+ honestos + 1 `false-timeout-vote`; verificar que ningún honesto es abortado |
| ¿Cuánto adelanto real obtiene `last-revealer-vdf`? | 2 honestos + 1 `last-revealer-vdf` (debe quedar último en reveal2); comparar timestamps `[vdf] input obtenido` / `output obtenido` entre atacante y honestos |
| ¿Cuánto sesgo introduce `last-revealer-abort`? | Varias rondas con 1 `last-revealer-abort`; observar si el orden de reveal2 se desvía de la distribución uniforme |
| ¿Cuánto puede sesgar `last-revealer-abort-r2` el output? | Varias rondas con 1 `last-revealer-abort-r2`; comparar el input VDF resultante contra el de rondas sin atacante |
| ¿El fallback mantiene la ronda válida ante `no-reveal2`? | 2+ honestos + 1 `no-reveal2`; verificar que la VDF termina con `r_i` como fallback |

## Launcher de experimentos

Levantar nodos a mano en varias terminales sirve para explorar, pero para correr
experimentos repetibles está el **launcher** (`cmd/launcher`): a partir de un archivo
YAML levanta N nodos como procesos separados (honestos + atacantes con los mismos
parámetros de protocolo), los deja descubrirse por `LocalDiscovery`, arranca el
protocolo automáticamente tras una ventana de descubrimiento, y termina cuando **todos
los nodos honestos completan la VDF** o cuando vence un **timeout global** (lo que
ocurra primero), apagando todos los procesos.

### Uso

```bash
go run ./cmd/launcher --config experiments/launch.yaml
```

### Archivo de configuración

```yaml
# Parámetros de protocolo compartidos por TODOS los nodos.
protocol:
  vdf_t: 1000           # iteraciones (T) de la VDF
  timeout_ready: 2s     # espera de READY_ACK
  timeout_commit: 1s    # fase commit2
  timeout_reveal1: 1s   # fase reveal1
  timeout_reveal2: 1s   # por nodo en reveal2

discovery_delay: 5s     # ventana para que los nodos se descubran antes de arrancar
max_runtime: 60s        # timeout global de la corrida

nodes:
  honest: 4             # cantidad de nodos honestos (>= 1; el primero es el proponente)
  attackers:            # opcional: lista de perfiles con su cantidad
    - profile: last-revealer-vdf
      count: 1
```

El campo `profile` acepta cualquiera de los [perfiles de atacante](#perfiles-disponibles)
del flag `--attacker`.

## Estructura del proyecto

```
cmd/node/main.go              # Entry point: flags, wiring, loop interactivo
cmd/launcher/main.go          # Launcher de experimentos: levanta N nodos desde YAML
experiments/example.yaml      # Config de ejemplo para el launcher
node/
  options.go                  # Config struct y defaults
  node.go                     # Node: host libp2p, orquestación de subsistemas
  attacker.go                 # AttackerBehavior: interfaz + 12 perfiles de atacante simulado
discovery/
  mdns.go                     # Descubrimiento de peers via mDNS (LAN)
  local.go                    # Descubrimiento local via /tmp/p2p-randomness (misma máquina)
protocol/
  message.go                  # Tipos de mensaje de control y topics gossipsub
  pubsub.go                   # Gossipsub: topics y suscripciones
  doublecommitreveal.go       # Lógica del protocolo Commit-Reveal²
  vdf.go                      # VDF de Wesolowski: cómputo y verificación
```
