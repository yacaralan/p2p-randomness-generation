package protocol

import "github.com/libp2p/go-libp2p/core/protocol"

// ProtocolID identifica el protocolo de generación de aleatoriedad.
// En libp2p, el protocol ID es el mecanismo de negociación: cuando un nodo
// abre un stream, le indica al peer remoto qué protocolo quiere hablar.
// Si el peer no lo soporta, rechaza el stream con un error de protocolo.
const ProtocolID = protocol.ID("/randomness/1.0.0")

// MessageType identifica el tipo semántico de un mensaje.
// En iteraciones futuras se agregarán: Commit, Reveal, VDFProof.
type MessageType string

const (
	MessageTypePing = MessageType("PING")
	MessageTypePong = MessageType("PONG")
	MessageTypeChat = MessageType("CHAT")
)

// Message es la unidad de comunicación del protocolo.
// Actualmente se serializa como JSON delimitado por '\n'.
//
// En la próxima iteración (integración de pubsub + protobuf), este struct
// será reemplazado por tipos generados desde un .proto, que permiten
// campos adicionales tipados (hash del commit, proof VDF, timestamp, etc.)
// sin necesidad de parsear JSON a mano.
type Message struct {
	Type    MessageType `json:"type"`
	Payload string      `json:"payload"`
}
