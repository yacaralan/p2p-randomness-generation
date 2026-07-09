// Modo sweep: calibración del par (TO, T). Para cada timeout de reveal2 (TO) busca,
// mediante búsqueda binaria + confirmación, el T de VDF más chico que impide que el
// último revelador obtenga el output de la VDF antes de que expire la ventana de TO.
// Todas las corridas usan N nodos de perfil last-revealer-vdf sin simulación de
// capacidad (velocidad real de la máquina). Ver experiments/vdf-timeout-calibration.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// sweepConfig es el esquema del bloque `sweep:` del YAML de calibración.
type sweepConfig struct {
	TimeoutsReveal2 []time.Duration `yaml:"timeouts_reveal2"`

	VDFTMin   int `yaml:"vdf_t_min"`   // cota inferior de la búsqueda
	VDFTStart int `yaml:"vdf_t_start"` // T inicial para la expansión de cota superior
	VDFTMax   int `yaml:"vdf_t_max"`   // tope; si se supera sin hallar T seguro → error

	BinaryGap   int `yaml:"binary_gap"`   // frenar la binaria cuando hi-lo <= gap
	ConfirmRuns int `yaml:"confirm_runs"` // corridas de confirmación en el T candidato
	ConfirmStep int `yaml:"confirm_step"` // +step al T si falla la confirmación

	Nodes int `yaml:"nodes"` // cantidad de nodos last-revealer-vdf por corrida

	TimeoutReady   time.Duration `yaml:"timeout_ready"`
	TimeoutCommit  time.Duration `yaml:"timeout_commit"`
	TimeoutReveal1 time.Duration `yaml:"timeout_reveal1"`
	DiscoveryDelay time.Duration `yaml:"discovery_delay"`
	PerRunTimeout  time.Duration `yaml:"per_run_timeout"`
}

type sweepFile struct {
	Sweep sweepConfig `yaml:"sweep"`
}

// loadSweepConfig lee el YAML, aplica defaults y valida.
func loadSweepConfig(path string) (sweepConfig, error) {
	var sf sweepFile
	data, err := os.ReadFile(path)
	if err != nil {
		return sweepConfig{}, err
	}
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return sweepConfig{}, err
	}
	sc := sf.Sweep

	// Defaults.
	if sc.VDFTMin <= 0 {
		sc.VDFTMin = 1
	}
	if sc.VDFTStart <= 0 {
		sc.VDFTStart = 2000
	}
	if sc.BinaryGap <= 0 {
		sc.BinaryGap = 50
	}
	if sc.ConfirmRuns <= 0 {
		sc.ConfirmRuns = 3
	}
	if sc.ConfirmStep <= 0 {
		sc.ConfirmStep = 50
	}
	if sc.Nodes <= 0 {
		sc.Nodes = 4
	}
	if sc.TimeoutReady <= 0 {
		sc.TimeoutReady = 2 * time.Second
	}
	if sc.TimeoutCommit <= 0 {
		sc.TimeoutCommit = 500 * time.Millisecond
	}
	if sc.TimeoutReveal1 <= 0 {
		sc.TimeoutReveal1 = 500 * time.Millisecond
	}
	if sc.DiscoveryDelay <= 0 {
		sc.DiscoveryDelay = 2 * time.Second
	}
	if sc.PerRunTimeout <= 0 {
		sc.PerRunTimeout = 60 * time.Second
	}

	// Validación.
	if len(sc.TimeoutsReveal2) == 0 {
		return sc, fmt.Errorf("sweep.timeouts_reveal2 no puede estar vacío")
	}
	if sc.VDFTMax <= 0 {
		return sc, fmt.Errorf("sweep.vdf_t_max debe ser > 0")
	}
	if sc.Nodes < 1 {
		return sc, fmt.Errorf("sweep.nodes debe ser >= 1")
	}
	return sc, nil
}

// runConfig arma la Config de una corrida concreta para un par (TO, T).
func (sc sweepConfig) runConfig(to time.Duration, T int) Config {
	var cfg Config
	cfg.Protocol.VDFT = T
	cfg.Protocol.TimeoutReady = sc.TimeoutReady
	cfg.Protocol.TimeoutCommit = sc.TimeoutCommit
	cfg.Protocol.TimeoutReveal1 = sc.TimeoutReveal1
	cfg.Protocol.TimeoutReveal2 = to
	cfg.DiscoveryDelay = sc.DiscoveryDelay
	cfg.MaxRuntime = sc.PerRunTimeout
	return cfg
}

