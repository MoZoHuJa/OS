package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---- parsing helpers ---------------------------------------------------------

func TestParseMounts(t *testing.T) {
	raw := `/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/nvme0n1p1 /boot vfat rw,utf8 0 0
tmpfs /run tmpfs rw,nosuid,nodev 0 0
server:/export /mnt/nfs nfs4 ro,vers=4.2 0 0
/dev/mapper/vg0-lv0 /home ext4 rw 0 0
/dev/sda1 /data\040store ext4 rw 0 0
`
	mounts := parseMounts(raw)
	if len(mounts) != 6 {
		t.Fatalf("parsed %d mounts, want 6", len(mounts))
	}
	if mounts[0].Device != "/dev/nvme0n1p2" || mounts[0].MountPoint != "/" || mounts[0].FSType != "ext4" {
		t.Fatalf("first mount misparsed: %+v", mounts[0])
	}
	if mounts[3].FSType != "nfs4" || mounts[3].Device != "server:/export" {
		t.Fatalf("nfs mount misparsed: %+v", mounts[3])
	}
	if mounts[5].MountPoint != "/data store" {
		t.Fatalf("octal escape not unescaped: %q", mounts[5].MountPoint)
	}
}

func TestUnescapeMount(t *testing.T) {
	cases := map[string]string{
		`/plain`:         "/plain",
		`/data\040store`: "/data store",
		`/a\011b`:        "/a\tb",
		`/a\134b`:        "/a\\b",
		`/a\012b`:        "/a\nb",
	}
	for in, want := range cases {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestPartitionParent(t *testing.T) {
	cases := map[string]string{
		"/dev/nvme0n1p2": "nvme0n1",
		"/dev/nvme1n1":   "nvme1n1",
		"/dev/sda3":      "sda",
		"/dev/sda":       "sda",
		"/dev/mmcblk0p1": "mmcblk0",
		"/dev/vda2":      "vda",
		"/dev/dm-0":      "dm-0",
	}
	for in, want := range cases {
		if got := partitionParent(in); got != want {
			t.Errorf("partitionParent(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestNvmeControllerName(t *testing.T) {
	cases := map[string]string{
		"nvme0n1":   "nvme0",
		"nvme0n1p2": "nvme0",
		"nvme1n1":   "nvme1",
		"nvme12n3":  "nvme12",
	}
	for in, want := range cases {
		if got := nvmeControllerName(in); got != want {
			t.Errorf("nvmeControllerName(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	raw := `MemTotal:       16384000 kB
MemFree:          123456 kB
MemAvailable:    8192000 kB
Buffers:           65536 kB
SwapTotal:        4194304 kB
SwapFree:         4194304 kB
`
	f := parseMeminfo(raw)
	if f["memtotal"] != 16384000*1024 {
		t.Errorf("memtotal=%d, want %d", f["memtotal"], 16384000*1024)
	}
	if f["memavailable"] != 8192000*1024 {
		t.Errorf("memavailable=%d, want %d", f["memavailable"], 8192000*1024)
	}
	if f["swaptotal"] != 4194304*1024 {
		t.Errorf("swaptotal=%d, want %d", f["swaptotal"], 4194304*1024)
	}
}

func TestParseCPUModel(t *testing.T) {
	raw := `processor       : 0
vendor_id       : GenuineIntel
model name      : AMD Ryzen 9 5900X 12-Core Processor
flags           : avx2
processor       : 1
model name      : AMD Ryzen 9 5900X 12-Core Processor
`
	if got := parseCPUModel(raw); got != "AMD Ryzen 9 5900X 12-Core Processor" {
		t.Errorf("cpu model=%q", got)
	}
	if got := parseCPUModel("garbage\nno colon lines\n"); got != "" {
		t.Errorf("cpu model from garbage=%q, want empty", got)
	}
}

// ---- CollectDevices (fixture-backed seams) ----------------------------------

// withSeams swaps every discovery I/O seam to the fixtures and restores
// them on cleanup. Tests execute ZERO real syscalls this way.
func withSeams(t *testing.T, mounts string, files map[string]string, dirs map[string][]string,
	statfsResults map[string]syscall.Statfs_t, symlinks map[string]string, statfsErr map[string]error) {
	t.Helper()
	origRead, origDir, origStatfs, origEval, origNow := readFileFn, readDirFn, statfsFn, evalSymlinksFn, nowFn
	fixed := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	readFileFn = func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}
		return nil, os.ErrNotExist
	}
	readDirFn = func(p string) ([]os.DirEntry, error) {
		names, ok := dirs[p]
		if !ok {
			return nil, os.ErrNotExist
		}
		out := make([]os.DirEntry, 0, len(names))
		for _, n := range names {
			out = append(out, fakeDirEntry(n))
		}
		return out, nil
	}
	statfsFn = func(p string, st *syscall.Statfs_t) error {
		if e, ok := statfsErr[p]; ok {
			return e
		}
		v, ok := statfsResults[p]
		if !ok {
			return os.ErrNotExist
		}
		*st = v
		return nil
	}
	evalSymlinksFn = func(p string) (string, error) {
		if v, ok := symlinks[p]; ok {
			return v, nil
		}
		return "", os.ErrNotExist
	}
	nowFn = func() time.Time { return fixed }
	t.Cleanup(func() {
		readFileFn, readDirFn, statfsFn, evalSymlinksFn, nowFn = origRead, origDir, origStatfs, origEval, origNow
	})
	_ = mounts // mounts supplied via files["/proc/self/mounts"] by callers usually
}

// fakeDirEntry is a minimal DirEntry for seam fixtures.
type fakeDirEntry string

func (f fakeDirEntry) Name() string { return string(f) }
func (f fakeDirEntry) IsDir() bool  { return false }
func (f fakeDirEntry) Type() os.FileMode {
	return 0
}
func (f fakeDirEntry) Info() (os.FileInfo, error) { return nil, os.ErrNotExist }

func TestCollectDevicesMountDriven(t *testing.T) {
	statfsRoot := syscall.Statfs_t{Blocks: 1000, Bsize: 4096, Bavail: 400}
	statfsData := syscall.Statfs_t{Blocks: 2000, Bsize: 4096, Bavail: 1500}
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/nvme0n1p3 /data ext4 rw,relatime 0 0
/dev/sda1 /backup ext4 ro,relatime 0 0
proc /proc proc rw 0 0
tmpfs /run tmpfs rw 0 0
server:/export /mnt/remote nfs4 ro 0 0
`,
		"/sys/class/nvme/nvme0/model":  "Samsung SSD 990 PRO",
		"/sys/class/nvme/nvme0/serial": "S6Z1NJ0R123456",
	}, map[string][]string{"/sys/block": {"nvme0n1", "sda", "loop0", "dm-0"}},
		map[string]syscall.Statfs_t{"/": statfsRoot, "/data": statfsData, "/backup": statfsRoot, "/mnt/remote": statfsData},
		nil, nil)

	devices, statuses := CollectDevices()
	if len(devices) != 3 {
		t.Fatalf("devices=%d, want 3 (nvme0n1, sda, network): %+v", len(devices), devices)
	}

	nvme := devices[0]
	if nvme.Kind != DeviceNVMe {
		t.Errorf("nvme kind=%q, want nvme", nvme.Kind)
	}
	if nvme.Model != "Samsung SSD 990 PRO" {
		t.Errorf("nvme model=%q", nvme.Model)
	}
	if nvme.Filesystem != "ext4" {
		t.Errorf("nvme fs=%q", nvme.Filesystem)
	}
	if len(nvme.MountPoints) != 2 || nvme.MountPoints[0] != "/" || nvme.MountPoints[1] != "/data" {
		t.Errorf("nvme mounts=%v, want [/ /data]", nvme.MountPoints)
	}
	// v19.7.1 (KPK-19.7.0-P2-01): capacity is the AGGREGATE across the
	// device's DISTINCT filesystems — / (1000 blocks) + /data (2000
	// blocks), each counted once. The old release reported only the
	// first mount's 1000*4096, silently losing /data's capacity.
	if nvme.CapacityBytes != (1000+2000)*4096 {
		t.Errorf("nvme capacity=%d, want %d (both partitions' filesystems)", nvme.CapacityBytes, (1000+2000)*4096)
	}
	if nvme.AvailableBytes != (400+1500)*4096 {
		t.Errorf("nvme available=%d, want %d", nvme.AvailableBytes, (400+1500)*4096)
	}
	// Per-filesystem view: BOTH partitions represented with their own
	// figures (the P2-01 acceptance test).
	if len(nvme.Filesystems) != 2 {
		t.Fatalf("nvme filesystems=%d, want 2 (the two partitions): %+v", len(nvme.Filesystems), nvme.Filesystems)
	}
	if nvme.Filesystems[0].Source != "/dev/nvme0n1p2" || nvme.Filesystems[0].MountPoints[0] != "/" {
		t.Errorf("fs[0] source=%q mounts=%v", nvme.Filesystems[0].Source, nvme.Filesystems[0].MountPoints)
	}
	if nvme.Filesystems[1].Source != "/dev/nvme0n1p3" || nvme.Filesystems[1].MountPoints[0] != "/data" {
		t.Errorf("fs[1] source=%q mounts=%v", nvme.Filesystems[1].Source, nvme.Filesystems[1].MountPoints)
	}
	if nvme.Filesystems[0].CapacityBytes != 1000*4096 || nvme.Filesystems[1].CapacityBytes != 2000*4096 {
		t.Errorf("fs capacities=%d/%d, want %d/%d", nvme.Filesystems[0].CapacityBytes, nvme.Filesystems[1].CapacityBytes, 1000*4096, 2000*4096)
	}
	if nvme.Filesystems[0].AvailableBytes != 400*4096 || nvme.Filesystems[1].AvailableBytes != 1500*4096 {
		t.Errorf("fs available=%d/%d, want %d/%d", nvme.Filesystems[0].AvailableBytes, nvme.Filesystems[1].AvailableBytes, 400*4096, 1500*4096)
	}
	if !nvme.Filesystems[0].Healthy || !nvme.Filesystems[1].Healthy {
		t.Errorf("fs healthy=%v/%v, want both true", nvme.Filesystems[0].Healthy, nvme.Filesystems[1].Healthy)
	}
	if nvme.Filesystems == nil || nvme.Filesystems[0].Sources == nil || nvme.Filesystems[0].MountPoints == nil {
		t.Errorf("filesystems/sources/mount_points must be non-nil (the []-not-null contract)")
	}
	// Mixed rw (/ and /data) + ro (/backup is sda, not nvme) → not ro.
	if nvme.ReadOnly {
		t.Errorf("nvme read_only=true, want false (both mounts rw)")
	}
	if !nvme.Healthy {
		t.Errorf("nvme healthy=false, want true")
	}
	if nvme.StableID == "" {
		t.Errorf("nvme stable_id empty — identity material was available")
	}
	if strings.Contains(nvme.StableID, "S6Z1NJ0R123456") {
		t.Errorf("stable_id leaks raw serial: %q", nvme.StableID)
	}
	// Temperature unknown — must be nil AND omitted from JSON.
	if nvme.TemperatureC != nil {
		t.Errorf("temperature must be nil (not measured), got %v", *nvme.TemperatureC)
	}
	if nvme.ReadProfile != nil {
		t.Errorf("discovery must never populate ReadProfile (nil = never measured), got %+v", nvme.ReadProfile)
	}

	sda := devices[1]
	if sda.Kind != DeviceSATA && sda.Kind != DeviceUSB && sda.Kind != DeviceUnknown {
		t.Errorf("sda kind=%q — outside the honest set", sda.Kind)
	}
	if !sda.ReadOnly {
		t.Errorf("sda read_only=false, want true (single ro mount)")
	}

	net := devices[2]
	if net.Kind != DeviceNetwork {
		t.Errorf("network kind=%q, want network", net.Kind)
	}
	if net.Filesystem != "nfs4" {
		t.Errorf("network fs=%q", net.Filesystem)
	}
	if net.CapacityBytes != 2000*4096 {
		t.Errorf("network capacity=%d", net.CapacityBytes)
	}
	if net.ReadOnly != true {
		t.Errorf("network read_only=%v, want true (ro in opts)", net.ReadOnly)
	}
	if len(net.Filesystems) != 1 || net.Filesystems[0].MountPoints[0] != "/mnt/remote" || !net.Filesystems[0].ReadOnly {
		t.Errorf("network filesystems=%+v, want one ro entry for /mnt/remote", net.Filesystems)
	}

	if statuses == nil {
		t.Fatalf("statuses nil")
	}
}

func TestCollectDevicesUnmountedRawCapacity(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts":           `/dev/nvme0n1p2 / ext4 rw 0 0`,
		"/sys/block/sdb/size":         "1953525168", // ~1TB in 512B sectors
		"/sys/block/sdb/device/model": "WDC WD10EZEX",
	}, map[string][]string{"/sys/block": {"nvme0n1", "sdb", "loop0"}},
		map[string]syscall.Statfs_t{"/": {Blocks: 100, Bsize: 4096, Bavail: 50}},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 2 {
		t.Fatalf("devices=%d, want 2", len(devices))
	}
	sdb := devices[1]
	if sdb.CapacityBytes != 1953525168*512 {
		t.Errorf("unmounted raw capacity=%d, want %d", sdb.CapacityBytes, 1953525168*512)
	}
	if sdb.AvailableBytes != 0 {
		t.Errorf("unmounted available=%d, want 0 (no filesystem → free space unobservable)", sdb.AvailableBytes)
	}
	if sdb.Filesystem != "" {
		t.Errorf("unmounted fs=%q, want empty", sdb.Filesystem)
	}
	if len(sdb.MountPoints) != 0 {
		t.Errorf("unmounted mount_points=%v, want empty non-nil", sdb.MountPoints)
	}
	if sdb.MountPoints == nil {
		t.Errorf("mount_points nil — must be non-nil per the []-not-null contract")
	}
}

func TestCollectDevicesUSBTransport(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/sdb1 /media/usb ext4 rw 0 0`,
	}, map[string][]string{"/sys/block": {"sdb"}},
		map[string]syscall.Statfs_t{"/media/usb": {Blocks: 100, Bsize: 4096, Bavail: 90}},
		map[string]string{"/sys/block/sdb/device": "/sys/devices/pci0000:00/0000:00:14.0/usb3/3-2/3-2:1.0/host6/target6:0:0/6:0:0:0"},
		nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d", len(devices))
	}
	if devices[0].Kind != DeviceUSB {
		t.Errorf("kind=%q, want usb (device path contains /usb)", devices[0].Kind)
	}
}

func TestCollectDevicesUnavailableMounts(t *testing.T) {
	// /proc/self/mounts unreadable → empty devices + "unavailable" status.
	withSeams(t, "", map[string]string{}, nil, nil, nil, nil)
	devices, statuses := CollectDevices()
	if len(devices) != 0 {
		t.Fatalf("devices=%d, want 0", len(devices))
	}
	if devices == nil {
		t.Fatalf("devices nil — must be non-nil")
	}
	if len(statuses) == 0 {
		t.Fatalf("statuses empty — the unavailable source must be named")
	}
	if statuses[0].Status != "unavailable" {
		t.Errorf("status=%q, want unavailable", statuses[0].Status)
	}
}

// TestCollectDevicesBindMountNoDoubleCount (KPK-19.7.0-P2-01 acceptance):
// the SAME filesystem mounted at several points (bind mounts / btrfs
// subvolumes share the mount source) is ONE Filesystem entry — capacity
// counted ONCE, every mount point listed.
func TestCollectDevicesBindMountNoDoubleCount(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/nvme0n1p2 / btrfs rw,relatime,ssd 0 0
/dev/nvme0n1p2 /home btrfs rw,relatime,ssd,subvol=@home 0 0
/dev/nvme0n1p2 /mnt/bind btrfs rw,relatime,ssd,subvol=@home,bind 0 0
`,
	}, map[string][]string{"/sys/block": {"nvme0n1"}},
		map[string]syscall.Statfs_t{"/": {Blocks: 500, Bsize: 4096, Bavail: 250}},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1", len(devices))
	}
	d := devices[0]
	if len(d.Filesystems) != 1 {
		t.Fatalf("filesystems=%d, want 1 (all three mounts are ONE btrfs): %+v", len(d.Filesystems), d.Filesystems)
	}
	if d.CapacityBytes != 500*4096 {
		t.Errorf("capacity=%d, want %d (counted ONCE — no double counting)", d.CapacityBytes, 500*4096)
	}
	if d.AvailableBytes != 250*4096 {
		t.Errorf("available=%d, want %d", d.AvailableBytes, 250*4096)
	}
	if len(d.Filesystems[0].MountPoints) != 3 {
		t.Errorf("fs mount_points=%v, want all three", d.Filesystems[0].MountPoints)
	}
	if len(d.MountPoints) != 3 {
		t.Errorf("device mount_points=%v, want all three", d.MountPoints)
	}
	if d.ReadOnly {
		t.Errorf("read_only=true, want false (all mounts rw)")
	}
}

// TestCollectDevicesFSTypesNotCollapsed (KPK-19.7.0-P2-01 acceptance):
// different filesystem types on one physical disk are NOT silently
// collapsed into the first filesystem's type — each entry keeps its own.
func TestCollectDevicesFSTypesNotCollapsed(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/sda2 / ext4 rw,relatime 0 0
/dev/sda3 /data btrfs rw,relatime,ssd 0 0
`,
	}, map[string][]string{"/sys/block": {"sda"}},
		map[string]syscall.Statfs_t{
			"/":     {Blocks: 100, Bsize: 4096, Bavail: 40},
			"/data": {Blocks: 700, Bsize: 4096, Bavail: 300},
		},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1", len(devices))
	}
	d := devices[0]
	if len(d.Filesystems) != 2 {
		t.Fatalf("filesystems=%d, want 2: %+v", len(d.Filesystems), d.Filesystems)
	}
	if d.Filesystems[0].FSType != "ext4" || d.Filesystems[1].FSType != "btrfs" {
		t.Errorf("fs types=%q/%q, want ext4/btrfs (not collapsed)", d.Filesystems[0].FSType, d.Filesystems[1].FSType)
	}
	if d.CapacityBytes != (100+700)*4096 {
		t.Errorf("capacity=%d, want %d", d.CapacityBytes, (100+700)*4096)
	}
}

// TestCollectDevicesPartialStatfs (KPK-19.7.0-P2-01 acceptance): a failed
// statfs for ONE partition marks that partition partial WITHOUT erasing
// the others — the healthy filesystem keeps its figures, the device
// carries the degraded state, a partial source status names the failure.
func TestCollectDevicesPartialStatfs(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/nvme0n1p3 /data ext4 rw,relatime 0 0
`,
	}, map[string][]string{"/sys/block": {"nvme0n1"}},
		map[string]syscall.Statfs_t{"/": {Blocks: 1000, Bsize: 4096, Bavail: 400}},
		nil,
		map[string]error{"/data": os.ErrPermission})

	devices, statuses := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1", len(devices))
	}
	d := devices[0]
	if len(d.Filesystems) != 2 {
		t.Fatalf("filesystems=%d, want 2 (the failed one is PRESENT, not erased): %+v", len(d.Filesystems), d.Filesystems)
	}
	if !d.Filesystems[0].Healthy || d.Filesystems[0].CapacityBytes != 1000*4096 {
		t.Errorf("fs[0] healthy=%v capacity=%d — the healthy partition must keep its figures", d.Filesystems[0].Healthy, d.Filesystems[0].CapacityBytes)
	}
	if d.Filesystems[1].Healthy || d.Filesystems[1].CapacityBytes != 0 {
		t.Errorf("fs[1] healthy=%v capacity=%d — the failed partition must be degraded/unknown", d.Filesystems[1].Healthy, d.Filesystems[1].CapacityBytes)
	}
	if d.CapacityBytes != 1000*4096 {
		t.Errorf("device capacity=%d, want only the healthy filesystem's %d", d.CapacityBytes, 1000*4096)
	}
	if d.Healthy {
		t.Errorf("device healthy=true, want false (one filesystem degraded)")
	}
	var foundPartial bool
	for _, s := range statuses {
		if s.Status == "partial" && strings.Contains(s.Source, "statfs:/data") {
			foundPartial = true
		}
	}
	if !foundPartial {
		t.Errorf("no partial source status names the failed statfs: %+v", statuses)
	}
}

