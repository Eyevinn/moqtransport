package integrationtests

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
)

func connect(t *testing.T) (server, client *quic.Conn, cancel func()) {
	tlsConfig, err := generateTLSConfig()
	assert.NoError(t, err)
	listener, err := quic.ListenAddr("localhost:0", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	assert.NoError(t, err)

	clientConn, err := quic.DialAddr(context.Background(), fmt.Sprintf("localhost:%d", listener.Addr().(*net.UDPAddr).Port), &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}, &quic.Config{
		EnableDatagrams: true,
	})
	assert.NoError(t, err)

	serverConn, err := listener.Accept(context.Background())
	assert.NoError(t, err)

	return serverConn, clientConn, func() {
		_ = listener.Close()
		assert.NoError(t, clientConn.CloseWithError(0, ""))
		assert.NoError(t, serverConn.CloseWithError(0, ""))
	}
}

func setup(t *testing.T, sConn, cConn *quic.Conn, handler moqtransport.Handler) (
	serverSession *moqtransport.Session,
	clientSession *moqtransport.Session,
	cancel func(),
) {
	return setupWithHandlers(t, sConn, cConn, handler, nil)
}

type sessionOptions struct {
	handler             moqtransport.Handler
	subscribeHandler    moqtransport.SubscribeHandler
	fetchHandler        moqtransport.FetchHandler
}

func setupWithHandlers(t *testing.T, sConn, cConn *quic.Conn, handler moqtransport.Handler, subscribeHandler moqtransport.SubscribeHandler) (
	serverSession *moqtransport.Session,
	clientSession *moqtransport.Session,
	cancel func(),
) {
	return setupWithAllHandlers(t, sConn, cConn, sessionOptions{
		handler:          handler,
		subscribeHandler: subscribeHandler,
	})
}

func setupWithAllHandlers(t *testing.T, sConn, cConn *quic.Conn, opts sessionOptions) (
	serverSession *moqtransport.Session,
	clientSession *moqtransport.Session,
	cancel func(),
) {
	serverSession = &moqtransport.Session{
		Handler:             opts.handler,
		SubscribeHandler:    opts.subscribeHandler,
		FetchHandler:        opts.fetchHandler,
		InitialMaxRequestID: 100,
		Qlogger:             nil,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := serverSession.Run(quicmoq.NewServer(sConn))
		assert.NoError(t, err)
	}()

	clientSession = &moqtransport.Session{
		Handler:             opts.handler,
		SubscribeHandler:    opts.subscribeHandler,
		FetchHandler:        opts.fetchHandler,
		InitialMaxRequestID: 100,
		Qlogger:             nil,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := clientSession.Run(quicmoq.NewClient(cConn))
		assert.NoError(t, err)
	}()

	cancel = func() {
		_ = serverSession.Close()
		_ = clientSession.Close()
	}
	wg.Wait()
	return
}

// Setup a bare-bones TLS config for the server
func generateTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"moq-00", "h3"},
	}, nil
}
