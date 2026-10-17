package example

import "sort"

func SortAsc(x []int) {
	sort.Sort(
		sort.Reverse(sort.IntSlice(x)),
	)
}