// TestMountReadOnlyExactToken (KPK-19.7.0-P3-05 acceptance): mount-option
// tokens are compared EXACTLY — "errors=remount-ro" is a read-write mount;
// option ordering does not change the result.
func TestMountReadOnlyExactToken(t *testing.T) {
	cases := []struct {
		opts string
		want bool
	}{
		{"rw,relatime,errors=remount-ro,commit=60", false},
		{"rw,errors=remount-ro", false},
		{"errors=remount-ro,rw", false},
		{"ro,relatime", true},
		{"relatime,ro", true},
		{"ro", true},
		{"rw,relatime", false},
		{"rw", false},
	}
	for _, c := range cases {
		if got := mountIsReadOnly(c.opts); got != c.want {
			t.Errorf("mountIsReadOnly(%q)=%v, want %v", c.opts, got, c.want)
		}
	}
}

// TestCollectDevicesRemountRONotReadOnly (KPK-19.7.0-P3-05): the local
// device path uses the exact token parse — an errors=remount-ro root
// mount is NOT a read-only device (the old substring check
// false-positived on "ro," inside "remount-ro,").
func TestCollectDevicesRemountRONotReadOnly(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/sda2 / ext4 rw,relatime,errors=remount-ro,commit=60 0 0
`,
	}, map[string][]string{"/sys/block": {"sda"}},
		map[string]syscall.Statfs_t{"/": {Blocks: 100, Bsize: 4096, Bavail: 40}},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1", len(devices))
	}
	if devices[0].ReadOnly {
		t.Errorf("read_only=true for a rw,errors=remount-ro mount — the exact-token parse must say false")
	}
	if len(devices[0].Filesystems) != 1 || devices[0].Filesystems[0].ReadOnly {
		t.Errorf("filesystem read_only must be false for errors=remount-ro")
	}
}

// TestCollectDevicesMixedROMounts (KPK-19.7.0-P3-05 acceptance): mixed
// ro and rw mount points on one device → device-level read_only=false,
// with per-filesystem states preserved.
func TestCollectDevicesMixedROMounts(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/sda2 / ext4 rw,relatime 0 0
/dev/sda3 /archive ext4 ro,relatime 0 0
`,
	}, map[string][]string{"/sys/block": {"sda"}},
		map[string]syscall.Statfs_t{
			"/":        {Blocks: 100, Bsize: 4096, Bavail: 40},
			"/archive": {Blocks: 100, Bsize: 4096, Bavail: 40},
		},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1", len(devices))
	}
	d := devices[0]
	if d.ReadOnly {
		t.Errorf("device read_only=true, want false (mixed ro/rw)")
	}
	if len(d.Filesystems) != 2 || d.Filesystems[0].ReadOnly || !d.Filesystems[1].ReadOnly {
		t.Errorf("per-filesystem ro=%v/%v, want false/true (per-fs states preserved)", d.Filesystems[0].ReadOnly, d.Filesystems[1].ReadOnly)
	}
}

