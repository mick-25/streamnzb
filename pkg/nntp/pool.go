package nntp

import (
	"sync"
	"time"
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
	mu          sync.Mutex
}

func NewClientPool(host string, port int, ssl bool, user, pass string, maxConn int) *ClientPool {
	p := &ClientPool{
		host:    host,
		port:    port,
		ssl:     ssl,
		user:    user,
		pass:    pass,
		maxConn: maxConn,
		idleClients: make(chan *Client, maxConn),
		slots:       make(chan struct{}, maxConn),
	}
	
	// Fill slots with permits
	for i := 0; i < maxConn; i++ {
		p.slots <- struct{}{}
	}
	
	// Start Reaper
	go p.reaperLoop()
	
	return p
}

func (p *ClientPool) Get() (*Client, error) {
	// 1. Prefer Idle Client
	select {
	case c := <-p.idleClients:
		return c, nil
	default:
	}
	
	// 2. Try to create new connection (check slots)
	select {
	case <-p.slots:
		// Got permit, dial
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{} // Return permit on failure
			return nil, err
		}
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		return c, nil
	default:
	}

	// 3. Block and wait for resource
	select {
	case c := <-p.idleClients:
		return c, nil
	case <-p.slots:
		// Got permit, dial
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, err
		}
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		return c, nil
	}
}

// TryGet attempts to get a client without blocking.
func (p *ClientPool) TryGet() (*Client, bool) {
	// 1. Check Idle
	select {
	case c := <-p.idleClients:
		return c, true
	default:
	}
	
	// 2. Check Slots
	select {
	case <-p.slots:
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, false
		}
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
	if c == nil { return }
	c.LastUsed = time.Now()
	
	select {
	case p.idleClients <- c:
	default:
		// Buffer full? Should not happen if logic is correct (slots + idle <= max).
		// But if it does, force close to avoid leak?
		c.Quit()
		p.slots <- struct{}{}
	}
}

// InitializedPool creates 'count' connections immediately. (Legacy/Eager)
func NewInitializedClientPool(host string, port int, ssl bool, user, pass string, count int) (*ClientPool, error) {
	p := NewClientPool(host, port, ssl, user, pass, count)
	
	// Eagerly fill
	// We consume slots and put into idleClients
	for i := 0; i < count; i++ {
		select {
		case <-p.slots:
			c, err := NewClient(host, port, ssl)
			if err != nil {
				return nil, err
			}
			if err := c.Authenticate(user, pass); err != nil {
				c.Quit()
				return nil, err
			}
			p.Put(c)
		default:
			// Max reached
		}
	}
	return p, nil
}

func (p *ClientPool) reaperLoop() {
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	timeout := 30 * time.Second                // Close connections idle >30s
	
	for range ticker.C {
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
func (p *ClientPool) Validate() error {
	c, err := p.Get()
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
