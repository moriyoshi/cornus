package herdr

import "os"

// readRepoFile reads a file from this package's own directory. The vendored
// LICENSE and README are repository artifacts rather than embedded assets, so
// they are checked on disk.
func readRepoFile(name string) ([]byte, error) { return os.ReadFile(name) }
