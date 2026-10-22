package engine

import (
	"testing"
	"unsafe"
)

// aggAccum is allocated per group per aggregate per extent; keep it at
// 56 bytes (see its doc comment).
func TestAggAccumSize(t *testing.T) {
	if s := unsafe.Sizeof(aggAccum{}); s != 56 {
		t.Errorf("aggAccum is %d bytes, want 56", s)
	}
}
