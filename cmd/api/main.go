// Command api is the WalletCore HTTP server entry point.
//
// Phase 0 keeps this minimal so the module builds. Phase 1 wires config,
// logging, the database pool, the router and graceful shutdown here.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "walletcore skeleton: wiring lands in Phase 1")
	os.Exit(0)
}
