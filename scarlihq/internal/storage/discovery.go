// Package storage — device + host discovery (v19.7.0, roadmap §5.2/§5.3).
//
// This file implements the READ-ONLY discovery promised by the package
// charter: /proc/self/mounts for the mount table, /sys/block (+/sys/class
// /nvme) for hardware identity material, statfs(2) for filesystem
// capacity, /proc/cpuinfo + /proc/meminfo for host resources. No exec, no
// writes, no benchmarks — the benchmark lives in benchmark.go and is
// reached only through an explicit operator invocation.
//
// Test seams: the I/O entry points are package-level function variables
// (the same seam style the repo uses elsewhere — e.g. airt's injectable
// Prober). Discovery code itself never touches os/exec: a test that swaps
// every seam to fixtures executes zero syscalls with side effects, and a
// source scan can PROVE the read-only charter (see storage_test.go's
// TestDiscoveryCharterNoExecNoWrites and the smoke check).
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SourceStatus records how one inventory component was observed — the
// honest provenance the v2 snapshot carries (roadmap §5.6: "source/probe
// status"). Status is one of: "ok" (source read and parsed), "partial"
// (source read, some entries degraded — Detail says which), "empty"
// (source read, reported nothing — e.g. no GPUs), "unavailable" (source
// could not be read — Detail names the error).
type SourceStatus struct {
	Component string `json:"component"`        // "storage", "storage.host", ...
	Source    string `json:"source"`           // "/proc/self/mounts", "/proc/meminfo", ...
	Status    string `json:"status"`           // "ok" | "partial" | "empty" | "unavailable"
	Detail    string `json:"detail,omitempty"` // human-readable provenance/failure
}

// I/O seams (test injection). Production values are the direct stdlib
// calls; tests swap them for fixtures. Nothing outside this file and
// benchmark.go touches the filesystem on the discovery path.
var (
	readFileFn     = os.ReadFile           // reads /proc + /sys sources
	readDirFn      = os.ReadDir            // lists /sys/block
	statfsFn       = syscall.Statfs        // filesystem capacity
	evalSymlinksFn = filepath.EvalSymlinks // transport discrimination (/sys/block/<d>/device)
	numCPUFn       = runtime.NumCPU        // logical CPU count
	nowFn          = time.Now              // observation timestamps
)

// mountEntry is one parsed line of /proc/self/mounts.
type mountEntry struct {
	Device     string // "/dev/nvme0n1p2", "server:/export", "tmpfs", ...
	MountPoint string // unescaped mount point path
	FSType     string // "ext4", "nfs4", "tmpfs", ...
	Options    string // raw mount options (comma-separated)
	RawMount   string // the mount point exactly as it appeared (pre-unescape)
}

// skipFSTypes are pseudo/virtual filesystems that are not storage devices
// and are excluded from the storage inventory. The exclusion is a POLICY
// with a reason: these mounts have no persistent storage semantics for
// model placement (kernel APIs, memory-backed temporaries, container
// union layers). tmpfs is excluded deliberately: it is RAM, not storage —
// admitting it as "storage capacity" would be exactly the silent
// RAM-as-disk substitution the roadmap's honest-accounting rules forbid.
var skipFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "ramfs": true, "cgroup": true, "cgroup2": true,
	"pstore": true, "bpf": true, "tracefs": true, "debugfs": true,
	"securityfs": true, "configfs": true, "fusectl": true,
	"hugetlbfs": true, "mqueue": true, "rpc_pipefs": true,
	"autofs": true, "binfmt_misc": true, "efivarfs": true,
	"overlay": true, "squashfs": true, "iso9660": true, "udf": true,
	"cramfs": true, "erofs": true, "fuse.gvfsd-fuse": true,
	"fuse.portal": true, "nsfs": true, "virtiofs": true,
}

// networkFSTypes produce DeviceNetwork entries (the mount IS the device).
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smbfs": true, "smb2": true,
	"sshfs": true, "fuse.sshfs": true, "9p": true, "9pnet": true,
	"glusterfs": true, "ceph": true, "beegfs": true, "lustre": true,
	"afp": true, "davfs": true, "fuse.s3fs": true,
}

