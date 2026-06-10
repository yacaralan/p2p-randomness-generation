package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sort"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

const valueSize = 32

// DoubleCommitReveal implementa el protocolo Commit-Reveal² del paper
// "Commit-Reveal²: Randomized Reveal Order".
//
// Cada nodo genera s_i, publica c_i = H(H(s_i)).
// En reveal1 publica r_i = H(s_i). Una vez recibidos todos los r_i,
// calcula el orden de reveal2 basado en d_i = H(|Ω_v − c_i|).
// En reveal2 publica s_i en el orden calculado.
type DoubleCommitReveal struct {
	mu   sync.Mutex
	self peer.ID

	mySecret  []byte // s_i
	myReveal1 []byte // r_i = H(s_i)
	myCommit2 []byte // c_i = H(r_i)

	commit2s     map[peer.ID][]byte // c_j recibidos (incluye self tras commit2)
	reveal1s     map[peer.ID][]byte // r_j verificados (incluye self tras reveal1)
	revealOrder  []peer.ID          // orden calculado para reveal2
	revealDist   map[peer.ID][]byte // d_i calculados, para mostrar en /order
	reveal2s     map[peer.ID][]byte // s_j verificados
	reveal2Sent  bool               // evita publicar reveal2 más de una vez
}

// NewDoubleCommitReveal crea el struct vacío.
func NewDoubleCommitReveal(self peer.ID) *DoubleCommitReveal {
	return &DoubleCommitReveal{
		self:       self,
		commit2s:   make(map[peer.ID][]byte),
		reveal1s:   make(map[peer.ID][]byte),
		revealDist: make(map[peer.ID][]byte),
		reveal2s:   make(map[peer.ID][]byte),
	}
}

// StartCommit2 genera s_i, calcula r_i = H(s_i) y c_i = H(r_i).
// Devuelve c_i para que el caller lo publique.
// También registra c_i en commit2s[self] para incluirlo en el cómputo de orden.
func (d *DoubleCommitReveal) StartCommit2() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	secret := make([]byte, valueSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generar secreto: %w", err)
	}

	r := sha256hash(secret)
	c := sha256hash(r)

	d.mySecret = secret
	d.myReveal1 = r
	d.myCommit2 = c
	d.commit2s[d.self] = clone(c)

	return c, nil
}

// HandleCommit2 registra el segundo commit recibido de un peer.
// Devuelve (accepted=true) si es la primera vez; (equivocated=true) si ya había
// un valor distinto almacenado para este peer.
func (d *DoubleCommitReveal) HandleCommit2(from peer.ID, hash []byte) (accepted, equivocated bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.commit2s[from]; ok {
		return false, string(existing) != string(hash)
	}
	d.commit2s[from] = clone(hash)
	return true, false
}

// StartReveal1 devuelve r_i para que el caller lo publique.
// También registra r_i en reveal1s[self] y retorna si el orden ya puede calcularse.
func (d *DoubleCommitReveal) StartReveal1() (r []byte, allReady bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.myReveal1 == nil {
		return nil, false, fmt.Errorf("no hay commit2 propio: ejecutá /commit2 primero")
	}
	d.reveal1s[d.self] = clone(d.myReveal1)
	allReady = len(d.reveal1s) == len(d.commit2s) && len(d.commit2s) > 0
	return clone(d.myReveal1), allReady, nil
}

// HandleReveal1 verifica que H(r_j) == c_j y guarda r_j.
// Devuelve (valid, allReady, equivocated): valid indica si el reveal fue correcto,
// allReady si ya se tienen todos los reveal1 esperados, equivocated si ya había
// un reveal1 distinto almacenado para este peer.
func (d *DoubleCommitReveal) HandleReveal1(from peer.ID, hash []byte) (valid, allReady, equivocated bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, ok := d.reveal1s[from]; ok {
		return false, false, string(existing) != string(hash)
	}
	expected, ok := d.commit2s[from]
	if !ok {
		return false, false, false
	}
	if string(sha256hash(hash)) != string(expected) {
		return false, false, false
	}
	d.reveal1s[from] = clone(hash)
	allReady = len(d.reveal1s) == len(d.commit2s)
	return true, allReady, false
}

