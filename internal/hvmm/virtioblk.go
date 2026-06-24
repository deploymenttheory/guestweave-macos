//go:build darwin

package hvmm

import "encoding/binary"

// virtioBlk is a minimal virtio-mmio (legacy, version 1) block device backing a
// fixed disk image. It is enough for edk2's VirtioMmioDeviceLib + VirtioBlkDxe to
// enumerate the device and read/write 512-byte sectors. edk2's block driver
// submits a request then polls the used ring for completion, so we process each
// notified request synchronously and need no interrupt delivery.
//
// Layout reference: virtio 0.9.5 split virtqueue (descriptor table, available
// ring, then the used ring aligned to QueueAlign), with the device's buffers
// addressed by guest-physical address and DMA'd via the Machine's mapped RAM.
type virtioBlk struct {
	mem  *Machine
	disk []byte

	status        uint32
	deviceFeatSel uint32
	driverFeatSel uint32
	guestPageSize uint64
	queueSel      uint32
	queueNum      uint32
	queueAlign    uint64
	queuePFN      uint64
	lastAvail     uint16
	intStatus     uint32
}

// virtio-mmio (legacy) register offsets.
const (
	vmMagic      = 0x000 // "virt"
	vmVersion    = 0x004 // 1 = legacy
	vmDeviceID   = 0x008 // 2 = block
	vmVendorID   = 0x00c
	vmDeviceFeat = 0x010
	vmDeviceFSel = 0x014
	vmDriverFeat = 0x020
	vmDriverFSel = 0x024
	vmGuestPgSz  = 0x028
	vmQueueSel   = 0x030
	vmQueueNumMx = 0x034
	vmQueueNum   = 0x038
	vmQueueAlign = 0x03c
	vmQueuePFN   = 0x040
	vmQueueReady = 0x044
	vmQueueNotfy = 0x050
	vmIntStatus  = 0x060
	vmIntACK     = 0x064
	vmStatus     = 0x070
	vmConfig     = 0x100 // device config: virtio-blk capacity (sectors) at +0
)

const (
	virtioBlkQueueMax = 256
	virtqDescNext     = 1 // VIRTQ_DESC_F_NEXT
	virtqDescWrite    = 2 // VIRTQ_DESC_F_WRITE (device writes the buffer)
	virtioBlkTypeOut  = 1 // VIRTIO_BLK_T_OUT (guest→disk write); 0 = IN (read)
)

func newVirtioBlk(m *Machine, disk []byte) *virtioBlk {
	return &virtioBlk{mem: m, disk: disk, guestPageSize: 4096, queueAlign: 4096}
}

func (v *virtioBlk) read(off uint64, bytes int) uint64 {
	switch off {
	case vmMagic:
		return 0x74726976 // "virt"
	case vmVersion:
		return 1
	case vmDeviceID:
		return 2 // block device
	case vmVendorID:
		return 0x554d4551 // "QEMU"
	case vmDeviceFeat:
		return 0 // legacy, no optional features advertised
	case vmQueueNumMx:
		return virtioBlkQueueMax
	case vmQueuePFN:
		return v.queuePFN
	case vmIntStatus:
		return uint64(v.intStatus)
	case vmStatus:
		return uint64(v.status)
	case vmConfig: // virtio-blk capacity in 512-byte sectors
		return readLE(v.capacityBytes(), bytes)
	}
	if off > vmConfig && off < vmConfig+0x40 {
		return readLE(v.capacityBytes()[off-vmConfig:], bytes)
	}
	return 0
}

func (v *virtioBlk) write(off uint64, _ int, val uint64) {
	switch off {
	case vmDeviceFSel:
		v.deviceFeatSel = uint32(val)
	case vmDriverFSel:
		v.driverFeatSel = uint32(val)
	case vmGuestPgSz:
		v.guestPageSize = val
	case vmQueueSel:
		v.queueSel = uint32(val)
	case vmQueueNum:
		v.queueNum = uint32(val)
	case vmQueueAlign:
		v.queueAlign = val
	case vmQueuePFN:
		v.queuePFN = val
		v.lastAvail = 0
	case vmQueueNotfy:
		v.processQueue()
	case vmIntACK:
		v.intStatus &^= uint32(val)
	case vmStatus:
		v.status = uint32(val)
		if val == 0 { // reset
			v.queuePFN, v.lastAvail, v.intStatus = 0, 0, 0
		}
	}
}

func (v *virtioBlk) capacityBytes() []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(len(v.disk))/512)
	return b[:]
}

