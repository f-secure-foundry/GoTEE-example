// Copyright (c) F-Secure Corporation
// https://foundry.f-secure.com
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	_ "unsafe"

	"github.com/f-secure-foundry/tamago/soc/imx6"
	"github.com/f-secure-foundry/tamago/soc/imx6/dcp"
	_ "github.com/f-secure-foundry/tamago/soc/imx6/imx6ul"

	"github.com/f-secure-foundry/GoTEE/syscall"

	"github.com/f-secure-foundry/GoTEE-example/mem"
)

//go:linkname ramStart runtime.ramStart
var ramStart uint32 = mem.NonSecureStart

//go:linkname ramSize runtime.ramSize
var ramSize uint32 = mem.NonSecureSize

//go:linkname hwinit runtime.hwinit
func hwinit() {
	imx6.Init()
	imx6.UART2.Init()
}

//go:linkname printk runtime.printk
func printk(c byte) {
	if imx6.Native {
		// monitor call to request logs on Secure World SSH console
		printSecure(c)
	} else {
		imx6.UART2.Tx(c)
	}
}

func init() {
	log.SetFlags(log.Ltime)
	log.SetOutput(os.Stdout)

	if !imx6.Native {
		return
	}

	if err := imx6.SetARMFreq(900); err != nil {
		panic(fmt.Sprintf("WARNING: error setting ARM frequency: %v", err))
	}
}

func main() {
	log.Printf("PL1 %s/%s (%s) • system/supervisor (Normal World)", runtime.GOOS, runtime.GOARCH, runtime.Version())

	if imx6.Native {
		log.Printf("PL1 in Normal World is about to perform DCP key derivation")

		dcp.Init()

		// this fails after restrictions are in place (see trusted_os/tz.go)
		k, err := dcp.DeriveKey(make([]byte, 8), make([]byte, 16), -1)

		if err != nil {
			log.Printf("PL1 in Normal World World failed to use DCP (%v)", err)
		} else {
			log.Printf("PL1 in Normal World successfully used DCP (%x)", k)
		}

		// Uncomment to test memory protection, this will hang Normal World and
		// therefore everything.
		// mem.TestAccess("PL1 in Normal World")
	}

	// yield back to secure monitor
	log.Printf("PL1 in Normal World is about to yield back")
	syscall.Monitor()

	// this should be unreachable
	log.Printf("PL1 in Normal World says goodbye")
}
