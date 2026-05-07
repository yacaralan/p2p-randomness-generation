package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/ayacar/p2p-randomness-generation/discovery"
	"github.com/ayacar/p2p-randomness-generation/protocol"
)

const (
	ansiRed   = "\033[31m"
	ansiReset = "\033[0m"
)

type Node struct {
	host           host.Host
	discovery      *discovery.MDNSDiscovery
	localDiscovery *discovery.LocalDiscovery
	handler        *protocol.Handler
	pubSub         *protocol.PubSub
	dcr            *protocol.DoubleCommitReveal
	config         Config

	vdfMu       sync.Mutex
	vdfResult   []byte
	vdfProof    []byte
	vdfTriggered bool

	// estado de la sesión del protocolo
	sessionMu        sync.Mutex
	sessionActive    bool
	sessionSize      int
	sessionProposer  peer.ID
	readyPeers       map[peer.ID]bool
	reveal1Triggered bool
}

func New(cfg Config) (*Node, error) {
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("generar clave Ed25519: %w", err)
	}

	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.Port)

	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.NoTransports,
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
	)
	if err != nil {
		return nil, fmt.Errorf("crear host libp2p: %w", err)
	}

	return &Node{
		host:       h,
		config:     cfg,
		readyPeers: make(map[peer.ID]bool),
	}, nil
}

func (n *Node) Start(ctx context.Context) error {
	n.handler = protocol.NewHandler(n.host)

	fmt.Printf("[node] PeerID: %s\n", n.host.ID().String())
	for _, addr := range n.host.Addrs() {
		fmt.Printf("[node] escuchando en: %s/p2p/%s\n", addr, n.host.ID())
	}

	n.host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			if conn.Stat().Direction == network.DirInbound {
				fmt.Printf("[node] descubierto por %s\n", conn.RemotePeer().ShortString())
			}
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			fmt.Printf("[node] desconectado de %s\n", conn.RemotePeer().ShortString())
		},
	})

	ps, err := protocol.NewPubSub(ctx, n.host)
	if err != nil {
		return fmt.Errorf("iniciar pubsub: %w", err)
	}
	n.pubSub = ps

	n.dcr = protocol.NewDoubleCommitReveal(n.host.ID())
	if err := n.wireDoubleCommitReveal(ctx); err != nil {
		return fmt.Errorf("cablear double commit-reveal: %w", err)
	}

	disc, err := discovery.NewMDNSDiscovery(n.host)
	if err != nil {
		return fmt.Errorf("iniciar discovery: %w", err)
	}
	n.discovery = disc

	ld, err := discovery.NewLocalDiscovery(ctx, n.host)
	if err != nil {
		return fmt.Errorf("iniciar local discovery: %w", err)
	}
	n.localDiscovery = ld

	for _, addrStr := range n.config.BootstrapPeers {
		if err := n.ConnectPeer(ctx, addrStr); err != nil {
			fmt.Printf("[node] bootstrap peer %q: %v\n", addrStr, err)
		}
	}

	go n.peerExchangeLoop(ctx)

	return nil
}

func (n *Node) ConnectPeer(ctx context.Context, addrStr string) error {
	info, err := peer.AddrInfoFromString(addrStr)
	if err != nil {
		return fmt.Errorf("multiaddr inválida: %w", err)
	}
	if err := n.host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("conectando a %s: %w", info.ID.ShortString(), err)
	}
	fmt.Printf("[node] conectado a %s\n", info.ID.ShortString())
	return nil
}

