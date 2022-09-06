// Copyright (c) WithSecure Corporation
// https://foundry.withsecure.com
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package main

import (
	"log"
	"os"
	"runtime"
	_ "unsafe"

	"github.com/usbarmory/tamago/soc/sifive/fu540"

	"github.com/usbarmory/GoTEE-example/mem"
)

//go:linkname ramStart runtime.ramStart
var ramStart uint64 = mem.NonSecureStart

//go:linkname ramSize runtime.ramSize
var ramSize uint64 = mem.NonSecureSize

//go:linkname hwinit runtime.hwinit
func hwinit() {
	// TODO: fu540.SetSupervisorExceptionHandler()
	fu540.UART1.Init()
}

//go:linkname printk runtime.printk
func printk(c byte) {
	fu540.UART1.Tx(c)
}

func init() {
	log.SetFlags(log.Ltime)
	log.SetOutput(os.Stdout)
}

func main() {
	log.Printf("%s/%s (%s) • supervisor", runtime.GOOS, runtime.GOARCH, runtime.Version())

	// uncomment to test memory protection
	// mem.TestAccess("supervisor")

	// yield back to secure monitor
	log.Printf("supervisor is about to yield back")
	exit()

	// this should be unreachable
	log.Printf("supervisor says goodbye")
}
