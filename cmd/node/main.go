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
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ayacar/p2p-randomness-generation/node"
)

func main() {
	// Definimos los flags de línea de comandos usando el paquete estándar "flag".
	// No usamos librerías externas (cobra, urfave/cli) para mantener las
	// dependencias al mínimo en esta primera iteración.
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

	// context.Background() como contexto raíz: no tiene deadline ni cancelación.
	// En iteraciones futuras usaremos un context con cancelación para
	// poder apagar el nodo limpiamente desde cualquier goroutine.
	ctx := context.Background()

	// Iniciamos los subsistemas: protocolo, descubrimiento, bootstrap.
	if err := n.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error iniciando nodo: %v\n", err)
		os.Exit(1)
	}

	// defer garantiza que el nodo se cierre limpiamente cuando main() retorne,
	// incluso si hay un panic o un return anticipado.
	defer n.Close()

	// Si se activó el flag --ping, lanzamos una goroutine que periódicamente
	// envía un Ping a todos los peers conectados.
	// Esto sirve para demostrar visualmente que la comunicación funciona.
	if *pingFlag {
		go pingLoop(ctx, n)
	}

	fmt.Println("[main] nodo corriendo. Presioná Ctrl+C para salir.")

	// Bloqueamos la goroutine principal hasta recibir SIGINT (Ctrl+C) o SIGTERM.
	// Esto es el patrón estándar en Go para programas de larga duración.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\n[main] apagando nodo...")
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
