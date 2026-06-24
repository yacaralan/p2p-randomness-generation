package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ayacar/p2p-randomness-generation/node"
	"github.com/ayacar/p2p-randomness-generation/protocol"
)

func main() {
	// Definimos los flags de línea de comandos usando el paquete estándar "flag".
	portFlag := flag.Int("port", 0, "Puerto TCP a escuchar (0 = asignado automáticamente)")
	peersFlag := flag.String("peer", "", "Multiaddrs de bootstrap separados por comas")
	vdfTFlag := flag.Int("vdf-t", 1000, "Número de iteraciones (T) para la VDF de Wesolowski")
	vdfCapacityFlag := flag.Float64("vdf-capacity", 0, "Capacidad simulada en squarings/seg para la VDF (0 = sin límite, velocidad real)")
	timeoutFlag := flag.Duration("timeout", 0, "Timeout para todas las fases (ej: 500ms, 2s); si >0 sobreescribe los flags individuales")
	timeoutReadyFlag := flag.Duration("timeout-ready", 2*time.Second, "Timeout esperando READY_ACK de todos los peers")
	timeoutCommitFlag := flag.Duration("timeout-commit", time.Second, "Timeout fase commit2")
	timeoutReveal1Flag := flag.Duration("timeout-reveal1", time.Second, "Timeout fase reveal1")
	timeoutReveal2Flag := flag.Duration("timeout-reveal2", time.Second, "Timeout por nodo en reveal2")
	attackerFlag := flag.String("attacker", "honest", "Perfil de atacante: honest|no-ready-ack|commit-invalid|no-commit|equivocate-commit|no-reveal1|reveal1-invalid|last-revealer-abort|last-revealer-abort-r2|no-reveal2|reveal2-invalid|last-revealer-vdf|false-timeout-vote")
	autoStartFlag := flag.Bool("auto-start", false, "Proponer el inicio del protocolo automáticamente tras --auto-start-delay (solo el proponente)")
	autoStartDelayFlag := flag.Duration("auto-start-delay", 5*time.Second, "Ventana de descubrimiento antes de proponer el inicio (solo con --auto-start)")
	exitOnVDFFlag := flag.Bool("exit-on-vdf", false, "Apagar el nodo y salir cuando se complete el cómputo de la VDF")
	flag.Parse()

	cfg := node.DefaultConfig()
	cfg.Port = *portFlag
	cfg.VDFT = *vdfTFlag
	cfg.VDFCapacity = *vdfCapacityFlag
	if *peersFlag != "" {
		cfg.BootstrapPeers = strings.Split(*peersFlag, ",")
	}
	if *timeoutFlag > 0 {
		cfg.TimeoutReadyAck = *timeoutFlag
		cfg.TimeoutCommit = *timeoutFlag
		cfg.TimeoutReveal1 = *timeoutFlag
		cfg.TimeoutReveal2 = *timeoutFlag
	} else {
		cfg.TimeoutReadyAck = *timeoutReadyFlag
		cfg.TimeoutCommit = *timeoutCommitFlag
		cfg.TimeoutReveal1 = *timeoutReveal1Flag
		cfg.TimeoutReveal2 = *timeoutReveal2Flag
	}
	cfg.AttackerProfile = *attackerFlag

	// Creamos el nodo (genera identidad, crea el host libp2p).
	n, err := node.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creando nodo: %v\n", err)
		os.Exit(1)
	}

	// Creamos un contexto cancelable. Al llamar cancel(), todas las operaciones
	// que usen este contexto (Connect, publicaciones, etc.) se interrumpen inmediatamente.
	// Esto es necesario para que n.Close() no quede bloqueado esperando que
	// terminen operaciones de red que ya no tienen sentido completar.
	ctx, cancel := context.WithCancel(context.Background())

	// Iniciamos los subsistemas: protocolo, descubrimiento, bootstrap.
	if err := n.Start(ctx); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "error iniciando nodo: %v\n", err)
		os.Exit(1)
	}

	// commandLoop lee comandos del protocolo desde stdin (modo manual/interactivo).
	go commandLoop(ctx, n)

	// Si --auto-start, proponemos el inicio del protocolo tras la ventana de
	// descubrimiento. Solo el nodo proponente recibe este flag desde el launcher.
	if *autoStartFlag {
		go func() {
			select {
			case <-time.After(*autoStartDelayFlag):
			case <-ctx.Done():
				return
			}
			if err := n.ProposeStart(ctx); err != nil {
				fmt.Printf("[auto-start] error: %v\n", err)
			}
		}()
	}

	// vdfDone se cierra cuando el nodo debe apagarse por completar la VDF (--exit-on-vdf).
	vdfDone := make(chan struct{})
	if *exitOnVDFFlag {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if n.VDFResult() != nil {
						close(vdfDone)
						return
					}
				}
			}
		}()
	}

	fmt.Println("[main] nodo corriendo. Comandos: /start, /peers, /values2, /order, /reset. Ctrl+C para salir.")

	// Bloqueamos la goroutine principal hasta recibir SIGINT (Ctrl+C) o SIGTERM,
	// o hasta que la VDF se complete si --exit-on-vdf está activo.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-vdfDone:
		fmt.Println("[main] VDF completada, apagando nodo...")
	}

	fmt.Println("\n[main] apagando nodo...")

	// Primero cancelamos el contexto: interrumpe Connect y publicaciones en curso,
	// y le señala a las goroutines (commandLoop, auto-start) que deben terminar.
	cancel()

	// Cerramos el nodo en una goroutine con un timeout de 5 segundos.
	// Si host.Close() tarda más (e.g. por goroutines internas de libp2p que
	// no terminan a tiempo), forzamos la salida igualmente.
	// Los streams ya tienen deadlines propios, así que en condiciones normales
	// Close() debería terminar mucho antes del timeout.
	done := make(chan struct{})
	go func() {
		n.Close()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("[main] nodo cerrado correctamente.")
	case <-time.After(5 * time.Second):
		fmt.Println("[main] timeout de apagado, forzando salida.")
	}
}

