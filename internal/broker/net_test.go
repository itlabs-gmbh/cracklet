package broker

import "net"

type netConn = net.Conn

func dialUnix(path string) (net.Conn, error) { return net.Dial("unix", path) }
