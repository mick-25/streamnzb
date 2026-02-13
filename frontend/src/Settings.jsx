import React, { useEffect, useState, useRef } from 'react'
import { useForm, useFieldArray } from 'react-hook-form'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from "@/components/ui/dialog"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { Trash2, Plus, Loader2, RotateCcw, Info, AlertCircle, Check, Key, Copy, AlertTriangle } from "lucide-react"
import { FiltersSection } from "@/components/FiltersSection"
import { SortingSection } from "@/components/SortingSection"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { 
  DropdownMenu, 
  DropdownMenuContent, 
  DropdownMenuItem, 
  DropdownMenuTrigger 
} from "@/components/ui/dropdown-menu"
import { ChevronDown, Menu } from "lucide-react"
import { IndexerSettings } from "@/components/IndexerSettings"
import { ProviderSettings } from "@/components/ProviderSettings"
import DeviceManagement from "@/components/DeviceManagement"


// Shown under a setting when an env var will overwrite it on restart
function EnvOverrideNote({ show }) {
  if (!show) return null
  return (
    <p className="text-xs text-amber-600 dark:text-amber-500 flex items-center gap-1 mt-1">
      <AlertTriangle className="h-3.5 w-3 shrink-0" />
      Overwritten by environment variable on restart.
    </p>
  )
}

