package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

func TestPullForAgentRechecksQueueWithoutNotifierSignal(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()

	receiverAgentUUID := "receiver-agent-uuid"
	queuedMessage := model.Message{
		MessageID:     "message-no-notify",
		FromAgentUUID: "sender-agent-uuid",
		ToAgentUUID:   receiverAgentUUID,
		ContentType:   "text/plain",
		Payload:       "hello over queued transport",
		CreatedAt:     time.Now().UTC(),
	}
	if _, _, err := mem.CreateOrGetMessageRecord(queuedMessage, queuedMessage.CreatedAt); err != nil {
		t.Fatalf("CreateOrGetMessageRecord: %v", err)
	}

	queue := &stagedQueue{
		releaseOnCall: 3, // first two dequeues empty, then deliver without waiter notify
		message:       queuedMessage,
	}
	h := NewHandler(mem, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)

	status, result, handlerErr := h.pullForAgent(context.Background(), receiverAgentUUID, 1500*time.Millisecond)
	if handlerErr != nil {
		t.Fatalf("pullForAgent returned handler error: %+v", handlerErr)
	}
	if status != http.StatusOK {
		t.Fatalf("expected pullForAgent status %d, got %d result=%v", http.StatusOK, status, result)
	}
	if queue.dequeueCalls() < 3 {
		t.Fatalf("expected pullForAgent to recheck queue at least 3 times, got %d", queue.dequeueCalls())
	}

	var message model.Message
	switch typed := result["message"].(type) {
	case model.Message:
		message = typed
	case map[string]any:
		body, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("marshal result.message: %v", err)
		}
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatalf("decode result.message into model.Message: %v", err)
		}
	default:
		t.Fatalf("expected result.message payload, got %T", result["message"])
	}
	if message.MessageID != queuedMessage.MessageID {
		t.Fatalf("expected message_id %q, got %q", queuedMessage.MessageID, message.MessageID)
	}
}

type stagedQueue struct {
	mu            sync.Mutex
	releaseOnCall int
	calls         int
	message       model.Message
}

func (q *stagedQueue) Enqueue(_ context.Context, _ model.Message) error {
	return nil
}

func (q *stagedQueue) Dequeue(_ context.Context, _ string) (model.Message, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.calls++
	if q.calls < q.releaseOnCall {
		return model.Message{}, false, nil
	}
	if strings.TrimSpace(q.message.MessageID) == "" {
		return model.Message{}, false, nil
	}
	message := q.message
	q.message = model.Message{}
	return message, true, nil
}

func (q *stagedQueue) dequeueCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}
