package app

// AssertPrivateDirectory does nothing on Windows, where Node has no process.getuid and the
// TypeScript CLI skipped the check.
func AssertPrivateDirectory(directory string) error { return nil }
