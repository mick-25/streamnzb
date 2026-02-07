import { useState, useEffect, useRef } from 'react'
import Settings from './Settings'
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { 
  ChartContainer, 
  ChartTooltip, 
  ChartTooltipContent 
} from "@/components/ui/chart"
import { Area, AreaChart, ResponsiveContainer, XAxis, YAxis } from "recharts"
import { 
  Activity, Server, Zap, Globe, Settings as SettingsIcon, AlertCircle, 
  Sun, Moon, Monitor, X, Loader2, Tv, Clipboard, Check, ChevronDown, MonitorPlay
} from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"


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

const DiscordIcon = (props) => (
  <svg role="img" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" fill="currentColor" {...props}>
    <path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028 14.09 14.09 0 0 0 1.226-1.994.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03zM8.02 15.33c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.956-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.956 2.418-2.157 2.418zm7.975 0c-1.183 0-2.157-1.085-2.157-2.419 0-1.333.955-2.418 2.157-2.418 1.21 0 2.176 1.096 2.157 2.42 0 1.333-.946 2.418-2.157 2.418z"/>
  </svg>
)

function App() {
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
  
  const [logs, setLogs] = useState([])
  const logsEndRef = useRef(null)
  
  const MAX_HISTORY = 60
  const MAX_LOGS = 200

  // Auto-scroll logs
  useEffect(() => {
    if (logsEndRef.current) {
        logsEndRef.current.scrollIntoView({ behavior: "smooth" });
    }
  }, [logs]);

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
    let socket;
    let reconnectTimeout;

    const connect = () => {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;
      const pathParts = window.location.pathname.split('/').filter(p => p !== '');
      const tokenPrefix = pathParts.length > 0 ? `/${pathParts[0]}` : '';
      socket = new WebSocket(`${protocol}//${host}${tokenPrefix}/api/ws`);

      socket.onopen = () => {
        if (isRestartingRef.current) {
            window.location.reload(); // Forces a clean home redirect
            return;
        }
        setWsStatus('connected');
        setError(null);
        setWs(socket);
        setLogs([]); // Clear logs on reconnect
      };

      socket.onmessage = (event) => {
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
             setTimeout(() => {
                 if (logsEndRef.current) {
                     logsEndRef.current.scrollIntoView({ behavior: "auto" });
                 }
             }, 100);
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
        }
      };

      socket.onclose = () => {
        setWsStatus('disconnected');
        setWs(null);
        reconnectTimeout = setTimeout(connect, 3000);
      };

      socket.onerror = () => {
        setError("Network Error: Could not connect to API");
        socket.close();
      };
    };

    connect();
    return () => {
      if (socket) socket.close();
      if (reconnectTimeout) clearTimeout(reconnectTimeout);
    }
  }, []);

  const sendCommand = (type, payload) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
          if (type === 'save_config') {
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
      // Ensure protocol is https if it's not present (though origin usually has it)
      // Actually we just want the full manifest URL in HTTP(S) format
      let url = baseUrl.replace(/\/$/, '');
      const token = config.security_token ? `/${config.security_token}` : '';
      return `${url}${token}/manifest.json`;
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
      <header className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-8">
        <div className="flex items-center gap-3">
            <div className="bg-primary p-2 rounded-lg">
                <Zap className="h-6 w-6 text-primary-foreground" />
            </div>
            <div>
                <h1 className="text-3xl font-bold tracking-tight">StreamNZB</h1>
                <p className="text-sm text-muted-foreground">High-performance Usenet Streaming</p>
            </div>
        </div>
        <div className="flex items-center gap-2">
          

          <div className="flex items-center bg-secondary rounded-lg p-1 mr-2">
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
          
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button 
                    variant="outline" 
                    size="sm" 
                    className="gap-2" 
                    disabled={!config}
                    title="Install options"
                >
                    <Tv className="h-4 w-4" />
                    <span className="hidden sm:inline">Install</span>
                    <ChevronDown className="h-4 w-4 opacity-50" />
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

          <Button variant="outline" size="sm" onClick={() => setShowSettings(true)} className="gap-2">
            <SettingsIcon className="h-4 w-4" />
            Settings
          </Button>

          <Button 
            variant="default" 
            size="sm" 
            className="gap-2 bg-[#5865F2] hover:bg-[#4752C4] text-white"
            onClick={() => window.open('https://discord.gg/rivenmedia', '_blank')}
          >
            <DiscordIcon className="h-4 w-4" />
          </Button>
        </div>
      </header>
      
      {showSettings && (
        <Settings 
            initialConfig={config} 
            sendCommand={sendCommand} 
            saveStatus={saveStatus}
            isSaving={isSaving}
            onClose={() => {
                setShowSettings(false);
                setSaveStatus({ type: '', msg: '', errors: null });
            }} 
        />
      )}

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
        <Card className="flex flex-col">
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Active Connections</CardTitle>
            <Activity className={`h-4 w-4 ${stats.active_sessions?.length > 0 ? 'text-primary animate-pulse' : 'text-muted-foreground'}`} />
          </CardHeader>
          <CardContent className="flex-1 overflow-y-auto max-h-[140px] pt-2">
            {stats.active_sessions?.length > 0 ? (
                <div className="space-y-3">
                    {stats.active_sessions.map(sess => (
                        <div key={sess.id} className="group relative bg-secondary/30 rounded-md p-2 pr-10">
                            <div className="text-xs font-bold truncate pr-2" title={sess.title}>{sess.title}</div>
                            <div className="text-[10px] text-muted-foreground truncate">
                                {sess.clients.join(', ')}
                            </div>
                            <Button 
                                variant="ghost" 
                                size="icon" 
                                className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 text-destructive hover:bg-destructive/10"
                                onClick={() => sendCommand('close_session', { id: sess.id })}
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                    ))}
                </div>
            ) : (
                <div className="flex flex-col items-center justify-center h-full text-muted-foreground py-4">
                    <div className="text-2xl font-bold text-foreground">0</div>
                    <p className="text-xs mt-1">No active playback</p>
                </div>
            )}
          </CardContent>
        </Card>

        <Card className="md:col-span-2 overflow-hidden flex flex-col">
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <div>
                <CardTitle className="text-sm font-medium">Total Speed</CardTitle>
                <div className="text-2xl font-bold mt-1">{stats.total_speed_mbps.toFixed(1)} <span className="text-sm font-normal text-muted-foreground">Mbps</span></div>
            </div>
            <Zap className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent className="p-0 flex-1">
            <ChartContainer config={chartConfig} className="h-[120px] w-full">
                <AreaChart data={history}>
                    <defs>
                        <linearGradient id="fillSpeed" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="5%" stopColor="hsl(var(--chart-1))" stopOpacity={0.4}/>
                            <stop offset="100%" stopColor="hsl(var(--chart-1))" stopOpacity={0.1}/>
                        </linearGradient>
                    </defs>
                    <Area 
                        type="monotone" 
                        dataKey="speed" 
                        stroke="hsl(var(--chart-1))" 
                        fill="url(#fillSpeed)" 
                        strokeWidth={2}
                        isAnimationActive={false}
                    />
                    <ChartTooltip content={<ChartTooltipContent hideLabel />} />
                </AreaChart>
            </ChartContainer>
          </CardContent>
        </Card>

        <Card className="md:col-span-3 overflow-hidden flex flex-col">
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
                <div>
                    <CardTitle className="text-sm font-medium">Pool Connections</CardTitle>
                    <div className="text-2xl font-bold mt-1">
                        {stats.active_connections} <span className="text-sm font-normal text-muted-foreground">/ {stats.total_connections} active</span>
                    </div>
                </div>
                <Server className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent className="p-0 flex-1">
               <ChartContainer config={chartConfig} className="h-[80px] w-full">
                    <AreaChart data={connHistory}>
                        <defs>
                            <linearGradient id="fillConns" x1="0" y1="0" x2="0" y2="1">
                                <stop offset="5%" stopColor="hsl(var(--chart-2))" stopOpacity={0.4}/>
                                <stop offset="100%" stopColor="hsl(var(--chart-2))" stopOpacity={0.1}/>
                            </linearGradient>
                        </defs>
                        <Area 
                            type="step" 
                            dataKey="conns" 
                            stroke="hsl(var(--chart-2))" 
                            fill="url(#fillConns)" 
                            strokeWidth={2}
                            isAnimationActive={false}
                        />
                        <ChartTooltip content={<ChartTooltipContent hideLabel />} />
                    </AreaChart>
                </ChartContainer>
            </CardContent>
        </Card>
      </div>

      <div className="space-y-4">
        <div className="flex items-center gap-2">
            <Globe className="h-5 w-5 text-muted-foreground" />
            <h2 className="text-xl font-semibold tracking-tight">Usenet Providers</h2>
        </div>
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
          {stats.providers.map((p) => (
            <Card key={p.name} className="bg-card/50">
               <CardHeader className="p-4 pb-2">
                  <div className="flex justify-between items-start">
                    <CardTitle className="text-sm font-bold truncate leading-tight" title={p.name}>{p.name}</CardTitle>
                    <Badge variant="outline" className="text-[10px] py-0 h-4">{p.max_conns}</Badge>
                  </div>
                  <p className="text-[10px] text-muted-foreground truncate" title={p.host}>{p.host}</p>
               </CardHeader>
               <CardContent className="p-4 pt-0">
                  <div className="flex items-center justify-between mt-2">
                     <div className="flex flex-col">
                        <span className="text-[10px] uppercase text-muted-foreground font-medium">Load</span>
                        <span className="text-sm font-bold">{((p.active_conns / (p.max_conns || 1)) * 100).toFixed(0)}%</span>
                     </div>
                     <div className="flex flex-col text-right">
                        <span className="text-[10px] uppercase text-muted-foreground font-medium">Speed</span>
                        <span className="text-sm font-bold text-primary">{p.current_speed_mbps.toFixed(1)} <span className="text-[10px]">M</span></span>
                     </div>
                  </div>
                  <div className="w-full bg-secondary h-1 rounded-full mt-2 overflow-hidden">
                     <div 
                        className="bg-primary h-full transition-all duration-500" 
                        style={{ width: `${(p.active_conns / (p.max_conns || 1)) * 100}%` }} 
                     />
                  </div>
               </CardContent>
            </Card>
          ))}
        </div>
      </div>

      <div className="mt-8">
        <Card className="flex flex-col h-[300px]">
            <CardHeader className="py-3 px-4 border-b bg-muted/20">
                <div className="flex items-center gap-2">
                    <Clipboard className="h-4 w-4 text-muted-foreground" />
                    <CardTitle className="text-sm font-medium">System Logs</CardTitle>
                </div>
            </CardHeader>
            <CardContent className="flex-1 p-0 overflow-hidden relative">
                 <div className="absolute inset-0 overflow-y-auto p-4 font-mono text-xs space-y-1">
                    {logs.length === 0 && <div className="text-muted-foreground italic">Waiting for logs...</div>}
                    {logs.map((log, i) => (
                        <div key={i} className="whitespace-pre-wrap break-all border-b border-border/40 pb-0.5 mb-0.5 last:border-0">{log}</div>
                    ))}
                    <div ref={logsEndRef} />
                 </div>
            </CardContent>
        </Card>
      </div>

    </div>
  )
}

export default App
