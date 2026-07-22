package filter

import (
	"fmt"
	"slices"

	"github.com/jv-k/gh-runs/v2/internal/domain"
)

// ParseStatus classifies one permissive -s value into the set that owns it, and
// rejects an unrecognised value by name (cli-surface R6). The 15 accepted values
// are the six Statuses plus the nine Conclusions, disjoint, so one input belongs
// to exactly one enum: found in a list, it is appended to that set; found in
// neither, it is rejected before any request is built. It appends rather than
// replaces, so a multi-select input builds the pair value by value.
//
// It is the single validation point for every consumer. A typo is rejected by
// this code with this message whether it arrived from a flag, the Feed's filter
// input, or a Purge command, and it validates against domain's own value lists
// so the accepted set and the vocabulary cannot drift.
func (f *Filter) ParseStatus(value string) error {
	if slices.Contains(domain.StatusValues(), domain.Status(value)) {
		f.Statuses = append(f.Statuses, domain.Status(value))
		return nil
	}
	if slices.Contains(domain.ConclusionValues(), domain.Conclusion(value)) {
		f.Conclusions = append(f.Conclusions, domain.Conclusion(value))
		return nil
	}
	return fmt.Errorf("unknown status or conclusion %q", value)
}
