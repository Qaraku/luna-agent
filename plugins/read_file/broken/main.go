// Command broken is the file-read plugin candidate that cannot complete the
// handshake, so a failed replacement of this tool is observable.
package main

import "os"

func main() { os.Exit(78) }
