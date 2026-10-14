package googleworkspace

import (
	"fmt"
	"reflect"
	"testing"

	"google.golang.org/api/cloudidentity/v1"
)

func strp(s string) *string { return &s }

func TestDynamicGroupPendingFields(t *testing.T) {
	oldQuery := "user.org_units.exists(org_unit, org_unit.org_unit_id==orgUnitId('old'))"
	newQuery := "user.org_units.exists(org_unit, org_unit.org_unit_id==orgUnitId('new'))"

	liveWith := func(name, desc, query string) *cloudidentity.Group {
		g := &cloudidentity.Group{DisplayName: name, Description: desc}
		if query != "" {
			g.DynamicGroupMetadata = &cloudidentity.DynamicGroupMetadata{
				Queries: []*cloudidentity.DynamicGroupQuery{{ResourceType: "USER", Query: query}},
			}
		}
		return g
	}

	cases := []struct {
		name string
		live *cloudidentity.Group
		want dynamicGroupExpected
		exp  []string
	}{
		{
			name: "nothing expected",
			live: liveWith("n", "d", oldQuery),
			want: dynamicGroupExpected{},
			exp:  nil,
		},
		{
			name: "query landed",
			live: liveWith("n", "d", newQuery),
			want: dynamicGroupExpected{Query: strp(newQuery)},
			exp:  nil,
		},
		{
			// The failure mode this test exists for: patch accepted, live query unchanged.
			name: "query still old",
			live: liveWith("n", "d", oldQuery),
			want: dynamicGroupExpected{Query: strp(newQuery)},
			exp:  []string{"query"},
		},
		{
			name: "query missing from live metadata",
			live: liveWith("n", "d", ""),
			want: dynamicGroupExpected{Query: strp(newQuery)},
			exp:  []string{"query"},
		},
		{
			name: "display name and description lag, query fine",
			live: liveWith("old name", "old desc", newQuery),
			want: dynamicGroupExpected{DisplayName: strp("new name"), Description: strp("new desc"), Query: strp(newQuery)},
			exp:  []string{"display_name", "description"},
		},
		{
			name: "nil group",
			live: nil,
			want: dynamicGroupExpected{Query: strp(newQuery)},
			exp:  []string{"group"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dynamicGroupPendingFields(tc.live, tc.want)
			if !reflect.DeepEqual(got, tc.exp) {
				t.Fatalf("pending = %v, want %v", got, tc.exp)
			}
		})
	}
}

// retryTimeDuration only keeps polling for errors IsNotConsistent recognizes;
// the Update path relies on its message matching, so pin that contract here.
func TestDynamicGroupUpdatePendingErrorIsRetried(t *testing.T) {
	err := fmt.Errorf("timed out while waiting for dynamic group update to be reflected: %s not yet updated", "query")
	if !IsNotConsistent(err) {
		t.Fatalf("pending-update error must be classified as retryable, got non-retryable for %q", err)
	}
}
