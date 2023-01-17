// Copyright (c) WithSecure Corporation
// https://foundry.withsecure.com
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/usbarmory/tamago/arm"
	usbarmory "github.com/usbarmory/tamago/board/usbarmory/mk2"
	"github.com/usbarmory/tamago/soc/nxp/imx6ul"
	"github.com/usbarmory/tamago/soc/nxp/usdhc"

	"github.com/usbarmory/GoTEE/monitor"
	"github.com/usbarmory/GoTEE/syscall"

	"github.com/usbarmory/GoTEE-example/mem"
	"github.com/usbarmory/GoTEE-example/util"

	"github.com/usbarmory/armory-boot/config"
	"github.com/usbarmory/armory-boot/disk"
	"github.com/usbarmory/armory-boot/exec"
)

// This example embeds the Trusted Applet and Main OS ELF binaries within the
// Trusted OS executable, using Go embed package.
//
// The loading strategy is up to implementers, on the NXP i.MX6 the armory-boot
// bootloader primitives can be used to create a bootable Trusted OS with
// authenticated disk loading of applets and kernels, see loadLinux() and:
//   https://pkg.go.dev/github.com/usbarmory/armory-boot

//go:embed assets/trusted_applet.elf
var taELF []byte

//go:embed assets/nonsecure_os_go.elf
var osELF []byte

// bootConfLinux is the path to the armory-boot configuration file for loading a
// Linux kernel as Non-secure OS.
const bootConfLinux = "/boot/armory-boot-nonsecure.conf"

// logHandler allows to override the GoTEE default handler and avoid
// interleaved logs, as the supervisor and applet contexts are logging
// simultaneously.
func logHandler(ctx *monitor.ExecCtx) (err error) {
	switch {
	case ctx.A0() == syscall.SYS_WRITE:
		if ssh != nil {
			util.BufferedTermLog(byte(ctx.A1()), !ctx.NonSecure(), ssh.Term)
		} else {
			util.BufferedStdoutLog(byte(ctx.A1()), !ctx.NonSecure())
		}
	case ctx.NonSecure() && ctx.A0() == syscall.SYS_EXIT:
		if ctx.Debug {
			ctx.Print()
		}

		return errors.New("exit")
	default:
		if !ctx.NonSecure() {
			return monitor.SecureHandler(ctx)
		}
	}

	return
}

// linuxHandler services the TrustZone Watchdog
func linuxHandler(ctx *monitor.ExecCtx) (err error) {
	if !ctx.NonSecure() {
		panic("unexpected processor mode")
	}

	if ctx.ExceptionVector == arm.FIQ && imx6ul.ARM.GetInterrupt() == imx6ul.TZ_WDOG.IRQ {
		log.Printf("SM servicing TrustZone Watchdog")
		imx6ul.TZ_WDOG.Service(watchdogTimeout)

		// PC must be adjusted when returning from FIQ exceptions
		// (Table 11-3, ARM® Cortex™ -A Series Programmer’s Guide).
		ctx.R15 -= 4

		return
	}

	return monitor.NonSecureHandler(ctx)
}

// loadApplet loads a TamaGo unikernel as trusted applet.
func loadApplet() (ta *monitor.ExecCtx, err error) {
	image := &exec.ELFImage{
		Region: mem.AppletRegion,
		ELF:    taELF,
	}

	if err = image.Load(); err != nil {
		return
	}

	if ta, err = monitor.Load(image.Entry(), image.Region, true); err != nil {
		return nil, fmt.Errorf("SM could not load applet, %v", err)
	}

	log.Printf("SM loaded applet addr:%#x entry:%#x size:%d", ta.Memory.Start(), ta.R15, len(taELF))

	// register example RPC receiver
	ta.Server.Register(&RPC{})

	// set stack pointer to the end of available memory
	ta.R13 = uint32(ta.Memory.End())

	// override default handler to improve logging
	ta.Handler = logHandler
	ta.Debug = true

	return
}

// loadNormalWorld loads a TamaGo unikernel as Normal World OS.
func loadNormalWorld(lock bool) (os *monitor.ExecCtx, err error) {
	image := &exec.ELFImage{
		Region: mem.NonSecureRegion,
		ELF:    osELF,
	}

	if err = image.Load(); err != nil {
		return
	}

	if os, err = monitor.Load(image.Entry(), image.Region, false); err != nil {
		return nil, fmt.Errorf("SM could not load kernel, %v", err)
	}

	log.Printf("SM loaded kernel addr:%#x entry:%#x size:%d", os.Memory.Start(), os.R15, len(osELF))

	if err = configureTrustZone(lock); err != nil {
		return nil, fmt.Errorf("SM could not configure TrustZone, %v", err)
	}

	// override default handler to improve logging
	os.Handler = logHandler
	os.Debug = true

	return
}

// loadLinux loads a Linux kernel as Normal World OS, the kernel configuration
// is read from an armory-boot configuration file on the given device ("eMMC"
// or "uSD").
func loadLinux(device string) (os *monitor.ExecCtx, err error) {
	var id int
	var card *usdhc.USDHC

	switch device {
	case "uSD":
		id = 10
		card = usbarmory.SD
	case "eMMC":
		id = 11
		card = usbarmory.MMC
	default:
		return nil, errors.New("invalid device")
	}

	// Set the device USDHC controller as Secure master to grant access to
	// the Trusted OS DMA region.
	if err = imx6ul.CSU.SetAccess(id, true, false); err != nil {
		return
	}

	part, err := disk.Detect(card, "")

	if err != nil {
		return
	}

	conf, err := config.Load(part, bootConfLinux, "", "")

	if err != nil {
		return
	}

	log.Printf("\n%s", conf.JSON)

	image := &exec.LinuxImage{
		Region:               mem.NonSecureRegion,
		Kernel:               conf.Kernel(),
		DeviceTreeBlob:       conf.DeviceTreeBlob(),
		InitialRamDisk:       conf.InitialRamDisk(),
		KernelOffset:         0x00800000,
		DeviceTreeBlobOffset: 0x07000000,
		InitialRamDiskOffset: 0x08000000,
		CmdLine:              conf.CmdLine,
	}

	if err = image.Load(); err != nil {
		return
	}

	if os, err = monitor.Load(image.Entry(), image.Region, false); err != nil {
		return nil, fmt.Errorf("SM could not load kernel, %v", err)
	}

	log.Printf("SM loaded kernel addr:%#x size:%d entry:%#x", os.Memory.Start(), len(image.Kernel), os.R15)

	if err = configureTrustZone(true); err != nil {
		return nil, fmt.Errorf("SM could not configure TrustZone, %v", err)
	}

	if err = grantPeripheralAccess(); err != nil {
		return nil, fmt.Errorf("SM could not configure TrustZone peripheral access, %v", err)
	}

	os.R0 = 0
	os.R2 = uint32(image.DTB())
	os.SPSR = arm.SVC_MODE

	// override default handler to service TrustZone Watchdog
	os.Handler = linuxHandler
	os.Debug = true

	return
}

func run(ctx *monitor.ExecCtx, wg *sync.WaitGroup) {
	mode := arm.ModeName(int(ctx.SPSR) & 0x1f)
	ns := ctx.NonSecure()

	log.Printf("SM starting mode:%s sp:%#.8x pc:%#.8x ns:%v", mode, ctx.R13, ctx.R15, ns)

	err := ctx.Run()

	if wg != nil {
		wg.Done()
	}

	log.Printf("SM stopped mode:%s sp:%#.8x lr:%#.8x pc:%#.8x ns:%v err:%v", mode, ctx.R13, ctx.R14, ctx.R15, ns, err)
}