// TestCollectDevicesFSIDAliasMerge (KPK-19.7.0-P2-01): two different
// source paths with ONE non-zero statfs fsid under one parent device are
// one filesystem observed twice — merged, capacity counted once.
func TestCollectDevicesFSIDAliasMerge(t *testing.T) {
	st := syscall.Statfs_t{Blocks: 300, Bsize: 4096, Bavail: 100, Fsid: syscall.Fsid{X__val: [2]int32{0x1234, 0x5678}}}
	withSeams(t, "", map[string]string{
		"/proc/self/mounts": `/dev/sda1 /primary ext4 rw,relatime 0 0
/dev/sda /alias ext4 rw,relatime 0 0
`,
	}, map[string][]string{"/sys/block": {"sda"}},
		map[string]syscall.Statfs_t{"/primary": st, "/alias": st},
		nil, nil)

	devices, _ := CollectDevices()
	if len(devices) != 1 {
		t.Fatalf("devices=%d, want 1 (both sources parent to sda)", len(devices))
	}
	d := devices[0]
	if len(d.Filesystems) != 1 {
		t.Fatalf("filesystems=%d, want 1 (fsid alias merged): %+v", len(d.Filesystems), d.Filesystems)
	}
	if d.CapacityBytes != 300*4096 {
		t.Errorf("capacity=%d, want %d (the aliased filesystem counted ONCE)", d.CapacityBytes, 300*4096)
	}
	if len(d.Filesystems[0].MountPoints) != 2 || len(d.Filesystems[0].Sources) != 2 {
		t.Errorf("fs mounts/sources=%v/%v, want both recorded", d.Filesystems[0].MountPoints, d.Filesystems[0].Sources)
	}
}

