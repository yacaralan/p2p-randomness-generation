// Package node encapsula el ciclo de vida de un nodo en la red P2P.
// Un "nodo" es la unidad de participación: cada instancia del programa
// es un nodo independiente con su propia identidad y conjunto de conexiones.
package node

import "time"

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

	// VDFT es el número de iteraciones de squarings para la VDF de Wesolowski.
	// Controla el delay secuencial Δ. Valores orientativos: 1000 (demo rápido),
	// 100000+ (experimentos con delays medibles).
	VDFT int

	// VDFCapacity simula la capacidad de cómputo del nodo en squarings por segundo.
	// La duración observada de la VDF se estira a T/VDFCapacity segundos: el nodo
	// computa el resultado real (rápido) pero no lo libera hasta que pase ese tiempo,
	// modelando hardware más lento o más rápido. 0 = sin simulación (velocidad real).
	VDFCapacity float64

	// TimeoutReadyAck es el tiempo máximo que el proponente espera READY_ACK de todos los peers.
	// Al vencer, bloquea la sesión con los peers que respondieron hasta ese momento.
	// 0 = sin timeout (espera indefinida).
	TimeoutReadyAck time.Duration

	// TimeoutCommit es el tiempo máximo para esperar todos los commit2 tras SESSION_LOCK.
	// 0 = sin timeout (espera indefinida).
	TimeoutCommit time.Duration

	// TimeoutReveal1 es el tiempo máximo para esperar todos los reveal1 tras publicar el propio.
	// 0 = sin timeout.
	TimeoutReveal1 time.Duration

	// TimeoutReveal2 es el tiempo máximo por nodo en la fase reveal2 (se reinicia tras cada reveal).
	// 0 = sin timeout.
	TimeoutReveal2 time.Duration

	// AttackerProfile especifica el comportamiento adversarial del nodo.
	// "honest" (o vacío) = comportamiento normal del protocolo.
	// Perfiles disponibles: no-ready-ack, commit-invalid, no-commit, equivocate-commit,
	// no-reveal1, reveal1-invalid, last-revealer-abort, last-revealer-abort-r2, no-reveal2,
	// reveal2-invalid, last-revealer-vdf, false-timeout-vote.
	AttackerProfile string
}

// DefaultConfig devuelve una Config con valores razonables para desarrollo:
// puerto aleatorio (Port=0), sin peers de bootstrap manuales, timeouts de 1s.
// mDNS se encargará de encontrar peers automáticamente en la red local.
func DefaultConfig() Config {
	return Config{
		Port:            0,
		BootstrapPeers:  nil,
		VDFT:            1000,
		TimeoutReadyAck: 2 * time.Second,
		TimeoutCommit:   time.Second,
		TimeoutReveal1:  time.Second,
		TimeoutReveal2:  time.Second,
	}
}
