// Package discovery implementa el descubrimiento de peers en la red local
// usando mDNS (Multicast DNS).
//
// ¿Qué es el descubrimiento de peers y por qué es necesario?
// En una red P2P no hay un servidor central que lleve el registro de quién
// está conectado. Cada nodo nuevo necesita encontrar a otros por su cuenta.
// mDNS resuelve esto para redes locales (LAN/WiFi): los nodos anuncian
// su presencia periódicamente en la red local usando multicast UDP,
// y escuchan los anuncios de otros.
//
// Ventajas de mDNS para desarrollo:
//   - No requiere configuración: funciona automáticamente en cualquier red local
//   - No requiere un servidor de bootstrap conocido de antemano
//   - Es el mismo mecanismo que usan dispositivos como Chromecasts, impresoras, etc.
//
// Limitación: solo funciona dentro de la misma red local (no cruza routers).
// Para redes globales, se usaría DHT (Kademlia), que se implementará
// en una iteración futura en discovery/dht.go.
package discovery

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

// ServiceTag es el nombre del servicio mDNS que usamos para identificar
// nuestra red de generación de aleatoriedad.
//
// Solo los nodos que usen el mismo ServiceTag se van a "ver" entre sí.
// Esto actúa como un namespace: podríamos tener múltiples grupos de nodos
// en la misma red local sin interferencia, usando tags distintos.
const ServiceTag = "randomness-p2p"

// MDNSDiscovery encapsula el servicio mDNS de libp2p.
// Una vez iniciado, trabaja en segundo plano: anuncia este nodo en la red
// local y llama a HandlePeerFound cada vez que descubre un peer nuevo.
type MDNSDiscovery struct {
	service mdns.Service
}

// NewMDNSDiscovery crea e inicia el servicio de descubrimiento mDNS.
//
// Internamente, go-libp2p lanza goroutines que:
//  1. Emiten anuncios mDNS periódicos con las direcciones de este nodo
//  2. Escuchan anuncios de otros nodos en la misma red local
//  3. Llaman a discoveryNotifee.HandlePeerFound cuando encuentran uno nuevo
func NewMDNSDiscovery(h host.Host) (*MDNSDiscovery, error) {
	// discoveryNotifee es el "callback" que se invoca al encontrar un peer.
	// Lo pasamos al servicio mDNS para que lo llame automáticamente.
	n := &discoveryNotifee{host: h}

	svc := mdns.NewMdnsService(h, ServiceTag, n)
	if err := svc.Start(); err != nil {
		return nil, fmt.Errorf("iniciar mDNS: %w", err)
	}

	fmt.Println("[discovery] mDNS iniciado, escuchando peers en la red local...")
	return &MDNSDiscovery{service: svc}, nil
}

// Close detiene el servicio mDNS y libera los recursos asociados.
// Debe llamarse cuando el nodo se apaga (ver node.Close()).
func (d *MDNSDiscovery) Close() error {
	return d.service.Close()
}

// discoveryNotifee implementa la interfaz mdns.Notifee de go-libp2p.
// No se exporta porque es un detalle de implementación: el único punto
// de acceso externo es NewMDNSDiscovery.
//
// La interfaz mdns.Notifee tiene un único método: HandlePeerFound.
// go-libp2p llama a este método automáticamente en una goroutine propia
// cada vez que detecta un nodo nuevo en la red local.
type discoveryNotifee struct {
	host host.Host
}

// HandlePeerFound es invocado por el servicio mDNS cada vez que detecta
// un peer nuevo en la red local. Recibe un peer.AddrInfo que contiene:
//   - info.ID: el PeerID del nodo descubierto (su identidad criptográfica)
//   - info.Addrs: sus direcciones de red (multiaddrs, e.g. /ip4/192.168.1.5/tcp/4001)
//
// Llamamos a host.Connect para establecer la conexión TCP con ese peer.
// host.Connect es idempotente: si ya estamos conectados, no hace nada.
// libp2p gestiona un pool de conexiones internamente y reutiliza las existentes.
func (n *discoveryNotifee) HandlePeerFound(info peer.AddrInfo) {
	fmt.Printf("[discovery] peer encontrado: %s\n", info.ID.ShortString())

	// context.Background() porque la conexión no tiene un deadline específico.
	// En iteraciones futuras podríamos usar un contexto con timeout.
	if err := n.host.Connect(context.Background(), info); err != nil {
		fmt.Printf("[discovery] error conectando a %s: %v\n", info.ID.ShortString(), err)
		return
	}

	fmt.Printf("[discovery] conectado a %s\n", info.ID.ShortString())
}