// sysfsSectorBytes — /sys/block/<dev>/size reports 512-byte sectors
// regardless of the device's actual logical block size (the kernel's
// historical contract for this file).
const sysfsSectorBytes = 512

// CollectDevices enumerates storage devices and network mounts, read-only.
//
// Algorithm (all sources read-only, see charter):
//  1. /proc/self/mounts → the mount table; virtual filesystems skipped
//     (skipFSTypes), network filesystems kept as their own devices.
//  2. Local block-device mounts are grouped onto their PARENT device
//     (partition → device: nvme0n1p2 → nvme0n1, sda3 → sda); statfs on
//     the first mount point provides filesystem capacity/available.
//  3. /sys/block is scanned for block devices WITHOUT mounts — an
//     unmounted device is real capacity (raw sysfs size), reported with
//     Filesystem="" and AvailableBytes=0 (no filesystem → no free-space
//     observable — honest, prevents phantom "free" devices).
//  4. Identity material (model/serial/transport) is read per device from
//     /sys/class/nvme or /sys/block/<dev>/device; StableID is a digest,
//     never a raw serial.
//
// Returns (devices, sourceStatuses) — devices is NEVER nil (the v1
// []-not-null contract extends to v2). A mounts-source failure yields
// empty devices + an "unavailable" status naming the error; a sysfs
// failure yields mount-driven devices + a "partial" status.
func CollectDevices() ([]Device, []SourceStatus) {
	var statuses []SourceStatus

	raw, err := readFileFn("/proc/self/mounts")
	if err != nil {
		// The mount table is the primary source; without it there is no
		// honest storage inventory. Fail to EMPTY + named status (never
		// panic, never fabricate).
		statuses = append(statuses, SourceStatus{
			Component: "storage",
			Source:    "/proc/self/mounts",
			Status:    "unavailable",
			Detail:    err.Error(),
		})
		return []Device{}, statuses
	}

	mounts := parseMounts(string(raw))
	if len(mounts) == 0 {
		statuses = append(statuses, SourceStatus{
			Component: "storage",
			Source:    "/proc/self/mounts",
			Status:    "empty",
			Detail:    "mount table parsed but carries no entries",
		})
		return []Device{}, statuses
	}

	now := nowFn().UTC()

	// ---- local block devices (mount-driven) --------------------------------
	// parentName → aggregated device under construction + mount entries.
	type devAgg struct {
		name      string
		mounts    []mountEntry
		capOK     bool
		capacity  uint64
		available uint64
		readOnly  bool
		roCount   int
		statErr   error
	}
	byParent := make(map[string]*devAgg)
	var order []string // first-seen order — stable output within a snapshot

	var networkMounts []mountEntry
	var statfsFailed int

	for _, m := range mounts {
		if skipFSTypes[m.FSType] {
			continue
		}
		if networkFSTypes[m.FSType] {
			networkMounts = append(networkMounts, m)
			continue
		}
		if !strings.HasPrefix(m.Device, "/dev/") {
			// Not a block-device mount and not a known network fstype
			// (e.g. a fuse.* variant not in the table). Skipped from the
			// device inventory — the source status reports the count so
			// the skip is visible, not silent.
			statfsFailed++ // reuse counter as "skipped-other" tally for the partial note
			continue
		}
		parent := partitionParent(m.Device)
		agg, ok := byParent[parent]
		if !ok {
			agg = &devAgg{name: parent}
			byParent[parent] = agg
			order = append(order, parent)
		}
		agg.mounts = append(agg.mounts, m)
	}

	out := make([]Device, 0, len(order)+len(networkMounts))

	for _, name := range order {
		agg := byParent[name]
		kind, model, serial := deviceIdentity(name)

		d := Device{
			ID:          deviceID(kind, name, len(out)),
			StableID:    stableDeviceID(kind, name, model, serial),
			Model:       model,
			Kind:        kind,
			MountPoints: make([]string, 0, len(agg.mounts)),
			ObservedAt:  now,
		}

		for _, m := range agg.mounts {
			d.MountPoints = append(d.MountPoints, m.MountPoint)
			if strings.Contains(m.Options, "ro,") || m.Options == "ro" ||
				strings.HasPrefix(m.Options, "ro,") {
				agg.roCount++
			}
		}
		d.ReadOnly = agg.roCount == len(agg.mounts) && len(agg.mounts) > 0

		// Filesystem facts from the FIRST mount point (statfs).
		if len(agg.mounts) > 0 {
			d.Filesystem = agg.mounts[0].FSType
			var st syscall.Statfs_t
			if err := statfsFn(agg.mounts[0].MountPoint, &st); err != nil {
				agg.statErr = err
				statuses = append(statuses, SourceStatus{
					Component: "storage",
					Source:    "statfs:" + agg.mounts[0].MountPoint,
					Status:    "partial",
					Detail:    fmt.Sprintf("device %s capacity unknown: %v", name, err),
				})
			} else {
				d.CapacityBytes = uint64(st.Blocks) * uint64(st.Bsize)
				d.AvailableBytes = uint64(st.Bavail) * uint64(st.Bsize)
				agg.capOK = true
			}
		}
		d.Healthy = (len(agg.mounts) > 0 && agg.capOK && d.CapacityBytes > 0) ||
			(len(agg.mounts) == 0)

		out = append(out, d)
	}

	// ---- unmounted block devices (sysfs-driven) ----------------------------
	// A device present in /sys/block with no mount is real hardware with
	// raw capacity (size*512) but NO filesystem — AvailableBytes stays 0
	// (no free-space observable without a filesystem; a planner must not
	// treat an unmounted device as writable capacity).
	sysEntries, sysErr := readDirFn("/sys/block")
	if sysErr != nil {
		statuses = append(statuses, SourceStatus{
			Component: "storage",
			Source:    "/sys/block",
			Status:    "partial",
			Detail:    fmt.Sprintf("unmounted-device scan skipped: %v", sysErr),
		})
	} else {
		var unmounted []string
		for _, e := range sysEntries {
			name := e.Name()
			if !isCandidateBlockDevice(name) {
				continue
			}
			if _, mounted := byParent[name]; mounted {
				continue // already inventoried through its mounts
			}
			unmounted = append(unmounted, name)

			kind, model, serial := deviceIdentity(name)
			d := Device{
				ID:          deviceID(kind, name, len(out)),
				StableID:    stableDeviceID(kind, name, model, serial),
				Model:       model,
				Kind:        kind,
				MountPoints: []string{},
				ObservedAt:  now,
				Healthy:     true, // hardware positively observed via sysfs
			}
			if sectors, err := readSysfsSize(name); err == nil {
				d.CapacityBytes = sectors * sysfsSectorBytes
			} else {
				statuses = append(statuses, SourceStatus{
					Component: "storage",
					Source:    "/sys/block/" + name + "/size",
					Status:    "partial",
					Detail:    fmt.Sprintf("unmounted device %s raw capacity unknown: %v", name, err),
				})
			}
			out = append(out, d)
		}
		if len(unmounted) == 0 && len(out) == 0 {
			// Mount table yielded nothing AND sysfs has no unmounted
			// candidates → genuinely empty (container without storage).
			statuses = append(statuses, SourceStatus{
				Component: "storage",
				Source:    "/proc/self/mounts+/sys/block",
				Status:    "empty",
				Detail:    "no block devices or network mounts observed",
			})
		}
	}

	// ---- network filesystem mounts ------------------------------------------
	for i, m := range networkMounts {
		d := Device{
			ID:          fmt.Sprintf("storage.network.%d", i),
			StableID:    stableNetworkID(m.FSType, m.Device),
			Kind:        DeviceNetwork,
			Filesystem:  m.FSType,
			MountPoints: []string{m.MountPoint},
			ReadOnly:    mountIsReadOnly(m.Options),
			ObservedAt:  now,
		}
		var st syscall.Statfs_t
		if err := statfsFn(m.MountPoint, &st); err == nil {
			d.CapacityBytes = uint64(st.Blocks) * uint64(st.Bsize)
			d.AvailableBytes = uint64(st.Bavail) * uint64(st.Bsize)
			d.Healthy = d.CapacityBytes > 0
		} else {
			d.Healthy = false
			statuses = append(statuses, SourceStatus{
				Component: "storage",
				Source:    "statfs:" + m.MountPoint,
				Status:    "partial",
				Detail:    fmt.Sprintf("network mount %s capacity unknown: %v", m.MountPoint, err),
			})
		}
		out = append(out, d)
	}

	// Overall storage status: ok unless something degraded.
	status := "ok"
	detail := fmt.Sprintf("%d devices (%d mount-driven, %d unmounted, %d network)",
		len(out), len(order), len(out)-len(order)-len(networkMounts), len(networkMounts))
	if statfsFailed > 0 || sysErr != nil {
		status = "partial"
		detail += fmt.Sprintf("; %d non-device mounts skipped", statfsFailed)
	}
	statuses = append(statuses, SourceStatus{
		Component: "storage",
		Source:    "/proc/self/mounts+/sys/block+statfs",
		Status:    status,
		Detail:    detail,
	})
	return out, statuses
}

