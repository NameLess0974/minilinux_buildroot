package arp

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"time"
)

// Cache provides cached ARP lookups from IP to MAC address
type Cache struct {
	entries sync.Map // map[string]entry
	ttl     time.Duration
}

type entry struct {
	mac       string
	expiresAt time.Time
}

// NewCache creates a new ARP cache with the specified TTL
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl: ttl,
	}
}

// Lookup returns the MAC address for an IP, using cache if available
func (c *Cache) Lookup(ip string) string {
	// Check cache first
	if e, ok := c.entries.Load(ip); ok {
		ent := e.(entry)
		if time.Now().Before(ent.expiresAt) {
			return ent.mac
		}
		// Expired, delete from cache
		c.entries.Delete(ip)
	}

	// Lookup from /proc/net/arp
	mac := c.lookupARP(ip)
	if mac != "" && mac != "UNKNOWN" {
		c.entries.Store(ip, entry{
			mac:       mac,
			expiresAt: time.Now().Add(c.ttl),
		})
	}

	return mac
}

// lookupARP reads /proc/net/arp to find MAC for IP
func (c *Cache) lookupARP(ip string) string {
	file, err := os.Open("/proc/net/arp")
	if err != nil {
		return "UNKNOWN"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Skip header line
	scanner.Scan()

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == ip {
			mac := fields[3]
			// Validate MAC (not empty or zero)
			if mac != "00:00:00:00:00:00" && strings.Contains(mac, ":") {
				return strings.ToUpper(mac)
			}
		}
	}

	return "UNKNOWN"
}

// Invalidate removes an entry from the cache
func (c *Cache) Invalidate(ip string) {
	c.entries.Delete(ip)
}

// Clear removes all entries from the cache
func (c *Cache) Clear() {
	c.entries.Range(func(key, _ interface{}) bool {
		c.entries.Delete(key)
		return true
	})
}

// Size returns the number of entries in the cache (approximate)
func (c *Cache) Size() int {
	count := 0
	c.entries.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
