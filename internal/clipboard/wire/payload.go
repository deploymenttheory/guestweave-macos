package wire

import (
	"fmt"
	"io"

	"github.com/deploymenttheory/weave/internal/guestweaveagent/proto"
)

// Module is the agent module name the clipboard registers under.
const Module = "clipboard"

// Clipboard operations carried in proto.Request.Op.
const (
	OpStat = "stat"
	OpGet  = "get"
	OpSet  = "set"
)

// Item describes one clipboard representation in the meta: its canonical format
// and the byte length of its data frame.
type Item struct {
	Format Canonical `json:"format"`
	Size   int       `json:"size"`
}

// FileRef describes one file in the meta: its base name and byte size. Files
// are streamed sequentially, one data frame each, after the items.
type FileRef struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Meta is the clipboard module's control payload, marshalled into a
// proto.Request.Meta or proto.Response.Meta. Fields are used per operation:
// stat responses set ChangeCount/AgentVersion/OS; get requests set Allowed; get
// responses and set requests set Items/Files describing the data frames.
type Meta struct {
	ChangeCount  uint64      `json:"changeCount,omitempty"`
	AgentVersion string      `json:"agentVersion,omitempty"`
	OS           string      `json:"os,omitempty"`
	Allowed      []Canonical `json:"allowed,omitempty"`
	MaxBytes     int64       `json:"maxBytes,omitempty"` // per-item/file cap the sender must honour (0 = unlimited)
	Items        []Item      `json:"items,omitempty"`
	Files        []FileRef   `json:"files,omitempty"`
}

// DataItem is one clipboard representation with its bytes (e.g. the RTF or PNG
// data). Items travel before files in a payload.
type DataItem struct {
	Format Canonical
	Data   []byte
}

// DataFile is one file with its bytes.
type DataFile struct {
	Name string
	Data []byte
}

// Payload is a full clipboard snapshot: typed items plus files.
type Payload struct {
	Items []DataItem
	Files []DataFile
}

// Empty reports whether the payload carries nothing.
func (p Payload) Empty() bool { return len(p.Items) == 0 && len(p.Files) == 0 }

// OversizeDrop describes one representation removed by CapTo because it exceeded
// the per-item/file size cap. Format is set for an item (Name empty); Name is
// set for a file (Format empty).
type OversizeDrop struct {
	Format Canonical
	Name   string
	Size   int
}

// CapTo returns p with any item or file whose data exceeds maxBytes removed
// (maxBytes <= 0 means unlimited), plus a description of what was dropped. It
// mirrors the host capture cap (macpb.Read's tooBig) so that a guest→host
// payload cannot exceed the policy's MaxContentBytes regardless of what the
// guest sends — the host enforces the cap on receive, the guest applies it
// before transfer to avoid wasting bandwidth.
func (p Payload) CapTo(maxBytes int64) (Payload, []OversizeDrop) {
	if maxBytes <= 0 {
		return p, nil
	}
	var kept Payload
	var dropped []OversizeDrop
	for _, it := range p.Items {
		if int64(len(it.Data)) > maxBytes {
			dropped = append(dropped, OversizeDrop{Format: it.Format, Size: len(it.Data)})
			continue
		}
		kept.Items = append(kept.Items, it)
	}
	for _, f := range p.Files {
		if int64(len(f.Data)) > maxBytes {
			dropped = append(dropped, OversizeDrop{Name: f.Name, Size: len(f.Data)})
			continue
		}
		kept.Files = append(kept.Files, f)
	}
	return kept, dropped
}

// MetaFor builds the meta describing p, filling item formats/sizes and file
// names/sizes in transmission order.
func MetaFor(p Payload) Meta {
	var m Meta
	for _, item := range p.Items {
		m.Items = append(m.Items, Item{Format: item.Format, Size: len(item.Data)})
	}
	for _, file := range p.Files {
		m.Files = append(m.Files, FileRef{Name: file.Name, Size: int64(len(file.Data))})
	}
	return m
}

// Gate is an optional per-frame hook called with each data frame's byte length
// just before it is read or written, used by the host engine to apply the
// bandwidth limiter. A nil Gate means no throttling.
type Gate func(n int) error

// WriteBody writes the payload's data frames (items then files, in order),
// invoking gate before each. The caller writes the meta-bearing envelope first.
func WriteBody(w io.Writer, p Payload, gate Gate) error {
	for _, item := range p.Items {
		if err := gateAndFrame(w, item.Data, gate); err != nil {
			return err
		}
	}
	for _, file := range p.Files {
		if err := gateAndFrame(w, file.Data, gate); err != nil {
			return err
		}
	}
	return nil
}

func gateAndFrame(w io.Writer, data []byte, gate Gate) error {
	if gate != nil {
		if err := gate(len(data)); err != nil {
			return err
		}
	}
	return proto.WriteFrame(w, data)
}

// ReadBody reads the data frames described by m (items then files, in order),
// invoking gate before each, and assembles a Payload.
func ReadBody(r io.Reader, m Meta, gate Gate) (Payload, error) {
	var p Payload
	for _, item := range m.Items {
		if gate != nil {
			if err := gate(item.Size); err != nil {
				return Payload{}, err
			}
		}
		data, err := proto.ReadFrame(r)
		if err != nil {
			return Payload{}, err
		}
		if len(data) != item.Size {
			return Payload{}, fmt.Errorf("clipboard: item %q frame size %d != declared %d", item.Format, len(data), item.Size)
		}
		p.Items = append(p.Items, DataItem{Format: item.Format, Data: data})
	}
	for _, file := range m.Files {
		if gate != nil {
			if err := gate(int(file.Size)); err != nil {
				return Payload{}, err
			}
		}
		data, err := proto.ReadFrame(r)
		if err != nil {
			return Payload{}, err
		}
		p.Files = append(p.Files, DataFile{Name: file.Name, Data: data})
	}
	return p, nil
}
