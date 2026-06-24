# Throttling de hardware real (opción para un plan futuro)

Estado: **no implementado**. La simulación de capacidad de cómputo actual usa un
modelo por software (tasa absoluta de squarings/seg, ver `--vdf-capacity` y el campo
`capacity` en `launch.yaml`). Este documento registra la alternativa de limitar la CPU
*física* del proceso, descartada como base de las mediciones, por si en el futuro se
quiere respaldar la pregunta P2 (viabilidad en hardware heterogéneo) con CPU real.

## Por qué NO es la base de las mediciones

- **No reproducible.** El throttling real depende de la velocidad física de la CPU, la
  carga del momento y el estado térmico. El mismo experimento en otra máquina da otro Δ.
  El modelo por software da el mismo Δ en cualquier máquina, atribuible solo a la
  heterogeneidad definida.
- **macOS.** La máquina de desarrollo es darwin: no tiene cgroups; `cpulimit` hay que
  instalarlo y es impreciso (throttlea pausando el proceso, con jitter); `nice` solo
  cambia prioridad de scheduling bajo contención, no la velocidad absoluta.
- **Calibración.** "Limitar a 30% de CPU" no se traduce a un número conocido de
  squarings/seg; habría que calibrar en cada máquina. El modelo por software *ya es* esa
  relación, exacta.
- **VDF de un solo hilo.** La afinidad de cores (`taskset`) no sirve: la VDF usa un core.

## Cómo se integraría (gold standard: Docker + cgroups)

La opción reproducible *y* de CPU real es ejecutar cada nodo en un contenedor con cuota
de CPU vía cgroups, en un host Linux (o CI):

```bash
docker run --cpus=0.2 nodo-img --port 0 --vdf-t 500 ...   # ~1/5 de un core
docker run --cpus=1.0 nodo-img ...                         # 1 core completo
```

Cambios necesarios (esbozo):

- **Dockerfile** para `cmd/node` (binario estático).
- **launcher**: en vez de `exec.Command(binPath, args...)`, lanzar
  `docker run --cpus=<c> ...` por nodo, mapeando `capacity` → `--cpus`. Habría que
  resolver el descubrimiento entre contenedores (red Docker compartida o `--network host`
  en Linux) y la captura de stdout/stderr (ya se hace por pipes, se mantiene).
- **Calibración** de `--cpus` ↔ duración de VDF observada, para reportar el equivalente
  en squarings/seg y poder comparar con las corridas por software.

## Alternativa liviana en macOS (solo demostrativa)

`cpulimit` como wrapper, sin Docker, para una corrida puntual de viabilidad:

```bash
brew install cpulimit
cpulimit --limit=20 -- ./node --port 0 --vdf-t 500 ...
```

Impreciso y no reproducible; sirve solo como evidencia anecdótica de "corre con CPU
restringida real", no como dato de los experimentos de equidad temporal.

## Recomendación

Mantener el modelo por software como base de todas las mediciones de Δ-acotamiento.
Si se quiere una corrida de respaldo con CPU real para P2, hacerla con Docker `--cpus`
en un host Linux y reportarla como anexo, no como serie principal.
