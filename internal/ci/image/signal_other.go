//go:build !linux && !darwin

package image

func handleBrokenPipe() func() {
	return func() {}
}
