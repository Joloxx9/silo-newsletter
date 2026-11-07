//go:build !linux

package main

// dropRoot only matters inside the Linux container.
func dropRoot(string) error { return nil }
