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
| `--peer` | string | — | Multiaddr de un peer de bootstrap para conectarse al iniciar |
| `--ping` | bool | false | Envía un Ping a todos los peers conectados cada 5 segundos |

## Probar con dos nodos

Abrí dos terminales en el directorio del proyecto.

**Terminal 1** — levantá el primer nodo:

```bash
go run ./cmd/node --port 4001 --ping
```

La salida muestra el PeerID y las direcciones del nodo:

```
[node] PeerID: 12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[node] escuchando en: /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[node] escuchando en: /ip4/192.168.0.45/tcp/4001/p2p/12D3KooWKViKtc2acrjneSZ7ufLDZEV4C8txxAp7sguWJDyT76cs
[discovery] mDNS iniciado, escuchando peers en la red local...
```

**Terminal 2** — levantá el segundo nodo apuntando al primero (reemplazá el PeerID):

```bash
go run ./cmd/node --port 4002 --ping --peer "/ip4/127.0.0.1/tcp/4001/p2p/<PeerID del nodo 1>"
```

Si ambos nodos están en la misma red local, mDNS los conecta automáticamente sin necesidad del flag `--peer`.

### Salida esperada

Nodo 1:
```
[discovery] peer encontrado: 12*Dmyfgi
[discovery] conectado a 12*Dmyfgi
[ping] enviado PING a 12*Dmyfgi
[ping] recibido PONG de 12*Dmyfgi: "hola de vuelta"
[handler] recibido PING de 12*Dmyfgi: "hola"
```

Nodo 2:
```
[node] conectado a bootstrap peer 12*yT76cs
[handler] recibido PING de 12*yT76cs: "hola"
[ping] enviado PING a 12*yT76cs
[ping] recibido PONG de 12*yT76cs: "hola de vuelta"
```

Presioná `Ctrl+C` en cualquier terminal para apagar el nodo.

## Estructura del proyecto

```
cmd/node/main.go      # Entry point: flags, wiring, loop principal
node/
  options.go          # Config struct y defaults
  node.go             # Node: crea el host libp2p, orquesta subsistemas
discovery/
  mdns.go             # Descubrimiento de peers via mDNS en red local
protocol/
  message.go          # Tipos de mensaje (MessageType, Message)
  handler.go          # Lógica de streams: handleStream (inbound) y Ping (outbound)
```
