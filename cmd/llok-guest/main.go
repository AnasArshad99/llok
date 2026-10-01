//go:build linux

// llok-guest runs inside the VM. "proxy" exposes the host broker on
// 127.0.0.1:3128 by forwarding each TCP connection over vsock (the guest has
// no network device; this port is its only way out). "control" keeps a
// connection to the host for resize and shutdown commands.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/mdlayher/vsock"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: llok-guest proxy|control|exit [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "control":
		controlLoop(os.Args[2:])
		return
	case "exit":
		sendExit(os.Args[2:])
		return
	}
	if os.Args[1] != "proxy" {
		fmt.Fprintln(os.Stderr, "usage: llok-guest proxy|control|exit [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:3128", "local address to listen on")
	port := fs.Uint("port", 3128, "host vsock port to forward to")
	fs.Parse(os.Args[2:])

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llok-guest:", err)
		os.Exit(1)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go forward(c, uint32(*port))
	}
}

func forward(c net.Conn, port uint32) {
	defer c.Close()
	h, err := vsock.Dial(vsock.Host, port, nil)
	if err != nil {
		return
	}
	defer h.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(h, c); h.Close() }()
	go func() { defer wg.Done(); io.Copy(c, h); c.Close() }()
	wg.Wait()
}
