package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Handler gestiona toda la lógica de streams del protocolo /randomness/1.0.0.
//
// En libp2p, la comunicación entre peers ocurre a través de "streams":
// canales bidireccionales multiplexados sobre una única conexión TCP.
// Cada stream tiene un Protocol.ID que indica qué protocolo se está usando,
// similar a cómo HTTP usa puertos (80/443) pero más flexible.
//
// Handler tiene dos responsabilidades:
//  1. Inbound: responder a streams abiertos por otros peers (handleStream)
//  2. Outbound: abrir streams hacia otros peers (Ping)
//
// En iteraciones futuras, este struct tendrá más métodos:
// SendCommit, SendReveal, handleCommit, handleReveal, etc.
type Handler struct {
	host host.Host
}

// NewHandler crea un Handler y lo registra inmediatamente en el host.
//
// SetStreamHandler le dice al host: "cada vez que un peer remoto abra
// un stream con el protocol ID /randomness/1.0.0, llamá a handleStream".
// Este registro es global al host: basta llamarlo una vez al inicio.
func NewHandler(h host.Host) *Handler {
	ph := &Handler{host: h}
	h.SetStreamHandler(ProtocolID, ph.handleStream)
	return ph
}

// handleStream es llamado automáticamente por libp2p cada vez que un peer
// remoto abre un nuevo stream hacia nosotros usando ProtocolID.
//
// El parámetro s (network.Stream) es el canal de comunicación bidireccional.
// Implementa io.ReadWriteCloser: podemos leer y escribir bytes en él.
//
// Importante: cada stream es independiente. Si el peer nos abre 3 streams,
// handleStream se llama 3 veces en goroutines separadas. libp2p maneja
// la concurrencia internamente a nivel de conexión.
//
// La responsabilidad de esta función es: leer un mensaje, procesarlo,
// responder, y cerrar el stream. En iteraciones futuras, el "procesamiento"
// incluirá validar commits criptográficos, agregar reveals, etc.
func (ph *Handler) handleStream(s network.Stream) {
	// Siempre cerrar el stream al terminar. Si hubo un error no recuperable,
	// s.Reset() le indica al peer remoto que algo salió mal (en vez de
	// que simplemente espere datos que nunca van a llegar).
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()

	// bufio.Scanner lee línea por línea del stream.
	// Usamos este enfoque porque nuestro protocolo delimita mensajes con '\n'.
	// Alternativa: io.ReadAll lería hasta que se cierre el stream, pero eso
	// no permite diálogo (request-response) dentro del mismo stream.
	scanner := bufio.NewScanner(s)

	if !scanner.Scan() {
		// El peer cerró el stream sin enviar nada, o hubo un error de red.
		if err := scanner.Err(); err != nil {
			fmt.Printf("[handler] error leyendo de %s: %v\n", remotePeer.ShortString(), err)
			s.Reset()
		}
		return
	}

	var msg Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		fmt.Printf("[handler] mensaje inválido de %s: %v\n", remotePeer.ShortString(), err)
		// Reset le señala al peer que rechazamos el stream por protocolo inválido.
		s.Reset()
		return
	}

	fmt.Printf("[handler] recibido %s de %s: %q\n", msg.Type, remotePeer.ShortString(), msg.Payload)

	// En esta iteración respondemos siempre con Pong.
	// En iteraciones futuras, el switch aquí decidirá cómo manejar
	// Commit vs Reveal vs VDFProof, etc.
	switch msg.Type {
	case MessageTypePing:
		ph.sendMessage(s, Message{Type: MessageTypePong, Payload: "hola de vuelta"})
	default:
		fmt.Printf("[handler] tipo desconocido %q de %s\n", msg.Type, remotePeer.ShortString())
	}
}

// sendMessage serializa un Message y lo escribe en el stream con un '\n' al final.
// El '\n' es el delimitador que el Scanner del otro lado usa para saber
// cuándo terminó el mensaje.
func (ph *Handler) sendMessage(s network.Stream, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		fmt.Printf("[handler] error serializando mensaje: %v\n", err)
		return
	}
	// Escribimos data + '\n' como una sola llamada para minimizar syscalls.
	if _, err := fmt.Fprintf(s, "%s\n", data); err != nil {
		fmt.Printf("[handler] error escribiendo en stream: %v\n", err)
	}
}

// Ping abre un nuevo stream hacia peerID y envía un mensaje Ping.
// Luego espera y lee la respuesta Pong del peer remoto.
//
// Esta es la función "cliente" del protocolo: nosotros iniciamos el contacto.
// En contraste, handleStream es la función "servidor": el peer remoto nos contacta.
//
// En libp2p, cualquier nodo puede ser cliente y servidor simultáneamente,
// lo que es fundamental para un protocolo P2P simétrico.
//
// host.NewStream negocia el protocolo con el peer remoto: si el peer
// no tiene un handler para ProtocolID, NewStream devuelve un error.
func (ph *Handler) Ping(ctx context.Context, peerID peer.ID) error {
	// Abrimos un stream nuevo hacia el peer. libp2p reutiliza la conexión
	// TCP existente si ya hay una (no abre un nuevo socket).
	s, err := ph.host.NewStream(ctx, peerID, ProtocolID)
	if err != nil {
		return fmt.Errorf("abrir stream a %s: %w", peerID.ShortString(), err)
	}
	defer s.Close()

	// Enviamos el Ping.
	ph.sendMessage(s, Message{Type: MessageTypePing, Payload: "hola"})
	fmt.Printf("[ping] enviado PING a %s\n", peerID.ShortString())

	// Leemos la respuesta Pong.
	scanner := bufio.NewScanner(s)
	if scanner.Scan() {
		var reply Message
		if err := json.Unmarshal(scanner.Bytes(), &reply); err == nil {
			fmt.Printf("[ping] recibido %s de %s: %q\n", reply.Type, peerID.ShortString(), reply.Payload)
		}
	}
	return nil
}
