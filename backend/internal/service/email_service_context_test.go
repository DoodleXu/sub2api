//go:build unit

package service

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSMTPConnectionClosesSocketWhenContextIsCanceled(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _, _ = serverConn.Write([]byte("220 smtp.example.test ESMTP\r\n")) }()
	client, err := newSMTPClient(ctx, clientConn, "smtp.example.test")
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	cancel()
	require.NoError(t, serverConn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = serverConn.Read(make([]byte, 1))
	require.Error(t, err, "canceling the context must close an in-flight SMTP connection")
}
