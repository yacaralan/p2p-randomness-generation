package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	if err := n.pubSub.PublishControl(ctx, protocol.ControlProposeStart); err != nil {
		return err
	}
	n.startReadyAckTimer(ctx)
	return nil
}

// handleProposeStart responde a una propuesta de inicio publicando READY_ACK.
// Todos los nodos (no solo el proponente) inician el timer de READY_ACK para poder
// votar timeout contra peers que no respondan.
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

	// Todos los nodos arrancan el timer para poder votar si el timeout vence.
	n.startReadyAckTimer(ctx)

	if !n.attacker.ShouldSendReadyAck() {
		n.attackerLogf("omitiendo READY_ACK")
		return
	}
	fmt.Printf("[session] propuesta recibida de %s, enviando READY_ACK\n", from.ShortString())
	if err := n.pubSub.PublishControl(ctx, protocol.ControlReadyAck); err != nil {
		fmt.Printf("[session] error enviando READY_ACK: %v\n", err)
	}
}

// handleReadyAck acumula confirmaciones de todos los nodos (no solo el proponente).
// Cuando todos los peers están contabilizados (respondieron o fueron abortados por timeout),
// el proponente publica SESSION_LOCK.
func (n *Node) handleReadyAck(ctx context.Context, from peer.ID) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	n.readyPeers[from] = true
	got := len(n.readyPeers)
	n.sessionMu.Unlock()

	tcpPeers := n.host.Network().Peers()
	fmt.Printf("[session] READY_ACK de %s (%d/%d)\n", from.ShortString(), got, len(tcpPeers)+1)

	n.tryPublishSessionLock(ctx)
}

// startReadyAckTimer inicia el timer de espera de READY_ACK. Es idempotente.
// Al vencer, cada nodo vota timeout contra los peers que no respondieron.
func (n *Node) startReadyAckTimer(ctx context.Context) {
	if n.config.TimeoutReadyAck == 0 {
		return
	}
	// Idempotente: si ya fue iniciado (ej. proposer recibe su propio PROPOSE_START), no reiniciar.
	if n.readyTimer != nil {
		return
	}
	n.readyTimer = time.AfterFunc(n.config.TimeoutReadyAck, func() {
		n.onReadyAckTimeout(ctx)
	})
}

// onReadyAckTimeout emite TIMEOUT_VOTE para cada peer TCP que no envió READY_ACK.
// Corre en todos los nodos cuando el timer de la fase SESSION_LOCK vence.
func (n *Node) onReadyAckTimeout(ctx context.Context) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	readyPeers := make(map[peer.ID]bool, len(n.readyPeers))
	for p := range n.readyPeers {
		readyPeers[p] = true
	}
	n.sessionMu.Unlock()

	tcpPeers := n.host.Network().Peers()
	voted := false
	for _, p := range tcpPeers {
		if !readyPeers[p] {
			fmt.Printf("[session] READY_ACK de %s no llegó — emitiendo TIMEOUT_VOTE\n", p.ShortString())
			n.broadcastTimeoutVote(ctx, "ready_ack", p)
			voted = true
		}
	}
	if !voted {
		// Todos respondieron antes del timeout; el proponente puede bloquear ahora.
		n.tryPublishSessionLock(ctx)
	}
}

// tryPublishSessionLock publica SESSION_LOCK cuando todos los peers TCP están
// contabilizados: respondieron con READY_ACK o fueron excluidos por mayoría de votos.
// Solo el proponente publica; el resto llama a esta función sin efecto.
func (n *Node) tryPublishSessionLock(ctx context.Context) {
	if n.sessionProposer != n.host.ID() {
		return
	}
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	ready := len(n.readyPeers)
	n.sessionMu.Unlock()

	n.timeoutMu.Lock()
	readyAckAbortedCount := 0
	for _, phase := range n.abortedPeers {
		if phase == "ready_ack" {
			readyAckAbortedCount++
		}
	}
	n.timeoutMu.Unlock()

	tcpPeers := n.host.Network().Peers()
	if ready+readyAckAbortedCount >= len(tcpPeers)+1 {
		n.publishSessionLock(ctx)
	}
}

// publishSessionLock serializa la lista de participantes (excluyendo los abortados) y publica SESSION_LOCK.
func (n *Node) publishSessionLock(ctx context.Context) {
	stopTimer(n.readyTimer)

	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}

	n.timeoutMu.Lock()
	abortedPeers := n.abortedPeers
	n.timeoutMu.Unlock()

	participants := make([]string, 0, len(n.readyPeers))
	for p := range n.readyPeers {
		if _, isAborted := abortedPeers[p]; !isAborted {
			participants = append(participants, p.String())
		}
	}
	n.sessionMu.Unlock()

	if len(participants) == 0 {
		fmt.Println("[session] ningún participante disponible para SESSION_LOCK, cancelando")
		n.sessionMu.Lock()
		n.sessionProposer = ""
		n.sessionMu.Unlock()
		return
	}

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
		pid, err := peer.Decode(raw)
		if err != nil {
			fmt.Printf("[session] SESSION_LOCK: peer ID inválido %q: %v\n", raw, err)
			return
		}
		participants = append(participants, pid)
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

	stopTimer(n.readyTimer)

	// Marcar como readyAckAborted cualquier peer TCP que no esté en la lista de participantes.
	// Garantiza que todos los nodos filtren mensajes del peer excluido en fases posteriores,
	// incluso si el mecanismo de TIMEOUT_VOTE no llegó a todos antes del SESSION_LOCK.
	participantSet := make(map[peer.ID]bool, len(participants))
	for _, p := range participants {
		participantSet[p] = true
	}
	n.timeoutMu.Lock()
	for _, p := range n.host.Network().Peers() {
		if !participantSet[p] {
			if _, already := n.abortedPeers[p]; !already {
				n.abortedPeers[p] = "ready_ack"
			}
		}
	}
	n.timeoutMu.Unlock()
	fmt.Printf("[session] sesión bloqueada con %d participantes — iniciando commit2\n", len(rawIDs))

	if n.attacker.ShouldSkipCommit() {
		n.attackerLogf("omitiendo commit2 — timer de otros nodos abortará este nodo")
		return
	}

	hash, err := n.dcr.StartCommit2()
	if err != nil {
		fmt.Printf("[dcr] error generando commit2: %v\n", err)
		return
	}
	toPublish := n.modifyAndLog(hash, n.attacker.ModifyCommit, "commit2 modificado con hash inválido")
	if err := n.signAndPublishCommit2(ctx, toPublish); err != nil {
		fmt.Printf("[dcr] error publicando commit2: %v\n", err)
		return
	}
	fmt.Println("[dcr] commit2 broadcasteado")
	n.startCommitTimer(ctx)
	n.tryStartReveal1(ctx)
}
