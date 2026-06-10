package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// --- Helpers de tamaño efectivo y peers abortados ---

func (n *Node) majority() int {
	return n.sessionSize/2 + 1
}

// peerMajority es la mayoría sobre los peers TCP conectados (incluido self). Se usa en la
// fase ready_ack, donde sessionSize aún no está definido y no puede usarse majority().
func (n *Node) peerMajority() int {
	return (len(n.host.Network().Peers())+1)/2 + 1
}

func (n *Node) effectiveSize() int {
	n.timeoutMu.Lock()
	defer n.timeoutMu.Unlock()
	// "ready_ack" aborts ya están excluidos de sessionSize (no aparecen en el payload del SESSION_LOCK),
	// así que no se restan aquí para evitar doble conteo.
	postSession := 0
	for _, phase := range n.abortedPeers {
		if phase != "ready_ack" {
			postSession++
		}
	}
	return n.sessionSize - postSession
}

func (n *Node) abortedSet() map[peer.ID]bool {
	n.timeoutMu.Lock()
	defer n.timeoutMu.Unlock()
	set := make(map[peer.ID]bool, len(n.abortedPeers))
	for p := range n.abortedPeers {
		set[p] = true
	}
	return set
}

func (n *Node) abortedInReveal2Set() map[peer.ID]bool {
	n.timeoutMu.Lock()
	defer n.timeoutMu.Unlock()
	set := make(map[peer.ID]bool)
	for p, phase := range n.abortedPeers {
		if phase == "reveal2" {
			set[p] = true
		}
	}
	return set
}

func (n *Node) isAborted(p peer.ID) bool {
	n.timeoutMu.Lock()
	defer n.timeoutMu.Unlock()
	_, ok := n.abortedPeers[p]
	return ok
}

// --- Timers de fase ---

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

func (n *Node) startCommitTimer(ctx context.Context) {
	if n.config.TimeoutCommit == 0 {
		return
	}
	n.commitTimer = time.AfterFunc(n.config.TimeoutCommit, func() {
		n.onCommitTimeout(ctx)
	})
}

func (n *Node) startReveal1Timer(ctx context.Context) {
	if n.config.TimeoutReveal1 == 0 {
		return
	}
	n.reveal1Timer = time.AfterFunc(n.config.TimeoutReveal1, func() {
		n.onReveal1Timeout(ctx)
	})
}

func (n *Node) startReveal2TimerFor(ctx context.Context, target peer.ID) {
	if n.config.TimeoutReveal2 == 0 || target == "" {
		return
	}
	n.reveal2TimerTarget = target
	stopTimer(n.reveal2Timer)
	n.reveal2Timer = time.AfterFunc(n.config.TimeoutReveal2, func() {
		n.onReveal2Timeout(ctx, target)
	})
}

// --- Callbacks de timeout ---

func (n *Node) onCommitTimeout(ctx context.Context) {
	n.sessionMu.Lock()
	if !n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	participants := n.sessionParticipants
	n.sessionMu.Unlock()

	commitPeers := n.dcr.CommitPeers()
	aborted := n.abortedSet()
	for _, p := range participants {
		if !commitPeers[p] && !aborted[p] {
			fmt.Printf("[timeout] commit2 de %s no llegó — emitiendo TIMEOUT_VOTE\n", p.ShortString())
			n.broadcastTimeoutVote(ctx, "commit2", p)
		}
	}
	n.voteFalseTimeout(ctx, "commit2", commitPeers, aborted, participants)
}

func (n *Node) onReveal1Timeout(ctx context.Context) {
	n.sessionMu.Lock()
	if !n.sessionActive || !n.reveal1Triggered {
		n.sessionMu.Unlock()
		return
	}
	participants := n.sessionParticipants
	n.sessionMu.Unlock()

	reveal1Peers := n.dcr.Reveal1Peers()
	aborted := n.abortedSet()
	for _, p := range participants {
		if !reveal1Peers[p] && !aborted[p] {
			fmt.Printf("[timeout] reveal1 de %s no llegó — emitiendo TIMEOUT_VOTE\n", p.ShortString())
			n.broadcastTimeoutVote(ctx, "reveal1", p)
		}
	}
	n.voteFalseTimeout(ctx, "reveal1", reveal1Peers, aborted, participants)
}

