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
| `--ping` | bool | `false` | Envía un Ping a todos los peers conectados cada 5 segundos |
| `--vdf-t` | int | `1000` | Iteraciones T de la VDF de Wesolowski (controla el delay secuencial) |

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
| `streamDeadline` | `protocol/handler.go:21` | `10s` | Timeout de I/O en streams directos (Ping/Pong). Si un peer no responde en este tiempo, el stream se cierra. |
| Peer exchange interval | `node/node.go:205` | `5s` | Con qué frecuencia el nodo intenta conectarse a peers conocidos del peerstore. |
| Ping interval | `cmd/node/main.go:254` | `5s` | Intervalo del loop de Ping cuando se usa `--ping`. |
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

### Chat

Cualquier texto que no empiece con `/` se publica como mensaje de chat broadcast a todos los peers.

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

## Estructura del proyecto

```
cmd/node/main.go              # Entry point: flags, wiring, loop interactivo
node/
  options.go                  # Config struct y defaults
  node.go                     # Node: host libp2p, orquestación de subsistemas
discovery/
  mdns.go                     # Descubrimiento de peers via mDNS (LAN)
  local.go                    # Descubrimiento local via /tmp/p2p-randomness (misma máquina)
protocol/
  message.go                  # Tipos de mensaje y topics gossipsub
  handler.go                  # Streams directos: handleStream (inbound) y Ping (outbound)
  pubsub.go                   # Gossipsub: topics y suscripciones
  doublecommitreveal.go       # Lógica del protocolo Commit-Reveal²
  vdf.go                      # VDF de Wesolowski: cómputo y verificación
```
