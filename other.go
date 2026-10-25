//go:build !linux && !windows && !darwin && !freebsd && !openbsd && !netbsd

package fcam

type stream struct{}

func devices() ([]Device, error) { return nil, errUnsupported }

func modes(string) ([]mode, error) { return nil, errUnsupported }

func open(string, mode) (*stream, error) { return nil, errUnsupported }

func (*stream) next(*picture) (bool, error) { return false, errUnsupported }

func (*stream) close() {}