function Settings({ initialConfig, sendCommand, saveStatus, isSaving, onClose, adminToken }) {
  const [loading, setLoading] = useState(!initialConfig)
  const [activeTab, setActiveTab] = useState("general")
  const [hasUnsavedChanges, setHasUnsavedChanges] = useState(false)
  const [initialFormValues, setInitialFormValues] = useState(null)
  const deviceManagementRef = useRef(null)
  const [adminPassword, setAdminPassword] = useState('')
  const [adminPasswordError, setAdminPasswordError] = useState('')
  const [adminPasswordSuccess, setAdminPasswordSuccess] = useState('')
  const [adminPasswordLoading, setAdminPasswordLoading] = useState(false)
  const [copiedManifestUrl, setCopiedManifestUrl] = useState(false)

  const form = useForm({
    defaultValues: {
      addon_port: 7000,
      addon_base_url: '',
      log_level: 'INFO',
      proxy_enabled: false,
      proxy_port: 119,
      proxy_host: '',
      proxy_auth_user: '',
      proxy_auth_pass: '',
      cache_ttl_seconds: 300,
      validation_sample_size: 5,
      max_streams: 6,
      providers: [],
      indexers: [],
      filters: {
        allowed_qualities: [],
        blocked_qualities: [],
        min_resolution: '',
        max_resolution: '',
        allowed_codecs: [],
        blocked_codecs: [],
        required_audio: [],
        allowed_audio: [],
        min_channels: '',
        require_hdr: false,
        allowed_hdr: [],
        blocked_hdr: [],
        block_sdr: false,
        required_languages: [],
        allowed_languages: [],
        block_dubbed: false,
        block_cam: false,
        require_proper: false,
        allow_repack: true,
        block_hardcoded: false,
        min_bit_depth: '',
        min_size_gb: 0,
        max_size_gb: 0,
        blocked_groups: []
      },
      sorting: {
        resolution_weights: {
          '4k': 4000000,
          '1080p': 3000000,
          '720p': 2000000,
          'sd': 1000000
        },
        codec_weights: {
          'HEVC': 1000,
          'x265': 1000,
          'x264': 500,
          'AVC': 500
        },
        audio_weights: {
          'Atmos': 1500,
          'TrueHD': 1200,
          'DTS-HD': 1000,
          'DTS-X': 1000,
          'DTS': 500,
          'DD+': 400,
          'DD': 300,
          'AC3': 200,
          '5.1': 500,
          '7.1': 1000
        },
        quality_weights: {
          'BluRay': 2000,
          'WEB-DL': 1500,
          'WEBRip': 1200,
          'HDTV': 1000,
          'Blu-ray': 2000
        },
        visual_tag_weights: {
          'DV': 1500,
          'HDR10+': 1200,
          'HDR': 1000,
          '3D': 800
        },
        grab_weight: 0.5,
        age_weight: 1.0,
        preferred_groups: [],
        preferred_languages: []
      }
    }
  })

  // Keys that have env var overrides (overwritten on restart) - for showing warnings
  const envOverrides = initialConfig?.env_overrides ?? []

  // Destructure for easy access
  const { control, handleSubmit, reset, setError, formState, setValue, watch, getValues } = form
  const { fields, append, remove } = useFieldArray({
    control,
    name: 'providers'
  })
  
  const { fields: indexerFields, append: appendIndexer, remove: removeIndexer } = useFieldArray({
    control,
    name: 'indexers'
  })

  useEffect(() => {
    if (initialConfig) {
      // Default sorting config
      const defaultSorting = {
        resolution_weights: {
          '4k': 4000000,
          '1080p': 3000000,
          '720p': 2000000,
          'sd': 1000000
        },
        codec_weights: {
          'HEVC': 1000,
          'x265': 1000,
          'x264': 500,
          'AVC': 500
        },
        audio_weights: {
          'Atmos': 1500,
          'TrueHD': 1200,
          'DTS-HD': 1000,
          'DTS-X': 1000,
          'DTS': 500,
          'DD+': 400,
          'DD': 300,
          'AC3': 200,
          '5.1': 500,
          '7.1': 1000
        },
        quality_weights: {
          'BluRay': 2000,
          'WEB-DL': 1500,
          'WEBRip': 1200,
          'HDTV': 1000,
          'Blu-ray': 2000
        },
        visual_tag_weights: {
          'DV': 1500,
          'HDR10+': 1200,
          'HDR': 1000,
          '3D': 800
        },
        grab_weight: 0.5,
        age_weight: 1.0,
        preferred_groups: [],
        preferred_languages: []
      }

      // Default filters config
      const defaultFilters = {
        allowed_qualities: [],
        blocked_qualities: [],
        min_resolution: '',
        max_resolution: '',
        allowed_codecs: [],
        blocked_codecs: [],
        required_audio: [],
        allowed_audio: [],
        min_channels: '',
        require_hdr: false,
        allowed_hdr: [],
        blocked_hdr: [],
        block_sdr: false,
        required_languages: [],
        allowed_languages: [],
        block_dubbed: false,
        block_cam: false,
        require_proper: false,
        allow_repack: true,
        block_hardcoded: false,
        min_bit_depth: '',
        min_size_gb: 0,
        max_size_gb: 0,
        blocked_groups: []
      }

      const { env_overrides: _envOverrides, ...configForForm } = initialConfig
      const formattedData = {
        ...configForForm,
        addon_port: Number(initialConfig.addon_port),
        proxy_port: Number(initialConfig.proxy_port),
        cache_ttl_seconds: Number(initialConfig.cache_ttl_seconds),
        validation_sample_size: Number(initialConfig.validation_sample_size),
        max_concurrent_validations: Number(initialConfig.max_concurrent_validations),
        providers: initialConfig.providers?.map(p => ({
          ...p,
          port: Number(p.port),
          connections: Number(p.connections)
        })) || [],
        indexers: initialConfig.indexers?.map(idx => ({
          ...idx,
          api_path: idx.api_path || '/api',
          api_hits_day: Number(idx.api_hits_day || 0),
          downloads_day: Number(idx.downloads_day || 0),
          username: idx.username || '',
          password: idx.password || ''
        })) || [],
        // Merge sorting config with defaults (deep merge)
        sorting: {
          ...defaultSorting,
          ...(initialConfig.sorting || {}),
          resolution_weights: {
            ...defaultSorting.resolution_weights,
            ...(initialConfig.sorting?.resolution_weights || {})
          },
          codec_weights: {
            ...defaultSorting.codec_weights,
            ...(initialConfig.sorting?.codec_weights || {})
          },
          audio_weights: {
            ...defaultSorting.audio_weights,
            ...(initialConfig.sorting?.audio_weights || {})
          },
          quality_weights: {
            ...defaultSorting.quality_weights,
            ...(initialConfig.sorting?.quality_weights || {})
          },
          visual_tag_weights: {
            ...defaultSorting.visual_tag_weights,
            ...(initialConfig.sorting?.visual_tag_weights || {})
          },
          grab_weight: initialConfig.sorting?.grab_weight ?? defaultSorting.grab_weight,
          age_weight: initialConfig.sorting?.age_weight ?? defaultSorting.age_weight,
          preferred_groups: initialConfig.sorting?.preferred_groups || defaultSorting.preferred_groups,
          preferred_languages: initialConfig.sorting?.preferred_languages || defaultSorting.preferred_languages
        },
        // Merge filters config with defaults (deep merge)
        filters: {
          ...defaultFilters,
          ...(initialConfig.filters || {}),
          allowed_qualities: initialConfig.filters?.allowed_qualities || defaultFilters.allowed_qualities,
          blocked_qualities: initialConfig.filters?.blocked_qualities || defaultFilters.blocked_qualities,
          allowed_codecs: initialConfig.filters?.allowed_codecs || defaultFilters.allowed_codecs,
          blocked_codecs: initialConfig.filters?.blocked_codecs || defaultFilters.blocked_codecs,
          required_audio: initialConfig.filters?.required_audio || defaultFilters.required_audio,
          allowed_audio: initialConfig.filters?.allowed_audio || defaultFilters.allowed_audio,
          required_languages: initialConfig.filters?.required_languages || defaultFilters.required_languages,
          allowed_languages: initialConfig.filters?.allowed_languages || defaultFilters.allowed_languages,
          allowed_hdr: initialConfig.filters?.allowed_hdr || defaultFilters.allowed_hdr,
          blocked_hdr: initialConfig.filters?.blocked_hdr || defaultFilters.blocked_hdr,
          blocked_groups: initialConfig.filters?.blocked_groups || defaultFilters.blocked_groups
        }
      }
      reset(formattedData)
      setInitialFormValues(JSON.stringify(formattedData))
      setHasUnsavedChanges(false)
      setLoading(false)
    }
  }, [initialConfig, reset])

  // Track form changes
  useEffect(() => {
    const subscription = watch((value) => {
      const currentValues = JSON.stringify(value)
      
      if (initialFormValues && currentValues !== initialFormValues) {
        setHasUnsavedChanges(true)
      } else {
        setHasUnsavedChanges(false)
      }
    })
    return () => subscription.unsubscribe()
  }, [watch, initialFormValues])

  // Map backend errors to form fields
  useEffect(() => {
      if (saveStatus.errors) {
          Object.keys(saveStatus.errors).forEach(key => {
              setError(key, { type: 'server', message: saveStatus.errors[key] });
          });
      }
  }, [saveStatus.errors, setError]);

  const onSubmit = async (data) => {
    try {
      // Save device configs via WebSocket (they're stored in state.json, not config.json)
      if (deviceManagementRef.current) {
        const deviceConfigs = deviceManagementRef.current.getDeviceConfigs()
        if (Object.keys(deviceConfigs).length > 0) {
          // Filter out admin and prepare configs for WebSocket
          const configsToSave = {}
          for (const [username, deviceConfig] of Object.entries(deviceConfigs)) {
            if (username === 'admin' || !deviceConfig) continue
            configsToSave[username] = {
              filters: deviceConfig.filters || {},
              sorting: deviceConfig.sorting || {}
            }
          }
          if (Object.keys(configsToSave).length > 0) {
            sendCommand('save_user_configs', configsToSave)
          }
        }
      }
      
      // Recursive trim function
      const trimData = (obj) => {
        if (typeof obj !== 'object' || obj === null) return obj;
        
        if (Array.isArray(obj)) {
          return obj.map(item => trimData(item));
        }
        
        const newObj = {};
        for (const key in obj) {
          if (typeof obj[key] === 'string') {
            newObj[key] = obj[key].trim();
          } else if (typeof obj[key] === 'object') {
            newObj[key] = trimData(obj[key]);
          } else {
            newObj[key] = obj[key];
          }
        }
        return newObj;
      };

      const trimmedData = trimData(data);
      sendCommand('save_config', trimmedData)
      setHasUnsavedChanges(false)
      setInitialFormValues(JSON.stringify(trimmedData))
    } catch (error) {
      console.error('Error saving configuration:', error)
      setError('Failed to save configuration: ' + error.message)
    }
  }

  const handleClose = (open) => {
    if (!open && hasUnsavedChanges) {
      if (confirm('You have unsaved changes. Are you sure you want to close without saving?')) {
        onClose()
        setHasUnsavedChanges(false)
      }
    } else if (!open) {
      onClose()
    }
  }

  if (loading) return null
  
  return (
    <Dialog open={true} onOpenChange={handleClose}>
      <DialogContent className="max-w-[90vw] h-[90vh] flex flex-col p-0 gap-0 overflow-hidden [&>*]:overflow-visible">
        <DialogHeader className="p-6 pb-4 border-b shrink-0">
          <DialogTitle>Configuration</DialogTitle>
          <DialogDescription>
            Configure your indexers and Usenet providers.
          </DialogDescription>
        </DialogHeader>

        <Tabs value={activeTab} onValueChange={setActiveTab} className="w-full flex flex-col flex-1 min-h-0">
          {/* Fixed Tabs Navigation */}
          <div className="px-6 pt-4 pb-2 border-b shrink-0">
            {/* Desktop Tabs */}
            <div className="hidden md:block">
              <TabsList className="flex w-full bg-muted/50 p-1">
                <TabsTrigger value="general" className="flex-1">General</TabsTrigger>
                <TabsTrigger value="indexers" className="flex-1">Indexers</TabsTrigger>
                <TabsTrigger value="providers" className="flex-1">Providers</TabsTrigger>
                <TabsTrigger value="filters" className="flex-1">Filters</TabsTrigger>
                <TabsTrigger value="sorting" className="flex-1">Sorting</TabsTrigger>
                <TabsTrigger value="devices" className="flex-1">Devices</TabsTrigger>
                <TabsTrigger value="advanced" className="flex-1">Advanced</TabsTrigger>
              </TabsList>
            </div>

            {/* Mobile Navigation Dropdown */}
            <div className="md:hidden">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" className="w-full justify-between bg-muted/30">
                    <div className="flex items-center gap-2">
                      <Menu className="h-4 w-4 text-muted-foreground" />
                      <span className="capitalize">{activeTab}</span>
                    </div>
                    <ChevronDown className="h-4 w-4 opacity-50" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent className="w-[calc(100vw-5rem)]">
                  <DropdownMenuItem onClick={() => setActiveTab("general")}>General</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("indexers")}>Indexers</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("providers")}>Providers</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("filters")}>Filters</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("sorting")}>Sorting</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("devices")}>Devices</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => setActiveTab("advanced")}>Advanced</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>

          <ScrollArea className="flex-1 min-h-0 w-full">
            <div className="p-6">
              <Form {...form}>
                <form onSubmit={handleSubmit(onSubmit)}>
                  <TabsContent value="general" className="space-y-6 mt-0">
                                {/* Addon Settings */}
                                <Card>
                                    <CardHeader>
                                        <CardTitle className="text-lg">Addon Settings</CardTitle>
                                        <CardDescription>Configure how the Stremio addon listens and is accessed.</CardDescription>
                                    </CardHeader>
                                    <CardContent className="grid gap-4">
                                        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                            <FormField
                                                control={control}
                                                name="addon_base_url"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Base URL</FormLabel>
                                                        <FormControl>
                                                            <Input placeholder="http://localhost:7000" {...field} />
                                                        </FormControl>
                                                        <FormMessage />
                                                        <EnvOverrideNote show={envOverrides.includes('addon_base_url')} />
                                                    </FormItem>
                                                )}
                                            />
                                            <FormField
                                                control={control}
                                                name="addon_port"
                                                render={({ field }) => (
                                                    <FormItem>
                                                        <FormLabel>Port (Requires Restart)</FormLabel>
                                                        <FormControl>
                                                            <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                        </FormControl>
                                                        <FormMessage />
                                                        <EnvOverrideNote show={envOverrides.includes('addon_port')} />
                                                    </FormItem>
                                                )}
                                            />
                                        </div>
                                        
                                        {/* Admin Manifest URL */}
                                        {adminToken && (() => {
                                            const baseUrl = watch('addon_base_url') || initialConfig?.addon_base_url || window.location.origin
                                            const cleanBaseUrl = baseUrl.replace(/\/$/, '')
                                            const manifestUrl = `${cleanBaseUrl}/${adminToken}/manifest.json`
                                            
                                            return (
                                                <div className="space-y-2">
                                                    <Label>Admin Stremio URL</Label>
                                                    <div className="flex items-center gap-2">
                                                        <code className="text-xs bg-muted px-2 py-1 rounded flex-1 truncate">
                                                            {manifestUrl}
                                                        </code>
                                                        <Button
                                                            type="button"
                                                            variant="ghost"
                                                            size="sm"
                                                            onClick={() => {
                                                                navigator.clipboard.writeText(manifestUrl)
                                                                setCopiedManifestUrl(true)
                                                                setTimeout(() => setCopiedManifestUrl(false), 2000)
                                                            }}
                                                            className="h-7"
                                                            title="Copy manifest URL"
                                                        >
                                                            {copiedManifestUrl ? (
                                                                <Check className="h-3 w-3" />
                                                            ) : (
                                                                <Copy className="h-3 w-3" />
                                                            )}
                                                        </Button>
                                                    </div>
                                                </div>
                                            )
                                        })()}
                                        
                                        <div className="space-y-2">
                                            <Label>Admin Password</Label>
                                            <div className="flex gap-2">
                                                <Input
                                                    type="password"
                                                    placeholder="Enter new admin password"
                                                    value={adminPassword}
                                                    onChange={(e) => {
                                                        setAdminPassword(e.target.value)
                                                        setAdminPasswordError('')
                                                        setAdminPasswordSuccess('')
                                                    }}
                                                    disabled={adminPasswordLoading}
                                                />
                                                <Button
                                                    type="button"
                                                    onClick={() => {
                                                        if (!adminPassword) {
                                                            setAdminPasswordError('Password cannot be empty')
                                                            return
                                                        }
                                                        
                                                        setAdminPasswordError('')
                                                        setAdminPasswordSuccess('')
                                                        setAdminPasswordLoading(true)

                                                        // Store previous callback to restore if needed
                                                        const previousCallback = window.deviceActionCallback

                                                        // Set our callback
                                                        window.deviceActionCallback = (payload) => {
                                                            // Check if this looks like a password update response
                                                            // Password update has 'success' or 'error', device actions have 'token' or 'user'
                                                            const isPasswordResponse = payload.success !== undefined || 
                                                                                          (payload.error && !payload.token && !payload.user)
                                                            
                                                            if (isPasswordResponse) {
                                                                setAdminPasswordLoading(false)
                                                                if (payload.error) {
                                                                    setAdminPasswordError(payload.error)
                                                                } else {
                                                                    setAdminPasswordSuccess('Password updated successfully')
                                                                    setAdminPassword('')
                                                                    setTimeout(() => setAdminPasswordSuccess(''), 3000)
                                                                }
                                                                // Restore previous callback if it existed
                                                                if (previousCallback) {
                                                                    window.deviceActionCallback = previousCallback
                                                                } else {
                                                                    delete window.deviceActionCallback
                                                                }
                                                            } else if (previousCallback) {
                                                                // Not our response, pass to previous callback
                                                                previousCallback(payload)
                                                            }
                                                        }

                                                        sendCommand('update_password', { username: 'admin', password: adminPassword })
                                                    }}
                                                    disabled={adminPasswordLoading || !adminPassword}
                                                >
                                                    {adminPasswordLoading ? (
                                                        <>
                                                            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                                                            Updating...
                                                        </>
                                                    ) : (
                                                        <>
                                                            <Key className="mr-2 h-4 w-4" />
                                                            Change Password
                                                        </>
                                                    )}
                                                </Button>
                                            </div>
                                            {adminPasswordError && (
                                                <div className="flex items-center gap-2 p-2 text-sm text-destructive bg-destructive/10 rounded-md">
                                                    <AlertCircle className="h-4 w-4" />
                                                    <span>{adminPasswordError}</span>
                                                </div>
                                            )}
                                            {adminPasswordSuccess && (
                                                <div className="flex items-center gap-2 p-2 text-sm text-green-600 bg-green-50 rounded-md">
                                                    <Check className="h-4 w-4" />
                                                    <span>{adminPasswordSuccess}</span>
                                                </div>
                                            )}
                                        </div>
                                    </CardContent>
                                </Card>

                                {/* NNTP Proxy Server */}
                                <Card>
                                    <CardHeader>
                                        <CardTitle className="text-lg">NNTP Proxy Server</CardTitle>
                                        <CardDescription>Allow other apps (SABnzbd, NZBGet) to use StreamNZB as a localized news server.</CardDescription>
                                        {(envOverrides.includes('proxy_enabled') || envOverrides.includes('proxy_port') || envOverrides.includes('proxy_host') || envOverrides.includes('proxy_auth_user') || envOverrides.includes('proxy_auth_pass')) && (
                                            <p className="text-xs text-amber-600 dark:text-amber-500 flex items-center gap-1 mt-1">
                                                <AlertTriangle className="h-3.5 w-3 shrink-0" />
                                                Some settings overwritten by environment variables (NNTP_PROXY_*) on restart.
                                            </p>
                                        )}
                                    </CardHeader>
                                    <CardContent className="grid gap-4">
                                        <FormField
                                            control={control}
                                            name="proxy_enabled"
                                            render={({ field }) => (
                                                <FormItem className="flex flex-row items-center justify-between rounded-lg border p-4">
                                                    <div className="space-y-0.5">
                                                        <FormLabel className="text-base">Enable Proxy</FormLabel>
                                                    </div>
                                                    <FormControl>
                                                        <Checkbox
                                                            checked={field.value}
                                                            onCheckedChange={field.onChange}
                                                        />
                                                    </FormControl>
                                                </FormItem>
                                            )}
                                        />
                                        {form.watch('proxy_enabled') && (
                                            <>
                                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                                    <FormField
                                                        control={control}
                                                        name="proxy_host"
                                                        render={({ field }) => (
                                                            <FormItem>
                                                                <FormLabel>Bind Host</FormLabel>
                                                                <FormControl>
                                                                    <Input placeholder="0.0.0.0" {...field} />
                                                                </FormControl>
                                                                <FormMessage />
                                                            </FormItem>
                                                        )}
                                                    />
                                                    <FormField
                                                        control={control}
                                                        name="proxy_port"
                                                        render={({ field }) => (
                                                            <FormItem>
                                                                <FormLabel>Port</FormLabel>
                                                                <FormControl>
                                                                    <Input type="number" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                                </FormControl>
                                                                <FormMessage />
                                                            </FormItem>
                                                        )}
                                                    />
                                                </div>
                                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                                    <FormField
                                                        control={control}
                                                        name="proxy_auth_user"
                                                        render={({ field }) => (
                                                            <FormItem>
                                                                <FormLabel>Proxy Username</FormLabel>
                                                                <FormControl>
                                                                    <Input {...field} />
                                                                </FormControl>
                                                                <FormMessage />
                                                            </FormItem>
                                                        )}
                                                    />
                                                    <FormField
                                                        control={control}
                                                        name="proxy_auth_pass"
                                                        render={({ field }) => (
                                                            <FormItem>
                                                                <FormLabel>Proxy Password</FormLabel>
                                                                <FormControl>
                                                                    <PasswordInput {...field} />
                                                                </FormControl>
                                                                <FormMessage />
                                                            </FormItem>
                                                        )}
                                                    />
                                                </div>
                                            </>
                                        )}
                                    </CardContent>
                                </Card>
                            </TabsContent>

                            <TabsContent value="indexers" className="space-y-6">
                                {envOverrides.includes('indexers') && (
                                    <div className="rounded-lg border border-amber-200 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40 p-3">
                                        <p className="text-sm text-amber-800 dark:text-amber-200 flex items-center gap-2">
                                            <AlertTriangle className="h-4 w-4 shrink-0" />
                                            Indexer list is overwritten by environment variables (INDEXER_1_*, etc.) on restart.
                                        </p>
                                    </div>
                                )}
                                <IndexerSettings
                                    control={control}
                                    indexerFields={indexerFields}
                                    appendIndexer={appendIndexer}
                                    removeIndexer={removeIndexer}
                                    watch={watch}
                                    setValue={setValue}
                                />
                            </TabsContent>


                            <TabsContent value="advanced" className="space-y-6">
                        {/* Advanced Settings */}
                        <Card>
                            <CardHeader>
                                <CardTitle className="text-lg">Advanced Settings</CardTitle>
                            </CardHeader>
                            <CardContent>
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                                     <FormField
                                        control={control}
                                        name="log_level"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel>Log Level</FormLabel>
                                                <div className="relative w-full">
                                                    <select 
                                                        className="flex h-10 w-full items-center justify-between rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
                                                        {...field}
                                                    >
                                                        <option value="DEBUG">DEBUG</option>
                                                        <option value="INFO">INFO</option>
                                                        <option value="WARN">WARN</option>
                                                        <option value="ERROR">ERROR</option>
                                                    </select>
                                                </div>
                                                <FormMessage />
                                                <EnvOverrideNote show={envOverrides.includes('log_level')} />
                                            </FormItem>
                                        )}
                                    />
                                    <FormField
                                        control={control}
                                        name="cache_ttl_seconds"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel className="flex items-center gap-2">
                                                    Cache TTL (seconds)
                                                    <TooltipProvider>
                                                        <Tooltip>
                                                            <TooltipTrigger asChild>
                                                                <Info className="h-4 w-4 text-muted-foreground cursor-help" />
                                                            </TooltipTrigger>
                                                            <TooltipContent className="max-w-xs">
                                                                <p>How long to cache validation results. Cached results avoid re-checking the same NZB files repeatedly, improving performance.</p>
                                                            </TooltipContent>
                                                        </Tooltip>
                                                    </TooltipProvider>
                                                </FormLabel>
                                                <FormControl>
                                                    <Input type="number" min="60" max="3600" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormDescription>
                                                    Cache duration in seconds (default: 300)
                                                </FormDescription>
                                                <FormMessage />
                                                <EnvOverrideNote show={envOverrides.includes('cache_ttl_seconds')} />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-4">
                                    <FormField
                                        control={control}
                                        name="validation_sample_size"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel className="flex items-center gap-2">
                                                    Validation Sample Size
                                                    <TooltipProvider>
                                                        <Tooltip>
                                                            <TooltipTrigger asChild>
                                                                <Info className="h-4 w-4 text-muted-foreground cursor-help" />
                                                            </TooltipTrigger>
                                                            <TooltipContent className="max-w-xs">
                                                                <p>Number of segments to check from each NZB file. Samples first, last, and evenly distributed segments. Higher values = more accurate validation but slower.</p>
                                                            </TooltipContent>
                                                        </Tooltip>
                                                    </TooltipProvider>
                                                </FormLabel>
                                                <FormControl>
                                                    <Input type="number" min="1" max="20" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormDescription>
                                                    Segments to check per NZB (default: 5)
                                                </FormDescription>
                                                <FormMessage />
                                                <EnvOverrideNote show={envOverrides.includes('validation_sample_size')} />
                                            </FormItem>
                                        )}
                                    />

                                    <FormField
                                        control={control}
                                        name="max_streams"
                                        render={({ field }) => (
                                            <FormItem>
                                                <FormLabel className="flex items-center gap-2">
                                                    Max Streams
                                                    <TooltipProvider>
                                                        <Tooltip>
                                                            <TooltipTrigger asChild>
                                                                <Info className="h-4 w-4 text-muted-foreground cursor-help" />
                                                            </TooltipTrigger>
                                                            <TooltipContent className="max-w-xs">
                                                                <p>Maximum number of successful streams to return per search. The system will validate up to 2x this number of releases to find healthy ones. Higher values provide more options but take longer.</p>
                                                            </TooltipContent>
                                                        </Tooltip>
                                                    </TooltipProvider>
                                                </FormLabel>
                                                <FormControl>
                                                    <Input type="number" min="1" max="20" {...field} onChange={e => field.onChange(e.target.valueAsNumber)} />
                                                </FormControl>
                                                <FormDescription>
                                                    Number of streams to return (default: 6)
                                                </FormDescription>
                                                <FormMessage />
                                            </FormItem>
                                        )}
                                    />
                                </div>
                            </CardContent>
                        </Card>
                            </TabsContent>

                            <TabsContent value="providers" className="space-y-6">
                                {envOverrides.includes('providers') && (
                                    <div className="rounded-lg border border-amber-200 bg-amber-50 dark:border-amber-900 dark:bg-amber-950/40 p-3">
                                        <p className="text-sm text-amber-800 dark:text-amber-200 flex items-center gap-2">
                                            <AlertTriangle className="h-4 w-4 shrink-0" />
                                            Provider list is overwritten by environment variables (PROVIDER_1_*, etc.) on restart.
                                        </p>
                                    </div>
                                )}
                                <ProviderSettings
                                    control={control}
                                    fields={fields}
                                    append={append}
                                    remove={remove}
                                    watch={watch}
                                />
                            </TabsContent>

                            <TabsContent value="filters" className="space-y-6">
                        <FiltersSection control={control} watch={form.watch} />
                            </TabsContent>

                            <TabsContent value="sorting" className="space-y-6">
                        <SortingSection control={control} watch={form.watch} />
                            </TabsContent>

                            <TabsContent value="devices" className="space-y-6">
                                <DeviceManagement 
                                  ref={deviceManagementRef}
                                  globalFilters={getValues('filters')}
                                  globalSorting={getValues('sorting')}
                                  sendCommand={sendCommand}
                                  ws={window.ws}
                                />
                            </TabsContent>
                </form>
              </Form>
            </div>
          </ScrollArea>
        </Tabs>

        <DialogFooter className="p-6 pt-4 border-t gap-2 sm:justify-between shrink-0">
           <div className={`flex items-center text-sm ${saveStatus.type === 'error' ? 'text-destructive' : saveStatus.type === 'success' ? 'text-green-500' : 'text-muted-foreground'}`}>
              {saveStatus.msg}
           </div>
           <div className="flex gap-2">
              <Button type="button" variant="destructive" onClick={() => {
                  if (confirm('Are you sure you want to restart StreamNZB?')) {
                      // Calculate new URL based on current form values (which should be saved)
                      const currentPort = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
                      const newPort = form.getValues('addon_port').toString();
                      
                      // Check if we need to redirect
                      // Note: We use existing hostname
                      
                      const hostname = window.location.hostname;
                      const protocol = window.location.protocol;
                      
                      const newUrl = `${protocol}//${hostname}:${newPort}/`;
                      
                      sendCommand('restart', {})
                      
                      // If URL changed, redirect after a delay
                      if (newPort !== currentPort) {
                           // Use a slightly longer timeout to allow the backend to receive the command and die
                           // and hopefully start coming back up.
                          setTimeout(() => {
                              window.location.href = newUrl;
                          }, 3000); 
                      }
                  }
              }}>
                 <RotateCcw className="mr-2 h-4 w-4" />
                 Restart App
              </Button>
              <Button type="button" onClick={handleSubmit(onSubmit)} disabled={isSaving || formState.isSubmitting}>
                 {(isSaving || formState.isSubmitting) && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                 Save Changes
              </Button>
           </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default Settings


