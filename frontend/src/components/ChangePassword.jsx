import { useState, useEffect } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { AlertCircle, Loader2 } from "lucide-react"

export default function ChangePassword({ username, onPasswordChanged }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [ws, setWs] = useState(null)

  // Wait for WebSocket connection
  useEffect(() => {
    const checkWS = () => {
      if (window.ws && window.ws.readyState === WebSocket.OPEN) {
        setWs(window.ws)
        return
      }
      // Retry after a short delay
      setTimeout(checkWS, 100)
    }
    checkWS()
  }, [])

  const sendCommand = (type, payload) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type, payload }))
      return true
    }
    return false
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError('')

    if (newPassword !== confirmPassword) {
      setError('New passwords do not match')
      return
    }

    if (newPassword.length < 6) {
      setError('Password must be at least 6 characters long')
      return
    }

    if (!ws || ws.readyState !== WebSocket.OPEN) {
      setError('WebSocket not connected. Please wait...')
      return
    }

    setLoading(true)

    try {
      const token = localStorage.getItem('auth_token') || ''
      
      // First verify current password by attempting login
      const loginResponse = await fetch('/api/login', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify({ username, password: currentPassword }),
      })

      const loginData = await loginResponse.json()
      if (!loginData.success) {
        setError('Current password is incorrect')
        setLoading(false)
        return
      }

      // Current password is correct, now update it via WebSocket
      window.passwordChangeCallback = (payload) => {
        setLoading(false)
        if (payload.error) {
          setError(payload.error)
        } else {
          // Update token if new one was provided
          if (loginData.token) {
            localStorage.setItem('auth_token', loginData.token)
          }
          // Password changed successfully, notify parent
          onPasswordChanged()
        }
        delete window.passwordChangeCallback
      }

      if (!sendCommand('update_password', { username, password: newPassword })) {
        setError('Failed to send update request')
        setLoading(false)
        delete window.passwordChangeCallback
      }
    } catch (err) {
      setError('Failed to connect to server')
      setLoading(false)
      delete window.passwordChangeCallback
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="space-y-1">
          <CardTitle className="text-2xl font-bold">Change Password</CardTitle>
          <CardDescription>
            You must change your password before continuing
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            {error && (
              <div className="flex items-center gap-2 p-3 text-sm text-destructive bg-destructive/10 rounded-md">
                <AlertCircle className="h-4 w-4" />
                <span>{error}</span>
              </div>
            )}
            
            <div className="space-y-2">
              <Label htmlFor="current-password">Current Password</Label>
              <Input
                id="current-password"
                type="password"
                placeholder="Enter current password"
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
                required
                autoFocus
                disabled={loading}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="new-password">New Password</Label>
              <Input
                id="new-password"
                type="password"
                placeholder="Enter new password (min 6 characters)"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
                disabled={loading}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="confirm-password">Confirm New Password</Label>
              <Input
                id="confirm-password"
                type="password"
                placeholder="Confirm new password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                required
                disabled={loading}
              />
            </div>

            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Changing password...
                </>
              ) : (
                'Change Password'
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