// ComputeRevealOrder calcula el orden de reveal2 y lo almacena internamente.
// Debe llamarse sólo cuando todos los reveal1 ya fueron recibidos.
//
// Algoritmo:
//  1. Ordenar todos los r_i de mayor a menor (big-endian integer).
//  2. Ω_v = SHA256(r_sorted_1 || r_sorted_2 || ... || r_sorted_n).
//  3. d_i = SHA256(|Ω_v_int − c_i_int|.Bytes()) para cada peer.
//  4. Ordenar peers por d_i descendente.
func (d *DoubleCommitReveal) ComputeRevealOrder() []peer.ID {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Paso 1: recolectar y ordenar r_i de mayor a menor.
	type entry struct {
		id peer.ID
		r  []byte
	}
	entries := make([]entry, 0, len(d.reveal1s))
	for id, r := range d.reveal1s {
		entries = append(entries, entry{id, r})
	}
	sort.Slice(entries, func(i, j int) bool {
		bi := new(big.Int).SetBytes(entries[i].r)
		bj := new(big.Int).SetBytes(entries[j].r)
		return bi.Cmp(bj) > 0 // descendente
	})

	// Paso 2: calcular Ω_v.
	h := sha256.New()
	for _, e := range entries {
		h.Write(e.r)
	}
	omega := h.Sum(nil)
	omegaInt := new(big.Int).SetBytes(omega)

	// Paso 3: calcular d_i para cada peer.
	type peerDist struct {
		id   peer.ID
		dist []byte
	}
	dists := make([]peerDist, 0, len(d.commit2s))
	for id, c := range d.commit2s {
		cInt := new(big.Int).SetBytes(c)
		diff := new(big.Int).Abs(new(big.Int).Sub(omegaInt, cInt))
		di := sha256hash(diff.Bytes())
		d.revealDist[id] = di
		dists = append(dists, peerDist{id, di})
	}

	// Paso 4: ordenar por d_i descendente.
	sort.Slice(dists, func(i, j int) bool {
		bi := new(big.Int).SetBytes(dists[i].dist)
		bj := new(big.Int).SetBytes(dists[j].dist)
		return bi.Cmp(bj) > 0
	})

	order := make([]peer.ID, len(dists))
	for i, pd := range dists {
		order[i] = pd.id
	}
	d.revealOrder = order
	return order
}

// RevealOrder devuelve el orden calculado (nil si aún no se calculó).
func (d *DoubleCommitReveal) RevealOrder() []peer.ID {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.revealOrder == nil {
		return nil
	}
	out := make([]peer.ID, len(d.revealOrder))
	copy(out, d.revealOrder)
	return out
}

// RevealDist devuelve una copia del mapa de distancias d_i calculadas.
func (d *DoubleCommitReveal) RevealDist() map[peer.ID][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[peer.ID][]byte, len(d.revealDist))
	for id, di := range d.revealDist {
		out[id] = clone(di)
	}
	return out
}

// IsFirstReveal2 devuelve true si el nodo local es el primero en el orden de
// reveal2 y aún no publicó su reveal2. Marca internamente que ya fue llamado
// para evitar publicaciones duplicadas.
func (d *DoubleCommitReveal) IsFirstReveal2() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reveal2Sent || len(d.revealOrder) == 0 {
		return false
	}
	if d.revealOrder[0] != d.self {
		return false
	}
	d.reveal2Sent = true
	return true
}

// MyTurnAfter devuelve true si el nodo local debe revelar inmediatamente
// después de que `from` acaba de publicar su reveal2 (es decir, `from` ocupa
// la posición anterior a la del nodo local en el orden). También verifica que
// el nodo aún no haya publicado su reveal2.
func (d *DoubleCommitReveal) MyTurnAfter(from peer.ID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reveal2Sent || len(d.revealOrder) == 0 {
		return false
	}
	myIdx, fromIdx := -1, -1
	for i, p := range d.revealOrder {
		if p == d.self {
			myIdx = i
		}
		if p == from {
			fromIdx = i
		}
	}
	if myIdx <= 0 || fromIdx != myIdx-1 {
		return false
	}
	d.reveal2Sent = true
	return true
}

