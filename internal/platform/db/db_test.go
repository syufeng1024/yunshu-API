package db

import (
	"testing"
	"time"
)

func TestGatewayChannelIsRestricted(t *testing.T) {
	d, err := OpenGateway("user:pass@tcp(127.0.0.1:1)/yunshu?parseTime=true", 8)
	if err != nil {
		t.Fatalf("OpenGateway: %v", err)
	}
	defer d.Close()

	if got := d.Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("MaxOpenConnections = %d, want 8", got)
	}
	if GatewayConnMaxLifetime != 5*time.Minute {
		t.Fatalf("GatewayConnMaxLifetime = %s, want 5m", GatewayConnMaxLifetime)
	}
	if GatewayTxnTimeout != 200*time.Millisecond {
		t.Fatalf("GatewayTxnTimeout = %s, want 200ms", GatewayTxnTimeout)
	}
}

func TestGatewayDefaultsToEightConnections(t *testing.T) {
	d, err := OpenGateway("user:pass@tcp(127.0.0.1:1)/yunshu?parseTime=true", 0)
	if err != nil {
		t.Fatalf("OpenGateway: %v", err)
	}
	defer d.Close()

	if got := d.Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("MaxOpenConnections = %d, want 8", got)
	}
}
