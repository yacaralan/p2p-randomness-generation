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
	TopicCommit  = "randomness/commit"
	TopicReveal  = "randomness/reveal"
	TopicChat    = "randomness/chat"
	TopicControl = "randomness/control"
)

// PubSub gestiona la comunicación broadcast del protocolo usando gossipsub.
//
// Gossipsub difunde mensajes a todos los suscriptores de un topic a la vez,
// a diferencia de los streams directos que requieren abrir una conexión por peer.
// Esta propiedad es fundamental para la equidad temporal Δ-acotada: el Δ queda
// determinado por la latencia de propagación de gossipsub, no por decisiones
// del nodo publicador.
//
// Los topics mapean a las fases del protocolo:
//   - randomness/commit:  cada nodo publica hash(value || nonce)
//   - randomness/reveal:  cada nodo publica (value, nonce)
//   - randomness/control: dispara acciones globales (/commit, /reveal)
//   - randomness/chat:    broadcast de texto para demostración interactiva
type PubSub struct {
	ps           *pubsub.PubSub
	commitTopic  *pubsub.Topic
	revealTopic  *pubsub.Topic
	chatTopic    *pubsub.Topic
	controlTopic *pubsub.Topic
	localPeerID  peer.ID
}

// NewPubSub crea una instancia de gossipsub y se une a los topics del protocolo.
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
	controlTopic, err := gs.Join(TopicControl)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicControl, err)
	}

	return &PubSub{
		ps:           gs,
		commitTopic:  commitTopic,
		revealTopic:  revealTopic,
		chatTopic:    chatTopic,
		controlTopic: controlTopic,
		localPeerID:  h.ID(),
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
// Ignora los mensajes enviados por el propio nodo.
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
				return
			}
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

// PublishCommit publica un commit hash en randomness/commit.
func (p *PubSub) PublishCommit(ctx context.Context, hash []byte) error {
	data, err := json.Marshal(CommitMsg{Hash: hash})
	if err != nil {
		return fmt.Errorf("serializar commit: %w", err)
	}
	return p.commitTopic.Publish(ctx, data)
}

// SubscribeCommit recibe commits de otros peers (filtra los propios).
func (p *PubSub) SubscribeCommit(ctx context.Context, handler func(from peer.ID, hash []byte)) error {
	sub, err := p.commitTopic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicCommit, err)
	}
	go func() {
		defer sub.Cancel()
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				return
			}
			from := peer.ID(msg.GetFrom())
			if from == p.localPeerID {
				continue
			}
			var m CommitMsg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] commit inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m.Hash)
		}
	}()
	return nil
}

// PublishReveal publica un (value, nonce) en randomness/reveal.
func (p *PubSub) PublishReveal(ctx context.Context, value, nonce []byte) error {
	data, err := json.Marshal(RevealMsg{Value: value, Nonce: nonce})
	if err != nil {
		return fmt.Errorf("serializar reveal: %w", err)
	}
	return p.revealTopic.Publish(ctx, data)
}

// SubscribeReveal recibe reveals de otros peers (filtra los propios).
func (p *PubSub) SubscribeReveal(ctx context.Context, handler func(from peer.ID, value, nonce []byte)) error {
	sub, err := p.revealTopic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicReveal, err)
	}
	go func() {
		defer sub.Cancel()
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				return
			}
			from := peer.ID(msg.GetFrom())
			if from == p.localPeerID {
				continue
			}
			var m RevealMsg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] reveal inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m.Value, m.Nonce)
		}
	}()
	return nil
}

// PublishControl dispara una acción global publicándola en randomness/control.
func (p *PubSub) PublishControl(ctx context.Context, action ControlAction) error {
	data, err := json.Marshal(ControlMsg{Action: action})
	if err != nil {
		return fmt.Errorf("serializar control: %w", err)
	}
	return p.controlTopic.Publish(ctx, data)
}

// SubscribeControl recibe acciones de control. NO filtra los propios:
// el nodo que disparó /commit también debe ejecutar el commit cuando
// el mensaje vuelve a través de gossipsub.
func (p *PubSub) SubscribeControl(ctx context.Context, handler func(from peer.ID, action ControlAction)) error {
	sub, err := p.controlTopic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicControl, err)
	}
	go func() {
		defer sub.Cancel()
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				return
			}
			from := peer.ID(msg.GetFrom())
			var m ControlMsg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] control inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m.Action)
		}
	}()
	return nil
}

// MeshPeers devuelve los peers suscritos en cada topic del protocolo.
func (p *PubSub) MeshPeers() map[string][]peer.ID {
	return map[string][]peer.ID{
		TopicCommit:  p.commitTopic.ListPeers(),
		TopicReveal:  p.revealTopic.ListPeers(),
		TopicChat:    p.chatTopic.ListPeers(),
		TopicControl: p.controlTopic.ListPeers(),
	}
}

// Close libera los topics. El *pubsub.PubSub subyacente se cierra
// cuando el host libp2p se cierra.
func (p *PubSub) Close() {
	p.commitTopic.Close()
	p.revealTopic.Close()
	p.chatTopic.Close()
	p.controlTopic.Close()
}