// StartReveal2 devuelve s_i para que el caller lo publique.
// También registra s_i en reveal2s[self] para que FinalInput pueda incluirlo.
func (d *DoubleCommitReveal) StartReveal2() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mySecret == nil {
		return nil, fmt.Errorf("no hay commit2 propio: ejecutá /commit2 primero")
	}
	d.reveal2s[d.self] = clone(d.mySecret)
	return clone(d.mySecret), nil
}

// FinalInput devuelve la concatenación de los secretos reveal2 en el orden
// definido por revealOrder (la función de distancia). Es el input de la VDF.
// Retorna (nil, false) si aún no se recibieron todos los reveal2.
func (d *DoubleCommitReveal) FinalInput() ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.revealOrder) == 0 || len(d.reveal2s) != len(d.revealOrder) {
		return nil, false
	}
	buf := make([]byte, 0, len(d.revealOrder)*valueSize)
	for _, id := range d.revealOrder {
		s, ok := d.reveal2s[id]
		if !ok {
			return nil, false
		}
		buf = append(buf, s...)
	}
	return buf, true
}

// HandleReveal2 verifica que H(s_j) == r_j y guarda s_j.
// Devuelve (accepted, equivocated): accepted indica si fue guardado correctamente,
// equivocated si ya había un secreto distinto almacenado para este peer.
func (d *DoubleCommitReveal) HandleReveal2(from peer.ID, secret []byte) (accepted, equivocated bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, ok := d.reveal2s[from]; ok {
		return false, string(existing) != string(secret)
	}
	expected, ok := d.reveal1s[from]
	if !ok {
		return false, false
	}
	if string(sha256hash(secret)) != string(expected) {
		return false, false
	}
	d.reveal2s[from] = clone(secret)
	return true, false
}

// SelfID expone el peer.ID propio (útil para el caller al mostrar /order).
func (d *DoubleCommitReveal) SelfID() peer.ID {
	return d.self
}

// CommitPeers devuelve el conjunto de peers que enviaron commit2.
func (d *DoubleCommitReveal) CommitPeers() map[peer.ID]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	set := make(map[peer.ID]bool, len(d.commit2s))
	for id := range d.commit2s {
		set[id] = true
	}
	return set
}

// Reveal1Peers devuelve el conjunto de peers con reveal1 verificado.
func (d *DoubleCommitReveal) Reveal1Peers() map[peer.ID]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	set := make(map[peer.ID]bool, len(d.reveal1s))
	for id := range d.reveal1s {
		set[id] = true
	}
	return set
}

// GetPhaseValue devuelve el valor que tiene un peer en la fase indicada
// ("commit2", "reveal1", "reveal2"). Devuelve (nil, false) si no existe.
func (d *DoubleCommitReveal) GetPhaseValue(phase string, id peer.ID) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var m map[peer.ID][]byte
	switch phase {
	case "commit2":
		m = d.commit2s
	case "reveal1":
		m = d.reveal1s
	case "reveal2":
		m = d.reveal2s
	default:
		return nil, false
	}
	v, ok := m[id]
	if !ok {
		return nil, false
	}
	return clone(v), true
}

// Commit2Count devuelve cuántos commit2 se han recibido (incluido el propio).
func (d *DoubleCommitReveal) Commit2Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.commit2s)
}

// Reveal1Count devuelve cuántos reveal1 verificados se han recibido (incluido el propio).
func (d *DoubleCommitReveal) Reveal1Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reveal1s)
}

// Reveal2CountExcluding devuelve cuántos reveal2 se han recibido excluyendo los peers del set dado.
func (d *DoubleCommitReveal) Reveal2CountExcluding(excluded map[peer.ID]bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for id := range d.reveal2s {
		if !excluded[id] {
			count++
		}
	}
	return count
}

