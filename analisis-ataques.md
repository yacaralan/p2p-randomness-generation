# Análisis de vectores de ataque al protocolo

Protocolo: Commit-Reveal² + VDF (Wesolowski) con mecanismo de timeout por mayoría.

---

## 1. Clasificación de atacantes

### Por capacidad de acción

| Tipo | Descripción |
|------|-------------|
| **Honesto-pero-curioso** | Sigue el protocolo exactamente pero intenta obtener el output antes que los demás (ventaja temporal) |
| **Deshonesto activo (Byzantine)** | Se desvía arbitrariamente: omite mensajes, envía valores falsos, aborta estratégicamente |
| **Coalición coordinada** | Múltiples nodos maliciosos que se comunican fuera de banda. Crítico cuando `2m ≥ n` (tienen mayoría en el mecanismo de timeout) |
| **Adversario de red** | Controla (parte de) la red: puede retrasar, reordenar o dropar mensajes, sin romper criptografía |

### Por objetivo

| Objetivo | Definición |
|----------|------------|
| **Sesgador (bias)** | Influir en la distribución del output hacia valores favorables |
| **Ventajista temporal** | Obtener el valor final antes que los nodos honestos (violar Δ-fairness) |
| **Abortador** | Forzar que el protocolo falle o quede bloqueado |
| **Free-rider** | Beneficiarse del output sin contribuir entropía real (s_i predecible o constante) |

---

## 2. Ataques por fase

### Fase SESSION\_LOCK

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **Flooding de PROPOSE\_START** | Enviar múltiples propuestas para confundir qué sesión es válida | Parcialmente: el código acepta la primera y rechaza el resto |
| **No enviar READY\_ACK** | El proponente nunca alcanza el quórum esperado; el protocolo no arranca | No: falta timeout en esta fase |

### Fase Commit2

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **Commit inválido** | Publicar c_i sin preimagen s_i válida | Sí: falla en reveal1 cuando H(r_i) ≠ c_i |
| **No commitear** | Dejar expirar el timer de commit2 | Sí: timeout por mayoría → abortado y excluido |
| **Commit tardío adaptativo** | Esperar ver los commits ajenos antes de publicar el propio para elegir s_i favorablemente | Parcialmente: c_j = H(H(s_j)) no revela s_j ni Ω_v, por lo que ver los commits ajenos aporta poco. Sin embargo, un atacante con alto poder de cómputo podría hacer brute-force buscando s_i tal que el orden de reveal2 resultante lo ubique en última posición |
| **Commit múltiple (equivocación)** | El atacante publica dos commits distintos c_A y c_B con IDs de mensaje distintos. Gossipsub propaga ambos a toda la red, pero el orden de llegada varía por topología y latencia. Distintos peers honestos pueden terminar almacenando versiones distintas del commit del atacante. En reveal1, el atacante publica el r_i que valida uno de los dos: algunos peers pasan la verificación y otros la fallan → inconsistencia de estado | No: la deduplicación de gossipsub previene el reenvío del mismo mensaje (mismo ID), pero no detecta que un peer envió dos mensajes distintos para la misma fase. Requeriría detección explícita de equivocación: si se reciben dos commits distintos del mismo peer en la misma sesión → marcarlo como Byzantine |

### Fase Reveal1

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **No revelar** | Timeout → abortado, excluido del revealOrder | Sí |
| **Reveal1 inválido** | Publicar r_i' con H(r_i') ≠ c_i | Sí: verificación inmediata |
| **Aborto estratégico del último revelador** | El nodo que revela r_i al final ve el XOR parcial de todos los demás y puede calcular cómo afecta su r_i al revealOrder; aborta si el orden resultante no le conviene | No: instancia de [Cleve86]. Con n participantes, el último revelador puede explorar 2 outcomes posibles (su r_i vs. su ausencia). Cuanto más tarde en el orden, mayor influencia |
| **Reveal1 selectivo (eclipsing)** | Enviar r_i solo a un subconjunto de peers para que distintos nodos honestos calculen revealOrders distintos | Difícil en gossipsub broadcast; posible si el atacante controla directamente sus conexiones salientes |

