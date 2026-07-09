# Experimento: calibración del par (TO, T) — timeout de reveal2 vs T de la VDF

## Motivación

El protocolo acota la ventaja temporal del adversario con una VDF cuyo tiempo de cómputo
depende del parámetro **T** (iteraciones de squaring de Wesolowski). El atacante más peligroso
es el **último revelador**: al conocer el input completo de la VDF antes que los honestos, puede
iniciar el cómputo de la VDF de inmediato y retener su propio `reveal2` hasta casi el final de la
ventana de timeout (TO). Si en ese lapso **obtiene el output de la VDF antes de que expire TO**,
puede decidir *selectivamente* si publicar su reveal o abortar, según le guste o no el resultado
—rompiendo la resistencia al sesgo.

De ahí las restricciones de diseño:

1. **T debe ser lo bastante grande** para que ningún último revelador obtenga el output de la
   VDF antes de que se cierre la ventana de TO.
2. Por (1), **T depende de TO**: a mayor TO, mayor debe ser T.
3. Pero **T no debe ser mayor que lo necesario**: cuanto más grande T, más se amplifica la
   ventaja de los nodos con más poder de cómputo (computan la VDF proporcionalmente más rápido).
4. En la práctica primero se fija TO según la latencia de la red, y recién entonces se elige T.

**Objetivo del experimento:** para cada TO candidato, hallar el **T mínimo** que satisface (1).

## Método

- Cada corrida levanta **N nodos, todos con perfil `last-revealer-vdf`** (por defecto 4). Solo
  uno es el verdadero último revelador por corrida; el resto se comporta honestamente (hereda
  `honestBehavior`). Así asumimos el caso adverso: cualquiera que quede último intenta
  aprovecharse.
- **Sin simulación de capacidad** (`capacity = 0`): se usa la velocidad real máxima de la
  máquina. La medición es por tanto **dependiente del hardware** (aporta a las preguntas P2 y P3
  de la tesis: viabilidad práctica y límites de la ventaja temporal).
- El comportamiento del atacante **no se modifica**: sigue publicando su `reveal2` con el delay
  actual (0.9·TO). Lo único nuevo es que, cuando el último revelador termina de computar la VDF,
  emite una línea de veredicto que compara la duración del cómputo contra la ventana de TO.

### Definición de corrida

Sea `dur` la duración real del cómputo de la VDF del último revelador (medida desde que arranca,
que coincide ~con el instante en que es designado último y los honestos inician su timer de TO
apuntándolo):

- **SEGURA**: `dur ≥ TO` — no alcanzó a conocer el output dentro de la ventana.
- **INSEGURA (output anticipado)**: `dur < TO` — conoció el output dentro de la ventana y podría
  abortar selectivamente. El `reveal2` se publica igual; el experimento solo registra el evento.

La línea de veredicto en el transcript del nodo es:

```
[exp] last-revealer output_dur_ms=<dur> window_ms=<TO> output_anticipado=<true|false>
```

## Búsqueda del T mínimo por cada TO

Por cada TO se corre una **búsqueda binaria + confirmación**:

1. **Expansión de la cota superior.** Se parte de `vdf_t_start` y se duplica T hasta que una
   corrida resulte SEGURA. Queda `lo` = último T inseguro conocido, `hi` = primer T seguro.
   Si se supera `vdf_t_max` sin hallar T seguro, se reporta "no hallado".
2. **Binaria.** Se estrecha `[lo, hi]` hasta que `hi - lo ≤ binary_gap` (50 por defecto),
   probando el punto medio en cada paso. `candidate = hi`.
3. **Confirmación.** Se corre `candidate` `confirm_runs` veces (3 por defecto). Si las 3 son
   SEGURAS, `candidate` es el T mínimo. Si alguna resulta INSEGURA, se sube `candidate` en
   `confirm_step` (+50) y se repite la tanda, hasta lograr 3/3 seguras.

La confirmación absorbe el ruido de scheduling del sistema en la frontera. La duración del
cómputo de la VDF es esencialmente determinista dado T y el hardware (el input no cambia el número
de squarings), por lo que los resultados son reproducibles en la misma máquina.

## Cómo ejecutarlo

```bash
go run ./cmd/launcher --sweep experiments/sweep.yaml
```

Parámetros configurables en `experiments/sweep.yaml`: la lista de TO (`timeouts_reveal2`), las
cotas y el paso de la búsqueda (`vdf_t_min`, `vdf_t_start`, `vdf_t_max`, `binary_gap`), la
confirmación (`confirm_runs`, `confirm_step`), la cantidad de nodos, los timeouts de las demás
fases y `per_run_timeout`.

## Salidas

Todo queda en `experiments/sweep_runs/<timestamp>/`:

- `runs/<TOms>_T<T>_<phase><rep>/*.log` — transcripts por corrida (para debug).
- `sweep_runs.csv` — una fila por corrida:
  `to_ms, vdf_t, phase(search|confirm), rep, last_revealer_label, vdf_dur_ms, window_ms, output_anticipado`.
- `min_t.csv` — **tabla final**: el T mínimo seguro por cada TO.

> **Nota sobre métricas.** Este experimento **no** usa `summary.json` ni las invariantes
> honesto-vs-atacante del launcher normal: con todos los nodos `last-revealer-vdf` parecería que
> "no hubo honestos". El veredicto se computa exclusivamente a partir de la línea `[exp]`.

### Gráfico (opcional)

```bash
python experiments/scripts/plot_min_t.py <timestamp>
```

donde `<timestamp>` es el nombre de la carpeta de la corrida (ej. `2026-07-09_01-09-11`). Busca
`experiments/sweep_runs/<timestamp>/min_t.csv` y genera `min_t.png` a su lado (barras: TO en el
eje x, T mínimo seguro en el eje y). Requiere `matplotlib`.

## Interpretación

La tabla `min_t.csv` responde directamente la pregunta de diseño: dado el TO que impone la red,
qué T de VDF elegir. Debe usarse el **T mínimo** de la fila correspondiente (o el TO seguro
inmediatamente superior), ya que T más grandes solo amplifican la ventaja de los nodos con más
cómputo sin aportar seguridad adicional. Como la medición depende del hardware, la calibración
debe rehacerse (o ajustarse con un margen) para el hardware más rápido esperado entre los
participantes.
