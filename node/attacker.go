package node

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// AttackerBehavior define el comportamiento adversarial inyectable en cada fase del protocolo.
// Cada método responde a una decisión que el atacante toma en una fase específica.
// El perfil "honest" implementa el comportamiento normal: todos los métodos retornan
// el valor sin modificar y los flags retornan false.
type AttackerBehavior interface {
	// SESSION_LOCK
	ShouldSendReadyAck() bool // false = omitir READY_ACK; el proponente nunca alcanza quórum

	// Commit2
	ShouldSkipCommit() bool           // true = no enviar commit2 ni llamar StartCommit2
	ModifyCommit(real []byte) []byte  // puede retornar hash falso; solo llamado si !ShouldSkipCommit
	ShouldEquivocateCommit() bool     // true = enviar un segundo commit2 con hash diferente (equivocación)

	// Reveal1
	ShouldSkipReveal1() bool          // true = no enviar reveal1 (aborto estratégico o no participación)
	ModifyReveal1(real []byte) []byte // puede retornar hash falso; solo llamado si !ShouldSkipReveal1

	// Reveal1 — decisión estratégica del último revelador
	IsStrategicReveal1() bool                  // true = diferir la decisión de reveal1 hasta ver los reveal1 ajenos
	ShouldReveal1IfLast(wouldBeLast bool) bool // true = publicar reveal1; sólo se llama si IsStrategicReveal1

	// Reveal2
	ShouldSkipReveal2() bool          // true = no enviar reveal2 (deja expirar timer de turno)
	ModifyReveal2(real []byte) []byte // puede retornar secreto falso; solo llamado si !ShouldSkipReveal2

	// Reveal2 — ventajista temporal del último revelador (early VDF + delay del reveal2).
	// El caller sólo lo aplica cuando el nodo es efectivamente el último en revelar.
	IsLastRevealerVDF() bool // true = precomputar VDF y demorar el reveal2 (sólo si es último)

	// Reveal2 — decisión estratégica del último revelador
	IsStrategicReveal2() bool                         // true = activar la elección de input de VDF en reveal2
	ShouldAbortReveal2(ifReveal, ifAbort []byte) bool // true = abortar; sólo se llama si IsStrategicReveal2

	// Timeout/disputa
	ShouldVoteFalseTimeout() bool // true = emitir TIMEOUT_VOTE contra nodos que sí respondieron

	Name() string
}

// --- Comportamiento honesto (por defecto) ---

type honestBehavior struct{}

func (h *honestBehavior) ShouldSendReadyAck() bool                { return true }
func (h *honestBehavior) ShouldSkipCommit() bool                  { return false }
func (h *honestBehavior) ModifyCommit(v []byte) []byte            { return v }
func (h *honestBehavior) ShouldEquivocateCommit() bool            { return false }
func (h *honestBehavior) ShouldSkipReveal1() bool                 { return false }
func (h *honestBehavior) ModifyReveal1(v []byte) []byte           { return v }
func (h *honestBehavior) IsStrategicReveal1() bool                { return false }
func (h *honestBehavior) ShouldReveal1IfLast(_ bool) bool         { return false }
func (h *honestBehavior) ShouldSkipReveal2() bool                 { return false }
func (h *honestBehavior) ModifyReveal2(v []byte) []byte           { return v }
func (h *honestBehavior) IsLastRevealerVDF() bool                 { return false }
func (h *honestBehavior) IsStrategicReveal2() bool                { return false }
func (h *honestBehavior) ShouldAbortReveal2(_, _ []byte) bool     { return false }
func (h *honestBehavior) ShouldVoteFalseTimeout() bool            { return false }
func (h *honestBehavior) Name() string                            { return "honest" }

// --- Perfiles de atacante ---

// SESSION_LOCK: no envía READY_ACK → el proponente nunca alcanza quórum.
type noReadyAckBehavior struct{ honestBehavior }

func (b *noReadyAckBehavior) ShouldSendReadyAck() bool { return false }
func (b *noReadyAckBehavior) Name() string             { return "no-ready-ack" }

// Commit2: envía c_i aleatorio sin preimagen válida → rechazado en reveal1.
type commitInvalidBehavior struct{ honestBehavior }

func (b *commitInvalidBehavior) ModifyCommit(_ []byte) []byte { return randomBytes(32) }
func (b *commitInvalidBehavior) Name() string                 { return "commit-invalid" }

// Commit2: no envía commit2 → timeout por mayoría → abortado y excluido.
type noCommitBehavior struct{ honestBehavior }

func (b *noCommitBehavior) ShouldSkipCommit() bool { return true }
func (b *noCommitBehavior) Name() string           { return "no-commit" }

// Commit2: envía dos commit2 con hashes distintos → equivocación detectada → abortado.
type equivocateCommitBehavior struct{ honestBehavior }

func (b *equivocateCommitBehavior) ShouldEquivocateCommit() bool { return true }
func (b *equivocateCommitBehavior) Name() string                 { return "equivocate-commit" }

// Reveal1: no envía reveal1 → timeout por mayoría → abortado, excluido del revealOrder.
type noReveal1Behavior struct{ honestBehavior }

func (b *noReveal1Behavior) ShouldSkipReveal1() bool { return true }
func (b *noReveal1Behavior) Name() string            { return "no-reveal1" }