// buildSweepSpecs genera N nodos last-revealer-vdf (el primero proponente, sin capacidad simulada).
func (sc sweepConfig) buildSweepSpecs() []nodeSpec {
	specs := make([]nodeSpec, 0, sc.Nodes)
	for i := 0; i < sc.Nodes; i++ {
		specs = append(specs, nodeSpec{
			label:    fmt.Sprintf("last-revealer-vdf_%d", i+1),
			profile:  "last-revealer-vdf",
			honest:   false,
			proposer: i == 0,
			capacity: 0,
		})
	}
	return specs
}

// waitExpVerdict es la condición de fin de una corrida del sweep: espera a que algún
// nodo emita la línea de veredicto [exp] (el último revelador) o a que venza per_run_timeout.
func waitExpVerdict(timeout time.Duration) func([]*runningNode) {
	return func(nodes []*runningNode) {
		deadline := time.After(timeout)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				return
			case <-ticker.C:
				for _, rn := range nodes {
					rn.mu.Lock()
					seen := rn.expSeen
					rn.mu.Unlock()
					if seen {
						return
					}
				}
			}
		}
	}
}

// trialResult es el resultado de una corrida del sweep.
type trialResult struct {
	anticipado bool  // true si el último revelador obtuvo el output dentro de la ventana de TO
	durMs      int64 // duración de la VDF
	label      string
}

// runTrial corre el protocolo una vez para (TO, T), captura el veredicto del último
// revelador y escribe una fila en sweep_runs.csv.
func (sc sweepConfig) runTrial(binPath, root string, to time.Duration, T int, phase string, rep int, runsCSV *os.File) trialResult {
	cfg := sc.runConfig(to, T)
	specs := sc.buildSweepSpecs()

	runDir := filepath.Join(root, "runs", fmt.Sprintf("%dms_T%d_%s%d", to.Milliseconds(), T, phase, rep))
	if err := os.MkdirAll(runDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[sweep] error creando %s: %v\n", runDir, err)
	}

	fmt.Printf("[sweep] TO=%v T=%d (%s rep=%d)... ", to, T, phase, rep)
	nodes, _ := runOnce(binPath, specs, cfg, runDir, waitExpVerdict(sc.PerRunTimeout))

	res := trialResult{}
	found := false
	for _, rn := range nodes {
		if rn.expSeen {
			res.anticipado = rn.expAnticipado
			res.durMs = rn.expDurMs
			res.label = rn.spec.label
			found = true
			break
		}
	}

	if !found {
		// Sin veredicto: la VDF del último revelador tardó más que per_run_timeout
		// (>> TO) ⇒ definitivamente NO anticipó el output. Se registra como segura.
		fmt.Printf("sin veredicto (VDF > %v ⇒ SEGURA)\n", sc.PerRunTimeout)
		res.anticipado = false
	} else {
		verdict := "SEGURA"
		if res.anticipado {
			verdict = "INSEGURA"
		}
		fmt.Printf("%s (dur=%dms win=%dms)\n", verdict, res.durMs, to.Milliseconds())
	}

	fmt.Fprintf(runsCSV, "%d,%d,%s,%d,%s,%d,%d,%t\n",
		to.Milliseconds(), T, phase, rep, res.label, res.durMs, to.Milliseconds(), res.anticipado)
	return res
}

