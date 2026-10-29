package config

// T41 §4.4: idem.duplicate_window_seconds must accept 0 as the "off" switch
// for automatic de-duplication, while the 3600s upper bound stays closed.

import (
	"testing"

	"yunshu.dev/core/internal/jsonx"
)

func TestIdemDuplicateWindowAcceptsZeroKeepsUpperBound(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		// 0 reaches the P08 window <= 0 branch, which skips the stage.
		{`0`, false},
		{`1`, false},
		{`10`, false},
		// 361 is inside [0, 3600] and must stay accepted: the upper bound is
		// 3600, not 360.
		{`361`, false},
		{`3600`, false},
		{`3601`, true},
		{`-1`, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			err := Validate(KeyIdemDuplicateWindowSeconds, jsonx.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s=%s was accepted", KeyIdemDuplicateWindowSeconds, tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s=%s was rejected: %v", KeyIdemDuplicateWindowSeconds, tc.raw, err)
			}
		})
	}
}
