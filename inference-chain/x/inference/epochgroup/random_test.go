package epochgroup

import (
	"sort"
	"testing"
)

func FuzzCanFindUpperBound(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4}, 2)
	f.Fuzz(func(t *testing.T, data []byte, needle int) {
		haystack := make([]int, len(data))
		for i, b := range data {
			haystack[i] = int(b)
		}

		sort.Ints(haystack)

		i := upperBound(needle, haystack)

		if i < 0 || i > len(haystack) {
			t.Fatalf("invalid index %d for len=%d", i, len(haystack))
		}

		for j := range i {
			if haystack[j] > needle {
				t.Fatalf("a[%d]=%d > x=%d", j, haystack[j], needle)
			}
		}
		if i < len(haystack) && haystack[i] <= needle {
			t.Fatalf("a[%d]=%d <= x=%d", i, haystack[i], needle)
		}
	})
}
