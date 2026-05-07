package protocol

import (
	"context"
	"crypto/sha256"
	"time"
)

// ComputeMockVDF simula una Verifiable Delay Function mediante un sleep seguido
// de SHA256(input). El delay modela el tiempo secuencial T de la VDF real.
// Retorna nil si el contexto es cancelado antes de terminar.
func ComputeMockVDF(ctx context.Context, input []byte, delay time.Duration) []byte {
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return nil
	}
	h := sha256.Sum256(input)
	return h[:]
}
