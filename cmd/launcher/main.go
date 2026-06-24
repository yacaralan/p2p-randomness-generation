// Command launcher levanta una red de N nodos (honestos + atacantes) como procesos
// separados a partir de un archivo de configuración YAML, los deja descubrirse por
// local-discovery, arranca el protocolo automáticamente y termina cuando todos los
// nodos honestos completan la VDF o al vencer un timeout global.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// localDir es el rendezvous de local-discovery; lo limpiamos antes de cada corrida
// para evitar peers fantasma de ejecuciones previas. Debe coincidir con discovery/local.go.
const localDir = "/tmp/p2p-randomness"

// Config es el esquema del archivo YAML.
type Config struct {
	Protocol struct {
		VDFT           int           `yaml:"vdf_t"`
		TimeoutReady   time.Duration `yaml:"timeout_ready"`
		TimeoutCommit  time.Duration `yaml:"timeout_commit"`
		TimeoutReveal1 time.Duration `yaml:"timeout_reveal1"`
		TimeoutReveal2 time.Duration `yaml:"timeout_reveal2"`
	} `yaml:"protocol"`
	DiscoveryDelay time.Duration `yaml:"discovery_delay"`
	MaxRuntime     time.Duration `yaml:"max_runtime"`
	Nodes          struct {
		Honest    honestSpec `yaml:"honest"`
		Attackers []struct {
			Profile  string       `yaml:"profile"`
			Count    int          `yaml:"count"`
			Capacity capacitySpec `yaml:"capacity"`
		} `yaml:"attackers"`
	} `yaml:"nodes"`
}

// honestSpec acepta dos formas en el YAML para el bloque "honest":
//   - un entero (honest: 4) → solo la cantidad, sin capacidades simuladas.
//   - un mapping (honest: {count: 4, capacity: [...]}) → cantidad + capacidades.
type honestSpec struct {
	Count    int          `yaml:"count"`
	Capacity capacitySpec `yaml:"capacity"`
}

func (h *honestSpec) UnmarshalYAML(value *yaml.Node) error {
	// Forma escalar: honest: 4
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&h.Count)
	}
	// Forma mapping: honest: {count, capacity}. Usamos un alias para evitar recursión.
	type raw honestSpec
	return value.Decode((*raw)(h))
}

// capacitySpec acepta un escalar (mismo valor para todos los nodos del grupo) o una
// lista (un valor por nodo). Representa la capacidad de cómputo en squarings/seg.
type capacitySpec struct {
	values []float64
}

func (c *capacitySpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode {
		return value.Decode(&c.values)
	}
	var v float64
	if err := value.Decode(&v); err != nil {
		return err
	}
	c.values = []float64{v}
	return nil
}

// resolve devuelve la capacidad de cada uno de los `count` nodos del grupo:
//   - sin valores → todos 0 (sin simulación).
//   - un solo valor → se replica a los `count` nodos.
//   - una lista → debe tener largo `count` exacto.
func (c capacitySpec) resolve(count int) ([]float64, error) {
	switch {
	case len(c.values) == 0:
		return make([]float64, count), nil
	case len(c.values) == 1:
		out := make([]float64, count)
		for i := range out {
			out[i] = c.values[0]
		}
		return out, nil
	case len(c.values) == count:
		return c.values, nil
	default:
		return nil, fmt.Errorf("capacity tiene %d valores pero el grupo tiene %d nodos", len(c.values), count)
	}
}

// nodeSpec describe un nodo a levantar.
type nodeSpec struct {
	label    string
	profile  string
	honest   bool
	proposer bool
	capacity float64 // capacidad simulada en squarings/seg; 0 = sin simulación
}

