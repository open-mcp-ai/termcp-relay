package sshserver

import (
	"io"
	"net"
	"sync"
)

// echoListener returns a listener whose accepted connections echo bytes back.
func echoListener(tT testingT) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					_, _ = c.Write(buf[:n])
				}
			}(c)
		}
	}()
	return ln, nil
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { _, _ = io.Copy(a, b); wg.Done() }()
	go func() { _, _ = io.Copy(b, a); wg.Done() }()
	wg.Wait()
}

type testingT interface {
	Helper()
	Cleanup(func())
}