// CollectHostResources reads the host-level resource view from
// /proc/cpuinfo + /proc/meminfo (+ runtime.NumCPU). Read-only; every
// failure is carried in the returned SourceStatus, never as a fabricated
// value. RAMAvailableBytes is the kernel's MemAvailable — the honest
// "what can still be allocated" figure; swap is REPORTED but must never
// be ADMITTED as model memory (see HostResources docs).
func CollectHostResources() (HostResources, []SourceStatus) {
	var res HostResources
	var statuses []SourceStatus
	degraded := false

	res.CPUThreads = numCPUFn()

	if raw, err := readFileFn("/proc/cpuinfo"); err == nil {
		res.CPUModel = parseCPUModel(string(raw))
		if res.CPUModel == "" {
			degraded = true
			statuses = append(statuses, SourceStatus{
				Component: "storage.host",
				Source:    "/proc/cpuinfo",
				Status:    "partial",
				Detail:    "no 'model name' lines parsed — CPUModel unknown",
			})
		}
	} else {
		degraded = true
		statuses = append(statuses, SourceStatus{
			Component: "storage.host",
			Source:    "/proc/cpuinfo",
			Status:    "unavailable",
			Detail:    err.Error(),
		})
	}

	if raw, err := readFileFn("/proc/meminfo"); err == nil {
		fields := parseMeminfo(string(raw))
		res.RAMTotalBytes = fields["memtotal"]
		res.RAMAvailableBytes = fields["memavailable"]
		res.SwapTotalBytes = fields["swaptotal"]
		res.SwapFreeBytes = fields["swapfree"]
		if res.RAMTotalBytes == 0 {
			degraded = true
			statuses = append(statuses, SourceStatus{
				Component: "storage.host",
				Source:    "/proc/meminfo",
				Status:    "partial",
				Detail:    "MemTotal parsed as 0 — RAM figures unreliable",
			})
		}
	} else {
		degraded = true
		statuses = append(statuses, SourceStatus{
			Component: "storage.host",
			Source:    "/proc/meminfo",
			Status:    "unavailable",
			Detail:    err.Error(),
		})
	}

	status := "ok"
	if degraded {
		status = "partial"
	}
	statuses = append(statuses, SourceStatus{
		Component: "storage.host",
		Source:    "/proc/cpuinfo+/proc/meminfo",
		Status:    status,
		Detail: fmt.Sprintf("cpu_threads=%d ram_total=%dB ram_available=%dB (swap reported, never admitted)",
			res.CPUThreads, res.RAMTotalBytes, res.RAMAvailableBytes),
	})
	return res, statuses
}