// runningNode agrupa un proceso lanzado con su spec, su archivo de transcript y el
// output capturado de la VDF.
type runningNode struct {
	spec         nodeSpec
	cmd          *exec.Cmd
	logFile      *os.File       // archivo de transcript del nodo
	streams      sync.WaitGroup // goroutines de captura (stdout + stderr)
	vdfOutput    string         // hex del "[vdf] output=" si lo emitió
	vdfInput     string         // hex del "input=" de "[vdf] iniciando cómputo"
	vdfProof     string         // hex del "[vdf] proof="
	vdfTimestamp string         // hora .log en que se capturó "[vdf] output="
	vdfEpochMs   int64          // epoch ms propio del nodo ("[vdf] output obtenido: <ms> ms")
	peerID       string         // de "[node] PeerID: <id>"
	participated bool           // true si el nodo emitió "[dcr] commit2 broadcasteado"
	mu           sync.Mutex     // serializa escrituras al logFile y los campos capturados
}

func main() {
	configFlag := flag.String("config", "", "Ruta al archivo de configuración YAML")
	flag.Parse()

	if *configFlag == "" {
		fmt.Fprintln(os.Stderr, "error: falta --config <path.yaml>")
		os.Exit(1)
	}

	cfg, err := loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error cargando config: %v\n", err)
		os.Exit(1)
	}

	specs, err := buildSpecs(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error en la config: %v\n", err)
		os.Exit(1)
	}

	// Limpiar el rendezvous de local-discovery para que no queden peers de corridas previas.
	if err := os.RemoveAll(localDir); err != nil {
		fmt.Fprintf(os.Stderr, "advertencia: no se pudo limpiar %s: %v\n", localDir, err)
	}

	// Carpeta de este experimento: un .txt por nodo con su transcript completo.
	expDir := filepath.Join("experiments", "runs", time.Now().Format("2006-01-02_15-04-05"))
	if err := os.MkdirAll(expDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creando carpeta del experimento: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[launcher] transcripts en: %s\n", expDir)

	// Compilar el binario del nodo una sola vez y reutilizarlo para todos los procesos.
	binPath, cleanup, err := buildNodeBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error compilando el nodo: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	fmt.Printf("[launcher] levantando %d nodos (%d honestos)\n", len(specs), countHonest(specs))

	ac := newAbortCollector()

	start := time.Now()
	nodes := make([]*runningNode, 0, len(specs))
	for _, spec := range specs {
		rn, err := spawnNode(binPath, spec, cfg, expDir, ac)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error lanzando %s: %v\n", spec.label, err)
			continue
		}
		nodes = append(nodes, rn)
	}

	// Esperar a que terminen los nodos honestos (se auto-apagan al completar la VDF)
	// o a que venza el timeout global, lo que ocurra primero.
	honestDone := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, rn := range nodes {
			if !rn.spec.honest {
				continue
			}
			wg.Add(1)
			go func(rn *runningNode) {
				defer wg.Done()
				rn.cmd.Wait()
			}(rn)
		}
		wg.Wait()
		close(honestDone)
	}()

	select {
	case <-honestDone:
		fmt.Println("[launcher] todos los nodos honestos completaron la ejecución")
	case <-time.After(cfg.MaxRuntime):
		fmt.Printf("[launcher] timeout global (%v) alcanzado\n", cfg.MaxRuntime)
	}

	// Apagar todo lo que siga vivo: SIGTERM, esperar, SIGKILL.
	shutdown(nodes)

	// Procesos muertos y pipes en EOF: esperar a que las goroutines de captura
	// terminen de escribir y cerrar cada transcript.
	for _, rn := range nodes {
		rn.streams.Wait()
		rn.logFile.Close()
	}

	elapsed := time.Since(start)
	printSummary(nodes, elapsed, expDir)
	writeResults(nodes, ac, cfg, expDir, elapsed)
}

// loadConfig lee y parsea el YAML.
func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.MaxRuntime <= 0 {
		return cfg, fmt.Errorf("max_runtime debe ser > 0")
	}
	return cfg, nil
}

