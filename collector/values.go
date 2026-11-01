package collector

// boolValue renders a flag as a gauge value: 1 for true, 0 for false.
func boolValue(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
