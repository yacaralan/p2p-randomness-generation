package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/ayacar/p2p-randomness-generation/discovery"
	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// Node representa un participante en la red P2P.
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
	host           host.Host
	discovery      *discovery.MDNSDiscovery
	localDiscovery *discovery.LocalDiscovery
	handler        *protocol.Handler
	pubSub         *protocol.PubSub
	config         Config
}

// New crea un Node a partir de una Config pero no inicia ninguna conexión de red.
//
// Genera una nueva identidad Ed25519 para este nodo. En libp2p, la identidad
// de un nodo (su PeerID) se deriva de su clave pública. Esto significa que:
//   - Cada vez que se ejecuta el programa, el nodo tiene un PeerID diferente
//   - No hay forma de "hacerse pasar" por otro nodo sin tener su clave privada
//   - La autenticación entre peers es implícita: la conexión TLS/Noise usa estas claves
//
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
	//
	// Por defecto, libp2p.New habilita múltiples transportes: TCP, QUIC,
	// WebTransport y WebRTC. Para este protocolo solo necesitamos TCP.
	// Deshabilitar los demás tiene dos beneficios concretos:
	//   1. El nodo escucha en una sola dirección (más simple de leer en logs)
	//   2. host.Close() termina inmediatamente en lugar de esperar el cierre
	//      de cada transporte (QUIC en particular tarda ~5s en cerrar por diseño
	//      del protocolo para enviar los paquetes de fin de conexión)
	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.NoTransports,
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
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

	// Registramos un notificador de red para loggear conexiones entrantes.
	// DirInbound indica que el peer remoto fue quien inició la conexión,
	// es decir, que ese nodo nos descubrió a nosotros.
	n.host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			if conn.Stat().Direction == network.DirInbound {
				fmt.Printf("[node] descubierto por %s\n", conn.RemotePeer().ShortString())
			}
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			fmt.Printf("[node] desconectado de %s\n", conn.RemotePeer().ShortString())
		},
	})

	// Iniciamos gossipsub antes que mDNS: gossipsub necesita estar listo
	// para aceptar peers cuando mDNS empiece a conectarlos.
	ps, err := protocol.NewPubSub(ctx, n.host)
	if err != nil {
		return fmt.Errorf("iniciar pubsub: %w", err)
	}
	n.pubSub = ps

	// Iniciamos el descubrimiento mDNS (funciona en LAN).
	disc, err := discovery.NewMDNSDiscovery(n.host)
	if err != nil {
		return fmt.Errorf("iniciar discovery: %w", err)
	}
	n.discovery = disc

	// Iniciamos el descubrimiento local via /tmp (funciona en la misma máquina).
	// Complementa mDNS: resuelve el problema de multicast en loopback en macOS.
	ld, err := discovery.NewLocalDiscovery(ctx, n.host)
	if err != nil {
		return fmt.Errorf("iniciar local discovery: %w", err)
	}
	n.localDiscovery = ld

	// Conectamos a los peers de bootstrap manuales (si los hay).
	for _, addrStr := range n.config.BootstrapPeers {
		if err := n.ConnectPeer(ctx, addrStr); err != nil {
			fmt.Printf("[node] bootstrap peer %q: %v\n", addrStr, err)
		}
	}

	// Peer exchange: periódicamente intenta conectar a todos los peers que el
	// protocolo identify de libp2p fue poblando en el peerstore. Esto permite
	// que la red forme una malla completa sin configuración manual adicional.
	go n.peerExchangeLoop(ctx)

	return nil
}

// ConnectPeer conecta a un peer por su multiaddr completa (con PeerID).
// Se puede llamar tanto en bootstrap como dinámicamente desde stdin.
func (n *Node) ConnectPeer(ctx context.Context, addrStr string) error {
	info, err := peer.AddrInfoFromString(addrStr)
	if err != nil {
		return fmt.Errorf("multiaddr inválida: %w", err)
	}
	if err := n.host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("conectando a %s: %w", info.ID.ShortString(), err)
	}
	fmt.Printf("[node] conectado a %s\n", info.ID.ShortString())
	return nil
}

// peerExchangeLoop escanea periódicamente el peerstore e intenta conectar a
// peers conocidos que aún no están conectados. El protocolo identify (habilitado
// por defecto en libp2p) llena el peerstore con los peers de los vecinos,
// lo que permite construir automáticamente una malla completa.
func (n *Node) peerExchangeLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, p := range n.host.Peerstore().Peers() {
				if p == n.host.ID() {
					continue
				}
				if n.host.Network().Connectedness(p) == network.Connected {
					continue
				}
				addrs := n.host.Peerstore().Addrs(p)
				if len(addrs) == 0 {
					continue
				}
				if err := n.host.Connect(ctx, peer.AddrInfo{ID: p, Addrs: addrs}); err == nil {
					fmt.Printf("[node] peer exchange: conectado a %s\n", p.ShortString())
				}
			}
		case <-ctx.Done():
			return
		}
	}
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

// PubSub expone el subsistema de gossipsub.
func (n *Node) PubSub() *protocol.PubSub {
	return n.pubSub
}

// Close apaga el nodo cerrando el host libp2p y el servicio mDNS en paralelo.
//
// Los cerramos concurrentemente porque son independientes entre sí: no tiene
// sentido esperar a que mDNS termine de enviar sus paquetes de despedida
// antes de empezar a cerrar el host, ni viceversa.
func (n *Node) Close() error {
	var wg sync.WaitGroup

	if n.pubSub != nil {
		n.pubSub.Close()
	}

	if n.localDiscovery != nil {
		if err := n.localDiscovery.Close(); err != nil {
			fmt.Printf("[node] error cerrando local discovery: %v\n", err)
		}
	}

	if n.discovery != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.discovery.Close(); err != nil {
				fmt.Printf("[node] error cerrando discovery: %v\n", err)
			}
		}()
	}

	wg.Add(1)
	var hostErr error
	go func() {
		defer wg.Done()
		hostErr = n.host.Close()
	}()

	wg.Wait()
	return hostErr
}