// buildSpecs expande la composición de la red en una lista de nodos concretos.
// El proponente es el primer nodo honesto.
func buildSpecs(cfg Config) ([]nodeSpec, error) {
	if cfg.Nodes.Honest.Count < 1 {
		return nil, fmt.Errorf("se requiere al menos 1 nodo honesto (el proponente)")
	}

	honestCaps, err := cfg.Nodes.Honest.Capacity.resolve(cfg.Nodes.Honest.Count)
	if err != nil {
		return nil, fmt.Errorf("honest: %w", err)
	}

	// Numeración por perfil: honest_1, honest_2, …, last-revealer-vdf_1, …
	count := make(map[string]int)
	specs := make([]nodeSpec, 0)
	for i := 0; i < cfg.Nodes.Honest.Count; i++ {
		count["honest"]++
		specs = append(specs, nodeSpec{
			label:    fmt.Sprintf("honest_%d", count["honest"]),
			profile:  "honest",
			honest:   true,
			proposer: i == 0, // el primer honesto propone el inicio
			capacity: honestCaps[i],
		})
	}
	for _, a := range cfg.Nodes.Attackers {
		if a.Profile == "" || a.Profile == "honest" {
			return nil, fmt.Errorf("entrada de atacante con perfil inválido: %q", a.Profile)
		}
		attackerCaps, err := a.Capacity.resolve(a.Count)
		if err != nil {
			return nil, fmt.Errorf("atacante %q: %w", a.Profile, err)
		}
		for i := 0; i < a.Count; i++ {
			count[a.Profile]++
			specs = append(specs, nodeSpec{
				label:    fmt.Sprintf("%s_%d", a.Profile, count[a.Profile]),
				profile:  a.Profile,
				honest:   false,
				capacity: attackerCaps[i],
			})
		}
	}
	return specs, nil
}

// buildNodeBinary compila ./cmd/node a un binario temporal y devuelve su ruta y un cleanup.
func buildNodeBinary() (string, func(), error) {
	dir, err := os.MkdirTemp("", "p2p-launcher-")
	if err != nil {
		return "", nil, err
	}
	binPath := filepath.Join(dir, "node")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/node")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return binPath, func() { os.RemoveAll(dir) }, nil
}

// spawnNode lanza un subproceso del nodo con los flags correspondientes a su spec
// y vuelca su transcript a <expDir>/<label>.txt.
func spawnNode(binPath string, spec nodeSpec, cfg Config, expDir string, ac *abortCollector) (*runningNode, error) {
	args := []string{
		"--port", "0",
		"--vdf-t", fmt.Sprintf("%d", cfg.Protocol.VDFT),
		"--timeout-ready", cfg.Protocol.TimeoutReady.String(),
		"--timeout-commit", cfg.Protocol.TimeoutCommit.String(),
		"--timeout-reveal1", cfg.Protocol.TimeoutReveal1.String(),
		"--timeout-reveal2", cfg.Protocol.TimeoutReveal2.String(),
		"--attacker", spec.profile,
		"--exit-on-vdf",
	}
	if spec.capacity > 0 {
		args = append(args, "--vdf-capacity", strconv.FormatFloat(spec.capacity, 'g', -1, 64))
	}
	if spec.proposer {
		args = append(args, "--auto-start", "--auto-start-delay", cfg.DiscoveryDelay.String())
	}

	logFile, err := os.Create(filepath.Join(expDir, spec.label+".log"))
	if err != nil {
		return nil, err
	}
	role := "honesto"
	if !spec.honest {
		role = "atacante"
	}
	if spec.proposer {
		role += ", proponente"
	}
	fmt.Fprintf(logFile, "# transcript de %s (perfil=%s, %s) — %s\n",
		spec.label, spec.profile, role, time.Now().Format("2006-01-02 15:04:05.000000000"))

	cmd := exec.Command(binPath, args...)
	rn := &runningNode{spec: spec, cmd: cmd, logFile: logFile}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logFile.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		logFile.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}

	rn.streams.Add(2)
	go streamOutput(rn, stdout, ac)
	go streamOutput(rn, stderr, ac)
	return rn, nil
}

