// Package storage — unified storage inventory and host resource collection
// (v19.7.0, Compute Fabric Phase 1, roadmap §5.2/§5.3 of
// docs/roadmaps/SCARLIXOS_UNIFIED_COMPUTE_FABRIC_IMPLEMENTATION_ROADMAP.md).
//
// CHARTER (the package's non-negotiable contract):
//
//   - READ-ONLY. Discovery enumerates block devices and mount points from
//     safe, read-only system sources (/proc/self/mounts, /sys/block, and
//     the statfs(2) syscall). It writes NOTHING, forks NOTHING, and
//     benchmarks NOTHING. There is no code path from Collect* to any
//     mutation or any child process.
//   - The benchmark is a SEPARATE, opt-in operation (BenchmarkOptions +
//     RunReadBenchmark) that an OPERATOR must invoke explicitly with a
//     target path. It is never called by discovery, never called at boot,
//     never called during routine inventory refresh. A future diagnostic
//     surface may expose it behind its own explicit request; v19.7.0
//     exposes it only through the scarlix-inventory CLI's
//     --storage-benchmark flag.
//   - Unknown measurements are UNKNOWN, not zero. Fields where zero is a
//     legitimate value the kernel could report but we did not observe
//     (device temperature) are pointers — nil means "not measured". Zero
//     values in numeric fields mean the source reported zero, or the
//     metric does not apply (see per-field docs).
//   - No secrets, no raw serials. Stable hardware identity is exposed as
//     StableID — a one-way SHA-256 digest of the hardware identity
//     material (transport + model + serial), NEVER the raw serial itself
//     (roadmap §5.2 rule 5: serials stay out of public inventory
//     responses unless access-controlled).
//   - A benchmark result is a MEASUREMENT, not a promise. ReadProfile
//     records provenance (timestamp, block size, queue depth, profile
//     ID); it is never presented as a guaranteed sustained rate.
//
// This package complements (does NOT replace) internal/inventory — the
// inventory package remains the normalized GPU/runtime/model view; storage
// provides the storage-device and host-resource dimensions that the v2
// snapshot (inventory.InventorySnapshotV2) composes.
package storage

import "time"

// DeviceKind classifies a storage device by its interface/transport.
// The set is deliberately small and honest: if the transport cannot be
// determined from the read-only sources, the device is DeviceUnknown —
// never a guess.
type DeviceKind string

const (
	// DeviceNVMe — an NVMe namespace (kernel names nvme0n1, nvme1n1, ...).
	DeviceNVMe DeviceKind = "nvme"
	// DeviceSATA — a SATA/ATA-attached disk (kernel names sd*, hd*).
	DeviceSATA DeviceKind = "sata"
	// DeviceUSB — a USB-attached disk. USB mass-storage also surfaces as
	// sd*, so the discriminator is the /sys device path containing a USB
	// segment — when that path is unreadable or ambiguous, the device is
	// reported as sata or unknown, never assumed USB.
	DeviceUSB DeviceKind = "usb"
	// DeviceNetwork — a network-mounted filesystem (nfs, nfs4, cifs,
	// smbfs, sshfs, 9p, ...). There is no local block device; the
	// "device" is the mount itself with its remote source as identity.
	DeviceNetwork DeviceKind = "network"
	// DeviceUnknown — the transport could not be determined from the
	// read-only sources. This is a valid, honest value: the planner must
	// treat unknown transport as unknown, not as any specific kind.
	DeviceUnknown DeviceKind = "unknown"
)

// ReadProfile is a measured read-performance record. All fields are
// provenance-carrying: a zero ReadProfile (MeasuredAt.IsZero()) means
// "never measured", NOT "zero performance". Discovery never populates a
// ReadProfile; only an operator-triggered RunReadBenchmark does.
type ReadProfile struct {
	// SequentialReadMBps — the measured sequential-read throughput in
	// MB/s (decimal megabytes). 0 means not measured (MeasuredAt zero).
	SequentialReadMBps float64 `json:"sequential_read_mbps,omitempty"`
	// RandomReadIOPS — measured random-read I/O operations per second.
	// 0 means not measured. v19.7.0's benchmark measured sequential read
	// only; v19.8.0 (roadmap §8.6 — SSD performance qualification for
	// SSD-backed expert paging) added the opt-in random-read phase that
	// populates this field plus the latency percentiles below.
	RandomReadIOPS float64 `json:"random_read_iops,omitempty"`
	// RandomReadP50Ms — measured random-read p50 (median) latency in
	// milliseconds. 0 means not measured (v19.8.0 field; additive and
	// omitempty — the v2 snapshot JSON contract is unchanged for
	// profiles measured without the random phase).
	RandomReadP50Ms float64 `json:"random_read_p50_ms,omitempty"`
	// RandomReadP95Ms — measured random-read p95 latency in
	// milliseconds. 0 means not measured.
	RandomReadP95Ms float64 `json:"random_read_p95_ms,omitempty"`
	// RandomReadP99Ms — measured random-read p99 latency in
	// milliseconds. 0 means not measured (v19.8.0 field; roadmap §8.6
	// requires "p50/p95/p99 latency" — all three percentiles travel with
	// the record, never one masquerading as the others).
	RandomReadP99Ms float64 `json:"random_read_p99_ms,omitempty"`
	// BlockSizeBytes — the read block size the measurement used
	// (e.g. 1048576 = 1 MiB). 0 means not measured.
	BlockSizeBytes int `json:"block_size_bytes,omitempty"`
	// RandomBlockBytes — the random-read block size (v19.8.0; distinct
	// from the sequential BlockSizeBytes — a device may be profiled at
	// 1 MiB sequential and 4 KiB random in one run). 0 means the random
	// phase did not run.
	RandomBlockBytes int `json:"random_block_bytes,omitempty"`
	// QueueDepth — the queue depth the measurement used. 0 means not
	// measured (a single synchronous reader).
	QueueDepth int `json:"queue_depth,omitempty"`
	// MeasuredAt — when the measurement ran. The zero value means the
	// profile is absent; every other field is only meaningful when
	// MeasuredAt is non-zero.
	MeasuredAt time.Time `json:"measured_at,omitempty"`
	// ProfileID — identifies the benchmark invocation (the same ID the
	// operator saw in the diagnostic output), so a recorded profile can
	// be traced to its run.
	ProfileID string `json:"profile_id,omitempty"`
}

