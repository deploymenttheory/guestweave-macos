//go:build darwin

package hvmm

import (
	"encoding/binary"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// nvmeController is a minimal NVMe controller: a PCIe function (config space +
// one 64-bit memory BAR) exposing the NVMe register block, an admin queue, and
// one or more I/O queues, all backed by a disk image. It is enough for edk2's
// NvmExpressDxe — and Windows ARM64's inbox stornvme — to enumerate the single
// namespace and read/write 512-byte logical blocks.
//
// The host posts 64-byte commands to a submission queue in guest RAM and rings a
// doorbell; we service them synchronously and post 16-byte completions to the
// completion queue. edk2 polls the completion phase tag, so no interrupt is
// delivered (MSI-X, which Windows needs, is a later addition).
type nvmeController struct {
	mem  *Machine
	disk []byte

	// PCIe config space (type 0 header). BAR0/1 form a 64-bit memory BAR.
	cmd     uint16 // command register (bit1 = memory space enable)
	barLow  uint32
	barHigh uint32

	// Controller registers.
	cc   uint32 // controller configuration
	csts uint32 // controller status
	aqa  uint32 // admin queue attributes
	asq  uint64 // admin submission queue base
	acq  uint64 // admin completion queue base

	sq [maxNvmeQueues]nvmeSQ
	cq [maxNvmeQueues]nvmeCQ

	// MSI-X: interrupt-driven completions (Windows' NVMe driver requires this;
	// edk2 polls and never enables it). The table lives in BAR0 at nvmeMSIXTable;
	// on a completion we deliver the CQ's vector via Apple's GIC (HvGicSendMsi).
	msixEnabled bool
	msixTable   [nvmeMSIXVectors]msixEntry
}

type msixEntry struct {
	addr uint64 // message address
	data uint32 // message data (Apple's GIC takes this as the interrupt id)
	ctrl uint32 // vector control (bit0 = masked)
}

type nvmeSQ struct {
	base uint64
	size uint32 // entries
	head uint32
	cqid uint16
}

type nvmeCQ struct {
	base   uint64
	size   uint32 // entries
	tail   uint32
	phase  uint8  // expected phase tag (toggles each wrap)
	vector uint16 // MSI-X vector (from Create I/O CQ); admin CQ uses 0
}

const (
	maxNvmeQueues = 8
	nvmeBARSize   = 0x4000 // 16 KiB register region
	nvmeSQES      = 64
	nvmeCQES      = 16

	// MSI-X table and pending-bit array, in BAR0 above the doorbells.
	nvmeMSIXVectors = 8
	nvmeMSIXTable   = 0x3000
	nvmeMSIXPBA     = 0x3800

	// PCI config offsets.
	pciCommand  = 0x04
	pciBAR0     = 0x10
	pciBAR1     = 0x14
	pciCapPtr   = 0x40   // our capabilities list starts here (MSI-X)
	msixCapCtrl = 0x42   // MSI-X message control (within the capability)

	// NVMe register offsets (BAR-relative).
	nvmeCAP    = 0x00 // capabilities (8 bytes)
	nvmeVS     = 0x08 // version
	nvmeCC     = 0x14 // controller configuration
	nvmeCSTS   = 0x1c // controller status
	nvmeAQA    = 0x24 // admin queue attributes
	nvmeASQ    = 0x28 // admin SQ base (8 bytes)
	nvmeACQ    = 0x30 // admin CQ base (8 bytes)
	nvmeDBBase = 0x1000
)

func newNvmeController(m *Machine, disk []byte) *nvmeController {
	return &nvmeController{mem: m, disk: disk}
}

// barBase returns the assigned BAR0 address (the NVMe register block).
func (n *nvmeController) barBase() uint64 {
	return (uint64(n.barHigh) << 32) | uint64(n.barLow&^uint32(nvmeBARSize-1))
}

// inBAR reports whether a guest-physical address falls in the enabled register
// block (memory decode on, BAR assigned to a real MMIO address).
func (n *nvmeController) inBAR(addr uint64) bool {
	if n.cmd&0x2 == 0 { // memory space disabled
		return false
	}
	b := n.barBase()
	return b >= 0x1000_0000 && addr >= b && addr < b+nvmeBARSize
}

// --- PCIe config space (bus0/dev0/fn0) ---

// nvmeStaticConfig is the fixed part of the PCI type-0 header; the command
// register and BARs are overlaid dynamically by configByte.
var nvmeStaticConfig = func() [80]byte {
	var c [80]byte
	binary.LittleEndian.PutUint16(c[0x00:], 0x1b36) // VID: Red Hat
	binary.LittleEndian.PutUint16(c[0x02:], 0x0010) // DID: NVMe
	c[0x06] = 0x10                                  // status: capabilities list present
	c[0x08] = 0x01                                  // revision
	c[0x09] = 0x02                                  // prog IF: NVM Express
	c[0x0a] = 0x08                                  // subclass: NVM
	c[0x0b] = 0x01                                  // class: mass storage
	binary.LittleEndian.PutUint16(c[0x2c:], 0x1b36) // subsystem VID
	binary.LittleEndian.PutUint16(c[0x2e:], 0x0010) // subsystem DID
	c[0x34] = pciCapPtr                             // capabilities pointer -> MSI-X
	c[0x3d] = 0x01                                  // interrupt pin A
	// MSI-X capability at 0x40: table & PBA both in BAR0 (BIR 0).
	c[pciCapPtr] = 0x11 // cap ID: MSI-X
	c[pciCapPtr+1] = 0  // next capability: end of list
	binary.LittleEndian.PutUint16(c[msixCapCtrl:], uint16(nvmeMSIXVectors-1)) // table size N-1
	binary.LittleEndian.PutUint32(c[pciCapPtr+4:], uint32(nvmeMSIXTable))
	binary.LittleEndian.PutUint32(c[pciCapPtr+8:], uint32(nvmeMSIXPBA))
	return c
}()

// configByte returns one byte of the PCI config space. It must serve ANY offset
// and width — edk2's NvmExpressDxe reads the class code as 3 bytes at offset 0x09,
// so a DWORD-only model would report class 0 and the driver would never bind.
func (n *nvmeController) configByte(i int) byte {
	switch {
	case i >= pciCommand && i < pciCommand+2: // command register
		return byte(n.cmd >> (8 * (i - pciCommand)))
	case i >= pciBAR0 && i < pciBAR0+4: // BAR0 low: 64-bit memory BAR
		v := (n.barLow &^ uint32(nvmeBARSize-1)) | 0x4
		return byte(v >> (8 * (i - pciBAR0)))
	case i >= pciBAR1 && i < pciBAR1+4: // BAR0 high
		return byte(n.barHigh >> (8 * (i - pciBAR1)))
	case i >= msixCapCtrl && i < msixCapCtrl+2: // MSI-X message control (enable bit is dynamic)
		v := uint16(nvmeMSIXVectors - 1)
		if n.msixEnabled {
			v |= 0x8000
		}
		return byte(v >> (8 * (i - msixCapCtrl)))
	case i >= 0 && i < len(nvmeStaticConfig):
		return nvmeStaticConfig[i]
	}
	return 0
}

func (n *nvmeController) configRead(off uint64, bytes int) uint64 {
	var v uint64
	for i := 0; i < bytes; i++ {
		v |= uint64(n.configByte(int(off)+i)) << (8 * i)
	}
	return v
}

func (n *nvmeController) configWrite(off uint64, bytes int, val uint64) {
	switch off {
	case pciCommand:
		n.cmd = uint16(val)
	case pciBAR0:
		n.barLow = uint32(val)
	case pciBAR1:
		n.barHigh = uint32(val)
	case msixCapCtrl: // MSI-X message control (16-bit): bit 15 = enable
		n.msixEnabled = val&0x8000 != 0
	case pciCapPtr: // 32-bit write covering the cap header; control is the high half
		n.msixEnabled = val&0x8000_0000 != 0
	}
}

// --- NVMe register block ---

func (n *nvmeController) regRead(off uint64, bytes int) uint64 {
	switch {
	case off == nvmeCAP:
		// MQES=0x3f (64 entries, 0-based), CQR=1, TO=0x0f, CSS bit37 (NVM), MPS 0.
		cap := uint64(0x3f) | (1 << 16) | (0x0f << 24) | (uint64(1) << 37)
		return cap & mask(bytes)
	case off == nvmeCAP+4:
		cap := uint64(0x3f) | (1 << 16) | (0x0f << 24) | (uint64(1) << 37)
		return (cap >> 32) & mask(bytes)
	case off == nvmeVS:
		return 0x0001_0400 // NVMe 1.4
	case off == nvmeCC:
		return uint64(n.cc) & mask(bytes)
	case off == nvmeCSTS:
		return uint64(n.csts) & mask(bytes)
	case off == nvmeAQA:
		return uint64(n.aqa) & mask(bytes)
	case off >= nvmeMSIXTable && off < nvmeMSIXTable+nvmeMSIXVectors*16:
		return uint64(n.msixTableRead(int(off - nvmeMSIXTable)))
	case off >= nvmeMSIXPBA && off < nvmeMSIXPBA+8:
		return 0 // no pending bits: completions are delivered synchronously
	}
	return 0
}

func (n *nvmeController) regWrite(off uint64, bytes int, val uint64) {
	switch off {
	case nvmeCC:
		prevEN := n.cc & 1
		n.cc = uint32(val)
		switch {
		case n.cc&1 == 1 && prevEN == 0: // enable: admin queue goes live, ready
			n.sq[0] = nvmeSQ{base: n.asq, size: (n.aqa & 0xfff) + 1, cqid: 0}
			n.cq[0] = nvmeCQ{base: n.acq, size: ((n.aqa >> 16) & 0xfff) + 1, phase: 1}
			n.csts |= 1 // RDY
		case n.cc&1 == 0 && prevEN == 1: // reset
			n.csts &^= 1
			n.sq, n.cq = [maxNvmeQueues]nvmeSQ{}, [maxNvmeQueues]nvmeCQ{}
		}
	case nvmeAQA:
		n.aqa = uint32(val)
	case nvmeASQ:
		n.asq = (n.asq &^ 0xffff_ffff) | (val & 0xffff_ffff)
	case nvmeASQ + 4:
		n.asq = (n.asq & 0xffff_ffff) | (val << 32)
	case nvmeACQ:
		n.acq = (n.acq &^ 0xffff_ffff) | (val & 0xffff_ffff)
	case nvmeACQ + 4:
		n.acq = (n.acq & 0xffff_ffff) | (val << 32)
	default:
		switch {
		case off >= nvmeMSIXTable && off < nvmeMSIXTable+nvmeMSIXVectors*16:
			n.msixTableWrite(int(off-nvmeMSIXTable), uint32(val))
		case off >= nvmeDBBase && off < nvmeMSIXTable:
			n.doorbell(int(off-nvmeDBBase)/4, uint32(val))
		}
	}
}

// MSI-X table: each 16-byte entry is { addr_lo, addr_hi, data, vector_control }.
func (n *nvmeController) msixTableRead(off int) uint32 {
	e := &n.msixTable[off/16]
	switch off % 16 {
	case 0:
		return uint32(e.addr)
	case 4:
		return uint32(e.addr >> 32)
	case 8:
		return e.data
	case 12:
		return e.ctrl
	}
	return 0
}

func (n *nvmeController) msixTableWrite(off int, val uint32) {
	e := &n.msixTable[off/16]
	switch off % 16 {
	case 0:
		e.addr = (e.addr &^ 0xffff_ffff) | uint64(val)
	case 4:
		e.addr = (e.addr & 0xffff_ffff) | (uint64(val) << 32)
	case 8:
		e.data = val
	case 12:
		e.ctrl = val
	}
}

// doorbell index 2q is queue q's SQ tail; 2q+1 is queue q's CQ head.
func (n *nvmeController) doorbell(idx int, val uint32) {
	q := idx / 2
	if q >= maxNvmeQueues {
		return
	}
	if idx%2 == 0 { // SQ tail updated — service new commands
		n.runSQ(q, val)
	}
	// CQ head doorbell (idx odd) just acknowledges completions; nothing to do.
}

// runSQ processes submission-queue entries for queue q from the current head to
// the new tail, posting a completion for each.
func (n *nvmeController) runSQ(q int, tail uint32) {
	sq := &n.sq[q]
	if sq.size == 0 {
		return
	}
	for sq.head != tail {
		cmd := n.mem.ReadGuest(sq.base+uint64(sq.head)*nvmeSQES, nvmeSQES)
		if cmd == nil {
			return
		}
		res, cqid := n.execute(q, cmd)
		n.complete(cqid, q, sq.head, le16(cmd[2:]), res)
		sq.head = (sq.head + 1) % sq.size
	}
}

// execute runs one command and returns its DW0 result and the target CQ id.
func (n *nvmeController) execute(q int, cmd []byte) (uint32, uint16) {
	op := cmd[0]
	prp1 := le64(cmd[24:])
	prp2 := le64(cmd[32:])
	cdw10 := le32(cmd[40:])
	cdw11 := le32(cmd[44:])
	cdw12 := le32(cmd[48:])

	if q == 0 { // admin commands
		switch op {
		case 0x06: // Identify
			n.identify(cdw10&0xff, prp1)
		case 0x05: // Create I/O Completion Queue (CDW11 high half = MSI-X vector)
			qid := cdw10 & 0xffff
			if qid < maxNvmeQueues {
				n.cq[qid] = nvmeCQ{base: prp1, size: ((cdw10 >> 16) & 0xffff) + 1, phase: 1, vector: uint16(cdw11 >> 16)}
			}
		case 0x01: // Create I/O Submission Queue
			qid := cdw10 & 0xffff
			if qid < maxNvmeQueues {
				n.sq[qid] = nvmeSQ{base: prp1, size: ((cdw10 >> 16) & 0xffff) + 1, cqid: uint16(cdw11 >> 16)}
			}
		case 0x09: // Set Features
			if cdw10&0xff == 0x07 { // Number of Queues: grant 1 I/O queue pair
				return 0x0000_0000, n.sq[q].cqid
			}
		}
		return 0, n.sq[q].cqid
	}

	// I/O commands.
	switch op {
	case 0x02, 0x01: // Read / Write
		slba := uint64(cdw10) | (uint64(cdw11) << 32)
		nlb := (cdw12 & 0xffff) + 1
		n.transfer(op == 0x01, slba, nlb, prp1, prp2)
	}
	return 0, n.sq[q].cqid
}

// transfer copies nlb 512-byte blocks at slba between the disk and guest buffers
// described by the PRP list (PRP1, then PRP2 directly or as a page list).
func (n *nvmeController) transfer(write bool, slba uint64, nlb uint32, prp1, prp2 uint64) {
	const lba, page = 512, 4096
	total := int(nlb) * lba
	off := slba * lba
	for done := 0; done < total; {
		gpa := n.prpAddr(prp1, prp2, done, total, page)
		chunk := page - int(gpa&(page-1)) // up to the guest buffer's page boundary
		if chunk > total-done {
			chunk = total - done
		}
		if write {
			if b := n.mem.ReadGuest(gpa, chunk); b != nil {
				copy(n.diskSlice(off+uint64(done), chunk), b)
			}
		} else {
			n.mem.WriteGuest(gpa, n.diskBytes(off+uint64(done), chunk))
		}
		done += chunk
	}
}

// prpAddr resolves the guest-physical address for byte offset `done` of a
// `total`-byte transfer. PRP1 covers from its (possibly non-zero) page offset to
// the end of its page; the rest is covered by PRP2 — directly when the whole
// transfer fits in two pages, otherwise as a list of page-aligned pointers.
func (n *nvmeController) prpAddr(prp1, prp2 uint64, done, total, page int) uint64 {
	offset := int(prp1) & (page - 1)
	first := page - offset // bytes addressed by PRP1's page
	if done < first {
		return prp1 + uint64(done)
	}
	rem := done - first
	pageIdx, pageOff := rem/page, rem%page
	if total <= first+page { // exactly one more page: PRP2 is that page
		return prp2 + uint64(pageOff)
	}
	if b := n.mem.ReadGuest(prp2+uint64(pageIdx)*8, 8); b != nil { // PRP list
		return le64(b) + uint64(pageOff)
	}
	return 0
}

// identify writes a 4096-byte Identify data structure to the guest buffer.
func (n *nvmeController) identify(cns uint32, prp1 uint64) {
	buf := make([]byte, 4096)
	switch cns {
	case 0x01: // Identify Controller
		copy(buf[4:24], padText("WEAVE0000000000000001", 20))   // SN
		copy(buf[24:64], padText("weave NVMe", 40))             // MN
		copy(buf[64:72], padText("1.0", 8))                     // FR
		binary.LittleEndian.PutUint32(buf[516:], 1)            // NN = 1 namespace
		buf[512] = 0x66                                        // SQES (min/max 64)
		buf[513] = 0x44                                        // CQES (min/max 16)
	case 0x00: // Identify Namespace
		nlbas := uint64(len(n.disk)) / 512
		binary.LittleEndian.PutUint64(buf[0:], nlbas)  // NSZE
		binary.LittleEndian.PutUint64(buf[8:], nlbas)  // NCAP
		binary.LittleEndian.PutUint64(buf[16:], nlbas) // NUSE
		buf[25] = 0                                     // NLBAF = 0 (one format)
		binary.LittleEndian.PutUint32(buf[128:], 9<<16) // LBAF0: LBADS=9 (512 B)
	}
	n.mem.WriteGuest(prp1, buf)
}

// complete posts a completion-queue entry and advances the CQ tail/phase.
func (n *nvmeController) complete(cqid uint16, sqid int, sqhead uint32, cid uint16, res uint32) {
	if int(cqid) >= maxNvmeQueues || n.cq[cqid].size == 0 {
		return
	}
	cq := &n.cq[cqid]
	var e [16]byte
	binary.LittleEndian.PutUint32(e[0:], res)
	binary.LittleEndian.PutUint16(e[8:], uint16(sqhead+1))
	binary.LittleEndian.PutUint16(e[10:], uint16(sqid))
	binary.LittleEndian.PutUint16(e[12:], cid)
	binary.LittleEndian.PutUint16(e[14:], uint16(cq.phase)) // SC=0 (success) | phase
	n.mem.WriteGuest(cq.base+uint64(cq.tail)*nvmeCQES, e[:])
	cq.tail++
	if cq.tail >= cq.size {
		cq.tail = 0
		cq.phase ^= 1
	}
	// MSI-X: deliver this CQ's vector via Apple's GIC. The guest programmed the
	// message address/data; Apple's GIC takes the data as the interrupt id and
	// raises it. edk2 polls (MSI-X disabled), so this only fires under Windows.
	if n.msixEnabled && int(cq.vector) < nvmeMSIXVectors {
		if v := n.msixTable[cq.vector]; v.ctrl&1 == 0 { // vector not masked
			hv.HvGicSendMsi(v.addr, v.data)
		}
	}
}

func (n *nvmeController) diskBytes(off uint64, count int) []byte {
	out := make([]byte, count)
	if off < uint64(len(n.disk)) {
		copy(out, n.disk[off:min(off+uint64(count), uint64(len(n.disk)))])
	}
	return out
}

func (n *nvmeController) diskSlice(off uint64, count int) []byte {
	if off+uint64(count) <= uint64(len(n.disk)) {
		return n.disk[off : off+uint64(count)]
	}
	return make([]byte, count) // out of range: discard
}

// helpers
func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

func mask(bytes int) uint64 {
	if bytes >= 8 {
		return ^uint64(0)
	}
	return (uint64(1) << (8 * bytes)) - 1
}

func padText(s string, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	copy(b, s)
	return b
}
