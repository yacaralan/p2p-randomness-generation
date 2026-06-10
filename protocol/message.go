package protocol

// ControlAction identifica una acción a disparar globalmente en todos los nodos.
type ControlAction string

const (
	ControlStartCommit2      = ControlAction("START_COMMIT2")
	ControlStartReveal1      = ControlAction("START_REVEAL1")
	ControlStartReveal2      = ControlAction("START_REVEAL2")
	ControlProposeStart      = ControlAction("PROPOSE_START")
	ControlReadyAck          = ControlAction("READY_ACK")
	ControlSessionLock       = ControlAction("SESSION_LOCK")
	ControlReset             = ControlAction("RESET")
	ControlTimeoutVote       = ControlAction("TIMEOUT_VOTE")
	ControlTimeoutDispute    = ControlAction("TIMEOUT_DISPUTE")
	ControlEquivocationAbort = ControlAction("EQUIVOCATION_ABORT")
)

// TimeoutVotePayload es el payload de un voto de timeout para un peer en una fase.
type TimeoutVotePayload struct {
	Phase  string `json:"phase"`
	Target string `json:"target"`
}

// TimeoutDisputePayload dispute un voto de timeout reenviando el valor correcto.
type TimeoutDisputePayload struct {
	Phase  string `json:"phase"`
	Target string `json:"target"`
	Value  []byte `json:"value"`
}

// EquivocationProofPayload lleva las dos firmas del nodo equivocador como prueba.
// Cualquier nodo puede verificarlas independientemente sin necesidad de votación.
type EquivocationProofPayload struct {
	Phase  string `json:"phase"`
	Target string `json:"target"`
	First  []byte `json:"first"`  // JSON del primer mensaje firmado (ya aceptado)
	Second []byte `json:"second"` // JSON del segundo mensaje firmado (con valor distinto)
}

// ControlMsg dispara una acción global en todos los nodos.
// Se publica en el topic randomness/control.
// Payload es opcional; SESSION_LOCK lo usa para transportar la lista de participantes (JSON []string).
type ControlMsg struct {
	Action  ControlAction `json:"action"`
	Payload string        `json:"payload,omitempty"`
}

// Topics para el protocolo double commit-reveal.
const (
	TopicCommit2 = "randomness/commit2"
	TopicReveal1 = "randomness/reveal1"
	TopicReveal2 = "randomness/reveal2"
)

// Commit2Msg lleva c_i = H(H(s_i)), el segundo commit del protocolo.
// AuthorID y Signature permiten verificar la autoría cuando el mensaje
// es reenviado por un tercero durante una disputa de timeout.
type Commit2Msg struct {
	AuthorID  string `json:"author_id"`
	Hash      []byte `json:"hash"`
	Signature []byte `json:"signature"`
}

// Reveal1Msg lleva r_i = H(s_i), el primer reveal del protocolo.
type Reveal1Msg struct {
	AuthorID  string `json:"author_id"`
	Hash      []byte `json:"hash"`
	Signature []byte `json:"signature"`
}

// Reveal2Msg lleva s_i, el secreto original del protocolo.
type Reveal2Msg struct {
	AuthorID  string `json:"author_id"`
	Secret    []byte `json:"secret"`
	Signature []byte `json:"signature"`
}
