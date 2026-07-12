package eventmap

import "fmt"

func errf(format string, args ...any) error {
	return fmt.Errorf("eventmap: "+format, args...)
}
