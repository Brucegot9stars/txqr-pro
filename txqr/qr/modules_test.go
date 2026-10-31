package qr

import (
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

// TestModulesForBytesMatchesEncoder keeps the capacity table honest: the
// receiver uses it to tell the user how large a capture region has to be, and a
// wrong table would give confident, wrong advice.
func TestModulesForBytesMatchesEncoder(t *testing.T) {
	for _, n := range []int{532, 1045, 1557, 2070, 2300} {
		q, err := qrcode.New(strings.Repeat("x", n), qrcode.Medium)
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		if got, want := ModulesForBytes(n), len(q.Bitmap()); got != want {
			t.Errorf("ModulesForBytes(%d) = %d, encoder produced %d", n, got, want)
		}
	}
	// One byte past the last version's capacity.
	if got := ModulesForBytes(2332); got != 0 {
		t.Errorf("ModulesForBytes(2332) = %d, want 0 (no QR version fits)", got)
	}
}
