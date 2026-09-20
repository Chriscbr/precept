package discover

import (
	"errors"
	"fmt"
)

// ValidateClaimIDs requires IDs to be unique within each source file. Reusing an
// ID in different files is allowed. All duplicate groups are reported in source
// discovery order, including collisions between explicit and generated IDs.
func ValidateClaimIDs(claims []Claim) error {
	type key struct{ file, id string }
	counts := make(map[key]int)
	var order []key
	for _, claim := range claims {
		k := key{file: claim.File, id: claim.ID}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	var duplicates []error
	for _, k := range order {
		count := counts[k]
		if count < 2 {
			continue
		}
		frequency := "twice"
		if count > 2 {
			frequency = fmt.Sprintf("%d times", count)
		}
		duplicates = append(duplicates, fmt.Errorf("claim ID %q is used %s in the same file (%s)", k.id, frequency, k.file))
	}
	return errors.Join(duplicates...)
}
