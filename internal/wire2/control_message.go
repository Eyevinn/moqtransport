package wire2

const (
	ControlMessageTypeClientSetup ControlMessageType = 0x20
	ControlMessageTypeServerSetup ControlMessageType = 0x21

	ControlMessageTypeGoAway ControlMessageType = 0x10

	ControlMessageTypeMaxRequestID    ControlMessageType = 0x15
	ControlMessageTypeRequestsBlocked ControlMessageType = 0x1a

	ControlMessageTypeRequestOk    ControlMessageType = 0x7
	ControlMessageTypeRequestError ControlMessageType = 0x5

	ControlMessageTypeSubscribe       ControlMessageType = 0x3
	ControlMessageTypeSubscribeOk     ControlMessageType = 0x4
	ControlMessageTypeSubscribeUpdate ControlMessageType = 0x2
	ControlMessageTypeUnsubscribe     ControlMessageType = 0xa

	ControlMessageTypePublish     ControlMessageType = 0x1d
	ControlMessageTypePublishOk   ControlMessageType = 0x1e
	ControlMessageTypePublishDone ControlMessageType = 0xb

	ControlMessageTypeFetch       ControlMessageType = 0x16
	ControlMessageTypeFetchOk     ControlMessageType = 0x18
	ControlMessageTypeFetchCancel ControlMessageType = 0x17

	ControlMessageTypeTrackStatus ControlMessageType = 0xd

	ControlMessageTypePublishNamespace       ControlMessageType = 0x6
	ControlMessageTypePublishNamespaceDone   ControlMessageType = 0x9
	ControlMessageTypePublishNamespaceCancel ControlMessageType = 0xc

	ControlMessageTypeSubscribeNamespace   ControlMessageType = 0x11
	ControlMessageTypeUnsubscribeNamespace ControlMessageType = 0x14
)

type ControlMessageType uint64

type ControlMessage interface {
	Type() ControlMessageType
	append_v15([]byte) []byte
	parse_v15([]byte) error
}
