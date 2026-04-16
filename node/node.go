package node

import (
	"context"
	"fmt"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/discovery"
	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// Node representa un participante en la red P2P de generación de aleatoriedad.
//
// En esta primera iteración, un Node puede:
//   - Descubrir otros nodos en la red local (via mDNS)
//   - Conectarse a nodos conocidos (via BootstrapPeers)
//   - Intercambiar mensajes Ping/Pong con sus peers
//
// En iteraciones futuras, Node incorporará:
//   - Un gestor de rondas (node/round.go): coordina las fases del protocolo
//   - Un módulo VDF (vdf/): computa y verifica Verifiable Delay Functions
//   - Persistencia de identidad: guardar/cargar la clave privada del disco
//
// La separación entre New (constructor) y Start (ciclo de vida) es intencional:
// permite crear un Node en tests sin abrir puertos de red, y solo llamar
// Start en el binario final.
type Node struct {
	host      host.Host
	discovery *discovery.MDNSDiscovery
	handler   *protocol.Handler
	config    Config
}

// New crea un Node a partir de una Config pero no inicia ninguna conexión de red.
//
// Genera una nueva identidad Ed25519 para este nodo. En libp2p, la identidad
// de un nodo (su PeerID) se deriva de su clave pública. Esto significa que:
//   - Cada vez que se ejecuta el programa, el nodo tiene un PeerID diferente
//   - No hay forma de "hacerse pasar" por otro nodo sin tener su clave privada
//   - La autenticación entre peers es implícita: la conexión TLS/Noise usa estas claves
//
// En una iteración futura, guardaremos la clave en disco para tener
// un PeerID estable entre reinicios (importante para la reputación en la red).
func New(cfg Config) (*Node, error) {
	// Ed25519 es el algoritmo de firma estándar en libp2p.
	// El segundo parámetro (-1) indica "tamaño de clave por defecto" (irrelevante para Ed25519).
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("generar clave Ed25519: %w", err)
	}

	// ListenAddrStrings define en qué interfaz y puerto escuchará el nodo.
	// "0.0.0.0" significa "todas las interfaces de red disponibles".
	// El puerto 0 le pide al SO que asigne uno disponible automáticamente.
	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.Port)

	// libp2p.New crea el Host: la pieza central de un nodo libp2p.
	// Un Host gestiona:
	//   - Las conexiones TCP con otros peers
	//   - La negociación de protocolos (quién habla qué)
	//   - El multiplexing de streams sobre una misma conexión
	//   - La seguridad (TLS o Noise, negociado automáticamente)
	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
	)
	if err != nil {
		return nil, fmt.Errorf("crear host libp2p: %w", err)
	}

	return &Node{host: h, config: cfg}, nil
}

// Start inicia los subsistemas del nodo: registra el protocolo, arranca
// el descubrimiento mDNS, y conecta a los peers de bootstrap manuales.
//
// Después de llamar a Start, el nodo está activo y puede recibir conexiones.
// Bloqueá en main() usando una señal del SO (SIGINT) para mantenerlo vivo.
func (n *Node) Start(ctx context.Context) error {
	// Registramos el handler del protocolo /randomness/1.0.0.
	// A partir de este momento, cualquier peer que abra un stream con ese
	// protocol ID tendrá su stream despachado al handler correspondiente.
	n.handler = protocol.NewHandler(n.host)

	// Imprimimos el PeerID y las direcciones del nodo.
	// El PeerID es la identidad global del nodo en la red.
	// Las multiaddrs combinan la dirección IP, el puerto TCP y el PeerID
	// en un único string que otros nodos pueden usar para conectarse.
	fmt.Printf("[node] PeerID: %s\n", n.host.ID().String())
	for _, addr := range n.host.Addrs() {
		fmt.Printf("[node] escuchando en: %s/p2p/%s\n", addr, n.host.ID())
	}

	// Iniciamos el descubrimiento mDNS.
	// Esto lanza goroutines en segundo plano que anuncian este nodo
	// y detectan otros nodos en la red local automáticamente.
	disc, err := discovery.NewMDNSDiscovery(n.host)
	if err != nil {
		return fmt.Errorf("iniciar discovery: %w", err)
	}
	n.discovery = disc

	// Conectamos a los peers de bootstrap manuales (si los hay).
	// peer.AddrInfoFromString parsea una multiaddr completa (con PeerID)
	// y devuelve un AddrInfo que host.Connect puede usar.
	for _, addrStr := range n.config.BootstrapPeers {
		info, err := peer.AddrInfoFromString(addrStr)
		if err != nil {
			fmt.Printf("[node] multiaddr inválida %q: %v\n", addrStr, err)
			continue
		}
		if err := n.host.Connect(ctx, *info); err != nil {
			fmt.Printf("[node] error conectando a bootstrap peer %s: %v\n", info.ID.ShortString(), err)
		} else {
			fmt.Printf("[node] conectado a bootstrap peer %s\n", info.ID.ShortString())
		}
	}

	return nil
}

// Host expone el host libp2p subyacente.
// Necesario en cmd/node/main.go para consultar la lista de peers conectados
// y enviar Pings periódicos.
func (n *Node) Host() host.Host {
	return n.host
}

// Handler expone el handler del protocolo.
// Necesario en cmd/node/main.go para llamar a handler.Ping() manualmente.
func (n *Node) Handler() *protocol.Handler {
	return n.handler
}

// Close apaga el nodo ordenadamente: detiene el descubrimiento mDNS
// y cierra todas las conexiones TCP activas.
func (n *Node) Close() error {
	if n.discovery != nil {
		if err := n.discovery.Close(); err != nil {
			fmt.Printf("[node] error cerrando discovery: %v\n", err)
		}
	}
	return n.host.Close()
}
