//go:build !windows

package winsys

// RegisterURLProtocol gibt es nur unter Windows.
func RegisterURLProtocol(scheme, exe, description string) (changed bool, err error) {
	return false, ErrUnsupported
}
