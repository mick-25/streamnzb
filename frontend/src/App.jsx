import { useState, useEffect, useRef } from 'react'
import Settings from './Settings'
import Login from './components/Login'
import DeviceManagement from './components/DeviceManagement'
import ChangePassword from './components/ChangePassword'
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { 
  ChartContainer, 
  ChartTooltip, 
  ChartTooltipContent,
  ChartLegendContent,
} from "@/components/ui/chart"
import { Area, AreaChart, ComposedChart, Line, Legend, ResponsiveContainer, XAxis, YAxis } from "recharts"
import { 
  Activity, Server, Zap, Globe, Settings as SettingsIcon, AlertCircle, 
  Sun, Moon, Monitor, X, Loader2, Tv, Clipboard, Check, ChevronDown, ChevronUp, MonitorPlay, Menu, LogOut
} from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"


const chartConfig = {
  speed: {
    label: "Speed",
    color: "hsl(var(--chart-1))",
  },
  conns: {
    label: "Connections",
    color: "hsl(var(--chart-2))",
  },
}

function formatDownloadedMb(mb) {
  const n = Number(mb) || 0
  if (n >= 1000) return { value: (n / 1000).toFixed(2), unit: 'GB' }
  return { value: n.toFixed(1), unit: 'MB' }
}

const DiscordIcon = (props) => (
  <svg role="img" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" fill="currentColor" {...props}>
    <path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z"/>
  </svg>
)

