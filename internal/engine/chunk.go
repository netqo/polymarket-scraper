package engine

// chunk splits ids into batches of at most size.
//
// It sits here rather than beside any one caller because all three of them use
// it for different reasons: spreading tokens across connections, batching a
// metadata seed, and batching a rest-only pass. A non-positive size yields no
// batches rather than panicking, which is what makes it safe to call before the
// configuration that bounds it has been validated.
func chunk(ids []string, size int) [][]string {
	if size <= 0 || len(ids) == 0 {
		return nil
	}

	batches := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
		batches = append(batches, ids[start:end])
	}

	return batches
}