// searchMinT busca el T mínimo seguro para un TO: expansión de cota superior, binaria
// hasta gap <= binary_gap, y confirmación de confirm_runs corridas (subiendo confirm_step
// si alguna falla). Devuelve (minT, durMsDeLaÚltimaConfirmación, hallado).
func (sc sweepConfig) searchMinT(binPath, root string, to time.Duration, runsCSV *os.File) (int, int64, bool) {
	// 1. Expansión: hi debe quedar seguro (no anticipado), lo inseguro (o VDFTMin).
	lo := sc.VDFTMin
	hi := sc.VDFTStart
	if hi <= lo {
		hi = lo + 1
	}
	for sc.runTrial(binPath, root, to, hi, "search", 0, runsCSV).anticipado {
		lo = hi
		hi *= 2
		if hi > sc.VDFTMax {
			fmt.Printf("[sweep] TO=%v: no se halló T seguro <= %d\n", to, sc.VDFTMax)
			return 0, 0, false
		}
	}

	// 2. Binaria hasta que la diferencia entre el T seguro y el anterior sea <= binary_gap.
	for hi-lo > sc.BinaryGap {
		mid := lo + (hi-lo)/2
		if sc.runTrial(binPath, root, to, mid, "search", 0, runsCSV).anticipado {
			lo = mid
		} else {
			hi = mid
		}
	}
	candidate := hi

	// 3. Confirmación: confirm_runs corridas seguras seguidas; si alguna falla, +confirm_step.
	for {
		allSafe := true
		var durMs int64
		for rep := 1; rep <= sc.ConfirmRuns; rep++ {
			res := sc.runTrial(binPath, root, to, candidate, "confirm", rep, runsCSV)
			if res.anticipado {
				allSafe = false
				break
			}
			durMs = res.durMs
		}
		if allSafe {
			fmt.Printf("[sweep] TO=%v: T mínimo seguro = %d (dur ~%d ms)\n", to, candidate, durMs)
			return candidate, durMs, true
		}
		candidate += sc.ConfirmStep
		if candidate > sc.VDFTMax {
			fmt.Printf("[sweep] TO=%v: no se confirmó T seguro <= %d\n", to, sc.VDFTMax)
			return 0, 0, false
		}
		fmt.Printf("[sweep] TO=%v: confirmación falló, subiendo T a %d\n", to, candidate)
	}
}

// minResult agrupa el T mínimo hallado para un TO.
type minResult struct {
	toMs  int64
	minT  int
	durMs int64
	found bool
}

// cells devuelve el T mínimo y la duración como strings, o "NA" si no se halló T seguro.
func (r minResult) cells() (minT, dur string) {
	if !r.found {
		return "NA", "NA"
	}
	return strconv.Itoa(r.minT), strconv.FormatInt(r.durMs, 10)
}

// runSweep ejecuta la calibración completa y escribe los resultados.
func runSweep(path string) error {
	sc, err := loadSweepConfig(path)
	if err != nil {
		return err
	}

	root := filepath.Join("experiments", "sweep_runs", time.Now().Format("2006-01-02_15-04-05"))
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0755); err != nil {
		return err
	}
	fmt.Printf("[sweep] resultados en: %s\n", root)

	binPath, cleanup, err := buildNodeBinary()
	if err != nil {
		return fmt.Errorf("compilando el nodo: %w", err)
	}
	defer cleanup()

	runsCSV, err := os.Create(filepath.Join(root, "sweep_runs.csv"))
	if err != nil {
		return err
	}
	defer runsCSV.Close()
	fmt.Fprintln(runsCSV, "to_ms,vdf_t,phase,rep,last_revealer_label,vdf_dur_ms,window_ms,output_anticipado")

	var results []minResult
	for _, to := range sc.TimeoutsReveal2 {
		fmt.Printf("\n[sweep] ===== TO=%v =====\n", to)
		minT, durMs, found := sc.searchMinT(binPath, root, to, runsCSV)
		results = append(results, minResult{toMs: to.Milliseconds(), minT: minT, durMs: durMs, found: found})
	}

	if err := writeMinT(root, results); err != nil {
		return err
	}
	printSweepSummary(root, results)
	return nil
}

// writeMinT escribe min_t.csv con el T mínimo seguro por cada TO.
func writeMinT(root string, results []minResult) error {
	csv, err := os.Create(filepath.Join(root, "min_t.csv"))
	if err != nil {
		return err
	}
	defer csv.Close()
	fmt.Fprintln(csv, "to_ms,min_vdf_t,vdf_dur_ms")
	for _, r := range results {
		minT, dur := r.cells()
		fmt.Fprintf(csv, "%d,%s,%s\n", r.toMs, minT, dur)
	}
	return nil
}

// printSweepSummary imprime la tabla final por stdout.
func printSweepSummary(root string, results []minResult) {
	fmt.Println()
	fmt.Println("================ SWEEP: T mínimo por TO ================")
	fmt.Printf("%-12s %-18s %s\n", "TO (ms)", "T mínimo VDF", "dur VDF (ms)")
	for _, r := range results {
		minT, dur := r.cells()
		fmt.Printf("%-12d %-18s %s\n", r.toMs, minT, dur)
	}
	fmt.Println("-------------------------------------------------------")
	fmt.Printf("resultados en: %s\n", root)
	fmt.Println("=======================================================")
}
