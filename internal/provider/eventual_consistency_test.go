package googleworkspace

import (
	"testing"
	"time"
)

func TestConsistencyCheckReachedConsistency(t *testing.T) {
	// We'll test that there were 3 inserts
	numInserts := 3

	cc := consistencyCheck{
		timeout:        time.Duration(time.Minute * 5),
		currConsistent: 1,
		etagChanges:    1,
		lastEtag:       "12345",
	}

	// So far we've seen one etag and it's been consistent once
	if cc.reachedConsistency(numInserts) {
		t.Errorf("Failed: reached consistency (numInserts: %d, currConsistent: %d, etagChanges: %d, timeout: %d)", numInserts, cc.currConsistent, cc.etagChanges, int(cc.timeout.Minutes()))
	}

	// We only have 2 previous Etags, but we've been consistent for 3 minutes
	// We'll assume it's consistent and that one of the inserts already contained
	// and updated etag that we're missing
	cc.etagChanges = 2
	cc.currConsistent = 18

	if !cc.reachedConsistency(numInserts) {
		t.Errorf("Failed: did not reach consistency (numInserts: %d, currConsistent: %d, etagChanges: %d, timeout: %d)", numInserts, cc.currConsistent, cc.etagChanges, int(cc.timeout.Minutes()))
	}

	// We've seen all the inserts come through, but we haven't had 4 consistent tags yet
	cc.etagChanges = 3
	cc.currConsistent = 1

	if cc.reachedConsistency(numInserts) {
		t.Errorf("Failed: reached consistency (numInserts: %d, currConsistent: %d, etagChanges: %d, timeout: %d)", numInserts, cc.currConsistent, cc.etagChanges, int(cc.timeout.Minutes()))
	}

	// We've seen all the inserts come through, and it's been consistent 4 times
	cc.currConsistent = 4

	if !cc.reachedConsistency(numInserts) {
		t.Errorf("Failed: did not reach consistency (numInserts: %d, currConsistent: %d, etagChanges: %d, timeout: %d)", numInserts, cc.currConsistent, cc.etagChanges, int(cc.timeout.Minutes()))
	}
}

func TestConsistencyHandleNewEtag(t *testing.T) {
	cc := consistencyCheck{
		resourceType: "test",
	}

	cc.handleNewEtag("12345")
	if cc.currConsistent != 0 {
		t.Errorf("Failed ['12345']: new etag shows currConsistent > 0 (%d)", cc.currConsistent)
	}

	cc.handleNewEtag("abcde")
	if cc.lastEtag != "abcde" {
		t.Errorf("Failed ['abcde']: ends with incorrect lastEtag (%s)", cc.lastEtag)
	}

	cc.handleNewEtag("54321")
	if cc.etagChanges != 3 {
		t.Errorf("Failed ['abcde']: shows more/less etag changes (expected: %d, got: %d)", 3, cc.etagChanges)
	}
}

// consistencyRead is one simulated answer to the Get in a consistency loop:
// a status code and, for a 200, the etag returned.
type consistencyRead struct {
	code int
	etag string
}

// pollsToConsistency drives cc through reads the same way the group member
// create loop does, and returns the number of reads made before
// reachedConsistency(numInserts) returned true, or -1 if it never did.
func pollsToConsistency(cc *consistencyCheck, numInserts int, reads []consistencyRead) int {
	for i, r := range reads {
		if cc.reachedConsistency(numInserts) {
			return i
		}

		switch r.code {
		case 304:
			cc.currConsistent += 1
		case 404:
			cc.handleNotFound()
		case 200:
			cc.handleEtag(r.etag)
		}
	}

	if cc.reachedConsistency(numInserts) {
		return len(reads)
	}
	return -1
}

func repeatReads(n int, pattern ...consistencyRead) []consistencyRead {
	var reads []consistencyRead
	for len(reads) < n {
		reads = append(reads, pattern...)
	}
	return reads[:n]
}

func TestConsistencyFlappingReads(t *testing.T) {
	ok := func(etag string) consistencyRead { return consistencyRead{code: 200, etag: etag} }
	notModified := consistencyRead{code: 304}
	notFound := consistencyRead{code: 404}

	// A 5 minute timeout polls about every 10 seconds, so about 30 reads.
	const maxReads = 30

	cases := []struct {
		name  string
		reads []consistencyRead
		// want is the number of reads before consistency, or -1 for never
		want int
	}{
		{
			name:  "steady",
			reads: append([]consistencyRead{ok("A")}, repeatReads(maxReads-1, notModified)...),
			want:  5,
		},
		{
			name:  "404 after a successful read does not reset",
			reads: append([]consistencyRead{ok("A")}, repeatReads(maxReads-1, notFound, notModified)...),
			want:  9,
		},
		{
			name:  "etag flips back to one already seen",
			reads: append([]consistencyRead{ok("A"), ok("B"), ok("A")}, repeatReads(maxReads-3, notModified)...),
			want:  6,
		},
		{
			name:  "reads alternate between two etags",
			reads: repeatReads(maxReads, ok("A"), ok("B")),
			want:  6,
		},
		{
			name:  "404 before the first successful read still resets",
			reads: append([]consistencyRead{notFound, notFound, ok("A")}, repeatReads(maxReads-3, notModified)...),
			want:  7,
		},
		{
			name:  "never found",
			reads: repeatReads(maxReads, notFound),
			want:  -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := consistencyCheck{
				resourceType: "test",
				timeout:      time.Duration(time.Minute * 5),
			}

			got := pollsToConsistency(&cc, 1, tc.reads)
			if got != tc.want {
				t.Errorf("Failed: reached consistency after %d reads, expected %d (currConsistent: %d, etagChanges: %d)", got, tc.want, cc.currConsistent, cc.etagChanges)
			}
		})
	}
}

func TestConsistencyHandleEtag(t *testing.T) {
	cc := consistencyCheck{
		resourceType: "test",
	}

	cc.handleEtag("A")
	cc.currConsistent = 2

	// An etag that was already seen counts as consistent
	cc.handleEtag("A")
	if cc.currConsistent != 3 || cc.etagChanges != 1 {
		t.Errorf("Failed ['A']: seen etag was handled as a change (currConsistent: %d, etagChanges: %d)", cc.currConsistent, cc.etagChanges)
	}

	// An etag that was not seen yet is a change and resets the count
	cc.handleEtag("B")
	if cc.currConsistent != 0 || cc.etagChanges != 2 || cc.lastEtag != "B" {
		t.Errorf("Failed ['B']: new etag was not handled as a change (currConsistent: %d, etagChanges: %d, lastEtag: %s)", cc.currConsistent, cc.etagChanges, cc.lastEtag)
	}

	// Going back to an etag that was seen before counts as consistent and becomes lastEtag
	cc.handleEtag("A")
	if cc.currConsistent != 1 || cc.etagChanges != 2 || cc.lastEtag != "A" {
		t.Errorf("Failed ['A']: etag seen before was not counted as consistent (currConsistent: %d, etagChanges: %d, lastEtag: %s)", cc.currConsistent, cc.etagChanges, cc.lastEtag)
	}
}