func (n *Node) peerExchangeLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, p := range n.host.Peerstore().Peers() {
				if p == n.host.ID() {
					continue
				}
				if n.host.Network().Connectedness(p) == network.Connected {
					continue
				}
				addrs := n.host.Peerstore().Addrs(p)
				if len(addrs) == 0 {
					continue
				}
				if err := n.host.Connect(ctx, peer.AddrInfo{ID: p, Addrs: addrs}); err == nil {
					fmt.Printf("[node] peer exchange: conectado a %s\n", p.ShortString())
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (n *Node) Host() host.Host {
	return n.host
}

func (n *Node) Handler() *protocol.Handler {
	return n.handler
}

func (n *Node) PubSub() *protocol.PubSub {
	return n.pubSub
}

func (n *Node) DoubleCommitReveal() *protocol.DoubleCommitReveal {
	return n.dcr
}

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

// logRevealOrder calcula y loggea el orden de reveal2.
func (n *Node) logRevealOrder() {
	order := n.dcr.ComputeRevealOrder()
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
			if err := n.pubSub.PublishCommit2(ctx, hash); err != nil {
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
			if err := n.pubSub.PublishReveal1(ctx, r); err != nil {
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
		}
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeCommit2(ctx, func(from peer.ID, hash []byte) {
		fmt.Printf("[dcr] commit2 recibido de %s\n", from.ShortString())
		n.dcr.HandleCommit2(from, hash)
		n.tryStartReveal1(ctx)
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeReveal1(ctx, func(from peer.ID, hash []byte) {
		fmt.Printf("[dcr] reveal1 recibido de %s\n", from.ShortString())
		valid, allReady := n.dcr.HandleReveal1(from, hash)
		if !valid {
			fmt.Printf("[dcr] %sERROR:%s reveal1 de %s inválido (hash no coincide con commit2)\n",
				ansiRed, ansiReset, from.ShortString())
			return
		}
		fmt.Printf("[dcr] reveal1 de %s verificado correctamente\n", from.ShortString())
		if allReady {
			n.logRevealOrder()
			n.triggerReveal2IfFirst(ctx)
		}
	}); err != nil {
		return err
	}

	if err := n.pubSub.SubscribeReveal2(ctx, func(from peer.ID, secret []byte) {
		fmt.Printf("[dcr] reveal2 recibido de %s\n", from.ShortString())
		if !n.dcr.HandleReveal2(from, secret) {
			fmt.Printf("[dcr] %sERROR:%s reveal2 de %s inválido (H(secret) no coincide con reveal1)\n",
				ansiRed, ansiReset, from.ShortString())
			return
		}
		fmt.Printf("[dcr] reveal2 de %s verificado correctamente\n", from.ShortString())
		if n.dcr.MyTurnAfter(from) {
			if err := n.publishReveal2(ctx); err != nil {
				fmt.Printf("[dcr] error publicando reveal2: %v\n", err)
			}
		}
		n.tryStartVDF(ctx)
	}); err != nil {
		return err
	}

	return nil
}

// handleProposeStart responde a una propuesta de inicio publicando READY_ACK.
func (n *Node) handleProposeStart(ctx context.Context, from peer.ID) {
	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	// Si ya hay un proponente distinto al que acaba de enviar, ignorar.
	if n.sessionProposer != "" && n.sessionProposer != from && n.sessionProposer != n.host.ID() {
		n.sessionMu.Unlock()
		fmt.Printf("[session] PROPOSE_START ignorado (ya hay propuesta de %s)\n", n.sessionProposer.ShortString())
		return
	}
	// Registrar al proponente si aún no se hizo (nodos que no son el iniciador).
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
	// Solo el proponente acumula ACKs y decide cuándo bloquear.
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
	// Construir lista de participantes: peers que respondieron READY_ACK.
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

	n.sessionMu.Lock()
	if n.sessionActive {
		n.sessionMu.Unlock()
		return
	}
	n.sessionActive = true
	n.sessionSize = len(rawIDs)
	n.sessionMu.Unlock()

	fmt.Printf("[session] sesión bloqueada con %d participantes — iniciando commit2\n", len(rawIDs))

	hash, err := n.dcr.StartCommit2()
	if err != nil {
		fmt.Printf("[dcr] error generando commit2: %v\n", err)
		return
	}
	if err := n.pubSub.PublishCommit2(ctx, hash); err != nil {
		fmt.Printf("[dcr] error publicando commit2: %v\n", err)
		return
	}
	fmt.Println("[dcr] commit2 broadcasteado")
	// Verificar si ya tenemos todos (caso de 1 participante).
	n.tryStartReveal1(ctx)
}

// tryStartReveal1 transiciona a reveal1 cuando se recibieron todos los commit2.
// Es idempotente: el flag reveal1Triggered garantiza que se ejecuta una sola vez.
func (n *Node) tryStartReveal1(ctx context.Context) {
	n.sessionMu.Lock()
	if !n.sessionActive || n.reveal1Triggered {
		n.sessionMu.Unlock()
		return
	}
	// Commit2Count adquiere dcr.mu independientemente; el ordering sessionMu→dcr.mu es seguro.
	count := n.dcr.Commit2Count()
	if count < n.sessionSize {
		n.sessionMu.Unlock()
		return
	}
	n.reveal1Triggered = true
	n.sessionMu.Unlock()

	r, allReady, err := n.dcr.StartReveal1()
	if err != nil {
		fmt.Printf("[dcr] error iniciando reveal1: %v\n", err)
		return
	}
	if err := n.pubSub.PublishReveal1(ctx, r); err != nil {
		fmt.Printf("[dcr] error publicando reveal1: %v\n", err)
		return
	}
	fmt.Println("[dcr] reveal1 broadcasteado (auto)")
	if allReady {
		n.logRevealOrder()
		n.triggerReveal2IfFirst(ctx)
	}
}

// triggerReveal2IfFirst dispara el reveal2 del primer nodo en el orden (si somos nosotros).
func (n *Node) triggerReveal2IfFirst(ctx context.Context) {
	if !n.dcr.IsFirstReveal2() {
		return
	}
	if err := n.publishReveal2(ctx); err != nil {
		fmt.Printf("[dcr] error publicando reveal2 (auto): %v\n", err)
	}
}

// tryStartVDF inicia la VDF cuando todos los reveal2 están disponibles.
// Es idempotente gracias al flag vdfTriggered.
func (n *Node) tryStartVDF(ctx context.Context) {
	n.vdfMu.Lock()
	if n.vdfTriggered {
		n.vdfMu.Unlock()
		return
	}
	_, ok := n.dcr.FinalInput()
	if !ok {
		n.vdfMu.Unlock()
		return
	}
	n.vdfTriggered = true
	n.vdfMu.Unlock()

	fmt.Printf("[vdf] input obtenido: %d ms\n", tsMs())
	n.StartVDF(ctx, n.config.VDFT)
}

// publishReveal2 obtiene s_i y lo publica en el topic reveal2.
func (n *Node) publishReveal2(ctx context.Context) error {
	s, err := n.dcr.StartReveal2()
	if err != nil {
		return err
	}
	if err := n.pubSub.PublishReveal2(ctx, s); err != nil {
		return err
	}
	fmt.Println("[dcr] reveal2 broadcasteado")
	return nil
}

// StartVDF dispara el cómputo de la VDF de Wesolowski en una goroutine.
func (n *Node) StartVDF(ctx context.Context, iterations int) {
	input, ok := n.dcr.FinalInput()
	if !ok {
		fmt.Println("[vdf] input no disponible aún (esperando todos los reveal2)")
		return
	}
	go func() {
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
	}()
}

// Reset limpia todo el estado de sesión y del protocolo para permitir una nueva ronda.
func (n *Node) Reset() {
	n.sessionMu.Lock()
	n.sessionActive = false
	n.sessionSize = 0
	n.sessionProposer = ""
	n.readyPeers = make(map[peer.ID]bool)
	n.reveal1Triggered = false
	n.sessionMu.Unlock()

	n.vdfMu.Lock()
	n.vdfResult = nil
	n.vdfProof = nil
	n.vdfTriggered = false
	n.vdfMu.Unlock()

	n.dcr.Reset()
	fmt.Println("[session] estado reseteado — podés iniciar una nueva ronda con /start")
}

// VDFT expone el parámetro T de la VDF configurado en el nodo.
func (n *Node) VDFT() int {
	return n.config.VDFT
}

func (n *Node) VDFInput() ([]byte, bool) {
	return n.dcr.FinalInput()
}

func (n *Node) VDFResult() []byte {
	n.vdfMu.Lock()
	defer n.vdfMu.Unlock()
	return n.vdfResult
}

func (n *Node) VDFProof() []byte {
	n.vdfMu.Lock()
	defer n.vdfMu.Unlock()
	return n.vdfProof
}

// tsMs devuelve el Unix timestamp actual en milisegundos.
func tsMs() int64 {
	return time.Now().UnixMilli()
}

func (n *Node) Close() error {
	var wg sync.WaitGroup

	if n.pubSub != nil {
		n.pubSub.Close()
	}

	if n.localDiscovery != nil {
		if err := n.localDiscovery.Close(); err != nil {
			fmt.Printf("[node] error cerrando local discovery: %v\n", err)
		}
	}

	if n.discovery != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.discovery.Close(); err != nil {
				fmt.Printf("[node] error cerrando discovery: %v\n", err)
			}
		}()
	}

	wg.Add(1)
	var hostErr error
	go func() {
		defer wg.Done()
		hostErr = n.host.Close()
	}()

	wg.Wait()
	return hostErr
}
