package stream

import "time"

// DefaultHeartbeatInterval is P23: inject a comment only after 15s of silence.
// A heartbeat inserted between data frames pollutes strict client parsers.
const DefaultHeartbeatInterval = 15 * time.Second

// KeepAliveFrame is the SSE comment frame. Clients ignore it; intermediaries
// such as Nginx use the bytes to keep the connection open.
func KeepAliveFrame() []byte { return []byte(": keep-alive\n\n") }
