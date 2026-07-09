package node

import (
	"bytes"
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
			if n.attacker.ShouldSkipCommit() {
				n.attackerLogf("omitiendo commit2 (manual)")
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

		case protocol.ControlStartReveal1:
			if n.attacker.ShouldSkipReveal1() {
				n.attackerLogf("omitiendo reveal1 (manual)")
				return
			}
			r, allReady, err := n.dcr.StartReveal1()
			if err != nil {
				fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
				return
			}
			toPublish := n.modifyAndLog(r, n.attacker.ModifyReveal1, "reveal1 modificado con hash inválido")
			if err := n.signAndPublishReveal1(ctx, toPublish); err != nil {
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
		n.tryFlushReveal1(ctx, from) // procesar reveal1 que llegó antes que este commit2
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
		// Si el commit2 de este peer aún no llegó, buffereamos y esperamos.
		if _, hasCommit := n.dcr.GetPhaseValue("commit2", from); !hasCommit {
			n.bufferReveal1(from, msg, newData)
			return
		}
		n.processReveal1(ctx, from, msg, newData)
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
		// Si el reveal1 de este peer aún no fue aceptado, buffereamos y esperamos.
		if _, hasReveal1 := n.dcr.GetPhaseValue("reveal1", from); !hasReveal1 {
			n.bufferReveal2(from, msg, newData)
			return
		}
		n.processReveal2(ctx, from, msg, newData)
	}); err != nil {
		return err
	}

	return nil
}

// modifyAndLog aplica la transformación adversarial modify sobre original y, si el valor
// cambió (perfil de atacante activo que falsea el valor), loguea logMsg. Devuelve el valor
// a publicar. Con el perfil honesto modify es identidad y no loguea nada.
func (n *Node) modifyAndLog(original []byte, modify func([]byte) []byte, logMsg string) []byte {
	modified := modify(original)
	if !bytes.Equal(modified, original) {
		n.attackerLogf("%s", logMsg)
	}
	return modified
}

// processReveal1 verifica y aplica un reveal1. Se llama desde el handler de gossipsub
// y desde tryFlushReveal1 cuando el commit2 llega después.
func (n *Node) processReveal1(ctx context.Context, from peer.ID, msg protocol.Reveal1Msg, newData []byte) {
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
	n.tryFlushReveal2(ctx, from) // procesar reveal2 que llegó antes que este reveal1
	if allReady || n.dcr.Reveal1Count() >= n.effectiveSize() {
		n.logRevealOrder()
		n.triggerReveal2IfFirst(ctx)
	}
	n.tryStrategicReveal1Decision(ctx)
}

// tryStrategicReveal1Decision implementa el aborto estratégico del "último revelador"
// en reveal1. El atacante difirió su reveal1 (ShouldSkipReveal1) y, una vez recibidos
// todos los reveal1 ajenos, calcula el orden hipotético que resultaría si publicara el
// suyo. Sólo lo publica si ese orden lo dejaría como último en reveal2 (posición
// ventajosa); en caso contrario aborta dejando que el timer de los honestos lo excluya.
func (n *Node) tryStrategicReveal1Decision(ctx context.Context) {
	if !n.attacker.IsStrategicReveal1() {
		return
	}
	// Ya decidimos publicar nuestro reveal1 en una llamada anterior.
	if _, ok := n.dcr.GetPhaseValue("reveal1", n.host.ID()); ok {
		return
	}
	// Esperamos a tener todos los reveal1 ajenos antes de decidir.
	if n.dcr.Reveal1Count() < n.effectiveSize()-1 {
		return
	}

	order, ok := n.dcr.HypotheticalRevealOrderWithSelf(n.abortedSet())
	if !ok || len(order) == 0 {
		return
	}
	self := n.host.ID()
	pos := -1
	for i, p := range order {
		if p == self {
			pos = i
			break
		}
	}
	isLast := pos == len(order)-1
	n.attackerLogf("orden hipotético de reveal2 si publico r_i: posición %d/%d (último=%v)", pos+1, len(order), isLast)

	if !n.attacker.ShouldReveal1IfLast(isLast) {
		n.attackerLogf("aborto estratégico en reveal1 — no sería último en reveal2")
		return
	}

	r, allReady, err := n.dcr.StartReveal1()
	if err != nil {
		fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
		return
	}
	if err := n.signAndPublishReveal1(ctx, r); err != nil {
		fmt.Printf("[dcr] error publicando reveal1: %v\n", err)
		return
	}
	n.attackerLogf("reveal1 publicado estratégicamente — sería último en reveal2")
	if allReady {
		n.logRevealOrder()
		n.triggerReveal2IfFirst(ctx)
	}
}

// processReveal2 verifica y aplica un reveal2. Se llama desde el handler de gossipsub
// y desde tryFlushReveal2 cuando el reveal1 se acepta después.
func (n *Node) processReveal2(ctx context.Context, from peer.ID, msg protocol.Reveal2Msg, newData []byte) {
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
}

