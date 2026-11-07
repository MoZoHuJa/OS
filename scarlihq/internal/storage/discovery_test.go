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
	// Capacity from the FIRST mount (/): 1000*4096.
	if nvme.CapacityBytes != 1000*4096 {
		t.Errorf("nvme capacity=%d, want %d", nvme.CapacityBytes, 1000*4096)
	}
	if nvme.AvailableBytes != 400*4096 {
		t.Errorf("nvme available=%d, want %d", nvme.AvailableBytes, 400*4096)
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
