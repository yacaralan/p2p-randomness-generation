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
	TopicControl = "randomness/control"
)

// PubSub gestiona la comunicación broadcast del protocolo usando gossipsub.
//
// Los topics mapean a las fases del protocolo Commit-Reveal²:
//   - randomness/control:  dispara acciones globales
//   - randomness/commit2:  cada nodo publica c_i = H(H(s_i))
//   - randomness/reveal1:  cada nodo publica r_i = H(s_i)
//   - randomness/reveal2:  cada nodo publica s_i
type PubSub struct {
	ps           *pubsub.PubSub
	controlTopic *pubsub.Topic
	commit2Topic *pubsub.Topic
	reveal1Topic *pubsub.Topic
	reveal2Topic *pubsub.Topic
	localPeerID  peer.ID
}

// NewPubSub crea una instancia de gossipsub y se une a los topics del protocolo.
func NewPubSub(ctx context.Context, h host.Host) (*PubSub, error) {
	gs, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("crear gossipsub: %w", err)
	}

	controlTopic, err := gs.Join(TopicControl)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicControl, err)
	}

	commit2Topic, err := gs.Join(TopicCommit2)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicCommit2, err)
	}
	reveal1Topic, err := gs.Join(TopicReveal1)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicReveal1, err)
	}
	reveal2Topic, err := gs.Join(TopicReveal2)
	if err != nil {
		return nil, fmt.Errorf("unirse a topic %s: %w", TopicReveal2, err)
	}

	return &PubSub{
		ps:           gs,
		controlTopic: controlTopic,
		commit2Topic: commit2Topic,
		reveal1Topic: reveal1Topic,
		reveal2Topic: reveal2Topic,
		localPeerID:  h.ID(),
	}, nil
}

// PublishControl dispara una acción global publicándola en randomness/control.
func (p *PubSub) PublishControl(ctx context.Context, action ControlAction) error {
	return p.PublishControlWithPayload(ctx, action, "")
}

// PublishControlWithPayload es igual que PublishControl pero adjunta un payload opcional.
func (p *PubSub) PublishControlWithPayload(ctx context.Context, action ControlAction, payload string) error {
	data, err := json.Marshal(ControlMsg{Action: action, Payload: payload})
	if err != nil {
		return fmt.Errorf("serializar control: %w", err)
	}
	return p.controlTopic.Publish(ctx, data)
}

// SubscribeControl recibe acciones de control. NO filtra los propios:
// el nodo que disparó /commit también debe ejecutar el commit cuando
// el mensaje vuelve a través de gossipsub.
func (p *PubSub) SubscribeControl(ctx context.Context, handler func(from peer.ID, action ControlAction, payload string)) error {
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
			handler(from, m.Action, m.Payload)
		}
	}()
	return nil
}

// PublishCommit2 publica un Commit2Msg firmado en randomness/commit2.
func (p *PubSub) PublishCommit2(ctx context.Context, msg Commit2Msg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serializar commit2: %w", err)
	}
	return p.commit2Topic.Publish(ctx, data)
}

// SubscribeCommit2 recibe commit2 de otros peers (filtra los propios).
// El callback recibe el mensaje completo incluyendo firma para que el caller pueda verificarla.
func (p *PubSub) SubscribeCommit2(ctx context.Context, handler func(from peer.ID, msg Commit2Msg)) error {
	sub, err := p.commit2Topic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicCommit2, err)
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
			var m Commit2Msg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] commit2 inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m)
		}
	}()
	return nil
}

// PublishReveal1 publica un Reveal1Msg firmado en randomness/reveal1.
func (p *PubSub) PublishReveal1(ctx context.Context, msg Reveal1Msg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serializar reveal1: %w", err)
	}
	return p.reveal1Topic.Publish(ctx, data)
}

// SubscribeReveal1 recibe reveal1 de otros peers (filtra los propios).
func (p *PubSub) SubscribeReveal1(ctx context.Context, handler func(from peer.ID, msg Reveal1Msg)) error {
	sub, err := p.reveal1Topic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicReveal1, err)
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
			var m Reveal1Msg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] reveal1 inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m)
		}
	}()
	return nil
}

// PublishReveal2 publica un Reveal2Msg firmado en randomness/reveal2.
func (p *PubSub) PublishReveal2(ctx context.Context, msg Reveal2Msg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("serializar reveal2: %w", err)
	}
	return p.reveal2Topic.Publish(ctx, data)
}

// SubscribeReveal2 recibe reveal2 de otros peers (filtra los propios).
func (p *PubSub) SubscribeReveal2(ctx context.Context, handler func(from peer.ID, msg Reveal2Msg)) error {
	sub, err := p.reveal2Topic.Subscribe()
	if err != nil {
		return fmt.Errorf("suscribirse a %s: %w", TopicReveal2, err)
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
			var m Reveal2Msg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				fmt.Printf("[pubsub] reveal2 inválido de %s: %v\n", from.ShortString(), err)
				continue
			}
			handler(from, m)
		}
	}()
	return nil
}

// MeshPeers devuelve los peers suscritos en cada topic del protocolo.
func (p *PubSub) MeshPeers() map[string][]peer.ID {
	return map[string][]peer.ID{
		TopicControl: p.controlTopic.ListPeers(),
		TopicCommit2: p.commit2Topic.ListPeers(),
		TopicReveal1: p.reveal1Topic.ListPeers(),
		TopicReveal2: p.reveal2Topic.ListPeers(),
	}
}

// Close libera los topics. El *pubsub.PubSub subyacente se cierra
// cuando el host libp2p se cierra.
func (p *PubSub) Close() {
	p.controlTopic.Close()
	p.commit2Topic.Close()
	p.reveal1Topic.Close()
	p.reveal2Topic.Close()
}
