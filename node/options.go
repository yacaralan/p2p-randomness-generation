// Package node encapsula el ciclo de vida de un nodo en la red P2P.
// Un "nodo" es la unidad de participación: cada instancia del programa
// es un nodo independiente con su propia identidad y conjunto de conexiones.
package node

// Config contiene toda la configuración de un nodo.
// Separar la configuración del código de inicialización permite:
//   - Cambiar la configuración sin tocar la lógica del nodo
//   - Testear el nodo con distintas configuraciones fácilmente
//   - En el futuro: cargar la config desde archivo TOML/YAML
type Config struct {
	// Port es el puerto TCP en el que el nodo va a escuchar conexiones entrantes.
	// Si es 0, el sistema operativo asigna un puerto disponible automáticamente.
	// Usar Port=0 es útil para tests y para correr múltiples nodos en la misma máquina.
	Port int

	// BootstrapPeers es una lista opcional de multiaddrs de peers conocidos
	// a los que conectarse al iniciar, sin esperar al descubrimiento mDNS.
	//
	// Un multiaddr tiene la forma: /ip4/192.168.1.5/tcp/4001/p2p/<PeerID>
	// Ejemplo de uso desde el CLI: --peer /ip4/192.168.1.5/tcp/4001/p2p/12D3Koo...
	//
	// En una red real, estos serían "nodos de entrada" conocidos de antemano.
	// mDNS hace esto innecesario en redes locales, pero en redes globales
	// (con DHT) siempre se necesitan algunos peers de bootstrap iniciales.
	BootstrapPeers []string
}

// DefaultConfig devuelve una Config con valores razonables para desarrollo:
// puerto aleatorio (Port=0) y sin peers de bootstrap manuales.
// mDNS se encargará de encontrar peers automáticamente en la red local.
func DefaultConfig() Config {
	return Config{
		Port:           0,
		BootstrapPeers: nil,
	}
}
