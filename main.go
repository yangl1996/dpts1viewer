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
	"bufio"
	"flag"
	"runtime/pprof"
)

type device struct {
	addr string
	display image.Image
	lock *sync.Mutex
	x int
	y int

	buffer *bufio.Reader
	searchIndex int
}

var ENDTAG = []byte("</command>\n")

func (d *device) Update() error {
	// Ebiten calls Update and Draw non-concurrently. Since download image
	// from DPT-S1 is slow, we do not want that happen in the critical path.
	// Instead the screen refreshing logic happens in the Refresh function
	// which we call in a separate goroutine.
	return nil
}

// TODO: support landscape mode
// TODO: lock-free
func (d *device) Refresh() error {
	conn, err := net.Dial("tcp", d.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if d.buffer == nil {
		d.buffer = bufio.NewReaderSize(conn, 16 * 1024 * 1024)
	} else {
		d.buffer.Reset(conn)
	}
	// search for beginning of image
	d.searchIndex = 0
	for d.searchIndex < len(ENDTAG) {
		b, err := d.buffer.ReadByte()
		if err != nil {
			return err
		}
		if b == ENDTAG[d.searchIndex] {
			d.searchIndex += 1
		} else {
			d.searchIndex = 0
		}
	}
	img, err := jpeg.Decode(d.buffer)
	if err != nil {
		return err
	} else {
		d.lock.Lock()
		d.display = img
		b := d.display.Bounds()
		d.x = b.Max.X
		d.y = b.Max.Y
		d.lock.Unlock()
		ebiten.ScheduleFrame()
		return nil
	}
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
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `file`")
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not create CPU profile: ", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "could not start CPU profile: ", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}
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
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
	go func() {
		for ;; {
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
