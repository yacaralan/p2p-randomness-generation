package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// wireDoubleCommitReveal conecta el DoubleCommitReveal con los topics gossipsub
// e implementa la orquestación automática entre fases.
func (n *Node) wireDoubleCommitReveal(ctx context.Context) error {
	if err := n.pubSub.SubscribeControl(ctx, func(from peer.ID, action protocol.ControlAction, payload string) {
		switch action {
		case protocol.ControlReset:
			n.Reset()

		case protocol.ControlProposeStart:
			n.handleProposeStart(ctx, from)

		case protocol.ControlReadyAck:
			n.handleReadyAck(ctx, from)

		case protocol.ControlSessionLock:
			n.handleSessionLock(ctx, payload)

		case protocol.ControlStartCommit2:
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

		case protocol.ControlStartReveal1:
			r, allReady, err := n.dcr.StartReveal1()
			if err != nil {
				fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
				return
			}
			if err := n.signAndPublishReveal1(ctx, r); err != nil {
				fmt.Printf("[dcr] error publicando reveal1: %v\n", err)
				return
			}
			fmt.Println("[dcr] reveal1 broadcasteado")
			if allReady {
				n.logRevealOrder()
				n.triggerReveal2IfFirst(ctx)
			}

		case protocol.ControlStartReveal2:
			if !n.dcr.IsFirstReveal2() {
				return
			}
			if err := n.publishReveal2(ctx); err != nil {
				fmt.Printf("[dcr] error publicando reveal2: %v\n", err)
			}

		case protocol.ControlTimeoutVote:
			var p protocol.TimeoutVotePayload
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				return
			}
			target, err := peer.Decode(p.Target)
			if err != nil {
				return
			}
			n.handleTimeoutVote(ctx, from, p.Phase, target)

		case protocol.ControlTimeoutDispute:
			var p protocol.TimeoutDisputePayload
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				return
			}
			target, err := peer.Decode(p.Target)
			if err != nil {
				return
			}
			n.handleTimeoutDispute(ctx, from, p.Phase, target, p.Value)

		case protocol.ControlEquivocationAbort:
			var p protocol.EquivocationProofPayload
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				return
			}
			target, err := peer.Decode(p.Target)
			if err != nil {
				return
			}
			n.handleEquivocationAbort(ctx, p.Phase, target, p.First, p.Second)
		}
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeCommit2(ctx, func(from peer.ID, msg protocol.Commit2Msg) {
		if n.isAborted(from) {
			return
		}
		if !n.verifySignedMsg("commit2", from, msg.AuthorID, msg.Hash, msg.Signature) {
			return
		}
		newData, _ := json.Marshal(msg)
		accepted, equivocated := n.dcr.HandleCommit2(from, msg.Hash)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("commit2", from)
			n.broadcastEquivocationAbort(ctx, "commit2", from, firstSigned, newData)
			return
		}
		if !accepted {
			return // duplicado exacto, ignorar
		}
		n.storeSignedMsg("commit2", from, newData)
		fmt.Printf("[dcr] commit2 recibido de %s\n", from.ShortString())
		n.tryStartReveal1(ctx)
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeReveal1(ctx, func(from peer.ID, msg protocol.Reveal1Msg) {
		if n.isAborted(from) {
			return
		}
		if !n.verifySignedMsg("reveal1", from, msg.AuthorID, msg.Hash, msg.Signature) {
			return
		}
		newData, _ := json.Marshal(msg)
		valid, allReady, equivocated := n.dcr.HandleReveal1(from, msg.Hash)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("reveal1", from)
			n.broadcastEquivocationAbort(ctx, "reveal1", from, firstSigned, newData)
			return
		}
		if !valid {
			fmt.Printf("[dcr] %sERROR:%s reveal1 de %s inválido (hash no coincide con commit2)\n",
				ansiRed, ansiReset, from.ShortString())
			return
		}
		n.storeSignedMsg("reveal1", from, newData)
		fmt.Printf("[dcr] reveal1 de %s verificado correctamente\n", from.ShortString())
		if allReady || n.dcr.Reveal1Count() >= n.effectiveSize() {
			n.logRevealOrder()
			n.triggerReveal2IfFirst(ctx)
		}
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeReveal2(ctx, func(from peer.ID, msg protocol.Reveal2Msg) {
		if n.isAborted(from) {
			return
		}
		if !n.verifySignedMsg("reveal2", from, msg.AuthorID, msg.Secret, msg.Signature) {
			return
		}
		newData, _ := json.Marshal(msg)
		accepted, equivocated := n.dcr.HandleReveal2(from, msg.Secret)
		if equivocated {
			firstSigned, _ := n.getSignedMsg("reveal2", from)
			n.broadcastEquivocationAbort(ctx, "reveal2", from, firstSigned, newData)
			return
		}
		if !accepted {
			fmt.Printf("[dcr] %sERROR:%s reveal2 de %s inválido (H(secret) no coincide con reveal1)\n",
				ansiRed, ansiReset, from.ShortString())
			return
		}
		n.storeSignedMsg("reveal2", from, newData)
		fmt.Printf("[dcr] reveal2 de %s verificado correctamente\n", from.ShortString())
		if n.dcr.MyTurnAfter(from) {
			if err := n.publishReveal2(ctx); err != nil {
				fmt.Printf("[dcr] error publicando reveal2: %v\n", err)
			}
		}
		next := n.nextPendingReveal2Peer(from)
		stopTimer(n.reveal2Timer)
		n.startReveal2TimerFor(ctx, next)
		n.tryStartVDF(ctx)
	}); err != nil {
		return err
	}

	return nil
}

// tryStartReveal1 transiciona a reveal1 cuando se recibieron todos los commit2.
// Es idempotente: el flag reveal1Triggered garantiza que se ejecuta una sola vez.
func (n *Node) tryStartReveal1(ctx context.Context) {
	effective := n.effectiveSize() // sin sessionMu (safe: sessionSize es write-once tras SESSION_LOCK)

	n.sessionMu.Lock()
	if !n.sessionActive || n.reveal1Triggered {
		n.sessionMu.Unlock()
		return
	}
	count := n.dcr.Commit2Count()
	if count < effective {
		n.sessionMu.Unlock()
		return
	}
	n.reveal1Triggered = true
	n.sessionMu.Unlock()

	stopTimer(n.commitTimer)

	r, allReady, err := n.dcr.StartReveal1()
	if err != nil {
		fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
		return
	}
	if err := n.signAndPublishReveal1(ctx, r); err != nil {
		fmt.Printf("[dcr] error publicando reveal1: %v\n", err)
		return
	}
	fmt.Println("[dcr] reveal1 broadcasteado (auto)")
	n.startReveal1Timer(ctx)
	if allReady {
		n.logRevealOrder()
		n.triggerReveal2IfFirst(ctx)
	}
}

// logRevealOrder calcula y loggea el orden de reveal2 excluyendo peers abortados.
func (n *Node) logRevealOrder() {
	stopTimer(n.reveal1Timer)
	order := n.dcr.ComputeRevealOrderExcluding(n.abortedSet())
	self := n.dcr.SelfID()
	fmt.Println("[dcr] orden de reveal2 calculado (mayor d_i primero):")
	for i, p := range order {
		tag := ""
		if p == self {
			tag = "  [YO]"
		}
		fmt.Printf("[dcr]   %d. %s%s\n", i+1, p.ShortString(), tag)
	}
}

// triggerReveal2IfFirst dispara el reveal2 del primer nodo en el orden (si somos nosotros).
// Si no somos primeros, inicia el timer para el primer nodo esperado.
func (n *Node) triggerReveal2IfFirst(ctx context.Context) {
	if !n.dcr.IsFirstReveal2() {
		first := n.nextPendingReveal2Peer("")
		n.startReveal2TimerFor(ctx, first)
		return
	}
	if err := n.publishReveal2(ctx); err != nil {
		fmt.Printf("[dcr] error publicando reveal2 (auto): %v\n", err)
	}
}

// publishReveal2 obtiene s_i, lo firma y lo publica en el topic reveal2.
func (n *Node) publishReveal2(ctx context.Context) error {
	s, err := n.dcr.StartReveal2()
	if err != nil {
		return err
	}
	if err := n.signAndPublishReveal2(ctx, s); err != nil {
		return err
	}
	fmt.Println("[dcr] reveal2 broadcasteado")
	return nil
}

// signAndPublishCommit2 firma hash con la clave propia y lo publica como Commit2Msg.
// También almacena el mensaje firmado para poder disputar acusaciones de timeout.
func (n *Node) signAndPublishCommit2(ctx context.Context, hash []byte) error {
	sig, err := signValue(n.privKey, "commit2", n.host.ID(), hash)
	if err != nil {
		return fmt.Errorf("firmar commit2: %w", err)
	}
	msg := protocol.Commit2Msg{
		AuthorID:  n.host.ID().String(),
		Hash:      hash,
		Signature: sig,
	}
	data, _ := json.Marshal(msg)
	n.storeSignedMsg("commit2", n.host.ID(), data)
	return n.pubSub.PublishCommit2(ctx, msg)
}

// signAndPublishReveal1 firma hash con la clave propia y lo publica como Reveal1Msg.
func (n *Node) signAndPublishReveal1(ctx context.Context, reveal1Value []byte) error {
	sig, err := signValue(n.privKey, "reveal1", n.host.ID(), reveal1Value)
	if err != nil {
		return fmt.Errorf("firmar reveal1: %w", err)
	}
	msg := protocol.Reveal1Msg{
		AuthorID:  n.host.ID().String(),
		Hash:      reveal1Value,
		Signature: sig,
	}
	data, _ := json.Marshal(msg)
	n.storeSignedMsg("reveal1", n.host.ID(), data)
	return n.pubSub.PublishReveal1(ctx, msg)
}

// signAndPublishReveal2 firma secret con la clave propia y lo publica como Reveal2Msg.
func (n *Node) signAndPublishReveal2(ctx context.Context, secret []byte) error {
	sig, err := signValue(n.privKey, "reveal2", n.host.ID(), secret)
	if err != nil {
		return fmt.Errorf("firmar reveal2: %w", err)
	}
	msg := protocol.Reveal2Msg{
		AuthorID:  n.host.ID().String(),
		Secret:    secret,
		Signature: sig,
	}
	data, _ := json.Marshal(msg)
	n.storeSignedMsg("reveal2", n.host.ID(), data)
	return n.pubSub.PublishReveal2(ctx, msg)
}

// broadcastEquivocationAbort publica una prueba de equivocación en el topic de control.
// first y second son los JSON de los dos mensajes firmados con valores distintos del mismo peer.
func (n *Node) broadcastEquivocationAbort(ctx context.Context, phase string, target peer.ID, first, second []byte) {
	fmt.Printf("[equivocación] %sEQUIVOCACIÓN DETECTADA%s: %s en fase %s — difundiendo prueba\n",
		ansiRed, ansiReset, target.ShortString(), phase)
	payload, _ := json.Marshal(protocol.EquivocationProofPayload{
		Phase:  phase,
		Target: target.String(),
		First:  first,
		Second: second,
	})
	if err := n.pubSub.PublishControlWithPayload(ctx, protocol.ControlEquivocationAbort, string(payload)); err != nil {
		fmt.Printf("[equivocación] error publicando prueba: %v\n", err)
	}
}

// tryComputeRevealOrderAndProceed fuerza el cálculo del orden de reveal2
// cuando suficientes reveal1 están disponibles (considerando abortos).
func (n *Node) tryComputeRevealOrderAndProceed(ctx context.Context) {
	n.sessionMu.Lock()
	if !n.sessionActive || !n.reveal1Triggered {
		n.sessionMu.Unlock()
		return
	}
	n.sessionMu.Unlock()

	if n.dcr.Reveal1Count() < n.effectiveSize() {
		return
	}
	n.logRevealOrder()
	n.triggerReveal2IfFirst(ctx)
}

// nextPendingReveal2Peer devuelve el primer peer en revealOrder después de 'after'
// que no está abortado y no ha enviado su reveal2 aún. Si after=="", comienza desde el inicio.
func (n *Node) nextPendingReveal2Peer(after peer.ID) peer.ID {
	order := n.dcr.RevealOrder()
	aborted := n.abortedSet()
	startFromNext := (after == "")
	for _, p := range order {
		if !startFromNext {
			if p == after {
				startFromNext = true
			}
			continue
		}
		if aborted[p] {
			continue
		}
		if _, ok := n.dcr.GetPhaseValue("reveal2", p); !ok {
			return p
		}
	}
	return ""
}

// tryStartVDF inicia la VDF cuando todos los reveal2 esperados están disponibles.
// Para peers abortados en reveal2 usa su reveal1 como fallback.
// Es idempotente gracias al flag vdfTriggered.
func (n *Node) tryStartVDF(ctx context.Context) {
	n.vdfMu.Lock()
	if n.vdfTriggered {
		n.vdfMu.Unlock()
		return
	}
	order := n.dcr.RevealOrder()
	if len(order) == 0 {
		n.vdfMu.Unlock()
		return
	}
	abortedR2 := n.abortedInReveal2Set()
	expected := len(order) - len(abortedR2)
	got := n.dcr.Reveal2CountExcluding(abortedR2)
	if got < expected {
		n.vdfMu.Unlock()
		return
	}
	n.vdfTriggered = true
	n.vdfMu.Unlock()

	stopTimer(n.reveal2Timer)

	input, ok := n.dcr.FinalInputWithFallback(abortedR2)
	if !ok {
		fmt.Println("[vdf] error obteniendo input")
		return
	}
	fmt.Printf("[vdf] input obtenido: %d ms\n", tsMs())
	go n.runVDF(ctx, input, n.config.VDFT)
}

// StartVDF dispara el cómputo de la VDF de Wesolowski en una goroutine.
// Usa reveal1 como fallback para peers abortados en reveal2.
func (n *Node) StartVDF(ctx context.Context, iterations int) {
	abortedR2 := n.abortedInReveal2Set()
	input, ok := n.dcr.FinalInputWithFallback(abortedR2)
	if !ok {
		input, ok = n.dcr.FinalInput()
		if !ok {
			fmt.Println("[vdf] input no disponible aún (esperando todos los reveal2)")
			return
		}
	}
	go n.runVDF(ctx, input, iterations)
}

// runVDF ejecuta el cómputo VDF y guarda el resultado.
func (n *Node) runVDF(ctx context.Context, input []byte, iterations int) {
	start := time.Now()
	fmt.Printf("[vdf] iniciando cómputo (T=%d). input=%x\n", iterations, input)
	output, proof, err := protocol.ComputeVDF(ctx, input, iterations)
	if err != nil {
		return
	}
	n.vdfMu.Lock()
	n.vdfResult = output
	n.vdfProof = proof
	n.vdfMu.Unlock()
	valid := protocol.VerifyVDF(input, iterations, output, proof)
	fmt.Printf("[vdf] output obtenido: %d ms (duración: %v)\n", tsMs(), time.Since(start))
	fmt.Printf("[vdf] output=%x\n", output)
	fmt.Printf("[vdf] proof=%x\n", proof)
	fmt.Printf("[vdf] verificación inline: %v\n", valid)
}
