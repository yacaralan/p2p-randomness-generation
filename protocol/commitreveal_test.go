package protocol

import (
	"bytes"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"
)

func TestComputeCommitDeterministico(t *testing.T) {
	value := []byte("valor-de-prueba-fijo-de-32-bytes")
	nonce := []byte("nonce-de-prueba-fijo-de-32-bytes")

	h1 := computeCommit(value, nonce)
	h2 := computeCommit(value, nonce)

	if !bytes.Equal(h1, h2) {
		t.Fatalf("computeCommit no determinístico: %x != %x", h1, h2)
	}
	if len(h1) != 32 {
		t.Fatalf("hash SHA-256 debería ser 32 bytes, fue %d", len(h1))
	}
}

func TestStartCommitGeneraValoresAleatorios(t *testing.T) {
	self, _ := test.RandPeerID()
	cr := NewCommitReveal(self)

	hash1, err := cr.StartCommit()
	if err != nil {
		t.Fatalf("primer StartCommit: %v", err)
	}
	v1 := cr.MyValue()

	hash2, err := cr.StartCommit()
	if err != nil {
		t.Fatalf("segundo StartCommit: %v", err)
	}
	v2 := cr.MyValue()

	if len(v1) != valueSize || len(v2) != valueSize {
		t.Fatalf("MyValue debería ser %d bytes", valueSize)
	}
	if bytes.Equal(v1, v2) {
		t.Fatal("dos llamadas a StartCommit produjeron el mismo value")
	}
	if bytes.Equal(hash1, hash2) {
		t.Fatal("dos llamadas a StartCommit produjeron el mismo commit hash")
	}
}

func TestHandleRevealCorrecto(t *testing.T) {
	self, _ := test.RandPeerID()
	other, _ := test.RandPeerID()
	cr := NewCommitReveal(self)

	value := bytes.Repeat([]byte{0xAB}, valueSize)
	nonce := bytes.Repeat([]byte{0xCD}, nonceSize)
	hash := computeCommit(value, nonce)

	cr.HandleCommit(other, hash)
	if !cr.HandleReveal(other, value, nonce).Valid {
		t.Fatal("reveal correcto fue rechazado")
	}

	values := cr.Values()
	got, ok := values[other]
	if !ok {
		t.Fatal("Values() no incluye al peer cuyo reveal fue verificado")
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Values() devolvió %x, esperaba %x", got, value)
	}
}

func TestHandleRevealIncorrectoValorDistinto(t *testing.T) {
	self, _ := test.RandPeerID()
	other, _ := test.RandPeerID()
	cr := NewCommitReveal(self)

	committedValue := bytes.Repeat([]byte{0xAB}, valueSize)
	nonce := bytes.Repeat([]byte{0xCD}, nonceSize)
	hash := computeCommit(committedValue, nonce)
	cr.HandleCommit(other, hash)

	tampered := bytes.Repeat([]byte{0xFF}, valueSize)
	res := cr.HandleReveal(other, tampered, nonce)
	if res.Valid {
		t.Fatal("reveal con value distinto al commiteado fue aceptado")
	}
	if !res.HadCommit {
		t.Fatal("HadCommit debería ser true: el peer sí había commiteado")
	}
	if !bytes.Equal(res.ExpectedCommit, hash) {
		t.Fatalf("ExpectedCommit %x != commit registrado %x", res.ExpectedCommit, hash)
	}
	if bytes.Equal(res.ComputedCommit, hash) {
		t.Fatal("ComputedCommit coincide con el commit registrado, debería diferir")
	}
	if _, ok := cr.Values()[other]; ok {
		t.Fatal("Values() incluye un peer cuyo reveal fue inválido")
	}
}

func TestHandleRevealSinCommitPrevio(t *testing.T) {
	self, _ := test.RandPeerID()
	other, _ := test.RandPeerID()
	cr := NewCommitReveal(self)

	value := bytes.Repeat([]byte{0xAB}, valueSize)
	nonce := bytes.Repeat([]byte{0xCD}, nonceSize)

	res := cr.HandleReveal(other, value, nonce)
	if res.Valid {
		t.Fatal("reveal sin commit previo fue aceptado")
	}
	if res.HadCommit {
		t.Fatal("HadCommit debería ser false: el peer nunca commiteó")
	}
}

func TestHandleCommitGuardaPorPeer(t *testing.T) {
	self, _ := test.RandPeerID()
	peerA, _ := test.RandPeerID()
	peerB, _ := test.RandPeerID()
	cr := NewCommitReveal(self)

	valueA := bytes.Repeat([]byte{0x01}, valueSize)
	nonceA := bytes.Repeat([]byte{0x02}, nonceSize)
	valueB := bytes.Repeat([]byte{0x03}, valueSize)
	nonceB := bytes.Repeat([]byte{0x04}, nonceSize)

	cr.HandleCommit(peerA, computeCommit(valueA, nonceA))
	cr.HandleCommit(peerB, computeCommit(valueB, nonceB))

	if !cr.HandleReveal(peerA, valueA, nonceA).Valid {
		t.Fatal("reveal de peerA contra su propio commit falló")
	}
	if !cr.HandleReveal(peerB, valueB, nonceB).Valid {
		t.Fatal("reveal de peerB contra su propio commit falló")
	}

	if cr.HandleReveal(peerA, valueB, nonceB).Valid {
		t.Fatal("reveal de peerA con datos de peerB fue aceptado (cross-peer leak)")
	}
}
