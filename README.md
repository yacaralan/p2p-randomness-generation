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

| Flag | Tipo | Default | Descripción |
|------|------|---------|-------------|
| `--port` | int | 0 (aleatorio) | Puerto TCP en el que el nodo escucha |
| `--peer` | string | — | Multiaddr(s) de bootstrap separadas por comas |
| `--ping` | bool | false | Envía un Ping a todos los peers conectados cada 5 segundos |

Una vez iniciado, el nodo queda en modo interactivo: escribí un mensaje y Enter para enviarlo a todos los peers via gossipsub. También están disponibles los siguientes comandos:

| Comando | Descripción |
|---------|-------------|
| `/connect <multiaddr>` | Conectarse manualmente a un peer |
| `/peers` | Listar peers conectados |
| `/mesh` | Ver peers en la malla gossipsub por topic |

## Probar con dos nodos

Abrí dos terminales en el directorio del proyecto.

**Terminal 1** — levantá el primer nodo:

```bash
go run ./cmd/node --port 4001
```

La salida muestra el PeerID y las direcciones del nodo:

```
[node] PeerID: 12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[node] escuchando en: /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[discovery] mDNS iniciado, escuchando peers en la red local...
[local-discovery] iniciado, rendezvous en /tmp/p2p-randomness
[main] nodo corriendo. Escribí un mensaje y Enter para enviarlo a todos los peers. Ctrl+C para salir.
```

**Terminal 2** — levantá el segundo nodo. Si ambos nodos están en la misma máquina, `LocalDiscovery` los conecta automáticamente via `/tmp/p2p-randomness` sin necesidad del flag `--peer`:

```bash
go run ./cmd/node --port 4002
```

Para conectarse a un nodo en otra máquina de la misma LAN (mDNS) o manualmente:

```bash
go run ./cmd/node --port 4002 --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID del nodo 1>"
```

### Chat entre nodos

Una vez conectados, cualquier texto que escribas en una terminal aparece en la otra:

```
# Terminal 1
hola mundo
```

```
# Terminal 2
[chat] 12*yT76cs: hola mundo
```

Presioná `Ctrl+C` en cualquier terminal para apagar el nodo.

## Estructura del proyecto

```
cmd/node/main.go      # Entry point: flags, wiring, chat/ping interactivo
node/
  options.go          # Config struct y defaults
  node.go             # Node: crea el host libp2p, orquesta subsistemas
discovery/
  mdns.go             # Descubrimiento de peers via mDNS (LAN)
  local.go            # Descubrimiento local via /tmp/p2p-randomness (misma máquina)
protocol/
  message.go          # Tipos de mensaje (MessageType, Message)
  handler.go          # Lógica de streams: handleStream (inbound) y Ping (outbound)
  pubsub.go           # Gossipsub: topics commit/reveal/chat para broadcast
```
