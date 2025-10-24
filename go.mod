module github.com/mengelbart/moqtransport

go 1.25.2

require (
	github.com/mengelbart/protogen v0.0.0
	github.com/mengelbart/qlog v0.1.0
	github.com/quic-go/quic-go v0.53.0
	github.com/quic-go/webtransport-go v0.9.0
	github.com/stretchr/testify v1.11.1
	go.uber.org/goleak v1.3.0
	go.uber.org/mock v0.5.0
	golang.org/x/sync v0.17.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/quic-go/qpack v0.5.1 // indirect
	golang.org/x/crypto v0.43.0 // indirect
	golang.org/x/mod v0.29.0 // indirect
	golang.org/x/net v0.46.0 // indirect
	golang.org/x/sys v0.37.0 // indirect
	golang.org/x/text v0.30.0 // indirect
	golang.org/x/tools v0.38.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/mengelbart/protogen v0.0.0 => ../protogen
