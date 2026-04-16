// Package protocol define los tipos de mensajes y la lógica de comunicación
// del protocolo de generación distribuida de aleatoriedad.
//
// En libp2p, los protocolos se identifican con un string del estilo
// "/nombre/versión". Cuando un nodo abre un stream a otro, especifica
// qué protocolo quiere usar. El nodo remoto acepta si tiene un handler
// registrado para ese ID, o rechaza con un error "protocol not supported".
//
// Esta primera iteración solo define mensajes de tipo Ping/Pong para
// verificar que la conectividad entre nodos funciona correctamente.
// Las iteraciones futuras extenderán MessageType con Commit, Reveal,
// VDFProof, etc., sin cambiar la estructura base de Message.
package protocol

// ProtocolID es el identificador del protocolo en la red libp2p.
// Todos los nodos que quieran participar deben registrar un handler
// para este mismo ID. La versión "/1.0.0" nos permite evolucionar
// el protocolo sin romper compatibilidad (futuro: "/2.0.0").
const ProtocolID = "/randomness/1.0.0"

// MessageType identifica la semántica del mensaje dentro del protocolo.
// Usar un string tipado (en lugar de int o byte) hace el código más
// legible y facilita el debugging al leer logs o capturas de red.
type MessageType string

const (
	// MessageTypePing es enviado por un nodo para iniciar contacto.
	// En iteraciones futuras, el "primer contacto" será un Commit.
	MessageTypePing MessageType = "PING"

	// MessageTypePong es la respuesta a un Ping.
	// En iteraciones futuras, la respuesta al Commit será un Ack o un Commit propio.
	MessageTypePong MessageType = "PONG"
)

// Message es el envelope que se intercambia entre peers a través de un stream.
//
// Formato de serialización: JSON newline-delimited (un objeto JSON por línea).
// Se eligió este formato para esta iteración porque:
//   - Es legible en el terminal durante desarrollo (sin herramientas externas)
//   - bufio.Scanner con ScanLines lo parsea trivialmente
//   - Es reemplazable por Protocol Buffers en iteraciones futuras sin
//     cambiar la estructura de Message ni la lógica del handler
//
// En una red real, usaríamos length-prefixed encoding (varint + bytes)
// para evitar ambigüedades con payloads que contengan saltos de línea.
type Message struct {
	Type    MessageType `json:"type"`
	Payload string      `json:"payload"` // contenido arbitrario; se tipará en iteraciones futuras
}
