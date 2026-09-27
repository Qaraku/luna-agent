// Command broken is the directory-listing plugin candidate that cannot complete
// the handshake, so a failed replacement of this tool is observable.
package main

import "os"

func main() { os.Exit(78) }