func (n *Node) onReveal2Timeout(ctx context.Context, target peer.ID) {
	if n.reveal2TimerTarget != target {
		return // disparo obsoleto
	}
	fmt.Printf("[timeout] reveal2 de %s no llegó — emitiendo TIMEOUT_VOTE\n", target.ShortString())
	n.broadcastTimeoutVote(ctx, "reveal2", target)
}

// voteFalseTimeout implementa el perfil false-timeout-vote: emite TIMEOUT_VOTE contra los
// participantes que SÍ respondieron en la fase dada (responded[p]), para forzar disputas.
// No hace nada con el perfil honesto. Los nodos honestos disputan reenviando el mensaje firmado.
func (n *Node) voteFalseTimeout(ctx context.Context, phase string, responded, aborted map[peer.ID]bool, participants []peer.ID) {
	if !n.attacker.ShouldVoteFalseTimeout() {
		return
	}
	for _, p := range participants {
		if responded[p] && !aborted[p] && p != n.host.ID() {
			n.attackerLogf("TIMEOUT_VOTE falso contra %s (sí envió %s)", p.ShortString(), phase)
			n.broadcastTimeoutVote(ctx, phase, p)
		}
	}
}

// --- Broadcast de vote/dispute ---

func (n *Node) broadcastTimeoutVote(ctx context.Context, phase string, target peer.ID) {
	payload, _ := json.Marshal(protocol.TimeoutVotePayload{
		Phase:  phase,
		Target: target.String(),
	})
	if err := n.pubSub.PublishControlWithPayload(ctx, protocol.ControlTimeoutVote, string(payload)); err != nil {
		fmt.Printf("[timeout] error publicando TIMEOUT_VOTE: %v\n", err)
	}
}

func (n *Node) broadcastTimeoutDispute(ctx context.Context, phase string, target peer.ID, value []byte) {
	payload, _ := json.Marshal(protocol.TimeoutDisputePayload{
		Phase:  phase,
		Target: target.String(),
		Value:  value,
	})
	if err := n.pubSub.PublishControlWithPayload(ctx, protocol.ControlTimeoutDispute, string(payload)); err != nil {
		fmt.Printf("[timeout] error publicando TIMEOUT_DISPUTE: %v\n", err)
	}
}

// --- Handlers de vote/dispute ---

func (n *Node) handleTimeoutVote(ctx context.Context, from peer.ID, phase string, target peer.ID) {
	// ready_ack usa un mecanismo propio: disputa si recibimos READY_ACK del target.
	if phase == "ready_ack" {
		n.handleReadyAckTimeoutVote(ctx, from, target)
		return
	}

	if signed, ok := n.getSignedMsg(phase, target); ok {
		n.broadcastTimeoutDispute(ctx, phase, target, signed)
	}

	key := fmt.Sprintf("%s:%s", phase, target)
	n.timeoutMu.Lock()
	if n.timeoutVotes[key] == nil {
		n.timeoutVotes[key] = make(map[peer.ID]bool)
	}
	n.timeoutVotes[key][from] = true
	votes := len(n.timeoutVotes[key])
	n.timeoutMu.Unlock()

	if votes >= n.majority() {
		n.abortPeer(ctx, target, phase)
	}
}

