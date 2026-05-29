package service

import "testing"

func TestPSComputeValidityDaysAcceptsSingularAndPluralUnits(t *testing.T) {
	tests := []struct {
		unit string
		want int
	}{
		{"day", 1},
		{"days", 1},
		{"week", 7},
		{"weeks", 7},
		{"month", 30},
		{"months", 30},
		{"year", 365},
		{"years", 365},
	}

	for _, tt := range tests {
		t.Run(tt.unit, func(t *testing.T) {
			if got := psComputeValidityDays(1, tt.unit); got != tt.want {
				t.Fatalf("psComputeValidityDays(1, %q) = %d, want %d", tt.unit, got, tt.want)
			}
		})
	}
}
