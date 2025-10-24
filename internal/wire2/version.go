package wire2

import "errors"

var errUnsupportedVersion = errors.New("unsupported version")

type Version int

const (
	DraftVersion15 Version = 15
)

var supportedVersions = []Version{
	DraftVersion15,
}