// processQueue services every newly-available request descriptor chain: read the
// 16-byte virtio-blk header, perform the read/write against the disk image, write
// the status byte, and publish the chain head on the used ring.
func (v *virtioBlk) processQueue() {
	if v.queuePFN == 0 || v.queueNum == 0 {
		return
	}
	descBase := v.queuePFN * v.guestPageSize
	availBase := descBase + 16*uint64(v.queueNum)
	usedBase := align(availBase+6+2*uint64(v.queueNum), v.queueAlign)

	availIdx := v.readU16(availBase + 2)
	for v.lastAvail != availIdx {
		head := v.readU16(availBase + 4 + 2*uint64(v.lastAvail%uint16(v.queueNum)))
		written := v.serviceChain(descBase, head)
		v.publishUsed(usedBase, uint32(head), written)
		v.lastAvail++
	}
	v.intStatus |= 1 // used-buffer notification
}

// serviceChain walks one descriptor chain (header, data buffers, status) and
// returns the number of bytes written back into guest buffers.
func (v *virtioBlk) serviceChain(descBase uint64, head uint16) uint32 {
	// Gather the chain.
	type desc struct {
		addr  uint64
		len   uint32
		write bool
	}
	var chain []desc
	for idx, n := head, 0; n < int(v.queueNum); n++ {
		d := descBase + 16*uint64(idx)
		addr := v.readU64(d)
		dlen := uint32(v.readU32(d + 8))
		flags := v.readU16(d + 12)
		next := v.readU16(d + 14)
		chain = append(chain, desc{addr, dlen, flags&virtqDescWrite != 0})
		if flags&virtqDescNext == 0 {
			break
		}
		idx = next
	}
	if len(chain) < 2 {
		return 0
	}

	// chain[0] = 16-byte request header (type, _, sector).
	hdr := v.mem.ReadGuest(chain[0].addr, 16)
	if hdr == nil {
		return 0
	}
	reqType := binary.LittleEndian.Uint32(hdr[0:4])
	sector := binary.LittleEndian.Uint64(hdr[8:16])
	off := sector * 512

	var written uint32
	statusIdx := len(chain) - 1 // last descriptor is the 1-byte status
	for _, d := range chain[1:statusIdx] {
		n := uint64(d.len)
		if reqType == virtioBlkTypeOut { // guest → disk
			if buf := v.mem.ReadGuest(d.addr, int(n)); buf != nil {
				copy(v.diskAt(off, n), buf)
			}
		} else { // disk → guest (read)
			v.mem.WriteGuest(d.addr, v.diskAt(off, n))
			written += d.len
		}
		off += n
	}
	// Status byte: 0 = VIRTIO_BLK_S_OK.
	v.mem.WriteGuest(chain[statusIdx].addr, []byte{0})
	return written + 1
}

// diskAt returns a zero-padded n-byte view of the disk at byte offset off.
func (v *virtioBlk) diskAt(off, n uint64) []byte {
	out := make([]byte, n)
	if off < uint64(len(v.disk)) {
		copy(out, v.disk[off:min(off+n, uint64(len(v.disk)))])
	}
	return out
}

func (v *virtioBlk) publishUsed(usedBase uint64, id, length uint32) {
	idx := v.readU16(usedBase + 2)
	slot := usedBase + 4 + 8*uint64(idx%uint16(v.queueNum))
	v.writeU32(slot, id)
	v.writeU32(slot+4, length)
	v.writeU16(usedBase+2, idx+1)
}

// guest-RAM little-endian accessors.
func (v *virtioBlk) readU16(gpa uint64) uint16 {
	if b := v.mem.ReadGuest(gpa, 2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}
func (v *virtioBlk) readU32(gpa uint64) uint64 {
	if b := v.mem.ReadGuest(gpa, 4); b != nil {
		return uint64(binary.LittleEndian.Uint32(b))
	}
	return 0
}
func (v *virtioBlk) readU64(gpa uint64) uint64 {
	if b := v.mem.ReadGuest(gpa, 8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}
func (v *virtioBlk) writeU16(gpa uint64, val uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], val)
	v.mem.WriteGuest(gpa, b[:])
}
func (v *virtioBlk) writeU32(gpa uint64, val uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], val)
	v.mem.WriteGuest(gpa, b[:])
}

func align(n, a uint64) uint64 {
	if a == 0 {
		return n
	}
	return (n + a - 1) &^ (a - 1)
}

func readLE(b []byte, bytes int) uint64 {
	var v uint64
	for i := 0; i < bytes && i < len(b); i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}
