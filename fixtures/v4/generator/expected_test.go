package workload

import "testing"

// TestMoney locks the display rendering of minor units, negatives included.
func TestMoney(t *testing.T) {
	for _, c := range []struct {
		minor int64
		scale int
		want  string
	}{
		{123456, 2, "1234.56"},
		{5, 2, "0.05"},
		{0, 2, "0.00"},
		{-123456, 2, "-1234.56"},
		{-50, 2, "-0.50"},
		{1234, 0, "1234"},
		{-1234, 0, "-1234"},
	} {
		if got := money(c.minor, c.scale); got != c.want {
			t.Errorf("money(%d, %d) = %q, want %q", c.minor, c.scale, got, c.want)
		}
	}
}
