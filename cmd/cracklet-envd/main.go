// Command cracklet-envd runs inside every cracklet microVM. It waits on vsock for the
// identity message the host agent sends after a snapshot restore and applies
// it (IP, route, hostname, clock, entropy).
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/envd"
)

const hostnameFile = "/etc/hostname"

func main() {
	log.SetFlags(0)
	log.SetPrefix("cracklet-envd: ")
	ln, err := listen(envd.Port)
	if err != nil {
		log.Fatalf("listen on vsock port %d: %v", envd.Port, err)
	}
	log.Printf("listening on vsock port %d", envd.Port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		serve(conn)
	}
}

// serve handles one identity delivery: a single JSON line, answered with OK or ERR.
func serve(conn io.ReadWriteCloser) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		log.Printf("read: %v", err)
		return
	}
	id, err := envd.Parse(line)
	started := time.Now()
	if err == nil {
		err = envd.Apply(id, linuxSystem{}, hostnameFile)
	}
	if err != nil {
		log.Printf("apply identity: %v", err)
		fmt.Fprintf(conn, "ERR %v\n", err)
		return
	}
	log.Printf("identity applied: %s as %s in %s", id.IP, id.Hostname, time.Since(started).Round(time.Millisecond))
	fmt.Fprint(conn, "OK\n")
}
