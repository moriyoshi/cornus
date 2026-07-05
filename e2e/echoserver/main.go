// Command echoserver is a tiny static TCP echo used by the multi-replica hub E2E
// scripts as the delivery target (the e2e image ships no socat/python3).
package main

import (
	"io"
	"net"
	"os"
)

func main() {
	ln, err := net.Listen("tcp", os.Args[1])
	if err != nil {
		panic(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) { io.Copy(c, c); c.Close() }(c)
	}
}
