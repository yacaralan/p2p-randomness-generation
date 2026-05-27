package node

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// ProposeStart propone el inicio del protocolo a todos los peers.
// Solo puede llamarse una vez; si ya hay una sesión activa o propuesta, retorna error.
func (n *Node) ProposeStart(ctx context.Context) error {
	n.sessionMu.Lock()
	if n.sessionProposer != "" {
		n.sessionMu.Unlock()
		return fmt.Errorf("ya hay una sesión en curso (propuesta por %s)", n.sessionProposer.ShortString())
	}
	n.sessionProposer = n.host.ID()
	n.sessionMu.Unlock()

	fmt.Println("[session] proponiendo inicio del protocolo...")
	return n.pubSub.PublishControl(ctx, protocol.ControlProposeStart)
}

// handleProposeStart responde a una propuesta de inicio publicando READY_ACK.
func (n *Node) handleProposeStart(ctx context.Context, from peer.ID) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	if n.sessionProposer != "" && n.sessionProposer != from && n.sessionProposer != n.host.ID() {
		n.sessionMu.Unlock()
		fmt.Printf("[session] PROPOSE_START ignorado (ya hay propuesta de %s)\n", n.sessionProposer.ShortString())
		return
	}
	if n.sessionProposer == "" {
		n.sessionProposer = from
	}
	n.sessionMu.Unlock()

	fmt.Printf("[session] propuesta recibida de %s, enviando READY_ACK\n", from.ShortString())
	if err := n.pubSub.PublishControl(ctx, protocol.ControlReadyAck); err != nil {
		fmt.Printf("[session] error enviando READY_ACK: %v\n", err)
	}
}

// handleReadyAck acumula confirmaciones; cuando todas llegaron publica SESSION_LOCK.
// Solo el proponente ejecuta este conteo.
func (n *Node) handleReadyAck(ctx context.Context, from peer.ID) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	if n.sessionProposer != n.host.ID() {
		n.sessionMu.Unlock()
		return
	}

	n.readyPeers[from] = true

	// Esperamos ACK de todos los peers TCP conectados + el propio (que llega vía loopback).
	tcpPeers := n.host.Network().Peers()
	expected := len(tcpPeers) + 1 // peers + self
	got := len(n.readyPeers)
	n.sessionMu.Unlock()

	fmt.Printf("[session] READY_ACK de %s (%d/%d)\n", from.ShortString(), got, expected)

	if got >= expected {
		n.publishSessionLock(ctx)
	}
}

// publishSessionLock serializa la lista de participantes y publica SESSION_LOCK.
func (n *Node) publishSessionLock(ctx context.Context) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	participants := make([]string, 0, len(n.readyPeers))
	for p := range n.readyPeers {
		participants = append(participants, string(p))
	}
	n.sessionMu.Unlock()

	payload, err := json.Marshal(participants)
	if err != nil {
		fmt.Printf("[session] error serializando participantes: %v\n", err)
		return
	}
	fmt.Printf("[session] bloqueando sesión con %d participantes\n", len(participants))
	if err := n.pubSub.PublishControlWithPayload(ctx, protocol.ControlSessionLock, string(payload)); err != nil {
		fmt.Printf("[session] error publicando SESSION_LOCK: %v\n", err)
	}
}

// handleSessionLock procesa el bloqueo de sesión e inicia el commit2.
func (n *Node) handleSessionLock(ctx context.Context, payload string) {
	var rawIDs []string
	if err := json.Unmarshal([]byte(payload), &rawIDs); err != nil {
		fmt.Printf("[session] SESSION_LOCK con payload inválido: %v\n", err)
		return
	}

	participants := make([]peer.ID, 0, len(rawIDs))
	for _, raw := range rawIDs {
		participants = append(participants, peer.ID(raw))
	}

	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	n.sessionActive = true
	n.sessionSize = len(rawIDs)
	n.sessionParticipants = participants
	n.sessionMu.Unlock()

	fmt.Printf("[session] sesión bloqueada con %d participantes — iniciando commit2\n", len(rawIDs))

	hash, err := n.dcr.StartCommit2()
	if err != nil {
		fmt.Printf("[dcr] error generando commit2: %v\n", err)
		return
	}
	if err := n.signAndPublishCommit2(ctx, hash); err != nil {
		fmt.Printf("[dcr] error publicando commit2: %v\n", err)
		return
	}
	fmt.Println("[dcr] commit2 broadcasteado")
	n.startCommitTimer(ctx)
	n.tryStartReveal1(ctx)
}