// bufferReveal1 guarda un reveal1 que llegó antes que el commit2 de ese peer.
func (n *Node) bufferReveal1(from peer.ID, msg protocol.Reveal1Msg, rawData []byte) {
	n.pendingMu.Lock()
	defer n.pendingMu.Unlock()
	if _, exists := n.pendingReveal1[from]; !exists {
		n.pendingReveal1[from] = bufferedReveal1{msg: msg, rawData: rawData}
		fmt.Printf("[dcr] reveal1 de %s buffereado (commit2 aún no llegó)\n", from.ShortString())
	}
}

// tryFlushReveal1 procesa el reveal1 buffereado para from, si existe, ahora que su commit2 llegó.
func (n *Node) tryFlushReveal1(ctx context.Context, from peer.ID) {
	n.pendingMu.Lock()
	buf, ok := n.pendingReveal1[from]
	if ok {
		delete(n.pendingReveal1, from)
	}
	n.pendingMu.Unlock()
	if !ok {
		return
	}
	fmt.Printf("[dcr] procesando reveal1 buffereado de %s\n", from.ShortString())
	n.processReveal1(ctx, from, buf.msg, buf.rawData)
}

// bufferReveal2 guarda un reveal2 que llegó antes que el reveal1 de ese peer.
func (n *Node) bufferReveal2(from peer.ID, msg protocol.Reveal2Msg, rawData []byte) {
	n.pendingMu.Lock()
	defer n.pendingMu.Unlock()
	if _, exists := n.pendingReveal2[from]; !exists {
		n.pendingReveal2[from] = bufferedReveal2{msg: msg, rawData: rawData}
		fmt.Printf("[dcr] reveal2 de %s buffereado (reveal1 aún no llegó)\n", from.ShortString())
	}
}

