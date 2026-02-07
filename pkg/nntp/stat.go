package nntp

// StatArticle checks if an article exists without downloading it
func (c *Client) StatArticle(messageID string) (bool, error) {
	c.setShortDeadline()

	// Send STAT command
	id, err := c.conn.Cmd("STAT <%s>", messageID)
	if err != nil {
		return false, err
	}

	// Read response
	c.conn.StartResponse(id)
	code, _, err := c.conn.ReadCodeLine(223) // 223 = article exists
	c.conn.EndResponse(id)

	if err != nil {
		// Check if it's a "no such article" error (430)
		if code == 430 {
			return false, nil // Article doesn't exist, but no error
		}
		return false, err
	}

	return true, nil
}
