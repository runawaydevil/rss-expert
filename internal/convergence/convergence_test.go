package convergence

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func hash(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestNothingToConverge(t *testing.T) {
	if _, err := Resolve(nil); !errors.Is(err, ErrNoCandidates) {
		t.Errorf("got %v, want ErrNoCandidates", err)
	}
}

func TestSingleCandidateWinsTrivially(t *testing.T) {
	only := Candidate{ID: 7, Updated: at("2026-07-23T12:00:00Z"), ContentHash: hash("a")}
	got, err := Resolve([]Candidate{only})
	if err != nil {
		t.Fatal(err)
	}
	if got.Winner.ID != 7 || got.Reason != ReasonOnlyCandidate {
		t.Errorf("got %+v", got)
	}
}

func TestAuthorOriginBeatsEverything(t *testing.T) {
	thirdParty := Candidate{
		ID: 1, ClaimedByAuthor: false,
		Updated: at("2026-07-23T18:00:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("aaa"),
	}
	fromAuthor := Candidate{
		ID: 2, ClaimedByAuthor: true,
		Updated: at("2026-07-23T09:00:00Z"), Fidelity: FidelityHTML, ContentHash: hash("zzz"),
	}

	got, err := Resolve([]Candidate{thirdParty, fromAuthor})
	if err != nil {
		t.Fatal(err)
	}
	if got.Winner.ID != 2 {
		t.Errorf("winner = %d, want the author's own copy even though it is older and thinner", got.Winner.ID)
	}
	if got.Reason != ReasonAuthorOrigin {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestNewerWinsWhenOriginIsEqual(t *testing.T) {
	older := Candidate{ID: 1, Updated: at("2026-07-23T09:00:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("aaa")}
	newer := Candidate{ID: 2, Updated: at("2026-07-23T18:00:00Z"), Fidelity: FidelityHTML, ContentHash: hash("zzz")}

	got, _ := Resolve([]Candidate{older, newer})
	if got.Winner.ID != 2 || got.Reason != ReasonNewer {
		t.Errorf("got %+v, want the newer observation", got)
	}
}

func TestFidelityBreaksATimeTie(t *testing.T) {
	when := at("2026-07-23T12:00:00Z")
	htmlOnly := Candidate{ID: 1, Updated: when, Fidelity: FidelityHTML, ContentHash: hash("aaa")}
	withMarkdown := Candidate{ID: 2, Updated: when, Fidelity: FidelityMarkdown, ContentHash: hash("zzz")}

	got, _ := Resolve([]Candidate{htmlOnly, withMarkdown})
	if got.Winner.ID != 2 || got.Reason != ReasonFidelity {
		t.Errorf("got %+v, want the copy carrying markdown", got)
	}
}

func TestHashBreaksEveryOtherTie(t *testing.T) {
	when := at("2026-07-23T12:00:00Z")
	high := Candidate{ID: 1, Updated: when, Fidelity: FidelityHTML, ContentHash: []byte{0xff, 0x00}}
	low := Candidate{ID: 2, Updated: when, Fidelity: FidelityHTML, ContentHash: []byte{0x00, 0xff}}

	got, _ := Resolve([]Candidate{high, low})
	if got.Winner.ID != 2 || got.Reason != ReasonHash {
		t.Errorf("got %+v, want the lower hash", got)
	}
}

func TestOrderNeverChangesTheOutcome(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	for round := 0; round < 400; round++ {
		n := 2 + rng.Intn(6)
		candidates := make([]Candidate, n)
		for i := range candidates {
			candidates[i] = Candidate{
				ID:              int64(i + 1),
				ClaimedByAuthor: rng.Intn(3) == 0,
				Updated:         at("2026-07-23T12:00:00Z").Add(time.Duration(rng.Intn(4)) * time.Hour),
				Fidelity:        rng.Intn(3),
				ContentHash:     hash(fmt.Sprintf("body-%d-%d", round, i)),
			}
		}

		want, err := Resolve(candidates)
		if err != nil {
			t.Fatal(err)
		}

		for shuffle := 0; shuffle < 12; shuffle++ {
			mixed := make([]Candidate, len(candidates))
			copy(mixed, candidates)
			rng.Shuffle(len(mixed), func(i, j int) { mixed[i], mixed[j] = mixed[j], mixed[i] })

			got, err := Resolve(mixed)
			if err != nil {
				t.Fatal(err)
			}
			if got.Winner.ID != want.Winner.ID {
				t.Fatalf("round %d: shuffling changed the winner from %d to %d\ncandidates: %+v",
					round, want.Winner.ID, got.Winner.ID, candidates)
			}
			if got.Reason != want.Reason {
				t.Fatalf("round %d: shuffling changed the reason from %q to %q",
					round, want.Reason, got.Reason)
			}
		}
	}
}

func TestTwoInstancesAgreeFromDifferentArrivalOrders(t *testing.T) {
	fromAuthorFeed := Candidate{
		ID: 1, ClaimedByAuthor: true,
		Updated: at("2026-07-23T18:05:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("v3"),
	}
	fromFirehose := Candidate{
		ID: 2, ClaimedByAuthor: false,
		Updated: at("2026-07-23T18:04:00Z"), Fidelity: FidelityHTML, ContentHash: hash("v2"),
	}
	fromPush := Candidate{
		ID: 3, ClaimedByAuthor: true,
		Updated: at("2026-07-23T18:05:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("v3"),
	}

	instanceA, _ := Resolve([]Candidate{fromPush, fromFirehose, fromAuthorFeed})
	instanceB, _ := Resolve([]Candidate{fromFirehose, fromAuthorFeed, fromPush})

	if string(instanceA.Winner.ContentHash) != string(instanceB.Winner.ContentHash) {
		t.Fatalf("two instances disagreed: %x vs %x", instanceA.Winner.ContentHash, instanceB.Winner.ContentHash)
	}
	if string(instanceA.Winner.ContentHash) != string(hash("v3")) {
		t.Errorf("both agreed on the wrong version: %x", instanceA.Winner.ContentHash)
	}
}

func TestAnEditNeverRegresses(t *testing.T) {
	original := Candidate{
		ID: 1, ClaimedByAuthor: true,
		Updated: at("2026-07-23T10:00:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("first draft"),
	}
	edited := Candidate{
		ID: 2, ClaimedByAuthor: true,
		Updated: at("2026-07-23T11:30:00Z"), Fidelity: FidelityMarkdown, ContentHash: hash("corrected"),
	}

	afterSlowPoll, _ := Resolve([]Candidate{edited, original})
	if string(afterSlowPoll.Winner.ContentHash) != string(hash("corrected")) {
		t.Error("a slow poll of the old version overwrote the edit")
	}
}

func FuzzResolveIsTotal(f *testing.F) {
	f.Add(uint8(3), uint64(1), uint64(2), uint64(3))

	f.Fuzz(func(t *testing.T, count uint8, a, b, c uint64) {
		n := int(count%8) + 1
		seeds := []uint64{a, b, c}

		candidates := make([]Candidate, n)
		for i := range candidates {
			seed := seeds[i%len(seeds)] + uint64(i)
			candidates[i] = Candidate{
				ID:              int64(i + 1),
				ClaimedByAuthor: seed%2 == 0,
				Updated:         time.Unix(int64(seed%1_000_000), 0).UTC(),
				Fidelity:        int(seed % 3),
				ContentHash:     hash(fmt.Sprint(seed)),
			}
		}

		first, err := Resolve(candidates)
		if err != nil {
			t.Fatalf("Resolve refused a non-empty slice: %v", err)
		}
		if first.Reason == "" {
			t.Fatal("Resolve returned no reason")
		}

		reversed := make([]Candidate, n)
		for i := range candidates {
			reversed[n-1-i] = candidates[i]
		}
		second, err := Resolve(reversed)
		if err != nil {
			t.Fatal(err)
		}
		if string(first.Winner.ContentHash) != string(second.Winner.ContentHash) {
			t.Fatalf("reversing the order changed the winner")
		}
	})
}