// ComputeRevealOrderExcluding calcula el orden de reveal2 ignorando los peers en excluded.
func (d *DoubleCommitReveal) ComputeRevealOrderExcluding(excluded map[peer.ID]bool) []peer.ID {
	d.mu.Lock()
	defer d.mu.Unlock()

	type entry struct {
		id peer.ID
		r  []byte
	}
	entries := make([]entry, 0, len(d.reveal1s))
	for id, r := range d.reveal1s {
		if !excluded[id] {
			entries = append(entries, entry{id, r})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		bi := new(big.Int).SetBytes(entries[i].r)
		bj := new(big.Int).SetBytes(entries[j].r)
		return bi.Cmp(bj) > 0
	})

	h := sha256.New()
	for _, e := range entries {
		h.Write(e.r)
	}
	omega := h.Sum(nil)
	omegaInt := new(big.Int).SetBytes(omega)

	type peerDist struct {
		id   peer.ID
		dist []byte
	}
	dists := make([]peerDist, 0, len(d.commit2s))
	for id, c := range d.commit2s {
		if excluded[id] {
			continue
		}
		cInt := new(big.Int).SetBytes(c)
		diff := new(big.Int).Abs(new(big.Int).Sub(omegaInt, cInt))
		di := sha256hash(diff.Bytes())
		d.revealDist[id] = di
		dists = append(dists, peerDist{id, di})
	}

	sort.Slice(dists, func(i, j int) bool {
		bi := new(big.Int).SetBytes(dists[i].dist)
		bj := new(big.Int).SetBytes(dists[j].dist)
		return bi.Cmp(bj) > 0
	})

	order := make([]peer.ID, len(dists))
	for i, pd := range dists {
		order[i] = pd.id
	}
	d.revealOrder = order
	return order
}

// FinalInputWithFallback devuelve el input a la VDF usando reveal1 como fallback
// para los peers en useReveal1 (abortados en reveal2).
func (d *DoubleCommitReveal) FinalInputWithFallback(useReveal1 map[peer.ID]bool) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.revealOrder) == 0 {
		return nil, false
	}
	buf := make([]byte, 0, len(d.revealOrder)*valueSize)
	for _, id := range d.revealOrder {
		if useReveal1[id] {
			r, ok := d.reveal1s[id]
			if !ok {
				return nil, false
			}
			buf = append(buf, r...)
		} else {
			s, ok := d.reveal2s[id]
			if !ok {
				return nil, false
			}
			buf = append(buf, s...)
		}
	}
	return buf, true
}

// HypotheticalRevealOrderWithSelf calcula el orden de reveal2 que resultaría si el
// nodo local publicara su reveal1 ahora, SIN mutar el estado interno (no escribe
// revealOrder ni revealDist). Incluye d.myReveal1 para self aunque StartReveal1 no
// se haya llamado todavía. Excluye los peers en excluded.
// Retorna (nil, false) si no hay reveal1 propio computado aún.
//
// La usa el atacante "last-revealer-abort" para decidir estratégicamente si publicar
// su reveal1: sólo lo hace si el orden lo dejaría como último en reveal2.
func (d *DoubleCommitReveal) HypotheticalRevealOrderWithSelf(excluded map[peer.ID]bool) ([]peer.ID, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.myReveal1 == nil {
		return nil, false
	}

	type entry struct {
		id peer.ID
		r  []byte
	}
	entries := make([]entry, 0, len(d.reveal1s)+1)
	for id, r := range d.reveal1s {
		if !excluded[id] {
			entries = append(entries, entry{id, r})
		}
	}
	if _, ok := d.reveal1s[d.self]; !ok && !excluded[d.self] {
		entries = append(entries, entry{d.self, d.myReveal1})
	}
	sort.Slice(entries, func(i, j int) bool {
		bi := new(big.Int).SetBytes(entries[i].r)
		bj := new(big.Int).SetBytes(entries[j].r)
		return bi.Cmp(bj) > 0
	})

	h := sha256.New()
	for _, e := range entries {
		h.Write(e.r)
	}
	omega := h.Sum(nil)
	omegaInt := new(big.Int).SetBytes(omega)

	type peerDist struct {
		id   peer.ID
		dist []byte
	}
	dists := make([]peerDist, 0, len(d.commit2s))
	for id, c := range d.commit2s {
		if excluded[id] {
			continue
		}
		cInt := new(big.Int).SetBytes(c)
		diff := new(big.Int).Abs(new(big.Int).Sub(omegaInt, cInt))
		di := sha256hash(diff.Bytes())
		dists = append(dists, peerDist{id, di})
	}

	sort.Slice(dists, func(i, j int) bool {
		bi := new(big.Int).SetBytes(dists[i].dist)
		bj := new(big.Int).SetBytes(dists[j].dist)
		return bi.Cmp(bj) > 0
	})

	order := make([]peer.ID, len(dists))
	for i, pd := range dists {
		order[i] = pd.id
	}
	return order, true
}