// handleReadyAckTimeoutVote procesa un TIMEOUT_VOTE para la fase ready_ack.
// Si tenemos READY_ACK del target, disputamos. Con mayoría de votos, el peer es excluido de la sesión.
func (n *Node) handleReadyAckTimeoutVote(ctx context.Context, from peer.ID, target peer.ID) {
	// Dispute: si tenemos READY_ACK del target, lo reenviamos como prueba.
	n.sessionMu.Lock()
	_, hasACK := n.readyPeers[target]
	n.sessionMu.Unlock()
	if hasACK {
		// El valor de la disputa es el PeerID del target — la prueba es que
		// la mayoría de nodos honestos aseguran haberlo recibido (broadcast confiable).
		n.broadcastTimeoutDispute(ctx, "ready_ack", target, []byte(target))
	}

	key := fmt.Sprintf("ready_ack:%s", target)
	n.timeoutMu.Lock()
	if n.timeoutVotes[key] == nil {
		n.timeoutVotes[key] = make(map[peer.ID]bool)
	}
	n.timeoutVotes[key][from] = true
	votes := len(n.timeoutVotes[key])
	n.timeoutMu.Unlock()

	if votes >= n.peerMajority() {
		n.excludeReadyAckPeer(ctx, target)
	}
}

// excludeReadyAckPeer marca un peer como excluido de la sesión por no enviar READY_ACK
// con mayoría de acuerdo. Reutiliza abortedPeers con phase="ready_ack".
func (n *Node) excludeReadyAckPeer(ctx context.Context, target peer.ID) {
	n.timeoutMu.Lock()
	if _, already := n.abortedPeers[target]; already {
		n.timeoutMu.Unlock()
		return
	}
	n.abortedPeers[target] = "ready_ack"
	n.timeoutMu.Unlock()

	fmt.Printf("[session] %s%s excluido de la sesión (sin READY_ACK, mayoría)%s\n",
		ansiRed, target.ShortString(), ansiReset)
	n.tryPublishSessionLock(ctx)
}

func (n *Node) handleTimeoutDispute(ctx context.Context, from peer.ID, phase string, target peer.ID, value []byte) {
	key := fmt.Sprintf("%s:%s", phase, target)
	n.timeoutMu.Lock()
	if n.disputeVoters[key] == nil {
		n.disputeVoters[key] = make(map[peer.ID]bool)
	}
	n.disputeVoters[key][from] = true
	disputes := len(n.disputeVoters[key])
	n.timeoutMu.Unlock()

	majority := n.majority()
	// ready_ack usa el total de peers conectados como base, no sessionSize.
	if phase == "ready_ack" {
		majority = n.peerMajority()
	}

	if disputes < majority {
		return
	}

	// Mayoría disputó: limpiar votos y procesar según la fase.
	n.timeoutMu.Lock()
	delete(n.timeoutVotes, key)
	n.timeoutMu.Unlock()

	switch phase {
	case "ready_ack":
		// Mayoría asegura haber recibido READY_ACK del target → incluirlo en la sesión.
		fmt.Printf("[session] READY_ACK de %s aceptado por disputa de mayoría\n", target.ShortString())
		n.sessionMu.Lock()
		n.readyPeers[target] = true
		n.sessionMu.Unlock()
		n.tryPublishSessionLock(ctx)
		return
	case "commit2":
		var msg protocol.Commit2Msg
		if err := json.Unmarshal(value, &msg); err != nil {
			return
		}
		if !n.verifySignedMsg("commit2", target, msg.AuthorID, msg.Hash, msg.Signature) {
			return
		}
		accepted, equivocated := n.dcr.HandleCommit2(target, msg.Hash)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("commit2", target)
			n.broadcastEquivocationAbort(ctx, "commit2", target, firstSigned, value)
			return
		}
		if !accepted {
			return
		}
		n.storeSignedMsg("commit2", target, value)
		fmt.Printf("[timeout] commit2 de %s aceptado por disputa de mayoría\n", target.ShortString())
		n.tryStartReveal1(ctx)
	case "reveal1":
		var msg protocol.Reveal1Msg
		if err := json.Unmarshal(value, &msg); err != nil {
			return
		}
		if !n.verifySignedMsg("reveal1", target, msg.AuthorID, msg.Hash, msg.Signature) {
			return
		}
		valid, allReady, equivocated := n.dcr.HandleReveal1(target, msg.Hash)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("reveal1", target)
			n.broadcastEquivocationAbort(ctx, "reveal1", target, firstSigned, value)
			return
		}
		if !valid {
			return
		}
		n.storeSignedMsg("reveal1", target, value)
		fmt.Printf("[timeout] reveal1 de %s aceptado por disputa de mayoría\n", target.ShortString())
		n.tryFlushReveal2(ctx, target) // procesar reveal2 buffereado si llegó antes que este reveal1
		if allReady || n.dcr.Reveal1Count() >= n.effectiveSize() {
			n.logRevealOrder()
			n.triggerReveal2IfFirst(ctx)
		}
	case "reveal2":
		var msg protocol.Reveal2Msg
		if err := json.Unmarshal(value, &msg); err != nil {
			return
		}
		if !n.verifySignedMsg("reveal2", target, msg.AuthorID, msg.Secret, msg.Signature) {
			return
		}
		accepted, equivocated := n.dcr.HandleReveal2(target, msg.Secret)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("reveal2", target)
			n.broadcastEquivocationAbort(ctx, "reveal2", target, firstSigned, value)
			return
		}
		if !accepted {
			return
		}
		n.storeSignedMsg("reveal2", target, value)
		fmt.Printf("[timeout] reveal2 de %s aceptado por disputa de mayoría\n", target.ShortString())
		if n.dcr.MyTurnAfter(target) {
			if err := n.publishReveal2(ctx); err != nil {
				fmt.Printf("[dcr] error publicando reveal2: %v\n", err)
			}
		}
		next := n.nextPendingReveal2Peer(target)
		stopTimer(n.reveal2Timer)
		n.startReveal2TimerFor(ctx, next)
		n.tryStartVDF(ctx)
	}
}

