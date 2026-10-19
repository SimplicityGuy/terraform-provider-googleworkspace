package googleworkspace

import (
	"time"
)

// The number of consistent responses we want before we consider the resource consistent
const numConsistent = 4

type consistencyCheck struct {
	currConsistent int
	etagChanges    int
	lastEtag       string
	resourceType   string
	// seenEtags holds every etag returned by a successful read, see handleEtag
	seenEtags map[string]bool
	// timeout should be set to the timeout of the action
	timeout time.Duration
}

func (cc *consistencyCheck) reachedConsistency(numInserts int) bool {
	// Ideally we will get a min number of consistent tags and >= numInserts
	// However, there are cases where we'll have multiple Inserts, and the
	// initial changes were already consistent by the time the latter
	// inserts happened, thus once we start polling, those changes
	// wouldn't be counted. In which case, we have a maxConsistent
	// that we'll assume is good.

	// max consistent will be set to the timeout * 6 / 2 (or, every 10 seconds for half the timeout time)
	// so that it checks that at least the last half of responses were consistent
	maxConsistent := int(cc.timeout.Minutes()) * 6 / 2

	return (cc.currConsistent == numConsistent && cc.etagChanges >= numInserts) ||
		cc.currConsistent >= maxConsistent
}

func (cc *consistencyCheck) handleNewEtag(etag string) {
	cc.currConsistent = 0
	cc.lastEtag = etag
	cc.etagChanges += 1
}

// handleEtag records the etag of a successful read. A new etag is handled as
// a change (see handleNewEtag). An etag that was already seen is not a change:
// right after a write, reads can reach replicas that are at different points,
// so the answers can alternate between two etags. Count such a read as
// consistent instead of starting again, otherwise the check can fail to reach
// consistency for a resource that already exists.
func (cc *consistencyCheck) handleEtag(etag string) {
	if cc.seenEtags[etag] {
		cc.currConsistent += 1
		cc.lastEtag = etag
		return
	}

	if cc.seenEtags == nil {
		cc.seenEtags = make(map[string]bool)
	}
	cc.seenEtags[etag] = true
	cc.handleNewEtag(etag)
}

// handleNotFound records a read that returned 404. Before any successful read,
// the resource is not visible yet, so we start again. After a successful read,
// the 404 comes from a replica that has not caught up; it does not count as
// consistent, but it does not discard the consistent reads already counted.
func (cc *consistencyCheck) handleNotFound() {
	if cc.etagChanges == 0 {
		cc.currConsistent = 0
	}
}
