//go:build gomock || generate

package moqtransport

//go:generate sh -c "go run go.uber.org/mock/mockgen -build_flags=\"-tags=gomock\" -typed -package moqtransport -write_package_comment=false -self_package github.com/Eyevinn/moqtransport -destination mock_stream_test.go github.com/Eyevinn/moqtransport Stream"

//go:generate sh -c "go run go.uber.org/mock/mockgen -build_flags=\"-tags=gomock\" -typed -package moqtransport -write_package_comment=false -self_package github.com/Eyevinn/moqtransport -destination mock_receive_stream_test.go github.com/Eyevinn/moqtransport ReceiveStream"

//go:generate sh -c "go run go.uber.org/mock/mockgen -build_flags=\"-tags=gomock\" -typed -package moqtransport -write_package_comment=false -self_package github.com/Eyevinn/moqtransport -destination mock_send_stream_test.go github.com/Eyevinn/moqtransport SendStream"

//go:generate sh -c "go run go.uber.org/mock/mockgen -build_flags=\"-tags=gomock\" -typed -package moqtransport -write_package_comment=false -self_package github.com/Eyevinn/moqtransport -destination mock_connection_test.go github.com/Eyevinn/moqtransport Connection"