func TestCollectHostResources(t *testing.T) {
	withSeams(t, "", map[string]string{
		"/proc/cpuinfo": "model name\t: Test CPU X9\n",
		"/proc/meminfo": "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 100 kB\nSwapFree: 90 kB\n",
	}, nil, nil, nil, nil)
	origCPU := numCPUFn
	numCPUFn = func() int { return 12 }
	t.Cleanup(func() { numCPUFn = origCPU })

	res, statuses := CollectHostResources()
	if res.CPUThreads != 12 {
		t.Errorf("cpu_threads=%d", res.CPUThreads)
	}
	if res.CPUModel != "Test CPU X9" {
		t.Errorf("cpu_model=%q", res.CPUModel)
	}
	if res.RAMTotalBytes != 1000*1024 || res.RAMAvailableBytes != 500*1024 {
		t.Errorf("ram total/avail=%d/%d", res.RAMTotalBytes, res.RAMAvailableBytes)
	}
	if res.SwapTotalBytes != 100*1024 || res.SwapFreeBytes != 90*1024 {
		t.Errorf("swap total/free=%d/%d", res.SwapTotalBytes, res.SwapFreeBytes)
	}
	if res.RAMPinnedBytes != 0 {
		t.Errorf("ram_pinned=%d — Phase 1 reports none (documented honest unknown)", res.RAMPinnedBytes)
	}
	if len(statuses) == 0 || statuses[len(statuses)-1].Status != "ok" {
		t.Errorf("overall host status not ok: %+v", statuses)
	}
}

