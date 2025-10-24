package wire2

import "slices"

type ControlMessageSerializer struct {
	version Version
}

func NewControlMessageSerializer(v Version) (*ControlMessageSerializer, error) {
	if !slices.Contains(supportedVersions, v) {
		return nil, errUnsupportedVersion
	}
	cms := &ControlMessageSerializer{
		version: v,
	}
	return cms, nil
}

func (s *ControlMessageSerializer) Append(buf []byte, msg ControlMessage) []byte {
	switch s.version {
	case DraftVersion15:
		return msg.append_v15(buf)
	}
	return nil
}