// Filesystem is ONE mounted filesystem belonging to a Device
// (v19.7.1, KPK-19.7.0-P2-01: a physical disk with several partitions
// reports EVERY filesystem, not just the first mount's numbers).
//
// Multiple mount table entries that are the SAME filesystem (bind mounts,
// btrfs subvolumes, by-uuid/by-path source aliases) are merged into ONE
// Filesystem entry — their capacity is counted ONCE (statfs returns the
// same figures and the same fsid for every mount point of one
// filesystem). The merge key is the mount SOURCE first (the kernel's
// device path for the filesystem) and the statfs fsid as a defensive
// second guard (two different source paths with one fsid are one
// filesystem observed twice).
type Filesystem struct {
	// Source — the mount source as /proc/self/mounts reports it for
	// this filesystem ("/dev/nvme0n1p2"). For merged entries this is
	// the first-seen source; every other source alias is recorded in
	// Sources.
	Source string `json:"source"`
	// Sources — every distinct mount source that resolved to this
	// filesystem (non-nil; usually exactly one entry — Source).
	Sources []string `json:"sources"`
	// MountPoints — every mount point of this filesystem (in mount
	// table order; non-nil).
	MountPoints []string `json:"mount_points"`
	// FSType — the filesystem type (ext4, btrfs, nfs4, ...).
	FSType string `json:"fstype,omitempty"`
	// CapacityBytes — total capacity of THIS filesystem
	// (statfs blocks*bsize). 0 when statfs failed (see Healthy).
	CapacityBytes uint64 `json:"capacity_bytes"`
	// AvailableBytes — bytes available to unprivileged writes on THIS
	// filesystem (statfs bavail*bsize). 0 when statfs failed.
	AvailableBytes uint64 `json:"available_bytes"`
	// ReadOnly — true iff this filesystem's mount options carry the
	// exact token "ro" (KPK-19.7.0-P3-05: exact token comparison —
	// "errors=remount-ro" is NOT a read-only mount).
	ReadOnly bool `json:"read_only"`
	// Healthy — true iff statfs succeeded for this filesystem AND
	// reported a non-zero capacity. A failed statfs marks ONLY this
	// filesystem degraded (the device's other filesystems keep their
	// figures; the snapshot's source status names the failed mount).
	Healthy bool `json:"healthy"`
}

