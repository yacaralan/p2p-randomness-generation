package protocol

import (
	"context"
	"crypto/sha256"

	vdfgo "github.com/harmony-one/vdf/src/vdf_go"
)

// ComputeVDF ejecuta la VDF de Wesolowski con `iterations` squarings sobre grupos de clase de 2048 bits.
// El input se hashea a 32 bytes (SHA256). Retorna (output[258], proof[258], nil).
// Si el contexto se cancela antes de terminar, retorna (nil, nil, ctx.Err()).
func ComputeVDF(ctx context.Context, input []byte, iterations int) (output []byte, proof []byte, err error) {
	seed := sha256.Sum256(input)
	v := vdfgo.New(iterations, seed)
	ch := v.GetOutputChannel()
	go v.Execute()
	select {
	case result := <-ch:
		return result[:258], result[258:], nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// VerifyVDF verifica que output+proof son la respuesta correcta de la VDF para el input e iterations dados.
func VerifyVDF(input []byte, iterations int, output []byte, proof []byte) bool {
	if len(output) != 258 || len(proof) != 258 {
		return false
	}
	seed := sha256.Sum256(input)
	v := vdfgo.New(iterations, seed)
	var combined [516]byte
	copy(combined[:258], output)
	copy(combined[258:], proof)
	return v.Verify(combined)
}
