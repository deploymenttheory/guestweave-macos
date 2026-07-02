//go:build darwin

package hvmm

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"sync"
	"time"

	weaveconfig "github.com/deploymenttheory/guestweave/internal/config"
)

// ramfb is a QEMU "ramfb" display: a simple linear framebuffer whose location,
// format, and geometry the guest sets by writing a 28-byte config to the
// etc/ramfb fw_cfg file. edk2's QemuRamfbDxe turns it into a UEFI GOP, and
// Windows ARM64's inbox BasicDisplay driver renders into it — no GPU model. weave
// snapshots the framebuffer to a PNG (GUESTWEAVE_HVMM_FRAMEBUFFER, default /tmp/weave-fb.png)
// for headless verification; a live AppKit window can come later.
type ramfb struct {
	mem *Machine
	out io.Writer

	mu     sync.Mutex
	addr   uint64
	width  int
	height int
	stride int
	active bool
}

func newRamfb(m *Machine, out io.Writer) *ramfb {
	r := &ramfb{mem: m, out: out}
	go r.renderLoop()
	return r
}

// setConfig parses the big-endian RAMFBCfg the guest wrote to etc/ramfb:
// { u64 addr, u32 fourcc, u32 flags, u32 width, u32 height, u32 stride }.
func (r *ramfb) setConfig(b []byte) {
	if len(b) < 28 {
		return
	}
	r.mu.Lock()
	r.addr = binary.BigEndian.Uint64(b[0:])
	r.width = int(binary.BigEndian.Uint32(b[16:]))
	r.height = int(binary.BigEndian.Uint32(b[20:]))
	r.stride = int(binary.BigEndian.Uint32(b[24:]))
	r.active = r.addr != 0 && r.width > 0 && r.height > 0
	w, h, a := r.width, r.height, r.addr
	r.mu.Unlock()
	if r.active && r.out != nil {
		fmt.Fprintf(r.out, "[ramfb] framebuffer %dx%d @ 0x%x\n", w, h, a)
	}
}

func (r *ramfb) renderLoop() {
	path := weaveconfig.HVMMFramebuffer()
	if path == "" {
		path = "/tmp/weave-fb.png"
	}
	for range time.Tick(time.Second) {
		r.snapshot(path)
	}
}

// snapshot reads the live framebuffer from guest RAM (a benign torn-frame race
// with the guest is acceptable for verification) and writes it as a PNG. The
// guest format is XRGB8888 (little-endian B,G,R,X per pixel).
func (r *ramfb) snapshot(path string) {
	r.mu.Lock()
	addr, w, h, stride, active := r.addr, r.width, r.height, r.stride, r.active
	r.mu.Unlock()
	if !active {
		return
	}
	fb := r.mem.ReadGuest(addr, stride*h)
	if fb == nil {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*stride + x*4
			if i+3 >= len(fb) {
				continue
			}
			o := img.PixOffset(x, y)
			img.Pix[o+0] = fb[i+2] // R
			img.Pix[o+1] = fb[i+1] // G
			img.Pix[o+2] = fb[i+0] // B
			img.Pix[o+3] = 0xff    // A
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	_ = png.Encode(f, img)
	_ = f.Close()
}
