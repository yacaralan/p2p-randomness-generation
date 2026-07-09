package main

import (
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ayacar/p2p-randomness-generation/protocol"
)

// abortCollector recibe eventos de aborto de cualquier nodo y los indexa por el
// ShortString() del peer abortado. Los abortos aparecen en los logs de otros nodos,
// no del nodo que fue abortado.
type abortEntry struct {
	phase  string // "commit2"|"reveal1"|"reveal2"|...
	reason string // "timeout"|"equivocacion"
}

type abortCollector struct {
	mu     sync.Mutex
	aborts map[string]abortEntry // shortID → {phase, reason}
}

func newAbortCollector() *abortCollector {
	return &abortCollector{aborts: make(map[string]abortEntry)}
}

func (ac *abortCollector) record(short, phase, reason string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if _, already := ac.aborts[short]; !already {
		ac.aborts[short] = abortEntry{phase: phase, reason: reason}
	}
}

func (ac *abortCollector) get(short string) (phase, reason string, aborted bool) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	e, aborted := ac.aborts[short]
	return e.phase, e.reason, aborted
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseAbort detecta líneas de aborto por timeout o equivocación y extrae el
// ShortString del peer abortado y la fase. Las líneas tienen códigos ANSI.
//
// Formatos:
//
//	[timeout] <ANSI><peer.ID 12*XXXXXX><ANSI> abortado en fase <fase> (mayoría)<ANSI>
//	[equivocación] <ANSI>PRUEBA VERIFICADA<ANSI>: <peer.ID 12*XXXXXX> equivocó en fase <fase> — abortando
func parseAbort(line string) (short, phase, reason string, ok bool) {
	clean := ansiRe.ReplaceAllString(line, "")

	// Timeout: "[timeout] <peer.ID 12*XXXXXX> abortado en fase <fase> (mayoría)"
	if strings.Contains(clean, "abortado en fase") {
		short, phase, ok = extractShortAndPhase(clean, "abortado en fase", " (mayoría)")
		return short, phase, "timeout", ok
	}
	// Equivocación: "PRUEBA VERIFICADA: <peer.ID 12*XXXXXX> equivocó en fase <fase> — abortando"
	if strings.Contains(clean, "equivocó en fase") {
		short, phase, ok = extractShortAndPhase(clean, "equivocó en fase", " — abortando")
		return short, phase, "equivocacion", ok
	}
	return "", "", "", false
}

// extractShortAndPhase encuentra "<peer.ID ...>" justo antes de verbMarker y la
// fase entre verbMarker y endMarker.
func extractShortAndPhase(clean, verbMarker, endMarker string) (short, phase string, ok bool) {
	verbIdx := strings.Index(clean, verbMarker)
	if verbIdx < 0 {
		return "", "", false
	}
	before := clean[:verbIdx]
	endID := strings.LastIndex(before, ">")
	if endID < 0 {
		return "", "", false
	}
	startID := strings.LastIndex(before[:endID], "<peer.ID ")
	if startID < 0 {
		return "", "", false
	}
	short = before[startID : endID+1] // "<peer.ID 12*XXXXXX>"

	after := clean[verbIdx+len(verbMarker):]
	endIdx := strings.Index(after, endMarker)
	if endIdx < 0 {
		phase = strings.TrimSpace(after)
	} else {
		phase = strings.TrimSpace(after[:endIdx])
	}
	return short, phase, true
}

// parsePeerID extrae el PeerID de "[node] PeerID: <id>".
func parsePeerID(line string) (string, bool) {
	const marker = "[node] PeerID: "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(line[idx+len(marker):]), true
}

// parseVDFInput extrae el hex de "[vdf] iniciando cómputo (T=...). input=<hex>".
func parseVDFInput(line string) (string, bool) {
	const marker = "input="
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(line[idx+len(marker):]), true
}

// parseVDFProof extrae el hex de "[vdf] proof=<hex>".
func parseVDFProof(line string) (string, bool) {
	const marker = "[vdf] proof="
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(line[idx+len(marker):]), true
}

