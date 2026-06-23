package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"plexus/pkg/effector"
)

// busApprover is the chat agent's interactive Approver (E2.6.4): when the brain
// hits an approval-required effector (e.g. run_command, an ExecArbitrary tool),
// it asks the user over the bus and blocks the cognitive loop until the answer
// arrives. Only the brain's single worker calls RequestApproval, so at most one
// approval is outstanding at a time; the pending map keeps the design general.
//
// resolve() is called from the host's message demux (a different goroutine than
// the blocked worker), which is why the answer can wake the worker even though
// the worker is parked inside RequestApproval.
type busApprover struct {
	// ask sends an approval-request to the control plane (the user), tagged with
	// the given correlation id so the answer can be paired back.
	ask func(corr, description string)
	// newCorr mints a fresh correlation id per request (injected to avoid a
	// time/random dependency in the type itself).
	newCorr func() string

	mu      sync.Mutex
	pending map[string]chan bool
}

func newBusApprover(ask func(corr, description string), newCorr func() string) *busApprover {
	return &busApprover{ask: ask, newCorr: newCorr, pending: map[string]chan bool{}}
}

// RequestApproval asks the user to approve the gated effector call and blocks
// until they answer or ctx is cancelled. It satisfies brain.Approver.
func (a *busApprover) RequestApproval(ctx context.Context, eff effector.Effector, args json.RawMessage) (bool, error) {
	corr := a.newCorr()
	ch := make(chan bool, 1)

	a.mu.Lock()
	a.pending[corr] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, corr)
		a.mu.Unlock()
	}()

	a.ask(corr, fmt.Sprintf("%s wants to run (%s): %s", eff.Name(), eff.Risk(), string(args)))

	select {
	case approved := <-ch:
		return approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// resolve delivers an approval answer to the waiting RequestApproval. It returns
// true if corr matched a pending approval (so the caller knows the message was an
// approval answer, not a normal turn).
func (a *busApprover) resolve(corr string, approved bool) bool {
	a.mu.Lock()
	ch, ok := a.pending[corr]
	a.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approved:
	default: // already answered; ignore duplicates
	}
	return true
}
