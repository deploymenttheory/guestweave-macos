//go:build darwin

package hvmm

import "fmt"

// Device-tree phandles. Values are arbitrary but must be unique and match every
// reference (interrupt-parent, clocks, msi-map, cpu-map).
const (
	phApbPclk uint32 = 0x8000
	phCpu0    uint32 = 0x8001
	phIntc    uint32 = 0x8002
	phIts     uint32 = 0x8003
	phPl061   uint32 = 0x8004
)

// virtDTB is weave's flattened device tree, generated from the same machine
// constants the platform models (RAM, UART, GICv3, virtio-mmio, PCIe, flash,
// fw_cfg, timer). It replaces a blob dumped from QEMU: edk2 ArmVirtQemu's
// MemoryInit PEIM reads it from the base of system RAM
// (PcdDeviceTreeInitialBaseAddress = 0x40000000) to discover the memory map and
// every device, so emitting it from code keeps the DTB in lock-step with the
// devices weave actually implements.
var virtDTB = buildDTB()

// buildDTB describes a QEMU-"virt"-compatible GICv3 machine (the layout edk2
// ArmVirtQemu expects) using weave's own address constants.
func buildDTB() []byte {
	root := &dtNode{}
	root.set("interrupt-parent", cells(phIntc))
	root.set("dma-coherent", nil)
	root.set("model", dtStr("weave,virt"))
	root.set("#size-cells", cells(2))
	root.set("#address-cells", cells(2))
	root.set("compatible", dtStr("linux,dummy-virt"))

	psci := root.node("psci")
	psci.set("migrate", cells(0xc4000005))
	psci.set("cpu_on", cells(0xc4000003))
	psci.set("cpu_off", cells(0x84000002))
	psci.set("cpu_suspend", cells(0xc4000001))
	psci.set("method", dtStr("smc"))
	psci.set("compatible", dtStrList("arm,psci-1.0", "arm,psci-0.2", "arm,psci"))

	mem := root.node("memory@40000000")
	mem.set("reg", regCells(bootRAMBase, uint64(bootRAMSize)))
	mem.set("device_type", dtStr("memory"))

	fwcfg := root.node(fmt.Sprintf("fw-cfg@%x", bootFwCfgBase))
	fwcfg.set("dma-coherent", nil)
	fwcfg.set("reg", regCells(bootFwCfgBase, 0x18))
	fwcfg.set("compatible", dtStr("qemu,fw-cfg-mmio"))

	// 32 virtio-mmio transports at bootVirtioBase + i*0x200, SPI 0x10+i (edge).
	// weave only services slot 0 (the block device); the rest read back empty
	// (magic 0) so edk2 probes and skips them.
	for i := uint64(0); i < 32; i++ {
		base := bootVirtioBase + i*bootVirtioSize
		vn := root.node(fmt.Sprintf("virtio_mmio@%x", base))
		vn.set("dma-coherent", nil)
		vn.set("interrupts", cells(0, uint32(0x10+i), 1))
		vn.set("reg", regCells(base, bootVirtioSize))
		vn.set("compatible", dtStr("virtio,mmio"))
	}

	pcie := root.node("pcie@10000000")
	pcie.set("interrupt-map-mask", cells(0x1800, 0, 0, 7))
	pcie.set("interrupt-map", pcieInterruptMap())
	pcie.set("#interrupt-cells", cells(1))
	pcie.set("ranges", cells(
		0x1000000, 0, 0, 0, 0x3eff0000, 0, 0x10000, // PIO window
		0x2000000, 0, 0x10000000, 0, 0x10000000, 0, 0x2eff0000, // 32-bit MMIO
		0x3000000, 0x80, 0, 0x80, 0, 0x80, 0, // 64-bit MMIO
	))
	pcie.set("reg", regCells(bootEcamBase, bootEcamSize))
	pcie.set("msi-map", cells(0, phIts, 0, 0x10000))
	pcie.set("dma-coherent", nil)
	pcie.set("bus-range", cells(0, 0xff))
	pcie.set("linux,pci-domain", cells(0))
	pcie.set("#size-cells", cells(2))
	pcie.set("#address-cells", cells(3))
	pcie.set("device_type", dtStr("pci"))
	pcie.set("compatible", dtStr("pci-host-ecam-generic"))

	rtc := root.node("pl031@9010000")
	rtc.set("clock-names", dtStr("apb_pclk"))
	rtc.set("clocks", cells(phApbPclk))
	rtc.set("interrupts", cells(0, 2, 4))
	rtc.set("reg", regCells(0x9010000, 0x1000))
	rtc.set("compatible", dtStrList("arm,pl031", "arm,primecell"))

	uart := root.node(fmt.Sprintf("pl011@%x", bootUARTBase))
	uart.set("clock-names", dtStrList("uartclk", "apb_pclk"))
	uart.set("clocks", cells(phApbPclk, phApbPclk))
	uart.set("interrupts", cells(0, 1, 4))
	uart.set("reg", regCells(bootUARTBase, 0x1000))
	uart.set("compatible", dtStrList("arm,pl011", "arm,primecell"))

	intc := root.node(fmt.Sprintf("intc@%x", gicDistBase))
	intc.set("phandle", cells(phIntc))
	intc.set("interrupts", cells(1, 9, 4))
	intc.set("reg", regCells(gicDistBase, 0x10000, gicRedistBase, 0x2000000))
	intc.set("#redistributor-regions", cells(1))
	intc.set("compatible", dtStr("arm,gic-v3"))
	intc.set("ranges", nil)
	intc.set("#size-cells", cells(2))
	intc.set("#address-cells", cells(2))
	intc.set("interrupt-controller", nil)
	intc.set("#interrupt-cells", cells(3))
	its := intc.node("its@8080000")
	its.set("phandle", cells(phIts))
	its.set("reg", regCells(0x8080000, 0x20000))
	its.set("#msi-cells", cells(1))
	its.set("msi-controller", nil)
	its.set("compatible", dtStr("arm,gic-v3-its"))

	flash := root.node("flash@0")
	flash.set("bank-width", cells(4))
	flash.set("reg", regCells(bootFlashBase, uint64(bootVarsBase), bootVarsBase, uint64(bootVarsSize)))
	flash.set("compatible", dtStr("cfi-flash"))

	cpus := root.node("cpus")
	cpus.set("#size-cells", cells(0))
	cpus.set("#address-cells", cells(1))
	core0 := cpus.node("cpu-map").node("socket0").node("cluster0").node("core0")
	core0.set("cpu", cells(phCpu0))
	cpu0 := cpus.node("cpu@0")
	cpu0.set("phandle", cells(phCpu0))
	cpu0.set("reg", cells(0))
	cpu0.set("compatible", dtStr("arm,cortex-a72"))
	cpu0.set("device_type", dtStr("cpu"))

	timer := root.node("timer")
	// secure, non-secure, virtual, hypervisor PPIs (13,14,11,10), level.
	timer.set("interrupts", cells(1, 0xd, 4, 1, 0xe, 4, 1, 0xb, 4, 1, 0xa, 4))
	timer.set("always-on", nil)
	timer.set("compatible", dtStrList("arm,armv8-timer", "arm,armv7-timer"))

	clk := root.node("apb-pclk")
	clk.set("phandle", cells(phApbPclk))
	clk.set("clock-output-names", dtStr("clk24mhz"))
	clk.set("clock-frequency", cells(0x16e3600)) // 24 MHz
	clk.set("#clock-cells", cells(0))
	clk.set("compatible", dtStr("fixed-clock"))

	root.node("aliases").set("serial0", dtStr("/pl011@9000000"))
	root.node("chosen").set("stdout-path", dtStr("/pl011@9000000"))

	return root.marshal()
}

// pcieInterruptMap routes each (device 0..3, INTx pin 1..4) to a GIC SPI, the
// standard QEMU-virt swizzle: SPI = 3 + ((device + pin - 1) mod 4), level.
func pcieInterruptMap() []byte {
	var b []byte
	for dev := uint32(0); dev < 4; dev++ {
		for pin := uint32(1); pin <= 4; pin++ {
			spi := 3 + (dev+pin-1)%4
			b = append(b, cells(dev<<11, 0, 0, pin, phIntc, 0, 0, 0, spi, 4)...)
		}
	}
	return b
}
