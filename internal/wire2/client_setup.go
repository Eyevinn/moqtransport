package wire2

type ClientSetup struct {
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *ClientSetup) Type() ControlMessageType {
	return ControlMessageTypeClientSetup
}

type ServerSetup struct {
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *ServerSetup) Type() ControlMessageType {
	return ControlMessageTypeServerSetup
}

type GoAway struct {
	NewSessionURI string `proto:"tlv_string"`
}

func (m *GoAway) Type() ControlMessageType {
	return ControlMessageTypeGoAway
}

type MaxRequestID struct {
	MaxRequestID uint64 `proto:"quicvarint"`
}

func (m *MaxRequestID) Type() ControlMessageType {
	return ControlMessageTypeMaxRequestID
}

type RequestsBlocked struct {
	MaxRequestID uint64 `proto:"quicvarint"`
}

func (m *RequestsBlocked) Type() ControlMessageType {
	return ControlMessageTypeRequestsBlocked
}

type RequestOk struct {
	RequestID  uint64         `proto:"quicvarint"`
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *RequestOk) Type() ControlMessageType {
	return ControlMessageTypeRequestOk
}

type RequestError struct {
	RequestID   uint64 `proto:"quicvarint"`
	ErrorCode   uint64 `proto:"quicvarint"`
	ErrorReason string `proto:"tlv_string"`
}

func (m *RequestError) Type() ControlMessageType {
	return ControlMessageTypeRequestError
}

type Subscribe struct {
	RequestID      uint64         `proto:"quicvarint"`
	TrackNamespace [][]byte       `proto:"ntlv_bytes"`
	TrackName      []byte         `proto:"tlv_bytes"`
	Parameters     []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *Subscribe) Type() ControlMessageType {
	return ControlMessageTypeSubscribe
}

type SubscribeOk struct {
	RequestID  uint64         `proto:"quicvarint"`
	TrackAlias uint64         `proto:"quicvarint"`
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *SubscribeOk) Type() ControlMessageType {
	return ControlMessageTypeSubscribeOk
}

type SubscribeUpdate struct {
	RequestID             uint64         `proto:"quicvarint"`
	SubscriptionRequestID uint64         `proto:"quicvarint"`
	Parameters            []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *SubscribeUpdate) Type() ControlMessageType {
	return ControlMessageTypeSubscribeUpdate
}

type Unsubscribe struct {
	RequestID uint64 `proto:"quicvarint"`
}

func (m *Unsubscribe) Type() ControlMessageType {
	return ControlMessageTypeUnsubscribe
}

type Publish struct {
	RequestID      uint64         `proto:"quicvarint"`
	TrackNamespace [][]byte       `proto:"ntlv_bytes"`
	TrackName      []byte         `proto:"tlv_bytes"`
	TrackAlias     uint64         `proto:"quicvarint"`
	Parameters     []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *Publish) Type() ControlMessageType {
	return ControlMessageTypePublish
}

type PublishOk struct {
	RequestID  uint64         `proto:"quicvarint"`
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *PublishOk) Type() ControlMessageType {
	return ControlMessageTypePublishOk
}

type PublishDone struct {
	RequestID   uint64 `proto:"quicvarint"`
	StatusCode  uint64 `proto:"quicvarint"`
	StreamCount uint64 `proto:"quicvarint"`
	ErrorReason string `proto:"tlv_string"`
}

func (m *PublishDone) Type() ControlMessageType {
	return ControlMessageTypePublishDone
}

type Fetch struct {
	// TODO
}

func (m *Fetch) Type() ControlMessageType {
	return ControlMessageTypeFetch
}

type FetchOk struct {
	RequestID  uint64         `proto:"quicvarint"`
	EndOfTrack bool           `proto:"bool"`
	EndGroup   uint64         `proto:"quicvarint"`
	EndObject  uint64         `proto:"quicvarint"`
	Parameters []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *FetchOk) Type() ControlMessageType {
	return ControlMessageTypeFetchOk
}

type FetchCancel struct {
	RequestID uint64 `proto:"quicvarint"`
}

func (m *FetchCancel) Type() ControlMessageType {
	return ControlMessageTypeFetchCancel
}

type TrackStatus struct {
	RequestID      uint64         `proto:"quicvarint"`
	TrackNamespace [][]byte       `proto:"ntlv_bytes"`
	TrackName      []byte         `proto:"tlv_bytes"`
	Parameters     []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *TrackStatus) Type() ControlMessageType {
	return ControlMessageTypeTrackStatus
}

type PublishNamespace struct {
	RequestID      uint64         `proto:"quicvarint"`
	TrackNamespace [][]byte       `proto:"ntlv_bytes"`
	Parameters     []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *PublishNamespace) Type() ControlMessageType {
	return ControlMessageTypePublishNamespace
}

type PublishNamespaceDone struct {
	TrackNamespace [][]byte `proto:"ntlv_bytes"`
}

func (m *PublishNamespaceDone) Type() ControlMessageType {
	return ControlMessageTypePublishNamespaceDone
}

type PublishNamespaceCancel struct {
	TrackNamespace [][]byte `proto:"ntlv_bytes"`
	ErrorCode      uint64   `proto:"quicvarint"`
	ErrorReason    string   `proto:"tlv_string"`
}

func (m *PublishNamespaceCancel) Type() ControlMessageType {
	return ControlMessageTypePublishNamespaceCancel
}

type SubscribeNamespace struct {
	RequestID            uint64         `proto:"quicvarint"`
	TrackNamespacePrefix [][]byte       `proto:"ntlv_bytes"`
	Parameters           []KeyValuePair `proto:"moq_kvp_list"`
}

func (m *SubscribeNamespace) Type() ControlMessageType {
	return ControlMessageTypeSubscribeNamespace
}

type UnsubscribeNamespace struct {
	RequestID uint64 `proto:"quicvarint"`
}

func (m *UnsubscribeNamespace) Type() ControlMessageType {
	return ControlMessageTypeUnsubscribeNamespace
}