### Fase Reveal2

#### Ataques de ventaja temporal

La VDF recibe como input la concatenación completa `s_1 || s_2 || ... || s_n` (en revealOrder). A diferencia de otras fases, aquí el input se construye incrementalmente a medida que cada nodo revela.

> **Nota sobre precomputación:** la VDF no es incremental ni reutilizable. Si el atacante en posición k empieza a computar VDF con el input parcial `s_1 || ... || s_k`, ese cómputo es completamente inútil cuando lleguen `s_{k+1}...s_n` — el input cambia y hay que reiniciar. La precomputación útil solo aplica al **último revelador**.

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **Precomputación del último revelador** | El nodo en posición n conoce `s_1,...,s_{n-1}` (ya revelados) **y** su propio `s_n`: tiene el input **completo** antes de decidir si revelar. Puede iniciar VDF en paralelo con su decisión. Los nodos honestos solo conocen el input completo cuando él publica s_n, arrancando la VDF con τ_VDF segundos de desventaja | No: esta ventaja es estructural. Es exactamente el Δ = τ_VDF que el protocolo intenta acotar |
| **Elección entre dos inputs (1 decisión binaria)** | El último revelador puede calcular el output de **dos escenarios**: (A) revelar `s_n` → input `...‖s_n`; (B) abortar → input `...‖r_n` (fallback). Elige el más favorable. Ambos valores (s_n y r_n) son de 32 bytes y estaban criptográficamente comprometidos desde el commit — el adversario no puede cambiarlos. Lo que tiene es una **elección binaria** entre dos outputs pre-determinados: es el espacio de decisión lo que es de "1 bit", no el tamaño del input. Con k nodos de coalición en las últimas k posiciones del revealOrder, el espacio crece a 2^k decisiones binarias independientes | No: consecuencia del mecanismo de aborto + fallback. La alternativa (no tener fallback) bloquearía el protocolo |
| **Delay de reveal2** | Retrasar el propio reveal2 hasta cerca del límite del timeout (sin llegar a ser abortado) | Parcialmente: el timeout limita la ventana; pero maximiza el tiempo disponible para computar la VDF en paralelo antes de decidir |
| **Coalición en posiciones finales** | Si el atacante controla los nodos en las últimas k posiciones del revealOrder, tiene k decisiones binarias independientes (revelar/abortar cada una) → 2^k combinaciones posibles de input a la VDF. Todos los valores individuales (s_i, r_i) son de 32 bytes y estaban fijos desde el commit; el adversario no genera nuevos valores, solo elige entre combinaciones pre-comprometidas | No: escala exponencialmente con k; pero el espacio de búsqueda es manejable para k pequeño |
| **ASIC / hardware especializado** | Un adversario con hardware de cómputo secuencial más rápido termina la VDF antes que los nodos honestos, acortando τ_VDF efectivamente | No mitigable sin proof-of-hardware o VDF ajustable dinámicamente. Rompe la garantía Δ si el factor de aceleración es significativo |

#### Otros ataques en reveal2

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **No revelar s_i** | Timeout → abortado en reveal2; r_i se usa como fallback | Sí |
| **Reveal2 inválido** | Publicar s_i' con H(s_i') ≠ r_i | Sí: verificación inmediata |
| **Equivocación en reveal2** | Enviar distintos s_i a distintos peers → subconjuntos de honestos calculan inputs distintos para la VDF | No completamente: depende de si gossipsub deduplica o si se agrega un mecanismo de validación cruzada |

### Mecanismo de timeout / disputa

