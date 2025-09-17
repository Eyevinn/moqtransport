package wire

import (
	"bufio"
	"fmt"
	"io"

	"github.com/quic-go/quic-go/quicvarint"
)

type ControlMessageParser struct {
	reader messageReader
}

func NewControlMessageParser(r io.Reader) *ControlMessageParser {
	return &ControlMessageParser{
		reader: bufio.NewReader(r),
	}
}

func (p *ControlMessageParser) Parse() (ControlMessage, error) {
	mt, err := quicvarint.Read(p.reader)
	if err != nil {
		return nil, err
	}
	hi, err := p.reader.ReadByte()
	if err != nil {
		return nil, err
	}
	lo, err := p.reader.ReadByte()
	if err != nil {
		return nil, err
	}
	length := uint16(hi)<<8 | uint16(lo)

	msg := make([]byte, length)
	n, err := io.ReadFull(p.reader, msg)
	if err != nil {
		return nil, err
	}
	if n != int(length) {
		return nil, errLengthMismatch
	}

	var m ControlMessage
	switch controlMessageType(mt) {
	case messageTypeClientSetup:
		m = &ClientSetupMessage{}
	case messageTypeServerSetup:
		m = &ServerSetupMessage{}

	case messageTypeGoAway:
		m = &GoAwayMessage{}

	case messageTypeMaxRequestID:
		m = &MaxRequestIDMessage{}
	case messageTypeRequestsBlocked:
		m = &RequestsBlockedMessage{}

	case messageTypeSubscribe:
		m = &SubscribeMessage{TrackStatus: false}
	case messageTypeSubscribeOk:
		m = &SubscribeOkMessage{}
	case messageTypeSubscribeError:
		m = &SubscribeErrorMessage{}
	case messageTypeUnsubscribe:
		m = &UnsubscribeMessage{}
	case messageTypeSubscribeUpdate:
		m = &SubscribeUpdateMessage{}

	case messageTypePublishDone:
		m = &PublishDoneMessage{}
	case messageTypePublish:
		m = &PublishMessage{}
	case messageTypePublishOk:
		m = &PublishOkMessage{}
	case messageTypePublishError:
		m = &PublishErrorMessage{}

	case messageTypeFetch:
		m = &FetchMessage{}
	case messageTypeFetchOk:
		m = &FetchOkMessage{}
	case messageTypeFetchError:
		m = &FetchErrorMessage{}
	case messageTypeFetchCancel:
		m = &FetchCancelMessage{}

	case messageTypeTrackStatus:
		m = &SubscribeMessage{TrackStatus: true}
	case messageTypeTrackStatusOk:
		m = &SubscribeOkMessage{}
	case messageTypeTrackStatusError:
		m = &SubscribeErrorMessage{}

	case messageTypePublishNamespace:
		m = &PublishNamespaceMessage{}
	case messageTypePublishNamespaceOk:
		m = &PublishNamespaceOkMessage{}
	case messageTypePublishNamespaceError:
		m = &PublishNamespaceErrorMessage{}
	case messageTypePublishNamespaceDone:
		m = &PublishNamespaceDoneMessage{}
	case messageTypePublishNamespaceCancel:
		m = &PublishNamespaceCancelMessage{}

	case messageTypeSubscribeNamespace:
		m = &SubscribeNamespaceMessage{}
	case messageTypeSubscribeNamespaceOk:
		m = &SubscribeNamespaceOkMessage{}
	case messageTypeSubscribeNamespaceError:
		m = &SubscribeNamespaceErrorMessage{}
	case messageTypeUnsubscribeNamespace:
		m = &UnsubscribeNamespaceMessage{}
	default:
		return nil, fmt.Errorf("%w: %v", errInvalidMessageType, mt)
	}
	err = m.parse(CurrentVersion, msg)
	return m, err
}