// abortPeer marca un peer como abortado en una fase y reacciona según la fase.
func (n *Node) abortPeer(ctx context.Context, target peer.ID, phase string) {
	n.timeoutMu.Lock()
	if _, already := n.abortedPeers[target]; already {
		n.timeoutMu.Unlock()
		return
	}
	n.abortedPeers[target] = phase
	n.timeoutMu.Unlock()

	fmt.Printf("[timeout] %s%s abortado en fase %s (mayoría)%s\n", ansiRed, target.ShortString(), phase, ansiReset)

	switch phase {
	case "commit2":
		n.tryStartReveal1(ctx)
	case "reveal1":
		n.tryComputeRevealOrderAndProceed(ctx)
	case "reveal2":
		stopTimer(n.reveal2Timer)
		if n.dcr.MyTurnAfter(target) {
			if err := n.publishReveal2(ctx); err != nil {
				fmt.Printf("[dcr] error publicando reveal2 tras aborto: %v\n", err)
			}
		}
		next := n.nextPendingReveal2Peer(target)
		n.startReveal2TimerFor(ctx, next)
		n.tryStartVDF(ctx)
	}
}

// handleEquivocationAbort verifica una prueba de equivocación y aborta al peer si es válida.
// La prueba es autoevidente: contiene dos mensajes firmados por el mismo peer con valores distintos.
// No requiere votación por mayoría — cualquier nodo puede verificarla independientemente.
func (n *Node) handleEquivocationAbort(ctx context.Context, phase string, target peer.ID, first, second []byte) {
	if n.isAborted(target) {
		return
	}
	firstAuthor, firstValue, firstSig, ok := extractPhaseValue(phase, first)
	if !ok || !n.verifySignedMsg(phase, target, firstAuthor, firstValue, firstSig) {
		return
	}
	secondAuthor, secondValue, secondSig, ok := extractPhaseValue(phase, second)
	if !ok || !n.verifySignedMsg(phase, target, secondAuthor, secondValue, secondSig) {
		return
	}
	if string(firstValue) == string(secondValue) {
		return
	}
	fmt.Printf("[equivocación] %sPRUEBA VERIFICADA%s: %s equivocó en fase %s — abortando\n",
		ansiRed, ansiReset, target.ShortString(), phase)
	n.abortPeer(ctx, target, phase)
}