| Ataque | Descripción | ¿Mitigado? |
|--------|-------------|------------|
| **Voto de timeout falso** | Votar timeout para un nodo honesto que sí envió el mensaje | Sí: la disputa — los que tienen el valor lo reenvían; si ≥ mayoría disputan → timeout rechazado |
| **Delay de red + voto falso** | El atacante retrasa el mensaje de X para una porción de nodos; esos nodos votan timeout mientras el resto disputa. Si logra que `mayoría` no reciban el mensaje, fuerza el aborto de un nodo honesto | Parcialmente: requiere que el atacante controle la red para bloquear el mensaje de al menos `n/2 + 1` nodos. Un adversario de red fuerte puede lograrlo |
| **Coalición ≥ mayoría** | `m ≥ n/2 + 1` nodos maliciosos votan timeout coordinadamente para abortar cualquier nodo honesto | No mitigable: es el límite teórico del mecanismo de mayoría. Requiere `m < n/2` nodos honestos para que el sistema sea seguro |

---

## 3. Ventaja temporal del adversario (Δ-fairness)

El nodo en posición k de reveal2 tiene una ventaja temporal estructural que crece con la posición:

- **Posiciones k < n**: no hay precomputación útil de VDF (el input cambia cuando revelan los siguientes). La ventaja temporal es mínima (solo la latencia de red para recibir el output final).
- **Posición n (último revelador)**: tiene el input completo antes de publicar s_n. Puede computar VDF en paralelo con la decisión de revelar/abortar. **Ventaja = τ_VDF**.

```
Ventaja del adversario en posición n ≤ τ_VDF + tiempo_de_decisión
```

Para coaliciones que controlan las últimas k posiciones:

```
Ventaja ≤ τ_VDF + tiempo_para_explorar_2^k_combinaciones
```

La Δ-fairness que busca el protocolo dice que τ_VDF es la cota superior de esta ventaja. Si τ_VDF es muy grande, el adversario tiene más tiempo pero la VDF es más fuerte (harder to precompute). Si τ_VDF es pequeño, la ventaja es menor pero la VDF aporta menos garantías de secuencialidad.

---

## 4. Ataques no contemplados en la implementación actual

| Ataque | Descripción |
|--------|-------------|
| **Sybil / entropy grinding** | Un atacante genera múltiples identidades y elige la que produce el commit más favorable (por ejemplo, la que lo ubica en última posición de reveal2). Sin mecanismo de admisión (proof-of-work, stake, etc.) no hay defensa |
| **Adaptive corruption** | El adversario observa los commits y decide corromper a un nodo honesto después de ver sus compromisos (corrupción adaptativa vs. estática). El modelo de seguridad del protocolo asume adversario estático |
| **Timeout de SESSION\_LOCK** | Si el proponente muere después de publicar PROPOSE\_START pero antes de SESSION\_LOCK, el protocolo queda bloqueado indefinidamente (no hay timeout en esta fase) |
| **Replay de valor entre fases** | Reutilizar un r_i válido de una sesión anterior como valor de disputa de otra fase/sesión | Depende de si hay identificadores de sesión en los mensajes |

---

## 5. Relevancia para las preguntas de investigación

| Pregunta | Ataques más relevantes |
|----------|----------------------|
| **P1** (Δ-fairness sin autoridad central) | Precomputación del último revelador, coalición en posiciones finales, ASIC advantage |
| **P2** (viabilidad práctica) | Coalición ≥ mayoría, Sybil, adaptive corruption |
| **P3** (límites inferiores sobre ventaja temporal) | Precomputación del último revelador (cota inferior = τ_VDF), elección entre 2 outputs pre-comprometidos (1 decisión binaria inevitable por participante en posición final), [Cleve86] como imposibilidad base |

El resultado de imposibilidad [Cleve86] implica que con adversarios que pueden abortar, siempre existe un tradeoff: el último revelador siempre tiene al menos 1 bit de influencia sobre el output. La VDF no elimina esto — lo acota temporalmente a τ_VDF.
