// Command scarlix-reservation is the production reservation composition
// root (audit F-04, v19.3.2): the ONE long-running service that owns the
// file-backed reservation engine.
//
// Startup contract (KPK-19.2.12-P1-02 / KPK-19.2.13-P1-01):
//
//  1. Load the administrator-owned config (/etc/scarlix/reservation.json,
//     root-owned 0640 root:65532 — tokens never world-readable).
//  2. Build the production providers: TokenIdentityProvider (config tokens),
//     HostInventoryProvider (read-only collectors), DockerCleanupVerifier
//     (docker ps over the scarlix.managed label).
//  3. Compose reservation.NewFileBackedManager over the durable stores.
//  4. Recover(reconcilerToken) BEFORE anything else — finalize RESERVING
//     allocations, reap expired leases, complete interrupted releases,
//     repair RESERVED lease metadata. A corrupt store aborts startup;
//     other recovery errors are logged and left to the periodic reaper.
//  5. Run the lease reaper until SIGINT/SIGTERM, then exit cleanly.
//
// The governor is deliberately OFF the HTTP plane: it owns reconciliation,
// not intake. Reserve/Release intake (the compute API) is the next
// documented step and will reuse the same stores under the same flock.
//
// Deployment: scarlix-reservation.service (User=65532, docker supplementary
// group, ProtectSystem=strict) — copied by install.sh but DISABLED by
// default until an operator installs the config and completes host
// acceptance. See docs/SCARLIX_COMPUTE_FABRIC_RESERVATION_GOVERNOR.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/reservation"
	"github.com/MoZoHuJa/OS/scarlihq/internal/reservationops"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lmicroseconds)

	configPath := flag.String("config", "/etc/scarlix/reservation.json", "administrator-owned reservation governor config (tokens + managed ports)")
	reservationPath := flag.String("reservations", "/var/lib/scarlix-reservation/reservations.jsonl", "durable reservation store (JSONL, flock-protected)")
	allocationPath := flag.String("allocations", "/var/lib/scarlix-reservation/allocations.jsonl", "durable allocation store shared with the compute lifecycle (JSONL, flock-protected)")
	reapInterval := flag.Duration("reap-interval", 30*time.Second, "lease reaper interval (must be positive)")
	flag.Parse()

	if *reapInterval <= 0 {
		log.Fatalf("reservation governor: --reap-interval must be positive (got %s)", *reapInterval)
	}

	cfg, err := reservationops.LoadGovernorConfig(*configPath)
	if err != nil {
		log.Fatalf("reservation governor: config rejected: %v", err)
	}

	// Trusted identities: the governor's own reconciler token plus any
	// configured user principals (empty until the intake API exists —
	// fail-closed: nobody else can reserve).
	reconcilerTokens := map[string]string{cfg.ReconcilerToken: reservationops.ReconcilerPrincipal}
	identities, err := reservation.NewTokenIdentityProvider(cfg.UserTokens, reconcilerTokens)
	if err != nil {
		log.Fatalf("reservation governor: identity provider rejected: %v", err)
	}

	inventoryProvider, err := reservationops.NewHostInventoryProvider(cfg.ManagedPorts)
	if err != nil {
		log.Fatalf("reservation governor: inventory provider rejected: %v", err)
	}

	cleanupVerifier := reservationops.NewDockerCleanupVerifier()

	manager, err := reservation.NewFileBackedManager(
		*reservationPath,
		*allocationPath,
		reservation.DefaultPolicy(),
		reservation.RealClock{},
		inventoryProvider,
		cleanupVerifier,
		identities,
	)
	if err != nil {
		log.Fatalf("reservation governor: composition rejected: %v", err)
	}
	log.Printf("reservation governor: stores ready (reservations=%s allocations=%s)", *reservationPath, *allocationPath)

	// Startup recovery — the documented contract. Everything durable is
	// converged before the reaper loop takes over periodic repair.
	summary, err := manager.Recover(cfg.ReconcilerToken)
	if summary != nil {
		log.Printf("reservation governor: recovery summary: finalized=%d aborted=%d released=%d cleanupBlocked=%d leaseRepaired=%d failed=%d",
			countOrZero(summary.Finalized), countOrZero(summary.Aborted), countOrZero(summary.Released),
			countOrZero(summary.CleanupBlocked), countOrZero(summary.LeaseRepaired), countOrZero(summary.Failed))
	}
	if err != nil {
		if errors.Is(err, reservation.ErrReservationStoreCorrupt) {
			// A corrupt store is unrecoverable in-process: stay down loudly.
			log.Fatalf("reservation governor: store is corrupt — refusing to start: %v", err)
		}
		// Non-corrupt recovery errors keep partial progress; the periodic
		// reaper retries convergence every tick and surfaces failures.
		log.Printf("reservation governor: recovery reported errors (reaper will retry): %v", err)
	} else {
		log.Printf("reservation governor: startup recovery complete")
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	report := func(err error) {
		log.Printf("reservation governor: reaper pass error: %v", err)
	}
	log.Printf("reservation governor: lease reaper engaged (interval %s)", *reapInterval)
	if err := manager.RunLeaseReaper(signalCtx, cfg.ReconcilerToken, *reapInterval, report); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("reservation governor: shutdown requested — reaper stopped cleanly")
			return
		}
		log.Fatalf("reservation governor: reaper failed: %v", err)
	}
}

// countOrZero guards nil slices in the recovery summary for stable logging.
func countOrZero(ids []string) int {
	if ids == nil {
		return 0
	}
	return len(ids)
}

// version is injected at build time via -ldflags "-X main.version=...".
// Default matches the VERSION file (see install.sh build step).
var version = "19.3.2"

func init() {
	// First log line identifies the binary version unambiguously.
	log.Printf("SCARLIX OS %s — reservation governor (F-04 composition root)", version)
}
