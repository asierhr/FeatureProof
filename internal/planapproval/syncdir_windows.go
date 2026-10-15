//go:build windows

package planapproval

// SyncDir acknowledges metadata durability on Windows where directory handles cannot be flushed through os.File.Sync.
func (osJournalFS) SyncDir(string) error { return nil }
