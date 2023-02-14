package main

import (
	"net"
	"fmt"
	"os"
	"time"
	"sync/atomic"
	"image/draw"
	"image"
	"image/jpeg"
	"github.com/hajimehoshi/ebiten/v2"
	"bufio"
	"flag"
	"runtime/pprof"
	"math"
)

type device struct {
	addr string
	display *atomic.Pointer[image.YCbCr]
	landscape *atomic.Bool // portrait=1200x1600, landscape=1600x1200

	framebuffer *ebiten.Image
	rgbabuffer *image.RGBA
	lastDraw *image.YCbCr
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
	// It is silly that we have to start new TCP connections every time. But it
	// seems like the behavior of Sony's client as well, so there is little we
	// can do.
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
	d.display.Store(img.(*image.YCbCr))
	return nil
}

func (d *device) Draw(screen *ebiten.Image){
	img := d.display.Load()
	if img != nil {
		if d.framebuffer == nil {
			d.framebuffer = ebiten.NewImage(1200, 1600)
		}
		// We could have called image/draw.Draw to draw directly on the
		// framebuffer (ebiten.Image), but apparently that calls draw.Set on
		// individual pixels and is slow. draw.Draw has specialization for
		// image.RGBA, so we allocate an image.RGBA (reused) as the
		// intermediary.
		// Idea came from https://github.com/hajimehoshi/ebiten/blob/4c520581b89b05c1dd06baaa7c646f095f37980a/imagetobytes.go#L78
		if d.rgbabuffer == nil {
			d.rgbabuffer = &image.RGBA{
				Pix: make([]byte, 4*1200*1600),
				Stride: 4*1200,
				Rect: image.Rectangle{image.Point{0, 0}, image.Point{1200, 1600}},
			}
		}
		if d.lastDraw != img {
			draw.Draw(d.rgbabuffer, image.Rectangle{image.Point{0, 0}, image.Point{1200, 1600}}, img, image.Point{0, 0}, draw.Src)
			d.framebuffer.WritePixels(d.rgbabuffer.Pix)
			d.lastDraw = img
		}
		if d.landscape.Load() {
			screen.DrawImage(d.framebuffer, ROTATE)
		} else {
			screen.DrawImage(d.framebuffer, nil)
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
	interval := flag.Duration("i", time.Duration(1 * time.Second), "refreshing interval")
	usePolling := flag.Bool("poll", false, "use polling to discover DPT-S1 (avoids opening UDP listening socket)")
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

	// locate DPT-S1
	var err error
	var addr string
	if *usePolling {
		addr, err = getDPTS1AddrPolling()
	} else {
		addr, err = getDPTS1Addr()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("found DPT-S1 at", addr)

	d := &device{
		addr: addr,
		display: &atomic.Pointer[image.YCbCr]{},
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
		t := time.NewTicker(*interval)
		for {
			<-t.C
			d.Refresh()
		}
	}()
	if err := ebiten.RunGame(d); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