func TestCollectHostResourcesPartial(t *testing.T) {
	// /proc/meminfo unreadable → partial status, zero RAM figures carried honestly.
	withSeams(t, "", map[string]string{
		"/proc/cpuinfo": "model name\t: Test CPU\n",
	}, nil, nil, nil, nil)
	res, statuses := CollectHostResources()
	if res.RAMTotalBytes != 0 {
		t.Errorf("ram_total=%d, want 0 (source unavailable)", res.RAMTotalBytes)
	}
	found := false
	for _, s := range statuses {
		if s.Status == "unavailable" && s.Source == "/proc/meminfo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("meminfo unavailable status missing: %+v", statuses)
	}
}

// ---- the read-only charter (source-level assertion) --------------------------

// stripGoComments removes //-comments per line (the smoke check's
// _strip_go_comments discipline: prose cannot mask code).
func stripGoComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "//"); j >= 0 {
			lines[i] = l[:j]
		}
	}
	return strings.Join(lines, "\n")
}

// TestDiscoveryCharterNoExec asserts the READ-ONLY charter at the source
// level: the discovery file must not import os/exec nor reference any
// process-spawning symbol. Prose cannot mask code — comments are
// stripped before the assertion.
func TestDiscoveryCharterNoExec(t *testing.T) {
	for _, f := range []string{"discovery.go", "storage.go"} {
		raw, err := os.ReadFile(filepath.Join(".", f))
		if err != nil {
			t.Fatalf("cannot read %s: %v", f, err)
		}
		src := stripGoComments(string(raw))
		if strings.Contains(src, "os/exec") {
			t.Errorf("%s imports os/exec — discovery must never fork (read-only charter)", f)
		}
		if strings.Contains(src, "exec.Command") {
			t.Errorf("%s references exec.Command — discovery must never fork (read-only charter)", f)
		}
	}
}

// ---- JSON wire contract ------------------------------------------------------

func TestDeviceJSONContract(t *testing.T) {
	d := Device{
		ID:          "storage.nvme.0",
		Kind:        DeviceNVMe,
		MountPoints: []string{"/"},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	// temperature_c must be ABSENT when unknown (omitempty on pointer).
	if _, ok := m["temperature_c"]; ok {
		t.Errorf("temperature_c present with nil pointer — unknown must be omitted, got %s", raw)
	}
	// mount_points must be [] not null.
	if _, ok := m["mount_points"]; !ok {
		t.Errorf("mount_points key missing: %s", raw)
	}
	// kind is a bare string.
	if m["kind"] != "nvme" {
		t.Errorf("kind=%v, want nvme", m["kind"])
	}
}
