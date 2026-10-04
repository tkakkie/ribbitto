package realtime

// CachedLen reports how many batches c holds, for the external retention tests.
func CachedLen(c *CachedEvents) int { return c.cache.len() }