// ----- parsing helpers ------------------------------------------------------

// parseMounts parses /proc/self/mounts content: one entry per line,
// whitespace-separated (device mountpoint fstype options dump pass).
// Mount points containing spaces appear with octal escapes (\040 etc.)
// which are unescaped here. Malformed lines are skipped (a kernel-provided
// mounts file is well-formed in practice; tolerance keeps the collector
// total even on exotic mounts).
func parseMounts(raw string) []mountEntry {
	lines := strings.Split(raw, "\n")
	out := make([]mountEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		out = append(out, mountEntry{
			Device:     fields[0],
			MountPoint: unescapeMount(fields[1]),
			FSType:     fields[2],
			Options:    fields[3],
			RawMount:   fields[1],
		})
	}
	return out
}

// unescapeMount decodes the octal escapes /proc/self/mounts uses for
// special characters in mount points: \040 (space), \011 (tab),
// \134 (backslash), \012 (newline).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\0`) && !strings.Contains(s, `\1`) {
		return s
	}
	r := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\134`, "\\",
		`\012`, "\n",
	)
	return r.Replace(s)
}

// partitionParent maps a partition device path to its parent device name:
// /dev/nvme0n1p2 → "nvme0n1" (NVMe namespaces partition as <ctrl>n<ns>p<N>),
// /dev/sda3 → "sda", /dev/mmcblk0p1 → "mmcblk0". Whole-device paths
// (/dev/sda, /dev/nvme0n1, /dev/dm-0) map to themselves. The trailing-
// digit strip applies ONLY to the sd*/hd*/vd* families — dm-0 must keep
// its digit (its parent is itself) and nvme0n1 must never lose the
// namespace suffix.
func partitionParent(devPath string) string {
	name := filepath.Base(devPath)
	// NVMe: nvme0n1p3 → nvme0n1; nvme0n1 (whole namespace) → itself.
	if strings.HasPrefix(name, "nvme") {
		if i := strings.IndexByte(name, 'p'); i > 0 {
			if tail := name[i+1:]; tail != "" && isAllDigits(tail) {
				return name[:i]
			}
		}
		return name
	}
	// mmcblk0p1 → mmcblk0; mmcblk0 → itself.
	if strings.HasPrefix(name, "mmcblk") {
		if i := strings.IndexByte(name, 'p'); i > 0 {
			if tail := name[i+1:]; tail != "" && isAllDigits(tail) {
				return name[:i]
			}
		}
		return name
	}
	// sdX<N> / hdX<N> / vdX<N> → strip trailing digits (family-scoped).
	if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "hd") || strings.HasPrefix(name, "vd") {
		if trimmed := strings.TrimRight(name, "0123456789"); trimmed != "" && trimmed != name {
			return trimmed
		}
	}
	return name
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isCandidateBlockDevice filters /sys/block entries to real storage
// candidates: device-mapper (dm-*), mdraid (md*), loop devices, zram, and
// RAM disks are excluded — they are STACKED or VIRTUAL layers whose
// inclusion would double-count capacity already reported through their
// mounts (an LVM logical volume's filesystem is inventoried at the dm-*
// mount; the underlying PV device reports raw capacity only).
func isCandidateBlockDevice(name string) bool {
	if strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "md") ||
		strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "zram") ||
		strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "sr") {
		return false
	}
	if strings.HasPrefix(name, "nvme") {
		return true
	}
	if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "hd") ||
		strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "mmcblk") {
		return true
	}
	return false
}

// deviceIdentity resolves (kind, model, serial) for a block device name
// from read-only sysfs sources. Failures degrade honestly: kind falls
// back by name prefix where that is unambiguous (nvme), otherwise
// unknown; model/serial fall back to empty. The serial is used ONLY as
// StableID digest material — never emitted raw.
func deviceIdentity(name string) (DeviceKind, string, string) {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return DeviceNVMe, readTrimmed("/sys/class/nvme/" + nvmeControllerName(name) + "/model"),
			readTrimmed("/sys/class/nvme/" + nvmeControllerName(name) + "/serial")
	case strings.HasPrefix(name, "sd"), strings.HasPrefix(name, "hd"):
		kind := DeviceSATA
		if devPath, err := evalSymlinksFn("/sys/block/" + name + "/device"); err == nil {
			lower := strings.ToLower(devPath)
			switch {
			case strings.Contains(lower, "/usb"):
				kind = DeviceUSB
			case strings.Contains(lower, "/ata"):
				kind = DeviceSATA
			default:
				// virtio/SCSI/other transports surfaced under sd* — the
				// transport is NOT determinable from the path: unknown.
				kind = DeviceUnknown
			}
		} else {
			// Transport resolution failed — sd* could be SATA, SAS, USB
			// or virtio: report unknown, never guess.
			kind = DeviceUnknown
		}
		return kind, readTrimmed("/sys/block/" + name + "/device/model"), ""
	case strings.HasPrefix(name, "vd"), strings.HasPrefix(name, "mmcblk"):
		// virtio-blk / MMC-eMMC: transport is hypervisor/board-defined —
		// no honest mapping to the four kinds. Model still recorded when
		// exposed (mmcblk carries CID, not a model file — empty is honest).
		return DeviceUnknown, readTrimmed("/sys/block/" + name + "/device/model"), ""
	default:
		return DeviceUnknown, "", ""
	}
}

// nvmeControllerName maps a namespace/partition name to its controller
// sysfs name: nvme0n1p2 → "nvme0" (controller 0), nvme1n1 → "nvme1".
func nvmeControllerName(ns string) string {
	// nvme<ctl>n<ns>[p<part>]
	s := ns
	if i := strings.IndexByte(s, 'p'); i > 0 && strings.Contains(s, "n") && i > strings.Index(s, "n") {
		s = s[:i]
	}
	// now: nvme<ctl>n<ns>
	if j := strings.LastIndexByte(s, 'n'); j > 0 {
		return s[:j]
	}
	return s
}

// readTrimmed reads a sysfs file and returns its whitespace-trimmed
// content; empty string on any failure (honest absence).
func readTrimmed(path string) string {
	raw, err := readFileFn(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readSysfsSize reads /sys/block/<name>/size (512-byte sectors).
func readSysfsSize(name string) (uint64, error) {
	raw, err := readFileFn("/sys/block/" + name + "/size")
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
}

// deviceID builds the inventory ID: "storage.<kind>.<n>" with a per-kind
// counter derived from the running output length (stable within a
// snapshot; StableID carries cross-boot identity).
func deviceID(kind DeviceKind, name string, n int) string {
	return fmt.Sprintf("storage.%s.%d", kind, n)
}

// stableDeviceID digests the available identity material into the
// pseudonymous StableID ("dev." + 16 hex chars). Material: kind, kernel
// name, model, serial (when observable). The digest is deterministic:
// the same device in the same slot yields the same StableID across boots;
// a device moved to a different name keeps identity ONLY when serial/
// model material was observable (documented honestly — identity strength
// varies by transport).
func stableDeviceID(kind DeviceKind, name, model, serial string) string {
	material := string(kind) + "|" + name + "|" + model + "|" + serial
	sum := sha256.Sum256([]byte(material))
	return "dev." + hex.EncodeToString(sum[:8])
}

// stableNetworkID digests fstype+remote-source for network mounts.
func stableNetworkID(fstype, source string) string {
	sum := sha256.Sum256([]byte(fstype + "|" + source))
	return "net." + hex.EncodeToString(sum[:8])
}

// mountIsReadOnly parses the options field for a leading/embedded "ro".
func mountIsReadOnly(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == "ro" {
			return true
		}
	}
	return false
}

// parseCPUModel extracts the first "model name" value from /proc/cpuinfo.
func parseCPUModel(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "model name") {
			if i := strings.IndexByte(line, ':'); i >= 0 && i+1 < len(line) {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

// parseMeminfo parses /proc/meminfo into a lowercase-keyed byte map
// (kB values converted to bytes).
func parseMeminfo(raw string) map[string]uint64 {
	out := make(map[string]uint64, 8)
	for _, line := range strings.Split(raw, "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		valPart := strings.TrimSpace(line[i+1:])
		if j := strings.IndexByte(valPart, ' '); j > 0 {
			valPart = valPart[:j]
		}
		kb, err := strconv.ParseUint(valPart, 10, 64)
		if err != nil {
			continue
		}
		out[key] = kb * 1024
	}
	return out
}
