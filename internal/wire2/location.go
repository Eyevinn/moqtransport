package wire2

import (
	"fmt"

	"github.com/Eyevinn/locmaf/vi64"
)

// Location identifies an Object within a Group of a Track
// (draft-ietf-moq-transport-18, Section 1.4.2).
type Location struct {
	Group  uint64
	Object uint64
}

func (l Location) String() string {
	return fmt.Sprintf("{group: %v, object: %v}", l.Group, l.Object)
}

func (l Location) append(buf []byte) []byte {
	buf = vi64.Append(buf, l.Group)
	return vi64.Append(buf, l.Object)
}

// parse reads a Location from the start of data and returns the number of
// bytes consumed.
func (l *Location) parse(data []byte) (int, error) {
	group, n, err := vi64.Parse(data)
	if err != nil {
		return n, err
	}
	object, m, err := vi64.Parse(data[n:])
	if err != nil {
		return n + m, err
	}
	l.Group, l.Object = group, object
	return n + m, nil
}
