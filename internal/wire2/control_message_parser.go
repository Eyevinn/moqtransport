package wire2

import (
	"fmt"
	"slices"
)

type ControlMessageParser struct {
	version Version
}

func NewControlMessageParser(v Version) (*ControlMessageParser, error) {
	if !slices.Contains(supportedVersions, v) {
		return nil, errUnsupportedVersion
	}
	cmp := &ControlMessageParser{
		version: v,
	}
	return cmp, nil
}

func (p *ControlMessageParser) Parse(typ ControlMessageType, data []byte) (ControlMessage, error) {
	var msg any
	switch typ {
	case ControlMessageTypeClientSetup:
		msg = &ClientSetup{}
	case ControlMessageTypeServerSetup:
		msg = &ServerSetup{}

	case ControlMessageTypeGoAway:
		msg = &GoAway{}

	case ControlMessageTypeMaxRequestID:
		msg = &MaxRequestID{}
	case ControlMessageTypeRequestsBlocked:
		msg = &RequestsBlocked{}

	case ControlMessageTypeRequestOk:
		msg = &RequestOk{}
	case ControlMessageTypeRequestError:
		msg = &RequestError{}

	case ControlMessageTypeSubscribe:
		msg = &Subscribe{}
	case ControlMessageTypeSubscribeOk:
		msg = &SubscribeOk{}
	case ControlMessageTypeSubscribeUpdate:
		msg = &SubscribeUpdate{}
	case ControlMessageTypeUnsubscribe:
		msg = &Unsubscribe{}

	case ControlMessageTypePublish:
		msg = &Publish{}
	case ControlMessageTypePublishOk:
		msg = &PublishOk{}
	case ControlMessageTypePublishDone:
		msg = &PublishDone{}

	case ControlMessageTypeFetch:
		msg = &Fetch{}
	case ControlMessageTypeFetchOk:
		msg = &FetchOk{}
	case ControlMessageTypeFetchCancel:
		msg = &FetchCancel{}

	case ControlMessageTypeTrackStatus:
		msg = &TrackStatus{}

	case ControlMessageTypePublishNamespace:
		msg = &PublishNamespaceCancel{}
	case ControlMessageTypePublishNamespaceDone:
		msg = &PublishNamespaceDone{}
	case ControlMessageTypePublishNamespaceCancel:
		msg = &PublishNamespaceCancel{}

	case ControlMessageTypeSubscribeNamespace:
		msg = &SubscribeNamespace{}
	case ControlMessageTypeUnsubscribeNamespace:
		msg = &UnsubscribeNamespace{}
	}
	if ctrlMsg, ok := msg.(ControlMessage); ok {
		return ctrlMsg, p.parseAtVersion(ctrlMsg, data)
	}
	return nil, fmt.Errorf("unexpected wire2.ControlMessageType: %#v", typ)
}

func (p *ControlMessageParser) parseAtVersion(msg ControlMessage, data []byte) error {
	switch p.version {
	case DraftVersion15:
		return msg.parse_v15(data)
	}
	return errUnsupportedVersion
}
