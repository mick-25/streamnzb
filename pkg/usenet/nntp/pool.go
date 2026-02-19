package nntp

import (
	"context"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

type ClientPool struct {
	host    string
	port    int
	ssl     bool
	user    string
	pass    string
	maxConn int

	idleClients chan *Client
	slots       chan struct{} // Semaphore tokens for creating new connections
	// Metrics
	bytesRead         int64   // bytes since last speed sample (kept for compatibility)
	totalBytesRead    int64   // cumulative bytes read (lifetime)
	lastTotalBytes    int64   // totalBytesRead at last GetSpeed sample (for delta-based speed)
	lastSpeed         float64 // Mbps
	lastCheck         time.Time

	// Reporting: pool reports bytes to usage manager; manager owns persistence
	providerName  string
	usageManager  *ProviderUsageManager

	mu     sync.Mutex
	closed bool
}

func NewClientPool(host string, port int, ssl bool, user, pass string, maxConn int) *ClientPool {
	p := &ClientPool{
		host:        host,
		port:        port,
		ssl:         ssl,
		user:        user,
		pass:        pass,
		maxConn:     maxConn,
		idleClients: make(chan *Client, maxConn),
		slots:       make(chan struct{}, maxConn),
		lastCheck:   time.Now(),
	}

	// Fill slots with permits
	for i := 0; i < maxConn; i++ {
		p.slots <- struct{}{}
	}

	go p.reaperLoop()
	return p
}

// SetUsageManager configures the pool to persist usage data
func (p *ClientPool) SetUsageManager(name string, mgr *ProviderUsageManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providerName = name
	p.usageManager = mgr
}

// RestoreTotalBytes allows persisted counters to be injected on startup
func (p *ClientPool) RestoreTotalBytes(total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.totalBytesRead = total
	p.lastTotalBytes = total // avoid fake speed spike from delta at startup
}

// TrackRead updates the total bytes read and reports the delta to the usage manager.
// The pool only records; ProviderUsageManager owns when to persist.
func (p *ClientPool) TrackRead(n int) {
	p.mu.Lock()
	p.bytesRead += int64(n)
	p.totalBytesRead += int64(n)
	usageMgr := p.usageManager
	providerName := p.providerName
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" && n > 0 {
		usageMgr.AddBytes(providerName, int64(n))
	}
}

// minSpeedWindow is the minimum duration (seconds) before we report speed from delta.
const minSpeedWindow = 0.05

// maxSpeedDuration caps the duration used for speed calculation so that after
// idle we don't show an artificially low speed (delta/small over huge seconds).
const maxSpeedDuration = 5.0

// GetSpeed returns the current speed in Mbps.
// Speed is derived from the delta of totalBytesRead over time so that any
// TrackRead() activity is reflected. The clock (lastCheck) is advanced on
// every call so seeking or a new stream sees correct speed immediately.
func (p *ClientPool) GetSpeed() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	duration := now.Sub(p.lastCheck).Seconds()

	// Always advance the clock so the next sample has a correct window (avoids
	// "stuck" speed after idle or when starting a new stream).
	p.lastCheck = now

	if duration < minSpeedWindow {
		return p.lastSpeed
	}

	// Cap duration so we don't understate speed after long idle
	if duration > maxSpeedDuration {
		duration = maxSpeedDuration
	}

	delta := p.totalBytesRead - p.lastTotalBytes
	p.lastTotalBytes = p.totalBytesRead

	if delta > 0 {
		// Mbps = (bytes * 8) / (1024*1024) / seconds
		p.lastSpeed = (float64(delta) * 8) / (1024 * 1024) / duration
	} else {
		const decay = 0.35
		p.lastSpeed *= decay
		if p.lastSpeed < 0.1 {
			p.lastSpeed = 0
		}
	}
	return p.lastSpeed
}

// TotalMegabytes returns the cumulative downloaded data in megabytes (lifetime)
func (p *ClientPool) TotalMegabytes() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return float64(p.totalBytesRead) / (1024 * 1024)
}

