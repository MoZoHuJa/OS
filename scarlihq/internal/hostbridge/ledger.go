package hostbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

const ledgerVersion = 1

type LedgerEntry struct {
	AllocationID string    `json:"allocation_id"`
	Fingerprint  string    `json:"fingerprint"`
	RuntimeID    string    `json:"runtime_id"`
	ModelID      string    `json:"model_id"`
	GPUID        string    `json:"gpu_id,omitempty"`
	Port         int       `json:"port"`
	State        string    `json:"state"`
	Ref          string    `json:"ref,omitempty"`
	Owned        bool      `json:"owned"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ledgerFile struct {
	Version int                    `json:"version"`
	Entries map[string]LedgerEntry `json:"entries"`
}

type Ledger struct {
	path string
}

var ledgerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// NewLedger opens a durable owner-only JSON ledger. The parent directory must
// already exist and must not be accessible by group/other users. It does not
// create directories or repair unsafe permissions on an existing path.
func NewLedger(path string) (*Ledger, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("hostbridge: ledger path must be absolute")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("hostbridge: ledger parent must be an owner-only real directory")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
			return nil, errors.New("hostbridge: ledger file must be a regular 0600 file")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("hostbridge: cannot inspect ledger path")
	}
	return &Ledger{path: path}, nil
}

func (l *Ledger) withLock(fn func(*ledgerFile) error) error {
	lockPath := l.path + ".lock"
	if info, statErr := os.Lstat(lockPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("hostbridge: ledger lock must be a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return errors.New("hostbridge: cannot inspect ledger lock")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("hostbridge: cannot open ledger lock")
	}
	defer lock.Close()
	if err := lock.Chmod(0600); err != nil {
		return errors.New("hostbridge: cannot secure ledger lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return errors.New("hostbridge: cannot lock ledger")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	data := ledgerFile{Version: ledgerVersion, Entries: map[string]LedgerEntry{}}
	raw, err := os.ReadFile(l.path)
	if err == nil {
		if err := json.Unmarshal(raw, &data); err != nil {
			return errors.New("hostbridge: ledger is corrupt; manual recovery required")
		}
		if data.Version != ledgerVersion || data.Entries == nil {
			return errors.New("hostbridge: unsupported or corrupt ledger schema")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("hostbridge: cannot read ledger")
	}
	if err := fn(&data); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errors.New("hostbridge: cannot encode ledger")
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".ledger-*.tmp")
	if err != nil {
		return errors.New("hostbridge: cannot create ledger temporary file")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return errors.New("hostbridge: cannot secure ledger temporary file")
	}
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return errors.New("hostbridge: cannot write ledger")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.New("hostbridge: cannot sync ledger")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("hostbridge: cannot close ledger temporary file")
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return errors.New("hostbridge: cannot atomically replace ledger")
	}
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return errors.New("hostbridge: cannot open ledger directory for sync")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("hostbridge: cannot sync ledger directory")
	}
	return nil
}

func (l *Ledger) Create(req workload.StartRequest) error {
	if !ledgerIDPattern.MatchString(req.AllocationID) || !resourcecontract.IsSupportedRuntime(req.RuntimeID) ||
		!modelPattern.MatchString(req.ModelID) || !validGPU(req.GPUID) || req.Port < 1 || req.Port > 65535 {
		return errors.New("hostbridge: invalid allocation assignment for ledger")
	}
	entry := LedgerEntry{
		AllocationID: req.AllocationID,
		Fingerprint:  requestFingerprint(req),
		RuntimeID:    req.RuntimeID,
		ModelID:      req.ModelID,
		GPUID:        req.GPUID,
		Port:         req.Port,
		State:        "STARTING",
		UpdatedAt:    time.Now().UTC(),
	}
	return l.withLock(func(data *ledgerFile) error {
		if _, exists := data.Entries[req.AllocationID]; exists {
			return errors.New("hostbridge: allocation already exists in ledger")
		}
		data.Entries[req.AllocationID] = entry
		return nil
	})
}

func (l *Ledger) Get(id string) (LedgerEntry, bool, error) {
	var result LedgerEntry
	var found bool
	err := l.withLock(func(data *ledgerFile) error {
		result, found = data.Entries[id]
		return nil
	})
	return result, found, err
}

func (l *Ledger) SetState(id, state, ref string, owned bool) error {
	switch state {
	case "STARTING", "RUNNING", "STOPPED", "RECONCILE_REQUIRED":
	default:
		return fmt.Errorf("hostbridge: invalid ledger state %q", state)
	}
	if len(ref) > 256 {
		return errors.New("hostbridge: invalid workload reference")
	}
	return l.withLock(func(data *ledgerFile) error {
		entry, ok := data.Entries[id]
		if !ok {
			return errors.New("hostbridge: allocation not found in ledger")
		}
		entry.State = state
		entry.Ref = ref
		entry.Owned = owned
		entry.UpdatedAt = time.Now().UTC()
		data.Entries[id] = entry
		return nil
	})
}

// List returns ledger entries in deterministic allocation-ID order for audits.
func (l *Ledger) List() ([]LedgerEntry, error) {
	var entries []LedgerEntry
	err := l.withLock(func(data *ledgerFile) error {
		entries = make([]LedgerEntry, 0, len(data.Entries))
		for _, entry := range data.Entries {
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].AllocationID < entries[j].AllocationID })
		return nil
	})
	return entries, err
}
