package durability

import "testing"

// Real mount lines, including the two shapes the optional fields take: present
// (shared:N) and absent.
const sampleMountInfo = `25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
27 30 0:25 / /dev/shm rw,nosuid,nodev shared:14 - tmpfs tmpfs rw
30 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
100 30 0:52 / /mnt/share rw,relatime shared:60 - nfs4 10.0.0.1:/export rw,vers=4.2
120 30 0:60 / /mnt/remote rw,nosuid,nodev,relatime - fuse.sshfs user@host:/ rw,user_id=1000
140 30 0:5 / /var/lib/docker/overlay rw,relatime shared:70 - overlay overlay rw,lowerdir=/a,upperdir=/b
160 30 0:39 / /mnt/c rw,relatime - 9p C:\134 rw,dirsync,aname=drvfs
180 30 0:71 / /mnt/lab rw,relatime shared:80 - weirdfs /dev/weird rw
`

func TestMountTypeForDevice(t *testing.T) {
	cases := map[string]struct {
		major, minor uint32
		want         string
	}{
		"root filesystem":     {major: 8, minor: 1, want: "ext4"},
		"anonymous device":    {major: 0, minor: 52, want: "nfs4"},
		"without optional":    {major: 0, minor: 60, want: "fuse.sshfs"},
		"escaped mount point": {major: 0, minor: 39, want: "9p"},
		"absent device":       {major: 253, minor: 7, want: ""},
	}

	for name, testCase := range cases {
		if got := mountTypeForDevice([]byte(sampleMountInfo), testCase.major, testCase.minor); got != testCase.want {
			t.Errorf("%s: %d:%d resolved to %q, want %q", name, testCase.major, testCase.minor, got, testCase.want)
		}
	}
}

func TestClassifyMountType(t *testing.T) {
	cases := map[string]struct {
		network  bool
		volatile bool
		known    bool
	}{
		"ext4":       {known: true},
		"btrfs":      {known: true},
		"nfs4":       {network: true, known: true},
		"cifs":       {network: true, known: true},
		"9p":         {network: true, known: true},
		"tmpfs":      {volatile: true, known: true},
		"overlay":    {volatile: true, known: true},
		"fuse.sshfs": {network: true, known: true},
		"fuseblk":    {network: true, known: true},
		// A name the kernel gave us and no list here knows is refused rather
		// than assumed local, which is the whole reason for reading names.
		"weirdfs": {},
	}

	for name, want := range cases {
		got, known := classifyMountType(name)
		if known != want.known {
			t.Errorf("%s: known=%v, want %v", name, known, want.known)
		}
		if got.name != name {
			t.Errorf("%s: reported name %q", name, got.name)
		}
		if got.network != want.network || got.volatile != want.volatile {
			t.Errorf("%s: network=%v volatile=%v, want %v and %v", name, got.network, got.volatile, want.network, want.volatile)
		}
	}
}

// The encoding spreads both numbers across the word, so the low bits alone
// would mismatch every device numbered above 255.
func TestLinuxDeviceNumbers(t *testing.T) {
	cases := map[uint64]struct{ major, minor uint32 }{
		0x00801:        {major: 8, minor: 1},
		0x0002a:        {major: 0, minor: 42},
		0xfd003:        {major: 0xfd0, minor: 3},
		0x100100:       {major: 1, minor: 256},
		0x100000000105: {major: 0x1001, minor: 5},
	}

	for device, want := range cases {
		major, minor := linuxDeviceNumbers(device)
		if major != want.major || minor != want.minor {
			t.Errorf("device %#x split to %d:%d, want %d:%d", device, major, minor, want.major, want.minor)
		}
	}
}
