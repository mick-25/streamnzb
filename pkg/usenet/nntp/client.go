package nntp

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dialTimeout = 30 * time.Second

type Client struct {
	conn    *textproto.Conn
	netConn net.Conn
	host    string
	port    int
	ssl     bool
	user    string
	pass    string

	LastUsed time.Time
	pool     *ClientPool // Reference to parent pool for metrics
}

func NewClient(address string, port int, ssl bool) (*Client, error) {
	fullAddr := net.JoinHostPort(address, strconv.Itoa(port))
	var conn net.Conn
	var err error

	if ssl {
		dialer := &net.Dialer{Timeout: dialTimeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", fullAddr, nil)
	} else {
		conn, err = net.DialTimeout("tcp", fullAddr, dialTimeout)
	}

	if err != nil {
		return nil, err
	}

	// Validate connection
	// Set initial deadline for greeting
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	tp := textproto.NewConn(conn)
	_, _, err = tp.ReadResponse(200) // Expect 200 or 201 greeting
	if err != nil {
		tp.Close()
		return nil, err
	}
	// Clear deadline? Or keep it?
	// Better to set it per-operation. Clear for now to be safe.
	conn.SetDeadline(time.Time{})

	return &Client{
		conn:    tp,
		netConn: conn,
		host:    address,
		port:    port,
		ssl:     ssl,
	}, nil
}

// SetPool assigns the parent pool for metric tracking
func (c *Client) SetPool(p *ClientPool) {
	c.pool = p
}

func (c *Client) Authenticate(user, pass string) error {
	c.user = user
	c.pass = pass
	c.setDeadline()
	id, err := c.conn.Cmd("AUTHINFO USER %s", user)
	if err != nil {
		return err
	}
	c.conn.StartResponse(id)
	code, _, err := c.conn.ReadCodeLine(381) // 381 PASS required
	c.conn.EndResponse(id)

	if err != nil {
		// Sometimes servers respond 281 immediately if no pass needed?
		// But mostly 381. If we got 281, good.
		if code == 281 {
			return nil
		}
		return err
	}

	id, err = c.conn.Cmd("AUTHINFO PASS %s", pass)
	if err != nil {
		return err
	}
	c.conn.StartResponse(id)
	_, _, err = c.conn.ReadCodeLine(281) // 281 Authentication accepted
	c.conn.EndResponse(id)
	return err
}

func (c *Client) Group(group string) error {
	const maxRetries = 2

	for i := 0; i <= maxRetries; i++ {
		c.setDeadline()
		id, err := c.conn.Cmd("GROUP %s", group)
		if err != nil {
			if c.shouldRetry(0, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return err
		}

		c.conn.StartResponse(id)
		code, _, err := c.conn.ReadCodeLine(211)
		c.conn.EndResponse(id)

		if err == nil {
			return nil
		}

		if c.shouldRetry(code, err) {
			if recErr := c.Reconnect(); recErr == nil {
				continue
			}
		} else {
			return err
		}
	}
	// Return generic or last error
	return errors.New("group command failed after retries")
}

// bodyReader calls EndResponse when the body is fully read (on EOF), so the
// pipeline is notified only after consumption, per textproto semantics.
type bodyReader struct {
	io.Reader
	endResponse func()
	once        sync.Once
}

func (b *bodyReader) Read(p []byte) (n int, err error) {
	n, err = b.Reader.Read(p)
	if err == io.EOF {
		b.once.Do(b.endResponse)
	}
	return n, err
}

// formatMessageID returns the message-id for NNTP BODY: must be in angle brackets.
// NZB segment IDs are usually "id@host"; some include "<>". Avoid double-wrapping.
func formatMessageID(messageID string) string {
	s := strings.TrimSpace(messageID)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s
	}
	return "<" + s + ">"
}

// Body returns a Reader for the body of the article.
// Caller is responsible for reading until EOF (dot). EndResponse is called only after EOF.
func (c *Client) Body(messageID string) (io.Reader, error) {
	const maxRetries = 2
	var lastErr error

	for i := 0; i <= maxRetries; i++ {
		// 1. Send Command (message-id must be in angle brackets)
		c.setDeadline()
		bodyArg := formatMessageID(messageID)
		id, err := c.conn.Cmd("BODY %s", bodyArg)
		if err != nil {
			// Network error sending command?
			// Force reconnect and retry
			lastErr = err
			// Only retry if network error (not logical error, though Cmd usually is network)
			if c.shouldRetry(0, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return nil, err
		}

		// 2. Read Response (do NOT call EndResponse yet — body must be consumed first)
		c.conn.StartResponse(id)
		code, _, err := c.conn.ReadCodeLine(222)
		if err != nil {
			c.conn.EndResponse(id)
			lastErr = err
			if c.shouldRetry(code, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return nil, err
		}

		// Set deadline for body read to prevent indefinite blocking
		c.setDeadline()
		if c.netConn != nil {
			c.netConn.SetDeadline(time.Now().Add(5 * time.Minute))
		}
		metricR := &metricReader{r: c.conn.DotReader(), client: c}
		// Defer EndResponse until caller reads to EOF so pipeline matches actual consumption
		return &bodyReader{
			Reader:      metricR,
			endResponse: func() { c.conn.EndResponse(id) },
		}, nil
	}
	return nil, lastErr
}

// metricReader wraps io.Reader to track bytes read
type metricReader struct {
	r      io.Reader
	client *Client
}

func (m *metricReader) Read(p []byte) (n int, err error) {
	n, err = m.r.Read(p)
	if n > 0 && m.client.pool != nil {
		m.client.pool.TrackRead(n)
	}
	return n, err
}

func (c *Client) shouldRetry(code int, err error) bool {
	// Retry on Auth Required (480)
	if code == 480 {
		return true
	}
	// Retry on connection/network errors (code is 0 usually)
	// But NOT if it's a parsing error that yielded a valid textproto error code (like 430)
	// textproto.Error has Code. If err is textproto.Error, Code is set.
	// If err is io.EOF or net.OpError, Code is 0 (from ReadCodeLine return).
	if code == 0 && err != nil {
		return true
	}
	return false
}

func (c *Client) Reconnect() error {
	if c.conn != nil {
		c.conn.Close()
	}

	fullAddr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	var conn net.Conn
	var err error

	if c.ssl {
		dialer := &net.Dialer{Timeout: dialTimeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", fullAddr, nil)
	} else {
		conn, err = net.DialTimeout("tcp", fullAddr, dialTimeout)
	}

	if err != nil {
		return err
	}

	tp := textproto.NewConn(conn)
	_, _, err = tp.ReadResponse(200)
	if err != nil {
		tp.Close()
		return err
	}

	c.conn = tp
	c.netConn = conn

	// Re-authenticate
	if c.user != "" {
		return c.Authenticate(c.user, c.pass)
	}
	return nil
}

func (c *Client) Quit() error {
	return c.conn.Close()
}

func (c *Client) setDeadline() {
	if c.netConn != nil {
		c.netConn.SetDeadline(time.Now().Add(60 * time.Second))
	}
}

func (c *Client) setShortDeadline() {
	if c.netConn != nil {
		// Aggressive 2s timeout for STAT checks to ensure responsiveness during triage
		c.netConn.SetDeadline(time.Now().Add(2 * time.Second))
	}
}

// GetArticle fetches a full article by message ID (for proxy)
func (c *Client) GetArticle(messageID string) (string, error) {
	c.setDeadline()
	id, err := c.conn.Cmd("ARTICLE %s", messageID)
	if err != nil {
		return "", err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	_, _, err = c.conn.ReadCodeLine(220)
	if err != nil {
		return "", err
	}

	var lines []string
	for {
		line, err := c.conn.ReadLine()
		if err != nil {
			return "", err
		}
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	// Success! Return article
	result := strings.Join(lines, "\n")
	if c.pool != nil {
		c.pool.TrackRead(len(result))
	}
	return result, nil
}

// GetBody fetches article body by message ID (for proxy)
func (c *Client) GetBody(messageID string) (string, error) {
	c.setDeadline()
	id, err := c.conn.Cmd("BODY %s", messageID)
	if err != nil {
		return "", err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	_, _, err = c.conn.ReadCodeLine(222)
	if err != nil {
		return "", err
	}

	var lines []string
	for {
		line, err := c.conn.ReadLine()
		if err != nil {
			return "", err
		}
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	result := strings.Join(lines, "\n")
	if c.pool != nil {
		c.pool.TrackRead(len(result))
	}
	return result, nil
}

// drainBackendBody reads and discards lines from the backend until "." or error.
// Used when StreamBody fails so the connection is not left with unconsumed data (which causes "short response" on reuse).
func (c *Client) drainBackendBody() {
	const maxDrainLines = 10_000_000
	for i := 0; i < maxDrainLines; i++ {
		line, err := c.conn.ReadLine()
		if err != nil {
			return
		}
		if line == "." {
			return
		}
	}
}

// StreamBody fetches article body by message ID and streams it to w in NNTP format (222 line + body + .).
// This sends the first bytes to the client as soon as the backend responds, reducing client timeouts.
// On any error after sending BODY, the backend response is drained so the connection is safe to reuse.
func (c *Client) StreamBody(messageID string, w io.Writer) (written int64, err error) {
	c.setDeadline()
	id, err := c.conn.Cmd("BODY %s", messageID)
	if err != nil {
		return 0, err
	}

	c.conn.StartResponse(id)
	defer func() {
		c.conn.EndResponse(id)
		if err != nil {
			c.drainBackendBody()
		}
	}()

	_, _, err = c.conn.ReadCodeLine(222)
	if err != nil {
		return 0, err
	}

	header := "222 0 " + messageID + "\r\n"
	n, err := w.Write([]byte(header))
	written += int64(n)
	if err != nil {
		return written, err
	}

	for {
		line, err := c.conn.ReadLine()
		if err != nil {
			return written, err
		}
		if line == "." {
			break
		}
		line = line + "\r\n"
		n, err = w.Write([]byte(line))
		written += int64(n)
		if err != nil {
			return written, err
		}
	}

	n, err = w.Write([]byte(".\r\n"))
	written += int64(n)
	if err != nil {
		return written, err
	}
	if c.pool != nil {
		c.pool.TrackRead(int(written))
	}
	return written, nil
}

// GetHead fetches article headers by message ID (for proxy)
func (c *Client) GetHead(messageID string) (string, error) {
	c.setDeadline()
	id, err := c.conn.Cmd("HEAD %s", messageID)
	if err != nil {
		return "", err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	_, _, err = c.conn.ReadCodeLine(221)
	if err != nil {
		return "", err
	}

	var lines []string
	for {
		line, err := c.conn.ReadLine()
		if err != nil {
			return "", err
		}
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	result := strings.Join(lines, "\n")
	if c.pool != nil {
		c.pool.TrackRead(len(result))
	}
	return result, nil
}

// CheckArticle checks if an article exists (STAT command, for proxy)
func (c *Client) CheckArticle(messageID string) (bool, error) {
	c.setDeadline()
	id, err := c.conn.Cmd("STAT %s", messageID)
	if err != nil {
		return false, err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	code, _, err := c.conn.ReadCodeLine(223)
	if err != nil {
		if code == 430 {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
