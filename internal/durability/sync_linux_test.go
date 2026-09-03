package durability

import "testing"

// Sign extension on 32-bit kernels was the defect: Statfs_t.Type is signed
// there, so a magic with the high bit set arrived negative, widened past every
// table key, and left CIFS looking like an unknown local filesystem. The three
// magics below are the ones the bug reached; taking a narrowed uint32 also
// pins the signature, so a return to a widened signed lookup fails to compile.
func TestHighBitMagicsClassifyThroughANarrowedValue(t *testing.T) {
	cases := map[string]struct {
		magic    uint32
		network  bool
		volatile bool
	}{
		"cifs":  {magic: 0xff534d42, network: true},
		"smb2":  {magic: 0xfe534d42, network: true},
		"btrfs": {magic: 0x9123683e},
		"ramfs": {magic: 0x858458f6, volatile: true},
	}

	for name, testCase := range cases {
		// What a 32-bit kernel reports, and what the call site narrows back.
		signed := int32(testCase.magic)
		got := filesystemForMagic(uint32(signed))

		if got.name != name {
			t.Errorf("magic %#x resolved to %q, want %q", testCase.magic, got.name, name)
		}
		if got.network != testCase.network {
			t.Errorf("%s classified network=%v, want %v", name, got.network, testCase.network)
		}
		if got.volatile != testCase.volatile {
			t.Errorf("%s classified volatile=%v, want %v", name, got.volatile, testCase.volatile)
		}
	}
}

// An overlay mount can be backed by tmpfs and statfs cannot see through to the
// layers, so the mode it can honestly advertise is the weaker one.
func TestOverlayIsCappedAtTheWeakerMode(t *testing.T) {
	overlay := filesystemForMagic(0x794c7630)

	if overlay.name != "overlay" {
		t.Fatalf("overlay magic resolved to %q, want \"overlay\"", overlay.name)
	}
	if !overlay.volatile {
		t.Error("overlay is trusted for host-loss durability despite an unknown upper layer")
	}
}