// Reveal1: envía r_i' aleatorio con H(r_i') ≠ c_i → verificación inmediata falla → excluido.
type reveal1InvalidBehavior struct{ honestBehavior }

func (b *reveal1InvalidBehavior) ModifyReveal1(_ []byte) []byte { return randomBytes(32) }
func (b *reveal1InvalidBehavior) Name() string                  { return "reveal1-invalid" }

// Reveal1: aborto estratégico del "último revelador". Difiere la publicación de r_i
// (ShouldSkipReveal1 evita publicarlo automáticamente) y, una vez recibidos todos los
// reveal1 ajenos, calcula el orden hipotético: sólo publica r_i si lo dejaría como
// último en reveal2 (posición ventajosa). Si no sería último, aborta.
type lastRevealerAbortBehavior struct{ honestBehavior }

func (b *lastRevealerAbortBehavior) ShouldSkipReveal1() bool            { return true }
func (b *lastRevealerAbortBehavior) IsStrategicReveal1() bool           { return true }
func (b *lastRevealerAbortBehavior) ShouldReveal1IfLast(last bool) bool { return last }
func (b *lastRevealerAbortBehavior) Name() string                      { return "last-revealer-abort" }

// Reveal2: aborto estratégico del último revelador. Cuando le toca revelar (es el
// último del orden y observa todos los s_j), calcula los dos posibles inputs de la VDF
// —revelando s_i vs abortando con r_i como fallback— y elige la acción que produzca el
// input numéricamente menor. Es el análogo en reveal2 de last-revealer-abort.
type lastRevealerAbortR2Behavior struct{ honestBehavior }

func (b *lastRevealerAbortR2Behavior) IsStrategicReveal2() bool { return true }
func (b *lastRevealerAbortR2Behavior) ShouldAbortReveal2(ifReveal, ifAbort []byte) bool {
	return new(big.Int).SetBytes(ifAbort).Cmp(new(big.Int).SetBytes(ifReveal)) < 0
}
func (b *lastRevealerAbortR2Behavior) Name() string { return "last-revealer-abort-r2" }

// Reveal2: no envía reveal2 en su turno → timeout → siguiente peer avanza.
type noReveal2Behavior struct{ honestBehavior }

func (b *noReveal2Behavior) ShouldSkipReveal2() bool { return true }
func (b *noReveal2Behavior) Name() string            { return "no-reveal2" }

// Reveal2: envía s_i' aleatorio con H(s_i') ≠ r_i → verificación falla → fallback activado.
type reveal2InvalidBehavior struct{ honestBehavior }

func (b *reveal2InvalidBehavior) ModifyReveal2(_ []byte) []byte { return randomBytes(32) }
func (b *reveal2InvalidBehavior) Name() string                  { return "reveal2-invalid" }

// Reveal2: ventajista temporal del último revelador. Siendo el último, conoce el input
// completo de la VDF antes que los nodos honestos: inicia la VDF de inmediato y demora su
// propio reveal2 para maximizar su ventana exclusiva de precómputo (la ventaja temporal Δ).
// El caller (publishReveal2) sólo lo activa cuando el nodo es efectivamente el último.
type lastRevealerVDFBehavior struct{ honestBehavior }

func (b *lastRevealerVDFBehavior) IsLastRevealerVDF() bool { return true }
func (b *lastRevealerVDFBehavior) Name() string           { return "last-revealer-vdf" }

// Timeout: emite TIMEOUT_VOTE contra nodos que sí respondieron → fuerza diputas.
// Requiere mayoría honesta para ser neutralizado por el mecanismo de disputa.
type falseTimeoutVoteBehavior struct{ honestBehavior }

func (b *falseTimeoutVoteBehavior) ShouldVoteFalseTimeout() bool { return true }
func (b *falseTimeoutVoteBehavior) Name() string                 { return "false-timeout-vote" }

// NewAttacker construye el perfil de atacante según el string del flag -attacker.
// Retorna error si el perfil es desconocido.
func NewAttacker(profile string) (AttackerBehavior, error) {
	switch profile {
	case "", "honest":
		return &honestBehavior{}, nil
	case "no-ready-ack":
		return &noReadyAckBehavior{}, nil
	case "commit-invalid":
		return &commitInvalidBehavior{}, nil
	case "no-commit":
		return &noCommitBehavior{}, nil
	case "equivocate-commit":
		return &equivocateCommitBehavior{}, nil
	case "no-reveal1":
		return &noReveal1Behavior{}, nil
	case "reveal1-invalid":
		return &reveal1InvalidBehavior{}, nil
	case "last-revealer-abort":
		return &lastRevealerAbortBehavior{}, nil
	case "last-revealer-abort-r2":
		return &lastRevealerAbortR2Behavior{}, nil
	case "no-reveal2":
		return &noReveal2Behavior{}, nil
	case "reveal2-invalid":
		return &reveal2InvalidBehavior{}, nil
	case "last-revealer-vdf":
		return &lastRevealerVDFBehavior{}, nil
	case "false-timeout-vote":
		return &falseTimeoutVoteBehavior{}, nil
	default:
		return nil, fmt.Errorf(
			"perfil de atacante desconocido: %q\nOpciones: honest, no-ready-ack, commit-invalid, no-commit, equivocate-commit, no-reveal1, reveal1-invalid, last-revealer-abort, last-revealer-abort-r2, no-reveal2, reveal2-invalid, last-revealer-vdf, false-timeout-vote",
			profile,
		)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}
