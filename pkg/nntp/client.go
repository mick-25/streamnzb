package nntp

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	conn    *textproto.Conn
	netConn net.Conn
	host    string
	port    int
	ssl     bool
	user    string
	pass    string
	
	LastUsed time.Time
}

func NewClient(address string, port int, ssl bool) (*Client, error) {
	fullAddr := net.JoinHostPort(address, strconv.Itoa(port))
	var conn net.Conn
	var err error

	if ssl {
		conn, err = tls.Dial("tcp", fullAddr, nil)
	} else {
		conn, err = net.Dial("tcp", fullAddr)
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

// Body returns a Reader for the body of the article.
// Caller is responsible for reading until EOF (dot).
func (c *Client) Body(messageID string) (io.Reader, error) {
	const maxRetries = 2
	var lastErr error

	for i := 0; i <= maxRetries; i++ {
		// 1. Send Command
		c.setDeadline()
		id, err := c.conn.Cmd("BODY <%s>", messageID)
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

		// 2. Read Response
		c.conn.StartResponse(id)
		code, _, err := c.conn.ReadCodeLine(222)
		c.conn.EndResponse(id)

		if err == nil {
			return c.conn.DotReader(), nil
		}
		
		lastErr = err

		// 3. Handle Errors
		// Retry on 480 (Auth) or 0 (Network)
		if c.shouldRetry(code, err) {
			// If we reconnected successfully, loop again
			if recErr := c.Reconnect(); recErr == nil {
				continue
			}
			// If reconnect fails, we probably return the reconnect error or original?
			// Let's return the original error wrapped or just fail.
		} else {
			// Unrecoverable error (e.g. 430 Not Found)
			return nil, err
		}
	}

	return nil, lastErr
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
		conn, err = tls.Dial("tcp", fullAddr, nil)
	} else {
		conn, err = net.Dial("tcp", fullAddr)
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

type AuthError struct {
	Inner error
}

func (e *AuthError) Error() string {
	return "authentication required (480)"
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
	
	return strings.Join(lines, "\n"), nil
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
	
	return strings.Join(lines, "\n"), nil
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
	
	return strings.Join(lines, "\n"), nil
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
