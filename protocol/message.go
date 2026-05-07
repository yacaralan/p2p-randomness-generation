package protocol

import "github.com/libp2p/go-libp2p/core/protocol"

// ProtocolID identifica el protocolo de generación de aleatoriedad.
// En libp2p, el protocol ID es el mecanismo de negociación: cuando un nodo
// abre un stream, le indica al peer remoto qué protocolo quiere hablar.
// Si el peer no lo soporta, rechaza el stream con un error de protocolo.
const ProtocolID = protocol.ID("/randomness/1.0.0")

// MessageType identifica el tipo semántico de un mensaje.
type MessageType string

const (
	MessageTypePing = MessageType("PING")
	MessageTypePong = MessageType("PONG")
	MessageTypeChat = MessageType("CHAT")
)

// ControlAction identifica una acción a disparar globalmente en todos los nodos.
type ControlAction string

const (
	ControlStartCommit  = ControlAction("START_COMMIT")
	ControlStartReveal  = ControlAction("START_REVEAL")
	ControlStartCommit2 = ControlAction("START_COMMIT2")
	ControlStartReveal1 = ControlAction("START_REVEAL1")
	ControlStartReveal2 = ControlAction("START_REVEAL2")
)

// Message es el formato genérico para los streams directos (Ping/Pong/Chat).
// Se serializa como JSON delimitado por '\n'.
type Message struct {
	Type    MessageType `json:"type"`
	Payload string      `json:"payload"`
}

// CommitMsg lleva el hash del commit publicado en el topic randomness/commit.
// La autoría (qué peer lo envió) viene del propio gossipsub, no del payload.
type CommitMsg struct {
	Hash []byte `json:"hash"`
}

// RevealMsg lleva el value y nonce que abren el commit, publicados en
// el topic randomness/reveal.
type RevealMsg struct {
	Value []byte `json:"value"`
	Nonce []byte `json:"nonce"`
}

// ControlMsg dispara una acción global (commit o reveal) en todos los nodos.
// Se publica en el topic randomness/control.
type ControlMsg struct {
	Action ControlAction `json:"action"`
}

// Topics para el protocolo double commit-reveal.
const (
	TopicCommit2 = "randomness/commit2"
	TopicReveal1 = "randomness/reveal1"
	TopicReveal2 = "randomness/reveal2"
)

// Commit2Msg lleva c_i = H(H(s_i)), el segundo commit del protocolo.
type Commit2Msg struct {
	Hash []byte `json:"hash"`
}

// Reveal1Msg lleva r_i = H(s_i), el primer reveal del protocolo.
type Reveal1Msg struct {
	Hash []byte `json:"hash"`
}

// Reveal2Msg lleva s_i, el secreto original del protocolo.
type Reveal2Msg struct {
	Secret []byte `json:"secret"`
}
