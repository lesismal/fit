package store

// syncDir does nothing on Windows, where a directory cannot be synced.
func syncDir(string) {}
