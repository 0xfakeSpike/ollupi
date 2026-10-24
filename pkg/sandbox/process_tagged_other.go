//go:build unix && !darwin

package sandbox

func killTagged(string, int) {}