function App() {
  const [authenticated, setAuthenticated] = useState(false)
  const [currentUser, setCurrentUser] = useState(null)
  const [authToken, setAuthToken] = useState(localStorage.getItem('auth_token') || '')
  const [mustChangePassword, setMustChangePassword] = useState(false)
  const [stats, setStats] = useState(null)
  const [config, setConfig] = useState(null)
  const [saveStatus, setSaveStatus] = useState({ type: '', msg: '', errors: null })
  const [isSaving, setIsSaving] = useState(false)
  const [isRestarting, setIsRestarting] = useState(false)
  const isRestartingRef = useRef(false)
  const [error, setError] = useState(null)
  const [history, setHistory] = useState([])
  const [connHistory, setConnHistory] = useState([])
  const [showSettings, setShowSettings] = useState(false)
  const [wsStatus, setWsStatus] = useState('connecting')
  const [ws, setWs] = useState(null)
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'system')
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)
  const [version, setVersion] = useState(null)
  const hasLoggedOutRef = useRef(false)
  const authCheckTimeoutRef = useRef(null)
  
  const [logs, setLogs] = useState([])
  const logsEndRef = useRef(null)
  const [logsCollapsed, setLogsCollapsed] = useState(true)

  const MAX_HISTORY = 60
  const MAX_LOGS = 200

  // Merged series for dual-axis chart (speed + connections over time)
  const chartData = history.map((h, i) => ({
    time: h.time,
    speed: h.speed,
    conns: connHistory[i]?.conns ?? 0,
  }))

  // Fetch app info (version) on mount - public endpoint
  useEffect(() => {
    fetch('/api/info')
      .then(res => res.ok ? res.json() : null)
      .then(data => data?.version && setVersion(data.version))
      .catch(() => {})
  }, [])

  // Check authentication on mount - WebSocket will send auth_info on connect
  useEffect(() => {
    const token = localStorage.getItem('auth_token')
    if (!token) {
      // Check for legacy token in URL
      const pathParts = window.location.pathname.split('/').filter(p => p !== '')
      if (pathParts.length > 0 && pathParts[0] !== 'api') {
        // Legacy token in URL path
        hasLoggedOutRef.current = false
        setAuthenticated(true)
        setCurrentUser('legacy')
      } else {
        setAuthenticated(false)
      }
    } else {
      // Token exists - optimistically set authenticated to true
      // WebSocket will confirm or correct this when it connects
      hasLoggedOutRef.current = false
      setAuthToken(token)
      setAuthenticated(true)
      // Will be updated when WebSocket sends auth_info
      
      // Set timeout to clear invalid token if auth_info doesn't arrive
      authCheckTimeoutRef.current = setTimeout(() => {
        // If still waiting for auth after 5 seconds, token is likely invalid
        if (authenticated && !currentUser && wsStatus !== 'connected') {
          setAuthenticated(false)
          setAuthToken('')
          localStorage.removeItem('auth_token')
        }
      }, 5000)
    }
    // Auth will be confirmed when WebSocket connects and sends auth_info
    return () => {
      if (authCheckTimeoutRef.current) {
        clearTimeout(authCheckTimeoutRef.current)
      }
    }
  }, [])

  const handleLogin = (username, token, mustChange) => {
    hasLoggedOutRef.current = false
    setAuthenticated(true)
    setCurrentUser(username)
    setAuthToken(token)
    setMustChangePassword(mustChange)
    localStorage.setItem('auth_token', token)
  }

  const handleLogout = () => {
    hasLoggedOutRef.current = true
    setAuthenticated(false)
    setCurrentUser(null)
    setAuthToken('')
    localStorage.removeItem('auth_token')
    if (ws) {
      ws.close()
    }
    setWs(null)
    window.ws = null
  }

  useEffect(() => {
    const root = window.document.documentElement;
    root.classList.remove("light", "dark");

    if (theme === "system") {
      const systemTheme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
      root.classList.add(systemTheme);
    } else {
      root.classList.add(theme);
    }
    localStorage.setItem('theme', theme);
  }, [theme]);

  useEffect(() => {
    if (!authenticated) return
    if (hasLoggedOutRef.current) return // Don't connect if user has logged out

    let socket;
    let reconnectTimeout;

    const connect = () => {
      // Don't connect if user has logged out
      if (hasLoggedOutRef.current) return
      
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;
      const pathParts = window.location.pathname.split('/').filter(p => p !== '');
      const tokenPrefix = pathParts.length > 0 && pathParts[0] !== 'api' ? `/${pathParts[0]}` : '';
      // Use auth token if available, otherwise fall back to legacy token in URL
      const wsToken = authToken || (pathParts.length > 0 && pathParts[0] !== 'api' ? pathParts[0] : '');
      const wsUrl = `${protocol}//${host}${tokenPrefix}/api/ws${wsToken ? `?token=${wsToken}` : ''}`;
      socket = new WebSocket(wsUrl);

      socket.onopen = () => {
        if (isRestartingRef.current) {
            window.location.reload(); // Forces a clean home redirect
            return;
        }
        // Don't proceed if user has logged out
        if (hasLoggedOutRef.current) {
          socket.close();
          return;
        }
        setWsStatus('connected');
        setError(null);
        setWs(socket);
        window.ws = socket; // Make available globally for DeviceManagement
        setLogs([]); // Clear logs on reconnect
      };

      socket.onmessage = (event) => {
        // Ignore messages if user has logged out
        if (hasLoggedOutRef.current) return
        
        const msg = JSON.parse(event.data);
        
        switch (msg.type) {
          case 'stats': {
            const data = msg.payload;
            setStats(data);
            const timestamp = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
            setHistory(prev => [...prev, { time: timestamp, speed: data.total_speed_mbps }].slice(-MAX_HISTORY));
            setConnHistory(prev => [...prev, { time: timestamp, conns: data.active_connections }].slice(-MAX_HISTORY));
            break;
          }
          case 'config': {
            setConfig(msg.payload);
            // Also update global config for DeviceManagement if callback exists
            if (window.globalConfigCallback) {
              window.globalConfigCallback(msg.payload);
            }
            break;
          }
          case 'log_entry': {
             // Append log entry
             setLogs(prev => [...prev, msg.payload].slice(-MAX_LOGS));
             break;
          }
          case 'log_history': {
             // Replace logs with history
             setLogs(msg.payload.slice(-MAX_LOGS));
             break;
          }
          case 'save_status': {
            setSaveStatus({
                type: msg.payload.status === 'success' ? 'success' : 'error',
                msg: msg.payload.message,
                errors: msg.payload.errors
            });
            setIsSaving(false);
            break;
          }
          case 'auth_info': {
            // Auth info sent on WebSocket connect (replaces /api/auth/check)
            if (msg.payload?.version) setVersion(msg.payload.version)
            // Ignore if user has explicitly logged out
            if (hasLoggedOutRef.current) {
              socket.close();
              return;
            }
            if (msg.payload.authenticated) {
              // Clear auth check timeout since we got valid auth
              if (authCheckTimeoutRef.current) {
                clearTimeout(authCheckTimeoutRef.current)
                authCheckTimeoutRef.current = null
              }
              setAuthenticated(true);
              setCurrentUser(msg.payload.username);
              setMustChangePassword(msg.payload.must_change_password || false);
              // Token is already in localStorage from login
            } else {
              // Invalid token - clear it and show login immediately
              if (authCheckTimeoutRef.current) {
                clearTimeout(authCheckTimeoutRef.current)
                authCheckTimeoutRef.current = null
              }
              hasLoggedOutRef.current = false // Reset logout flag since we're clearing invalid token
              setAuthenticated(false);
              setCurrentUser(null);
              setAuthToken('')
              localStorage.removeItem('auth_token');
              socket.close(); // Close WebSocket since auth failed
              // Component will re-render and show Login screen
            }
            break;
          }
          case 'users_response': {
            // Handle devices list response - dispatch to DeviceManagement if callback exists
            if (window.deviceManagementCallback) {
              window.deviceManagementCallback(msg.payload);
            }
            break;
          }
          case 'user_response': {
            // Handle single device response - dispatch to DeviceManagement if callback exists
            if (window.deviceResponseCallback) {
              window.deviceResponseCallback(msg.payload);
            }
            break;
          }
          case 'user_action_response': {
            // Handle device action responses (create, delete, regenerate, update password)
            if (window.deviceActionCallback) {
              window.deviceActionCallback(msg.payload);
            }
            // Also handle password change callback (used by ChangePassword component)
            if (window.passwordChangeCallback) {
              window.passwordChangeCallback(msg.payload);
            }
            break;
          }
        }
      };

      socket.onclose = () => {
        setWsStatus('disconnected');
        setWs(null);
        window.ws = null; // Clear global reference
        // Don't reconnect if user has logged out
        if (!hasLoggedOutRef.current) {
          // If we were trying to authenticate and connection closed, check if we should show login
          // Wait a moment to see if auth_info came through before closing
          reconnectTimeout = setTimeout(() => {
            // If still authenticated but no WebSocket, try reconnecting
            // But if auth failed (authenticated is false), don't reconnect
            if (authenticated && !hasLoggedOutRef.current) {
              connect();
            }
          }, 3000);
        }
      };

      socket.onerror = () => {
        setError("Network Error: Could not connect to API");
        // If we were trying to authenticate and connection fails, clear token and show login
        if (authToken && authenticated && !currentUser) {
          setAuthenticated(false);
          setAuthToken('')
          localStorage.removeItem('auth_token');
        }
        socket.close();
      };
    };

    connect();
    return () => {
      if (socket) socket.close();
      if (reconnectTimeout) clearTimeout(reconnectTimeout);
    }
  }, [authenticated, authToken]);

  const sendCommand = (type, payload) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
          if (type === 'save_config' || type === 'save_user_configs') {
              setSaveStatus({ type: 'normal', msg: 'Validating and saving...', errors: null });
              setIsSaving(true);
          } else if (type === 'restart') {
              setIsRestarting(true);
              isRestartingRef.current = true;
          }
          ws.send(JSON.stringify({ type, payload }));
      }
  };

 const getHTTPSLink = () => {
      if (!config) return '#';
      let baseUrl = config.addon_base_url || window.location.origin;
      let url = baseUrl.replace(/\/$/, '');
      // Use admin manifest URL when user is admin (has token in path)
      if (currentUser && currentUser !== 'legacy' && authToken) {
        return `${url}/${authToken}/manifest.json`;
      }
      return `${url}/manifest.json`;
  }

  const handleInstallClick = (type) => {
      const httpsLink = getHTTPSLink();

      if (type === 'web') {
          // Encode the HTTPS manifest URL
          const encodedManifest = encodeURIComponent(httpsLink);
          window.open(`https://web.stremio.com/#/addons?addon=${encodedManifest}`, '_blank');
      } else if (type === 'copy') {
          navigator.clipboard.writeText(httpsLink).then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 2000);
          });
      }
  };

  const [copied, setCopied] = useState(false);


  // Show login page if not authenticated
  if (!authenticated) {
    return <Login onLogin={handleLogin} version={version} />
  }

  // Show password change page if password must be changed
  if (mustChangePassword && currentUser) {
    return <ChangePassword username={currentUser} onPasswordChanged={() => {
      setMustChangePassword(false)
      // Auth info will be updated when WebSocket sends auth_info (sent on connect/reconnect)
      // The server will automatically send updated auth_info after password change
    }} />
  }

  if (error && wsStatus === 'disconnected') {
      return (
        <div className="flex flex-col h-screen items-center justify-center gap-4">
            <AlertCircle className="h-12 w-12 text-destructive animate-pulse" />
            <div className="text-xl font-semibold text-destructive">{error}</div>
            <p className="text-muted-foreground">Retrying connection...</p>
        </div>
      )
  }

  if (!stats || isRestarting) return (
    <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">
            {isRestarting ? "Restarting StreamNZB..." : "Initializing StreamNZB Dashboard..."}
        </div>
        {isRestarting && <p className="text-muted-foreground animate-pulse">Redirecting to home shortly...</p>}
    </div>
  )

  return (
    <div className="min-h-screen bg-background text-foreground p-4 md:p-8">
      <header className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-4">
        <div className="flex items-center gap-3 w-full md:w-auto">
          {/* Mobile hamburger - next to title */}
          <Button
            variant="ghost"
            size="icon"
            className="md:hidden h-9 w-9"
            onClick={() => setMobileMenuOpen((open) => !open)}
            title="Menu"
          >
            <Menu className="h-5 w-5" />
          </Button>
          
          <div className="bg-primary p-2 rounded-lg">
            <Zap className="h-6 w-6 text-primary-foreground" />
          </div>
          <div>
            <h1 className="text-3xl font-bold tracking-tight flex items-baseline gap-2">
              StreamNZB
              {version && <span className="text-xs font-normal text-muted-foreground">v{version}</span>}
            </h1>
            <p className="text-sm text-muted-foreground">High-performance Usenet Streaming</p>
          </div>
        </div>
        
        {/* Desktop toolbar */}
        <div className="hidden md:flex items-center">
          <div className="flex items-center gap-1.5 bg-secondary/60 border border-border/60 rounded-xl px-1.5 py-1">
            {/* Theme selector */}
            <div className="flex items-center bg-background/70 rounded-lg p-0.5 gap-0.5">
              <Button
                variant={theme === 'light' ? 'default' : 'ghost'}
                size="icon"
                className="h-8 w-8"
                onClick={() => setTheme('light')}
              >
                <Sun className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === 'dark' ? 'default' : 'ghost'}
                size="icon"
                className="h-8 w-8"
                onClick={() => setTheme('dark')}
              >
                <Moon className="h-4 w-4" />
              </Button>
              <Button
                variant={theme === 'system' ? 'default' : 'ghost'}
                size="icon"
                className="h-8 w-8"
                onClick={() => setTheme('system')}
              >
                <Monitor className="h-4 w-4" />
              </Button>
            </div>

            {/* Install */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="outline"
                  size="icon"
                  className="h-8 w-8 md:w-auto md:px-3 gap-2"
                  disabled={!config}
                  title="Install options"
                >
                  <Tv className="h-4 w-4" />
                  <span className="hidden md:inline">Install</span>
                  <ChevronDown className="hidden md:inline h-4 w-4 opacity-50" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {/* <DropdownMenuItem onClick={() => handleInstallClick('client')}>
                    <MonitorPlay className="mr-2 h-4 w-4" />
                    <span>Stremio Client</span>
                </DropdownMenuItem> */}
                <DropdownMenuItem onClick={() => handleInstallClick('web')}>
                  <Globe className="mr-2 h-4 w-4" />
                  <span>Stremio Web</span>
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => handleInstallClick('copy')}>
                  {copied ? <Check className="mr-2 h-4 w-4" /> : <Clipboard className="mr-2 h-4 w-4" />}
                  <span>{copied ? 'Copied!' : 'Copy Link'}</span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>

            {/* Settings */}
            <Button
              variant="outline"
              size="icon"
              onClick={() => setShowSettings(true)}
              className="h-8 w-8 md:w-auto md:px-3 gap-2"
            >
              <SettingsIcon className="h-4 w-4" />
              <span className="hidden md:inline">Settings</span>
            </Button>

            {currentUser && currentUser !== 'legacy' && (
              <Button
                variant="outline"
                size="icon"
                onClick={handleLogout}
                className="h-8 w-8 md:w-auto md:px-3 gap-2"
                title="Logout"
              >
                <LogOut className="h-4 w-4" />
                <span className="hidden md:inline">Logout</span>
              </Button>
            )}

            {/* Discord */}
            <Button
              variant="default"
              size="icon"
              className="h-8 w-8 md:w-auto md:px-3 gap-2 bg-[#5865F2] hover:bg-[#4752C4] text-white"
              onClick={() => window.open('https://snzb.stream/discord', '_blank')}
            >
              <DiscordIcon className="h-4 w-4" />
              <span className="hidden md:inline">Discord</span>
            </Button>
          </div>
        </div>
      </header>

      {/* Mobile full-page menu overlay */}
      {mobileMenuOpen && (
        <div className="md:hidden fixed inset-0 z-50 bg-background">
          <div className="flex flex-col h-full">
            {/* Header with close button */}
            <div className="flex items-center justify-between p-4 border-b">
              <h2 className="text-xl font-semibold">Menu</h2>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => setMobileMenuOpen(false)}
                className="h-9 w-9"
              >
                <X className="h-5 w-5" />
              </Button>
            </div>

            {/* Menu content */}
            <div className="flex-1 overflow-y-auto p-4">
              <div className="flex flex-col gap-4 max-w-md mx-auto">
                {/* Theme selector */}
                <div>
                  <h3 className="text-sm font-medium text-muted-foreground mb-3">Theme</h3>
                  <div className="flex items-center justify-center bg-secondary/70 rounded-lg p-1 gap-1">
                    <Button
                      variant={theme === 'light' ? 'default' : 'ghost'}
                      size="icon"
                      className="h-10 w-10"
                      onClick={() => setTheme('light')}
                    >
                      <Sun className="h-5 w-5" />
                    </Button>
                    <Button
                      variant={theme === 'dark' ? 'default' : 'ghost'}
                      size="icon"
                      className="h-10 w-10"
                      onClick={() => setTheme('dark')}
                    >
                      <Moon className="h-5 w-5" />
                    </Button>
                    <Button
                      variant={theme === 'system' ? 'default' : 'ghost'}
                      size="icon"
                      className="h-10 w-10"
                      onClick={() => setTheme('system')}
                    >
                      <Monitor className="h-5 w-5" />
                    </Button>
                  </div>
                </div>

                {/* Install */}
                <div>
                  <h3 className="text-sm font-medium text-muted-foreground mb-3">Install</h3>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        variant="outline"
                        className="w-full justify-start gap-3 h-12"
                        disabled={!config}
                      >
                        <Tv className="h-5 w-5" />
                        <span>Install Addon</span>
                        <ChevronDown className="h-4 w-4 opacity-50 ml-auto" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="start" className="w-[calc(100vw-4rem)]">
                      <DropdownMenuItem onClick={() => handleInstallClick('web')}>
                        <Globe className="mr-2 h-4 w-4" />
                        <span>Stremio Web</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => handleInstallClick('copy')}>
                        {copied ? <Check className="mr-2 h-4 w-4" /> : <Clipboard className="mr-2 h-4 w-4" />}
                        <span>{copied ? 'Copied!' : 'Copy Link'}</span>
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>

                {/* Settings & Discord */}
                <div className="flex flex-col gap-3 pt-4 border-t">
                  <Button
                    variant="outline"
                    className="w-full justify-start gap-3 h-12"
                    onClick={() => {
                      setShowSettings(true)
                      setMobileMenuOpen(false)
                    }}
                  >
                    <SettingsIcon className="h-5 w-5" />
                    <span>Settings</span>
                  </Button>

                  {currentUser && currentUser !== 'legacy' && (
                    <Button
                      variant="ghost"
                      className="w-full justify-start gap-3 h-12"
                      onClick={() => {
                        handleLogout()
                        setMobileMenuOpen(false)
                      }}
                    >
                      <LogOut className="h-5 w-5" />
                      <span>Logout</span>
                    </Button>
                  )}

                  <Button
                    variant="default"
                    className="w-full justify-start gap-3 h-12 bg-[#5865F2] hover:bg-[#4752C4] text-white"
                    onClick={() => window.open('https://snzb.stream/discord', '_blank')}
                  >
                    <DiscordIcon className="h-5 w-5" />
                    <span>Discord</span>
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}
      
      {showSettings && (
        <Settings 
            initialConfig={config} 
            sendCommand={sendCommand} 
            saveStatus={saveStatus}
            isSaving={isSaving}
            adminToken={currentUser && currentUser !== 'legacy' ? authToken : null}
            onClose={() => {
                setShowSettings(false);
                setSaveStatus({ type: '', msg: '', errors: null });
            }} 
        />
      )}

      {/* Top row: compact KPI cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-6">
        <Card className="bg-card/80 border-border/80 shadow-sm">
          <CardContent className="p-4">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Total Speed</p>
            <p className="text-2xl md:text-3xl font-bold mt-1 tabular-nums">{(stats.total_speed_mbps ?? 0).toFixed(1)} <span className="text-sm font-normal text-muted-foreground">Mbps</span></p>
          </CardContent>
        </Card>
        <Card className="bg-card/80 border-border/80 shadow-sm">
          <CardContent className="p-4">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Active Connections</p>
            <p className="text-2xl md:text-3xl font-bold mt-1 tabular-nums">{stats.active_sessions?.length ?? 0}</p>
            <p className="text-xs text-muted-foreground mt-0.5">streaming</p>
          </CardContent>
        </Card>
        <Card className="bg-card/80 border-border/80 shadow-sm">
          <CardContent className="p-4">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Pool Connections</p>
            <p className="text-2xl md:text-3xl font-bold mt-1 tabular-nums">{stats.active_connections} <span className="text-sm font-normal text-muted-foreground">/ {stats.total_connections}</span></p>
          </CardContent>
        </Card>
        <Card className="bg-card/80 border-border/80 shadow-sm">
          <CardContent className="p-4">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Downloaded Today</p>
            <p className="text-2xl md:text-3xl font-bold mt-1 tabular-nums">
              {(() => {
                const { value, unit } = formatDownloadedMb(stats.total_downloaded_mb)
                return <>{value} <span className="text-sm font-normal text-muted-foreground">{unit}</span></>
              })()}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Main visualization: dual-axis Speed + Connections */}
      <Card className="mb-6 bg-card/80 border-border/80 shadow-sm overflow-hidden">
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-medium">Network activity</CardTitle>
          <p className="text-xs text-muted-foreground">Speed (Mbps) and active connections over time</p>
        </CardHeader>
        <CardContent className="p-0">
          <ChartContainer config={chartConfig} className="h-[200px] w-full">
            <ComposedChart data={chartData} margin={{ top: 36, right: 8, bottom: 8, left: 32 }}>
              <Legend content={<ChartLegendContent />} verticalAlign="top" align="right" wrapperStyle={{ paddingBottom: 4 }} />
              <XAxis dataKey="time" tick={{ fontSize: 10 }} />
              <YAxis yAxisId="left" tick={{ fontSize: 10 }} width={28} />
              <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 10 }} allowDecimals={false} width={28} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Line yAxisId="left" type="monotone" dataKey="speed" stroke="hsl(var(--chart-1))" strokeWidth={2} dot={false} isAnimationActive={false} name="speed" />
              <Line yAxisId="right" type="monotone" dataKey="conns" stroke="hsl(var(--chart-2))" strokeWidth={2} dot={false} isAnimationActive={false} name="conns" />
            </ComposedChart>
          </ChartContainer>
        </CardContent>
      </Card>

      {/* Active sessions list (compact, when any) */}
      {stats.active_sessions?.length > 0 && (
        <div className="mb-6">
          <div className="flex items-center gap-2 mb-2">
            <Activity className="h-4 w-4 text-primary" />
            <h2 className="text-sm font-semibold">Active streams</h2>
          </div>
          <div className="grid gap-2 md:grid-cols-2 lg:grid-cols-3">
            {stats.active_sessions.map(sess => (
              <div key={sess.id} className="group relative min-w-0 bg-card/80 border border-border/80 rounded-lg p-3 pr-10 shadow-sm">
                <div className="text-sm font-medium truncate pr-2 min-w-0" title={sess.title}>{sess.title}</div>
                <div className="text-xs text-muted-foreground truncate min-w-0">{sess.clients.join(', ')}</div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="absolute right-2 top-1/2 -translate-y-1/2 h-7 w-7 text-red-500 hover:text-red-400 hover:bg-red-500/15 transition-colors"
                  onClick={() => sendCommand('close_session', { id: sess.id })}
                  title="End stream"
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Bottom row: Providers (left) | Indexers (right) */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-8">
        {/* Left: Usenet Providers */}
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <Globe className="h-5 w-5 text-muted-foreground" />
            <h2 className="text-lg font-semibold tracking-tight">Usenet Providers</h2>
          </div>
          <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
            {stats.providers.map((p) => {
              const loadPct = (p.active_conns / (p.max_conns || 1)) * 100
              const trafficSharePct = p.usage_percent ?? 0
              const isHealthy = (p.active_conns >= 0 && p.max_conns > 0)
              return (
                <Card key={p.name} className="bg-card/80 border-border/80 shadow-sm">
                  <CardHeader className="p-3 pb-1">
                    <div className="flex items-center gap-2">
                      <span className={`h-2 w-2 rounded-full shrink-0 ${isHealthy ? 'bg-emerald-500' : 'bg-muted-foreground/50'}`} title={isHealthy ? 'Connected' : 'Idle'} />
                      <CardTitle className="text-sm font-bold truncate leading-tight" title={p.name}>{p.name}</CardTitle>
                      <Badge variant="outline" className="text-[10px] py-0 h-4 ml-auto">{p.max_conns}</Badge>
                    </div>
                    <p className="text-[10px] text-muted-foreground truncate pl-4" title={p.host}>{p.host}</p>
                  </CardHeader>
                  <CardContent className="p-3 pt-0">
                    <div className="flex items-center justify-between mt-2">
                      <div className="flex flex-col">
                        <span className="text-[10px] uppercase text-muted-foreground font-medium">Load</span>
                        <span className="text-sm font-bold">{loadPct.toFixed(0)}%</span>
                      </div>
                      <div className="flex flex-col text-right">
                        <span className="text-[10px] uppercase text-muted-foreground font-medium">Speed</span>
                        <span className="text-sm font-bold text-primary">{(p.current_speed_mbps ?? 0).toFixed(1)} <span className="text-[10px]">Mbps</span></span>
                      </div>
                    </div>
                    <div className="w-full bg-secondary h-1.5 rounded-full mt-2 overflow-hidden">
                      <div className="bg-primary h-full transition-all duration-500 rounded-full" style={{ width: `${loadPct}%` }} />
                    </div>
                    {stats.total_downloaded_mb > 0 && (
                      <>
                        <p className="text-[10px] text-muted-foreground mt-2 mb-1">Traffic share</p>
                        <div className="w-full bg-secondary h-2 rounded-full overflow-hidden">
                          <div className="h-full transition-all duration-500 rounded-full bg-[hsl(var(--chart-1))]/70" style={{ width: `${Math.min(trafficSharePct, 100)}%` }} />
                        </div>
                        <p className="text-[10px] text-muted-foreground mt-1">Downloaded: {(p.downloaded_mb ?? 0).toFixed(1)} MB · {trafficSharePct.toFixed(0)}% of total</p>
                      </>
                    )}
                    {stats.total_downloaded_mb <= 0 && (
                      <p className="text-[10px] text-muted-foreground mt-2">Downloaded: {(p.downloaded_mb ?? 0).toFixed(1)} MB</p>
                    )}
                  </CardContent>
                </Card>
              )
            })}
          </div>
        </div>

        {/* Right: Indexers */}
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <MonitorPlay className="h-5 w-5 text-muted-foreground" />
            <h2 className="text-lg font-semibold tracking-tight">Indexers</h2>
          </div>
          <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
            {stats.indexers?.map((idx) => {
              const apiUsedPct = idx.api_hits_limit > 0 ? ((idx.api_hits_limit - idx.api_hits_remaining) / idx.api_hits_limit) * 100 : 0
              const dlUsedPct = idx.downloads_limit > 0 ? ((idx.downloads_limit - idx.downloads_remaining) / idx.downloads_limit) * 100 : 0
              const barColor = (pct) => pct >= 90 ? 'bg-destructive' : pct >= 75 ? 'bg-yellow-500 dark:bg-yellow-600' : 'bg-emerald-500 dark:bg-emerald-600'
              const hasApiLimit = idx.api_hits_limit > 0
              const hasDlLimit = idx.downloads_limit > 0
              return (
                <Card key={idx.name} className="relative bg-card/80 border-border/80 shadow-sm overflow-hidden">
                  <div className="absolute left-0 top-0 bottom-0 w-1 bg-primary/30 rounded-l-md" aria-hidden />
                  <CardHeader className="p-4 pb-2">
                    <CardTitle className="text-base font-semibold truncate leading-tight" title={idx.name}>{idx.name}</CardTitle>
                  </CardHeader>
                  <CardContent className="p-4 pt-0">
                    <div className="grid grid-cols-2 gap-4">
                      <div className="space-y-1.5">
                        <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">API hits</p>
                        <p className="text-lg font-bold tabular-nums">{idx.api_hits_used}</p>
                        <p className="text-xs text-muted-foreground">
                          {hasApiLimit ? `of ${idx.api_hits_limit} today` : 'Unlimited'}
                        </p>
                        {hasApiLimit && (
                          <div className="w-full bg-secondary h-2 rounded-full overflow-hidden mt-1">
                            <div className={`h-full transition-all duration-500 rounded-full ${barColor(apiUsedPct)}`} style={{ width: `${apiUsedPct}%` }} />
                          </div>
                        )}
                        <p className="text-[11px] text-muted-foreground">All-time: {idx.api_hits_used_all_time ?? 0}</p>
                      </div>
                      <div className="space-y-1.5">
                        <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">Downloads</p>
                        <p className="text-lg font-bold tabular-nums">{idx.downloads_used}</p>
                        <p className="text-xs text-muted-foreground">
                          {hasDlLimit ? `of ${idx.downloads_limit} today` : 'Unlimited'}
                        </p>
                        {hasDlLimit && (
                          <div className="w-full bg-secondary h-2 rounded-full overflow-hidden mt-1">
                            <div className={`h-full transition-all duration-500 rounded-full ${barColor(dlUsedPct)}`} style={{ width: `${dlUsedPct}%` }} />
                          </div>
                        )}
                        <p className="text-[11px] text-muted-foreground">All-time: {idx.downloads_used_all_time ?? 0}</p>
                      </div>
                    </div>
                  </CardContent>
                </Card>
              )
            })}
            {(!stats.indexers || stats.indexers.length === 0) && (
              <div className="col-span-full py-8 text-center border border-dashed rounded-lg text-muted-foreground text-sm bg-card/40">
                No internal indexers configured.
              </div>
            )}
          </div>
        </div>
      </div>

      {/* System logs: collapsible */}
      <div className="mt-6">
        <Card className={`flex flex-col bg-card/80 border-border/80 shadow-sm overflow-hidden ${logsCollapsed ? '' : 'min-h-[200px]'}`}>
          <CardHeader
            className="py-3 px-4 border-b bg-muted/20 cursor-pointer select-none flex flex-row items-center justify-between space-y-0"
            onClick={() => setLogsCollapsed((c) => !c)}
          >
            <div className="flex items-center gap-2">
              <Clipboard className="h-4 w-4 text-muted-foreground" />
              <CardTitle className="text-sm font-medium">System Logs</CardTitle>
              {logsCollapsed && logs.length > 0 && (
                <span className="text-xs text-muted-foreground font-mono truncate max-w-[min(50vw,320px)]" title={logs[logs.length - 1]}>
                  {logs[logs.length - 1]}
                </span>
              )}
            </div>
            <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0">
              {logsCollapsed ? <ChevronDown className="h-4 w-4" /> : <ChevronUp className="h-4 w-4" />}
            </Button>
          </CardHeader>
          {!logsCollapsed && (
            <CardContent className="flex-1 p-0 overflow-hidden relative min-h-[240px]">
              <div className="absolute inset-0 overflow-y-auto p-4 font-mono text-xs space-y-1">
                {logs.length === 0 && <div className="text-muted-foreground italic">Waiting for logs...</div>}
                {logs.map((log, i) => (
                  <div key={i} className="whitespace-pre-wrap break-all border-b border-border/40 pb-0.5 mb-0.5 last:border-0">{log}</div>
                ))}
                <div ref={logsEndRef} />
              </div>
            </CardContent>
          )}
        </Card>
      </div>

    </div>
  )
}

export default App
