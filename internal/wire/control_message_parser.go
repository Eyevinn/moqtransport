package wire

import (
	"bufio"
	"fmt"
	"io"

	"github.com/quic-go/quic-go/quicvarint"
)

type ControlMessageParser struct {
	reader  messageReader
	version Version
}

func NewControlMessageParser(r io.Reader, version Version) *ControlMessageParser {
	return &ControlMessageParser{
		reader:  bufio.NewReader(r),
		version: version,
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
		m = &SubscribeMessage{}
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
		m = &TrackStatusMessage{}
	case messageTypeTrackStatusOk:
		m = &TrackStatusOkMessage{}
	case messageTypeTrackStatusError:
		m = &TrackStatusErrorMessage{}

	case messageTypeAnnounce:
		m = &AnnounceMessage{}
	case messageTypeAnnounceOk:
		m = &AnnounceOkMessage{}
	case messageTypeAnnounceError:
		if p.version.NegotiatedViaALPN() {
			// Draft-16: 0x08 is NAMESPACE, not ANNOUNCE_ERROR
			// TODO: implement NAMESPACE message parsing
			return nil, fmt.Errorf("%w: NAMESPACE (0x08) not yet implemented", errInvalidMessageType)
		}
		m = &AnnounceErrorMessage{}
	case messageTypeUnannounce:
		m = &UnannounceMessage{}
	case messageTypeAnnounceCancel:
		m = &AnnounceCancelMessage{}

	case messageTypeSubscribeAnnounces:
		m = &SubscribeAnnouncesMessage{}
	case messageTypeSubscribeAnnouncesOk:
		m = &SubscribeAnnouncesOkMessage{}
	case messageTypeSubscribeAnnouncesError:
		m = &SubscribeAnnouncesErrorMessage{}
	case messageTypeUnsubscribeAnnounces:
		m = &UnsubscribeAnnouncesMessage{}
	default:
		return nil, fmt.Errorf("%w: %v", errInvalidMessageType, mt)
	}
	err = m.parse(p.version, msg)
	return m, err
}
