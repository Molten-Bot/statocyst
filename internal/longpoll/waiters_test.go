package longpoll

import "testing"

func TestRegisterNotifyAndCancel(t *testing.T) {
	w := NewWaiters()
	ch, cancel := w.Register("agent-a")

	w.Notify("agent-a")
	select {
	case <-ch:
	default:
		t.Fatal("expected waiter notification")
	}

	cancel()
	cancel() // idempotent cancel should not panic
	if len(w.byAgent) != 0 {
		t.Fatalf("expected waiter map cleanup after cancel, got %+v", w.byAgent)
	}

	w.Notify("agent-a")
}

func TestNotifyDoesNotBlockWhenChannelBufferIsFull(t *testing.T) {
	w := NewWaiters()
	ch, _ := w.Register("agent-a")

	w.Notify("agent-a")
	w.Notify("agent-a")

	select {
	case <-ch:
	default:
		t.Fatal("expected one queued notification")
	}

	select {
	case <-ch:
		t.Fatal("expected second notify to be dropped while buffer full")
	default:
	}
}

func TestNotifyOnlyTargetsMatchingAgent(t *testing.T) {
	w := NewWaiters()
	chA, _ := w.Register("agent-a")
	chB, _ := w.Register("agent-b")

	w.Notify("agent-a")

	select {
	case <-chA:
	default:
		t.Fatal("expected agent-a waiter to be notified")
	}

	select {
	case <-chB:
		t.Fatal("did not expect agent-b waiter notification")
	default:
	}
}
