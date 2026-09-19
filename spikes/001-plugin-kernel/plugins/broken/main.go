// Deliberate demo fixture: exits without the go-plugin handshake.
package main

import "os"

func main() { os.Stderr.WriteString("broken demo fixture: refusing handshake\n"); os.Exit(17) }
