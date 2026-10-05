package statefile

// seen reports whether record r was in the context that step s was built from.
func seen(r, s Record) bool { return r.N <= s.Upto }

func isBlock(k Kind) bool { return k == Text || k == Thinking || k == Call }

// Build reorders a file into context order and returns it with the index the
// next step's upto must carry. File order is time order; the API wants each
// step's results directly after it. So for each step: the results it saw,
// then the messages it saw as one user turn, then its blocks, then the step
// record itself. Blocks no step closed are dropped. Start and config never
// enter.
func Build(chain []Record) ([]Record, int) {
	var ctx, pending []Record
	blocks := map[string][]Record{}
	flush := func(s *Record) {
		var rest []Record
		for _, p := range pending {
			if s != nil && !seen(p, *s) {
				rest = append(rest, p)
			} else if p.Kind == Result {
				ctx = append(ctx, p)
			}
		}
		for _, p := range pending {
			if p.Kind != Result && (s == nil || seen(p, *s)) {
				ctx = append(ctx, p)
			}
		}
		pending = rest
	}
	for _, r := range chain {
		switch {
		case r.Kind == Start || r.Kind == Config:
		case isBlock(r.Kind):
			blocks[r.Step] = append(blocks[r.Step], r)
		case r.Kind == Step:
			flush(&r)
			ctx = append(ctx, blocks[r.ID]...)
			ctx = append(ctx, r)
			delete(blocks, r.ID)
		default:
			pending = append(pending, r)
		}
	}
	flush(nil)
	return ctx, chain[len(chain)-1].N
}

type Status string

const (
	StatusOpen        Status = "open"        // a message or result is unaddressed. needs a step
	StatusClosed      Status = "closed"      // the last step addressed everything and made no calls
	StatusInterrupted Status = "interrupted" // a closed call has no result and nobody holds the lock
	StatusFailed      Status = "failed"      // the last record is an error
	StatusStepping    Status = "stepping"    // the harness holds the session. Never derived from the file
)

// Waiting returns the calls closed by a step that have no result, in file order.
func Waiting(chain []Record) []Record {
	calls := map[string][]Record{} // step id -> calls
	var waiting []Record
	for _, r := range chain {
		switch r.Kind {
		case Call:
			calls[r.Step] = append(calls[r.Step], r)
		case Step:
			waiting = append(waiting, calls[r.ID]...)
		case Result:
			for i, c := range waiting {
				if c.ID == r.For {
					waiting = append(waiting[:i], waiting[i+1:]...)
					break
				}
			}
		case Start, Config, System, Message, Text, Thinking, Error:
		}
	}
	return waiting
}

// LastStep is the newest step record, or nil.
func LastStep(chain []Record) *Record {
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind == Step {
			return &chain[i]
		}
	}
	return nil
}

// ContextUsed is the size of the last step's prompt in tokens: what the
// context costs now. Input excludes cache reads and writes on every provider.
func ContextUsed(chain []Record) int {
	s := LastStep(chain)
	if s == nil {
		return 0
	}
	return s.Usage.Prompt()
}

// Derive reads status off the tip. Stepping is the lock's word, not the
// file's, so a caller that holds the lock knows better than this.
func Derive(chain []Record) Status {
	if chain[len(chain)-1].Kind == Error {
		return StatusFailed
	}
	if len(Waiting(chain)) > 0 {
		return StatusInterrupted
	}
	last := LastStep(chain)
	for _, r := range chain {
		if (r.Kind == Message || r.Kind == Result) && (last == nil || !seen(r, *last)) {
			return StatusOpen
		}
	}
	return StatusClosed
}
