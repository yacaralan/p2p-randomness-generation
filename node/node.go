package node

import (
	"context"
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
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

type Node struct {
	host           host.Host
	privKey        crypto.PrivKey
	discovery      *discovery.MDNSDiscovery
	localDiscovery *discovery.LocalDiscovery
	pubSub         *protocol.PubSub
	dcr            *protocol.DoubleCommitReveal
	config         Config
	attacker       AttackerBehavior

	vdfMu        sync.Mutex
	vdfResult    []byte
	vdfProof     []byte
	vdfTriggered bool

	// estado de la sesión del protocolo
	sessionMu           sync.Mutex
	sessionActive       bool
	sessionSize         int
	sessionProposer     peer.ID
	sessionParticipants []peer.ID
	readyPeers          map[peer.ID]bool
	reveal1Triggered    bool

	// timers de fase
	readyTimer         *time.Timer
	commitTimer        *time.Timer
	reveal1Timer       *time.Timer
	reveal2Timer       *time.Timer
	reveal2TimerTarget peer.ID

	// votación y abortos por timeout
	timeoutMu       sync.Mutex
	timeoutVotes    map[string]map[peer.ID]bool // "phase:target" → voters
	disputeVoters   map[string]map[peer.ID]bool // "phase:target" → disputers
	timeoutDisputes map[string][]byte           // "phase:target" → valor disputado
	abortedPeers    map[peer.ID]string          // peer → fase donde fue abortado ("ready_ack"|"commit2"|"reveal1"|"reveal2")

	// mensajes firmados por fase, para reenvío en disputas de timeout
	signedMu   sync.Mutex
	signedMsgs map[string]map[peer.ID][]byte // "commit2"/"reveal1"/"reveal2" → peer → JSON firmado

	// mensajes pendientes que llegaron antes que su prerequisito (gossipsub no garantiza orden)
	pendingMu      sync.Mutex
	pendingReveal1 map[peer.ID]bufferedReveal1
	pendingReveal2 map[peer.ID]bufferedReveal2
}

type bufferedReveal1 struct {
	msg     protocol.Reveal1Msg
	rawData []byte
}

type bufferedReveal2 struct {
	msg     protocol.Reveal2Msg
	rawData []byte
}

func New(cfg Config) (*Node, error) {
	a, err := NewAttacker(cfg.AttackerProfile)
	if err != nil {
		return nil, err
	}

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
		host:            h,
		privKey:         privKey,
		config:          cfg,
		attacker:        a,
		readyPeers:      make(map[peer.ID]bool),
		timeoutVotes:    make(map[string]map[peer.ID]bool),
		disputeVoters:   make(map[string]map[peer.ID]bool),
		timeoutDisputes: make(map[string][]byte),
		abortedPeers:    make(map[peer.ID]string),
		signedMsgs: map[string]map[peer.ID][]byte{
			"commit2": {},
			"reveal1": {},
			"reveal2": {},
		},
		pendingReveal1: make(map[peer.ID]bufferedReveal1),
		pendingReveal2: make(map[peer.ID]bufferedReveal2),
	}, nil
}

func (n *Node) Start(ctx context.Context) error {
	fmt.Printf("[node] PeerID: %s\n", n.host.ID().String())
	for _, addr := range n.host.Addrs() {
		fmt.Printf("[node] escuchando en: %s/p2p/%s\n", addr, n.host.ID())
	}
	if n.attacker.Name() != "honest" {
		fmt.Printf("%s[ATACANTE] perfil activo: %s%s\n", ansiYellow, n.attacker.Name(), ansiReset)
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

// Reset limpia todo el estado de sesión y del protocolo para permitir una nueva ronda.
func (n *Node) Reset() {
	stopTimer(n.readyTimer)
	stopTimer(n.commitTimer)
	stopTimer(n.reveal1Timer)
	stopTimer(n.reveal2Timer)
	n.readyTimer = nil
	n.commitTimer = nil
	n.reveal1Timer = nil
	n.reveal2Timer = nil
	n.reveal2TimerTarget = ""

	n.sessionMu.Lock()
	n.sessionActive = false
	n.sessionSize = 0
	n.sessionProposer = ""
	n.sessionParticipants = nil
	n.readyPeers = make(map[peer.ID]bool)
	n.reveal1Triggered = false
	n.sessionMu.Unlock()

	n.vdfMu.Lock()
	n.vdfResult = nil
	n.vdfProof = nil
	n.vdfTriggered = false
	n.vdfMu.Unlock()

	n.timeoutMu.Lock()
	n.timeoutVotes = make(map[string]map[peer.ID]bool)
	n.disputeVoters = make(map[string]map[peer.ID]bool)
	n.timeoutDisputes = make(map[string][]byte)
	n.abortedPeers = make(map[peer.ID]string)
	n.timeoutMu.Unlock()

	n.signedMu.Lock()
	n.signedMsgs = map[string]map[peer.ID][]byte{
		"commit2": {},
		"reveal1": {},
		"reveal2": {},
	}
	n.signedMu.Unlock()

	n.pendingMu.Lock()
	n.pendingReveal1 = make(map[peer.ID]bufferedReveal1)
	n.pendingReveal2 = make(map[peer.ID]bufferedReveal2)
	n.pendingMu.Unlock()

	n.dcr.Reset()
	fmt.Println("[session] estado reseteado — podés iniciar una nueva ronda con /start")
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

// --- Accessors públicos ---

func (n *Node) Host() host.Host          { return n.host }
func (n *Node) PubSub() *protocol.PubSub { return n.pubSub }
func (n *Node) VDFT() int                { return n.config.VDFT }

func (n *Node) DoubleCommitReveal() *protocol.DoubleCommitReveal { return n.dcr }

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

func (n *Node) storeSignedMsg(phase string, id peer.ID, data []byte) {
	n.signedMu.Lock()
	defer n.signedMu.Unlock()
	n.signedMsgs[phase][id] = data
}

func (n *Node) getSignedMsg(phase string, id peer.ID) ([]byte, bool) {
	n.signedMu.Lock()
	defer n.signedMu.Unlock()
	v, ok := n.signedMsgs[phase][id]
	return v, ok
}

// tsMs devuelve el Unix timestamp actual en milisegundos.
func tsMs() int64 {
	return time.Now().UnixMilli()
}

// attackerLogf loguea una acción adversarial con el nombre del perfil activo.
func (n *Node) attackerLogf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s[ATACANTE:%s] %s%s\n", ansiYellow, n.attacker.Name(), msg, ansiReset)
}
