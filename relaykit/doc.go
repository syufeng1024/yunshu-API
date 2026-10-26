// Package relaykit is the protocol conversion layer. It is a standalone Go
// module (yunshu.dev/relaykit) with no dependency on internal/ or cmd/.
//
// The public surface lives in the subpackages ir, openai, anthropic and
// bailian. Every wire protocol converts to ir first and from ir second; there
// is no protocol-to-protocol shortcut. Unknown members are preserved so a
// parameter the gateway does not model still reaches the upstream unchanged.
package relaykit