// streamOutput escribe la salida del subproceso al transcript del nodo, prefijando cada
// línea con un timestamp de reloj de máxima precisión, y captura el output de la VDF,
// el PeerID y los eventos de aborto para el resumen final.
func streamOutput(rn *runningNode, r io.Reader, ac *abortCollector) {
	defer rn.streams.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ts := time.Now().Format("15:04:05.000000000")
		rn.mu.Lock()
		fmt.Fprintf(rn.logFile, "%s %s\n", ts, line)
		if hex, ok := parseVDFOutput(line); ok {
			rn.vdfOutput = hex
			rn.vdfTimestamp = ts
		}
		if in, ok := parseVDFInput(line); ok {
			rn.vdfInput = in
		}
		if pf, ok := parseVDFProof(line); ok {
			rn.vdfProof = pf
		}
		if ms, ok := parseVDFEpochMs(line); ok && rn.vdfEpochMs == 0 {
			rn.vdfEpochMs = ms
		}
		if pid, ok := parsePeerID(line); ok && rn.peerID == "" {
			rn.peerID = pid
		}
		if !rn.participated && strings.Contains(line, "[dcr] commit2 broadcasteado") {
			rn.participated = true
		}
		rn.mu.Unlock()
		if short, phase, reason, ok := parseAbort(line); ok {
			ac.record(short, phase, reason)
		}
	}
}

// parseVDFOutput extrae el hex de una línea "[vdf] output=<hex>".
func parseVDFOutput(line string) (string, bool) {
	const marker = "[vdf] output="
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(line[idx+len(marker):]), true
}

// shutdown envía SIGTERM a los procesos vivos, espera, y mata los que sigan corriendo.
func shutdown(nodes []*runningNode) {
	alive := make([]*runningNode, 0)
	for _, rn := range nodes {
		if rn.cmd.ProcessState != nil && rn.cmd.ProcessState.Exited() {
			continue
		}
		if rn.cmd.Process == nil {
			continue
		}
		_ = rn.cmd.Process.Signal(os.Interrupt)
		alive = append(alive, rn)
	}
	if len(alive) == 0 {
		return
	}

	done := make(chan struct{})
	go func() {
		for _, rn := range alive {
			rn.cmd.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		for _, rn := range alive {
			if rn.cmd.Process != nil {
				_ = rn.cmd.Process.Kill()
			}
		}
	}
}

// printSummary imprime el resultado de la corrida: consistencia entre honestos y estado de cada nodo.
func printSummary(nodes []*runningNode, elapsed time.Duration, expDir string) {
	fmt.Println()
	fmt.Println("================ RESUMEN ================")
	fmt.Printf("duración total: %v\n", elapsed.Round(time.Millisecond))

	var honestOutputs []string
	for _, rn := range nodes {
		rn.mu.Lock()
		out := rn.vdfOutput
		rn.mu.Unlock()

		status := "sin VDF"
		if out != "" {
			status = fmt.Sprintf("VDF=%s…", short(out))
		}
		fmt.Printf("  %-26s %s\n", rn.spec.label, status)
		if rn.spec.honest {
			honestOutputs = append(honestOutputs, out)
		}
	}

	fmt.Println("----------------------------------------")
	switch {
	case allEqualNonEmpty(honestOutputs):
		fmt.Printf("consistencia OK: los %d nodos honestos coinciden en el valor aleatorio\n", len(honestOutputs))
	case anyEmpty(honestOutputs):
		fmt.Println("consistencia N/A: algún nodo honesto no completó la VDF (ver arriba)")
	default:
		fmt.Println("consistencia FALLÓ: los nodos honestos no coinciden en el valor aleatorio")
	}
	fmt.Printf("transcripts en: %s\n", expDir)
	fmt.Println("=========================================")
}

func countHonest(specs []nodeSpec) int {
	n := 0
	for _, s := range specs {
		if s.honest {
			n++
		}
	}
	return n
}

func allEqualNonEmpty(xs []string) bool {
	if len(xs) == 0 {
		return false
	}
	for _, x := range xs {
		if x == "" || x != xs[0] {
			return false
		}
	}
	return true
}

func anyEmpty(xs []string) bool {
	for _, x := range xs {
		if x == "" {
			return true
		}
	}
	return false
}

func short(hex string) string {
	if len(hex) <= 16 {
		return hex
	}
	return hex[:16]
}
