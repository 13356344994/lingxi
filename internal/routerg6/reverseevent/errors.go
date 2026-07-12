package reverseevent

import "fmt"

func errf(format string, args ...any) error {
	return fmt.Errorf("reverseevent: "+format, args...)
}