// parseVDFEpochMs extrae el epoch ms de "[vdf] output obtenido: <ms> ms".
func parseVDFEpochMs(line string) (int64, bool) {
	const marker = "[vdf] output obtenido: "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return 0, false
	}
	rest := line[idx+len(marker):]
	end := strings.Index(rest, " ms")
	if end < 0 {
		return 0, false
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		return 0, false
	}
	return ms, true
}

// shortOf replica peer.ID.ShortString() a partir del PeerID en string, para
// mapear abortos (que usan ShortString) al nodo que los sufrió.
func shortOf(peerID string) string {
	if len(peerID) <= 10 {
		return fmt.Sprintf("<peer.ID %s>", peerID)
	}
	return fmt.Sprintf("<peer.ID %s*%s>", peerID[:2], peerID[len(peerID)-6:])
}

// writeResults escribe results.csv y summary.json en expDir.
func writeResults(nodes []*runningNode, ac *abortCollector, cfg Config, expDir string, elapsed time.Duration) {
	runID := filepath.Base(expDir)

	// --- Calcular consistencia ---
	var honestOutputs []string
	for _, rn := range nodes {
		rn.mu.Lock()
		out := rn.vdfOutput
		rn.mu.Unlock()
		if rn.spec.honest {
			honestOutputs = append(honestOutputs, out)
		}
	}
	var consistency string
	switch {
	case allEqualNonEmpty(honestOutputs):
		consistency = "ok"
	case anyEmpty(honestOutputs):
		consistency = "n_a"
	default:
		consistency = "failed"
	}
	protocolSuccess := consistency == "ok"

	// El output correcto del protocolo es el valor común de los honestos. Solo está
	// definido si los honestos coincidieron; sirve de referencia para el timing y para
	// distinguir a los atacantes que forkearon.
	correctOutput := ""
	if protocolSuccess {
		correctOutput = honestOutputs[0]
	}

	// --- results.csv ---
	csvPath := filepath.Join(expDir, "results.csv")
	csvFile, err := os.Create(csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[results] error creando %s: %v\n", csvPath, err)
		return
	}
	defer csvFile.Close()

	w := csv.NewWriter(csvFile)
	_ = w.Write([]string{
		"run_id", "label", "profile", "proposer", "participated", "vdf_capacity",
		"peer_id", "vdf_valid", "vdf_timestamp", "vdf_epoch_ms",
		"aborted", "abort_phase", "abort_reason", "protocol_success",
		"vdf_output", "vdf_input", "vdf_proof",
	})

	abortedCount := 0
	completedCount := 0    // nodos que emitieron output
	allVDFValid := true    // ¿toda VDF emitida reverifica con VerifyVDF?
	for _, rn := range nodes {
		rn.mu.Lock()
		peerID := rn.peerID
		vdfOut := rn.vdfOutput
		vdfIn := rn.vdfInput
		vdfProof := rn.vdfProof
		vdfTS := rn.vdfTimestamp
		vdfEpoch := rn.vdfEpochMs
		participated := rn.participated
		rn.mu.Unlock()

		abortPhase, abortReason, wasAborted := ac.get(shortOf(peerID))
		if wasAborted {
			abortedCount++
		}

		// Reverificación independiente de la VDF: el launcher reverifica con la prueba
		// publicada, sin confiar en el "verificación inline" del propio nodo.
		vdfValid := false
		if vdfOut != "" && vdfIn != "" && vdfProof != "" {
			completedCount++
			if inB, e1 := hex.DecodeString(vdfIn); e1 == nil {
				if outB, e2 := hex.DecodeString(vdfOut); e2 == nil {
					if pfB, e3 := hex.DecodeString(vdfProof); e3 == nil {
						vdfValid = protocol.VerifyVDF(inB, cfg.Protocol.VDFT, outB, pfB)
					}
				}
			}
			if !vdfValid {
				allVDFValid = false
			}
		}

		_ = w.Write([]string{
			runID,
			rn.spec.label,
			rn.spec.profile,
			strconv.FormatBool(rn.spec.proposer),
			strconv.FormatBool(participated),
			strconv.FormatFloat(rn.spec.capacity, 'g', -1, 64),
			peerID,
			strconv.FormatBool(vdfValid),
			vdfTS,
			epochMsStr(vdfEpoch),
			strconv.FormatBool(wasAborted),
			abortPhase,
			abortReason,
			strconv.FormatBool(protocolSuccess),
			vdfOut,
			vdfIn,
			vdfProof,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "[results] error escribiendo CSV: %v\n", err)
		return
	}

	// --- summary.json ---
	type attackerEntry struct {
		Profile string `json:"profile"`
		Count   int    `json:"count"`
	}
	type configSummary struct {
		VDFT           int     `json:"vdf_t"`
		TimeoutReadyS  float64 `json:"timeout_ready_s"`
		TimeoutCommitS float64 `json:"timeout_commit_s"`
		TimeoutReveal1S float64 `json:"timeout_reveal1_s"`
		TimeoutReveal2S float64 `json:"timeout_reveal2_s"`
		DiscoveryDelayS float64 `json:"discovery_delay_s"`
		MaxRuntimeS    float64 `json:"max_runtime_s"`
	}
	type nodesSummary struct {
		Honest    int             `json:"honest"`
		Attackers []attackerEntry `json:"attackers"`
		Total     int             `json:"total"`
	}

	attackers := make([]attackerEntry, 0, len(cfg.Nodes.Attackers))
	for _, a := range cfg.Nodes.Attackers {
		attackers = append(attackers, attackerEntry{Profile: a.Profile, Count: a.Count})
	}
	totalNodes := len(nodes)

	honestCompleted := 0
	for _, rn := range nodes {
		if rn.spec.honest {
			rn.mu.Lock()
			if rn.vdfOutput != "" {
				honestCompleted++
			}
			rn.mu.Unlock()
		}
	}

	// Equidad temporal: el más rápido, el más lento y el delta se calculan sobre los
	// nodos que llegaron al output correcto (honestos + atacantes que coincidieron),
	// no sobre los que forkearon. Si los honestos no coincidieron no hay referencia.
	type timingEntry struct {
		Label    string  `json:"label"`
		Profile  string  `json:"profile"`
		Capacity float64 `json:"capacity"`
		EpochMs  int64   `json:"epoch_ms"`
	}
	type timingSummary struct {
		Fastest *timingEntry `json:"fastest"`
		Slowest *timingEntry `json:"slowest"`
		DeltaMs *int64       `json:"delta_ms"`
	}
	var timing timingSummary
	var fastest, slowest *timingEntry
	if correctOutput != "" {
		for _, rn := range nodes {
			rn.mu.Lock()
			ms := rn.vdfEpochMs
			out := rn.vdfOutput
			rn.mu.Unlock()
			if ms == 0 || out != correctOutput {
				continue
			}
			e := &timingEntry{Label: rn.spec.label, Profile: rn.spec.profile, Capacity: rn.spec.capacity, EpochMs: ms}
			if fastest == nil || ms < fastest.EpochMs {
				fastest = e
			}
			if slowest == nil || ms > slowest.EpochMs {
				slowest = e
			}
		}
	}
	if fastest != nil && slowest != nil {
		delta := slowest.EpochMs - fastest.EpochMs
		timing = timingSummary{Fastest: fastest, Slowest: slowest, DeltaMs: &delta}
	}

	// --- Invariantes del protocolo ---
	// honest_agreement: los honestos que participaron en el protocolo y completaron
	// la VDF coinciden en el output. Se usa `participated` (emitieron commit2) en lugar
	// del conteo de lanzados, para considerar los que realmente formaron parte del protocolo.
	var honestDone []string
	honestParticipants := 0  // honestos que llegaron a commit2
	sessionParticipants := 0 // todos los que llegaron a commit2 (honestos + atacantes)
	for _, rn := range nodes {
		rn.mu.Lock()
		part := rn.participated
		out := rn.vdfOutput
		rn.mu.Unlock()
		if !part {
			continue
		}
		sessionParticipants++
		if !rn.spec.honest {
			continue
		}
		honestParticipants++
		if out != "" {
			honestDone = append(honestDone, out)
		}
	}
	honestAgreement := "n_a"
	if len(honestDone) > 0 {
		honestAgreement = "pass"
		for _, o := range honestDone {
			if o != honestDone[0] {
				honestAgreement = "fail"
				break
			}
		}
	}
	// liveness: con mayoría honesta entre los participantes de la sesión, el protocolo
	// debe terminar exitosamente. Usa participantes reales, no nodos lanzados.
	liveness := "n_a"
	if sessionParticipants > 0 && honestParticipants*2 > sessionParticipants {
		if protocolSuccess {
			liveness = "pass"
		} else {
			liveness = "fail"
		}
	}
	// vdf_validity: toda VDF emitida reverifica con la prueba publicada.
	vdfValidity := "n_a"
	if completedCount > 0 {
		if allVDFValid {
			vdfValidity = "pass"
		} else {
			vdfValidity = "fail"
		}
	}
	invariants := struct {
		HonestAgreement string `json:"honest_agreement"`
		Liveness        string `json:"liveness"`
		VDFValidity     string `json:"vdf_validity"`
	}{honestAgreement, liveness, vdfValidity}

	summary := struct {
		RunID           string        `json:"run_id"`
		Timestamp       string        `json:"timestamp"`
		ProtocolSuccess bool          `json:"protocol_success"`
		Consistency     string        `json:"consistency"`
		DurationMs      int64         `json:"duration_ms"`
		Config          configSummary `json:"config"`
		Nodes           nodesSummary  `json:"nodes"`
		HonestCompleted int           `json:"honest_completed"`
		AbortedCount    int           `json:"aborted_count"`
		Timing          timingSummary `json:"timing"`
		Invariants      any           `json:"invariants"`
	}{
		RunID:           runID,
		Timestamp:       time.Now().Format("2006-01-02T15:04:05"),
		ProtocolSuccess: protocolSuccess,
		Consistency:     consistency,
		DurationMs:      elapsed.Milliseconds(),
		Config: configSummary{
			VDFT:            cfg.Protocol.VDFT,
			TimeoutReadyS:   cfg.Protocol.TimeoutReady.Seconds(),
			TimeoutCommitS:  cfg.Protocol.TimeoutCommit.Seconds(),
			TimeoutReveal1S: cfg.Protocol.TimeoutReveal1.Seconds(),
			TimeoutReveal2S: cfg.Protocol.TimeoutReveal2.Seconds(),
			DiscoveryDelayS: cfg.DiscoveryDelay.Seconds(),
			MaxRuntimeS:     cfg.MaxRuntime.Seconds(),
		},
		Nodes: nodesSummary{
			Honest:    cfg.Nodes.Honest.Count,
			Attackers: attackers,
			Total:     totalNodes,
		},
		HonestCompleted: honestCompleted,
		AbortedCount:    abortedCount,
		Timing:          timing,
		Invariants:      invariants,
	}

	jsonPath := filepath.Join(expDir, "summary.json")
	jsonFile, err := os.Create(jsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[results] error creando %s: %v\n", jsonPath, err)
		return
	}
	defer jsonFile.Close()

	enc := json.NewEncoder(jsonFile)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summary); err != nil {
		fmt.Fprintf(os.Stderr, "[results] error escribiendo JSON: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Println("=========== INVARIANTES ===========")
	fmt.Printf("  %-20s %s\n", "acuerdo honestos", invMark(honestAgreement))
	fmt.Printf("  %-20s %s\n", "liveness", invMark(liveness))
	fmt.Printf("  %-20s %s\n", "validez VDF", invMark(vdfValidity))
	if timing.DeltaMs != nil {
		fmt.Printf("  equidad temporal: delta=%d ms (rápido=%s, lento=%s)\n",
			*timing.DeltaMs, timing.Fastest.Label, timing.Slowest.Label)
	} else {
		fmt.Println("  equidad temporal: N/A (sin output correcto de referencia)")
	}
	fmt.Println("===================================")

	fmt.Printf("[launcher] resultados en: %s/{results.csv,summary.json}\n", expDir)
}

// invMark formatea el estado de un invariante para la consola.
func invMark(state string) string {
	switch state {
	case "pass":
		return "PASS"
	case "fail":
		return "FAIL"
	default:
		return "N/A"
	}
}

func epochMsStr(ms int64) string {
	if ms == 0 {
		return ""
	}
	return strconv.FormatInt(ms, 10)
}
