//go:build darwin

package hvmm

import "encoding/binary"

// This is a minimal builder + serializer for the Flattened Device Tree (FDT/DTB)
// binary format (https://devicetree-specification.readthedocs.io). weave uses it
// to emit its OWN device tree from its actual machine model (see buildDTB in
// dtb.go) rather than embedding a blob dumped from QEMU — so the RAM size, device
// bases, and interrupt routing all derive from the same constants the MMIO
// handler uses, and stay correct by construction.

const (
	fdtMagic     = 0xd00dfeed
	fdtBeginNode = 0x00000001
	fdtEndNode   = 0x00000002
	fdtPropTok   = 0x00000003
	fdtEnd       = 0x00000009
	fdtVersion   = 17
	fdtCompVer   = 16
)

// dtNode is a device-tree node: a name, ordered properties, and child nodes.
type dtNode struct {
	name  string
	props []dtProp
	kids  []*dtNode
}

type dtProp struct {
	name string
	val  []byte
}

// set appends a property (value may be nil for a boolean/empty property).
func (n *dtNode) set(name string, val []byte) *dtNode {
	n.props = append(n.props, dtProp{name, val})
	return n
}

// node appends and returns a child node.
func (n *dtNode) node(name string) *dtNode {
	c := &dtNode{name: name}
	n.kids = append(n.kids, c)
	return c
}

// cells encodes a list of 32-bit big-endian cells (the device-tree word).
func cells(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.BigEndian.PutUint32(b[4*i:], v)
	}
	return b
}

// regCells encodes <hi lo> address/size pairs as two cells each (the #*-cells=2
// convention this machine uses for /memory, MMIO reg, etc.).
func regCells(vs ...uint64) []byte {
	b := make([]byte, 8*len(vs))
	for i, v := range vs {
		binary.BigEndian.PutUint64(b[8*i:], v)
	}
	return b
}

// dtStr encodes a single NUL-terminated string property.
func dtStr(s string) []byte { return append([]byte(s), 0) }

// dtStrList encodes a NUL-separated, NUL-terminated string list (e.g. compatible).
func dtStrList(ss ...string) []byte {
	var b []byte
	for _, s := range ss {
		b = append(b, s...)
		b = append(b, 0)
	}
	return b
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// marshal serializes the tree rooted at n into a DTB blob (header + memory
// reservation block + structure block + strings block, all big-endian).
func (n *dtNode) marshal() []byte {
	var strs []byte
	offs := map[string]uint32{}
	strOff := func(name string) uint32 {
		if o, ok := offs[name]; ok {
			return o
		}
		o := uint32(len(strs))
		offs[name] = o
		strs = append(append(strs, name...), 0)
		return o
	}

	var st []byte
	tok := func(t uint32) { st = binary.BigEndian.AppendUint32(st, t) }
	var emit func(*dtNode)
	emit = func(nd *dtNode) {
		tok(fdtBeginNode)
		st = pad4(append(st, append([]byte(nd.name), 0)...))
		for _, p := range nd.props {
			tok(fdtPropTok)
			st = binary.BigEndian.AppendUint32(st, uint32(len(p.val)))
			st = binary.BigEndian.AppendUint32(st, strOff(p.name))
			st = pad4(append(st, p.val...))
		}
		for _, c := range nd.kids {
			emit(c)
		}
		tok(fdtEndNode)
	}
	emit(n)
	tok(fdtEnd)

	const hdrLen = 40
	rsv := make([]byte, 16) // single terminating (0,0) reservation entry
	offStruct := uint32(hdrLen + len(rsv))
	offStrings := offStruct + uint32(len(st))
	total := offStrings + uint32(len(strs))

	out := make([]byte, total)
	be := binary.BigEndian
	be.PutUint32(out[0:], fdtMagic)
	be.PutUint32(out[4:], total)
	be.PutUint32(out[8:], offStruct)
	be.PutUint32(out[12:], offStrings)
	be.PutUint32(out[16:], hdrLen) // off_mem_rsvmap
	be.PutUint32(out[20:], fdtVersion)
	be.PutUint32(out[24:], fdtCompVer)
	be.PutUint32(out[28:], 0) // boot_cpuid_phys
	be.PutUint32(out[32:], uint32(len(strs)))
	be.PutUint32(out[36:], uint32(len(st)))
	copy(out[hdrLen:], rsv)
	copy(out[offStruct:], st)
	copy(out[offStrings:], strs)
	return out
}
