package routerledger

// Progress is bounded memory-only work for a synchronous Observe callback. Its
// separate mutex cannot wait behind SQLite reads or writes. Start and Finish
// remain the durability boundaries; a crash therefore loses live progress and
// recovered interrupted records explicitly have executionKnown=false.
func (s *Store) Progress(id string, p ProgressState) error {
	if !identifier(id, 128, false) || !validOutcome(Outcome{Status: "processing", Destination: p.Destination, Attempts: p.Attempts, OutputCommitted: p.OutputCommitted, FailureStage: p.FailureStage}, true) {
		return ErrInvalid
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	previous, ok := s.progress[id]
	if !ok {
		return ErrNotFound
	}
	if p.Attempts < previous.Attempts {
		p.Attempts = previous.Attempts
	}
	p.OutputCommitted = p.OutputCommitted || previous.OutputCommitted
	if p.Attempts > 0 {
		p.Destination = "cloud"
	}
	s.progress[id] = p
	return nil
}

func (s *Store) overlay(r Record) Record {
	if r.Status != "processing" {
		return r
	}
	s.progressMu.RLock()
	p, ok := s.progress[r.RequestID]
	s.progressMu.RUnlock()
	if ok {
		r.Attempts = p.Attempts
		r.OutputCommitted = p.OutputCommitted
		r.Destination = p.Destination
		r.FailureStage = p.FailureStage
	}
	return r
}
