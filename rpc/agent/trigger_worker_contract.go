package agent

import "context"

// TriggerProcessor handles exactly one already-claimed source. The worker owns
// lease renewal and failure release; the processor owns source authorization,
// the bounded model attempt, candidate checks and atomic result persistence.
type TriggerProcessor interface {
	Process(context.Context, TriggerLease) error
}
