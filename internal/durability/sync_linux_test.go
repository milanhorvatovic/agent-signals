package durability

import "testing"

// Sign extension on 32-bit kernels was the defect: Statfs_t.Type is signed
// there, so a magic with the high bit set arrived negative, widened past every
// table key, and left CIFS looking like an unknown local filesystem. The three
// magics below are the ones the bug reached; taking a narrowed uint32 also
// pins the signature, so a return to a widened signed lookup fails to compile.
func TestHighBitMagicsClassifyThroughANarrowedValue(t *testing.T) {
	cases := map[string]struct {
		magic   uint32
		network bool
	}{
		"cifs":  {magic: 0xff534d42, network: true},
		"btrfs": {magic: 0x9123683e},
		"ramfs": {magic: 0x858458f6},
	}

	for name, testCase := range cases {
		// What a 32-bit kernel reports, and what the call site narrows back.
		signed := int32(testCase.magic)
		got := filesystemForMagic(uint32(signed))

		if got.name != name {
			t.Errorf("%s magic resolved to %q", name, got.name)
		}
		if got.network != testCase.network {
			t.Errorf("%s classified network=%v, want %v", name, got.network, testCase.network)
		}
	}
}
