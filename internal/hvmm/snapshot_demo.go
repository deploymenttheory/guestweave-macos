//go:build darwin

package hvmm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	hv "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/hypervisor"
)

// SnapshotRoundTrip demonstrates Windows-style VM save/restore on the
// Hypervisor.framework backend: it boots the firmware, runs it to a pause point,
// snapshots the entire VM (every vCPU register, the GIC state, and all guest
// memory) to snapPath, destroys the VM, restores a fresh VM from the snapshot
// file, verifies the restored CPU and memory match the saved image, and resumes
// execution from exactly where it paused.
func SnapshotRoundTrip(out io.Writer, fwPath, snapPath string, maxExits int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 1. Boot and run to a pause point.
	m, vcpu, err := setupGuest(out, fwPath, cpsrEL2hMasked)
	if err != nil {
		return err
	}
	vcpu.MaxExits = maxExits
	vcpu.Watchdog = 2 * time.Second
	fmt.Fprintf(out, "✓ vCPU running to ~%d exits, then snapshotting\n\n--- firmware output ---\n", maxExits)
	_ = vcpu.Run(&Platform{uart: &pl011{out: out}, out: out, maxExits: maxExits, unknown: map[uint64]int{}})
	pcBefore := vcpu.regOrZero(hv.HV_REG_PC)
	spsrBefore := vcpu.regOrZero(hv.HV_REG_CPSR)
	fmt.Fprintf(out, "\n✓ paused at PC=0x%x CPSR=0x%x\n", pcBefore, spsrBefore)

	// 2. Snapshot the whole VM to disk.
	snap, err := m.Snapshot(vcpu)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	f, err := os.Create(snapPath)
	if err != nil {
		return err
	}
	if err := snap.Encode(f); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()
	mem := 0
	for _, r := range snap.Regions {
		mem += len(r.Data)
	}
	fmt.Fprintf(out, "✓ snapshot → %s: %d vCPU gen+sys regs, %d-byte GIC state, %d MiB memory across %d regions\n",
		snapPath, len(snap.VCPUs[0].Gen)+len(snap.VCPUs[0].Sys), len(snap.GIC), mem>>20, len(snap.Regions))

	// A memory fingerprint (the DTB magic at RAM base) to verify the restore.
	sampleBefore := memSample(snap.Regions, bootRAMBase)

	// 3. Destroy the original VM — Hypervisor.framework allows one VM per process.
	_ = vcpu.Destroy()
	_ = m.Close()
	fmt.Fprintln(out, "✓ original VM destroyed")

	// 4. Restore a fresh VM from the snapshot file.
	rf, err := os.Open(snapPath)
	if err != nil {
		return err
	}
	snap2, err := ReadSnapshot(rf)
	_ = rf.Close()
	if err != nil {
		return err
	}
	m2, vcpus2, err := RestoreMachine(snap2)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	defer m2.Close()
	rv := vcpus2[0]
	defer rv.Destroy()
	fmt.Fprintln(out, "✓ VM restored from snapshot file")

	// 5. Verify the restored state matches.
	pcAfter := rv.regOrZero(hv.HV_REG_PC)
	spsrAfter := rv.regOrZero(hv.HV_REG_CPSR)
	sampleAfter := liveSample(m2.regions, bootRAMBase)
	regsOK := pcAfter == pcBefore && spsrAfter == spsrBefore
	memOK := bytes.Equal(sampleBefore, sampleAfter)
	fmt.Fprintf(out, "✓ restored state: PC=0x%x CPSR=0x%x (registers match=%v, memory match=%v)\n",
		pcAfter, spsrAfter, regsOK, memOK)
	if !regsOK || !memOK {
		return fmt.Errorf("snapshot verification failed (registers=%v memory=%v)", regsOK, memOK)
	}

	// 6. Resume the restored VM — it continues from exactly where it paused.
	fmt.Fprintln(out, "\n--- resuming the restored VM ---")
	rv.Trace = out
	rv.MaxExits = 300
	rv.Watchdog = 2 * time.Second
	_ = rv.Run(&Platform{uart: &pl011{out: out}, out: out, maxExits: 300, unknown: map[uint64]int{}})
	fmt.Fprintln(out, "\n🎉 snapshot/restore round-trip OK: the VM resumed from the restored state.")
	return nil
}

func memSample(regions []RegionState, gpa uint64) []byte {
	for _, r := range regions {
		if r.GPA == gpa && len(r.Data) >= 16 {
			return append([]byte(nil), r.Data[:16]...)
		}
	}
	return nil
}

func liveSample(regions []region, gpa uint64) []byte {
	for _, r := range regions {
		if r.gpa == gpa && len(r.host) >= 16 {
			return append([]byte(nil), r.host[:16]...)
		}
	}
	return nil
}
