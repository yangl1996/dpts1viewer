package main

import (
	"net"
	"fmt"
	"errors"
	"time"
	"strings"
	"context"
)

func getDPTS1Addr() (string, error) {
	// search for the local interface in 203.0.113.0/24 network
	ifaddrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	var laddr net.Addr
	for _, addr := range ifaddrs {
		if strings.HasPrefix(addr.String(), "203.0.113.") {
			if laddr != nil {
				return "", errors.New("multiple interfaces in 203.0.113.0/24 network")
			}
			laddr = addr
		}
	}
	if laddr == nil {
		return "", errors.New("cannot find DPT-S1 interface")
	}

	// multicast byte 1 to 203.0.113.0/24 port 54321
	multicastAddr, err := net.ResolveUDPAddr("udp", "203.0.113.255:54321")
	if err != nil {
		return "", err
	}
	pc, err := net.ListenPacket("udp", strings.Split(laddr.String(), "/")[0]+":")
	if err != nil {
		return "", err
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(time.Duration(3 * time.Second)))
	_, err = pc.WriteTo([]byte{1}, multicastAddr)
	if err != nil {
		return "", err
	}
	// wait for response byte 15
	rcvdPkt := []byte{0}
	var raddr net.Addr
	for rcvdPkt[0] != 15 {
		_, raddr, err = pc.ReadFrom(rcvdPkt)
		if err != nil {
			return "", err
		}
	}
	// return address
	return raddr.String(), nil
}

// getDPTS1AddrPolling searches for the device by trying to connect to
// every address in 203.0.113.0/24. The method is copied from
// https://github.com/tjwei/PyDPTS1.
func getDPTS1AddrPolling() (string, error) {
	dialer := &net.Dialer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connCh := make(chan net.Conn)
	for i := 0; i < 256; i++ {
		scan := func(i int) {
			remoteAddr := fmt.Sprintf("203.0.113.%d:54321", i)
			conn, err := dialer.DialContext(ctx, "tcp", remoteAddr)
			if err != nil {
				if conn != nil {
					conn.Close()
				}
			} else {
				connCh <- conn
			}
		}
		go scan(i)
	}

	timer := time.NewTimer(3 * time.Second)
	select {
	case <-timer.C:
		return "", errors.New("cannot establish connection to DPT-S1")
	case c := <-connCh:
		defer c.Close()
		return c.RemoteAddr().String(), nil
	}
}
