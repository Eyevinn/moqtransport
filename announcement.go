package moqtransport

import "github.com/Eyevinn/moqtransport/internal/wire"

type announcement struct {
	requestID  uint64
	namespace  []string
	parameters wire.KVPList

	response chan error
}