// tryFlushReveal2 procesa el reveal2 buffereado para from, si existe, ahora que su reveal1 fue aceptado.
func (n *Node) tryFlushReveal2(ctx context.Context, from peer.ID) {
	n.pendingMu.Lock()
	buf, ok := n.pendingReveal2[from]
	if ok {
		delete(n.pendingReveal2, from)
	}
	n.pendingMu.Unlock()
	if !ok {
		return
	}
	fmt.Printf("[dcr] procesando reveal2 buffereado de %s\n", from.ShortString())
	n.processReveal2(ctx, from, buf.msg, buf.rawData)
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

	if n.attacker.ShouldSkipReveal1() {
		if n.attacker.IsStrategicReveal1() {
			n.attackerLogf("reveal1 diferido — esperando reveal1s ajenos para decidir estratégicamente")
		} else {
			n.attackerLogf("omitiendo reveal1 — timer de otros nodos abortará este nodo")
		}
		return
	}

	r, allReady, err := n.dcr.StartReveal1()
	if err != nil {
		fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
		return
	}
	toPublish := n.modifyAndLog(r, n.attacker.ModifyReveal1, "reveal1 modificado con hash inválido")
	if err := n.signAndPublishReveal1(ctx, toPublish); err != nil {
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
// Si hay un perfil de atacante activo, puede omitir, modificar o demorar la publicación,
// o iniciar la VDF antes de publicar para obtener ventaja temporal.
func (n *Node) publishReveal2(ctx context.Context) error {
	if n.attacker.ShouldSkipReveal2() {
		n.attackerLogf("omitiendo reveal2 en turno — timer abortará este nodo")
		return nil
	}

	// last-revealer-abort-r2: siendo el último en revelar, el atacante observa todos los
	// s_j y compara los dos inputs posibles de la VDF (revelar s_i vs abortar con r_i como
	// fallback), eligiendo la acción que produzca el input numéricamente menor.
	if n.attacker.IsStrategicReveal2() {
		abortedR2 := n.abortedInReveal2Set()
		ifReveal, ifAbort, ok := n.dcr.ComputeBothVDFInputs(abortedR2)
		if ok {
			n.attackerLogf("input VDF si revela s_i:       %x", ifReveal)
			n.attackerLogf("input VDF si aborta (usa r_i): %x", ifAbort)
			if n.attacker.ShouldAbortReveal2(ifReveal, ifAbort) {
				n.attackerLogf("decisión: ABORTAR en reveal2 — el input resultante es numéricamente menor")
				return nil
			}
			n.attackerLogf("decisión: REVELAR en reveal2 — el input resultante es numéricamente menor o igual")
		}
	}

	// last-revealer-vdf: sólo cuando el nodo es el último revelador conoce el input completo
	// de la VDF antes que los honestos. En esa posición inicia la VDF de inmediato y demora
	// su propio reveal2 para maximizar su ventana exclusiva de precómputo (la ventaja Δ).
	lastRevealerVDF := n.attacker.IsLastRevealerVDF() && n.isLastReveal2()

	s, err := n.dcr.StartReveal2()
	if err != nil {
		return err
	}

	if lastRevealerVDF {
		n.attackerLogf("último revelador — iniciando VDF con input completo antes de publicar reveal2: %d ms", tsMs())
		n.expLastRevealer = true
		n.tryStartVDF(ctx)
	}

	toPublish := n.modifyAndLog(s, n.attacker.ModifyReveal2, "reveal2 modificado con secreto inválido")

	if lastRevealerVDF {
		if d := n.reveal2PrecomputeDelay(); d > 0 {
			// Lanzar en goroutine para no bloquear el callback de gossipsub.
			n.attackerLogf("retrasando reveal2 %v (maximiza ventana exclusiva de precómputo)", d)
			go func() {
				time.Sleep(d)
				if err := n.signAndPublishReveal2(ctx, toPublish); err != nil {
					fmt.Printf("[dcr] error publicando reveal2 (demorado): %v\n", err)
					return
				}
				fmt.Println("[dcr] reveal2 broadcasteado (demorado)")
			}()
			return nil
		}
	}

	if err := n.signAndPublishReveal2(ctx, toPublish); err != nil {
		return err
	}
	fmt.Println("[dcr] reveal2 broadcasteado")
	return nil
}

// isLastReveal2 indica si el nodo local es el último revelador esperado en reveal2:
// el último peer del orden que no abortó en reveal2. Sólo en esa posición el nodo conoce
// el input completo de la VDF antes que los honestos.
func (n *Node) isLastReveal2() bool {
	order := n.dcr.RevealOrder()
	if len(order) == 0 {
		return false
	}
	abortedR2 := n.abortedInReveal2Set()
	self := n.host.ID()
	lastIdx := -1
	for i, p := range order {
		if !abortedR2[p] {
			lastIdx = i
		}
	}
	return lastIdx >= 0 && order[lastIdx] == self
}

// reveal2PrecomputeDelay es la ventana con que el último revelador demora su reveal2 para
// maximizar su precómputo exclusivo de la VDF: 90% del timeout de reveal2 (justo por debajo
// del umbral de aborto), o 900ms si no hay timeout configurado.
func (n *Node) reveal2PrecomputeDelay() time.Duration {
	if n.config.TimeoutReveal2 <= 0 {
		return 900 * time.Millisecond
	}
	return time.Duration(float64(n.config.TimeoutReveal2) * 0.9)
}

// signAndPublishCommit2 firma hash con la clave propia y lo publica como Commit2Msg.
// También almacena el mensaje firmado para poder disputar acusaciones de timeout.
// Si el perfil equivocate-commit está activo, publica un segundo mensaje con hash diferente.
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
	if err := n.pubSub.PublishCommit2(ctx, msg); err != nil {
		return err
	}

	// equivocate-commit: envía un segundo commit2 con hash diferente.
	// Los peers reciben ambos mensajes (gossipsub no deduplica por contenido distinto)
	// y detectan equivocación → aborto del atacante.
	if n.attacker.ShouldEquivocateCommit() {
		fakeHash := randomBytes(32)
		fakeSig, err := signValue(n.privKey, "commit2", n.host.ID(), fakeHash)
		if err != nil {
			return fmt.Errorf("firmar commit2 falso: %w", err)
		}
		fakeMsg := protocol.Commit2Msg{
			AuthorID:  n.host.ID().String(),
			Hash:      fakeHash,
			Signature: fakeSig,
		}
		n.attackerLogf("enviando segundo commit2 con hash diferente → equivocación")
		return n.pubSub.PublishCommit2(ctx, fakeMsg)
	}

	return nil
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
	// Simulación de capacidad de cómputo: si VDFCapacity > 0, estiramos la duración
	// observada de la VDF a T/VDFCapacity segundos, durmiendo el tiempo restante.
	// El output recién se libera (vdfResult) tras este sleep, de modo que el nodo no
	// puede usar el resultado antes del tiempo simulado: así se modela hardware más
	// lento o más rápido y la ventaja temporal del adversario.
	if n.config.VDFCapacity > 0 {
		target := time.Duration(float64(iterations) / n.config.VDFCapacity * float64(time.Second))
		if remaining := target - time.Since(start); remaining > 0 {
			select {
			case <-time.After(remaining):
			case <-ctx.Done():
				return
			}
		} else {
			fmt.Printf("[vdf] advertencia: capacidad simulada no alcanzable (cómputo real %v > objetivo %v)\n",
				time.Since(start), target)
		}
	}
	n.vdfMu.Lock()
	n.vdfResult = output
	n.vdfProof = proof
	n.vdfMu.Unlock()
	dur := time.Since(start)
	valid := protocol.VerifyVDF(input, iterations, output, proof)
	fmt.Printf("[vdf] output obtenido: %d ms (duración: %v)\n", tsMs(), dur)
	fmt.Printf("[vdf] output=%x\n", output)
	fmt.Printf("[vdf] proof=%x\n", proof)
	fmt.Printf("[vdf] verificación inline: %v\n", valid)

	// Veredicto experimental: si este nodo es el último revelador que disparó la VDF
	// temprana, reportar si obtuvo el output DENTRO de la ventana de TO (output
	// anticipado → podría abortar selectivamente). El reveal se publica igual.
	if n.expLastRevealer {
		window := n.config.TimeoutReveal2
		anticipado := dur < window
		fmt.Printf("[exp] last-revealer output_dur_ms=%d window_ms=%d output_anticipado=%t\n",
			dur.Milliseconds(), window.Milliseconds(), anticipado)
	}
}