func (p *ClientPool) Get(ctx context.Context) (*Client, error) {
	logger.Trace("pool.Get start", "host", p.host)
	// 1. Prefer Idle Client
	select {
	case <-ctx.Done():
		logger.Trace("pool.Get ctx.Done (idle check)", "host", p.host)
		return nil, ctx.Err()
	case c := <-p.idleClients:
		logger.Trace("pool.Get from idle", "host", p.host)
		return c, nil
	default:
	}

	// 2. Try to create new connection (check slots)
	select {
	case <-ctx.Done():
		logger.Trace("pool.Get ctx.Done (slot check)", "host", p.host)
		return nil, ctx.Err()
	case <-p.slots:
		// Got permit, dial
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{} // Return permit on failure
			return nil, err
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		logger.Trace("pool.Get new client", "host", p.host)
		return c, nil
	default:
	}

	// 3. Block and wait for resource
	logger.Trace("pool.Get blocking", "host", p.host)
	select {
	case <-ctx.Done():
		logger.Trace("pool.Get ctx.Done (blocking)", "host", p.host)
		return nil, ctx.Err()
	case c := <-p.idleClients:
		logger.Trace("pool.Get from idle (after block)", "host", p.host)
		return c, nil
	case <-p.slots:
		// Got permit, dial
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, err
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		logger.Trace("pool.Get new client (after block)", "host", p.host)
		return c, nil
	}
}

// TryGet attempts to get a client without blocking.
func (p *ClientPool) TryGet(ctx context.Context) (*Client, bool) {
	// 1. Check Idle
	select {
	case <-ctx.Done():
		return nil, false
	case c := <-p.idleClients:
		return c, true
	default:
	}

	// 2. Check Slots
	select {
	case <-ctx.Done():
		return nil, false
	case <-p.slots:
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, false
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, false
		}
		return c, true
	default:
		return nil, false
	}
}

func (p *ClientPool) Put(c *Client) {
	if c == nil {
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		// Shutdown closed idleClients; don't send on closed channel (would panic)
		c.Quit()
		p.slots <- struct{}{}
		return
	}
	c.LastUsed = time.Now()
	logger.Trace("pool.Put", "host", p.host)

	select {
	case p.idleClients <- c:
	default:
		// Buffer full? Should not happen if logic is correct (slots + idle <= max).
		// But if it does, force close to avoid leak?
		c.Quit()
		p.slots <- struct{}{}
	}
}

// Discard closes the client and releases its slot. Use when the connection cannot be reused
// (e.g. body was not fully read), so the next user does not get a connection in a bad state.
func (p *ClientPool) Discard(c *Client) {
	if c == nil {
		return
	}
	logger.Trace("pool.Discard", "host", p.host)
	c.Quit()
	p.slots <- struct{}{}
}

func (p *ClientPool) reaperLoop() {
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	timeout := 30 * time.Second                // Close connections idle >30s

	for range ticker.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		// Scan idle clients
		// We want to check ALL currently idle clients.
		// However, channel is FIFO/random.
		// Strategy: Iterate 'count' times equal to current length.
		// If used recently, put back. If old, close.

		count := len(p.idleClients)
		for i := 0; i < count; i++ {
			select {
			case c := <-p.idleClients:
				if time.Since(c.LastUsed) > timeout {
					// Idle timeout
					c.Quit()
					p.slots <- struct{}{} // Release permit
				} else {
					// Still fresh, keep
					p.idleClients <- c
				}
			default:
				// Empty
			}
		}
	}
}

// Validate checks if the pool can successfully connect and authenticate.
// Uses a timeout to avoid blocking config save/validation indefinitely when the pool is exhausted.
func (p *ClientPool) Validate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := p.Get(ctx)
	if err != nil {
		return err
	}
	p.Put(c)
	return nil
}

func (p *ClientPool) Host() string {
	return p.host
}

func (p *ClientPool) MaxConn() int {
	return p.maxConn
}

// TotalConnections returns the number of open connections (active + idle)
func (p *ClientPool) TotalConnections() int {
	return p.maxConn - len(p.slots)
}

// IdleConnections returns the number of idle connections ready for reuse
func (p *ClientPool) IdleConnections() int {
	return len(p.idleClients)
}

// ActiveConnections returns the number of connections continuously using bandwidth
func (p *ClientPool) ActiveConnections() int {
	return p.TotalConnections() - p.IdleConnections()
}

// Shutdown closes all connections and asks the usage manager to flush this provider's total.
func (p *ClientPool) Shutdown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	usageMgr := p.usageManager
	providerName := p.providerName
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" {
		usageMgr.FlushProvider(providerName)
	}

	close(p.idleClients)
	for c := range p.idleClients {
		c.Quit()
	}
}
