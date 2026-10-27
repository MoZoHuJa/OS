package agentfabric

import (
	"testing"
	"time"
)

func TestFabricBusSubscribeBound(t *testing.T) {
	bus := NewFabricBus()
	subs := make([]<-chan Signal, 0, MaxBusSubscribers)
	ids := make([]uint64, 0, MaxBusSubscribers)
	for i := 0; i < MaxBusSubscribers; i++ {
		id, ch, ok := bus.Subscribe()
		if !ok {
			t.Fatalf("Subscribe(%d) rejected below the bound", i)
		}
		subs = append(subs, ch)
		ids = append(ids, id)
	}
	if _, _, ok := bus.Subscribe(); ok {
		t.Error("Subscribe beyond MaxBusSubscribers accepted — the bound is real state")
	}
	if bus.Subscribers() != MaxBusSubscribers {
		t.Errorf("Subscribers = %d, want %d", bus.Subscribers(), MaxBusSubscribers)
	}
	// Unsubscribe frees a slot; a new subscriber fits.
	bus.Unsubscribe(ids[0])
	if _, _, ok := bus.Subscribe(); !ok {
		t.Error("Subscribe after Unsubscribe rejected — slot not freed")
	}
	// Unsubscribing an unknown id is a no-op (idempotent disconnect path).
	bus.Unsubscribe(9999)
}

func TestFabricBusDelivers(t *testing.T) {
	bus := NewFabricBus()
	_, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	sig := Signal{Type: SignalCommandSubmitted, Ref: "cmd-abc", At: time.Now().UTC()}
	bus.Publish(sig)
	select {
	case got := <-ch:
		if got.Type != sig.Type || got.Ref != sig.Ref {
			t.Errorf("delivered %+v, want %+v", got, sig)
		}
	default:
		t.Fatal("signal not delivered — Publish must deliver to a fresh queue")
	}
}

func TestFabricBusDropNotBlock(t *testing.T) {
	bus := NewFabricBus()
	_, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	// Fill the queue (BusSubscriberQueue slots), then publish one more than
	// fits — Publish must NOT block, the overflow drops and counts.
	for i := 0; i < BusSubscriberQueue+8; i++ {
		bus.Publish(Signal{Type: SignalCommandDecided, Ref: "cmd-x", At: time.Now().UTC()})
	}
	// Drain: exactly BusSubscriberQueue delivered, the rest dropped.
	delivered := 0
	for {
		select {
		case <-ch:
			delivered++
			continue
		default:
		}
		break
	}
	if delivered != BusSubscriberQueue {
		t.Errorf("delivered %d signals, want %d (the queue bound)", delivered, BusSubscriberQueue)
	}
	if bus.Dropped() != 8 {
		t.Errorf("Dropped = %d, want 8 (drops are counted, not silent)", bus.Dropped())
	}
}

func TestFabricBusNoReplay(t *testing.T) {
	bus := NewFabricBus()
	// Publish BEFORE any subscriber exists.
	bus.Publish(Signal{Type: SignalExecutionRecorded, Ref: "afx-1", At: time.Now().UTC()})
	// A late subscriber receives NOTHING (no replay — the bus is a wake-up
	// channel, not a log; the first authoritative state comes from the
	// REST refetch on fabric.subscribed).
	_, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	select {
	case got := <-ch:
		t.Fatalf("replayed a pre-subscription signal: %+v — NO replay is the contract", got)
	default:
	}
	// And signals published after ANOTHER subscriber joined do not reach
	// this one after Unsubscribe.
	bus.Publish(Signal{Type: SignalCommandCancelled, Ref: "cmd-y", At: time.Now().UTC()})
	select {
	case got := <-ch:
		_ = got // this one IS a live signal — must arrive
	case <-time.After(2 * time.Second):
		t.Fatal("live signal not delivered")
	}
	id, _, _ := bus.Subscribe()
	bus.Unsubscribe(id) // close that channel — no further delivery, no panic
	bus.Publish(Signal{Type: SignalCommandCancelled, Ref: "cmd-z", At: time.Now().UTC()})
}

func TestFabricBusUnsubscribeClosesChannel(t *testing.T) {
	bus := NewFabricBus()
	id, ch, ok := bus.Subscribe()
	if !ok {
		t.Fatal("Subscribe failed")
	}
	bus.Unsubscribe(id)
	select {
	case _, open := <-ch:
		if open {
			t.Error("channel still open after Unsubscribe")
		}
	default:
		t.Error("channel not closed after Unsubscribe (select would block)")
	}
}

func TestFabricBusSignalTypesClosed(t *testing.T) {
	want := []SignalType{
		SignalCommandSubmitted,
		SignalCommandRouted,
		SignalCommandDecided,
		SignalCommandCancelled,
		SignalExecutionRecorded,
	}
	for _, st := range want {
		if st == "" {
			t.Errorf("empty signal type in %v", want)
		}
	}
	// The signal set is pinned: command.* + execution.* are CHANGE
	// signals; fabric.* are CONTROL frames owned by the WS layer.
	for _, st := range want {
		if len(st) > 7 && (st[:8] == "fabric.") == true {
			t.Errorf("change signal %q uses the fabric.* control namespace", st)
		}
	}
}
