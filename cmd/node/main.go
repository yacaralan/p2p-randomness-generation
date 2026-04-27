// Package main es el punto de entrada del binario del nodo.
// Este archivo conecta el CLI (flags de línea de comandos) con el código
// del nodo, y mantiene el proceso vivo hasta recibir una señal de apagado.
//
// Uso:
//
//	go run ./cmd/node [flags]
//
// Flags disponibles:
//
//	--port int     Puerto TCP a usar (default: 0 = asignado por el SO)
//	--peer string  Multiaddrs de peers de bootstrap, separados por comas
//	--ping         Si está activado, envía un Ping a todos los peers cada 5 segundos
//
// Ejemplo con dos nodos:
//
//	# Terminal 1
//	go run ./cmd/node --port 4001 --ping
//
//	# Terminal 2 (el nodo 2 descubre al nodo 1 automáticamente via mDNS)
//	go run ./cmd/node --port 4002 --ping
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

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/node"
)

func main() {
	// Definimos los flags de línea de comandos usando el paquete estándar "flag".
	portFlag := flag.Int("port", 0, "Puerto TCP a escuchar (0 = asignado automáticamente)")
	peersFlag := flag.String("peer", "", "Multiaddrs de bootstrap separados por comas")
	pingFlag := flag.Bool("ping", false, "Enviar Ping a todos los peers cada 5 segundos")
	flag.Parse()

	cfg := node.DefaultConfig()
	cfg.Port = *portFlag
	if *peersFlag != "" {
		cfg.BootstrapPeers = strings.Split(*peersFlag, ",")
	}

	// Creamos el nodo (genera identidad, crea el host libp2p).
	n, err := node.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creando nodo: %v\n", err)
		os.Exit(1)
	}

	// Creamos un contexto cancelable. Al llamar cancel(), todas las operaciones
	// que usen este contexto (Ping, Connect, etc.) se interrumpen inmediatamente.
	// Esto es necesario para que n.Close() no quede bloqueado esperando que
	// terminen operaciones de red que ya no tienen sentido completar.
	ctx, cancel := context.WithCancel(context.Background())

	// Iniciamos los subsistemas: protocolo, descubrimiento, bootstrap.
	if err := n.Start(ctx); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "error iniciando nodo: %v\n", err)
		os.Exit(1)
	}

	// Si se activó el flag --ping, lanzamos una goroutine que periódicamente
	// envía un Ping a todos los peers conectados.
	// Esto sirve para demostrar visualmente que la comunicación funciona.
	if *pingFlag {
		go pingLoop(ctx, n)
	}

	// Registramos el handler de mensajes de chat entrantes vía gossipsub.
	if err := n.PubSub().SubscribeChat(ctx, func(from peer.ID, text string) {
		fmt.Printf("[chat] %s: %s\n", from.ShortString(), text)
	}); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "error suscribiéndose a chat: %v\n", err)
		os.Exit(1)
	}

	// chatLoop lee mensajes de stdin y los publica vía gossipsub.
	go chatLoop(ctx, n)

	fmt.Println("[main] nodo corriendo. Escribí un mensaje y Enter para enviarlo a todos los peers. Ctrl+C para salir.")

	// Bloqueamos la goroutine principal hasta recibir SIGINT (Ctrl+C) o SIGTERM.
	// Esto es el patrón estándar en Go para programas de larga duración.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\n[main] apagando nodo...")

	// Primero cancelamos el contexto: interrumpe NewStream y Connect en curso,
	// y le señala al pingLoop que debe terminar.
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

// chatLoop lee líneas de stdin y las publica en el topic gossipsub de chat.
func chatLoop(ctx context.Context, n *node.Node) {
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
		if err := n.PubSub().PublishChat(ctx, text); err != nil {
			fmt.Printf("[chat] error publicando: %v\n", err)
		}
	}
}

// pingLoop envía un Ping a cada peer conectado cada 5 segundos.
// Corre en su propia goroutine para no bloquear el hilo principal.
//
// n.Host().Network().Peers() devuelve la lista de PeerIDs con los que
// tenemos una conexión activa en este momento. La lista puede cambiar
// dinámicamente a medida que peers se conectan y desconectan.
func pingLoop(ctx context.Context, n *node.Node) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			peers := n.Host().Network().Peers()
			if len(peers) == 0 {
				fmt.Println("[ping] sin peers conectados todavía...")
				continue
			}
			for _, peerID := range peers {
				if err := n.Handler().Ping(ctx, peerID); err != nil {
					fmt.Printf("[ping] error enviando ping a %s: %v\n", peerID.ShortString(), err)
				}
			}
		case <-ctx.Done():
			// El contexto fue cancelado (apagado del nodo), salimos del loop.
			return
		}
	}
}
