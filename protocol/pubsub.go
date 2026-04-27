package protocol

import (
	"context"
	"encoding/json"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	TopicCommit = "randomness/commit"
	TopicReveal = "randomness/reveal"
	TopicChat   = "randomness/chat"
)

// PubSub gestiona la comunicación broadcast del protocolo usando gossipsub.
//
// Gossipsub difunde mensajes a todos los suscriptores de un topic a la vez,
// a diferencia de los streams directos que requieren abrir una conexión por peer.
// Esta propiedad es fundamental para la equidad temporal Δ-acotada: el Δ queda
// determinado por la latencia de propagación de gossipsub, no por decisiones
// del nodo publicador.
//
// Los tres topics mapean a las fases del protocolo:
//   - randomness/commit: cada nodo publica hash(valor_secreto || nonce)
//   - randomness/reveal: cada nodo publica valor_secreto || nonce
//   - randomness/chat:   broadcast de texto para demostración interactiva
type PubSub struct {
	ps          *pubsub.PubSub
	commitTopic *pubsub.Topic
	revealTopic *pubsub.Topic
	chatTopic   *pubsub.Topic
	localPeerID peer.ID
}

// NewPubSub crea una instancia de gossipsub y se une a los tres topics del protocolo.
//
// gossipsub es el algoritmo de difusión usado en producción por Ethereum y Filecoin.
// Mantiene una malla de peers por topic y propaga mensajes en O(log n) saltos.
func NewPubSub(ctx context.Context, h host.Host) (*PubSub, error) {
	gs, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("crear gossipsub: %w", err)
	}

	commitTopic, err := gs.Join(TopicCommit)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicCommit, err)
	}

	revealTopic, err := gs.Join(TopicReveal)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicReveal, err)
	}

	chatTopic, err := gs.Join(TopicChat)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicChat, err)
	}

	return &PubSub{
		ps:          gs,
		commitTopic: commitTopic,
		revealTopic: revealTopic,
		chatTopic:   chatTopic,
		localPeerID: h.ID(),
	}, nil
}

// PublishChat publica un mensaje de texto en el topic de chat.
func (p *PubSub) PublishChat(ctx context.Context, text string) error {
	msg := Message{Type: MessageTypeChat, Payload: text}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serializar mensaje: %w", err)
	}
	return p.chatTopic.Publish(ctx, data)
}

// SubscribeChat se suscribe al topic de chat y llama a handler por cada mensaje recibido.
// Ignora los mensajes enviados por el propio nodo (gossipsub los retransmite localmente).
// Corre en una goroutine que termina cuando ctx es cancelado.
func (p *PubSub) SubscribeChat(ctx context.Context, handler func(from peer.ID, text string)) error {
	sub, err := p.chatTopic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicChat, err)
	}

	go func() {
		defer sub.Cancel()
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				// ctx cancelado: salida normal
				return
			}
			// msg.ReceivedFrom es el último salto (quien nos reenvió el mensaje).
			// msg.GetFrom() es el publicador original; es lo que queremos mostrar.
			from := peer.ID(msg.GetFrom())
			if from == p.localPeerID {
				continue
			}
			var m Message
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] mensaje inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m.Payload)
		}
	}()

	return nil
}

// MeshPeers devuelve los peers suscritos en cada topic del protocolo.
// Topic.ListPeers() es la API pública de gossipsub para inspeccionar
// qué peers participan en cada topic.
func (p *PubSub) MeshPeers() map[string][]peer.ID {
	return map[string][]peer.ID{
		TopicCommit: p.commitTopic.ListPeers(),
		TopicReveal: p.revealTopic.ListPeers(),
		TopicChat:   p.chatTopic.ListPeers(),
	}
}

// Close libera los topics. El *pubsub.PubSub subyacente se cierra
// cuando el host libp2p se cierra.
func (p *PubSub) Close() {
	p.commitTopic.Close()
	p.revealTopic.Close()
	p.chatTopic.Close()
}
