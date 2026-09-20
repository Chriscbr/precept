package discover

import (
	"strings"
	"testing"
)

func TestValidateClaimIDs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		claims []Claim
		want   string
	}{
		{name: "empty"},
		{name: "unique within file", claims: []Claim{{ID: "one", File: "a.go"}, {ID: "two", File: "a.go"}}},
		{name: "same ID across files", claims: []Claim{{ID: "shared", File: "a.go"}, {ID: "shared", File: "b.go"}}},
		{name: "case sensitive", claims: []Claim{{ID: "shared", File: "a.go"}, {ID: "Shared", File: "a.go"}}},
		{
			name: "all duplicate groups in source order",
			claims: []Claim{
				{ID: "second", File: "a.go"}, {ID: "second", File: "a.go"}, {ID: "second", File: "a.go"},
				{ID: "first", File: "a.go"}, {ID: "first", File: "a.go"},
				{ID: "second", File: "b.go"}, {ID: "second", File: "b.go"},
			},
			want: strings.Join([]string{
				`claim ID "second" is used 3 times in the same file (a.go)`,
				`claim ID "first" is used twice in the same file (a.go)`,
				`claim ID "second" is used twice in the same file (b.go)`,
			}, "\n"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateClaimIDs(test.claims)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != test.want {
				t.Fatalf("ValidateClaimIDs() = %v, want %s", err, test.want)
			}
		})
	}
}
