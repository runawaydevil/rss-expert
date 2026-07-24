package convergence

import (
	"bytes"
	"errors"
	"time"
)

const (
	FidelityNothing  = 0
	FidelityHTML     = 1
	FidelityMarkdown = 2
)

type Reason string

const (
	ReasonOnlyCandidate = "only observation"
	ReasonAuthorOrigin  = "came from the author's own domain"
	ReasonNewer         = "carries the later updated time"
	ReasonFidelity      = "carries more of the original than the others"
	ReasonHash          = "tied on every rule; lowest content hash wins"
)

var ErrNoCandidates = errors.New("convergence: nothing to converge")

type Candidate struct {
	ID              int64
	ClaimedByAuthor bool
	Updated         time.Time
	Fidelity        int
	ContentHash     []byte
}

type Result struct {
	Winner Candidate
	Reason Reason
}

func Resolve(candidates []Candidate) (Result, error) {
	if len(candidates) == 0 {
		return Result{}, ErrNoCandidates
	}
	if len(candidates) == 1 {
		return Result{Winner: candidates[0], Reason: ReasonOnlyCandidate}, nil
	}

	winner, at := best(candidates)

	rest := make([]Candidate, 0, len(candidates)-1)
	rest = append(rest, candidates[:at]...)
	rest = append(rest, candidates[at+1:]...)
	runnerUp, _ := best(rest)

	_, reason := beats(winner, runnerUp)
	return Result{Winner: winner, Reason: reason}, nil
}

func best(candidates []Candidate) (Candidate, int) {
	winner, at := candidates[0], 0
	for i, other := range candidates[1:] {
		if better, _ := beats(other, winner); better {
			winner, at = other, i+1
		}
	}
	return winner, at
}

func beats(a, b Candidate) (bool, Reason) {
	if a.ClaimedByAuthor != b.ClaimedByAuthor {
		return a.ClaimedByAuthor, ReasonAuthorOrigin
	}
	if !a.Updated.Equal(b.Updated) {
		return a.Updated.After(b.Updated), ReasonNewer
	}
	if a.Fidelity != b.Fidelity {
		return a.Fidelity > b.Fidelity, ReasonFidelity
	}
	return bytes.Compare(a.ContentHash, b.ContentHash) < 0, ReasonHash
}
