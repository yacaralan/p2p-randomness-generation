package node

import (
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// signingBytes construye los bytes canónicos a firmar: phase || peerID || value.
// Incluir la fase evita que una firma válida de commit2 pueda usarse como reveal1.
func signingBytes(phase string, id peer.ID, value []byte) []byte {
	buf := make([]byte, 0, len(phase)+len(id)+len(value))
	buf = append(buf, []byte(phase)...)
	buf = append(buf, []byte(id)...)
	buf = append(buf, value...)
	return buf
}

func signValue(priv crypto.PrivKey, phase string, id peer.ID, value []byte) ([]byte, error) {
	return priv.Sign(signingBytes(phase, id, value))
}

func verifyValue(pub crypto.PubKey, phase string, id peer.ID, value []byte, sig []byte) (bool, error) {
	return pub.Verify(signingBytes(phase, id, value), sig)
}

// pubKeyFor extrae la clave pública de un peer. Para claves Ed25519 (identity multihash),
// la clave está embebida en el peer.ID y no necesita la red. Si falla, usa el peerstore.
func (n *Node) pubKeyFor(id peer.ID) (crypto.PubKey, error) {
	pk, err := id.ExtractPublicKey()
	if err == nil {
		return pk, nil
	}
	pk = n.host.Peerstore().PubKey(id)
	if pk == nil {
		return nil, fmt.Errorf("clave pública de %s no disponible", id.ShortString())
	}
	return pk, nil
}

// extractPhaseValue parsea el JSON de un mensaje firmado y extrae (authorID, value, signature)
// según la fase. Devuelve ok=false si el JSON es inválido o la fase no es reconocida.
func extractPhaseValue(phase string, data []byte) (authorID string, value, sig []byte, ok bool) {
	switch phase {
	case "commit2":
		var m protocol.Commit2Msg
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		return m.AuthorID, m.Hash, m.Signature, true
	case "reveal1":
		var m protocol.Reveal1Msg
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		return m.AuthorID, m.Hash, m.Signature, true
	case "reveal2":
		var m protocol.Reveal2Msg
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		return m.AuthorID, m.Secret, m.Signature, true
	}
	return
}

// verifySignedMsg verifica que authorIDStr == target y que la firma es válida para (phase, value).
func (n *Node) verifySignedMsg(phase string, target peer.ID, authorIDStr string, value []byte, sig []byte) bool {
	authorID, err := peer.Decode(authorIDStr)
	if err != nil || authorID != target {
		fmt.Printf("[crypto] %s de %s: AuthorID no coincide con remitente\n", phase, target.ShortString())
		return false
	}
	pub, err := n.pubKeyFor(target)
	if err != nil {
		fmt.Printf("[crypto] %s de %s: %v\n", phase, target.ShortString(), err)
		return false
	}
	ok, err := verifyValue(pub, phase, target, value, sig)
	if err != nil || !ok {
		fmt.Printf("[crypto] %sERROR:%s firma inválida en %s de %s\n", ansiRed, ansiReset, phase, target.ShortString())
		return false
	}
	return true
}
