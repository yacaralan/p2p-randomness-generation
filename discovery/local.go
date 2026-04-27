package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// localDir es el directorio compartido entre todos los nodos de la misma máquina.
// Cada nodo escribe un archivo con su PeerID como nombre y su multiaddr como contenido.
const localDir = "/tmp/p2p-randomness"

// LocalDiscovery implementa descubrimiento entre nodos en la misma máquina
// usando un directorio compartido en /tmp como rendezvous point.
//
// Esto resuelve la limitación de mDNS en macOS: los paquetes multicast
// entre procesos del mismo host no siempre se enrutan correctamente.
// /tmp no tiene esa limitación: cualquier proceso puede leer y escribir ahí.
type LocalDiscovery struct {
	host   host.Host
	cancel context.CancelFunc
}

// NewLocalDiscovery inicia el descubrimiento local: escribe la dirección del nodo
// en el directorio compartido y lanza un loop que conecta a nuevos nodos que aparezcan.
func NewLocalDiscovery(ctx context.Context, h host.Host) (*LocalDiscovery, error) {
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return nil, fmt.Errorf("crear directorio rendezvous: %w", err)
	}
	if err := writeOwnAddr(h); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	ld := &LocalDiscovery{host: h, cancel: cancel}
	go ld.loop(ctx)

	fmt.Printf("[local-discovery] iniciado, rendezvous en %s\n", localDir)
	return ld, nil
}

// writeOwnAddr escribe las multiaddrs del nodo en el archivo del directorio compartido.
// Reemplaza 0.0.0.0 por 127.0.0.1 para que otros procesos en la misma máquina
// puedan conectarse directamente via loopback.
func writeOwnAddr(h host.Host) error {
	var addrs []string
	for _, laddr := range h.Network().ListenAddresses() {
		addrStr := strings.ReplaceAll(laddr.String(), "/ip4/0.0.0.0/", "/ip4/127.0.0.1/")
		addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", addrStr, h.ID()))
	}
	if len(addrs) == 0 {
		return fmt.Errorf("no hay direcciones de escucha disponibles")
	}
	path := filepath.Join(localDir, h.ID().String())
	return os.WriteFile(path, []byte(strings.Join(addrs, "\n")), 0644)
}

func (d *LocalDiscovery) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.scan(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (d *LocalDiscovery) scan(ctx context.Context) {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == d.host.ID().String() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(localDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			info, err := peer.AddrInfoFromString(line)
			if err != nil {
				continue
			}
			if d.host.Network().Connectedness(info.ID) == network.Connected {
				continue
			}
			if err := d.host.Connect(ctx, *info); err == nil {
				fmt.Printf("[local-discovery] conectado a %s\n", info.ID.ShortString())
			}
		}
	}
}

// Close cancela el loop y elimina el archivo del nodo del directorio compartido.
func (d *LocalDiscovery) Close() error {
	d.cancel()
	return os.Remove(filepath.Join(localDir, d.host.ID().String()))
}
