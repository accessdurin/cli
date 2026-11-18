package app

import "os"

// fileUID is fs.Stats#uid, which Node reports as 0 on Windows.
func fileUID(os.FileInfo) uint32 { return 0 }
