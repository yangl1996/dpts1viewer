package main

import (
	"net"
	"fmt"
	"errors"
	"os"
	"time"
	"context"
	"sync"
	"image"
	"image/jpeg"
	"github.com/hajimehoshi/ebiten/v2"
	"bytes"
	"io"
)

type device struct {
	addr string
	display image.Image
	lock *sync.Mutex
	x int
	y int
}

var ENDTAG = []byte("</command>\n")

func (d *device) Update() error {
	// Ebiten calls Update and Draw non-concurrently. Since download image
	// from DPT-S1 is slow, we do not want that happen in the critical path.
	// Instead the screen refreshing logic happens in the Refresh function
	// which we call in a separate goroutine.
	return nil
}

// TODO: lock-free
func (d *device) Refresh() error {
	conn, err := net.Dial("tcp", d.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	buffer, err := io.ReadAll(conn)
	first := bytes.Index(buffer, ENDTAG)
	if first == -1 {
		return errors.New("cannot find start of image")
	}
	first += len(ENDTAG)
	r := bytes.NewReader(buffer[first:])
	d.lock.Lock()
	defer d.lock.Unlock()
	d.display, err = jpeg.Decode(r)
	b := d.display.Bounds()
	d.x = b.Max.X
	d.y = b.Max.Y
	return err
}

func (d *device) Draw(screen *ebiten.Image){
	d.lock.Lock()
	img := ebiten.NewImageFromImage(d.display)
	d.lock.Unlock()
	if d.display != nil {
		screen.Clear()
		screen.DrawImage(img, nil)
	}
	return
}

func (d *device) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.x, d.y 
}

func main() {
	// TODO: confirm if DPT API really requires repeatedly establishing connections
	addr, err := getDPTS1Addr()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("found DPT-S1 at", addr)
	d := &device{
		addr: addr,
		lock: &sync.Mutex{}}
	d.Refresh()	// make sure we have the image before start the UI
	ebiten.SetWindowSize(d.x / 2, d.y / 2)
	ebiten.SetWindowTitle("DPT-S1 Screen Sharing")
	go func() {
		for ;;{
			d.Refresh()
		}
	}()
	if err := ebiten.RunGame(d); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func getDPTS1Addr() (string, error) {
	// TODO: handle conflict/multiple devices
	// TODO: find the device without polling?
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
