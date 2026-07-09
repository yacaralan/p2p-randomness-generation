#!/usr/bin/env python3
"""Grafica el T mínimo seguro de VDF por cada TO para una corrida del sweep.

Uso:
    python experiments/scripts/plot_min_t.py <timestamp>

<timestamp> es el nombre de la carpeta de la corrida (ej. 2026-07-09_01-09-11).
El script busca experiments/sweep_runs/<timestamp>/min_t.csv y genera min_t.png
junto a ese CSV. Requiere matplotlib (pip install matplotlib).
"""
import csv
import sys
from pathlib import Path

import matplotlib.pyplot as plt

# Raíz de las corridas del sweep, relativa a la ubicación de este script
# (experiments/scripts/ → experiments/sweep_runs/).
SWEEP_RUNS = Path(__file__).resolve().parent.parent / "sweep_runs"


def main() -> None:
    if len(sys.argv) != 2:
        print(f"uso: {sys.argv[0]} <timestamp>  (ej. 2026-07-09_01-09-11)", file=sys.stderr)
        sys.exit(1)

    timestamp = sys.argv[1]
    csv_path = SWEEP_RUNS / timestamp / "min_t.csv"
    if not csv_path.exists():
        print(f"no existe {csv_path}", file=sys.stderr)
        sys.exit(1)

    tos, min_ts = [], []
    with csv_path.open() as f:
        for row in csv.DictReader(f):
            if row["min_vdf_t"] == "NA":
                continue
            tos.append(int(row["to_ms"]))
            min_ts.append(int(row["min_vdf_t"]))

    if not tos:
        print("no hay filas con T hallado en el CSV", file=sys.stderr)
        sys.exit(1)

    labels = [str(t) for t in tos]
    fig, ax = plt.subplots(figsize=(8, 5))
    bars = ax.bar(labels, min_ts, color="#4C72B0")
    ax.set_xlabel("Timeout de reveal2 — TO (ms)")
    ax.set_ylabel("T mínimo seguro de VDF (iteraciones)")
    ax.set_title("T mínimo de VDF que evita el output anticipado del último revelador")
    ax.bar_label(bars, padding=3)

    out = csv_path.with_name("min_t.png")
    fig.tight_layout()
    fig.savefig(out, dpi=150)
    print(f"gráfico guardado en: {out}")


if __name__ == "__main__":
    main()