// ComputeBothVDFInputs calcula los dos posibles inputs de la VDF que vería un atacante
// que es el último en revelar en reveal2: ifReveal usa su propio s_i, mientras que
// ifAbort usa su r_i como fallback (el comportamiento que aplican los nodos honestos
// cuando un peer aborta en reveal2). Para los demás peers usa s_j (o r_j si están en
// abortedR2). NO muta estado.
//
// Retorna ok=false si falta algún reveal2 ajeno (el nodo aún no es el último) o si no
// hay secreto/reveal1 propio. La usa el atacante "last-revealer-abort-r2" para elegir
// la acción que produzca el input numéricamente menor.
func (d *DoubleCommitReveal) ComputeBothVDFInputs(abortedR2 map[peer.ID]bool) (ifReveal, ifAbort []byte, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.revealOrder) == 0 || d.mySecret == nil || d.myReveal1 == nil {
		return nil, nil, false
	}

	rev := make([]byte, 0, len(d.revealOrder)*valueSize)
	abo := make([]byte, 0, len(d.revealOrder)*valueSize)
	for _, id := range d.revealOrder {
		switch {
		case id == d.self:
			rev = append(rev, d.mySecret...)
			abo = append(abo, d.myReveal1...)
		case abortedR2[id]:
			r, okR := d.reveal1s[id]
			if !okR {
				return nil, nil, false
			}
			rev = append(rev, r...)
			abo = append(abo, r...)
		default:
			s, okS := d.reveal2s[id]
			if !okS {
				return nil, nil, false
			}
			rev = append(rev, s...)
			abo = append(abo, s...)
		}
	}
	return rev, abo, true
}

// Reset limpia todo el estado del protocolo para permitir una nueva ronda.
func (d *DoubleCommitReveal) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mySecret = nil
	d.myReveal1 = nil
	d.myCommit2 = nil
	d.commit2s = make(map[peer.ID][]byte)
	d.reveal1s = make(map[peer.ID][]byte)
	d.revealOrder = nil
	d.revealDist = make(map[peer.ID][]byte)
	d.reveal2s = make(map[peer.ID][]byte)
	d.reveal2Sent = false
}

// PeerValues agrupa los valores de un peer en cada etapa del protocolo.
// Un campo nil indica que esa etapa aún no fue completada/verificada.
type PeerValues struct {
	Commit2 []byte // c_i = H(H(s_i))
	Reveal1 []byte // r_i = H(s_i)
	Reveal2 []byte // s_i
}

// AllValues devuelve un snapshot de los valores de todos los participantes,
// incluido el nodo local (identificado por SelfID()).
func (d *DoubleCommitReveal) AllValues() map[peer.ID]PeerValues {
	d.mu.Lock()
	defer d.mu.Unlock()

	all := make(map[peer.ID]PeerValues)

	// Unión de todos los peer.ID conocidos en cualquier etapa.
	seen := make(map[peer.ID]struct{})
	for id := range d.commit2s {
		seen[id] = struct{}{}
	}
	for id := range d.reveal1s {
		seen[id] = struct{}{}
	}
	for id := range d.reveal2s {
		seen[id] = struct{}{}
	}

	for id := range seen {
		var pv PeerValues
		if id == d.self {
			pv.Commit2 = clone(d.myCommit2)
			pv.Reveal1 = clone(d.myReveal1)
			pv.Reveal2 = clone(d.mySecret)
		} else {
			if v, ok := d.commit2s[id]; ok {
				pv.Commit2 = clone(v)
			}
			if v, ok := d.reveal1s[id]; ok {
				pv.Reveal1 = clone(v)
			}
			if v, ok := d.reveal2s[id]; ok {
				pv.Reveal2 = clone(v)
			}
		}
		all[id] = pv
	}
	return all
}

func sha256hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func clone(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}
