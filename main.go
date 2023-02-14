package main

import (
	"net"
	"fmt"
	"errors"
	"os"
	"time"
	"context"
	"sync/atomic"
	"image/jpeg"
	"github.com/hajimehoshi/ebiten/v2"
	"bufio"
	"flag"
	"runtime/pprof"
	"math"
)

type device struct {
	addr string
	display *atomic.Pointer[ebiten.Image]
	landscape *atomic.Bool // portrait=1200x1600, landscape=1600x1200

	lastDraw *ebiten.Image
	buffer *bufio.Reader
}

var ENDTAG = []byte("</command>\n")
var PORTRAIT = []byte("portrait")
var ROTATE = &ebiten.DrawImageOptions{}

func init() {
	ROTATE.GeoM.Translate(-1200/2, -1600/2)	// move centerpoint to origin
	ROTATE.GeoM.Rotate(270 * 2 * math.Pi / 360)	// rotate at origin
	ROTATE.GeoM.Translate(1600/2, 1200/2) // move back
}

func (d *device) Update() error {
	// Update seems to block Draw. Since downloading image from DPT-S1 is slow,
	// we do not want that happen in the critical path. Instead the screen
	// refreshing logic happens in the Refresh function which we call in a
	// separate goroutine.
	return nil
}

func (d *device) Refresh() error {
	conn, err := net.Dial("tcp", d.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if d.buffer == nil {
		d.buffer = bufio.NewReader(conn)
	} else {
		d.buffer.Reset(conn)
	}
	// search for beginning of image and mode
	demidx := 0		// idx of ENDTAG we are matching next
	poridx := 0		// idx of PORTRAIT we are matching next
	for demidx < len(ENDTAG) {
		b, err := d.buffer.ReadByte()
		if err != nil {
			return err
		}
		if b == ENDTAG[demidx] {
			demidx += 1
		} else {
			demidx = 0
		}
		if poridx < len(PORTRAIT) {
			if b == PORTRAIT[poridx] {
				poridx += 1
			} else {
				poridx = 0
			}
		}
	}
	img, err := jpeg.Decode(d.buffer)
	if err != nil {
		return err
	}

	if poridx != len(PORTRAIT) {
		// landscape mode; we need to rotate the image
		// resize window if not already in landscape mode
		if d.landscape.CompareAndSwap(false, true) {
			ebiten.SetWindowSize(800, 600)
		}
	} else {
		// portrait mode, resize window if needed
		if d.landscape.CompareAndSwap(true, false) {
			ebiten.SetWindowSize(600, 800)
		}
	}
	newImg := ebiten.NewImageFromImage(img)
	d.display.Store(newImg)
	return nil
}

func (d *device) Draw(screen *ebiten.Image){
	img := d.display.Load()
	if img != nil {
		if d.landscape.Load() {
			screen.DrawImage(img, ROTATE)
		} else {
			screen.DrawImage(img, nil)
		}
	}
	return
}

func (d *device) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	if d.landscape.Load() {
		return 1600, 1200
	} else {
		return 1200, 1600
	}
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
	// TODO: confirm if DPT API really requires repeatedly establishing connections. For example, is there any other port that we can talk to which gives us a persistent connection?
	addr, err := getDPTS1Addr()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("found DPT-S1 at", addr)
	d := &device{
		addr: addr,
		display: &atomic.Pointer[ebiten.Image]{},
		landscape: &atomic.Bool{},
	}
	d.Refresh()	// make sure we have the image before start the UI
	if d.landscape.Load() {
		ebiten.SetWindowSize(800, 600)
	} else {
		ebiten.SetWindowSize(600, 800)
	}
	ebiten.SetWindowTitle("DPT-S1 Display")
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