// commandLoop lee comandos del protocolo desde stdin para operar el nodo a mano.
func commandLoop(ctx context.Context, n *node.Node) {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !scanner.Scan() {
			return
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/connect ") {
			addr := strings.TrimSpace(strings.TrimPrefix(text, "/connect "))
			if err := n.ConnectPeer(ctx, addr); err != nil {
				fmt.Printf("[connect] error: %v\n", err)
			}
			continue
		}
		if text == "/peers" {
			peers := n.Host().Network().Peers()
			if len(peers) == 0 {
				fmt.Println("[peers] sin peers conectados")
			} else {
				for _, p := range peers {
					fmt.Printf("[peers] %s\n", p.ShortString())
				}
			}
			continue
		}
		if text == "/start" {
			if err := n.ProposeStart(ctx); err != nil {
				fmt.Printf("[start] error: %v\n", err)
			}
			continue
		}
		if text == "/reset" {
			if err := n.PubSub().PublishControl(ctx, protocol.ControlReset); err != nil {
				fmt.Printf("[reset] error publicando reset: %v\n", err)
			}
			continue
		}
		if text == "/commit" {
			if err := n.PubSub().PublishControl(ctx, protocol.ControlStartCommit2); err != nil {
				fmt.Printf("[commit] error publicando trigger: %v\n", err)
			} else {
				fmt.Println("[commit] trigger broadcasteado")
			}
			continue
		}
		if text == "/reveal1" {
			if err := n.PubSub().PublishControl(ctx, protocol.ControlStartReveal1); err != nil {
				fmt.Printf("[reveal1] error publicando trigger: %v\n", err)
			} else {
				fmt.Println("[reveal1] trigger broadcasteado")
			}
			continue
		}
		if text == "/reveal2" {
			if order := n.DoubleCommitReveal().RevealOrder(); len(order) == 0 {
				fmt.Println("[reveal2] reveal1 has not finished, cannot start reveal2")
			} else {
				if err := n.PubSub().PublishControl(ctx, protocol.ControlStartReveal2); err != nil {
					fmt.Printf("[reveal2] error publicando trigger: %v\n", err)
				} else {
					fmt.Println("[reveal2] trigger broadcasteado")
				}
			}
			continue
		}
		if text == "/values2" {
			all := n.DoubleCommitReveal().AllValues()
			self := n.DoubleCommitReveal().SelfID()
			if len(all) == 0 {
				fmt.Println("[values2] sin datos (ejecutá /commit2 primero)")
				continue
			}
			for p, pv := range all {
				tag := ""
				if p == self {
					tag = " [YO]"
				}
				fmt.Printf("[values2] %s%s\n", p.ShortString(), tag)
				if pv.Commit2 != nil {
					fmt.Printf("           commit2  (c_i): %x\n", pv.Commit2)
				}
				if pv.Reveal1 != nil {
					fmt.Printf("           reveal1  (r_i): %x\n", pv.Reveal1)
				}
				if pv.Reveal2 != nil {
					fmt.Printf("           reveal2  (s_i): %x\n", pv.Reveal2)
				}
			}
			fmt.Println("[values2] --- VDF ---")
			if input, ok := n.VDFInput(); ok {
				fmt.Printf("[values2] input:  %x\n", input)
			} else {
				fmt.Println("[values2] input:  no disponible (esperando reveal2 de todos los peers)")
			}
			if result := n.VDFResult(); result != nil {
				fmt.Printf("[values2] output: %x\n", result)
				fmt.Printf("[values2] proof:  %x\n", n.VDFProof())
			} else {
				fmt.Println("[values2] output: no computado aún (ejecutá /vdf)")
			}
			continue
		}
		if text == "/vdf" {
			n.StartVDF(ctx, n.VDFT())
			continue
		}
		if text == "/order" {
			order := n.DoubleCommitReveal().RevealOrder()
			if len(order) == 0 {
				fmt.Println("[order] orden aún no calculado (esperando todos los reveal1)")
				continue
			}
			dists := n.DoubleCommitReveal().RevealDist()
			self := n.DoubleCommitReveal().SelfID()
			fmt.Println("[order] orden de reveal2 (mayor d_i primero):")
			for i, p := range order {
				tag := ""
				if p == self {
					tag = "  [YO]"
				}
				fmt.Printf("[order]   %d. %s  d_i=%x%s\n", i+1, p.ShortString(), dists[p], tag)
			}
			continue
		}
		if text == "/mesh" {
			mesh := n.PubSub().MeshPeers()
			for topic, peers := range mesh {
				if len(peers) == 0 {
					fmt.Printf("[mesh] %s: (vacío)\n", topic)
					continue
				}
				for _, p := range peers {
					fmt.Printf("[mesh] %s: %s\n", topic, p.ShortString())
				}
			}
			continue
		}
		fmt.Printf("[cmd] comando desconocido: %q\n", text)
	}
}
