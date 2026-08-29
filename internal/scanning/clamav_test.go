package scanning

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClamAVScan(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  bool
	}{
		{name: "clean file", response: "stream: OK\n"},
		{name: "malware", response: "stream: Eicar-Test-Signature FOUND\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverErr := make(chan error, 1)
			go serveClamAVTestResponse(listener, tt.response, serverErr)

			scanner := ClamAV{Address: listener.Addr().String(), Timeout: time.Second}
			err = scanner.Scan(context.Background(), strings.NewReader("safe upload body"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Scan() error = %v, want error: %v", err, tt.wantErr)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveClamAVTestResponse(listener net.Listener, response string, result chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer conn.Close()
	command := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(conn, command); err != nil {
		result <- err
		return
	}
	for {
		var length uint32
		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
			result <- err
			return
		}
		if length == 0 {
			break
		}
		if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
			result <- err
			return
		}
	}
	_, err = io.WriteString(conn, response)
	result <- err
}
