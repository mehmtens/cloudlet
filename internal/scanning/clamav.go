package scanning

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Scanner is intentionally small so ClamAV can later be replaced by another engine.
type Scanner interface {
	Scan(context.Context, io.Reader) error
}

type ClamAV struct {
	Address string
	Timeout time.Duration
}

func (c ClamAV) Scan(ctx context.Context, body io.Reader) error {
	if c.Address == "" {
		return fmt.Errorf("clamav address is required")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return fmt.Errorf("connect to clamav: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err = conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return err
	}
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			header := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
			if _, err = conn.Write(header); err == nil {
				_, err = conn.Write(buf[:n])
			}
			if err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if _, err = conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return err
	}
	response, err := bufio.NewReader(conn).ReadString('\x00')
	if err != nil && response == "" {
		return err
	}
	response = strings.TrimSpace(response)
	if strings.Contains(response, "FOUND") {
		return fmt.Errorf("malware detected: %s", response)
	}
	if !strings.Contains(response, "OK") {
		return fmt.Errorf("clamav scan failed: %s", response)
	}
	return nil
}