// Device is the normalized, read-only view of one storage device or
// network mount. One Device is emitted per block device (with all its
// mount points and filesystems) or per network filesystem mount.
//
// Identity policy:
//
//   - ID — the stable inventory identifier ("storage.nvme.0",
//     "storage.network.1", ...). Stable within a boot for the same
//     underlying device; NOT stable across reordering. Planners that
//     need cross-boot identity use StableID.
//   - StableID — a pseudonymous digest of hardware identity material
//     (16 hex chars, "dev." prefix) or, for network mounts, of
//     fstype:source. Same hardware → same StableID across boots and
//     device-name changes. Raw serials are NEVER placed here.
type Device struct {
	// ID — the inventory identifier (see identity policy above).
	ID string `json:"id"`
	// StableID — the cross-boot pseudonymous hardware identity digest;
	// empty when no identity material was observable (honest absence).
	StableID string `json:"stable_id,omitempty"`
	// Model — the device model string (from /sys/block/<dev>/device/model
	// or the NVMe model). Empty when not exposed.
	Model string `json:"model,omitempty"`
	// Kind — the interface classification (nvme/sata/usb/network/unknown).
	Kind DeviceKind `json:"kind"`
	// CapacityBytes — the AGGREGATE capacity across the device's
	// DISTINCT filesystems (v19.7.1, KPK-19.7.0-P2-01: every
	// partition's filesystem counts, each counted once — bind mounts
	// and btrfs subvolumes of one filesystem are merged before the
	// sum). An UNMOUNTED device reports RAW sysfs capacity here
	// instead. 0 when every statfs failed or no filesystem is
	// attached (the source status carries the reason).
	CapacityBytes uint64 `json:"capacity_bytes"`
	// AvailableBytes — the aggregate bytes available to unprivileged
	// writes across the device's distinct filesystems (same dedupe
	// discipline as CapacityBytes). 0 when statfs failed, no mount
	// point, or the device is unmounted (free space is UNOBSERVABLE
	// without a filesystem — never a phantom "free" device).
	AvailableBytes uint64 `json:"available_bytes"`
	// Filesystem — the fstype of the device's first filesystem (kept
	// for the v19.7.0 field compatibility; per-filesystem types are in
	// Filesystems[]). Empty when the device has no mounted filesystem.
	Filesystem string `json:"filesystem,omitempty"`
	// Filesystems — EVERY distinct mounted filesystem on this device
	// with its own capacity, availability, and read-only state
	// (KPK-19.7.0-P2-01; non-nil, empty when unmounted). A filesystem
	// shared by several mount points appears ONCE with all of them.
	Filesystems []Filesystem `json:"filesystems"`
	// MountPoints — every mount point backed by this device (in
	// /proc/self/mounts order). Non-nil (empty when unmounted).
	MountPoints []string `json:"mount_points"`
	// ReadOnly — true when EVERY filesystem of this device is mounted
	// read-only (a mixed ro/rw device reports false; the per-
	// filesystem state is the source of truth in Filesystems[]).
	ReadOnly bool `json:"read_only"`
	// TemperatureC — device temperature in Celsius. nil = NOT MEASURED
	// (the honest unknown — /sys/class/hwmon association is not part of
	// v19.7.0 discovery). A non-nil value was read from a source.
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	// ReadProfile — the last operator-triggered benchmark result.
	// nil = "never measured" — ABSENT from JSON, never a zero timestamp
	// masquerading as data (unknown ≠ zero).
	ReadProfile *ReadProfile `json:"read_profile,omitempty"`
	// Healthy — true iff discovery positively established the device:
	// identity material or a mount point was observed AND (when a mount
	// exists) statfs returned sane values (capacity > 0). Devices whose
	// discovery partially failed are Healthy=false with the failure in
	// the snapshot's source status — an inventory consumer must not
	// treat Healthy=false as "device absent" (absence is a missing
	// entry; failure is an unhealthy entry).
	Healthy bool `json:"healthy"`
	// ObservedAt — when this device record was collected.
	ObservedAt time.Time `json:"observed_at"`
}

// HostResources is the read-only host-level resource view (roadmap §5.3).
// Collected from /proc/cpuinfo and /proc/meminfo; every field is a
// point-in-time observation carried with the snapshot's ObservedAt.
//
// Swap policy (roadmap §5.3): swap is REPORTED, never ADMITTED. No
// workload may treat SwapFreeBytes as model memory unless a backend has an
// explicitly tested swap strategy — that is a planner/admission policy
// (Phase 2), this type only observes.
type HostResources struct {
	// CPUThreads — the number of logical CPUs visible to this process
	// (runtime.NumCPU). 0 would mean an interrogated-but-empty view; in
	// practice Go guarantees >= 1.
	CPUThreads int `json:"cpu_threads"`
	// CPUModel — the "model name" from /proc/cpuinfo (first processor).
	// Empty when /proc/cpuinfo is unreadable or lacks the field (e.g.
	// exotic architectures) — an honest empty, not a fabricated value.
	CPUModel string `json:"cpu_model,omitempty"`
	// RAMTotalBytes — MemTotal from /proc/meminfo, bytes.
	RAMTotalBytes uint64 `json:"ram_total_bytes"`
	// RAMAvailableBytes — MemAvailable from /proc/meminfo, bytes. The
	// kernel's own estimate of memory available for new allocations
	// without swapping — the number an admission gate compares against.
	RAMAvailableBytes uint64 `json:"ram_available_bytes"`
	// RAMPinnedBytes — pinned/page-locked RAM attributable to ACTIVE
	// WORKLOAD RESERVATIONS. This is a reservation-ledger property, not
	// a /proc observation — Phase 2's admission layer populates it when
	// it owns the ledger. 0 here means "no pinned-RAM figure reported",
	// NOT "no memory is pinned" (the honest-unknown convention).
	RAMPinnedBytes uint64 `json:"ram_pinned_bytes,omitempty"`
	// SwapTotalBytes — SwapTotal from /proc/meminfo, bytes.
	SwapTotalBytes uint64 `json:"swap_total_bytes,omitempty"`
	// SwapFreeBytes — SwapFree from /proc/meminfo, bytes.
	SwapFreeBytes uint64 `json:"swap_free_bytes,omitempty"`
}
