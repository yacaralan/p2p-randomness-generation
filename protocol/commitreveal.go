package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// valueSize y nonceSize fijan el tamaño del valor secreto y del nonce
// que cada nodo aporta a la ronda. 32 bytes alcanzan para usar el output
// directamente como semilla criptográfica (256 bits) y para que los
// hashes SHA-256 se vean balanceados.
const (
	valueSize = 32
	nonceSize = 32
)

// computeCommit calcula el hash del commit como SHA-256(value || nonce).
// La concatenación con un nonce aleatorio es lo que hace al commit
// indistinguible de un hash de cualquier otro valor: sin nonce, un
// adversario podría precomputar hashes de valores predecibles.
func computeCommit(value, nonce []byte) []byte {
	h := sha256.New()
	h.Write(value)
	h.Write(nonce)
	return h.Sum(nil)
}

// CommitReveal mantiene el estado de una ejecución del protocolo commit-reveal.
//
// El prototipo modela una única ejecución: no hay número de ronda. Si se
// dispara un nuevo commit, los maps se resetean y el valor propio se
// regenera. El struct es thread-safe (un mutex protege todo el estado).
type CommitReveal struct {
	mu sync.Mutex

	self    peer.ID
	myValue []byte
	myNonce []byte

	commits        map[peer.ID][]byte
	verifiedValues map[peer.ID][]byte
}

// NewCommitReveal crea un struct vacío. self es el PeerID del nodo local,
// que se usa para identificar el "yo" cuando se loggean los valores.
func NewCommitReveal(self peer.ID) *CommitReveal {
	return &CommitReveal{
		self:           self,
		commits:        make(map[peer.ID][]byte),
		verifiedValues: make(map[peer.ID][]byte),
	}
}

// StartCommit genera un nuevo (value, nonce) aleatorio para este nodo
// y devuelve el commit hash listo para ser publicado por el caller.
func (cr *CommitReveal) StartCommit() ([]byte, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	value := make([]byte, valueSize)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("generar value: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generar nonce: %w", err)
	}

	cr.myValue = value
	cr.myNonce = nonce

	return computeCommit(value, nonce), nil
}

// HandleCommit registra el commit hash recibido de un peer. Sobreescribe
// si ya había uno previo (decisión consciente: una sola ejecución viva).
func (cr *CommitReveal) HandleCommit(from peer.ID, hash []byte) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cp := make([]byte, len(hash))
	copy(cp, hash)
	cr.commits[from] = cp
}

// StartReveal devuelve el (value, nonce) propios para que el caller
// los publique. Devuelve error si nunca se llamó a StartCommit.
func (cr *CommitReveal) StartReveal() (value, nonce []byte, err error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.myValue == nil {
		return nil, nil, fmt.Errorf("no hay commit propio: ejecutá /commit primero")
	}
	return cr.myValue, cr.myNonce, nil
}

// RevealResult describe el resultado de verificar un reveal contra su commit.
//
// Valid es true sólo si el peer había commiteado y el hash recomputado
// coincide con el commit. ExpectedCommit es nil cuando el peer nunca
// commiteó (en ese caso ComputedCommit igual se calcula para reportarlo).
type RevealResult struct {
	Valid          bool
	HadCommit      bool
	ExpectedCommit []byte
	ComputedCommit []byte
}

// HandleReveal verifica un reveal recibido de un peer contra el commit
// previamente registrado. En caso válido, guarda el value en verifiedValues
// para que /values pueda mostrarlo. En caso inválido, devuelve los hashes
// involucrados para que el caller pueda loggear por qué falló.
func (cr *CommitReveal) HandleReveal(from peer.ID, value, nonce []byte) RevealResult {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	got := computeCommit(value, nonce)

	expected, ok := cr.commits[from]
	if !ok {
		return RevealResult{Valid: false, HadCommit: false, ComputedCommit: got}
	}
	if subtle.ConstantTimeCompare(expected, got) != 1 {
		return RevealResult{Valid: false, HadCommit: true, ExpectedCommit: expected, ComputedCommit: got}
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	cr.verifiedValues[from] = cp
	return RevealResult{Valid: true, HadCommit: true, ExpectedCommit: expected, ComputedCommit: got}
}

// Values devuelve una copia del map de values verificados (peer → value).
// Sólo incluye peers cuyo reveal pasó la verificación contra su commit.
func (cr *CommitReveal) Values() map[peer.ID][]byte {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	out := make(map[peer.ID][]byte, len(cr.verifiedValues))
	for p, v := range cr.verifiedValues {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[p] = cp
	}
	return out
}

// MyValue devuelve una copia del value propio, o nil si nunca se commiteó.
func (cr *CommitReveal) MyValue() []byte {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.myValue == nil {
		return nil
	}
	cp := make([]byte, len(cr.myValue))
	copy(cp, cr.myValue)
	return cp
}

